//! Workspace: projects, scratch folders, imported GitHub/GitLab repositories,
//! and the automatic (optional) cleanup of what `.gitignore` ignores in inactive repos.

use crate::config::{self, ConfigFile};
use crate::util::*;
use clap::{Args, Subcommand};
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

pub const DEFAULT_INACTIVE_DAYS: u64 = 30;
pub const DEFAULT_INTERVAL_HOURS: u64 = 24;
pub const DEFAULT_KEEP: &[&str] = &[".env", ".env.*", "*.local.*", "*.local", ".idea", ".vscode"];
const REGISTRY: &str = ".dev-workspace.toml";
const WALK_LIMIT: usize = 50_000;

#[derive(Args, Debug)]
pub struct WsArgs {
    #[command(subcommand)]
    pub cmd: Option<WsCmd>,
}

#[derive(Subcommand, Debug)]
pub enum WsCmd {
    /// Create the workspace folders (projects, scratch, repos) and remember where they are
    Init {
        /// Folder for the workspace; defaults to ~/dev-workspace
        path: Option<PathBuf>,
    },
    /// Show the workspace folder, how many entries it holds and the cleanup settings
    Status,
    /// List projects, scratch folders and imported repositories with their last activity
    #[command(visible_alias = "list")]
    Ls {
        /// Only this kind: projects, scratch or repos
        #[arg(long, short)]
        kind: Option<String>,
        /// Output as JSON
        #[arg(long, short = 'J')]
        json: bool,
    },
    /// Create a new project in the workspace
    New {
        /// Name of the project folder
        name: String,
        /// Tools to add to its dev.toml, such as node@24 (repeat for several)
        #[arg(long = "tool", short = 't')]
        tools: Vec<String>,
        /// Do not run `git init`
        #[arg(long)]
        no_git: bool,
    },
    /// Create a throwaway scratch folder (dated) and print its path
    Scratch {
        /// Optional name; the folder is <date>-<name>
        name: Option<String>,
    },
    /// Turn a scratch folder into a project
    Promote {
        /// Scratch folder name
        scratch: String,
        /// New project name (defaults to the scratch name without its date)
        name: Option<String>,
    },
    /// Import (clone) a repository from GitHub, GitLab or any git URL
    #[command(visible_alias = "clone")]
    Import {
        /// github:OWNER/REPO, gitlab:GROUP/REPO, gh:/gl:, an https:// or git@ URL, or OWNER/REPO
        source: String,
        /// Folder name to use instead of the repository's
        #[arg(long)]
        name: Option<String>,
        /// Branch to check out
        #[arg(long, short)]
        branch: Option<String>,
        /// Shallow clone with this many commits
        #[arg(long)]
        depth: Option<u32>,
        /// Clone over SSH (git@host:...) instead of HTTPS
        #[arg(long)]
        ssh: bool,
        /// Host for a self-hosted GitLab or GitHub Enterprise, such as gitlab.example.com
        #[arg(long)]
        host: Option<String>,
        /// Import every repository of a user, organization or group (github:USER / gitlab:GROUP)
        #[arg(long)]
        all: bool,
        /// With --all: include forks
        #[arg(long)]
        include_forks: bool,
        /// With --all: include archived repositories
        #[arg(long)]
        include_archived: bool,
        /// Show what would be cloned, without cloning
        #[arg(long, short = 'n')]
        dry_run: bool,
    },
    /// Fetch and fast-forward imported repositories (all, or one)
    #[command(visible_alias = "pull")]
    Sync {
        /// Only this repository
        name: Option<String>,
    },
    /// Print the folder of an entry (and count it as used)
    Path {
        name: String,
    },
    /// Delete an entry's folder from the workspace
    Rm {
        name: String,
        /// Do not ask for confirmation (same as the global --yes)
        #[arg(long, short)]
        force: bool,
    },
    /// Never clean this entry automatically
    Pin {
        name: String,
    },
    /// Allow automatic cleanup of this entry again
    Unpin {
        name: String,
    },
    /// Remove files ignored by .gitignore (dependencies, build output) from inactive repositories
    Clean {
        /// Only this entry (cleaned even if it was used recently)
        name: Option<String>,
        /// Inactive for at least this long, such as 30d or 2w (default: the setting)
        #[arg(long)]
        older_than: Option<String>,
        /// Clean every entry, active or not (pinned ones are still skipped)
        #[arg(long)]
        all: bool,
        /// Show what would be removed, without removing anything
        #[arg(long, short = 'n')]
        dry_run: bool,
    },
    /// Turn automatic cleanup on or off, or change when it happens
    #[command(name = "auto-clean")]
    AutoClean {
        /// on or off; leave out to show the current setting
        state: Option<String>,
        /// Days without activity before a repository is cleaned
        #[arg(long)]
        days: Option<u64>,
        /// Hours between automatic checks
        #[arg(long)]
        interval: Option<u64>,
    },
}

#[derive(Clone, Debug)]
pub struct WsSettings {
    pub root: PathBuf,
    pub auto_clean: bool,
    pub inactive_days: u64,
    pub interval_hours: u64,
    pub keep: Vec<String>,
}

pub fn settings() -> WsSettings {
    let global = global_config_path();
    let s = if global.is_file() {
        ConfigFile::load(&global, true).map(|c| c.settings).unwrap_or_default()
    } else {
        BTreeMap::new()
    };
    // The [workspace] table lives at the top level of the global config.
    let ws = read_workspace_table(&global);
    let get = |k: &str| ws.get(k).cloned().or_else(|| s.get(&format!("workspace.{}", k)).cloned());
    let root = std::env::var_os("DEV_WORKSPACE")
        .filter(|v| !v.is_empty())
        .map(PathBuf::from)
        .or_else(|| get("root").and_then(|v| v.as_str().map(|s| PathBuf::from(config::expand(s, &home())))))
        .unwrap_or_else(|| home().join("dev-workspace"));
    let auto_clean = match std::env::var("DEV_WORKSPACE_AUTO_CLEAN").ok().as_deref() {
        Some("0") | Some("false") | Some("off") => false,
        Some("1") | Some("true") | Some("on") => true,
        _ => get("auto_clean").and_then(|v| v.as_bool()).unwrap_or(true),
    };
    let inactive_days = get("inactive_days").and_then(|v| v.as_integer()).map(|i| i.max(1) as u64).unwrap_or(DEFAULT_INACTIVE_DAYS);
    let interval_hours = get("check_interval_hours").and_then(|v| v.as_integer()).map(|i| i.max(1) as u64).unwrap_or(DEFAULT_INTERVAL_HOURS);
    let keep = get("keep")
        .and_then(|v| v.as_array().map(|a| a.iter().filter_map(|x| x.as_str().map(String::from)).collect()))
        .unwrap_or_else(|| DEFAULT_KEEP.iter().map(|s| s.to_string()).collect());
    WsSettings { root, auto_clean, inactive_days, interval_hours, keep }
}

fn read_workspace_table(path: &Path) -> BTreeMap<String, toml::Value> {
    let text = match fs::read_to_string(path) {
        Ok(t) => t,
        Err(_) => return BTreeMap::new(),
    };
    let doc: toml::Table = match text.parse() {
        Ok(d) => d,
        Err(_) => return BTreeMap::new(),
    };
    match doc.get("workspace") {
        Some(toml::Value::Table(t)) => t.iter().map(|(k, v)| (k.clone(), v.clone())).collect(),
        _ => BTreeMap::new(),
    }
}

fn save_setting(key: &str, v: toml_edit::Value) -> Res<()> {
    config::set_dotted(&global_config_path(), "workspace", key, v)
}

#[derive(Serialize, Deserialize, Default, Clone, Debug)]
pub struct Meta {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub source: String,
    #[serde(default)]
    pub created: u64,
    #[serde(default)]
    pub last_used: u64,
    #[serde(default)]
    pub pinned: bool,
    #[serde(default)]
    pub last_cleaned: u64,
}

#[derive(Serialize, Deserialize, Default, Debug)]
pub struct Registry {
    #[serde(default)]
    pub entries: BTreeMap<String, Meta>,
}

impl Registry {
    pub fn load(root: &Path) -> Registry {
        fs::read_to_string(root.join(REGISTRY))
            .ok()
            .and_then(|t| toml::from_str(&t).ok())
            .unwrap_or_default()
    }
    pub fn save(&self, root: &Path) -> Res<()> {
        let mut text = String::from("# dev workspace registry (managed by `dev workspace`)\n");
        text.push_str(&toml::to_string_pretty(self).map_err(|e| e.to_string())?);
        write_file(&root.join(REGISTRY), &text)
    }
}

#[derive(Clone, Debug, PartialEq)]
pub enum Kind {
    Project,
    Scratch,
    Repo,
}

impl Kind {
    fn label(&self) -> &'static str {
        match self {
            Kind::Project => "project",
            Kind::Scratch => "scratch",
            Kind::Repo => "repo",
        }
    }
    fn folder(&self) -> &'static str {
        match self {
            Kind::Project => "projects",
            Kind::Scratch => "scratch",
            Kind::Repo => "repos",
        }
    }
}

#[derive(Clone, Debug)]
pub struct Entry {
    pub kind: Kind,
    /// Path inside its kind folder, with `/` separators (`github.com/jdx/mise`).
    pub name: String,
    pub path: PathBuf,
    /// Registry key: `<kind folder>/<name>`.
    pub key: String,
}

pub fn discover(root: &Path) -> Vec<Entry> {
    let mut out = vec![];
    for kind in [Kind::Project, Kind::Scratch] {
        if let Ok(rd) = fs::read_dir(root.join(kind.folder())) {
            for e in rd.flatten() {
                if e.path().is_dir() && !e.file_name().to_string_lossy().starts_with('.') {
                    let name = e.file_name().to_string_lossy().into_owned();
                    out.push(Entry { key: format!("{}/{}", kind.folder(), name), kind: kind.clone(), name, path: e.path() });
                }
            }
        }
    }
    let repos = root.join("repos");
    let mut stack = vec![(repos.clone(), 0)];
    while let Some((d, depth)) = stack.pop() {
        if depth > 0 && d.join(".git").exists() {
            let rel = d.strip_prefix(&repos).unwrap().to_string_lossy().replace('\\', "/");
            out.push(Entry { key: format!("repos/{}", rel), kind: Kind::Repo, name: rel, path: d });
            continue;
        }
        if depth >= 6 {
            continue;
        }
        if let Ok(rd) = fs::read_dir(&d) {
            for e in rd.flatten() {
                if e.path().is_dir() && !e.file_name().to_string_lossy().starts_with('.') {
                    stack.push((e.path(), depth + 1));
                }
            }
        }
    }
    out.sort_by(|a, b| (a.kind.folder(), &a.name).cmp(&(b.kind.folder(), &b.name)));
    out
}

/// Finds an entry by its full name, `owner/repo`, or its last folder name.
pub fn find(root: &Path, name: &str) -> Res<Entry> {
    let name = name.replace('\\', "/").trim_matches('/').to_string();
    let all = discover(root);
    if let Some(e) = all.iter().find(|e| e.name == name || e.key == name) {
        return Ok(e.clone());
    }
    let hits: Vec<&Entry> = all
        .iter()
        .filter(|e| e.name.ends_with(&format!("/{}", name)) || e.name.rsplit('/').next() == Some(name.as_str()))
        .collect();
    match hits.len() {
        0 => err(format!("no workspace entry named {} (see `dev workspace ls`)", name)),
        1 => Ok(hits[0].clone()),
        _ => err(format!(
            "{} matches several entries: {}",
            name,
            hits.iter().map(|e| e.key.clone()).collect::<Vec<_>>().join(", ")
        )),
    }
}

/// When the entry was last worked on: dev use, files git doesn't ignore, git activity.
pub fn last_activity(e: &Entry, meta: Option<&Meta>) -> u64 {
    let mut newest = meta.map(|m| m.last_used.max(m.created)).unwrap_or(0);
    for f in [".git/index", ".git/HEAD", ".git/FETCH_HEAD", ".git/ORIG_HEAD"] {
        if let Ok(m) = fs::metadata(e.path.join(f)) {
            if let Ok(t) = m.modified() {
                newest = newest.max(secs_of(t));
            }
        }
    }
    let walker = ignore::WalkBuilder::new(&e.path)
        .hidden(false)
        .git_ignore(true)
        .git_exclude(true)
        .git_global(false)
        .require_git(false)
        .filter_entry(|d| d.file_name() != ".git")
        .build();
    for (i, entry) in walker.flatten().enumerate() {
        if i > WALK_LIMIT {
            break;
        }
        if entry.file_type().map(|t| t.is_file()).unwrap_or(false) {
            if let Ok(m) = entry.metadata() {
                if let Ok(t) = m.modified() {
                    newest = newest.max(secs_of(t));
                }
            }
        }
    }
    newest
}

pub fn run(args: WsArgs) -> Res<i32> {
    let s = settings();
    match args.cmd.unwrap_or(WsCmd::Status) {
        WsCmd::Init { path } => init(path),
        WsCmd::Status => status(&s),
        WsCmd::Ls { kind, json } => ls(&s, kind, json),
        WsCmd::New { name, tools, no_git } => new_project(&s, &name, &tools, no_git),
        WsCmd::Scratch { name } => scratch(&s, name),
        WsCmd::Promote { scratch, name } => promote(&s, &scratch, name),
        WsCmd::Import { source, name, branch, depth, ssh, host, all, include_forks, include_archived, dry_run } => {
            let opts = ImportOpts { name, branch, depth, ssh, host, include_forks, include_archived, dry_run };
            if all {
                import_all(&s, &source, &opts)
            } else {
                import_one(&s, &source, &opts).map(|_| 0)
            }
        }
        WsCmd::Sync { name } => sync(&s, name),
        WsCmd::Path { name } => {
            let e = find(&s.root, &name)?;
            touch(&s.root, &e.key);
            println!("{}", e.path.display());
            Ok(0)
        }
        WsCmd::Rm { name, force } => rm(&s, &name, force || std::env::var("DEV_YES").is_ok()),
        WsCmd::Pin { name } => set_pin(&s, &name, true),
        WsCmd::Unpin { name } => set_pin(&s, &name, false),
        WsCmd::Clean { name, older_than, all, dry_run } => {
            let age = match older_than {
                Some(o) => parse_duration(&o)?,
                None => s.inactive_days * 86_400,
            };
            let report = clean(&s, name.as_deref(), age, all, dry_run, false)?;
            print_report(&report, dry_run);
            Ok(0)
        }
        WsCmd::AutoClean { state, days, interval } => auto_clean_cmd(state, days, interval),
    }
}

fn ensure_root(s: &WsSettings) -> Res<()> {
    if !s.root.is_dir() {
        for k in ["projects", "scratch", "repos"] {
            fs::create_dir_all(s.root.join(k)).map_err(|e| format!("{}: {}", s.root.display(), e))?;
        }
        info(format!("workspace created at {}", display_path(&s.root)));
    }
    Ok(())
}

fn init(path: Option<PathBuf>) -> Res<i32> {
    let cwd = std::env::current_dir().map_err(|e| e.to_string())?;
    let root = match path {
        Some(p) if p.is_absolute() => p,
        Some(p) => cwd.join(p),
        None => settings().root,
    };
    for k in ["projects", "scratch", "repos"] {
        fs::create_dir_all(root.join(k)).map_err(|e| format!("{}: {}", root.display(), e))?;
    }
    let reg = Registry::load(&root);
    reg.save(&root)?;
    save_setting("root", toml_edit::Value::from(root.display().to_string()))?;
    println!("workspace ready at {}", root.display());
    println!("  projects  {}", root.join("projects").display());
    println!("  scratch   {}", root.join("scratch").display());
    println!("  repos     {}", root.join("repos").display());
    Ok(0)
}

fn status(s: &WsSettings) -> Res<i32> {
    println!("workspace       {}", display_path(&s.root));
    if !s.root.is_dir() {
        println!("                (not created yet; run `dev workspace init`)");
    }
    let all = discover(&s.root);
    let count = |k: Kind| all.iter().filter(|e| e.kind == k).count();
    println!("projects        {}", count(Kind::Project));
    println!("scratch         {}", count(Kind::Scratch));
    println!("repos           {}", count(Kind::Repo));
    println!("auto-clean      {}", if s.auto_clean { "on" } else { "off" });
    println!("inactive after  {} days", s.inactive_days);
    println!("check every     {} hours", s.interval_hours);
    println!("always kept     {}", s.keep.join(", "));
    let last = fs::read_to_string(last_check_file()).ok().and_then(|t| t.trim().parse::<u64>().ok()).unwrap_or(0);
    println!("last check      {}", if last == 0 { "never".to_string() } else { format!("{} ({})", date_of(last), ago(last)) });
    Ok(0)
}

fn ls(s: &WsSettings, kind: Option<String>, json: bool) -> Res<i32> {
    let reg = Registry::load(&s.root);
    let want = kind.map(|k| k.trim_end_matches('s').to_ascii_lowercase());
    let entries: Vec<Entry> = discover(&s.root)
        .into_iter()
        .filter(|e| match &want {
            Some(w) => e.kind.label() == w || (w == "repo" && e.kind == Kind::Repo) || (w == "project" && e.kind == Kind::Project),
            None => true,
        })
        .collect();
    let limit = s.inactive_days * 86_400;
    let mut rows = vec![];
    for e in &entries {
        let meta = reg.entries.get(&e.key);
        let act = last_activity(e, meta);
        let idle = now_secs().saturating_sub(act);
        let has_gi = e.path.join(".gitignore").is_file();
        let state = if meta.map(|m| m.pinned).unwrap_or(false) {
            "pinned"
        } else if !has_gi {
            "no .gitignore"
        } else if meta.map(|m| m.last_cleaned >= act && m.last_cleaned > 0).unwrap_or(false) {
            "cleaned"
        } else if idle >= limit {
            "inactive"
        } else {
            "active"
        };
        rows.push((e, act, state, meta.map(|m| m.source.clone()).unwrap_or_default()));
    }
    if json {
        let arr: Vec<serde_json::Value> = rows
            .iter()
            .map(|(e, act, st, src)| {
                serde_json::json!({
                    "name": e.name, "kind": e.kind.label(), "path": e.path.display().to_string(),
                    "last_activity": date_of(*act), "status": st, "source": src,
                })
            })
            .collect();
        println!("{}", serde_json::to_string_pretty(&arr).unwrap());
        return Ok(0);
    }
    if rows.is_empty() {
        eprintln!("{} workspace is empty; try `dev workspace new NAME`, `dev workspace scratch` or `dev workspace import github:OWNER/REPO`", NAME);
        return Ok(0);
    }
    let w = rows.iter().map(|r| r.0.name.len()).max().unwrap_or(4).max(4);
    println!("{:<w$}  {:<7}  {:<14}  {}", "Name", "Kind", "Last active", "Status", w = w);
    for (e, act, st, _) in rows {
        println!("{:<w$}  {:<7}  {:<14}  {}", e.name, e.kind.label(), ago(act), st, w = w);
    }
    Ok(0)
}

fn touch(root: &Path, key: &str) {
    let mut reg = Registry::load(root);
    let m = reg.entries.entry(key.to_string()).or_default();
    m.last_used = now_secs();
    let _ = reg.save(root);
}

const GITIGNORE_TEMPLATE: &str = "# Dependencies and build output (dev workspace cleans these when the project sits idle)\nnode_modules/\ntarget/\ndist/\nbuild/\n.venv/\n__pycache__/\n*.log\n\n# Local secrets (never cleaned)\n.env\n.env.*\n";

fn new_project(s: &WsSettings, name: &str, tools: &[String], no_git: bool) -> Res<i32> {
    check_name(name)?;
    ensure_root(s)?;
    let dir = s.root.join("projects").join(name);
    if dir.exists() {
        return err(format!("project {} already exists at {}", name, dir.display()));
    }
    fs::create_dir_all(&dir).map_err(|e| e.to_string())?;
    let mut toml_text = String::from("[tools]\n");
    for t in tools {
        let (tool, ver) = crate::tools::split_spec(t);
        toml_text.push_str(&format!("{} = \"{}\"\n", toml_key(&tool), ver.unwrap_or_else(|| "latest".into())));
    }
    toml_text.push_str("\n[env]\n\n[tasks.hello]\ndescription = \"Say hello\"\nrun = \"echo hello from ");
    toml_text.push_str(name);
    toml_text.push_str("\"\n");
    write_file(&dir.join("dev.toml"), &toml_text)?;
    write_file(&dir.join(".gitignore"), GITIGNORE_TEMPLATE)?;
    write_file(&dir.join("README.md"), &format!("# {}\n", name))?;
    if !no_git && git_available() {
        let ok = Command::new("git").arg("init").arg("-q").arg(&dir).status().map(|s| s.success()).unwrap_or(false);
        if !ok {
            warn("git init failed; the project was created without git");
        }
    }
    let key = format!("projects/{}", name);
    let mut reg = Registry::load(&s.root);
    reg.entries.insert(key, Meta { created: now_secs(), last_used: now_secs(), ..Default::default() });
    reg.save(&s.root)?;
    info(format!("project {} created", name));
    println!("{}", dir.display());
    Ok(0)
}

fn toml_key(k: &str) -> String {
    if k.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_') {
        k.to_string()
    } else {
        format!("\"{}\"", k)
    }
}

fn check_name(name: &str) -> Res<()> {
    if name.is_empty() || name.contains(['/', '\\', ':', '*', '?', '"', '<', '>', '|']) || name == "." || name == ".." {
        return err(format!("invalid name: {}", name));
    }
    Ok(())
}

fn scratch(s: &WsSettings, name: Option<String>) -> Res<i32> {
    ensure_root(s)?;
    let base = match &name {
        Some(n) => {
            check_name(n)?;
            format!("{}-{}", today(), n)
        }
        None => format!("{}-scratch", today()),
    };
    let dir_root = s.root.join("scratch");
    let mut dir = dir_root.join(&base);
    let mut n = 2;
    while dir.exists() {
        dir = dir_root.join(format!("{}-{}", base, n));
        n += 1;
    }
    fs::create_dir_all(&dir).map_err(|e| e.to_string())?;
    let folder = dir.file_name().unwrap().to_string_lossy().into_owned();
    let mut reg = Registry::load(&s.root);
    reg.entries.insert(format!("scratch/{}", folder), Meta { created: now_secs(), last_used: now_secs(), ..Default::default() });
    reg.save(&s.root)?;
    info(format!("scratch {} created", folder));
    println!("{}", dir.display());
    Ok(0)
}

fn promote(s: &WsSettings, scratch: &str, name: Option<String>) -> Res<i32> {
    let e = find(&s.root, scratch)?;
    if e.kind != Kind::Scratch {
        return err(format!("{} is not a scratch folder", e.name));
    }
    let new_name = name.unwrap_or_else(|| {
        // Drop the leading YYYY-MM-DD- of the scratch name.
        let n = &e.name;
        if n.len() > 11 && n.as_bytes()[4] == b'-' && n.as_bytes()[10] == b'-' {
            n[11..].to_string()
        } else {
            n.clone()
        }
    });
    check_name(&new_name)?;
    let dest = s.root.join("projects").join(&new_name);
    if dest.exists() {
        return err(format!("project {} already exists", new_name));
    }
    fs::create_dir_all(s.root.join("projects")).map_err(|e| e.to_string())?;
    fs::rename(&e.path, &dest).map_err(|er| format!("could not move {}: {}", e.path.display(), er))?;
    if !dest.join(".gitignore").exists() {
        write_file(&dest.join(".gitignore"), GITIGNORE_TEMPLATE)?;
    }
    let mut reg = Registry::load(&s.root);
    let mut meta = reg.entries.remove(&e.key).unwrap_or_default();
    meta.last_used = now_secs();
    reg.entries.insert(format!("projects/{}", new_name), meta);
    reg.save(&s.root)?;
    info(format!("scratch {} is now project {}", e.name, new_name));
    println!("{}", dest.display());
    Ok(0)
}

pub struct ImportOpts {
    pub name: Option<String>,
    pub branch: Option<String>,
    pub depth: Option<u32>,
    pub ssh: bool,
    pub host: Option<String>,
    pub include_forks: bool,
    pub include_archived: bool,
    pub dry_run: bool,
}

#[derive(Debug, PartialEq)]
pub struct Source {
    pub url: String,
    pub host: String,
    /// `owner/repo` or `group/sub/repo`
    pub path: String,
}

/// Understands github:, gitlab:, gh:, gl:, https://, ssh:// and git@ forms, or OWNER/REPO.
pub fn parse_source(src: &str, ssh: bool, host: Option<&str>) -> Res<Source> {
    let s = src.trim();
    let (host_name, path) = if let Some(r) = s.strip_prefix("github:").or_else(|| s.strip_prefix("gh:")) {
        (host.unwrap_or("github.com").to_string(), r.to_string())
    } else if let Some(r) = s.strip_prefix("gitlab:").or_else(|| s.strip_prefix("gl:")) {
        (host.unwrap_or("gitlab.com").to_string(), r.to_string())
    } else if let Some(r) = s.strip_prefix("https://").or_else(|| s.strip_prefix("http://")) {
        let mut it = r.splitn(2, '/');
        let h = it.next().unwrap_or("").to_string();
        let h = h.rsplit('@').next().unwrap_or("").to_string();
        (h, it.next().unwrap_or("").to_string())
    } else if let Some(r) = s.strip_prefix("ssh://") {
        let r = r.rsplit_once('@').map(|x| x.1).unwrap_or(r);
        let mut it = r.splitn(2, '/');
        let h = it.next().unwrap_or("").split(':').next().unwrap_or("").to_string();
        (h, it.next().unwrap_or("").to_string())
    } else if let Some((user_host, p)) = s.split_once(':').filter(|(a, _)| a.contains('@')) {
        (user_host.rsplit('@').next().unwrap_or("").to_string(), p.to_string())
    } else if s.matches('/').count() >= 1 && !s.contains(':') {
        (host.unwrap_or("github.com").to_string(), s.to_string())
    } else {
        return err(format!("not a repository: {} (use github:OWNER/REPO, gitlab:GROUP/REPO or a git URL)", src));
    };
    let path = path.trim_matches('/').trim_end_matches(".git").to_string();
    let path = path.split("/-/").next().unwrap_or(&path).to_string();
    if host_name.is_empty() || path.split('/').filter(|p| !p.is_empty()).count() < 2 {
        return err(format!("not a repository: {} (expected OWNER/REPO)", src));
    }
    let keep_original = s.starts_with("https://") || s.starts_with("http://") || s.starts_with("ssh://") || s.contains('@');
    let url = if ssh {
        format!("git@{}:{}.git", host_name, path)
    } else if keep_original {
        s.to_string()
    } else {
        format!("https://{}/{}.git", host_name, path)
    };
    Ok(Source { url, host: host_name, path })
}

fn import_one(s: &WsSettings, source: &str, o: &ImportOpts) -> Res<PathBuf> {
    let src = parse_source(source, o.ssh, o.host.as_deref())?;
    let mut rel = format!("{}/{}", src.host, src.path);
    if let Some(n) = &o.name {
        check_name(n)?;
        let parent = rel.rsplit_once('/').map(|x| x.0.to_string()).unwrap_or_default();
        rel = format!("{}/{}", parent, n);
    }
    let dest = s.root.join("repos").join(rel.replace('/', "\\"));
    if dest.join(".git").exists() {
        info(format!("{} is already imported at {}", rel, dest.display()));
        return Ok(dest);
    }
    if o.dry_run {
        println!("would clone {} into {}", src.url, dest.display());
        return Ok(dest);
    }
    if !git_available() {
        return err("git is not installed; install it from https://git-scm.com and try again");
    }
    ensure_root(s)?;
    fs::create_dir_all(dest.parent().unwrap()).map_err(|e| e.to_string())?;
    info(format!("cloning {}", src.url));
    let mut cmd = Command::new("git");
    cmd.arg("clone");
    if let Some(d) = o.depth {
        cmd.arg("--depth").arg(d.to_string());
    }
    if let Some(b) = &o.branch {
        cmd.arg("--branch").arg(b);
    }
    cmd.arg(&src.url).arg(&dest);
    let st = cmd.status().map_err(|e| format!("could not run git: {}", e))?;
    if !st.success() {
        let _ = remove_path(&dest);
        return err(format!("git clone of {} failed", src.url));
    }
    let mut reg = Registry::load(&s.root);
    reg.entries.insert(
        format!("repos/{}", rel),
        Meta { source: src.url.clone(), created: now_secs(), last_used: now_secs(), ..Default::default() },
    );
    reg.save(&s.root)?;
    info(format!("{} imported", rel));
    println!("{}", dest.display());
    Ok(dest)
}

/// Lists a GitHub user's/org's or a GitLab group's/user's repositories and imports each one.
fn import_all(s: &WsSettings, source: &str, o: &ImportOpts) -> Res<i32> {
    let (provider, owner) = if let Some(r) = source.strip_prefix("github:").or_else(|| source.strip_prefix("gh:")) {
        ("github", r)
    } else if let Some(r) = source.strip_prefix("gitlab:").or_else(|| source.strip_prefix("gl:")) {
        ("gitlab", r)
    } else {
        return err("--all needs github:USER_OR_ORG or gitlab:GROUP_OR_USER");
    };
    let owner = owner.trim_matches('/');
    let mut repos: Vec<String> = vec![];
    if provider == "github" {
        let api = match &o.host {
            Some(h) => format!("https://{}/api/v3", h),
            None => "https://api.github.com".into(),
        };
        for page in 1..=20 {
            let url = format!("{}/users/{}/repos?per_page=100&page={}&type=owner", api, owner, page);
            let text = http_get(&url, Some("GITHUB_TOKEN"))?;
            let j: serde_json::Value = serde_json::from_str(&text).map_err(|e| e.to_string())?;
            let arr = j.as_array().cloned().unwrap_or_default();
            if arr.is_empty() {
                break;
            }
            for r in &arr {
                if (!o.include_forks && r["fork"].as_bool().unwrap_or(false))
                    || (!o.include_archived && r["archived"].as_bool().unwrap_or(false))
                {
                    continue;
                }
                if let Some(n) = r["full_name"].as_str() {
                    repos.push(format!("github:{}", n));
                }
            }
            if arr.len() < 100 {
                break;
            }
        }
    } else {
        let host = o.host.clone().unwrap_or_else(|| "gitlab.com".into());
        let enc = owner.replace('/', "%2F");
        let mut endpoint = format!("https://{}/api/v4/groups/{}/projects?include_subgroups=true", host, enc);
        if http_get(&format!("{}&per_page=1", endpoint), Some("GITLAB_TOKEN")).is_err() {
            endpoint = format!("https://{}/api/v4/users/{}/projects?", host, enc);
        }
        for page in 1..=20 {
            let url = format!("{}&per_page=100&page={}", endpoint, page);
            let text = http_get(&url, Some("GITLAB_TOKEN"))?;
            let j: serde_json::Value = serde_json::from_str(&text).map_err(|e| e.to_string())?;
            let arr = j.as_array().cloned().unwrap_or_default();
            if arr.is_empty() {
                break;
            }
            for r in &arr {
                if (!o.include_forks && r.get("forked_from_project").map(|f| !f.is_null()).unwrap_or(false))
                    || (!o.include_archived && r["archived"].as_bool().unwrap_or(false))
                {
                    continue;
                }
                if let Some(n) = r["path_with_namespace"].as_str() {
                    repos.push(format!("gitlab:{}", n));
                }
            }
            if arr.len() < 100 {
                break;
            }
        }
    }
    if repos.is_empty() {
        info(format!("no repositories found for {}", source));
        return Ok(0);
    }
    info(format!("{} repositories found for {}", repos.len(), source));
    let mut failed = 0;
    for r in &repos {
        let one = ImportOpts {
            name: None,
            branch: None,
            depth: o.depth,
            ssh: o.ssh,
            host: o.host.clone(),
            include_forks: false,
            include_archived: false,
            dry_run: o.dry_run,
        };
        if let Err(e) = import_one(s, r, &one) {
            eprintln!("{} ERROR {}", NAME, e);
            failed += 1;
        }
    }
    Ok(if failed > 0 { 1 } else { 0 })
}

fn sync(s: &WsSettings, name: Option<String>) -> Res<i32> {
    let entries: Vec<Entry> = match name {
        Some(n) => vec![find(&s.root, &n)?],
        None => discover(&s.root).into_iter().filter(|e| e.kind == Kind::Repo).collect(),
    };
    if !git_available() {
        return err("git is not installed");
    }
    let mut failed = 0;
    for e in entries.iter().filter(|e| e.path.join(".git").exists()) {
        let st = Command::new("git").arg("-C").arg(&e.path).args(["pull", "--ff-only", "-q"]).status();
        match st {
            Ok(st) if st.success() => println!("{}  ✓ up to date", e.name),
            _ => {
                println!("{}  ✗ could not fast-forward (local changes or diverged)", e.name);
                failed += 1;
            }
        }
    }
    Ok(if failed > 0 { 1 } else { 0 })
}

fn rm(s: &WsSettings, name: &str, yes: bool) -> Res<i32> {
    let e = find(&s.root, name)?;
    if !yes && !confirm(&format!("Delete {} ({})?", e.name, e.path.display()), false) {
        info("nothing deleted");
        return Ok(1);
    }
    remove_path(&e.path)?;
    // Remove empty owner/host folders left behind by repos.
    let mut p = e.path.parent().map(Path::to_path_buf);
    let stop = s.root.join(e.kind.folder());
    while let Some(d) = p {
        if d == stop || fs::read_dir(&d).map(|mut r| r.next().is_some()).unwrap_or(true) {
            break;
        }
        let _ = fs::remove_dir(&d);
        p = d.parent().map(Path::to_path_buf);
    }
    let mut reg = Registry::load(&s.root);
    reg.entries.remove(&e.key);
    reg.save(&s.root)?;
    info(format!("{} deleted", e.name));
    Ok(0)
}

fn set_pin(s: &WsSettings, name: &str, pinned: bool) -> Res<i32> {
    let e = find(&s.root, name)?;
    let mut reg = Registry::load(&s.root);
    reg.entries.entry(e.key.clone()).or_default().pinned = pinned;
    reg.save(&s.root)?;
    info(format!("{} {}", e.name, if pinned { "pinned: it will never be cleaned automatically" } else { "unpinned" }));
    Ok(0)
}

fn auto_clean_cmd(state: Option<String>, days: Option<u64>, interval: Option<u64>) -> Res<i32> {
    if let Some(st) = state {
        let on = match st.to_ascii_lowercase().as_str() {
            "on" | "true" | "1" | "yes" | "enable" | "ligar" | "sim" => true,
            "off" | "false" | "0" | "no" | "disable" | "desligar" | "nao" | "não" => false,
            _ => return err(format!("expected on or off, got {}", st)),
        };
        save_setting("auto_clean", toml_edit::Value::from(on))?;
    }
    if let Some(d) = days {
        if d == 0 {
            return err("--days must be at least 1");
        }
        save_setting("inactive_days", toml_edit::Value::from(d as i64))?;
    }
    if let Some(h) = interval {
        if h == 0 {
            return err("--interval must be at least 1");
        }
        save_setting("check_interval_hours", toml_edit::Value::from(h as i64))?;
    }
    let s = settings();
    println!("auto-clean      {}", if s.auto_clean { "on" } else { "off" });
    println!("inactive after  {} days", s.inactive_days);
    println!("check every     {} hours", s.interval_hours);
    Ok(0)
}

pub struct CleanItem {
    pub entry: String,
    pub path: PathBuf,
    pub bytes: u64,
}

#[derive(Default)]
pub struct CleanReport {
    pub items: Vec<CleanItem>,
    pub entries_cleaned: usize,
    pub skipped: Vec<(String, String)>,
}

/// Cleans gitignored files from entries idle for at least `min_idle` seconds.
pub fn clean(s: &WsSettings, only: Option<&str>, min_idle: u64, all: bool, dry_run: bool, quiet: bool) -> Res<CleanReport> {
    let mut reg = Registry::load(&s.root);
    let entries = match only {
        Some(n) => vec![find(&s.root, n)?],
        None => discover(&s.root),
    };
    let mut report = CleanReport::default();
    for e in entries {
        let meta = reg.entries.get(&e.key).cloned().unwrap_or_default();
        if meta.pinned && only.is_none() {
            report.skipped.push((e.name.clone(), "pinned".into()));
            continue;
        }
        if !e.path.join(".gitignore").is_file() {
            report.skipped.push((e.name.clone(), "no .gitignore".into()));
            continue;
        }
        let act = last_activity(&e, Some(&meta));
        if only.is_none() && !all {
            if now_secs().saturating_sub(act) < min_idle {
                report.skipped.push((e.name.clone(), format!("active {}", ago(act))));
                continue;
            }
            if meta.last_cleaned > 0 && meta.last_cleaned >= act {
                report.skipped.push((e.name.clone(), "already clean".into()));
                continue;
            }
        }
        let paths = ignored_paths(&e.path, &s.keep);
        let mut any = false;
        for p in paths {
            let bytes = dir_size(&p);
            if !dry_run {
                if let Err(er) = remove_except(&p, &e.path, &s.keep) {
                    if !quiet {
                        warn(er);
                    }
                    continue;
                }
            }
            any = true;
            report.items.push(CleanItem { entry: e.name.clone(), path: p, bytes });
        }
        if any {
            report.entries_cleaned += 1;
        }
        if !dry_run {
            let m = reg.entries.entry(e.key.clone()).or_default();
            m.last_cleaned = now_secs();
        }
    }
    if !dry_run && s.root.is_dir() {
        reg.save(&s.root)?;
    }
    Ok(report)
}

fn print_report(r: &CleanReport, dry_run: bool) {
    let total: u64 = r.items.iter().map(|i| i.bytes).sum();
    for i in &r.items {
        println!("{}  {}  {}", if dry_run { "would remove" } else { "removed" }, i.path.display(), human_size(i.bytes));
    }
    for (n, why) in &r.skipped {
        if verbose() {
            eprintln!("{} skipped {} ({})", NAME, n, why);
        }
    }
    eprintln!(
        "{} {} {} item(s) from {} entr{}, {}",
        NAME,
        if dry_run { "would remove" } else { "removed" },
        r.items.len(),
        r.entries_cleaned,
        if r.entries_cleaned == 1 { "y" } else { "ies" },
        human_size(total)
    );
}

/// Paths git (or the root .gitignore, without git) ignores, minus the kept patterns.
pub fn ignored_paths(dir: &Path, keep: &[String]) -> Vec<PathBuf> {
    let mut out = vec![];
    if dir.join(".git").exists() && git_available() {
        let res = Command::new("git")
            .arg("-C")
            .arg(dir)
            .args(["ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z"])
            .output();
        if let Ok(o) = res {
            if o.status.success() {
                for rel in o.stdout.split(|b| *b == 0).filter(|x| !x.is_empty()) {
                    let rel = String::from_utf8_lossy(rel).trim_end_matches('/').to_string();
                    let p = dir.join(rel.replace('/', "\\"));
                    if !is_kept(&p, dir, keep) {
                        out.push(p);
                    }
                }
                return out;
            }
        }
    }
    // Without git: match the root .gitignore ourselves.
    let mut b = ignore::gitignore::GitignoreBuilder::new(dir);
    b.add(dir.join(".gitignore"));
    let gi = match b.build() {
        Ok(g) => g,
        Err(_) => return out,
    };
    let mut stack = vec![dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        let rd = match fs::read_dir(&d) {
            Ok(r) => r,
            Err(_) => continue,
        };
        for e in rd.flatten() {
            let p = e.path();
            if e.file_name() == ".git" {
                continue;
            }
            let is_dir = p.is_dir();
            if gi.matched(&p, is_dir).is_ignore() {
                if !is_kept(&p, dir, keep) {
                    out.push(p);
                }
            } else if is_dir {
                stack.push(p);
            }
        }
    }
    out.sort();
    out
}

fn is_kept(p: &Path, root: &Path, keep: &[String]) -> bool {
    let rel = p.strip_prefix(root).unwrap_or(p);
    rel.components().any(|c| {
        let n = c.as_os_str().to_string_lossy();
        keep.iter().any(|k| wildcard(k, &n))
    }) || p.file_name().map(|n| n == ".gitignore" || n == REGISTRY).unwrap_or(false)
}

/// Removes a path but leaves anything inside it that matches a kept pattern.
fn remove_except(p: &Path, root: &Path, keep: &[String]) -> Res<()> {
    if p.is_dir() && contains_kept(p, root, keep) {
        for e in fs::read_dir(p).map_err(|e| e.to_string())?.flatten() {
            let c = e.path();
            if !is_kept(&c, root, keep) {
                remove_except(&c, root, keep)?;
            }
        }
        return Ok(());
    }
    remove_path(p)
}

fn contains_kept(d: &Path, root: &Path, keep: &[String]) -> bool {
    let mut stack = vec![d.to_path_buf()];
    let mut seen = 0;
    while let Some(x) = stack.pop() {
        seen += 1;
        if seen > WALK_LIMIT {
            return true; // too big to be sure: be careful and walk it piece by piece
        }
        if let Ok(rd) = fs::read_dir(&x) {
            for e in rd.flatten() {
                let p = e.path();
                if is_kept(&p, root, keep) {
                    return true;
                }
                if fs::symlink_metadata(&p).map(|m| m.is_dir()).unwrap_or(false) {
                    stack.push(p);
                }
            }
        }
    }
    false
}

/// `*` and `?` wildcards, case-insensitive (Windows file names).
pub fn wildcard(pattern: &str, text: &str) -> bool {
    let p: Vec<char> = pattern.to_lowercase().chars().collect();
    let t: Vec<char> = text.to_lowercase().chars().collect();
    let (mut pi, mut ti, mut star, mut mark) = (0usize, 0usize, None, 0usize);
    while ti < t.len() {
        if pi < p.len() && (p[pi] == '?' || p[pi] == t[ti]) {
            pi += 1;
            ti += 1;
        } else if pi < p.len() && p[pi] == '*' {
            star = Some(pi);
            mark = ti;
            pi += 1;
        } else if let Some(sp) = star {
            pi = sp + 1;
            mark += 1;
            ti = mark;
        } else {
            return false;
        }
    }
    while pi < p.len() && p[pi] == '*' {
        pi += 1;
    }
    pi == p.len()
}

fn git_available() -> bool {
    Command::new("git").arg("--version").output().map(|o| o.status.success()).unwrap_or(false)
}

fn last_check_file() -> PathBuf {
    state_dir().join("workspace-last-clean")
}

/// Runs the automatic cleanup at most once per interval, when it is turned on.
pub fn maybe_auto_clean() {
    let s = settings();
    if !s.auto_clean || !s.root.is_dir() {
        return;
    }
    let f = last_check_file();
    let last = fs::read_to_string(&f).ok().and_then(|t| t.trim().parse::<u64>().ok()).unwrap_or(0);
    if now_secs().saturating_sub(last) < s.interval_hours * 3600 {
        return;
    }
    let _ = write_file(&f, &now_secs().to_string());
    if let Ok(r) = clean(&s, None, s.inactive_days * 86_400, false, false, true) {
        if !r.items.is_empty() {
            let total: u64 = r.items.iter().map(|i| i.bytes).sum();
            eprintln!(
                "{} workspace auto-clean: freed {} from {} inactive entr{} (turn off with `dev workspace auto-clean off`)",
                NAME,
                human_size(total),
                r.entries_cleaned,
                if r.entries_cleaned == 1 { "y" } else { "ies" }
            );
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sources() {
        let s = parse_source("github:jdx/mise", false, None).unwrap();
        assert_eq!(s.url, "https://github.com/jdx/mise.git");
        assert_eq!(s.path, "jdx/mise");
        let s = parse_source("gitlab:group/sub/repo", false, None).unwrap();
        assert_eq!(s.host, "gitlab.com");
        assert_eq!(s.path, "group/sub/repo");
        let s = parse_source("git@gitlab.com:a/b.git", false, None).unwrap();
        assert_eq!((s.host.as_str(), s.path.as_str()), ("gitlab.com", "a/b"));
        let s = parse_source("https://github.com/a/b", true, None).unwrap();
        assert_eq!(s.url, "git@github.com:a/b.git");
        let s = parse_source("https://gitlab.com/g/r/-/tree/main", false, None).unwrap();
        assert_eq!(s.path, "g/r");
        assert!(parse_source("nope", false, None).is_err());
    }

    #[test]
    fn wild() {
        assert!(wildcard(".env.*", ".env.local"));
        assert!(wildcard("*.local.*", "dev.local.toml"));
        assert!(!wildcard(".env", "node_modules"));
    }
}
