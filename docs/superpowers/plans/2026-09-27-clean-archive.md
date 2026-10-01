# Clean & Archive Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `dev workspace clean <name>`, `dev workspace clean --all`, and `dev workspace archive <name>` (aliases `c`/`a`) work, each with `--dry-run` support, using real `.gitignore` semantics to decide what gets removed.

**Architecture:** `internal/workspace` gains `clean.go` (gitignore pattern collection + `Clean`/`CleanAll`, pure filesystem logic, no interactive I/O — same separation `workspace.go` already established in 3a) and `archive.go` (`Archive`, built on `Clean`). `cmd/workspace.go` gains two new Cobra commands that resolve the workspace root (reusing 3a's `resolveWorkspaceRoot`) and call into the new `internal/workspace` functions; `clean --all`'s confirmation reuses `internal/shell.Confirm`, the same stdin-based y/N prompt `dev setup` already uses (not Huh — a single confirmation doesn't need a form).

**Tech Stack:** Go stdlib plus one new dependency, `github.com/go-git/go-git/v5/plumbing/format/gitignore`, for real `.gitignore` pattern matching (nested files, negation) without requiring the target to be an actual git repository.

**Spec:** `docs/superpowers/specs/2026-09-27-clean-archive-design.md`

## Global Constraints

- `internal/workspace` (both new files) never prompts, reads stdin, or touches `internal/config` — pure filesystem logic, exactly like `workspace.go`'s existing separation. All interactivity lives in `cmd/workspace.go`.
- `Clean`/`Archive` operate only on `root/src/<name>` — never `scratch/` (out of scope per spec's Non-goals).
- `Clean` removes exactly the set of paths `.gitignore` rules mark ignored (the `git clean -Xdf` set) — never requires the target to be a git repository, never shells out to the real `git` binary.
- A project's top-level `.git` directory, if present, is never a removal candidate, regardless of what any `.gitignore` pattern matches. This check applies only at the project root (`rel == ".git"` or `rel` starting with `".git" + separator`) — a nested `.git` deeper in the tree is out of scope per the spec.
- A removed directory is reported and deleted as one unit (trailing `/` in its reported path, never descended into once matched) — never itemized file-by-file, matching how real `git clean` reports removals.
- `Archive` checks `root/archive/<name>` doesn't already exist *before* running `Clean` — never destructively cleans a project it can't actually move.
- `Archive` never moves `src/<name>` to `archive/<name>` if the `Clean` step returns an error.
- `clean <name>` and `archive <name>` (single-project forms) never prompt for confirmation — only `clean --all` requires it, and only when it isn't `--dry-run` (nothing is deleted in dry-run, so nothing to confirm).
- `--dry-run` output for `clean`/`archive` follows the spec's format exactly: `"The following files would be removed:"`, the list (one path per line, directories with a trailing `/`), a blank line, then `"No files were deleted."` — no other line before or after.
- No test using `t.Setenv` also calls `t.Parallel()` on itself. No Testify — stdlib `testing` only.
- No permission-bit-based failure injection in any test (flaky when tests run as root, inconsistent cross-platform) — use a directory named `.gitignore` (forcing `os.ReadFile` to fail with a real, deterministic, portable "is a directory" error) wherever a test needs to force a real collection/read failure.

## Review Focus

- A `.gitignore` pattern that would match `.git` itself (e.g. a deliberately adversarial `.git/` line) must never cause `.git`'s contents to be removed — the explicit top-level-`.git` guard must win over the matcher's own verdict, not just be redundant with it — Task 1.
- A file re-included by a nested `.gitignore`'s negation (`!pattern`) after a parent directory's `.gitignore` ignored it must survive `clean` — this is the exact "nested `.gitignore` scoping" correctness the whole new dependency exists to get right, and a project with none of this in its test fixtures would not actually prove the library integration works — Task 1.
- `dev workspace clean --all` must make zero filesystem changes when the user declines the confirmation prompt — a bulk destructive operation with a silently-ignored "no" would be a serious trust break — Task 4.
- `dev workspace archive <name>` must leave `src/<name>` completely untouched (not partially cleaned) when `archive/<name>` already exists — the existence check has to run *before* any cleaning starts, not after a clean that then fails to move — Task 3 and Task 5.
- `dev workspace clean <name> --dry-run`'s printed output must match the spec's exact wording — a subtly different format (missing the blank line, a different closing sentence) would silently violate the one requirement the brief gives verbatim — Task 4.

---

## Task 1: `internal/workspace/clean.go` — gitignore collection + `Clean`

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/workspace/clean.go`
- Create: `internal/workspace/clean_test.go`

**Interfaces:**
- Consumes: `filesystem.Exists(path string) bool` (sub-project 1), the existing unexported `srcDir` constant in `internal/workspace/workspace.go`.
- Produces: `workspace.Clean(root, name string, dryRun bool) ([]string, error)` — consumed by Task 2 (`CleanAll`), Task 3 (`Archive`), and Task 4 (`cmd/workspace.go`'s clean command).

- [ ] **Step 1: Add the go-git gitignore dependency**

```bash
go get github.com/go-git/go-git/v5
```

This adds `github.com/go-git/go-git/v5` (and its transitive dependencies) to `go.mod`/`go.sum`. Only the `plumbing/format/gitignore` subpackage will actually be imported by this codebase's source — the rest of go-git is pulled in as an unused-but-required module dependency, which is normal and expected for importing one subpackage of a larger module.

- [ ] **Step 2: Confirm the dependency resolves and nothing else broke**

Run: `go build ./... && go test ./...`
Expected: all succeed (no source file references the new package yet, so this just confirms `go.mod`/`go.sum` are consistent).

- [ ] **Step 3: Commit the dependency addition on its own**

```bash
git add go.mod go.sum
git commit -m "Add github.com/go-git/go-git/v5 for real .gitignore pattern matching"
```

- [ ] **Step 4: Verify the gitignore package's real API before writing against it**

Run: `go doc github.com/go-git/go-git/v5/plumbing/format/gitignore`

This plan assumes the package exposes `type Pattern interface { Match(path []string, isDir bool) MatchResult }`, `func ParsePattern(line string, domain []string) Pattern`, `type Matcher interface { Match(path []string, isDir bool) bool }`, and `func NewMatcher(patterns []Pattern) Matcher`. If the real API differs from this (different function names, different signatures), STOP and report back with NEEDS_CONTEXT describing the actual API surface — do not guess or silently adapt. If it matches, proceed.

- [ ] **Step 5: Write the failing tests**

Create `internal/workspace/clean_test.go`:

```go
package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeFile creates path (and its parent directories) with content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
}

func TestClean_RemovesIgnoredFilesAndDirectoriesOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	proj := filepath.Join(root, srcDir, "api")
	writeFile(t, filepath.Join(proj, ".gitignore"), "node_modules/\n*.log\n")
	writeFile(t, filepath.Join(proj, "app.go"), "package main")
	writeFile(t, filepath.Join(proj, "debug.log"), "log content")
	writeFile(t, filepath.Join(proj, "node_modules", "pkg", "index.js"), "module")

	removed, err := Clean(root, "api", false)
	if err != nil {
		t.Fatalf("Clean() returned error: %v", err)
	}

	sort.Strings(removed)
	want := []string{"debug.log", "node_modules/"}
	if len(removed) != len(want) {
		t.Fatalf("Clean() removed = %v, want %v", removed, want)
	}
	for i := range want {
		if removed[i] != want[i] {
			t.Errorf("Clean() removed[%d] = %q, want %q", i, removed[i], want[i])
		}
	}

	if _, err := os.Stat(filepath.Join(proj, "app.go")); err != nil {
		t.Errorf("app.go was removed, want it kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".gitignore")); err != nil {
		t.Errorf(".gitignore was removed, want it kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "debug.log")); !os.IsNotExist(err) {
		t.Error("debug.log still exists, want it removed")
	}
	if _, err := os.Stat(filepath.Join(proj, "node_modules")); !os.IsNotExist(err) {
		t.Error("node_modules still exists, want it removed")
	}
}

func TestClean_NestedGitignoreNegationReincludesFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	proj := filepath.Join(root, srcDir, "api")
	writeFile(t, filepath.Join(proj, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(proj, "debug.log"), "root log")
	writeFile(t, filepath.Join(proj, "logs", ".gitignore"), "!important.log\n")
	writeFile(t, filepath.Join(proj, "logs", "important.log"), "keep me")
	writeFile(t, filepath.Join(proj, "logs", "debug.log"), "nested log")

	removed, err := Clean(root, "api", false)
	if err != nil {
		t.Fatalf("Clean() returned error: %v", err)
	}

	removedSet := map[string]bool{}
	for _, r := range removed {
		removedSet[r] = true
	}
	if !removedSet["debug.log"] {
		t.Error("root debug.log was not removed, want it removed")
	}
	if !removedSet["logs/debug.log"] {
		t.Error("logs/debug.log was not removed, want it removed")
	}
	if removedSet["logs/important.log"] {
		t.Error("logs/important.log was removed, want it kept (re-included by nested negation)")
	}
	if _, err := os.Stat(filepath.Join(proj, "logs", "important.log")); err != nil {
		t.Errorf("logs/important.log missing from disk: %v", err)
	}
}

func TestClean_DryRunRemovesNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	proj := filepath.Join(root, srcDir, "api")
	writeFile(t, filepath.Join(proj, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(proj, "debug.log"), "log content")
	writeFile(t, filepath.Join(proj, "app.go"), "package main")

	removed, err := Clean(root, "api", true)
	if err != nil {
		t.Fatalf("Clean() returned error: %v", err)
	}
	if len(removed) != 1 || removed[0] != "debug.log" {
		t.Errorf("Clean(dryRun=true) removed = %v, want [\"debug.log\"]", removed)
	}
	if _, err := os.Stat(filepath.Join(proj, "debug.log")); err != nil {
		t.Errorf("debug.log was actually removed during a dry run: %v", err)
	}
}

func TestClean_ProtectsGitDirectoryEvenIfPatternMatchesIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	proj := filepath.Join(root, srcDir, "api")
	// A deliberately adversarial pattern: a real .gitignore would never
	// contain this, but it proves the .git guard wins over the matcher's
	// own verdict rather than merely happening to agree with it.
	writeFile(t, filepath.Join(proj, ".gitignore"), ".git/\n")
	writeFile(t, filepath.Join(proj, ".git", "HEAD"), "ref: refs/heads/main")
	writeFile(t, filepath.Join(proj, "app.go"), "package main")

	removed, err := Clean(root, "api", false)
	if err != nil {
		t.Fatalf("Clean() returned error: %v", err)
	}
	for _, r := range removed {
		if r == ".git/" {
			t.Errorf("Clean() reported removing .git/, want it always protected")
		}
	}
	if _, err := os.Stat(filepath.Join(proj, ".git", "HEAD")); err != nil {
		t.Errorf(".git/HEAD missing after Clean(): %v", err)
	}
}

func TestClean_NonexistentProjectReturnsError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := Clean(root, "does-not-exist", false)
	if err == nil {
		t.Fatal("Clean() returned nil error for a nonexistent project")
	}
}

func TestClean_UnreadableGitignoreReturnsError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	proj := filepath.Join(root, srcDir, "api")
	// A directory named ".gitignore" forces os.ReadFile to fail with a
	// real, deterministic, portable "is a directory" error — no
	// permission bits involved, so this doesn't flake when tests run as
	// root (unlike a chmod-based approach).
	if err := os.MkdirAll(filepath.Join(proj, ".gitignore"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := Clean(root, "api", false)
	if err == nil {
		t.Fatal("Clean() returned nil error when .gitignore could not be read")
	}
}
```

- [ ] **Step 6: Run tests to verify they fail**

Run: `go test ./internal/workspace/... -run TestClean -v`
Expected: FAIL — `undefined: Clean` (the package/file doesn't exist yet).

- [ ] **Step 7: Write the implementation**

Create `internal/workspace/clean.go`:

```go
// clean.go implements gitignore-aware cleanup of a project directory:
// collecting every .gitignore file's patterns (honoring nested
// directory scoping and negation), then removing exactly the paths
// those patterns mark ignored.
package workspace

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"

	"github.com/jpsdm/dev/internal/filesystem"
)

// isGitDir reports whether rel (a path relative to a project root, as
// produced by filepath.Rel) names the project's own top-level .git
// directory or something inside it. A nested .git deeper in the tree
// is not protected — out of scope for this project.
func isGitDir(rel string) bool {
	return rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator))
}

// collectGitignorePatterns walks projectDir looking for .gitignore
// files at every level (skipping the project's own .git directory),
// parsing each into gitignore.Patterns scoped to the directory
// (relative to projectDir) they were found in — this domain scoping is
// what lets the resulting Matcher correctly apply git's real nested-
// .gitignore precedence and negation rules.
func collectGitignorePatterns(projectDir string) ([]gitignore.Pattern, error) {
	var patterns []gitignore.Pattern
	err := filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == projectDir {
			return nil
		}
		rel, relErr := filepath.Rel(projectDir, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			if isGitDir(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != ".gitignore" {
			return nil
		}
		relDir := filepath.Dir(rel)
		var domain []string
		if relDir != "." {
			domain = strings.Split(relDir, string(filepath.Separator))
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimRight(line, "\r")
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			patterns = append(patterns, gitignore.ParsePattern(line, domain))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("collecting .gitignore patterns under %s: %w", projectDir, err)
	}
	return patterns, nil
}

// Clean removes every path under root/src/<name> that .gitignore rules
// (collected from every .gitignore file in the project, honoring
// nested scoping and negation) mark ignored — the same set `git clean
// -Xdf` would remove. It never requires <name> to be a git repository.
// The project's own top-level .git directory, if present, is never a
// candidate for removal. Returns the list of removed (or, if dryRun,
// would-be-removed) paths relative to the project root, with a
// trailing "/" on directory entries — a matched directory is reported
// and removed as a single unit, never descended into, matching how
// real `git clean` reports removals. Returns an error, with nothing
// removed, if the project doesn't exist.
func Clean(root, name string, dryRun bool) ([]string, error) {
	projectDir := filepath.Join(root, srcDir, name)
	if !filesystem.Exists(projectDir) {
		return nil, fmt.Errorf("project %q does not exist at %s", name, projectDir)
	}

	patterns, err := collectGitignorePatterns(projectDir)
	if err != nil {
		return nil, err
	}
	matcher := gitignore.NewMatcher(patterns)

	var matched []string
	err = filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == projectDir {
			return nil
		}
		rel, relErr := filepath.Rel(projectDir, path)
		if relErr != nil {
			return relErr
		}
		if isGitDir(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		components := strings.Split(rel, string(filepath.Separator))
		if matcher.Match(components, d.IsDir()) {
			if d.IsDir() {
				matched = append(matched, rel+"/")
				return fs.SkipDir
			}
			matched = append(matched, rel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", projectDir, err)
	}

	if dryRun {
		return matched, nil
	}

	for _, entry := range matched {
		full := filepath.Join(projectDir, strings.TrimSuffix(entry, "/"))
		if err := os.RemoveAll(full); err != nil {
			return matched, fmt.Errorf("removing %s: %w", full, err)
		}
	}
	return matched, nil
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/workspace/... -run TestClean -v`
Expected: PASS for all tests.

- [ ] **Step 9: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 10: Commit**

```bash
git add internal/workspace/clean.go internal/workspace/clean_test.go
git commit -m "Add gitignore-aware workspace.Clean"
```

---

## Task 2: `internal/workspace/clean.go` — `CleanAll`

**Files:**
- Modify: `internal/workspace/clean.go`
- Modify: `internal/workspace/clean_test.go`

**Interfaces:**
- Consumes: `Clean(root, name string, dryRun bool) ([]string, error)` (Task 1), the existing unexported `srcDir` constant.
- Produces: `workspace.CleanAll(root string, dryRun bool) (map[string][]string, error)` — consumed by Task 4 (`cmd/workspace.go`'s `clean --all`).

- [ ] **Step 1: Write the failing tests**

Add to `internal/workspace/clean_test.go`:

```go
func TestCleanAll_CleansEveryProjectIndependently(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	writeFile(t, filepath.Join(root, srcDir, "api", ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, srcDir, "api", "debug.log"), "log")
	writeFile(t, filepath.Join(root, srcDir, "api", "app.go"), "package main")
	writeFile(t, filepath.Join(root, srcDir, "web", ".gitignore"), "dist/\n")
	writeFile(t, filepath.Join(root, srcDir, "web", "dist", "bundle.js"), "bundled")
	writeFile(t, filepath.Join(root, srcDir, "web", "index.html"), "<html></html>")

	results, err := CleanAll(root, false)
	if err != nil {
		t.Fatalf("CleanAll() returned error: %v", err)
	}
	if len(results["api"]) != 1 || results["api"][0] != "debug.log" {
		t.Errorf(`results["api"] = %v, want ["debug.log"]`, results["api"])
	}
	if len(results["web"]) != 1 || results["web"][0] != "dist/" {
		t.Errorf(`results["web"] = %v, want ["dist/"]`, results["web"])
	}
	if _, err := os.Stat(filepath.Join(root, srcDir, "api", "debug.log")); !os.IsNotExist(err) {
		t.Error("api/debug.log still exists")
	}
	if _, err := os.Stat(filepath.Join(root, srcDir, "web", "dist")); !os.IsNotExist(err) {
		t.Error("web/dist still exists")
	}
	if _, err := os.Stat(filepath.Join(root, srcDir, "web", "index.html")); err != nil {
		t.Errorf("web/index.html missing: %v", err)
	}
}

func TestCleanAll_IsolatesOneProjectsFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	writeFile(t, filepath.Join(root, srcDir, "good", ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, srcDir, "good", "debug.log"), "log")
	// Same "directory named .gitignore" trick as Task 1's
	// TestClean_UnreadableGitignoreReturnsError, forcing this one
	// project's Clean to fail deterministically.
	if err := os.MkdirAll(filepath.Join(root, srcDir, "broken", ".gitignore"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	results, err := CleanAll(root, false)
	if err == nil {
		t.Fatal("CleanAll() returned nil error despite one project failing")
	}
	if len(results["good"]) != 1 || results["good"][0] != "debug.log" {
		t.Errorf(`results["good"] = %v, want ["debug.log"] (the other project's success must still be reported)`, results["good"])
	}
	if _, err := os.Stat(filepath.Join(root, srcDir, "good", "debug.log")); !os.IsNotExist(err) {
		t.Error("good/debug.log still exists — the working project should have been cleaned despite the other one failing")
	}
}

func TestCleanAll_EmptySrcReturnsEmptyResultNoError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}

	results, err := CleanAll(root, false)
	if err != nil {
		t.Fatalf("CleanAll() returned error for an empty src/: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("CleanAll() = %v, want empty map", results)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/workspace/... -run TestCleanAll -v`
Expected: FAIL — `undefined: CleanAll`.

- [ ] **Step 3: Write the implementation**

Add to `internal/workspace/clean.go` (add `"errors"` to the imports):

```go
// CleanAll runs Clean(root, name, dryRun) for every project currently
// under root/src, independently — one project's clean failure does not
// prevent the others from running. Returns a map of project name to
// its removed-paths list (only for projects that succeeded) and an
// aggregated error (via errors.Join) naming every project that failed,
// or a nil error if all succeeded. An empty (or not-yet-existing) src/
// yields an empty map and a nil error — not an error condition.
func CleanAll(root string, dryRun bool) (map[string][]string, error) {
	srcRoot := filepath.Join(root, srcDir)
	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string][]string{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", srcRoot, err)
	}

	results := map[string][]string{}
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		removed, err := Clean(root, name, dryRun)
		if err != nil {
			errs = append(errs, fmt.Errorf("cleaning %s: %w", name, err))
			continue
		}
		results[name] = removed
	}
	return results, errors.Join(errs...)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/workspace/... -run TestCleanAll -v`
Expected: PASS for all tests.

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 6: Commit**

```bash
git add internal/workspace/clean.go internal/workspace/clean_test.go
git commit -m "Add workspace.CleanAll"
```

---

## Task 3: `internal/workspace/archive.go` — `Archive`

**Files:**
- Create: `internal/workspace/archive.go`
- Create: `internal/workspace/archive_test.go`

**Interfaces:**
- Consumes: `Clean(root, name string, dryRun bool) ([]string, error)` (Task 1), `ValidProjectName(name string) error`, `filesystem.Exists(path string) bool`, the existing unexported `srcDir`/`archiveDir` constants.
- Produces: `workspace.Archive(root, name string, dryRun bool) ([]string, error)` — consumed by Task 5 (`cmd/workspace.go`'s archive command).

- [ ] **Step 1: Write the failing tests**

Create `internal/workspace/archive_test.go`:

```go
package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArchive_CleansAndMovesToArchive(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	proj := filepath.Join(root, srcDir, "api")
	writeFile(t, filepath.Join(proj, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(proj, "debug.log"), "log")
	writeFile(t, filepath.Join(proj, "app.go"), "package main")

	removed, err := Archive(root, "api", false)
	if err != nil {
		t.Fatalf("Archive() returned error: %v", err)
	}
	if len(removed) != 1 || removed[0] != "debug.log" {
		t.Errorf("Archive() removed = %v, want [\"debug.log\"]", removed)
	}

	if _, err := os.Stat(proj); !os.IsNotExist(err) {
		t.Error("src/api still exists after Archive()")
	}
	archived := filepath.Join(root, archiveDir, "api")
	if _, err := os.Stat(filepath.Join(archived, "app.go")); err != nil {
		t.Errorf("archive/api/app.go missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archived, "debug.log")); !os.IsNotExist(err) {
		t.Error("archive/api/debug.log exists, want it cleaned before the move")
	}
}

func TestArchive_NeverMovesIfCleanFails(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	proj := filepath.Join(root, srcDir, "api")
	writeFile(t, filepath.Join(proj, "app.go"), "package main")
	// Directory-named-.gitignore trick (see clean_test.go) forces Clean
	// to fail deterministically.
	if err := os.MkdirAll(filepath.Join(proj, ".gitignore"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := Archive(root, "api", false)
	if err == nil {
		t.Fatal("Archive() returned nil error despite Clean failing")
	}
	if _, err := os.Stat(proj); err != nil {
		t.Errorf("src/api missing after a failed Archive(): %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, archiveDir, "api")); !os.IsNotExist(err) {
		t.Error("archive/api exists despite Clean having failed")
	}
}

func TestArchive_ExistingArchiveTargetErrorsWithoutCleaning(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	proj := filepath.Join(root, srcDir, "api")
	writeFile(t, filepath.Join(proj, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(proj, "debug.log"), "log")
	writeFile(t, filepath.Join(root, archiveDir, "api", "marker.txt"), "already archived")

	_, err := Archive(root, "api", false)
	if err == nil {
		t.Fatal("Archive() returned nil error when archive/api already exists")
	}

	if _, err := os.Stat(filepath.Join(proj, "debug.log")); err != nil {
		t.Errorf("src/api/debug.log missing — Clean must not run before the existence check: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, archiveDir, "api", "marker.txt"))
	if err != nil {
		t.Fatalf("archive/api/marker.txt missing: %v", err)
	}
	if string(got) != "already archived" {
		t.Errorf("archive/api/marker.txt content = %q, want it untouched", got)
	}
}

func TestArchive_DryRunMovesNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	proj := filepath.Join(root, srcDir, "api")
	writeFile(t, filepath.Join(proj, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(proj, "debug.log"), "log")
	writeFile(t, filepath.Join(proj, "app.go"), "package main")

	removed, err := Archive(root, "api", true)
	if err != nil {
		t.Fatalf("Archive() returned error: %v", err)
	}
	if len(removed) != 1 || removed[0] != "debug.log" {
		t.Errorf("Archive(dryRun=true) removed = %v, want [\"debug.log\"]", removed)
	}
	if _, err := os.Stat(proj); err != nil {
		t.Errorf("src/api missing after a dry-run Archive(): %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "debug.log")); err != nil {
		t.Errorf("src/api/debug.log removed during a dry run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, archiveDir, "api")); !os.IsNotExist(err) {
		t.Error("archive/api exists after a dry-run Archive()")
	}
}

func TestArchive_NonexistentProjectReturnsError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := Archive(root, "does-not-exist", false)
	if err == nil {
		t.Fatal("Archive() returned nil error for a nonexistent project")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/workspace/... -run TestArchive -v`
Expected: FAIL — `undefined: Archive`.

- [ ] **Step 3: Write the implementation**

Create `internal/workspace/archive.go`:

```go
// archive.go moves a cleaned project from src/ into archive/.
package workspace

import (
	"fmt"
	"path/filepath"

	"github.com/jpsdm/dev/internal/filesystem"
)

// Archive moves root/src/<name> to root/archive/<name>. It first
// errors, touching nothing, if root/archive/<name> already exists —
// before any cleaning happens, so a project that can't be archived is
// never destructively cleaned first. It then runs Clean(root, name,
// dryRun) for real; if that fails, the move never happens and the
// error is returned as-is (Clean's own partial-removal list is still
// returned alongside it, so a caller can report what did happen). If
// dryRun is true, Archive stops after Clean reports what it would
// remove and never moves anything. If dryRun is false and the clean
// succeeds, the project directory is moved into place with a single
// os.Rename — src/<name> and archive/<name> are siblings under the
// same workspace root, so no temp-copy dance like New/Scratch's
// cross-content template copy is needed.
func Archive(root, name string, dryRun bool) ([]string, error) {
	if err := ValidProjectName(name); err != nil {
		return nil, err
	}
	archived := filepath.Join(root, archiveDir, name)
	if filesystem.Exists(archived) {
		return nil, fmt.Errorf("project %q is already archived at %s", name, archived)
	}

	removed, err := Clean(root, name, dryRun)
	if err != nil {
		return removed, err
	}
	if dryRun {
		return removed, nil
	}

	src := filepath.Join(root, srcDir, name)
	if err := os.Rename(src, archived); err != nil {
		return removed, fmt.Errorf("moving %s to %s: %w", src, archived, err)
	}
	return removed, nil
}
```

Add `"os"` to this file's imports (used by `os.Rename`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/workspace/... -run TestArchive -v`
Expected: PASS for all tests.

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 6: Commit**

```bash
git add internal/workspace/archive.go internal/workspace/archive_test.go
git commit -m "Add workspace.Archive (clean, then move to archive/)"
```

---

## Task 4: `cmd/workspace.go` — `clean` command (single project and `--all`)

**Files:**
- Modify: `cmd/workspace.go`
- Modify: `cmd/workspace_test.go`

**Interfaces:**
- Consumes: `resolveWorkspaceRoot(cmd *cobra.Command) (string, error)` (sub-project 3a), `workspace.Clean(root, name string, dryRun bool) ([]string, error)` (Task 1), `workspace.CleanAll(root string, dryRun bool) (map[string][]string, error)` (Task 2), `shell.Confirm(prompt string, in io.Reader, out io.Writer) (bool, error)` (sub-project 2b), `cliutil.Fsuccess`/`cliutil.Fstep` (sub-project 2b).
- Produces: `workspaceCleanCmd` (Cobra command, registered on `workspaceCmd`) — no other task depends on this file's clean-specific additions.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/workspace_test.go` (add `"github.com/jpsdm/dev/internal/workspace"`'s already-present import stays; this task doesn't need new imports beyond what Task 5 of 3a already left in place — `bytes`, `os`, `path/filepath`, `strings`, `testing`, `config`, `workspace`):

```go
func TestWorkspaceCleanCommand_RemovesIgnoredFiles(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "clean", "api"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace clean api` returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "debug.log")); !os.IsNotExist(err) {
		t.Error("debug.log still exists after `dev workspace clean api`")
	}
	if !strings.Contains(out.String(), "Cleaned") {
		t.Errorf("output = %q, want it to confirm the project was cleaned", out.String())
	}
}

func TestWorkspaceCleanCommand_DryRunMatchesSpecFormat(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "clean", "api", "--dry-run"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace clean api --dry-run` returned error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "The following files would be removed:") {
		t.Errorf("output = %q, want the spec's exact opening line", got)
	}
	if !strings.Contains(got, "debug.log") {
		t.Errorf("output = %q, want it to list debug.log", got)
	}
	if !strings.Contains(got, "No files were deleted.") {
		t.Errorf("output = %q, want the spec's exact closing line", got)
	}
	if _, err := os.Stat(filepath.Join(proj, "debug.log")); err != nil {
		t.Errorf("debug.log removed during a dry run: %v", err)
	}
}

func TestWorkspaceCleanCommand_NeitherNameNorAllErrors(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "clean"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("`dev workspace clean` with neither a name nor --all returned nil error")
	}
}

func TestWorkspaceCleanCommand_NameAndAllTogetherErrors(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "clean", "api", "--all"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("`dev workspace clean api --all` (both name and --all) returned nil error")
	}
}

func TestWorkspaceCleanCommand_AllDeclinedConfirmationMakesNoChanges(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("n\n"))
	rootCmd.SetArgs([]string{"workspace", "clean", "--all"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace clean --all` (declined) returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "debug.log")); err != nil {
		t.Errorf("debug.log removed despite declining confirmation: %v", err)
	}
	if !strings.Contains(out.String(), "No changes made.") {
		t.Errorf("output = %q, want it to confirm no changes were made", out.String())
	}
}

func TestWorkspaceCleanCommand_AllConfirmedCleansEveryProject(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"workspace", "clean", "--all"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace clean --all` (confirmed) returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "debug.log")); !os.IsNotExist(err) {
		t.Error("debug.log still exists after confirmed `dev workspace clean --all`")
	}
}

func TestWorkspaceCleanCommand_AllDryRunSkipsConfirmationPrompt(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// No stdin reader set at all: if the command tried to read a
	// confirmation, Confirm's bufio.Scanner would hit EOF and treat it
	// as "no" rather than hanging — but a "no" would also mean nothing
	// gets cleaned, so this test's real assertion (debug.log IS removed)
	// only passes if no confirmation was requested at all.
	rootCmd.SetArgs([]string{"workspace", "clean", "--all", "--dry-run"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace clean --all --dry-run` returned error: %v", err)
	}
	if !strings.Contains(out.String(), "debug.log") {
		t.Errorf("output = %q, want it to preview debug.log without a confirmation prompt", out.String())
	}
	if _, err := os.Stat(filepath.Join(proj, "debug.log")); err != nil {
		t.Errorf("debug.log removed during a dry run: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/... -run TestWorkspaceCleanCommand -v`
Expected: FAIL — `undefined: workspaceCleanCmd` (build error, since the command isn't registered yet).

- [ ] **Step 3: Write the implementation**

Add to `cmd/workspace.go` (add `"sort"` and `"github.com/jpsdm/dev/internal/shell"` to the imports):

```go
func printCleanPreview(out io.Writer, removed []string) {
	fmt.Fprintln(out, "The following files would be removed:")
	fmt.Fprintln(out)
	for _, r := range removed {
		fmt.Fprintln(out, r)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "No files were deleted.")
}

var workspaceCleanCmd = &cobra.Command{
	Use:     "clean [name]",
	Aliases: []string{"c"},
	Args:    cobra.MaximumNArgs(1),
	Short:   "Remove a project's gitignored files, or every project's with --all",
	RunE: func(cmd *cobra.Command, args []string) error {
		all, err := cmd.Flags().GetBool("all")
		if err != nil {
			return err
		}
		dryRun, err := cmd.Flags().GetBool("dry-run")
		if err != nil {
			return err
		}
		if all == (len(args) == 1) {
			return fmt.Errorf("specify either a project name or --all, not both or neither")
		}

		root, err := resolveWorkspaceRoot(cmd)
		if err != nil {
			return err
		}

		if !all {
			removed, err := workspace.Clean(root, args[0], dryRun)
			if err != nil {
				return err
			}
			if dryRun {
				printCleanPreview(cmd.OutOrStdout(), removed)
				return nil
			}
			if len(removed) == 0 {
				cliutil.Fstep(cmd.OutOrStdout(), "Nothing to clean")
			} else {
				for _, r := range removed {
					fmt.Fprintln(cmd.OutOrStdout(), r)
				}
			}
			cliutil.Fsuccess(cmd.OutOrStdout(), "Cleaned src/%s", args[0])
			return nil
		}

		if !dryRun {
			confirmed, err := shell.Confirm("Remove ignored files from every project in src/? [y/N] ", cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if !confirmed {
				cliutil.Fstep(cmd.OutOrStdout(), "No changes made.")
				return nil
			}
		}

		results, cleanErr := workspace.CleanAll(root, dryRun)
		names := make([]string, 0, len(results))
		for name := range results {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			removed := results[name]
			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "%s:\n", name)
				printCleanPreview(cmd.OutOrStdout(), removed)
				continue
			}
			if len(removed) == 0 {
				cliutil.Fstep(cmd.OutOrStdout(), "%s: nothing to clean", name)
				continue
			}
			cliutil.Fsuccess(cmd.OutOrStdout(), "Cleaned src/%s", name)
		}
		return cleanErr
	},
}

func init() {
	workspaceCleanCmd.Flags().Bool("all", false, "clean every project in src/")
	workspaceCleanCmd.Flags().Bool("dry-run", false, "show what would be removed without removing anything")
	workspaceCmd.AddCommand(workspaceCleanCmd)
}
```

This is a second `init()` function in the file — Go allows multiple `init()` functions per file/package, all of which run; this keeps each task's registration next to the command it registers, matching how 3a's Task 4 and Task 5 each added their own `workspaceCmd.AddCommand(...)` call.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/... -run TestWorkspaceCleanCommand -v`
Expected: PASS for all tests.

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 6: Commit**

```bash
git add cmd/workspace.go cmd/workspace_test.go
git commit -m "Add dev workspace clean command (single project and --all)"
```

---

## Task 5: `cmd/workspace.go` — `archive` command

**Files:**
- Modify: `cmd/workspace.go`
- Modify: `cmd/workspace_test.go`

**Interfaces:**
- Consumes: `resolveWorkspaceRoot(cmd *cobra.Command) (string, error)` (sub-project 3a), `workspace.Archive(root, name string, dryRun bool) ([]string, error)` (Task 3), `printCleanPreview(out io.Writer, removed []string)` (Task 4).
- Produces: `workspaceArchiveCmd` (Cobra command) — no other task depends on this.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/workspace_test.go`:

```go
func TestWorkspaceArchiveCommand_MovesToArchive(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "app.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "archive", "api"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace archive api` returned error: %v", err)
	}
	if _, err := os.Stat(proj); !os.IsNotExist(err) {
		t.Error("src/api still exists after archive")
	}
	if _, err := os.Stat(filepath.Join(root, "archive", "api", "app.go")); err != nil {
		t.Errorf("archive/api/app.go missing: %v", err)
	}
	if !strings.Contains(out.String(), "Archived") {
		t.Errorf("output = %q, want it to confirm the archive", out.String())
	}
}

func TestWorkspaceArchiveCommand_DryRunDoesNotMove(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "app.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "archive", "api", "--dry-run"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace archive api --dry-run` returned error: %v", err)
	}
	if _, err := os.Stat(proj); err != nil {
		t.Errorf("src/api missing after a dry-run archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "archive", "api")); !os.IsNotExist(err) {
		t.Error("archive/api exists after a dry-run archive")
	}
}

func TestWorkspaceArchiveCommand_ExistingTargetErrors(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "archive", "api"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "archive", "api"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("`dev workspace archive api` returned nil error when archive/api already exists")
	}
}

func TestWorkspaceArchiveCommand_WsAAliasChain(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"ws", "a", "api"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev ws a api` returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "archive", "api")); err != nil {
		t.Errorf("expected archive/api to exist: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/... -run TestWorkspaceArchiveCommand -v`
Expected: FAIL — `undefined: workspaceArchiveCmd`.

- [ ] **Step 3: Write the implementation**

Add to `cmd/workspace.go`:

```go
var workspaceArchiveCmd = &cobra.Command{
	Use:     "archive <name>",
	Aliases: []string{"a"},
	Args:    cobra.ExactArgs(1),
	Short:   "Clean a project and move it to archive/",
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, err := cmd.Flags().GetBool("dry-run")
		if err != nil {
			return err
		}
		root, err := resolveWorkspaceRoot(cmd)
		if err != nil {
			return err
		}
		removed, err := workspace.Archive(root, args[0], dryRun)
		if err != nil {
			return err
		}
		if dryRun {
			printCleanPreview(cmd.OutOrStdout(), removed)
			return nil
		}
		for _, r := range removed {
			fmt.Fprintln(cmd.OutOrStdout(), r)
		}
		cliutil.Fsuccess(cmd.OutOrStdout(), "Archived src/%s to archive/%s", args[0], args[0])
		return nil
	},
}

func init() {
	workspaceArchiveCmd.Flags().Bool("dry-run", false, "show what would be removed without cleaning or moving anything")
	workspaceCmd.AddCommand(workspaceArchiveCmd)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/... -run TestWorkspaceArchiveCommand -v`
Expected: PASS for all tests.

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 6: Commit**

```bash
git add cmd/workspace.go cmd/workspace_test.go
git commit -m "Add dev workspace archive command"
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

Proceed using whatever `DEV_HOME` actually resolves to in this environment; do not silently override it.

- [ ] **Step 2: Run the full check chain**

```bash
make check
```

Expected: `fmt`, `vet`, `lint`, `test`, `build` all succeed.

- [ ] **Step 3: Set up an isolated workspace with a real, non-trivial project**

```bash
export TEST_DEV_HOME=$(mktemp -d)
export TEST_WS=$(mktemp -d)
mkdir -p "$TEST_DEV_HOME/config"
cat > "$TEST_DEV_HOME/config/config.json" <<EOF
{"workspace":{"path":"$TEST_WS"}}
EOF
DEV_HOME=$TEST_DEV_HOME ./dev workspace
mkdir -p "$TEST_WS/src/api/node_modules/pkg"
echo 'node_modules/' > "$TEST_WS/src/api/.gitignore"
echo '*.log' >> "$TEST_WS/src/api/.gitignore"
echo 'package main' > "$TEST_WS/src/api/app.go"
echo 'debug output' > "$TEST_WS/src/api/debug.log"
echo 'module' > "$TEST_WS/src/api/node_modules/pkg/index.js"
```

This mirrors 3a's Task 6 approach: pre-populating config avoids needing a live interactive Huh prompt (this environment likely has no TTY, same as 3a's acceptance run) — everything from this point on exercises this sub-project's own new commands against the real compiled binary.

- [ ] **Step 4: Verify `clean --dry-run` matches the spec's format and removes nothing**

```bash
DEV_HOME=$TEST_DEV_HOME ./dev workspace clean api --dry-run
ls "$TEST_WS/src/api"
```

Expected: output starts with "The following files would be removed:", lists `debug.log` and `node_modules/`, ends with "No files were deleted." `ls` afterward shows `node_modules` and `debug.log` are both still present.

- [ ] **Step 5: Verify `clean` for real, via the `ws`/`c` alias chain**

```bash
DEV_HOME=$TEST_DEV_HOME ./dev ws c api
ls "$TEST_WS/src/api"
```

Expected: `app.go` and `.gitignore` remain; `node_modules` and `debug.log` are gone. (Uses the `ws c` alias chain here, and the bare `dev workspace clean` form in Step 4 — both routes are exercised across this walkthrough, on top of the alias-resolution unit tests Tasks 4/5 already added.)

- [ ] **Step 6: Verify `archive --dry-run`, then `archive` for real**

```bash
mkdir -p "$TEST_WS/src/svc/node_modules/pkg"
echo 'node_modules/' > "$TEST_WS/src/svc/.gitignore"
echo 'main.go content' > "$TEST_WS/src/svc/main.go"
echo 'module' > "$TEST_WS/src/svc/node_modules/pkg/index.js"

DEV_HOME=$TEST_DEV_HOME ./dev workspace archive svc --dry-run
ls "$TEST_WS/src/svc"
test -d "$TEST_WS/archive/svc" && echo "UNEXPECTED: archive/svc exists after dry-run" || echo "OK: archive/svc does not exist yet"

DEV_HOME=$TEST_DEV_HOME ./dev ws a svc
ls "$TEST_WS/archive/svc"
test -d "$TEST_WS/src/svc" && echo "UNEXPECTED: src/svc still exists" || echo "OK: src/svc gone"
```

Expected: the dry-run previews `node_modules/` in the spec's format and moves/removes nothing (`src/svc/node_modules` still present, `archive/svc` doesn't exist yet); the real run (via the `ws a` alias) leaves `archive/svc/main.go` present, `archive/svc/node_modules` absent (cleaned before the move), and `src/svc` gone entirely.

- [ ] **Step 7: Verify `archive` on an already-archived name errors**

```bash
mkdir -p "$TEST_WS/src/api2"
echo 'x' > "$TEST_WS/src/api2/keep.txt"
mkdir -p "$TEST_WS/archive/api2"
DEV_HOME=$TEST_DEV_HOME ./dev workspace archive api2
echo "exit code: $?"
ls "$TEST_WS/src/api2"
```

Expected: a `✗`-prefixed error mentioning `api2` is already archived, non-zero exit code, `src/api2/keep.txt` still present (never cleaned since the existence check runs first).

- [ ] **Step 8: Verify `clean --all --dry-run`, then a real confirmation prompt declined and confirmed**

```bash
mkdir -p "$TEST_WS/src/web"
echo 'dist/' > "$TEST_WS/src/web/.gitignore"
mkdir -p "$TEST_WS/src/web/dist"
echo 'bundled' > "$TEST_WS/src/web/dist/bundle.js"
echo '<html></html>' > "$TEST_WS/src/web/index.html"

DEV_HOME=$TEST_DEV_HOME ./dev ws c --all --dry-run
ls "$TEST_WS/src/web"

printf 'n\n' | DEV_HOME=$TEST_DEV_HOME ./dev workspace clean --all
ls "$TEST_WS/src/web"

printf 'y\n' | DEV_HOME=$TEST_DEV_HOME ./dev workspace clean --all
ls "$TEST_WS/src/web"
```

Expected: `ws c --all --dry-run` (the alias form) previews `dist/` and prompts for nothing (no stdin was even piped in, and the command must not hang waiting for one) — `dist` still exists afterward; after the declined run, `dist` still exists under `src/web` and the printed output says "No changes made."; after the confirmed run, `dist` is gone and `index.html` remains. (`shell.Confirm` reads from stdin via a plain `bufio.Scanner`, not a TTY-requiring library like Huh, so this works the same way `dev setup`'s confirmation already does under piped input.)

- [ ] **Step 9: Verify usage errors**

```bash
DEV_HOME=$TEST_DEV_HOME ./dev workspace clean
echo "exit code: $?"
DEV_HOME=$TEST_DEV_HOME ./dev workspace clean somename --all
echo "exit code: $?"
```

Expected: both non-zero exit, both print a clear error about specifying exactly one of a name or `--all`.

- [ ] **Step 10: Update the README**

Add to `README.md`'s existing "Workspace" section (from 3a), after the current three example commands and before the "Aliases" line:

```markdown
    dev workspace clean api        # remove src/api's gitignored files
    dev workspace clean api --dry-run
    dev workspace clean --all      # clean every project in src/ (asks for confirmation)
    dev workspace archive api      # clean src/api, then move it to archive/api
```

Update the existing `Aliases:` line to also mention the new ones:

```markdown
Aliases: `ws` for `workspace`, `ws s` for `scratch`, `ws c` for `clean`, `ws a` for `archive`.
```

And update the closing sentence, which currently reads `` `clean`/`archive`/`metrics` are not implemented yet. `` — change it to:

```markdown
`clean` uses each project's real `.gitignore` (including nested files) to
decide what gets removed — the same set `git clean -Xdf` would remove — and
never requires the project to be a git repository. `metrics` is not
implemented yet.
```

- [ ] **Step 11: Commit**

```bash
git add README.md
git commit -m "Document dev workspace clean and archive in the README"
```

- [ ] **Step 12: Final confirmation**

```bash
make check
git status --short
```

Expected: `make check` passes and `git status --short` is empty.
