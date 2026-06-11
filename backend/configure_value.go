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
	case "opcache-config":
		return applyOPcacheConfigValue(environment, path, value)
	default:
		return unsupportedConfigKey(path)
	}
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
	case toolComposer:
		environment.ComposerVersion = version
	case toolPIE:
		environment.PIEVersion = version
	case toolNodeJS:
		environment.NodeJSVersion = version
	case toolMago:
		environment.MagoVersion = version
	case toolNginx:
		environment.NginxVersion = version
	case toolMySQL:
		environment.MySQLVersion = version
	case toolMariaDB:
		environment.MariaDBVersion = version
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
		case strings.TrimSpace(environment.MySQLVersion) != "" && strings.TrimSpace(environment.MariaDBVersion) == "":
			database.Engine = toolMySQL
			database.Version = strings.TrimSpace(environment.MySQLVersion)
		case strings.TrimSpace(environment.MariaDBVersion) != "" && strings.TrimSpace(environment.MySQLVersion) == "":
			database.Engine = toolMariaDB
			database.Version = strings.TrimSpace(environment.MariaDBVersion)
		}
	case toolMySQL:
		database.Version = strings.TrimSpace(environment.MySQLVersion)
	case toolMariaDB:
		database.Version = strings.TrimSpace(environment.MariaDBVersion)
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
