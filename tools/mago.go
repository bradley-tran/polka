package tools

func magoPlugin() Plugin {
	return newManifestPlugin(Mago, pluginHooks{})
}
