package config

import "strings"

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
	Environments map[string]Environment `yaml:"environments,omitempty"`
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
