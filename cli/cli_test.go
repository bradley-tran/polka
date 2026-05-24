package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"polka/backend"
)

type testConfigFile struct {
	Version      int                              `yaml:"version,omitempty"`
	Root         string                           `yaml:"root,omitempty"`
	Current      string                           `yaml:"current,omitempty"`
	Environments map[string]testEnvironmentConfig `yaml:"environments"`
}

type testEnvironmentConfig struct {
	PHP           string              `yaml:"php"`
	Composer      string              `yaml:"composer"`
	Nginx         string              `yaml:"nginx,omitempty"`
	Database      *testDatabaseConfig `yaml:"database,omitempty"`
	PHPExtensions map[string]bool     `yaml:"php-extensions,omitempty"`
	Server        *testServerConfig   `yaml:"server,omitempty"`
}

type testDatabaseConfig struct {
	Engine  string `yaml:"engine,omitempty"`
	Version string `yaml:"version,omitempty"`
	Port    int    `yaml:"port,omitempty"`
}

type testServerConfig struct {
	Hostname string `yaml:"hostname,omitempty"`
	Port     int    `yaml:"port,omitempty"`
}

func TestRunConfigSetsVersionLabelsAndDispatchesPhp(t *testing.T) {
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

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4", "--composer", "2.8"}); code != 0 {
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
	if config.Environments["demo"].PHP != "8.4" || config.Environments["demo"].Composer != "2.8" {
		t.Fatalf("config = %#v, want version labels for demo", config)
	}
	if strings.Contains(string(configData), fakePHP) {
		t.Fatalf("config contents = %q, want versions rather than paths", string(configData))
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(projectInstalledPHPPath(root, "8.4")); err != nil {
		t.Fatalf("Stat(installed php) error = %v", err)
	}
	installOutput := stdout.String()
	expectedProgress := []string{
		"[1/2] php 8.4: using cache",
		"[1/2] php 8.4: installing",
		"[1/2] php 8.4: configuring",
		"[1/2] php 8.4: installed",
		"[2/2] composer 2.8: using cache",
		"[2/2] composer 2.8: installing",
		"[2/2] composer 2.8: installed",
		"Installed 'demo' environment",
	}
	previousIndex := -1
	for _, expected := range expectedProgress {
		currentIndex := strings.Index(installOutput, expected)
		if currentIndex < 0 {
			t.Fatalf("Run(install) stdout = %q, want %q", installOutput, expected)
		}
		if currentIndex < previousIndex {
			t.Fatalf("Run(install) stdout = %q, want ordered progress lines %#v", installOutput, expectedProgress)
		}
		previousIndex = currentIndex
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "dispatch", "php", "-v", "--ini"}); code != 0 {
		t.Fatalf("Run(dispatch) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "fake-php -v --ini") {
		t.Fatalf("Run(dispatch) output = %q, want forwarded arguments", output)
	}
}

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
	if config.Environments["demo"].PHP != "8.4" || config.Environments["demo"].Composer != "2.8" {
		t.Fatalf("config = %#v, want default versions for demo", config)
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

func TestRunDBDispatchesConfiguredDatabaseTool(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakeMySQL := cachedDatabasePath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQL), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysql) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQL, fakeDatabaseScript("mysql"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysql) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--db-engine", "mysql", "--db-version", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "--version"}); code != 0 {
		t.Fatalf("Run(db) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "fake-mysql") || !strings.Contains(output, "--version") || !strings.Contains(output, "--defaults-extra-file=") || !strings.Contains(output, "--protocol=tcp") || !strings.Contains(output, "--host=127.0.0.1") || !strings.Contains(output, "--port=3306") {
		t.Fatalf("Run(db) output = %q, want managed credential defaults and TCP connection arguments", output)
	}
}

func TestRunDBPreservesExplicitConnectionArguments(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakeMySQL := cachedDatabasePath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQL), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysql) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQL, fakeDatabaseScript("mysql"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysql) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--db-engine", "mysql", "--db-version", "8.4", "--db-port", "3307"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "--host=db.internal", "--port=4406", "--protocol=tcp", "--version"}); code != 0 {
		t.Fatalf("Run(db explicit connection) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "--defaults-extra-file=") || !strings.Contains(output, "--host=db.internal") || !strings.Contains(output, "--port=4406") || !strings.Contains(output, "--protocol=tcp") {
		t.Fatalf("Run(db explicit connection) output = %q, want explicit connection arguments forwarded with managed credential defaults", output)
	}
	if strings.Contains(output, "--host=127.0.0.1") || strings.Contains(output, "--port=3307") {
		t.Fatalf("Run(db explicit connection) output = %q, want injected defaults suppressed", output)
	}
}

func TestRunDBClientSubcommandDispatchesReservedWord(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakeMySQL := cachedDatabasePath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQL), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysql) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQL, fakeDatabaseScript("mysql"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysql) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--db-engine", "mysql", "--db-version", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "client", "status"}); code != 0 {
		t.Fatalf("Run(db client) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "fake-mysql") || !strings.Contains(output, " status") || !strings.Contains(output, "--defaults-extra-file=") || !strings.Contains(output, "--host=127.0.0.1") || !strings.Contains(output, "--port=3306") {
		t.Fatalf("Run(db client) output = %q, want reserved word forwarded with managed credential defaults", output)
	}
}

func TestRunDBLifecycleSubcommandsManageState(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakeMySQL := cachedDatabasePath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQL), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysql) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQL, fakeDatabaseScript("mysql"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysql) error = %v", err)
	}
	fakeMySQLServer := cachedDatabaseServerPath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQLServer), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysqld) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQLServer, fakeDatabaseScript("mysqld"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysqld) error = %v", err)
	}
	fakeMySQLAdmin := cachedDatabaseAdminPath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQLAdmin), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysqladmin) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQLAdmin, fakeDatabaseScript("mysqladmin"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysqladmin) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--db-engine", "mysql", "--db-version", "8.4", "--db-port", "3307"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	oldInitialize := initializeDatabaseServerFunc
	oldStart := startDatabaseServerFunc
	oldStop := stopDatabaseServerFunc
	oldPing := pingDatabaseAddressFunc
	oldNow := dbNowFunc
	t.Cleanup(func() {
		initializeDatabaseServerFunc = oldInitialize
		startDatabaseServerFunc = oldStart
		stopDatabaseServerFunc = oldStop
		pingDatabaseAddressFunc = oldPing
		dbNowFunc = oldNow
	})

	running := map[string]bool{}
	initialized := 0
	started := 0
	stopped := 0
	var startedSpec dbServerSpec
	var stoppedState dbRuntimeState

	initializeDatabaseServerFunc = func(spec dbServerSpec) error {
		initialized++
		if err := os.MkdirAll(spec.DataDir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(spec.DataDir, "initialized.txt"), []byte("ok\n"), 0o644)
	}
	startDatabaseServerFunc = func(spec dbServerSpec) (dbStartResult, error) {
		started++
		startedSpec = spec
		running[databaseAddress(spec.Port)] = true
		return dbStartResult{PID: 4242}, nil
	}
	stopDatabaseServerFunc = func(state dbRuntimeState) error {
		stopped++
		stoppedState = state
		running[databaseAddress(state.Port)] = false
		return nil
	}
	pingDatabaseAddressFunc = func(address string) bool {
		return running[address]
	}
	dbNowFunc = func() time.Time {
		return time.Date(2026, time.May, 23, 12, 0, 0, 0, time.UTC)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "start"}); code != 0 {
		t.Fatalf("Run(db start) code = %d, stderr = %q", code, stderr.String())
	}
	if initialized != 1 || started != 1 {
		t.Fatalf("initialize/start counts = %d/%d, want 1/1", initialized, started)
	}
	if startedSpec.Port != 3307 {
		t.Fatalf("started port = %d, want 3307", startedSpec.Port)
	}
	if startedSpec.AdminTarget == "" || startedSpec.DefaultsFile == "" || startedSpec.BootstrapSQLFile == "" {
		t.Fatalf("started spec = %#v, want admin target and credential/bootstrap assets", startedSpec)
	}
	if !strings.Contains(stdout.String(), "Started mysql") {
		t.Fatalf("Run(db start) stdout = %q, want start summary", stdout.String())
	}

	state, err := loadDatabaseState(databaseStatePath(root, "demo"))
	if err != nil {
		t.Fatalf("loadDatabaseState() error = %v", err)
	}
	if state.PID != 4242 || state.Port != 3307 || state.Engine != "mysql" {
		t.Fatalf("database state = %#v, want mysql pid 4242 on port 3307", state)
	}
	if state.AdminTarget == "" || state.DefaultsFile == "" {
		t.Fatalf("database state = %#v, want native shutdown fields", state)
	}

	credentials, err := loadDatabaseCredentials(databaseCredentialStatePath(root, "demo"))
	if err != nil {
		t.Fatalf("loadDatabaseCredentials() error = %v", err)
	}
	if credentials.User != dbManagedUserName || credentials.Password == "" || credentials.Port != 3307 {
		t.Fatalf("credentials = %#v, want managed user with generated password on port 3307", credentials)
	}
	defaultsData, err := os.ReadFile(databaseDefaultsFilePath(root, "demo"))
	if err != nil {
		t.Fatalf("ReadFile(defaults) error = %v", err)
	}
	if !strings.Contains(string(defaultsData), "user="+dbManagedUserName) || !strings.Contains(string(defaultsData), "protocol=tcp") {
		t.Fatalf("defaults file = %q, want managed client config", string(defaultsData))
	}
	bootstrapData, err := os.ReadFile(databaseBootstrapSQLPath(root, "demo"))
	if err != nil {
		t.Fatalf("ReadFile(bootstrap sql) error = %v", err)
	}
	if !strings.Contains(string(bootstrapData), "CREATE USER IF NOT EXISTS '"+dbManagedUserName+"'") || !strings.Contains(string(bootstrapData), credentials.Password) {
		t.Fatalf("bootstrap sql = %q, want managed bootstrap statements", string(bootstrapData))
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "status"}); code != 0 {
		t.Fatalf("Run(db status) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "is running") || !strings.Contains(stdout.String(), "3307") {
		t.Fatalf("Run(db status) stdout = %q, want running status on configured port", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "stop"}); code != 0 {
		t.Fatalf("Run(db stop) code = %d, stderr = %q", code, stderr.String())
	}
	if stopped != 1 {
		t.Fatalf("stop count = %d, want 1", stopped)
	}
	if stoppedState.AdminTarget == "" || stoppedState.DefaultsFile == "" {
		t.Fatalf("stopped state = %#v, want native shutdown target and defaults file", stoppedState)
	}
	if !strings.Contains(stdout.String(), "Stopped mysql") {
		t.Fatalf("Run(db stop) stdout = %q, want stop summary", stdout.String())
	}
	if _, err := os.Stat(databaseStatePath(root, "demo")); !os.IsNotExist(err) {
		t.Fatalf("Stat(database state) error = %v, want not exists", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "status"}); code != 0 {
		t.Fatalf("Run(db status after stop) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "is stopped") || !strings.Contains(stdout.String(), "3307") {
		t.Fatalf("Run(db status after stop) stdout = %q, want stopped status on configured port", stdout.String())
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

func TestRunServeUsesCurrentServerConfig(t *testing.T) {
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
	docroot := filepath.Join(projectDir, "named-project", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
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
	environment.Server = &testServerConfig{Hostname: "localhost", Port: 8080}
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
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "serve", filepath.Join("named-project", "public")}); code != 0 {
		t.Fatalf("Run(serve) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "-S localhost:8080") {
		t.Fatalf("Run(serve) output = %q, want configured server address", output)
	}
	if !strings.Contains(output, "-t "+docroot) {
		t.Fatalf("Run(serve) output = %q, want resolved docroot", output)
	}
}

func TestRunServeAllowsServerOverride(t *testing.T) {
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
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
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
	environment.Server = &testServerConfig{Hostname: "localhost", Port: 8080}
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
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "serve", "--server", "127.0.0.1:9001", filepath.Join("site", "public")}); code != 0 {
		t.Fatalf("Run(serve) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "-S 127.0.0.1:9001") {
		t.Fatalf("Run(serve) output = %q, want overridden server address", output)
	}
	if strings.Contains(output, "-S localhost:8080") {
		t.Fatalf("Run(serve) output = %q, want CLI override to win", output)
	}
	if !strings.Contains(output, "-t "+docroot) {
		t.Fatalf("Run(serve) output = %q, want resolved docroot", output)
	}
}

func TestRunServeUsesNginxWhenConfigured(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Current: "demo",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:    "8.4",
				Nginx:  "1.30",
				Server: &testServerConfig{Hostname: "localhost", Port: 8080},
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

	oldPHPServe := runPHPRuntimeServeFunc
	oldNginxServe := runNginxServeFunc
	t.Cleanup(func() {
		runPHPRuntimeServeFunc = oldPHPServe
		runNginxServeFunc = oldNginxServe
	})

	phpCalls := 0
	nginxCalls := 0
	gotAddress := ""
	gotDocroot := ""
	runPHPRuntimeServeFunc = func(stdout, stderr io.Writer, store backend.Store, serverAddress, docroot string) (int, error) {
		phpCalls++
		return 0, nil
	}
	runNginxServeFunc = func(stdout, stderr io.Writer, store backend.Store, environment backend.Environment, serverAddress, docroot string) (int, error) {
		nginxCalls++
		gotAddress = serverAddress
		gotDocroot = docroot
		_, _ = io.WriteString(stdout, "fake-nginx "+serverAddress+" -t "+docroot+"\n")
		return 0, nil
	}

	if code := Run(stdout, stderr, []string{"--root", root, "serve", filepath.Join("site", "public")}); code != 0 {
		t.Fatalf("Run(serve with nginx) code = %d, stderr = %q", code, stderr.String())
	}
	if phpCalls != 0 {
		t.Fatalf("php serve calls = %d, want nginx branch only", phpCalls)
	}
	if nginxCalls != 1 {
		t.Fatalf("nginx serve calls = %d, want 1", nginxCalls)
	}
	if gotAddress != "localhost:8080" {
		t.Fatalf("nginx serve address = %q, want %q", gotAddress, "localhost:8080")
	}
	if gotDocroot != docroot {
		t.Fatalf("nginx docroot = %q, want %q", gotDocroot, docroot)
	}
	if !strings.Contains(stdout.String(), "fake-nginx localhost:8080 -t "+docroot) {
		t.Fatalf("Run(serve with nginx) output = %q, want nginx serve output", stdout.String())
	}
}

func TestPrepareNginxServeRuntimeCreatesLogsPath(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "run", "serve", "demo")
	configPath, phpLogPath, err := prepareNginxServeRuntime(runtimeDir, "localhost:8080", filepath.Join(runtimeDir, "docroot"), "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("prepareNginxServeRuntime() error = %v", err)
	}
	if configPath != filepath.Join(runtimeDir, "nginx.conf") {
		t.Fatalf("config path = %q, want %q", configPath, filepath.Join(runtimeDir, "nginx.conf"))
	}
	if phpLogPath != filepath.Join(runtimeDir, "php.log") {
		t.Fatalf("php log path = %q, want %q", phpLogPath, filepath.Join(runtimeDir, "php.log"))
	}
	if info, err := os.Stat(filepath.Join(runtimeDir, "logs")); err != nil {
		t.Fatalf("Stat(logs dir) error = %v", err)
	} else if !info.IsDir() {
		t.Fatalf("logs path mode = %v, want directory", info.Mode())
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	config := string(configData)
	if !strings.Contains(config, "fastcgi_pass 127.0.0.1:9000;") {
		t.Fatalf("nginx config = %q, want fastcgi upstream", config)
	}
	if !strings.Contains(config, "text/css css;") {
		t.Fatalf("nginx config = %q, want css mime type mapping", config)
	}
	if !strings.Contains(config, "application/javascript js mjs;") {
		t.Fatalf("nginx config = %q, want js mime type mapping", config)
	}
	if !strings.Contains(config, "fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;") {
		t.Fatalf("nginx config = %q, want script filename fastcgi param", config)
	}
	if strings.Contains(config, "proxy_pass") {
		t.Fatalf("nginx config = %q, want no proxy_pass", config)
	}
}

func TestResolvePHPCGITargetUsesSiblingBinary(t *testing.T) {
	phpDir := filepath.Join(t.TempDir(), "php")
	phpTarget := filepath.Join(phpDir, "php")
	phpCGITarget := filepath.Join(phpDir, "php-cgi")
	if runtime.GOOS == "windows" {
		phpTarget += ".exe"
		phpCGITarget += ".exe"
	}
	if err := os.MkdirAll(filepath.Dir(phpTarget), 0o755); err != nil {
		t.Fatalf("MkdirAll(php dir) error = %v", err)
	}
	if err := os.WriteFile(phpTarget, []byte("php\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(php target) error = %v", err)
	}
	if err := os.WriteFile(phpCGITarget, []byte("php-cgi\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(php-cgi target) error = %v", err)
	}

	resolved, err := resolvePHPCGITarget(phpTarget)
	if err != nil {
		t.Fatalf("resolvePHPCGITarget() error = %v", err)
	}
	if resolved != phpCGITarget {
		t.Fatalf("resolvePHPCGITarget() = %q, want %q", resolved, phpCGITarget)
	}
}

func TestRunServeStartsConfiguredDatabaseBeforePhp(t *testing.T) {
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
	fakeMySQL := cachedDatabasePath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQL), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysql) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQL, fakeDatabaseScript("mysql"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysql) error = %v", err)
	}
	fakeMySQLServer := cachedDatabaseServerPath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQLServer), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysqld) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQLServer, fakeDatabaseScript("mysqld"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysqld) error = %v", err)
	}
	fakeMySQLAdmin := cachedDatabaseAdminPath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQLAdmin), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysqladmin) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQLAdmin, fakeDatabaseScript("mysqladmin"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysqladmin) error = %v", err)
	}
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4", "--db-engine", "mysql", "--db-version", "8.4", "--db-port", "3307"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	oldInitialize := initializeDatabaseServerFunc
	oldStart := startDatabaseServerFunc
	oldPing := pingDatabaseAddressFunc
	oldNow := dbNowFunc
	t.Cleanup(func() {
		initializeDatabaseServerFunc = oldInitialize
		startDatabaseServerFunc = oldStart
		pingDatabaseAddressFunc = oldPing
		dbNowFunc = oldNow
	})

	running := map[string]bool{}
	initialized := 0
	started := 0
	var startedSpec dbServerSpec
	initializeDatabaseServerFunc = func(spec dbServerSpec) error {
		initialized++
		if err := os.MkdirAll(spec.DataDir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(spec.DataDir, "initialized.txt"), []byte("ok\n"), 0o644)
	}
	startDatabaseServerFunc = func(spec dbServerSpec) (dbStartResult, error) {
		started++
		startedSpec = spec
		running[databaseAddress(spec.Port)] = true
		return dbStartResult{PID: 7878}, nil
	}
	pingDatabaseAddressFunc = func(address string) bool {
		return running[address]
	}
	dbNowFunc = func() time.Time {
		return time.Date(2026, time.May, 23, 15, 0, 0, 0, time.UTC)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "serve", filepath.Join("site", "public")}); code != 0 {
		t.Fatalf("Run(serve with db) code = %d, stderr = %q", code, stderr.String())
	}

	if initialized != 1 || started != 1 {
		t.Fatalf("initialize/start counts = %d/%d, want 1/1", initialized, started)
	}
	if startedSpec.Port != 3307 {
		t.Fatalf("started port = %d, want 3307", startedSpec.Port)
	}
	if startedSpec.AdminTarget == "" || startedSpec.DefaultsFile == "" || startedSpec.BootstrapSQLFile == "" {
		t.Fatalf("started spec = %#v, want credential/bootstrap assets for serve", startedSpec)
	}
	output := stdout.String()
	if !strings.Contains(output, "fake-php") || !strings.Contains(output, "-t "+docroot) {
		t.Fatalf("Run(serve with db) output = %q, want php command output after database start", output)
	}
	state, err := loadDatabaseState(databaseStatePath(root, "demo"))
	if err != nil {
		t.Fatalf("loadDatabaseState() error = %v", err)
	}
	if state.PID != 7878 || state.Port != 3307 {
		t.Fatalf("database state = %#v, want pid 7878 on port 3307", state)
	}
}

func TestRunCreateIsUnknownCommand(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"create", "demo"}); code != 2 {
		t.Fatalf("Run(create) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command \"create\"") {
		t.Fatalf("Run(create) stderr = %q, want unknown command message", stderr.String())
	}
}

func TestRunShLaunchesInteractiveShellWithPreferredPath(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	systemPath := filepath.Join(projectDir, "system-bin")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	configPath := filepath.Join(projectDir, "polka.yaml")
	configData := []byte("version: 1\nroot: .polka\ncurrent: demo\nenvironments:\n  demo:\n    php: \"8.4\"\n")
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	t.Setenv("PATH", systemPath)
	if runtime.GOOS != "windows" {
		t.Setenv("SHELL", filepath.Join(projectDir, "bin", "custom-shell"))
	}

	oldLaunch := launchInteractiveShellFunc
	t.Cleanup(func() {
		launchInteractiveShellFunc = oldLaunch
	})

	var capturedTarget string
	var capturedArgs []string
	var capturedEnv []string
	launchInteractiveShellFunc = func(stdout, stderr io.Writer, env []string, target string, args []string) (int, error) {
		capturedTarget = target
		capturedArgs = append([]string(nil), args...)
		capturedEnv = append([]string(nil), env...)
		return 0, nil
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

	if code := Run(stdout, stderr, []string{"--root", root, "sh"}); code != 0 {
		t.Fatalf("Run(sh) code = %d, stderr = %q", code, stderr.String())
	}

	pathKey, pathValue, ok := lookupEnvValue(runtime.GOOS, capturedEnv, "PATH")
	if !ok {
		t.Fatalf("captured env = %#v, want PATH entry", capturedEnv)
	}
	expectedArgs := []string{posixInteractiveArg}
	if runtime.GOOS == "windows" {
		expectedArgs = windowsInteractiveShellArgs()
	}
	if strings.Join(capturedArgs, "\x00") != strings.Join(expectedArgs, "\x00") {
		t.Fatalf("captured args = %#v, want %#v", capturedArgs, expectedArgs)
	}
	promptKey, promptRoot, ok := lookupEnvValue(runtime.GOOS, capturedEnv, polkaPromptRootEnv)
	if !ok {
		t.Fatalf("captured env = %#v, want %s entry", capturedEnv, polkaPromptRootEnv)
	}
	if promptRoot != projectDir {
		t.Fatalf("%s = %q, want %q", promptKey, promptRoot, projectDir)
	}
	promptEnvKey, promptEnvironment, ok := lookupEnvValue(runtime.GOOS, capturedEnv, polkaPromptEnvEnv)
	if !ok {
		t.Fatalf("captured env = %#v, want %s entry", capturedEnv, polkaPromptEnvEnv)
	}
	if promptEnvironment != "demo" {
		t.Fatalf("%s = %q, want demo", promptEnvKey, promptEnvironment)
	}

	expectedTarget := defaultWindowsShell
	if runtime.GOOS != "windows" {
		expectedTarget = filepath.Join(projectDir, "bin", "custom-shell")
	}
	if capturedTarget != expectedTarget {
		t.Fatalf("captured target = %q, want %q", capturedTarget, expectedTarget)
	}

	expectedPath := joinPathList(runtime.GOOS,
		filepath.Join(root, "bin"),
		filepath.Join(projectDir, "vendor", "bin"),
		systemPath,
	)
	if pathValue != expectedPath {
		t.Fatalf("%s = %q, want %q", pathKey, pathValue, expectedPath)
	}
	if stdout.String() != "Opened Polka shell for environtment demo\n" {
		t.Fatalf("Run(sh) stdout = %q, want environment banner", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(sh) stderr = %q, want empty", stderr.String())
	}

	if runtime.GOOS == "windows" && pathKey != "Path" && pathKey != "PATH" {
		t.Fatalf("PATH key = %q, want preserved PATH casing", pathKey)
	}
}

func TestRunShRejectsArguments(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"sh", "composer"}); code != 1 {
		t.Fatalf("Run(sh composer) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "sh does not take arguments") {
		t.Fatalf("Run(sh composer) stderr = %q, want argument error", stderr.String())
	}
}

func TestResolveInteractiveShellUsesExpectedDefaults(t *testing.T) {
	if target, args := resolveInteractiveShell("windows", "C:/Program Files/Git/bin/sh.exe"); target != defaultWindowsShell || strings.Join(args, "\x00") != strings.Join(windowsInteractiveShellArgs(), "\x00") {
		t.Fatalf("resolveInteractiveShell(windows) = (%q, %#v), want (%q, %#v)", target, args, defaultWindowsShell, windowsInteractiveShellArgs())
	}

	if target, args := resolveInteractiveShell("linux", " /bin/bash "); target != "/bin/bash" || strings.Join(args, "\x00") != strings.Join([]string{posixInteractiveArg}, "\x00") {
		t.Fatalf("resolveInteractiveShell(linux, shell) = (%q, %#v), want (/bin/bash, %#v)", target, args, []string{posixInteractiveArg})
	}

	if target, args := resolveInteractiveShell("linux", " "); target != "/bin/sh" || strings.Join(args, "\x00") != strings.Join([]string{posixInteractiveArg}, "\x00") {
		t.Fatalf("resolveInteractiveShell(linux, empty) = (%q, %#v), want (/bin/sh, %#v)", target, args, []string{posixInteractiveArg})
	}
}

func TestWindowsPromptCommandUsesPromptRootEnv(t *testing.T) {
	command := windowsPromptCommand()
	for _, expected := range []string{
		"function global:Get-PolkaPromptPath",
		"function global:prompt",
		"$env:" + polkaPromptEnvEnv,
		"$env:" + polkaPromptRootEnv,
		"Get-PolkaPromptPath $root $current",
		"('polka ' + $relative + ' (' + $name + ') > ')",
	} {
		if !strings.Contains(command, expected) {
			t.Fatalf("windowsPromptCommand() = %q, want substring %q", command, expected)
		}
	}
}

func TestBuildShellEnvironmentPreservesExistingWindowsPathKey(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewStore(filepath.Join(projectDir, ".polka"))
	env := []string{"Path=C:/Windows/System32", "HOME=" + projectDir}

	updated := buildShellEnvironment("windows", env, store)
	pathKey, pathValue, ok := lookupEnvValue("windows", updated, "PATH")
	if !ok {
		t.Fatalf("updated env = %#v, want PATH entry", updated)
	}
	if pathKey != "Path" {
		t.Fatalf("PATH key = %q, want Path", pathKey)
	}

	expectedPath := joinPathList("windows",
		filepath.Join(store.RootDir, "bin"),
		filepath.Join(projectDir, "vendor", "bin"),
		"C:/Windows/System32",
	)
	if pathValue != expectedPath {
		t.Fatalf("Path = %q, want %q", pathValue, expectedPath)
	}

	pathEntries := 0
	for _, entry := range updated {
		entryKey, _, ok := strings.Cut(entry, "=")
		if ok && envKeysEqual("windows", entryKey, "PATH") {
			pathEntries++
		}
	}
	if pathEntries != 1 {
		t.Fatalf("PATH entries = %d, want 1 in %#v", pathEntries, updated)
	}
}

func TestPrepareShellEnvironmentAddsWindowsVendorPHPShimDir(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewStore(filepath.Join(projectDir, ".polka"))
	vendorBinDir := filepath.Join(projectDir, "vendor", "bin")
	if err := os.MkdirAll(vendorBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(vendor/bin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(vendorBinDir, "dcg"), []byte("#!/usr/bin/env php\n<?php echo 'dcg';\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(vendor php proxy) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(vendorBinDir, "drush"), []byte("#!/usr/bin/env sh\nexec \"$DRUSH_PHP\" \"$@\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(vendor shell launcher) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(vendorBinDir, "drush.php"), []byte("#!/usr/bin/env php\n<?php echo 'drush';\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(drush.php) error = %v", err)
	}

	updated, err := prepareShellEnvironment("windows", []string{"PATH=C:/Windows/System32"}, store, projectDir, "demo")
	if err != nil {
		t.Fatalf("prepareShellEnvironment() error = %v", err)
	}

	promptEnvKey, promptEnvironment, ok := lookupEnvValue("windows", updated, polkaPromptEnvEnv)
	if !ok {
		t.Fatalf("updated env = %#v, want %s entry", updated, polkaPromptEnvEnv)
	}
	if promptEnvKey != polkaPromptEnvEnv || promptEnvironment != "demo" {
		t.Fatalf("%s = %q, want demo", promptEnvKey, promptEnvironment)
	}

	promptKey, promptRoot, ok := lookupEnvValue("windows", updated, polkaPromptRootEnv)
	if !ok {
		t.Fatalf("updated env = %#v, want %s entry", updated, polkaPromptRootEnv)
	}
	if promptKey != polkaPromptRootEnv || promptRoot != projectDir {
		t.Fatalf("%s = %q, want %q", promptKey, promptRoot, projectDir)
	}

	_, pathValue, ok := lookupEnvValue("windows", updated, "PATH")
	if !ok {
		t.Fatalf("updated env = %#v, want PATH entry", updated)
	}

	shimDir := filepath.Join(store.RootDir, shellRuntimeDirName, shellSupportDirName, vendorShimDirName)
	expectedPath := joinPathList("windows",
		store.BinDir,
		shimDir,
		filepath.Join(projectDir, "vendor", "bin"),
		"C:/Windows/System32",
	)
	if pathValue != expectedPath {
		t.Fatalf("PATH = %q, want %q", pathValue, expectedPath)
	}

	dcgShimData, err := os.ReadFile(filepath.Join(shimDir, "dcg.cmd"))
	if err != nil {
		t.Fatalf("ReadFile(dcg shim) error = %v", err)
	}
	if !strings.Contains(string(dcgShimData), "call php \""+escapeWindowsShimValue(filepath.Join(vendorBinDir, "dcg"))+"\" %*") {
		t.Fatalf("dcg shim = %q, want php proxy target", string(dcgShimData))
	}

	vendorShimData, err := os.ReadFile(filepath.Join(shimDir, "drush.cmd"))
	if err != nil {
		t.Fatalf("ReadFile(vendor shim) error = %v", err)
	}
	if !strings.Contains(string(vendorShimData), "call php \""+escapeWindowsShimValue(filepath.Join(vendorBinDir, "drush.php"))+"\" %*") {
		t.Fatalf("vendor shim = %q, want matching php target", string(vendorShimData))
	}
}

func TestShellPromptRootReturnsAbsolutePath(t *testing.T) {
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

	root, err := shellPromptRoot(filepath.Join(".", ".polka", ".."))
	if err != nil {
		t.Fatalf("shellPromptRoot() error = %v", err)
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("shellPromptRoot() = %q, want absolute path", root)
	}
	if root != projectDir {
		t.Fatalf("shellPromptRoot() = %q, want %q", root, projectDir)
	}
}

func TestPrepareShellEnvironmentSkipsUnresolvedWindowsVendorShellLauncher(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewStore(filepath.Join(projectDir, ".polka"))
	vendorBinDir := filepath.Join(projectDir, "vendor", "bin")
	if err := os.MkdirAll(vendorBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(vendor/bin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(vendorBinDir, "hello"), []byte("#!/usr/bin/env sh\necho hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(vendor script) error = %v", err)
	}

	updated, err := prepareShellEnvironment("windows", []string{"PATH=C:/Windows/System32"}, store, projectDir, "none")
	if err != nil {
		t.Fatalf("prepareShellEnvironment() error = %v", err)
	}
	_, pathValue, ok := lookupEnvValue("windows", updated, "PATH")
	if !ok {
		t.Fatalf("updated env = %#v, want PATH entry", updated)
	}
	expectedPath := joinPathList("windows",
		store.BinDir,
		filepath.Join(projectDir, "vendor", "bin"),
		"C:/Windows/System32",
	)
	if pathValue != expectedPath {
		t.Fatalf("PATH = %q, want %q", pathValue, expectedPath)
	}
	if _, err := os.Stat(filepath.Join(store.RootDir, shellRuntimeDirName, shellSupportDirName, vendorShimDirName, "hello.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(unresolved shim) error = %v, want missing wrapper", err)
	}
}

func TestPrepareShellEnvironmentUsesNearestNestedVendorBin(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewStore(filepath.Join(projectDir, ".polka"))
	nestedProjectDir := filepath.Join(projectDir, "drupal")
	nestedVendorBinDir := filepath.Join(nestedProjectDir, "vendor", "bin")
	if err := os.MkdirAll(nestedVendorBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested vendor/bin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedVendorBinDir, "drush"), []byte("#!/usr/bin/env sh\nexec \"$DRUSH_PHP\" \"$@\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(vendor shell launcher) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedVendorBinDir, "drush.php"), []byte("#!/usr/bin/env php\n<?php echo 'drush';\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(drush.php) error = %v", err)
	}

	updated, err := prepareShellEnvironment("windows", []string{"PATH=C:/Windows/System32"}, store, nestedProjectDir, "demo")
	if err != nil {
		t.Fatalf("prepareShellEnvironment() error = %v", err)
	}

	_, pathValue, ok := lookupEnvValue("windows", updated, "PATH")
	if !ok {
		t.Fatalf("updated env = %#v, want PATH entry", updated)
	}
	shimDir := filepath.Join(store.RootDir, shellRuntimeDirName, shellSupportDirName, vendorShimDirName)
	expectedPath := joinPathList("windows",
		store.BinDir,
		shimDir,
		nestedVendorBinDir,
		"C:/Windows/System32",
	)
	if pathValue != expectedPath {
		t.Fatalf("PATH = %q, want %q", pathValue, expectedPath)
	}
	shimData, err := os.ReadFile(filepath.Join(shimDir, "drush.cmd"))
	if err != nil {
		t.Fatalf("ReadFile(drush shim) error = %v", err)
	}
	if !strings.Contains(string(shimData), escapeWindowsShimValue(filepath.Join(nestedVendorBinDir, "drush.php"))) {
		t.Fatalf("drush shim = %q, want nested vendor target", string(shimData))
	}
}

func TestRunSessionStartWritesActivationScriptAndState(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	systemPath := filepath.Join(projectDir, "system-bin")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	configPath := filepath.Join(projectDir, "polka.yaml")
	configData := []byte("version: 1\nroot: .polka\ncurrent: demo\nenvironments:\n  demo:\n    php: \"8.4\"\n")
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	oldSessionIDFunc := newShellSessionIDFunc
	t.Cleanup(func() {
		newShellSessionIDFunc = oldSessionIDFunc
	})
	newShellSessionIDFunc = func() (string, error) {
		return "session-demo", nil
	}

	t.Setenv("PATH", systemPath)
	t.Setenv(polkaSessionIDEnv, "")
	t.Setenv(polkaSessionStateEnv, "")

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

	if code := Run(stdout, stderr, []string{"--root", root, "session", "start"}); code != 0 {
		t.Fatalf("Run(session start) code = %d, stderr = %q", code, stderr.String())
	}

	kind := currentShellScriptKind(runtime.GOOS)
	activationPath := strings.TrimSpace(stdout.String())
	expectedActivationPath := shellSessionActivationPath(root, "session-demo", kind)
	if activationPath != expectedActivationPath {
		t.Fatalf("activation path = %q, want %q", activationPath, expectedActivationPath)
	}
	if !filepath.IsAbs(activationPath) {
		t.Fatalf("activation path = %q, want absolute path", activationPath)
	}

	statePath := shellSessionStatePath(root, "session-demo")
	state, err := loadShellSessionState(statePath)
	if err != nil {
		t.Fatalf("loadShellSessionState() error = %v", err)
	}
	if state.EnvironmentName != "demo" {
		t.Fatalf("session environment = %q, want demo", state.EnvironmentName)
	}
	if state.OriginalPathValue != systemPath {
		t.Fatalf("original PATH = %q, want %q", state.OriginalPathValue, systemPath)
	}
	if state.ActivationPath != activationPath {
		t.Fatalf("state activation path = %q, want %q", state.ActivationPath, activationPath)
	}

	activationScriptData, err := os.ReadFile(activationPath)
	if err != nil {
		t.Fatalf("ReadFile(activation script) error = %v", err)
	}
	expectedActivatedPath := joinPathList(runtime.GOOS,
		filepath.Join(root, "bin"),
		filepath.Join(projectDir, "vendor", "bin"),
		systemPath,
	)
	expectedScript := renderShellSessionStartScript(kind, state.OriginalPathKey, expectedActivatedPath, state.ID, statePath)
	if string(activationScriptData) != expectedScript {
		t.Fatalf("activation script = %q, want %q", string(activationScriptData), expectedScript)
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(session start) stderr = %q, want empty", stderr.String())
	}
}

func TestRunSessionStopWritesDeactivationScriptFromActiveSessionState(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	systemPath := filepath.Join(projectDir, "system-bin")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	configPath := filepath.Join(projectDir, "polka.yaml")
	configData := []byte("version: 1\nroot: .polka\ncurrent: demo\nenvironments:\n  demo:\n    php: \"8.4\"\n")
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	oldSessionIDFunc := newShellSessionIDFunc
	t.Cleanup(func() {
		newShellSessionIDFunc = oldSessionIDFunc
	})
	newShellSessionIDFunc = func() (string, error) {
		return "session-demo", nil
	}

	t.Setenv("PATH", systemPath)
	t.Setenv(polkaSessionIDEnv, "")
	t.Setenv(polkaSessionStateEnv, "")

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

	if code := Run(stdout, stderr, []string{"--root", root, "session", "start"}); code != 0 {
		t.Fatalf("Run(session start) code = %d, stderr = %q", code, stderr.String())
	}
	statePath := shellSessionStatePath(root, "session-demo")
	state, err := loadShellSessionState(statePath)
	if err != nil {
		t.Fatalf("loadShellSessionState() error = %v", err)
	}
	t.Setenv(polkaSessionIDEnv, state.ID)
	t.Setenv(polkaSessionStateEnv, statePath)

	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("Chdir(otherDir) error = %v", err)
	}
	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"session", "stop"}); code != 0 {
		t.Fatalf("Run(session stop) code = %d, stderr = %q", code, stderr.String())
	}

	kind := currentShellScriptKind(runtime.GOOS)
	deactivationPath := strings.TrimSpace(stdout.String())
	expectedDeactivationPath := shellSessionDeactivationPath(root, "session-demo", kind)
	if deactivationPath != expectedDeactivationPath {
		t.Fatalf("deactivation path = %q, want %q", deactivationPath, expectedDeactivationPath)
	}
	deactivationScriptData, err := os.ReadFile(deactivationPath)
	if err != nil {
		t.Fatalf("ReadFile(deactivation script) error = %v", err)
	}
	expectedScript := renderShellSessionStopScript(kind, *state, statePath)
	if string(deactivationScriptData) != expectedScript {
		t.Fatalf("deactivation script = %q, want %q", string(deactivationScriptData), expectedScript)
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(session stop) stderr = %q, want empty", stderr.String())
	}
}

func TestRunSessionStartRejectsNestedSession(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	t.Setenv(polkaSessionStateEnv, filepath.Join(projectDir, "active-session.json"))

	if code := Run(stdout, stderr, []string{"--root", root, "session", "start"}); code != 1 {
		t.Fatalf("Run(session start) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "already active") {
		t.Fatalf("Run(session start) stderr = %q, want active session error", stderr.String())
	}
}

func TestRunSessionStopRejectsWhenNoSessionIsActive(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	t.Setenv(polkaSessionIDEnv, "")
	t.Setenv(polkaSessionStateEnv, "")

	if code := Run(stdout, stderr, []string{"session", "stop"}); code != 1 {
		t.Fatalf("Run(session stop) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no active Polka session") {
		t.Fatalf("Run(session stop) stderr = %q, want inactive session error", stderr.String())
	}
}

func TestWriteShellSessionStateRoundTripsJSON(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "session.json")
	state := shellSessionState{
		ID:                "demo",
		Shell:             string(shellScriptKindPOSIX),
		EnvironmentName:   "blog",
		OriginalPathKey:   "PATH",
		OriginalPathValue: "/tmp/bin",
		HasOriginalPath:   true,
		ActivationPath:    "/tmp/activate.sh",
		DeactivationPath:  "/tmp/deactivate.sh",
	}

	if err := writeShellSessionState(statePath, state); err != nil {
		t.Fatalf("writeShellSessionState() error = %v", err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile(state) error = %v", err)
	}
	var decoded shellSessionState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(state) error = %v", err)
	}
	if decoded != state {
		t.Fatalf("decoded state = %#v, want %#v", decoded, state)
	}
}

func cachedPHPPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "php", version, "bin", "php.cmd")
	}

	return filepath.Join(root, "php", version, "bin", "php")
}

func cachedComposerPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "composer", version, "bin", "composer.cmd")
	}

	return filepath.Join(root, "composer", version, "bin", "composer.phar")
}

func cachedDatabasePath(root, tool, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, tool, version, "bin", tool+".cmd")
	}

	return filepath.Join(root, tool, version, "bin", tool)
}

func cachedDatabaseServerPath(root, tool, version string) string {
	serverName := "mysqld"
	if tool == "mariadb" {
		serverName = "mariadbd"
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(root, tool, version, "bin", serverName+".exe")
	}

	return filepath.Join(root, tool, version, "bin", serverName)
}

func cachedDatabaseAdminPath(root, tool, version string) string {
	adminName := "mysqladmin"
	if tool == "mariadb" {
		adminName = "mariadb-admin"
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(root, tool, version, "bin", adminName+".exe")
	}

	return filepath.Join(root, tool, version, "bin", adminName)
}

func projectInstalledPHPPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "envs", "php", version, "bin", "php.cmd")
	}

	return filepath.Join(root, "envs", "php", version, "bin", "php")
}

func projectInstalledPHPConfigPath(root, version string) string {
	return filepath.Join(filepath.Dir(projectInstalledPHPPath(root, version)), "php.ini")
}

func fakePHPScript() []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-php %*\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-php %s\n' \"$*\"\n")
}

func fakeDatabaseScript(name string) []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-" + name + " %*\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-" + name + " %s\n' \"$*\"\n")
}
