//! Tool backends: where versions come from, how they install, where their programs live.

use crate::util::*;
use std::fs;
use std::path::{Path, PathBuf};
use std::time::Duration;

const REMOTE_CACHE: Duration = Duration::from_secs(3600);

#[derive(Clone, Debug, PartialEq)]
pub enum Backend {
    Node,
    Go,
    Github { owner: String, repo: String },
    /// `npm:PACKAGE`, installed with npm into its own prefix.
    Npm(String),
    /// `cargo:CRATE`, built with `cargo install --root`.
    Cargo(String),
    /// `pipx:PACKAGE` / `pypi:PACKAGE`, installed with uv (or pipx when uv is missing).
    Pypi(String),
    /// `java`: JDKs from the vendors' own downloads, listed by the original's metadata service.
    Java,
    /// Known only through `dev link`.
    Local(String),
}

impl Backend {
    pub fn of(tool: &str) -> Backend {
        let t = tool.trim();
        if let Some(rest) = t.strip_prefix("github:").or_else(|| t.strip_prefix("ubi:")).or_else(|| t.strip_prefix("aqua:")) {
            let mut it = rest.splitn(2, '/');
            if let (Some(o), Some(r)) = (it.next(), it.next()) {
                return Backend::Github { owner: o.into(), repo: r.trim_end_matches(".git").into() };
            }
        }
        if let Some(p) = t.strip_prefix("npm:") {
            return Backend::Npm(p.to_string());
        }
        if let Some(p) = t.strip_prefix("cargo:") {
            return Backend::Cargo(p.to_string());
        }
        if let Some(p) = t.strip_prefix("pipx:").or_else(|| t.strip_prefix("pypi:")) {
            return Backend::Pypi(p.to_string());
        }
        match t {
            "node" | "nodejs" | "core:node" => Backend::Node,
            "go" | "golang" | "core:go" => Backend::Go,
            "java" | "core:java" | "jdk" => Backend::Java,
            // A few popular tools, mapped to their GitHub releases.
            "gh" => gh("cli", "cli"),
            "ripgrep" | "rg" => gh("BurntSushi", "ripgrep"),
            "fd" => gh("sharkdp", "fd"),
            "bat" => gh("sharkdp", "bat"),
            "jq" => gh("jqlang", "jq"),
            "just" => gh("casey", "just"),
            "deno" => gh("denoland", "deno"),
            "bun" => gh("oven-sh", "bun"),
            "terraform-ls" => gh("hashicorp", "terraform-ls"),
            "lazygit" => gh("jesseduffield", "lazygit"),
            "uv" => gh("astral-sh", "uv"),
            "zig" => gh("ziglang", "zig"),
            "watchexec" => gh("watchexec", "watchexec"),
            "starship" => gh("starship", "starship"),
            "fzf" => gh("junegunn", "fzf"),
            "yq" => gh("mikefarah", "yq"),
            "hugo" => gh("gohugoio", "hugo"),
            "kubectl" => Backend::Local("kubectl".into()),
            other => Backend::Local(other.into()),
        }
    }

    pub fn label(&self) -> &'static str {
        match self {
            Backend::Node => "core",
            Backend::Go => "core",
            Backend::Java => "core",
            Backend::Github { .. } => "github",
            Backend::Npm(_) => "npm",
            Backend::Cargo(_) => "cargo",
            Backend::Pypi(_) => "pypi",
            Backend::Local(_) => "local",
        }
    }
}

fn gh(o: &str, r: &str) -> Backend {
    Backend::Github { owner: o.into(), repo: r.into() }
}

/// Folder name for a tool: `github:cli/cli` → `github-cli-cli`.
pub fn dir_name(tool: &str) -> String {
    tool.chars()
        .map(|c| if c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.' { c } else { '-' })
        .collect()
}

pub fn tool_dir(tool: &str) -> PathBuf {
    installs_dir().join(dir_name(tool))
}

pub fn install_path(tool: &str, version: &str) -> PathBuf {
    tool_dir(tool).join(version)
}

/// Follows a `dev link` marker to the real folder.
pub fn real_path(p: &Path) -> PathBuf {
    let marker = p.join(".dev-link");
    if let Ok(t) = fs::read_to_string(&marker) {
        return PathBuf::from(t.trim());
    }
    p.to_path_buf()
}

pub fn installed_versions(tool: &str) -> Vec<String> {
    let mut v: Vec<String> = fs::read_dir(tool_dir(tool))
        .map(|rd| {
            rd.flatten()
                .filter(|e| e.path().is_dir())
                .filter(|e| !e.file_name().to_string_lossy().starts_with('.'))
                .filter(|e| e.path().join(".dev-complete").exists() || e.path().join(".dev-link").exists())
                .map(|e| e.file_name().to_string_lossy().into_owned())
                .collect()
        })
        .unwrap_or_default();
    v.sort_by(|a, b| version_cmp(b, a));
    v
}

pub fn all_installed() -> Vec<(String, String)> {
    let mut out = vec![];
    if let Ok(rd) = fs::read_dir(installs_dir()) {
        for e in rd.flatten() {
            if !e.path().is_dir() {
                continue;
            }
            let dir = e.file_name().to_string_lossy().into_owned();
            let tool = fs::read_to_string(e.path().join(".dev-tool")).map(|s| s.trim().to_string()).unwrap_or(dir);
            for v in installed_versions(&tool) {
                out.push((tool.clone(), v));
            }
        }
    }
    out.sort();
    out
}

fn remember_tool_name(tool: &str) {
    let _ = write_file(&tool_dir(tool).join(".dev-tool"), tool);
}

/// Versions available to install, newest first.
pub fn remote_versions(tool: &str) -> Res<Vec<String>> {
    let backend = Backend::of(tool);
    let cache = cache_dir().join(dir_name(tool)).join("remote_versions.json");
    if let Some(text) = read_cache(&cache, REMOTE_CACHE) {
        if let Ok(v) = serde_json::from_str::<Vec<String>>(&text) {
            return Ok(v);
        }
    }
    let mut list: Vec<String> = match &backend {
        Backend::Node => {
            let text = http_get("https://nodejs.org/dist/index.json", None)?;
            let j: serde_json::Value = serde_json::from_str(&text).map_err(|e| e.to_string())?;
            j.as_array()
                .map(|a| {
                    a.iter()
                        .filter_map(|x| x["version"].as_str().map(|s| s.trim_start_matches('v').to_string()))
                        .collect()
                })
                .unwrap_or_default()
        }
        Backend::Go => {
            let text = http_get("https://go.dev/dl/?mode=json&include=all", None)?;
            let j: serde_json::Value = serde_json::from_str(&text).map_err(|e| e.to_string())?;
            j.as_array()
                .map(|a| {
                    a.iter()
                        .filter_map(|x| x["version"].as_str().map(|s| s.trim_start_matches("go").to_string()))
                        .collect()
                })
                .unwrap_or_default()
        }
        Backend::Github { owner, repo } => {
            let url = format!("https://api.github.com/repos/{}/{}/releases?per_page=100", owner, repo);
            let text = http_get(&url, Some("GITHUB_TOKEN"))?;
            let j: serde_json::Value = serde_json::from_str(&text).map_err(|e| e.to_string())?;
            j.as_array()
                .map(|a| {
                    a.iter()
                        .filter(|x| !x["draft"].as_bool().unwrap_or(false))
                        .filter_map(|x| x["tag_name"].as_str().map(tag_to_version))
                        .collect()
                })
                .unwrap_or_default()
        }
        Backend::Npm(pkg) => npm_versions(pkg)?,
        Backend::Cargo(krate) => cargo_versions(krate)?,
        Backend::Pypi(pkg) => pypi_versions(pkg)?,
        Backend::Java => java_releases()?.into_iter().map(|r| r.name).collect(),
        Backend::Local(name) => {
            return err(format!(
                "{} has no download source in dev; register an existing install with `dev link {}@VERSION <DIR>` or use github:OWNER/REPO",
                name, name
            ))
        }
    };
    list.sort_by(|a, b| version_cmp(b, a));
    list.dedup();
    let _ = write_file(&cache, &serde_json::to_string(&list).unwrap_or_default());
    Ok(list)
}

fn tag_to_version(tag: &str) -> String {
    let t = tag.trim();
    // "v1.2.3", "jq-1.7.1", "release-1.0" → the part that starts with a digit.
    match t.char_indices().find(|(_, c)| c.is_ascii_digit()) {
        Some((i, _)) => t[i..].to_string(),
        None => t.to_string(),
    }
}

fn node_lts_versions() -> Res<Vec<String>> {
    let text = http_get("https://nodejs.org/dist/index.json", None)?;
    let j: serde_json::Value = serde_json::from_str(&text).map_err(|e| e.to_string())?;
    let mut v: Vec<String> = j
        .as_array()
        .map(|a| {
            a.iter()
                .filter(|x| x["lts"].is_string())
                .filter_map(|x| x["version"].as_str().map(|s| s.trim_start_matches('v').to_string()))
                .collect()
        })
        .unwrap_or_default();
    v.sort_by(|a, b| version_cmp(b, a));
    Ok(v)
}

/// Turns a request (`latest`, `lts`, `24`, `1.22.3`) into a concrete version.
/// Installed versions that satisfy it are preferred unless `remote` is forced.
pub fn resolve(tool: &str, request: &str, prefer_installed: bool) -> Res<String> {
    let req = request.trim().trim_start_matches('v');
    if let Some(p) = req.strip_prefix("path:").or_else(|| req.strip_prefix("ref:")) {
        return Ok(p.to_string());
    }
    let installed = installed_versions(tool);
    if prefer_installed {
        let hit = match req {
            "latest" | "" => installed.iter().find(|v| !is_prerelease(v)).or(installed.first()),
            "lts" => None,
            _ => installed.iter().find(|v| version_matches(v, req)),
        };
        if let Some(v) = hit {
            return Ok(v.clone());
        }
    }
    if matches!(Backend::of(tool), Backend::Local(_)) {
        if let Some(v) = installed.iter().find(|v| *v == req || version_matches(v, req)) {
            return Ok(v.clone());
        }
        return Ok(req.to_string());
    }
    let remote = if req == "lts" && Backend::of(tool) == Backend::Node {
        node_lts_versions()?
    } else {
        remote_versions(tool)?
    };
    // `latest` means a plain version (for java: the default vendor's, not `zulu-…`).
    let plain = |v: &&String| v.chars().next().map(|c| c.is_ascii_digit()).unwrap_or(false);
    let found = match req {
        "latest" | "lts" | "" => remote
            .iter()
            .find(|v| plain(v) && !is_prerelease(v))
            .or_else(|| remote.iter().find(|v| !is_prerelease(v)))
            .or(remote.first()),
        _ => remote
            .iter()
            .find(|v| version_matches(v, req) && !is_prerelease(v))
            .or_else(|| remote.iter().find(|v| version_matches(v, req))),
    };
    found
        .cloned()
        .ok_or_else(|| format!("no version of {} matches {}", tool, request))
}

pub fn is_installed(tool: &str, version: &str) -> bool {
    let p = install_path(tool, version);
    p.join(".dev-complete").exists() || p.join(".dev-link").exists()
}

/// Downloads and unpacks one version. Returns the install folder.
pub fn install(tool: &str, version: &str, force: bool, verbose_out: bool) -> Res<PathBuf> {
    let dest = install_path(tool, version);
    if is_installed(tool, version) && !force {
        return Ok(dest);
    }
    let backend = Backend::of(tool);
    if matches!(backend, Backend::Npm(_) | Backend::Cargo(_) | Backend::Pypi(_)) {
        return install_package(tool, version, &backend, &dest);
    }
    let mut checksum: Option<String> = None;
    let (url, file_name) = match &backend {
        Backend::Node => {
            let f = node_archive(version);
            (format!("https://nodejs.org/dist/v{}/{}", version, f), f)
        }
        Backend::Go => {
            let f = go_archive(version);
            (format!("https://go.dev/dl/{}", f), f)
        }
        Backend::Java => {
            let rel = java_releases()?
                .into_iter()
                .find(|r| r.name == version)
                .ok_or_else(|| format!("no java {} download for {}-{}", version, os_name(), cpu_arch()))?;
            checksum = rel.sha256.clone();
            let f = format!("java-{}.{}", dir_name(version), rel.file_type);
            (rel.url, f)
        }
        Backend::Github { owner, repo } => github_asset(owner, repo, version)?,
        Backend::Npm(_) | Backend::Cargo(_) | Backend::Pypi(_) => unreachable!(),
        Backend::Local(name) => {
            return err(format!(
                "{}@{} is not installed and dev cannot download {}; use `dev link {}@{} <DIR>`",
                name, version, name, name, version
            ))
        }
    };
    info(format!("{}@{} downloading {}", tool, version, url));
    let dl = cache_dir().join("downloads").join(&file_name);
    download(&url, &dl)?;
    if let Some(want) = &checksum {
        let got = sha256_file(&dl)?;
        if !got.eq_ignore_ascii_case(want) {
            let _ = fs::remove_file(&dl);
            return err(format!("checksum mismatch for {}: expected {}, got {}", file_name, want, got));
        }
        if verbose_out {
            info(format!("{}@{} checksum ok (sha256:{})", tool, version, got));
        }
    }
    let tmp = tool_dir(tool).join(format!(".tmp-{}", version));
    let _ = remove_path(&tmp);
    fs::create_dir_all(&tmp).map_err(|e| e.to_string())?;
    let lower = file_name.to_ascii_lowercase();
    if lower.ends_with(".exe") || is_raw_binary(&lower) {
        let name = match &backend {
            Backend::Github { repo, .. } if cfg!(windows) => format!("{}.exe", repo),
            Backend::Github { repo, .. } => repo.clone(),
            _ => file_name.clone(),
        };
        fs::copy(&dl, tmp.join(&name)).map_err(|e| e.to_string())?;
        make_executable(&tmp.join(&name));
    } else {
        if verbose_out {
            info(format!("{}@{} unpacking {}", tool, version, file_name));
        }
        extract(&dl, &tmp)?;
    }
    // Most archives hold one top folder (node-v24-win-x64/, go/): lift its contents up.
    let entries: Vec<_> = fs::read_dir(&tmp).map_err(|e| e.to_string())?.flatten().collect();
    let src = if entries.len() == 1 && entries[0].path().is_dir() { entries[0].path() } else { tmp.clone() };
    let _ = remove_path(&dest);
    fs::create_dir_all(dest.parent().unwrap()).map_err(|e| e.to_string())?;
    fs::rename(&src, &dest).map_err(|e| format!("could not move install into place: {}", e))?;
    let _ = remove_path(&tmp);
    write_file(&dest.join(".dev-complete"), &now_secs().to_string())?;
    remember_tool_name(tool);
    let _ = fs::remove_file(&dl);
    Ok(dest)
}

fn github_asset(owner: &str, repo: &str, version: &str) -> Res<(String, String)> {
    let mut release = None;
    for tag in [format!("v{}", version), version.to_string(), format!("{}-{}", repo, version)] {
        let url = format!("https://api.github.com/repos/{}/{}/releases/tags/{}", owner, repo, tag);
        if let Ok(text) = http_get(&url, Some("GITHUB_TOKEN")) {
            if let Ok(j) = serde_json::from_str::<serde_json::Value>(&text) {
                release = Some(j);
                break;
            }
        }
    }
    let rel = release.ok_or_else(|| format!("no GitHub release of {}/{} for version {}", owner, repo, version))?;
    let assets = rel["assets"].as_array().cloned().unwrap_or_default();
    let mut best: Option<(i32, String, String)> = None;
    for a in &assets {
        let name = a["name"].as_str().unwrap_or("").to_string();
        let url = a["browser_download_url"].as_str().unwrap_or("").to_string();
        let score = asset_score(&name);
        if score > 0 && best.as_ref().map(|b| score > b.0).unwrap_or(true) {
            best = Some((score, url, name));
        }
    }
    best.map(|(_, u, n)| (u, n)).ok_or_else(|| {
        format!("{}/{} {} has no download for {}-{}", owner, repo, version, os_name(), cpu_arch())
    })
}

fn node_archive(version: &str) -> String {
    let arch = if cpu_arch() == "aarch64" { "arm64" } else { "x64" };
    match os_name() {
        "windows" => format!("node-v{}-win-{}.zip", version, arch),
        "macos" => format!("node-v{}-darwin-{}.tar.gz", version, arch),
        _ => format!("node-v{}-linux-{}.tar.gz", version, arch),
    }
}

fn go_archive(version: &str) -> String {
    let arch = if cpu_arch() == "aarch64" { "arm64" } else { "amd64" };
    match os_name() {
        "windows" => format!("go{}.windows-{}.zip", version, arch),
        "macos" => format!("go{}.darwin-{}.tar.gz", version, arch),
        _ => format!("go{}.linux-{}.tar.gz", version, arch),
    }
}

/// A release file with no extension (`jq-linux-amd64`), usable as-is on Linux and macOS.
fn is_raw_binary(lower_name: &str) -> bool {
    if cfg!(windows) {
        return false;
    }
    let last = lower_name.rsplit(['-', '_']).next().unwrap_or("");
    !last.contains('.')
}

#[cfg(unix)]
fn make_executable(p: &Path) {
    use std::os::unix::fs::PermissionsExt;
    let _ = fs::set_permissions(p, fs::Permissions::from_mode(0o755));
}

#[cfg(not(unix))]
fn make_executable(_p: &Path) {}

/// Scores how well a release file fits this computer (0 = not usable).
pub fn asset_score(name: &str) -> i32 {
    asset_score_for(name, os_name(), cpu_arch())
}

pub fn asset_score_for(name: &str, os: &str, arch: &str) -> i32 {
    let n = name.to_ascii_lowercase();
    let bad = [
        ".sha256", ".sha512", ".sig", ".asc", ".pem", ".sbom", ".json", ".txt", ".msi", ".deb", ".rpm", ".apk", ".pkg",
        ".dmg", ".sh", ".md", ".bundle", ".intoto.jsonl", "checksums",
    ];
    if bad.iter().any(|b| n.ends_with(b) || n.contains("checksums")) {
        return 0;
    }
    let os_ok = match os {
        "windows" => n.contains("windows") || n.contains("win64") || n.contains("-win") || n.contains("pc-windows") || n.ends_with(".exe"),
        "macos" => n.contains("darwin") || n.contains("macos") || n.contains("apple") || n.contains("osx") || n.contains("-mac"),
        _ => n.contains("linux"),
    };
    if !os_ok {
        return 0;
    }
    let is_arm = n.contains("arm64") || n.contains("aarch64");
    let is_x64 = n.contains("x86_64") || n.contains("amd64") || n.contains("x64") || n.contains("win64");
    let universal = n.contains("universal");
    let mut s = 10;
    if arch == "aarch64" {
        if is_arm {
            s += 10;
        } else if universal {
            s += 8;
        } else {
            return 0;
        }
    } else if is_x64 {
        s += 10;
    } else if universal {
        s += 8;
    } else if is_arm || n.contains("386") || n.contains("i686") || n.contains("x86") || n.contains("armv") || n.contains("32") {
        return 0;
    }
    if n.ends_with(".zip") {
        s += if os == "windows" { 5 } else { 3 };
    } else if n.ends_with(".exe") && os == "windows" {
        s += 4;
    } else if n.ends_with(".tar.gz") || n.ends_with(".tgz") {
        s += if os == "windows" { 2 } else { 5 };
    } else if n.ends_with(".tar.xz") || n.ends_with(".txz") {
        s += if os == "windows" { 1 } else { 4 };
    } else if os != "windows" && is_raw_binary_on(&n) {
        s += 1;
    } else {
        return 0;
    }
    if n.contains("msvc") || n.contains("musl") {
        s += 1;
    }
    s
}

fn is_raw_binary_on(lower_name: &str) -> bool {
    let last = lower_name.rsplit(['-', '_']).next().unwrap_or("");
    !last.contains('.')
}

// ---------------------------------------------------------------------------
// Java
// ---------------------------------------------------------------------------

/// The original's JVM list: https://mise-java.jdx.dev/jvm/{release_type}/{os}/{arch}.json
/// (template 0x14b6fda80, built at 0x143ec15fb; arm64 is sent as aarch64 at 0x143ec158a).
pub const JAVA_METADATA_URL: &str = "https://mise-java.jdx.dev/jvm";
/// Vendor used for a bare version such as `java@21` (setting `java.shorthand_vendor`, default temurin).
pub const JAVA_SHORTHAND_VENDOR: &str = "temurin";

#[derive(Clone, Debug)]
pub struct JavaRelease {
    /// `21.0.5+11` for the shorthand vendor, `zulu-21.0.5+11` for the others.
    pub name: String,
    pub url: String,
    pub file_type: String,
    pub sha256: Option<String>,
}

fn java_os() -> &'static str {
    match os_name() {
        "macos" => "macosx",
        "windows" => "windows",
        _ => "linux",
    }
}

fn java_arch() -> &'static str {
    if cpu_arch() == "aarch64" {
        "aarch64"
    } else {
        "x86_64"
    }
}

pub fn java_shorthand_vendor() -> String {
    std::env::var("DEV_JAVA_SHORTHAND_VENDOR")
        .ok()
        .filter(|v| !v.is_empty())
        .unwrap_or_else(|| JAVA_SHORTHAND_VENDOR.to_string())
}

/// Every GA JDK for this computer, one per name, newest first.
pub fn java_releases() -> Res<Vec<JavaRelease>> {
    let url = format!("{}/ga/{}/{}.json", JAVA_METADATA_URL, java_os(), java_arch());
    let cache = cache_dir().join("java").join(format!("ga-{}-{}.json", java_os(), java_arch()));
    let text = match read_cache(&cache, REMOTE_CACHE) {
        Some(t) => t,
        None => {
            let t = http_get(&url, None)?;
            let _ = write_file(&cache, &t);
            t
        }
    };
    let j: serde_json::Value = serde_json::from_str(&text).map_err(|e| format!("bad java metadata: {}", e))?;
    let short = java_shorthand_vendor();
    let wanted_types: &[&str] = if cfg!(windows) { &["zip", "tar.gz"] } else { &["tar.gz", "zip"] };
    let mut out: Vec<(usize, JavaRelease)> = vec![];
    for e in j.as_array().cloned().unwrap_or_default() {
        if e["image_type"].as_str() != Some("jdk") {
            continue;
        }
        if e["features"].as_array().map(|f| !f.is_empty()).unwrap_or(false) {
            continue;
        }
        let ft = e["file_type"].as_str().unwrap_or("");
        let rank = match wanted_types.iter().position(|t| *t == ft) {
            Some(r) => r,
            None => continue,
        };
        let vendor = e["vendor"].as_str().unwrap_or("");
        let version = e["version"].as_str().unwrap_or("");
        let url = e["url"].as_str().unwrap_or("");
        if vendor.is_empty() || version.is_empty() || url.is_empty() {
            continue;
        }
        let name = if vendor == short { version.to_string() } else { format!("{}-{}", vendor, version) };
        let sha256 = e["checksum"].as_str().and_then(|c| c.strip_prefix("sha256:")).map(String::from);
        // OpenJ9 builds come second to HotSpot for the same name.
        let rank = rank + if e["jvm_impl"].as_str() == Some("hotspot") { 0 } else { 10 };
        out.push((rank, JavaRelease { name, url: url.to_string(), file_type: ft.to_string(), sha256 }));
    }
    out.sort_by(|a, b| version_cmp(&b.1.name, &a.1.name).then(a.0.cmp(&b.0)));
    let mut seen = std::collections::HashSet::new();
    Ok(out.into_iter().filter(|(_, r)| seen.insert(r.name.clone())).map(|(_, r)| r).collect())
}

/// JAVA_HOME of an install: the folder itself, or Contents/Home in macOS JDK bundles.
pub fn java_home(install: &Path) -> PathBuf {
    let root = real_path(install);
    let mac = root.join("Contents").join("Home");
    if mac.join("bin").is_dir() {
        mac
    } else {
        root
    }
}

pub fn sha256_file(p: &Path) -> Res<String> {
    use sha2::{Digest, Sha256};
    use std::io::Read;
    let mut f = fs::File::open(p).map_err(|e| format!("{}: {}", p.display(), e))?;
    let mut h = Sha256::new();
    let mut buf = vec![0u8; 1 << 16];
    loop {
        let n = f.read(&mut buf).map_err(|e| e.to_string())?;
        if n == 0 {
            break;
        }
        h.update(&buf[..n]);
    }
    Ok(h.finalize().iter().map(|b| format!("{:02x}", b)).collect())
}

/// Folders holding the programs of an install.
pub fn bin_dirs(tool: &str, path: &Path) -> Vec<PathBuf> {
    let root = real_path(path);
    let mut out = vec![];
    match Backend::of(tool) {
        Backend::Node if cfg!(windows) => out.push(root.clone()),
        Backend::Node => out.push(root.join("bin")),
        Backend::Go => out.push(root.join("bin")),
        Backend::Java => out.push(java_home(&root).join("bin")),
        // npm on Windows puts its .cmd launchers in the prefix itself; elsewhere in prefix/bin.
        Backend::Npm(_) if cfg!(windows) => out.push(root.clone()),
        Backend::Npm(_) => out.push(root.join("bin")),
        Backend::Cargo(_) | Backend::Pypi(_) => out.push(root.join("bin")),
        _ => {
            if root.join("bin").is_dir() {
                out.push(root.join("bin"));
            }
            out.push(root.clone());
            // Some archives keep the program one folder down.
            if let Ok(rd) = fs::read_dir(&root) {
                for e in rd.flatten() {
                    let p = e.path();
                    if p.is_dir() && !e.file_name().to_string_lossy().starts_with('.') && has_exe(&p) && !has_exe(&root) {
                        out.push(p);
                    }
                }
            }
        }
    }
    out
}

fn has_exe(d: &Path) -> bool {
    fs::read_dir(d)
        .map(|rd| rd.flatten().any(|e| e.path().extension().map(|x| x.eq_ignore_ascii_case("exe")).unwrap_or(false)))
        .unwrap_or(false)
}

pub fn link(tool: &str, version: &str, target: &Path, force: bool) -> Res<()> {
    let dest = install_path(tool, version);
    if dest.exists() && !force {
        return err(format!("{}@{} is already installed; pass --force to replace it", tool, version));
    }
    if !target.is_dir() {
        return err(format!("{} is not a directory", target.display()));
    }
    let _ = remove_path(&dest);
    let abs = fs::canonicalize(target).map_err(|e| e.to_string())?;
    let s = abs.display().to_string();
    let s = s.strip_prefix(r"\\?\").unwrap_or(&s).to_string();
    write_file(&dest.join(".dev-link"), &s)?;
    remember_tool_name(tool);
    Ok(())
}

pub fn uninstall(tool: &str, version: &str) -> Res<()> {
    let p = install_path(tool, version);
    if !p.exists() {
        return err(format!("{}@{} is not installed", tool, version));
    }
    remove_path(&p)
}

/// Splits `node@24` → ("node", Some("24")).
pub fn split_spec(spec: &str) -> (String, Option<String>) {
    // Keep `@` inside the tool name for npm-style scopes (`npm:@scope/pkg@1`).
    let s = spec.trim();
    let search_from = if s.contains(':') { s.find(':').unwrap() + 2 } else { 1 };
    if s.len() > search_from {
        if let Some(i) = s[search_from..].rfind('@') {
            let i = i + search_from;
            return (s[..i].to_string(), Some(s[i + 1..].to_string()));
        }
    }
    (s.to_string(), None)
}

// ---------------------------------------------------------------------------
// Package-manager backends (npm:, cargo:, pipx:)
// ---------------------------------------------------------------------------

/// Default npm registry (compared against at 0x142d6da14 in the original).
pub const NPM_REGISTRY: &str = "https://registry.npmjs.org";
/// Accept header the original sends for package metadata (string 0x14aa6a83e).
pub const NPM_ACCEPT: &str = "application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*";
/// Default PyPI version list, `{}` = package name (0x144db9319 in the original).
pub const PYPI_REGISTRY_URL: &str = "https://pypi.org/pypi/{}/json";
/// cargo-binstall exits with this code when it finds no prebuilt binary (0x14589fbef).
pub const BINSTALL_NO_PREBUILT_EXIT: i32 = 94;

fn npm_versions(pkg: &str) -> Res<Vec<String>> {
    // Scoped names keep their `@` and encode the slash: @scope%2fname.
    let url = format!("{}/{}", NPM_REGISTRY, pkg.replace('/', "%2f"));
    let text = http_get_accept(&url, NPM_ACCEPT)?;
    let j: serde_json::Value = serde_json::from_str(&text).map_err(|e| e.to_string())?;
    Ok(j["versions"].as_object().map(|m| m.keys().cloned().collect()).unwrap_or_default())
}

/// Path of a crate in the crates.io sparse index (`se/rd/serde`, `3/a/abc`, `2/ab`).
pub fn crate_index_path(name: &str) -> String {
    let n = name.to_ascii_lowercase();
    match n.len() {
        0 => n,
        1 => format!("1/{}", n),
        2 => format!("2/{}", n),
        3 => format!("3/{}/{}", &n[..1], n),
        _ => format!("{}/{}/{}", &n[..2], &n[2..4], n),
    }
}

fn cargo_versions(krate: &str) -> Res<Vec<String>> {
    let url = format!("https://index.crates.io/{}", crate_index_path(krate));
    let text = http_get(&url, None)?;
    Ok(text
        .lines()
        .filter_map(|l| serde_json::from_str::<serde_json::Value>(l).ok())
        .filter(|j| !j["yanked"].as_bool().unwrap_or(false))
        .filter_map(|j| j["vers"].as_str().map(String::from))
        .collect())
}

fn pypi_versions(pkg: &str) -> Res<Vec<String>> {
    let url = PYPI_REGISTRY_URL.replace("{}", pkg);
    let text = http_get(&url, None)?;
    let j: serde_json::Value = serde_json::from_str(&text).map_err(|e| e.to_string())?;
    Ok(j["releases"]
        .as_object()
        .map(|m| {
            m.iter()
                .filter(|(_, files)| files.as_array().map(|a| !a.is_empty()).unwrap_or(true))
                .map(|(k, _)| k.clone())
                .collect()
        })
        .unwrap_or_default())
}

/// Finds a helper program (npm, cargo, uv, pipx): first in a version dev installed of
/// `provider`, then in the extra folders, then on PATH.
pub fn helper_program(name: &str, provider: Option<&str>, extra: &[PathBuf]) -> Option<PathBuf> {
    if let Some(prov) = provider {
        for v in installed_versions(prov) {
            let dirs = bin_dirs(prov, &install_path(prov, &v));
            if let Some(p) = find_in_path(name, &dirs) {
                return Some(p);
            }
        }
    }
    if let Some(p) = find_in_path(name, extra) {
        return Some(p);
    }
    let path = std::env::var("PATH").unwrap_or_default();
    find_in_path(name, &split_path_var(&path))
}

fn with_helper_path(cmd: &mut std::process::Command, helper: &Path) {
    let mut dirs = vec![];
    if let Some(d) = helper.parent() {
        dirs.push(d.to_path_buf());
    }
    dirs.extend(split_path_var(&std::env::var("PATH").unwrap_or_default()));
    if let Ok(joined) = std::env::join_paths(dirs) {
        cmd.env("PATH", joined);
    }
}

fn install_package(tool: &str, version: &str, backend: &Backend, dest: &Path) -> Res<PathBuf> {
    let _ = remove_path(dest);
    fs::create_dir_all(dest).map_err(|e| e.to_string())?;
    if let Err(e) = run_package_manager(tool, version, backend, dest) {
        let _ = remove_path(dest);
        return Err(e);
    }
    write_file(&dest.join(".dev-complete"), &now_secs().to_string())?;
    remember_tool_name(tool);
    Ok(dest.to_path_buf())
}

/// DEV_PIPX_UVX=0 (or false) installs Python tools with pipx instead of uv.
fn pipx_prefers_uv() -> bool {
    match std::env::var("DEV_PIPX_UVX") {
        Ok(v) => !(v == "0" || v.eq_ignore_ascii_case("false")),
        Err(_) => true,
    }
}

/// Shown when `cargo:` is used without Rust (string 0x14c5afb00 in the original, renamed).
pub const CARGO_MISSING: &str =
    "To use cargo packages with dev, you need to install Rust first:\nInstall Rust via https://rustup.rs/";

/// DEV_CARGO_BINSTALL=0 (or false) skips cargo-binstall (the original's `cargo.binstall`, default true).
fn cargo_binstall_enabled() -> bool {
    !matches!(std::env::var("DEV_CARGO_BINSTALL").as_deref(), Ok("0") | Ok("false"))
}

/// DEV_CARGO_BINSTALL_QUICKINSTALL=1 lets cargo-binstall use cargo-quickinstall (default off).
fn cargo_binstall_quickinstall() -> bool {
    matches!(std::env::var("DEV_CARGO_BINSTALL_QUICKINSTALL").as_deref(), Ok("1") | Ok("true"))
}

/// Runs an installer; returns its exit code.
fn run_installer(mut cmd: std::process::Command, dest: &Path, label: &str) -> Res<i32> {
    use std::process::Stdio;
    cmd.current_dir(dest).stdin(Stdio::null());
    if !verbose() {
        cmd.stdout(Stdio::null());
    }
    let status = cmd
        .status()
        .map_err(|e| format!("could not start the {} installer: {}", label, e))?;
    Ok(if status.success() { 0 } else { status.code().unwrap_or(1) })
}

fn failed(tool: &str, version: &str, code: i32) -> Res<()> {
    err(format!("{}@{} failed to install (exit code {})", tool, version, code))
}

fn run_package_manager(tool: &str, version: &str, backend: &Backend, dest: &Path) -> Res<()> {
    use std::process::Command;
    info(format!("{}@{} installing with {}", tool, version, backend.label()));
    match backend {
        Backend::Npm(pkg) => {
            // npm install -g <pkg@ver> --prefix <dir>, NPM_CONFIG_UPDATE_NOTIFIER=false (0x14375978a..0x143759ac1).
            let npm = helper_program("npm", Some("node"), &[])
                .ok_or("npm was not found: install node first (`dev use -g node@lts`)")?;
            let mut cmd = Command::new(&npm);
            with_helper_path(&mut cmd, &npm);
            cmd.args(["install", "-g"])
                .arg(format!("{}@{}", pkg, version))
                .arg("--prefix")
                .arg(dest)
                .env("NPM_CONFIG_UPDATE_NOTIFIER", "false");
            match run_installer(cmd, dest, "npm")? {
                0 => Ok(()),
                c => failed(tool, version, c),
            }
        }
        Backend::Cargo(krate) => {
            let cargo_home = std::env::var("CARGO_HOME")
                .map(PathBuf::from)
                .unwrap_or_else(|_| home().join(".cargo"));
            let extra = [cargo_home.join("bin")];
            let spec = format!("{}@{}", krate, version); // "{}@{}" template 0x14c5afb98
            // 1. cargo-binstall when installed (0x14589f54c..0x14589f9e3):
            //    cargo-binstall -y [--disable-strategies quick-install] crate@ver --locked --root <dir>
            if cargo_binstall_enabled() {
                if let Some(binstall) = helper_program("cargo-binstall", None, &extra) {
                    let mut cmd = Command::new(&binstall);
                    with_helper_path(&mut cmd, &binstall);
                    cmd.arg("-y");
                    if !cargo_binstall_quickinstall() {
                        cmd.args(["--disable-strategies", "quick-install"]);
                    }
                    cmd.arg(&spec).arg("--locked").arg("--root").arg(dest);
                    match run_installer(cmd, dest, "cargo-binstall")? {
                        0 => return Ok(()),
                        BINSTALL_NO_PREBUILT_EXIT => {
                            warn("cargo-binstall found no prebuilt binary; falling back to cargo install")
                        }
                        c => return failed(tool, version, c),
                    }
                }
            }
            // 2. cargo install crate@ver --locked --root <dir> (options from 0x1458a4370).
            let cargo = helper_program("cargo", None, &extra).ok_or(CARGO_MISSING)?;
            let mut cmd = Command::new(&cargo);
            with_helper_path(&mut cmd, &cargo);
            cmd.args(["install", spec.as_str(), "--locked", "--root"]).arg(dest);
            match run_installer(cmd, dest, "cargo")? {
                0 => Ok(()),
                c => failed(tool, version, c),
            }
        }
        Backend::Pypi(pkg) => {
            // uv when available, pipx otherwise (setting `pypi.uvx`).
            let uv = if pipx_prefers_uv() { helper_program("uv", Some("uv"), &[]) } else { None };
            let mut cmd;
            match uv {
                Some(uv) => {
                    // UV_TOOL_DIR = install dir, UV_TOOL_BIN_DIR = install\bin (0x14374011b, 0x143740301).
                    cmd = Command::new(&uv);
                    with_helper_path(&mut cmd, &uv);
                    cmd.env("UV_TOOL_DIR", dest)
                        .env("UV_TOOL_BIN_DIR", dest.join("bin"))
                        .args(["tool", "install", "--force"])
                        // uv's own "not on your PATH" hint doesn't apply: dev puts bin/ on PATH.
                        .args(if verbose() { &[][..] } else { &["--quiet"][..] })
                        .arg(format!("{}=={}", pkg, version));
                }
                None => {
                    // PIPX_DEFAULT_BACKEND=pip (0x1436844b7), PIPX_HOME / PIPX_BIN_DIR.
                    let pipx = helper_program("pipx", None, &[])
                        .ok_or("neither uv nor pipx was found: install uv first (`dev use -g uv`)")?;
                    cmd = Command::new(&pipx);
                    with_helper_path(&mut cmd, &pipx);
                    cmd.env("PIPX_DEFAULT_BACKEND", "pip")
                        .env("PIPX_HOME", dest)
                        .env("PIPX_BIN_DIR", dest.join("bin"))
                        .args(["install", "--force"])
                        .arg(format!("{}=={}", pkg, version));
                }
            }
            match run_installer(cmd, dest, "pypi")? {
                0 => Ok(()),
                c => failed(tool, version, c),
            }
        }
        _ => err("not a package backend"),
    }
}

#[cfg(test)]
mod package_tests {
    use super::*;

    #[test]
    fn backends_from_names() {
        assert_eq!(Backend::of("npm:prettier"), Backend::Npm("prettier".into()));
        assert_eq!(Backend::of("npm:@biomejs/biome"), Backend::Npm("@biomejs/biome".into()));
        assert_eq!(Backend::of("cargo:ripgrep"), Backend::Cargo("ripgrep".into()));
        assert_eq!(Backend::of("pipx:black"), Backend::Pypi("black".into()));
        assert_eq!(Backend::of("pypi:ruff"), Backend::Pypi("ruff".into()));
    }

    #[test]
    fn crate_index_paths() {
        assert_eq!(crate_index_path("a"), "1/a");
        assert_eq!(crate_index_path("ab"), "2/ab");
        assert_eq!(crate_index_path("abc"), "3/a/abc");
        assert_eq!(crate_index_path("Serde"), "se/rd/serde");
    }

    #[test]
    fn scoped_spec_split() {
        assert_eq!(split_spec("npm:@biomejs/biome@1.9"), ("npm:@biomejs/biome".into(), Some("1.9".into())));
        assert_eq!(split_spec("npm:@biomejs/biome"), ("npm:@biomejs/biome".into(), None));
    }
}
