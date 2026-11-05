package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
)

type commandContext struct {
	stdout io.Writer
	stderr io.Writer
	// projectDir is the -C/--dir value: the directory holding polka.yaml. When
	// empty, commands discover the project from the working directory.
	projectDir string
	exitCode   int
}

type statusError struct {
	code      int
	err       error
	showUsage bool
	usage     string
}

func (e *statusError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}

	return e.err.Error()
}

func Run(stdout, stderr io.Writer, args []string) int {
	ctx := &commandContext{stdout: stdout, stderr: stderr}
	root := newRootCommand(ctx)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	if err := root.Execute(); err != nil {
		return handleRunError(stderr, err)
	}

	return ctx.exitCode
}

func newRootCommand(ctx *commandContext) *cobra.Command {
	root := &cobra.Command{
		Use:              "polka",
		Short:            "Polka manages isolated PHP virtual environments.",
		SilenceErrors:    true,
		SilenceUsage:     true,
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return &statusError{code: 2, err: fmt.Errorf("unknown command %q", strings.TrimSpace(args[0])), showUsage: true, usage: rootUsage}
			}

			writeRootUsage(cmd.OutOrStdout())
			return nil
		},
	}

	root.CompletionOptions.DisableDefaultCmd = true
	configureHelp(root, rootUsage)
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return &statusError{code: 2, err: err, showUsage: true, usage: rootUsage}
	})
	root.PersistentFlags().StringVarP(&ctx.projectDir, "dir", "C", "", "use the project whose polka.yaml is in DIR")
	root.AddCommand(
		newInitCommand(ctx),
		newEnvCommand(ctx),
		newCreateProjectCommand(ctx),
		newExtCommand(ctx),
		newBuildCommand(ctx),
		newCertInstallCommand(ctx),
		newDBCommand(ctx),
		newServeCommand(ctx),
		newStopCommand(ctx),
		newExecCommand(ctx),
		newShCommand(ctx),
		newLogsCommand(ctx),
		newStatusCommand(ctx),
		newDashboardCommand(ctx),
		newDispatchCommand(ctx),
	)
	for _, spec := range shortcutSpecs() {
		root.AddCommand(newShortcutCommand(spec))
	}

	return root
}

// resolveStore opens the project whose polka.yaml is in projectDir, or
// discovers the project from the working directory when projectDir is empty.
// Either way the state directory follows the config's root key.
func resolveStore(projectDir string) (backend.Store, error) {
	if strings.TrimSpace(projectDir) != "" {
		return backend.StoreForProjectDir(projectDir)
	}

	return backend.DefaultStore()
}

// resolveInitStore opens the project in projectDir, or in the working directory
// when projectDir is empty, without requiring polka.yaml to exist yet.
func resolveInitStore(projectDir string) (backend.Store, error) {
	return backend.InitStore(projectDir)
}

// store opens the project selected by -C/--dir or the working directory.
func (ctx *commandContext) store() (backend.Store, error) {
	return resolveStore(ctx.projectDir)
}

// initStore opens the project for commands that may run before polka.yaml
// exists, such as init, create-project and build.
func (ctx *commandContext) initStore() (backend.Store, error) {
	return resolveInitStore(ctx.projectDir)
}

func configureHelp(cmd *cobra.Command, usage string) {
	cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		_, _ = fmt.Fprint(cmd.OutOrStdout(), usage)
	})
}

func configureCommand(cmd *cobra.Command, usage string) {
	configureHelp(cmd, usage)
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return &statusError{code: 1, err: err}
	})
}

func handleRunError(stderr io.Writer, err error) int {
	var status *statusError
	if errors.As(err, &status) {
		if status.err != nil {
			fmt.Fprintf(stderr, "error: %v\n", status.err)
			if status.showUsage && status.usage != "" {
				fmt.Fprintln(stderr)
				_, _ = fmt.Fprint(stderr, status.usage)
			}
		}

		return status.code
	}

	if isUnknownCommandError(err) {
		fmt.Fprintf(stderr, "error: %v\n\n", err)
		writeRootUsage(stderr)
		return 2
	}

	fmt.Fprintf(stderr, "error: %v\n", err)
	return 1
}

func isUnknownCommandError(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "unknown command ")
}

func exactArgsError(message string, count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != count {
			return &statusError{code: 1, err: fmt.Errorf("%s", message)}
		}

		return nil
	}
}

func maximumArgsError(message string, count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > count {
			return &statusError{code: 1, err: fmt.Errorf("%s", message)}
		}

		return nil
	}
}

func writeRootUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, rootUsage)
}
