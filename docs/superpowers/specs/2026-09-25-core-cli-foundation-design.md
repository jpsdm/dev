# Core CLI Foundation — Design Spec

Date: 2026-09-25
Status: Approved for planning
Sub-project: 1 of 4 (`dev` CLI — Development Environment Manager)

## Context

`dev` is a new, standalone CLI (Go) that will eventually manage language/runtime
versions, a developer workspace, and its own multiplatform releases. This is the
first of four sub-projects that build it incrementally:

1. **Core CLI foundation** (this spec)
2. Runtime/language management (`dev lang ...`)
3. Workspace management (`dev workspace ...`)
4. Release automation (cross-platform builds, GitHub Releases)

This spec covers only sub-project 1: the scaffold everything else depends on —
repo structure, config, platform/filesystem abstractions, the root command,
`dev version`, error/output conventions, Makefile, and CI. No `lang` or
`workspace` commands are implemented here.

The full product requirements (all 4 sub-projects) are recorded in the
project's driving brief (user-supplied); this spec operationalizes only the
Phase 1 slice of it.

## Goals

- A buildable, testable, lintable Go module named `dev` that runs and prints
  its version.
- Reusable foundations (`config`, `platform`, `filesystem`, `cliutil`) that
  later phases build on without rework.
- A CI pipeline that enforces fmt/vet/lint/test/build on every PR and push.
- Idiomatic, dependency-light Go — stdlib first, add a library only when it
  clearly earns its place (Cobra and Huh are the two already justified by the
  overall project's stack decision).

## Non-goals (deferred to later phases)

- `dev lang` and its subcommands (sub-project 2).
- `dev workspace` and its subcommands (sub-project 3).
- Shims, PATH wiring, `dev env`, `dev setup` (sub-project 2).
- Cross-platform release builds, GoReleaser config, `release.yml` (sub-project 4).
- Any actual runtime download/install logic.

## Repository layout

```text
dev/
├── cmd/
│   ├── root.go        # root command, persistent flags, error handling
│   └── version.go      # `dev version` subcommand
├── internal/
│   ├── config/         # Config struct, Load/Save (JSON), DefaultPath()
│   ├── platform/        # OS/arch detection, DEV_HOME resolution, path layout
│   ├── filesystem/      # EnsureDir, Exists, atomic write helpers
│   └── cliutil/         # output helpers (✓/✗/→), verbose-aware logging, error formatting
├── .github/
│   └── workflows/
│       └── ci.yml
├── .golangci.yml
├── go.mod
├── go.sum
├── Makefile
├── VERSION               # starts at "0.1.0"
├── LICENSE                # MIT
├── README.md              # stub: what `dev` is, build instructions; expanded in later phases
└── main.go                # thin entrypoint calling cmd.Execute()
```

Tests are colocated as `*_test.go` next to the code they test (idiomatic Go),
not in a separate `tests/` directory. A `tests/` tree is reserved for future
cross-package integration/e2e tests, introduced only when such a test
actually needs it (sub-project 2+).

## Module & tooling

- Module path: `github.com/jpsdm/dev`
- `go.mod` `go` directive: `1.23` (a safe floor that exists today); CI uses
  `actions/setup-go` with `go-version: 'stable'` so builds always run against
  the current stable toolchain rather than a hardcoded, possibly-stale
  version number.
- `CGO_ENABLED=0` for all builds (reproducibility, no C toolchain dependency).
- License: MIT.

## `internal/platform`

Responsible for OS/arch detection and the on-disk layout rooted at `DEV_HOME`.

- `DevHome() (string, error)` — resolves `DEV_HOME` env var if set, otherwise
  `$HOME/.dev` via `os.UserHomeDir()`.
- `VersionsDir()`, `CurrentDir()`, `BinDir()`, `CacheDir()`, `ConfigDir() (string, error)`
  — typed accessors so no other package hardcodes `.dev/...` path segments.
- `OS() string`, `Arch() string` — thin wrappers over `runtime.GOOS`/`GOARCH`,
  kept as functions (not a struct) since there's nothing to inject yet; this
  gives later phases (downloader) a single seam to target if that ever needs
  to change (e.g., normalizing arch names for a specific vendor's release
  naming).

No symlink/shim logic here yet — that's sub-project 2, once there's a `current`
version to point at.

## `internal/config`

- `Config` struct — starts minimal:
  ```go
  type Config struct {
      Workspace WorkspaceConfig `json:"workspace"`
  }
  type WorkspaceConfig struct {
      Path string `json:"path"`
  }
  ```
  (Workspace path is unused until sub-project 3, but the shape from the brief
  is fixed now so the config file format doesn't change later.)
- `DefaultPath() (string, error)` — `platform.ConfigDir()` + `config.json`.
- `Load(path string) (*Config, error)` — returns zero-value `*Config` and a
  sentinel `ErrNotFound` (wrapped via `errors.Is`-compatible `%w`) if the file
  doesn't exist yet; callers decide whether that's an error or a "use
  defaults" signal.
- `Save(path string, cfg *Config) error` — creates parent dirs, writes
  pretty-printed JSON, uses a temp-file-then-rename pattern for atomicity.
- Plain functions, not an interface — there is exactly one implementation
  (JSON on local disk) and no current need to swap it; an interface here
  would be speculative.

## `internal/filesystem`

Minimal for this phase — only what config/platform actually need:

- `Exists(path string) bool`
- `EnsureDir(path string, perm os.FileMode) error`
- `WriteFileAtomic(path string, data []byte, perm os.FileMode) error` (temp
  file + rename, used by `config.Save`)

Deliberately not building copy-tree, gitignore-aware clean, or extraction
helpers here — those land in sub-projects 2 and 3 when there's a concrete
caller, per YAGNI.

## `internal/cliutil`

Output and error-reporting conventions shared by every command:

- `Success(msg string, args ...any)` → prints `✓ msg`
- `Error(msg string, args ...any)` → prints `✗ msg` (to stderr)
- `Step(msg string, args ...any)` → prints `→ msg` (used for in-progress
  steps like "Resolving...", "Downloading...")
- `Verbosef(msg string, args ...any)` → prints only when `--verbose`/`-v` is
  set; used for diagnostic detail, never required reading for normal use.
- A package-level `SetVerbose(bool)` called once from `root.go` after flag
  parsing.

Error presentation contract (enforced in `cmd/root.go`'s `Execute()`):

- Every `RunE` returns a plain Go `error` (wrapped with `%w` through layers
  as needed).
- On failure, `Execute()` prints `✗ <top-level message>` and, only when
  `--verbose` is set, follows with the full wrapped error chain (via
  `fmt.Sprintf("%+v")`-style unwrapping — implemented with stdlib
  `errors.Unwrap`, no third-party stacktrace/error library).
  Never a Go panic stack trace for an ordinary returned error.
- Exit code is non-zero (Cobra's default `SilenceUsage`/`SilenceErrors` are
  set on the root command so Cobra doesn't double-print; `Execute()` owns all
  error output).

## `cmd/root.go`

- Root command name `dev`, `Short`/`Long` description of the tool.
- Persistent flags: `--verbose` / `-v` (bool). No conflict with `--version`
  since that's a separate long flag Cobra manages independently, and `-v` is
  free to bind to verbose.
- `RootCmd.Version` set from the build-injected `Version` var, so Cobra's
  built-in `--version` flag works automatically.
- `SilenceUsage: true`, `SilenceErrors: true` (cliutil owns error printing).
- `Execute()` function called from `main.go`; on error, prints via
  `cliutil.Error` and calls `os.Exit(1)`.

## `cmd/version.go`

`dev version` subcommand, printing:

```text
dev version 0.1.0
commit: abc1234
build date: 2026-09-25
platform: linux/amd64
```

Backed by package vars in `cmd` (or a small `internal/buildinfo` package if
that reads cleaner once written):

```go
var (
    Version   = "dev"
    Commit    = "none"
    BuildDate = "unknown"
)
```

injected via `-ldflags "-X ...Version=... -X ...Commit=... -X ...BuildDate=..."`.

## `main.go`

Thin entrypoint: `func main() { cmd.Execute() }`. No logic beyond that.

## Makefile

Targets: `build`, `test`, `lint`, `fmt`, `vet`, `clean`, `check` (runs
`fmt vet lint test build` in order, stopping on first failure).

- `build` computes `VERSION` (from the `VERSION` file), `COMMIT` (`git
  rev-parse --short HEAD`, falling back to `none` if not in a git repo or no
  commits yet), and `BUILD_DATE` (`date -u +%Y-%m-%dT%H:%M:%SZ`), passing all
  three via `-ldflags`. `CGO_ENABLED=0`.
- `lint` invokes `golangci-lint run` (assumes it's installed; CI installs it
  explicitly via the official action, so the Makefile target itself doesn't
  attempt to install tooling).
- `fmt` runs `gofmt -l -w .` for local convenience; CI instead checks
  `gofmt -l .` produces no output (fails if it does), so CI never silently
  reformats.

## `.golangci.yml`

Minimal config enabling the standard/default linter set (no exotic linters
added speculatively); revisited if later phases surface real lint noise that
needs tuning.

## CI (`.github/workflows/ci.yml`)

Triggers: `pull_request` and `push` (to the default branch).

Pipeline, failing the workflow on any step's failure:

1. checkout
2. `actions/setup-go` with `go-version: 'stable'`
3. `go mod download`
4. `gofmt -l .` — fail if any file listed
5. `go vet ./...`
6. `golangci-lint` (via `golangci/golangci-lint-action`)
7. `go test ./...`
8. `go build ./...`

No release/publish steps here — that's `release.yml` in sub-project 4.

## Testing approach

- Stdlib `testing`, table-driven where it fits.
- `t.TempDir()` for anything touching the filesystem (config load/save,
  filesystem helpers) — never the real user filesystem.
- `platform` tests avoid hardcoding paths like `/home` or `~/Documents`;
  they use `t.Setenv("DEV_HOME", t.TempDir())` and `os.UserHomeDir()`-relative
  assertions instead.
- No Testify in this phase — stdlib assertions are sufficient for what's
  being tested; revisit only if a later phase's tests get meaningfully more
  readable with it.
- Tests run in parallel (`t.Parallel()`) where there's no shared state.

## Error handling & idempotency notes for this phase

- `config.Load` on a missing file is not a crash — it's a defined,
  recoverable condition (`ErrNotFound`), setting the pattern later phases
  (e.g., "is this runtime already installed?") will follow.
- Nothing in this phase is destructive or has side effects beyond writing
  the config file, so `--dry-run` is not needed yet; it becomes relevant
  starting with `workspace clean`/`archive` in sub-project 3.

## Open items carried to implementation

- Go toolchain is not currently installed on the dev machine this is being
  built on; the implementation plan must install it (system package manager)
  as an early step before any build/test can run locally.
- `git init` for this repository happens as part of implementation (this
  directory is not yet a git repo).

## Acceptance criteria for this sub-project

- `go build ./...` succeeds.
- `go test ./...` passes.
- `make check` runs fmt, vet, lint, test, build in order and succeeds.
- `dev --version` and `dev version` both print version/commit/build
  date/platform.
- `dev --help` / `dev -v <anything>` do not panic; `--verbose` visibly
  changes output on at least one exercised path (e.g., a deliberately
  triggered config load error).
- CI workflow runs on a pushed branch and passes.
