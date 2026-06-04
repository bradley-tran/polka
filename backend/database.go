package backend

import (
	"fmt"
	"strings"

	"polka/config"
)

func mergeDatabaseConfig(existing, override *DatabaseConfig) *DatabaseConfig {
	return config.MergeDatabaseConfig(existing, override)
}

func normalizeDatabaseConfig(database *DatabaseConfig) *DatabaseConfig {
	return config.NormalizeDatabaseConfig(database)
}

func validateDatabaseConfig(database *DatabaseConfig) error {
	if database == nil {
		return nil
	}

	engine, err := normalizeDatabaseEngine(database.Engine)
	if err != nil {
		return err
	}
	if engine == "" || database.Version == "" {
		return fmt.Errorf("database configuration requires both engine and version")
	}
	if err := validateVersion(engine, database.Version); err != nil {
		return err
	}
	if database.Port != 0 && (database.Port < 1 || database.Port > 65535) {
		return fmt.Errorf("database port must be between 1 and 65535")
	}

	return nil
}

func setDatabaseToolVersion(environment Environment, database *DatabaseConfig) Environment {
	if database == nil || strings.TrimSpace(database.Version) == "" {
		return environment
	}

	switch strings.ToLower(strings.TrimSpace(database.Engine)) {
	case toolMySQL:
		environment.MySQLVersion = strings.TrimSpace(database.Version)
	case toolMariaDB:
		environment.MariaDBVersion = strings.TrimSpace(database.Version)
	}

	return environment
}

func normalizeDatabaseEngine(engine string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(engine))
	switch trimmed {
	case "":
		return "", nil
	case toolMySQL, toolMariaDB:
		return trimmed, nil
	default:
		return "", fmt.Errorf("unsupported database engine %q", engine)
	}
}
