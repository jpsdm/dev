package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/downloader"
	"github.com/jpsdm/dev/internal/installer"
)

// archiveName returns the release asset filename dev's own
// .goreleaser.yml produces for goos/goarch — "dev_linux_amd64.tar.gz",
// "dev_windows_amd64.zip", etc. Windows gets a .zip archive; every
// other platform gets .tar.gz.
func archiveName(goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("dev_%s_%s.%s", goos, goarch, ext)
}

// BinaryName returns the dev binary's filename inside the archive, and
// at devHome — "dev" everywhere except Windows, which gets "dev.exe".
// Exported so cmd/update.go can compute the freshly-installed binary's
// path without duplicating this platform-naming rule.
func BinaryName(goos string) string {
	if goos == "windows" {
		return "dev.exe"
	}
	return "dev"
}

// findAsset returns the release asset named name, or an error naming
// every asset actually present — release.Assets never legitimately
// omits a platform this project builds for (see .goreleaser.yml's
// goos/goarch matrix), so a miss here means either a malformed
// release or a platform dev doesn't ship for.
func findAsset(assets []ReleaseAsset, name string) (ReleaseAsset, error) {
	for _, a := range assets {
		if a.Name == name {
			return a, nil
		}
	}
	var names []string
	for _, a := range assets {
		names = append(names, a.Name)
	}
	return ReleaseAsset{}, fmt.Errorf("no release asset named %s (have: %s)", name, strings.Join(names, ", "))
}

// checksumFor extracts the sha256 hex digest for filename from a
// checksums.txt body in the standard "sha256sum" format
// ("<hex>  <filename>", one entry per line, as GoReleaser writes it).
func checksumFor(checksumsBody, filename string) (string, error) {
	for _, line := range strings.Split(checksumsBody, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == filename {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum entry for %s in checksums.txt", filename)
}

// fetchText fetches url and returns its body as a string — used only
// for checksums.txt, which is the trust anchor internal/downloader.Download
// verifies every other file against (the same "root of trust fetched
// once over HTTPS" model every checksum-based installer uses), not
// something to verify itself.
func fetchText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("building request for %s: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: unexpected status %s", url, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", url, err)
	}
	return string(body), nil
}

// ApplyUpdate downloads, checksum-verifies, and installs release in
// place of the binary currently at devHome (devHome/dev or
// devHome/dev.exe).
func ApplyUpdate(ctx context.Context, out io.Writer, release *LatestRelease, devHome string) error {
	archive := archiveName(runtime.GOOS, runtime.GOARCH)
	archiveAsset, err := findAsset(release.Assets, archive)
	if err != nil {
		return err
	}
	checksumsAsset, err := findAsset(release.Assets, "checksums.txt")
	if err != nil {
		return err
	}

	checksumsBody, err := fetchText(ctx, checksumsAsset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("downloading checksums.txt: %w", err)
	}
	wantSHA256, err := checksumFor(checksumsBody, archive)
	if err != nil {
		return err
	}

	scratchDir, err := os.MkdirTemp(devHome, ".update-*")
	if err != nil {
		return fmt.Errorf("creating temp directory in %s: %w", devHome, err)
	}
	defer os.RemoveAll(scratchDir)

	archivePath := filepath.Join(scratchDir, archive)
	if err := cliutil.FWithSpinner(out, fmt.Sprintf("Downloading %s...", release.TagName), func(update func(int64, int64)) error {
		return downloader.DownloadWithProgress(ctx, archiveAsset.BrowserDownloadURL, archivePath, wantSHA256, update)
	}); err != nil {
		return fmt.Errorf("downloading %s: %w", archive, err)
	}

	extractDir := filepath.Join(scratchDir, "extracted")
	if err := cliutil.FWithSpinner(out, "Installing...", func(func(int64, int64)) error {
		return installer.ExtractAtomic(archivePath, extractDir)
	}); err != nil {
		return fmt.Errorf("extracting %s: %w", archive, err)
	}

	binName := BinaryName(runtime.GOOS)
	newBinary := filepath.Join(extractDir, binName)
	if _, err := os.Stat(newBinary); err != nil {
		return fmt.Errorf("extracted archive has no %s: %w", binName, err)
	}

	currentPath := filepath.Join(devHome, binName)
	oldPath := currentPath + ".old"

	// Rename-then-best-effort-delete, mirroring cmd/setup.go's
	// installRelocation: on Windows, a running process can't delete or
	// overwrite its own executable image directly, but renaming it
	// aside first (to a name nothing has open) works, then the new
	// binary takes the original name.
	if err := os.Rename(currentPath, oldPath); err != nil {
		return fmt.Errorf("moving current %s aside: %w", binName, err)
	}
	if err := os.Rename(newBinary, currentPath); err != nil {
		_ = os.Rename(oldPath, currentPath) // best-effort restore
		return fmt.Errorf("installing new %s: %w", binName, err)
	}

	if err := os.Remove(oldPath); err != nil {
		reportLeftover(out, oldPath)
	}
	return nil
}

// reportLeftover prints the reassuring "safe to delete" message for a
// leftover file that couldn't be removed after copying — expected to
// happen often on Windows, where a running process can't delete its
// own executable image. Extracted into its own function so the exact
// wording is directly unit-testable without needing to force a real
// removal failure. Mirrors cmd/setup.go's installRelocation, which
// established this exact framing for the same reason.
func reportLeftover(out io.Writer, path string) {
	cliutil.Fsuccess(out, "Old copy is set up and safe to delete — you can remove %s now", path)
}
