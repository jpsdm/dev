package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the dev version",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Writes via cmd.OutOrStdout(), not cliutil, as a narrow and
		// deliberate exception to "only cliutil writes to stdout/stderr":
		// Cobra's SetOut-based test capture needs it. Don't generalize
		// this to other commands without a similar reason.
		fmt.Fprintln(cmd.OutOrStdout(), versionString())
		return nil
	},
}
