package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func buildTestTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("writing tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("writing tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	return buf.Bytes()
}

func buildTestZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, err := zw.Create(name)
	if err != nil {
		t.Fatalf("creating zip entry: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("writing zip content: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip writer: %v", err)
	}
	return buf.Bytes()
}

func TestArchiveName_WindowsGetsZip(t *testing.T) {
	t.Parallel()
	if got := archiveName("windows", "amd64"); got != "dev_windows_amd64.zip" {
		t.Errorf("archiveName(windows, amd64) = %q, want %q", got, "dev_windows_amd64.zip")
	}
}

func TestArchiveName_OtherPlatformsGetTarGz(t *testing.T) {
	t.Parallel()
	if got := archiveName("linux", "arm64"); got != "dev_linux_arm64.tar.gz" {
		t.Errorf("archiveName(linux, arm64) = %q, want %q", got, "dev_linux_arm64.tar.gz")
	}
}

func TestBinaryName_WindowsGetsExeSuffix(t *testing.T) {
	t.Parallel()
	if got := BinaryName("windows"); got != "dev.exe" {
		t.Errorf("BinaryName(windows) = %q, want %q", got, "dev.exe")
	}
}

func TestBinaryName_OtherPlatformsGetPlainName(t *testing.T) {
	t.Parallel()
	if got := BinaryName("darwin"); got != "dev" {
		t.Errorf("BinaryName(darwin) = %q, want %q", got, "dev")
	}
}

func TestFindAsset_ReturnsMatchingAsset(t *testing.T) {
	t.Parallel()
	assets := []ReleaseAsset{
		{Name: "dev_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.com/a"},
		{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/b"},
	}
	got, err := findAsset(assets, "checksums.txt")
	if err != nil {
		t.Fatalf("findAsset() returned error: %v", err)
	}
	if got.BrowserDownloadURL != "https://example.com/b" {
		t.Errorf("findAsset() = %+v, want the checksums.txt entry", got)
	}
}

func TestFindAsset_MissingAssetListsWhatWasThere(t *testing.T) {
	t.Parallel()
	assets := []ReleaseAsset{{Name: "dev_linux_amd64.tar.gz"}}
	_, err := findAsset(assets, "dev_windows_amd64.zip")
	if err == nil {
		t.Fatal("findAsset() returned nil error for a missing asset")
	}
	if !strings.Contains(err.Error(), "dev_linux_amd64.tar.gz") {
		t.Errorf("error = %q, want it to list the assets that were actually present", err.Error())
	}
}

func TestChecksumFor_ExtractsMatchingLine(t *testing.T) {
	t.Parallel()
	body := "abc123  dev_linux_amd64.tar.gz\ndef456  dev_windows_amd64.zip\n"
	got, err := checksumFor(body, "dev_windows_amd64.zip")
	if err != nil {
		t.Fatalf("checksumFor() returned error: %v", err)
	}
	if got != "def456" {
		t.Errorf("checksumFor() = %q, want %q", got, "def456")
	}
}

func TestChecksumFor_NoMatchingLineReturnsError(t *testing.T) {
	t.Parallel()
	_, err := checksumFor("abc123  dev_linux_amd64.tar.gz\n", "dev_windows_amd64.zip")
	if err == nil {
		t.Fatal("checksumFor() returned nil error for a missing entry")
	}
}

// TestApplyUpdate_DownloadsVerifiesAndSwapsTheBinary is a real,
// end-to-end test: an httptest.Server serves a genuine archive
// (matching what GoReleaser actually produces for the current
// runtime.GOOS/GOARCH) containing a "dev"/"dev.exe" file, plus a
// checksums.txt whose entry matches that archive's real sha256.
// ApplyUpdate must download both, verify the checksum, extract, and
// swap the real on-disk devHome/dev in place.
func TestApplyUpdate_DownloadsVerifiesAndSwapsTheBinary(t *testing.T) {
	binName := BinaryName(runtime.GOOS)
	archive := archiveName(runtime.GOOS, runtime.GOARCH)
	newContent := []byte("new dev binary content")

	var archiveData []byte
	if runtime.GOOS == "windows" {
		archiveData = buildTestZip(t, binName, newContent)
	} else {
		archiveData = buildTestTarGz(t, binName, newContent)
	}
	sum := sha256.Sum256(archiveData)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archive)

	mux := http.NewServeMux()
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archiveData)
	})
	mux.HandleFunc("/checksums", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	release := &LatestRelease{
		TagName: "v9.9.9",
		Assets: []ReleaseAsset{
			{Name: archive, BrowserDownloadURL: server.URL + "/archive"},
			{Name: "checksums.txt", BrowserDownloadURL: server.URL + "/checksums"},
		},
	}

	devHome := t.TempDir()
	currentPath := filepath.Join(devHome, binName)
	if err := os.WriteFile(currentPath, []byte("old dev binary content"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	if err := ApplyUpdate(context.Background(), &out, release, devHome); err != nil {
		t.Fatalf("ApplyUpdate() returned error: %v", err)
	}

	got, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("reading %s: %v", currentPath, err)
	}
	if string(got) != string(newContent) {
		t.Errorf("content at %s = %q, want %q (the new binary)", currentPath, got, newContent)
	}

	if oldContent, err := os.ReadFile(currentPath + ".old"); err == nil {
		// Best-effort removal: either it's gone (removed successfully)
		// or still there with the ORIGINAL content — never corrupted.
		if string(oldContent) != "old dev binary content" {
			t.Errorf(".old content = %q, want the original %q", oldContent, "old dev binary content")
		}
	}
}

func TestApplyUpdate_ArchiveMissingExpectedBinaryReturnsClearError(t *testing.T) {
	t.Parallel()
	archive := archiveName(runtime.GOOS, runtime.GOARCH)
	otherContent := []byte("not the dev binary")

	var archiveData []byte
	if runtime.GOOS == "windows" {
		archiveData = buildTestZip(t, "not-dev", otherContent)
	} else {
		archiveData = buildTestTarGz(t, "not-dev", otherContent)
	}
	sum := sha256.Sum256(archiveData)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archive)

	mux := http.NewServeMux()
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archiveData)
	})
	mux.HandleFunc("/checksums", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	release := &LatestRelease{
		TagName: "v9.9.9",
		Assets: []ReleaseAsset{
			{Name: archive, BrowserDownloadURL: server.URL + "/archive"},
			{Name: "checksums.txt", BrowserDownloadURL: server.URL + "/checksums"},
		},
	}

	devHome := t.TempDir()
	binName := BinaryName(runtime.GOOS)
	currentPath := filepath.Join(devHome, binName)
	if err := os.WriteFile(currentPath, []byte("old dev binary content"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := ApplyUpdate(context.Background(), &bytes.Buffer{}, release, devHome)
	if err == nil {
		t.Fatal("ApplyUpdate() returned nil error for an archive missing the expected binary")
	}
	if !strings.Contains(err.Error(), binName) {
		t.Errorf("error = %q, want it to name the expected binary %q", err.Error(), binName)
	}
}

func TestApplyUpdate_ChecksumMismatchLeavesCurrentBinaryUntouched(t *testing.T) {
	t.Parallel()
	binName := BinaryName(runtime.GOOS)
	archive := archiveName(runtime.GOOS, runtime.GOARCH)
	newContent := []byte("new dev binary content")

	var archiveData []byte
	if runtime.GOOS == "windows" {
		archiveData = buildTestZip(t, binName, newContent)
	} else {
		archiveData = buildTestTarGz(t, binName, newContent)
	}
	// Deliberately wrong checksum — does not match archiveData's real sha256.
	wrongChecksum := strings.Repeat("0", 64)
	checksums := fmt.Sprintf("%s  %s\n", wrongChecksum, archive)

	mux := http.NewServeMux()
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archiveData)
	})
	mux.HandleFunc("/checksums", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	release := &LatestRelease{
		TagName: "v9.9.9",
		Assets: []ReleaseAsset{
			{Name: archive, BrowserDownloadURL: server.URL + "/archive"},
			{Name: "checksums.txt", BrowserDownloadURL: server.URL + "/checksums"},
		},
	}

	devHome := t.TempDir()
	currentPath := filepath.Join(devHome, binName)
	originalContent := []byte("old dev binary content")
	if err := os.WriteFile(currentPath, originalContent, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := ApplyUpdate(context.Background(), &bytes.Buffer{}, release, devHome)
	if err == nil {
		t.Fatal("ApplyUpdate() returned nil error for a checksum mismatch")
	}

	got, readErr := os.ReadFile(currentPath)
	if readErr != nil {
		t.Fatalf("reading %s: %v", currentPath, readErr)
	}
	if string(got) != string(originalContent) {
		t.Errorf("content at %s = %q, want it untouched at %q", currentPath, got, originalContent)
	}
}

func TestApplyUpdate_MissingArchiveAssetReturnsClearError(t *testing.T) {
	t.Parallel()
	release := &LatestRelease{
		TagName: "v9.9.9",
		Assets:  []ReleaseAsset{{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/checksums.txt"}},
	}
	devHome := t.TempDir()

	err := ApplyUpdate(context.Background(), &bytes.Buffer{}, release, devHome)
	if err == nil {
		t.Fatal("ApplyUpdate() returned nil error for a release missing the archive for this platform")
	}
	if !strings.Contains(err.Error(), archiveName(runtime.GOOS, runtime.GOARCH)) {
		t.Errorf("error = %q, want it to name the missing asset", err.Error())
	}
}

// TestReportLeftover_UsesReassuringNotAlarmingWording pins the exact
// message shown when the old binary can't be removed after the swap —
// a real, expected case on Windows, where a running process can't
// delete its own executable image. Extracted into its own tiny
// function specifically so this wording is directly testable without
// needing to force a real removal failure (devHome must stay writable
// for the rename/swap steps themselves, so making it read-only to
// simulate a stuck .old file isn't an option here the way
// cmd/setup.go's installRelocation test could use a separate,
// independently-read-only extraction directory).
func TestReportLeftover_UsesReassuringNotAlarmingWording(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	reportLeftover(&out, "/tmp/dev.old")

	got := out.String()
	if !strings.Contains(got, "safe to delete") {
		t.Errorf("output = %q, want a reassuring \"safe to delete\" message", got)
	}
	if strings.Contains(got, "Could not remove") {
		t.Errorf("output = %q, want it not to use alarming \"Could not remove\" wording", got)
	}
}
