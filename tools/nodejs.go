package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

var nodeJSReleaseIndexURL = "https://nodejs.org/download/release/index.json"

type nodeJSRelease struct {
	Version string `json:"version"`
}

func nodeJSPlugin() Plugin {
	return newManifestPlugin(NodeJS, pluginHooks{
		download: func(ctx DownloadContext) error {
			return downloadNodeJS(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	})
}

func downloadNodeJS(client *http.Client, cacheDir, version string) error {
	resolvedVersion, err := resolveNodeJSReleaseVersion(client, version)
	if err != nil {
		return err
	}

	return downloadBuiltinManifestToolResolvedWithTag(client, cacheDir, NodeJS, version, resolvedVersion, "v"+resolvedVersion, nil)
}

func resolveNodeJSDownloadAsset(client *http.Client, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	resolvedVersion, err := resolveNodeJSReleaseVersion(client, requestedVersion)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	manifest, err := loadBuiltinManifest(NodeJS)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	asset, err := resolveManifestDownloadAsset(NodeJS, manifest.Download.Assets, requestedVersion, resolvedVersion, "v"+resolvedVersion, goos, goarch, nil)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	return resolvedVersion, asset, nil
}

func resolveNodeJSReleaseVersion(client *http.Client, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("nodejs version cannot be empty")
	}
	if !versionNeedsResolution(requested) {
		return strings.TrimPrefix(requested, "v"), nil
	}

	text, err := downloadText(client, nodeJSReleaseIndexURL, "nodejs release index")
	if err != nil {
		return "", err
	}

	var releases []nodeJSRelease
	if err := json.Unmarshal([]byte(text), &releases); err != nil {
		return "", fmt.Errorf("decode nodejs release index: %w", err)
	}

	versions := make([]string, 0, len(releases))
	for _, release := range releases {
		version := strings.TrimPrefix(strings.TrimSpace(release.Version), "v")
		if version != "" {
			versions = append(versions, version)
		}
	}

	resolvedVersion, err := resolveMatchingVersion(versions, requested)
	if err != nil {
		return "", fmt.Errorf("resolve nodejs version %q: %w", requested, err)
	}

	return resolvedVersion, nil
}
