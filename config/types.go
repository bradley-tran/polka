package config

import (
	"fmt"
	"regexp"
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
	PHPVersion         string `yaml:"php,omitempty"`
	PHPZTSVersion      string `yaml:"php-zts,omitempty"`
	FrankenPHPVersion  string `yaml:"frankenphp,omitempty"`
	RoadRunnerVersion  string `yaml:"roadrunner,omitempty"`
	ComposerVersion    string `yaml:"composer,omitempty"`
	PIEVersion         string `yaml:"pie,omitempty"`
	NodeJSVersion      string `yaml:"nodejs,omitempty"`
	MagoVersion        string `yaml:"mago,omitempty"`
	NginxVersion       string `yaml:"nginx,omitempty"`
	ApacheVersion      string `yaml:"apache,omitempty"`
	MySQLVersion       string `yaml:"mysql,omitempty"`
	MariaDBVersion     string `yaml:"mariadb,omitempty"`
	PostgreSQLVersion  string `yaml:"postgresql,omitempty"`
	SQLiteVersion      string `yaml:"sqlite,omitempty"`
	MailpitVersion     string `yaml:"mailpit,omitempty"`
	PHPMyAdminVersion  string `yaml:"phpmyadmin,omitempty"`
	MeilisearchVersion string `yaml:"meilisearch,omitempty"`
	RedisVersion       string `yaml:"redis,omitempty"`
	RabbitMQVersion    string `yaml:"rabbitmq,omitempty"`
	TraefikVersion     string `yaml:"traefik,omitempty"`
}

// SettingsConfig is the YAML shape for versionless per-tool settings.
type SettingsConfig struct {
	Mailpit     *MailpitSettingsConfig     `yaml:"mailpit,omitempty"`
	PHPMyAdmin  *PHPMyAdminSettingsConfig  `yaml:"phpmyadmin,omitempty"`
	Meilisearch *MeilisearchSettingsConfig `yaml:"meilisearch,omitempty"`
	Redis       *RedisSettingsConfig       `yaml:"redis,omitempty"`
	RabbitMQ    *RabbitMQSettingsConfig    `yaml:"rabbitmq,omitempty"`
	Traefik     *TraefikSettingsConfig     `yaml:"traefik,omitempty"`
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

// MeilisearchSettingsConfig is the YAML shape for Meilisearch runtime settings.
type MeilisearchSettingsConfig struct {
	Port      int    `yaml:"port,omitempty"`
	MasterKey string `yaml:"master-key,omitempty"`
}

// RedisSettingsConfig is the YAML shape for Redis runtime settings.
type RedisSettingsConfig struct {
	Port     int    `yaml:"port,omitempty"`
	Password string `yaml:"password,omitempty"`
}

// RabbitMQSettingsConfig is the YAML shape for RabbitMQ runtime settings.
type RabbitMQSettingsConfig struct {
	Port           int    `yaml:"port,omitempty"`
	ManagementPort int    `yaml:"management-port,omitempty"`
	Username       string `yaml:"username,omitempty"`
	Password       string `yaml:"password,omitempty"`
}

// TraefikSettingsConfig is the YAML shape for Traefik runtime settings.
type TraefikSettingsConfig struct {
	Port int `yaml:"port,omitempty"`
}

// ProjectFile is the YAML shape of polka.yaml, which also defines the default environment.
type ProjectFile struct {
	Version        int                     `yaml:"version"`
	Root           string                  `yaml:"root"`
	Framework      string                  `yaml:"framework,omitempty"`
	Tools          *ToolsConfig            `yaml:"tools,omitempty"`
	Settings       *SettingsConfig         `yaml:"settings,omitempty"`
	Docroot        string                  `yaml:"docroot,omitempty"`
	HTTPS          bool                    `yaml:"https,omitempty"`
	EnvFile        string                  `yaml:"env-file,omitempty"`
	EnvVars        map[string]string       `yaml:"env-vars,omitempty"`
	Database       *DatabaseConfig         `yaml:"database,omitempty"`
	MemoryLimit    any                     `yaml:"memory-limit,omitempty"`
	PHPExtensions  map[string]any          `yaml:"php-extensions,omitempty"`
	PECLExtensions map[string]any          `yaml:"pecl-extensions,omitempty"`
	OPcachePreset  string                  `yaml:"opcache-preset,omitempty"`
	OPcacheConfig  map[string]any          `yaml:"opcache-config,omitempty"`
	Server         *ServerConfig           `yaml:"server,omitempty"`
	Workers        map[string]WorkerConfig `yaml:"workers,omitempty"`
}

// EnvironmentFile is the YAML shape of polka.<name>.yaml.
type EnvironmentFile struct {
	Framework      string                  `yaml:"framework,omitempty"`
	Tools          *ToolsConfig            `yaml:"tools,omitempty"`
	Settings       *SettingsConfig         `yaml:"settings,omitempty"`
	Docroot        string                  `yaml:"docroot,omitempty"`
	HTTPS          bool                    `yaml:"https,omitempty"`
	EnvFile        string                  `yaml:"env-file,omitempty"`
	EnvVars        map[string]string       `yaml:"env-vars,omitempty"`
	Database       *DatabaseConfig         `yaml:"database,omitempty"`
	MemoryLimit    any                     `yaml:"memory-limit,omitempty"`
	PHPExtensions  map[string]any          `yaml:"php-extensions,omitempty"`
	PECLExtensions map[string]any          `yaml:"pecl-extensions,omitempty"`
	OPcachePreset  string                  `yaml:"opcache-preset,omitempty"`
	OPcacheConfig  map[string]any          `yaml:"opcache-config,omitempty"`
	Server         *ServerConfig           `yaml:"server,omitempty"`
	Workers        map[string]WorkerConfig `yaml:"workers,omitempty"`
}

type Environment struct {
	Name              string                         `yaml:"-"`
	Framework         string                         `yaml:"framework,omitempty"`
	PHPVersion        string                         `yaml:"php,omitempty"`
	PHPZTSVersion     string                         `yaml:"php-zts,omitempty"`
	FrankenPHPVersion string                         `yaml:"frankenphp,omitempty"`
	RoadRunnerVersion string                         `yaml:"roadrunner,omitempty"`
	ComposerVersion   string                         `yaml:"composer,omitempty"`
	PIEVersion        string                         `yaml:"pie,omitempty"`
	NodeJSVersion     string                         `yaml:"nodejs,omitempty"`
	MagoVersion       string                         `yaml:"mago,omitempty"`
	NginxVersion      string                         `yaml:"nginx,omitempty"`
	ApacheVersion     string                         `yaml:"apache,omitempty"`
	MySQLVersion      string                         `yaml:"mysql,omitempty"`
	MariaDBVersion    string                         `yaml:"mariadb,omitempty"`
	PostgreSQLVersion string                         `yaml:"postgresql,omitempty"`
	SQLiteVersion     string                         `yaml:"sqlite,omitempty"`
	Docroot           string                         `yaml:"docroot,omitempty"`
	HTTPS             bool                           `yaml:"https,omitempty"`
	EnvFile           string                         `yaml:"env-file,omitempty"`
	EnvVars           map[string]string              `yaml:"env-vars,omitempty"`
	Database          *DatabaseConfig                `yaml:"database,omitempty"`
	Mailpit           *MailpitConfig                 `yaml:"mailpit,omitempty"`
	PHPMyAdmin        *PHPMyAdminConfig              `yaml:"phpmyadmin,omitempty"`
	Meilisearch       *MeilisearchConfig             `yaml:"meilisearch,omitempty"`
	Redis             *RedisConfig                   `yaml:"redis,omitempty"`
	RabbitMQ          *RabbitMQConfig                `yaml:"rabbitmq,omitempty"`
	Traefik           *TraefikConfig                 `yaml:"traefik,omitempty"`
	MemoryLimit       string                         `yaml:"memory-limit,omitempty"`
	PHPExtensions     map[string]bool                `yaml:"php-extensions,omitempty"`
	PIEExtensions     map[string]string              `yaml:"-"`
	PECLExtensions    map[string]PECLExtensionConfig `yaml:"-"`
	ZendExtensions    map[string]bool                `yaml:"-"`
	OPcachePreset     string                         `yaml:"opcache-preset,omitempty"`
	OPcacheConfig     map[string]string              `yaml:"opcache-config,omitempty"`
	Server            *ServerConfig                  `yaml:"server,omitempty"`
	Workers           map[string]WorkerConfig        `yaml:"workers,omitempty"`
}

// PECLExtensionConfig describes one legacy PECL package. ConfigureOptions
// contains answers for package.xml configure options used by Linux builds.
type PECLExtensionConfig struct {
	Version          string            `yaml:"version"`
	ConfigureOptions map[string]string `yaml:"configure-options,omitempty"`
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

type MeilisearchConfig struct {
	Version   string `yaml:"version,omitempty"`
	Port      int    `yaml:"port,omitempty"`
	MasterKey string `yaml:"master-key,omitempty"`
}

type RedisConfig struct {
	Version  string `yaml:"version,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	Password string `yaml:"password,omitempty"`
}

// RabbitMQConfig combines the managed version with local broker settings.
type RabbitMQConfig struct {
	Version        string `yaml:"version,omitempty"`
	Port           int    `yaml:"port,omitempty"`
	ManagementPort int    `yaml:"management-port,omitempty"`
	Username       string `yaml:"username,omitempty"`
	Password       string `yaml:"password,omitempty"`
}

type TraefikConfig struct {
	Version string `yaml:"version,omitempty"`
	Port    int    `yaml:"port,omitempty"`
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
		file.MemoryLimit,
		file.PHPExtensions,
		file.PECLExtensions,
		file.OPcachePreset,
		file.OPcacheConfig,
		file.Server,
		file.Workers,
	)
}

// ProjectFileFromEnvironment converts the default environment into polka.yaml data.
func ProjectFileFromEnvironment(version int, root string, environment Environment) ProjectFile {
	file := ProjectFile{
		Version:        version,
		Root:           root,
		Framework:      strings.ToLower(strings.TrimSpace(environment.Framework)),
		Tools:          ToolsConfigFromEnvironment(environment),
		Settings:       SettingsConfigFromEnvironment(environment),
		Docroot:        environment.Docroot,
		HTTPS:          environment.HTTPS,
		EnvFile:        environment.EnvFile,
		EnvVars:        environment.EnvVars,
		Database:       DatabaseRuntimeConfigFromEnvironment(environment),
		MemoryLimit:    phpMemoryLimitFileValue(environment.MemoryLimit),
		PHPExtensions:  PHPExtensionsFileValue(environment.PHPExtensions, environment.PIEExtensions),
		PECLExtensions: PECLExtensionsFileValue(environment.PECLExtensions),
		OPcachePreset:  NormalizeOPcachePreset(environment.OPcachePreset),
		OPcacheConfig:  OPcacheFileConfigFromEnvironment(environment),
		Server:         ServerConfigFromEnvironment(environment),
		Workers:        NormalizeWorkersConfig(environment.Workers),
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
		file.MemoryLimit,
		file.PHPExtensions,
		file.PECLExtensions,
		file.OPcachePreset,
		file.OPcacheConfig,
		file.Server,
		file.Workers,
	)
}

// EnvironmentFileFromEnvironment converts a named environment into polka.<name>.yaml data.
func EnvironmentFileFromEnvironment(environment Environment) EnvironmentFile {
	return EnvironmentFile{
		Framework:      strings.ToLower(strings.TrimSpace(environment.Framework)),
		Tools:          ToolsConfigFromEnvironment(environment),
		Settings:       SettingsConfigFromEnvironment(environment),
		Docroot:        environment.Docroot,
		HTTPS:          environment.HTTPS,
		EnvFile:        environment.EnvFile,
		EnvVars:        environment.EnvVars,
		Database:       DatabaseRuntimeConfigFromEnvironment(environment),
		MemoryLimit:    phpMemoryLimitFileValue(environment.MemoryLimit),
		PHPExtensions:  PHPExtensionsFileValue(environment.PHPExtensions, environment.PIEExtensions),
		PECLExtensions: PECLExtensionsFileValue(environment.PECLExtensions),
		OPcachePreset:  NormalizeOPcachePreset(environment.OPcachePreset),
		OPcacheConfig:  OPcacheFileConfigFromEnvironment(environment),
		Server:         ServerConfigFromEnvironment(environment),
		Workers:        NormalizeWorkersConfig(environment.Workers),
	}
}

// ToolsConfigFromEnvironment extracts managed tool settings from an environment.
func ToolsConfigFromEnvironment(environment Environment) *ToolsConfig {
	tools := &ToolsConfig{
		PHPVersion:         environment.PHPVersion,
		PHPZTSVersion:      environment.PHPZTSVersion,
		FrankenPHPVersion:  environment.FrankenPHPVersion,
		RoadRunnerVersion:  environment.RoadRunnerVersion,
		ComposerVersion:    environment.ComposerVersion,
		PIEVersion:         environment.PIEVersion,
		NodeJSVersion:      environment.NodeJSVersion,
		MagoVersion:        environment.MagoVersion,
		NginxVersion:       environment.NginxVersion,
		ApacheVersion:      environment.ApacheVersion,
		MySQLVersion:       DatabaseToolVersion(environment, "mysql"),
		MariaDBVersion:     DatabaseToolVersion(environment, "mariadb"),
		PostgreSQLVersion:  DatabaseToolVersion(environment, "postgresql"),
		SQLiteVersion:      environment.SQLiteVersion,
		MailpitVersion:     ToolVersionFromMailpitConfig(environment.Mailpit),
		PHPMyAdminVersion:  ToolVersionFromPHPMyAdminConfig(environment.PHPMyAdmin),
		MeilisearchVersion: ToolVersionFromMeilisearchConfig(environment.Meilisearch),
		RedisVersion:       ToolVersionFromRedisConfig(environment.Redis),
		RabbitMQVersion:    ToolVersionFromRabbitMQConfig(environment.RabbitMQ),
		TraefikVersion:     ToolVersionFromTraefikConfig(environment.Traefik),
	}
	if tools.IsZero() {
		return nil
	}

	return tools
}

// SettingsConfigFromEnvironment extracts versionless tool settings from an environment.
func SettingsConfigFromEnvironment(environment Environment) *SettingsConfig {
	settings := &SettingsConfig{
		Mailpit:     MailpitSettingsConfigFromEnvironment(environment),
		PHPMyAdmin:  PHPMyAdminSettingsConfigFromEnvironment(environment),
		Meilisearch: MeilisearchSettingsConfigFromEnvironment(environment),
		Redis:       RedisSettingsConfigFromEnvironment(environment),
		RabbitMQ:    RabbitMQSettingsConfigFromEnvironment(environment),
		Traefik:     TraefikSettingsConfigFromEnvironment(environment),
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

// ToolVersionFromMeilisearchConfig extracts Meilisearch's managed tool version label.
func ToolVersionFromMeilisearchConfig(meilisearch *MeilisearchConfig) string {
	if meilisearch == nil {
		return ""
	}

	return strings.TrimSpace(meilisearch.Version)
}

// ToolVersionFromRedisConfig extracts Redis's managed tool version label.
func ToolVersionFromRedisConfig(redis *RedisConfig) string {
	if redis == nil {
		return ""
	}

	return strings.TrimSpace(redis.Version)
}

// ToolVersionFromRabbitMQConfig extracts RabbitMQ's managed tool version label.
func ToolVersionFromRabbitMQConfig(rabbitMQ *RabbitMQConfig) string {
	if rabbitMQ == nil {
		return ""
	}

	return strings.TrimSpace(rabbitMQ.Version)
}

// ToolVersionFromTraefikConfig extracts Traefik's managed tool version label.
func ToolVersionFromTraefikConfig(traefik *TraefikConfig) string {
	if traefik == nil {
		return ""
	}

	return strings.TrimSpace(traefik.Version)
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

// MeilisearchSettingsConfigFromEnvironment extracts Meilisearch settings that belong under settings.
func MeilisearchSettingsConfigFromEnvironment(environment Environment) *MeilisearchSettingsConfig {
	if environment.Meilisearch == nil {
		return nil
	}

	meilisearch := &MeilisearchSettingsConfig{
		Port:      environment.Meilisearch.Port,
		MasterKey: strings.TrimSpace(environment.Meilisearch.MasterKey),
	}
	if meilisearch.Port == 0 && meilisearch.MasterKey == "" {
		return nil
	}

	return meilisearch
}

// RedisSettingsConfigFromEnvironment extracts Redis settings that belong under settings.
func RedisSettingsConfigFromEnvironment(environment Environment) *RedisSettingsConfig {
	if environment.Redis == nil {
		return nil
	}

	redis := &RedisSettingsConfig{
		Port:     environment.Redis.Port,
		Password: strings.TrimSpace(environment.Redis.Password),
	}
	if redis.Port == 0 && redis.Password == "" {
		return nil
	}

	return redis
}

// RabbitMQSettingsConfigFromEnvironment extracts versionless broker settings.
func RabbitMQSettingsConfigFromEnvironment(environment Environment) *RabbitMQSettingsConfig {
	if environment.RabbitMQ == nil {
		return nil
	}

	rabbitMQ := &RabbitMQSettingsConfig{
		Port:           environment.RabbitMQ.Port,
		ManagementPort: environment.RabbitMQ.ManagementPort,
		Username:       strings.TrimSpace(environment.RabbitMQ.Username),
		Password:       strings.TrimSpace(environment.RabbitMQ.Password),
	}
	if rabbitMQ.Port == 0 && rabbitMQ.ManagementPort == 0 && rabbitMQ.Username == "" && rabbitMQ.Password == "" {
		return nil
	}

	return rabbitMQ
}

// TraefikSettingsConfigFromEnvironment extracts Traefik settings that belong under settings.
func TraefikSettingsConfigFromEnvironment(environment Environment) *TraefikSettingsConfig {
	if environment.Traefik == nil {
		return nil
	}

	traefik := &TraefikSettingsConfig{
		Port: environment.Traefik.Port,
	}
	if traefik.Port == 0 {
		return nil
	}

	return traefik
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
		strings.TrimSpace(tools.RoadRunnerVersion) == "" &&
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
		strings.TrimSpace(tools.PHPMyAdminVersion) == "" &&
		strings.TrimSpace(tools.MeilisearchVersion) == "" &&
		strings.TrimSpace(tools.RedisVersion) == "" &&
		strings.TrimSpace(tools.RabbitMQVersion) == "" &&
		strings.TrimSpace(tools.TraefikVersion) == ""
}

// IsZero reports whether no versionless tool settings are configured.
func (settings SettingsConfig) IsZero() bool {
	return settings.Mailpit == nil &&
		settings.PHPMyAdmin == nil &&
		settings.Meilisearch == nil &&
		settings.Redis == nil &&
		settings.RabbitMQ == nil &&
		settings.Traefik == nil
}

func environmentFromFileParts(name string, framework string, tools *ToolsConfig, settings *SettingsConfig, docroot string, https bool, envFile string, envVars map[string]string, database *DatabaseConfig, memoryLimit any, phpExtensions map[string]any, peclExtensions map[string]any, opcachePreset string, opcacheConfig map[string]any, server *ServerConfig, workers map[string]WorkerConfig) Environment {
	bundledExtensions, pieExtensions := SplitPHPExtensionsFromYAML(phpExtensions)
	environment := Environment{
		Name:           name,
		Framework:      strings.ToLower(strings.TrimSpace(framework)),
		Docroot:        docroot,
		HTTPS:          https,
		EnvFile:        envFile,
		EnvVars:        envVars,
		Database:       database,
		MemoryLimit:    NormalizePHPMemoryLimit(phpMemoryLimitValueString(memoryLimit)),
		PHPExtensions:  bundledExtensions,
		PIEExtensions:  pieExtensions,
		PECLExtensions: PECLExtensionsFromYAML(peclExtensions),
		OPcachePreset:  opcachePreset,
		OPcacheConfig:  NormalizeOPcacheConfigFromYAML(opcacheConfig),
		Server:         server,
		Workers:        NormalizeWorkersConfig(workers),
	}
	if tools != nil {
		environment.PHPVersion = tools.PHPVersion
		environment.PHPZTSVersion = tools.PHPZTSVersion
		environment.FrankenPHPVersion = tools.FrankenPHPVersion
		environment.RoadRunnerVersion = tools.RoadRunnerVersion
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
		if strings.TrimSpace(tools.MeilisearchVersion) != "" {
			environment.Meilisearch = &MeilisearchConfig{Version: tools.MeilisearchVersion}
		}
		if strings.TrimSpace(tools.RedisVersion) != "" {
			environment.Redis = &RedisConfig{Version: tools.RedisVersion}
		}
		if strings.TrimSpace(tools.RabbitMQVersion) != "" {
			environment.RabbitMQ = &RabbitMQConfig{Version: tools.RabbitMQVersion}
		}
		if strings.TrimSpace(tools.TraefikVersion) != "" {
			environment.Traefik = &TraefikConfig{Version: tools.TraefikVersion}
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
	if settings.Meilisearch != nil {
		if environment.Meilisearch == nil {
			environment.Meilisearch = &MeilisearchConfig{}
		}
		environment.Meilisearch.Port = settings.Meilisearch.Port
		environment.Meilisearch.MasterKey = settings.Meilisearch.MasterKey
	}
	if settings.Redis != nil {
		if environment.Redis == nil {
			environment.Redis = &RedisConfig{}
		}
		environment.Redis.Port = settings.Redis.Port
		environment.Redis.Password = settings.Redis.Password
	}
	if settings.RabbitMQ != nil {
		if environment.RabbitMQ == nil {
			environment.RabbitMQ = &RabbitMQConfig{}
		}
		environment.RabbitMQ.Port = settings.RabbitMQ.Port
		environment.RabbitMQ.ManagementPort = settings.RabbitMQ.ManagementPort
		environment.RabbitMQ.Username = settings.RabbitMQ.Username
		environment.RabbitMQ.Password = settings.RabbitMQ.Password
	}
	if settings.Traefik != nil {
		if environment.Traefik == nil {
			environment.Traefik = &TraefikConfig{}
		}
		environment.Traefik.Port = settings.Traefik.Port
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
		RoadRunnerVersion: strings.TrimSpace(environment.RoadRunnerVersion),
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
		Meilisearch:       NormalizeMeilisearchConfig(environment.Meilisearch),
		Redis:             NormalizeRedisConfig(environment.Redis),
		RabbitMQ:          NormalizeRabbitMQConfig(environment.RabbitMQ),
		Traefik:           NormalizeTraefikConfig(environment.Traefik),
		MemoryLimit:       NormalizePHPMemoryLimit(environment.MemoryLimit),
		PHPExtensions:     NormalizePHPExtensions(environment.PHPExtensions),
		PIEExtensions:     NormalizePIEExtensions(environment.PIEExtensions),
		PECLExtensions:    NormalizePECLExtensions(environment.PECLExtensions),
		ZendExtensions:    NormalizePHPExtensions(environment.ZendExtensions),
		OPcachePreset:     NormalizeOPcachePreset(environment.OPcachePreset),
		OPcacheConfig:     NormalizeOPcacheConfig(environment.OPcacheConfig),
		Server:            NormalizeServerConfig(environment.Server),
		Workers:           NormalizeWorkersConfig(environment.Workers),
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

func NormalizeMeilisearchConfig(meilisearch *MeilisearchConfig) *MeilisearchConfig {
	if meilisearch == nil {
		return nil
	}

	normalized := &MeilisearchConfig{
		Version:   strings.TrimSpace(meilisearch.Version),
		Port:      meilisearch.Port,
		MasterKey: strings.TrimSpace(meilisearch.MasterKey),
	}
	if normalized.Version == "" && normalized.Port == 0 && normalized.MasterKey == "" {
		return nil
	}

	return normalized
}

func NormalizeRedisConfig(redis *RedisConfig) *RedisConfig {
	if redis == nil {
		return nil
	}

	normalized := &RedisConfig{
		Version:  strings.TrimSpace(redis.Version),
		Port:     redis.Port,
		Password: strings.TrimSpace(redis.Password),
	}
	if normalized.Version == "" && normalized.Port == 0 && normalized.Password == "" {
		return nil
	}

	return normalized
}

// NormalizeRabbitMQConfig trims scalar values and removes an empty config.
func NormalizeRabbitMQConfig(rabbitMQ *RabbitMQConfig) *RabbitMQConfig {
	if rabbitMQ == nil {
		return nil
	}

	normalized := &RabbitMQConfig{
		Version:        strings.TrimSpace(rabbitMQ.Version),
		Port:           rabbitMQ.Port,
		ManagementPort: rabbitMQ.ManagementPort,
		Username:       strings.TrimSpace(rabbitMQ.Username),
		Password:       strings.TrimSpace(rabbitMQ.Password),
	}
	if normalized.Version == "" && normalized.Port == 0 && normalized.ManagementPort == 0 && normalized.Username == "" && normalized.Password == "" {
		return nil
	}

	return normalized
}

func NormalizeTraefikConfig(traefik *TraefikConfig) *TraefikConfig {
	if traefik == nil {
		return nil
	}

	normalized := &TraefikConfig{
		Version: strings.TrimSpace(traefik.Version),
		Port:    traefik.Port,
	}
	if normalized.Version == "" && normalized.Port == 0 {
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

// NormalizePIEExtensions canonicalizes PIE-managed extension entries: package
// names are lowercased and version constraints trimmed; empty entries drop out.
func NormalizePIEExtensions(extensions map[string]string) map[string]string {
	if len(extensions) == 0 {
		return nil
	}

	normalized := make(map[string]string, len(extensions))
	for name, version := range extensions {
		trimmedName := strings.ToLower(strings.TrimSpace(name))
		trimmedVersion := strings.TrimSpace(version)
		if trimmedName == "" || trimmedVersion == "" {
			continue
		}
		normalized[trimmedName] = trimmedVersion
	}
	if len(normalized) == 0 {
		return nil
	}

	return normalized
}

// NormalizePECLExtensions canonicalizes legacy PECL package names, versions,
// and configure option keys and removes incomplete entries.
func NormalizePECLExtensions(extensions map[string]PECLExtensionConfig) map[string]PECLExtensionConfig {
	if len(extensions) == 0 {
		return nil
	}

	normalized := make(map[string]PECLExtensionConfig, len(extensions))
	for name, extension := range extensions {
		packageName := strings.ToLower(strings.TrimSpace(name))
		version := strings.TrimSpace(extension.Version)
		if packageName == "" || version == "" {
			continue
		}
		options := make(map[string]string, len(extension.ConfigureOptions))
		for option, value := range extension.ConfigureOptions {
			option = strings.TrimLeft(strings.ToLower(strings.TrimSpace(option)), "-")
			if option != "" {
				options[option] = strings.TrimSpace(value)
			}
		}
		if len(options) == 0 {
			options = nil
		}
		normalized[packageName] = PECLExtensionConfig{Version: version, ConfigureOptions: options}
	}
	if len(normalized) == 0 {
		return nil
	}

	return normalized
}

// PECLExtensionsFromYAML converts scalar versions and structured legacy PECL
// package definitions from the public YAML representation.
func PECLExtensionsFromYAML(values map[string]any) map[string]PECLExtensionConfig {
	if len(values) == 0 {
		return nil
	}

	extensions := make(map[string]PECLExtensionConfig, len(values))
	for name, value := range values {
		extension := PECLExtensionConfig{}
		switch typed := value.(type) {
		case map[string]any:
			extension.Version = peclExtensionVersionString(typed["version"])
			switch rawOptions := typed["configure-options"].(type) {
			case map[string]any:
				extension.ConfigureOptions = make(map[string]string, len(rawOptions))
				for option, optionValue := range rawOptions {
					extension.ConfigureOptions[option] = strings.TrimSpace(fmt.Sprint(optionValue))
				}
			case map[string]string:
				extension.ConfigureOptions = make(map[string]string, len(rawOptions))
				for option, optionValue := range rawOptions {
					extension.ConfigureOptions[option] = strings.TrimSpace(optionValue)
				}
			}
		default:
			extension.Version = peclExtensionVersionString(value)
		}
		extensions[name] = extension
	}

	return NormalizePECLExtensions(extensions)
}

// PECLExtensionsFileValue renders scalar values for ordinary packages and an
// object only when reproducible configure options are present.
func PECLExtensionsFileValue(extensions map[string]PECLExtensionConfig) map[string]any {
	normalized := NormalizePECLExtensions(extensions)
	if len(normalized) == 0 {
		return nil
	}

	values := make(map[string]any, len(normalized))
	for name, extension := range normalized {
		if len(extension.ConfigureOptions) == 0 {
			values[name] = extension.Version
			continue
		}
		values[name] = map[string]any{
			"version":           extension.Version,
			"configure-options": extension.ConfigureOptions,
		}
	}

	return values
}

func peclExtensionVersionString(value any) string {
	if value == nil {
		return "*"
	}

	return strings.TrimSpace(fmt.Sprint(value))
}

// SplitPHPExtensionsFromYAML separates the raw php-extensions YAML map into
// bundled extension toggles (name → enabled) and PIE-managed entries
// (vendor/name → version constraint). Malformed values are coerced
// best-effort; validateEnvironmentFileSchema rejects them with clear errors
// before configs reach this conversion.
func SplitPHPExtensionsFromYAML(values map[string]any) (map[string]bool, map[string]string) {
	if len(values) == 0 {
		return nil, nil
	}

	bundled := map[string]bool{}
	pie := map[string]string{}
	for key, value := range values {
		name := strings.ToLower(strings.TrimSpace(key))
		if name == "" {
			continue
		}
		if strings.Contains(name, "/") {
			pie[name] = pieExtensionVersionString(value)
			continue
		}
		if enabled, ok := value.(bool); ok {
			bundled[name] = enabled
			continue
		}
		// Coerce scalar strings such as "true"; anything else reads as enabled.
		bundled[name] = !strings.EqualFold(strings.TrimSpace(fmt.Sprint(value)), "false")
	}
	if len(bundled) == 0 {
		bundled = nil
	}
	if len(pie) == 0 {
		pie = nil
	}

	return bundled, pie
}

// PHPExtensionsFileValue merges bundled toggles and PIE-managed entries back
// into the shared php-extensions YAML wire shape.
func PHPExtensionsFileValue(bundled map[string]bool, pie map[string]string) map[string]any {
	if len(bundled) == 0 && len(pie) == 0 {
		return nil
	}

	values := make(map[string]any, len(bundled)+len(pie))
	for name, enabled := range NormalizePHPExtensions(bundled) {
		values[name] = enabled
	}
	for name, version := range NormalizePIEExtensions(pie) {
		values[name] = version
	}

	return values
}

// pieExtensionVersionString coerces a YAML scalar into a version constraint
// string; nil (a bare key) means "any version".
func pieExtensionVersionString(value any) string {
	if value == nil {
		return "*"
	}

	return strings.TrimSpace(fmt.Sprint(value))
}

// validPIEExtensionPackage matches Composer package names (vendor/name).
var validPIEExtensionPackage = regexp.MustCompile(`^[a-z0-9]([_.-]?[a-z0-9]+)*/[a-z0-9](([_.]|-{1,2})?[a-z0-9]+)*$`)

// ValidatePIEExtensionPackage checks a PIE-managed extension key uses the
// Composer vendor/name package shape.
func ValidatePIEExtensionPackage(name string) error {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if trimmed == "" {
		return fmt.Errorf("php extension package cannot be empty")
	}
	if !validPIEExtensionPackage.MatchString(trimmed) {
		return fmt.Errorf("invalid php extension package %q: use the Composer vendor/name shape, such as xdebug/xdebug", name)
	}

	return nil
}

// ValidatePIEExtensionVersion checks a PIE-managed extension version
// constraint is a single-line Composer-style constraint.
func ValidatePIEExtensionVersion(pkg, version string) error {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return fmt.Errorf("php extension %s requires a version constraint; use * for the latest version", pkg)
	}
	if strings.EqualFold(trimmed, "true") || strings.EqualFold(trimmed, "false") {
		return fmt.Errorf("invalid version %q for php extension %s: use a Composer version constraint such as 1.2 or *", version, pkg)
	}
	if strings.ContainsAny(trimmed, " \t\r\n\"'") {
		return fmt.Errorf("invalid version %q for php extension %s: constraints must be single-line scalars", version, pkg)
	}

	return nil
}

var validPECLExtensionPackage = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidatePECLExtensionPackage validates one package name from the legacy
// PECL channel. PIE-style vendor/name packages are deliberately excluded.
func ValidatePECLExtensionPackage(name string) error {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if trimmed == "" {
		return fmt.Errorf("PECL extension package cannot be empty")
	}
	if !validPECLExtensionPackage.MatchString(trimmed) {
		return fmt.Errorf("invalid PECL extension package %q: use a bare package name such as redis; prefer a PIE vendor/name package when available", name)
	}

	return nil
}

// ValidatePECLExtensionVersion validates an exact PECL release or the latest
// stable marker. PECL does not accept Composer-style version constraints.
func ValidatePECLExtensionVersion(pkg, version string) error {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return fmt.Errorf("PECL extension %s requires a version; use * for the latest compatible stable release", pkg)
	}
	if trimmed == "*" {
		return nil
	}
	if strings.ContainsAny(trimmed, " \t\r\n\"'") || strings.ContainsAny(trimmed, "^~<>,") {
		return fmt.Errorf("invalid PECL version %q for %s: use an exact release such as 6.2.0 or *", version, pkg)
	}

	return nil
}

// NormalizePHPMemoryLimit trims a PHP memory_limit value and canonicalizes its unit suffix.
func NormalizePHPMemoryLimit(value string) string {
	normalized := strings.TrimSpace(value)
	if len(normalized) < 2 {
		return normalized
	}

	unit := normalized[len(normalized)-1]
	switch unit {
	case 'k', 'm', 'g':
		return normalized[:len(normalized)-1] + strings.ToUpper(string(unit))
	default:
		return normalized
	}
}

// ValidatePHPMemoryLimit checks the supported php.ini memory_limit value shape.
func ValidatePHPMemoryLimit(value string) error {
	normalized := NormalizePHPMemoryLimit(value)
	if normalized == "" || normalized == "-1" {
		return nil
	}

	digits := normalized
	unit := normalized[len(normalized)-1]
	switch unit {
	case 'K', 'M', 'G':
		digits = normalized[:len(normalized)-1]
	}
	if digits == "" {
		return fmt.Errorf("invalid memory-limit %q: use -1, bytes, or PHP shorthand such as 512M", value)
	}
	for _, char := range digits {
		if char < '0' || char > '9' {
			return fmt.Errorf("invalid memory-limit %q: use -1, bytes, or PHP shorthand such as 512M", value)
		}
	}

	return nil
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

func phpMemoryLimitValueString(value any) string {
	if value == nil {
		return ""
	}

	return strings.TrimSpace(fmt.Sprint(value))
}

func phpMemoryLimitFileValue(value string) any {
	normalized := NormalizePHPMemoryLimit(value)
	if normalized == "" {
		return nil
	}

	return normalized
}
