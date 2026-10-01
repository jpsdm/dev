# Go Provider — Design Spec

Date: 2026-09-29
Status: Approved for planning
Sub-project: 2c (continued — Java shipped 2026-09-28; Go is next; Python and
Rust remain deferred)

## Context

Sub-project 2a shipped the `Runtime` interface, the `Manager` registry, and
a Node.js provider; 2c added Java. The user has now asked for Go — Python
and Rust remain deferred, unchanged from the earlier decision.

Go is distributed by `go.dev/dl` itself (the Go team's own download
service), which exposes a single JSON index — verified live against
`https://go.dev/dl/?mode=json&include=all` before this design, the same
research discipline the Node and Java providers were held to.

## Goals

- `dev lang install go 1.24`, `dev lang use go 1.24`, `dev lang list go`,
  `dev lang installed go`, `dev lang current go`, `dev lang uninstall go
  1.24` all work, exactly the way the equivalent `node`/`java` commands
  already do — no changes anywhere outside `internal/runtime/go` and the
  one-line registration point (`internal/providers`).
- Shims for `go` and `gofmt` work via `dev setup`, running the
  currently-active Go version transparently on all three supported
  platforms.
- Checksum-verified downloads, atomic extraction, and the same
  install/uninstall/activate idempotency guarantees the Node and Java
  providers already have — no new risk class introduced.

## Non-goals (deferred)

- Python and Rust providers — still deferred.
- `GOPATH`/`GOBIN`/module-cache management, or any per-project Go tooling
  concept — out of scope, matches how neither Node nor Java provider
  manages `node_modules` or Maven repos either. `dev` only manages which Go
  toolchain is active, not anything about how it's used afterward.
- Go's own internal per-project toolchain-switching (the `go` command's own
  `GOTOOLCHAIN`/`go.mod`-directed auto-download of newer toolchains) is a
  separate, orthogonal mechanism this provider does not interact with or
  disable.

## Real API research (verified live against `go.dev/dl/?mode=json&include=all`, 2026-09-29)

- `GET /dl/?mode=json&include=all` → one call, returns an array of release
  objects:
  ```json
  {
    "version": "go1.27.1",
    "stable": true,
    "files": [
      {
        "filename": "go1.27.1.linux-amd64.tar.gz",
        "os": "linux",
        "arch": "amd64",
        "version": "go1.27.1",
        "sha256": "63d339f0...(64 hex chars)",
        "size": 70553950,
        "kind": "archive"
      }
    ]
  }
  ```
  Without `include=all` the endpoint returns only the most recent releases;
  `include=all` is required to list every line the way `dev lang list
  node`/`java` already do for their own languages. `stable` is a top-level
  per-release boolean — `false` for beta/rc builds (e.g. `go1.28rc1`),
  which must be filtered out. `kind` is `"archive"`, `"installer"` (`.msi`/
  `.pkg` — not used), or `"source"` (`os`/`arch` both empty — not used);
  filtering to `kind == "archive"` alone already excludes source tarballs
  without a separate check. **The checksum is embedded directly in each
  file entry** — like Java, unlike Node, no second checksum-file fetch
  needed.
- `os`/`arch` values are Go's own `GOOS`/`GOARCH` vocabulary
  (`linux`/`darwin`/`windows`, `amd64`/`arm64`/`386`/...) — because this
  *is* Go's own release-build matrix, `platform.OS()`/`platform.Arch()`
  (which are literally `runtime.GOOS`/`runtime.GOARCH`) match it directly.
  This is the one provider where the OS/arch "mapping" is an identity
  function rather than a real vendor-specific relabeling like Java's
  `arm64→aarch64`. Kept as explicit `goOS()`/`goArch()` functions anyway,
  for pattern consistency with the other two providers and as a defensive
  whitelist — not because a real relabeling is expected.
- Filenames follow `go<version>.<os>-<arch>.<ext>`, e.g.
  `go1.27.1.linux-amd64.tar.gz`, `go1.27.1.darwin-arm64.tar.gz`,
  `go1.27.1.windows-amd64.zip`. Windows is `.zip`; every other platform is
  `.tar.gz` — same two-way split Node and Java both already have.
- **Version-string format defensiveness (not fully verifiable live):**
  every version string I could directly confirm in the current index is
  three-component (`go1.27.0`, `go1.27.1` both exist as distinct real
  releases — the first patch of a line is *not* omitted the way some
  older Go tooling folklore suggests). I could not rule out a
  two-component form (`go1.21` with no patch) appearing somewhere in
  `include=all`'s full history, since the fetch tool summarizes/truncates
  very large responses rather than guaranteeing I saw every entry. The
  parsing regex below accepts both forms defensively rather than assuming
  one — cheap insurance, not overengineering.
- **Archive layout:** every Go archive extracts to a single top-level `go/`
  directory (`go/bin/go`, `go/bin/gofmt`, `go/src/`, `go/pkg/`, ...) — this
  is Go's own long-documented manual-install convention (`tar -C
  /usr/local -xzf goX.Y.Z.<os>-<arch>.tar.gz` produces `/usr/local/go`).
  Not independently verified from live archive bytes in this research pass
  (WebFetch cannot fetch and inspect binary archive contents), but this is
  well-established, unchanged Go distribution behavior, not a guess.
  Unlike Java, there's no macOS `Contents/Home` nesting — Go's layout is
  flat and identical across all three platforms.

## `internal/runtime/go/go.go`

Package name `golang` (`go` is a reserved word). Mirrors
`internal/runtime/java/java.go`'s structure as closely as the real API
differences allow.

```go
package golang

type Go struct {
	baseURL string // overridable in tests; defaults to "https://go.dev/dl"
}

func New() *Go
func (g *Go) Name() string // "go"

func (g *Go) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error)
// One GET to baseURL+"/?mode=json&include=all". Keeps stable==true releases
// with a kind=="archive" file matching this platform's os/arch. Groups by
// major.minor "line" (e.g. "1.24"); for each line, keeps the release with
// the numerically greatest patch — compared explicitly by parsed integer,
// not by trusting the index's own ordering (unlike Node's ListRemoteVersions,
// which documents relying on nodejs.org's index being newest-first; Go's
// API doesn't document an ordering guarantee, so this doesn't assume one).
// LTS is always false — Go has no LTS concept.

func (g *Go) ListInstalledVersions() ([]devruntime.Version, error)
func (g *Go) CurrentVersion() (*devruntime.Version, error)
// Identical in structure to Node/Java's — local versions/go/ and
// current/go reads, including the same "."/".old" filtering.

func (g *Go) Install(ctx context.Context, name string) error
func (g *Go) Uninstall(name string) error
func (g *Go) Activate(name string) error
func (g *Go) ShimNames() []string // ["go", "gofmt"]
func (g *Go) BinaryPath(versionDir, binName string) (string, error)
```

### `resolveRelease` — one call, like Java

```go
// goOS/goArch validate platform.OS()/Arch() against go.dev/dl's own
// vocabulary (which is the same vocabulary by construction — see research
// notes above). Same unsupported-value error shape nodeOS/javaOS use.
func goOS() (string, error)
func goArch() (string, error)

// lineRE matches a release's major.minor "line" and optional patch:
// "go1.24.3" -> line "1.24", patch 3; "go1.24" (defensive two-component
// form) -> line "1.24", patch 0. Non-matching strings (rc/beta suffixes,
// anything malformed) are excluded upstream by the stable==true filter
// and this regex's own failure to match.
var lineRE = regexp.MustCompile(`^go(\d+\.\d+)(?:\.(\d+))?$`)

// resolveRelease finds, among stable releases on line `name` (e.g. "1.24"),
// the one with the greatest patch number that has an archive for this
// platform, and returns its exact version string, download URL, filename,
// and checksum together. A zero-value goRelease (nil error) means no
// matching release was found — reported as "no release found" by the
// caller, the same convention Node/Java use for an unrecognized line.
func (g *Go) resolveRelease(ctx context.Context, name string) (release goRelease, err error)

type goRelease struct {
	Version  string // e.g. "go1.24.3" — used as the .dev-release marker content
	URL      string
	Filename string
	SHA256   string
}
```

`release.Version` and `release.Filename` both come from the same untrusted
JSON response and both flow into filesystem paths — validated via
`devruntime.ValidVersionName` before either is used, applying the lesson
from Java's original gap (where only the release name, not the sibling
filename field, was validated) from the start rather than after a review
catches it again.

### `Install`

Same structure and same offline-fallback behavior as Node/Java's `Install`
(if `resolveRelease` fails outright but a `.dev-release` marker already
exists for this line, report the cached release and succeed rather than
hard-failing an already-installed line). Extraction reuses
`installer.ExtractAtomic` plus the same "strip one top-level directory,
rename-aside-then-swap" `extractStrippingTopLevel` helper pattern, copied
(not shared) the same way Java's copy of Node's version is — matching this
codebase's established reasoning for why small, self-contained
extraction/validation helpers are duplicated per-provider rather than
factored into a shared package.

### `BinaryPath`

```go
// binaryPathForOS resolves binName's path for a given goOS() value. All
// three platforms use a flat bin/ layout (no macOS nesting quirk unlike
// Java) — Windows binaries carry a .exe suffix, everything else doesn't.
// Kept as its own function for pattern consistency and testability, even
// though the branches are simpler than Java's three-way split.
func binaryPathForOS(osName, versionDir, binName string) string

func (g *Go) BinaryPath(versionDir, binName string) (string, error)
```

## Registration

`internal/providers.Register` gains one line (`m.Register(golang.New())`),
matching Node and Java. `cmd/lang.go` and `internal/shim` need zero
changes.

## Error handling & idempotency

Identical guarantees to the Node and Java providers, for the same reasons
(same underlying `installer`/`downloader` packages, same idempotency
contract): re-running `Install` on an already-installed line is a no-op
network call avoided via the release marker; a checksum mismatch or
extraction failure leaves no partial install; `Uninstall` on a
non-installed version errors without touching disk; `Activate` requires
the version already be installed.

## Testing approach

Mirrors `internal/runtime/java/java_test.go`'s structure: `Go{baseURL:
server.URL}` against an `httptest.Server`, JSON fixtures shaped exactly
like the real `?mode=json&include=all` response documented above (not
guessed — copied from this spec's verified research), including a fixture
with both a `stable:false` rc entry and multiple patches on the same line,
to exercise the stability filter and the explicit-max-patch selection
together. Additional coverage specific to Go: `lineRE` matching both the
three-component and defensive two-component forms, and `resolveRelease`'s
path-safety validation rejecting a path-traversal-shaped `version` or
`filename`.

## Acceptance criteria

```bash
dev lang list go
dev lang install go 1.24
dev lang use go 1.24
dev lang installed go
dev lang current go
dev lang uninstall go 1.24
```

Plus, after `dev setup`, `go version` and `gofmt -h` via the shims, on
whatever platform this is verified on.
