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
	"time"

	"github.com/jpsdm/dev/internal/config"
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
