// Package bun implements the dev Runtime interface for Bun, using the
// oven-sh/bun GitHub releases as the distribution source.
package bun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/downloader"
	"github.com/jpsdm/dev/internal/filesystem"
	"github.com/jpsdm/dev/internal/installer"
	"github.com/jpsdm/dev/internal/platform"
	devruntime "github.com/jpsdm/dev/internal/runtime"
)

const bunBaseURL = "https://api.github.com/repos/oven-sh/bun"

// Bun implements devruntime.Runtime for Bun, via its GitHub releases.
type Bun struct {
	baseURL string // overridable in tests; defaults to bunBaseURL
}

// New returns a production Bun provider pointed at the real GitHub API.
func New() *Bun {
	return &Bun{baseURL: bunBaseURL}
}

func (b *Bun) Name() string { return "bun" }

// bunOS maps this project's platform.OS() to Bun's release-asset
// vocabulary (an identity mapping for the three supported hosts).
func bunOS() (string, error) {
	return mapBunOS(platform.OS())
}

// mapBunOS is bunOS's pure mapping logic, split out so its
// unsupported-value branch is testable regardless of the host.
func mapBunOS(osName string) (string, error) {
	switch osName {
	case "linux", "darwin", "windows":
		return osName, nil
	default:
		return "", fmt.Errorf("unsupported operating system: %s", osName)
	}
}

// bunArch maps this project's platform.Arch() to Bun's asset vocabulary —
// "x64" and "aarch64", not "amd64" and "arm64".
func bunArch() (string, error) {
	return mapBunArch(platform.Arch())
}

// mapBunArch is bunArch's pure mapping logic; see mapBunOS.
func mapBunArch(archName string) (string, error) {
	switch archName {
	case "amd64":
		return "x64", nil
	case "arm64":
		return "aarch64", nil
	default:
		return "", fmt.Errorf("unsupported architecture: %s", archName)
	}
}

type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"` // "sha256:<hex>"
}

type githubRelease struct {
	TagName    string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

// fetchReleases retrieves the 100 most recent Bun releases. That window
// covers every minor line Bun has shipped so far; a line old enough to
// fall out of it is simply not offered.
func (b *Bun) fetchReleases(ctx context.Context) ([]githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/releases?per_page=100", nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching Bun releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching Bun releases: unexpected status %s", resp.Status)
	}

	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("parsing Bun releases: %w", err)
	}
	return releases, nil
}

// tagRE matches a stable Bun release tag. The "canary" tag and any
// suffixed pre-release tag don't match.
var tagRE = regexp.MustCompile(`^bun-v(\d+)\.(\d+)\.(\d+)$`)

// parseTag splits a release tag into its "major.minor" line and patch.
func parseTag(tag string) (line string, patch int, ok bool) {
	m := tagRE.FindStringSubmatch(tag)
	if m == nil {
		return "", 0, false
	}
	patch, err := strconv.Atoi(m[3])
	if err != nil {
		return "", 0, false
	}
	return m[1] + "." + m[2], patch, true
}

func splitLine(line string) (major, minor int) {
	parts := strings.SplitN(line, ".", 2)
	major, _ = strconv.Atoi(parts[0])
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	return major, minor
}

// compareLines orders two "major.minor" lines numerically ("1.10" is
// newer than "1.9", which a string comparison gets wrong).
func compareLines(a, b string) int {
	aMajor, aMinor := splitLine(a)
	bMajor, bMinor := splitLine(b)
	if aMajor != bMajor {
		return aMajor - bMajor
	}
	return aMinor - bMinor
}

// assetFor returns the release asset for this platform together with
// its sha256, if the release has one with a usable digest. An asset
// with no sha256 digest is never installable: there would be nothing to
// verify the download against.
func assetFor(assets []releaseAsset, osName, archName string) (releaseAsset, string, bool) {
	want := fmt.Sprintf("bun-%s-%s.zip", osName, archName)
	for _, a := range assets {
		if a.Name != want {
			continue
		}
		sum, ok := strings.CutPrefix(a.Digest, "sha256:")
		if !ok || sum == "" {
			continue
		}
		return a, sum, true
	}
	return releaseAsset{}, "", false
}

// ListRemoteVersions returns one Version per installable "major.minor"
// line, newest first. A line is included when it has at least one
// stable release with a checksummed asset for this platform. LTS is
// always false — Bun has no LTS concept.
func (b *Bun) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error) {
	osName, err := bunOS()
	if err != nil {
		return nil, err
	}
	archName, err := bunArch()
	if err != nil {
		return nil, err
	}
	releases, err := b.fetchReleases(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	for _, r := range releases {
		if r.Draft || r.Prerelease {
			continue
		}
		line, _, ok := parseTag(r.TagName)
		if !ok {
			continue
		}
		if _, _, found := assetFor(r.Assets, osName, archName); !found {
			continue
		}
		seen[line] = true
	}

	versions := make([]devruntime.Version, 0, len(seen))
	for line := range seen {
		versions = append(versions, devruntime.Version{Name: line})
	}
	sort.Slice(versions, func(i, j int) bool {
		return compareLines(versions[i].Name, versions[j].Name) > 0
	})
	return versions, nil
}

// ListInstalledVersions returns one Version per subdirectory of
// versions/bun/, skipping leftover temp-extraction and swap-backup
// directories a killed-mid-install process can leave behind.
func (b *Bun) ListInstalledVersions() ([]devruntime.Version, error) {
	dir, err := platform.VersionsDir()
	if err != nil {
		return nil, err
	}
	bunDir := filepath.Join(dir, "bun")

	entries, err := os.ReadDir(bunDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", bunDir, err)
	}

	versions := make([]devruntime.Version, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".old") {
			continue
		}
		versions = append(versions, devruntime.Version{Name: name})
	}
	return versions, nil
}

// CurrentVersion reads current/bun. A missing file means no active
// version, not an error.
func (b *Bun) CurrentVersion() (*devruntime.Version, error) {
	dir, err := platform.CurrentDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "bun")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	name := strings.TrimSpace(string(data))
	if name == "" {
		return nil, nil
	}
	return &devruntime.Version{Name: name}, nil
}

// bunRelease is the resolved, ready-to-download release resolveRelease
// finds for a requested line.
type bunRelease struct {
	Version  string // e.g. "1.4.2" — used as the .dev-release marker content
	URL      string
	Filename string
	SHA256   string
}

// resolveRelease finds the newest stable release on the requested line
// ("1.4") with a checksummed asset for this platform. The checksum is
// embedded in the release listing, so one HTTP call is enough. A
// zero-value bunRelease with a nil error means nothing matched.
func (b *Bun) resolveRelease(ctx context.Context, name string) (bunRelease, error) {
	osName, err := bunOS()
	if err != nil {
		return bunRelease{}, err
	}
	archName, err := bunArch()
	if err != nil {
		return bunRelease{}, err
	}
	releases, err := b.fetchReleases(ctx)
	if err != nil {
		return bunRelease{}, err
	}

	var best bunRelease
	bestPatch := -1
	for _, r := range releases {
		if r.Draft || r.Prerelease {
			continue
		}
		line, patch, ok := parseTag(r.TagName)
		if !ok || line != name || patch <= bestPatch {
			continue
		}
		asset, sum, found := assetFor(r.Assets, osName, archName)
		if !found {
			continue
		}
		bestPatch = patch
		best = bunRelease{
			Version:  strings.TrimPrefix(r.TagName, "bun-v"),
			URL:      asset.URL,
			Filename: asset.Name,
			SHA256:   sum,
		}
	}
	if bestPatch == -1 {
		return bunRelease{}, nil
	}

	// The version and the asset filename both come from the untrusted
	// API response and both flow into filesystem paths in Install.
	if err := devruntime.ValidVersionName(best.Version); err != nil {
		return bunRelease{}, fmt.Errorf("resolving Bun %s: %w", name, err)
	}
	if err := devruntime.ValidVersionName(best.Filename); err != nil {
		return bunRelease{}, fmt.Errorf("resolving Bun %s: %w", name, err)
	}
	if best.URL == "" {
		return bunRelease{}, fmt.Errorf("resolving Bun %s: incomplete release metadata from GitHub", name)
	}
	return best, nil
}

// extractBun extracts archivePath into destDir, arranging it as
// bin/bun (bin/bun.exe on Windows) plus a bunx alias. Bun's zip holds a
// single top-level directory containing just the executable; moving it
// to bin/ gives Bun the same layout as every other provider, so BinDir
// needs no special case. destDir is only replaced once extraction and
// the layout check both succeed.
func extractBun(archivePath, destDir, osName string) error {
	parent := filepath.Dir(destDir)
	if err := filesystem.EnsureDir(parent, 0o755); err != nil {
		return err
	}
	tmpParent, err := os.MkdirTemp(parent, ".tmp-bun-extract-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpParent)

	extractedRoot := filepath.Join(tmpParent, "root")
	if err := installer.ExtractAtomic(archivePath, extractedRoot); err != nil {
		return err
	}
	entries, err := os.ReadDir(extractedRoot)
	if err != nil {
		return fmt.Errorf("reading extracted contents: %w", err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return fmt.Errorf("unexpected archive layout: expected exactly one top-level directory")
	}

	staged := filepath.Join(tmpParent, "staged")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		return fmt.Errorf("creating staging dir: %w", err)
	}
	binDir := filepath.Join(staged, "bin")
	if err := os.Rename(filepath.Join(extractedRoot, entries[0].Name()), binDir); err != nil {
		return fmt.Errorf("staging extracted contents: %w", err)
	}
	if !filesystem.Exists(binaryPathForOS(osName, staged, "bun")) {
		return fmt.Errorf("unexpected archive layout: no bun executable found")
	}
	if err := linkBunx(binDir, osName); err != nil {
		return err
	}

	backupDir := destDir + ".old"
	if err := installer.ReconcileStaleBackup(destDir, backupDir); err != nil {
		return fmt.Errorf("reconciling previous install state for %s: %w", destDir, err)
	}
	hadExisting := false
	if _, err := os.Stat(destDir); err == nil {
		if err := os.Rename(destDir, backupDir); err != nil {
			return fmt.Errorf("moving existing %s aside: %w", destDir, err)
		}
		hadExisting = true
	}
	if err := os.Rename(staged, destDir); err != nil {
		if hadExisting {
			_ = os.Rename(backupDir, destDir) // best-effort restore; nothing more we can do if this also fails
		}
		return fmt.Errorf("moving %s to %s: %w", staged, destDir, err)
	}
	if hadExisting {
		os.RemoveAll(backupDir)
	}
	return nil
}

// linkBunx adds the bunx alias next to bun, which Bun decides to behave
// as `bun x` by its own executable name. Bun's own installer makes a
// symlink on Unix and a copy on Windows (symlinks need elevated rights
// there); this does the same. A bunx already shipped in the archive is
// left alone.
func linkBunx(binDir, osName string) error {
	if osName == "windows" {
		dst := filepath.Join(binDir, "bunx.exe")
		if filesystem.Exists(dst) {
			return nil
		}
		return copyFile(filepath.Join(binDir, "bun.exe"), dst)
	}
	dst := filepath.Join(binDir, "bunx")
	if filesystem.Exists(dst) {
		return nil
	}
	if err := os.Symlink("bun", dst); err != nil {
		return fmt.Errorf("creating bunx alias: %w", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("creating bunx alias: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("creating bunx alias: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("creating bunx alias: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("creating bunx alias: %w", err)
	}
	return nil
}

// Install resolves name (e.g. "1.4") to the newest matching stable
// release, downloads and checksum-verifies it, and installs it to
// versions/bun/<name>. Re-running Install with the same name is a no-op
// (no download) if the exact same release is already installed there;
// if a newer patch has shipped, it replaces the existing install.
func (b *Bun) Install(ctx context.Context, name string) error {
	if err := devruntime.ValidVersionName(name); err != nil {
		return err
	}
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "bun", name)
	releaseMarker := filepath.Join(destDir, ".dev-release")

	release, err := b.resolveRelease(ctx, name)
	if err != nil {
		// If checking for the newest patch fails outright (offline,
		// GitHub down, rate-limited) but this line has a release marker
		// from a previous install, report that release and succeed.
		if installed, readErr := os.ReadFile(releaseMarker); readErr == nil {
			cachedVersion := strings.TrimSpace(string(installed))
			cliutil.Success("Bun %s is already installed (%s) — could not check for updates: %s", name, cachedVersion, err)
			return nil
		}
		return err
	}
	if release.Version == "" {
		osName, _ := bunOS()
		archName, _ := bunArch()
		return fmt.Errorf("no Bun release of line %s for %s/%s", name, osName, archName)
	}

	if installed, err := os.ReadFile(releaseMarker); err == nil && strings.TrimSpace(string(installed)) == release.Version {
		cliutil.Success("Bun %s is already installed (%s)", name, release.Version)
		return nil
	}

	cacheDir, err := platform.CacheDir()
	if err != nil {
		return err
	}
	// The cached name is versioned: every Bun release's asset is called
	// bun-<os>-<arch>.zip, so the bare filename would collide across
	// versions.
	archivePath := filepath.Join(cacheDir, "bun-"+release.Version+"-"+release.Filename)

	downloadMsg := fmt.Sprintf("Downloading Bun %s...", release.Version)
	if err := cliutil.WithSpinner(downloadMsg, func(update func(int64, int64)) error {
		return downloader.DownloadWithProgress(ctx, release.URL, archivePath, release.SHA256, update)
	}); err != nil {
		return fmt.Errorf("installing Bun %s: %w", name, err)
	}

	osName, err := bunOS()
	if err != nil {
		return err
	}
	if err := cliutil.WithSpinner("Installing...", func(func(int64, int64)) error {
		return extractBun(archivePath, destDir, osName)
	}); err != nil {
		return fmt.Errorf("installing Bun %s: %w", name, err)
	}

	if err := filesystem.WriteFileAtomic(releaseMarker, []byte(release.Version), 0o644); err != nil {
		return fmt.Errorf("recording installed release for Bun %s: %w", name, err)
	}

	cliutil.Success("Bun %s installed", name)
	return nil
}

// Uninstall removes versions/bun/<name>. If it was the active version,
// current/bun is cleared too.
func (b *Bun) Uninstall(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "bun", name)

	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Bun is not installed", name)
	}

	current, err := b.CurrentVersion()
	if err != nil {
		return err
	}

	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("removing %s: %w", destDir, err)
	}

	if current != nil && current.Name == name {
		currentDir, err := platform.CurrentDir()
		if err != nil {
			return err
		}
		markerPath := filepath.Join(currentDir, "bun")
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clearing active version marker: %w", err)
		}

		cliutil.Step("Bun %s was the active version; no version is active now", name)
	}

	cliutil.Success("Bun %s uninstalled", name)
	return nil
}

// Activate makes name the active Bun version by writing it to
// current/bun. name must already be installed.
func (b *Bun) Activate(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "bun", name)
	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Bun is not installed — run `dev lang install bun %s` first", name, name)
	}

	currentDir, err := platform.CurrentDir()
	if err != nil {
		return err
	}
	markerPath := filepath.Join(currentDir, "bun")
	if err := filesystem.WriteFileAtomic(markerPath, []byte(name), 0o644); err != nil {
		return fmt.Errorf("activating Bun %s: %w", name, err)
	}

	cliutil.Success("Bun %s activated", name)
	return nil
}

// binaryPathForOS resolves binName's path for a given bunOS() value.
// The layout is bin/<name> everywhere (extractBun arranges it so);
// Windows binaries carry a .exe suffix.
func binaryPathForOS(osName, versionDir, binName string) string {
	if osName == "windows" {
		return filepath.Join(versionDir, "bin", binName+".exe")
	}
	return filepath.Join(versionDir, "bin", binName)
}

// BinaryPath resolves the absolute path to binName inside an installed
// Bun version at versionDir, using this host's own OS layout.
func (b *Bun) BinaryPath(versionDir, binName string) (string, error) {
	osName, err := bunOS()
	if err != nil {
		return "", err
	}

	candidate := binaryPathForOS(osName, versionDir, binName)
	if !filesystem.Exists(candidate) {
		return "", fmt.Errorf("%s not found in Bun installation at %s", binName, versionDir)
	}
	return candidate, nil
}

// BinDir returns the directory whose contents should be exposed on PATH
// for an active Bun version — always bin/.
func (b *Bun) BinDir(versionDir string) (string, error) {
	return filepath.Join(versionDir, "bin"), nil
}
