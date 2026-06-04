package config

import "strings"

// ToolsConfig is the YAML shape for managed tools inside an environment file.
type ToolsConfig struct {
	PHPVersion      string            `yaml:"php,omitempty"`
	ComposerVersion string            `yaml:"composer,omitempty"`
	NodeJSVersion   string            `yaml:"nodejs,omitempty"`
	MagoVersion     string            `yaml:"mago,omitempty"`
	NginxVersion    string            `yaml:"nginx,omitempty"`
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
	EnvFile       string            `yaml:"env-file,omitempty"`
	EnvVars       map[string]string `yaml:"env-vars,omitempty"`
	PHPExtensions map[string]bool   `yaml:"php-extensions,omitempty"`
	Server        *ServerConfig     `yaml:"server,omitempty"`
}

// EnvironmentFile is the YAML shape of polka.<name>.yaml.
type EnvironmentFile struct {
	Tools         *ToolsConfig      `yaml:"tools,omitempty"`
	Docroot       string            `yaml:"docroot,omitempty"`
	EnvFile       string            `yaml:"env-file,omitempty"`
	EnvVars       map[string]string `yaml:"env-vars,omitempty"`
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
	Docroot         string            `yaml:"docroot,omitempty"`
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
		file.EnvFile,
		file.EnvVars,
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
		EnvFile:       environment.EnvFile,
		EnvVars:       environment.EnvVars,
		PHPExtensions: environment.PHPExtensions,
		Server:        environment.Server,
	}

	return file
}

// EnvironmentFileToEnvironment converts polka.<name>.yaml data into the internal environment model.
func EnvironmentFileToEnvironment(name string, file EnvironmentFile) Environment {
	return environmentFromFileParts(
		name,
		file.Tools,
		file.Docroot,
		file.EnvFile,
		file.EnvVars,
		file.PHPExtensions,
		file.Server,
	)
}

// EnvironmentFileFromEnvironment converts a named environment into polka.<name>.yaml data.
func EnvironmentFileFromEnvironment(environment Environment) EnvironmentFile {
	return EnvironmentFile{
		Tools:         ToolsConfigFromEnvironment(environment),
		Docroot:       environment.Docroot,
		EnvFile:       environment.EnvFile,
		EnvVars:       environment.EnvVars,
		PHPExtensions: environment.PHPExtensions,
		Server:        environment.Server,
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
		Database:        environment.Database,
		Mailpit:         environment.Mailpit,
		PHPMyAdmin:      environment.PHPMyAdmin,
	}
	if tools.IsZero() {
		return nil
	}

	return tools
}

// IsZero reports whether no managed tool settings are configured.
func (tools ToolsConfig) IsZero() bool {
	return strings.TrimSpace(tools.PHPVersion) == "" &&
		strings.TrimSpace(tools.ComposerVersion) == "" &&
		strings.TrimSpace(tools.NodeJSVersion) == "" &&
		strings.TrimSpace(tools.MagoVersion) == "" &&
		strings.TrimSpace(tools.NginxVersion) == "" &&
		tools.Database == nil &&
		tools.Mailpit == nil &&
		tools.PHPMyAdmin == nil
}

func environmentFromFileParts(name string, tools *ToolsConfig, docroot, envFile string, envVars map[string]string, phpExtensions map[string]bool, server *ServerConfig) Environment {
	environment := Environment{
		Name:          name,
		Docroot:       docroot,
		EnvFile:       envFile,
		EnvVars:       envVars,
		PHPExtensions: phpExtensions,
		Server:        server,
	}
	if tools != nil {
		environment.PHPVersion = tools.PHPVersion
		environment.ComposerVersion = tools.ComposerVersion
		environment.NodeJSVersion = tools.NodeJSVersion
		environment.MagoVersion = tools.MagoVersion
		environment.NginxVersion = tools.NginxVersion
		environment.Database = tools.Database
		environment.Mailpit = tools.Mailpit
		environment.PHPMyAdmin = tools.PHPMyAdmin
	}

	return environment
}

func NormalizeEnvironment(name string, environment Environment) Environment {
	return Environment{
		Name:            name,
		PHPVersion:      strings.TrimSpace(environment.PHPVersion),
		ComposerVersion: strings.TrimSpace(environment.ComposerVersion),
		NodeJSVersion:   strings.TrimSpace(environment.NodeJSVersion),
		MagoVersion:     strings.TrimSpace(environment.MagoVersion),
		NginxVersion:    strings.TrimSpace(environment.NginxVersion),
		Docroot:         strings.TrimSpace(environment.Docroot),
		EnvFile:         strings.TrimSpace(environment.EnvFile),
		EnvVars:         NormalizeEnvironmentVariables(environment.EnvVars),
		Database:        NormalizeDatabaseConfig(environment.Database),
		Mailpit:         NormalizeMailpitConfig(environment.Mailpit),
		PHPMyAdmin:      NormalizePHPMyAdminConfig(environment.PHPMyAdmin),
		PHPExtensions:   NormalizePHPExtensions(environment.PHPExtensions),
		Server:          NormalizeServerConfig(environment.Server),
	}
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
