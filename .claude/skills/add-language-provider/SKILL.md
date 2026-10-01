---
name: add-language-provider
description: Scaffold a new language runtime provider (e.g. Go, Python, Rust) for the dev CLI, following the exact pattern internal/runtime/node and internal/runtime/java already establish. Use when the user asks to add support for a new language, runtime, or SDK to dev lang.
---

# Add a Language Provider

Every managed language in this project implements the same `internal/runtime.Runtime`
interface and slots into the registry with zero changes anywhere else. This skill is a
structural checklist, not a substitute for the real process — **still run this through
`superpowers:brainstorming` first** (architectural path: it's a new subsystem) if that skill
plugin is available in your session, because the genuinely hard part of adding a language is
always the same research problem this project has solved twice already: *how does this
specific language actually distribute its releases?* Node.js has a single JSON index
(`nodejs.org/dist/index.json`); Java (Eclipse Temurin/Adoptium) has a REST API with per-OS/arch
queries and embedded checksums. Go, Python, and Rust each have their own, different answer, and
you need to research the real API/index shape live before designing — don't assume it mirrors
either existing provider.

Read `internal/runtime/java/java.go` in full before starting — it's the more recently written,
more refined reference (Node's provider predates several hardening fixes that Java's design
incorporated from the start: OS-parameterized `binaryPathForOS` for host-independent testing,
path-safety validation on every server-supplied field that reaches a filesystem path, not just
the obvious one).

## The interface every provider implements

```go
type Runtime interface {
	Name() string
	ListRemoteVersions(ctx context.Context) ([]Version, error)
	ListInstalledVersions() ([]Version, error)
	CurrentVersion() (*Version, error)
	Install(ctx context.Context, name string) error
	Uninstall(name string) error
	Activate(name string) error
	BinaryPath(versionDir, binName string) (string, error)
	BinDir(versionDir string) (string, error)
}
```

`BinDir` is the one every provider must get right for `PATH` to work: it returns the directory
whose contents get exposed on `PATH` for an active version (`<versionDir>/bin` on most
platforms, or `versionDir` itself where a provider's binaries sit at the version root, as
Python's Windows builds do). `dev env` uses it directly to compute `PATH`, so anything a
language's own package manager installs into that directory (`npm install -g`, a
`pip`-installed console script) is reachable the moment it lands.

## File structure

- `internal/runtime/<lang>/<lang>.go` — the provider itself.
- `internal/runtime/<lang>/<lang>_test.go` — real `httptest.Server`-backed tests, real archive
  bytes built with `archive/tar`/`archive/zip`, no mocks.
- One line added to `internal/providers.Register` (`m.Register(<lang>.New())`).
- **Nothing else changes.** If you find yourself wanting to touch `cmd/lang.go` or
  `internal/shell`, stop — those are generic over the registry by design (`internal/shell`'s
  `ComputePathEntries` and `cmd/env.go`'s `activeBinDirs` iterate `langManager.Names()`
  generically to put a new provider's active version on `PATH`), and needing to modify them
  means something about the new provider doesn't actually fit the `Runtime` interface as
  intended. The installed shell function and `dev setup` likewise need no per-provider
  knowledge at all.

## Non-negotiable patterns (both existing providers do all of these — don't skip any)

1. **`baseURL string` field on the struct, defaulting in `New()`, overridable in tests.** This
   is the entire testing strategy — tests construct `&Lang{baseURL: server.URL}` against a
   `httptest.Server` serving realistic fixture responses.
2. **Path-safety validation on every externally-sourced string that becomes part of a filesystem
   path or the `.dev-release` marker file** — not just the obvious "version" field. Java's
   provider was shipped with a path-injection gap where the release *name* was validated but the
   download *filename* (a sibling field from the same API response) wasn't — caught in review,
   not before. Validate every such field, not just the first one you think of. See
   `internal/runtime.ValidVersionName` (reuse it or delegate to it, don't reimplement the check).
3. **`ListInstalledVersions` filters out `.`-prefixed and `.old`-suffixed directory entries** —
   leftover temp-extraction and swap-backup directories from a killed-mid-install process must
   never be reported as installed versions.
4. **`Install` has an offline fallback**: if the network resolve call fails but a `.dev-release`
   marker already exists for the requested version, report the cached release and succeed rather
   than hard-failing a machine that's merely offline.
5. **Extraction reuses `internal/installer.ExtractAtomic`** (zip-slip-protected, dispatches
   `.zip` vs `.tar.gz`/`.tgz` by the archive's own filename extension) **plus a
   strip-single-top-level-directory helper** using
   `internal/installer.ReconcileStaleBackup` for the atomic rename-swap's crash recovery — copy
   the pattern from `extractStrippingTopLevel` in either existing provider, don't invent a new
   one.
6. **`BinaryPath`'s OS-dispatch logic is a separate, pure function** (`binaryPathForOS(osName,
   versionDir, binName string) string` in Java's provider) so each OS branch can be tested
   directly, independent of the host actually running the test suite. `platform.OS()` has no
   override hook — a `BinaryPath`-only test only ever exercises the host's own branch, which is a
   real coverage gap if the language has a platform-specific quirk (Java's macOS
   `Contents/Home/` nesting is exactly this kind of thing — research whether the new language has
   an equivalent before assuming a flat `bin/` layout everywhere).
7. **Downloads are checksum-verified** via `internal/downloader.Download` — if the source API
   provides a checksum inline (like Adoptium does), use it directly; if it requires a separate
   fetch (like Node's `SHASUMS256.txt`), fetch it before downloading, never after.

## Process

1. Research the language's real distribution mechanism live (API docs, or fetch the real
   index/endpoint and read the actual response shape — don't rely on memory of how similar APIs
   usually look).
2. Design: OS/arch vocabulary mapping (`platform.OS()`/`platform.Arch()` → the vendor's own
   naming), archive format(s), checksum source, any platform-specific layout quirks.
3. Write the design as a short spec if the research surfaced anything non-obvious (an API
   quirk, a checksum handling difference, a platform-layout surprise) — this project's
   `docs/superpowers/specs/2026-09-28-java-provider-design.md` is a good model for how much
   detail is worth writing down.
4. Implement with TDD: real httptest fixtures shaped exactly like the verified real API
   response, real archive bytes for extraction tests, the offline-fallback and path-injection
   tests from the start (not added after a reviewer asks for them).
5. Register in `internal/providers.Register`, update the README's supported-languages line.
6. Full-repo `make check` must pass — this is what proves `cmd/lang.go` and `internal/shell`
   genuinely needed zero changes.
