// Package downloader fetches files over HTTP and verifies their
// integrity before the caller does anything else with them.
package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jpsdm/dev/internal/filesystem"
)

// removeAndWrapErr removes path (cleaning up a partially-written
// download) and returns primary — or, if the removal itself also
// fails, both errors joined, so a permission problem on the leftover
// file isn't silently lost behind the original download failure.
func removeAndWrapErr(path string, primary error) error {
	if err := os.Remove(path); err != nil {
		return errors.Join(primary, fmt.Errorf("also failed to remove %s: %w", path, err))
	}
	return primary
}

// Download fetches url into destPath (creating parent directories as
// needed), then verifies the downloaded file's sha256 digest against
// wantSHA256 (case-insensitive hex). On any failure — network error,
// non-2xx response, write error, or checksum mismatch — destPath is
// removed and a descriptive error is returned. A caller must never act
// on destPath's contents unless Download returned nil.
//
// Download is DownloadWithProgress with a no-op progress callback —
// use that instead if you want to report progress as the download
// proceeds (e.g. via cliutil.WithSpinner).
func Download(ctx context.Context, url, destPath, wantSHA256 string) error {
	return DownloadWithProgress(ctx, url, destPath, wantSHA256, func(int64, int64) {})
}

// DownloadWithProgress behaves exactly like Download, but calls
// progress(written, total) as the download proceeds — written is the
// number of bytes written to destPath so far, and total is the
// server's reported Content-Length, or 0 if the server didn't send
// one (meaning the total is unknown/indeterminate, not that the
// download is already complete). progress may be called many times
// from within the copy loop; pass a func that does nothing if you
// don't need updates.
func DownloadWithProgress(ctx context.Context, url, destPath, wantSHA256 string, progress func(written, total int64)) error {
	if err := filesystem.EnsureDir(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building request for %s: %w", url, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: unexpected status %s", url, resp.Status)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", destPath, err)
	}

	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	hasher := sha256.New()
	pw := &progressWriter{total: total, report: progress}
	_, copyErr := io.Copy(io.MultiWriter(out, hasher, pw), resp.Body)
	closeErr := out.Close()

	if copyErr != nil {
		return removeAndWrapErr(destPath, fmt.Errorf("writing %s: %w", destPath, copyErr))
	}
	if closeErr != nil {
		return removeAndWrapErr(destPath, fmt.Errorf("closing %s: %w", destPath, closeErr))
	}

	gotSHA256 := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(gotSHA256, wantSHA256) {
		return removeAndWrapErr(destPath, fmt.Errorf("checksum mismatch for %s: got %s, want %s", url, gotSHA256, wantSHA256))
	}

	return nil
}

// progressWriter is an io.Writer that reports cumulative bytes
// written through it via report, so it can be layered into
// io.MultiWriter alongside the real destination and the checksum
// hasher without either of those needing to know about progress
// reporting at all.
type progressWriter struct {
	total, written int64
	report         func(written, total int64)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.written += int64(len(p))
	w.report(w.written, w.total)
	return len(p), nil
}
