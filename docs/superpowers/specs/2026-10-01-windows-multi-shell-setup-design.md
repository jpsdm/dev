# Multi-Shell Setup — Design Spec

Date: 2026-10-01
Status: Approved for planning

## Context

`docs/superpowers/specs/2026-10-01-shell-function-path-management-design.md` (this same day,
earlier) replaced `dev`'s shim mechanism with a shell function that re-evals `dev env` to
recompute `PATH` in the current shell. Its stated goal was "works on Bash, Zsh, Fish, and
PowerShell" — the same four shells `dev setup` already detects. In practice, `internal/shell`'s
`Detect()` has always hardcoded `PowerShell` for the entire `windows` GOOS branch, with its own
doc comment noting this was "this project's only supported Windows shell target for now." And
`cmd/setup.go` only ever configures the one shell `Detect()` currently resolves to — never any
other shell also installed on the machine.

A real user installed `dev` on Windows (via `dev setup` from a PowerShell session, which
configured the PowerShell profile as designed) and then opened Git Bash — and found neither
`dev`'s own commands nor any active language version's tools recognized there. Root cause:
nothing in the current implementation ever detects Bash on Windows or writes to its `.bashrc`,
and even if it had, `setup` only ever touches the one currently-detected shell.

`cmd.exe` was also raised, but is explicitly out of scope (see Non-goals) — it has no
function/eval mechanism at all, and the only way to make per-version `PATH` switching reach it
immediately would be a fixed, repointed directory (symlink/junction) on a static `PATH` entry —
exactly the indirection mechanism the shell-function design replaced and the user does not want
revisited.

Initial brainstorming scoped the fix to "Windows gets both PowerShell and Bash configured." That
undersells the actual shape of the problem: Windows isn't special here, it's just the OS with two
candidate shells at once instead of one. A Linux user with both `bash` and `zsh` installed has
exactly the same gap today (`dev setup` only ever configures whichever one invoked it). This spec
generalizes the fix to **every supported shell dev knows about, on every OS**, rather than special-
casing Windows — which also directly serves the stated goal of making it easy to support more
shells later.

## Goals

- `dev env`'s shell-appropriate output (`ExportLines`) is correct when invoked from Git Bash on
  Windows, not just from PowerShell — i.e. `Detect()` must distinguish the two there instead of
  assuming PowerShell unconditionally.
- `dev setup`, run once on any OS, configures every shell this project supports that is actually
  present on the machine at that time — not only whichever shell happened to invoke it. This is
  one uniform code path, not a Windows-specific branch: the same loop configures both Bash and
  Zsh on a Linux box with both installed, and both PowerShell and Bash on Windows.
- Re-running `dev setup` later, after installing a shell that wasn't present before (e.g. Git
  Bash added after an initial PowerShell-only setup, or Fish installed later on Linux),
  configures only what's new. An already-configured shell whose block is already up to date is
  left untouched and not re-prompted for.
- Adding a new shell dev can configure in the future costs: one `Shell` constant, its lookup
  binary name(s), and the per-shell cases `RCPath`/`FunctionLines` already branch on. No change
  to `cmd/setup.go`'s loop itself.

## Non-goals

- **`cmd.exe` support.** Discussed and explicitly deferred: no function/eval mechanism exists for
  it, and the only way to make `dev lang use` take effect there immediately is a
  symlink/junction-based static redirect — the exact mechanism this project's prior design spec
  removed. `cmd.exe` stays undocumented/unsupported, the same position most of this ecosystem
  takes (`nvm.sh`/`mise` don't run in plain `cmd.exe` either).
- **WSL.** A WSL session is a real Linux environment; a `dev` install there is a separate,
  already-working install under the existing Unix `Detect()` path. Nothing about this spec
  touches WSL.
- **Changing `FunctionLines`/`ExportLines`/`rcPathForHome`'s per-shell syntax.** Already correct
  for every currently-supported shell on any OS it can run on (POSIX syntax shared by Bash/Zsh,
  `.bashrc`/`.bash_profile` handled per-`goos` already, Fish/PowerShell have their own branches).
  Nothing here needs to change; the gap is purely in *detection* and *which shells `setup`
  considers*, not in what gets written to each one's file.
- **A generic plugin/registration mechanism for third-party shells.** "Easy to add a shell" means
  a small, fixed amount of code per shell in this package (as today), not a dynamically
  extensible registry — this project's scale doesn't call for that, and the four shells already
  named cover the realistic set for the foreseeable future.

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
to a new `parentCommNameWindows(pid)` (see §2). `shellFromName` gains recognition for
PowerShell's own process names:

```go
case "powershell", "pwsh":
	return PowerShell, true
```

(`pwsh` is PowerShell 7+, cross-platform; `powershell` is Windows PowerShell 5.1, Windows-only
but still the default there on many machines.) This also means a user running PowerShell Core on
Linux/macOS now gets correctly detected via the existing non-Windows parent-process/`$SHELL`
path — a side benefit of the shared `shellFromName`, not extra code.

This piece (and only this piece) is genuinely Windows-specific: there is no `/proc` and no
portable `ps` equivalent there, unlike Linux/macOS.

### 2. `parentCommNameWindows`: resolving a PID's process name on Windows

No new dependency: `golang.org/x/sys/windows` is already a direct dependency (used today by
`internal/shell/windows_env.go` for registry access) and provides
`CreateToolhelp32Snapshot`/`Process32First`/`Process32Next` for process enumeration — the
standard way to resolve a PID's executable name on Windows.

Lives in a new `internal/shell/windows_parent.go` (`//go:build windows`), paired with a
`internal/shell/windows_parent_other.go` (`//go:build !windows`) stub returning `("", false)` —
the same split `windows_env.go`/`windows_env_other.go` already establishes. Strips the `.exe`
suffix and lowercases before returning, so `shellFromName` sees the same bare-name shape
`parentCommNameLinux`/`parentCommNameDarwin` already produce (e.g. `"bash.exe"` → `"bash"`).

### 3. `internal/shell`: a lookup-binary-names table per shell

A small, unexported map (or switch) pairing each configurable `Shell` with the binary name(s) a
user's `PATH` would have if that shell is installed:

```go
var shellLookupNames = map[Shell][]string{
	Bash:       {"bash"},
	Zsh:        {"zsh"},
	Fish:       {"fish"},
	PowerShell: {"pwsh", "powershell"},
}
```

`Unknown` has no entry (not a concrete, configurable shell). This is the one place a future shell
addition touches beyond its own `RCPath`/`FunctionLines` cases.

### 4. `internal/shell.Configurable() []Shell` and `IsPresent(sh Shell) bool`

`Configurable()` returns `{Bash, Zsh, Fish, PowerShell}` in a fixed, deterministic order (for
predictable prompt ordering) — the set `cmd/setup.go` iterates.

`IsPresent(sh Shell) bool`: true when any of `shellLookupNames[sh]` resolves via
`exec.LookPath`, OR `sh` is what `Detect()` currently resolves to for this process (covers an
install not on `PATH` but demonstrably running right now — the same reasoning the Windows
`$SHELL`-after-parent-process fallback already uses). No OS branching here at all: `LookPath`
naturally returns "not found" for a shell that isn't installed on any OS, and naturally finds
`pwsh`/`bash` wherever they really are, Windows included.

### 5. `cmd/setup.go`: one generic multi-shell loop, same on every OS

Replaces today's single-shell flow. For every OS:

```
devHome := platform.DevHome()
for _, sh := range shell.Configurable() {
	if !shell.IsPresent(sh) {
		continue
	}
	path, supported, err := shell.RCPath(sh)
	if err != nil { return err }
	if !supported {
		continue // e.g. PowerShell found via $PROFILE lookup failing — nothing safe to auto-edit
	}
	lines := shell.FunctionLines(sh, devHome)
	if alreadyCurrent(path, lines) {
		continue // silent — this is what makes a later re-run only surface what's new
	}
	// print lines, ask "Add this to <path>? [y/N]", UpsertBlock if yes — independent
	// per target, so declining one doesn't block another
}
```

`alreadyCurrent(path, lines)` is a small new helper: reads `path` (treating "doesn't exist" as
"not current," not an error), and checks whether it already contains the exact
`blockBegin`...`lines`...`blockEnd` block `UpsertBlock` would write. This is what makes a second
`dev setup` run — after installing a shell that wasn't present before — surface only the new
one: everything already current is dropped from the list before anything is printed or asked.

If the filtered list ends up empty (everything already current, or nothing supported was
present), the rc-file section is skipped entirely and `setup` goes straight to
`relocateIfNeeded` and (Windows only) the existing registry-env-vars prompt — both unchanged.

`relocateIfNeeded` and the Windows registry-env step (`ConfigureWindowsUserEnv`, covering
`$DEV_HOME` itself for every Windows process including `cmd.exe`) are untouched — they're about
the binary's location and `$DEV_HOME`'s own static `PATH` entry, not about which shell(s) get the
function block.

## Error handling

- Parent-process lookup fails or names an unrecognized process (e.g. `dev setup` launched from
  Explorer, a VS Code task, `wt.exe` itself, on Windows): falls through to the `$SHELL` check,
  then to the `PowerShell` default — identical to today's unconditional behavior in that case,
  never worse.
- `exec.LookPath` erroring for a given shell just means "not present" — `setup` silently excludes
  it, same as it does today for a shell nobody has installed.
- A target already mid-edit by the user (an unterminated `blockBegin`/`blockEnd` pair) still
  surfaces `UpsertBlock`'s existing repair-it-by-hand error, per-target — one broken file doesn't
  prevent another target from being offered.

## Testing approach

Real behavior, no mocks, matching this project's convention:

- `shellFromName`'s new `"powershell"`/`"pwsh"` cases: pure table test, runs on every OS.
- `Detect()`'s new Windows branch ordering (parent signal → `$SHELL` → `PowerShell` default):
  exercised via the existing `parentShellDetector` test seam (already used to force deterministic
  answers today), runs on every OS since it's pure logic once the signal is injected.
- `parentCommNameWindows` itself: only buildable and runnable on Windows, so this is a
  Windows-only test (guarded the same way `TestDetect_UsesShellEnvVar` already guards its
  POSIX-only assumptions, just inverted) — spawn a real child process with a known name via
  `os/exec`, resolve its PID, and assert the resolved name matches. Runs for real in CI's
  `windows-latest` job (GitHub's Windows runners ship Git for Windows, so a real `bash.exe` is
  available there to test the Bash-recognition path specifically, not just a generic lookup).
- `IsPresent`: real `exec.LookPath` against whatever shells actually exist on the test runner —
  at minimum `bash` is always present on every CI runner (Linux, macOS, and Windows all ship Git
  Bash or an equivalent), giving a real positive case everywhere; a made-up shell name is not a
  valid `Shell` value, so the "absent" case is instead covered by asserting a shell's absence
  from the real environment where that's actually true (e.g. `Fish` on the Linux/macOS/Windows CI
  images, none of which ship it by default).
- `alreadyCurrent`: real temp files — one pre-populated with the exact block (expect skip), one
  with a stale/different block (expect not-skip), one missing entirely (expect not-skip).
- `cmd/setup.go`'s filtered loop: real temp `$DEV_HOME`/rc-file paths, asserting the prompt/write
  sequence only covers the expected subset — mirrors this project's existing `setup_test.go`
  style.
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

```bash
# Linux/macOS, both bash and zsh installed
dev setup                        # offers BOTH .bashrc and .zshrc in one run
```
