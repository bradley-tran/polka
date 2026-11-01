package tools

// roadRunnerPlugin installs the RoadRunner PHP application server and dispatches
// its binary. RoadRunner is a managed installable tool only: users supply their
// own .rr.yaml and PSR worker script, so it is not selectable as a server.type.
func roadRunnerPlugin() Plugin {
	return newManifestPlugin(RoadRunner, pluginHooks{})
}
