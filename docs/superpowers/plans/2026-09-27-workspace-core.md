# Workspace Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `dev workspace`, `dev workspace new <name>`, and `dev workspace scratch <name>` (aliases `ws`/`ws s`) work: a workspace location asked once and persisted, a `src/scratch/archive/base` scaffold, and template-copying `new`/`scratch` commands.

**Architecture:** `internal/workspace` holds pure filesystem domain logic (scaffold creation, atomic template-copying) with no interactive I/O, mirroring `internal/installer`'s separation of concerns. `internal/platform` gains one pure function suggesting a default workspace path. `cmd/workspace.go` is the only place that touches interactivity (a Huh prompt) or config I/O, via a `resolveWorkspaceRoot` helper every workspace subcommand calls first.

**Tech Stack:** Go stdlib plus one new dependency, `github.com/charmbracelet/huh`, for the first-use workspace-path prompt (explicitly justified by the project's brief; not used anywhere else in this sub-project).

**Spec:** `docs/superpowers/specs/2026-09-27-workspace-core-design.md`

## Global Constraints

- `internal/workspace` never prompts, reads stdin, or touches `internal/config` — it is pure filesystem logic taking already-resolved paths as arguments. All interactivity and config I/O live in `cmd/workspace.go`.
- `EnsureScaffold` is idempotent: creating a scaffold that already (partially) exists is a no-op for the parts that exist, not an error.
- `New`/`Scratch` never mutate the filesystem if the target project name is invalid or the target directory already exists — validation and the existence check both happen before any directory is created.
- `New`/`Scratch`'s template copy is atomic: copy into a temp sibling directory first, then `os.Rename` into place, matching `internal/installer.ExtractAtomic`'s and `internal/runtime/node.extractStrippingTopLevel`'s established pattern. A failure partway through leaves no trace at the destination path.
- A missing or empty `base/` directory is not an error for `New`/`Scratch` — the new project directory is simply created empty.
- The Huh prompt is called through a package-level, swappable function var (`promptWorkspacePath`) in `cmd/workspace.go`, not driven directly through piped test input — the same substitution pattern `cmd/lang_test.go` already uses for `langManager`, applied here because driving a real interactive Huh prompt through scripted stdin isn't practical.
- `platform.DefaultWorkspacePath()` only ever produces a *suggested* default (used to pre-fill the prompt) — it never creates a directory or gets treated as the actual workspace path without the user's answer being saved.
- No test makes a real network call or depends on the real `$HOME`/`$DEV_HOME` — all such tests use `t.TempDir()` plus `t.Setenv("HOME", ...)`/`t.Setenv("DEV_HOME", ...)`, following this codebase's existing convention.
- No test using `t.Setenv` also calls `t.Parallel()` on itself.
- No Testify — stdlib `testing` only.

## Review Focus

- `dev workspace new <name>` (or `scratch`) when `src/<name>` (or `scratch/<name>`) already exists must error without creating, modifying, or deleting anything at that path — a silent overwrite would destroy a real project's contents — Task 3 (`workspace.New`/`Scratch`) and Task 5 (command-level).
- A project name containing `..` or a path separator must be rejected before any filesystem mutation — an unvalidated name reaching `filepath.Join` could write outside the workspace root entirely (the same class of bug `internal/runtime.ValidVersionName` already guards against for language versions) — Task 2.
- Running `dev workspace` (or `new`/`scratch`) a second time after the workspace path is already configured must not re-prompt, re-write the config file, or otherwise behave differently from a workspace that's always existed — the brief's own idempotency example (§39) uses `dev workspace` specifically — Task 4.
- A `base/` directory that's missing or empty must not be treated as an error by `new`/`scratch` — only "no template to copy," resulting in a valid, empty new project — Task 3.
- A copy that fails partway through (e.g. because a file inside `base/` can't be read) must leave no partial directory at the destination — neither an incomplete `src/<name>` nor an orphaned temp directory outside `EnsureScaffold`'s four known subdirectories — Task 3.

---

## Task 1: `internal/platform` — `DefaultWorkspacePath`

**Files:**
- Modify: `internal/platform/platform.go`
- Modify: `internal/platform/platform_test.go`

**Interfaces:**
- Consumes: `os.UserHomeDir()` (stdlib only).
- Produces: `platform.DefaultWorkspacePath() (string, error)` — consumed by `cmd/workspace.go` (Task 4).

- [ ] **Step 1: Write the failing tests**

Add to `internal/platform/platform_test.go`:

```go
func TestDefaultWorkspacePath_UsesDocumentsWhenItExists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.UserHomeDir() reads USERPROFILE on Windows, not HOME")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "Documents"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := DefaultWorkspacePath()
	if err != nil {
		t.Fatalf("DefaultWorkspacePath() returned error: %v", err)
	}
	want := filepath.Join(home, "Documents", "workspace")
	if got != want {
		t.Errorf("DefaultWorkspacePath() = %q, want %q", got, want)
	}
}

func TestDefaultWorkspacePath_FallsBackToHomeWhenNoDocuments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.UserHomeDir() reads USERPROFILE on Windows, not HOME")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := DefaultWorkspacePath()
	if err != nil {
		t.Fatalf("DefaultWorkspacePath() returned error: %v", err)
	}
	want := filepath.Join(home, "workspace")
	if got != want {
		t.Errorf("DefaultWorkspacePath() = %q, want %q", got, want)
	}
}
```

Add `"os"` to this file's imports (it currently imports `"path/filepath"`, `"runtime"`, `"testing"` only).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/platform/... -run TestDefaultWorkspacePath -v`
Expected: FAIL — `undefined: DefaultWorkspacePath`.

- [ ] **Step 3: Write the implementation**

Add to `internal/platform/platform.go`, after `ConfigDir`:

```go
// DefaultWorkspacePath returns a suggested default workspace location:
// <home>/Documents/workspace if a Documents directory already exists
// (the common case on macOS and Windows, and increasingly common on
// Linux desktops), otherwise <home>/workspace. This is only ever used
// to pre-fill the first-use prompt — dev never creates or assumes a
// workspace location without the user confirming it.
func DefaultWorkspacePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving user home directory: %w", err)
	}
	docs := filepath.Join(home, "Documents")
	if info, err := os.Stat(docs); err == nil && info.IsDir() {
		return filepath.Join(docs, "workspace"), nil
	}
	return filepath.Join(home, "workspace"), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/platform/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/platform.go internal/platform/platform_test.go
git commit -m "Add platform.DefaultWorkspacePath for the workspace first-use prompt"
```

---

## Task 2: `internal/workspace` — scaffold and name validation

**Files:**
- Create: `internal/workspace/workspace.go`
- Test: `internal/workspace/workspace_test.go`

**Interfaces:**
- Consumes: `internal/filesystem.EnsureDir(path string, perm os.FileMode) error` (sub-project 1).
- Produces: `workspace.EnsureScaffold(root string) error`, `workspace.ValidProjectName(name string) error` — both consumed by Task 3 (`New`/`Scratch`) and Task 4 (`cmd/workspace.go`).

- [ ] **Step 1: Write the failing tests**

Create `internal/workspace/workspace_test.go`:

```go
package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureScaffold_CreatesAllFourSubdirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("EnsureScaffold() returned error: %v", err)
	}

	for _, name := range []string{"src", "scratch", "archive", "base"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s exists but is not a directory", name)
		}
	}
}

func TestEnsureScaffold_IdempotentOnSecondCall(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("first EnsureScaffold() returned error: %v", err)
	}
	marker := filepath.Join(root, "src", "keep-me.txt")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("second EnsureScaffold() returned error: %v", err)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("second EnsureScaffold() disturbed existing content: %v", err)
	}
}

func TestValidProjectName_RejectsPathTraversal(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", ".", "..", "../escape", "a/b", `a\b`} {
		if err := ValidProjectName(name); err == nil {
			t.Errorf("ValidProjectName(%q) returned nil error, want an error", name)
		}
	}
}

func TestValidProjectName_AcceptsOrdinaryNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"api", "my-project", "project_2"} {
		if err := ValidProjectName(name); err != nil {
			t.Errorf("ValidProjectName(%q) returned error: %v, want nil", name, err)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/workspace/... -v`
Expected: FAIL — the package/directory doesn't exist yet.

- [ ] **Step 3: Write the implementation**

Create `internal/workspace/workspace.go`:

```go
// Package workspace manages the user's development workspace: a
// scaffold of src/scratch/archive/base directories, and creating new
// projects from the base/ template. It never prompts or touches
// config — all interactive I/O and config persistence live in
// cmd/workspace.go, which resolves a root path before calling here.
package workspace

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jpsdm/dev/internal/filesystem"
)

const (
	srcDir     = "src"
	scratchDir = "scratch"
	archiveDir = "archive"
	baseDir    = "base"
)

// EnsureScaffold creates root's src/, scratch/, archive/, and base/
// subdirectories if they don't already exist. Safe to call on every
// invocation — never disturbs a subdirectory that's already there.
func EnsureScaffold(root string) error {
	for _, name := range []string{srcDir, scratchDir, archiveDir, baseDir} {
		if err := filesystem.EnsureDir(filepath.Join(root, name), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// ValidProjectName reports an error if name could escape root when
// used as a path component: empty, ".", "..", or containing a path
// separator. Mirrors internal/runtime.ValidVersionName's checks;
// duplicated here rather than imported — internal/workspace importing
// internal/runtime for a generic string check would be a backwards,
// purely coincidental dependency between two unrelated domains.
func ValidProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("project name must not be empty")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid project name: %q", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid project name: %q", name)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/workspace/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 6: Commit**

```bash
git add internal/workspace/workspace.go internal/workspace/workspace_test.go
git commit -m "Add internal/workspace scaffold creation and name validation"
```

---

## Task 3: `internal/workspace` — `New`/`Scratch` (atomic template copy)

**Files:**
- Modify: `internal/workspace/workspace.go`
- Modify: `internal/workspace/workspace_test.go`

**Interfaces:**
- Consumes: `workspace.ValidProjectName` (Task 2), `filesystem.Exists(path string) bool` (sub-project 1).
- Produces: `workspace.New(root, name string) error`, `workspace.Scratch(root, name string) error` — both consumed by `cmd/workspace.go` (Task 5).

- [ ] **Step 1: Write the failing tests**

Add to `internal/workspace/workspace_test.go`:

```go
func buildBaseFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	base := filepath.Join(root, baseDir)
	for name, content := range files {
		path := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
}

func TestNew_CopiesBaseIntoSrc(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	buildBaseFixture(t, root, map[string]string{
		".editorconfig":     "root = true",
		"README.md":         "hello",
		".vscode/settings.json": "{}",
	})

	if err := New(root, "api"); err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	for name, want := range map[string]string{
		".editorconfig":         "root = true",
		"README.md":             "hello",
		".vscode/settings.json": "{}",
	} {
		got, err := os.ReadFile(filepath.Join(root, srcDir, "api", name))
		if err != nil {
			t.Errorf("reading copied %s: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s content = %q, want %q", name, got, want)
		}
	}
}

func TestNew_EmptyBaseProducesEmptyProject(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := New(root, "api"); err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(root, srcDir, "api"))
	if err != nil {
		t.Fatalf("reading new project directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("new project directory = %v, want empty (base/ was empty)", entries)
	}
}

func TestNew_ExistingProjectErrorsWithoutMutating(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	existing := filepath.Join(root, srcDir, "api")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existing, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	buildBaseFixture(t, root, map[string]string{"README.md": "template"})

	err := New(root, "api")
	if err == nil {
		t.Fatal("New() returned nil error for an already-existing project")
	}

	got, readErr := os.ReadFile(filepath.Join(existing, "keep.txt"))
	if readErr != nil {
		t.Fatalf("existing project's content was lost: %v", readErr)
	}
	if string(got) != "keep" {
		t.Errorf("existing project's content = %q, want unchanged %q", got, "keep")
	}
	if _, err := os.Stat(filepath.Join(existing, "README.md")); !os.IsNotExist(err) {
		t.Error("New() copied the template into an existing project despite erroring")
	}
}

func TestNew_InvalidNameErrorsWithoutTouchingFilesystem(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := New(root, "../escape"); err == nil {
		t.Fatal("New() returned nil error for a path-traversal project name")
	}

	entries, err := os.ReadDir(filepath.Join(root, srcDir))
	if err != nil {
		t.Fatalf("reading src directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("src directory = %v, want empty (invalid name must not create anything)", entries)
	}
}

func TestScratch_CopiesBaseIntoScratch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	buildBaseFixture(t, root, map[string]string{"README.md": "hello"})

	if err := Scratch(root, "experiment"); err != nil {
		t.Fatalf("Scratch() returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, scratchDir, "experiment", "README.md"))
	if err != nil {
		t.Fatalf("reading copied file: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("copied content = %q, want %q", got, "hello")
	}
}

func TestNew_NoTempDirectoryLeftBehindAfterSuccess(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	buildBaseFixture(t, root, map[string]string{"README.md": "hello"})

	if err := New(root, "api"); err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(root, srcDir))
	if err != nil {
		t.Fatalf("reading src directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "api" {
		t.Errorf("src directory = %v, want exactly one entry named %q (no leftover temp directory)", entries, "api")
	}
}

func TestNew_FailedCopyLeavesNoPartialDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	base := filepath.Join(root, baseDir)
	if err := os.WriteFile(filepath.Join(base, "a-good-file.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// A symlink whose target doesn't exist: os.ReadDir lists it as a
	// non-directory entry (its own Lstat-based type, not the target's),
	// so copyTree routes it to copyFile, which then fails resolving the
	// broken target via os.Stat — a deterministic, portable way to force
	// a failure partway through the copy loop without relying on
	// permission bits (which behave inconsistently when tests run as
	// root, and not at all the same way on Windows).
	if err := os.Symlink(filepath.Join(root, "does-not-exist"), filepath.Join(base, "broken-link")); err != nil {
		t.Skipf("cannot create symlinks on this system: %v", err)
	}

	err := New(root, "api")
	if err == nil {
		t.Fatal("New() returned nil error for a base/ containing an unreadable entry")
	}

	if _, statErr := os.Stat(filepath.Join(root, srcDir, "api")); !os.IsNotExist(statErr) {
		t.Error("New() left a partial project directory behind after a failed copy")
	}
	entries, readErr := os.ReadDir(filepath.Join(root, srcDir))
	if readErr != nil {
		t.Fatalf("reading src directory: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("src directory = %v, want empty (no orphaned temp directory after a failed copy)", entries)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/workspace/... -run 'TestNew|TestScratch' -v`
Expected: FAIL — `undefined: New`, `undefined: Scratch`.

- [ ] **Step 3: Write the implementation**

Add to `internal/workspace/workspace.go` (add `"io"` and `"os"` to the imports):

```go
// New creates root/src/<name> by copying root/base/'s contents into
// it. Returns an error, without touching the filesystem, if name is
// invalid or root/src/<name> already exists.
func New(root, name string) error {
	return createFromBase(root, srcDir, name)
}

// Scratch does the same as New, under root/scratch/<name>.
func Scratch(root, name string) error {
	return createFromBase(root, scratchDir, name)
}

func createFromBase(root, kind, name string) error {
	if err := ValidProjectName(name); err != nil {
		return err
	}
	dest := filepath.Join(root, kind, name)
	if filesystem.Exists(dest) {
		return fmt.Errorf("project %q already exists at %s", name, dest)
	}

	parent := filepath.Join(root, kind)
	tmp, err := os.MkdirTemp(parent, ".tmp-"+name+"-*")
	if err != nil {
		return fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	base := filepath.Join(root, baseDir)
	if err := copyTree(base, tmp); err != nil {
		return fmt.Errorf("copying template: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("moving %s into place: %w", dest, err)
	}
	return nil
}

// copyTree recursively copies src's contents into dst (which must
// already exist), preserving hidden files, nested directories, and
// file permissions. A missing src is not an error — dst is simply left
// empty, since an unpopulated base/ is a valid starting state. A
// symlink in src is dereferenced (its target's content is copied as a
// regular file) rather than recreated as a symlink.
func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading %s: %w", src, err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("reading info for %s: %w", srcPath, err)
			}
			if err := os.MkdirAll(dstPath, info.Mode().Perm()); err != nil {
				return fmt.Errorf("creating %s: %w", dstPath, err)
			}
			if err := copyTree(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}

		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("reading info for %s: %w", src, err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}
	return out.Close()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/workspace/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 6: Commit**

```bash
git add internal/workspace/workspace.go internal/workspace/workspace_test.go
git commit -m "Add workspace.New and workspace.Scratch (atomic template copy)"
```

---

## Task 4: Add Huh dependency + `cmd/workspace.go` — first-use prompt and bare `workspace` command

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `cmd/workspace.go`
- Test: `cmd/workspace_test.go`

**Interfaces:**
- Consumes: `config.DefaultPath() (string, error)`, `config.Load(path string) (*Config, error)`, `config.Save(path string, cfg *Config) error`, `config.ErrNotFound`, `config.Config`, `config.WorkspaceConfig` (sub-project 1), `platform.DefaultWorkspacePath()` (Task 1), `workspace.EnsureScaffold(root string) error` (Task 2), `cliutil.Fsuccess(w io.Writer, format string, args ...any)` (sub-project 2b).
- Produces: `resolveWorkspaceRoot() (string, error)`, the package-level `promptWorkspacePath func(defaultPath string) (string, error)` var, `workspaceCmd` (Cobra command, `Use: "workspace"`, alias `ws`) — all consumed by Task 5 (`new`/`scratch` subcommands).

- [ ] **Step 1: Add the Huh dependency**

```bash
go get github.com/charmbracelet/huh@latest
```

This updates `go.mod`/`go.sum` with `github.com/charmbracelet/huh` as a direct dependency, plus its transitive dependencies (Bubble Tea, Lip Gloss, and related Charm libraries) as indirect ones.

- [ ] **Step 2: Run the whole module's tests to confirm the new dependency doesn't break anything**

Run: `go build ./... && go test ./...`
Expected: all succeed (no source file references Huh yet, so this is purely confirming `go.mod`/`go.sum` are consistent).

- [ ] **Step 3: Commit the dependency addition on its own**

```bash
git add go.mod go.sum
git commit -m "Add github.com/charmbracelet/huh for the workspace first-use prompt"
```

- [ ] **Step 4: Write the failing tests**

Create `cmd/workspace_test.go`:

```go
// workspace's tests substitute the package-level promptWorkspacePath
// var rather than driving a real interactive Huh prompt through piped
// stdin — the same substitution pattern cmd/lang_test.go already uses
// for langManager. None of them use t.Parallel() against each other
// since they share that package-level var.
package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubWorkspacePrompt(t *testing.T, answer string) *int {
	t.Helper()
	calls := 0
	orig := promptWorkspacePath
	promptWorkspacePath = func(defaultPath string) (string, error) {
		calls++
		return answer, nil
	}
	t.Cleanup(func() { promptWorkspacePath = orig })
	return &calls
}

func TestWorkspaceCommand_FirstUsePromptsAndPersistsPath(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	workspaceDir := t.TempDir()
	stubWorkspacePrompt(t, workspaceDir)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace` returned error: %v", err)
	}

	if !strings.Contains(out.String(), workspaceDir) {
		t.Errorf("output = %q, want it to mention the workspace path %q", out.String(), workspaceDir)
	}
	for _, name := range []string{"src", "scratch", "archive", "base"} {
		if _, err := os.Stat(filepath.Join(workspaceDir, name)); err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
		}
	}

	cfgPath := filepath.Join(devHome, "config", "config.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reading config file: %v", err)
	}
	if !strings.Contains(string(data), workspaceDir) {
		t.Errorf("config file = %q, want it to persist the chosen workspace path", data)
	}
}

func TestWorkspaceCommand_SecondRunDoesNotReprompt(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	workspaceDir := t.TempDir()
	calls := stubWorkspacePrompt(t, workspaceDir)

	run := func() {
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		rootCmd.SetArgs([]string{"workspace"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("`dev workspace` returned error: %v", err)
		}
	}
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	run()
	run()

	if *calls != 1 {
		t.Errorf("prompt called %d times, want exactly 1 (second run should reuse the persisted path)", *calls)
	}
}

func TestWorkspaceCommand_WsAliasWorks(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	workspaceDir := t.TempDir()
	stubWorkspacePrompt(t, workspaceDir)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"ws"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev ws` returned error: %v", err)
	}
	if !strings.Contains(out.String(), workspaceDir) {
		t.Errorf("output = %q, want it to mention the workspace path", out.String())
	}
}
```

- [ ] **Step 5: Run tests to verify they fail**

Run: `go test ./cmd/... -run TestWorkspaceCommand -v`
Expected: FAIL — `undefined: promptWorkspacePath` / `undefined: workspaceCmd` (package has no implementation yet).

- [ ] **Step 6: Write the implementation**

Create `cmd/workspace.go`:

```go
package cmd

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/config"
	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/workspace"
)

// promptWorkspacePath asks the user where dev should keep their
// workspace, pre-filled with defaultPath, and returns their answer.
// Tests replace this package-level var with a stub — driving a real
// interactive Huh prompt through piped test input isn't practical, so
// the swap happens here instead, the same way cmd/lang_test.go
// substitutes langManager rather than trying to drive a real registry
// through I/O.
var promptWorkspacePath = huhPromptWorkspacePath

func huhPromptWorkspacePath(defaultPath string) (string, error) {
	value := defaultPath
	err := huh.NewInput().
		Title("Where should dev keep your workspace?").
		Value(&value).
		Validate(func(s string) error {
			if s == "" {
				return fmt.Errorf("a workspace path is required")
			}
			return nil
		}).
		Run()
	if err != nil {
		return "", fmt.Errorf("prompting for workspace path: %w", err)
	}
	return value, nil
}

// resolveWorkspaceRoot returns the workspace root directory, prompting
// for and persisting it on first use (when config.Config.Workspace.Path
// is empty), and ensuring its scaffold subdirectories exist on every
// call. Every workspace subcommand calls this first.
func resolveWorkspaceRoot() (string, error) {
	path, err := config.DefaultPath()
	if err != nil {
		return "", err
	}
	cfg, err := config.Load(path)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return "", err
	}

	if cfg.Workspace.Path == "" {
		def, err := platform.DefaultWorkspacePath()
		if err != nil {
			return "", err
		}
		chosen, err := promptWorkspacePath(def)
		if err != nil {
			return "", err
		}
		cfg.Workspace.Path = chosen
		if err := config.Save(path, cfg); err != nil {
			return "", err
		}
	}

	if err := workspace.EnsureScaffold(cfg.Workspace.Path); err != nil {
		return "", err
	}
	return cfg.Workspace.Path, nil
}

var workspaceCmd = &cobra.Command{
	Use:     "workspace",
	Aliases: []string{"ws"},
	Short:   "Manage your development workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveWorkspaceRoot()
		if err != nil {
			return err
		}
		cliutil.Fsuccess(cmd.OutOrStdout(), "Workspace at %s", root)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(workspaceCmd)
}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./cmd/... -run TestWorkspaceCommand -v`
Expected: PASS for all three tests.

- [ ] **Step 8: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 9: Commit**

```bash
git add cmd/workspace.go cmd/workspace_test.go
git commit -m "Add dev workspace command with first-use path prompt"
```

---

## Task 5: `cmd/workspace.go` — `new` and `scratch` subcommands

**Files:**
- Modify: `cmd/workspace.go`
- Modify: `cmd/workspace_test.go`

**Interfaces:**
- Consumes: `resolveWorkspaceRoot() (string, error)` (Task 4), `workspace.New(root, name string) error`, `workspace.Scratch(root, name string) error`, `workspace.EnsureScaffold(root string) error` (Tasks 2/3), `config.DefaultPath`, `config.Save`, `config.Config`, `config.WorkspaceConfig` (sub-project 1).
- Produces: `workspaceNewCmd`, `workspaceScratchCmd` (Cobra commands) — no other task depends on these.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/workspace_test.go` (add `"github.com/jpsdm/dev/internal/config"` and `"github.com/jpsdm/dev/internal/workspace"` to the imports):

```go
// writeConfiguredWorkspace pre-populates DEV_HOME's config file so
// resolveWorkspaceRoot skips the first-use prompt entirely, pointing
// at root (already containing an empty scaffold).
func writeConfiguredWorkspace(t *testing.T, root string) {
	t.Helper()
	if err := workspace.EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	cfg := &config.Config{Workspace: config.WorkspaceConfig{Path: root}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("setup: %v", err)
	}
}

func TestWorkspaceNewCommand_CopiesBaseIntoSrc(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	if err := os.WriteFile(filepath.Join(root, "base", "README.md"), []byte("template"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "new", "api"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace new` returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "src", "api", "README.md"))
	if err != nil {
		t.Fatalf("reading copied file: %v", err)
	}
	if string(got) != "template" {
		t.Errorf("copied content = %q, want %q", got, "template")
	}
}

func TestWorkspaceNewCommand_ExistingProjectErrors(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	if err := os.MkdirAll(filepath.Join(root, "src", "api"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "new", "api"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("`dev workspace new` returned nil error for an already-existing project")
	}
}

func TestWorkspaceScratchCommand_WsSAliasChain(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"ws", "s", "experiment"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev ws s` returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "scratch", "experiment")); err != nil {
		t.Errorf("expected scratch/experiment to exist: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/... -run 'TestWorkspaceNewCommand|TestWorkspaceScratchCommand' -v`
Expected: FAIL — `undefined: workspaceNewCmd` (build error, since `new`/`scratch` aren't registered subcommands yet).

- [ ] **Step 3: Write the implementation**

Add to `cmd/workspace.go` (add `"path/filepath"` to the imports):

```go
var workspaceNewCmd = &cobra.Command{
	Use:   "new <name>",
	Args:  cobra.ExactArgs(1),
	Short: "Create a new project from the workspace template",
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveWorkspaceRoot()
		if err != nil {
			return err
		}
		if err := workspace.New(root, args[0]); err != nil {
			return err
		}
		cliutil.Fsuccess(cmd.OutOrStdout(), "Created %s", filepath.Join(root, "src", args[0]))
		return nil
	},
}

var workspaceScratchCmd = &cobra.Command{
	Use:     "scratch <name>",
	Aliases: []string{"s"},
	Args:    cobra.ExactArgs(1),
	Short:   "Create a new scratch project from the workspace template",
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveWorkspaceRoot()
		if err != nil {
			return err
		}
		if err := workspace.Scratch(root, args[0]); err != nil {
			return err
		}
		cliutil.Fsuccess(cmd.OutOrStdout(), "Created %s", filepath.Join(root, "scratch", args[0]))
		return nil
	},
}
```

Update this file's `init()` function to register both as subcommands of `workspaceCmd`:

```go
func init() {
	rootCmd.AddCommand(workspaceCmd)
	workspaceCmd.AddCommand(workspaceNewCmd)
	workspaceCmd.AddCommand(workspaceScratchCmd)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/... -run 'TestWorkspace' -v`
Expected: PASS for every `TestWorkspace*` test, including Task 4's.

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 6: Commit**

```bash
git add cmd/workspace.go cmd/workspace_test.go
git commit -m "Add dev workspace new and scratch subcommands"
```

---

## Task 6: Final acceptance verification

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: everything produced by Tasks 1–5.
- Produces: nothing new; verifies this sub-project's acceptance criteria against the real filesystem and leaves the tree committed and clean.

- [ ] **Step 1: Check for a stray `DEV_HOME` before doing anything real**

```bash
echo "DEV_HOME=${DEV_HOME:-<unset>}"
echo "HOME=$HOME"
```

If `DEV_HOME` is set to something unexpected, note it and proceed using whatever it actually resolves to — do not silently override it.

- [ ] **Step 2: Run the full check chain**

```bash
make check
```

Expected: `fmt`, `vet`, `lint`, `test`, `build` all succeed.

- [ ] **Step 3: Verify the first-use prompt for real**

```bash
./dev workspace
```

Expected: an interactive Huh prompt titled "Where should dev keep your workspace?", pre-filled with a real suggested default (`~/Documents/workspace` if that directory exists on this machine, else `~/workspace`). Type a temporary test path (recommended, to avoid touching your real home directory) and submit. Expect a `✓ Workspace at <path>` line afterward.

- [ ] **Step 4: Derive the configured path and verify the scaffold + idempotency**

```bash
WS=$(grep -o '"path": *"[^"]*"' "$DEV_HOME/config/config.json" | sed -E 's/.*"([^"]+)"$/\1/')
echo "workspace path: $WS"
ls "$WS"
./dev workspace
```

Expected: `ls` shows `src`, `scratch`, `archive`, `base` all present. The second `./dev workspace` call must print `✓ Workspace at $WS` immediately, with no prompt at all — confirming Task 4's "second run doesn't reprompt" behavior for real, not just under the test's stubbed prompt.

- [ ] **Step 5: Verify `new` and `scratch` actually copy the template**

```bash
echo "hello from base" > "$WS/base/README.md"
./dev workspace new api
cat "$WS/src/api/README.md"
./dev ws s experiment
cat "$WS/scratch/experiment/README.md"
```

Expected: both `cat` commands print `hello from base`, confirming the template was actually copied by both the full command and the `ws s` alias chain.

- [ ] **Step 6: Verify the already-exists error path**

```bash
./dev workspace new api
echo "exit code: $?"
```

Expected: a `✗`-prefixed error naming `api` and that it already exists, non-zero exit code, and `$WS/src/api/README.md` still reads `hello from base` (unchanged, not duplicated or corrupted).

- [ ] **Step 7: Update the README**

Add a "Workspace" section to `README.md` after the existing "PATH and shims" section:

```markdown

## Workspace

    dev workspace                  # ensure the workspace is initialized (asks for a path on first use)
    dev workspace new api          # create src/api from the base/ template
    dev workspace scratch spike    # create scratch/spike from the base/ template

Aliases: `ws` for `workspace`, `ws s` for `scratch`.

The workspace root holds `src/` (active projects), `scratch/`
(experiments), `archive/` (archived projects — sub-project 3b), and
`base/` (files copied into every new project or scratch — e.g.
`.editorconfig`, `.gitignore`, `README.md`). `clean`/`archive`/`metrics`
are not implemented yet.
```

- [ ] **Step 8: Commit**

```bash
git add README.md
git commit -m "Document dev workspace, new, and scratch in the README"
```

- [ ] **Step 9: Final confirmation**

```bash
make check
git status --short
```

Expected: `make check` passes and `git status --short` is empty.
