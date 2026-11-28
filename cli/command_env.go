package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"polka/backend"
	"polka/config"
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
func newEnvCommand(ctx *commandContext) *ucli.Command {
	cmd := &ucli.Command{
		Name:  "env",
		Usage: "Manage environment definitions.",
		CommandNotFound: func(_ context.Context, _ *ucli.Command, command string) {
			ctx.err = &statusError{
				code:      2,
				err:       fmt.Errorf("unknown command %q", strings.TrimSpace(command)),
				showUsage: true,
				usage:     envUsage,
			}
		},
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if cmd.Args().Len() > 0 {
				return &statusError{
					code:      2,
					err:       fmt.Errorf("unknown command %q", strings.TrimSpace(cmd.Args().First())),
					showUsage: true,
					usage:     envUsage,
				}
			}

			_, _ = fmt.Fprint(ctx.stdout, envUsage)
			return nil
		},
		Commands: []*ucli.Command{
			newNewCommand(ctx),
			newConfigCommand(ctx),
			newInstallCommand(ctx),
			newListCommand(ctx),
			newUseCommand(ctx),
			newRemoveCommand(ctx),
		},
	}
	configureHelp(cmd, envUsage)

	return cmd
}

func newInitCommand(ctx *commandContext) *ucli.Command {
	var input initCommandInput

	cmd := &ucli.Command{
		Name: "init",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "docroot",
				Destination: &input.Docroot,
				Usage:       "set docroot",
			},
			&ucli.StringFlag{
				Name:        "package",
				Destination: &input.Package,
				Usage:       "set preset Composer package name (vendor/name)",
			},
		},
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if err := requireMaximumArgs(cmd, 1, "init accepts at most one framework or preset argument"); err != nil {
				return err
			}
			input.Preset = ""
			if cmd.Args().Len() > 0 {
				input.Preset = strings.TrimSpace(cmd.Args().First())
			}
			input.DocrootChanged = cmd.IsSet("docroot")
			input.PackageChanged = cmd.IsSet("package")
			normalizedInput, err := normalizeInitCommandInput(input)
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			store, err := ctx.initStore()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runInit(ctx.stdout, store, normalizedInput)
		},
	}
	configureCommand(cmd, initUsage)

	return cmd
}

func newNewCommand(ctx *commandContext) *ucli.Command {
	input := newCommandInput{
		PHPVersion:      defaultNewPHPVersion,
		ComposerVersion: defaultNewComposerVersion,
		NodeJSVersion:   defaultNewNodeJSVersion,
	}

	cmd := &ucli.Command{
		Name: "new",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "php",
				Value:       defaultNewPHPVersion,
				Destination: &input.PHPVersion,
				Usage:       "PHP version",
			},
			&ucli.StringFlag{
				Name:        "composer",
				Value:       defaultNewComposerVersion,
				Destination: &input.ComposerVersion,
				Usage:       "Composer version",
			},
			&ucli.StringFlag{
				Name:        "nodejs",
				Value:       defaultNewNodeJSVersion,
				Destination: &input.NodeJSVersion,
				Usage:       "Node.js version",
			},
			&ucli.StringFlag{
				Name:        "db-engine",
				Destination: &input.DatabaseEngine,
				Usage:       "database engine (mysql, mariadb, or postgresql)",
			},
			&ucli.StringFlag{
				Name:        "db-version",
				Destination: &input.DatabaseVersion,
				Usage:       "database version",
			},
			&ucli.IntFlag{
				Name:        "db-port",
				Destination: &input.DatabasePort,
				Usage:       "database port",
			},
		},
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if err := requireExactArgs(cmd, 1, "new requires exactly one environment name"); err != nil {
				return err
			}
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			input.Name = strings.TrimSpace(cmd.Args().First())
			if strings.TrimSpace(input.PHPVersion) == "" {
				return &statusError{code: 1, err: fmt.Errorf("--php requires a non-empty value")}
			}
			if strings.TrimSpace(input.ComposerVersion) == "" {
				return &statusError{code: 1, err: fmt.Errorf("--composer requires a non-empty value")}
			}
			if cmd.IsSet("nodejs") && strings.TrimSpace(input.NodeJSVersion) == "" {
				return &statusError{code: 1, err: fmt.Errorf("--nodejs requires a non-empty value")}
			}
			input.Database, err = buildDatabaseInput(
				cmd.IsSet("db-engine"),
				cmd.IsSet("db-version"),
				cmd.IsSet("db-port"),
				input.DatabaseEngine,
				input.DatabaseVersion,
				input.DatabasePort,
			)
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runNew(ctx.stdout, store, input)
		},
	}
	configureCommand(cmd, newUsage)

	return cmd
}

func newConfigCommand(ctx *commandContext) *ucli.Command {
	var input configCommandInput

	cmd := &ucli.Command{
		Name: "config",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "env",
				Destination: &input.Name,
				Usage:       "environment name",
			},
		},
		// No arguments opens the interactive settings form; a key and value
		// set one config value directly.
		Action: func(_ context.Context, cmd *ucli.Command) error {
			args := cmd.Args().Slice()
			if len(args) != 0 && len(args) != 2 {
				return &statusError{code: 1, err: fmt.Errorf("config requires a key and value, or no arguments for the interactive form")}
			}

			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			if len(args) == 0 {
				return runConfigForm(ctx.stdout, ctx.stderr, store, input.Name)
			}

			input.Key = args[0]
			input.Value = args[1]

			return runConfig(ctx.stdout, ctx.stderr, store, input)
		},
	}
	configureCommand(cmd, configUsage)

	return cmd
}

func newInstallCommand(ctx *commandContext) *ucli.Command {
	var input installCommandInput

	cmd := &ucli.Command{
		Name: "install",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "env",
				Destination: &input.Name,
				Usage:       "environment name",
			},
			&ucli.BoolFlag{
				Name:        "force",
				Destination: &input.Force,
				Usage:       "reinstall tools even if already installed",
			},
			&ucli.BoolFlag{
				Name:        "build",
				Destination: &input.Build,
				Usage:       "compile tools that have a build recipe but no cached payload",
			},
		},
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if err := requireMaximumArgs(cmd, 1, "install accepts at most one tool:version argument"); err != nil {
				return err
			}
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			input.Tool = ""
			input.Version = ""
			if cmd.Args().Len() > 0 {
				tool, version, err := parseInstallToolVersion(cmd.Args().First())
				if err != nil {
					return &statusError{code: 1, err: err}
				}
				input.Tool = tool
				input.Version = version
			}

			return runInstall(ctx.stdout, ctx.stderr, store, input)
		},
	}
	configureCommand(cmd, installUsage)

	return cmd
}

func newListCommand(ctx *commandContext) *ucli.Command {
	cmd := &ucli.Command{
		Name: "list",
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if err := requireExactArgs(cmd, 0, "list does not take arguments"); err != nil {
				return err
			}
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runList(ctx.stdout, store)
		},
	}
	configureCommand(cmd, listUsage)

	return cmd
}

func newUseCommand(ctx *commandContext) *ucli.Command {
	cmd := &ucli.Command{
		Name: "use",
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if err := requireExactArgs(cmd, 1, "use requires exactly one environment name"); err != nil {
				return err
			}
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runUse(ctx.stdout, ctx.stderr, store, strings.TrimSpace(cmd.Args().First()))
		},
	}
	configureCommand(cmd, useUsage)

	return cmd
}

func newStatusCommand(ctx *commandContext) *ucli.Command {
	cmd := &ucli.Command{
		Name:    "status",
		Aliases: []string{"info"},
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if err := requireExactArgs(cmd, 0, "status/info does not take arguments"); err != nil {
				return err
			}
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runStatus(ctx.stdout, ctx.stderr, store)
		},
	}
	configureCommand(cmd, statusUsage)

	return cmd
}

func newRemoveCommand(ctx *commandContext) *ucli.Command {
	cmd := &ucli.Command{
		Name: "remove",
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if err := requireExactArgs(cmd, 1, "remove requires exactly one environment name"); err != nil {
				return err
			}
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runRemove(ctx.stdout, store, strings.TrimSpace(cmd.Args().First()))
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
	if input.PackageChanged {
		options.Package = input.Package
	}
	if input.Preset != "" {
		if input.PackageChanged {
			if _, isFramework := store.FrameworkPlugin(input.Preset); isFramework {
				return fmt.Errorf("--package can only be used with a project preset")
			}
		}
		result, err := store.InitWithPresetOptions(input.Preset, options)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "Initialized Polka %s project at %s with config %s\n", strings.ToLower(input.Preset), store.RootDir, store.ConfigFile); err != nil {
			return err
		}
		if input.Preset == "php-extension" {
			if initResultContains(result.ScaffoldWritten, "composer.json") {
				if _, err := fmt.Fprintln(stdout, "Wrote composer.json (type: php-ext) for PIE"); err != nil {
					return err
				}
			} else if initResultContains(result.ScaffoldSkipped, "composer.json") {
				if _, err := fmt.Fprintln(stdout, "Kept existing composer.json"); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(stdout, "Next steps: set name/description/license in composer.json, then polka install"); err != nil {
				return err
			}
		}
		return nil
	}

	if err := store.InitWithOptions(options); err != nil {
		return err
	}

	_, err := fmt.Fprintf(stdout, "Initialized Polka at %s with config %s\n", store.RootDir, store.ConfigFile)
	return err
}

func normalizeInitCommandInput(input initCommandInput) (initCommandInput, error) {
	input.Preset = strings.ToLower(strings.TrimSpace(input.Preset))
	input.Docroot = strings.TrimSpace(input.Docroot)
	input.Package = strings.ToLower(strings.TrimSpace(input.Package))
	if input.DocrootChanged && input.Docroot == "" {
		return input, fmt.Errorf("--docroot requires a non-empty value")
	}
	if input.PackageChanged {
		if input.Package == "" {
			return input, fmt.Errorf("--package requires a non-empty value")
		}
		if input.Preset == "" {
			return input, fmt.Errorf("--package requires a project preset")
		}
		if err := config.ValidatePIEExtensionPackage(input.Package); err != nil {
			return input, err
		}
	}

	return input, nil
}

func initResultContains(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}

	return false
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
			for _, pkg := range sortedPECLExtensionPackages(environment) {
				spinner.Register("pecl:"+pkg, environment.PECLExtensions[pkg].Version)
			}
		}
	}
	if input.Build {
		// Show the internal PHP compile too, since it can take many minutes. A
		// lookup failure surfaces from the install below.
		if environment, envErr := environmentByName(store, input.Name); envErr == nil {
			if request, needed, requestErr := store.InternalBuildRequest(environment); requestErr == nil && needed {
				spinner.Register(request.Tool, request.Version)
			}
		}
		// Printed once up front: a long path in a spinner row could wrap the
		// terminal line and break its in-place rendering.
		_, _ = fmt.Fprintf(stdout, "Compile logs are written to %s\n", store.BuildLogDir())
	}
	spinner.printInitialLines()
	spinner.Start()

	var results []backend.InstallResult
	var warnings []string
	options := backend.InstallOptions{
		Force: input.Force,
		Build: input.Build,
		// Warnings print after the spinner stops so they do not tear
		// its in-place rendering.
		Warn: func(message string) { warnings = append(warnings, message) },
		BuildContext: func() (context.Context, context.CancelFunc) {
			return signal.NotifyContext(context.Background(), os.Interrupt)
		},
	}
	if input.Tool != "" {
		result, installErr := store.InstallToolWithOptions(input.Name, input.Tool, input.Version, options, func(progress backend.InstallProgress) {
			spinner.Report(progress)
		})
		err = installErr
		if err == nil {
			results = []backend.InstallResult{result}
		}
	} else {
		results, err = store.InstallWithProgress(input.Name, options, func(progress backend.InstallProgress) {
			spinner.Report(progress)
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
	for _, warning := range warnings {
		_, _ = fmt.Fprintf(stderr, "warning: %s\n", warning)
	}
	if input.Tool != "" {
		_, _ = fmt.Fprintf(stdout, "Installed %s:%s for '%s' environment\n", input.Tool, input.Version, input.Name)
	} else {
		_, _ = fmt.Fprintf(stdout, "Installed '%s' environment\n", input.Name)
	}
	for _, result := range results {
		switch {
		case result.Skipped:
			_, _ = fmt.Fprintf(stdout, "%s %s\t(unchanged)\n", result.Tool, result.Version)
		case result.Built:
			_, _ = fmt.Fprintf(stdout, "%s %s\t(built)\n", result.Tool, result.Version)
		case result.Downloaded:
			_, _ = fmt.Fprintf(stdout, "%s %s\n", result.Tool, result.Version)
		default:
			_, _ = fmt.Fprintf(stdout, "%s %s\t(cached)\n", result.Tool, result.Version)
		}
	}
	if input.Tool == "" {
		toolchain, err := store.MSVCToolchain(environment)
		if err != nil {
			return err
		}
		if toolchain != nil {
			_, _ = fmt.Fprintf(stdout, "msvc %s\t(%s)\n", toolchain.Version, toolchain.DisplayName)
		}
	}

	return nil
}

func runNew(stdout io.Writer, store backend.Store, input newCommandInput) error {
	environment, err := store.Create(input.Name, input.PHPVersion, input.ComposerVersion, input.NodeJSVersion, input.Database)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Created %s\tphp=%s\tcomposer=%s\tnodejs=%s\tdb=%s\n", environment.Name, labelOrUnset(environment.ToolVersion("php")), labelOrUnset(environment.ToolVersion("composer")), labelOrUnset(environment.ToolVersion("nodejs")), labelDatabase(environment.Database))
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

		_, _ = fmt.Fprintf(stdout, "%s %s\tphp=%s\tcomposer=%s\tnodejs=%s\tdb=%s\n", marker, environment.Name, phpCLIListLabel(environment), labelOrUnset(environment.ToolVersion("composer")), labelOrUnset(environment.ToolVersion("nodejs")), labelDatabase(environment.Database))
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

// sortedPECLExtensionPackages returns legacy PECL packages in stable order for
// install progress display registration.
func sortedPECLExtensionPackages(environment backend.Environment) []string {
	packages := make([]string, 0, len(environment.PECLExtensions))
	for pkg := range environment.PECLExtensions {
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
	frankenPHPVersion := strings.TrimSpace(environment.ToolVersion("frankenphp"))
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
	serverURL, routed, err := environmentServerURL(store, *current)
	if err != nil {
		return err
	}
	if routed {
		serverURL += " (via router)"
	}

	// Status reports the effective ports, including leased ones.
	environment, err := runtimeServiceEnvironment(store, *current)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "environment %s\n", current.Name)
	hooks := defaultCLIHookRegistry()
	statusContext := statusHookContext{
		Stdout:      stdout,
		Stderr:      stderr,
		Store:       store,
		Environment: environment,
	}
	if err := hooks.WriteConfigStatus(statusContext); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "server %s\n", serverURL)

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
	Preset         string
	Docroot        string
	Package        string
	DocrootChanged bool
	PackageChanged bool
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
	Build   bool
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

func labelRabbitMQ(rabbitMQ *backend.RabbitMQConfig) string {
	if rabbitMQ == nil {
		return "unset"
	}
	return fmt.Sprintf("%s amqp=%s management=%s auth=enabled", rabbitMQ.Version, rabbitMQURLForConfig(rabbitMQ), rabbitMQManagementURLForConfig(rabbitMQ))
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
