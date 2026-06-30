package cli

import (
	"time"

	"polka/backend"
	"polka/service"
)

var (
	startMeilisearchServerFunc = service.StartMeilisearchServer
	stopMeilisearchRuntimeFunc = service.StopMeilisearchRuntime
	pingMeilisearchAddressFunc = service.PingMeilisearchAddress
	meilisearchNowFunc         = time.Now
)

type meilisearchRuntimeState = service.MeilisearchRuntimeState
type meilisearchServerSpec = service.MeilisearchServerSpec
type meilisearchStartResult = service.MeilisearchStartResult

func ensureManagedMeilisearchStarted(store backend.Store, environment backend.Environment) (meilisearchRuntimeState, bool, error) {
	return service.EnsureManagedMeilisearchStarted(managedServiceContext(store, environment, nil), meilisearchRuntimeHooks())
}

func stopManagedMeilisearch(store backend.Store, environmentName string) (meilisearchRuntimeState, bool, error) {
	return service.StopManagedMeilisearch(managedServiceContext(store, backend.Environment{Name: environmentName}, nil), meilisearchRuntimeHooks())
}

func meilisearchRuntimeHooks() service.MeilisearchRuntimeHooks {
	return service.MeilisearchRuntimeHooks{
		StartServer: startMeilisearchServerFunc,
		StopServer:  stopMeilisearchRuntimeFunc,
		PingAddress: pingMeilisearchAddressFunc,
		Now:         meilisearchNowFunc,
	}
}

func meilisearchServerArgs(spec meilisearchServerSpec) []string {
	return service.MeilisearchServerArgs(spec)
}

func meilisearchStatePath(rootDir, environmentName string) string {
	return service.MeilisearchStatePath(rootDir, environmentName)
}

func loadMeilisearchState(path string) (*meilisearchRuntimeState, error) {
	return service.LoadMeilisearchState(path)
}

func writeMeilisearchState(path string, state meilisearchRuntimeState) error {
	return service.WriteMeilisearchState(path, state)
}

func loadLiveMeilisearchState(rootDir, environmentName string) (*meilisearchRuntimeState, error) {
	return service.LoadLiveMeilisearchState(rootDir, environmentName, pingMeilisearchAddressFunc)
}

func meilisearchURL(state meilisearchRuntimeState) string {
	return service.MeilisearchURL(state)
}

func meilisearchURLForConfig(meilisearch *backend.MeilisearchConfig) string {
	return service.MeilisearchURLForConfig(meilisearch)
}
