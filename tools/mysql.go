package tools

func mysqlPlugin() Plugin {
	return newManifestPlugin(MySQL, pluginHooks{})
}
