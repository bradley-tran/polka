package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"polka/backend"
)

const (
	managedMailpitStateDirectory    = "run"
	managedMailpitStateSubdirectory = "mailpit"
	managedMailpitLogFileName       = "mailpit.log"
	managedMailpitPollInterval      = 100 * time.Millisecond
	managedMailpitStartupTimeout    = 5 * time.Second
	managedMailpitShutdownTimeout   = 5 * time.Second
)

var (
	startMailpitServerFunc = startMailpitServer
	stopMailpitRuntimeFunc = stopMailpitRuntime
	pingMailpitAddressFunc = pingMailpitAddress
	mailpitNowFunc         = time.Now
)

type mailpitRuntimeState struct {
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

type mailpitServerSpec struct {
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

type mailpitStartResult struct {
	PID int
}

func ensureManagedMailpitStarted(store backend.Store, environment backend.Environment) (mailpitRuntimeState, bool, error) {
	if environment.Mailpit == nil || strings.TrimSpace(environment.Mailpit.Version) == "" {
		return mailpitRuntimeState{}, true, nil
	}

	state, err := loadLiveMailpitStateForEnvironment(store.RootDir, environment)
	if err != nil {
		return mailpitRuntimeState{}, false, err
	}
	if state != nil {
		return *state, true, nil
	}

	spec, err := buildMailpitServerSpec(store, environment)
	if err != nil {
		return mailpitRuntimeState{}, false, err
	}
	result, err := startMailpitServerFunc(spec)
	if err != nil {
		return mailpitRuntimeState{}, false, err
	}

	startedState := mailpitRuntimeState{
		EnvironmentName: environment.Name,
		Version:         spec.Version,
		SMTPPort:        spec.SMTPPort,
		UIPort:          spec.UIPort,
		UIScheme:        mailpitUIScheme(spec.HTTPS),
		PID:             result.PID,
		LogPath:         spec.LogPath,
		TLSCertPath:     spec.TLSCertPath,
		TLSKeyPath:      spec.TLSKeyPath,
		StartedAt:       mailpitNowFunc().UTC(),
	}
	if err := writeMailpitState(mailpitStatePath(store.RootDir, environment.Name), startedState); err != nil {
		_ = stopMailpitRuntimeFunc(startedState)
		return mailpitRuntimeState{}, false, err
	}

	return startedState, false, nil
}

func stopManagedMailpit(store backend.Store, environmentName string) (mailpitRuntimeState, bool, error) {
	state, err := loadLiveMailpitState(store.RootDir, environmentName)
	if err != nil {
		return mailpitRuntimeState{}, false, err
	}
	if state == nil {
		return mailpitRuntimeState{}, true, nil
	}
	if err := stopMailpitRuntimeFunc(*state); err != nil {
		return mailpitRuntimeState{}, false, err
	}
	if err := os.Remove(mailpitStatePath(store.RootDir, environmentName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return mailpitRuntimeState{}, false, fmt.Errorf("remove mailpit state: %w", err)
	}

	return *state, false, nil
}

func buildMailpitServerSpec(store backend.Store, environment backend.Environment) (mailpitServerSpec, error) {
	if environment.Mailpit == nil {
		return mailpitServerSpec{}, fmt.Errorf("environment %q does not define mailpit", environment.Name)
	}
	version := strings.TrimSpace(environment.Mailpit.Version)
	if version == "" {
		return mailpitServerSpec{}, fmt.Errorf("environment %q defines mailpit but does not define a version", environment.Name)
	}
	target, err := store.ResolveTool("mailpit")
	if err != nil {
		return mailpitServerSpec{}, err
	}
	env, err := resolveRuntimeEnvironment(runtime.GOOS, os.Environ(), store)
	if err != nil {
		return mailpitServerSpec{}, err
	}
	certPath, keyPath := "", ""
	if environment.Mailpit.HTTPS {
		certPath, keyPath, err = ensureGlobalTLSCertificate(store.CacheDir, backend.MailpitListenHost)
		if err != nil {
			return mailpitServerSpec{}, err
		}
	}

	return mailpitServerSpec{
		EnvironmentName: environment.Name,
		Version:         version,
		Target:          target,
		SMTPPort:        backend.EffectiveMailpitSMTPPort(environment.Mailpit),
		UIPort:          backend.EffectiveMailpitUIPort(environment.Mailpit),
		HTTPS:           environment.Mailpit.HTTPS,
		TLSCertPath:     certPath,
		TLSKeyPath:      keyPath,
		LogPath:         mailpitLogPath(store.RootDir, environment.Name),
		Env:             env,
	}, nil
}

func startMailpitServer(spec mailpitServerSpec) (mailpitStartResult, error) {
	logFile, err := openServeLog(spec.LogPath)
	if err != nil {
		return mailpitStartResult{}, err
	}

	args := mailpitServerArgs(spec)
	command, err := prepareCommand(spec.Target, args)
	if err != nil {
		_ = logFile.Close()
		return mailpitStartResult{}, err
	}
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = spec.Env

	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return mailpitStartResult{}, fmt.Errorf("start mailpit: %w", err)
	}

	if err := waitForMailpitAddresses(spec.SMTPPort, spec.UIPort, managedMailpitStartupTimeout); err != nil {
		stopServeProcess(command.Process)
		_ = logFile.Close()
		return mailpitStartResult{}, fmt.Errorf("start mailpit on smtp %s and ui %s: %w (see %s)", backend.MailpitAddress(spec.SMTPPort), backend.MailpitAddress(spec.UIPort), err, spec.LogPath)
	}

	pid := command.Process.Pid
	_ = logFile.Close()
	_ = command.Process.Release()

	return mailpitStartResult{PID: pid}, nil
}

func mailpitServerArgs(spec mailpitServerSpec) []string {
	args := []string{
		"--smtp", backend.MailpitAddress(spec.SMTPPort),
		"--listen", backend.MailpitAddress(spec.UIPort),
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

func stopMailpitRuntime(state mailpitRuntimeState) error {
	if err := stopServePID(state.PID); err != nil {
		return err
	}

	deadline := time.Now().Add(managedMailpitShutdownTimeout)
	for time.Now().Before(deadline) {
		if !mailpitStateIsLive(state) {
			return nil
		}
		time.Sleep(managedMailpitPollInterval)
	}
	if !mailpitStateIsLive(state) {
		return nil
	}

	return fmt.Errorf("mailpit did not stop listening on %s or %s within %s", backend.MailpitAddress(state.SMTPPort), backend.MailpitAddress(state.UIPort), managedMailpitShutdownTimeout)
}

func loadLiveMailpitStateForEnvironment(rootDir string, environment backend.Environment) (*mailpitRuntimeState, error) {
	state, err := loadLiveMailpitState(rootDir, environment.Name)
	if err != nil || state == nil {
		return state, err
	}
	if mailpitStateMatchesEnvironment(*state, environment) {
		return state, nil
	}

	return nil, fmt.Errorf(
		"environment %q still has a running mailpit %s on smtp %s and ui %s, but the current mailpit is %s; stop the running mailpit first",
		environment.Name,
		state.Version,
		backend.MailpitAddress(state.SMTPPort),
		backend.MailpitAddress(state.UIPort),
		strings.TrimSpace(environment.Mailpit.Version),
	)
}

func loadLiveMailpitState(rootDir, environmentName string) (*mailpitRuntimeState, error) {
	path := mailpitStatePath(rootDir, environmentName)
	state, err := loadMailpitState(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if mailpitStateIsLive(*state) {
		return state, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale mailpit state: %w", err)
	}

	return nil, nil
}

func loadMailpitState(path string) (*mailpitRuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state mailpitRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode mailpit state %s: %w", path, err)
	}

	return &state, nil
}

func writeMailpitState(path string, state mailpitRuntimeState) error {
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

func mailpitStatePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedMailpitStateDirectory, managedMailpitStateSubdirectory, environmentName+".json")
}

func mailpitLogPath(rootDir, environmentName string) string {
	return filepath.Join(backend.ToolLogRoot(rootDir, "mailpit", environmentName), managedMailpitLogFileName)
}

func mailpitUIURL(state mailpitRuntimeState) string {
	return mailpitUIURLForConfig(&backend.MailpitConfig{
		UIPort: state.UIPort,
		HTTPS:  mailpitStateHTTPS(state),
	})
}

func mailpitUIURLForConfig(mailpit *backend.MailpitConfig) string {
	if mailpit == nil {
		return ""
	}

	return mailpitUIScheme(mailpit.HTTPS) + "://" + backend.MailpitAddress(backend.EffectiveMailpitUIPort(mailpit))
}

func mailpitUIScheme(https bool) string {
	if https {
		return "https"
	}

	return "http"
}

func waitForMailpitAddresses(smtpPort, uiPort int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if mailpitPortsAreLive(smtpPort, uiPort) {
			return nil
		}
		time.Sleep(managedMailpitPollInterval)
	}

	return fmt.Errorf("server did not start listening within %s", timeout)
}

func mailpitStateMatchesEnvironment(state mailpitRuntimeState, environment backend.Environment) bool {
	if environment.Mailpit == nil {
		return false
	}

	return strings.TrimSpace(state.Version) == strings.TrimSpace(environment.Mailpit.Version) &&
		state.SMTPPort == backend.EffectiveMailpitSMTPPort(environment.Mailpit) &&
		state.UIPort == backend.EffectiveMailpitUIPort(environment.Mailpit) &&
		mailpitStateHTTPS(state) == environment.Mailpit.HTTPS
}

func mailpitStateHTTPS(state mailpitRuntimeState) bool {
	return strings.EqualFold(strings.TrimSpace(state.UIScheme), "https") || strings.TrimSpace(state.TLSCertPath) != ""
}

func mailpitStateIsLive(state mailpitRuntimeState) bool {
	return mailpitPortsAreLive(state.SMTPPort, state.UIPort)
}

func mailpitPortsAreLive(smtpPort, uiPort int) bool {
	return pingMailpitAddressFunc(backend.MailpitAddress(smtpPort)) && pingMailpitAddressFunc(backend.MailpitAddress(uiPort))
}

func pingMailpitAddress(address string) bool {
	connection, err := net.DialTimeout("tcp", address, managedMailpitPollInterval)
	if err != nil {
		return false
	}
	_ = connection.Close()

	return true
}
