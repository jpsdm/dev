package python

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/filesystem"
	devruntime "github.com/jpsdm/dev/internal/runtime"
)

var _ devruntime.Runtime = (*Python)(nil)

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
	_ = triple
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

func TestResolveRelease_PicksGreatestPatchOnMinorRegardlessOfOrder(t *testing.T) {
	triple := ownTriple(t)
	rel := release{
		Assets: []releaseAsset{
			{Name: "SHA256SUMS", BrowserDownloadURL: "http://example.invalid/SHA256SUMS"},
			{Name: "cpython-3.12.13+20260929-" + triple + "-install_only.tar.gz", BrowserDownloadURL: "http://example.invalid/old.tar.gz"},
			{Name: "cpython-3.12.14+20260929-" + triple + "-install_only.tar.gz", BrowserDownloadURL: "http://example.invalid/new.tar.gz"},
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
		t.Errorf("resolveRelease().FullVersion = %q, want %q (the greater patch, regardless of asset order)", got.FullVersion, "3.12.14")
	}
	if got.DownloadURL != "http://example.invalid/new.tar.gz" {
		t.Errorf("resolveRelease().DownloadURL = %q, want the greater patch's URL", got.DownloadURL)
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

func TestExtractStrippingTopLevel_ReplacesExistingDestDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	destDir := filepath.Join(dir, "dest")

	archive1 := filepath.Join(dir, "v1.tar.gz")
	if err := os.WriteFile(archive1, buildFlatTarGz(t, map[string]string{"python-v1/bin/python3": "v1"}), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := extractStrippingTopLevel(archive1, destDir); err != nil {
		t.Fatalf("extractStrippingTopLevel() first call returned error: %v", err)
	}

	archive2 := filepath.Join(dir, "v2.tar.gz")
	if err := os.WriteFile(archive2, buildFlatTarGz(t, map[string]string{"python-v2/bin/python3": "v2"}), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := extractStrippingTopLevel(archive2, destDir); err != nil {
		t.Fatalf("extractStrippingTopLevel() second call returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "bin", "python3"))
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

	// Two top-level entries instead of the single directory
	// python-build-standalone archives always contain.
	badArchive := filepath.Join(dir, "bad.tar.gz")
	if err := os.WriteFile(badArchive, buildFlatTarGz(t, map[string]string{
		"one/file.txt": "a",
		"two/file.txt": "b",
	}), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := extractStrippingTopLevel(badArchive, destDir)
	if err == nil {
		t.Fatal("extractStrippingTopLevel() returned nil error for an archive with more than one top-level entry")
	}
	if !strings.Contains(err.Error(), "unexpected archive layout") {
		t.Errorf("extractStrippingTopLevel() error = %q, want it to mention the unexpected layout", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "keep.txt"))
	if err != nil {
		t.Fatalf("keep.txt missing after failed extractStrippingTopLevel: %v", err)
	}
	if string(got) != "keep me" {
		t.Errorf("keep.txt content = %q, want %q (destDir should be untouched)", got, "keep me")
	}
}

func TestInstall_NewerPatchReplacesExistingInstall(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	triple := ownTriple(t)

	type releaseFixture struct {
		filename string
		bytes    []byte
		checksum string
	}
	build := func(fullVersion, content string) releaseFixture {
		filename := "cpython-" + fullVersion + "+20260929-" + triple + "-install_only.tar.gz"
		archiveBytes := buildPythonArchive(t, map[string]string{"bin/python3": content})
		sum := sha256.Sum256(archiveBytes)
		return releaseFixture{filename: filename, bytes: archiveBytes, checksum: hex.EncodeToString(sum[:])}
	}
	old := build("3.12.13", "old binary")
	newRel := build("3.12.14", "new binary")

	var mu sync.Mutex
	current := old
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		c := current
		mu.Unlock()
		fmt.Fprintf(w, `{"tag_name":"20260929","assets":[
			{"name":"SHA256SUMS","browser_download_url":"%[1]s/SHA256SUMS"},
			{"name":%[2]q,"browser_download_url":"%[1]s/%[2]s"}
		]}`, "http://"+r.Host, c.filename)
	})
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		c := current
		mu.Unlock()
		fmt.Fprintf(w, "%s  %s\n", c.checksum, c.filename)
	})
	mux.HandleFunc("/"+old.filename, func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write(old.bytes); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})
	mux.HandleFunc("/"+newRel.filename, func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write(newRel.bytes); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := &Python{baseURL: server.URL}
	if err := p.Install(context.Background(), "3.12"); err != nil {
		t.Fatalf("installing 3.12.13 returned error: %v", err)
	}

	mu.Lock()
	current = newRel
	mu.Unlock()

	if err := p.Install(context.Background(), "3.12"); err != nil {
		t.Fatalf("installing 3.12.14 returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(devHome, "versions", "python", "3.12", "bin", "python3"))
	if err != nil {
		t.Fatalf("reading installed binary: %v", err)
	}
	if string(got) != "new binary" {
		t.Errorf("installed binary content = %q, want %q (should have replaced the old patch)", got, "new binary")
	}
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

func TestBinDirForOS_WindowsUsesVersionRoot(t *testing.T) {
	t.Parallel()
	got := binDirForOS("windows", "/versiondir")
	want := "/versiondir"
	if got != want {
		t.Errorf(`binDirForOS("windows", ...) = %q, want %q`, got, want)
	}
}

func TestBinDirForOS_UnixUsesBinSubdir(t *testing.T) {
	t.Parallel()
	for _, osName := range []string{"linux", "darwin"} {
		got := binDirForOS(osName, "/versiondir")
		want := filepath.Join("/versiondir", "bin")
		if got != want {
			t.Errorf("binDirForOS(%q, ...) = %q, want %q", osName, got, want)
		}
	}
}

func TestBinDir_ReturnsRealDirectory(t *testing.T) {
	t.Parallel()
	p := New()
	got, err := p.BinDir("/some/version/dir")
	if err != nil {
		t.Fatalf("BinDir() returned error on this test's platform: %v", err)
	}
	if got == "" {
		t.Error("BinDir() returned empty string")
	}
}
