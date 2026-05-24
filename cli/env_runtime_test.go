package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"polka/backend"
)

func TestParseEnvironmentFileSupportsCommentsQuotesAndExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	contents := strings.Join([]string{
		"# comment",
		"export APP_ENV=development",
		"QUOTED=\"hello world\"",
		"SINGLE='a # b'",
		"TRAILING=value # comment",
		"EMPTY=",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}

	variables, err := readEnvironmentFile(path)
	if err != nil {
		t.Fatalf("readEnvironmentFile() error = %v", err)
	}
	if len(variables) != 5 {
		t.Fatalf("readEnvironmentFile() length = %d, want 5", len(variables))
	}
	if variables[0] != (environmentVariable{Name: "APP_ENV", Value: "development"}) {
		t.Fatalf("APP_ENV = %#v, want development", variables[0])
	}
	if variables[1] != (environmentVariable{Name: "QUOTED", Value: "hello world"}) {
		t.Fatalf("QUOTED = %#v, want hello world", variables[1])
	}
	if variables[2] != (environmentVariable{Name: "SINGLE", Value: "a # b"}) {
		t.Fatalf("SINGLE = %#v, want literal single-quoted value", variables[2])
	}
	if variables[3] != (environmentVariable{Name: "TRAILING", Value: "value"}) {
		t.Fatalf("TRAILING = %#v, want inline comment stripped", variables[3])
	}
	if variables[4] != (environmentVariable{Name: "EMPTY", Value: ""}) {
		t.Fatalf("EMPTY = %#v, want empty string", variables[4])
	}
}

func TestResolveRuntimeEnvironmentAppliesConfiguredPrecedence(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewProjectStore(projectDir)

	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte("APP_ENV=project\nSHARED=project\nPROJECT_ONLY=1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(project .env) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, "config"), 0o755); err != nil {
		t.Fatalf("MkdirAll(config) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "config", ".env.local"), []byte("APP_ENV=file\nSHARED=file\nFILE_ONLY=1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(env-file) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Current: "demo",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				EnvFile: "config/.env.local",
				EnvVars: map[string]string{
					"APP_ENV":     "config",
					"CONFIG_ONLY": "1",
					"SHARED":      "config",
				},
			},
		},
	}
	configData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	configData = append(configData, '\n')
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	resolved, err := resolveRuntimeEnvironment("linux", []string{"APP_ENV=os", "OS_ONLY=1"}, store)
	if err != nil {
		t.Fatalf("resolveRuntimeEnvironment() error = %v", err)
	}
	if _, value, ok := lookupEnvValue("linux", resolved, "APP_ENV"); !ok || value != "config" {
		t.Fatalf("APP_ENV = %q, want config from env-vars", value)
	}
	if _, value, ok := lookupEnvValue("linux", resolved, "SHARED"); !ok || value != "config" {
		t.Fatalf("SHARED = %q, want config from env-vars", value)
	}
	if _, value, ok := lookupEnvValue("linux", resolved, "PROJECT_ONLY"); !ok || value != "1" {
		t.Fatalf("PROJECT_ONLY = %q, want project .env value", value)
	}
	if _, value, ok := lookupEnvValue("linux", resolved, "FILE_ONLY"); !ok || value != "1" {
		t.Fatalf("FILE_ONLY = %q, want env-file value", value)
	}
	if _, value, ok := lookupEnvValue("linux", resolved, "CONFIG_ONLY"); !ok || value != "1" {
		t.Fatalf("CONFIG_ONLY = %q, want env-vars value", value)
	}
	if _, value, ok := lookupEnvValue("linux", resolved, "OS_ONLY"); !ok || value != "1" {
		t.Fatalf("OS_ONLY = %q, want inherited env value", value)
	}
}

func TestResolveRuntimeEnvironmentUsesWindowsKeyReplacement(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewProjectStore(projectDir)

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Current: "demo",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP: "8.4",
				EnvVars: map[string]string{
					"app_env": "config",
				},
			},
		},
	}
	configData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	configData = append(configData, '\n')
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	resolved, err := resolveRuntimeEnvironment("windows", []string{"App_ENV=os"}, store)
	if err != nil {
		t.Fatalf("resolveRuntimeEnvironment(windows) error = %v", err)
	}
	key, value, ok := lookupEnvValue("windows", resolved, "APP_ENV")
	if !ok {
		t.Fatalf("resolved env = %#v, want APP_ENV entry", resolved)
	}
	if key != "App_ENV" {
		t.Fatalf("APP_ENV key = %q, want original casing preserved", key)
	}
	if value != "config" {
		t.Fatalf("APP_ENV value = %q, want config", value)
	}

	count := 0
	for _, entry := range resolved {
		entryKey, _, ok := strings.Cut(entry, "=")
		if ok && envKeysEqual("windows", entryKey, "APP_ENV") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("resolved env = %#v, want one APP_ENV entry", resolved)
	}
}
