package tools

import "net/http"

func nginxPlugin() Plugin {
	return newManifestPlugin(Nginx, pluginHooks{})
}

func downloadNginx(client *http.Client, cacheDir, version string) error {
	return downloadBuiltinManifestTool(client, cacheDir, Nginx, version)
}

func resolveNginxDownloadAsset(requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	return resolveBuiltinManifestDownloadAsset(Nginx, requestedVersion, goos, goarch)
}
