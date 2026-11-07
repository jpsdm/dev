//! New-version alert and `dev self-update`, from the project's GitHub releases.
//!
//! The repository comes from DEV_REPO (owner/name) at run time, or the one the release build was
//! made from (the GitHub Actions workflow bakes `DEV_REPO` in). Without one, both stay quiet.

use crate::util::*;
use std::fs;
use std::path::{Path, PathBuf};

/// How often the automatic check may contact GitHub.
pub const CHECK_INTERVAL_SECS: u64 = 24 * 3600;
/// The automatic check gives up after this long so it never slows a command down much.
pub const CHECK_TIMEOUT_SECS: u32 = 3;

pub fn repo() -> Option<String> {
    std::env::var("DEV_REPO")
        .ok()
        .filter(|r| !r.trim().is_empty())
        .or_else(|| option_env!("DEV_REPO").map(String::from).filter(|r| !r.trim().is_empty()))
}

/// The release file for this computer: dev-windows-x86_64.exe, dev-linux-aarch64, dev-macos-aarch64...
pub fn asset_name() -> String {
    format!("dev-{}-{}{}", os_name(), cpu_arch(), if cfg!(windows) { ".exe" } else { "" })
}

pub struct Release {
    pub version: String,
    pub url: String,
    pub assets: Vec<(String, String)>,
}

fn latest_release(repo: &str, max_secs: Option<u32>) -> Res<Release> {
    let api = format!("https://api.github.com/repos/{}/releases/latest", repo);
    let text = http_get_with(&api, Some("GITHUB_TOKEN"), max_secs)?;
    let j: serde_json::Value = serde_json::from_str(&text).map_err(|e| format!("bad release data: {}", e))?;
    let tag = j["tag_name"].as_str().ok_or("the latest release has no tag")?;
    let assets = j["assets"]
        .as_array()
        .map(|a| {
            a.iter()
                .filter_map(|x| Some((x["name"].as_str()?.to_string(), x["browser_download_url"].as_str()?.to_string())))
                .collect()
        })
        .unwrap_or_default();
    Ok(Release {
        version: tag.trim_start_matches('v').to_string(),
        url: j["html_url"].as_str().unwrap_or("").to_string(),
        assets,
    })
}

fn is_newer(latest: &str) -> bool {
    version_cmp(latest, VERSION) == std::cmp::Ordering::Greater
}

fn state_file() -> PathBuf {
    state_dir().join("update-check.json")
}

/// After a command: once a day, look for a newer release and say so on stderr.
/// Off with DEV_NO_UPDATE_CHECK=1, in CI, or when stderr isn't a terminal.
pub fn maybe_notify() {
    use std::io::IsTerminal;
    let off = |k: &str| std::env::var(k).map(|v| !v.is_empty() && v != "0" && v != "false").unwrap_or(false);
    if off("DEV_NO_UPDATE_CHECK") || off("CI") || !std::io::stderr().is_terminal() {
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
    let last = state["checked"].as_u64().unwrap_or(0);
    if now_secs().saturating_sub(last) < CHECK_INTERVAL_SECS {
        return;
    }
    // Record the attempt first, so a slow or offline network costs at most one try a day.
    let mut latest = state["latest"].as_str().unwrap_or("").to_string();
    if let Ok(r) = latest_release(&repo, Some(CHECK_TIMEOUT_SECS)) {
        latest = r.version;
    }
    let _ = write_file(&state_file(), &serde_json::json!({ "checked": now_secs(), "latest": latest }).to_string());
    if !latest.is_empty() && is_newer(&latest) {
        eprintln!();
        eprintln!("{} a new version is available: {} (you have {})", NAME, latest, VERSION);
        eprintln!("{} update with: dev self-update", NAME);
    }
}

/// `dev self-update`: download the newest release for this computer and replace this program.
pub fn self_update(check_only: bool, force: bool, quiet: bool) -> Res<i32> {
    let repo = repo().ok_or("no release repository is known; set DEV_REPO=owner/name")?;
    let rel = latest_release(&repo, None)?;
    let _ = write_file(&state_file(), &serde_json::json!({ "checked": now_secs(), "latest": rel.version }).to_string());
    if !is_newer(&rel.version) && !force {
        if !quiet {
            info(format!("dev {} is up to date (latest release: {})", VERSION, rel.version));
        }
        return Ok(0);
    }
    if check_only {
        println!("{} → {}  {}", VERSION, rel.version, rel.url);
        return Ok(0);
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
    info(format!("downloading {} {}", rel.version, url));
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
}
