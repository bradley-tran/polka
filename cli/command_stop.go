package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"polka/backend"
)

func newStopCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "stop",
		Args: exactArgsError("stop does not take arguments", 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			ctx.exitCode = runStop(cmd.OutOrStdout(), cmd.ErrOrStderr(), store)
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
