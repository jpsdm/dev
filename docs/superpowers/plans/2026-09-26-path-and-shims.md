# PATH and Shims Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `~/.dev/bin` on `PATH` sufficient to run whatever Node.js version is currently active, via `dev env` (print instructions), `dev setup` (detect shell, confirm, write rc file + create shims), and a shim dispatch mechanism that works for any current or future language provider.

**Architecture:** The `Runtime` interface gains `ShimNames()`/`BinaryPath()` so each provider owns its own shim-binary-name and on-disk-layout knowledge. A new `internal/providers` package is the single place that lists registered providers, consumed by both `cmd/lang.go` (already existing) and the new `internal/shim` package. `main.go` inspects its own invocation name to decide whether to run the normal CLI or act as a shim; shimming replaces the current process (`syscall.Exec` on Unix, a child process + exit-code passthrough on Windows) rather than merely wrapping it.

**Tech Stack:** Go stdlib only (`syscall`, `os/exec`, `bufio`) — no new third-party dependency. `dev setup`'s confirmation prompt is a plain stdlib y/N read, not Huh, since a single confirmation doesn't need a form library.

**Spec:** `docs/superpowers/specs/2026-09-26-path-and-shims-design.md`

## Global Constraints

- `Runtime` interface gains `ShimNames() []string` and `BinaryPath(versionDir, binName string) (string, error)`; every implementation and test stub must implement both to keep compiling.
- `internal/providers.Register(m *runtime.Manager)` is the single place that lists registered providers; `cmd/lang.go` and `internal/shim` both call it — neither registers providers any other way.
- Node's `ShimNames()` is exactly `["node", "npm", "npx"]`. `BinaryPath` resolves `bin/<name>` on Linux/macOS; on Windows, `node.exe` for `"node"`, otherwise `<name>.cmd`, both flat at the version directory's root (no `bin/` subdirectory on Windows — verified against Node's real distribution layout).
- Process replacement: `syscall.Exec` on Unix (`//go:build unix`), `os/exec` + `os.Exit` with the child's exact exit code on Windows (`//go:build windows`) — no cross-platform abstraction beyond the shared `replaceProcess(path string, args []string) error` signature.
- `dev setup`'s confirmation prompt is a plain stdlib `bufio.Scanner` read (`y`/`yes` case-insensitive = yes, everything else = no) — no Huh or other prompt library.
- `dev setup` automatically edits an rc file only for bash/zsh/fish; PowerShell gets print-only instructions (identical to `dev env`'s output), never an automatic profile write.
- Shell rc block insertion uses a `# BEGIN dev shell setup` / `# END dev shell setup` marker pair and is idempotent (replaces the block in place on a second run, never duplicates it).
- Shims are created by copying the currently-running `dev` binary (via `os.Executable()`) to `<DEV_HOME>/bin/<name>` for every registered provider's `ShimNames()` — never symlinks or hardlinks, matching this project's established "no symlinks anywhere" precedent from the `current/<lang>` marker-file design.
- No test makes a real network call, writes to a real shell rc file, or depends on the real `$HOME`/`$DEV_HOME` — all such tests use `t.TempDir()` plus `t.Setenv("HOME", ...)`/`t.Setenv("DEV_HOME", ...)`.
- No test using `t.Setenv` also calls `t.Parallel()` on itself.
- No Testify — stdlib `testing` only.

## Review Focus

- The real `syscall.Exec` path on Unix must be exercised by an actual subprocess-based test, not mocked — a wrong exec call would silently break every shimmed command in production while still looking fine under a superficial unit test — Task 4.
- `dev setup` declining the confirmation prompt must make zero filesystem changes: no rc file created or modified, no shim files created — Task 7.
- `dev setup` run twice in a row must not duplicate its block in the rc file — Task 3 (the underlying `UpsertBlock`) and Task 7 (the command-level behavior).
- A shim invoked for a language with no active version must fail with a specific, actionable message (which `dev lang use` command to run) — never a generic "file not found" or a panic — Task 4.
- Windows's flat, `.cmd`-suffixed binary layout must be distinguished from Unix's `bin/` layout by a real, dedicated test — even though only one branch can execute on any single CI machine, both must have their own test rather than one implicitly covering the other — Task 1.

---

## Task 1: Extend `Runtime` interface + Node.js provider implementation + fix stubs

**Files:**
- Modify: `internal/runtime/runtime.go`
- Modify: `internal/runtime/runtime_test.go`
- Modify: `internal/runtime/node/node.go`
- Modify: `internal/runtime/node/node_test.go`
- Modify: `cmd/lang_test.go`

**Interfaces:**
- Consumes: nothing beyond what Tasks from sub-project 2a already established.
- Produces: `runtime.Runtime` interface with two new methods, `(*Node).ShimNames() []string`, `(*Node).BinaryPath(versionDir, binName string) (string, error)` — consumed by `internal/shim` (Task 4) and `cmd/setup.go` (Task 7).

This task must land as one unit: extending the interface without updating its only real implementation and both test stubs in the same change would leave the module in a state that doesn't compile.

- [ ] **Step 1: Add the two methods to the `Runtime` interface**

Edit `internal/runtime/runtime.go`, adding to the `Runtime` interface (after the existing `Activate` method):

```go
	// ShimNames returns the binary names this provider's installed
	// versions expose (e.g. ["node", "npm", "npx"] for Node.js). dev
	// setup creates a shim copy for each name returned by every
	// registered provider.
	ShimNames() []string

	// BinaryPath resolves the absolute path to binName inside an
	// installed version at versionDir (e.g. "<DEV_HOME>/versions/node/22").
	// Each provider owns its own on-disk layout knowledge here.
	BinaryPath(versionDir, binName string) (string, error)
```

- [ ] **Step 2: Confirm the expected compile failures**

Run: `go build ./...`
Expected: FAIL — `*stubRuntime does not implement runtime.Runtime (missing method ShimNames)` (or similar) in both `internal/runtime/runtime_test.go` and `cmd/lang_test.go`, and `*Node does not implement runtime.Runtime` from `internal/runtime/node/node_test.go`'s compile-time assertion `var _ devruntime.Runtime = (*Node)(nil)`.

- [ ] **Step 3: Fix `internal/runtime/runtime_test.go`'s stub**

Add to `stubRuntime`'s method set in `internal/runtime/runtime_test.go`:

```go
func (s *stubRuntime) ShimNames() []string { return nil }
func (s *stubRuntime) BinaryPath(versionDir, binName string) (string, error) {
	return "", nil
}
```

- [ ] **Step 4: Fix `cmd/lang_test.go`'s stub**

Add the same two methods to `stubRuntime`'s method set in `cmd/lang_test.go` (this file's `stubRuntime` already has `installCalled`/`uninstallCalled`/`activateCalled` fields from an earlier fix round — add these two methods alongside the existing ones, matching the existing method style in that file):

```go
func (s *stubRuntime) ShimNames() []string { return nil }
func (s *stubRuntime) BinaryPath(versionDir, binName string) (string, error) {
	return "", nil
}
```

- [ ] **Step 5: Write the failing tests for Node's new methods**

Append to `internal/runtime/node/node_test.go`:

```go
func TestShimNames(t *testing.T) {
	t.Parallel()
	n := New()
	names := n.ShimNames()
	want := map[string]bool{"node": true, "npm": true, "npx": true}
	if len(names) != len(want) {
		t.Fatalf("ShimNames() = %v, want exactly %v", names, want)
	}
	for _, name := range names {
		if !want[name] {
			t.Errorf("ShimNames() included unexpected %q", name)
		}
	}
}

func TestBinaryPath_UnixLayout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test targets the Unix (bin/) layout")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	nodeBin := filepath.Join(binDir, "node")
	if err := os.WriteFile(nodeBin, []byte("fake"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	n := New()
	got, err := n.BinaryPath(dir, "node")
	if err != nil {
		t.Fatalf("BinaryPath() returned error: %v", err)
	}
	if got != nodeBin {
		t.Errorf("BinaryPath() = %q, want %q", got, nodeBin)
	}
}

func TestBinaryPath_WindowsLayout(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("this test targets the Windows (flat, .cmd) layout")
	}
	dir := t.TempDir()
	nodeExe := filepath.Join(dir, "node.exe")
	if err := os.WriteFile(nodeExe, []byte("fake"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	npmCmd := filepath.Join(dir, "npm.cmd")
	if err := os.WriteFile(npmCmd, []byte("fake"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	n := New()
	gotNode, err := n.BinaryPath(dir, "node")
	if err != nil {
		t.Fatalf("BinaryPath(node) returned error: %v", err)
	}
	if gotNode != nodeExe {
		t.Errorf("BinaryPath(node) = %q, want %q", gotNode, nodeExe)
	}

	gotNpm, err := n.BinaryPath(dir, "npm")
	if err != nil {
		t.Fatalf("BinaryPath(npm) returned error: %v", err)
	}
	if gotNpm != npmCmd {
		t.Errorf("BinaryPath(npm) = %q, want %q", gotNpm, npmCmd)
	}
}

func TestBinaryPath_MissingBinaryReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	n := New()

	_, err := n.BinaryPath(dir, "node")
	if err == nil {
		t.Fatal("BinaryPath() returned nil error for a binary that doesn't exist")
	}
}
```

Add `"os"` and `"path/filepath"` to this test file's imports if not already present (they should already be present from earlier tasks in sub-project 2a).

- [ ] **Step 6: Run tests to verify they fail**

Run: `go test ./internal/runtime/node/... -run 'TestShimNames|TestBinaryPath' -v`
Expected: FAIL — `undefined: (*Node).ShimNames` / `undefined: (*Node).BinaryPath`.

- [ ] **Step 7: Implement Node's `ShimNames` and `BinaryPath`**

Add to `internal/runtime/node/node.go`:

```go
// ShimNames returns the Node.js binaries dev creates shims for.
func (n *Node) ShimNames() []string {
	return []string{"node", "npm", "npx"}
}

// BinaryPath resolves binName inside an installed Node.js version.
// Node's Unix tarballs put binaries in bin/; its Windows zip extracts
// flat with .cmd wrappers at the version root (verified live against
// nodejs.org's real distribution layout).
func (n *Node) BinaryPath(versionDir, binName string) (string, error) {
	osName, err := nodeOS()
	if err != nil {
		return "", err
	}

	var candidate string
	if osName == "win" {
		if binName == "node" {
			candidate = filepath.Join(versionDir, "node.exe")
		} else {
			candidate = filepath.Join(versionDir, binName+".cmd")
		}
	} else {
		candidate = filepath.Join(versionDir, "bin", binName)
	}

	if !filesystem.Exists(candidate) {
		return "", fmt.Errorf("%s not found in Node.js installation at %s", binName, versionDir)
	}
	return candidate, nil
}
```

`filepath`, `fmt`, and `filesystem` should already be imported in `node.go` from earlier tasks.

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/runtime/node/... -v`
Expected: PASS for all tests (the Windows-layout test will `SKIP` on this machine if it's not Windows — that's expected, not a failure).

- [ ] **Step 9: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed — the interface extension compiles everywhere now.

- [ ] **Step 10: Commit**

```bash
git add internal/runtime/runtime.go internal/runtime/runtime_test.go internal/runtime/node/node.go internal/runtime/node/node_test.go cmd/lang_test.go
git commit -m "Add ShimNames and BinaryPath to the Runtime interface"
```

---

## Task 2: `internal/providers` — the single place that lists providers

**Files:**
- Create: `internal/providers/providers.go`
- Test: `internal/providers/providers_test.go`
- Modify: `cmd/lang.go`

**Interfaces:**
- Consumes: `runtime.Manager`, `runtime.NewManager()`, `(*Manager).Register` (sub-project 2a), `node.New()` (sub-project 2a).
- Produces: `providers.Register(m *runtime.Manager)` — consumed by `cmd/lang.go` (this task) and `internal/shim` (Task 4).

- [ ] **Step 1: Write the failing test**

Create `internal/providers/providers_test.go`:

```go
package providers

import (
	"testing"

	"github.com/jpsdm/dev/internal/runtime"
)

func TestRegister_RegistersNode(t *testing.T) {
	t.Parallel()
	m := runtime.NewManager()
	Register(m)

	r, ok := m.Get("node")
	if !ok {
		t.Fatal(`Get("node") ok = false after Register(), want true`)
	}
	if r.Name() != "node" {
		t.Errorf(`Get("node").Name() = %q, want "node"`, r.Name())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/providers/... -v`
Expected: FAIL — the package/directory doesn't exist yet (build error).

- [ ] **Step 3: Write the implementation**

Create `internal/providers/providers.go`:

```go
// Package providers is the single place that lists every Runtime
// provider dev knows about, so command wiring (cmd/lang.go) and shim
// dispatch (internal/shim) can never register a different set.
package providers

import (
	"github.com/jpsdm/dev/internal/runtime"
	"github.com/jpsdm/dev/internal/runtime/node"
)

// Register adds every known Runtime provider to m.
func Register(m *runtime.Manager) {
	m.Register(node.New())
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/providers/... -v`
Expected: PASS.

- [ ] **Step 5: Wire `cmd/lang.go` to use it**

In `cmd/lang.go`, find the `init()` function that currently does:

```go
func init() {
	langManager.Register(node.New())
	rootCmd.AddCommand(langCmd)
```

Replace the registration line and update imports:

```go
func init() {
	providers.Register(langManager)
	rootCmd.AddCommand(langCmd)
```

In the import block, remove `"github.com/jpsdm/dev/internal/runtime/node"` (no longer used directly in this file) and add `"github.com/jpsdm/dev/internal/providers"`.

- [ ] **Step 6: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed; `cmd` package's existing `TestLang*` tests pass unchanged (the registered provider set is identical, only where the registration call lives changed).

- [ ] **Step 7: Commit**

```bash
git add internal/providers cmd/lang.go
git commit -m "Add internal/providers as the single source of registered providers"
```

---

## Task 3: `internal/shell` — detection, export lines, idempotent rc editing

**Files:**
- Create: `internal/shell/shell.go`
- Test: `internal/shell/shell_test.go`

**Interfaces:**
- Consumes: nothing beyond stdlib.
- Produces: `shell.Shell` (type, with `Unknown`/`Bash`/`Zsh`/`Fish`/`PowerShell` constants), `shell.Detect() Shell`, `shell.ExportLines(sh Shell, devHome string) []string`, `shell.RCPath(sh Shell) (path string, supported bool)`, `shell.UpsertBlock(path string, lines []string) error`, `shell.Confirm(prompt string, in io.Reader, out io.Writer) (bool, error)` — all consumed by `cmd/env.go` (Task 6) and `cmd/setup.go` (Task 7).

- [ ] **Step 1: Write the failing tests**

Create `internal/shell/shell_test.go`:

```go
package shell

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetect_UsesShellEnvVar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Detect() always reports PowerShell on windows")
	}
	t.Setenv("SHELL", "/bin/zsh")
	if got := Detect(); got != Zsh {
		t.Errorf("Detect() = %v, want Zsh", got)
	}
}

func TestDetect_UnknownShellFallsBackToUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Detect() always reports PowerShell on windows")
	}
	t.Setenv("SHELL", "/bin/tcsh")
	if got := Detect(); got != Unknown {
		t.Errorf("Detect() = %v, want Unknown", got)
	}
}

func TestExportLines_Bash(t *testing.T) {
	t.Parallel()
	lines := ExportLines(Bash, "/home/user/.dev")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `export DEV_HOME="/home/user/.dev"`) {
		t.Errorf("ExportLines(Bash) = %v, want it to contain the DEV_HOME export", lines)
	}
	if !strings.Contains(joined, `export PATH="$DEV_HOME/bin:$PATH"`) {
		t.Errorf("ExportLines(Bash) = %v, want it to contain the PATH export", lines)
	}
}

func TestExportLines_Fish(t *testing.T) {
	t.Parallel()
	lines := ExportLines(Fish, "/home/user/.dev")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "set -gx DEV_HOME /home/user/.dev") {
		t.Errorf("ExportLines(Fish) = %v, want the fish-syntax DEV_HOME export", lines)
	}
}

func TestExportLines_PowerShellDoesNotBackslashEscapeThePath(t *testing.T) {
	t.Parallel()
	// A real Windows path contains backslashes. Go's %q would escape
	// them as \\, which is wrong inside a PowerShell double-quoted
	// string — this test pins that ExportLines does not make that
	// mistake.
	lines := ExportLines(PowerShell, `C:\Users\foo\.dev`)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `$env:DEV_HOME = "C:\Users\foo\.dev"`) {
		t.Errorf(`ExportLines(PowerShell) = %v, want it to contain an unescaped $env:DEV_HOME = "C:\Users\foo\.dev"`, lines)
	}
	if strings.Contains(joined, `\\`) {
		t.Errorf("ExportLines(PowerShell) = %v, backslashes must not be doubled", lines)
	}
}

func TestRCPath_Bash(t *testing.T) {
	t.Parallel()
	path, supported := RCPath(Bash)
	if !supported {
		t.Fatal("RCPath(Bash) supported = false, want true")
	}
	if !strings.HasSuffix(path, ".bashrc") {
		t.Errorf("RCPath(Bash) = %q, want it to end in .bashrc", path)
	}
}

func TestRCPath_PowerShellUnsupported(t *testing.T) {
	t.Parallel()
	_, supported := RCPath(PowerShell)
	if supported {
		t.Error("RCPath(PowerShell) supported = true, want false")
	}
}

func TestUpsertBlock_CreatesNewFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "rc")

	if err := UpsertBlock(path, []string{"export FOO=bar"}); err != nil {
		t.Fatalf("UpsertBlock() returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading result: %v", err)
	}
	content := string(got)
	if !strings.Contains(content, "# BEGIN dev shell setup") ||
		!strings.Contains(content, "export FOO=bar") ||
		!strings.Contains(content, "# END dev shell setup") {
		t.Errorf("UpsertBlock() wrote %q, missing expected markers/content", content)
	}
}

func TestUpsertBlock_PreservesUnrelatedContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "rc")
	if err := os.WriteFile(path, []byte("alias ll='ls -la'\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := UpsertBlock(path, []string{"export FOO=bar"}); err != nil {
		t.Fatalf("UpsertBlock() returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading result: %v", err)
	}
	content := string(got)
	if !strings.Contains(content, "alias ll='ls -la'") {
		t.Errorf("UpsertBlock() lost unrelated existing content: %q", content)
	}
	if !strings.Contains(content, "export FOO=bar") {
		t.Errorf("UpsertBlock() did not add the new block: %q", content)
	}
}

func TestUpsertBlock_ReplacesExistingBlockWithoutDuplicating(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "rc")

	if err := UpsertBlock(path, []string{"export FOO=old"}); err != nil {
		t.Fatalf("first UpsertBlock() returned error: %v", err)
	}
	if err := UpsertBlock(path, []string{"export FOO=new"}); err != nil {
		t.Fatalf("second UpsertBlock() returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading result: %v", err)
	}
	content := string(got)
	if strings.Count(content, "# BEGIN dev shell setup") != 1 {
		t.Errorf("UpsertBlock() duplicated the marker block: %q", content)
	}
	if strings.Contains(content, "export FOO=old") {
		t.Errorf("UpsertBlock() left the old block content behind: %q", content)
	}
	if !strings.Contains(content, "export FOO=new") {
		t.Errorf("UpsertBlock() did not write the new block content: %q", content)
	}
}

func TestConfirm_AcceptsYAndYes(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"y", "Y", "yes", "YES", " y \n"} {
		var out bytes.Buffer
		got, err := Confirm("prompt: ", strings.NewReader(input), &out)
		if err != nil {
			t.Fatalf("Confirm(%q) returned error: %v", input, err)
		}
		if !got {
			t.Errorf("Confirm(%q) = false, want true", input)
		}
	}
}

func TestConfirm_RejectsAnythingElse(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"n", "no", "", "maybe"} {
		var out bytes.Buffer
		got, err := Confirm("prompt: ", strings.NewReader(input), &out)
		if err != nil {
			t.Fatalf("Confirm(%q) returned error: %v", input, err)
		}
		if got {
			t.Errorf("Confirm(%q) = true, want false", input)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/shell/... -v`
Expected: FAIL — the package/directory doesn't exist yet.

- [ ] **Step 3: Write the implementation**

Create `internal/shell/shell.go`:

```go
// Package shell detects the user's shell, generates the PATH export
// lines dev needs, and can idempotently insert/replace a marked block in
// a shell rc file.
package shell

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Shell identifies a supported shell.
type Shell int

const (
	Unknown Shell = iota
	Bash
	Zsh
	Fish
	PowerShell
)

const (
	blockBegin = "# BEGIN dev shell setup"
	blockEnd   = "# END dev shell setup"
)

// Detect identifies the current shell from $SHELL (Unix) or reports
// PowerShell on Windows (this project's only supported Windows shell
// target for now).
func Detect() Shell {
	if runtime.GOOS == "windows" {
		return PowerShell
	}
	switch filepath.Base(os.Getenv("SHELL")) {
	case "bash":
		return Bash
	case "zsh":
		return Zsh
	case "fish":
		return Fish
	default:
		return Unknown
	}
}

// ExportLines returns the lines dev env prints / dev setup would insert,
// in sh's own syntax. Unknown falls back to POSIX sh-compatible syntax
// (the same as Bash/Zsh), since that's the most broadly interpretable
// default when the shell couldn't be identified.
func ExportLines(sh Shell, devHome string) []string {
	switch sh {
	case Fish:
		return []string{
			fmt.Sprintf("set -gx DEV_HOME %s", devHome),
			`set -gx PATH $DEV_HOME/bin $PATH`,
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
			`$env:PATH = "$env:DEV_HOME\bin;$env:PATH"`,
		}
	default: // Bash, Zsh, Unknown
		return []string{
			fmt.Sprintf(`export DEV_HOME=%q`, devHome),
			`export PATH="$DEV_HOME/bin:$PATH"`,
		}
	}
}

// RCPath returns the rc file dev setup would edit for sh, and whether
// automatic editing is supported for it. PowerShell and Unknown report
// unsupported — PowerShell because its real profile path can only be
// known authoritatively by asking PowerShell itself, and guessing wrong
// risks writing a file it never loads; Unknown because there's no rc
// file to safely guess for an unidentified shell.
func RCPath(sh Shell) (path string, supported bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	switch sh {
	case Bash:
		return filepath.Join(home, ".bashrc"), true
	case Zsh:
		return filepath.Join(home, ".zshrc"), true
	case Fish:
		return filepath.Join(home, ".config", "fish", "config.fish"), true
	default: // PowerShell, Unknown
		return "", false
	}
}

// UpsertBlock writes lines into path between a marker pair, replacing an
// existing block if one is present or appending a new one if not.
// Creates path's parent directory and the file itself if neither exists.
func UpsertBlock(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", path, err)
	}

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	block := append([]string{blockBegin}, lines...)
	block = append(block, blockEnd)

	var out []string
	if len(existing) > 0 {
		fileLines := strings.Split(strings.TrimRight(string(existing), "\n"), "\n")
		inBlock := false
		replaced := false
		for _, line := range fileLines {
			switch {
			case line == blockBegin:
				inBlock = true
				out = append(out, block...)
				replaced = true
			case line == blockEnd:
				inBlock = false
			case inBlock:
				// skip old block content
			default:
				out = append(out, line)
			}
		}
		if !replaced {
			out = append(out, block...)
		}
	} else {
		out = block
	}

	content := strings.Join(out, "\n") + "\n"
	return os.WriteFile(path, []byte(content), 0o644)
}

// Confirm prompts with prompt, reads one line from in, and reports
// whether it was an affirmative response ("y" or "yes", case
// insensitive) — anything else, including EOF or an empty line, is "no".
func Confirm(prompt string, in io.Reader, out io.Writer) (bool, error) {
	fmt.Fprint(out, prompt)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, fmt.Errorf("reading confirmation: %w", err)
		}
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes", nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/shell/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/shell
git commit -m "Add internal/shell for detection, export lines, and idempotent rc editing"
```

---

## Task 4: `internal/shim` — dispatch and process replacement

**Files:**
- Create: `internal/shim/shim.go`
- Create: `internal/shim/exec_unix.go`
- Create: `internal/shim/exec_windows.go`
- Test: `internal/shim/shim_test.go`

**Interfaces:**
- Consumes: `runtime.NewManager()`, `(*Manager).Names()`, `(*Manager).Get(name string) (Runtime, bool)` (sub-project 2a), `providers.Register` (Task 2), `platform.VersionsDir()` (sub-project 1), `Runtime.ShimNames()`, `Runtime.CurrentVersion()`, `Runtime.BinaryPath(versionDir, binName string) (string, error)` (Task 1).
- Produces: `shim.Run(binName string, args []string) error` — consumed by `main.go` (Task 5).

- [ ] **Step 1: Write the failing tests**

Create `internal/shim/shim_test.go`:

```go
package shim

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	devruntime "github.com/jpsdm/dev/internal/runtime"
)

type stubRuntime struct {
	name       string
	shimNames  []string
	current    *devruntime.Version
	binaryPath string
	binaryErr  error
}

func (s *stubRuntime) Name() string { return s.name }
func (s *stubRuntime) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error) {
	return nil, nil
}
func (s *stubRuntime) ListInstalledVersions() ([]devruntime.Version, error) { return nil, nil }
func (s *stubRuntime) CurrentVersion() (*devruntime.Version, error)         { return s.current, nil }
func (s *stubRuntime) Install(ctx context.Context, name string) error      { return nil }
func (s *stubRuntime) Uninstall(name string) error                         { return nil }
func (s *stubRuntime) Activate(name string) error                          { return nil }
func (s *stubRuntime) ShimNames() []string                                 { return s.shimNames }
func (s *stubRuntime) BinaryPath(versionDir, binName string) (string, error) {
	return s.binaryPath, s.binaryErr
}

func TestResolve_UnknownBinNameErrors(t *testing.T) {
	t.Parallel()
	m := devruntime.NewManager()
	m.Register(&stubRuntime{name: "node", shimNames: []string{"node", "npm"}})

	_, err := resolve(m, "ruby")
	if err == nil {
		t.Fatal("resolve() returned nil error for an unregistered shim name")
	}
}

func TestResolve_NoActiveVersionErrors(t *testing.T) {
	t.Parallel()
	m := devruntime.NewManager()
	m.Register(&stubRuntime{name: "node", shimNames: []string{"node"}, current: nil})

	_, err := resolve(m, "node")
	if err == nil {
		t.Fatal("resolve() returned nil error when no version is active")
	}
	if !strings.Contains(err.Error(), "dev lang use") {
		t.Errorf("resolve() error = %v, want it to name the fix (`dev lang use`)", err)
	}
}

func TestResolve_ReturnsProviderBinaryPath(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	m := devruntime.NewManager()
	m.Register(&stubRuntime{
		name:       "node",
		shimNames:  []string{"node", "npm"},
		current:    &devruntime.Version{Name: "22"},
		binaryPath: "/fake/path/to/node",
	})

	got, err := resolve(m, "node")
	if err != nil {
		t.Fatalf("resolve() returned error: %v", err)
	}
	if got != "/fake/path/to/node" {
		t.Errorf("resolve() = %q, want %q", got, "/fake/path/to/node")
	}
}

func TestResolve_PropagatesBinaryPathError(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	m := devruntime.NewManager()
	m.Register(&stubRuntime{
		name:      "node",
		shimNames: []string{"node"},
		current:   &devruntime.Version{Name: "22"},
		binaryErr: errors.New("binary not found"),
	})

	_, err := resolve(m, "node")
	if err == nil {
		t.Fatal("resolve() returned nil error when BinaryPath failed")
	}
}

// TestReplaceProcess_ExecutesAndPropagatesExitCode verifies
// replaceProcess end-to-end by re-invoking this same test binary as a
// subprocess: the subprocess calls replaceProcess on a trivial helper
// program (built fresh with `go build` into a temp dir) and this test
// asserts on the subprocess's own exit code and output. This must run
// as a real subprocess because a successful syscall.Exec on Unix
// replaces the calling process image outright — calling replaceProcess
// directly from within `go test` would replace the test binary itself.
func TestReplaceProcess_ExecutesAndPropagatesExitCode(t *testing.T) {
	if os.Getenv("DEV_SHIM_TEST_HELPER") == "1" {
		dir, err := os.MkdirTemp("", "shim-helper")
		if err != nil {
			os.Exit(2)
		}
		defer os.RemoveAll(dir)

		src := filepath.Join(dir, "helper.go")
		if err := os.WriteFile(src, []byte(helperProgramSource), 0o644); err != nil {
			os.Exit(2)
		}
		bin := filepath.Join(dir, "helper")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		build := exec.Command("go", "build", "-o", bin, src)
		if out, err := build.CombinedOutput(); err != nil {
			os.Stderr.WriteString(string(out))
			os.Exit(2)
		}

		err = replaceProcess(bin, []string{"hello"})
		// On Unix, a successful replaceProcess never returns here. If we
		// get here, something went wrong.
		os.Stderr.WriteString(err.Error())
		os.Exit(3)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestReplaceProcess_ExecutesAndPropagatesExitCode")
	cmd.Env = append(os.Environ(), "DEV_SHIM_TEST_HELPER=1")
	out, err := cmd.CombinedOutput()

	if !strings.Contains(string(out), "hello from helper: [hello]") {
		t.Errorf("subprocess output = %q, want it to contain the helper's own output", out)
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("running subprocess: %v", err)
	}
	gotExit := 0
	if exitErr != nil {
		gotExit = exitErr.ExitCode()
	}
	if gotExit != 7 {
		t.Errorf("subprocess exit code = %d, want 7 (the helper's own exit code)", gotExit)
	}
}

const helperProgramSource = `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Printf("hello from helper: %v\n", os.Args[1:])
	os.Exit(7)
}
`
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/shim/... -v`
Expected: FAIL — the package/directory doesn't exist yet.

- [ ] **Step 3: Write the implementation**

Create `internal/shim/shim.go`:

```go
// Package shim resolves a shim binary name (e.g. "node") to the real
// executable of the currently active version, and replaces the current
// process with it.
package shim

import (
	"fmt"
	"path/filepath"

	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/providers"
	"github.com/jpsdm/dev/internal/runtime"
)

// resolve finds the registered provider whose ShimNames includes
// binName, and returns the real executable path for its active version.
// It is the pure, testable core of Run.
func resolve(m *runtime.Manager, binName string) (string, error) {
	r, err := findProvider(m, binName)
	if err != nil {
		return "", err
	}

	current, err := r.CurrentVersion()
	if err != nil {
		return "", err
	}
	if current == nil {
		return "", fmt.Errorf("%s is not active — run `dev lang use %s <version>` first", binName, r.Name())
	}

	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return "", err
	}
	versionDir := filepath.Join(versionsDir, r.Name(), current.Name)

	return r.BinaryPath(versionDir, binName)
}

func findProvider(m *runtime.Manager, binName string) (runtime.Runtime, error) {
	for _, name := range m.Names() {
		r, _ := m.Get(name)
		for _, shimName := range r.ShimNames() {
			if shimName == binName {
				return r, nil
			}
		}
	}
	return nil, fmt.Errorf("no active dev-managed runtime provides %q", binName)
}

// Run resolves binName against every registered provider and replaces
// the current process with the result. See replaceProcess for exactly
// what "replaces" means on this platform.
func Run(binName string, args []string) error {
	m := runtime.NewManager()
	providers.Register(m)

	binPath, err := resolve(m, binName)
	if err != nil {
		return err
	}
	return replaceProcess(binPath, args)
}
```

Create `internal/shim/exec_unix.go`:

```go
//go:build unix

package shim

import (
	"os"
	"syscall"
)

// replaceProcess replaces the current process image with the binary at
// path, passing args as its arguments and inheriting the environment.
// On success, this function never returns — the calling process becomes
// the new binary.
func replaceProcess(path string, args []string) error {
	argv := append([]string{path}, args...)
	return syscall.Exec(path, argv, os.Environ())
}
```

Create `internal/shim/exec_windows.go`:

```go
//go:build windows

package shim

import (
	"errors"
	"os"
	"os/exec"
)

// replaceProcess runs the binary at path as a child process with
// inherited stdio, then exits this process with the child's exact exit
// code. Windows has no process-image-replacement syscall equivalent to
// Unix's exec, so this is the closest equivalent.
func replaceProcess(path string, args []string) error {
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/shim/... -v`
Expected: PASS for all tests. The subprocess test takes noticeably longer than the others (it invokes `go build` once) — that's expected, not a hang.

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 6: Commit**

```bash
git add internal/shim
git commit -m "Add internal/shim for shim dispatch and process replacement"
```

---

## Task 5: `main.go` — shim vs. normal CLI dispatch

**Files:**
- Modify: `main.go`
- Test: `main_test.go`

**Interfaces:**
- Consumes: `shim.Run(binName string, args []string) error` (Task 4), `cmd.Execute()`, `cliutil.PrintError(err error)` (sub-project 1).
- Produces: nothing further downstream depends on `main.go` — this is the top of the call graph.

- [ ] **Step 1: Write the failing test**

Create `main_test.go`:

```go
package main

import "testing"

func TestShimBinaryName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		arg0 string
		want string
	}{
		{"/usr/local/bin/dev", ""},
		{"dev", ""},
		{"dev.exe", ""},
		{"/home/user/.dev/bin/node", "node"},
		{"npm.exe", "npm"},
	}
	for _, tc := range cases {
		if got := shimBinaryName(tc.arg0); got != tc.want {
			t.Errorf("shimBinaryName(%q) = %q, want %q", tc.arg0, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestShimBinaryName -v`
Expected: FAIL — `undefined: shimBinaryName`.

- [ ] **Step 3: Write the implementation**

Read the current `main.go` first (it currently just calls `cmd.Execute()`), then replace its full contents with:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jpsdm/dev/cmd"
	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/shim"
)

func main() {
	if name := shimBinaryName(os.Args[0]); name != "" {
		if err := shim.Run(name, os.Args[1:]); err != nil {
			cliutil.PrintError(err)
			os.Exit(1)
		}
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

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run TestShimBinaryName -v`
Expected: PASS.

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "Add shim vs CLI dispatch to main.go"
```

---

## Task 6: `cmd/env.go` — `dev env`

**Files:**
- Create: `cmd/env.go`
- Test: `cmd/env_test.go`

**Interfaces:**
- Consumes: `platform.DevHome() (string, error)` (sub-project 1), `shell.Detect() Shell`, `shell.ExportLines(sh Shell, devHome string) []string` (Task 3).
- Produces: `dev env` command — no other task depends on this file.

- [ ] **Step 1: Write the failing test**

Create `cmd/env_test.go`:

```go
package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestEnvCommand_PrintsExportLines(t *testing.T) {
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("DEV_HOME", "/custom/dev/home")

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"env"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev env` returned error: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "DEV_HOME") || !strings.Contains(got, "/custom/dev/home") {
		t.Errorf("`dev env` output = %q, want it to mention DEV_HOME and its value", got)
	}
	if !strings.Contains(got, "PATH") {
		t.Errorf("`dev env` output = %q, want it to mention PATH", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/... -run TestEnvCommand -v`
Expected: FAIL — `undefined: envCmd` (package has no implementation yet).

- [ ] **Step 3: Write the implementation**

Create `cmd/env.go`:

```go
package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/shell"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Print shell export lines to put dev's bin directory on PATH",
	RunE: func(cmd *cobra.Command, args []string) error {
		devHome, err := platform.DevHome()
		if err != nil {
			return err
		}
		sh := shell.Detect()
		lines := shell.ExportLines(sh, devHome)
		fmt.Fprintln(cmd.OutOrStdout(), strings.Join(lines, "\n"))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(envCmd)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/... -run TestEnvCommand -v`
Expected: PASS.

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 6: Commit**

```bash
git add cmd/env.go cmd/env_test.go
git commit -m "Add dev env command"
```

---

## Task 7: `cmd/setup.go` — `dev setup`

**Files:**
- Create: `cmd/setup.go`
- Test: `cmd/setup_test.go`

**Interfaces:**
- Consumes: `platform.DevHome() (string, error)`, `platform.BinDir() (string, error)` (sub-project 1), `shell.Detect`, `shell.ExportLines`, `shell.RCPath`, `shell.UpsertBlock`, `shell.Confirm` (Task 3), `providers.Register` (Task 2), `runtime.NewManager`, `(*Manager).Names`, `(*Manager).Get` (sub-project 2a), `cliutil.Success`/`cliutil.Step` (sub-project 1).
- Produces: `dev setup` command — no other task depends on this file.

- [ ] **Step 1: Write the failing tests**

Create `cmd/setup_test.go`:

```go
package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupCommand_DeclinedConfirmationMakesNoChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DEV_HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/bash")

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("n\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	rcPath := filepath.Join(home, ".bashrc")
	if _, err := os.Stat(rcPath); !os.IsNotExist(err) {
		t.Errorf("declining confirmation still created %s", rcPath)
	}
	if !strings.Contains(out.String(), "No changes made.") {
		t.Errorf("output = %q, want it to confirm no changes were made", out.String())
	}
}

func TestSetupCommand_ConfirmedWritesRCFileAndShims(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	rcPath := filepath.Join(home, ".bashrc")
	data, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("reading rc file: %v", err)
	}
	if !strings.Contains(string(data), "DEV_HOME") {
		t.Errorf("rc file content = %q, want it to contain the DEV_HOME export", data)
	}

	for _, name := range []string{"node", "npm", "npx"} {
		shimPath := filepath.Join(devHome, "bin", name)
		if _, err := os.Stat(shimPath); err != nil {
			t.Errorf("expected shim %s to exist: %v", shimPath, err)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/... -run TestSetupCommand -v`
Expected: FAIL — `undefined: setupCmd`.

- [ ] **Step 3: Write the implementation**

Create `cmd/setup.go`:

```go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/cliutil"
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

		path, supported := shell.RCPath(sh)
		if !supported {
			fmt.Fprintln(cmd.OutOrStdout(), "Automatic setup isn't supported for this shell yet. Add these lines manually:")
			fmt.Fprintln(cmd.OutOrStdout())
			for _, line := range lines {
				fmt.Fprintln(cmd.OutOrStdout(), line)
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
			cliutil.Step("No changes made.")
			return nil
		}

		if err := shell.UpsertBlock(path, lines); err != nil {
			return fmt.Errorf("updating %s: %w", path, err)
		}
		cliutil.Success("Updated %s", path)

		return installShims()
	},
}

func installShims() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding the dev binary: %w", err)
	}

	binDir, err := platform.BinDir()
	if err != nil {
		return err
	}

	m := devruntime.NewManager()
	providers.Register(m)

	for _, name := range m.Names() {
		r, _ := m.Get(name)
		for _, shimName := range r.ShimNames() {
			dest := filepath.Join(binDir, shimName)
			if runtime.GOOS == "windows" {
				dest += ".exe"
			}
			if err := copyExecutable(exe, dest); err != nil {
				return fmt.Errorf("creating shim %s: %w", shimName, err)
			}
			cliutil.Success("Shim created: %s", dest)
		}
	}
	return nil
}

// copyExecutable copies src to dest with executable permissions,
// overwriting any existing file at dest.
func copyExecutable(src, dest string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", dest, err)
	}
	if err := os.WriteFile(dest, data, 0o755); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(setupCmd)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/... -run TestSetupCommand -v`
Expected: PASS for both tests.

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed, including all pre-existing `cmd` package tests from sub-projects 1 and 2a.

- [ ] **Step 6: Commit**

```bash
git add cmd/setup.go cmd/setup_test.go
git commit -m "Add dev setup command"
```

---

## Task 8: Final acceptance verification

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: everything produced by Tasks 1–7.
- Produces: nothing new; verifies this sub-project's acceptance criteria against the real shell/filesystem and leaves the tree committed and clean.

- [ ] **Step 1: Check for a stray `DEV_HOME` before doing anything real**

```bash
echo "DEV_HOME=${DEV_HOME:-<unset>}"
echo "HOME=$HOME"
```

If `DEV_HOME` is set to something other than `$HOME/.dev`, note it — this sub-project's earlier acceptance test (2a's Task 9) found `DEV_HOME` pointed at a workspace root instead of the true default once before, and the resulting cache/scaffold directories had to be cleaned up manually. Proceed using whatever `DEV_HOME` actually resolves to in this environment; do not silently override it.

- [ ] **Step 2: Run the full check chain**

```bash
make check
```

Expected: `fmt`, `vet`, `lint`, `test`, `build` all succeed.

- [ ] **Step 3: Verify `dev env`**

```bash
./dev env
```

Expected: two export lines (`DEV_HOME` and `PATH`) in the syntax matching your actual shell (check with `echo $SHELL` first).

- [ ] **Step 4: Verify `dev setup`'s declined path**

```bash
echo "n" | ./dev setup
```

Expected: shows the shell detected, the exact lines that would be added, and the target rc file path, then prints "No changes made." after declining. Confirm the real rc file was NOT modified: `git diff --no-index /dev/null ~/.bashrc 2>&1 | head -1` or simply note its modification time before and after.

- [ ] **Step 5: Verify `dev setup`'s confirmed path (this really edits your shell rc file and creates real shims — that is the intended, permanent effect of running it for real)**

```bash
echo "y" | ./dev setup
```

Expected: the rc file (e.g. `~/.bashrc`) now contains a `# BEGIN dev shell setup` / `# END dev shell setup` block with the same lines Step 3 printed; `$DEV_HOME/bin/node`, `$DEV_HOME/bin/npm`, `$DEV_HOME/bin/npx` all exist and are executable.

- [ ] **Step 6: Verify idempotency**

```bash
echo "y" | ./dev setup
grep -c "# BEGIN dev shell setup" ~/.bashrc
```

Expected: the count is `1`, not `2` — running `dev setup` twice does not duplicate the block.

- [ ] **Step 7: Verify the shim actually runs the active Node.js version**

```bash
./dev lang install node 22
./dev lang use node 22
"$DEV_HOME/bin/node" --version
```

Expected: the shim prints a real Node.js version string (e.g. `v22.x.x`) matching the installed release, with exit code 0 — confirming the shim genuinely dispatched to and executed the real binary, not just printed something plausible.

- [ ] **Step 8: Verify the "no active version" error path**

```bash
./dev lang uninstall node 22
"$DEV_HOME/bin/node" --version
echo "exit code: $?"
```

Expected: a `✗`-prefixed error naming `dev lang use node <version>` as the fix, exit code `1` — not a generic shell "command not found" or a panic (the shim binary itself still exists and runs; it's `resolve`'s own error path being exercised).

- [ ] **Step 9: Update the README**

Add a "PATH and shims" section to `README.md` after the existing "Language management" section:

```markdown

## PATH and shims

    dev env      # print PATH export lines for your shell
    dev setup    # detect your shell, confirm, and configure PATH + shims

After `dev setup`, `~/.dev/bin` on `PATH` is enough to run whatever
version of a managed language is currently active — no per-language PATH
entries needed. Running `node`, `npm`, or `npx` transparently runs the
version most recently activated with `dev lang use`.
```

- [ ] **Step 10: Commit**

```bash
git add README.md
git commit -m "Document dev env and dev setup in the README"
```

- [ ] **Step 11: Final confirmation**

```bash
make check
git status --short
```

Expected: `make check` passes and `git status --short` is empty.
