package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
)

func newDispatchCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:                "dispatch <tool> [args...]",
		Hidden:             true,
		DisableFlagParsing: true,
		Run: func(cmd *cobra.Command, args []string) {
			store, err := ctx.store()
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				ctx.exitCode = 1
				return
			}

			ctx.exitCode = runDispatch(cmd.OutOrStdout(), cmd.ErrOrStderr(), args, store)
		},
	}

	return cmd
}

func runDispatch(stdout, stderr io.Writer, args []string, store backend.Store) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "error: dispatch requires a tool name")
		return 2
	}

	tool := strings.TrimSpace(args[0])
	target, err := store.ResolveTool(tool)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	dispatchArgs := args[1:]
	if strings.EqualFold(tool, "composer") && strings.HasSuffix(strings.ToLower(target), ".phar") {
		phpTarget, resolveErr := store.ResolveTool("php")
		if resolveErr != nil {
			fmt.Fprintf(stderr, "error: resolve php for composer: %v\n", resolveErr)
			return 1
		}

		dispatchArgs = append([]string{target}, dispatchArgs...)
		target = phpTarget
	}

	exitCode, err := executeTarget(stdout, stderr, target, dispatchArgs)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	return exitCode
}

func executeTarget(stdout, stderr io.Writer, target string, args []string) (int, error) {
	command, err := prepareCommand(target, args)
	if err != nil {
		return 0, err
	}
	command.Stdout = stdout
	command.Stderr = stderr
	command.Stdin = os.Stdin

	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return exitError.ExitCode(), nil
		}

		return 0, fmt.Errorf("run %s: %w", target, err)
	}

	return 0, nil
}

func prepareCommand(target string, args []string) (*exec.Cmd, error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return nil, fmt.Errorf("dispatch target cannot be empty")
	}

	if runtime.GOOS == "windows" {
		extension := strings.ToLower(filepath.Ext(trimmed))
		if extension == ".cmd" || extension == ".bat" {
			commandArgs := append([]string{"/c", trimmed}, args...)
			return exec.Command("cmd.exe", commandArgs...), nil
		}
	}

	return exec.Command(trimmed, args...), nil
}
