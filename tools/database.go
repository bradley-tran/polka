package tools

import (
	"fmt"
	"strings"

	"polka/config"
)

func validateDatabaseConfig(database *config.DatabaseConfig) error {
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
	if database.Port != 0 && !validPort(database.Port) {
		return fmt.Errorf("database port must be between 1 and 65535")
	}

	return nil
}

func normalizeDatabaseEngine(engine string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(engine))
	switch trimmed {
	case "":
		return "", nil
	case MySQL, MariaDB, PostgreSQL:
		return trimmed, nil
	default:
		return "", fmt.Errorf("unsupported database engine %q", engine)
	}
}
