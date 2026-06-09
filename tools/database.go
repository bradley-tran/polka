package tools

import (
	"fmt"
	"net/http"
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
	case MySQL, MariaDB:
		return trimmed, nil
	default:
		return "", fmt.Errorf("unsupported database engine %q", engine)
	}
}

func downloadDatabaseTool(client *http.Client, cacheDir, tool, version string) error {
	return downloadBuiltinManifestTool(client, cacheDir, tool, version)
}

func resolveDatabaseDownloadAsset(client *http.Client, tool, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	var resolvedVersion string
	var err error
	switch tool {
	case MySQL:
		resolvedVersion, err = resolveMySQLReleaseVersion(client, requestedVersion)
	case MariaDB:
		resolvedVersion, err = resolveMariaDBReleaseVersion(client, requestedVersion)
	default:
		return "", databaseDownloadAsset{}, fmt.Errorf("unsupported database tool %q", tool)
	}
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	manifest, err := loadBuiltinManifest(tool)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	asset, err := resolveManifestDownloadAsset(tool, manifest.Download.Assets, requestedVersion, resolvedVersion, resolvedVersion, goos, goarch, map[string]string{
		"major_minor": versionMajorMinor(resolvedVersion),
	})
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	return resolvedVersion, asset, nil
}
