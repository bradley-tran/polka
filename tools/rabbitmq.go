package tools

import (
	"fmt"
	"strings"

	"polka/config"
)

func erlangPlugin() Plugin {
	return newManifestPlugin(Erlang, pluginHooks{})
}

func rabbitMQPlugin() Plugin {
	return newManifestPlugin(RabbitMQ, pluginHooks{
		validate: func(environment config.Environment) error {
			return validateRabbitMQConfig(environment.RabbitMQ())
		},
	})
}

// validateRabbitMQConfig enforces broker-specific cross-field rules on top of
// the manifest's version constraint and port-range validation.
func validateRabbitMQConfig(rabbitMQ *config.RabbitMQConfig) error {
	if rabbitMQ == nil {
		return nil
	}

	port := rabbitMQ.Port
	if port == 0 {
		port = 5672
	}
	managementPort := rabbitMQ.ManagementPort
	if managementPort == 0 {
		managementPort = 15672
	}
	if port == managementPort {
		return fmt.Errorf("rabbitmq port and management port must be different")
	}
	if strings.ContainsAny(rabbitMQ.Username, "\r\n") || strings.ContainsAny(rabbitMQ.Password, "\r\n") {
		return fmt.Errorf("rabbitmq credentials must be single-line values")
	}

	return nil
}
