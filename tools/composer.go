package tools

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const composerDownloadBaseURL = "https://getcomposer.org/download"

var composerReleaseLinkPattern = regexp.MustCompile(`(?:https://getcomposer\.org)?/download/([0-9]+(?:\.[0-9]+){1,2}(?:-[0-9A-Za-z.-]+)?)/composer\.phar`)

func composerPlugin() Plugin {
	return newManifestPlugin(Composer, pluginHooks{
		download: func(ctx DownloadContext) error {
			return downloadComposer(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	})
}

func downloadComposer(client *http.Client, cacheDir, version string) error {
	if err := os.MkdirAll(filepath.Join(cacheDir, Composer), 0o755); err != nil {
		return fmt.Errorf("create composer cache dir: %w", err)
	}

	cacheVersionDir := filepath.Join(cacheDir, Composer, version)
	stagingDir, err := os.MkdirTemp(filepath.Join(cacheDir, Composer), version+"-tmp-")
	if err != nil {
		return fmt.Errorf("create composer staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	targetPath := filepath.Join(stagingDir, "bin", "composer.phar")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create composer target dir: %w", err)
	}

	url, checksumURL, err := resolveComposerDownloadURLs(client, version)
	if err != nil {
		return err
	}
	if err := downloadFile(client, url, targetPath); err != nil {
		return err
	}
	if err := verifyFileSHA256(client, targetPath, checksumURL); err != nil {
		return err
	}

	return finalizeCacheVersion(cacheVersionDir, stagingDir)
}

func resolveComposerDownloadURLs(client *http.Client, version string) (string, string, error) {
	resolvedVersion := strings.TrimSpace(version)
	if composerVersionNeedsResolution(resolvedVersion) {
		var err error
		resolvedVersion, err = resolveComposerReleaseVersion(client, resolvedVersion)
		if err != nil {
			return "", "", err
		}
	}

	url := fmt.Sprintf("%s/%s/composer.phar", composerDownloadBaseURL, resolvedVersion)
	return url, url + ".sha256sum", nil
}

func composerVersionNeedsResolution(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}

	return !strings.Contains(version, "-") && strings.Count(version, ".") < 2
}

func resolveComposerReleaseVersion(client *http.Client, requested string) (string, error) {
	response, err := client.Get(composerDownloadBaseURL + "/")
	if err != nil {
		return "", fmt.Errorf("download composer release index: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download composer release index: unexpected status %s", response.Status)
	}

	page, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("read composer release index: %w", err)
	}

	resolvedVersion, err := selectComposerReleaseVersion(string(page), requested)
	if err != nil {
		return "", fmt.Errorf("resolve composer version %q: %w", requested, err)
	}

	return resolvedVersion, nil
}

func selectComposerReleaseVersion(page, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("requested composer version is empty")
	}

	allowPrerelease := strings.Contains(requested, "-")
	seen := map[string]struct{}{}
	for _, match := range composerReleaseLinkPattern.FindAllStringSubmatch(page, -1) {
		version := strings.TrimSpace(match[1])
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}

		if version != requested && !strings.HasPrefix(version, requested+".") {
			continue
		}
		if !allowPrerelease && strings.Contains(version, "-") {
			continue
		}

		return version, nil
	}

	return "", fmt.Errorf("no matching release found")
}
