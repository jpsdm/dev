# Core CLI Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up a buildable, testable, lintable `dev` CLI skeleton — repo scaffold, config, platform/filesystem abstractions, root command, `dev version`, error/output conventions, Makefile, and CI — that every later sub-project (lang management, workspace management, release automation) builds on without rework.

**Architecture:** A thin `cmd` package (Cobra) delegates all filesystem/path knowledge to `internal/platform`, all config I/O to `internal/config` (which itself uses `internal/filesystem` for atomic writes), and all user-facing output/error formatting to `internal/cliutil`. No package other than `platform` knows about `DEV_HOME`'s on-disk layout; no package other than `cliutil` writes directly to stdout/stderr.

**Tech Stack:** Go (module `github.com/jpsdm/dev`), Cobra for the CLI, stdlib `testing` for tests, `golangci-lint` for linting, GitHub Actions for CI.

**Spec:** `docs/superpowers/specs/2026-09-25-core-cli-foundation-design.md`

## Global Constraints

- Module path: `github.com/jpsdm/dev`; `go.mod` `go` directive: `1.23`; CI uses `actions/setup-go` with `go-version: 'stable'`.
- `CGO_ENABLED=0` for all builds.
- License: MIT.
- No third-party error/stacktrace library — stdlib `errors`/`fmt` only.
- No Testify in this phase — stdlib `testing` only.
- No Huh/Lip Gloss or any other UI library added until a later phase actually needs interactive prompts or rich TUI output (none is needed in this phase).
- Tests are colocated as `*_test.go` next to the code they test, never in a top-level `tests/` directory.
- Filesystem tests always use `t.TempDir()`; never touch the real user filesystem or hardcode paths like `/home` or `~/Documents`.
- `DEV_HOME` env var overrides the default `$HOME/.dev`; an empty-string value counts as unset, not as a literal path.
- No normal user-facing command error may print a Go stack trace or panic output; `cliutil.PrintError` owns all error presentation.
- Subcommands must never define their own `PersistentPreRun` without also calling `cliutil.SetVerbose(...)` themselves — Cobra does not chain parent/child `PersistentPreRun` automatically, so skipping this would silently break `--verbose` for that subcommand.
- No `lang` or `workspace` commands in this phase — the root command stays minimal; those arrive in later sub-projects.

## Review Focus

- `DEV_HOME` set to the empty string must fall back to the default `$HOME/.dev`, not resolve to an invalid empty path — Task 2.
- `config.Load` on a file that exists but holds invalid JSON must return a clear wrapped error distinguishable from `ErrNotFound`, never panic — Task 4.
- `dev --version` and `dev version` must print byte-identical output (version, commit, build date, platform) — Cobra's default version template only prints the bare version string, so this needs an explicit override — Task 6.
- `dev` invoked with no subcommand must print help text and exit 0, not error, despite `SilenceUsage`/`SilenceErrors` being set on root for the error-reporting path — Task 6.
- `cliutil` and `cmd` package tests mutate shared package-level state (`Stdout`/`Stderr`/`verbose`, the global `rootCmd`) and must not run with `t.Parallel()` against each other, or output assertions become flaky — Task 5 and Task 6.

---

## Task 1: Install Go and scaffold the Go module

**Files:**
- Create: `go.mod`
- Create: `VERSION`
- Create: `LICENSE`
- Create: `.gitignore`
- Create: `README.md`
- Create: `main.go` (placeholder — replaced in Task 6)

**Interfaces:**
- Consumes: nothing (first task).
- Produces: a Go module (`github.com/jpsdm/dev`, `go 1.23`) that later tasks add packages under; `VERSION` file (`0.1.0`) that the Makefile (Task 7) reads.

- [ ] **Step 1: Confirm no Go toolchain is installed, then install one**

Run: `go version`
Expected: `command not found` (confirms the current state noted in the spec).

Install via Homebrew (already on this machine, no `sudo` required):

```bash
brew install go
go version
```

Expected: a `go version go1.2x.x linux/amd64` line (any current stable 1.23+ release is fine).

- [ ] **Step 2: Initialize the Go module**

```bash
cd /var/home/jpsdm/Dev/src/github.com/jpsdm/godev
go mod init github.com/jpsdm/dev
go mod edit -go=1.23
cat go.mod
```

Expected: `go.mod` contains `module github.com/jpsdm/dev` and `go 1.23`.

- [ ] **Step 3: Create the VERSION file**

Write `VERSION`:

```text
0.1.0
```

- [ ] **Step 4: Create the LICENSE file**

Write `LICENSE`:

```text
MIT License

Copyright (c) 2026 jpsdm

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to
deal in the Software without restriction, including without limitation the
rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
sell copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS
IN THE SOFTWARE.
```

- [ ] **Step 5: Create .gitignore**

Write `.gitignore`:

```text
/dev
/dev.exe
*.test
*.out
```

- [ ] **Step 6: Create the README stub**

Write `README.md`:

```markdown
# dev

`dev` is a small, fast Development Environment Manager: a single Go binary
that installs and switches language/runtime versions and organizes a
developer workspace.

This is an early, in-progress build. `lang` and `workspace` commands are
being added incrementally.

## Requirements

- Go (stable release)

## Build

    make build

## Test

    make test

## Full check (fmt, vet, lint, test, build)

    make check
```

- [ ] **Step 7: Create the main.go placeholder**

Write `main.go`:

```go
package main

func main() {}
```

- [ ] **Step 8: Verify the module builds**

Run: `go build ./... && go vet ./...`
Expected: both succeed with no output.

- [ ] **Step 9: Commit**

```bash
git add go.mod VERSION LICENSE .gitignore README.md main.go
git commit -m "Scaffold Go module for dev CLI"
```

---

## Task 2: `internal/platform` — OS/arch detection and DEV_HOME layout

**Files:**
- Create: `internal/platform/platform.go`
- Test: `internal/platform/platform_test.go`

**Interfaces:**
- Consumes: nothing beyond stdlib.
- Produces: `platform.DevHome() (string, error)`, `platform.VersionsDir() (string, error)`, `platform.CurrentDir() (string, error)`, `platform.BinDir() (string, error)`, `platform.CacheDir() (string, error)`, `platform.ConfigDir() (string, error)`, `platform.OS() string`, `platform.Arch() string` — used by `internal/config` (Task 4) and `cmd` (Task 6).

- [ ] **Step 1: Write the failing tests**

Create `internal/platform/platform_test.go`:

```go
package platform

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestDevHome_UsesEnvVarWhenSet(t *testing.T) {
	t.Setenv("DEV_HOME", "/custom/dev/home")

	got, err := DevHome()
	if err != nil {
		t.Fatalf("DevHome() returned error: %v", err)
	}
	if got != "/custom/dev/home" {
		t.Errorf("DevHome() = %q, want %q", got, "/custom/dev/home")
	}
}

func TestDevHome_EmptyEnvVarFallsBackToDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.UserHomeDir() reads USERPROFILE on Windows, not HOME")
	}
	t.Setenv("DEV_HOME", "")
	t.Setenv("HOME", "/home/tester")

	got, err := DevHome()
	if err != nil {
		t.Fatalf("DevHome() returned error: %v", err)
	}
	want := filepath.Join("/home/tester", ".dev")
	if got != want {
		t.Errorf("DevHome() = %q, want %q", got, want)
	}
}

func TestSubdirHelpers(t *testing.T) {
	t.Setenv("DEV_HOME", "/custom/dev/home")

	cases := []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"VersionsDir", VersionsDir, "/custom/dev/home/versions"},
		{"CurrentDir", CurrentDir, "/custom/dev/home/current"},
		{"BinDir", BinDir, "/custom/dev/home/bin"},
		{"CacheDir", CacheDir, "/custom/dev/home/cache"},
		{"ConfigDir", ConfigDir, "/custom/dev/home/config"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.fn()
			if err != nil {
				t.Fatalf("%s() returned error: %v", tc.name, err)
			}
			want := filepath.FromSlash(tc.want)
			if got != want {
				t.Errorf("%s() = %q, want %q", tc.name, got, want)
			}
		})
	}
}

func TestOSAndArch(t *testing.T) {
	t.Parallel()
	if OS() == "" {
		t.Error("OS() returned empty string")
	}
	if Arch() == "" {
		t.Error("Arch() returned empty string")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/platform/... -v`
Expected: FAIL — `undefined: DevHome` (package has no implementation yet).

- [ ] **Step 3: Write the implementation**

Create `internal/platform/platform.go`:

```go
// Package platform resolves the on-disk layout dev manages everything
// under, plus the current OS/architecture.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// DevHome resolves the root directory dev manages everything under.
// It honors the DEV_HOME environment variable when set to a non-empty
// value; otherwise it defaults to $HOME/.dev.
func DevHome() (string, error) {
	if v := os.Getenv("DEV_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving user home directory: %w", err)
	}
	return filepath.Join(home, ".dev"), nil
}

// VersionsDir returns DEV_HOME/versions, where installed runtime
// versions live.
func VersionsDir() (string, error) {
	return subdir("versions")
}

// CurrentDir returns DEV_HOME/current, holding the active-version
// links for each runtime.
func CurrentDir() (string, error) {
	return subdir("current")
}

// BinDir returns DEV_HOME/bin, where shims live.
func BinDir() (string, error) {
	return subdir("bin")
}

// CacheDir returns DEV_HOME/cache, used for downloaded artifacts.
func CacheDir() (string, error) {
	return subdir("cache")
}

// ConfigDir returns DEV_HOME/config, holding config.json.
func ConfigDir() (string, error) {
	return subdir("config")
}

func subdir(name string) (string, error) {
	home, err := DevHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, name), nil
}

// OS returns the current operating system identifier (e.g. "linux").
func OS() string {
	return runtime.GOOS
}

// Arch returns the current architecture identifier (e.g. "amd64").
func Arch() string {
	return runtime.GOARCH
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/platform/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/platform
git commit -m "Add internal/platform for DEV_HOME layout and OS/arch detection"
```

---

## Task 3: `internal/filesystem` — safe file helpers

**Files:**
- Create: `internal/filesystem/filesystem.go`
- Test: `internal/filesystem/filesystem_test.go`

**Interfaces:**
- Consumes: nothing beyond stdlib.
- Produces: `filesystem.Exists(path string) bool`, `filesystem.EnsureDir(path string, perm os.FileMode) error`, `filesystem.WriteFileAtomic(path string, data []byte, perm os.FileMode) error` — used by `internal/config` (Task 4).

- [ ] **Step 1: Write the failing tests**

Create `internal/filesystem/filesystem_test.go`:

```go
package filesystem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "present.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if !Exists(file) {
		t.Error("Exists() = false for a file that exists")
	}
	if Exists(filepath.Join(dir, "missing.txt")) {
		t.Error("Exists() = true for a file that does not exist")
	}
}

func TestEnsureDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "a", "b", "c")

	if err := EnsureDir(target, 0o755); err != nil {
		t.Fatalf("EnsureDir() returned error: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat after EnsureDir: %v", err)
	}
	if !info.IsDir() {
		t.Error("EnsureDir() did not create a directory")
	}

	if err := EnsureDir(target, 0o755); err != nil {
		t.Fatalf("EnsureDir() on existing dir returned error: %v", err)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "config.json")

	if err := WriteFileAtomic(target, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic() returned error: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Errorf("file content = %q, want %q", got, `{"a":1}`)
	}

	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want 1 (no leftover temp files)", len(entries))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/filesystem/... -v`
Expected: FAIL — `undefined: Exists` etc.

- [ ] **Step 3: Write the implementation**

Create `internal/filesystem/filesystem.go`:

```go
// Package filesystem provides small, safe filesystem helpers shared
// across dev's packages.
package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
)

// Exists reports whether path exists on disk (file or directory).
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// EnsureDir creates path and any missing parents with the given
// permissions if they don't already exist. It is a no-op if the
// directory already exists.
func EnsureDir(path string, perm os.FileMode) error {
	if err := os.MkdirAll(path, perm); err != nil {
		return fmt.Errorf("creating directory %s: %w", path, err)
	}
	return nil
}

// WriteFileAtomic writes data to path by writing to a temporary file
// in the same directory and renaming it into place, so readers never
// observe a partially written file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := EnsureDir(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing temp file %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp file %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("setting permissions on %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming %s to %s: %w", tmpPath, path, err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/filesystem/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/filesystem
git commit -m "Add internal/filesystem with Exists/EnsureDir/WriteFileAtomic"
```

---

## Task 4: `internal/config` — config file load/save

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `platform.ConfigDir() (string, error)` (Task 2), `filesystem.WriteFileAtomic(path string, data []byte, perm os.FileMode) error` (Task 3).
- Produces: `config.Config{Workspace config.WorkspaceConfig}`, `config.WorkspaceConfig{Path string}`, `config.ErrNotFound`, `config.DefaultPath() (string, error)`, `config.Load(path string) (*Config, error)`, `config.Save(path string, cfg *Config) error` — `DefaultPath`/`Load`/`Save` are used starting in sub-project 3 (workspace); no consumer in this phase besides tests.

- [ ] **Step 1: Write the failing tests**

Create `internal/config/config_test.go`:

```go
package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_MissingFileReturnsErrNotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg, err := Load(path)
	if cfg == nil {
		t.Fatal("Load() returned nil *Config, want zero-value *Config")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() error = %v, want it to wrap ErrNotFound", err)
	}
}

func TestLoad_InvalidJSONReturnsClearError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() returned nil error for invalid JSON")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("Load() on invalid JSON should not report ErrNotFound")
	}
}

func TestSaveThenLoad_RoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.json")

	want := &Config{Workspace: WorkspaceConfig{Path: "/home/user/workspace"}}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save() returned error: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if got.Workspace.Path != want.Workspace.Path {
		t.Errorf("Workspace.Path = %q, want %q", got.Workspace.Path, want.Workspace.Path)
	}
}

func TestDefaultPath_UsesConfigDir(t *testing.T) {
	t.Parallel()
	t.Setenv("DEV_HOME", "/custom/dev/home")

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() returned error: %v", err)
	}
	want := filepath.Join("/custom/dev/home", "config", "config.json")
	if got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/... -v`
Expected: FAIL — `undefined: Load` etc.

- [ ] **Step 3: Write the implementation**

Create `internal/config/config.go`:

```go
// Package config loads and saves dev's config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jpsdm/dev/internal/filesystem"
	"github.com/jpsdm/dev/internal/platform"
)

// ErrNotFound indicates the config file does not exist yet. Callers
// may treat this as "use defaults" rather than a fatal error.
var ErrNotFound = errors.New("config file not found")

// WorkspaceConfig holds workspace-related settings.
type WorkspaceConfig struct {
	Path string `json:"path"`
}

// Config is the full contents of DEV_HOME/config/config.json.
type Config struct {
	Workspace WorkspaceConfig `json:"workspace"`
}

// DefaultPath returns the default location of the config file.
func DefaultPath() (string, error) {
	dir, err := platform.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads and parses the config file at path. If the file does not
// exist, it returns a zero-value *Config together with an error that
// wraps ErrNotFound (checkable via errors.Is), so callers can choose
// to treat a missing file as "use defaults" instead of failing.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes cfg to path as pretty-printed JSON, creating parent
// directories as needed and writing atomically.
func Save(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := filesystem.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("writing config file %s: %w", path, err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "Add internal/config for config.json load/save"
```

---

## Task 5: `internal/cliutil` — output and error presentation

**Files:**
- Create: `internal/cliutil/cliutil.go`
- Test: `internal/cliutil/cliutil_test.go`

**Interfaces:**
- Consumes: nothing beyond stdlib.
- Produces: `cliutil.Stdout`, `cliutil.Stderr` (`io.Writer` vars), `cliutil.SetVerbose(v bool)`, `cliutil.Success(format string, args ...any)`, `cliutil.Step(format string, args ...any)`, `cliutil.Error(format string, args ...any)`, `cliutil.Verbosef(format string, args ...any)`, `cliutil.PrintError(err error)` — all used by `cmd` (Task 6) and by every later phase's commands.

- [ ] **Step 1: Write the failing tests**

Create `internal/cliutil/cliutil_test.go`:

```go
// cliutil's tests mutate package-level state (Stdout, Stderr,
// verbose), so none of them use t.Parallel() against each other.
package cliutil

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func withCapturedOutput(t *testing.T, fn func(stdout, stderr *bytes.Buffer)) {
	t.Helper()
	origStdout, origStderr, origVerbose := Stdout, Stderr, verbose
	t.Cleanup(func() {
		Stdout, Stderr, verbose = origStdout, origStderr, origVerbose
	})

	var stdout, stderr bytes.Buffer
	Stdout, Stderr = &stdout, &stderr
	fn(&stdout, &stderr)
}

func TestSuccess(t *testing.T) {
	withCapturedOutput(t, func(stdout, _ *bytes.Buffer) {
		Success("Node.js %d installed", 22)
		if got := stdout.String(); got != "✓ Node.js 22 installed\n" {
			t.Errorf("Success() output = %q", got)
		}
	})
}

func TestStep(t *testing.T) {
	withCapturedOutput(t, func(stdout, _ *bytes.Buffer) {
		Step("Downloading...")
		if got := stdout.String(); got != "→ Downloading...\n" {
			t.Errorf("Step() output = %q", got)
		}
	})
}

func TestError(t *testing.T) {
	withCapturedOutput(t, func(_, stderr *bytes.Buffer) {
		Error("Node.js 22 could not be installed")
		if got := stderr.String(); got != "✗ Node.js 22 could not be installed\n" {
			t.Errorf("Error() output = %q", got)
		}
	})
}

func TestVerbosef_SilentWhenNotVerbose(t *testing.T) {
	withCapturedOutput(t, func(stdout, _ *bytes.Buffer) {
		SetVerbose(false)
		Verbosef("detail: %s", "value")
		if got := stdout.String(); got != "" {
			t.Errorf("Verbosef() printed %q while not verbose, want nothing", got)
		}
	})
}

func TestVerbosef_PrintsWhenVerbose(t *testing.T) {
	withCapturedOutput(t, func(stdout, _ *bytes.Buffer) {
		SetVerbose(true)
		Verbosef("detail: %s", "value")
		if got := stdout.String(); got != "detail: value\n" {
			t.Errorf("Verbosef() output = %q", got)
		}
	})
}

func TestPrintError_NonVerboseShowsOnlyTopMessage(t *testing.T) {
	withCapturedOutput(t, func(_, stderr *bytes.Buffer) {
		SetVerbose(false)
		inner := errors.New("checksum mismatch")
		outer := fmt.Errorf("installing node 22: %w", inner)

		PrintError(outer)

		got := stderr.String()
		if !strings.Contains(got, "installing node 22: checksum mismatch") {
			t.Errorf("PrintError() output = %q, want it to contain the top-level message", got)
		}
		if strings.Contains(got, "caused by:") {
			t.Errorf("PrintError() output = %q, should not show the error chain when not verbose", got)
		}
	})
}

func TestPrintError_VerboseShowsErrorChain(t *testing.T) {
	withCapturedOutput(t, func(_, stderr *bytes.Buffer) {
		SetVerbose(true)
		inner := errors.New("checksum mismatch")
		outer := fmt.Errorf("installing node 22: %w", inner)

		PrintError(outer)

		got := stderr.String()
		if !strings.Contains(got, "caused by: checksum mismatch") {
			t.Errorf("PrintError() output = %q, want it to show the wrapped chain when verbose", got)
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cliutil/... -v`
Expected: FAIL — `undefined: Success` etc.

- [ ] **Step 3: Write the implementation**

Create `internal/cliutil/cliutil.go`:

```go
// Package cliutil is the single place dev's commands write
// user-facing output and report errors, so formatting and the
// --verbose contract stay consistent everywhere.
package cliutil

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Stdout and Stderr are the writers all output functions use. Tests
// may swap them to capture output; production code leaves them at
// their defaults (os.Stdout / os.Stderr).
var (
	Stdout io.Writer = os.Stdout
	Stderr io.Writer = os.Stderr
)

var verbose bool

// SetVerbose sets whether Verbosef output and the extended error
// chain in PrintError are shown. cmd/root.go calls this once, from
// the root command's PersistentPreRun, after flags are parsed.
func SetVerbose(v bool) {
	verbose = v
}

// Success prints a ✓-prefixed success message to Stdout.
func Success(format string, args ...any) {
	fmt.Fprintf(Stdout, "✓ "+format+"\n", args...)
}

// Step prints a →-prefixed in-progress message to Stdout.
func Step(format string, args ...any) {
	fmt.Fprintf(Stdout, "→ "+format+"\n", args...)
}

// Error prints a ✗-prefixed error message to Stderr.
func Error(format string, args ...any) {
	fmt.Fprintf(Stderr, "✗ "+format+"\n", args...)
}

// Verbosef prints diagnostic detail to Stdout, but only when verbose
// mode is enabled; it is a no-op otherwise.
func Verbosef(format string, args ...any) {
	if !verbose {
		return
	}
	fmt.Fprintf(Stdout, format+"\n", args...)
}

// PrintError reports err the way every command's error path should:
// a plain ✗-prefixed top-level message always, and, only when verbose
// mode is enabled, the full wrapped error chain beneath it. It never
// prints a Go stack trace.
func PrintError(err error) {
	Error(err.Error())
	if !verbose {
		return
	}
	for wrapped := errors.Unwrap(err); wrapped != nil; wrapped = errors.Unwrap(wrapped) {
		fmt.Fprintf(Stderr, "  caused by: %v\n", wrapped)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cliutil/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/cliutil
git commit -m "Add internal/cliutil for output and error presentation"
```

---

## Task 6: `cmd` package — root command and `dev version`

**Files:**
- Create: `cmd/root.go`
- Create: `cmd/version.go`
- Test: `cmd/root_test.go`
- Modify: `main.go` (replace the Task 1 placeholder with the real entrypoint)

**Interfaces:**
- Consumes: `platform.OS() string`, `platform.Arch() string` (Task 2); `cliutil.SetVerbose(v bool)`, `cliutil.PrintError(err error)`, `cliutil.Stderr` (Task 5).
- Produces: `cmd.Execute()` (called from `main.go`), `cmd.Version`, `cmd.Commit`, `cmd.BuildDate` (package vars targeted by the Makefile's `-ldflags` in Task 7).

- [ ] **Step 1: Add the Cobra dependency**

```bash
go get github.com/spf13/cobra@latest
```

This resolves whatever the current release is at implementation time; `go.sum` then pins that exact version for reproducibility going forward — the `@latest` tag is only used for this one `go get`.

Expected: `go.mod` gains a `require github.com/spf13/cobra ...` line and `go.sum` is created/updated.

- [ ] **Step 2: Write the failing tests**

Create `cmd/root_test.go`:

```go
// cmd's tests mutate the shared package-level rootCmd (adding/removing
// commands, SetArgs, SetOut/SetErr), so none of them use t.Parallel()
// against each other.
package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/cliutil"
)

func TestVersionCommand_MatchesVersionFlag(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"version"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev version` returned error: %v", err)
	}

	got := strings.TrimSpace(out.String())
	want := versionString()
	if got != want {
		t.Errorf("`dev version` output = %q, want %q (must match `dev --version`)", got, want)
	}
}

func TestNoSubcommand_PrintsHelpWithoutError(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev` with no args returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Errorf("`dev` with no args output = %q, want it to contain usage/help text", out.String())
	}
}

func TestExecute_ErrorPathRespectsVerboseFlag(t *testing.T) {
	// Execute() itself calls os.Exit on failure, which can't be
	// exercised in-process. This test instead pins the two things
	// Execute() composes: rootCmd.Execute() surfaces the RunE error,
	// and PersistentPreRun's cliutil.SetVerbose(verboseFlag) call
	// makes cliutil.PrintError honor --verbose on that same error.
	inner := errors.New("checksum mismatch")
	failing := &cobra.Command{
		Use:           "test-fail",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("installing node 22: %w", inner)
		},
	}
	rootCmd.AddCommand(failing)
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	t.Cleanup(func() {
		rootCmd.RemoveCommand(failing)
		rootCmd.SetArgs(nil)
		cliutil.SetVerbose(false)
	})

	cases := []struct {
		args      []string
		wantChain bool
	}{
		{args: []string{"test-fail"}, wantChain: false},
		{args: []string{"test-fail", "--verbose"}, wantChain: true},
	}

	for _, tc := range cases {
		rootCmd.SetArgs(tc.args)
		err := rootCmd.Execute()
		if err == nil {
			t.Fatalf("args=%v: expected an error from test-fail", tc.args)
		}

		var buf bytes.Buffer
		origStderr := cliutil.Stderr
		cliutil.Stderr = &buf
		cliutil.PrintError(err)
		cliutil.Stderr = origStderr

		gotChain := strings.Contains(buf.String(), "caused by: checksum mismatch")
		if gotChain != tc.wantChain {
			t.Errorf("args=%v: chain present = %v, want %v (output: %q)", tc.args, gotChain, tc.wantChain, buf.String())
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./cmd/... -v`
Expected: FAIL — `undefined: rootCmd` / `undefined: versionString` (package has no implementation yet).

- [ ] **Step 4: Write the implementation**

Create `cmd/root.go`:

```go
// Package cmd defines dev's Cobra command tree. It is the only
// package that constructs Cobra commands; all filesystem/path logic
// lives in internal/platform and internal/config, and all output goes
// through internal/cliutil.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/platform"
)

// Version, Commit and BuildDate are injected at build time via
// -ldflags (see the Makefile's build target). Their defaults here are
// only used for `go run`/`go test`, never for a released binary.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

var verboseFlag bool

var rootCmd = &cobra.Command{
	Use:   "dev",
	Short: "dev manages language runtimes, workspaces and dev environments",
	Long: "dev is a small, fast Development Environment Manager: it installs " +
		"and switches language/runtime versions, and organizes a project " +
		"workspace.",
	Version:       versionString(),
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		cliutil.SetVerbose(verboseFlag)
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "enable verbose output")
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	rootCmd.AddCommand(versionCmd)
}

// versionString builds the full multi-line version block shared by
// both `dev --version` and `dev version`, so the two never diverge.
func versionString() string {
	return fmt.Sprintf(
		"dev version %s\ncommit: %s\nbuild date: %s\nplatform: %s/%s",
		Version, Commit, BuildDate, platform.OS(), platform.Arch(),
	)
}

// Execute runs the root command and is the CLI's single entry point,
// called from main.go. On error it prints via cliutil.PrintError and
// exits non-zero; it never lets a Go panic surface for an ordinary
// command error.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		cliutil.PrintError(err)
		os.Exit(1)
	}
}
```

Create `cmd/version.go`:

```go
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the dev version",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintln(cmd.OutOrStdout(), versionString())
		return nil
	},
}
```

Replace `main.go`:

```go
package main

import "github.com/jpsdm/dev/cmd"

func main() {
	cmd.Execute()
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/... -v`
Expected: PASS for all tests.

- [ ] **Step 6: Run the full test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all succeed.

- [ ] **Step 7: Commit**

```bash
git add cmd main.go go.mod go.sum
git commit -m "Add root command and dev version"
```

---

## Task 7: Makefile and version-injected builds

**Files:**
- Create: `Makefile`

**Interfaces:**
- Consumes: `VERSION` file (Task 1); `cmd.Version`, `cmd.Commit`, `cmd.BuildDate` (Task 6, as `-ldflags -X` targets).
- Produces: `make build`, `make test`, `make fmt`, `make vet`, `make clean`, `make check` targets used by Task 9 and by CI (Task 8) locally-equivalent commands.

- [ ] **Step 1: Write the Makefile**

Create `Makefile`:

```makefile
MODULE := github.com/jpsdm/dev
BINARY := dev

VERSION := $(shell cat VERSION)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X '$(MODULE)/cmd.Version=$(VERSION)' \
           -X '$(MODULE)/cmd.Commit=$(COMMIT)' \
           -X '$(MODULE)/cmd.BuildDate=$(BUILD_DATE)'

.PHONY: build test lint fmt vet clean check

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

lint:
	golangci-lint run

fmt:
	gofmt -l -w .

vet:
	go vet ./...

clean:
	rm -f $(BINARY)

check: fmt vet lint test build
```

- [ ] **Step 2: Verify the targets that don't depend on golangci-lint (added in Task 8)**

```bash
make fmt
make vet
make test
make build
```

Expected: `fmt` reports nothing to change (files are already gofmt-clean from `go build`/editor formatting); `vet` and `test` succeed silently/with PASS; `build` produces a `./dev` binary.

- [ ] **Step 3: Verify version injection**

```bash
./dev version
./dev --version
```

Expected: both print an identical 4-line block — `dev version 0.1.0`, a `commit:` line with a real short git hash, a `build date:` line matching today's UTC date, and a `platform:` line matching `go env GOOS`/`GOARCH` (e.g. `linux/amd64`).

- [ ] **Step 4: Commit**

```bash
git add Makefile
git commit -m "Add Makefile with version-injected build"
```

---

## Task 8: golangci-lint config and CI workflow

**Files:**
- Create: `.golangci.yml`
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: `make fmt`, `make vet`, `make test`, `make build` (Task 7, as the commands CI mirrors).
- Produces: a working `make lint` (and therefore `make check`) locally, and a CI workflow that runs the same checks on every PR/push. Nothing in later tasks depends on this one directly.

- [ ] **Step 1: Install golangci-lint locally**

```bash
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin
golangci-lint version
```

Expected: a version line prints. Ensure `$(go env GOPATH)/bin` is on `PATH` for this session (add it if `golangci-lint version` reports "command not found" after install).

- [ ] **Step 2: Write the lint config**

Create `.golangci.yml`:

```yaml
run:
  timeout: 5m

linters:
  enable:
    - govet
    - staticcheck
    - unused
    - ineffassign
    - errcheck
```

- [ ] **Step 3: Run the linter and reconcile the config with the installed version**

```bash
make lint
```

Expected: passes with no findings. If it instead fails with a config-schema error (golangci-lint made a breaking config format change), run `golangci-lint migrate` in this directory to rewrite `.golangci.yml` to the installed version's schema, then re-run `make lint` until it passes.

- [ ] **Step 4: Write the CI workflow**

Create `.github/workflows/ci.yml`:

```yaml
name: CI

on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  ci:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: 'stable'

      - name: Download dependencies
        run: go mod download

      - name: Check formatting
        run: |
          UNFORMATTED=$(gofmt -l .)
          if [ -n "$UNFORMATTED" ]; then
            echo "The following files are not gofmt-formatted:"
            echo "$UNFORMATTED"
            exit 1
          fi

      - name: Vet
        run: go vet ./...

      - name: Lint
        uses: golangci/golangci-lint-action@v6
        with:
          version: latest

      - name: Test
        run: go test ./...

      - name: Build
        run: go build ./...
```

- [ ] **Step 5: Simulate the CI pipeline locally**

```bash
gofmt -l .
go vet ./...
golangci-lint run
go test ./...
go build ./...
```

Expected: every command succeeds (the `gofmt -l .` command prints nothing).

- [ ] **Step 6: Commit**

```bash
git add .golangci.yml .github/workflows/ci.yml
git commit -m "Add golangci-lint config and CI workflow"
```

---

## Task 9: Final acceptance verification

**Files:**
- Modify: `README.md` (expand from the Task 1 stub)

**Interfaces:**
- Consumes: everything produced by Tasks 1–8.
- Produces: nothing new for later phases; this task only verifies the sub-project's acceptance criteria and leaves the tree committed and clean.

- [ ] **Step 1: Run the full check chain**

```bash
make check
```

Expected: `fmt`, `vet`, `lint`, `test`, `build` all run in order and succeed.

- [ ] **Step 2: Verify version output**

```bash
./dev --version
./dev version
```

Expected: identical output on both, matching the format verified in Task 7.

- [ ] **Step 3: Verify no-subcommand and help behavior**

```bash
./dev
echo "exit code: $?"
./dev --help
echo "exit code: $?"
./dev -v version
echo "exit code: $?"
```

Expected: all three exit with code `0`; the first two print usage/help text; the third prints the same version block as Step 2 (verbose doesn't break a successful path).

- [ ] **Step 4: Verify the built binary is gitignored and the tree is otherwise clean**

```bash
git check-ignore dev
git status --short
```

Expected: `git check-ignore dev` prints `dev` (confirms it's ignored); `git status --short` shows no unexpected untracked or modified files.

- [ ] **Step 5: Expand the README**

Update `README.md` to:

```markdown
# dev

`dev` is a small, fast Development Environment Manager: a single Go binary
that installs and switches language/runtime versions and organizes a
developer workspace.

This build covers the CLI foundation: configuration, platform detection,
and `dev version`. `lang` and `workspace` commands are being added in
later phases.

## Requirements

- Go (stable release) to build from source
- No runtime dependency (Node.js, Python, etc.) is required to run `dev`
  itself — it ships as a single static binary

## Build

    make build

## Test

    make test

## Lint

    make lint

## Full check (fmt, vet, lint, test, build)

    make check

## Usage

    dev --version
    dev version
    dev --help
```

- [ ] **Step 6: Commit**

```bash
git add README.md
git commit -m "Expand README for the core CLI foundation"
```

- [ ] **Step 7: Final confirmation**

```bash
make check
git status --short
```

Expected: `make check` passes and `git status --short` is empty — the sub-project is complete and the tree is clean.
