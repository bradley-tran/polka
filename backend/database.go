package backend

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

type DatabaseConfig struct {
	Engine  string `yaml:"engine,omitempty"`
	Version string `yaml:"version,omitempty"`
	Port    int    `yaml:"port,omitempty"`
}

func mergeDatabaseConfig(existing, override *DatabaseConfig) *DatabaseConfig {
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

	return normalizeDatabaseConfig(merged)
}

func normalizeDatabaseConfig(database *DatabaseConfig) *DatabaseConfig {
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

func databaseToolInstallCandidates(tool, installDir string) []string {
	switch tool {
	case toolMySQL:
		if runtime.GOOS == "windows" {
			return []string{
				filepath.Join(installDir, "bin", "mysql.cmd"),
				filepath.Join(installDir, "bin", "mysql.bat"),
				filepath.Join(installDir, "bin", "mysql.exe"),
				filepath.Join(installDir, "mysql.cmd"),
				filepath.Join(installDir, "mysql.bat"),
				filepath.Join(installDir, "mysql.exe"),
			}
		}

		return []string{
			filepath.Join(installDir, "bin", "mysql"),
			filepath.Join(installDir, "mysql"),
		}
	case toolMariaDB:
		if runtime.GOOS == "windows" {
			return []string{
				filepath.Join(installDir, "bin", "mariadb.cmd"),
				filepath.Join(installDir, "bin", "mariadb.bat"),
				filepath.Join(installDir, "bin", "mariadb.exe"),
				filepath.Join(installDir, "bin", "mysql.cmd"),
				filepath.Join(installDir, "bin", "mysql.bat"),
				filepath.Join(installDir, "bin", "mysql.exe"),
				filepath.Join(installDir, "mariadb.cmd"),
				filepath.Join(installDir, "mariadb.bat"),
				filepath.Join(installDir, "mariadb.exe"),
				filepath.Join(installDir, "mysql.cmd"),
				filepath.Join(installDir, "mysql.bat"),
				filepath.Join(installDir, "mysql.exe"),
			}
		}

		return []string{
			filepath.Join(installDir, "bin", "mariadb"),
			filepath.Join(installDir, "bin", "mysql"),
			filepath.Join(installDir, "mariadb"),
			filepath.Join(installDir, "mysql"),
		}
	default:
		return nil
	}
}

func (e Environment) databaseToolVersion(tool string) string {
	if e.Database != nil && e.Database.Engine == tool {
		return e.Database.Version
	}

	return ""
}
