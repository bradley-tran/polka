package tools

import "net/http"

func magoPlugin() Plugin {
	return newManifestPlugin(Mago, pluginHooks{})
}

func resolveMagoDownloadAsset(client *http.Client, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	manifest, err := loadBuiltinManifest(Mago)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	resolvedVersion, tag, githubAssets, err := resolveManifestDownloadVersion(client, Mago, requestedVersion, manifest.Download)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	asset, err := resolveManifestDownloadAsset(Mago, manifest.Download.Assets, requestedVersion, resolvedVersion, tag, goos, goarch, nil)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	applyGitHubAssetDigest(&asset, githubAssets)

	return resolvedVersion, asset, nil
}
