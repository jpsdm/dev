// Package python implements the dev Runtime interface for Python, using
// python-build-standalone (astral-sh) as the distribution source —
// python.org itself ships no portable prebuilt binaries for arbitrary
// platforms, unlike Node.js/nodejs.org or Java/Adoptium.
package python

import (
	"bufio"
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

const pbsBaseURL = "https://api.github.com/repos/astral-sh/python-build-standalone"

// Python implements devruntime.Runtime for Python, via
// python-build-standalone.
type Python struct {
	baseURL string // overridable in tests; defaults to pbsBaseURL
}

// New returns a production Python provider pointed at the real GitHub
// releases API.
func New() *Python {
	return &Python{baseURL: pbsBaseURL}
}

func (p *Python) Name() string { return "python" }

// pythonOS maps this project's platform.OS() through mapPythonOS.
func pythonOS() (string, error) {
	return mapPythonOS(platform.OS())
}

// mapPythonOS validates osName is one of the three platforms this
// project supports. Unlike Node/Java, python-build-standalone's own
// vocabulary for the OS itself matches platform.OS() exactly ("linux",
// "darwin", "windows") — only the arch and the full triple need
// translation — but this is still split into a pure function, separate
// from platform.OS() (which has no override hook), so the
// unsupported-value branch is directly testable regardless of host.
func mapPythonOS(osName string) (string, error) {
	switch osName {
	case "linux", "darwin", "windows":
		return osName, nil
	default:
		return "", fmt.Errorf("unsupported operating system: %s", osName)
	}
}

// pythonArch maps this project's platform.Arch() through mapPythonArch.
func pythonArch() (string, error) {
	return mapPythonArch(platform.Arch())
}

// mapPythonArch maps this project's arch identifiers to
// python-build-standalone's vocabulary — "x86_64"/"aarch64", not
// "amd64"/"arm64" (verified live against the real release assets).
func mapPythonArch(archName string) (string, error) {
	switch archName {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "aarch64", nil
	default:
		return "", fmt.Errorf("unsupported architecture: %s", archName)
	}
}

// pythonOSTripleComponent maps an OS name (as mapPythonOS returns it)
// to python-build-standalone's OS/vendor/abi triple segment — verified
// live against the real release assets: linux uses glibc ("gnu", never
// "musl"), darwin is Apple's own vendor segment, windows always targets
// MSVC.
func pythonOSTripleComponent(osName string) (string, error) {
	switch osName {
	case "linux":
		return "unknown-linux-gnu", nil
	case "darwin":
		return "apple-darwin", nil
	case "windows":
		return "pc-windows-msvc", nil
	default:
		return "", fmt.Errorf("unsupported operating system: %s", osName)
	}
}

// pythonTriple returns this host's full python-build-standalone asset
// triple, e.g. "x86_64-unknown-linux-gnu" or "aarch64-apple-darwin".
func pythonTriple() (string, error) {
	osName, err := pythonOS()
	if err != nil {
		return "", err
	}
	archName, err := pythonArch()
	if err != nil {
		return "", err
	}
	osTriple, err := pythonOSTripleComponent(osName)
	if err != nil {
		return "", err
	}
	return archName + "-" + osTriple, nil
}

// assetPattern anchors a python-build-standalone install_only asset
// filename for a specific triple: "cpython-X.Y.Z+<tag>-<triple>-
// install_only.tar.gz", nothing before or after. Anchoring (not a
// substring/Contains check) is what keeps "x86_64-..." from matching an
// "x86_64_v2-..." asset, and keeps "_stripped"/"_full" variants from
// matching at all.
func assetPattern(triple string) *regexp.Regexp {
	return regexp.MustCompile(`^cpython-(\d+\.\d+\.\d+)\+\d+-` + regexp.QuoteMeta(triple) + `-install_only\.tar\.gz$`)
}

// matchAsset reports whether assetName is a plain install_only CPython
// build for triple, returning its exact "X.Y.Z" version if so. The
// anchored pattern doubles as a path-safety check on its own capture —
// a filename with an embedded "/" or ".." cannot match this pattern at
// all — but callers still run devruntime.ValidVersionName on both the
// full asset name and the extracted version before using either in a
// filesystem path, per this project's standing rule of validating every
// externally-sourced path component, not just the one field a reviewer
// happened to check first.
func matchAsset(assetName, triple string) (fullVersion string, ok bool) {
	m := assetPattern(triple).FindStringSubmatch(assetName)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// minorOf returns "X.Y" from a "X.Y.Z" full version string.
func minorOf(fullVersion string) string {
	parts := strings.SplitN(fullVersion, ".", 3)
	return parts[0] + "." + parts[1]
}

// compareMinors compares two "X.Y" minor-version strings numerically,
// component by component — plain string comparison would sort "3.10"
// before "3.9", which is wrong. Returns <0, 0, or >0 like strings.Compare.
func compareMinors(a, b string) int {
	pa := strings.SplitN(a, ".", 2)
	pb := strings.SplitN(b, ".", 2)
	aMajor, _ := strconv.Atoi(pa[0])
	bMajor, _ := strconv.Atoi(pb[0])
	if aMajor != bMajor {
		return aMajor - bMajor
	}
	aMinor, _ := strconv.Atoi(pa[1])
	bMinor, _ := strconv.Atoi(pb[1])
	return aMinor - bMinor
}

// patchOf returns the numeric patch component from a "X.Y.Z" full
// version string, so resolveRelease can pick the greatest patch among
// several assets that match the same requested minor instead of
// trusting the release JSON's own asset ordering.
func patchOf(fullVersion string) (int, bool) {
	parts := strings.SplitN(fullVersion, ".", 3)
	if len(parts) != 3 {
		return 0, false
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, false
	}
	return patch, true
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type release struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

// fetchLatestRelease retrieves and parses python-build-standalone's
// newest GitHub release — one call returns every asset for every
// supported CPython minor and platform combination embedded inline (no
// pagination needed for a single release; verified live).
func (p *Python) fetchLatestRelease(ctx context.Context) (release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/releases/latest", nil)
	if err != nil {
		return release{}, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return release{}, fmt.Errorf("fetching python-build-standalone release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return release{}, fmt.Errorf("fetching python-build-standalone release: unexpected status %s", resp.Status)
	}

	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return release{}, fmt.Errorf("parsing python-build-standalone release: %w", err)
	}
	return rel, nil
}

// ListRemoteVersions fetches the latest python-build-standalone release
// and returns one Version per distinct "X.Y" minor that has an
// install_only build for this host's platform, newest first. LTS is
// always false — Python has no LTS concept.
func (p *Python) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error) {
	rel, err := p.fetchLatestRelease(ctx)
	if err != nil {
		return nil, err
	}

	triple, err := pythonTriple()
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var minors []string
	for _, a := range rel.Assets {
		fullVersion, ok := matchAsset(a.Name, triple)
		if !ok {
			continue
		}
		minor := minorOf(fullVersion)
		if seen[minor] {
			continue
		}
		seen[minor] = true
		minors = append(minors, minor)
	}

	sort.Slice(minors, func(i, j int) bool {
		return compareMinors(minors[i], minors[j]) > 0
	})

	versions := make([]devruntime.Version, 0, len(minors))
	for _, m := range minors {
		versions = append(versions, devruntime.Version{Name: m})
	}
	return versions, nil
}

// ListInstalledVersions returns one Version per subdirectory of
// versions/python/. LTS is always false here — this is a local
// directory listing, not a network lookup.
func (p *Python) ListInstalledVersions() ([]devruntime.Version, error) {
	dir, err := platform.VersionsDir()
	if err != nil {
		return nil, err
	}
	pythonDir := filepath.Join(dir, "python")

	entries, err := os.ReadDir(pythonDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", pythonDir, err)
	}

	versions := make([]devruntime.Version, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip leftover temp-extraction (".tmp-python-extract-*") and
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

// CurrentVersion reads current/python. A missing file means no active
// version, not an error.
func (p *Python) CurrentVersion() (*devruntime.Version, error) {
	dir, err := platform.CurrentDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "python")

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

// resolvedRelease is the resolved, ready-to-download release
// resolveRelease finds for a requested "X.Y" minor.
type resolvedRelease struct {
	FullVersion  string // e.g. "3.12.14" — used as the .dev-release marker content
	AssetName    string // e.g. "cpython-3.12.14+20260929-x86_64-unknown-linux-gnu-install_only.tar.gz"
	DownloadURL  string
	ChecksumsURL string // the same release's SHA256SUMS asset URL
}

// resolveRelease fetches the latest release and finds the install_only
// asset matching both this host's triple and the requested "X.Y" minor,
// picking the greatest patch when more than one asset matches, returning
// its exact version, filename, and download URL together with the
// release's SHA256SUMS URL — one API call serves both Install's download
// step and its checksum-fetch step. A zero-value result (nil error)
// means no matching release was found (an unrecognized minor, or a real
// minor with no build for this OS/arch pair, such as Windows/arm64
// before Python 3.11) — reported by the caller as "no release found",
// not a hard error.
func (p *Python) resolveRelease(ctx context.Context, name string) (resolvedRelease, error) {
	rel, err := p.fetchLatestRelease(ctx)
	if err != nil {
		return resolvedRelease{}, err
	}

	var checksumsURL string
	for _, a := range rel.Assets {
		if a.Name == "SHA256SUMS" {
			checksumsURL = a.BrowserDownloadURL
			break
		}
	}

	triple, err := pythonTriple()
	if err != nil {
		return resolvedRelease{}, err
	}

	var best releaseAsset
	var bestFullVersion string
	bestPatch := -1
	for _, a := range rel.Assets {
		fullVersion, ok := matchAsset(a.Name, triple)
		if !ok || minorOf(fullVersion) != name {
			continue
		}
		patch, ok := patchOf(fullVersion)
		if !ok {
			continue
		}
		if patch > bestPatch {
			bestPatch = patch
			best = a
			bestFullVersion = fullVersion
		}
	}
	if bestPatch == -1 {
		return resolvedRelease{}, nil
	}

	if err := devruntime.ValidVersionName(bestFullVersion); err != nil {
		return resolvedRelease{}, fmt.Errorf("resolving Python %s: %w", name, err)
	}
	// best.Name flows into archivePath via filepath.Join in Install — it
	// comes from the same untrusted API response as bestFullVersion and
	// needs the same path-safety check before Install ever uses it to
	// build a filesystem path.
	if err := devruntime.ValidVersionName(best.Name); err != nil {
		return resolvedRelease{}, fmt.Errorf("resolving Python %s: %w", name, err)
	}
	if best.BrowserDownloadURL == "" || checksumsURL == "" {
		return resolvedRelease{}, fmt.Errorf("resolving Python %s: incomplete release metadata from python-build-standalone", name)
	}

	return resolvedRelease{
		FullVersion:  bestFullVersion,
		AssetName:    best.Name,
		DownloadURL:  best.BrowserDownloadURL,
		ChecksumsURL: checksumsURL,
	}, nil
}

// fetchChecksum retrieves checksumsURL (a release's SHA256SUMS asset)
// and returns the sha256 checksum listed for filename.
func fetchChecksum(ctx context.Context, checksumsURL, filename string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumsURL, nil)
	if err != nil {
		return "", fmt.Errorf("building checksum request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching checksums: unexpected status %s", resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == filename {
			return fields[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading checksums: %w", err)
	}
	return "", fmt.Errorf("no checksum found for %s", filename)
}

// extractStrippingTopLevel extracts archivePath into destDir, removing
// the single top-level "python/" directory every python-build-standalone
// archive contains, on every platform including Windows. destDir is
// only ever replaced once extraction and the layout check both succeed.
func extractStrippingTopLevel(archivePath, destDir string) error {
	parent := filepath.Dir(destDir)
	if err := filesystem.EnsureDir(parent, 0o755); err != nil {
		return err
	}
	tmpParent, err := os.MkdirTemp(parent, ".tmp-python-extract-*")
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

// Install resolves name (e.g. "3.12") to the newest matching
// python-build-standalone release, downloads and checksum-verifies it,
// and extracts it to versions/python/<name>. Re-running Install with the
// same name is a no-op (no network call) if the exact same release is
// already installed there; if a newer patch has shipped, it replaces the
// existing install.
func (p *Python) Install(ctx context.Context, name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "python", name)
	releaseMarker := filepath.Join(destDir, ".dev-release")

	release, err := p.resolveRelease(ctx, name)
	if err != nil {
		// Minor-line granularity means even the "already installed, no
		// re-download" path normally still asks GitHub for the newest
		// patch. If that ask fails outright (offline, DNS, GitHub down)
		// but this minor has a release marker from a previous successful
		// install, report that installed release and succeed rather than
		// treating "can't check for updates" as a hard failure.
		if installed, readErr := os.ReadFile(releaseMarker); readErr == nil {
			cachedVersion := strings.TrimSpace(string(installed))
			cliutil.Success("Python %s is already installed (%s) — could not check for updates: %s", name, cachedVersion, err)
			return nil
		}
		return err
	}
	if release.FullVersion == "" {
		osName, _ := pythonOS()
		archName, _ := pythonArch()
		return fmt.Errorf("no python-build-standalone release of Python %s for %s/%s", name, osName, archName)
	}

	if installed, err := os.ReadFile(releaseMarker); err == nil && strings.TrimSpace(string(installed)) == release.FullVersion {
		cliutil.Success("Python %s is already installed (%s)", name, release.FullVersion)
		return nil
	}

	checksum, err := fetchChecksum(ctx, release.ChecksumsURL, release.AssetName)
	if err != nil {
		return fmt.Errorf("installing Python %s: %w", name, err)
	}

	cacheDir, err := platform.CacheDir()
	if err != nil {
		return err
	}
	archivePath := filepath.Join(cacheDir, release.AssetName)

	downloadMsg := fmt.Sprintf("Downloading Python %s...", release.FullVersion)
	if err := cliutil.WithSpinner(downloadMsg, func(update func(int64, int64)) error {
		return downloader.DownloadWithProgress(ctx, release.DownloadURL, archivePath, checksum, update)
	}); err != nil {
		return fmt.Errorf("installing Python %s: %w", name, err)
	}

	if err := cliutil.WithSpinner("Installing...", func(func(int64, int64)) error {
		return extractStrippingTopLevel(archivePath, destDir)
	}); err != nil {
		return fmt.Errorf("installing Python %s: %w", name, err)
	}

	if err := filesystem.WriteFileAtomic(releaseMarker, []byte(release.FullVersion), 0o644); err != nil {
		return fmt.Errorf("recording installed release for Python %s: %w", name, err)
	}

	cliutil.Success("Python %s installed", name)
	return nil
}

// Uninstall removes versions/python/<name>. If it was the active
// version, current/python is cleared too, since a marker pointing at
// a removed version is worse than no active version.
func (p *Python) Uninstall(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "python", name)

	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Python is not installed", name)
	}

	current, err := p.CurrentVersion()
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
		markerPath := filepath.Join(currentDir, "python")
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clearing active version marker: %w", err)
		}

		cliutil.Step("Python %s was the active version; no version is active now", name)
	}

	cliutil.Success("Python %s uninstalled", name)
	return nil
}

// Activate makes name the active Python version by writing it to
// current/python. name must already be installed.
func (p *Python) Activate(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "python", name)
	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Python is not installed — run `dev lang install python %s` first", name, name)
	}

	currentDir, err := platform.CurrentDir()
	if err != nil {
		return err
	}
	markerPath := filepath.Join(currentDir, "python")
	if err := filesystem.WriteFileAtomic(markerPath, []byte(name), 0o644); err != nil {
		return fmt.Errorf("activating Python %s: %w", name, err)
	}

	cliutil.Success("Python %s activated", name)
	return nil
}

// binaryPathForOS resolves binName's path inside an installed Python
// version for a given pythonOS() value ("linux", "darwin", "windows").
// Unix archives (linux, darwin) put every binary flat under bin/.
// Windows archives put python.exe/pythonw.exe at the version root
// instead of under bin/, and ship no python3.exe at all — both "python"
// and "python3" resolve to the same python.exe. Windows archives also
// ship no pip/pip3 executable at all (verified live: install_only
// Windows builds include the pip module but no prebuilt console-script
// entry point for it), so the Scripts/<name>.exe candidate this
// function builds for a pip binary name legitimately does not exist;
// BinaryPath's existing "not found" check reports that the same way it
// would for any other missing binary — no special-case error text.
// Kept separate from BinaryPath so each OS branch is directly testable
// regardless of the host platform running the tests.
func binaryPathForOS(osName, versionDir, binName string) string {
	switch osName {
	case "windows":
		if binName == "python" || binName == "python3" {
			return filepath.Join(versionDir, "python.exe")
		}
		return filepath.Join(versionDir, "Scripts", binName+".exe")
	default:
		return filepath.Join(versionDir, "bin", binName)
	}
}

// BinaryPath resolves the absolute path to binName inside an installed
// Python version at versionDir, using this host's own OS layout (see
// binaryPathForOS).
func (p *Python) BinaryPath(versionDir, binName string) (string, error) {
	osName, err := pythonOS()
	if err != nil {
		return "", err
	}

	candidate := binaryPathForOS(osName, versionDir, binName)
	if !filesystem.Exists(candidate) {
		return "", fmt.Errorf("%s not found in Python installation at %s", binName, versionDir)
	}
	return candidate, nil
}

// binDirForOS returns the directory Python binaries live in for a
// given pythonOS() value — mirrors binaryPathForOS's split: Windows
// puts binaries at the version root, Unix uses bin/.
func binDirForOS(osName, versionDir string) string {
	if osName == "windows" {
		return versionDir
	}
	return filepath.Join(versionDir, "bin")
}

// BinDir returns the directory whose contents should be exposed on
// PATH for an active Python version.
func (p *Python) BinDir(versionDir string) (string, error) {
	osName, err := pythonOS()
	if err != nil {
		return "", err
	}
	return binDirForOS(osName, versionDir), nil
}
