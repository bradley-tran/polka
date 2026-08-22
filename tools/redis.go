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
		download: func(ctx DownloadContext) error {
			return downloadRedis(ctx.Client, ctx.CacheDir, ctx.Version)
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

// downloadRedis fetches the community redis-windows build. zkteco-home publishes
// no release assets (the binaries are committed to the repo), so Polka downloads
// the tag's GitHub source-archive zip. That archive has no published checksum, so
// integrity is verified against the commit the tag resolves to: GitHub's
// git-archive zips embed the source commit SHA in the zip archive comment, and
// Polka compares it to the commit SHA reported by the GitHub API for the tag.
func downloadRedis(client *http.Client, cacheDir, version string) error {
	manifest, err := loadBuiltinManifest(Redis)
	if err != nil {
		return err
	}
	resolvedVersion, tag, _, err := resolveManifestDownloadVersion(client, Redis, version, manifest.Download)
	if err != nil {
		return err
	}
	asset, err := resolveManifestDownloadAsset(Redis, manifest.Download.Assets, version, resolvedVersion, tag, runtime.GOOS, runtime.GOARCH, nil)
	if err != nil {
		return err
	}
	if asset.ChecksumAlgorithm != checksumAlgorithmEmbeddedCommitID {
		return fmt.Errorf("redis download expects %q integrity, got %q", checksumAlgorithmEmbeddedCommitID, asset.ChecksumAlgorithm)
	}
	commit, err := resolveGitHubTagCommit(client, Redis, manifest.Download.GitHub, tag)
	if err != nil {
		return err
	}

	return downloadGitHubSourceArchive(client, cacheDir, Redis, version, resolvedVersion, asset, commit)
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
