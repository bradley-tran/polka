package config

import "strings"

// ToolsConfig is the YAML shape for managed tools inside an environment file.
type ToolsConfig struct {
	PHPVersion      string            `yaml:"php,omitempty"`
	ComposerVersion string            `yaml:"composer,omitempty"`
	NodeJSVersion   string            `yaml:"nodejs,omitempty"`
	MagoVersion     string            `yaml:"mago,omitempty"`
	NginxVersion    string            `yaml:"nginx,omitempty"`
	MySQLVersion    string            `yaml:"mysql,omitempty"`
	MariaDBVersion  string            `yaml:"mariadb,omitempty"`
	SQLiteVersion   string            `yaml:"sqlite,omitempty"`
	Database        *DatabaseConfig   `yaml:"database,omitempty"`
	Mailpit         *MailpitConfig    `yaml:"mailpit,omitempty"`
	PHPMyAdmin      *PHPMyAdminConfig `yaml:"phpmyadmin,omitempty"`
}

// ProjectFile is the YAML shape of polka.yaml, which also defines the default environment.
type ProjectFile struct {
	Version       int               `yaml:"version"`
	Root          string            `yaml:"root"`
	Tools         *ToolsConfig      `yaml:"tools,omitempty"`
	Docroot       string            `yaml:"docroot,omitempty"`
	HTTPS         bool              `yaml:"https,omitempty"`
	EnvFile       string            `yaml:"env-file,omitempty"`
	EnvVars       map[string]string `yaml:"env-vars,omitempty"`
	Database      *DatabaseConfig   `yaml:"database,omitempty"`
	PHPExtensions map[string]bool   `yaml:"php-extensions,omitempty"`
	Server        *ServerConfig     `yaml:"server,omitempty"`
}

// EnvironmentFile is the YAML shape of polka.<name>.yaml.
type EnvironmentFile struct {
	Tools         *ToolsConfig      `yaml:"tools,omitempty"`
	Docroot       string            `yaml:"docroot,omitempty"`
	HTTPS         bool              `yaml:"https,omitempty"`
	EnvFile       string            `yaml:"env-file,omitempty"`
	EnvVars       map[string]string `yaml:"env-vars,omitempty"`
	Database      *DatabaseConfig   `yaml:"database,omitempty"`
	PHPExtensions map[string]bool   `yaml:"php-extensions,omitempty"`
	Server        *ServerConfig     `yaml:"server,omitempty"`
}

type Environment struct {
	Name            string            `yaml:"-"`
	PHPVersion      string            `yaml:"php,omitempty"`
	ComposerVersion string            `yaml:"composer,omitempty"`
	NodeJSVersion   string            `yaml:"nodejs,omitempty"`
	MagoVersion     string            `yaml:"mago,omitempty"`
	NginxVersion    string            `yaml:"nginx,omitempty"`
	MySQLVersion    string            `yaml:"mysql,omitempty"`
	MariaDBVersion  string            `yaml:"mariadb,omitempty"`
	SQLiteVersion   string            `yaml:"sqlite,omitempty"`
	Docroot         string            `yaml:"docroot,omitempty"`
	HTTPS           bool              `yaml:"https,omitempty"`
	EnvFile         string            `yaml:"env-file,omitempty"`
	EnvVars         map[string]string `yaml:"env-vars,omitempty"`
	Database        *DatabaseConfig   `yaml:"database,omitempty"`
	Mailpit         *MailpitConfig    `yaml:"mailpit,omitempty"`
	PHPMyAdmin      *PHPMyAdminConfig `yaml:"phpmyadmin,omitempty"`
	PHPExtensions   map[string]bool   `yaml:"php-extensions,omitempty"`
	Server          *ServerConfig     `yaml:"server,omitempty"`
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
	Hostname string `yaml:"hostname,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	HTTPS    bool   `yaml:"https,omitempty"`
}

type Config struct {
	Version      int                    `yaml:"version"`
	Root         string                 `yaml:"root"`
	Environments map[string]Environment `yaml:"-"`
}

// ProjectFileToEnvironment converts polka.yaml data into the internal environment model.
func ProjectFileToEnvironment(name string, file ProjectFile) Environment {
	return environmentFromFileParts(
		name,
		file.Tools,
		file.Docroot,
		file.HTTPS,
		file.EnvFile,
		file.EnvVars,
		file.Database,
		file.PHPExtensions,
		file.Server,
	)
}

// ProjectFileFromEnvironment converts the default environment into polka.yaml data.
func ProjectFileFromEnvironment(version int, root string, environment Environment) ProjectFile {
	file := ProjectFile{
		Version:       version,
		Root:          root,
		Tools:         ToolsConfigFromEnvironment(environment),
		Docroot:       environment.Docroot,
		HTTPS:         environment.HTTPS,
		EnvFile:       environment.EnvFile,
		EnvVars:       environment.EnvVars,
		Database:      DatabaseRuntimeConfigFromEnvironment(environment),
		PHPExtensions: environment.PHPExtensions,
		Server:        ServerConfigFromEnvironment(environment),
	}

	return file
}

// EnvironmentFileToEnvironment converts polka.<name>.yaml data into the internal environment model.
func EnvironmentFileToEnvironment(name string, file EnvironmentFile) Environment {
	return environmentFromFileParts(
		name,
		file.Tools,
		file.Docroot,
		file.HTTPS,
		file.EnvFile,
		file.EnvVars,
		file.Database,
		file.PHPExtensions,
		file.Server,
	)
}

// EnvironmentFileFromEnvironment converts a named environment into polka.<name>.yaml data.
func EnvironmentFileFromEnvironment(environment Environment) EnvironmentFile {
	return EnvironmentFile{
		Tools:         ToolsConfigFromEnvironment(environment),
		Docroot:       environment.Docroot,
		HTTPS:         environment.HTTPS,
		EnvFile:       environment.EnvFile,
		EnvVars:       environment.EnvVars,
		Database:      DatabaseRuntimeConfigFromEnvironment(environment),
		PHPExtensions: environment.PHPExtensions,
		Server:        ServerConfigFromEnvironment(environment),
	}
}

// ToolsConfigFromEnvironment extracts managed tool settings from an environment.
func ToolsConfigFromEnvironment(environment Environment) *ToolsConfig {
	tools := &ToolsConfig{
		PHPVersion:      environment.PHPVersion,
		ComposerVersion: environment.ComposerVersion,
		NodeJSVersion:   environment.NodeJSVersion,
		MagoVersion:     environment.MagoVersion,
		NginxVersion:    environment.NginxVersion,
		MySQLVersion:    DatabaseToolVersion(environment, "mysql"),
		MariaDBVersion:  DatabaseToolVersion(environment, "mariadb"),
		SQLiteVersion:   environment.SQLiteVersion,
		Mailpit:         MailpitConfigFromEnvironment(environment),
		PHPMyAdmin:      PHPMyAdminConfigFromEnvironment(environment),
	}
	if tools.IsZero() {
		return nil
	}

	return tools
}

// MailpitConfigFromEnvironment extracts Mailpit settings that belong in YAML.
func MailpitConfigFromEnvironment(environment Environment) *MailpitConfig {
	if environment.Mailpit == nil {
		return nil
	}

	mailpit := &MailpitConfig{
		Version:  strings.TrimSpace(environment.Mailpit.Version),
		SMTPPort: environment.Mailpit.SMTPPort,
		UIPort:   environment.Mailpit.UIPort,
	}
	if mailpit.Version == "" && mailpit.SMTPPort == 0 && mailpit.UIPort == 0 {
		return nil
	}

	return mailpit
}

// PHPMyAdminConfigFromEnvironment extracts phpMyAdmin settings that belong in YAML.
func PHPMyAdminConfigFromEnvironment(environment Environment) *PHPMyAdminConfig {
	if environment.PHPMyAdmin == nil {
		return nil
	}

	phpMyAdmin := &PHPMyAdminConfig{
		Version: strings.TrimSpace(environment.PHPMyAdmin.Version),
		Port:    environment.PHPMyAdmin.Port,
	}
	if phpMyAdmin.Version == "" && phpMyAdmin.Port == 0 {
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
		Hostname: strings.TrimSpace(environment.Server.Hostname),
		Port:     environment.Server.Port,
	}
	if server.Hostname == "" && server.Port == 0 {
		return nil
	}

	return server
}

// IsZero reports whether no managed tool settings are configured.
func (tools ToolsConfig) IsZero() bool {
	return strings.TrimSpace(tools.PHPVersion) == "" &&
		strings.TrimSpace(tools.ComposerVersion) == "" &&
		strings.TrimSpace(tools.NodeJSVersion) == "" &&
		strings.TrimSpace(tools.MagoVersion) == "" &&
		strings.TrimSpace(tools.NginxVersion) == "" &&
		strings.TrimSpace(tools.MySQLVersion) == "" &&
		strings.TrimSpace(tools.MariaDBVersion) == "" &&
		strings.TrimSpace(tools.SQLiteVersion) == "" &&
		tools.Database == nil &&
		tools.Mailpit == nil &&
		tools.PHPMyAdmin == nil
}

func environmentFromFileParts(name string, tools *ToolsConfig, docroot string, https bool, envFile string, envVars map[string]string, database *DatabaseConfig, phpExtensions map[string]bool, server *ServerConfig) Environment {
	environment := Environment{
		Name:          name,
		Docroot:       docroot,
		HTTPS:         https,
		EnvFile:       envFile,
		EnvVars:       envVars,
		Database:      database,
		PHPExtensions: phpExtensions,
		Server:        server,
	}
	if tools != nil {
		environment.PHPVersion = tools.PHPVersion
		environment.ComposerVersion = tools.ComposerVersion
		environment.NodeJSVersion = tools.NodeJSVersion
		environment.MagoVersion = tools.MagoVersion
		environment.NginxVersion = tools.NginxVersion
		environment.MySQLVersion = tools.MySQLVersion
		environment.MariaDBVersion = tools.MariaDBVersion
		environment.SQLiteVersion = tools.SQLiteVersion
		environment.Database = PrimaryDatabaseConfigFromTools(tools, database)
		environment = populateDatabaseToolVersion(environment)
		environment.Mailpit = tools.Mailpit
		environment.PHPMyAdmin = tools.PHPMyAdmin
	}

	return inheritEnvironmentHTTPS(environment)
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
	if tools.Database != nil {
		legacy := NormalizeDatabaseConfig(tools.Database)
		if legacy != nil {
			if merged.Engine == "" {
				merged.Engine = legacy.Engine
			}
			if merged.Version == "" {
				merged.Version = legacy.Version
			}
			if merged.Port == 0 {
				merged.Port = legacy.Port
			}
		}
	}
	if merged.Engine == "" {
		switch {
		case strings.TrimSpace(tools.MySQLVersion) != "" && strings.TrimSpace(tools.MariaDBVersion) == "":
			merged.Engine = "mysql"
		case strings.TrimSpace(tools.MariaDBVersion) != "" && strings.TrimSpace(tools.MySQLVersion) == "":
			merged.Engine = "mariadb"
		}
	}
	if merged.Version == "" {
		switch strings.ToLower(strings.TrimSpace(merged.Engine)) {
		case "mysql":
			merged.Version = tools.MySQLVersion
		case "mariadb":
			merged.Version = tools.MariaDBVersion
		}
	}

	return NormalizeDatabaseConfig(merged)
}

func NormalizeEnvironment(name string, environment Environment) Environment {
	normalized := Environment{
		Name:            name,
		PHPVersion:      strings.TrimSpace(environment.PHPVersion),
		ComposerVersion: strings.TrimSpace(environment.ComposerVersion),
		NodeJSVersion:   strings.TrimSpace(environment.NodeJSVersion),
		MagoVersion:     strings.TrimSpace(environment.MagoVersion),
		NginxVersion:    strings.TrimSpace(environment.NginxVersion),
		MySQLVersion:    strings.TrimSpace(environment.MySQLVersion),
		MariaDBVersion:  strings.TrimSpace(environment.MariaDBVersion),
		SQLiteVersion:   strings.TrimSpace(environment.SQLiteVersion),
		Docroot:         strings.TrimSpace(environment.Docroot),
		HTTPS:           environment.HTTPS,
		EnvFile:         strings.TrimSpace(environment.EnvFile),
		EnvVars:         NormalizeEnvironmentVariables(environment.EnvVars),
		Database:        NormalizeDatabaseConfig(environment.Database),
		Mailpit:         NormalizeMailpitConfig(environment.Mailpit),
		PHPMyAdmin:      NormalizePHPMyAdminConfig(environment.PHPMyAdmin),
		PHPExtensions:   NormalizePHPExtensions(environment.PHPExtensions),
		Server:          NormalizeServerConfig(environment.Server),
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
		Hostname: strings.TrimSpace(server.Hostname),
		Port:     server.Port,
		HTTPS:    server.HTTPS,
	}
	if normalized.Hostname == "" && normalized.Port == 0 && !normalized.HTTPS {
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
