# Python Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Python language provider (via python-build-standalone) so `dev lang install python <X.Y>`, `dev lang use python <X.Y>`, and the `python3`/`python`/`pip3`/`pip` shims work exactly the way the existing Node.js/Java/Go providers' equivalents already do.

**Architecture:** A new `internal/runtime/python` package implements `devruntime.Runtime`, mirroring `internal/runtime/node`'s structure closely (same `baseURL`-override-for-tests pattern, same local-directory-listing helpers, same atomic-extraction helper, same "index fetch + separate checksums-file fetch" shape). It differs from Node in four real ways: (1) the "index" is a single GitHub Releases API response (`GET /releases/latest`) whose `assets` array embeds every platform/version combination — there is no platform-specific "files" key, so matching is done by parsing each asset's filename against this host's triple; (2) OS/arch map to python-build-standalone's own LLVM-triple vocabulary (`x86_64-unknown-linux-gnu`, `aarch64-apple-darwin`, `x86_64-pc-windows-msvc`, ...); (3) every asset is `.tar.gz`, even on Windows, so there's no archive-format branch; (4) Windows installs have no `pip`/`pip3` executable and no `python3.exe` (only `python.exe`) — a real, documented platform gap, not a bug to work around. The provider is registered in `internal/providers.Register` alongside the others — `cmd/lang.go` and `internal/shim` need zero changes.

**Tech Stack:** Go stdlib only (`net/http`, `encoding/json`, `regexp`, `bufio`, `archive/tar`, `compress/gzip`) plus this project's own `internal/installer`, `internal/downloader`, `internal/filesystem`, `internal/platform`, `internal/cliutil` packages — no new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-29-python-provider-design.md`

## Global Constraints

- Production `baseURL` defaults to `https://api.github.com/repos/astral-sh/python-build-standalone` (spec §"Real distribution research").
- Only the `install_only` archive variant is used — filenames ending `_stripped.tar.gz` or `_full.tar.gz` (or any other suffix) must never match.
- Platform triple table (spec §"Real distribution research", verified live):

  | `platform.OS()` | `platform.Arch()` | PBS triple |
  |---|---|---|
  | linux | amd64 | `x86_64-unknown-linux-gnu` |
  | linux | arm64 | `aarch64-unknown-linux-gnu` |
  | darwin | amd64 | `x86_64-apple-darwin` |
  | darwin | arm64 | `aarch64-apple-darwin` |
  | windows | amd64 | `x86_64-pc-windows-msvc` |
  | windows | arm64 | `aarch64-pc-windows-msvc` |

  Only `*-unknown-linux-gnu` (never `-musl`), and only baseline `x86_64` (never `_v2`/`_v3`/`_v4`).
- Archive format is always `.tar.gz`, on every platform including Windows — no `archiveExtension`-style branch is needed.
- Checksum verification: fetch the release's single `SHA256SUMS` asset (found by asset **name**, not a constructed URL — its `browser_download_url` comes from the same `/releases/latest` response already fetched) and match the exact asset filename's line, in `<sha256>  <filename>` format — same parsing shape as Node's `SHASUMS256.txt`.
- Version granularity is `major.minor` (e.g. `"3.12"`), resolved to the newest matching `X.Y.Z` patch in the latest release.
- `ShimNames()` returns exactly `["python3", "python", "pip3", "pip"]`.
- Windows layout gap (real, not a defect): the archive ships `python.exe`/`pythonw.exe` at the version root and no `python3.exe` — both `"python"` and `"python3"` resolve to the same `python.exe`. It ships **no** `pip`/`pip3` executable at all — those shim names resolve to a `Scripts/<name>.exe` path that legitimately does not exist there, surfaced through `BinaryPath`'s ordinary "not found" error (no special-case error text).
- Every filename or version string pulled from the release JSON or `SHA256SUMS` is validated with `devruntime.ValidVersionName` before it touches a filesystem path, per this project's standing path-safety rule.
- Registration is the only change outside `internal/runtime/python`: one line in `internal/providers.Register`. `cmd/lang.go` and `internal/shim` must not be touched.
- Every commit ends with `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.

## Review Focus

- **A path-injection-shaped asset filename or version from the release JSON.** GitHub's API is a real external service; a malformed or spoofed response's asset `name` (or the `X.Y.Z` extracted from it) flows into `archivePath` and the `.dev-release` marker. Task 3's tests include a fixture asset name containing `../` and confirm `resolveRelease` rejects it via `devruntime.ValidVersionName` before that value is ever used.
- **A minor with no build for the current OS/arch** — most concretely, Windows/arm64 before Python 3.11 (a real, documented PBS gap). `ListRemoteVersions` must not list it on that host, and `Install` must fail cleanly without creating `versions/python/<name>`. Task 1 and Task 3 both cover this with a fixture release that includes a minor missing this host's triple.
- **`matchAsset` accidentally matching the wrong variant or triple** — e.g. an `_stripped`/`_full` asset, or `x86_64-unknown-linux-gnu` wrongly matching an `x86_64_v2-unknown-linux-gnu` asset via a loose substring check instead of an anchored pattern. Task 1's tests assert both rejections directly against real PBS filename shapes.
- **Leftover temp-extraction/backup directories in `ListInstalledVersions`.** A killed-mid-install process can leave `.tmp-python-extract-*` or `<name>.old` directories under `versions/python/`; these must never be reported as installed versions. Task 2 mirrors Node's/Java's already-fixed `TestListInstalledVersions_SkipsHiddenAndBackupDirs`.
- **Windows `pip`/`pip3` shims failing loudly and differently instead of cleanly.** Since PBS ships no pip executable there, `BinaryPath` must return the same ordinary "not found" error any missing binary produces — not a panic, not an empty path treated as valid, not different wording that would make a `dev`-specific bug indistinguishable from the documented platform gap. Task 5 tests this directly against the Windows branch of `binaryPathForOS`.

---

### Task 1: Package scaffold, OS/arch/triple mapping, asset matching, `ListRemoteVersions`

**Files:**
- Create: `internal/runtime/python/python.go`
- Test: `internal/runtime/python/python_test.go`

**Interfaces:**
- Consumes: `platform.OS()`, `platform.Arch()` (both `func() string`, `internal/platform`); `devruntime.Version{Name string; LTS bool}` and `devruntime.Runtime` (`internal/runtime`).
- Produces: `type Python struct { baseURL string }`; `func New() *Python`; `func (p *Python) Name() string`; `func pythonOS() (string, error)`; `func mapPythonOS(osName string) (string, error)`; `func pythonArch() (string, error)`; `func mapPythonArch(archName string) (string, error)`; `func pythonOSTripleComponent(osName string) (string, error)`; `func pythonTriple() (string, error)`; `func matchAsset(assetName, triple string) (fullVersion string, ok bool)`; `func minorOf(fullVersion string) string`; `func compareMinors(a, b string) int`; `type releaseAsset struct { Name string; BrowserDownloadURL string }`; `type release struct { TagName string; Assets []releaseAsset }`; `func (p *Python) fetchLatestRelease(ctx context.Context) (release, error)`; `func (p *Python) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error)`. Later tasks call `pythonOS()`/`pythonArch()`/`pythonTriple()`/`matchAsset` directly, construct `&Python{baseURL: ...}` for tests, and reuse `release`/`releaseAsset`/`fetchLatestRelease`.

- [ ] **Step 1: Write the failing tests**

Create `internal/runtime/python/python_test.go`:

```go
package python

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMapPythonOS_AcceptsKnownValues(t *testing.T) {
	t.Parallel()
	for _, osName := range []string{"linux", "darwin", "windows"} {
		got, err := mapPythonOS(osName)
		if err != nil {
			t.Errorf("mapPythonOS(%q) returned error: %v", osName, err)
		}
		if got != osName {
			t.Errorf("mapPythonOS(%q) = %q, want %q", osName, got, osName)
		}
	}
}

func TestMapPythonOS_RejectsUnsupported(t *testing.T) {
	t.Parallel()
	if _, err := mapPythonOS("plan9"); err == nil {
		t.Error(`mapPythonOS("plan9") returned nil error, want an error`)
	}
}

func TestMapPythonArch_MapsToPBSVocabulary(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}
	for in, want := range cases {
		got, err := mapPythonArch(in)
		if err != nil {
			t.Errorf("mapPythonArch(%q) returned error: %v", in, err)
		}
		if got != want {
			t.Errorf("mapPythonArch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMapPythonArch_RejectsUnsupported(t *testing.T) {
	t.Parallel()
	if _, err := mapPythonArch("riscv64"); err == nil {
		t.Error(`mapPythonArch("riscv64") returned nil error, want an error`)
	}
}

func TestPythonOSTripleComponent_MatchesRealPBSTriples(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"linux":   "unknown-linux-gnu",
		"darwin":  "apple-darwin",
		"windows": "pc-windows-msvc",
	}
	for in, want := range cases {
		got, err := pythonOSTripleComponent(in)
		if err != nil {
			t.Errorf("pythonOSTripleComponent(%q) returned error: %v", in, err)
		}
		if got != want {
			t.Errorf("pythonOSTripleComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFullTripleTable_MatchesLiveVerifiedValues(t *testing.T) {
	t.Parallel()
	// Table from the spec's "Real distribution research" section,
	// verified live against astral-sh/python-build-standalone's actual
	// release assets before this plan was written.
	cases := []struct{ osName, archName, want string }{
		{"linux", "amd64", "x86_64-unknown-linux-gnu"},
		{"linux", "arm64", "aarch64-unknown-linux-gnu"},
		{"darwin", "amd64", "x86_64-apple-darwin"},
		{"darwin", "arm64", "aarch64-apple-darwin"},
		{"windows", "amd64", "x86_64-pc-windows-msvc"},
		{"windows", "arm64", "aarch64-pc-windows-msvc"},
	}
	for _, c := range cases {
		arch, err := mapPythonArch(c.archName)
		if err != nil {
			t.Fatalf("mapPythonArch(%q) returned error: %v", c.archName, err)
		}
		osTriple, err := pythonOSTripleComponent(c.osName)
		if err != nil {
			t.Fatalf("pythonOSTripleComponent(%q) returned error: %v", c.osName, err)
		}
		got := arch + "-" + osTriple
		if got != c.want {
			t.Errorf("triple for os=%q arch=%q = %q, want %q", c.osName, c.archName, got, c.want)
		}
	}
}

func TestMatchAsset_AcceptsInstallOnly(t *testing.T) {
	t.Parallel()
	fullVersion, ok := matchAsset("cpython-3.12.14+20260929-x86_64-unknown-linux-gnu-install_only.tar.gz", "x86_64-unknown-linux-gnu")
	if !ok {
		t.Fatal("matchAsset() ok = false, want true for a real install_only filename")
	}
	if fullVersion != "3.12.14" {
		t.Errorf("matchAsset() fullVersion = %q, want %q", fullVersion, "3.12.14")
	}
}

func TestMatchAsset_RejectsStrippedAndFullVariants(t *testing.T) {
	t.Parallel()
	names := []string{
		"cpython-3.12.14+20260929-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz",
		"cpython-3.12.14+20260929-x86_64-unknown-linux-gnu-full.tar.gz",
		"cpython-3.12.14+20260929-x86_64-unknown-linux-gnu-debug-full.tar.gz",
	}
	for _, name := range names {
		if _, ok := matchAsset(name, "x86_64-unknown-linux-gnu"); ok {
			t.Errorf("matchAsset(%q) ok = true, want false (not a plain install_only asset)", name)
		}
	}
}

func TestMatchAsset_RejectsWrongTriple(t *testing.T) {
	t.Parallel()
	// x86_64_v2-... must not match a lookup for the x86_64-... baseline
	// triple via a loose substring check.
	name := "cpython-3.12.14+20260929-x86_64_v2-unknown-linux-gnu-install_only.tar.gz"
	if _, ok := matchAsset(name, "x86_64-unknown-linux-gnu"); ok {
		t.Errorf("matchAsset(%q) ok = true, want false (different triple, x86_64_v2 not x86_64)", name)
	}
}

func TestMatchAsset_RejectsPathTraversalShapedName(t *testing.T) {
	t.Parallel()
	name := "cpython-3.12.14+20260929-x86_64-unknown-linux-gnu-install_only.tar.gz/../../evil"
	if _, ok := matchAsset(name, "x86_64-unknown-linux-gnu"); ok {
		t.Errorf("matchAsset(%q) ok = true, want false (must not match a path-traversal-shaped name)", name)
	}
}

func TestMinorOf(t *testing.T) {
	t.Parallel()
	if got := minorOf("3.12.14"); got != "3.12" {
		t.Errorf(`minorOf("3.12.14") = %q, want "3.12"`, got)
	}
}

func TestCompareMinors_NumericNotLexicographic(t *testing.T) {
	t.Parallel()
	// "3.10" must sort after "3.9" — plain string comparison gets this
	// wrong ("3.10" < "3.9" lexicographically).
	if compareMinors("3.10", "3.9") <= 0 {
		t.Error(`compareMinors("3.10", "3.9") <= 0, want > 0 (3.10 is newer than 3.9)`)
	}
	if compareMinors("3.9", "3.10") >= 0 {
		t.Error(`compareMinors("3.9", "3.10") >= 0, want < 0`)
	}
	if compareMinors("3.12", "3.12") != 0 {
		t.Error(`compareMinors("3.12", "3.12") != 0, want 0`)
	}
}

// ownTriple returns the real PBS triple for the platform/arch these
// tests are actually running on, so fixtures aren't hardcoded to one
// platform.
func ownTriple(t *testing.T) string {
	t.Helper()
	triple, err := pythonTriple()
	if err != nil {
		t.Skipf("unsupported platform for this test: %v", err)
	}
	return triple
}

func TestListRemoteVersions_ReturnsDistinctMinorsNewestFirst(t *testing.T) {
	triple := ownTriple(t)
	rel := release{
		TagName: "20260929",
		Assets: []releaseAsset{
			{Name: "cpython-3.10.21+20260929-" + triple + "-install_only.tar.gz"},
			{Name: "cpython-3.11.16+20260929-" + triple + "-install_only.tar.gz"},
			{Name: "cpython-3.12.14+20260929-" + triple + "-install_only.tar.gz"},
			{Name: "cpython-3.9.7+20260929-" + triple + "-install_only.tar.gz"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(rel); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	p := &Python{baseURL: server.URL}

	versions, err := p.ListRemoteVersions(context.Background())
	if err != nil {
		t.Fatalf("ListRemoteVersions() returned error: %v", err)
	}
	want := []string{"3.12", "3.11", "3.10", "3.9"}
	if len(versions) != len(want) {
		t.Fatalf("ListRemoteVersions() returned %d versions, want %d: %+v", len(versions), len(want), versions)
	}
	for i, w := range want {
		if versions[i].Name != w {
			t.Errorf("ListRemoteVersions()[%d].Name = %q, want %q", i, versions[i].Name, w)
		}
	}
}

func TestListRemoteVersions_ExcludesOtherTripleAndVariants(t *testing.T) {
	triple := ownTriple(t)
	rel := release{
		Assets: []releaseAsset{
			{Name: "cpython-3.12.14+20260929-some-other-triple-install_only.tar.gz"},
			{Name: "cpython-3.12.14+20260929-" + triple + "-install_only_stripped.tar.gz"},
			{Name: "cpython-3.11.16+20260929-" + triple + "-install_only.tar.gz"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(rel); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	p := &Python{baseURL: server.URL}

	versions, err := p.ListRemoteVersions(context.Background())
	if err != nil {
		t.Fatalf("ListRemoteVersions() returned error: %v", err)
	}
	if len(versions) != 1 || versions[0].Name != "3.11" {
		t.Errorf("ListRemoteVersions() = %+v, want exactly [{3.11}] (other-triple and stripped assets excluded)", versions)
	}
}

func TestListRemoteVersions_ServerErrorReturnsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	p := &Python{baseURL: server.URL}

	_, err := p.ListRemoteVersions(context.Background())
	if err == nil {
		t.Fatal("ListRemoteVersions() returned nil error for a 500 response")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/python/... -v`
Expected: FAIL — package `python` doesn't exist yet (`no Go files in ...`).

- [ ] **Step 3: Write the implementation**

Create `internal/runtime/python/python.go`:

```go
// Package python implements the dev Runtime interface for Python, using
// python-build-standalone (astral-sh) as the distribution source —
// python.org itself ships no portable prebuilt binaries for arbitrary
// platforms, unlike Node.js/nodejs.org or Java/Adoptium.
package python

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/python/... -v`
Expected: PASS (all tests in Step 1).

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/runtime/python/... && gofmt -l internal/runtime/python/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/python/python.go internal/runtime/python/python_test.go
git commit -m "$(cat <<'EOF'
Add Python provider scaffold: triple mapping and ListRemoteVersions

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `ListInstalledVersions` and `CurrentVersion`

**Files:**
- Modify: `internal/runtime/python/python.go`
- Modify: `internal/runtime/python/python_test.go`

**Interfaces:**
- Consumes: `platform.VersionsDir()`, `platform.CurrentDir()` (both `func() (string, error)`, `internal/platform`).
- Produces: `func (p *Python) ListInstalledVersions() ([]devruntime.Version, error)`; `func (p *Python) CurrentVersion() (*devruntime.Version, error)`. Later tasks (`Install`, `Uninstall`, `Activate`) read/write the same `versions/python/<name>` and `current/python` paths these establish.

- [ ] **Step 1: Write the failing tests**

Add to `internal/runtime/python/python_test.go`:

```go
func TestListInstalledVersions_EmptyWhenNoneInstalled(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	p := New()

	versions, err := p.ListInstalledVersions()
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
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "python", "3.12"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "python", "3.11"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	p := New()

	versions, err := p.ListInstalledVersions()
	if err != nil {
		t.Fatalf("ListInstalledVersions() returned error: %v", err)
	}
	got := map[string]bool{}
	for _, v := range versions {
		got[v.Name] = true
	}
	if !got["3.12"] || !got["3.11"] || len(got) != 2 {
		t.Errorf("ListInstalledVersions() = %+v, want exactly {3.11, 3.12}", versions)
	}
}

func TestListInstalledVersions_SkipsHiddenAndBackupDirs(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, name := range []string{"3.12", ".tmp-python-extract-abc123", "3.11.old"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "python", name), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	p := New()

	versions, err := p.ListInstalledVersions()
	if err != nil {
		t.Fatalf("ListInstalledVersions() returned error: %v", err)
	}
	if len(versions) != 1 || versions[0].Name != "3.12" {
		t.Errorf("ListInstalledVersions() = %+v, want only {3.12} (leftover temp/backup dirs filtered out)", versions)
	}
}

func TestCurrentVersion_NilWhenNoneActive(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	p := New()

	got, err := p.CurrentVersion()
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
	if err := os.WriteFile(filepath.Join(devHome, "current", "python"), []byte("3.12"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	p := New()

	got, err := p.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got == nil || got.Name != "3.12" {
		t.Errorf(`CurrentVersion() = %+v, want Name="3.12"`, got)
	}
}
```

Add these imports to the test file's `import` block: `"os"`, `"path/filepath"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/python/... -v`
Expected: FAIL — `p.ListInstalledVersions undefined` / `p.CurrentVersion undefined`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/python/python.go` (add `"os"`, `"path/filepath"`, `"strings"` is already imported):

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/python/... -v`
Expected: PASS.

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/runtime/python/... && gofmt -l internal/runtime/python/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/python/python.go internal/runtime/python/python_test.go
git commit -m "$(cat <<'EOF'
Add Python provider ListInstalledVersions and CurrentVersion

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `resolveRelease`, `fetchChecksum`, and `Install`

**Files:**
- Modify: `internal/runtime/python/python.go`
- Modify: `internal/runtime/python/python_test.go`

**Interfaces:**
- Consumes: `devruntime.ValidVersionName(name string) error`; `platform.CacheDir() (string, error)`; `cliutil.WithSpinner(msg string, fn func(update func(int64, int64)) error) error`; `cliutil.Success(format string, args ...any)`; `downloader.DownloadWithProgress(ctx context.Context, url, destPath, sha256 string, update func(int64, int64)) error`; `filesystem.EnsureDir(path string, perm os.FileMode) error`; `filesystem.WriteFileAtomic(path string, data []byte, perm os.FileMode) error`; `installer.ExtractAtomic(archivePath, destDir string) error`; `installer.ReconcileStaleBackup(destDir, backupDir string) error`.
- Produces: `type resolvedRelease struct { FullVersion, AssetName, DownloadURL, ChecksumsURL string }`; `func (p *Python) resolveRelease(ctx context.Context, name string) (resolvedRelease, error)`; `func fetchChecksum(ctx context.Context, checksumsURL, filename string) (string, error)`; `func extractStrippingTopLevel(archivePath, destDir string) error`; `func (p *Python) Install(ctx context.Context, name string) error`. Task 4 reuses `destDir := filepath.Join(versionsDir, "python", name)` and the `.dev-release` marker convention this task establishes.

- [ ] **Step 1: Write the failing tests**

Add to `internal/runtime/python/python_test.go`:

```go
func fixtureAssetName(t *testing.T, fullVersion string) string {
	t.Helper()
	return "cpython-" + fullVersion + "+20260929-" + ownTriple(t) + "-install_only.tar.gz"
}

func TestResolveRelease_FindsMatchingMinorAndTriple(t *testing.T) {
	triple := ownTriple(t)
	rel := release{
		Assets: []releaseAsset{
			{Name: "SHA256SUMS", BrowserDownloadURL: "http://example.invalid/SHA256SUMS"},
			{Name: fixtureAssetName(t, "3.12.14"), BrowserDownloadURL: "http://example.invalid/cpython-3.12.14.tar.gz"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(rel); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	p := &Python{baseURL: server.URL}

	got, err := p.resolveRelease(context.Background(), "3.12")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if got.FullVersion != "3.12.14" {
		t.Errorf("resolveRelease().FullVersion = %q, want %q", got.FullVersion, "3.12.14")
	}
	if got.AssetName != fixtureAssetName(t, "3.12.14") {
		t.Errorf("resolveRelease().AssetName = %q, want %q", got.AssetName, fixtureAssetName(t, "3.12.14"))
	}
	if got.DownloadURL != "http://example.invalid/cpython-3.12.14.tar.gz" {
		t.Errorf("resolveRelease().DownloadURL = %q, want the asset's browser_download_url", got.DownloadURL)
	}
	if got.ChecksumsURL != "http://example.invalid/SHA256SUMS" {
		t.Errorf("resolveRelease().ChecksumsURL = %q, want the SHA256SUMS asset's browser_download_url", got.ChecksumsURL)
	}
}

func TestResolveRelease_NoMatchReturnsZeroValueNoError(t *testing.T) {
	triple := ownTriple(t)
	rel := release{Assets: []releaseAsset{
		{Name: "SHA256SUMS", BrowserDownloadURL: "http://example.invalid/SHA256SUMS"},
		{Name: "cpython-3.12.14+20260929-" + triple + "-install_only.tar.gz"},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(rel); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	p := &Python{baseURL: server.URL}

	got, err := p.resolveRelease(context.Background(), "2.7")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if got.FullVersion != "" {
		t.Errorf("resolveRelease() = %+v, want zero value for an unavailable minor", got)
	}
}

func TestResolveRelease_RejectsPathTraversalShapedAssetName(t *testing.T) {
	// A compromised/misbehaving release response must never be trusted to
	// build a filesystem path. matchAsset's anchored pattern (tested
	// directly in Task 1) is what actually rejects this shape — this test
	// pins that resolveRelease integrates that rejection end-to-end
	// rather than, say, matching on a loose prefix/substring check that
	// would let a trailing "/../../evil" segment through.
	triple := ownTriple(t)
	rel := release{Assets: []releaseAsset{
		{Name: "SHA256SUMS", BrowserDownloadURL: "http://example.invalid/SHA256SUMS"},
		{Name: "cpython-3.12.14+20260929-" + triple + "-install_only.tar.gz/../../evil", BrowserDownloadURL: "http://example.invalid/evil"},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(rel); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	p := &Python{baseURL: server.URL}

	got, err := p.resolveRelease(context.Background(), "3.12")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if got.FullVersion != "" {
		t.Errorf("resolveRelease() = %+v, want zero value (malformed asset name must not match)", got)
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

func buildPythonArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	prefixed := make(map[string]string, len(files))
	for name, content := range files {
		prefixed["python/"+name] = content
	}
	return buildFlatTarGz(t, prefixed)
}

func TestInstall_DownloadsVerifiesAndExtracts(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	triple := ownTriple(t)

	filename := "cpython-3.12.14+20260929-" + triple + "-install_only.tar.gz"
	archiveBytes := buildPythonArchive(t, map[string]string{"bin/python3": "fake python binary"})
	sum := sha256.Sum256(archiveBytes)
	checksumLine := hex.EncodeToString(sum[:]) + "  " + filename + "\n"

	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"20260929","assets":[
			{"name":"SHA256SUMS","browser_download_url":"%s/SHA256SUMS"},
			{"name":%q,"browser_download_url":"%s/%s"}
		]}`, "http://"+r.Host, filename, "http://"+r.Host, filename)
	})
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(checksumLine)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})
	mux.HandleFunc("/"+filename, func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write(archiveBytes); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := &Python{baseURL: server.URL}
	if err := p.Install(context.Background(), "3.12"); err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(devHome, "versions", "python", "3.12", "bin", "python3"))
	if err != nil {
		t.Fatalf("reading installed binary: %v", err)
	}
	if string(got) != "fake python binary" {
		t.Errorf("installed binary content = %q, want %q", got, "fake python binary")
	}
}

func TestInstall_AlreadyInstalledSkipsNetworkRedownload(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	triple := ownTriple(t)

	filename := "cpython-3.12.14+20260929-" + triple + "-install_only.tar.gz"
	archiveBytes := buildPythonArchive(t, map[string]string{"bin/python3": "v1"})
	sum := sha256.Sum256(archiveBytes)
	checksumLine := hex.EncodeToString(sum[:]) + "  " + filename + "\n"

	downloadCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"20260929","assets":[
			{"name":"SHA256SUMS","browser_download_url":"%s/SHA256SUMS"},
			{"name":%q,"browser_download_url":"%s/%s"}
		]}`, "http://"+r.Host, filename, "http://"+r.Host, filename)
	})
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(checksumLine)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})
	mux.HandleFunc("/"+filename, func(w http.ResponseWriter, r *http.Request) {
		downloadCount++
		if _, err := w.Write(archiveBytes); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := &Python{baseURL: server.URL}
	if err := p.Install(context.Background(), "3.12"); err != nil {
		t.Fatalf("first Install() returned error: %v", err)
	}
	if err := p.Install(context.Background(), "3.12"); err != nil {
		t.Fatalf("second Install() returned error: %v", err)
	}

	if downloadCount != 1 {
		t.Errorf("archive downloaded %d times, want exactly 1 (second Install should skip the network)", downloadCount)
	}
}

func TestInstall_OfflineFallbackUsesCachedReleaseWhenIndexFetchFails(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	destDir := filepath.Join(devHome, "versions", "python", "3.12")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, ".dev-release"), []byte("3.12.14"), 0o644); err != nil {
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

	p := &Python{baseURL: server.URL}
	if err := p.Install(context.Background(), "3.12"); err != nil {
		t.Fatalf("Install() returned error despite an offline fallback being available: %v", err)
	}
	if !strings.Contains(buf.String(), "3.12.14") {
		t.Errorf("Install() output = %q, want it to mention the already-installed release", buf.String())
	}
}

func TestInstall_NoOfflineFallbackWhenNothingCached(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	p := &Python{baseURL: server.URL}
	if err := p.Install(context.Background(), "3.12"); err == nil {
		t.Fatal("Install() returned nil error despite no cached release and a failing fetch")
	}
}

func TestInstall_UnknownVersionFailsWithoutCreatingDirectory(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	triple := ownTriple(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := release{Assets: []releaseAsset{
			{Name: "SHA256SUMS", BrowserDownloadURL: "http://example.invalid/SHA256SUMS"},
			{Name: "cpython-3.12.14+20260929-" + triple + "-install_only.tar.gz", BrowserDownloadURL: "http://example.invalid/x"},
		}}
		if err := json.NewEncoder(w).Encode(rel); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()

	p := &Python{baseURL: server.URL}
	err := p.Install(context.Background(), "9.9")
	if err == nil {
		t.Fatal("Install() returned nil error for a nonexistent minor version")
	}

	if _, statErr := os.Stat(filepath.Join(devHome, "versions", "python", "9.9")); !os.IsNotExist(statErr) {
		t.Error("Install() created a directory for a version that failed to resolve")
	}
}
```

Add these imports to the test file's `import` block: `"archive/tar"`, `"bytes"`, `"compress/gzip"`, `"crypto/sha256"`, `"encoding/hex"`, `"fmt"`, `"github.com/jpsdm/dev/internal/cliutil"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/python/... -v`
Expected: FAIL — `p.resolveRelease undefined` / `p.Install undefined`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/python/python.go` (add imports `"bufio"`, `"os"` (already added in Task 2), `"path/filepath"` (already added), `"github.com/jpsdm/dev/internal/cliutil"`, `"github.com/jpsdm/dev/internal/downloader"`, `"github.com/jpsdm/dev/internal/filesystem"`, `"github.com/jpsdm/dev/internal/installer"`):

```go
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
// returning its exact version, filename, and download URL together with
// the release's SHA256SUMS URL — one API call serves both Install's
// download step and its checksum-fetch step. A zero-value result (nil
// error) means no matching release was found (an unrecognized minor, or
// a real minor with no build for this OS/arch pair, such as Windows/arm64
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

	for _, a := range rel.Assets {
		fullVersion, ok := matchAsset(a.Name, triple)
		if !ok || minorOf(fullVersion) != name {
			continue
		}
		if err := devruntime.ValidVersionName(fullVersion); err != nil {
			return resolvedRelease{}, fmt.Errorf("resolving Python %s: %w", name, err)
		}
		// a.Name flows into archivePath via filepath.Join in Install —
		// it comes from the same untrusted API response as fullVersion
		// and needs the same path-safety check before Install ever uses
		// it to build a filesystem path.
		if err := devruntime.ValidVersionName(a.Name); err != nil {
			return resolvedRelease{}, fmt.Errorf("resolving Python %s: %w", name, err)
		}
		if a.BrowserDownloadURL == "" || checksumsURL == "" {
			return resolvedRelease{}, fmt.Errorf("resolving Python %s: incomplete release metadata from python-build-standalone", name)
		}
		return resolvedRelease{
			FullVersion:  fullVersion,
			AssetName:    a.Name,
			DownloadURL:  a.BrowserDownloadURL,
			ChecksumsURL: checksumsURL,
		}, nil
	}
	return resolvedRelease{}, nil
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/python/... -v`
Expected: PASS.

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/runtime/python/... && gofmt -l internal/runtime/python/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/python/python.go internal/runtime/python/python_test.go
git commit -m "$(cat <<'EOF'
Add Python provider resolveRelease and Install

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `Uninstall` and `Activate`

**Files:**
- Modify: `internal/runtime/python/python.go`
- Modify: `internal/runtime/python/python_test.go`

**Interfaces:**
- Consumes: `filesystem.Exists(path string) bool`; `filesystem.WriteFileAtomic` (from Task 3); `p.CurrentVersion()` (from Task 2).
- Produces: `func (p *Python) Uninstall(name string) error`; `func (p *Python) Activate(name string) error`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/runtime/python/python_test.go`:

```go
func TestUninstall_NotInstalledReturnsError(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	p := New()

	err := p.Uninstall("3.12")
	if err == nil {
		t.Fatal("Uninstall() returned nil error for a version that was never installed")
	}
	if !strings.Contains(err.Error(), "Python") {
		t.Errorf("Uninstall() error = %q, want it to preserve the \"Python\" brand name", err)
	}
}

func TestUninstall_LeavesOtherActiveVersionMarkerAlone(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "python", "3.12"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "python", "3.11"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "current"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devHome, "current", "python"), []byte("3.11"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	p := New()

	if err := p.Uninstall("3.12"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	got, err := p.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got == nil || got.Name != "3.11" {
		t.Errorf("CurrentVersion() = %+v after uninstalling a non-active version, want Name=\"3.11\" untouched", got)
	}
}

func TestUninstall_RemovesDirectory(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	dir := filepath.Join(devHome, "versions", "python", "3.12")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	p := New()

	if err := p.Uninstall("3.12"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}
	if filesystem.Exists(dir) {
		t.Error("Uninstall() did not remove the version directory")
	}
}

func TestUninstall_ClearsActiveMarkerIfCurrentlyActive(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "python", "3.12"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "current"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devHome, "current", "python"), []byte("3.12"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	p := New()

	if err := p.Uninstall("3.12"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	got, err := p.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got != nil {
		t.Errorf("CurrentVersion() = %+v after uninstalling the active version, want nil", got)
	}
}

func TestActivate_NotInstalledReturnsError(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	p := New()

	err := p.Activate("3.12")
	if err == nil {
		t.Fatal("Activate() returned nil error for a version that isn't installed")
	}
	if !strings.Contains(err.Error(), "Python") {
		t.Errorf("Activate() error = %q, want it to preserve the \"Python\" brand name", err)
	}

	current, err := p.CurrentVersion()
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
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "python", "3.12"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	p := New()

	if err := p.Activate("3.12"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	got, err := p.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got == nil || got.Name != "3.12" {
		t.Errorf(`CurrentVersion() = %+v, want Name="3.12"`, got)
	}
}
```

Add `"github.com/jpsdm/dev/internal/filesystem"` to the test file's `import` block (used by `TestUninstall_RemovesDirectory`'s `filesystem.Exists` call).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/python/... -v`
Expected: FAIL — `p.Uninstall undefined` / `p.Activate undefined`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/python/python.go`:

```go
// Uninstall removes versions/python/<name>. If it was the active
// version, current/python is cleared too, since a marker pointing at a
// removed version is worse than no active version.
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/python/... -v`
Expected: PASS.

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/runtime/python/... && gofmt -l internal/runtime/python/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/python/python.go internal/runtime/python/python_test.go
git commit -m "$(cat <<'EOF'
Add Python provider Uninstall and Activate

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `ShimNames` and `BinaryPath` (Windows pip/pip3 gap included)

**Files:**
- Modify: `internal/runtime/python/python.go`
- Modify: `internal/runtime/python/python_test.go`

**Interfaces:**
- Consumes: `filesystem.Exists(path string) bool` (from Task 4).
- Produces: `func (p *Python) ShimNames() []string`; `func binaryPathForOS(osName, versionDir, binName string) string`; `func (p *Python) BinaryPath(versionDir, binName string) (string, error)`. This completes `devruntime.Runtime` for `*Python`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/runtime/python/python_test.go`:

```go
var _ devruntime.Runtime = (*Python)(nil)

func TestShimNames(t *testing.T) {
	t.Parallel()
	p := New()
	names := p.ShimNames()
	want := map[string]bool{"python3": true, "python": true, "pip3": true, "pip": true}
	if len(names) != len(want) {
		t.Fatalf("ShimNames() = %v, want exactly %v", names, want)
	}
	for _, name := range names {
		if !want[name] {
			t.Errorf("ShimNames() included unexpected %q", name)
		}
	}
}

func TestBinaryPathForOS_UnixLayout(t *testing.T) {
	t.Parallel()
	for _, osName := range []string{"linux", "darwin"} {
		for _, bin := range []string{"python3", "python", "pip3", "pip"} {
			got := binaryPathForOS(osName, "/versiondir", bin)
			want := filepath.Join("/versiondir", "bin", bin)
			if got != want {
				t.Errorf("binaryPathForOS(%q, ..., %q) = %q, want %q", osName, bin, got, want)
			}
		}
	}
}

func TestBinaryPathForOS_WindowsLayout(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"python3": filepath.Join("/versiondir", "python.exe"),
		"python":  filepath.Join("/versiondir", "python.exe"),
		"pip3":    filepath.Join("/versiondir", "Scripts", "pip3.exe"),
		"pip":     filepath.Join("/versiondir", "Scripts", "pip.exe"),
	}
	for bin, want := range cases {
		got := binaryPathForOS("windows", "/versiondir", bin)
		if got != want {
			t.Errorf("binaryPathForOS(\"windows\", ..., %q) = %q, want %q", bin, got, want)
		}
	}
}

func TestBinaryPath_MissingBinaryReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := New()

	_, err := p.BinaryPath(dir, "python3")
	if err == nil {
		t.Fatal("BinaryPath() returned nil error for a binary that doesn't exist")
	}
}

func TestBinaryPath_WindowsPipReturnsOrdinaryNotFoundError(t *testing.T) {
	// python-build-standalone's Windows install_only builds ship no
	// pip/pip3 executable at all — BinaryPath must report this the same
	// ordinary way it reports any other missing binary, not a special
	// error shape (see Review Focus).
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "python.exe"), []byte("fake"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	candidate := binaryPathForOS("windows", dir, "pip3")
	if filesystem.Exists(candidate) {
		t.Fatalf("test setup invalid: %q unexpectedly exists", candidate)
	}
}

func TestBinaryPath_UnixLayout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test targets the Unix (bin/) layout")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	pythonBin := filepath.Join(binDir, "python3")
	if err := os.WriteFile(pythonBin, []byte("fake"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	p := New()
	got, err := p.BinaryPath(dir, "python3")
	if err != nil {
		t.Fatalf("BinaryPath() returned error: %v", err)
	}
	if got != pythonBin {
		t.Errorf("BinaryPath() = %q, want %q", got, pythonBin)
	}
}

func TestBinaryPath_WindowsLayout(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("this test targets the Windows layout")
	}
	dir := t.TempDir()
	pythonExe := filepath.Join(dir, "python.exe")
	if err := os.WriteFile(pythonExe, []byte("fake"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	p := New()
	got, err := p.BinaryPath(dir, "python3")
	if err != nil {
		t.Fatalf("BinaryPath(python3) returned error: %v", err)
	}
	if got != pythonExe {
		t.Errorf("BinaryPath(python3) = %q, want %q", got, pythonExe)
	}

	if _, err := p.BinaryPath(dir, "pip3"); err == nil {
		t.Fatal("BinaryPath(pip3) returned nil error, want an error (no pip shipped on Windows)")
	}
}
```

Add `"runtime"` (stdlib) and `devruntime "github.com/jpsdm/dev/internal/runtime"` to the test file's `import` block (`devruntime.Runtime` is used by the `var _ devruntime.Runtime = (*Python)(nil)` compile-time assertion; `filesystem` was already imported in Task 4).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/python/... -v`
Expected: FAIL — `p.ShimNames undefined` / `binaryPathForOS undefined` / `p.BinaryPath undefined`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/python/python.go`:

```go
// ShimNames returns the Python binaries dev creates shims for.
func (p *Python) ShimNames() []string {
	return []string{"python3", "python", "pip3", "pip"}
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/python/... -v`
Expected: PASS.

- [ ] **Step 5: Run the full test suite, `go vet`, and `gofmt` check**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all tests pass; no output from `go vet` or `gofmt -l`.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/python/python.go internal/runtime/python/python_test.go
git commit -m "$(cat <<'EOF'
Add Python provider ShimNames and BinaryPath

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Register the Python provider and update the README

**Files:**
- Modify: `internal/providers/providers.go`
- Modify: `internal/providers/providers_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `python.New() *python.Python` (Task 1); `runtime.Manager.Register(r runtime.Runtime)`, `runtime.Manager.Get(name string) (runtime.Runtime, bool)` (`internal/runtime`, unchanged).
- Produces: nothing new — this task only wires the finished provider in.

- [ ] **Step 1: Write the failing test**

Add to `internal/providers/providers_test.go`:

```go
func TestRegister_RegistersPython(t *testing.T) {
	t.Parallel()
	m := runtime.NewManager()
	Register(m)

	r, ok := m.Get("python")
	if !ok {
		t.Fatal(`Get("python") ok = false after Register(), want true`)
	}
	if r.Name() != "python" {
		t.Errorf(`Get("python").Name() = %q, want "python"`, r.Name())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/providers/... -v`
Expected: FAIL — `Get("python") ok = false`.

- [ ] **Step 3: Register the provider**

In `internal/providers/providers.go`, add the import and the one registration line:

```go
import (
	"github.com/jpsdm/dev/internal/runtime"
	golang "github.com/jpsdm/dev/internal/runtime/go"
	"github.com/jpsdm/dev/internal/runtime/java"
	"github.com/jpsdm/dev/internal/runtime/node"
	"github.com/jpsdm/dev/internal/runtime/python"
)

// Register adds every known Runtime provider to m.
func Register(m *runtime.Manager) {
	m.Register(node.New())
	m.Register(java.New())
	m.Register(golang.New())
	m.Register(python.New())
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/providers/... -v`
Expected: PASS.

- [ ] **Step 5: Update the README**

In `README.md`, find the line:

```
Currently supported languages: Node.js, Java (Eclipse Temurin), Go.
```

Replace it with:

```
Currently supported languages: Node.js, Java (Eclipse Temurin), Go, Python
(via python-build-standalone).
```

Find the usage example block that lists `dev lang install go 1.24` / `dev lang use go 1.24`, and add matching lines immediately after them:

```
    dev lang install python 3.12
    dev lang use python 3.12
```

Find the paragraph documenting the Java majors/platform gap (the one starting "The Java majors listed by `dev lang list java` are all majors Temurin..."), and add a new paragraph immediately after it:

```
Similarly, `python-build-standalone`'s Windows/arm64 builds only exist for
Python 3.11 and newer — `dev lang list python` on that platform won't offer
earlier minors. Its Windows builds also ship no `pip`/`pip3` executable at
all (only the `pip` module, no prebuilt console-script entry point) — the
`pip`/`pip3` shims report "not found" there; use `python3 -m pip` instead.
```

Find the sentence listing which shimmed binaries run transparently (the one starting "Running `node`, `npm`, `npx`, `java`, `javac`, `go`, or `gofmt`..."), and update it to include Python's shims:

```
Running `node`, `npm`, `npx`, `java`, `javac`, `go`, `gofmt`, `python3`,
`python`, `pip3`, or `pip` (once a version is installed and active)
needed.
```

(Keep the rest of that sentence exactly as it already reads — only the list of binary names changes.)

- [ ] **Step 6: Run the full test suite one more time**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all tests pass; no output from `go vet` or `gofmt -l`.

- [ ] **Step 7: Commit**

```bash
git add internal/providers/providers.go internal/providers/providers_test.go README.md
git commit -m "$(cat <<'EOF'
Register Python provider and document it in the README

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Post-plan manual smoke test (not a task — run once after Task 6, on whatever platform is available)

```bash
make build
dev lang list python
dev lang install python 3.12
dev lang use python 3.12
dev lang installed python
dev lang current python
python3 --version   # via the shim, after `dev setup`
pip3 --version      # via the shim (expected to report "not found" on Windows)
dev lang uninstall python 3.12
```
