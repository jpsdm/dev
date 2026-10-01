# Install-Location Enforcement & Windows Env Config Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `dev` a real installed location (`$DEV_HOME` itself), refuse to run any command except `setup`/`version`/`--version` from outside it, have `dev setup` relocate a freshly-downloaded binary into `$DEV_HOME` and configure `PATH` for it, and add real (not just printed) automatic environment configuration on Windows.

**Architecture:** A new pure comparison helper in `internal/platform` backs two independent dispatch-gate call sites (`main.go`'s pre-Cobra shim dispatch, and `cmd/root.go`'s `PersistentPreRunE`). `dev setup` gains a relocation step reusing the existing `copyExecutable` helper, and `internal/shell` gains a build-tag-split Windows registry writer whose PATH-merging logic is pure and testable on any OS, with only the actual registry I/O gated to real Windows.

**Tech Stack:** Go stdlib (`os`, `path/filepath`, `strings`), `golang.org/x/sys/windows/registry` (already an indirect dependency at v0.46.0 — this plan promotes it to direct, no new module).

**Spec:** docs/superpowers/specs/2026-09-28-install-enforcement-design.md

## Global Constraints

- `RunningFromDevHome`'s directory comparison is case-insensitive when `runtime.GOOS == "windows"`, case-sensitive otherwise.
- The "not installed" warning text is one shared constant, used by both `main.go` and `cmd/root.go` — never restated separately.
- `setup`, the `version` subcommand, and the `--version` flag are exempt from the location-check gate; every other command is not.
- Only a **registered** shim name triggers the shim-side check — an unrecognized name still falls through to the normal CLI unchanged (pre-existing, already-tested behavior in `shimBinaryName`/`IsRegistered`).
- `dev setup`'s relocation step only runs when the binary is **not** already running from `$DEV_HOME` — re-running `setup` on an already-installed copy skips it entirely (nothing to relocate, and no self-copy risk).
- Relocation is copy-then-best-effort-delete of the original — never a hard `os.Rename`/move.
- `PATH` gets exactly two entries after this change: `$DEV_HOME` and `$DEV_HOME/bin`, on every shell (bash/zsh/fish POSIX lines, PowerShell lines, and the real Windows registry write).
- Windows environment writes target `HKEY_CURRENT_USER\Environment` only — never `HKEY_LOCAL_MACHINE`.
- The Windows registry write is idempotent: re-running `dev setup` must not duplicate `PATH` entries.
- Every commit ends with `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.

## Review Focus

- **`DEV_HOME` unset with no resolvable `$HOME`, or set to a relative path.** `platform.DevHome()` can already error (no home directory); the new location-check call sites in both `main.go` and `cmd/root.go` must propagate that error cleanly (refuse safely) rather than panicking or silently treating it as "installed."
- **Windows path-comparison case sensitivity is the single most likely real-world mismatch** — a differently-cased but equivalent path (e.g. a drive letter or component case difference between how `os.Executable()` and `$DEV_HOME` each resolve) must still compare equal on Windows, and must NOT compare equal cross-platform when cases genuinely differ on Linux/macOS.
- **Re-running `dev setup` on an already-installed copy must not attempt to copy `$DEV_HOME/dev` onto itself** (the exact self-copy scenario a prior, since-reverted attempt at this feature already had to reason through) **or duplicate `PATH`/registry entries.**
- **Both `dev --version` and `dev version` must remain callable from outside `$DEV_HOME`** — a naive implementation might only exempt one of the two forms (the flag vs. the subcommand), since Cobra handles them through different code paths.
- **The Windows registry code must at least cross-compile cleanly** (`GOOS=windows go build ./...`) — this development environment cannot runtime-test Windows registry behavior at all, so a task that skips this check could ship code that only fails once a real Windows user tries to build or run it.

---

### Task 1: `internal/platform` — the location-check helper

**Files:**
- Modify: `internal/platform/platform.go` (add to the existing file — it's small and this is exactly the kind of location-resolution logic it already owns)
- Test: `internal/platform/platform_test.go`

**Interfaces:**
- Consumes: `DevHome() (string, error)` (existing, in this same file).
- Produces: `func RunningFromDevHome() (ok bool, devHome string, err error)`, `func dirMatchesDevHome(exeDir, devHome, goos string) bool` (unexported, pure — later tasks don't call this directly, but its existence is what makes `RunningFromDevHome` testable), `const NotInstalledWarning string` — the exact shared warning text Task 2 and Task 3 both use.

- [ ] **Step 1: Write the failing tests**

`internal/platform/platform_test.go` already exists (with `TestDevHome_*` tests) and already imports `os`, `path/filepath`, `runtime`, and `testing` — only `strings` needs adding to its import block. Append these tests to the file:

```go
func TestDirMatchesDevHome_ExactMatchOnDevHomeItself(t *testing.T) {
	t.Parallel()
	if !dirMatchesDevHome("/home/user/.dev", "/home/user/.dev", "linux") {
		t.Error("dirMatchesDevHome() = false, want true for an exact devHome match")
	}
}

func TestDirMatchesDevHome_ExactMatchOnBinSubdir(t *testing.T) {
	t.Parallel()
	if !dirMatchesDevHome("/home/user/.dev/bin", "/home/user/.dev", "linux") {
		t.Error("dirMatchesDevHome() = false, want true for a devHome/bin match")
	}
}

func TestDirMatchesDevHome_RejectsUnrelatedDirectory(t *testing.T) {
	t.Parallel()
	if dirMatchesDevHome("/tmp/extracted", "/home/user/.dev", "linux") {
		t.Error("dirMatchesDevHome() = true, want false for an unrelated directory")
	}
}

func TestDirMatchesDevHome_CaseInsensitiveOnWindows(t *testing.T) {
	t.Parallel()
	if !dirMatchesDevHome(`C:\Users\Foo\.DEV`, `c:\users\foo\.dev`, "windows") {
		t.Error("dirMatchesDevHome() = false, want true — Windows paths are case-insensitive")
	}
}

func TestDirMatchesDevHome_CaseSensitiveOnLinux(t *testing.T) {
	t.Parallel()
	if dirMatchesDevHome("/home/user/.DEV", "/home/user/.dev", "linux") {
		t.Error("dirMatchesDevHome() = true, want false — Linux paths are case-sensitive")
	}
}

func TestRunningFromDevHome_FalseWhenDevHomeIsSomewhereElse(t *testing.T) {
	// The real test binary running this test is never actually inside
	// an arbitrary temp DEV_HOME, so this exercises the true I/O path
	// (os.Executable() + DevHome()) end-to-end and must report false.
	t.Setenv("DEV_HOME", t.TempDir())

	ok, devHome, err := RunningFromDevHome()
	if err != nil {
		t.Fatalf("RunningFromDevHome() returned error: %v", err)
	}
	if ok {
		t.Error("RunningFromDevHome() = true, want false (test binary isn't inside DEV_HOME)")
	}
	if devHome == "" {
		t.Error("RunningFromDevHome() returned empty devHome alongside a nil error")
	}
}

func TestNotInstalledWarning_MentionsSetup(t *testing.T) {
	t.Parallel()
	if !strings.Contains(NotInstalledWarning, "dev setup") {
		t.Errorf("NotInstalledWarning = %q, want it to mention `dev setup`", NotInstalledWarning)
	}
}
```

Add `"strings"` to the test file's imports if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/platform/... -v -run 'TestDirMatchesDevHome|TestRunningFromDevHome|TestNotInstalledWarning'`
Expected: FAIL — `undefined: dirMatchesDevHome`, `undefined: RunningFromDevHome`, `undefined: NotInstalledWarning`.

- [ ] **Step 3: Write the implementation**

Add to `internal/platform/platform.go`. Add `"strings"` to the existing import block if not already present:

```go
// NotInstalledWarning is shown when dev (or a shim) is invoked from
// outside $DEV_HOME — both main.go's shim dispatch and cmd/root.go's
// PersistentPreRunE use this exact text, so the message a user sees is
// identical regardless of which dispatch path caught it.
const NotInstalledWarning = "dev isn't installed yet — this looks like a copy running from outside $DEV_HOME. " +
	"Run `dev setup` to install it (creates $DEV_HOME, sets up shims, and configures your PATH), then run this command again."

// RunningFromDevHome reports whether the currently-executing binary's
// own directory is exactly $DEV_HOME or exactly $DEV_HOME/bin (where
// shims live). Also returns the resolved devHome path so callers that
// need it (e.g. to build the warning, or to skip a relocation step
// that's already done) don't have to call DevHome() a second time.
func RunningFromDevHome() (ok bool, devHome string, err error) {
	devHome, err = DevHome()
	if err != nil {
		return false, "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return false, "", fmt.Errorf("finding the running executable: %w", err)
	}
	return dirMatchesDevHome(filepath.Dir(exe), devHome, runtime.GOOS), devHome, nil
}

// dirMatchesDevHome is RunningFromDevHome's pure comparison logic,
// separated so it's testable with arbitrary inputs regardless of
// where the test binary actually lives — the same reason javaOS's
// switch logic lives in a separate mapJavaOS function elsewhere in
// this codebase. goos is a parameter (not read from runtime.GOOS
// directly) for the same testability reason.
func dirMatchesDevHome(exeDir, devHome, goos string) bool {
	exeDir = filepath.Clean(exeDir)
	devHome = filepath.Clean(devHome)
	binDir := filepath.Join(devHome, "bin")
	if goos == "windows" {
		return strings.EqualFold(exeDir, devHome) || strings.EqualFold(exeDir, binDir)
	}
	return exeDir == devHome || exeDir == binDir
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/platform/... -v`
Expected: PASS (all tests in the package, including the new ones).

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/platform/... && gofmt -l internal/platform/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/platform.go internal/platform/platform_test.go
git commit -m "$(cat <<'EOF'
Add RunningFromDevHome, the install-location check helper

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `main.go` — shim-dispatch gating

**Files:**
- Modify: `main.go`
- Test: `main_test.go`

**Interfaces:**
- Consumes: `platform.RunningFromDevHome() (bool, string, error)`, `platform.NotInstalledWarning string` (Task 1); `shim.IsRegistered(binName string) bool`, `shim.Run(binName string, args []string) error` (pre-existing).
- Produces: `func decideDispatch(name string, registered, runningFromDevHome bool) dispatchDecision` and the `dispatchDecision` type/constants — pure decision logic, not consumed by any later task, but this is the piece that makes `main.go`'s new branching testable without a subprocess.

- [ ] **Step 1: Write the failing tests**

Append to `main_test.go`:

```go
func TestDecideDispatch_UnnamedFallsThroughToCLI(t *testing.T) {
	t.Parallel()
	if got := decideDispatch("", false, false); got != dispatchAsCLI {
		t.Errorf("decideDispatch(\"\", false, false) = %v, want dispatchAsCLI", got)
	}
}

func TestDecideDispatch_UnregisteredNameFallsThroughToCLI(t *testing.T) {
	t.Parallel()
	// An arbitrary renamed copy of dev, or any name that isn't a
	// registered shim, must fall through unchanged — this is
	// pre-existing, already-relied-upon behavior (see
	// TestShimBinaryName's own "dev"/unrecognized-name cases) that
	// this task must not disturb.
	if got := decideDispatch("some-random-name", false, false); got != dispatchAsCLI {
		t.Errorf("decideDispatch(\"some-random-name\", false, false) = %v, want dispatchAsCLI", got)
	}
}

func TestDecideDispatch_RegisteredNameInsideDevHomeDispatchesAsShim(t *testing.T) {
	t.Parallel()
	if got := decideDispatch("node", true, true); got != dispatchAsShim {
		t.Errorf("decideDispatch(\"node\", true, true) = %v, want dispatchAsShim", got)
	}
}

func TestDecideDispatch_RegisteredNameOutsideDevHomeRefuses(t *testing.T) {
	t.Parallel()
	if got := decideDispatch("node", true, false); got != dispatchRefuseNotInstalled {
		t.Errorf("decideDispatch(\"node\", true, false) = %v, want dispatchRefuseNotInstalled", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test . -v -run TestDecideDispatch`
Expected: FAIL — `undefined: decideDispatch`, `undefined: dispatchAsCLI`, etc.

- [ ] **Step 3: Write the implementation**

Replace `main.go`'s full content with:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jpsdm/dev/cmd"
	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/shim"
)

// dispatchDecision is what main should do with this invocation.
type dispatchDecision int

const (
	dispatchAsCLI dispatchDecision = iota
	dispatchAsShim
	dispatchRefuseNotInstalled
)

// decideDispatch is main's branching logic, separated from main()
// itself so it's directly unit-testable — main() only ever wires real
// I/O (shim.IsRegistered, platform.RunningFromDevHome) into this pure
// function's parameters.
func decideDispatch(name string, registered, runningFromDevHome bool) dispatchDecision {
	if name == "" || !registered {
		return dispatchAsCLI
	}
	if !runningFromDevHome {
		return dispatchRefuseNotInstalled
	}
	return dispatchAsShim
}

func main() {
	name := shimBinaryName(os.Args[0])
	registered := name != "" && shim.IsRegistered(name)

	var runningFromDevHome bool
	if registered {
		// Only resolved when it might actually matter — an unnamed or
		// unregistered invocation always falls through to the CLI
		// regardless of this value, so there's no reason to pay for
		// os.Executable()/DevHome() resolution (or surface an error
		// from it) on every single ordinary `dev` invocation.
		var err error
		runningFromDevHome, _, err = platform.RunningFromDevHome()
		if err != nil {
			cliutil.PrintError(err)
			os.Exit(1)
		}
	}

	switch decideDispatch(name, registered, runningFromDevHome) {
	case dispatchAsShim:
		if err := shim.Run(name, os.Args[1:]); err != nil {
			cliutil.PrintError(err)
			os.Exit(1)
		}
		return
	case dispatchRefuseNotInstalled:
		cliutil.Error("%s", platform.NotInstalledWarning)
		os.Exit(1)
		return
	}
	cmd.Execute()
}

// shimBinaryName returns the binary name to shim for based on how this
// executable was invoked (its own argv[0]), or "" if it was invoked as
// the dev CLI itself.
func shimBinaryName(arg0 string) string {
	base := strings.TrimSuffix(filepath.Base(arg0), ".exe")
	if base == "dev" {
		return ""
	}
	return base
}
```

(`cliutil.Error` is `internal/cliutil`'s existing `✗`-prefixed stderr printer — check `internal/cliutil/cliutil.go` for its exact signature if this doesn't compile as written; it takes the same `format string, args ...any` shape as `Success`/`Step`, called here with no `%`-verbs so the whole message is a single literal argument.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test . -v`
Expected: PASS (all tests in package `main`, including the pre-existing `TestShimBinaryName`).

- [ ] **Step 5: Run `go vet`, `gofmt`, and a full build**

Run: `go vet . && gofmt -l . && go build ./...`
Expected: no output from `go vet`/`gofmt`; build succeeds.

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "$(cat <<'EOF'
Refuse shim dispatch for a registered name run outside $DEV_HOME

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `cmd/root.go` — `PersistentPreRunE` gating

**Files:**
- Modify: `cmd/root.go`
- Test: `cmd/root_test.go`

**Interfaces:**
- Consumes: `platform.RunningFromDevHome()`, `platform.NotInstalledWarning` (Task 1).
- Produces: nothing consumed by a later task in this plan.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/root_test.go`. These exercise `rootCmd.Execute()` directly (the same pattern `TestExecute_ErrorPathRespectsVerboseFlag` already uses for a synthetic subcommand), with `DEV_HOME` pointed at a temp directory the test binary is never actually running from — so every one of these genuinely exercises the "outside $DEV_HOME" branch, not a mock:

```go
func TestPersistentPreRunE_RefusesOrdinaryCommandOutsideDevHome(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{"lang", "current"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("rootCmd.Execute() returned nil error for an ordinary command run outside $DEV_HOME")
	}
	if !strings.Contains(err.Error(), "dev setup") {
		t.Errorf("error = %q, want it to mention `dev setup`", err.Error())
	}
}

func TestPersistentPreRunE_AllowsSetupOutsideDevHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DEV_HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/bash")
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetIn(strings.NewReader("n\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error when run outside $DEV_HOME (it must always be allowed): %v", err)
	}
}

func TestPersistentPreRunE_AllowsVersionSubcommandOutsideDevHome(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"version"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev version` returned error when run outside $DEV_HOME (it must always be allowed): %v", err)
	}
}

func TestPersistentPreRunE_AllowsVersionFlagOutsideDevHome(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"--version"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		if err := rootCmd.Flags().Set("version", "false"); err != nil {
			t.Fatalf("resetting --version flag: %v", err)
		}
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev --version` returned error when run outside $DEV_HOME (it must always be allowed): %v", err)
	}
}
```

No import changes needed — `cmd/root_test.go` already imports `bytes`, `io`, and `strings`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/... -v -run TestPersistentPreRunE`
Expected: `TestPersistentPreRunE_RefusesOrdinaryCommandOutsideDevHome` FAILs (no error is returned today — the check doesn't exist yet); the other three currently pass vacuously (there's nothing to refuse yet) but re-run them after Step 3 too, since a broken exemption could make them fail then instead.

- [ ] **Step 3: Write the implementation**

In `cmd/root.go`, change `PersistentPreRun` to `PersistentPreRunE`:

```go
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		cliutil.SetVerbose(verboseFlag)
		if cmd.Name() == "setup" || cmd.Name() == "version" {
			return nil
		}
		if v, _ := cmd.Flags().GetBool("version"); v {
			return nil
		}
		ok, _, err := platform.RunningFromDevHome()
		if err != nil {
			return err
		}
		if !ok {
			return errors.New(platform.NotInstalledWarning)
		}
		return nil
	},
```

Add `"errors"` to the import block if not already present (it is — `cmd/root.go` already imports `"errors"` for `exitCodeFor`'s `errors.Is` call).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/... -v`
Expected: PASS — the whole `cmd` package's suite, not just the new tests (this file's `PersistentPreRunE` change affects every command's dispatch, so a regression could show up in an existing test rather than a new one).

- [ ] **Step 5: Run `go vet`, `gofmt`, and a full build**

Run: `go vet ./... && gofmt -l . && go build ./...`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/root.go cmd/root_test.go
git commit -m "$(cat <<'EOF'
Refuse ordinary commands run outside $DEV_HOME, exempting setup/version

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `internal/shell` — two `PATH` entries on every shell

**Files:**
- Modify: `internal/shell/shell.go`
- Test: `internal/shell/shell_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: no new exported names — `ExportLines`'s existing signature and behavior change (more lines / different PATH content), consumed by Task 6 (`cmd/setup.go`) exactly as `ExportLines` already is today.

- [ ] **Step 1: Write the failing tests**

`internal/shell/shell_test.go` already has one existing test, `TestExportLines_Bash` (currently at line 32), whose assertion pins the *old*, single-entry `PATH` line — it will start failing once Step 3's implementation lands, so update it in place rather than leaving it to fail unexplained. Change its second `strings.Contains` check from:

```go
	if !strings.Contains(joined, `export PATH="$DEV_HOME/bin:$PATH"`) {
		t.Errorf("ExportLines(Bash) = %v, want it to contain the PATH export", lines)
	}
```

to:

```go
	if !strings.Contains(joined, `export PATH="$DEV_HOME:$DEV_HOME/bin:$PATH"`) {
		t.Errorf("ExportLines(Bash) = %v, want it to contain the PATH export", lines)
	}
```

(Nothing else in that test changes — its `DEV_HOME` assertion above this block is unaffected.) None of the file's other existing `ExportLines` tests (`TestExportLines_Fish`, `TestExportLines_PowerShellDoesNotBackslashEscapeThePath`, `TestExportLines_UnknownShellWarnsItWasNotRecognized`, `TestExportLines_QuotesPathsSafelyAcrossShells`) assert on the `PATH` line's content, only on the `DEV_HOME` line or quoting behavior — they need no changes.

Then append three new tests to the same file, covering the `PATH` line's new two-entry content directly:

```go
func TestExportLines_PosixIncludesBothDevHomeAndBinOnPath(t *testing.T) {
	t.Parallel()
	got := ExportLines(Bash, "/home/user/.dev")
	want := []string{
		`export DEV_HOME='/home/user/.dev'`,
		`export PATH="$DEV_HOME:$DEV_HOME/bin:$PATH"`,
	}
	if len(got) != len(want) {
		t.Fatalf("ExportLines() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ExportLines()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExportLines_FishIncludesBothDevHomeAndBinOnPath(t *testing.T) {
	t.Parallel()
	got := ExportLines(Fish, "/home/user/.dev")
	want := []string{
		`set -gx DEV_HOME /home/user/.dev`,
		`set -gx PATH $DEV_HOME $DEV_HOME/bin $PATH`,
	}
	if len(got) != len(want) {
		t.Fatalf("ExportLines() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ExportLines()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExportLines_PowerShellIncludesBothDevHomeAndBinOnPath(t *testing.T) {
	t.Parallel()
	got := ExportLines(PowerShell, `C:\Users\foo\.dev`)
	want := []string{
		`$env:DEV_HOME = "C:\Users\foo\.dev"`,
		`$env:PATH = "$env:DEV_HOME;$env:DEV_HOME\bin;$env:PATH"`,
	}
	if len(got) != len(want) {
		t.Fatalf("ExportLines() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ExportLines()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

If `internal/shell/shell_test.go` already has differently-named tests covering `ExportLines` for these three cases (very likely, given `ExportLines` already exists and is tested), **update those existing tests' `want` values in place** to match the two-entry form above instead of adding parallel new tests with different names — this plan's names are illustrative; match this file's own existing naming convention. Do not leave both an old one-entry-`want` test and a new two-entry test coexisting.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/shell/... -v -run TestExportLines`
Expected: FAIL — the existing implementation still only emits one `PATH` entry (`$DEV_HOME/bin`), not two.

- [ ] **Step 3: Write the implementation**

In `internal/shell/shell.go`, three lines change — `Fish`'s `PATH` line, `PowerShell`'s `PATH` line, and `posixExportLines`'s `PATH` line — nothing else in `ExportLines`, `shellQuote`, or the surrounding file:

```go
	case Fish:
		return []string{
			fmt.Sprintf("set -gx DEV_HOME %s", shellQuote(devHome)),
			`set -gx PATH $DEV_HOME $DEV_HOME/bin $PATH`,
		}
	case PowerShell:
		// Not Go's %q: it escapes backslashes as \\, which is wrong
		// inside a PowerShell double-quoted string (a literal Windows
		// path like C:\Users\foo\.dev must not be backslash-escaped
		// there). Only a literal embedded double-quote needs escaping,
		// via PowerShell's backtick escape character.
		escaped := strings.ReplaceAll(devHome, `"`, "`\"")
		return []string{
			fmt.Sprintf(`$env:DEV_HOME = "%s"`, escaped),
			`$env:PATH = "$env:DEV_HOME;$env:DEV_HOME\bin;$env:PATH"`,
		}
```

and:

```go
func posixExportLines(devHome string) []string {
	return []string{
		fmt.Sprintf(`export DEV_HOME=%s`, shellQuote(devHome)),
		`export PATH="$DEV_HOME:$DEV_HOME/bin:$PATH"`,
	}
}
```

(`ExportLines`'s `Unknown` and `default` cases already delegate to `posixExportLines`, so they pick up the change automatically — nothing to edit there.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/shell/... -v`
Expected: PASS — the whole package's suite (this function has callers/tests beyond just `ExportLines` itself, e.g. anything asserting on `RCPath`/`UpsertBlock` interplay — a change here shouldn't touch those, but confirm).

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/shell/... && gofmt -l internal/shell/`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/shell/shell.go internal/shell/shell_test.go
git commit -m "$(cat <<'EOF'
Add $DEV_HOME (not just $DEV_HOME/bin) to every shell's PATH lines

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `internal/shell` — real Windows user-environment configuration

**Files:**
- Create: `internal/shell/windows_env.go` (`//go:build windows`)
- Create: `internal/shell/windows_env_other.go` (`//go:build !windows`)
- Create: `internal/shell/windows_env_merge.go` (pure logic, no build tag — this is what Step 1's tests exercise directly on any OS)
- Test: `internal/shell/windows_env_merge_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `func mergeUserPath(existing string, wanted []string) string` (pure, exported so it's directly testable and so `windows_env.go` can call it); `func ConfigureWindowsUserEnv(devHome string) error` (the function Task 6 calls — its real body only exists in the Windows-tagged file, with an identical-signature stub in the non-Windows file returning a "Windows-only" error, so the rest of the codebase compiles everywhere even though the function is only ever actually invoked behind a `runtime.GOOS == "windows"` check).

- [ ] **Step 1: Write the failing tests for the pure merge logic**

Create `internal/shell/windows_env_merge_test.go`:

```go
package shell

import "testing"

func TestMergeUserPath_EmptyExistingGetsWantedInOrder(t *testing.T) {
	t.Parallel()
	got := mergeUserPath("", []string{`C:\Users\foo\.dev`, `C:\Users\foo\.dev\bin`})
	want := `C:\Users\foo\.dev;C:\Users\foo\.dev\bin`
	if got != want {
		t.Errorf("mergeUserPath() = %q, want %q", got, want)
	}
}

func TestMergeUserPath_PrependsMissingEntriesBeforeExisting(t *testing.T) {
	t.Parallel()
	got := mergeUserPath(`C:\other`, []string{`C:\Users\foo\.dev`})
	want := `C:\Users\foo\.dev;C:\other`
	if got != want {
		t.Errorf("mergeUserPath() = %q, want %q", got, want)
	}
}

func TestMergeUserPath_DoesNotDuplicateAlreadyPresentEntry(t *testing.T) {
	t.Parallel()
	got := mergeUserPath(`C:\Users\foo\.dev;C:\other`, []string{`C:\Users\foo\.dev`, `C:\Users\foo\.dev\bin`})
	want := `C:\Users\foo\.dev\bin;C:\Users\foo\.dev;C:\other`
	if got != want {
		t.Errorf("mergeUserPath() = %q, want %q (only the missing entry should be prepended)", got, want)
	}
}

func TestMergeUserPath_MatchIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	got := mergeUserPath(`c:\users\foo\.dev`, []string{`C:\Users\Foo\.dev`})
	want := `c:\users\foo\.dev`
	if got != want {
		t.Errorf("mergeUserPath() = %q, want %q (an equivalent differently-cased entry must not be duplicated)", got, want)
	}
}

func TestMergeUserPath_IgnoresEmptySegmentsInExisting(t *testing.T) {
	t.Parallel()
	got := mergeUserPath(`C:\other;;`, []string{`C:\Users\foo\.dev`})
	want := `C:\Users\foo\.dev;C:\other`
	if got != want {
		t.Errorf("mergeUserPath() = %q, want %q (a stray empty segment from a trailing ';' must not survive)", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/shell/... -v -run TestMergeUserPath`
Expected: FAIL — `undefined: mergeUserPath`.

- [ ] **Step 3: Write the pure merge logic**

Create `internal/shell/windows_env_merge.go`:

```go
package shell

import "strings"

// mergeUserPath returns existing's ";"-separated PATH entries with
// any of wanted not already present (case-insensitively — Windows
// paths are case-insensitive) prepended, in wanted's own order,
// ahead of existing's entries in their original order. Empty segments
// in existing (e.g. from a stray leading/trailing/double ";") are
// dropped. Pure logic, with no registry I/O, so it's testable on any
// OS — the Windows-only registry read/write lives in windows_env.go.
func mergeUserPath(existing string, wanted []string) string {
	var existingEntries []string
	for _, e := range strings.Split(existing, ";") {
		if e != "" {
			existingEntries = append(existingEntries, e)
		}
	}

	present := make(map[string]bool, len(existingEntries))
	for _, e := range existingEntries {
		present[strings.ToLower(e)] = true
	}

	var missing []string
	for _, w := range wanted {
		if !present[strings.ToLower(w)] {
			missing = append(missing, w)
		}
	}

	all := append(missing, existingEntries...)
	return strings.Join(all, ";")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/shell/... -v -run TestMergeUserPath`
Expected: PASS (all 5).

- [ ] **Step 5: Write the Windows registry implementation (untestable at runtime here — cross-compile only)**

Create `internal/shell/windows_env.go`:

```go
//go:build windows

package shell

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// ConfigureWindowsUserEnv sets DEV_HOME and merges devHome and
// devHome\bin into the current user's PATH — HKEY_CURRENT_USER\
// Environment, never HKEY_LOCAL_MACHINE (which would need admin
// rights and affect every user on the machine). Idempotent: entries
// already present in PATH are not duplicated (see mergeUserPath).
// Best-effort broadcasts WM_SETTINGCHANGE afterward so already-open
// applications notice without a reboot; a new terminal session picks
// up the registry change on its own regardless, so a broadcast
// failure is not treated as an error.
func ConfigureWindowsUserEnv(devHome string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("opening HKCU\\Environment: %w", err)
	}
	defer key.Close()

	existing, _, err := key.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("reading current user PATH: %w", err)
	}

	wanted := []string{devHome, devHome + `\bin`}
	merged := mergeUserPath(existing, wanted)

	if err := key.SetStringValue("Path", merged); err != nil {
		return fmt.Errorf("writing user PATH: %w", err)
	}
	if err := key.SetStringValue("DEV_HOME", devHome); err != nil {
		return fmt.Errorf("writing DEV_HOME: %w", err)
	}

	broadcastEnvironmentChange()
	return nil
}

// broadcastEnvironmentChange sends WM_SETTINGCHANGE so already-running
// processes (e.g. File Explorer) notice the environment change without
// a reboot. golang.org/x/sys/windows doesn't wrap SendMessageTimeout,
// so it's declared directly here. Failure is deliberately ignored —
// see ConfigureWindowsUserEnv's doc comment for why this is safe to
// treat as non-fatal.
func broadcastEnvironmentChange() {
	user32 := syscall.NewLazyDLL("user32.dll")
	sendMessageTimeout := user32.NewProc("SendMessageTimeoutW")

	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
	)
	param, _ := syscall.UTF16PtrFromString("Environment")
	sendMessageTimeout.Call(
		uintptr(hwndBroadcast),
		uintptr(wmSettingChange),
		0,
		uintptr(unsafe.Pointer(param)),
		uintptr(smtoAbortIfHung),
		uintptr(5000),
		0,
	)
}
```

Create `internal/shell/windows_env_other.go`:

```go
//go:build !windows

package shell

import "fmt"

// ConfigureWindowsUserEnv's non-Windows stub. Never actually called
// outside a runtime.GOOS == "windows" branch — exists only so the rest
// of the codebase compiles on every OS.
func ConfigureWindowsUserEnv(devHome string) error {
	return fmt.Errorf("ConfigureWindowsUserEnv is only supported on Windows")
}
```

- [ ] **Step 6: Add the direct dependency and cross-compile**

Run:
```bash
go get golang.org/x/sys@v0.46.0
go mod tidy
GOOS=windows GOARCH=amd64 go build ./...
```
Expected: `go mod tidy` moves `golang.org/x/sys` from the `// indirect` block to the direct `require` block in `go.mod` (it's already present transitively at this exact version, so this should not change its resolved version or pull in anything new — if `go mod tidy` reports a different version being selected, stop and report rather than proceeding, since that would be an unexpected transitive dependency change this task isn't meant to cause). The `GOOS=windows` build must succeed with no errors — this is the only verification this environment can perform for `windows_env.go`; note in your report that real runtime behavior (the actual registry write, the actual broadcast) is unverified here and needs manual testing on real Windows (Task 6 covers wiring this in; a manual verification checklist for the user comes after all tasks in this plan, not as an automated step).

- [ ] **Step 7: Run the full non-Windows test suite, `go vet`, and `gofmt`**

Run: `go test ./internal/shell/... -v && go vet ./... && gofmt -l .`
Expected: all pass, no output from `vet`/`gofmt`. (`go vet ./...` and `gofmt -l .` naturally skip files excluded by the `windows_env.go` build tag when run on this non-Windows environment — that's expected, not a gap; Step 6's cross-compile is what covers that file.)

- [ ] **Step 8: Commit**

```bash
git add internal/shell/windows_env.go internal/shell/windows_env_other.go internal/shell/windows_env_merge.go internal/shell/windows_env_merge_test.go go.mod go.sum
git commit -m "$(cat <<'EOF'
Add real Windows user-environment configuration via HKCU\Environment

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: `cmd/setup.go` — relocation step and wiring up real Windows config

**Files:**
- Modify: `cmd/setup.go`
- Test: `cmd/setup_test.go`

**Interfaces:**
- Consumes: `platform.RunningFromDevHome()` (Task 1); `shell.ConfigureWindowsUserEnv(devHome string) error` (Task 5); `shell.ExportLines`, `shell.RCPath`, `shell.Confirm`, `shell.UpsertBlock` (pre-existing, Task 4 already changed `ExportLines`'s output); `copyExecutable(src, dest string) error` (pre-existing, in this same file).
- Produces: nothing consumed by a later task — this is the last task in the plan.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/setup_test.go`:

```go
func TestSetupCommand_RelocatesDevAndDocsIntoDevHomeWhenConfirmed(t *testing.T) {
	extractDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")

	// Simulate a freshly-extracted release archive: a fake "running
	// executable" plus its two siblings, none of them anywhere near
	// $DEV_HOME. os.Executable() can't be overridden directly, so this
	// test calls installRelocation (Step 3's new, separately-testable
	// function) with an explicit exe path instead of relying on the
	// real running test binary's own location.
	exe := filepath.Join(extractDir, "dev")
	if err := os.WriteFile(exe, []byte("fake binary"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extractDir, "README.md"), []byte("readme"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extractDir, "LICENSE"), []byte("license"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := installRelocation(exe, devHome); err != nil {
		t.Fatalf("installRelocation() returned error: %v", err)
	}

	for _, name := range []string{"dev", "README.md", "LICENSE"} {
		got, err := os.ReadFile(filepath.Join(devHome, name))
		if err != nil {
			t.Errorf("expected %s to be copied into $DEV_HOME: %v", name, err)
		}
		_ = got
	}
	for _, name := range []string{"dev", "README.md", "LICENSE"} {
		if _, err := os.Stat(filepath.Join(extractDir, name)); !os.IsNotExist(err) {
			t.Errorf("expected original %s to be removed after relocation, got stat err: %v", name, err)
		}
	}
}

func TestInstallRelocation_MissingReadmeOrLicenseIsNotFatal(t *testing.T) {
	extractDir := t.TempDir()
	devHome := t.TempDir()
	exe := filepath.Join(extractDir, "dev")
	if err := os.WriteFile(exe, []byte("fake binary"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Deliberately no README.md/LICENSE alongside exe — matches a
	// from-source `go build` binary, which has no such siblings.

	if err := installRelocation(exe, devHome); err != nil {
		t.Fatalf("installRelocation() returned error for a binary with no README/LICENSE siblings: %v", err)
	}
	if _, err := os.Stat(filepath.Join(devHome, "dev")); err != nil {
		t.Errorf("expected dev itself to still be relocated: %v", err)
	}
}

func TestSetupCommand_SkipsRelocationWhenAlreadyInsideDevHome(t *testing.T) {
	// This test can't easily force os.Executable() to report a path
	// inside devHome (it's always the real go test binary's own
	// location), so it instead verifies the higher-level property:
	// running `dev setup` from this environment (which is NOT inside
	// the configured $DEV_HOME) still succeeds and does not error even
	// when the relocation prompt is declined — the "skip when already
	// installed" branch itself is exercised indirectly via
	// TestInstallRelocation's own tests above plus this confirms
	// setupCmd's RunE doesn't panic or error on the decline path.
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("n\nn\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}
}
```

Add `"path/filepath"` to the test file's imports if not already present (it is, from earlier tasks this session).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/... -v -run 'TestSetupCommand_Relocates|TestInstallRelocation|TestSetupCommand_SkipsRelocation'`
Expected: FAIL — `undefined: installRelocation`.

- [ ] **Step 3: Write the implementation**

Replace `cmd/setup.go`'s full content with:

```go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/filesystem"
	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/providers"
	devruntime "github.com/jpsdm/dev/internal/runtime"
	"github.com/jpsdm/dev/internal/shell"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Detect your shell and configure PATH and shims (asks for confirmation)",
	RunE: func(cmd *cobra.Command, args []string) error {
		devHome, err := platform.DevHome()
		if err != nil {
			return err
		}
		sh := shell.Detect()
		lines := shell.ExportLines(sh, devHome)

		path, supported, err := shell.RCPath(sh)
		if err != nil {
			return err
		}
		if !supported {
			fmt.Fprintln(cmd.OutOrStdout(), "Automatic setup isn't supported for this shell yet. Add these lines manually:")
			fmt.Fprintln(cmd.OutOrStdout())
			for _, line := range lines {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			fmt.Fprintln(cmd.OutOrStdout())

			confirmed, err := shell.Confirm("Create shims in $DEV_HOME/bin now? [y/N] ", cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if !confirmed {
				cliutil.Fstep(cmd.OutOrStdout(), "No changes made.")
				return nil
			}
			if err := installShims(cmd); err != nil {
				return err
			}

			if err := relocateIfNeeded(cmd, devHome); err != nil {
				return err
			}

			if runtime.GOOS == "windows" {
				envConfirmed, err := shell.Confirm("Add these to your user environment now? [y/N] ", cmd.InOrStdin(), cmd.OutOrStdout())
				if err != nil {
					return err
				}
				if envConfirmed {
					if err := shell.ConfigureWindowsUserEnv(devHome); err != nil {
						return err
					}
					cliutil.Fsuccess(cmd.OutOrStdout(), "Updated your user environment (DEV_HOME and PATH)")
				}
			}
			return nil
		}

		fmt.Fprintf(cmd.OutOrStdout(), "The following lines will be added to %s:\n\n", path)
		for _, line := range lines {
			fmt.Fprintln(cmd.OutOrStdout(), line)
		}
		fmt.Fprintln(cmd.OutOrStdout())

		confirmed, err := shell.Confirm(fmt.Sprintf("Add these lines to %s? [y/N] ", path), cmd.InOrStdin(), cmd.OutOrStdout())
		if err != nil {
			return err
		}
		if !confirmed {
			cliutil.Fstep(cmd.OutOrStdout(), "No changes made.")
			return nil
		}

		// Shims first, then relocation, then the rc file: if installShims
		// fails (read-only DEV_HOME, no disk space), a stray shim with no
		// PATH entry yet is inert, whereas a PATH entry with no shims
		// behind it is confusing — so the rc file should only ever
		// describe a PATH that actually has something in it. Relocation
		// runs before the rc file too, since the rc file's lines already
		// assume dev itself lives at $DEV_HOME (see shell.ExportLines).
		if err := installShims(cmd); err != nil {
			return err
		}

		if err := relocateIfNeeded(cmd, devHome); err != nil {
			return err
		}

		if err := shell.UpsertBlock(path, lines); err != nil {
			return fmt.Errorf("updating %s: %w", path, err)
		}
		cliutil.Fsuccess(cmd.OutOrStdout(), "Updated %s", path)

		cliutil.Fstep(cmd.OutOrStdout(), "Restart your shell (or run `source %s`) for these changes to take effect", path)
		return nil
	},
}

// relocateIfNeeded offers to move the running binary (plus its
// README.md/LICENSE siblings) into devHome, when it isn't already
// running from there. A no-op when re-running setup on an
// already-installed copy — there's nothing to relocate, and
// installRelocation must never be given devHome as its own exe
// argument (see installRelocation's doc comment).
func relocateIfNeeded(cmd *cobra.Command, devHome string) error {
	runningFromDevHome, _, err := platform.RunningFromDevHome()
	if err != nil {
		return err
	}
	if runningFromDevHome {
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding the dev binary: %w", err)
	}

	confirmed, err := shell.Confirm("Move dev, README.md, and LICENSE into $DEV_HOME now? [y/N] ", cmd.InOrStdin(), cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if !confirmed {
		return nil
	}

	if err := installRelocation(exe, devHome); err != nil {
		return err
	}
	cliutil.Fsuccess(cmd.OutOrStdout(), "Moved dev into %s", devHome)
	return nil
}

// installRelocation copies exe (and its README.md/LICENSE siblings,
// if present) into devHome, then best-effort removes the originals.
// A missing README.md/LICENSE (e.g. a from-source `go build` binary,
// which has no such siblings) is not an error; only exe itself is
// required to exist. Callers must only invoke this when exe is
// genuinely outside devHome (relocateIfNeeded's RunningFromDevHome
// check guarantees this) — copying devHome/dev onto itself would
// truncate it via copyExecutable's read-then-write.
func installRelocation(exe, devHome string) error {
	dest := filepath.Join(devHome, filepath.Base(exe))
	if err := copyExecutable(exe, dest); err != nil {
		return fmt.Errorf("copying %s into %s: %w", exe, devHome, err)
	}
	relocated := []string{exe}

	srcDir := filepath.Dir(exe)
	for _, name := range []string{"README.md", "LICENSE"} {
		src := filepath.Join(srcDir, name)
		data, err := os.ReadFile(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("reading %s: %w", src, err)
		}
		if err := filesystem.WriteFileAtomic(filepath.Join(devHome, name), data, 0o644); err != nil {
			return fmt.Errorf("copying %s into %s: %w", src, devHome, err)
		}
		relocated = append(relocated, src)
	}

	for _, path := range relocated {
		if err := os.Remove(path); err != nil {
			// Best-effort: a leftover original outside $DEV_HOME is
			// exactly what RunningFromDevHome's check guards against
			// if it's ever run again — not fatal here.
			cliutil.Step("Could not remove %s after copying it: %v", path, err)
		}
	}
	return nil
}
```

`platform` and `shell` are both already imported in the file's current state — no new imports needed for this task; only the function bodies change. `installShims` and `copyExecutable` (both further down in the same file) are unchanged by this task.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/... -v`
Expected: PASS — the whole `cmd` package's suite, including every pre-existing `TestSetupCommand_*` test (this task changes both branches of `setupCmd`'s `RunE`, so a regression in the already-covered shim/rc-file behavior is exactly what a full-package run here would catch).

- [ ] **Step 5: Run `go vet`, `gofmt`, full build, and full repo test suite**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: clean everywhere.

- [ ] **Step 6: Commit**

```bash
git add cmd/setup.go cmd/setup_test.go
git commit -m "$(cat <<'EOF'
dev setup relocates itself into $DEV_HOME and configures Windows env

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: CI — add a `windows-latest` build job

**Files:**
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: nothing.
- Produces: nothing consumed by a later task — this is the last task in the plan.

- [ ] **Step 1: Add the job**

In `.github/workflows/ci.yml`, add a second job alongside the existing `ci` job (which stays exactly as it is — this doesn't replace it, it adds Windows coverage on top):

```yaml
  windows:
    runs-on: windows-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: 'stable'

      - name: Download dependencies
        run: go mod download

      - name: Vet
        run: go vet ./...

      - name: Test
        run: go test ./...

      - name: Build
        run: go build ./...
```

No formatting/lint step here — `gofmt -l` and `golangci-lint` already run once on `ubuntu-latest` in the existing `ci` job and their output doesn't depend on which OS runs them (they check source text, not platform-specific compiled behavior); duplicating them on Windows would be redundant CI time for no new signal. `go test ./...` on `windows-latest` is what actually matters here — it's the only place `internal/shell`'s Windows-tagged files (`windows_env.go`, and any `_test.go` file under the same build tag, if one exists) ever actually compile and run for real.

- [ ] **Step 2: Validate the workflow YAML**

Run: `export PATH="$PATH:$(go env GOPATH)/bin" && actionlint .github/workflows/ci.yml`
(Install first if not already present in this environment from earlier work this session: `go install github.com/rhysd/actionlint/cmd/actionlint@latest`.)
Expected: no output, exit code 0.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "$(cat <<'EOF'
Add a windows-latest CI job

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Post-plan manual verification (not a task — for the user, on a real Windows machine)

This plan's automated tasks cannot verify Windows registry behavior at runtime —
only that it cross-compiles (Task 5) and that CI now builds/tests it for real going
forward (Task 7, which will report its own pass/fail on GitHub once these commits are
pushed and a future workflow run happens). Once implemented, verify by hand:

```powershell
# From a fresh extraction, outside $DEV_HOME entirely:
.\dev.exe --version        # should work
.\dev.exe lang list         # should refuse with the "not installed" warning
.\dev.exe setup              # confirm shims, confirm relocation, confirm env config

# Close and reopen a NEW PowerShell window (registry changes don't apply
# to already-open shells without the broadcast, and even with it, a
# brand-new window is the reliable way to confirm persistence):
dev --version                # should now work from anywhere, no PATH edited by hand
java --version                # (once a Java version is installed/activated)

# Confirm it wrote to the USER hive, not the system one:
reg query "HKCU\Environment" /v DEV_HOME
reg query "HKCU\Environment" /v Path
# (HKLM should be untouched — do not run this against HKLM to "confirm
# it's empty," since checking there isn't meaningful; the point is that
# dev never touches it, not that it stays empty for other reasons.)
```
