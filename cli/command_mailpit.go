package cli

import (
	"time"

	"polka/backend"
	"polka/service"
)

var (
	startMailpitServerFunc = service.StartMailpitServer
	stopMailpitRuntimeFunc = service.StopMailpitRuntime
	pingMailpitAddressFunc = service.PingMailpitAddress
	mailpitNowFunc         = time.Now
)

type mailpitRuntimeState = service.MailpitRuntimeState
type mailpitServerSpec = service.MailpitServerSpec
type mailpitStartResult = service.MailpitStartResult

func ensureManagedMailpitStarted(store backend.Store, environment backend.Environment) (mailpitRuntimeState, bool, error) {
	return service.EnsureManagedMailpitStarted(managedServiceContext(store, environment, nil), mailpitRuntimeHooks())
}

func stopManagedMailpit(store backend.Store, environmentName string) (mailpitRuntimeState, bool, error) {
	return service.StopManagedMailpit(managedServiceContext(store, backend.Environment{Name: environmentName}, nil), mailpitRuntimeHooks())
}

func mailpitRuntimeHooks() service.MailpitRuntimeHooks {
	return service.MailpitRuntimeHooks{
		StartServer: startMailpitServerFunc,
		StopServer:  stopMailpitRuntimeFunc,
		PingAddress: pingMailpitAddressFunc,
		Now:         mailpitNowFunc,
	}
}

func mailpitServerArgs(spec mailpitServerSpec) []string {
	return service.MailpitServerArgs(spec)
}

func mailpitStatePath(rootDir, environmentName string) string {
	return service.MailpitStatePath(rootDir, environmentName)
}

func loadMailpitState(path string) (*mailpitRuntimeState, error) {
	return service.LoadMailpitState(path)
}

func writeMailpitState(path string, state mailpitRuntimeState) error {
	return service.WriteMailpitState(path, state)
}

func loadLiveMailpitState(rootDir, environmentName string) (*mailpitRuntimeState, error) {
	return service.LoadLiveMailpitState(rootDir, environmentName, pingMailpitAddressFunc)
}

func mailpitUIURL(state mailpitRuntimeState) string {
	return service.MailpitUIURL(state)
}

func mailpitUIURLForConfig(mailpit *backend.MailpitConfig) string {
	return service.MailpitUIURLForConfig(mailpit)
}
