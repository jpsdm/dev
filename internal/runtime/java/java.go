// Package java implements the dev Runtime interface for Java, using
// Eclipse Temurin (Adoptium) as the distribution source.
package java

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

const adoptiumBaseURL = "https://api.adoptium.net"

// Java implements devruntime.Runtime for Java, via Eclipse Temurin.
type Java struct {
	baseURL string // overridable in tests; defaults to adoptiumBaseURL
}

// New returns a production Java provider pointed at the real Adoptium API.
func New() *Java {
	return &Java{baseURL: adoptiumBaseURL}
}

func (j *Java) Name() string { return "java" }

// javaOS maps this project's platform.OS() to Temurin's API vocabulary.
func javaOS() (string, error) {
	return mapJavaOS(platform.OS())
}

// mapJavaOS is javaOS's pure mapping logic, kept separate so its
// unsupported-value branch can be tested directly with an arbitrary
// input regardless of the host platform actually running the tests —
// platform.OS() has no override hook, the same reason BinaryPath's OS
// dispatch is split out into binaryPathForOS.
func mapJavaOS(osName string) (string, error) {
	switch osName {
	case "linux":
		return "linux", nil
	case "darwin":
		return "mac", nil
	case "windows":
		return "windows", nil
	default:
		return "", fmt.Errorf("unsupported operating system: %s", osName)
	}
}

// javaArch maps this project's platform.Arch() to Temurin's API
// vocabulary — notably "aarch64", not "arm64" (verified live against
// api.adoptium.net for os=mac&architecture=aarch64).
func javaArch() (string, error) {
	return mapJavaArch(platform.Arch())
}

// mapJavaArch is javaArch's pure mapping logic; see mapJavaOS for why
// it's split out.
func mapJavaArch(archName string) (string, error) {
	switch archName {
	case "amd64":
		return "x64", nil
	case "arm64":
		return "aarch64", nil
	default:
		return "", fmt.Errorf("unsupported architecture: %s", archName)
	}
}

// validJavaPathComponent reports an error if value could escape a
// destination directory when used as a path component: empty, ".",
// "..", or containing a path separator. Used for both the release name
// and the download filename Adoptium returns, since Temurin's
// release-name format differs across majors (9+ uses "jdk-X.Y.Z+B";
// 8 uses "jdk8u<update>-b<build>"), so this validates path-safety
// rather than an exact version-string shape.
func validJavaPathComponent(value string) error {
	return devruntime.ValidVersionName(value)
}

type availableReleasesResponse struct {
	AvailableReleases    []int `json:"available_releases"`
	AvailableLTSReleases []int `json:"available_lts_releases"`
}

// fetchAvailableReleases retrieves and parses Adoptium's
// available-releases index.
func (j *Java) fetchAvailableReleases(ctx context.Context) (availableReleasesResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.baseURL+"/v3/info/available_releases", nil)
	if err != nil {
		return availableReleasesResponse{}, fmt.Errorf("building request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return availableReleasesResponse{}, fmt.Errorf("fetching Adoptium available releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return availableReleasesResponse{}, fmt.Errorf("fetching Adoptium available releases: unexpected status %s", resp.Status)
	}

	var info availableReleasesResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return availableReleasesResponse{}, fmt.Errorf("parsing Adoptium available releases: %w", err)
	}
	return info, nil
}

// ListRemoteVersions fetches Adoptium's available-releases index and
// returns one Version per installable major, newest major first.
func (j *Java) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error) {
	info, err := j.fetchAvailableReleases(ctx)
	if err != nil {
		return nil, err
	}

	lts := make(map[int]bool, len(info.AvailableLTSReleases))
	for _, m := range info.AvailableLTSReleases {
		lts[m] = true
	}

	majors := append([]int(nil), info.AvailableReleases...)
	sort.Sort(sort.Reverse(sort.IntSlice(majors)))

	versions := make([]devruntime.Version, 0, len(majors))
	for _, m := range majors {
		versions = append(versions, devruntime.Version{Name: strconv.Itoa(m), LTS: lts[m]})
	}
	return versions, nil
}

// ListInstalledVersions returns one Version per subdirectory of
// versions/java/. LTS is always false here — this is a local
// directory listing, not a network lookup, so LTS status is unknown
// (not "not LTS"); only ListRemoteVersions reports real LTS status.
func (j *Java) ListInstalledVersions() ([]devruntime.Version, error) {
	dir, err := platform.VersionsDir()
	if err != nil {
		return nil, err
	}
	javaDir := filepath.Join(dir, "java")

	entries, err := os.ReadDir(javaDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", javaDir, err)
	}

	versions := make([]devruntime.Version, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip leftover temp-extraction (".tmp-java-extract-*") and
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

// CurrentVersion reads current/java. A missing file means no active
// version, not an error. Like ListInstalledVersions, this is a local
// read, so the returned Version.LTS is always false (unknown).
func (j *Java) CurrentVersion() (*devruntime.Version, error) {
	dir, err := platform.CurrentDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "java")

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

type assetRelease struct {
	ReleaseName string `json:"release_name"`
	Binary      struct {
		Package struct {
			Name     string `json:"name"`
			Link     string `json:"link"`
			Checksum string `json:"checksum"`
		} `json:"package"`
	} `json:"binary"`
}

// javaRelease is the resolved, ready-to-download release resolveRelease
// finds for a requested major version.
type javaRelease struct {
	Name     string // Temurin's release_name, e.g. "jdk-21.0.12.1+1" — used as the .dev-release marker content
	URL      string
	Filename string
	SHA256   string
}

// resolveRelease finds the newest Temurin HotSpot JDK release for the
// requested major version and this platform, returning its exact
// release name, download URL, filename, and checksum together —
// Temurin's API gives us in one request what Node's Install needs two
// separate calls for (index lookup, then a checksums-file fetch). A
// zero-value javaRelease (with a nil error) means no matching release
// was found (an unrecognized major, or a real major with no build for
// this OS/arch pair) — this is reported as "no release found" by the
// caller, not treated as a hard error here.
func (j *Java) resolveRelease(ctx context.Context, name string) (javaRelease, error) {
	osName, err := javaOS()
	if err != nil {
		return javaRelease{}, err
	}
	archName, err := javaArch()
	if err != nil {
		return javaRelease{}, err
	}

	url := fmt.Sprintf("%s/v3/assets/latest/%s/hotspot?architecture=%s&os=%s&image_type=jdk", j.baseURL, name, archName, osName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return javaRelease{}, fmt.Errorf("building request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return javaRelease{}, fmt.Errorf("resolving Java %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return javaRelease{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return javaRelease{}, fmt.Errorf("resolving Java %s: unexpected status %s", name, resp.Status)
	}

	var assets []assetRelease
	if err := json.NewDecoder(resp.Body).Decode(&assets); err != nil {
		return javaRelease{}, fmt.Errorf("parsing Adoptium release for Java %s: %w", name, err)
	}
	if len(assets) == 0 {
		return javaRelease{}, nil
	}
	asset := assets[0]
	if err := validJavaPathComponent(asset.ReleaseName); err != nil {
		return javaRelease{}, fmt.Errorf("resolving Java %s: %w", name, err)
	}
	// asset.Binary.Package.Name flows into archivePath via filepath.Join
	// in Install — it comes from the same untrusted API response as
	// ReleaseName and needs the same path-safety check before Install
	// ever uses it to build a filesystem path.
	if err := validJavaPathComponent(asset.Binary.Package.Name); err != nil {
		return javaRelease{}, fmt.Errorf("resolving Java %s: %w", name, err)
	}
	if asset.Binary.Package.Link == "" || asset.Binary.Package.Checksum == "" {
		return javaRelease{}, fmt.Errorf("resolving Java %s: incomplete release metadata from Adoptium", name)
	}

	return javaRelease{
		Name:     asset.ReleaseName,
		URL:      asset.Binary.Package.Link,
		Filename: asset.Binary.Package.Name,
		SHA256:   asset.Binary.Package.Checksum,
	}, nil
}

// extractStrippingTopLevel extracts archivePath into destDir, removing
// the single top-level directory Temurin tarballs/zips always contain
// (e.g. "jdk-21.0.12.1+1/") so destDir's own layout starts directly at
// bin/ (or, on macOS, Contents/Home/bin/). destDir is only ever
// replaced once extraction and the layout check both succeed,
// preserving ExtractAtomic's atomicity.
func extractStrippingTopLevel(archivePath, destDir string) error {
	parent := filepath.Dir(destDir)
	if err := filesystem.EnsureDir(parent, 0o755); err != nil {
		return err
	}
	tmpParent, err := os.MkdirTemp(parent, ".tmp-java-extract-*")
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

// Install resolves name (e.g. "21") to the newest matching Temurin
// release, downloads and checksum-verifies it, and extracts it to
// versions/java/<name>. Re-running Install with the same name is a
// no-op (no network call) if the exact same release is already
// installed there; if a newer patch has shipped, it replaces the
// existing install.
func (j *Java) Install(ctx context.Context, name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "java", name)
	releaseMarker := filepath.Join(destDir, ".dev-release")

	release, err := j.resolveRelease(ctx, name)
	if err != nil {
		// Major-line granularity means even the "already installed, no
		// re-download" path normally still asks Adoptium for the
		// newest patch. If that ask fails outright (offline, DNS,
		// api.adoptium.net down) but this major version has a release
		// marker from a previous successful install, report that
		// installed release and succeed rather than treating "can't
		// check for updates" as a hard failure.
		if installed, readErr := os.ReadFile(releaseMarker); readErr == nil {
			cachedVersion := strings.TrimSpace(string(installed))
			cliutil.Success("Java %s is already installed (%s) — could not check for updates: %s", name, cachedVersion, err)
			return nil
		}
		return err
	}
	if release.Name == "" {
		osName, _ := javaOS()
		archName, _ := javaArch()
		return fmt.Errorf("no Temurin JDK build of Java %s for %s/%s", name, osName, archName)
	}

	if installed, err := os.ReadFile(releaseMarker); err == nil && strings.TrimSpace(string(installed)) == release.Name {
		cliutil.Success("Java %s is already installed (%s)", name, release.Name)
		return nil
	}

	cacheDir, err := platform.CacheDir()
	if err != nil {
		return err
	}
	archivePath := filepath.Join(cacheDir, release.Filename)

	downloadMsg := fmt.Sprintf("Downloading Java %s...", release.Name)
	if err := cliutil.WithSpinner(downloadMsg, func(update func(int64, int64)) error {
		return downloader.DownloadWithProgress(ctx, release.URL, archivePath, release.SHA256, update)
	}); err != nil {
		return fmt.Errorf("installing Java %s: %w", name, err)
	}

	if err := cliutil.WithSpinner("Installing...", func(func(int64, int64)) error {
		return extractStrippingTopLevel(archivePath, destDir)
	}); err != nil {
		return fmt.Errorf("installing Java %s: %w", name, err)
	}

	if err := filesystem.WriteFileAtomic(releaseMarker, []byte(release.Name), 0o644); err != nil {
		return fmt.Errorf("recording installed release for Java %s: %w", name, err)
	}

	cliutil.Success("Java %s installed", name)
	return nil
}

// Uninstall removes versions/java/<name>. If it was the active
// version, current/java is cleared too, since a marker pointing at a
// removed version is worse than no active version.
func (j *Java) Uninstall(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "java", name)

	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Java is not installed", name)
	}

	current, err := j.CurrentVersion()
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
		markerPath := filepath.Join(currentDir, "java")
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clearing active version marker: %w", err)
		}

		cliutil.Step("Java %s was the active version; no version is active now", name)
	}

	cliutil.Success("Java %s uninstalled", name)
	return nil
}

// Activate makes name the active Java version by writing it to
// current/java. name must already be installed.
func (j *Java) Activate(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "java", name)
	if !filesystem.Exists(destDir) {
		return fmt.Errorf("version %s of Java is not installed — run `dev lang install java %s` first", name, name)
	}

	currentDir, err := platform.CurrentDir()
	if err != nil {
		return err
	}
	markerPath := filepath.Join(currentDir, "java")
	if err := filesystem.WriteFileAtomic(markerPath, []byte(name), 0o644); err != nil {
		return fmt.Errorf("activating Java %s: %w", name, err)
	}

	cliutil.Success("Java %s activated", name)
	return nil
}

// binaryPathForOS resolves binName's path inside an installed Java
// version for a given Temurin OS identifier. Linux and Windows put
// binaries directly under bin/ (Windows binaries carry a .exe
// suffix); macOS Temurin archives nest an extra Contents/Home/ level,
// matching Apple's standard JDK bundle layout — verified against
// Adoptium's own installation docs. Kept separate from BinaryPath so
// the mac branch can be tested directly regardless of the host
// platform running the tests.
func binaryPathForOS(osName, versionDir, binName string) string {
	switch osName {
	case "windows":
		return filepath.Join(versionDir, "bin", binName+".exe")
	case "mac":
		return filepath.Join(versionDir, "Contents", "Home", "bin", binName)
	default:
		return filepath.Join(versionDir, "bin", binName)
	}
}

// BinaryPath resolves the absolute path to binName inside an
// installed Java version at versionDir, using this host's own OS
// layout (see binaryPathForOS).
func (j *Java) BinaryPath(versionDir, binName string) (string, error) {
	osName, err := javaOS()
	if err != nil {
		return "", err
	}

	candidate := binaryPathForOS(osName, versionDir, binName)
	if !filesystem.Exists(candidate) {
		return "", fmt.Errorf("%s not found in Java installation at %s", binName, versionDir)
	}
	return candidate, nil
}

// binDirForOS returns the directory Java binaries live in for a given
// javaOS() value — mirrors binaryPathForOS's OS split: macOS's
// Contents/Home nesting is a directory-level difference, not just a
// per-binary one.
func binDirForOS(osName, versionDir string) string {
	switch osName {
	case "mac":
		return filepath.Join(versionDir, "Contents", "Home", "bin")
	default:
		return filepath.Join(versionDir, "bin")
	}
}

// BinDir returns the directory whose contents should be exposed on
// PATH for an active Java version.
func (j *Java) BinDir(versionDir string) (string, error) {
	osName, err := javaOS()
	if err != nil {
		return "", err
	}
	return binDirForOS(osName, versionDir), nil
}
