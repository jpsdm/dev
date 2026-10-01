package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/cliutil"
	"github.com/jpsdm/dev/internal/config"
	"github.com/jpsdm/dev/internal/metrics"
	"github.com/jpsdm/dev/internal/platform"
	"github.com/jpsdm/dev/internal/shell"
	"github.com/jpsdm/dev/internal/workspace"
)

// promptWorkspace asks the user for their workspace folder's name and
// parent location, pre-filled with defaultName/defaultLocation,
// reading input from in and writing prompt output to out, and returns
// the two answers. Tests replace this package-level var with a stub
// in most cases — driving two real prompts through piped test input
// isn't practical for every call site, so the swap happens here
// instead, the same way cmd/lang_test.go substitutes langManager
// rather than trying to drive a real registry through I/O.
// promptWorkspaceFields itself is still directly tested.
//
// Deliberately built on shell.PromptLine rather than a Huh form with
// two fields: Huh's accessible-mode Input.RunAccessible allocates a
// fresh bufio.Scanner per field, which over-reads into its own buffer
// and silently drops the second field's answer when both fields read
// from the same underlying reader in one form.Run() call — exactly
// the class of bug Confirm's own doc comment describes and PromptLine
// already avoids (see shell.go). This codebase already forces
// Huh's accessible mode unconditionally (WithAccessible(true), not
// gated on an actual TTY check) for every prompt it builds, for
// testability — a second sequential field inherits that bug for free.
var promptWorkspace = promptWorkspaceFields

func promptWorkspaceFields(in io.Reader, out io.Writer, defaultName, defaultLocation string) (name, location string, err error) {
	name, err = shell.PromptLine(fmt.Sprintf("What should the workspace folder be named? [%s] ", defaultName), in, out, defaultName)
	if err != nil {
		return "", "", fmt.Errorf("prompting for workspace folder name: %w", err)
	}
	location, err = shell.PromptLine(fmt.Sprintf("Where should that folder be created? [%s] ", defaultLocation), in, out, defaultLocation)
	if err != nil {
		return "", "", fmt.Errorf("prompting for workspace location: %w", err)
	}
	return name, location, nil
}

// normalizeWorkspacePath expands a leading ~ (or ~/...) to the user's
// home directory and resolves the result to an absolute path, so a
// persisted workspace path always refers to a stable, absolute
// location regardless of the cwd or home directory it was resolved
// from originally.
func normalizeWorkspacePath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving workspace path %s: %w", path, err)
	}
	return abs, nil
}

// resolveWorkspaceRoot returns the workspace root directory, prompting
// for and persisting it on first use (when config.Config.Workspace.Path
// is empty), and ensuring its scaffold subdirectories exist on every
// call. Every workspace subcommand calls this first. Both a freshly
// prompted answer and a path already loaded from config are
// normalized (see normalizeWorkspacePath) — a hand-edited config with
// a ~ or relative path is corrected and re-persisted, not just a
// first-use answer.
func resolveWorkspaceRoot(cmd *cobra.Command) (string, error) {
	path, err := config.DefaultPath()
	if err != nil {
		return "", err
	}
	cfg, err := config.Load(path)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return "", err
	}

	if cfg.Workspace.Path == "" {
		defLocation, err := platform.DefaultWorkspaceLocation()
		if err != nil {
			return "", err
		}
		name, location, err := promptWorkspace(cmd.InOrStdin(), cmd.OutOrStdout(), platform.DefaultWorkspaceName, defLocation)
		if err != nil {
			return "", err
		}
		normalizedLocation, err := normalizeWorkspacePath(location)
		if err != nil {
			return "", err
		}
		cfg.Workspace.Path = filepath.Join(normalizedLocation, name)
		if err := config.Save(path, cfg); err != nil {
			return "", err
		}
	} else {
		normalized, err := normalizeWorkspacePath(cfg.Workspace.Path)
		if err != nil {
			return "", err
		}
		if normalized != cfg.Workspace.Path {
			cfg.Workspace.Path = normalized
			if err := config.Save(path, cfg); err != nil {
				return "", err
			}
		}
	}

	if err := workspace.EnsureScaffold(cfg.Workspace.Path); err != nil {
		return "", err
	}
	return cfg.Workspace.Path, nil
}

var workspaceCmd = &cobra.Command{
	Use:     "workspace",
	Aliases: []string{"ws"},
	Short:   "Manage your development workspace",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveWorkspaceRoot(cmd)
		if err != nil {
			return err
		}
		cliutil.Fsuccess(cmd.OutOrStdout(), "Workspace at %s", root)
		return nil
	},
}

var workspaceNewCmd = &cobra.Command{
	Use:   "new <name>",
	Args:  cobra.ExactArgs(1),
	Short: "Create a new project from the workspace template",
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveWorkspaceRoot(cmd)
		if err != nil {
			return err
		}
		dest, err := workspace.New(root, args[0])
		if err != nil {
			return err
		}
		cliutil.Fsuccess(cmd.OutOrStdout(), "Created %s", dest)
		return nil
	},
}

var workspaceScratchCmd = &cobra.Command{
	Use:     "scratch <name>",
	Aliases: []string{"s"},
	Args:    cobra.ExactArgs(1),
	Short:   "Create a new scratch project from the workspace template",
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveWorkspaceRoot(cmd)
		if err != nil {
			return err
		}
		dest, err := workspace.Scratch(root, args[0])
		if err != nil {
			return err
		}
		cliutil.Fsuccess(cmd.OutOrStdout(), "Created %s", dest)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(workspaceCmd)
	workspaceCmd.AddCommand(workspaceNewCmd)
	workspaceCmd.AddCommand(workspaceScratchCmd)
}

func printCleanPreview(out io.Writer, removed []string) {
	if len(removed) == 0 {
		cliutil.Fstep(out, "Nothing to clean")
		return
	}
	fmt.Fprintln(out, "The following files would be removed:")
	fmt.Fprintln(out)
	for _, r := range removed {
		fmt.Fprintln(out, r)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "No files were deleted.")
}

var workspaceCleanCmd = &cobra.Command{
	Use:     "clean [name]",
	Aliases: []string{"c"},
	Args:    cobra.MaximumNArgs(1),
	Short:   "Remove a project's gitignored files, or every project's with --all",
	RunE: func(cmd *cobra.Command, args []string) error {
		all, err := cmd.Flags().GetBool("all")
		if err != nil {
			return err
		}
		dryRun, err := cmd.Flags().GetBool("dry-run")
		if err != nil {
			return err
		}
		if all == (len(args) == 1) {
			return fmt.Errorf("specify either a project name or --all, not both or neither")
		}

		root, err := resolveWorkspaceRoot(cmd)
		if err != nil {
			return err
		}

		if !all {
			removed, err := workspace.Clean(root, args[0], dryRun)
			if err != nil {
				return err
			}
			if dryRun {
				printCleanPreview(cmd.OutOrStdout(), removed)
				return nil
			}
			if len(removed) == 0 {
				cliutil.Fstep(cmd.OutOrStdout(), "Nothing to clean")
			} else {
				for _, r := range removed {
					fmt.Fprintln(cmd.OutOrStdout(), r)
				}
			}
			cliutil.Fsuccess(cmd.OutOrStdout(), "Cleaned src/%s", args[0])
			return nil
		}

		if !dryRun {
			confirmed, err := shell.Confirm("Remove ignored files from every project in src/? [y/N] ", cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if !confirmed {
				cliutil.Fstep(cmd.OutOrStdout(), "No changes made.")
				return nil
			}
		}

		results, cleanErr := workspace.CleanAll(root, dryRun)
		names := make([]string, 0, len(results))
		for name := range results {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			removed := results[name]
			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "%s:\n", name)
				printCleanPreview(cmd.OutOrStdout(), removed)
				continue
			}
			if len(removed) == 0 {
				cliutil.Fstep(cmd.OutOrStdout(), "%s: nothing to clean", name)
				continue
			}
			cliutil.Fsuccess(cmd.OutOrStdout(), "Cleaned src/%s", name)
		}
		return cleanErr
	},
}

func init() {
	workspaceCleanCmd.Flags().Bool("all", false, "clean every project in src/")
	workspaceCleanCmd.Flags().Bool("dry-run", false, "show what would be removed without removing anything")
	workspaceCmd.AddCommand(workspaceCleanCmd)
}

var workspaceArchiveCmd = &cobra.Command{
	Use:     "archive <name>",
	Aliases: []string{"a"},
	Args:    cobra.ExactArgs(1),
	Short:   "Clean a project and move it to archive/",
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, err := cmd.Flags().GetBool("dry-run")
		if err != nil {
			return err
		}
		root, err := resolveWorkspaceRoot(cmd)
		if err != nil {
			return err
		}
		removed, err := workspace.Archive(root, args[0], dryRun)
		if err != nil {
			return err
		}
		if dryRun {
			printCleanPreview(cmd.OutOrStdout(), removed)
			return nil
		}
		for _, r := range removed {
			fmt.Fprintln(cmd.OutOrStdout(), r)
		}
		cliutil.Fsuccess(cmd.OutOrStdout(), "Archived src/%s to archive/%s", args[0], args[0])
		return nil
	},
}

func init() {
	workspaceArchiveCmd.Flags().Bool("dry-run", false, "show what would be removed without cleaning or moving anything")
	workspaceCmd.AddCommand(workspaceArchiveCmd)
}

// formatSize renders bytes as a human-readable binary-unit (1024-based)
// string, always with two decimal places once at least 1024 bytes,
// falling back to a plain byte count under that.
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	div, exp := int64(unit), 0
	// Clamp exp so it never exceeds the last unit's index: once we'd
	// reach TB, stop growing div/exp further and just report however
	// large a TB value results (e.g. "4096.00 TB" for a value that
	// would otherwise be 4 PB), rather than indexing out of bounds
	// for sizes >= 1 PiB (reachable via a sparse file, whose
	// info.Size() reports "apparent size").
	for n := bytes / unit; n >= unit && exp < len(units)-1; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %s", float64(bytes)/float64(div), units[exp])
}

func printMetricsReport(out io.Writer, report metrics.Report) {
	fmt.Fprintln(out, "Workspace Metrics")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Path:")
	fmt.Fprintln(out, report.Path)
	fmt.Fprintln(out)

	tw := tabwriter.NewWriter(out, 0, 4, 3, ' ', 0)
	fmt.Fprintln(tw, "Directory\tProjects\tSize")
	for _, d := range report.Directories {
		projects := "-"
		if d.ProjectCount >= 0 {
			projects = strconv.Itoa(d.ProjectCount)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", d.Name, projects, formatSize(d.Size))
	}
	// A genuinely empty line (no tabs) rather than "\t\t": tabwriter
	// only pads/aligns a line's tab-terminated cells, so a line with no
	// tabs at all is a single unaligned field, printed verbatim — for
	// an empty string, that's a truly blank line, not one padded out
	// to the table's column width with spaces. The Total row below
	// still lands in the same tabwriter batch (flushed together), so
	// its own column alignment is unaffected.
	fmt.Fprintln(tw)
	fmt.Fprintf(tw, "Total\t\t%s\n", formatSize(report.TotalSize))
	tw.Flush()

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Files: %d\n", report.TotalFiles)
	fmt.Fprintf(out, "Directories: %d\n", report.TotalDirs)
	if report.LargestProject.Name == "" {
		fmt.Fprintln(out, "Largest project: (none)")
	} else {
		fmt.Fprintf(out, "Largest project: %s (%s)\n", report.LargestProject.Name, formatSize(report.LargestProject.Size))
	}
	if report.LargestFile.Path == "" {
		fmt.Fprintln(out, "Largest file: (none)")
	} else {
		fmt.Fprintf(out, "Largest file: %s (%s)\n", report.LargestFile.Path, formatSize(report.LargestFile.Size))
	}
}

var workspaceMetricsCmd = &cobra.Command{
	Use:     "metrics",
	Aliases: []string{"m"},
	Args:    cobra.NoArgs,
	Short:   "Show size and project counts for your workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveWorkspaceRoot(cmd)
		if err != nil {
			return err
		}
		report, err := metrics.Collect(root)
		if err != nil {
			return err
		}
		printMetricsReport(cmd.OutOrStdout(), report)
		return nil
	},
}

func init() {
	workspaceCmd.AddCommand(workspaceMetricsCmd)
}
