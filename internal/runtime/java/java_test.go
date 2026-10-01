package java

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

func TestMapJavaOS_RejectsUnsupportedValue(t *testing.T) {
	t.Parallel()
	_, err := mapJavaOS("plan9")
	if err == nil {
		t.Fatal("mapJavaOS(\"plan9\") returned nil error, want an error")
	}
}

func TestMapJavaArch_RejectsUnsupportedValue(t *testing.T) {
	t.Parallel()
	_, err := mapJavaArch("mips")
	if err == nil {
		t.Fatal("mapJavaArch(\"mips\") returned nil error, want an error")
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

func TestValidJavaPathComponent_RejectsEmptyDotDotAndSeparators(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", ".", "..", "a/b", "a\\b", "../escape", "jdk-21.0.12.1+1/../../evil"} {
		if err := validJavaPathComponent(name); err == nil {
			t.Errorf("validJavaPathComponent(%q) returned nil error, want an error", name)
		}
	}
}

func TestValidJavaPathComponent_AcceptsRealTemurinFormats(t *testing.T) {
	t.Parallel()
	// jdk-X.Y.Z+B is the 9+ format; jdk8u<update>-b<build> is the
	// distinct format Java 8 uses.
	for _, name := range []string{"jdk-21.0.12.1+1", "jdk8u432-b06"} {
		if err := validJavaPathComponent(name); err != nil {
			t.Errorf("validJavaPathComponent(%q) returned error: %v, want nil", name, err)
		}
	}
}

func TestListRemoteVersions_ReturnsOneEntryPerMajorNewestFirst(t *testing.T) {
	t.Parallel()
	// package java already has direct access to availableReleasesResponse
	// (java.go's own production type) — no need for a test-local
	// redeclaration of the same shape.
	fixture := availableReleasesResponse{
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
	fixture := availableReleasesResponse{
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

func TestResolveRelease_RejectsPathInjectionShapedFilename(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A malformed binary.package.name (as if a compromised/misbehaving
		// server crafted a path-traversal filename) must never be trusted
		// for building a filesystem path, even though release_name itself
		// is well-formed here.
		fmt.Fprint(w, `[{
			"release_name": "jdk-21.0.12.1+1",
			"binary": {"package": {
				"name": "../../../../tmp/evil.tar.gz",
				"link": "https://example.invalid/evil.tar.gz",
				"checksum": "deadbeef"
			}}
		}]`)
	}))
	defer server.Close()
	j := &Java{baseURL: server.URL}

	_, err := j.resolveRelease(context.Background(), "21")
	if err == nil {
		t.Fatal("resolveRelease() returned nil error for a path-injection-shaped binary.package.name")
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

var _ devruntime.Runtime = (*Java)(nil)

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
	t.Parallel()
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
	t.Parallel()
	dir := t.TempDir()
	j := New()

	_, err := j.BinaryPath(dir, "java")
	if err == nil {
		t.Fatal("BinaryPath() returned nil error for a binary that doesn't exist")
	}
}

func TestBinDirForOS_MacUsesContentsHomeBin(t *testing.T) {
	t.Parallel()
	got := binDirForOS("mac", "/versiondir")
	want := filepath.Join("/versiondir", "Contents", "Home", "bin")
	if got != want {
		t.Errorf(`binDirForOS("mac", ...) = %q, want %q`, got, want)
	}
}

func TestBinDirForOS_LinuxAndWindowsUseBinSubdir(t *testing.T) {
	t.Parallel()
	for _, osName := range []string{"linux", "windows"} {
		got := binDirForOS(osName, "/versiondir")
		want := filepath.Join("/versiondir", "bin")
		if got != want {
			t.Errorf("binDirForOS(%q, ...) = %q, want %q", osName, got, want)
		}
	}
}

func TestBinDir_ReturnsRealDirectory(t *testing.T) {
	t.Parallel()
	j := New()
	got, err := j.BinDir("/some/version/dir")
	if err != nil {
		t.Fatalf("BinDir() returned error on this test's platform: %v", err)
	}
	if got == "" {
		t.Error("BinDir() returned empty string")
	}
}
