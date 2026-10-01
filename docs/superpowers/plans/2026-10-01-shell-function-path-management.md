# Shell-Function PATH Management (Replace Shims) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `dev`'s entire shim/symlink/placeholder-directory mechanism with a shell function (`dev`) that re-evaluates a dynamically-computed `dev env` after `dev lang`/`dev l` commands, so `PATH` references `versions/<lang>/<version>/...` directly — no copied binaries, no symlinks/junctions, no placeholder directories.

**Architecture:** `dev env` becomes a pure, dynamic PATH computation: strip any stale `$DEV_HOME/versions/...` entry from the inherited `PATH`, then prepend `$DEV_HOME` plus each registered provider's current-active-version `BinDir()`. `dev setup` installs a shell function (not static export lines) that runs the real subcommand first, then — only for `dev lang`/`dev l` invocations — `eval`s a fresh `dev env` in the current shell. Everything built for the superseded shim/placeholder designs (`internal/shim`, `internal/activebin`, `ShimNames()`, `$DEV_HOME/bin`/`active`/`no-active`) is removed outright, not deprecated.

**Tech Stack:** Go (stdlib only — no new dependencies), Cobra, POSIX sh/Bash/Zsh/Fish/PowerShell shell syntax generation.

**Spec:** `docs/superpowers/specs/2026-10-01-shell-function-path-management-design.md`

## Global Constraints

- No shim binary copies, no symlinks/junctions, no placeholder directories — the whole class of solution the user explicitly rejected must not reappear in any form.
- No per-directory/per-project version files (`.nvmrc`-equivalent) — out of scope, explicitly deferred in the spec.
- No per-shell-prompt hook — the function only re-evaluates `dev env` when invoked as `dev lang ...`/`dev l ...`, never on every prompt.
- The friendly "`X` is not active" message is gone — an inactive language's command is ordinary shell "command not found". This is an accepted, deliberate trade-off, not a regression to fix.
- `Runtime.BinDir(versionDir) (string, error)` is unchanged and becomes the sole input to PATH computation. Do not modify any provider's `BinDir` implementation.
- `current/<lang>` marker file and `versions/<lang>/<version>/` layout are unchanged.
- No automatic migration/cleanup of a pre-this-design install's `$DEV_HOME/bin`/`active/*`/`no-active/*` leftovers — mention it in docs only (see Task 6). An un-migrated shell must keep working exactly as it does today until the user re-runs `dev setup`.
- Stdlib first; this project adds a third-party dependency only when it earns its place (CLAUDE.md) — this plan adds none.
- `make check` (fmt + vet + lint + test + build) must pass before every commit.
- TDD is required: write the failing test first for every new behavior.

## Review Focus

- **Empty `PATH` environment variable.** `strings.Split("", sep)` yields `[""]`, not `[]` — if not filtered, `dev env` would print a trailing/leading empty `PATH` segment, which POSIX shells treat as "current directory", a real (if narrow) security footgun this design must not introduce. Covered in Task 1's `ComputePathEntries` tests.
- **Idempotency.** Running `dev env`'s computation twice in a row (second run's input `PATH` = first run's output) must produce byte-identical output — this is the property the whole "strip `versions/` prefix, then re-add" design depends on for correctness across repeated `dev lang use` calls in the same shell. Covered in Task 1.
- **A provider with no active version, or whose `BinDir` errors.** Must be silently skipped, not fail the whole `dev env` command — one misbehaving provider must never block every other language's PATH entry. Covered in Task 2.
- **`dev lang use` failing (bad version name, not installed).** The shell function must still `return $status` reflecting the real subcommand's exit code — a trailing `dev env` re-eval must never mask a non-zero exit. Covered in Task 5's end-to-end test.
- **A pre-this-design shell (old static export lines, no function).** Must keep working exactly as before until the user re-runs `dev setup` — nothing in this plan may actively break an un-migrated rc file. No code change enforces this (the old lines are simply never touched unless the user re-runs `dev setup`, which replaces the whole marked block atomically via `shell.UpsertBlock`); called out explicitly in Task 6's migration note so it's not lost.

---

## Task 1: `internal/shell` — dynamic PATH computation and the shell-function template

**Files:**
- Modify: `internal/shell/shell.go`
- Modify: `internal/shell/shell_test.go`

**Interfaces:**
- Consumes: nothing new (stdlib `path/filepath`, `strings` only).
- Produces:
  - `shell.ComputePathEntries(current []string, devHome string, activeDirs []string) []string` — pure PATH computation.
  - `shell.ExportLines(sh Shell, devHome string, pathEntries []string) []string` — **signature change** from the old `(sh Shell, devHome string, langs []string)`. Task 2 updates both call sites.
  - `shell.FunctionLines(sh Shell, devHome string) []string` — new; returns the full rc-file block `dev setup` installs (static `DEV_HOME`/`PATH` entry + the `dev` wrapper function). Task 2 is its only caller.

- [ ] **Step 1: Write the failing tests for `ComputePathEntries`**

Add to `internal/shell/shell_test.go`:

```go
func TestComputePathEntries_PrependsDevHomeAndActiveDirs(t *testing.T) {
	current := []string{"/usr/bin", "/bin"}
	got := ComputePathEntries(current, "/home/u/.dev", []string{"/home/u/.dev/versions/node/22/bin"})
	want := []string{"/home/u/.dev", "/home/u/.dev/versions/node/22/bin", "/usr/bin", "/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputePathEntries_StripsStaleVersionsEntries(t *testing.T) {
	current := []string{
		"/home/u/.dev/versions/node/20/bin", // stale: a prior active Node version
		"/usr/bin",
	}
	got := ComputePathEntries(current, "/home/u/.dev", []string{"/home/u/.dev/versions/node/22/bin"})
	want := []string{"/home/u/.dev", "/home/u/.dev/versions/node/22/bin", "/usr/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputePathEntries_StripsDevHomeItself(t *testing.T) {
	// devHome is always re-prepended fresh; a stale literal devHome entry
	// already present in current must not be duplicated.
	current := []string{"/home/u/.dev", "/usr/bin"}
	got := ComputePathEntries(current, "/home/u/.dev", nil)
	want := []string{"/home/u/.dev", "/usr/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputePathEntries_SkipsEmptyEntries(t *testing.T) {
	// strings.Split("", sep) yields [""], not []; an empty PATH segment
	// means "current directory" to a POSIX shell and must never be
	// introduced by this computation.
	got := ComputePathEntries([]string{""}, "/home/u/.dev", nil)
	want := []string{"/home/u/.dev"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputePathEntries_NoActiveProvidersContributesNothing(t *testing.T) {
	got := ComputePathEntries([]string{"/usr/bin"}, "/home/u/.dev", nil)
	want := []string{"/home/u/.dev", "/usr/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputePathEntries_IdempotentAcrossRepeatedCalls(t *testing.T) {
	devHome := "/home/u/.dev"
	active := []string{"/home/u/.dev/versions/node/22/bin"}
	first := ComputePathEntries([]string{"/usr/bin", "/bin"}, devHome, active)
	second := ComputePathEntries(first, devHome, active)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("not idempotent: first=%v second=%v", first, second)
	}
}
```

Add `"reflect"` to the test file's import block if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/shell/... -run TestComputePathEntries -v`
Expected: FAIL with `undefined: ComputePathEntries`

- [ ] **Step 3: Implement `ComputePathEntries`**

Add to `internal/shell/shell.go` (after the `Detect`/parent-shell-detection block, before `ExportLines`):

```go
// ComputePathEntries returns the PATH entries dev env should print:
// devHome itself, then each of activeDirs (in order), then every entry
// from current that is not devHome itself, not a stale dev-managed
// version directory (anything under devHome/versions — a prior run's
// own active dirs, or any other installed version), and not empty
// (an empty PATH segment means "current directory" to a POSIX shell,
// and strings.Split of an empty PATH env var yields one such entry).
//
// Pure and idempotent: feeding a prior call's own result back in as
// current, with the same devHome and activeDirs, returns the same
// result — the strip step removes exactly what the prepend step adds
// back, nothing else, since devHome+"/versions/" is a path shape only
// dev's own installs ever use.
func ComputePathEntries(current []string, devHome string, activeDirs []string) []string {
	versionsPrefix := filepath.Join(devHome, "versions") + string(filepath.Separator)
	kept := make([]string, 0, len(current))
	for _, entry := range current {
		if entry == "" || entry == devHome || strings.HasPrefix(entry, versionsPrefix) {
			continue
		}
		kept = append(kept, entry)
	}
	result := make([]string, 0, 1+len(activeDirs)+len(kept))
	result = append(result, devHome)
	result = append(result, activeDirs...)
	result = append(result, kept...)
	return result
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/shell/... -run TestComputePathEntries -v`
Expected: PASS (all 6 subtests)

- [ ] **Step 5: Commit**

```bash
git add internal/shell/shell.go internal/shell/shell_test.go
git commit -m "feat(shell): add ComputePathEntries, the pure PATH-recomputation core"
```

- [ ] **Step 6: Write the failing tests for the rewritten `ExportLines`**

Replace every existing `TestExportLines*` test in `internal/shell/shell_test.go` (they currently call `ExportLines(sh, devHome, langs []string)` — the old signature) with:

```go
func TestExportLines_Posix(t *testing.T) {
	got := ExportLines(Bash, "/home/u/.dev", []string{"/home/u/.dev", "/home/u/.dev/versions/node/22/bin", "/usr/bin"})
	want := []string{
		`export DEV_HOME='/home/u/.dev'`,
		`export PATH="/home/u/.dev:/home/u/.dev/versions/node/22/bin:/usr/bin"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExportLines_Fish(t *testing.T) {
	got := ExportLines(Fish, "/home/u/.dev", []string{"/home/u/.dev", "/usr/bin"})
	want := []string{
		`set -gx DEV_HOME '/home/u/.dev'`,
		`set -gx PATH /home/u/.dev /usr/bin`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExportLines_PowerShell(t *testing.T) {
	got := ExportLines(PowerShell, `C:\Users\u\.dev`, []string{`C:\Users\u\.dev`, `C:\Users\u\.dev\versions\node\22`})
	want := []string{
		`$env:DEV_HOME = "C:\Users\u\.dev"`,
		`$env:PATH = "C:\Users\u\.dev;C:\Users\u\.dev\versions\node\22"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExportLines_UnknownFallsBackToPosixWithWarning(t *testing.T) {
	got := ExportLines(Unknown, "/home/u/.dev", []string{"/home/u/.dev"})
	want := []string{
		"# dev could not detect your shell ($SHELL was not recognized); using POSIX sh syntax, which may not be correct for yours",
		`export DEV_HOME='/home/u/.dev'`,
		`export PATH="/home/u/.dev"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
```

- [ ] **Step 7: Run tests to verify they fail**

Run: `go test ./internal/shell/... -run TestExportLines -v`
Expected: FAIL — old tests reference `ExportLines`'s old 3-arg `langs []string` signature, which no longer type-checks once Step 9 lands; for now (before Step 9), these new tests fail because they expect output `ExportLines` doesn't yet produce (it's still emitting `$DEV_HOME/bin` and `active/<lang>` segments).

- [ ] **Step 8: Rewrite `ExportLines`, deleting the now-unused segment builders**

In `internal/shell/shell.go`, replace the entire `ExportLines` function and the three `*ActiveDirSegments` helper functions below it with:

```go
// ExportLines returns the lines dev env prints, in sh's own syntax,
// given pathEntries (see ComputePathEntries) as the full, already-
// computed PATH value, in order. Unknown falls back to POSIX
// sh-compatible syntax (the same as Bash/Zsh), since that's the most
// broadly interpretable default when the shell couldn't be identified.
func ExportLines(sh Shell, devHome string, pathEntries []string) []string {
	switch sh {
	case Fish:
		return []string{
			fmt.Sprintf("set -gx DEV_HOME %s", shellQuote(devHome)),
			fmt.Sprintf("set -gx PATH %s", strings.Join(pathEntries, " ")),
		}
	case PowerShell:
		// Not Go's %q: it escapes backslashes as \\, which is wrong
		// inside a PowerShell double-quoted string (a literal Windows
		// path like C:\Users\foo\.dev must not be backslash-escaped
		// there). Only a literal embedded double-quote needs escaping,
		// via PowerShell's backtick escape character.
		escapedHome := strings.ReplaceAll(devHome, `"`, "`\"")
		escapedPath := strings.ReplaceAll(strings.Join(pathEntries, ";"), `"`, "`\"")
		return []string{
			fmt.Sprintf(`$env:DEV_HOME = "%s"`, escapedHome),
			fmt.Sprintf(`$env:PATH = "%s"`, escapedPath),
		}
	case Unknown:
		return append([]string{
			"# dev could not detect your shell ($SHELL was not recognized); using POSIX sh syntax, which may not be correct for yours",
		}, posixExportLines(devHome, pathEntries)...)
	default: // Bash, Zsh
		return posixExportLines(devHome, pathEntries)
	}
}

func posixExportLines(devHome string, pathEntries []string) []string {
	return []string{
		fmt.Sprintf(`export DEV_HOME=%s`, shellQuote(devHome)),
		fmt.Sprintf(`export PATH="%s"`, strings.Join(pathEntries, ":")),
	}
}
```

Delete `posixActiveDirSegments`, `fishActiveDirSegments`, and `powershellActiveDirSegments` entirely — nothing else calls them after this edit.

- [ ] **Step 9: Run tests to verify they pass**

Run: `go test ./internal/shell/... -run TestExportLines -v`
Expected: PASS. The package itself will not yet build cleanly (`cmd/env.go` and `cmd/setup.go` still call the old `ExportLines` signature) — that's expected and fixed in Task 2; this step only verifies `internal/shell`'s own package compiles and its own tests pass in isolation (`go test ./internal/shell/...` only builds that package and its test file, not `cmd`).

- [ ] **Step 10: Commit**

```bash
git add internal/shell/shell.go internal/shell/shell_test.go
git commit -m "feat(shell): rewrite ExportLines around a precomputed PATH entry list"
```

- [ ] **Step 11: Write the failing tests for `FunctionLines`**

Add to `internal/shell/shell_test.go`:

```go
func TestFunctionLines_Posix(t *testing.T) {
	got := FunctionLines(Bash, "/home/u/.dev")
	want := []string{
		`export DEV_HOME='/home/u/.dev'`,
		`export PATH="$DEV_HOME:$PATH"`,
		`dev() {`,
		`    command dev "$@"`,
		`    local status=$?`,
		`    case "$1" in`,
		`        lang|l) eval "$(command dev env)" ;;`,
		`    esac`,
		`    return $status`,
		`}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFunctionLines_Fish(t *testing.T) {
	got := FunctionLines(Fish, "/home/u/.dev")
	want := []string{
		`set -gx DEV_HOME '/home/u/.dev'`,
		`set -gx PATH $DEV_HOME $PATH`,
		`function dev`,
		`    command dev $argv`,
		`    set -l status $status`,
		`    switch "$argv[1]"`,
		`        case lang l`,
		`            eval (command dev env | string collect)`,
		`    end`,
		`    return $status`,
		`end`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFunctionLines_PowerShell(t *testing.T) {
	got := FunctionLines(PowerShell, `C:\Users\u\.dev`)
	want := []string{
		`$env:DEV_HOME = "C:\Users\u\.dev"`,
		`$env:PATH = "$env:DEV_HOME;$env:PATH"`,
		`function dev {`,
		`    & "$env:DEV_HOME\dev.exe" @args`,
		`    $exitStatus = $LASTEXITCODE`,
		`    if ($args.Count -gt 0 -and ($args[0] -eq "lang" -or $args[0] -eq "l")) {`,
		"        (& \"$env:DEV_HOME\\dev.exe\" env) -join \"`n\" | Invoke-Expression",
		`    }`,
		`    $global:LASTEXITCODE = $exitStatus`,
		`}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFunctionLines_UnknownFallsBackToPosixWithWarning(t *testing.T) {
	got := FunctionLines(Unknown, "/home/u/.dev")
	if got[0] != "# dev could not detect your shell ($SHELL was not recognized); using POSIX sh syntax, which may not be correct for yours" {
		t.Fatalf("expected warning comment first, got %q", got[0])
	}
	if got[1] != `export DEV_HOME='/home/u/.dev'` {
		t.Fatalf("expected POSIX fallback after the warning, got %v", got)
	}
}
```

- [ ] **Step 12: Run tests to verify they fail**

Run: `go test ./internal/shell/... -run TestFunctionLines -v`
Expected: FAIL with `undefined: FunctionLines`

- [ ] **Step 13: Implement `FunctionLines`**

Add to `internal/shell/shell.go`, after `ExportLines`/`posixExportLines`:

```go
// FunctionLines returns the full rc-file block dev setup installs: a
// one-time static PATH entry for devHome (so "command dev" can be
// found at all), followed by a dev wrapper function in sh's own
// syntax. The function always runs the real dev subcommand first,
// then — only when invoked as "dev lang ..." or its "l" alias — "re-
// evals a fresh "dev env" in the CURRENT shell, so "dev lang
// use"/"dev lang uninstall" change PATH immediately with no restart.
// Checking only the first argument (not the specific subcommand) is
// deliberate: every other "dev lang" subcommand gains a harmless,
// cheap extra "dev env" call (pure local computation, no network) in
// exchange for this function never needing to track cmd/lang.go's
// exact subcommand names. See
// docs/superpowers/specs/2026-10-01-shell-function-path-management-design.md.
func FunctionLines(sh Shell, devHome string) []string {
	switch sh {
	case Fish:
		return fishFunctionLines(devHome)
	case PowerShell:
		return powershellFunctionLines(devHome)
	case Unknown:
		return append([]string{
			"# dev could not detect your shell ($SHELL was not recognized); using POSIX sh syntax, which may not be correct for yours",
		}, posixFunctionLines(devHome)...)
	default: // Bash, Zsh
		return posixFunctionLines(devHome)
	}
}

func posixFunctionLines(devHome string) []string {
	return []string{
		fmt.Sprintf(`export DEV_HOME=%s`, shellQuote(devHome)),
		`export PATH="$DEV_HOME:$PATH"`,
		`dev() {`,
		`    command dev "$@"`,
		`    local status=$?`,
		`    case "$1" in`,
		`        lang|l) eval "$(command dev env)" ;;`,
		`    esac`,
		`    return $status`,
		`}`,
	}
}

// fishFunctionLines is posixFunctionLines's Fish equivalent. "string
// collect" gathers dev env's multi-line stdout into a single argument
// (preserving embedded newlines) before handing it to eval — fish's
// eval otherwise joins multiple arguments with spaces, which would
// mangle a multi-statement script into one broken line.
func fishFunctionLines(devHome string) []string {
	return []string{
		fmt.Sprintf("set -gx DEV_HOME %s", shellQuote(devHome)),
		`set -gx PATH $DEV_HOME $PATH`,
		`function dev`,
		`    command dev $argv`,
		`    set -l status $status`,
		`    switch "$argv[1]"`,
		`        case lang l`,
		`            eval (command dev env | string collect)`,
		`    end`,
		`    return $status`,
		`end`,
	}
}

// powershellFunctionLines is posixFunctionLines's PowerShell
// equivalent. It invokes the real binary by its absolute path
// ($env:DEV_HOME\dev.exe) rather than via PATH lookup, since a
// function named "dev" shadows any "dev" command resolution PowerShell
// would otherwise do — there is no PowerShell equivalent of POSIX
// "command" for this. "-join \"`n\"" rejoins the array of lines
// PowerShell automatically splits external-program stdout into, before
// handing the result to Invoke-Expression.
//
// Unverified on a real Windows host — this project's development
// environment is Linux-only; see internal/activebin's (now-removed)
// Windows half for the same accepted limitation on prior work.
func powershellFunctionLines(devHome string) []string {
	escaped := strings.ReplaceAll(devHome, `"`, "`\"")
	return []string{
		fmt.Sprintf(`$env:DEV_HOME = "%s"`, escaped),
		`$env:PATH = "$env:DEV_HOME;$env:PATH"`,
		`function dev {`,
		`    & "$env:DEV_HOME\dev.exe" @args`,
		`    $exitStatus = $LASTEXITCODE`,
		`    if ($args.Count -gt 0 -and ($args[0] -eq "lang" -or $args[0] -eq "l")) {`,
		"        (& \"$env:DEV_HOME\\dev.exe\" env) -join \"`n\" | Invoke-Expression",
		`    }`,
		`    $global:LASTEXITCODE = $exitStatus`,
		`}`,
	}
}
```

- [ ] **Step 14: Run tests to verify they pass**

Run: `go test ./internal/shell/... -v`
Expected: PASS (every test in the package, including Steps 1-10's)

- [ ] **Step 15: Commit**

```bash
git add internal/shell/shell.go internal/shell/shell_test.go
git commit -m "feat(shell): add FunctionLines, the dev wrapper-function template"
```

---

## Task 2: `cmd/env.go`, `cmd/setup.go`, `cmd/update.go` — wire the new mechanism in, remove shim installation

**Files:**
- Modify: `cmd/env.go`
- Modify: `cmd/env_test.go`
- Modify: `cmd/setup.go`
- Modify: `cmd/setup_test.go`
- Modify: `cmd/update.go`
- Modify: `cmd/update_test.go`
- Modify: `internal/update/apply.go` (doc comment only)

**Interfaces:**
- Consumes: `shell.ComputePathEntries`, `shell.ExportLines` (new signature), `shell.FunctionLines` (Task 1). `langManager` (`cmd/lang.go`, unchanged) and `runtime.Runtime.CurrentVersion()`/`BinDir()` (unchanged).
- Produces: `cmd.activeBinDirs(devHome string) []string` (unexported, `cmd/env.go`) — Task 5's end-to-end test does not call this directly (it drives the real `dev` binary as a subprocess), but later tasks in this package may.
- This task deletes `cmd/setup.go`'s `installShims`, `refreshShimsCmd`, `refreshShimsCommandName`, and `cmd/update.go`'s `runShimRefresh`/`defaultRunShimRefresh` — nothing outside this task references any of them (confirmed: `refreshShimsCommandName` is referenced only by `cmd/update.go`, updated in this same task).

- [ ] **Step 1: Write the failing test for `dev env`'s dynamic computation**

Replace `cmd/env_test.go`'s existing contents with:

```go
package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jpsdm/dev/internal/filesystem"
)

func TestEnvCmd_NoActiveVersionsPrintsBareDevHome(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("PATH", "/usr/bin:/bin")

	buf := new(bytes.Buffer)
	envCmd.SetOut(buf)
	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, devHome) {
		t.Fatalf("expected output to contain devHome %q, got %q", devHome, out)
	}
	if !strings.Contains(out, "/usr/bin:/bin") || !strings.HasSuffix(strings.TrimSpace(out), `/usr/bin:/bin"`) {
		t.Fatalf("expected the inherited PATH entries preserved at the end, got %q", out)
	}
}

func TestEnvCmd_IncludesActiveVersionBinDir(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("PATH", "/usr/bin")

	versionDir := filepath.Join(devHome, "versions", "node", "22")
	if err := os.MkdirAll(filepath.Join(versionDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	currentDir := filepath.Join(devHome, "current")
	if err := filesystem.WriteFileAtomic(filepath.Join(currentDir, "node"), []byte("22"), 0o644); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	envCmd.SetOut(buf)
	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	wantBinDir := filepath.Join(versionDir, "bin")
	if !strings.Contains(buf.String(), wantBinDir) {
		t.Fatalf("expected output to contain %q, got %q", wantBinDir, buf.String())
	}
}

func TestEnvCmd_StripsStaleVersionsPathEntry(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	staleEntry := filepath.Join(devHome, "versions", "node", "20", "bin")
	t.Setenv("PATH", staleEntry+":/usr/bin")

	buf := new(bytes.Buffer)
	envCmd.SetOut(buf)
	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if strings.Contains(buf.String(), staleEntry) {
		t.Fatalf("expected stale entry %q to be stripped, got %q", staleEntry, buf.String())
	}
}
```

Node is registered by `providers.Register` (via `cmd/lang.go`'s `init()`, already wired into `langManager`), so this test relies on the real Node provider's `BinDir` — matching this project's no-mocks convention.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/... -run TestEnvCmd -v`
Expected: FAIL — `envCmd`'s current `RunE` calls `shell.ExportLines(sh, devHome, langManager.Names())`, which still produces the old static `$DEV_HOME/bin`/`active/<lang>` shape, not a dynamically-computed `BinDir`-based one; `TestEnvCmd_IncludesActiveVersionBinDir` and `TestEnvCmd_StripsStaleVersionsPathEntry` fail.

- [ ] **Step 3: Rewrite `cmd/env.go`**

Replace `cmd/env.go`'s full contents with:

```go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/shell"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Print shell export lines that put the active version of every language on PATH",
	RunE: func(cmd *cobra.Command, args []string) error {
		devHome, err := platform.DevHome()
		if err != nil {
			return err
		}
		sh := shell.Detect()
		current := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))
		pathEntries := shell.ComputePathEntries(current, devHome, activeBinDirs(devHome))
		lines := shell.ExportLines(sh, devHome, pathEntries)
		fmt.Fprintln(cmd.OutOrStdout(), strings.Join(lines, "\n"))
		return nil
	},
}

// activeBinDirs returns, in langManager.Names()'s alphabetical order,
// the BinDir of every registered provider's current active version —
// skipping a provider with no active version, and skipping (rather
// than failing the whole command on) a BinDir error, since one
// language's provider misbehaving must never break dev env for every
// other language, which this command is now load-bearing for on every
// shell prompt following a `dev lang use` call.
func activeBinDirs(devHome string) []string {
	var dirs []string
	for _, name := range langManager.Names() {
		r, ok := langManager.Get(name)
		if !ok {
			continue
		}
		current, err := r.CurrentVersion()
		if err != nil || current == nil {
			continue
		}
		versionDir := filepath.Join(devHome, "versions", name, current.Name)
		dir, err := r.BinDir(versionDir)
		if err != nil {
			continue
		}
		dirs = append(dirs, dir)
	}
	return dirs
}

func init() {
	rootCmd.AddCommand(envCmd)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/... -run TestEnvCmd -v`
Expected: PASS. (The `cmd` package overall will not yet build — `cmd/setup.go` still calls the old `ExportLines` signature — fixed next.)

- [ ] **Step 5: Update `cmd/setup_test.go` for the function-based install**

`cmd/setup_test.go` currently drives `dev setup` end-to-end via `rootCmd.Execute()` (not `setupCmd.RunE` directly), using real helpers already in the file: `t.Setenv("HOME", ...)`/`t.Setenv("DEV_HOME", ...)`/`t.Setenv("SHELL", "/bin/bash")` to force Bash detection, `fakeInstalledDev(t, devHome)` to materialize `$DEV_HOME/dev` (read by `platform.Executable`, which `cmd/main_test.go`'s `TestMain` already points at `$DEV_HOME/dev` by default), and `skipOnWindowsRCFile(t)` to skip rc-file assertions on Windows (where `dev setup` never writes one). Reuse these exactly as they are — do not introduce a new seam.

Delete these five test functions entirely — they assert shim/placeholder/symlink behavior that no longer exists:
- `TestSetupCommand_CreatesPlaceholdersNotFlatBinShims`
- `TestSetupCommand_CreatesActiveDirPointingAtPlaceholder`
- `TestSetupCommand_RunningTwiceDoesNotResetAnAlreadyActiveLink`
- `TestRefreshShimsCommand_CreatesShimsWithoutPrompting`
- `TestRefreshShimsCommand_IsHiddenFromHelp`

Delete `TestSetupCommand_DeclinedConfirmationCreatesNoShims` too — `TestSetupCommand_DeclinedConfirmationMakesNoChanges` already covers "declining does nothing" without any shim-specific assertion, making this one redundant once shims don't exist.

Delete the now-dead `shimFileName` helper (nothing references it after the above deletions).

Remove the `"github.com/jpsdm/dev/internal/activebin"` import — nothing in this file uses it after the above deletions.

Replace `TestSetupCommand_ConfirmedWritesRCFileAndShims` with:

```go
func TestSetupCommand_ConfirmedWritesRCFileWithTheDevFunction(t *testing.T) {
	skipOnWindowsRCFile(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	rcPath := filepath.Join(home, ".bashrc")
	data, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("reading rc file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "DEV_HOME") {
		t.Errorf("rc file content = %q, want it to contain the DEV_HOME export", content)
	}
	if !strings.Contains(content, "dev() {") {
		t.Errorf("rc file content = %q, want it to contain the dev wrapper function", content)
	}
	if !strings.Contains(content, `eval "$(command dev env)"`) {
		t.Errorf("rc file content = %q, want it to re-eval dev env on dev lang/l", content)
	}

	got := out.String()
	if !strings.Contains(got, "Updated") {
		t.Errorf("output = %q, want it to confirm the rc file was updated", got)
	}
	if !strings.Contains(got, "estart your shell") {
		t.Errorf("output = %q, want a final summary telling the user to restart their shell", got)
	}

	for _, sub := range []string{"bin", "active", "no-active"} {
		if _, err := os.Stat(filepath.Join(devHome, sub)); !os.IsNotExist(err) {
			t.Errorf("expected %s/%s not to exist, stat err=%v", devHome, sub, err)
		}
	}
}
```

Replace `TestSetupCommand_UnsupportedShellStillCreatesShimsWhenConfirmed` with:

```go
func TestSetupCommand_UnsupportedShellStillConfirmable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	// An unrecognized $SHELL makes shell.Detect() return Unknown on
	// Linux/macOS, which takes the same "automatic rc-file editing not
	// supported" path RCPath's default case also gives PowerShell on
	// Windows — but shell.Detect() ignores $SHELL entirely on Windows
	// (runtime.GOOS=="windows" short-circuits to PowerShell
	// unconditionally), so this env var has no effect there. The
	// assertion below computes the platform-correct expected text via
	// unsupportedShellMessage(runtime.GOOS) rather than a hardcoded
	// literal, so this test is correct on every platform without
	// needing a runtime.GOOS branch of its own.
	t.Setenv("SHELL", "/bin/some-unrecognized-shell")
	fakeInstalledDev(t, devHome)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "dev() {") {
		t.Errorf("output = %q, want it to print the dev wrapper function for manual setup", got)
	}
	wantMsg := unsupportedShellMessage(runtime.GOOS)
	if !strings.Contains(got, wantMsg) {
		t.Errorf("output = %q, want it to contain %q (the platform-correct unsupported-shell message)", got, wantMsg)
	}
}
```

In `TestSetupCommand_RunningTwiceDoesNotDuplicateOrError`, delete the trailing block that resolves `nodePlaceholder`/`javaPlaceholder` via `platform.NoActiveDir` and loops over expected shim paths (everything from the `nodePlaceholder, err := platform.NoActiveDir("node")` line through the end of that `for` loop) — keep the rest of the test (the two `run()` calls and the rc-file marker-block-count assertion) unchanged; `UpsertBlock`'s idempotency on a second `dev setup` is still exactly what this test covers.

Leave `TestSetupCommand_DeclinedConfirmationMakesNoChanges`, `TestSetupCommand_UnsupportedShellDeclinedCreatesNoShims`, `TestSetupCommand_RelocatesDevAndDocsIntoDevHomeWhenConfirmed`, `TestInstallRelocation_MissingReadmeOrLicenseIsNotFatal`, `TestInstallRelocation_ReportsRemovalFailureAsSafeToDelete`, `TestSetupCommand_SkipsRelocationWhenAlreadyInsideDevHome`, `TestSetupCommand_OffersRelocationWhenNotYetInstalled`, `TestSetupCommand_DeclinedRelocationPromptDoesNotAbortSetup`, `TestCopyExecutable_ReappliesPermissionsToExistingFile`, `TestUnsupportedShellMessage_WindowsDoesNotAskForManualEditing`, and `TestUnsupportedShellMessage_UnknownShellKeepsManualInstructions` exactly as they are — none of them reference shims, placeholders, or `ExportLines`, and all still exercise behavior this task leaves unchanged (relocation, `copyExecutable`, declining prompts, the unsupported-shell message text itself).

- [ ] **Step 6: Run tests to verify they fail**

Run: `go test ./cmd/... -run TestSetupCommand -v`
Expected: FAIL to compile — `cmd/setup.go` still calls `shell.ExportLines(sh, devHome, langManager.Names())`, which no longer matches what Task 1 defines (`ExportLines`'s parameter is unchanged in type (`[]string`) but `setup.go` hasn't yet been rewritten to call `shell.FunctionLines` at all) — the new/rewritten tests fail on content assertions (no `dev() {` block present, `DEV_HOME/bin`-reference still absent-but-untested), not a Go compile error, since `langManager.Names()` still type-checks as a `[]string` argument either way.

- [ ] **Step 7: Rewrite `cmd/setup.go`**

Replace `cmd/setup.go`'s full contents with:

```go
package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/filesystem"
	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/shell"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Detect your shell and configure PATH (asks for confirmation)",
	RunE: func(cmd *cobra.Command, args []string) error {
		printBanner(cmd.OutOrStdout())

		devHome, err := platform.DevHome()
		if err != nil {
			return err
		}
		sh := shell.Detect()
		lines := shell.FunctionLines(sh, devHome)

		path, supported, err := shell.RCPath(sh)
		if err != nil {
			return err
		}
		if !supported {
			fmt.Fprintln(cmd.OutOrStdout(), unsupportedShellMessage(runtime.GOOS))
			fmt.Fprintln(cmd.OutOrStdout())
			for _, line := range lines {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			fmt.Fprintln(cmd.OutOrStdout())

			confirmed, err := shell.Confirm("Set these up now? [y/N] ", cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if !confirmed {
				cliutil.Fstep(cmd.OutOrStdout(), "No changes made.")
				return nil
			}

			if err := relocateIfNeeded(cmd, devHome); err != nil {
				return err
			}

			if runtime.GOOS == "windows" {
				envConfirmed, err := shell.Confirm("Add these to your user environment now? [y/N] ", cmd.InOrStdin(), cmd.OutOrStdout())
				if err != nil {
					return err
				}
				if envConfirmed {
					//nolint:staticcheck // SA4023: Check is only statically-always-true on non-Windows builds; it's genuinely needed on Windows
					if err := shell.ConfigureWindowsUserEnv(devHome); err != nil {
						return err
					}
					cliutil.Fsuccess(cmd.OutOrStdout(), "Updated your user environment (DEV_HOME and PATH)")
				}
			}
			return nil
		}

		fmt.Fprintf(cmd.OutOrStdout(), "The following will be added to %s:\n\n", path)
		for _, line := range lines {
			fmt.Fprintln(cmd.OutOrStdout(), line)
		}
		fmt.Fprintln(cmd.OutOrStdout())

		confirmed, err := shell.Confirm(fmt.Sprintf("Add this to %s? [y/N] ", path), cmd.InOrStdin(), cmd.OutOrStdout())
		if err != nil {
			return err
		}
		if !confirmed {
			cliutil.Fstep(cmd.OutOrStdout(), "No changes made.")
			return nil
		}

		if err := relocateIfNeeded(cmd, devHome); err != nil {
			return err
		}

		if err := shell.UpsertBlock(path, lines); err != nil {
			return fmt.Errorf("updating %s: %w", path, err)
		}
		cliutil.Fsuccess(cmd.OutOrStdout(), "Updated %s", path)

		cliutil.Fstep(cmd.OutOrStdout(), "Restart your shell (or run `source %s`) for these changes to take effect", path)
		return nil
	},
}

// unsupportedShellMessage returns the header setup prints when the
// detected shell has no rc file dev can safely auto-edit. Windows gets
// different wording than a genuinely unidentified $SHELL (Linux/macOS
// Unknown): PowerShell's environment variables ARE configured
// automatically later in this same run (via the registry, see
// ConfigureWindowsUserEnv), so the header must not tell the user to
// set them by hand — only the rc-file auto-edit is what's actually
// missing there. A genuine Unknown shell has no automatic mechanism at
// all, so it keeps asking for manual editing.
func unsupportedShellMessage(goos string) string {
	if goos == "windows" {
		return "PowerShell doesn't have a profile file dev can safely auto-edit, but dev can configure your environment variables directly. Here's what will be set:"
	}
	return "Automatic setup isn't supported for this shell yet. Add these lines manually:"
}

// relocateIfNeeded offers to move the running binary (plus its
// README.md/LICENSE siblings) into devHome, when it isn't already
// running from there. A no-op when re-running setup on an
// already-installed copy — there's nothing to relocate, and
// installRelocation must never be given devHome as its own exe
// argument (see installRelocation's doc comment).
func relocateIfNeeded(cmd *cobra.Command, devHome string) error {
	runningFromDevHome, _, err := platform.RunningFromDevHome()
	if err != nil {
		return err
	}
	if runningFromDevHome {
		return nil
	}

	// platform.Executable, not the raw os.Executable — the same
	// resolvable-in-tests source of truth RunningFromDevHome's check
	// above just used, so the exe this function relocates is
	// guaranteed to be the exact one that check evaluated. Calling the
	// unmockable os.Executable directly here would let this function's
	// idea of "the binary" silently diverge from RunningFromDevHome's
	// in a test that overrides platform.Executable (production takes
	// the same code path either way, since platform.Executable's
	// default value is os.Executable itself).
	exe, err := platform.Executable()
	if err != nil {
		return fmt.Errorf("finding the dev binary: %w", err)
	}

	confirmed, err := shell.Confirm("Move dev, README.md, and LICENSE into $DEV_HOME now? [y/N] ", cmd.InOrStdin(), cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if !confirmed {
		cliutil.Fstep(cmd.OutOrStdout(), "No changes made.")
		return nil
	}

	if err := installRelocation(cmd.OutOrStdout(), exe, devHome); err != nil {
		return err
	}
	cliutil.Fsuccess(cmd.OutOrStdout(), "Moved dev into %s", devHome)
	return nil
}

// installRelocation copies exe (and its README.md/LICENSE siblings,
// if present) into devHome, then best-effort removes the originals.
// A missing README.md/LICENSE (e.g. a from-source `go build` binary,
// which has no such siblings) is not an error; only exe itself is
// required to exist. Callers must only invoke this when exe is
// genuinely outside devHome (relocateIfNeeded's RunningFromDevHome
// check guarantees this) — copying devHome/dev onto itself would
// truncate it via copyExecutable's read-then-write.
//
// out takes the best-effort removal warning: the caller's own writer,
// so it is captured under test like every other message setup emits,
// rather than escaping to the global stdout.
func installRelocation(out io.Writer, exe, devHome string) error {
	dest := filepath.Join(devHome, filepath.Base(exe))
	if err := copyExecutable(exe, dest); err != nil {
		return fmt.Errorf("copying %s into %s: %w", exe, devHome, err)
	}
	relocated := []string{exe}

	srcDir := filepath.Dir(exe)
	for _, name := range []string{"README.md", "LICENSE"} {
		src := filepath.Join(srcDir, name)
		data, err := os.ReadFile(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("reading %s: %w", src, err)
		}
		if err := filesystem.WriteFileAtomic(filepath.Join(devHome, name), data, 0o644); err != nil {
			return fmt.Errorf("copying %s into %s: %w", src, devHome, err)
		}
		relocated = append(relocated, src)
	}

	for _, path := range relocated {
		if err := os.Remove(path); err != nil {
			// Best-effort, and expected to fail often on Windows, where
			// a running process can't delete its own executable image —
			// dev's copy in devHome is already in place and working, so
			// this is not a problem to report as one. A leftover
			// original outside $DEV_HOME is exactly what
			// RunningFromDevHome's check guards against if it's ever
			// run again, so there's nothing left for the user to do
			// except delete it whenever they like.
			cliutil.Fsuccess(out, "%s is copied and set up — you can delete %s now", filepath.Base(path), path)
		}
	}
	return nil
}

// copyExecutable copies src to dest with executable permissions,
// atomically replacing any existing file at dest (and re-applying 0o755
// to it, unlike a plain os.WriteFile which only sets permissions when
// creating a new file). Still used by installRelocation — not removed
// alongside the shim mechanism, since relocating dev's own binary is
// an unrelated, ongoing need.
func copyExecutable(src, dest string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if err := filesystem.WriteFileAtomic(dest, data, 0o755); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(setupCmd)
}
```

This removes `installShims`, `refreshShimsCmd`, and `refreshShimsCommandName` entirely, and drops the `internal/activebin`, `internal/providers`, and `devruntime "github.com/jpsdm/dev/internal/runtime"` imports (nothing in this file uses them anymore).

- [ ] **Step 8: Update `cmd/update.go`, which references the now-deleted `refreshShimsCommandName`**

Replace `cmd/update.go`'s full contents with:

```go
package cmd

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/config"
	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/shell"
	"github.com/jpsdm/dev/internal/update"
)

// newUpdateClient is a package-level var so tests can point dev
// update (and cmd/root.go's passive notice) at a fake server — the
// same substitution pattern this package already uses for
// promptWorkspace/langManager. Production code always gets the real
// GitHub API via update.NewClient.
var newUpdateClient = update.NewClient

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check for and install the latest dev release",
	RunE: func(cmd *cobra.Command, args []string) error {
		current := Version
		if !update.IsCleanVersion(current) {
			return fmt.Errorf("dev update isn't available for a local build (%s isn't a released version)", current)
		}

		client := newUpdateClient()
		release, err := client.FetchLatest(cmd.Context())
		if err != nil {
			return err
		}

		if !update.NewerThan(release.TagName, current) {
			cliutil.Fsuccess(cmd.OutOrStdout(), "Already on the latest version (%s).", current)
			return nil
		}

		fmt.Fprintf(cmd.OutOrStdout(), "%s → %s\n\n", current, release.TagName)
		confirmed, err := shell.Confirm("Update now? [y/N] ", cmd.InOrStdin(), cmd.OutOrStdout())
		if err != nil {
			return err
		}
		if !confirmed {
			cliutil.Fstep(cmd.OutOrStdout(), "No changes made.")
			return nil
		}

		devHome, err := platform.DevHome()
		if err != nil {
			return err
		}
		if err := update.ApplyUpdate(cmd.Context(), cmd.OutOrStdout(), release, devHome); err != nil {
			return err
		}

		cfgPath, err := config.DefaultPath()
		if err != nil {
			return err
		}
		cfg, err := config.Load(cfgPath)
		if err != nil && !errors.Is(err, config.ErrNotFound) {
			return err
		}
		cfg.UpdateCheck = config.UpdateCheck{LastChecked: time.Now(), LatestVersion: release.TagName}
		if err := config.Save(cfgPath, cfg); err != nil {
			return err
		}

		cliutil.Fsuccess(cmd.OutOrStdout(), "Updated to %s.", release.TagName)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)
}
```

This drops `runShimRefresh`/`defaultRunShimRefresh` entirely (no shim files are ever created, so there's nothing left to refresh), the shim-refresh call and its recovery-message error wrap, and the trailing `dev setup` reminder (it existed specifically to pick up a new provider's `active/<lang>` PATH entry — moot, since the shell function never needs to know about specific providers; a real migration note is added in Task 6 instead, for users still on a pre-this-design install).

In `cmd/update_test.go`, delete any test that sets or asserts against `runShimRefresh` (search for `runShimRefresh`) — there is no longer a seam to substitute.

- [ ] **Step 9: Fix `internal/update/apply.go`'s stale doc comment**

In `internal/update/apply.go`, `ApplyUpdate`'s doc comment currently reads:

```go
// ApplyUpdate downloads, checksum-verifies, and installs release in
// place of the binary currently at devHome (devHome/dev or
// devHome/dev.exe). Does not touch $DEV_HOME/bin's shims — the caller
// (cmd/update.go) refreshes those afterward by spawning the
// newly-installed binary itself (not by calling into this
// still-running old process), since only the new binary's own
// compiled-in provider list knows about a language it may have just
// added.
```

Replace it with:

```go
// ApplyUpdate downloads, checksum-verifies, and installs release in
// place of the binary currently at devHome (devHome/dev or
// devHome/dev.exe).
```

- [ ] **Step 10: Run the full `cmd` package test suite**

Run: `go build ./... && go test ./cmd/... -v`
Expected: PASS. If any pre-existing test in `cmd/setup_test.go`/`cmd/update_test.go` still references deleted symbols (`installShims`, `refreshShimsCmd`, `runShimRefresh`, old `ExportLines`/langs-based assertions), delete or rewrite it to match this task's new behavior — there must be zero references to the removed symbols left in the package.

- [ ] **Step 11: Commit**

```bash
git add cmd/env.go cmd/env_test.go cmd/setup.go cmd/setup_test.go cmd/update.go cmd/update_test.go internal/update/apply.go
git commit -m "feat: compute PATH dynamically in dev env, install a shell function instead of shims"
```

---

## Task 3: Remove the shim-dispatch mechanism — `ShimNames()`, `internal/shim`, `main.go`, `platform.go`, and revert the 4 providers

**Files:**
- Modify: `internal/runtime/runtime.go`
- Modify: `internal/runtime/runtime_test.go`
- Modify: `internal/runtime/node/node.go`, `internal/runtime/node/node_test.go`
- Modify: `internal/runtime/java/java.go`, `internal/runtime/java/java_test.go`
- Modify: `internal/runtime/go/go.go`, `internal/runtime/go/go_test.go`
- Modify: `internal/runtime/python/python.go`, `internal/runtime/python/python_test.go`
- Delete: `internal/shim/shim.go`, `internal/shim/exec_unix.go`, `internal/shim/exec_windows.go`, `internal/shim/shim_test.go` (whole package)
- Modify: `main.go`
- Modify: `main_test.go`
- Modify: `internal/platform/platform.go`
- Modify: `internal/platform/platform_test.go`
- Modify: `cmd/lang_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `Runtime` interface with `ShimNames()` removed — every provider's concrete type must still satisfy the narrower interface. `platform.RunningFromDevHome`/`dirMatchesDevHome` recognize only `$DEV_HOME` itself (no `bin`/`no-active` branches). `platform.ActiveDir`/`platform.NoActiveDir`/`platform.BinDir` ($DEV_HOME/bin) are deleted entirely — Task 4 confirms (via `go build ./...`) nothing outside this task's own edits still references them.

This task removes a tightly-coupled cluster in one pass: `main.go`'s dispatch only compiles against a `Runtime` whose providers still have `ShimNames()`+`internal/shim`'s `findProvider`, so none of these pieces can be removed independently without breaking the build.

- [ ] **Step 1: Remove `ShimNames()` from the `Runtime` interface**

In `internal/runtime/runtime.go`, delete the `ShimNames` method and its doc comment from the `Runtime` interface:

```go
	// ShimNames returns the binary names this provider's installed
	// versions expose (e.g. ["node", "npm", "npx"] for Node.js). dev
	// setup creates a shim copy for each name returned by every
	// registered provider.
	ShimNames() []string

```

Also update `BinDir`'s doc comment, which currently describes the now-removed `internal/activebin` indirection:

```go
	// BinDir returns the directory whose contents should be exposed on
	// PATH for an active version installed at versionDir — e.g.
	// "<versionDir>/bin" on most platforms, or versionDir itself where a
	// provider's binaries sit at the version root (Python's Windows
	// builds). Used by Activate/Uninstall to repoint
	// $DEV_HOME/active/<name> (see internal/activebin) so tools
	// installed by a language's own package manager (npm -g,
	// pip console-scripts — not go install, which writes to
	// GOBIN/GOPATH outside any managed version's own directory, a
	// deliberate non-goal of the Go provider) are reachable on PATH
	// without dev knowing about them by name in advance — unlike
	// ShimNames(), which only ever covers a fixed, known set.
	BinDir(versionDir string) (string, error)
```

Replace with:

```go
	// BinDir returns the directory whose contents should be exposed on
	// PATH for an active version installed at versionDir — e.g.
	// "<versionDir>/bin" on most platforms, or versionDir itself where a
	// provider's binaries sit at the version root (Python's Windows
	// builds). dev env uses this directly to compute PATH, so tools
	// installed by a language's own package manager (npm -g,
	// pip console-scripts — not go install, which writes to
	// GOBIN/GOPATH outside any managed version's own directory, a
	// deliberate non-goal of the Go provider) are reachable on PATH
	// the moment they're installed, with no dev-side configuration.
	BinDir(versionDir string) (string, error)
```

In `internal/runtime/runtime_test.go:24`, delete the stub's `ShimNames` method:

```go
func (s *stubRuntime) ShimNames() []string         { return nil }
```

- [ ] **Step 2: Revert each of the 4 providers' `Activate`/`Uninstall`, and delete their `ShimNames()`**

For each of the 4 providers, make the same four edits. The exact before/after text for each is given below — each provider's current `Uninstall`/`Activate` has the identical shape (confirmed by reading all 4 source files), just with the receiver, language key, and display name swapped.

### `internal/runtime/node/node.go`

Replace:

```go
		activeDir, err := platform.ActiveDir("node")
		if err != nil {
			return err
		}
		noActiveDir, err := platform.NoActiveDir("node")
		if err != nil {
			return err
		}
		if err := activebin.Repoint(activeDir, noActiveDir); err != nil {
			return fmt.Errorf("reverting active/node to the placeholder: %w", err)
		}

		cliutil.Step("Node.js %s was the active version; no version is active now", name)
```

with:

```go
		cliutil.Step("Node.js %s was the active version; no version is active now", name)
```

Replace:

```go
	binDir, err := n.BinDir(destDir)
	if err != nil {
		return fmt.Errorf("activating Node.js %s: %w", name, err)
	}
	activeDir, err := platform.ActiveDir("node")
	if err != nil {
		return err
	}
	if err := activebin.Repoint(activeDir, binDir); err != nil {
		return fmt.Errorf("activating Node.js %s: %w", name, err)
	}

	currentDir, err := platform.CurrentDir()
```

with:

```go
	currentDir, err := platform.CurrentDir()
```

Delete the `ShimNames()` method (`func (n *Node) ShimNames() []string { ... }`, around line 545) entirely, and remove the `"github.com/jpsdm/dev/internal/activebin"` import line.

### `internal/runtime/java/java.go`

Replace:

```go
		activeDir, err := platform.ActiveDir("java")
		if err != nil {
			return err
		}
		noActiveDir, err := platform.NoActiveDir("java")
		if err != nil {
			return err
		}
		if err := activebin.Repoint(activeDir, noActiveDir); err != nil {
			return fmt.Errorf("reverting active/java to the placeholder: %w", err)
		}

		cliutil.Step("Java %s was the active version; no version is active now", name)
```

with:

```go
		cliutil.Step("Java %s was the active version; no version is active now", name)
```

Replace:

```go
	binDir, err := j.BinDir(destDir)
	if err != nil {
		return fmt.Errorf("activating Java %s: %w", name, err)
	}
	activeDir, err := platform.ActiveDir("java")
	if err != nil {
		return err
	}
	if err := activebin.Repoint(activeDir, binDir); err != nil {
		return fmt.Errorf("activating Java %s: %w", name, err)
	}

	currentDir, err := platform.CurrentDir()
```

with:

```go
	currentDir, err := platform.CurrentDir()
```

Delete the `ShimNames()` method (`func (j *Java) ShimNames() []string { ... }`, around line 508) entirely, and remove the `"github.com/jpsdm/dev/internal/activebin"` import line.

### `internal/runtime/go/go.go`

Replace:

```go
		activeDir, err := platform.ActiveDir("go")
		if err != nil {
			return err
		}
		noActiveDir, err := platform.NoActiveDir("go")
		if err != nil {
			return err
		}
		if err := activebin.Repoint(activeDir, noActiveDir); err != nil {
			return fmt.Errorf("reverting active/go to the placeholder: %w", err)
		}

		cliutil.Step("Go %s was the active version; no version is active now", name)
```

with:

```go
		cliutil.Step("Go %s was the active version; no version is active now", name)
```

Replace:

```go
	binDir, err := g.BinDir(destDir)
	if err != nil {
		return fmt.Errorf("activating Go %s: %w", name, err)
	}
	activeDir, err := platform.ActiveDir("go")
	if err != nil {
		return err
	}
	if err := activebin.Repoint(activeDir, binDir); err != nil {
		return fmt.Errorf("activating Go %s: %w", name, err)
	}

	currentDir, err := platform.CurrentDir()
```

with:

```go
	currentDir, err := platform.CurrentDir()
```

Delete the `ShimNames()` method (`func (g *Go) ShimNames() []string { ... }`, around line 584) entirely, and remove the `"github.com/jpsdm/dev/internal/activebin"` import line.

### `internal/runtime/python/python.go`

Replace:

```go
		activeDir, err := platform.ActiveDir("python")
		if err != nil {
			return err
		}
		noActiveDir, err := platform.NoActiveDir("python")
		if err != nil {
			return err
		}
		if err := activebin.Repoint(activeDir, noActiveDir); err != nil {
			return fmt.Errorf("reverting active/python to the placeholder: %w", err)
		}

		cliutil.Step("Python %s was the active version; no version is active now", name)
```

with:

```go
		cliutil.Step("Python %s was the active version; no version is active now", name)
```

Replace:

```go
	binDir, err := p.BinDir(destDir)
	if err != nil {
		return fmt.Errorf("activating Python %s: %w", name, err)
	}
	activeDir, err := platform.ActiveDir("python")
	if err != nil {
		return err
	}
	if err := activebin.Repoint(activeDir, binDir); err != nil {
		return fmt.Errorf("activating Python %s: %w", name, err)
	}

	currentDir, err := platform.CurrentDir()
```

with:

```go
	currentDir, err := platform.CurrentDir()
```

Delete the `ShimNames()` method (`func (p *Python) ShimNames() []string { ... }`, around line 652) entirely, and remove the `"github.com/jpsdm/dev/internal/activebin"` import line.

- [ ] **Step 3: Delete the now-obsolete tests in each provider's `_test.go`**

From each of `node_test.go`, `java_test.go`, `go_test.go`, `python_test.go`, delete:
- `TestShimNames`
- `TestActivate_RepointsActiveDirToBinDir`
- `TestActivate_TwiceInARowRepointsToTheSecondVersion`
- `TestUninstall_OfActiveVersionRepointsActiveDirToPlaceholder`
- `TestUninstall_OfNonActiveVersionLeavesActiveDirAlone`

`node_test.go` additionally has `TestActivate_RepointFailureDoesNotWriteMarker` — delete that one too (unique to Node; the other three providers don't have it).

Keep every other existing test in these files unchanged (`TestUninstall_NotInstalledReturnsError`, `TestUninstall_RemovesDirectory`, `TestUninstall_LeavesOtherActiveVersionMarkerAlone`, `TestUninstall_ClearsActiveMarkerIfCurrentlyActive`, `TestActivate_NotInstalledReturnsError`, `TestActivate_WritesMarkerFile`, `TestCurrentVersion_NilWhenNoneActive`, and all install/list/download tests) — they exercise the marker-file behavior this task's revert preserves unchanged. If any of the kept tests also asserts repoint/active-dir behavior inline (rather than purely via a separately-named test), strip just that assertion, keeping the rest of the test.

- [ ] **Step 4: Run each provider's test suite**

Run: `go test ./internal/runtime/... -v`
Expected: PASS for every kept test; no references to `ShimNames`, `activebin`, `platform.ActiveDir`, or `platform.NoActiveDir` remain anywhere under `internal/runtime/`.

- [ ] **Step 5: Commit the provider revert**

```bash
git add internal/runtime/runtime.go internal/runtime/runtime_test.go \
  internal/runtime/node internal/runtime/java internal/runtime/go internal/runtime/python
git commit -m "feat(runtime): remove ShimNames and revert Activate/Uninstall to marker-only"
```

- [ ] **Step 6: Delete `internal/shim`**

```bash
git rm internal/shim/shim.go internal/shim/exec_unix.go internal/shim/exec_windows.go internal/shim/shim_test.go
rmdir internal/shim 2>/dev/null || true
```

- [ ] **Step 7: Simplify `main.go`**

Replace `main.go`'s full contents with:

```go
package main

import (
	"github.com/jpsdm/dev/cmd"
)

func main() {
	cmd.Execute()
}
```

`dev` is only ever invoked as `dev` now — there is no other binary name to dispatch on, and `cmd.Execute()`'s own `PersistentPreRunE` (in `cmd/root.go`, unchanged by this plan) already handles the "not installed" warning for a copy running outside `$DEV_HOME`.

- [ ] **Step 8: Update `main_test.go`**

Delete every test exercising `decideDispatch`/`shimBinaryName`/`dispatchAsShim`/`dispatchRefuseNotInstalled` (the entire dispatch-decision logic this step's `main.go` rewrite removes). If `main_test.go` ends up with no remaining tests, delete the file entirely:

```bash
git rm main_test.go
```

(Check first with `grep -n "^func Test" main_test.go` — if it has any test unrelated to dispatch, keep the file and only remove the dispatch-specific tests.)

- [ ] **Step 9: Simplify `internal/platform/platform.go`**

Delete the `ActiveDir` and `NoActiveDir` functions entirely:

```go
// ActiveDir returns DEV_HOME/active/<lang> — the directory symlink
// (Unix) or junction (Windows) dev setup creates and dev lang
// use/uninstall repoint (see internal/activebin) so lang's active
// version's whole bin directory, not just a fixed set of known binary
// names, is reachable on PATH.
func ActiveDir(lang string) (string, error) {
	home, err := DevHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "active", lang), nil
}

// NoActiveDir returns DEV_HOME/no-active/<lang> — the placeholder
// directory ActiveDir's link points at before any version of lang has
// ever been activated. Populated by dev setup with a copy of the dev
// binary per lang's ShimNames() entry (see cmd/setup.go), so the
// existing internal/shim dispatch's friendly "not active" message
// still fires until a real version takes over.
func NoActiveDir(lang string) (string, error) {
	home, err := DevHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "no-active", lang), nil
}
```

Delete the `BinDir` function too (the `$DEV_HOME/bin` resolver — confirmed unused by any caller anywhere in the codebase as of this task, and the spec lists `$DEV_HOME/bin` itself as removed):

```go
// BinDir returns DEV_HOME/bin. Nothing is written here anymore as of
// the active-bin-dir-exposure feature (see internal/activebin and
// cmd/setup.go's installShims) — shims now live under
// DEV_HOME/no-active/<lang>/ instead — but this directory stays on
// PATH (see internal/shell.ExportLines) so an older dev version's
// leftover shim copies here keep working until the user re-runs
// `dev setup`.
func BinDir() (string, error) {
	return subdir("bin")
}
```

Simplify `dirMatchesDevHome` to drop the `bin`/`no-active` branches — replace:

```go
// dirMatchesDevHome is RunningFromDevHome's pure comparison logic,
// separated so it's testable with arbitrary inputs regardless of
// where the test binary actually lives — the same reason javaOS's
// switch logic lives in a separate mapJavaOS function elsewhere in
// this codebase. goos is a parameter (not read from runtime.GOOS
// directly) for the same testability reason.
//
// A placeholder copy of dev at DEV_HOME/no-active/<lang>/<name> (see
// cmd/setup.go's installShims and internal/activebin) must also count
// as "running from DEV_HOME" — exeDir in that case is
// DEV_HOME/no-active/<lang>, one level under DEV_HOME/no-active, never
// deeper (each language gets exactly one placeholder directory), so
// checking the immediate parent is sufficient and doesn't need to
// enumerate language names.
func dirMatchesDevHome(exeDir, devHome, goos string) bool {
	if goos == "windows" {
		// filepath.Dir/Join/Clean use the separator of the OS this
		// binary was actually built for, not the goos parameter — on a
		// non-Windows build (e.g. this package's own tests, run on
		// Linux CI) they'd treat a backslash-separated Windows path as
		// one giant path component instead of splitting it. Normalizing
		// to "/" first keeps the comparison correct regardless of which
		// OS this code is running on; it's a no-op in production, where
		// goos always matches the real build target and real Windows
		// paths from os.Executable() already use backslashes natively
		// (which filepath.Clean on Windows accepts and normalizes).
		exeDir = strings.ReplaceAll(exeDir, `\`, "/")
		devHome = strings.ReplaceAll(devHome, `\`, "/")
	}
	exeDir = filepath.Clean(exeDir)
	devHome = filepath.Clean(devHome)
	binDir := filepath.Join(devHome, "bin")
	noActiveParent := filepath.Join(devHome, "no-active")
	exeParent := filepath.Dir(exeDir)
	if goos == "windows" {
		return strings.EqualFold(exeDir, devHome) ||
			strings.EqualFold(exeDir, binDir) ||
			strings.EqualFold(exeParent, noActiveParent)
	}
	return exeDir == devHome || exeDir == binDir || exeParent == noActiveParent
}
```

with:

```go
// dirMatchesDevHome is RunningFromDevHome's pure comparison logic,
// separated so it's testable with arbitrary inputs regardless of
// where the test binary actually lives — the same reason javaOS's
// switch logic lives in a separate mapJavaOS function elsewhere in
// this codebase. goos is a parameter (not read from runtime.GOOS
// directly) for the same testability reason.
func dirMatchesDevHome(exeDir, devHome, goos string) bool {
	if goos == "windows" {
		// filepath.Dir/Join/Clean use the separator of the OS this
		// binary was actually built for, not the goos parameter — on a
		// non-Windows build (e.g. this package's own tests, run on
		// Linux CI) they'd treat a backslash-separated Windows path as
		// one giant path component instead of splitting it. Normalizing
		// to "/" first keeps the comparison correct regardless of which
		// OS this code is running on; it's a no-op in production, where
		// goos always matches the real build target and real Windows
		// paths from os.Executable() already use backslashes natively
		// (which filepath.Clean on Windows accepts and normalizes).
		exeDir = strings.ReplaceAll(exeDir, `\`, "/")
		devHome = strings.ReplaceAll(devHome, `\`, "/")
	}
	exeDir = filepath.Clean(exeDir)
	devHome = filepath.Clean(devHome)
	if goos == "windows" {
		return strings.EqualFold(exeDir, devHome)
	}
	return exeDir == devHome
}
```

Also update `RunningFromDevHome`'s own doc comment, which currently says `"or exactly $DEV_HOME/bin (where shims live)"`:

```go
// RunningFromDevHome reports whether the currently-executing binary's
// own directory is exactly $DEV_HOME or exactly $DEV_HOME/bin (where
// shims live). Also returns the resolved devHome path so callers that
```

Replace with:

```go
// RunningFromDevHome reports whether the currently-executing binary's
// own directory is exactly $DEV_HOME. Also returns the resolved
// devHome path so callers that
```

And `NotInstalledWarning`'s text currently says `"creates $DEV_HOME, sets up shims, and configures your PATH"` — update to:

```go
const NotInstalledWarning = "dev isn't installed yet — this looks like a copy running from outside $DEV_HOME. " +
	"Run `dev setup` to install it (creates $DEV_HOME and configures your PATH), then run this command again."
```

- [ ] **Step 10: Update `internal/platform/platform_test.go`**

Delete every test for `ActiveDir`, `NoActiveDir`, `BinDir` ($DEV_HOME/bin), and any `dirMatchesDevHome`/`RunningFromDevHome` subtest asserting the `bin`/`no-active` branches (e.g. a subtest checking `exeDir == filepath.Join(devHome, "bin")` matches, or `exeDir`'s parent being `no-active`). Keep the subtests asserting `exeDir == devHome` matches and a clearly-unrelated directory does not match.

- [ ] **Step 11: Update `cmd/lang_test.go`'s stub**

Delete line 54:

```go
func (s *stubRuntime) ShimNames() []string { return nil }
```

- [ ] **Step 12: Run the full build and test suite**

Run: `go build ./... && go test ./... -v`
Expected: PASS, with zero remaining references anywhere in the module to `internal/shim`, `ShimNames`, `platform.ActiveDir`, `platform.NoActiveDir`, or `platform.BinDir()` (the `$DEV_HOME/bin` one — not to be confused with `runtime.Runtime.BinDir`, which is unrelated and unchanged). Confirm with:

```bash
grep -rn "internal/shim\|ShimNames\|platform\.ActiveDir\|platform\.NoActiveDir\|platform\.BinDir" --include="*.go" .
```

Expected: no output.

- [ ] **Step 13: Commit**

```bash
git add main.go internal/platform/platform.go internal/platform/platform_test.go cmd/lang_test.go
git add -u  # picks up main_test.go deletion if applicable, and internal/shim's removal
git commit -m "feat: remove the shim-dispatch mechanism entirely"
```

---

## Task 4: Delete `internal/activebin`

**Files:**
- Delete: `internal/activebin/activebin_unix.go`, `internal/activebin/activebin_windows.go`, and their `_test.go` siblings (whole package)

**Interfaces:**
- Consumes: nothing (by the end of Task 3, nothing in the module imports `internal/activebin` anymore).
- Produces: nothing — pure deletion.

- [ ] **Step 1: Confirm nothing still imports `internal/activebin`**

Run:
```bash
grep -rln "internal/activebin" --include="*.go" . | grep -v "^internal/activebin/"
```
Expected: no output (Task 2 removed `cmd/setup.go`'s import, Task 3 removed all 4 providers' imports).

- [ ] **Step 2: Delete the package**

```bash
git rm -r internal/activebin
```

- [ ] **Step 3: Run the full build and test suite**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 4: Run `make check`**

Run: `make check`
Expected: PASS (fmt + vet + lint + test + build all clean).

- [ ] **Step 5: Commit**

```bash
git add -u
git commit -m "chore: remove internal/activebin"
```

---

## Task 5: Real end-to-end shell integration test

**Files:**
- Create: `cmd/shellintegration_test.go`

**Interfaces:**
- Consumes: `go build` (via `os/exec`, this project's established pattern from `internal/shim/shim_test.go`'s now-deleted history and `main_test.go`) to produce a real `dev` binary in a temp dir; the real `dev() { ... }` POSIX function body from `shell.FunctionLines(shell.Bash, devHome)` (Task 1).
- Produces: nothing consumed by other tasks — this is the spec's required "real end-to-end dispatch check" (the exact test class the prior `v0.7.0` final review found missing before it shipped a real bug), applied here from the start.

- [ ] **Step 1: Write the end-to-end test**

Create `cmd/shellintegration_test.go`:

```go
package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jpsdm/dev/internal/filesystem"
	"github.com/jpsdm/dev/internal/shell"
)

// TestShellIntegration_LangUseUpdatesCurrentShellPath builds this
// branch's own dev binary, writes a real POSIX shell function matching
// what dev setup installs, sources it in a real sh subprocess, runs
// `dev lang use node 22` against a fake-but-real installed version
// directory, and asserts the SUBPROCESS'S OWN PATH (not dev's
// internal computation) ends up containing the version's bin
// directory — the exact "real end-to-end dispatch, not just a unit
// test of the pure computation" class of test the v0.7.0 final review
// found missing before it shipped a real bug. Skipped on Windows: the
// function under test here is the POSIX one; PowerShell's equivalent
// cannot be executed in this project's Linux-only CI/dev environment
// (see shell.powershellFunctionLines's doc comment).
func TestShellIntegration_LangUseUpdatesCurrentShellPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell function test; not applicable on Windows")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}

	devHome := t.TempDir()
	binDir := t.TempDir()
	devBinary := filepath.Join(binDir, "dev")
	build := exec.Command("go", "build", "-o", devBinary, "github.com/jpsdm/dev")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building dev: %v\n%s", err, out)
	}

	versionBin := filepath.Join(devHome, "versions", "node", "22", "bin")
	if err := os.MkdirAll(versionBin, 0o755); err != nil {
		t.Fatal(err)
	}

	functionScript := strings.Join(shell.FunctionLines(shell.Bash, devHome), "\n")
	script := functionScript + "\n" +
		"dev lang install node 22 >/dev/null 2>&1 || true\n" + // best-effort; the fake version dir above already exists
		"dev lang use node 22\n" +
		"echo \"PATH_AFTER=$PATH\"\n"

	scriptPath := filepath.Join(t.TempDir(), "test.sh")
	if err := filesystem.WriteFileAtomic(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DEV_HOME="+devHome,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("running script: %v\n%s", err, out.String())
	}

	found := false
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "PATH_AFTER=") {
			if strings.Contains(line, versionBin) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected the subprocess's own PATH to contain %q after `dev lang use`, got:\n%s", versionBin, out.String())
	}
}

// repoRoot resolves the module root from the current test binary's
// working directory (cmd/), so `go build` targets the real module
// regardless of which directory `go test` happens to run from.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd) // cmd/ -> module root
}
```

`dev lang install node 22` genuinely attempting a network install would be slow/flaky in CI, so the script pre-creates `versions/node/22/bin` directly (matching how `node_test.go`'s own `Activate`/`Uninstall` tests already fake an "installed" version without a real download) and lets the install line fail harmlessly (`|| true`) — only `dev lang use`, which only needs the marker file and an existing version directory, is under test here.

- [ ] **Step 2: Write the exit-code-preservation test**

Add to `cmd/shellintegration_test.go`:

```go
// TestShellIntegration_FailedLangUsePreservesExitCode pins the spec's
// error-handling requirement: "dev lang use nonexistent-version"'s
// failure must not be masked by the function's trailing `dev env`
// re-eval, which (being pure local computation) always succeeds and
// would otherwise make the function return 0 regardless of whether
// the real subcommand failed.
func TestShellIntegration_FailedLangUsePreservesExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell function test; not applicable on Windows")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}

	devHome := t.TempDir()
	binDir := t.TempDir()
	devBinary := filepath.Join(binDir, "dev")
	build := exec.Command("go", "build", "-o", devBinary, "github.com/jpsdm/dev")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building dev: %v\n%s", err, out)
	}

	functionScript := strings.Join(shell.FunctionLines(shell.Bash, devHome), "\n")
	script := functionScript + "\n" +
		"dev lang use node does-not-exist\n" +
		"echo \"EXIT_STATUS=$?\"\n"

	scriptPath := filepath.Join(t.TempDir(), "test.sh")
	if err := filesystem.WriteFileAtomic(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DEV_HOME="+devHome,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	// The script itself always exits 0 (it ends with a successful
	// echo) — what's under test is the EXIT_STATUS value the function
	// captured from the failed `dev lang use`, not cmd.Run()'s own
	// error.
	if err := cmd.Run(); err != nil {
		t.Fatalf("running script: %v\n%s", err, out.String())
	}

	if strings.Contains(out.String(), "EXIT_STATUS=0") {
		t.Fatalf("expected a non-zero exit status from the failed `dev lang use` to survive the function's trailing `dev env` re-eval, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "EXIT_STATUS=") {
		t.Fatalf("expected the script to reach its EXIT_STATUS echo, got:\n%s", out.String())
	}
}
```

- [ ] **Step 3: Run both tests**

Both tests exercise behavior already built in Tasks 1-3 (`FunctionLines`, `dev env`, `dev lang use`) rather than driving new implementation, so — unlike this plan's other TDD steps — both are expected to pass on first run, acting as this task's own real end-to-end regression check.

Run: `go test ./cmd/... -run TestShellIntegration -v`
Expected: PASS for both `TestShellIntegration_LangUseUpdatesCurrentShellPath` and `TestShellIntegration_FailedLangUsePreservesExitCode`. If either fails, the most likely causes are: `dev lang use`'s `ValidVersionName`/`Install`-path assumptions not matching a hand-created version directory (check `cmd/lang.go`'s `langUseCmd`, unchanged by this plan — it only requires the version directory to exist, which the test creates directly), or a quoting mismatch in `shell.FunctionLines`'s literal output versus what `sh` accepts (compare against Task 1's `TestFunctionLines_Posix` expectation, which is the same literal text).

- [ ] **Step 4: Run the full suite one more time**

Run: `make check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/shellintegration_test.go
git commit -m "test: add a real end-to-end shell-function PATH integration test"
```

---

## Task 6: Documentation — README, CONTRIBUTING, migration note

**Files:**
- Modify: `README.md`
- Modify: `CONTRIBUTING.md`

**Interfaces:**
- Consumes: nothing.
- Produces: nothing other agents depend on — pure documentation.

- [ ] **Step 1: Rewrite README's install section**

In `README.md`, find the numbered install-steps list (around the existing lines describing `$DEV_HOME/bin` shims and the two PATH entries, near "populates `$DEV_HOME/bin` with the shims" and "puts both entries (`$DEV_HOME` and `$DEV_HOME/bin`) on your `PATH`"). Rewrite that step to describe the new mechanism:

```markdown
2. `dev setup` detects your shell and, with your confirmation,
   installs a small `dev` shell function (not a separate binary) into
   your shell's rc file. The function runs the real `dev` command,
   then — only after `dev lang`/`dev l` — refreshes `PATH` in your
   current shell so a newly-activated version takes effect
   immediately, no restart needed;
3. puts `$DEV_HOME` on your `PATH`, where `dev` itself lives, and adds
   each language's currently-active version's own `bin` directory
   directly — no copies, no symlinks.
```

(Match the surrounding numbered-list formatting already in the file; adjust numbering if the file's existing steps don't align exactly with this plan's description — read the current numbered list before editing, since this plan's summary of it may not match line-for-line.)

- [ ] **Step 2: Rewrite the "PATH and shims" section**

Rename the section header from `## PATH and shims` to `## PATH management`, and replace its body (the paragraphs describing `active/<language>`, `no-active/<language>` placeholders, the friendly "not active" message, and the Java/system-JDK interaction) with:

```markdown
## PATH management

    dev env      # print the current PATH export lines for your shell
    dev setup    # detect your shell, confirm, and install the dev function

`dev setup` installs a `dev` shell function (not a separate binary) into
your shell's rc file. The function always runs the real `dev` command
first; afterward, only for `dev lang`/`dev l` invocations, it re-evaluates
a fresh `dev env` in your **current shell**, so `dev lang use`/`dev lang
uninstall` take effect immediately — no restart, no sourcing anything by
hand.

`dev env` computes `PATH` fresh every time it runs: it starts from your
shell's current `PATH`, strips any entry under `$DEV_HOME/versions/...`
(so a previous run's own entries never accumulate), then prepends
`$DEV_HOME` and each registered language's currently-active version's real
`bin` directory. There are no copied binaries, symlinks, or junctions
anywhere in this — `node`, `java`, `python`, etc. resolve on `PATH`
straight to the real binary inside `versions/<lang>/<version>/...`. A
tool installed later by a language's own package manager (`npm install -g
pnpm`, a `pip`-installed console script) is reachable the moment it's
installed, since it lands inside that same active version's `bin`
directory.

Running a command for a language with no active version is ordinary shell
"command not found" — there's no `dev`-provided binary standing in to
print a friendlier message, the same as `nvm`/`mise` without shims.

**Upgrading from a `v0.7.0` or earlier install:** run `dev setup` once to
replace your rc file's old static `PATH` lines with the new function.
Until you do, `dev` keeps working exactly as it did before (the old
shim/symlink mechanism stays in place, untouched) — nothing here breaks
an un-migrated shell. The old `$DEV_HOME/bin`, `$DEV_HOME/active/*`, and
`$DEV_HOME/no-active/*` directories become unused once you do migrate;
they aren't deleted automatically and are safe to remove by hand.
```

Delete the paragraph below it describing Windows registry PATH ordering with the JDK specifically in terms of `active/java` (search for "On a machine with a system-installed JDK") — replace any reference to `$DEV_HOME/active/java` there with `$DEV_HOME` (the active Java version's `bin` directory is now just one of the entries `dev env` prepends ahead of the system `PATH`, not a separate always-present placeholder-or-real link); keep the rest of that paragraph's substance (system JDK precedence/registry ordering) otherwise intact if it still applies conceptually — read it in place before editing, since this plan's description may not capture its exact current wording.

- [ ] **Step 3: Update CONTRIBUTING.md**

At the line referencing `internal/shim` (around "the runtime dispatch that makes `node`, `java`, `go`, ... work"), remove that bullet entirely — there is no such package anymore.

At the line referencing `cmd/lang.go` or `internal/shim` being "generic over the registry" (around "needs zero changes to `cmd/lang.go` or `internal/shim`"), replace `internal/shim` with `internal/shell` — adding a provider still needs zero changes to the mechanism that puts it on `PATH`, just now that's `internal/shell`'s `ComputePathEntries`/`cmd/env.go`'s `activeBinDirs`, which iterate `langManager.Names()` generically.

- [ ] **Step 4: Also update `CLAUDE.md`'s `Runtime` interface listing**

In `/var/home/jpsdm/Dev/src/github.com/jpsdm/godev/CLAUDE.md`'s "Architecture: the provider pattern" section, the `Runtime` interface code block still lists `ShimNames() []string`. Remove that line from the interface listing so the doc matches the real interface after Task 3.

- [ ] **Step 5: Commit**

```bash
git add README.md CONTRIBUTING.md CLAUDE.md
git commit -m "docs: describe the shell-function PATH mechanism, replacing shim/placeholder docs"
```
