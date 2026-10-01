package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jpsdm/dev/internal/providers"
	"github.com/jpsdm/dev/internal/runtime"
)

var langManager = runtime.NewManager()

func init() {
	providers.Register(langManager)
	rootCmd.AddCommand(langCmd)
	langCmd.AddCommand(langListCmd)
	langCmd.AddCommand(langInstalledCmd)
	langCmd.AddCommand(langCurrentCmd)
	langCmd.AddCommand(langInstallCmd)
	langCmd.AddCommand(langUninstallCmd)
	langCmd.AddCommand(langUseCmd)
}

func getRuntime(name string) (runtime.Runtime, error) {
	r, ok := langManager.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown language: %s", name)
	}
	return r, nil
}

var langCmd = &cobra.Command{
	Use:     "lang",
	Aliases: []string{"l"},
	Short:   "Manage language and runtime versions",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := cmd.Help(); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "Registered languages:")
		for _, name := range langManager.Names() {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", name)
		}
		return nil
	},
}

var langListCmd = &cobra.Command{
	Use:     "list [language]",
	Aliases: []string{"ls"},
	Args:    cobra.MaximumNArgs(1),
	Short:   "List available versions",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			return listOneLanguageRemote(cmd, args[0])
		}
		return listAllLanguagesNewest(cmd)
	},
}

func listOneLanguageRemote(cmd *cobra.Command, name string) error {
	r, err := getRuntime(name)
	if err != nil {
		return err
	}
	versions, err := r.ListRemoteVersions(cmd.Context())
	if err != nil {
		return fmt.Errorf("listing %s versions: %w", name, err)
	}
	installed, err := r.ListInstalledVersions()
	if err != nil {
		return fmt.Errorf("listing installed %s versions: %w", name, err)
	}
	current, err := r.CurrentVersion()
	if err != nil {
		return fmt.Errorf("reading current %s version: %w", name, err)
	}

	installedSet := make(map[string]bool, len(installed))
	for _, v := range installed {
		installedSet[v.Name] = true
	}

	for _, v := range versions {
		line := v.Name
		if v.LTS {
			line += " (LTS)"
		}
		switch {
		case current != nil && current.Name == v.Name:
			line += " (current)"
		case installedSet[v.Name]:
			line += " (installed)"
		}
		fmt.Fprintln(cmd.OutOrStdout(), line)
	}
	return nil
}

func listAllLanguagesNewest(cmd *cobra.Command) error {
	var errs []error
	for _, name := range langManager.Names() {
		r, _ := langManager.Get(name)
		versions, err := r.ListRemoteVersions(cmd.Context())
		if err != nil {
			errs = append(errs, fmt.Errorf("listing %s versions: %w", name, err))
			continue
		}
		if len(versions) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: (no versions available)\n", name)
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (latest)\n", name, versions[0].Name)
	}
	return errors.Join(errs...)
}

// languageTargets resolves the runtimes a "[language]"-optional command
// should operate on: just the named one if args has it (already
// validated by the caller's getRuntime lookup, so callers don't repeat
// that same registry lookup a second time), or every registered
// provider otherwise.
func languageTargets(args []string) ([]runtime.Runtime, error) {
	if len(args) == 1 {
		r, err := getRuntime(args[0])
		if err != nil {
			return nil, err
		}
		return []runtime.Runtime{r}, nil
	}
	names := langManager.Names()
	targets := make([]runtime.Runtime, 0, len(names))
	for _, name := range names {
		r, _ := langManager.Get(name)
		targets = append(targets, r)
	}
	return targets, nil
}

var langInstalledCmd = &cobra.Command{
	Use:   "installed [language]",
	Args:  cobra.MaximumNArgs(1),
	Short: "List installed versions",
	RunE: func(cmd *cobra.Command, args []string) error {
		targets, err := languageTargets(args)
		if err != nil {
			return err
		}
		single := len(args) == 1
		var errs []error
		for _, r := range targets {
			versions, err := r.ListInstalledVersions()
			if err != nil {
				err = fmt.Errorf("listing installed %s versions: %w", r.Name(), err)
				if single {
					return err
				}
				errs = append(errs, err)
				continue
			}
			if len(versions) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: (none installed)\n", r.Name())
				continue
			}
			for _, v := range versions {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", r.Name(), v.Name)
			}
		}
		return errors.Join(errs...)
	},
}

var langCurrentCmd = &cobra.Command{
	Use:     "current [language]",
	Aliases: []string{"c"},
	Args:    cobra.MaximumNArgs(1),
	Short:   "Show the active version",
	RunE: func(cmd *cobra.Command, args []string) error {
		targets, err := languageTargets(args)
		if err != nil {
			return err
		}
		single := len(args) == 1
		var errs []error
		for _, r := range targets {
			current, err := r.CurrentVersion()
			if err != nil {
				err = fmt.Errorf("reading current %s version: %w", r.Name(), err)
				if single {
					return err
				}
				errs = append(errs, err)
				continue
			}
			if current == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: (none active)\n", r.Name())
				continue
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", r.Name(), current.Name)
		}
		return errors.Join(errs...)
	},
}

var langInstallCmd = &cobra.Command{
	Use:     "install <language> <version>",
	Aliases: []string{"i"},
	Args:    cobra.ExactArgs(2),
	Short:   "Install a language version",
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getRuntime(args[0])
		if err != nil {
			return err
		}
		if err := runtime.ValidVersionName(args[1]); err != nil {
			return err
		}
		if err := r.Install(cmd.Context(), args[1]); err != nil {
			return fmt.Errorf("installing %s: %w", args[0], err)
		}
		return nil
	},
}

var langUninstallCmd = &cobra.Command{
	Use:   "uninstall <language> <version>",
	Args:  cobra.ExactArgs(2),
	Short: "Uninstall a language version",
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getRuntime(args[0])
		if err != nil {
			return err
		}
		if err := runtime.ValidVersionName(args[1]); err != nil {
			return err
		}
		if err := r.Uninstall(args[1]); err != nil {
			return fmt.Errorf("uninstalling %s: %w", args[0], err)
		}
		return nil
	},
}

var langUseCmd = &cobra.Command{
	Use:     "use <language> <version>",
	Aliases: []string{"u"},
	Args:    cobra.ExactArgs(2),
	Short:   "Activate a language version",
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getRuntime(args[0])
		if err != nil {
			return err
		}
		if err := runtime.ValidVersionName(args[1]); err != nil {
			return err
		}
		if err := r.Activate(args[1]); err != nil {
			return fmt.Errorf("activating %s: %w", args[0], err)
		}
		return nil
	},
}
