package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
	"polka/service"
)

const (
	defaultEnvironmentName    = "default"
	defaultNewPHPVersion      = backend.DefaultPHPVersion
	defaultNewComposerVersion = backend.DefaultComposerVersion
	defaultNewNodeJSVersion   = backend.DefaultNodeJSVersion
)

// newEnvCommand groups the environment-definition commands under "env". The real
// implementations live here; the familiar top-level names (new, config, install,
// list, use) are registered on the root as shortcuts that expand to these.
func newEnvCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env <command> [options]",
		Short: "Manage environment definitions.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return &statusError{code: 2, err: fmt.Errorf("unknown command %q", strings.TrimSpace(args[0])), showUsage: true, usage: envUsage}
			}

			_, _ = fmt.Fprint(cmd.OutOrStdout(), envUsage)
			return nil
		},
	}
	configureHelp(cmd, envUsage)
	cmd.AddCommand(
		newNewCommand(ctx),
		newConfigCommand(ctx),
		newInstallCommand(ctx),
		newListCommand(ctx),
		newUseCommand(ctx),
		newRemoveCommand(ctx),
	)

	return cmd
}

func newInitCommand(ctx *commandContext) *cobra.Command {
	var input initCommandInput

	cmd := &cobra.Command{
		Use:  "init [framework]",
		Args: maximumArgsError("init accepts at most one framework argument", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input.Framework = ""
			if len(args) > 0 {
				input.Framework = strings.TrimSpace(args[0])
			}
			input.DocrootChanged = cmd.Flags().Changed("docroot")
			normalizedInput, err := normalizeInitCommandInput(input)
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			store, err := ctx.initStore()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runInit(cmd.OutOrStdout(), store, normalizedInput)
		},
	}
	cmd.Flags().StringVar(&input.Docroot, "docroot", "", "set docroot")
	configureCommand(cmd, initUsage)

	return cmd
}

func newNewCommand(ctx *commandContext) *cobra.Command {
	input := newCommandInput{
		PHPVersion:      defaultNewPHPVersion,
		ComposerVersion: defaultNewComposerVersion,
		NodeJSVersion:   defaultNewNodeJSVersion,
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
			if cmd.Flags().Changed("nodejs") && strings.TrimSpace(input.NodeJSVersion) == "" {
				return &statusError{code: 1, err: fmt.Errorf("--nodejs requires a non-empty value")}
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
	cmd.Flags().StringVar(&input.NodeJSVersion, "nodejs", defaultNewNodeJSVersion, "Node.js version")
	cmd.Flags().StringVar(&input.DatabaseEngine, "db-engine", "", "database engine (mysql, mariadb, or postgresql)")
	cmd.Flags().StringVar(&input.DatabaseVersion, "db-version", "", "database version")
	cmd.Flags().IntVar(&input.DatabasePort, "db-port", 0, "database port")
	configureCommand(cmd, newUsage)

	return cmd
}

func newConfigCommand(ctx *commandContext) *cobra.Command {
	var input configCommandInput

	cmd := &cobra.Command{
		Use:  "config [--env NAME] <key> <value>",
		Args: exactArgsError("config requires exactly a key and value", 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			input.Key = args[0]
			input.Value = args[1]

			return runConfig(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, input)
		},
	}
	cmd.Flags().StringVar(&input.Name, "env", "", "environment name")
	configureCommand(cmd, configUsage)

	return cmd
}

func newInstallCommand(ctx *commandContext) *cobra.Command {
	var input installCommandInput

	cmd := &cobra.Command{
		Use:  "install [tool:version]",
		Args: maximumArgsError("install accepts at most one tool:version argument", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			input.Tool = ""
			input.Version = ""
			if len(args) > 0 {
				tool, version, err := parseInstallToolVersion(args[0])
				if err != nil {
					return &statusError{code: 1, err: err}
				}
				input.Tool = tool
				input.Version = version
			}

			return runInstall(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, input)
		},
	}
	cmd.Flags().StringVar(&input.Name, "env", "", "environment name")
	cmd.Flags().BoolVar(&input.Force, "force", false, "reinstall tools even if already installed")
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

			return runUse(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, strings.TrimSpace(args[0]))
		},
	}
	configureCommand(cmd, useUsage)

	return cmd
}

func newStatusCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"info"},
		Args:    exactArgsError("status/info does not take arguments", 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runStatus(cmd.OutOrStdout(), cmd.ErrOrStderr(), store)
		},
	}
	configureCommand(cmd, statusUsage)

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

func runInit(stdout io.Writer, store backend.Store, input initCommandInput) error {
	options := backend.InitOptions{}
	if input.DocrootChanged {
		options.Docroot = input.Docroot
	}
	if input.Framework != "" {
		if err := store.InitWithFrameworkOptions(input.Framework, options); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Initialized Polka %s project at %s with config %s\n", strings.ToLower(input.Framework), store.RootDir, store.ConfigFile)
		return nil
	}

	if err := store.InitWithOptions(options); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Initialized Polka at %s with config %s\n", store.RootDir, store.ConfigFile)
	return nil
}

func normalizeInitCommandInput(input initCommandInput) (initCommandInput, error) {
	input.Framework = strings.TrimSpace(input.Framework)
	input.Docroot = strings.TrimSpace(input.Docroot)
	if input.DocrootChanged && input.Docroot == "" {
		return input, fmt.Errorf("--docroot requires a non-empty value")
	}

	return input, nil
}

func runInstall(stdout, stderr io.Writer, store backend.Store, input installCommandInput) error {
	resolvedName, setCurrent, err := resolveCommandEnvironmentName(stdout, store, input.Name, "Installing")
	if err != nil {
		return err
	}
	input.Name = resolvedName

	var requests []backend.InstallRequest
	if input.Tool != "" {
		requests = []backend.InstallRequest{{Tool: input.Tool, Version: input.Version}}
	} else {
		// Determine the ordered list of install requests so the spinner can
		// pre-register them and print placeholder lines before work begins.
		requests, err = store.InstallRequests(input.Name)
		if err != nil {
			return err
		}
	}

	spinner := newInstallSpinner(stdout, len(requests))
	for _, req := range requests {
		spinner.Register(req.Tool, req.Version)
	}
	if input.Tool == "" {
		// Pre-register PIE-managed extension entries so their provisioning
		// progress renders; a lookup failure surfaces from the install below.
		if environment, envErr := environmentByName(store, input.Name); envErr == nil {
			for _, pkg := range sortedPIEExtensionPackages(environment) {
				spinner.Register(pkg, environment.PIEExtensions[pkg])
			}
		}
	}
	spinner.printInitialLines()
	spinner.Start()

	var results []backend.InstallResult
	if input.Tool != "" {
		result, installErr := store.InstallToolWithProgress(input.Name, input.Tool, input.Version, func(progress backend.InstallProgress) {
			spinner.Update(progress.Tool, progress.Version, progress.Stage)
		})
		err = installErr
		if err == nil {
			results = []backend.InstallResult{result}
		}
	} else {
		results, err = store.InstallWithProgress(input.Name, backend.InstallOptions{Force: input.Force}, func(progress backend.InstallProgress) {
			spinner.Update(progress.Tool, progress.Version, progress.Stage)
		})
	}

	spinner.Stop()

	if err != nil {
		return err
	}
	if setCurrent {
		if err := store.Use(input.Name); err != nil {
			return err
		}
	}
	environment, err := environmentByName(store, input.Name)
	if err != nil {
		return err
	}
	writePHPCLIRuntimeWarning(stderr, environment)
	writePHPMyAdminPostgreSQLWarning(stderr, environment)
	if input.Tool != "" {
		_, _ = fmt.Fprintf(stdout, "Installed %s:%s for '%s' environment\n", input.Tool, input.Version, input.Name)
	} else {
		_, _ = fmt.Fprintf(stdout, "Installed '%s' environment\n", input.Name)
	}
	for _, result := range results {
		switch {
		case result.Skipped:
			_, _ = fmt.Fprintf(stdout, "%s %s\t(unchanged)\n", result.Tool, result.Version)
		case result.Downloaded:
			_, _ = fmt.Fprintf(stdout, "%s %s\n", result.Tool, result.Version)
		default:
			_, _ = fmt.Fprintf(stdout, "%s %s\t(cached)\n", result.Tool, result.Version)
		}
	}

	return nil
}

func runNew(stdout io.Writer, store backend.Store, input newCommandInput) error {
	environment, err := store.Create(input.Name, input.PHPVersion, input.ComposerVersion, input.NodeJSVersion, input.Database)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Created %s\tphp=%s\tcomposer=%s\tnodejs=%s\tdb=%s\n", environment.Name, labelOrUnset(environment.PHPVersion), labelOrUnset(environment.ComposerVersion), labelOrUnset(environment.NodeJSVersion), labelDatabase(environment.Database))
	return nil
}

func runConfig(stdout, stderr io.Writer, store backend.Store, input configCommandInput) error {
	resolvedName, setCurrent, err := resolveCommandEnvironmentName(stdout, store, input.Name, "Configuring")
	if err != nil {
		return err
	}
	input.Name = resolvedName

	environment, err := store.ConfigureValue(input.Name, input.Key, input.Value)
	if err != nil {
		return err
	}
	if setCurrent {
		if err := store.Use(input.Name); err != nil {
			return err
		}
	}

	writePHPCLIRuntimeWarning(stderr, environment)
	writePHPMyAdminPostgreSQLWarning(stderr, environment)
	_, _ = fmt.Fprintf(stdout, "Configured %s\t%s=%s\n", environment.Name, strings.TrimSpace(input.Key), input.Value)
	return nil
}

func resolveCommandEnvironmentName(stdout io.Writer, store backend.Store, name, action string) (string, bool, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName != "" {
		return trimmedName, false, nil
	}

	current, err := store.Current()
	if err != nil {
		return "", false, err
	}
	if current != nil {
		_, _ = fmt.Fprintf(stdout, "%s %s environment\n", action, current.Name)
		return current.Name, false, nil
	}

	_, _ = fmt.Fprintf(stdout, "%s %s environment\n", action, defaultEnvironmentName)
	return defaultEnvironmentName, true, nil
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
		_, _ = fmt.Fprintln(stdout, "No environments found. Run `polka config tools.php <version>` to add one.")
		return nil
	}

	for _, environment := range environments {
		marker := " "
		if current != nil && current.Name == environment.Name {
			marker = "*"
		}

		_, _ = fmt.Fprintf(stdout, "%s %s\tphp=%s\tcomposer=%s\tnodejs=%s\tdb=%s\n", marker, environment.Name, phpCLIListLabel(environment), labelOrUnset(environment.ComposerVersion), labelOrUnset(environment.NodeJSVersion), labelDatabase(environment.Database))
	}

	return nil
}

func runUse(stdout, stderr io.Writer, store backend.Store, name string) error {
	if err := store.Use(name); err != nil {
		return err
	}
	current, err := store.Current()
	if err != nil {
		return err
	}
	if current != nil {
		writePHPCLIRuntimeWarning(stderr, *current)
		writePHPMyAdminPostgreSQLWarning(stderr, *current)
	}

	_, _ = fmt.Fprintf(stdout, "Selected %s\n", name)
	return nil
}

// environmentByName returns a normalized environment after a command mutates it.
// sortedPIEExtensionPackages returns the environment's PIE-managed extension
// packages in stable order for progress display registration.
func sortedPIEExtensionPackages(environment backend.Environment) []string {
	packages := make([]string, 0, len(environment.PIEExtensions))
	for pkg := range environment.PIEExtensions {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)

	return packages
}

func environmentByName(store backend.Store, name string) (backend.Environment, error) {
	environments, err := store.List()
	if err != nil {
		return backend.Environment{}, err
	}
	for _, environment := range environments {
		if environment.Name == name {
			return environment, nil
		}
	}

	return backend.Environment{}, fmt.Errorf("environment %q does not exist", name)
}

// writePHPCLIRuntimeWarning makes an intentional split PHP runtime visible.
func writePHPCLIRuntimeWarning(stderr io.Writer, environment backend.Environment) {
	phpTool, phpVersion := backend.PrimaryPHPTool(environment)
	frankenPHPVersion := strings.TrimSpace(environment.FrankenPHPVersion)
	if phpTool == "" || frankenPHPVersion == "" {
		return
	}

	_, _ = fmt.Fprintf(stderr, "warning: environment %q selects %s %s for the php shim instead of FrankenPHP %s's bundled PHP; their PHP versions may differ\n", environment.Name, phpTool, phpVersion, frankenPHPVersion)
}

func writePHPMyAdminPostgreSQLWarning(stderr io.Writer, environment backend.Environment) {
	if !service.PHPMyAdminUsesPostgreSQL(environment) {
		return
	}

	_, _ = fmt.Fprintf(stderr, "warning: %s", service.PHPMyAdminPostgreSQLWarning(environment.Name))
}

// phpCLIListLabel distinguishes a FrankenPHP release from a PHP version.
func phpCLIListLabel(environment backend.Environment) string {
	tool, version := backend.PHPCLIProvider(environment)
	if tool == "frankenphp" {
		return "frankenphp:" + version
	}

	return labelOrUnset(version)
}

func runStatus(stdout, stderr io.Writer, store backend.Store) error {
	current, err := store.Current()
	if err != nil {
		return err
	}
	if current == nil {
		_, _ = fmt.Fprintln(stdout, "No active environment selected.")
		return nil
	}
	serverEndpoint, err := resolveServerEndpoint(current.Server, "")
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "environment %s\n", current.Name)
	hooks := defaultCLIHookRegistry()
	statusContext := statusHookContext{
		Stdout:      stdout,
		Stderr:      stderr,
		Store:       store,
		Environment: *current,
	}
	if err := hooks.WriteConfigStatus(statusContext); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "server %s\n", serverEndpointURL(serverEndpoint))

	return hooks.WriteRuntimeStatus(statusContext)
}

func runRemove(stdout io.Writer, store backend.Store, name string) error {
	if err := store.Remove(name); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Removed %s\n", name)
	return nil
}

type configCommandInput struct {
	Name  string
	Key   string
	Value string
}

type initCommandInput struct {
	Framework      string
	Docroot        string
	DocrootChanged bool
}

type newCommandInput struct {
	Name            string
	PHPVersion      string
	ComposerVersion string
	NodeJSVersion   string
	Database        *backend.DatabaseConfig
	DatabaseEngine  string
	DatabaseVersion string
	DatabasePort    int
}

type installCommandInput struct {
	Name    string
	Tool    string
	Version string
	Force   bool
}

func parseInstallToolVersion(value string) (string, string, error) {
	trimmed := strings.TrimSpace(value)
	tool, version, ok := strings.Cut(trimmed, ":")
	if !ok || strings.TrimSpace(tool) == "" || strings.TrimSpace(version) == "" || strings.Contains(version, ":") {
		return "", "", fmt.Errorf("install argument must be TOOL:VERSION, for example php:8.4; use --env NAME to select an environment")
	}

	return strings.ToLower(strings.TrimSpace(tool)), strings.TrimSpace(version), nil
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

func labelMailpit(mailpit *backend.MailpitConfig) string {
	if mailpit == nil {
		return "unset"
	}

	return fmt.Sprintf("%s smtp=%d ui=%s", mailpit.Version, service.EffectiveMailpitSMTPPort(mailpit), mailpitUIURLForConfig(mailpit))
}

func labelPHPMyAdmin(phpMyAdmin *backend.PHPMyAdminConfig) string {
	if phpMyAdmin == nil {
		return "unset"
	}

	return fmt.Sprintf("%s ui=%s", phpMyAdmin.Version, phpMyAdminUIURLForConfig(phpMyAdmin))
}

func labelMeilisearch(meilisearch *backend.MeilisearchConfig) string {
	if meilisearch == nil {
		return "unset"
	}

	auth := "disabled"
	if strings.TrimSpace(meilisearch.MasterKey) != "" {
		auth = "enabled"
	}

	return fmt.Sprintf("%s http=%s auth=%s", meilisearch.Version, meilisearchURLForConfig(meilisearch), auth)
}

func labelRedis(redis *backend.RedisConfig) string {
	if redis == nil {
		return "unset"
	}

	auth := "disabled"
	if strings.TrimSpace(redis.Password) != "" {
		auth = "enabled"
	}

	return fmt.Sprintf("%s url=%s auth=%s", redis.Version, redisURLForConfig(redis), auth)
}

func labelTraefik(traefik *backend.TraefikConfig) string {
	if traefik == nil {
		return "unset"
	}

	return fmt.Sprintf("%s http=%s", traefik.Version, traefikURLForConfig(traefik))
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
