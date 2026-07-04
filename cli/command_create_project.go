package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// runCreateProject scaffolds a new application with the internal composer,
// then initializes polka in the target directory with detected framework
// defaults so `polka install` works immediately.
func runCreateProject(stdout, stderr io.Writer, store backend.Store, args []string) error {
	pkg := createProjectPackage(args)
	if pkg == "" {
		return &statusError{code: 1, err: fmt.Errorf("create-project requires a composer package argument, such as laravel/laravel")}
	}

	workingDir, err := os.Getwd()
	if err != nil {
		return &statusError{code: 1, err: fmt.Errorf("resolve current working directory: %w", err)}
	}

	composerArgs := append([]string{"create-project"}, args...)
	targetDir := plugins.ComposerCreateProjectDirectory(composerArgs, workingDir)
	if targetDir == "" {
		return &statusError{code: 1, err: fmt.Errorf("cannot determine the create-project target directory from %q", pkg)}
	}

	phpPath, composerPath, env, err := ensureInternalPHPAndPHAR(stdout, store, tools.Composer)
	if err != nil {
		return &statusError{code: 1, err: err}
	}

	exitCode, err := runInternalPHAR(stdout, stderr, env, phpPath, composerPath, composerArgs)
	if err != nil {
		return &statusError{code: 1, err: err}
	}
	if exitCode != 0 {
		// Composer already reported the failure on stderr; propagate its code.
		return &statusError{code: exitCode}
	}

	framework := plugins.DetectFramework(pkg, targetDir)
	projectStore := backend.NewProjectStore(targetDir)
	if framework != "" {
		err = projectStore.InitWithFrameworkOptions(framework, backend.InitOptions{})
	} else {
		err = projectStore.InitWithOptions(backend.InitOptions{})
	}
	if err != nil {
		return &statusError{code: 1, err: err}
	}

	// Reuse the composer post-hook so framework plugins write database
	// secrets into the fresh scaffold exactly as a dispatched composer
	// create-project would.
	if err := runPostComposerHook(projectStore, "composer", composerArgs, workingDir); err != nil {
		return &statusError{code: 1, err: err}
	}

	if framework != "" {
		_, _ = fmt.Fprintf(stdout, "Created %s project at %s\n", framework, targetDir)
	} else {
		_, _ = fmt.Fprintf(stdout, "Created project at %s (no framework detected; run `polka init <framework>` to change the config)\n", targetDir)
	}
	_, _ = fmt.Fprintf(stdout, "Next steps: cd %s && polka install\n", relativeOrAbsolutePath(workingDir, targetDir))

	return nil
}

// createProjectPackage returns the first positional argument: the composer
// package to scaffold.
func createProjectPackage(args []string) string {
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" || strings.HasPrefix(trimmed, "-") {
			continue
		}

		return trimmed
	}

	return ""
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
