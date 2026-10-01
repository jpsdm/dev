# Java Provider — Design Spec

Date: 2026-09-28
Status: Approved for planning
Sub-project: 2c (partial — Java only; Go/Python/Rust remain deferred, same as 2c itself was deferred until now)

## Context

Sub-project 2a shipped the `Runtime` interface, the `Manager` registry, and
a Node.js provider. The original brief (§5) also names Java, Python, Go, and
Rust as initial languages; this was deferred as "sub-project 2c" until now.
The user has asked to implement only the Java provider for this cycle —
Go, Python, and Rust remain deferred exactly as 2c itself was.

Java is distributed by Eclipse Temurin (Adoptium), the de facto standard
open-source OpenJDK build (successor to AdoptOpenJDK) — verified live
against `api.adoptium.net`'s real v3 API before this design, the same way
`nodejs.org/dist`'s real index was verified before the Node provider.

## Goals

- `dev lang install java 21`, `dev lang use java 21`, `dev lang list java`,
  `dev lang installed java`, `dev lang current java`, `dev lang uninstall
  java 21` all work, exactly the way the equivalent `node` commands already
  do — no changes anywhere outside `internal/runtime/java` and the two
  one-line registration points (`internal/providers`, this file only).
- Shims for `java` and `javac` (the brief's own example, §12) work via `dev
  setup`, running the currently-active Java version, transparently, on all
  three supported platforms — including macOS's real binary layout, which
  differs from Linux/Windows (see below).
- Checksum-verified downloads, atomic extraction, and the same
  install/uninstall/activate idempotency guarantees the Node provider
  already has — no new risk class introduced.

## Non-goals (deferred)

- Go, Python, and Rust providers — still deferred, unchanged from the
  earlier decision to defer all of 2c.
- Any JVM implementation other than HotSpot (Eclipse OpenJ9 exists as an
  alternative but is niche; not exposed as a user-facing choice).
- JRE-only installs — always installs the full JDK, since `javac` (a
  compiler, JDK-only) is one of the two required shims.
- Any Java version manager concept beyond what `Runtime` already provides
  (e.g. per-project `.java-version` files) — out of scope, matches how the
  Node provider doesn't have this either.

## Real API research (verified live against `api.adoptium.net`, 2026-09-28)

- `GET /v3/info/available_releases` → one call, returns:
  ```json
  {
    "available_releases": [8, 11, 16, 17, ..., 27],
    "available_lts_releases": [8, 11, 17, 21, 25],
    "most_recent_lts": 25,
    "most_recent_feature_release": 27,
    ...
  }
  ```
  This alone is everything `ListRemoteVersions` needs — one major-version
  list, one LTS subset — no per-major follow-up calls required.
- `GET /v3/assets/latest/{major}/hotspot?architecture={arch}&os={os}&image_type=jdk`
  → a JSON array (one element for a valid major/os/arch/image_type
  combination) containing, among other fields:
  ```json
  {
    "release_name": "jdk-21.0.12.1+1",
    "binary": {
      "package": {
        "name": "OpenJDK21U-jdk_x64_linux_hotspot_21.0.12.1_1.tar.gz",
        "link": "https://github.com/adoptium/.../OpenJDK21U-....tar.gz",
        "checksum": "f9d6e191...(64 hex chars, sha256)"
      }
    }
  }
  ```
  Critically, **the checksum is already embedded in this response** — unlike
  Node (which needs a second `SHASUMS256.txt` fetch), Java's Install flow
  needs exactly one HTTP call to resolve a major version to its exact
  release, download link, and checksum together.
- `os` values: `linux`, `mac`, `windows`. `architecture` values: `x64`,
  `aarch64` (not `arm64` — this project's `platform.Arch()` returns `arm64`,
  needs mapping). Windows packages are `.zip`; Linux and macOS are
  `.tar.gz` — same two-way split as the Node provider already has.
- **macOS layout difference (the one real surprise here):** after stripping
  the archive's single top-level directory (same as Node), Linux and
  Windows both put binaries directly at `bin/<name>` (or `bin\<name>.exe`)
  — but macOS Temurin archives nest an extra `Contents/Home/` level
  (matching Apple's standard JDK bundle convention), so the real path is
  `Contents/Home/bin/<name>`. Verified via Adoptium's own installation
  docs. `BinaryPath` needs a third branch for this, not just Node's
  two-way Windows/Unix split.

## `internal/runtime/java/java.go`

Mirrors `internal/runtime/node/node.go`'s structure and file layout as
closely as the real API differences allow — same `baseURL string` override
field for tests, same `New()` constructor, same package-per-language
directory convention (`internal/runtime/java/`, alongside
`internal/runtime/node/`).

```go
type Java struct {
	baseURL string // overridable in tests; defaults to "https://api.adoptium.net"
}

func New() *Java
func (j *Java) Name() string // "java"

func (j *Java) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error)
// One GET to baseURL+"/v3/info/available_releases". One Version per
// entry in available_releases, LTS=true iff present in
// available_lts_releases. No per-major follow-up call needed (unlike
// Node, which must filter each index entry by platform-specific file key).

func (j *Java) ListInstalledVersions() ([]devruntime.Version, error)
func (j *Java) CurrentVersion() (*devruntime.Version, error)
// Identical in structure to Node's — local versions/java/ and
// current/java reads, including the same "." /".old" filtering
// ListInstalledVersions already established as a fixed defect class.

func (j *Java) Install(ctx context.Context, name string) error
func (j *Java) Uninstall(name string) error
func (j *Java) Activate(name string) error
func (j *Java) ShimNames() []string // ["java", "javac"]
func (j *Java) BinaryPath(versionDir, binName string) (string, error)
```

### `resolveRelease` equivalent — one call does what Node needs two for

```go
// javaOS/javaArch map platform.OS()/Arch() to Temurin's vocabulary:
// linux→"linux", darwin→"mac", windows→"windows";
// amd64→"x64", arm64→"aarch64". Same unsupported-value error shape
// nodeOS/nodeArch already use.
func javaOS() (string, error)
func javaArch() (string, error)

// resolveRelease calls GET {baseURL}/v3/assets/latest/{major}/hotspot
// ?architecture={arch}&os={os}&image_type=jdk, and returns the single
// result's exact release name, download link, and checksum together —
// Java's API gives us in one request what Node's Install needs two
// separate calls for (index lookup, then a checksums-file fetch).
// An empty response array (valid major, but the requested os/arch
// combination doesn't exist for it) is reported as "no release found",
// not a hard error — mirrors Node's "no release found" handling for an
// unrecognized major.
func (j *Java) resolveRelease(ctx context.Context, name string) (release javaRelease, err error)

type javaRelease struct {
	Name     string // e.g. "jdk-21.0.12.1+1" — used as the .dev-release marker content
	URL      string
	Filename string
	SHA256   string
}
```

`release.Name` (Temurin's `release_name`) flows into a filesystem path (the
`.dev-release` marker file) the same way Node's `exactVersion` does — but
unlike Node's version strings, Java's aren't uniformly `vX.Y.Z` (Java 8
uses a different scheme, `jdk8u<update>-b<build>`, not the `X.Y.Z+B` style
9+ uses). Rather than hardcode a format regex that could reject a
legitimate but differently-shaped real version string, `resolveRelease`
validates `release_name` the same way `internal/runtime.ValidVersionName`
already validates user-supplied version names: reject empty, `.`, `..`, or
anything containing a path separator. This is weaker than Node's exact
`^v\d+\.\d+\.\d+$` match but is the right trade-off here — it still blocks
every real path-injection shape, without assuming a version-string format
that doesn't actually hold across all Java majors.

### `Install`

Same structure and same offline-fallback behavior as Node's `Install` (if
`resolveRelease` fails outright — network down, Adoptium unreachable — but
a `.dev-release` marker already exists for this major, report the cached
release and succeed rather than hard-failing an already-installed major).
Extraction reuses `installer.ExtractAtomic` plus the same
"strip one top-level directory, rename-aside-then-swap" helper pattern
`extractStrippingTopLevel` already established (copied, not shared, since
it's a small, self-contained ~50-line function already duplicated by
design rather than factored into a shared package — matching this
codebase's own stated reasoning for why `internal/workspace`'s
`ValidProjectName` duplicates rather than imports
`internal/runtime.ValidVersionName`: these are two unrelated domains that
happen to need the same small algorithm, not a reason to couple them).

### `BinaryPath`

```go
// BinaryPath resolves binName inside an installed Java version.
// Linux: bin/<name>. Windows: bin\<name>.exe. macOS: Contents/Home/bin/<name>
// (Temurin's macOS archives nest an extra Contents/Home/ level, matching
// Apple's standard JDK bundle layout — verified against Adoptium's own
// installation docs, not guessed).
func (j *Java) BinaryPath(versionDir, binName string) (string, error)
```

## Registration

`internal/providers.Register` gains one line (`m.Register(java.New())`),
matching how it already registers Node. `cmd/lang.go` and `internal/shim`
need zero changes — both are already generic over whatever `Manager` has
registered, which is the entire point of the `Runtime`/`Manager`
abstraction sub-project 2a built.

## Error handling & idempotency

Identical guarantees to the Node provider, for the same reasons (same
underlying `installer`/`downloader` packages, same idempotency contract):
re-running `Install` with an already-installed major is a no-op network
call avoided via the release marker; a checksum mismatch or extraction
failure leaves no partial install; `Uninstall` on a non-installed version
errors without touching disk; `Activate` requires the version already be
installed.

## Testing approach

Mirrors `internal/runtime/node/node_test.go`'s structure: `Java{baseURL:
server.URL}` against an `httptest.Server`, JSON fixtures shaped exactly
like the real `available_releases`/`assets/latest` responses documented
above (not guessed — copied from this spec's verified research).
Additional coverage specific to Java: the three-way `BinaryPath` OS split
(especially the macOS `Contents/Home` case, which Node's test suite has no
equivalent of), and `resolveRelease`'s `release_name` validation rejecting
a path-traversal-shaped value.

## Acceptance criteria

```bash
dev lang list java
dev lang install java 21
dev lang use java 21
dev lang installed java
dev lang current java
dev lang uninstall java 21
```

Plus, after `dev setup`, `java --version` and `javac --version` via the
shims, on whatever platform this is verified on.
