package cli

import (
	"time"

	"polka/backend"
	"polka/service"
)

var (
	startRedisServerFunc = service.StartRedisServer
	stopRedisRuntimeFunc = service.StopRedisRuntime
	pingRedisAddressFunc = service.PingRedisAddress
	redisNowFunc         = time.Now
)

type redisRuntimeState = service.RedisRuntimeState
type redisServerSpec = service.RedisServerSpec
type redisStartResult = service.RedisStartResult

func ensureManagedRedisStarted(store backend.Store, environment backend.Environment) (redisRuntimeState, bool, error) {
	return service.EnsureManagedRedisStarted(managedServiceContext(store, environment, nil), redisRuntimeHooks())
}

func stopManagedRedis(store backend.Store, environmentName string) (redisRuntimeState, bool, error) {
	return service.StopManagedRedis(managedServiceContext(store, backend.Environment{Name: environmentName}, nil), redisRuntimeHooks())
}

func redisRuntimeHooks() service.RedisRuntimeHooks {
	return service.RedisRuntimeHooks{
		StartServer: startRedisServerFunc,
		StopServer:  stopRedisRuntimeFunc,
		PingAddress: pingRedisAddressFunc,
		Now:         redisNowFunc,
	}
}

func redisServerArgs(spec redisServerSpec) []string {
	return service.RedisServerArgs(spec)
}

func redisStatePath(rootDir, environmentName string) string {
	return service.RedisStatePath(rootDir, environmentName)
}

func loadRedisState(path string) (*redisRuntimeState, error) {
	return service.LoadRedisState(path)
}

func writeRedisState(path string, state redisRuntimeState) error {
	return service.WriteRedisState(path, state)
}

func loadLiveRedisState(rootDir, environmentName string) (*redisRuntimeState, error) {
	return service.LoadLiveRedisState(rootDir, environmentName, pingRedisAddressFunc)
}

func redisURL(state redisRuntimeState) string {
	return service.RedisURL(state)
}

func redisURLForConfig(redis *backend.RedisConfig) string {
	return service.RedisURLForConfig(redis)
}
