// Package cmd defines dev's Cobra command tree. It is the only
// package that constructs Cobra commands; all filesystem/path logic
// lives in internal/platform and internal/config, and all output goes
// through internal/cliutil.
package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/config"
	"github.com/jpsdm/dev/internal/platform"
)

// Version, Commit and BuildDate are injected at build time via
// -ldflags (see the Makefile's build target). Their defaults here are
// only used for `go run`/`go test`, never for a released binary.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

var verboseFlag bool

var rootCmd = &cobra.Command{
	Use:   "dev",
	Short: "dev manages language runtimes, workspaces and dev environments",
	Long: "dev is a small, fast Development Environment Manager: it installs " +
		"and switches language/runtime versions, and organizes a project " +
		"workspace.",
	Version:       versionString(),
	SilenceUsage:  true,
	SilenceErrors: true,
	// Only reached for bare `dev` — `dev --help`/`dev <sub> --help` are
	// short-circuited by Cobra's own flag handling before RunE ever
	// runs, and every other command has its own RunE. So this is the
	// one place a startup banner can appear without printing it on
	// every single command's output.
	RunE: func(cmd *cobra.Command, args []string) error {
		printBanner(cmd.OutOrStdout())
		return cmd.Help()
	},
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		cliutil.SetVerbose(verboseFlag)
		// "help" joins setup/version because it is a real Cobra
		// command and so reaches this gate, while the --help *flag*
		// forms (`dev --help`, `dev lang --help`) are short-circuited
		// by Cobra before PersistentPreRunE runs. Without it, the one
		// help form a not-yet-installed user is most likely to reach
		// for would be the only one refused.
		switch cmd.Name() {
		case "setup", "version", "help":
			return nil
		}
		ok, _, err := platform.RunningFromDevHome()
		if err != nil {
			return err
		}
		if !ok {
			//nolint:staticcheck // ST1005: Message is deliberately a complete user-facing sentence, shared verbatim with main.go
			return errors.New(platform.NotInstalledWarning)
		}
		return nil
	},
	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		notice := updateNoticeIfDue(cmd)
		if notice == "" {
			return nil
		}
		if cmd.Name() == "version" {
			// dev version's own notice: printed as part of its own
			// output (stdout), not off to the side on stderr like every
			// other command's notice — a user running `dev version`
			// wants this directly in what they asked to see.
			fmt.Fprintln(cmd.OutOrStdout(), notice)
		} else {
			fmt.Fprintln(cmd.ErrOrStderr(), notice)
		}
		return nil
	},
}

// updateNoticeIfDue returns the one-line "a newer version is
// available" notice if one is due for cmd's invocation, or "" if
// there's nothing to show — either because nothing newer exists, or
// because the check itself was skipped. Skipped entirely, before
// touching config or network, when: this is `dev update` itself (it
// already does its own fresh check, see cmd/update.go);
// DEV_NO_UPDATE_CHECK is set; platform.RunningFromDevHome() reports
// false (suggesting an update before dev is even installed doesn't
// make sense — this also covers `setup` and `help` for free, since
// neither runs from $DEV_HOME on a fresh, not-yet-installed copy); or
// Version isn't a clean release version (a local/dev build). Every
// other failure along the way (reading/writing config.json, a network
// error inside CachedNotice) is logged only at --verbose and never
// surfaces as a command failure — an update check must never visibly
// break an otherwise-successful command.
func updateNoticeIfDue(cmd *cobra.Command) string {
	if cmd.Name() == "update" {
		return ""
	}
	if os.Getenv("DEV_NO_UPDATE_CHECK") != "" {
		return ""
	}
	if ok, _, err := platform.RunningFromDevHome(); err != nil || !ok {
		return ""
	}

	cfgPath, err := config.DefaultPath()
	if err != nil {
		cliutil.Verbosef("update check: %v", err)
		return ""
	}
	cfg, err := config.Load(cfgPath)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		cliutil.Verbosef("update check: %v", err)
		return ""
	}

	client := newUpdateClient()
	notice, updated := client.CachedNotice(cmd.Context(), Version, cfg.UpdateCheck)
	if updated != nil {
		cfg.UpdateCheck = *updated
		if err := config.Save(cfgPath, cfg); err != nil {
			cliutil.Verbosef("update check: saving cache: %v", err)
		}
	}
	return notice
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "enable verbose output")
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	rootCmd.AddCommand(versionCmd)
}

// versionString builds the full multi-line version block shared by
// both `dev --version` and `dev version`, so the two never diverge.
func versionString() string {
	return fmt.Sprintf(
		"dev version %s\ncommit: %s\nbuild date: %s\nplatform: %s/%s",
		Version, Commit, BuildDate, platform.OS(), platform.Arch(),
	)
}

// exitCodeFor maps a command's returned error to the process's exit
// code: 0 for a user-initiated abort of an interactive Huh prompt
// (Ctrl+C at a prompt like `dev workspace`'s first-use path question
// is a deliberate cancellation, not a failure), 1 for anything else.
// Extracted from Execute so this mapping is directly testable without
// exercising a real os.Exit.
func exitCodeFor(err error) int {
	if errors.Is(err, huh.ErrUserAborted) {
		return 0
	}
	return 1
}

// Execute runs the root command and is the CLI's single entry point,
// called from main.go. On error it prints via cliutil.PrintError and
// exits non-zero — except a user aborting an interactive prompt
// (Ctrl+C), which exits quietly with status 0. It never lets a Go
// panic surface for an ordinary command error.
func Execute() {
	err := rootCmd.Execute()
	if err == nil {
		return
	}
	code := exitCodeFor(err)
	if code != 0 {
		cliutil.PrintError(err)
	}
	os.Exit(code)
}
