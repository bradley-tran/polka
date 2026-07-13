package cli

import (
	"time"

	"polka/backend"
	"polka/service"
)

var (
	startRabbitMQServerFunc = service.StartRabbitMQServer
	stopRabbitMQRuntimeFunc = service.StopRabbitMQRuntime
	pingRabbitMQAddressFunc = service.PingRabbitMQAddress
	rabbitMQNowFunc         = time.Now
)

type rabbitMQRuntimeState = service.RabbitMQRuntimeState
type rabbitMQServerSpec = service.RabbitMQServerSpec
type rabbitMQStartResult = service.RabbitMQStartResult

func ensureManagedRabbitMQStarted(store backend.Store, environment backend.Environment) (rabbitMQRuntimeState, bool, error) {
	return service.EnsureManagedRabbitMQStarted(managedServiceContext(store, environment, nil), rabbitMQRuntimeHooks())
}

func stopManagedRabbitMQ(store backend.Store, environmentName string) (rabbitMQRuntimeState, bool, error) {
	return service.StopManagedRabbitMQ(managedServiceContext(store, backend.Environment{Name: environmentName}, nil), rabbitMQRuntimeHooks())
}

func rabbitMQRuntimeHooks() service.RabbitMQRuntimeHooks {
	return service.RabbitMQRuntimeHooks{StartServer: startRabbitMQServerFunc, StopServer: stopRabbitMQRuntimeFunc, PingAddress: pingRabbitMQAddressFunc, Now: rabbitMQNowFunc}
}

func loadLiveRabbitMQState(rootDir, environmentName string) (*rabbitMQRuntimeState, error) {
	return service.LoadLiveRabbitMQState(rootDir, environmentName, pingRabbitMQAddressFunc)
}

func rabbitMQURLForConfig(value *backend.RabbitMQConfig) string {
	return service.RabbitMQURLForConfig(value)
}

func rabbitMQManagementURLForConfig(value *backend.RabbitMQConfig) string {
	return service.RabbitMQManagementURLForConfig(value)
}
