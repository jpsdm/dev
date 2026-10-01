# Releasing `dev`

This project's release pipeline (`.goreleaser.yml` + `.github/workflows/release.yml`)
is entirely **git-tag-driven**. Nothing about pushing ordinary commits — including
README/documentation-only changes — ever triggers a release. A release only ever
starts because someone deliberately pushes a tag matching `vX.Y.Z`. That's the whole
mental model this doc is built around.

Two GitHub Actions workflows exist, gated on completely different triggers:

| Workflow | Triggers on | Does |
|---|---|---|
| `.github/workflows/ci.yml` | every push to `main`, every pull request | fmt check, `go vet`, lint, test, build. **Never creates a release.** |
| `.github/workflows/release.yml` | a pushed tag matching `v*.*.*` — nothing else | GoReleaser: cross-compiles, archives, checksums, creates a **draft** GitHub Release |

A tag that doesn't match `v*.*.*` (e.g. `checkpoint`, `wip-1`) triggers neither
workflow. A branch push — of any file, including `README.md` — triggers only `ci.yml`.

## Part 1 — The first push

This repo's remote is already configured (`git remote -v` shows `origin`, pointing at
GitHub) and reachable, but nothing has been pushed to it yet — `git tag` is empty and
`git ls-remote origin` returns nothing. If you're setting this up fresh elsewhere, add
the remote first:

```bash
git remote add origin git@github.com:<you>/<repo>.git   # only if origin isn't set yet
```

Then push `main`:

```bash
git push -u origin main
```

`-u` sets the upstream tracking branch, so every future `git push`/`git pull` on
`main` works without repeating `origin main`. This alone does **not** trigger
`release.yml` — only `ci.yml` runs, on the push to `main`. Check the **Actions** tab
on GitHub to confirm it goes green.

## Part 2 — Cutting your first release

Use the `release-tag` skill (`.claude/skills/release-tag/`) for this — it runs
`make check` first, confirms the version number with you, and creates the tag, but
**never pushes it automatically**, since pushing a tag is what actually triggers a
public, visible release. The manual version of the same steps:

```bash
# 1. Confirm the tree is clean and make check passes
git status
make check

# 2. Decide the version. First release is conventionally v0.1.0.
git tag -a v0.1.0 -m "v0.1.0"

# 3. Confirm what got tagged
git show v0.1.0 --stat

# 4. Push the tag — this is the step that actually triggers release.yml
git push origin v0.1.0
```

What happens next, automatically, on GitHub:

1. `release.yml` runs (visible under **Actions**).
2. GoReleaser cross-compiles for linux/darwin/windows × amd64/arm64 (6 binaries),
   packages each into an archive (`.tar.gz` for Linux/macOS, `.zip` for Windows),
   and writes a single `checksums.txt` covering all six.
3. A **draft** GitHub Release is created under **Releases**, with all 6 archives and
   `checksums.txt` attached, and release notes auto-generated from the commit log
   since the previous tag (for `v0.1.0`, that's every commit in the repo's history).

Nothing is public yet. Go to **Releases** on GitHub, open the draft, edit the notes
if you want to trim them, and click **Publish release** when you're ready. Until you
do that, only you can see it.

**One thing to know:** pushing a tag does *not* re-run `ci.yml` on that exact commit
(tag pushes aren't branch pushes, so `ci.yml`'s trigger doesn't match). That's why
`release-tag`'s own first step is running `make check` locally before tagging — the
tag itself carries no independent proof that tests passed on that commit.

## Part 3 — The ongoing workflow, with the two examples you actually care about

### Example A: a documentation-only change (safe — never releases anything)

```bash
# Fix a typo in README.md, or update any doc
git add README.md
git commit -m "Fix a typo in the Usage section"
git push origin main
```

**What happens:** `ci.yml` runs (lint/test/build) and goes green. `release.yml` does
not run — no tag was pushed. No release is created, draft or otherwise. This is true
for *any* commit pushed to `main`, not specifically because it "looks like" a docs
change — the release pipeline doesn't inspect which files changed at all. It only
watches for a tag ref. You could push a change touching every file in the repo and
still trigger nothing beyond `ci.yml`, as long as you don't also push a matching tag.

There is no path-filtering rule to configure here (e.g. "skip release if only
`*.md` files changed") because there's nothing to skip — `release.yml` was never
going to run from a branch push in the first place.

### Example B: cutting a real second release later (`v0.2.0`)

```bash
# After merging real feature/fix work to main via one or more normal pushes...
make check
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
```

**What happens:** identical to Part 2 — `release.yml` runs, 6 archives + checksums
get built, a new draft release for `v0.2.0` appears (this time its auto-generated
notes cover only the commits since `v0.1.0`, not the whole history). Review and
publish it the same way.

### Quick reference

| You do this | `ci.yml` runs? | `release.yml` runs? |
|---|---|---|
| `git push origin main` (any files, including docs) | Yes | No |
| Open or update a pull request | Yes | No |
| `git push origin v0.2.0` (tag matches `v*.*.*`) | No | Yes |
| `git push origin checkpoint` (tag doesn't match `v*.*.*`) | No | No |

## Why draft, not auto-published

`release.draft: true` in `.goreleaser.yml` is a deliberate choice, not a default —
every release this pipeline creates needs a manual click to go public. This is the
one manual step in an otherwise fully automated pipeline, and it's intentional: it's
the last point where a mistake (an unwanted commit swept into the tag, a wrong
version number, generated notes you want to trim) can still be caught before anyone
outside sees it.
