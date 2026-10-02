# Windows Multi-Shell Setup — Design Spec

Date: 2026-10-01
Status: Approved for planning

## Context

`docs/superpowers/specs/2026-10-01-shell-function-path-management-design.md` (this same day,
earlier) replaced `dev`'s shim mechanism with a shell function that re-evals `dev env` to
recompute `PATH` in the current shell. Its stated goal was "works on Bash, Zsh, Fish, and
PowerShell" — the same four shells `dev setup` already detects. In practice, `internal/shell`'s
`Detect()` has always hardcoded `PowerShell` for the entire `windows` GOOS branch, with its own
doc comment noting this was "this project's only supported Windows shell target for now."

A real user installed `dev` on Windows (via `dev setup` from a PowerShell session, which
configured the PowerShell profile as designed) and then opened Git Bash — and found neither
`dev`'s own commands nor any active language version's tools recognized there. Root cause:
nothing in the current implementation ever detects Bash on Windows or writes to its `.bashrc`.
`cmd.exe` was also raised, but is explicitly out of scope (see Non-goals) — it has no
function/eval mechanism at all, and the only way to make per-version `PATH` switching reach it
immediately would be a fixed, repointed directory (symlink/junction) on a static `PATH` entry —
exactly the indirection mechanism the shell-function design replaced and the user does not want
revisited.

## Goals

- `dev env`'s shell-appropriate output (`ExportLines`) is correct when invoked from Git Bash on
  Windows, not just from PowerShell — i.e. `Detect()` must distinguish the two there instead of
  assuming PowerShell unconditionally.
- `dev setup`, run once on Windows, configures every Windows shell actually present on the
  machine at that time (PowerShell always; Bash when Git Bash is installed) — not only whichever
  shell happened to invoke it.
- Re-running `dev setup` later, after installing a shell that wasn't present before (e.g. Git
  Bash added after an initial PowerShell-only setup), configures only what's new. An
  already-configured shell whose block is already up to date is left untouched and not
  re-prompted for.

## Non-goals

- **`cmd.exe` support.** Discussed and explicitly deferred: no function/eval mechanism exists for
  it, and the only way to make `dev lang use` take effect there immediately is a
  symlink/junction-based static redirect — the exact mechanism this project's prior design spec
  removed. `cmd.exe` stays undocumented/unsupported, the same position most of this ecosystem
  takes (`nvm.sh`/`mise` don't run in plain `cmd.exe` either).
- **WSL.** A WSL session is a real Linux environment; a `dev` install there is a separate,
  already-working install under the existing Unix `Detect()` path (parent-process detection
  already handles Bash/Zsh/Fish there). Nothing about this spec touches WSL.
- **Detecting or configuring Zsh/Fish on Windows.** Out of scope — this is specifically about the
  two shells actually in play (PowerShell, Git Bash). Nothing prevents them working the same way
  if a future need arises; just not built speculatively now.
- **Changing `FunctionLines`/`ExportLines`/`rcPathForHome`'s per-shell syntax.** Already correct
  for Bash on any OS (POSIX syntax, `.bashrc` path via `os.UserHomeDir()` — not gated by `goos`
  except the existing Bash/macOS `.bash_profile` exception). Nothing here needs to change; the
  gap is purely in *detection* and *which files `setup` touches*, not in what gets written to
  them.

## Design

### 1. `internal/shell.Detect()`: real parent-process detection on Windows

Today:

```go
func Detect() Shell {
	if runtime.GOOS == "windows" {
		return PowerShell
	}
	...
}
```

Becomes: on Windows, try the same parent-process signal Linux/macOS already use, then the
`$SHELL` env var (Git Bash sets this, typically to a path ending in `bash`), then fall back to
`PowerShell` — preserving today's behavior exactly when detection is inconclusive, so no existing
PowerShell-only install regresses:

```go
func Detect() Shell {
	if runtime.GOOS == "windows" {
		if sh, ok := parentShellDetector(); ok {
			return sh
		}
		if sh := shellFromEnv(os.Getenv("SHELL")); sh != Unknown {
			return sh
		}
		return PowerShell
	}
	if sh, ok := parentShellDetector(); ok {
		return sh
	}
	return shellFromEnv(os.Getenv("SHELL"))
}
```

`parentShellName`/`parentCommName`'s existing `switch goos` gains a `"windows"` case dispatching
to a new `parentCommNameWindows(pid)`.

`shellFromName` gains recognition for PowerShell's own process names:

```go
case "powershell", "pwsh":
	return PowerShell, true
```

(`pwsh` is PowerShell 7+; `powershell` is Windows PowerShell 5.1, still the default on many
machines.)

### 2. `parentCommNameWindows`: resolving a PID's process name on Windows

No new dependency: `golang.org/x/sys/windows` is already a direct dependency (used today by
`internal/shell/windows_env.go` for registry access) and provides
`CreateToolhelp32Snapshot`/`Process32First`/`Process32Next` for process enumeration — the
standard way to resolve a PID's executable name on Windows (no `/proc`, no portable `ps`
equivalent).

Lives in a new `internal/shell/windows_parent.go` (`//go:build windows`), paired with a
`internal/shell/windows_parent_other.go` (`//go:build !windows`) stub returning `("", false")` —
the same split `windows_env.go`/`windows_env_other.go` already establishes. Strips the `.exe`
suffix and lowercases before returning, so `shellFromName` sees the same bare-name shape
`parentCommNameLinux`/`parentCommNameDarwin` already produce (e.g. `"bash.exe"` → `"bash"`).

### 3. `cmd/setup.go`: configure every present Windows shell, skip what's already current

Today, `setupCmd` detects one shell, computes one `(path, lines)` pair, and prompts once. On
Windows this becomes: build the list of **present** Windows shell targets, filter out any whose
file already contains the exact up-to-date block, and only prompt for/write what's left.

**Presence:**
- PowerShell: always present (it ships with Windows).
- Bash: present when `exec.LookPath("bash")` succeeds, OR the detected shell for this invocation
  (`shell.Detect()`) is already `Bash` (covers the case where `bash` somehow isn't the name
  `LookPath` finds but we're demonstrably running inside it right now).

**Already-current check:** a small helper reads the target file (if it exists) and checks
whether it already contains the exact `blockBegin...blockEnd` content `UpsertBlock` would write —
if so, that target is dropped from the list silently (no prompt, no mention, no write). This is
what makes a second `dev setup` run, after installing Bash later, surface only the new Bash
block: PowerShell's is already current and never comes up again.

**Flow change:** the non-Windows path is untouched (still exactly one shell, detected, same as
today). On Windows, after the existing `relocateIfNeeded` step, iterate the filtered present-and-
outdated target list; for each, print its block, ask its own "Add this to `<path>`? [y/N]"
confirmation, and `UpsertBlock` it independently — declining one doesn't block the other. If the
filtered list is empty (everything already current), skip straight to the existing
registry-env-vars prompt with no rc-file section printed at all.

The existing unconditional Windows registry-env step (`ConfigureWindowsUserEnv`, covering
`$DEV_HOME` itself for every Windows process including `cmd.exe`) is untouched.

## Error handling

- Parent-process lookup fails or names an unrecognized process (e.g. `dev setup` launched from
  Explorer, a VS Code task, `wt.exe` itself): falls through to the `$SHELL` check, then to the
  `PowerShell` default — identical to today's unconditional behavior in that case, never worse.
- `exec.LookPath("bash")` erroring is "not present," not a failure — `dev setup` proceeds with
  just PowerShell, same as it does today.
- A target already mid-edit by the user (an unterminated `blockBegin`/`blockEnd` pair) still
  surfaces `UpsertBlock`'s existing repair-it-by-hand error, per-target — one broken file doesn't
  prevent the other target from being offered.

## Testing approach

Real behavior, no mocks, matching this project's convention:

- `shellFromName`'s new `"powershell"`/`"pwsh"` cases: pure table test, runs on every OS.
- `Detect()`'s new Windows branch ordering (parent signal → `$SHELL` → `PowerShell` default):
  exercised via the existing `parentShellDetector` test seam (already used to force deterministic
  answers today), runs on every OS since it's pure logic once the signal is injected.
- `parentCommNameWindows` itself: only buildable and runnable on Windows, so this is a
  Windows-only test (guarded the same way `TestDetect_UsesShellEnvVar` already guards its
  POSIX-only assumptions, just inverted) — spawn a real child process with a known name (e.g.
  `cmd.exe /c pause`-equivalent, or a short-lived `bash.exe` if available in CI) via `os/exec`,
  resolve its PID, and assert the resolved name matches. Runs for real in CI's `windows-latest`
  job (GitHub's Windows runners ship Git for Windows, so a real `bash.exe` is available there to
  test the Bash-recognition path specifically, not just the generic lookup).
- `cmd/setup.go`'s present-and-filter logic: unit-testable directly against real temp files (an
  already-current rc file, a stale/missing one, `LookPath` substituted via the same kind of
  package-level var seam `platform.Executable`/`parentShellDetector` already use for testability)
  — assert the filtered list excludes an already-current target and includes a stale/missing one.
- Manual verification note: this project's dev/CI environment is Linux-only for interactive
  testing; the Windows-specific process-enumeration code and the real Git-Bash-after-PowerShell
  setup flow should be spot-checked once on a real Windows host with Git for Windows installed,
  the same honesty disclaimer the shell-function-path-management spec already gave for
  PowerShell's own function syntax.

## Acceptance criteria

```powershell
# First run, PowerShell only, Git Bash not yet installed
dev setup                        # offers PowerShell profile only
. $PROFILE
dev lang use node 22
node --version                   # works in PowerShell

# Later: install Git Bash, then
dev setup                        # PowerShell: already current, not re-prompted
                                  # Bash: offered for the first time
```

```bash
# In Git Bash, after the second dev setup run above
source ~/.bashrc
dev lang use node 22              # takes effect in this Git Bash session
node --version                    # works in Git Bash too, no restart needed
```
