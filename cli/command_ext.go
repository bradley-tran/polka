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

	cmd := &cobra.Command{
		Use:  "ext <install|remove> <vendor/name[:version]>",
		Args: exactArgsError("ext requires an action and one extension package", 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			switch strings.ToLower(strings.TrimSpace(args[0])) {
			case "install":
				return runExtInstall(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, envName, args[1])
			case "remove", "uninstall":
				return runExtRemove(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, envName, args[1])
			default:
				return &statusError{code: 1, err: fmt.Errorf("unsupported ext action %q: use install or remove", args[0])}
			}
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "environment name")
	configureCommand(cmd, extUsage)

	return cmd
}

// runExtInstall installs a PHP extension with the internal PIE against the
// environment's installed PHP, then records it in the config and regenerates
// the runtime php.ini.
func runExtInstall(stdout, stderr io.Writer, store backend.Store, envName, spec string) error {
	pkg, constraint, err := parseExtPackageSpec(spec)
	if err != nil {
		return &statusError{code: 1, err: err}
	}

	resolvedName, setCurrent, err := resolveCommandEnvironmentName(stdout, store, envName, "Configuring")
	if err != nil {
		return &statusError{code: 1, err: err}
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
	pkg, constraint, err := parseExtPackageSpec(spec)
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
