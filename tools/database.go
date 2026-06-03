package tools

import (
	"fmt"
	"net/http"
	"strings"

	"polka/config"
)

func databasePlugin(tool string) Plugin {
	return newManifestPlugin(tool, pluginHooks{})
}

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
	case MySQL, MariaDB:
		return trimmed, nil
	default:
		return "", fmt.Errorf("unsupported database engine %q", engine)
	}
}

func downloadDatabaseTool(client *http.Client, cacheDir, tool, version string) error {
	return downloadBuiltinManifestTool(client, cacheDir, tool, version)
}

func resolveDatabaseDownloadAsset(tool, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	return resolveBuiltinManifestDownloadAsset(tool, requestedVersion, goos, goarch)
}
