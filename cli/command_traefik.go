package cli

import (
	"time"

	"polka/backend"
	"polka/service"
)

var (
	startTraefikServerFunc = service.StartTraefikServer
	stopTraefikRuntimeFunc = service.StopTraefikRuntime
	pingTraefikAddressFunc = service.PingTraefikAddress
	traefikNowFunc         = time.Now
)

type traefikRuntimeState = service.TraefikRuntimeState
type traefikServerSpec = service.TraefikServerSpec
type traefikStartResult = service.TraefikStartResult

func ensureManagedTraefikStarted(store backend.Store, environment backend.Environment) (traefikRuntimeState, bool, error) {
	return service.EnsureManagedTraefikStarted(managedServiceContext(store, environment, nil), traefikRuntimeHooks())
}

func stopManagedTraefik(store backend.Store, environmentName string) (traefikRuntimeState, bool, error) {
	return service.StopManagedTraefik(managedServiceContext(store, backend.Environment{Name: environmentName}, nil), traefikRuntimeHooks())
}

func traefikRuntimeHooks() service.TraefikRuntimeHooks {
	return service.TraefikRuntimeHooks{
		StartServer: startTraefikServerFunc,
		StopServer:  stopTraefikRuntimeFunc,
		PingAddress: pingTraefikAddressFunc,
		Now:         traefikNowFunc,
	}
}

func traefikServerArgs(spec traefikServerSpec) []string {
	return service.TraefikServerArgs(spec)
}

func traefikStatePath(rootDir, environmentName string) string {
	return service.TraefikStatePath(rootDir, environmentName)
}

func loadTraefikState(path string) (*traefikRuntimeState, error) {
	return service.LoadTraefikState(path)
}

func writeTraefikState(path string, state traefikRuntimeState) error {
	return service.WriteTraefikState(path, state)
}

func loadLiveTraefikState(rootDir, environmentName string) (*traefikRuntimeState, error) {
	return service.LoadLiveTraefikState(rootDir, environmentName, pingTraefikAddressFunc)
}

func traefikURL(state traefikRuntimeState) string {
	return service.TraefikURL(state)
}

func traefikURLForConfig(traefik *backend.TraefikConfig) string {
	return service.TraefikURLForConfig(traefik)
}
