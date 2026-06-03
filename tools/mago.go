package tools

func magoPlugin() Plugin {
	return newManifestPlugin(Mago, pluginHooks{})
}

func resolveMagoDownloadAsset(requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	return resolveBuiltinManifestDownloadAsset(Mago, requestedVersion, goos, goarch)
}
