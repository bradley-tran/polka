package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestRunInitUsesDotPolkaByDefault(t *testing.T) {
	projectDir := t.TempDir()
	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})

	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"init"}); code != 0 {
		t.Fatalf("Run(init) code = %d, stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "php.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/php.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "composer.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/composer.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "node.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/node.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "npm.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/npm.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "npx.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/npx.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "nodejs.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/nodejs.cmd) error = %v, want legacy nodejs shim removed", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "mysql.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/mysql.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "mariadb.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/mariadb.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "polka.cmd")); err != nil {
		t.Fatalf("Stat(.polka/bin/polka.cmd) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "polka.yaml")); err != nil {
		t.Fatalf("Stat(polka.yaml) error = %v", err)
	}
	if !strings.Contains(stdout.String(), filepath.Join(projectDir, ".polka")) {
		t.Fatalf("Run(init) stdout = %q, want .polka path", stdout.String())
	}
}

func TestRunInstallUsesCurrentEnvironmentWhenNameOmitted(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install"}); code != 0 {
		t.Fatalf("Run(install current) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "Installing demo environment") {
		t.Fatalf("Run(install current) stdout = %q, want current environment banner", output)
	}
	if !strings.Contains(output, "Installed 'demo' environment") {
		t.Fatalf("Run(install current) stdout = %q, want install summary", output)
	}
}

func TestRunInstallUsesConfiguredNestedRootFromProjectConfig(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, "test-site", ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}

	configData := []byte("version: 1\nroot: test-site/.polka\ncurrent: demo\nenvironments:\n  demo:\n    php: \"8.4\"\n")
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"install"}); code != 0 {
		t.Fatalf("Run(install nested root) code = %d, stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(projectInstalledPHPPath(root, "8.4")); err != nil {
		t.Fatalf("Stat(installed php in nested root) error = %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "Installing demo environment") {
		t.Fatalf("Run(install nested root) stdout = %q, want current environment banner", output)
	}
	if !strings.Contains(output, "Installed 'demo' environment") {
		t.Fatalf("Run(install nested root) stdout = %q, want install summary", output)
	}
}

func TestRunInstallUsesConfiguredNestedRootFromExplicitRoot(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, "test-site", ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}

	configData := []byte("version: 1\nroot: test-site/.polka\ncurrent: demo\nenvironments:\n  demo:\n    php: \"8.4\"\n")
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "install"}); code != 0 {
		t.Fatalf("Run(install nested explicit root) code = %d, stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(projectInstalledPHPPath(root, "8.4")); err != nil {
		t.Fatalf("Stat(installed php in nested root) error = %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "Installing demo environment") {
		t.Fatalf("Run(install nested explicit root) stdout = %q, want current environment banner", output)
	}
	if !strings.Contains(output, "Installed 'demo' environment") {
		t.Fatalf("Run(install nested explicit root) stdout = %q, want install summary", output)
	}
}

func TestRunConfigUsesCurrentEnvironmentWhenNameOmitted(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config demo) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use demo) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "config", "--composer", "2.8"}); code != 0 {
		t.Fatalf("Run(config current) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "Configuring demo environment") {
		t.Fatalf("Run(config current) stdout = %q, want current environment banner", output)
	}
	if !strings.Contains(output, "Configured demo") {
		t.Fatalf("Run(config current) stdout = %q, want configured summary", output)
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	if config.Environments["demo"].Composer != "2.8" {
		t.Fatalf("config = %#v, want composer set on current environment", config)
	}
	if config.Current != "demo" {
		t.Fatalf("config current = %q, want demo", config.Current)
	}
}

func TestRunConfigUsesDefaultEnvironmentWhenCurrentMissing(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config default) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "Configuring default environment") {
		t.Fatalf("Run(config default) stdout = %q, want default environment banner", output)
	}
	if !strings.Contains(output, "Configured default") {
		t.Fatalf("Run(config default) stdout = %q, want configured summary", output)
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	if config.Current != defaultEnvironmentName {
		t.Fatalf("config current = %q, want %q", config.Current, defaultEnvironmentName)
	}
	if config.Environments[defaultEnvironmentName].PHP != "8.4" {
		t.Fatalf("config = %#v, want php configured on default environment", config)
	}
}

func TestRunInstallUsesDefaultEnvironmentWhenCurrentMissing(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", defaultEnvironmentName, "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config default) code = %d, stderr = %q", code, stderr.String())
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config before install) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config before install) error = %v", err)
	}
	config.Current = ""
	updatedConfigData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config before install) error = %v", err)
	}
	updatedConfigData = append(updatedConfigData, '\n')
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), updatedConfigData, 0o644); err != nil {
		t.Fatalf("WriteFile(config before install) error = %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install"}); code != 0 {
		t.Fatalf("Run(install default) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "Installing default environment") {
		t.Fatalf("Run(install default) stdout = %q, want default environment banner", output)
	}
	if !strings.Contains(output, "Installed 'default' environment") {
		t.Fatalf("Run(install default) stdout = %q, want install summary", output)
	}

	configData, err = os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config after install) error = %v", err)
	}
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config after install) error = %v", err)
	}
	if config.Current != defaultEnvironmentName {
		t.Fatalf("config current after install = %q, want %q", config.Current, defaultEnvironmentName)
	}
}

func TestRunNewUsesDefaultVersions(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "new", "demo"}); code != 0 {
		t.Fatalf("Run(new) code = %d, stderr = %q", code, stderr.String())
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	if config.Environments["demo"].PHP != "8.4" || config.Environments["demo"].Composer != "2.8" || config.Environments["demo"].NodeJS != "24" {
		t.Fatalf("config = %#v, want default versions for demo", config)
	}
	if !strings.Contains(stdout.String(), "nodejs=24") {
		t.Fatalf("Run(new) stdout = %q, want default nodejs summary", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Created demo") {
		t.Fatalf("Run(new) stdout = %q, want created summary", stdout.String())
	}
}

func TestRunConfigPersistsDatabaseSettings(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--db-engine", "mysql", "--db-version", "8.0", "--db-port", "3306"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	stored := config.Environments["demo"].Database
	if stored == nil {
		t.Fatalf("config = %#v, want database config for demo", config)
	}
	if stored.Engine != "mysql" || stored.Version != "8.0" || stored.Port != 3306 {
		t.Fatalf("database = %#v, want mysql 8.0 on port 3306", stored)
	}
	if !strings.Contains(stdout.String(), "db=mysql:8.0@3306") {
		t.Fatalf("Run(config) stdout = %q, want database summary", stdout.String())
	}
}

func TestRunConfigPersistsNodeJSSetting(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--nodejs", "24"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	if config.Environments["demo"].NodeJS != "24" {
		t.Fatalf("config = %#v, want nodejs configured for demo", config)
	}
	if !strings.Contains(stdout.String(), "nodejs=24") {
		t.Fatalf("Run(config) stdout = %q, want nodejs summary", stdout.String())
	}
}

func TestRunStatusShowsToolsEachOnOwnLine(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Current: "demo",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:      "8.4",
				Composer: "2.8",
				NodeJS:   "24",
				Nginx:    "1.30",
				Database: &testDatabaseConfig{Engine: "mysql", Version: "8.0", Port: 3306},
				Server:   &testServerConfig{Hostname: "localhost", Port: 8080},
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

	if code := Run(stdout, stderr, []string{"--root", root, "status"}); code != 0 {
		t.Fatalf("Run(status) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	for _, expected := range []string{
		"environment demo\n",
		"php 8.4\n",
		"composer 2.8\n",
		"nodejs 24\n",
		"nginx 1.30\n",
		"database mysql:8.0@3306\n",
		"server http://localhost:8080\n",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("Run(status) stdout = %q, want %q", output, expected)
		}
	}
}

func TestRunStatusUsesDefaultServerAddress(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "status"}); code != 0 {
		t.Fatalf("Run(status) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "server http://localhost:8000\n") {
		t.Fatalf("Run(status) stdout = %q, want default server address", output)
	}
	if !strings.Contains(output, "nodejs unset\n") {
		t.Fatalf("Run(status) stdout = %q, want nodejs unset line", output)
	}
	if !strings.Contains(output, "nginx unset\n") {
		t.Fatalf("Run(status) stdout = %q, want nginx unset line", output)
	}
}

func TestRunInstallAppliesPHPExtensionsFromConfigFile(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, "php", "8.4", "ext"), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache ext) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	configPath := filepath.Join(projectDir, "polka.yaml")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	environment := config.Environments["demo"]
	environment.PHPExtensions = map[string]bool{"openssl": true, "xdebug": false}
	config.Environments["demo"] = environment
	updatedConfig, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	updatedConfig = append(updatedConfig, '\n')
	if err := os.WriteFile(configPath, updatedConfig, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	phpIniData, err := os.ReadFile(projectInstalledPHPConfigPath(root, "8.4"))
	if err != nil {
		t.Fatalf("ReadFile(installed php.ini) error = %v", err)
	}
	phpIni := string(phpIniData)
	if !strings.Contains(phpIni, "extension=openssl") {
		t.Fatalf("php.ini = %q, want enabled openssl extension", phpIni)
	}
	if !strings.Contains(phpIni, ";extension=xdebug") {
		t.Fatalf("php.ini = %q, want disabled xdebug extension", phpIni)
	}
	if !strings.Contains(stdout.String(), "Installed 'demo' environment") {
		t.Fatalf("Run(install) stdout = %q, want install summary", stdout.String())
	}
}

func TestRunInstallEnablesComposerPHPExtensionsByDefault(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	fakeComposer := cachedComposerPath(cacheDir, "2.8")
	if err := os.MkdirAll(filepath.Dir(fakeComposer), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache composer) error = %v", err)
	}
	if err := os.WriteFile(fakeComposer, []byte("composer\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache composer) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, "php", "8.4", "ext"), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache ext) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4", "--composer", "2.8"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	phpIniData, err := os.ReadFile(projectInstalledPHPConfigPath(root, "8.4"))
	if err != nil {
		t.Fatalf("ReadFile(installed php.ini) error = %v", err)
	}
	phpIni := string(phpIniData)
	if !strings.Contains(phpIni, "extension=openssl") {
		t.Fatalf("php.ini = %q, want enabled openssl extension", phpIni)
	}
	if !strings.Contains(phpIni, "extension=zip") {
		t.Fatalf("php.ini = %q, want enabled zip extension", phpIni)
	}
}
