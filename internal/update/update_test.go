package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpsdm/dev/internal/config"
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
		{"v0.10.0", "v0.9.0", true},            // numeric, not lexical, comparison
		{"v0.2.1-3-gabc1234", "v0.2.0", false}, // non-clean candidate
		{"v0.2.1", "not-a-version", false},     // non-clean current
	}
	for _, tc := range cases {
		if got := NewerThan(tc.candidate, tc.current); got != tc.want {
			t.Errorf("NewerThan(%q, %q) = %v, want %v", tc.candidate, tc.current, got, tc.want)
		}
	}
}

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
