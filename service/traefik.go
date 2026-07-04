package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"polka/config"
)

const (
	TraefikListenHost               = "127.0.0.1"
	DefaultTraefikPort              = 8080
	managedTraefikStateDirectory    = "run"
	managedTraefikStateSubdirectory = "traefik"
	managedTraefikConfigSubdirName  = "dynamic"
	managedTraefikLogFileName       = "traefik.log"
	managedTraefikPollInterval      = 100 * time.Millisecond
	managedTraefikStartupTimeout    = 10 * time.Second
	managedTraefikShutdownTimeout   = 5 * time.Second
)

// TraefikRuntimeState is the persisted runtime state for one Traefik service.
type TraefikRuntimeState struct {
	EnvironmentName string    `json:"environment"`
	Version         string    `json:"version"`
	Port            int       `json:"port"`
	PID             int       `json:"pid"`
	ConfigDir       string    `json:"config_dir"`
	LogPath         string    `json:"log_path,omitempty"`
	StartedAt       time.Time `json:"started_at"`
}

// TraefikServerSpec describes a Traefik process launch.
type TraefikServerSpec struct {
	EnvironmentName string
	Version         string
	Target          string
	ConfigDir       string
	LogPath         string
	Port            int
	Env             []string
}

type TraefikStartResult struct {
	PID int
}

type TraefikRuntimeHooks struct {
	StartServer func(TraefikServerSpec) (TraefikStartResult, error)
	StopServer  func(TraefikRuntimeState) error
	PingAddress func(string) bool
	Now         func() time.Time
}

func EnsureManagedTraefikStarted(ctx Context, hooks TraefikRuntimeHooks) (TraefikRuntimeState, bool, error) {
	hooks = hooks.withDefaults()
	environment := ctx.Environment
	if environment.Traefik == nil || strings.TrimSpace(environment.Traefik.Version) == "" {
		return TraefikRuntimeState{}, true, nil
	}

	state, err := LoadLiveTraefikStateForEnvironment(ctx.RootDir, environment, hooks.PingAddress)
	if err != nil {
		return TraefikRuntimeState{}, false, err
	}
	if state != nil {
		return *state, true, nil
	}

	spec, err := BuildTraefikServerSpec(ctx)
	if err != nil {
		return TraefikRuntimeState{}, false, err
	}
	result, err := hooks.StartServer(spec)
	if err != nil {
		return TraefikRuntimeState{}, false, err
	}

	startedState := TraefikRuntimeState{
		EnvironmentName: environment.Name,
		Version:         spec.Version,
		Port:            spec.Port,
		PID:             result.PID,
		ConfigDir:       spec.ConfigDir,
		LogPath:         spec.LogPath,
		StartedAt:       hooks.Now().UTC(),
	}
	if err := WriteTraefikState(TraefikStatePath(ctx.RootDir, environment.Name), startedState); err != nil {
		_ = hooks.StopServer(startedState)
		return TraefikRuntimeState{}, false, err
	}

	return startedState, false, nil
}

func StopManagedTraefik(ctx Context, hooks TraefikRuntimeHooks) (TraefikRuntimeState, bool, error) {
	hooks = hooks.withDefaults()

	state, err := LoadLiveTraefikState(ctx.RootDir, ctx.Environment.Name, hooks.PingAddress)
	if err != nil {
		return TraefikRuntimeState{}, false, err
	}
	if state == nil {
		return TraefikRuntimeState{}, true, nil
	}
	if err := hooks.StopServer(*state); err != nil {
		return TraefikRuntimeState{}, false, err
	}
	if err := os.Remove(TraefikStatePath(ctx.RootDir, ctx.Environment.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return TraefikRuntimeState{}, false, fmt.Errorf("remove traefik state: %w", err)
	}

	return *state, false, nil
}

func BuildTraefikServerSpec(ctx Context) (TraefikServerSpec, error) {
	environment := ctx.Environment
	if environment.Traefik == nil {
		return TraefikServerSpec{}, fmt.Errorf("environment %q does not define traefik", environment.Name)
	}
	version := strings.TrimSpace(environment.Traefik.Version)
	if version == "" {
		return TraefikServerSpec{}, fmt.Errorf("environment %q defines traefik but does not define a version", environment.Name)
	}
	target, err := ctx.resolveInstalledTool(toolTraefik, version)
	if err != nil {
		return TraefikServerSpec{}, err
	}
	env, err := ctx.runtimeEnv()
	if err != nil {
		return TraefikServerSpec{}, err
	}

	return TraefikServerSpec{
		EnvironmentName: environment.Name,
		Version:         version,
		Target:          target,
		ConfigDir:       TraefikConfigDir(ctx.RootDir, environment.Name),
		LogPath:         TraefikLogPath(ctx.RootDir, environment.Name),
		Port:            EffectiveTraefikPort(environment.Traefik),
		Env:             env,
	}, nil
}

func StartTraefikServer(spec TraefikServerSpec) (TraefikStartResult, error) {
	if err := os.MkdirAll(spec.ConfigDir, 0o755); err != nil {
		return TraefikStartResult{}, fmt.Errorf("create traefik config directory: %w", err)
	}
	logFile, err := openServiceLog(spec.LogPath)
	if err != nil {
		return TraefikStartResult{}, err
	}

	args := TraefikServerArgs(spec)
	command, err := prepareServiceCommand(spec.Target, args)
	if err != nil {
		_ = logFile.Close()
		return TraefikStartResult{}, err
	}
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = spec.Env

	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return TraefikStartResult{}, fmt.Errorf("start traefik: %w", err)
	}

	address := TraefikAddress(spec.Port)
	if err := WaitForTraefikAddress(spec.Port, managedTraefikStartupTimeout, PingTraefikAddress); err != nil {
		stopServiceProcess(command.Process)
		_ = logFile.Close()
		return TraefikStartResult{}, fmt.Errorf("start traefik on %s: %w (see %s)", address, err, spec.LogPath)
	}

	pid := command.Process.Pid
	_ = logFile.Close()
	_ = command.Process.Release()

	return TraefikStartResult{PID: pid}, nil
}

// TraefikServerArgs builds the CLI flags that configure Traefik entirely from
// the command line: a single API/dashboard entrypoint on the managed port plus
// a file provider watching the project-local dynamic config directory.
func TraefikServerArgs(spec TraefikServerSpec) []string {
	args := []string{
		"--entrypoints.traefik.address=" + TraefikAddress(spec.Port),
		"--api.dashboard=true",
		"--api.insecure=true",
		"--ping=true",
	}
	if strings.TrimSpace(spec.ConfigDir) != "" {
		args = append(args,
			"--providers.file.directory="+spec.ConfigDir,
			"--providers.file.watch=true",
		)
	}

	return args
}

func StopTraefikRuntime(state TraefikRuntimeState) error {
	if err := stopServicePID(state.PID); err != nil {
		return err
	}

	deadline := time.Now().Add(managedTraefikShutdownTimeout)
	for time.Now().Before(deadline) {
		if !TraefikStateIsLive(state, PingTraefikAddress) {
			return nil
		}
		time.Sleep(managedTraefikPollInterval)
	}
	if !TraefikStateIsLive(state, PingTraefikAddress) {
		return nil
	}

	return fmt.Errorf("traefik did not stop listening on %s within %s", TraefikAddress(state.Port), managedTraefikShutdownTimeout)
}

func LoadLiveTraefikStateForEnvironment(rootDir string, environment config.Environment, ping func(string) bool) (*TraefikRuntimeState, error) {
	state, err := LoadLiveTraefikState(rootDir, environment.Name, ping)
	if err != nil || state == nil {
		return state, err
	}
	if TraefikStateMatchesEnvironment(*state, environment) {
		return state, nil
	}

	return nil, fmt.Errorf(
		"environment %q still has a running traefik %s on %s, but the current traefik is %s on %s; stop the running traefik first",
		environment.Name,
		state.Version,
		TraefikAddress(state.Port),
		strings.TrimSpace(environment.Traefik.Version),
		TraefikAddress(EffectiveTraefikPort(environment.Traefik)),
	)
}

func LoadLiveTraefikState(rootDir, environmentName string, ping func(string) bool) (*TraefikRuntimeState, error) {
	path := TraefikStatePath(rootDir, environmentName)
	state, err := LoadTraefikState(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if TraefikStateIsLive(*state, traefikPingFunc(ping)) {
		return state, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale traefik state: %w", err)
	}

	return nil, nil
}

func LoadTraefikState(path string) (*TraefikRuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state TraefikRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode traefik state %s: %w", path, err)
	}

	return &state, nil
}

func WriteTraefikState(path string, state TraefikRuntimeState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create traefik state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode traefik state: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write traefik state %s: %w", path, err)
	}

	return nil
}

func EffectiveTraefikPort(traefik *TraefikConfig) int {
	if traefik == nil || traefik.Port == 0 {
		return DefaultTraefikPort
	}

	return traefik.Port
}

func TraefikAddress(port int) string {
	return net.JoinHostPort(TraefikListenHost, strconv.Itoa(port))
}

func TraefikURL(state TraefikRuntimeState) string {
	return TraefikURLForConfig(&config.TraefikConfig{Port: state.Port})
}

func TraefikURLForConfig(traefik *config.TraefikConfig) string {
	if traefik == nil {
		return ""
	}

	return "http://" + TraefikAddress(EffectiveTraefikPort(traefik))
}

func TraefikStatePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedTraefikStateDirectory, managedTraefikStateSubdirectory, environmentName+".json")
}

func TraefikConfigDir(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedTraefikStateDirectory, managedTraefikStateSubdirectory, environmentName, managedTraefikConfigSubdirName)
}

func TraefikLogPath(rootDir, environmentName string) string {
	return filepath.Join(ToolLogRoot(rootDir, toolTraefik, environmentName), managedTraefikLogFileName)
}

func WaitForTraefikAddress(port int, timeout time.Duration, ping func(string) bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if traefikPingFunc(ping)(TraefikAddress(port)) {
			return nil
		}
		time.Sleep(managedTraefikPollInterval)
	}

	return fmt.Errorf("server did not start listening within %s", timeout)
}

func TraefikStateMatchesEnvironment(state TraefikRuntimeState, environment config.Environment) bool {
	if environment.Traefik == nil {
		return false
	}

	return strings.TrimSpace(state.Version) == strings.TrimSpace(environment.Traefik.Version) &&
		state.Port == EffectiveTraefikPort(environment.Traefik)
}

func TraefikStateIsLive(state TraefikRuntimeState, ping func(string) bool) bool {
	return traefikPingFunc(ping)(TraefikAddress(state.Port))
}

func PingTraefikAddress(address string) bool {
	connection, err := net.DialTimeout("tcp", address, managedTraefikPollInterval)
	if err != nil {
		return false
	}
	_ = connection.Close()

	return true
}

func traefikPingFunc(ping func(string) bool) func(string) bool {
	if ping == nil {
		return PingTraefikAddress
	}

	return ping
}

func (hooks TraefikRuntimeHooks) withDefaults() TraefikRuntimeHooks {
	if hooks.StartServer == nil {
		hooks.StartServer = StartTraefikServer
	}
	if hooks.StopServer == nil {
		hooks.StopServer = StopTraefikRuntime
	}
	if hooks.PingAddress == nil {
		hooks.PingAddress = PingTraefikAddress
	}
	if hooks.Now == nil {
		hooks.Now = time.Now
	}

	return hooks
}
