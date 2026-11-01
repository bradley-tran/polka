package tools

// sqlitePlugin resolves releases through manifests/scripts/sqlite.star.
func sqlitePlugin() Plugin {
	return newManifestPlugin(SQLite, pluginHooks{})
}
