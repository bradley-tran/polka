package cli

import (
	"crypto/rand"
	"encoding/hex"
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

	dbListenHost         = "127.0.0.1"
	dbDefaultPort        = 3306
	dbPollInterval       = 200 * time.Millisecond
	dbStartupTimeout     = 10 * time.Second
	dbShutdownTimeout    = 5 * time.Second
	dbStateDirectory     = "run"
	dbStateSubdirectory  = "db"
	dbDataDirectory      = "data"
	dbDataSubdirectory   = "db"
	dbSecretDirectory    = "secrets"
	dbSecretSubdirectory = "db"
	dbManagedUserName    = "polka"
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
	EnvironmentName  string
	Engine           string
	Version          string
	InstallDir       string
	Target           string
	AdminTarget      string
	DataDir          string
	LogPath          string
	DefaultsFile     string
	BootstrapSQLFile string
	Port             int
}

type dbStartResult struct {
	PID int
}

type dbManagedCredentials struct {
	EnvironmentName string `json:"environment"`
	Engine          string `json:"engine"`
	Version         string `json:"version"`
	User            string `json:"user"`
	Password        string `json:"password"`
	Port            int    `json:"port"`
}

type dbRuntimeState struct {
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
	if strings.TrimSpace(state.AdminTarget) == "" || strings.TrimSpace(state.DefaultsFile) == "" {
		spec, specErr := buildDBServerSpec(store, resolved)
		if specErr != nil {
			fmt.Fprintf(stderr, "error: %v\n", specErr)
			return 1
		}
		if strings.TrimSpace(state.AdminTarget) == "" {
			state.AdminTarget = spec.AdminTarget
		}
		if strings.TrimSpace(state.DefaultsFile) == "" {
			state.DefaultsFile = spec.DefaultsFile
		}
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

	state, err := loadLiveDatabaseStateForResolved(store.RootDir, resolved)
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
	credentials, err := ensureDatabaseCredentialAssets(store.RootDir, resolved)
	if err != nil {
		return dbServerSpec{}, err
	}
	dataDir, err := resolveDatabaseDataPath(store.RootDir, resolved)
	if err != nil {
		return dbServerSpec{}, err
	}

	installDir := filepath.Join(store.EnvsDir, resolved.Database.Engine, resolved.Database.Version)
	target, err := resolveDatabaseServerTarget(store.EnvsDir, resolved.Database.Engine, resolved.Database.Version)
	if err != nil {
		return dbServerSpec{}, err
	}
	adminTarget, err := resolveDatabaseAdminTarget(store.EnvsDir, resolved.Database.Engine, resolved.Database.Version)
	if err != nil {
		return dbServerSpec{}, err
	}

	return dbServerSpec{
		EnvironmentName:  resolved.Environment.Name,
		Engine:           resolved.Database.Engine,
		Version:          resolved.Database.Version,
		InstallDir:       installDir,
		Target:           target,
		AdminTarget:      adminTarget,
		DataDir:          dataDir,
		LogPath:          databaseLogPath(store.RootDir, resolved.Environment.Name),
		DefaultsFile:     databaseDefaultsFilePath(store.RootDir, resolved.Environment.Name),
		BootstrapSQLFile: databaseBootstrapSQLPath(store.RootDir, resolved.Environment.Name),
		Port:             credentials.Port,
	}, nil
}

func ensureManagedDatabaseStarted(store backend.Store, resolved dbResolvedEnvironment) (dbRuntimeState, bool, error) {
	statePath := databaseStatePath(store.RootDir, resolved.Environment.Name)
	state, err := loadLiveDatabaseStateForResolved(store.RootDir, resolved)
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
		AdminTarget:     spec.AdminTarget,
		DefaultsFile:    spec.DefaultsFile,
		StartedAt:       dbNowFunc().UTC(),
	}
	if err := writeDatabaseState(statePath, startedState); err != nil {
		_ = stopDatabaseServerFunc(startedState)
		return dbRuntimeState{}, false, err
	}

	return startedState, false, nil
}

func injectDatabaseConnectionArgs(rootDir string, resolved dbResolvedEnvironment, args []string) ([]string, error) {
	hasDefaultsFile, hasHost, hasPort, hasSocket, hasProtocol, protocol := databaseConnectionOverrides(args)
	if hasDefaultsFile || hasSocket {
		return args, nil
	}
	if hasProtocol && protocol != "" && !strings.EqualFold(protocol, "tcp") {
		return args, nil
	}

	state, err := loadLiveDatabaseStateForResolved(rootDir, resolved)
	if err != nil {
		return nil, err
	}
	if state != nil {
		credentials, credentialsErr := loadDatabaseCredentials(databaseCredentialStatePath(rootDir, resolved.Environment.Name))
		if errors.Is(credentialsErr, os.ErrNotExist) {
			return nil, fmt.Errorf("database credentials for environment %q are missing under %s; stop and restart the database to re-bootstrap them", resolved.Environment.Name, filepath.Join(rootDir, dbSecretDirectory, dbSecretSubdirectory))
		}
		if credentialsErr != nil {
			return nil, credentialsErr
		}
		if strings.TrimSpace(credentials.User) == "" || strings.TrimSpace(credentials.Password) == "" {
			return nil, fmt.Errorf("database credentials for environment %q are incomplete under %s; stop and restart the database to re-bootstrap them", resolved.Environment.Name, filepath.Join(rootDir, dbSecretDirectory, dbSecretSubdirectory))
		}
	} else {
		if _, credentialsErr := ensureDatabaseCredentialAssets(rootDir, resolved); credentialsErr != nil {
			return nil, credentialsErr
		}
	}

	port := effectiveDatabasePort(resolved.Database)
	if state != nil && state.Port != 0 {
		port = state.Port
	}

	injected := make([]string, 0, len(args)+4)
	injected = append(injected, "--defaults-extra-file="+databaseDefaultsFilePath(rootDir, resolved.Environment.Name))
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

func databaseConnectionOverrides(args []string) (hasDefaultsFile, hasHost, hasPort, hasSocket, hasProtocol bool, protocol string) {
	for index := 0; index < len(args); index++ {
		argument := strings.TrimSpace(args[index])
		switch {
		case argument == "--no-defaults":
			hasDefaultsFile = true
		case argument == "--defaults-file" || argument == "--defaults-extra-file":
			hasDefaultsFile = true
			if index+1 < len(args) {
				index++
			}
		case strings.HasPrefix(argument, "--defaults-file="):
			hasDefaultsFile = true
		case strings.HasPrefix(argument, "--defaults-extra-file="):
			hasDefaultsFile = true
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

	return hasDefaultsFile, hasHost, hasPort, hasSocket, hasProtocol, protocol
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

func resolveDatabaseAdminTarget(envsDir, engine, version string) (string, error) {
	installDir := filepath.Join(envsDir, engine, version)
	for _, candidate := range databaseAdminCandidates(installDir, engine) {
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

	return "", fmt.Errorf("%s admin client version %q is not installed under %s", engine, version, filepath.Join(envsDir, engine, version))
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

func databaseAdminCandidates(installDir, engine string) []string {
	if runtime.GOOS == "windows" {
		switch engine {
		case "mysql":
			return []string{
				filepath.Join(installDir, "bin", "mysqladmin.exe"),
				filepath.Join(installDir, "bin", "mysqladmin.cmd"),
				filepath.Join(installDir, "bin", "mysqladmin.bat"),
				filepath.Join(installDir, "mysqladmin.exe"),
				filepath.Join(installDir, "mysqladmin.cmd"),
				filepath.Join(installDir, "mysqladmin.bat"),
			}
		case "mariadb":
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
		}
	}

	switch engine {
	case "mysql":
		return []string{
			filepath.Join(installDir, "bin", "mysqladmin"),
			filepath.Join(installDir, "mysqladmin"),
		}
	case "mariadb":
		return []string{
			filepath.Join(installDir, "bin", "mariadb-admin"),
			filepath.Join(installDir, "bin", "mysqladmin"),
			filepath.Join(installDir, "mariadb-admin"),
			filepath.Join(installDir, "mysqladmin"),
		}
	default:
		return nil
	}
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
	if engine != "mariadb" {
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

func ensureDatabaseCredentialAssets(rootDir string, resolved dbResolvedEnvironment) (dbManagedCredentials, error) {
	path := databaseCredentialStatePath(rootDir, resolved.Environment.Name)
	credentials, err := loadDatabaseCredentials(path)
	if errors.Is(err, os.ErrNotExist) {
		password, passwordErr := generateDatabasePassword()
		if passwordErr != nil {
			return dbManagedCredentials{}, passwordErr
		}
		credentials = dbManagedCredentials{
			EnvironmentName: resolved.Environment.Name,
			Engine:          resolved.Database.Engine,
			Version:         resolved.Database.Version,
			User:            dbManagedUserName,
			Password:        password,
			Port:            effectiveDatabasePort(resolved.Database),
		}
	} else if err != nil {
		return dbManagedCredentials{}, err
	}

	if strings.TrimSpace(credentials.User) == "" {
		credentials.User = dbManagedUserName
	}
	if strings.TrimSpace(credentials.Password) == "" {
		password, passwordErr := generateDatabasePassword()
		if passwordErr != nil {
			return dbManagedCredentials{}, passwordErr
		}
		credentials.Password = password
	}
	credentials.EnvironmentName = resolved.Environment.Name
	credentials.Engine = resolved.Database.Engine
	credentials.Version = resolved.Database.Version
	credentials.Port = effectiveDatabasePort(resolved.Database)

	if err := writeDatabaseCredentials(path, credentials); err != nil {
		return dbManagedCredentials{}, err
	}
	if err := writeDatabaseDefaultsFile(databaseDefaultsFilePath(rootDir, resolved.Environment.Name), credentials); err != nil {
		return dbManagedCredentials{}, err
	}
	if err := writeDatabaseBootstrapSQLFile(databaseBootstrapSQLPath(rootDir, resolved.Environment.Name), credentials); err != nil {
		return dbManagedCredentials{}, err
	}

	return credentials, nil
}

func initializeDatabaseServer(spec dbServerSpec) error {
	initialized, err := databaseServerInitialized(spec)
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
	if spec.Engine == "mariadb" {
		initializeTarget, err = resolveDatabaseBootstrapTarget(spec.InstallDir, spec.Engine)
		if err != nil {
			return err
		}
	}

	command, err := prepareCommand(initializeTarget, databaseInitializeArgs(spec))
	if err != nil {
		return err
	}
	if spec.Engine == "mariadb" {
		command.Dir = spec.DataDir
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
	if strings.TrimSpace(state.AdminTarget) == "" {
		return fmt.Errorf("database state for %q does not include an admin target", state.EnvironmentName)
	}
	if strings.TrimSpace(state.DefaultsFile) == "" {
		return fmt.Errorf("database state for %q does not include a defaults file", state.EnvironmentName)
	}

	logFile, err := openDatabaseLog(state.LogPath)
	if err != nil {
		return err
	}
	defer logFile.Close()

	command, err := prepareCommand(state.AdminTarget, []string{"--defaults-extra-file=" + state.DefaultsFile, "shutdown"})
	if err != nil {
		return err
	}
	command.Stdout = logFile
	command.Stderr = logFile

	if err := command.Run(); err != nil {
		return fmt.Errorf("shutdown %s server: %w (see %s)", state.Engine, err, state.LogPath)
	}

	deadline := time.Now().Add(dbShutdownTimeout)
	for time.Now().Before(deadline) {
		if !pingDatabaseAddressFunc(address) {
			return nil
		}

		time.Sleep(dbPollInterval)
	}

	return fmt.Errorf("%s server did not stop listening on %s within %s", state.Engine, address, dbShutdownTimeout)
}

func databaseInitializeArgs(spec dbServerSpec) []string {
	if spec.Engine == "mariadb" {
		return []string{
			"--datadir=" + spec.DataDir,
			"--port=" + strconv.Itoa(spec.Port),
		}
	}

	args := []string{
		"--initialize-insecure",
		"--basedir=" + spec.InstallDir,
		"--datadir=" + spec.DataDir,
	}

	return args
}

func databaseStartArgs(spec dbServerSpec) []string {
	args := []string{
		"--basedir=" + spec.InstallDir,
		"--datadir=" + spec.DataDir,
		"--port=" + strconv.Itoa(spec.Port),
		"--bind-address=" + dbListenHost,
		"--init-file=" + spec.BootstrapSQLFile,
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

func databaseServerInitialized(spec dbServerSpec) (bool, error) {
	initialized, err := databaseDataInitialized(spec.DataDir)
	if err != nil || !initialized {
		return initialized, err
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

func legacyDatabaseDataPath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, dbDataDirectory, dbDataSubdirectory, environmentName)
}

func databaseDataPath(rootDir, environmentName, engine, version string) string {
	return filepath.Join(rootDir, dbDataDirectory, dbDataSubdirectory, environmentName, engine, version)
}

func resolveDatabaseDataPath(rootDir string, resolved dbResolvedEnvironment) (string, error) {
	preferred := databaseDataPath(rootDir, resolved.Environment.Name, resolved.Database.Engine, resolved.Database.Version)
	initialized, err := databaseDataInitialized(preferred)
	if err != nil {
		return "", err
	}
	if initialized {
		return preferred, nil
	}

	legacy := legacyDatabaseDataPath(rootDir, resolved.Environment.Name)
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

func detectDatabaseDataLayout(dataDir string) (string, error) {
	for _, marker := range []string{"#innodb_redo", "undo_001", "undo_002"} {
		exists, err := pathExists(filepath.Join(dataDir, marker))
		if err != nil {
			return "", err
		}
		if exists {
			return "mysql", nil
		}
	}

	for _, marker := range []string{"ib_logfile0", "aria_log_control"} {
		exists, err := pathExists(filepath.Join(dataDir, marker))
		if err != nil {
			return "", err
		}
		if exists {
			return "mariadb", nil
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

func databaseCredentialStatePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, dbSecretDirectory, dbSecretSubdirectory, environmentName+".json")
}

func databaseDefaultsFilePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, dbSecretDirectory, dbSecretSubdirectory, environmentName+".defaults.cnf")
}

func databaseBootstrapSQLPath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, dbSecretDirectory, dbSecretSubdirectory, environmentName+".bootstrap.sql")
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

func loadLiveDatabaseStateForResolved(rootDir string, resolved dbResolvedEnvironment) (*dbRuntimeState, error) {
	state, err := loadLiveDatabaseState(rootDir, resolved.Environment.Name)
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
		databaseAddress(state.Port),
		resolved.Database.Engine,
		resolved.Database.Version,
	)
}

func databaseStateMatchesResolved(state dbRuntimeState, resolved dbResolvedEnvironment) bool {
	return strings.EqualFold(strings.TrimSpace(state.Engine), strings.TrimSpace(resolved.Database.Engine)) && strings.TrimSpace(state.Version) == strings.TrimSpace(resolved.Database.Version)
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

func loadDatabaseCredentials(path string) (dbManagedCredentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return dbManagedCredentials{}, err
	}

	var credentials dbManagedCredentials
	if err := json.Unmarshal(data, &credentials); err != nil {
		return dbManagedCredentials{}, fmt.Errorf("decode database credentials %s: %w", path, err)
	}

	return credentials, nil
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

func writeDatabaseCredentials(path string, credentials dbManagedCredentials) error {
	data, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return fmt.Errorf("encode database credentials: %w", err)
	}
	data = append(data, '\n')

	if err := writeDatabaseSecretFile(path, data); err != nil {
		return fmt.Errorf("write database credentials %s: %w", path, err)
	}

	return nil
}

func writeDatabaseDefaultsFile(path string, credentials dbManagedCredentials) error {
	defaults := []byte(renderDatabaseDefaultsFile(credentials))
	if err := writeDatabaseSecretFile(path, defaults); err != nil {
		return fmt.Errorf("write database defaults file %s: %w", path, err)
	}

	return nil
}

func writeDatabaseBootstrapSQLFile(path string, credentials dbManagedCredentials) error {
	bootstrap := []byte(renderDatabaseBootstrapSQL(credentials))
	if err := writeDatabaseSecretFile(path, bootstrap); err != nil {
		return fmt.Errorf("write database bootstrap SQL %s: %w", path, err)
	}

	return nil
}

func writeDatabaseSecretFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create database secret directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}

	return nil
}

func renderDatabaseDefaultsFile(credentials dbManagedCredentials) string {
	var builder strings.Builder
	builder.WriteString("[client]\n")
	builder.WriteString("user=")
	builder.WriteString(credentials.User)
	builder.WriteByte('\n')
	builder.WriteString("password=")
	builder.WriteString(credentials.Password)
	builder.WriteByte('\n')
	builder.WriteString("host=")
	builder.WriteString(dbListenHost)
	builder.WriteByte('\n')
	builder.WriteString("port=")
	builder.WriteString(strconv.Itoa(credentials.Port))
	builder.WriteByte('\n')
	builder.WriteString("protocol=tcp\n")

	return builder.String()
}

func renderDatabaseBootstrapSQL(credentials dbManagedCredentials) string {
	var builder strings.Builder
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

func generateDatabasePassword() (string, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate database password: %w", err)
	}

	return hex.EncodeToString(buffer), nil
}
