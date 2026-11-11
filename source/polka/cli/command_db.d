module polka.cli.command_db;

// Managed database CLI command (`polka db`) and client/export/import/lifecycle
// handlers for MySQL, MariaDB, and PostgreSQL, including `.sql` / `.sql.gz`
// atomic dump streaming and connection flag injection.

import std.conv : to;
import std.datetime : Clock, SysTime, UTC;
import std.exception : collectException;
import std.file : exists, mkdirRecurse, remove, rename, tempDir;
import std.format : format;
import std.path : baseName, buildPath, dirName;
import std.stdio : File;
import std.string : endsWith, icmp, startsWith, strip, toLower;
import std.zlib : Compress, HeaderFormat, UnCompress, Z_FINISH;

import polka.backend : Store, TextWriter, tryCreateExclusiveFile, writeOut, writeOutf, writeOutln;
import polka.cli.command_dispatch : runDispatch;
import polka.cli.env_runtime : resolveRuntimeEnvironment;
import polka.cli.service_context : managedServiceContext;
import polka.cli.types :
    ByteReader,
    executeTargetWithIO,
    FileByteReader,
    isMissingFileError,
    writeError,
    writeErrorf;
import polka.service :
    currentEnvList,
    databaseAddress,
    databaseBootstrapSQLPath,
    databaseClientCommand,
    databaseCredentialStatePath,
    databaseDataPath,
    databaseDefaultsFilePath,
    databaseInitializeArgs,
    DatabaseListenHost,
    DatabaseRuntimeHooks,
    databaseServerInitialized,
    databaseStatePath,
    effectiveDatabasePort,
    ensureManagedDatabaseCredentialAssets,
    initializeDatabaseServer,
    legacyDatabaseDataPath,
    loadLiveManagedDatabaseState,
    loadLiveManagedDatabaseStateForResolved,
    loadManagedDatabaseCredentials,
    loadManagedDatabaseState,
    ManagedDatabaseCredentials,
    ManagedDatabaseRuntimeState,
    ManagedDatabaseServerSpec,
    ManagedDatabaseStartResult,
    ManagedDatabaseUserName,
    pingDatabaseAddress,
    resolveDatabaseDataPath,
    resolveDatabaseDumpTarget,
    resolveDatabaseEnvironment,
    ResolvedDatabaseEnvironment,
    serviceStartManagedDatabase = ensureManagedDatabaseStarted,
    serviceStopManagedDatabase = stopManagedDatabase,
    startDatabaseServer,
    stopDatabaseServer,
    writeManagedDatabaseState;
import polka.support.crypto : randomCryptoHex;
import polka.support.fs : cleanPath, currentOS;

enum string dbSubcommandClient = "client";
enum string dbSubcommandExport = "export";
enum string dbSubcommandImport = "import";
enum string dbSubcommandStart  = "start";
enum string dbSubcommandStop   = "stop";
enum string dbSubcommandStatus = "status";

alias dbListenHost      = DatabaseListenHost;
alias dbManagedUserName = ManagedDatabaseUserName;

alias dbResolvedEnvironment = ResolvedDatabaseEnvironment;
alias dbServerSpec          = ManagedDatabaseServerSpec;
alias dbStartResult         = ManagedDatabaseStartResult;
alias dbManagedCredentials  = ManagedDatabaseCredentials;
alias dbRuntimeState        = ManagedDatabaseRuntimeState;

/// Test seams mirroring `cli/command_db.go` globals for stubbing server lifecycle and clock.
void delegate(ManagedDatabaseServerSpec) initializeDatabaseServerFunc;
ManagedDatabaseStartResult delegate(ManagedDatabaseServerSpec) startDatabaseServerFunc;
void delegate(ManagedDatabaseRuntimeState) stopDatabaseServerFunc;
bool delegate(string) pingDatabaseAddressFunc;
SysTime delegate() dbNowFunc;

/// Invokes the configured `pingDatabaseAddressFunc` seam or defaults to `pingDatabaseAddress`.
bool callPingDatabaseAddress(string address) @trusted
{
    if (pingDatabaseAddressFunc !is null)
        return pingDatabaseAddressFunc(address);
    return pingDatabaseAddress(address);
}

/// Dispatches `polka db [subcommand|args...]`.
int runDB(TextWriter stdout, TextWriter stderr, ref Store store, const(string)[] args) @trusted
{
    dbResolvedEnvironment resolved;
    try
    {
        resolved = resolveDBEnvironment(store);
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    if (args.length > 0)
    {
        switch (args[0].strip().toLower())
        {
            case dbSubcommandClient:
                return runDBClient(stdout, stderr, store, resolved, args[1 .. $]);
            case dbSubcommandExport:
                return runDBExport(stdout, stderr, store, resolved, args[1 .. $]);
            case dbSubcommandImport:
                return runDBImport(stdout, stderr, store, resolved, args[1 .. $]);
            case dbSubcommandStart:
                return runDBStart(stdout, stderr, store, resolved, args[1 .. $]);
            case dbSubcommandStop:
                return runDBStop(stdout, stderr, store, resolved, args[1 .. $]);
            case dbSubcommandStatus:
                return runDBStatus(stdout, stderr, store, resolved, args[1 .. $]);
            default:
                break;
        }
    }

    return runDBClient(stdout, stderr, store, resolved, args);
}

/// Resolves and validates the active environment's managed database configuration.
dbResolvedEnvironment resolveDBEnvironment(ref Store store) @trusted
{
    auto current = store.current();
    if (current is null)
        throw new Exception("no active environment selected");
    return resolveDatabaseEnvironment(*current);
}

/// Connects to the managed database client (`mysql`/`mariadb`/`psql`) with managed credential flags injected.
int runDBClient(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    dbResolvedEnvironment resolved,
    const(string)[] args
) @trusted
{
    string[] dispatchArgs;
    try
    {
        dispatchArgs = injectDatabaseConnectionArgs(store.rootDir, resolved, args);
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    string[] fullArgs;
    fullArgs.reserve(dispatchArgs.length + 1);
    fullArgs ~= databaseClientCommand(resolved.database.engine);
    fullArgs ~= dispatchArgs;
    return runDispatch(stdout, stderr, fullArgs, store);
}

/// Streams a database dump (`mysqldump`/`mariadb-dump`/`pg_dump`) atomically to `.sql` or `.sql.gz`.
int runDBExport(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    dbResolvedEnvironment resolved,
    const(string)[] args
) @trusted
{
    string rawPath;
    string databaseName;
    bool explicitDatabase;
    try
    {
        rawPath = parseDatabaseTransferArgs("export", resolved, args, databaseName, explicitDatabase);
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    ExportWriterHandle exportHandle;
    try
    {
        exportHandle = openDatabaseExportWriter(rawPath);
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    string dumpDatabaseName = "";
    bool dumpDatabaseExplicit = false;
    if (icmp(resolved.database.engine, "postgresql") == 0)
    {
        dumpDatabaseName = databaseName;
        dumpDatabaseExplicit = true;
    }

    string[] dumpArgs;
    try
    {
        dumpArgs = injectDatabaseConnectionArgsWithDatabase(
            store.rootDir, resolved, null, dumpDatabaseName, dumpDatabaseExplicit
        );
    }
    catch (Exception e)
    {
        collectException(exportHandle.finalize(false));
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    if (icmp(resolved.database.engine, "postgresql") == 0)
        dumpArgs ~= ["--format=plain", "--no-owner", "--no-privileges"];
    else
        dumpArgs ~= ["--databases", databaseName, "--routines", "--events"];

    string dumpTarget;
    try
    {
        dumpTarget = resolveDatabaseDumpTarget(store.envsDir, resolved.database.engine, resolved.database.version_);
    }
    catch (Exception e)
    {
        collectException(exportHandle.finalize(false));
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    string[] env;
    try
    {
        env = resolveRuntimeEnvironment(currentOS(), currentEnvList(), store);
    }
    catch (Exception e)
    {
        collectException(exportHandle.finalize(false));
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    int exitCode = 0;
    Exception runErr;
    try
    {
        exitCode = executeTargetWithIO(exportHandle.writer, stderr, null, env, dumpTarget, dumpArgs);
    }
    catch (Exception e)
    {
        runErr = e;
    }

    bool success = runErr is null && exitCode == 0;
    Exception finalizeErr;
    try
    {
        exportHandle.finalize(success);
    }
    catch (Exception e)
    {
        finalizeErr = e;
    }

    if (runErr !is null)
    {
        writeErrorf(stderr, "%s", runErr.msg);
        return 1;
    }
    if (exitCode != 0)
        return exitCode;
    if (finalizeErr !is null)
    {
        writeErrorf(stderr, "%s", finalizeErr.msg);
        return 1;
    }

    writeOutf(stdout, "Exported database dump for environment %s to %s.\n",
        quoteForGoFmt(resolved.environment.name), exportHandle.resolvedPath);
    return 0;
}

/// Streams a `.sql` or `.sql.gz` dump into the managed database client.
int runDBImport(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    dbResolvedEnvironment resolved,
    const(string)[] args
) @trusted
{
    string rawPath;
    string databaseName;
    bool explicitDatabase;
    try
    {
        rawPath = parseDatabaseTransferArgs("import", resolved, args, databaseName, explicitDatabase);
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    ImportReaderHandle importHandle;
    try
    {
        importHandle = openDatabaseImportReader(rawPath);
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    string[] dispatchArgs;
    try
    {
        dispatchArgs = injectDatabaseConnectionArgsWithDatabase(
            store.rootDir, resolved, null, databaseName, explicitDatabase
        );
    }
    catch (Exception e)
    {
        collectException(importHandle.close());
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    if (icmp(resolved.database.engine, "postgresql") == 0)
        dispatchArgs ~= "--set=ON_ERROR_STOP=1";

    string target;
    try
    {
        target = store.resolveTool(resolved.database.engine);
    }
    catch (Exception e)
    {
        collectException(importHandle.close());
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    string[] env;
    try
    {
        env = resolveRuntimeEnvironment(currentOS(), currentEnvList(), store);
    }
    catch (Exception e)
    {
        collectException(importHandle.close());
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    int exitCode = 0;
    Exception runErr;
    try
    {
        exitCode = executeTargetWithIO(stdout, stderr, importHandle.reader, env, target, dispatchArgs);
    }
    catch (Exception e)
    {
        runErr = e;
    }

    Exception closeErr;
    try
    {
        importHandle.close();
    }
    catch (Exception e)
    {
        closeErr = e;
    }

    if (runErr !is null)
    {
        writeErrorf(stderr, "%s", runErr.msg);
        return 1;
    }
    if (closeErr !is null)
    {
        writeErrorf(stderr, "close database import %s: %s", importHandle.resolvedPath, closeErr.msg);
        return 1;
    }
    if (exitCode != 0)
        return exitCode;

    writeOutf(stdout, "Imported database dump from %s into environment %s.\n",
        importHandle.resolvedPath, quoteForGoFmt(resolved.environment.name));
    return 0;
}

/// Starts the configured database server in the background if not already running.
int runDBStart(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    dbResolvedEnvironment resolved,
    const(string)[] args
) @trusted
{
    if (args.length != 0)
    {
        writeError(stderr, "db start does not accept arguments");
        return 1;
    }

    dbRuntimeState state;
    bool alreadyRunning = false;
    try
    {
        state = ensureManagedDatabaseStarted(store, resolved, alreadyRunning);
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    if (alreadyRunning)
    {
        writeOutf(stdout, "Database for environment %s is already running on %s.\n",
            quoteForGoFmt(resolved.environment.name), databaseAddress(state.port));
        return 0;
    }

    writeOutf(stdout, "Started %s for environment %s on %s.\n",
        resolved.database.engine, quoteForGoFmt(resolved.environment.name), databaseAddress(state.port));
    return 0;
}

/// Stops the running database server for the active environment.
int runDBStop(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    dbResolvedEnvironment resolved,
    const(string)[] args
) @trusted
{
    if (args.length != 0)
    {
        writeError(stderr, "db stop does not accept arguments");
        return 1;
    }

    dbRuntimeState state;
    bool alreadyStopped = false;
    try
    {
        state = serviceStopManagedDatabase(
            managedServiceContext(store, resolved.environment, null),
            resolved,
            dbRuntimeHooks(),
            alreadyStopped
        );
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    if (alreadyStopped)
    {
        writeOutf(stdout, "Database for environment %s is already stopped.\n",
            quoteForGoFmt(resolved.environment.name));
        return 0;
    }

    writeOutf(stdout, "Stopped %s for environment %s.\n",
        state.engine, quoteForGoFmt(resolved.environment.name));
    return 0;
}

/// Prints the current database runtime status (running PID or stopped).
int runDBStatus(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    dbResolvedEnvironment resolved,
    const(string)[] args
) @trusted
{
    if (args.length != 0)
    {
        writeError(stderr, "db status does not accept arguments");
        return 1;
    }

    dbRuntimeState* state;
    try
    {
        state = loadLiveDatabaseStateForResolved(store.rootDir, resolved);
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    auto address = databaseAddress(effectiveDatabasePort(resolved.database));
    if (state !is null)
    {
        address = databaseAddress(state.port);
        writeOutf(stdout, "Database for environment %s is running on %s (pid %d).\n",
            quoteForGoFmt(resolved.environment.name), address, state.pid);
        return 0;
    }

    writeOutf(stdout, "Database for environment %s is stopped on %s.\n",
        quoteForGoFmt(resolved.environment.name), address);
    return 0;
}

/// Ensures the managed database for `resolved` is running, delegating via `dbRuntimeHooks()`.
dbRuntimeState ensureManagedDatabaseStarted(
    ref Store store,
    dbResolvedEnvironment resolved,
    out bool alreadyStarted
) @trusted
{
    return serviceStartManagedDatabase(
        managedServiceContext(store, resolved.environment, null),
        resolved,
        dbRuntimeHooks(),
        alreadyStarted
    );
}

/// Builds `DatabaseRuntimeHooks` wired to the CLI test seams (falling back to production services).
DatabaseRuntimeHooks dbRuntimeHooks() @trusted
{
    DatabaseRuntimeHooks hooks;
    hooks.initializeServer = initializeDatabaseServerFunc !is null
        ? initializeDatabaseServerFunc
        : (ManagedDatabaseServerSpec spec) { initializeDatabaseServer(spec); };
    hooks.startServer = startDatabaseServerFunc !is null
        ? startDatabaseServerFunc
        : (ManagedDatabaseServerSpec spec) => startDatabaseServer(spec);
    hooks.stopServer = stopDatabaseServerFunc !is null
        ? stopDatabaseServerFunc
        : (ManagedDatabaseRuntimeState st) { stopDatabaseServer(st); };
    hooks.pingAddress = pingDatabaseAddressFunc !is null
        ? pingDatabaseAddressFunc
        : (string addr) => pingDatabaseAddress(addr);
    hooks.now = dbNowFunc !is null
        ? dbNowFunc
        : () => Clock.currTime(UTC());
    return hooks;
}

/// Injects connection arguments and resolves `--db-name` overrides for `db` invocations.
string[] injectDatabaseConnectionArgs(
    string rootDir,
    dbResolvedEnvironment resolved,
    const(string)[] args
) @trusted
{
    string databaseName;
    bool explicitDatabase = false;
    auto cleanArgs = extractDatabaseNameOverride(args, databaseName, explicitDatabase);

    auto selectedDatabase = managedDatabaseName(resolved);
    if (explicitDatabase)
        selectedDatabase = databaseName;

    return injectDatabaseConnectionArgsWithDatabase(rootDir, resolved, cleanArgs, selectedDatabase, explicitDatabase);
}

/// Injects engine-appropriate connection and credential flags when not overridden by caller `args`.
string[] injectDatabaseConnectionArgsWithDatabase(
    string rootDir,
    dbResolvedEnvironment resolved,
    const(string)[] args,
    string databaseName,
    bool explicitDatabase
) @trusted
{
    if (icmp(resolved.database.engine.strip(), "postgresql") == 0)
        return injectPostgreSQLConnectionArgs(rootDir, resolved, args, databaseName, explicitDatabase);

    bool hasDefaultsFile, hasHost, hasPort, hasSocket, hasProtocol, hasDatabase;
    string protocol;
    databaseConnectionOverrides(args, hasDefaultsFile, hasHost, hasPort, hasSocket, hasProtocol, protocol, hasDatabase);

    if (explicitDatabase && hasDatabase)
        throw new Exception("use either --db-name or native --database/-D, not both");
    if (hasDefaultsFile || hasSocket)
        return appendDatabaseConnectionArg(args, databaseName, explicitDatabase && !hasDatabase);
    if (hasProtocol && protocol.length > 0 && icmp(protocol, "tcp") != 0)
        return appendDatabaseConnectionArg(args, databaseName, explicitDatabase && !hasDatabase);

    auto state = loadLiveDatabaseStateForResolved(rootDir, resolved);
    if (state !is null)
    {
        auto credentialsPath = databaseCredentialStatePath(rootDir, resolved.environment.name);
        dbManagedCredentials credentials;
        try
        {
            credentials = loadManagedDatabaseCredentials(credentialsPath);
        }
        catch (Exception e)
        {
            if (isMissingFileError(e))
            {
                throw new Exception(format(
                    `database credentials for environment %s are missing under %s; stop and restart the database to re-bootstrap them`,
                    quoteForGoFmt(resolved.environment.name), dirName(credentialsPath)
                ));
            }
            throw e;
        }
        if (credentials.user.strip().length == 0 || credentials.password.strip().length == 0)
        {
            throw new Exception(format(
                `database credentials for environment %s are incomplete under %s; stop and restart the database to re-bootstrap them`,
                quoteForGoFmt(resolved.environment.name), dirName(credentialsPath)
            ));
        }
    }
    else
    {
        ensureManagedDatabaseCredentialAssets(rootDir, resolved);
    }

    int port = effectiveDatabasePort(resolved.database);
    if (state !is null && state.port != 0)
        port = state.port;

    string[] injected;
    injected.reserve(args.length + 5);
    injected ~= "--defaults-extra-file=" ~ databaseDefaultsFilePath(rootDir, resolved.environment.name);
    if (!hasProtocol)
        injected ~= "--protocol=tcp";
    if (!hasHost)
        injected ~= "--host=" ~ dbListenHost;
    if (!hasPort)
        injected ~= "--port=" ~ to!string(port);
    if (!hasDatabase && databaseName.strip().length > 0)
        injected ~= "--database=" ~ databaseName;

    injected ~= args;
    return injected;
}

/// Injects PostgreSQL client connection flags (`--host`, `--port`, `--username`, `--dbname`).
string[] injectPostgreSQLConnectionArgs(
    string rootDir,
    dbResolvedEnvironment resolved,
    const(string)[] args,
    string databaseName,
    bool explicitDatabase
) @trusted
{
    bool hasHost, hasPort, hasUser, hasDatabase;
    postgreSQLConnectionOverrides(args, hasHost, hasPort, hasUser, hasDatabase);
    if (explicitDatabase && hasDatabase)
        throw new Exception("use either --db-name or native --dbname/-d, not both");

    auto state = loadLiveDatabaseStateForResolved(rootDir, resolved);
    auto credentials = ensureManagedDatabaseCredentialAssets(rootDir, resolved);
    int port = effectiveDatabasePort(resolved.database);
    if (state !is null && state.port != 0)
        port = state.port;

    string[] injected;
    injected.reserve(args.length + 4);
    if (!hasHost)
        injected ~= "--host=" ~ dbListenHost;
    if (!hasPort)
        injected ~= "--port=" ~ to!string(port);
    if (!hasUser)
        injected ~= "--username=" ~ credentials.user;
    if (!hasDatabase && databaseName.strip().length > 0)
        injected ~= "--dbname=" ~ databaseName;

    injected ~= args;
    return injected;
}

/// Scans `args` for PostgreSQL native connection override flags.
void postgreSQLConnectionOverrides(
    const(string)[] args,
    out bool hasHost,
    out bool hasPort,
    out bool hasUser,
    out bool hasDatabase
) @safe pure
{
    for (size_t i = 0; i < args.length; i++)
    {
        auto arg = args[i].strip();
        if (arg == "--host" || arg == "-h")
        {
            hasHost = true;
            i++;
        }
        else if (arg.startsWith("--host=") || (arg.startsWith("-h") && arg.length > 2))
            hasHost = true;
        else if (arg == "--port" || arg == "-p")
        {
            hasPort = true;
            i++;
        }
        else if (arg.startsWith("--port=") || (arg.startsWith("-p") && arg.length > 2))
            hasPort = true;
        else if (arg == "--username" || arg == "-U")
        {
            hasUser = true;
            i++;
        }
        else if (arg.startsWith("--username=") || (arg.startsWith("-U") && arg.length > 2))
            hasUser = true;
        else if (arg == "--dbname" || arg == "-d")
        {
            hasDatabase = true;
            i++;
        }
        else if (arg.startsWith("--dbname=") || (arg.startsWith("-d") && arg.length > 2))
            hasDatabase = true;
    }
}

/// Appends `--database=<databaseName>` when `shouldAppend` is true and non-empty.
string[] appendDatabaseConnectionArg(const(string)[] args, string databaseName, bool shouldAppend) @safe pure
{
    if (!shouldAppend || databaseName.strip().length == 0)
        return args.dup;
    auto res = args.dup;
    res ~= "--database=" ~ databaseName;
    return res;
}

/// Default managed database name equals the environment name.
string managedDatabaseName(dbResolvedEnvironment resolved) @safe pure
{
    return resolved.environment.name.strip();
}

/// Extracts and validates the single path argument and optional `--db-name` override for export/import.
string parseDatabaseTransferArgs(
    string subcommand,
    dbResolvedEnvironment resolved,
    const(string)[] args,
    out string databaseName,
    out bool explicitDatabase
) @safe
{
    string overrideName;
    auto cleanArgs = extractDatabaseNameOverride(args, overrideName, explicitDatabase);
    if (cleanArgs.length != 1)
        throw new Exception(format("db %s requires exactly one path argument", subcommand));

    databaseName = explicitDatabase ? overrideName : managedDatabaseName(resolved);
    return cleanArgs[0];
}

/// Strips `--db-name` / `--db-name=...` from `args`, returning filtered args and parsed name.
string[] extractDatabaseNameOverride(
    const(string)[] args,
    out string name,
    out bool hasOverride
) @safe
{
    string[] filtered;
    filtered.reserve(args.length);

    for (size_t i = 0; i < args.length; i++)
    {
        auto arg = args[i].strip();
        if (arg == "--db-name")
        {
            if (hasOverride)
                throw new Exception("--db-name can only be provided once");
            if (i + 1 >= args.length)
                throw new Exception("--db-name requires a value");
            name = args[i + 1].strip();
            if (name.length == 0)
                throw new Exception("--db-name requires a non-empty value");
            hasOverride = true;
            i++;
        }
        else if (arg.startsWith("--db-name="))
        {
            if (hasOverride)
                throw new Exception("--db-name can only be provided once");
            name = arg["--db-name=".length .. $].strip();
            if (name.length == 0)
                throw new Exception("--db-name requires a non-empty value");
            hasOverride = true;
        }
        else
        {
            filtered ~= args[i];
        }
    }

    return filtered;
}

/// Detects MySQL/MariaDB native connection/defaults overrides in `args`.
void databaseConnectionOverrides(
    const(string)[] args,
    out bool hasDefaultsFile,
    out bool hasHost,
    out bool hasPort,
    out bool hasSocket,
    out bool hasProtocol,
    out string protocol,
    out bool hasDatabase
) @safe pure
{
    for (size_t i = 0; i < args.length; i++)
    {
        auto arg = args[i].strip();
        if (arg == "--no-defaults")
        {
            hasDefaultsFile = true;
        }
        else if (arg == "--defaults-file" || arg == "--defaults-extra-file")
        {
            hasDefaultsFile = true;
            if (i + 1 < args.length)
                i++;
        }
        else if (arg.startsWith("--defaults-file=") || arg.startsWith("--defaults-extra-file="))
        {
            hasDefaultsFile = true;
        }
        else if (arg == "--host" || arg == "-h")
        {
            hasHost = true;
            if (i + 1 < args.length)
                i++;
        }
        else if (arg.startsWith("--host=") || (arg.startsWith("-h") && arg.length > 2))
        {
            hasHost = true;
        }
        else if (arg == "--port" || arg == "-P")
        {
            hasPort = true;
            if (i + 1 < args.length)
                i++;
        }
        else if (arg.startsWith("--port=") || (arg.startsWith("-P") && arg.length > 2))
        {
            hasPort = true;
        }
        else if (arg == "--socket" || arg == "-S")
        {
            hasSocket = true;
            if (i + 1 < args.length)
                i++;
        }
        else if (arg.startsWith("--socket=") || (arg.startsWith("-S") && arg.length > 2))
        {
            hasSocket = true;
        }
        else if (arg == "--protocol")
        {
            hasProtocol = true;
            if (i + 1 < args.length)
            {
                protocol = args[i + 1].strip();
                i++;
            }
        }
        else if (arg.startsWith("--protocol="))
        {
            hasProtocol = true;
            protocol = arg["--protocol=".length .. $].strip();
        }
        else if (arg == "--database" || arg == "-D")
        {
            hasDatabase = true;
            if (i + 1 < args.length)
                i++;
        }
        else if (arg.startsWith("--database=") || (arg.startsWith("-D") && arg.length > 2))
        {
            hasDatabase = true;
        }
    }
}

/// Pair returned by `openDatabaseImportReader`.
struct ImportReaderHandle
{
    ByteReader reader;
    void delegate() close;
    string resolvedPath;
}

/// Pair returned by `openDatabaseExportWriter`.
struct ExportWriterHandle
{
    TextWriter writer;
    void delegate(bool success) finalize;
    string resolvedPath;
}

private final class GzipFileByteReader : ByteReader
{
    private File file;
    private UnCompress decompressor;
    private ubyte[] pending;
    private bool rawEOF = false;
    private bool flushed = false;
    private bool closed = false;

    this(File f, UnCompress dec, ubyte[] initialData)
    {
        this.file = f;
        this.decompressor = dec;
        this.pending = initialData;
    }

    override ptrdiff_t read(ubyte[] buf) @trusted
    {
        if (buf.length == 0)
            return 0;

        while (pending.length == 0 && !rawEOF)
        {
            ubyte[4096] rawBuf;
            auto chunk = file.rawRead(rawBuf[]);
            if (chunk.length == 0)
            {
                rawEOF = true;
                if (!decompressor.empty)
                    throw new Exception("unexpected EOF in gzipped stream");
                break;
            }

            auto decoded = cast(const(ubyte)[]) decompressor.uncompress(chunk);
            if (decoded.length > 0)
                pending ~= decoded;
        }

        if (pending.length == 0)
            return 0;

        size_t n = buf.length < pending.length ? buf.length : pending.length;
        buf[0 .. n] = pending[0 .. n];
        if (n == pending.length)
            pending = null;
        else
            pending = pending[n .. $];
        return cast(ptrdiff_t) n;
    }

    void closeStream() @trusted
    {
        if (closed)
            return;
        closed = true;

        Exception flushErr;
        if (!flushed)
        {
            flushed = true;
            try
            {
                cast(void) decompressor.flush();
            }
            catch (Exception e)
            {
                flushErr = e;
            }
        }

        Exception fileErr;
        try
        {
            if (file.isOpen)
                file.close();
        }
        catch (Exception e)
        {
            fileErr = e;
        }

        if (flushErr !is null)
            throw flushErr;
        if (fileErr !is null)
            throw fileErr;
    }
}

private final class PlainFileTextWriter : TextWriter
{
    private File file;

    this(File f)
    {
        this.file = f;
    }

    override void write(const(char)[] chunk) @trusted
    {
        if (chunk.length == 0)
            return;
        file.rawWrite(chunk);
    }

    /// Flushes and releases the wrapped file handle.
    void closeStream() @trusted
    {
        if (file.isOpen)
        {
            file.flush();
            file.close();
        }
    }
}

private final class GzipFileTextWriter : TextWriter
{
    private File file;
    private Compress compressor;
    private bool wroteAny = false;
    private bool finished = false;

    this(File f)
    {
        this.file = f;
        this.compressor = new Compress(HeaderFormat.gzip);
    }

    override void write(const(char)[] chunk) @trusted
    {
        if (chunk.length == 0)
            return;
        wroteAny = true;
        auto compressed = cast(const(ubyte)[]) compressor.compress(cast(const(ubyte)[]) chunk);
        if (compressed.length > 0)
            file.rawWrite(compressed);
    }

    void finishGzip() @trusted
    {
        if (finished)
            return;
        finished = true;

        if (!wroteAny)
        {
            // `std.zlib.Compress.flush` returns null when `compress` was never
            // called with non-empty data; emit a valid empty RFC 1952 gzip frame.
            static immutable ubyte[20] emptyGzipFrame = [
                0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03,
                0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
            ];
            file.rawWrite(emptyGzipFrame[]);
            file.flush();
            return;
        }

        auto tail = cast(const(ubyte)[]) compressor.flush(Z_FINISH);
        if (tail.length > 0)
            file.rawWrite(tail);
        file.flush();
    }

    /// Releases the wrapped file handle after gzip finalization.
    void closeStream() @trusted
    {
        if (file.isOpen)
            file.close();
    }
}

/// Opens a `.sql` or `.sql.gz` import file for streaming decompression/reading.
/// Validates the gzip header on the initial read so corrupt files fail early.
ImportReaderHandle openDatabaseImportReader(string path) @trusted
{
    bool gzipped = false;
    auto resolvedPath = resolveDatabaseDumpPath(path, gzipped);

    File file;
    try
    {
        file = File(resolvedPath, "rb");
    }
    catch (Exception e)
    {
        throw new Exception(format("open database import file %s: %s", resolvedPath, e.msg));
    }

    if (!gzipped)
    {
        auto reader = new FileByteReader(file);
        // Drop local File reference so `reader.closeStream()` drops refcount to zero.
        file = File.init;
        return ImportReaderHandle(
            reader,
            () { reader.closeStream(); },
            resolvedPath
        );
    }

    try
    {
        auto decompressor = new UnCompress(HeaderFormat.gzip);
        ubyte[4096] rawBuf;
        auto firstChunk = file.rawRead(rawBuf[]);
        if (firstChunk.length == 0)
            throw new Exception("EOF");

        auto decoded = cast(const(ubyte)[]) decompressor.uncompress(firstChunk);
        auto gzReader = new GzipFileByteReader(file, decompressor, decoded.dup);
        file = File.init;
        return ImportReaderHandle(
            gzReader,
            () { gzReader.closeStream(); },
            resolvedPath
        );
    }
    catch (Exception e)
    {
        collectException(file.close());
        throw new Exception(format("open gzipped database import file %s: %s", resolvedPath, e.msg));
    }
}

/// Opens a temp `.sql` or `.sql.gz` export writer and returns a finalizer that
/// flushes, closes, and atomically replaces `resolvedPath` on `success`.
ExportWriterHandle openDatabaseExportWriter(string path) @trusted
{
    bool gzipped = false;
    auto resolvedPath = resolveDatabaseDumpPath(path, gzipped);

    auto parentDir = dirName(resolvedPath);
    string tempPath;
    File tempFile;
    try
    {
        mkdirRecurse(parentDir);
        bool opened = false;
        foreach (_; 0 .. 16)
        {
            tempPath = buildPath(parentDir, baseName(resolvedPath) ~ ".tmp-" ~ randomCryptoHex(8));
            try
            {
                if (tryCreateExclusiveFile(tempPath, null))
                {
                    tempFile = File(tempPath, "wb");
                    opened = true;
                    break;
                }
            }
            catch (Exception) {}
        }
        if (!opened)
            throw new Exception("failed to allocate unique temporary export file");
    }
    catch (Exception e)
    {
        throw new Exception(format("create database export file %s: %s", resolvedPath, e.msg));
    }

    TextWriter exportWriter;
    PlainFileTextWriter plainWriter;
    GzipFileTextWriter gzipWriter;
    if (gzipped)
    {
        gzipWriter = new GzipFileTextWriter(tempFile);
        exportWriter = gzipWriter;
    }
    else
    {
        plainWriter = new PlainFileTextWriter(tempFile);
        exportWriter = plainWriter;
    }

    void finalize(bool success) @trusted
    {
        Exception finalizeErr;
        if (gzipWriter !is null)
        {
            try
            {
                gzipWriter.finishGzip();
            }
            catch (Exception e)
            {
                finalizeErr = new Exception(format("close gzip database export %s: %s", resolvedPath, e.msg));
            }
            try
            {
                gzipWriter.closeStream();
            }
            catch (Exception e)
            {
                if (finalizeErr is null)
                    finalizeErr = new Exception(format("close database export %s: %s", resolvedPath, e.msg));
            }
        }
        if (plainWriter !is null)
        {
            try
            {
                plainWriter.closeStream();
            }
            catch (Exception e)
            {
                if (finalizeErr is null)
                    finalizeErr = new Exception(format("close database export %s: %s", resolvedPath, e.msg));
            }
        }
        try
        {
            if (tempFile.isOpen)
                tempFile.close();
        }
        catch (Exception e)
        {
            if (finalizeErr is null)
                finalizeErr = new Exception(format("close database export %s: %s", resolvedPath, e.msg));
        }

        if (!success || finalizeErr !is null)
        {
            try
            {
                if (exists(tempPath))
                    remove(tempPath);
            }
            catch (Exception e)
            {
                if (!isMissingFileError(e) && finalizeErr is null)
                    finalizeErr = new Exception(format("remove incomplete database export %s: %s", resolvedPath, e.msg));
            }
            if (finalizeErr !is null)
                throw finalizeErr;
            return;
        }

        try
        {
            if (exists(resolvedPath))
                remove(resolvedPath);
        }
        catch (Exception e)
        {
            if (!isMissingFileError(e))
            {
                collectException(remove(tempPath));
                throw new Exception(format("replace database export %s: %s", resolvedPath, e.msg));
            }
        }

        try
        {
            rename(tempPath, resolvedPath);
        }
        catch (Exception e)
        {
            collectException(remove(tempPath));
            throw new Exception(format("finalize database export %s: %s", resolvedPath, e.msg));
        }
    }

    return ExportWriterHandle(exportWriter, &finalize, resolvedPath);
}

/// Validates and normalizes a `.sql` or `.sql.gz` dump path.
string resolveDatabaseDumpPath(string path, out bool gzipped) @safe
{
    auto trimmed = path.strip();
    if (trimmed.length == 0)
        throw new Exception("database dump path cannot be empty");

    auto cleaned = cleanPath(trimmed);
    auto lowerPath = cleaned.toLower();
    if (lowerPath.endsWith(".sql.gz"))
    {
        gzipped = true;
        return cleaned;
    }
    if (lowerPath.endsWith(".sql"))
    {
        gzipped = false;
        return cleaned;
    }

    throw new Exception(format(`database dump path %s must end with .sql or .sql.gz`, quoteForGoFmt(path)));
}

/// Loads live managed database runtime state for `environmentName`.
dbRuntimeState* loadLiveDatabaseState(string rootDir, string environmentName) @trusted
{
    return loadLiveManagedDatabaseState(rootDir, environmentName, (string addr) => callPingDatabaseAddress(addr));
}

/// Loads live managed database runtime state for `resolved`, enforcing engine/version/port match.
dbRuntimeState* loadLiveDatabaseStateForResolved(string rootDir, dbResolvedEnvironment resolved) @trusted
{
    return loadLiveManagedDatabaseStateForResolved(rootDir, resolved, (string addr) => callPingDatabaseAddress(addr));
}

/// Decodes a persisted database runtime state file.
dbRuntimeState* loadDatabaseState(string path) @trusted
{
    return loadManagedDatabaseState(path);
}

/// Decodes persisted database credentials from JSON.
dbManagedCredentials loadDatabaseCredentials(string path) @trusted
{
    return loadManagedDatabaseCredentials(path);
}

/// Persists database runtime state atomically to `path`.
void writeDatabaseState(string path, dbRuntimeState state) @trusted
{
    writeManagedDatabaseState(path, state);
}

private string quoteForGoFmt(string s) @safe pure
{
    import polka.cli.types : quoteGoString;
    return quoteGoString(s);
}

unittest
{
    import std.exception : assertThrown;
    import polka.backend : DatabaseConfig, Environment;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    // 1. TestPostgreSQLConnectionOverrideDetection & TestInjectPostgreSQLConnectionArgs
    {
        auto oldPing = pingDatabaseAddressFunc;
        pingDatabaseAddressFunc = (string) => false;
        scope (exit) pingDatabaseAddressFunc = oldPing;

        auto root = createTempDir("polka-db-pg-");
        scope (exit) rmdirRecurseQuiet(root);

        Environment env;
        env.name = "demo";
        auto dbCfg = new DatabaseConfig();
        dbCfg.engine = "postgresql";
        dbCfg.version_ = "17";
        auto resolved = dbResolvedEnvironment(env, dbCfg);

        auto args = injectDatabaseConnectionArgs(root, resolved, null);
        assert(args == ["--host=127.0.0.1", "--port=5432", "--username=polka", "--dbname=demo"]);

        auto overrides = ["-hremote", "-p5544", "-Uadmin", "-dother"];
        args = injectDatabaseConnectionArgs(root, resolved, overrides);
        assert(args == overrides);

        bool hasHost, hasPort, hasUser, hasDB;
        postgreSQLConnectionOverrides(["--host", "remote", "--port=5544", "-Uadmin", "--dbname", "demo"],
            hasHost, hasPort, hasUser, hasDB);
        assert(hasHost && hasPort && hasUser && hasDB);
    }

    // 2. Gzip streaming export/import roundtrip (including empty gzip stream) + corrupt gzip error check
    {
        auto dir = createTempDir("polka-db-gz-");
        scope (exit) rmdirRecurseQuiet(dir);

        auto gzPath = buildPath(dir, "dump.sql.gz");
        auto exp = openDatabaseExportWriter(gzPath);
        exp.writer.write("CREATE TABLE test (id INT);\n");
        exp.writer.write("INSERT INTO test VALUES (1);\n");
        exp.finalize(true);

        auto imp = openDatabaseImportReader(gzPath);
        ubyte[128] buf;
        string decoded;
        while (true)
        {
            auto n = imp.reader.read(buf[]);
            if (n <= 0)
                break;
            decoded ~= cast(const(char)[]) buf[0 .. cast(size_t) n];
        }
        imp.close();
        assert(decoded == "CREATE TABLE test (id INT);\nINSERT INTO test VALUES (1);\n");

        // Empty gzip stream roundtrip
        auto emptyGzPath = buildPath(dir, "empty.sql.gz");
        auto emptyExp = openDatabaseExportWriter(emptyGzPath);
        emptyExp.finalize(true);
        auto emptyImp = openDatabaseImportReader(emptyGzPath);
        assert(emptyImp.reader.read(buf[]) == 0);
        emptyImp.close();

        // Corrupt gzip file must fail inside openDatabaseImportReader
        auto badGzPath = buildPath(dir, "bad.sql.gz");
        import std.file : write;
        write(badGzPath, "not gzip data at all");
        assertThrown!Exception(openDatabaseImportReader(badGzPath));
    }
}
