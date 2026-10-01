# Release Automation — Design Spec

Date: 2026-09-28
Status: Approved for planning
Sub-project: 4 (Release automation)

## Context

Sub-projects 1, 2a/2b/2c, and 3a/3b/3c are complete and merged to `main`.
This is the last currently-planned sub-project — Go/Python/Rust language
providers remain explicitly deferred. The original brief calls for
cross-platform release builds, GitHub Releases, a `checksums.txt`, and a
semver-tag-triggered workflow, naming GoReleaser as the likely tool.

This repo has **no git remote configured** — it is a solo local repo where
every prior sub-project has gone directly to `main`. The user has
explicitly chosen to build and wire up the complete release pipeline now,
without connecting a remote or pushing yet; connecting a remote and doing
the first real release is deferred to whenever the user is ready to go
public. Everything in this spec must therefore be verifiable locally,
without a live GitHub repo, via GoReleaser's snapshot mode.

The repo already has a foundation to build on: `.github/workflows/ci.yml`
(format/vet/lint/test/build on every push and PR), a `Makefile` with
ldflags-based version injection, and `cmd/root.go`'s `Version`/`Commit`/
`BuildDate` vars (currently defaulting to `"dev"`/`"none"`/`"unknown"` for
`go run`/`go test`, injected at build time otherwise).

## Goals

- Pushing a tag matching `vX.Y.Z` to a (future) GitHub remote triggers a
  GitHub Actions workflow that cross-compiles `dev` for linux/darwin/
  windows × amd64/arm64 (6 combinations), archives each build
  (`.tar.gz` for Linux/macOS, `.zip` for Windows), generates a single
  SHA256 `checksums.txt` covering all six archives, and creates a
  **draft** GitHub Release with all assets attached and auto-generated
  release notes — left for the user to review and publish manually.
- The git tag becomes the single source of truth for version numbers,
  replacing the static `VERSION` file. Release builds inject the exact
  tag (minus its `v` prefix) as `cmd.Version`; local `make build` derives
  a version from `git describe` instead.
- Everything is verifiable right now, with no remote, via GoReleaser's
  `--snapshot` mode.
- The existing `ci.yml` workflow, and everything about how `dev` itself
  behaves at runtime, is unaffected — this sub-project only touches the
  build/release/publish pipeline.

## Non-goals

- Actually connecting a GitHub remote or performing the first real
  release — explicitly deferred by the user to a later time of their
  choosing.
- Homebrew/Scoop/apt package manager integration, container images, or
  any distribution channel beyond a GitHub Release's attached archives.
- Signing/notarization (macOS codesigning, Windows Authenticode, GPG
  signing of checksums) — not requested, adds real operational
  complexity (certificates, secrets management) disproportionate to a
  first release pipeline for a solo project.
- Conventional-commit-based changelog grouping — this repo's commit
  history doesn't use `feat:`/`fix:` prefixes, so release notes are a
  flat, chronological list of commits since the previous tag.
- Go/Python/Rust language providers — still deferred, unrelated to this
  sub-project.

## Files

- **`.goreleaser.yml`** (new, repo root) — the complete build, archive,
  checksum, and release spec. See "GoReleaser configuration" below for
  its exact shape.
- **`.github/workflows/release.yml`** (new) — triggered only on tag push
  matching `v*.*.*`. Checks out full git history (`fetch-depth: 0`,
  required for both `git describe` and GoReleaser's changelog
  generation), sets up Go, runs `goreleaser/goreleaser-action@v6` with
  `args: release --clean`, using the automatically-provided
  `secrets.GITHUB_TOKEN` (no new secret needs to be configured by the
  user). Matches the existing `ci.yml`'s convention of pinning the
  action's major version while leaving the underlying tool version at
  `latest` (mirrors how `ci.yml` already uses
  `golangci-lint-action@v6` with `version: latest`).
- **`Makefile`** (modified) — `VERSION := $(shell cat VERSION)` becomes
  `VERSION := $(shell git describe --tags --always --dirty)`. Nothing
  else in the Makefile changes; the same `LDFLAGS` variable still injects
  into `cmd.Version`/`cmd.Commit`/`cmd.BuildDate`.
- **`VERSION`** (deleted) — no longer read anywhere once the Makefile
  change lands.
- **`.gitignore`** (modified) — add `/dist/`, GoReleaser's local output
  directory (created by `--snapshot` runs and real releases alike).
- **`README.md`** (modified) — a short "Releases" section documenting
  that tagged releases are built and published automatically, where to
  find them (once a remote exists), and that `make build` produces a
  `git describe`-based dev version locally.

No other files change. `cmd/root.go` is untouched — its `Version`/
`Commit`/`BuildDate` vars and `versionString()` function are reused
exactly as they are today.

## GoReleaser configuration

`.goreleaser.yml` (GoReleaser v2 config schema):

- **`builds`**: one build definition for the `dev` binary at the repo
  root (`main.go`). `goos: [linux, darwin, windows]`,
  `goarch: [amd64, arm64]` — 6 combinations, no exclusions.
  `env: [CGO_ENABLED=0]`, matching the Makefile's existing flag.
  `ldflags` injects the same three variables the Makefile already does,
  via GoReleaser's built-in template variables:
  `-X github.com/jpsdm/dev/cmd.Version={{.Version}} -X
  github.com/jpsdm/dev/cmd.Commit={{.Commit}} -X
  github.com/jpsdm/dev/cmd.BuildDate={{.Date}}`. GoReleaser's
  `{{.Version}}` is the tag with its `v` prefix already stripped (tag
  `v0.2.0` → `Version` = `"0.2.0"`), matching the version scheme's goal
  exactly with no extra templating needed.
- **`archives`**: `name_template` is `dev_{{ .Os }}_{{ .Arch }}`
  (GoReleaser appends the correct extension automatically based on
  `format`). `format: tar.gz` by default, with a `format_overrides`
  entry setting `format: zip` for `goos: windows` — the same two-way
  split this project's own Node.js and Java providers already
  implement for their downloaded archives. `files: [README.md,
  LICENSE]` are included in every archive alongside the binary (a
  GoReleaser default worth keeping explicit in the config for clarity).
- **`checksum`**: default settings produce one `checksums.txt` (SHA256,
  matching this project's existing checksum convention in `internal/
  downloader`) covering all 6 archives.
- **`changelog`**: default behavior (commits since the previous tag,
  sorted, merge commits excluded) — no `groups` configuration, since
  this repo's commits don't follow a prefix convention to group by.
- **`release`**: `draft: true`, per the user's explicit choice — the
  workflow creates the release with every asset attached but does not
  publish it; the user reviews and clicks "Publish" on GitHub manually.

## Release workflow (`.github/workflows/release.yml`)

```yaml
name: Release

on:
  push:
    tags:
      - "v*.*.*"

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: 'stable'

      - name: Run GoReleaser
        uses: goreleaser/goreleaser-action@v6
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

`permissions: contents: write` is the one addition beyond `ci.yml`'s
`contents: read` — required for GoReleaser to create the release and
upload assets via the GitHub API. This permission is scoped to this
workflow only; `ci.yml` keeps its own, narrower `contents: read`.

## Versioning scheme

- Release tags: `vX.Y.Z` (standard semver, `v`-prefixed) — this is also
  the exact pattern the workflow's `on.push.tags` filter matches, so a
  malformed tag (e.g. `v1.0` or `release-1`) simply never triggers the
  workflow rather than failing loudly. No additional validation needed.
- `cmd.Version` at release time = the matched tag with its `v` stripped
  (GoReleaser's `{{.Version}}` does this automatically).
- Local `make build`: `VERSION := $(shell git describe --tags --always
  --dirty)`. On a commit with an exact tag, this is just the tag (e.g.
  `v0.2.0`); on any other commit, it's something like
  `v0.1.0-3-gabc1234` (3 commits past the last tag) or
  `v0.1.0-3-gabc1234-dirty` (uncommitted changes present); with no tags
  at all in the repo's history yet, `--always` falls back to a bare
  short commit hash. This is intentionally more verbose than the
  release build's clean `{{.Version}}` — local builds are for
  development, and the extra detail (commits-since-tag, dirty flag) is
  useful there specifically.

## Testing / verification approach

No real tag will be pushed to a remote as part of this sub-project (no
remote exists). Verification instead uses GoReleaser's snapshot mode,
which runs the complete build → archive → checksum → (skipped) release
pipeline locally:

```bash
goreleaser release --snapshot --clean
```

This produces `dist/` containing all 6 archives and `checksums.txt`,
without requiring a git tag, a `GITHUB_TOKEN`, or any network access to
GitHub's release API (snapshot mode skips the publish step entirely).
The implementation plan's tasks verify success by running this command
and inspecting `dist/`'s contents — checking that all 6 expected archive
filenames exist, that `checksums.txt` lists all 6, and that extracting
one archive and running the contained binary with `--version` reports a
plausible snapshot version string.

`.goreleaser.yml`'s own syntax is additionally checked via
`goreleaser check` (a fast, no-build config-validation command), and the
full repo's existing `make check` (fmt/vet/lint/test/build) must stay
green throughout, since Makefile is one of the modified files.

Installing GoReleaser locally for this verification: the plan should use
whatever install method is simplest for this environment (e.g. `go
install github.com/goreleaser/goreleaser/v2@latest`, matching the "go
install" pattern already familiar from this Go-toolchain-based project) —
this is a one-time local tool install for verification, not a project
dependency, and is not added to `go.mod`.

## Acceptance criteria

```bash
goreleaser check                        # config is valid
goreleaser release --snapshot --clean   # full local dry-run succeeds
ls dist/                                # 6 archives + checksums.txt present
tar xzf dist/dev_linux_amd64.tar.gz -C /tmp/dev-smoke && /tmp/dev-smoke/dev --version
make check                              # fmt/vet/lint/test/build still green
```

Plus, whenever the user later connects a remote and pushes a real
`vX.Y.Z` tag: the `release.yml` workflow runs, and a draft GitHub
Release appears with 6 archives, `checksums.txt`, and generated release
notes attached — left for the user to review before publishing. That
step is explicitly out of scope for this sub-project's own verification,
since it requires infrastructure (the remote) this sub-project
deliberately doesn't set up.
