package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/shell"
)

// fakeLookPath overrides shell.LookPath for the duration of the test so
// only the given binary names resolve, deterministically, regardless of
// what's actually installed on the machine running `go test` — the
// multi-shell loop in cmd/setup.go otherwise depends directly on the
// real PATH, which varies across dev machines and CI images (this
// project's own CI images, for instance, don't all have the same set
// of shells installed).
func fakeLookPath(t *testing.T, present ...string) {
	t.Helper()
	found := make(map[string]bool, len(present))
	for _, name := range present {
		found[name] = true
	}
	original := shell.LookPath
	shell.LookPath = func(file string) (string, error) {
		if found[file] {
			return "/fake/" + file, nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { shell.LookPath = original })
}

// skipOnWindowsRCFile skips a test that asserts on a specific rc-file
// path. On a real Windows machine, PowerShell is essentially always
// present (shell.RCPath asks a real PowerShell process for its own
// $PROFILE value there — supported whenever pwsh or powershell is
// reachable at all, true on virtually every Windows install, built-in
// Windows PowerShell included), so fakeLookPath(t, "bash") alone can't
// exclude it from these tests' target list the way it does on
// Linux/macOS. This skip stays conservative anyway: a CI runner's
// actual $PROFILE value depends on its home-directory layout and
// installed PowerShell version, neither knowable ahead of time to
// assert against here, so these tests skip on Windows rather than
// assert against a path only discoverable by actually running the
// subprocess.
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
	fakeLookPath(t, "bash")
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
	fakeLookPath(t, "bash")
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
	// Nothing resolves via LookPath: no configurable shell is "present"
	// at all, regardless of what's actually installed on the machine
	// running this test — the only way to deterministically reproduce
	// the "nothing to configure automatically" case this test exists
	// for.
	fakeLookPath(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	// An unrecognized $SHELL makes shell.Detect() return Unknown on
	// Linux/macOS, which is what the manual-fallback section prints
	// FunctionLines for. The assertion below still computes the
	// expected text via unsupportedShellMessage(runtime.GOOS) for
	// generality across Linux/macOS — it just never sees "windows"
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

// TestSetupCommand_NoShellPresentPrintsManualInstructionsAndMakesNoChanges
// covers the case where IsPresent finds nothing configurable at all
// (an environment with none of bash/zsh/fish/pwsh/powershell on PATH,
// and an unrecognized $SHELL): there is nothing to prompt for, so
// `dev setup` must print Detect()'s manual-fallback instructions and
// touch no rc file, without needing any confirmation to decline.
func TestSetupCommand_NoShellPresentPrintsManualInstructionsAndMakesNoChanges(t *testing.T) {
	fakeLookPath(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/some-unrecognized-shell")

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader(""))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	for _, rc := range []string{".bashrc", ".zshrc", filepath.Join(".config", "fish", "config.fish")} {
		path := filepath.Join(home, rc)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected no shell to be auto-configured when nothing is present, but %s exists", path)
		}
	}
	if !strings.Contains(out.String(), "dev() {") {
		t.Errorf("output = %q, want it to print the dev wrapper function for manual setup", out.String())
	}
}

func TestSetupCommand_RunningTwiceDoesNotDuplicateOrError(t *testing.T) {
	skipOnWindowsRCFile(t)
	fakeLookPath(t, "bash")
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

// TestSetupCommand_ConfiguresEveryPresentShellInOneRun pins this
// feature's core behavior: a machine with two shells installed (e.g.
// Bash and Zsh on Linux, or PowerShell and Git Bash on Windows) gets
// BOTH configured in a single `dev setup` run, not just whichever one
// invoked it — the exact gap a real user hit (PowerShell configured,
// Git Bash left with nothing).
func TestSetupCommand_ConfiguresEveryPresentShellInOneRun(t *testing.T) {
	skipOnWindowsRCFile(t)
	fakeLookPath(t, "bash", "zsh")
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// One "y" per shell offered: Bash then Zsh, shell.Configurable()'s
	// fixed order.
	rootCmd.SetIn(strings.NewReader("y\ny\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	for _, rc := range []string{".bashrc", ".zshrc"} {
		path := filepath.Join(home, rc)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if !strings.Contains(string(data), "dev() {") {
			t.Errorf("%s = %q, want it to contain the dev wrapper function", path, data)
		}
	}
}

// TestSetupCommand_SkipsAlreadyConfiguredShellOnRerun covers the
// scenario the user asked for explicitly: PowerShell (here, Bash)
// already configured by an earlier run, then Zsh installed afterward —
// re-running `dev setup` must offer ONLY Zsh, not re-prompt for Bash's
// already-current block.
func TestSetupCommand_SkipsAlreadyConfiguredShellOnRerun(t *testing.T) {
	skipOnWindowsRCFile(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	fakeLookPath(t, "bash")
	var first bytes.Buffer
	rootCmd.SetOut(&first)
	rootCmd.SetErr(&first)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"setup"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("first `dev setup` returned error: %v", err)
	}

	// Zsh "installed" afterward.
	fakeLookPath(t, "bash", "zsh")
	var second bytes.Buffer
	rootCmd.SetOut(&second)
	rootCmd.SetErr(&second)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("second `dev setup` returned error: %v", err)
	}

	bashRC := filepath.Join(home, ".bashrc")
	if strings.Contains(second.String(), fmt.Sprintf("added to %s", bashRC)) {
		t.Errorf("second run output = %q, want it not to re-offer the already-current Bash block", second.String())
	}
	zshRC := filepath.Join(home, ".zshrc")
	if !strings.Contains(second.String(), fmt.Sprintf("added to %s", zshRC)) {
		t.Errorf("second run output = %q, want it to offer the newly-present Zsh block", second.String())
	}
	data, err := os.ReadFile(zshRC)
	if err != nil {
		t.Fatalf("reading %s: %v", zshRC, err)
	}
	if !strings.Contains(string(data), "dev() {") {
		t.Errorf("%s = %q, want it to contain the dev wrapper function", zshRC, data)
	}
}

// TestSetupCommand_DecliningOneShellStillWritesTheOther covers
// independence between targets: declining Bash's prompt must not
// prevent Zsh's own block from being written when Zsh is confirmed.
func TestSetupCommand_DecliningOneShellStillWritesTheOther(t *testing.T) {
	skipOnWindowsRCFile(t)
	fakeLookPath(t, "bash", "zsh")
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// "n" for Bash (first, by Configurable()'s order), "y" for Zsh.
	rootCmd.SetIn(strings.NewReader("n\ny\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(home, ".bashrc")); !os.IsNotExist(err) {
		t.Errorf("expected .bashrc not to be created after declining its prompt, stat err=%v", err)
	}
	zshData, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		t.Fatalf("reading .zshrc: %v", err)
	}
	if !strings.Contains(string(zshData), "dev() {") {
		t.Errorf(".zshrc = %q, want it to contain the dev wrapper function despite Bash's prompt being declined", zshData)
	}
}

// TestSetupCommand_OneCorruptedTargetDoesNotBlockTheOthers covers the
// spec's per-target error-handling guarantee: a pre-existing rc file
// UpsertBlock refuses to touch (an unterminated marker pair, here
// .bashrc) must not stop a different, healthy target (.zshrc) from
// still being offered and written in the same run. The command still
// reports the failure via a non-nil error, so the user knows to fix
// the broken file by hand, but only after every other target has had
// its chance.
func TestSetupCommand_OneCorruptedTargetDoesNotBlockTheOthers(t *testing.T) {
	skipOnWindowsRCFile(t)
	fakeLookPath(t, "bash", "zsh")
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	bashRC := filepath.Join(home, ".bashrc")
	corrupted := "alias a='1'\n# BEGIN dev shell setup\nexport OLD=1\n"
	if err := os.WriteFile(bashRC, []byte(corrupted), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// "y" for Bash's prompt (fails inside UpsertBlock), "y" for Zsh's.
	rootCmd.SetIn(strings.NewReader("y\ny\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("`dev setup` returned nil error, want it to report the corrupted .bashrc")
	}

	zshData, readErr := os.ReadFile(filepath.Join(home, ".zshrc"))
	if readErr != nil {
		t.Fatalf("reading .zshrc: %v", readErr)
	}
	if !strings.Contains(string(zshData), "dev() {") {
		t.Errorf(".zshrc = %q, want it to contain the dev wrapper function despite .bashrc failing", zshData)
	}
	bashData, readErr := os.ReadFile(bashRC)
	if readErr != nil {
		t.Fatalf("reading .bashrc: %v", readErr)
	}
	if string(bashData) != corrupted {
		t.Errorf(".bashrc = %q, want it left exactly as the corrupted original, untouched", bashData)
	}
}

// TestSetupCommand_RCPathErrorAbortsImmediately covers a genuine
// RCPath error (as opposed to supported=false) — an unresolvable
// $HOME, the one real way to trigger this today — must abort the
// whole command immediately, not be swallowed by the per-target
// "report and continue" handling meant only for
// BlockUpToDate/UpsertBlock failures.
func TestSetupCommand_RCPathErrorAbortsImmediately(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("$HOME is not the profile-dir env var on windows")
	}
	fakeLookPath(t, "bash")
	t.Setenv("HOME", "")
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/bash")
	fakeInstalledDev(t, devHome)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
	})

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("`dev setup` returned nil error when $HOME is unresolvable")
	}
}

// TestSetupCommand_PresentButRCPathUnsupportedDoesNotCountAsPresent
// covers a shell that resolves via LookPath but whose RCPath reports
// supported=false (here: a faked "pwsh" on PATH with no real
// pwsh/powershell binary behind it, so the real $PROFILE subprocess
// query genuinely fails) — it must not count toward `present`, so the
// manual-fallback branch still triggers when that was the only
// candidate.
func TestSetupCommand_PresentButRCPathUnsupportedDoesNotCountAsPresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on real Windows, pwsh/powershell's $PROFILE lookup succeeds almost always, defeating this test's premise")
	}
	fakeLookPath(t, "pwsh")
	home := t.TempDir()
	t.Setenv("HOME", home)
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("SHELL", "/bin/some-unrecognized-shell")
	fakeInstalledDev(t, devHome)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader(""))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error: %v", err)
	}

	if !strings.Contains(out.String(), "dev() {") {
		t.Errorf("output = %q, want the manual-fallback instructions since the only present shell (PowerShell) isn't actually auto-editable here", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); !os.IsNotExist(err) {
		t.Error("expected no .bashrc to be created")
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
	fakeLookPath(t, "bash")
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
	fakeLookPath(t, "bash")
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
	fakeLookPath(t, "bash")
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
