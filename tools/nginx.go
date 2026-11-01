package tools

func nginxPlugin() Plugin {
	return newManifestPlugin(Nginx, pluginHooks{})
}
