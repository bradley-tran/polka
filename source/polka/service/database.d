module polka.service.database;

import core.thread : Thread;
import core.time : Duration, dur;
import std.algorithm : startsWith;
import std.conv : to;
import std.datetime : Clock, SysTime, UTC;
import std.file : dirEntries, exists, isDir, mkdirRecurse, readText, remove, rmdirRecurse, SpanMode;
import std.format : format;
import std.json : JSONType, JSONValue, parseJSON;
import std.path : buildPath, dirName;
import std.stdio : File;
import std.string : icmp, replace, strip, toLower;

import polka.config;
import polka.service.context;
import polka.service.process;
import polka.support.crypto : randomCryptoHex;
import polka.support.fs : currentOS, isSymlinkPath, octal, setFileMode, writeTextAtomic;
import polka.support.net : joinHostPort, pingTCPAddress;
import polka.tools.cache : jsonLongField, jsonStringField;

enum string DatabaseListenHost = "127.0.0.1";
enum int DefaultDatabasePort = 3306;
enum int DefaultPostgreSQLPort = 5432;
enum Duration managedDatabasePollInterval = dur!"msecs"(200);
enum Duration managedDatabaseStartupTimeout = dur!"seconds"(10);
enum Duration managedDatabaseShutdownTimeout = dur!"seconds"(5);
enum string managedDatabaseStateDirectory = "run";
enum string managedDatabaseStateSubdirectory = "db";
enum string managedDatabaseDataDirectory = "data";
enum string managedDatabaseDataSubdirectory = "db";
enum string managedDatabaseSecretDirectory = "secrets";
enum string managedDatabaseSecretSubdirectory = "db";
enum string ManagedDatabaseUserName = "polka";

/// Validated resolved environment coupled with its non-null database configuration.
struct ResolvedDatabaseEnvironment
{
    Environment environment;
    DatabaseConfig* database;
}

/// Resolved executable paths, secrets, data directory, and runtime parameters
/// required to initialize and start a managed database instance.
struct ManagedDatabaseServerSpec
{
    string environmentName;
    string engine;
    string version_;
    string installDir;
    string target;
    string initializeTarget;
    string adminTarget;
    string createTarget;
    string dataDir;
    string logPath;
    string defaultsFile;
    string bootstrapSQLFile;
    string passwordFile;
    string bootstrapMarker;
    string databaseName;
    int port = 0;
}

/// Result returned when starting a managed database process.
struct ManagedDatabaseStartResult
{
    int pid = 0;
}

/// Generated/loaded credentials persisted for one environment's managed database.
struct ManagedDatabaseCredentials
{
    string environmentName;
    string engine;
    string version_;
    string databaseName;
    string user;
    string password;
    int port = 0;
}

/// Persisted runtime state for one live managed database server.
struct ManagedDatabaseRuntimeState
{
    string environmentName;
    string engine;
    string version_;
    int port = 0;
    int pid = 0;
    string dataDir;
    string logPath;
    string adminTarget;
    string defaultsFile;
    SysTime startedAt;

    bool opEquals(const ManagedDatabaseRuntimeState other) const @safe pure nothrow
    {
        return environmentName == other.environmentName &&
            engine == other.engine &&
            version_ == other.version_ &&
            port == other.port &&
            pid == other.pid &&
            dataDir == other.dataDir &&
            logPath == other.logPath &&
            adminTarget == other.adminTarget &&
            defaultsFile == other.defaultsFile &&
            startedAt == other.startedAt;
    }
}

/// Dependency-injection hooks allowing tests to stub database side effects.
struct DatabaseRuntimeHooks
{
    void delegate(ManagedDatabaseServerSpec) initializeServer;
    ManagedDatabaseStartResult delegate(ManagedDatabaseServerSpec) startServer;
    void delegate(ManagedDatabaseRuntimeState) stopServer;
    bool delegate(string) pingAddress;
    SysTime delegate() now;

    /// Returns a copy with nil hooks populated by real production implementations.
    DatabaseRuntimeHooks withDefaults() const @trusted
    {
        DatabaseRuntimeHooks copy = this;
        if (copy.initializeServer is null)
            copy.initializeServer = (ManagedDatabaseServerSpec spec) { initializeDatabaseServer(spec); };
        if (copy.startServer is null)
            copy.startServer = (ManagedDatabaseServerSpec spec) => startDatabaseServer(spec);
        if (copy.stopServer is null)
            copy.stopServer = (ManagedDatabaseRuntimeState st) { stopDatabaseServer(st); };
        if (copy.pingAddress is null)
            copy.pingAddress = (string addr) => pingDatabaseAddress(addr);
        if (copy.now is null)
            copy.now = () => Clock.currTime(UTC());
        return copy;
    }
}

/// Validates and returns the managed database selected by an already-resolved environment.
ResolvedDatabaseEnvironment resolveDatabaseEnvironment(Environment environment) @safe
{
    if (environment.database is null || environment.database.engine.strip().length == 0)
        throw new Exception(format(`environment "%s" does not define a database engine`, environment.name));

    return ResolvedDatabaseEnvironment(environment, environment.database);
}

/// Ensures the managed database for `resolved` is running, returning `(state, alreadyStarted)`.
ManagedDatabaseRuntimeState ensureManagedDatabaseStarted(
    ServiceContext ctx,
    ResolvedDatabaseEnvironment resolved,
    DatabaseRuntimeHooks hooks,
    out bool alreadyStarted
) @trusted
{
    hooks = hooks.withDefaults();

    auto statePath = databaseStatePath(ctx.rootDir, resolved.environment.name);
    auto liveState = loadLiveManagedDatabaseStateForResolved(ctx.rootDir, resolved, hooks.pingAddress);
    if (liveState !is null)
    {
        alreadyStarted = true;
        return *liveState;
    }

    auto spec = buildManagedDatabaseServerSpec(ctx, resolved);
    hooks.initializeServer(spec);
    auto result = hooks.startServer(spec);

    ManagedDatabaseRuntimeState startedState;
    startedState.environmentName = resolved.environment.name;
    startedState.engine = resolved.database.engine;
    startedState.version_ = resolved.database.version_;
    startedState.port = spec.port;
    startedState.pid = result.pid;
    startedState.dataDir = spec.dataDir;
    startedState.logPath = spec.logPath;
    startedState.adminTarget = spec.adminTarget;
    startedState.defaultsFile = spec.defaultsFile;
    startedState.startedAt = hooks.now().toUTC();

    try
    {
        writeManagedDatabaseState(statePath, startedState);
    }
    catch (Exception e)
    {
        try
        {
            hooks.stopServer(startedState);
        }
        catch (Exception) {} // Best-effort cleanup if state persistence fails.
        throw e;
    }

    alreadyStarted = false;
    return startedState;
}

/// Stops a managed database for `resolved` if currently live.
ManagedDatabaseRuntimeState stopManagedDatabase(
    ServiceContext ctx,
    ResolvedDatabaseEnvironment resolved,
    DatabaseRuntimeHooks hooks,
    out bool alreadyStopped
) @trusted
{
    hooks = hooks.withDefaults();

    auto statePath = databaseStatePath(ctx.rootDir, resolved.environment.name);
    auto state = loadLiveManagedDatabaseState(ctx.rootDir, resolved.environment.name, hooks.pingAddress);
    if (state is null)
    {
        alreadyStopped = true;
        return ManagedDatabaseRuntimeState.init;
    }

    if (state.adminTarget.strip().length == 0 || state.defaultsFile.strip().length == 0)
    {
        auto spec = buildManagedDatabaseServerSpec(ctx, resolved);
        if (state.adminTarget.strip().length == 0)
            state.adminTarget = spec.adminTarget;
        if (state.defaultsFile.strip().length == 0)
            state.defaultsFile = spec.defaultsFile;
    }

    hooks.stopServer(*state);
    removeDatabaseStateFile(statePath);

    alreadyStopped = false;
    return *state;
}

/// Stops a live database state without requiring the current config to still define the database service.
ManagedDatabaseRuntimeState stopManagedDatabaseForEnvironment(
    ServiceContext ctx,
    DatabaseRuntimeHooks hooks,
    out bool alreadyStopped
) @trusted
{
    hooks = hooks.withDefaults();

    auto statePath = databaseStatePath(ctx.rootDir, ctx.environment.name);
    auto state = loadLiveManagedDatabaseState(ctx.rootDir, ctx.environment.name, hooks.pingAddress);
    if (state is null)
    {
        alreadyStopped = true;
        return ManagedDatabaseRuntimeState.init;
    }

    if (state.adminTarget.strip().length == 0 || state.defaultsFile.strip().length == 0)
    {
        if (ctx.environment.database is null || ctx.environment.database.engine.strip().length == 0)
            throw new Exception(format(`database state for "%s" is missing shutdown fields and the current environment no longer defines a database`, ctx.environment.name));

        auto spec = buildManagedDatabaseServerSpec(ctx, ResolvedDatabaseEnvironment(ctx.environment, ctx.environment.database));
        if (state.adminTarget.strip().length == 0)
            state.adminTarget = spec.adminTarget;
        if (state.defaultsFile.strip().length == 0)
            state.defaultsFile = spec.defaultsFile;
    }

    hooks.stopServer(*state);
    removeDatabaseStateFile(statePath);

    alreadyStopped = false;
    return *state;
}

private void removeDatabaseStateFile(string path) @trusted
{
    if (!exists(path) && !isSymlinkPath(path))
        return;
    try
    {
        remove(path);
    }
    catch (Exception e)
    {
        throw new Exception("remove database state: " ~ e.msg);
    }
}

/// Ensures credential JSON and engine-specific secret helper files (`pgpass`/password or
/// `defaults.cnf`/bootstrap SQL) exist and stay synchronized with `resolved`.
ManagedDatabaseCredentials ensureManagedDatabaseCredentialAssets(string rootDir, ResolvedDatabaseEnvironment resolved) @trusted
{
    auto path = databaseCredentialStatePath(rootDir, resolved.environment.name);
    ManagedDatabaseCredentials credentials;
    if (!exists(path) && !isSymlinkPath(path))
    {
        auto password = generateDatabasePassword();
        credentials.environmentName = resolved.environment.name;
        credentials.engine = resolved.database.engine;
        credentials.version_ = resolved.database.version_;
        credentials.databaseName = managedDatabaseName(resolved);
        credentials.user = ManagedDatabaseUserName;
        credentials.password = password;
        credentials.port = effectiveDatabasePort(resolved.database);
    }
    else
    {
        credentials = loadManagedDatabaseCredentials(path);
    }

    if (credentials.user.strip().length == 0)
        credentials.user = ManagedDatabaseUserName;
    if (credentials.password.strip().length == 0)
        credentials.password = generateDatabasePassword();

    credentials.environmentName = resolved.environment.name;
    credentials.engine = resolved.database.engine;
    credentials.version_ = resolved.database.version_;
    credentials.databaseName = managedDatabaseName(resolved);
    credentials.port = effectiveDatabasePort(resolved.database);

    writeManagedDatabaseCredentials(path, credentials);
    if (icmp(resolved.database.engine, toolPostgreSQL) == 0)
    {
        writeManagedDatabasePasswordFile(databasePasswordFilePath(rootDir, resolved.environment.name), credentials);
        writeManagedDatabasePGPassFile(databasePGPassFilePath(rootDir, resolved.environment.name), credentials);
    }
    else
    {
        writeManagedDatabaseDefaultsFile(databaseDefaultsFilePath(rootDir, resolved.environment.name), credentials);
        writeManagedDatabaseBootstrapSQLFile(databaseBootstrapSQLPath(rootDir, resolved.environment.name), credentials);
    }

    return credentials;
}

/// Resolves the installed dump utility binary (`mysqldump`, `mariadb-dump`, or `pg_dump`).
string resolveDatabaseDumpTarget(string envsDir, string engine, string version_) @trusted
{
    auto installDir = buildPath(envsDir, engine, version_);
    return resolveDatabaseToolTarget(
        installDir,
        engine,
        &databaseDumpCandidates,
        `%s dump utility version "%s" is not installed under %s`,
        envsDir,
        version_
    );
}

/// Initializes a database data directory on first run (re-initializing incomplete directories).
void initializeDatabaseServer(ManagedDatabaseServerSpec spec) @trusted
{
    if (databaseServerInitialized(spec))
        return;

    if (databaseDataInitialized(spec.dataDir))
        resetDatabaseDataDir(spec.dataDir);

    if (!exists(spec.dataDir))
    {
        try
        {
            mkdirRecurse(spec.dataDir);
        }
        catch (Exception e)
        {
            throw new Exception("create database data directory: " ~ e.msg);
        }
    }

    auto logFile = openDatabaseLog(spec.logPath);
    scope (exit)
    {
        if (logFile.isOpen)
            logFile.close();
    }

    auto initializeTarget = spec.target;
    if (spec.engine == toolPostgreSQL)
        initializeTarget = spec.initializeTarget;
    else if (spec.engine == toolMariaDB)
        initializeTarget = resolveDatabaseBootstrapTarget(spec.installDir, spec.engine);

    string workDir = spec.engine == toolMariaDB ? spec.dataDir : null;
    string unused;
    auto status = runServiceCommandSync(
        initializeTarget,
        databaseInitializeArgs(spec),
        unused,
        null,
        &logFile,
        null,
        workDir
    );
    if (status != 0)
        throw new Exception(format("initialize %s data directory: exit status %d (see %s)", spec.engine, status, spec.logPath));

    if (spec.engine == toolPostgreSQL)
    {
        try
        {
            writeTextAtomic(spec.bootstrapMarker, "pending\n", octal!600);
        }
        catch (Exception e)
        {
            throw new Exception("write postgresql bootstrap marker: " ~ e.msg);
        }
    }
}

/// Starts a managed database daemon and waits until it listens on its configured port.
ManagedDatabaseStartResult startDatabaseServer(ManagedDatabaseServerSpec spec) @trusted
{
    auto logFile = openDatabaseLog(spec.logPath);
    scope (exit)
    {
        if (logFile.isOpen)
            logFile.close();
    }

    auto address = databaseAddress(spec.port);
    if (pingDatabaseAddress(address))
        throw new Exception(format("%s server cannot start because %s is already in use", spec.engine, address));

    int pid = 0;
    try
    {
        pid = spawnDetachedServiceProcess(spec.target, databaseStartArgs(spec), logFile);
    }
    catch (Exception e)
    {
        throw new Exception(format("start %s server: %s", spec.engine, e.msg));
    }

    auto deadline = Clock.currTime() + managedDatabaseStartupTimeout;
    while (Clock.currTime() < deadline)
    {
        if (pingDatabaseAddress(address))
        {
            if (spec.engine == toolPostgreSQL)
            {
                try
                {
                    bootstrapPostgreSQLDatabase(spec, logFile);
                }
                catch (Exception e)
                {
                    try { stopServicePID(pid); } catch (Exception) {} // Best-effort teardown after bootstrap failure.
                    throw e;
                }
            }
            return ManagedDatabaseStartResult(pid);
        }

        Thread.sleep(managedDatabasePollInterval);
    }

    try { stopServicePID(pid); } catch (Exception) {} // Best-effort teardown after startup timeout.
    throw new Exception(format("%s server did not start listening on %s within %s (see %s)", spec.engine, address, managedDatabaseStartupTimeout, spec.logPath));
}

private void bootstrapPostgreSQLDatabase(ManagedDatabaseServerSpec spec, ref File logFile) @trusted
{
    if (!exists(spec.bootstrapMarker) && !isSymlinkPath(spec.bootstrapMarker))
        return;

    auto args = [
        "--host=" ~ DatabaseListenHost,
        "--port=" ~ to!string(spec.port),
        "--username=" ~ ManagedDatabaseUserName,
        "--maintenance-db=postgres",
        "--encoding=UTF8",
        spec.databaseName,
    ];
    auto envList = setEnvironmentSliceValue(currentEnvList(), "PGPASSFILE", spec.defaultsFile);
    string unused;
    auto status = runServiceCommandSync(spec.createTarget, args, unused, null, &logFile, envList);
    if (status != 0)
        throw new Exception(format(`create postgresql database "%s": exit status %d (see %s)`, spec.databaseName, status, spec.logPath));

    if (exists(spec.bootstrapMarker) || isSymlinkPath(spec.bootstrapMarker))
    {
        try
        {
            remove(spec.bootstrapMarker);
        }
        catch (Exception e)
        {
            throw new Exception("remove postgresql bootstrap marker: " ~ e.msg);
        }
    }
}

/// Gracefully shuts down a managed database using its engine admin utility.
void stopDatabaseServer(ManagedDatabaseRuntimeState state) @trusted
{
    auto address = databaseAddress(state.port);
    if (!pingDatabaseAddress(address))
        return;
    if (state.adminTarget.strip().length == 0)
        throw new Exception(format(`database state for "%s" does not include an admin target`, state.environmentName));
    if (state.engine != toolPostgreSQL && state.defaultsFile.strip().length == 0)
        throw new Exception(format(`database state for "%s" does not include a defaults file`, state.environmentName));

    auto logFile = openDatabaseLog(state.logPath);
    scope (exit)
    {
        if (logFile.isOpen)
            logFile.close();
    }

    string[] args;
    if (state.engine == toolPostgreSQL)
    {
        args = [
            "-D", state.dataDir, "stop", "-m", "fast", "-w",
            "-t", to!string(managedDatabaseShutdownTimeout.total!"seconds")
        ];
    }
    else
    {
        args = ["--defaults-extra-file=" ~ state.defaultsFile, "shutdown"];
    }

    string unused;
    auto status = runServiceCommandSync(state.adminTarget, args, unused, null, &logFile);
    if (status != 0)
        throw new Exception(format("shutdown %s server: exit status %d (see %s)", state.engine, status, state.logPath));

    auto deadline = Clock.currTime() + managedDatabaseShutdownTimeout;
    while (Clock.currTime() < deadline)
    {
        if (!pingDatabaseAddress(address))
            return;
        Thread.sleep(managedDatabasePollInterval);
    }

    throw new Exception(format("%s server did not stop listening on %s within %s", state.engine, address, managedDatabaseShutdownTimeout));
}

/// Builds engine-specific initialization arguments (`initdb`, `mariadb-install-db`, or `mysqld --initialize-insecure`).
string[] databaseInitializeArgs(ManagedDatabaseServerSpec spec) @safe pure
{
    if (spec.engine == toolPostgreSQL)
    {
        return [
            "--pgdata=" ~ spec.dataDir,
            "--username=" ~ ManagedDatabaseUserName,
            "--pwfile=" ~ spec.passwordFile,
            "--auth-host=scram-sha-256",
            "--auth-local=trust",
            "--encoding=UTF8",
        ];
    }
    if (spec.engine == toolMariaDB)
    {
        return [
            "--datadir=" ~ spec.dataDir,
            "--port=" ~ to!string(spec.port),
        ];
    }

    return [
        "--initialize-insecure",
        "--basedir=" ~ spec.installDir,
        "--datadir=" ~ spec.dataDir,
    ];
}

/// Builds engine-specific server launch arguments.
string[] databaseStartArgs(ManagedDatabaseServerSpec spec) @safe
{
    if (spec.engine == toolPostgreSQL)
    {
        return [
            "-D", spec.dataDir,
            "-h", DatabaseListenHost,
            "-p", to!string(spec.port),
        ];
    }

    auto args = [
        "--basedir=" ~ spec.installDir,
        "--datadir=" ~ spec.dataDir,
        "--port=" ~ to!string(spec.port),
        "--bind-address=" ~ DatabaseListenHost,
        "--init-file=" ~ spec.bootstrapSQLFile,
        "--log-error=" ~ spec.logPath,
    ];
    if (currentOS() == "windows")
        args ~= "--console";
    return args;
}

/// Reports whether the database data directory already contains valid engine system files.
bool databaseServerInitialized(ManagedDatabaseServerSpec spec) @trusted
{
    if (!databaseDataInitialized(spec.dataDir))
        return false;

    if (spec.engine == toolPostgreSQL)
    {
        auto markerPath = buildPath(spec.dataDir, "PG_VERSION");
        if (!exists(markerPath) && !isSymlinkPath(markerPath))
            return false;
        try
        {
            return readText(markerPath).strip().length > 0;
        }
        catch (Exception e)
        {
            throw new Exception("read postgresql data version: " ~ e.msg);
        }
    }

    auto mysqlDir = buildPath(spec.dataDir, "mysql");
    if (!exists(mysqlDir) || !isDir(mysqlDir))
        return false;
    return hasDirEntries(mysqlDir, "read database system directory");
}

/// Returns the configured database port or the engine-specific default (5432 for PostgreSQL, 3306 otherwise).
int effectiveDatabasePort(const(DatabaseConfig)* database) @safe pure
{
    if (database !is null && database.port == 0 && icmp(database.engine.strip(), toolPostgreSQL) == 0)
        return DefaultPostgreSQLPort;
    if (database is null || database.port == 0)
        return DefaultDatabasePort;
    return database.port;
}

/// Returns the loopback TCP address for `port`.
string databaseAddress(int port) @safe
{
    return joinHostPort(DatabaseListenHost, port);
}

/// Returns the interactive CLI client dispatch command for `engine`.
string databaseClientCommand(string engine) @safe pure
{
    auto trimmed = engine.strip();
    if (icmp(trimmed, toolPostgreSQL) == 0)
        return "psql";
    return trimmed.toLower();
}

/// Probes whether `address` accepts TCP connections.
bool pingDatabaseAddress(string address) @trusted
{
    return pingTCPAddress(address, managedDatabasePollInterval);
}

/// Returns `<rootDir>/run/db/<env>.json`.
string databaseStatePath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedDatabaseStateDirectory, managedDatabaseStateSubdirectory, environmentName ~ ".json");
}

/// Returns the generic database server log path.
string databaseLogPath(string rootDir, string environmentName) @safe pure
{
    return databaseEngineLogPath(rootDir, "", environmentName);
}

/// Returns `<rootDir>/run/<engine|db>/<env>/server.log`.
string databaseEngineLogPath(string rootDir, string engine, string environmentName) @safe pure
{
    auto tool = engine.strip().toLower();
    if (tool.length == 0)
        tool = managedDatabaseStateSubdirectory;
    return buildPath(toolLogRoot(rootDir, tool, environmentName), "server.log");
}

/// Returns the legacy unversioned database data directory path `<rootDir>/data/db/<env>`.
string legacyDatabaseDataPath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedDatabaseDataDirectory, managedDatabaseDataSubdirectory, environmentName);
}

/// Returns the engine/version-scoped database data directory path `<rootDir>/data/db/<env>/<engine>/<version>`.
string databaseDataPath(string rootDir, string environmentName, string engine, string version_) @safe pure
{
    return buildPath(rootDir, managedDatabaseDataDirectory, managedDatabaseDataSubdirectory, environmentName, engine, version_);
}

/// Picks the versioned data path, reusing an existing compatible legacy data directory if present.
string resolveDatabaseDataPath(string rootDir, ResolvedDatabaseEnvironment resolved) @trusted
{
    auto preferred = databaseDataPath(rootDir, resolved.environment.name, resolved.database.engine, resolved.database.version_);
    if (databaseDataInitialized(preferred))
        return preferred;

    auto legacy = legacyDatabaseDataPath(rootDir, resolved.environment.name);
    if (!databaseDataInitialized(legacy))
        return preferred;

    auto layout = detectDatabaseDataLayout(legacy);
    if (layout.length > 0 && icmp(layout, resolved.database.engine) != 0)
        return preferred;

    return legacy;
}

string databaseCredentialStatePath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedDatabaseSecretDirectory, managedDatabaseSecretSubdirectory, environmentName ~ ".json");
}

string databaseDefaultsFilePath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedDatabaseSecretDirectory, managedDatabaseSecretSubdirectory, environmentName ~ ".defaults.cnf");
}

string databaseBootstrapSQLPath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedDatabaseSecretDirectory, managedDatabaseSecretSubdirectory, environmentName ~ ".bootstrap.sql");
}

/// Returns the initdb password file path for an environment.
string databasePasswordFilePath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedDatabaseSecretDirectory, managedDatabaseSecretSubdirectory, environmentName, "password");
}

/// Returns the PostgreSQL client password file (`pgpass`) path for an environment.
string databasePGPassFilePath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedDatabaseSecretDirectory, managedDatabaseSecretSubdirectory, environmentName, "pgpass");
}

/// Loads persisted database runtime state if still live, removing stale files otherwise.
ManagedDatabaseRuntimeState* loadLiveManagedDatabaseState(
    string rootDir,
    string environmentName,
    bool delegate(string) ping = null
) @trusted
{
    auto statePath = databaseStatePath(rootDir, environmentName);
    if (!exists(statePath) && !isSymlinkPath(statePath))
        return null;

    auto state = loadManagedDatabaseState(statePath);
    if (state !is null && managedDatabaseStateIsLive(*state, ping))
        return state;

    removeDatabaseStateFile(statePath);
    return null;
}

/// Loads live database state and rejects engine/version drift against `resolved`.
ManagedDatabaseRuntimeState* loadLiveManagedDatabaseStateForResolved(
    string rootDir,
    ResolvedDatabaseEnvironment resolved,
    bool delegate(string) ping = null
) @trusted
{
    auto state = loadLiveManagedDatabaseState(rootDir, resolved.environment.name, ping);
    if (state is null)
        return null;
    if (databaseStateMatchesResolved(*state, resolved))
        return state;

    throw new Exception(format(
        `environment "%s" still has a running %s %s database on %s, but the current database is %s %s; stop the running database first`,
        resolved.environment.name,
        state.engine,
        state.version_,
        databaseAddress(state.port),
        resolved.database.engine,
        resolved.database.version_
    ));
}

/// Reads and decodes a persisted database state JSON file.
ManagedDatabaseRuntimeState* loadManagedDatabaseState(string path) @trusted
{
    auto raw = readText(path);
    JSONValue root;
    try
    {
        root = parseJSON(raw);
    }
    catch (Exception e)
    {
        throw new Exception(format("decode database state %s: %s", path, e.msg));
    }

    auto state = new ManagedDatabaseRuntimeState();
    state.environmentName = jsonStringField(root, "environment");
    state.engine = jsonStringField(root, "engine");
    state.version_ = jsonStringField(root, "version");
    state.port = cast(int) jsonLongField(root, "port", 0);
    state.pid = cast(int) jsonLongField(root, "pid", 0);
    state.dataDir = jsonStringField(root, "data_dir");
    state.logPath = jsonStringField(root, "log_path");
    state.adminTarget = jsonStringField(root, "admin_target");
    state.defaultsFile = jsonStringField(root, "defaults_file");
    state.startedAt = parseServiceTimestamp(jsonStringField(root, "started_at"));
    return state;
}

/// Reads and decodes a persisted database credentials JSON file.
ManagedDatabaseCredentials loadManagedDatabaseCredentials(string path) @trusted
{
    auto raw = readText(path);
    JSONValue root;
    try
    {
        root = parseJSON(raw);
    }
    catch (Exception e)
    {
        throw new Exception(format("decode database credentials %s: %s", path, e.msg));
    }

    ManagedDatabaseCredentials creds;
    creds.environmentName = jsonStringField(root, "environment");
    creds.engine = jsonStringField(root, "engine");
    creds.version_ = jsonStringField(root, "version");
    creds.databaseName = jsonStringField(root, "database");
    creds.user = jsonStringField(root, "user");
    creds.password = jsonStringField(root, "password");
    creds.port = cast(int) jsonLongField(root, "port", 0);
    return creds;
}

/// Reports whether `state` refers to a live PID accepting TCP connections on its configured port.
bool managedDatabaseStateIsLive(ManagedDatabaseRuntimeState state, bool delegate(string) ping = null) @trusted
{
    return servicePIDIsLive(state.pid) && effectiveDatabasePing(ping)(databaseAddress(state.port));
}

/// Serializes `state` to `path` with 0644 permissions.
void writeManagedDatabaseState(string path, ManagedDatabaseRuntimeState state) @trusted
{
    JSONValue root = JSONValue(cast(JSONValue[string]) null);
    root["environment"] = state.environmentName;
    root["engine"] = state.engine;
    root["version"] = state.version_;
    root["port"] = state.port;
    root["pid"] = state.pid;
    root["data_dir"] = state.dataDir;
    root["log_path"] = state.logPath;
    if (state.adminTarget.length > 0)
        root["admin_target"] = state.adminTarget;
    if (state.defaultsFile.length > 0)
        root["defaults_file"] = state.defaultsFile;
    root["started_at"] = formatServiceTimestamp(state.startedAt);

    writeServiceJSONFile(path, root, octal!644);
}

private ManagedDatabaseServerSpec buildManagedDatabaseServerSpec(
    ServiceContext ctx,
    ResolvedDatabaseEnvironment resolved
) @trusted
{
    if (!ctx.hasTool(resolved.database.engine))
        throw new Exception(format(`unsupported tool "%s"`, resolved.database.engine));

    auto credentials = ensureManagedDatabaseCredentialAssets(ctx.rootDir, resolved);
    auto dataDir = resolveDatabaseDataPath(ctx.rootDir, resolved);

    auto installDir = buildPath(ctx.envsDir, resolved.database.engine, resolved.database.version_);
    auto target = resolveDatabaseServerTarget(ctx.envsDir, resolved.database.engine, resolved.database.version_);
    auto adminTarget = resolveDatabaseAdminTarget(ctx.envsDir, resolved.database.engine, resolved.database.version_);
    auto initializeTarget = target;
    string createTarget = "";
    auto defaultsFile = databaseDefaultsFilePath(ctx.rootDir, resolved.environment.name);

    if (resolved.database.engine == toolPostgreSQL)
    {
        initializeTarget = resolveDatabaseInitializeTarget(ctx.envsDir, resolved.database.engine, resolved.database.version_);
        createTarget = resolveDatabaseCreateTarget(ctx.envsDir, resolved.database.engine, resolved.database.version_);
        defaultsFile = databasePGPassFilePath(ctx.rootDir, resolved.environment.name);
    }

    ManagedDatabaseServerSpec spec;
    spec.environmentName = resolved.environment.name;
    spec.engine = resolved.database.engine;
    spec.version_ = resolved.database.version_;
    spec.installDir = installDir;
    spec.target = target;
    spec.initializeTarget = initializeTarget;
    spec.adminTarget = adminTarget;
    spec.createTarget = createTarget;
    spec.dataDir = dataDir;
    spec.logPath = databaseEngineLogPath(ctx.rootDir, resolved.database.engine, resolved.environment.name);
    spec.defaultsFile = defaultsFile;
    spec.bootstrapSQLFile = databaseBootstrapSQLPath(ctx.rootDir, resolved.environment.name);
    spec.passwordFile = databasePasswordFilePath(ctx.rootDir, resolved.environment.name);
    spec.bootstrapMarker = buildPath(dataDir, ".polka-bootstrap");
    spec.databaseName = credentials.databaseName;
    spec.port = credentials.port;
    return spec;
}

private string resolveDatabaseServerTarget(string envsDir, string engine, string version_) @trusted
{
    return resolveDatabaseToolTarget(
        buildPath(envsDir, engine, version_),
        engine,
        &databaseServerCandidates,
        `%s server version "%s" is not installed under %s`,
        envsDir,
        version_
    );
}

private string resolveDatabaseAdminTarget(string envsDir, string engine, string version_) @trusted
{
    return resolveDatabaseToolTarget(
        buildPath(envsDir, engine, version_),
        engine,
        &databaseAdminCandidates,
        `%s admin client version "%s" is not installed under %s`,
        envsDir,
        version_
    );
}

private string resolveDatabaseInitializeTarget(string envsDir, string engine, string version_) @trusted
{
    return resolveDatabaseToolTarget(
        buildPath(envsDir, engine, version_),
        engine,
        &databaseInitializeCandidates,
        `%s initialization utility version "%s" is not installed under %s`,
        envsDir,
        version_
    );
}

private string resolveDatabaseCreateTarget(string envsDir, string engine, string version_) @trusted
{
    return resolveDatabaseToolTarget(
        buildPath(envsDir, engine, version_),
        engine,
        &databaseCreateCandidates,
        `%s database creation utility version "%s" is not installed under %s`,
        envsDir,
        version_
    );
}

private string resolveDatabaseToolTarget(
    string installDir,
    string engine,
    string[] function(string, string) @safe pure candidatesFn,
    string messageFmt,
    string envsDir,
    string version_
) @trusted
{
    foreach (candidate; candidatesFn(installDir, engine))
    {
        if (!exists(candidate))
            continue;
        if (isDir(candidate))
            continue;
        return candidate;
    }

    throw new Exception(format(messageFmt, engine, version_, buildPath(envsDir, engine, version_)));
}

private string[] databaseServerCandidates(string installDir, string engine) @safe pure
{
    if (currentOS() == "windows")
    {
        switch (engine)
        {
            case toolMySQL:
                return [
                    buildPath(installDir, "bin", "mysqld.exe"),
                    buildPath(installDir, "bin", "mysqld.cmd"),
                    buildPath(installDir, "bin", "mysqld.bat"),
                    buildPath(installDir, "mysqld.exe"),
                    buildPath(installDir, "mysqld.cmd"),
                    buildPath(installDir, "mysqld.bat"),
                ];
            case toolMariaDB:
                return [
                    buildPath(installDir, "bin", "mariadbd.exe"),
                    buildPath(installDir, "bin", "mariadbd.cmd"),
                    buildPath(installDir, "bin", "mariadbd.bat"),
                    buildPath(installDir, "bin", "mysqld.exe"),
                    buildPath(installDir, "bin", "mysqld.cmd"),
                    buildPath(installDir, "bin", "mysqld.bat"),
                    buildPath(installDir, "mariadbd.exe"),
                    buildPath(installDir, "mariadbd.cmd"),
                    buildPath(installDir, "mariadbd.bat"),
                    buildPath(installDir, "mysqld.exe"),
                    buildPath(installDir, "mysqld.cmd"),
                    buildPath(installDir, "mysqld.bat"),
                ];
            case toolPostgreSQL:
                return [
                    buildPath(installDir, "bin", "postgres.exe"),
                    buildPath(installDir, "postgres.exe"),
                ];
            default:
                return null;
        }
    }

    switch (engine)
    {
        case toolMySQL:
            return [
                buildPath(installDir, "bin", "mysqld"),
                buildPath(installDir, "mysqld"),
            ];
        case toolMariaDB:
            return [
                buildPath(installDir, "bin", "mariadbd"),
                buildPath(installDir, "bin", "mysqld"),
                buildPath(installDir, "mariadbd"),
                buildPath(installDir, "mysqld"),
            ];
        case toolPostgreSQL:
            return [
                buildPath(installDir, "bin", "postgres"),
                buildPath(installDir, "postgres"),
            ];
        default:
            return null;
    }
}

private string[] databaseAdminCandidates(string installDir, string engine) @safe pure
{
    if (currentOS() == "windows")
    {
        switch (engine)
        {
            case toolMySQL:
                return [
                    buildPath(installDir, "bin", "mysqladmin.exe"),
                    buildPath(installDir, "bin", "mysqladmin.cmd"),
                    buildPath(installDir, "bin", "mysqladmin.bat"),
                    buildPath(installDir, "mysqladmin.exe"),
                    buildPath(installDir, "mysqladmin.cmd"),
                    buildPath(installDir, "mysqladmin.bat"),
                ];
            case toolMariaDB:
                return [
                    buildPath(installDir, "bin", "mariadb-admin.exe"),
                    buildPath(installDir, "bin", "mariadb-admin.cmd"),
                    buildPath(installDir, "bin", "mariadb-admin.bat"),
                    buildPath(installDir, "bin", "mysqladmin.exe"),
                    buildPath(installDir, "bin", "mysqladmin.cmd"),
                    buildPath(installDir, "bin", "mysqladmin.bat"),
                    buildPath(installDir, "mariadb-admin.exe"),
                    buildPath(installDir, "mariadb-admin.cmd"),
                    buildPath(installDir, "mariadb-admin.bat"),
                    buildPath(installDir, "mysqladmin.exe"),
                    buildPath(installDir, "mysqladmin.cmd"),
                    buildPath(installDir, "mysqladmin.bat"),
                ];
            case toolPostgreSQL:
                return [
                    buildPath(installDir, "bin", "pg_ctl.exe"),
                    buildPath(installDir, "pg_ctl.exe"),
                ];
            default:
                return null;
        }
    }

    switch (engine)
    {
        case toolMySQL:
            return [
                buildPath(installDir, "bin", "mysqladmin"),
                buildPath(installDir, "mysqladmin"),
            ];
        case toolMariaDB:
            return [
                buildPath(installDir, "bin", "mariadb-admin"),
                buildPath(installDir, "bin", "mysqladmin"),
                buildPath(installDir, "mariadb-admin"),
                buildPath(installDir, "mysqladmin"),
            ];
        case toolPostgreSQL:
            return [
                buildPath(installDir, "bin", "pg_ctl"),
                buildPath(installDir, "pg_ctl"),
            ];
        default:
            return null;
    }
}

private string[] databaseDumpCandidates(string installDir, string engine) @safe pure
{
    if (currentOS() == "windows")
    {
        switch (engine)
        {
            case toolMySQL:
                return [
                    buildPath(installDir, "bin", "mysqldump.exe"),
                    buildPath(installDir, "bin", "mysqldump.cmd"),
                    buildPath(installDir, "bin", "mysqldump.bat"),
                    buildPath(installDir, "mysqldump.exe"),
                    buildPath(installDir, "mysqldump.cmd"),
                    buildPath(installDir, "mysqldump.bat"),
                ];
            case toolMariaDB:
                return [
                    buildPath(installDir, "bin", "mariadb-dump.exe"),
                    buildPath(installDir, "bin", "mariadb-dump.cmd"),
                    buildPath(installDir, "bin", "mariadb-dump.bat"),
                    buildPath(installDir, "bin", "mysqldump.exe"),
                    buildPath(installDir, "bin", "mysqldump.cmd"),
                    buildPath(installDir, "bin", "mysqldump.bat"),
                    buildPath(installDir, "mariadb-dump.exe"),
                    buildPath(installDir, "mariadb-dump.cmd"),
                    buildPath(installDir, "mariadb-dump.bat"),
                    buildPath(installDir, "mysqldump.exe"),
                    buildPath(installDir, "mysqldump.cmd"),
                    buildPath(installDir, "mysqldump.bat"),
                ];
            case toolPostgreSQL:
                return [
                    buildPath(installDir, "bin", "pg_dump.exe"),
                    buildPath(installDir, "pg_dump.exe"),
                ];
            default:
                return null;
        }
    }

    switch (engine)
    {
        case toolMySQL:
            return [
                buildPath(installDir, "bin", "mysqldump"),
                buildPath(installDir, "mysqldump"),
            ];
        case toolMariaDB:
            return [
                buildPath(installDir, "bin", "mariadb-dump"),
                buildPath(installDir, "bin", "mysqldump"),
                buildPath(installDir, "mariadb-dump"),
                buildPath(installDir, "mysqldump"),
            ];
        case toolPostgreSQL:
            return [
                buildPath(installDir, "bin", "pg_dump"),
                buildPath(installDir, "pg_dump"),
            ];
        default:
            return null;
    }
}

private string[] databaseInitializeCandidates(string installDir, string engine) @safe pure
{
    if (engine != toolPostgreSQL)
        return null;
    if (currentOS() == "windows")
        return [buildPath(installDir, "bin", "initdb.exe"), buildPath(installDir, "initdb.exe")];
    return [buildPath(installDir, "bin", "initdb"), buildPath(installDir, "initdb")];
}

private string[] databaseCreateCandidates(string installDir, string engine) @safe pure
{
    if (engine != toolPostgreSQL)
        return null;
    if (currentOS() == "windows")
        return [buildPath(installDir, "bin", "createdb.exe"), buildPath(installDir, "createdb.exe")];
    return [buildPath(installDir, "bin", "createdb"), buildPath(installDir, "createdb")];
}

private string resolveDatabaseBootstrapTarget(string installDir, string engine) @trusted
{
    foreach (candidate; databaseBootstrapCandidates(installDir, engine))
    {
        if (!exists(candidate))
            continue;
        if (isDir(candidate))
            continue;
        return candidate;
    }
    throw new Exception(format("%s bootstrap helper was not found under %s", engine, installDir));
}

private string[] databaseBootstrapCandidates(string installDir, string engine) @safe pure
{
    if (engine != toolMariaDB)
        return null;

    if (currentOS() == "windows")
    {
        return [
            buildPath(installDir, "bin", "mariadb-install-db.exe"),
            buildPath(installDir, "bin", "mysql_install_db.exe"),
            buildPath(installDir, "mariadb-install-db.exe"),
            buildPath(installDir, "mysql_install_db.exe"),
        ];
    }

    return [
        buildPath(installDir, "bin", "mariadb-install-db"),
        buildPath(installDir, "bin", "mysql_install_db"),
        buildPath(installDir, "mariadb-install-db"),
        buildPath(installDir, "mysql_install_db"),
    ];
}

private bool hasDirEntries(string dir, string actionDesc) @trusted
{
    try
    {
        foreach (_; dirEntries(dir, SpanMode.shallow))
            return true;
        return false;
    }
    catch (Exception e)
    {
        throw new Exception(actionDesc ~ ": " ~ e.msg);
    }
}

private bool databaseDataInitialized(string dataDir) @trusted
{
    if (!exists(dataDir) && !isSymlinkPath(dataDir))
        return false;
    return hasDirEntries(dataDir, "read database data directory");
}

private void resetDatabaseDataDir(string dataDir) @trusted
{
    if (!exists(dataDir) && !isSymlinkPath(dataDir))
        return;
    string[] entries;
    try
    {
        foreach (de; dirEntries(dataDir, SpanMode.shallow))
            entries ~= de.name;
    }
    catch (Exception e)
    {
        throw new Exception("read database data directory: " ~ e.msg);
    }

    foreach (entryPath; entries)
    {
        try
        {
            if (isDir(entryPath) && !isSymlinkPath(entryPath))
                rmdirRecurse(entryPath);
            else
                remove(entryPath);
        }
        catch (Exception e)
        {
            throw new Exception("reset incomplete database data directory: " ~ e.msg);
        }
    }
}

private auto openDatabaseLog(string logPath) @trusted
{
    try
    {
        return openServiceLog(logPath, true);
    }
    catch (Exception e)
    {
        throw new Exception("open database log " ~ logPath ~ ": " ~ e.msg);
    }
}

/// Inspects an existing database data directory to infer which engine created it.
string detectDatabaseDataLayout(string dataDir) @trusted
{
    if (exists(buildPath(dataDir, "PG_VERSION")))
        return toolPostgreSQL;

    foreach (marker; ["#innodb_redo", "undo_001", "undo_002"])
    {
        if (exists(buildPath(dataDir, marker)))
            return toolMySQL;
    }

    foreach (marker; ["ib_logfile0", "aria_log_control"])
    {
        if (exists(buildPath(dataDir, marker)))
            return toolMariaDB;
    }

    return "";
}

private bool databaseStateMatchesResolved(ManagedDatabaseRuntimeState state, ResolvedDatabaseEnvironment resolved) @safe pure
{
    return icmp(state.engine.strip(), resolved.database.engine.strip()) == 0 &&
        state.version_.strip() == resolved.database.version_.strip();
}

private void writeManagedDatabaseCredentials(string path, ManagedDatabaseCredentials credentials) @trusted
{
    JSONValue root = JSONValue(cast(JSONValue[string]) null);
    root["environment"] = credentials.environmentName;
    root["engine"] = credentials.engine;
    root["version"] = credentials.version_;
    if (credentials.databaseName.length > 0)
        root["database"] = credentials.databaseName;
    root["user"] = credentials.user;
    root["password"] = credentials.password;
    root["port"] = credentials.port;

    writeManagedDatabaseSecretFile(path, root.toPrettyString() ~ "\n");
}

private void writeManagedDatabaseDefaultsFile(string path, ManagedDatabaseCredentials credentials) @trusted
{
    writeManagedDatabaseSecretFile(path, renderManagedDatabaseDefaultsFile(credentials));
}

private void writeManagedDatabaseBootstrapSQLFile(string path, ManagedDatabaseCredentials credentials) @trusted
{
    writeManagedDatabaseSecretFile(path, renderManagedDatabaseBootstrapSQL(credentials));
}

private void writeManagedDatabasePasswordFile(string path, ManagedDatabaseCredentials credentials) @trusted
{
    writeManagedDatabaseSecretFile(path, credentials.password ~ "\n");
}

private void writeManagedDatabasePGPassFile(string path, ManagedDatabaseCredentials credentials) @trusted
{
    writeManagedDatabaseSecretFile(path, renderManagedDatabasePGPassFile(credentials));
}

private void writeManagedDatabaseSecretFile(string path, string content) @trusted
{
    auto dir = dirName(path);
    if (dir.length > 0 && dir != "." && !exists(dir))
    {
        try
        {
            mkdirRecurse(dir);
            version (Posix)
                setFileMode(dir, octal!700);
        }
        catch (Exception e)
        {
            throw new Exception("create database secret directory: " ~ e.msg);
        }
    }

    writeTextAtomic(path, content, octal!600);
}

private string renderManagedDatabaseDefaultsFile(ManagedDatabaseCredentials credentials) @safe
{
    return "[client]\n" ~
        "user=" ~ credentials.user ~ "\n" ~
        "password=" ~ credentials.password ~ "\n" ~
        "host=" ~ DatabaseListenHost ~ "\n" ~
        "port=" ~ to!string(credentials.port) ~ "\n" ~
        "protocol=tcp\n";
}

private string renderManagedDatabaseBootstrapSQL(ManagedDatabaseCredentials credentials) @safe pure
{
    import std.array : appender;

    auto b = appender!string();
    if (credentials.databaseName.strip().length > 0)
    {
        b.put("CREATE DATABASE IF NOT EXISTS ");
        b.put(quoteDatabaseIdentifier(credentials.databaseName));
        b.put(";\n");
    }

    foreach (host; ["localhost", "127.0.0.1"])
    {
        b.put("CREATE USER IF NOT EXISTS '");
        b.put(credentials.user);
        b.put("'@'");
        b.put(host);
        b.put("' IDENTIFIED BY '");
        b.put(credentials.password);
        b.put("';\n");

        b.put("ALTER USER '");
        b.put(credentials.user);
        b.put("'@'");
        b.put(host);
        b.put("' IDENTIFIED BY '");
        b.put(credentials.password);
        b.put("';\n");

        b.put("GRANT ALL PRIVILEGES ON *.* TO '");
        b.put(credentials.user);
        b.put("'@'");
        b.put(host);
        b.put("' WITH GRANT OPTION;\n");
    }
    b.put("FLUSH PRIVILEGES;\n");
    return b.data;
}

private string renderManagedDatabasePGPassFile(ManagedDatabaseCredentials credentials) @safe
{
    return escapePGPassField(DatabaseListenHost) ~ ":" ~
        to!string(credentials.port) ~ ":*:" ~
        escapePGPassField(credentials.user) ~ ":" ~
        escapePGPassField(credentials.password) ~ "\n";
}

private string escapePGPassField(string value) @safe pure
{
    return value.replace(`\`, `\\`).replace(":", `\:`);
}

private string quoteDatabaseIdentifier(string name) @safe pure
{
    return "`" ~ name.strip().replace("`", "``") ~ "`";
}

private string generateDatabasePassword() @trusted
{
    return randomCryptoHex(24);
}

private string managedDatabaseName(ResolvedDatabaseEnvironment resolved) @safe pure
{
    return resolved.environment.name.strip();
}

private bool delegate(string) effectiveDatabasePing(bool delegate(string) ping) @trusted
{
    if (ping is null)
        return (string addr) => pingDatabaseAddress(addr);
    return ping;
}

unittest
{
    import std.algorithm : canFind;
    import std.socket : InternetAddress, TcpSocket;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    // TestLoadLiveManagedDatabaseStateRemovesStateWhenPIDIsNotLive
    {
        auto root = createTempDir("polka-db-stale-");
        scope (exit) rmdirRecurseQuiet(root);
        auto statePath = databaseStatePath(root, "demo");
        ManagedDatabaseRuntimeState st;
        st.environmentName = "demo";
        st.engine = toolMySQL;
        st.version_ = "8.4";
        st.port = 3306;
        writeManagedDatabaseState(statePath, st);

        auto loaded = loadLiveManagedDatabaseState(root, "demo", (string) => true);
        assert(loaded is null);
        assert(!exists(statePath));
    }

    // TestStartDatabaseServerRejectsOccupiedPort
    {
        auto listener = new TcpSocket();
        scope (exit) listener.close();
        listener.bind(new InternetAddress("127.0.0.1", 0));
        listener.listen(4);
        auto boundPort = (cast(InternetAddress) listener.localAddress).port;

        auto logRoot = createTempDir("polka-db-port-");
        scope (exit) rmdirRecurseQuiet(logRoot);
        ManagedDatabaseServerSpec spec;
        spec.engine = toolMySQL;
        spec.port = boundPort;
        spec.logPath = databaseLogPath(logRoot, "demo");

        bool threwOccupied = false;
        try { startDatabaseServer(spec); }
        catch (Exception e) { threwOccupied = e.msg.canFind("already in use"); }
        assert(threwOccupied);
    }

    // TestPostgreSQLDatabaseArgumentsAndDefaults
    {
        ManagedDatabaseServerSpec spec;
        spec.engine = toolPostgreSQL;
        spec.dataDir = buildPath("tmp", "data");
        spec.passwordFile = buildPath("tmp", "password");
        spec.port = 5544;

        auto wantInit = [
            "--pgdata=" ~ spec.dataDir,
            "--username=" ~ ManagedDatabaseUserName,
            "--pwfile=" ~ spec.passwordFile,
            "--auth-host=scram-sha-256",
            "--auth-local=trust",
            "--encoding=UTF8",
        ];
        assert(databaseInitializeArgs(spec) == wantInit);
        assert(databaseStartArgs(spec) == ["-D", spec.dataDir, "-h", DatabaseListenHost, "-p", "5544"]);

        auto pgCfg = DatabaseConfig(toolPostgreSQL);
        assert(effectiveDatabasePort(&pgCfg) == DefaultPostgreSQLPort);
        assert(databaseClientCommand(toolPostgreSQL) == "psql");
    }

    // TestEnsurePostgreSQLCredentialAssets
    {
        auto root = createTempDir("polka-pg-creds-");
        scope (exit) rmdirRecurseQuiet(root);
        Environment env;
        env.name = "demo";
        auto dbCfg = new DatabaseConfig(toolPostgreSQL, "17");
        auto resolved = ResolvedDatabaseEnvironment(env, dbCfg);

        auto creds = ensureManagedDatabaseCredentialAssets(root, resolved);
        assert(creds.port == DefaultPostgreSQLPort && creds.user == ManagedDatabaseUserName && creds.databaseName == "demo");

        auto pwdText = readText(databasePasswordFilePath(root, "demo"));
        assert(pwdText.strip() == creds.password);

        auto pgpassText = readText(databasePGPassFilePath(root, "demo"));
        auto wantPGPass = DatabaseListenHost ~ ":5432:*:" ~ ManagedDatabaseUserName ~ ":" ~ creds.password;
        assert(pgpassText.strip() == wantPGPass);
    }

    // TestPostgreSQLDataMarkers
    {
        auto dataDir = createTempDir("polka-pg-markers-");
        scope (exit) rmdirRecurseQuiet(dataDir);
        writeTextAtomic(buildPath(dataDir, "PG_VERSION"), "17\n", octal!644);

        ManagedDatabaseServerSpec spec;
        spec.engine = toolPostgreSQL;
        spec.dataDir = dataDir;
        assert(databaseServerInitialized(spec));
        assert(detectDatabaseDataLayout(dataDir) == toolPostgreSQL);
    }
}
