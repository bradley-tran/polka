package cli

import (
	"context"
	"fmt"
	"io"

	ucli "github.com/urfave/cli/v3"

	"polka/backend"
)

func newStopCommand(ctx *commandContext) *ucli.Command {
	cmd := &ucli.Command{
		Name: "stop",
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if err := requireExactArgs(cmd, 0, "stop does not take arguments"); err != nil {
				return err
			}
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			ctx.exitCode = runStop(ctx.stdout, ctx.stderr, store)
			return nil
		},
	}
	configureCommand(cmd, stopUsage)

	return cmd
}

func runStop(stdout, stderr io.Writer, store backend.Store) int {
	current, err := store.Current()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if current == nil {
		fmt.Fprintln(stderr, "error: no active environment selected")
		return 1
	}

	if err := defaultCLIHookRegistry().Stop(stopHookContext{
		Stdout:      stdout,
		Stderr:      stderr,
		Store:       store,
		Environment: *current,
	}); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	return 0
}
