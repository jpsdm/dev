# Expose Installed Tools Directly (Active Bin Dir) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose an active language version's whole binary directory on `PATH` (via a per-language directory symlink/junction `dev lang use`/`uninstall` repoint), so tools a language's own package manager installs later (`npm install -g pnpm`, a `pip`-installed console script, `go install`) work without `dev` knowing their names in advance — while keeping the existing friendly "not active" message for a language that's never had a version activated.

**Architecture:** Every `Runtime` gains one new method, `BinDir(versionDir string) (string, error)`, returning the directory whose contents should be exposed. A new `internal/activebin` package (Unix symlink / Windows junction, split by build tag) provides `Create` (idempotent — never resets an already-active pointer) and `Repoint` (atomic swap) for a directory-link primitive `dev lang use`/`Uninstall`/`dev setup` call. `dev setup`'s existing `installShims` stops writing flat `$DEV_HOME/bin/<name>` copies and instead writes per-language placeholder directories (`$DEV_HOME/no-active/<lang>/<name>`, same `copyExecutable`, same `internal/shim` dispatch, unchanged) — `$DEV_HOME/active/<lang>` starts pointed at that placeholder and each provider's `Activate`/`Uninstall` repoints it to/from the real version's `BinDir()`. `internal/shim`'s resolve-and-exec logic is untouched; it's now only ever reached by a placeholder copy, since an active version's real binaries are found by the OS's own `PATH` resolution before `dev` is ever invoked.

**Tech Stack:** Go stdlib only (`os`, `os/exec` for Windows' `mklink /J`, `path/filepath`) — no new dependency, matching this project's stated policy.

**Spec:** `docs/superpowers/specs/2026-09-30-active-bin-dir-exposure-design.md`

## Global Constraints

- `Runtime.BinDir(versionDir string) (string, error)` is the only interface addition — `ShimNames()`, `BinaryPath()`, and every other existing method/behavior stay exactly as they are.
- `$DEV_HOME/active/<lang>` is what actually goes on `PATH` (one entry per registered provider, always present after `dev setup`). `$DEV_HOME/no-active/<lang>/<name>` (a copy of `dev`, one per `ShimNames()` entry) is the placeholder a language's `active/<lang>` link points at before any version of it is ever activated.
- `internal/activebin.Create(linkPath, target string) error` never overwrites an existing link — used only by `dev setup`, so re-running it never resets an already-active pointer back to the placeholder. `internal/activebin.Repoint(linkPath, target string) error` always atomically (Unix) or best-effort (Windows — no atomic junction-replace primitive exists) replaces the link — used by `Activate`/`Uninstall`.
- Windows uses a directory junction (`mklink /J`, shelled out via `os/exec` — no new dependency, no elevated privileges needed), never a real directory symlink (which would need Developer Mode/admin rights there).
- **Sequencing dependency:** this plan modifies `cmd/setup.go`'s `installShims` function body. The `fix/update-shim-refresh-uses-old-binary` branch (already reviewed, PR open) also touches `cmd/setup.go` (adds `refreshShimsCmd`/`refreshShimsCommandName`) and `cmd/update.go`. **Do not start this plan's Task 6 until that PR has merged to `main`** — implement Tasks 1-5 first (they don't touch `cmd/setup.go` at all), rebase onto `main` once the other PR lands, then continue with Task 6 onward. The `__refresh-shims` hidden command and its cross-version-contract name are untouched by this plan — only `installShims`'s internal body (what it creates) changes.
- Every commit ends with `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.

## Review Focus

- **`dev setup` re-run must never reset an already-active language back to its placeholder.** `internal/activebin.Create`'s "only if `linkPath` doesn't already exist" semantics is the whole guarantee — Task 3's and Task 6's tests both need a "already active, re-run setup, still active" case, not just a fresh-install case.
- **Uninstalling a version that is *not* the active one must leave `active/<lang>`'s target completely alone** — mirrors the existing per-provider "leaves other active version marker alone" test already present for the `current/<lang>` marker file; Task 5 needs the equivalent assertion for the symlink/junction target.
- **Switching active versions twice in a row** (`dev lang use node 20` then `dev lang use node 22`) must correctly repoint away from the first version's `BinDir()` to the second's, not fail or silently no-op because the link already exists — this is exactly what `Repoint` (not `Create`) must guarantee. Task 3's and Task 5's tests both need a two-`Activate`-calls-in-a-row case.
- **A `BinDir()` error (unsupported OS/arch) during `Activate` must not leave a half-activated state** — the `current/<lang>` marker and the `active/<lang>` link must not diverge (one updated, the other not). Task 5's tests need this for at least one provider.
- **Windows junction creation is untestable on this development platform.** Task 4's tests are real (`exec.Command` invocation, error-path construction) but the actual `mklink /J` call itself can only be verified by CI running on Windows or by a human on a Windows machine — flag this explicitly rather than claiming full coverage.

---

### Task 1: `Runtime.BinDir` interface addition and all four providers' implementations

**Files:**
- Modify: `internal/runtime/runtime.go`
- Modify: `internal/runtime/node/node.go`, `internal/runtime/node/node_test.go`
- Modify: `internal/runtime/java/java.go`, `internal/runtime/java/java_test.go`
- Modify: `internal/runtime/go/go.go`, `internal/runtime/go/go_test.go`
- Modify: `internal/runtime/python/python.go`, `internal/runtime/python/python_test.go`

**Interfaces:**
- Produces: `BinDir(versionDir string) (string, error)` on the `Runtime` interface and on `*Node`, `*Java`, `*Go`, `*Python`. Task 5 calls `p.BinDir(destDir)` (per provider) from inside `Activate`/`Uninstall`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/runtime/runtime_test.go` (create the file if it doesn't already have a compile-time interface check — check first; if `var _ Runtime = (*node.Node)(nil)`-style assertions already live per-provider package instead, skip this file and rely on those):

Add to `internal/runtime/node/node_test.go`:

```go
func TestBinDirForOS_WindowsUsesVersionRoot(t *testing.T) {
	t.Parallel()
	got := binDirForOS("win", "/versiondir")
	want := "/versiondir"
	if got != want {
		t.Errorf(`binDirForOS("win", ...) = %q, want %q`, got, want)
	}
}

func TestBinDirForOS_UnixUsesBinSubdir(t *testing.T) {
	t.Parallel()
	for _, osName := range []string{"linux", "darwin"} {
		got := binDirForOS(osName, "/versiondir")
		want := filepath.Join("/versiondir", "bin")
		if got != want {
			t.Errorf("binDirForOS(%q, ...) = %q, want %q", osName, got, want)
		}
	}
}

func TestBinDir_ReturnsRealDirectory(t *testing.T) {
	t.Parallel()
	n := New()
	got, err := n.BinDir("/some/version/dir")
	if err != nil {
		t.Fatalf("BinDir() returned error on this test's platform: %v", err)
	}
	if got == "" {
		t.Error("BinDir() returned empty string")
	}
}
```

Add `"path/filepath"` to the test file's import block if not already present.

Add to `internal/runtime/java/java_test.go`:

```go
func TestBinDirForOS_MacUsesContentsHomeBin(t *testing.T) {
	t.Parallel()
	got := binDirForOS("mac", "/versiondir")
	want := filepath.Join("/versiondir", "Contents", "Home", "bin")
	if got != want {
		t.Errorf(`binDirForOS("mac", ...) = %q, want %q`, got, want)
	}
}

func TestBinDirForOS_LinuxAndWindowsUseBinSubdir(t *testing.T) {
	t.Parallel()
	for _, osName := range []string{"linux", "windows"} {
		got := binDirForOS(osName, "/versiondir")
		want := filepath.Join("/versiondir", "bin")
		if got != want {
			t.Errorf("binDirForOS(%q, ...) = %q, want %q", osName, got, want)
		}
	}
}

func TestBinDir_ReturnsRealDirectory(t *testing.T) {
	t.Parallel()
	j := New()
	got, err := j.BinDir("/some/version/dir")
	if err != nil {
		t.Fatalf("BinDir() returned error on this test's platform: %v", err)
	}
	if got == "" {
		t.Error("BinDir() returned empty string")
	}
}
```

Add to `internal/runtime/go/go_test.go`:

```go
func TestBinDir_AlwaysUsesBinSubdirOnEveryPlatform(t *testing.T) {
	t.Parallel()
	g := New()
	got, err := g.BinDir("/versiondir")
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	want := filepath.Join("/versiondir", "bin")
	if got != want {
		t.Errorf("BinDir() = %q, want %q", got, want)
	}
}
```

Add `"path/filepath"` to the test file's import block if not already present.

Add to `internal/runtime/python/python_test.go`:

```go
func TestBinDirForOS_WindowsUsesVersionRoot(t *testing.T) {
	t.Parallel()
	got := binDirForOS("windows", "/versiondir")
	want := "/versiondir"
	if got != want {
		t.Errorf(`binDirForOS("windows", ...) = %q, want %q`, got, want)
	}
}

func TestBinDirForOS_UnixUsesBinSubdir(t *testing.T) {
	t.Parallel()
	for _, osName := range []string{"linux", "darwin"} {
		got := binDirForOS(osName, "/versiondir")
		want := filepath.Join("/versiondir", "bin")
		if got != want {
			t.Errorf("binDirForOS(%q, ...) = %q, want %q", osName, got, want)
		}
	}
}

func TestBinDir_ReturnsRealDirectory(t *testing.T) {
	t.Parallel()
	p := New()
	got, err := p.BinDir("/some/version/dir")
	if err != nil {
		t.Fatalf("BinDir() returned error on this test's platform: %v", err)
	}
	if got == "" {
		t.Error("BinDir() returned empty string")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./internal/runtime/... -run TestBinDir -v`
Expected: FAIL — `undefined: binDirForOS` / `n.BinDir undefined` (and similar) for each package.

- [ ] **Step 3: Add the interface method**

In `internal/runtime/runtime.go`, add to the `Runtime` interface (after `BinaryPath`):

```go
	// BinDir returns the directory whose contents should be exposed on
	// PATH for an active version installed at versionDir — e.g.
	// "<versionDir>/bin" on most platforms, or versionDir itself where a
	// provider's binaries sit at the version root (Python's Windows
	// builds). Used by Activate/Uninstall to repoint
	// $DEV_HOME/active/<name> (see internal/activebin) so tools
	// installed by a language's own package manager (npm -g,
	// pip console-scripts, go install) are reachable on PATH without
	// dev knowing about them by name in advance — unlike ShimNames(),
	// which only ever covers a fixed, known set.
	BinDir(versionDir string) (string, error)
```

- [ ] **Step 4: Implement `BinDir` for Node**

Add to `internal/runtime/node/node.go` (near `nodeOS`):

```go
// binDirForOS returns the directory Node.js binaries live in for a
// given nodeOS() value. Node's Windows zip extracts flat at the
// version root (there is no bin/ subdirectory there); Linux/macOS
// tarballs use a real bin/ subdirectory. Kept separate from BinDir so
// the Windows branch is directly testable regardless of host.
func binDirForOS(osName, versionDir string) string {
	if osName == "win" {
		return versionDir
	}
	return filepath.Join(versionDir, "bin")
}

// BinDir returns the directory whose contents should be exposed on
// PATH for an active Node.js version.
func (n *Node) BinDir(versionDir string) (string, error) {
	osName, err := nodeOS()
	if err != nil {
		return "", err
	}
	return binDirForOS(osName, versionDir), nil
}
```

- [ ] **Step 5: Implement `BinDir` for Java**

Add to `internal/runtime/java/java.go` (near `binaryPathForOS`):

```go
// binDirForOS returns the directory Java binaries live in for a given
// javaOS() value — mirrors binaryPathForOS's OS split: macOS's
// Contents/Home nesting is a directory-level difference, not just a
// per-binary one.
func binDirForOS(osName, versionDir string) string {
	switch osName {
	case "mac":
		return filepath.Join(versionDir, "Contents", "Home", "bin")
	default:
		return filepath.Join(versionDir, "bin")
	}
}

// BinDir returns the directory whose contents should be exposed on
// PATH for an active Java version.
func (j *Java) BinDir(versionDir string) (string, error) {
	osName, err := javaOS()
	if err != nil {
		return "", err
	}
	return binDirForOS(osName, versionDir), nil
}
```

- [ ] **Step 6: Implement `BinDir` for Go**

Add to `internal/runtime/go/go.go` (near `binaryPathForOS`):

```go
// BinDir returns the directory whose contents should be exposed on
// PATH for an active Go version — always bin/, on every platform (Go
// archives are flat, no OS branching needed, same reasoning as
// BinaryPath's own).
func (g *Go) BinDir(versionDir string) (string, error) {
	return filepath.Join(versionDir, "bin"), nil
}
```

- [ ] **Step 7: Implement `BinDir` for Python**

Add to `internal/runtime/python/python.go` (near `binaryPathForOS`):

```go
// binDirForOS returns the directory Python binaries live in for a
// given pythonOS() value — mirrors binaryPathForOS's split: Windows
// puts binaries at the version root, Unix uses bin/.
func binDirForOS(osName, versionDir string) string {
	if osName == "windows" {
		return versionDir
	}
	return filepath.Join(versionDir, "bin")
}

// BinDir returns the directory whose contents should be exposed on
// PATH for an active Python version.
func (p *Python) BinDir(versionDir string) (string, error) {
	osName, err := pythonOS()
	if err != nil {
		return "", err
	}
	return binDirForOS(osName, versionDir), nil
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./internal/runtime/... -v`
Expected: PASS (all tests, including every existing test in these four packages — this step also proves the interface addition didn't break any other `Runtime` implementer's compile).

- [ ] **Step 9: Run `go vet` and `gofmt` check**

Run: `/home/linuxbrew/.linuxbrew/bin/go vet ./internal/runtime/... && /home/linuxbrew/.linuxbrew/bin/gofmt -l internal/runtime/`
Expected: no output from either command.

- [ ] **Step 10: Commit**

```bash
git add internal/runtime/runtime.go internal/runtime/node/node.go internal/runtime/node/node_test.go internal/runtime/java/java.go internal/runtime/java/java_test.go internal/runtime/go/go.go internal/runtime/go/go_test.go internal/runtime/python/python.go internal/runtime/python/python_test.go
git commit -m "$(cat <<'EOF'
feat(runtime): add BinDir to the Runtime interface

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `internal/platform` — `ActiveDir` and `NoActiveDir` helpers

**Files:**
- Modify: `internal/platform/platform.go`
- Modify: `internal/platform/platform_test.go`

**Interfaces:**
- Consumes: `DevHome() (string, error)` (already exists in this file).
- Produces: `ActiveDir(lang string) (string, error)`, `NoActiveDir(lang string) (string, error)`. Task 5 (providers) and Task 6 (`cmd/setup.go`) both call these directly.

- [ ] **Step 1: Write the failing tests**

Add to `internal/platform/platform_test.go`:

```go
func TestActiveDir_ReturnsDevHomeActiveLang(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)

	got, err := platform.ActiveDir("node")
	if err != nil {
		t.Fatalf("ActiveDir() returned error: %v", err)
	}
	want := filepath.Join(devHome, "active", "node")
	if got != want {
		t.Errorf("ActiveDir(%q) = %q, want %q", "node", got, want)
	}
}

func TestNoActiveDir_ReturnsDevHomeNoActiveLang(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)

	got, err := platform.NoActiveDir("python")
	if err != nil {
		t.Fatalf("NoActiveDir() returned error: %v", err)
	}
	want := filepath.Join(devHome, "no-active", "python")
	if got != want {
		t.Errorf("NoActiveDir(%q) = %q, want %q", "python", got, want)
	}
}
```

Check the test file's existing import block — add `"path/filepath"` and `"github.com/jpsdm/dev/internal/platform"` only if not already present (this package's own tests may already import it under a different alias, or be in `package platform` itself rather than `platform_test`; match whichever convention `platform_test.go` already uses for calling its own package's exported functions).

- [ ] **Step 2: Run tests to verify they fail**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./internal/platform/... -run "TestActiveDir|TestNoActiveDir" -v`
Expected: FAIL — `undefined: platform.ActiveDir` / `undefined: platform.NoActiveDir` (or unqualified if the test file is `package platform` itself).

- [ ] **Step 3: Implement**

Add to `internal/platform/platform.go` (near `VersionsDir`/`CurrentDir`):

```go
// ActiveDir returns DEV_HOME/active/<lang> — the directory symlink
// (Unix) or junction (Windows) dev setup creates and dev lang
// use/uninstall repoint (see internal/activebin) so lang's active
// version's whole bin directory, not just a fixed set of known binary
// names, is reachable on PATH.
func ActiveDir(lang string) (string, error) {
	home, err := DevHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "active", lang), nil
}

// NoActiveDir returns DEV_HOME/no-active/<lang> — the placeholder
// directory ActiveDir's link points at before any version of lang has
// ever been activated. Populated by dev setup with a copy of the dev
// binary per lang's ShimNames() entry (see cmd/setup.go), so the
// existing internal/shim dispatch's friendly "not active" message
// still fires until a real version takes over.
func NoActiveDir(lang string) (string, error) {
	home, err := DevHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "no-active", lang), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./internal/platform/... -v`
Expected: PASS.

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `/home/linuxbrew/.linuxbrew/bin/go vet ./internal/platform/... && /home/linuxbrew/.linuxbrew/bin/gofmt -l internal/platform/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/platform.go internal/platform/platform_test.go
git commit -m "$(cat <<'EOF'
feat(platform): add ActiveDir and NoActiveDir helpers

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `internal/activebin` — Unix symlink `Create`/`Repoint`

**Files:**
- Create: `internal/activebin/activebin_unix.go`
- Create: `internal/activebin/activebin_unix_test.go`

**Interfaces:**
- Produces (Unix build): `Create(linkPath, target string) error`, `Repoint(linkPath, target string) error`. Task 4 produces the same two function signatures for Windows (different file, `//go:build windows`), so callers in Task 5/6 never need a build tag themselves. Task 5 and Task 6 both call `activebin.Create`/`activebin.Repoint` directly.

- [ ] **Step 1: Write the failing tests**

Create `internal/activebin/activebin_unix_test.go`:

```go
//go:build !windows

package activebin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreate_CreatesSymlinkPointingAtTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	linkPath := filepath.Join(dir, "active", "node")

	if err := Create(linkPath, target); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", linkPath, err)
	}
	if got != target {
		t.Errorf("symlink target = %q, want %q", got, target)
	}
}

func TestCreate_DoesNotOverwriteAnExistingLink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	firstTarget := filepath.Join(dir, "first")
	secondTarget := filepath.Join(dir, "second")
	if err := os.MkdirAll(firstTarget, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(secondTarget, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	linkPath := filepath.Join(dir, "active", "node")

	if err := Create(linkPath, firstTarget); err != nil {
		t.Fatalf("first Create() returned error: %v", err)
	}
	if err := Create(linkPath, secondTarget); err != nil {
		t.Fatalf("second Create() returned error: %v", err)
	}

	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", linkPath, err)
	}
	if got != firstTarget {
		t.Errorf("symlink target = %q after a second Create(), want it unchanged at %q (Create must never reset an existing link)", got, firstTarget)
	}
}

func TestRepoint_SwitchesAnExistingLinkToANewTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	firstTarget := filepath.Join(dir, "first")
	secondTarget := filepath.Join(dir, "second")
	if err := os.MkdirAll(firstTarget, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(secondTarget, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	linkPath := filepath.Join(dir, "active", "node")

	if err := Create(linkPath, firstTarget); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	if err := Repoint(linkPath, secondTarget); err != nil {
		t.Fatalf("Repoint() returned error: %v", err)
	}

	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", linkPath, err)
	}
	if got != secondTarget {
		t.Errorf("symlink target = %q after Repoint(), want %q", got, secondTarget)
	}
}

func TestRepoint_WorksEvenWhenNoLinkExistsYet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	linkPath := filepath.Join(dir, "active", "node")

	if err := Repoint(linkPath, target); err != nil {
		t.Fatalf("Repoint() on a nonexistent link returned error: %v", err)
	}

	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", linkPath, err)
	}
	if got != target {
		t.Errorf("symlink target = %q, want %q", got, target)
	}
}

func TestRepoint_CanBeCalledTwiceInARowSwitchingEachTime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	targets := []string{filepath.Join(dir, "v20"), filepath.Join(dir, "v22")}
	for _, target := range targets {
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	linkPath := filepath.Join(dir, "active", "node")

	if err := Repoint(linkPath, targets[0]); err != nil {
		t.Fatalf("first Repoint() returned error: %v", err)
	}
	if err := Repoint(linkPath, targets[1]); err != nil {
		t.Fatalf("second Repoint() returned error: %v", err)
	}

	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", linkPath, err)
	}
	if got != targets[1] {
		t.Errorf("symlink target = %q after two Repoint() calls, want the second target %q", got, targets[1])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./internal/activebin/... -v`
Expected: FAIL — package `activebin` doesn't exist yet.

- [ ] **Step 3: Write the implementation**

Create `internal/activebin/activebin_unix.go`:

```go
//go:build !windows

// Package activebin manages the per-language directory link that
// exposes an active version's real bin directory on PATH — a symlink
// on Unix (this file), a junction on Windows (activebin_windows.go).
package activebin

import (
	"fmt"
	"os"
	"path/filepath"
)

// Create creates a directory symlink at linkPath pointing at target,
// but only if linkPath does not already exist. Used by dev setup,
// which must never reset an already-active language's link back to
// its placeholder just because setup ran again.
func Create(linkPath, target string) error {
	if _, err := os.Lstat(linkPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking %s: %w", linkPath, err)
	}

	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(linkPath), err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		return fmt.Errorf("linking %s to %s: %w", linkPath, target, err)
	}
	return nil
}

// Repoint atomically replaces linkPath so it points at target,
// whether or not linkPath previously existed. Used by
// Activate/Uninstall to switch which version's binaries a language's
// PATH entry resolves to. Atomic via create-as-temp-then-rename: a
// process reading linkPath never observes a half-updated link.
func Repoint(linkPath, target string) error {
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(linkPath), err)
	}

	tmpLink := linkPath + ".new"
	if err := os.RemoveAll(tmpLink); err != nil {
		return fmt.Errorf("clearing stale %s: %w", tmpLink, err)
	}
	if err := os.Symlink(target, tmpLink); err != nil {
		return fmt.Errorf("linking %s to %s: %w", tmpLink, target, err)
	}
	if err := os.Rename(tmpLink, linkPath); err != nil {
		return fmt.Errorf("repointing %s to %s: %w", linkPath, target, err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./internal/activebin/... -v`
Expected: PASS (all 5 tests).

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `/home/linuxbrew/.linuxbrew/bin/go vet ./internal/activebin/... && /home/linuxbrew/.linuxbrew/bin/gofmt -l internal/activebin/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/activebin/activebin_unix.go internal/activebin/activebin_unix_test.go
git commit -m "$(cat <<'EOF'
feat(activebin): add Unix symlink Create/Repoint

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `internal/activebin` — Windows junction `Create`/`Repoint`

**Files:**
- Create: `internal/activebin/activebin_windows.go`
- Create: `internal/activebin/activebin_windows_test.go`

**Interfaces:**
- Consumes: nothing beyond stdlib.
- Produces (Windows build): `Create(linkPath, target string) error`, `Repoint(linkPath, target string) error` — same signatures as Task 3's Unix build, so callers never branch on OS themselves.

**Note:** this task's actual `mklink /J` invocation can only be verified by CI or a human running on Windows — this development environment is Linux. The tests below are real (they exercise `Create`/`Repoint`'s actual logic, including the `exec.Command` construction), gated to only run on Windows via `runtime.GOOS`, matching this codebase's established pattern for other Windows-only code (e.g. `internal/runtime/node/node_test.go`'s `TestBinaryPath_WindowsLayout`). Report in your task report that this task's Windows-specific behavior is untested on this platform — do not claim full verification.

- [ ] **Step 1: Write the failing tests**

Create `internal/activebin/activebin_windows_test.go`:

```go
//go:build windows

package activebin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreate_CreatesJunctionPointingAtTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	linkPath := filepath.Join(dir, "active", "node")

	if err := Create(linkPath, target); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("os.Lstat(%s) returned error: %v", linkPath, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("%s is not reported as a link-type entry after Create()", linkPath)
	}
	marker := filepath.Join(linkPath, "marker.txt")
	if err := os.WriteFile(filepath.Join(target, "marker.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("reading %s through the junction failed: %v — junction does not resolve to target", marker, err)
	}
}

func TestCreate_DoesNotOverwriteAnExistingLink(t *testing.T) {
	dir := t.TempDir()
	firstTarget := filepath.Join(dir, "first")
	secondTarget := filepath.Join(dir, "second")
	if err := os.MkdirAll(firstTarget, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(secondTarget, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	linkPath := filepath.Join(dir, "active", "node")

	if err := Create(linkPath, firstTarget); err != nil {
		t.Fatalf("first Create() returned error: %v", err)
	}
	if err := Create(linkPath, secondTarget); err != nil {
		t.Fatalf("second Create() returned error: %v", err)
	}

	marker := filepath.Join(linkPath, "which.txt")
	if err := os.WriteFile(filepath.Join(firstTarget, "which.txt"), []byte("first"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("reading %s through the junction failed: %v", marker, err)
	}
	if string(got) != "first" {
		t.Errorf("junction resolves to the second target after a second Create(), want it unchanged at the first (Create must never reset an existing link)")
	}
}

func TestRepoint_SwitchesAnExistingJunctionToANewTarget(t *testing.T) {
	dir := t.TempDir()
	firstTarget := filepath.Join(dir, "first")
	secondTarget := filepath.Join(dir, "second")
	if err := os.MkdirAll(firstTarget, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(secondTarget, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	linkPath := filepath.Join(dir, "active", "node")

	if err := Create(linkPath, firstTarget); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	if err := Repoint(linkPath, secondTarget); err != nil {
		t.Fatalf("Repoint() returned error: %v", err)
	}

	marker := filepath.Join(linkPath, "which.txt")
	if err := os.WriteFile(filepath.Join(secondTarget, "which.txt"), []byte("second"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("reading %s through the junction failed: %v", marker, err)
	}
	if string(got) != "second" {
		t.Errorf("junction does not resolve to the second target after Repoint()")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail (on Windows only)**

This step cannot be executed in this development environment (Linux). Record in your report: "Windows-gated tests written but not run in this session; must be verified by CI or a human on Windows before this task is considered fully proven." Proceed to Step 3 regardless — the implementation is still real, testable code, just unverified on its target platform here.

- [ ] **Step 3: Write the implementation**

Create `internal/activebin/activebin_windows.go`:

```go
//go:build windows

package activebin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Create creates a directory junction at linkPath pointing at target,
// but only if linkPath does not already exist. Used by dev setup,
// which must never reset an already-active language's link back to
// its placeholder just because setup ran again.
func Create(linkPath, target string) error {
	if _, err := os.Lstat(linkPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking %s: %w", linkPath, err)
	}
	return createJunction(linkPath, target)
}

// Repoint replaces the junction at linkPath so it points at target.
// Windows has no atomic junction-replace primitive, so this is a
// best-effort remove-then-create — the same honesty this codebase
// already applies to Windows's other own-executable-in-use
// constraints (cmd/setup.go's installRelocation, internal/update's
// rename-aside for the dev binary itself).
func Repoint(linkPath, target string) error {
	if err := os.RemoveAll(linkPath); err != nil {
		return fmt.Errorf("removing existing %s: %w", linkPath, err)
	}
	return createJunction(linkPath, target)
}

// createJunction shells out to `mklink /J` rather than using a
// junction-creation library: this project adds a third-party
// dependency only when it earns its place (see CLAUDE.md), and mklink
// is a dependency-free cmd.exe builtin that needs no elevated
// privileges or Developer Mode for a same-volume directory junction
// (unlike a real directory symlink, which does).
func createJunction(linkPath, target string) error {
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(linkPath), err)
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", linkPath, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("creating junction %s -> %s: %w (%s)", linkPath, target, err, out)
	}
	return nil
}
```

- [ ] **Step 4: Run `go vet` and `gofmt` check (cross-compile, no execution needed)**

Run: `GOOS=windows /home/linuxbrew/.linuxbrew/bin/go vet ./internal/activebin/... && /home/linuxbrew/.linuxbrew/bin/gofmt -l internal/activebin/`
Expected: no output from either command — this at least proves the Windows file compiles and is correctly gated by its build tag, even without running its tests.

- [ ] **Step 5: Commit**

```bash
git add internal/activebin/activebin_windows.go internal/activebin/activebin_windows_test.go
git commit -m "$(cat <<'EOF'
feat(activebin): add Windows junction Create/Repoint

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Wire `activebin` into every provider's `Activate`/`Uninstall`

**Files:**
- Modify: `internal/runtime/node/node.go`, `internal/runtime/node/node_test.go`
- Modify: `internal/runtime/java/java.go`, `internal/runtime/java/java_test.go`
- Modify: `internal/runtime/go/go.go`, `internal/runtime/go/go_test.go`
- Modify: `internal/runtime/python/python.go`, `internal/runtime/python/python_test.go`

**Interfaces:**
- Consumes: `activebin.Repoint(linkPath, target string) error` (Tasks 3/4), `platform.ActiveDir(lang string) (string, error)`, `platform.NoActiveDir(lang string) (string, error)` (Task 2), `p.BinDir(versionDir string) (string, error)` (Task 1, called on the provider itself).
- Produces: nothing new — this task only changes `Activate`/`Uninstall`'s bodies.

- [ ] **Step 1: Write the failing tests**

Add to `internal/runtime/node/node_test.go`:

```go
func TestActivate_RepointsActiveDirToBinDir(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "node", "22"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	n := New()

	if err := n.Activate("22"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("node")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	wantBinDir, err := n.BinDir(filepath.Join(devHome, "versions", "node", "22"))
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	if got != wantBinDir {
		t.Errorf("active/node symlink target = %q, want %q", got, wantBinDir)
	}
}

func TestActivate_TwiceInARowRepointsToTheSecondVersion(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, v := range []string{"20", "22"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "node", v), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	n := New()

	if err := n.Activate("20"); err != nil {
		t.Fatalf("first Activate() returned error: %v", err)
	}
	if err := n.Activate("22"); err != nil {
		t.Fatalf("second Activate() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("node")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	want22, err := n.BinDir(filepath.Join(devHome, "versions", "node", "22"))
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	if got != want22 {
		t.Errorf("active/node symlink target = %q after activating 20 then 22, want the newest (%q)", got, want22)
	}
}

func TestUninstall_OfActiveVersionRepointsActiveDirToPlaceholder(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "node", "22"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	n := New()
	if err := n.Activate("22"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	if err := n.Uninstall("22"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("node")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	noActiveDir, err := platform.NoActiveDir("node")
	if err != nil {
		t.Fatalf("platform.NoActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	if got != noActiveDir {
		t.Errorf("active/node symlink target = %q after uninstalling the active version, want the placeholder %q", got, noActiveDir)
	}
}

func TestUninstall_OfNonActiveVersionLeavesActiveDirAlone(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, v := range []string{"20", "22"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "node", v), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	n := New()
	if err := n.Activate("22"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	if err := n.Uninstall("20"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("node")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	want22, err := n.BinDir(filepath.Join(devHome, "versions", "node", "22"))
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	if got != want22 {
		t.Errorf("active/node symlink target = %q after uninstalling the non-active version 20, want it untouched at %q", got, want22)
	}
}

// TestActivate_RepointFailureDoesNotWriteMarker pins the ordering
// guarantee Activate's new code must have: if repointing active/node
// fails for any reason (an unsupported OS from BinDir, a permissions
// problem, anything), the current/node marker must never be written
// anyway — a marker pointing at a version whose active/node link was
// never updated to match is exactly the divergent state this feature
// must not produce. Forces a real Repoint failure by making active/'s
// parent a plain file instead of a directory (so activebin.Repoint's
// internal MkdirAll fails) rather than needing to fake an unsupported
// OS — this is the one test of this specific ordering contract; it is
// not duplicated across the other three providers since Activate's
// shape (BinDir+Repoint strictly before the marker write) is identical
// in all four, so one provider's coverage of the ordering itself is
// enough — each provider's own tests above already cover its own
// BinDir/marker content separately.
func TestActivate_RepointFailureDoesNotWriteMarker(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "node", "22"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	activeParent := filepath.Join(devHome, "active")
	if err := os.WriteFile(activeParent, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	n := New()

	if err := n.Activate("22"); err == nil {
		t.Fatal("Activate() returned nil error despite active/ being unwritable (a plain file, not a directory)")
	}

	currentDir, err := platform.CurrentDir()
	if err != nil {
		t.Fatalf("platform.CurrentDir() returned error: %v", err)
	}
	markerPath := filepath.Join(currentDir, "node")
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Error("current/node marker was written despite the active/node repoint failing — marker and link must never diverge")
	}
}
```

Add `"github.com/jpsdm/dev/internal/platform"` to the test file's import block if not already present (it likely already is, for `platform.VersionsDir`-style setup in earlier tests — check before adding a duplicate).

Add to `internal/runtime/java/java_test.go`:

```go
func TestActivate_RepointsActiveDirToBinDir(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "java", "21"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	j := New()

	if err := j.Activate("21"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("java")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	wantBinDir, err := j.BinDir(filepath.Join(devHome, "versions", "java", "21"))
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	if got != wantBinDir {
		t.Errorf("active/java symlink target = %q, want %q", got, wantBinDir)
	}
}

func TestActivate_TwiceInARowRepointsToTheSecondVersion(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, v := range []string{"17", "21"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "java", v), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	j := New()

	if err := j.Activate("17"); err != nil {
		t.Fatalf("first Activate() returned error: %v", err)
	}
	if err := j.Activate("21"); err != nil {
		t.Fatalf("second Activate() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("java")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	want21, err := j.BinDir(filepath.Join(devHome, "versions", "java", "21"))
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	if got != want21 {
		t.Errorf("active/java symlink target = %q after activating 17 then 21, want the newest (%q)", got, want21)
	}
}

func TestUninstall_OfActiveVersionRepointsActiveDirToPlaceholder(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "java", "21"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	j := New()
	if err := j.Activate("21"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	if err := j.Uninstall("21"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("java")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	noActiveDir, err := platform.NoActiveDir("java")
	if err != nil {
		t.Fatalf("platform.NoActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	if got != noActiveDir {
		t.Errorf("active/java symlink target = %q after uninstalling the active version, want the placeholder %q", got, noActiveDir)
	}
}

func TestUninstall_OfNonActiveVersionLeavesActiveDirAlone(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, v := range []string{"17", "21"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "java", v), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	j := New()
	if err := j.Activate("21"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	if err := j.Uninstall("17"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("java")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	want21, err := j.BinDir(filepath.Join(devHome, "versions", "java", "21"))
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	if got != want21 {
		t.Errorf("active/java symlink target = %q after uninstalling the non-active version 17, want it untouched at %q", got, want21)
	}
}
```

Add `"github.com/jpsdm/dev/internal/platform"` to the test file's import block if not already present.

Add to `internal/runtime/go/go_test.go`:

```go
func TestActivate_RepointsActiveDirToBinDir(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "go", "1.24"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	g := New()

	if err := g.Activate("1.24"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("go")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	wantBinDir, err := g.BinDir(filepath.Join(devHome, "versions", "go", "1.24"))
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	if got != wantBinDir {
		t.Errorf("active/go symlink target = %q, want %q", got, wantBinDir)
	}
}

func TestActivate_TwiceInARowRepointsToTheSecondVersion(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, v := range []string{"1.23", "1.24"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "go", v), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	g := New()

	if err := g.Activate("1.23"); err != nil {
		t.Fatalf("first Activate() returned error: %v", err)
	}
	if err := g.Activate("1.24"); err != nil {
		t.Fatalf("second Activate() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("go")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	want124, err := g.BinDir(filepath.Join(devHome, "versions", "go", "1.24"))
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	if got != want124 {
		t.Errorf("active/go symlink target = %q after activating 1.23 then 1.24, want the newest (%q)", got, want124)
	}
}

func TestUninstall_OfActiveVersionRepointsActiveDirToPlaceholder(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "go", "1.24"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	g := New()
	if err := g.Activate("1.24"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	if err := g.Uninstall("1.24"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("go")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	noActiveDir, err := platform.NoActiveDir("go")
	if err != nil {
		t.Fatalf("platform.NoActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	if got != noActiveDir {
		t.Errorf("active/go symlink target = %q after uninstalling the active version, want the placeholder %q", got, noActiveDir)
	}
}

func TestUninstall_OfNonActiveVersionLeavesActiveDirAlone(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, v := range []string{"1.23", "1.24"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "go", v), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	g := New()
	if err := g.Activate("1.24"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	if err := g.Uninstall("1.23"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("go")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	want124, err := g.BinDir(filepath.Join(devHome, "versions", "go", "1.24"))
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	if got != want124 {
		t.Errorf("active/go symlink target = %q after uninstalling the non-active version 1.23, want it untouched at %q", got, want124)
	}
}
```

Add `"github.com/jpsdm/dev/internal/platform"` to the test file's import block if not already present.

Add to `internal/runtime/python/python_test.go`:

```go
func TestActivate_RepointsActiveDirToBinDir(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "python", "3.12"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	p := New()

	if err := p.Activate("3.12"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("python")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	wantBinDir, err := p.BinDir(filepath.Join(devHome, "versions", "python", "3.12"))
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	if got != wantBinDir {
		t.Errorf("active/python symlink target = %q, want %q", got, wantBinDir)
	}
}

func TestActivate_TwiceInARowRepointsToTheSecondVersion(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, v := range []string{"3.11", "3.12"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "python", v), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	p := New()

	if err := p.Activate("3.11"); err != nil {
		t.Fatalf("first Activate() returned error: %v", err)
	}
	if err := p.Activate("3.12"); err != nil {
		t.Fatalf("second Activate() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("python")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	want312, err := p.BinDir(filepath.Join(devHome, "versions", "python", "3.12"))
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	if got != want312 {
		t.Errorf("active/python symlink target = %q after activating 3.11 then 3.12, want the newest (%q)", got, want312)
	}
}

func TestUninstall_OfActiveVersionRepointsActiveDirToPlaceholder(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "python", "3.12"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	p := New()
	if err := p.Activate("3.12"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	if err := p.Uninstall("3.12"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("python")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	noActiveDir, err := platform.NoActiveDir("python")
	if err != nil {
		t.Fatalf("platform.NoActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	if got != noActiveDir {
		t.Errorf("active/python symlink target = %q after uninstalling the active version, want the placeholder %q", got, noActiveDir)
	}
}

func TestUninstall_OfNonActiveVersionLeavesActiveDirAlone(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, v := range []string{"3.11", "3.12"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "python", v), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	p := New()
	if err := p.Activate("3.12"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	if err := p.Uninstall("3.11"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("python")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	want312, err := p.BinDir(filepath.Join(devHome, "versions", "python", "3.12"))
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	if got != want312 {
		t.Errorf("active/python symlink target = %q after uninstalling the non-active version 3.11, want it untouched at %q", got, want312)
	}
}
```

Add `"github.com/jpsdm/dev/internal/platform"` to the test file's import block if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./internal/runtime/... -run "TestActivate_|TestUninstall_Of" -v`
Expected: FAIL — `active/node` (etc.) symlink never created, since `Activate`/`Uninstall` don't call `activebin` yet.

- [ ] **Step 3: Wire Node's `Activate`/`Uninstall`**

In `internal/runtime/node/node.go`, add the import `"github.com/jpsdm/dev/internal/activebin"`, then modify `Activate` and `Uninstall`:

```go
// Activate makes name the active Node.js version by writing it to
// current/node, and repointing active/node (see internal/activebin)
// to that version's bin directory so tools installed later by npm's
// own -g flag (pnpm, etc.) are reachable on PATH without dev knowing
// their names in advance.
func (n *Node) Activate(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "node", name)
	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Node.js is not installed — run `dev lang install node %s` first", name, name)
	}

	binDir, err := n.BinDir(destDir)
	if err != nil {
		return fmt.Errorf("activating Node.js %s: %w", name, err)
	}
	activeDir, err := platform.ActiveDir("node")
	if err != nil {
		return err
	}
	if err := activebin.Repoint(activeDir, binDir); err != nil {
		return fmt.Errorf("activating Node.js %s: %w", name, err)
	}

	currentDir, err := platform.CurrentDir()
	if err != nil {
		return err
	}
	markerPath := filepath.Join(currentDir, "node")
	if err := filesystem.WriteFileAtomic(markerPath, []byte(name), 0o644); err != nil {
		return fmt.Errorf("activating Node.js %s: %w", name, err)
	}

	cliutil.Success("Node.js %s activated", name)
	return nil
}
```

(The `BinDir`/`Repoint` step now runs *before* the `current/node` marker write, so a `BinDir` error — an unsupported OS/arch — never leaves the marker pointing at a version whose `active/node` link was never updated to match.)

```go
// Uninstall removes versions/node/<name>. If it was the active
// version, current/node is cleared too, and active/node is repointed
// back to the placeholder (see internal/activebin) — a dangling link
// pointing at a removed version's bin directory is worse than the
// deliberate "not active" placeholder.
func (n *Node) Uninstall(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "node", name)

	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Node.js is not installed", name)
	}

	current, err := n.CurrentVersion()
	if err != nil {
		return err
	}

	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("removing %s: %w", destDir, err)
	}

	if current != nil && current.Name == name {
		currentDir, err := platform.CurrentDir()
		if err != nil {
			return err
		}
		markerPath := filepath.Join(currentDir, "node")
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clearing active version marker: %w", err)
		}

		activeDir, err := platform.ActiveDir("node")
		if err != nil {
			return err
		}
		noActiveDir, err := platform.NoActiveDir("node")
		if err != nil {
			return err
		}
		if err := activebin.Repoint(activeDir, noActiveDir); err != nil {
			return fmt.Errorf("reverting active/node to the placeholder: %w", err)
		}

		cliutil.Step("Node.js %s was the active version; no version is active now", name)
	}

	cliutil.Success("Node.js %s uninstalled", name)
	return nil
}
```

- [ ] **Step 4: Wire Java's, Go's, and Python's `Activate`/`Uninstall`**

Add the import `"github.com/jpsdm/dev/internal/activebin"` to `internal/runtime/java/java.go`, `internal/runtime/go/go.go`, and `internal/runtime/python/python.go`, then replace each provider's `Activate` and `Uninstall` with the versions below.

In `internal/runtime/java/java.go`:

```go
// Uninstall removes versions/java/<name>. If it was the active
// version, current/java is cleared too, and active/java is repointed
// back to the placeholder (see internal/activebin) — a dangling link
// pointing at a removed version's bin directory is worse than the
// deliberate "not active" placeholder.
func (j *Java) Uninstall(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "java", name)

	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Java is not installed", name)
	}

	current, err := j.CurrentVersion()
	if err != nil {
		return err
	}

	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("removing %s: %w", destDir, err)
	}

	if current != nil && current.Name == name {
		currentDir, err := platform.CurrentDir()
		if err != nil {
			return err
		}
		markerPath := filepath.Join(currentDir, "java")
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clearing active version marker: %w", err)
		}

		activeDir, err := platform.ActiveDir("java")
		if err != nil {
			return err
		}
		noActiveDir, err := platform.NoActiveDir("java")
		if err != nil {
			return err
		}
		if err := activebin.Repoint(activeDir, noActiveDir); err != nil {
			return fmt.Errorf("reverting active/java to the placeholder: %w", err)
		}

		cliutil.Step("Java %s was the active version; no version is active now", name)
	}

	cliutil.Success("Java %s uninstalled", name)
	return nil
}

// Activate makes name the active Java version by writing it to
// current/java, and repointing active/java (see internal/activebin)
// to that version's bin directory so tools installed later are
// reachable on PATH without dev knowing their names in advance.
// name must already be installed.
func (j *Java) Activate(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "java", name)
	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Java is not installed — run `dev lang install java %s` first", name, name)
	}

	binDir, err := j.BinDir(destDir)
	if err != nil {
		return fmt.Errorf("activating Java %s: %w", name, err)
	}
	activeDir, err := platform.ActiveDir("java")
	if err != nil {
		return err
	}
	if err := activebin.Repoint(activeDir, binDir); err != nil {
		return fmt.Errorf("activating Java %s: %w", name, err)
	}

	currentDir, err := platform.CurrentDir()
	if err != nil {
		return err
	}
	markerPath := filepath.Join(currentDir, "java")
	if err := filesystem.WriteFileAtomic(markerPath, []byte(name), 0o644); err != nil {
		return fmt.Errorf("activating Java %s: %w", name, err)
	}

	cliutil.Success("Java %s activated", name)
	return nil
}
```

In `internal/runtime/go/go.go`:

```go
// Uninstall removes versions/go/<name>. If it was the active version,
// current/go is cleared too, and active/go is repointed back to the
// placeholder (see internal/activebin) — a dangling link pointing at
// a removed version's bin directory is worse than the deliberate
// "not active" placeholder.
func (g *Go) Uninstall(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "go", name)

	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Go is not installed", name)
	}

	current, err := g.CurrentVersion()
	if err != nil {
		return err
	}

	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("removing %s: %w", destDir, err)
	}

	if current != nil && current.Name == name {
		currentDir, err := platform.CurrentDir()
		if err != nil {
			return err
		}
		markerPath := filepath.Join(currentDir, "go")
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clearing active version marker: %w", err)
		}

		activeDir, err := platform.ActiveDir("go")
		if err != nil {
			return err
		}
		noActiveDir, err := platform.NoActiveDir("go")
		if err != nil {
			return err
		}
		if err := activebin.Repoint(activeDir, noActiveDir); err != nil {
			return fmt.Errorf("reverting active/go to the placeholder: %w", err)
		}

		cliutil.Step("Go %s was the active version; no version is active now", name)
	}

	cliutil.Success("Go %s uninstalled", name)
	return nil
}

// Activate makes name the active Go version by writing it to
// current/go, and repointing active/go (see internal/activebin) to
// that version's bin directory so tools installed later (go install)
// are reachable on PATH without dev knowing their names in advance.
// name must already be installed.
func (g *Go) Activate(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "go", name)
	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Go is not installed — run `dev lang install go %s` first", name, name)
	}

	binDir, err := g.BinDir(destDir)
	if err != nil {
		return fmt.Errorf("activating Go %s: %w", name, err)
	}
	activeDir, err := platform.ActiveDir("go")
	if err != nil {
		return err
	}
	if err := activebin.Repoint(activeDir, binDir); err != nil {
		return fmt.Errorf("activating Go %s: %w", name, err)
	}

	currentDir, err := platform.CurrentDir()
	if err != nil {
		return err
	}
	markerPath := filepath.Join(currentDir, "go")
	if err := filesystem.WriteFileAtomic(markerPath, []byte(name), 0o644); err != nil {
		return fmt.Errorf("activating Go %s: %w", name, err)
	}

	cliutil.Success("Go %s activated", name)
	return nil
}
```

In `internal/runtime/python/python.go`:

```go
// Uninstall removes versions/python/<name>. If it was the active
// version, current/python is cleared too, and active/python is
// repointed back to the placeholder (see internal/activebin) — a
// dangling link pointing at a removed version's bin directory is
// worse than the deliberate "not active" placeholder.
func (p *Python) Uninstall(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "python", name)

	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Python is not installed", name)
	}

	current, err := p.CurrentVersion()
	if err != nil {
		return err
	}

	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("removing %s: %w", destDir, err)
	}

	if current != nil && current.Name == name {
		currentDir, err := platform.CurrentDir()
		if err != nil {
			return err
		}
		markerPath := filepath.Join(currentDir, "python")
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clearing active version marker: %w", err)
		}

		activeDir, err := platform.ActiveDir("python")
		if err != nil {
			return err
		}
		noActiveDir, err := platform.NoActiveDir("python")
		if err != nil {
			return err
		}
		if err := activebin.Repoint(activeDir, noActiveDir); err != nil {
			return fmt.Errorf("reverting active/python to the placeholder: %w", err)
		}

		cliutil.Step("Python %s was the active version; no version is active now", name)
	}

	cliutil.Success("Python %s uninstalled", name)
	return nil
}

// Activate makes name the active Python version by writing it to
// current/python, and repointing active/python (see
// internal/activebin) to that version's bin directory so tools
// installed later (pip console-scripts) are reachable on PATH without
// dev knowing their names in advance. name must already be installed.
func (p *Python) Activate(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "python", name)
	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Python is not installed — run `dev lang install python %s` first", name, name)
	}

	binDir, err := p.BinDir(destDir)
	if err != nil {
		return fmt.Errorf("activating Python %s: %w", name, err)
	}
	activeDir, err := platform.ActiveDir("python")
	if err != nil {
		return err
	}
	if err := activebin.Repoint(activeDir, binDir); err != nil {
		return fmt.Errorf("activating Python %s: %w", name, err)
	}

	currentDir, err := platform.CurrentDir()
	if err != nil {
		return err
	}
	markerPath := filepath.Join(currentDir, "python")
	if err := filesystem.WriteFileAtomic(markerPath, []byte(name), 0o644); err != nil {
		return fmt.Errorf("activating Python %s: %w", name, err)
	}

	cliutil.Success("Python %s activated", name)
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./internal/runtime/... -v`
Expected: PASS (all tests in all four provider packages, including every pre-existing test — `TestActivate_WritesMarkerFile`-style tests must still pass unchanged, since `Activate`'s final marker-write behavior is unchanged, only preceded by the new repoint step).

- [ ] **Step 6: Run `go vet` and `gofmt` check**

Run: `/home/linuxbrew/.linuxbrew/bin/go vet ./internal/runtime/... && /home/linuxbrew/.linuxbrew/bin/gofmt -l internal/runtime/`
Expected: no output from either command.

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/node/node.go internal/runtime/node/node_test.go internal/runtime/java/java.go internal/runtime/java/java_test.go internal/runtime/go/go.go internal/runtime/go/go_test.go internal/runtime/python/python.go internal/runtime/python/python_test.go
git commit -m "$(cat <<'EOF'
feat(runtime): repoint active/<lang> on Activate and Uninstall

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: `cmd/setup.go` — placeholders replace flat `$DEV_HOME/bin` shims

**PREREQUISITE: do not start this task until `fix/update-shim-refresh-uses-old-binary` has merged to `main`.** Rebase this branch onto the post-merge `main` first — that PR also modifies `cmd/setup.go` (adds `refreshShimsCmd`/`refreshShimsCommandName`), and this task changes `installShims`'s body, so implementing both independently would conflict. The `__refresh-shims` hidden command itself, and its name, are untouched by this task.

**Files:**
- Modify: `cmd/setup.go`
- Modify: `cmd/setup_test.go`

**Interfaces:**
- Consumes: `platform.ActiveDir(lang string) (string, error)`, `platform.NoActiveDir(lang string) (string, error)` (Task 2); `activebin.Create(linkPath, target string) error` (Tasks 3/4); `r.ShimNames() []string` (unchanged, existing).
- Produces: nothing new — `installShims`'s signature (`func(cmd *cobra.Command) error`) is unchanged, only its body.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/setup_test.go`:

```go
func TestSetupCommand_CreatesPlaceholdersNotFlatBinShims(t *testing.T) {
	skipOnWindowsRCFile(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	for _, name := range []string{"node", "npm", "npx"} {
		flatShim := filepath.Join(devHome, "bin", shimFileName(name))
		if _, err := os.Stat(flatShim); !os.IsNotExist(err) {
			t.Errorf("flat shim %s was created — expected placeholders under no-active/node/ instead", flatShim)
		}
		placeholder := filepath.Join(devHome, "no-active", "node", shimFileName(name))
		if _, err := os.Stat(placeholder); err != nil {
			t.Errorf("expected placeholder %s to exist: %v", placeholder, err)
		}
	}
}

func TestSetupCommand_CreatesActiveDirPointingAtPlaceholder(t *testing.T) {
	skipOnWindowsRCFile(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	activeDir, err := platform.ActiveDir("node")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	noActiveDir, err := platform.NoActiveDir("node")
	if err != nil {
		t.Fatalf("platform.NoActiveDir() returned error: %v", err)
	}
	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	if got != noActiveDir {
		t.Errorf("active/node symlink target = %q, want the placeholder %q", got, noActiveDir)
	}
}

func TestSetupCommand_RunningTwiceDoesNotResetAnAlreadyActiveLink(t *testing.T) {
	skipOnWindowsRCFile(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	run := func() {
		rootCmd.SetOut(io.Discard)
		rootCmd.SetErr(io.Discard)
		rootCmd.SetIn(strings.NewReader("y\n"))
		rootCmd.SetArgs([]string{"setup"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("`dev setup` returned error: %v", err)
		}
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	}
	run()

	activeDir, err := platform.ActiveDir("node")
	if err != nil {
		t.Fatalf("platform.ActiveDir() returned error: %v", err)
	}
	realVersionDir := filepath.Join(devHome, "versions", "node", "22", "bin")
	if err := os.MkdirAll(realVersionDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := activebin.Repoint(activeDir, realVersionDir); err != nil {
		t.Fatalf("simulating an active version: %v", err)
	}

	run()

	got, err := os.Readlink(activeDir)
	if err != nil {
		t.Fatalf("os.Readlink(%s) returned error: %v", activeDir, err)
	}
	if got != realVersionDir {
		t.Errorf("active/node symlink target = %q after a second `dev setup`, want it left alone at %q", got, realVersionDir)
	}
}
```

Add `"github.com/jpsdm/dev/internal/activebin"` and `"github.com/jpsdm/dev/internal/platform"` to the test file's import block if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./cmd/... -run "TestSetupCommand_CreatesPlaceholders|TestSetupCommand_CreatesActiveDir|TestSetupCommand_RunningTwiceDoesNotReset" -v`
Expected: FAIL — flat shims are still created (old behavior), no `active/node` link exists yet.

- [ ] **Step 3: Rewrite `installShims`**

In `cmd/setup.go`, add the imports `"github.com/jpsdm/dev/internal/activebin"` and add `platform.ActiveDir`/`platform.NoActiveDir` calls (the `platform` import already exists in this file). Replace the body of `installShims`:

```go
func installShims(cmd *cobra.Command) error {
	// platform.Executable, not the raw os.Executable, for the same
	// reason relocateIfNeeded uses it: both functions must agree on
	// which binary "the running dev" is, including under a test that
	// overrides the seam. Production behavior is identical either way,
	// since platform.Executable's default value is os.Executable.
	exe, err := platform.Executable()
	if err != nil {
		return fmt.Errorf("finding the dev binary: %w", err)
	}

	m := devruntime.NewManager()
	providers.Register(m)

	for _, name := range m.Names() {
		r, _ := m.Get(name)

		noActiveDir, err := platform.NoActiveDir(name)
		if err != nil {
			return err
		}
		for _, shimName := range r.ShimNames() {
			dest := filepath.Join(noActiveDir, shimName)
			if runtime.GOOS == "windows" {
				dest += ".exe"
			}
			if err := copyExecutable(exe, dest); err != nil {
				return fmt.Errorf("creating placeholder %s: %w", shimName, err)
			}
			cliutil.Fsuccess(cmd.OutOrStdout(), "Shim created: %s", dest)
		}

		activeDir, err := platform.ActiveDir(name)
		if err != nil {
			return err
		}
		if err := activebin.Create(activeDir, noActiveDir); err != nil {
			return fmt.Errorf("linking %s: %w", activeDir, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./cmd/... -v`
Expected: this step will FAIL at first on at least one pre-existing test, because `installShims` no longer writes flat `$DEV_HOME/bin/<name>` shims. Find every assertion of that old flat path with:

```bash
grep -n '"bin", shimFileName' cmd/setup_test.go
```

For each one found, replace the pattern `filepath.Join(devHome, "bin", shimFileName(name))` with `filepath.Join(noActiveDir, shimFileName(name))`, where `noActiveDir` is obtained once per loop via `platform.NoActiveDir("node")` (every pre-existing test in this file that asserts shim existence uses the `"node"`/`"java"` sample names, so `"node"` is the right language key for all of them — confirm against each test's own loop variable list before assuming). For example, `TestSetupCommand_ConfirmedWritesRCFileAndShims`'s existing assertion block:

```go
	for _, name := range []string{"node", "npm", "npx", "java", "javac"} {
		shimPath := filepath.Join(devHome, "bin", shimFileName(name))
		if _, err := os.Stat(shimPath); err != nil {
			t.Errorf("expected shim %s to exist: %v", shimPath, err)
		}
	}
```

becomes:

```go
	nodePlaceholder, err := platform.NoActiveDir("node")
	if err != nil {
		t.Fatalf("platform.NoActiveDir() returned error: %v", err)
	}
	javaPlaceholder, err := platform.NoActiveDir("java")
	if err != nil {
		t.Fatalf("platform.NoActiveDir() returned error: %v", err)
	}
	for _, tc := range []struct{ dir, name string }{
		{nodePlaceholder, "node"}, {nodePlaceholder, "npm"}, {nodePlaceholder, "npx"},
		{javaPlaceholder, "java"}, {javaPlaceholder, "javac"},
	} {
		shimPath := filepath.Join(tc.dir, shimFileName(tc.name))
		if _, err := os.Stat(shimPath); err != nil {
			t.Errorf("expected shim %s to exist: %v", shimPath, err)
		}
	}
```

Apply the same restructuring (group each test's existing name list by which provider it actually belongs to, look up that provider's `NoActiveDir` once, assert against it) to every other match `grep` found. Re-run the full command from this step until it passes with no remaining flat-path assertions.

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `/home/linuxbrew/.linuxbrew/bin/go vet ./cmd/... && /home/linuxbrew/.linuxbrew/bin/gofmt -l cmd/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add cmd/setup.go cmd/setup_test.go
git commit -m "$(cat <<'EOF'
feat(setup): create per-language placeholders instead of flat bin shims

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: `internal/shell` PATH lines, plus README/CONTRIBUTING

**Files:**
- Modify: `internal/shell/shell.go`, `internal/shell/shell_test.go`
- Modify: `cmd/setup.go`, `cmd/env.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: nothing new from earlier tasks (this task's `ExportLines` signature change is consumed by `cmd/setup.go`/`cmd/env.go`, both already in this plan's file list).
- Produces: `ExportLines(sh Shell, devHome string, langs []string) []string` (signature change — the previous two-argument form goes away, both call sites update together in this same task).

- [ ] **Step 1: Write the failing tests**

`internal/shell/shell_test.go` has 8 existing `ExportLines` call sites. Five of them (`TestExportLines_Bash`, `TestExportLines_Fish`, `TestExportLines_PowerShellDoesNotBackslashEscapeThePath`, `TestExportLines_UnknownShellWarnsItWasNotRecognized`, `TestExportLines_QuotesPathsSafelyAcrossShells`) only assert `strings.Contains` on a substring unaffected by the new third argument — for these, just add `, nil` as the third argument to each call (e.g. `ExportLines(Bash, "/home/user/.dev", nil)`) so they keep compiling and keep passing unchanged.

The remaining three assert the full exact line list, and are where this task's new behavior actually gets exercised — change each to pass real language names and expect the new `active/<lang>` segments. In `TestExportLines_PosixIncludesBothDevHomeAndBinOnPath`:

```go
func TestExportLines_PosixIncludesBothDevHomeAndBinOnPath(t *testing.T) {
	t.Parallel()
	got := ExportLines(Bash, "/home/user/.dev", []string{"node", "python"})
	want := []string{
		`export DEV_HOME='/home/user/.dev'`,
		`export PATH="$DEV_HOME:$DEV_HOME/bin:$DEV_HOME/active/node:$DEV_HOME/active/python:$PATH"`,
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

In `TestExportLines_FishIncludesBothDevHomeAndBinOnPath`:

```go
func TestExportLines_FishIncludesBothDevHomeAndBinOnPath(t *testing.T) {
	t.Parallel()
	got := ExportLines(Fish, "/home/user/.dev", []string{"node", "python"})
	want := []string{
		`set -gx DEV_HOME '/home/user/.dev'`,
		`set -gx PATH $DEV_HOME $DEV_HOME/bin $DEV_HOME/active/node $DEV_HOME/active/python $PATH`,
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

In `TestExportLines_PowerShellIncludesBothDevHomeAndBinOnPath`:

```go
func TestExportLines_PowerShellIncludesBothDevHomeAndBinOnPath(t *testing.T) {
	t.Parallel()
	got := ExportLines(PowerShell, `C:\Users\foo\.dev`, []string{"node", "python"})
	want := []string{
		`$env:DEV_HOME = "C:\Users\foo\.dev"`,
		`$env:PATH = "$env:DEV_HOME;$env:DEV_HOME\bin;$env:DEV_HOME\active\node;$env:DEV_HOME\active\python;$env:PATH"`,
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

Add this new test for the empty-registry edge case:

```go
func TestExportLines_NoLangsProducesUnchangedPathLine(t *testing.T) {
	t.Parallel()
	lines := ExportLines(Bash, "/home/user/.dev", nil)
	want := `export PATH="/home/user/.dev:/home/user/.dev/bin:$PATH"`
	found := false
	for _, l := range lines {
		if l == want {
			found = true
		}
	}
	if !found {
		t.Errorf("ExportLines(..., nil) lines = %v, want one of them to be %q (no active/<lang> segments when no providers are registered)", lines, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./internal/shell/... -v`
Expected: FAIL — compile error (`ExportLines` called with 2 args, wants 3) once Step 1's edits are in place but before Step 3's implementation change — or, if you update call sites and expectations together, a real assertion failure showing the old (2-entry) `PATH` line instead of the new (3-entry) one.

- [ ] **Step 3: Implement the new `ExportLines`**

In `internal/shell/shell.go`, change the signature and every branch to build the `PATH` line dynamically:

```go
// ExportLines returns the lines dev env prints / dev setup would insert,
// in sh's own syntax. langs is every registered provider's name (see
// internal/runtime.Manager.Names()) — one $DEV_HOME/active/<lang>
// segment is added to PATH per entry, so an active version's whole bin
// directory (not just a fixed set of known binary names) is reachable.
// Unknown falls back to POSIX sh-compatible syntax (the same as
// Bash/Zsh), since that's the most broadly interpretable default when
// the shell couldn't be identified.
func ExportLines(sh Shell, devHome string, langs []string) []string {
	switch sh {
	case Fish:
		return []string{
			fmt.Sprintf("set -gx DEV_HOME %s", shellQuote(devHome)),
			fmt.Sprintf("set -gx PATH $DEV_HOME $DEV_HOME/bin%s $PATH", fishActiveDirSegments(langs)),
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
			fmt.Sprintf(`$env:PATH = "$env:DEV_HOME;$env:DEV_HOME\bin%s;$env:PATH"`, powershellActiveDirSegments(langs)),
		}
	case Unknown:
		// The caller couldn't identify $SHELL, so there's no
		// shell-specific syntax to fall back to; POSIX sh is the most
		// broadly interpretable guess, but it's still a guess — e.g. a
		// tcsh/csh user piping this into their rc file would hit a
		// syntax error. Say so, as a shell comment, so the fallback
		// isn't silently wrong.
		return append([]string{
			"# dev could not detect your shell ($SHELL was not recognized); using POSIX sh syntax, which may not be correct for yours",
		}, posixExportLines(devHome, langs)...)
	default: // Bash, Zsh
		return posixExportLines(devHome, langs)
	}
}

func posixExportLines(devHome string, langs []string) []string {
	return []string{
		fmt.Sprintf(`export DEV_HOME=%s`, shellQuote(devHome)),
		fmt.Sprintf(`export PATH="$DEV_HOME:$DEV_HOME/bin%s:$PATH"`, posixActiveDirSegments(langs)),
	}
}

// posixActiveDirSegments returns ":$DEV_HOME/active/<lang>" repeated
// once per lang, in order — empty string when langs is empty, so the
// PATH line is byte-identical to before this feature existed until at
// least one provider is registered.
func posixActiveDirSegments(langs []string) string {
	var b strings.Builder
	for _, lang := range langs {
		b.WriteString(":$DEV_HOME/active/")
		b.WriteString(lang)
	}
	return b.String()
}

// fishActiveDirSegments is posixActiveDirSegments's Fish-syntax
// equivalent — space-separated tokens, not colon-joined.
func fishActiveDirSegments(langs []string) string {
	var b strings.Builder
	for _, lang := range langs {
		b.WriteString(" $DEV_HOME/active/")
		b.WriteString(lang)
	}
	return b.String()
}

// powershellActiveDirSegments is posixActiveDirSegments's
// PowerShell-syntax equivalent — semicolon-joined, backslash paths.
func powershellActiveDirSegments(langs []string) string {
	var b strings.Builder
	for _, lang := range langs {
		b.WriteString(`;$env:DEV_HOME\active\`)
		b.WriteString(lang)
	}
	return b.String()
}
```

- [ ] **Step 4: Update both call sites**

In `cmd/setup.go`, change `lines := shell.ExportLines(sh, devHome)` to `lines := shell.ExportLines(sh, devHome, langManager.Names())`.

In `cmd/env.go`, change `lines := shell.ExportLines(sh, devHome)` to `lines := shell.ExportLines(sh, devHome, langManager.Names())`.

(`langManager` is `cmd/lang.go`'s existing package-level `runtime.NewManager()` with every provider already registered via `providers.Register(langManager)` in that file's `init()` — both `cmd/setup.go` and `cmd/env.go` are in the same `cmd` package, so this is a plain reference, no new import.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./... -v 2>&1 | tail -80`
Expected: PASS across the whole repo — this signature change ripples into any other test file calling `ExportLines` directly; find and fix any you missed via `grep -rn "ExportLines(" --include="*_test.go" .` before considering this step done.

- [ ] **Step 6: Update README**

In `README.md`, find the "PATH and shims" section's description of what `dev setup` puts on `PATH` (the paragraph starting "`dev setup` puts two entries on `PATH`..."). Add a sentence immediately after it:

```
As of this release, `dev setup` also adds one `$DEV_HOME/active/<language>`
entry per supported language — this is what makes a tool installed by that
language's own package manager (`npm install -g pnpm`, a `pip`-installed
console script, `go install`) reachable on `PATH` without `dev` needing to
know its name in advance, the same way `node`/`npm`/`npx` etc. already are.
```

- [ ] **Step 7: Run `go vet` and `gofmt` check**

Run: `/home/linuxbrew/.linuxbrew/bin/go vet ./... && /home/linuxbrew/.linuxbrew/bin/gofmt -l .`
Expected: no output from either command.

- [ ] **Step 8: Commit**

```bash
git add internal/shell/shell.go internal/shell/shell_test.go cmd/setup.go cmd/env.go README.md
git commit -m "$(cat <<'EOF'
feat(shell): add active/<lang> PATH entries per registered provider

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: `dev update` — remind existing installs to re-run `dev setup`

**Files:**
- Modify: `cmd/update.go`, `cmd/update_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: nothing new — only the final success message's text changes.

- [ ] **Step 1: Write the failing test**

Add to `cmd/update_test.go`:

```go
func TestUpdateCommand_SuccessMessageRemindsToRerunSetup(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	withVersion(t, "v0.2.0")
	devBinary := filepath.Join(devHome, "dev")
	if err := os.WriteFile(devBinary, []byte("current dev binary"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	server := stubUpdateServer(t, "v0.3.0", []byte("new dev binary"))
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	originalRefresh := runShimRefresh
	runShimRefresh = func(newBinaryPath string, stdout, stderr io.Writer) error { return nil }
	t.Cleanup(func() { runShimRefresh = originalRefresh })

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"update"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev update` returned error: %v", err)
	}
	if !strings.Contains(out.String(), "dev setup") {
		t.Errorf("output = %q, want it to remind the user to re-run `dev setup`", out.String())
	}
}
```

(This test assumes `fix/update-shim-refresh-uses-old-binary` has already merged — `runShimRefresh` is that PR's seam. If for any reason this task is implemented before that merge, skip this test and the `runShimRefresh` stub, and instead stub whatever the pre-merge equivalent call is at that point in history; but per this plan's Global Constraints, that PR should already be merged by the time Task 6 starts, well before this task.)

- [ ] **Step 2: Run test to verify it fails**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./cmd/... -run TestUpdateCommand_SuccessMessageRemindsToRerunSetup -v`
Expected: FAIL — current success message doesn't mention `dev setup`.

- [ ] **Step 3: Update the success message**

In `cmd/update.go`, change:

```go
		cliutil.Fsuccess(cmd.OutOrStdout(), "Updated to %s.", release.TagName)
		return nil
```

to:

```go
		cliutil.Fsuccess(cmd.OutOrStdout(), "Updated to %s.", release.TagName)
		cliutil.Fstep(cmd.OutOrStdout(), "Run `dev setup` again to pick up any new PATH entries this release adds.")
		return nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `/home/linuxbrew/.linuxbrew/bin/go test ./cmd/... -v`
Expected: PASS — including `TestUpdateCommand_AlreadyLatestPrintsMessage` (that path returns before this line, so it's unaffected) and every other existing `dev update` test.

- [ ] **Step 5: Run the full suite, `go vet`, and `gofmt` one more time**

Run: `/home/linuxbrew/.linuxbrew/bin/go build ./... && /home/linuxbrew/.linuxbrew/bin/go test ./... && /home/linuxbrew/.linuxbrew/bin/go vet ./... && /home/linuxbrew/.linuxbrew/bin/gofmt -l .`
Expected: clean build, all tests pass, no vet/fmt output.

- [ ] **Step 6: Commit**

```bash
git add cmd/update.go cmd/update_test.go
git commit -m "$(cat <<'EOF'
feat(update): remind the user to re-run dev setup after updating

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Post-plan manual smoke test (not a task — run once after Task 8, on whatever platform is available)

```bash
make build
dev setup                        # confirm the new active/<lang> PATH lines appear
source ~/.bashrc                 # or equivalent
dev lang install node 22
dev lang use node 22
node --version                   # works, as before
npm install -g pnpm
pnpm --version                   # works — this is the bug the whole plan exists to fix
dev lang uninstall node 22
node --version                   # friendly "not active" message, not "command not found"
```

Windows-specific: repeat the above in PowerShell after `dev setup`'s Windows path, and separately verify `internal/activebin`'s junction creation actually works (Task 4 could not be verified on this development platform).
