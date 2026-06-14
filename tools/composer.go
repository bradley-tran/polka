package tools

func composerPlugin() Plugin {
	return newManifestPlugin(Composer, pluginHooks{})
}
