# PATH & Shims — Design Spec

Date: 2026-09-26
Status: Approved for planning
Sub-project: 2b of 4 (sub-project 2 of 4, split into 2a/2b/2c)

## Context

Sub-project 2a shipped the `Runtime` interface, a Node.js provider, and
`dev lang` commands for installing/activating versions. There is currently
no way to actually *run* an installed version — a user would have to know
`~/.dev/versions/node/22/bin/node` exists and add it to `PATH` by hand,
defeating the point of version management. This sub-project closes that
gap: `dev env` (print PATH instructions), `dev setup` (detect shell, show
the change, confirm, write it), and a shim mechanism so `~/.dev/bin` alone
on `PATH` is enough to run whatever version is currently active for any
managed language.

This spec covers only 2b. Sub-project 2c (more language providers) is
untouched; this spec's job is to make the shim/PATH mechanism itself work
correctly for the one provider that already exists (Node.js), in a way
that needs zero changes when 2c adds more.

## Goals

- `~/.dev/bin` on `PATH` is sufficient — no per-language `PATH` entries.
- Running `node`, `npm`, or `npx` after `dev lang use node <version>`
  transparently runs that exact version, with identical exit codes,
  signals, and stdio behavior to running the real binary directly.
- `dev setup` never modifies a shell rc file without showing the user
  exactly what will change and getting explicit confirmation first.
- `dev setup` is idempotent — running it twice never duplicates its block
  in an rc file.
- Adding a future provider (sub-project 2c) requires no changes to the
  shim dispatch mechanism, `dev env`, or `dev setup` — only the new
  provider's own `ShimNames()`/`BinaryPath()` implementation.

## Non-goals (deferred)

- Sub-project 2c's additional language providers.
- Automatic PowerShell profile editing — Windows users get the same
  printed instructions `dev env` produces; `dev setup` does not attempt to
  write a PowerShell profile file in this phase (see "Windows scope"
  below for why).
- Self-updating the `dev` binary, and any mechanism to detect/refresh
  stale shim copies after such an update — out of scope until self-update
  exists as a feature at all.
- `corepack` as a shimmed binary name (Node bundles it only from v16.9+;
  omitted for now to keep the shimmed-name set unambiguous across every
  supported major — can be added later without any structural change).

## `Runtime` interface additions

Two new methods, added to the interface sub-project 2a defined in
`internal/runtime/runtime.go`:

```go
type Runtime interface {
	// ... the 7 existing methods, unchanged ...

	// ShimNames returns the binary names this provider's installed
	// versions expose (e.g. ["node", "npm", "npx"] for Node.js). dev
	// setup creates a shim copy for each name returned by every
	// registered provider.
	ShimNames() []string

	// BinaryPath resolves the absolute path to binName inside an
	// installed version at versionDir (e.g. "<DEV_HOME>/versions/node/22").
	// Each provider owns its own on-disk layout knowledge here — Node's
	// Unix tarballs put binaries in bin/, but its Windows zip extracts
	// flat with .cmd wrappers at the version root, and this is exactly
	// the kind of platform-specific detail the shim dispatcher itself
	// must not need to know.
	BinaryPath(versionDir, binName string) (string, error)
}
```

**Ripple effect on already-completed sub-project 2a code:** every existing
`Runtime` implementation and test stub must gain these two methods to keep
compiling — `internal/runtime/node/node.go` (real implementation),
`internal/runtime/runtime_test.go`'s `stubRuntime`, and
`cmd/lang_test.go`'s `stubRuntime`. This is expected, mechanical, and must
be done as part of this sub-project's first task, not treated as scope
creep.

## `internal/providers` — the one place that lists providers

`cmd/lang.go`'s command wiring and the new shim dispatch mechanism both
need "every registered `Runtime`," and they must never drift out of sync
with each other. Rather than duplicate the registration call in both
places, a tiny new package centralizes it:

```go
// Package providers is the single place that lists every Runtime
// provider dev knows about, so command wiring (cmd/lang.go) and shim
// dispatch (internal/shim) can never register a different set.
package providers

import (
	"github.com/jpsdm/dev/internal/runtime"
	"github.com/jpsdm/dev/internal/runtime/node"
)

func Register(m *runtime.Manager) {
	m.Register(node.New())
}
```

`cmd/lang.go`'s existing `init()` changes from directly registering
`node.New()` to calling `providers.Register(langManager)` — a small,
behavior-preserving edit to already-shipped code (the resulting
`langManager` is identical either way; only where the registration list
lives changes).

## Node.js provider additions

```go
// ShimNames returns the Node.js binaries dev creates shims for.
func (n *Node) ShimNames() []string {
	return []string{"node", "npm", "npx"}
}

// BinaryPath resolves binName inside an installed Node.js version.
func (n *Node) BinaryPath(versionDir, binName string) (string, error) {
	osName, err := nodeOS()
	if err != nil {
		return "", err
	}

	var candidate string
	if osName == "win" {
		if binName == "node" {
			candidate = filepath.Join(versionDir, "node.exe")
		} else {
			candidate = filepath.Join(versionDir, binName+".cmd")
		}
	} else {
		candidate = filepath.Join(versionDir, "bin", binName)
	}

	if !filesystem.Exists(candidate) {
		return "", fmt.Errorf("%s not found in Node.js installation at %s", binName, versionDir)
	}
	return candidate, nil
}
```

## `internal/shim` — dispatch and process replacement

```go
package shim

// Run resolves binName (e.g. "node") against every provider registered
// via providers.Register, finds that provider's active version, resolves
// the real executable, and replaces the current process with it. It
// returns an error only when something prevents that replacement from
// happening at all — once the real process image is running, this
// process's own exit code/output no longer matter, because it no longer
// exists (Unix) or this call does not return (Windows, see below).
func Run(binName string, args []string) error
```

Internals:

1. Build a local `runtime.Manager` via `providers.Register` (mirrors
   `cmd/lang.go`'s own manager — see "Why two Managers" below) and search
   every registered provider's `ShimNames()` for `binName`.
2. No match → a plain error ("no active dev-managed runtime provides
   %q", binName).
3. Match found → call that provider's `CurrentVersion()`. `nil` → a plain
   error naming the provider and the `dev lang use` command to fix it
   (e.g. "Node.js is not active — run `dev lang use node <version>`
   first").
4. Build `versionDir` from `platform.VersionsDir()` + provider name +
   version name, call `BinaryPath(versionDir, binName)`.
5. Replace the process (platform-specific, below), passing `args` through
   unchanged.

**Why two `Manager`s (one in `cmd/lang.go`, one built fresh inside
`shim.Run`) instead of one shared instance:** `main.go` dispatches to
either `cmd.Execute()` (normal CLI invocation, argv[0] is `dev`) or
`shim.Run` (argv[0] is a shim name) — never both in the same process
run — so there's no shared-state benefit to threading one `Manager`
through both paths, and keeping `cmd` and `internal/shim` independent of
each other (both depending only on `internal/providers`) avoids a
needless coupling between the full CLI's command tree and the shim's
minimal dispatch path.

**Process replacement — platform-specific files, matching this project's
existing cross-platform pattern:**

`internal/shim/exec_unix.go` (`//go:build unix`):
```go
func replaceProcess(path string, args []string) error {
	argv := append([]string{path}, args...)
	return syscall.Exec(path, argv, os.Environ())
}
```
`syscall.Exec` replaces the current process image in place — the shim
process literally becomes the real binary. Signals, exit codes, and stdio
are identical to invoking the real binary directly, because after this
call succeeds there is no separate "shim process" anymore.

`internal/shim/exec_windows.go` (`//go:build windows`):
```go
func replaceProcess(path string, args []string) error {
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil // unreachable
}
```
Windows has no equivalent syscall that replaces a running process image,
so this runs the real binary as a child with inherited stdio and exits
with its exact exit code — externally indistinguishable from a real exec
for the purposes this CLI cares about (exit code and passthrough I/O).

## `main.go` — shim vs. normal CLI dispatch

```go
func main() {
	base := filepath.Base(os.Args[0])
	base = strings.TrimSuffix(base, ".exe")
	if base == "dev" {
		cmd.Execute()
		return
	}
	if err := shim.Run(base, os.Args[1:]); err != nil {
		cliutil.PrintError(err)
		os.Exit(1)
	}
}
```

## `internal/shell` — detection, export lines, idempotent rc editing

```go
type Shell int

const (
	Unknown Shell = iota
	Bash
	Zsh
	Fish
	PowerShell
)

// Detect identifies the current shell from $SHELL (Unix) or reports
// PowerShell on Windows (this project's only supported Windows shell
// target for now).
func Detect() Shell

// ExportLines returns the lines dev env prints / dev setup would insert,
// in sh's own syntax.
func ExportLines(sh Shell, devHome string) []string

// RCPath returns the rc file dev setup would edit for sh, and whether
// automatic editing is supported for it at all (false for PowerShell —
// see "Windows scope").
func RCPath(sh Shell) (path string, supported bool)

// UpsertBlock writes lines into path between a
// "# BEGIN dev shell setup" / "# END dev shell setup" marker pair,
// replacing an existing block if one is present (idempotent) or
// appending a new one if not. Creates path's parent directory and the
// file itself if neither exists yet.
func UpsertBlock(path string, lines []string) error

// Confirm prompts with prompt, reads one line from in, and reports
// whether it was an affirmative response ("y" or "yes", case
// insensitive) — anything else, including EOF or an empty line, is "no".
func Confirm(prompt string, in io.Reader, out io.Writer) (bool, error)
```

`ExportLines` produces (adapted to `sh`'s syntax):
```bash
export DEV_HOME="$HOME/.dev"
export PATH="$DEV_HOME/bin:$PATH"
```

## `cmd/env.go` — `dev env`

Detects the shell via `shell.Detect()`, prints `shell.ExportLines(...)` to
stdout, nothing else. Purely read-only; no `--dry-run` needed (there's
nothing to preview beyond what it already only prints).

## `cmd/setup.go` — `dev setup`

1. Detect shell.
2. If `shell.RCPath` reports `supported == false` (PowerShell): print the
   same lines `dev env` would, plus a one-line note that automatic Windows
   setup isn't supported yet and these lines should be added to the
   PowerShell profile manually. Return success (this is a complete,
   correct outcome for that platform in this phase, not a failure).
3. Otherwise: print the rc file path and the exact lines that would be
   inserted/replaced, then call `shell.Confirm("Add these lines to
   <path>? [y/N] ", os.Stdin, os.Stdout)`.
4. Declined → print "No changes made." and exit 0 (declining is not an
   error).
5. Confirmed → `shell.UpsertBlock(path, lines)`, then create/refresh shim
   copies: for every provider from `providers.Register`, for every name in
   `ShimNames()`, copy the currently-running `dev` binary (via
   `os.Executable()`) to `<DEV_HOME>/bin/<name>` (`.exe` suffix on
   Windows), overwriting any existing file at that path. Report success
   via `cliutil.Success` per shim created, then a final summary.

**Windows scope:** PowerShell profile paths vary by PowerShell version and
scope (current-user-current-host vs. all-hosts), and the authoritative
path is only knowable by asking PowerShell itself (`$PROFILE`) — guessing
a path and writing to it risks creating a profile PowerShell doesn't
actually load, which is worse than not touching it. Printing instructions
(exactly what `dev env` already does) is the correct, honest behavior for
this phase; automatic PowerShell profile editing is deferred, not silently
half-supported.

## Error handling & idempotency

- A shim run with no active version for its language fails with a
  specific, actionable message (which `dev lang use` command to run),
  never a generic "file not found."
- `dev setup` run twice with the same shell produces the same end state:
  the rc block is replaced in place (not duplicated), and shim copies are
  simply overwritten with identical bytes.
- Declining `dev setup`'s confirmation is a normal, successful exit (0),
  not an error — matching the project's convention that a user's "no"
  is not a failure.

## Testing approach

- `internal/shim`: tests use a stub `Runtime` (same pattern as
  `cmd/lang_test.go`) whose `BinaryPath` points at a small real executable
  built in the test (e.g. compiling a trivial Go program with `go build`
  into `t.TempDir()`, or reusing a guaranteed-present system binary like
  `true`/`cmd.exe /c exit 0` gated by `runtime.GOOS`) to verify argument
  passthrough and exit-code propagation without invoking real Node.js.
  `replaceProcess` itself (the `syscall.Exec`/`os/exec` platform files) is
  necessarily thin and mostly untested directly — covered by this
  integration-style test instead, run in a subprocess so a successful
  `syscall.Exec` (which replaces the *test* process on Unix, since `Run`
  doesn't fork) doesn't take down the test binary itself.
- `internal/shell`: `UpsertBlock` tested against real temp files
  (`t.TempDir()`) — insert into a nonexistent file, insert into a file
  with unrelated existing content, replace an existing marked block,
  confirm no duplication on a second call. `Confirm` tested with an
  injected `strings.Reader` for both "y"/"yes" and every other input,
  never real stdin.
- `cmd/env.go`/`cmd/setup.go`: tested the same way as `cmd/lang.go` —
  `rootCmd.Execute()` against captured stdout, with `shell.Detect()`'s
  result not mocked (tests run wherever they run and assert against
  whatever shell that environment actually reports, using `os.Getenv`
  overrides via `t.Setenv("SHELL", ...)` to pin a specific shell for
  deterministic assertions).
- No test writes to a real shell rc file (`~/.bashrc` etc.) — all
  `UpsertBlock` tests target `t.TempDir()` paths.

## Acceptance criteria for this sub-project

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run
  ./... --max-same-issues=0 --max-issues-per-linter=0`, `go test ./...`
  all pass; `make check` succeeds.
- `dev env` prints correct export lines for the detected shell.
- `dev setup` shows the exact change, respects a declined confirmation,
  and on acceptance both edits the rc file idempotently and creates shim
  copies for every registered provider's `ShimNames()`.
- Manual verification against the real filesystem (not part of the
  automated suite): after `dev setup`, adding `~/.dev/bin` to `PATH` and
  running `node --version`/`npm --version` actually runs the currently
  active Node.js version with correct output and exit code; switching the
  active version via `dev lang use node <other-version>` changes what the
  shim runs without recreating the shim itself.
