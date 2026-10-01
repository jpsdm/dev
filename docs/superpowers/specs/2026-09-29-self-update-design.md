# Self-Update System — Design Spec

Date: 2026-09-29
Status: Approved for planning

## Context

`dev` has no way to update itself once installed. A user has to notice a new release
exists (by checking GitHub manually), download the archive, and re-run through
`dev setup`'s relocation flow by hand. There is also no signal anywhere in the CLI that
a newer version exists — `dev version` prints only the running binary's own version,
and every other command is silent about it too.

This adds three things: an explicit `dev update` command that checks GitHub and
replaces the running binary in place; a passive notice on `dev version` and on every
other command when a newer release is available; and caching/throttling so the passive
checks don't hit GitHub's API on every single invocation.

## Goals

- `dev update` fetches the latest published GitHub release, and — after confirming with
  the user — downloads, checksum-verifies, and installs it in place of the currently
  running `dev`, including refreshing every shim in `$DEV_HOME/bin` so `node`/`java`/etc.
  are instantly the new version too.
- `dev version` shows a one-line notice when a newer version is available.
- Every other `dev` command shows the same one-line notice (to stderr, after the
  command's own output) when a newer version is available, without ever blocking or
  visibly slowing the command down.
- Passive checks (the ones behind `dev version`'s notice and the other-command notice)
  are throttled: at most one real GitHub API call per 24 hours, cached in `config.json`.
  `dev update` itself always checks fresh, ignoring the cache, since the user explicitly
  asked for a check right now.
- A network failure during a passive check is silently swallowed — it must never cause
  a visible error, delay, or crash in an otherwise-successful command.
- `DEV_NO_UPDATE_CHECK=1` disables the passive check entirely (no cache read, no
  network call) — useful for CI and scripting.
- A locally-built `dev` (a `git describe`-style version like `v0.2.1-3-gabc1234-dirty`,
  not a clean release tag) never triggers a check or a notice — comparing it against a
  release tag would be noisy or misleading, and it isn't meant to auto-update.

## Non-goals

- Pinning or downgrading to a specific version — `dev update` only ever moves to
  "latest." No version argument, no rollback command.
- Updating without asking — `dev update` always confirms `[y/N]` before replacing
  anything, matching this project's existing pattern (`dev setup`'s every mutating
  step already confirms). A `--yes` flag for scripting is a plausible future addition,
  not part of this spec.
- Any change to `dev setup` itself — this is a separate command and a separate code
  path, not folded into setup's existing relocation flow.
- System-wide/admin-level anything — not applicable here; `dev update` only ever
  touches files already owned by the current user under `$DEV_HOME`.
- A relaunch-helper mechanism (Approach B considered and rejected during design) — the
  rename-based swap reuses this project's own already-established pattern instead.

## Files

- **`internal/update/update.go`** (new) — the GitHub client (`baseURL`-overridable,
  matching `internal/runtime/node`/`java`'s established pattern) and version comparison.
- **`internal/update/update_test.go`** (new) — `httptest.Server`-backed tests for the
  client, plus pure unit tests for version comparison and the "is this a clean release
  version" check.
- **`internal/update/apply.go`** (new) — the download-verify-swap mechanics `dev update`
  and nothing else calls (shim refresh stays a `cmd`-package concern — see "PATH/shim
  consistency" below).
- **`internal/update/apply_test.go`** (new) — real-filesystem tests for the rename/swap
  logic (temp dirs, real `os.Rename`), and a cross-compile-only note for the one
  Windows-specific unknown (see Testing approach).
- **`internal/config/config.go`** (modified) — `Config` gains an `UpdateCheck` field;
  `Load`/`Save` already handle arbitrary new fields via plain JSON (un)marshaling, so
  this is a pure additive change, not a migration.
- **`cmd/update.go`** (new) — the `dev update` command.
- **`cmd/update_test.go`** (new).
- **`cmd/root.go`** (modified) — gains a `PersistentPostRunE` (a sibling to the existing
  `PersistentPreRunE`, which already hosts the install-location gate — `PersistentPreRunE`
  itself is unchanged) that computes and prints the passive notice after every command
  succeeds, for every command except `update` itself (which does its own fresh check).
  `cmd/version.go` is deliberately **not** modified: `PersistentPostRunE` fires for every
  command including `version`, so it alone decides the notice's destination — stdout
  (as part of `dev version`'s own output) when `cmd.Name() == "version"`, stderr
  otherwise — rather than having `version.go`'s own `RunE` print it too, which would
  show the same notice twice (once from `RunE`, once from `PersistentPostRunE` firing
  afterward regardless).
- **`cmd/root_test.go`** (modified).
- **`README.md`** (modified) — document `dev update`, the passive notice, and
  `DEV_NO_UPDATE_CHECK`.

## `internal/update` — the GitHub client and version comparison

```go
// Client fetches the latest published GitHub release for dev and
// compares it against a running version. baseURL is overridable in
// tests — the same pattern internal/runtime/node and internal/runtime/java
// already use for their own upstream APIs.
type Client struct {
	baseURL string // defaults to apiBaseURL; overridable in tests
}

func NewClient() *Client // baseURL: apiBaseURL = "https://api.github.com"

// LatestRelease is the subset of GitHub's release JSON this package
// actually uses.
type LatestRelease struct {
	TagName string // e.g. "v0.2.1"
	Assets  []ReleaseAsset
}
type ReleaseAsset struct {
	Name               string
	BrowserDownloadURL string
}

// FetchLatest calls GET /repos/jpsdm/dev/releases/latest — the
// endpoint that returns the latest published, non-draft, non-prerelease
// release, matching this project's own "GoReleaser drafts it, you
// publish it manually" release flow.
func (c *Client) FetchLatest(ctx context.Context) (*LatestRelease, error)

// IsCleanVersion reports whether v looks like a plain "vMAJOR.MINOR.PATCH"
// release tag (no git-describe suffix like "-3-gabc1234" or "-dirty").
// A version that isn't clean is a local/dev build — the update system
// never checks or notifies for one.
func IsCleanVersion(v string) bool

// NewerThan reports whether candidate is a newer clean release version
// than current. Both must be clean (see IsCleanVersion) — the caller
// is responsible for checking current with IsCleanVersion first; a
// non-clean candidate (shouldn't happen — it's always GitHub's own
// tag_name) is treated as not newer, defensively.
func NewerThan(candidate, current string) bool
```

`NewerThan`/`IsCleanVersion` are pure functions, parsing `vMAJOR.MINOR.PATCH` by hand
(split on `.`, strip the leading `v`, parse three integers) rather than pulling in a
semver library — this project's tags never carry pre-release suffixes, and CLAUDE.md's
own stated policy is stdlib-first, a dependency only when it earns its place.

## Caching — `internal/config`

```go
type UpdateCheck struct {
	LastChecked   time.Time `json:"last_checked"`
	LatestVersion string    `json:"latest_version"`
}

type Config struct {
	Workspace   WorkspaceConfig `json:"workspace"`
	UpdateCheck UpdateCheck     `json:"update_check,omitempty"`
}
```

A new small helper in `internal/update` (not `internal/config`, to keep `config`
purely a load/save package with no business logic — `internal/update` already owns
"what does a fresh check even mean"):

```go
// CheckInterval is how long a cached check result stays valid before
// a passive caller (dev version, the other-command notice) does a
// real GitHub call again. dev update always bypasses this.
const CheckInterval = 24 * time.Hour

// CachedNotice returns the version to notify about (empty if none, or
// if the cached result is stale and refresh is false), reading cfg's
// UpdateCheck. When the cache is stale, it performs one real
// FetchLatest call and returns an updated *config.UpdateCheck for the
// caller to persist — callers that can't/shouldn't write (a network
// failure, or the check being disabled) get a nil update and should
// leave the config untouched.
func (c *Client) CachedNotice(ctx context.Context, current string, cached config.UpdateCheck) (notice string, updated *config.UpdateCheck)
```

The passive-check call site (`cmd/root.go`'s `PersistentPostRunE`, which — unlike
`PersistentPreRunE` — runs after the command's own work, only on success): read
`config.json` once, call `CachedNotice`, print the one-line notice if non-empty
(stdout for `dev version`, stderr for everything else — see Files above), and persist
`updated` back to `config.json` if non-nil — all
wrapped so any error (config load/save failure, network failure) is logged only at
`--verbose` (via `cliutil.Verbosef`) and never surfaces as a command failure. Skipped
entirely, before touching config or network, when `DEV_NO_UPDATE_CHECK` is set, when
`cmd.Name()` is `"update"` (has its own fresh-check flow), when
`platform.RunningFromDevHome()` reports `false` (suggesting an update before `dev` is
even installed doesn't make sense — this also covers `"setup"` and `"help"` for free,
since neither runs from `$DEV_HOME` on a fresh, not-yet-installed copy, and once
installed there's no reason to exempt them by name specifically), or when `cmd.Version`
(this binary's own version) fails `IsCleanVersion`.

## `dev update` — `cmd/update.go` and `internal/update/apply.go`

```go
// ApplyUpdate downloads, checksum-verifies, and installs release
// (already fetched via Client.FetchLatest) in place of the binary
// currently at devHome. Does not touch $DEV_HOME/bin's shims — the
// caller (cmd/update.go) refreshes those afterward, the same way
// dev setup already does (see "PATH/shim consistency" below).
// Reuses internal/downloader.Download (checksum-first) and
// internal/installer.ExtractAtomic (into a throwaway temp directory —
// only the single dev/dev.exe binary inside is used, the rest of
// ExtractAtomic's directory-swap semantics are incidental here, not
// load-bearing).
func ApplyUpdate(ctx context.Context, out io.Writer, release *LatestRelease, devHome string) error
```

Steps: pick the asset matching `dev_{runtime.GOOS}_{runtime.GOARCH}.{tar.gz|zip}` (the
exact naming `.goreleaser.yml` already produces) plus the `checksums.txt` asset; download
`checksums.txt` first (small, no checksum needed for it — GoReleaser's own file, fetched
straight from the release, not attacker-influenceable in a way a checksum would defend
against, matching how `internal/downloader` is only used for the payload that actually
gets executed); parse out the line matching the archive's filename; `downloader.Download`
the archive against that sha256; `installer.ExtractAtomic` into a fresh
`os.MkdirTemp`-scratch dir; read `dev`/`dev.exe` out of it.

Then the swap, mirroring `installRelocation`'s existing rename-then-best-effort-delete
shape:

1. `os.Rename($DEV_HOME/dev[.exe], $DEV_HOME/dev.old[.exe])`. If this fails, abort the
   whole update with a clear error — nothing has been placed yet, so aborting here
   leaves the existing install exactly as it was.
2. `os.Rename(<scratch>/dev[.exe], $DEV_HOME/dev[.exe])` — the new binary takes the
   original name.
3. Best-effort `os.Remove($DEV_HOME/dev.old[.exe])`. On failure, the exact same
   friendly, non-alarming message this session already built for relocation leftovers:
   `"dev.old is set up and safe to delete — you can remove <path> now"` — never an
   alarming "could not remove" framing, since this is expected to fail often on
   Windows (a running process can't delete its own executable image) and is
   functionally harmless either way.

`cmd/update.go`'s `RunE`: `platform.RunningFromDevHome()` check happens automatically
via `PersistentPreRunE` (update is not exempted, see Files above) — the command body
itself: fetch fresh (`Client.FetchLatest`, ignoring cache), compare with `IsCleanVersion`
+ `NewerThan`; if not newer, `cliutil.Fsuccess(out, "Already on the latest version (%s).", current)`
and return; if newer, print `"%s → %s"`, confirm via `shell.Confirm` (existing helper,
same pattern as every other mutating prompt in this codebase); on confirmation, call
`ApplyUpdate` (swaps the `dev` binary only — see "PATH/shim consistency" below for why),
then call the existing `installShims(cmd)` (the same call `dev setup` already makes) so
every shim is refreshed to match, then persist `config.json`'s `UpdateCheck` with
`LatestVersion` = the version just installed (so the passive notice doesn't immediately
re-fire), then print `"Updated to %s."`.

## PATH/shim consistency

`ApplyUpdate` (in `internal/update`) only swaps the `dev` binary itself — it does not
know what a "shim" is, and it shouldn't: `internal/update` knows how to fetch and swap
a binary, `cmd` owns what shims are and how to (re)install them. `cmd/update.go`'s
`RunE` calls the existing unexported `installShims(cmd)` (already defined in
`cmd/setup.go`, already reused as-is with no changes) right after a successful
`ApplyUpdate` returns, exactly the same call `dev setup` already makes — so
`node`/`npm`/`npx`/`java`/`javac` are all the new version immediately, with no
duplicated copy-loop and no new package boundary crossed.

## Testing approach

The GitHub client (`FetchLatest`) is fully testable via `httptest.Server`, matching
`internal/runtime/node`/`java`'s established pattern — real HTTP round-trips against a
local test server, real JSON parsing, no mocks. `IsCleanVersion`/`NewerThan` are pure
functions, tested with table-style cases (equal, newer, older, malformed, `-dirty`
suffix, missing `v` prefix). `CachedNotice`'s throttling logic is testable with a fake
`time.Now` seam (a `var now = time.Now` package var, overridable in tests — the same
pattern this codebase already uses for `platform.Executable`) and a `httptest.Server`
standing in for a real "GitHub is reachable" check.

The rename/swap mechanics in `ApplyUpdate` are testable on Linux/macOS with real temp
files, real `os.Rename` calls, and a `httptest.Server` serving a fake release +
checksums.txt + archive — this genuinely exercises the download-verify-extract-swap
pipeline end to end, not just each piece in isolation. The one thing this sandbox
cannot verify at runtime is whether `os.Rename`ing a *currently-executing* binary's
file behaves the same on Windows as renaming an arbitrary file (this project already
hit an equivalent unverifiable-here case with the Windows registry code, handled the
same way): `GOOS=windows GOARCH=amd64 go build ./...` proves it type-checks and links,
and a manual verification checklist is handed to the user to run once on a real Windows
machine after implementation.

## Acceptance criteria

```bash
# Passive notice, throttled:
dev lang list                     # first run: one real GitHub call, cached
dev lang list                     # second run within 24h: no network call, same notice
DEV_NO_UPDATE_CHECK=1 dev lang list  # no notice, no network call, no config read

# dev version:
dev version                       # shows current version; an extra line if newer exists

# dev update, from an installed copy:
dev update                        # always checks fresh; "Already on the latest..." or
                                   # shows old -> new, confirms, swaps dev + all shims

# dev update, from outside $DEV_HOME:
./dev update                      # refused with the same "not installed" message every
                                   # other ordinary command gets — update requires
                                   # already being installed
```
