package tools

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func phpDevelPlugin() Plugin {
	return newManifestPlugin(PHPDevel, pluginHooks{
		download: func(ctx DownloadContext) error {
			return downloadPHPDevel(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	})
}

// downloadPHPDevel fetches the devel pack from the exact Windows PHP release
// variant selected for the managed runtime.
func downloadPHPDevel(client *http.Client, cacheDir, version string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("automatic php-devel download is only implemented on Windows")
	}
	runtimeVersion, threadSafe := parsePHPDevelCacheVersion(version)
	if err := os.MkdirAll(filepath.Join(cacheDir, PHPDevel), 0o755); err != nil {
		return fmt.Errorf("create php-devel cache dir: %w", err)
	}

	index, err := fetchPHPWindowsReleaseIndex(client)
	if err != nil {
		return err
	}
	release, ok := index[phpSeries(runtimeVersion)]
	if !ok {
		return fmt.Errorf("php version %q is not available in the Windows release index", runtimeVersion)
	}
	variant, err := selectPHPWindowsVariant(release, threadSafe)
	if err != nil {
		return err
	}
	asset := variant.DevelPack
	if strings.TrimSpace(asset.Path) == "" || strings.TrimSpace(asset.SHA256) == "" {
		return fmt.Errorf("Windows PHP %s release does not publish a matching devel pack", release.Version)
	}

	stagingDir, err := os.MkdirTemp(filepath.Join(cacheDir, PHPDevel), version+"-tmp-")
	if err != nil {
		return fmt.Errorf("create php-devel staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	archivePath := filepath.Join(stagingDir, filepath.Base(asset.Path))
	archiveURL := fmt.Sprintf("%s/%s", phpWindowsBaseURL, asset.Path)
	if err := downloadFile(client, archiveURL, archivePath); err != nil {
		return err
	}
	if err := verifyChecksum(asset.SHA256, archivePath); err != nil {
		return err
	}

	_, err = cacheArchivePayload(cacheDir, PHPDevel, version, release.Version, downloadAsset{
		FileName:          filepath.Base(asset.Path),
		URL:               archiveURL,
		Checksum:          asset.SHA256,
		ChecksumAlgorithm: checksumAlgorithmSHA256,
		ArchiveFormat:     archiveFormatZip,
	}, archivePath)
	return err
}

// parsePHPDevelCacheVersion separates the backend's cache-only flavor suffix
// from the user-visible PHP version.
func parsePHPDevelCacheVersion(version string) (string, bool) {
	trimmed := strings.TrimSpace(version)
	if strings.HasSuffix(trimmed, "-zts") {
		return strings.TrimSuffix(trimmed, "-zts"), true
	}
	if strings.HasSuffix(trimmed, "-nts") {
		return strings.TrimSuffix(trimmed, "-nts"), false
	}

	return trimmed, false
}
