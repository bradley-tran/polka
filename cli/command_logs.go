package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"polka/backend"
)

type logsCommandInput struct {
	Level  string
	Follow bool
}

const logFollowPollInterval = 100 * time.Millisecond

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

			ctx.exitCode = runLogs(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), store, args[0], input.Level, input.Follow)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.Level, "level", "", "filter logs by level (info, error, debug)")
	cmd.Flags().BoolVarP(&input.Follow, "follow", "f", false, "follow log output as files grow")
	configureCommand(cmd, logsUsage)

	return cmd
}

func runLogs(ctx context.Context, stdout, stderr io.Writer, store backend.Store, tool, level string, follow bool) int {
	entries, err := store.ResolveToolLogs(tool, level)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if follow {
		paths := make([]string, 0, len(entries))
		for _, entry := range entries {
			paths = append(paths, entry.Path)
		}

		foundAny, err := followLogFiles(ctx, stdout, paths, logFollowPollInterval)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		if !foundAny {
			fmt.Fprintf(stderr, "error: no log files found for %s%s\n", strings.TrimSpace(tool), logLevelSuffix(level))
			return 1
		}

		return 0
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

// followedLog tracks the last-read position and file identity for one log path.
// The identity lets follow mode restart at the beginning after log rotation.
type followedLog struct {
	path   string
	offset int64
	info   os.FileInfo
}

// followLogFiles prints the current contents of existing logs, then polls all
// declared paths for appended data until the context is canceled.
func followLogFiles(ctx context.Context, stdout io.Writer, paths []string, interval time.Duration) (bool, error) {
	logs := make([]followedLog, 0, len(paths))
	foundAny := false
	for _, path := range paths {
		log := followedLog{path: path}
		exists, err := log.writeAvailable(stdout)
		if err != nil {
			return false, err
		}
		foundAny = foundAny || exists
		logs = append(logs, log)
	}
	if !foundAny {
		return false, nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return true, nil
		case <-ticker.C:
			for index := range logs {
				if _, err := logs[index].writeAvailable(stdout); err != nil {
					return true, err
				}
			}
		}
	}
}

// writeAvailable copies unread bytes from a followed log. Missing files are
// remembered as absent so a file recreated after rotation is read from byte zero.
func (log *followedLog) writeAvailable(stdout io.Writer) (bool, error) {
	file, err := os.Open(log.path)
	if errors.Is(err, os.ErrNotExist) {
		log.offset = 0
		log.info = nil
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open log %s: %w", log.path, err)
	}

	info, statErr := file.Stat()
	if statErr != nil {
		closeErr := file.Close()
		if closeErr != nil {
			return false, fmt.Errorf("stat log %s: %w (close log: %v)", log.path, statErr, closeErr)
		}
		return false, fmt.Errorf("stat log %s: %w", log.path, statErr)
	}

	start := log.offset
	if log.info == nil || !os.SameFile(log.info, info) || info.Size() < start {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		closeErr := file.Close()
		if closeErr != nil {
			return false, fmt.Errorf("seek log %s: %w (close log: %v)", log.path, err, closeErr)
		}
		return false, fmt.Errorf("seek log %s: %w", log.path, err)
	}

	written, copyErr := io.Copy(stdout, file)
	closeErr := file.Close()
	if copyErr != nil {
		return false, fmt.Errorf("read log %s: %w", log.path, copyErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close log %s: %w", log.path, closeErr)
	}

	log.offset = start + written
	log.info = info
	return true, nil
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
