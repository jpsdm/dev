// cmd's tests mutate the shared package-level rootCmd (adding/removing
// commands, SetArgs, SetOut/SetErr), so none of them use t.Parallel()
// against each other.
package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/update"
)

func TestVersionCommand_MatchesVersionFlag(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"version"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev version` returned error: %v", err)
	}

	got := strings.TrimSpace(out.String())
	want := versionString()
	if got != want {
		t.Errorf("`dev version` output = %q, want %q (must match `dev --version`)", got, want)
	}
}

// TestVersionFlag_MatchesVersionCommand actually exercises `dev
// --version` (not just versionString() directly), so it fails if
// rootCmd's SetVersionTemplate wiring in root.go's init() ever
// regresses to Cobra's default bare-version output.
func TestVersionFlag_MatchesVersionCommand(t *testing.T) {
	var versionOut bytes.Buffer
	rootCmd.SetOut(&versionOut)
	rootCmd.SetErr(&versionOut)
	rootCmd.SetArgs([]string{"version"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev version` returned error: %v", err)
	}

	var flagOut bytes.Buffer
	rootCmd.SetOut(&flagOut)
	rootCmd.SetErr(&flagOut)
	rootCmd.SetArgs([]string{"--version"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		// Unlike SetArgs, a flag's parsed value persists on rootCmd's
		// pflag.FlagSet across Execute() calls (rootCmd is shared by
		// every test in this file). Leaving --version's value at true
		// would make the next test's Execute() behave as if --version
		// had been passed, regardless of its own args.
		if err := rootCmd.Flags().Set("version", "false"); err != nil {
			t.Fatalf("resetting --version flag: %v", err)
		}
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev --version` returned error: %v", err)
	}

	got := strings.TrimSpace(flagOut.String())
	want := strings.TrimSpace(versionOut.String())
	if got != want {
		t.Errorf("`dev --version` output = %q, want it to match `dev version` output %q", got, want)
	}
}

func TestNoSubcommand_PrintsHelpWithoutError(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev` with no args returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Errorf("`dev` with no args output = %q, want it to contain usage/help text", out.String())
	}
}

func TestExitCodeFor_UserAbortedPromptExitsZero(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("prompting for workspace path: %w", huh.ErrUserAborted)
	if got := exitCodeFor(err); got != 0 {
		t.Errorf("exitCodeFor(%v) = %d, want 0 (Ctrl+C at a prompt is a cancellation, not a failure)", err, got)
	}
}

func TestExitCodeFor_OrdinaryErrorExitsOne(t *testing.T) {
	t.Parallel()
	err := errors.New("checksum mismatch")
	if got := exitCodeFor(err); got != 1 {
		t.Errorf("exitCodeFor(%v) = %d, want 1", err, got)
	}
}

// outsideDevHome makes platform.RunningFromDevHome report false for
// the duration of one test, overriding cmd/main_test.go's TestMain
// default (which makes every test in this package look installed).
// Every "outside $DEV_HOME" test must call this — without it a test
// named for the outside-$DEV_HOME case silently runs inside the
// looks-installed default and asserts nothing.
func outsideDevHome(t *testing.T) {
	t.Helper()
	outside := t.TempDir()
	original := platform.Executable
	platform.Executable = func() (string, error) { return filepath.Join(outside, "dev"), nil }
	t.Cleanup(func() { platform.Executable = original })
}

func TestPersistentPreRunE_RefusesOrdinaryCommandOutsideDevHome(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	outsideDevHome(t)
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{"lang", "current"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("rootCmd.Execute() returned nil error for an ordinary command run outside $DEV_HOME")
	}
	if !strings.Contains(err.Error(), "dev setup") {
		t.Errorf("error = %q, want it to mention `dev setup`", err.Error())
	}
}

func TestPersistentPreRunE_AllowsSetupOutsideDevHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DEV_HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/bash")
	outsideDevHome(t)
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetIn(strings.NewReader("n\n"))
	rootCmd.SetArgs([]string{"setup"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev setup` returned error when run outside $DEV_HOME (it must always be allowed): %v", err)
	}
}

func TestPersistentPreRunE_AllowsVersionSubcommandOutsideDevHome(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	outsideDevHome(t)
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"version"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev version` returned error when run outside $DEV_HOME (it must always be allowed): %v", err)
	}
}

// TestPersistentPreRunE_AllowsVersionFlagOutsideDevHome pins the
// observable behavior (`dev --version` works from anywhere): Cobra's
// own Execute() short-circuits --version before PersistentPreRunE
// runs at all.
func TestPersistentPreRunE_AllowsVersionFlagOutsideDevHome(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	outsideDevHome(t)
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"--version"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		if err := rootCmd.Flags().Set("version", "false"); err != nil {
			t.Fatalf("resetting --version flag: %v", err)
		}
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev --version` returned error when run outside $DEV_HOME (it must always be allowed): %v", err)
	}
}

// TestPersistentPreRunE_AllowsHelpSubcommandOutsideDevHome pins that
// `dev help <cmd>` works before dev is installed. Cobra's own help
// short-circuit covers the --help flag forms (`dev --help`, `dev lang
// --help`), but `help` is a real command whose Name() reaches this
// gate — so without an explicit exemption the one help form a
// not-yet-installed user is most likely to try would be refused while
// the other two worked.
func TestPersistentPreRunE_AllowsHelpSubcommandOutsideDevHome(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	outsideDevHome(t)
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"help", "lang"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev help lang` returned error when run outside $DEV_HOME (it must always be allowed): %v", err)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Errorf("`dev help lang` output = %q, want it to contain usage text", out.String())
	}
}

func TestExecute_ErrorPathRespectsVerboseFlag(t *testing.T) {
	// Execute() itself calls os.Exit on failure, which can't be
	// exercised in-process. This test instead pins the two things
	// Execute() composes: rootCmd.Execute() surfaces the RunE error,
	// and PersistentPreRun's cliutil.SetVerbose(verboseFlag) call
	// makes cliutil.PrintError honor --verbose on that same error.
	inner := errors.New("checksum mismatch")
	failing := &cobra.Command{
		Use:           "test-fail",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("installing node 22: %w", inner)
		},
	}
	rootCmd.AddCommand(failing)
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	t.Cleanup(func() {
		rootCmd.RemoveCommand(failing)
		rootCmd.SetArgs(nil)
		cliutil.SetVerbose(false)
	})

	cases := []struct {
		args      []string
		wantChain bool
	}{
		{args: []string{"test-fail"}, wantChain: false},
		{args: []string{"test-fail", "--verbose"}, wantChain: true},
	}

	for _, tc := range cases {
		rootCmd.SetArgs(tc.args)
		err := rootCmd.Execute()
		if err == nil {
			t.Fatalf("args=%v: expected an error from test-fail", tc.args)
		}

		var buf bytes.Buffer
		origStderr := cliutil.Stderr
		cliutil.Stderr = &buf
		cliutil.PrintError(err)
		cliutil.Stderr = origStderr

		gotChain := strings.Contains(buf.String(), "caused by: checksum mismatch")
		if gotChain != tc.wantChain {
			t.Errorf("args=%v: chain present = %v, want %v (output: %q)", tc.args, gotChain, tc.wantChain, buf.String())
		}
	}
}

func TestPersistentPostRunE_PrintsNoticeToStderrForOrdinaryCommand(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	// Clear TestMain's process-wide DEV_NO_UPDATE_CHECK=1 default so the
	// notice-computation path this test asserts on actually runs.
	t.Setenv("DEV_NO_UPDATE_CHECK", "")
	withVersion(t, "v0.2.0")

	server := stubUpdateServer(t, "v0.3.0", nil)
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"lang", "current"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev lang current` returned error: %v", err)
	}

	want := "A newer version is available: v0.3.0 (run 'dev update')"
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
	}
	if strings.Contains(stdout.String(), want) {
		t.Error("stdout unexpectedly contains the update notice — it belongs on stderr for an ordinary command")
	}
}

func TestPersistentPostRunE_PrintsNoticeToStdoutForVersionCommand(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	// Clear TestMain's process-wide DEV_NO_UPDATE_CHECK=1 default so the
	// notice-computation path this test asserts on actually runs.
	t.Setenv("DEV_NO_UPDATE_CHECK", "")
	withVersion(t, "v0.2.0")

	server := stubUpdateServer(t, "v0.3.0", nil)
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"version"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev version` returned error: %v", err)
	}

	want := "A newer version is available: v0.3.0 (run 'dev update')"
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q (dev version's own notice)", stdout.String(), want)
	}
	if strings.Contains(stderr.String(), want) {
		t.Error("stderr unexpectedly contains the update notice too — dev version must not print it twice")
	}
}

func TestPersistentPostRunE_NoNoticeWhenAlreadyLatest(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	// Clear TestMain's process-wide DEV_NO_UPDATE_CHECK=1 default so the
	// notice-computation path this test asserts on actually runs.
	t.Setenv("DEV_NO_UPDATE_CHECK", "")
	withVersion(t, "v0.2.0")

	server := stubUpdateServer(t, "v0.2.0", nil)
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"lang", "current"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev lang current` returned error: %v", err)
	}
	if strings.Contains(stderr.String(), "newer version") {
		t.Errorf("stderr = %q, want no update notice when already on the latest version", stderr.String())
	}
}

func TestPersistentPostRunE_SkipsForUpdateCommand(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	withVersion(t, "v0.2.0")

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(update.LatestRelease{TagName: "v0.2.0"})
	}))
	defer server.Close()
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"update"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev update` returned error: %v", err)
	}
	// dev update's own RunE makes exactly one FetchLatest call already
	// (Task 4) — PersistentPostRunE must not make a second one.
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("network calls = %d, want exactly 1 (dev update's own check, not a second one from PersistentPostRunE)", got)
	}
}

func TestPersistentPostRunE_SkipsWhenEnvVarSet(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("DEV_NO_UPDATE_CHECK", "1")
	withVersion(t, "v0.2.0")

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"lang", "current"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev lang current` returned error: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("network calls = %d, want 0 (DEV_NO_UPDATE_CHECK is set)", got)
	}
	if strings.Contains(stderr.String(), "newer version") {
		t.Errorf("stderr = %q, want no notice when DEV_NO_UPDATE_CHECK is set", stderr.String())
	}
}

func TestPersistentPostRunE_SkipsWhenNotInstalled(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	outsideDevHome(t)
	withVersion(t, "v0.2.0")

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"version"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev version` returned error: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("network calls = %d, want 0 (not running from $DEV_HOME)", got)
	}
}

func TestPersistentPostRunE_SkipsForNonCleanVersion(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	// Version left at its test-default "dev" — non-clean.

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()
	original := newUpdateClient
	newUpdateClient = func() *update.Client { return update.NewClientWithBaseURL(server.URL) }
	t.Cleanup(func() { newUpdateClient = original })

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"lang", "current"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev lang current` returned error: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("network calls = %d, want 0 (Version is a non-clean local-build string)", got)
	}
}
