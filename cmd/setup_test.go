package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/shell"
)

// skipOnWindowsRCFile skips a test that asserts on a shell rc file.
// shell.Detect() unconditionally reports PowerShell on Windows, and
// shell.RCPath now asks a real PowerShell process for its own
// $PROFILE value there — supported whenever pwsh or powershell is
// reachable, true on essentially every real Windows machine (built-in
// Windows PowerShell included). This skip stays conservative anyway:
// a CI runner's actual $PROFILE value depends on its home-directory
// layout and installed PowerShell version, neither knowable ahead of
// time to assert against here, so these tests skip on Windows rather
// than assert against a path only discoverable by actually running
// the subprocess.
func skipOnWindowsRCFile(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("dev setup's rc-file target on Windows depends on a real PowerShell subprocess's own $PROFILE value, not knowable ahead of time in this test")
	}
}

// fakeInstalledDev materializes the binary cmd/main_test.go's TestMain
// claims is the running executable — $DEV_HOME/dev. Several tests need
// this file to exist for relocateIfNeeded's RunningFromDevHome check
// to resolve cleanly under TestMain's default.
func fakeInstalledDev(t *testing.T, devHome string) {
	t.Helper()
	if err := os.MkdirAll(devHome, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devHome, "dev"), []byte("fake dev binary"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
}

func TestSetupCommand_DeclinedConfirmationMakesNoChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DEV_HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/bash")

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("n\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	if runtime.GOOS != "windows" {
		rcPath := filepath.Join(home, ".bashrc")
		if _, err := os.Stat(rcPath); !os.IsNotExist(err) {
			t.Errorf("declining confirmation still created %s", rcPath)
		}
	}
	if !strings.Contains(out.String(), "No changes made.") {
		t.Errorf("output = %q, want it to confirm no changes were made", out.String())
	}
}

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

func TestSetupCommand_UnsupportedShellStillConfirmable(t *testing.T) {
	if runtime.GOOS == "windows" {
		// shell.Detect() ignores $SHELL entirely on Windows
		// (runtime.GOOS=="windows" short-circuits to PowerShell
		// unconditionally), and RCPath(PowerShell) there asks a real
		// PowerShell process for $PROFILE — supported on essentially
		// every real Windows machine (built-in Windows PowerShell
		// included), so this test's "unsupported shell" scenario can't
		// be reliably reproduced on a real Windows host anymore.
		t.Skip("RCPath(PowerShell) is supported on essentially every real Windows machine; this scenario isn't reproducible there")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	// An unrecognized $SHELL makes shell.Detect() return Unknown on
	// Linux/macOS, which takes the same "automatic rc-file editing not
	// supported" path RCPath also gives PowerShell when no
	// pwsh/powershell is reachable at all. The assertion below still
	// computes the expected text via unsupportedShellMessage(runtime.GOOS)
	// for generality across Linux/macOS — it just never sees "windows"
	// here now, since that case is skipped above.
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

func TestSetupCommand_UnsupportedShellDeclinedMakesNoChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/some-unrecognized-shell")

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("n\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	binDir := filepath.Join(devHome, "bin")
	if _, err := os.Stat(binDir); !os.IsNotExist(err) {
		t.Errorf("declining confirmation still created a shim directory at %s", binDir)
	}
	if !strings.Contains(out.String(), "No changes made.") {
		t.Errorf("output = %q, want it to confirm no changes were made", out.String())
	}
}

func TestSetupCommand_RunningTwiceDoesNotDuplicateOrError(t *testing.T) {
	skipOnWindowsRCFile(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	run := func() {
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		rootCmd.SetIn(strings.NewReader("y\n"))
		rootCmd.SetArgs([]string{"setup"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("`dev setup` returned error: %v", err)
		}
	}
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	run()
	run()

	rcPath := filepath.Join(home, ".bashrc")
	data, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("reading rc file: %v", err)
	}
	if strings.Count(string(data), "# BEGIN dev shell setup") != 1 {
		t.Errorf("rc file = %q, want exactly one marker block after running `dev setup` twice", data)
	}
}

func TestSetupCommand_RelocatesDevAndDocsIntoDevHomeWhenConfirmed(t *testing.T) {
	extractDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")

	// Simulate a freshly-extracted release archive: a fake "running
	// executable" plus its two siblings, none of them anywhere near
	// $DEV_HOME. This calls installRelocation directly with an explicit
	// exe path, so it covers the copy/remove mechanics on their own,
	// without the guard or the prompt in front of them. The full
	// end-to-end path (which does override platform.Executable) is
	// TestSetupCommand_OffersRelocationWhenNotYetInstalled's job.
	exe := filepath.Join(extractDir, "dev")
	if err := os.WriteFile(exe, []byte("fake binary"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extractDir, "README.md"), []byte("readme"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extractDir, "LICENSE"), []byte("license"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := installRelocation(io.Discard, exe, devHome); err != nil {
		t.Fatalf("installRelocation() returned error: %v", err)
	}

	for _, name := range []string{"dev", "README.md", "LICENSE"} {
		got, err := os.ReadFile(filepath.Join(devHome, name))
		if err != nil {
			t.Errorf("expected %s to be copied into $DEV_HOME: %v", name, err)
		}
		_ = got
	}
	for _, name := range []string{"dev", "README.md", "LICENSE"} {
		if _, err := os.Stat(filepath.Join(extractDir, name)); !os.IsNotExist(err) {
			t.Errorf("expected original %s to be removed after relocation, got stat err: %v", name, err)
		}
	}
}

func TestInstallRelocation_MissingReadmeOrLicenseIsNotFatal(t *testing.T) {
	extractDir := t.TempDir()
	devHome := t.TempDir()
	exe := filepath.Join(extractDir, "dev")
	if err := os.WriteFile(exe, []byte("fake binary"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Deliberately no README.md/LICENSE alongside exe — matches a
	// from-source `go build` binary, which has no such siblings.

	if err := installRelocation(io.Discard, exe, devHome); err != nil {
		t.Fatalf("installRelocation() returned error for a binary with no README/LICENSE siblings: %v", err)
	}
	if _, err := os.Stat(filepath.Join(devHome, "dev")); err != nil {
		t.Errorf("expected dev itself to still be relocated: %v", err)
	}
}

// TestInstallRelocation_ReportsRemovalFailureAsSafeToDelete pins the
// message shown when the original can't be removed after copying — a
// real, expected case on Windows, where a running process can't delete
// its own executable image (a real user hit exactly this: "Could not
// remove ...dev.exe after copying it: ... Acesso negado", which reads
// like something went wrong, when actually the copy already succeeded
// and the original is simply safe to delete by hand). The message must
// say so plainly, not surface the raw OS error as if it were a
// problem.
func TestInstallRelocation_ReportsRemovalFailureAsSafeToDelete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("simulates a removal failure via a read-only directory, a Unix permission concept — the real Windows case (a running exe can't delete itself) isn't reproducible this way")
	}
	extractDir := t.TempDir()
	devHome := t.TempDir()
	exe := filepath.Join(extractDir, "dev")
	if err := os.WriteFile(exe, []byte("fake binary"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Removing a file requires write permission on its containing
	// directory, not the file itself — this reliably makes os.Remove
	// fail without touching the file dev already successfully copied.
	if err := os.Chmod(extractDir, 0o555); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(extractDir, 0o755); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	})

	var out bytes.Buffer
	if err := installRelocation(&out, exe, devHome); err != nil {
		t.Fatalf("installRelocation() returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(devHome, "dev")); err != nil {
		t.Errorf("expected dev to still be copied into devHome despite the removal failure: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "you can delete") {
		t.Errorf("output = %q, want a reassuring \"you can delete\" message", got)
	}
	if strings.Contains(got, "Could not remove") {
		t.Errorf("output = %q, want it not to use alarming \"Could not remove\" wording", got)
	}
}

// relocationPrompt is the exact question relocateIfNeeded asks. Kept
// in one place so the tests that assert it was (or was not) shown stay
// in step with cmd/setup.go.
const relocationPrompt = "Move dev, README.md, and LICENSE into $DEV_HOME now?"

func TestSetupCommand_SkipsRelocationWhenAlreadyInsideDevHome(t *testing.T) {
	// cmd/main_test.go's TestMain reports $DEV_HOME/dev as the running
	// executable, which is exactly the already-installed state, so
	// relocateIfNeeded's RunningFromDevHome guard must return before
	// asking anything. The first prompt is confirmed (not declined) on
	// purpose: declining it returns from RunE before relocateIfNeeded
	// is ever reached, which would make this test a duplicate of
	// TestSetupCommand_DeclinedConfirmationMakesNoChanges rather than
	// coverage of the guard.
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

	if strings.Contains(out.String(), relocationPrompt) {
		t.Errorf("output = %q, want no relocation prompt — dev is already running from inside $DEV_HOME", out.String())
	}
}

// TestSetupCommand_OffersRelocationWhenNotYetInstalled drives `dev
// setup` end-to-end through rootCmd.Execute() while genuinely "not yet
// installed": it overrides platform.Executable (the same pattern
// root_test.go's TestPersistentPreRunE_RefusesOrdinaryCommandOutsideDevHome
// uses) to point at a fake extracted-release binary outside $DEV_HOME,
// so relocateIfNeeded's RunningFromDevHome guard actually reports
// false and its confirm prompt is genuinely exercised — unlike
// TestSetupCommand_SkipsRelocationWhenAlreadyInsideDevHome, which
// reaches relocateIfNeeded only to have the guard return before the
// prompt, and unlike
// TestSetupCommand_RelocatesDevAndDocsIntoDevHomeWhenConfirmed, which
// calls installRelocation directly and bypasses the guard and prompt.
func TestSetupCommand_OffersRelocationWhenNotYetInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")

	extractDir := t.TempDir()
	exe := filepath.Join(extractDir, "dev")
	if err := os.WriteFile(exe, []byte("fake binary"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extractDir, "README.md"), []byte("readme"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	original := platform.Executable
	platform.Executable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { platform.Executable = original })

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// First "y" confirms adding the export lines to the rc file; the
	// second "y" confirms relocateIfNeeded's "Move dev, README.md, and
	// LICENSE into $DEV_HOME now?" prompt, which only fires because the
	// platform.Executable override above makes RunningFromDevHome
	// report false.
	rootCmd.SetIn(strings.NewReader("y\ny\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(devHome, "dev")); err != nil {
		t.Errorf("expected dev to be relocated into devHome: %v", err)
	}
	if _, err := os.Stat(filepath.Join(devHome, "README.md")); err != nil {
		t.Errorf("expected README.md to be relocated into devHome: %v", err)
	}
	if _, err := os.Stat(exe); !os.IsNotExist(err) {
		t.Errorf("expected original exe to be removed after relocation, got stat err: %v", err)
	}
	if !strings.Contains(out.String(), relocationPrompt) {
		t.Errorf("output = %q, want it to contain the relocation prompt %q", out.String(), relocationPrompt)
	}
	if !strings.Contains(out.String(), "Moved dev into") {
		t.Errorf("output = %q, want it to confirm dev was relocated", out.String())
	}
}

// TestSetupCommand_DeclinedRelocationPromptDoesNotAbortSetup mirrors
// the test above but declines relocateIfNeeded's confirm prompt
// specifically (while still confirming the rc-file/shim prompt): it
// asserts the original binary is left in place and the rest of `dev
// setup` (the rc file) still completes successfully.
func TestSetupCommand_DeclinedRelocationPromptDoesNotAbortSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")

	extractDir := t.TempDir()
	exe := filepath.Join(extractDir, "dev")
	if err := os.WriteFile(exe, []byte("fake binary"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	original := platform.Executable
	platform.Executable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { platform.Executable = original })

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// "y" confirms the rc-file/shim prompt; "n" declines relocation.
	rootCmd.SetIn(strings.NewReader("y\nn\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	if _, err := os.Stat(exe); err != nil {
		t.Errorf("expected original exe to remain in place after declining relocation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(devHome, "dev")); !os.IsNotExist(err) {
		t.Errorf("expected dev not to be copied into devHome after declining relocation, got stat err: %v", err)
	}
	// Declining relocation says so, the same way declining the shim and
	// rc-file prompts already did.
	if !strings.Contains(out.String(), "No changes made.") {
		t.Errorf("output = %q, want declining relocation to report that no changes were made", out.String())
	}
	// On Windows `dev setup` never writes an rc file at all (see
	// skipOnWindowsRCFile) — but the rest of this test's assertions
	// (relocation declined, original binary untouched) are still
	// meaningful there, so only this one assertion is conditional.
	if runtime.GOOS != "windows" {
		rcPath := filepath.Join(home, ".bashrc")
		if _, err := os.Stat(rcPath); err != nil {
			t.Errorf("expected rc file to still be written after declining only the relocation prompt: %v", err)
		}
	}
}

func TestCopyExecutable_ReappliesPermissionsToExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions don't apply on windows")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("binary content"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	dest := filepath.Join(dir, "dest")
	if err := os.WriteFile(dest, []byte("stale"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := copyExecutable(src, dest); err != nil {
		t.Fatalf("copyExecutable() returned error: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat dest: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("dest permissions = %o, want 0755 (re-applied to a pre-existing file)", info.Mode().Perm())
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading dest: %v", err)
	}
	if string(got) != "binary content" {
		t.Errorf("dest content = %q, want %q", got, "binary content")
	}
}

func TestUnsupportedShellMessage_WindowsDoesNotAskForManualEditing(t *testing.T) {
	t.Parallel()
	got := unsupportedShellMessage("windows")
	if strings.Contains(got, "manually") {
		t.Errorf("unsupportedShellMessage(\"windows\") = %q, want it not to ask the user to do this manually — dev configures the environment automatically there", got)
	}
}

func TestUnsupportedShellMessage_UnknownShellKeepsManualInstructions(t *testing.T) {
	t.Parallel()
	got := unsupportedShellMessage("linux")
	if !strings.Contains(got, "manually") {
		t.Errorf("unsupportedShellMessage(\"linux\") = %q, want it to still ask for manual editing — no automatic mechanism exists for a genuinely unidentified shell", got)
	}
}

func TestReloadHint_PosixFamilyUsesSource(t *testing.T) {
	t.Parallel()
	for _, sh := range []shell.Shell{shell.Bash, shell.Zsh, shell.Fish, shell.Unknown} {
		got := reloadHint(sh, "/home/u/.bashrc")
		if got != "source /home/u/.bashrc" {
			t.Errorf("reloadHint(%v, ...) = %q, want %q", sh, got, "source /home/u/.bashrc")
		}
	}
}

func TestReloadHint_PowerShellUsesDotSourcing(t *testing.T) {
	t.Parallel()
	got := reloadHint(shell.PowerShell, `C:\Users\u\Documents\WindowsPowerShell\Microsoft.PowerShell_profile.ps1`)
	want := `. C:\Users\u\Documents\WindowsPowerShell\Microsoft.PowerShell_profile.ps1`
	if got != want {
		t.Errorf("reloadHint(PowerShell, ...) = %q, want %q — PowerShell has no \"source\" command", got, want)
	}
}
