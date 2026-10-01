# Self-Update System Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `dev update` command that checks GitHub for the latest release and installs it in place of the running binary, plus a throttled passive notice shown on `dev version` and every other command when a newer release exists.

**Architecture:** A new `internal/update` package owns talking to GitHub (a `baseURL`-overridable client, matching `internal/runtime/node`/`java`'s established pattern) and the download-verify-swap mechanics, reusing this project's existing checksum-first downloader and atomic extractor rather than reimplementing either. `cmd/root.go`'s existing `PersistentPreRunE` (already home to the install-location gate) gains a sibling `PersistentPostRunE` that computes and prints the passive notice for every command — including `dev version`, which gets its own notice for free through this one shared hook rather than a separate code path.

**Tech Stack:** Go stdlib (`net/http`, `encoding/json`, `archive/tar`, `archive/zip`, `os`), this project's own `internal/downloader` (checksum-verified HTTP download) and `internal/installer` (atomic, zip-slip-protected extraction) — no new third-party dependencies.

**Spec:** docs/superpowers/specs/2026-09-29-self-update-design.md

## Global Constraints

- `dev update` always confirms `[y/N]` before replacing anything — no `--yes`/silent mode in this plan.
- Passive checks (behind `dev version`'s notice and the other-command notice) make at most one real GitHub API call per 24 hours (`update.CheckInterval`), cached in `config.json`. `dev update` itself always fetches fresh, ignoring the cache.
- A network failure during a **passive** check is silently swallowed — never a visible error, delay, or crash in an otherwise-successful command. A network failure during `dev update`'s own **explicit** fetch is a real, surfaced error (the user asked for it directly).
- `DEV_NO_UPDATE_CHECK=1` disables the passive check entirely — no cache read, no network call.
- A non-clean version (a `git describe`-style local build, not a plain `vMAJOR.MINOR.PATCH` tag) never triggers a check, a notice, or a successful `dev update` run.
- The passive check only runs when `platform.RunningFromDevHome()` reports `true` — suggesting an update (or checking for one) before `dev` is even installed doesn't make sense.
- No pinning/downgrading — `dev update` only ever moves to "latest." No version argument.
- No new third-party dependencies — version comparison is hand-rolled (`vMAJOR.MINOR.PATCH`, three integers), not a semver library.
- Every commit ends with `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.

## Review Focus

- **A malformed or incomplete GitHub release** (missing the archive for the current OS/arch, or missing `checksums.txt`, or the archive missing the expected `dev`/`dev.exe` file inside it) must produce a clear, specific error naming what's missing — never a panic, a confusing generic failure, or a silent no-op.
- **GitHub unreachable or rate-limited**: during a passive check, silent and harmless (per Global Constraints); during `dev update`'s own explicit check, a clear, real error — a reasonable person running `dev update` and seeing nothing happen would assume it's broken, not that it's "working as designed."
- **A locally-built `dev`** (this project's own `make build` output, or any `go test`/`go run` invocation) must never attempt a real update, never show a notice, and `dev update` on one must refuse with a clear message rather than silently succeeding, silently doing nothing, or crashing on an unparseable version string.
- **The leftover `dev.old` after a swap** (expected to fail removal often on Windows, since a running process can't delete its own executable image) must use this session's already-established "safe to delete, not an error" framing — a reasonable person seeing "Could not remove ... Acesso negado" right after a successful update would think something broke.
- **Re-running `dev update` immediately after a successful update** must not have the passive notice immediately claim an update is available for the version just installed — the cache must be updated with the version `dev update` actually moved to, not left stale.

---

### Task 1: `internal/update` — GitHub client and version comparison

**Files:**
- Create: `internal/update/update.go`
- Test: `internal/update/update_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `type Client struct` (unexported `baseURL` field), `func NewClient() *Client`, `func NewClientWithBaseURL(baseURL string) *Client` (exported specifically so other packages' tests — `cmd/update_test.go`, `cmd/root_test.go` — can point a `Client` at a local `httptest.Server`; production code always uses `NewClient`), `type LatestRelease struct { TagName string; Assets []ReleaseAsset }`, `type ReleaseAsset struct { Name, BrowserDownloadURL string }`, `func (c *Client) FetchLatest(ctx context.Context) (*LatestRelease, error)`, `func IsCleanVersion(v string) bool`, `func NewerThan(candidate, current string) bool`.

- [ ] **Step 1: Write the failing tests**

Create `internal/update/update_test.go`:

```go
package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchLatest_ParsesRealResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/jpsdm/dev/releases/latest" {
			t.Errorf("request path = %q, want %q", r.URL.Path, "/repos/jpsdm/dev/releases/latest")
		}
		_ = json.NewEncoder(w).Encode(LatestRelease{
			TagName: "v0.2.1",
			Assets: []ReleaseAsset{
				{Name: "dev_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.com/dev_linux_amd64.tar.gz"},
				{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/checksums.txt"},
			},
		})
	}))
	defer server.Close()

	c := NewClientWithBaseURL(server.URL)
	got, err := c.FetchLatest(context.Background())
	if err != nil {
		t.Fatalf("FetchLatest() returned error: %v", err)
	}
	if got.TagName != "v0.2.1" {
		t.Errorf("TagName = %q, want %q", got.TagName, "v0.2.1")
	}
	if len(got.Assets) != 2 {
		t.Fatalf("Assets = %v, want 2 entries", got.Assets)
	}
	if got.Assets[0].Name != "dev_linux_amd64.tar.gz" || got.Assets[0].BrowserDownloadURL != "https://example.com/dev_linux_amd64.tar.gz" {
		t.Errorf("Assets[0] = %+v, want the dev_linux_amd64.tar.gz entry", got.Assets[0])
	}
}

func TestFetchLatest_NonOKStatusReturnsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := NewClientWithBaseURL(server.URL)
	_, err := c.FetchLatest(context.Background())
	if err == nil {
		t.Fatal("FetchLatest() returned nil error for a 404 response")
	}
}

func TestIsCleanVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		v    string
		want bool
	}{
		{"v0.2.1", true},
		{"v0.10.0", true},
		{"v0.2.1-3-gabc1234", false},
		{"v0.2.1-dirty", false},
		{"v0.2.1-3-gabc1234-dirty", false},
		{"0.2.1", false},
		{"v0.2", false},
		{"v0.2.1.4", false},
		{"vX.Y.Z", false},
		{"", false},
		{"dev", false},
	}
	for _, tc := range cases {
		if got := IsCleanVersion(tc.v); got != tc.want {
			t.Errorf("IsCleanVersion(%q) = %v, want %v", tc.v, got, tc.want)
		}
	}
}

func TestNewerThan(t *testing.T) {
	t.Parallel()
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"v0.2.1", "v0.2.0", true},
		{"v0.3.0", "v0.2.9", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.2.0", "v0.2.0", false},
		{"v0.1.0", "v0.2.0", false},
		{"v0.10.0", "v0.9.0", true},  // numeric, not lexical, comparison
		{"v0.2.1-3-gabc1234", "v0.2.0", false}, // non-clean candidate
		{"v0.2.1", "not-a-version", false},     // non-clean current
	}
	for _, tc := range cases {
		if got := NewerThan(tc.candidate, tc.current); got != tc.want {
			t.Errorf("NewerThan(%q, %q) = %v, want %v", tc.candidate, tc.current, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/update/... -v`
Expected: FAIL — the package doesn't exist yet (`no such file or directory` / undefined symbols).

- [ ] **Step 3: Write the implementation**

Create `internal/update/update.go`:

```go
// Package update checks GitHub for newer dev releases and applies
// them in place of the currently running binary.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const apiBaseURL = "https://api.github.com"

// Client fetches the latest published GitHub release for dev.
// baseURL is overridable in tests — the same pattern
// internal/runtime/node and internal/runtime/java already use for
// their own upstream APIs.
type Client struct {
	baseURL string
}

// NewClient returns a Client pointed at the real GitHub API.
func NewClient() *Client {
	return &Client{baseURL: apiBaseURL}
}

// NewClientWithBaseURL returns a Client pointed at baseURL instead of
// the real GitHub API. Exported so other packages' tests (cmd's) can
// point dev's commands at a local httptest.Server; production code
// should use NewClient.
func NewClientWithBaseURL(baseURL string) *Client {
	return &Client{baseURL: baseURL}
}

// LatestRelease is the subset of GitHub's release JSON this package
// actually uses.
type LatestRelease struct {
	TagName string         `json:"tag_name"`
	Assets  []ReleaseAsset `json:"assets"`
}

// ReleaseAsset is one downloadable file attached to a release.
type ReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// FetchLatest calls GET /repos/jpsdm/dev/releases/latest — the
// endpoint that returns the latest published, non-draft,
// non-prerelease release, matching this project's own "GoReleaser
// drafts it, you publish it manually" release flow.
func (c *Client) FetchLatest(ctx context.Context) (*LatestRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/repos/jpsdm/dev/releases/latest", nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching latest release: unexpected status %s", resp.Status)
	}

	var release LatestRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("parsing latest release response: %w", err)
	}
	return &release, nil
}

// IsCleanVersion reports whether v looks like a plain
// "vMAJOR.MINOR.PATCH" release tag (no git-describe suffix like
// "-3-gabc1234" or "-dirty", and no missing "v" prefix). A version
// that isn't clean is a local/dev build — the update system never
// checks or notifies for one.
func IsCleanVersion(v string) bool {
	_, _, _, ok := parseVersion(v)
	return ok
}

// NewerThan reports whether candidate is a newer clean release
// version than current. Both must be clean (see IsCleanVersion) — a
// non-clean candidate or current is treated as "not newer",
// defensively, rather than guessing.
func NewerThan(candidate, current string) bool {
	cMajor, cMinor, cPatch, ok := parseVersion(candidate)
	if !ok {
		return false
	}
	curMajor, curMinor, curPatch, ok := parseVersion(current)
	if !ok {
		return false
	}
	if cMajor != curMajor {
		return cMajor > curMajor
	}
	if cMinor != curMinor {
		return cMinor > curMinor
	}
	return cPatch > curPatch
}

// parseVersion parses a "vMAJOR.MINOR.PATCH" string into its three
// integer components. Anything else (missing "v" prefix, a
// git-describe suffix, a non-numeric component, the wrong number of
// components) reports ok=false.
func parseVersion(v string) (major, minor, patch int, ok bool) {
	if !strings.HasPrefix(v, "v") {
		return 0, 0, 0, false
	}
	parts := strings.Split(v[1:], ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, 0, 0, false
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/update/... -v`
Expected: PASS (all tests).

- [ ] **Step 5: Run `go vet`, `gofmt`, and `go build`**

Run: `go vet ./internal/update/... && gofmt -l internal/update/ && go build ./...`
Expected: no output from `vet`/`gofmt`; build succeeds.

- [ ] **Step 6: Commit**

```bash
git add internal/update/update.go internal/update/update_test.go
git commit -m "$(cat <<'EOF'
Add the GitHub release client and version comparison for self-update

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Caching — `internal/config`'s `UpdateCheck` field and `internal/update`'s `CachedNotice`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/update/update.go`
- Modify: `internal/update/update_test.go`

**Interfaces:**
- Consumes: `Client`, `Client.FetchLatest`, `IsCleanVersion`, `NewerThan` (Task 1).
- Produces: `config.UpdateCheck{ LastChecked time.Time; LatestVersion string }` (new field on `config.Config`), `update.CheckInterval` (a `time.Duration` const), `func (c *Client) CachedNotice(ctx context.Context, current string, cached config.UpdateCheck) (notice string, updated *config.UpdateCheck)`.

- [ ] **Step 1: Write the failing config test**

Append to `internal/config/config_test.go`:

```go
func TestSaveThenLoad_RoundTripsUpdateCheck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	when := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	want := &Config{UpdateCheck: UpdateCheck{LastChecked: when, LatestVersion: "v0.2.1"}}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save() returned error: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if !got.UpdateCheck.LastChecked.Equal(when) {
		t.Errorf("UpdateCheck.LastChecked = %v, want %v", got.UpdateCheck.LastChecked, when)
	}
	if got.UpdateCheck.LatestVersion != "v0.2.1" {
		t.Errorf("UpdateCheck.LatestVersion = %q, want %q", got.UpdateCheck.LatestVersion, "v0.2.1")
	}
}
```

Add `"time"` to `internal/config/config_test.go`'s import block.

- [ ] **Step 2: Run the config test to verify it fails**

Run: `go test ./internal/config/... -run TestSaveThenLoad_RoundTripsUpdateCheck -v`
Expected: FAIL — `undefined: UpdateCheck` (compile error).

- [ ] **Step 3: Add the `UpdateCheck` field**

In `internal/config/config.go`, add `"time"` to the import block, then add the new type and field:

```go
// UpdateCheck records the last time dev checked GitHub for a newer
// release, and what it found — read and written by internal/update's
// CachedNotice so passive checks (behind `dev version`'s notice and
// the notice shown on every other command) don't hit GitHub's API on
// every single invocation.
type UpdateCheck struct {
	LastChecked   time.Time `json:"last_checked"`
	LatestVersion string    `json:"latest_version"`
}

// Config is the full contents of DEV_HOME/config/config.json.
type Config struct {
	Workspace   WorkspaceConfig `json:"workspace"`
	UpdateCheck UpdateCheck     `json:"update_check"`
}
```

(Replace the existing `Config` struct definition with this — note `UpdateCheck` has no `omitempty`: it's a non-pointer struct field, and Go's `encoding/json` `omitempty` never actually omits a zero-value struct, only zero-value primitives/maps/slices/pointers, so writing `omitempty` here would be misleading — it wouldn't do anything.)

- [ ] **Step 4: Run the config test to verify it passes**

Run: `go test ./internal/config/... -v`
Expected: PASS (the whole package's suite, including the pre-existing `TestSaveThenLoad_RoundTrips` and `TestDefaultPath_UsesConfigDir`).

- [ ] **Step 5: Write the failing `CachedNotice` tests**

Append to `internal/update/update_test.go`:

```go
func TestCachedNotice_FirstCheckFetchesAndCaches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(LatestRelease{TagName: "v0.3.0"})
	}))
	defer server.Close()
	c := NewClientWithBaseURL(server.URL)

	notice, updated := c.CachedNotice(context.Background(), "v0.2.0", config.UpdateCheck{})

	if updated == nil {
		t.Fatal("updated = nil, want a fresh cache entry — cached.LastChecked was zero (never checked)")
	}
	if updated.LatestVersion != "v0.3.0" {
		t.Errorf("updated.LatestVersion = %q, want %q", updated.LatestVersion, "v0.3.0")
	}
	if updated.LastChecked.IsZero() {
		t.Error("updated.LastChecked is zero, want it set to the time of this check")
	}
	want := "A newer version is available: v0.3.0 (run 'dev update')"
	if notice != want {
		t.Errorf("notice = %q, want %q", notice, want)
	}
}

func TestCachedNotice_FreshCacheSkipsNetworkCall(t *testing.T) {
	t.Parallel()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	c := NewClientWithBaseURL(server.URL)

	cached := config.UpdateCheck{LastChecked: time.Now().Add(-1 * time.Hour), LatestVersion: "v0.2.1"}
	notice, updated := c.CachedNotice(context.Background(), "v0.2.0", cached)

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("network calls = %d, want 0 (cache is within CheckInterval)", got)
	}
	if updated != nil {
		t.Errorf("updated = %+v, want nil (nothing to persist — cache was fresh)", updated)
	}
	want := "A newer version is available: v0.2.1 (run 'dev update')"
	if notice != want {
		t.Errorf("notice = %q, want %q", notice, want)
	}
}

// TestCachedNotice_StaleCacheRefetches mutates the package-level now
// var directly, so — like every other test in this codebase that
// overrides a shared seam (see cmd/root_test.go's platform.Executable
// pattern) — it deliberately does not call t.Parallel().
func TestCachedNotice_StaleCacheRefetches(t *testing.T) {
	fixedNow := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	original := now
	now = func() time.Time { return fixedNow }
	t.Cleanup(func() { now = original })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(LatestRelease{TagName: "v0.3.0"})
	}))
	defer server.Close()
	c := NewClientWithBaseURL(server.URL)

	cached := config.UpdateCheck{LastChecked: fixedNow.Add(-25 * time.Hour), LatestVersion: "v0.1.0"}
	notice, updated := c.CachedNotice(context.Background(), "v0.2.0", cached)

	if updated == nil {
		t.Fatal("updated = nil, want a fresh cache entry — cached.LastChecked was 25h old (stale)")
	}
	if updated.LatestVersion != "v0.3.0" {
		t.Errorf("updated.LatestVersion = %q, want %q", updated.LatestVersion, "v0.3.0")
	}
	if !updated.LastChecked.Equal(fixedNow) {
		t.Errorf("updated.LastChecked = %v, want %v", updated.LastChecked, fixedNow)
	}
	want := "A newer version is available: v0.3.0 (run 'dev update')"
	if notice != want {
		t.Errorf("notice = %q, want %q", notice, want)
	}
}

func TestCachedNotice_NoNoticeWhenNotNewer(t *testing.T) {
	t.Parallel()
	cached := config.UpdateCheck{LastChecked: time.Now(), LatestVersion: "v0.2.0"}
	c := NewClientWithBaseURL("http://unused.invalid")

	notice, updated := c.CachedNotice(context.Background(), "v0.2.0", cached)

	if notice != "" {
		t.Errorf("notice = %q, want empty (already on the cached latest version)", notice)
	}
	if updated != nil {
		t.Errorf("updated = %+v, want nil (cache was fresh, nothing to persist)", updated)
	}
}

func TestCachedNotice_NonCleanCurrentSkipsEntirely(t *testing.T) {
	t.Parallel()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()
	c := NewClientWithBaseURL(server.URL)

	notice, updated := c.CachedNotice(context.Background(), "v0.2.1-3-gabc1234-dirty", config.UpdateCheck{})

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("network calls = %d, want 0 (current is a non-clean/local-build version)", got)
	}
	if notice != "" {
		t.Errorf("notice = %q, want empty", notice)
	}
	if updated != nil {
		t.Errorf("updated = %+v, want nil", updated)
	}
}

func TestCachedNotice_NetworkFailureReturnsEmptyNotFatal(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	c := NewClientWithBaseURL(server.URL)

	notice, updated := c.CachedNotice(context.Background(), "v0.2.0", config.UpdateCheck{})

	if notice != "" {
		t.Errorf("notice = %q, want empty on a fetch failure", notice)
	}
	if updated != nil {
		t.Errorf("updated = %+v, want nil on a fetch failure (nothing to persist)", updated)
	}
}
```

Add `"sync/atomic"`, `"time"`, and `"github.com/jpsdm/dev/internal/config"` to `internal/update/update_test.go`'s import block.

- [ ] **Step 6: Run tests to verify they fail**

Run: `go test ./internal/update/... -run TestCachedNotice -v`
Expected: FAIL — `undefined: CachedNotice`, `undefined: now`, `undefined: config` (compile errors).

- [ ] **Step 7: Implement `CachedNotice`**

Append to `internal/update/update.go`. Add `"time"`, `"fmt"` (already imported), and `"github.com/jpsdm/dev/internal/config"` to the import block:

```go
// now is overridable in tests — the same seam pattern this codebase
// already uses for platform.Executable.
var now = time.Now

// CheckInterval is how long a cached check result stays valid before
// a passive caller (dev version's notice, the notice on every other
// command) triggers a real GitHub call again. dev update always
// bypasses this by calling FetchLatest directly instead.
const CheckInterval = 24 * time.Hour

// CachedNotice returns the version-available notice to show (empty if
// there's nothing to report) given cached's prior check result, and,
// when the cache was stale enough to trigger a real check, an updated
// *config.UpdateCheck for the caller to persist (nil when nothing
// changed — the cache was still fresh, or the fetch failed). Returns
// ("", nil) immediately, with no network call, when current isn't a
// clean release version (see IsCleanVersion) — a local/dev build never
// checks or notifies. A network failure during the fetch is not
// treated as an error: CachedNotice returns ("", nil) unchanged, so a
// passive caller never has anything to report or fail over.
func (c *Client) CachedNotice(ctx context.Context, current string, cached config.UpdateCheck) (notice string, updated *config.UpdateCheck) {
	if !IsCleanVersion(current) {
		return "", nil
	}

	latest := cached.LatestVersion
	var fresh *config.UpdateCheck
	if cached.LastChecked.IsZero() || now().Sub(cached.LastChecked) >= CheckInterval {
		release, err := c.FetchLatest(ctx)
		if err != nil {
			return "", nil
		}
		latest = release.TagName
		fresh = &config.UpdateCheck{LastChecked: now(), LatestVersion: latest}
	}

	if latest != "" && NewerThan(latest, current) {
		notice = fmt.Sprintf("A newer version is available: %s (run 'dev update')", latest)
	}
	return notice, fresh
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/update/... -v`
Expected: PASS (the whole package's suite).

- [ ] **Step 9: Run `go vet`, `gofmt`, `go build`, and the full repo suite**

Run: `go vet ./... && gofmt -l . && go build ./... && go test ./...`
Expected: all clean.

- [ ] **Step 10: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/update/update.go internal/update/update_test.go
git commit -m "$(cat <<'EOF'
Add throttled update-check caching via config.json

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `internal/update/apply.go` — download, verify, and swap the binary

**Files:**
- Create: `internal/update/apply.go`
- Create: `internal/update/apply_test.go`

**Interfaces:**
- Consumes: `LatestRelease`, `ReleaseAsset` (Task 1); `internal/downloader.Download(ctx context.Context, url, destPath, wantSHA256 string) error` (existing); `internal/installer.ExtractAtomic(archivePath, destDir string) error` (existing); `internal/cliutil.Fsuccess(w io.Writer, format string, args ...any)` (existing).
- Produces: `func ApplyUpdate(ctx context.Context, out io.Writer, release *LatestRelease, devHome string) error` — the only symbol Task 4 (`cmd/update.go`) consumes from this file.

- [ ] **Step 1: Write the failing tests**

Create `internal/update/apply_test.go`:

```go
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func buildTestTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("writing tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("writing tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	return buf.Bytes()
}

func buildTestZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, err := zw.Create(name)
	if err != nil {
		t.Fatalf("creating zip entry: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("writing zip content: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip writer: %v", err)
	}
	return buf.Bytes()
}

func TestArchiveName_WindowsGetsZip(t *testing.T) {
	t.Parallel()
	if got := archiveName("windows", "amd64"); got != "dev_windows_amd64.zip" {
		t.Errorf("archiveName(windows, amd64) = %q, want %q", got, "dev_windows_amd64.zip")
	}
}

func TestArchiveName_OtherPlatformsGetTarGz(t *testing.T) {
	t.Parallel()
	if got := archiveName("linux", "arm64"); got != "dev_linux_arm64.tar.gz" {
		t.Errorf("archiveName(linux, arm64) = %q, want %q", got, "dev_linux_arm64.tar.gz")
	}
}

func TestBinaryName_WindowsGetsExeSuffix(t *testing.T) {
	t.Parallel()
	if got := binaryName("windows"); got != "dev.exe" {
		t.Errorf("binaryName(windows) = %q, want %q", got, "dev.exe")
	}
}

func TestBinaryName_OtherPlatformsGetPlainName(t *testing.T) {
	t.Parallel()
	if got := binaryName("darwin"); got != "dev" {
		t.Errorf("binaryName(darwin) = %q, want %q", got, "dev")
	}
}

func TestFindAsset_ReturnsMatchingAsset(t *testing.T) {
	t.Parallel()
	assets := []ReleaseAsset{
		{Name: "dev_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.com/a"},
		{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/b"},
	}
	got, err := findAsset(assets, "checksums.txt")
	if err != nil {
		t.Fatalf("findAsset() returned error: %v", err)
	}
	if got.BrowserDownloadURL != "https://example.com/b" {
		t.Errorf("findAsset() = %+v, want the checksums.txt entry", got)
	}
}

func TestFindAsset_MissingAssetListsWhatWasThere(t *testing.T) {
	t.Parallel()
	assets := []ReleaseAsset{{Name: "dev_linux_amd64.tar.gz"}}
	_, err := findAsset(assets, "dev_windows_amd64.zip")
	if err == nil {
		t.Fatal("findAsset() returned nil error for a missing asset")
	}
	if !strings.Contains(err.Error(), "dev_linux_amd64.tar.gz") {
		t.Errorf("error = %q, want it to list the assets that were actually present", err.Error())
	}
}

func TestChecksumFor_ExtractsMatchingLine(t *testing.T) {
	t.Parallel()
	body := "abc123  dev_linux_amd64.tar.gz\ndef456  dev_windows_amd64.zip\n"
	got, err := checksumFor(body, "dev_windows_amd64.zip")
	if err != nil {
		t.Fatalf("checksumFor() returned error: %v", err)
	}
	if got != "def456" {
		t.Errorf("checksumFor() = %q, want %q", got, "def456")
	}
}

func TestChecksumFor_NoMatchingLineReturnsError(t *testing.T) {
	t.Parallel()
	_, err := checksumFor("abc123  dev_linux_amd64.tar.gz\n", "dev_windows_amd64.zip")
	if err == nil {
		t.Fatal("checksumFor() returned nil error for a missing entry")
	}
}

// TestApplyUpdate_DownloadsVerifiesAndSwapsTheBinary is a real,
// end-to-end test: an httptest.Server serves a genuine archive
// (matching what GoReleaser actually produces for the current
// runtime.GOOS/GOARCH) containing a "dev"/"dev.exe" file, plus a
// checksums.txt whose entry matches that archive's real sha256.
// ApplyUpdate must download both, verify the checksum, extract, and
// swap the real on-disk devHome/dev in place.
func TestApplyUpdate_DownloadsVerifiesAndSwapsTheBinary(t *testing.T) {
	binName := binaryName(runtime.GOOS)
	archive := archiveName(runtime.GOOS, runtime.GOARCH)
	newContent := []byte("new dev binary content")

	var archiveData []byte
	if runtime.GOOS == "windows" {
		archiveData = buildTestZip(t, binName, newContent)
	} else {
		archiveData = buildTestTarGz(t, binName, newContent)
	}
	sum := sha256.Sum256(archiveData)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archive)

	mux := http.NewServeMux()
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archiveData)
	})
	mux.HandleFunc("/checksums", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	release := &LatestRelease{
		TagName: "v9.9.9",
		Assets: []ReleaseAsset{
			{Name: archive, BrowserDownloadURL: server.URL + "/archive"},
			{Name: "checksums.txt", BrowserDownloadURL: server.URL + "/checksums"},
		},
	}

	devHome := t.TempDir()
	currentPath := filepath.Join(devHome, binName)
	if err := os.WriteFile(currentPath, []byte("old dev binary content"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	if err := ApplyUpdate(context.Background(), &out, release, devHome); err != nil {
		t.Fatalf("ApplyUpdate() returned error: %v", err)
	}

	got, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("reading %s: %v", currentPath, err)
	}
	if string(got) != string(newContent) {
		t.Errorf("content at %s = %q, want %q (the new binary)", currentPath, got, newContent)
	}

	if oldContent, err := os.ReadFile(currentPath + ".old"); err == nil {
		// Best-effort removal: either it's gone (removed successfully)
		// or still there with the ORIGINAL content — never corrupted.
		if string(oldContent) != "old dev binary content" {
			t.Errorf(".old content = %q, want the original %q", oldContent, "old dev binary content")
		}
	}
}

func TestApplyUpdate_MissingArchiveAssetReturnsClearError(t *testing.T) {
	t.Parallel()
	release := &LatestRelease{
		TagName: "v9.9.9",
		Assets:  []ReleaseAsset{{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/checksums.txt"}},
	}
	devHome := t.TempDir()

	err := ApplyUpdate(context.Background(), &bytes.Buffer{}, release, devHome)
	if err == nil {
		t.Fatal("ApplyUpdate() returned nil error for a release missing the archive for this platform")
	}
	if !strings.Contains(err.Error(), archiveName(runtime.GOOS, runtime.GOARCH)) {
		t.Errorf("error = %q, want it to name the missing asset", err.Error())
	}
}

// TestReportLeftover_UsesReassuringNotAlarmingWording pins the exact
// message shown when the old binary can't be removed after the swap —
// a real, expected case on Windows, where a running process can't
// delete its own executable image. Extracted into its own tiny
// function specifically so this wording is directly testable without
// needing to force a real removal failure (devHome must stay writable
// for the rename/swap steps themselves, so making it read-only to
// simulate a stuck .old file isn't an option here the way
// cmd/setup.go's installRelocation test could use a separate,
// independently-read-only extraction directory).
func TestReportLeftover_UsesReassuringNotAlarmingWording(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	reportLeftover(&out, "/tmp/dev.old")

	got := out.String()
	if !strings.Contains(got, "safe to delete") {
		t.Errorf("output = %q, want a reassuring \"safe to delete\" message", got)
	}
	if strings.Contains(got, "Could not remove") {
		t.Errorf("output = %q, want it not to use alarming \"Could not remove\" wording", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/update/... -run 'TestArchiveName|TestBinaryName|TestFindAsset|TestChecksumFor|TestApplyUpdate|TestReportLeftover' -v`
Expected: FAIL — `undefined: archiveName`, `undefined: binaryName`, `undefined: findAsset`, `undefined: checksumFor`, `undefined: ApplyUpdate`, `undefined: reportLeftover` (compile errors).

- [ ] **Step 3: Write the implementation**

Create `internal/update/apply.go`:

```go
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

// binaryName returns the dev binary's filename inside the archive —
// "dev" everywhere except Windows, which gets "dev.exe".
func binaryName(goos string) string {
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
// devHome/dev.exe). Does not touch $DEV_HOME/bin's shims — the caller
// (cmd/update.go) refreshes those afterward, the same way dev setup
// already does.
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
	if err := downloader.Download(ctx, archiveAsset.BrowserDownloadURL, archivePath, wantSHA256); err != nil {
		return fmt.Errorf("downloading %s: %w", archive, err)
	}

	extractDir := filepath.Join(scratchDir, "extracted")
	if err := installer.ExtractAtomic(archivePath, extractDir); err != nil {
		return fmt.Errorf("extracting %s: %w", archive, err)
	}

	binName := binaryName(runtime.GOOS)
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/update/... -v`
Expected: PASS (the whole package's suite).

- [ ] **Step 5: Run `go vet`, `gofmt`, `go build`, the full repo suite, and the Windows cross-compile**

Run: `go vet ./... && gofmt -l . && go build ./... && go test ./... && GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=amd64 go vet ./...`
Expected: all clean. The Windows cross-compile/vet is the only verification available for this task's Windows-specific behavior in this environment — `TestApplyUpdate_DownloadsVerifiesAndSwapsTheBinary` runs and passes on Linux/macOS, proving the download-verify-extract-swap pipeline end to end there, but whether `os.Rename`ing a *currently-executing* binary's file behaves the same way on real Windows cannot be verified here. Note this explicitly in your report — it needs a manual check on a real Windows machine after this plan ships (see the plan's closing manual-verification note).

- [ ] **Step 6: Commit**

```bash
git add internal/update/apply.go internal/update/apply_test.go
git commit -m "$(cat <<'EOF'
Add the download-verify-swap mechanics for dev update

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `cmd/update.go` — the `dev update` command

**Files:**
- Create: `cmd/update.go`
- Create: `cmd/update_test.go`

**Interfaces:**
- Consumes: `update.NewClient`, `update.NewClientWithBaseURL`, `update.IsCleanVersion`, `update.NewerThan`, `(*update.Client).FetchLatest`, `update.ApplyUpdate` (Tasks 1 and 3); `config.DefaultPath`, `config.Load`, `config.Save`, `config.ErrNotFound`, `config.UpdateCheck` (existing + Task 2); `platform.DevHome` (existing); `shell.Confirm` (existing); `installShims(cmd *cobra.Command) error` (existing, defined in `cmd/setup.go`, unchanged); `cliutil.Fsuccess`, `cliutil.Fstep` (existing); `Version` (the package-level var in `cmd/root.go`).
- Produces: `var newUpdateClient = update.NewClient` — a package-level var Task 5 (`cmd/root.go`) also reads, the same test-substitution pattern this package already uses for `promptWorkspace`/`langManager`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/update_test.go`:

```go
package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jpsdm/dev/internal/config"
	"github.com/jpsdm/dev/internal/update"
)

// buildTestTarGz builds a minimal real .tar.gz containing one file —
// used to fabricate a release archive an httptest.Server can serve so
// `dev update`'s real ApplyUpdate call succeeds end to end.
func buildTestTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("writing tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("writing tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	return buf.Bytes()
}

// stubUpdateServer serves a fake /repos/jpsdm/dev/releases/latest
// response naming tagName, plus a real tar.gz archive (containing
// archiveContent as the "dev" binary) and a matching checksums.txt —
// enough for ApplyUpdate to succeed against it for real. Linux/macOS
// only (a real archive for the current platform is always a .tar.gz
// there); see TestUpdateCommand_ConfirmedSwapsAndRefreshesShims for
// why the corresponding test skips on Windows.
func stubUpdateServer(t *testing.T, tagName string, archiveContent []byte) *httptest.Server {
	t.Helper()
	archive := fmt.Sprintf("dev_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archiveData := buildTestTarGz(t, "dev", archiveContent)
	sum := sha256.Sum256(archiveData)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archive)

	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/repos/jpsdm/dev/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		release := update.LatestRelease{
			TagName: tagName,
			Assets: []update.ReleaseAsset{
				{Name: archive, BrowserDownloadURL: server.URL + "/archive"},
				{Name: "checksums.txt", BrowserDownloadURL: server.URL + "/checksums"},
			},
		}
		_ = json.NewEncoder(w).Encode(release)
	})
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archiveData)
	})
	mux.HandleFunc("/checksums", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// withVersion temporarily overrides the package-level Version var
// (cmd/root.go), restoring it via t.Cleanup — the same
// package-var-override-with-cleanup pattern this package already uses
// for platform.Executable and promptWorkspace.
func withVersion(t *testing.T, v string) {
	t.Helper()
	original := Version
	Version = v
	t.Cleanup(func() { Version = original })
}

func TestUpdateCommand_NonCleanVersionRefuses(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	// Version is left at its test-default "dev" — never a clean release
	// tag under `go test`.

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"update"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("`dev update` returned nil error for a non-clean (local build) version")
	}
}

// TestUpdateCommand_RefusedOutsideDevHome pins the spec's own
// acceptance criterion that `dev update` requires already being
// installed — it is not in cmd/root.go's PersistentPreRunE exemption
// list (only "setup", "version", "help" are), so this is exercising
// existing, unmodified gating code, not new logic this task adds —
// but the spec names this scenario explicitly, so it gets its own
// test rather than being left to an inference from a differently-named
// command's coverage.
func TestUpdateCommand_RefusedOutsideDevHome(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	outsideDevHome(t)

	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{"update"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("`dev update` returned nil error when run outside $DEV_HOME")
	}
}

// TestUpdateCommand_NetworkFailureReturnsRealError pins the
// distinction Global Constraints draws between the passive check
// (silent on failure) and dev update's own explicit check (a real,
// surfaced error — the user asked for this directly).
func TestUpdateCommand_NetworkFailureReturnsRealError(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	withVersion(t, "v0.2.0")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{"update"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("`dev update` returned nil error when GitHub was unreachable — an explicit check must surface a real error, unlike the passive check")
	}
}

func TestUpdateCommand_AlreadyLatestPrintsMessage(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	withVersion(t, "v0.2.0")

	server := stubUpdateServer(t, "v0.2.0", nil)
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"update"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev update` returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Already on the latest version") {
		t.Errorf("output = %q, want it to say already on the latest version", out.String())
	}
}

func TestUpdateCommand_DeclinedConfirmationMakesNoChanges(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	withVersion(t, "v0.2.0")
	devBinary := filepath.Join(devHome, "dev")
	if err := os.WriteFile(devBinary, []byte("current dev binary"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	server := stubUpdateServer(t, "v0.3.0", []byte("new dev binary"))
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("n\n"))
	rootCmd.SetArgs([]string{"update"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev update` returned error: %v", err)
	}
	if !strings.Contains(out.String(), "No changes made.") {
		t.Errorf("output = %q, want it to confirm no changes were made", out.String())
	}
	got, err := os.ReadFile(devBinary)
	if err != nil {
		t.Fatalf("reading %s: %v", devBinary, err)
	}
	if string(got) != "current dev binary" {
		t.Errorf("dev binary content = %q, want it unchanged after declining", got)
	}
}

// TestUpdateCommand_ConfirmedSwapsAndRefreshesShims is the real
// end-to-end happy path. Skipped on Windows: stubUpdateServer only
// ever builds a .tar.gz archive (this test's fixed shape), which only
// matches the real runtime.GOOS-driven archive name ApplyUpdate
// expects when GOOS isn't windows — see internal/update/apply_test.go
// for the equivalent test that does build a real .zip and is exercised
// per-platform via runtime.GOOS there instead.
func TestUpdateCommand_ConfirmedSwapsAndRefreshesShims(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stubUpdateServer only builds a .tar.gz; internal/update/apply_test.go covers the real per-platform archive format")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	withVersion(t, "v0.2.0")
	devBinary := filepath.Join(devHome, "dev")
	if err := os.WriteFile(devBinary, []byte("current dev binary"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	server := stubUpdateServer(t, "v0.3.0", []byte("new dev binary"))
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"update"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev update` returned error: %v", err)
	}

	got, err := os.ReadFile(devBinary)
	if err != nil {
		t.Fatalf("reading %s: %v", devBinary, err)
	}
	if string(got) != "new dev binary" {
		t.Errorf("dev binary content = %q, want the new content", got)
	}
	for _, name := range []string{"node", "npm", "npx", "java", "javac"} {
		shimPath := filepath.Join(devHome, "bin", name)
		if _, err := os.Stat(shimPath); err != nil {
			t.Errorf("expected shim %s to be refreshed: %v", shimPath, err)
		}
	}
	if !strings.Contains(out.String(), "Updated to v0.3.0") {
		t.Errorf("output = %q, want it to confirm the update", out.String())
	}

	cfgPath, err := config.DefaultPath()
	if err != nil {
		t.Fatalf("config.DefaultPath() returned error: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load() returned error: %v", err)
	}
	if cfg.UpdateCheck.LatestVersion != "v0.3.0" {
		t.Errorf("persisted UpdateCheck.LatestVersion = %q, want %q (so the passive notice doesn't immediately re-fire)", cfg.UpdateCheck.LatestVersion, "v0.3.0")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/... -run TestUpdateCommand -v`
Expected: FAIL — `undefined: newUpdateClient` (and no `update` subcommand registered, so `rootCmd.Execute()` with `args=["update"]` fails with an "unknown command" error rather than the expected behavior).

- [ ] **Step 3: Write the implementation**

Create `cmd/update.go`:

```go
package cmd

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/config"
	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/shell"
	"github.com/jpsdm/dev/internal/update"
)

// newUpdateClient is a package-level var so tests can point dev
// update (and cmd/root.go's passive notice, see Task 5) at a fake
// server — the same substitution pattern this package already uses
// for promptWorkspace/langManager. Production code always gets the
// real GitHub API via update.NewClient.
var newUpdateClient = update.NewClient

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check for and install the latest dev release",
	RunE: func(cmd *cobra.Command, args []string) error {
		current := Version
		if !update.IsCleanVersion(current) {
			return fmt.Errorf("dev update isn't available for a local build (%s isn't a released version)", current)
		}

		client := newUpdateClient()
		release, err := client.FetchLatest(cmd.Context())
		if err != nil {
			return err
		}

		if !update.NewerThan(release.TagName, current) {
			cliutil.Fsuccess(cmd.OutOrStdout(), "Already on the latest version (%s).", current)
			return nil
		}

		fmt.Fprintf(cmd.OutOrStdout(), "%s → %s\n\n", current, release.TagName)
		confirmed, err := shell.Confirm("Update now? [y/N] ", cmd.InOrStdin(), cmd.OutOrStdout())
		if err != nil {
			return err
		}
		if !confirmed {
			cliutil.Fstep(cmd.OutOrStdout(), "No changes made.")
			return nil
		}

		devHome, err := platform.DevHome()
		if err != nil {
			return err
		}
		if err := update.ApplyUpdate(cmd.Context(), cmd.OutOrStdout(), release, devHome); err != nil {
			return err
		}
		if err := installShims(cmd); err != nil {
			return err
		}

		cfgPath, err := config.DefaultPath()
		if err != nil {
			return err
		}
		cfg, err := config.Load(cfgPath)
		if err != nil && !errors.Is(err, config.ErrNotFound) {
			return err
		}
		cfg.UpdateCheck = config.UpdateCheck{LastChecked: time.Now(), LatestVersion: release.TagName}
		if err := config.Save(cfgPath, cfg); err != nil {
			return err
		}

		cliutil.Fsuccess(cmd.OutOrStdout(), "Updated to %s.", release.TagName)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)
}
```

(`config.Load`'s existing contract already returns a valid, non-nil zero-value `*Config` alongside `ErrNotFound` — see `internal/config/config.go` — so `cfg.UpdateCheck = ...` below the `errors.Is` check is always safe, first-run or not.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/... -v -run TestUpdateCommand`
Expected: PASS (all 4 tests, or 3 + 1 skip on Windows).

- [ ] **Step 5: Run the full `cmd` package suite, `go vet`, `gofmt`, `go build`, and the Windows cross-compile**

Run: `go test ./cmd/... -v && go vet ./... && gofmt -l . && go build ./... && GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=amd64 go vet ./...`
Expected: all clean — the full `cmd` package run (not just the new tests) matters here, since this task adds a new command to the shared `rootCmd` tree that every other `cmd` test also exercises via `rootCmd.Execute()`.

- [ ] **Step 6: Commit**

```bash
git add cmd/update.go cmd/update_test.go
git commit -m "$(cat <<'EOF'
Add the dev update command

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `cmd/root.go` — the passive notice on every command (and `dev version`, for free)

**Files:**
- Modify: `cmd/root.go`
- Modify: `cmd/root_test.go`

**Interfaces:**
- Consumes: `newUpdateClient` (Task 4, `cmd/update.go`); `(*update.Client).CachedNotice` (Task 2); `config.DefaultPath`, `config.Load`, `config.Save`, `config.ErrNotFound` (existing); `platform.RunningFromDevHome` (existing); `cliutil.Verbosef` (existing); `Version` (existing package var).
- Produces: nothing consumed by a later task — this is the last code task in this plan.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/root_test.go`. These reuse `outsideDevHome(t)` (already defined in this file) and the same `stubUpdateServer`/`update.NewClientWithBaseURL` pattern Task 4 established, so first check: `cmd/update_test.go` (from Task 4) already defines `stubUpdateServer` in package `cmd` — reuse it directly here, no redefinition needed.

```go
func TestPersistentPostRunE_PrintsNoticeToStderrForOrdinaryCommand(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	withVersion(t, "v0.2.0")

	server := stubUpdateServer(t, "v0.3.0", nil)
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"lang", "current"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev lang current` returned error: %v", err)
	}

	want := "A newer version is available: v0.3.0 (run 'dev update')"
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
	}
	if strings.Contains(stdout.String(), want) {
		t.Error("stdout unexpectedly contains the update notice — it belongs on stderr for an ordinary command")
	}
}

func TestPersistentPostRunE_PrintsNoticeToStdoutForVersionCommand(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	withVersion(t, "v0.2.0")

	server := stubUpdateServer(t, "v0.3.0", nil)
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"version"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev version` returned error: %v", err)
	}

	want := "A newer version is available: v0.3.0 (run 'dev update')"
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q (dev version's own notice)", stdout.String(), want)
	}
	if strings.Contains(stderr.String(), want) {
		t.Error("stderr unexpectedly contains the update notice too — dev version must not print it twice")
	}
}

func TestPersistentPostRunE_NoNoticeWhenAlreadyLatest(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	withVersion(t, "v0.2.0")

	server := stubUpdateServer(t, "v0.2.0", nil)
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"lang", "current"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev lang current` returned error: %v", err)
	}
	if strings.Contains(stderr.String(), "newer version") {
		t.Errorf("stderr = %q, want no update notice when already on the latest version", stderr.String())
	}
}

func TestPersistentPostRunE_SkipsForUpdateCommand(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	withVersion(t, "v0.2.0")

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(update.LatestRelease{TagName: "v0.2.0"})
	}))
	defer server.Close()
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"update"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev update` returned error: %v", err)
	}
	// dev update's own RunE makes exactly one FetchLatest call already
	// (Task 4) — PersistentPostRunE must not make a second one.
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("network calls = %d, want exactly 1 (dev update's own check, not a second one from PersistentPostRunE)", got)
	}
}

func TestPersistentPostRunE_SkipsWhenEnvVarSet(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("DEV_NO_UPDATE_CHECK", "1")
	withVersion(t, "v0.2.0")

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"lang", "current"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev lang current` returned error: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("network calls = %d, want 0 (DEV_NO_UPDATE_CHECK is set)", got)
	}
	if strings.Contains(stderr.String(), "newer version") {
		t.Errorf("stderr = %q, want no notice when DEV_NO_UPDATE_CHECK is set", stderr.String())
	}
}

func TestPersistentPostRunE_SkipsWhenNotInstalled(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	outsideDevHome(t)
	withVersion(t, "v0.2.0")

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"version"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev version` returned error: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("network calls = %d, want 0 (not running from $DEV_HOME)", got)
	}
}

func TestPersistentPostRunE_SkipsForNonCleanVersion(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	// Version left at its test-default "dev" — non-clean.

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"lang", "current"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev lang current` returned error: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("network calls = %d, want 0 (Version is a non-clean local-build string)", got)
	}
}
```

Add `"encoding/json"`, `"net/http"`, `"net/http/httptest"`, `"sync/atomic"`, and `"github.com/jpsdm/dev/internal/update"` to `cmd/root_test.go`'s import block (`bytes`, `errors`, `fmt`, `io`, `path/filepath`, `strings`, `testing`, `huh`, `cobra`, `cliutil`, `platform` are already there).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/... -run TestPersistentPostRunE -v`
Expected: FAIL — every test's assertion fails (no `PersistentPostRunE` exists yet, so no notice is ever printed and no network call ever happens for any of them; the "no notice"/"no calls" assertions in the skip-condition tests happen to pass vacuously, but the ones expecting an actual notice — the first three — fail).

- [ ] **Step 3: Write the implementation**

In `cmd/root.go`, add `"errors"` (already imported), `"os"` (already imported), and `"github.com/jpsdm/dev/internal/config"` + `"github.com/jpsdm/dev/internal/update"` to the import block. Add `PersistentPostRunE` to `rootCmd` (alongside the existing `PersistentPreRunE`) and the two new helper functions:

```go
var rootCmd = &cobra.Command{
	Use:   "dev",
	Short: "dev manages language runtimes, workspaces and dev environments",
	Long: "dev is a small, fast Development Environment Manager: it installs " +
		"and switches language/runtime versions, and organizes a project " +
		"workspace.",
	Version:       versionString(),
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// ... unchanged, exactly as it is today ...
	},
	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		notice := updateNoticeIfDue(cmd)
		if notice == "" {
			return nil
		}
		if cmd.Name() == "version" {
			// dev version's own notice: printed as part of its own
			// output (stdout), not off to the side on stderr like every
			// other command's notice — a user running `dev version`
			// wants this directly in what they asked to see.
			fmt.Fprintln(cmd.OutOrStdout(), notice)
		} else {
			fmt.Fprintln(cmd.ErrOrStderr(), notice)
		}
		return nil
	},
}

// updateNoticeIfDue returns the one-line "a newer version is
// available" notice if one is due for cmd's invocation, or "" if
// there's nothing to show — either because nothing newer exists, or
// because the check itself was skipped. Skipped entirely, before
// touching config or network, when: this is `dev update` itself (it
// already does its own fresh check, see cmd/update.go); DEV_NO_UPDATE_CHECK
// is set; platform.RunningFromDevHome() reports false (suggesting an
// update before dev is even installed doesn't make sense — this also
// covers `setup` and `help` for free, since neither runs from
// $DEV_HOME on a fresh, not-yet-installed copy); or Version isn't a
// clean release version (a local/dev build). Every other failure along
// the way (reading/writing config.json, a network error inside
// CachedNotice) is logged only at --verbose and never surfaces as a
// command failure — an update check must never visibly break an
// otherwise-successful command.
func updateNoticeIfDue(cmd *cobra.Command) string {
	if cmd.Name() == "update" {
		return ""
	}
	if os.Getenv("DEV_NO_UPDATE_CHECK") != "" {
		return ""
	}
	if ok, _, err := platform.RunningFromDevHome(); err != nil || !ok {
		return ""
	}

	cfgPath, err := config.DefaultPath()
	if err != nil {
		cliutil.Verbosef("update check: %v", err)
		return ""
	}
	cfg, err := config.Load(cfgPath)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		cliutil.Verbosef("update check: %v", err)
		return ""
	}

	client := newUpdateClient()
	notice, updated := client.CachedNotice(cmd.Context(), Version, cfg.UpdateCheck)
	if updated != nil {
		cfg.UpdateCheck = *updated
		if err := config.Save(cfgPath, cfg); err != nil {
			cliutil.Verbosef("update check: saving cache: %v", err)
		}
	}
	return notice
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/... -v`
Expected: PASS — the whole `cmd` package's suite, not just the new tests (this task adds a hook that runs after every command, so a regression could show up in any pre-existing test rather than a new one).

- [ ] **Step 5: Run `go vet`, `gofmt`, `go build`, the full repo suite, and the Windows cross-compile**

Run: `go vet ./... && gofmt -l . && go build ./... && go test ./... && GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=amd64 go vet ./...`
Expected: all clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/root.go cmd/root_test.go
git commit -m "$(cat <<'EOF'
Show an update notice on dev version and every other command

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: `README.md` — document `dev update` and the notice

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: nothing (documentation only).
- Produces: nothing consumed by a later task — this is the last task in this plan.

- [ ] **Step 1: Add a "Staying up to date" section**

Add a new section to `README.md`, after the existing "Releases" section (find it by its `## Releases` heading) and before "## Usage":

```markdown
## Staying up to date

    dev update    # check GitHub for a newer release and install it (asks for confirmation)

`dev version` and every other command also print a one-line notice when a newer release
is available — at most once every 24 hours (cached in `config.json`), never on a network
failure, and never for a locally-built `dev` (one whose version isn't a plain `vX.Y.Z`
release tag). Set `DEV_NO_UPDATE_CHECK=1` to disable this entirely.
```

- [ ] **Step 2: Verify the rendered structure**

Run: `grep -n "^## " README.md`
Expected: `## Staying up to date` appears once, between `## Releases` and `## Usage`, and no other heading was accidentally duplicated or removed.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
Document dev update and the version-check notice

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Post-plan manual verification (not a task — for the user, on a real Windows machine)

This plan's automated tasks cannot verify one thing at runtime: whether `os.Rename`ing
`$DEV_HOME\dev.exe` (the *currently-executing* binary's own file) aside, then renaming
the new binary into its place, behaves the same on real Windows as the equivalent
operation on an arbitrary, non-running file — which is all Tasks 3-5's tests can
exercise here. Verify by hand once this plan ships:

```powershell
# From an installed dev (a real prior release, not a local build):
dev version                  # note the current version
dev update                   # confirm y; should report "Updated to vX.Y.Z."
dev version                  # should now show the new version, no "newer version" notice

# Confirm the shims were refreshed too:
node --version                # (once a Node version is installed/activated) still works

# Confirm the leftover, if any, reads as reassuring, not alarming:
dir $env:DEV_HOME\dev.exe.old  # may or may not exist — if it does, the update's own
                                 # output should have said "safe to delete", not
                                 # "could not remove"
```
