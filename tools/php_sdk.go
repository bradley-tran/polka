package tools

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
)

func phpSDKPlugin() Plugin {
	return newManifestPlugin(PHPSDK, pluginHooks{
		download: func(ctx DownloadContext) error {
			return downloadPHPSDK(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	})
}

// downloadPHPSDK authenticates the tagged GitHub source archive against the
// tag's resolved commit before caching the binary-tools repository.
func downloadPHPSDK(client *http.Client, cacheDir, version string) error {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("php-sdk is only supported on windows/amd64")
	}
	manifest, err := loadBuiltinManifest(PHPSDK)
	if err != nil {
		return err
	}
	resolvedVersion := strings.TrimPrefix(strings.TrimSpace(version), strings.TrimSpace(manifest.Download.GitHub.TagPrefix))
	if resolvedVersion == "" {
		return fmt.Errorf("php-sdk version cannot be empty")
	}
	if err := validateVersion(PHPSDK, resolvedVersion); err != nil {
		return err
	}
	// php-sdk-binary-tools publishes tags without GitHub Release objects, so
	// compose the exact tag directly instead of querying the releases endpoint.
	tag := strings.TrimSpace(manifest.Download.GitHub.TagPrefix) + resolvedVersion
	asset, err := resolveManifestDownloadAsset(PHPSDK, manifest.Download.Assets, version, resolvedVersion, tag, runtime.GOOS, runtime.GOARCH, nil)
	if err != nil {
		return err
	}
	if asset.ChecksumAlgorithm != checksumAlgorithmEmbeddedCommitID {
		return fmt.Errorf("php-sdk download expects %q integrity, got %q", checksumAlgorithmEmbeddedCommitID, asset.ChecksumAlgorithm)
	}
	commit, err := resolveGitHubTagCommit(client, PHPSDK, manifest.Download.GitHub, tag)
	if err != nil {
		return err
	}

	return downloadGitHubSourceArchive(client, cacheDir, PHPSDK, version, resolvedVersion, asset, commit)
}
