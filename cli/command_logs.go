package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
)

type logsCommandInput struct {
	Level string
}

func newLogsCommand(ctx *commandContext) *cobra.Command {
	input := logsCommandInput{}
	cmd := &cobra.Command{
		Use:  "logs <tool>",
		Args: exactArgsError("logs requires exactly one tool name", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			ctx.exitCode = runLogs(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, args[0], input.Level)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.Level, "level", "", "filter logs by level (info, error, debug)")
	configureCommand(cmd, logsUsage)

	return cmd
}

func runLogs(stdout, stderr io.Writer, store backend.Store, tool, level string) int {
	entries, err := store.ResolveToolLogs(tool, level)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	wroteAny := false
	for _, entry := range entries {
		wrote, err := writeLogFile(stdout, entry.Path)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		wroteAny = wroteAny || wrote
	}
	if !wroteAny {
		fmt.Fprintf(stderr, "error: no log files found for %s%s\n", strings.TrimSpace(tool), logLevelSuffix(level))
		return 1
	}

	return 0
}

func writeLogFile(stdout io.Writer, path string) (bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open log %s: %w", path, err)
	}

	_, copyErr := io.Copy(stdout, file)
	closeErr := file.Close()
	if copyErr != nil {
		return false, fmt.Errorf("read log %s: %w", path, copyErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close log %s: %w", path, closeErr)
	}

	return true, nil
}

func logLevelSuffix(level string) string {
	trimmed := strings.TrimSpace(level)
	if trimmed == "" {
		return ""
	}

	return " at level " + trimmed
}
