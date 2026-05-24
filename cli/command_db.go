package cli

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"polka/backend"
)

const (
	dbSubcommandClient = "client"
	dbSubcommandExport = "export"
	dbSubcommandImport = "import"
	dbSubcommandStart  = "start"
	dbSubcommandStop   = "stop"
	dbSubcommandStatus = "status"

	dbListenHost      = backend.DatabaseListenHost
	dbManagedUserName = backend.ManagedDatabaseUserName
)

var (
	initializeDatabaseServerFunc = backend.InitializeDatabaseServer
	startDatabaseServerFunc      = backend.StartDatabaseServer
	stopDatabaseServerFunc       = backend.StopDatabaseServer
	pingDatabaseAddressFunc      = backend.PingDatabaseAddress
	dbNowFunc                    = time.Now
)

type dbResolvedEnvironment = backend.ResolvedDatabaseEnvironment

type dbServerSpec = backend.ManagedDatabaseServerSpec

type dbStartResult = backend.ManagedDatabaseStartResult

type dbManagedCredentials = backend.ManagedDatabaseCredentials

type dbRuntimeState = backend.ManagedDatabaseRuntimeState

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
		case dbSubcommandExport:
			return runDBExport(stdout, stderr, store, resolved, args[1:])
		case dbSubcommandImport:
			return runDBImport(stdout, stderr, store, resolved, args[1:])
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
	return backend.ResolveDatabaseEnvironment(store)
}

func runDBClient(stdout, stderr io.Writer, store backend.Store, resolved dbResolvedEnvironment, args []string) int {
	dispatchArgs, err := injectDatabaseConnectionArgs(store.RootDir, resolved, args)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	return runDispatch(stdout, stderr, append([]string{resolved.Database.Engine}, dispatchArgs...), store)
}

func runDBExport(stdout, stderr io.Writer, store backend.Store, resolved dbResolvedEnvironment, args []string) int {
	exportPath, databaseName, _, err := parseDatabaseTransferArgs("export", resolved, args)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	exportWriter, finalizeExport, exportPath, err := openDatabaseExportWriter(exportPath)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	dumpArgs, err := injectDatabaseConnectionArgsWithDatabase(store.RootDir, resolved, nil, "", false)
	if err != nil {
		_ = finalizeExport(false)
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	dumpArgs = append(dumpArgs, "--databases", databaseName, "--routines", "--events")

	dumpTarget, err := backend.ResolveDatabaseDumpTarget(store.EnvsDir, resolved.Database.Engine, resolved.Database.Version)
	if err != nil {
		_ = finalizeExport(false)
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	exitCode, err := executeTargetWithIO(exportWriter, stderr, nil, nil, dumpTarget, dumpArgs)
	success := err == nil && exitCode == 0
	finalizeErr := finalizeExport(success)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if exitCode != 0 {
		return exitCode
	}
	if finalizeErr != nil {
		fmt.Fprintf(stderr, "error: %v\n", finalizeErr)
		return 1
	}

	fmt.Fprintf(stdout, "Exported database dump for environment %q to %s.\n", resolved.Environment.Name, exportPath)
	return 0
}

func runDBImport(stdout, stderr io.Writer, store backend.Store, resolved dbResolvedEnvironment, args []string) int {
	importPath, databaseName, explicitDatabase, err := parseDatabaseTransferArgs("import", resolved, args)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	importReader, closeImport, importPath, err := openDatabaseImportReader(importPath)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	dispatchArgs, err := injectDatabaseConnectionArgsWithDatabase(store.RootDir, resolved, nil, databaseName, explicitDatabase)
	if err != nil {
		_ = closeImport()
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	target, err := store.ResolveTool(resolved.Database.Engine)
	if err != nil {
		_ = closeImport()
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	exitCode, err := executeTargetWithIO(stdout, stderr, importReader, nil, target, dispatchArgs)
	closeErr := closeImport()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if closeErr != nil {
		fmt.Fprintf(stderr, "error: close database import %s: %v\n", importPath, closeErr)
		return 1
	}
	if exitCode != 0 {
		return exitCode
	}

	fmt.Fprintf(stdout, "Imported database dump from %s into environment %q.\n", importPath, resolved.Environment.Name)
	return 0
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

	state, alreadyStopped, err := backend.StopManagedDatabase(store, resolved, dbRuntimeHooks())
	if alreadyStopped {
		fmt.Fprintf(stdout, "Database for environment %q is already stopped.\n", resolved.Environment.Name)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
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

	state, err := backend.LoadLiveManagedDatabaseStateForResolved(store.RootDir, resolved, pingDatabaseAddressFunc)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	address := backend.DatabaseAddress(backend.EffectiveDatabasePort(resolved.Database))
	if state != nil {
		address = backend.DatabaseAddress(state.Port)
		fmt.Fprintf(stdout, "Database for environment %q is running on %s (pid %d).\n", resolved.Environment.Name, address, state.PID)
		return 0
	}

	fmt.Fprintf(stdout, "Database for environment %q is stopped on %s.\n", resolved.Environment.Name, address)
	return 0
}

func ensureManagedDatabaseStarted(store backend.Store, resolved dbResolvedEnvironment) (dbRuntimeState, bool, error) {
	return backend.EnsureManagedDatabaseStarted(store, resolved, dbRuntimeHooks())
}

func injectDatabaseConnectionArgs(rootDir string, resolved dbResolvedEnvironment, args []string) ([]string, error) {
	cleanArgs, databaseName, explicitDatabase, err := extractDatabaseNameOverride(args)
	if err != nil {
		return nil, err
	}

	selectedDatabase := managedDatabaseName(resolved)
	if explicitDatabase {
		selectedDatabase = databaseName
	}

	return injectDatabaseConnectionArgsWithDatabase(rootDir, resolved, cleanArgs, selectedDatabase, explicitDatabase)
}

func injectDatabaseConnectionArgsWithDatabase(rootDir string, resolved dbResolvedEnvironment, args []string, databaseName string, explicitDatabase bool) ([]string, error) {
	hasDefaultsFile, hasHost, hasPort, hasSocket, hasProtocol, protocol, hasDatabase := databaseConnectionOverrides(args)
	if explicitDatabase && hasDatabase {
		return nil, fmt.Errorf("use either --db-name or native --database/-D, not both")
	}
	if hasDefaultsFile || hasSocket {
		return appendDatabaseConnectionArg(args, databaseName, explicitDatabase && !hasDatabase), nil
	}
	if hasProtocol && protocol != "" && !strings.EqualFold(protocol, "tcp") {
		return appendDatabaseConnectionArg(args, databaseName, explicitDatabase && !hasDatabase), nil
	}

	state, err := backend.LoadLiveManagedDatabaseStateForResolved(rootDir, resolved, pingDatabaseAddressFunc)
	if err != nil {
		return nil, err
	}
	if state != nil {
		credentialsPath := backend.DatabaseCredentialStatePath(rootDir, resolved.Environment.Name)
		credentials, credentialsErr := backend.LoadManagedDatabaseCredentials(credentialsPath)
		if errors.Is(credentialsErr, os.ErrNotExist) {
			return nil, fmt.Errorf("database credentials for environment %q are missing under %s; stop and restart the database to re-bootstrap them", resolved.Environment.Name, filepath.Dir(credentialsPath))
		}
		if credentialsErr != nil {
			return nil, credentialsErr
		}
		if strings.TrimSpace(credentials.User) == "" || strings.TrimSpace(credentials.Password) == "" {
			return nil, fmt.Errorf("database credentials for environment %q are incomplete under %s; stop and restart the database to re-bootstrap them", resolved.Environment.Name, filepath.Dir(credentialsPath))
		}
	} else {
		if _, credentialsErr := backend.EnsureManagedDatabaseCredentialAssets(rootDir, resolved); credentialsErr != nil {
			return nil, credentialsErr
		}
	}

	port := backend.EffectiveDatabasePort(resolved.Database)
	if state != nil && state.Port != 0 {
		port = state.Port
	}

	injected := make([]string, 0, len(args)+4)
	injected = append(injected, "--defaults-extra-file="+backend.DatabaseDefaultsFilePath(rootDir, resolved.Environment.Name))
	if !hasProtocol {
		injected = append(injected, "--protocol=tcp")
	}
	if !hasHost {
		injected = append(injected, "--host="+dbListenHost)
	}
	if !hasPort {
		injected = append(injected, "--port="+strconv.Itoa(port))
	}
	if !hasDatabase && strings.TrimSpace(databaseName) != "" {
		injected = append(injected, "--database="+databaseName)
	}

	return append(injected, args...), nil
}

func dbRuntimeHooks() backend.DatabaseRuntimeHooks {
	return backend.DatabaseRuntimeHooks{
		InitializeServer: initializeDatabaseServerFunc,
		StartServer:      startDatabaseServerFunc,
		StopServer:       stopDatabaseServerFunc,
		PingAddress:      pingDatabaseAddressFunc,
		Now:              dbNowFunc,
	}
}

func appendDatabaseConnectionArg(args []string, databaseName string, shouldAppend bool) []string {
	if !shouldAppend || strings.TrimSpace(databaseName) == "" {
		return args
	}

	return append(append([]string{}, args...), "--database="+databaseName)
}

func managedDatabaseName(resolved dbResolvedEnvironment) string {
	return strings.TrimSpace(resolved.Environment.Name)
}

func parseDatabaseTransferArgs(subcommand string, resolved dbResolvedEnvironment, args []string) (string, string, bool, error) {
	cleanArgs, databaseName, explicitDatabase, err := extractDatabaseNameOverride(args)
	if err != nil {
		return "", "", false, err
	}
	if len(cleanArgs) != 1 {
		return "", "", false, fmt.Errorf("db %s requires exactly one path argument", subcommand)
	}
	if !explicitDatabase {
		databaseName = managedDatabaseName(resolved)
	}

	return cleanArgs[0], databaseName, explicitDatabase, nil
}

func extractDatabaseNameOverride(args []string) ([]string, string, bool, error) {
	filtered := make([]string, 0, len(args))
	name := ""
	hasOverride := false

	for index := 0; index < len(args); index++ {
		argument := strings.TrimSpace(args[index])
		switch {
		case argument == "--db-name":
			if hasOverride {
				return nil, "", false, fmt.Errorf("--db-name can only be provided once")
			}
			if index+1 >= len(args) {
				return nil, "", false, fmt.Errorf("--db-name requires a value")
			}
			name = strings.TrimSpace(args[index+1])
			if name == "" {
				return nil, "", false, fmt.Errorf("--db-name requires a non-empty value")
			}
			hasOverride = true
			index++
		case strings.HasPrefix(argument, "--db-name="):
			if hasOverride {
				return nil, "", false, fmt.Errorf("--db-name can only be provided once")
			}
			name = strings.TrimSpace(strings.TrimPrefix(argument, "--db-name="))
			if name == "" {
				return nil, "", false, fmt.Errorf("--db-name requires a non-empty value")
			}
			hasOverride = true
		default:
			filtered = append(filtered, args[index])
		}
	}

	return filtered, name, hasOverride, nil
}

func databaseConnectionOverrides(args []string) (hasDefaultsFile, hasHost, hasPort, hasSocket, hasProtocol bool, protocol string, hasDatabase bool) {
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
		case argument == "--database" || argument == "-D":
			hasDatabase = true
			if index+1 < len(args) {
				index++
			}
		case strings.HasPrefix(argument, "--database="):
			hasDatabase = true
		case strings.HasPrefix(argument, "-D") && len(argument) > 2:
			hasDatabase = true
		}
	}

	return hasDefaultsFile, hasHost, hasPort, hasSocket, hasProtocol, protocol, hasDatabase
}

func databaseInitializeArgs(spec dbServerSpec) []string {
	return backend.DatabaseInitializeArgs(spec)
}

func openDatabaseImportReader(path string) (io.Reader, func() error, string, error) {
	resolvedPath, gzipped, err := resolveDatabaseDumpPath(path)
	if err != nil {
		return nil, nil, "", err
	}

	file, err := os.Open(resolvedPath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("open database import file %s: %w", resolvedPath, err)
	}
	if !gzipped {
		return file, file.Close, resolvedPath, nil
	}

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, nil, "", fmt.Errorf("open gzipped database import file %s: %w", resolvedPath, err)
	}

	return gzipReader, func() error {
		closeErr := gzipReader.Close()
		fileErr := file.Close()
		if closeErr != nil {
			return closeErr
		}
		return fileErr
	}, resolvedPath, nil
}

func openDatabaseExportWriter(path string) (io.Writer, func(bool) error, string, error) {
	resolvedPath, gzipped, err := resolveDatabaseDumpPath(path)
	if err != nil {
		return nil, nil, "", err
	}

	tempFile, err := os.CreateTemp(filepath.Dir(resolvedPath), filepath.Base(resolvedPath)+".tmp-*")
	if err != nil {
		return nil, nil, "", fmt.Errorf("create database export file %s: %w", resolvedPath, err)
	}

	var exportWriter io.Writer = tempFile
	var gzipWriter *gzip.Writer
	if gzipped {
		gzipWriter = gzip.NewWriter(tempFile)
		exportWriter = gzipWriter
	}

	finalize := func(success bool) error {
		var finalizeErr error
		if gzipWriter != nil {
			if err := gzipWriter.Close(); err != nil {
				finalizeErr = fmt.Errorf("close gzip database export %s: %w", resolvedPath, err)
			}
		}
		if err := tempFile.Close(); err != nil && finalizeErr == nil {
			finalizeErr = fmt.Errorf("close database export %s: %w", resolvedPath, err)
		}
		if !success || finalizeErr != nil {
			if err := os.Remove(tempFile.Name()); err != nil && !errors.Is(err, os.ErrNotExist) && finalizeErr == nil {
				finalizeErr = fmt.Errorf("remove incomplete database export %s: %w", resolvedPath, err)
			}
			return finalizeErr
		}

		if err := os.Remove(resolvedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(tempFile.Name())
			return fmt.Errorf("replace database export %s: %w", resolvedPath, err)
		}
		if err := os.Rename(tempFile.Name(), resolvedPath); err != nil {
			_ = os.Remove(tempFile.Name())
			return fmt.Errorf("finalize database export %s: %w", resolvedPath, err)
		}

		return nil
	}

	return exportWriter, finalize, resolvedPath, nil
}

func resolveDatabaseDumpPath(path string) (string, bool, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", false, fmt.Errorf("database dump path cannot be empty")
	}

	cleaned := filepath.Clean(trimmed)
	lowerPath := strings.ToLower(cleaned)
	switch {
	case strings.HasSuffix(lowerPath, ".sql.gz"):
		return cleaned, true, nil
	case strings.HasSuffix(lowerPath, ".sql"):
		return cleaned, false, nil
	default:
		return "", false, fmt.Errorf("database dump path %q must end with .sql or .sql.gz", path)
	}
}

func databaseServerInitialized(spec dbServerSpec) (bool, error) {
	return backend.DatabaseServerInitialized(spec)
}

func databaseAddress(port int) string {
	return backend.DatabaseAddress(port)
}

func legacyDatabaseDataPath(rootDir, environmentName string) string {
	return backend.LegacyDatabaseDataPath(rootDir, environmentName)
}

func databaseDataPath(rootDir, environmentName, engine, version string) string {
	return backend.DatabaseDataPath(rootDir, environmentName, engine, version)
}

func resolveDatabaseDataPath(rootDir string, resolved dbResolvedEnvironment) (string, error) {
	return backend.ResolveDatabaseDataPath(rootDir, resolved)
}

func databaseStatePath(rootDir, environmentName string) string {
	return backend.DatabaseStatePath(rootDir, environmentName)
}

func databaseCredentialStatePath(rootDir, environmentName string) string {
	return backend.DatabaseCredentialStatePath(rootDir, environmentName)
}

func databaseDefaultsFilePath(rootDir, environmentName string) string {
	return backend.DatabaseDefaultsFilePath(rootDir, environmentName)
}

func databaseBootstrapSQLPath(rootDir, environmentName string) string {
	return backend.DatabaseBootstrapSQLPath(rootDir, environmentName)
}

func loadLiveDatabaseState(rootDir, environmentName string) (*dbRuntimeState, error) {
	return backend.LoadLiveManagedDatabaseState(rootDir, environmentName, pingDatabaseAddressFunc)
}

func loadLiveDatabaseStateForResolved(rootDir string, resolved dbResolvedEnvironment) (*dbRuntimeState, error) {
	return backend.LoadLiveManagedDatabaseStateForResolved(rootDir, resolved, pingDatabaseAddressFunc)
}

func loadDatabaseState(path string) (*dbRuntimeState, error) {
	return backend.LoadManagedDatabaseState(path)
}

func loadDatabaseCredentials(path string) (dbManagedCredentials, error) {
	return backend.LoadManagedDatabaseCredentials(path)
}

func writeDatabaseState(path string, state dbRuntimeState) error {
	return backend.WriteManagedDatabaseState(path, state)
}
