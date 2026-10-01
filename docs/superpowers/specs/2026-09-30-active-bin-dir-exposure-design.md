# Expose Installed Tools Directly (Active Bin Dir) — Design Spec

Date: 2026-09-30
Status: Approved for planning

## Context

Sub-project 2b (`docs/superpowers/specs/2026-09-26-path-and-shims-design.md`) gave `dev`
its shim mechanism: `$DEV_HOME/bin` on `PATH`, populated with one copy of the `dev` binary
per name in each provider's `ShimNames()` (`node`, `npm`, `npx`, `java`, `javac`, `go`,
`gofmt`, `python3`, `python`, `pip3`, `pip`). Each copy inspects its own invoked name,
resolves the active version via `current/<lang>`, and either execs the real binary
(`internal/runtime.Runtime.BinaryPath`) or prints a friendly "not active" message.

A real user hit the gap that design's own non-goals section anticipated but didn't yet
need to solve: **`npm install -g pnpm`, then running `pnpm`, fails** — npm installs it into
the active Node version's own `bin/` directory (the exact directory Node's and npm's shims
already dispatch into), but `pnpm` was never one of the fixed names `dev` created a shim
for, so nothing on `PATH` ever points at it. The same gap applies to a `pip install`'d
console script, a `go install`'d binary, or anything else a language's own package manager
adds after that language was installed — `ShimNames()`'s fixed whitelist can only ever cover
names `dev` already knows about at compile time.

This spec's fix: for each provider, expose the *directory* the active version's binaries
actually live in on `PATH` — not just a fixed list of copied names. Since `dev` is an
external binary (not a shell function like `nvm`), it cannot rewrite a running shell's
`PATH` when `dev lang use` runs; the design below use a stable, always-on-`PATH` directory
per language whose *target* `dev lang use` repoints — the same class of solution `pyenv`
converged on for the same architectural reason, adapted to use a directory-level
indirection instead of pyenv's file-level rehash, per the approach chosen in discussion
with the user.

This is additive to, not a wholesale deletion of, sub-project 2b's existing shim mechanism
— see "What stays, what's new" below for exactly what changes.

## Goals

- Any binary installed by a language's own package manager into the active version's bin
  directory — `pnpm`, a `pip`-installed console script, a `go install`-built binary — works
  immediately on `PATH`, with zero `dev`-specific configuration and no extra command to run.
- The existing friendly "`X` is not active — run `dev lang use X <version>` first" message is
  preserved for the well-known launcher names, for a language that has never had a version
  activated.
- Adding a future provider requires no changes to `dev env`, `dev setup`, or `dev lang`'s
  activation logic — only that provider's own new `BinDir()` implementation, matching sub-
  project 2b's original promise for `ShimNames()`/`BinaryPath()`.
- Works identically on Linux, macOS, and Windows, with no elevated-privilege requirement.

## Non-goals (deferred)

- **Removing `internal/shim`'s resolve-and-exec dispatch, or `ShimNames()`.** Both stay,
  repurposed (see below) rather than deleted — a full replacement would also mean designing
  a new "which names does this language claim, before any version is ever active" concept
  from scratch, when one already exists and already works correctly.
- **Automatically cleaning up a pre-upgrade `$DEV_HOME/bin`'s old shim copies.** They remain
  fully correct (they still resolve the active version exactly as before) and harmless —
  merely redundant with the new mechanism for the fixed names they already cover, and
  invisible to it for anything new like `pnpm`. Left in place; safe to delete by hand.
  Automatic cleanup would require `dev` to remember a hardcoded historical name list purely
  for teardown, for no correctness benefit.
- **musl/Alpine, exotic shells, or any platform not already supported** — unchanged scope
  from every prior sub-project.
- **Rewriting `internal/runtime/*` beyond adding `BinDir`** — each provider's existing
  `BinaryPath`/`ShimNames`/install logic is untouched.

## What stays, what's new

| Piece | Before | After |
|---|---|---|
| `Runtime.ShimNames() []string` | Every copied-shim name | **Unchanged signature**, narrower meaning: the names a language's *placeholder* (no version ever activated) responds to. Still exactly `node`/`npm`/`npx`, `java`/`javac`, `go`/`gofmt`, `python3`/`python`/`pip3`/`pip`. |
| `Runtime.BinaryPath(versionDir, binName)` | Used by every shim copy to resolve the real binary | **Unchanged** — still used, only now exclusively by placeholder copies (see below), since an active version's real binaries are reached directly via `PATH`, not through `dev` at all. |
| `Runtime.BinDir(versionDir) (string, error)` | — | **New.** The one directory whose contents should be exposed on `PATH` for an active version — e.g. `versionDir/bin` for Node/Java/Go/Python-Unix, `versionDir` itself for Python-Windows (its binaries sit at the version root, not under `bin/`). |
| `$DEV_HOME/bin/<name>` (copy of `dev`, one per `ShimNames()` entry, every provider mixed together in one flat directory) | Created by `dev setup`'s `installShims` | **Replaced** by `$DEV_HOME/no-active/<lang>/<name>` — the exact same copies, same content, same `internal/shim` dispatch logic, just organized one directory per language instead of flattened together. This is what a language's `PATH` entry points at before any version of it is ever activated. |
| `$DEV_HOME/active/<lang>` | — | **New.** A directory symlink (Unix) / junction (Windows) `dev setup` creates pointing at `$DEV_HOME/no-active/<lang>` initially, and `dev lang use`/`dev lang uninstall` repoint to the active version's `BinDir()` (or back to the placeholder, on uninstall of the active version). This is what actually goes on `PATH` — always present, one entry per registered provider, its *target* is what changes. |
| `internal/shim` (`resolve`, `findProvider`, `Run`, `replaceProcess`) | Runs for every shimmed invocation | **Unchanged code**, narrower in practice: only ever reached by a placeholder copy now (a real active version's binaries are reached by the OS's own `PATH` resolution before `dev` is ever invoked) — but kept exactly as-is as the same defensive, already-tested mechanism, not rewritten to assume that. |
| `cmd/setup.go`'s `installShims` | Copies `dev` to `$DEV_HOME/bin/<name>` for every provider's every `ShimNames()` entry | Renamed in spirit to "ensure placeholders": copies `dev` to `$DEV_HOME/no-active/<lang>/<name>` instead, then ensures `$DEV_HOME/active/<lang>` exists (creating it pointed at the placeholder only if it doesn't already exist — never overwriting an existing pointer, so a version that's already active stays active across a re-run). |
| `cmd/update.go`'s hidden `__refresh-shims` (2026-09-30 fix) | Refreshes `$DEV_HOME/bin` via the newly-installed binary's own provider list | **Reused as-is** for the same reason it exists today: placeholder copies are themselves copies of `dev`, so they go stale exactly like the old flat shims did after `dev update` swaps the binary — same root cause, same fix, now pointed at `no-active/<lang>/` instead of `$DEV_HOME/bin/`. |

## `dev lang use <lang> <version>`

After writing `current/<lang>` (unchanged), additionally repoints `$DEV_HOME/active/<lang>`
to `BinDir(versionDir)` for the version just activated:

- **Unix:** remove-then-recreate the symlink (`os.Symlink` to a temp name, then
  `os.Rename` over the real path) — the same rename-for-atomicity pattern
  `internal/installer`'s extraction swap and `cmd/setup.go`'s relocation already use in this
  codebase, so a process reading `$DEV_HOME/active/<lang>` mid-repoint never sees a
  half-updated link.
- **Windows:** a junction (`mklink /J`), not a symlink — junctions need no elevated
  privileges or Developer Mode, only that both paths are on the same volume (already true,
  since everything lives under `$DEV_HOME`). Repointing means removing the existing junction
  and creating a new one; Windows has no atomic junction-replace primitive, so this is a
  best-effort remove-then-create, matching the honesty this codebase already applies to
  Windows's other own-executable-in-use constraints (`cmd/setup.go`'s
  `installRelocation`, `internal/update`'s rename-aside).

## `dev lang uninstall <lang> <version>`

Unchanged except: if the version being removed was the active one, repoint
`$DEV_HOME/active/<lang>` back to `$DEV_HOME/no-active/<lang>` (the placeholder) in the same
step that already clears the `current/<lang>` marker — a dangling active-directory pointer
would be a strictly worse failure mode (an OS-level "no such file" on `PATH` lookup, or a
Windows junction pointing at a directory that no longer exists) than the deliberate,
friendly placeholder.

## `dev setup`

For every registered provider: ensure `$DEV_HOME/no-active/<lang>/<name>` exists (a copy of
the running `dev` binary) for each of that provider's `ShimNames()` — the same
`copyExecutable` helper already used today, just writing into the per-language placeholder
directory instead of flat `$DEV_HOME/bin`. Then ensure `$DEV_HOME/active/<lang>` exists,
creating it pointed at the placeholder **only if it doesn't already exist** (so re-running
`dev setup` after a version is already active never resets that pointer back to the
placeholder — matching sub-project 2b's existing idempotency requirement for the rest of
setup). Finally, the `PATH` lines `dev env`/`dev setup` print/write gain one entry per
registered provider: `$DEV_HOME/active/<lang>` (after `$DEV_HOME` and `$DEV_HOME/bin`, so
existing behavior for the still-supported flat names is unaffected by entry order).

## Migration for existing installs

An existing user's `dev update` alone does **not** pick up the new `PATH` shape — only
`dev setup` writes `PATH`/rc-file changes. `dev update`'s final success message should say
to re-run `dev setup` to gain this capability (one line; exact wording is an implementation
detail, not a design decision). Until they do, `dev` continues working exactly as before —
nothing about this change breaks an un-migrated install, it simply doesn't yet have the new
`active/<lang>` `PATH` entries, so a name like `pnpm` still isn't found (the same as today).

## Error handling

- `BinDir` returning an error (an unsupported OS/arch, mirroring `BinaryPath`'s existing
  contract) propagates from `dev lang use` exactly as a `BinaryPath` error already does
  elsewhere — no new error class.
- A version directory that's been manually deleted out from under an already-active
  `$DEV_HOME/active/<lang>` junction/symlink is not this spec's concern to detect
  proactively — the OS's own "no such file" surfaces naturally on the next invocation,
  identical in spirit to `internal/shim`'s existing handling of a stale `current/<lang>`
  marker pointing at a removed version.

## Testing approach

Real filesystem operations throughout, no mocks — matching this project's established
convention:

- `BinDir` per provider: table-driven, asserting the exact returned path per OS (mirroring
  each provider's existing `BinaryPath`-layout tests, especially Python's Windows
  root-vs-`bin/` split and Java's macOS `Contents/Home` nesting).
- `dev lang use`: real temp `$DEV_HOME`, real version directories, asserting the real
  symlink (`os.Readlink`) or junction target after activation, and after a second `use`
  correctly repoints rather than nesting/erroring.
- `dev lang uninstall` of the active version: asserts the pointer reverts to the real
  placeholder path.
- `dev setup`: asserts placeholder files exist and are executable, the initial pointer
  target, and idempotency (a second run doesn't reset an already-active pointer).
- An end-to-end test: real temp `$DEV_HOME`, activate a fake version whose `BinDir` contains
  a fake `pnpm`-shaped executable, assert it's resolvable by constructing the real `PATH`
  string `dev env` would print and checking the file exists at the expected resolved
  location — proving the exposed-directory mechanism actually works, not just that the
  right path string was computed.

## Acceptance criteria

```bash
dev setup                      # writes the new active/<lang> PATH entries
dev lang install node 22
dev lang use node 22
npm install -g pnpm
pnpm --version                 # works — pnpm was never a known ShimNames() entry
dev lang uninstall node 22
node --version                 # friendly "not active" message, not "command not found"
```
