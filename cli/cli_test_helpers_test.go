package cli

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
)

type testConfigFile struct {
	Version      int
	Root         string
	Environments map[string]testEnvironmentConfig
}

type testEnvironmentConfig struct {
	Framework     string                `yaml:"framework,omitempty"`
	PHP           string                `yaml:"php"`
	PHPZTS        string                `yaml:"php-zts,omitempty"`
	FrankenPHP    string                `yaml:"frankenphp,omitempty"`
	Composer      string                `yaml:"composer"`
	PIE           string                `yaml:"pie,omitempty"`
	NodeJS        string                `yaml:"nodejs,omitempty"`
	Mago          string                `yaml:"mago,omitempty"`
	Nginx         string                `yaml:"nginx,omitempty"`
	MySQL         string                `yaml:"mysql,omitempty"`
	MariaDB       string                `yaml:"mariadb,omitempty"`
	PostgreSQL    string                `yaml:"postgresql,omitempty"`
	SQLite        string                `yaml:"sqlite,omitempty"`
	PHPMyAdmin    *testPHPMyAdminConfig `yaml:"phpmyadmin,omitempty"`
	Docroot       string                `yaml:"docroot,omitempty"`
	HTTPS         bool                  `yaml:"https,omitempty"`
	EnvFile       string                `yaml:"env-file,omitempty"`
	EnvVars       map[string]string     `yaml:"env-vars,omitempty"`
	Database      *testDatabaseConfig   `yaml:"database,omitempty"`
	Mailpit       *testMailpitConfig    `yaml:"mailpit,omitempty"`
	PHPExtensions map[string]bool       `yaml:"php-extensions,omitempty"`
	OPcachePreset string                `yaml:"opcache-preset,omitempty"`
	OPcacheConfig map[string]string     `yaml:"opcache-config,omitempty"`
	Server        *testServerConfig     `yaml:"server,omitempty"`
}

func chdirTest(t *testing.T, dir string) {
	t.Helper()

	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%s) error = %v", dir, err)
	}
}

func runTestConfigValue(t *testing.T, stdout, stderr *bytes.Buffer, root, environmentName, key, value string) {
	t.Helper()

	args := []string{"--root", root, "config"}
	if strings.TrimSpace(environmentName) != "" {
		args = append(args, "--env", environmentName)
	}
	args = append(args, key, value)
	if code := Run(stdout, stderr, args); code != 0 {
		t.Fatalf("Run(config %s %s) code = %d, stderr = %q", key, value, code, stderr.String())
	}
}

func runTestMySQLConfig(t *testing.T, stdout, stderr *bytes.Buffer, root, environmentName, version string, port int) {
	t.Helper()

	runTestConfigValue(t, stdout, stderr, root, environmentName, "tools.mysql", version)
	runTestConfigValue(t, stdout, stderr, root, environmentName, "database.engine", "mysql")
	if port != 0 {
		runTestConfigValue(t, stdout, stderr, root, environmentName, "database.port", strconv.Itoa(port))
	}
}

type testDatabaseConfig struct {
	Engine  string `yaml:"engine,omitempty"`
	Version string `yaml:"version,omitempty"`
	Port    int    `yaml:"port,omitempty"`
}

type testServerConfig struct {
	Type     string `yaml:"type,omitempty"`
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
	Framework     string              `yaml:"framework,omitempty"`
	Tools         *testToolsConfig    `yaml:"tools,omitempty"`
	Settings      *testSettingsConfig `yaml:"settings,omitempty"`
	Docroot       string              `yaml:"docroot,omitempty"`
	HTTPS         bool                `yaml:"https,omitempty"`
	EnvFile       string              `yaml:"env-file,omitempty"`
	EnvVars       map[string]string   `yaml:"env-vars,omitempty"`
	Database      *testDatabaseConfig `yaml:"database,omitempty"`
	PHPExtensions map[string]bool     `yaml:"php-extensions,omitempty"`
	OPcachePreset string              `yaml:"opcache-preset,omitempty"`
	OPcacheConfig map[string]string   `yaml:"opcache-config,omitempty"`
	Server        *testServerConfig   `yaml:"server,omitempty"`
}

type testEnvironmentConfigData struct {
	Framework     string              `yaml:"framework,omitempty"`
	Tools         *testToolsConfig    `yaml:"tools,omitempty"`
	Settings      *testSettingsConfig `yaml:"settings,omitempty"`
	Docroot       string              `yaml:"docroot,omitempty"`
	HTTPS         bool                `yaml:"https,omitempty"`
	EnvFile       string              `yaml:"env-file,omitempty"`
	EnvVars       map[string]string   `yaml:"env-vars,omitempty"`
	Database      *testDatabaseConfig `yaml:"database,omitempty"`
	PHPExtensions map[string]bool     `yaml:"php-extensions,omitempty"`
	OPcachePreset string              `yaml:"opcache-preset,omitempty"`
	OPcacheConfig map[string]string   `yaml:"opcache-config,omitempty"`
	Server        *testServerConfig   `yaml:"server,omitempty"`
}

type testToolsConfig struct {
	PHP        string `yaml:"php,omitempty"`
	PHPZTS     string `yaml:"php-zts,omitempty"`
	FrankenPHP string `yaml:"frankenphp,omitempty"`
	Composer   string `yaml:"composer,omitempty"`
	PIE        string `yaml:"pie,omitempty"`
	NodeJS     string `yaml:"nodejs,omitempty"`
	Mago       string `yaml:"mago,omitempty"`
	Nginx      string `yaml:"nginx,omitempty"`
	MySQL      string `yaml:"mysql,omitempty"`
	MariaDB    string `yaml:"mariadb,omitempty"`
	PostgreSQL string `yaml:"postgresql,omitempty"`
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
		projectConfig.Framework = defaultEnvironment.Framework
		projectConfig.Tools = testToolsFromEnvironment(defaultEnvironment)
		projectConfig.Settings = testSettingsFromEnvironment(defaultEnvironment)
		projectConfig.Docroot = defaultEnvironment.Docroot
		projectConfig.HTTPS = defaultEnvironment.HTTPS
		projectConfig.EnvFile = defaultEnvironment.EnvFile
		projectConfig.EnvVars = defaultEnvironment.EnvVars
		projectConfig.Database = testDatabaseRuntimeFromEnvironment(defaultEnvironment)
		projectConfig.PHPExtensions = defaultEnvironment.PHPExtensions
		projectConfig.OPcachePreset = defaultEnvironment.OPcachePreset
		projectConfig.OPcacheConfig = defaultEnvironment.OPcacheConfig
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
		projectConfig.Framework = environment.Framework
		projectConfig.Settings = testSettingsFromEnvironment(environment)
		projectConfig.Docroot = environment.Docroot
		projectConfig.HTTPS = environment.HTTPS
		projectConfig.EnvFile = environment.EnvFile
		projectConfig.EnvVars = environment.EnvVars
		projectConfig.Database = testDatabaseRuntimeFromEnvironment(environment)
		projectConfig.PHPExtensions = environment.PHPExtensions
		projectConfig.OPcachePreset = environment.OPcachePreset
		projectConfig.OPcacheConfig = environment.OPcacheConfig
		projectConfig.Server = environment.Server
		writeTestYAML(t, filepath.Join(projectDir, "polka.yaml"), projectConfig)
		return
	}

	writeTestYAML(t, testEnvironmentConfigPath(projectDir, name), testEnvironmentConfigData{
		Framework:     environment.Framework,
		Tools:         testToolsFromEnvironment(environment),
		Settings:      testSettingsFromEnvironment(environment),
		Docroot:       environment.Docroot,
		HTTPS:         environment.HTTPS,
		EnvFile:       environment.EnvFile,
		EnvVars:       environment.EnvVars,
		Database:      testDatabaseRuntimeFromEnvironment(environment),
		PHPExtensions: environment.PHPExtensions,
		OPcachePreset: environment.OPcachePreset,
		OPcacheConfig: environment.OPcacheConfig,
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
	return testEnvironmentFromParts(projectConfig.Framework, projectConfig.Tools, projectConfig.Settings, projectConfig.Docroot, projectConfig.HTTPS, projectConfig.EnvFile, projectConfig.EnvVars, projectConfig.Database, projectConfig.PHPExtensions, projectConfig.OPcachePreset, projectConfig.OPcacheConfig, projectConfig.Server)
}

func testEnvironmentFromEnvironmentConfig(environmentConfig testEnvironmentConfigData) testEnvironmentConfig {
	return testEnvironmentFromParts(environmentConfig.Framework, environmentConfig.Tools, environmentConfig.Settings, environmentConfig.Docroot, environmentConfig.HTTPS, environmentConfig.EnvFile, environmentConfig.EnvVars, environmentConfig.Database, environmentConfig.PHPExtensions, environmentConfig.OPcachePreset, environmentConfig.OPcacheConfig, environmentConfig.Server)
}

func testEnvironmentFromParts(framework string, tools *testToolsConfig, settings *testSettingsConfig, docroot string, https bool, envFile string, envVars map[string]string, database *testDatabaseConfig, phpExtensions map[string]bool, opcachePreset string, opcacheConfig map[string]string, server *testServerConfig) testEnvironmentConfig {
	normalizedOPcachePreset := strings.ToLower(strings.TrimSpace(opcachePreset))
	if normalizedOPcachePreset == "none" {
		normalizedOPcachePreset = ""
	}
	environment := testEnvironmentConfig{
		Framework:     strings.ToLower(strings.TrimSpace(framework)),
		Docroot:       docroot,
		HTTPS:         https,
		EnvFile:       envFile,
		EnvVars:       envVars,
		Database:      database,
		PHPExtensions: phpExtensions,
		OPcachePreset: normalizedOPcachePreset,
		OPcacheConfig: testNormalizeOPcacheConfig(opcacheConfig),
		Server:        server,
	}
	if tools != nil {
		environment.PHP = tools.PHP
		environment.PHPZTS = tools.PHPZTS
		environment.FrankenPHP = tools.FrankenPHP
		environment.Composer = tools.Composer
		environment.PIE = tools.PIE
		environment.NodeJS = tools.NodeJS
		environment.Mago = tools.Mago
		environment.Nginx = tools.Nginx
		environment.MySQL = tools.MySQL
		environment.MariaDB = tools.MariaDB
		environment.PostgreSQL = tools.PostgreSQL
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

func testNormalizeOPcacheConfig(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	normalized := make(map[string]string, len(values))
	for key, value := range values {
		normalized[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}

	return normalized
}

func testToolsFromEnvironment(environment testEnvironmentConfig) *testToolsConfig {
	tools := &testToolsConfig{
		PHP:        environment.PHP,
		PHPZTS:     environment.PHPZTS,
		FrankenPHP: environment.FrankenPHP,
		Composer:   environment.Composer,
		PIE:        environment.PIE,
		NodeJS:     environment.NodeJS,
		Mago:       environment.Mago,
		Nginx:      environment.Nginx,
		MySQL:      testDatabaseToolVersion(environment, "mysql"),
		MariaDB:    testDatabaseToolVersion(environment, "mariadb"),
		PostgreSQL: testDatabaseToolVersion(environment, "postgresql"),
		SQLite:     environment.SQLite,
		Mailpit:    testMailpitVersion(environment.Mailpit),
		PHPMyAdmin: testPHPMyAdminVersion(environment.PHPMyAdmin),
	}
	if strings.TrimSpace(tools.PHP) == "" &&
		strings.TrimSpace(tools.PHPZTS) == "" &&
		strings.TrimSpace(tools.FrankenPHP) == "" &&
		strings.TrimSpace(tools.Composer) == "" &&
		strings.TrimSpace(tools.PIE) == "" &&
		strings.TrimSpace(tools.NodeJS) == "" &&
		strings.TrimSpace(tools.Mago) == "" &&
		strings.TrimSpace(tools.Nginx) == "" &&
		strings.TrimSpace(tools.MySQL) == "" &&
		strings.TrimSpace(tools.MariaDB) == "" &&
		strings.TrimSpace(tools.PostgreSQL) == "" &&
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
	case "postgresql":
		if strings.TrimSpace(environment.PostgreSQL) != "" {
			return environment.PostgreSQL
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
		case strings.TrimSpace(tools.MySQL) != "" && strings.TrimSpace(tools.MariaDB) == "" && strings.TrimSpace(tools.PostgreSQL) == "":
			merged.Engine = "mysql"
		case strings.TrimSpace(tools.MariaDB) != "" && strings.TrimSpace(tools.MySQL) == "" && strings.TrimSpace(tools.PostgreSQL) == "":
			merged.Engine = "mariadb"
		case strings.TrimSpace(tools.PostgreSQL) != "" && strings.TrimSpace(tools.MySQL) == "" && strings.TrimSpace(tools.MariaDB) == "":
			merged.Engine = "postgresql"
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
	case "postgresql":
		if strings.TrimSpace(merged.Version) == "" {
			merged.Version = tools.PostgreSQL
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
	case "postgresql":
		if strings.TrimSpace(environment.PostgreSQL) == "" {
			environment.PostgreSQL = environment.Database.Version
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

func writeCachedPHPCABundle(t *testing.T, root, version string) {
	t.Helper()

	path := filepath.Join(root, "php", version, "extras", "ssl", "cacert.pem")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
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

func cachedComposerExecutablePath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "composer", version, "bin", "composer.cmd")
	}

	return filepath.Join(root, "composer", version, "bin", "composer")
}

func cachedPIEPath(root, version string) string {
	return filepath.Join(root, "pie", version, "bin", "pie.phar")
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

func writeCachedPHP(t *testing.T, cacheDir, version string, script []byte) string {
	t.Helper()

	phpPath := cachedPHPPath(cacheDir, version)
	files := map[string][]byte{
		cachedToolRelativePath(t, cacheDir, "php", version, phpPath): script,
		"extras/ssl/cacert.pem": []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"),
	}

	return writeCachedArchivePayload(t, cacheDir, "php", version, files)
}

func writeCachedFrankenPHP(t *testing.T, cacheDir, version string) string {
	t.Helper()

	files := map[string][]byte{}
	if runtime.GOOS == "windows" {
		files["frankenphp.exe"] = []byte("placeholder\n")
		files["php.cmd"] = []byte("@echo off\r\necho fake-frankenphp-php %*\r\n")
	} else {
		files["frankenphp"] = []byte("#!/bin/sh\nif [ \"$1\" = \"php-cli\" ]; then\n  shift\n  printf 'fake-frankenphp-php %s\\n' \"$*\"\n  exit 0\nfi\nexit 1\n")
	}

	return writeCachedArchivePayload(t, cacheDir, "frankenphp", version, files)
}

func writeCachedComposer(t *testing.T, cacheDir, version string, data []byte) string {
	t.Helper()

	return writeCachedFilePayload(t, cacheDir, "composer", version, "composer.phar", "bin/composer.phar", data)
}

func writeCachedComposerExecutable(t *testing.T, cacheDir, version string, data []byte) string {
	t.Helper()

	installPath := cachedToolRelativePath(t, cacheDir, "composer", version, cachedComposerExecutablePath(cacheDir, version))
	return writeCachedFilePayload(t, cacheDir, "composer", version, filepath.Base(installPath), installPath, data)
}

func writeCachedPIE(t *testing.T, cacheDir, version string, data []byte) string {
	t.Helper()

	return writeCachedFilePayload(t, cacheDir, "pie", version, "pie.phar", "bin/pie.phar", data)
}

func writeCachedDatabaseTool(t *testing.T, cacheDir, tool, version string, includeDump, includeServer, includeAdmin bool) string {
	t.Helper()

	files := map[string][]byte{
		cachedToolRelativePath(t, cacheDir, tool, version, cachedDatabasePath(cacheDir, tool, version)): fakeDatabaseScript(tool),
	}
	if includeDump {
		files[cachedToolRelativePath(t, cacheDir, tool, version, cachedDatabaseDumpPath(cacheDir, tool, version))] = fakeDatabaseDumpScript()
	}
	if includeServer {
		serverName := "mysqld"
		if tool == "mariadb" {
			serverName = "mariadbd"
		}
		files[cachedToolRelativePath(t, cacheDir, tool, version, cachedDatabaseServerPath(cacheDir, tool, version))] = fakeDatabaseScript(serverName)
	}
	if includeAdmin {
		adminName := "mysqladmin"
		if tool == "mariadb" {
			adminName = "mariadb-admin"
		}
		files[cachedToolRelativePath(t, cacheDir, tool, version, cachedDatabaseAdminPath(cacheDir, tool, version))] = fakeDatabaseScript(adminName)
	}

	return writeCachedArchivePayload(t, cacheDir, tool, version, files)
}

type testCacheMetadata struct {
	SchemaVersion int                             `json:"schemaVersion"`
	Tool          string                          `json:"tool"`
	Versions      map[string]testCacheVersionMeta `json:"versions"`
}

type testCacheVersionMeta struct {
	DownloadedVersion string    `json:"downloadedVersion"`
	PayloadKind       string    `json:"payloadKind"`
	PayloadPath       string    `json:"payloadPath"`
	FileName          string    `json:"fileName"`
	InstallPath       string    `json:"installPath,omitempty"`
	SourceURL         string    `json:"sourceUrl"`
	ArchiveFormat     string    `json:"archiveFormat,omitempty"`
	ChecksumAlgorithm string    `json:"checksumAlgorithm"`
	Checksum          string    `json:"checksum"`
	Size              int64     `json:"size"`
	DownloadedAt      time.Time `json:"downloadedAt"`
}

func writeCachedArchivePayload(t *testing.T, cacheDir, tool, version string, files map[string][]byte) string {
	t.Helper()

	fileName := tool + "-" + version + "-test.zip"
	payloadPath, payloadRelativePath := cachedPayloadPath(cacheDir, tool, version, fileName)
	archiveData := buildTestZipArchive(t, files)
	if err := os.MkdirAll(filepath.Dir(payloadPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(payloadPath), err)
	}
	if err := os.WriteFile(payloadPath, archiveData, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", payloadPath, err)
	}
	writeTestCacheMetadata(t, cacheDir, tool, version, testCacheVersionMeta{
		DownloadedVersion: version,
		PayloadKind:       "archive",
		PayloadPath:       payloadRelativePath,
		FileName:          fileName,
		SourceURL:         "https://example.test/" + fileName,
		ArchiveFormat:     "zip",
		ChecksumAlgorithm: "sha256",
		Checksum:          sha256Hex(archiveData),
		Size:              int64(len(archiveData)),
		DownloadedAt:      time.Now().UTC(),
	})

	return payloadPath
}

func writeCachedFilePayload(t *testing.T, cacheDir, tool, version, fileName, installPath string, data []byte) string {
	t.Helper()

	payloadPath, payloadRelativePath := cachedPayloadPath(cacheDir, tool, version, fileName)
	if err := os.MkdirAll(filepath.Dir(payloadPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(payloadPath), err)
	}
	if err := os.WriteFile(payloadPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", payloadPath, err)
	}
	writeTestCacheMetadata(t, cacheDir, tool, version, testCacheVersionMeta{
		DownloadedVersion: version,
		PayloadKind:       "file",
		PayloadPath:       payloadRelativePath,
		FileName:          fileName,
		InstallPath:       filepath.ToSlash(installPath),
		SourceURL:         "https://example.test/" + fileName,
		ChecksumAlgorithm: "sha256",
		Checksum:          sha256Hex(data),
		Size:              int64(len(data)),
		DownloadedAt:      time.Now().UTC(),
	})

	return payloadPath
}

func cachedPayloadPath(cacheDir, tool, version, fileName string) (string, string) {
	relativePath := filepath.ToSlash(filepath.Join(version, fileName))
	return filepath.Join(cacheDir, tool, filepath.FromSlash(relativePath)), relativePath
}

func cachedToolRelativePath(t *testing.T, cacheDir, tool, version, path string) string {
	t.Helper()

	relativePath, err := filepath.Rel(filepath.Join(cacheDir, tool, version), path)
	if err != nil {
		t.Fatalf("Rel(%q) error = %v", path, err)
	}

	return filepath.ToSlash(relativePath)
}

func writeTestCacheMetadata(t *testing.T, cacheDir, tool, version string, entry testCacheVersionMeta) {
	t.Helper()

	metadataPath := filepath.Join(cacheDir, tool, "metadata.json")
	metadata := testCacheMetadata{
		SchemaVersion: 1,
		Tool:          tool,
		Versions:      map[string]testCacheVersionMeta{},
	}
	data, err := os.ReadFile(metadataPath)
	if err == nil {
		if err := json.Unmarshal(data, &metadata); err != nil {
			t.Fatalf("Unmarshal(%q) error = %v", metadataPath, err)
		}
	}
	if metadata.Versions == nil {
		metadata.Versions = map[string]testCacheVersionMeta{}
	}
	metadata.SchemaVersion = 1
	metadata.Tool = tool
	metadata.Versions[version] = entry

	data, err = json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(cache metadata) error = %v", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(metadataPath), err)
	}
	if err := os.WriteFile(metadataPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", metadataPath, err)
	}
}

func buildTestZipArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()

	buffer := &bytes.Buffer{}
	writer := zip.NewWriter(buffer)
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, filepath.ToSlash(path))
	}
	sort.Strings(paths)
	for _, path := range paths {
		header := &zip.FileHeader{Name: path}
		header.SetMode(0o755)
		fileWriter, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("CreateHeader(%q) error = %v", path, err)
		}
		if _, err := fileWriter.Write(files[path]); err != nil {
			t.Fatalf("Write(%q) error = %v", path, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close(zip writer) error = %v", err)
	}

	return buffer.Bytes()
}

func sha256Hex(data []byte) string {
	checksum := sha256.Sum256(data)
	return fmt.Sprintf("%x", checksum[:])
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

func fakeCakePHPCreateProjectComposerScript() []byte {
	appLocal := strings.Join([]string{
		"<?php",
		"declare(strict_types=1);",
		"",
		"return [",
		"    'Datasources' => [",
		"        'default' => [",
		"            'driver' => 'Cake\\Database\\Driver\\Sqlite',",
		"            'database' => 'tmp/test.sqlite',",
		"        ],",
		"    ],",
		"];",
		"",
	}, "\n")
	if runtime.GOOS == "windows" {
		return []byte(strings.Join([]string{
			"@echo off",
			"set \"dir=%POLKA_TEST_CREATE_PROJECT_DIR%\"",
			"mkdir \"%dir%\\config\" >nul 2>nul",
			"mkdir \"%dir%\\webroot\" >nul 2>nul",
			"> \"%dir%\\config\\app_local.php\" echo ^<?php",
			">> \"%dir%\\config\\app_local.php\" echo declare^(strict_types=1^);",
			">> \"%dir%\\config\\app_local.php\" echo(",
			">> \"%dir%\\config\\app_local.php\" echo return [",
			">> \"%dir%\\config\\app_local.php\" echo     'Datasources' =^> [",
			">> \"%dir%\\config\\app_local.php\" echo         'default' =^> [",
			">> \"%dir%\\config\\app_local.php\" echo             'driver' =^> 'Cake\\Database\\Driver\\Sqlite',",
			">> \"%dir%\\config\\app_local.php\" echo             'database' =^> 'tmp/test.sqlite',",
			">> \"%dir%\\config\\app_local.php\" echo         ],",
			">> \"%dir%\\config\\app_local.php\" echo     ],",
			">> \"%dir%\\config\\app_local.php\" echo ];",
			"echo fake-composer %*",
			"",
		}, "\r\n"))
	}

	return []byte("#!/usr/bin/env sh\nset -eu\ndir=${POLKA_TEST_CREATE_PROJECT_DIR:?}\nmkdir -p \"$dir/config\" \"$dir/webroot\"\ncat > \"$dir/config/app_local.php\" <<'EOF'\n" + appLocal + "EOF\nprintf 'fake-composer %s\n' \"$*\"\n")
}

func assertCakePHPAppLocalUsesManagedDatabase(t *testing.T, path, port string) {
	t.Helper()

	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(app_local.php) error = %v", err)
	}
	text := string(updated)
	for _, expected := range []string{
		"'default' => [",
		"'test' => [",
		"'debug_kit' => [",
		"'className' => 'Cake\\\\Database\\\\Connection',",
		"'driver' => 'Cake\\\\Database\\\\Driver\\\\Mysql',",
		"'host' => '127.0.0.1',",
		"'port' => '" + port + "',",
		"'username' => 'polka',",
		"'database' => 'default',",
		"'encoding' => 'utf8mb4',",
		"'url' => null,",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("app_local.php = %q, want %q", text, expected)
		}
	}
	if strings.Contains(text, "Sqlite") || strings.Contains(text, "sqlite://") {
		t.Fatalf("app_local.php = %q, want SQLite removed from generated datasources", text)
	}
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
