package tools

func mailpitPlugin() Plugin {
	return newManifestPlugin(Mailpit, pluginHooks{})
}
