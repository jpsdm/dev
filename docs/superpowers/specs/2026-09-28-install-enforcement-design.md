# Install-Location Enforcement & Real Windows Env Config — Design Spec

Date: 2026-09-28
Status: Approved for planning

## Context

A real user downloaded a Windows release archive, extracted it into a folder they
happened to name `.dev` (not realizing this had no relationship to `$DEV_HOME`), and
got confused about where `dev.exe` was supposed to live relative to `$DEV_HOME/bin`
(the shim directory `dev setup` creates on its own). Separately, Windows/PowerShell
has never had real automatic environment configuration — `dev setup` there only
prints manual instructions, unlike bash/zsh/fish, which already auto-edit an rc file.

Both problems have the same root cause: **there is no real "installed" concept**.
`dev` runs correctly from anywhere it happens to be extracted, so there's no natural
place for `dev setup` to make the whole environment self-consistent, and no way to
tell a user "you're running an uninstalled copy" versus "you're running the real,
configured one."

This redesign gives `dev` a real installed location (`$DEV_HOME` itself, not just its
`versions`/`bin`/`current`/`cache` subdirectories) and makes `dev setup` the one
command that bootstraps a fresh download into that location — on all three platforms
identically.

## Goals

- Running `dev` (or a shim) from outside `$DEV_HOME` refuses to do anything except
  `setup` (and `--version`/`version`, which stay available unconditionally), with a
  clear message explaining why and what to run.
- `dev setup`, after creating `$DEV_HOME` and populating `$DEV_HOME/bin` with shims
  (unchanged from today), offers to relocate the running binary plus `README.md` and
  `LICENSE` into `$DEV_HOME` itself — explicitly **not** `$DEV_HOME/bin`, which stays
  shim-only.
- `PATH` gets **two** entries after this: `$DEV_HOME` (for `dev` itself) and
  `$DEV_HOME/bin` (for the shims) — on every supported shell, including the two new
  Windows registry entries.
- Windows gets real, automatic, confirmation-gated environment configuration for the
  first time — writing to the **current user's** environment
  (`HKEY_CURRENT_USER\Environment`), never system-wide (`HKEY_LOCAL_MACHINE`, which
  needs admin rights and affects every user on the machine).
- Identical experience on Linux, macOS, and Windows: `dev setup` asks once, confirms,
  and configures everything — no manual PATH editing on any platform.

## Non-goals

- Uninstalling / removing an existing `$DEV_HOME` installation — out of scope, not
  requested.
- Detecting or migrating a `$DEV_HOME` from a version of `dev` that predates this
  change (i.e. one where `dev` itself was never relocated there) beyond what
  `dev setup`'s existing idempotent re-run behavior already provides — running
  `dev setup` again is the migration path, not a separate one-time upgrade command.
- Any change to `dev`'s actual language-management or workspace behavior — this is
  entirely about installation/bootstrapping.
- System-wide (`HKEY_LOCAL_MACHINE`) Windows environment configuration — explicitly
  excluded per the user's own requirement, not a follow-up to add later without a
  fresh discussion (it has real implications: admin rights, affecting every user on
  the machine).

## Files

- **`internal/platform/installed.go`** (new) — the shared location-check helper both
  dispatch paths (shim and Cobra) call.
- **`main.go`** (modified) — the shim-dispatch path gains a location check before
  ever calling `shim.Run`.
- **`cmd/root.go`** (modified) — `PersistentPreRun` becomes `PersistentPreRunE` and
  gains the same check, exempting `setup` and `version`/`--version`.
- **`cmd/setup.go`** (modified) — the new relocation step, and the Windows branch
  now calls the real registry writer instead of only printing instructions.
- **`internal/shell/shell.go`** (modified) — `ExportLines`/`posixExportLines`/the
  Fish and PowerShell cases all gain the second (`$DEV_HOME`) PATH entry.
- **`internal/shell/windows_env.go`** (new, `//go:build windows`) — the real registry
  read/merge/write implementation using `golang.org/x/sys/windows/registry`.
- **`internal/shell/windows_env_other.go`** (new, `//go:build !windows`) — a stub so
  the rest of the codebase can call the same function on any OS; only ever actually
  invoked when `runtime.GOOS == "windows"`, so the stub's body is unreachable in
  practice, but it must exist for the code to compile on Linux/macOS.
- **`.github/workflows/ci.yml`** (modified) — add a `windows-latest` job so the
  Windows-specific code at least builds and its OS-agnostic tests run in real CI
  going forward (see "Testing approach" below for why this project's own sandbox
  can't verify Windows runtime behavior).
- **`go.mod`** (modified) — `golang.org/x/sys` moves from an indirect to a direct
  dependency (it's already present transitively at v0.46.0; no new module is added).

## `internal/platform/installed.go` — the location check

```go
// RunningFromDevHome reports whether the currently-executing binary's
// directory is exactly devHome or exactly devHome's "bin" subdirectory
// (where shims live). Comparison is case-insensitive on Windows (where
// paths are case-insensitive) and case-sensitive elsewhere.
func RunningFromDevHome() (ok bool, devHome string, err error)
```

Implementation: `os.Executable()` → `filepath.Dir(...)`, compared (via `filepath.Clean`
on both sides, and `strings.EqualFold` on Windows / `==` elsewhere) against
`DevHome()` and `filepath.Join(DevHome(), "bin")`. `os.Executable()` is already the
exact mechanism `installShims` trusts today for the same purpose (locating "this
binary" to copy) — reusing it here is consistent, not a new trust assumption.

## Dispatch gating

**`main.go`** (shim path): right before the existing `shim.Run(name, os.Args[1:])`
call, check `RunningFromDevHome()`. If false, print the same warning message (see
below) to stderr and exit 1 — never call `shim.Run`. Shims have no `setup` or
`version` concept of their own to exempt; the check is unconditional for them.

**`cmd/root.go`**: `PersistentPreRun` becomes `PersistentPreRunE`. After the existing
`cliutil.SetVerbose(verboseFlag)` call, if `cmd.Name() != "setup"` and
`cmd.Name() != "version"` and the `--version` flag isn't set, check
`RunningFromDevHome()`; if false, return an error carrying the warning message
(surfaced the same way every other command error already is, via `Execute()`'s
existing `cliutil.PrintError` + `os.Exit(1)`).

**Warning message** (shared constant, used by both call sites):

```
dev isn't installed yet — this looks like a copy running from outside $DEV_HOME.
Run `dev setup` to install it (creates $DEV_HOME, sets up shims, and configures
your PATH), then run this command again.
```

## `dev setup`'s new relocation step

After the existing scaffold + shim creation (unchanged), and only when
`RunningFromDevHome()` is currently false (i.e. this is a fresh, not-yet-installed
copy — re-running `setup` on an already-installed copy skips this step entirely,
since there's nothing to relocate):

1. Prompt: `Move dev, README.md, and LICENSE into $DEV_HOME now? [y/N]` (same
   `shell.Confirm` helper already used elsewhere).
2. If confirmed: copy the running executable (`os.Executable()`) into
   `$DEV_HOME/dev` (`.exe` suffix on Windows) via the existing `copyExecutable`
   helper. Then copy `README.md` and `LICENSE` — resolved relative to the
   executable's own directory, since that's where a real release archive's flat
   layout places them — into `$DEV_HOME`, tolerating either file being absent
   (`os.IsNotExist`) without failing the whole step, since a from-source `go build`
   binary has no such siblings.
3. After copying, best-effort `os.Remove` each original (the executable, and
   whichever of `README.md`/`LICENSE` were actually found and copied) — a failure
   here is logged as a step (`cliutil.Step`), not a hard error: a stray leftover
   copy outside `$DEV_HOME` is exactly what the location check now guards against if
   anyone ever runs it again.
4. If declined: proceed to the PATH-configuration step below anyway (same pattern
   as the existing shim-creation confirmation — declining one step doesn't abort
   the whole command), but the printed PATH lines still reference `$DEV_HOME` as the
   `dev`-itself location, which will be wrong until the user relocates it themselves
   or re-runs `setup` and confirms. This tradeoff is accepted: forcing the
   relocation would remove the user's ability to decline, which every other
   filesystem-mutating step in this command already allows.

## PATH: two entries everywhere

`shell.ExportLines` and its Fish/POSIX/PowerShell branches all change from one PATH
entry to two. POSIX (bash/zsh/Unknown-fallback):

```sh
export DEV_HOME='/home/user/.dev'
export PATH="$DEV_HOME:$DEV_HOME/bin:$PATH"
```

Fish: `set -gx PATH $DEV_HOME $DEV_HOME/bin $PATH`. PowerShell (both the real
registry write and the printed fallback text, so they stay textually consistent):
`$env:PATH = "$env:DEV_HOME;$env:DEV_HOME\bin;$env:PATH"`.

## Windows: real registry-based env configuration

`internal/shell/windows_env.go` (`//go:build windows`), using
`golang.org/x/sys/windows/registry` (already an indirect dependency at v0.46.0 —
this change makes it direct, no new module):

```go
// ConfigureWindowsUserEnv sets DEV_HOME and merges devHome/devHome\bin into
// the current user's PATH (HKEY_CURRENT_USER\Environment — never
// HKEY_LOCAL_MACHINE), then best-effort broadcasts WM_SETTINGCHANGE so
// already-running processes notice without a reboot. Idempotent: entries
// already present in PATH are not duplicated.
func ConfigureWindowsUserEnv(devHome string) error
```

Steps: open `registry.CURRENT_USER, \Environment` with
`registry.QUERY_VALUE|registry.SET_VALUE`; read the existing `Path` string value
(tolerating `registry.ErrNotExist` as empty); split on `;`, check whether `devHome`
and `devHome\bin` are already present (case-insensitive), prepend whichever are
missing; `SetStringValue` the merged `Path` and the plain `DEV_HOME` value; close
the key; broadcast `WM_SETTINGCHANGE` via a `user32.dll` `SendMessageTimeoutW` call
declared through `syscall.NewLazyDLL`/`NewProc` (not present in `x/sys/windows`'s
existing bindings, so declared locally) — a failure to broadcast is logged, not
fatal, since a new terminal session picks up the registry change on its own
regardless.

`internal/shell/windows_env_other.go` (`//go:build !windows`) provides the same
function signature returning an error stating it's Windows-only, so the rest of the
codebase compiles everywhere; it is never actually called outside a
`runtime.GOOS == "windows"` branch.

`cmd/setup.go`'s unsupported-shell branch, when `runtime.GOOS == "windows"`
specifically (distinguishing it from the genuine `Unknown`-shell case on
Linux/macOS, which still only gets the manual-instructions fallback — there's
nothing to automate there since the shell itself couldn't be identified), now:
prints the lines as before, then prompts `Add these to your user environment now?
[y/N]`, and on confirmation calls `shell.ConfigureWindowsUserEnv(devHome)` instead
of just returning. This replaces `RCPath`'s current blanket "PowerShell is
unsupported" framing for the *environment-variable* half of setup — the rc-file
auto-edit concept genuinely still doesn't apply to PowerShell (there's no
equivalent single profile file to safely guess), but environment variables
themselves are no longer manual there.

## Testing approach

Everything in "Dispatch gating," "relocation step," and the two-PATH-entry change is
pure Go, fully testable on this Linux development environment via the same patterns
already established (temp `$DEV_HOME`, `os.Executable()` pointing at the real test
binary, `t.Setenv`). The Windows registry code is the one piece that **cannot** be
runtime-tested here: it can be cross-compiled (`GOOS=windows go build ./...`) to
prove it type-checks and links, but `registry.CURRENT_USER` and `SendMessageTimeoutW`
have no meaning off real Windows. The plan's tasks make this limitation explicit
rather than silently skipping coverage: a cross-compile check stands in as the
automated verification for that one file, and a manual checklist is handed to the
user to run on their own Windows machine once implemented. `.github/workflows/ci.yml`
gains a `windows-latest` job (build + the OS-agnostic parts of the suite) so this
gap narrows for future changes, even though it can't retroactively verify this one
before merge.

## Acceptance criteria

```bash
# From outside $DEV_HOME (a fresh extraction):
./dev --version        # works
./dev lang list         # refuses, prints the "not installed" warning
./dev setup              # works, offers relocation + shim + PATH config

# After confirming setup's relocation step:
$DEV_HOME/dev --version  # works, identical output
$DEV_HOME/dev lang list  # works normally now
$DEV_HOME/bin/node --version  # (once a version is installed/activated) works

# Windows only, manually verified on a real machine:
# dev setup's confirmation writes HKCU\Environment's DEV_HOME and Path,
# a brand-new PowerShell window has `dev`/shims on PATH with no manual step.
```
