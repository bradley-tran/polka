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
	"sort"
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
	ID                string                      `json:"id"`
	Shell             string                      `json:"shell"`
	EnvironmentName   string                      `json:"environment"`
	OriginalPathKey   string                      `json:"path_key"`
	OriginalPathValue string                      `json:"path_value,omitempty"`
	HasOriginalPath   bool                        `json:"has_path"`
	Variables         []shellSessionVariableState `json:"variables,omitempty"`
	ActivationPath    string                      `json:"activation_path"`
	DeactivationPath  string                      `json:"deactivation_path"`
}

type shellSessionVariableState struct {
	Key           string `json:"key"`
	Value         string `json:"value"`
	OriginalKey   string `json:"original_key,omitempty"`
	OriginalValue string `json:"original_value,omitempty"`
	HasOriginal   bool   `json:"has_original"`
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
	resolvedEnv, err := resolveRuntimeEnvironment(goos, env, store)
	if err != nil {
		return err
	}

	pathKey, systemPath, hasPath := lookupEnvValue(goos, resolvedEnv, "PATH")
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
	activatedEnv := replaceEnvValue(goos, resolvedEnv, pathKey, activatedPath)
	activatedEnv = replaceEnvValue(goos, activatedEnv, polkaPromptEnvEnv, context.EnvironmentName)
	activatedEnv = replaceEnvValue(goos, activatedEnv, polkaPromptRootEnv, context.PromptRoot)
	activatedEnv = replaceEnvValue(goos, activatedEnv, polkaSessionIDEnv, sessionID)
	activatedEnv = replaceEnvValue(goos, activatedEnv, polkaSessionStateEnv, statePath)
	variables := captureShellSessionVariables(goos, env, activatedEnv)
	state := shellSessionState{
		ID:                sessionID,
		Shell:             string(kind),
		EnvironmentName:   context.EnvironmentName,
		OriginalPathKey:   pathKey,
		OriginalPathValue: systemPath,
		HasOriginalPath:   hasPath,
		Variables:         variables,
		ActivationPath:    activationPath,
		DeactivationPath:  deactivationPath,
	}
	if err := writeShellSessionState(statePath, state); err != nil {
		return err
	}
	startScript := renderShellSessionStartScript(kind, state.Variables)
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

func captureShellSessionVariables(goos string, before, after []string) []shellSessionVariableState {
	changes := make([]shellSessionVariableState, 0)
	seen := map[string]struct{}{}
	for _, entry := range after {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}

		canonical := key
		if goos == "windows" {
			canonical = strings.ToUpper(canonical)
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}

		originalKey, originalValue, hasOriginal := lookupEnvValue(goos, before, key)
		if hasOriginal && originalValue == value {
			continue
		}

		changes = append(changes, shellSessionVariableState{
			Key:           key,
			Value:         value,
			OriginalKey:   originalKey,
			OriginalValue: originalValue,
			HasOriginal:   hasOriginal,
		})
	}

	sort.Slice(changes, func(left, right int) bool {
		leftKey := changes[left].Key
		rightKey := changes[right].Key
		if goos == "windows" {
			leftKey = strings.ToUpper(leftKey)
			rightKey = strings.ToUpper(rightKey)
		}
		if leftKey == rightKey {
			return changes[left].Key < changes[right].Key
		}

		return leftKey < rightKey
	})

	return changes
}

func renderShellSessionStartScript(kind shellScriptKind, variables []shellSessionVariableState) string {
	if kind == shellScriptKindPowerShell {
		lines := make([]string, 0, len(variables)+1)
		for _, variable := range variables {
			lines = append(lines, "$env:"+variable.Key+" = "+quotePowerShellLiteral(variable.Value))
		}
		lines = append(lines, "")

		return strings.Join(lines, "\n")
	}

	lines := make([]string, 0, len(variables)+1)
	for _, variable := range variables {
		lines = append(lines, "export "+variable.Key+"="+quotePOSIXLiteral(variable.Value))
	}
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

func renderShellSessionStopScript(kind shellScriptKind, state shellSessionState, statePath string) string {
	if len(state.Variables) > 0 {
		if kind == shellScriptKindPowerShell {
			lines := make([]string, 0, len(state.Variables)+4)
			for _, variable := range state.Variables {
				restoreKey := strings.TrimSpace(variable.Key)
				if restoreKey == "" {
					continue
				}
				if variable.HasOriginal {
					if strings.TrimSpace(variable.OriginalKey) != "" {
						restoreKey = variable.OriginalKey
					}
					lines = append(lines, "$env:"+restoreKey+" = "+quotePowerShellLiteral(variable.OriginalValue))
				} else {
					lines = append(lines, "Remove-Item Env:"+restoreKey+" -ErrorAction SilentlyContinue")
				}
			}
			lines = append(lines,
				"Remove-Item -LiteralPath "+quotePowerShellLiteral(statePath)+" -Force -ErrorAction SilentlyContinue",
				"Remove-Item -LiteralPath "+quotePowerShellLiteral(state.ActivationPath)+" -Force -ErrorAction SilentlyContinue",
				"Remove-Item -LiteralPath "+quotePowerShellLiteral(state.DeactivationPath)+" -Force -ErrorAction SilentlyContinue",
				"",
			)

			return strings.Join(lines, "\n")
		}

		lines := make([]string, 0, len(state.Variables)*2+4)
		for _, variable := range state.Variables {
			restoreKey := strings.TrimSpace(variable.Key)
			if restoreKey == "" {
				continue
			}
			if variable.HasOriginal {
				if strings.TrimSpace(variable.OriginalKey) != "" {
					restoreKey = variable.OriginalKey
				}
				lines = append(lines,
					restoreKey+"="+quotePOSIXLiteral(variable.OriginalValue),
					"export "+restoreKey,
				)
			} else {
				lines = append(lines, "unset "+restoreKey)
			}
		}
		lines = append(lines,
			"rm -f -- "+quotePOSIXLiteral(statePath)+" "+quotePOSIXLiteral(state.ActivationPath)+" "+quotePOSIXLiteral(state.DeactivationPath),
			"",
		)

		return strings.Join(lines, "\n")
	}

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
