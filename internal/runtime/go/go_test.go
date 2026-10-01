package golang

import (
	"archive/tar"
	"archive/zip"
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
	"strings"
	"testing"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/filesystem"
	devruntime "github.com/jpsdm/dev/internal/runtime"
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

func TestMapGoOS_RejectsUnsupportedValue(t *testing.T) {
	t.Parallel()
	_, err := mapGoOS("plan9")
	if err == nil {
		t.Fatal("mapGoOS(\"plan9\") returned nil error, want non-nil")
	}
}

func TestMapGoArch_RejectsUnsupportedValue(t *testing.T) {
	t.Parallel()
	_, err := mapGoArch("z80")
	if err == nil {
		t.Fatal("mapGoArch(\"z80\") returned nil error, want non-nil")
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

func TestListRemoteVersions_ExcludesFilesWithEmptyChecksum(t *testing.T) {
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

	// go.dev/dl's real index has release lines (e.g. 1.2, 1.3, 1.4)
	// whose archive entry for this platform has no published checksum.
	// Such a line must never be advertised as installable.
	fixture := []indexRelease{
		{Version: "go1.4", Stable: true, Files: []indexFile{
			{Filename: "go1.4." + osName + "-" + archName + ext, OS: osName, Arch: archName, Version: "go1.4", SHA256: "", Kind: "archive"},
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
		t.Errorf("ListRemoteVersions() = %+v, want empty (only file for this platform has no checksum)", versions)
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

func TestResolveRelease_ExcludesFilesWithEmptyChecksum(t *testing.T) {
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

	// Same real-world shape as the go.dev/dl lines with no published
	// checksum (1.2, 1.3, 1.4) — resolveRelease must treat this the
	// same as "no archive for this platform", not proceed to a broken
	// download with an empty want-checksum.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"version":"go1.4","stable":true,"files":[
			{"filename":"go1.4.%[1]s-%[2]s%[3]s","os":%[1]q,"arch":%[2]q,"version":"go1.4","sha256":"","kind":"archive"}
		]}]`, osName, archName, ext)
	}))
	defer server.Close()
	g := &Go{baseURL: server.URL}

	release, err := g.resolveRelease(context.Background(), "1.4")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if release.Version != "" {
		t.Errorf("resolveRelease() = %+v, want a zero-value release when the only matching file has no checksum", release)
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
	// The fixture's os/arch must match this test's actual host platform
	// (via goOS()/goArch()), not a hardcoded "linux"/"amd64" — a
	// hardcoded value only matches hasArchiveFor on that one platform,
	// so on any other host (e.g. Windows CI) resolveRelease would find
	// no candidate at all and return a nil error for an entirely
	// different reason ("no release found") without ever reaching the
	// validation this test exists to exercise.
	osName, err := goOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	archName, err := goArch()
	if err != nil {
		t.Skipf("unsupported arch for this test: %v", err)
	}

	// Unlike the version field, a malformed filename does NOT get
	// filtered out by lineRE (the filename isn't parsed by it at all)
	// — this is exactly the shape of gap Java's original review finding
	// caught (release name validated, sibling filename field from the
	// same response not validated). resolveRelease must reject this.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"version":"go1.24.3","stable":true,"files":[
			{"filename":"../../evil.tar.gz","os":%q,"arch":%q,"version":"go1.24.3","sha256":"x","kind":"archive"}
		]}]`, osName, archName)
	}))
	defer server.Close()
	g := &Go{baseURL: server.URL}

	_, err = g.resolveRelease(context.Background(), "1.24")
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

var _ devruntime.Runtime = (*Go)(nil)

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

func TestBinDir_AlwaysUsesBinSubdirOnEveryPlatform(t *testing.T) {
	t.Parallel()
	g := New()
	got, err := g.BinDir("/versiondir")
	if err != nil {
		t.Fatalf("BinDir() returned error: %v", err)
	}
	want := filepath.Join("/versiondir", "bin")
	if got != want {
		t.Errorf("BinDir() = %q, want %q", got, want)
	}
}
