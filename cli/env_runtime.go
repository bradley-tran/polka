package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"polka/backend"
	"polka/plugins"
)

const defaultProjectEnvFileName = ".env"

type environmentVariable struct {
	Name  string
	Value string
}

func resolveRuntimeEnvironment(goos string, inherited []string, store backend.Store) ([]string, error) {
	resolved := append([]string(nil), inherited...)

	autoEnvPath := filepath.Join(store.ProjectDir, defaultProjectEnvFileName)
	autoEnvExists, err := regularFileExists(autoEnvPath)
	if err != nil {
		return nil, fmt.Errorf("stat project env file %s: %w", autoEnvPath, err)
	}
	if autoEnvExists {
		resolved, err = overlayEnvironmentFile(goos, resolved, autoEnvPath)
		if err != nil {
			return nil, fmt.Errorf("load project env file %s: %w", autoEnvPath, err)
		}
	}

	current, err := store.Current()
	if err != nil {
		return nil, err
	}
	if current == nil {
		return resolved, nil
	}

	if strings.TrimSpace(current.EnvFile) != "" {
		configuredPath := resolveConfiguredEnvironmentFilePath(store.ProjectDir, current.EnvFile)
		resolved, err = overlayEnvironmentFile(goos, resolved, configuredPath)
		if err != nil {
			return nil, fmt.Errorf("load configured env-file %q for environment %q: %w", current.EnvFile, current.Name, err)
		}
	}

	frameworkValues, err := frameworkRuntimeEnvironmentVariables(store, *current)
	if err != nil {
		return nil, err
	}
	resolved, err = overlayEnvironmentVariables(goos, resolved, frameworkValues)
	if err != nil {
		return nil, fmt.Errorf("load framework env-vars for environment %q: %w", current.Name, err)
	}

	resolved, err = overlayEnvironmentVariables(goos, resolved, current.EnvVars)
	if err != nil {
		return nil, fmt.Errorf("load configured env-vars for environment %q: %w", current.Name, err)
	}

	return resolved, nil
}

func frameworkRuntimeEnvironmentVariables(store backend.Store, environment backend.Environment) (map[string]string, error) {
	if strings.TrimSpace(environment.Framework) == "" {
		return nil, nil
	}

	plugin, ok := store.FrameworkPlugin(environment.Framework)
	if !ok {
		return nil, fmt.Errorf("unsupported framework %q; supported frameworks: %s", environment.Framework, strings.Join(store.SupportedFrameworks(), ", "))
	}
	credentials, err := frameworkDatabaseCredentials(store.RootDir, environment)
	if err != nil {
		return nil, err
	}

	return plugin.RuntimeEnv(plugins.RuntimeEnvContext{
		Environment: environment,
		Database:    credentials,
	}), nil
}

func frameworkDatabaseCredentials(rootDir string, environment backend.Environment) (*plugins.DatabaseCredentials, error) {
	if environment.Database == nil || strings.TrimSpace(environment.Database.Engine) == "" {
		return nil, nil
	}

	credentials, err := backend.LoadManagedDatabaseCredentials(backend.DatabaseCredentialStatePath(rootDir, environment.Name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return frameworkDatabaseCredentialsFromManaged(environment, credentials), nil
}

func frameworkDatabaseCredentialsFromManaged(environment backend.Environment, credentials backend.ManagedDatabaseCredentials) *plugins.DatabaseCredentials {
	port := credentials.Port
	if port == 0 {
		port = backend.EffectiveDatabasePort(environment.Database)
	}
	databaseName := strings.TrimSpace(credentials.DatabaseName)
	if databaseName == "" {
		databaseName = environment.Name
	}

	return &plugins.DatabaseCredentials{
		Host:         backend.DatabaseListenHost,
		Port:         port,
		DatabaseName: databaseName,
		User:         credentials.User,
		Password:     credentials.Password,
	}
}

func resolveConfiguredEnvironmentFilePath(projectDir, configuredPath string) string {
	trimmed := strings.TrimSpace(configuredPath)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}

	return filepath.Clean(filepath.Join(projectDir, filepath.FromSlash(trimmed)))
}

func overlayEnvironmentFile(goos string, env []string, path string) ([]string, error) {
	variables, err := readEnvironmentFile(path)
	if err != nil {
		return nil, err
	}

	updated := append([]string(nil), env...)
	for _, variable := range variables {
		updated = replaceEnvValue(goos, updated, variable.Name, variable.Value)
	}

	return updated, nil
}

func overlayEnvironmentVariables(goos string, env []string, values map[string]string) ([]string, error) {
	if len(values) == 0 {
		return env, nil
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	updated := append([]string(nil), env...)
	for _, rawKey := range keys {
		key := strings.TrimSpace(rawKey)
		if err := validateEnvironmentVariableName(key); err != nil {
			return nil, err
		}

		updated = replaceEnvValue(goos, updated, key, values[rawKey])
	}

	return updated, nil
}

func readEnvironmentFile(path string) ([]environmentVariable, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	variables := make([]environmentVariable, 0)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "export ") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
		}

		key, rawValue, ok := strings.Cut(trimmed, "=")
		if !ok {
			return nil, fmt.Errorf("parse %s:%d: expected KEY=VALUE", path, lineNumber)
		}

		key = strings.TrimSpace(key)
		if err := validateEnvironmentVariableName(key); err != nil {
			return nil, fmt.Errorf("parse %s:%d: %w", path, lineNumber, err)
		}

		value, err := parseEnvironmentVariableValue(rawValue)
		if err != nil {
			return nil, fmt.Errorf("parse %s:%d: %w", path, lineNumber, err)
		}

		variables = append(variables, environmentVariable{Name: key, Value: value})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}

	return variables, nil
}

func parseEnvironmentVariableValue(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}

	if strings.HasPrefix(trimmed, "\"") {
		return parseDoubleQuotedEnvironmentValue(trimmed)
	}
	if strings.HasPrefix(trimmed, "'") {
		return parseSingleQuotedEnvironmentValue(trimmed)
	}

	return stripEnvironmentInlineComment(trimmed), nil
}

func parseDoubleQuotedEnvironmentValue(value string) (string, error) {
	escaped := false
	for index := 1; index < len(value); index++ {
		current := value[index]
		if !escaped && current == '\\' {
			escaped = true
			continue
		}
		if !escaped && current == '"' {
			remainder := strings.TrimSpace(value[index+1:])
			if remainder != "" && !strings.HasPrefix(remainder, "#") {
				return "", fmt.Errorf("unexpected trailing content %q", remainder)
			}

			decoded, err := strconv.Unquote(value[:index+1])
			if err != nil {
				return "", fmt.Errorf("decode quoted value: %w", err)
			}

			return decoded, nil
		}
		escaped = false
	}

	return "", fmt.Errorf("unterminated quoted value")
}

func parseSingleQuotedEnvironmentValue(value string) (string, error) {
	for index := 1; index < len(value); index++ {
		if value[index] != '\'' {
			continue
		}

		remainder := strings.TrimSpace(value[index+1:])
		if remainder != "" && !strings.HasPrefix(remainder, "#") {
			return "", fmt.Errorf("unexpected trailing content %q", remainder)
		}

		return value[1:index], nil
	}

	return "", fmt.Errorf("unterminated quoted value")
}

func stripEnvironmentInlineComment(value string) string {
	for index, current := range value {
		if current != '#' {
			continue
		}
		if index == 0 {
			return ""
		}
		if unicode.IsSpace(rune(value[index-1])) {
			return strings.TrimSpace(value[:index])
		}
	}

	return strings.TrimSpace(value)
}

func validateEnvironmentVariableName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("environment variable name cannot be empty")
	}
	if strings.Contains(trimmed, "=") {
		return fmt.Errorf("environment variable name %q cannot contain =", name)
	}
	if strings.ContainsRune(trimmed, 0) {
		return fmt.Errorf("environment variable name %q cannot contain NUL", name)
	}

	return nil
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return false, fmt.Errorf("path is a directory")
		}
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	return false, err
}
