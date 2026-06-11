package tools

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const piePHARFileName = "pie.phar"

var pieGitHubDownload = manifestGitHubDownload{
	Owner: "php",
	Repo:  "pie",
}

func piePlugin() Plugin {
	return newManifestPlugin(PIE, pluginHooks{
		download: func(ctx DownloadContext) error {
			return downloadPIE(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	})
}

func downloadPIE(client *http.Client, cacheDir, version string) error {
	if err := os.MkdirAll(filepath.Join(cacheDir, PIE), 0o755); err != nil {
		return fmt.Errorf("create pie cache dir: %w", err)
	}

	resolvedVersion, asset, err := resolvePIEDownloadAsset(client, version)
	if err != nil {
		return err
	}

	stagingDir, err := os.MkdirTemp(filepath.Join(cacheDir, PIE), version+"-tmp-")
	if err != nil {
		return fmt.Errorf("create pie staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	targetPath := filepath.Join(stagingDir, piePHARFileName)
	if err := downloadFile(client, asset.BrowserDownloadURL, targetPath); err != nil {
		return err
	}
	algorithm := checksumAlgorithmNone
	checksum := ""
	if parsedAlgorithm, parsedChecksum, ok := parseGitHubAssetDigest(asset.Digest); ok {
		if err := verifyFileChecksum(parsedAlgorithm, parsedChecksum, targetPath); err != nil {
			return err
		}
		algorithm = parsedAlgorithm
		checksum = parsedChecksum
	}

	_, err = cacheFilePayload(cacheDir, PIE, version, resolvedVersion, piePHARFileName, asset.BrowserDownloadURL, "bin/"+piePHARFileName, algorithm, checksum, targetPath)
	return err
}

func resolvePIEDownloadAsset(client *http.Client, requestedVersion string) (string, githubReleaseAsset, error) {
	resolvedVersion, _, assets, err := resolveGitHubReleaseVersion(client, pieGitHubDownload, requestedVersion)
	if err != nil {
		return "", githubReleaseAsset{}, fmt.Errorf("resolve pie version %q: %w", requestedVersion, err)
	}

	for _, asset := range assets {
		if strings.EqualFold(strings.TrimSpace(asset.Name), piePHARFileName) {
			if strings.TrimSpace(asset.BrowserDownloadURL) == "" {
				return "", githubReleaseAsset{}, fmt.Errorf("pie release %s asset %s does not define a download URL", resolvedVersion, piePHARFileName)
			}
			return resolvedVersion, asset, nil
		}
	}

	return "", githubReleaseAsset{}, fmt.Errorf("pie release %s does not include %s", resolvedVersion, piePHARFileName)
}
