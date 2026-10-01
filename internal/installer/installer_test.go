package installer

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildTarGz(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("writing tar content: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "archive.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing archive file: %v", err)
	}
	return path
}

func buildZip(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("creating zip entry: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("writing zip content: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip writer: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "archive.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing archive file: %v", err)
	}
	return path
}

func TestExtractAtomic_TarGz(t *testing.T) {
	t.Parallel()
	archive := buildTarGz(t, map[string]string{
		"node-v22.11.0-linux-x64/bin/node":  "fake binary",
		"node-v22.11.0-linux-x64/README.md": "readme",
	})
	destDir := filepath.Join(t.TempDir(), "dest")

	if err := ExtractAtomic(archive, destDir); err != nil {
		t.Fatalf("ExtractAtomic() returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "node-v22.11.0-linux-x64", "bin", "node"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != "fake binary" {
		t.Errorf("extracted content = %q, want %q", got, "fake binary")
	}
}

func TestExtractAtomic_Zip(t *testing.T) {
	t.Parallel()
	archive := buildZip(t, map[string]string{
		"node-v22.11.0-win-x64/node.exe": "fake exe",
	})
	destDir := filepath.Join(t.TempDir(), "dest")

	if err := ExtractAtomic(archive, destDir); err != nil {
		t.Fatalf("ExtractAtomic() returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "node-v22.11.0-win-x64", "node.exe"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != "fake exe" {
		t.Errorf("extracted content = %q, want %q", got, "fake exe")
	}
}

func TestExtractAtomic_ReplacesExistingDestDir(t *testing.T) {
	t.Parallel()
	destDir := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	archive := buildTarGz(t, map[string]string{"new.txt": "new"})
	if err := ExtractAtomic(archive, destDir); err != nil {
		t.Fatalf("ExtractAtomic() returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, "old.txt")); !os.IsNotExist(err) {
		t.Error("old.txt still present after ExtractAtomic replaced destDir")
	}
	got, err := os.ReadFile(filepath.Join(destDir, "new.txt"))
	if err != nil {
		t.Fatalf("reading new.txt: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("new.txt content = %q, want %q", got, "new")
	}
}

func TestExtractAtomic_CorruptArchiveLeavesExistingDestDirUntouched(t *testing.T) {
	t.Parallel()
	destDir := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "keep.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	badArchive := filepath.Join(t.TempDir(), "corrupt.tar.gz")
	if err := os.WriteFile(badArchive, []byte("not a real gzip stream"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := ExtractAtomic(badArchive, destDir)
	if err == nil {
		t.Fatal("ExtractAtomic() returned nil error for a corrupt archive")
	}

	got, err := os.ReadFile(filepath.Join(destDir, "keep.txt"))
	if err != nil {
		t.Fatalf("keep.txt missing after failed ExtractAtomic: %v", err)
	}
	if string(got) != "keep me" {
		t.Errorf("keep.txt content = %q, want %q (destDir should be untouched)", got, "keep me")
	}
}

func TestExtractAtomic_RejectsSymlinkEscapingDestDir(t *testing.T) {
	t.Parallel()
	parentDir := t.TempDir()
	outsideMarker := filepath.Join(parentDir, "outside-target")

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "evil",
		Typeflag: tar.TypeSymlink,
		Linkname: "../../outside-target",
		Mode:     0o777,
	}); err != nil {
		t.Fatalf("writing symlink header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "evil.tar.gz")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing archive: %v", err)
	}

	destDir := filepath.Join(parentDir, "dest")
	err := ExtractAtomic(archivePath, destDir)
	if err == nil {
		t.Fatal("ExtractAtomic() returned nil error for a symlink escaping destDir")
	}
	if _, statErr := os.Lstat(outsideMarker); !os.IsNotExist(statErr) {
		t.Error("a path outside destDir was created despite the symlink being rejected")
	}
}

func TestExtractAtomic_RejectsChainedSymlinkEscape(t *testing.T) {
	t.Parallel()
	// Reproduces a "tar symlink chain" attack: entry 1 makes "a" a
	// symlink to ".", entry 2 makes "a/b" a symlink to "..", and entry 3
	// writes a regular file at "a/b/pwned". None of these three entries'
	// own names contain "..", so a lexical-only check (validating each
	// entry's name/target in isolation) sees nothing wrong — but
	// physically walking the resulting path ("a" -> destDir, "a/b" ->
	// destDir/.. == parentDir) lands "pwned" one level outside destDir.
	parentDir := t.TempDir()
	grandparentDir := filepath.Dir(parentDir)

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	entries := []struct {
		name     string
		linkname string
		typeflag byte
		content  string
	}{
		{name: "a", linkname: ".", typeflag: tar.TypeSymlink},
		{name: "a/b", linkname: "..", typeflag: tar.TypeSymlink},
		{name: "a/b/pwned", typeflag: tar.TypeReg, content: "escaped"},
	}
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Linkname: e.linkname,
			Mode:     0o777,
			Size:     int64(len(e.content)),
		}
		if e.typeflag == tar.TypeReg {
			hdr.Mode = 0o644
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing tar header for %s: %v", e.name, err)
		}
		if e.content != "" {
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatalf("writing tar content for %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "chained-evil.tar.gz")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing archive: %v", err)
	}

	destDir := filepath.Join(parentDir, "dest")
	err := ExtractAtomic(archivePath, destDir)
	if err == nil {
		t.Error("ExtractAtomic() returned nil error for a chained symlink escape")
	}

	if _, statErr := os.Lstat(filepath.Join(parentDir, "pwned")); !os.IsNotExist(statErr) {
		t.Error("pwned was created one level outside destDir (in its parent) despite the chained symlink escape being rejected")
	}
	if _, statErr := os.Lstat(filepath.Join(grandparentDir, "pwned")); !os.IsNotExist(statErr) {
		t.Error("pwned was created two levels outside destDir (in its grandparent) despite the chained symlink escape being rejected")
	}
}

func buildZipWithSymlink(t *testing.T, name, linkTarget string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: name}
	hdr.SetMode(os.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatalf("creating zip symlink entry: %v", err)
	}
	if _, err := w.Write([]byte(linkTarget)); err != nil {
		t.Fatalf("writing zip symlink target: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip writer: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "archive.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing archive file: %v", err)
	}
	return path
}

func TestExtractAtomic_RejectsSymlinkEscapingDestDirInZip(t *testing.T) {
	t.Parallel()
	parentDir := t.TempDir()
	outsideMarker := filepath.Join(parentDir, "outside-target")

	archivePath := buildZipWithSymlink(t, "evil", "../../outside-target")
	destDir := filepath.Join(parentDir, "dest")

	err := ExtractAtomic(archivePath, destDir)
	if err == nil {
		t.Fatal("ExtractAtomic() returned nil error for a zip symlink escaping destDir")
	}
	if _, statErr := os.Lstat(outsideMarker); !os.IsNotExist(statErr) {
		t.Error("a path outside destDir was created despite the zip symlink being rejected")
	}
}

func TestExtractAtomic_UnsupportedArchiveFormatReturnsExplicitError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "archive.rar")
	if err := os.WriteFile(archivePath, []byte("irrelevant"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	destDir := filepath.Join(dir, "dest")

	err := ExtractAtomic(archivePath, destDir)
	if err == nil {
		t.Fatal("ExtractAtomic() returned nil error for an unsupported archive format")
	}
	if !strings.Contains(err.Error(), "unsupported archive format") {
		t.Errorf("ExtractAtomic() error = %q, want it to mention an unsupported archive format", err)
	}
}

func TestReconcileStaleBackup_RestoresDestDirWhenMissing(t *testing.T) {
	t.Parallel()
	parentDir := t.TempDir()
	destDir := filepath.Join(parentDir, "dest")
	backupDir := filepath.Join(parentDir, "dest.old")

	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := ReconcileStaleBackup(destDir, backupDir); err != nil {
		t.Fatalf("ReconcileStaleBackup() returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "old.txt"))
	if err != nil {
		t.Fatalf("destDir does not contain restored backup content: %v", err)
	}
	if string(got) != "old" {
		t.Errorf("restored content = %q, want %q", got, "old")
	}
	if _, err := os.Stat(backupDir); !os.IsNotExist(err) {
		t.Error("backupDir still present after being restored into destDir")
	}
}

func TestReconcileStaleBackup_RemovesStaleBackupWhenDestDirExists(t *testing.T) {
	t.Parallel()
	parentDir := t.TempDir()
	destDir := filepath.Join(parentDir, "dest")
	backupDir := filepath.Join(parentDir, "dest.old")

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "current.txt"), []byte("current"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := ReconcileStaleBackup(destDir, backupDir); err != nil {
		t.Fatalf("ReconcileStaleBackup() returned error: %v", err)
	}

	if _, err := os.Stat(backupDir); !os.IsNotExist(err) {
		t.Error("stale backupDir still present after destDir was confirmed current")
	}
	got, err := os.ReadFile(filepath.Join(destDir, "current.txt"))
	if err != nil {
		t.Fatalf("destDir content lost: %v", err)
	}
	if string(got) != "current" {
		t.Errorf("destDir content = %q, want %q", got, "current")
	}
}

func TestReconcileStaleBackup_NoopWhenNeitherExists(t *testing.T) {
	t.Parallel()
	parentDir := t.TempDir()
	destDir := filepath.Join(parentDir, "dest")
	backupDir := filepath.Join(parentDir, "dest.old")

	if err := ReconcileStaleBackup(destDir, backupDir); err != nil {
		t.Fatalf("ReconcileStaleBackup() returned error: %v", err)
	}
	if _, err := os.Stat(destDir); !os.IsNotExist(err) {
		t.Error("ReconcileStaleBackup() created destDir out of nothing")
	}
}

func TestExtractAtomic_RecoversFromInterruptedMidSwapDiskState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	destDir := filepath.Join(dir, "dest")

	// Reconstruct exactly the disk state a killed-mid-swap process
	// leaves behind: destDir itself is gone (already renamed aside),
	// and its ".old" backup — the only surviving copy of the
	// previous install — is still sitting there with old content.
	backupDir := destDir + ".old"
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "bin"), []byte("old version"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// A fresh install now runs against that exact disk state — this
	// exercises the real recovery path through ExtractAtomic's own
	// entry point (which calls ReconcileStaleBackup internally),
	// rather than testing ReconcileStaleBackup in isolation.
	archivePath := buildTarGz(t, map[string]string{"bin": "new version"})
	if err := ExtractAtomic(archivePath, destDir); err != nil {
		t.Fatalf("ExtractAtomic() returned error recovering from interrupted mid-swap state: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "bin"))
	if err != nil {
		t.Fatalf("reading destDir content: %v", err)
	}
	if string(got) != "new version" {
		t.Errorf("destDir content = %q, want %q (new install should have landed cleanly)", got, "new version")
	}
	if _, err := os.Stat(backupDir); !os.IsNotExist(err) {
		t.Error("backupDir still present after a successful recovery + install")
	}
}

func TestReconcileStaleBackup_StatErrorOtherThanNotExistIsReturned(t *testing.T) {
	t.Parallel()
	parentDir := t.TempDir()
	// A regular file where a parent directory component is expected
	// makes os.Stat on anything underneath it fail with "not a
	// directory" — a real error os.IsNotExist does not recognize, unlike
	// a simple missing path. destDir's status is therefore genuinely
	// unknown, not "confirmed missing."
	blocker := filepath.Join(parentDir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	destDir := filepath.Join(blocker, "dest")
	backupDir := filepath.Join(parentDir, "dest.old")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := ReconcileStaleBackup(destDir, backupDir)
	if err == nil {
		t.Fatal("ReconcileStaleBackup() returned nil error for a non-IsNotExist stat failure, want an error")
	}
	// The old behavior treated this the same as "destDir exists" and
	// discarded backupDir; confirm the fix leaves it alone instead of
	// silently destroying the only surviving copy of an interrupted
	// install.
	if _, statErr := os.Stat(backupDir); statErr != nil {
		t.Errorf("backupDir was removed despite destDir's status being unknown: %v", statErr)
	}
}

func TestExtractAtomic_TarGzSkipsHardLinksWithoutError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: "regular.txt", Mode: 0o644, Size: int64(len("content"))}); err != nil {
		t.Fatalf("writing tar header: %v", err)
	}
	if _, err := tw.Write([]byte("content")); err != nil {
		t.Fatalf("writing tar content: %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "hardlink", Typeflag: tar.TypeLink, Linkname: "regular.txt"}); err != nil {
		t.Fatalf("writing tar hardlink header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing archive: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "dest")
	if err := ExtractAtomic(archivePath, destDir); err != nil {
		t.Fatalf("ExtractAtomic() returned error for an archive containing a hard link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "hardlink")); !os.IsNotExist(err) {
		t.Error("hard link entry was extracted; want it silently skipped")
	}
}

func TestExtractAtomic_NoBackupDirLeftAfterSuccessfulReplace(t *testing.T) {
	t.Parallel()
	destDir := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	archive := buildTarGz(t, map[string]string{"new.txt": "new"})
	if err := ExtractAtomic(archive, destDir); err != nil {
		t.Fatalf("ExtractAtomic() returned error: %v", err)
	}

	if _, err := os.Stat(destDir + ".old"); !os.IsNotExist(err) {
		t.Error(`backup directory destDir+".old" was not cleaned up after a successful replace`)
	}
}
