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
