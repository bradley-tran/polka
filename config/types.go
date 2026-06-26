package config

import (
	"fmt"
	"strings"
)

const (
	OPcachePresetNone       = "none"
	OPcachePresetDev        = "dev"
	OPcachePresetProduction = "production"
	ServerTypePHP           = "php"
	ServerTypeNginx         = "nginx"
	ServerTypeApache        = "apache"
	ServerTypeFrankenPHP    = "frankenphp"
)

// ToolsConfig is the YAML shape for managed tool version labels inside an environment file.
type ToolsConfig struct {
	PHPVersion        string `yaml:"php,omitempty"`
	PHPZTSVersion     string `yaml:"php-zts,omitempty"`
	FrankenPHPVersion string `yaml:"frankenphp,omitempty"`
	ComposerVersion   string `yaml:"composer,omitempty"`
	PIEVersion        string `yaml:"pie,omitempty"`
	NodeJSVersion     string `yaml:"nodejs,omitempty"`
	MagoVersion       string `yaml:"mago,omitempty"`
	NginxVersion      string `yaml:"nginx,omitempty"`
	ApacheVersion     string `yaml:"apache,omitempty"`
	MySQLVersion      string `yaml:"mysql,omitempty"`
	MariaDBVersion    string `yaml:"mariadb,omitempty"`
	PostgreSQLVersion string `yaml:"postgresql,omitempty"`
	SQLiteVersion     string `yaml:"sqlite,omitempty"`
	MailpitVersion    string `yaml:"mailpit,omitempty"`
	PHPMyAdminVersion string `yaml:"phpmyadmin,omitempty"`
}

// SettingsConfig is the YAML shape for versionless per-tool settings.
type SettingsConfig struct {
	Mailpit    *MailpitSettingsConfig    `yaml:"mailpit,omitempty"`
	PHPMyAdmin *PHPMyAdminSettingsConfig `yaml:"phpmyadmin,omitempty"`
}

// MailpitSettingsConfig is the YAML shape for Mailpit runtime settings.
type MailpitSettingsConfig struct {
	SMTPPort int `yaml:"smtp-port,omitempty"`
	UIPort   int `yaml:"ui-port,omitempty"`
}

// PHPMyAdminSettingsConfig is the YAML shape for phpMyAdmin runtime settings.
type PHPMyAdminSettingsConfig struct {
	Port int `yaml:"port,omitempty"`
}

// ProjectFile is the YAML shape of polka.yaml, which also defines the default environment.
type ProjectFile struct {
	Version       int               `yaml:"version"`
	Root          string            `yaml:"root"`
	Framework     string            `yaml:"framework,omitempty"`
	Tools         *ToolsConfig      `yaml:"tools,omitempty"`
	Settings      *SettingsConfig   `yaml:"settings,omitempty"`
	Docroot       string            `yaml:"docroot,omitempty"`
	HTTPS         bool              `yaml:"https,omitempty"`
	EnvFile       string            `yaml:"env-file,omitempty"`
	EnvVars       map[string]string `yaml:"env-vars,omitempty"`
	Database      *DatabaseConfig   `yaml:"database,omitempty"`
	PHPExtensions map[string]bool   `yaml:"php-extensions,omitempty"`
	OPcachePreset string            `yaml:"opcache-preset,omitempty"`
	OPcacheConfig map[string]any    `yaml:"opcache-config,omitempty"`
	Server        *ServerConfig     `yaml:"server,omitempty"`
}

// EnvironmentFile is the YAML shape of polka.<name>.yaml.
type EnvironmentFile struct {
	Framework     string            `yaml:"framework,omitempty"`
	Tools         *ToolsConfig      `yaml:"tools,omitempty"`
	Settings      *SettingsConfig   `yaml:"settings,omitempty"`
	Docroot       string            `yaml:"docroot,omitempty"`
	HTTPS         bool              `yaml:"https,omitempty"`
	EnvFile       string            `yaml:"env-file,omitempty"`
	EnvVars       map[string]string `yaml:"env-vars,omitempty"`
	Database      *DatabaseConfig   `yaml:"database,omitempty"`
	PHPExtensions map[string]bool   `yaml:"php-extensions,omitempty"`
	OPcachePreset string            `yaml:"opcache-preset,omitempty"`
	OPcacheConfig map[string]any    `yaml:"opcache-config,omitempty"`
	Server        *ServerConfig     `yaml:"server,omitempty"`
}

type Environment struct {
	Name              string            `yaml:"-"`
	Framework         string            `yaml:"framework,omitempty"`
	PHPVersion        string            `yaml:"php,omitempty"`
	PHPZTSVersion     string            `yaml:"php-zts,omitempty"`
	FrankenPHPVersion string            `yaml:"frankenphp,omitempty"`
	ComposerVersion   string            `yaml:"composer,omitempty"`
	PIEVersion        string            `yaml:"pie,omitempty"`
	NodeJSVersion     string            `yaml:"nodejs,omitempty"`
	MagoVersion       string            `yaml:"mago,omitempty"`
	NginxVersion      string            `yaml:"nginx,omitempty"`
	ApacheVersion     string            `yaml:"apache,omitempty"`
	MySQLVersion      string            `yaml:"mysql,omitempty"`
	MariaDBVersion    string            `yaml:"mariadb,omitempty"`
	PostgreSQLVersion string            `yaml:"postgresql,omitempty"`
	SQLiteVersion     string            `yaml:"sqlite,omitempty"`
	Docroot           string            `yaml:"docroot,omitempty"`
	HTTPS             bool              `yaml:"https,omitempty"`
	EnvFile           string            `yaml:"env-file,omitempty"`
	EnvVars           map[string]string `yaml:"env-vars,omitempty"`
	Database          *DatabaseConfig   `yaml:"database,omitempty"`
	Mailpit           *MailpitConfig    `yaml:"mailpit,omitempty"`
	PHPMyAdmin        *PHPMyAdminConfig `yaml:"phpmyadmin,omitempty"`
	PHPExtensions     map[string]bool   `yaml:"php-extensions,omitempty"`
	OPcachePreset     string            `yaml:"opcache-preset,omitempty"`
	OPcacheConfig     map[string]string `yaml:"opcache-config,omitempty"`
	Server            *ServerConfig     `yaml:"server,omitempty"`
}

type DatabaseConfig struct {
	Engine  string `yaml:"engine,omitempty"`
	Version string `yaml:"version,omitempty"`
	Port    int    `yaml:"port,omitempty"`
}

type MailpitConfig struct {
	Version  string `yaml:"version,omitempty"`
	SMTPPort int    `yaml:"smtp-port,omitempty"`
	UIPort   int    `yaml:"ui-port,omitempty"`
	HTTPS    bool   `yaml:"https,omitempty"`
}

type PHPMyAdminConfig struct {
	Version string `yaml:"version,omitempty"`
	Port    int    `yaml:"port,omitempty"`
	HTTPS   bool   `yaml:"https,omitempty"`
}

type ServerConfig struct {
	Type     string `yaml:"type,omitempty"`
	Hostname string `yaml:"hostname,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	HTTPS    bool   `yaml:"https,omitempty"`
}

type Config struct {
	Version      int                    `yaml:"version"`
	Root         string                 `yaml:"root"`
	Environments map[string]Environment `yaml:"-"`
}

// PrimaryPHPTool returns the configured PHP runtime tool and version. The
// caller must validate mutual exclusion when both runtime fields are present.
func PrimaryPHPTool(environment Environment) (string, string) {
	if version := strings.TrimSpace(environment.PHPZTSVersion); version != "" {
		return "php-zts", version
	}
	if version := strings.TrimSpace(environment.PHPVersion); version != "" {
		return "php", version
	}

	return "", ""
}

// PrimaryPHPVersion returns the version of the configured primary PHP runtime.
func PrimaryPHPVersion(environment Environment) string {
	_, version := PrimaryPHPTool(environment)
	return version
}

// PHPCLIProvider returns the configured provider for the php command. A
// standalone PHP runtime takes precedence over FrankenPHP's bundled CLI.
func PHPCLIProvider(environment Environment) (string, string) {
	if tool, version := PrimaryPHPTool(environment); tool != "" {
		return tool, version
	}
	if version := strings.TrimSpace(environment.FrankenPHPVersion); version != "" {
		return ServerTypeFrankenPHP, version
	}

	return "", ""
}

// HasPHPCLI reports whether the environment configures any php command provider.
func HasPHPCLI(environment Environment) bool {
	tool, _ := PHPCLIProvider(environment)
	return tool != ""
}

// ProjectFileToEnvironment converts polka.yaml data into the internal environment model.
func ProjectFileToEnvironment(name string, file ProjectFile) Environment {
	return environmentFromFileParts(
		name,
		file.Framework,
		file.Tools,
		file.Settings,
		file.Docroot,
		file.HTTPS,
		file.EnvFile,
		file.EnvVars,
		file.Database,
		file.PHPExtensions,
		file.OPcachePreset,
		file.OPcacheConfig,
		file.Server,
	)
}

// ProjectFileFromEnvironment converts the default environment into polka.yaml data.
func ProjectFileFromEnvironment(version int, root string, environment Environment) ProjectFile {
	file := ProjectFile{
		Version:       version,
		Root:          root,
		Framework:     strings.ToLower(strings.TrimSpace(environment.Framework)),
		Tools:         ToolsConfigFromEnvironment(environment),
		Settings:      SettingsConfigFromEnvironment(environment),
		Docroot:       environment.Docroot,
		HTTPS:         environment.HTTPS,
		EnvFile:       environment.EnvFile,
		EnvVars:       environment.EnvVars,
		Database:      DatabaseRuntimeConfigFromEnvironment(environment),
		PHPExtensions: environment.PHPExtensions,
		OPcachePreset: NormalizeOPcachePreset(environment.OPcachePreset),
		OPcacheConfig: OPcacheFileConfigFromEnvironment(environment),
		Server:        ServerConfigFromEnvironment(environment),
	}

	return file
}

// EnvironmentFileToEnvironment converts polka.<name>.yaml data into the internal environment model.
func EnvironmentFileToEnvironment(name string, file EnvironmentFile) Environment {
	return environmentFromFileParts(
		name,
		file.Framework,
		file.Tools,
		file.Settings,
		file.Docroot,
		file.HTTPS,
		file.EnvFile,
		file.EnvVars,
		file.Database,
		file.PHPExtensions,
		file.OPcachePreset,
		file.OPcacheConfig,
		file.Server,
	)
}

// EnvironmentFileFromEnvironment converts a named environment into polka.<name>.yaml data.
func EnvironmentFileFromEnvironment(environment Environment) EnvironmentFile {
	return EnvironmentFile{
		Framework:     strings.ToLower(strings.TrimSpace(environment.Framework)),
		Tools:         ToolsConfigFromEnvironment(environment),
		Settings:      SettingsConfigFromEnvironment(environment),
		Docroot:       environment.Docroot,
		HTTPS:         environment.HTTPS,
		EnvFile:       environment.EnvFile,
		EnvVars:       environment.EnvVars,
		Database:      DatabaseRuntimeConfigFromEnvironment(environment),
		PHPExtensions: environment.PHPExtensions,
		OPcachePreset: NormalizeOPcachePreset(environment.OPcachePreset),
		OPcacheConfig: OPcacheFileConfigFromEnvironment(environment),
		Server:        ServerConfigFromEnvironment(environment),
	}
}

// ToolsConfigFromEnvironment extracts managed tool settings from an environment.
func ToolsConfigFromEnvironment(environment Environment) *ToolsConfig {
	tools := &ToolsConfig{
		PHPVersion:        environment.PHPVersion,
		PHPZTSVersion:     environment.PHPZTSVersion,
		FrankenPHPVersion: environment.FrankenPHPVersion,
		ComposerVersion:   environment.ComposerVersion,
		PIEVersion:        environment.PIEVersion,
		NodeJSVersion:     environment.NodeJSVersion,
		MagoVersion:       environment.MagoVersion,
		NginxVersion:      environment.NginxVersion,
		ApacheVersion:     environment.ApacheVersion,
		MySQLVersion:      DatabaseToolVersion(environment, "mysql"),
		MariaDBVersion:    DatabaseToolVersion(environment, "mariadb"),
		PostgreSQLVersion: DatabaseToolVersion(environment, "postgresql"),
		SQLiteVersion:     environment.SQLiteVersion,
		MailpitVersion:    ToolVersionFromMailpitConfig(environment.Mailpit),
		PHPMyAdminVersion: ToolVersionFromPHPMyAdminConfig(environment.PHPMyAdmin),
	}
	if tools.IsZero() {
		return nil
	}

	return tools
}

// SettingsConfigFromEnvironment extracts versionless tool settings from an environment.
func SettingsConfigFromEnvironment(environment Environment) *SettingsConfig {
	settings := &SettingsConfig{
		Mailpit:    MailpitSettingsConfigFromEnvironment(environment),
		PHPMyAdmin: PHPMyAdminSettingsConfigFromEnvironment(environment),
	}
	if settings.IsZero() {
		return nil
	}

	return settings
}

// ToolVersionFromMailpitConfig extracts Mailpit's managed tool version label.
func ToolVersionFromMailpitConfig(mailpit *MailpitConfig) string {
	if mailpit == nil {
		return ""
	}

	return strings.TrimSpace(mailpit.Version)
}

// ToolVersionFromPHPMyAdminConfig extracts phpMyAdmin's managed tool version label.
func ToolVersionFromPHPMyAdminConfig(phpMyAdmin *PHPMyAdminConfig) string {
	if phpMyAdmin == nil {
		return ""
	}

	return strings.TrimSpace(phpMyAdmin.Version)
}

// MailpitSettingsConfigFromEnvironment extracts Mailpit settings that belong under settings.
func MailpitSettingsConfigFromEnvironment(environment Environment) *MailpitSettingsConfig {
	if environment.Mailpit == nil {
		return nil
	}

	mailpit := &MailpitSettingsConfig{
		SMTPPort: environment.Mailpit.SMTPPort,
		UIPort:   environment.Mailpit.UIPort,
	}
	if mailpit.SMTPPort == 0 && mailpit.UIPort == 0 {
		return nil
	}

	return mailpit
}

// PHPMyAdminSettingsConfigFromEnvironment extracts phpMyAdmin settings that belong under settings.
func PHPMyAdminSettingsConfigFromEnvironment(environment Environment) *PHPMyAdminSettingsConfig {
	if environment.PHPMyAdmin == nil {
		return nil
	}

	phpMyAdmin := &PHPMyAdminSettingsConfig{
		Port: environment.PHPMyAdmin.Port,
	}
	if phpMyAdmin.Port == 0 {
		return nil
	}

	return phpMyAdmin
}

// ServerConfigFromEnvironment extracts server settings that belong in YAML.
func ServerConfigFromEnvironment(environment Environment) *ServerConfig {
	if environment.Server == nil {
		return nil
	}

	server := &ServerConfig{
		Type:     strings.ToLower(strings.TrimSpace(environment.Server.Type)),
		Hostname: strings.TrimSpace(environment.Server.Hostname),
		Port:     environment.Server.Port,
	}
	if server.Type == "" && server.Hostname == "" && server.Port == 0 {
		return nil
	}

	return server
}

// IsZero reports whether no managed tool settings are configured.
func (tools ToolsConfig) IsZero() bool {
	return strings.TrimSpace(tools.PHPVersion) == "" &&
		strings.TrimSpace(tools.PHPZTSVersion) == "" &&
		strings.TrimSpace(tools.FrankenPHPVersion) == "" &&
		strings.TrimSpace(tools.ComposerVersion) == "" &&
		strings.TrimSpace(tools.PIEVersion) == "" &&
		strings.TrimSpace(tools.NodeJSVersion) == "" &&
		strings.TrimSpace(tools.MagoVersion) == "" &&
		strings.TrimSpace(tools.NginxVersion) == "" &&
		strings.TrimSpace(tools.ApacheVersion) == "" &&
		strings.TrimSpace(tools.MySQLVersion) == "" &&
		strings.TrimSpace(tools.MariaDBVersion) == "" &&
		strings.TrimSpace(tools.PostgreSQLVersion) == "" &&
		strings.TrimSpace(tools.SQLiteVersion) == "" &&
		strings.TrimSpace(tools.MailpitVersion) == "" &&
		strings.TrimSpace(tools.PHPMyAdminVersion) == ""
}

// IsZero reports whether no versionless tool settings are configured.
func (settings SettingsConfig) IsZero() bool {
	return settings.Mailpit == nil &&
		settings.PHPMyAdmin == nil
}

func environmentFromFileParts(name string, framework string, tools *ToolsConfig, settings *SettingsConfig, docroot string, https bool, envFile string, envVars map[string]string, database *DatabaseConfig, phpExtensions map[string]bool, opcachePreset string, opcacheConfig map[string]any, server *ServerConfig) Environment {
	environment := Environment{
		Name:          name,
		Framework:     strings.ToLower(strings.TrimSpace(framework)),
		Docroot:       docroot,
		HTTPS:         https,
		EnvFile:       envFile,
		EnvVars:       envVars,
		Database:      database,
		PHPExtensions: phpExtensions,
		OPcachePreset: opcachePreset,
		OPcacheConfig: NormalizeOPcacheConfigFromYAML(opcacheConfig),
		Server:        server,
	}
	if tools != nil {
		environment.PHPVersion = tools.PHPVersion
		environment.PHPZTSVersion = tools.PHPZTSVersion
		environment.FrankenPHPVersion = tools.FrankenPHPVersion
		environment.ComposerVersion = tools.ComposerVersion
		environment.PIEVersion = tools.PIEVersion
		environment.NodeJSVersion = tools.NodeJSVersion
		environment.MagoVersion = tools.MagoVersion
		environment.NginxVersion = tools.NginxVersion
		environment.ApacheVersion = tools.ApacheVersion
		environment.MySQLVersion = tools.MySQLVersion
		environment.MariaDBVersion = tools.MariaDBVersion
		environment.PostgreSQLVersion = tools.PostgreSQLVersion
		environment.SQLiteVersion = tools.SQLiteVersion
		environment.Database = PrimaryDatabaseConfigFromTools(tools, database)
		environment = populateDatabaseToolVersion(environment)
		if strings.TrimSpace(tools.MailpitVersion) != "" {
			environment.Mailpit = &MailpitConfig{Version: tools.MailpitVersion}
		}
		if strings.TrimSpace(tools.PHPMyAdminVersion) != "" {
			environment.PHPMyAdmin = &PHPMyAdminConfig{Version: tools.PHPMyAdminVersion}
		}
	}
	environment = applySettingsConfig(environment, settings)

	return inheritEnvironmentHTTPS(environment)
}

func applySettingsConfig(environment Environment, settings *SettingsConfig) Environment {
	if settings == nil {
		return environment
	}
	if settings.Mailpit != nil {
		if environment.Mailpit == nil {
			environment.Mailpit = &MailpitConfig{}
		}
		environment.Mailpit.SMTPPort = settings.Mailpit.SMTPPort
		environment.Mailpit.UIPort = settings.Mailpit.UIPort
	}
	if settings.PHPMyAdmin != nil {
		if environment.PHPMyAdmin == nil {
			environment.PHPMyAdmin = &PHPMyAdminConfig{}
		}
		environment.PHPMyAdmin.Port = settings.PHPMyAdmin.Port
	}

	return environment
}

func populateDatabaseToolVersion(environment Environment) Environment {
	if environment.Database == nil || strings.TrimSpace(environment.Database.Version) == "" {
		return environment
	}

	switch strings.ToLower(strings.TrimSpace(environment.Database.Engine)) {
	case "mysql":
		if strings.TrimSpace(environment.MySQLVersion) == "" {
			environment.MySQLVersion = environment.Database.Version
		}
	case "mariadb":
		if strings.TrimSpace(environment.MariaDBVersion) == "" {
			environment.MariaDBVersion = environment.Database.Version
		}
	case "postgresql":
		if strings.TrimSpace(environment.PostgreSQLVersion) == "" {
			environment.PostgreSQLVersion = environment.Database.Version
		}
	}

	return environment
}

// DatabaseToolVersion returns the configured database version for the requested engine.
func DatabaseToolVersion(environment Environment, engine string) string {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "mysql":
		if strings.TrimSpace(environment.MySQLVersion) != "" {
			return environment.MySQLVersion
		}
	case "mariadb":
		if strings.TrimSpace(environment.MariaDBVersion) != "" {
			return environment.MariaDBVersion
		}
	case "postgresql":
		if strings.TrimSpace(environment.PostgreSQLVersion) != "" {
			return environment.PostgreSQLVersion
		}
	default:
		return ""
	}
	if environment.Database != nil && strings.EqualFold(strings.TrimSpace(environment.Database.Engine), strings.TrimSpace(engine)) {
		return environment.Database.Version
	}

	return ""
}

// DatabaseRuntimeConfigFromEnvironment extracts root-level database runtime settings.
func DatabaseRuntimeConfigFromEnvironment(environment Environment) *DatabaseConfig {
	if environment.Database == nil {
		return nil
	}

	runtime := &DatabaseConfig{
		Engine: environment.Database.Engine,
		Port:   environment.Database.Port,
	}
	if runtime.Engine == "" && runtime.Port == 0 {
		return nil
	}

	return runtime
}

// PrimaryDatabaseConfigFromTools combines database runtime settings with the selected database tool.
func PrimaryDatabaseConfigFromTools(tools *ToolsConfig, database *DatabaseConfig) *DatabaseConfig {
	if tools == nil {
		return database
	}

	merged := &DatabaseConfig{}
	if database != nil {
		*merged = *database
	}
	if merged.Engine == "" {
		switch {
		case strings.TrimSpace(tools.MySQLVersion) != "" && strings.TrimSpace(tools.MariaDBVersion) == "" && strings.TrimSpace(tools.PostgreSQLVersion) == "":
			merged.Engine = "mysql"
		case strings.TrimSpace(tools.MariaDBVersion) != "" && strings.TrimSpace(tools.MySQLVersion) == "" && strings.TrimSpace(tools.PostgreSQLVersion) == "":
			merged.Engine = "mariadb"
		case strings.TrimSpace(tools.PostgreSQLVersion) != "" && strings.TrimSpace(tools.MySQLVersion) == "" && strings.TrimSpace(tools.MariaDBVersion) == "":
			merged.Engine = "postgresql"
		}
	}
	if merged.Version == "" {
		switch strings.ToLower(strings.TrimSpace(merged.Engine)) {
		case "mysql":
			merged.Version = tools.MySQLVersion
		case "mariadb":
			merged.Version = tools.MariaDBVersion
		case "postgresql":
			merged.Version = tools.PostgreSQLVersion
		}
	}

	return NormalizeDatabaseConfig(merged)
}

func NormalizeEnvironment(name string, environment Environment) Environment {
	normalized := Environment{
		Name:              name,
		Framework:         strings.ToLower(strings.TrimSpace(environment.Framework)),
		PHPVersion:        strings.TrimSpace(environment.PHPVersion),
		PHPZTSVersion:     strings.TrimSpace(environment.PHPZTSVersion),
		FrankenPHPVersion: strings.TrimSpace(environment.FrankenPHPVersion),
		ComposerVersion:   strings.TrimSpace(environment.ComposerVersion),
		PIEVersion:        strings.TrimSpace(environment.PIEVersion),
		NodeJSVersion:     strings.TrimSpace(environment.NodeJSVersion),
		MagoVersion:       strings.TrimSpace(environment.MagoVersion),
		NginxVersion:      strings.TrimSpace(environment.NginxVersion),
		ApacheVersion:     strings.TrimSpace(environment.ApacheVersion),
		MySQLVersion:      strings.TrimSpace(environment.MySQLVersion),
		MariaDBVersion:    strings.TrimSpace(environment.MariaDBVersion),
		PostgreSQLVersion: strings.TrimSpace(environment.PostgreSQLVersion),
		SQLiteVersion:     strings.TrimSpace(environment.SQLiteVersion),
		Docroot:           strings.TrimSpace(environment.Docroot),
		HTTPS:             environment.HTTPS,
		EnvFile:           strings.TrimSpace(environment.EnvFile),
		EnvVars:           NormalizeEnvironmentVariables(environment.EnvVars),
		Database:          NormalizeDatabaseConfig(environment.Database),
		Mailpit:           NormalizeMailpitConfig(environment.Mailpit),
		PHPMyAdmin:        NormalizePHPMyAdminConfig(environment.PHPMyAdmin),
		PHPExtensions:     NormalizePHPExtensions(environment.PHPExtensions),
		OPcachePreset:     NormalizeOPcachePreset(environment.OPcachePreset),
		OPcacheConfig:     NormalizeOPcacheConfig(environment.OPcacheConfig),
		Server:            NormalizeServerConfig(environment.Server),
	}

	return populateDatabaseToolVersion(inheritEnvironmentHTTPS(normalized))
}

func inheritEnvironmentHTTPS(environment Environment) Environment {
	if !environment.HTTPS {
		return environment
	}

	if environment.Server == nil {
		environment.Server = &ServerConfig{}
	}
	environment.Server.HTTPS = true

	if environment.Mailpit != nil {
		environment.Mailpit.HTTPS = true
	}
	if environment.PHPMyAdmin != nil {
		environment.PHPMyAdmin.HTTPS = true
	}

	return environment
}

func NormalizeEnvironmentVariables(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	normalized := make(map[string]string, len(values))
	for key, value := range values {
		normalized[strings.TrimSpace(key)] = value
	}

	return normalized
}

func MergeDatabaseConfig(existing, override *DatabaseConfig) *DatabaseConfig {
	if existing == nil && override == nil {
		return nil
	}

	merged := &DatabaseConfig{}
	if existing != nil {
		*merged = *existing
	}
	if override != nil {
		if override.Engine != "" {
			merged.Engine = override.Engine
		}
		if override.Version != "" {
			merged.Version = override.Version
		}
		if override.Port != 0 {
			merged.Port = override.Port
		}
	}

	return NormalizeDatabaseConfig(merged)
}

func NormalizeDatabaseConfig(database *DatabaseConfig) *DatabaseConfig {
	if database == nil {
		return nil
	}

	normalized := &DatabaseConfig{
		Engine:  strings.ToLower(strings.TrimSpace(database.Engine)),
		Version: strings.TrimSpace(database.Version),
		Port:    database.Port,
	}
	if normalized.Engine == "" && normalized.Version == "" && normalized.Port == 0 {
		return nil
	}

	return normalized
}

func NormalizeMailpitConfig(mailpit *MailpitConfig) *MailpitConfig {
	if mailpit == nil {
		return nil
	}

	normalized := &MailpitConfig{
		Version:  strings.TrimSpace(mailpit.Version),
		SMTPPort: mailpit.SMTPPort,
		UIPort:   mailpit.UIPort,
		HTTPS:    mailpit.HTTPS,
	}
	if normalized.Version == "" && normalized.SMTPPort == 0 && normalized.UIPort == 0 && !normalized.HTTPS {
		return nil
	}

	return normalized
}

func NormalizePHPMyAdminConfig(phpMyAdmin *PHPMyAdminConfig) *PHPMyAdminConfig {
	if phpMyAdmin == nil {
		return nil
	}

	normalized := &PHPMyAdminConfig{
		Version: strings.TrimSpace(phpMyAdmin.Version),
		Port:    phpMyAdmin.Port,
		HTTPS:   phpMyAdmin.HTTPS,
	}
	if normalized.Version == "" && normalized.Port == 0 && !normalized.HTTPS {
		return nil
	}

	return normalized
}

func NormalizeServerConfig(server *ServerConfig) *ServerConfig {
	if server == nil {
		return nil
	}

	normalized := &ServerConfig{
		Type:     strings.ToLower(strings.TrimSpace(server.Type)),
		Hostname: strings.TrimSpace(server.Hostname),
		Port:     server.Port,
		HTTPS:    server.HTTPS,
	}
	if normalized.Type == "" && normalized.Hostname == "" && normalized.Port == 0 && !normalized.HTTPS {
		return nil
	}

	return normalized
}

func NormalizePHPExtensions(extensions map[string]bool) map[string]bool {
	if len(extensions) == 0 {
		return nil
	}

	normalized := make(map[string]bool, len(extensions))
	for name, enabled := range extensions {
		normalized[strings.ToLower(strings.TrimSpace(name))] = enabled
	}

	return normalized
}

// NormalizeOPcachePreset canonicalizes the configured OPcache preset label.
func NormalizeOPcachePreset(preset string) string {
	normalized := strings.ToLower(strings.TrimSpace(preset))
	if normalized == OPcachePresetNone {
		return ""
	}

	return normalized
}

// NormalizeOPcacheConfig canonicalizes OPcache directive keys and trims values.
func NormalizeOPcacheConfig(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	normalized := make(map[string]string, len(values))
	for key, value := range values {
		normalized[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}

	return normalized
}

// NormalizeOPcacheConfigFromYAML converts YAML scalar values into the internal string map.
func NormalizeOPcacheConfigFromYAML(values map[string]any) map[string]string {
	if len(values) == 0 {
		return nil
	}

	normalized := make(map[string]string, len(values))
	for key, value := range values {
		normalized[strings.ToLower(strings.TrimSpace(key))] = opcacheConfigValueString(value)
	}

	return normalized
}

// OPcacheFileConfigFromEnvironment extracts OPcache directives for YAML output.
func OPcacheFileConfigFromEnvironment(environment Environment) map[string]any {
	if len(environment.OPcacheConfig) == 0 {
		return nil
	}

	config := NormalizeOPcacheConfig(environment.OPcacheConfig)
	values := make(map[string]any, len(config))
	for key, value := range config {
		values[key] = value
	}

	return values
}

// OPcachePresetConfig returns Polka's generated OPcache directives for a preset.
func OPcachePresetConfig(preset string) map[string]string {
	switch NormalizeOPcachePreset(preset) {
	case OPcachePresetDev:
		return map[string]string{
			"opcache.enable":                 "1",
			"opcache.file_update_protection": "0",
			"opcache.revalidate_freq":        "0",
			"opcache.validate_timestamps":    "1",
		}
	case OPcachePresetProduction:
		return map[string]string{
			"opcache.enable":                 "1",
			"opcache.file_update_protection": "0",
			"opcache.validate_timestamps":    "0",
		}
	default:
		return nil
	}
}

func opcacheConfigValueString(value any) string {
	if value == nil {
		return ""
	}

	return strings.TrimSpace(fmt.Sprint(value))
}
