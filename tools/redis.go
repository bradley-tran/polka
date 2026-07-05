package tools

import (
	"archive/zip"
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
	commit, err := resolveGitHubTagCommit(client, manifest.Download.GitHub, tag)
	if err != nil {
		return err
	}

	return downloadRedisArchive(client, cacheDir, version, resolvedVersion, asset, commit)
}

// resolveGitHubTagCommit returns the commit SHA that a tag resolves to. The
// commits endpoint dereferences both lightweight and annotated tags to their
// underlying commit, which is the commit GitHub builds the source archive from.
func resolveGitHubTagCommit(client *http.Client, github manifestGitHubDownload, tag string) (string, error) {
	owner := strings.TrimSpace(github.Owner)
	repo := strings.TrimSpace(github.Repo)
	if owner == "" || repo == "" {
		return "", fmt.Errorf("redis download resolver requires github owner and repo")
	}
	trimmedTag := strings.TrimSpace(tag)
	if trimmedTag == "" {
		return "", fmt.Errorf("redis download resolver requires a resolved tag")
	}

	url := fmt.Sprintf("%s/repos/%s/%s/commits/%s", strings.TrimRight(githubAPIBaseURL, "/"), owner, repo, trimmedTag)
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := downloadJSON(client, url, "github tag commit", &commit); err != nil {
		return "", err
	}
	sha := strings.TrimSpace(commit.SHA)
	if sha == "" {
		return "", fmt.Errorf("github tag %q resolved to an empty commit sha", trimmedTag)
	}

	return sha, nil
}

// downloadRedisArchive downloads the source-archive zip, verifies its embedded
// commit against the expected tag commit, then caches the payload for install.
func downloadRedisArchive(client *http.Client, cacheDir, requestedVersion, resolvedVersion string, asset downloadAsset, commit string) error {
	if err := os.MkdirAll(filepath.Join(cacheDir, Redis), 0o755); err != nil {
		return fmt.Errorf("create %s cache dir: %w", Redis, err)
	}
	stagingDir, err := os.MkdirTemp(filepath.Join(cacheDir, Redis), requestedVersion+"-tmp-")
	if err != nil {
		return fmt.Errorf("create %s staging dir: %w", Redis, err)
	}
	defer os.RemoveAll(stagingDir)

	archivePath := filepath.Join(stagingDir, asset.FileName)
	if err := downloadFile(client, asset.URL, archivePath); err != nil {
		return err
	}
	if err := verifyRedisArchiveCommit(archivePath, commit); err != nil {
		return err
	}

	_, err = cacheArchivePayload(cacheDir, Redis, requestedVersion, resolvedVersion, asset, archivePath)
	return err
}

// verifyRedisArchiveCommit checks that the zip archive comment (the source commit
// SHA that GitHub's git-archive embeds) matches the expected tag commit.
func verifyRedisArchiveCommit(archivePath, commit string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open redis archive %s: %w", archivePath, err)
	}
	defer reader.Close()

	comment := strings.TrimSpace(reader.Comment)
	expected := strings.TrimSpace(commit)
	if comment == "" {
		return fmt.Errorf("redis archive %s has no embedded commit to verify against tag commit %s", archivePath, expected)
	}
	if !strings.EqualFold(comment, expected) {
		return fmt.Errorf("redis archive commit %s does not match expected tag commit %s", comment, expected)
	}

	return nil
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
