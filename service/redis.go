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
	RedisListenHost               = "127.0.0.1"
	DefaultRedisPort              = 6379
	managedRedisStateDirectory    = "run"
	managedRedisStateSubdirectory = "redis"
	managedRedisDataDirectory     = "data"
	managedRedisDataSubdirectory  = "redis"
	managedRedisLogFileName       = "redis.log"
	managedRedisPollInterval      = 100 * time.Millisecond
	managedRedisStartupTimeout    = 10 * time.Second
	managedRedisShutdownTimeout   = 5 * time.Second
)

// RedisRuntimeState is the persisted runtime state for one Redis service.
type RedisRuntimeState struct {
	EnvironmentName string    `json:"environment"`
	Version         string    `json:"version"`
	Port            int       `json:"port"`
	PID             int       `json:"pid"`
	DataDir         string    `json:"data_dir"`
	LogPath         string    `json:"log_path,omitempty"`
	StartedAt       time.Time `json:"started_at"`
}

// RedisServerSpec describes a Redis server process launch.
type RedisServerSpec struct {
	EnvironmentName string
	Version         string
	Target          string
	DataDir         string
	LogPath         string
	Port            int
	Env             []string
}

// RedisStartResult reports the started Redis process.
type RedisStartResult struct {
	PID int
}

// RedisRuntimeHooks allows tests and the CLI adapter to override Redis process operations.
type RedisRuntimeHooks struct {
	StartServer func(RedisServerSpec) (RedisStartResult, error)
	StopServer  func(RedisRuntimeState) error
	PingAddress func(string) bool
	Now         func() time.Time
}

// EnsureManagedRedisStarted starts Redis for the environment unless a matching live state already exists.
func EnsureManagedRedisStarted(ctx Context, hooks RedisRuntimeHooks) (RedisRuntimeState, bool, error) {
	hooks = hooks.withDefaults()
	environment := ctx.Environment
	if environment.Redis == nil || strings.TrimSpace(environment.Redis.Version) == "" {
		return RedisRuntimeState{}, true, nil
	}

	state, err := LoadLiveRedisStateForEnvironment(ctx.RootDir, environment, hooks.PingAddress)
	if err != nil {
		return RedisRuntimeState{}, false, err
	}
	if state != nil {
		return *state, true, nil
	}

	spec, err := BuildRedisServerSpec(ctx)
	if err != nil {
		return RedisRuntimeState{}, false, err
	}
	result, err := hooks.StartServer(spec)
	if err != nil {
		return RedisRuntimeState{}, false, err
	}

	startedState := RedisRuntimeState{
		EnvironmentName: environment.Name,
		Version:         spec.Version,
		Port:            spec.Port,
		PID:             result.PID,
		DataDir:         spec.DataDir,
		LogPath:         spec.LogPath,
		StartedAt:       hooks.Now().UTC(),
	}
	if err := WriteRedisState(RedisStatePath(ctx.RootDir, environment.Name), startedState); err != nil {
		_ = hooks.StopServer(startedState)
		return RedisRuntimeState{}, false, err
	}

	return startedState, false, nil
}

// StopManagedRedis stops a live Redis runtime and removes its state file.
func StopManagedRedis(ctx Context, hooks RedisRuntimeHooks) (RedisRuntimeState, bool, error) {
	hooks = hooks.withDefaults()

	state, err := LoadLiveRedisState(ctx.RootDir, ctx.Environment.Name, hooks.PingAddress)
	if err != nil {
		return RedisRuntimeState{}, false, err
	}
	if state == nil {
		return RedisRuntimeState{}, true, nil
	}
	if err := hooks.StopServer(*state); err != nil {
		return RedisRuntimeState{}, false, err
	}
	if err := os.Remove(RedisStatePath(ctx.RootDir, ctx.Environment.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return RedisRuntimeState{}, false, fmt.Errorf("remove redis state: %w", err)
	}

	return *state, false, nil
}

// BuildRedisServerSpec resolves paths and runtime environment for the Redis process.
func BuildRedisServerSpec(ctx Context) (RedisServerSpec, error) {
	environment := ctx.Environment
	if environment.Redis == nil {
		return RedisServerSpec{}, fmt.Errorf("environment %q does not define redis", environment.Name)
	}
	version := strings.TrimSpace(environment.Redis.Version)
	if version == "" {
		return RedisServerSpec{}, fmt.Errorf("environment %q defines redis but does not define a version", environment.Name)
	}
	target, err := ctx.resolveInstalledTool(toolRedis, version)
	if err != nil {
		return RedisServerSpec{}, err
	}
	env, err := ctx.runtimeEnv()
	if err != nil {
		return RedisServerSpec{}, err
	}

	return RedisServerSpec{
		EnvironmentName: environment.Name,
		Version:         version,
		Target:          target,
		DataDir:         RedisDataPath(ctx.RootDir, environment.Name),
		LogPath:         RedisLogPath(ctx.RootDir, environment.Name),
		Port:            EffectiveRedisPort(environment.Redis),
		Env:             env,
	}, nil
}

// StartRedisServer launches redis-server in the foreground and waits for its TCP port.
func StartRedisServer(spec RedisServerSpec) (RedisStartResult, error) {
	if err := os.MkdirAll(spec.DataDir, 0o755); err != nil {
		return RedisStartResult{}, fmt.Errorf("create redis data directory: %w", err)
	}
	logFile, err := openServiceLog(spec.LogPath)
	if err != nil {
		return RedisStartResult{}, err
	}

	command, err := prepareServiceCommand(spec.Target, RedisServerArgs(spec))
	if err != nil {
		_ = logFile.Close()
		return RedisStartResult{}, err
	}
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = spec.Env

	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return RedisStartResult{}, fmt.Errorf("start redis: %w", err)
	}

	address := RedisAddress(spec.Port)
	if err := WaitForRedisAddress(spec.Port, managedRedisStartupTimeout, PingRedisAddress); err != nil {
		stopServiceProcess(command.Process)
		_ = logFile.Close()
		return RedisStartResult{}, fmt.Errorf("start redis on %s: %w (see %s)", address, err, spec.LogPath)
	}

	pid := command.Process.Pid
	_ = logFile.Close()
	_ = command.Process.Release()

	return RedisStartResult{PID: pid}, nil
}

// RedisServerArgs returns redis-server arguments for Polka's managed runtime.
func RedisServerArgs(spec RedisServerSpec) []string {
	return []string{
		"--bind", RedisListenHost,
		"--port", strconv.Itoa(spec.Port),
		"--dir", spec.DataDir,
		"--daemonize", "no",
	}
}

// StopRedisRuntime terminates a Redis process and waits for its port to close.
func StopRedisRuntime(state RedisRuntimeState) error {
	if err := stopServicePID(state.PID); err != nil {
		return err
	}

	deadline := time.Now().Add(managedRedisShutdownTimeout)
	for time.Now().Before(deadline) {
		if !RedisStateIsLive(state, PingRedisAddress) {
			return nil
		}
		time.Sleep(managedRedisPollInterval)
	}
	if !RedisStateIsLive(state, PingRedisAddress) {
		return nil
	}

	return fmt.Errorf("redis did not stop listening on %s within %s", RedisAddress(state.Port), managedRedisShutdownTimeout)
}

// LoadLiveRedisStateForEnvironment returns live Redis state only when it matches the current config.
func LoadLiveRedisStateForEnvironment(rootDir string, environment config.Environment, ping func(string) bool) (*RedisRuntimeState, error) {
	state, err := LoadLiveRedisState(rootDir, environment.Name, ping)
	if err != nil || state == nil {
		return state, err
	}
	if RedisStateMatchesEnvironment(*state, environment) {
		return state, nil
	}

	return nil, fmt.Errorf(
		"environment %q still has a running redis %s on %s, but the current redis is %s on %s; stop the running redis first",
		environment.Name,
		state.Version,
		RedisAddress(state.Port),
		strings.TrimSpace(environment.Redis.Version),
		RedisAddress(EffectiveRedisPort(environment.Redis)),
	)
}

// LoadLiveRedisState loads state, drops stale state files, and returns nil for stopped Redis.
func LoadLiveRedisState(rootDir, environmentName string, ping func(string) bool) (*RedisRuntimeState, error) {
	path := RedisStatePath(rootDir, environmentName)
	state, err := LoadRedisState(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if RedisStateIsLive(*state, redisPingFunc(ping)) {
		return state, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale redis state: %w", err)
	}

	return nil, nil
}

// LoadRedisState decodes a Redis runtime state file.
func LoadRedisState(path string) (*RedisRuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state RedisRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode redis state %s: %w", path, err)
	}

	return &state, nil
}

// WriteRedisState writes a Redis runtime state file.
func WriteRedisState(path string, state RedisRuntimeState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create redis state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode redis state: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write redis state %s: %w", path, err)
	}

	return nil
}

// EffectiveRedisPort returns the configured Redis port, or the default when unset.
func EffectiveRedisPort(redis *RedisConfig) int {
	if redis == nil || redis.Port == 0 {
		return DefaultRedisPort
	}

	return redis.Port
}

// RedisAddress returns the local TCP address Redis should bind.
func RedisAddress(port int) string {
	return net.JoinHostPort(RedisListenHost, strconv.Itoa(port))
}

// RedisAddressForConfig returns the effective Redis address for a config.
func RedisAddressForConfig(redis *config.RedisConfig) string {
	if redis == nil {
		return ""
	}

	return RedisAddress(EffectiveRedisPort(redis))
}

// RedisStatePath returns the state file path for one Redis environment.
func RedisStatePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedRedisStateDirectory, managedRedisStateSubdirectory, environmentName+".json")
}

// RedisDataPath returns the Redis data directory for one environment.
func RedisDataPath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedRedisDataDirectory, managedRedisDataSubdirectory, environmentName)
}

// RedisLogPath returns the Redis log file path for one environment.
func RedisLogPath(rootDir, environmentName string) string {
	return filepath.Join(ToolLogRoot(rootDir, toolRedis, environmentName), managedRedisLogFileName)
}

// WaitForRedisAddress waits until Redis accepts TCP connections on its configured port.
func WaitForRedisAddress(port int, timeout time.Duration, ping func(string) bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if redisPingFunc(ping)(RedisAddress(port)) {
			return nil
		}
		time.Sleep(managedRedisPollInterval)
	}

	return fmt.Errorf("server did not start listening within %s", timeout)
}

// RedisStateMatchesEnvironment reports whether a live state belongs to the current config.
func RedisStateMatchesEnvironment(state RedisRuntimeState, environment config.Environment) bool {
	if environment.Redis == nil {
		return false
	}

	return strings.TrimSpace(state.Version) == strings.TrimSpace(environment.Redis.Version) &&
		state.Port == EffectiveRedisPort(environment.Redis)
}

// RedisStateIsLive reports whether the state still has an accepting TCP address.
func RedisStateIsLive(state RedisRuntimeState, ping func(string) bool) bool {
	return redisPingFunc(ping)(RedisAddress(state.Port))
}

// PingRedisAddress probes a Redis TCP address.
func PingRedisAddress(address string) bool {
	connection, err := net.DialTimeout("tcp", address, managedRedisPollInterval)
	if err != nil {
		return false
	}
	_ = connection.Close()

	return true
}

func redisPingFunc(ping func(string) bool) func(string) bool {
	if ping == nil {
		return PingRedisAddress
	}

	return ping
}

func (hooks RedisRuntimeHooks) withDefaults() RedisRuntimeHooks {
	if hooks.StartServer == nil {
		hooks.StartServer = StartRedisServer
	}
	if hooks.StopServer == nil {
		hooks.StopServer = StopRedisRuntime
	}
	if hooks.PingAddress == nil {
		hooks.PingAddress = PingRedisAddress
	}
	if hooks.Now == nil {
		hooks.Now = time.Now
	}

	return hooks
}
