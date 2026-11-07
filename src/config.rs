//! Config files: finding them, reading tools/env/tasks/settings, and writing changes.

use crate::util::*;
use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};
use toml_edit::{value, Array, DocumentMut, Item, Table};

/// File names looked for in each folder, highest priority first.
pub const CONFIG_NAMES: &[&str] = &["dev.local.toml", "dev.toml", ".dev.toml", ".tool-versions"];

#[derive(Clone, Debug, Default)]
pub struct Task {
    pub name: String,
    pub description: String,
    pub run: Vec<String>,
    pub depends: Vec<String>,
    pub dir: Option<String>,
    pub env: Vec<(String, String)>,
    pub source: PathBuf,
}

#[derive(Clone, Debug)]
pub struct ConfigFile {
    pub path: PathBuf,
    pub tools: Vec<(String, Vec<String>)>,
    pub env: Vec<(String, String)>,
    pub env_path: Vec<String>,
    pub tasks: Vec<Task>,
    pub settings: BTreeMap<String, toml::Value>,
    pub is_global: bool,
}

impl ConfigFile {
    pub fn load(path: &Path, is_global: bool) -> Res<ConfigFile> {
        let text = fs::read_to_string(path).map_err(|e| format!("{}: {}", path.display(), e))?;
        let mut cf = ConfigFile {
            path: path.to_path_buf(),
            tools: vec![],
            env: vec![],
            env_path: vec![],
            tasks: vec![],
            settings: BTreeMap::new(),
            is_global,
        };
        if path.file_name().map(|n| n == ".tool-versions").unwrap_or(false) {
            for line in text.lines() {
                let line = line.split('#').next().unwrap_or("").trim();
                let mut parts = line.split_whitespace();
                if let Some(tool) = parts.next() {
                    let versions: Vec<String> = parts.map(|s| s.to_string()).collect();
                    if !versions.is_empty() {
                        cf.tools.push((tool.to_string(), versions));
                    }
                }
            }
            return Ok(cf);
        }
        let doc: toml::Table = text
            .parse()
            .map_err(|e: toml::de::Error| format!("failed to parse {}: {}", path.display(), e.message()))?;
        if let Some(toml::Value::Table(t)) = doc.get("tools") {
            for (k, v) in t {
                let versions = match v {
                    toml::Value::String(s) => vec![s.clone()],
                    toml::Value::Array(a) => a
                        .iter()
                        .filter_map(|x| match x {
                            toml::Value::String(s) => Some(s.clone()),
                            toml::Value::Table(t) => t.get("version").and_then(|v| v.as_str()).map(String::from),
                            _ => None,
                        })
                        .collect(),
                    toml::Value::Table(t) => t
                        .get("version")
                        .and_then(|v| v.as_str())
                        .map(|s| vec![s.to_string()])
                        .unwrap_or_default(),
                    toml::Value::Integer(i) => vec![i.to_string()],
                    toml::Value::Float(f) => vec![f.to_string()],
                    _ => vec![],
                };
                if !versions.is_empty() {
                    cf.tools.push((k.clone(), versions));
                }
            }
        }
        if let Some(toml::Value::Table(t)) = doc.get("env") {
            for (k, v) in t {
                if k == "_" {
                    if let Some(p) = v.get("path") {
                        match p {
                            toml::Value::String(s) => cf.env_path.push(s.clone()),
                            toml::Value::Array(a) => {
                                cf.env_path.extend(a.iter().filter_map(|x| x.as_str().map(String::from)))
                            }
                            _ => {}
                        }
                    }
                    continue;
                }
                let s = match v {
                    toml::Value::String(s) => s.clone(),
                    toml::Value::Boolean(b) => b.to_string(),
                    toml::Value::Integer(i) => i.to_string(),
                    toml::Value::Float(f) => f.to_string(),
                    _ => continue,
                };
                cf.env.push((k.clone(), s));
            }
        }
        if let Some(toml::Value::Table(t)) = doc.get("tasks") {
            for (name, v) in t {
                let mut task = Task { name: name.clone(), source: path.to_path_buf(), ..Default::default() };
                match v {
                    toml::Value::String(s) => task.run = vec![s.clone()],
                    toml::Value::Array(a) => task.run = a.iter().filter_map(|x| x.as_str().map(String::from)).collect(),
                    toml::Value::Table(tt) => {
                        task.description = tt.get("description").and_then(|x| x.as_str()).unwrap_or("").into();
                        task.run = str_list(tt.get("run"));
                        task.depends = str_list(tt.get("depends"));
                        task.dir = tt.get("dir").and_then(|x| x.as_str()).map(String::from);
                        if let Some(toml::Value::Table(e)) = tt.get("env") {
                            for (k, v) in e {
                                if let Some(s) = v.as_str() {
                                    task.env.push((k.clone(), s.to_string()));
                                } else {
                                    task.env.push((k.clone(), v.to_string()));
                                }
                            }
                        }
                    }
                    _ => continue,
                }
                cf.tasks.push(task);
            }
        }
        if let Some(toml::Value::Table(t)) = doc.get("settings") {
            flatten("", t, &mut cf.settings);
        }
        Ok(cf)
    }

    pub fn dir(&self) -> PathBuf {
        self.path.parent().map(Path::to_path_buf).unwrap_or_default()
    }
}

fn flatten(prefix: &str, t: &toml::Table, out: &mut BTreeMap<String, toml::Value>) {
    for (k, v) in t {
        let key = if prefix.is_empty() { k.clone() } else { format!("{}.{}", prefix, k) };
        if let toml::Value::Table(inner) = v {
            flatten(&key, inner, out);
        } else {
            out.insert(key, v.clone());
        }
    }
}

fn str_list(v: Option<&toml::Value>) -> Vec<String> {
    match v {
        Some(toml::Value::String(s)) => vec![s.clone()],
        Some(toml::Value::Array(a)) => a.iter().filter_map(|x| x.as_str().map(String::from)).collect(),
        _ => vec![],
    }
}

/// Every config that applies in `cwd`: nearest first, the global config last.
pub struct Config {
    pub files: Vec<ConfigFile>,
}

impl Config {
    pub fn load(cwd: &Path) -> Res<Config> {
        let mut files = vec![];
        let global = global_config_path();
        let home = home();
        let mut dir = Some(cwd.to_path_buf());
        while let Some(d) = dir {
            for name in CONFIG_NAMES {
                let p = d.join(name);
                if p.is_file() && p != global {
                    files.push(ConfigFile::load(&p, false)?);
                }
            }
            // The config folder inside a project counts too (dev/config.toml).
            let p = d.join(".config").join(NAME).join("config.toml");
            if d != home && p.is_file() && p != global {
                files.push(ConfigFile::load(&p, false)?);
            }
            dir = d.parent().map(Path::to_path_buf);
        }
        if global.is_file() {
            files.push(ConfigFile::load(&global, true)?);
        }
        Ok(Config { files })
    }

    /// Tool requests with the file that made them; the nearest file wins per tool.
    pub fn tools(&self) -> Vec<(String, Vec<String>, PathBuf)> {
        let mut seen = BTreeMap::new();
        let mut out = vec![];
        for f in &self.files {
            for (t, v) in &f.tools {
                if seen.insert(t.clone(), ()).is_none() {
                    out.push((t.clone(), v.clone(), f.path.clone()));
                }
            }
        }
        out
    }

    pub fn env(&self) -> Vec<(String, String)> {
        let mut map: BTreeMap<String, String> = BTreeMap::new();
        for f in self.files.iter().rev() {
            for (k, v) in &f.env {
                map.insert(k.clone(), expand(v, &f.dir()));
            }
        }
        map.into_iter().collect()
    }

    pub fn env_paths(&self) -> Vec<PathBuf> {
        let mut out = vec![];
        for f in &self.files {
            for p in &f.env_path {
                let e = expand(p, &f.dir());
                let pb = PathBuf::from(&e);
                out.push(if pb.is_absolute() { pb } else { f.dir().join(pb) });
            }
        }
        out
    }

    pub fn tasks(&self) -> BTreeMap<String, Task> {
        let mut map = BTreeMap::new();
        for f in self.files.iter().rev() {
            for t in &f.tasks {
                map.insert(t.name.clone(), t.clone());
            }
        }
        map
    }

    pub fn settings(&self) -> BTreeMap<String, (toml::Value, PathBuf)> {
        let mut map = BTreeMap::new();
        for f in self.files.iter().rev() {
            for (k, v) in &f.settings {
                map.insert(k.clone(), (v.clone(), f.path.clone()));
            }
        }
        map
    }
}

/// `{{config_root}}`, `~` and `$VAR` / `${VAR}` / `%VAR%` in config values.
pub fn expand(s: &str, root: &Path) -> String {
    let mut out = s.replace("{{config_root}}", &root.display().to_string());
    if out.starts_with("~/") || out.starts_with("~\\") || out == "~" {
        out = format!("{}{}", home().display(), &out[1..]);
    }
    let mut res = String::new();
    let chars: Vec<char> = out.chars().collect();
    let mut i = 0;
    while i < chars.len() {
        if chars[i] == '$' && i + 1 < chars.len() {
            let (name, next) = if chars[i + 1] == '{' {
                let end = chars[i + 2..].iter().position(|c| *c == '}').map(|p| p + i + 2);
                match end {
                    Some(e) => (chars[i + 2..e].iter().collect::<String>(), e + 1),
                    None => (String::new(), i + 1),
                }
            } else {
                let mut j = i + 1;
                while j < chars.len() && (chars[j].is_ascii_alphanumeric() || chars[j] == '_') {
                    j += 1;
                }
                (chars[i + 1..j].iter().collect::<String>(), j)
            };
            if !name.is_empty() {
                res.push_str(&std::env::var(&name).unwrap_or_default());
                i = next;
                continue;
            }
        }
        res.push(chars[i]);
        i += 1;
    }
    res
}

/// Which file `dev use` writes to: the nearest project config, else dev.toml here.
pub fn target_for_write(cwd: &Path, global: bool, path: Option<&Path>, env_name: Option<&str>) -> PathBuf {
    if let Some(p) = path {
        let p = if p.is_absolute() { p.to_path_buf() } else { cwd.join(p) };
        return if p.is_dir() { p.join("dev.toml") } else { p };
    }
    if global || cwd == home() {
        return global_config_path();
    }
    if let Some(e) = env_name {
        let hidden = cwd.join(format!(".dev.{}.toml", e));
        return if hidden.is_file() { hidden } else { cwd.join(format!("dev.{}.toml", e)) };
    }
    let mut dir = Some(cwd.to_path_buf());
    while let Some(d) = dir {
        if d == home() {
            break;
        }
        for name in ["dev.toml", ".dev.toml"] {
            let p = d.join(name);
            if p.is_file() {
                return p;
            }
        }
        dir = d.parent().map(Path::to_path_buf);
    }
    cwd.join("dev.toml")
}

pub fn open_doc(path: &Path) -> Res<DocumentMut> {
    if !path.exists() {
        return Ok(DocumentMut::new());
    }
    let text = fs::read_to_string(path).map_err(|e| format!("{}: {}", path.display(), e))?;
    text.parse::<DocumentMut>().map_err(|e| format!("failed to parse {}: {}", path.display(), e))
}

pub fn save_doc(path: &Path, doc: &DocumentMut) -> Res<()> {
    write_file(path, &doc.to_string())
}

fn table_mut<'a>(doc: &'a mut DocumentMut, name: &str) -> &'a mut Table {
    if !doc.contains_table(name) {
        let mut t = Table::new();
        t.set_implicit(false);
        doc.insert(name, Item::Table(t));
    }
    doc[name].as_table_mut().expect("table")
}

pub fn set_tool(path: &Path, tool: &str, versions: &[String]) -> Res<()> {
    if path.file_name().map(|n| n == ".tool-versions").unwrap_or(false) {
        let text = fs::read_to_string(path).unwrap_or_default();
        let mut lines: Vec<String> = text
            .lines()
            .filter(|l| l.split_whitespace().next() != Some(tool))
            .map(String::from)
            .collect();
        lines.push(format!("{} {}", tool, versions.join(" ")));
        return write_file(path, &(lines.join("\n") + "\n"));
    }
    let mut doc = open_doc(path)?;
    let t = table_mut(&mut doc, "tools");
    if versions.len() == 1 {
        t[tool] = value(versions[0].clone());
    } else {
        let mut a = Array::new();
        for v in versions {
            a.push(v.clone());
        }
        t[tool] = value(a);
    }
    save_doc(path, &doc)
}

pub fn remove_tool(path: &Path, tool: &str) -> Res<bool> {
    if !path.exists() {
        return Ok(false);
    }
    let mut doc = open_doc(path)?;
    let removed = doc
        .get_mut("tools")
        .and_then(|t| t.as_table_like_mut())
        .map(|t| t.remove(tool).is_some())
        .unwrap_or(false);
    if removed {
        save_doc(path, &doc)?;
    }
    Ok(removed)
}

pub fn set_env(path: &Path, key: &str, val: &str) -> Res<()> {
    let mut doc = open_doc(path)?;
    table_mut(&mut doc, "env")[key] = value(val);
    save_doc(path, &doc)
}

pub fn unset_env(path: &Path, key: &str) -> Res<bool> {
    let mut doc = open_doc(path)?;
    let removed = doc
        .get_mut("env")
        .and_then(|t| t.as_table_like_mut())
        .map(|t| t.remove(key).is_some())
        .unwrap_or(false);
    if removed {
        save_doc(path, &doc)?;
    }
    Ok(removed)
}

/// Writes `section.a.b = value` (dotted key) into a config file.
pub fn set_dotted(path: &Path, section: &str, key: &str, v: toml_edit::Value) -> Res<()> {
    let mut doc = open_doc(path)?;
    let mut t = table_mut(&mut doc, section);
    let parts: Vec<&str> = key.split('.').collect();
    for p in &parts[..parts.len() - 1] {
        if !t.contains_key(p) {
            let mut nt = Table::new();
            nt.set_implicit(true);
            t.insert(p, Item::Table(nt));
        }
        t = t[p].as_table_mut().ok_or_else(|| format!("{} is not a table", p))?;
    }
    t[parts[parts.len() - 1]] = Item::Value(v);
    save_doc(path, &doc)
}

pub fn unset_dotted(path: &Path, section: &str, key: &str) -> Res<bool> {
    let mut doc = open_doc(path)?;
    let mut item: &mut Item = match doc.get_mut(section) {
        Some(i) => i,
        None => return Ok(false),
    };
    let parts: Vec<&str> = key.split('.').collect();
    for p in &parts[..parts.len() - 1] {
        item = match item.get_mut(p) {
            Some(i) => i,
            None => return Ok(false),
        };
    }
    let removed = item
        .as_table_like_mut()
        .map(|t| t.remove(parts[parts.len() - 1]).is_some())
        .unwrap_or(false);
    if removed {
        save_doc(path, &doc)?;
    }
    Ok(removed)
}

/// Turns text typed on the command line into a TOML value (number, true/false or text).
pub fn parse_value(s: &str) -> toml_edit::Value {
    if s == "true" || s == "false" {
        return toml_edit::Value::from(s == "true");
    }
    if let Ok(i) = s.parse::<i64>() {
        return toml_edit::Value::from(i);
    }
    if let Ok(f) = s.parse::<f64>() {
        if s.contains('.') {
            return toml_edit::Value::from(f);
        }
    }
    if s.starts_with('[') {
        if let Ok(v) = s.parse::<toml_edit::Value>() {
            return v;
        }
    }
    toml_edit::Value::from(s)
}

pub const STARTER: &str = r#"# dev.toml — tools, environment and tasks for this folder.
# Docs: run `dev --help`.

[tools]
# node = "24"
# go = "latest"
# "github:cli/cli" = "latest"

[env]
# DATABASE_URL = "postgres://localhost/dev"
# _.path = ["./bin"]

[tasks.hello]
description = "Say hello"
run = "echo hello from dev"
"#;
