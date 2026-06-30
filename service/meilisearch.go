package service

import (
	"crypto/sha256"
	"encoding/hex"
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
	MeilisearchListenHost               = "127.0.0.1"
	DefaultMeilisearchPort              = 7700
	managedMeilisearchStateDirectory    = "run"
	managedMeilisearchStateSubdirectory = "meilisearch"
	managedMeilisearchDataDirectory     = "data"
	managedMeilisearchDataSubdirectory  = "meilisearch"
	managedMeilisearchLogFileName       = "meilisearch.log"
	managedMeilisearchPollInterval      = 100 * time.Millisecond
	managedMeilisearchStartupTimeout    = 10 * time.Second
	managedMeilisearchShutdownTimeout   = 5 * time.Second
)

// MeilisearchRuntimeState is the persisted runtime state for one Meilisearch service.
type MeilisearchRuntimeState struct {
	EnvironmentName string    `json:"environment"`
	Version         string    `json:"version"`
	Port            int       `json:"port"`
	PID             int       `json:"pid"`
	DataDir         string    `json:"data_dir"`
	LogPath         string    `json:"log_path,omitempty"`
	AuthEnabled     bool      `json:"auth_enabled"`
	MasterKeyHash   string    `json:"master_key_hash,omitempty"`
	StartedAt       time.Time `json:"started_at"`
}

// MeilisearchServerSpec describes a Meilisearch process launch.
type MeilisearchServerSpec struct {
	EnvironmentName string
	Version         string
	Target          string
	DataDir         string
	LogPath         string
	Port            int
	MasterKey       string
	Env             []string
}

type MeilisearchStartResult struct {
	PID int
}

type MeilisearchRuntimeHooks struct {
	StartServer func(MeilisearchServerSpec) (MeilisearchStartResult, error)
	StopServer  func(MeilisearchRuntimeState) error
	PingAddress func(string) bool
	Now         func() time.Time
}

func EnsureManagedMeilisearchStarted(ctx Context, hooks MeilisearchRuntimeHooks) (MeilisearchRuntimeState, bool, error) {
	hooks = hooks.withDefaults()
	environment := ctx.Environment
	if environment.Meilisearch == nil || strings.TrimSpace(environment.Meilisearch.Version) == "" {
		return MeilisearchRuntimeState{}, true, nil
	}

	state, err := LoadLiveMeilisearchStateForEnvironment(ctx.RootDir, environment, hooks.PingAddress)
	if err != nil {
		return MeilisearchRuntimeState{}, false, err
	}
	if state != nil {
		return *state, true, nil
	}

	spec, err := BuildMeilisearchServerSpec(ctx)
	if err != nil {
		return MeilisearchRuntimeState{}, false, err
	}
	result, err := hooks.StartServer(spec)
	if err != nil {
		return MeilisearchRuntimeState{}, false, err
	}

	startedState := MeilisearchRuntimeState{
		EnvironmentName: environment.Name,
		Version:         spec.Version,
		Port:            spec.Port,
		PID:             result.PID,
		DataDir:         spec.DataDir,
		LogPath:         spec.LogPath,
		AuthEnabled:     strings.TrimSpace(spec.MasterKey) != "",
		MasterKeyHash:   MeilisearchMasterKeyHash(spec.MasterKey),
		StartedAt:       hooks.Now().UTC(),
	}
	if err := WriteMeilisearchState(MeilisearchStatePath(ctx.RootDir, environment.Name), startedState); err != nil {
		_ = hooks.StopServer(startedState)
		return MeilisearchRuntimeState{}, false, err
	}

	return startedState, false, nil
}

func StopManagedMeilisearch(ctx Context, hooks MeilisearchRuntimeHooks) (MeilisearchRuntimeState, bool, error) {
	hooks = hooks.withDefaults()

	state, err := LoadLiveMeilisearchState(ctx.RootDir, ctx.Environment.Name, hooks.PingAddress)
	if err != nil {
		return MeilisearchRuntimeState{}, false, err
	}
	if state == nil {
		return MeilisearchRuntimeState{}, true, nil
	}
	if err := hooks.StopServer(*state); err != nil {
		return MeilisearchRuntimeState{}, false, err
	}
	if err := os.Remove(MeilisearchStatePath(ctx.RootDir, ctx.Environment.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return MeilisearchRuntimeState{}, false, fmt.Errorf("remove meilisearch state: %w", err)
	}

	return *state, false, nil
}

func BuildMeilisearchServerSpec(ctx Context) (MeilisearchServerSpec, error) {
	environment := ctx.Environment
	if environment.Meilisearch == nil {
		return MeilisearchServerSpec{}, fmt.Errorf("environment %q does not define meilisearch", environment.Name)
	}
	version := strings.TrimSpace(environment.Meilisearch.Version)
	if version == "" {
		return MeilisearchServerSpec{}, fmt.Errorf("environment %q defines meilisearch but does not define a version", environment.Name)
	}
	target, err := ctx.resolveInstalledTool(toolMeilisearch, version)
	if err != nil {
		return MeilisearchServerSpec{}, err
	}
	env, err := ctx.runtimeEnv()
	if err != nil {
		return MeilisearchServerSpec{}, err
	}

	return MeilisearchServerSpec{
		EnvironmentName: environment.Name,
		Version:         version,
		Target:          target,
		DataDir:         MeilisearchDataPath(ctx.RootDir, environment.Name),
		LogPath:         MeilisearchLogPath(ctx.RootDir, environment.Name),
		Port:            EffectiveMeilisearchPort(environment.Meilisearch),
		MasterKey:       strings.TrimSpace(environment.Meilisearch.MasterKey),
		Env:             env,
	}, nil
}

func StartMeilisearchServer(spec MeilisearchServerSpec) (MeilisearchStartResult, error) {
	if err := os.MkdirAll(spec.DataDir, 0o755); err != nil {
		return MeilisearchStartResult{}, fmt.Errorf("create meilisearch data directory: %w", err)
	}
	logFile, err := openServiceLog(spec.LogPath)
	if err != nil {
		return MeilisearchStartResult{}, err
	}

	args := MeilisearchServerArgs(spec)
	command, err := prepareServiceCommand(spec.Target, args)
	if err != nil {
		_ = logFile.Close()
		return MeilisearchStartResult{}, err
	}
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = spec.Env

	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return MeilisearchStartResult{}, fmt.Errorf("start meilisearch: %w", err)
	}

	address := MeilisearchAddress(spec.Port)
	if err := WaitForMeilisearchAddress(spec.Port, managedMeilisearchStartupTimeout, PingMeilisearchAddress); err != nil {
		stopServiceProcess(command.Process)
		_ = logFile.Close()
		return MeilisearchStartResult{}, fmt.Errorf("start meilisearch on %s: %w (see %s)", address, err, spec.LogPath)
	}

	pid := command.Process.Pid
	_ = logFile.Close()
	_ = command.Process.Release()

	return MeilisearchStartResult{PID: pid}, nil
}

func MeilisearchServerArgs(spec MeilisearchServerSpec) []string {
	args := []string{
		"--db-path", spec.DataDir,
		"--http-addr", MeilisearchAddress(spec.Port),
	}
	if strings.TrimSpace(spec.MasterKey) != "" {
		args = append(args, "--master-key", strings.TrimSpace(spec.MasterKey))
	}

	return args
}

func StopMeilisearchRuntime(state MeilisearchRuntimeState) error {
	if err := stopServicePID(state.PID); err != nil {
		return err
	}

	deadline := time.Now().Add(managedMeilisearchShutdownTimeout)
	for time.Now().Before(deadline) {
		if !MeilisearchStateIsLive(state, PingMeilisearchAddress) {
			return nil
		}
		time.Sleep(managedMeilisearchPollInterval)
	}
	if !MeilisearchStateIsLive(state, PingMeilisearchAddress) {
		return nil
	}

	return fmt.Errorf("meilisearch did not stop listening on %s within %s", MeilisearchAddress(state.Port), managedMeilisearchShutdownTimeout)
}

func LoadLiveMeilisearchStateForEnvironment(rootDir string, environment config.Environment, ping func(string) bool) (*MeilisearchRuntimeState, error) {
	state, err := LoadLiveMeilisearchState(rootDir, environment.Name, ping)
	if err != nil || state == nil {
		return state, err
	}
	if MeilisearchStateMatchesEnvironment(*state, environment) {
		return state, nil
	}

	return nil, fmt.Errorf(
		"environment %q still has a running meilisearch %s on %s, but the current meilisearch is %s on %s; stop the running meilisearch first",
		environment.Name,
		state.Version,
		MeilisearchAddress(state.Port),
		strings.TrimSpace(environment.Meilisearch.Version),
		MeilisearchAddress(EffectiveMeilisearchPort(environment.Meilisearch)),
	)
}

func LoadLiveMeilisearchState(rootDir, environmentName string, ping func(string) bool) (*MeilisearchRuntimeState, error) {
	path := MeilisearchStatePath(rootDir, environmentName)
	state, err := LoadMeilisearchState(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if MeilisearchStateIsLive(*state, meilisearchPingFunc(ping)) {
		return state, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale meilisearch state: %w", err)
	}

	return nil, nil
}

func LoadMeilisearchState(path string) (*MeilisearchRuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state MeilisearchRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode meilisearch state %s: %w", path, err)
	}

	return &state, nil
}

func WriteMeilisearchState(path string, state MeilisearchRuntimeState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create meilisearch state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode meilisearch state: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write meilisearch state %s: %w", path, err)
	}

	return nil
}

func EffectiveMeilisearchPort(meilisearch *MeilisearchConfig) int {
	if meilisearch == nil || meilisearch.Port == 0 {
		return DefaultMeilisearchPort
	}

	return meilisearch.Port
}

func MeilisearchAddress(port int) string {
	return net.JoinHostPort(MeilisearchListenHost, strconv.Itoa(port))
}

func MeilisearchURL(state MeilisearchRuntimeState) string {
	return MeilisearchURLForConfig(&config.MeilisearchConfig{Port: state.Port})
}

func MeilisearchURLForConfig(meilisearch *config.MeilisearchConfig) string {
	if meilisearch == nil {
		return ""
	}

	return "http://" + MeilisearchAddress(EffectiveMeilisearchPort(meilisearch))
}

func MeilisearchStatePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedMeilisearchStateDirectory, managedMeilisearchStateSubdirectory, environmentName+".json")
}

func MeilisearchDataPath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedMeilisearchDataDirectory, managedMeilisearchDataSubdirectory, environmentName)
}

func MeilisearchLogPath(rootDir, environmentName string) string {
	return filepath.Join(ToolLogRoot(rootDir, toolMeilisearch, environmentName), managedMeilisearchLogFileName)
}

func WaitForMeilisearchAddress(port int, timeout time.Duration, ping func(string) bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if meilisearchPingFunc(ping)(MeilisearchAddress(port)) {
			return nil
		}
		time.Sleep(managedMeilisearchPollInterval)
	}

	return fmt.Errorf("server did not start listening within %s", timeout)
}

func MeilisearchStateMatchesEnvironment(state MeilisearchRuntimeState, environment config.Environment) bool {
	if environment.Meilisearch == nil {
		return false
	}

	return strings.TrimSpace(state.Version) == strings.TrimSpace(environment.Meilisearch.Version) &&
		state.Port == EffectiveMeilisearchPort(environment.Meilisearch) &&
		state.AuthEnabled == (strings.TrimSpace(environment.Meilisearch.MasterKey) != "") &&
		strings.TrimSpace(state.MasterKeyHash) == MeilisearchMasterKeyHash(environment.Meilisearch.MasterKey)
}

func MeilisearchStateIsLive(state MeilisearchRuntimeState, ping func(string) bool) bool {
	return meilisearchPingFunc(ping)(MeilisearchAddress(state.Port))
}

func PingMeilisearchAddress(address string) bool {
	connection, err := net.DialTimeout("tcp", address, managedMeilisearchPollInterval)
	if err != nil {
		return false
	}
	_ = connection.Close()

	return true
}

func MeilisearchMasterKeyHash(masterKey string) string {
	trimmed := strings.TrimSpace(masterKey)
	if trimmed == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:])
}

func meilisearchPingFunc(ping func(string) bool) func(string) bool {
	if ping == nil {
		return PingMeilisearchAddress
	}

	return ping
}

func (hooks MeilisearchRuntimeHooks) withDefaults() MeilisearchRuntimeHooks {
	if hooks.StartServer == nil {
		hooks.StartServer = StartMeilisearchServer
	}
	if hooks.StopServer == nil {
		hooks.StopServer = StopMeilisearchRuntime
	}
	if hooks.PingAddress == nil {
		hooks.PingAddress = PingMeilisearchAddress
	}
	if hooks.Now == nil {
		hooks.Now = time.Now
	}

	return hooks
}
