package tools

func piePlugin() Plugin {
	return newManifestPlugin(PIE, pluginHooks{})
}
