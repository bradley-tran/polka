package tools

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var (
	nginxDownloadIndexURL       = "https://nginx.org/download/"
	nginxDownloadVersionPattern = regexp.MustCompile(`nginx-([0-9]+\.[0-9]+\.[0-9]+)\.zip`)
)

func nginxPlugin() Plugin {
	return newManifestPlugin(Nginx, pluginHooks{
		download: func(ctx DownloadContext) error {
			return downloadNginx(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	})
}

func downloadNginx(client *http.Client, cacheDir, version string) error {
	resolvedVersion, err := resolveNginxReleaseVersion(client, version)
	if err != nil {
		return err
	}

	return downloadBuiltinManifestToolResolved(client, cacheDir, Nginx, version, resolvedVersion, nil)
}

func resolveNginxDownloadAsset(client *http.Client, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	resolvedVersion, err := resolveNginxReleaseVersion(client, requestedVersion)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	manifest, err := loadBuiltinManifest(Nginx)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	asset, err := resolveManifestDownloadAsset(Nginx, manifest.Download.Assets, requestedVersion, resolvedVersion, resolvedVersion, goos, goarch, nil)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	return resolvedVersion, asset, nil
}

func resolveNginxReleaseVersion(client *http.Client, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("nginx version cannot be empty")
	}
	if !versionNeedsResolution(requested) {
		return requested, nil
	}

	page, err := downloadText(client, nginxDownloadIndexURL, "nginx download index")
	if err != nil {
		return "", err
	}

	versions := []string{}
	seen := map[string]struct{}{}
	for _, match := range nginxDownloadVersionPattern.FindAllStringSubmatch(page, -1) {
		version := strings.TrimSpace(match[1])
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		versions = append(versions, version)
	}

	resolvedVersion, err := resolveMatchingVersion(versions, requested)
	if err != nil {
		return "", fmt.Errorf("resolve nginx version %q: %w", requested, err)
	}

	return resolvedVersion, nil
}
