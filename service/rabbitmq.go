package service

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"polka/config"
)

const (
	RabbitMQListenHost            = "127.0.0.1"
	DefaultRabbitMQPort           = 5672
	DefaultRabbitMQManagementPort = 15672
	DefaultRabbitMQUsername       = "guest"
	DefaultRabbitMQPassword       = "guest"
	rabbitMQStartupTimeout        = 30 * time.Second
	rabbitMQShutdownTimeout       = 10 * time.Second
)

// RabbitMQRuntimeState is the non-secret persisted state of one broker.
type RabbitMQRuntimeState struct {
	EnvironmentName string    `json:"environment"`
	Version         string    `json:"version"`
	Port            int       `json:"port"`
	ManagementPort  int       `json:"management_port"`
	Username        string    `json:"username"`
	PasswordHash    string    `json:"password_hash"`
	PID             int       `json:"pid"`
	Target          string    `json:"target"`
	ErlangHome      string    `json:"erlang_home"`
	DataDir         string    `json:"data_dir"`
	RunDir          string    `json:"run_dir"`
	LogPath         string    `json:"log_path"`
	StartedAt       time.Time `json:"started_at"`
}

// RabbitMQServerSpec describes a project-local broker launch.
type RabbitMQServerSpec struct {
	EnvironmentName string
	Version         string
	Target          string
	ControlTarget   string
	ErlangHome      string
	DataDir         string
	RunDir          string
	LogPath         string
	Port            int
	ManagementPort  int
	Username        string
	Password        string
	Env             []string
}

type RabbitMQStartResult struct{ PID int }

type RabbitMQRuntimeHooks struct {
	StartServer func(RabbitMQServerSpec) (RabbitMQStartResult, error)
	StopServer  func(RabbitMQRuntimeState) error
	PingAddress func(string) bool
	Now         func() time.Time
}

// EnsureManagedRabbitMQStarted starts or reuses the configured broker.
func EnsureManagedRabbitMQStarted(ctx Context, hooks RabbitMQRuntimeHooks) (RabbitMQRuntimeState, bool, error) {
	hooks = hooks.withDefaults()
	configured := ctx.Environment.RabbitMQ
	if configured == nil || strings.TrimSpace(configured.Version) == "" {
		return RabbitMQRuntimeState{}, true, nil
	}
	state, err := LoadLiveRabbitMQStateForEnvironment(ctx.RootDir, ctx.Environment, hooks.PingAddress)
	if err != nil {
		return RabbitMQRuntimeState{}, false, err
	}
	if state != nil {
		return *state, true, nil
	}
	spec, err := BuildRabbitMQServerSpec(ctx)
	if err != nil {
		return RabbitMQRuntimeState{}, false, err
	}
	result, err := hooks.StartServer(spec)
	if err != nil {
		return RabbitMQRuntimeState{}, false, err
	}
	stateValue := RabbitMQRuntimeState{
		EnvironmentName: ctx.Environment.Name, Version: spec.Version,
		Port: spec.Port, ManagementPort: spec.ManagementPort, Username: spec.Username,
		PasswordHash: RabbitMQPasswordHash(spec.Password), PID: result.PID,
		Target: spec.Target, ErlangHome: spec.ErlangHome, DataDir: spec.DataDir,
		RunDir: spec.RunDir, LogPath: spec.LogPath, StartedAt: hooks.Now().UTC(),
	}
	if err := WriteRabbitMQState(RabbitMQStatePath(ctx.RootDir, ctx.Environment.Name), stateValue); err != nil {
		_ = hooks.StopServer(stateValue)
		return RabbitMQRuntimeState{}, false, err
	}
	return stateValue, false, nil
}

// StopManagedRabbitMQ gracefully stops the active environment's broker.
func StopManagedRabbitMQ(ctx Context, hooks RabbitMQRuntimeHooks) (RabbitMQRuntimeState, bool, error) {
	hooks = hooks.withDefaults()
	state, err := LoadLiveRabbitMQState(ctx.RootDir, ctx.Environment.Name, hooks.PingAddress)
	if err != nil || state == nil {
		return RabbitMQRuntimeState{}, state == nil, err
	}
	if err := hooks.StopServer(*state); err != nil {
		return RabbitMQRuntimeState{}, false, err
	}
	if err := os.Remove(RabbitMQStatePath(ctx.RootDir, ctx.Environment.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return RabbitMQRuntimeState{}, false, fmt.Errorf("remove rabbitmq state: %w", err)
	}
	return *state, false, nil
}

// BuildRabbitMQServerSpec resolves RabbitMQ and its managed Erlang dependency.
func BuildRabbitMQServerSpec(ctx Context) (RabbitMQServerSpec, error) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return RabbitMQServerSpec{}, fmt.Errorf("rabbitmq is only supported on windows/amd64")
	}
	rabbitMQ := ctx.Environment.RabbitMQ
	if rabbitMQ == nil || strings.TrimSpace(rabbitMQ.Version) == "" {
		return RabbitMQServerSpec{}, fmt.Errorf("environment %q does not define rabbitmq", ctx.Environment.Name)
	}
	target, err := ctx.resolveInstalledTool(toolRabbitMQ, rabbitMQ.Version)
	if err != nil {
		return RabbitMQServerSpec{}, err
	}
	erl, err := ctx.resolveInstalledTool(toolErlang, "27")
	if err != nil {
		return RabbitMQServerSpec{}, fmt.Errorf("resolve rabbitmq erlang dependency: %w", err)
	}
	env, err := ctx.runtimeEnv()
	if err != nil {
		return RabbitMQServerSpec{}, err
	}
	return RabbitMQServerSpec{
		EnvironmentName: ctx.Environment.Name, Version: strings.TrimSpace(rabbitMQ.Version),
		Target: target, ControlTarget: filepath.Join(filepath.Dir(target), "rabbitmqctl.bat"),
		ErlangHome: filepath.Dir(filepath.Dir(erl)), DataDir: RabbitMQDataPath(ctx.RootDir, ctx.Environment.Name),
		RunDir: RabbitMQRunPath(ctx.RootDir, ctx.Environment.Name), LogPath: RabbitMQLogPath(ctx.RootDir, ctx.Environment.Name),
		Port: EffectiveRabbitMQPort(rabbitMQ), ManagementPort: EffectiveRabbitMQManagementPort(rabbitMQ),
		Username: EffectiveRabbitMQUsername(rabbitMQ), Password: EffectiveRabbitMQPassword(rabbitMQ), Env: env,
	}, nil
}

// StartRabbitMQServer writes isolated configuration and launches the broker.
func StartRabbitMQServer(spec RabbitMQServerSpec) (RabbitMQStartResult, error) {
	if err := prepareRabbitMQRuntime(spec); err != nil {
		return RabbitMQStartResult{}, err
	}
	command, err := prepareServiceCommand(spec.Target, nil)
	if err != nil {
		return RabbitMQStartResult{}, err
	}
	logFile, err := openServiceLog(spec.LogPath)
	if err != nil {
		return RabbitMQStartResult{}, err
	}
	defer logFile.Close()
	command.Stdout, command.Stderr = logFile, logFile
	command.Env = RabbitMQRuntimeEnvironment(spec)
	if err := command.Start(); err != nil {
		return RabbitMQStartResult{}, fmt.Errorf("start rabbitmq: %w", err)
	}
	pid := command.Process.Pid
	if err := WaitForRabbitMQAddress(spec.Port, rabbitMQStartupTimeout, PingRabbitMQAddress); err != nil {
		stopServiceProcess(command.Process)
		return RabbitMQStartResult{}, fmt.Errorf("start rabbitmq on %s: %w (see %s)", RabbitMQAddress(spec.Port), err, spec.LogPath)
	}
	if err := reconcileRabbitMQCredentials(spec); err != nil {
		stopServiceProcess(command.Process)
		return RabbitMQStartResult{}, err
	}
	if data, readErr := os.ReadFile(filepath.Join(spec.RunDir, "rabbitmq.pid")); readErr == nil {
		if brokerPID, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && brokerPID > 0 {
			pid = brokerPID
		}
	}
	return RabbitMQStartResult{PID: pid}, nil
}

func prepareRabbitMQRuntime(spec RabbitMQServerSpec) error {
	if err := os.MkdirAll(spec.DataDir, 0o755); err != nil {
		return fmt.Errorf("create rabbitmq data directory: %w", err)
	}
	if err := os.MkdirAll(spec.RunDir, 0o755); err != nil {
		return fmt.Errorf("create rabbitmq runtime directory: %w", err)
	}
	cookiePath := filepath.Join(spec.RunDir, ".erlang.cookie")
	if _, err := os.Stat(cookiePath); errors.Is(err, os.ErrNotExist) {
		value := make([]byte, 32)
		if _, err := rand.Read(value); err != nil {
			return fmt.Errorf("generate rabbitmq erlang cookie: %w", err)
		}
		if err := os.WriteFile(cookiePath, []byte(hex.EncodeToString(value)), 0o600); err != nil {
			return fmt.Errorf("write rabbitmq erlang cookie: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("stat rabbitmq erlang cookie: %w", err)
	}
	configuration := fmt.Sprintf("listeners.tcp.1 = %s:%d\nmanagement.tcp.ip = %s\nmanagement.tcp.port = %d\ndefault_user_tags.administrator = true\n", RabbitMQListenHost, spec.Port, RabbitMQListenHost, spec.ManagementPort)
	if err := os.WriteFile(filepath.Join(spec.RunDir, "rabbitmq.conf"), []byte(configuration), 0o600); err != nil {
		return fmt.Errorf("write rabbitmq configuration: %w", err)
	}
	if err := os.WriteFile(filepath.Join(spec.RunDir, "enabled_plugins"), []byte("[rabbitmq_management].\n"), 0o644); err != nil {
		return fmt.Errorf("write rabbitmq enabled plugins: %w", err)
	}
	return nil
}

// PrepareRabbitMQDispatchEnvironment prepares the active broker identity for CLI tools.
func PrepareRabbitMQDispatchEnvironment(ctx Context) ([]string, error) {
	spec, err := BuildRabbitMQServerSpec(ctx)
	if err != nil {
		return nil, err
	}
	if err := prepareRabbitMQRuntime(spec); err != nil {
		return nil, err
	}
	return RabbitMQRuntimeEnvironment(spec), nil
}

// RabbitMQRuntimeEnvironment isolates node, data, logs, configuration and cookie.
func RabbitMQRuntimeEnvironment(spec RabbitMQServerSpec) []string {
	env := append([]string(nil), spec.Env...)
	nodeName := "polka_" + sanitizeRabbitMQNodeName(spec.EnvironmentName) + "@localhost"
	values := map[string]string{
		"ERLANG_HOME": spec.ErlangHome, "RABBITMQ_NODENAME": nodeName,
		"RABBITMQ_MNESIA_BASE": spec.DataDir, "RABBITMQ_LOGS": spec.LogPath,
		"RABBITMQ_CONFIG_FILE":          filepath.Join(spec.RunDir, "rabbitmq"),
		"RABBITMQ_ENABLED_PLUGINS_FILE": filepath.Join(spec.RunDir, "enabled_plugins"),
		"RABBITMQ_PID_FILE":             filepath.Join(spec.RunDir, "rabbitmq.pid"),
		"RABBITMQ_ERLANG_COOKIE":        readRabbitMQCookie(filepath.Join(spec.RunDir, ".erlang.cookie")),
	}
	if spec.Username != "" {
		values["RABBITMQ_DEFAULT_USER"] = spec.Username
		values["RABBITMQ_DEFAULT_PASS"] = spec.Password
	}
	for key, value := range values {
		env = setEnvironmentValue(env, key, value)
	}
	path := environmentValue(env, "PATH")
	env = setEnvironmentValue(env, "PATH", filepath.Join(spec.ErlangHome, "bin")+string(os.PathListSeparator)+path)
	return env
}

func reconcileRabbitMQCredentials(spec RabbitMQServerSpec) error {
	env := RabbitMQRuntimeEnvironment(spec)
	var output string
	var err error
	deadline := time.Now().Add(rabbitMQStartupTimeout)
	for time.Now().Before(deadline) {
		output, err = runRabbitMQControl(spec.ControlTarget, env, nil, "list_users", "-q")
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("wait for rabbitmq control plane: %w", err)
	}
	exists := false
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) > 0 && fields[0] == spec.Username {
			exists = true
			break
		}
	}
	command := "add_user"
	if exists {
		command = "change_password"
	}
	if _, err := runRabbitMQControl(spec.ControlTarget, env, strings.NewReader(spec.Password+"\n"), command, spec.Username); err != nil {
		return fmt.Errorf("configure rabbitmq credentials: %w", err)
	}
	if _, err := runRabbitMQControl(spec.ControlTarget, env, nil, "set_user_tags", spec.Username, "administrator"); err != nil {
		return err
	}
	_, err = runRabbitMQControl(spec.ControlTarget, env, nil, "set_permissions", "-p", "/", spec.Username, ".*", ".*", ".*")
	return err
}

func runRabbitMQControl(target string, env []string, stdin *strings.Reader, args ...string) (string, error) {
	command, err := prepareServiceCommand(target, args)
	if err != nil {
		return "", err
	}
	command.Env = env
	if stdin != nil {
		command.Stdin = stdin
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("rabbitmqctl %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// StopRabbitMQRuntime requests shutdown and falls back to process termination.
func StopRabbitMQRuntime(state RabbitMQRuntimeState) error {
	spec := RabbitMQServerSpec{EnvironmentName: state.EnvironmentName, Target: state.Target, ControlTarget: filepath.Join(filepath.Dir(state.Target), "rabbitmqctl.bat"), ErlangHome: state.ErlangHome, DataDir: state.DataDir, RunDir: state.RunDir, LogPath: state.LogPath, Port: state.Port, ManagementPort: state.ManagementPort, Env: os.Environ()}
	_, _ = runRabbitMQControl(spec.ControlTarget, RabbitMQRuntimeEnvironment(spec), nil, "shutdown")
	deadline := time.Now().Add(rabbitMQShutdownTimeout)
	for time.Now().Before(deadline) {
		if !PingRabbitMQAddress(RabbitMQAddress(state.Port)) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return stopServicePID(state.PID)
}

// EffectiveRabbitMQPort returns the configured or default AMQP port.
func EffectiveRabbitMQPort(value *config.RabbitMQConfig) int {
	if value == nil || value.Port == 0 {
		return DefaultRabbitMQPort
	}
	return value.Port
}

// EffectiveRabbitMQManagementPort returns the configured or default UI port.
func EffectiveRabbitMQManagementPort(value *config.RabbitMQConfig) int {
	if value == nil || value.ManagementPort == 0 {
		return DefaultRabbitMQManagementPort
	}
	return value.ManagementPort
}

// EffectiveRabbitMQUsername returns the configured or default local user.
func EffectiveRabbitMQUsername(value *config.RabbitMQConfig) string {
	if value == nil || strings.TrimSpace(value.Username) == "" {
		return DefaultRabbitMQUsername
	}
	return strings.TrimSpace(value.Username)
}

// EffectiveRabbitMQPassword returns the configured or default local password.
func EffectiveRabbitMQPassword(value *config.RabbitMQConfig) string {
	if value == nil || strings.TrimSpace(value.Password) == "" {
		return DefaultRabbitMQPassword
	}
	return strings.TrimSpace(value.Password)
}

// RabbitMQAddress returns the loopback AMQP listener address.
func RabbitMQAddress(port int) string {
	return net.JoinHostPort(RabbitMQListenHost, strconv.Itoa(port))
}

// RabbitMQURLForConfig returns the credential-free AMQP URL used in output.
func RabbitMQURLForConfig(value *config.RabbitMQConfig) string {
	return "amqp://" + RabbitMQAddress(EffectiveRabbitMQPort(value))
}

// RabbitMQManagementURLForConfig returns the local management UI URL.
func RabbitMQManagementURLForConfig(value *config.RabbitMQConfig) string {
	return "http://" + RabbitMQAddress(EffectiveRabbitMQManagementPort(value))
}

// RabbitMQStatePath returns the persisted runtime-state path.
func RabbitMQStatePath(root, environment string) string {
	return filepath.Join(root, "run", "rabbitmq", environment+".json")
}

// RabbitMQRunPath returns the environment-specific runtime directory.
func RabbitMQRunPath(root, environment string) string {
	return filepath.Join(root, "run", "rabbitmq", environment)
}

// RabbitMQDataPath returns the environment-specific durable broker directory.
func RabbitMQDataPath(root, environment string) string {
	return filepath.Join(root, "data", "rabbitmq", environment)
}

// RabbitMQLogPath returns the manifest-declared broker log path.
func RabbitMQLogPath(root, environment string) string {
	return filepath.Join(RabbitMQRunPath(root, environment), "rabbitmq.log")
}

// RabbitMQPasswordHash fingerprints a password without persisting plaintext.
func RabbitMQPasswordHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

// LoadRabbitMQState loads runtime state, treating a missing file as stopped.
func LoadRabbitMQState(path string) (*RabbitMQRuntimeState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read rabbitmq state: %w", err)
	}
	var state RabbitMQRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode rabbitmq state: %w", err)
	}
	return &state, nil
}

// WriteRabbitMQState persists JSON state with restrictive permissions.
func WriteRabbitMQState(path string, state RabbitMQRuntimeState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write rabbitmq state: %w", err)
	}
	return nil
}

// LoadLiveRabbitMQState returns live state and removes stale state files.
func LoadLiveRabbitMQState(root, environment string, ping func(string) bool) (*RabbitMQRuntimeState, error) {
	state, err := LoadRabbitMQState(RabbitMQStatePath(root, environment))
	if err != nil || state == nil {
		return state, err
	}
	if rabbitMQPing(ping)(RabbitMQAddress(state.Port)) {
		return state, nil
	}
	if err := os.Remove(RabbitMQStatePath(root, environment)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return nil, nil
}

// LoadLiveRabbitMQStateForEnvironment also rejects configuration drift.
func LoadLiveRabbitMQStateForEnvironment(root string, environment config.Environment, ping func(string) bool) (*RabbitMQRuntimeState, error) {
	state, err := LoadLiveRabbitMQState(root, environment.Name, ping)
	if err != nil || state == nil {
		return state, err
	}
	if RabbitMQStateMatchesEnvironment(*state, environment) {
		return state, nil
	}
	return nil, fmt.Errorf("rabbitmq for environment %q is already running with different settings", environment.Name)
}

// RabbitMQStateMatchesEnvironment compares every launch-affecting setting.
func RabbitMQStateMatchesEnvironment(state RabbitMQRuntimeState, environment config.Environment) bool {
	value := environment.RabbitMQ
	return value != nil && strings.TrimSpace(state.Version) == strings.TrimSpace(value.Version) && state.Port == EffectiveRabbitMQPort(value) && state.ManagementPort == EffectiveRabbitMQManagementPort(value) && state.Username == EffectiveRabbitMQUsername(value) && state.PasswordHash == RabbitMQPasswordHash(EffectiveRabbitMQPassword(value))
}

// PingRabbitMQAddress reports whether the AMQP TCP listener accepts connections.
func PingRabbitMQAddress(address string) bool {
	connection, err := net.DialTimeout("tcp", address, 150*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

// WaitForRabbitMQAddress polls the AMQP listener until timeout.
func WaitForRabbitMQAddress(port int, timeout time.Duration, ping func(string) bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if rabbitMQPing(ping)(RabbitMQAddress(port)) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", RabbitMQAddress(port))
}
func rabbitMQPing(ping func(string) bool) func(string) bool {
	if ping == nil {
		return PingRabbitMQAddress
	}
	return ping
}
func sanitizeRabbitMQNodeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' {
			result.WriteRune(char)
		} else {
			result.WriteByte('_')
		}
	}
	if result.Len() == 0 {
		return "default"
	}
	return result.String()
}
func readRabbitMQCookie(path string) string {
	data, _ := os.ReadFile(path)
	return strings.TrimSpace(string(data))
}
func environmentValue(env []string, key string) string {
	prefix := strings.ToUpper(key) + "="
	for _, item := range env {
		if strings.HasPrefix(strings.ToUpper(item), prefix) {
			return strings.SplitN(item, "=", 2)[1]
		}
	}
	return ""
}
func setEnvironmentValue(env []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	for index, item := range env {
		if strings.HasPrefix(strings.ToUpper(item), prefix) {
			env[index] = key + "=" + value
			return env
		}
	}
	return append(env, key+"="+value)
}
func (hooks RabbitMQRuntimeHooks) withDefaults() RabbitMQRuntimeHooks {
	if hooks.StartServer == nil {
		hooks.StartServer = StartRabbitMQServer
	}
	if hooks.StopServer == nil {
		hooks.StopServer = StopRabbitMQRuntime
	}
	if hooks.PingAddress == nil {
		hooks.PingAddress = PingRabbitMQAddress
	}
	if hooks.Now == nil {
		hooks.Now = time.Now
	}
	return hooks
}
