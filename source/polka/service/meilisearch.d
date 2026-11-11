module polka.service.meilisearch;

import core.thread : Thread;
import core.time : Duration, dur;
import std.datetime : Clock, DateTime, SysTime, UTC;
import std.file : exists, mkdirRecurse, readText, remove;
import std.format : format;
import std.json : JSONValue, parseJSON;
import std.path : buildPath;
import std.string : strip;

import polka.config;
import polka.service.context;
import polka.service.process;
import polka.support.crypto : sha256Hex;
import polka.support.fs : currentOS, isSymlinkPath, octal;
import polka.support.net : joinHostPort, pingTCPAddress;
import polka.tools.cache : jsonBoolField, jsonLongField, jsonStringField;

enum string MeilisearchListenHost = "127.0.0.1";
enum int DefaultMeilisearchPort = 7700;
enum string managedMeilisearchStateDirectory = "run";
enum string managedMeilisearchStateSubdirectory = "meilisearch";
enum string managedMeilisearchDataDirectory = "data";
enum string managedMeilisearchDataSubdirectory = "meilisearch";
enum string managedMeilisearchLogFileName = "meilisearch.log";
enum Duration managedMeilisearchPollInterval = dur!"msecs"(100);
enum Duration managedMeilisearchStartupTimeout = dur!"seconds"(10);
enum Duration managedMeilisearchShutdownTimeout = dur!"seconds"(5);

/// Persisted runtime state for one Meilisearch instance.
struct MeilisearchRuntimeState
{
    string environmentName;
    string version_;
    int port = 0;
    int pid = 0;
    string dataDir;
    string logPath;
    bool authEnabled = false;
    string masterKeyHash;
    SysTime startedAt;
}

/// Describes a Meilisearch process launch.
struct MeilisearchServerSpec
{
    string environmentName;
    string version_;
    string target;
    string dataDir;
    string logPath;
    int port = 0;
    string masterKey;
    string[] env;
}

/// Result returned when starting a Meilisearch server process.
struct MeilisearchStartResult
{
    int pid = 0;
}

/// Hooks allowing tests to stub Meilisearch process lifecycle operations.
struct MeilisearchRuntimeHooks
{
    MeilisearchStartResult delegate(MeilisearchServerSpec) startServer;
    void delegate(MeilisearchRuntimeState) stopServer;
    bool delegate(string) pingAddress;
    SysTime delegate() now;

    /// Returns a copy with unset hooks filled in by production implementations.
    MeilisearchRuntimeHooks withDefaults() const @trusted
    {
        MeilisearchRuntimeHooks copy = this;
        if (copy.startServer is null)
            copy.startServer = (MeilisearchServerSpec spec) => startMeilisearchServer(spec);
        if (copy.stopServer is null)
            copy.stopServer = (MeilisearchRuntimeState st) { stopMeilisearchRuntime(st); };
        if (copy.pingAddress is null)
            copy.pingAddress = (string addr) => pingMeilisearchAddress(addr);
        if (copy.now is null)
            copy.now = () => Clock.currTime(UTC());
        return copy;
    }
}

/// Starts or reuses the configured Meilisearch service for `ctx.environment`.
MeilisearchRuntimeState ensureManagedMeilisearchStarted(
    ServiceContext ctx,
    MeilisearchRuntimeHooks hooks,
    out bool alreadyStarted
) @trusted
{
    hooks = hooks.withDefaults();
    auto cfg = ctx.environment.meilisearch();
    if (cfg is null || cfg.version_.strip().length == 0)
    {
        alreadyStarted = true;
        return MeilisearchRuntimeState.init;
    }

    auto state = loadLiveMeilisearchStateForEnvironment(ctx.rootDir, ctx.environment, hooks.pingAddress);
    if (state !is null)
    {
        alreadyStarted = true;
        return *state;
    }

    auto spec = buildMeilisearchServerSpec(ctx);
    auto result = hooks.startServer(spec);

    MeilisearchRuntimeState startedState;
    startedState.environmentName = ctx.environment.name;
    startedState.version_ = spec.version_;
    startedState.port = spec.port;
    startedState.pid = result.pid;
    startedState.dataDir = spec.dataDir;
    startedState.logPath = spec.logPath;
    startedState.authEnabled = spec.masterKey.strip().length > 0;
    startedState.masterKeyHash = meilisearchMasterKeyHash(spec.masterKey);
    startedState.startedAt = hooks.now().toUTC();

    try
    {
        writeMeilisearchState(meilisearchStatePath(ctx.rootDir, ctx.environment.name), startedState);
    }
    catch (Exception e)
    {
        try
        {
            hooks.stopServer(startedState);
        }
        catch (Exception) {} // Best-effort cleanup if persisting state fails.
        throw e;
    }

    alreadyStarted = false;
    return startedState;
}

/// Stops the running Meilisearch server for `ctx.environment` and clears persisted state.
MeilisearchRuntimeState stopManagedMeilisearch(
    ServiceContext ctx,
    MeilisearchRuntimeHooks hooks,
    out bool alreadyStopped
) @trusted
{
    hooks = hooks.withDefaults();

    auto state = loadLiveMeilisearchState(ctx.rootDir, ctx.environment.name, hooks.pingAddress);
    if (state is null)
    {
        alreadyStopped = true;
        return MeilisearchRuntimeState.init;
    }

    hooks.stopServer(*state);
    removeMeilisearchStateFile(meilisearchStatePath(ctx.rootDir, ctx.environment.name));

    alreadyStopped = false;
    return *state;
}

private void removeMeilisearchStateFile(string path) @trusted
{
    if (!exists(path) && !isSymlinkPath(path))
        return;
    try
    {
        remove(path);
    }
    catch (Exception e)
    {
        throw new Exception("remove meilisearch state: " ~ e.msg);
    }
}

/// Resolves the Meilisearch executable, data directory, and port into a launch spec.
MeilisearchServerSpec buildMeilisearchServerSpec(ServiceContext ctx) @trusted
{
    auto environment = ctx.environment;
    auto cfg = environment.meilisearch();
    if (cfg is null)
        throw new Exception(format(`environment "%s" does not define meilisearch`, environment.name));

    auto ver = cfg.version_.strip();
    if (ver.length == 0)
        throw new Exception(format(`environment "%s" defines meilisearch but does not define a version`, environment.name));

    auto target = ctx.resolveInstalledTool(toolMeilisearch, ver);
    auto env = ctx.runtimeEnv();

    MeilisearchServerSpec spec;
    spec.environmentName = environment.name;
    spec.version_ = ver;
    spec.target = target;
    spec.dataDir = meilisearchDataPath(ctx.rootDir, environment.name);
    spec.logPath = meilisearchLogPath(ctx.rootDir, environment.name);
    spec.port = effectiveMeilisearchPort(cfg);
    spec.masterKey = cfg.masterKey.strip();
    spec.env = env;
    return spec;
}

/// Launches Meilisearch and waits for it to begin listening on its configured HTTP port.
MeilisearchStartResult startMeilisearchServer(MeilisearchServerSpec spec) @trusted
{
    if (!exists(spec.dataDir))
    {
        try
        {
            mkdirRecurse(spec.dataDir);
        }
        catch (Exception e)
        {
            throw new Exception("create meilisearch data directory: " ~ e.msg);
        }
    }

    auto logFile = openServiceLog(spec.logPath);
    scope (exit)
    {
        if (logFile.isOpen)
            logFile.close();
    }

    int pid = 0;
    try
    {
        pid = spawnDetachedServiceProcess(spec.target, meilisearchServerArgs(spec), logFile, spec.env);
    }
    catch (Exception e)
    {
        throw new Exception("start meilisearch: " ~ e.msg);
    }

    auto address = meilisearchAddress(spec.port);
    try
    {
        waitForMeilisearchAddress(spec.port, managedMeilisearchStartupTimeout, null);
    }
    catch (Exception e)
    {
        try { stopServicePID(pid); } catch (Exception) {} // Best-effort teardown on startup timeout.
        throw new Exception(format("start meilisearch on %s: %s (see %s)", address, e.msg, spec.logPath));
    }

    return MeilisearchStartResult(pid);
}

/// Builds the CLI arguments for `meilisearch`.
string[] meilisearchServerArgs(MeilisearchServerSpec spec) @safe
{
    auto args = [
        "--db-path", spec.dataDir,
        "--http-addr", meilisearchAddress(spec.port),
    ];
    if (spec.masterKey.strip().length > 0)
        args ~= ["--master-key", spec.masterKey.strip()];
    return args;
}

/// Stops a running Meilisearch process and waits for its TCP socket to close.
void stopMeilisearchRuntime(MeilisearchRuntimeState state) @trusted
{
    stopServicePID(state.pid);

    auto deadline = Clock.currTime() + managedMeilisearchShutdownTimeout;
    while (Clock.currTime() < deadline)
    {
        if (!meilisearchStateIsLive(state, null))
            return;
        Thread.sleep(managedMeilisearchPollInterval);
    }
    if (!meilisearchStateIsLive(state, null))
        return;

    throw new Exception(format("meilisearch did not stop listening on %s within %s", meilisearchAddress(state.port), managedMeilisearchShutdownTimeout));
}

/// Loads live Meilisearch state and fails on version, port, or master-key drift against `environment`.
MeilisearchRuntimeState* loadLiveMeilisearchStateForEnvironment(
    string rootDir,
    Environment environment,
    bool delegate(string) ping = null
) @trusted
{
    auto state = loadLiveMeilisearchState(rootDir, environment.name, ping);
    if (state is null)
        return null;
    if (meilisearchStateMatchesEnvironment(*state, environment))
        return state;

    auto cfg = environment.meilisearch();
    throw new Exception(format(
        `environment "%s" still has a running meilisearch %s on %s, but the current meilisearch is %s on %s; stop the running meilisearch first`,
        environment.name,
        state.version_,
        meilisearchAddress(state.port),
        cfg is null ? "" : cfg.version_.strip(),
        meilisearchAddress(effectiveMeilisearchPort(cfg))
    ));
}

/// Loads persisted Meilisearch state if live, removing stale files otherwise.
MeilisearchRuntimeState* loadLiveMeilisearchState(
    string rootDir,
    string environmentName,
    bool delegate(string) ping = null
) @trusted
{
    auto path = meilisearchStatePath(rootDir, environmentName);
    if (!exists(path) && !isSymlinkPath(path))
        return null;

    auto state = loadMeilisearchState(path);
    if (state !is null && meilisearchStateIsLive(*state, ping))
        return state;

    removeMeilisearchStateFile(path);
    return null;
}

/// Reads and decodes a persisted Meilisearch state JSON file.
MeilisearchRuntimeState* loadMeilisearchState(string path) @trusted
{
    auto raw = readText(path);
    JSONValue root;
    try
    {
        root = parseJSON(raw);
    }
    catch (Exception e)
    {
        throw new Exception(format("decode meilisearch state %s: %s", path, e.msg));
    }

    auto st = new MeilisearchRuntimeState();
    st.environmentName = jsonStringField(root, "environment");
    st.version_ = jsonStringField(root, "version");
    st.port = cast(int) jsonLongField(root, "port", 0);
    st.pid = cast(int) jsonLongField(root, "pid", 0);
    st.dataDir = jsonStringField(root, "data_dir");
    st.logPath = jsonStringField(root, "log_path");
    st.authEnabled = jsonBoolField(root, "auth_enabled", false);
    st.masterKeyHash = jsonStringField(root, "master_key_hash");
    st.startedAt = parseServiceTimestamp(jsonStringField(root, "started_at"));
    return st;
}

/// Writes `state` to `path` without persisting any plaintext master key.
void writeMeilisearchState(string path, MeilisearchRuntimeState state) @trusted
{
    JSONValue root = JSONValue(cast(JSONValue[string]) null);
    root["environment"] = state.environmentName;
    root["version"] = state.version_;
    root["port"] = state.port;
    root["pid"] = state.pid;
    root["data_dir"] = state.dataDir;
    if (state.logPath.length > 0)
        root["log_path"] = state.logPath;
    root["auth_enabled"] = state.authEnabled;
    if (state.masterKeyHash.length > 0)
        root["master_key_hash"] = state.masterKeyHash;
    root["started_at"] = formatServiceTimestamp(state.startedAt);

    writeServiceJSONFile(path, root, octal!644);
}

int effectiveMeilisearchPort(const(MeilisearchConfig)* meilisearch) @safe pure nothrow @nogc
{
    if (meilisearch is null || meilisearch.port == 0)
        return DefaultMeilisearchPort;
    return meilisearch.port;
}

string meilisearchAddress(int port) @safe
{
    return joinHostPort(MeilisearchListenHost, port);
}

string meilisearchURL(MeilisearchRuntimeState state) @trusted
{
    MeilisearchConfig cfg;
    cfg.port = state.port;
    return meilisearchURLForConfig(&cfg);
}

string meilisearchURLForConfig(const(MeilisearchConfig)* meilisearch) @safe
{
    if (meilisearch is null)
        return "";
    return "http://" ~ meilisearchAddress(effectiveMeilisearchPort(meilisearch));
}

string meilisearchStatePath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedMeilisearchStateDirectory, managedMeilisearchStateSubdirectory, environmentName ~ ".json");
}

string meilisearchDataPath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedMeilisearchDataDirectory, managedMeilisearchDataSubdirectory, environmentName);
}

string meilisearchLogPath(string rootDir, string environmentName) @safe pure
{
    return buildPath(toolLogRoot(rootDir, toolMeilisearch, environmentName), managedMeilisearchLogFileName);
}

void waitForMeilisearchAddress(int port, Duration timeout, bool delegate(string) ping) @trusted
{
    auto p = meilisearchPingFunc(ping);
    auto deadline = Clock.currTime() + timeout;
    while (Clock.currTime() < deadline)
    {
        if (p(meilisearchAddress(port)))
            return;
        Thread.sleep(managedMeilisearchPollInterval);
    }

    throw new Exception(format("server did not start listening within %s", timeout));
}

/// Reports whether persisted `state` matches the configured Meilisearch version, port, and master key hash.
bool meilisearchStateMatchesEnvironment(MeilisearchRuntimeState state, Environment environment) @safe pure
{
    auto cfg = environment.meilisearch();
    if (cfg is null)
        return false;

    return state.version_.strip() == cfg.version_.strip() &&
        state.port == effectiveMeilisearchPort(cfg) &&
        state.authEnabled == (cfg.masterKey.strip().length > 0) &&
        state.masterKeyHash.strip() == meilisearchMasterKeyHash(cfg.masterKey);
}

bool meilisearchStateIsLive(MeilisearchRuntimeState state, bool delegate(string) ping = null) @trusted
{
    return meilisearchPingFunc(ping)(meilisearchAddress(state.port));
}

bool pingMeilisearchAddress(string address) @trusted
{
    return pingTCPAddress(address, managedMeilisearchPollInterval);
}

/// Returns the SHA-256 hex digest of a non-empty master key for safe state drift checks.
string meilisearchMasterKeyHash(string masterKey) @safe pure
{
    auto trimmed = masterKey.strip();
    if (trimmed.length == 0)
        return "";
    return sha256Hex(cast(const(ubyte)[]) trimmed);
}

private bool delegate(string) meilisearchPingFunc(bool delegate(string) ping) @trusted
{
    if (ping is null)
        return (string addr) => pingMeilisearchAddress(addr);
    return ping;
}

unittest
{
    import std.algorithm : canFind;
    import std.file : write;
    import std.path : dirName;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet, setFileMode;
    import polka.support.yaml : YamlValue;

    static string meilisearchExecutableName()
    {
        return currentOS() == "windows" ? "meilisearch.exe" : "meilisearch";
    }

    // TestMeilisearchServerArgsIncludeDataAddressAndOptionalMasterKey
    {
        MeilisearchServerSpec spec;
        spec.dataDir = buildPath("root", "data", "meilisearch", "demo");
        spec.port = 7701;
        spec.masterKey = "local-dev-key";

        assert(meilisearchServerArgs(spec) == [
            "--db-path", spec.dataDir,
            "--http-addr", "127.0.0.1:7701",
            "--master-key", "local-dev-key",
        ]);

        spec.masterKey = "";
        assert(meilisearchServerArgs(spec) == ["--db-path", spec.dataDir, "--http-addr", "127.0.0.1:7701"]);
    }

    // TestMeilisearchStateMatchesEnvironmentIncludesPortAndMasterKeyHash
    {
        MeilisearchRuntimeState st;
        st.version_ = "1.48";
        st.port = 7701;
        st.authEnabled = true;
        st.masterKeyHash = meilisearchMasterKeyHash("local-dev-key");

        Environment env;
        env.tools = ["meilisearch": "1.48"];
        env.settings = ["meilisearch": ["port": YamlValue.fromInt(7701), "master-key": YamlValue.fromString("local-dev-key")]];
        assert(meilisearchStateMatchesEnvironment(st, env));

        env.setSettingString("meilisearch", "master-key", "other-key");
        assert(!meilisearchStateMatchesEnvironment(st, env));

        env.setSettingString("meilisearch", "master-key", "local-dev-key");
        env.setSettingInt("meilisearch", "port", 7702);
        assert(!meilisearchStateMatchesEnvironment(st, env));
    }

    // TestWriteMeilisearchStateDoesNotPersistPlaintextMasterKey
    {
        auto root = createTempDir("polka-meili-state-");
        scope (exit) rmdirRecurseQuiet(root);
        auto path = buildPath(root, "state.json");
        MeilisearchRuntimeState st;
        st.environmentName = "demo";
        st.version_ = "1.48";
        st.port = 7701;
        st.authEnabled = true;
        st.masterKeyHash = meilisearchMasterKeyHash("local-dev-key");
        writeMeilisearchState(path, st);

        auto data = readText(path);
        assert(!data.canFind("local-dev-key"));
        auto loaded = loadMeilisearchState(path);
        assert(loaded.masterKeyHash == st.masterKeyHash && loaded.authEnabled);
    }

    // TestEnsureManagedMeilisearchStartedAndStop
    {
        auto rootDir = createTempDir("polka-meili-run-");
        scope (exit) rmdirRecurseQuiet(rootDir);
        auto envsDir = buildPath(rootDir, "envs");
        auto ver = "1.48";
        auto target = buildPath(envsDir, toolMeilisearch, ver, meilisearchExecutableName());
        mkdirRecurse(dirName(target));
        write(target, "meilisearch");
        version (Posix)
            setFileMode(target, octal!755);

        bool[string] running;
        ServiceContext ctx;
        ctx.rootDir = rootDir;
        ctx.envsDir = envsDir;
        ctx.environment.name = "demo";
        ctx.environment.tools = ["meilisearch": ver];
        ctx.environment.settings = ["meilisearch": ["port": YamlValue.fromInt(7701), "master-key": YamlValue.fromString("local-dev-key")]];

        MeilisearchRuntimeHooks hooks;
        hooks.startServer = (MeilisearchServerSpec spec) {
            assert(spec.target == target && spec.dataDir == meilisearchDataPath(rootDir, "demo") && spec.masterKey == "local-dev-key");
            running[meilisearchAddress(spec.port)] = true;
            return MeilisearchStartResult(1234);
        };
        hooks.stopServer = (MeilisearchRuntimeState state) {
            running[meilisearchAddress(state.port)] = false;
        };
        hooks.pingAddress = (string addr) => addr in running ? running[addr] : false;
        hooks.now = () => SysTime(DateTime(2026, 6, 30, 12, 0, 0), UTC());

        bool alreadyStarted = false;
        auto started = ensureManagedMeilisearchStarted(ctx, hooks, alreadyStarted);
        assert(!alreadyStarted && started.pid == 1234 && started.authEnabled && started.masterKeyHash == meilisearchMasterKeyHash("local-dev-key"));

        bool alreadyStopped = false;
        auto stopped = stopManagedMeilisearch(ctx, hooks, alreadyStopped);
        assert(!alreadyStopped && stopped.pid == 1234);
        assert(!exists(meilisearchStatePath(rootDir, "demo")));
    }
}
