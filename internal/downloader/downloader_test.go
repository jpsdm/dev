package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownload_Success(t *testing.T) {
	t.Parallel()
	content := []byte("hello from a fake node tarball")
	sum := sha256.Sum256(content)
	wantSHA256 := hex.EncodeToString(sum[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write(content); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "nested", "file.tar.gz")

	if err := Download(context.Background(), server.URL, dest, wantSHA256); err != nil {
		t.Fatalf("Download() returned error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("downloaded content = %q, want %q", got, content)
	}
}

func TestDownload_ChecksumMismatchRemovesFile(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte("actual content")); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "file.tar.gz")

	err := Download(context.Background(), server.URL, dest, strings.Repeat("0", 64))
	if err == nil {
		t.Fatal("Download() returned nil error for a checksum mismatch")
	}

	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("Download() left %s on disk after a checksum mismatch", dest)
	}
}

func TestRemoveAndWrapErr_JoinsRemovalFailureWithPrimaryError(t *testing.T) {
	t.Parallel()
	primary := errors.New("primary failure")
	nonexistent := filepath.Join(t.TempDir(), "does-not-exist")

	got := removeAndWrapErr(nonexistent, primary)
	if got == nil {
		t.Fatal("removeAndWrapErr() returned nil, want a non-nil error")
	}
	if !errors.Is(got, primary) {
		t.Errorf("removeAndWrapErr() = %v, want it to wrap the primary error", got)
	}
	if !strings.Contains(got.Error(), "also failed to remove") {
		t.Errorf("removeAndWrapErr() = %v, want it to mention the removal failure too", got)
	}
}

func TestRemoveAndWrapErr_ReturnsPrimaryWhenRemovalSucceeds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	primary := errors.New("primary failure")

	got := removeAndWrapErr(path, primary)
	if !errors.Is(got, primary) {
		t.Errorf("removeAndWrapErr() = %v, want it to be (or wrap) exactly the primary error when removal succeeds", got)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("removeAndWrapErr() did not actually remove the file")
	}
}

func TestDownload_ServerErrorReturnsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "file.tar.gz")

	err := Download(context.Background(), server.URL, dest, "irrelevant")
	if err == nil {
		t.Fatal("Download() returned nil error for a 404 response")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("Download() created %s despite a 404 response", dest)
	}
}

func TestDownloadWithProgress_ReportsBytesWrittenAndTotal(t *testing.T) {
	t.Parallel()
	content := []byte(strings.Repeat("x", 1000))
	sum := sha256.Sum256(content)
	wantSHA256 := hex.EncodeToString(sum[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		if _, err := w.Write(content); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "file.bin")

	var calls int
	var lastWritten, lastTotal int64
	err := DownloadWithProgress(context.Background(), server.URL, dest, wantSHA256, func(written, total int64) {
		calls++
		lastWritten, lastTotal = written, total
	})
	if err != nil {
		t.Fatalf("DownloadWithProgress() returned error: %v", err)
	}
	if calls == 0 {
		t.Fatal("DownloadWithProgress() never called the progress callback")
	}
	if lastWritten != 1000 {
		t.Errorf("final written = %d, want 1000", lastWritten)
	}
	if lastTotal != 1000 {
		t.Errorf("final total = %d, want 1000 (from Content-Length)", lastTotal)
	}
}

func TestDownloadWithProgress_UnknownContentLengthReportsZeroTotal(t *testing.T) {
	t.Parallel()
	content := []byte("no content-length header on this response")
	sum := sha256.Sum256(content)
	wantSHA256 := hex.EncodeToString(sum[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// httptest still sets a real Content-Length by default unless
		// we use a hijacked/chunked response — force it absent by
		// disabling length sniffing via chunked transfer encoding.
		w.Header().Set("Transfer-Encoding", "chunked")
		flusher, _ := w.(http.Flusher)
		if _, err := w.Write(content); err != nil {
			t.Errorf("writing response: %v", err)
		}
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "file.bin")

	var lastTotal int64 = -1
	err := DownloadWithProgress(context.Background(), server.URL, dest, wantSHA256, func(written, total int64) {
		lastTotal = total
	})
	if err != nil {
		t.Fatalf("DownloadWithProgress() returned error: %v", err)
	}
	if lastTotal != 0 {
		t.Errorf("total = %d, want 0 (unknown/indeterminate) when Content-Length isn't sent", lastTotal)
	}
}

func TestDownload_IsDownloadWithProgressWithANoOpCallback(t *testing.T) {
	t.Parallel()
	content := []byte("plain Download must still work exactly as before")
	sum := sha256.Sum256(content)
	wantSHA256 := hex.EncodeToString(sum[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write(content); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "file.bin")

	if err := Download(context.Background(), server.URL, dest, wantSHA256); err != nil {
		t.Fatalf("Download() returned error: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("downloaded content = %q, want %q", got, content)
	}
}
