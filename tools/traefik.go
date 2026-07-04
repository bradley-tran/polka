package tools

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"

	"polka/config"
)

// traefikPlugin installs the Traefik reverse proxy and dispatches its binary.
func traefikPlugin() Plugin {
	return newManifestPlugin(Traefik, pluginHooks{
		validate: func(environment config.Environment) error {
			return validateTraefikConfig(environment.Traefik)
		},
		postInstall: chmodInstalledTraefik,
	})
}

// validateTraefikConfig checks the Traefik version and optional port.
func validateTraefikConfig(traefik *config.TraefikConfig) error {
	if traefik == nil {
		return nil
	}
	if strings.TrimSpace(traefik.Version) == "" {
		return fmt.Errorf("traefik configuration requires version")
	}
	if err := validateVersion(Traefik, traefik.Version); err != nil {
		return err
	}
	if traefik.Port != 0 && !validPort(traefik.Port) {
		return fmt.Errorf("traefik port must be between 1 and 65535")
	}

	return nil
}

// downloadTraefik resolves and downloads the Traefik archive for a version.
func downloadTraefik(client *http.Client, cacheDir, version string) error {
	return downloadBuiltinManifestTool(client, cacheDir, Traefik, version)
}

// resolveTraefikDownloadAsset resolves the platform archive for a Traefik version.
func resolveTraefikDownloadAsset(client *http.Client, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	manifest, err := loadBuiltinManifest(Traefik)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	resolvedVersion, tag, githubAssets, err := resolveManifestDownloadVersion(client, Traefik, requestedVersion, manifest.Download)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	asset, err := resolveManifestDownloadAsset(Traefik, manifest.Download.Assets, requestedVersion, resolvedVersion, tag, goos, goarch, nil)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	applyGitHubAssetDigest(&asset, githubAssets)

	return resolvedVersion, asset, nil
}

// chmodInstalledTraefik marks the extracted Traefik binary executable on Unix.
func chmodInstalledTraefik(ctx InstallContext) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	target := ctx.Result.TargetPath
	if strings.TrimSpace(target) == "" {
		return nil
	}
	if err := os.Chmod(target, 0o755); err != nil {
		return fmt.Errorf("make traefik executable: %w", err)
	}

	return nil
}
