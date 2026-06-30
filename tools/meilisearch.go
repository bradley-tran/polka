package tools

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"

	"polka/config"
)

func meilisearchPlugin() Plugin {
	return newManifestPlugin(Meilisearch, pluginHooks{
		validate: func(environment config.Environment) error {
			return validateMeilisearchConfig(environment.Meilisearch)
		},
		postInstall: chmodInstalledMeilisearch,
	})
}

func validateMeilisearchConfig(meilisearch *config.MeilisearchConfig) error {
	if meilisearch == nil {
		return nil
	}
	if strings.TrimSpace(meilisearch.Version) == "" {
		return fmt.Errorf("meilisearch configuration requires version")
	}
	if err := validateVersion(Meilisearch, meilisearch.Version); err != nil {
		return err
	}
	if meilisearch.Port != 0 && !validPort(meilisearch.Port) {
		return fmt.Errorf("meilisearch port must be between 1 and 65535")
	}

	return nil
}

func downloadMeilisearch(client *http.Client, cacheDir, version string) error {
	return downloadBuiltinManifestTool(client, cacheDir, Meilisearch, version)
}

func resolveMeilisearchDownloadAsset(client *http.Client, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	manifest, err := loadBuiltinManifest(Meilisearch)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	resolvedVersion, tag, githubAssets, err := resolveManifestDownloadVersion(client, Meilisearch, requestedVersion, manifest.Download)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	asset, err := resolveManifestDownloadAsset(Meilisearch, manifest.Download.Assets, requestedVersion, resolvedVersion, tag, goos, goarch, nil)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	applyGitHubAssetDigest(&asset, githubAssets)

	return resolvedVersion, asset, nil
}

func chmodInstalledMeilisearch(ctx InstallContext) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	target := ctx.Result.TargetPath
	if strings.TrimSpace(target) == "" {
		return nil
	}
	if err := os.Chmod(target, 0o755); err != nil {
		return fmt.Errorf("make meilisearch executable: %w", err)
	}

	return nil
}
