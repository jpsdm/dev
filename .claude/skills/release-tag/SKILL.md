---
name: release-tag
description: Cut a new vX.Y.Z release tag for the dev CLI, following this project's exact GoReleaser-based release scheme. Use when the user asks to "release", "cut a version", "tag a release", or "bump the version".
---

# Release Tag

This project's release pipeline (`.goreleaser.yml` + `.github/workflows/release.yml`) is entirely
git-tag-driven: pushing a tag matching `vX.Y.Z` is the only trigger. There is no separate
version-bump commit or file to edit — the tag itself is the single source of truth for the
version number (see `docs/superpowers/specs/2026-09-28-release-automation-design.md` for the
full design rationale).

## Before tagging

1. **Confirm the working tree is clean and on `main`.** This repo has no branch workflow — if
   there's uncommitted work, stop and ask the user whether to commit it first.
2. **Run `make check`.** Never tag a commit that hasn't passed fmt/vet/lint/test/build. If it
   fails, stop — do not tag a red commit.
3. **Determine the next version number.** List existing tags (`git tag --sort=-v:refname`).
   With no prior tags, the first release is typically `v0.1.0`. Otherwise, ask the user whether
   this is a patch/minor/major bump relative to the last tag — don't guess semver intent
   silently, since "what changed since the last release" is a judgment call only they can make
   confidently (skim `git log <last-tag>..HEAD --oneline` and offer a recommendation, but confirm
   before creating the tag).
4. **Check for a remote.** `git remote -v`. If none exists, the tag can still be created locally
   (harmless, fully reversible with `git tag -d`), but the release workflow will never run until
   the user connects a remote and pushes the tag — say this explicitly so they aren't confused
   later when nothing happens on GitHub.

## Creating the tag

Use an **annotated** tag (not lightweight) — GoReleaser's changelog generation and `git describe`
both work better with annotated tags, and it gives the tag its own message distinct from the
commit it points at:

```bash
git tag -a v0.2.0 -m "v0.2.0"
```

Confirm what was tagged:

```bash
git show v0.2.0 --stat
```

## After tagging

- **Never push automatically.** Pushing a tag is a real, visible action (if a remote exists, it
  immediately triggers the release workflow) — this is exactly the kind of side effect that needs
  the user's explicit go-ahead, not an assumption that "tag" implies "push."
- If a remote exists and the user confirms they want to push: `git push origin v0.2.0`. Then tell
  them what happens next — the workflow builds and creates a **draft** GitHub Release (per this
  project's config), which they still need to review and publish manually on GitHub.
- If no remote exists yet, tell the user the tag is created locally and will do nothing until they
  connect a remote and push it (see `README.md`'s Releases section for the exact commands, or just
  read `docs/superpowers/plans/2026-09-28-release-automation.md`'s "Post-plan manual step").

## Writing the release description

`.goreleaser.yml`'s `changelog:` section already makes GoReleaser populate the draft release's
body on its own — but only with a flat, unsorted-by-theme list of commit subject lines since the
last tag. There is no `gh` CLI configured in this environment to push a better body onto the draft
automatically, so this step produces text for the user to paste in by hand when they review the
draft on GitHub (offer to set up `gh auth login` if they'd rather this be automatic going forward
— that's a one-time interactive login only the user can complete).

After determining the version number (you already have `git log <last-tag>..HEAD --oneline` from
that step) and once the tag exists, draft a release description as a single Markdown block:

1. **A short description paragraph** (2-4 sentences, plain prose) summarizing what this release is
   about and why, not a restatement of the commit list. Read the actual diffs for anything
   non-obvious from subject lines alone (`git log <last-tag>..HEAD --stat`, or the full diff for a
   small release) — don't guess from titles alone.
2. **A changelog grouped by theme**, not the flat chronological list GoReleaser generates on its
   own. Infer groups from what each commit actually changed (e.g. "Features", "Fixes",
   "Documentation", "Internal" — pick whatever categories actually fit this release; don't force
   empty ones). Commits after this project adopted Conventional Commits (see `CONTRIBUTING.md`)
   carry a `feat:`/`fix:`/`docs:`/etc. prefix that's a useful starting signal for grouping, but
   still read each commit — a prefix says the commit's type, not which release theme it belongs
   under. Older commits predate the convention and have no prefix at all; group those by reading
   them the same way.

Present this as a ready-to-paste Markdown block and tell the user it's meant to **replace**
GoReleaser's auto-generated changelog in the draft's body (both together would be redundant), once
the draft actually exists — remind them the draft only appears after the tag is pushed and the
release workflow finishes.

## If something needs to be undone

A tag that hasn't been pushed is trivially reversible: `git tag -d vX.Y.Z`. A tag that has been
pushed is a different, more disruptive situation (deleting/moving a pushed tag can break anyone
who already fetched it, and may have already triggered a real release) — treat that as a
destructive action requiring explicit confirmation, not something to do reflexively.
