package tools

import (
	"fmt"
	"net/http"
	"strings"

	"polka/config"
)

func phpMyAdminPlugin() Plugin {
	return newManifestPlugin(PHPMyAdmin, pluginHooks{
		validate: func(environment config.Environment) error {
			return validatePHPMyAdminConfig(environment.PHPMyAdmin)
		},
		postInstall: configureInstalledPHPMyAdmin,
	})
}

func validatePHPMyAdminConfig(phpMyAdmin *config.PHPMyAdminConfig) error {
	if phpMyAdmin == nil {
		return nil
	}
	if strings.TrimSpace(phpMyAdmin.Version) == "" {
		return fmt.Errorf("phpmyadmin configuration requires version")
	}
	if err := validateVersion(PHPMyAdmin, phpMyAdmin.Version); err != nil {
		return err
	}
	if phpMyAdmin.Port != 0 && !validPort(phpMyAdmin.Port) {
		return fmt.Errorf("phpmyadmin port must be between 1 and 65535")
	}

	return nil
}

func downloadPHPMyAdmin(client *http.Client, cacheDir, version string) error {
	return downloadBuiltinManifestTool(client, cacheDir, PHPMyAdmin, version)
}

func resolvePHPMyAdminDownloadAsset(requestedVersion string) (string, databaseDownloadAsset, error) {
	return resolveBuiltinManifestDownloadAsset(PHPMyAdmin, requestedVersion, "", "")
}
