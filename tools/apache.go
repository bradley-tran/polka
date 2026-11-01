package tools

// apachePlugin resolves Apache Lounge builds through
// manifests/scripts/apache.star.
func apachePlugin() Plugin {
	return newManifestPlugin(Apache, pluginHooks{})
}
