package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"polka/config"
)

const (
	managedPHPMyAdminStateDirectory    = "run"
	managedPHPMyAdminStateSubdirectory = "phpmyadmin"
	defaultServeHostname               = "localhost"
	serveProxyHost                     = "127.0.0.1"
)

// ServeRuntimeState is the persisted runtime state shape used by web-backed
// services such as phpMyAdmin.
type ServeRuntimeState struct {
	EnvironmentName string    `json:"environment"`
	Version         string    `json:"version,omitempty"`
	ServerKind      string    `json:"server_kind"`
	ServerScheme    string    `json:"server_scheme,omitempty"`
	ServerAddress   string    `json:"server_address"`
	Docroot         string    `json:"docroot"`
	RuntimeDir      string    `json:"runtime_dir,omitempty"`
	LogPath         string    `json:"log_path,omitempty"`
	BackendLogPath  string    `json:"backend_log_path,omitempty"`
	ConfigPath      string    `json:"config_path,omitempty"`
	RouterPath      string    `json:"router_path,omitempty"`
	PrimaryPID      int       `json:"primary_pid"`
	SecondaryPID    int       `json:"secondary_pid,omitempty"`
	StartedAt       time.Time `json:"started_at"`
}

type AppLayout struct {
	Docroot                 string
	FrontControllerRelative string
	FrontControllerWebPath  string
	FrontControllerIndex    string
}

type Endpoint struct {
	Scheme  string
	Address string
	HTTPS   bool
}

type PHPMyAdminRuntimeHooks struct {
	ResolveLayout func(docroot string) (AppLayout, error)
	StartServe    func(environment config.Environment, endpoint Endpoint, layout AppLayout, runtimeDir string) (ServeRuntimeState, error)
	StopServe     func(ServeRuntimeState) error
	PingAddress   func(string) bool
	Now           func() time.Time
}

func EnsureManagedPHPMyAdminStarted(ctx Context, hooks PHPMyAdminRuntimeHooks) (ServeRuntimeState, bool, error) {
	hooks = hooks.withDefaults()
	environment := ctx.Environment
	if environment.PHPMyAdmin == nil || strings.TrimSpace(environment.PHPMyAdmin.Version) == "" {
		return ServeRuntimeState{}, true, nil
	}

	state, err := LoadLivePHPMyAdminStateForEnvironment(ctx.RootDir, environment, hooks.PingAddress)
	if err != nil {
		return ServeRuntimeState{}, false, err
	}
	if state != nil {
		return *state, true, nil
	}

	version := strings.TrimSpace(environment.PHPMyAdmin.Version)
	docroot, err := ResolvePHPMyAdminDocroot(ctx.EnvsDir, version)
	if err != nil {
		return ServeRuntimeState{}, false, err
	}
	layout, err := hooks.ResolveLayout(docroot)
	if err != nil {
		return ServeRuntimeState{}, false, err
	}
	endpoint := PHPMyAdminEndpoint(environment.PHPMyAdmin)
	runtimeDir := PHPMyAdminRuntimeDir(ctx.RootDir, environment.Name)
	startedState, err := hooks.StartServe(environment, endpoint, layout, runtimeDir)
	if err != nil {
		return ServeRuntimeState{}, false, err
	}
	startedState.EnvironmentName = environment.Name
	startedState.Version = version
	if strings.TrimSpace(startedState.ServerKind) == "" {
		startedState.ServerKind = DesiredServeKind(endpoint.HTTPS)
	}
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
		startedState.StartedAt = hooks.Now().UTC()
	}

	if err := WritePHPMyAdminState(PHPMyAdminStatePath(ctx.RootDir, environment.Name), startedState); err != nil {
		_ = hooks.StopServe(startedState)
		return ServeRuntimeState{}, false, err
	}

	return startedState, false, nil
}

func StopManagedPHPMyAdmin(ctx Context, hooks PHPMyAdminRuntimeHooks) (ServeRuntimeState, bool, error) {
	hooks = hooks.withDefaults()

	state, err := LoadLivePHPMyAdminState(ctx.RootDir, ctx.Environment.Name, hooks.PingAddress)
	if err != nil {
		return ServeRuntimeState{}, false, err
	}
	if state == nil {
		return ServeRuntimeState{}, true, nil
	}
	if err := hooks.StopServe(*state); err != nil {
		return ServeRuntimeState{}, false, err
	}
	if err := os.Remove(PHPMyAdminStatePath(ctx.RootDir, ctx.Environment.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ServeRuntimeState{}, false, fmt.Errorf("remove phpmyadmin state: %w", err)
	}

	return *state, false, nil
}

func LoadLivePHPMyAdminStateForEnvironment(rootDir string, environment config.Environment, ping func(string) bool) (*ServeRuntimeState, error) {
	state, err := LoadLivePHPMyAdminState(rootDir, environment.Name, ping)
	if err != nil || state == nil {
		return state, err
	}
	if PHPMyAdminStateMatchesEnvironment(*state, environment) {
		return state, nil
	}

	return nil, fmt.Errorf(
		"environment %q still has a running phpmyadmin %s at %s, but the current phpmyadmin is %s at %s; stop the running phpmyadmin first",
		environment.Name,
		state.Version,
		ServeStateURL(*state),
		strings.TrimSpace(environment.PHPMyAdmin.Version),
		EndpointURL(PHPMyAdminEndpoint(environment.PHPMyAdmin)),
	)
}

func LoadLivePHPMyAdminState(rootDir, environmentName string, ping func(string) bool) (*ServeRuntimeState, error) {
	path := PHPMyAdminStatePath(rootDir, environmentName)
	state, err := LoadPHPMyAdminState(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(state.ServerAddress) != "" && phpMyAdminPingFunc(ping)(ServeStateProbeAddress(*state)) {
		return state, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale phpmyadmin state: %w", err)
	}

	return nil, nil
}

func LoadPHPMyAdminState(path string) (*ServeRuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state ServeRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode phpmyadmin state %s: %w", path, err)
	}

	return &state, nil
}

func WritePHPMyAdminState(path string, state ServeRuntimeState) error {
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

func PHPMyAdminStateMatchesEnvironment(state ServeRuntimeState, environment config.Environment) bool {
	if environment.PHPMyAdmin == nil {
		return false
	}

	endpoint := PHPMyAdminEndpoint(environment.PHPMyAdmin)
	return strings.TrimSpace(state.Version) == strings.TrimSpace(environment.PHPMyAdmin.Version) &&
		strings.EqualFold(strings.TrimSpace(state.ServerKind), DesiredServeKind(endpoint.HTTPS)) &&
		strings.EqualFold(strings.TrimSpace(state.ServerAddress), strings.TrimSpace(endpoint.Address)) &&
		strings.EqualFold(strings.TrimSpace(state.ServerScheme), strings.TrimSpace(endpoint.Scheme))
}

func PHPMyAdminStatePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedPHPMyAdminStateDirectory, managedPHPMyAdminStateSubdirectory, environmentName+".json")
}

func PHPMyAdminRuntimeDir(rootDir, environmentName string) string {
	name := strings.TrimSpace(environmentName)
	if name == "" {
		name = "current"
	}

	return filepath.Join(rootDir, managedPHPMyAdminStateDirectory, managedPHPMyAdminStateSubdirectory, name)
}

func PHPMyAdminUIURLForConfig(phpMyAdmin *config.PHPMyAdminConfig) string {
	if phpMyAdmin == nil {
		return ""
	}

	return PHPMyAdminUIScheme(phpMyAdmin.HTTPS) + "://" + PHPMyAdminAddress(EffectivePHPMyAdminPort(phpMyAdmin))
}

func PHPMyAdminEndpoint(phpMyAdmin *config.PHPMyAdminConfig) Endpoint {
	https := phpMyAdmin != nil && phpMyAdmin.HTTPS
	scheme := PHPMyAdminUIScheme(https)
	return Endpoint{
		Scheme:  scheme,
		Address: PHPMyAdminAddress(EffectivePHPMyAdminPort(phpMyAdmin)),
		HTTPS:   https,
	}
}

func PHPMyAdminUIScheme(https bool) string {
	if https {
		return "https"
	}

	return "http"
}

func DesiredServeKind(useNginx bool) string {
	if useNginx {
		return "nginx"
	}

	return "php"
}

func EndpointURL(endpoint Endpoint) string {
	scheme := strings.TrimSpace(endpoint.Scheme)
	if scheme == "" {
		scheme = "http"
	}

	return scheme + "://" + strings.TrimSpace(endpoint.Address)
}

func ServeStateURL(state ServeRuntimeState) string {
	return EndpointURL(Endpoint{
		Scheme:  strings.TrimSpace(state.ServerScheme),
		Address: strings.TrimSpace(state.ServerAddress),
	})
}

func ServeStateProbeAddress(state ServeRuntimeState) string {
	return ServeProbeAddress(strings.TrimSpace(state.ServerAddress))
}

func ServeProbeAddress(address string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return address
	}
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	if strings.HasSuffix(strings.ToLower(trimmed), "."+defaultServeHostname) {
		return net.JoinHostPort(serveProxyHost, port)
	}

	return address
}

func phpMyAdminPingFunc(ping func(string) bool) func(string) bool {
	if ping == nil {
		return func(string) bool { return false }
	}

	return ping
}

func (hooks PHPMyAdminRuntimeHooks) withDefaults() PHPMyAdminRuntimeHooks {
	if hooks.ResolveLayout == nil {
		hooks.ResolveLayout = func(string) (AppLayout, error) {
			return AppLayout{}, fmt.Errorf("phpmyadmin layout resolver is not configured")
		}
	}
	if hooks.StartServe == nil {
		hooks.StartServe = func(config.Environment, Endpoint, AppLayout, string) (ServeRuntimeState, error) {
			return ServeRuntimeState{}, fmt.Errorf("phpmyadmin web runtime starter is not configured")
		}
	}
	if hooks.StopServe == nil {
		hooks.StopServe = func(ServeRuntimeState) error { return nil }
	}
	if hooks.Now == nil {
		hooks.Now = time.Now
	}

	return hooks
}
