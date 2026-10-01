// Package node implements the dev Runtime interface for Node.js.
package node

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

const distBaseURL = "https://nodejs.org/dist"

// Node implements devruntime.Runtime for Node.js.
type Node struct {
	baseURL string // overridable in tests; defaults to distBaseURL
}

// New returns a production Node provider pointed at the real nodejs.org.
func New() *Node {
	return &Node{baseURL: distBaseURL}
}

func (n *Node) Name() string { return "node" }

type indexEntry struct {
	Version string      `json:"version"` // e.g. "v22.11.0"
	LTS     interface{} `json:"lts"`     // false, or a codename string
	Files   []string    `json:"files"`   // e.g. ["linux-x64", "osx-arm64-tar", ...]
}

// filesKey returns the identifier nodejs.org's release index uses in
// its "files" array for this platform/arch — a different naming
// scheme from the actual download filenames (verified live against
// https://nodejs.org/dist/index.json): Linux entries are bare
// "<os>-<arch>", while macOS and Windows entries carry an explicit
// archive-type suffix.
func filesKey(osName, archName string) (string, error) {
	switch osName {
	case "linux":
		return fmt.Sprintf("linux-%s", archName), nil
	case "darwin":
		return fmt.Sprintf("osx-%s-tar", archName), nil
	case "win":
		return fmt.Sprintf("win-%s-zip", archName), nil
	default:
		return "", fmt.Errorf("unsupported operating system: %s", osName)
	}
}

func hasFile(files []string, key string) bool {
	for _, f := range files {
		if f == key {
			return true
		}
	}
	return false
}

// majorVersionRE both extracts a release's major-version number and,
// critically, validates that the release-index-supplied version string
// is exactly "vX.Y.Z" with nothing appended. resolveRelease returns this
// same string unmodified as exactVersion, which later flows straight
// into a filesystem path (archivePath, destDir's release marker) — a
// server response with trailing path segments (e.g.
// "v22.11.0/../../evil") must not silently match.
var majorVersionRE = regexp.MustCompile(`^v(\d+)\.\d+\.\d+$`)

// fetchIndex retrieves and parses nodejs.org's release index.
func (n *Node) fetchIndex(ctx context.Context) ([]indexEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.baseURL+"/index.json", nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching Node.js release index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching Node.js release index: unexpected status %s", resp.Status)
	}

	var entries []indexEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("parsing Node.js release index: %w", err)
	}
	return entries, nil
}

// ListRemoteVersions fetches nodejs.org's release index and returns the
// newest release for each major version line, newest major first.
func (n *Node) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error) {
	entries, err := n.fetchIndex(ctx)
	if err != nil {
		return nil, err
	}

	osName, err := nodeOS()
	if err != nil {
		return nil, err
	}
	archName, err := nodeArch()
	if err != nil {
		return nil, err
	}
	key, err := filesKey(osName, archName)
	if err != nil {
		return nil, err
	}

	newestByMajor := make(map[string]devruntime.Version)
	for _, e := range entries {
		if !hasFile(e.Files, key) {
			continue
		}
		m := majorVersionRE.FindStringSubmatch(e.Version)
		if m == nil {
			continue
		}
		major := m[1]
		if _, seen := newestByMajor[major]; seen {
			continue // index is newest-first; first occurrence per major wins
		}
		lts, _ := e.LTS.(string)
		newestByMajor[major] = devruntime.Version{Name: major, LTS: lts != ""}
	}

	versions := make([]devruntime.Version, 0, len(newestByMajor))
	for _, v := range newestByMajor {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool {
		vi, _ := strconv.Atoi(versions[i].Name)
		vj, _ := strconv.Atoi(versions[j].Name)
		return vi > vj
	})
	return versions, nil
}

// nodeOS maps this project's platform.OS() to Node.js's release
// filename convention.
func nodeOS() (string, error) {
	osName := platform.OS()
	switch osName {
	case "linux":
		return "linux", nil
	case "darwin":
		return "darwin", nil
	case "windows":
		return "win", nil
	default:
		return "", fmt.Errorf("unsupported operating system: %s", osName)
	}
}

// nodeArch maps this project's platform.Arch() to Node.js's release
// filename convention.
func nodeArch() (string, error) {
	archName := platform.Arch()
	switch archName {
	case "amd64":
		return "x64", nil
	case "arm64":
		return "arm64", nil
	case "386":
		return "x86", nil
	default:
		return "", fmt.Errorf("unsupported architecture: %s", archName)
	}
}

// archiveExtension returns the file extension Node.js publishes for
// the given nodeOS() value: linux and darwin both publish .tar.gz
// (verified live against nodejs.org/dist — no XZ decoder needed), and
// win publishes .zip.
func archiveExtension(os string) string {
	if os == "win" {
		return ".zip"
	}
	return ".tar.gz"
}

// ListInstalledVersions returns one Version per subdirectory of
// versions/node/. LTS is always false here — this is a local
// directory listing, not a network lookup, so LTS status is unknown
// (not "not LTS"); only ListRemoteVersions reports real LTS status.
func (n *Node) ListInstalledVersions() ([]devruntime.Version, error) {
	dir, err := platform.VersionsDir()
	if err != nil {
		return nil, err
	}
	nodeDir := filepath.Join(dir, "node")

	entries, err := os.ReadDir(nodeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", nodeDir, err)
	}

	versions := make([]devruntime.Version, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip leftover temp-extraction (".tmp-node-extract-*") and
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

// CurrentVersion reads current/node. A missing file means no active
// version, not an error. Like ListInstalledVersions, this is a local
// read, so the returned Version.LTS is always false (unknown).
func (n *Node) CurrentVersion() (*devruntime.Version, error) {
	dir, err := platform.CurrentDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "node")

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

// resolveRelease finds the index entry whose major version matches
// name, returning its exact "vX.Y.Z" version string and LTS status.
// An empty exactVersion (with a nil error) means no match was found.
func (n *Node) resolveRelease(ctx context.Context, name string) (exactVersion string, lts bool, err error) {
	entries, err := n.fetchIndex(ctx)
	if err != nil {
		return "", false, err
	}
	for _, e := range entries {
		m := majorVersionRE.FindStringSubmatch(e.Version)
		if m != nil && m[1] == name {
			l, _ := e.LTS.(string)
			return e.Version, l != "", nil
		}
	}
	return "", false, nil
}

// fetchChecksum retrieves releaseURL/SHASUMS256.txt and returns the
// sha256 checksum listed for filename.
func (n *Node) fetchChecksum(ctx context.Context, releaseURL, filename string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL+"/SHASUMS256.txt", nil)
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
// the single top-level directory Node.js tarballs/zips always contain
// (e.g. "node-v22.11.0-linux-x64/") so destDir/bin/node exists
// directly. destDir is only ever replaced once extraction and the
// layout check both succeed, preserving ExtractAtomic's atomicity.
func extractStrippingTopLevel(archivePath, destDir string) error {
	parent := filepath.Dir(destDir)
	// os.MkdirTemp does not create parent directories, and on a fresh
	// DEV_HOME versions/node/ does not exist yet before the first install.
	if err := filesystem.EnsureDir(parent, 0o755); err != nil {
		return err
	}
	tmpParent, err := os.MkdirTemp(parent, ".tmp-node-extract-*")
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

	// Swap destDir for innerDir the same way installer.ExtractAtomic
	// swaps its own tmpDir: rename the old directory aside first, so
	// the only point-of-no-return is a single os.Rename, and the
	// previous install survives (at backupDir) if anything after that
	// still fails.
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

// Install resolves name (e.g. "22") to the newest matching release,
// downloads and checksum-verifies it, and extracts it to
// versions/node/<name>. Re-running Install with the same name is a
// no-op (no network call) if the exact same release is already
// installed there; if a newer patch has shipped, it replaces the
// existing install.
func (n *Node) Install(ctx context.Context, name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "node", name)
	releaseMarker := filepath.Join(destDir, ".dev-release")

	exactVersion, _, err := n.resolveRelease(ctx, name)
	if err != nil {
		// Major-line granularity means even the "already installed, no
		// re-download" path normally still asks the index for the
		// newest patch. If that ask fails outright (offline, DNS,
		// nodejs.org down) but this major version has a release marker
		// from a previous successful install, report that installed
		// release and succeed rather than treating "can't check for
		// updates" as a hard failure.
		if installed, readErr := os.ReadFile(releaseMarker); readErr == nil {
			cachedVersion := strings.TrimSpace(string(installed))
			cliutil.Success("Node.js %s is already installed (%s) — could not check for updates: %s", name, cachedVersion, err)
			return nil
		}
		return err
	}
	if exactVersion == "" {
		return fmt.Errorf("no Node.js release found for %q", name)
	}

	if installed, err := os.ReadFile(releaseMarker); err == nil && strings.TrimSpace(string(installed)) == exactVersion {
		cliutil.Success("Node.js %s is already installed (%s)", name, exactVersion)
		return nil
	}

	osName, err := nodeOS()
	if err != nil {
		return err
	}
	archName, err := nodeArch()
	if err != nil {
		return err
	}
	ext := archiveExtension(osName)
	filename := fmt.Sprintf("node-%s-%s-%s%s", exactVersion, osName, archName, ext)
	releaseURL := fmt.Sprintf("%s/%s", n.baseURL, exactVersion)

	checksum, err := n.fetchChecksum(ctx, releaseURL, filename)
	if err != nil {
		return fmt.Errorf("installing Node.js %s: %w", name, err)
	}

	cacheDir, err := platform.CacheDir()
	if err != nil {
		return err
	}
	archivePath := filepath.Join(cacheDir, filename)

	downloadURL := releaseURL + "/" + filename
	downloadMsg := fmt.Sprintf("Downloading Node.js %s...", exactVersion)
	if err := cliutil.WithSpinner(downloadMsg, func(update func(int64, int64)) error {
		return downloader.DownloadWithProgress(ctx, downloadURL, archivePath, checksum, update)
	}); err != nil {
		return fmt.Errorf("installing Node.js %s: %w", name, err)
	}

	if err := cliutil.WithSpinner("Installing...", func(func(int64, int64)) error {
		return extractStrippingTopLevel(archivePath, destDir)
	}); err != nil {
		return fmt.Errorf("installing Node.js %s: %w", name, err)
	}

	if err := filesystem.WriteFileAtomic(releaseMarker, []byte(exactVersion), 0o644); err != nil {
		return fmt.Errorf("recording installed release for Node.js %s: %w", name, err)
	}

	cliutil.Success("Node.js %s installed", name)
	return nil
}

// Uninstall removes versions/node/<name>. If it was the active
// version, current/node is cleared too, since a marker pointing at a
// removed version is worse than no active version.
func (n *Node) Uninstall(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "node", name)

	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Node.js is not installed", name)
	}

	current, err := n.CurrentVersion()
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
		markerPath := filepath.Join(currentDir, "node")
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clearing active version marker: %w", err)
		}

		cliutil.Step("Node.js %s was the active version; no version is active now", name)
	}

	cliutil.Success("Node.js %s uninstalled", name)
	return nil
}

// Activate makes name the active Node.js version by writing it to
// current/node. name must already be installed.
func (n *Node) Activate(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "node", name)
	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Node.js is not installed — run `dev lang install node %s` first", name, name)
	}

	currentDir, err := platform.CurrentDir()
	if err != nil {
		return err
	}
	markerPath := filepath.Join(currentDir, "node")
	if err := filesystem.WriteFileAtomic(markerPath, []byte(name), 0o644); err != nil {
		return fmt.Errorf("activating Node.js %s: %w", name, err)
	}

	cliutil.Success("Node.js %s activated", name)
	return nil
}

// BinaryPath resolves binName inside an installed Node.js version.
// Node's Unix tarballs put binaries in bin/; its Windows zip extracts
// flat with .cmd wrappers at the version root (verified live against
// nodejs.org's real distribution layout).
func (n *Node) BinaryPath(versionDir, binName string) (string, error) {
	osName, err := nodeOS()
	if err != nil {
		return "", err
	}

	var candidate string
	if osName == "win" {
		if binName == "node" {
			candidate = filepath.Join(versionDir, "node.exe")
		} else {
			candidate = filepath.Join(versionDir, binName+".cmd")
		}
	} else {
		candidate = filepath.Join(versionDir, "bin", binName)
	}

	if !filesystem.Exists(candidate) {
		return "", fmt.Errorf("%s not found in Node.js installation at %s", binName, versionDir)
	}
	return candidate, nil
}

// binDirForOS returns the directory Node.js binaries live in for a
// given nodeOS() value. Node's Windows zip extracts flat at the
// version root (there is no bin/ subdirectory there); Linux/macOS
// tarballs use a real bin/ subdirectory. Kept separate from BinDir so
// the Windows branch is directly testable regardless of host.
func binDirForOS(osName, versionDir string) string {
	if osName == "win" {
		return versionDir
	}
	return filepath.Join(versionDir, "bin")
}

// BinDir returns the directory whose contents should be exposed on
// PATH for an active Node.js version.
func (n *Node) BinDir(versionDir string) (string, error) {
	osName, err := nodeOS()
	if err != nil {
		return "", err
	}
	return binDirForOS(osName, versionDir), nil
}
