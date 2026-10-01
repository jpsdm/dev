# Metrics — Design Spec

Date: 2026-09-27
Status: Approved for planning
Sub-project: 3c of 4 (sub-project 3 of 4, split into 3a/3b/3c — this is the last piece)

## Context

Sub-projects 3a and 3b shipped the workspace scaffold, `new`/`scratch`, and
`clean`/`archive`. This sub-project adds `dev workspace metrics` (alias `dev
ws m`): a read-only report on the workspace's size and shape — per-directory
breakdown, totals, and the largest project/file. This is the last piece of
sub-project 3; after this, workspace management as scoped by the brief is
complete (sub-project 2c's additional language providers and sub-project 4's
release automation remain separate, not-yet-scheduled work).

## Goals

- `dev workspace metrics` prints a per-directory table (`src`, `scratch`,
  `archive`, `base`) with project count and size, a total row, and then file
  count, directory count, largest project, and largest file — covering every
  item the brief's §22 lists.
- Size calculation never follows symlinks — a symlinked directory
  contributes only its own (typically tiny) directory-entry size, never its
  target's contents.
- Sizes are formatted in binary units (1024-based: KiB/MiB/GiB math, labeled
  `KB`/`MB`/`GB` to match the brief's own labels and what `du -h`/Finder/
  Explorer already train users to expect), two decimal places always.
- An empty workspace (no projects, no files anywhere) still produces a
  complete, valid report — zeroed table, `(none)` for largest project/file —
  never an error.

## Non-goals (deferred)

- Per-directory file/directory counts — the brief's "quantidade de
  arquivos"/"quantidade de diretórios" are workspace-wide totals only, not
  broken out per `src`/`scratch`/`archive`/`base`.
- Listing every individual project's size — "tamanho por projeto" is the
  data `Collect` computes internally to find the largest project; it is not
  printed as its own section. A future `dev workspace metrics <name>`
  (single-project detail) is a plausible later addition but out of scope
  here — nothing in this design blocks adding it.
- Any destructive or interactive behavior — this command is pure read/report,
  no `--dry-run` (nothing to preview), no confirmation prompt.
- Exact byte-for-byte reproduction of the brief's example table's spacing —
  unlike `clean --dry-run`'s literal required wording (brief §38), the
  metrics table in brief §22 is illustrative example *data* (fake project
  counts and sizes), not a mandated literal string. Column alignment via
  `text/tabwriter` satisfies the intent (a readable, aligned table with
  those exact headers and row contents) without matching the brief's exact
  whitespace character-for-character.

## `internal/metrics` — `Collect`

```go
// DirStats is one row of the per-directory table. ProjectCount is -1 for
// directories (currently only "base") that don't have a "projects"
// concept — the sentinel a caller renders as "-" rather than a number.
type DirStats struct {
	Name         string // "src", "scratch", "archive", or "base"
	ProjectCount int
	Size         int64 // bytes
}

// LargestProject names the single largest top-level project directory
// across src/, scratch/, and archive/ combined (base/ has no "projects").
// Zero value (empty Name) means the workspace has no projects at all.
type LargestProject struct {
	Name string // e.g. "src/api"
	Size int64
}

// LargestFile names the single largest regular file anywhere in the
// workspace (all four directories). Zero value (empty Path) means the
// workspace has no files at all.
type LargestFile struct {
	Path string // relative to the workspace root, e.g. "archive/old/dist/bundle.js"
	Size int64
}

// Report is everything dev workspace metrics prints.
type Report struct {
	Path           string
	Directories    []DirStats // always exactly 4, in order: src, scratch, archive, base
	TotalSize      int64
	TotalFiles     int
	TotalDirs      int
	LargestProject LargestProject
	LargestFile    LargestFile
}

// Collect walks root (a workspace root, already scaffolded) once and
// returns a complete Report. Never follows symlinks: filepath.WalkDir
// does not descend into a symlinked directory regardless of its
// target, and a symlink's own (non-directory) entry contributes only
// its own on-disk size, the same as any other file. An empty or
// not-yet-fully-scaffolded workspace produces a valid, zeroed Report
// (LargestProject/LargestFile left at their zero values) rather than
// an error.
func Collect(root string) (Report, error)
```

`Collect` does one `filepath.WalkDir(root, ...)` pass, tracking: a running
byte total per top-level directory (`src`/`scratch`/`archive`/`base`); a
running byte total per immediate project directory under `src`/`scratch`/
`archive` (e.g. key `"src/api"`) — used only to find the maximum, never
returned as a list; a global file count and directory count (every entry
`WalkDir` visits except the root itself — a symlink is counted as a file,
since its `fs.DirEntry.IsDir()` reflects its own type, not its target's);
and the single largest regular file seen, by size. `ProjectCount` for
`src`/`scratch`/`archive` comes from a separate, simple `os.ReadDir` +
directory-filter count (the same pattern `internal/workspace.CleanAll`
already uses to enumerate projects) — not derived from the wide walk.
`base`'s `ProjectCount` is hardcoded to `-1`.

## `cmd/workspace.go` — `metrics` command

```text
dev workspace metrics    (alias: m, under either "workspace" or "ws")
```

`workspaceMetricsCmd`: `Args: cobra.NoArgs`, alias `m`. `RunE` calls
`resolveWorkspaceRoot(cmd)` (same as every other workspace subcommand — no
special-casing; if this is the very first workspace command the user ever
runs, it triggers the same first-use prompt `new`/`scratch`/`clean`/`archive`
already would), then `metrics.Collect(root)`, then renders the `Report`
via `text/tabwriter` in this shape:

```
Workspace Metrics

Path:
<root>

Directory       Projects    Size
src             <n>         <size>
scratch         <n>         <size>
archive         <n>         <size>
base            -           <size>

Total                       <total size>

Files: <total files>
Directories: <total dirs>
Largest project: <name> (<size>)
Largest file: <path> (<size>)
```

`Largest project`/`Largest file` print `(none)` in place of `<name> (<size>)`
/`<path> (<size>)` when the corresponding `Report` field is at its zero
value (empty `Name`/`Path`).

## Size formatting

A small formatter (`formatSize(bytes int64) string` — lives alongside the
render code, not in `internal/metrics`, since it's a presentation concern)
converts to `B`/`KB`/`MB`/`GB`/`TB` using 1024-based thresholds, always two
decimal places (`8.42 GB`, `0.04 GB` never `42 MB`-style unit-switching
inconsistency — pick the largest unit where the value is `>= 1`, falling
back to `B` for anything under 1024 bytes).

## Error handling & idempotency

- `Collect` returns an error only for a real filesystem failure during the
  walk (permission error, etc.) — a missing or empty `src`/`scratch`/
  `archive`/`base` subdirectory is not an error (matches
  `workspace.EnsureScaffold`'s guarantee that these always exist once any
  workspace command has run once; `resolveWorkspaceRoot` already calls it
  before `Collect` runs).
- Running `dev workspace metrics` repeatedly is inherently idempotent — it
  never writes anything.

## Testing approach

- `internal/metrics`: `Collect` against real `t.TempDir()` fixtures —
  multiple projects across `src`/`scratch`/`archive` with known file sizes,
  confirming per-directory totals, project counts, the total row, global
  file/directory counts, and the largest-project/largest-file picks;
  `base`'s `ProjectCount == -1`; a symlinked directory inside a project
  contributes only its own entry size and its target's content is never
  counted (create a real symlink pointing at a large file/directory
  elsewhere in the temp tree and confirm the size doesn't leak in); a
  completely empty (but scaffolded) workspace returns a valid zeroed
  `Report` with empty `LargestProject.Name`/`LargestFile.Path`.
- `cmd/workspace_test.go`: a command-level test asserting the rendered
  output contains the expected headers, directory rows, total, and
  summary lines for a small known fixture, plus the `ws m` alias chain.

## Acceptance criteria for this sub-project

```bash
dev workspace metrics
dev ws m
```

Both against a workspace with at least one project in each of `src`,
`scratch`, and `archive`, and an empty workspace (immediately after
`dev workspace`'s first-use scaffold, before any project exists).
