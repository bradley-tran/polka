package cli

import (
	"fmt"
	"io"
	"strings"

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

	state, alreadyStopped, err := stopManagedServe(store, current.Name)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if alreadyStopped {
		fmt.Fprintf(stdout, "Webserver for environment %q is already stopped.\n", current.Name)
	} else {
		fmt.Fprintf(stdout, "Stopped %s for environment %q.\n", serveRuntimeLabel(state.ServerKind), current.Name)
	}

	if current.Database != nil && strings.TrimSpace(current.Database.Engine) != "" {
		resolved := dbResolvedEnvironment{Environment: *current, Database: current.Database}
		databaseState, databaseAlreadyStopped, err := backend.StopManagedDatabase(store, resolved, dbRuntimeHooks())
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		if databaseAlreadyStopped {
			fmt.Fprintf(stdout, "Database for environment %q is already stopped.\n", current.Name)
		} else {
			fmt.Fprintf(stdout, "Stopped %s for environment %q.\n", databaseState.Engine, current.Name)
		}
	}

	if current.Mailpit == nil || strings.TrimSpace(current.Mailpit.Version) == "" {
		return 0
	}
	_, mailpitAlreadyStopped, err := stopManagedMailpit(store, current.Name)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if mailpitAlreadyStopped {
		fmt.Fprintf(stdout, "Mailpit for environment %q is already stopped.\n", current.Name)
		return 0
	}

	fmt.Fprintf(stdout, "Stopped mailpit for environment %q.\n", current.Name)
	return 0
}
