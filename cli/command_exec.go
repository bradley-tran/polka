package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
)

func newExecCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:                "exec <command> [args...]",
		DisableFlagParsing: true,
		Run: func(cmd *cobra.Command, args []string) {
			store, err := ctx.store()
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				ctx.exitCode = 1
				return
			}

			ctx.exitCode = runExec(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, args)
		},
	}
	configureHelp(cmd, execUsage)

	return cmd
}

func runExec(stdout, stderr io.Writer, store backend.Store, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "error: exec requires a command")
		return 2
	}

	environmentName, err := currentShellEnvironmentName(store)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	workingDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "error: resolve current working directory: %v\n", err)
		return 1
	}
	context, err := buildShellSessionContext(runtime.GOOS, store, workingDir, environmentName)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	resolvedEnv, err := buildShellExecutionEnvironment(runtime.GOOS, os.Environ(), store, context)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	target, err := resolveExecTarget(runtime.GOOS, args[0], resolvedEnv)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	exitCode, err := executeTargetWithEnv(stdout, stderr, resolvedEnv, target, args[1:])
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if exitCode == 0 {
		if err := runPostExecComposerHook(store, args[0], args[1:], workingDir, target); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}

	return exitCode
}

func resolveExecTarget(goos, command string, env []string) (string, error) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "", fmt.Errorf("exec command cannot be empty")
	}
	if filepath.IsAbs(trimmed) || hasPathSeparator(trimmed) {
		return trimmed, nil
	}

	_, pathValue, _ := lookupEnvValue(goos, env, "PATH")
	for _, pathEntry := range splitPathList(goos, pathValue) {
		for _, candidate := range execCandidates(goos, pathEntry, trimmed, env) {
			if fileExists(candidate) {
				return candidate, nil
			}
		}
	}

	return "", fmt.Errorf("command %q not found in Polka shell PATH", command)
}

func hasPathSeparator(value string) bool {
	return strings.ContainsRune(value, filepath.Separator) || strings.Contains(value, "/")
}

func splitPathList(goos, pathValue string) []string {
	parts := strings.Split(pathValue, pathListSeparator(goos))
	entries := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			entries = append(entries, trimmed)
		}
	}

	return entries
}

func execCandidates(goos, dir, command string, env []string) []string {
	base := filepath.Join(dir, command)
	if goos != "windows" {
		return []string{base}
	}
	if filepath.Ext(command) != "" {
		return []string{base}
	}

	candidates := make([]string, 0, len(windowsPathExts(env))+1)
	for _, ext := range windowsPathExts(env) {
		candidates = append(candidates, base+ext)
	}

	return append(candidates, base)
}

func windowsPathExts(env []string) []string {
	_, value, ok := lookupEnvValue("windows", env, "PATHEXT")
	if !ok || strings.TrimSpace(value) == "" {
		value = ".COM;.EXE;.BAT;.CMD"
	}

	parts := strings.Split(value, ";")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		result = append(result, strings.ToLower(trimmed))
	}

	return result
}
