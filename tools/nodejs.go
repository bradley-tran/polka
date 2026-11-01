package tools

func nodeJSPlugin() Plugin {
	return newManifestPlugin(NodeJS, pluginHooks{})
}
