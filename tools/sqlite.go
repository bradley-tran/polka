package tools

import "net/http"

func sqlitePlugin() Plugin {
	return newManifestPlugin(SQLite, pluginHooks{})
}

func downloadSQLite(client *http.Client, cacheDir, version string) error {
	return downloadBuiltinManifestTool(client, cacheDir, SQLite, version)
}

func resolveSQLiteDownloadAsset(requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	return resolveBuiltinManifestDownloadAsset(SQLite, requestedVersion, goos, goarch)
}
