package tools

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"polka/config"
)

func redisPlugin() Plugin {
	return newManifestPlugin(Redis, pluginHooks{
		validate: func(environment config.Environment) error {
			return validateRedisConfig(environment.Redis)
		},
		postInstall: chmodInstalledRedis,
	})
}

func validateRedisConfig(redis *config.RedisConfig) error {
	if redis == nil {
		return nil
	}
	if strings.TrimSpace(redis.Version) == "" {
		return fmt.Errorf("redis configuration requires version")
	}
	if err := validateVersion(Redis, redis.Version); err != nil {
		return err
	}
	if redis.Port != 0 && !validPort(redis.Port) {
		return fmt.Errorf("redis port must be between 1 and 65535")
	}

	return nil
}

func downloadRedis(client *http.Client, cacheDir, version string) error {
	return downloadBuiltinManifestTool(client, cacheDir, Redis, version)
}

func resolveRedisDownloadAsset(client *http.Client, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	manifest, err := loadBuiltinManifest(Redis)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	resolvedVersion, tag, githubAssets, err := resolveManifestDownloadVersion(client, Redis, requestedVersion, manifest.Download)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	asset, err := resolveManifestDownloadAsset(Redis, manifest.Download.Assets, requestedVersion, resolvedVersion, tag, goos, goarch, nil)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	applyGitHubAssetDigest(&asset, githubAssets)

	return resolvedVersion, asset, nil
}

// chmodInstalledRedis makes the extracted redis-server and redis-cli binaries
// executable on non-Windows platforms. Redis ships both binaries in the same
// install directory, so we mark each of them.
func chmodInstalledRedis(ctx InstallContext) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	target := ctx.Result.TargetPath
	if strings.TrimSpace(target) == "" {
		return nil
	}
	installDir := filepath.Dir(target)
	for _, binary := range []string{filepath.Base(target), RedisCLI} {
		path := filepath.Join(installDir, binary)
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat redis binary %s: %w", path, err)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			return fmt.Errorf("make redis binary %s executable: %w", path, err)
		}
	}

	return nil
}
