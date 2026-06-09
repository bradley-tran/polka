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
	MySQL         string                `yaml:"mysql,omitempty"`
	MariaDB       string                `yaml:"mariadb,omitempty"`
	SQLite        string                `yaml:"sqlite,omitempty"`
	PHPMyAdmin    *testPHPMyAdminConfig `yaml:"phpmyadmin,omitempty"`
	Docroot       string                `yaml:"docroot,omitempty"`
	HTTPS         bool                  `yaml:"https,omitempty"`
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
	Version       int                 `yaml:"version,omitempty"`
	Root          string              `yaml:"root,omitempty"`
	Tools         *testToolsConfig    `yaml:"tools,omitempty"`
	Settings      *testSettingsConfig `yaml:"settings,omitempty"`
	Docroot       string              `yaml:"docroot,omitempty"`
	HTTPS         bool                `yaml:"https,omitempty"`
	EnvFile       string              `yaml:"env-file,omitempty"`
	EnvVars       map[string]string   `yaml:"env-vars,omitempty"`
	Database      *testDatabaseConfig `yaml:"database,omitempty"`
	PHPExtensions map[string]bool     `yaml:"php-extensions,omitempty"`
	Server        *testServerConfig   `yaml:"server,omitempty"`
}

type testEnvironmentConfigData struct {
	Tools         *testToolsConfig    `yaml:"tools,omitempty"`
	Settings      *testSettingsConfig `yaml:"settings,omitempty"`
	Docroot       string              `yaml:"docroot,omitempty"`
	HTTPS         bool                `yaml:"https,omitempty"`
	EnvFile       string              `yaml:"env-file,omitempty"`
	EnvVars       map[string]string   `yaml:"env-vars,omitempty"`
	Database      *testDatabaseConfig `yaml:"database,omitempty"`
	PHPExtensions map[string]bool     `yaml:"php-extensions,omitempty"`
	Server        *testServerConfig   `yaml:"server,omitempty"`
}

type testToolsConfig struct {
	PHP        string `yaml:"php,omitempty"`
	Composer   string `yaml:"composer,omitempty"`
	NodeJS     string `yaml:"nodejs,omitempty"`
	Mago       string `yaml:"mago,omitempty"`
	Nginx      string `yaml:"nginx,omitempty"`
	MySQL      string `yaml:"mysql,omitempty"`
	MariaDB    string `yaml:"mariadb,omitempty"`
	SQLite     string `yaml:"sqlite,omitempty"`
	Mailpit    string `yaml:"mailpit,omitempty"`
	PHPMyAdmin string `yaml:"phpmyadmin,omitempty"`
}

type testSettingsConfig struct {
	Mailpit    *testMailpitSettingsConfig    `yaml:"mailpit,omitempty"`
	PHPMyAdmin *testPHPMyAdminSettingsConfig `yaml:"phpmyadmin,omitempty"`
}

type testMailpitSettingsConfig struct {
	SMTPPort int `yaml:"smtp-port,omitempty"`
	UIPort   int `yaml:"ui-port,omitempty"`
}

type testPHPMyAdminSettingsConfig struct {
	Port int `yaml:"port,omitempty"`
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
		projectConfig.Settings = testSettingsFromEnvironment(defaultEnvironment)
		projectConfig.Docroot = defaultEnvironment.Docroot
		projectConfig.HTTPS = defaultEnvironment.HTTPS
		projectConfig.EnvFile = defaultEnvironment.EnvFile
		projectConfig.EnvVars = defaultEnvironment.EnvVars
		projectConfig.Database = testDatabaseRuntimeFromEnvironment(defaultEnvironment)
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
		projectConfig.Settings = testSettingsFromEnvironment(environment)
		projectConfig.Docroot = environment.Docroot
		projectConfig.HTTPS = environment.HTTPS
		projectConfig.EnvFile = environment.EnvFile
		projectConfig.EnvVars = environment.EnvVars
		projectConfig.Database = testDatabaseRuntimeFromEnvironment(environment)
		projectConfig.PHPExtensions = environment.PHPExtensions
		projectConfig.Server = environment.Server
		writeTestYAML(t, filepath.Join(projectDir, "polka.yaml"), projectConfig)
		return
	}

	writeTestYAML(t, testEnvironmentConfigPath(projectDir, name), testEnvironmentConfigData{
		Tools:         testToolsFromEnvironment(environment),
		Settings:      testSettingsFromEnvironment(environment),
		Docroot:       environment.Docroot,
		HTTPS:         environment.HTTPS,
		EnvFile:       environment.EnvFile,
		EnvVars:       environment.EnvVars,
		Database:      testDatabaseRuntimeFromEnvironment(environment),
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
	return testEnvironmentFromParts(projectConfig.Tools, projectConfig.Settings, projectConfig.Docroot, projectConfig.HTTPS, projectConfig.EnvFile, projectConfig.EnvVars, projectConfig.Database, projectConfig.PHPExtensions, projectConfig.Server)
}

func testEnvironmentFromEnvironmentConfig(environmentConfig testEnvironmentConfigData) testEnvironmentConfig {
	return testEnvironmentFromParts(environmentConfig.Tools, environmentConfig.Settings, environmentConfig.Docroot, environmentConfig.HTTPS, environmentConfig.EnvFile, environmentConfig.EnvVars, environmentConfig.Database, environmentConfig.PHPExtensions, environmentConfig.Server)
}

func testEnvironmentFromParts(tools *testToolsConfig, settings *testSettingsConfig, docroot string, https bool, envFile string, envVars map[string]string, database *testDatabaseConfig, phpExtensions map[string]bool, server *testServerConfig) testEnvironmentConfig {
	environment := testEnvironmentConfig{
		Docroot:       docroot,
		HTTPS:         https,
		EnvFile:       envFile,
		EnvVars:       envVars,
		Database:      database,
		PHPExtensions: phpExtensions,
		Server:        server,
	}
	if tools != nil {
		environment.PHP = tools.PHP
		environment.Composer = tools.Composer
		environment.NodeJS = tools.NodeJS
		environment.Mago = tools.Mago
		environment.Nginx = tools.Nginx
		environment.MySQL = tools.MySQL
		environment.MariaDB = tools.MariaDB
		environment.SQLite = tools.SQLite
		environment.Database = testDatabaseFromTools(tools, database)
		environment = testPopulateDatabaseToolVersion(environment)
		if strings.TrimSpace(tools.Mailpit) != "" {
			environment.Mailpit = &testMailpitConfig{Version: tools.Mailpit}
		}
		if strings.TrimSpace(tools.PHPMyAdmin) != "" {
			environment.PHPMyAdmin = &testPHPMyAdminConfig{Version: tools.PHPMyAdmin}
		}
	}
	environment = testApplySettings(environment, settings)
	if environment.HTTPS {
		if environment.Server == nil {
			environment.Server = &testServerConfig{}
		}
		environment.Server.HTTPS = true
		if environment.Mailpit != nil {
			environment.Mailpit.HTTPS = true
		}
		if environment.PHPMyAdmin != nil {
			environment.PHPMyAdmin.HTTPS = true
		}
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
		MySQL:      testDatabaseToolVersion(environment, "mysql"),
		MariaDB:    testDatabaseToolVersion(environment, "mariadb"),
		SQLite:     environment.SQLite,
		Mailpit:    testMailpitVersion(environment.Mailpit),
		PHPMyAdmin: testPHPMyAdminVersion(environment.PHPMyAdmin),
	}
	if strings.TrimSpace(tools.PHP) == "" &&
		strings.TrimSpace(tools.Composer) == "" &&
		strings.TrimSpace(tools.NodeJS) == "" &&
		strings.TrimSpace(tools.Mago) == "" &&
		strings.TrimSpace(tools.Nginx) == "" &&
		strings.TrimSpace(tools.MySQL) == "" &&
		strings.TrimSpace(tools.MariaDB) == "" &&
		strings.TrimSpace(tools.SQLite) == "" &&
		strings.TrimSpace(tools.Mailpit) == "" &&
		strings.TrimSpace(tools.PHPMyAdmin) == "" {
		return nil
	}

	return tools
}

func testSettingsFromEnvironment(environment testEnvironmentConfig) *testSettingsConfig {
	settings := &testSettingsConfig{
		Mailpit:    testMailpitSettingsFromEnvironment(environment),
		PHPMyAdmin: testPHPMyAdminSettingsFromEnvironment(environment),
	}
	if settings.Mailpit == nil && settings.PHPMyAdmin == nil {
		return nil
	}

	return settings
}

func testMailpitSettingsFromEnvironment(environment testEnvironmentConfig) *testMailpitSettingsConfig {
	if environment.Mailpit == nil || (environment.Mailpit.SMTPPort == 0 && environment.Mailpit.UIPort == 0) {
		return nil
	}

	return &testMailpitSettingsConfig{
		SMTPPort: environment.Mailpit.SMTPPort,
		UIPort:   environment.Mailpit.UIPort,
	}
}

func testPHPMyAdminSettingsFromEnvironment(environment testEnvironmentConfig) *testPHPMyAdminSettingsConfig {
	if environment.PHPMyAdmin == nil || environment.PHPMyAdmin.Port == 0 {
		return nil
	}

	return &testPHPMyAdminSettingsConfig{Port: environment.PHPMyAdmin.Port}
}

func testApplySettings(environment testEnvironmentConfig, settings *testSettingsConfig) testEnvironmentConfig {
	if settings == nil {
		return environment
	}
	if settings.Mailpit != nil {
		if environment.Mailpit == nil {
			environment.Mailpit = &testMailpitConfig{}
		}
		environment.Mailpit.SMTPPort = settings.Mailpit.SMTPPort
		environment.Mailpit.UIPort = settings.Mailpit.UIPort
	}
	if settings.PHPMyAdmin != nil {
		if environment.PHPMyAdmin == nil {
			environment.PHPMyAdmin = &testPHPMyAdminConfig{}
		}
		environment.PHPMyAdmin.Port = settings.PHPMyAdmin.Port
	}

	return environment
}

func testMailpitVersion(mailpit *testMailpitConfig) string {
	if mailpit == nil {
		return ""
	}

	return strings.TrimSpace(mailpit.Version)
}

func testPHPMyAdminVersion(phpMyAdmin *testPHPMyAdminConfig) string {
	if phpMyAdmin == nil {
		return ""
	}

	return strings.TrimSpace(phpMyAdmin.Version)
}

func testDatabaseToolVersion(environment testEnvironmentConfig, engine string) string {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "mysql":
		if strings.TrimSpace(environment.MySQL) != "" {
			return environment.MySQL
		}
	case "mariadb":
		if strings.TrimSpace(environment.MariaDB) != "" {
			return environment.MariaDB
		}
	default:
		return ""
	}
	if environment.Database == nil || !strings.EqualFold(environment.Database.Engine, engine) {
		return ""
	}

	return environment.Database.Version
}

func testDatabaseRuntimeFromEnvironment(environment testEnvironmentConfig) *testDatabaseConfig {
	if environment.Database == nil {
		return nil
	}

	runtime := &testDatabaseConfig{Engine: environment.Database.Engine, Port: environment.Database.Port}
	if strings.TrimSpace(runtime.Engine) == "" && runtime.Port == 0 {
		return nil
	}

	return runtime
}

func testDatabaseFromTools(tools *testToolsConfig, database *testDatabaseConfig) *testDatabaseConfig {
	if tools == nil {
		return database
	}

	merged := &testDatabaseConfig{}
	if database != nil {
		*merged = *database
	}
	if strings.TrimSpace(merged.Engine) == "" {
		switch {
		case strings.TrimSpace(tools.MySQL) != "" && strings.TrimSpace(tools.MariaDB) == "":
			merged.Engine = "mysql"
		case strings.TrimSpace(tools.MariaDB) != "" && strings.TrimSpace(tools.MySQL) == "":
			merged.Engine = "mariadb"
		}
	}
	switch strings.ToLower(strings.TrimSpace(merged.Engine)) {
	case "mysql":
		if strings.TrimSpace(merged.Version) == "" {
			merged.Version = tools.MySQL
		}
	case "mariadb":
		if strings.TrimSpace(merged.Version) == "" {
			merged.Version = tools.MariaDB
		}
	}
	if strings.TrimSpace(merged.Engine) == "" && strings.TrimSpace(merged.Version) == "" && merged.Port == 0 {
		return nil
	}

	return merged
}

func testPopulateDatabaseToolVersion(environment testEnvironmentConfig) testEnvironmentConfig {
	if environment.Database == nil || strings.TrimSpace(environment.Database.Version) == "" {
		return environment
	}

	switch strings.ToLower(strings.TrimSpace(environment.Database.Engine)) {
	case "mysql":
		if strings.TrimSpace(environment.MySQL) == "" {
			environment.MySQL = environment.Database.Version
		}
	case "mariadb":
		if strings.TrimSpace(environment.MariaDB) == "" {
			environment.MariaDB = environment.Database.Version
		}
	}

	return environment
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
