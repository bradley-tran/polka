package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"polka/backend"
)

func newLogsCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <tool>",
		Short: "Print tool logs to stdout",
		Args:  exactArgsError("usage: polka logs <tool>", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runLogs(cmd.OutOrStdout(), store, args[0])
		},
	}
	configureCommand(cmd, logsUsage)
	return cmd
}

func runLogs(stdout io.Writer, store backend.Store, tool string) error {
	environment, err := store.Current()
	if err != nil {
		return err
	}
	if environment == nil {
		environments, err := store.List()
		if err != nil {
			return err
		}
		for _, env := range environments {
			if env.Name == "default" {
				e := env
				environment = &e
				break
			}
		}
		if environment == nil {
			return fmt.Errorf("no active environment selected and default environment not found")
		}
	}

	plugin, ok := store.Plugins.Plugin(tool)
	if !ok {
		return fmt.Errorf("unsupported tool %q", tool)
	}

	version := plugin.Version(*environment)
	if version == "" {
		return fmt.Errorf("%s is not configured for this environment", plugin.ID())
	}

	logCandidates := plugin.Logs(store.RootDir, version, *environment)
	if len(logCandidates) == 0 {
		return fmt.Errorf("no logs available for %s", plugin.ID())
	}

	for _, path := range logCandidates {
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read log file %s: %w", path, err)
		}
		fmt.Fprintf(stdout, "==> %s <==\n", path)
		stdout.Write(content)
	}

	return nil
}
