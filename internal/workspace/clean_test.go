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

// TestClean_NestedGitignoreInAlphabeticallyEarlierDirNegationWins
// exercises the ordering bug directly: collectGitignorePatterns used to
// append patterns in filepath.WalkDir's lexical order, which visits a
// subdirectory named ".alpha" (sorts before the literal string
// ".gitignore") before it reaches the current directory's own
// .gitignore file. That put the root's *.log pattern AFTER (and so
// wrongly outranking) .alpha's own !keep.log negation. A directory
// named "logs" (as in TestClean_NestedGitignoreNegationReincludesFile)
// sorts AFTER ".gitignore" and so never exercised this path.
func TestClean_NestedGitignoreInAlphabeticallyEarlierDirNegationWins(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	proj := filepath.Join(root, srcDir, "api")
	writeFile(t, filepath.Join(proj, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(proj, ".alpha", ".gitignore"), "!keep.log\n")
	writeFile(t, filepath.Join(proj, ".alpha", "keep.log"), "keep me")
	writeFile(t, filepath.Join(proj, ".alpha", "other.log"), "remove me")

	removed, err := Clean(root, "api", false)
	if err != nil {
		t.Fatalf("Clean() returned error: %v", err)
	}

	removedSet := map[string]bool{}
	for _, r := range removed {
		removedSet[r] = true
	}
	// Clean's returned paths are always forward-slash-normalized (see
	// the filepath.ToSlash calls in clean.go), regardless of the host
	// OS's separator — so the lookup key here is a literal forward
	// slash, not filepath.Join (which would use "\" on Windows and
	// never match).
	if removedSet[".alpha/keep.log"] {
		t.Error(".alpha/keep.log was removed, want it kept (re-included by nested negation)")
	}
	if !removedSet[".alpha/other.log"] {
		t.Error(".alpha/other.log was not removed, want it removed")
	}
	if _, err := os.Stat(filepath.Join(proj, ".alpha", "keep.log")); err != nil {
		t.Errorf(".alpha/keep.log missing from disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".alpha", "other.log")); !os.IsNotExist(err) {
		t.Error(".alpha/other.log still exists, want it removed")
	}
}

func TestClean_RejectsInvalidProjectNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// A canary file under an existing, legitimately-named project: if
	// any of the bad names below actually got resolved and cleaned
	// (rather than rejected up front), this file would be removed.
	writeFile(t, filepath.Join(root, srcDir, "api", ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, srcDir, "api", "debug.log"), "log content")

	for _, name := range []string{".", "..", "a/b"} {
		removed, err := Clean(root, name, false)
		if err == nil {
			t.Errorf("Clean(root, %q, false) returned nil error, want an error", name)
		}
		if len(removed) != 0 {
			t.Errorf("Clean(root, %q, false) removed = %v, want empty", name, removed)
		}
	}

	if _, err := os.Stat(filepath.Join(root, srcDir, "api", "debug.log")); err != nil {
		t.Errorf("src/api/debug.log missing after Clean() with invalid names: %v", err)
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

func TestCleanAll_SkipsLeftoverTempDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	writeFile(t, filepath.Join(root, srcDir, "api", ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, srcDir, "api", "debug.log"), "log")
	// A leftover ".tmp-<name>-*" directory, exactly like one an
	// interrupted `dev workspace new` can leave behind. It IS a
	// directory (unlike a symlink), so it must be filtered by name, not
	// caught by the existing !entry.IsDir() skip.
	writeFile(t, filepath.Join(root, srcDir, ".tmp-orphan-abc123", "partial.go"), "package main")

	results, err := CleanAll(root, false)
	if err != nil {
		t.Fatalf("CleanAll() returned error: %v", err)
	}
	if _, ok := results[".tmp-orphan-abc123"]; ok {
		t.Errorf("CleanAll() treated the leftover temp directory as a project: %v", results)
	}
	if _, statErr := os.Stat(filepath.Join(root, srcDir, ".tmp-orphan-abc123", "partial.go")); statErr != nil {
		t.Errorf("leftover temp directory's content was disturbed: %v", statErr)
	}
	if len(results["api"]) != 1 || results["api"][0] != "debug.log" {
		t.Errorf(`results["api"] = %v, want ["debug.log"] (the real project must still be cleaned)`, results["api"])
	}
}

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
