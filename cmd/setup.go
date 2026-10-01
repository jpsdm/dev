package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/filesystem"
	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/shell"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Detect your shell and configure PATH (asks for confirmation)",
	RunE: func(cmd *cobra.Command, args []string) error {
		printBanner(cmd.OutOrStdout())

		devHome, err := platform.DevHome()
		if err != nil {
			return err
		}
		sh := shell.Detect()
		lines := shell.FunctionLines(sh, devHome)

		path, supported, err := shell.RCPath(sh)
		if err != nil {
			return err
		}

		if supported {
			fmt.Fprintf(cmd.OutOrStdout(), "The following will be added to %s:\n\n", path)
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), unsupportedShellMessage(runtime.GOOS))
			fmt.Fprintln(cmd.OutOrStdout())
		}
		for _, line := range lines {
			fmt.Fprintln(cmd.OutOrStdout(), line)
		}
		fmt.Fprintln(cmd.OutOrStdout())

		prompt := "Set these up now? [y/N] "
		if supported {
			prompt = fmt.Sprintf("Add this to %s? [y/N] ", path)
		}
		confirmed, err := shell.Confirm(prompt, cmd.InOrStdin(), cmd.OutOrStdout())
		if err != nil {
			return err
		}
		if !confirmed {
			cliutil.Fstep(cmd.OutOrStdout(), "No changes made.")
			return nil
		}

		if err := relocateIfNeeded(cmd, devHome); err != nil {
			return err
		}

		if supported {
			if err := shell.UpsertBlock(path, lines); err != nil {
				return fmt.Errorf("updating %s: %w", path, err)
			}
			cliutil.Fsuccess(cmd.OutOrStdout(), "Updated %s", path)
			cliutil.Fstep(cmd.OutOrStdout(), "Restart your shell (or run `%s`) for these changes to take effect", reloadHint(sh, path))
		}

		// Independent of whether the profile/rc-file write above
		// happened: the registry entries cover every Windows process
		// (GUI apps, cmd.exe, a non-PowerShell integrated terminal),
		// not just PowerShell sessions that load the written profile —
		// so this is offered unconditionally on Windows, not only when
		// the profile write was skipped.
		if runtime.GOOS == "windows" {
			envConfirmed, err := shell.Confirm("Add these to your user environment now? [y/N] ", cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if envConfirmed {
				//nolint:staticcheck // SA4023: Check is only statically-always-true on non-Windows builds; it's genuinely needed on Windows
				if err := shell.ConfigureWindowsUserEnv(devHome); err != nil {
					return err
				}
				cliutil.Fsuccess(cmd.OutOrStdout(), "Updated your user environment (DEV_HOME and PATH)")
			}
		}
		return nil
	},
}

// reloadHint returns the command the user should run to make a
// freshly-written block take effect in their CURRENT shell without
// restarting it. PowerShell has no "source" command at all — its
// dot-sourcing operator (". <path>") is the equivalent.
func reloadHint(sh shell.Shell, path string) string {
	if sh == shell.PowerShell {
		return fmt.Sprintf(". %s", path)
	}
	return fmt.Sprintf("source %s", path)
}

// unsupportedShellMessage returns the header setup prints when the
// detected shell has no rc file dev can safely auto-edit. Windows gets
// different wording than a genuinely unidentified $SHELL (Linux/macOS
// Unknown): PowerShell's environment variables ARE configured
// automatically later in this same run (via the registry, see
// ConfigureWindowsUserEnv), so the header must not tell the user to
// set them by hand — only the rc-file auto-edit is what's actually
// missing there. A genuine Unknown shell has no automatic mechanism at
// all, so it keeps asking for manual editing.
func unsupportedShellMessage(goos string) string {
	if goos == "windows" {
		return "PowerShell doesn't have a profile file dev can safely auto-edit, but dev can configure your environment variables directly. Here's what will be set:"
	}
	return "Automatic setup isn't supported for this shell yet. Add these lines manually:"
}

// relocateIfNeeded offers to move the running binary (plus its
// README.md/LICENSE siblings) into devHome, when it isn't already
// running from there. A no-op when re-running setup on an
// already-installed copy — there's nothing to relocate, and
// installRelocation must never be given devHome as its own exe
// argument (see installRelocation's doc comment).
func relocateIfNeeded(cmd *cobra.Command, devHome string) error {
	runningFromDevHome, _, err := platform.RunningFromDevHome()
	if err != nil {
		return err
	}
	if runningFromDevHome {
		return nil
	}

	// platform.Executable, not the raw os.Executable — the same
	// resolvable-in-tests source of truth RunningFromDevHome's check
	// above just used, so the exe this function relocates is
	// guaranteed to be the exact one that check evaluated. Calling the
	// unmockable os.Executable directly here would let this function's
	// idea of "the binary" silently diverge from RunningFromDevHome's
	// in a test that overrides platform.Executable (production takes
	// the same code path either way, since platform.Executable's
	// default value is os.Executable itself).
	exe, err := platform.Executable()
	if err != nil {
		return fmt.Errorf("finding the dev binary: %w", err)
	}

	confirmed, err := shell.Confirm("Move dev, README.md, and LICENSE into $DEV_HOME now? [y/N] ", cmd.InOrStdin(), cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if !confirmed {
		cliutil.Fstep(cmd.OutOrStdout(), "No changes made.")
		return nil
	}

	if err := installRelocation(cmd.OutOrStdout(), exe, devHome); err != nil {
		return err
	}
	cliutil.Fsuccess(cmd.OutOrStdout(), "Moved dev into %s", devHome)
	return nil
}

// installRelocation copies exe (and its README.md/LICENSE siblings,
// if present) into devHome, then best-effort removes the originals.
// A missing README.md/LICENSE (e.g. a from-source `go build` binary,
// which has no such siblings) is not an error; only exe itself is
// required to exist. Callers must only invoke this when exe is
// genuinely outside devHome (relocateIfNeeded's RunningFromDevHome
// check guarantees this) — copying devHome/dev onto itself would
// truncate it via copyExecutable's read-then-write.
//
// out takes the best-effort removal warning: the caller's own writer,
// so it is captured under test like every other message setup emits,
// rather than escaping to the global stdout.
func installRelocation(out io.Writer, exe, devHome string) error {
	dest := filepath.Join(devHome, filepath.Base(exe))
	if err := copyExecutable(exe, dest); err != nil {
		return fmt.Errorf("copying %s into %s: %w", exe, devHome, err)
	}
	relocated := []string{exe}

	srcDir := filepath.Dir(exe)
	for _, name := range []string{"README.md", "LICENSE"} {
		src := filepath.Join(srcDir, name)
		data, err := os.ReadFile(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("reading %s: %w", src, err)
		}
		if err := filesystem.WriteFileAtomic(filepath.Join(devHome, name), data, 0o644); err != nil {
			return fmt.Errorf("copying %s into %s: %w", src, devHome, err)
		}
		relocated = append(relocated, src)
	}

	for _, path := range relocated {
		if err := os.Remove(path); err != nil {
			// Best-effort, and expected to fail often on Windows, where
			// a running process can't delete its own executable image —
			// dev's copy in devHome is already in place and working, so
			// this is not a problem to report as one. A leftover
			// original outside $DEV_HOME is exactly what
			// RunningFromDevHome's check guards against if it's ever
			// run again, so there's nothing left for the user to do
			// except delete it whenever they like.
			cliutil.Fsuccess(out, "%s is copied and set up — you can delete %s now", filepath.Base(path), path)
		}
	}
	return nil
}

// copyExecutable copies src to dest with executable permissions,
// atomically replacing any existing file at dest (and re-applying 0o755
// to it, unlike a plain os.WriteFile which only sets permissions when
// creating a new file).
func copyExecutable(src, dest string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if err := filesystem.WriteFileAtomic(dest, data, 0o755); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(setupCmd)
}
