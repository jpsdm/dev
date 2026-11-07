//! Small helpers shared by every command: folders, errors, dates, downloads.

use std::cmp::Ordering;
use std::env;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

pub type Res<T> = Result<T, String>;

pub const VERSION: &str = env!("CARGO_PKG_VERSION");
/// Set by the release workflow (DEV_BUILD_DATE=YYYY-MM-DD); local builds show the date below.
pub const BUILD_DATE: &str = match option_env!("DEV_BUILD_DATE") {
    Some(d) => d,
    None => "2026-11-06",
};
pub const NAME: &str = "dev";

pub fn err<T>(msg: impl Into<String>) -> Res<T> {
    Err(msg.into())
}

pub fn home() -> PathBuf {
    let (first, second) = if cfg!(windows) { ("USERPROFILE", "HOME") } else { ("HOME", "USERPROFILE") };
    env::var_os(first)
        .filter(|v| !v.is_empty())
        .or_else(|| env::var_os(second))
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
}

fn env_dir(name: &str) -> Option<PathBuf> {
    env::var_os(name).filter(|v| !v.is_empty()).map(PathBuf::from)
}

/// Data folder: %LOCALAPPDATA%\dev on Windows, $XDG_DATA_HOME/dev or ~/.local/share/dev elsewhere
/// (the original's layout, install-script template src\cli\generate\install_script.rs).
pub fn data_dir() -> PathBuf {
    env_dir("DEV_DATA_DIR").unwrap_or_else(|| {
        if cfg!(windows) {
            if let Some(p) = env_dir("LOCALAPPDATA") {
                return p.join(NAME);
            }
        }
        env_dir("XDG_DATA_HOME")
            .unwrap_or_else(|| home().join(".local").join("share"))
            .join(NAME)
    })
}

/// Where the installer puts the dev program itself.
pub fn self_bin_dir() -> PathBuf {
    env_dir("DEV_INSTALL_DIR").unwrap_or_else(|| {
        if cfg!(windows) {
            data_dir().join("bin")
        } else {
            home().join(".local").join("bin")
        }
    })
}

/// "windows", "linux" or "macos".
pub fn os_name() -> &'static str {
    std::env::consts::OS
}

/// "x86_64" or "aarch64" (others pass through).
pub fn cpu_arch() -> &'static str {
    std::env::consts::ARCH
}

/// The system curl / tar programs (curl.exe and tar.exe ship with Windows 10+).
pub fn curl_program() -> &'static str {
    if cfg!(windows) {
        "curl.exe"
    } else {
        "curl"
    }
}

pub fn tar_program() -> &'static str {
    if cfg!(windows) {
        "tar.exe"
    } else {
        "tar"
    }
}

/// Converts a Windows path (C:\x\y) to the form Git Bash / MSYS shells use (/c/x/y).
pub fn to_posix_path(p: &str) -> String {
    let s = p.replace('\\', "/");
    let b = s.as_bytes();
    if b.len() >= 2 && b[1] == b':' && b[0].is_ascii_alphabetic() {
        format!("/{}{}", (b[0] as char).to_ascii_lowercase(), &s[2..])
    } else {
        s
    }
}

pub fn config_dir() -> PathBuf {
    env_dir("DEV_CONFIG_DIR").unwrap_or_else(|| {
        env_dir("XDG_CONFIG_HOME")
            .unwrap_or_else(|| home().join(".config"))
            .join(NAME)
    })
}

pub fn cache_dir() -> PathBuf {
    env_dir("DEV_CACHE_DIR").unwrap_or_else(|| data_dir().join("cache"))
}

pub fn state_dir() -> PathBuf {
    env_dir("DEV_STATE_DIR").unwrap_or_else(|| data_dir().join("state"))
}

pub fn installs_dir() -> PathBuf {
    data_dir().join("installs")
}

pub fn global_config_path() -> PathBuf {
    config_dir().join("config.toml")
}

pub fn verbose() -> bool {
    env::var("DEV_VERBOSE").map(|v| v == "1" || v == "true").unwrap_or(false)
}

/// Shows a path with the home folder written as `~`.
pub fn display_path(p: &Path) -> String {
    let h = home();
    if let Ok(rest) = p.strip_prefix(&h) {
        let r = rest.display().to_string();
        if r.is_empty() {
            "~".into()
        } else {
            format!("~\\{}", r)
        }
    } else {
        p.display().to_string()
    }
}

pub fn now_secs() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)
}

pub fn secs_of(t: SystemTime) -> u64 {
    t.duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)
}

/// Days since 1970-01-01 → (year, month, day), proleptic Gregorian calendar.
pub fn civil_from_days(z: i64) -> (i64, u32, u32) {
    let z = z + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    (if m <= 2 { y + 1 } else { y }, m, d)
}

/// (year, month, day) → days since 1970-01-01 (inverse of `civil_from_days`).
pub fn days_from_civil(y: i64, m: u32, d: u32) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = y - era * 400;
    let mp = if m > 2 { m - 3 } else { m + 9 } as i64;
    let doy = (153 * mp + 2) / 5 + d as i64 - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

/// `2026-11-06T12:34:56Z` (GitHub's timestamps, UTC) → seconds since 1970.
pub fn parse_utc_timestamp(s: &str) -> Option<u64> {
    let s = s.trim();
    let num = |a: usize, b: usize| s.get(a..b)?.parse::<i64>().ok();
    let (y, mo, d) = (num(0, 4)?, num(5, 7)?, num(8, 10)?);
    let (h, mi, se) = (num(11, 13).unwrap_or(0), num(14, 16).unwrap_or(0), num(17, 19).unwrap_or(0));
    let days = days_from_civil(y, mo as u32, d as u32);
    let t = days * 86_400 + h * 3600 + mi * 60 + se;
    (t >= 0).then_some(t as u64)
}

pub fn date_of(secs: u64) -> String {
    let (y, m, d) = civil_from_days((secs / 86_400) as i64);
    format!("{:04}-{:02}-{:02}", y, m, d)
}

pub fn today() -> String {
    date_of(now_secs())
}

/// "3 days ago", "5 hours ago", "just now".
pub fn ago(secs: u64) -> String {
    if secs == 0 {
        return "never".into();
    }
    let now = now_secs();
    let d = now.saturating_sub(secs);
    if d < 60 {
        "just now".into()
    } else if d < 3600 {
        format!("{} min ago", d / 60)
    } else if d < 86_400 {
        format!("{} h ago", d / 3600)
    } else {
        let days = d / 86_400;
        format!("{} day{} ago", days, if days == 1 { "" } else { "s" })
    }
}

pub fn human_size(bytes: u64) -> String {
    let units = ["B", "KB", "MB", "GB", "TB"];
    let mut v = bytes as f64;
    let mut i = 0;
    while v >= 1024.0 && i < units.len() - 1 {
        v /= 1024.0;
        i += 1;
    }
    if i == 0 {
        format!("{} B", bytes)
    } else {
        format!("{:.1} {}", v, units[i])
    }
}

/// Parses `30d`, `12h`, `2w`, `1y` or a bare number of days into seconds.
pub fn parse_duration(s: &str) -> Res<u64> {
    let s = s.trim();
    let (num, unit) = match s.char_indices().find(|(_, c)| !c.is_ascii_digit()) {
        Some((i, _)) => (&s[..i], &s[i..]),
        None => (s, "d"),
    };
    let n: u64 = num.parse().map_err(|_| format!("invalid duration: {}", s))?;
    let mult = match unit {
        "s" => 1,
        "m" | "min" => 60,
        "h" => 3600,
        "d" => 86_400,
        "w" => 7 * 86_400,
        "y" => 365 * 86_400,
        _ => return err(format!("invalid duration: {} (use a number with d, h, w or y)", s)),
    };
    Ok(n * mult)
}

/// Compares versions numerically segment by segment: 1.10.0 > 1.9.3.
pub fn version_cmp(a: &str, b: &str) -> Ordering {
    let seg = |s: &str| -> Vec<String> {
        s.trim_start_matches('v')
            .split(|c: char| c == '.' || c == '-' || c == '+' || c == '_')
            .map(|x| x.to_string())
            .collect()
    };
    let (sa, sb) = (seg(a), seg(b));
    for i in 0..sa.len().max(sb.len()) {
        match (sa.get(i), sb.get(i)) {
            (Some(x), Some(y)) => {
                let o = match (x.parse::<u64>(), y.parse::<u64>()) {
                    (Ok(p), Ok(q)) => p.cmp(&q),
                    (Ok(_), Err(_)) => Ordering::Greater,
                    (Err(_), Ok(_)) => Ordering::Less,
                    _ => x.cmp(y),
                };
                if o != Ordering::Equal {
                    return o;
                }
            }
            (Some(x), None) => {
                // 1.0.0 > 1.0.0-rc1 style: a trailing number counts as newer.
                return if x.parse::<u64>().is_ok() { Ordering::Greater } else { Ordering::Less };
            }
            (None, Some(y)) => {
                return if y.parse::<u64>().is_ok() { Ordering::Less } else { Ordering::Greater };
            }
            (None, None) => {}
        }
    }
    Ordering::Equal
}

/// Whether `version` satisfies a prefix request such as `24` or `1.22`.
pub fn version_matches(version: &str, prefix: &str) -> bool {
    let v = version.trim_start_matches('v');
    let p = prefix.trim_start_matches('v');
    v == p || v.starts_with(&format!("{}.", p)) || v.starts_with(&format!("{}-", p))
}

pub fn is_prerelease(v: &str) -> bool {
    let l = v.to_ascii_lowercase();
    ["alpha", "beta", "rc", "pre", "nightly", "dev", "canary", "preview"]
        .iter()
        .any(|k| l.contains(k))
}

/// Fetches a URL with the system curl and returns its body as text.
pub fn http_get(url: &str, token_env: Option<&str>) -> Res<String> {
    http_get_with(url, token_env, None)
}

/// `http_get` that gives up after `max_secs` (for the background update check).
pub fn http_get_with(url: &str, token_env: Option<&str>, max_secs: Option<u32>) -> Res<String> {
    let mut cmd = Command::new(curl_program());
    cmd.args(["-sSfL", "-A", "dev-cli"]);
    match max_secs {
        Some(s) => {
            cmd.arg("--max-time").arg(s.to_string());
        }
        None => {
            cmd.args(["--retry", "2"]);
        }
    }
    if let Some(var) = token_env {
        if let Ok(tok) = env::var(var) {
            if !tok.is_empty() {
                if var == "GITLAB_TOKEN" {
                    cmd.arg("-H").arg(format!("PRIVATE-TOKEN: {}", tok));
                } else {
                    cmd.arg("-H").arg(format!("Authorization: Bearer {}", tok));
                }
            }
        }
    }
    cmd.arg(url);
    let out = cmd.output().map_err(|e| format!("could not run curl: {}", e))?;
    if !out.status.success() {
        return err(format!(
            "failed to fetch {}: {}",
            url,
            String::from_utf8_lossy(&out.stderr).trim()
        ));
    }
    Ok(String::from_utf8_lossy(&out.stdout).into_owned())
}

/// Like `http_get`, with an Accept header (npm's short metadata format).
pub fn http_get_accept(url: &str, accept: &str) -> Res<String> {
    let out = Command::new(curl_program())
        .args(["-sSfL", "--compressed", "--retry", "2", "-A", "dev-cli", "-H"])
        .arg(format!("Accept: {}", accept))
        .arg(url)
        .output()
        .map_err(|e| format!("could not run curl: {}", e))?;
    if !out.status.success() {
        return err(format!("failed to fetch {}: {}", url, String::from_utf8_lossy(&out.stderr).trim()));
    }
    Ok(String::from_utf8_lossy(&out.stdout).into_owned())
}

pub fn download(url: &str, dest: &Path) -> Res<()> {
    if let Some(p) = dest.parent() {
        fs::create_dir_all(p).map_err(|e| e.to_string())?;
    }
    let status = Command::new(curl_program())
        .args(["-sSfL", "--retry", "2", "-A", "dev-cli", "-o"])
        .arg(dest)
        .arg(url)
        .status()
        .map_err(|e| format!("could not run curl: {}", e))?;
    if !status.success() {
        return err(format!("download failed: {}", url));
    }
    Ok(())
}

pub fn extract(archive: &Path, dest: &Path) -> Res<()> {
    fs::create_dir_all(dest).map_err(|e| e.to_string())?;
    let status = Command::new(tar_program())
        .arg("-xf")
        .arg(archive)
        .arg("-C")
        .arg(dest)
        .status()
        .map_err(|e| format!("could not run tar: {}", e))?;
    if !status.success() {
        return err(format!("could not unpack {}", archive.display()));
    }
    Ok(())
}

/// Reads a cached text file when it is younger than `max_age`.
pub fn read_cache(path: &Path, max_age: Duration) -> Option<String> {
    let meta = fs::metadata(path).ok()?;
    let age = SystemTime::now().duration_since(meta.modified().ok()?).ok()?;
    if age > max_age {
        return None;
    }
    fs::read_to_string(path).ok()
}

pub fn write_file(path: &Path, text: &str) -> Res<()> {
    if let Some(p) = path.parent() {
        fs::create_dir_all(p).map_err(|e| format!("{}: {}", p.display(), e))?;
    }
    fs::write(path, text).map_err(|e| format!("{}: {}", path.display(), e))
}

/// Total size of a folder (best effort, never follows links).
pub fn dir_size(p: &Path) -> u64 {
    let mut total = 0;
    let mut stack = vec![p.to_path_buf()];
    while let Some(d) = stack.pop() {
        let meta = match fs::symlink_metadata(&d) {
            Ok(m) => m,
            Err(_) => continue,
        };
        if meta.is_dir() {
            if let Ok(rd) = fs::read_dir(&d) {
                for e in rd.flatten() {
                    stack.push(e.path());
                }
            }
        } else {
            total += meta.len();
        }
    }
    total
}

/// Removes a file or a folder; clears read-only flags (git objects) when needed.
pub fn remove_path(p: &Path) -> Res<()> {
    let meta = match fs::symlink_metadata(p) {
        Ok(m) => m,
        Err(_) => return Ok(()),
    };
    let r = if meta.is_dir() { fs::remove_dir_all(p) } else { fs::remove_file(p) };
    if r.is_ok() {
        return Ok(());
    }
    clear_readonly(p);
    if meta.is_dir() {
        fs::remove_dir_all(p)
    } else {
        fs::remove_file(p)
    }
    .map_err(|e| format!("could not remove {}: {}", p.display(), e))
}

fn clear_readonly(p: &Path) {
    let mut stack = vec![p.to_path_buf()];
    while let Some(d) = stack.pop() {
        if let Ok(m) = fs::symlink_metadata(&d) {
            let mut perm = m.permissions();
            #[allow(clippy::permissions_set_readonly_false)]
            perm.set_readonly(false);
            let _ = fs::set_permissions(&d, perm);
            if m.is_dir() {
                if let Ok(rd) = fs::read_dir(&d) {
                    for e in rd.flatten() {
                        stack.push(e.path());
                    }
                }
            }
        }
    }
}

/// Asks a yes/no question on the terminal; `default` is used when stdin is closed.
pub fn confirm(question: &str, default: bool) -> bool {
    use std::io::{BufRead, IsTerminal, Write};
    if !std::io::stdin().is_terminal() {
        return default;
    }
    eprint!("{} {} ", question, if default { "[Y/n]" } else { "[y/N]" });
    let _ = std::io::stderr().flush();
    let mut line = String::new();
    if std::io::stdin().lock().read_line(&mut line).is_err() {
        return default;
    }
    match line.trim().to_ascii_lowercase().as_str() {
        "" => default,
        "y" | "yes" | "s" | "sim" => true,
        _ => false,
    }
}

pub fn info(msg: impl AsRef<str>) {
    eprintln!("{} {}", NAME, msg.as_ref());
}

pub fn warn(msg: impl AsRef<str>) {
    eprintln!("{} WARN  {}", NAME, msg.as_ref());
}

/// Finds an executable in the given PATH folders, trying the PATHEXT endings.
pub fn find_in_path(name: &str, dirs: &[PathBuf]) -> Option<PathBuf> {
    let candidate = Path::new(name);
    if candidate.components().count() > 1 && candidate.exists() {
        return Some(candidate.to_path_buf());
    }
    let exts: Vec<String> = env::var("PATHEXT")
        .unwrap_or_else(|_| ".COM;.EXE;.BAT;.CMD".into())
        .split(';')
        .filter(|s| !s.is_empty())
        .map(|s| s.to_ascii_lowercase())
        .collect();
    let has_ext = candidate.extension().is_some();
    for d in dirs {
        if has_ext {
            let p = d.join(name);
            if p.is_file() {
                return Some(p);
            }
        }
        for e in &exts {
            let p = d.join(format!("{}{}", name, e));
            if p.is_file() {
                return Some(p);
            }
        }
        if !cfg!(windows) {
            let p = d.join(name);
            if p.is_file() {
                return Some(p);
            }
        }
    }
    None
}

pub fn split_path_var(v: &str) -> Vec<PathBuf> {
    env::split_paths(v).filter(|p| !p.as_os_str().is_empty()).collect()
}
