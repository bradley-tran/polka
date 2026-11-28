package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"polka/backend"
)

type commandContext struct {
	stdout io.Writer
	stderr io.Writer
	// projectDir is the -C/--dir value: the directory holding polka.yaml. When
	// empty, commands discover the project from the working directory.
	projectDir string
	exitCode   int
	// err captures errors from void callbacks such as CommandNotFound so
	// runWithContext can route every failure through handleRunError.
	err error
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

// Run executes the Polka CLI with a background context and returns the process exit code.
func Run(stdout, stderr io.Writer, args []string) int {
	return runWithContext(context.Background(), stdout, stderr, args)
}

// runWithContext executes the Polka CLI with the given context and returns the process exit code.
func runWithContext(runCtx context.Context, stdout, stderr io.Writer, args []string) int {
	ctx := &commandContext{stdout: stdout, stderr: stderr}
	root := newRootCommand(ctx)

	// urfave/cli expects argv[0] to be the program name.
	runArgs := append([]string{"polka"}, args...)
	err := root.Run(runCtx, runArgs)
	if err == nil && ctx.err != nil {
		err = ctx.err
	}
	if err != nil {
		return handleRunError(stderr, err)
	}

	return ctx.exitCode
}

func newRootCommand(ctx *commandContext) *ucli.Command {
	root := &ucli.Command{
		Name:                          "polka",
		Usage:                         "Polka manages isolated PHP virtual environments.",
		Writer:                        ctx.stdout,
		ErrWriter:                     ctx.stderr,
		HideVersion:                   true,
		DisableSliceFlagSeparator:     true,
		CustomRootCommandHelpTemplate: rootUsage,
		CustomHelpTemplate:            rootUsage,
		// Suppress urfave/cli's default ExitErrHandler so handleRunError owns all error output.
		ExitErrHandler: func(context.Context, *ucli.Command, error) {},
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "dir",
				Aliases:     []string{"C"},
				Value:       ctx.projectDir,
				Destination: &ctx.projectDir,
				Usage:       "use the project whose polka.yaml is in DIR",
			},
		},
		OnUsageError: func(_ context.Context, _ *ucli.Command, err error, isSubcommand bool) error {
			if isSubcommand {
				return &statusError{code: 1, err: err}
			}

			return &statusError{code: 2, err: err, showUsage: true, usage: rootUsage}
		},
		CommandNotFound: func(_ context.Context, _ *ucli.Command, command string) {
			ctx.err = &statusError{
				code:      2,
				err:       fmt.Errorf("unknown command %q", strings.TrimSpace(command)),
				showUsage: true,
				usage:     rootUsage,
			}
		},
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if cmd.Args().Len() > 0 {
				return &statusError{
					code:      2,
					err:       fmt.Errorf("unknown command %q", strings.TrimSpace(cmd.Args().First())),
					showUsage: true,
					usage:     rootUsage,
				}
			}

			writeRootUsage(ctx.stdout)
			return nil
		},
	}

	root.Commands = []*ucli.Command{
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
		newRouterCommand(ctx),
		newCronCommand(ctx),
		newDispatchCommand(ctx),
	}
	for _, spec := range shortcutSpecs() {
		root.Commands = append(root.Commands, newShortcutCommand(ctx, spec))
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

// configureHelp attaches static usage text for --help, -h, and `polka help <cmd>`.
func configureHelp(cmd *ucli.Command, usage string) {
	cmd.CustomHelpTemplate = usage
}

// configureCommand attaches static usage text and maps flag/usage errors to exit code 1.
func configureCommand(cmd *ucli.Command, usage string) {
	configureHelp(cmd, usage)
	cmd.OnUsageError = func(_ context.Context, _ *ucli.Command, err error, _ bool) error {
		return &statusError{code: 1, err: err}
	}
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

// requireExactArgs returns a statusError with exit code 1 when positional args do not match count.
func requireExactArgs(cmd *ucli.Command, count int, message string) error {
	if cmd.Args().Len() != count {
		return &statusError{code: 1, err: fmt.Errorf("%s", message)}
	}

	return nil
}

// requireMaximumArgs returns a statusError with exit code 1 when positional args exceed count.
func requireMaximumArgs(cmd *ucli.Command, count int, message string) error {
	if cmd.Args().Len() > count {
		return &statusError{code: 1, err: fmt.Errorf("%s", message)}
	}

	return nil
}

func writeRootUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, rootUsage)
}
