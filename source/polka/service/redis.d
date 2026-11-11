module polka.service.redis;

import core.thread : Thread;
import core.time : Duration, dur;
import std.conv : to;
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

enum string RedisListenHost = "127.0.0.1";
enum int DefaultRedisPort = 6379;
enum string managedRedisStateDirectory = "run";
enum string managedRedisStateSubdirectory = "redis";
enum string managedRedisDataDirectory = "data";
enum string managedRedisDataSubdirectory = "redis";
enum string managedRedisLogFileName = "redis.log";
enum Duration managedRedisPollInterval = dur!"msecs"(100);
enum Duration managedRedisStartupTimeout = dur!"seconds"(10);
enum Duration managedRedisShutdownTimeout = dur!"seconds"(5);

/// Persisted runtime state for one managed Redis service.
struct RedisRuntimeState
{
    string environmentName;
    string version_;
    int port = 0;
    int pid = 0;
    string dataDir;
    string logPath;
    bool authEnabled = false;
    string passwordHash;
    SysTime startedAt;
}

/// Describes a project-local Redis server launch.
struct RedisServerSpec
{
    string environmentName;
    string version_;
    string target;
    string dataDir;
    string logPath;
    int port = 0;
    string password;
    string[] env;
}

/// Result returned when starting a Redis process.
struct RedisStartResult
{
    int pid = 0;
}

/// Hooks allowing tests to intercept Redis process lifecycle calls.
struct RedisRuntimeHooks
{
    RedisStartResult delegate(RedisServerSpec) startServer;
    void delegate(RedisRuntimeState) stopServer;
    bool delegate(string) pingAddress;
    SysTime delegate() now;

    /// Returns a copy with unset delegates populated by real implementations.
    RedisRuntimeHooks withDefaults() const @trusted
    {
        RedisRuntimeHooks copy = this;
        if (copy.startServer is null)
            copy.startServer = (RedisServerSpec spec) => startRedisServer(spec);
        if (copy.stopServer is null)
            copy.stopServer = (RedisRuntimeState st) { stopRedisRuntime(st); };
        if (copy.pingAddress is null)
            copy.pingAddress = (string addr) => pingRedisAddress(addr);
        if (copy.now is null)
            copy.now = () => Clock.currTime(UTC());
        return copy;
    }
}

/// Ensures the configured Redis server for `ctx.environment` is running.
RedisRuntimeState ensureManagedRedisStarted(
    ServiceContext ctx,
    RedisRuntimeHooks hooks,
    out bool alreadyStarted
) @trusted
{
    hooks = hooks.withDefaults();
    auto cfg = ctx.environment.redis();
    if (cfg is null || cfg.version_.strip().length == 0)
    {
        alreadyStarted = true;
        return RedisRuntimeState.init;
    }

    auto state = loadLiveRedisStateForEnvironment(ctx.rootDir, ctx.environment, hooks.pingAddress);
    if (state !is null)
    {
        alreadyStarted = true;
        return *state;
    }

    auto spec = buildRedisServerSpec(ctx);
    auto result = hooks.startServer(spec);

    RedisRuntimeState startedState;
    startedState.environmentName = ctx.environment.name;
    startedState.version_ = spec.version_;
    startedState.port = spec.port;
    startedState.pid = result.pid;
    startedState.dataDir = spec.dataDir;
    startedState.logPath = spec.logPath;
    startedState.authEnabled = spec.password.strip().length > 0;
    startedState.passwordHash = redisPasswordHash(spec.password);
    startedState.startedAt = hooks.now().toUTC();

    try
    {
        writeRedisState(redisStatePath(ctx.rootDir, ctx.environment.name), startedState);
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

/// Stops the running Redis server for `ctx.environment` and deletes its state file.
RedisRuntimeState stopManagedRedis(
    ServiceContext ctx,
    RedisRuntimeHooks hooks,
    out bool alreadyStopped
) @trusted
{
    hooks = hooks.withDefaults();

    auto state = loadLiveRedisState(ctx.rootDir, ctx.environment.name, hooks.pingAddress);
    if (state is null)
    {
        alreadyStopped = true;
        return RedisRuntimeState.init;
    }

    hooks.stopServer(*state);
    removeRedisStateFile(redisStatePath(ctx.rootDir, ctx.environment.name));

    alreadyStopped = false;
    return *state;
}

private void removeRedisStateFile(string path) @trusted
{
    if (!exists(path) && !isSymlinkPath(path))
        return;
    try
    {
        remove(path);
    }
    catch (Exception e)
    {
        throw new Exception("remove redis state: " ~ e.msg);
    }
}

/// Resolves the Redis executable, data directory, and port into a launch spec.
RedisServerSpec buildRedisServerSpec(ServiceContext ctx) @trusted
{
    auto environment = ctx.environment;
    auto cfg = environment.redis();
    if (cfg is null)
        throw new Exception(format(`environment "%s" does not define redis`, environment.name));

    auto ver = cfg.version_.strip();
    if (ver.length == 0)
        throw new Exception(format(`environment "%s" defines redis but does not define a version`, environment.name));

    auto target = ctx.resolveInstalledTool(toolRedis, ver);
    auto env = ctx.runtimeEnv();

    RedisServerSpec spec;
    spec.environmentName = environment.name;
    spec.version_ = ver;
    spec.target = target;
    spec.dataDir = redisDataPath(ctx.rootDir, environment.name);
    spec.logPath = redisLogPath(ctx.rootDir, environment.name);
    spec.port = effectiveRedisPort(cfg);
    spec.password = cfg.password.strip();
    spec.env = env;
    return spec;
}

/// Starts `redis-server` detached in `spec.dataDir` and waits for its TCP socket to listen.
RedisStartResult startRedisServer(RedisServerSpec spec) @trusted
{
    if (!exists(spec.dataDir))
    {
        try
        {
            mkdirRecurse(spec.dataDir);
        }
        catch (Exception e)
        {
            throw new Exception("create redis data directory: " ~ e.msg);
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
        pid = spawnDetachedServiceProcess(spec.target, redisServerArgs(spec), logFile, spec.env, spec.dataDir);
    }
    catch (Exception e)
    {
        throw new Exception("start redis: " ~ e.msg);
    }

    auto address = redisAddress(spec.port);
    try
    {
        waitForRedisAddress(spec.port, managedRedisStartupTimeout, null);
    }
    catch (Exception e)
    {
        try { stopServicePID(pid); } catch (Exception) {} // Best-effort teardown after startup timeout.
        throw new Exception(format("start redis on %s: %s (see %s)", address, e.msg, spec.logPath));
    }

    return RedisStartResult(pid);
}

/// Builds the CLI flags passed to `redis-server`.
string[] redisServerArgs(RedisServerSpec spec) @safe
{
    auto args = [
        "--dir", spec.dataDir,
        "--bind", RedisListenHost,
        "--port", to!string(spec.port),
    ];
    if (spec.password.strip().length > 0)
        args ~= ["--requirepass", spec.password.strip()];
    return args;
}

/// Terminates the running Redis process and waits for its TCP port to close.
void stopRedisRuntime(RedisRuntimeState state) @trusted
{
    stopServicePID(state.pid);

    auto deadline = Clock.currTime() + managedRedisShutdownTimeout;
    while (Clock.currTime() < deadline)
    {
        if (!redisStateIsLive(state, null))
            return;
        Thread.sleep(managedRedisPollInterval);
    }
    if (!redisStateIsLive(state, null))
        return;

    throw new Exception(format("redis did not stop listening on %s within %s", redisAddress(state.port), managedRedisShutdownTimeout));
}

/// Loads live Redis state and fails on version, port, or password-hash drift.
RedisRuntimeState* loadLiveRedisStateForEnvironment(
    string rootDir,
    Environment environment,
    bool delegate(string) ping = null
) @trusted
{
    auto state = loadLiveRedisState(rootDir, environment.name, ping);
    if (state is null)
        return null;
    if (redisStateMatchesEnvironment(*state, environment))
        return state;

    auto cfg = environment.redis();
    throw new Exception(format(
        `environment "%s" still has a running redis %s on %s, but the current redis is %s on %s; stop the running redis first`,
        environment.name,
        state.version_,
        redisAddress(state.port),
        cfg is null ? "" : cfg.version_.strip(),
        redisAddress(effectiveRedisPort(cfg))
    ));
}

/// Loads persisted Redis state if still live, removing stale state files otherwise.
RedisRuntimeState* loadLiveRedisState(
    string rootDir,
    string environmentName,
    bool delegate(string) ping = null
) @trusted
{
    auto path = redisStatePath(rootDir, environmentName);
    if (!exists(path) && !isSymlinkPath(path))
        return null;

    auto state = loadRedisState(path);
    if (state !is null && redisStateIsLive(*state, ping))
        return state;

    removeRedisStateFile(path);
    return null;
}

/// Reads and decodes a persisted Redis state JSON file.
RedisRuntimeState* loadRedisState(string path) @trusted
{
    auto raw = readText(path);
    JSONValue root;
    try
    {
        root = parseJSON(raw);
    }
    catch (Exception e)
    {
        throw new Exception(format("decode redis state %s: %s", path, e.msg));
    }

    auto st = new RedisRuntimeState();
    st.environmentName = jsonStringField(root, "environment");
    st.version_ = jsonStringField(root, "version");
    st.port = cast(int) jsonLongField(root, "port", 0);
    st.pid = cast(int) jsonLongField(root, "pid", 0);
    st.dataDir = jsonStringField(root, "data_dir");
    st.logPath = jsonStringField(root, "log_path");
    st.authEnabled = jsonBoolField(root, "auth_enabled", false);
    st.passwordHash = jsonStringField(root, "password_hash");
    st.startedAt = parseServiceTimestamp(jsonStringField(root, "started_at"));
    return st;
}

/// Persists `state` to `path` without writing any plaintext password.
void writeRedisState(string path, RedisRuntimeState state) @trusted
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
    if (state.passwordHash.length > 0)
        root["password_hash"] = state.passwordHash;
    root["started_at"] = formatServiceTimestamp(state.startedAt);

    writeServiceJSONFile(path, root, octal!644);
}

int effectiveRedisPort(const(RedisConfig)* redis) @safe pure nothrow @nogc
{
    if (redis is null || redis.port == 0)
        return DefaultRedisPort;
    return redis.port;
}

string redisAddress(int port) @safe
{
    return joinHostPort(RedisListenHost, port);
}

string redisURL(RedisRuntimeState state) @trusted
{
    RedisConfig cfg;
    cfg.port = state.port;
    return redisURLForConfig(&cfg);
}

string redisURLForConfig(const(RedisConfig)* redis) @safe
{
    if (redis is null)
        return "";
    return "redis://" ~ redisAddress(effectiveRedisPort(redis));
}

string redisStatePath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedRedisStateDirectory, managedRedisStateSubdirectory, environmentName ~ ".json");
}

string redisDataPath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedRedisDataDirectory, managedRedisDataSubdirectory, environmentName);
}

string redisLogPath(string rootDir, string environmentName) @safe pure
{
    return buildPath(toolLogRoot(rootDir, toolRedis, environmentName), managedRedisLogFileName);
}

void waitForRedisAddress(int port, Duration timeout, bool delegate(string) ping) @trusted
{
    auto p = redisPingFunc(ping);
    auto deadline = Clock.currTime() + timeout;
    while (Clock.currTime() < deadline)
    {
        if (p(redisAddress(port)))
            return;
        Thread.sleep(managedRedisPollInterval);
    }

    throw new Exception(format("server did not start listening within %s", timeout));
}

/// Reports whether persisted `state` matches the configured Redis version, port, and password hash.
bool redisStateMatchesEnvironment(RedisRuntimeState state, Environment environment) @safe pure
{
    auto cfg = environment.redis();
    if (cfg is null)
        return false;

    return state.version_.strip() == cfg.version_.strip() &&
        state.port == effectiveRedisPort(cfg) &&
        state.authEnabled == (cfg.password.strip().length > 0) &&
        state.passwordHash.strip() == redisPasswordHash(cfg.password);
}

bool redisStateIsLive(RedisRuntimeState state, bool delegate(string) ping = null) @trusted
{
    return redisPingFunc(ping)(redisAddress(state.port));
}

bool pingRedisAddress(string address) @trusted
{
    return pingTCPAddress(address, managedRedisPollInterval);
}

/// Returns the SHA-256 hex digest of a non-empty password for drift checks without plaintext storage.
string redisPasswordHash(string password) @safe pure
{
    auto trimmed = password.strip();
    if (trimmed.length == 0)
        return "";
    return sha256Hex(cast(const(ubyte)[]) trimmed);
}

private bool delegate(string) redisPingFunc(bool delegate(string) ping) @trusted
{
    if (ping is null)
        return (string addr) => pingRedisAddress(addr);
    return ping;
}

unittest
{
    import std.algorithm : canFind;
    import std.file : write;
    import std.path : dirName;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet, setFileMode;
    import polka.support.yaml : YamlValue;

    static string redisExecutableName()
    {
        return currentOS() == "windows" ? "redis-server.exe" : "redis-server";
    }

    // TestRedisServerArgsIncludeDirBindPortAndOptionalPassword
    {
        RedisServerSpec spec;
        spec.dataDir = buildPath("root", "data", "redis", "demo");
        spec.port = 6380;
        spec.password = "local-dev-password";

        assert(redisServerArgs(spec) == [
            "--dir", spec.dataDir,
            "--bind", "127.0.0.1",
            "--port", "6380",
            "--requirepass", "local-dev-password",
        ]);

        spec.password = "";
        assert(redisServerArgs(spec) == ["--dir", spec.dataDir, "--bind", "127.0.0.1", "--port", "6380"]);
    }

    // TestEffectiveRedisPortDefaultsTo6379
    {
        assert(effectiveRedisPort(null) == DefaultRedisPort);
        RedisConfig zeroCfg;
        assert(effectiveRedisPort(&zeroCfg) == DefaultRedisPort);
        RedisConfig customCfg;
        customCfg.port = 6390;
        assert(effectiveRedisPort(&customCfg) == 6390);
    }

    // TestRedisURLUsesRedisScheme
    {
        RedisRuntimeState st;
        st.port = 6379;
        assert(redisURL(st) == "redis://127.0.0.1:6379");
    }

    // TestRedisStateMatchesEnvironmentIncludesPortAndPasswordHash
    {
        RedisRuntimeState st;
        st.version_ = "8.8.0";
        st.port = 6380;
        st.authEnabled = true;
        st.passwordHash = redisPasswordHash("local-dev-password");

        Environment env;
        env.tools = ["redis": "8.8.0"];
        env.settings = ["redis": ["port": YamlValue.fromInt(6380), "password": YamlValue.fromString("local-dev-password")]];
        assert(redisStateMatchesEnvironment(st, env));

        env.setSettingString("redis", "password", "other-password");
        assert(!redisStateMatchesEnvironment(st, env));

        env.setSettingString("redis", "password", "local-dev-password");
        env.setSettingInt("redis", "port", 6381);
        assert(!redisStateMatchesEnvironment(st, env));
    }

    // TestWriteRedisStateDoesNotPersistPlaintextPassword
    {
        auto root = createTempDir("polka-redis-state-");
        scope (exit) rmdirRecurseQuiet(root);
        auto path = buildPath(root, "state.json");
        RedisRuntimeState st;
        st.environmentName = "demo";
        st.version_ = "8.8.0";
        st.port = 6380;
        st.authEnabled = true;
        st.passwordHash = redisPasswordHash("local-dev-password");
        writeRedisState(path, st);

        auto data = readText(path);
        assert(!data.canFind("local-dev-password"));
        auto loaded = loadRedisState(path);
        assert(loaded.passwordHash == st.passwordHash && loaded.authEnabled);
    }

    // TestEnsureManagedRedisStartedAndStop
    {
        auto rootDir = createTempDir("polka-redis-run-");
        scope (exit) rmdirRecurseQuiet(rootDir);
        auto envsDir = buildPath(rootDir, "envs");
        auto ver = "8.8.0";
        auto target = buildPath(envsDir, toolRedis, ver, redisExecutableName());
        mkdirRecurse(dirName(target));
        write(target, "redis-server");
        version (Posix)
            setFileMode(target, octal!755);

        bool[string] running;
        ServiceContext ctx;
        ctx.rootDir = rootDir;
        ctx.envsDir = envsDir;
        ctx.environment.name = "demo";
        ctx.environment.tools = ["redis": ver];
        ctx.environment.settings = ["redis": ["port": YamlValue.fromInt(6380), "password": YamlValue.fromString("local-dev-password")]];

        RedisRuntimeHooks hooks;
        hooks.startServer = (RedisServerSpec spec) {
            assert(spec.target == target && spec.dataDir == redisDataPath(rootDir, "demo") && spec.password == "local-dev-password");
            running[redisAddress(spec.port)] = true;
            return RedisStartResult(4321);
        };
        hooks.stopServer = (RedisRuntimeState state) {
            running[redisAddress(state.port)] = false;
        };
        hooks.pingAddress = (string addr) => addr in running ? running[addr] : false;
        hooks.now = () => SysTime(DateTime(2026, 6, 30, 12, 0, 0), UTC());

        bool alreadyStarted = false;
        auto started = ensureManagedRedisStarted(ctx, hooks, alreadyStarted);
        assert(!alreadyStarted && started.pid == 4321 && started.authEnabled && started.passwordHash == redisPasswordHash("local-dev-password"));

        bool alreadyStopped = false;
        auto stopped = stopManagedRedis(ctx, hooks, alreadyStopped);
        assert(!alreadyStopped && stopped.pid == 4321);
        assert(!exists(redisStatePath(rootDir, "demo")));
    }
}
