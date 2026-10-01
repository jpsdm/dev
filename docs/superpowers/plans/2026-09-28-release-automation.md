# Release Automation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and locally verify a complete GoReleaser-based release pipeline for the `dev` CLI — cross-platform builds, archives, a `checksums.txt`, and a tag-triggered GitHub Actions workflow that creates a draft GitHub Release — without connecting a remote or pushing a real tag.

**Architecture:** `.goreleaser.yml` describes the entire build → archive → checksum → release pipeline in one declarative config; `.github/workflows/release.yml` is a thin wrapper that runs it on tag push. Local versioning switches from a static `VERSION` file to `git describe`, so the git tag becomes the single source of truth for version numbers everywhere. Everything is verified locally via GoReleaser's `--snapshot` mode, which runs the full pipeline minus the actual GitHub publish step.

**Tech Stack:** GoReleaser v2 (installed locally via `go install` for verification; runs in CI via `goreleaser/goreleaser-action@v6`), GitHub Actions, the existing Makefile/ldflags version-injection pattern.

**Spec:** docs/superpowers/specs/2026-09-28-release-automation-design.md

## Global Constraints

- Release tags follow `vX.Y.Z` (semver, `v`-prefixed); the release workflow's trigger matches `v*.*.*` exactly.
- Build matrix: `goos: [linux, darwin, windows]` × `goarch: [amd64, arm64]` (6 combinations), `CGO_ENABLED=0`.
- `ldflags` inject `github.com/jpsdm/dev/cmd.Version`, `github.com/jpsdm/dev/cmd.Commit`, `github.com/jpsdm/dev/cmd.BuildDate` via GoReleaser's `{{.Version}}`/`{{.Commit}}`/`{{.Date}}` template variables.
- Archive naming: `dev_{{ .Os }}_{{ .Arch }}`, format `tar.gz` for linux/darwin, `zip` for windows (via `format_overrides`). Each archive includes `README.md` and `LICENSE` alongside the binary.
- `checksums.txt`: one file, SHA256, covering every archive.
- `release.draft: true` — the workflow creates the release with all assets attached but does not publish it.
- The release workflow needs `permissions: contents: write`; the existing `ci.yml` keeps its own `contents: read` untouched.
- `goreleaser/goreleaser-action@v6` pinned, `version: latest` for the underlying tool — mirrors `ci.yml`'s existing `golangci-lint-action@v6` + `version: latest` convention.
- No real tag is pushed and no remote is connected as part of this plan — every task's verification is local-only, via `goreleaser check` and `goreleaser release --snapshot --clean`.
- Every commit ends with `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.

## Review Focus

- **A fresh/zero-tag repository must not break local versioning.** This repo currently has zero git tags (`git tag` lists nothing). `git describe --tags --always --dirty` must still produce a usable, non-empty string (falling back to an abbreviated commit hash) rather than erroring — a reasonable person running `make build` right after this change lands should not get a broken build.
- **All 6 platform/arch combinations must actually cross-compile, not just the one running the test.** A GoReleaser config can look correct but silently fail (or skip) one exotic combination. Verification must count the produced archives, not just check that the command exited zero.
- **Archive naming and internal layout must be correct enough to actually use.** Extracting an archive and finding a working binary at the expected path is a real-world sanity check no config-syntax check catches.
- **`checksums.txt` must genuinely match the produced archives, not merely exist.** A wrong algorithm, truncated file, or stale content is exactly the kind of file that "looks right" without being verified byte-for-byte.
- **`release.yml`'s YAML can't be exercised by a real trigger in this plan** (no remote, no tag push) — static validation (`actionlint`) is the closest substitute and must actually run, not just be assumed correct by eye.

---

### Task 1: Local versioning via `git describe`

**Files:**
- Modify: `Makefile:4` (the `VERSION :=` line)
- Modify: `.gitignore` (add `/dist/`)
- Delete: `VERSION`

**Interfaces:**
- Consumes: nothing from earlier tasks (this is the first task).
- Produces: a `make build` that no longer reads the `VERSION` file — Task 2 and Task 3 don't depend on this task's output directly (they use GoReleaser's own version resolution, not the Makefile), but this task must land first since it removes the `VERSION` file Task 2's config must never reference.

- [ ] **Step 1: Confirm the current state**

Run: `cat Makefile | head -5` and `git tag`
Expected: the Makefile's 4th line reads `VERSION := $(shell cat VERSION)`, and `git tag` prints nothing (this repo has zero tags right now) — this second fact is exactly the edge case this task's tests must cover.

- [ ] **Step 2: Change the Makefile's version source**

In `Makefile`, change:

```makefile
VERSION := $(shell cat VERSION)
```

to:

```makefile
VERSION := $(shell git describe --tags --always --dirty)
```

Nothing else in the Makefile changes — `COMMIT`, `BUILD_DATE`, and `LDFLAGS` stay exactly as they are; they already reference `$(VERSION)` by name, so they pick up the new source automatically.

- [ ] **Step 3: Delete the VERSION file**

```bash
git rm VERSION
```

- [ ] **Step 4: Add `/dist/` to `.gitignore`**

Add a new line to `.gitignore` (GoReleaser's local output directory, used starting in Task 2):

```
/dist/
```

The full file should now read:

```
/.superpowers/
/dev
/dev.exe
*.test
*.out
/dist/
```

- [ ] **Step 5: Verify the zero-tag case works (this task's Review Focus item)**

Run: `git describe --tags --always --dirty`
Expected: a non-empty output — with zero tags in this repo's history, this should be just an abbreviated commit hash (e.g. `84c0ebf` or `84c0ebf-dirty` if the working tree has uncommitted changes), NOT an error and NOT an empty string. If this command errors or prints nothing, stop and report — the Makefile change is unsafe until this is understood.

- [ ] **Step 6: Build and verify the injected version**

Run: `make build && ./dev version`
Expected: output starting with `dev version <something>`, where `<something>` matches whatever `git describe --tags --always --dirty` printed in Step 5 (not the literal string `"0.1.0"`, and not empty) — confirms the ldflags wiring actually picked up the new `VERSION` source end-to-end, through the Makefile into the compiled binary.

- [ ] **Step 7: Run the full existing test suite to confirm nothing else broke**

Run: `make check`
Expected: PASS — fmt/vet/lint/test/build all succeed. No test in this repo currently references the `VERSION` file or its literal contents, so none should need updating, but this step confirms that.

- [ ] **Step 8: Commit**

```bash
git add Makefile .gitignore
git commit -m "$(cat <<'EOF'
Switch local versioning from a static VERSION file to git describe

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

(The `VERSION` file's deletion is already staged from Step 3's `git rm` — it will be included in this same commit.)

---

### Task 2: GoReleaser configuration and local snapshot verification

**Files:**
- Create: `.goreleaser.yml`

**Interfaces:**
- Consumes: `Makefile`'s existing ldflags pattern (Task 1, for reference/consistency — GoReleaser's own `ldflags` are independent config, not a call into the Makefile) and the exact var names `cmd.Version`/`cmd.Commit`/`cmd.BuildDate` already defined in `cmd/root.go` (pre-existing, unmodified by this plan).
- Produces: a validated `.goreleaser.yml` that Task 3's workflow file invokes via `goreleaser-action`. No later task reads specific values out of this file programmatically — Task 3 only needs it to exist and be valid.

- [ ] **Step 1: Install GoReleaser locally**

This is a one-time local tool install for verification, not a project dependency — it must NOT be added to `go.mod`.

Run: `go install github.com/goreleaser/goreleaser/v2@latest`
Expected: installs cleanly; `goreleaser --version` afterward prints a version string starting with a `2.` release.

- [ ] **Step 2: Write `.goreleaser.yml`**

Create `.goreleaser.yml` at the repo root with exactly this content:

```yaml
version: 2

project_name: dev

builds:
  - id: dev
    main: .
    binary: dev
    env:
      - CGO_ENABLED=0
    goos:
      - linux
      - darwin
      - windows
    goarch:
      - amd64
      - arm64
    ldflags:
      - -s -w
      - -X github.com/jpsdm/dev/cmd.Version={{.Version}}
      - -X github.com/jpsdm/dev/cmd.Commit={{.Commit}}
      - -X github.com/jpsdm/dev/cmd.BuildDate={{.Date}}

archives:
  - name_template: "dev_{{ .Os }}_{{ .Arch }}"
    formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]
    files:
      - README.md
      - LICENSE

checksum:
  name_template: "checksums.txt"
  algorithm: sha256

changelog:
  sort: asc
  filters:
    exclude:
      - "^Merge"

release:
  draft: true
```

- [ ] **Step 3: Validate the config syntax**

Run: `goreleaser check`
Expected: reports the config is valid (no errors). If it reports a deprecation warning about the missing `version` key or an unrecognized field, the config above has a typo relative to the real GoReleaser v2 schema — fix it before proceeding; do not ignore a `check` failure.

- [ ] **Step 4: Run a full local snapshot build (this task's core Review Focus verification)**

Run: `goreleaser release --snapshot --clean`
Expected: succeeds, ends with a summary showing the release pipeline completed (build, archive, checksum stages all green; the actual GitHub publish stage is skipped automatically in snapshot mode — no `GITHUB_TOKEN` is needed and no network call to GitHub happens).

- [ ] **Step 5: Verify all 6 archives were actually produced (not just that the command exited zero)**

Run: `ls dist/*.tar.gz dist/*.zip`
Expected: exactly 6 files total, matching this pattern (the exact version suffix in the filename comes from GoReleaser's snapshot versioning, but the `dev_<os>_<arch>` prefix and extension must match):

```
dist/dev_darwin_amd64.tar.gz
dist/dev_darwin_arm64.tar.gz
dist/dev_linux_amd64.tar.gz
dist/dev_linux_arm64.tar.gz
dist/dev_windows_amd64.zip
dist/dev_windows_arm64.zip
```

If any of the 6 is missing, one platform/arch combination failed to cross-compile — do not treat 5-out-of-6 as acceptable; investigate the missing one's build error in GoReleaser's output.

- [ ] **Step 6: Verify `checksums.txt` genuinely matches the archives (this task's other Review Focus item)**

Run:
```bash
cd dist && sha256sum -c checksums.txt --ignore-missing
```
Expected: every listed archive reports `OK`. `--ignore-missing` accounts for `checksums.txt` potentially listing additional artifact types (e.g. a source archive) that Step 5 didn't specifically check for — but every `.tar.gz`/`.zip` from Step 5 must show `OK`, not `FAILED` and not be silently absent from the checksums file (cross-check the file count matches: `wc -l dist/checksums.txt` should show at least 6 lines).

- [ ] **Step 7: Extract and run the native-platform binary**

On whatever platform this verification runs (adapt the filename to match — e.g. `dev_linux_amd64.tar.gz` on Linux/amd64):

```bash
mkdir -p /tmp/dev-release-smoke
tar xzf dist/dev_linux_amd64.tar.gz -C /tmp/dev-release-smoke
/tmp/dev-release-smoke/dev --version
```

Expected: prints a version block starting with `dev version` (the version string will be GoReleaser's snapshot-mode placeholder, something like `0.0.0-SNAPSHOT-<hash>` or similar — the exact format doesn't matter here, what matters is that the binary runs at all and reports a plausible, non-empty version rather than crashing or printing a build error).

Clean up afterward: `rm -rf /tmp/dev-release-smoke`

- [ ] **Step 8: Structurally verify one non-native archive**

Pick any archive from Step 5 that doesn't match the platform this verification is running on (e.g., if Step 7 used the Linux archive, check the Windows one here):

```bash
unzip -l dist/dev_windows_amd64.zip
```
(or, for a tar.gz on a different OS: `tar tzf dist/dev_darwin_amd64.tar.gz`)

Expected: the listing includes `dev.exe` (for the Windows zip) or `dev` (for a tar.gz), plus `README.md` and `LICENSE` — confirming the `files:` inclusion in the archive config worked, not just the binary itself.

- [ ] **Step 9: Clean up the snapshot build output**

```bash
rm -rf dist/
```

(`dist/` is now gitignored per Task 1, but snapshot runs are local scratch output — no reason to leave 6 archives sitting in the working tree.)

- [ ] **Step 10: Commit**

```bash
git add .goreleaser.yml
git commit -m "$(cat <<'EOF'
Add GoReleaser configuration for cross-platform release builds

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Release workflow and documentation

**Files:**
- Create: `.github/workflows/release.yml`
- Modify: `README.md` (add a new `## Releases` section)

**Interfaces:**
- Consumes: `.goreleaser.yml` (Task 2, by reference — the workflow invokes `goreleaser-action`, which reads that file; this task does not need to know its contents).
- Produces: nothing consumed by a later task — this is the final task in the plan.

- [ ] **Step 1: Write the release workflow**

Create `.github/workflows/release.yml` with exactly this content:

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

- [ ] **Step 2: Install a GitHub Actions workflow linter**

This is a one-time local tool install for verification — not a project dependency, not added to `go.mod`.

Run: `go install github.com/rhysd/actionlint/cmd/actionlint@latest`
Expected: installs cleanly; `actionlint -version` afterward prints a version.

- [ ] **Step 3: Lint the new workflow file (this task's Review Focus verification)**

Run: `actionlint .github/workflows/release.yml`
Expected: no output and a zero exit code (`echo $?` prints `0`) — `actionlint` prints nothing on success. If it reports any finding, fix `release.yml` before proceeding; do not dismiss an `actionlint` finding without understanding it first.

As a sanity check that `actionlint` itself is working correctly (not just silently passing everything), also run it against the pre-existing, known-good workflow:

Run: `actionlint .github/workflows/ci.yml`
Expected: also no output, zero exit code — confirms the tool recognizes valid GitHub Actions YAML and isn't failing open.

- [ ] **Step 4: Add the README's Releases section**

In `README.md`, add a new `## Releases` section immediately after the existing `## Full check (fmt, vet, lint, test, build)` section and before `## Usage`. The full block to insert:

```markdown
## Releases

Pushing a tag matching `vX.Y.Z` triggers `.github/workflows/release.yml`,
which cross-compiles `dev` for Linux, macOS, and Windows (amd64 and
arm64), packages each build into an archive with a `checksums.txt`
covering all of them, and creates a **draft** GitHub Release with
everything attached — reviewed and published manually, not automatic.

`make build` (used for local development) derives its version from
`git describe --tags --always --dirty` rather than a real release tag,
so it prints something like `0.1.0-3-gabc1234-dirty` rather than a
clean release version — this is expected and only matters for actual
tagged releases.
```

- [ ] **Step 5: Run the full check one more time**

Run: `make check`
Expected: PASS — confirms the README/workflow additions haven't broken anything in the existing build/test/lint pipeline (these are non-Go files, so this step mainly exists to confirm nothing else in the working tree was accidentally left broken from earlier tasks).

- [ ] **Step 6: Commit**

```bash
git add .github/workflows/release.yml README.md
git commit -m "$(cat <<'EOF'
Add tag-triggered release workflow and document it in the README

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Post-plan manual step (not a task — for the user, whenever they're ready)

This plan deliberately does not connect a git remote or push a tag — per
the user's explicit choice during brainstorming. Whenever ready to do a
real release:

```bash
git remote add origin <github-repo-url>
git push -u origin main
git tag v0.1.0
git push origin v0.1.0
```

The `release.yml` workflow then runs automatically, and a draft release
with 6 archives, `checksums.txt`, and generated notes appears on GitHub
— review it there and click "Publish" when ready.
