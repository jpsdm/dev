# Runtime Core + Node.js Provider — Design Spec

Date: 2026-09-26
Status: Approved for planning
Sub-project: 2a of 4 (sub-project 2 of 4, split into 2a/2b/2c)

## Context

This is the first slice of sub-project 2 ("Runtime/language management") from
the overall `dev` CLI plan. Sub-project 1 (Core CLI Foundation) shipped
`internal/platform`, `internal/filesystem`, `internal/config`,
`internal/cliutil`, and the Cobra `cmd` root/`version` commands. This spec
builds directly on those.

Sub-project 2 was split into three pieces because it bundles independent
concerns:

- **2a (this spec):** the `Runtime` abstraction, download/checksum/install
  machinery, a single provider (Node.js), and the `dev lang` command group.
- **2b:** `dev env`, `dev setup`, and cross-platform shims so `~/.dev/bin`
  alone suffices on `PATH`.
- **2c:** Java, Python, Go, Rust providers plugged into the same
  abstraction, proving it generalizes.

This spec covers only 2a. No shims, no `PATH` wiring, no additional
languages.

## Goals

- A `Runtime` interface and a `Manager` registry that later providers
  (2c) implement/register without touching `cmd` or the interface itself.
- A working, fully tested Node.js provider: list remote versions, install,
  uninstall, activate, report current version.
- Download integrity: every install verifies a sha256 checksum against the
  vendor's published checksum file before extracting anything.
- Install atomicity: a failed install (network, checksum, extraction)
  leaves `~/.dev/versions/node/` exactly as it was before the attempt.
- `dev lang` commands operate uniformly whether given a specific language
  or none (report across all registered languages).

## Non-goals (deferred to later phases)

- Shims and `PATH`/`dev env`/`dev setup` (sub-project 2b).
- Java, Python, Go, Rust providers (sub-project 2c).
- Workspace management (sub-project 3).
- Release automation (sub-project 4).
- Multiple simultaneously-installed patch versions of the same major line
  (installing "22" again just updates `versions/node/22/` in place).

## Version model

Each `Runtime` provider defines its own version granularity — for Node.js,
that is **major-version granularity**, matching the brief's own directory
tree (`versions/node/18`, `/20`, `/22`). `dev lang install node 22`
resolves `"22"` to the latest `22.x.z` stable release from nodejs.org,
downloads that release, but installs it at `versions/node/22/`. Re-running
`dev lang install node 22` later (e.g. after a new 22.x.z patch ships)
updates that same directory in place rather than creating a new one;
running it when the *same* resolved release is already installed is a
no-op ("Node.js 22 is already installed").

```go
// internal/runtime/runtime.go
type Version struct {
	Name string // provider-defined identifier, e.g. "22" for Node's major line
	LTS  bool
}
```

`LTS` is included because nodejs.org's release index provides it for free
and `dev lang list node` can display it; it costs nothing to add now and
avoids a breaking type change later.

## `internal/runtime` — interface and registry

```go
package runtime

import "context"

type Runtime interface {
	Name() string
	ListRemoteVersions(ctx context.Context) ([]Version, error)
	ListInstalledVersions() ([]Version, error)
	CurrentVersion() (*Version, error) // nil, nil if none active
	Install(ctx context.Context, name string) error
	Uninstall(name string) error
	Activate(name string) error
}

type Manager struct {
	runtimes map[string]Runtime
}

func NewManager() *Manager
func (m *Manager) Register(r Runtime)
func (m *Manager) Get(name string) (Runtime, bool)
func (m *Manager) Names() []string // sorted, for iterating "all languages"
```

`cmd/lang.go` constructs one `Manager`, registers the Node provider, and is
the only place that knows which providers exist. Adding a provider later
(2c) is one new `Register` call plus a new package — `Manager`, the
interface, and `cmd/lang.go`'s command wiring do not change.

## `internal/downloader`

```go
package downloader

// Download fetches url into destPath (creating parent dirs), then verifies
// the file's sha256 against wantSHA256. On any failure (network, write,
// checksum mismatch) destPath is removed and an error is returned — never
// a partially-written or unverified file left behind.
func Download(ctx context.Context, url, destPath, wantSHA256 string) error
```

Uses `net/http` with the given context (so callers can time out/cancel),
streams the response body through both a file writer and a
`crypto/sha256.Hash` simultaneously (via `io.MultiWriter`), then compares
the computed digest to `wantSHA256` (case-insensitive hex compare) before
returning success. A checksum mismatch deletes the downloaded file and
returns a clear error — this is the spec-wide rule "never execute/extract a
downloaded artifact before validating its checksum when one is available,"
applied here for the first time in this codebase.

## `internal/installer`

```go
package installer

// ExtractAtomic extracts the archive at archivePath (.tar.gz or .zip,
// detected by extension) into destDir, doing the work in a temporary
// sibling directory and renaming into place only once extraction fully
// succeeds — so a failure never leaves destDir partially populated, and a
// pre-existing destDir is atomically replaced (for re-installing a major
// line's latest patch).
func ExtractAtomic(archivePath, destDir string) error
```

Implemented with stdlib `archive/tar` + `compress/gzip` for `.tar.gz` and
stdlib `archive/zip` for `.zip` — no third-party archive library needed.
Extraction target is `filepath.Dir(destDir)/.tmp-extract-*`; on success,
`os.RemoveAll(destDir)` (if it exists) followed by `os.Rename(tmpDir,
destDir)`. On any extraction error, the temp directory is removed and
`destDir` is left untouched.

## `internal/runtime/node`

```go
package node

func New() runtime.Runtime // production constructor, hits nodejs.org
```

Internals (unexported, but described for the implementer):

- **Remote listing:** `GET https://nodejs.org/dist/index.json` (base URL
  overridable via an unexported field for tests, defaulting to the real
  host) returns an array of `{version, lts, files[]}`. The provider keeps
  only entries whose `files` includes this platform's build, groups by
  major version number (parsed from the `vX.Y.Z` string), and keeps the
  first (newest — the index is newest-first) entry per major. `lts` is
  either `false` or a codename string in the JSON; both map to
  `Version.LTS` as a plain bool.
- **Platform mapping:** `platform.OS()`/`platform.Arch()` (from
  sub-project 1) map to Node's naming: `windows`→`win`, `darwin`/`linux`
  unchanged; `amd64`→`x64`, `386`→`x86`, `arm64` unchanged. Unsupported
  combinations return a clear error rather than a malformed URL.
- **Install:** resolve `name` (e.g. `"22"`) against `ListRemoteVersions`;
  if no match, error. If `ListInstalledVersions` already contains a
  version whose resolved release matches exactly, report via
  `cliutil.Success` that it's already installed and return nil (no
  network call). Otherwise: build the tarball/zip URL
  (`https://nodejs.org/dist/vX.Y.Z/node-vX.Y.Z-<os>-<arch>.<ext>`) and the
  checksum-file URL (same directory, `SHASUMS256.txt`), fetch
  `SHASUMS256.txt` first (small, no checksum needed for a checksum file
  itself, but it's served over HTTPS from the vendor's own host), find the
  line matching the tarball's exact filename, then call
  `downloader.Download` with that checksum into `platform.CacheDir()`,
  then `installer.ExtractAtomic` into `versions/node/<major>/`. On Unix
  archives, Node's tarball has a single top-level `node-vX.Y.Z-<os>-<arch>`
  directory — extraction strips that one path component so
  `versions/node/22/bin/node` exists directly (no double-nested
  directory).
- **Uninstall:** `os.RemoveAll(versions/node/<name>)`. If
  `CurrentVersion()` equals `name`, also clear `current/node` (delete the
  marker file) and print a `cliutil.Step` explaining that the active
  version was removed.
- **Activate:** verify `name` is installed (else error: "Node.js 22 is not
  installed — run `dev lang install node 22` first"), then write `name` as
  the sole contents of `current/node` via `filesystem.WriteFileAtomic`.
- **CurrentVersion:** read `current/node`; if the file doesn't exist,
  return `(nil, nil)` — no active version is not an error. Like
  `ListInstalledVersions`, this is a local file read, not a network call,
  so the returned `Version.LTS` is always `false` (unknown, not "not
  LTS") — only `ListRemoteVersions` populates real LTS status.
- **ListInstalledVersions:** read directory entries under
  `versions/node/`; each subdirectory name is an installed version's
  `Name`. `LTS` is left `false` here (installed-version listing doesn't
  re-hit the network to look up LTS status; `dev lang list node` cross
  references the remote list separately for that annotation).

## Activation storage (`current/<lang>`)

Per your decision: a plain marker file, not an OS symlink, on every
platform. `current/node` is written via `filesystem.WriteFileAtomic` and
contains exactly the version name (e.g. `22`), no trailing structure —
plain text, trivially read with `os.ReadFile` +
`strings.TrimSpace`. This avoids Windows' unprivileged-symlink limitations
entirely and gives every platform the same code path. Sub-project 2b's
shims read this file to resolve `versions/<lang>/<name>/bin/<binary>`.

## `cmd/lang.go` — commands

```text
dev lang                          # help + list of registered languages
dev lang list [<lang>]            # all languages' newest known version, or one language's full remote list
dev lang installed [<lang>]       # all languages' installed versions, or one language's
dev lang current [<lang>]         # all languages' active version, or one language's
dev lang install <lang> <version>
dev lang uninstall <lang> <version>
dev lang use <lang> <version>
```

Aliases (Cobra `Aliases` field on each subcommand):

| Full        | Alias |
|-------------|-------|
| `lang`      | `l`   |
| `list`      | `ls`  |
| `current`   | `c`   |
| `install`   | `i`   |
| `use`       | `u`   |

`installed` and `uninstall` get no short alias — this resolves the
original brief's ambiguous `dev l u` example as `use` (a one-letter
shortcut for `uninstall` is a needless foot-gun; `use` is the safe,
frequently-typed command that deserves it).

An unknown language name (e.g. `dev lang install ruby 3`) is a plain user
error via `cliutil.Error` ("unknown language: ruby"), not a panic or stack
trace — `Manager.Get` returning `false` is the trigger.

`dev lang` / `dev lang list` / `dev lang installed` / `dev lang current`
with no language argument iterate `Manager.Names()` (currently just
`["node"]`) and print one line per language; with today's single
provider this looks list-of-one, but the loop is unconditional so 2c's
providers need zero changes here.

## Error handling & idempotency

- Installing an already-installed major is a no-op with a success message,
  not an error — same pattern as sub-project 1's `config.Load`/`ErrNotFound`
  distinguishing "expected absence" from real failure.
- Every network/checksum/extraction failure reports via
  `cliutil.PrintError`, wrapped with `%w` through each layer, no partial
  state left in `versions/node/` or `current/node`.
- `context.Context` threads through `ListRemoteVersions` and `Install` (the
  two network-touching calls) so they're cancellable; 2a does not add a
  `--timeout` CLI flag, just the plumbing — `context.Background()` from
  `cmd/lang.go` for now.
- No `--dry-run` in this phase: install/uninstall are the first
  filesystem-mutating commands in the CLI, but neither is as destructive
  as `workspace clean`/`archive` (sub-project 3, where `--dry-run` was
  already scoped) — installing writes into a fresh directory and
  uninstalling only ever removes `versions/<lang>/<name>`, a
  single well-known directory the command just named back to the user.

## Testing approach

- `internal/downloader`: `httptest.Server` serving a small fixed byte
  string; tests cover success (checksum matches), checksum mismatch (file
  removed, error returned), and a server error (404) case.
- `internal/installer`: build a tiny `.tar.gz` and a tiny `.zip` in the
  test itself (via `archive/tar`/`archive/zip` + `compress/gzip`,
  in-memory or `t.TempDir()`), extract with `ExtractAtomic`, assert
  contents; a re-extraction over an existing `destDir` replaces it
  cleanly; a corrupt archive leaves `destDir` untouched.
- `internal/runtime/node`: the provider's HTTP base URL is overridable;
  tests stand up an `httptest.Server` serving a fixed `index.json` fixture
  (a handful of entries spanning several majors, including an LTS one and
  one for a platform/arch combination that won't match the test's
  `GOOS`/`GOARCH` so the filtering logic is exercised) plus fixture
  tarball/zip bytes and a matching `SHASUMS256.txt`, all under
  `t.TempDir()`-backed `DEV_HOME`. No real network access in the suite.
- `cmd/lang.go`: tests exercise the command tree against a `Manager` with
  a stub in-memory `Runtime` implementation (not the real Node provider),
  verifying routing (unknown language errors, no-arg "all languages"
  iteration, alias wiring) without touching the network at all — the real
  Node provider's own package tests are what verify Node-specific
  behavior.
- All filesystem-touching tests use `t.TempDir()` and `DEV_HOME` overrides,
  per sub-project 1's established pattern; no test touches the real user
  filesystem or network.

## Open items carried to implementation

- Node's tarball's single top-level directory must be stripped during
  extraction (see Install, above) — this is Node-specific tarball
  structure, not a general `installer.ExtractAtomic` behavior, so the
  strip happens in the node provider (e.g. by extracting to a temp dir and
  moving the one child up) rather than being a parameter on
  `ExtractAtomic` itself.
- `versions/node/` and `current/` do not exist on a clean `DEV_HOME` yet
  (sub-project 1 never created them) — `Install`/`Activate` must
  `filesystem.EnsureDir` their parent before writing.

## Acceptance criteria for this sub-project

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run`,
  `go test ./...` all pass; `make check` succeeds.
- Against the real nodejs.org (manual verification, not part of the
  automated suite): `dev lang install node 22` downloads, verifies, and
  installs; running it again reports "already installed" without a
  network re-download; `dev lang use node 22` then `dev lang current node`
  shows `22`; `dev lang uninstall node 22` removes it and clears
  `current/node`; `dev lang install node bogus-version` fails with a
  clear error, no directory created.
- `dev lang list`, `dev lang installed`, `dev lang current` all work with
  and without a language argument.
- No test in the automated suite makes a real network call.
