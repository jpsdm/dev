package shell

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// withNoParentShellSignal forces Detect()'s parent-process lookup to
// be inconclusive, so tests that want to exercise the $SHELL fallback
// path get a result that depends only on $SHELL — not on whatever
// process happens to have spawned `go test` on a given machine, which
// varies (a shell, a CI runner, make, ...) and would otherwise make
// these tests non-deterministic across environments.
func withNoParentShellSignal(t *testing.T) {
	t.Helper()
	orig := parentShellDetector
	parentShellDetector = func() (Shell, bool) { return Unknown, false }
	t.Cleanup(func() { parentShellDetector = orig })
}

func TestDetect_UsesShellEnvVar(t *testing.T) {
	withNoParentShellSignal(t)
	t.Setenv("SHELL", "/bin/zsh")
	if got := Detect(); got != Zsh {
		t.Errorf("Detect() = %v, want Zsh", got)
	}
}

func TestDetect_UnknownShellFallsBackToUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on Windows, an unrecognized/inconclusive shell signal falls back to PowerShell, not Unknown — see TestDetect_WindowsDefaultsToPowerShellWhenNothingConclusive")
	}
	withNoParentShellSignal(t)
	t.Setenv("SHELL", "/bin/tcsh")
	if got := Detect(); got != Unknown {
		t.Errorf("Detect() = %v, want Unknown", got)
	}
}

func TestDetect_WindowsDefaultsToPowerShellWhenNothingConclusive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("exercises Detect()'s Windows-only final fallback")
	}
	withNoParentShellSignal(t)
	t.Setenv("SHELL", "/bin/tcsh")
	if got := Detect(); got != PowerShell {
		t.Errorf("Detect() = %v, want PowerShell (Windows's final fallback when neither signal is conclusive)", got)
	}
}

func TestDetect_PrefersParentProcessOverAStaleShellEnvVar(t *testing.T) {
	// The exact real-world bug this whole change fixes: $SHELL (the
	// configured *login* shell) says bash, but the process that
	// actually launched this invocation — resolved independently of
	// $SHELL — is fish. Detect() must trust the parent process, not
	// the stale env var, or dev writes bash syntax into a file fish
	// can't source.
	orig := parentShellDetector
	parentShellDetector = func() (Shell, bool) { return Fish, true }
	t.Cleanup(func() { parentShellDetector = orig })
	t.Setenv("SHELL", "/bin/bash")

	if got := Detect(); got != Fish {
		t.Errorf("Detect() = %v, want Fish (from the parent process, not $SHELL)", got)
	}
}

func TestDetect_FallsBackToShellEnvVarWhenParentProcessIsInconclusive(t *testing.T) {
	// An inconclusive parent-process lookup (unsupported platform, a
	// permission error, a parent that isn't a known shell at all —
	// e.g. dev invoked from a Makefile or another program) must not
	// be treated as "no shell" — it falls back to the existing
	// $SHELL-based behavior exactly as before this change.
	withNoParentShellSignal(t)
	t.Setenv("SHELL", "/bin/zsh")

	if got := Detect(); got != Zsh {
		t.Errorf("Detect() = %v, want Zsh (from the $SHELL fallback)", got)
	}
}

// parentCommName resolves the raw process-name string for pid (e.g.
// "bash", "fish", "sleep") — the low-level lookup parentShellName
// builds on. These tests exercise it directly since parentShellName
// itself just composes parentCommName with shellFromName (tested
// separately below).

func TestParentCommName_Linux_ReadsRealProcessComm(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("exercises the /proc-based lookup, which only exists on Linux")
	}
	// bash is present on essentially every Linux system, including
	// this project's own CI image.
	cmd := exec.Command("bash", "-c", "sleep 5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting bash subprocess: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	name, ok := parentCommName("linux", cmd.Process.Pid)
	if !ok {
		t.Fatal("parentCommName() ok = false for a real, running process")
	}
	if name != "bash" {
		t.Errorf("parentCommName() = %q, want %q", name, "bash")
	}
}

func TestParentCommName_Linux_NonexistentPIDReturnsFalse(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("exercises the /proc-based lookup, which only exists on Linux")
	}
	// A PID far beyond any real process table on a normal system won't
	// exist. Inherently a best-effort choice, not a guarantee, but
	// reliable in practice for a test.
	if _, ok := parentCommName("linux", 999999); ok {
		t.Error("parentCommName() ok = true for a PID that almost certainly doesn't exist")
	}
}

func TestParentCommName_UnsupportedPlatformReturnsFalse(t *testing.T) {
	if _, ok := parentCommName("plan9", 1); ok {
		t.Error("parentCommName() ok = true for a platform with no lookup strategy")
	}
}

func TestParentCommName_Darwin_UsesPs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ps has no POSIX-compatible -p/-o comm= interface on Windows; this test needs a POSIX host, not necessarily darwin itself — see the comment below")
	}
	// The real `ps -p <pid> -o comm=` command this exercises is
	// POSIX-compatible and runs the same way on this project's Linux
	// CI as it does on the real macOS this code path targets — a
	// genuine exercise of the exact command/argument-slice construction
	// that ships, not a platform-gated skip (beyond the Windows one
	// above, which is a real environment limitation, not a design
	// choice). TestParseCommOutput below covers the one thing this
	// can't independently prove here: a real macOS `ps` returning a
	// full path (this Linux host's `ps` doesn't, for a plain "sleep").
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleep subprocess: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	name, ok := parentCommName("darwin", cmd.Process.Pid)
	if !ok {
		t.Fatal("parentCommName() ok = false for a real, running process")
	}
	if name != "sleep" {
		t.Errorf("parentCommName() = %q, want %q", name, "sleep")
	}
}

func TestParseCommOutput_StripsAFullPathToItsBaseName(t *testing.T) {
	t.Parallel()
	got, ok := parseCommOutput("/usr/local/bin/fish\n")
	if !ok || got != "fish" {
		t.Errorf("parseCommOutput() = (%q, %v), want (%q, true)", got, ok, "fish")
	}
}

func TestParseCommOutput_BlankIsInconclusive(t *testing.T) {
	t.Parallel()
	if _, ok := parseCommOutput("   \n"); ok {
		t.Error("parseCommOutput() ok = true for blank output")
	}
}

func TestShellFromName_MapsKnownShellNames(t *testing.T) {
	t.Parallel()
	cases := map[string]Shell{
		"bash":       Bash,
		"zsh":        Zsh,
		"fish":       Fish,
		"powershell": PowerShell,
		"pwsh":       PowerShell,
	}
	for name, want := range cases {
		if got, ok := shellFromName(name); !ok || got != want {
			t.Errorf("shellFromName(%q) = (%v, %v), want (%v, true)", name, got, ok, want)
		}
	}
}

func TestShellFromName_UnrecognizedNameReturnsFalse(t *testing.T) {
	t.Parallel()
	if _, ok := shellFromName("sleep"); ok {
		t.Error("shellFromName(\"sleep\") ok = true, want false")
	}
	if _, ok := shellFromName(""); ok {
		t.Error("shellFromName(\"\") ok = true, want false")
	}
}

func TestParentShellName_Linux_EndToEndWithARealBashProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("exercises the /proc-based lookup, which only exists on Linux")
	}
	cmd := exec.Command("bash", "-c", "sleep 5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting bash subprocess: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	got, ok := parentShellName("linux", cmd.Process.Pid)
	if !ok || got != Bash {
		t.Errorf("parentShellName() = (%v, %v), want (Bash, true) for a real bash subprocess", got, ok)
	}
}

func TestParentShellName_UnrecognizedProcessReturnsFalse(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("exercises the /proc-based lookup, which only exists on Linux")
	}
	// sleep is a real, running process, but not a known shell —
	// parentShellName must report this the same way as an inconclusive
	// lookup (false), not as some meaningful "shell" answer.
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleep subprocess: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	if _, ok := parentShellName("linux", cmd.Process.Pid); ok {
		t.Error("parentShellName() ok = true for a real process that isn't a known shell")
	}
}

// testDevHome returns an OS-native absolute devHome path ("/home/u/.dev"
// on Unix, `\home\u\.dev` on Windows) — ComputePathEntries builds its
// stale-versions prefix with filepath.Join/Separator, so a literal
// forward-slash string here would silently never match it on Windows.
func testDevHome() string {
	return filepath.Join(string(filepath.Separator), "home", "u", ".dev")
}

func TestComputePathEntries_PrependsDevHomeAndActiveDirs(t *testing.T) {
	devHome := testDevHome()
	activeDir := filepath.Join(devHome, "versions", "node", "22", "bin")
	current := []string{"/usr/bin", "/bin"}
	got := ComputePathEntries(current, devHome, []string{activeDir})
	want := []string{devHome, activeDir, "/usr/bin", "/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputePathEntries_StripsStaleVersionsEntries(t *testing.T) {
	devHome := testDevHome()
	activeDir := filepath.Join(devHome, "versions", "node", "22", "bin")
	current := []string{
		filepath.Join(devHome, "versions", "node", "20", "bin"), // stale: a prior active Node version
		"/usr/bin",
	}
	got := ComputePathEntries(current, devHome, []string{activeDir})
	want := []string{devHome, activeDir, "/usr/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputePathEntries_StripsDevHomeItself(t *testing.T) {
	// devHome is always re-prepended fresh; a stale literal devHome entry
	// already present in current must not be duplicated.
	devHome := testDevHome()
	current := []string{devHome, "/usr/bin"}
	got := ComputePathEntries(current, devHome, nil)
	want := []string{devHome, "/usr/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputePathEntries_SkipsEmptyEntries(t *testing.T) {
	// strings.Split("", sep) yields [""], not []; an empty PATH segment
	// means "current directory" to a POSIX shell and must never be
	// introduced by this computation.
	devHome := testDevHome()
	got := ComputePathEntries([]string{""}, devHome, nil)
	want := []string{devHome}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputePathEntries_NoActiveProvidersContributesNothing(t *testing.T) {
	devHome := testDevHome()
	got := ComputePathEntries([]string{"/usr/bin"}, devHome, nil)
	want := []string{devHome, "/usr/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputePathEntries_IdempotentAcrossRepeatedCalls(t *testing.T) {
	devHome := testDevHome()
	active := []string{filepath.Join(devHome, "versions", "node", "22", "bin")}
	first := ComputePathEntries([]string{"/usr/bin", "/bin"}, devHome, active)
	second := ComputePathEntries(first, devHome, active)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("not idempotent: first=%v second=%v", first, second)
	}
}

func TestExportLines_Posix(t *testing.T) {
	got := ExportLines(Bash, "/home/u/.dev", []string{"/home/u/.dev", "/home/u/.dev/versions/node/22/bin", "/usr/bin"})
	want := []string{
		`export DEV_HOME='/home/u/.dev'`,
		`export PATH='/home/u/.dev:/home/u/.dev/versions/node/22/bin:/usr/bin'`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExportLines_Fish(t *testing.T) {
	got := ExportLines(Fish, "/home/u/.dev", []string{"/home/u/.dev", "/usr/bin"})
	want := []string{
		`set -gx DEV_HOME '/home/u/.dev'`,
		`set -gx PATH '/home/u/.dev' '/usr/bin'`,
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
		`export PATH='/home/u/.dev'`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestExportLines_PosixQuotesPathEntriesAgainstCommandSubstitution pins
// the Critical finding the v0.8.0 final review caught by executing the
// generated script rather than reading it: these lines are deliberately
// fed to `eval` by the installed shell function, and POSIX *double*
// quotes do not suppress `backtick`/$(...) command substitution. A
// backtick in a PATH entry (a legal directory name) was therefore
// executed. Single quotes are the only POSIX construct that suppresses
// it. See TestShellIntegration_PathEntryWithBacktickDoesNotExecute in
// cmd/ for the same property proven through a real sh subprocess.
func TestExportLines_PosixQuotesPathEntriesAgainstCommandSubstitution(t *testing.T) {
	t.Parallel()
	entries := []string{
		"/home/u/.dev",
		"/tmp/`touch pwned`/bin",
		"/tmp/$(touch pwned2)/bin",
		"/tmp/it's a dir/bin",
		`/tmp/"quoted"/bin`,
	}
	got := ExportLines(Bash, "/home/u/.dev", entries)
	pathLine := got[1]

	if !strings.HasPrefix(pathLine, `export PATH='`) || !strings.HasSuffix(pathLine, `'`) {
		t.Fatalf("PATH line is not single-quoted end to end: %q", pathLine)
	}
	// Every single quote in the value must be the escaped form, so no
	// character of the value can ever be seen outside the quotes.
	if strings.Contains(pathLine, `it's`) {
		t.Errorf("an embedded single quote survived unescaped, closing the quoted string early: %q", pathLine)
	}
	if !strings.Contains(pathLine, `/tmp/it'\''s a dir/bin`) {
		t.Errorf("expected the embedded single quote escaped as '\\'': %q", pathLine)
	}
	// Backticks and $(...) stay literal inside single quotes; what must
	// never happen is them appearing inside a double-quoted value.
	if strings.Contains(pathLine, `export PATH="`) {
		t.Errorf("PATH value is double-quoted, which does not suppress command substitution: %q", pathLine)
	}
}

func TestExportLines_FishQuotesEveryPathEntryIndividually(t *testing.T) {
	t.Parallel()
	entries := []string{
		"/home/u/.dev",
		"/tmp/a space/bin",
		"/tmp/`touch pwned`/bin",
		"/tmp/$(touch pwned2)/bin",
		"/tmp/it's a dir/bin",
		`/tmp/back\slash/bin`,
	}
	got := ExportLines(Fish, "/home/u/.dev", entries)
	want := []string{
		`set -gx DEV_HOME '/home/u/.dev'`,
		`set -gx PATH '/home/u/.dev' '/tmp/a space/bin' '/tmp/` + "`" + `touch pwned` + "`" + `/bin' '/tmp/$(touch pwned2)/bin' '/tmp/it\'s a dir/bin' '/tmp/back\\slash/bin'`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%v\nwant\n%v", got, want)
	}
}

// TestFishQuote_EscapesBackslashUnlikePosix pins the one real
// divergence between fish's single-quote rules and POSIX's, verified
// against fish 4.6.0: fish honours \\ and \' as escapes inside single
// quotes, so a value containing a backslash must have it doubled or
// fish silently eats it (and a trailing backslash makes the whole line
// an unterminated-string parse error). shellQuote must NOT gain this
// behavior — it would corrupt the value under POSIX sh, which honours
// no escapes inside single quotes at all.
func TestFishQuote_EscapesBackslashUnlikePosix(t *testing.T) {
	t.Parallel()
	if got, want := fishQuote(`/tmp/a\b`), `'/tmp/a\\b'`; got != want {
		t.Errorf("fishQuote(%q) = %q, want %q", `/tmp/a\b`, got, want)
	}
	if got, want := fishQuote(`/tmp/trailing\`), `'/tmp/trailing\\'`; got != want {
		t.Errorf("fishQuote(%q) = %q, want %q", `/tmp/trailing\`, got, want)
	}
	if got, want := shellQuote(`/tmp/a\b`), `'/tmp/a\b'`; got != want {
		t.Errorf("shellQuote(%q) = %q, want %q (POSIX must stay backslash-literal)", `/tmp/a\b`, got, want)
	}
}

// TestEscapePowerShellDoubleQuoted_EscapesBacktickDollarAndQuote
// covers the PowerShell half of the same finding: PowerShell
// double-quoted strings interpolate $variable and $(expression), and
// the prior code escaped only an embedded double quote. Unverifiable
// against a real PowerShell host from this project's Linux-only
// development environment — this test pins the intended output shape,
// not observed PowerShell behavior.
func TestEscapePowerShellDoubleQuoted_EscapesBacktickDollarAndQuote(t *testing.T) {
	t.Parallel()
	// Backtick first: the backticks the $ and " replacements insert
	// must not themselves get escaped.
	if got, want := escapePowerShellDoubleQuoted("a`b"), "a``b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := escapePowerShellDoubleQuoted(`$(calc)`), "`$(calc)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := escapePowerShellDoubleQuoted(`a"b`), "a`\"b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// A literal Windows path's backslashes must survive untouched.
	if got, want := escapePowerShellDoubleQuoted(`C:\Users\u\.dev`), `C:\Users\u\.dev`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExportLines_PowerShellEscapesDollarInPathEntries(t *testing.T) {
	t.Parallel()
	got := ExportLines(PowerShell, `C:\Users\u\.dev`, []string{`C:\Users\u\.dev`, `C:\tmp\$(calc)`})
	want := []string{
		`$env:DEV_HOME = "C:\Users\u\.dev"`,
		"$env:PATH = \"C:\\Users\\u\\.dev;C:\\tmp\\`$(calc)\"",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestShellQuote_EscapesEmbeddedSingleQuote(t *testing.T) {
	t.Parallel()
	got := shellQuote("it's")
	want := `'it'\''s'`
	if got != want {
		t.Errorf(`shellQuote("it's") = %q, want %q`, got, want)
	}
}

func TestFunctionLines_Posix(t *testing.T) {
	got := FunctionLines(Bash, "/home/u/.dev")
	want := []string{
		`export DEV_HOME='/home/u/.dev'`,
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
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestFunctionLines_AllShellsEndWithAOneTimeEnvEval pins the Critical
// finding the v0.8.0 final review caught by opening a second shell: the
// installed block defines the wrapper function, but nothing evaluated
// `dev env` at rc-source time, so a freshly started shell never got the
// already-active version on PATH — only a shell that had itself run
// `dev lang ...` did. The trailing eval must be the LAST line of the
// block and must sit outside the function body (a line inside it would
// be the per-prompt-adjacent behavior the spec's non-goals rule out).
func TestFunctionLines_AllShellsEndWithAOneTimeEnvEval(t *testing.T) {
	t.Parallel()
	cases := map[Shell]string{
		Bash:       `eval "$(command dev env)"`,
		Zsh:        `eval "$(command dev env)"`,
		Unknown:    `eval "$(command dev env)"`,
		Fish:       `eval (command dev env | string collect)`,
		PowerShell: "(& \"$env:DEV_HOME\\dev.exe\" env) -join \"`n\" | Invoke-Expression",
	}
	for sh, wantLast := range cases {
		got := FunctionLines(sh, "/home/u/.dev")
		if last := got[len(got)-1]; last != wantLast {
			t.Errorf("FunctionLines(%v) last line = %q, want %q", sh, last, wantLast)
		}
		// The line before it closes the function body, proving the eval
		// is outside it rather than part of the wrapper.
		switch closer := got[len(got)-2]; sh {
		case Fish:
			if closer != "end" {
				t.Errorf("FunctionLines(Fish): expected the eval to follow the function's `end`, got %q", closer)
			}
		default:
			if closer != "}" {
				t.Errorf("FunctionLines(%v): expected the eval to follow the function's closing brace, got %q", sh, closer)
			}
		}
	}
}

func TestFunctionLines_Fish(t *testing.T) {
	got := FunctionLines(Fish, "/home/u/.dev")
	want := []string{
		`set -gx DEV_HOME '/home/u/.dev'`,
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
		"(& \"$env:DEV_HOME\\dev.exe\" env) -join \"`n\" | Invoke-Expression",
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
	if last := got[len(got)-1]; last != `eval "$(command dev env)"` {
		t.Fatalf("expected the POSIX fallback's trailing one-time env eval, got %q", last)
	}
}

func TestRCPath_Bash(t *testing.T) {
	t.Parallel()
	path, supported, err := RCPath(Bash)
	if err != nil {
		t.Fatalf("RCPath(Bash) returned error: %v", err)
	}
	if !supported {
		t.Fatal("RCPath(Bash) supported = false, want true")
	}
	if !strings.HasSuffix(path, ".bashrc") {
		t.Errorf("RCPath(Bash) = %q, want it to end in .bashrc", path)
	}
}

func TestRCPath_PowerShellUsesRealProfilePathWhenAvailable(t *testing.T) {
	original := powershellProfilePath
	powershellProfilePath = func() (string, bool) {
		return `C:\Users\u\Documents\WindowsPowerShell\Microsoft.PowerShell_profile.ps1`, true
	}
	t.Cleanup(func() { powershellProfilePath = original })

	path, supported, err := RCPath(PowerShell)
	if err != nil {
		t.Fatalf("RCPath(PowerShell) returned error: %v", err)
	}
	if !supported {
		t.Fatal("RCPath(PowerShell) supported = false, want true when a real PowerShell profile path was found")
	}
	if path != `C:\Users\u\Documents\WindowsPowerShell\Microsoft.PowerShell_profile.ps1` {
		t.Errorf("RCPath(PowerShell) = %q, want the path powershellProfilePath reported", path)
	}
}

func TestRCPath_PowerShellFallsBackToUnsupportedWhenProfilePathUnknown(t *testing.T) {
	original := powershellProfilePath
	powershellProfilePath = func() (string, bool) { return "", false }
	t.Cleanup(func() { powershellProfilePath = original })

	_, supported, err := RCPath(PowerShell)
	if err != nil {
		t.Fatalf("RCPath(PowerShell) returned error: %v", err)
	}
	if supported {
		t.Error("RCPath(PowerShell) supported = true, want false when no PowerShell executable was reachable")
	}
}

func TestDefaultPowershellProfilePath_NoExecutableReachableReturnsNotOK(t *testing.T) {
	// This project's dev/CI environment has neither pwsh nor powershell
	// installed, so the real (non-overridden) lookup is expected to
	// fail gracefully here rather than panic or error — exercising the
	// actual exec.Command calls, not just the overridable seam the
	// tests above use.
	if _, err := exec.LookPath("pwsh"); err == nil {
		t.Skip("pwsh is installed on this machine; this test only exercises the not-found path")
	}
	if _, err := exec.LookPath("powershell"); err == nil {
		t.Skip("powershell is installed on this machine; this test only exercises the not-found path")
	}

	path, ok := defaultPowershellProfilePath()
	if ok {
		t.Errorf("defaultPowershellProfilePath() = (%q, true), want (_, false) with neither pwsh nor powershell installed", path)
	}
}

func TestRCPath_HomeDirFailureReturnsDistinctError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("$HOME is not the profile-dir env var on windows")
	}
	t.Setenv("HOME", "")

	_, supported, err := RCPath(Bash)
	if err == nil {
		t.Fatal("RCPath() returned nil error when $HOME is unresolvable")
	}
	if supported {
		t.Error("RCPath() supported = true despite an unresolvable home directory")
	}
}

func TestRcPathForHome_DarwinBashTargetsBashProfile(t *testing.T) {
	t.Parallel()
	path, supported, err := rcPathForHome(Bash, "/Users/jp", "darwin")
	if err != nil {
		t.Fatalf("rcPathForHome() returned error: %v", err)
	}
	if !supported {
		t.Fatal("rcPathForHome(Bash, darwin) supported = false, want true")
	}
	if !strings.HasSuffix(path, ".bash_profile") {
		t.Errorf("rcPathForHome(Bash, darwin) = %q, want it to end in .bash_profile", path)
	}
}

func TestRcPathForHome_LinuxBashTargetsBashrc(t *testing.T) {
	t.Parallel()
	path, supported, err := rcPathForHome(Bash, "/home/jp", "linux")
	if err != nil {
		t.Fatalf("rcPathForHome() returned error: %v", err)
	}
	if !supported {
		t.Fatal("rcPathForHome(Bash, linux) supported = false, want true")
	}
	if !strings.HasSuffix(path, ".bashrc") {
		t.Errorf("rcPathForHome(Bash, linux) = %q, want it to end in .bashrc", path)
	}
}

func TestUpsertBlock_CreatesNewFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "rc")

	if err := UpsertBlock(path, []string{"export FOO=bar"}); err != nil {
		t.Fatalf("UpsertBlock() returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading result: %v", err)
	}
	content := string(got)
	if !strings.Contains(content, "# BEGIN dev shell setup") ||
		!strings.Contains(content, "export FOO=bar") ||
		!strings.Contains(content, "# END dev shell setup") {
		t.Errorf("UpsertBlock() wrote %q, missing expected markers/content", content)
	}
}

func TestUpsertBlock_PreservesUnrelatedContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "rc")
	if err := os.WriteFile(path, []byte("alias ll='ls -la'\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := UpsertBlock(path, []string{"export FOO=bar"}); err != nil {
		t.Fatalf("UpsertBlock() returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading result: %v", err)
	}
	content := string(got)
	if !strings.Contains(content, "alias ll='ls -la'") {
		t.Errorf("UpsertBlock() lost unrelated existing content: %q", content)
	}
	if !strings.Contains(content, "export FOO=bar") {
		t.Errorf("UpsertBlock() did not add the new block: %q", content)
	}
}

func TestUpsertBlock_ReplacesExistingBlockWithoutDuplicating(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "rc")

	if err := UpsertBlock(path, []string{"export FOO=old"}); err != nil {
		t.Fatalf("first UpsertBlock() returned error: %v", err)
	}
	if err := UpsertBlock(path, []string{"export FOO=new"}); err != nil {
		t.Fatalf("second UpsertBlock() returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading result: %v", err)
	}
	content := string(got)
	if strings.Count(content, "# BEGIN dev shell setup") != 1 {
		t.Errorf("UpsertBlock() duplicated the marker block: %q", content)
	}
	if strings.Contains(content, "export FOO=old") {
		t.Errorf("UpsertBlock() left the old block content behind: %q", content)
	}
	if !strings.Contains(content, "export FOO=new") {
		t.Errorf("UpsertBlock() did not write the new block content: %q", content)
	}
}

func TestUpsertBlock_AddsBlankLineSeparatorWhenAppendingToExistingContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "rc")
	if err := os.WriteFile(path, []byte("alias ll='ls -la'\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := UpsertBlock(path, []string{"export FOO=bar"}); err != nil {
		t.Fatalf("UpsertBlock() returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading result: %v", err)
	}
	if !strings.Contains(string(got), "alias ll='ls -la'\n\n# BEGIN dev shell setup") {
		t.Errorf("UpsertBlock() = %q, want a blank line separating prior content from the new block", got)
	}
}

func TestUpsertBlock_UnterminatedBlockReturnsErrorWithoutTruncating(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "rc")
	original := "alias a='1'\n# BEGIN dev shell setup\nexport OLD=1\nalias b='2'\nexport IMPORTANT=yes\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := UpsertBlock(path, []string{"export NEW=1"})
	if err == nil {
		t.Fatal("UpsertBlock() returned nil error for a file with an unterminated BEGIN marker")
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("reading file after failed UpsertBlock: %v", readErr)
	}
	if string(got) != original {
		t.Errorf("UpsertBlock() modified the file despite returning an error: got %q, want unchanged %q", got, original)
	}
}

func TestConfigurable_ReturnsTheFourKnownShellsInAFixedOrder(t *testing.T) {
	t.Parallel()
	got := Configurable()
	want := []Shell{Bash, Zsh, Fish, PowerShell}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Configurable() = %v, want %v", got, want)
	}
}

// withLookPath overrides LookPath for the duration of the test so only
// the given names resolve, deterministically, regardless of what's
// actually installed on the machine running `go test`.
func withLookPath(t *testing.T, present ...string) {
	t.Helper()
	found := make(map[string]bool, len(present))
	for _, name := range present {
		found[name] = true
	}
	orig := LookPath
	LookPath = func(file string) (string, error) {
		if found[file] {
			return "/fake/" + file, nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { LookPath = orig })
}

func TestIsPresent_TrueWhenLookupNameResolves(t *testing.T) {
	withLookPath(t, "zsh")
	if !IsPresent(Zsh) {
		t.Error("IsPresent(Zsh) = false, want true when \"zsh\" resolves via LookPath")
	}
}

func TestIsPresent_FalseWhenNoLookupNameResolvesAndNotTheDetectedShell(t *testing.T) {
	withLookPath(t) // nothing resolves
	withNoParentShellSignal(t)
	t.Setenv("SHELL", "/bin/bash") // Detect() -> Bash, not Fish
	if IsPresent(Fish) {
		t.Error("IsPresent(Fish) = true, want false when fish isn't on PATH and isn't the detected shell")
	}
}

func TestIsPresent_TrueWhenItsTheCurrentlyDetectedShellEvenIfNotOnPath(t *testing.T) {
	withLookPath(t) // nothing resolves via LookPath
	orig := parentShellDetector
	parentShellDetector = func() (Shell, bool) { return Fish, true }
	t.Cleanup(func() { parentShellDetector = orig })

	if !IsPresent(Fish) {
		t.Error("IsPresent(Fish) = false, want true when Fish is the currently-detected shell, even with no lookupNames match")
	}
}

func TestIsPresent_PowerShellResolvesEitherBinaryName(t *testing.T) {
	withLookPath(t, "pwsh")
	if !IsPresent(PowerShell) {
		t.Error("IsPresent(PowerShell) = false, want true when only \"pwsh\" (not \"powershell\") resolves")
	}
}

func TestBlockUpToDate_FalseForAMissingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "rc")
	got, err := BlockUpToDate(path, []string{"export X=1"})
	if err != nil {
		t.Fatalf("BlockUpToDate() returned error: %v", err)
	}
	if got {
		t.Error("BlockUpToDate() = true for a file that doesn't exist, want false")
	}
}

func TestBlockUpToDate_TrueAfterUpsertBlockWroteTheSameLines(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "rc")
	lines := []string{"export DEV_HOME=/home/u/.dev", "export PATH=\"$DEV_HOME:$PATH\""}
	if err := UpsertBlock(path, lines); err != nil {
		t.Fatalf("UpsertBlock() returned error: %v", err)
	}
	got, err := BlockUpToDate(path, lines)
	if err != nil {
		t.Fatalf("BlockUpToDate() returned error: %v", err)
	}
	if !got {
		t.Error("BlockUpToDate() = false right after UpsertBlock wrote the exact same lines, want true")
	}
}

func TestBlockUpToDate_FalseWhenLinesDiffer(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "rc")
	if err := UpsertBlock(path, []string{"export OLD=1"}); err != nil {
		t.Fatalf("UpsertBlock() returned error: %v", err)
	}
	got, err := BlockUpToDate(path, []string{"export NEW=1"})
	if err != nil {
		t.Fatalf("BlockUpToDate() returned error: %v", err)
	}
	if got {
		t.Error("BlockUpToDate() = true for a file whose block content differs, want false")
	}
}

func TestConfirm_AcceptsYAndYes(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"y", "Y", "yes", "YES", " y \n"} {
		var out bytes.Buffer
		got, err := Confirm("prompt: ", strings.NewReader(input), &out)
		if err != nil {
			t.Fatalf("Confirm(%q) returned error: %v", input, err)
		}
		if !got {
			t.Errorf("Confirm(%q) = false, want true", input)
		}
	}
}

func TestConfirm_RejectsAnythingElse(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"n", "no", "", "maybe"} {
		var out bytes.Buffer
		got, err := Confirm("prompt: ", strings.NewReader(input), &out)
		if err != nil {
			t.Fatalf("Confirm(%q) returned error: %v", input, err)
		}
		if got {
			t.Errorf("Confirm(%q) = true, want false", input)
		}
	}
}

func TestConfirm_EchoesNewlineAfterPromptForNonInteractiveClarity(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if _, err := Confirm("prompt: ", strings.NewReader("y\n"), &out); err != nil {
		t.Fatalf("Confirm() returned error: %v", err)
	}
	if got := out.String(); got != "prompt: \n" {
		t.Errorf("Confirm() wrote %q, want the prompt followed by a newline", got)
	}
}

// TestConfirm_SequentialCallsShareTheReaderCorrectly reproduces a
// prompt sequence like cmd/setup.go's setupCmd RunE, which calls
// Confirm more than once against the same underlying reader within a
// single command invocation (e.g. "add these lines to your rc file?"
// followed by "move dev into $DEV_HOME now?"). A bufio.Scanner
// allocated fresh on every call reads ahead into its own buffer and
// discards any unconsumed bytes when the function returns, so a
// second Confirm() call against the same reader would silently see
// EOF and default to false regardless of what was actually typed for
// it — this test pins the fix: each call must consume only its own
// line, leaving the rest of the reader intact for the next call.
func TestConfirm_SequentialCallsShareTheReaderCorrectly(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	in := strings.NewReader("y\nn\ny\n")

	first, err := Confirm("first: ", in, &out)
	if err != nil {
		t.Fatalf("first Confirm() returned error: %v", err)
	}
	if !first {
		t.Errorf("first Confirm() = false, want true (input was %q)", "y")
	}

	second, err := Confirm("second: ", in, &out)
	if err != nil {
		t.Fatalf("second Confirm() returned error: %v", err)
	}
	if second {
		t.Errorf("second Confirm() = true, want false (input was %q)", "n")
	}

	third, err := Confirm("third: ", in, &out)
	if err != nil {
		t.Fatalf("third Confirm() returned error: %v", err)
	}
	if !third {
		t.Errorf("third Confirm() = false, want true (input was %q)", "y")
	}
}

func TestPromptLine_ReturnsTrimmedAnswerWhenGiven(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	in := strings.NewReader("  MyFolder  \n")

	got, err := PromptLine("Name: ", in, &out, "Workspace")
	if err != nil {
		t.Fatalf("PromptLine() returned error: %v", err)
	}
	if got != "MyFolder" {
		t.Errorf("PromptLine() = %q, want %q", got, "MyFolder")
	}
}

func TestPromptLine_ReturnsDefaultOnEmptyLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	in := strings.NewReader("\n")

	got, err := PromptLine("Name: ", in, &out, "Workspace")
	if err != nil {
		t.Fatalf("PromptLine() returned error: %v", err)
	}
	if got != "Workspace" {
		t.Errorf("PromptLine() = %q, want the default %q", got, "Workspace")
	}
}

func TestPromptLine_ReturnsDefaultOnEOF(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	in := strings.NewReader("")

	got, err := PromptLine("Name: ", in, &out, "Workspace")
	if err != nil {
		t.Fatalf("PromptLine() returned error: %v", err)
	}
	if got != "Workspace" {
		t.Errorf("PromptLine() = %q, want the default %q", got, "Workspace")
	}
}

// TestPromptLine_SequentialCallsShareTheReaderCorrectly pins the same
// property TestConfirm_SequentialCallsShareTheReaderCorrectly pins for
// Confirm: two PromptLine calls against the same reader in one command
// run (e.g. asking for a workspace folder's name, then its location)
// must each see exactly the line meant for them.
func TestPromptLine_SequentialCallsShareTheReaderCorrectly(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	in := strings.NewReader("MyFolder\n/tmp/chosen\n")

	name, err := PromptLine("Name: ", in, &out, "Workspace")
	if err != nil {
		t.Fatalf("first PromptLine() returned error: %v", err)
	}
	if name != "MyFolder" {
		t.Errorf("first PromptLine() = %q, want %q", name, "MyFolder")
	}

	location, err := PromptLine("Location: ", in, &out, "/tmp/default")
	if err != nil {
		t.Fatalf("second PromptLine() returned error: %v", err)
	}
	if location != "/tmp/chosen" {
		t.Errorf("second PromptLine() = %q, want %q", location, "/tmp/chosen")
	}
}
