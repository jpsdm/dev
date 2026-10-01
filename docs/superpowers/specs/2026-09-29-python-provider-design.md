# Python Provider — Design Spec

Date: 2026-09-29
Status: Approved for planning
Sub-project: 2c (continued — Java shipped 2026-09-28, Go shipped 2026-09-29;
Python is next; Rust remains deferred)

## Context

Sub-project 2a shipped the `Runtime` interface, the `Manager` registry, and
a Node.js provider. Java and Go followed. The user has now asked for a
Python provider — Rust remains deferred.

Python has no single official prebuilt-binary distributor the way Node.js
(nodejs.org) and Java/Temurin (Adoptium) do: python.org ships source
tarballs and OS-native installers (macOS `.pkg`, Windows `.exe`/`.msi`),
not portable archives for arbitrary Linux/macOS/Windows × amd64/arm64
targets. The de facto standard portable-binary source — used by `uv`,
`rye`, `pyenv`'s `python-build`, and GitHub Actions' `setup-python` — is
**python-build-standalone** (PBS, `astral-sh/python-build-standalone` on
GitHub, formerly `indygreg/python-build-standalone`). Verified live against
the real GitHub Releases API before this design, the same way the Node and
Java APIs were verified before those providers.

## Goals

- `dev lang install python 3.12`, `dev lang use python 3.12`, `dev lang
  list python`, `dev lang installed python`, `dev lang current python`,
  `dev lang uninstall python 3.12` all work, exactly the way the
  equivalent `node`/`java`/`go` commands already do — no changes outside
  `internal/runtime/python` and the one-line registration in
  `internal/providers`.
- Shims for `python3`, `python`, `pip3`, and `pip` work via `dev setup` on
  all three supported platforms, with a documented, honest gap on Windows
  (see below) rather than a silent failure shape different from the
  existing "binary not found" error.
- Checksum-verified downloads, atomic extraction, and the same
  install/uninstall/activate idempotency guarantees the other providers
  already have — no new risk class introduced.

## Non-goals (deferred)

- Rust — still deferred.
- Any virtualenv/venv management, `pip install` orchestration, or
  per-project Python version files (e.g. `.python-version`) — out of scope,
  matches how no other provider does this either. `dev` installs
  interpreters; it does not manage project dependencies.
- PBS's `full` and `_stripped` archive variants, debug builds, or
  free-threaded (`3.13t`/`3.14t`) builds — only the plain `install_only`
  variant of the standard build is exposed.
- musl-libc Linux builds — only `*-unknown-linux-gnu` is used (PBS's own
  guidance: gnu builds load compiled C extensions correctly; musl is for
  musl-based distros like Alpine, which this project doesn't specifically
  target).
- x86_64 microarchitecture-level variants (`x86_64_v2`/`v3`/`v4`) — only
  the baseline `x86_64` triple is used, for maximum compatibility. Revisit
  only if a real performance complaint surfaces.

## Real distribution research (verified live against GitHub, 2026-09-29)

- `GET https://api.github.com/repos/astral-sh/python-build-standalone/releases/latest`
  → one call, returns the newest release (tagged by date, e.g. `20260929`)
  with every asset for every supported CPython minor × platform combination
  embedded inline in one JSON array (897 assets in the release verified
  live — no pagination needed for a single release fetch, unlike a
  releases-list call). One release covers several CPython minors at once
  (verified: `3.10.21`, `3.11.16`, `3.12.14`, `3.13.15`, `3.14.7` all in the
  same `20260929` release) — there's no separate per-major "available
  releases" endpoint the way Adoptium has, so `ListRemoteVersions` parses
  asset filenames directly, the same way Node parses `index.json` entries.
- Asset filenames: `cpython-<X.Y.Z>+<tag>-<triple>-<variant>.tar.gz`, e.g.
  `cpython-3.12.14+20260929-x86_64-unknown-linux-gnu-install_only.tar.gz`.
  **Every platform uses `.tar.gz`, including Windows** — verified live
  (no `.zip` variant exists) — simpler than Node, no archive-format branch
  needed.
- Checksums ship as a single `SHA256SUMS` text asset per release (standard
  `sha256sum` format, `<hex>  <filename>`), covering all ~900 assets in
  that release — same "index, then a separate checksums-file fetch" shape
  Node's `SHASUMS256.txt` already established, just for `.tar.gz` archives
  on every platform instead of only Linux/macOS.
- Platform triples (LLVM-style, verified against the real asset list for
  exactly this project's six supported OS/arch combinations):

  | `platform.OS()` | `platform.Arch()` | PBS triple |
  |---|---|---|
  | linux | amd64 | `x86_64-unknown-linux-gnu` |
  | linux | arm64 | `aarch64-unknown-linux-gnu` |
  | darwin | amd64 | `x86_64-apple-darwin` |
  | darwin | arm64 | `aarch64-apple-darwin` |
  | windows | amd64 | `x86_64-pc-windows-msvc` |
  | windows | arm64 | `aarch64-pc-windows-msvc` |

  **Real platform gap** (same shape as Java's per-major platform gaps):
  `aarch64-pc-windows-msvc` only exists starting with Python 3.11 — older
  minors have no Windows/arm64 build. `ListRemoteVersions` filters to what
  actually exists for the host, so this surfaces as "3.10 isn't offered on
  this machine" rather than an install-time failure — see below.
- Archive layout (verified live by streaming `tar tzf` over the real
  download for both a Linux and a Windows asset): a single top-level
  `python/` directory on **every** platform, including Windows — no
  Java-style OS-specific nesting split needed. Unix binaries live flat at
  `python/bin/<name>` (`python`, `python3`, `python3.12`, `pip`, `pip3`,
  `pip3.12`, ...). Windows has `python/python.exe` at the archive root
  (not under `bin/`) and, critically, **no `pip.exe`/`pip3.exe` at all** —
  `install_only` Windows builds ship the `pip` module but no prebuilt
  console-script entry point for it (verified: `python/Scripts/` exists
  but is empty except a placeholder file).
- `install_only` vs `install_only_stripped` vs `full`: PBS's own README
  states "most users should choose an `install_only` archive" — that
  settles the variant choice without needing to reverse-engineer the
  difference further.

## `internal/runtime/python/python.go`

Mirrors `internal/runtime/node/node.go`'s structure (same "index +
separate checksum file" shape) more closely than Java's (which gets a
checksum inline). Same `baseURL string` override field for tests, same
`New()` constructor, same per-language package directory convention.

```go
type Python struct {
	baseURL string // overridable in tests; defaults to "https://api.github.com/repos/astral-sh/python-build-standalone"
}

func New() *Python
func (p *Python) Name() string // "python"

func (p *Python) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error)
// One GET to baseURL+"/releases/latest". Parses every asset name, keeps
// only those matching this host's triple and the "-install_only.tar.gz"
// suffix (not "_stripped", not "_full"), extracts the "X.Y" from each
// matching "X.Y.Z" and returns one Version per distinct minor, newest
// first. LTS is always false — Python has no LTS concept.

func (p *Python) ListInstalledVersions() ([]devruntime.Version, error)
func (p *Python) CurrentVersion() (*devruntime.Version, error)
// Identical in structure to Node's/Java's — local versions/python/ and
// current/python reads, including the same "." / ".old" directory
// filtering already established as a fixed defect class.

func (p *Python) Install(ctx context.Context, name string) error
func (p *Python) Uninstall(name string) error
func (p *Python) Activate(name string) error
func (p *Python) ShimNames() []string // ["python3", "python", "pip3", "pip"]
func (p *Python) BinaryPath(versionDir, binName string) (string, error)
```

### OS/arch mapping

```go
// pythonOS/pythonArch map platform.OS()/Arch() to PBS's LLVM-triple
// vocabulary. Split into mapPythonOS/mapPythonArch pure functions (same
// reason as Java's mapJavaOS/mapJavaArch) so the unsupported-value branch
// is directly testable regardless of host platform.
func pythonOS() (string, error)   // linux, darwin, windows
func pythonArch() (string, error) // amd64, arm64

// pythonTriple combines both into PBS's exact asset-filename triple,
// e.g. "x86_64-unknown-linux-gnu". Table above, verified live.
func pythonTriple() (string, error)
```

### `resolveRelease` — parse the release JSON, no per-version API call

```go
type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type release struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

// fetchLatestRelease retrieves and parses baseURL+"/releases/latest".
func (p *Python) fetchLatestRelease(ctx context.Context) (release, error)

// pythonVersionRE extracts the full "X.Y.Z" version and validates a
// candidate asset name matches exactly
// "cpython-X.Y.Z+<tag>-<triple>-install_only.tar.gz" for the given
// triple — both selecting the right variant (not "_stripped"/"_full")
// and, critically, validating the filename shape before any part of it
// is used as a path component (the download filename flows into
// archivePath via filepath.Join in Install, same risk class Java's
// asset.Binary.Package.Name check exists for).
func matchAsset(assetName, triple string) (fullVersion string, ok bool)

// resolveRelease finds, among the latest release's assets, the one
// install_only asset whose triple matches this host and whose "X.Y"
// matches name, returning its exact "X.Y.Z" version and download URL.
// A zero-value result (nil error) means no matching release was found —
// reported by the caller as "no release found", mirroring Node's and
// Java's handling of an unrecognized/unavailable version.
func (p *Python) resolveRelease(ctx context.Context, name string) (fullVersion, downloadURL string, err error)

// fetchChecksum retrieves the release's SHA256SUMS asset and returns the
// sha256 checksum listed for filename. Structurally identical to Node's
// fetchChecksum (same bufio.Scanner two-field-per-line parse), just
// pointed at a release-level asset instead of a per-version directory
// URL.
func (p *Python) fetchChecksum(ctx context.Context, rel release, filename string) (string, error)
```

`fullVersion` (e.g. `"3.12.14"`) is what flows into the `.dev-release`
marker and the download filename — both are validated with
`devruntime.ValidVersionName` before use, same as every other provider's
externally-sourced path component.

### `Install`

Same structure and same offline-fallback behavior as Node's/Java's
`Install`: if `resolveRelease` fails outright (network down, GitHub
unreachable) but a `.dev-release` marker already exists for this
major.minor, report the cached release and succeed rather than
hard-failing an already-installed line. Extraction reuses
`installer.ExtractAtomic` plus the same "strip one top-level directory,
rename-aside-then-swap" `extractStrippingTopLevel` helper pattern already
established in both Node and Java (copied, not shared — same reasoning
as Java's spec gives for not factoring it into a common package).

Unlike Node/Java, there is no archive-format branch: every PBS asset is
`.tar.gz`, so `ExtractAtomic`'s own extension dispatch always takes the
tar path here regardless of host OS.

### `BinaryPath`

```go
// binaryPathForOS resolves binName's path inside an installed Python
// version for a given pythonOS() value. Unix: bin/<name>. Windows:
// <name>.exe at the version root for "python"/"python3" (PBS's Windows
// archives put python.exe at the top level, not under bin/); Scripts/
// <name>.exe for "pip"/"pip3" — PBS's install_only Windows builds ship
// no pip executable at all, so this candidate path legitimately doesn't
// exist and BinaryPath's existing "not found" check reports that
// honestly, the same way it would for any other missing binary. No
// special-case error text needed — this is the same code path as a
// version simply not having a requested binary.
// Kept separate from BinaryPath so each OS branch is directly testable
// regardless of the host platform running the tests (same reason as
// Java's binaryPathForOS).
func binaryPathForOS(osName, versionDir, binName string) string

func (p *Python) BinaryPath(versionDir, binName string) (string, error)
```

## Registration

`internal/providers.Register` gains one line (`m.Register(python.New())`).
`cmd/lang.go` and `internal/shim` need zero changes.

## Error handling & idempotency

Identical guarantees to the other providers, for the same reasons (same
underlying `installer`/`downloader` packages, same idempotency contract):
re-running `Install` with an already-installed minor is a no-op network
call avoided via the release marker; a checksum mismatch or extraction
failure leaves no partial install; `Uninstall` on a non-installed version
errors without touching disk; `Activate` requires the version already be
installed.

## Path-safety

Every string from the release JSON or `SHA256SUMS` that reaches a
filesystem path — the matched asset's filename and the resolved
`fullVersion` — is validated with `devruntime.ValidVersionName` before
`Install` uses it in `filepath.Join` or writes it to the `.dev-release`
marker, per this project's standing rule (two real path-injection findings
already caught by review in other providers).

## Testing approach

Mirrors `internal/runtime/node/node_test.go`'s structure:
`Python{baseURL: server.URL}` against an `httptest.Server`, JSON fixtures
shaped exactly like the real `releases/latest` response documented above
(asset names copied verbatim from the live research, not guessed) plus a
`SHA256SUMS`-shaped text fixture. Additional coverage specific to Python:

- `matchAsset` rejecting `_stripped`/`_full` variants and a
  path-traversal-shaped filename.
- The Windows `pip`/`pip3` "no binary shipped" case in `BinaryPath` —
  asserting the existing not-found error, not a crash or a different
  error shape.
- `ListRemoteVersions` correctly filtering out a minor whose only asset is
  for a different triple (the Windows/arm64-before-3.11 gap), using a
  fixture release that includes such a minor.

## README update

Add a sentence alongside the existing Java platform-gap note: PBS's
`install_only` Windows builds ship no `pip`/`pip3` executable, so those
shims are unavailable on Windows — `python3 -m pip` works there instead.
Also note the Windows/arm64-before-3.11 gap next to Java's own listed gaps.

## Acceptance criteria

```bash
dev lang list python
dev lang install python 3.12
dev lang use python 3.12
dev lang installed python
dev lang current python
dev lang uninstall python 3.12
```

Plus, after `dev setup`, `python3 --version` and `pip3 --version` (or
`python --version` on Windows, where `pip`/`pip3` are expected to report
"not found" per the documented gap) via the shims, on whatever platform
this is verified on.
