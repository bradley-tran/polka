package service

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"polka/config"
)

const (
	DatabaseListenHost                = "127.0.0.1"
	DefaultDatabasePort               = 3306
	DefaultPostgreSQLPort             = 5432
	managedDatabasePollInterval       = 200 * time.Millisecond
	managedDatabaseStartupTimeout     = 10 * time.Second
	managedDatabaseShutdownTimeout    = 5 * time.Second
	managedDatabaseStateDirectory     = "run"
	managedDatabaseStateSubdirectory  = "db"
	managedDatabaseDataDirectory      = "data"
	managedDatabaseDataSubdirectory   = "db"
	managedDatabaseSecretDirectory    = "secrets"
	managedDatabaseSecretSubdirectory = "db"
	ManagedDatabaseUserName           = "polka"
)

type ResolvedDatabaseEnvironment struct {
	Environment config.Environment
	Database    *config.DatabaseConfig
}

type ManagedDatabaseServerSpec struct {
	EnvironmentName  string
	Engine           string
	Version          string
	InstallDir       string
	Target           string
	InitializeTarget string
	AdminTarget      string
	CreateTarget     string
	DataDir          string
	LogPath          string
	DefaultsFile     string
	BootstrapSQLFile string
	PasswordFile     string
	BootstrapMarker  string
	DatabaseName     string
	Port             int
}

type ManagedDatabaseStartResult struct {
	PID int
}

type ManagedDatabaseCredentials struct {
	EnvironmentName string `json:"environment"`
	Engine          string `json:"engine"`
	Version         string `json:"version"`
	DatabaseName    string `json:"database,omitempty"`
	User            string `json:"user"`
	Password        string `json:"password"`
	Port            int    `json:"port"`
}

type ManagedDatabaseRuntimeState struct {
	EnvironmentName string    `json:"environment"`
	Engine          string    `json:"engine"`
	Version         string    `json:"version"`
	Port            int       `json:"port"`
	PID             int       `json:"pid"`
	DataDir         string    `json:"data_dir"`
	LogPath         string    `json:"log_path"`
	AdminTarget     string    `json:"admin_target,omitempty"`
	DefaultsFile    string    `json:"defaults_file,omitempty"`
	StartedAt       time.Time `json:"started_at"`
}

type DatabaseRuntimeHooks struct {
	InitializeServer func(ManagedDatabaseServerSpec) error
	StartServer      func(ManagedDatabaseServerSpec) (ManagedDatabaseStartResult, error)
	StopServer       func(ManagedDatabaseRuntimeState) error
	PingAddress      func(string) bool
	Now              func() time.Time
}

// ResolveDatabaseEnvironment validates and returns the managed database selected
// by an already-resolved environment.
func ResolveDatabaseEnvironment(environment config.Environment) (ResolvedDatabaseEnvironment, error) {
	if environment.Database == nil || strings.TrimSpace(environment.Database.Engine) == "" {
		return ResolvedDatabaseEnvironment{}, fmt.Errorf("environment %q does not define a database engine", environment.Name)
	}

	return ResolvedDatabaseEnvironment{Environment: environment, Database: environment.Database}, nil
}

func EnsureManagedDatabaseStarted(ctx Context, resolved ResolvedDatabaseEnvironment, hooks DatabaseRuntimeHooks) (ManagedDatabaseRuntimeState, bool, error) {
	hooks = hooks.withDefaults()

	statePath := DatabaseStatePath(ctx.RootDir, resolved.Environment.Name)
	state, err := LoadLiveManagedDatabaseStateForResolved(ctx.RootDir, resolved, hooks.PingAddress)
	if err != nil {
		return ManagedDatabaseRuntimeState{}, false, err
	}
	if state != nil {
		return *state, true, nil
	}

	spec, err := buildManagedDatabaseServerSpec(ctx, resolved)
	if err != nil {
		return ManagedDatabaseRuntimeState{}, false, err
	}
	if err := hooks.InitializeServer(spec); err != nil {
		return ManagedDatabaseRuntimeState{}, false, err
	}

	result, err := hooks.StartServer(spec)
	if err != nil {
		return ManagedDatabaseRuntimeState{}, false, err
	}

	startedState := ManagedDatabaseRuntimeState{
		EnvironmentName: resolved.Environment.Name,
		Engine:          resolved.Database.Engine,
		Version:         resolved.Database.Version,
		Port:            spec.Port,
		PID:             result.PID,
		DataDir:         spec.DataDir,
		LogPath:         spec.LogPath,
		AdminTarget:     spec.AdminTarget,
		DefaultsFile:    spec.DefaultsFile,
		StartedAt:       hooks.Now().UTC(),
	}
	if err := WriteManagedDatabaseState(statePath, startedState); err != nil {
		_ = hooks.StopServer(startedState)
		return ManagedDatabaseRuntimeState{}, false, err
	}

	return startedState, false, nil
}

func StopManagedDatabase(ctx Context, resolved ResolvedDatabaseEnvironment, hooks DatabaseRuntimeHooks) (ManagedDatabaseRuntimeState, bool, error) {
	hooks = hooks.withDefaults()

	statePath := DatabaseStatePath(ctx.RootDir, resolved.Environment.Name)
	state, err := LoadLiveManagedDatabaseState(ctx.RootDir, resolved.Environment.Name, hooks.PingAddress)
	if state == nil && err == nil {
		return ManagedDatabaseRuntimeState{}, true, nil
	}
	if err != nil {
		return ManagedDatabaseRuntimeState{}, false, err
	}
	if strings.TrimSpace(state.AdminTarget) == "" || strings.TrimSpace(state.DefaultsFile) == "" {
		spec, specErr := buildManagedDatabaseServerSpec(ctx, resolved)
		if specErr != nil {
			return ManagedDatabaseRuntimeState{}, false, specErr
		}
		if strings.TrimSpace(state.AdminTarget) == "" {
			state.AdminTarget = spec.AdminTarget
		}
		if strings.TrimSpace(state.DefaultsFile) == "" {
			state.DefaultsFile = spec.DefaultsFile
		}
	}

	if err := hooks.StopServer(*state); err != nil {
		return ManagedDatabaseRuntimeState{}, false, err
	}
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ManagedDatabaseRuntimeState{}, false, fmt.Errorf("remove database state: %w", err)
	}

	return *state, false, nil
}

// StopManagedDatabaseForEnvironment stops a live database state without
// requiring the current config to still define the database service.
func StopManagedDatabaseForEnvironment(ctx Context, hooks DatabaseRuntimeHooks) (ManagedDatabaseRuntimeState, bool, error) {
	hooks = hooks.withDefaults()

	statePath := DatabaseStatePath(ctx.RootDir, ctx.Environment.Name)
	state, err := LoadLiveManagedDatabaseState(ctx.RootDir, ctx.Environment.Name, hooks.PingAddress)
	if state == nil && err == nil {
		return ManagedDatabaseRuntimeState{}, true, nil
	}
	if err != nil {
		return ManagedDatabaseRuntimeState{}, false, err
	}

	if strings.TrimSpace(state.AdminTarget) == "" || strings.TrimSpace(state.DefaultsFile) == "" {
		if ctx.Environment.Database == nil || strings.TrimSpace(ctx.Environment.Database.Engine) == "" {
			return ManagedDatabaseRuntimeState{}, false, fmt.Errorf("database state for %q is missing shutdown fields and the current environment no longer defines a database", ctx.Environment.Name)
		}
		spec, specErr := buildManagedDatabaseServerSpec(ctx, ResolvedDatabaseEnvironment{
			Environment: ctx.Environment,
			Database:    ctx.Environment.Database,
		})
		if specErr != nil {
			return ManagedDatabaseRuntimeState{}, false, specErr
		}
		if strings.TrimSpace(state.AdminTarget) == "" {
			state.AdminTarget = spec.AdminTarget
		}
		if strings.TrimSpace(state.DefaultsFile) == "" {
			state.DefaultsFile = spec.DefaultsFile
		}
	}

	if err := hooks.StopServer(*state); err != nil {
		return ManagedDatabaseRuntimeState{}, false, err
	}
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ManagedDatabaseRuntimeState{}, false, fmt.Errorf("remove database state: %w", err)
	}

	return *state, false, nil
}

func EnsureManagedDatabaseCredentialAssets(rootDir string, resolved ResolvedDatabaseEnvironment) (ManagedDatabaseCredentials, error) {
	path := DatabaseCredentialStatePath(rootDir, resolved.Environment.Name)
	credentials, err := LoadManagedDatabaseCredentials(path)
	if errors.Is(err, os.ErrNotExist) {
		password, passwordErr := generateDatabasePassword()
		if passwordErr != nil {
			return ManagedDatabaseCredentials{}, passwordErr
		}
		credentials = ManagedDatabaseCredentials{
			EnvironmentName: resolved.Environment.Name,
			Engine:          resolved.Database.Engine,
			Version:         resolved.Database.Version,
			DatabaseName:    managedDatabaseName(resolved),
			User:            ManagedDatabaseUserName,
			Password:        password,
			Port:            EffectiveDatabasePort(resolved.Database),
		}
	} else if err != nil {
		return ManagedDatabaseCredentials{}, err
	}

	if strings.TrimSpace(credentials.User) == "" {
		credentials.User = ManagedDatabaseUserName
	}
	if strings.TrimSpace(credentials.Password) == "" {
		password, passwordErr := generateDatabasePassword()
		if passwordErr != nil {
			return ManagedDatabaseCredentials{}, passwordErr
		}
		credentials.Password = password
	}
	credentials.EnvironmentName = resolved.Environment.Name
	credentials.Engine = resolved.Database.Engine
	credentials.Version = resolved.Database.Version
	credentials.DatabaseName = managedDatabaseName(resolved)
	credentials.Port = EffectiveDatabasePort(resolved.Database)

	if err := writeManagedDatabaseCredentials(path, credentials); err != nil {
		return ManagedDatabaseCredentials{}, err
	}
	if strings.EqualFold(resolved.Database.Engine, toolPostgreSQL) {
		if err := writeManagedDatabasePasswordFile(DatabasePasswordFilePath(rootDir, resolved.Environment.Name), credentials); err != nil {
			return ManagedDatabaseCredentials{}, err
		}
		if err := writeManagedDatabasePGPassFile(DatabasePGPassFilePath(rootDir, resolved.Environment.Name), credentials); err != nil {
			return ManagedDatabaseCredentials{}, err
		}
	} else {
		if err := writeManagedDatabaseDefaultsFile(DatabaseDefaultsFilePath(rootDir, resolved.Environment.Name), credentials); err != nil {
			return ManagedDatabaseCredentials{}, err
		}
		if err := writeManagedDatabaseBootstrapSQLFile(DatabaseBootstrapSQLPath(rootDir, resolved.Environment.Name), credentials); err != nil {
			return ManagedDatabaseCredentials{}, err
		}
	}

	return credentials, nil
}

func ResolveDatabaseDumpTarget(envsDir, engine, version string) (string, error) {
	installDir := filepath.Join(envsDir, engine, version)
	return resolveDatabaseToolTarget(installDir, engine, databaseDumpCandidates, "%s dump utility version %q is not installed under %s", envsDir, version)
}

func InitializeDatabaseServer(spec ManagedDatabaseServerSpec) error {
	initialized, err := DatabaseServerInitialized(spec)
	if err != nil {
		return err
	}
	if initialized {
		return nil
	}

	hasContents, err := databaseDataInitialized(spec.DataDir)
	if err != nil {
		return err
	}
	if hasContents {
		if err := resetDatabaseDataDir(spec.DataDir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(spec.DataDir, 0o755); err != nil {
		return fmt.Errorf("create database data directory: %w", err)
	}

	logFile, err := openDatabaseLog(spec.LogPath)
	if err != nil {
		return err
	}
	defer logFile.Close()

	initializeTarget := spec.Target
	if spec.Engine == toolPostgreSQL {
		initializeTarget = spec.InitializeTarget
	} else if spec.Engine == toolMariaDB {
		initializeTarget, err = resolveDatabaseBootstrapTarget(spec.InstallDir, spec.Engine)
		if err != nil {
			return err
		}
	}

	command, err := prepareDatabaseCommand(initializeTarget, DatabaseInitializeArgs(spec))
	if err != nil {
		return err
	}
	if spec.Engine == toolMariaDB {
		command.Dir = spec.DataDir
	}
	command.Stdout = logFile
	command.Stderr = logFile

	if err := command.Run(); err != nil {
		return fmt.Errorf("initialize %s data directory: %w (see %s)", spec.Engine, err, spec.LogPath)
	}
	if spec.Engine == toolPostgreSQL {
		if err := os.WriteFile(spec.BootstrapMarker, []byte("pending\n"), 0o600); err != nil {
			return fmt.Errorf("write postgresql bootstrap marker: %w", err)
		}
	}

	return nil
}

func StartDatabaseServer(spec ManagedDatabaseServerSpec) (ManagedDatabaseStartResult, error) {
	logFile, err := openDatabaseLog(spec.LogPath)
	if err != nil {
		return ManagedDatabaseStartResult{}, err
	}
	defer logFile.Close()

	address := DatabaseAddress(spec.Port)
	if PingDatabaseAddress(address) {
		return ManagedDatabaseStartResult{}, fmt.Errorf("%s server cannot start because %s is already in use", spec.Engine, address)
	}

	command, err := prepareDatabaseCommand(spec.Target, DatabaseStartArgs(spec))
	if err != nil {
		return ManagedDatabaseStartResult{}, err
	}
	command.Stdout = logFile
	command.Stderr = logFile

	if err := command.Start(); err != nil {
		return ManagedDatabaseStartResult{}, fmt.Errorf("start %s server: %w", spec.Engine, err)
	}

	pid := command.Process.Pid
	deadline := time.Now().Add(managedDatabaseStartupTimeout)
	for time.Now().Before(deadline) {
		if PingDatabaseAddress(address) {
			if spec.Engine == toolPostgreSQL {
				if err := bootstrapPostgreSQLDatabase(spec, logFile); err != nil {
					if command.Process != nil {
						_ = command.Process.Kill()
						_ = command.Process.Release()
					}
					return ManagedDatabaseStartResult{}, err
				}
			}
			_ = command.Process.Release()
			return ManagedDatabaseStartResult{PID: pid}, nil
		}

		time.Sleep(managedDatabasePollInterval)
	}

	if command.Process != nil {
		_ = command.Process.Kill()
		_ = command.Process.Release()
	}

	return ManagedDatabaseStartResult{}, fmt.Errorf("%s server did not start listening on %s within %s (see %s)", spec.Engine, address, managedDatabaseStartupTimeout, spec.LogPath)
}

func bootstrapPostgreSQLDatabase(spec ManagedDatabaseServerSpec, logFile *os.File) error {
	_, err := os.Stat(spec.BootstrapMarker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat postgresql bootstrap marker: %w", err)
	}

	args := []string{
		"--host=" + DatabaseListenHost,
		"--port=" + strconv.Itoa(spec.Port),
		"--username=" + ManagedDatabaseUserName,
		"--maintenance-db=postgres",
		"--encoding=UTF8",
		spec.DatabaseName,
	}
	command, err := prepareDatabaseCommand(spec.CreateTarget, args)
	if err != nil {
		return err
	}
	command.Env = append(os.Environ(), "PGPASSFILE="+spec.DefaultsFile)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Run(); err != nil {
		return fmt.Errorf("create postgresql database %q: %w (see %s)", spec.DatabaseName, err, spec.LogPath)
	}
	if err := os.Remove(spec.BootstrapMarker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove postgresql bootstrap marker: %w", err)
	}

	return nil
}

func StopDatabaseServer(state ManagedDatabaseRuntimeState) error {
	address := DatabaseAddress(state.Port)
	if !PingDatabaseAddress(address) {
		return nil
	}
	if strings.TrimSpace(state.AdminTarget) == "" {
		return fmt.Errorf("database state for %q does not include an admin target", state.EnvironmentName)
	}
	if state.Engine != toolPostgreSQL && strings.TrimSpace(state.DefaultsFile) == "" {
		return fmt.Errorf("database state for %q does not include a defaults file", state.EnvironmentName)
	}

	logFile, err := openDatabaseLog(state.LogPath)
	if err != nil {
		return err
	}
	defer logFile.Close()

	args := []string{"--defaults-extra-file=" + state.DefaultsFile, "shutdown"}
	if state.Engine == toolPostgreSQL {
		args = []string{"-D", state.DataDir, "stop", "-m", "fast", "-w", "-t", strconv.Itoa(int(managedDatabaseShutdownTimeout.Seconds()))}
	}
	command, err := prepareDatabaseCommand(state.AdminTarget, args)
	if err != nil {
		return err
	}
	command.Stdout = logFile
	command.Stderr = logFile

	if err := command.Run(); err != nil {
		return fmt.Errorf("shutdown %s server: %w (see %s)", state.Engine, err, state.LogPath)
	}

	deadline := time.Now().Add(managedDatabaseShutdownTimeout)
	for time.Now().Before(deadline) {
		if !PingDatabaseAddress(address) {
			return nil
		}

		time.Sleep(managedDatabasePollInterval)
	}

	return fmt.Errorf("%s server did not stop listening on %s within %s", state.Engine, address, managedDatabaseShutdownTimeout)
}

func DatabaseInitializeArgs(spec ManagedDatabaseServerSpec) []string {
	if spec.Engine == toolPostgreSQL {
		return []string{
			"--pgdata=" + spec.DataDir,
			"--username=" + ManagedDatabaseUserName,
			"--pwfile=" + spec.PasswordFile,
			"--auth-host=scram-sha-256",
			"--auth-local=trust",
			"--encoding=UTF8",
		}
	}
	if spec.Engine == toolMariaDB {
		return []string{
			"--datadir=" + spec.DataDir,
			"--port=" + strconv.Itoa(spec.Port),
		}
	}

	return []string{
		"--initialize-insecure",
		"--basedir=" + spec.InstallDir,
		"--datadir=" + spec.DataDir,
	}
}

func DatabaseStartArgs(spec ManagedDatabaseServerSpec) []string {
	if spec.Engine == toolPostgreSQL {
		return []string{
			"-D", spec.DataDir,
			"-h", DatabaseListenHost,
			"-p", strconv.Itoa(spec.Port),
		}
	}
	args := []string{
		"--basedir=" + spec.InstallDir,
		"--datadir=" + spec.DataDir,
		"--port=" + strconv.Itoa(spec.Port),
		"--bind-address=" + DatabaseListenHost,
		"--init-file=" + spec.BootstrapSQLFile,
		"--log-error=" + spec.LogPath,
	}

	if runtime.GOOS == "windows" {
		args = append(args, "--console")
	}

	return args
}

func DatabaseServerInitialized(spec ManagedDatabaseServerSpec) (bool, error) {
	initialized, err := databaseDataInitialized(spec.DataDir)
	if err != nil || !initialized {
		return initialized, err
	}
	if spec.Engine == toolPostgreSQL {
		data, err := os.ReadFile(filepath.Join(spec.DataDir, "PG_VERSION"))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read postgresql data version: %w", err)
		}

		return strings.TrimSpace(string(data)) != "", nil
	}

	entries, err := os.ReadDir(filepath.Join(spec.DataDir, "mysql"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read database system directory: %w", err)
	}

	return len(entries) > 0, nil
}

func EffectiveDatabasePort(database *config.DatabaseConfig) int {
	if database != nil && database.Port == 0 && strings.EqualFold(strings.TrimSpace(database.Engine), toolPostgreSQL) {
		return DefaultPostgreSQLPort
	}
	if database == nil || database.Port == 0 {
		return DefaultDatabasePort
	}

	return database.Port
}

func DatabaseAddress(port int) string {
	return net.JoinHostPort(DatabaseListenHost, strconv.Itoa(port))
}

// DatabaseClientCommand returns the dispatch command for a database engine.
func DatabaseClientCommand(engine string) string {
	if strings.EqualFold(strings.TrimSpace(engine), toolPostgreSQL) {
		return "psql"
	}

	return strings.ToLower(strings.TrimSpace(engine))
}

func PingDatabaseAddress(address string) bool {
	connection, err := net.DialTimeout("tcp", address, managedDatabasePollInterval)
	if err != nil {
		return false
	}
	_ = connection.Close()

	return true
}

func DatabaseStatePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedDatabaseStateDirectory, managedDatabaseStateSubdirectory, environmentName+".json")
}

func DatabaseLogPath(rootDir, environmentName string) string {
	return DatabaseEngineLogPath(rootDir, "", environmentName)
}

func DatabaseEngineLogPath(rootDir, engine, environmentName string) string {
	tool := strings.ToLower(strings.TrimSpace(engine))
	if tool == "" {
		tool = managedDatabaseStateSubdirectory
	}

	return filepath.Join(ToolLogRoot(rootDir, tool, environmentName), "server.log")
}

func LegacyDatabaseDataPath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedDatabaseDataDirectory, managedDatabaseDataSubdirectory, environmentName)
}

func DatabaseDataPath(rootDir, environmentName, engine, version string) string {
	return filepath.Join(rootDir, managedDatabaseDataDirectory, managedDatabaseDataSubdirectory, environmentName, engine, version)
}

func ResolveDatabaseDataPath(rootDir string, resolved ResolvedDatabaseEnvironment) (string, error) {
	preferred := DatabaseDataPath(rootDir, resolved.Environment.Name, resolved.Database.Engine, resolved.Database.Version)
	initialized, err := databaseDataInitialized(preferred)
	if err != nil {
		return "", err
	}
	if initialized {
		return preferred, nil
	}

	legacy := LegacyDatabaseDataPath(rootDir, resolved.Environment.Name)
	initialized, err = databaseDataInitialized(legacy)
	if err != nil {
		return "", err
	}
	if !initialized {
		return preferred, nil
	}

	layout, err := detectDatabaseDataLayout(legacy)
	if err != nil {
		return "", err
	}
	if layout != "" && !strings.EqualFold(layout, resolved.Database.Engine) {
		return preferred, nil
	}

	return legacy, nil
}

func DatabaseCredentialStatePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedDatabaseSecretDirectory, managedDatabaseSecretSubdirectory, environmentName+".json")
}

func DatabaseDefaultsFilePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedDatabaseSecretDirectory, managedDatabaseSecretSubdirectory, environmentName+".defaults.cnf")
}

func DatabaseBootstrapSQLPath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedDatabaseSecretDirectory, managedDatabaseSecretSubdirectory, environmentName+".bootstrap.sql")
}

// DatabasePasswordFilePath returns the initdb password file for an environment.
func DatabasePasswordFilePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedDatabaseSecretDirectory, managedDatabaseSecretSubdirectory, environmentName, "password")
}

// DatabasePGPassFilePath returns the PostgreSQL client password file for an environment.
func DatabasePGPassFilePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedDatabaseSecretDirectory, managedDatabaseSecretSubdirectory, environmentName, "pgpass")
}

func LoadLiveManagedDatabaseState(rootDir, environmentName string, ping func(string) bool) (*ManagedDatabaseRuntimeState, error) {
	statePath := DatabaseStatePath(rootDir, environmentName)
	state, err := LoadManagedDatabaseState(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ManagedDatabaseStateIsLive(*state, ping) {
		return state, nil
	}
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale database state: %w", err)
	}

	return nil, nil
}

func LoadLiveManagedDatabaseStateForResolved(rootDir string, resolved ResolvedDatabaseEnvironment, ping func(string) bool) (*ManagedDatabaseRuntimeState, error) {
	state, err := LoadLiveManagedDatabaseState(rootDir, resolved.Environment.Name, ping)
	if err != nil || state == nil {
		return state, err
	}
	if databaseStateMatchesResolved(*state, resolved) {
		return state, nil
	}

	return nil, fmt.Errorf(
		"environment %q still has a running %s %s database on %s, but the current database is %s %s; stop the running database first",
		resolved.Environment.Name,
		state.Engine,
		state.Version,
		DatabaseAddress(state.Port),
		resolved.Database.Engine,
		resolved.Database.Version,
	)
}

func LoadManagedDatabaseState(path string) (*ManagedDatabaseRuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state ManagedDatabaseRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode database state %s: %w", path, err)
	}

	return &state, nil
}

func LoadManagedDatabaseCredentials(path string) (ManagedDatabaseCredentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ManagedDatabaseCredentials{}, err
	}

	var credentials ManagedDatabaseCredentials
	if err := json.Unmarshal(data, &credentials); err != nil {
		return ManagedDatabaseCredentials{}, fmt.Errorf("decode database credentials %s: %w", path, err)
	}

	return credentials, nil
}

func ManagedDatabaseStateIsLive(state ManagedDatabaseRuntimeState, ping func(string) bool) bool {
	return servicePIDIsLive(state.PID) && databasePingFunc(ping)(DatabaseAddress(state.Port))
}

func WriteManagedDatabaseState(path string, state ManagedDatabaseRuntimeState) error {
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

func buildManagedDatabaseServerSpec(ctx Context, resolved ResolvedDatabaseEnvironment) (ManagedDatabaseServerSpec, error) {
	if !ctx.hasTool(resolved.Database.Engine) {
		return ManagedDatabaseServerSpec{}, fmt.Errorf("unsupported tool %q", resolved.Database.Engine)
	}

	credentials, err := EnsureManagedDatabaseCredentialAssets(ctx.RootDir, resolved)
	if err != nil {
		return ManagedDatabaseServerSpec{}, err
	}
	dataDir, err := ResolveDatabaseDataPath(ctx.RootDir, resolved)
	if err != nil {
		return ManagedDatabaseServerSpec{}, err
	}

	installDir := filepath.Join(ctx.EnvsDir, resolved.Database.Engine, resolved.Database.Version)
	target, err := resolveDatabaseServerTarget(ctx.EnvsDir, resolved.Database.Engine, resolved.Database.Version)
	if err != nil {
		return ManagedDatabaseServerSpec{}, err
	}
	adminTarget, err := resolveDatabaseAdminTarget(ctx.EnvsDir, resolved.Database.Engine, resolved.Database.Version)
	if err != nil {
		return ManagedDatabaseServerSpec{}, err
	}
	initializeTarget := target
	createTarget := ""
	defaultsFile := DatabaseDefaultsFilePath(ctx.RootDir, resolved.Environment.Name)
	if resolved.Database.Engine == toolPostgreSQL {
		initializeTarget, err = resolveDatabaseInitializeTarget(ctx.EnvsDir, resolved.Database.Engine, resolved.Database.Version)
		if err != nil {
			return ManagedDatabaseServerSpec{}, err
		}
		createTarget, err = resolveDatabaseCreateTarget(ctx.EnvsDir, resolved.Database.Engine, resolved.Database.Version)
		if err != nil {
			return ManagedDatabaseServerSpec{}, err
		}
		defaultsFile = DatabasePGPassFilePath(ctx.RootDir, resolved.Environment.Name)
	}

	return ManagedDatabaseServerSpec{
		EnvironmentName:  resolved.Environment.Name,
		Engine:           resolved.Database.Engine,
		Version:          resolved.Database.Version,
		InstallDir:       installDir,
		Target:           target,
		InitializeTarget: initializeTarget,
		AdminTarget:      adminTarget,
		CreateTarget:     createTarget,
		DataDir:          dataDir,
		LogPath:          DatabaseEngineLogPath(ctx.RootDir, resolved.Database.Engine, resolved.Environment.Name),
		DefaultsFile:     defaultsFile,
		BootstrapSQLFile: DatabaseBootstrapSQLPath(ctx.RootDir, resolved.Environment.Name),
		PasswordFile:     DatabasePasswordFilePath(ctx.RootDir, resolved.Environment.Name),
		BootstrapMarker:  filepath.Join(dataDir, ".polka-bootstrap"),
		DatabaseName:     credentials.DatabaseName,
		Port:             credentials.Port,
	}, nil
}

func resolveDatabaseServerTarget(envsDir, engine, version string) (string, error) {
	installDir := filepath.Join(envsDir, engine, version)
	return resolveDatabaseToolTarget(installDir, engine, databaseServerCandidates, "%s server version %q is not installed under %s", envsDir, version)
}

func resolveDatabaseAdminTarget(envsDir, engine, version string) (string, error) {
	installDir := filepath.Join(envsDir, engine, version)
	return resolveDatabaseToolTarget(installDir, engine, databaseAdminCandidates, "%s admin client version %q is not installed under %s", envsDir, version)
}

func resolveDatabaseInitializeTarget(envsDir, engine, version string) (string, error) {
	installDir := filepath.Join(envsDir, engine, version)
	return resolveDatabaseToolTarget(installDir, engine, databaseInitializeCandidates, "%s initialization utility version %q is not installed under %s", envsDir, version)
}

func resolveDatabaseCreateTarget(envsDir, engine, version string) (string, error) {
	installDir := filepath.Join(envsDir, engine, version)
	return resolveDatabaseToolTarget(installDir, engine, databaseCreateCandidates, "%s database creation utility version %q is not installed under %s", envsDir, version)
}

func resolveDatabaseToolTarget(installDir, engine string, candidates func(string, string) []string, message string, envsDir, version string) (string, error) {
	for _, candidate := range candidates(installDir, engine) {
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

	return "", fmt.Errorf(message, engine, version, filepath.Join(envsDir, engine, version))
}

func databaseServerCandidates(installDir, engine string) []string {
	if runtime.GOOS == "windows" {
		switch engine {
		case toolMySQL:
			return []string{
				filepath.Join(installDir, "bin", "mysqld.exe"),
				filepath.Join(installDir, "bin", "mysqld.cmd"),
				filepath.Join(installDir, "bin", "mysqld.bat"),
				filepath.Join(installDir, "mysqld.exe"),
				filepath.Join(installDir, "mysqld.cmd"),
				filepath.Join(installDir, "mysqld.bat"),
			}
		case toolMariaDB:
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
		case toolPostgreSQL:
			return []string{
				filepath.Join(installDir, "bin", "postgres.exe"),
				filepath.Join(installDir, "postgres.exe"),
			}
		}
	}

	switch engine {
	case toolMySQL:
		return []string{
			filepath.Join(installDir, "bin", "mysqld"),
			filepath.Join(installDir, "mysqld"),
		}
	case toolMariaDB:
		return []string{
			filepath.Join(installDir, "bin", "mariadbd"),
			filepath.Join(installDir, "bin", "mysqld"),
			filepath.Join(installDir, "mariadbd"),
			filepath.Join(installDir, "mysqld"),
		}
	case toolPostgreSQL:
		return []string{
			filepath.Join(installDir, "bin", "postgres"),
			filepath.Join(installDir, "postgres"),
		}
	default:
		return nil
	}
}

func databaseAdminCandidates(installDir, engine string) []string {
	if runtime.GOOS == "windows" {
		switch engine {
		case toolMySQL:
			return []string{
				filepath.Join(installDir, "bin", "mysqladmin.exe"),
				filepath.Join(installDir, "bin", "mysqladmin.cmd"),
				filepath.Join(installDir, "bin", "mysqladmin.bat"),
				filepath.Join(installDir, "mysqladmin.exe"),
				filepath.Join(installDir, "mysqladmin.cmd"),
				filepath.Join(installDir, "mysqladmin.bat"),
			}
		case toolMariaDB:
			return []string{
				filepath.Join(installDir, "bin", "mariadb-admin.exe"),
				filepath.Join(installDir, "bin", "mariadb-admin.cmd"),
				filepath.Join(installDir, "bin", "mariadb-admin.bat"),
				filepath.Join(installDir, "bin", "mysqladmin.exe"),
				filepath.Join(installDir, "bin", "mysqladmin.cmd"),
				filepath.Join(installDir, "bin", "mysqladmin.bat"),
				filepath.Join(installDir, "mariadb-admin.exe"),
				filepath.Join(installDir, "mariadb-admin.cmd"),
				filepath.Join(installDir, "mariadb-admin.bat"),
				filepath.Join(installDir, "mysqladmin.exe"),
				filepath.Join(installDir, "mysqladmin.cmd"),
				filepath.Join(installDir, "mysqladmin.bat"),
			}
		case toolPostgreSQL:
			return []string{
				filepath.Join(installDir, "bin", "pg_ctl.exe"),
				filepath.Join(installDir, "pg_ctl.exe"),
			}
		}
	}

	switch engine {
	case toolMySQL:
		return []string{
			filepath.Join(installDir, "bin", "mysqladmin"),
			filepath.Join(installDir, "mysqladmin"),
		}
	case toolMariaDB:
		return []string{
			filepath.Join(installDir, "bin", "mariadb-admin"),
			filepath.Join(installDir, "bin", "mysqladmin"),
			filepath.Join(installDir, "mariadb-admin"),
			filepath.Join(installDir, "mysqladmin"),
		}
	case toolPostgreSQL:
		return []string{
			filepath.Join(installDir, "bin", "pg_ctl"),
			filepath.Join(installDir, "pg_ctl"),
		}
	default:
		return nil
	}
}

func databaseDumpCandidates(installDir, engine string) []string {
	if runtime.GOOS == "windows" {
		switch engine {
		case toolMySQL:
			return []string{
				filepath.Join(installDir, "bin", "mysqldump.exe"),
				filepath.Join(installDir, "bin", "mysqldump.cmd"),
				filepath.Join(installDir, "bin", "mysqldump.bat"),
				filepath.Join(installDir, "mysqldump.exe"),
				filepath.Join(installDir, "mysqldump.cmd"),
				filepath.Join(installDir, "mysqldump.bat"),
			}
		case toolMariaDB:
			return []string{
				filepath.Join(installDir, "bin", "mariadb-dump.exe"),
				filepath.Join(installDir, "bin", "mariadb-dump.cmd"),
				filepath.Join(installDir, "bin", "mariadb-dump.bat"),
				filepath.Join(installDir, "bin", "mysqldump.exe"),
				filepath.Join(installDir, "bin", "mysqldump.cmd"),
				filepath.Join(installDir, "bin", "mysqldump.bat"),
				filepath.Join(installDir, "mariadb-dump.exe"),
				filepath.Join(installDir, "mariadb-dump.cmd"),
				filepath.Join(installDir, "mariadb-dump.bat"),
				filepath.Join(installDir, "mysqldump.exe"),
				filepath.Join(installDir, "mysqldump.cmd"),
				filepath.Join(installDir, "mysqldump.bat"),
			}
		case toolPostgreSQL:
			return []string{
				filepath.Join(installDir, "bin", "pg_dump.exe"),
				filepath.Join(installDir, "pg_dump.exe"),
			}
		}
	}

	switch engine {
	case toolMySQL:
		return []string{
			filepath.Join(installDir, "bin", "mysqldump"),
			filepath.Join(installDir, "mysqldump"),
		}
	case toolMariaDB:
		return []string{
			filepath.Join(installDir, "bin", "mariadb-dump"),
			filepath.Join(installDir, "bin", "mysqldump"),
			filepath.Join(installDir, "mariadb-dump"),
			filepath.Join(installDir, "mysqldump"),
		}
	case toolPostgreSQL:
		return []string{
			filepath.Join(installDir, "bin", "pg_dump"),
			filepath.Join(installDir, "pg_dump"),
		}
	default:
		return nil
	}
}

func databaseInitializeCandidates(installDir, engine string) []string {
	if engine != toolPostgreSQL {
		return nil
	}
	if runtime.GOOS == "windows" {
		return []string{filepath.Join(installDir, "bin", "initdb.exe"), filepath.Join(installDir, "initdb.exe")}
	}

	return []string{filepath.Join(installDir, "bin", "initdb"), filepath.Join(installDir, "initdb")}
}

func databaseCreateCandidates(installDir, engine string) []string {
	if engine != toolPostgreSQL {
		return nil
	}
	if runtime.GOOS == "windows" {
		return []string{filepath.Join(installDir, "bin", "createdb.exe"), filepath.Join(installDir, "createdb.exe")}
	}

	return []string{filepath.Join(installDir, "bin", "createdb"), filepath.Join(installDir, "createdb")}
}

func resolveDatabaseBootstrapTarget(installDir, engine string) (string, error) {
	for _, candidate := range databaseBootstrapCandidates(installDir, engine) {
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

	return "", fmt.Errorf("%s bootstrap helper was not found under %s", engine, installDir)
}

func databaseBootstrapCandidates(installDir, engine string) []string {
	if engine != toolMariaDB {
		return nil
	}

	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(installDir, "bin", "mariadb-install-db.exe"),
			filepath.Join(installDir, "bin", "mysql_install_db.exe"),
			filepath.Join(installDir, "mariadb-install-db.exe"),
			filepath.Join(installDir, "mysql_install_db.exe"),
		}
	}

	return []string{
		filepath.Join(installDir, "bin", "mariadb-install-db"),
		filepath.Join(installDir, "bin", "mysql_install_db"),
		filepath.Join(installDir, "mariadb-install-db"),
		filepath.Join(installDir, "mysql_install_db"),
	}
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

func resetDatabaseDataDir(dataDir string) error {
	entries, err := os.ReadDir(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read database data directory: %w", err)
	}

	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dataDir, entry.Name())); err != nil {
			return fmt.Errorf("reset incomplete database data directory: %w", err)
		}
	}

	return nil
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

func detectDatabaseDataLayout(dataDir string) (string, error) {
	if exists, err := pathExists(filepath.Join(dataDir, "PG_VERSION")); err != nil {
		return "", err
	} else if exists {
		return toolPostgreSQL, nil
	}

	for _, marker := range []string{"#innodb_redo", "undo_001", "undo_002"} {
		exists, err := pathExists(filepath.Join(dataDir, marker))
		if err != nil {
			return "", err
		}
		if exists {
			return toolMySQL, nil
		}
	}

	for _, marker := range []string{"ib_logfile0", "aria_log_control"} {
		exists, err := pathExists(filepath.Join(dataDir, marker))
		if err != nil {
			return "", err
		}
		if exists {
			return toolMariaDB, nil
		}
	}

	return "", nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	return false, fmt.Errorf("stat %s: %w", path, err)
}

func databaseStateMatchesResolved(state ManagedDatabaseRuntimeState, resolved ResolvedDatabaseEnvironment) bool {
	return strings.EqualFold(strings.TrimSpace(state.Engine), strings.TrimSpace(resolved.Database.Engine)) && strings.TrimSpace(state.Version) == strings.TrimSpace(resolved.Database.Version)
}

func writeManagedDatabaseCredentials(path string, credentials ManagedDatabaseCredentials) error {
	data, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return fmt.Errorf("encode database credentials: %w", err)
	}
	data = append(data, '\n')

	if err := writeManagedDatabaseSecretFile(path, data); err != nil {
		return fmt.Errorf("write database credentials %s: %w", path, err)
	}

	return nil
}

func writeManagedDatabaseDefaultsFile(path string, credentials ManagedDatabaseCredentials) error {
	defaults := []byte(renderManagedDatabaseDefaultsFile(credentials))
	if err := writeManagedDatabaseSecretFile(path, defaults); err != nil {
		return fmt.Errorf("write database defaults file %s: %w", path, err)
	}

	return nil
}

func writeManagedDatabaseBootstrapSQLFile(path string, credentials ManagedDatabaseCredentials) error {
	bootstrap := []byte(renderManagedDatabaseBootstrapSQL(credentials))
	if err := writeManagedDatabaseSecretFile(path, bootstrap); err != nil {
		return fmt.Errorf("write database bootstrap SQL %s: %w", path, err)
	}

	return nil
}

func writeManagedDatabasePasswordFile(path string, credentials ManagedDatabaseCredentials) error {
	if err := writeManagedDatabaseSecretFile(path, []byte(credentials.Password+"\n")); err != nil {
		return fmt.Errorf("write postgresql password file %s: %w", path, err)
	}

	return nil
}

func writeManagedDatabasePGPassFile(path string, credentials ManagedDatabaseCredentials) error {
	if err := writeManagedDatabaseSecretFile(path, []byte(renderManagedDatabasePGPassFile(credentials))); err != nil {
		return fmt.Errorf("write postgresql password file %s: %w", path, err)
	}

	return nil
}

func writeManagedDatabaseSecretFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create database secret directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}

	return nil
}

func renderManagedDatabaseDefaultsFile(credentials ManagedDatabaseCredentials) string {
	var builder strings.Builder
	builder.WriteString("[client]\n")
	builder.WriteString("user=")
	builder.WriteString(credentials.User)
	builder.WriteByte('\n')
	builder.WriteString("password=")
	builder.WriteString(credentials.Password)
	builder.WriteByte('\n')
	builder.WriteString("host=")
	builder.WriteString(DatabaseListenHost)
	builder.WriteByte('\n')
	builder.WriteString("port=")
	builder.WriteString(strconv.Itoa(credentials.Port))
	builder.WriteByte('\n')
	builder.WriteString("protocol=tcp\n")

	return builder.String()
}

func renderManagedDatabaseBootstrapSQL(credentials ManagedDatabaseCredentials) string {
	var builder strings.Builder
	if strings.TrimSpace(credentials.DatabaseName) != "" {
		builder.WriteString("CREATE DATABASE IF NOT EXISTS ")
		builder.WriteString(quoteDatabaseIdentifier(credentials.DatabaseName))
		builder.WriteString(";\n")
	}
	builder.WriteString("CREATE USER IF NOT EXISTS '")
	builder.WriteString(credentials.User)
	builder.WriteString("'@'localhost' IDENTIFIED BY '")
	builder.WriteString(credentials.Password)
	builder.WriteString("';\n")
	builder.WriteString("ALTER USER '")
	builder.WriteString(credentials.User)
	builder.WriteString("'@'localhost' IDENTIFIED BY '")
	builder.WriteString(credentials.Password)
	builder.WriteString("';\n")
	builder.WriteString("GRANT ALL PRIVILEGES ON *.* TO '")
	builder.WriteString(credentials.User)
	builder.WriteString("'@'localhost' WITH GRANT OPTION;\n")
	builder.WriteString("CREATE USER IF NOT EXISTS '")
	builder.WriteString(credentials.User)
	builder.WriteString("'@'127.0.0.1' IDENTIFIED BY '")
	builder.WriteString(credentials.Password)
	builder.WriteString("';\n")
	builder.WriteString("ALTER USER '")
	builder.WriteString(credentials.User)
	builder.WriteString("'@'127.0.0.1' IDENTIFIED BY '")
	builder.WriteString(credentials.Password)
	builder.WriteString("';\n")
	builder.WriteString("GRANT ALL PRIVILEGES ON *.* TO '")
	builder.WriteString(credentials.User)
	builder.WriteString("'@'127.0.0.1' WITH GRANT OPTION;\n")
	builder.WriteString("FLUSH PRIVILEGES;\n")

	return builder.String()
}

func renderManagedDatabasePGPassFile(credentials ManagedDatabaseCredentials) string {
	return strings.Join([]string{
		escapePGPassField(DatabaseListenHost),
		strconv.Itoa(credentials.Port),
		"*",
		escapePGPassField(credentials.User),
		escapePGPassField(credentials.Password),
	}, ":") + "\n"
}

func escapePGPassField(value string) string {
	escaped := strings.ReplaceAll(value, "\\", "\\\\")
	return strings.ReplaceAll(escaped, ":", "\\:")
}

func quoteDatabaseIdentifier(name string) string {
	return "`" + strings.ReplaceAll(strings.TrimSpace(name), "`", "``") + "`"
}

func generateDatabasePassword() (string, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate database password: %w", err)
	}

	return hex.EncodeToString(buffer), nil
}

func prepareDatabaseCommand(target string, args []string) (*exec.Cmd, error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return nil, fmt.Errorf("dispatch target cannot be empty")
	}

	if runtime.GOOS == "windows" {
		extension := strings.ToLower(filepath.Ext(trimmed))
		if extension == ".cmd" || extension == ".bat" {
			commandArgs := append([]string{"/c", trimmed}, args...)
			return exec.Command("cmd.exe", commandArgs...), nil
		}
	}

	return exec.Command(trimmed, args...), nil
}

func managedDatabaseName(resolved ResolvedDatabaseEnvironment) string {
	return strings.TrimSpace(resolved.Environment.Name)
}

func databasePingFunc(ping func(string) bool) func(string) bool {
	if ping == nil {
		return PingDatabaseAddress
	}

	return ping
}

func (hooks DatabaseRuntimeHooks) withDefaults() DatabaseRuntimeHooks {
	if hooks.InitializeServer == nil {
		hooks.InitializeServer = InitializeDatabaseServer
	}
	if hooks.StartServer == nil {
		hooks.StartServer = StartDatabaseServer
	}
	if hooks.StopServer == nil {
		hooks.StopServer = StopDatabaseServer
	}
	if hooks.PingAddress == nil {
		hooks.PingAddress = PingDatabaseAddress
	}
	if hooks.Now == nil {
		hooks.Now = time.Now
	}

	return hooks
}
