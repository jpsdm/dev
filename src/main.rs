//! dev — manages dev tools, environment variables and tasks, plus a workspace for projects,
//! scratch folders and imported GitHub/GitLab repositories.

mod certs;
mod config;
mod runtime;
mod screenshot;
mod setup;
mod tools;
mod update;
mod util;
mod workspace;

use clap::{Parser, Subcommand};
use config::Config;
use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};
use util::*;

pub const COMMAND_SUMMARY: &[(&str, &str)] = &[
    ("use", "Install a tool (node, java, maven...) and add it to dev.toml"),
    ("add cacert", "Add certificates to installed Javas"),
    ("install", "Install the tools in your config"),
    ("ls", "List installed and active tool versions"),
    ("exec", "Run a command with the tools on PATH"),
    ("run", "Run tasks and their dependencies"),
    ("env", "Print the environment for a shell"),
    ("doctor", "Check your dev setup for problems"),
    ("workspace", "Projects, scratch, repo imports, cleanup"),
    ("setup", "Install dev for bash, zsh, fish, PowerShell"),
];

pub const WORKSPACE_SUMMARY: &[(&str, &str)] = &[
    ("new", "Create a project"),
    ("scratch", "Create a dated scratch folder"),
    ("import", "Clone github:/gitlab: repos (--all for a user)"),
    ("sync", "Fast-forward imported repos"),
    ("clean", "Remove gitignored files from idle repos"),
    ("auto-clean", "Turn automatic cleanup on or off"),
];

#[derive(Parser, Debug)]
#[command(
    name = "dev",
    bin_name = "dev",
    version = VERSION,
    disable_version_flag = true,
    about = "dev manages dev tools, environment variables and tasks, plus a workspace for your projects",
    long_about = "dev manages dev tools (node, go, java, GitHub releases, npm/cargo/pypi packages...), environment variables and tasks per folder,\n\
                  from dev.toml files, plus a workspace for projects, scratch folders and imported\n\
                  GitHub/GitLab repositories with optional automatic cleanup of inactive ones."
)]
struct Cli {
    /// Change to this directory before doing anything
    #[arg(short = 'C', long = "cd", global = true, value_name = "DIR")]
    cd: Option<PathBuf>,
    /// Print less
    #[arg(short, long, global = true)]
    quiet: bool,
    /// Answer yes to every question
    #[arg(short, long, global = true)]
    yes: bool,
    /// Show more about what dev is doing
    #[arg(long, global = true)]
    verbose: bool,
    /// Print the version
    #[arg(short = 'V', long = "version")]
    version: bool,
    #[command(subcommand)]
    cmd: Option<Cmd>,
}

#[derive(Subcommand, Debug)]
enum Cmd {
    /// Add things to installed tools, such as certificates to Java (`dev add cacert`)
    Add {
        #[command(subcommand)]
        what: AddCmd,
    },
    /// Print the shell code that turns dev on in every prompt
    Activate {
        /// pwsh, powershell, bash, zsh or fish (defaults to $SHELL, else pwsh on Windows)
        shell: Option<String>,
    },
    /// List the bin directories of the active tools
    #[command(name = "bin-paths")]
    BinPaths,
    /// Manage the dev cache
    Cache {
        #[command(subcommand)]
        cmd: Option<CacheCmd>,
    },
    /// List the config files that apply here
    #[command(visible_alias = "cfg")]
    Config {
        #[command(subcommand)]
        cmd: Option<ConfigCmd>,
    },
    /// Show the active version of each tool (or one)
    Current {
        /// Only this tool
        tool: Option<String>,
    },
    /// Check your dev setup for problems
    #[command(visible_alias = "dr")]
    Doctor,
    /// Create or edit dev.toml in the current directory (writes a starter file)
    #[command(visible_alias = "init")]
    Edit {
        /// Config file to create; defaults to dev.toml
        path: Option<PathBuf>,
        /// Replace an existing file with the starter template
        #[arg(long, short)]
        force: bool,
        /// Print the result instead of writing it
        #[arg(long, short = 'n')]
        dry_run: bool,
    },
    /// Print the environment variables dev sets, for a shell
    #[command(visible_alias = "e")]
    Env {
        /// Shell syntax: pwsh, bash, zsh, fish or cmd
        #[arg(short, long)]
        shell: Option<String>,
        /// Output as JSON
        #[arg(long, short = 'J')]
        json: bool,
        /// Tools to add, such as node@20
        tools: Vec<String>,
    },
    /// Run a command with the tools on PATH
    #[command(visible_alias = "x")]
    Exec {
        /// Tools to use, such as node@20 (on top of the config)
        tools: Vec<String>,
        /// A command line to run through the shell
        #[arg(short = 'c', long = "command")]
        c: Option<String>,
        /// The command to run, after --
        #[arg(last = true)]
        command: Vec<String>,
    },
    #[command(name = "hook-env", hide = true)]
    HookEnv {
        #[arg(short, long)]
        shell: Option<String>,
    },
    /// Install the tools in your config, or specific tool versions
    #[command(visible_alias = "i")]
    Install {
        /// Tools to install, such as node@24; defaults to every tool in the config
        tools: Vec<String>,
        /// Reinstall even if already installed
        #[arg(long, short)]
        force: bool,
        /// Show what would be installed, without installing anything
        #[arg(long, short = 'n')]
        dry_run: bool,
    },
    /// Print the latest version that matches a version request
    Latest {
        /// Tool, optionally with a version prefix, such as node or node@22
        tool: String,
        /// Print the latest installed version instead of the latest available one
        #[arg(long, short)]
        installed: bool,
    },
    /// Register a tool installed elsewhere, such as `dev link node@brew C:\nodejs`
    #[command(visible_alias = "ln")]
    Link {
        /// Tool and version, such as node@24.11.0
        spec: String,
        /// Directory of the existing installation
        path: PathBuf,
        /// Replace an existing installation of this version
        #[arg(long, short)]
        force: bool,
    },
    /// List installed and active tool versions
    #[command(visible_alias = "list")]
    Ls {
        /// Only this tool
        tool: Option<String>,
        /// Only versions the config files request
        #[arg(long, short)]
        current: bool,
        /// Only requested versions that are not installed
        #[arg(long, short)]
        missing: bool,
        /// Output as JSON
        #[arg(long, short = 'J')]
        json: bool,
    },
    /// List tool versions available to install
    #[command(name = "ls-remote")]
    LsRemote {
        /// Tool to get versions for, optionally TOOL@PREFIX
        tool: String,
        /// Version prefix to filter by
        prefix: Option<String>,
    },
    /// Show tools whose installed version is older than the newest one their request allows
    Outdated {
        /// Only these tools; defaults to every tool in the config
        tools: Vec<String>,
    },
    /// Delete tool versions that no tracked config uses
    Prune {
        /// Show what would be deleted
        #[arg(long, short = 'n')]
        dry_run: bool,
    },
    /// Run tasks and their dependencies
    #[command(visible_alias = "r")]
    Run {
        /// Task to run; `default` when omitted
        task: Option<String>,
        /// Arguments passed to the task
        #[arg(trailing_var_arg = true, allow_hyphen_values = true)]
        args: Vec<String>,
        /// Print the tasks in the order they would run, without running them
        #[arg(long, short = 'n')]
        dry_run: bool,
    },
    /// Set environment variables in dev.toml, or list them
    #[command(name = "set")]
    SetEnv {
        /// KEY=VALUE pairs; none lists every variable
        pairs: Vec<String>,
        /// Write to the global config
        #[arg(long, short)]
        global: bool,
        /// Write to this file
        #[arg(long)]
        file: Option<PathBuf>,
    },
    /// Update dev to the newest release from GitHub (releases younger than 24h are skipped)
    #[command(name = "self-update")]
    SelfUpdate {
        /// Install this release (skips the minimum release age), such as 2026.11.4
        version: Option<String>,
        /// Only say whether a newer version exists
        #[arg(long)]
        check: bool,
        /// Update even if already up to date (or if a package manager installed dev)
        #[arg(long, short)]
        force: bool,
        /// Skip releases published more recently than this, such as 7d or 0 (default 24h)
        #[arg(long, value_name = "AGE")]
        minimum_release_age: Option<String>,
    },
    /// Install dev on this computer (PATH and bash/zsh/fish/PowerShell setup), or --uninstall
    #[command(name = "setup", visible_alias = "self-install")]
    Setup(setup::SetupArgs),
    /// Manage settings
    Settings {
        #[command(subcommand)]
        cmd: Option<SettingsCmd>,
    },
    /// Manage tasks
    #[command(visible_alias = "t")]
    Tasks {
        #[command(subcommand)]
        cmd: Option<TasksCmd>,
    },
    /// Remove installed tool versions
    #[command(visible_alias = "remove")]
    Uninstall {
        /// Tools to remove, such as node@20; a bare name removes every version with --all
        tools: Vec<String>,
        /// Remove every installed version of the tools
        #[arg(long, short)]
        all: bool,
    },
    /// Remove environment variables from dev.toml
    Unset {
        /// Variable names to remove
        keys: Vec<String>,
        /// Remove from the global config
        #[arg(long, short)]
        global: bool,
        /// Remove from this file
        #[arg(long)]
        file: Option<PathBuf>,
    },
    /// Remove tools from the config file
    Unuse {
        /// Tools to remove from the config, such as node
        tools: Vec<String>,
        /// Remove from the global config
        #[arg(long, short)]
        global: bool,
    },
    /// Upgrade tools to the newest version their request allows
    #[command(visible_alias = "up")]
    Upgrade {
        /// Only these tools; defaults to every tool in the config
        tools: Vec<String>,
        /// Show what would be upgraded, without installing
        #[arg(long, short = 'n')]
        dry_run: bool,
    },
    /// Install a tool and add it to the config
    #[command(visible_alias = "u")]
    Use {
        /// Tools to add, such as node@24, go@latest, github:cli/cli, npm:prettier, cargo:just or pipx:black
        tools: Vec<String>,
        /// Use the global config (~/.config/dev/config.toml)
        #[arg(long, short)]
        global: bool,
        /// Save the exact version it resolves to
        #[arg(long)]
        pin: bool,
        /// Write to this file or directory
        #[arg(long, short)]
        path: Option<PathBuf>,
        /// Write dev.ENV.toml
        #[arg(long, short)]
        env: Option<String>,
        /// Remove these tools from the config
        #[arg(long = "rm")]
        remove: Vec<String>,
    },
    /// Show the version of dev
    #[command(visible_alias = "v")]
    Version {
        /// Output as JSON
        #[arg(long, short = 'J')]
        json: bool,
    },
    /// Show the install directory of a tool version
    Where {
        /// Tool, optionally with a version, such as java or java@21
        tool: String,
    },
    /// Show the path a tool's executable resolves to
    Which {
        /// Program name, such as java or node
        bin: String,
        /// Show the version instead of the path
        #[arg(long)]
        version: bool,
        /// Show the tool that provides it
        #[arg(long)]
        plugin: bool,
    },
    /// Workspace: projects, scratch folders, GitHub/GitLab imports and automatic cleanup
    #[command(visible_alias = "ws")]
    Workspace(workspace::WsArgs),
}

#[derive(Subcommand, Debug)]
enum AddCmd {
    /// Add a certificate (or a folder of .crt/.cer/.pem files) to the cacerts of installed Javas.
    /// Saved in dev's certs folder; Javas installed later get them too.
    #[command(after_help = "Examples:\n  dev add cacert C:/certs 8,11,17\n  dev add cacert ./corp-root.crt 21\n  dev add cacert C:/certs --all\n  dev add cacert --list")]
    Cacert {
        /// A certificate file or a folder with certificates
        #[arg(required_unless_present = "list")]
        path: Option<PathBuf>,
        /// Installed Java versions, such as 8,11,17 (prefixes; zulu-17 for other vendors)
        versions: Vec<String>,
        /// Every installed Java
        #[arg(long, short)]
        all: bool,
        /// Show the certificates already saved
        #[arg(long, short)]
        list: bool,
    },
}

#[derive(Subcommand, Debug)]
enum CacheCmd {
    /// Delete cached data
    Clear,
    /// Print the cache directory path
    Path,
}

#[derive(Subcommand, Debug)]
enum ConfigCmd {
    /// List the config files that apply here
    Ls,
}

#[derive(Subcommand, Debug)]
enum SettingsCmd {
    /// List configured settings and their sources
    Ls,
    /// Print one setting
    Get { key: String },
    /// Set a setting in the global config
    Set { key: String, value: String },
    /// Remove a setting from the global config
    Unset { key: String },
}

#[derive(Subcommand, Debug)]
enum TasksCmd {
    /// List available tasks
    Ls,
    /// Show one task
    Info { task: String },
}

fn main() {
    let raw: Vec<String> = std::env::args().collect();
    // `--screenshot FILE` draws the program's screen into a picture and exits.
    if let Some(i) = raw.iter().position(|a| a == "--screenshot") {
        let code = match raw.get(i + 1) {
            Some(p) => match screenshot::take(Path::new(p)) {
                Ok(()) => 0,
                Err(e) => report(&e),
            },
            None => report("--screenshot needs a file name"),
        };
        std::process::exit(code);
    }
    // Started as `dev-setup.exe` (the Windows installer): install and wait for Enter.
    if setup::started_as_installer() && raw.len() <= 2 {
        if raw.get(1).map(|a| a == "-y" || a == "--yes").unwrap_or(false) {
            std::env::set_var("DEV_YES", "1");
        }
        let code = match setup::run(setup::SetupArgs::default()) {
            Ok(c) => c,
            Err(e) => report(&e),
        };
        setup::pause_if_interactive();
        std::process::exit(code);
    }
    // `dev -v` alone prints the version, like `--version`.
    if raw.len() == 2 && raw[1] == "-v" {
        print_version(false);
        return;
    }
    let cli = Cli::parse();
    if cli.verbose {
        std::env::set_var("DEV_VERBOSE", "1");
    }
    if cli.yes {
        std::env::set_var("DEV_YES", "1");
    }
    if let Some(d) = &cli.cd {
        if let Err(e) = std::env::set_current_dir(d) {
            std::process::exit(report(&format!("cannot change to {}: {}", d.display(), e)));
        }
    }
    let skip_auto = matches!(
        cli.cmd,
        Some(Cmd::Workspace(_))
            | Some(Cmd::HookEnv { .. })
            | Some(Cmd::Activate { .. })
            | Some(Cmd::Setup(_))
            | Some(Cmd::SelfUpdate { .. })
            | None
    );
    let code = match dispatch(cli) {
        Ok(c) => c,
        Err(e) => report(&e),
    };
    if !skip_auto {
        workspace::maybe_auto_clean();
        update::maybe_notify();
    }
    std::process::exit(code);
}

fn report(msg: &str) -> i32 {
    eprintln!("{} ERROR {}", NAME, msg);
    if !verbose() {
        eprintln!("{} ERROR Run with --verbose or DEV_VERBOSE=1 for more information", NAME);
    }
    1
}

fn version_line() -> String {
    format!("{} {}-{} ({})", VERSION, std::env::consts::OS, screenshot::arch(), BUILD_DATE)
}

fn print_version(json: bool) {
    if json {
        let j = serde_json::json!({
            "version": version_line(),
            "os": std::env::consts::OS,
            "arch": screenshot::arch(),
            "build_time": format!("{} 00:00:00 +00:00", BUILD_DATE),
        });
        println!("{}", serde_json::to_string_pretty(&j).unwrap());
    } else {
        println!("{}", version_line());
    }
}

/// Windows: puts JAVA_HOME / MAVEN_HOME / ANT_HOME (and `%VAR%\bin` on PATH) in the user's
/// environment variables, so cmd.exe, Explorer and IDEs see them. `always` (the global version)
/// replaces what is there; otherwise it is only filled in when Windows has none, or when the saved
/// folder no longer exists, so a project's version never overrides your global choice.
fn save_home_var(tool: &str, version: &str, always: bool, quiet: bool) {
    if !cfg!(windows) {
        return;
    }
    let var = match tools::home_var(tool) {
        Some(v) => v,
        None => return,
    };
    let home = tools::home_dir(tool, &tools::install_path(tool, version));
    let cur = setup::windows_user_var(var).filter(|c| !c.trim().is_empty());
    let cur_exists = cur.as_deref().map(|c| Path::new(c).is_dir()).unwrap_or(false);
    if !should_save_home(always, cur.is_some(), cur_exists) {
        if let Some(c) = cur {
            if !quiet && !same_path(Path::new(&c), &home) {
                info(format!("{} in Windows stays {} (run `dev use -g {}@{}` to switch it)", var, c, tool, version));
            }
        }
        return;
    }
    match setup::windows_set_global_home(tool, var, &home) {
        Ok(()) => {
            if !quiet {
                info(format!(
                    "{}={} saved in your user environment variables (with {} on PATH); open a new terminal",
                    var,
                    home.display(),
                    setup::home_path_entry(var)
                ));
            }
        }
        Err(e) => warn(format!("could not save {} in the user environment: {}", var, e)),
    }
}

/// Save the user variable? Always for the global version; otherwise only when Windows has none,
/// or the saved folder is gone.
fn should_save_home(always: bool, has_current: bool, current_exists: bool) -> bool {
    always || !has_current || !current_exists
}

fn same_path(a: &Path, b: &Path) -> bool {
    match (std::fs::canonicalize(a), std::fs::canonicalize(b)) {
        (Ok(x), Ok(y)) => x == y,
        _ => a.display().to_string().eq_ignore_ascii_case(&b.display().to_string()),
    }
}

fn cwd() -> Res<PathBuf> {
    std::env::current_dir().map_err(|e| e.to_string())
}

fn load() -> Res<Config> {
    let c = Config::load(&cwd()?)?;
    track(&c);
    Ok(c)
}

/// Remembers project config files so `dev prune` knows which versions are still used.
fn track(c: &Config) {
    let f = state_dir().join("tracked-configs.txt");
    let existing = std::fs::read_to_string(&f).unwrap_or_default();
    let mut set: BTreeSet<String> = existing.lines().map(String::from).collect();
    let before = set.len();
    for cf in &c.files {
        set.insert(cf.path.display().to_string());
    }
    if set.len() != before {
        let _ = write_file(&f, &(set.into_iter().collect::<Vec<_>>().join("\n") + "\n"));
    }
}

fn parse_specs(specs: &[String]) -> Vec<(String, String)> {
    specs
        .iter()
        .map(|s| {
            let (t, v) = tools::split_spec(s);
            (t, v.unwrap_or_else(|| "latest".into()))
        })
        .collect()
}

/// True for a word that can only be a version: `21`, `21.0.5`, `v1.2`, `latest`, `lts`, and for
/// java a vendor version such as `zulu-21` or `corretto-17.0.9`.
fn looks_like_version(word: &str, tool: &str) -> bool {
    if word.contains('@') || word.contains(':') || word.contains('/') || word.contains('\\') {
        return false;
    }
    let w = word.to_ascii_lowercase();
    if w == "latest" || w == "lts" {
        return true;
    }
    let digits_first = |s: &str| s.chars().next().map(|c| c.is_ascii_digit()).unwrap_or(false);
    if digits_first(&w) || (w.starts_with('v') && digits_first(&w[1..])) {
        return true;
    }
    if tools::Backend::of(tool) == tools::Backend::Java {
        if let Some((vendor, rest)) = w.split_once('-') {
            return !vendor.is_empty() && vendor.chars().all(|c| c.is_ascii_alphanumeric() || c == '-') && digits_first(rest);
        }
    }
    false
}

/// `^\d+(\.\d+)*$`: the pattern the original tests the second word against
/// (built once by FUN_1452cd510 from the string at 0x14c57d478, src\args\tool_arg.rs).
fn is_numeric_version(word: &str) -> bool {
    !word.is_empty() && word.split('.').all(|part| !part.is_empty() && part.bytes().all(|b| b.is_ascii_digit()))
}

/// `dev install java 21` means java@21. Rule from the original (FUN_144275920, src\args\tool_arg.rs):
/// only when exactly two tools are given, neither has an @version, and the second matches
/// `^\d+(\.\d+)*$`; then it becomes the first one's version and is dropped as a tool.
fn join_versions(args: Vec<String>) -> Vec<String> {
    if args.len() == 2 {
        let (tool, ver) = tools::split_spec(&args[0]);
        let (second, second_ver) = tools::split_spec(&args[1]);
        if ver.is_none() && second_ver.is_none() && is_numeric_version(&second) {
            return vec![format!("{}@{}", tool, second)];
        }
    }
    args
}

fn dispatch(cli: Cli) -> Res<i32> {
    if cli.version {
        print_version(false);
        return Ok(0);
    }
    let quiet = cli.quiet;
    let yes = cli.yes;
    let mut cmd = match cli.cmd {
        Some(c) => c,
        None => {
            use clap::CommandFactory;
            Cli::command().print_help().map_err(|e| e.to_string())?;
            println!();
            return Ok(0);
        }
    };
    // `dev install java 21` means java@21, not two tools.
    match &mut cmd {
        Cmd::Env { tools, .. }
        | Cmd::Exec { tools, .. }
        | Cmd::Install { tools, .. }
        | Cmd::Outdated { tools, .. }
        | Cmd::Uninstall { tools, .. }
        | Cmd::Unuse { tools, .. }
        | Cmd::Upgrade { tools, .. }
        | Cmd::Use { tools, .. } => {
            *tools = join_versions(std::mem::take(tools));
            if let Some(i) = tools.iter().position(|t| looks_like_version(&tools::split_spec(t).0, "")) {
                let before = if i > 0 { tools::split_spec(&tools[i - 1]).0 } else { "java".to_string() };
                return err(format!(
                    "`{v}` is a version, not a tool; join it to its tool with @, such as `{t}@{v}`",
                    v = tools[i],
                    t = before
                ));
            }
        }
        _ => {}
    }
    match cmd {
        Cmd::Version { json } => {
            print_version(json);
            Ok(0)
        }
        Cmd::Activate { shell } => activate(shell),
        Cmd::HookEnv { shell } => hook_env(shell),
        Cmd::BinPaths => {
            let cfg = load()?;
            for p in runtime::bin_paths(&runtime::active_tools(&cfg, &[])) {
                println!("{}", p.display());
            }
            Ok(0)
        }
        Cmd::Cache { cmd } => match cmd.unwrap_or(CacheCmd::Path) {
            CacheCmd::Path => {
                println!("{}", cache_dir().display());
                Ok(0)
            }
            CacheCmd::Clear => {
                remove_path(&cache_dir())?;
                if !quiet {
                    info("cache cleared");
                }
                Ok(0)
            }
        },
        Cmd::Config { .. } => {
            let cfg = load()?;
            println!("{:<60}  Tools", "Path");
            for f in &cfg.files {
                let t: Vec<String> = f.tools.iter().map(|(t, _)| t.clone()).collect();
                println!("{:<60}  {}", display_path(&f.path), t.join(", "));
            }
            Ok(0)
        }
        Cmd::Current { tool } => {
            let cfg = load()?;
            let act = runtime::active_tools(&cfg, &[]);
            let mut by: BTreeMap<String, Vec<String>> = BTreeMap::new();
            for a in &act {
                if a.installed {
                    by.entry(a.tool.clone()).or_default().push(a.version.clone());
                } else {
                    warn(format!("{}@{} is specified in {} but not installed", a.tool, a.request, a.source.as_deref().map(display_path).unwrap_or_default()));
                }
            }
            match tool {
                Some(t) => println!("{}", by.get(&t).map(|v| v.join(" ")).unwrap_or_default()),
                None => {
                    for (t, v) in by {
                        println!("{} {}", t, v.join(" "));
                    }
                }
            }
            Ok(0)
        }
        Cmd::Doctor => doctor(),
        Cmd::Edit { path, force, dry_run } => {
            let p = cwd()?.join(path.unwrap_or_else(|| PathBuf::from("dev.toml")));
            if dry_run {
                print!("{}", config::STARTER);
                return Ok(0);
            }
            if p.exists() && !force {
                return err(format!("{} already exists; pass --force to replace it", p.display()));
            }
            write_file(&p, config::STARTER)?;
            if !quiet {
                info(format!("wrote {}", display_path(&p)));
            }
            Ok(0)
        }
        Cmd::Env { shell, json, tools } => {
            let cfg = load()?;
            let act = runtime::active_tools(&cfg, &parse_specs(&tools));
            let (vars, paths) = runtime::env_vars(&cfg, &act);
            if json {
                let mut all = vars;
                all.push(("PATH".into(), runtime::full_path(&paths)));
                let m: BTreeMap<_, _> = all.into_iter().collect();
                println!("{}", serde_json::to_string_pretty(&m).unwrap());
            } else {
                let sh = shell.unwrap_or_else(default_shell);
                for (k, v) in vars {
                    println!("{}", export_line(&sh, &k, &v));
                }
                println!("{}", path_line(&sh, &paths));
            }
            Ok(0)
        }
        Cmd::Exec { tools, c, command } => {
            let cfg = load()?;
            let mut act = runtime::active_tools(&cfg, &parse_specs(&tools));
            runtime::ensure_installed(&mut act, quiet)?;
            let (vars, paths) = runtime::env_vars(&cfg, &act);
            let status = if let Some(line) = c {
                runtime::run_shell(&line, &vars, &paths, &cwd()?)?
            } else if let Some((prog, args)) = command.split_first() {
                runtime::run_program(prog, args, &vars, &paths, None)?
            } else {
                return err("no command given; use `dev exec TOOL@VERSION -- COMMAND`");
            };
            Ok(status.code().unwrap_or(1))
        }
        Cmd::Install { tools: specs, force, dry_run } => {
            let wanted: Vec<(String, String)> = if specs.is_empty() {
                let cfg = load()?;
                cfg.tools().into_iter().flat_map(|(t, v, _)| v.into_iter().map(move |x| (t.clone(), x))).collect()
            } else {
                let cfg = load()?;
                let req: BTreeMap<String, Vec<String>> = cfg.tools().into_iter().map(|(t, v, _)| (t, v)).collect();
                specs
                    .iter()
                    .flat_map(|s| {
                        let (t, v) = tools::split_spec(s);
                        match v {
                            Some(v) => vec![(t, v)],
                            None => req.get(&t).cloned().unwrap_or_else(|| vec!["latest".into()]).into_iter().map(|v| (t.clone(), v)).collect(),
                        }
                    })
                    .collect()
            };
            if wanted.is_empty() {
                if !quiet {
                    info("all tools are installed");
                }
                return Ok(0);
            }
            let mut failed = 0;
            let mut did = 0;
            for (t, req) in wanted {
                let v = match tools::resolve(&t, &req, !force) {
                    Ok(v) => v,
                    Err(e) => {
                        eprintln!("{} ERROR {}", NAME, e);
                        failed += 1;
                        continue;
                    }
                };
                if tools::is_installed(&t, &v) && !force {
                    // Already there: still make sure Windows has its JAVA_HOME / MAVEN_HOME / ANT_HOME.
                    if !dry_run {
                        save_home_var(&t, &v, false, quiet);
                    }
                    continue;
                }
                did += 1;
                if dry_run {
                    println!("{}@{} would be installed", t, v);
                    continue;
                }
                match tools::install(&t, &v, force, verbose()) {
                    Ok(_) => {
                        if !quiet {
                            info(format!("{}@{} ✓ installed", t, v));
                        }
                        save_home_var(&t, &v, false, quiet);
                    }
                    Err(e) => {
                        eprintln!("{} ERROR {}@{}: {}", NAME, t, v, e);
                        failed += 1;
                    }
                }
            }
            if did == 0 && failed == 0 && !quiet {
                info("all tools are installed");
            }
            Ok(if failed > 0 { 1 } else { 0 })
        }
        Cmd::Latest { tool, installed } => {
            let (t, prefix) = tools::split_spec(&tool);
            let prefix = prefix.unwrap_or_else(|| "latest".into());
            let v = if installed {
                let list = tools::installed_versions(&t);
                list.into_iter()
                    .find(|v| prefix == "latest" || version_matches(v, &prefix))
                    .ok_or_else(|| format!("no installed version of {} matches {}", t, prefix))?
            } else {
                tools::resolve(&t, &prefix, false)?
            };
            println!("{}", v);
            Ok(0)
        }
        Cmd::Link { spec, path, force } => {
            let (t, v) = tools::split_spec(&spec);
            let v = v.ok_or("give a version: dev link TOOL@VERSION DIR")?;
            if v == "latest" || v == "lts" {
                return err("link needs an exact version, not latest or lts");
            }
            tools::link(&t, &v, &path, force)?;
            if !quiet {
                info(format!("{}@{} linked to {}", t, v, path.display()));
            }
            Ok(0)
        }
        Cmd::Ls { tool, current, missing, json } => ls(tool, current, missing, json),
        Cmd::LsRemote { tool, prefix } => {
            let (t, p) = tools::split_spec(&tool);
            let p = prefix.or(p);
            let list = tools::remote_versions(&t)?;
            let mut list: Vec<String> = list.into_iter().filter(|v| p.as_deref().map(|p| version_matches(v, p)).unwrap_or(true)).collect();
            list.reverse();
            for v in list {
                println!("{}", v);
            }
            Ok(0)
        }
        Cmd::Outdated { tools: only } => {
            let cfg = load()?;
            let act = runtime::active_tools(&cfg, &[]);
            let mut rows = vec![];
            for a in act.iter().filter(|a| only.is_empty() || only.contains(&a.tool)) {
                if matches!(tools::Backend::of(&a.tool), tools::Backend::Local(_)) {
                    continue;
                }
                let newest = match tools::resolve(&a.tool, &a.request, false) {
                    Ok(v) => v,
                    Err(e) => {
                        warn(e);
                        continue;
                    }
                };
                let cur = if a.installed { a.version.clone() } else { "[MISSING]".into() };
                if cur != newest {
                    rows.push((a.tool.clone(), a.request.clone(), cur, newest));
                }
            }
            if rows.is_empty() {
                if !quiet {
                    info("all tools are up to date");
                }
                return Ok(0);
            }
            println!("{:<20} {:<12} {:<14} {}", "Tool", "Requested", "Current", "Latest");
            for (t, r, c, l) in rows {
                println!("{:<20} {:<12} {:<14} {}", t, r, c, l);
            }
            Ok(0)
        }
        Cmd::Prune { dry_run } => prune(dry_run, quiet),
        Cmd::Run { task, args, dry_run } => run_task(task, args, dry_run, quiet),
        Cmd::SetEnv { pairs, global, file } => {
            if pairs.is_empty() {
                let cfg = load()?;
                let mut rows = vec![];
                for f in cfg.files.iter().rev() {
                    for (k, v) in &f.env {
                        rows.push((k.clone(), v.clone(), display_path(&f.path)));
                    }
                }
                println!("{:<24} {:<32} {}", "Key", "Value", "Source");
                for (k, v, s) in rows {
                    println!("{:<24} {:<32} {}", k, v, s);
                }
                return Ok(0);
            }
            let target = file.map(|f| cwd().map(|c| c.join(f))).transpose()?.unwrap_or(config::target_for_write(&cwd()?, global, None, None));
            for p in pairs {
                let (k, v) = p.split_once('=').ok_or_else(|| format!("expected KEY=VALUE, got {}", p))?;
                config::set_env(&target, k, v)?;
            }
            Ok(0)
        }
        Cmd::Unset { keys, global, file } => {
            let target = file.map(|f| cwd().map(|c| c.join(f))).transpose()?.unwrap_or(config::target_for_write(&cwd()?, global, None, None));
            for k in keys {
                if !config::unset_env(&target, &k)? && !quiet {
                    warn(format!("{} is not set in {}", k, display_path(&target)));
                }
            }
            Ok(0)
        }
        Cmd::Settings { cmd } => settings_cmd(cmd.unwrap_or(SettingsCmd::Ls)),
        Cmd::Tasks { cmd } => {
            let cfg = load()?;
            let tasks = cfg.tasks();
            match cmd.unwrap_or(TasksCmd::Ls) {
                TasksCmd::Ls => {
                    if tasks.is_empty() {
                        if !quiet {
                            info("no tasks defined; add [tasks.NAME] to dev.toml");
                        }
                        return Ok(0);
                    }
                    let w = tasks.keys().map(|k| k.len()).max().unwrap_or(4).max(4);
                    println!("{:<w$}  {:<40}  Source", "Name", "Description", w = w);
                    for t in tasks.values() {
                        println!("{:<w$}  {:<40}  {}", t.name, t.description, display_path(&t.source), w = w);
                    }
                    Ok(0)
                }
                TasksCmd::Info { task } => {
                    let t = tasks.get(&task).ok_or_else(|| format!("no task named {}", task))?;
                    println!("Task: {}", t.name);
                    println!("Description: {}", t.description);
                    println!("Source: {}", display_path(&t.source));
                    if !t.depends.is_empty() {
                        println!("Depends on: {}", t.depends.join(", "));
                    }
                    println!("Run:");
                    for r in &t.run {
                        println!("  {}", r);
                    }
                    Ok(0)
                }
            }
        }
        Cmd::Uninstall { tools: specs, all } => {
            if specs.is_empty() {
                return err("name the tools to remove, such as `dev uninstall node@20`");
            }
            let mut failed = 0;
            for s in specs {
                let (t, v) = tools::split_spec(&s);
                let versions: Vec<String> = match v {
                    Some(v) => {
                        let inst = tools::installed_versions(&t);
                        match inst.iter().find(|x| **x == v || version_matches(x, &v)) {
                            Some(x) => vec![x.clone()],
                            None => vec![v],
                        }
                    }
                    None if all => tools::installed_versions(&t),
                    None => {
                        let cfg = load()?;
                        let act = runtime::active_tools(&cfg, &[]);
                        act.iter().filter(|a| a.tool == t && a.installed).map(|a| a.version.clone()).collect()
                    }
                };
                if versions.is_empty() {
                    eprintln!("{} ERROR {} is not installed", NAME, t);
                    failed += 1;
                }
                for v in versions {
                    match tools::uninstall(&t, &v) {
                        Ok(()) => {
                            if !quiet {
                                info(format!("{}@{} ✓ uninstalled", t, v));
                            }
                        }
                        Err(e) => {
                            eprintln!("{} ERROR {}", NAME, e);
                            failed += 1;
                        }
                    }
                }
            }
            Ok(if failed > 0 { 1 } else { 0 })
        }
        Cmd::Unuse { tools: specs, global } => {
            let target = config::target_for_write(&cwd()?, global, None, None);
            for s in specs {
                let (t, _) = tools::split_spec(&s);
                if config::remove_tool(&target, &t)? {
                    if !quiet {
                        info(format!("removed {} from {}", t, display_path(&target)));
                    }
                } else if !quiet {
                    warn(format!("{} is not in {}", t, display_path(&target)));
                }
            }
            Ok(0)
        }
        Cmd::Upgrade { tools: only, dry_run } => {
            let cfg = load()?;
            let act = runtime::active_tools(&cfg, &[]);
            let mut failed = 0;
            for a in act.iter().filter(|a| only.is_empty() || only.iter().any(|o| tools::split_spec(o).0 == a.tool)) {
                if matches!(tools::Backend::of(&a.tool), tools::Backend::Local(_)) {
                    continue;
                }
                let newest = match tools::resolve(&a.tool, &a.request, false) {
                    Ok(v) => v,
                    Err(e) => {
                        eprintln!("{} ERROR {}", NAME, e);
                        failed += 1;
                        continue;
                    }
                };
                if a.installed && a.version == newest {
                    continue;
                }
                if dry_run {
                    println!("Would upgrade {} {} -> {}", a.tool, if a.installed { &a.version } else { "(missing)" }, newest);
                    continue;
                }
                tools::install(&a.tool, &newest, false, verbose())?;
                if !quiet {
                    info(format!("{}@{} ✓ installed", a.tool, newest));
                }
            }
            Ok(if failed > 0 { 1 } else { 0 })
        }
        Cmd::Use { tools: specs, global, pin, path, env, remove } => {
            let here = cwd()?;
            let target = config::target_for_write(&here, global, path.as_deref(), env.as_deref());
            for r in &remove {
                config::remove_tool(&target, &tools::split_spec(r).0)?;
            }
            if specs.is_empty() && remove.is_empty() {
                return err("name a tool, such as `dev use node@24`");
            }
            for s in &specs {
                let (t, req) = tools::split_spec(s);
                let req = req.unwrap_or_else(|| "latest".into());
                let v = tools::resolve(&t, &req, req == "latest" || req != "lts")?;
                if !tools::is_installed(&t, &v) {
                    tools::install(&t, &v, false, verbose())?;
                    if !quiet {
                        info(format!("{}@{} ✓ installed", t, v));
                    }
                }
                let written = if pin { v.clone() } else { req.clone() };
                config::set_tool(&target, &t, &[written.clone()])?;
                if !quiet {
                    info(format!("{} tools: {}@{}", display_path(&target), t, v));
                }
                // JAVA_HOME / MAVEN_HOME / ANT_HOME.
                if let Some(var) = tools::home_var(&t) {
                    let home = tools::home_dir(&t, &tools::install_path(&t, &v));
                    if !quiet {
                        info(format!("{}={}", var, home.display()));
                    }
                    // The global one (`-g`, or `dev use` run where the global config is the target)
                    // always replaces the Windows user variable; a project's only fills it when
                    // Windows has none yet (or a stale one).
                    let is_global = global || same_path(&target, &global_config_path());
                    save_home_var(&t, &v, is_global, quiet);
                }
            }
            let _ = yes;
            Ok(0)
        }
        Cmd::Where { tool } => {
            let (t, v) = tools::split_spec(&tool);
            let version = match v {
                Some(v) => tools::installed_versions(&t).into_iter().find(|x| *x == v || version_matches(x, &v)),
                None => {
                    let cfg = load()?;
                    runtime::active_tools(&cfg, &[])
                        .into_iter()
                        .find(|a| a.tool == t && a.installed)
                        .map(|a| a.version)
                        .or_else(|| tools::installed_versions(&t).into_iter().next())
                }
            };
            match version {
                Some(v) => {
                    println!("{}", tools::real_path(&tools::install_path(&t, &v)).display());
                    Ok(0)
                }
                None => err(format!("{} is not installed", tool)),
            }
        }
        Cmd::Which { bin, version, plugin } => {
            let cfg = load()?;
            let act = runtime::active_tools(&cfg, &[]);
            for a in act.iter().filter(|a| a.installed) {
                let dirs = tools::bin_dirs(&a.tool, &tools::install_path(&a.tool, &a.version));
                if let Some(p) = find_in_path(&bin, &dirs) {
                    if plugin {
                        println!("{}", a.tool);
                    } else if version {
                        println!("{}", a.version);
                    } else {
                        println!("{}", p.display());
                    }
                    return Ok(0);
                }
            }
            err(format!("{} is not a dev bin; perhaps you need to install it", bin))
        }
        Cmd::Workspace(args) => workspace::run(args),
        Cmd::Setup(args) => setup::run(args),
        Cmd::Add { what: AddCmd::Cacert { path, versions, all, list } } => {
            if list {
                return certs::list();
            }
            let path = path.ok_or("give a certificate file or folder")?;
            if !all && versions.is_empty() {
                return err("say which Java versions, such as `dev add cacert C:/certs 8,11,17`, or use --all");
            }
            certs::add_cacert(&path, &versions, all, quiet)
        }
        Cmd::SelfUpdate { version, check, force, minimum_release_age } => update::self_update(update::SelfUpdateArgs {
            version,
            check,
            force,
            yes: yes || std::env::var("DEV_YES").is_ok(),
            minimum_release_age,
            quiet,
        }),
    }
}

fn default_shell() -> String {
    if let Ok(sh) = std::env::var("SHELL") {
        let name = Path::new(&sh).file_stem().map(|s| s.to_string_lossy().to_lowercase()).unwrap_or_default();
        if ["bash", "zsh", "fish"].contains(&name.as_str()) {
            return name;
        }
    }
    if cfg!(windows) {
        "pwsh".into()
    } else {
        "bash".into()
    }
}

fn is_posix_shell(sh: &str) -> bool {
    matches!(sh, "bash" | "zsh" | "sh" | "fish")
}

/// The line that sets PATH in a shell; Git Bash / MSYS on Windows gets /c/... paths joined with `:`.
fn path_line(sh: &str, paths: &[PathBuf]) -> String {
    let full = runtime::full_path(paths);
    let parts: Vec<String> = split_path_var(&full)
        .into_iter()
        .map(|p| {
            let s = p.display().to_string();
            if cfg!(windows) && is_posix_shell(sh) {
                to_posix_path(&s)
            } else {
                s
            }
        })
        .collect();
    match sh {
        "fish" => {
            let quoted: Vec<String> = parts.iter().map(|p| format!("'{}'", p.replace('\'', "\\'"))).collect();
            format!("set -gx PATH {}", quoted.join(" "))
        }
        _ if is_posix_shell(sh) => export_line(sh, "PATH", &parts.join(":")),
        _ => export_line(sh, "PATH", &full),
    }
}

fn export_line(shell: &str, k: &str, v: &str) -> String {
    match shell {
        "pwsh" | "powershell" => format!("$env:{}='{}'", k, v.replace('\'', "''")),
        "cmd" => format!("set \"{}={}\"", k, v),
        "fish" => format!("set -gx {} '{}'", k, v.replace('\'', "\\'")),
        "nu" => format!("$env.{} = '{}'", k, v),
        _ => format!("export {}='{}'", k, v.replace('\'', "'\\''")),
    }
}

fn activate(shell: Option<String>) -> Res<i32> {
    let sh = shell.unwrap_or_else(default_shell);
    let exe = std::env::current_exe().map_err(|e| e.to_string())?;
    let exe = exe.display().to_string();
    match sh.as_str() {
        "pwsh" | "powershell" => {
            // Keep the PATH from before the first activation (the original's pwsh script, 0x14c533af5).
            println!("if (-not (Test-Path -Path Env:/__DEV_ORIG_PATH)) {{ $env:__DEV_ORIG_PATH = $env:PATH }}");
            println!("if (-not $global:__dev_prev_prompt) {{ $global:__dev_prev_prompt = $function:prompt }}");
            println!("function global:prompt {{");
            // The prompt hook must not change the last command's exit code.
            println!("  $__dev_code = $global:LASTEXITCODE");
            println!("  & '{}' hook-env -s pwsh | Out-String | Invoke-Expression", exe.replace('\'', "''"));
            println!("  $global:LASTEXITCODE = $__dev_code");
            println!("  & $global:__dev_prev_prompt");
            println!("}}");
        }
        "bash" | "zsh" | "sh" => {
            let e = if cfg!(windows) { to_posix_path(&exe) } else { exe.clone() };
            let e = e.replace('\'', "'\\''");
            // Like the original's bash script (0x14b5e28c0): save PATH on first activation only,
            // and give back the previous command's exit status from the hook.
            println!("if [ -z \"${{__DEV_ORIG_PATH:-}}\" ]; then export __DEV_ORIG_PATH=\"$PATH\"; fi");
            println!(
                "_dev_hook() {{ local previous_exit_status=$?; eval \"$('{}' hook-env -s {})\"; return $previous_exit_status; }}",
                e,
                if sh == "zsh" { "zsh" } else { "bash" }
            );
            match sh.as_str() {
                "zsh" => {
                    println!("autoload -Uz add-zsh-hook");
                    println!("add-zsh-hook precmd _dev_hook");
                    println!("add-zsh-hook chpwd _dev_hook");
                }
                "bash" => {
                    // PROMPT_COMMAND may be a plain string or (bash 5.1+) an array.
                    println!("if [[ \"$(declare -p PROMPT_COMMAND 2>/dev/null)\" == \"declare -a\"* ]]; then");
                    println!("  if [[ \" ${{PROMPT_COMMAND[*]}} \" != *\" _dev_hook \"* ]]; then PROMPT_COMMAND=(\"_dev_hook\" \"${{PROMPT_COMMAND[@]}}\"); fi");
                    println!("elif [[ \";${{PROMPT_COMMAND:-}};\" != *\";_dev_hook;\"* ]]; then");
                    println!("  PROMPT_COMMAND=\"_dev_hook${{PROMPT_COMMAND:+;$PROMPT_COMMAND}}\"");
                    println!("fi");
                }
                _ => {}
            }
            println!("_dev_hook");
        }
        "fish" => {
            let e = if cfg!(windows) { to_posix_path(&exe) } else { exe.clone() };
            println!("function __dev_hook --on-event fish_prompt --on-variable PWD");
            println!("    '{}' hook-env -s fish | source", e.replace('\'', "\\'"));
            println!("end");
            println!("__dev_hook");
        }
        other => {
            return err(format!(
                "activate does not support {}; use pwsh, bash, zsh or fish (or `dev env -s cmd`)",
                other
            ))
        }
    }
    Ok(0)
}

/// Called at every prompt: rebuilds PATH from the PATH dev first saw, so entries don't pile up.
fn hook_env(shell: Option<String>) -> Res<i32> {
    let sh = shell.unwrap_or_else(default_shell);
    let base = std::env::var("__DEV_ORIG_PATH").unwrap_or_else(|_| std::env::var("PATH").unwrap_or_default());
    std::env::set_var("PATH", &base);
    let cfg = match Config::load(&cwd()?) {
        Ok(c) => c,
        Err(e) => {
            eprintln!("{} WARN  {}", NAME, e);
            return Ok(0);
        }
    };
    track(&cfg);
    let act = runtime::active_tools(&cfg, &[]);
    let (vars, paths) = runtime::env_vars(&cfg, &act);
    println!("{}", export_line(&sh, "__DEV_ORIG_PATH", &base));
    println!("{}", path_line(&sh, &paths));
    // Variables dev set at the last prompt that no longer apply (JAVA_HOME after leaving a
    // Java project) are removed, or given back the value the shell had before dev set them.
    let prev: Vec<String> = std::env::var("__DEV_VARS")
        .unwrap_or_default()
        .split(',')
        .filter(|s| !s.is_empty())
        .map(String::from)
        .collect();
    let now: Vec<String> = vars.iter().map(|(k, _)| k.clone()).collect();
    for k in prev.iter().filter(|k| !now.contains(k)) {
        let saved = format!("__DEV_SAVED_{}", k);
        match std::env::var(&saved) {
            Ok(v) => {
                println!("{}", export_line(&sh, k, &v));
                println!("{}", unset_line(&sh, &saved));
            }
            Err(_) => println!("{}", unset_line(&sh, k)),
        }
    }
    for (k, v) in &vars {
        if !prev.contains(k) {
            if let Ok(old) = std::env::var(k) {
                println!("{}", export_line(&sh, &format!("__DEV_SAVED_{}", k), &old));
            }
        }
        println!("{}", export_line(&sh, k, v));
    }
    if now.is_empty() {
        if !prev.is_empty() {
            println!("{}", unset_line(&sh, "__DEV_VARS"));
        }
    } else {
        println!("{}", export_line(&sh, "__DEV_VARS", &now.join(",")));
    }
    Ok(0)
}

fn unset_line(shell: &str, k: &str) -> String {
    match shell {
        "pwsh" | "powershell" => format!("Remove-Item Env:{} -ErrorAction SilentlyContinue", k),
        "cmd" => format!("set \"{}=\"", k),
        "fish" => format!("set -e {}", k),
        _ => format!("unset {}", k),
    }
}

fn ls(tool: Option<String>, current: bool, missing: bool, json: bool) -> Res<i32> {
    let cfg = load()?;
    let act = runtime::active_tools(&cfg, &[]);
    // (tool, version, installed, source, request)
    let mut rows: Vec<(String, String, bool, Option<PathBuf>, String)> = vec![];
    for a in &act {
        rows.push((a.tool.clone(), a.version.clone(), a.installed, a.source.clone(), a.request.clone()));
    }
    if !current && !missing {
        for (t, v) in tools::all_installed() {
            if !rows.iter().any(|r| r.0 == t && r.1 == v) {
                rows.push((t, v, true, None, String::new()));
            }
        }
    }
    rows.retain(|r| tool.as_deref().map(|t| t == r.0).unwrap_or(true));
    if missing {
        rows.retain(|r| !r.2);
    }
    rows.sort_by(|a, b| a.0.cmp(&b.0).then(version_cmp(&a.1, &b.1)));
    if json {
        let mut map: BTreeMap<String, Vec<serde_json::Value>> = BTreeMap::new();
        for (t, v, inst, src, req) in &rows {
            let mut o = serde_json::json!({
                "version": v,
                "install_path": tools::real_path(&tools::install_path(t, v)).display().to_string(),
                "installed": inst,
                "active": src.is_some(),
            });
            if let Some(s) = src {
                o["requested_version"] = serde_json::json!(req);
                o["source"] = serde_json::json!({"type": "dev.toml", "path": s.display().to_string()});
            }
            map.entry(t.clone()).or_default().push(o);
        }
        let out = match &tool {
            Some(t) => serde_json::to_string_pretty(&map.get(t).cloned().unwrap_or_default()),
            None => serde_json::to_string_pretty(&map),
        };
        println!("{}", out.unwrap());
        return Ok(0);
    }
    if rows.is_empty() {
        return Ok(0);
    }
    let shown: Vec<(String, String, String, String)> = rows
        .iter()
        .map(|(t, v, inst, src, req)| {
            let ver = if *inst { v.clone() } else { format!("{} (missing)", v) };
            (t.clone(), ver, src.as_deref().map(display_path).unwrap_or_default(), req.clone())
        })
        .collect();
    let w0 = shown.iter().map(|r| r.0.len()).max().unwrap_or(4).max(4);
    let w1 = shown.iter().map(|r| r.1.len()).max().unwrap_or(7).max(7);
    let w2 = shown.iter().map(|r| r.2.len()).max().unwrap_or(6).max(6);
    println!("{:<w0$}  {:<w1$}  {:<w2$}  {}", "Tool", "Version", "Source", "Requested", w0 = w0, w1 = w1, w2 = w2);
    for (t, v, s, r) in shown {
        println!("{:<w0$}  {:<w1$}  {:<w2$}  {}", t, v, s, r, w0 = w0, w1 = w1, w2 = w2);
    }
    Ok(0)
}

fn prune(dry_run: bool, quiet: bool) -> Res<i32> {
    let f = state_dir().join("tracked-configs.txt");
    let mut used: BTreeSet<(String, String)> = BTreeSet::new();
    let mut live = vec![];
    let tracked = std::fs::read_to_string(&f).unwrap_or_default();
    let mut files: Vec<PathBuf> = tracked.lines().map(PathBuf::from).filter(|p| p.is_file()).collect();
    let g = global_config_path();
    if g.is_file() && !files.contains(&g) {
        files.push(g);
    }
    for p in &files {
        live.push(p.display().to_string());
        if let Ok(cf) = config::ConfigFile::load(p, false) {
            for (t, reqs) in cf.tools {
                let inst = tools::installed_versions(&t);
                for r in reqs {
                    let hit = match r.as_str() {
                        "latest" | "lts" => inst.iter().find(|v| !is_prerelease(v)).or(inst.first()).cloned(),
                        _ => inst.iter().find(|v| **v == r || version_matches(v, &r)).cloned(),
                    };
                    if let Some(v) = hit {
                        used.insert((t.clone(), v));
                    }
                }
            }
        }
    }
    let _ = write_file(&f, &(live.join("\n") + "\n"));
    let mut removed = 0;
    for (t, v) in tools::all_installed() {
        if used.contains(&(t.clone(), v.clone())) {
            continue;
        }
        let p = tools::install_path(&t, &v);
        if dry_run {
            println!("{}@{} [dryrun]  remove {}", t, v, display_path(&p));
        } else {
            remove_path(&p)?;
            if !quiet {
                info(format!("{}@{} ✓ removed", t, v));
            }
        }
        removed += 1;
    }
    if removed == 0 && !quiet {
        info("nothing to prune");
    }
    Ok(0)
}

fn run_task(task: Option<String>, args: Vec<String>, dry_run: bool, quiet: bool) -> Res<i32> {
    let cfg = load()?;
    let tasks = cfg.tasks();
    let name = task.unwrap_or_else(|| "default".into());
    if !tasks.contains_key(&name) {
        let names: Vec<&String> = tasks.keys().collect();
        if name == "default" && names.is_empty() {
            return err("no tasks defined; add [tasks.NAME] to dev.toml");
        }
        return err(format!(
            "no task named {}{}",
            name,
            if names.is_empty() { String::new() } else { format!(" (tasks: {})", names.iter().map(|s| s.as_str()).collect::<Vec<_>>().join(", ")) }
        ));
    }
    // Order: dependencies first, each once; a cycle is an error.
    let mut order = vec![];
    let mut visiting = BTreeSet::new();
    fn visit(n: &str, tasks: &BTreeMap<String, config::Task>, order: &mut Vec<String>, visiting: &mut BTreeSet<String>) -> Res<()> {
        if order.iter().any(|o| o == n) {
            return Ok(());
        }
        if !visiting.insert(n.to_string()) {
            return err(format!("task dependency cycle at {}", n));
        }
        let t = tasks.get(n).ok_or_else(|| format!("no task named {}", n))?;
        for d in &t.depends {
            visit(d, tasks, order, visiting)?;
        }
        order.push(n.to_string());
        Ok(())
    }
    visit(&name, &tasks, &mut order, &mut visiting)?;
    let mut act = runtime::active_tools(&cfg, &[]);
    if !dry_run {
        runtime::ensure_installed(&mut act, quiet)?;
    }
    let (vars, paths) = runtime::env_vars(&cfg, &act);
    for n in &order {
        let t = &tasks[n];
        let dir = match &t.dir {
            Some(d) => {
                let e = config::expand(d, &t.source.parent().unwrap_or(Path::new(".")).to_path_buf());
                let p = PathBuf::from(&e);
                if p.is_absolute() { p } else { t.source.parent().unwrap().join(p) }
            }
            None => t.source.parent().map(Path::to_path_buf).unwrap_or(cwd()?),
        };
        let mut v = vars.clone();
        v.extend(t.env.iter().cloned());
        for line in &t.run {
            let line = if *n == name && !args.is_empty() { format!("{} {}", line, args.join(" ")) } else { line.clone() };
            if !quiet {
                eprintln!("[{}] $ {}", n, line);
            }
            if dry_run {
                continue;
            }
            let st = runtime::run_shell(&line, &v, &paths, &dir)?;
            if !st.success() {
                let code = st.code().unwrap_or(1);
                eprintln!("{} ERROR task {} failed (exit code {})", NAME, n, code);
                return Ok(code);
            }
        }
    }
    Ok(0)
}

fn settings_cmd(cmd: SettingsCmd) -> Res<i32> {
    let g = global_config_path();
    match cmd {
        SettingsCmd::Ls => {
            let cfg = load()?;
            for (k, (v, src)) in cfg.settings() {
                println!("{:<32} {:<20} {}", k, v.to_string(), display_path(&src));
            }
            let ws = workspace::settings();
            println!("{:<32} {:<20} {}", "workspace.root", format!("\"{}\"", ws.root.display()), "(workspace)");
            println!("{:<32} {:<20} {}", "workspace.auto_clean", ws.auto_clean, "(workspace)");
            println!("{:<32} {:<20} {}", "workspace.inactive_days", ws.inactive_days, "(workspace)");
            println!("{:<32} {:<20} {}", "workspace.check_interval_hours", ws.interval_hours, "(workspace)");
            Ok(0)
        }
        SettingsCmd::Get { key } => {
            if let Some(k) = key.strip_prefix("workspace.") {
                let ws = workspace::settings();
                let v = match k {
                    "root" => ws.root.display().to_string(),
                    "auto_clean" => ws.auto_clean.to_string(),
                    "inactive_days" => ws.inactive_days.to_string(),
                    "check_interval_hours" => ws.interval_hours.to_string(),
                    "keep" => ws.keep.join(","),
                    _ => return err(format!("unknown setting: {}", key)),
                };
                println!("{}", v);
                return Ok(0);
            }
            let cfg = load()?;
            match cfg.settings().get(&key) {
                Some((v, _)) => {
                    println!("{}", v.as_str().map(String::from).unwrap_or_else(|| v.to_string()));
                    Ok(0)
                }
                None => err(format!("setting {} is not set", key)),
            }
        }
        SettingsCmd::Set { key, value } => {
            let (section, k) = match key.strip_prefix("workspace.") {
                Some(k) => ("workspace", k.to_string()),
                None => ("settings", key.clone()),
            };
            config::set_dotted(&g, section, &k, config::parse_value(&value))?;
            Ok(0)
        }
        SettingsCmd::Unset { key } => {
            let (section, k) = match key.strip_prefix("workspace.") {
                Some(k) => ("workspace", k.to_string()),
                None => ("settings", key.clone()),
            };
            config::unset_dotted(&g, section, &k)?;
            Ok(0)
        }
    }
}

fn doctor() -> Res<i32> {
    let mut problems: Vec<String> = vec![];
    println!("version: {}", version_line());
    println!("activated: {}", if std::env::var("__DEV_ORIG_PATH").is_ok() { "yes" } else { "no" });
    println!();
    println!("dirs:");
    println!("  data: {}", display_path(&data_dir()));
    println!("  config: {}", display_path(&config_dir()));
    println!("  cache: {}", display_path(&cache_dir()));
    println!("  state: {}", display_path(&state_dir()));
    println!();
    let cfg = match load() {
        Ok(c) => c,
        Err(e) => {
            problems.push(e);
            Config { files: vec![] }
        }
    };
    println!("config_files:");
    for f in &cfg.files {
        println!("  {}", display_path(&f.path));
    }
    if cfg.files.is_empty() {
        println!("  (none)");
    }
    println!();
    println!("toolset:");
    for a in runtime::active_tools(&cfg, &[]) {
        if a.installed {
            println!("  {}@{}", a.tool, a.version);
        } else {
            println!("  {}@{} (missing)", a.tool, a.request);
            problems.push(format!("{}@{} is not installed; run `dev install`", a.tool, a.request));
        }
    }
    println!();
    let has = |p: &str, a: &str| std::process::Command::new(p).arg(a).output().map(|o| o.status.success()).unwrap_or(false);
    println!("helpers:");
    for (name, arg) in [("git", "--version"), (curl_program(), "--version"), (tar_program(), "--version")] {
        let ok = has(name, arg);
        println!("  {}: {}", name.trim_end_matches(".exe"), if ok { "found" } else { "missing" });
        if !ok {
            problems.push(format!("{} was not found on PATH", name));
        }
    }
    println!();
    if cfg!(windows) {
        println!("windows_user_environment:");
        for (tool, var) in [("java", "JAVA_HOME"), ("maven", "MAVEN_HOME"), ("ant", "ANT_HOME")] {
            let v = setup::windows_user_var(var).filter(|s| !s.trim().is_empty());
            println!("  {}: {}", var, v.as_deref().unwrap_or("(not set)"));
            let installed = !tools::installed_versions(tool).is_empty();
            let ok = v.as_deref().map(|p| Path::new(p).is_dir()).unwrap_or(false);
            if installed && !ok {
                problems.push(format!("{} is installed but {} is not set in Windows; run `dev use -g {}`", tool, var, tool));
            }
        }
        let path = setup::windows_user_var("Path").unwrap_or_default();
        let dev_on_path = path.split(';').any(|p| p.trim_end_matches('\\').eq_ignore_ascii_case(&self_bin_dir().display().to_string()));
        println!("  dev on PATH: {}", if dev_on_path { "yes" } else { "no" });
        if !dev_on_path {
            problems.push("dev's folder is not on your user PATH; run `dev setup`".into());
        }
        if let Some(old) = setup::legacy_bin_dir() {
            if old.join("dev.exe").exists() {
                problems.push(format!("an old copy of dev is still in {}; run `dev setup` to remove it", old.display()));
            }
        }
        println!();
    }
    let ws = workspace::settings();
    println!("workspace:");
    println!("  root: {}{}", display_path(&ws.root), if ws.root.is_dir() { "" } else { " (not created)" });
    println!("  auto_clean: {} (after {} days idle)", if ws.auto_clean { "on" } else { "off" }, ws.inactive_days);
    println!();
    if problems.is_empty() {
        println!("No problems found");
        Ok(0)
    } else {
        println!("{} problem{} found:", problems.len(), if problems.len() == 1 { "" } else { "s" });
        for (i, p) in problems.iter().enumerate() {
            println!("{}. {}", i + 1, p);
        }
        Ok(1)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn s(v: &[&str]) -> Vec<String> {
        v.iter().map(|x| x.to_string()).collect()
    }

    #[test]
    fn versions_after_tool_names_are_joined() {
        // The original joins only two words, the second matching ^\d+(\.\d+)*$ (FUN_144275920).
        assert_eq!(join_versions(s(&["java", "21"])), s(&["java@21"]));
        assert_eq!(join_versions(s(&["java", "21.0.5"])), s(&["java@21.0.5"]));
        assert_eq!(join_versions(s(&["npm:prettier", "3"])), s(&["npm:prettier@3"]));
        assert_eq!(join_versions(s(&["java", "21", "node"])), s(&["java", "21", "node"]));
        assert_eq!(join_versions(s(&["java@21", "17"])), s(&["java@21", "17"]));
        assert_eq!(join_versions(s(&["node", "go"])), s(&["node", "go"]));
        assert_eq!(join_versions(s(&["node", "lts"])), s(&["node", "lts"]));
        assert_eq!(join_versions(s(&["go", "v1.22"])), s(&["go", "v1.22"]));
        assert!(is_numeric_version("21") && is_numeric_version("1.22.3"));
        assert!(!is_numeric_version("21.") && !is_numeric_version("v1") && !is_numeric_version(""));
    }

    #[test]
    fn home_variable_rule() {
        assert!(should_save_home(true, true, true)); // dev use -g: always
        assert!(should_save_home(false, false, false)); // nothing saved yet: fill it in
        assert!(should_save_home(false, true, false)); // saved folder is gone: replace
        assert!(!should_save_home(false, true, true)); // project/install never overrides a working one
    }

    #[test]
    fn version_words() {
        assert!(looks_like_version("21", "java"));
        assert!(looks_like_version("latest", "node"));
        assert!(!looks_like_version("node", "java"));
        assert!(!looks_like_version("zulu-17", "node"));
        assert!(!looks_like_version("github:cli/cli", "java"));
    }
}
