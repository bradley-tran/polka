package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"polka/backend"
)

const (
	managedPHPMyAdminStateDirectory    = "run"
	managedPHPMyAdminStateSubdirectory = "phpmyadmin"
)

var (
	startPHPMyAdminServeFunc              = startPHPMyAdminServe
	ensurePHPMyAdminStorageConfiguredFunc = backend.EnsurePHPMyAdminStorageConfigured
)

func ensureManagedPHPMyAdminStarted(store backend.Store, environment backend.Environment) (serveRuntimeState, bool, error) {
	if environment.PHPMyAdmin == nil || strings.TrimSpace(environment.PHPMyAdmin.Version) == "" {
		return serveRuntimeState{}, true, nil
	}

	state, err := loadLivePHPMyAdminStateForEnvironment(store.RootDir, environment)
	if err != nil {
		return serveRuntimeState{}, false, err
	}
	if state != nil {
		return *state, true, nil
	}

	version := strings.TrimSpace(environment.PHPMyAdmin.Version)
	docroot, err := backend.ResolvePHPMyAdminDocroot(store.EnvsDir, version)
	if err != nil {
		return serveRuntimeState{}, false, err
	}
	endpoint := phpMyAdminEndpoint(environment.PHPMyAdmin)
	layout, err := resolveServeAppLayout(docroot)
	if err != nil {
		return serveRuntimeState{}, false, err
	}

	startedState, err := startPHPMyAdminServeFunc(store, environment, endpoint, layout)
	if err != nil {
		return serveRuntimeState{}, false, err
	}
	startedState.EnvironmentName = environment.Name
	startedState.Version = version
	if strings.TrimSpace(startedState.ServerScheme) == "" {
		startedState.ServerScheme = endpoint.Scheme
	}
	if strings.TrimSpace(startedState.ServerAddress) == "" {
		startedState.ServerAddress = endpoint.Address
	}
	if strings.TrimSpace(startedState.Docroot) == "" {
		startedState.Docroot = layout.Docroot
	}
	if startedState.StartedAt.IsZero() {
		startedState.StartedAt = serveNowFunc().UTC()
	}

	if err := writePHPMyAdminState(phpMyAdminStatePath(store.RootDir, environment.Name), startedState); err != nil {
		_ = stopServeRuntimeFunc(startedState)
		return serveRuntimeState{}, false, err
	}

	return startedState, false, nil
}

func stopManagedPHPMyAdmin(store backend.Store, environmentName string) (serveRuntimeState, bool, error) {
	state, err := loadLivePHPMyAdminState(store.RootDir, environmentName)
	if err != nil {
		return serveRuntimeState{}, false, err
	}
	if state == nil {
		return serveRuntimeState{}, true, nil
	}
	if err := stopServeRuntimeFunc(*state); err != nil {
		return serveRuntimeState{}, false, err
	}
	if err := os.Remove(phpMyAdminStatePath(store.RootDir, environmentName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return serveRuntimeState{}, false, fmt.Errorf("remove phpmyadmin state: %w", err)
	}

	return *state, false, nil
}

func startPHPMyAdminServe(store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout) (serveRuntimeState, error) {
	if strings.TrimSpace(environment.PHPVersion) == "" {
		return serveRuntimeState{}, fmt.Errorf("environment %q defines phpmyadmin but does not define a php version", environment.Name)
	}

	runtimeDir := phpMyAdminRuntimeDir(store.RootDir, environment.Name)
	if endpoint.HTTPS {
		if strings.TrimSpace(environment.NginxVersion) == "" {
			return serveRuntimeState{}, fmt.Errorf("phpmyadmin.https requires nginx in the current environment")
		}

		return startNginxServeInBackgroundAt(store, environment, endpoint, layout, runtimeDir)
	}

	return startPHPRuntimeServeInBackgroundAt(store, environment, endpoint.Address, layout, runtimeDir)
}

func loadLivePHPMyAdminStateForEnvironment(rootDir string, environment backend.Environment) (*serveRuntimeState, error) {
	state, err := loadLivePHPMyAdminState(rootDir, environment.Name)
	if err != nil || state == nil {
		return state, err
	}
	if phpMyAdminStateMatchesEnvironment(*state, environment) {
		return state, nil
	}

	return nil, fmt.Errorf(
		"environment %q still has a running phpmyadmin %s at %s, but the current phpmyadmin is %s at %s; stop the running phpmyadmin first",
		environment.Name,
		state.Version,
		serveStateURL(*state),
		strings.TrimSpace(environment.PHPMyAdmin.Version),
		serverEndpointURL(phpMyAdminEndpoint(environment.PHPMyAdmin)),
	)
}

func loadLivePHPMyAdminState(rootDir, environmentName string) (*serveRuntimeState, error) {
	path := phpMyAdminStatePath(rootDir, environmentName)
	state, err := loadPHPMyAdminState(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(state.ServerAddress) != "" && pingServeAddressFunc(serveStateProbeAddress(*state)) {
		return state, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale phpmyadmin state: %w", err)
	}

	return nil, nil
}

func loadPHPMyAdminState(path string) (*serveRuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state serveRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode phpmyadmin state %s: %w", path, err)
	}

	return &state, nil
}

func writePHPMyAdminState(path string, state serveRuntimeState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create phpmyadmin state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode phpmyadmin state: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write phpmyadmin state %s: %w", path, err)
	}

	return nil
}

func phpMyAdminStateMatchesEnvironment(state serveRuntimeState, environment backend.Environment) bool {
	if environment.PHPMyAdmin == nil {
		return false
	}

	endpoint := phpMyAdminEndpoint(environment.PHPMyAdmin)
	return strings.TrimSpace(state.Version) == strings.TrimSpace(environment.PHPMyAdmin.Version) &&
		strings.EqualFold(strings.TrimSpace(state.ServerKind), desiredServeKind(endpoint.HTTPS)) &&
		strings.EqualFold(strings.TrimSpace(state.ServerAddress), strings.TrimSpace(endpoint.Address)) &&
		strings.EqualFold(strings.TrimSpace(state.ServerScheme), strings.TrimSpace(endpoint.Scheme))
}

func phpMyAdminStatePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedPHPMyAdminStateDirectory, managedPHPMyAdminStateSubdirectory, environmentName+".json")
}

func phpMyAdminRuntimeDir(rootDir, environmentName string) string {
	name := strings.TrimSpace(environmentName)
	if name == "" {
		name = "current"
	}

	return filepath.Join(rootDir, managedPHPMyAdminStateDirectory, managedPHPMyAdminStateSubdirectory, name)
}

func phpMyAdminUIURLForConfig(phpMyAdmin *backend.PHPMyAdminConfig) string {
	if phpMyAdmin == nil {
		return ""
	}

	return phpMyAdminUIScheme(phpMyAdmin.HTTPS) + "://" + backend.PHPMyAdminAddress(backend.EffectivePHPMyAdminPort(phpMyAdmin))
}

func phpMyAdminEndpoint(phpMyAdmin *backend.PHPMyAdminConfig) serverEndpoint {
	https := phpMyAdmin != nil && phpMyAdmin.HTTPS
	scheme := phpMyAdminUIScheme(https)
	return serverEndpoint{
		Scheme:  scheme,
		Address: backend.PHPMyAdminAddress(backend.EffectivePHPMyAdminPort(phpMyAdmin)),
		HTTPS:   https,
	}
}

func phpMyAdminUIScheme(https bool) string {
	if https {
		return "https"
	}

	return "http"
}
