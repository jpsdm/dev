// Package golang implements the dev Runtime interface for Go, using
// go.dev/dl's own JSON release index as the distribution source. The
// package is named golang, not go, because go is a reserved word.
package golang

import (
	"context"
	"encoding/json"
	"fmt"
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

const goDevDLBaseURL = "https://go.dev/dl"

// Go implements devruntime.Runtime for the Go toolchain, via go.dev/dl.
type Go struct {
	baseURL string // overridable in tests; defaults to goDevDLBaseURL
}

// New returns a production Go provider pointed at the real go.dev/dl.
func New() *Go {
	return &Go{baseURL: goDevDLBaseURL}
}

func (g *Go) Name() string { return "go" }

// goOS maps this project's platform.OS() to go.dev/dl's vocabulary,
// which is Go's own GOOS values — since the index is Go's own release
// build matrix, this is an identity mapping in practice, not a real
// relabeling like Java's arm64->aarch64. Kept as an explicit function
// for pattern consistency with the Node and Java providers and as a
// defensive whitelist against an unexpected platform.OS() value.
func goOS() (string, error) {
	return mapGoOS(platform.OS())
}

// mapGoOS is goOS's pure mapping logic, kept separate so its
// unsupported-value branch can be exercised directly in tests without
// depending on the host platform.
func mapGoOS(osName string) (string, error) {
	switch osName {
	case "linux", "darwin", "windows":
		return osName, nil
	default:
		return "", fmt.Errorf("unsupported operating system: %s", osName)
	}
}

// goArch maps this project's platform.Arch() to go.dev/dl's vocabulary
// (Go's own GOARCH values) — see goOS for why this is an identity
// mapping rather than a real relabeling.
func goArch() (string, error) {
	return mapGoArch(platform.Arch())
}

// mapGoArch is goArch's pure mapping logic; see mapGoOS for why this
// is split out.
func mapGoArch(archName string) (string, error) {
	switch archName {
	case "amd64", "arm64", "386":
		return archName, nil
	default:
		return "", fmt.Errorf("unsupported architecture: %s", archName)
	}
}

// indexFile is one platform-specific asset within a release, as
// go.dev/dl's JSON index describes it.
type indexFile struct {
	Filename string `json:"filename"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
	Kind     string `json:"kind"` // "archive", "installer", or "source"
}

// indexRelease is one release entry in go.dev/dl's JSON index.
type indexRelease struct {
	Version string      `json:"version"` // e.g. "go1.24.3"
	Stable  bool        `json:"stable"`
	Files   []indexFile `json:"files"`
}

// fetchIndex retrieves and parses go.dev/dl's full release index.
// include=all is required — without it the endpoint returns only the
// most recent releases, not every line dev lang list go should show.
func (g *Go) fetchIndex(ctx context.Context) ([]indexRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+"/?mode=json&include=all", nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching go.dev/dl release index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching go.dev/dl release index: unexpected status %s", resp.Status)
	}

	var releases []indexRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("parsing go.dev/dl release index: %w", err)
	}
	return releases, nil
}

// lineRE matches a Go version string's major.minor "line" and optional
// patch number: "go1.24.3" -> line "1.24", patch "3"; the defensive
// two-component form "go1.24" -> line "1.24", no patch group (implicit
// 0). Anything else (rc/beta suffixes, malformed strings) doesn't
// match at all.
var lineRE = regexp.MustCompile(`^go(\d+\.\d+)(?:\.(\d+))?$`)

// parseLineAndPatch splits version into its line and patch number. ok
// is false when version doesn't match the expected shape at all.
func parseLineAndPatch(version string) (line string, patch int, ok bool) {
	m := lineRE.FindStringSubmatch(version)
	if m == nil {
		return "", 0, false
	}
	if m[2] != "" {
		p, err := strconv.Atoi(m[2])
		if err != nil {
			return "", 0, false
		}
		patch = p
	}
	return m[1], patch, true
}

// hasArchiveFor reports whether files contains an archive-kind entry
// for the given os/arch with a non-empty checksum, returning that
// entry. Some real go.dev/dl release lines (e.g. 1.2, 1.3, 1.4) list
// an archive with no published SHA256 — those must never be treated
// as installable, since there would be nothing to verify the download
// against.
func hasArchiveFor(files []indexFile, osName, archName string) (indexFile, bool) {
	for _, f := range files {
		if f.Kind == "archive" && f.OS == osName && f.Arch == archName && f.SHA256 != "" {
			return f, true
		}
	}
	return indexFile{}, false
}

// splitLine parses a "major.minor" line string into its two integer
// components, defaulting missing parts to 0.
func splitLine(line string) (major, minor int) {
	parts := strings.SplitN(line, ".", 2)
	major, _ = strconv.Atoi(parts[0])
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	return major, minor
}

// compareLines orders two "major.minor" line strings numerically. A
// plain string comparison would incorrectly order "1.9" after "1.24"
// (since '9' > '2' lexically, even though 9 < 24 numerically).
func compareLines(a, b string) int {
	aMajor, aMinor := splitLine(a)
	bMajor, bMinor := splitLine(b)
	if aMajor != bMajor {
		return aMajor - bMajor
	}
	return aMinor - bMinor
}

// ListRemoteVersions fetches go.dev/dl's release index and returns one
// Version per installable line (e.g. "1.24"), newest line first. A
// line is included only if it has at least one stable release with an
// archive for this platform; among a line's stable releases, the
// newest is chosen by explicit numeric patch comparison, never by
// trusting the index's own ordering. LTS is always false — Go has no
// LTS concept.
func (g *Go) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error) {
	osName, err := goOS()
	if err != nil {
		return nil, err
	}
	archName, err := goArch()
	if err != nil {
		return nil, err
	}
	releases, err := g.fetchIndex(ctx)
	if err != nil {
		return nil, err
	}

	bestPatchByLine := make(map[string]int)
	for _, r := range releases {
		if !r.Stable {
			continue
		}
		line, patch, ok := parseLineAndPatch(r.Version)
		if !ok {
			continue
		}
		if _, hasArchive := hasArchiveFor(r.Files, osName, archName); !hasArchive {
			continue
		}
		if current, seen := bestPatchByLine[line]; !seen || patch > current {
			bestPatchByLine[line] = patch
		}
	}

	versions := make([]devruntime.Version, 0, len(bestPatchByLine))
	for line := range bestPatchByLine {
		versions = append(versions, devruntime.Version{Name: line})
	}
	sort.Slice(versions, func(i, j int) bool {
		return compareLines(versions[i].Name, versions[j].Name) > 0
	})
	return versions, nil
}

// ListInstalledVersions returns one Version per subdirectory of
// versions/go/. LTS is always false here — this is a local directory
// listing, not a network lookup, so LTS status is unknown (and Go has
// no LTS concept regardless).
func (g *Go) ListInstalledVersions() ([]devruntime.Version, error) {
	dir, err := platform.VersionsDir()
	if err != nil {
		return nil, err
	}
	goDir := filepath.Join(dir, "go")

	entries, err := os.ReadDir(goDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", goDir, err)
	}

	versions := make([]devruntime.Version, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip leftover temp-extraction (".tmp-go-extract-*") and
		// swap-backup ("<name>.old") directories a killed-mid-install
		// process can leave behind — real versions never start with "."
		// or end in ".old".
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".old") {
			continue
		}
		versions = append(versions, devruntime.Version{Name: name})
	}
	return versions, nil
}

// CurrentVersion reads current/go. A missing file means no active
// version, not an error.
func (g *Go) CurrentVersion() (*devruntime.Version, error) {
	dir, err := platform.CurrentDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "go")

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

// goRelease is the resolved, ready-to-download release resolveRelease
// finds for a requested line.
type goRelease struct {
	Version  string // e.g. "go1.24.3" — used as the .dev-release marker content
	URL      string
	Filename string
	SHA256   string
}

// resolveRelease finds the newest stable Go release on the requested
// line (e.g. "1.24") that has an archive for this platform, returning
// its exact version string, download URL, filename, and checksum
// together — the checksum is embedded directly in the index response,
// so (like Java, unlike Node) this needs exactly one HTTP call. A
// zero-value goRelease (with a nil error) means no matching release was
// found (an unrecognized line, or a real line with no archive for this
// OS/arch) — reported as "no release found" by the caller, not treated
// as a hard error here.
func (g *Go) resolveRelease(ctx context.Context, name string) (goRelease, error) {
	releases, err := g.fetchIndex(ctx)
	if err != nil {
		return goRelease{}, err
	}
	osName, err := goOS()
	if err != nil {
		return goRelease{}, err
	}
	archName, err := goArch()
	if err != nil {
		return goRelease{}, err
	}

	var best indexRelease
	var bestFile indexFile
	bestPatch := -1
	for _, r := range releases {
		if !r.Stable {
			continue
		}
		line, patch, ok := parseLineAndPatch(r.Version)
		if !ok || line != name {
			continue
		}
		file, hasArchive := hasArchiveFor(r.Files, osName, archName)
		if !hasArchive {
			continue
		}
		if patch > bestPatch {
			bestPatch = patch
			best = r
			bestFile = file
		}
	}
	if bestPatch == -1 {
		return goRelease{}, nil
	}

	// best.Version and bestFile.Filename both come from the same
	// untrusted API response and both flow into filesystem paths in
	// Install — validate both before either is used.
	if err := devruntime.ValidVersionName(best.Version); err != nil {
		return goRelease{}, fmt.Errorf("resolving Go %s: %w", name, err)
	}
	if err := devruntime.ValidVersionName(bestFile.Filename); err != nil {
		return goRelease{}, fmt.Errorf("resolving Go %s: %w", name, err)
	}

	return goRelease{
		Version:  best.Version,
		URL:      g.baseURL + "/" + bestFile.Filename,
		Filename: bestFile.Filename,
		SHA256:   bestFile.SHA256,
	}, nil
}

// extractStrippingTopLevel extracts archivePath into destDir, removing
// the single top-level "go/" directory Go tarballs/zips always contain
// so destDir's own layout starts directly at bin/. destDir is only ever
// replaced once extraction and the layout check both succeed,
// preserving ExtractAtomic's atomicity.
func extractStrippingTopLevel(archivePath, destDir string) error {
	parent := filepath.Dir(destDir)
	if err := filesystem.EnsureDir(parent, 0o755); err != nil {
		return err
	}
	tmpParent, err := os.MkdirTemp(parent, ".tmp-go-extract-*")
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
	innerDir := filepath.Join(extractedRoot, entries[0].Name())

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
	if err := os.Rename(innerDir, destDir); err != nil {
		if hadExisting {
			_ = os.Rename(backupDir, destDir) // best-effort restore; nothing more we can do if this also fails
		}
		return fmt.Errorf("moving %s to %s: %w", innerDir, destDir, err)
	}
	if hadExisting {
		os.RemoveAll(backupDir)
	}
	return nil
}

// Install resolves name (e.g. "1.24") to the newest matching stable Go
// release, downloads and checksum-verifies it, and extracts it to
// versions/go/<name>. Re-running Install with the same name is a no-op
// (no network call) if the exact same release is already installed
// there; if a newer patch has shipped, it replaces the existing
// install.
func (g *Go) Install(ctx context.Context, name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "go", name)
	releaseMarker := filepath.Join(destDir, ".dev-release")

	release, err := g.resolveRelease(ctx, name)
	if err != nil {
		// Line granularity means even the "already installed, no
		// re-download" path normally still asks go.dev/dl for the
		// newest patch. If that ask fails outright (offline, DNS,
		// go.dev down) but this line has a release marker from a
		// previous successful install, report that installed release
		// and succeed rather than treating "can't check for updates"
		// as a hard failure.
		if installed, readErr := os.ReadFile(releaseMarker); readErr == nil {
			cachedVersion := strings.TrimSpace(string(installed))
			cliutil.Success("Go %s is already installed (%s) — could not check for updates: %s", name, cachedVersion, err)
			return nil
		}
		return err
	}
	if release.Version == "" {
		osName, _ := goOS()
		archName, _ := goArch()
		return fmt.Errorf("no Go release of line %s for %s/%s", name, osName, archName)
	}

	if installed, err := os.ReadFile(releaseMarker); err == nil && strings.TrimSpace(string(installed)) == release.Version {
		cliutil.Success("Go %s is already installed (%s)", name, release.Version)
		return nil
	}

	cacheDir, err := platform.CacheDir()
	if err != nil {
		return err
	}
	archivePath := filepath.Join(cacheDir, release.Filename)

	downloadMsg := fmt.Sprintf("Downloading Go %s...", strings.TrimPrefix(release.Version, "go"))
	if err := cliutil.WithSpinner(downloadMsg, func(update func(int64, int64)) error {
		return downloader.DownloadWithProgress(ctx, release.URL, archivePath, release.SHA256, update)
	}); err != nil {
		return fmt.Errorf("installing Go %s: %w", name, err)
	}

	if err := cliutil.WithSpinner("Installing...", func(func(int64, int64)) error {
		return extractStrippingTopLevel(archivePath, destDir)
	}); err != nil {
		return fmt.Errorf("installing Go %s: %w", name, err)
	}

	if err := filesystem.WriteFileAtomic(releaseMarker, []byte(release.Version), 0o644); err != nil {
		return fmt.Errorf("recording installed release for Go %s: %w", name, err)
	}

	cliutil.Success("Go %s installed", name)
	return nil
}

// Uninstall removes versions/go/<name>. If it was the active version,
// current/go is cleared too, since a marker pointing at a removed
// version is worse than no active version.
func (g *Go) Uninstall(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "go", name)

	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Go is not installed", name)
	}

	current, err := g.CurrentVersion()
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
		markerPath := filepath.Join(currentDir, "go")
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clearing active version marker: %w", err)
		}

		cliutil.Step("Go %s was the active version; no version is active now", name)
	}

	cliutil.Success("Go %s uninstalled", name)
	return nil
}

// Activate makes name the active Go version by writing it to
// current/go. name must already be installed.
func (g *Go) Activate(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "go", name)
	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Go is not installed — run `dev lang install go %s` first", name, name)
	}

	currentDir, err := platform.CurrentDir()
	if err != nil {
		return err
	}
	markerPath := filepath.Join(currentDir, "go")
	if err := filesystem.WriteFileAtomic(markerPath, []byte(name), 0o644); err != nil {
		return fmt.Errorf("activating Go %s: %w", name, err)
	}

	cliutil.Success("Go %s activated", name)
	return nil
}

// binaryPathForOS resolves binName's path for a given goOS() value. Go
// archives are flat on every platform — no macOS nesting quirk unlike
// Java — so this only has two real branches: Windows binaries carry a
// .exe suffix, everything else doesn't. Kept as its own function for
// pattern consistency and testability, matching Node and Java.
func binaryPathForOS(osName, versionDir, binName string) string {
	if osName == "windows" {
		return filepath.Join(versionDir, "bin", binName+".exe")
	}
	return filepath.Join(versionDir, "bin", binName)
}

// BinaryPath resolves the absolute path to binName inside an installed
// Go version at versionDir, using this host's own OS layout (see
// binaryPathForOS).
func (g *Go) BinaryPath(versionDir, binName string) (string, error) {
	osName, err := goOS()
	if err != nil {
		return "", err
	}

	candidate := binaryPathForOS(osName, versionDir, binName)
	if !filesystem.Exists(candidate) {
		return "", fmt.Errorf("%s not found in Go installation at %s", binName, versionDir)
	}
	return candidate, nil
}

// BinDir returns the directory whose contents should be exposed on
// PATH for an active Go version — always bin/, on every platform (Go
// archives are flat, no OS branching needed, same reasoning as
// BinaryPath's own).
func (g *Go) BinDir(versionDir string) (string, error) {
	return filepath.Join(versionDir, "bin"), nil
}
