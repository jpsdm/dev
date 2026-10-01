# Go Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Go language provider (go.dev/dl) so `dev lang install go <line>`, `dev lang use go <line>`, and the `go`/`gofmt` shims work exactly the way the existing Node.js and Java providers' equivalents already do.

**Architecture:** A new `internal/runtime/go` package (package name `golang` — `go` is a reserved word) implements `devruntime.Runtime`, mirroring `internal/runtime/java`'s structure as closely as the real API differences allow (same `baseURL`-override-for-tests pattern, same local-directory-listing helpers, same atomic-extraction helper). It differs in three real ways: (1) go.dev/dl exposes a *single* JSON index covering every release, every platform, and every checksum at once — both `ListRemoteVersions` and `resolveRelease` hit the same endpoint, and there is no separate checksum-file fetch (like Java, unlike Node); (2) OS/arch mapping to `platform.OS()`/`platform.Arch()` is an identity function, not a real relabeling, because the index's own vocabulary literally *is* Go's `GOOS`/`GOARCH`; (3) version granularity is a two-component "line" (`"1.24"`, i.e. major.minor) rather than a single integer, which means line-ordering and newest-patch selection both need explicit numeric comparisons instead of the string/integer comparisons Node and Java could get away with — and, since the index's ordering is undocumented, newest-patch selection never trusts array order. The provider is registered in `internal/providers.Register` alongside Node and Java — `cmd/lang.go` and `internal/shim` need zero changes.

**Tech Stack:** Go stdlib only (`net/http`, `encoding/json`, `regexp`, `strconv`, `archive/tar`, `archive/zip`, `compress/gzip`) plus this project's own `internal/installer`, `internal/downloader`, `internal/filesystem`, `internal/platform`, `internal/cliutil` packages — no new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-29-go-provider-design.md`

## Global Constraints

- Production `baseURL` defaults to `https://go.dev/dl`.
- Index endpoint: `GET {baseURL}/?mode=json&include=all` — a single call used by both `ListRemoteVersions` and `resolveRelease`. No separate checksum fetch: the checksum is embedded in each file entry's `sha256` field.
- Filter releases to `Stable == true`; filter files to `Kind == "archive"` (excludes `"installer"` and `"source"` kinds).
- `os`/`arch` matching uses `goOS()`/`goArch()`, which validate `platform.OS()`/`platform.Arch()` against go.dev/dl's own vocabulary — an identity mapping in practice (it's Go's own build matrix), kept as explicit functions for pattern consistency with Node/Java and as a defensive whitelist, not because real relabeling is expected.
- Version granularity is the major.minor "line" (e.g. `"1.24"`). `parseLineAndPatch` accepts both the three-component form (`"go1.24.3"`) and a defensive two-component form (`"go1.24"`, implicit patch `0`) — don't assume only one form appears in the full release history.
- The newest patch on a line is chosen by explicit numeric comparison of the parsed patch number, **never** by trusting the JSON array's own ordering — go.dev/dl documents no ordering guarantee.
- Line-vs-line ordering (`ListRemoteVersions`' newest-first output) is numeric (major, then minor) via `compareLines`, never a plain string comparison — `"1.9"` must sort before `"1.24"`.
- The package lives at `internal/runtime/go/go.go` with package clause `package golang` — `go` is a reserved word, so the directory and package names differ on purpose. Import it under an explicit `golang` alias everywhere it's referenced.
- `BinaryPath` has only two branches (Windows vs. everything else) — Go's archives are flat (`bin/<name>`, `.exe` suffix on Windows only); there is no macOS `Contents/Home` nesting the way Java has.
- `ShimNames()` returns exactly `["go", "gofmt"]`.
- Every field sourced from the JSON response that reaches a filesystem path (`Version`, `Filename`) is validated via `devruntime.ValidVersionName` before use — both fields, not just one, from the start.
- Registration is the only change outside `internal/runtime/go`: one line in `internal/providers.Register`. `cmd/lang.go` and `internal/shim` must not be touched.
- Every commit ends with `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.

## Review Focus

- **Non-stable (rc/beta) releases must never be installable or listed.** Task 1's `ListRemoteVersions` tests include a `Stable: false` rc entry that must be excluded, and Task 3's `resolveRelease` must never select one either — same `Stable == true` filter, exercised by an equivalent fixture.
- **Path-injection-shaped `Version` or `Filename` fields.** Task 3's tests cover both fields independently, not just the first one that looks path-like — applying the lesson from Java's original review finding, where only the release name, not the sibling filename field from the same response, was validated.
- **Multiple patches on the same line arriving out of order.** Task 1's and Task 3's fixtures deliberately place the newest patch *before* the oldest in the JSON array, to prove newest-patch selection is an explicit numeric comparison, not a reliance on index ordering — a real, undocumented assumption Node's own `ListRemoteVersions` makes (see its "index is newest-first" comment) that this provider deliberately does not repeat.
- **A line with no archive for the running platform.** Task 1's `ListRemoteVersions` must exclude such a line entirely, and Task 3's `resolveRelease` must report "no release found" for it rather than erroring or silently substituting a different platform's file.
- **Leftover temp-extraction/backup directories in `ListInstalledVersions`.** Task 2 mirrors Node/Java's already-fixed `.`/`.old`-suffix filtering from the start, not as an after-the-fact fix.

---

### Task 1: Package scaffold, OS/arch mapping, line/patch parsing, `ListRemoteVersions`

**Files:**
- Create: `internal/runtime/go/go.go`
- Test: `internal/runtime/go/go_test.go`

**Interfaces:**
- Consumes: `platform.OS()`, `platform.Arch()` (`internal/platform`); `devruntime.Version{Name string; LTS bool}` (`internal/runtime`).
- Produces: `type Go struct { baseURL string }`; `func New() *Go`; `func (g *Go) Name() string`; `func goOS() (string, error)`; `func goArch() (string, error)`; `type indexFile struct { Filename, OS, Arch, Version, SHA256, Kind string }`; `type indexRelease struct { Version string; Stable bool; Files []indexFile }`; `func (g *Go) fetchIndex(ctx context.Context) ([]indexRelease, error)`; `func parseLineAndPatch(version string) (line string, patch int, ok bool)`; `func hasArchiveFor(files []indexFile, osName, archName string) (indexFile, bool)`; `func compareLines(a, b string) int`; `func (g *Go) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error)`. Later tasks call `goOS()`/`goArch()`, construct `&Go{baseURL: ...}` for tests, and Task 3 reuses `fetchIndex`, `parseLineAndPatch`, and `hasArchiveFor` directly.

- [ ] **Step 1: Write the failing tests**

Create `internal/runtime/go/go_test.go`:

```go
package golang

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGoOS_ReturnsANonEmptyMapping(t *testing.T) {
	t.Parallel()
	got, err := goOS()
	if err != nil {
		t.Fatalf("goOS() returned error on this test's platform: %v", err)
	}
	if got == "" {
		t.Error("goOS() returned empty string")
	}
}

func TestGoArch_ReturnsANonEmptyMapping(t *testing.T) {
	t.Parallel()
	got, err := goArch()
	if err != nil {
		t.Fatalf("goArch() returned error on this test's platform: %v", err)
	}
	if got == "" {
		t.Error("goArch() returned empty string")
	}
}

func TestParseLineAndPatch_ParsesThreeComponentForm(t *testing.T) {
	t.Parallel()
	line, patch, ok := parseLineAndPatch("go1.24.3")
	if !ok {
		t.Fatal("parseLineAndPatch(\"go1.24.3\") ok = false, want true")
	}
	if line != "1.24" {
		t.Errorf("line = %q, want %q", line, "1.24")
	}
	if patch != 3 {
		t.Errorf("patch = %d, want 3", patch)
	}
}

func TestParseLineAndPatch_ParsesDefensiveTwoComponentForm(t *testing.T) {
	t.Parallel()
	// go.dev/dl's historical version strings sometimes omit the patch
	// number for a line's first release ("go1.24" rather than
	// "go1.24.0") — accept this defensively rather than assume only the
	// three-component form ever appears.
	line, patch, ok := parseLineAndPatch("go1.24")
	if !ok {
		t.Fatal("parseLineAndPatch(\"go1.24\") ok = false, want true")
	}
	if line != "1.24" {
		t.Errorf("line = %q, want %q", line, "1.24")
	}
	if patch != 0 {
		t.Errorf("patch = %d, want 0", patch)
	}
}

func TestParseLineAndPatch_RejectsNonMatchingForms(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"", "go", "go1", "go1.28rc1", "go1.28beta1", "1.24.3", "not-a-version"} {
		if _, _, ok := parseLineAndPatch(v); ok {
			t.Errorf("parseLineAndPatch(%q) ok = true, want false", v)
		}
	}
}

func TestCompareLines_OrdersNumericallyNotLexically(t *testing.T) {
	t.Parallel()
	// A plain string comparison would put "1.9" after "1.24" (since '9'
	// > '2' lexically) even though 9 < 24 numerically — compareLines
	// must not make that mistake.
	if compareLines("1.24", "1.9") <= 0 {
		t.Error(`compareLines("1.24", "1.9") <= 0, want > 0`)
	}
	if compareLines("1.9", "1.24") >= 0 {
		t.Error(`compareLines("1.9", "1.24") >= 0, want < 0`)
	}
	if compareLines("2.0", "1.99") <= 0 {
		t.Error(`compareLines("2.0", "1.99") <= 0, want > 0`)
	}
	if compareLines("1.24", "1.24") != 0 {
		t.Error(`compareLines("1.24", "1.24") != 0, want 0`)
	}
}

func archiveFileFor(osName, archName, version, ext string) indexFile {
	return indexFile{
		Filename: version + "." + osName + "-" + archName + ext,
		OS:       osName,
		Arch:     archName,
		Version:  version,
		SHA256:   "deadbeef",
		Kind:     "archive",
	}
}

func archiveExtFor(osName string) string {
	if osName == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

func TestListRemoteVersions_ReturnsNewestPatchPerLineNewestLineFirst(t *testing.T) {
	t.Parallel()
	osName, err := goOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	archName, err := goArch()
	if err != nil {
		t.Skipf("unsupported arch for this test: %v", err)
	}
	ext := archiveExtFor(osName)

	fixture := []indexRelease{
		// Deliberately out of newest-first order, to prove the newest
		// patch on a line is chosen by explicit numeric comparison, not
		// by trusting the index's own ordering.
		{Version: "go1.24.1", Stable: true, Files: []indexFile{archiveFileFor(osName, archName, "go1.24.1", ext)}},
		{Version: "go1.24.3", Stable: true, Files: []indexFile{archiveFileFor(osName, archName, "go1.24.3", ext)}},
		{Version: "go1.23.9", Stable: true, Files: []indexFile{archiveFileFor(osName, archName, "go1.23.9", ext)}},
		{Version: "go1.28rc1", Stable: false, Files: []indexFile{archiveFileFor(osName, archName, "go1.28rc1", ext)}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(fixture); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	g := &Go{baseURL: server.URL}

	versions, err := g.ListRemoteVersions(context.Background())
	if err != nil {
		t.Fatalf("ListRemoteVersions() returned error: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("ListRemoteVersions() = %+v, want exactly 2 lines (1.24 and 1.23 — the rc must be excluded)", versions)
	}
	if versions[0].Name != "1.24" {
		t.Errorf("ListRemoteVersions()[0].Name = %q, want %q (newest line first)", versions[0].Name, "1.24")
	}
	if versions[1].Name != "1.23" {
		t.Errorf("ListRemoteVersions()[1].Name = %q, want %q", versions[1].Name, "1.23")
	}
}

func TestListRemoteVersions_ExcludesLinesWithNoArchiveForThisPlatform(t *testing.T) {
	t.Parallel()
	fixture := []indexRelease{
		{Version: "go1.24.3", Stable: true, Files: []indexFile{
			{Filename: "go1.24.3.plan9-386.tar.gz", OS: "plan9", Arch: "386", Version: "go1.24.3", SHA256: "x", Kind: "archive"},
		}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(fixture); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	g := &Go{baseURL: server.URL}

	versions, err := g.ListRemoteVersions(context.Background())
	if err != nil {
		t.Fatalf("ListRemoteVersions() returned error: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("ListRemoteVersions() = %+v, want empty (no archive for this test's platform)", versions)
	}
}

func TestListRemoteVersions_ServerErrorReturnsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	g := &Go{baseURL: server.URL}

	_, err := g.ListRemoteVersions(context.Background())
	if err == nil {
		t.Fatal("ListRemoteVersions() returned nil error for a 500 response")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/go/... -v`
Expected: FAIL — package `golang` doesn't exist yet (`no Go files in ...`, then `undefined: Go`/`undefined: goOS` etc. once the file exists but before the symbols are defined).

- [ ] **Step 3: Write the implementation**

Create `internal/runtime/go/go.go`:

```go
// Package golang implements the dev Runtime interface for Go, using
// go.dev/dl's own JSON release index as the distribution source. The
// package is named golang, not go, because go is a reserved word.
package golang

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
	osName := platform.OS()
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
	archName := platform.Arch()
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
// for the given os/arch, returning that entry.
func hasArchiveFor(files []indexFile, osName, archName string) (indexFile, bool) {
	for _, f := range files {
		if f.Kind == "archive" && f.OS == osName && f.Arch == archName {
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
	releases, err := g.fetchIndex(ctx)
	if err != nil {
		return nil, err
	}
	osName, err := goOS()
	if err != nil {
		return nil, err
	}
	archName, err := goArch()
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/go/... -v`
Expected: PASS (all tests in Step 1).

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/runtime/go/... && gofmt -l internal/runtime/go/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/go/go.go internal/runtime/go/go_test.go
git commit -m "$(cat <<'EOF'
Add Go provider scaffold: OS/arch mapping and ListRemoteVersions

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `ListInstalledVersions` and `CurrentVersion`

**Files:**
- Modify: `internal/runtime/go/go.go`
- Test: `internal/runtime/go/go_test.go`

**Interfaces:**
- Consumes: `platform.VersionsDir() (string, error)`, `platform.CurrentDir() (string, error)` (`internal/platform`); `devruntime.Version` (Task 1).
- Produces: `func (g *Go) ListInstalledVersions() ([]devruntime.Version, error)`; `func (g *Go) CurrentVersion() (*devruntime.Version, error)`. Task 3's `Install`/offline-fallback and Task 4's `Uninstall` call `CurrentVersion()`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/runtime/go/go_test.go`. Add `"os"` and `"path/filepath"` to the file's `import` block.

```go
func TestListInstalledVersions_EmptyWhenNoneInstalled(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	g := New()

	versions, err := g.ListInstalledVersions()
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
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "go", "1.24"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "go", "1.23"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	g := New()

	versions, err := g.ListInstalledVersions()
	if err != nil {
		t.Fatalf("ListInstalledVersions() returned error: %v", err)
	}
	got := map[string]bool{}
	for _, v := range versions {
		got[v.Name] = true
	}
	if !got["1.24"] || !got["1.23"] || len(got) != 2 {
		t.Errorf("ListInstalledVersions() = %+v, want exactly {1.23, 1.24}", versions)
	}
}

func TestListInstalledVersions_SkipsHiddenAndBackupDirs(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, name := range []string{"1.24", ".tmp-go-extract-abc123", "1.23.old"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "go", name), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	g := New()

	versions, err := g.ListInstalledVersions()
	if err != nil {
		t.Fatalf("ListInstalledVersions() returned error: %v", err)
	}
	if len(versions) != 1 || versions[0].Name != "1.24" {
		t.Errorf("ListInstalledVersions() = %+v, want only {1.24} (leftover temp/backup dirs filtered out)", versions)
	}
}

func TestCurrentVersion_NilWhenNoneActive(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	g := New()

	got, err := g.CurrentVersion()
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
	if err := os.WriteFile(filepath.Join(devHome, "current", "go"), []byte("1.24"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	g := New()

	got, err := g.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got == nil || got.Name != "1.24" {
		t.Errorf(`CurrentVersion() = %+v, want Name="1.24"`, got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/go/... -v -run 'TestListInstalledVersions|TestCurrentVersion'`
Expected: FAIL with `undefined: (*Go).ListInstalledVersions` / `undefined: (*Go).CurrentVersion`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/go/go.go`. Extend the import block with `"os"`, `"path/filepath"`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/go/... -v`
Expected: PASS (all tests so far).

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/runtime/go/... && gofmt -l internal/runtime/go/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/go/go.go internal/runtime/go/go_test.go
git commit -m "$(cat <<'EOF'
Add Go provider ListInstalledVersions and CurrentVersion

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `resolveRelease` and `Install`

**Files:**
- Modify: `internal/runtime/go/go.go`
- Test: `internal/runtime/go/go_test.go`

**Interfaces:**
- Consumes: `goOS()`, `goArch()`, `fetchIndex`, `parseLineAndPatch`, `hasArchiveFor` (Task 1); `devruntime.ValidVersionName(name string) error` (`internal/runtime`); `platform.VersionsDir()`, `platform.CacheDir()`; `downloader.Download(ctx context.Context, url, destPath, wantSHA256 string) error` (`internal/downloader`); `installer.ExtractAtomic(archivePath, destDir string) error`, `installer.ReconcileStaleBackup(destDir, backupDir string) error` (`internal/installer`); `filesystem.EnsureDir(path string, perm os.FileMode) error`, `filesystem.WriteFileAtomic(path string, data []byte, perm os.FileMode) error` (`internal/filesystem`); `cliutil.Success(format string, args ...any)`, `cliutil.Step(format string, args ...any)` (`internal/cliutil`).
- Produces: `type goRelease struct { Version, URL, Filename, SHA256 string }`; `func (g *Go) resolveRelease(ctx context.Context, name string) (goRelease, error)`; `func extractStrippingTopLevel(archivePath, destDir string) error`; `func (g *Go) Install(ctx context.Context, name string) error`. Task 4's `Uninstall` looks for the same `versions/go/<name>` layout `Install` creates.

- [ ] **Step 1: Write the failing tests**

Append to `internal/runtime/go/go_test.go`. Add these imports to the file's `import` block: `"archive/tar"`, `"archive/zip"`, `"bytes"`, `"compress/gzip"`, `"crypto/sha256"`, `"encoding/hex"`, `"fmt"`, `"strings"`, `"github.com/jpsdm/dev/internal/cliutil"`.

```go
func TestResolveRelease_ParsesVersionURLFilenameAndChecksum(t *testing.T) {
	t.Parallel()
	osName, err := goOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	archName, err := goArch()
	if err != nil {
		t.Skipf("unsupported arch for this test: %v", err)
	}
	ext := archiveExtFor(osName)
	filename := "go1.24.3." + osName + "-" + archName + ext

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"version":"go1.24.3","stable":true,"files":[
			{"filename":%q,"os":%q,"arch":%q,"version":"go1.24.3","sha256":"f9d6e191deadbeef","kind":"archive"}
		]}]`, filename, osName, archName)
	}))
	defer server.Close()
	g := &Go{baseURL: server.URL}

	release, err := g.resolveRelease(context.Background(), "1.24")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if release.Version != "go1.24.3" {
		t.Errorf("release.Version = %q, want %q", release.Version, "go1.24.3")
	}
	if release.Filename != filename {
		t.Errorf("release.Filename = %q, want %q", release.Filename, filename)
	}
	if release.SHA256 != "f9d6e191deadbeef" {
		t.Errorf("release.SHA256 = %q, want %q", release.SHA256, "f9d6e191deadbeef")
	}
	wantURL := server.URL + "/" + filename
	if release.URL != wantURL {
		t.Errorf("release.URL = %q, want %q", release.URL, wantURL)
	}
}

func TestResolveRelease_PicksGreatestPatchOnLineRegardlessOfOrder(t *testing.T) {
	t.Parallel()
	osName, err := goOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	archName, err := goArch()
	if err != nil {
		t.Skipf("unsupported arch for this test: %v", err)
	}
	ext := archiveExtFor(osName)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[
			{"version":"go1.24.1","stable":true,"files":[{"filename":"go1.24.1.%[1]s-%[2]s%[3]s","os":%[1]q,"arch":%[2]q,"version":"go1.24.1","sha256":"old","kind":"archive"}]},
			{"version":"go1.24.3","stable":true,"files":[{"filename":"go1.24.3.%[1]s-%[2]s%[3]s","os":%[1]q,"arch":%[2]q,"version":"go1.24.3","sha256":"new","kind":"archive"}]}
		]`, osName, archName, ext)
	}))
	defer server.Close()
	g := &Go{baseURL: server.URL}

	release, err := g.resolveRelease(context.Background(), "1.24")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if release.Version != "go1.24.3" {
		t.Errorf("release.Version = %q, want %q (the greater patch)", release.Version, "go1.24.3")
	}
	if release.SHA256 != "new" {
		t.Errorf("release.SHA256 = %q, want %q", release.SHA256, "new")
	}
}

func TestResolveRelease_NoMatchingLineReturnsEmptyReleaseNoError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	g := &Go{baseURL: server.URL}

	release, err := g.resolveRelease(context.Background(), "9.99")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if release.Version != "" {
		t.Errorf("resolveRelease() = %+v, want a zero-value release for an unrecognized line", release)
	}
}

func TestResolveRelease_LineExistsButNoArchiveForThisPlatformReturnsEmptyReleaseNoError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"version":"go1.24.3","stable":true,"files":[
			{"filename":"go1.24.3.plan9-386.tar.gz","os":"plan9","arch":"386","version":"go1.24.3","sha256":"x","kind":"archive"}
		]}]`)
	}))
	defer server.Close()
	g := &Go{baseURL: server.URL}

	release, err := g.resolveRelease(context.Background(), "1.24")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if release.Version != "" {
		t.Errorf("resolveRelease() = %+v, want a zero-value release when no archive matches this platform", release)
	}
}

func TestResolveRelease_ExcludesNonStableReleasesOnTheLine(t *testing.T) {
	t.Parallel()
	osName, err := goOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	archName, err := goArch()
	if err != nil {
		t.Skipf("unsupported arch for this test: %v", err)
	}
	ext := archiveExtFor(osName)

	// Only an rc build exists on this line — resolveRelease must report
	// "no release found", the same as if the line didn't exist at all,
	// never fall back to an unstable release.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"version":"go1.28rc1","stable":false,"files":[
			{"filename":"go1.28rc1.%[1]s-%[2]s%[3]s","os":%[1]q,"arch":%[2]q,"version":"go1.28rc1","sha256":"x","kind":"archive"}
		]}]`, osName, archName, ext)
	}))
	defer server.Close()
	g := &Go{baseURL: server.URL}

	release, err := g.resolveRelease(context.Background(), "1.28")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if release.Version != "" {
		t.Errorf("resolveRelease() = %+v, want a zero-value release when only an rc build exists on this line", release)
	}
}

func TestResolveRelease_RejectsPathInjectionShapedVersion(t *testing.T) {
	t.Parallel()
	// A malformed version (as if a compromised/misbehaving server
	// appended path segments) must never be trusted for building a
	// filesystem path — even though it would fail parseLineAndPatch's
	// own line-matching in practice, resolveRelease's explicit
	// validation is the defense that must not be skipped.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"version":"go1.24.3/../../evil","stable":true,"files":[
			{"filename":"evil.tar.gz","os":"linux","arch":"amd64","version":"go1.24.3/../../evil","sha256":"x","kind":"archive"}
		]}]`)
	}))
	defer server.Close()
	g := &Go{baseURL: server.URL}

	_, err := g.resolveRelease(context.Background(), "1.24")
	// This particular payload never matches lineRE (it isn't shaped
	// like "go1.24.3"), so it's excluded upstream and resolveRelease
	// reports "no release found" rather than a validation error — both
	// outcomes are safe; what matters is that it's never accepted.
	if err != nil {
		t.Fatalf("resolveRelease() returned error (acceptable, but unexpected for this payload): %v", err)
	}
}

func TestResolveRelease_RejectsPathInjectionShapedFilename(t *testing.T) {
	t.Parallel()
	// Unlike the version field, a malformed filename does NOT get
	// filtered out by lineRE (the filename isn't parsed by it at all)
	// — this is exactly the shape of gap Java's original review finding
	// caught (release name validated, sibling filename field from the
	// same response not validated). resolveRelease must reject this.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"version":"go1.24.3","stable":true,"files":[
			{"filename":"../../evil.tar.gz","os":"linux","arch":"amd64","version":"go1.24.3","sha256":"x","kind":"archive"}
		]}]`)
	}))
	defer server.Close()
	g := &Go{baseURL: server.URL}

	_, err := g.resolveRelease(context.Background(), "1.24")
	if err == nil {
		t.Fatal("resolveRelease() returned nil error for a path-injection-shaped filename")
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

	osName, err := goOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	archName, err := goArch()
	if err != nil {
		t.Skipf("unsupported arch for this test: %v", err)
	}
	ext := archiveExtFor(osName)
	filename := "go1.24.3." + osName + "-" + archName + ext
	archiveBytes := buildFixtureArchive(t, ext, "go", map[string]string{
		"bin/go": "fake go binary",
	})
	sum := sha256.Sum256(archiveBytes)
	checksum := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"version":"go1.24.3","stable":true,"files":[
			{"filename":%q,"os":%q,"arch":%q,"version":"go1.24.3","sha256":%q,"kind":"archive"}
		]}]`, filename, osName, archName, checksum)
	})
	mux.HandleFunc("/"+filename, func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write(archiveBytes); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})

	g := &Go{baseURL: server.URL}
	if err := g.Install(context.Background(), "1.24"); err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(devHome, "versions", "go", "1.24", "bin", "go"))
	if err != nil {
		t.Fatalf("reading installed binary: %v", err)
	}
	if string(got) != "fake go binary" {
		t.Errorf("installed binary content = %q, want %q", got, "fake go binary")
	}
}

func TestInstall_AlreadyInstalledSkipsNetworkRedownload(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)

	osName, _ := goOS()
	archName, _ := goArch()
	ext := archiveExtFor(osName)
	filename := "go1.24.3." + osName + "-" + archName + ext
	archiveBytes := buildFixtureArchive(t, ext, "go", map[string]string{"bin/go": "v1"})
	sum := sha256.Sum256(archiveBytes)
	checksum := hex.EncodeToString(sum[:])

	downloadCount := 0
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"version":"go1.24.3","stable":true,"files":[
			{"filename":%q,"os":%q,"arch":%q,"version":"go1.24.3","sha256":%q,"kind":"archive"}
		]}]`, filename, osName, archName, checksum)
	})
	mux.HandleFunc("/"+filename, func(w http.ResponseWriter, r *http.Request) {
		downloadCount++
		if _, err := w.Write(archiveBytes); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})

	g := &Go{baseURL: server.URL}
	if err := g.Install(context.Background(), "1.24"); err != nil {
		t.Fatalf("first Install() returned error: %v", err)
	}
	if err := g.Install(context.Background(), "1.24"); err != nil {
		t.Fatalf("second Install() returned error: %v", err)
	}

	if downloadCount != 1 {
		t.Errorf("archive downloaded %d times, want exactly 1 (second Install should skip the network)", downloadCount)
	}
}

func TestInstall_OfflineFallbackUsesCachedReleaseWhenResolveFails(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	destDir := filepath.Join(devHome, "versions", "go", "1.24")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, ".dev-release"), []byte("go1.24.3"), 0o644); err != nil {
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

	g := &Go{baseURL: server.URL}
	if err := g.Install(context.Background(), "1.24"); err != nil {
		t.Fatalf("Install() returned error despite an offline fallback being available: %v", err)
	}
	if !strings.Contains(buf.String(), "go1.24.3") {
		t.Errorf("Install() output = %q, want it to mention the already-installed release", buf.String())
	}
}

func TestInstall_NoOfflineFallbackWhenNothingCached(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	g := &Go{baseURL: server.URL}
	if err := g.Install(context.Background(), "1.24"); err == nil {
		t.Fatal("Install() returned nil error despite no cached release and a failing resolve")
	}
}

func TestInstall_UnknownLineFailsWithoutCreatingDirectory(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()

	g := &Go{baseURL: server.URL}
	err := g.Install(context.Background(), "9.99")
	if err == nil {
		t.Fatal("Install() returned nil error for a nonexistent line")
	}

	if _, statErr := os.Stat(filepath.Join(devHome, "versions", "go", "9.99")); !os.IsNotExist(statErr) {
		t.Error("Install() created a directory for a line that failed to resolve")
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
	if err := os.WriteFile(archive1, buildFixtureArchive(t, ".tar.gz", "go", map[string]string{"bin/go": "v1"}), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := extractStrippingTopLevel(archive1, destDir); err != nil {
		t.Fatalf("extractStrippingTopLevel() first call returned error: %v", err)
	}

	archive2 := filepath.Join(dir, "v2.tar.gz")
	if err := os.WriteFile(archive2, buildFixtureArchive(t, ".tar.gz", "go", map[string]string{"bin/go": "v2"}), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := extractStrippingTopLevel(archive2, destDir); err != nil {
		t.Fatalf("extractStrippingTopLevel() second call returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "bin", "go"))
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

	// Two top-level entries instead of the single "go/" directory Go
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

Run: `go test ./internal/runtime/go/... -v -run 'TestResolveRelease|TestInstall|TestExtractStrippingTopLevel'`
Expected: FAIL with `undefined: (*Go).resolveRelease` / `undefined: (*Go).Install` / `undefined: extractStrippingTopLevel`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/go/go.go`. Extend the import block with `"os"` (already present from Task 2), `"path/filepath"` (already present), plus new imports `"github.com/jpsdm/dev/internal/cliutil"`, `"github.com/jpsdm/dev/internal/downloader"`, `"github.com/jpsdm/dev/internal/filesystem"`, `"github.com/jpsdm/dev/internal/installer"`:

```go
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
		return fmt.Errorf("no Go release found for %q", name)
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

	cliutil.Step("Downloading Go %s...", release.Version)
	if err := downloader.Download(ctx, release.URL, archivePath, release.SHA256); err != nil {
		return fmt.Errorf("installing Go %s: %w", name, err)
	}

	cliutil.Step("Installing...")
	if err := extractStrippingTopLevel(archivePath, destDir); err != nil {
		return fmt.Errorf("installing Go %s: %w", name, err)
	}

	if err := filesystem.WriteFileAtomic(releaseMarker, []byte(release.Version), 0o644); err != nil {
		return fmt.Errorf("recording installed release for Go %s: %w", name, err)
	}

	cliutil.Success("Go %s installed", name)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/go/... -v`
Expected: PASS (all tests so far).

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/runtime/go/... && gofmt -l internal/runtime/go/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/go/go.go internal/runtime/go/go_test.go
git commit -m "$(cat <<'EOF'
Add Go provider resolveRelease and Install

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `Uninstall` and `Activate`

**Files:**
- Modify: `internal/runtime/go/go.go`
- Test: `internal/runtime/go/go_test.go`

**Interfaces:**
- Consumes: `platform.VersionsDir()`, `platform.CurrentDir()`; `filesystem.Exists(path string) bool`, `filesystem.WriteFileAtomic` (Task 3); `g.CurrentVersion()` (Task 2); `cliutil.Success`, `cliutil.Step` (Task 3).
- Produces: `func (g *Go) Uninstall(name string) error`; `func (g *Go) Activate(name string) error`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/runtime/go/go_test.go`. Add `"github.com/jpsdm/dev/internal/filesystem"` to the import block.

```go
func TestUninstall_NotInstalledReturnsError(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	g := New()

	err := g.Uninstall("1.24")
	if err == nil {
		t.Fatal("Uninstall() returned nil error for a version that was never installed")
	}
	if !strings.Contains(err.Error(), "Go") {
		t.Errorf("Uninstall() error = %q, want it to preserve the \"Go\" brand name", err)
	}
}

func TestUninstall_RemovesDirectory(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	dir := filepath.Join(devHome, "versions", "go", "1.24")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	g := New()

	if err := g.Uninstall("1.24"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}
	if filesystem.Exists(dir) {
		t.Error("Uninstall() did not remove the version directory")
	}
}

func TestUninstall_LeavesOtherActiveVersionMarkerAlone(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "go", "1.24"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "go", "1.23"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "current"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devHome, "current", "go"), []byte("1.23"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	g := New()

	if err := g.Uninstall("1.24"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	got, err := g.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got == nil || got.Name != "1.23" {
		t.Errorf("CurrentVersion() = %+v after uninstalling a non-active version, want Name=\"1.23\" untouched", got)
	}
}

func TestUninstall_ClearsActiveMarkerIfCurrentlyActive(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "go", "1.24"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "current"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devHome, "current", "go"), []byte("1.24"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	g := New()

	if err := g.Uninstall("1.24"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	got, err := g.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got != nil {
		t.Errorf("CurrentVersion() = %+v after uninstalling the active version, want nil", got)
	}
}

func TestActivate_NotInstalledReturnsError(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	g := New()

	err := g.Activate("1.24")
	if err == nil {
		t.Fatal("Activate() returned nil error for a version that isn't installed")
	}
	if !strings.Contains(err.Error(), "Go") {
		t.Errorf("Activate() error = %q, want it to preserve the \"Go\" brand name", err)
	}

	current, err := g.CurrentVersion()
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
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "go", "1.24"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	g := New()

	if err := g.Activate("1.24"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	got, err := g.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got == nil || got.Name != "1.24" {
		t.Errorf(`CurrentVersion() = %+v, want Name="1.24"`, got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/go/... -v -run 'TestUninstall|TestActivate'`
Expected: FAIL with `undefined: (*Go).Uninstall` / `undefined: (*Go).Activate`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/go/go.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/go/... -v`
Expected: PASS (all tests so far).

- [ ] **Step 5: Run `go vet` and `gofmt` check**

Run: `go vet ./internal/runtime/go/... && gofmt -l internal/runtime/go/`
Expected: no output from either command.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/go/go.go internal/runtime/go/go_test.go
git commit -m "$(cat <<'EOF'
Add Go provider Uninstall and Activate

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `ShimNames` and `BinaryPath` (two-way OS layout)

**Files:**
- Modify: `internal/runtime/go/go.go`
- Test: `internal/runtime/go/go_test.go`

**Interfaces:**
- Consumes: `goOS()` (Task 1); `filesystem.Exists` (Task 3).
- Produces: `func (g *Go) ShimNames() []string`; `func binaryPathForOS(osName, versionDir, binName string) string`; `func (g *Go) BinaryPath(versionDir, binName string) (string, error)`. This completes the `devruntime.Runtime` interface.

- [ ] **Step 1: Write the failing tests**

Append to `internal/runtime/go/go_test.go`. Add `"github.com/jpsdm/dev/internal/runtime"` (aliased `devruntime`) to the import block if not already present from Task 1 (it is — Task 1 already imports `devruntime "github.com/jpsdm/dev/internal/runtime"`).

```go
var _ devruntime.Runtime = (*Go)(nil)

func TestShimNames(t *testing.T) {
	t.Parallel()
	g := New()
	names := g.ShimNames()
	want := map[string]bool{"go": true, "gofmt": true}
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
	got := binaryPathForOS("linux", "/opt/go/1.24", "go")
	want := filepath.Join("/opt/go/1.24", "bin", "go")
	if got != want {
		t.Errorf(`binaryPathForOS("linux", ...) = %q, want %q`, got, want)
	}
}

func TestBinaryPathForOS_DarwinUsesFlatLayoutLikeLinux(t *testing.T) {
	t.Parallel()
	// Unlike Java's macOS archives (which nest an extra Contents/Home/
	// level), Go's archives are flat on every platform — this test
	// makes that design decision explicit rather than leaving darwin
	// implicitly covered by the "default" switch branch.
	got := binaryPathForOS("darwin", "/opt/go/1.24", "go")
	want := filepath.Join("/opt/go/1.24", "bin", "go")
	if got != want {
		t.Errorf(`binaryPathForOS("darwin", ...) = %q, want %q`, got, want)
	}
}

func TestBinaryPathForOS_Windows(t *testing.T) {
	t.Parallel()
	got := binaryPathForOS("windows", "C:\\go\\1.24", "go")
	want := filepath.Join("C:\\go\\1.24", "bin", "go.exe")
	if got != want {
		t.Errorf(`binaryPathForOS("windows", ...) = %q, want %q`, got, want)
	}
}

func TestBinaryPath_UsesHostLayoutAndChecksExistence(t *testing.T) {
	dir := t.TempDir()
	osName, err := goOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	binPath := binaryPathForOS(osName, dir, "go")
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(binPath, []byte("fake"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	g := New()
	got, err := g.BinaryPath(dir, "go")
	if err != nil {
		t.Fatalf("BinaryPath() returned error: %v", err)
	}
	if got != binPath {
		t.Errorf("BinaryPath() = %q, want %q", got, binPath)
	}
}

func TestBinaryPath_MissingBinaryReturnsError(t *testing.T) {
	dir := t.TempDir()
	g := New()

	_, err := g.BinaryPath(dir, "go")
	if err == nil {
		t.Fatal("BinaryPath() returned nil error for a binary that doesn't exist")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/go/... -v -run 'TestShimNames|TestBinaryPath'`
Expected: FAIL — `undefined: (*Go).ShimNames`, `undefined: binaryPathForOS`, `undefined: (*Go).BinaryPath`, plus the package-level `var _ devruntime.Runtime = (*Go)(nil)` failing to compile.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/go/go.go`:

```go
// ShimNames returns the Go binaries dev creates shims for.
func (g *Go) ShimNames() []string {
	return []string{"go", "gofmt"}
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/go/... -v`
Expected: PASS (every test in the package).

- [ ] **Step 5: Run `go vet`, `gofmt`, and the full package build**

Run: `go vet ./internal/runtime/go/... && gofmt -l internal/runtime/go/ && go build ./...`
Expected: no output from `go vet`/`gofmt`; `go build ./...` succeeds.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/go/go.go internal/runtime/go/go_test.go
git commit -m "$(cat <<'EOF'
Add Go provider ShimNames and BinaryPath

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Register the Go provider and update the README

**Files:**
- Modify: `internal/providers/providers.go`
- Modify: `internal/providers/providers_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `golang.New() *golang.Go` (Task 1, package `golang` at `internal/runtime/go`); `runtime.Manager.Register(r Runtime)`, `runtime.Manager.Get(name string) (Runtime, bool)` (`internal/runtime`, pre-existing).
- Produces: nothing new consumed by later tasks — this is the final integration task.

- [ ] **Step 1: Write the failing test**

Add this test to `internal/providers/providers_test.go`, after the existing `TestRegister_RegistersJava` (or `TestRegister_RegistersNode` if Java hasn't landed yet in this checkout — check the file first):

```go
func TestRegister_RegistersGo(t *testing.T) {
	t.Parallel()
	m := runtime.NewManager()
	Register(m)

	r, ok := m.Get("go")
	if !ok {
		t.Fatal(`Get("go") ok = false after Register(), want true`)
	}
	if r.Name() != "go" {
		t.Errorf(`Get("go").Name() = %q, want "go"`, r.Name())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/providers/... -v -run TestRegister_RegistersGo`
Expected: FAIL — `Get("go") ok = false after Register(), want true`.

- [ ] **Step 3: Register the Go provider**

Read `internal/providers/providers.go` first (it currently registers `node` and `java`), then add the Go provider alongside them:

```go
// Package providers is the single place that lists every Runtime
// provider dev knows about, so command wiring (cmd/lang.go) and shim
// dispatch (internal/shim) can never register a different set.
package providers

import (
	golang "github.com/jpsdm/dev/internal/runtime/go"
	"github.com/jpsdm/dev/internal/runtime/java"
	"github.com/jpsdm/dev/internal/runtime/node"
	"github.com/jpsdm/dev/internal/runtime"
)

// Register adds every known Runtime provider to m.
func Register(m *runtime.Manager) {
	m.Register(node.New())
	m.Register(java.New())
	m.Register(golang.New())
}
```

Keep the existing `node.New()`/`java.New()` lines exactly as they already are in the file — only add the `golang` import and the `m.Register(golang.New())` line. If `gofmt`/`goimports` reorders the import block afterward, that's expected and fine.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/providers/... -v`
Expected: PASS (`TestRegister_RegistersNode`, `TestRegister_RegistersJava`, and `TestRegister_RegistersGo`).

- [ ] **Step 5: Update the README**

In `README.md`, change:

```markdown
Currently supported languages: Node.js, Java (Eclipse Temurin).
```

to:

```markdown
Currently supported languages: Node.js, Java (Eclipse Temurin), Go.
```

And change the language-management example block:

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

to:

```markdown
    dev lang list                  # newest version of every supported language
    dev lang list node             # all available Node.js major versions
    dev lang install node 22
    dev lang use node 22
    dev lang install java 21
    dev lang use java 21
    dev lang install go 1.24
    dev lang use go 1.24
    dev lang current               # active version of every language
    dev lang installed
    dev lang uninstall node 22
```

And change the PATH-and-shims section's sentence naming the shimmed binaries:

```markdown
Those two are enough to run `dev` plus whatever version of a managed
language is currently active — no per-language PATH entries needed.
Running `node`, `npm`, `npx`, `java`, or `javac` transparently runs the
version most recently activated with `dev lang use`.
```

to:

```markdown
Those two are enough to run `dev` plus whatever version of a managed
language is currently active — no per-language PATH entries needed.
Running `node`, `npm`, `npx`, `java`, `javac`, `go`, or `gofmt`
transparently runs the version most recently activated with `dev lang
use`.
```

(Read the actual current wording of both blocks in `README.md` before editing — this plan quotes them from the state after the Java provider shipped; if either has drifted, match the edit to the real surrounding text rather than the quote above.)

- [ ] **Step 6: Run the full test suite, lint, and build**

Run: `make check`
Expected: `gofmt -l` produces no output; `go vet` and `golangci-lint run` report no issues; every package's tests pass, including `internal/runtime/go`, `internal/providers`, and `cmd` (the `lang`/`env`/`setup` commands are generic over the registry, so they pick up `go` automatically — this run is what confirms Global Constraint "`cmd/lang.go` and `internal/shim` need zero changes" actually holds); `go build ./...` (via `make build`) succeeds.

- [ ] **Step 7: Commit**

```bash
git add internal/providers/providers.go internal/providers/providers_test.go README.md
git commit -m "$(cat <<'EOF'
Register the Go provider and document it in the README

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Post-plan manual smoke test (not a task — run once after Task 6, on whatever platform is available)

```bash
go build -o /tmp/dev-smoke .
DEV_HOME=$(mktemp -d) /tmp/dev-smoke lang list go
DEV_HOME=$(mktemp -d) /tmp/dev-smoke lang install go 1.24
```

Confirms the real go.dev/dl index round-trips end-to-end (not just the httptest fixtures) before considering the feature done. This step needs live network access and is not part of the automated task loop — run it manually, or skip it if this environment has no outbound network access, and note the skip in the final report.
