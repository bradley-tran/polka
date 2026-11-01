package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
	"polka/plugins"
	"polka/tools"
)

func newCreateProjectCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use: "create-project <package> [directory] [composer-args...]",
		// Composer options such as --stability pass through untouched.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if wantsCreateProjectHelp(args) {
				_, _ = fmt.Fprint(cmd.OutOrStdout(), createProjectUsage)
				return nil
			}

			store, err := ctx.initStore()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runCreateProject(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, args)
		},
	}
	configureCommand(cmd, createProjectUsage)

	return cmd
}

// wantsCreateProjectHelp detects help flags manually because flag parsing is
// disabled for composer pass-through.
func wantsCreateProjectHelp(args []string) bool {
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "-h" || trimmed == "--help" {
			return true
		}
	}

	return false
}

// runCreateProject downloads the scaffold with internal Composer, installs the
// detected project's tools, then completes Composer setup in that environment.
func runCreateProject(stdout, stderr io.Writer, store backend.Store, args []string) error {
	positionals, options := splitCreateProjectArgs(args)
	if len(positionals) == 0 {
		return &statusError{code: 1, err: fmt.Errorf("create-project requires a composer package argument, such as laravel/laravel")}
	}
	pkg := positionals[0]

	workingDir, err := os.Getwd()
	if err != nil {
		return &statusError{code: 1, err: fmt.Errorf("resolve current working directory: %w", err)}
	}

	targetDir := plugins.ComposerCreateProjectDirectory(append([]string{"create-project"}, positionals...), composerCommandWorkingDir(workingDir, args))
	if targetDir == "" {
		return &statusError{code: 1, err: fmt.Errorf("cannot determine the create-project target directory from %q", pkg)}
	}

	phpPath, composerPath, env, err := ensureInternalPHPAndPHAR(stdout, store, tools.Composer)
	if err != nil {
		return &statusError{code: 1, err: err}
	}

	// Neither dependency installation nor root-package scripts may run before
	// project tools exist. Put the flags before any caller-supplied `--`.
	scaffoldArgs := append([]string{"create-project", "--no-install", "--no-scripts"}, args...)
	exitCode, err := runInternalPHAR(stdout, stderr, env, phpPath, composerPath, scaffoldArgs)
	if err != nil {
		return &statusError{code: 1, err: err}
	}
	if exitCode != 0 {
		// Composer already reported the failure on stderr; propagate its code.
		return &statusError{code: exitCode}
	}

	framework, err := store.DetectFramework(pkg, targetDir)
	if err != nil {
		return &statusError{code: 1, err: err}
	}
	projectStore := backend.NewProjectStore(targetDir)
	projectStore.Registry = store.Registry
	projectStore.Plugins = store.Plugins
	projectStore.CacheDir = store.CacheDir
	projectStore.Downloader = store.Downloader
	if framework != "" {
		err = projectStore.InitWithFrameworkOptions(framework, backend.InitOptions{})
	} else {
		err = projectStore.InitWithOptions(backend.InitOptions{})
	}
	if err != nil {
		return &statusError{code: 1, err: err}
	}
	current, err := projectStore.Current()
	if err != nil {
		return &statusError{code: 1, err: err}
	}
	if current != nil && len(current.Tools) > 0 {
		if err := runInstall(stdout, stderr, projectStore, installCommandInput{Name: defaultEnvironmentName}); err != nil {
			return &statusError{code: 1, err: err}
		}
	}

	// Configure framework secrets before building Composer's environment. In
	// particular, Laravel's create-project migration must see the managed
	// database instead of the SQLite settings from its scaffold's .env.example.
	hookArgs := []string{"create-project", pkg, targetDir}
	if err := runPostComposerHook(projectStore, "composer", hookArgs, workingDir); err != nil {
		return &statusError{code: 1, err: err}
	}
	if current != nil && !slices.Contains(options, "--no-scripts") {
		// Hooks may connect immediately, so initialize and start the selected
		// database before Composer runs any application code.
		if err := startDatabaseServiceHook(startHookContext{Store: projectStore, Environment: *current}); err != nil {
			return &statusError{code: 1, err: fmt.Errorf("prepare database for Composer scripts: %w", err)}
		}
	}

	// Without a package argument Composer uses the existing composer.json and
	// dispatches post-root-package-install, dependency events and
	// post-create-project-cmd itself, preserving their order and skip options.
	completionArgs := append([]string{"create-project", "--working-dir", targetDir}, options...)
	exitCode, err = completeCreateProject(stdout, stderr, projectStore, phpPath, composerPath, completionArgs)
	if err != nil {
		return &statusError{code: 1, err: err}
	}
	if exitCode != 0 {
		return &statusError{code: exitCode}
	}

	// Reapply managed settings if Composer's hooks created or rewrote files.
	if err := runPostComposerHook(projectStore, "composer", hookArgs, workingDir); err != nil {
		return &statusError{code: 1, err: err}
	}

	if framework != "" {
		_, _ = fmt.Fprintf(stdout, "Created %s project at %s\n", framework, targetDir)
	} else {
		_, _ = fmt.Fprintf(stdout, "Created project at %s (no framework detected; run `polka init <framework>` to change the config)\n", targetDir)
	}
	_, _ = fmt.Fprintf(stdout, "Next steps: cd %s && polka sh\n", relativeOrAbsolutePath(workingDir, targetDir))

	return nil
}

// splitCreateProjectArgs separates package/directory/version from options for
// resuming Composer in the scaffold. Root-download and working-directory options
// belong only to the first invocation; other options pass through to both.
func splitCreateProjectArgs(args []string) (positionals, options []string) {
	positionalOnly := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" {
			continue
		}
		if positionalOnly || !strings.HasPrefix(trimmed, "-") {
			positionals = append(positionals, arg)
			continue
		}
		if trimmed == "--" {
			positionalOnly = true
			continue
		}
		name, _, hasValue := strings.Cut(trimmed, "=")
		rootOnly, consumesValue := false, false
		switch name {
		case "--stability", "-s", "--repository", "--repository-url", "--working-dir", "-d":
			rootOnly, consumesValue = true, true
		case "--add-repository", "--no-secure-http", "--keep-vcs", "--remove-vcs", "--ask":
			rootOnly = true
		case "--prefer-install", "--audit-format", "--ignore-platform-req":
			consumesValue = true
		default:
			rootOnly = strings.HasPrefix(name, "-d") || strings.HasPrefix(name, "-s")
		}
		if !rootOnly {
			options = append(options, arg)
		}
		if consumesValue && !hasValue && index+1 < len(args) {
			index++
			if !rootOnly {
				options = append(options, args[index])
			}
		}
	}

	return positionals, options
}

// completeCreateProject runs Composer with project PHP, runtime configuration
// and managed tools on PATH, including support for framework script entrypoints.
func completeCreateProject(stdout, stderr io.Writer, store backend.Store, phpPath, composerPath string, args []string) (int, error) {
	current, err := store.Current()
	if err != nil {
		return 0, err
	}
	// A plain init for an unrecognized package has no configured tools. Keep
	// using the internal hosts for those missing from the project config.
	if current != nil {
		if current.ToolVersion(tools.PHP) != "" || current.ToolVersion(tools.PHPZTS) != "" || current.ToolVersion(tools.FrankenPHP) != "" {
			phpPath, err = store.ResolveTool("php")
			if err != nil {
				return 0, err
			}
		}
		if current.ToolVersion(tools.Composer) != "" {
			composerPath, err = store.ResolveTool("composer")
			if err != nil {
				return 0, err
			}
		}
	}
	env, err := prepareShellEnvironment(runtime.GOOS, os.Environ(), store, store.ProjectDir, defaultEnvironmentName)
	if err != nil {
		return 0, err
	}
	env, err = applyManagedPHPRuntimeConfig(runtime.GOOS, env, phpPath)
	if err != nil {
		return 0, err
	}
	// The internal PHP fallback also needs to be discoverable by script
	// commands that invoke `php` directly instead of Composer's @php alias.
	pathKey, pathValue, _ := lookupEnvValue(runtime.GOOS, env, "PATH")
	if pathKey == "" {
		pathKey = "PATH"
	}
	env = replaceEnvValue(runtime.GOOS, env, pathKey, joinPathList(runtime.GOOS, pathValue, filepath.Dir(phpPath)))
	target := composerPath
	if dispatchPHARRequiresManagedPHP("composer", composerPath) {
		target = phpPath
		args = append([]string{composerPath}, args...)
	}
	return executeTargetWithComposerScriptSupport(runtime.GOOS, true, stdout, stderr, env, store.ProjectDir, target, args)
}

// relativeOrAbsolutePath prefers the shorter relative form for user-facing
// next-step hints.
func relativeOrAbsolutePath(base, target string) string {
	relative, err := filepath.Rel(base, target)
	if err != nil || strings.HasPrefix(relative, "..") {
		return target
	}

	return relative
}
