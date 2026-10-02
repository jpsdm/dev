# Multi-Shell Setup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `dev setup` detects and configures every shell dev supports that's actually installed on the machine — not just whichever shell invoked it — on every OS, and a later re-run only offers what's newly present.

**Architecture:** `internal/shell` gains a small presence-detection layer (`Configurable()`, `IsPresent()`, an overridable `LookPath` seam, `BlockUpToDate()`) and real Windows parent-process detection (`Detect()` no longer hardcodes PowerShell there). `cmd/setup.go`'s single-shell flow becomes a loop over every present, auto-editable shell, each prompted and written independently, falling back to today's manual-instructions behavior only when nothing at all is present.

**Tech Stack:** Go 1.25, Cobra, `golang.org/x/sys/windows` (already a direct dependency — no new one added).

**Spec:** `docs/superpowers/specs/2026-10-01-windows-multi-shell-setup-design.md`

## Global Constraints

- No new third-party dependency — `golang.org/x/sys/windows` is already a direct `go.mod` requirement (used today by `internal/shell/windows_env.go`); every new Windows-only API call in this plan comes from that same package.
- `cmd.exe` support is explicitly out of scope (see spec's Non-goals) — do not add any cmd.exe-specific code path.
- Every externally-sourced identifier that becomes part of a filesystem path must go through the project's existing path-safety gate (`internal/runtime.ValidVersionName`) — not applicable to any task in this plan (no new path ever comes from a network response, git tag, or marker file; every path here comes from `platform.DevHome()`, `os.UserHomeDir()`, or a fixed rc-file basename, all already-trusted inputs the existing code already uses the same way).
- Windows-only code lives in a `//go:build windows` file paired with a `//go:build !windows` stub, following the existing `internal/shell/windows_env.go` / `windows_env_other.go` split exactly.
- `gofmt -l .` must report nothing, `go vet ./...` and `golangci-lint run` (unused, staticcheck, ineffassign, errcheck) must be clean, and `go test ./...` must pass — this is `make check`, and it's the merge gate (`ci.yml` runs it on `ubuntu-latest` and separately runs `go vet`/`go test`/`go build` on `windows-latest`).
- Tests exercise real behavior, not mocks: real temp files/directories, a real `exec.Command` child process for the Windows-only process-lookup test, package-level var seams (`shell.LookPath`, `parentShellDetector`) only for genuine OS/hardware boundaries `go test` itself can't otherwise control — the same pattern `platform.Executable` and `parentShellDetector` already establish.
- Commit messages follow Conventional Commits (`feat`, `fix`, `test`, `docs`, ...); this repo requires a PR via the `open-pr` skill, never a direct push to `main`.

## Review Focus

- **A present shell's `RCPath` call returning a genuine error** (not just `supported=false`) must abort the whole command immediately, the same as before this change — pinned by Task 4's `TestSetupCommand_RCPathErrorAbortsImmediately` (unresolvable `$HOME`), confirming the loop's `return err` on that path isn't accidentally caught by the newer per-target "report and continue" handling meant only for `BlockUpToDate`/`UpsertBlock` failures.
- **A shell that's "present" via `LookPath` but whose `RCPath` reports `supported=false`** (e.g. `pwsh` resolves on PATH, but the real `$PROFILE` subprocess query fails — a locked-down execution policy, say, or simply no real `pwsh`/`powershell` binary behind a faked `LookPath` in a test) must NOT count toward `present`, so the "nothing configurable" manual-fallback branch still correctly triggers when that was the only candidate — pinned by Task 4's `TestSetupCommand_PresentButRCPathUnsupportedDoesNotCountAsPresent`, a different code path from "nothing resolves via `LookPath` at all."
- **One target's `UpsertBlock` failure must not stop a different, healthy target from still being offered and written in the same run** (the spec's explicit per-target error-handling requirement) — Task 4 fixes a real instance of this being violated by a straightforward first draft (an immediate `return err` on the first write failure), pinned by `TestSetupCommand_OneCorruptedTargetDoesNotBlockTheOthers`.
- **`IsPresent`'s `Detect() == sh` fallback must never make an absent shell look present just because it happens to be `go test`'s own inconclusive-parent-process default** — on this project's Linux dev/CI machines, `Detect()` with no parent/`$SHELL` signal falls back to `Unknown` (not, say, `Bash`), so this can't silently inflate presence; pinned by Task 3's `TestIsPresent_FalseWhenNoLookupNameResolvesAndNotTheDetectedShell`.
- **`BlockUpToDate` must report `false` (not error) for a file that doesn't exist yet**, since that's the ordinary "never configured this shell before" case on every first run, not a failure — distinct from a real read error (permission denied, say), which must propagate so the loop's per-target error handling can report it rather than silently treating a permissions problem as "nothing to do here." Pinned by Task 3's `TestBlockUpToDate_FalseForAMissingFile`; the permission-error-propagates half isn't separately tested (Go's `os.IsNotExist` branch is the only one worth distinguishing here, and every other `os.ReadFile` error already propagates via the function's plain `return false, fmt.Errorf(...)` path) — a reviewer should still confirm no later change collapses that branch back into "always false."

---

### Task 1: `internal/shell.Detect()` — generalize the Windows fallback and recognize PowerShell by process name

**Files:**
- Modify: `internal/shell/shell.go:35-90` (the `Detect` function, its doc comment, and `shellFromName`)
- Test: `internal/shell/shell_test.go` (modify four existing `Detect()` tests, add one new one, extend `TestShellFromName_MapsKnownShellNames`)

**Interfaces:**
- Consumes: nothing new — `parentShellDetector` (existing package var), `shellFromEnv`, `os.Getenv`, `runtime.GOOS` (all already in this file).
- Produces: `Detect()`'s new fallback ordering (parent process → `$SHELL` → `PowerShell` only on Windows → `Unknown`) and `shellFromName`'s new `"powershell"`/`"pwsh"` recognition — both consumed directly by Task 2 (parent-process detection feeds into this same `Detect()`/`shellFromName` pair) and Task 3 (`IsPresent`'s `Detect() == sh` fallback).

- [ ] **Step 1: Write the failing/updated tests**

In `internal/shell/shell_test.go`, remove the `runtime.GOOS == "windows"` skip from these three tests (they now behave identically on every OS, since the Windows-specific branch only matters when every other signal is inconclusive):

```go
func TestDetect_UsesShellEnvVar(t *testing.T) {
	withNoParentShellSignal(t)
	t.Setenv("SHELL", "/bin/zsh")
	if got := Detect(); got != Zsh {
		t.Errorf("Detect() = %v, want Zsh", got)
	}
}
```

```go
func TestDetect_PrefersParentProcessOverAStaleShellEnvVar(t *testing.T) {
	// The exact real-world bug this whole change fixes: $SHELL (the
	// configured *login* shell) says bash, but the process that
	// actually launched this invocation — resolved independently of
	// $SHELL — is fish. Detect() must trust the parent process, not
	// the stale env var, or dev writes bash syntax into a file fish
	// can't source.
	orig := parentShellDetector
	parentShellDetector = func() (Shell, bool) { return Fish, true }
	t.Cleanup(func() { parentShellDetector = orig })
	t.Setenv("SHELL", "/bin/bash")

	if got := Detect(); got != Fish {
		t.Errorf("Detect() = %v, want Fish (from the parent process, not $SHELL)", got)
	}
}
```

```go
func TestDetect_FallsBackToShellEnvVarWhenParentProcessIsInconclusive(t *testing.T) {
	// An inconclusive parent-process lookup (unsupported platform, a
	// permission error, a parent that isn't a known shell at all —
	// e.g. dev invoked from a Makefile or another program) must not
	// be treated as "no shell" — it falls back to the existing
	// $SHELL-based behavior exactly as before this change.
	withNoParentShellSignal(t)
	t.Setenv("SHELL", "/bin/zsh")

	if got := Detect(); got != Zsh {
		t.Errorf("Detect() = %v, want Zsh (from the $SHELL fallback)", got)
	}
}
```

Keep `TestDetect_UnknownShellFallsBackToUnknown`'s Windows skip, but update its rationale (Windows now has a *different* correct answer, not "always PowerShell"):

```go
func TestDetect_UnknownShellFallsBackToUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on Windows, an unrecognized/inconclusive shell signal falls back to PowerShell, not Unknown — see TestDetect_WindowsDefaultsToPowerShellWhenNothingConclusive")
	}
	withNoParentShellSignal(t)
	t.Setenv("SHELL", "/bin/tcsh")
	if got := Detect(); got != Unknown {
		t.Errorf("Detect() = %v, want Unknown", got)
	}
}
```

Add the new Windows-only counterpart right after it:

```go
func TestDetect_WindowsDefaultsToPowerShellWhenNothingConclusive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("exercises Detect()'s Windows-only final fallback")
	}
	withNoParentShellSignal(t)
	t.Setenv("SHELL", "/bin/tcsh")
	if got := Detect(); got != PowerShell {
		t.Errorf("Detect() = %v, want PowerShell (Windows's final fallback when neither signal is conclusive)", got)
	}
}
```

Extend `TestShellFromName_MapsKnownShellNames`'s cases map:

```go
func TestShellFromName_MapsKnownShellNames(t *testing.T) {
	t.Parallel()
	cases := map[string]Shell{
		"bash":       Bash,
		"zsh":        Zsh,
		"fish":       Fish,
		"powershell": PowerShell,
		"pwsh":       PowerShell,
	}
	for name, want := range cases {
		if got, ok := shellFromName(name); !ok || got != want {
			t.Errorf("shellFromName(%q) = (%v, %v), want (%v, true)", name, got, ok, want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify the new/changed ones fail**

Run: `go test ./internal/shell/... -run 'TestDetect_|TestShellFromName_MapsKnownShellNames' -v`
Expected: `TestDetect_WindowsDefaultsToPowerShellWhenNothingConclusive` fails (skipped on non-Windows, so on this Linux dev machine it reports `SKIP` — if developing on Windows, confirm it fails there first); `TestShellFromName_MapsKnownShellNames` fails on the `"powershell"`/`"pwsh"` cases with `ok = false`.

- [ ] **Step 3: Implement**

Replace `Detect`'s doc comment and body in `internal/shell/shell.go`:

```go
// Detect identifies the current shell. It prefers the actual process
// that launched this invocation of dev (see parentShellDetector) over
// $SHELL, which is only the user's configured *login* shell — a value
// that's often stale or simply wrong for anyone who runs a different
// shell day to day (they switched via `chsh` but the record wasn't
// updated, their terminal profile launches a shell directly regardless
// of the login shell, etc.). A real, running shell process is strictly
// better evidence of "what shell is actually asking" than an inherited
// env var; $SHELL remains the fallback when the parent process can't
// be determined or isn't a shell this project recognizes. On Windows,
// where neither signal is conclusive (e.g. dev launched from Explorer,
// a VS Code task, or Windows Terminal itself rather than a shell
// directly), the final fallback is PowerShell — it ships with every
// Windows install, unlike Bash.
func Detect() Shell {
	if sh, ok := parentShellDetector(); ok {
		return sh
	}
	if sh := shellFromEnv(os.Getenv("SHELL")); sh != Unknown {
		return sh
	}
	if runtime.GOOS == "windows" {
		return PowerShell
	}
	return Unknown
}
```

Add the two new cases to `shellFromName`'s switch (right after the existing `"fish"` case):

```go
	case "powershell", "pwsh":
		// "pwsh" is PowerShell 7+ (cross-platform); "powershell" is
		// Windows PowerShell 5.1, Windows-only but still the default
		// there on many machines.
		return PowerShell, true
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/shell/... -v 2>&1 | tail -60`
Expected: PASS for every test in the package (the whole package, not just the ones touched — this file's change affects `Detect()`'s behavior broadly).

- [ ] **Step 5: `gofmt`/`vet`/lint**

Run: `gofmt -l internal/shell/shell.go internal/shell/shell_test.go && go vet ./internal/shell/... && golangci-lint run ./internal/shell/...`
Expected: no `gofmt` output, no vet/lint issues.

- [ ] **Step 6: Commit**

```bash
git add internal/shell/shell.go internal/shell/shell_test.go
git commit -m "$(cat <<'EOF'
feat(shell): generalize Detect()'s Windows fallback and recognize PowerShell by name

Detect() hardcoded PowerShell unconditionally on Windows, so dev env
printed PowerShell syntax even when actually invoked from Git Bash
there. It now tries the same parent-process/$SHELL signals Linux and
macOS already use first, falling back to PowerShell only when neither
is conclusive.
EOF
)"
```

---

### Task 2: Windows parent-process shell detection

**Files:**
- Create: `internal/shell/windows_parent.go` (`//go:build windows`)
- Create: `internal/shell/windows_parent_other.go` (`//go:build !windows`)
- Create: `internal/shell/windows_parent_test.go` (`//go:build windows`)
- Modify: `internal/shell/shell.go:113-122` (`parentCommName`'s switch)

**Interfaces:**
- Consumes: `shellFromName` (Task 1) — not called directly by this task's own code, but `parentCommNameWindows`'s return value feeds into `parentShellName`, which already calls `shellFromName` on whatever `parentCommName` returns.
- Produces: `parentCommNameWindows(pid int) (string, bool)`, wired into `parentCommName`'s existing `switch goos` — consumed only internally by this package's own `parentShellName`/`Detect()`; no other task calls it directly.

- [ ] **Step 1: Write the failing tests**

Create `internal/shell/windows_parent_test.go`:

```go
//go:build windows

package shell

import (
	"os/exec"
	"testing"
)

func TestParentCommName_Windows_EndToEndWithARealProcess(t *testing.T) {
	// cmd.exe is present on every Windows install; "ping" keeps the
	// child alive briefly without needing a real console (unlike
	// "timeout", which fails under a redirected/closed stdin).
	cmd := exec.Command("cmd.exe", "/c", "ping", "-n", "6", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting cmd.exe subprocess: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	name, ok := parentCommName("windows", cmd.Process.Pid)
	if !ok {
		t.Fatal("parentCommName() ok = false for a real, running process")
	}
	if name != "cmd" {
		t.Errorf("parentCommName() = %q, want %q", name, "cmd")
	}
}

func TestParentCommName_Windows_NonexistentPIDReturnsFalse(t *testing.T) {
	if _, ok := parentCommName("windows", 999999); ok {
		t.Error("parentCommName() ok = true for a PID that almost certainly doesn't exist")
	}
}

func TestParseWindowsExeName_StripsExtensionAndLowercases(t *testing.T) {
	t.Parallel()
	if got := parseWindowsExeName("Bash.EXE"); got != "bash" {
		t.Errorf("parseWindowsExeName(%q) = %q, want %q", "Bash.EXE", got, "bash")
	}
}
```

This test file can't run on this project's Linux dev/CI environment — it only compiles and executes on the `windows-latest` CI job (`ci.yml`'s `windows` job already runs `go test ./...`). Note this plainly; don't claim local verification you can't actually perform, matching the spec's own disclaimer for PowerShell syntax.

- [ ] **Step 2: Confirm it doesn't compile yet (there's nothing to run it against)**

Run: `GOOS=windows GOARCH=amd64 go vet ./internal/shell/...`
Expected: FAIL — `undefined: parentCommNameWindows`, `undefined: parseWindowsExeName` (the implementation files don't exist yet).

- [ ] **Step 3: Implement**

Create `internal/shell/windows_parent.go`:

```go
//go:build windows

package shell

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// parentCommNameWindows resolves pid's process executable name via a
// process snapshot (CreateToolhelp32Snapshot) — Windows has no /proc
// filesystem and no portable `ps` equivalent, unlike
// parentCommNameLinux/parentCommNameDarwin. Strips the ".exe" suffix
// and lowercases the result so shellFromName sees the same bare-name
// shape the other two platforms' lookups already produce (e.g.
// "bash.exe" -> "bash").
func parentCommNameWindows(pid int) (string, bool) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return "", false
	}
	for {
		if entry.ProcessID == uint32(pid) {
			return parseWindowsExeName(windows.UTF16ToString(entry.ExeFile[:])), true
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return "", false
		}
	}
}

// parseWindowsExeName reduces a raw ProcessEntry32.ExeFile value (e.g.
// "bash.exe", sometimes with different casing) to the same bare,
// lowercase, extension-free shape shellFromName expects.
func parseWindowsExeName(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	return strings.TrimSuffix(name, ".exe")
}
```

Create `internal/shell/windows_parent_other.go`:

```go
//go:build !windows

package shell

// parentCommNameWindows's non-Windows stub. Never actually called
// outside parentCommName's "windows" case — exists only so the rest of
// the codebase compiles on every OS.
func parentCommNameWindows(pid int) (string, bool) {
	return "", false
}
```

Modify `parentCommName`'s switch in `internal/shell/shell.go` to add the Windows case:

```go
	switch goos {
	case "linux":
		return parentCommNameLinux(pid)
	case "darwin":
		return parentCommNameDarwin(pid)
	case "windows":
		return parentCommNameWindows(pid)
	default:
		return "", false
	}
```

- [ ] **Step 4: Verify on Linux (compile + cross-compile) and run what can run**

Run: `go build ./... && go vet ./... && go test ./internal/shell/... -v 2>&1 | tail -40`
Expected: builds clean, every test still PASSes (the new Windows-only test file is excluded from this build entirely by its build tag).

Run: `GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=amd64 go vet ./... && GOOS=windows GOARCH=amd64 go test -c -o /tmp/shell_windows_test.exe ./internal/shell/...`
Expected: all three succeed — this is the furthest this task's Windows-specific code can be verified without a real Windows host; the actual `TestParentCommName_Windows_*`/`TestParseWindowsExeName_*` results only become known when CI's `windows-latest` job runs them for real.

- [ ] **Step 5: `gofmt`/lint**

Run: `gofmt -l internal/shell/ && golangci-lint run ./internal/shell/...`
Expected: no `gofmt` output, no lint issues. (`golangci-lint-action` in CI only runs on `ubuntu-latest`, so this Linux-side check is the full lint coverage this code gets before a human reviews the Windows CI run.)

- [ ] **Step 6: Commit**

```bash
git add internal/shell/windows_parent.go internal/shell/windows_parent_other.go internal/shell/windows_parent_test.go internal/shell/shell.go
git commit -m "$(cat <<'EOF'
feat(shell): resolve the parent process name on Windows

Windows has no /proc and no portable `ps` equivalent, so this is a
CreateToolhelp32Snapshot-based lookup (golang.org/x/sys/windows,
already a direct dependency) instead — the same role
parentCommNameLinux/parentCommNameDarwin already fill on their
platforms. Wires into parentCommName's existing dispatch, so Detect()
now distinguishes bash.exe from powershell.exe/pwsh.exe on Windows
instead of assuming PowerShell unconditionally.
EOF
)"
```

---

### Task 3: `Configurable`, `IsPresent`, and `BlockUpToDate`

**Files:**
- Modify: `internal/shell/shell.go` (add before `UpsertBlock`, currently at line 515)
- Test: `internal/shell/shell_test.go` (add new tests)

**Interfaces:**
- Consumes: `Detect()` (Task 1), `Shell`/`Bash`/`Zsh`/`Fish`/`PowerShell`/`Unknown` constants, `blockBegin`/`blockEnd` (existing package constants), `UpsertBlock` (existing).
- Produces: `Configurable() []Shell`, `LookPath` (package var, type `func(string) (string, error)`), `IsPresent(sh Shell) bool`, `BlockUpToDate(path string, lines []string) (bool, error)` — all four consumed directly by Task 4's `cmd/setup.go` loop and its tests (`shell.LookPath` is overridden from the `cmd` package's tests, so it must be exported).

- [ ] **Step 1: Write the failing tests**

Add to `internal/shell/shell_test.go` (near the `UpsertBlock` tests, before `TestConfirm_AcceptsYAndYes`):

```go
func TestConfigurable_ReturnsTheFourKnownShellsInAFixedOrder(t *testing.T) {
	t.Parallel()
	got := Configurable()
	want := []Shell{Bash, Zsh, Fish, PowerShell}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Configurable() = %v, want %v", got, want)
	}
}

// withLookPath overrides LookPath for the duration of the test so only
// the given names resolve, deterministically, regardless of what's
// actually installed on the machine running `go test`.
func withLookPath(t *testing.T, present ...string) {
	t.Helper()
	found := make(map[string]bool, len(present))
	for _, name := range present {
		found[name] = true
	}
	orig := LookPath
	LookPath = func(file string) (string, error) {
		if found[file] {
			return "/fake/" + file, nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { LookPath = orig })
}

func TestIsPresent_TrueWhenLookupNameResolves(t *testing.T) {
	withLookPath(t, "zsh")
	if !IsPresent(Zsh) {
		t.Error("IsPresent(Zsh) = false, want true when \"zsh\" resolves via LookPath")
	}
}

func TestIsPresent_FalseWhenNoLookupNameResolvesAndNotTheDetectedShell(t *testing.T) {
	withLookPath(t) // nothing resolves
	withNoParentShellSignal(t)
	t.Setenv("SHELL", "/bin/bash") // Detect() -> Bash, not Fish
	if IsPresent(Fish) {
		t.Error("IsPresent(Fish) = true, want false when fish isn't on PATH and isn't the detected shell")
	}
}

func TestIsPresent_TrueWhenItsTheCurrentlyDetectedShellEvenIfNotOnPath(t *testing.T) {
	withLookPath(t) // nothing resolves via LookPath
	orig := parentShellDetector
	parentShellDetector = func() (Shell, bool) { return Fish, true }
	t.Cleanup(func() { parentShellDetector = orig })

	if !IsPresent(Fish) {
		t.Error("IsPresent(Fish) = false, want true when Fish is the currently-detected shell, even with no lookupNames match")
	}
}

func TestIsPresent_PowerShellResolvesEitherBinaryName(t *testing.T) {
	withLookPath(t, "pwsh")
	if !IsPresent(PowerShell) {
		t.Error("IsPresent(PowerShell) = false, want true when only \"pwsh\" (not \"powershell\") resolves")
	}
}

func TestBlockUpToDate_FalseForAMissingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "rc")
	got, err := BlockUpToDate(path, []string{"export X=1"})
	if err != nil {
		t.Fatalf("BlockUpToDate() returned error: %v", err)
	}
	if got {
		t.Error("BlockUpToDate() = true for a file that doesn't exist, want false")
	}
}

func TestBlockUpToDate_TrueAfterUpsertBlockWroteTheSameLines(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "rc")
	lines := []string{"export DEV_HOME=/home/u/.dev", "export PATH=\"$DEV_HOME:$PATH\""}
	if err := UpsertBlock(path, lines); err != nil {
		t.Fatalf("UpsertBlock() returned error: %v", err)
	}
	got, err := BlockUpToDate(path, lines)
	if err != nil {
		t.Fatalf("BlockUpToDate() returned error: %v", err)
	}
	if !got {
		t.Error("BlockUpToDate() = false right after UpsertBlock wrote the exact same lines, want true")
	}
}

func TestBlockUpToDate_FalseWhenLinesDiffer(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "rc")
	if err := UpsertBlock(path, []string{"export OLD=1"}); err != nil {
		t.Fatalf("UpsertBlock() returned error: %v", err)
	}
	got, err := BlockUpToDate(path, []string{"export NEW=1"})
	if err != nil {
		t.Fatalf("BlockUpToDate() returned error: %v", err)
	}
	if got {
		t.Error("BlockUpToDate() = true for a file whose block content differs, want false")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/shell/... -run 'TestConfigurable_|TestIsPresent_|TestBlockUpToDate_' -v`
Expected: FAIL to compile — `undefined: Configurable`, `undefined: LookPath`, `undefined: IsPresent`, `undefined: BlockUpToDate`.

- [ ] **Step 3: Implement**

Add to `internal/shell/shell.go`, directly before the `UpsertBlock` function:

```go
// Configurable returns every shell dev setup knows how to configure, in
// a fixed, deterministic order (so prompts always appear in the same
// order across runs). Unknown is deliberately excluded: it isn't a
// concrete shell dev could write an rc file for.
func Configurable() []Shell {
	return []Shell{Bash, Zsh, Fish, PowerShell}
}

// lookupNames pairs each Configurable shell with the PATH binary
// name(s) that indicate it's installed on this machine. PowerShell
// lists both names because either a Windows PowerShell 5.1 ("powershell")
// or a PowerShell 7+ ("pwsh") install counts as present.
var lookupNames = map[Shell][]string{
	Bash:       {"bash"},
	Zsh:        {"zsh"},
	Fish:       {"fish"},
	PowerShell: {"pwsh", "powershell"},
}

// LookPath is exec.LookPath, as a package-level var so tests (in this
// package and in cmd) can control which shells appear "installed"
// without depending on whatever is actually on PATH on the machine
// running `go test` — the same seam pattern as platform.Executable.
var LookPath = exec.LookPath

// IsPresent reports whether sh appears to be installed on this
// machine: any of its lookupNames resolves via LookPath, or sh is what
// Detect() reports for the current process (covers an install that's
// genuinely running right now but not found via LookPath, e.g. an
// unusual install location not on PATH).
func IsPresent(sh Shell) bool {
	for _, name := range lookupNames[sh] {
		if _, err := LookPath(name); err == nil {
			return true
		}
	}
	return Detect() == sh
}

// BlockUpToDate reports whether path already contains exactly the
// block UpsertBlock(path, lines) would write — a missing file counts
// as "not up to date," never as an error. This is what lets dev setup
// silently skip a shell it already configured on a prior run, so a
// second run only surfaces what's actually new.
func BlockUpToDate(path string, lines []string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	block := strings.Join(append(append([]string{blockBegin}, lines...), blockEnd), "\n")
	return strings.Contains(string(data), block), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/shell/... -v 2>&1 | tail -80`
Expected: PASS for the whole package.

- [ ] **Step 5: `gofmt`/`vet`/lint + Windows cross-check**

Run: `gofmt -l internal/shell/ && go vet ./internal/shell/... && golangci-lint run ./internal/shell/... && GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=amd64 go vet ./...`
Expected: clean throughout.

- [ ] **Step 6: Commit**

```bash
git add internal/shell/shell.go internal/shell/shell_test.go
git commit -m "$(cat <<'EOF'
feat(shell): add Configurable, IsPresent, and BlockUpToDate

cmd/setup.go only ever configured whichever single shell Detect()
resolved to. These three give it what it needs to instead configure
every shell dev supports that's actually installed, and to skip one
it already configured on a prior run.
EOF
)"
```

---

### Task 4: `cmd/setup.go` — the multi-shell loop, test overhaul, and README update

**Files:**
- Modify: `internal/cliutil/cliutil.go` (add `Ferror`, next to `Fsuccess`/`Fstep`)
- Test: `internal/cliutil/cliutil_test.go` (add `TestFerror_WritesToGivenWriter`)
- Modify: `cmd/setup.go` (the whole shell-configuration section of `RunE`)
- Test: `cmd/setup_test.go` (add `fakeLookPath` helper; update 7 existing tests; replace 1; add 4 new ones)
- Modify: `README.md` (the "detects your shell" install-steps paragraph)

**Interfaces:**
- Consumes: `shell.Configurable()`, `shell.IsPresent()`, `shell.RCPath()` (existing), `shell.FunctionLines()` (existing), `shell.BlockUpToDate()`, `shell.UpsertBlock()` (existing), `shell.Confirm()` (existing), `shell.LookPath` (Task 3); `cliutil.Ferror` (this task, added first).
- Produces: nothing new for other tasks — this is the final, user-facing task.

- [ ] **Step 1: Write the failing test for `cliutil.Ferror`**

Add to `internal/cliutil/cliutil_test.go`, right after `TestFstep_WritesToGivenWriter`:

```go
func TestFerror_WritesToGivenWriter(t *testing.T) {
	var buf bytes.Buffer
	Ferror(&buf, "updating %s failed", "/home/u/.bashrc")
	if got := buf.String(); got != "✗ updating /home/u/.bashrc failed\n" {
		t.Errorf("Ferror() output = %q", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/cliutil/... -run TestFerror -v`
Expected: FAIL to compile — `undefined: Ferror`.

- [ ] **Step 3: Implement `Ferror`**

Add to `internal/cliutil/cliutil.go`, right after `Fstep`:

```go
// Ferror writes a ✗-prefixed error message to w. See Fsuccess.
func Ferror(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "✗ "+format+"\n", args...)
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/cliutil/... -v 2>&1 | tail -20`
Expected: PASS for the whole package.

- [ ] **Step 5: Add the `cmd/setup_test.go` helper and pin every existing test's shell presence**

Add this import and helper right after the existing imports in `cmd/setup_test.go`:

```go
import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/shell"
)

// fakeLookPath overrides shell.LookPath for the duration of the test so
// only the given binary names resolve, deterministically, regardless of
// what's actually installed on the machine running `go test` — the
// multi-shell loop in cmd/setup.go otherwise depends directly on the
// real PATH, which varies across dev machines and CI images (this
// project's own CI images, for instance, don't all have the same set
// of shells installed).
func fakeLookPath(t *testing.T, present ...string) {
	t.Helper()
	found := make(map[string]bool, len(present))
	for _, name := range present {
		found[name] = true
	}
	original := shell.LookPath
	shell.LookPath = func(file string) (string, error) {
		if found[file] {
			return "/fake/" + file, nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { shell.LookPath = original })
}
```

Update `skipOnWindowsRCFile`'s doc comment (its rationale no longer matches Task 1's `Detect()` wording):

```go
// skipOnWindowsRCFile skips a test that asserts on a specific rc-file
// path. On a real Windows machine, PowerShell is essentially always
// present (shell.RCPath asks a real PowerShell process for its own
// $PROFILE value there — supported whenever pwsh or powershell is
// reachable at all, true on virtually every Windows install, built-in
// Windows PowerShell included), so fakeLookPath(t, "bash") alone can't
// exclude it from these tests' target list the way it does on
// Linux/macOS. This skip stays conservative anyway: a CI runner's
// actual $PROFILE value depends on its home-directory layout and
// installed PowerShell version, neither knowable ahead of time to
// assert against here, so these tests skip on Windows rather than
// assert against a path only discoverable by actually running the
// subprocess.
func skipOnWindowsRCFile(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("dev setup's rc-file target on Windows depends on a real PowerShell subprocess's own $PROFILE value, not knowable ahead of time in this test")
	}
}
```

Add `fakeLookPath(t, "bash")` as the first line of each of these six existing tests' bodies: `TestSetupCommand_DeclinedConfirmationMakesNoChanges`, `TestSetupCommand_ConfirmedWritesRCFileWithTheDevFunction` (after its `skipOnWindowsRCFile(t)` call), `TestSetupCommand_RunningTwiceDoesNotDuplicateOrError` (same), `TestSetupCommand_SkipsRelocationWhenAlreadyInsideDevHome`, `TestSetupCommand_OffersRelocationWhenNotYetInstalled`, `TestSetupCommand_DeclinedRelocationPromptDoesNotAbortSetup`.

In `TestSetupCommand_UnsupportedShellStillConfirmable`, add `fakeLookPath(t)` (no arguments — nothing resolves) right after the Windows skip, and update the comment above `t.Setenv("SHELL", ...)`:

```go
	// Nothing resolves via LookPath: no configurable shell is "present"
	// at all, regardless of what's actually installed on the machine
	// running this test — the only way to deterministically reproduce
	// the "nothing to configure automatically" case this test exists
	// for.
	fakeLookPath(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	// An unrecognized $SHELL makes shell.Detect() return Unknown on
	// Linux/macOS, which is what the manual-fallback section prints
	// FunctionLines for. The assertion below still computes the
	// expected text via unsupportedShellMessage(runtime.GOOS) for
	// generality across Linux/macOS — it just never sees "windows"
	// here now, since that case is skipped above.
	t.Setenv("SHELL", "/bin/some-unrecognized-shell")
	fakeInstalledDev(t, devHome)
```

Replace `TestSetupCommand_UnsupportedShellDeclinedMakesNoChanges` entirely (its old "shim directory" assertion is already stale — shims were removed in an earlier sub-project — and its premise doesn't hold once presence is driven by real `LookPath` rather than `$SHELL` alone):

```go
// TestSetupCommand_NoShellPresentPrintsManualInstructionsAndMakesNoChanges
// covers the case where IsPresent finds nothing configurable at all
// (an environment with none of bash/zsh/fish/pwsh/powershell on PATH,
// and an unrecognized $SHELL): there is nothing to prompt for, so
// `dev setup` must print Detect()'s manual-fallback instructions and
// touch no rc file, without needing any confirmation to decline.
func TestSetupCommand_NoShellPresentPrintsManualInstructionsAndMakesNoChanges(t *testing.T) {
	fakeLookPath(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/some-unrecognized-shell")

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader(""))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	for _, rc := range []string{".bashrc", ".zshrc", filepath.Join(".config", "fish", "config.fish")} {
		path := filepath.Join(home, rc)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected no shell to be auto-configured when nothing is present, but %s exists", path)
		}
	}
	if !strings.Contains(out.String(), "dev() {") {
		t.Errorf("output = %q, want it to print the dev wrapper function for manual setup", out.String())
	}
}
```

- [ ] **Step 6: Add the new multi-shell tests**

Insert these four tests right after `TestSetupCommand_RunningTwiceDoesNotDuplicateOrError` (i.e. before `TestSetupCommand_RelocatesDevAndDocsIntoDevHomeWhenConfirmed`):

```go
// TestSetupCommand_ConfiguresEveryPresentShellInOneRun pins this
// feature's core behavior: a machine with two shells installed (e.g.
// Bash and Zsh on Linux, or PowerShell and Git Bash on Windows) gets
// BOTH configured in a single `dev setup` run, not just whichever one
// invoked it — the exact gap a real user hit (PowerShell configured,
// Git Bash left with nothing).
func TestSetupCommand_ConfiguresEveryPresentShellInOneRun(t *testing.T) {
	skipOnWindowsRCFile(t)
	fakeLookPath(t, "bash", "zsh")
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// One "y" per shell offered: Bash then Zsh, shell.Configurable()'s
	// fixed order.
	rootCmd.SetIn(strings.NewReader("y\ny\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	for _, rc := range []string{".bashrc", ".zshrc"} {
		path := filepath.Join(home, rc)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if !strings.Contains(string(data), "dev() {") {
			t.Errorf("%s = %q, want it to contain the dev wrapper function", path, data)
		}
	}
}

// TestSetupCommand_SkipsAlreadyConfiguredShellOnRerun covers the
// scenario the user asked for explicitly: PowerShell (here, Bash)
// already configured by an earlier run, then Zsh installed afterward —
// re-running `dev setup` must offer ONLY Zsh, not re-prompt for Bash's
// already-current block.
func TestSetupCommand_SkipsAlreadyConfiguredShellOnRerun(t *testing.T) {
	skipOnWindowsRCFile(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	fakeLookPath(t, "bash")
	var first bytes.Buffer
	rootCmd.SetOut(&first)
	rootCmd.SetErr(&first)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"setup"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("first `dev setup` returned error: %v", err)
	}

	// Zsh "installed" afterward.
	fakeLookPath(t, "bash", "zsh")
	var second bytes.Buffer
	rootCmd.SetOut(&second)
	rootCmd.SetErr(&second)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("second `dev setup` returned error: %v", err)
	}

	bashRC := filepath.Join(home, ".bashrc")
	if strings.Contains(second.String(), fmt.Sprintf("added to %s", bashRC)) {
		t.Errorf("second run output = %q, want it not to re-offer the already-current Bash block", second.String())
	}
	zshRC := filepath.Join(home, ".zshrc")
	if !strings.Contains(second.String(), fmt.Sprintf("added to %s", zshRC)) {
		t.Errorf("second run output = %q, want it to offer the newly-present Zsh block", second.String())
	}
	data, err := os.ReadFile(zshRC)
	if err != nil {
		t.Fatalf("reading %s: %v", zshRC, err)
	}
	if !strings.Contains(string(data), "dev() {") {
		t.Errorf("%s = %q, want it to contain the dev wrapper function", zshRC, data)
	}
}

// TestSetupCommand_DecliningOneShellStillWritesTheOther covers
// independence between targets: declining Bash's prompt must not
// prevent Zsh's own block from being written when Zsh is confirmed.
func TestSetupCommand_DecliningOneShellStillWritesTheOther(t *testing.T) {
	skipOnWindowsRCFile(t)
	fakeLookPath(t, "bash", "zsh")
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// "n" for Bash (first, by Configurable()'s order), "y" for Zsh.
	rootCmd.SetIn(strings.NewReader("n\ny\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(home, ".bashrc")); !os.IsNotExist(err) {
		t.Errorf("expected .bashrc not to be created after declining its prompt, stat err=%v", err)
	}
	zshData, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		t.Fatalf("reading .zshrc: %v", err)
	}
	if !strings.Contains(string(zshData), "dev() {") {
		t.Errorf(".zshrc = %q, want it to contain the dev wrapper function despite Bash's prompt being declined", zshData)
	}
}

// TestSetupCommand_OneCorruptedTargetDoesNotBlockTheOthers covers the
// spec's per-target error-handling guarantee: a pre-existing rc file
// UpsertBlock refuses to touch (an unterminated marker pair, here
// .bashrc) must not stop a different, healthy target (.zshrc) from
// still being offered and written in the same run. The command still
// reports the failure via a non-nil error, so the user knows to fix
// the broken file by hand, but only after every other target has had
// its chance.
func TestSetupCommand_OneCorruptedTargetDoesNotBlockTheOthers(t *testing.T) {
	skipOnWindowsRCFile(t)
	fakeLookPath(t, "bash", "zsh")
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	bashRC := filepath.Join(home, ".bashrc")
	corrupted := "alias a='1'\n# BEGIN dev shell setup\nexport OLD=1\n"
	if err := os.WriteFile(bashRC, []byte(corrupted), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// "y" for Bash's prompt (fails inside UpsertBlock), "y" for Zsh's.
	rootCmd.SetIn(strings.NewReader("y\ny\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("`dev setup` returned nil error, want it to report the corrupted .bashrc")
	}

	zshData, readErr := os.ReadFile(filepath.Join(home, ".zshrc"))
	if readErr != nil {
		t.Fatalf("reading .zshrc: %v", readErr)
	}
	if !strings.Contains(string(zshData), "dev() {") {
		t.Errorf(".zshrc = %q, want it to contain the dev wrapper function despite .bashrc failing", zshData)
	}
	bashData, readErr := os.ReadFile(bashRC)
	if readErr != nil {
		t.Fatalf("reading .bashrc: %v", readErr)
	}
	if string(bashData) != corrupted {
		t.Errorf(".bashrc = %q, want it left exactly as the corrupted original, untouched", bashData)
	}
}

// TestSetupCommand_RCPathErrorAbortsImmediately covers a genuine
// RCPath error (as opposed to supported=false) — an unresolvable
// $HOME, the one real way to trigger this today — must abort the
// whole command immediately, not be swallowed by the per-target
// "report and continue" handling meant only for
// BlockUpToDate/UpsertBlock failures.
func TestSetupCommand_RCPathErrorAbortsImmediately(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("$HOME is not the profile-dir env var on windows")
	}
	fakeLookPath(t, "bash")
	t.Setenv("HOME", "")
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
	})

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("`dev setup` returned nil error when $HOME is unresolvable")
	}
}

// TestSetupCommand_PresentButRCPathUnsupportedDoesNotCountAsPresent
// covers a shell that resolves via LookPath but whose RCPath reports
// supported=false (here: a faked "pwsh" on PATH with no real
// pwsh/powershell binary behind it, so the real $PROFILE subprocess
// query genuinely fails) — it must not count toward `present`, so the
// manual-fallback branch still triggers when that was the only
// candidate.
func TestSetupCommand_PresentButRCPathUnsupportedDoesNotCountAsPresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on real Windows, pwsh/powershell's $PROFILE lookup succeeds almost always, defeating this test's premise")
	}
	fakeLookPath(t, "pwsh")
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/some-unrecognized-shell")
	fakeInstalledDev(t, devHome)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader(""))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	if !strings.Contains(out.String(), "dev() {") {
		t.Errorf("output = %q, want the manual-fallback instructions since the only present shell (PowerShell) isn't actually auto-editable here", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); !os.IsNotExist(err) {
		t.Error("expected no .bashrc to be created")
	}
}
```

- [ ] **Step 7: Run the whole `cmd` package test suite to verify everything fails/compiles-against-old-code correctly**

Run: `go test ./cmd/... -run TestSetupCommand -v 2>&1 | tail -100`
Expected: compile failure (`shell.LookPath` doesn't exist as an exported var the `cmd` test file can reference — Task 3 already added it, so this should actually compile; what fails instead is behavioral: e.g. `TestSetupCommand_ConfiguresEveryPresentShellInOneRun` fails because `cmd/setup.go` hasn't been rewritten yet and still only configures one shell).

- [ ] **Step 8: Implement the `cmd/setup.go` rewrite**

Replace the `setupCmd.RunE` function body (currently `cmd/setup.go:21-92`) with:

```go
	RunE: func(cmd *cobra.Command, args []string) error {
		printBanner(cmd.OutOrStdout())

		devHome, err := platform.DevHome()
		if err != nil {
			return err
		}

		// present tracks whether at least one configurable shell was
		// actually found on this machine (installed and auto-editable)
		// — independent of whether it needed any change. Only when
		// this stays false do we fall back to printing manual
		// instructions for Detect()'s single best guess, the same way
		// dev setup always has for a shell it can't act on.
		//
		// targetErr tracks whether any individual target failed (a
		// corrupted rc file UpsertBlock refuses to touch, say) — such a
		// failure must not stop other targets in this same run from
		// still being offered and written, so it's reported inline and
		// only turned into RunE's own return value once the whole loop
		// (and everything after it) has run to completion.
		present := false
		var targetErr error
		for _, sh := range shell.Configurable() {
			if !shell.IsPresent(sh) {
				continue
			}
			path, supported, err := shell.RCPath(sh)
			if err != nil {
				return err
			}
			if !supported {
				continue
			}
			present = true

			lines := shell.FunctionLines(sh, devHome)
			upToDate, err := shell.BlockUpToDate(path, lines)
			if err != nil {
				cliutil.Ferror(cmd.ErrOrStderr(), "checking %s: %v", path, err)
				targetErr = err
				continue
			}
			if upToDate {
				continue
			}

			fmt.Fprintf(cmd.OutOrStdout(), "The following will be added to %s:\n\n", path)
			for _, line := range lines {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			fmt.Fprintln(cmd.OutOrStdout())

			confirmed, err := shell.Confirm(fmt.Sprintf("Add this to %s? [y/N] ", path), cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if !confirmed {
				cliutil.Fstep(cmd.OutOrStdout(), "No changes made.")
				continue
			}

			if err := shell.UpsertBlock(path, lines); err != nil {
				cliutil.Ferror(cmd.ErrOrStderr(), "updating %s: %v", path, err)
				targetErr = err
				continue
			}
			cliutil.Fsuccess(cmd.OutOrStdout(), "Updated %s", path)
			cliutil.Fstep(cmd.OutOrStdout(), "Restart your shell (or run `%s`) for these changes to take effect", reloadHint(sh, path))
		}

		if !present {
			sh := shell.Detect()
			fmt.Fprintln(cmd.OutOrStdout(), unsupportedShellMessage(runtime.GOOS))
			fmt.Fprintln(cmd.OutOrStdout())
			for _, line := range shell.FunctionLines(sh, devHome) {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			fmt.Fprintln(cmd.OutOrStdout())
		}

		if err := relocateIfNeeded(cmd, devHome); err != nil {
			return err
		}

		// Independent of every shell loop/fallback above: the registry
		// entries cover every Windows process (GUI apps, cmd.exe, a
		// non-PowerShell integrated terminal), not just the shells
		// whose rc file/profile got a function written to it — so
		// this is offered unconditionally on Windows.
		if runtime.GOOS == "windows" {
			envConfirmed, err := shell.Confirm("Add these to your user environment now? [y/N] ", cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if envConfirmed {
				//nolint:staticcheck // SA4023: Check is only statically-always-true on non-Windows builds; it's genuinely needed on Windows
				if err := shell.ConfigureWindowsUserEnv(devHome); err != nil {
					return err
				}
				cliutil.Fsuccess(cmd.OutOrStdout(), "Updated your user environment (DEV_HOME and PATH)")
			}
		}
		return targetErr
	},
```

Also update the command's one-line `Short` description a few lines above it:

```go
	Short: "Detect every shell you have installed and configure PATH for each (asks for confirmation)",
```

- [ ] **Step 9: Run the full test suite to verify everything passes**

Run: `go test ./cmd/... -v 2>&1 | tail -200`
Expected: PASS for every test in the package, including all the new and modified ones.

- [ ] **Step 10: Update the README**

Replace the third numbered install step in `README.md` (currently the paragraph starting "3. detects your shell..."):

```markdown
3. detects every shell you have installed and, with your confirmation,
   installs a small `dev` shell function (not a separate binary) into
   each one's rc file/profile — not just whichever shell happens to be
   running `dev setup`. On Windows, for instance, having both
   PowerShell and Git Bash installed gets both configured in one run.
   The function runs the real `dev` command, then — only after `dev
   lang`/`dev l` — refreshes `PATH` in your current shell so a
   newly-activated version takes effect immediately, no restart
   needed. The function puts `$DEV_HOME` on your `PATH`, where `dev`
   itself lives, and adds each language's currently-active version's
   own `bin` directory directly — no copies, no symlinks. Re-running
   `dev setup` later (after installing a shell that wasn't present
   before) only offers what's new — an already-configured shell is
   left untouched. A shell dev can't safely auto-edit (or can't detect
   at all) gets the function printed to add by hand instead; on
   Windows, `dev setup` also offers to write your user environment
   variables directly.
```

- [ ] **Step 11: Full repo check**

Run: `make check`
Expected: `gofmt -l` reports nothing, `go vet ./...` clean, `golangci-lint run` reports `0 issues.`, `go test ./...` all `ok`, `go build ./...` succeeds.

Run: `GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=amd64 go vet ./... && GOOS=windows GOARCH=amd64 go test -c -o /tmp/cmd_windows_test.exe ./cmd/...`
Expected: all three succeed (this is the furthest Windows verification possible outside CI's own `windows-latest` job).

- [ ] **Step 12: Commit**

```bash
git add internal/cliutil/cliutil.go internal/cliutil/cliutil_test.go cmd/setup.go cmd/setup_test.go README.md
git commit -m "$(cat <<'EOF'
feat(setup): configure every present shell, not just the detected one

dev setup only ever wrote to whichever single shell shell.Detect()
resolved to, so a user who ran it from PowerShell got nothing in Git
Bash, and vice versa. It now loops over every shell dev supports
(shell.Configurable()), configuring each one that's actually installed
and not already up to date — on any OS, not just Windows. A corrupted
target no longer blocks the others from still being offered.
EOF
)"
```
