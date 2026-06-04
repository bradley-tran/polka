package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

type testConfigFile struct {
	Version      int
	Root         string
	Environments map[string]testEnvironmentConfig
}

type testEnvironmentConfig struct {
	PHP           string                `yaml:"php"`
	Composer      string                `yaml:"composer"`
	NodeJS        string                `yaml:"nodejs,omitempty"`
	Mago          string                `yaml:"mago,omitempty"`
	Nginx         string                `yaml:"nginx,omitempty"`
	PHPMyAdmin    *testPHPMyAdminConfig `yaml:"phpmyadmin,omitempty"`
	Docroot       string                `yaml:"docroot,omitempty"`
	EnvFile       string                `yaml:"env-file,omitempty"`
	EnvVars       map[string]string     `yaml:"env-vars,omitempty"`
	Database      *testDatabaseConfig   `yaml:"database,omitempty"`
	Mailpit       *testMailpitConfig    `yaml:"mailpit,omitempty"`
	PHPExtensions map[string]bool       `yaml:"php-extensions,omitempty"`
	Server        *testServerConfig     `yaml:"server,omitempty"`
}

type testDatabaseConfig struct {
	Engine  string `yaml:"engine,omitempty"`
	Version string `yaml:"version,omitempty"`
	Port    int    `yaml:"port,omitempty"`
}

type testServerConfig struct {
	Hostname string `yaml:"hostname,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	HTTPS    bool   `yaml:"https,omitempty"`
}

type testMailpitConfig struct {
	Version  string `yaml:"version,omitempty"`
	SMTPPort int    `yaml:"smtp-port,omitempty"`
	UIPort   int    `yaml:"ui-port,omitempty"`
	HTTPS    bool   `yaml:"https,omitempty"`
}

type testPHPMyAdminConfig struct {
	Version string `yaml:"version,omitempty"`
	Port    int    `yaml:"port,omitempty"`
	HTTPS   bool   `yaml:"https,omitempty"`
}

type testProjectConfigData struct {
	Version       int               `yaml:"version,omitempty"`
	Root          string            `yaml:"root,omitempty"`
	Tools         *testToolsConfig  `yaml:"tools,omitempty"`
	Docroot       string            `yaml:"docroot,omitempty"`
	EnvFile       string            `yaml:"env-file,omitempty"`
	EnvVars       map[string]string `yaml:"env-vars,omitempty"`
	PHPExtensions map[string]bool   `yaml:"php-extensions,omitempty"`
	Server        *testServerConfig `yaml:"server,omitempty"`
}

type testEnvironmentConfigData struct {
	Tools         *testToolsConfig  `yaml:"tools,omitempty"`
	Docroot       string            `yaml:"docroot,omitempty"`
	EnvFile       string            `yaml:"env-file,omitempty"`
	EnvVars       map[string]string `yaml:"env-vars,omitempty"`
	PHPExtensions map[string]bool   `yaml:"php-extensions,omitempty"`
	Server        *testServerConfig `yaml:"server,omitempty"`
}

type testToolsConfig struct {
	PHP        string                `yaml:"php,omitempty"`
	Composer   string                `yaml:"composer,omitempty"`
	NodeJS     string                `yaml:"nodejs,omitempty"`
	Mago       string                `yaml:"mago,omitempty"`
	Nginx      string                `yaml:"nginx,omitempty"`
	Database   *testDatabaseConfig   `yaml:"database,omitempty"`
	Mailpit    *testMailpitConfig    `yaml:"mailpit,omitempty"`
	PHPMyAdmin *testPHPMyAdminConfig `yaml:"phpmyadmin,omitempty"`
}

func writeTestConfigFile(t *testing.T, projectDir string, config testConfigFile) {
	t.Helper()

	projectConfig := testProjectConfigData{
		Version: config.Version,
		Root:    config.Root,
	}
	if projectConfig.Version == 0 {
		projectConfig.Version = 1
	}
	if strings.TrimSpace(projectConfig.Root) == "" {
		projectConfig.Root = ".polka"
	}
	if defaultEnvironment, ok := config.Environments[defaultEnvironmentName]; ok {
		projectConfig.Tools = testToolsFromEnvironment(defaultEnvironment)
		projectConfig.Docroot = defaultEnvironment.Docroot
		projectConfig.EnvFile = defaultEnvironment.EnvFile
		projectConfig.EnvVars = defaultEnvironment.EnvVars
		projectConfig.PHPExtensions = defaultEnvironment.PHPExtensions
		projectConfig.Server = defaultEnvironment.Server
	}

	writeTestYAML(t, filepath.Join(projectDir, "polka.yaml"), projectConfig)
	for name, environment := range config.Environments {
		if name == defaultEnvironmentName {
			continue
		}
		writeTestEnvironmentConfig(t, projectDir, name, environment)
	}
}

func readTestConfigFile(t *testing.T, projectDir string) testConfigFile {
	t.Helper()

	var projectConfig testProjectConfigData
	readTestYAML(t, filepath.Join(projectDir, "polka.yaml"), &projectConfig)
	config := testConfigFile{
		Version: projectConfig.Version,
		Root:    projectConfig.Root,
		Environments: map[string]testEnvironmentConfig{
			defaultEnvironmentName: testEnvironmentFromProjectConfig(projectConfig),
		},
	}

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		t.Fatalf("ReadDir(project) error = %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name, ok := testNamedEnvironmentName(entry.Name())
		if !ok {
			continue
		}
		config.Environments[name] = readTestEnvironmentConfig(t, projectDir, name)
	}

	return config
}

func readTestEnvironmentConfig(t *testing.T, projectDir, name string) testEnvironmentConfig {
	t.Helper()

	if name == defaultEnvironmentName {
		var projectConfig testProjectConfigData
		readTestYAML(t, filepath.Join(projectDir, "polka.yaml"), &projectConfig)
		return testEnvironmentFromProjectConfig(projectConfig)
	}

	var environmentConfig testEnvironmentConfigData
	readTestYAML(t, testEnvironmentConfigPath(projectDir, name), &environmentConfig)
	return testEnvironmentFromEnvironmentConfig(environmentConfig)
}

func writeTestEnvironmentConfig(t *testing.T, projectDir, name string, environment testEnvironmentConfig) {
	t.Helper()

	if name == defaultEnvironmentName {
		var projectConfig testProjectConfigData
		readTestYAML(t, filepath.Join(projectDir, "polka.yaml"), &projectConfig)
		projectConfig.Tools = testToolsFromEnvironment(environment)
		projectConfig.Docroot = environment.Docroot
		projectConfig.EnvFile = environment.EnvFile
		projectConfig.EnvVars = environment.EnvVars
		projectConfig.PHPExtensions = environment.PHPExtensions
		projectConfig.Server = environment.Server
		writeTestYAML(t, filepath.Join(projectDir, "polka.yaml"), projectConfig)
		return
	}

	writeTestYAML(t, testEnvironmentConfigPath(projectDir, name), testEnvironmentConfigData{
		Tools:         testToolsFromEnvironment(environment),
		Docroot:       environment.Docroot,
		EnvFile:       environment.EnvFile,
		EnvVars:       environment.EnvVars,
		PHPExtensions: environment.PHPExtensions,
		Server:        environment.Server,
	})
}

func readTestYAML(t *testing.T, path string, value any) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if err := yaml.Unmarshal(data, value); err != nil {
		t.Fatalf("yaml.Unmarshal(%s) error = %v", path, err)
	}
}

func writeTestYAML(t *testing.T, path string, value any) {
	t.Helper()

	data, err := yaml.Marshal(value)
	if err != nil {
		t.Fatalf("yaml.Marshal(%s) error = %v", path, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func testEnvironmentFromProjectConfig(projectConfig testProjectConfigData) testEnvironmentConfig {
	return testEnvironmentFromParts(projectConfig.Tools, projectConfig.Docroot, projectConfig.EnvFile, projectConfig.EnvVars, projectConfig.PHPExtensions, projectConfig.Server)
}

func testEnvironmentFromEnvironmentConfig(environmentConfig testEnvironmentConfigData) testEnvironmentConfig {
	return testEnvironmentFromParts(environmentConfig.Tools, environmentConfig.Docroot, environmentConfig.EnvFile, environmentConfig.EnvVars, environmentConfig.PHPExtensions, environmentConfig.Server)
}

func testEnvironmentFromParts(tools *testToolsConfig, docroot, envFile string, envVars map[string]string, phpExtensions map[string]bool, server *testServerConfig) testEnvironmentConfig {
	environment := testEnvironmentConfig{
		Docroot:       docroot,
		EnvFile:       envFile,
		EnvVars:       envVars,
		PHPExtensions: phpExtensions,
		Server:        server,
	}
	if tools != nil {
		environment.PHP = tools.PHP
		environment.Composer = tools.Composer
		environment.NodeJS = tools.NodeJS
		environment.Mago = tools.Mago
		environment.Nginx = tools.Nginx
		environment.Database = tools.Database
		environment.Mailpit = tools.Mailpit
		environment.PHPMyAdmin = tools.PHPMyAdmin
	}

	return environment
}

func testToolsFromEnvironment(environment testEnvironmentConfig) *testToolsConfig {
	tools := &testToolsConfig{
		PHP:        environment.PHP,
		Composer:   environment.Composer,
		NodeJS:     environment.NodeJS,
		Mago:       environment.Mago,
		Nginx:      environment.Nginx,
		Database:   environment.Database,
		Mailpit:    environment.Mailpit,
		PHPMyAdmin: environment.PHPMyAdmin,
	}
	if strings.TrimSpace(tools.PHP) == "" &&
		strings.TrimSpace(tools.Composer) == "" &&
		strings.TrimSpace(tools.NodeJS) == "" &&
		strings.TrimSpace(tools.Mago) == "" &&
		strings.TrimSpace(tools.Nginx) == "" &&
		tools.Database == nil &&
		tools.Mailpit == nil &&
		tools.PHPMyAdmin == nil {
		return nil
	}

	return tools
}

func testEnvironmentConfigPath(projectDir, name string) string {
	if name == defaultEnvironmentName {
		return filepath.Join(projectDir, "polka.yaml")
	}

	return filepath.Join(projectDir, "polka."+name+".yaml")
}

func testNamedEnvironmentName(fileName string) (string, bool) {
	if fileName == "polka.yaml" || !strings.HasPrefix(fileName, "polka.") || !strings.HasSuffix(fileName, ".yaml") {
		return "", false
	}

	return strings.TrimSuffix(strings.TrimPrefix(fileName, "polka."), ".yaml"), true
}

func cachedPHPPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "php", version, "bin", "php.cmd")
	}

	return filepath.Join(root, "php", version, "bin", "php")
}

func readTestActiveEnvironment(t *testing.T, root string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, "run", "current"))
	if err != nil {
		t.Fatalf("ReadFile(active environment) error = %v", err)
	}

	return strings.TrimSpace(string(data))
}

func writeTestActiveEnvironment(t *testing.T, root, name string) {
	t.Helper()

	path := filepath.Join(root, "run", "current")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(active environment dir) error = %v", err)
	}
	if err := os.WriteFile(path, []byte(name+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(active environment) error = %v", err)
	}
}

func cachedComposerPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "composer", version, "bin", "composer.cmd")
	}

	return filepath.Join(root, "composer", version, "bin", "composer.phar")
}

func cachedNodeJSPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "nodejs", version, "node.exe")
	}

	return filepath.Join(root, "nodejs", version, "bin", "node")
}

func projectInstalledNodeJSCommandPath(root, version, command string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "envs", "nodejs", version, command+".cmd")
	}

	return filepath.Join(root, "envs", "nodejs", version, "bin", command)
}

func projectInstalledNodeJSPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "envs", "nodejs", version, "node.exe")
	}

	return filepath.Join(root, "envs", "nodejs", version, "bin", "node")
}

func cachedDatabasePath(root, tool, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, tool, version, "bin", tool+".cmd")
	}

	return filepath.Join(root, tool, version, "bin", tool)
}

func cachedMailpitPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "mailpit", version, "mailpit.exe")
	}

	return filepath.Join(root, "mailpit", version, "mailpit")
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

func cachedDatabaseDumpPath(root, tool, version string) string {
	dumpName := "mysqldump"
	if tool == "mariadb" {
		dumpName = "mariadb-dump"
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(root, tool, version, "bin", dumpName+".cmd")
	}

	return filepath.Join(root, tool, version, "bin", dumpName)
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

func fakePHPScriptWithEnv(varName string) []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-php %* %" + varName + "%\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-php %s %s\n' \"$*\" \"$" + varName + "\"\n")
}

func fakeToolScript(name string) []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-" + name + " %*\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-" + name + " %s\n' \"$*\"\n")
}

func fakeDatabaseScript(name string) []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-" + name + " %*\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-" + name + " %s\n' \"$*\"\n")
}

func fakeDatabaseCaptureScript(name string) []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-" + name + " %*\r\npowershell -NoProfile -Command \"[IO.File]::WriteAllText($env:POLKA_TEST_DB_CAPTURE_PATH, [Console]::In.ReadToEnd())\"\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-" + name + " %s\n' \"$*\"\ncat > \"$POLKA_TEST_DB_CAPTURE_PATH\"\n")
}

func fakeDatabaseDumpScript() []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\nif not \"%POLKA_TEST_DB_DUMP_CAPTURE_PATH%\"==\"\" echo %* > \"%POLKA_TEST_DB_DUMP_CAPTURE_PATH%\"\r\npowershell -NoProfile -Command \"[Console]::Out.Write($env:POLKA_TEST_DB_DUMP_OUTPUT)\"\r\n")
	}

	return []byte("#!/usr/bin/env sh\nif [ -n \"$POLKA_TEST_DB_DUMP_CAPTURE_PATH\" ]; then\n  printf '%s\n' \"$*\" > \"$POLKA_TEST_DB_DUMP_CAPTURE_PATH\"\nfi\nprintf '%s' \"$POLKA_TEST_DB_DUMP_OUTPUT\"\n")
}
