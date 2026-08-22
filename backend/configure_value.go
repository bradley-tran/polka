package backend

import (
	"fmt"
	"strconv"
	"strings"

	"polka/config"
)

// ConfigureValue creates or updates a single schema-aware config value for an
// environment, then refreshes the managed command shims for the loaded config.
func (s Store) ConfigureValue(name, key, value string) (Environment, error) {
	if err := s.Init(); err != nil {
		return Environment{}, err
	}
	if err := validateName(name); err != nil {
		return Environment{}, err
	}

	path, err := parseConfigValuePath(key)
	if err != nil {
		return Environment{}, err
	}

	storedEnvironment, _, err := s.readEnvironment(name)
	if err != nil {
		return Environment{}, err
	}

	environment := s.normalizeEnvironment(name, storedEnvironment)
	if err := applyConfigValue(&environment, path, value); err != nil {
		return Environment{}, err
	}
	environment = normalizeConfigValueEnvironment(environment)
	if err := s.validateEnvironmentFramework(environment); err != nil {
		return Environment{}, err
	}
	if err := s.toolRegistry().ValidateEnvironment(environment); err != nil {
		return Environment{}, err
	}

	if err := s.writeEnvironmentConfig(name, environment); err != nil {
		return Environment{}, fmt.Errorf("write config file: %w", err)
	}
	loadedConfig, err := s.loadConfig()
	if err != nil {
		return Environment{}, err
	}
	if err := s.syncManagedBinaries(loadedConfig); err != nil {
		return Environment{}, fmt.Errorf("sync managed binaries: %w", err)
	}

	return environment, nil
}

func parseConfigValuePath(key string) ([]string, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return nil, fmt.Errorf("config key cannot be empty")
	}
	if strings.HasPrefix(trimmed, "opcache-config.") {
		directive := strings.TrimSpace(strings.TrimPrefix(trimmed, "opcache-config."))
		if directive == "" {
			return nil, fmt.Errorf("config key %q must include an OPcache directive", key)
		}

		return []string{"opcache-config", directive}, nil
	}
	// Split php-extensions keys only once: PIE package names such as
	// vendor/my.ext may legitimately contain dots.
	if strings.HasPrefix(trimmed, "php-extensions.") {
		name := strings.TrimSpace(strings.TrimPrefix(trimmed, "php-extensions."))
		if name == "" {
			return nil, fmt.Errorf("config key %q must include a PHP extension name", key)
		}

		return []string{"php-extensions", name}, nil
	}
	if strings.HasPrefix(trimmed, "pecl-extensions.") {
		name := strings.TrimSpace(strings.TrimPrefix(trimmed, "pecl-extensions."))
		if name == "" {
			return nil, fmt.Errorf("config key %q must include a PECL extension name", key)
		}

		return []string{"pecl-extensions", name}, nil
	}

	path := strings.Split(trimmed, ".")
	for _, segment := range path {
		if strings.TrimSpace(segment) == "" {
			return nil, fmt.Errorf("config key %q contains an empty path segment", key)
		}
	}

	return path, nil
}

func applyConfigValue(environment *Environment, path []string, value string) error {
	if len(path) == 1 {
		return applyTopLevelConfigValue(environment, path[0], value)
	}

	switch path[0] {
	case "tools":
		return applyToolConfigValue(environment, path, value)
	case "database":
		return applyDatabaseConfigValue(environment, path, value)
	case "server":
		return applyServerConfigValue(environment, path, value)
	case "settings":
		return applySettingsConfigValue(environment, path, value)
	case "env-vars":
		return applyEnvVarConfigValue(environment, path, value)
	case "php-extensions":
		return applyPHPExtensionConfigValue(environment, path, value)
	case "pecl-extensions":
		return applyPECLExtensionConfigValue(environment, path, value)
	case "opcache-config":
		return applyOPcacheConfigValue(environment, path, value)
	default:
		return unsupportedConfigKey(path)
	}
}

// applyPECLExtensionConfigValue updates a scalar legacy PECL package entry.
func applyPECLExtensionConfigValue(environment *Environment, path []string, value string) error {
	if len(path) != 2 {
		return unsupportedConfigKey(path)
	}
	name := strings.ToLower(strings.TrimSpace(path[1]))
	if err := config.ValidatePECLExtensionPackage(name); err != nil {
		return err
	}
	version := strings.TrimSpace(value)
	if version == "" {
		delete(environment.PECLExtensions, name)
		return nil
	}
	if err := config.ValidatePECLExtensionVersion(name, version); err != nil {
		return err
	}
	if environment.PECLExtensions == nil {
		environment.PECLExtensions = map[string]config.PECLExtensionConfig{}
	}
	environment.PECLExtensions[name] = config.PECLExtensionConfig{Version: version}

	return nil
}

// ConfigurePECLExtension records a resolved legacy PECL extension including
// reproducible configure options. An empty version removes the package.
func (s Store) ConfigurePECLExtension(name, packageName string, extension config.PECLExtensionConfig) (Environment, error) {
	if err := s.Init(); err != nil {
		return Environment{}, err
	}
	if err := validateName(name); err != nil {
		return Environment{}, err
	}
	packageName = strings.ToLower(strings.TrimSpace(packageName))
	if err := config.ValidatePECLExtensionPackage(packageName); err != nil {
		return Environment{}, err
	}

	stored, _, err := s.readEnvironment(name)
	if err != nil {
		return Environment{}, err
	}
	environment := s.normalizeEnvironment(name, stored)
	if strings.TrimSpace(extension.Version) == "" {
		delete(environment.PECLExtensions, packageName)
	} else {
		if err := config.ValidatePECLExtensionVersion(packageName, extension.Version); err != nil {
			return Environment{}, err
		}
		if environment.PECLExtensions == nil {
			environment.PECLExtensions = map[string]config.PECLExtensionConfig{}
		}
		environment.PECLExtensions[packageName] = extension
	}
	environment = normalizeConfigValueEnvironment(environment)
	if err := s.validateEnvironmentFramework(environment); err != nil {
		return Environment{}, err
	}
	if err := s.toolRegistry().ValidateEnvironment(environment); err != nil {
		return Environment{}, err
	}
	if err := s.writeEnvironmentConfig(name, environment); err != nil {
		return Environment{}, fmt.Errorf("write config file: %w", err)
	}
	loadedConfig, err := s.loadConfig()
	if err != nil {
		return Environment{}, err
	}
	if err := s.syncManagedBinaries(loadedConfig); err != nil {
		return Environment{}, fmt.Errorf("sync managed binaries: %w", err)
	}

	return environment, nil
}

func applyTopLevelConfigValue(environment *Environment, key, value string) error {
	switch key {
	case "framework":
		environment.Framework = strings.TrimSpace(value)
	case "docroot":
		environment.Docroot = strings.TrimSpace(value)
	case "https":
		parsed, err := parseConfigBoolValue(key, value)
		if err != nil {
			return err
		}
		environment.HTTPS = parsed
	case "env-file":
		environment.EnvFile = strings.TrimSpace(value)
	case "memory-limit":
		environment.MemoryLimit = strings.TrimSpace(value)
	case "opcache-preset":
		environment.OPcachePreset = strings.TrimSpace(value)
	default:
		return unsupportedConfigKey([]string{key})
	}

	return nil
}

func applyToolConfigValue(environment *Environment, path []string, value string) error {
	if len(path) != 2 {
		return unsupportedConfigKey(path)
	}

	version := strings.TrimSpace(value)
	switch path[1] {
	case toolPHP:
		environment.PHPVersion = version
		if version != "" {
			environment.PHPZTSVersion = ""
		}
	case toolPHPZTS:
		environment.PHPZTSVersion = version
		if version != "" {
			environment.PHPVersion = ""
		}
	case toolFrankenPHP:
		environment.FrankenPHPVersion = version
	case toolRoadRunner:
		environment.RoadRunnerVersion = version
	case toolComposer:
		environment.ComposerVersion = version
	case toolPIE:
		return fmt.Errorf("pie is managed internally by polka and cannot be configured in a project environment; use 'polka ext' to manage PHP extensions")
	case toolNodeJS:
		environment.NodeJSVersion = version
	case toolMago:
		environment.MagoVersion = version
	case toolNginx:
		environment.NginxVersion = version
	case toolApache:
		environment.ApacheVersion = version
	case toolMySQL:
		environment.MySQLVersion = version
	case toolMariaDB:
		environment.MariaDBVersion = version
	case toolPostgreSQL:
		environment.PostgreSQLVersion = version
	case toolSQLite:
		environment.SQLiteVersion = version
	case toolMailpit:
		if environment.Mailpit == nil {
			environment.Mailpit = &MailpitConfig{}
		}
		environment.Mailpit.Version = version
	case toolPHPMyAdmin:
		if environment.PHPMyAdmin == nil {
			environment.PHPMyAdmin = &PHPMyAdminConfig{}
		}
		environment.PHPMyAdmin.Version = version
	case toolMeilisearch:
		if environment.Meilisearch == nil {
			environment.Meilisearch = &MeilisearchConfig{}
		}
		environment.Meilisearch.Version = version
	case toolRedis:
		if environment.Redis == nil {
			environment.Redis = &RedisConfig{}
		}
		environment.Redis.Version = version
	case toolRabbitMQ:
		if environment.RabbitMQ == nil {
			environment.RabbitMQ = &RabbitMQConfig{}
		}
		environment.RabbitMQ.Version = version
	case toolTraefik:
		if environment.Traefik == nil {
			environment.Traefik = &TraefikConfig{}
		}
		environment.Traefik.Version = version
	default:
		return unsupportedConfigKey(path)
	}

	return nil
}

func applyDatabaseConfigValue(environment *Environment, path []string, value string) error {
	if len(path) != 2 {
		return unsupportedConfigKey(path)
	}
	if environment.Database == nil {
		environment.Database = &DatabaseConfig{}
	}

	switch path[1] {
	case "engine":
		environment.Database.Engine = strings.TrimSpace(value)
	case "port":
		port, err := parseConfigPortValue(strings.Join(path, "."), value)
		if err != nil {
			return err
		}
		environment.Database.Port = port
	default:
		return unsupportedConfigKey(path)
	}

	return nil
}

func applyServerConfigValue(environment *Environment, path []string, value string) error {
	if len(path) != 2 {
		return unsupportedConfigKey(path)
	}
	if environment.Server == nil {
		environment.Server = &ServerConfig{}
	}

	switch path[1] {
	case "type":
		typeName := strings.ToLower(strings.TrimSpace(value))
		switch typeName {
		case "", config.ServerTypePHP, config.ServerTypeNginx, config.ServerTypeApache, config.ServerTypeFrankenPHP:
			environment.Server.Type = typeName
		default:
			return fmt.Errorf("unsupported server type %q: use php, nginx, apache, or frankenphp", value)
		}
	case "hostname":
		environment.Server.Hostname = strings.TrimSpace(value)
	case "port":
		port, err := parseConfigPortValue(strings.Join(path, "."), value)
		if err != nil {
			return err
		}
		environment.Server.Port = port
	default:
		return unsupportedConfigKey(path)
	}

	return nil
}

func applySettingsConfigValue(environment *Environment, path []string, value string) error {
	if len(path) != 3 {
		return unsupportedConfigKey(path)
	}

	switch path[1] {
	case toolPHP:
		if path[2] != "extension-sdk" {
			return unsupportedConfigKey(path)
		}
		enabled, err := parseConfigBoolValue(strings.Join(path, "."), value)
		if err != nil {
			return err
		}
		environment.PHPBuildTools = enabled
	case toolMailpit:
		if environment.Mailpit == nil {
			environment.Mailpit = &MailpitConfig{}
		}
		switch path[2] {
		case "smtp-port":
			port, err := parseConfigPortValue(strings.Join(path, "."), value)
			if err != nil {
				return err
			}
			environment.Mailpit.SMTPPort = port
		case "ui-port":
			port, err := parseConfigPortValue(strings.Join(path, "."), value)
			if err != nil {
				return err
			}
			environment.Mailpit.UIPort = port
		default:
			return unsupportedConfigKey(path)
		}
	case toolPHPMyAdmin:
		if environment.PHPMyAdmin == nil {
			environment.PHPMyAdmin = &PHPMyAdminConfig{}
		}
		if path[2] != "port" {
			return unsupportedConfigKey(path)
		}
		port, err := parseConfigPortValue(strings.Join(path, "."), value)
		if err != nil {
			return err
		}
		environment.PHPMyAdmin.Port = port
	case toolMeilisearch:
		if environment.Meilisearch == nil {
			environment.Meilisearch = &MeilisearchConfig{}
		}
		switch path[2] {
		case "port":
			port, err := parseConfigPortValue(strings.Join(path, "."), value)
			if err != nil {
				return err
			}
			environment.Meilisearch.Port = port
		case "master-key":
			environment.Meilisearch.MasterKey = strings.TrimSpace(value)
		default:
			return unsupportedConfigKey(path)
		}
	case toolRedis:
		if environment.Redis == nil {
			environment.Redis = &RedisConfig{}
		}
		switch path[2] {
		case "port":
			port, err := parseConfigPortValue(strings.Join(path, "."), value)
			if err != nil {
				return err
			}
			environment.Redis.Port = port
		case "password":
			environment.Redis.Password = strings.TrimSpace(value)
		default:
			return unsupportedConfigKey(path)
		}
	case toolRabbitMQ:
		if environment.RabbitMQ == nil {
			environment.RabbitMQ = &RabbitMQConfig{}
		}
		switch path[2] {
		case "port":
			port, err := parseConfigPortValue(strings.Join(path, "."), value)
			if err != nil {
				return err
			}
			environment.RabbitMQ.Port = port
		case "management-port":
			port, err := parseConfigPortValue(strings.Join(path, "."), value)
			if err != nil {
				return err
			}
			environment.RabbitMQ.ManagementPort = port
		case "username":
			environment.RabbitMQ.Username = strings.TrimSpace(value)
		case "password":
			environment.RabbitMQ.Password = strings.TrimSpace(value)
		default:
			return unsupportedConfigKey(path)
		}
	case toolTraefik:
		if environment.Traefik == nil {
			environment.Traefik = &TraefikConfig{}
		}
		if path[2] != "port" {
			return unsupportedConfigKey(path)
		}
		port, err := parseConfigPortValue(strings.Join(path, "."), value)
		if err != nil {
			return err
		}
		environment.Traefik.Port = port
	default:
		return unsupportedConfigKey(path)
	}

	return nil
}

func applyEnvVarConfigValue(environment *Environment, path []string, value string) error {
	if len(path) != 2 {
		return unsupportedConfigKey(path)
	}
	name := strings.TrimSpace(path[1])
	if name == "" {
		return unsupportedConfigKey(path)
	}
	if environment.EnvVars == nil {
		environment.EnvVars = map[string]string{}
	}
	environment.EnvVars[name] = value

	return nil
}

func applyPHPExtensionConfigValue(environment *Environment, path []string, value string) error {
	if len(path) != 2 {
		return unsupportedConfigKey(path)
	}
	name := strings.TrimSpace(path[1])
	if name == "" {
		return unsupportedConfigKey(path)
	}

	// vendor/name keys are PIE-managed extensions holding a version
	// constraint; an empty value removes the entry (used by polka ext remove).
	if strings.Contains(name, "/") {
		normalized := strings.ToLower(name)
		if err := config.ValidatePIEExtensionPackage(normalized); err != nil {
			return err
		}
		version := strings.TrimSpace(value)
		if version == "" {
			delete(environment.PIEExtensions, normalized)
			return nil
		}
		if err := config.ValidatePIEExtensionVersion(normalized, version); err != nil {
			return err
		}
		if environment.PIEExtensions == nil {
			environment.PIEExtensions = map[string]string{}
		}
		environment.PIEExtensions[normalized] = version

		return nil
	}

	enabled, err := parseConfigBoolValue(strings.Join(path, "."), value)
	if err != nil {
		return err
	}
	if environment.PHPExtensions == nil {
		environment.PHPExtensions = map[string]bool{}
	}
	environment.PHPExtensions[name] = enabled

	return nil
}

func applyOPcacheConfigValue(environment *Environment, path []string, value string) error {
	if len(path) != 2 {
		return unsupportedConfigKey(path)
	}
	directive := strings.TrimSpace(path[1])
	if directive == "" {
		return unsupportedConfigKey(path)
	}
	if environment.OPcacheConfig == nil {
		environment.OPcacheConfig = map[string]string{}
	}
	environment.OPcacheConfig[directive] = strings.TrimSpace(value)

	return nil
}

func normalizeConfigValueEnvironment(environment Environment) Environment {
	environment = config.NormalizeEnvironment(environment.Name, environment)
	if environment.Database == nil {
		return environment
	}

	database := *environment.Database
	switch strings.ToLower(strings.TrimSpace(database.Engine)) {
	case "":
		switch {
		case strings.TrimSpace(environment.MySQLVersion) != "" && strings.TrimSpace(environment.MariaDBVersion) == "" && strings.TrimSpace(environment.PostgreSQLVersion) == "":
			database.Engine = toolMySQL
			database.Version = strings.TrimSpace(environment.MySQLVersion)
		case strings.TrimSpace(environment.MariaDBVersion) != "" && strings.TrimSpace(environment.MySQLVersion) == "" && strings.TrimSpace(environment.PostgreSQLVersion) == "":
			database.Engine = toolMariaDB
			database.Version = strings.TrimSpace(environment.MariaDBVersion)
		case strings.TrimSpace(environment.PostgreSQLVersion) != "" && strings.TrimSpace(environment.MySQLVersion) == "" && strings.TrimSpace(environment.MariaDBVersion) == "":
			database.Engine = toolPostgreSQL
			database.Version = strings.TrimSpace(environment.PostgreSQLVersion)
		}
	case toolMySQL:
		database.Version = strings.TrimSpace(environment.MySQLVersion)
	case toolMariaDB:
		database.Version = strings.TrimSpace(environment.MariaDBVersion)
	case toolPostgreSQL:
		database.Version = strings.TrimSpace(environment.PostgreSQLVersion)
	}
	environment.Database = config.NormalizeDatabaseConfig(&database)

	return config.NormalizeEnvironment(environment.Name, environment)
}

func parseConfigPortValue(key, value string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s requires an integer value", key)
	}
	if port != 0 && (port < 1 || port > 65535) {
		return 0, fmt.Errorf("%s must be 0 or between 1 and 65535", key)
	}

	return port, nil
}

func parseConfigBoolValue(key, value string) (bool, error) {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("%s requires a boolean value", key)
	}

	return parsed, nil
}

func unsupportedConfigKey(path []string) error {
	return fmt.Errorf("unsupported config key %q", strings.Join(path, "."))
}
