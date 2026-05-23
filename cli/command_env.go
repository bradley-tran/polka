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
			input.Database, err = buildDatabaseInput(
				cmd.Flags().Changed("db-engine"),
				cmd.Flags().Changed("db-version"),
				cmd.Flags().Changed("db-port"),
				input.DatabaseEngine,
				input.DatabaseVersion,
				input.DatabasePort,
			)
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runNew(cmd.OutOrStdout(), store, input)
		},
	}
	cmd.Flags().StringVar(&input.PHPVersion, "php", defaultNewPHPVersion, "PHP version")
	cmd.Flags().StringVar(&input.ComposerVersion, "composer", defaultNewComposerVersion, "Composer version")
	cmd.Flags().StringVar(&input.DatabaseEngine, "db-engine", "", "database engine (mysql or mariadb)")
	cmd.Flags().StringVar(&input.DatabaseVersion, "db-version", "", "database version")
	cmd.Flags().IntVar(&input.DatabasePort, "db-port", 0, "database port")
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
			input.HasDatabase = cmd.Flags().Changed("db-engine") || cmd.Flags().Changed("db-version") || cmd.Flags().Changed("db-port")
			input.PHPVersion = strings.TrimSpace(input.PHPVersion)
			input.ComposerVersion = strings.TrimSpace(input.ComposerVersion)
			if !input.HasPHP && !input.HasComposer && !input.HasDatabase {
				return &statusError{code: 1, err: fmt.Errorf("config requires at least one of --php, --composer, or --db-engine/--db-version")}
			}
			if input.HasPHP && input.PHPVersion == "" {
				return &statusError{code: 1, err: fmt.Errorf("--php requires a non-empty value")}
			}
			if input.HasComposer && input.ComposerVersion == "" {
				return &statusError{code: 1, err: fmt.Errorf("--composer requires a non-empty value")}
			}
			input.Database, err = buildDatabaseInput(
				cmd.Flags().Changed("db-engine"),
				cmd.Flags().Changed("db-version"),
				cmd.Flags().Changed("db-port"),
				input.DatabaseEngine,
				input.DatabaseVersion,
				input.DatabasePort,
			)
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runConfig(cmd.OutOrStdout(), store, input)
		},
	}
	cmd.Flags().StringVar(&input.PHPVersion, "php", "", "PHP version")
	cmd.Flags().StringVar(&input.ComposerVersion, "composer", "", "Composer version")
	cmd.Flags().StringVar(&input.DatabaseEngine, "db-engine", "", "database engine (mysql or mariadb)")
	cmd.Flags().StringVar(&input.DatabaseVersion, "db-version", "", "database version")
	cmd.Flags().IntVar(&input.DatabasePort, "db-port", 0, "database port")
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
	environment, err := store.Create(input.Name, input.PHPVersion, input.ComposerVersion, input.Database)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Created %s\tphp=%s\tcomposer=%s\tdb=%s\n", environment.Name, labelOrUnset(environment.PHPVersion), labelOrUnset(environment.ComposerVersion), labelDatabase(environment.Database))
	return nil
}

func runConfig(stdout io.Writer, store backend.Store, input configCommandInput) error {
	environment, err := store.Configure(input.Name, input.PHPVersion, input.ComposerVersion, input.Database)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Configured %s\tphp=%s\tcomposer=%s\tdb=%s\n", environment.Name, labelOrUnset(environment.PHPVersion), labelOrUnset(environment.ComposerVersion), labelDatabase(environment.Database))
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

		_, _ = fmt.Fprintf(stdout, "%s %s\tphp=%s\tcomposer=%s\tdb=%s\n", marker, environment.Name, labelOrUnset(environment.PHPVersion), labelOrUnset(environment.ComposerVersion), labelDatabase(environment.Database))
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

	_, _ = fmt.Fprintf(stdout, "%s\tphp=%s\tcomposer=%s\tdb=%s\n", current.Name, labelOrUnset(current.PHPVersion), labelOrUnset(current.ComposerVersion), labelDatabase(current.Database))
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
	Database        *backend.DatabaseConfig
	DatabaseEngine  string
	DatabaseVersion string
	DatabasePort    int
	HasPHP          bool
	HasComposer     bool
	HasDatabase     bool
}

type newCommandInput struct {
	Name            string
	PHPVersion      string
	ComposerVersion string
	Database        *backend.DatabaseConfig
	DatabaseEngine  string
	DatabaseVersion string
	DatabasePort    int
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

func labelDatabase(database *backend.DatabaseConfig) string {
	if database == nil {
		return "unset"
	}

	label := fmt.Sprintf("%s:%s", database.Engine, database.Version)
	if database.Port != 0 {
		label = fmt.Sprintf("%s@%d", label, database.Port)
	}

	return label
}

func buildDatabaseInput(engineChanged, versionChanged, portChanged bool, engine, version string, port int) (*backend.DatabaseConfig, error) {
	if !engineChanged && !versionChanged && !portChanged {
		return nil, nil
	}
	if engineChanged && strings.TrimSpace(engine) == "" {
		return nil, fmt.Errorf("--db-engine requires a non-empty value")
	}
	if versionChanged && strings.TrimSpace(version) == "" {
		return nil, fmt.Errorf("--db-version requires a non-empty value")
	}
	if engineChanged != versionChanged {
		return nil, fmt.Errorf("--db-engine and --db-version must be provided together")
	}
	if portChanged && (port < 1 || port > 65535) {
		return nil, fmt.Errorf("--db-port must be between 1 and 65535")
	}

	return &backend.DatabaseConfig{
		Engine:  strings.TrimSpace(engine),
		Version: strings.TrimSpace(version),
		Port:    port,
	}, nil
}
