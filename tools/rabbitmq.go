package tools

func erlangPlugin() Plugin {
	return newManifestPlugin(Erlang, pluginHooks{})
}

// rabbitMQPlugin validates broker settings through
// manifests/scripts/rabbitmq.star.
func rabbitMQPlugin() Plugin {
	return newManifestPlugin(RabbitMQ, pluginHooks{})
}
