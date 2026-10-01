# Workspace Core — Design Spec

Date: 2026-09-27
Status: Approved for planning
Sub-project: 3a of 4 (sub-project 3 of 4, split into 3a/3b/3c)

## Context

Sub-projects 1, 2a, and 2b shipped the CLI foundation, language/runtime
management (`dev lang`), and PATH/shims (`dev env`, `dev setup`). Sub-project
2c (additional language providers) is explicitly deferred by the user's own
choice, not by this spec.

Sub-project 3 is workspace management: a place, separate from
`$DEV_HOME`, where the user's actual projects live, plus commands to
create, clean, and archive them, and to report on their size. That whole
surface (`new`, `scratch`, `clean`, `archive`, `metrics`, plus a
`.gitignore`-aware cleanup engine) is comparable in size to sub-project 2
as a whole, so — following the same precedent that split sub-project 2
into 2a/2b/2c — it's split into three parts:

- **3a (this spec):** workspace location + first-use setup, the
  `src/scratch/archive/base` scaffold, and the two commands that don't
  need a cleanup engine yet: `new` and `scratch`.
- **3b (later):** `.gitignore`-aware `clean`, `clean --all`, `archive`
  (which is `clean` then move), `--dry-run`.
- **3c (later):** `dev workspace metrics`.

This spec covers only 3a.

## Goals

- `dev workspace`, `dev workspace new <name>`, and `dev workspace scratch
  <name>` all work, with `ws` / `ws s` aliases per the brief.
- The workspace location is asked once, interactively, the first time any
  workspace command runs — never assumed or silently created at a guessed
  path — and persisted to the config file sub-project 1 already scaffolded
  (`config.Config.Workspace.Path`).
- Every workspace command is idempotent in the sense the brief's §39
  describes: running `dev workspace` (or any other workspace command)
  twice never re-prompts, never recreates, never destroys anything already
  there.
- `new`/`scratch` copy `workspace/base/`'s contents (hidden files, nested
  directories, permissions where meaningful) into the new project
  directory, atomically — a failure partway through never leaves a
  half-copied project where a full one, or none at all, should be.
- Creating a project whose name already exists is a plain, fast error —
  nothing is touched.

## Non-goals (deferred)

- `clean`, `clean --all`, `archive`, `--dry-run` (sub-project 3b).
- `dev workspace metrics` (sub-project 3c).
- Any `.gitignore` parsing/matching (only needed starting in 3b).
- Managing `base/`'s own contents — 3a only ensures the directory exists;
  populating it with real template files is a manual, out-of-band user
  action (per the brief, §16).

## New dependency: `github.com/charmbracelet/huh`

The brief's stack section (§2) explicitly names Huh for
"prompt/interatividade," but nothing before this sub-project has needed
more than a plain yes/no (`internal/shell.Confirm`, built on `bufio`
alone). The first-use workspace-path prompt is a real text input with a
pre-filled default — Huh earns its place here rather than being added for
convenience. It is not used anywhere else in this sub-project.

## `internal/platform` — `DefaultWorkspacePath`

```go
// DefaultWorkspacePath returns a suggested default workspace location:
// <home>/Documents/workspace if a Documents directory already exists
// (the common case on macOS and Windows, and increasingly common on
// Linux desktops), otherwise <home>/workspace. This is only ever used
// to pre-fill the first-use prompt — dev never creates or assumes a
// workspace location without the user confirming it.
func DefaultWorkspacePath() (string, error)
```

Go's stdlib has no "Documents directory" API (`os.UserHomeDir` is the
only relevant one), so this does its own `os.Stat` check rather than
guessing blindly. It is a suggestion only: the returned value pre-fills
the Huh prompt's input, and whatever the user submits — accepted as-is,
edited, or replaced entirely — is what gets saved.

## `internal/workspace` — domain logic

Pure filesystem logic, no interactive I/O (mirrors `internal/installer`'s
separation: prompting and confirmation stay in the `cmd` layer, exactly
like `dev setup` calls `shell.Confirm` itself rather than pushing that
concern into `internal/shell`).

```go
// EnsureScaffold creates root's src/, scratch/, archive/, and base/
// subdirectories if they don't already exist. Safe to call on every
// invocation.
func EnsureScaffold(root string) error

// ValidProjectName reports an error if name could escape root when
// used as a path component: empty, ".", "..", or containing a path
// separator. Mirrors internal/runtime.ValidVersionName's checks;
// duplicated here rather than imported — internal/workspace importing
// internal/runtime for a generic string check would be a backwards,
// purely coincidental dependency between two unrelated domains.
func ValidProjectName(name string) error

// New creates root/src/<name> by copying root/base/'s contents into
// it. Returns an error, without touching the filesystem, if name is
// invalid or root/src/<name> already exists.
func New(root, name string) error

// Scratch does the same as New, under root/scratch/<name>.
func Scratch(root, name string) error
```

`New`/`Scratch` share an unexported `createFromBase(root, kind, name
string) error`: validate name → error if `root/<kind>/<name>` already
exists → copy `root/base/` into a temp sibling directory → `os.Rename`
the temp directory into place. This is the same rename-aside-then-swap
atomicity already established in `internal/installer.ExtractAtomic` and
`internal/runtime/node.extractStrippingTopLevel` — a failure partway
through the copy leaves the temp directory orphaned (cleaned up via
`defer os.RemoveAll`) and the destination never created, rather than a
half-populated project directory.

A missing (or empty) `root/base/` is not an error: the new project
directory is simply created empty, since an unpopulated `base/` is a
valid starting state (per Non-goals above, 3a doesn't manage `base/`'s
contents).

Copying preserves hidden files, nested directories, and file
permissions. A symlink inside `base/` is dereferenced (its target's
content is copied as a regular file at the destination) rather than
recreated as a symlink — the brief doesn't call out symlink handling for
templates, and a hand-maintained template directory is not expected to
contain them; this is a low-stakes default that can change later if it
turns out to matter.

## `cmd/workspace.go` — commands and aliases

```text
dev workspace              (aliases: ws)
dev workspace new <name>
dev workspace scratch <name>   (alias: s, under either "workspace" or "ws")
```

All three share a `resolveWorkspaceRoot(cmd *cobra.Command) (string,
error)` helper:

1. `config.Load(config.DefaultPath())` — treat `config.ErrNotFound` as "no
   workspace path configured yet," any other error as real.
2. If `cfg.Workspace.Path == ""`: run the first-use prompt (see below),
   save the result via `config.Save`.
3. `workspace.EnsureScaffold(cfg.Workspace.Path)`.
4. Return `cfg.Workspace.Path`.

`workspaceCmd`'s own `RunE` is just `resolveWorkspaceRoot` plus a
`cliutil.Fsuccess(cmd.OutOrStdout(), "Workspace at %s", root)` — this is
the command the brief's idempotency example (§39) uses directly, so its
entire job is "ensure initialized, say where," nothing more.
`workspaceNewCmd`/`workspaceScratchCmd` call `resolveWorkspaceRoot` then
`workspace.New`/`workspace.Scratch`, reporting success the same way
`dev lang install` does.

### First-use prompt

A single Huh text input: title asking where dev should keep the
workspace, pre-filled with `platform.DefaultWorkspacePath()`, validated
non-empty. Wired to the command's own `cmd.InOrStdin()`/
`cmd.OutOrStdout()` in Huh's non-fullscreen ("accessible") mode, so it
degrades to plain sequential prompts under a piped/non-interactive stdin
— the same requirement `dev setup`'s `shell.Confirm` already satisfies,
just via a different library. The exact Huh API calls that achieve this
will be confirmed empirically at the start of implementation (Huh's
accessible-mode I/O injection is the mechanism this design assumes, but
its precise method names aren't being asserted as gospel here); if it
turns out Huh can't be driven this way cleanly, the fallback is a small
`PathPrompter` interface with a real-Huh production implementation and a
scriptable test double — implementation's call, not a blocker for this
spec.

## Error handling & idempotency

- A configured `Workspace.Path` means every future command skips the
  prompt entirely — the "ask once" contract.
- Running `dev workspace` (or `new`/`scratch`) again after everything
  already exists is a no-op scaffold-wise; `EnsureScaffold` only creates
  what's missing, never touches what's there.
- `new`/`scratch` on an existing project name is a plain error
  (`project %q already exists at %s`) with zero filesystem mutation —
  name validation and the existence check both happen before any
  directory is created, matching the codebase's established "fail before
  touching disk" pattern (`cmd/lang.go`'s `runtime.ValidVersionName`
  checks, `internal/installer`'s pre-swap checks).
- A config load failure that isn't `config.ErrNotFound` (e.g. corrupt
  JSON) is a real, surfaced error — never silently treated as "no path
  configured yet" and overwritten.
- A copy failure partway through leaves no trace at the destination path
  (see the atomicity note under `internal/workspace` above).

## Testing approach

- `internal/platform`: `DefaultWorkspacePath` — both the
  Documents-directory-exists and Documents-directory-missing branches,
  using `t.TempDir()` + `t.Setenv("HOME", ...)`, the same override
  pattern already used in `internal/shell`'s tests.
- `internal/workspace`: `EnsureScaffold` (creates all four, idempotent on
  a second call), `ValidProjectName` (empty/./../separator rejection),
  `New`/`Scratch` (copies hidden files and nested structure from a built
  `base/` fixture; errors without mutating anything when the destination
  already exists; succeeds with an empty result when `base/` is missing
  or empty; leaves no partial directory behind on a simulated failure
  mid-copy). Pure `t.TempDir()`-based, `t.Parallel()` where tests don't
  share state — same style as `internal/installer`'s and
  `internal/runtime/node`'s test suites.
- `cmd/workspace_test.go`: first-use flow (prompt fires, config gets
  written, scaffold gets created) driven the same way
  `cmd/setup_test.go` drives `dev setup`'s confirmation prompt — piped
  stdin via `rootCmd.SetIn`; second-run idempotency (no reprompt, no
  duplicate work, matching §39's own example); `new`/`scratch` success
  and already-exists paths; alias resolution (`dev ws`, `dev ws s
  <name>`).

## Acceptance criteria for this sub-project

```bash
dev workspace
dev workspace new api
dev workspace scratch experiment

dev ws
dev ws new api
dev ws s experiment
```

All idempotent on repeat invocation per the brief's §39. `clean`,
`archive`, `--dry-run`, and `metrics` remain unimplemented until 3b/3c.
