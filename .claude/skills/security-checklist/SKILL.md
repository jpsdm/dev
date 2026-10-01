---
name: security-checklist
description: This project's own known security risk patterns, to load alongside a general security review of the dev CLI. Use before or during a security review of this codebase, or when adding/reviewing any code that downloads, extracts, or installs external content.
---

# Security Checklist for `dev`

This is not a general security-review methodology — use the installed `security-review` or
`security` skill for that. This is the short list of risk patterns **specific to this codebase**
that a generic review won't know to look for without being told. Load this alongside a general
review, or run through it directly when touching any of the areas below.

## Why this exists

`dev` downloads and extracts untrusted archives from external APIs (npm/Node's dist index,
Eclipse Temurin/Adoptium, and whatever future language providers add), then runs the extracted
binaries — and it manages `PATH` by generating shell code the user's shell `eval`s. Every one of
these steps is a place where trusting external input without validation becomes a real
vulnerability, not a theoretical one — this project has already had three real findings caught
by review (two path-injection, one shell-injection), all listed below as patterns to keep
checking for.

## 1. Path injection via externally-sourced strings used in filesystem paths

**Real finding, not hypothetical:** during the Java provider's implementation, `resolveRelease`
validated the release *name* from Adoptium's API response before using it, but not the sibling
`binary.package.name` (download filename) field from the *same* response — even though both
flow into `filepath.Join` calls. A crafted filename containing `../` segments could have written
a downloaded archive outside the intended cache directory.

**What to check:** every string that originates from a network response (an API's JSON field, an
HTTP header, a filename embedded in a manifest) and is later passed to `filepath.Join`,
`os.Rename`, or used to construct a path in any way, must be validated first — see
`internal/runtime.ValidVersionName` for the established check (reject empty, `.`, `..`, and
anything containing a path separator). When reviewing a diff, grep for every field read off a
parsed API response and trace each one forward to confirm it either never reaches a path, or is
validated before it does. Don't stop at the first field that looks obviously path-like — check
all of them, the way the filename field was missed the first time.

## 2. Zip-slip / archive extraction safety

All extraction must go through `internal/installer.ExtractAtomic`, which validates that every
archive entry's resolved path stays inside the destination directory and that symlink targets
don't escape it either (`validateSymlinkTarget`). **Never** add a second, ad hoc extraction path
(a raw `archive/zip` or `archive/tar` loop) anywhere in this codebase — if the existing helper
doesn't support something a new provider needs, extend it there, don't bypass it.

## 3. Checksum verification before use

Every downloaded file must be checksum-verified via `internal/downloader.Download` before
anything else touches it (extraction, execution). If a new provider's source API doesn't
provide a checksum for a given asset, that's a design problem worth stopping on and discussing —
don't silently skip verification for one specific case.

## 4. Atomic install / no partial state on failure

Installs use a rename-aside-then-swap pattern (`installer.ReconcileStaleBackup` plus a single
`os.Rename` as the only point-of-no-return) specifically so a killed-mid-install process never
leaves a corrupted, half-extracted version directory that later gets treated as a valid install.
When reviewing a new provider's `Install`, confirm this pattern is reused, not reinvented with a
plain `os.RemoveAll` + `os.Rename` (which has a real window where a crash destroys the old
install without the new one being in place).

## 5. Shell-code generation: `internal/shell` is a real `eval` sink

**This is the highest-value thing on this list, and the thing a generic review is least likely
to look for.** Do not believe any older claim that this project has "no shell interpolation" —
it had that property only while shim dispatch existed, and the shell-function `PATH` redesign
(`docs/superpowers/specs/2026-10-01-shell-function-path-management-design.md`) removed
`internal/shim` entirely and replaced it with the opposite design.

`dev` now manages `PATH` by **generating shell code that the shell then evaluates**. `dev setup`
writes a `dev` wrapper function into the user's rc file; that function, and the one-time line at
the end of the same block, run:

```
# POSIX (bash/zsh/sh)
eval "$(command dev env)"
# Fish
eval (command dev env | string collect)
# PowerShell
(& "$env:DEV_HOME\dev.exe" env) -join "`n" | Invoke-Expression
```

So every string `internal/shell` interpolates into `ExportLines`/`FunctionLines` output —
`DEV_HOME` and **every single `PATH` entry**, which includes directory names `dev` did not
choose — is executed as shell code unless it is quoted correctly for that specific shell.

**What to check:**

- Every value interpolated into generated shell code goes through the right quoting helper:
  `shellQuote` (POSIX single-quoting via the close/escape/reopen trick), `fishQuote` (Fish —
  deliberately *different*, because fish honours `\'` and `\\` as escapes inside single quotes
  where POSIX honours none, so POSIX-quoted output corrupts any value containing a backslash),
  `escapePowerShellDoubleQuoted` (backtick first, then `$`, then `"`). A raw `fmt.Sprintf` of a
  value into a generated line is a finding.
- **POSIX double quotes are not a defence.** They suppress neither `` `backtick` `` nor `$(...)`
  command substitution. A real finding on this branch: `export PATH="%s"` executed a `touch`
  embedded in a directory name. Only single quotes suppress substitution.
- **Fish needs per-entry quoting**, not one quoted string: `set -gx PATH` takes a list, so a
  single space-joined value cannot be quoted as a whole, and an unquoted entry containing a
  space silently becomes two bogus `PATH` entries.
- **Verify empirically, in the real shell.** Three successive diff reviews approved the broken
  quoting; running the generated block through real `bash`/`sh`/`fish` with a path containing a
  space, a backtick, a `$(...)`, a single quote and a double quote found it immediately. See
  `cmd/shellintegration_test.go` for the established pattern (and read its comments — two of
  those adversarial characters *mask* the bug by turning the pre-fix output into a parse error,
  so mixing them into one test makes it silently stop proving anything).

Separately, the older property still holds where it applies: provider code never builds a
command line as a string for `sh -c`, and `os/exec` is always called with an argument slice. If
new code does construct a shell command string, that's still a red flag worth stopping on.

## 6. `DEV_HOME` and other environment-derived paths

`platform.DevHome()` honors the `DEV_HOME` environment variable directly as a directory path
with no validation beyond what the OS itself enforces. This is an accepted, intentional design
(it's the user's own environment, not untrusted external input) — don't flag it as a finding,
but do keep in mind that anything built *from* `DEV_HOME` (via `filepath.Join`) inherits whatever
trust level `DEV_HOME` itself has, which is "as trusted as the user's own shell environment," not
"validated."
