//! Installing dev itself: copy the program into place, put it on PATH and turn it on in every
//! shell the computer has (bash, zsh, fish, PowerShell, and Git Bash on Windows).
//!
//! On Windows the same program is the installer: named `dev-setup.exe` (or run as `dev setup`)
//! it installs itself. On Linux and macOS, install.sh fetches or builds the program and runs
//! `dev setup`.

use crate::util::*;
use std::fs;
use std::path::{Path, PathBuf};

const BEGIN: &str = "# >>> dev >>>";
const END: &str = "# <<< dev <<<";

#[derive(clap::Args, Debug, Default)]
pub struct SetupArgs {
    /// Remove dev's shell setup, PATH entry and program (your tools and settings stay)
    #[arg(long)]
    pub uninstall: bool,
    /// With --uninstall: also delete installed tools, caches and settings
    #[arg(long)]
    pub purge: bool,
    /// Don't change PATH
    #[arg(long)]
    pub no_modify_path: bool,
    /// Don't touch shell startup files
    #[arg(long)]
    pub no_shells: bool,
    /// Folder to install the program into
    #[arg(long, value_name = "DIR")]
    pub dir: Option<PathBuf>,
}

fn exe_name() -> &'static str {
    if cfg!(windows) {
        "dev.exe"
    } else {
        "dev"
    }
}

/// True when this program was started as the Windows installer (`dev-setup.exe`).
pub fn started_as_installer() -> bool {
    std::env::current_exe()
        .ok()
        .and_then(|p| p.file_stem().map(|s| s.to_string_lossy().to_lowercase()))
        .map(|s| s.starts_with("dev-setup") || s.starts_with("dev-installer") || s.starts_with("dev_setup"))
        .unwrap_or(false)
}

/// Waits for Enter when a window was opened just for the installer (double-click).
pub fn pause_if_interactive() {
    use std::io::{BufRead, IsTerminal, Write};
    if std::env::var("DEV_YES").is_ok() || !std::io::stdin().is_terminal() {
        return;
    }
    print!("\nPress Enter to close this window...");
    let _ = std::io::stdout().flush();
    let mut s = String::new();
    let _ = std::io::stdin().lock().read_line(&mut s);
}

pub fn run(mut args: SetupArgs) -> Res<i32> {
    let env_on = |k: &str| matches!(std::env::var(k).as_deref(), Ok("1") | Ok("true"));
    args.no_modify_path |= env_on("DEV_NO_MODIFY_PATH");
    args.no_shells |= env_on("DEV_NO_SHELLS");
    if args.uninstall {
        uninstall(&args)
    } else {
        install(&args)
    }
}

fn install(args: &SetupArgs) -> Res<i32> {
    let dir = args.dir.clone().unwrap_or_else(self_bin_dir);
    fs::create_dir_all(&dir).map_err(|e| format!("{}: {}", dir.display(), e))?;
    let dest = dir.join(exe_name());
    let me = std::env::current_exe().map_err(|e| e.to_string())?;
    println!("Installing dev {} ({}-{})", VERSION, os_name(), cpu_arch());
    if !same_file(&me, &dest) {
        // Replace a running copy safely: move the old one aside first.
        if dest.exists() {
            let old = dir.join(format!("{}.old", exe_name()));
            let _ = fs::remove_file(&old);
            let _ = fs::rename(&dest, &old);
        }
        fs::copy(&me, &dest).map_err(|e| format!("could not copy dev to {}: {}", dest.display(), e))?;
        set_executable(&dest);
    }
    println!("  program:  {}", dest.display());

    if !args.no_modify_path {
        if cfg!(windows) {
            // An earlier PowerShell installer used %LOCALAPPDATA%\Programs\dev\bin: drop it so the
            // old copy can't shadow this one.
            if let Some(old) = legacy_bin_dir().filter(|o| !same_dir(&o.display().to_string(), &dir)) {
                if windows_user_path_remove(&old).unwrap_or(false) {
                    println!("  PATH:     removed the old {}", old.display());
                }
                let old_exe = old.join(exe_name());
                if old_exe.exists() && !same_file(&old_exe, &me) {
                    let _ = fs::remove_file(&old_exe).or_else(|_| fs::rename(&old_exe, old.join("dev.exe.old")));
                    println!("  removed:  old copy {}", old_exe.display());
                }
            }
            if windows_user_path_add(&dir)? {
                println!("  PATH:     added {} (for new terminals)", dir.display());
            } else {
                println!("  PATH:     {} already there", dir.display());
            }
        } else {
            println!("  PATH:     {} (added in the shell files below)", dir.display());
        }
    }

    let _ = fs::create_dir_all(config_dir());
    let _ = fs::create_dir_all(installs_dir());

    if !args.no_shells {
        println!("  shells:");
        for (file, block) in shell_blocks(&dest, &dir, !args.no_modify_path) {
            match write_block(&file, &block) {
                Ok(true) => println!("    configured {}", display_path(&file)),
                Ok(false) => println!("    already set {}", display_path(&file)),
                Err(e) => println!("    skipped {} ({})", display_path(&file), e),
            }
        }
    }
    println!();
    println!("dev is installed. Open a new terminal, then try:");
    println!("  dev use node@lts");
    println!("  dev use java@21      (sets JAVA_HOME)");
    println!("  dev workspace new my-project");
    Ok(0)
}

fn uninstall(args: &SetupArgs) -> Res<i32> {
    let dir = args.dir.clone().unwrap_or_else(self_bin_dir);
    let dest = dir.join(exe_name());
    for (file, _) in shell_blocks(&dest, &dir, true) {
        if remove_block(&file).unwrap_or(false) {
            println!("removed dev from {}", display_path(&file));
        }
    }
    if cfg!(windows) && windows_user_path_remove(&dir).unwrap_or(false) {
        println!("removed {} from PATH", dir.display());
    }
    if args.purge {
        let _ = remove_path(&installs_dir());
        let _ = remove_path(&cache_dir());
        let _ = remove_path(&state_dir());
        let _ = remove_path(&config_dir());
        println!("removed installed tools, caches and settings");
    }
    if dest.exists() {
        if cfg!(windows) {
            // A running program can't delete itself on Windows: ask cmd to do it a moment later.
            let _ = std::process::Command::new("cmd.exe")
                .args(["/C", "ping", "127.0.0.1", "-n", "3", ">NUL", "&", "del", "/f", "/q"])
                .arg(&dest)
                .spawn();
        } else {
            let _ = fs::remove_file(&dest);
        }
        println!("removed {}", dest.display());
    }
    println!("dev is uninstalled. Open a new terminal for it to take effect.");
    Ok(0)
}

fn same_file(a: &Path, b: &Path) -> bool {
    match (fs::canonicalize(a), fs::canonicalize(b)) {
        (Ok(x), Ok(y)) => x == y,
        _ => false,
    }
}

#[cfg(unix)]
fn set_executable(p: &Path) {
    use std::os::unix::fs::PermissionsExt;
    let _ = fs::set_permissions(p, fs::Permissions::from_mode(0o755));
}

#[cfg(not(unix))]
fn set_executable(_p: &Path) {}

fn on_path(name: &str) -> bool {
    let path = std::env::var("PATH").unwrap_or_default();
    find_in_path(name, &split_path_var(&path)).is_some()
}

/// Every shell startup file to set up, with the block to put in it.
pub fn shell_blocks(exe: &Path, bin_dir: &Path, add_path: bool) -> Vec<(PathBuf, String)> {
    let h = home();
    let mut out = vec![];
    let exe_s = exe.display().to_string();
    if cfg!(windows) {
        // PowerShell 7 and Windows PowerShell 5.1.
        let docs = windows_documents_dir();
        let ps_line = format!(
            "if (Test-Path '{e}') {{ Invoke-Expression (& '{e}' activate pwsh | Out-String) }}",
            e = exe_s.replace('\'', "''")
        );
        for sub in ["PowerShell", "WindowsPowerShell"] {
            out.push((docs.join(sub).join("Microsoft.PowerShell_profile.ps1"), ps_line.clone()));
        }
        // Git Bash / MSYS2.
        let pe = to_posix_path(&exe_s);
        out.push((h.join(".bashrc"), posix_block(&pe, "bash", None)));
        if h.join(".zshrc").exists() {
            out.push((h.join(".zshrc"), posix_block(&pe, "zsh", None)));
        }
        return out;
    }
    let path_dir = if add_path { Some(bin_dir.display().to_string()) } else { None };
    out.push((h.join(".bashrc"), posix_block(&exe_s, "bash", path_dir.as_deref())));
    // Login shells (macOS Terminal, ssh) read .bash_profile / .profile instead of .bashrc.
    let login = if h.join(".bash_profile").exists() || cfg!(target_os = "macos") {
        h.join(".bash_profile")
    } else {
        h.join(".profile")
    };
    out.push((login, posix_block(&exe_s, "bash", path_dir.as_deref())));
    if on_path("zsh") || h.join(".zshrc").exists() || cfg!(target_os = "macos") {
        let zdot = std::env::var_os("ZDOTDIR").map(PathBuf::from).unwrap_or_else(|| h.clone());
        out.push((zdot.join(".zshrc"), posix_block(&exe_s, "zsh", path_dir.as_deref())));
    }
    let fish_dir = std::env::var_os("XDG_CONFIG_HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| h.join(".config"))
        .join("fish");
    if on_path("fish") || fish_dir.exists() {
        let mut b = String::new();
        if let Some(d) = &path_dir {
            b.push_str(&format!("fish_add_path -g '{}'\n", d));
        }
        b.push_str(&format!("if test -x '{e}'\n    '{e}' activate fish | source\nend", e = exe_s));
        out.push((fish_dir.join("conf.d").join("dev.fish"), b));
    }
    if on_path("pwsh") {
        let ps = std::env::var_os("XDG_CONFIG_HOME")
            .map(PathBuf::from)
            .unwrap_or_else(|| h.join(".config"))
            .join("powershell")
            .join("Microsoft.PowerShell_profile.ps1");
        out.push((
            ps,
            format!(
                "if (Test-Path '{e}') {{ Invoke-Expression (& '{e}' activate pwsh | Out-String) }}",
                e = exe_s.replace('\'', "''")
            ),
        ));
    }
    out
}

fn posix_block(exe: &str, shell: &str, path_dir: Option<&str>) -> String {
    let mut b = String::new();
    if let Some(d) = path_dir {
        b.push_str(&format!("case \":$PATH:\" in *\":{d}:\"*) ;; *) export PATH=\"{d}:$PATH\";; esac\n", d = d));
    }
    // Only interactive shells get the prompt hook; scripts keep a plain environment.
    b.push_str(&format!(
        "case $- in *i*) if [ -x '{e}' ]; then eval \"$('{e}' activate {s})\"; fi;; esac",
        e = exe,
        s = shell
    ));
    b
}

/// Writes (or replaces) dev's block in a file. Returns false when it was already identical.
pub fn write_block(file: &Path, block: &str) -> Res<bool> {
    let existing = fs::read_to_string(file).unwrap_or_default();
    let new_block = format!("{}\n{}\n{}\n", BEGIN, block.trim_end(), END);
    let updated = match (existing.find(BEGIN), existing.find(END)) {
        (Some(s), Some(e)) if e > s => {
            let end = e + END.len();
            let end = if existing[end..].starts_with("\r\n") {
                end + 2
            } else if existing[end..].starts_with('\n') {
                end + 1
            } else {
                end
            };
            format!("{}{}{}", &existing[..s], new_block, &existing[end..])
        }
        _ => {
            let mut s = existing.clone();
            if !s.is_empty() && !s.ends_with('\n') {
                s.push('\n');
            }
            if !s.is_empty() {
                s.push('\n');
            }
            s.push_str(&new_block);
            s
        }
    };
    if updated == existing {
        return Ok(false);
    }
    write_file(file, &updated)?;
    Ok(true)
}

pub fn remove_block(file: &Path) -> Res<bool> {
    let existing = match fs::read_to_string(file) {
        Ok(s) => s,
        Err(_) => return Ok(false),
    };
    match (existing.find(BEGIN), existing.find(END)) {
        (Some(s), Some(e)) if e > s => {
            let mut end = e + END.len();
            if existing[end..].starts_with("\r\n") {
                end += 2;
            } else if existing[end..].starts_with('\n') {
                end += 1;
            }
            let updated = format!("{}{}", existing[..s].trim_end_matches('\n'), &existing[end..]);
            write_file(file, &updated)?;
            Ok(true)
        }
        _ => Ok(false),
    }
}

// ---------------------------------------------------------------------------
// Windows user environment (persistent, for new terminals and cmd.exe)
// ---------------------------------------------------------------------------

/// Windows PowerShell from its fixed place (PATH may not have it), else whatever PATH offers.
fn powershell_program() -> PathBuf {
    if let Some(root) = std::env::var_os("SystemRoot").or_else(|| std::env::var_os("windir")) {
        let p = PathBuf::from(root).join(r"System32\WindowsPowerShell\v1.0\powershell.exe");
        if p.is_file() {
            return p;
        }
    }
    let path = std::env::var("PATH").unwrap_or_default();
    find_in_path("powershell", &split_path_var(&path))
        .or_else(|| find_in_path("pwsh", &split_path_var(&path)))
        .unwrap_or_else(|| PathBuf::from("powershell.exe"))
}

fn powershell(script: &str, value: &str) -> Res<String> {
    let out = std::process::Command::new(powershell_program())
        .args(["-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script])
        .env("DEV_VALUE", value)
        .output()
        .map_err(|e| format!("could not run PowerShell: {}", e))?;
    if !out.status.success() {
        return err(String::from_utf8_lossy(&out.stderr).trim().to_string());
    }
    Ok(String::from_utf8_lossy(&out.stdout).trim().to_string())
}

fn windows_documents_dir() -> PathBuf {
    if let Some(d) = std::env::var_os("DEV_DOCUMENTS_DIR").filter(|v| !v.is_empty()) {
        return PathBuf::from(d);
    }
    if cfg!(windows) {
        if let Ok(p) = powershell("[Environment]::GetFolderPath('MyDocuments')", "") {
            if !p.is_empty() {
                return PathBuf::from(p);
            }
        }
    }
    home().join("Documents")
}

/// The user's environment in the registry (HKCU\Environment), read and written raw through
/// PowerShell's registry API, so entries such as `%JAVA_HOME%\bin` stay unexpanded, then announced
/// to Windows (WM_SETTINGCHANGE) so new terminals, Explorer and IDEs see the change without
/// logging out. (Doing this from dev's own code got the program blocked by Smart App Control.)
mod winenv {
    use super::powershell;
    use crate::util::*;

    const READ: &str = "$k=[Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment'); \
        try { $v=$k.GetValue($env:DEV_NAME, $null, 'DoNotExpandEnvironmentNames'); \
        if ($null -eq $v) { '<<none>>' } else { [Console]::Out.Write($v) } } finally { $k.Close() }";

    const WRITE: &str = "$k=[Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true); \
        try { $kind = if ($env:DEV_VALUE.Contains('%') -or $env:DEV_NAME -eq 'Path') { 'ExpandString' } else { 'String' }; \
        $k.SetValue($env:DEV_NAME, $env:DEV_VALUE, $kind) } finally { $k.Close() }; \
        Add-Type -Namespace DevWin32 -Name Native -MemberDefinition '[DllImport(\"user32.dll\", CharSet=CharSet.Unicode)] public static extern System.IntPtr SendMessageTimeout(System.IntPtr h, uint m, System.UIntPtr w, string l, uint f, uint t, out System.UIntPtr r);'; \
        $r=[UIntPtr]::Zero; [void][DevWin32.Native]::SendMessageTimeout([IntPtr]0xffff, 0x1A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$r)";

    fn ps(script: &str, name: &str, value: &str) -> Res<String> {
        if !cfg!(windows) {
            return err("the Windows user environment exists only on Windows");
        }
        std::env::set_var("DEV_NAME", name);
        let out = powershell(script, value);
        std::env::remove_var("DEV_NAME");
        out
    }

    pub fn get(name: &str) -> Res<Option<String>> {
        let v = ps(READ, name, "")?;
        Ok(if v == "<<none>>" { None } else { Some(v) })
    }

    pub fn set(name: &str, value: &str) -> Res<()> {
        ps(WRITE, name, value).map(|_| ())
    }
}

fn user_path_entries() -> Res<Vec<String>> {
    let cur = winenv::get("Path")?.unwrap_or_default();
    Ok(cur.split(';').filter(|s| !s.trim().is_empty()).map(String::from).collect())
}

fn set_user_path(entries: &[String]) -> Res<()> {
    winenv::set("Path", &entries.join(";"))
}

/// Where the old PowerShell installer (install.ps1, removed) put dev; cleaned up on setup.
pub fn legacy_bin_dir() -> Option<PathBuf> {
    std::env::var_os("LOCALAPPDATA").map(|p| PathBuf::from(p).join("Programs").join("dev").join("bin"))
}

fn same_dir(a: &str, b: &Path) -> bool {
    a.trim_end_matches(['\\', '/']).eq_ignore_ascii_case(b.display().to_string().trim_end_matches(['\\', '/']))
}

/// Adds a folder to the user's PATH. Returns false when it was already there.
pub fn windows_user_path_add(dir: &Path) -> Res<bool> {
    let mut entries = user_path_entries()?;
    if entries.iter().any(|e| same_dir(e, dir)) {
        return Ok(false);
    }
    entries.insert(0, dir.display().to_string());
    set_user_path(&entries)?;
    Ok(true)
}

pub fn windows_user_path_remove(dir: &Path) -> Res<bool> {
    let entries = user_path_entries()?;
    let kept: Vec<String> = entries.iter().filter(|e| !same_dir(e, dir)).cloned().collect();
    if kept.len() == entries.len() {
        return Ok(false);
    }
    set_user_path(&kept)?;
    Ok(true)
}

/// The user PATH entry for a `<NAME>_HOME` variable: `%JAVA_HOME%\bin`, `%MAVEN_HOME%\bin`...
pub fn home_path_entry(var: &str) -> String {
    format!(r"%{}%\bin", var)
}

/// For a global Java or Maven (`dev use -g`, or `dev use` writing the global config): the
/// user variable (JAVA_HOME, MAVEN_HOME) plus `%VAR%\bin` once on the user PATH, so
/// every new Windows terminal (cmd.exe and IDEs included) gets it. Folders of older dev versions
/// of the same tool on PATH are removed.
pub fn windows_set_global_home(tool: &str, var: &str, home: &Path) -> Res<()> {
    winenv::set(var, &home.display().to_string())?;
    let entry = home_path_entry(var);
    let tool_root = crate::tools::tool_dir(tool).display().to_string().to_lowercase();
    let mut entries: Vec<String> = user_path_entries()?
        .into_iter()
        .filter(|e| !e.to_lowercase().starts_with(&tool_root) && !e.eq_ignore_ascii_case(&entry))
        .collect();
    let at = entries.iter().position(|e| same_dir(e, &self_bin_dir())).map(|i| i + 1).unwrap_or(0);
    entries.insert(at, entry);
    set_user_path(&entries)
}

/// Reads a user environment variable as saved (for `dev doctor` and tests).
pub fn windows_user_var(name: &str) -> Option<String> {
    winenv::get(name).ok().flatten()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn block_is_added_once_and_replaced() {
        let dir = std::env::temp_dir().join(format!("dev-setup-test-{}", std::process::id()));
        let _ = fs::create_dir_all(&dir);
        let f = dir.join(".bashrc");
        fs::write(&f, "alias ll='ls -l'\n").unwrap();
        assert!(write_block(&f, "echo one").unwrap());
        assert!(!write_block(&f, "echo one").unwrap());
        assert!(write_block(&f, "echo two").unwrap());
        let s = fs::read_to_string(&f).unwrap();
        assert_eq!(s.matches(BEGIN).count(), 1);
        assert!(s.contains("echo two") && !s.contains("echo one"));
        assert!(s.starts_with("alias ll"));
        assert!(remove_block(&f).unwrap());
        let s = fs::read_to_string(&f).unwrap();
        assert!(!s.contains(BEGIN));
        assert!(s.contains("alias ll"));
        let _ = fs::remove_dir_all(&dir);
    }

    /// Writes a throwaway user variable, reads it back raw, then deletes it.
    /// Run on purpose with `cargo test -- --ignored` (it touches the real user environment).
    #[cfg(windows)]
    #[test]
    #[ignore]
    fn user_environment_round_trip() {
        let name = "DEV_SELFTEST_TMP";
        let value = r"%JAVA_HOME%\bin;C:\dev test";
        winenv::set(name, value).unwrap();
        assert_eq!(winenv::get(name).unwrap().as_deref(), Some(value));
        powershell(
            "$k=[Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment',$true); $k.DeleteValue('DEV_SELFTEST_TMP'); $k.Close()",
            "",
        )
        .unwrap();
        assert_eq!(winenv::get(name).unwrap(), None);
    }

    #[test]
    fn posix_paths() {
        assert_eq!(to_posix_path(r"C:\Users\me\dev.exe"), "/c/Users/me/dev.exe");
        assert_eq!(to_posix_path("/usr/bin"), "/usr/bin");
    }
}
