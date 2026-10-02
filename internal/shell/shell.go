// Package shell detects the user's shell, generates the PATH export
// lines dev needs, and can idempotently insert/replace a marked block in
// a shell rc file.
package shell

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/jpsdm/dev/internal/filesystem"
)

// Shell identifies a supported shell.
type Shell int

const (
	Unknown Shell = iota
	Bash
	Zsh
	Fish
	PowerShell
)

const (
	blockBegin = "# BEGIN dev shell setup"
	blockEnd   = "# END dev shell setup"
)

// Detect identifies the current shell. It prefers the actual process
// that launched this invocation of dev (see parentShellDetector) over
// $SHELL, which is only the user's configured *login* shell — a value
// that's often stale or simply wrong for anyone who runs a different
// shell day to day (they switched via `chsh` but the record wasn't
// updated, their terminal profile launches a shell directly regardless
// of the login shell, etc.). A real, running shell process is strictly
// better evidence of "what shell is actually asking" than an inherited
// env var; $SHELL remains the fallback when the parent process can't
// be determined or isn't a shell this project recognizes. On Windows,
// where neither signal is conclusive (e.g. dev launched from Explorer,
// a VS Code task, or Windows Terminal itself rather than a shell
// directly), the final fallback is PowerShell — it ships with every
// Windows install, unlike Bash.
func Detect() Shell {
	if sh, ok := parentShellDetector(); ok {
		return sh
	}
	if sh := shellFromEnv(os.Getenv("SHELL")); sh != Unknown {
		return sh
	}
	if runtime.GOOS == "windows" {
		return PowerShell
	}
	return Unknown
}

// parentShellDetector backs Detect()'s parent-process lookup; a
// package-level var so tests can force a deterministic answer
// regardless of whatever process actually spawned `go test` on a
// given machine (a shell, a CI runner, make, ...) — the same pattern
// this project already uses for other OS/hardware boundaries `go
// test` can't otherwise control (platform.Executable, cliutil's
// isTerminalFunc).
var parentShellDetector = func() (Shell, bool) {
	return parentShellName(runtime.GOOS, os.Getppid())
}

// shellFromEnv maps a $SHELL-style path (e.g. "/bin/bash") to a
// Shell, or Unknown if it doesn't match one this project recognizes.
func shellFromEnv(shellPath string) Shell {
	sh, _ := shellFromName(filepath.Base(shellPath))
	return sh
}

// shellFromName maps a bare shell executable name (as reported by
// $SHELL's basename or a process's own name) to a Shell. false means
// name isn't one this project recognizes — including Shell's own
// zero value, Unknown, which this never returns as a "match".
func shellFromName(name string) (Shell, bool) {
	switch name {
	case "bash":
		return Bash, true
	case "zsh":
		return Zsh, true
	case "fish":
		return Fish, true
	case "powershell", "pwsh":
		// "pwsh" is PowerShell 7+ (cross-platform); "powershell" is
		// Windows PowerShell 5.1, Windows-only but still the default
		// there on many machines.
		return PowerShell, true
	default:
		return Unknown, false
	}
}

// parentShellName resolves pid's process name (typically
// os.Getppid(), the process that actually launched this invocation)
// and reports whether it matches a shell this project recognizes.
// false means inconclusive — a lookup failure, an unsupported
// platform, or a real process that simply isn't a known shell (e.g.
// dev invoked from a Makefile or another program) — callers must fall
// back to another signal (Detect falls back to $SHELL), not treat
// false as "no shell, definitely."
func parentShellName(goos string, pid int) (Shell, bool) {
	name, ok := parentCommName(goos, pid)
	if !ok {
		return Unknown, false
	}
	return shellFromName(name)
}

// parentCommName resolves the raw process-name string for pid,
// dispatching by goos to the platform-appropriate lookup. false means
// the lookup itself was inconclusive (unsupported platform, permission
// error, the process is gone) — this says nothing about what the name
// would have meant.
func parentCommName(goos string, pid int) (string, bool) {
	switch goos {
	case "linux":
		return parentCommNameLinux(pid)
	case "darwin":
		return parentCommNameDarwin(pid)
	case "windows":
		return parentCommNameWindows(pid)
	default:
		return "", false
	}
}

// parentCommNameLinux reads /proc/<pid>/comm, which the kernel always
// populates with the bare executable name (never a path, unlike
// macOS's `ps`) for any process that still exists.
func parentCommNameLinux(pid int) (string, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return "", false
	}
	return parseCommOutput(string(data))
}

// parentCommNameDarwin resolves pid's process name via `ps` — macOS
// has no /proc filesystem. Uses an argument slice, never a shell
// string, with pid (an int this package itself computed, never
// user-controlled input) as the only variable part.
func parentCommNameDarwin(pid int) (string, bool) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return "", false
	}
	return parseCommOutput(string(out))
}

// parseCommOutput trims a raw comm/ps result and reduces it to a bare
// executable name — macOS's `ps -o comm=` can report a full path
// (e.g. "/usr/local/bin/fish") depending on how the process was
// invoked, unlike Linux's /proc/<pid>/comm, which never does; this
// normalizes both the same way. Blank output (whitespace-only) is
// reported as inconclusive, not as a real empty name.
func parseCommOutput(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	return filepath.Base(trimmed), true
}

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

// ExportLines returns the lines dev env prints, in sh's own syntax,
// given pathEntries (see ComputePathEntries) as the full, already-
// computed PATH value, in order. Unknown falls back to POSIX
// sh-compatible syntax (the same as Bash/Zsh), since that's the most
// broadly interpretable default when the shell couldn't be identified.
func ExportLines(sh Shell, devHome string, pathEntries []string) []string {
	switch sh {
	case Fish:
		// Every entry is quoted individually: fish's "set -gx PATH"
		// takes a list, one token per entry, so the whole
		// space-joined value cannot be quoted as one string the way
		// POSIX's single ":"-joined value can.
		return []string{
			fmt.Sprintf("set -gx DEV_HOME %s", fishQuote(devHome)),
			fmt.Sprintf("set -gx PATH %s", strings.Join(quoteEachFish(pathEntries), " ")),
		}
	case PowerShell:
		// Not Go's %q: it escapes backslashes as \\, which is wrong
		// inside a PowerShell double-quoted string (a literal Windows
		// path like C:\Users\foo\.dev must not be backslash-escaped
		// there) — see escapePowerShellDoubleQuoted for what does need
		// escaping there and why.
		return []string{
			fmt.Sprintf(`$env:DEV_HOME = "%s"`, escapePowerShellDoubleQuoted(devHome)),
			fmt.Sprintf(`$env:PATH = "%s"`, escapePowerShellDoubleQuoted(strings.Join(pathEntries, ";"))),
		}
	case Unknown:
		return append([]string{
			"# dev could not detect your shell ($SHELL was not recognized); using POSIX sh syntax, which may not be correct for yours",
		}, posixExportLines(devHome, pathEntries)...)
	default: // Bash, Zsh
		return posixExportLines(devHome, pathEntries)
	}
}

// posixExportLines single-quotes the whole joined PATH value, exactly
// as it does DEV_HOME. Double quotes would be wrong here, not merely
// inconsistent: these lines are deliberately fed to the installed
// function's `eval "$(command dev env)"`, and POSIX double quotes do
// not suppress `backtick` or $(...) command substitution — only single
// quotes do. A PATH entry containing a backtick (a legal directory
// name) would otherwise be *executed* by that eval.
func posixExportLines(devHome string, pathEntries []string) []string {
	return []string{
		fmt.Sprintf(`export DEV_HOME=%s`, shellQuote(devHome)),
		fmt.Sprintf(`export PATH=%s`, shellQuote(strings.Join(pathEntries, ":"))),
	}
}

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

// posixFunctionLines ends with a one-time `eval "$(command dev env)"`,
// outside the function body, so a freshly started shell gets the active
// version of every language on PATH at rc-source time — the same timing
// the prior design's static `export PATH=...` lines had, just computed
// dynamically now. Without it, PATH would only ever be correct in a
// shell that had itself run `dev lang ...`: open a new terminal after
// activating a version and `node` would not resolve, despite
// `current/node` still naming it. This is explicitly NOT a per-prompt
// hook (ruled out by the spec's non-goals) — it runs exactly once per
// new shell, mirroring nvm.sh's own trailing `nvm use default`.
//
// `"${1-}"`, not `"$1"`: a bare `dev` with no arguments under `set -u`
// would otherwise abort with "$1: unbound variable" (and in dash, exit
// the shell outright).
func posixFunctionLines(devHome string) []string {
	return []string{
		fmt.Sprintf(`export DEV_HOME=%s`, shellQuote(devHome)),
		`export PATH="$DEV_HOME:$PATH"`,
		`dev() {`,
		`    command dev "$@"`,
		`    local status=$?`,
		`    case "${1-}" in`,
		`        lang|l) eval "$(command dev env)" ;;`,
		`    esac`,
		`    return $status`,
		`}`,
		`eval "$(command dev env)"`,
	}
}

// fishFunctionLines is posixFunctionLines's Fish equivalent, including
// its trailing one-time eval (see that function's doc comment for why
// it's there and why it is not a per-prompt hook). "string collect"
// gathers dev env's multi-line stdout into a single argument
// (preserving embedded newlines) before handing it to eval — fish's
// eval otherwise joins multiple arguments with spaces, which would
// mangle a multi-statement script into one broken line.
//
// The captured exit status is named devStatus, not status: `status` is
// one of fish's own reserved special variables, and `set -l status
// $status` fails outright with "Tried to modify the special variable
// 'status' with the wrong scope" on every single `dev` invocation. The
// subsequent `return $status` then reflected whatever the *last*
// command in the function left behind rather than the real dev exit
// code, so a failed `dev lang use` exited 0 under fish — exactly the
// masking the spec's error-handling section forbids.
func fishFunctionLines(devHome string) []string {
	return []string{
		fmt.Sprintf("set -gx DEV_HOME %s", fishQuote(devHome)),
		`set -gx PATH $DEV_HOME $PATH`,
		`function dev`,
		`    command dev $argv`,
		`    set -l devStatus $status`,
		`    switch "$argv[1]"`,
		`        case lang l`,
		`            eval (command dev env | string collect)`,
		`    end`,
		`    return $devStatus`,
		`end`,
		`eval (command dev env | string collect)`,
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
// environment is Linux-only.
func powershellFunctionLines(devHome string) []string {
	return []string{
		fmt.Sprintf(`$env:DEV_HOME = "%s"`, escapePowerShellDoubleQuoted(devHome)),
		`$env:PATH = "$env:DEV_HOME;$env:PATH"`,
		`function dev {`,
		`    & "$env:DEV_HOME\dev.exe" @args`,
		`    $exitStatus = $LASTEXITCODE`,
		`    if ($args.Count -gt 0 -and ($args[0] -eq "lang" -or $args[0] -eq "l")) {`,
		"        (& \"$env:DEV_HOME\\dev.exe\" env) -join \"`n\" | Invoke-Expression",
		`    }`,
		`    $global:LASTEXITCODE = $exitStatus`,
		`}`,
		"(& \"$env:DEV_HOME\\dev.exe\" env) -join \"`n\" | Invoke-Expression",
	}
}

// shellQuote wraps s in single quotes, escaping any embedded single
// quote using the classic POSIX trick: end the quoted string, emit a
// backslash-escaped quote, then reopen the quoted string.
// Single-quoted strings suppress $ variable and `command` substitution
// in bash, zsh, and fish alike — Go's %q is string-literal quoting, not
// shell quoting, and leaves $ and backticks unescaped inside a
// double-quoted string. They do not, however, suppress interactive
// bash/zsh's "!" history expansion (quote-unaware, on by default in an
// interactive shell), so a DEV_HOME value containing "!" could still
// trigger an "event not found" error when this line is sourced
// interactively — a pre-existing limitation, not one this function
// claims to solve.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fishQuote is shellQuote's Fish equivalent. Fish needs its own
// function rather than reusing shellQuote because its single-quoted
// strings are not POSIX's: fish honours \' and \\ as escapes *inside*
// single quotes, where POSIX honours none at all. Verified against
// fish 4.6.0 on this project's development machine: shellQuote's POSIX
// close-quote/escaped-quote/reopen-quote output does parse correctly
// under fish for a value containing a single quote, a space, a
// backtick, a $(...), or a double quote (fish concatenates adjacent
// quoted tokens the same way POSIX does) — but it breaks for a value
// containing a backslash, which fish then eats: 'a\\b' yields a\b
// under fish versus a\\b under sh, and a value ending in a single
// backslash produces an unterminated-string parse error outright. So
// fish gets backslash-escaping, which fish's own rules require and
// POSIX's forbid. Like shellQuote, this suppresses $ variable and
// (command) substitution entirely.
func fishQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "'", `\'`)
	return "'" + s + "'"
}

// quoteEachFish fishQuotes every entry individually, for the one place
// a list of values must each survive as a separate shell token: fish's
// `set -gx PATH entry1 entry2 ...`.
func quoteEachFish(entries []string) []string {
	quoted := make([]string, len(entries))
	for i, e := range entries {
		quoted[i] = fishQuote(e)
	}
	return quoted
}

// escapePowerShellDoubleQuoted escapes the three characters that are
// special inside a PowerShell double-quoted string: the backtick
// escape character itself, "$" (which introduces $variable and
// $(expression) interpolation — the PowerShell equivalent of the POSIX
// command substitution shellQuote exists to suppress), and the closing
// double quote. Backtick must be replaced first, or the backticks the
// later two replacements insert would themselves be escaped.
//
// Backslashes are deliberately left alone: a literal Windows path like
// C:\Users\foo\.dev must not be backslash-escaped inside a PowerShell
// double-quoted string (unlike in Go's %q or a POSIX double-quoted
// string).
//
// Unverified against a real PowerShell host — this project's
// development environment is Linux-only.
func escapePowerShellDoubleQuoted(s string) string {
	s = strings.ReplaceAll(s, "`", "``")
	s = strings.ReplaceAll(s, "$", "`$")
	s = strings.ReplaceAll(s, `"`, "`\"")
	return s
}

// RCPath returns the rc file dev setup would edit for sh, and whether
// automatic editing is supported for it. For PowerShell, this asks a
// real PowerShell process for its own $PROFILE value (see
// powershellProfilePath) — the only authoritative way to know it,
// since the profile path depends on the PowerShell version, how it
// was installed, and which host is running it. PowerShell is
// "supported" only when that lookup actually succeeds; when neither
// pwsh nor powershell is reachable at all, it falls back to
// unsupported the same as Unknown, which is always unsupported —
// there's no rc file to safely guess for an unidentified shell. A
// non-nil error means something else went wrong (today, only
// "couldn't resolve the home directory", which the PowerShell path
// never hits) and is distinct from "supported = false": callers must
// not treat the two the same, since one is an unactionable but
// deliberately-detected outcome and the other is a real, reportable
// failure.
func RCPath(sh Shell) (path string, supported bool, err error) {
	if sh == PowerShell {
		if p, ok := powershellProfilePath(); ok {
			return p, true, nil
		}
		return "", false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("resolving home directory: %w", err)
	}
	return rcPathForHome(sh, home, runtime.GOOS)
}

// rcPathForHome does RCPath's actual mapping for every shell except
// PowerShell (handled directly in RCPath, since it needs to run a
// real process rather than compute a path from home/goos alone),
// taking home and goos as parameters so the darwin-specific Bash
// branch is testable without requiring the test itself to run on
// macOS.
func rcPathForHome(sh Shell, home, goos string) (path string, supported bool, err error) {
	switch sh {
	case Bash:
		// macOS's Terminal.app starts login shells, which read
		// ~/.bash_profile, not ~/.bashrc — targeting .bashrc there
		// would report success while doing nothing observable.
		if goos == "darwin" {
			return filepath.Join(home, ".bash_profile"), true, nil
		}
		return filepath.Join(home, ".bashrc"), true, nil
	case Zsh:
		return filepath.Join(home, ".zshrc"), true, nil
	case Fish:
		return filepath.Join(home, ".config", "fish", "config.fish"), true, nil
	default: // Unknown (and PowerShell, defensively, though RCPath never reaches here with it)
		return "", false, nil
	}
}

// powershellProfilePath asks a real PowerShell process for $PROFILE's
// value. A package-level var (not a direct call) so tests can
// override it with a deterministic answer regardless of whether this
// machine has PowerShell installed at all — the same seam pattern
// this project already uses for other OS boundaries go test can't
// otherwise control (platform.Executable, parentShellDetector).
// Production code always uses defaultPowershellProfilePath.
var powershellProfilePath = defaultPowershellProfilePath

// defaultPowershellProfilePath tries "pwsh" (PowerShell 7+) before
// falling back to "powershell" (Windows PowerShell 5.1) — a machine
// with both installed is more likely to actually use pwsh day to day.
// ok is false when neither executable is reachable, or runs but
// prints nothing usable (this binary's own test run on a non-Windows
// machine with no PowerShell at all is the common case here) —
// callers must treat that as "can't auto-detect the profile path",
// falling back to printing the lines for manual setup, not as an
// error.
func defaultPowershellProfilePath() (path string, ok bool) {
	for _, exe := range []string{"pwsh", "powershell"} {
		out, err := exec.Command(exe, "-NoProfile", "-NonInteractive", "-Command", "$PROFILE").Output()
		if err != nil {
			continue
		}
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" {
			continue
		}
		return trimmed, true
	}
	return "", false
}

// Configurable returns every shell dev setup knows how to configure, in
// a fixed, deterministic order (so prompts always appear in the same
// order across runs). Unknown is deliberately excluded: it isn't a
// concrete shell dev could write an rc file for.
func Configurable() []Shell {
	return []Shell{Bash, Zsh, Fish, PowerShell}
}

// lookupNames pairs each Configurable shell with the PATH binary
// name(s) that indicate it's installed on this machine. PowerShell
// lists both names because either a Windows PowerShell 5.1 ("powershell")
// or a PowerShell 7+ ("pwsh") install counts as present.
var lookupNames = map[Shell][]string{
	Bash:       {"bash"},
	Zsh:        {"zsh"},
	Fish:       {"fish"},
	PowerShell: {"pwsh", "powershell"},
}

// LookPath is exec.LookPath, as a package-level var so tests (in this
// package and in cmd) can control which shells appear "installed"
// without depending on whatever is actually on PATH on the machine
// running `go test` — the same seam pattern as platform.Executable.
var LookPath = exec.LookPath

// IsPresent reports whether sh appears to be installed on this
// machine: any of its lookupNames resolves via LookPath, or sh is what
// Detect() reports for the current process (covers an install that's
// genuinely running right now but not found via LookPath, e.g. an
// unusual install location not on PATH).
func IsPresent(sh Shell) bool {
	for _, name := range lookupNames[sh] {
		if _, err := LookPath(name); err == nil {
			return true
		}
	}
	return Detect() == sh
}

// BlockUpToDate reports whether path already contains exactly the
// block UpsertBlock(path, lines) would write — a missing file counts
// as "not up to date," never as an error. This is what lets dev setup
// silently skip a shell it already configured on a prior run, so a
// second run only surfaces what's actually new.
func BlockUpToDate(path string, lines []string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	block := strings.Join(append(append([]string{blockBegin}, lines...), blockEnd), "\n")
	return strings.Contains(string(data), block), nil
}

// UpsertBlock writes lines into path between a marker pair, replacing an
// existing block if one is present or appending a new one if not.
// Creates path's parent directory and the file itself if neither exists.
func UpsertBlock(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", path, err)
	}

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	block := append([]string{blockBegin}, lines...)
	block = append(block, blockEnd)

	var out []string
	if len(existing) > 0 {
		fileLines := strings.Split(strings.TrimRight(string(existing), "\n"), "\n")
		inBlock := false
		replaced := false
		for _, line := range fileLines {
			switch {
			case line == blockBegin:
				inBlock = true
				out = append(out, block...)
				replaced = true
			case line == blockEnd:
				inBlock = false
			case inBlock:
				// skip old block content
			default:
				out = append(out, line)
			}
		}
		if inBlock {
			return fmt.Errorf("%s has a %q marker with no matching %q — remove or repair it by hand before running this again", path, blockBegin, blockEnd)
		}
		if !replaced {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			out = append(out, block...)
		}
	} else {
		out = block
	}

	content := strings.Join(out, "\n") + "\n"
	if err := filesystem.WriteFileAtomic(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// Confirm prompts with prompt, reads one line from in, and reports
// whether it was an affirmative response ("y" or "yes", case
// insensitive) — anything else, including EOF or an empty line, is "no".
//
// Confirm is sometimes called more than once in sequence against the
// same underlying reader within a single command invocation (e.g.
// cmd/setup.go asks up to three questions in one run). It therefore
// reads exactly one line's worth of bytes from in and no further —
// unlike a fresh bufio.Scanner (or bufio.Reader) allocated per call,
// which reads ahead into its own internal buffer and silently
// discards any unconsumed bytes when it goes out of scope, causing a
// later Confirm call against the same reader to see EOF regardless of
// what was actually typed for it.
func Confirm(prompt string, in io.Reader, out io.Writer) (bool, error) {
	fmt.Fprint(out, prompt)
	line, err := readLine(in)
	// Echoed unconditionally: an interactive terminal already shows the
	// typed answer via its own line-editing, but a piped/non-interactive
	// stdin (scripts, the acceptance test) doesn't, so without this the
	// prompt and whatever gets printed next run together on one line.
	fmt.Fprintln(out)
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("reading confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// PromptLine prompts with prompt, reads one line from in the same way
// Confirm does (see its doc comment — one line's worth of bytes and no
// further, so a later PromptLine or Confirm call against the same
// reader within one command run sees exactly the bytes meant for it),
// and returns it trimmed, or defaultValue if the trimmed line is empty
// (including on EOF).
func PromptLine(prompt string, in io.Reader, out io.Writer, defaultValue string) (string, error) {
	fmt.Fprint(out, prompt)
	line, err := readLine(in)
	fmt.Fprintln(out)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("reading input: %w", err)
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return defaultValue, nil
	}
	return trimmed, nil
}

// readLine reads a single '\n'-terminated (or EOF-terminated) line
// from r, one byte at a time. A byte-at-a-time read is deliberate: it
// guarantees r's read position stops exactly at the byte after the
// newline, so a caller that reads another line from the same r
// afterward (as Confirm's callers do) sees exactly the bytes meant
// for it, never bytes an earlier, larger buffered read already
// consumed. The returned error is io.EOF when the line was terminated
// by end of input rather than a newline (including an entirely empty
// read); any other non-nil error is a genuine read failure.
func readLine(r io.Reader) (string, error) {
	var sb strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return sb.String(), nil
			}
			sb.WriteByte(buf[0])
		}
		if err != nil {
			if err == io.EOF {
				return sb.String(), io.EOF
			}
			return sb.String(), err
		}
	}
}
