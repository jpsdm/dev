//! The active toolset for a folder: which versions apply, their PATH and env, and running commands.

use crate::config::Config;
use crate::tools;
use crate::util::*;
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::{Command, ExitStatus};

#[derive(Clone, Debug)]
pub struct Active {
    pub tool: String,
    pub request: String,
    pub version: String,
    pub source: Option<PathBuf>,
    pub installed: bool,
}

/// Resolves every requested tool against what is installed (no network).
pub fn active_tools(cfg: &Config, extra: &[(String, String)]) -> Vec<Active> {
    let mut out: Vec<Active> = vec![];
    let mut seen = BTreeMap::new();
    for (tool, req) in extra {
        seen.insert(tool.clone(), ());
        out.push(resolve_local(tool, req, None));
    }
    for (tool, reqs, src) in cfg.tools() {
        if seen.contains_key(&tool) {
            continue;
        }
        for r in reqs {
            out.push(resolve_local(&tool, &r, Some(src.clone())));
        }
    }
    out
}

fn resolve_local(tool: &str, req: &str, source: Option<PathBuf>) -> Active {
    let installed = tools::installed_versions(tool);
    let hit = match req {
        "latest" | "lts" | "" => installed.iter().find(|v| !is_prerelease(v)).or(installed.first()).cloned(),
        _ => installed.iter().find(|v| *v == req || version_matches(v, req)).cloned(),
    };
    match hit {
        Some(v) => Active { tool: tool.into(), request: req.into(), version: v, source, installed: true },
        None => Active { tool: tool.into(), request: req.into(), version: req.into(), source, installed: false },
    }
}

pub fn bin_paths(active: &[Active]) -> Vec<PathBuf> {
    let mut out = vec![];
    for a in active.iter().filter(|a| a.installed) {
        for d in tools::bin_dirs(&a.tool, &tools::install_path(&a.tool, &a.version)) {
            if !out.contains(&d) {
                out.push(d);
            }
        }
    }
    out
}

/// The variables dev sets: config [env], tool versions and PATH.
pub fn env_vars(cfg: &Config, active: &[Active]) -> (Vec<(String, String)>, Vec<PathBuf>) {
    let mut vars = cfg.env();
    let mut paths = cfg.env_paths();
    paths.extend(bin_paths(active));
    for a in active.iter().filter(|a| a.installed) {
        match tools::Backend::of(&a.tool) {
            tools::Backend::Go => {
                let root = tools::real_path(&tools::install_path(&a.tool, &a.version));
                vars.push(("GOROOT".into(), root.display().to_string()));
            }
            // JAVA_HOME = the active JDK's install folder (0x143ef69f3 in the original);
            // MAVEN_HOME / ANT_HOME the same way for Maven and Ant (new in dev).
            _ => {
                if let Some(var) = tools::home_var(&a.tool) {
                    let home = tools::home_dir(&a.tool, &tools::install_path(&a.tool, &a.version));
                    vars.push((var.into(), home.display().to_string()));
                }
            }
        }
    }
    (vars, paths)
}

pub fn full_path(paths: &[PathBuf]) -> String {
    let mut all: Vec<PathBuf> = paths.to_vec();
    let current = std::env::var("PATH").unwrap_or_default();
    for p in split_path_var(&current) {
        if !all.contains(&p) {
            all.push(p);
        }
    }
    std::env::join_paths(all).map(|s| s.to_string_lossy().into_owned()).unwrap_or(current)
}

/// Installs any requested version that is missing (used by exec and run).
pub fn ensure_installed(active: &mut [Active], quiet: bool) -> Res<()> {
    for a in active.iter_mut().filter(|a| !a.installed) {
        if matches!(tools::Backend::of(&a.tool), tools::Backend::Local(_)) {
            warn(format!("{}@{} is not installed (missing)", a.tool, a.request));
            continue;
        }
        let v = tools::resolve(&a.tool, &a.request, true)?;
        if !tools::is_installed(&a.tool, &v) {
            tools::install(&a.tool, &v, false, !quiet)?;
            info(format!("{}@{} ✓ installed", a.tool, v));
        }
        a.version = v;
        a.installed = true;
    }
    Ok(())
}

/// Runs a program with dev's environment and returns how it ended.
pub fn run_program(
    program: &str,
    args: &[String],
    vars: &[(String, String)],
    paths: &[PathBuf],
    dir: Option<&Path>,
) -> Res<ExitStatus> {
    let path_var = full_path(paths);
    let search = split_path_var(&path_var);
    let resolved = find_in_path(program, &search);
    let mut cmd = match &resolved {
        Some(p) if cfg!(windows) && is_script(p) => {
            let mut c = Command::new("cmd.exe");
            c.arg("/C").arg(p).args(args);
            c
        }
        Some(p) => {
            let mut c = Command::new(p);
            c.args(args);
            c
        }
        None if cfg!(windows) => {
            // Let the shell try (built-ins like `echo`).
            let mut c = Command::new("cmd.exe");
            c.arg("/C").arg(program).args(args);
            c
        }
        None => {
            let mut c = Command::new(program);
            c.args(args);
            c
        }
    };
    cmd.env("PATH", &path_var);
    for (k, v) in vars {
        cmd.env(k, v);
    }
    if let Some(d) = dir {
        cmd.current_dir(d);
    }
    cmd.status().map_err(|e| format!("failed to run {}: {}", program, e))
}

fn is_script(p: &Path) -> bool {
    p.extension()
        .map(|e| e.eq_ignore_ascii_case("cmd") || e.eq_ignore_ascii_case("bat"))
        .unwrap_or(false)
}

/// Runs a shell command line (tasks) with dev's environment.
pub fn run_shell(line: &str, vars: &[(String, String)], paths: &[PathBuf], dir: &Path) -> Res<ExitStatus> {
    #[cfg(windows)]
    let mut cmd = {
        use std::os::windows::process::CommandExt;
        let mut c = Command::new("cmd.exe");
        c.raw_arg("/D /S /C").raw_arg(format!("\"{}\"", line));
        c
    };
    #[cfg(not(windows))]
    let mut cmd = {
        let mut c = Command::new("sh");
        c.arg("-c").arg(line);
        c
    };
    cmd.env("PATH", full_path(paths));
    for (k, v) in vars {
        cmd.env(k, v);
    }
    cmd.current_dir(dir);
    cmd.status().map_err(|e| format!("failed to run `{}`: {}", line, e))
}
