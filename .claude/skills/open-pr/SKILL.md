---
name: open-pr
description: Push the current branch and open a pull request for the dev CLI, following this project's exact conventions (Conventional Commit title, the real PR template checklist filled out with verified statuses, make check passed first). Use when the user asks to "open a PR", "create a pull request", "submit this", or when wrapping up a finished piece of work that should go through review instead of straight to main.
---

# Open a Pull Request

This project now works on a PR-based workflow (branch protection on `main` requires it) —
see `CONTRIBUTING.md` for the human-facing version of these same conventions. This skill is
the concrete "how do I actually do that for `jpsdm/dev`" companion to
`superpowers:finishing-a-development-branch`'s generic "push and create PR" option: use this
skill to work out the branch name, commit hygiene, and PR content; hand off to
`finishing-a-development-branch` for the worktree/environment mechanics if you're inside that
skill's flow already. Standalone, this skill is the whole thing end to end.

## Step 1: Get the work onto its own branch

Never open a PR from `main` itself.

- **Already on a feature branch:** nothing to do here, skip to Step 2.
- **On `main` with commits already made there** (this repo's history has direct-to-`main`
  commits from before branch protection was enabled — that pattern is over, but a session can
  still start this way by habit): create a branch at the current commit, then reset `main`
  back to match `origin/main` so it doesn't carry local-only commits:
  ```bash
  git branch <type>/<slug> HEAD
  git reset --hard origin/main
  git checkout <type>/<slug>
  ```
  **`git reset --hard` is destructive — confirm with the user before running it**, and confirm
  `git branch <type>/<slug> HEAD` succeeded first so the commits are safely captured on the new
  branch before `main` moves. If `origin/main` isn't fetched recently, `git fetch origin main`
  first.
- **On `main` with no new commits (clean):** just create and check out a new branch —
  `git checkout -b <type>/<slug>`.

**Branch naming:** `<type>/<kebab-case-slug>`, where `<type>` is the dominant Conventional
Commit type of the change (see Step 2) — e.g. `feat/go-language-provider`,
`fix/windows-path-separators`, `docs/contributing-guide`.

## Step 2: Verify every commit follows Conventional Commits

```bash
git log origin/main..HEAD --format='%H %s'
```

Each subject line must match `^(feat|fix|docs|refactor|test|chore|ci|build|perf)(\([a-z0-9_/-]+\))?: .+`
— see `CONTRIBUTING.md`'s "Commit messages" section for the full convention. Anything from
before this project adopted the convention doesn't count; you're only checking commits that
are actually part of *this* branch's new work.

If a commit doesn't match:

- **The branch has never been pushed:** rewriting is cheap — `git commit --amend` for the tip
  commit, or `git rebase -i origin/main` for one further back. Confirm the reword with the user
  before running it; don't silently rewrite their message.
- **The branch was already pushed** (resuming a previous session, say): rewriting now needs a
  force-push. Treat this as the destructive operation it is — explain what you'd change and
  why, and only force-push with explicit confirmation. When in doubt, leave it and mention it
  as a note for the PR description instead of rewriting.

## Step 3: `make check`

Must be clean. Never open a PR on a red branch — CI runs the identical gate and will fail the
same way, and branch protection requires that check to pass before merge anyway.

## Step 4: Push the branch

```bash
git push -u origin <type>/<slug>
```

Confirm with the user first — like any push, this is a visible, shared-state action.

## Step 5: Write the PR title and body

**Title:** Conventional-Commit style, matching Step 1's branch type —
`<type>(<scope>): <summary>`. If the branch is a single commit, the title is usually that
commit's own subject line verbatim. If it's several commits forming one coherent change,
synthesize a title the way `release-tag` synthesizes a release description: read the actual
diffs for anything non-obvious, don't just concatenate subject lines. If this repo's merge
strategy is squash-merge (check the repo's GitHub settings, or just assume it might be), this
title becomes the permanent commit message on `main` — get it right.

**Body:** fill `.github/PULL_REQUEST_TEMPLATE.md`'s actual content — don't leave it as a blank
template for the human to fill in later.

- **"What does this PR do?"** — a real one-or-two-sentence description, plus `Closes #N` if
  there's a known related issue (ask if it's unclear which issue; don't guess a number).
- **Checklist** — check each box only if it's actually true, verified, not assumed:
  - `make check` passes locally — checked, because Step 3 just confirmed it.
  - Tests added/updated — check the actual diff (`git diff origin/main..HEAD -- '*_test.go'`)
    before checking this box.
  - Tests exercise real behavior, not mocks — same: look at what the tests in the diff
    actually do.
  - Any externally-sourced string reaching a filesystem path is validated — only relevant if
    the diff touches a provider or similar; check `.` (not applicable) if it plainly doesn't,
    rather than leaving it unchecked and unexplained.
  - Commit messages follow Conventional Commits — checked, because Step 2 just confirmed it.
  - No unrelated changes bundled in — check the diff's file list against what the PR claims to
    do.
- **"Notes for reviewers"** — anything a reviewer would otherwise have to ask: a tradeoff you
  made, something you're unsure about, an alternative you considered and rejected.

## Step 6: Create the PR

**If `gh` is installed and authenticated** (`gh auth status`): create it directly.
```bash
gh pr create --title "<title>" --body "$(cat <<'EOF'
<body>
EOF
)"
```
Confirm the title/body with the user before running this — creating a PR is exactly the kind
of visible, shared-state action this project's conventions ask you to confirm first.

**If `gh` isn't available:** push already happened in Step 4, so GitHub already knows about the
branch. Print the compare URL —
`https://github.com/jpsdm/dev/compare/main...<type>/<slug>?expand=1` — and the ready-to-paste
title/body as a Markdown block, the same way `release-tag` hands over a release description
when `gh` isn't set up. Offer to help set up `gh auth login` if the user would rather this step
be automatic next time.

## Step 7: After creation

Report the PR URL back (if `gh` succeeded). CI runs automatically on the PR and branch
protection requires it — and a review — to pass before merge. **Never approve, merge, or
dismiss review on your own PR** — that decision belongs to the human reviewer, same as every
other visible action this project's conventions gate on explicit confirmation.
