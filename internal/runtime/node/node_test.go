package node

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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/filesystem"
	devruntime "github.com/jpsdm/dev/internal/runtime"
)

// ownPlatformKey returns the nodejs.org release-index "files" key for
// the platform/arch these tests are actually running on, so fixtures
// aren't hardcoded to one platform (e.g. "linux-x64").
func ownPlatformKey(t *testing.T) string {
	t.Helper()
	osName, err := nodeOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	archName, err := nodeArch()
	if err != nil {
		t.Skipf("unsupported arch for this test: %v", err)
	}
	key, err := filesKey(osName, archName)
	if err != nil {
		t.Fatalf("filesKey() returned error: %v", err)
	}
	return key
}

type fixtureEntry struct {
	Version string      `json:"version"`
	LTS     interface{} `json:"lts"`
	Files   []string    `json:"files"`
}

// matchingFixtureIndexJSON builds the same release-index fixture the
// old fixtureIndexJSON constant held, except every entry now carries
// the current test-running platform's "files" key so
// ListRemoteVersions's platform filtering doesn't exclude everything.
func matchingFixtureIndexJSON(t *testing.T) string {
	t.Helper()
	key := ownPlatformKey(t)
	entries := []fixtureEntry{
		{Version: "v23.1.0", LTS: false, Files: []string{key}},
		{Version: "v22.11.0", LTS: "Jod", Files: []string{key}},
		{Version: "v22.9.0", LTS: "Jod", Files: []string{key}},
		{Version: "v20.18.0", LTS: "Iron", Files: []string{key}},
		{Version: "v18.20.4", LTS: "Hydrogen", Files: []string{key}},
		{Version: "v9.5.0", LTS: false, Files: []string{key}},
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshaling fixture: %v", err)
	}
	return string(data)
}

func TestListRemoteVersions_KeepsNewestPerMajor(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(matchingFixtureIndexJSON(t))); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	n := &Node{baseURL: server.URL}

	versions, err := n.ListRemoteVersions(context.Background())
	if err != nil {
		t.Fatalf("ListRemoteVersions() returned error: %v", err)
	}

	want := map[string]bool{"23": false, "22": true, "20": true, "18": true, "9": false}
	if len(versions) != len(want) {
		t.Fatalf("ListRemoteVersions() returned %d versions, want %d: %+v", len(versions), len(want), versions)
	}
	for _, v := range versions {
		wantLTS, ok := want[v.Name]
		if !ok {
			t.Errorf("unexpected version %q in result", v.Name)
			continue
		}
		if v.LTS != wantLTS {
			t.Errorf("version %q LTS = %v, want %v", v.Name, v.LTS, wantLTS)
		}
	}
}

func TestListRemoteVersions_OnlyNewestEntryPerMajorKept(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(matchingFixtureIndexJSON(t))); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	n := &Node{baseURL: server.URL}

	versions, err := n.ListRemoteVersions(context.Background())
	if err != nil {
		t.Fatalf("ListRemoteVersions() returned error: %v", err)
	}

	count := 0
	for _, v := range versions {
		if v.Name == "22" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("found %d entries for major 22, want exactly 1 (only the newest, v22.11.0)", count)
	}
}

func TestListRemoteVersions_SortedNewestMajorFirst(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(matchingFixtureIndexJSON(t))); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	n := &Node{baseURL: server.URL}

	versions, err := n.ListRemoteVersions(context.Background())
	if err != nil {
		t.Fatalf("ListRemoteVersions() returned error: %v", err)
	}

	for i := 1; i < len(versions); i++ {
		prev, _ := strconv.Atoi(versions[i-1].Name)
		curr, _ := strconv.Atoi(versions[i].Name)
		if prev < curr {
			t.Errorf("versions not sorted newest-major-first: %+v", versions)
			break
		}
	}
}

func TestListRemoteVersions_ServerErrorReturnsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	n := &Node{baseURL: server.URL}

	_, err := n.ListRemoteVersions(context.Background())
	if err == nil {
		t.Fatal("ListRemoteVersions() returned nil error for a 500 response")
	}
}

func TestListRemoteVersions_ExcludesEntriesMissingCurrentPlatform(t *testing.T) {
	key := ownPlatformKey(t)
	fixture := fmt.Sprintf(
		`[{"version":"v99.0.0","lts":false,"files":["some-other-platform-key"]},{"version":"v22.11.0","lts":"Jod","files":[%q]}]`,
		key,
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(fixture)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	n := &Node{baseURL: server.URL}

	versions, err := n.ListRemoteVersions(context.Background())
	if err != nil {
		t.Fatalf("ListRemoteVersions() returned error: %v", err)
	}
	for _, v := range versions {
		if v.Name == "99" {
			t.Errorf("ListRemoteVersions() included major 99, which has no build for this platform (key %q)", key)
		}
	}
	found22 := false
	for _, v := range versions {
		if v.Name == "22" {
			found22 = true
		}
	}
	if !found22 {
		t.Error("ListRemoteVersions() excluded major 22, which does have a build for this platform")
	}
}

func TestNodeOS_ReturnsANonEmptyMapping(t *testing.T) {
	t.Parallel()
	got, err := nodeOS()
	if err != nil {
		t.Fatalf("nodeOS() returned error on this test's platform: %v", err)
	}
	if got == "" {
		t.Error("nodeOS() returned empty string")
	}
}

func TestNodeArch_ReturnsANonEmptyMapping(t *testing.T) {
	t.Parallel()
	got, err := nodeArch()
	if err != nil {
		t.Fatalf("nodeArch() returned error on this test's platform: %v", err)
	}
	if got == "" {
		t.Error("nodeArch() returned empty string")
	}
}

func TestArchiveExtension(t *testing.T) {
	t.Parallel()
	if got := archiveExtension("win"); got != ".zip" {
		t.Errorf(`archiveExtension("win") = %q, want ".zip"`, got)
	}
	if got := archiveExtension("linux"); got != ".tar.gz" {
		t.Errorf(`archiveExtension("linux") = %q, want ".tar.gz"`, got)
	}
	if got := archiveExtension("darwin"); got != ".tar.gz" {
		t.Errorf(`archiveExtension("darwin") = %q, want ".tar.gz"`, got)
	}
}

func TestListInstalledVersions_EmptyWhenNoneInstalled(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	n := New()

	versions, err := n.ListInstalledVersions()
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
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "node", "22"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "node", "20"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	n := New()

	versions, err := n.ListInstalledVersions()
	if err != nil {
		t.Fatalf("ListInstalledVersions() returned error: %v", err)
	}
	got := map[string]bool{}
	for _, v := range versions {
		got[v.Name] = true
	}
	if !got["22"] || !got["20"] || len(got) != 2 {
		t.Errorf("ListInstalledVersions() = %+v, want exactly {20, 22}", versions)
	}
}

func TestListInstalledVersions_SkipsHiddenAndBackupDirs(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, name := range []string{"22", ".tmp-node-extract-abc123", "20.old"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "node", name), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	n := New()

	versions, err := n.ListInstalledVersions()
	if err != nil {
		t.Fatalf("ListInstalledVersions() returned error: %v", err)
	}
	if len(versions) != 1 || versions[0].Name != "22" {
		t.Errorf("ListInstalledVersions() = %+v, want only {22} (leftover temp/backup dirs filtered out)", versions)
	}
}

func TestCurrentVersion_NilWhenNoneActive(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	n := New()

	got, err := n.CurrentVersion()
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
	if err := os.WriteFile(filepath.Join(devHome, "current", "node"), []byte("22"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	n := New()

	got, err := n.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got == nil || got.Name != "22" {
		t.Errorf(`CurrentVersion() = %+v, want Name="22"`, got)
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

	osName, err := nodeOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	archName, err := nodeArch()
	if err != nil {
		t.Skipf("unsupported arch for this test: %v", err)
	}
	ext := archiveExtension(osName)
	filename := "node-v22.11.0-" + osName + "-" + archName + ext

	archiveBytes := buildFixtureArchive(t, ext, "node-v22.11.0-"+osName+"-"+archName, map[string]string{
		"bin/node": "fake node binary",
	})
	sum := sha256.Sum256(archiveBytes)
	checksumLine := hex.EncodeToString(sum[:]) + "  " + filename + "\n"

	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(`[{"version":"v22.11.0","lts":"Jod"}]`)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})
	mux.HandleFunc("/v22.11.0/SHASUMS256.txt", func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(checksumLine)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})
	mux.HandleFunc("/v22.11.0/"+filename, func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write(archiveBytes); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	n := &Node{baseURL: server.URL}
	if err := n.Install(context.Background(), "22"); err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(devHome, "versions", "node", "22", "bin", "node"))
	if err != nil {
		t.Fatalf("reading installed binary: %v", err)
	}
	if string(got) != "fake node binary" {
		t.Errorf("installed binary content = %q, want %q", got, "fake node binary")
	}
}

func TestInstall_AlreadyInstalledSkipsNetworkRedownload(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)

	osName, _ := nodeOS()
	archName, _ := nodeArch()
	ext := archiveExtension(osName)
	filename := "node-v22.11.0-" + osName + "-" + archName + ext
	archiveBytes := buildFixtureArchive(t, ext, "node-v22.11.0-"+osName+"-"+archName, map[string]string{"bin/node": "v1"})
	sum := sha256.Sum256(archiveBytes)
	checksumLine := hex.EncodeToString(sum[:]) + "  " + filename + "\n"

	downloadCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(`[{"version":"v22.11.0","lts":"Jod"}]`)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})
	mux.HandleFunc("/v22.11.0/SHASUMS256.txt", func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(checksumLine)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})
	mux.HandleFunc("/v22.11.0/"+filename, func(w http.ResponseWriter, r *http.Request) {
		downloadCount++
		if _, err := w.Write(archiveBytes); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	n := &Node{baseURL: server.URL}
	if err := n.Install(context.Background(), "22"); err != nil {
		t.Fatalf("first Install() returned error: %v", err)
	}
	if err := n.Install(context.Background(), "22"); err != nil {
		t.Fatalf("second Install() returned error: %v", err)
	}

	if downloadCount != 1 {
		t.Errorf("archive downloaded %d times, want exactly 1 (second Install should skip the network)", downloadCount)
	}
}

func TestInstall_NewerPatchReplacesExistingInstall(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)

	osName, _ := nodeOS()
	archName, _ := nodeArch()
	ext := archiveExtension(osName)

	var mu sync.Mutex
	currentVersion := "v22.9.0"
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		v := currentVersion
		mu.Unlock()
		fmt.Fprintf(w, `[{"version":%q,"lts":"Jod"}]`, v)
	})
	serveRelease := func(version, content string) {
		filename := "node-" + version + "-" + osName + "-" + archName + ext
		archiveBytes := buildFixtureArchive(t, ext, "node-"+version+"-"+osName+"-"+archName, map[string]string{"bin/node": content})
		sum := sha256.Sum256(archiveBytes)
		checksumLine := hex.EncodeToString(sum[:]) + "  " + filename + "\n"
		mux.HandleFunc("/"+version+"/SHASUMS256.txt", func(w http.ResponseWriter, r *http.Request) {
			if _, err := w.Write([]byte(checksumLine)); err != nil {
				t.Errorf("writing response: %v", err)
			}
		})
		mux.HandleFunc("/"+version+"/"+filename, func(w http.ResponseWriter, r *http.Request) {
			if _, err := w.Write(archiveBytes); err != nil {
				t.Errorf("writing response: %v", err)
			}
		})
	}
	serveRelease("v22.9.0", "old binary")
	serveRelease("v22.11.0", "new binary")

	server := httptest.NewServer(mux)
	defer server.Close()

	n := &Node{baseURL: server.URL}
	if err := n.Install(context.Background(), "22"); err != nil {
		t.Fatalf("installing v22.9.0 returned error: %v", err)
	}

	mu.Lock()
	currentVersion = "v22.11.0"
	mu.Unlock()

	if err := n.Install(context.Background(), "22"); err != nil {
		t.Fatalf("installing v22.11.0 returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(devHome, "versions", "node", "22", "bin", "node"))
	if err != nil {
		t.Fatalf("reading installed binary: %v", err)
	}
	if string(got) != "new binary" {
		t.Errorf("installed binary content = %q, want %q (should have replaced the old patch)", got, "new binary")
	}
}

func TestResolveRelease_RejectsVersionStringNotFullyMatchingVXYZ(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A malformed "version" (as if a compromised/misbehaving index
		// server appended extra path segments) must never be trusted for
		// building a filesystem path, so it should simply not match.
		if _, err := w.Write([]byte(`[{"version":"v22.11.0/../../evil","lts":false}]`)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()
	n := &Node{baseURL: server.URL}

	exactVersion, _, err := n.resolveRelease(context.Background(), "22")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if exactVersion != "" {
		t.Errorf("resolveRelease() = %q, want empty (malformed version string must not match)", exactVersion)
	}
}

func TestInstall_OfflineFallbackUsesCachedReleaseWhenIndexFetchFails(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	destDir := filepath.Join(devHome, "versions", "node", "22")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, ".dev-release"), []byte("v22.11.0"), 0o644); err != nil {
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

	n := &Node{baseURL: server.URL}
	if err := n.Install(context.Background(), "22"); err != nil {
		t.Fatalf("Install() returned error despite an offline fallback being available: %v", err)
	}
	if !strings.Contains(buf.String(), "v22.11.0") {
		t.Errorf("Install() output = %q, want it to mention the already-installed release", buf.String())
	}
}

func TestInstall_NoOfflineFallbackWhenNothingCached(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	n := &Node{baseURL: server.URL}
	if err := n.Install(context.Background(), "22"); err == nil {
		t.Fatal("Install() returned nil error despite no cached release and a failing index fetch")
	}
}

func TestInstall_UnknownVersionFailsWithoutCreatingDirectory(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(`[{"version":"v22.11.0","lts":"Jod"}]`)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()

	n := &Node{baseURL: server.URL}
	err := n.Install(context.Background(), "999")
	if err == nil {
		t.Fatal("Install() returned nil error for a nonexistent major version")
	}

	if _, statErr := os.Stat(filepath.Join(devHome, "versions", "node", "999")); !os.IsNotExist(statErr) {
		t.Error("Install() created a directory for a version that failed to resolve")
	}
}

func TestUninstall_NotInstalledReturnsError(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	n := New()

	err := n.Uninstall("22")
	if err == nil {
		t.Fatal("Uninstall() returned nil error for a version that was never installed")
	}
	if !strings.Contains(err.Error(), "Node.js") {
		t.Errorf("Uninstall() error = %q, want it to preserve the \"Node.js\" brand name", err)
	}
}

func TestUninstall_LeavesOtherActiveVersionMarkerAlone(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "node", "22"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "node", "20"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "current"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devHome, "current", "node"), []byte("20"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	n := New()

	if err := n.Uninstall("22"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	got, err := n.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got == nil || got.Name != "20" {
		t.Errorf("CurrentVersion() = %+v after uninstalling a non-active version, want Name=\"20\" untouched", got)
	}
}

func TestUninstall_RemovesDirectory(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	dir := filepath.Join(devHome, "versions", "node", "22")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	n := New()

	if err := n.Uninstall("22"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}
	if filesystem.Exists(dir) {
		t.Error("Uninstall() did not remove the version directory")
	}
}

func TestUninstall_ClearsActiveMarkerIfCurrentlyActive(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "node", "22"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "current"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devHome, "current", "node"), []byte("22"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	n := New()

	if err := n.Uninstall("22"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}

	got, err := n.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got != nil {
		t.Errorf("CurrentVersion() = %+v after uninstalling the active version, want nil", got)
	}
}

func TestActivate_NotInstalledReturnsError(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	n := New()

	err := n.Activate("22")
	if err == nil {
		t.Fatal("Activate() returned nil error for a version that isn't installed")
	}
	if !strings.Contains(err.Error(), "Node.js") {
		t.Errorf("Activate() error = %q, want it to preserve the \"Node.js\" brand name", err)
	}

	current, err := n.CurrentVersion()
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
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "node", "22"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	n := New()

	if err := n.Activate("22"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	got, err := n.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion() returned error: %v", err)
	}
	if got == nil || got.Name != "22" {
		t.Errorf(`CurrentVersion() = %+v, want Name="22"`, got)
	}
}

var _ devruntime.Runtime = (*Node)(nil)

func TestBinaryPath_UnixLayout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test targets the Unix (bin/) layout")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	nodeBin := filepath.Join(binDir, "node")
	if err := os.WriteFile(nodeBin, []byte("fake"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	n := New()
	got, err := n.BinaryPath(dir, "node")
	if err != nil {
		t.Fatalf("BinaryPath() returned error: %v", err)
	}
	if got != nodeBin {
		t.Errorf("BinaryPath() = %q, want %q", got, nodeBin)
	}
}

func TestBinaryPath_WindowsLayout(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("this test targets the Windows (flat, .cmd) layout")
	}
	dir := t.TempDir()
	nodeExe := filepath.Join(dir, "node.exe")
	if err := os.WriteFile(nodeExe, []byte("fake"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	npmCmd := filepath.Join(dir, "npm.cmd")
	if err := os.WriteFile(npmCmd, []byte("fake"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	n := New()
	gotNode, err := n.BinaryPath(dir, "node")
	if err != nil {
		t.Fatalf("BinaryPath(node) returned error: %v", err)
	}
	if gotNode != nodeExe {
		t.Errorf("BinaryPath(node) = %q, want %q", gotNode, nodeExe)
	}

	gotNpm, err := n.BinaryPath(dir, "npm")
	if err != nil {
		t.Fatalf("BinaryPath(npm) returned error: %v", err)
	}
	if gotNpm != npmCmd {
		t.Errorf("BinaryPath(npm) = %q, want %q", gotNpm, npmCmd)
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
	if err := os.WriteFile(archive1, buildFixtureArchive(t, ".tar.gz", "node-v1", map[string]string{"bin/node": "v1"}), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := extractStrippingTopLevel(archive1, destDir); err != nil {
		t.Fatalf("extractStrippingTopLevel() first call returned error: %v", err)
	}

	archive2 := filepath.Join(dir, "v2.tar.gz")
	if err := os.WriteFile(archive2, buildFixtureArchive(t, ".tar.gz", "node-v2", map[string]string{"bin/node": "v2"}), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := extractStrippingTopLevel(archive2, destDir); err != nil {
		t.Fatalf("extractStrippingTopLevel() second call returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "bin", "node"))
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

	// Two top-level entries instead of the single directory Node.js
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

func TestBinaryPath_MissingBinaryReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	n := New()

	_, err := n.BinaryPath(dir, "node")
	if err == nil {
		t.Fatal("BinaryPath() returned nil error for a binary that doesn't exist")
	}
}

func TestBinDirForOS_WindowsUsesVersionRoot(t *testing.T) {
	t.Parallel()
	got := binDirForOS("win", "/versiondir")
	want := "/versiondir"
	if got != want {
		t.Errorf(`binDirForOS("win", ...) = %q, want %q`, got, want)
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
	n := New()
	got, err := n.BinDir("/some/version/dir")
	if err != nil {
		t.Fatalf("BinDir() returned error on this test's platform: %v", err)
	}
	if got == "" {
		t.Error("BinDir() returned empty string")
	}
}
