package workspace

import (
	"os"
	"path/filepath"
	"runtime"
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
		".editorconfig":         "root = true",
		"README.md":             "hello",
		".vscode/settings.json": "{}",
	})

	if _, err := New(root, "api"); err != nil {
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

func TestNew_ReturnsTheCreatedPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := New(root, "api")
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	want := filepath.Join(root, srcDir, "api")
	if got != want {
		t.Errorf("New() returned path %q, want %q", got, want)
	}
}

func TestScratch_ReturnsTheCreatedPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := Scratch(root, "experiment")
	if err != nil {
		t.Fatalf("Scratch() returned error: %v", err)
	}
	want := filepath.Join(root, scratchDir, "experiment")
	if got != want {
		t.Errorf("Scratch() returned path %q, want %q", got, want)
	}
}

func TestNew_EmptyBaseProducesEmptyProject(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := New(root, "api"); err != nil {
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

	_, err := New(root, "api")
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

	if _, err := New(root, "../escape"); err == nil {
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

	if _, err := Scratch(root, "experiment"); err != nil {
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

	if _, err := New(root, "api"); err != nil {
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

	_, err := New(root, "api")
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

func TestNew_PreservesFilePermissionsFromBase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions don't apply on windows")
	}
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	base := filepath.Join(root, baseDir)
	scriptPath := filepath.Join(base, "run.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := New(root, "api"); err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	info, err := os.Stat(filepath.Join(root, srcDir, "api", "run.sh"))
	if err != nil {
		t.Fatalf("reading copied file info: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("copied run.sh mode = %o, want %o (base/'s executable permission must survive the copy)", info.Mode().Perm(), 0o755)
	}
}

func TestNew_ProjectDirectoryModeMatchesBase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions don't apply on windows")
	}
	t.Parallel()
	root := t.TempDir()
	if err := EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	base := filepath.Join(root, baseDir)
	// EnsureScaffold already created base/ at 0755; make the mismatch
	// with os.MkdirTemp's fixed 0700 default explicit and deliberate,
	// so this test is really asserting the chmod-to-base's-mode step,
	// not accidentally matching MkdirTemp's own default.
	if err := os.Chmod(base, 0o750); err != nil {
		t.Fatalf("setup: %v", err)
	}

	dest, err := New(root, "api")
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("reading created project directory info: %v", err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Errorf("created project directory mode = %o, want %o (base/'s own mode)", info.Mode().Perm(), 0o750)
	}
}

func TestNew_ProjectDirectoryFallsBackTo0755WhenBaseIsMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions don't apply on windows")
	}
	t.Parallel()
	root := t.TempDir()
	// Deliberately skip EnsureScaffold's base/ creation — src/ still
	// needs to exist for New's own EnsureDir(parent) call to have
	// somewhere to land, but base/ itself is absent, exercising
	// copyTree's already-documented "missing src/ is not an error" path
	// together with the mode-fallback path.
	if err := os.MkdirAll(filepath.Join(root, srcDir), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	dest, err := New(root, "api")
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("reading created project directory info: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("created project directory mode = %o, want %o (fallback default with no base/ to match)", info.Mode().Perm(), 0o755)
	}
}
