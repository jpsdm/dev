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
	// The real dev binary must live directly inside $DEV_HOME (exactly
	// where `dev setup` installs it — see cmd/setup.go's
	// relocateIfNeeded, which copies to filepath.Join(devHome,
	// filepath.Base(exe))): cmd/root.go's PersistentPreRunE refuses
	// every subcommand but setup/version/help with
	// platform.NotInstalledWarning unless
	// platform.RunningFromDevHome() reports that the running
	// executable's own directory is exactly $DEV_HOME. A binary built
	// into some other temp dir would trip that guard and never reach
	// langUseCmd at all.
	devBinary := filepath.Join(devHome, "dev")
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
		"PATH="+os.Getenv("PATH"),
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

// buildDevInto builds this branch's own dev binary directly inside
// devHome — exactly where `dev setup` installs it (see cmd/setup.go's
// relocateIfNeeded) — and returns devHome. cmd/root.go's
// PersistentPreRunE refuses every subcommand but setup/version/help
// with platform.NotInstalledWarning unless
// platform.RunningFromDevHome() reports that the running executable's
// own directory is exactly $DEV_HOME, so a binary built anywhere else
// would trip that guard and never reach the command under test.
func buildDevInto(t *testing.T, devHome string) string {
	t.Helper()
	build := exec.Command("go", "build", "-o", filepath.Join(devHome, "dev"), "github.com/jpsdm/dev")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building dev: %v\n%s", err, out)
	}
	return devHome
}

// runShellScript writes script to a temp file, runs it with shellName,
// and returns its combined output. A failure to even run the script is
// fatal; the script's own exit status is not inspected (several tests
// here deliberately assert on a status the script itself echoes).
func runShellScript(t *testing.T, shellName, script, devHome string) string {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "test."+shellName)
	if err := filesystem.WriteFileAtomic(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shellName, scriptPath)
	cmd.Env = append(os.Environ(),
		"PATH="+os.Getenv("PATH"),
		"DEV_HOME="+devHome,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("running %s script: %v\n%s", shellName, err, out.String())
	}
	return out.String()
}

// requireShell skips the test unless shellName is on PATH (fish in
// particular is not installed everywhere, including some CI images).
func requireShell(t *testing.T, shellName string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX/fish shell function test; not applicable on Windows")
	}
	if _, err := exec.LookPath(shellName); err != nil {
		t.Skipf("no %s on PATH", shellName)
	}
}

// TestShellIntegration_FreshShellPicksUpPreviouslyActivatedVersion pins
// the Critical finding the final whole-branch review caught by opening
// a second terminal: a version activated in one shell session must
// already be on PATH in the NEXT shell, which only ever *sources* the
// rc-file block and never runs a `dev lang` command of its own. Before
// the fix, the installed block defined the wrapper function but nothing
// evaluated `dev env` at source time, so `node` silently did not
// resolve in a fresh shell despite `current/node` still naming a
// version — breaking the spec's unconditional Goal 1 and contradicting
// `dev setup`'s own "restart your shell" message.
//
// The current/<lang> marker is written directly here rather than via a
// real `dev lang use`: the marker file IS what Activate leaves behind,
// and writing it directly is what makes this a test of a *previous*
// session's state rather than of this script's own.
func TestShellIntegration_FreshShellPicksUpPreviouslyActivatedVersion(t *testing.T) {
	requireShell(t, "sh")

	devHome := buildDevInto(t, t.TempDir())

	versionBin := filepath.Join(devHome, "versions", "node", "22", "bin")
	if err := os.MkdirAll(versionBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Exactly what Activate leaves behind for "node 22".
	if err := filesystem.WriteFileAtomic(filepath.Join(devHome, "current", "node"), []byte("22"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Note what this script does NOT do: it never calls `dev lang`, or
	// `dev` at all. Sourcing the block is the whole exercise.
	script := strings.Join(shell.FunctionLines(shell.Bash, devHome), "\n") + "\n" +
		"echo \"PATH_AFTER_SOURCE=$PATH\"\n"

	out := runShellScript(t, "sh", script, devHome)

	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "PATH_AFTER_SOURCE=") && strings.Contains(line, versionBin) {
			return
		}
	}
	t.Fatalf("a freshly started shell that only sourced the block does not have the already-active version's bin dir %q on PATH, got:\n%s", versionBin, out)
}

// TestShellIntegration_Fish_LangUseUpdatesCurrentShellPath is
// TestShellIntegration_LangUseUpdatesCurrentShellPath's Fish
// counterpart. Task 5 covered only POSIX, which is precisely why fish's
// `set -l status $status` reserved-variable collision (see
// shell.fishFunctionLines) shipped unnoticed through three
// individually-approved task reviews: nothing ever executed the fish
// block.
func TestShellIntegration_Fish_LangUseUpdatesCurrentShellPath(t *testing.T) {
	requireShell(t, "fish")

	devHome := buildDevInto(t, t.TempDir())

	versionBin := filepath.Join(devHome, "versions", "node", "22", "bin")
	if err := os.MkdirAll(versionBin, 0o755); err != nil {
		t.Fatal(err)
	}

	script := strings.Join(shell.FunctionLines(shell.Fish, devHome), "\n") + "\n" +
		"dev lang use node 22\n" +
		"echo \"PATH_AFTER=$PATH\"\n"

	out := runShellScript(t, "fish", script, devHome)

	// fish's reserved-variable error is a *message*, not a failed exit
	// status, so it has to be asserted against explicitly — the bug it
	// signals is exactly what this test exists to catch.
	if strings.Contains(out, "special variable") {
		t.Errorf("the fish function body modified one of fish's own reserved variables:\n%s", out)
	}

	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "PATH_AFTER=") && strings.Contains(line, versionBin) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the fish subprocess's own PATH to contain %q after `dev lang use`, got:\n%s", versionBin, out)
	}
}

// TestShellIntegration_Fish_FailedLangUsePreservesExitCode is the test
// whose absence let the fish `status` collision ship: with `set -l
// status $status` failing outright, `return $status` reflected the last
// command's status rather than dev's, so a failed `dev lang use` exited
// 0 under fish — the exact masking the spec's error-handling section
// forbids, and which the POSIX-only
// TestShellIntegration_FailedLangUsePreservesExitCode could not see.
func TestShellIntegration_Fish_FailedLangUsePreservesExitCode(t *testing.T) {
	requireShell(t, "fish")

	devHome := buildDevInto(t, t.TempDir())

	script := strings.Join(shell.FunctionLines(shell.Fish, devHome), "\n") + "\n" +
		"dev lang use node does-not-exist\n" +
		"echo \"EXIT_STATUS=$status\"\n"

	out := runShellScript(t, "fish", script, devHome)

	if strings.Contains(out, "special variable") {
		t.Errorf("the fish function body modified one of fish's own reserved variables:\n%s", out)
	}
	if strings.Contains(out, "EXIT_STATUS=0") {
		t.Fatalf("expected a non-zero $status from the failed `dev lang use` to survive the fish function's trailing `dev env` re-eval, got:\n%s", out)
	}
	if !strings.Contains(out, "EXIT_STATUS=") {
		t.Fatalf("expected the script to reach its EXIT_STATUS echo, got:\n%s", out)
	}
}

// TestShellIntegration_PathEntryWithBacktickDoesNotExecute proves,
// through real shell execution rather than Go string assertions, that
// the eval sink `dev env`'s output is deliberately fed to cannot be
// turned into command execution by a PATH entry's own contents. The
// final review demonstrated the bug by getting a sentinel file created
// from a backtick in a directory name while `export PATH="..."` was
// double-quoted (POSIX double quotes suppress neither `backtick` nor
// $(...) substitution); this is that demonstration inverted to prove
// the single-quoting fix closes it.
//
// The adversarial entries are injected through the inherited PATH that
// `dev env` reads and re-emits, rather than through a real `dev lang
// use` — the entries end up on the exact same ExportLines code path
// either way, and this reaches it without needing a crafted
// versions/<lang>/<version> directory name that ValidVersionName would
// (correctly) now reject.
func TestShellIntegration_PathEntryWithBacktickDoesNotExecute(t *testing.T) {
	requireShell(t, "sh")

	devHome := buildDevInto(t, t.TempDir())

	// t.TempDir() can't produce these names itself, so they're built by
	// hand under it.
	//
	// Deliberately NOT in this list: an entry containing a double quote.
	// Verified empirically against the pre-fix code — a `"` makes the
	// old `export PATH="..."` line unparseable, and a shell parses an
	// eval'd string in full before executing any of it, so the backtick
	// never ran and this test silently stopped proving anything (it
	// passed against the very code it exists to catch). The `"` case is
	// exercised separately below, after the injection assertion.
	workDir := t.TempDir()
	sentinel := filepath.Join(workDir, "PWNED")
	adversarial := []string{
		filepath.Join(workDir, "`touch "+sentinel+"`", "bin"),
		filepath.Join(workDir, "$(touch "+sentinel+")", "bin"),
		filepath.Join(workDir, "a space", "bin"),
		filepath.Join(workDir, "it's", "bin"),
	}
	quoteEntry := filepath.Join(workDir, `a"quote`, "bin")
	for _, dir := range append(append([]string{}, adversarial...), quoteEntry) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// The test script's own PATH assignment has to be single-quoted for
	// the same reason the code under test does: these entry names would
	// otherwise break (or execute in) this script before `dev env` is
	// ever reached.
	block := strings.Join(shell.FunctionLines(shell.Bash, devHome), "\n") + "\n"
	script := block +
		"PATH=\"$PATH:\"" + posixSingleQuote(strings.Join(adversarial, ":")) + "\n" +
		"eval \"$(command dev env)\"\n" +
		"echo \"PATH_AFTER=$PATH\"\n"

	out := runShellScript(t, "sh", script, devHome)

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("a PATH entry's embedded command substitution was EXECUTED by the eval of `dev env`'s output — the sentinel %q exists. Output:\n%s", sentinel, out)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", sentinel, err)
	}

	// Closing the injection must not have mangled the values either:
	// every adversarial entry has to survive the round trip byte-exact.
	assertPathAfterContains(t, out, adversarial)

	// The double-quote entry, on its own run, so a parse failure here
	// can't mask the injection assertion above.
	quoteOut := runShellScript(t, "sh", block+
		"PATH=\"$PATH:\""+posixSingleQuote(quoteEntry)+"\n"+
		"eval \"$(command dev env)\"\n"+
		"echo \"PATH_AFTER=$PATH\"\n", devHome)
	assertPathAfterContains(t, quoteOut, []string{quoteEntry})
}

// assertPathAfterContains checks every want against the PATH_AFTER=
// line of a script's output, proving each entry survived a `dev env`
// round trip byte-exact.
func assertPathAfterContains(t *testing.T, out string, want []string) {
	t.Helper()
	var pathAfter string
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, "PATH_AFTER="); ok {
			pathAfter = v
		}
	}
	if pathAfter == "" {
		t.Fatalf("script never reached its PATH_AFTER echo, got:\n%s", out)
	}
	for _, dir := range want {
		if !strings.Contains(pathAfter, dir) {
			t.Errorf("PATH entry %q did not survive the dev env round trip intact; got PATH_AFTER=%q", dir, pathAfter)
		}
	}
}

// TestShellIntegration_Fish_PathEntryWithSpaceStaysOneEntry is the Fish
// half of the same finding: fish's `set -gx PATH` takes a list, and the
// pre-fix code emitted every entry unquoted, so a legal directory name
// containing a space became two bogus PATH entries. Fish never
// word-splits a variable's *value* the way POSIX does, which is exactly
// why this is a correctness bug there rather than an injection one —
// and why only executing real fish could show it.
func TestShellIntegration_Fish_PathEntryWithSpaceStaysOneEntry(t *testing.T) {
	requireShell(t, "fish")

	devHome := buildDevInto(t, t.TempDir())

	workDir := t.TempDir()
	block := strings.Join(shell.FunctionLines(shell.Fish, devHome), "\n") + "\n"

	// The regression anchor, on its own: a space is the one adversarial
	// character that pre-fix fish mis-handled *silently* — the unquoted
	// entry simply became two bogus list elements, with no error. Mixing
	// in a single-quote or backtick entry would make the pre-fix `set
	// -gx PATH ...` line a parse error instead, which aborts the eval
	// and leaves PATH untouched — i.e. still correct, so the test would
	// pass against the broken code. Verified empirically; don't merge
	// these two runs.
	spaced := filepath.Join(workDir, "a space", "bin")
	if err := os.MkdirAll(spaced, 0o755); err != nil {
		t.Fatal(err)
	}
	out := runShellScript(t, "fish", block+
		"set -gx PATH $PATH "+fishSingleQuote(spaced)+"\n"+
		"eval (command dev env | string collect)\n"+
		"for p in $PATH\n    echo \"ENTRY=$p\"\nend\n", devHome)
	if !fishEntries(out)[spaced] {
		t.Errorf("PATH entry %q was not preserved as exactly one fish list element; got entries %v", spaced, fishEntries(out))
	}

	// A second run for the entries whose failure mode is a parse error
	// rather than a silent split: they must still round-trip intact, and
	// the backtick must not execute.
	sentinel := filepath.Join(workDir, "PWNED")
	backticked := filepath.Join(workDir, "`touch "+sentinel+"`", "bin")
	quoted := filepath.Join(workDir, "it's", "bin")
	for _, dir := range []string{backticked, quoted} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	out2 := runShellScript(t, "fish", block+
		"set -gx PATH $PATH "+fishSingleQuote(backticked)+" "+fishSingleQuote(quoted)+"\n"+
		"eval (command dev env | string collect)\n"+
		"for p in $PATH\n    echo \"ENTRY=$p\"\nend\n", devHome)
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("a PATH entry's backtick was executed by fish's eval of `dev env`'s output — sentinel %q exists. Output:\n%s", sentinel, out2)
	}
	entries2 := fishEntries(out2)
	for _, want := range []string{backticked, quoted} {
		if !entries2[want] {
			t.Errorf("PATH entry %q was not preserved as exactly one fish list element; got entries %v", want, entries2)
		}
	}
}

// fishEntries collects the ENTRY=<value> lines a fish test script
// echoes, one per element of its $PATH list.
func fishEntries(out string) map[string]bool {
	entries := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		if e, ok := strings.CutPrefix(line, "ENTRY="); ok {
			entries[e] = true
		}
	}
	return entries
}

// posixSingleQuote is a test-local copy of internal/shell's
// shellQuote, needed only so these tests' own generated scripts can
// safely carry the adversarial values they exercise (the real one is
// unexported).
func posixSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fishSingleQuote is a test-local copy of internal/shell's fishQuote,
// needed only to build this test's own adversarial fish script (the
// real one is unexported, and nothing in production needs it from
// here).
func fishSingleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "'", `\'`)
	return "'" + s + "'"
}

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
	// See the matching comment in
	// TestShellIntegration_LangUseUpdatesCurrentShellPath: the binary
	// must live directly inside $DEV_HOME or RunningFromDevHome's
	// guard in cmd/root.go refuses every subcommand under test here.
	devBinary := filepath.Join(devHome, "dev")
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
		"PATH="+os.Getenv("PATH"),
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
