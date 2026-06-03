package tools

import "net/http"

func nodeJSPlugin() Plugin {
	return newManifestPlugin(NodeJS, pluginHooks{})
}

func downloadNodeJS(client *http.Client, cacheDir, version string) error {
	return downloadBuiltinManifestTool(client, cacheDir, NodeJS, version)
}

func resolveNodeJSDownloadAsset(requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	return resolveBuiltinManifestDownloadAsset(NodeJS, requestedVersion, goos, goarch)
}
