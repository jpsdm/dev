# Shell-Function PATH Management (Replace Shims) — Design Spec

Date: 2026-10-01
Status: Approved for planning

## Context

Sub-project 2b (`docs/superpowers/specs/2026-09-26-path-and-shims-design.md`) gave `dev` its
original shim mechanism, and `docs/superpowers/specs/2026-09-30-active-bin-dir-exposure-design.md`
(merged as PR #7, released as `v0.7.0`) extended it with per-language placeholder directories
and a directory-symlink/junction indirection. Both share the same root design: `dev` is copied
under well-known binary names (`node`, `python3`, ...), and those copies dispatch to the real
tool by inspecting `current/<lang>` and re-executing — because `dev` is an external binary, not
a shell function, and so cannot alter the PATH of the shell that invoked `dev lang use`.

After using `v0.7.0` in practice, the user rejected this whole approach, citing `nvm` and `mise`
as the model to follow instead: no copied binaries, no symlink/junction indirection, no
placeholder directories — `PATH` should reference `versions/<lang>/<version>` and
`current/<lang>` directly, with nothing duplicated. Discussion converged on the actual
mechanism that makes this possible without either per-directory version files or a
per-shell-prompt hook (both explicitly ruled out as overkill — see Non-goals): `dev` is
installed as a **shell function** that wraps the real binary. The function only acts after
`dev lang use`/`dev lang uninstall`, when it re-evaluates `dev env` to recompute `PATH` in the
current shell — a model closer to `nvm`'s own actual implementation (a shell function, not a
per-prompt hook) than to `mise activate`'s per-directory-aware hook, which requires overhead
this project's scope doesn't call for.

This spec **supersedes both prior PATH/shim specs** and the released `v0.7.0` behavior. It
removes the entire shim mechanism (both the original and the `v0.7.0` placeholder/symlink
layer) rather than extending it further.

## Goals

- `node`, `python3`, etc. resolve on `PATH` to the *real* binary inside
  `versions/<lang>/<version>/...` — never a copy of `dev`, a symlink, or a junction.
- Any tool installed later by a language's own package manager (`npm install -g pnpm`, a
  `pip`-installed console-script) is reachable the moment it's installed, with zero `dev`-side
  configuration — this was the actual bug `v0.7.0` was built to fix, and this design fixes it
  more simply.
- `dev lang use`/`dev lang uninstall` take effect in the **current shell**, immediately, with
  no "restart your shell" step.
- Adding a future language provider requires zero changes to the shell function itself, `dev
  setup`, or any installed shell configuration — only that provider's own `BinDir()`
  implementation (already built, unchanged by this spec). This closes the root cause of the
  `dev update` shim-staleness bug from earlier in this project's history permanently: there are
  no shim files left to go stale.
- Works on Bash, Zsh, Fish, and PowerShell — the same four shells `dev setup` already detects
  and configures today.

## Non-goals (deferred)

- **Per-directory/per-project version files** (`.nvmrc`, `.tool-versions`-equivalent). Explicitly
  discussed and ruled out: this project's model stays "one global active version per language,
  changed explicitly via `dev lang use`" — unchanged from every prior design. Revisit only if a
  real request for project-local pinning surfaces.
- **A per-shell-prompt hook** (`mise activate`'s model: `PATH` recomputed before every prompt,
  needed to detect an automatic directory-triggered version switch). Not needed without
  per-directory files, and would add constant per-prompt overhead this design has no reason to
  pay.
- **Any friendly "`X` is not active" message.** Without a binary or shim standing in for an
  inactive language's commands, there is nothing left to intercept the invocation and print a
  message — running `node` with no active Node version is ordinary shell "command not found",
  the same as `nvm`/`mise` without shims. Discussed and explicitly accepted as the trade-off for
  removing the mechanism that made the message possible.
- **Automatic migration or cleanup of a `v0.7.0` (or earlier) install's on-disk leftovers**
  (`$DEV_HOME/bin`, `$DEV_HOME/active/*`, `$DEV_HOME/no-active/*`). These become inert the moment
  the user re-runs `dev setup` under this design (nothing references them any longer); not
  automatically deleted, for the same reasoning prior specs already gave for not cleaning up
  stale shim artifacts: no correctness benefit, pure optional tidiness. Mentioned to the user in
  the migration note below instead.
- **Rewriting `internal/runtime/*` beyond removing `ShimNames()`.** `BinDir()` (already built)
  is reused as-is; every other provider method is untouched.

## What's removed

| Component | Why |
|---|---|
| `internal/shim` (whole package) | Its entire job — resolve `current/<lang>`, re-exec or print a message — is now done by the shell itself finding the real binary on `PATH`. Nothing invokes `dev` under a language's binary name anymore. |
| `internal/activebin` (whole package, just built in `v0.7.0`) | The symlink/junction indirection it provided is exactly what this design replaces with direct `PATH` references computed by `dev env`. |
| `Runtime.ShimNames() []string` (interface method + all 4 providers' implementations) | No caller remains — nothing creates shims, placeholders, or copies of any binary name anymore. |
| `$DEV_HOME/bin`, `$DEV_HOME/active/<lang>`, `$DEV_HOME/no-active/<lang>/...` | No longer created, no longer referenced by anything `dev` writes going forward. |
| `cmd/setup.go`'s `installShims`, the hidden `__refresh-shims` command (`refreshShimsCmd`/`refreshShimsCommandName`), and `cmd/update.go`'s `runShimRefresh`/`defaultRunShimRefresh` machinery | Built specifically to keep shim copies in sync with `dev`'s own binary version — with no shim copies left to go stale, the entire problem class (and this project's whole prior bugfix for it) disappears. |
| `platform.dirMatchesDevHome`'s `$DEV_HOME/bin` / `no-active` branches | `main.go`'s shim-dispatch path is gone, so `RunningFromDevHome` only ever needs to recognize `$DEV_HOME` itself (where the real `dev` binary lives) — the function keeps its symlink-resolution robustness, just loses the shim-specific branches. |
| `main.go`'s `shimBinaryName`/`decideDispatch`/`dispatchAsShim` dispatch branch | Same reason — `dev` is only ever invoked as `dev`, never under another name. |
| `dev update`'s "remind the user to re-run `dev setup`" message (`cmd/update.go`, added in `v0.7.0`) | Was specifically about picking up new `active/<lang>` `PATH` entries from a provider-list change the shell function never needs to know about — moot under this design. Replaced by a narrower migration note (see below) that only matters once, for pre-this-design installs. |

## What's kept, what's new

| Piece | Before (`v0.7.0`) | Now |
|---|---|---|
| `Runtime.BinDir(versionDir) (string, error)` | Used to compute a directory to symlink/junction | **Unchanged.** Now the sole input `dev env` uses to compute `PATH` directly — no indirection layer between this value and what ends up on `PATH`. |
| `current/<lang>` marker file | Read by each provider's `Activate`/`Uninstall`/`CurrentVersion` | **Unchanged.** Still the single source of truth for "which version is active" — `dev env` reads it the same way `CurrentVersion()` already does. |
| `versions/<lang>/<version>/` | Where installs land | **Unchanged.** |
| `Activate`/`Uninstall` (per provider) | Wrote the marker, then called `activebin.Repoint` | **`activebin.Repoint` calls removed** — these methods go back to doing exactly what they did before `v0.7.0` (write/clear the marker, nothing else). `PATH` is no longer this method's concern at all; it's entirely `dev env`'s job, computed fresh whenever asked. |
| `dev env` | Printed two static lines: `export DEV_HOME=...` / `export PATH="$DEV_HOME:$DEV_HOME/bin:...:$PATH"` | **Becomes dynamic** (see below) — computes `PATH` from the current environment plus every provider's current active version, every time it's invoked. |
| `dev setup` | Wrote the two static lines (via `shell.ExportLines`) into the rc file / env | **Writes a shell function** instead (see below) — the function still ultimately calls the same binary via `command dev`/an absolute path, and the static part of `PATH` (`$DEV_HOME` itself, so `dev` can be found at all) is set once, same as before. |

## `dev env`'s new algorithm

```
devHome := platform.DevHome()
currentPath := os.Getenv("PATH")                     // inherited from the invoking shell
segments := split(currentPath, OS path separator)
kept := [s for s in segments if NOT s starts-with (devHome + "/versions/")]
                                                        // strips any prior dev-managed
                                                        // version directory, regardless of
                                                        // which language/version it was —
                                                        // the same technique nvm.sh's own
                                                        // nvm_strip_path uses, just computed
                                                        // in Go instead of shell string
                                                        // manipulation
active := []
for each registered provider (langManager.Names(), alphabetical, deterministic):
    v := provider.CurrentVersion()
    if v != nil:
        dir, err := provider.BinDir(filepath.Join(devHome, "versions", provider.Name(), v.Name))
        if err == nil:
            active = append(active, dir)
newPath := join([devHome] + active + kept, OS path separator)
print the shell-appropriate "export PATH=..." (and "export DEV_HOME=...") lines for newPath
```

Idempotent by construction: running it twice in a row produces the same `PATH`, since the
strip step removes exactly what the previous run added (and nothing else — the generic
`devHome + "/versions/"` prefix check cannot mistake an unrelated directory for a dev-managed
one, since that's the one path shape only `dev`'s own installs ever use). A provider with no
active version contributes nothing, matching today's behavior where an unactivated language is
simply absent from `PATH`.

A `BinDir` error (same unsupported-OS/arch shape `BinaryPath` already has) is skipped rather
than failing the whole command — one language's provider misbehaving must never break `dev
env` for every other language, since this command's job is now load-bearing for every shell
prompt that follows a `dev lang use` call.

## The shell function `dev setup` installs

Same per-shell branching `internal/shell` already has (`Bash`/`Zsh` share POSIX syntax, `Fish`,
`PowerShell`, `Unknown` falls back to POSIX with the existing "couldn't detect your shell"
warning comment) — only the *content* written changes, from two export lines to a function
definition. The exact shape, POSIX form:

```sh
dev() {
    command dev "$@"
    local status=$?
    case "$1" in
        lang|l) eval "$(command dev env)" ;;
    esac
    return $status
}
```

Checking only `$1` (not the specific subcommand `use`/`uninstall`) is deliberate: `dev lang
install`/`list`/`current`/`installed` gain a harmless, cheap extra `dev env` call (pure local
computation — reads `current/<lang>` marker files and env vars, no network, no spinner), in
exchange for never needing this function to enumerate exact subcommand names or track new
aliases if `cmd/lang.go` ever adds one. `l` is `lang`'s existing alias (see `cmd/lang.go`); the
function's `case` pattern covers both without needing cobra's own alias-resolution logic
duplicated in shell.

Fish and PowerShell get the equivalent construct in their own syntax (a `function`/`function`
block with the same "run the real command, then conditionally re-eval `dev env`'s output"
shape) — exact syntax is a planning-time detail per shell, not a design fork.

`$DEV_HOME` itself still needs a one-time, static `PATH` entry (unchanged from every prior
design) so `command dev` can find the real binary at all — this is the one part of `dev
setup`'s old two-line `export` block that survives unchanged in spirit, just now living
alongside the function definition in the same rc-file block `shell.UpsertBlock` already
manages idempotently.

## Error handling

- `dev env` run completely outside a shell context that then evals its output (e.g. piped to a
  file, or run under a shell this project doesn't detect) is harmless — it just prints to
  stdout; nothing executes until something `eval`s it, same as today's `dev env`.
- If `command dev "$@"` (the real subcommand) fails, the function still returns that exit code
  (captured before the conditional `dev env` re-eval, restored via `return $status` after) —
  `dev lang use nonexistent-version`'s failure must not be masked by a trailing `dev env` call
  that then "succeeds" and returns 0.
- A shell that was never upgraded past a `v0.7.0`-or-earlier install (static `PATH` lines, no
  function) keeps working exactly as it does today until the user re-runs `dev setup` — nothing
  in this design actively breaks an un-migrated shell, it simply doesn't gain the new behavior
  until they do. No forced migration path; no non-goal item needed beyond what's already listed.

## Testing approach

Real shell behavior where feasible, matching this project's established convention — no mocks:

- `dev env`'s `PATH`-computation logic: real temp `$DEV_HOME`, real `versions/<lang>/<version>`
  directories, real `t.Setenv("PATH", ...)` fixtures (including a fixture that already contains
  a stale `versions/<lang>/<old-version>` entry, asserting it's stripped), asserting the exact
  printed line per shell syntax — mirrors `internal/shell/shell_test.go`'s existing
  `ExportLines` test style directly.
- An idempotency test: call the computation twice with the first call's own output as the
  second call's input `PATH`, assert identical results.
- A real end-to-end test: build the branch's own `dev` binary (this project's established
  `go build`-into-a-temp-dir pattern, already used in `internal/shim/shim_test.go`'s history and
  `main_test.go`), write a real shell script implementing the POSIX function above, source it in
  a real `sh -c` subprocess, run `dev lang use`, and assert the *subprocess's own* `PATH`
  env var (queried via a trailing `echo $PATH` in the same script) contains the right directory
  — this is the test class the `v0.7.0` final review identified as missing before (an
  end-to-end dispatch check, not just a unit test of the pure computation), applied here from
  the start rather than discovered after the fact.
- PowerShell's function syntax cannot be executed in this project's Linux development
  environment, the same limitation `internal/activebin`'s Windows half already had — write real,
  syntactically-checked PowerShell function code and state plainly that it is unverified on a
  real Windows host, rather than skip it or overclaim coverage.

## Migration note (for the README / release notes, not a design decision)

Existing users on `v0.7.0` or earlier need to re-run `dev setup` once to replace their rc
file's old static `export` lines with the new function. Until they do, `dev` keeps working
exactly as it does today (old shim/symlink mechanism, unchanged, still installed) — nothing in
this design removes or breaks an existing un-migrated install. The exact wording is a
documentation detail for the implementation plan, not a design fork.

## Acceptance criteria

```bash
dev setup                        # installs the new shell function
source ~/.bashrc                 # or equivalent
dev lang install node 22
dev lang use node 22              # takes effect in THIS shell, no restart
which node                        # resolves inside versions/node/22/..., not a shim/symlink
npm install -g pnpm
pnpm --version                    # works immediately
dev lang uninstall node 22
which node                        # not found (plain shell behavior, no dev involved)
```
