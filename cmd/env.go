package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/runtime"
	"github.com/jpsdm/dev/internal/shell"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Print shell export lines that put the active version of every language on PATH",
	RunE: func(cmd *cobra.Command, args []string) error {
		devHome, err := platform.DevHome()
		if err != nil {
			return err
		}
		sh := shell.Detect()
		current := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))
		pathEntries := shell.ComputePathEntries(current, devHome, activeBinDirs(devHome))
		lines := shell.ExportLines(sh, devHome, pathEntries)
		fmt.Fprintln(cmd.OutOrStdout(), strings.Join(lines, "\n"))
		return nil
	},
}

// activeBinDirs returns, in langManager.Names()'s alphabetical order,
// the BinDir of every registered provider's current active version —
// skipping a provider with no active version, and skipping (rather
// than failing the whole command on) a BinDir error, since one
// language's provider misbehaving must never break dev env for every
// other language.
//
// current.Name comes from the on-disk current/<lang> marker file's
// contents and is joined into a filesystem path below, so it goes
// through runtime.ValidVersionName first — the mandatory gate for any
// externally-sourced identifier that becomes part of a path (see
// CLAUDE.md). Without it, a hand-edited or corrupted marker containing
// "../../../../tmp" escapes $DEV_HOME/versions entirely and lands an
// arbitrary directory on the user's PATH. An invalid name is skipped
// rather than fatal, matching how every other per-provider failure
// here is handled.
func activeBinDirs(devHome string) []string {
	var dirs []string
	for _, name := range langManager.Names() {
		r, ok := langManager.Get(name)
		if !ok {
			continue
		}
		current, err := r.CurrentVersion()
		if err != nil || current == nil {
			continue
		}
		if err := runtime.ValidVersionName(current.Name); err != nil {
			continue
		}
		versionDir := filepath.Join(devHome, "versions", name, current.Name)
		dir, err := r.BinDir(versionDir)
		if err != nil {
			continue
		}
		dirs = append(dirs, dir)
	}
	return dirs
}

func init() {
	rootCmd.AddCommand(envCmd)
}
