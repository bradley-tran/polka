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

// ToolsConfig maps managed tool IDs to version labels inside an environment file.
type ToolsConfig map[string]string

// SettingsConfig maps a settings namespace (typically a tool ID) to versionless
// scalar settings inside an environment file.
type SettingsConfig map[string]map[string]any

// ProjectFile is the YAML shape of polka.yaml, which also defines the default environment.
type ProjectFile struct {
	Version        int                     `yaml:"version"`
	Root           string                  `yaml:"root"`
	Framework      string                  `yaml:"framework,omitempty"`
	Tools          ToolsConfig             `yaml:"tools,omitempty"`
	Settings       SettingsConfig          `yaml:"settings,omitempty"`
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
	Tools          ToolsConfig             `yaml:"tools,omitempty"`
	Settings       SettingsConfig          `yaml:"settings,omitempty"`
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

// Environment is the normalized in-memory model for a Polka environment.
// Managed tool versions live in Tools and versionless tool options live in
// Settings; service-specific helper methods expose typed read-only views.
type Environment struct {
	Name           string                         `yaml:"-"`
	Framework      string                         `yaml:"framework,omitempty"`
	Tools          ToolsConfig                    `yaml:"tools,omitempty"`
	Settings       SettingsConfig                 `yaml:"settings,omitempty"`
	Docroot        string                         `yaml:"docroot,omitempty"`
	HTTPS          bool                           `yaml:"https,omitempty"`
	EnvFile        string                         `yaml:"env-file,omitempty"`
	EnvVars        map[string]string              `yaml:"env-vars,omitempty"`
	Database       *DatabaseConfig                `yaml:"database,omitempty"`
	MemoryLimit    string                         `yaml:"memory-limit,omitempty"`
	PHPExtensions  map[string]bool                `yaml:"php-extensions,omitempty"`
	PIEExtensions  map[string]string              `yaml:"-"`
	PECLExtensions map[string]PECLExtensionConfig `yaml:"-"`
	ZendExtensions map[string]bool                `yaml:"-"`
	OPcachePreset  string                         `yaml:"opcache-preset,omitempty"`
	OPcacheConfig  map[string]string              `yaml:"opcache-config,omitempty"`
	Server         *ServerConfig                  `yaml:"server,omitempty"`
	Workers        map[string]WorkerConfig        `yaml:"workers,omitempty"`
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

// MailpitConfig is the read-only typed view of Mailpit tool and runtime settings.
type MailpitConfig struct {
	Version  string `yaml:"version,omitempty"`
	SMTPPort int    `yaml:"smtp-port,omitempty"`
	UIPort   int    `yaml:"ui-port,omitempty"`
	HTTPS    bool   `yaml:"https,omitempty"`
}

// PHPMyAdminConfig is the read-only typed view of phpMyAdmin tool and runtime settings.
type PHPMyAdminConfig struct {
	Version string `yaml:"version,omitempty"`
	Port    int    `yaml:"port,omitempty"`
	HTTPS   bool   `yaml:"https,omitempty"`
}

// MeilisearchConfig is the read-only typed view of Meilisearch tool and runtime settings.
type MeilisearchConfig struct {
	Version   string `yaml:"version,omitempty"`
	Port      int    `yaml:"port,omitempty"`
	MasterKey string `yaml:"master-key,omitempty"`
}

// RedisConfig is the read-only typed view of Redis tool and runtime settings.
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

// TraefikConfig is the read-only typed view of Traefik tool and runtime settings.
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

// ToolVersion returns the configured version label for tool, or an empty string
// when the tool is not configured on the environment.
func (e Environment) ToolVersion(tool string) string {
	if len(e.Tools) == 0 {
		return ""
	}
	key := strings.ToLower(strings.TrimSpace(tool))
	if key == "" {
		return ""
	}
	if version, ok := e.Tools[key]; ok {
		return strings.TrimSpace(version)
	}
	for rawKey, version := range e.Tools {
		if strings.EqualFold(strings.TrimSpace(rawKey), key) {
			return strings.TrimSpace(version)
		}
	}

	return ""
}

// SetToolVersion records or clears a managed tool version on the environment.
// An empty version removes the tool entry.
func (e *Environment) SetToolVersion(tool, version string) {
	if e == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(tool))
	if key == "" {
		return
	}
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		if len(e.Tools) == 0 {
			return
		}
		delete(e.Tools, key)
		for rawKey := range e.Tools {
			if strings.EqualFold(strings.TrimSpace(rawKey), key) {
				delete(e.Tools, rawKey)
			}
		}
		if len(e.Tools) == 0 {
			e.Tools = nil
		}
		return
	}
	if e.Tools == nil {
		e.Tools = ToolsConfig{}
	}
	for rawKey := range e.Tools {
		if rawKey != key && strings.EqualFold(strings.TrimSpace(rawKey), key) {
			delete(e.Tools, rawKey)
		}
	}
	e.Tools[key] = trimmed
}

// Setting returns the raw setting value stored under settings.<namespace>.<key>.
func (e Environment) Setting(namespace, key string) (any, bool) {
	if len(e.Settings) == 0 {
		return nil, false
	}
	ns := strings.ToLower(strings.TrimSpace(namespace))
	k := strings.ToLower(strings.TrimSpace(key))
	if ns == "" || k == "" {
		return nil, false
	}
	if nsMap, ok := e.Settings[ns]; ok {
		if value, ok := nsMap[k]; ok {
			return value, true
		}
		for rawKey, value := range nsMap {
			if strings.EqualFold(strings.TrimSpace(rawKey), k) {
				return value, true
			}
		}
		return nil, false
	}
	for rawNS, nsMap := range e.Settings {
		if !strings.EqualFold(strings.TrimSpace(rawNS), ns) {
			continue
		}
		if value, ok := nsMap[k]; ok {
			return value, true
		}
		for rawKey, value := range nsMap {
			if strings.EqualFold(strings.TrimSpace(rawKey), k) {
				return value, true
			}
		}
	}

	return nil, false
}

// SettingString returns a trimmed string setting under settings.<namespace>.<key>.
func (e Environment) SettingString(namespace, key string) string {
	value, ok := e.Setting(namespace, key)
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}

	return strings.TrimSpace(fmt.Sprint(value))
}

// SettingInt returns an integer setting under settings.<namespace>.<key>, or 0
// when unset or non-numeric.
func (e Environment) SettingInt(namespace, key string) int {
	value, ok := e.Setting(namespace, key)
	if !ok || value == nil {
		return 0
	}
	if parsed, ok := settingIntValue(value); ok {
		return parsed
	}

	return 0
}

// SettingBool returns a boolean setting under settings.<namespace>.<key>.
func (e Environment) SettingBool(namespace, key string) bool {
	value, ok := e.Setting(namespace, key)
	if !ok || value == nil {
		return false
	}
	if enabled, ok := value.(bool); ok {
		return enabled
	}

	return false
}

// SetSetting sets or clears a versionless setting under settings.<namespace>.<key>.
// Zero values ("", 0, false, nil) remove the key and prune empty maps.
func (e *Environment) SetSetting(namespace, key string, value any) {
	if e == nil {
		return
	}
	ns := strings.ToLower(strings.TrimSpace(namespace))
	k := strings.ToLower(strings.TrimSpace(key))
	if ns == "" || k == "" {
		return
	}
	normalized, ok := normalizeSettingValue(value)
	if !ok {
		if len(e.Settings) == 0 {
			return
		}
		if nsMap, exists := e.Settings[ns]; exists {
			delete(nsMap, k)
			for rawKey := range nsMap {
				if strings.EqualFold(strings.TrimSpace(rawKey), k) {
					delete(nsMap, rawKey)
				}
			}
			if len(nsMap) == 0 {
				delete(e.Settings, ns)
			}
		}
		if len(e.Settings) == 0 {
			e.Settings = nil
		}
		return
	}
	if e.Settings == nil {
		e.Settings = SettingsConfig{}
	}
	if e.Settings[ns] == nil {
		e.Settings[ns] = map[string]any{}
	}
	for rawKey := range e.Settings[ns] {
		if rawKey != k && strings.EqualFold(strings.TrimSpace(rawKey), k) {
			delete(e.Settings[ns], rawKey)
		}
	}
	e.Settings[ns][k] = normalized
}

// PHPBuildTools reports whether settings.php.extension-sdk is enabled.
func (e Environment) PHPBuildTools() bool {
	return e.SettingBool("php", "extension-sdk")
}

// Mailpit returns the typed Mailpit view, or nil when neither tools.mailpit nor
// settings.mailpit is configured.
func (e Environment) Mailpit() *MailpitConfig {
	version := e.ToolVersion("mailpit")
	smtpPort := e.SettingInt("mailpit", "smtp-port")
	uiPort := e.SettingInt("mailpit", "ui-port")
	if version == "" && smtpPort == 0 && uiPort == 0 {
		return nil
	}

	return &MailpitConfig{
		Version:  version,
		SMTPPort: smtpPort,
		UIPort:   uiPort,
		HTTPS:    e.HTTPS,
	}
}

// PHPMyAdmin returns the typed phpMyAdmin view, or nil when neither
// tools.phpmyadmin nor settings.phpmyadmin is configured.
func (e Environment) PHPMyAdmin() *PHPMyAdminConfig {
	version := e.ToolVersion("phpmyadmin")
	port := e.SettingInt("phpmyadmin", "port")
	if version == "" && port == 0 {
		return nil
	}

	return &PHPMyAdminConfig{
		Version: version,
		Port:    port,
		HTTPS:   e.HTTPS,
	}
}

// Meilisearch returns the typed Meilisearch view, or nil when neither
// tools.meilisearch nor settings.meilisearch is configured.
func (e Environment) Meilisearch() *MeilisearchConfig {
	version := e.ToolVersion("meilisearch")
	port := e.SettingInt("meilisearch", "port")
	masterKey := e.SettingString("meilisearch", "master-key")
	if version == "" && port == 0 && masterKey == "" {
		return nil
	}

	return &MeilisearchConfig{
		Version:   version,
		Port:      port,
		MasterKey: masterKey,
	}
}

// Redis returns the typed Redis view, or nil when neither tools.redis nor
// settings.redis is configured.
func (e Environment) Redis() *RedisConfig {
	version := e.ToolVersion("redis")
	port := e.SettingInt("redis", "port")
	password := e.SettingString("redis", "password")
	if version == "" && port == 0 && password == "" {
		return nil
	}

	return &RedisConfig{
		Version:  version,
		Port:     port,
		Password: password,
	}
}

// RabbitMQ returns the typed RabbitMQ view, or nil when neither tools.rabbitmq
// nor settings.rabbitmq is configured.
func (e Environment) RabbitMQ() *RabbitMQConfig {
	version := e.ToolVersion("rabbitmq")
	port := e.SettingInt("rabbitmq", "port")
	managementPort := e.SettingInt("rabbitmq", "management-port")
	username := e.SettingString("rabbitmq", "username")
	password := e.SettingString("rabbitmq", "password")
	if version == "" && port == 0 && managementPort == 0 && username == "" && password == "" {
		return nil
	}

	return &RabbitMQConfig{
		Version:        version,
		Port:           port,
		ManagementPort: managementPort,
		Username:       username,
		Password:       password,
	}
}

// Traefik returns the typed Traefik view, or nil when neither tools.traefik nor
// settings.traefik is configured.
func (e Environment) Traefik() *TraefikConfig {
	version := e.ToolVersion("traefik")
	port := e.SettingInt("traefik", "port")
	if version == "" && port == 0 {
		return nil
	}

	return &TraefikConfig{
		Version: version,
		Port:    port,
	}
}

// PrimaryPHPTool returns the configured PHP runtime tool and version. The
// caller must validate mutual exclusion when both runtime entries are present.
func PrimaryPHPTool(environment Environment) (string, string) {
	if version := environment.ToolVersion("php-zts"); version != "" {
		return "php-zts", version
	}
	if version := environment.ToolVersion("php"); version != "" {
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
	if version := environment.ToolVersion(ServerTypeFrankenPHP); version != "" {
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
	return ProjectFile{
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

// ToolsConfigFromEnvironment extracts normalized managed tool versions from an environment.
func ToolsConfigFromEnvironment(environment Environment) ToolsConfig {
	environment = populateDatabaseToolVersion(environment)
	return NormalizeToolsConfig(environment.Tools)
}

// SettingsConfigFromEnvironment extracts normalized versionless tool settings from an environment.
func SettingsConfigFromEnvironment(environment Environment) SettingsConfig {
	return NormalizeSettingsConfig(environment.Settings)
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

// IsZero reports whether no managed tool versions are configured.
func (tools ToolsConfig) IsZero() bool {
	return len(NormalizeToolsConfig(tools)) == 0
}

// IsZero reports whether no versionless tool settings are configured.
func (settings SettingsConfig) IsZero() bool {
	return len(NormalizeSettingsConfig(settings)) == 0
}

// NormalizeToolsConfig canonicalizes tool IDs to lowercase, trims versions, and
// removes empty entries.
func NormalizeToolsConfig(tools ToolsConfig) ToolsConfig {
	if len(tools) == 0 {
		return nil
	}

	normalized := make(ToolsConfig, len(tools))
	for rawKey, rawVersion := range tools {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		version := strings.TrimSpace(rawVersion)
		if key == "" || version == "" {
			continue
		}
		normalized[key] = version
	}
	if len(normalized) == 0 {
		return nil
	}

	return normalized
}

// NormalizeSettingsConfig canonicalizes namespaces and keys to lowercase, trims
// strings, normalizes integer types to int, and drops zero values.
func NormalizeSettingsConfig(settings SettingsConfig) SettingsConfig {
	if len(settings) == 0 {
		return nil
	}

	normalized := make(SettingsConfig, len(settings))
	for rawNamespace, rawValues := range settings {
		namespace := strings.ToLower(strings.TrimSpace(rawNamespace))
		if namespace == "" || len(rawValues) == 0 {
			continue
		}
		values := make(map[string]any, len(rawValues))
		for rawKey, rawValue := range rawValues {
			key := strings.ToLower(strings.TrimSpace(rawKey))
			if key == "" {
				continue
			}
			if value, ok := normalizeSettingValue(rawValue); ok {
				values[key] = value
			}
		}
		if len(values) > 0 {
			normalized[namespace] = values
		}
	}
	if len(normalized) == 0 {
		return nil
	}

	return normalized
}

func normalizeSettingValue(value any) (any, bool) {
	if value == nil {
		return nil, false
	}
	switch typed := value.(type) {
	case bool:
		if !typed {
			return nil, false
		}
		return true, true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil, false
		}
		return trimmed, true
	default:
		if parsed, ok := settingIntValue(value); ok {
			if parsed == 0 {
				return nil, false
			}
			return parsed, true
		}
		return value, true
	}
}

func settingIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	default:
		return 0, false
	}
}

func environmentFromFileParts(name string, framework string, tools ToolsConfig, settings SettingsConfig, docroot string, https bool, envFile string, envVars map[string]string, database *DatabaseConfig, memoryLimit any, phpExtensions map[string]any, peclExtensions map[string]any, opcachePreset string, opcacheConfig map[string]any, server *ServerConfig, workers map[string]WorkerConfig) Environment {
	bundledExtensions, pieExtensions := SplitPHPExtensionsFromYAML(phpExtensions)
	normalizedTools := NormalizeToolsConfig(tools)
	environment := Environment{
		Name:           name,
		Framework:      strings.ToLower(strings.TrimSpace(framework)),
		Tools:          normalizedTools,
		Settings:       NormalizeSettingsConfig(settings),
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
	if len(normalizedTools) > 0 {
		environment.Database = PrimaryDatabaseConfigFromTools(normalizedTools, database)
	}
	environment = populateDatabaseToolVersion(environment)

	return inheritEnvironmentHTTPS(environment)
}

func populateDatabaseToolVersion(environment Environment) Environment {
	if environment.Database == nil || strings.TrimSpace(environment.Database.Version) == "" {
		return environment
	}

	engine := strings.ToLower(strings.TrimSpace(environment.Database.Engine))
	switch engine {
	case "mysql", "mariadb", "postgresql":
		if environment.ToolVersion(engine) == "" {
			environment.SetToolVersion(engine, environment.Database.Version)
		}
	}

	return environment
}

// DatabaseToolVersion returns the configured database version for the requested engine.
func DatabaseToolVersion(environment Environment, engine string) string {
	normalized := strings.ToLower(strings.TrimSpace(engine))
	switch normalized {
	case "mysql", "mariadb", "postgresql":
		if version := environment.ToolVersion(normalized); version != "" {
			return version
		}
	default:
		return ""
	}
	if environment.Database != nil && strings.EqualFold(strings.TrimSpace(environment.Database.Engine), normalized) {
		return strings.TrimSpace(environment.Database.Version)
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
func PrimaryDatabaseConfigFromTools(tools ToolsConfig, database *DatabaseConfig) *DatabaseConfig {
	normalizedTools := NormalizeToolsConfig(tools)
	if len(normalizedTools) == 0 {
		return database
	}

	merged := &DatabaseConfig{}
	if database != nil {
		*merged = *database
	}
	mysqlVersion := normalizedTools["mysql"]
	mariaDBVersion := normalizedTools["mariadb"]
	postgreSQLVersion := normalizedTools["postgresql"]
	if merged.Engine == "" {
		switch {
		case mysqlVersion != "" && mariaDBVersion == "" && postgreSQLVersion == "":
			merged.Engine = "mysql"
		case mariaDBVersion != "" && mysqlVersion == "" && postgreSQLVersion == "":
			merged.Engine = "mariadb"
		case postgreSQLVersion != "" && mysqlVersion == "" && mariaDBVersion == "":
			merged.Engine = "postgresql"
		}
	}
	if merged.Version == "" {
		switch strings.ToLower(strings.TrimSpace(merged.Engine)) {
		case "mysql":
			merged.Version = mysqlVersion
		case "mariadb":
			merged.Version = mariaDBVersion
		case "postgresql":
			merged.Version = postgreSQLVersion
		}
	}

	return NormalizeDatabaseConfig(merged)
}

func NormalizeEnvironment(name string, environment Environment) Environment {
	normalized := Environment{
		Name:           name,
		Framework:      strings.ToLower(strings.TrimSpace(environment.Framework)),
		Tools:          NormalizeToolsConfig(environment.Tools),
		Settings:       NormalizeSettingsConfig(environment.Settings),
		Docroot:        strings.TrimSpace(environment.Docroot),
		HTTPS:          environment.HTTPS,
		EnvFile:        strings.TrimSpace(environment.EnvFile),
		EnvVars:        NormalizeEnvironmentVariables(environment.EnvVars),
		Database:       NormalizeDatabaseConfig(environment.Database),
		MemoryLimit:    NormalizePHPMemoryLimit(environment.MemoryLimit),
		PHPExtensions:  NormalizePHPExtensions(environment.PHPExtensions),
		PIEExtensions:  NormalizePIEExtensions(environment.PIEExtensions),
		PECLExtensions: NormalizePECLExtensions(environment.PECLExtensions),
		ZendExtensions: NormalizePHPExtensions(environment.ZendExtensions),
		OPcachePreset:  NormalizeOPcachePreset(environment.OPcachePreset),
		OPcacheConfig:  NormalizeOPcacheConfig(environment.OPcacheConfig),
		Server:         NormalizeServerConfig(environment.Server),
		Workers:        NormalizeWorkersConfig(environment.Workers),
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
