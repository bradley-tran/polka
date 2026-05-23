package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"polka/backend"
)

const (
	dbSubcommandClient = "client"
	dbSubcommandStart  = "start"
	dbSubcommandStop   = "stop"
	dbSubcommandStatus = "status"

	dbListenHost        = "127.0.0.1"
	dbDefaultPort       = 3306
	dbPollInterval      = 200 * time.Millisecond
	dbStartupTimeout    = 10 * time.Second
	dbShutdownTimeout   = 5 * time.Second
	dbStateDirectory    = "run"
	dbStateSubdirectory = "db"
	dbDataDirectory     = "data"
	dbDataSubdirectory  = "db"
)

var (
	initializeDatabaseServerFunc = initializeDatabaseServer
	startDatabaseServerFunc      = startDatabaseServer
	stopDatabaseServerFunc       = stopDatabaseServer
	pingDatabaseAddressFunc      = pingDatabaseAddress
	dbNowFunc                    = time.Now
)

type dbResolvedEnvironment struct {
	Environment backend.Environment
	Database    *backend.DatabaseConfig
}

type dbServerSpec struct {
	EnvironmentName string
	Engine          string
	Version         string
	InstallDir      string
	Target          string
	DataDir         string
	LogPath         string
	Port            int
}

type dbStartResult struct {
	PID int
}

type dbRuntimeState struct {
	EnvironmentName string    `json:"environment"`
	Engine          string    `json:"engine"`
	Version         string    `json:"version"`
	Port            int       `json:"port"`
	PID             int       `json:"pid"`
	DataDir         string    `json:"data_dir"`
	LogPath         string    `json:"log_path"`
	StartedAt       time.Time `json:"started_at"`
}

func newDBCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:                "db [args...]",
		DisableFlagParsing: true,
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
				_, _ = fmt.Fprint(cmd.OutOrStdout(), dbUsage)
				return
			}

			store, err := ctx.store()
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				ctx.exitCode = 1
				return
			}

			ctx.exitCode = runDB(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, args)
		},
	}
	configureHelp(cmd, dbUsage)

	return cmd
}

func runDB(stdout, stderr io.Writer, store backend.Store, args []string) int {
	resolved, err := resolveDBEnvironment(store)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case dbSubcommandClient:
			return runDBClient(stdout, stderr, store, resolved, args[1:])
		case dbSubcommandStart:
			return runDBStart(stdout, stderr, store, resolved, args[1:])
		case dbSubcommandStop:
			return runDBStop(stdout, stderr, store, resolved, args[1:])
		case dbSubcommandStatus:
			return runDBStatus(stdout, stderr, store, resolved, args[1:])
		}
	}

	return runDBClient(stdout, stderr, store, resolved, args)
}

func resolveDBEnvironment(store backend.Store) (dbResolvedEnvironment, error) {
	current, err := store.Current()
	if err != nil {
		return dbResolvedEnvironment{}, err
	}
	if current == nil {
		return dbResolvedEnvironment{}, fmt.Errorf("no active environment selected")
	}
	if current.Database == nil || strings.TrimSpace(current.Database.Engine) == "" {
		return dbResolvedEnvironment{}, fmt.Errorf("environment %q does not define a database engine", current.Name)
	}

	return dbResolvedEnvironment{Environment: *current, Database: current.Database}, nil
}

func runDBClient(stdout, stderr io.Writer, store backend.Store, resolved dbResolvedEnvironment, args []string) int {
	dispatchArgs, err := injectDatabaseConnectionArgs(store.RootDir, resolved, args)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	return runDispatch(stdout, stderr, append([]string{resolved.Database.Engine}, dispatchArgs...), store)
}

func runDBStart(stdout, stderr io.Writer, store backend.Store, resolved dbResolvedEnvironment, args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "error: db start does not accept arguments")
		return 1
	}

	state, alreadyRunning, err := ensureManagedDatabaseStarted(store, resolved)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if alreadyRunning {
		fmt.Fprintf(stdout, "Database for environment %q is already running on %s.\n", resolved.Environment.Name, databaseAddress(state.Port))
		return 0
	}

	fmt.Fprintf(stdout, "Started %s for environment %q on %s.\n", resolved.Database.Engine, resolved.Environment.Name, databaseAddress(state.Port))
	return 0
}

func runDBStop(stdout, stderr io.Writer, store backend.Store, resolved dbResolvedEnvironment, args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "error: db stop does not accept arguments")
		return 1
	}

	statePath := databaseStatePath(store.RootDir, resolved.Environment.Name)
	state, err := loadLiveDatabaseState(store.RootDir, resolved.Environment.Name)
	if state == nil && err == nil {
		fmt.Fprintf(stdout, "Database for environment %q is already stopped.\n", resolved.Environment.Name)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if err := stopDatabaseServerFunc(*state); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "error: remove database state: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Stopped %s for environment %q.\n", state.Engine, resolved.Environment.Name)
	return 0
}

func runDBStatus(stdout, stderr io.Writer, store backend.Store, resolved dbResolvedEnvironment, args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "error: db status does not accept arguments")
		return 1
	}

	state, err := loadLiveDatabaseState(store.RootDir, resolved.Environment.Name)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	address := databaseAddress(effectiveDatabasePort(resolved.Database))
	if state != nil {
		address = databaseAddress(state.Port)
		fmt.Fprintf(stdout, "Database for environment %q is running on %s (pid %d).\n", resolved.Environment.Name, address, state.PID)
		return 0
	}

	fmt.Fprintf(stdout, "Database for environment %q is stopped on %s.\n", resolved.Environment.Name, address)
	return 0
}

func buildDBServerSpec(store backend.Store, resolved dbResolvedEnvironment) (dbServerSpec, error) {
	installDir := filepath.Join(store.EnvsDir, resolved.Database.Engine, resolved.Database.Version)
	target, err := resolveDatabaseServerTarget(store.EnvsDir, resolved.Database.Engine, resolved.Database.Version)
	if err != nil {
		return dbServerSpec{}, err
	}

	return dbServerSpec{
		EnvironmentName: resolved.Environment.Name,
		Engine:          resolved.Database.Engine,
		Version:         resolved.Database.Version,
		InstallDir:      installDir,
		Target:          target,
		DataDir:         databaseDataPath(store.RootDir, resolved.Environment.Name),
		LogPath:         databaseLogPath(store.RootDir, resolved.Environment.Name),
		Port:            effectiveDatabasePort(resolved.Database),
	}, nil
}

func ensureManagedDatabaseStarted(store backend.Store, resolved dbResolvedEnvironment) (dbRuntimeState, bool, error) {
	statePath := databaseStatePath(store.RootDir, resolved.Environment.Name)
	state, err := loadLiveDatabaseState(store.RootDir, resolved.Environment.Name)
	if err != nil {
		return dbRuntimeState{}, false, err
	}
	if state != nil {
		return *state, true, nil
	}

	spec, err := buildDBServerSpec(store, resolved)
	if err != nil {
		return dbRuntimeState{}, false, err
	}
	if err := initializeDatabaseServerFunc(spec); err != nil {
		return dbRuntimeState{}, false, err
	}

	result, err := startDatabaseServerFunc(spec)
	if err != nil {
		return dbRuntimeState{}, false, err
	}

	startedState := dbRuntimeState{
		EnvironmentName: resolved.Environment.Name,
		Engine:          resolved.Database.Engine,
		Version:         resolved.Database.Version,
		Port:            spec.Port,
		PID:             result.PID,
		DataDir:         spec.DataDir,
		LogPath:         spec.LogPath,
		StartedAt:       dbNowFunc().UTC(),
	}
	if err := writeDatabaseState(statePath, startedState); err != nil {
		_ = stopDatabaseServerFunc(startedState)
		return dbRuntimeState{}, false, err
	}

	return startedState, false, nil
}

func injectDatabaseConnectionArgs(rootDir string, resolved dbResolvedEnvironment, args []string) ([]string, error) {
	hasHost, hasPort, hasSocket, hasProtocol, protocol := databaseConnectionOverrides(args)
	if hasSocket {
		return args, nil
	}
	if hasProtocol && protocol != "" && !strings.EqualFold(protocol, "tcp") {
		return args, nil
	}

	state, err := loadLiveDatabaseState(rootDir, resolved.Environment.Name)
	if err != nil {
		return nil, err
	}
	port := effectiveDatabasePort(resolved.Database)
	if state != nil && state.Port != 0 {
		port = state.Port
	}

	injected := make([]string, 0, len(args)+3)
	if !hasProtocol {
		injected = append(injected, "--protocol=tcp")
	}
	if !hasHost {
		injected = append(injected, "--host="+dbListenHost)
	}
	if !hasPort {
		injected = append(injected, "--port="+strconv.Itoa(port))
	}

	return append(injected, args...), nil
}

func databaseConnectionOverrides(args []string) (hasHost, hasPort, hasSocket, hasProtocol bool, protocol string) {
	for index := 0; index < len(args); index++ {
		argument := strings.TrimSpace(args[index])
		switch {
		case argument == "--host" || argument == "-h":
			hasHost = true
			if index+1 < len(args) {
				index++
			}
		case strings.HasPrefix(argument, "--host="):
			hasHost = true
		case strings.HasPrefix(argument, "-h") && len(argument) > 2:
			hasHost = true
		case argument == "--port" || argument == "-P":
			hasPort = true
			if index+1 < len(args) {
				index++
			}
		case strings.HasPrefix(argument, "--port="):
			hasPort = true
		case strings.HasPrefix(argument, "-P") && len(argument) > 2:
			hasPort = true
		case argument == "--socket" || argument == "-S":
			hasSocket = true
			if index+1 < len(args) {
				index++
			}
		case strings.HasPrefix(argument, "--socket="):
			hasSocket = true
		case strings.HasPrefix(argument, "-S") && len(argument) > 2:
			hasSocket = true
		case argument == "--protocol":
			hasProtocol = true
			if index+1 < len(args) {
				protocol = strings.TrimSpace(args[index+1])
				index++
			}
		case strings.HasPrefix(argument, "--protocol="):
			hasProtocol = true
			protocol = strings.TrimSpace(strings.TrimPrefix(argument, "--protocol="))
		}
	}

	return hasHost, hasPort, hasSocket, hasProtocol, protocol
}

func resolveDatabaseServerTarget(envsDir, engine, version string) (string, error) {
	installDir := filepath.Join(envsDir, engine, version)
	for _, candidate := range databaseServerCandidates(installDir, engine) {
		fileInfo, err := os.Stat(candidate)
		if err == nil {
			if fileInfo.IsDir() {
				continue
			}

			return candidate, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat %s: %w", candidate, err)
		}
	}

	return "", fmt.Errorf("%s server version %q is not installed under %s", engine, version, filepath.Join(envsDir, engine, version))
}

func databaseServerCandidates(installDir, engine string) []string {
	if runtime.GOOS == "windows" {
		switch engine {
		case "mysql":
			return []string{
				filepath.Join(installDir, "bin", "mysqld.exe"),
				filepath.Join(installDir, "bin", "mysqld.cmd"),
				filepath.Join(installDir, "bin", "mysqld.bat"),
				filepath.Join(installDir, "mysqld.exe"),
				filepath.Join(installDir, "mysqld.cmd"),
				filepath.Join(installDir, "mysqld.bat"),
			}
		case "mariadb":
			return []string{
				filepath.Join(installDir, "bin", "mariadbd.exe"),
				filepath.Join(installDir, "bin", "mariadbd.cmd"),
				filepath.Join(installDir, "bin", "mariadbd.bat"),
				filepath.Join(installDir, "bin", "mysqld.exe"),
				filepath.Join(installDir, "bin", "mysqld.cmd"),
				filepath.Join(installDir, "bin", "mysqld.bat"),
				filepath.Join(installDir, "mariadbd.exe"),
				filepath.Join(installDir, "mariadbd.cmd"),
				filepath.Join(installDir, "mariadbd.bat"),
				filepath.Join(installDir, "mysqld.exe"),
				filepath.Join(installDir, "mysqld.cmd"),
				filepath.Join(installDir, "mysqld.bat"),
			}
		}
	}

	switch engine {
	case "mysql":
		return []string{
			filepath.Join(installDir, "bin", "mysqld"),
			filepath.Join(installDir, "mysqld"),
		}
	case "mariadb":
		return []string{
			filepath.Join(installDir, "bin", "mariadbd"),
			filepath.Join(installDir, "bin", "mysqld"),
			filepath.Join(installDir, "mariadbd"),
			filepath.Join(installDir, "mysqld"),
		}
	default:
		return nil
	}
}

func initializeDatabaseServer(spec dbServerSpec) error {
	initialized, err := databaseDataInitialized(spec.DataDir)
	if err != nil {
		return err
	}
	if initialized {
		return nil
	}
	if err := os.MkdirAll(spec.DataDir, 0o755); err != nil {
		return fmt.Errorf("create database data directory: %w", err)
	}

	logFile, err := openDatabaseLog(spec.LogPath)
	if err != nil {
		return err
	}
	defer logFile.Close()

	command, err := prepareCommand(spec.Target, databaseInitializeArgs(spec))
	if err != nil {
		return err
	}
	command.Stdout = logFile
	command.Stderr = logFile

	if err := command.Run(); err != nil {
		return fmt.Errorf("initialize %s data directory: %w (see %s)", spec.Engine, err, spec.LogPath)
	}

	return nil
}

func startDatabaseServer(spec dbServerSpec) (dbStartResult, error) {
	logFile, err := openDatabaseLog(spec.LogPath)
	if err != nil {
		return dbStartResult{}, err
	}

	command, err := prepareCommand(spec.Target, databaseStartArgs(spec))
	if err != nil {
		_ = logFile.Close()
		return dbStartResult{}, err
	}
	command.Stdout = logFile
	command.Stderr = logFile

	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return dbStartResult{}, fmt.Errorf("start %s server: %w", spec.Engine, err)
	}

	pid := command.Process.Pid
	deadline := time.Now().Add(dbStartupTimeout)
	address := databaseAddress(spec.Port)
	for time.Now().Before(deadline) {
		if pingDatabaseAddressFunc(address) {
			_ = logFile.Close()
			_ = command.Process.Release()
			return dbStartResult{PID: pid}, nil
		}

		time.Sleep(dbPollInterval)
	}

	if command.Process != nil {
		_ = command.Process.Kill()
		_ = command.Process.Release()
	}
	_ = logFile.Close()

	return dbStartResult{}, fmt.Errorf("%s server did not start listening on %s within %s (see %s)", spec.Engine, address, dbStartupTimeout, spec.LogPath)
}

func stopDatabaseServer(state dbRuntimeState) error {
	address := databaseAddress(state.Port)
	if !pingDatabaseAddressFunc(address) {
		return nil
	}
	if state.PID == 0 {
		return fmt.Errorf("database state for %q does not include a pid", state.EnvironmentName)
	}

	process, err := os.FindProcess(state.PID)
	if err != nil {
		return fmt.Errorf("find database process %d: %w", state.PID, err)
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stop database process %d: %w", state.PID, err)
	}

	deadline := time.Now().Add(dbShutdownTimeout)
	for time.Now().Before(deadline) {
		if !pingDatabaseAddressFunc(address) {
			_ = process.Release()
			return nil
		}

		time.Sleep(dbPollInterval)
	}

	_ = process.Release()
	return nil
}

func databaseInitializeArgs(spec dbServerSpec) []string {
	args := []string{
		"--initialize-insecure",
		"--basedir=" + spec.InstallDir,
		"--datadir=" + spec.DataDir,
	}

	if spec.Engine == "mariadb" {
		args = append(args, "--auth-root-authentication-method=normal")
	}

	return args
}

func databaseStartArgs(spec dbServerSpec) []string {
	args := []string{
		"--basedir=" + spec.InstallDir,
		"--datadir=" + spec.DataDir,
		"--port=" + strconv.Itoa(spec.Port),
		"--bind-address=" + dbListenHost,
		"--log-error=" + spec.LogPath,
	}

	if runtime.GOOS == "windows" {
		args = append(args, "--console")
	}

	return args
}

func databaseDataInitialized(dataDir string) (bool, error) {
	entries, err := os.ReadDir(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read database data directory: %w", err)
	}

	return len(entries) > 0, nil
}

func openDatabaseLog(logPath string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("create database log directory: %w", err)
	}

	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open database log %s: %w", logPath, err)
	}

	return file, nil
}

func effectiveDatabasePort(database *backend.DatabaseConfig) int {
	if database == nil || database.Port == 0 {
		return dbDefaultPort
	}

	return database.Port
}

func databaseAddress(port int) string {
	return net.JoinHostPort(dbListenHost, strconv.Itoa(port))
}

func pingDatabaseAddress(address string) bool {
	connection, err := net.DialTimeout("tcp", address, dbPollInterval)
	if err != nil {
		return false
	}
	_ = connection.Close()

	return true
}

func databaseStatePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, dbStateDirectory, dbStateSubdirectory, environmentName+".json")
}

func databaseLogPath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, dbStateDirectory, dbStateSubdirectory, environmentName+".log")
}

func databaseDataPath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, dbDataDirectory, dbDataSubdirectory, environmentName)
}

func loadLiveDatabaseState(rootDir, environmentName string) (*dbRuntimeState, error) {
	statePath := databaseStatePath(rootDir, environmentName)
	state, err := loadDatabaseState(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pingDatabaseAddressFunc(databaseAddress(state.Port)) {
		return state, nil
	}
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale database state: %w", err)
	}

	return nil, nil
}

func loadDatabaseState(path string) (*dbRuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state dbRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode database state %s: %w", path, err)
	}

	return &state, nil
}

func writeDatabaseState(path string, state dbRuntimeState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create database state directory: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode database state: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write database state %s: %w", path, err)
	}

	return nil
}
