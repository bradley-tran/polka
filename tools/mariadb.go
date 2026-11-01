package tools

func mariaDBPlugin() Plugin {
	return newManifestPlugin(MariaDB, pluginHooks{})
}
