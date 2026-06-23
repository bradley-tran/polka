package tools

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"polka/config"
)

var (
	phpMyAdminDownloadsURL           = "https://www.phpmyadmin.net/downloads/"
	phpMyAdminDownloadVersionPattern = regexp.MustCompile(`phpMyAdmin-([0-9]+\.[0-9]+\.[0-9]+)-all-languages\.zip`)
)

func phpMyAdminPlugin() Plugin {
	return newManifestPlugin(PHPMyAdmin, pluginHooks{
		validate: func(environment config.Environment) error {
			if err := validatePHPMyAdminConfig(environment.PHPMyAdmin); err != nil {
				return err
			}

			return nil
		},
		download: func(ctx DownloadContext) error {
			return downloadPHPMyAdmin(ctx.Client, ctx.CacheDir, ctx.Version)
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
	resolvedVersion, err := resolvePHPMyAdminReleaseVersion(client, version)
	if err != nil {
		return err
	}

	return downloadBuiltinManifestToolResolved(client, cacheDir, PHPMyAdmin, version, resolvedVersion, nil)
}

func resolvePHPMyAdminDownloadAsset(client *http.Client, requestedVersion string) (string, databaseDownloadAsset, error) {
	resolvedVersion, err := resolvePHPMyAdminReleaseVersion(client, requestedVersion)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	manifest, err := loadBuiltinManifest(PHPMyAdmin)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	asset, err := resolveManifestDownloadAsset(PHPMyAdmin, manifest.Download.Assets, requestedVersion, resolvedVersion, resolvedVersion, "", "", nil)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	return resolvedVersion, asset, nil
}

func resolvePHPMyAdminReleaseVersion(client *http.Client, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("phpmyadmin version cannot be empty")
	}
	if !versionNeedsResolution(requested) {
		return requested, nil
	}

	page, err := downloadText(client, phpMyAdminDownloadsURL, "phpmyadmin downloads page")
	if err != nil {
		return "", err
	}

	versions := []string{}
	seen := map[string]struct{}{}
	for _, match := range phpMyAdminDownloadVersionPattern.FindAllStringSubmatch(page, -1) {
		version := strings.TrimSpace(match[1])
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		versions = append(versions, version)
	}

	resolvedVersion, err := resolveMatchingVersion(versions, requested)
	if err != nil {
		return "", fmt.Errorf("resolve phpmyadmin version %q: %w", requested, err)
	}

	return resolvedVersion, nil
}
