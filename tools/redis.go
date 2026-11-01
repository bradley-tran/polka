package tools

func redisPlugin() Plugin {
	return newManifestPlugin(Redis, pluginHooks{})
}
