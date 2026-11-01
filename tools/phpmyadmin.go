package tools

func phpMyAdminPlugin() Plugin {
	return newManifestPlugin(PHPMyAdmin, pluginHooks{
		postInstall: configureInstalledPHPMyAdmin,
	})
}
