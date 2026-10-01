# Runtime Core + Node.js Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `Runtime` abstraction, a download/checksum/install pipeline, a working Node.js provider, and the `dev lang` command group — the foundation sub-project 2b (shims/PATH) and 2c (more providers) build on.

**Architecture:** `internal/runtime` defines the `Runtime` interface and a `Manager` registry; `internal/downloader` and `internal/installer` are provider-agnostic building blocks (HTTP fetch + checksum verification; archive extraction with atomic swap); `internal/runtime/node` is the first `Runtime` implementation, built on those two plus sub-project 1's `platform`/`filesystem`/`cliutil`; `cmd/lang.go` wires a `Manager` to Cobra commands.

**Tech Stack:** Go stdlib only for this phase (`net/http`, `archive/tar`, `compress/gzip`, `archive/zip`, `crypto/sha256`, `encoding/json`) — no new third-party dependencies beyond Cobra (already present). Verified live against nodejs.org during planning: release filenames use `darwin`/`linux`/`win` + `x64`/`arm64`/`x86`, Linux and macOS both publish `.tar.gz` (so no XZ decoder is needed), Windows publishes `.zip`, checksums are plain `<sha256hex>  <filename>` lines in `SHASUMS256.txt`, and every tarball/zip has exactly one top-level directory.

**Spec:** `docs/superpowers/specs/2026-09-26-runtime-core-node-design.md`

## Global Constraints

- No third-party archive, checksum, or HTTP client library — stdlib `archive/tar`, `compress/gzip`, `archive/zip`, `crypto/sha256`, `net/http` only.
- Node.js version granularity is major-version-only (`"22"`, not `"22.11.0"`); the resolved exact release (`"v22.11.0"`) is tracked internally so re-running install on the same major only re-downloads when a newer patch has actually shipped.
- `current/<lang>` is a plain text marker file (the resolved version name, nothing else), never an OS symlink, on every platform.
- Every install verifies a sha256 checksum against the vendor's published checksum file before extraction; a checksum mismatch deletes the downloaded file and returns an error.
- Extraction is atomic: a failed or interrupted install/re-install never leaves `versions/<lang>/<name>` partially populated or destroys a previously-good install.
- `context.Context` threads through `ListRemoteVersions` and `Install` (the two network-touching `Runtime` methods).
- No test in the automated suite makes a real network call — `httptest.Server` stands in for nodejs.org everywhere.
- All filesystem-touching tests use `t.TempDir()` and a `DEV_HOME` env override, per sub-project 1's established pattern; tests using `t.Setenv` never call `t.Parallel()` on themselves (Go forbids combining the two).
- No Testify — stdlib `testing` only, matching sub-project 1.
- `lang`→`l`, `list`→`ls`, `current`→`c`, `install`→`i`, `use`→`u` aliases; `installed` and `uninstall` get no short alias.

## Review Focus

- A checksum mismatch must delete the downloaded file from the cache directory — a later retry must never silently reuse a corrupt cached file — Task 2.
- Re-installing the exact already-installed release must skip the network entirely, but re-installing when a newer patch has shipped for the same major must replace it — Task 6.
- `dev lang install node <unknown-version>` must fail cleanly with no directory created under `versions/node/` — Task 6.
- `dev lang use node <not-installed>` must error clearly and never write a `current/node` marker pointing at a version that isn't there — Task 7.
- A corrupted/truncated archive must leave an existing `destDir` completely untouched (a failed re-install must not destroy the previously-good install) — Task 3.

---

## Task 1: `internal/runtime` — interface and registry

**Files:**
- Create: `internal/runtime/runtime.go`
- Test: `internal/runtime/runtime_test.go`

**Interfaces:**
- Consumes: nothing beyond stdlib.
- Produces: `runtime.Version{Name string; LTS bool}`, `runtime.Runtime` interface (`Name() string`, `ListRemoteVersions(ctx context.Context) ([]Version, error)`, `ListInstalledVersions() ([]Version, error)`, `CurrentVersion() (*Version, error)`, `Install(ctx context.Context, name string) error`, `Uninstall(name string) error`, `Activate(name string) error`), `runtime.NewManager() *Manager`, `(*Manager).Register(r Runtime)`, `(*Manager).Get(name string) (Runtime, bool)`, `(*Manager).Names() []string` (sorted) — used by `internal/runtime/node` (Tasks 4–7, implements the interface) and `cmd/lang.go` (Task 8, uses the Manager).

- [ ] **Step 1: Write the failing tests**

Create `internal/runtime/runtime_test.go`:

```go
package runtime

import (
	"context"
	"testing"
)

type stubRuntime struct {
	name string
}

func (s *stubRuntime) Name() string { return s.name }
func (s *stubRuntime) ListRemoteVersions(ctx context.Context) ([]Version, error) {
	return nil, nil
}
func (s *stubRuntime) ListInstalledVersions() ([]Version, error) { return nil, nil }
func (s *stubRuntime) CurrentVersion() (*Version, error)         { return nil, nil }
func (s *stubRuntime) Install(ctx context.Context, name string) error {
	return nil
}
func (s *stubRuntime) Uninstall(name string) error { return nil }
func (s *stubRuntime) Activate(name string) error  { return nil }

func TestManager_RegisterAndGet(t *testing.T) {
	t.Parallel()
	m := NewManager()
	m.Register(&stubRuntime{name: "node"})

	got, ok := m.Get("node")
	if !ok {
		t.Fatal(`Get("node") ok = false, want true`)
	}
	if got.Name() != "node" {
		t.Errorf(`Get("node").Name() = %q, want "node"`, got.Name())
	}
}

func TestManager_GetUnknown(t *testing.T) {
	t.Parallel()
	m := NewManager()

	_, ok := m.Get("ruby")
	if ok {
		t.Error(`Get("ruby") ok = true, want false (nothing registered)`)
	}
}

func TestManager_NamesSorted(t *testing.T) {
	t.Parallel()
	m := NewManager()
	m.Register(&stubRuntime{name: "python"})
	m.Register(&stubRuntime{name: "node"})
	m.Register(&stubRuntime{name: "go"})

	got := m.Names()
	want := []string{"go", "node", "python"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/... -v`
Expected: FAIL — `undefined: NewManager` (package has no implementation yet).

- [ ] **Step 3: Write the implementation**

Create `internal/runtime/runtime.go`:

```go
// Package runtime defines the abstraction every language/tool provider
// (Node.js, Java, Python, ...) implements, and a registry the CLI uses
// to look providers up by name.
package runtime

import (
	"context"
	"sort"
)

// Version identifies one installable release in a provider-defined
// granularity (e.g. "22" for Node's major-version lines).
type Version struct {
	Name string
	LTS  bool
}

// Runtime is implemented once per managed language/tool.
type Runtime interface {
	// Name is the identifier used on the command line, e.g. "node".
	Name() string
	// ListRemoteVersions returns installable versions, newest first.
	ListRemoteVersions(ctx context.Context) ([]Version, error)
	// ListInstalledVersions returns versions currently installed locally.
	ListInstalledVersions() ([]Version, error)
	// CurrentVersion returns the active version, or (nil, nil) if none.
	CurrentVersion() (*Version, error)
	// Install resolves name against the remote versions and installs it.
	Install(ctx context.Context, name string) error
	// Uninstall removes an installed version.
	Uninstall(name string) error
	// Activate makes an installed version the active one.
	Activate(name string) error
}

// Manager is a registry of Runtimes keyed by name.
type Manager struct {
	runtimes map[string]Runtime
}

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{runtimes: make(map[string]Runtime)}
}

// Register adds r to the registry, keyed by r.Name().
func (m *Manager) Register(r Runtime) {
	m.runtimes[r.Name()] = r
}

// Get looks up a Runtime by name.
func (m *Manager) Get(name string) (Runtime, bool) {
	r, ok := m.runtimes[name]
	return r, ok
}

// Names returns every registered Runtime's name, sorted.
func (m *Manager) Names() []string {
	names := make([]string, 0, len(m.runtimes))
	for name := range m.runtimes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/runtime.go internal/runtime/runtime_test.go
git commit -m "Add internal/runtime interface and Manager registry"
```

---

## Task 2: `internal/downloader` — HTTP fetch with checksum verification

**Files:**
- Create: `internal/downloader/downloader.go`
- Test: `internal/downloader/downloader_test.go`

**Interfaces:**
- Consumes: `filesystem.EnsureDir(path string, perm os.FileMode) error` (sub-project 1).
- Produces: `downloader.Download(ctx context.Context, url, destPath, wantSHA256 string) error` — used by `internal/runtime/node` (Task 6).

- [ ] **Step 1: Write the failing tests**

Create `internal/downloader/downloader_test.go`:

```go
package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownload_Success(t *testing.T) {
	t.Parallel()
	content := []byte("hello from a fake node tarball")
	sum := sha256.Sum256(content)
	wantSHA256 := hex.EncodeToString(sum[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "nested", "file.tar.gz")

	if err := Download(context.Background(), server.URL, dest, wantSHA256); err != nil {
		t.Fatalf("Download() returned error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("downloaded content = %q, want %q", got, content)
	}
}

func TestDownload_ChecksumMismatchRemovesFile(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("actual content"))
	}))
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "file.tar.gz")

	err := Download(context.Background(), server.URL, dest, strings.Repeat("0", 64))
	if err == nil {
		t.Fatal("Download() returned nil error for a checksum mismatch")
	}

	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("Download() left %s on disk after a checksum mismatch", dest)
	}
}

func TestDownload_ServerErrorReturnsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "file.tar.gz")

	err := Download(context.Background(), server.URL, dest, "irrelevant")
	if err == nil {
		t.Fatal("Download() returned nil error for a 404 response")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("Download() created %s despite a 404 response", dest)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/downloader/... -v`
Expected: FAIL — `undefined: Download` (package has no implementation yet).

- [ ] **Step 3: Write the implementation**

Create `internal/downloader/downloader.go`:

```go
// Package downloader fetches files over HTTP and verifies their
// integrity before the caller does anything else with them.
package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jpsdm/dev/internal/filesystem"
)

// Download fetches url into destPath (creating parent directories as
// needed), then verifies the downloaded file's sha256 digest against
// wantSHA256 (case-insensitive hex). On any failure — network error,
// non-2xx response, write error, or checksum mismatch — destPath is
// removed and a descriptive error is returned. A caller must never act
// on destPath's contents unless Download returned nil.
func Download(ctx context.Context, url, destPath, wantSHA256 string) error {
	if err := filesystem.EnsureDir(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building request for %s: %w", url, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: unexpected status %s", url, resp.Status)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", destPath, err)
	}

	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, hasher), resp.Body)
	closeErr := out.Close()

	if copyErr != nil {
		os.Remove(destPath)
		return fmt.Errorf("writing %s: %w", destPath, copyErr)
	}
	if closeErr != nil {
		os.Remove(destPath)
		return fmt.Errorf("closing %s: %w", destPath, closeErr)
	}

	gotSHA256 := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(gotSHA256, wantSHA256) {
		os.Remove(destPath)
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", url, gotSHA256, wantSHA256)
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/downloader/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/downloader
git commit -m "Add internal/downloader with checksum-verified fetch"
```

---

## Task 3: `internal/installer` — atomic archive extraction

**Files:**
- Create: `internal/installer/installer.go`
- Test: `internal/installer/installer_test.go`

**Interfaces:**
- Consumes: `filesystem.EnsureDir(path string, perm os.FileMode) error` (sub-project 1).
- Produces: `installer.ExtractAtomic(archivePath, destDir string) error` — used by `internal/runtime/node` (Task 6).

- [ ] **Step 1: Write the failing tests**

Create `internal/installer/installer_test.go`:

```go
package installer

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func buildTarGz(t *testing.T, files map[string]string) string {
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

	dir := t.TempDir()
	path := filepath.Join(dir, "archive.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing archive file: %v", err)
	}
	return path
}

func buildZip(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
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

	dir := t.TempDir()
	path := filepath.Join(dir, "archive.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing archive file: %v", err)
	}
	return path
}

func TestExtractAtomic_TarGz(t *testing.T) {
	t.Parallel()
	archive := buildTarGz(t, map[string]string{
		"node-v22.11.0-linux-x64/bin/node":  "fake binary",
		"node-v22.11.0-linux-x64/README.md": "readme",
	})
	destDir := filepath.Join(t.TempDir(), "dest")

	if err := ExtractAtomic(archive, destDir); err != nil {
		t.Fatalf("ExtractAtomic() returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "node-v22.11.0-linux-x64", "bin", "node"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != "fake binary" {
		t.Errorf("extracted content = %q, want %q", got, "fake binary")
	}
}

func TestExtractAtomic_Zip(t *testing.T) {
	t.Parallel()
	archive := buildZip(t, map[string]string{
		"node-v22.11.0-win-x64/node.exe": "fake exe",
	})
	destDir := filepath.Join(t.TempDir(), "dest")

	if err := ExtractAtomic(archive, destDir); err != nil {
		t.Fatalf("ExtractAtomic() returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "node-v22.11.0-win-x64", "node.exe"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != "fake exe" {
		t.Errorf("extracted content = %q, want %q", got, "fake exe")
	}
}

func TestExtractAtomic_ReplacesExistingDestDir(t *testing.T) {
	t.Parallel()
	destDir := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	archive := buildTarGz(t, map[string]string{"new.txt": "new"})
	if err := ExtractAtomic(archive, destDir); err != nil {
		t.Fatalf("ExtractAtomic() returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, "old.txt")); !os.IsNotExist(err) {
		t.Error("old.txt still present after ExtractAtomic replaced destDir")
	}
	got, err := os.ReadFile(filepath.Join(destDir, "new.txt"))
	if err != nil {
		t.Fatalf("reading new.txt: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("new.txt content = %q, want %q", got, "new")
	}
}

func TestExtractAtomic_CorruptArchiveLeavesExistingDestDirUntouched(t *testing.T) {
	t.Parallel()
	destDir := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "keep.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	badArchive := filepath.Join(t.TempDir(), "corrupt.tar.gz")
	if err := os.WriteFile(badArchive, []byte("not a real gzip stream"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := ExtractAtomic(badArchive, destDir)
	if err == nil {
		t.Fatal("ExtractAtomic() returned nil error for a corrupt archive")
	}

	got, err := os.ReadFile(filepath.Join(destDir, "keep.txt"))
	if err != nil {
		t.Fatalf("keep.txt missing after failed ExtractAtomic: %v", err)
	}
	if string(got) != "keep me" {
		t.Errorf("keep.txt content = %q, want %q (destDir should be untouched)", got, "keep me")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/installer/... -v`
Expected: FAIL — `undefined: ExtractAtomic` (package has no implementation yet).

- [ ] **Step 3: Write the implementation**

Create `internal/installer/installer.go`:

```go
// Package installer extracts downloaded archives into their final
// location without ever leaving a partially-extracted directory behind.
package installer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jpsdm/dev/internal/filesystem"
)

// ExtractAtomic extracts the archive at archivePath (.tar.gz or .zip,
// chosen by file extension) into destDir. Extraction happens in a
// temporary sibling directory first; destDir is only ever replaced once
// extraction fully succeeds, so a failure leaves destDir exactly as it
// was (untouched if it didn't exist, unmodified if it did).
func ExtractAtomic(archivePath, destDir string) error {
	parent := filepath.Dir(destDir)
	if err := filesystem.EnsureDir(parent, 0o755); err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp(parent, ".tmp-extract-*")
	if err != nil {
		return fmt.Errorf("creating temp extraction dir in %s: %w", parent, err)
	}
	defer os.RemoveAll(tmpDir)

	var extractErr error
	if strings.HasSuffix(archivePath, ".zip") {
		extractErr = extractZip(archivePath, tmpDir)
	} else {
		extractErr = extractTarGz(archivePath, tmpDir)
	}
	if extractErr != nil {
		return fmt.Errorf("extracting %s: %w", archivePath, extractErr)
	}

	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("removing existing %s: %w", destDir, err)
	}
	if err := os.Rename(tmpDir, destDir); err != nil {
		return fmt.Errorf("moving extracted contents to %s: %w", destDir, err)
	}
	return nil
}

// safeJoin joins destDir with an archive entry's name, rejecting any
// attempt to escape destDir via ".." path segments (zip-slip protection).
func safeJoin(destDir, name string) string {
	return filepath.Join(destDir, filepath.Clean("/"+name)[1:])
}

func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("opening gzip stream: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar entry: %w", err)
		}

		target := safeJoin(destDir, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

func extractZip(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		target := safeJoin(destDir, f.Name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/installer/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/installer
git commit -m "Add internal/installer with atomic archive extraction"
```

---

## Task 4: `internal/runtime/node` — remote listing and platform mapping

**Files:**
- Create: `internal/runtime/node/node.go`
- Test: `internal/runtime/node/node_test.go`

**Interfaces:**
- Consumes: `runtime.Version`, `runtime.Runtime` (Task 1).
- Produces: `node.New() *Node` (production constructor), `(*Node).Name() string`, `(*Node).ListRemoteVersions(ctx) ([]runtime.Version, error)` — the unexported `fetchIndex`, `nodeOS`, `nodeArch`, `archiveExtension` helpers are consumed directly by Task 6 within the same package.

- [ ] **Step 1: Write the failing tests**

Create `internal/runtime/node/node_test.go`:

```go
package node

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

const fixtureIndexJSON = `[
  {"version":"v23.1.0","lts":false},
  {"version":"v22.11.0","lts":"Jod"},
  {"version":"v22.9.0","lts":"Jod"},
  {"version":"v20.18.0","lts":"Iron"},
  {"version":"v18.20.4","lts":"Hydrogen"}
]`

func TestListRemoteVersions_KeepsNewestPerMajor(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixtureIndexJSON))
	}))
	defer server.Close()
	n := &Node{baseURL: server.URL}

	versions, err := n.ListRemoteVersions(context.Background())
	if err != nil {
		t.Fatalf("ListRemoteVersions() returned error: %v", err)
	}

	want := map[string]bool{"23": false, "22": true, "20": true, "18": true}
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
		w.Write([]byte(fixtureIndexJSON))
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
		w.Write([]byte(fixtureIndexJSON))
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/node/... -v`
Expected: FAIL — `undefined: Node` (package has no implementation yet).

- [ ] **Step 3: Write the implementation**

Create `internal/runtime/node/node.go`:

```go
// Package node implements the dev Runtime interface for Node.js.
package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strconv"

	devruntime "github.com/jpsdm/dev/internal/runtime"
)

const distBaseURL = "https://nodejs.org/dist"

// Node implements devruntime.Runtime for Node.js.
type Node struct {
	baseURL string // overridable in tests; defaults to distBaseURL
}

// New returns a production Node provider pointed at the real nodejs.org.
func New() *Node {
	return &Node{baseURL: distBaseURL}
}

func (n *Node) Name() string { return "node" }

type indexEntry struct {
	Version string      `json:"version"` // e.g. "v22.11.0"
	LTS     interface{} `json:"lts"`     // false, or a codename string
}

var majorVersionRE = regexp.MustCompile(`^v(\d+)\.`)

// fetchIndex retrieves and parses nodejs.org's release index.
func (n *Node) fetchIndex(ctx context.Context) ([]indexEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.baseURL+"/index.json", nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching Node.js release index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching Node.js release index: unexpected status %s", resp.Status)
	}

	var entries []indexEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("parsing Node.js release index: %w", err)
	}
	return entries, nil
}

// ListRemoteVersions fetches nodejs.org's release index and returns the
// newest release for each major version line, newest major first.
func (n *Node) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error) {
	entries, err := n.fetchIndex(ctx)
	if err != nil {
		return nil, err
	}

	newestByMajor := make(map[string]devruntime.Version)
	for _, e := range entries {
		m := majorVersionRE.FindStringSubmatch(e.Version)
		if m == nil {
			continue
		}
		major := m[1]
		if _, seen := newestByMajor[major]; seen {
			continue // index is newest-first; first occurrence per major wins
		}
		lts, _ := e.LTS.(string)
		newestByMajor[major] = devruntime.Version{Name: major, LTS: lts != ""}
	}

	versions := make([]devruntime.Version, 0, len(newestByMajor))
	for _, v := range newestByMajor {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool {
		vi, _ := strconv.Atoi(versions[i].Name)
		vj, _ := strconv.Atoi(versions[j].Name)
		return vi > vj
	})
	return versions, nil
}

// nodeOS maps Go's runtime.GOOS to Node.js's release filename convention.
func nodeOS() (string, error) {
	switch runtime.GOOS {
	case "linux":
		return "linux", nil
	case "darwin":
		return "darwin", nil
	case "windows":
		return "win", nil
	default:
		return "", fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}

// nodeArch maps Go's runtime.GOARCH to Node.js's release filename convention.
func nodeArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "x64", nil
	case "arm64":
		return "arm64", nil
	case "386":
		return "x86", nil
	default:
		return "", fmt.Errorf("unsupported architecture: %s", runtime.GOARCH)
	}
}

// archiveExtension returns the file extension Node.js publishes for
// the given nodeOS() value: linux and darwin both publish .tar.gz
// (verified live against nodejs.org/dist — no XZ decoder needed), and
// win publishes .zip.
func archiveExtension(os string) string {
	if os == "win" {
		return ".zip"
	}
	return ".tar.gz"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/node/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/node
git commit -m "Add Node.js provider: remote version listing and platform mapping"
```

---

## Task 5: Node provider — local state (`ListInstalledVersions`, `CurrentVersion`)

**Files:**
- Modify: `internal/runtime/node/node.go`
- Modify: `internal/runtime/node/node_test.go`

**Interfaces:**
- Consumes: `platform.VersionsDir() (string, error)`, `platform.CurrentDir() (string, error)` (sub-project 1).
- Produces: `(*Node).ListInstalledVersions() ([]runtime.Version, error)`, `(*Node).CurrentVersion() (*runtime.Version, error)` — consumed by `cmd/lang.go` (Task 8) and by Task 6/7's own Install/Uninstall/Activate logic.

- [ ] **Step 1: Write the failing tests**

Append to `internal/runtime/node/node_test.go`:

```go
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
```

Add `"os"` and `"path/filepath"` to this test file's import block.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/node/... -v -run 'TestListInstalledVersions|TestCurrentVersion'`
Expected: FAIL — `undefined: (*Node).ListInstalledVersions` / `undefined: (*Node).CurrentVersion`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/node/node.go` (add `"os"`, `"path/filepath"`, `"strings"`, and `"github.com/jpsdm/dev/internal/platform"` to the import block):

```go
// ListInstalledVersions returns one Version per subdirectory of
// versions/node/. LTS is always false here — this is a local
// directory listing, not a network lookup, so LTS status is unknown
// (not "not LTS"); only ListRemoteVersions reports real LTS status.
func (n *Node) ListInstalledVersions() ([]devruntime.Version, error) {
	dir, err := platform.VersionsDir()
	if err != nil {
		return nil, err
	}
	nodeDir := filepath.Join(dir, "node")

	entries, err := os.ReadDir(nodeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", nodeDir, err)
	}

	versions := make([]devruntime.Version, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, devruntime.Version{Name: e.Name()})
		}
	}
	return versions, nil
}

// CurrentVersion reads current/node. A missing file means no active
// version, not an error. Like ListInstalledVersions, this is a local
// read, so the returned Version.LTS is always false (unknown).
func (n *Node) CurrentVersion() (*devruntime.Version, error) {
	dir, err := platform.CurrentDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "node")

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

Run: `go test ./internal/runtime/node/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/node
git commit -m "Add Node.js provider: ListInstalledVersions and CurrentVersion"
```

---

## Task 6: Node provider — `Install`

**Files:**
- Modify: `internal/runtime/node/node.go`
- Modify: `internal/runtime/node/node_test.go`

**Interfaces:**
- Consumes: `downloader.Download(ctx, url, destPath, wantSHA256 string) error` (Task 2), `installer.ExtractAtomic(archivePath, destDir string) error` (Task 3), `platform.CacheDir() (string, error)` (sub-project 1), `filesystem.WriteFileAtomic(path string, data []byte, perm os.FileMode) error` (sub-project 1), `cliutil.Step`/`cliutil.Success` (sub-project 1), this task's own `fetchIndex`, `nodeOS`, `nodeArch`, `archiveExtension` (Task 4).
- Produces: `(*Node).Install(ctx context.Context, name string) error` — completes 4 of the 7 `runtime.Runtime` methods; Task 7 adds the remaining two.

- [ ] **Step 1: Write the failing tests**

Append to `internal/runtime/node/node_test.go` (add `"archive/tar"`, `"archive/zip"`, `"bytes"`, `"compress/gzip"`, `"crypto/sha256"`, `"encoding/hex"`, `"fmt"`, `"sync"` to the import block):

```go
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
		w.Write([]byte(`[{"version":"v22.11.0","lts":"Jod"}]`))
	})
	mux.HandleFunc("/v22.11.0/SHASUMS256.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(checksumLine))
	})
	mux.HandleFunc("/v22.11.0/"+filename, func(w http.ResponseWriter, r *http.Request) {
		w.Write(archiveBytes)
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
		w.Write([]byte(`[{"version":"v22.11.0","lts":"Jod"}]`))
	})
	mux.HandleFunc("/v22.11.0/SHASUMS256.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(checksumLine))
	})
	mux.HandleFunc("/v22.11.0/"+filename, func(w http.ResponseWriter, r *http.Request) {
		downloadCount++
		w.Write(archiveBytes)
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
			w.Write([]byte(checksumLine))
		})
		mux.HandleFunc("/"+version+"/"+filename, func(w http.ResponseWriter, r *http.Request) {
			w.Write(archiveBytes)
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

func TestInstall_UnknownVersionFailsWithoutCreatingDirectory(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"version":"v22.11.0","lts":"Jod"}]`))
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/node/... -v -run TestInstall`
Expected: FAIL — `undefined: (*Node).Install`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/node/node.go` (add `"bufio"`, `"os"`, `"path/filepath"`, `"strings"`, `"github.com/jpsdm/dev/internal/cliutil"`, `"github.com/jpsdm/dev/internal/downloader"`, `"github.com/jpsdm/dev/internal/filesystem"`, `"github.com/jpsdm/dev/internal/installer"`, `"github.com/jpsdm/dev/internal/platform"` to the import block — `"os"`, `"path/filepath"`, `"strings"`, and `platform` may already be present from Task 5):

```go
// resolveRelease finds the index entry whose major version matches
// name, returning its exact "vX.Y.Z" version string and LTS status.
// An empty exactVersion (with a nil error) means no match was found.
func (n *Node) resolveRelease(ctx context.Context, name string) (exactVersion string, lts bool, err error) {
	entries, err := n.fetchIndex(ctx)
	if err != nil {
		return "", false, err
	}
	for _, e := range entries {
		m := majorVersionRE.FindStringSubmatch(e.Version)
		if m != nil && m[1] == name {
			l, _ := e.LTS.(string)
			return e.Version, l != "", nil
		}
	}
	return "", false, nil
}

// fetchChecksum retrieves releaseURL/SHASUMS256.txt and returns the
// sha256 checksum listed for filename.
func (n *Node) fetchChecksum(ctx context.Context, releaseURL, filename string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL+"/SHASUMS256.txt", nil)
	if err != nil {
		return "", fmt.Errorf("building checksum request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching checksums: unexpected status %s", resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == filename {
			return fields[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading checksums: %w", err)
	}
	return "", fmt.Errorf("no checksum found for %s", filename)
}

// extractStrippingTopLevel extracts archivePath into destDir, removing
// the single top-level directory Node.js tarballs/zips always contain
// (e.g. "node-v22.11.0-linux-x64/") so destDir/bin/node exists
// directly. destDir is only ever replaced once extraction and the
// layout check both succeed, preserving ExtractAtomic's atomicity.
func extractStrippingTopLevel(archivePath, destDir string) error {
	parent := filepath.Dir(destDir)
	// os.MkdirTemp does not create parent directories, and on a fresh
	// DEV_HOME versions/node/ does not exist yet before the first install.
	if err := filesystem.EnsureDir(parent, 0o755); err != nil {
		return err
	}
	tmpParent, err := os.MkdirTemp(parent, ".tmp-node-extract-*")
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

	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("removing existing %s: %w", destDir, err)
	}
	if err := os.Rename(innerDir, destDir); err != nil {
		return fmt.Errorf("moving %s to %s: %w", innerDir, destDir, err)
	}
	return nil
}

// Install resolves name (e.g. "22") to the newest matching release,
// downloads and checksum-verifies it, and extracts it to
// versions/node/<name>. Re-running Install with the same name is a
// no-op (no network call) if the exact same release is already
// installed there; if a newer patch has shipped, it replaces the
// existing install.
func (n *Node) Install(ctx context.Context, name string) error {
	exactVersion, _, err := n.resolveRelease(ctx, name)
	if err != nil {
		return err
	}
	if exactVersion == "" {
		return fmt.Errorf("no Node.js release found for %q", name)
	}

	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "node", name)
	releaseMarker := filepath.Join(destDir, ".dev-release")

	if installed, err := os.ReadFile(releaseMarker); err == nil && strings.TrimSpace(string(installed)) == exactVersion {
		cliutil.Success("Node.js %s is already installed (%s)", name, exactVersion)
		return nil
	}

	osName, err := nodeOS()
	if err != nil {
		return err
	}
	archName, err := nodeArch()
	if err != nil {
		return err
	}
	ext := archiveExtension(osName)
	filename := fmt.Sprintf("node-%s-%s-%s%s", exactVersion, osName, archName, ext)
	releaseURL := fmt.Sprintf("%s/%s", n.baseURL, exactVersion)

	checksum, err := n.fetchChecksum(ctx, releaseURL, filename)
	if err != nil {
		return fmt.Errorf("installing Node.js %s: %w", name, err)
	}

	cacheDir, err := platform.CacheDir()
	if err != nil {
		return err
	}
	archivePath := filepath.Join(cacheDir, filename)

	cliutil.Step("Downloading Node.js %s...", exactVersion)
	if err := downloader.Download(ctx, releaseURL+"/"+filename, archivePath, checksum); err != nil {
		return fmt.Errorf("installing Node.js %s: %w", name, err)
	}

	cliutil.Step("Installing...")
	if err := extractStrippingTopLevel(archivePath, destDir); err != nil {
		return fmt.Errorf("installing Node.js %s: %w", name, err)
	}

	if err := filesystem.WriteFileAtomic(releaseMarker, []byte(exactVersion), 0o644); err != nil {
		return fmt.Errorf("recording installed release for Node.js %s: %w", name, err)
	}

	cliutil.Success("Node.js %s installed", name)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/node/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/node
git commit -m "Add Node.js provider: Install with checksum verification"
```

---

## Task 7: Node provider — `Uninstall` and `Activate`

**Files:**
- Modify: `internal/runtime/node/node.go`
- Modify: `internal/runtime/node/node_test.go`

**Interfaces:**
- Consumes: `filesystem.Exists(path string) bool`, `filesystem.WriteFileAtomic` (sub-project 1), this package's `CurrentVersion` (Task 5).
- Produces: `(*Node).Uninstall(name string) error`, `(*Node).Activate(name string) error` — completes `runtime.Runtime` for `*Node`; consumed by `cmd/lang.go` (Task 8).

- [ ] **Step 1: Write the failing tests**

Append to `internal/runtime/node/node_test.go` (add `"github.com/jpsdm/dev/internal/filesystem"` to the import block):

```go
func TestUninstall_NotInstalledReturnsError(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	n := New()

	if err := n.Uninstall("22"); err == nil {
		t.Fatal("Uninstall() returned nil error for a version that was never installed")
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

	if err := n.Activate("22"); err == nil {
		t.Fatal("Activate() returned nil error for a version that isn't installed")
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
```

The final line is a compile-time assertion: if `*Node` ever stops satisfying `runtime.Runtime`, the package fails to build.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/node/... -v -run 'TestUninstall|TestActivate'`
Expected: FAIL — `undefined: (*Node).Uninstall` / `undefined: (*Node).Activate`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/node/node.go`:

```go
// Uninstall removes versions/node/<name>. If it was the active
// version, current/node is cleared too, since a marker pointing at a
// removed version is worse than no active version.
func (n *Node) Uninstall(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "node", name)

	if !filesystem.Exists(destDir) {
		return fmt.Errorf("Node.js %s is not installed", name)
	}

	current, err := n.CurrentVersion()
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
		markerPath := filepath.Join(currentDir, "node")
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clearing active version marker: %w", err)
		}
		cliutil.Step("Node.js %s was the active version; no version is active now", name)
	}

	cliutil.Success("Node.js %s uninstalled", name)
	return nil
}

// Activate makes name the active Node.js version by writing it to
// current/node. name must already be installed.
func (n *Node) Activate(name string) error {
	versionsDir, err := platform.VersionsDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(versionsDir, "node", name)
	if !filesystem.Exists(destDir) {
		return fmt.Errorf("Node.js %s is not installed — run `dev lang install node %s` first", name, name)
	}

	currentDir, err := platform.CurrentDir()
	if err != nil {
		return err
	}
	markerPath := filepath.Join(currentDir, "node")
	if err := filesystem.WriteFileAtomic(markerPath, []byte(name), 0o644); err != nil {
		return fmt.Errorf("activating Node.js %s: %w", name, err)
	}

	cliutil.Success("Node.js %s activated", name)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/node/... -v`
Expected: PASS for all tests, including the `var _ devruntime.Runtime = (*Node)(nil)` compile check.

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all succeed.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/node
git commit -m "Add Node.js provider: Uninstall and Activate"
```

---

## Task 8: `cmd/lang.go` — the `dev lang` command group

**Files:**
- Create: `cmd/lang.go`
- Test: `cmd/lang_test.go`

**Interfaces:**
- Consumes: `runtime.NewManager()`, `(*Manager).Register`, `(*Manager).Get`, `(*Manager).Names()` (Task 1), `node.New()` (Task 4), full `runtime.Runtime` interface (Tasks 4–7), `cmd`'s existing `rootCmd` (sub-project 1).
- Produces: `dev lang`, `dev lang list [<lang>]`, `dev lang installed [<lang>]`, `dev lang current [<lang>]`, `dev lang install <lang> <version>`, `dev lang uninstall <lang> <version>`, `dev lang use <lang> <version>`, with aliases `l`/`ls`/`c`/`i`/`u`. Nothing else in this sub-project depends on `cmd/lang.go`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/lang_test.go`:

```go
// lang's tests mutate the shared package-level langManager, so none of
// them use t.Parallel() against each other.
package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	devruntime "github.com/jpsdm/dev/internal/runtime"
)

type stubRuntime struct {
	name       string
	remote     []devruntime.Version
	installed  []devruntime.Version
	current    *devruntime.Version
	installErr error
}

func (s *stubRuntime) Name() string { return s.name }
func (s *stubRuntime) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error) {
	return s.remote, nil
}
func (s *stubRuntime) ListInstalledVersions() ([]devruntime.Version, error) {
	return s.installed, nil
}
func (s *stubRuntime) CurrentVersion() (*devruntime.Version, error) { return s.current, nil }
func (s *stubRuntime) Install(ctx context.Context, name string) error {
	return s.installErr
}
func (s *stubRuntime) Uninstall(name string) error { return nil }
func (s *stubRuntime) Activate(name string) error  { return nil }

func withStubManager(t *testing.T, stubs ...*stubRuntime) {
	t.Helper()
	orig := langManager
	m := devruntime.NewManager()
	for _, s := range stubs {
		m.Register(s)
	}
	langManager = m
	t.Cleanup(func() { langManager = orig })
}

func TestLangInstall_UnknownLanguageErrors(t *testing.T) {
	withStubManager(t, &stubRuntime{name: "node"})

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"lang", "install", "ruby", "3"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected an error installing an unregistered language")
	}
	if !strings.Contains(err.Error(), "unknown language: ruby") {
		t.Errorf(`error = %v, want it to mention "unknown language: ruby"`, err)
	}
}

func TestLangInstall_RoutesToTheNamedRuntime(t *testing.T) {
	nodeStub := &stubRuntime{name: "node"}
	pythonStub := &stubRuntime{name: "python"}
	withStubManager(t, nodeStub, pythonStub)

	rootCmd.SetOut(new(bytes.Buffer))
	rootCmd.SetErr(new(bytes.Buffer))
	rootCmd.SetArgs([]string{"lang", "install", "python", "3.12"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute() returned error: %v", err)
	}
}

func TestLangCurrent_NoArgsListsEveryRegisteredLanguage(t *testing.T) {
	withStubManager(t,
		&stubRuntime{name: "node", current: &devruntime.Version{Name: "22"}},
		&stubRuntime{name: "python", current: nil},
	)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"lang", "current"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute() returned error: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "node: 22") {
		t.Errorf(`output = %q, want it to mention "node: 22"`, got)
	}
	if !strings.Contains(got, "python: (none active)") {
		t.Errorf(`output = %q, want it to mention "python: (none active)"`, got)
	}
}

func TestLangAliases_ShortFormsWork(t *testing.T) {
	withStubManager(t, &stubRuntime{name: "node", current: &devruntime.Version{Name: "22"}})

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"l", "c"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev l c` returned error: %v", err)
	}
	if !strings.Contains(out.String(), "node: 22") {
		t.Errorf(`"dev l c" output = %q, want it to mention "node: 22"`, out.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/... -v -run TestLang`
Expected: FAIL — `undefined: langManager` (package has no implementation yet).

- [ ] **Step 3: Write the implementation**

Create `cmd/lang.go`:

```go
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/runtime"
	"github.com/jpsdm/dev/internal/runtime/node"
)

var langManager = runtime.NewManager()

func init() {
	langManager.Register(node.New())
	rootCmd.AddCommand(langCmd)
	langCmd.AddCommand(langListCmd)
	langCmd.AddCommand(langInstalledCmd)
	langCmd.AddCommand(langCurrentCmd)
	langCmd.AddCommand(langInstallCmd)
	langCmd.AddCommand(langUninstallCmd)
	langCmd.AddCommand(langUseCmd)
}

func getRuntime(name string) (runtime.Runtime, error) {
	r, ok := langManager.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown language: %s", name)
	}
	return r, nil
}

var langCmd = &cobra.Command{
	Use:     "lang",
	Aliases: []string{"l"},
	Short:   "Manage language and runtime versions",
}

var langListCmd = &cobra.Command{
	Use:     "list [language]",
	Aliases: []string{"ls"},
	Args:    cobra.MaximumNArgs(1),
	Short:   "List available versions",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			return listOneLanguageRemote(cmd, args[0])
		}
		return listAllLanguagesNewest(cmd)
	},
}

func listOneLanguageRemote(cmd *cobra.Command, name string) error {
	r, err := getRuntime(name)
	if err != nil {
		return err
	}
	versions, err := r.ListRemoteVersions(cmd.Context())
	if err != nil {
		return fmt.Errorf("listing %s versions: %w", name, err)
	}
	installed, err := r.ListInstalledVersions()
	if err != nil {
		return fmt.Errorf("listing installed %s versions: %w", name, err)
	}
	current, err := r.CurrentVersion()
	if err != nil {
		return fmt.Errorf("reading current %s version: %w", name, err)
	}

	installedSet := make(map[string]bool, len(installed))
	for _, v := range installed {
		installedSet[v.Name] = true
	}

	for _, v := range versions {
		line := v.Name
		if v.LTS {
			line += " (LTS)"
		}
		switch {
		case current != nil && current.Name == v.Name:
			line += " (current)"
		case installedSet[v.Name]:
			line += " (installed)"
		}
		fmt.Fprintln(cmd.OutOrStdout(), line)
	}
	return nil
}

func listAllLanguagesNewest(cmd *cobra.Command) error {
	for _, name := range langManager.Names() {
		r, _ := langManager.Get(name)
		versions, err := r.ListRemoteVersions(cmd.Context())
		if err != nil {
			return fmt.Errorf("listing %s versions: %w", name, err)
		}
		if len(versions) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: (no versions available)\n", name)
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (latest)\n", name, versions[0].Name)
	}
	return nil
}

var langInstalledCmd = &cobra.Command{
	Use:   "installed [language]",
	Args:  cobra.MaximumNArgs(1),
	Short: "List installed versions",
	RunE: func(cmd *cobra.Command, args []string) error {
		names := langManager.Names()
		if len(args) == 1 {
			if _, err := getRuntime(args[0]); err != nil {
				return err
			}
			names = args
		}
		for _, name := range names {
			r, _ := langManager.Get(name)
			versions, err := r.ListInstalledVersions()
			if err != nil {
				return fmt.Errorf("listing installed %s versions: %w", name, err)
			}
			if len(versions) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: (none installed)\n", name)
				continue
			}
			for _, v := range versions {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", name, v.Name)
			}
		}
		return nil
	},
}

var langCurrentCmd = &cobra.Command{
	Use:     "current [language]",
	Aliases: []string{"c"},
	Args:    cobra.MaximumNArgs(1),
	Short:   "Show the active version",
	RunE: func(cmd *cobra.Command, args []string) error {
		names := langManager.Names()
		if len(args) == 1 {
			if _, err := getRuntime(args[0]); err != nil {
				return err
			}
			names = args
		}
		for _, name := range names {
			r, _ := langManager.Get(name)
			current, err := r.CurrentVersion()
			if err != nil {
				return fmt.Errorf("reading current %s version: %w", name, err)
			}
			if current == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: (none active)\n", name)
				continue
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", name, current.Name)
		}
		return nil
	},
}

var langInstallCmd = &cobra.Command{
	Use:     "install <language> <version>",
	Aliases: []string{"i"},
	Args:    cobra.ExactArgs(2),
	Short:   "Install a language version",
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getRuntime(args[0])
		if err != nil {
			return err
		}
		return r.Install(cmd.Context(), args[1])
	},
}

var langUninstallCmd = &cobra.Command{
	Use:   "uninstall <language> <version>",
	Args:  cobra.ExactArgs(2),
	Short: "Uninstall a language version",
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getRuntime(args[0])
		if err != nil {
			return err
		}
		return r.Uninstall(args[1])
	},
}

var langUseCmd = &cobra.Command{
	Use:     "use <language> <version>",
	Aliases: []string{"u"},
	Args:    cobra.ExactArgs(2),
	Short:   "Activate a language version",
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getRuntime(args[0])
		if err != nil {
			return err
		}
		return r.Activate(args[1])
	},
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/... -v`
Expected: PASS for all tests, including sub-project 1's existing `cmd` tests (unaffected by this change).

- [ ] **Step 5: Run the whole module's tests**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all succeed.

- [ ] **Step 6: Commit**

```bash
git add cmd/lang.go cmd/lang_test.go
git commit -m "Add dev lang command group wired to the Node.js provider"
```

---

## Task 9: Final acceptance verification

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: everything produced by Tasks 1–8.
- Produces: nothing new; verifies this sub-project's acceptance criteria against the real nodejs.org and leaves the tree committed and clean.

- [ ] **Step 1: Run the full check chain**

```bash
make check
```

Expected: `fmt`, `vet`, `lint`, `test`, `build` all succeed.

- [ ] **Step 2: Verify `dev lang list` with no arguments**

```bash
./dev lang list
```

Expected: a line like `node: 26 (latest)` (the current newest major at time of testing).

- [ ] **Step 3: Install a real Node.js version and verify idempotency**

```bash
./dev lang install node 22
./dev lang install node 22
```

Expected: the first run shows download/install progress and ends with `✓ Node.js 22 installed`; the second run prints `✓ Node.js 22 is already installed (vX.Y.Z)` immediately, with no download step shown.

- [ ] **Step 4: Verify activation and current**

```bash
./dev lang use node 22
./dev lang current node
./dev lang current
```

Expected: `✓ Node.js 22 activated`; `node: 22` from both commands.

- [ ] **Step 5: Verify `installed` and aliases**

```bash
./dev lang installed
./dev l ls node
./dev l i node 22
./dev l c
```

Expected: `node: 22` from `installed`; `dev l ls node` shows the same annotated list as `dev lang list node` (with `(installed)`/`(current)` markers on 22); `dev l i node 22` reports already installed; `dev l c` shows `node: 22`.

- [ ] **Step 6: Verify uninstall clears activation**

```bash
./dev lang uninstall node 22
./dev lang current node
ls ~/.dev/versions/node 2>/dev/null || echo "(no versions dir, or empty)"
```

Expected: `✓ Node.js 22 was the active version; no version is active now` followed by `✓ Node.js 22 uninstalled`; `dev lang current node` then shows `node: (none active)`; the `22` subdirectory is gone.

- [ ] **Step 7: Verify a bogus version fails cleanly**

```bash
./dev lang install node not-a-real-version
echo "exit code: $?"
ls ~/.dev/versions/node 2>/dev/null
```

Expected: a `✗`-prefixed error mentioning the version wasn't found; exit code `1`; no `not-a-real-version` directory listed.

- [ ] **Step 8: Update the README**

Add a "Language management" section to `README.md` after the existing "Usage" section:

```markdown

## Language management

    dev lang list                  # newest version of every supported language
    dev lang list node             # all available Node.js major versions
    dev lang install node 22
    dev lang use node 22
    dev lang current               # active version of every language
    dev lang installed
    dev lang uninstall node 22

Aliases: `l` for `lang`, `ls` for `list`, `c` for `current`, `i` for
`install`, `u` for `use`.

Currently supported languages: Node.js.
```

- [ ] **Step 9: Commit**

```bash
git add README.md
git commit -m "Document dev lang commands in the README"
```

- [ ] **Step 10: Final confirmation**

```bash
make check
git status --short
```

Expected: `make check` passes and `git status --short` is empty.
