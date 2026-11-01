package tools

// traefikPlugin installs the Traefik reverse proxy and dispatches its binary.
func traefikPlugin() Plugin {
	return newManifestPlugin(Traefik, pluginHooks{})
}
