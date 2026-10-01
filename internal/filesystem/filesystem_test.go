package filesystem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "present.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if !Exists(file) {
		t.Error("Exists() = false for a file that exists")
	}
	if Exists(filepath.Join(dir, "missing.txt")) {
		t.Error("Exists() = true for a file that does not exist")
	}
}

func TestEnsureDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "a", "b", "c")

	if err := EnsureDir(target, 0o755); err != nil {
		t.Fatalf("EnsureDir() returned error: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat after EnsureDir: %v", err)
	}
	if !info.IsDir() {
		t.Error("EnsureDir() did not create a directory")
	}

	if err := EnsureDir(target, 0o755); err != nil {
		t.Fatalf("EnsureDir() on existing dir returned error: %v", err)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "config.json")

	if err := WriteFileAtomic(target, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic() returned error: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Errorf("file content = %q, want %q", got, `{"a":1}`)
	}

	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want 1 (no leftover temp files)", len(entries))
	}
}
