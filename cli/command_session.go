package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
)

const (
	sessionSubcommandStart = "start"
	sessionSubcommandStop  = "stop"
	sessionStateDirName    = "session"
	polkaSessionIDEnv      = "POLKA_SESSION_ID"
	polkaSessionStateEnv   = "POLKA_SESSION_STATE"
)

type shellScriptKind string

const (
	shellScriptKindPOSIX      shellScriptKind = "posix"
	shellScriptKindPowerShell shellScriptKind = "powershell"
)

var (
	newShellSessionIDFunc = newShellSessionID
)

type shellSessionState struct {
	ID                string `json:"id"`
	Shell             string `json:"shell"`
	EnvironmentName   string `json:"environment"`
	OriginalPathKey   string `json:"path_key"`
	OriginalPathValue string `json:"path_value,omitempty"`
	HasOriginalPath   bool   `json:"has_path"`
	ActivationPath    string `json:"activation_path"`
	DeactivationPath  string `json:"deactivation_path"`
}

func newSessionCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use: "session",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprint(cmd.OutOrStdout(), sessionUsage)
			return nil
		},
	}
	configureHelp(cmd, sessionUsage)
	cmd.AddCommand(newSessionStartCommand(ctx), newSessionStopCommand())

	return cmd
}

func newSessionStartCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:  sessionSubcommandStart,
		Args: exactArgsError("session start does not take arguments", 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}
			if err := runSessionStart(cmd.OutOrStdout(), store, runtime.GOOS, os.Environ()); err != nil {
				return &statusError{code: 1, err: err}
			}

			return nil
		},
	}
	configureCommand(cmd, sessionUsage)

	return cmd
}

func newSessionStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:  sessionSubcommandStop,
		Args: exactArgsError("session stop does not take arguments", 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runSessionStop(cmd.OutOrStdout(), runtime.GOOS, os.Environ()); err != nil {
				return &statusError{code: 1, err: err}
			}

			return nil
		},
	}
	configureCommand(cmd, sessionUsage)

	return cmd
}

func runSessionStart(stdout io.Writer, store backend.Store, goos string, env []string) error {
	if err := ensureNoActiveShellSession(goos, env); err != nil {
		return err
	}
	environmentName, err := currentShellEnvironmentName(store)
	if err != nil {
		return err
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve current working directory: %w", err)
	}
	context, err := buildShellSessionContext(goos, store, workingDir, environmentName)
	if err != nil {
		return err
	}

	pathKey, systemPath, hasPath := lookupEnvValue(goos, env, "PATH")
	if pathKey == "" {
		pathKey = "PATH"
	}
	pathEntries := make([]string, 0, len(context.PathEntries)+1)
	pathEntries = append(pathEntries, context.PathEntries...)
	pathEntries = append(pathEntries, systemPath)
	activatedPath := joinPathList(goos, pathEntries...)

	sessionID, err := newShellSessionIDFunc()
	if err != nil {
		return err
	}
	kind := currentShellScriptKind(goos)
	statePath := shellSessionStatePath(context.RootDir, sessionID)
	activationPath := shellSessionActivationPath(context.RootDir, sessionID, kind)
	deactivationPath := shellSessionDeactivationPath(context.RootDir, sessionID, kind)
	state := shellSessionState{
		ID:                sessionID,
		Shell:             string(kind),
		EnvironmentName:   context.EnvironmentName,
		OriginalPathKey:   pathKey,
		OriginalPathValue: systemPath,
		HasOriginalPath:   hasPath,
		ActivationPath:    activationPath,
		DeactivationPath:  deactivationPath,
	}
	if err := writeShellSessionState(statePath, state); err != nil {
		return err
	}
	startScript := renderShellSessionStartScript(kind, pathKey, activatedPath, sessionID, statePath)
	if err := writeShellSessionScript(activationPath, startScript); err != nil {
		_ = os.Remove(statePath)
		return err
	}
	_, err = fmt.Fprintln(stdout, activationPath)

	return err
}

func runSessionStop(stdout io.Writer, goos string, env []string) error {
	statePath, err := currentShellSessionStatePath(goos, env)
	if err != nil {
		return err
	}
	state, err := loadShellSessionState(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("active Polka session state %q does not exist", statePath)
	}
	if err != nil {
		return err
	}
	kind, err := parseShellScriptKind(state.Shell)
	if err != nil {
		return err
	}
	stopScript := renderShellSessionStopScript(kind, *state, statePath)
	if err := writeShellSessionScript(state.DeactivationPath, stopScript); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, state.DeactivationPath)

	return err
}

func currentShellScriptKind(goos string) shellScriptKind {
	if goos == "windows" {
		return shellScriptKindPowerShell
	}

	return shellScriptKindPOSIX
}

func parseShellScriptKind(value string) (shellScriptKind, error) {
	kind := shellScriptKind(strings.TrimSpace(value))
	switch kind {
	case shellScriptKindPOSIX, shellScriptKindPowerShell:
		return kind, nil
	default:
		return "", fmt.Errorf("unsupported shell session kind %q", value)
	}
}

func ensureNoActiveShellSession(goos string, env []string) error {
	statePath, err := currentShellSessionStatePath(goos, env)
	if err == nil && strings.TrimSpace(statePath) != "" {
		return fmt.Errorf("a Polka session is already active in this shell; run polka session stop and source the returned script")
	}

	return nil
}

func currentShellSessionStatePath(goos string, env []string) (string, error) {
	_, statePath, ok := lookupEnvValue(goos, env, polkaSessionStateEnv)
	if !ok || strings.TrimSpace(statePath) == "" {
		return "", fmt.Errorf("no active Polka session in this shell")
	}

	return strings.TrimSpace(statePath), nil
}

func newShellSessionID() (string, error) {
	var data [8]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("generate shell session id: %w", err)
	}

	return hex.EncodeToString(data[:]), nil
}

func shellSessionStateDirectory(rootDir string) string {
	return filepath.Join(rootDir, shellRuntimeDirName, shellSupportDirName, sessionStateDirName)
}

func shellSessionStatePath(rootDir, sessionID string) string {
	return filepath.Join(shellSessionStateDirectory(rootDir), sessionID+".json")
}

func shellSessionActivationPath(rootDir, sessionID string, kind shellScriptKind) string {
	return filepath.Join(shellSessionStateDirectory(rootDir), "activate-"+sessionID+shellScriptExtension(kind))
}

func shellSessionDeactivationPath(rootDir, sessionID string, kind shellScriptKind) string {
	return filepath.Join(shellSessionStateDirectory(rootDir), "deactivate-"+sessionID+shellScriptExtension(kind))
}

func shellScriptExtension(kind shellScriptKind) string {
	if kind == shellScriptKindPowerShell {
		return ".ps1"
	}

	return ".sh"
}

func loadShellSessionState(path string) (*shellSessionState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state shellSessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode shell session state %s: %w", path, err)
	}

	return &state, nil
}

func writeShellSessionState(path string, state shellSessionState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create shell session state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode shell session state: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write shell session state %s: %w", path, err)
	}

	return nil
}

func writeShellSessionScript(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create shell session script directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		return fmt.Errorf("write shell session script %s: %w", path, err)
	}

	return nil
}

func renderShellSessionStartScript(kind shellScriptKind, pathKey, pathValue, sessionID, statePath string) string {
	if strings.TrimSpace(pathKey) == "" {
		pathKey = "PATH"
	}
	if kind == shellScriptKindPowerShell {
		return strings.Join([]string{
			"$env:" + pathKey + " = " + quotePowerShellLiteral(pathValue),
			"$env:" + polkaSessionIDEnv + " = " + quotePowerShellLiteral(sessionID),
			"$env:" + polkaSessionStateEnv + " = " + quotePowerShellLiteral(statePath),
			"",
		}, "\n")
	}

	return strings.Join([]string{
		"export " + pathKey + "=" + quotePOSIXLiteral(pathValue),
		"export " + polkaSessionIDEnv + "=" + quotePOSIXLiteral(sessionID),
		"export " + polkaSessionStateEnv + "=" + quotePOSIXLiteral(statePath),
		"",
	}, "\n")
}

func renderShellSessionStopScript(kind shellScriptKind, state shellSessionState, statePath string) string {
	pathKey := strings.TrimSpace(state.OriginalPathKey)
	if pathKey == "" {
		pathKey = "PATH"
	}
	if kind == shellScriptKindPowerShell {
		lines := make([]string, 0, 6)
		if state.HasOriginalPath {
			lines = append(lines, "$env:"+pathKey+" = "+quotePowerShellLiteral(state.OriginalPathValue))
		} else {
			lines = append(lines, "Remove-Item Env:"+pathKey+" -ErrorAction SilentlyContinue")
		}
		lines = append(lines,
			"Remove-Item Env:"+polkaSessionIDEnv+" -ErrorAction SilentlyContinue",
			"Remove-Item Env:"+polkaSessionStateEnv+" -ErrorAction SilentlyContinue",
			"Remove-Item -LiteralPath "+quotePowerShellLiteral(statePath)+" -Force -ErrorAction SilentlyContinue",
			"Remove-Item -LiteralPath "+quotePowerShellLiteral(state.ActivationPath)+" -Force -ErrorAction SilentlyContinue",
			"Remove-Item -LiteralPath "+quotePowerShellLiteral(state.DeactivationPath)+" -Force -ErrorAction SilentlyContinue",
			"",
		)

		return strings.Join(lines, "\n")
	}

	lines := make([]string, 0, 6)
	if state.HasOriginalPath {
		lines = append(lines,
			pathKey+"="+quotePOSIXLiteral(state.OriginalPathValue),
			"export "+pathKey,
		)
	} else {
		lines = append(lines, "unset "+pathKey)
	}
	lines = append(lines,
		"unset "+polkaSessionIDEnv,
		"unset "+polkaSessionStateEnv,
		"rm -f -- "+quotePOSIXLiteral(statePath)+" "+quotePOSIXLiteral(state.ActivationPath)+" "+quotePOSIXLiteral(state.DeactivationPath),
		"",
	)

	return strings.Join(lines, "\n")
}

func quotePowerShellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func quotePOSIXLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
