package bun

import (
	"archive/zip"
	"bytes"
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

	"github.com/jpsdm/dev/internal/cliutil"
)

func TestMapBunOS_RejectsUnsupportedValue(t *testing.T) {
	t.Parallel()
	if _, err := mapBunOS("plan9"); err == nil {
		t.Fatal(`mapBunOS("plan9") returned nil error, want non-nil`)
	}
}

func TestMapBunArch_MapsArm64ToAarch64(t *testing.T) {
	t.Parallel()
	got, err := mapBunArch("arm64")
	if err != nil {
		t.Fatalf("mapBunArch(arm64) returned error: %v", err)
	}
	if got != "aarch64" {
		t.Errorf("mapBunArch(arm64) = %q, want %q", got, "aarch64")
	}
	if got, _ := mapBunArch("amd64"); got != "x64" {
		t.Errorf("mapBunArch(amd64) = %q, want %q", got, "x64")
	}
}

func TestMapBunArch_RejectsUnsupportedValue(t *testing.T) {
	t.Parallel()
	if _, err := mapBunArch("z80"); err == nil {
		t.Fatal(`mapBunArch("z80") returned nil error, want non-nil`)
	}
}

func TestParseTag(t *testing.T) {
	t.Parallel()
	line, patch, ok := parseTag("bun-v1.4.12")
	if !ok || line != "1.4" || patch != 12 {
		t.Errorf(`parseTag("bun-v1.4.12") = (%q, %d, %v), want ("1.4", 12, true)`, line, patch, ok)
	}
	for _, bad := range []string{"", "canary", "bun-v1.4", "bun-v1.4.2-canary", "v1.4.2", "bun-v1.x.2"} {
		if _, _, ok := parseTag(bad); ok {
			t.Errorf("parseTag(%q) ok = true, want false", bad)
		}
	}
}

func TestCompareLines_OrdersNumericallyNotLexically(t *testing.T) {
	t.Parallel()
	if compareLines("1.10", "1.9") <= 0 {
		t.Error(`compareLines("1.10", "1.9") <= 0, want > 0`)
	}
	if compareLines("1.0", "1.4") >= 0 {
		t.Error(`compareLines("1.0", "1.4") >= 0, want < 0`)
	}
}

// releasesJSON builds a GitHub-releases-shaped fixture with one asset
// for this test host's platform per tag, using sums[tag] as its digest.
func releasesJSON(t *testing.T, baseURL string, entries ...fixtureRelease) string {
	t.Helper()
	osName, err := bunOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	archName, err := bunArch()
	if err != nil {
		t.Skipf("unsupported arch for this test: %v", err)
	}
	name := fmt.Sprintf("bun-%s-%s.zip", osName, archName)

	var parts []string
	for _, e := range entries {
		assetName := name
		if e.assetName != "" {
			assetName = e.assetName
		}
		digest := e.digest
		if digest == "" && !e.noDigest {
			digest = "sha256:" + strings.Repeat("a", 64)
		}
		parts = append(parts, fmt.Sprintf(`{"tag_name":%q,"draft":%t,"prerelease":%t,"assets":[
			{"name":"bun-%s-%s-profile.zip","browser_download_url":"%s/dl/%s/profile.zip","digest":"sha256:ffff"},
			{"name":%q,"browser_download_url":"%s/dl/%s/%s","digest":%q}
		]}`, e.tag, e.draft, e.prerelease, osName, archName, baseURL, e.tag, assetName, baseURL, e.tag, assetName, digest))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

type fixtureRelease struct {
	tag        string
	digest     string
	assetName  string
	noDigest   bool
	draft      bool
	prerelease bool
}

func serveJSON(t *testing.T, body func(baseURL string) string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body(server.URL))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestListRemoteVersions_NewestLineFirstSkippingUnusableReleases(t *testing.T) {
	t.Parallel()
	server := serveJSON(t, func(base string) string {
		return releasesJSON(t, base,
			fixtureRelease{tag: "bun-v1.3.1"},
			fixtureRelease{tag: "bun-v1.4.2"},
			fixtureRelease{tag: "bun-v1.4.1"},
			fixtureRelease{tag: "bun-v1.10.0"},
			fixtureRelease{tag: "canary"},
			fixtureRelease{tag: "bun-v1.9.0", prerelease: true},
			fixtureRelease{tag: "bun-v1.8.0", draft: true},
			fixtureRelease{tag: "bun-v1.7.0", noDigest: true},
		)
	})
	b := &Bun{baseURL: server.URL}

	versions, err := b.ListRemoteVersions(context.Background())
	if err != nil {
		t.Fatalf("ListRemoteVersions() returned error: %v", err)
	}
	var got []string
	for _, v := range versions {
		got = append(got, v.Name)
	}
	want := "1.10,1.4,1.3"
	if strings.Join(got, ",") != want {
		t.Errorf("ListRemoteVersions() = %v, want %s", got, want)
	}
}

func TestListRemoteVersions_ServerErrorReturnsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	b := &Bun{baseURL: server.URL}
	if _, err := b.ListRemoteVersions(context.Background()); err == nil {
		t.Fatal("ListRemoteVersions() returned nil error for a 500 response")
	}
}

func TestResolveRelease_PicksGreatestPatchAndParsesDigest(t *testing.T) {
	t.Parallel()
	server := serveJSON(t, func(base string) string {
		return releasesJSON(t, base,
			fixtureRelease{tag: "bun-v1.4.2", digest: "sha256:" + strings.Repeat("b", 64)},
			fixtureRelease{tag: "bun-v1.4.10", digest: "sha256:" + strings.Repeat("c", 64)},
			fixtureRelease{tag: "bun-v1.4.9", digest: "sha256:" + strings.Repeat("d", 64)},
		)
	})
	b := &Bun{baseURL: server.URL}

	rel, err := b.resolveRelease(context.Background(), "1.4")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if rel.Version != "1.4.10" {
		t.Errorf("rel.Version = %q, want %q (numeric, not lexical, patch comparison)", rel.Version, "1.4.10")
	}
	if rel.SHA256 != strings.Repeat("c", 64) {
		t.Errorf("rel.SHA256 = %q, want the digest without its sha256: prefix", rel.SHA256)
	}
	if !strings.HasPrefix(rel.URL, server.URL+"/dl/bun-v1.4.10/") {
		t.Errorf("rel.URL = %q, want it under %s/dl/bun-v1.4.10/", rel.URL, server.URL)
	}
}

func TestResolveRelease_UnknownLineReturnsEmptyReleaseNoError(t *testing.T) {
	t.Parallel()
	server := serveJSON(t, func(base string) string {
		return releasesJSON(t, base, fixtureRelease{tag: "bun-v1.4.2"})
	})
	b := &Bun{baseURL: server.URL}

	rel, err := b.resolveRelease(context.Background(), "9.9")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if rel.Version != "" {
		t.Errorf("rel = %+v, want zero value", rel)
	}
}

func TestResolveRelease_IgnoresAssetsWithoutSHA256Digest(t *testing.T) {
	t.Parallel()
	server := serveJSON(t, func(base string) string {
		return releasesJSON(t, base,
			fixtureRelease{tag: "bun-v1.4.2", digest: "md5:abcd"},
			fixtureRelease{tag: "bun-v1.4.1", noDigest: true},
		)
	})
	b := &Bun{baseURL: server.URL}

	rel, err := b.resolveRelease(context.Background(), "1.4")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if rel.Version != "" {
		t.Errorf("rel = %+v, want zero value (nothing to verify a download against)", rel)
	}
}

func TestResolveRelease_RejectsPathInjectionShapedAssetName(t *testing.T) {
	t.Parallel()
	osName, _ := bunOS()
	archName, _ := bunArch()
	evil := fmt.Sprintf("../bun-%s-%s.zip", osName, archName)
	server := serveJSON(t, func(base string) string {
		return releasesJSON(t, base, fixtureRelease{tag: "bun-v1.4.2", assetName: evil})
	})
	b := &Bun{baseURL: server.URL}

	// An asset whose name isn't the exact expected one must never
	// match, and — were it to — its path-unsafe name must be refused.
	rel, err := b.resolveRelease(context.Background(), "1.4")
	if err == nil && rel.Version != "" {
		t.Fatalf("resolveRelease() accepted path-injection-shaped asset name: %+v", rel)
	}
}

func TestResolveRelease_RejectsAssetWithoutDownloadURL(t *testing.T) {
	t.Parallel()
	osName, _ := bunOS()
	archName, _ := bunArch()
	server := serveJSON(t, func(string) string {
		return fmt.Sprintf(`[{"tag_name":"bun-v1.4.2","assets":[{"name":"bun-%s-%s.zip","browser_download_url":"","digest":"sha256:%s"}]}]`,
			osName, archName, strings.Repeat("a", 64))
	})
	b := &Bun{baseURL: server.URL}

	if _, err := b.resolveRelease(context.Background(), "1.4"); err == nil {
		t.Fatal("resolveRelease() returned nil error for an asset with no download URL")
	}
}

func buildBunZip(t *testing.T, topLevelDir, exeName, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: topLevelDir + "/" + exeName, Method: zip.Deflate}
	hdr.SetMode(0o755)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatalf("creating zip entry: %v", err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatalf("writing zip content: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
	return buf.Bytes()
}

func exeNameFor(osName string) string {
	if osName == "windows" {
		return "bun.exe"
	}
	return "bun"
}

// hostOS returns the OS vocabulary extractBun is called with in
// production. Tests that go through the real symlink/copy step use the
// host's own value: creating a Unix-style symlink on a Windows host
// needs privileges a test run can't assume.
func hostOS(t *testing.T) string {
	t.Helper()
	osName, err := bunOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	return osName
}

func TestExtractBun_PlacesBinaryUnderBinAndAddsBunx(t *testing.T) {
	t.Parallel()
	osName := hostOS(t)
	exe := exeNameFor(osName)
	dir := t.TempDir()
	archive := filepath.Join(dir, "bun.zip")
	if err := os.WriteFile(archive, buildBunZip(t, "bun-top", exe, "fake bun"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	destDir := filepath.Join(dir, "dest")

	if err := extractBun(archive, destDir, osName); err != nil {
		t.Fatalf("extractBun() returned error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(destDir, "bin", exe))
	if err != nil || string(got) != "fake bun" {
		t.Fatalf("bin/%s = %q, %v; want %q", exe, got, err, "fake bun")
	}
	bunx := "bunx"
	if osName == "windows" {
		bunx = "bunx.exe"
	}
	if got, err := os.ReadFile(filepath.Join(destDir, "bin", bunx)); err != nil || string(got) != "fake bun" {
		t.Errorf("bin/%s = %q, %v; want it to resolve to bun's content", bunx, got, err)
	}
}

func TestLinkBunx_UnixCreatesRelativeSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs elevated rights on Windows")
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "bun"), []byte("x"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := linkBunx(binDir, "linux"); err != nil {
		t.Fatalf("linkBunx() returned error: %v", err)
	}
	target, err := os.Readlink(filepath.Join(binDir, "bunx"))
	if err != nil || target != "bun" {
		t.Errorf("bunx -> %q, %v; want relative link to %q", target, err, "bun")
	}
}

func TestLinkBunx_LeavesAnExistingBunxAlone(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	for _, f := range []string{"bun", "bunx", "bun.exe", "bunx.exe"} {
		if err := os.WriteFile(filepath.Join(binDir, f), []byte(f), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	for _, osName := range []string{"linux", "windows"} {
		if err := linkBunx(binDir, osName); err != nil {
			t.Errorf("linkBunx(%s) returned error: %v", osName, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(binDir, "bunx")); string(got) != "bunx" {
		t.Errorf("existing bunx was overwritten: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(binDir, "bunx.exe")); string(got) != "bunx.exe" {
		t.Errorf("existing bunx.exe was overwritten: %q", got)
	}
}

func TestExtractBun_WindowsLayoutCopiesBunx(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := filepath.Join(dir, "bun-windows-x64.zip")
	if err := os.WriteFile(archive, buildBunZip(t, "bun-windows-x64", "bun.exe", "fake bun.exe"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	destDir := filepath.Join(dir, "dest")

	if err := extractBun(archive, destDir, "windows"); err != nil {
		t.Fatalf("extractBun() returned error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(destDir, "bin", "bunx.exe"))
	if err != nil || string(got) != "fake bun.exe" {
		t.Errorf("bin/bunx.exe = %q, %v; want a copy of bun.exe", got, err)
	}
}

func TestExtractBun_MissingBinaryLeavesDestDirUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	destDir := filepath.Join(dir, "dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "keep.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	archive := filepath.Join(dir, "bad.zip")
	if err := os.WriteFile(archive, buildBunZip(t, "bun-top", "not-bun", "x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := extractBun(archive, destDir, hostOS(t)); err == nil {
		t.Fatal("extractBun() returned nil error for an archive with no bun binary")
	}
	if got, err := os.ReadFile(filepath.Join(destDir, "keep.txt")); err != nil || string(got) != "keep me" {
		t.Errorf("destDir was modified despite the error: %q, %v", got, err)
	}
}

func TestExtractBun_ReplacesExistingDestDir(t *testing.T) {
	t.Parallel()
	osName := hostOS(t)
	exe := exeNameFor(osName)
	dir := t.TempDir()
	destDir := filepath.Join(dir, "dest")
	for _, content := range []string{"v1", "v2"} {
		archive := filepath.Join(dir, content+".zip")
		if err := os.WriteFile(archive, buildBunZip(t, "bun-top", exe, content), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := extractBun(archive, destDir, osName); err != nil {
			t.Fatalf("extractBun(%s) returned error: %v", content, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(destDir, "bin", exe)); string(got) != "v2" {
		t.Errorf("bin/%s = %q, want %q", exe, got, "v2")
	}
	if _, err := os.Stat(destDir + ".old"); !os.IsNotExist(err) {
		t.Error("backup directory left behind after a successful replace")
	}
}

func TestListInstalledVersions_EmptyWhenNoneInstalled(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	got, err := New().ListInstalledVersions()
	if err != nil {
		t.Fatalf("ListInstalledVersions() returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListInstalledVersions() = %+v, want empty", got)
	}
}

func TestListInstalledVersions_SkipsHiddenAndBackupDirsAndFiles(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	base := filepath.Join(devHome, "versions", "bun")
	for _, d := range []string{"1.4", "1.3", ".tmp-bun-extract-123", "1.2.old"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "stray-file"), nil, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := New().ListInstalledVersions()
	if err != nil {
		t.Fatalf("ListInstalledVersions() returned error: %v", err)
	}
	names := map[string]bool{}
	for _, v := range got {
		names[v.Name] = true
	}
	if len(got) != 2 || !names["1.4"] || !names["1.3"] {
		t.Errorf("ListInstalledVersions() = %+v, want exactly 1.4 and 1.3", got)
	}
}

func TestCurrentVersion_NilWhenNoneActiveAndReadsMarker(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	b := New()
	if got, err := b.CurrentVersion(); err != nil || got != nil {
		t.Fatalf("CurrentVersion() = %+v, %v; want nil, nil", got, err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "current"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devHome, "current", "bun"), []byte("1.4\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := b.CurrentVersion()
	if err != nil || got == nil || got.Name != "1.4" {
		t.Errorf("CurrentVersion() = %+v, %v; want Name=1.4", got, err)
	}
}

// installFixture serves a resolvable Bun 1.4.2 for this host and returns
// the provider, the request counter for the archive endpoint, and the
// archive's checksum.
func installFixture(t *testing.T, archive []byte, checksum string) (*Bun, *int) {
	t.Helper()
	osName, err := bunOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	archName, err := bunArch()
	if err != nil {
		t.Skipf("unsupported arch for this test: %v", err)
	}
	name := fmt.Sprintf("bun-%s-%s.zip", osName, archName)
	downloads := 0

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/releases", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"tag_name":"bun-v1.4.2","assets":[{"name":%q,"browser_download_url":"%s/dl/%s","digest":"sha256:%s"}]}]`,
			name, server.URL, name, checksum)
	})
	mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, r *http.Request) {
		downloads++
		_, _ = w.Write(archive)
	})
	return &Bun{baseURL: server.URL}, &downloads
}

func TestInstall_DownloadsVerifiesAndExtracts(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	osName, _ := bunOS()
	archive := buildBunZip(t, "bun-top", exeNameFor(osName), "fake bun")
	sum := sha256.Sum256(archive)
	b, _ := installFixture(t, archive, hex.EncodeToString(sum[:]))

	if err := b.Install(context.Background(), "1.4"); err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(devHome, "versions", "bun", "1.4", "bin", exeNameFor(osName)))
	if err != nil || string(got) != "fake bun" {
		t.Fatalf("installed binary = %q, %v; want %q", got, err, "fake bun")
	}
	marker, _ := os.ReadFile(filepath.Join(devHome, "versions", "bun", "1.4", ".dev-release"))
	if string(marker) != "1.4.2" {
		t.Errorf(".dev-release = %q, want %q", marker, "1.4.2")
	}
}

func TestInstall_ChecksumMismatchFailsAndInstallsNothing(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	osName, _ := bunOS()
	archive := buildBunZip(t, "bun-top", exeNameFor(osName), "fake bun")
	b, _ := installFixture(t, archive, strings.Repeat("0", 64))

	if err := b.Install(context.Background(), "1.4"); err == nil {
		t.Fatal("Install() returned nil error despite a checksum mismatch")
	}
	if _, err := os.Stat(filepath.Join(devHome, "versions", "bun", "1.4")); !os.IsNotExist(err) {
		t.Error("version directory exists after a failed, checksum-mismatched install")
	}
}

func TestInstall_AlreadyInstalledSkipsRedownload(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	osName, _ := bunOS()
	archive := buildBunZip(t, "bun-top", exeNameFor(osName), "fake bun")
	sum := sha256.Sum256(archive)
	b, downloads := installFixture(t, archive, hex.EncodeToString(sum[:]))

	for i := 0; i < 2; i++ {
		if err := b.Install(context.Background(), "1.4"); err != nil {
			t.Fatalf("Install() #%d returned error: %v", i+1, err)
		}
	}
	if *downloads != 1 {
		t.Errorf("archive downloaded %d times, want 1", *downloads)
	}
}

func TestInstall_OfflineFallbackUsesCachedReleaseWhenResolveFails(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	dest := filepath.Join(devHome, "versions", "bun", "1.4")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, ".dev-release"), []byte("1.4.1"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	var buf bytes.Buffer
	origStdout := cliutil.Stdout
	cliutil.Stdout = &buf
	defer func() { cliutil.Stdout = origStdout }()

	if err := (&Bun{baseURL: server.URL}).Install(context.Background(), "1.4"); err != nil {
		t.Fatalf("Install() returned error, want offline fallback success: %v", err)
	}
	if !strings.Contains(buf.String(), "1.4.1") {
		t.Errorf("output = %q, want it to mention the cached release 1.4.1", buf.String())
	}
}

func TestInstall_NoOfflineFallbackWhenNothingCached(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if err := (&Bun{baseURL: server.URL}).Install(context.Background(), "1.4"); err == nil {
		t.Fatal("Install() returned nil error with nothing cached and the server down")
	}
}

func TestInstall_UnknownLineFailsWithoutCreatingDirectory(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	server := serveJSON(t, func(base string) string {
		return releasesJSON(t, base, fixtureRelease{tag: "bun-v1.4.2"})
	})

	if err := (&Bun{baseURL: server.URL}).Install(context.Background(), "9.9"); err == nil {
		t.Fatal("Install() returned nil error for an unknown line")
	}
	if _, err := os.Stat(filepath.Join(devHome, "versions", "bun", "9.9")); !os.IsNotExist(err) {
		t.Error("version directory created for an unknown line")
	}
}

func TestInstall_RejectsPathInjectionShapedName(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, name := range []string{"", ".", "..", "../x", `a\b`} {
		if err := New().Install(context.Background(), name); err == nil {
			t.Errorf("Install(%q) returned nil error", name)
		}
	}
}

func TestUninstall_RemovesDirectoryAndClearsActiveMarker(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "bun", "1.4"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	b := New()
	if err := b.Activate("1.4"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}
	if err := b.Uninstall("1.4"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(devHome, "versions", "bun", "1.4")); !os.IsNotExist(err) {
		t.Error("version directory still exists after Uninstall")
	}
	if got, _ := b.CurrentVersion(); got != nil {
		t.Errorf("CurrentVersion() = %+v after uninstalling the active version, want nil", got)
	}
}

func TestUninstall_LeavesOtherActiveVersionAlone(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, v := range []string{"1.4", "1.3"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "bun", v), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	b := New()
	if err := b.Activate("1.3"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}
	if err := b.Uninstall("1.4"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}
	if got, _ := b.CurrentVersion(); got == nil || got.Name != "1.3" {
		t.Errorf("CurrentVersion() = %+v, want 1.3 untouched", got)
	}
}

func TestUninstallAndActivate_NotInstalledReturnError(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	b := New()
	if err := b.Uninstall("1.4"); err == nil {
		t.Error("Uninstall() of a missing version returned nil error")
	}
	if err := b.Activate("1.4"); err == nil {
		t.Error("Activate() of a missing version returned nil error")
	}
}

func TestBinaryPathForOS(t *testing.T) {
	t.Parallel()
	if got, want := binaryPathForOS("windows", "/v", "bun"), filepath.Join("/v", "bin", "bun.exe"); got != want {
		t.Errorf("windows = %q, want %q", got, want)
	}
	for _, osName := range []string{"linux", "darwin"} {
		if got, want := binaryPathForOS(osName, "/v", "bun"), filepath.Join("/v", "bin", "bun"); got != want {
			t.Errorf("%s = %q, want %q", osName, got, want)
		}
	}
}

func TestBinaryPath_ChecksExistence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	b := New()
	if _, err := b.BinaryPath(dir, "bun"); err == nil {
		t.Fatal("BinaryPath() returned nil error for a missing binary")
	}
	osName, _ := bunOS()
	want := binaryPathForOS(osName, dir, "bun")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(want, nil, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := b.BinaryPath(dir, "bun")
	if err != nil || got != want {
		t.Errorf("BinaryPath() = %q, %v; want %q", got, err, want)
	}
}

func TestBinDir_IsBinSubdir(t *testing.T) {
	t.Parallel()
	got, err := New().BinDir("/v")
	if err != nil || got != filepath.Join("/v", "bin") {
		t.Errorf("BinDir() = %q, %v; want %q", got, err, filepath.Join("/v", "bin"))
	}
}

func TestName(t *testing.T) {
	t.Parallel()
	if New().Name() != "bun" {
		t.Errorf("Name() = %q, want %q", New().Name(), "bun")
	}
}
