# Java Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Java language provider (Eclipse Temurin/Adoptium) so `dev lang install java <major>`, `dev lang use java <major>`, and the `java`/`javac` shims work exactly the way the existing Node.js provider's equivalents already do.

**Architecture:** A new `internal/runtime/java` package implements `devruntime.Runtime`, mirroring `internal/runtime/node`'s structure closely (same `baseURL`-override-for-tests pattern, same local-directory-listing helpers, same atomic-extraction helper). It differs from Node in three real ways: (1) Temurin's API returns the exact release name, download link, and SHA256 checksum in one HTTP call, so there is no second checksum-file fetch; (2) OS/arch map to Temurin's own vocabulary (`linux`/`mac`/`windows`, `x64`/`aarch64`); (3) `BinaryPath` has three branches instead of two, because macOS Temurin archives nest an extra `Contents/Home/` directory that Linux and Windows don't have. The provider is registered in `internal/providers.Register` alongside Node — `cmd/lang.go` and `internal/shim` need zero changes, since both already consume the `Runtime`/`Manager` abstraction generically.

**Tech Stack:** Go stdlib only (`net/http`, `encoding/json`, `archive/tar`, `archive/zip`, `compress/gzip`) plus this project's own `internal/installer`, `internal/downloader`, `internal/filesystem`, `internal/platform`, `internal/cliutil` packages — no new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-28-java-provider-design.md`

## Global Constraints

- Production `baseURL` defaults to `https://api.adoptium.net` (spec §"Real API research").
- Temurin `os` query values: `linux`, `mac`, `windows`. `architecture` query values: `x64`, `aarch64`. Always `image_type=jdk` (JDK only — `javac` is a required shim, and JRE-only installs are a stated non-goal).
- Checksum verification uses the SHA256 hex string embedded directly in the `/v3/assets/latest/.../hotspot` response's `binary.package.checksum` field — never a second HTTP call.
- Extraction reuses `internal/installer.ExtractAtomic` (dispatches `.zip` vs `.tar.gz` by the archive's own filename extension — Temurin's `binary.package.name` already carries the correct one, so no `archiveExtension`-style helper is needed for Java) plus the same strip-single-top-level-directory, `installer.ReconcileStaleBackup`-guarded atomic rename-swap pattern `internal/runtime/node`'s `extractStrippingTopLevel` already established.
- `BinaryPath` has three branches: Linux `bin/<name>`, Windows `bin\<name>.exe`, macOS `Contents/Home/bin/<name>`.
- `ShimNames()` returns exactly `["java", "javac"]`.
- Registration is the only change outside `internal/runtime/java`: one line in `internal/providers.Register`. `cmd/lang.go` and `internal/shim` must not be touched.
- Every commit ends with `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.

## Review Focus

- **macOS `Contents/Home` `BinaryPath` nesting.** If this branch is wrong (or untested because CI runs on Linux), the `java`/`javac` shims silently fail to resolve on macOS specifically, with no Linux-CI signal at all. Task 5 isolates the OS-dispatch logic into a plain `binaryPathForOS(osName, versionDir, binName) string` helper so its `mac` branch can be asserted directly, independent of the host running the tests (`platform.OS()` wraps `runtime.GOOS` with no override hook, so a `BinaryPath`-only test would only ever exercise the host's own branch).
- **A path-injection-shaped `release_name`.** Adoptium's API is a real external service; a malformed or compromised response's `release_name` flows into `destDir`'s `.dev-release` marker file. Task 3's tests include a fixture with `release_name` containing `../` and confirm `resolveRelease` rejects it via `validJavaReleaseName` before that value is ever used.
- **A major version with no matching release** (unsupported major, or a real major with no build for this OS/arch pair). `Install` must fail cleanly without creating `versions/java/<name>` — mirrors the established Node behavior for the same situation. Task 3's tests cover both an empty JSON array and an HTTP 404 response from `resolveRelease`'s endpoint.
- **Offline install of an already-installed major.** When the network fetch to Adoptium fails outright but a `.dev-release` marker already exists for that major, `Install` must report the cached release and succeed rather than hard-failing a machine that's merely offline — this is an established pattern in this codebase (`internal/runtime/node`'s `Install`), not new risk. Task 3 mirrors Node's `TestInstall_OfflineFallbackUsesCachedReleaseWhenIndexFetchFails`.
- **Leftover temp-extraction/backup directories in `ListInstalledVersions`.** A killed-mid-install process can leave `.tmp-java-extract-*` or `<name>.old` directories under `versions/java/`; these must never be reported as installed versions. Task 2 mirrors Node's already-fixed `TestListInstalledVersions_SkipsHiddenAndBackupDirs`.

---

### Task 1: Package scaffold, OS/arch mapping, release-name validation, `ListRemoteVersions`

**Files:**
- Create: `internal/runtime/java/java.go`
- Test: `internal/runtime/java/java_test.go`

**Interfaces:**
- Consumes: `platform.OS()`, `platform.Arch()` (both `func() string`, `internal/platform`); `devruntime.Version{Name string; LTS bool}` and `devruntime.Runtime` (`internal/runtime`).
- Produces: `type Java struct { baseURL string }`; `func New() *Java`; `func (j *Java) Name() string`; `func javaOS() (string, error)`; `func javaArch() (string, error)`; `func validJavaReleaseName(name string) error`; `func (j *Java) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error)`. Later tasks call `javaOS()`/`javaArch()`/`validJavaReleaseName` directly and construct `&Java{baseURL: ...}` for tests.

- [ ] **Step 1: Write the failing tests**

Create `internal/runtime/java/java_test.go`:

```go
package java

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJavaOS_ReturnsANonEmptyMapping(t *testing.T) {
	t.Parallel()
	got, err := javaOS()
	if err != nil {
		t.Fatalf("javaOS() returned error on this test's platform: %v", err)
	}
	if got == "" {
		t.Error("javaOS() returned empty string")
	}
}

func TestJavaArch_ReturnsANonEmptyMapping(t *testing.T) {
	t.Parallel()
	got, err := javaArch()
	if err != nil {
		t.Fatalf("javaArch() returned error on this test's platform: %v", err)
	}
	if got == "" {
		t.Error("javaArch() returned empty string")
	}
}

func TestValidJavaReleaseName_RejectsEmptyDotDotAndSeparators(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", ".", "..", "a/b", "a\\b", "../escape", "jdk-21.0.12.1+1/../../evil"} {
		if err := validJavaReleaseName(name); err == nil {
			t.Errorf("validJavaReleaseName(%q) returned nil error, want an error", name)
		}
	}
}

func TestValidJavaReleaseName_AcceptsRealTemurinFormats(t *testing.T) {
	t.Parallel()
	// jdk-X.Y.Z+B is the 9+ format; jdk8u<update>-b<build> is the
	// distinct format Java 8 uses.
	for _, name := range []string{"jdk-21.0.12.1+1", "jdk8u432-b06"} {
		if err := validJavaReleaseName(name); err != nil {
			t.Errorf("validJavaReleaseName(%q) returned error: %v, want nil", name, err)
		}
	}
}

type fixtureAvailableReleases struct {
	AvailableReleases    []int `json:"available_releases"`
	AvailableLTSReleases []int `json:"available_lts_releases"`
}

func TestListRemoteVersions_ReturnsOneEntryPerMajorNewestFirst(t *testing.T) {
	t.Parallel()
	fixture := fixtureAvailableReleases{
		AvailableReleases:    []int{8, 11, 17, 21, 25},
		AvailableLTSReleases: []int{8, 11, 17, 21, 25},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(fixture); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	j := &Java{baseURL: server.URL}

	versions, err := j.ListRemoteVersions(context.Background())
	if err != nil {
		t.Fatalf("ListRemoteVersions() returned error: %v", err)
	}
	if len(versions) != 5 {
		t.Fatalf("ListRemoteVersions() returned %d versions, want 5: %+v", len(versions), versions)
	}
	if versions[0].Name != "25" {
		t.Errorf("ListRemoteVersions()[0].Name = %q, want %q (newest major first)", versions[0].Name, "25")
	}
	if versions[len(versions)-1].Name != "8" {
		t.Errorf("ListRemoteVersions()[last].Name = %q, want %q (oldest major last)", versions[len(versions)-1].Name, "8")
	}
}

func TestListRemoteVersions_MarksOnlyLTSMajorsAsLTS(t *testing.T) {
	t.Parallel()
	fixture := fixtureAvailableReleases{
		AvailableReleases:    []int{18, 21},
		AvailableLTSReleases: []int{21},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(fixture); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	j := &Java{baseURL: server.URL}

	versions, err := j.ListRemoteVersions(context.Background())
	if err != nil {
		t.Fatalf("ListRemoteVersions() returned error: %v", err)
	}
	for _, v := range versions {
		wantLTS := v.Name == "21"
		if v.LTS != wantLTS {
			t.Errorf("version %q LTS = %v, want %v", v.Name, v.LTS, wantLTS)
		}
	}
}

func TestListRemoteVersions_ServerErrorReturnsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	j := &Java{baseURL: server.URL}

	_, err := j.ListRemoteVersions(context.Background())
	if err == nil {
		t.Fatal("ListRemoteVersions() returned nil error for a 500 response")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/java/... -v`
Expected: FAIL — package `java` doesn't exist yet (`no Go files in ...` or `undefined: Java` once the package file exists but before the symbols are defined).

- [ ] **Step 3: Write the implementation**

Create `internal/runtime/java/java.go`:

```go
// Package java implements the dev Runtime interface for Java, using
// Eclipse Temurin (Adoptium) as the distribution source.
package java

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	devruntime "github.com/jpsdm/dev/internal/runtime"
	"github.com/jpsdm/dev/internal/platform"
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
	osName := platform.OS()
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
	archName := platform.Arch()
	switch archName {
	case "amd64":
		return "x64", nil
	case "arm64":
		return "aarch64", nil
	default:
		return "", fmt.Errorf("unsupported architecture: %s", archName)
	}
}

// validJavaReleaseName reports an error if name could escape a
// destination directory when used as a path component, or when
// written into the .dev-release marker file: empty, ".", "..", or
// containing a path separator. Temurin's release_name format differs
// across majors (9+ uses "jdk-X.Y.Z+B"; 8 uses
// "jdk8u<update>-b<build>"), so this validates path-safety rather
// than an exact version-string shape.
func validJavaReleaseName(name string) error {
	if name == "" {
		return fmt.Errorf("empty release name")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid release name: %q", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid release name: %q", name)
	}
	return nil
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/java/... -v`
Expected: PASS (all tests in Step 1).

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/runtime/java/... && gofmt -l internal/runtime/java/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/java/java.go internal/runtime/java/java_test.go
git commit -m "$(cat <<'EOF'
Add Java provider scaffold: OS/arch mapping and ListRemoteVersions

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `ListInstalledVersions` and `CurrentVersion`

**Files:**
- Modify: `internal/runtime/java/java.go`
- Test: `internal/runtime/java/java_test.go`

**Interfaces:**
- Consumes: `platform.VersionsDir() (string, error)`, `platform.CurrentDir() (string, error)` (`internal/platform`); `devruntime.Version` (Task 1).
- Produces: `func (j *Java) ListInstalledVersions() ([]devruntime.Version, error)`; `func (j *Java) CurrentVersion() (*devruntime.Version, error)`. Task 3's `Install`/offline-fallback and Task 4's `Uninstall` call `CurrentVersion()`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/runtime/java/java_test.go`:

```go
func TestListInstalledVersions_EmptyWhenNoneInstalled(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	j := New()

	versions, err := j.ListInstalledVersions()
	if err != nil {
		t.Fatalf("ListInstalledVersions() returned error: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("ListInstalledVersions() = %+v, want empty", versions)
	}
}

func TestListInstalledVersions_ListsInstalledDirs(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "java", "21"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "java", "17"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	j := New()

	versions, err := j.ListInstalledVersions()
	if err != nil {
		t.Fatalf("ListInstalledVersions() returned error: %v", err)
	}
	got := map[string]bool{}
	for _, v := range versions {
		got[v.Name] = true
	}
	if !got["21"] || !got["17"] || len(got) != 2 {
		t.Errorf("ListInstalledVersions() = %+v, want exactly {17, 21}", versions)
	}
}

func TestListInstalledVersions_SkipsHiddenAndBackupDirs(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, name := range []string{"21", ".tmp-java-extract-abc123", "17.old"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "java", name), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	j := New()

	versions, err := j.ListInstalledVersions()
	if err != nil {
		t.Fatalf("ListInstalledVersions() returned error: %v", err)
	}
	if len(versions) != 1 || versions[0].Name != "21" {
		t.Errorf("ListInstalledVersions() = %+v, want only {21} (leftover temp/backup dirs filtered out)", versions)
	}
}

func TestCurrentVersion_NilWhenNoneActive(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	j := New()

	got, err := j.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got != nil {
		t.Errorf("CurrentVersion() = %+v, want nil", got)
	}
}

func TestCurrentVersion_ReadsMarkerFile(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "current"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devHome, "current", "java"), []byte("21"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	j := New()

	got, err := j.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got == nil || got.Name != "21" {
		t.Errorf(`CurrentVersion() = %+v, want Name="21"`, got)
	}
}
```

Add these imports to the test file's `import` block: `"os"`, `"path/filepath"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/java/... -v -run 'TestListInstalledVersions|TestCurrentVersion'`
Expected: FAIL with `undefined: (*Java).ListInstalledVersions` / `undefined: (*Java).CurrentVersion`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/java/java.go`. Extend the import block with `"os"`, `"path/filepath"`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/java/... -v`
Expected: PASS (all tests so far).

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/runtime/java/... && gofmt -l internal/runtime/java/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/java/java.go internal/runtime/java/java_test.go
git commit -m "$(cat <<'EOF'
Add Java provider ListInstalledVersions and CurrentVersion

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `resolveRelease` and `Install`

**Files:**
- Modify: `internal/runtime/java/java.go`
- Test: `internal/runtime/java/java_test.go`

**Interfaces:**
- Consumes: `javaOS()`, `javaArch()`, `validJavaReleaseName()` (Task 1); `platform.VersionsDir()`, `platform.CacheDir()` (`internal/platform`); `downloader.Download(ctx context.Context, url, destPath, wantSHA256 string) error` (`internal/downloader`); `installer.ExtractAtomic(archivePath, destDir string) error`, `installer.ReconcileStaleBackup(destDir, backupDir string) error` (`internal/installer`); `filesystem.EnsureDir(path string, perm os.FileMode) error`, `filesystem.WriteFileAtomic(path string, data []byte, perm os.FileMode) error` (`internal/filesystem`); `cliutil.Success(format string, args ...any)`, `cliutil.Step(format string, args ...any)` (`internal/cliutil`).
- Produces: `type javaRelease struct { Name, URL, Filename, SHA256 string }`; `func (j *Java) resolveRelease(ctx context.Context, name string) (javaRelease, error)`; `func extractStrippingTopLevel(archivePath, destDir string) error`; `func (j *Java) Install(ctx context.Context, name string) error`. Task 4's `Uninstall` looks for the same `versions/java/<name>` layout `Install` creates; the `.dev-release` marker filename (`.dev-release`) is not consumed elsewhere in this codebase but must match the name used by the offline-fallback test.

- [ ] **Step 1: Write the failing tests**

Append to `internal/runtime/java/java_test.go`. Add these imports to the file's `import` block: `"archive/tar"`, `"archive/zip"`, `"bytes"`, `"compress/gzip"`, `"crypto/sha256"`, `"encoding/hex"`, `"fmt"`, `"strings"`, `"github.com/jpsdm/dev/internal/cliutil"`.

```go
func TestResolveRelease_ParsesReleaseNameURLAndChecksum(t *testing.T) {
	t.Parallel()
	osName, err := javaOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	archName, err := javaArch()
	if err != nil {
		t.Skipf("unsupported arch for this test: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/v3/assets/latest/21/hotspot"
		if r.URL.Path != wantPath {
			t.Errorf("request path = %q, want %q", r.URL.Path, wantPath)
		}
		if got := r.URL.Query().Get("os"); got != osName {
			t.Errorf("os query = %q, want %q", got, osName)
		}
		if got := r.URL.Query().Get("architecture"); got != archName {
			t.Errorf("architecture query = %q, want %q", got, archName)
		}
		if got := r.URL.Query().Get("image_type"); got != "jdk" {
			t.Errorf("image_type query = %q, want %q", got, "jdk")
		}
		fmt.Fprint(w, `[{
			"release_name": "jdk-21.0.12.1+1",
			"binary": {"package": {
				"name": "OpenJDK21U-jdk_x64_linux_hotspot_21.0.12.1_1.tar.gz",
				"link": "https://example.invalid/OpenJDK21U-jdk_x64_linux_hotspot_21.0.12.1_1.tar.gz",
				"checksum": "f9d6e191deadbeef"
			}}
		}]`)
	}))
	defer server.Close()
	j := &Java{baseURL: server.URL}

	release, err := j.resolveRelease(context.Background(), "21")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if release.Name != "jdk-21.0.12.1+1" {
		t.Errorf("release.Name = %q, want %q", release.Name, "jdk-21.0.12.1+1")
	}
	if release.Filename != "OpenJDK21U-jdk_x64_linux_hotspot_21.0.12.1_1.tar.gz" {
		t.Errorf("release.Filename = %q, want the package name from the response", release.Filename)
	}
	if release.SHA256 != "f9d6e191deadbeef" {
		t.Errorf("release.SHA256 = %q, want the checksum from the response", release.SHA256)
	}
	if release.URL != "https://example.invalid/OpenJDK21U-jdk_x64_linux_hotspot_21.0.12.1_1.tar.gz" {
		t.Errorf("release.URL = %q, want the link from the response", release.URL)
	}
}

func TestResolveRelease_EmptyArrayReturnsEmptyReleaseNoError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	j := &Java{baseURL: server.URL}

	release, err := j.resolveRelease(context.Background(), "999")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if release.Name != "" {
		t.Errorf("resolveRelease() = %+v, want a zero-value release for an empty array response", release)
	}
}

func TestResolveRelease_404ReturnsEmptyReleaseNoError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	j := &Java{baseURL: server.URL}

	release, err := j.resolveRelease(context.Background(), "999")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if release.Name != "" {
		t.Errorf("resolveRelease() = %+v, want a zero-value release for a 404 response", release)
	}
}

func TestResolveRelease_RejectsPathInjectionShapedReleaseName(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A malformed release_name (as if a compromised/misbehaving
		// server appended path segments) must never be trusted for
		// building a filesystem path.
		fmt.Fprint(w, `[{
			"release_name": "jdk-21.0.12.1+1/../../evil",
			"binary": {"package": {
				"name": "evil.tar.gz",
				"link": "https://example.invalid/evil.tar.gz",
				"checksum": "deadbeef"
			}}
		}]`)
	}))
	defer server.Close()
	j := &Java{baseURL: server.URL}

	_, err := j.resolveRelease(context.Background(), "21")
	if err == nil {
		t.Fatal("resolveRelease() returned nil error for a path-injection-shaped release_name")
	}
}

func buildFixtureArchive(t *testing.T, ext, topLevelDir string, files map[string]string) []byte {
	t.Helper()
	prefixed := make(map[string]string, len(files))
	for name, content := range files {
		prefixed[topLevelDir+"/"+name] = content
	}

	if ext == ".zip" {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, content := range prefixed {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatalf("creating zip entry: %v", err)
			}
			if _, err := w.Write([]byte(content)); err != nil {
				t.Fatalf("writing zip content: %v", err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("closing zip writer: %v", err)
		}
		return buf.Bytes()
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range prefixed {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("writing tar content: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	return buf.Bytes()
}

func TestInstall_DownloadsVerifiesAndExtracts(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)

	osName, err := javaOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	archName, err := javaArch()
	if err != nil {
		t.Skipf("unsupported arch for this test: %v", err)
	}
	ext := ".tar.gz"
	if osName == "windows" {
		ext = ".zip"
	}
	filename := "OpenJDK21U-jdk_" + archName + "_" + osName + "_hotspot_21.0.12.1_1" + ext
	archiveBytes := buildFixtureArchive(t, ext, "jdk-21.0.12.1+1", map[string]string{
		"bin/java": "fake java binary",
	})
	sum := sha256.Sum256(archiveBytes)
	checksum := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/v3/assets/latest/21/hotspot", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{
			"release_name": "jdk-21.0.12.1+1",
			"binary": {"package": {
				"name": %q,
				"link": %q,
				"checksum": %q
			}}
		}]`, filename, server.URL+"/download/"+filename, checksum)
	})
	mux.HandleFunc("/download/"+filename, func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write(archiveBytes); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})

	j := &Java{baseURL: server.URL}
	if err := j.Install(context.Background(), "21"); err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(devHome, "versions", "java", "21", "bin", "java"))
	if err != nil {
		t.Fatalf("reading installed binary: %v", err)
	}
	if string(got) != "fake java binary" {
		t.Errorf("installed binary content = %q, want %q", got, "fake java binary")
	}
}

func TestInstall_AlreadyInstalledSkipsNetworkRedownload(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)

	osName, _ := javaOS()
	archName, _ := javaArch()
	ext := ".tar.gz"
	if osName == "windows" {
		ext = ".zip"
	}
	filename := "OpenJDK21U-jdk_" + archName + "_" + osName + "_hotspot_21.0.12.1_1" + ext
	archiveBytes := buildFixtureArchive(t, ext, "jdk-21.0.12.1+1", map[string]string{"bin/java": "v1"})
	sum := sha256.Sum256(archiveBytes)
	checksum := hex.EncodeToString(sum[:])

	downloadCount := 0
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/v3/assets/latest/21/hotspot", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{
			"release_name": "jdk-21.0.12.1+1",
			"binary": {"package": {
				"name": %q,
				"link": %q,
				"checksum": %q
			}}
		}]`, filename, server.URL+"/download/"+filename, checksum)
	})
	mux.HandleFunc("/download/"+filename, func(w http.ResponseWriter, r *http.Request) {
		downloadCount++
		if _, err := w.Write(archiveBytes); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})

	j := &Java{baseURL: server.URL}
	if err := j.Install(context.Background(), "21"); err != nil {
		t.Fatalf("first Install() returned error: %v", err)
	}
	if err := j.Install(context.Background(), "21"); err != nil {
		t.Fatalf("second Install() returned error: %v", err)
	}

	if downloadCount != 1 {
		t.Errorf("archive downloaded %d times, want exactly 1 (second Install should skip the network)", downloadCount)
	}
}

func TestInstall_OfflineFallbackUsesCachedReleaseWhenResolveFails(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	destDir := filepath.Join(devHome, "versions", "java", "21")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, ".dev-release"), []byte("jdk-21.0.12.1+1"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	origStdout := cliutil.Stdout
	var buf bytes.Buffer
	cliutil.Stdout = &buf
	defer func() { cliutil.Stdout = origStdout }()

	j := &Java{baseURL: server.URL}
	if err := j.Install(context.Background(), "21"); err != nil {
		t.Fatalf("Install() returned error despite an offline fallback being available: %v", err)
	}
	if !strings.Contains(buf.String(), "jdk-21.0.12.1+1") {
		t.Errorf("Install() output = %q, want it to mention the already-installed release", buf.String())
	}
}

func TestInstall_NoOfflineFallbackWhenNothingCached(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	j := &Java{baseURL: server.URL}
	if err := j.Install(context.Background(), "21"); err == nil {
		t.Fatal("Install() returned nil error despite no cached release and a failing resolve")
	}
}

func TestInstall_UnknownVersionFailsWithoutCreatingDirectory(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()

	j := &Java{baseURL: server.URL}
	err := j.Install(context.Background(), "999")
	if err == nil {
		t.Fatal("Install() returned nil error for a nonexistent major version")
	}

	if _, statErr := os.Stat(filepath.Join(devHome, "versions", "java", "999")); !os.IsNotExist(statErr) {
		t.Error("Install() created a directory for a version that failed to resolve")
	}
}

func buildFlatTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("writing tar content: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	return buf.Bytes()
}

func TestExtractStrippingTopLevel_ReplacesExistingDestDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	destDir := filepath.Join(dir, "dest")

	archive1 := filepath.Join(dir, "v1.tar.gz")
	if err := os.WriteFile(archive1, buildFixtureArchive(t, ".tar.gz", "jdk-1", map[string]string{"bin/java": "v1"}), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := extractStrippingTopLevel(archive1, destDir); err != nil {
		t.Fatalf("extractStrippingTopLevel() first call returned error: %v", err)
	}

	archive2 := filepath.Join(dir, "v2.tar.gz")
	if err := os.WriteFile(archive2, buildFixtureArchive(t, ".tar.gz", "jdk-2", map[string]string{"bin/java": "v2"}), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := extractStrippingTopLevel(archive2, destDir); err != nil {
		t.Fatalf("extractStrippingTopLevel() second call returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "bin", "java"))
	if err != nil {
		t.Fatalf("reading replaced binary: %v", err)
	}
	if string(got) != "v2" {
		t.Errorf("binary content = %q, want %q (destDir should have been replaced)", got, "v2")
	}
	if _, err := os.Stat(destDir + ".old"); !os.IsNotExist(err) {
		t.Error("backup directory left behind after a successful replace")
	}
}

func TestExtractStrippingTopLevel_UnexpectedLayoutLeavesDestDirUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	destDir := filepath.Join(dir, "dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "keep.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Two top-level entries instead of the single directory Temurin
	// archives always contain.
	badArchive := filepath.Join(dir, "bad.tar.gz")
	if err := os.WriteFile(badArchive, buildFlatTarGz(t, map[string]string{
		"one/file.txt": "a",
		"two/file.txt": "b",
	}), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := extractStrippingTopLevel(badArchive, destDir)
	if err == nil {
		t.Fatal("extractStrippingTopLevel() returned nil error for a two-top-level-entry archive")
	}
	got, readErr := os.ReadFile(filepath.Join(destDir, "keep.txt"))
	if readErr != nil {
		t.Fatalf("destDir was modified despite the error: %v", readErr)
	}
	if string(got) != "keep me" {
		t.Errorf("destDir content = %q, want the original %q (must be untouched on failure)", got, "keep me")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/java/... -v -run 'TestResolveRelease|TestInstall|TestExtractStrippingTopLevel'`
Expected: FAIL with `undefined: (*Java).resolveRelease` / `undefined: (*Java).Install` / `undefined: extractStrippingTopLevel`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/java/java.go`. Extend the import block with `"os"` (already present from Task 2), `"path/filepath"` (already present), plus new imports `"github.com/jpsdm/dev/internal/cliutil"`, `"github.com/jpsdm/dev/internal/downloader"`, `"github.com/jpsdm/dev/internal/filesystem"`, `"github.com/jpsdm/dev/internal/installer"`:

```go
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
	if err := validJavaReleaseName(asset.ReleaseName); err != nil {
		return javaRelease{}, fmt.Errorf("resolving Java %s: %w", name, err)
	}
	// asset.Binary.Package.Name flows into archivePath via filepath.Join
	// in Install — it comes from the same untrusted API response as
	// ReleaseName and needs the same path-safety check before Install
	// ever uses it to build a filesystem path.
	if err := validJavaReleaseName(asset.Binary.Package.Name); err != nil {
		return javaRelease{}, fmt.Errorf("resolving Java %s: %w", name, err)
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
		return fmt.Errorf("no Java release found for %q", name)
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

	cliutil.Step("Downloading Java %s...", release.Name)
	if err := downloader.Download(ctx, release.URL, archivePath, release.SHA256); err != nil {
		return fmt.Errorf("installing Java %s: %w", name, err)
	}

	cliutil.Step("Installing...")
	if err := extractStrippingTopLevel(archivePath, destDir); err != nil {
		return fmt.Errorf("installing Java %s: %w", name, err)
	}

	if err := filesystem.WriteFileAtomic(releaseMarker, []byte(release.Name), 0o644); err != nil {
		return fmt.Errorf("recording installed release for Java %s: %w", name, err)
	}

	cliutil.Success("Java %s installed", name)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/java/... -v`
Expected: PASS (all tests so far).

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/runtime/java/... && gofmt -l internal/runtime/java/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/java/java.go internal/runtime/java/java_test.go
git commit -m "$(cat <<'EOF'
Add Java provider resolveRelease and Install

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `Uninstall` and `Activate`

**Files:**
- Modify: `internal/runtime/java/java.go`
- Test: `internal/runtime/java/java_test.go`

**Interfaces:**
- Consumes: `platform.VersionsDir()`, `platform.CurrentDir()`; `filesystem.Exists(path string) bool`, `filesystem.WriteFileAtomic` (Task 3); `j.CurrentVersion()` (Task 2); `cliutil.Success`, `cliutil.Step` (Task 3).
- Produces: `func (j *Java) Uninstall(name string) error`; `func (j *Java) Activate(name string) error`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/runtime/java/java_test.go`. Add `"github.com/jpsdm/dev/internal/filesystem"` to the import block.

```go
func TestUninstall_NotInstalledReturnsError(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	j := New()

	err := j.Uninstall("21")
	if err == nil {
		t.Fatal("Uninstall() returned nil error for a version that was never installed")
	}
	if !strings.Contains(err.Error(), "Java") {
		t.Errorf("Uninstall() error = %q, want it to preserve the \"Java\" brand name", err)
	}
}

func TestUninstall_RemovesDirectory(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	dir := filepath.Join(devHome, "versions", "java", "21")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	j := New()

	if err := j.Uninstall("21"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}
	if filesystem.Exists(dir) {
		t.Error("Uninstall() did not remove the version directory")
	}
}

func TestUninstall_LeavesOtherActiveVersionMarkerAlone(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "java", "21"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "java", "17"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "current"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devHome, "current", "java"), []byte("17"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	j := New()

	if err := j.Uninstall("21"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	got, err := j.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got == nil || got.Name != "17" {
		t.Errorf("CurrentVersion() = %+v after uninstalling a non-active version, want Name=\"17\" untouched", got)
	}
}

func TestUninstall_ClearsActiveMarkerIfCurrentlyActive(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "java", "21"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "current"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devHome, "current", "java"), []byte("21"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	j := New()

	if err := j.Uninstall("21"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	got, err := j.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got != nil {
		t.Errorf("CurrentVersion() = %+v after uninstalling the active version, want nil", got)
	}
}

func TestActivate_NotInstalledReturnsError(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	j := New()

	err := j.Activate("21")
	if err == nil {
		t.Fatal("Activate() returned nil error for a version that isn't installed")
	}
	if !strings.Contains(err.Error(), "Java") {
		t.Errorf("Activate() error = %q, want it to preserve the \"Java\" brand name", err)
	}

	current, err := j.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if current != nil {
		t.Error("Activate() on a missing version left a current-version marker behind")
	}
}

func TestActivate_WritesMarkerFile(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "java", "21"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	j := New()

	if err := j.Activate("21"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	got, err := j.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got == nil || got.Name != "21" {
		t.Errorf(`CurrentVersion() = %+v, want Name="21"`, got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/java/... -v -run 'TestUninstall|TestActivate'`
Expected: FAIL with `undefined: (*Java).Uninstall` / `undefined: (*Java).Activate`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/java/java.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/java/... -v`
Expected: PASS (all tests so far).

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/runtime/java/... && gofmt -l internal/runtime/java/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/java/java.go internal/runtime/java/java_test.go
git commit -m "$(cat <<'EOF'
Add Java provider Uninstall and Activate

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `ShimNames` and `BinaryPath` (three-way OS layout)

**Files:**
- Modify: `internal/runtime/java/java.go`
- Test: `internal/runtime/java/java_test.go`

**Interfaces:**
- Consumes: `javaOS()` (Task 1); `filesystem.Exists` (Task 3).
- Produces: `func (j *Java) ShimNames() []string`; `func binaryPathForOS(osName, versionDir, binName string) string`; `func (j *Java) BinaryPath(versionDir, binName string) (string, error)`. This completes the `devruntime.Runtime` interface — the compile-time assertion in Step 3 below fails to build until every method exists.

- [ ] **Step 1: Write the failing tests**

Append to `internal/runtime/java/java_test.go`. Add `"github.com/jpsdm/dev/internal/runtime"` (aliased `devruntime`, matching Task 1's import) — it's already imported; no new import needed for this step besides what's already present.

```go
var _ devruntime.Runtime = (*Java)(nil)

func TestShimNames(t *testing.T) {
	t.Parallel()
	j := New()
	names := j.ShimNames()
	want := map[string]bool{"java": true, "javac": true}
	if len(names) != len(want) {
		t.Fatalf("ShimNames() = %v, want exactly %v", names, want)
	}
	for _, name := range names {
		if !want[name] {
			t.Errorf("ShimNames() included unexpected %q", name)
		}
	}
}

func TestBinaryPathForOS_Linux(t *testing.T) {
	t.Parallel()
	got := binaryPathForOS("linux", "/opt/java/21", "java")
	want := filepath.Join("/opt/java/21", "bin", "java")
	if got != want {
		t.Errorf(`binaryPathForOS("linux", ...) = %q, want %q`, got, want)
	}
}

func TestBinaryPathForOS_Windows(t *testing.T) {
	t.Parallel()
	got := binaryPathForOS("windows", "C:\\java\\21", "java")
	want := filepath.Join("C:\\java\\21", "bin", "java.exe")
	if got != want {
		t.Errorf(`binaryPathForOS("windows", ...) = %q, want %q`, got, want)
	}
}

func TestBinaryPathForOS_MacNestsContentsHome(t *testing.T) {
	t.Parallel()
	// The one real macOS-specific quirk: Temurin's macOS archives nest
	// an extra Contents/Home/ level that Linux and Windows don't have.
	// This is tested via the OS-parameterized helper directly (not
	// through BinaryPath's javaOS() dispatch) because platform.OS()
	// wraps runtime.GOOS with no override hook — a BinaryPath-only test
	// would only ever exercise the host running the test suite, which
	// is Linux in this project's CI.
	got := binaryPathForOS("mac", "/opt/java/21", "java")
	want := filepath.Join("/opt/java/21", "Contents", "Home", "bin", "java")
	if got != want {
		t.Errorf(`binaryPathForOS("mac", ...) = %q, want %q`, got, want)
	}
}

func TestBinaryPath_UsesHostLayoutAndChecksExistence(t *testing.T) {
	dir := t.TempDir()
	osName, err := javaOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	binPath := binaryPathForOS(osName, dir, "java")
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(binPath, []byte("fake"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	j := New()
	got, err := j.BinaryPath(dir, "java")
	if err != nil {
		t.Fatalf("BinaryPath() returned error: %v", err)
	}
	if got != binPath {
		t.Errorf("BinaryPath() = %q, want %q", got, binPath)
	}
}

func TestBinaryPath_MissingBinaryReturnsError(t *testing.T) {
	dir := t.TempDir()
	j := New()

	_, err := j.BinaryPath(dir, "java")
	if err == nil {
		t.Fatal("BinaryPath() returned nil error for a binary that doesn't exist")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/java/... -v -run 'TestShimNames|TestBinaryPath'`
Expected: FAIL — `undefined: (*Java).ShimNames`, `undefined: binaryPathForOS`, `undefined: (*Java).BinaryPath`, plus the package-level `var _ devruntime.Runtime = (*Java)(nil)` failing to compile.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/java/java.go`:

```go
// ShimNames returns the Java binaries dev creates shims for.
func (j *Java) ShimNames() []string {
	return []string{"java", "javac"}
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/java/... -v`
Expected: PASS (every test in the package).

- [ ] **Step 5: Run `go vet`, `gofmt`, and the full package build**

Run: `go vet ./internal/runtime/java/... && gofmt -l internal/runtime/java/ && go build ./...`
Expected: no output from `go vet`/`gofmt`; `go build ./...` succeeds.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/java/java.go internal/runtime/java/java_test.go
git commit -m "$(cat <<'EOF'
Add Java provider ShimNames and BinaryPath

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Register the Java provider and update the README

**Files:**
- Modify: `internal/providers/providers.go`
- Modify: `internal/providers/providers_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `java.New() *java.Java` (Task 1); `runtime.Manager.Register(r Runtime)`, `runtime.Manager.Get(name string) (Runtime, bool)` (`internal/runtime`, pre-existing).
- Produces: nothing new consumed by later tasks — this is the final integration task.

- [ ] **Step 1: Write the failing test**

Read the current `internal/providers/providers_test.go` first (shown in full in this plan's research — reproduced here for the exact starting content):

```go
package providers

import (
	"testing"

	"github.com/jpsdm/dev/internal/runtime"
)

func TestRegister_RegistersNode(t *testing.T) {
	t.Parallel()
	m := runtime.NewManager()
	Register(m)

	r, ok := m.Get("node")
	if !ok {
		t.Fatal(`Get("node") ok = false after Register(), want true`)
	}
	if r.Name() != "node" {
		t.Errorf(`Get("node").Name() = %q, want "node"`, r.Name())
	}
}
```

Add this test to the same file, after `TestRegister_RegistersNode`:

```go
func TestRegister_RegistersJava(t *testing.T) {
	t.Parallel()
	m := runtime.NewManager()
	Register(m)

	r, ok := m.Get("java")
	if !ok {
		t.Fatal(`Get("java") ok = false after Register(), want true`)
	}
	if r.Name() != "java" {
		t.Errorf(`Get("java").Name() = %q, want "java"`, r.Name())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/providers/... -v -run TestRegister_RegistersJava`
Expected: FAIL — `Get("java") ok = false after Register(), want true`.

- [ ] **Step 3: Register the Java provider**

Modify `internal/providers/providers.go` to:

```go
// Package providers is the single place that lists every Runtime
// provider dev knows about, so command wiring (cmd/lang.go) and shim
// dispatch (internal/shim) can never register a different set.
package providers

import (
	"github.com/jpsdm/dev/internal/runtime"
	"github.com/jpsdm/dev/internal/runtime/java"
	"github.com/jpsdm/dev/internal/runtime/node"
)

// Register adds every known Runtime provider to m.
func Register(m *runtime.Manager) {
	m.Register(node.New())
	m.Register(java.New())
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/providers/... -v`
Expected: PASS (both `TestRegister_RegistersNode` and `TestRegister_RegistersJava`).

- [ ] **Step 5: Update the README**

In `README.md`, change:

```markdown
Currently supported languages: Node.js.
```

to:

```markdown
Currently supported languages: Node.js, Java (Eclipse Temurin).
```

And change the language-management example block:

```markdown
    dev lang list                  # newest version of every supported language
    dev lang list node             # all available Node.js major versions
    dev lang install node 22
    dev lang use node 22
    dev lang current               # active version of every language
    dev lang installed
    dev lang uninstall node 22
```

to:

```markdown
    dev lang list                  # newest version of every supported language
    dev lang list node             # all available Node.js major versions
    dev lang install node 22
    dev lang use node 22
    dev lang install java 21
    dev lang use java 21
    dev lang current               # active version of every language
    dev lang installed
    dev lang uninstall node 22
```

And change the PATH-and-shims section's last sentence:

```markdown
After `dev setup`, `~/.dev/bin` on `PATH` is enough to run whatever
version of a managed language is currently active — no per-language PATH
entries needed. Running `node`, `npm`, or `npx` transparently runs the
version most recently activated with `dev lang use`.
```

to:

```markdown
After `dev setup`, `~/.dev/bin` on `PATH` is enough to run whatever
version of a managed language is currently active — no per-language PATH
entries needed. Running `node`, `npm`, `npx`, `java`, or `javac`
transparently runs the version most recently activated with `dev lang use`.
```

- [ ] **Step 6: Run the full test suite and build**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: `go build`/`go vet` produce no errors; `gofmt -l .` produces no output; every package's tests pass, including `internal/runtime/java`, `internal/providers`, `cmd` (the `lang`/`env`/`setup` commands are generic over the registry, so they pick up `java` automatically — this run is what confirms Global Constraint "`cmd/lang.go` and `internal/shim` need zero changes" actually holds).

- [ ] **Step 7: Commit**

```bash
git add internal/providers/providers.go internal/providers/providers_test.go README.md
git commit -m "$(cat <<'EOF'
Register the Java provider and document it in the README

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Post-plan manual smoke test (not a task — run once after Task 6, on whatever platform is available)

```bash
go build -o /tmp/dev-smoke .
DEV_HOME=$(mktemp -d) /tmp/dev-smoke lang list java
DEV_HOME=$(mktemp -d) /tmp/dev-smoke lang install java 21
```

Confirms the real Adoptium API round-trips end-to-end (not just the httptest fixtures) before considering the feature done. This step needs live network access and is not part of the automated task loop — run it manually, or skip it if this environment has no outbound network access, and note the skip in the final report.
