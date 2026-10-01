# Clean & Archive — Design Spec

Date: 2026-09-27
Status: Approved for planning
Sub-project: 3b of 4 (sub-project 3 of 4, split into 3a/3b/3c)

## Context

Sub-project 3a shipped the workspace scaffold (`src/scratch/archive/base`), the
first-use path prompt, and `dev workspace new`/`scratch`. This sub-project adds
the two commands that make `src/` and `archive/` actually useful over time:
`dev workspace clean <name>` (remove a project's gitignored files) and
`dev workspace archive <name>` (clean, then move to `archive/`), plus
`dev workspace clean --all` and `--dry-run` support for both.

This spec covers only 3b. Sub-project 3c (`dev workspace metrics`) is untouched.

## Goals

- `dev workspace clean <name>` removes exactly the set of paths under
  `src/<name>` that `.gitignore` would mark ignored — the same set `git clean
  -Xdf` would remove, using real `.gitignore` semantics (glob, `!` negation,
  nested `.gitignore` files, directory patterns) via a mature library, not a
  hand-rolled parser.
- `dev workspace clean <name> --dry-run` reports exactly what would be
  removed, in the brief's own format, and removes nothing.
- `dev workspace clean --all [--dry-run]` runs the same operation across
  every project in `src/`, requiring explicit y/N confirmation before any
  real (non-dry-run) deletion.
- `dev workspace archive <name> [--dry-run]` cleans `src/<name>` for real,
  then moves it to `archive/<name>` — never moving if the clean step failed,
  and never overwriting an existing `archive/<name>`.
- A project's `.git` directory, if present, is never touched by `clean` or
  `archive`, regardless of what any `.gitignore` pattern matches — the same
  guarantee real `git clean` gives.
- None of this requires the target project to actually be a git repository —
  clean/archive read `.gitignore` files as plain text and match patterns
  against them; they never shell out to `git` or require a `.git` directory
  to exist at all. A project created via `dev workspace new` (which only
  copies `base/`, never runs `git init`) works the same as a real git repo.

## Non-goals (deferred)

- `dev workspace metrics` (sub-project 3c).
- A global excludes file (`~/.gitignore_global`) or `.git/info/exclude` —
  only each project's own `.gitignore` file(s) are consulted, matching the
  brief's own wording ("`.gitignore` real").
- `clean`/`archive` operating on `scratch/` — the brief's only examples
  target `src/` projects; `scratch/` stays purely `new`/`scratch`'s domain
  from 3a.
- Any confirmation prompt for single-project `clean <name>` or `archive
  <name>` — the brief requires explicit confirmation only for `clean --all`
  (§21); single-project operations proceed directly (`--dry-run` exists
  precisely for previewing them safely first).

## New dependency: `github.com/go-git/go-git/v5/plumbing/format/gitignore`

Only this one subpackage of go-git is imported — not a full `go-git.Repository`,
no networking, no SSH/crypto machinery. It provides `gitignore.Pattern`
(`ParsePattern(line string, domain []string) Pattern`), matched via
`gitignore.NewMatcher(patterns []Pattern)` and `Matcher.Match(path []string,
isDir bool) bool`. Each pattern carries a `domain` — the directory (as path
components relative to the project root) its `.gitignore` file lives in —
which is how the matcher enforces git's real nested-`.gitignore` scoping:
a pattern only applies to paths under its own domain, and negation (`!`) in a
deeper `.gitignore` can re-include something an ancestor's pattern excluded,
without affecting unrelated directories.

This project reads `.gitignore` files itself (plain `os.ReadFile` +
line-by-line scanning, skipping blank lines and `#` comments) and constructs
`Pattern`s directly with `ParsePattern`, rather than using the library's own
`ReadPatterns` helper (which expects a `go-billy` virtual filesystem — an
unnecessary second directory-abstraction layered on top of the `os`/
`filepath.WalkDir` traversal this codebase already uses everywhere else).
The exact call shapes will be confirmed empirically at the start of
implementation (mirroring how 3a's Huh integration was verified); if
`ParsePattern`/`NewMatcher`/`Match` don't behave as described here, the
documented fallback is `github.com/sabhiram/go-gitignore` with this project
owning the nested-domain combination logic by hand.

## `internal/workspace/clean.go` — `Clean`, `CleanAll`

```go
// Clean removes every path under root/src/<name> that .gitignore rules
// (collected from every .gitignore file in the project, honoring nested
// scoping and negation) mark ignored — the same set `git clean -Xdf`
// would remove. It never requires <name> to be a git repository. The
// project's own .git directory, if present, is never a candidate for
// removal. Returns the list of removed (or, if dryRun, would-be-removed)
// paths relative to the project root, with a trailing "/" on directory
// entries — removed directories are reported and deleted as a single
// unit, never descended into once matched, matching git's own clean
// reporting. Returns an error, with nothing removed, if the project
// doesn't exist.
func Clean(root, name string, dryRun bool) ([]string, error)

// CleanAll runs Clean(root, name, dryRun) for every project currently
// under root/src, independently — one project's clean failure does not
// prevent the others from running. Returns a map of project name to its
// removed-paths list (only for projects that succeeded) and an
// aggregated error (via errors.Join) naming every project that failed,
// or a nil error if all succeeded. An empty (or not-yet-existing) src/
// yields an empty map and a nil error — not an error condition.
func CleanAll(root string, dryRun bool) (map[string][]string, error)
```

Internally, `Clean` collects every `.gitignore` file under the project
directory first (a plain `filepath.WalkDir` pass building `Pattern`s tagged
with their own domain), builds one `gitignore.Matcher` from the combined
list, then walks the project directory a second time, checking each
candidate path against the matcher. A matched directory is added to the
result and not descended into (`filepath.SkipDir`); a matched file is added
directly. `.git` (top-level only — a project is never expected to contain a
second, nested `.git`, and this spec doesn't attempt to protect one) is
skipped unconditionally before any matcher check. When `dryRun` is `false`,
each collected path is removed via `os.RemoveAll` after the walk completes
(never during it, so the removal list a caller sees is always complete and
accurate even if a later removal in the list fails partway through).

## `internal/workspace/archive.go` — `Archive`

```go
// Archive moves root/src/<name> to root/archive/<name>. It first errors,
// touching nothing, if root/archive/<name> already exists. It then runs
// Clean(root, name, dryRun) for real — if that fails, the move never
// happens and the error is returned as-is. If dryRun is true, Archive
// stops after reporting what Clean would remove (the returned []string)
// and never moves anything, regardless of whether the clean-preview
// itself "succeeded." If dryRun is false and the clean succeeds, the
// project directory is moved into place with a single os.Rename (both
// src/<name> and archive/<name> are siblings under the same workspace
// root, so no temp-copy dance is needed the way New/Scratch's
// cross-content template copy requires).
func Archive(root, name string, dryRun bool) ([]string, error)
```

## `cmd/workspace.go` — commands, flags, aliases

```text
dev workspace clean <name> [--dry-run]
dev workspace clean --all [--dry-run]
dev workspace archive <name> [--dry-run]   (alias: a, under either "workspace" or "ws")
```

`workspaceCleanCmd`: `Args: cobra.MaximumNArgs(1)`, alias `c`, two bool flags
(`--all`, `--dry-run`). `RunE` validates the argument shape before doing
anything else: exactly one of "a name was given" / "`--all` was passed" must
be true — both or neither is a plain usage error, no filesystem touched.

- **Single-project path** (`clean <name>`): calls `resolveWorkspaceRoot`,
  then `workspace.Clean(root, name, dryRun)`. Non-dry-run: prints what was
  removed (or "Nothing to clean" if the list is empty) via `cliutil`, then a
  `✓ Cleaned src/<name>` line. Dry-run: prints the brief's exact format —
  "The following files would be removed:", the list, then "No files were
  deleted." — and does not print a `✓` success line (nothing happened).
- **`--all` path**: calls `resolveWorkspaceRoot`, then, if not `--dry-run`,
  prompts via `shell.Confirm` ("Remove ignored files from every project in
  src/? [y/N] ") before doing anything — declining prints the same
  "No changes made." wording `dev setup` already uses and exits cleanly.
  Dry-run skips the prompt entirely (nothing is being deleted, so there's
  nothing to confirm) and goes straight to `workspace.CleanAll(root, true)`,
  printing each project's preview. A confirmed real run calls
  `workspace.CleanAll(root, false)`, prints each project's result, and
  surfaces the aggregated error (if any) after printing every project's own
  outcome — one project's failure is reported, not hidden, but doesn't stop
  the others' results from printing first.

`workspaceArchiveCmd`: `Args: cobra.ExactArgs(1)`, alias `a`, one bool flag
(`--dry-run`). Calls `resolveWorkspaceRoot`, then `workspace.Archive(root,
name, dryRun)`. Dry-run prints the same clean-preview format as `clean
--dry-run` (since that's genuinely all that would happen — nothing moves).
Non-dry-run prints what was cleaned, then `✓ Archived src/<name> to
archive/<name>`.

## Error handling & idempotency

- `clean`/`archive` on a nonexistent project name is a plain error
  (`project %q does not exist at %s`), zero filesystem mutation.
- `archive <name>` on an existing `archive/<name>` is a plain error, checked
  *before* the clean step runs — no point cleaning a project that can't be
  moved, and no risk of destructively cleaning `src/<name>` only to fail on
  the rename.
- A `Clean` failure partway through removal (e.g. a permission error on one
  matched path) still returns every path it successfully removed before the
  failure, alongside the error — callers (including `Archive`) can report
  exactly what happened rather than an opaque failure.
- `clean --all` isolates failures per project (`CleanAll`'s aggregated
  `errors.Join`); one broken project's clean does not block the others from
  running or being reported.
- Running `clean <name>` or `archive <name>` again after a successful run is
  simply a fresh operation against whatever now exists on disk — there's no
  special "already cleaned" state to track, since a clean project (nothing
  left to match) just produces an empty removal list, and `archive` still
  errors on its own already-covered "target already exists" case if run
  twice for the same name.

## Testing approach

- `internal/workspace`: `Clean`/`CleanAll`/`Archive` against real
  `t.TempDir()` fixtures — a project with a root `.gitignore` (glob,
  negation) and a nested subdirectory `.gitignore` overriding/extending it,
  confirming the matched set is exactly right and nothing else is touched;
  `.git` presence never removed even when a (deliberately adversarial)
  `.gitignore` pattern would otherwise match it; dry-run leaves the
  filesystem untouched while still reporting the same set a real run would
  remove; `Archive` never moves when `Clean` fails (simulate via an
  unreadable/unremovable matched path) and never touches `src/<name>` when
  `archive/<name>` already exists; `CleanAll` continues past one project's
  induced failure and reports both the successes and the failure.
- `cmd/workspace_test.go`: command-level tests for `clean <name>`, `clean
  --all` (both the declined-confirmation-changes-nothing path and the
  confirmed path, via piped stdin — the same pattern `cmd/setup_test.go`
  already established), `clean --all --dry-run` (no prompt at all), `archive
  <name>`, and the usage-error paths (`clean` with neither a name nor
  `--all`, `clean <name> --all` with both).

## Acceptance criteria for this sub-project

```bash
dev workspace clean api --dry-run
dev workspace clean api
dev workspace archive api --dry-run
dev workspace archive api
dev workspace clean --all --dry-run
dev workspace clean --all

dev ws c api
dev ws a api
dev ws c --all
```

`dev workspace metrics` remains unimplemented until 3c.
