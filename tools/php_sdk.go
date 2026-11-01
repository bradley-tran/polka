package tools

func phpSDKPlugin() Plugin {
	return newManifestPlugin(PHPSDK, pluginHooks{})
}
