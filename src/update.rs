//! New-version warning and `dev self-update`, from the project's GitHub releases.
//! Rules follow the original's `src\cli\version.rs` / `src\cli\self_update.rs` (see MECHANICS.md).

use crate::util::*;
use std::fs;
use std::path::{Path, PathBuf};

/// How often the warning may contact GitHub: 0x15180 seconds, stored at 0x145fbfafe.
pub const CHECK_INTERVAL_SECS: u64 = 86_400;
/// The background check gives up after this long (estimated; keeps commands quick offline).
pub const CHECK_TIMEOUT_SECS: u32 = 3;
/// Releases younger than this are skipped unless a version is named (`self_update.minimum_release_age`, default 24h).
pub const DEFAULT_MINIMUM_RELEASE_AGE: &str = "24h";
/// `self_update.repository` default: where dev's releases are published.
pub const DEFAULT_REPO: &str = "jpsdm/dev";
/// `self_update.api_url` default.
pub const DEFAULT_API_URL: &str = "https://api.github.com";
/// Install folders of package managers that own the program (FUN_1434a3010):
/// `{dir}/packages/` and `{dir}/links/` for each of these, matched on the lower-cased path with `/`.
pub const PACKAGE_MANAGER_DIRS: &[&str] = &["microsoft/winget", "program files/winget"];

fn global_setting(key: &str) -> Option<toml::Value> {
    let path = global_config_path();
    crate::config::ConfigFile::load(&path, true).ok()?.settings.get(key).cloned()
}

fn env_flag(k: &str) -> Option<bool> {
    std::env::var(k).ok().map(|v| !(v.is_empty() || v == "0" || v.eq_ignore_ascii_case("false")))
}

/// `owner/name` the releases come from: DEV_REPO, `self_update.repository`, the repository the
/// release build was made from (baked in by the workflow), else jpsdm/dev.
pub fn repo() -> Option<String> {
    std::env::var("DEV_REPO")
        .ok()
        .filter(|r| r.contains('/'))
        .or_else(|| global_setting("self_update.repository").and_then(|v| v.as_str().map(String::from)))
        .or_else(|| option_env!("DEV_REPO").map(String::from))
        .filter(|r| r.contains('/'))
        .or_else(|| Some(DEFAULT_REPO.to_string()))
}

fn api_url() -> String {
    global_setting("self_update.api_url")
        .and_then(|v| v.as_str().map(String::from))
        .unwrap_or_else(|| DEFAULT_API_URL.into())
        .trim_end_matches('/')
        .to_string()
}

fn minimum_release_age(override_age: Option<&str>) -> Res<u64> {
    let s = override_age
        .map(String::from)
        .or_else(|| std::env::var("DEV_MINIMUM_RELEASE_AGE").ok())
        .or_else(|| global_setting("self_update.minimum_release_age").and_then(|v| v.as_str().map(String::from)))
        .or_else(|| global_setting("minimum_release_age").and_then(|v| v.as_str().map(String::from)))
        .unwrap_or_else(|| DEFAULT_MINIMUM_RELEASE_AGE.into());
    if s.trim() == "0" {
        return Ok(0);
    }
    parse_duration(&s).map_err(|_| format!("invalid self-update release age: {}", s))
}

/// The release file for this computer: dev-windows-x86_64.exe, dev-linux-aarch64, dev-macos-aarch64...
pub fn asset_name() -> String {
    format!("dev-{}-{}{}", os_name(), cpu_arch(), if cfg!(windows) { ".exe" } else { "" })
}

#[derive(Clone, Debug)]
pub struct Release {
    pub version: String,
    pub url: String,
    pub published: Option<u64>,
    pub assets: Vec<(String, String)>,
}

fn parse_release(j: &serde_json::Value) -> Option<Release> {
    let tag = j["tag_name"].as_str()?;
    Some(Release {
        version: tag.trim_start_matches('v').to_string(),
        url: j["html_url"].as_str().unwrap_or("").to_string(),
        published: j["published_at"].as_str().and_then(parse_utc_timestamp),
        assets: j["assets"]
            .as_array()
            .map(|a| {
                a.iter()
                    .filter_map(|x| Some((x["name"].as_str()?.to_string(), x["browser_download_url"].as_str()?.to_string())))
                    .collect()
            })
            .unwrap_or_default(),
    })
}

/// Releases asked for per page: `{api}/repos/{repo}/releases?per_page=100&page={n}` (template 0x14c5f4878).
pub const RELEASES_PER_PAGE: usize = 100;
/// Most pages read (estimated; 1000 releases is far more than needed).
pub const MAX_RELEASE_PAGES: u32 = 10;

/// Published, non-prerelease releases, highest version first. Pages are read from 1 upward
/// (the original counts the page up by one at 0x145fc1367) until one comes back short.
fn releases(repo: &str, max_secs: Option<u32>) -> Res<Vec<Release>> {
    let mut out: Vec<Release> = vec![];
    for page in 1..=MAX_RELEASE_PAGES {
        let url = format!("{}/repos/{}/releases?per_page={}&page={}", api_url(), repo, RELEASES_PER_PAGE, page);
        let text = http_get_with(&url, Some("GITHUB_TOKEN"), max_secs)?;
        let j: serde_json::Value = serde_json::from_str(&text).map_err(|e| format!("failed to check for version: {}", e))?;
        let rows = j.as_array().cloned().unwrap_or_default();
        out.extend(
            rows.iter()
                .filter(|r| !r["draft"].as_bool().unwrap_or(false) && !r["prerelease"].as_bool().unwrap_or(false))
                .filter_map(parse_release),
        );
        if rows.len() < RELEASES_PER_PAGE {
            break;
        }
    }
    out.sort_by(|a, b| version_cmp(&b.version, &a.version));
    Ok(out)
}

fn release_by_version(repo: &str, version: &str) -> Res<Release> {
    let url = format!("{}/repos/{}/releases/tags/v{}", api_url(), repo, version.trim_start_matches('v'));
    let text = http_get(&url, Some("GITHUB_TOKEN")).map_err(|_| format!("no release v{} in {}", version.trim_start_matches('v'), repo))?;
    let j: serde_json::Value = serde_json::from_str(&text).map_err(|e| e.to_string())?;
    parse_release(&j).ok_or_else(|| format!("release v{} has no tag", version))
}

/// The highest version published at or before `now - min_age`, and the highest one skipped for
/// being younger (FUN_1471f2430: keep rows whose time <= cutoff, sort, take the last). A release
/// without a publish time is never eligible, as an index row without one is invalid there.
fn eligible(list: &[Release], min_age: u64) -> (Option<&Release>, Option<&Release>) {
    let cutoff = now_secs().saturating_sub(min_age);
    let mut skipped = None;
    for r in list {
        match r.published {
            Some(p) if p <= cutoff => return (Some(r), skipped),
            Some(_) if skipped.is_none() => skipped = Some(r),
            _ => {}
        }
    }
    (None, skipped)
}

/// The original's error when nothing is old enough (string 0x14c709f8f).
pub const NO_ELIGIBLE_RELEASE: &str =
    "no dev release satisfies minimum release age; lower self_update.minimum_release_age or specify a version";

fn is_newer(latest: &str) -> bool {
    version_cmp(latest, VERSION) == std::cmp::Ordering::Greater
}

/// Whether this copy may replace itself (FUN_1434a3700 / FUN_1434a3580 / FUN_1434a3010):
/// DEV_SELF_UPDATE_AVAILABLE decides when set; otherwise not when the program sits in a package
/// manager's folder (winget), checked on its path and on the path with links resolved.
pub fn self_update_available() -> bool {
    if let Some(v) = env_flag("DEV_SELF_UPDATE_AVAILABLE") {
        return v;
    }
    let me = match std::env::current_exe() {
        Ok(p) => p,
        Err(_) => return false,
    };
    if managed_path(&me) {
        return false;
    }
    match fs::canonicalize(&me) {
        Ok(real) => !managed_path(&real),
        Err(_) => true,
    }
}

fn managed_path(p: &Path) -> bool {
    let s = p.display().to_string().replace('\\', "/").to_lowercase();
    PACKAGE_MANAGER_DIRS
        .iter()
        .any(|d| s.contains(&format!("{}/packages/", d)) || s.contains(&format!("{}/links/", d)))
}

fn state_file() -> PathBuf {
    state_dir().join("update-check.json")
}

fn warning_disabled() -> bool {
    env_flag("DEV_NO_UPDATE_CHECK").unwrap_or(false)
        || env_flag("DEV_DISABLE_UPDATE_WARNING").unwrap_or(false)
        || global_setting("disable_update_warning").and_then(|v| v.as_bool()).unwrap_or(false)
}

/// After a command: at most once per 24 h, look for a newer eligible release and warn on stderr.
/// Quiet with `disable_update_warning`, in CI, or when stderr isn't a terminal.
pub fn maybe_notify() {
    use std::io::IsTerminal;
    if warning_disabled() || env_flag("CI").unwrap_or(false) || !std::io::stderr().is_terminal() {
        return;
    }
    let repo = match repo() {
        Some(r) => r,
        None => return,
    };
    let state: serde_json::Value = fs::read_to_string(state_file())
        .ok()
        .and_then(|t| serde_json::from_str(&t).ok())
        .unwrap_or_default();
    if now_secs().saturating_sub(state["checked"].as_u64().unwrap_or(0)) < CHECK_INTERVAL_SECS {
        return;
    }
    // Record the attempt first, so a slow or offline network costs at most one try a day.
    let mut latest = state["latest"].as_str().unwrap_or("").to_string();
    if let (Ok(list), Ok(age)) = (releases(&repo, Some(CHECK_TIMEOUT_SECS)), minimum_release_age(None)) {
        if let (Some(r), _) = eligible(&list, age) {
            latest = r.version.clone();
        }
    }
    let _ = write_file(&state_file(), &serde_json::json!({ "checked": now_secs(), "latest": latest }).to_string());
    if !latest.is_empty() && is_newer(&latest) {
        eprintln!();
        warn(format!("dev version {} available", latest));
        if self_update_available() {
            warn("To update, run dev self-update");
        } else {
            warn("self-update is disabled for this install; update dev the same way you installed it");
        }
    }
}

pub struct SelfUpdateArgs {
    pub version: Option<String>,
    pub check: bool,
    pub force: bool,
    pub yes: bool,
    pub minimum_release_age: Option<String>,
    pub quiet: bool,
}

/// `dev self-update [VERSION]`: install the newest eligible release (or VERSION) for this computer.
pub fn self_update(a: SelfUpdateArgs) -> Res<i32> {
    if !self_update_available() && !a.force {
        return err("self-update is disabled for this install; update dev the same way you installed it (or pass --force)");
    }
    let repo = repo().ok_or("no release repository is known; set DEV_REPO=owner/name or `dev settings set self_update.repository owner/name`")?;
    let rel = match &a.version {
        // A named version skips the minimum release age.
        Some(v) => release_by_version(&repo, v)?,
        None => {
            let list = releases(&repo, None)?;
            if list.is_empty() {
                if !a.quiet {
                    info(format!("no dev releases published yet in https://github.com/{}/releases", repo));
                }
                return Ok(0);
            }
            let age = minimum_release_age(a.minimum_release_age.as_deref())?;
            let (pick, skipped) = eligible(&list, age);
            if let Some(s) = skipped {
                if is_newer(&s.version) && !a.quiet {
                    let hours = s.published.map(|p| now_secs().saturating_sub(p) / 3600).unwrap_or(0);
                    info(format!(
                        "newer {} release {} ({} h old) ignored by minimum_release_age; pass it by name or use --minimum-release-age 0",
                        repo, s.version, hours
                    ));
                }
            }
            pick.cloned().ok_or(NO_ELIGIBLE_RELEASE)?
        }
    };
    let _ = write_file(&state_file(), &serde_json::json!({ "checked": now_secs(), "latest": rel.version }).to_string());
    let newer = is_newer(&rel.version);
    if a.version.is_none() && !newer && !a.force {
        if !a.quiet {
            info(format!("dev {} is up to date (latest release: {})", VERSION, rel.version));
        }
        return Ok(0);
    }
    if a.check {
        println!("{} → {}  {}", VERSION, rel.version, rel.url);
        return Ok(0);
    }
    if !a.yes && !confirm(&format!("Update dev {} → {}?", VERSION, rel.version), false) {
        return err("self-update cancelled; use --yes to update non-interactively");
    }
    let name = asset_name();
    let url = rel
        .assets
        .iter()
        .find(|(n, _)| *n == name)
        .map(|(_, u)| u.clone())
        .ok_or_else(|| format!("release {} has no {} for this computer", rel.version, name))?;
    let me = std::env::current_exe().map_err(|e| e.to_string())?;
    let dir = me.parent().ok_or("cannot find dev's folder")?.to_path_buf();
    let tmp = dir.join(format!(".{}.download", name));
    info(format!("self-update downloading release {}", rel.version));
    download(&url, &tmp)?;
    if let Some((_, sums_url)) = rel.assets.iter().find(|(n, _)| n == "SHA256SUMS.txt") {
        let sums = http_get(sums_url, Some("GITHUB_TOKEN"))?;
        let want = sums
            .lines()
            .find_map(|l| {
                let mut it = l.split_whitespace();
                let h = it.next()?;
                let f = it.next()?.trim_start_matches('*');
                (f == name).then(|| h.to_string())
            })
            .ok_or_else(|| format!("SHA256SUMS.txt doesn't list {}", name))?;
        let got = crate::tools::sha256_file(&tmp)?;
        if !got.eq_ignore_ascii_case(&want) {
            let _ = fs::remove_file(&tmp);
            return err(format!("checksum mismatch for {}: expected {}, got {}", name, want, got));
        }
    }
    replace_self(&me, &tmp)?;
    info(format!("dev updated {} → {}", VERSION, rel.version));
    Ok(0)
}

/// Swaps in the new program. A running program can be renamed (not deleted) on Windows, so the
/// old one is moved aside to dev.exe.old and removed by the next update.
fn replace_self(me: &Path, new: &Path) -> Res<()> {
    let old = me.with_extension(if cfg!(windows) { "exe.old" } else { "old" });
    let _ = fs::remove_file(&old);
    fs::rename(me, &old).map_err(|e| format!("could not move the running dev aside: {}", e))?;
    if let Err(e) = fs::rename(new, me) {
        let _ = fs::rename(&old, me);
        return err(format!("could not put the new dev in place: {}", e));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(me, fs::Permissions::from_mode(0o755));
    }
    if !cfg!(windows) {
        let _ = fs::remove_file(&old);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rel(v: &str, hours_ago: u64) -> Release {
        Release { version: v.into(), url: String::new(), published: Some(now_secs() - hours_ago * 3600), assets: vec![] }
    }

    #[test]
    fn asset_names() {
        let n = asset_name();
        assert!(n.starts_with("dev-"));
        assert!(n.contains(os_name()) && n.contains(cpu_arch()));
    }

    #[test]
    fn newer_versions() {
        assert!(is_newer("9999.1.1"));
        assert!(!is_newer("2000.1.1"));
        assert!(!is_newer(VERSION));
    }

    #[test]
    fn minimum_release_age_skips_young_releases() {
        let list = vec![rel("2026.11.6", 2), rel("2026.11.5", 30), rel("2026.11.4", 100)];
        let (pick, skipped) = eligible(&list, 24 * 3600);
        assert_eq!(pick.unwrap().version, "2026.11.5");
        assert_eq!(skipped.unwrap().version, "2026.11.6");
        let (pick, skipped) = eligible(&list, 0);
        assert_eq!(pick.unwrap().version, "2026.11.6");
        assert!(skipped.is_none());
        // No publish time: never eligible.
        let undated = vec![Release { version: "2026.12.1".into(), url: String::new(), published: None, assets: vec![] }, rel("2026.11.4", 100)];
        assert_eq!(eligible(&undated, 0).0.unwrap().version, "2026.11.4");
        // Nothing old enough.
        assert!(eligible(&[rel("2026.11.6", 1)], 24 * 3600).0.is_none());
    }

    #[test]
    fn package_manager_paths() {
        assert!(managed_path(Path::new(r"C:\Users\me\AppData\Local\Microsoft\WinGet\Packages\dev\dev.exe")));
        assert!(managed_path(Path::new(r"C:\Program Files\WinGet\Links\dev.exe")));
        assert!(!managed_path(Path::new(r"C:\Users\me\AppData\Local\dev\bin\dev.exe")));
    }

    #[test]
    fn timestamps() {
        assert_eq!(parse_utc_timestamp("1970-01-02T00:00:00Z"), Some(86_400));
        assert_eq!(parse_utc_timestamp("2026-11-06T09:47:45Z").map(date_of).as_deref(), Some("2026-11-06"));
    }
}
