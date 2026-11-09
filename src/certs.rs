//! `dev add cacert`: add company / custom certificates to the trust store (cacerts) of installed
//! JDKs. The certificates are kept in dev's own `certs` folder, and every JDK installed later gets
//! them too. New in dev: the original has no such command.

use crate::tools;
use crate::util::*;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

/// File types read as certificates.
pub const CERT_EXTENSIONS: &[&str] = &["crt", "cer", "pem", "der"];
/// The JDK trust store's default password (DEV_CACERTS_PASSWORD changes it).
pub const DEFAULT_STOREPASS: &str = "changeit";
/// Aliases dev gives the certificates it adds, so they can be told apart.
pub const ALIAS_PREFIX: &str = "dev-";

/// `<dev data>/certs`: %LOCALAPPDATA%\dev\certs on Windows, ~/.local/share/dev/certs elsewhere.
pub fn certs_dir() -> PathBuf {
    data_dir().join("certs")
}

fn storepass() -> String {
    std::env::var("DEV_CACERTS_PASSWORD").ok().filter(|p| !p.is_empty()).unwrap_or_else(|| DEFAULT_STOREPASS.into())
}

fn is_cert_file(p: &Path) -> bool {
    p.is_file()
        && p.extension()
            .map(|e| CERT_EXTENSIONS.iter().any(|x| e.eq_ignore_ascii_case(x)))
            .unwrap_or(false)
}

/// A certificate file, or every certificate file in a folder (not recursive).
pub fn find_cert_files(path: &Path) -> Res<Vec<PathBuf>> {
    if path.is_file() {
        return Ok(vec![path.to_path_buf()]);
    }
    if !path.is_dir() {
        return err(format!("{} does not exist", path.display()));
    }
    let mut files: Vec<PathBuf> = fs::read_dir(path)
        .map_err(|e| format!("{}: {}", path.display(), e))?
        .flatten()
        .map(|e| e.path())
        .filter(|p| is_cert_file(p))
        .collect();
    files.sort();
    if files.is_empty() {
        return err(format!("no certificate files (.crt, .cer, .pem, .der) in {}", path.display()));
    }
    Ok(files)
}

/// The keytool alias for a certificate: `dev-<file name without extension>`, lower-case, with
/// anything other than letters, digits, `-`, `_` and `.` turned into `-`.
pub fn alias_for(file: &Path, part: Option<usize>) -> String {
    let stem = file.file_stem().map(|s| s.to_string_lossy().to_lowercase()).unwrap_or_default();
    let clean: String = stem
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.' { c } else { '-' })
        .collect();
    match part {
        Some(n) => format!("{}{}-{}", ALIAS_PREFIX, clean, n),
        None => format!("{}{}", ALIAS_PREFIX, clean),
    }
}

/// Splits a PEM bundle into its certificates (keytool only reads the first of a bundle).
/// A file without PEM blocks (DER) is returned whole.
pub fn split_pem(text: &str) -> Vec<String> {
    const BEGIN: &str = "-----BEGIN CERTIFICATE-----";
    const END: &str = "-----END CERTIFICATE-----";
    let mut out = vec![];
    let mut rest = text;
    while let Some(s) = rest.find(BEGIN) {
        match rest[s..].find(END) {
            Some(e) => {
                out.push(format!("{}\n", &rest[s..s + e + END.len()]));
                rest = &rest[s + e + END.len()..];
            }
            None => break,
        }
    }
    out
}

/// One certificate to import: alias and the file keytool reads.
struct CertItem {
    alias: String,
    file: PathBuf,
}

/// Copies the certificates into dev's certs folder and returns the items to import
/// (bundles split into one file per certificate).
fn store_certs(files: &[PathBuf]) -> Res<Vec<CertItem>> {
    let dir = certs_dir();
    fs::create_dir_all(&dir).map_err(|e| format!("{}: {}", dir.display(), e))?;
    let mut items = vec![];
    for f in files {
        let name = f.file_name().ok_or("bad certificate file name")?;
        let dest = dir.join(name);
        if !same_file(f, &dest) {
            fs::copy(f, &dest).map_err(|e| format!("could not copy {} to {}: {}", f.display(), dest.display(), e))?;
        }
        items.extend(items_for(&dest)?);
    }
    Ok(items)
}

fn same_file(a: &Path, b: &Path) -> bool {
    match (fs::canonicalize(a), fs::canonicalize(b)) {
        (Ok(x), Ok(y)) => x == y,
        _ => false,
    }
}

/// The import items for one stored certificate file (bundle parts go to certs/.parts/).
fn items_for(stored: &Path) -> Res<Vec<CertItem>> {
    let bytes = fs::read(stored).map_err(|e| format!("{}: {}", stored.display(), e))?;
    let text = String::from_utf8_lossy(&bytes);
    let parts = split_pem(&text);
    if parts.len() <= 1 {
        return Ok(vec![CertItem { alias: alias_for(stored, None), file: stored.to_path_buf() }]);
    }
    let pdir = certs_dir().join(".parts");
    fs::create_dir_all(&pdir).map_err(|e| e.to_string())?;
    let stem = stored.file_stem().map(|s| s.to_string_lossy().to_string()).unwrap_or_default();
    let mut out = vec![];
    for (i, p) in parts.iter().enumerate() {
        let f = pdir.join(format!("{}-{}.pem", stem, i + 1));
        write_file(&f, p)?;
        out.push(CertItem { alias: alias_for(stored, Some(i + 1)), file: f });
    }
    Ok(out)
}

/// Every certificate kept in dev's certs folder, as import items.
fn stored_items() -> Vec<CertItem> {
    let mut files: Vec<PathBuf> = fs::read_dir(certs_dir())
        .map(|rd| rd.flatten().map(|e| e.path()).filter(|p| is_cert_file(p)).collect())
        .unwrap_or_default();
    files.sort();
    files.iter().filter_map(|f| items_for(f).ok()).flatten().collect()
}

/// keytool and the trust store of a JDK. Java 9+ keep it in lib/security/cacerts, Java 8 JDKs in
/// jre/lib/security/cacerts.
pub fn jdk_paths(java_home: &Path) -> Res<(PathBuf, PathBuf)> {
    let keytool = java_home.join("bin").join(if cfg!(windows) { "keytool.exe" } else { "keytool" });
    if !keytool.is_file() {
        return err(format!("keytool not found in {}", java_home.display()));
    }
    let store = [java_home.join("lib").join("security").join("cacerts"), java_home.join("jre").join("lib").join("security").join("cacerts")]
        .into_iter()
        .find(|p| p.is_file())
        .ok_or_else(|| format!("no cacerts file in {}", java_home.display()))?;
    Ok((keytool, store))
}

fn keytool(keytool: &Path, args: &[&str]) -> Res<(bool, String)> {
    let out = Command::new(keytool)
        .args(args)
        .stdin(Stdio::null())
        .output()
        .map_err(|e| format!("could not run keytool: {}", e))?;
    let text = format!("{}{}", String::from_utf8_lossy(&out.stdout), String::from_utf8_lossy(&out.stderr));
    Ok((out.status.success(), text.trim().to_string()))
}

#[derive(Default, Debug)]
pub struct Counts {
    pub added: usize,
    pub replaced: usize,
    pub failed: usize,
}

/// Imports the items into one JDK's cacerts; an alias that already exists is replaced.
fn import_into(java_home: &Path, items: &[CertItem]) -> Res<Counts> {
    let (kt, store) = jdk_paths(java_home)?;
    let store_s = store.display().to_string();
    let pass = storepass();
    let mut c = Counts::default();
    for it in items {
        let (exists, _) = keytool(&kt, &["-list", "-keystore", &store_s, "-storepass", &pass, "-alias", &it.alias])?;
        if exists {
            let _ = keytool(&kt, &["-delete", "-keystore", &store_s, "-storepass", &pass, "-alias", &it.alias])?;
        }
        let file_s = it.file.display().to_string();
        let (ok, text) = keytool(
            &kt,
            &["-importcert", "-noprompt", "-trustcacerts", "-alias", &it.alias, "-file", &file_s, "-keystore", &store_s, "-storepass", &pass],
        )?;
        if ok {
            if exists {
                c.replaced += 1;
            } else {
                c.added += 1;
            }
        } else {
            c.failed += 1;
            warn(format!("{}: {}", it.alias, text.lines().last().unwrap_or("keytool failed")));
        }
    }
    Ok(c)
}

/// Installed Java versions chosen by `8,11,17` (a version prefix or exact name, vendor names such
/// as zulu-17 too) or all of them.
pub fn select_javas(selectors: &[String], all: bool) -> Res<Vec<String>> {
    let installed = tools::installed_versions("java");
    if installed.is_empty() {
        return err("no Java is installed; install one first, such as `dev install java@21`");
    }
    if all {
        return Ok(installed);
    }
    let wanted: Vec<String> = selectors
        .iter()
        .flat_map(|s| s.split(','))
        .map(|s| s.trim().trim_start_matches("java@").to_string())
        .filter(|s| !s.is_empty())
        .collect();
    if wanted.is_empty() {
        return err("say which Java versions, such as `8,11,17`, or --all");
    }
    let mut out: Vec<String> = vec![];
    for w in &wanted {
        let hits: Vec<&String> = installed.iter().filter(|v| *v == w || version_matches(v, w)).collect();
        if hits.is_empty() {
            return err(format!("Java {} is not installed (installed: {})", w, installed.join(", ")));
        }
        for h in hits {
            if !out.contains(h) {
                out.push(h.clone());
            }
        }
    }
    Ok(out)
}

/// `dev add cacert <PATH> <VERSIONS>|--all`.
pub fn add_cacert(path: &Path, selectors: &[String], all: bool, quiet: bool) -> Res<i32> {
    let files = find_cert_files(path)?;
    let javas = select_javas(selectors, all)?;
    let items = store_certs(&files)?;
    if !quiet {
        info(format!(
            "{} certificate{} saved in {}",
            items.len(),
            if items.len() == 1 { "" } else { "s" },
            display_path(&certs_dir())
        ));
    }
    let mut failed = 0;
    for v in &javas {
        let home = tools::java_home(&tools::install_path("java", v));
        match import_into(&home, &items) {
            Ok(c) => {
                failed += c.failed;
                if !quiet {
                    info(format!("java@{}: {} added, {} replaced{}", v, c.added, c.replaced, if c.failed > 0 { format!(", {} failed", c.failed) } else { String::new() }));
                }
            }
            Err(e) => {
                failed += 1;
                warn(format!("java@{}: {}", v, e));
            }
        }
    }
    Ok(if failed > 0 { 1 } else { 0 })
}

/// After a JDK is installed: add every certificate already in dev's certs folder.
/// DEV_JAVA_AUTO_CACERTS=0 turns this off.
pub fn auto_apply(java_home: &Path, version: &str) {
    if matches!(std::env::var("DEV_JAVA_AUTO_CACERTS").as_deref(), Ok("0") | Ok("false")) {
        return;
    }
    let items = stored_items();
    if items.is_empty() {
        return;
    }
    match import_into(java_home, &items) {
        Ok(c) => info(format!("java@{}: {} certificate{} from {} added", version, c.added + c.replaced, if c.added + c.replaced == 1 { "" } else { "s" }, display_path(&certs_dir()))),
        Err(e) => warn(format!("java@{}: could not add the saved certificates: {}", version, e)),
    }
}

/// `dev add cacert --list`: the saved certificates.
pub fn list() -> Res<i32> {
    let items = stored_items();
    if items.is_empty() {
        info(format!("no certificates saved in {}", display_path(&certs_dir())));
    }
    for it in items {
        println!("{:<40} {}", it.alias, it.file.display());
    }
    Ok(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn aliases() {
        assert_eq!(alias_for(Path::new(r"C:\certs\Corp Root CA.crt"), None), "dev-corp-root-ca");
        assert_eq!(alias_for(Path::new("/c/bundle.pem"), Some(2)), "dev-bundle-2");
    }

    #[test]
    fn pem_bundles_split() {
        let one = "-----BEGIN CERTIFICATE-----\nAAA\n-----END CERTIFICATE-----\n";
        let two = format!("junk\n{}{}", one, one.replace("AAA", "BBB"));
        assert_eq!(split_pem(one).len(), 1);
        let parts = split_pem(&two);
        assert_eq!(parts.len(), 2);
        assert!(parts[1].contains("BBB"));
        assert!(split_pem("binary DER").is_empty());
    }
}
