package cli

import (
	"fmt"
	"strings"

	"polka/backend"
	"polka/service"
)

var (
	startPHPMyAdminServeFunc              = startPHPMyAdminServe
	ensurePHPMyAdminStorageConfiguredFunc = service.EnsurePHPMyAdminStorageConfigured
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
	if strings.TrimSpace(environment.PHPVersion) == "" {
		return serveRuntimeState{}, fmt.Errorf("environment %q defines phpmyadmin but does not define a php version", environment.Name)
	}

	if endpoint.HTTPS {
		if strings.TrimSpace(environment.NginxVersion) == "" {
			return serveRuntimeState{}, fmt.Errorf("https requires nginx in the current environment")
		}

		return startNginxServeInBackgroundAt(store, environment, endpoint, layout, runtimeDir)
	}

	return startPHPRuntimeServeInBackgroundAt(store, environment, endpoint.Address, layout, runtimeDir)
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
