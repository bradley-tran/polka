package cli

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
	"polka/config"
)

func newExtCommand(ctx *commandContext) *cobra.Command {
	var envName string
	var configureOptions []string
	var clearConfigureOptions bool

	cmd := &cobra.Command{
		Use:  "ext <install|remove> <vendor/name[:version]|name[:version]>",
		Args: exactArgsError("ext requires an action and one extension package", 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			switch strings.ToLower(strings.TrimSpace(args[0])) {
			case "install":
				return runExtInstall(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, envName, args[1], configureOptions, clearConfigureOptions)
			case "remove", "uninstall":
				if len(configureOptions) > 0 || clearConfigureOptions {
					return &statusError{code: 1, err: fmt.Errorf("configure options are only valid with ext install")}
				}
				return runExtRemove(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, envName, args[1])
			default:
				return &statusError{code: 1, err: fmt.Errorf("unsupported ext action %q: use install or remove", args[0])}
			}
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "environment name")
	cmd.Flags().StringArrayVar(&configureOptions, "configure-option", nil, "legacy PECL configure option NAME=VALUE (repeatable)")
	cmd.Flags().BoolVar(&clearConfigureOptions, "clear-configure-options", false, "clear persisted legacy PECL configure options")
	configureCommand(cmd, extUsage)

	return cmd
}

// runExtInstall installs a PHP extension with the internal PIE against the
// environment's installed PHP, then records it in the config and regenerates
// the runtime php.ini.
func runExtInstall(stdout, stderr io.Writer, store backend.Store, envName, spec string, optionValues []string, clearOptions bool) error {
	provider, pkg, constraint, err := parseExtSpec(spec)
	if err != nil {
		return &statusError{code: 1, err: err}
	}

	resolvedName, setCurrent, err := resolveCommandEnvironmentName(stdout, store, envName, "Configuring")
	if err != nil {
		return &statusError{code: 1, err: err}
	}

	if provider == extProviderPECL {
		return runPECLExtInstall(stdout, stderr, store, resolvedName, setCurrent, pkg, constraint, optionValues, clearOptions)
	}
	if len(optionValues) > 0 || clearOptions {
		return &statusError{code: 1, err: fmt.Errorf("--configure-option is only supported for legacy PECL packages; PIE package %s does not use it", pkg)}
	}

	projectPHP, err := resolveExtTargetPHP(store, resolvedName)
	if err != nil {
		return &statusError{code: 1, err: err}
	}

	// Tee PIE output so the resolved version can be parsed from it while the
	// user still sees the live install log.
	var output bytes.Buffer
	exitCode, err := store.RunPIE(io.MultiWriter(stdout, &output), io.MultiWriter(stderr, &output), backend.PIEExtensionInstallArgs(projectPHP, pkg, constraint), internalToolProgress(stdout))
	if err != nil {
		return &statusError{code: 1, err: err}
	}
	if exitCode != 0 {
		// PIE already reported the failure; propagate its exit code.
		return &statusError{code: exitCode}
	}

	version := constraint
	if version == "" {
		version = parsePIEInstalledVersion(output.String(), pkg)
	}
	if version == "" {
		version = "*"
		_, _ = fmt.Fprintf(stderr, "warning: could not determine the installed %s version; recording * (latest). Pin it with polka ext install %s:<version>\n", pkg, pkg)
	}

	if _, err := store.ConfigureValue(resolvedName, "php-extensions."+pkg, version); err != nil {
		return &statusError{code: 1, err: err}
	}
	if setCurrent {
		if err := store.Use(resolvedName); err != nil {
			return &statusError{code: 1, err: err}
		}
	}
	if err := store.SyncPHPRuntimeConfig(resolvedName); err != nil {
		return &statusError{code: 1, err: err}
	}

	_, _ = fmt.Fprintf(stdout, "Installed %s %s for '%s' environment\n", pkg, version, resolvedName)
	return nil
}

// runExtRemove uninstalls a PIE-managed PHP extension, deletes its config
// entry, and regenerates the runtime php.ini.
func runExtRemove(stdout, stderr io.Writer, store backend.Store, envName, spec string) error {
	provider, pkg, constraint, err := parseExtSpec(spec)
	if err != nil {
		return &statusError{code: 1, err: err}
	}
	if constraint != "" {
		return &statusError{code: 1, err: fmt.Errorf("ext remove takes a package without a version, such as %s", pkg)}
	}

	resolvedName, setCurrent, err := resolveCommandEnvironmentName(stdout, store, envName, "Configuring")
	if err != nil {
		return &statusError{code: 1, err: err}
	}
	if provider == extProviderPECL {
		return runPECLExtRemove(stdout, stderr, store, resolvedName, setCurrent, pkg)
	}

	projectPHP, err := resolveExtTargetPHP(store, resolvedName)
	if err != nil {
		return &statusError{code: 1, err: err}
	}

	exitCode, err := store.RunPIE(stdout, stderr, backend.PIEExtensionUninstallArgs(projectPHP, pkg), internalToolProgress(stdout))
	if err != nil {
		return &statusError{code: 1, err: err}
	}
	if exitCode != 0 {
		return &statusError{code: exitCode}
	}

	// An empty value deletes the vendor/name entry from php-extensions.
	if _, err := store.ConfigureValue(resolvedName, "php-extensions."+pkg, ""); err != nil {
		return &statusError{code: 1, err: err}
	}
	if setCurrent {
		if err := store.Use(resolvedName); err != nil {
			return &statusError{code: 1, err: err}
		}
	}
	if err := store.SyncPHPRuntimeConfig(resolvedName); err != nil {
		return &statusError{code: 1, err: err}
	}

	_, _ = fmt.Fprintf(stdout, "Removed %s from '%s' environment\n", pkg, resolvedName)
	return nil
}

func runPECLExtInstall(stdout, stderr io.Writer, store backend.Store, environmentName string, setCurrent bool, packageName, version string, optionValues []string, clearOptions bool) error {
	options, err := parsePECLConfigureOptions(optionValues)
	if err != nil {
		return &statusError{code: 1, err: err}
	}
	if len(optionValues) == 0 && !clearOptions {
		environment, err := environmentByName(store, environmentName)
		if err != nil {
			return &statusError{code: 1, err: err}
		}
		if configured, ok := environment.PECLExtensions[packageName]; ok {
			options = configured.ConfigureOptions
		}
	}
	_, _ = fmt.Fprintln(stderr, "warning: PECL support is legacy and discouraged; prefer a PIE vendor/name package when available")
	result, err := store.InstallPECLExtension(stdout, stderr, environmentName, packageName, version, options)
	if err != nil {
		return &statusError{code: 1, err: err}
	}
	if _, err := store.ConfigurePECLExtension(environmentName, packageName, config.PECLExtensionConfig{Version: result.Version, ConfigureOptions: options}); err != nil {
		return &statusError{code: 1, err: err}
	}
	if setCurrent {
		if err := store.Use(environmentName); err != nil {
			return &statusError{code: 1, err: err}
		}
	}
	if err := store.SyncPHPRuntimeConfig(environmentName); err != nil {
		return &statusError{code: 1, err: err}
	}
	_, _ = fmt.Fprintf(stdout, "Installed legacy PECL %s %s for '%s' environment\n", packageName, result.Version, environmentName)

	return nil
}

func runPECLExtRemove(stdout, stderr io.Writer, store backend.Store, environmentName string, setCurrent bool, packageName string) error {
	if _, err := store.RemovePECLExtension(stderr, environmentName, packageName); err != nil {
		return &statusError{code: 1, err: err}
	}
	if _, err := store.ConfigurePECLExtension(environmentName, packageName, config.PECLExtensionConfig{}); err != nil {
		return &statusError{code: 1, err: err}
	}
	if setCurrent {
		if err := store.Use(environmentName); err != nil {
			return &statusError{code: 1, err: err}
		}
	}
	if err := store.SyncPHPRuntimeConfig(environmentName); err != nil {
		return &statusError{code: 1, err: err}
	}
	_, _ = fmt.Fprintf(stdout, "Removed legacy PECL %s from '%s' environment\n", packageName, environmentName)

	return nil
}

// resolveExtTargetPHP returns the executable of the environment's installed
// standalone PHP runtime: the install PIE builds extensions against.
func resolveExtTargetPHP(store backend.Store, name string) (string, error) {
	environment, err := environmentByName(store, name)
	if err != nil {
		return "", err
	}

	tool, version := backend.PrimaryPHPTool(environment)
	if tool == "" {
		return "", fmt.Errorf("environment %q does not define a standalone php or php-zts runtime; PIE cannot target FrankenPHP's embedded PHP", name)
	}

	projectPHP, err := store.ResolveInstalledTool(tool, version)
	if err != nil {
		return "", fmt.Errorf("%s %s is not installed for environment %q; run `polka install` first", tool, version, name)
	}

	return projectPHP, nil
}

// parseExtPackageSpec splits vendor/name[:version] into a validated package
// and optional version constraint.
func parseExtPackageSpec(spec string) (string, string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(spec))
	pkg, constraint := trimmed, ""
	if index := strings.Index(trimmed, ":"); index >= 0 {
		pkg = strings.TrimSpace(trimmed[:index])
		constraint = strings.TrimSpace(trimmed[index+1:])
		if constraint == "" {
			return "", "", fmt.Errorf("extension spec %q has an empty version constraint", spec)
		}
	}
	if err := config.ValidatePIEExtensionPackage(pkg); err != nil {
		return "", "", err
	}
	if constraint != "" {
		if err := config.ValidatePIEExtensionVersion(pkg, constraint); err != nil {
			return "", "", err
		}
	}

	return pkg, constraint, nil
}

type extProvider int

const (
	extProviderPIE extProvider = iota
	extProviderPECL
)

func parseExtSpec(spec string) (extProvider, string, string, error) {
	trimmed := strings.TrimSpace(spec)
	packagePart := trimmed
	if index := strings.Index(trimmed, ":"); index >= 0 {
		packagePart = trimmed[:index]
	}
	if strings.Contains(packagePart, "/") {
		pkg, version, err := parseExtPackageSpec(spec)
		return extProviderPIE, pkg, version, err
	}
	pkg, version, err := parsePECLExtPackageSpec(spec)
	return extProviderPECL, pkg, version, err
}

func parsePECLExtPackageSpec(spec string) (string, string, error) {
	trimmed := strings.TrimSpace(spec)
	pkg, version := trimmed, ""
	if index := strings.Index(trimmed, ":"); index >= 0 {
		pkg = strings.TrimSpace(trimmed[:index])
		version = strings.TrimSpace(trimmed[index+1:])
		if version == "" {
			return "", "", fmt.Errorf("extension spec %q has an empty version", spec)
		}
	}
	pkg = strings.ToLower(pkg)
	if err := config.ValidatePECLExtensionPackage(pkg); err != nil {
		return "", "", err
	}
	if version != "" {
		if err := config.ValidatePECLExtensionVersion(pkg, version); err != nil {
			return "", "", err
		}
	}

	return pkg, version, nil
}

func parsePECLConfigureOptions(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	options := make(map[string]string, len(values))
	for _, value := range values {
		name, optionValue, ok := strings.Cut(value, "=")
		name = strings.ToLower(strings.TrimLeft(strings.TrimSpace(name), "-"))
		if !ok || name == "" {
			return nil, fmt.Errorf("configure option %q must use NAME=VALUE", value)
		}
		if _, exists := options[name]; exists {
			return nil, fmt.Errorf("configure option %q was provided more than once", name)
		}
		options[name] = strings.TrimSpace(optionValue)
	}

	return options, nil
}

// parsePIEInstalledVersion extracts the resolved package version from PIE's
// install output, tolerating "vendor/name:1.2.3", "vendor/name 1.2.3", and
// "vendor/name@v1.2.3" shapes across PIE releases.
func parsePIEInstalledVersion(output, pkg string) string {
	pattern := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(pkg) + `[:@\s]+v?([0-9][0-9A-Za-z.+-]*)`)
	match := pattern.FindStringSubmatch(output)
	if len(match) == 2 {
		return strings.TrimSpace(match[1])
	}

	return ""
}

// internalToolProgress prints download/install lines for internal tool
// provisioning triggered by a PIE run.
func internalToolProgress(stdout io.Writer) func(backend.InstallProgress) {
	return func(progress backend.InstallProgress) {
		switch progress.Stage {
		case backend.InstallProgressDownloading:
			_, _ = fmt.Fprintf(stdout, "Downloading internal %s %s...\n", progress.Tool, progress.Version)
		case backend.InstallProgressInstalled:
			_, _ = fmt.Fprintf(stdout, "Installed internal %s %s\n", progress.Tool, progress.Version)
		}
	}
}
