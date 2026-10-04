// Package rust implements the dev Runtime interface for Rust, installing
// the official standalone toolchain (rustc, cargo, rust-std) published
// on static.rust-lang.org. Version discovery uses the rust-lang/rust
// GitHub releases; the download URL and its sha256 come from the
// version's channel manifest.
package rust

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
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

const (
	rustGitHubURL = "https://api.github.com/repos/rust-lang/rust"
	rustDistURL   = "https://static.rust-lang.org/dist"
)

// Rust implements devruntime.Runtime for the Rust toolchain.
type Rust struct {
	githubURL string // overridable in tests; defaults to rustGitHubURL
	distURL   string // overridable in tests; defaults to rustDistURL
}

// New returns a production Rust provider pointed at the real GitHub API
// and static.rust-lang.org.
func New() *Rust {
	return &Rust{githubURL: rustGitHubURL, distURL: rustDistURL}
}

func (r *Rust) Name() string { return "rust" }

// rustOS validates this project's platform.OS() against the hosts
// Rust publishes toolchains for.
func rustOS() (string, error) {
	return mapRustOS(platform.OS())
}

func mapRustOS(osName string) (string, error) {
	switch osName {
	case "linux", "darwin", "windows":
		return osName, nil
	default:
		return "", fmt.Errorf("unsupported operating system: %s", osName)
	}
}

// rustTarget maps this host to the Rust target triple of its toolchain.
// Windows uses the MSVC toolchain, the one rustup installs by default.
func rustTarget() (string, error) {
	return mapRustTarget(platform.OS(), platform.Arch())
}

// mapRustTarget is rustTarget's pure mapping logic, split out so every
// branch is testable regardless of the host.
func mapRustTarget(osName, archName string) (string, error) {
	var arch string
	switch archName {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", fmt.Errorf("unsupported architecture: %s", archName)
	}
	switch osName {
	case "linux":
		return arch + "-unknown-linux-gnu", nil
	case "darwin":
		return arch + "-apple-darwin", nil
	case "windows":
		return arch + "-pc-windows-msvc", nil
	default:
		return "", fmt.Errorf("unsupported operating system: %s", osName)
	}
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// fetchReleases retrieves the 100 most recent rust-lang/rust releases
// (several years of stable versions); an older line isn't offered.
func (r *Rust) fetchReleases(ctx context.Context) ([]githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.githubURL+"/releases?per_page=100", nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching Rust releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching Rust releases: unexpected status %s", resp.Status)
	}

	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("parsing Rust releases: %w", err)
	}
	return releases, nil
}

// tagRE matches a stable Rust release tag ("1.85.0").
var tagRE = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

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

// compareLines orders two "major.minor" lines numerically ("1.100" is
// newer than "1.99", which a string comparison gets wrong).
func compareLines(a, b string) int {
	aMajor, aMinor := splitLine(a)
	bMajor, bMinor := splitLine(b)
	if aMajor != bMajor {
		return aMajor - bMajor
	}
	return aMinor - bMinor
}

// ListRemoteVersions returns one Version per stable "1.N" line, newest
// first. It doesn't probe each line for a build for this platform —
// that would cost a request per line — so Install reports a line that
// has none (e.g. Windows/arm64 before Rust 1.7x). LTS is always false —
// Rust has no LTS concept.
func (r *Rust) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error) {
	releases, err := r.fetchReleases(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	for _, rel := range releases {
		if rel.Draft || rel.Prerelease {
			continue
		}
		if line, _, ok := parseTag(rel.TagName); ok {
			seen[line] = true
		}
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
// versions/rust/, skipping leftover temp-extraction and swap-backup
// directories a killed-mid-install process can leave behind.
func (r *Rust) ListInstalledVersions() ([]devruntime.Version, error) {
	dir, err := platform.VersionsDir()
	if err != nil {
		return nil, err
	}
	rustDir := filepath.Join(dir, "rust")

	entries, err := os.ReadDir(rustDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", rustDir, err)
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

// CurrentVersion reads current/rust. A missing file means no active
// version, not an error.
func (r *Rust) CurrentVersion() (*devruntime.Version, error) {
	dir, err := platform.CurrentDir()
	if err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "rust")

	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	name := strings.TrimSpace(string(data))
	if name == "" {
		return nil, nil
	}
	return &devruntime.Version{Name: name}, nil
}

// parseManifestTarget extracts the combined-installer tarball's URL and
// sha256 for triple from a channel manifest. The manifest is TOML, but
// the one section needed is flat key = "value" lines under the exact
// header [pkg.rust.target.<triple>], so a small line scanner avoids a
// TOML dependency. Matching the header exactly matters: sibling
// sections like [pkg.rustc.target.<triple>] and the
// [[pkg.rust.target.<triple>.components]] sub-tables carry their own
// url/hash/target keys. ok is false if the section is missing, marked
// unavailable, or lacks a url or hash.
func parseManifestTarget(manifest, triple string) (url, hash string, ok bool) {
	header := "[pkg.rust.target." + triple + "]"
	inSection := false
	available := false

	scanner := bufio.NewScanner(strings.NewReader(manifest))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			if inSection {
				break
			}
			inSection = line == header
			continue
		}
		if !inSection {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch key {
		case "available":
			available = value == "true"
		case "url":
			url = value
		case "hash":
			hash = value
		}
	}
	if !available || url == "" || hash == "" {
		return "", "", false
	}
	return url, hash, true
}

// sha256RE matches a hex-encoded sha256 digest.
var sha256RE = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// rustRelease is the resolved, ready-to-download release resolveRelease
// finds for a requested line.
type rustRelease struct {
	Version  string // e.g. "1.85.0" — used as the .dev-release marker content
	URL      string
	Filename string
	SHA256   string
}

// fetchManifest retrieves the channel manifest for one exact version.
func (r *Rust) fetchManifest(ctx context.Context, version string) (string, error) {
	url := fmt.Sprintf("%s/channel-rust-%s.toml", r.distURL, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching Rust %s manifest: %w", version, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching Rust %s manifest: unexpected status %s", version, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading Rust %s manifest: %w", version, err)
	}
	return string(body), nil
}

// resolveRelease finds the newest stable release on the requested line
// ("1.85") and reads its tarball URL and checksum for this platform from
// that version's manifest. A zero-value rustRelease with a nil error
// means nothing matched: an unknown line, or a real one with no build
// for this target.
func (r *Rust) resolveRelease(ctx context.Context, name string) (rustRelease, error) {
	triple, err := rustTarget()
	if err != nil {
		return rustRelease{}, err
	}
	releases, err := r.fetchReleases(ctx)
	if err != nil {
		return rustRelease{}, err
	}

	bestVersion := ""
	bestPatch := -1
	for _, rel := range releases {
		if rel.Draft || rel.Prerelease {
			continue
		}
		line, patch, ok := parseTag(rel.TagName)
		if !ok || line != name || patch <= bestPatch {
			continue
		}
		bestPatch = patch
		bestVersion = rel.TagName
	}
	if bestPatch == -1 {
		return rustRelease{}, nil
	}

	manifest, err := r.fetchManifest(ctx, bestVersion)
	if err != nil {
		return rustRelease{}, fmt.Errorf("resolving Rust %s: %w", name, err)
	}
	url, hash, ok := parseManifestTarget(manifest, triple)
	if !ok {
		return rustRelease{}, nil
	}

	// The filename is taken from the manifest's URL and flows into a
	// filesystem path in Install, so it gets the same path-safety check
	// as any other externally-sourced path component; the version and
	// hash are validated too since they end up in a marker file and a
	// checksum comparison.
	filename := path.Base(url)
	if err := devruntime.ValidVersionName(filename); err != nil {
		return rustRelease{}, fmt.Errorf("resolving Rust %s: %w", name, err)
	}
	if !strings.HasSuffix(filename, ".tar.gz") {
		return rustRelease{}, fmt.Errorf("resolving Rust %s: unexpected archive %q in manifest", name, filename)
	}
	if err := devruntime.ValidVersionName(bestVersion); err != nil {
		return rustRelease{}, fmt.Errorf("resolving Rust %s: %w", name, err)
	}
	if !sha256RE.MatchString(hash) {
		return rustRelease{}, fmt.Errorf("resolving Rust %s: malformed checksum in manifest", name)
	}

	return rustRelease{Version: bestVersion, URL: url, Filename: filename, SHA256: hash}, nil
}

// mergeDir moves the contents of src into dst, merging directories that
// exist on both sides. Entries are renamed, not copied, so this is
// cheap for the toolchain's many large files; src and dst are both
// inside the same temp directory, so the rename never crosses a
// filesystem. Symlinks are moved as links. Where two components ship
// the same file path, the later one wins.
func mergeDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := mergeDir(from, to); err != nil {
				return err
			}
			continue
		}
		if err := os.RemoveAll(to); err != nil {
			return err
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	return nil
}

// readComponents reads the installer's top-level "components" file: one
// component directory name per line. Every name becomes a path
// component under the extraction directory, so each is validated.
func readComponents(installerDir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(installerDir, "components"))
	if err != nil {
		return nil, fmt.Errorf("reading installer components list: %w", err)
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if err := devruntime.ValidVersionName(name); err != nil {
			return nil, fmt.Errorf("invalid component name %q in installer: %w", name, err)
		}
		names = append(names, name)
	}
	return names, nil
}

// skippedComponent is a component deliberately left out of the install:
// the offline HTML docs are by far the largest piece of the toolchain
// and `rustup`'s default minimal profile omits them too.
const skippedComponent = "rust-docs"

// extractRust extracts Rust's combined installer tarball into destDir.
// The tarball isn't a ready-made tree: it holds one directory per
// component (rustc, cargo, rust-std-<target>, ...), each laid out as
// bin/, lib/, share/ for a common prefix, plus an install.sh that does
// the merge on Unix. Doing the merge here keeps `dev` free of a shell
// dependency and works on Windows. destDir is only replaced once the
// extraction, merge, and layout check all succeed.
func extractRust(archivePath, destDir string) error {
	parent := filepath.Dir(destDir)
	if err := filesystem.EnsureDir(parent, 0o755); err != nil {
		return err
	}
	tmpParent, err := os.MkdirTemp(parent, ".tmp-rust-extract-*")
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
	installerDir := filepath.Join(extractedRoot, entries[0].Name())

	components, err := readComponents(installerDir)
	if err != nil {
		return err
	}
	staged := filepath.Join(tmpParent, "staged")
	for _, c := range components {
		if c == skippedComponent {
			continue
		}
		componentDir := filepath.Join(installerDir, c)
		info, err := os.Lstat(componentDir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("unexpected archive layout: component %q listed but not found", c)
		}
		// manifest.in is the installer's own bookkeeping, not part of the
		// toolchain; remove it so it doesn't land in the merged tree.
		if err := os.Remove(filepath.Join(componentDir, "manifest.in")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("preparing component %q: %w", c, err)
		}
		if err := mergeDir(componentDir, staged); err != nil {
			return fmt.Errorf("merging component %q: %w", c, err)
		}
	}
	if info, err := os.Stat(filepath.Join(staged, "bin")); err != nil || !info.IsDir() {
		return fmt.Errorf("unexpected archive layout: installer produced no bin directory")
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

// Install resolves name (e.g. "1.85") to the newest matching stable
// release, downloads and checksum-verifies it, and installs it to
// versions/rust/<name>. Re-running Install with the same name is a
// no-op (no download) if the exact same release is already installed
// there; if a newer patch has shipped, it replaces the existing install.
func (r *Rust) Install(ctx context.Context, name string) error {
	if err := devruntime.ValidVersionName(name); err != nil {
		return err
	}
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "rust", name)
	releaseMarker := filepath.Join(destDir, ".dev-release")

	release, err := r.resolveRelease(ctx, name)
	if err != nil {
		// If checking for the newest patch fails outright (offline,
		// GitHub or static.rust-lang.org down, rate-limited) but this
		// line has a release marker from a previous install, report that
		// release and succeed.
		if installed, readErr := os.ReadFile(releaseMarker); readErr == nil {
			cachedVersion := strings.TrimSpace(string(installed))
			cliutil.Success("Rust %s is already installed (%s) — could not check for updates: %s", name, cachedVersion, err)
			return nil
		}
		return err
	}
	if release.Version == "" {
		triple, _ := rustTarget()
		return fmt.Errorf("no Rust release of line %s for %s", name, triple)
	}

	if installed, err := os.ReadFile(releaseMarker); err == nil && strings.TrimSpace(string(installed)) == release.Version {
		cliutil.Success("Rust %s is already installed (%s)", name, release.Version)
		return nil
	}

	cacheDir, err := platform.CacheDir()
	if err != nil {
		return err
	}
	archivePath := filepath.Join(cacheDir, release.Filename)

	downloadMsg := fmt.Sprintf("Downloading Rust %s...", release.Version)
	if err := cliutil.WithSpinner(downloadMsg, func(update func(int64, int64)) error {
		return downloader.DownloadWithProgress(ctx, release.URL, archivePath, release.SHA256, update)
	}); err != nil {
		return fmt.Errorf("installing Rust %s: %w", name, err)
	}

	if err := cliutil.WithSpinner("Installing...", func(func(int64, int64)) error {
		return extractRust(archivePath, destDir)
	}); err != nil {
		return fmt.Errorf("installing Rust %s: %w", name, err)
	}

	if err := filesystem.WriteFileAtomic(releaseMarker, []byte(release.Version), 0o644); err != nil {
		return fmt.Errorf("recording installed release for Rust %s: %w", name, err)
	}

	cliutil.Success("Rust %s installed", name)
	return nil
}

// Uninstall removes versions/rust/<name>. If it was the active version,
// current/rust is cleared too.
func (r *Rust) Uninstall(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "rust", name)

	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Rust is not installed", name)
	}

	current, err := r.CurrentVersion()
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
		markerPath := filepath.Join(currentDir, "rust")
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clearing active version marker: %w", err)
		}

		cliutil.Step("Rust %s was the active version; no version is active now", name)
	}

	cliutil.Success("Rust %s uninstalled", name)
	return nil
}

// Activate makes name the active Rust version by writing it to
// current/rust. name must already be installed.
func (r *Rust) Activate(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "rust", name)
	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Rust is not installed — run `dev lang install rust %s` first", name, name)
	}

	currentDir, err := platform.CurrentDir()
	if err != nil {
		return err
	}
	markerPath := filepath.Join(currentDir, "rust")
	if err := filesystem.WriteFileAtomic(markerPath, []byte(name), 0o644); err != nil {
		return fmt.Errorf("activating Rust %s: %w", name, err)
	}

	cliutil.Success("Rust %s activated", name)
	return nil
}

// binaryPathForOS resolves binName's path for a given rustOS() value.
// The merged toolchain is flat bin/ on every platform; Windows
// binaries carry a .exe suffix.
func binaryPathForOS(osName, versionDir, binName string) string {
	if osName == "windows" {
		return filepath.Join(versionDir, "bin", binName+".exe")
	}
	return filepath.Join(versionDir, "bin", binName)
}

// BinaryPath resolves the absolute path to binName inside an installed
// Rust version at versionDir, using this host's own OS layout.
func (r *Rust) BinaryPath(versionDir, binName string) (string, error) {
	osName, err := rustOS()
	if err != nil {
		return "", err
	}

	candidate := binaryPathForOS(osName, versionDir, binName)
	if !filesystem.Exists(candidate) {
		return "", fmt.Errorf("%s not found in Rust installation at %s", binName, versionDir)
	}
	return candidate, nil
}

// BinDir returns the directory whose contents should be exposed on PATH
// for an active Rust version — always bin/.
func (r *Rust) BinDir(versionDir string) (string, error) {
	return filepath.Join(versionDir, "bin"), nil
}
