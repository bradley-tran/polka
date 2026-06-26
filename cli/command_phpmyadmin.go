package cli

import (
	"fmt"

	"polka/backend"
	"polka/config"
	"polka/service"
)

var (
	startPHPMyAdminServeFunc              = startPHPMyAdminServe
	ensurePHPMyAdminStorageConfiguredFunc = service.EnsurePHPMyAdminStorageConfigured
	startPHPMyAdminPHPRuntimeServeFunc    = startPHPRuntimeServeInBackgroundAt
	startPHPMyAdminNginxServeFunc         = startNginxServeInBackgroundAt
	startPHPMyAdminApacheServeFunc        = startApacheServeInBackgroundAt
	startPHPMyAdminFrankenPHPServeFunc    = startFrankenPHPServeInBackgroundAt
)

func ensureManagedPHPMyAdminStarted(store backend.Store, environment backend.Environment) (serveRuntimeState, bool, error) {
	return service.EnsureManagedPHPMyAdminStarted(managedServiceContext(store, environment, nil), phpMyAdminRuntimeHooks(store))
}

func stopManagedPHPMyAdmin(store backend.Store, environmentName string) (serveRuntimeState, bool, error) {
	return service.StopManagedPHPMyAdmin(managedServiceContext(store, backend.Environment{Name: environmentName}, nil), phpMyAdminRuntimeHooks(store))
}

func phpMyAdminRuntimeHooks(store backend.Store) service.PHPMyAdminRuntimeHooks {
	return service.PHPMyAdminRuntimeHooks{
		ResolveLayout: func(docroot string) (service.AppLayout, error) {
			return resolveServeAppLayout(docroot)
		},
		StartServe: func(environment service.Environment, endpoint service.Endpoint, layout service.AppLayout, runtimeDir string) (service.ServeRuntimeState, error) {
			return startPHPMyAdminServeFunc(store, environment, endpoint, layout, runtimeDir)
		},
		StopServe:   stopServeRuntimeFunc,
		PingAddress: pingServeAddressFunc,
		Now:         serveNowFunc,
	}
}

func startPHPMyAdminServe(store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout, runtimeDir string) (serveRuntimeState, error) {
	if !backend.HasPHPCLI(environment) {
		return serveRuntimeState{}, fmt.Errorf("environment %q defines phpmyadmin but does not define a PHP CLI provider", environment.Name)
	}

	if endpoint.HTTPS {
		serverType, err := resolveEnvironmentServerType(environment)
		if err != nil {
			return serveRuntimeState{}, err
		}
		switch serverType {
		case config.ServerTypeFrankenPHP:
			return startPHPMyAdminFrankenPHPServeFunc(store, environment, endpoint, layout, runtimeDir)
		case config.ServerTypeNginx:
			return startPHPMyAdminNginxServeFunc(store, environment, endpoint, layout, runtimeDir)
		case config.ServerTypeApache:
			return startPHPMyAdminApacheServeFunc(store, environment, endpoint, layout, runtimeDir)
		default:
			return serveRuntimeState{}, fmt.Errorf("https requires nginx, Apache, or FrankenPHP in the current environment")
		}
	}

	return startPHPMyAdminPHPRuntimeServeFunc(store, environment, endpoint.Address, layout, runtimeDir)
}

func loadLivePHPMyAdminState(rootDir, environmentName string) (*serveRuntimeState, error) {
	return service.LoadLivePHPMyAdminState(rootDir, environmentName, pingServeAddressFunc)
}

func loadPHPMyAdminState(path string) (*serveRuntimeState, error) {
	return service.LoadPHPMyAdminState(path)
}

func writePHPMyAdminState(path string, state serveRuntimeState) error {
	return service.WritePHPMyAdminState(path, state)
}

func phpMyAdminStatePath(rootDir, environmentName string) string {
	return service.PHPMyAdminStatePath(rootDir, environmentName)
}

func phpMyAdminRuntimeDir(rootDir, environmentName string) string {
	return service.PHPMyAdminRuntimeDir(rootDir, environmentName)
}

func phpMyAdminUIURLForConfig(phpMyAdmin *backend.PHPMyAdminConfig) string {
	return service.PHPMyAdminUIURLForConfig(phpMyAdmin)
}

func phpMyAdminEndpoint(phpMyAdmin *backend.PHPMyAdminConfig) serverEndpoint {
	return service.PHPMyAdminEndpoint(phpMyAdmin)
}
