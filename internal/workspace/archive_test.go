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
