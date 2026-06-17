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
	managedMailpitStateDirectory    = "run"
	managedMailpitStateSubdirectory = "mailpit"
	managedMailpitLogFileName       = "mailpit.log"
	managedMailpitPollInterval      = 100 * time.Millisecond
	managedMailpitStartupTimeout    = 5 * time.Second
	managedMailpitShutdownTimeout   = 5 * time.Second
)

// MailpitRuntimeState is the persisted runtime state for one Mailpit service.
type MailpitRuntimeState struct {
	EnvironmentName string    `json:"environment"`
	Version         string    `json:"version"`
	SMTPPort        int       `json:"smtp_port"`
	UIPort          int       `json:"ui_port"`
	UIScheme        string    `json:"ui_scheme,omitempty"`
	PID             int       `json:"pid"`
	LogPath         string    `json:"log_path,omitempty"`
	TLSCertPath     string    `json:"tls_cert_path,omitempty"`
	TLSKeyPath      string    `json:"tls_key_path,omitempty"`
	StartedAt       time.Time `json:"started_at"`
}

// MailpitServerSpec describes a Mailpit process launch.
type MailpitServerSpec struct {
	EnvironmentName string
	Version         string
	Target          string
	SMTPPort        int
	UIPort          int
	HTTPS           bool
	TLSCertPath     string
	TLSKeyPath      string
	LogPath         string
	Env             []string
}

type MailpitStartResult struct {
	PID int
}

type MailpitRuntimeHooks struct {
	StartServer func(MailpitServerSpec) (MailpitStartResult, error)
	StopServer  func(MailpitRuntimeState) error
	PingAddress func(string) bool
	Now         func() time.Time
}

func EnsureManagedMailpitStarted(ctx Context, hooks MailpitRuntimeHooks) (MailpitRuntimeState, bool, error) {
	hooks = hooks.withDefaults()
	environment := ctx.Environment
	if environment.Mailpit == nil || strings.TrimSpace(environment.Mailpit.Version) == "" {
		return MailpitRuntimeState{}, true, nil
	}

	state, err := LoadLiveMailpitStateForEnvironment(ctx.RootDir, environment, hooks.PingAddress)
	if err != nil {
		return MailpitRuntimeState{}, false, err
	}
	if state != nil {
		return *state, true, nil
	}

	spec, err := BuildMailpitServerSpec(ctx)
	if err != nil {
		return MailpitRuntimeState{}, false, err
	}
	result, err := hooks.StartServer(spec)
	if err != nil {
		return MailpitRuntimeState{}, false, err
	}

	startedState := MailpitRuntimeState{
		EnvironmentName: environment.Name,
		Version:         spec.Version,
		SMTPPort:        spec.SMTPPort,
		UIPort:          spec.UIPort,
		UIScheme:        MailpitUIScheme(spec.HTTPS),
		PID:             result.PID,
		LogPath:         spec.LogPath,
		TLSCertPath:     spec.TLSCertPath,
		TLSKeyPath:      spec.TLSKeyPath,
		StartedAt:       hooks.Now().UTC(),
	}
	if err := WriteMailpitState(MailpitStatePath(ctx.RootDir, environment.Name), startedState); err != nil {
		_ = hooks.StopServer(startedState)
		return MailpitRuntimeState{}, false, err
	}

	return startedState, false, nil
}

func StopManagedMailpit(ctx Context, hooks MailpitRuntimeHooks) (MailpitRuntimeState, bool, error) {
	hooks = hooks.withDefaults()

	state, err := LoadLiveMailpitState(ctx.RootDir, ctx.Environment.Name, hooks.PingAddress)
	if err != nil {
		return MailpitRuntimeState{}, false, err
	}
	if state == nil {
		return MailpitRuntimeState{}, true, nil
	}
	if err := hooks.StopServer(*state); err != nil {
		return MailpitRuntimeState{}, false, err
	}
	if err := os.Remove(MailpitStatePath(ctx.RootDir, ctx.Environment.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return MailpitRuntimeState{}, false, fmt.Errorf("remove mailpit state: %w", err)
	}

	return *state, false, nil
}

func BuildMailpitServerSpec(ctx Context) (MailpitServerSpec, error) {
	environment := ctx.Environment
	if environment.Mailpit == nil {
		return MailpitServerSpec{}, fmt.Errorf("environment %q does not define mailpit", environment.Name)
	}
	version := strings.TrimSpace(environment.Mailpit.Version)
	if version == "" {
		return MailpitServerSpec{}, fmt.Errorf("environment %q defines mailpit but does not define a version", environment.Name)
	}
	target, err := ctx.resolveInstalledTool(toolMailpit, version)
	if err != nil {
		return MailpitServerSpec{}, err
	}
	env, err := ctx.runtimeEnv()
	if err != nil {
		return MailpitServerSpec{}, err
	}
	certPath, keyPath := "", ""
	if environment.Mailpit.HTTPS {
		certPath, keyPath, err = ctx.tlsCertificate(MailpitListenHost)
		if err != nil {
			return MailpitServerSpec{}, err
		}
	}

	return MailpitServerSpec{
		EnvironmentName: environment.Name,
		Version:         version,
		Target:          target,
		SMTPPort:        EffectiveMailpitSMTPPort(environment.Mailpit),
		UIPort:          EffectiveMailpitUIPort(environment.Mailpit),
		HTTPS:           environment.Mailpit.HTTPS,
		TLSCertPath:     certPath,
		TLSKeyPath:      keyPath,
		LogPath:         MailpitLogPath(ctx.RootDir, environment.Name),
		Env:             env,
	}, nil
}

func StartMailpitServer(spec MailpitServerSpec) (MailpitStartResult, error) {
	logFile, err := openServiceLog(spec.LogPath)
	if err != nil {
		return MailpitStartResult{}, err
	}

	args := MailpitServerArgs(spec)
	command, err := prepareServiceCommand(spec.Target, args)
	if err != nil {
		_ = logFile.Close()
		return MailpitStartResult{}, err
	}
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = spec.Env

	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return MailpitStartResult{}, fmt.Errorf("start mailpit: %w", err)
	}

	if err := WaitForMailpitAddresses(spec.SMTPPort, spec.UIPort, managedMailpitStartupTimeout, PingMailpitAddress); err != nil {
		stopServiceProcess(command.Process)
		_ = logFile.Close()
		return MailpitStartResult{}, fmt.Errorf("start mailpit on smtp %s and ui %s: %w (see %s)", MailpitAddress(spec.SMTPPort), MailpitAddress(spec.UIPort), err, spec.LogPath)
	}

	pid := command.Process.Pid
	_ = logFile.Close()
	_ = command.Process.Release()

	return MailpitStartResult{PID: pid}, nil
}

func MailpitServerArgs(spec MailpitServerSpec) []string {
	args := []string{
		"--smtp", MailpitAddress(spec.SMTPPort),
		"--listen", MailpitAddress(spec.UIPort),
	}
	if spec.HTTPS {
		args = append(args,
			"--ui-tls-cert", spec.TLSCertPath,
			"--ui-tls-key", spec.TLSKeyPath,
			"--smtp-tls-cert", spec.TLSCertPath,
			"--smtp-tls-key", spec.TLSKeyPath,
		)
	}

	return args
}

func StopMailpitRuntime(state MailpitRuntimeState) error {
	if err := stopServicePID(state.PID); err != nil {
		return err
	}

	deadline := time.Now().Add(managedMailpitShutdownTimeout)
	for time.Now().Before(deadline) {
		if !MailpitStateIsLive(state, PingMailpitAddress) {
			return nil
		}
		time.Sleep(managedMailpitPollInterval)
	}
	if !MailpitStateIsLive(state, PingMailpitAddress) {
		return nil
	}

	return fmt.Errorf("mailpit did not stop listening on %s or %s within %s", MailpitAddress(state.SMTPPort), MailpitAddress(state.UIPort), managedMailpitShutdownTimeout)
}

func LoadLiveMailpitStateForEnvironment(rootDir string, environment config.Environment, ping func(string) bool) (*MailpitRuntimeState, error) {
	state, err := LoadLiveMailpitState(rootDir, environment.Name, ping)
	if err != nil || state == nil {
		return state, err
	}
	if MailpitStateMatchesEnvironment(*state, environment) {
		return state, nil
	}

	return nil, fmt.Errorf(
		"environment %q still has a running mailpit %s on smtp %s and ui %s, but the current mailpit is %s; stop the running mailpit first",
		environment.Name,
		state.Version,
		MailpitAddress(state.SMTPPort),
		MailpitAddress(state.UIPort),
		strings.TrimSpace(environment.Mailpit.Version),
	)
}

func LoadLiveMailpitState(rootDir, environmentName string, ping func(string) bool) (*MailpitRuntimeState, error) {
	path := MailpitStatePath(rootDir, environmentName)
	state, err := LoadMailpitState(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if MailpitStateIsLive(*state, mailpitPingFunc(ping)) {
		return state, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale mailpit state: %w", err)
	}

	return nil, nil
}

func LoadMailpitState(path string) (*MailpitRuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state MailpitRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode mailpit state %s: %w", path, err)
	}

	return &state, nil
}

func WriteMailpitState(path string, state MailpitRuntimeState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create mailpit state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode mailpit state: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write mailpit state %s: %w", path, err)
	}

	return nil
}

func MailpitStatePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedMailpitStateDirectory, managedMailpitStateSubdirectory, environmentName+".json")
}

func MailpitLogPath(rootDir, environmentName string) string {
	return filepath.Join(ToolLogRoot(rootDir, toolMailpit, environmentName), managedMailpitLogFileName)
}

func MailpitUIURL(state MailpitRuntimeState) string {
	return MailpitUIURLForConfig(&config.MailpitConfig{
		UIPort: state.UIPort,
		HTTPS:  MailpitStateHTTPS(state),
	})
}

func MailpitUIURLForConfig(mailpit *config.MailpitConfig) string {
	if mailpit == nil {
		return ""
	}

	return MailpitUIScheme(mailpit.HTTPS) + "://" + MailpitAddress(EffectiveMailpitUIPort(mailpit))
}

func MailpitUIScheme(https bool) string {
	if https {
		return "https"
	}

	return "http"
}

func WaitForMailpitAddresses(smtpPort, uiPort int, timeout time.Duration, ping func(string) bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if MailpitPortsAreLive(smtpPort, uiPort, ping) {
			return nil
		}
		time.Sleep(managedMailpitPollInterval)
	}

	return fmt.Errorf("server did not start listening within %s", timeout)
}

func MailpitStateMatchesEnvironment(state MailpitRuntimeState, environment config.Environment) bool {
	if environment.Mailpit == nil {
		return false
	}

	return strings.TrimSpace(state.Version) == strings.TrimSpace(environment.Mailpit.Version) &&
		state.SMTPPort == EffectiveMailpitSMTPPort(environment.Mailpit) &&
		state.UIPort == EffectiveMailpitUIPort(environment.Mailpit) &&
		MailpitStateHTTPS(state) == environment.Mailpit.HTTPS
}

func MailpitStateHTTPS(state MailpitRuntimeState) bool {
	return strings.EqualFold(strings.TrimSpace(state.UIScheme), "https") || strings.TrimSpace(state.TLSCertPath) != ""
}

func MailpitStateIsLive(state MailpitRuntimeState, ping func(string) bool) bool {
	return MailpitPortsAreLive(state.SMTPPort, state.UIPort, ping)
}

func MailpitPortsAreLive(smtpPort, uiPort int, ping func(string) bool) bool {
	ping = mailpitPingFunc(ping)
	return ping(MailpitAddress(smtpPort)) && ping(MailpitAddress(uiPort))
}

func PingMailpitAddress(address string) bool {
	connection, err := net.DialTimeout("tcp", address, managedMailpitPollInterval)
	if err != nil {
		return false
	}
	_ = connection.Close()

	return true
}

func mailpitPingFunc(ping func(string) bool) func(string) bool {
	if ping == nil {
		return PingMailpitAddress
	}

	return ping
}

func (hooks MailpitRuntimeHooks) withDefaults() MailpitRuntimeHooks {
	if hooks.StartServer == nil {
		hooks.StartServer = StartMailpitServer
	}
	if hooks.StopServer == nil {
		hooks.StopServer = StopMailpitRuntime
	}
	if hooks.PingAddress == nil {
		hooks.PingAddress = PingMailpitAddress
	}
	if hooks.Now == nil {
		hooks.Now = time.Now
	}

	return hooks
}
