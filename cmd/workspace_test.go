// workspace's tests substitute the package-level promptWorkspacePath
// var rather than driving a real interactive Huh prompt through piped
// stdin — the same substitution pattern cmd/lang_test.go already uses
// for langManager. None of them use t.Parallel() against each other
// since they share that package-level var.
package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jpsdm/dev/internal/config"
	"github.com/jpsdm/dev/internal/metrics"
	"github.com/jpsdm/dev/internal/workspace"
)

// stubWorkspacePrompt stubs promptWorkspace so the workspace ends up
// at exactly answer, splitting it into a name/location pair
// (filepath.Base/filepath.Dir) rather than answer's callers having to
// know about the name+location split resolveWorkspaceRoot now does —
// filepath.Join(normalizeWorkspacePath(Dir(answer)), Base(answer))
// reconstructs answer exactly, so every existing caller's expected
// final path is unaffected by this prompt's two-field split.
func stubWorkspacePrompt(t *testing.T, answer string) *int {
	t.Helper()
	calls := 0
	orig := promptWorkspace
	promptWorkspace = func(in io.Reader, out io.Writer, defaultName, defaultLocation string) (string, string, error) {
		calls++
		return filepath.Base(answer), filepath.Dir(answer), nil
	}
	t.Cleanup(func() { promptWorkspace = orig })
	return &calls
}

// TestPromptWorkspaceFields_ReadsFromInjectedReader exercises the real
// prompt function directly (not the stubbed package var), confirming
// it reads both answers from an injected io.Reader with no TTY
// involved, and — the property that actually matters here — that the
// second answer survives at all: a Huh-form-based version of this
// prompt silently dropped it (see promptWorkspace's doc comment).
func TestPromptWorkspaceFields_ReadsFromInjectedReader(t *testing.T) {
	in := strings.NewReader("MyFolder\n/tmp/chosen\n")
	var out bytes.Buffer

	name, location, err := promptWorkspaceFields(in, &out, "Workspace", "/tmp/default")
	if err != nil {
		t.Fatalf("promptWorkspaceFields returned error: %v", err)
	}
	if name != "MyFolder" {
		t.Errorf("name = %q, want %q", name, "MyFolder")
	}
	if location != "/tmp/chosen" {
		t.Errorf("location = %q, want %q", location, "/tmp/chosen")
	}
}

// TestPromptWorkspaceFields_EmptyAnswersKeepDefaults confirms blank
// lines (the user just pressing enter) keep the pre-filled defaults,
// rather than persisting an empty name or location.
func TestPromptWorkspaceFields_EmptyAnswersKeepDefaults(t *testing.T) {
	in := strings.NewReader("\n\n")
	var out bytes.Buffer

	name, location, err := promptWorkspaceFields(in, &out, "Workspace", "/tmp/default")
	if err != nil {
		t.Fatalf("promptWorkspaceFields returned error: %v", err)
	}
	if name != "Workspace" {
		t.Errorf("name = %q, want the default %q", name, "Workspace")
	}
	if location != "/tmp/default" {
		t.Errorf("location = %q, want the default %q", location, "/tmp/default")
	}
}

func TestWorkspaceCommand_FirstUsePromptsAndPersistsPath(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	workspaceDir := t.TempDir()
	stubWorkspacePrompt(t, workspaceDir)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace` returned error: %v", err)
	}

	if !strings.Contains(out.String(), workspaceDir) {
		t.Errorf("output = %q, want it to mention the workspace path %q", out.String(), workspaceDir)
	}
	for _, name := range []string{"src", "scratch", "archive", "base"} {
		if _, err := os.Stat(filepath.Join(workspaceDir, name)); err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
		}
	}

	cfgPath := filepath.Join(devHome, "config", "config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reading config file: %v", err)
	}
	// Compared via the decoded struct field, not a raw substring match
	// against the JSON bytes: JSON escapes every path separator
	// backslash as "\\", so a literal Windows path can never appear as
	// a substring of its own JSON encoding.
	if cfg.Workspace.Path != workspaceDir {
		t.Errorf("persisted Workspace.Path = %q, want %q", cfg.Workspace.Path, workspaceDir)
	}
}

// TestWorkspaceCommand_FirstUseAsksNameThenLocation drives `dev
// workspace` end-to-end through rootCmd.Execute() with the real
// (unstubbed) promptWorkspaceFields, proving the production wiring —
// not just the pure prompt function in isolation — correctly reads
// the name answer, then the location answer, from the same stdin.
func TestWorkspaceCommand_FirstUseAsksNameThenLocation(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	location := t.TempDir()

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("MyProjects\n" + location + "\n"))
	rootCmd.SetArgs([]string{"workspace"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace` returned error: %v", err)
	}

	want := filepath.Join(location, "MyProjects")
	if !strings.Contains(out.String(), want) {
		t.Errorf("output = %q, want it to mention %q", out.String(), want)
	}
	for _, name := range []string{"src", "scratch", "archive", "base"} {
		if _, err := os.Stat(filepath.Join(want, name)); err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
		}
	}
}

func TestWorkspaceCommand_SecondRunDoesNotReprompt(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	workspaceDir := t.TempDir()
	calls := stubWorkspacePrompt(t, workspaceDir)

	run := func() {
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		rootCmd.SetArgs([]string{"workspace"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("`dev workspace` returned error: %v", err)
		}
	}
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	run()
	run()

	if *calls != 1 {
		t.Errorf("prompt called %d times, want exactly 1 (second run should reuse the persisted path)", *calls)
	}
}

func TestWorkspaceCommand_WsAliasWorks(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	workspaceDir := t.TempDir()
	stubWorkspacePrompt(t, workspaceDir)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"ws"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev ws` returned error: %v", err)
	}
	if !strings.Contains(out.String(), workspaceDir) {
		t.Errorf("output = %q, want it to mention the workspace path", out.String())
	}
}

// writeConfiguredWorkspace pre-populates DEV_HOME's config file so
// resolveWorkspaceRoot skips the first-use prompt entirely, pointing
// at root (already containing an empty scaffold).
func writeConfiguredWorkspace(t *testing.T, root string) {
	t.Helper()
	if err := workspace.EnsureScaffold(root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	cfg := &config.Config{Workspace: config.WorkspaceConfig{Path: root}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("setup: %v", err)
	}
}

func TestWorkspaceNewCommand_CopiesBaseIntoSrc(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	if err := os.WriteFile(filepath.Join(root, "base", "README.md"), []byte("template"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "new", "api"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace new` returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "src", "api", "README.md"))
	if err != nil {
		t.Fatalf("reading copied file: %v", err)
	}
	if string(got) != "template" {
		t.Errorf("copied content = %q, want %q", got, "template")
	}
}

func TestWorkspaceNewCommand_ExistingProjectErrors(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	if err := os.MkdirAll(filepath.Join(root, "src", "api"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "new", "api"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("`dev workspace new` returned nil error for an already-existing project")
	}
}

func TestWorkspaceScratchCommand_WsSAliasChain(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"ws", "s", "experiment"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev ws s` returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "scratch", "experiment")); err != nil {
		t.Errorf("expected scratch/experiment to exist: %v", err)
	}
}

func TestWorkspaceCommand_UnknownSubcommandErrors(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	workspaceDir := t.TempDir()
	stubWorkspacePrompt(t, workspaceDir)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "nwe", "api"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("`dev workspace nwe api` returned nil error, want an unknown-command error")
	}
	if strings.Contains(out.String(), "✓") {
		t.Errorf("output = %q, want no success checkmark for an unknown subcommand", out.String())
	}
}

func TestWorkspaceCommand_NormalizesTildeAndRelativePaths(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	home := t.TempDir()
	// Both env vars: os.UserHomeDir() reads $HOME on Unix but only
	// %USERPROFILE% on Windows (Go's stdlib never consults HOME there),
	// so setting only one leaves this test's override silently ignored
	// on whichever platform its own var isn't read on.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	stubWorkspacePrompt(t, "~/myworkspace")

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace` returned error: %v", err)
	}

	want := filepath.Join(home, "myworkspace")
	cfgPath := filepath.Join(devHome, "config", "config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reading config file: %v", err)
	}
	if cfg.Workspace.Path != want {
		t.Errorf("persisted Workspace.Path = %q, want the normalized absolute path %q", cfg.Workspace.Path, want)
	}
	// HasPrefix, not Contains: a bare substring search for "~" is a
	// false positive on Windows CI runners, where %TEMP% itself can
	// resolve to a short 8.3-form path segment like "RUNNER~1" — an
	// OS-level naming artifact that has nothing to do with whether our
	// own leading-~ shorthand was expanded.
	if strings.HasPrefix(cfg.Workspace.Path, "~") {
		t.Errorf("persisted Workspace.Path = %q, want no unexpanded leading ~", cfg.Workspace.Path)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected normalized workspace %s to exist: %v", want, err)
	}
}

func TestWorkspaceCleanCommand_RemovesIgnoredFiles(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "clean", "api"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace clean api` returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "debug.log")); !os.IsNotExist(err) {
		t.Error("debug.log still exists after `dev workspace clean api`")
	}
	if !strings.Contains(out.String(), "Cleaned") {
		t.Errorf("output = %q, want it to confirm the project was cleaned", out.String())
	}
}

func TestWorkspaceCleanCommand_DryRunMatchesSpecFormat(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "clean", "api", "--dry-run"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		// Like --version in root_test.go, --dry-run's parsed value
		// persists on workspaceCleanCmd's shared pflag.FlagSet across
		// Execute() calls, so it must be reset or it leaks into later
		// tests that reuse this same command without passing the flag.
		if err := workspaceCleanCmd.Flags().Set("dry-run", "false"); err != nil {
			t.Fatalf("resetting --dry-run flag: %v", err)
		}
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace clean api --dry-run` returned error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "The following files would be removed:") {
		t.Errorf("output = %q, want the spec's exact opening line", got)
	}
	if !strings.Contains(got, "debug.log") {
		t.Errorf("output = %q, want it to list debug.log", got)
	}
	if !strings.Contains(got, "No files were deleted.") {
		t.Errorf("output = %q, want the spec's exact closing line", got)
	}
	if _, err := os.Stat(filepath.Join(proj, "debug.log")); err != nil {
		t.Errorf("debug.log removed during a dry run: %v", err)
	}
}

func TestWorkspaceCleanCommand_DryRunWithNothingToCleanPrintsNothingToClean(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "app.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "clean", "api", "--dry-run"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		if err := workspaceCleanCmd.Flags().Set("dry-run", "false"); err != nil {
			t.Fatalf("resetting --dry-run flag: %v", err)
		}
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace clean api --dry-run` returned error: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "The following files would be removed:") {
		t.Errorf("output = %q, want no misleading header when nothing would be removed", got)
	}
	if !strings.Contains(got, "Nothing to clean") {
		t.Errorf("output = %q, want it to say Nothing to clean", got)
	}
}

func TestWorkspaceCleanCommand_NeitherNameNorAllErrors(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "clean"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("`dev workspace clean` with neither a name nor --all returned nil error")
	}
}

func TestWorkspaceCleanCommand_NameAndAllTogetherErrors(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "clean", "api", "--all"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		if err := workspaceCleanCmd.Flags().Set("all", "false"); err != nil {
			t.Fatalf("resetting --all flag: %v", err)
		}
	})

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("`dev workspace clean api --all` (both name and --all) returned nil error")
	}
}

func TestWorkspaceCleanCommand_AllDeclinedConfirmationMakesNoChanges(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("n\n"))
	rootCmd.SetArgs([]string{"workspace", "clean", "--all"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
		if err := workspaceCleanCmd.Flags().Set("all", "false"); err != nil {
			t.Fatalf("resetting --all flag: %v", err)
		}
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace clean --all` (declined) returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "debug.log")); err != nil {
		t.Errorf("debug.log removed despite declining confirmation: %v", err)
	}
	if !strings.Contains(out.String(), "No changes made.") {
		t.Errorf("output = %q, want it to confirm no changes were made", out.String())
	}
}

func TestWorkspaceCleanCommand_AllConfirmedCleansEveryProject(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"workspace", "clean", "--all"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
		if err := workspaceCleanCmd.Flags().Set("all", "false"); err != nil {
			t.Fatalf("resetting --all flag: %v", err)
		}
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace clean --all` (confirmed) returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "debug.log")); !os.IsNotExist(err) {
		t.Error("debug.log still exists after confirmed `dev workspace clean --all`")
	}
}

func TestWorkspaceCleanCommand_AllReportsOneProjectFailureWithoutSuppressingTheOthers(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)

	good := filepath.Join(root, "src", "good")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(good, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(good, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Same "directory named .gitignore" trick used at the
	// internal/workspace layer, forcing this one project's clean to
	// fail deterministically without relying on permission bits.
	if err := os.MkdirAll(filepath.Join(root, "src", "broken", ".gitignore"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader("y\n"))
	rootCmd.SetArgs([]string{"workspace", "clean", "--all"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
		if err := workspaceCleanCmd.Flags().Set("all", "false"); err != nil {
			t.Fatalf("resetting --all flag: %v", err)
		}
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("`dev workspace clean --all` returned nil error despite one project failing")
	}
	if _, statErr := os.Stat(filepath.Join(good, "debug.log")); !os.IsNotExist(statErr) {
		t.Error("the working project ('good') was not cleaned despite the other project ('broken') failing")
	}
	if !strings.Contains(out.String(), "good") {
		t.Errorf("output = %q, want it to mention the successfully-cleaned project", out.String())
	}
}

func TestWorkspaceCleanCommand_AllDryRunSkipsConfirmationPrompt(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// No stdin reader set at all: if the command tried to read a
	// confirmation, Confirm's bufio.Scanner would hit EOF and treat it
	// as "no" rather than hanging — but a "no" would also mean nothing
	// gets cleaned, so this test's real assertion (debug.log IS removed)
	// only passes if no confirmation was requested at all.
	rootCmd.SetArgs([]string{"workspace", "clean", "--all", "--dry-run"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		if err := workspaceCleanCmd.Flags().Set("all", "false"); err != nil {
			t.Fatalf("resetting --all flag: %v", err)
		}
		if err := workspaceCleanCmd.Flags().Set("dry-run", "false"); err != nil {
			t.Fatalf("resetting --dry-run flag: %v", err)
		}
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace clean --all --dry-run` returned error: %v", err)
	}
	if !strings.Contains(out.String(), "debug.log") {
		t.Errorf("output = %q, want it to preview debug.log without a confirmation prompt", out.String())
	}
	if _, err := os.Stat(filepath.Join(proj, "debug.log")); err != nil {
		t.Errorf("debug.log removed during a dry run: %v", err)
	}
}

func TestWorkspaceCommand_NormalizesPreExistingConfigPath(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	home := t.TempDir()
	// Both env vars — see TestWorkspaceCommand_NormalizesTildeAndRelativePaths
	// for why os.UserHomeDir() needs both to be overridden cross-platform.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	path, err := config.DefaultPath()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	cfg := &config.Config{Workspace: config.WorkspaceConfig{Path: "~/handwritten"}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace` returned error: %v", err)
	}

	want := filepath.Join(home, "handwritten")
	gotCfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reading config file: %v", err)
	}
	if gotCfg.Workspace.Path != want {
		t.Errorf("persisted Workspace.Path = %q, want the hand-edited ~ path normalized and re-persisted as %q", gotCfg.Workspace.Path, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected normalized workspace %s to exist: %v", want, err)
	}
}

func TestWorkspaceArchiveCommand_MovesToArchive(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "app.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "archive", "api"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace archive api` returned error: %v", err)
	}
	if _, err := os.Stat(proj); !os.IsNotExist(err) {
		t.Error("src/api still exists after archive")
	}
	if _, err := os.Stat(filepath.Join(root, "archive", "api", "app.go")); err != nil {
		t.Errorf("archive/api/app.go missing: %v", err)
	}
	if !strings.Contains(out.String(), "Archived") {
		t.Errorf("output = %q, want it to confirm the archive", out.String())
	}
}

func TestWorkspaceArchiveCommand_DryRunDoesNotMove(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "app.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "archive", "api", "--dry-run"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		if err := workspaceArchiveCmd.Flags().Set("dry-run", "false"); err != nil {
			t.Fatalf("resetting --dry-run flag: %v", err)
		}
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace archive api --dry-run` returned error: %v", err)
	}
	if _, err := os.Stat(proj); err != nil {
		t.Errorf("src/api missing after a dry-run archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "archive", "api")); !os.IsNotExist(err) {
		t.Error("archive/api exists after a dry-run archive")
	}
}

func TestWorkspaceArchiveCommand_ExistingTargetErrors(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "archive", "api"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "archive", "api"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("`dev workspace archive api` returned nil error when archive/api already exists")
	}
}

func TestWorkspaceArchiveCommand_WsAAliasChain(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	proj := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"ws", "a", "api"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev ws a api` returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "archive", "api")); err != nil {
		t.Errorf("expected archive/api to exist: %v", err)
	}
}

func TestFormatSize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		bytes int64
		want  string
	}{
		{500, "500 B"},
		{1024, "1.00 KB"},
		{44040192, "42.00 MB"},
		{9041732239, "8.42 GB"},
		// 1 PiB: exercises a sparse file's "apparent size" reaching
		// far past the last unit (TB). Before the clamp fix, this
		// panicked with "index out of range [4] with length 4" since
		// exp grew unbounded past len(units)-1. 1<<50 bytes is
		// 2^50 / 2^40 = 2^10 = 1024 TB.
		{int64(1) << 50, "1024.00 TB"},
	}
	for _, tc := range cases {
		if got := formatSize(tc.bytes); got != tc.want {
			t.Errorf("formatSize(%d) = %q, want %q", tc.bytes, got, tc.want)
		}
	}
}

func TestPrintMetricsReport_EmptyWorkspaceShowsNone(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	report := metrics.Report{
		Path: "/tmp/ws",
		Directories: []metrics.DirStats{
			{Name: "src", ProjectCount: 0, Size: 0},
			{Name: "scratch", ProjectCount: 0, Size: 0},
			{Name: "archive", ProjectCount: 0, Size: 0},
			{Name: "base", ProjectCount: -1, Size: 0},
		},
	}
	printMetricsReport(&out, report)

	got := out.String()
	if !strings.Contains(got, "base") || !strings.Contains(got, "-") {
		t.Errorf("output = %q, want it to show \"-\" for base's project count", got)
	}
	if !strings.Contains(got, "Largest project: (none)") {
		t.Errorf("output = %q, want \"Largest project: (none)\"", got)
	}
	if !strings.Contains(got, "Largest file: (none)") {
		t.Errorf("output = %q, want \"Largest file: (none)\"", got)
	}
	if !strings.Contains(got, "/tmp/ws") {
		t.Errorf("output = %q, want it to print report.Path (%q)", got, "/tmp/ws")
	}
}

func TestPrintMetricsReport_SeparatorRowIsGenuinelyBlank(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	report := metrics.Report{
		Path: "/tmp/ws",
		Directories: []metrics.DirStats{
			{Name: "src", ProjectCount: 1, Size: 100},
		},
	}
	printMetricsReport(&out, report)

	for _, line := range strings.Split(out.String(), "\n") {
		if strings.TrimRight(line, "\r") == "" {
			continue // a real blank line is fine
		}
		if strings.Trim(line, " \t") == "" && line != "" {
			t.Errorf("output contains a whitespace-only line %q, want the separator row to be truly empty", line)
		}
	}
}

func TestWorkspaceMetricsCommand_PrintsReport(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)
	if err := os.MkdirAll(filepath.Join(root, "src", "api"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "api", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "metrics"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace metrics` returned error: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "Workspace Metrics") {
		t.Errorf("output = %q, want the \"Workspace Metrics\" header", got)
	}
	if !strings.Contains(got, "src") {
		t.Errorf("output = %q, want it to mention src", got)
	}
	if !strings.Contains(got, "Total") {
		t.Errorf("output = %q, want a Total row", got)
	}
	if !strings.Contains(got, "Files:") || !strings.Contains(got, "Directories:") {
		t.Errorf("output = %q, want Files: and Directories: lines", got)
	}
	if !strings.Contains(got, "Largest project:") || !strings.Contains(got, "Largest file:") {
		t.Errorf("output = %q, want Largest project:/Largest file: lines", got)
	}
}

func TestWorkspaceMetricsCommand_WsMAliasChain(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	root := t.TempDir()
	writeConfiguredWorkspace(t, root)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"ws", "m"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev ws m` returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Workspace Metrics") {
		t.Errorf("output = %q, want the \"Workspace Metrics\" header", out.String())
	}
}

func TestWorkspaceMetricsCommand_TriggersFirstUsePromptWhenUnconfigured(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	workspaceDir := t.TempDir()
	calls := stubWorkspacePrompt(t, workspaceDir)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"workspace", "metrics"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev workspace metrics` (unconfigured) returned error: %v", err)
	}
	if *calls != 1 {
		t.Errorf("prompt called %d times, want exactly 1 — metrics must not special-case or bypass resolveWorkspaceRoot's first-use path", *calls)
	}
	if !strings.Contains(out.String(), "Workspace Metrics") {
		t.Errorf("output = %q, want the report to still print after the first-use prompt", out.String())
	}
}
