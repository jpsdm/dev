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

func TestUpdateCommand_ConfirmedSwapsBinaryAndPersistsConfig(t *testing.T) {
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
