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
	"polka/service"
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
	env, err := resolveRuntimeEnvironment(runtime.GOOS, os.Environ(), store)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	tool := strings.TrimSpace(args[0])
	target, err := store.ResolveTool(tool)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	workingDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "error: resolve current working directory: %v\n", err)
		return 1
	}

	composerArgs := append([]string(nil), args[1:]...)
	dispatchArgs := args[1:]
	usesManagedPHP := strings.EqualFold(tool, "php") || strings.EqualFold(tool, "frankenphp")
	if dispatchPHARRequiresManagedPHP(tool, target) {
		phpTarget, resolveErr := store.ResolveTool("php")
		if resolveErr != nil {
			fmt.Fprintf(stderr, "error: resolve php for %s: %v\n", strings.ToLower(tool), resolveErr)
			return 1
		}

		dispatchArgs = append([]string{target}, dispatchArgs...)
		target = phpTarget
		usesManagedPHP = true
	}
	if usesManagedPHP {
		env, err = applyManagedPHPRuntimeConfig(runtime.GOOS, env, target)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}
	if strings.HasPrefix(strings.ToLower(tool), "rabbitmq") {
		current, currentErr := store.Current()
		if currentErr != nil || current == nil {
			if currentErr == nil {
				currentErr = fmt.Errorf("no active environment")
			}
			fmt.Fprintf(stderr, "error: prepare rabbitmq runtime: %v\n", currentErr)
			return 1
		}
		env, err = service.PrepareRabbitMQDispatchEnvironment(managedServiceContext(store, *current, stderr))
		if err != nil {
			fmt.Fprintf(stderr, "error: prepare rabbitmq runtime: %v\n", err)
			return 1
		}
	}

	exitCode, err := executeTargetWithComposerScriptSupport(
		runtime.GOOS,
		strings.EqualFold(tool, "composer"),
		stdout,
		stderr,
		env,
		workingDir,
		target,
		dispatchArgs,
	)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if shouldRunPostComposerHook(exitCode) {
		if err := runPostComposerHook(store, tool, composerArgs, workingDir); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}

	return exitCode
}

func dispatchPHARRequiresManagedPHP(tool, target string) bool {
	if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(target)), ".phar") {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "composer":
		return true
	default:
		return false
	}
}

func executeTarget(stdout, stderr io.Writer, target string, args []string) (int, error) {
	return executeTargetWithIO(stdout, stderr, nil, nil, target, args)
}

func executeTargetWithEnv(stdout, stderr io.Writer, env []string, target string, args []string) (int, error) {
	return executeTargetWithIO(stdout, stderr, nil, env, target, args)
}

func executeTargetWithIO(stdout, stderr io.Writer, stdin io.Reader, env []string, target string, args []string) (int, error) {
	command, err := prepareCommand(target, args)
	if err != nil {
		return 0, err
	}
	command.Stdout = stdout
	command.Stderr = stderr
	if stdin != nil {
		command.Stdin = stdin
	} else {
		command.Stdin = os.Stdin
	}
	if env != nil {
		command.Env = env
	}

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
