package tools

func meilisearchPlugin() Plugin {
	return newManifestPlugin(Meilisearch, pluginHooks{})
}
