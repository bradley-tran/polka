package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
)

const (
	defaultNewPHPVersion      = "8.4"
	defaultNewComposerVersion = "2.8"
)

func newInitCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "init",
		Args: exactArgsError("init does not take arguments", 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runInit(cmd.OutOrStdout(), store)
		},
	}
	configureCommand(cmd, initUsage)

	return cmd
}

func newNewCommand(ctx *commandContext) *cobra.Command {
	input := newCommandInput{
		PHPVersion:      defaultNewPHPVersion,
		ComposerVersion: defaultNewComposerVersion,
	}

	cmd := &cobra.Command{
		Use:  "new <name>",
		Args: exactArgsError("new requires exactly one environment name", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			input.Name = strings.TrimSpace(args[0])
			if strings.TrimSpace(input.PHPVersion) == "" {
				return &statusError{code: 1, err: fmt.Errorf("--php requires a non-empty value")}
			}
			if strings.TrimSpace(input.ComposerVersion) == "" {
				return &statusError{code: 1, err: fmt.Errorf("--composer requires a non-empty value")}
			}

			return runNew(cmd.OutOrStdout(), store, input)
		},
	}
	cmd.Flags().StringVar(&input.PHPVersion, "php", defaultNewPHPVersion, "PHP version")
	cmd.Flags().StringVar(&input.ComposerVersion, "composer", defaultNewComposerVersion, "Composer version")
	configureCommand(cmd, newUsage)

	return cmd
}

func newConfigCommand(ctx *commandContext) *cobra.Command {
	var input configCommandInput

	cmd := &cobra.Command{
		Use:  "config <name>",
		Args: exactArgsError("config requires exactly one environment name", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			input.Name = strings.TrimSpace(args[0])
			input.HasPHP = cmd.Flags().Changed("php")
			input.HasComposer = cmd.Flags().Changed("composer")
			input.PHPVersion = strings.TrimSpace(input.PHPVersion)
			input.ComposerVersion = strings.TrimSpace(input.ComposerVersion)
			if !input.HasPHP && !input.HasComposer {
				return &statusError{code: 1, err: fmt.Errorf("config requires at least one of --php or --composer")}
			}
			if input.HasPHP && input.PHPVersion == "" {
				return &statusError{code: 1, err: fmt.Errorf("--php requires a non-empty value")}
			}
			if input.HasComposer && input.ComposerVersion == "" {
				return &statusError{code: 1, err: fmt.Errorf("--composer requires a non-empty value")}
			}

			return runConfig(cmd.OutOrStdout(), store, input)
		},
	}
	cmd.Flags().StringVar(&input.PHPVersion, "php", "", "PHP version")
	cmd.Flags().StringVar(&input.ComposerVersion, "composer", "", "Composer version")
	configureCommand(cmd, configUsage)

	return cmd
}

func newInstallCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "install <name>",
		Args: exactArgsError("install requires exactly one environment name", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runInstall(cmd.OutOrStdout(), store, installCommandInput{Name: strings.TrimSpace(args[0])})
		},
	}
	configureCommand(cmd, installUsage)

	return cmd
}

func newListCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "list",
		Args: exactArgsError("list does not take arguments", 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runList(cmd.OutOrStdout(), store)
		},
	}
	configureCommand(cmd, listUsage)

	return cmd
}

func newUseCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "use <name>",
		Args: exactArgsError("use requires exactly one environment name", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runUse(cmd.OutOrStdout(), store, strings.TrimSpace(args[0]))
		},
	}
	configureCommand(cmd, useUsage)

	return cmd
}

func newCurrentCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "current",
		Args: exactArgsError("current does not take arguments", 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runCurrent(cmd.OutOrStdout(), store)
		},
	}
	configureCommand(cmd, currentUsage)

	return cmd
}

func newRemoveCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "remove <name>",
		Args: exactArgsError("remove requires exactly one environment name", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runRemove(cmd.OutOrStdout(), store, strings.TrimSpace(args[0]))
		},
	}
	configureCommand(cmd, removeUsage)

	return cmd
}

func runInit(stdout io.Writer, store backend.Store) error {
	if err := store.Init(); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Initialized Polka at %s with config %s\n", store.RootDir, store.ConfigFile)
	return nil
}

func runInstall(stdout io.Writer, store backend.Store, input installCommandInput) error {
	results, err := store.Install(input.Name)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "Installed %s\n", input.Name)
	for _, result := range results {
		sourceKind := "cache"
		if result.Downloaded {
			sourceKind = "download"
		}
		_, _ = fmt.Fprintf(stdout, "%s %s\tfrom=%s\tcache=%s\ttarget=%s\n", result.Tool, result.Version, sourceKind, result.CachePath, result.TargetPath)
	}

	return nil
}

func runNew(stdout io.Writer, store backend.Store, input newCommandInput) error {
	environment, err := store.Create(input.Name, input.PHPVersion, input.ComposerVersion)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Created %s\tphp=%s\tcomposer=%s\n", environment.Name, environment.PHPVersion, environment.ComposerVersion)
	return nil
}

func runConfig(stdout io.Writer, store backend.Store, input configCommandInput) error {
	environment, err := store.Configure(input.Name, input.PHPVersion, input.ComposerVersion)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Configured %s\tphp=%s\tcomposer=%s\n", environment.Name, labelOrUnset(environment.PHPVersion), labelOrUnset(environment.ComposerVersion))
	return nil
}

func runList(stdout io.Writer, store backend.Store) error {
	environments, err := store.List()
	if err != nil {
		return err
	}
	current, err := store.Current()
	if err != nil {
		return err
	}

	if len(environments) == 0 {
		_, _ = fmt.Fprintln(stdout, "No environments found. Run `polka config <name> --php <version>` to add one.")
		return nil
	}

	for _, environment := range environments {
		marker := " "
		if current != nil && current.Name == environment.Name {
			marker = "*"
		}

		_, _ = fmt.Fprintf(stdout, "%s %s\tphp=%s\tcomposer=%s\n", marker, environment.Name, labelOrUnset(environment.PHPVersion), labelOrUnset(environment.ComposerVersion))
	}

	return nil
}

func runUse(stdout io.Writer, store backend.Store, name string) error {
	if err := store.Use(name); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Selected %s\n", name)
	return nil
}

func runCurrent(stdout io.Writer, store backend.Store) error {
	current, err := store.Current()
	if err != nil {
		return err
	}
	if current == nil {
		_, _ = fmt.Fprintln(stdout, "No active environment selected.")
		return nil
	}

	_, _ = fmt.Fprintf(stdout, "%s\tphp=%s\tcomposer=%s\n", current.Name, labelOrUnset(current.PHPVersion), labelOrUnset(current.ComposerVersion))
	return nil
}

func runRemove(stdout io.Writer, store backend.Store, name string) error {
	if err := store.Remove(name); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Removed %s\n", name)
	return nil
}

type configCommandInput struct {
	Name            string
	PHPVersion      string
	ComposerVersion string
	HasPHP          bool
	HasComposer     bool
}

type newCommandInput struct {
	Name            string
	PHPVersion      string
	ComposerVersion string
}

type installCommandInput struct {
	Name string
}

func labelOrUnset(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unset"
	}

	return value
}
