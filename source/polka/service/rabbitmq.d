module polka.service.rabbitmq;

import core.thread : Thread;
import core.time : Duration, dur;
import std.array : appender;
import std.conv : to;
import std.datetime : Clock, SysTime, UTC;
import std.file : exists, mkdirRecurse, readText, remove;
import std.format : format;
import std.json : JSONValue, parseJSON;
import std.path : buildPath, dirName;
import std.string : split, strip, toLower;

import polka.config;
import polka.service.context;
import polka.service.process;
import polka.support.crypto : randomCryptoHex, sha256Hex;
import polka.support.fs : currentArch, currentOS, isSymlinkPath, octal, writeTextAtomic;
import polka.support.net : joinHostPort, pingTCPAddress;
import polka.tools.cache : jsonLongField, jsonStringField;

enum string RabbitMQListenHost = "127.0.0.1";
enum int DefaultRabbitMQPort = 5672;
enum int DefaultRabbitMQManagementPort = 15672;
enum string DefaultRabbitMQUsername = "guest";
enum string DefaultRabbitMQPassword = "guest";
enum Duration rabbitMQStartupTimeout = dur!"seconds"(30);
enum Duration rabbitMQShutdownTimeout = dur!"seconds"(10);
enum Duration rabbitMQPollInterval = dur!"msecs"(100);

/// Non-secret persisted runtime state for one managed RabbitMQ broker.
struct RabbitMQRuntimeState
{
    string environmentName;
    string version_;
    int port = 0;
    int managementPort = 0;
    string username;
    string passwordHash;
    int pid = 0;
    string target;
    string erlangHome;
    string dataDir;
    string runDir;
    string logPath;
    SysTime startedAt;
}

/// Project-local broker launch specification.
struct RabbitMQServerSpec
{
    string environmentName;
    string version_;
    string target;
    string controlTarget;
    string erlangHome;
    string dataDir;
    string runDir;
    string logPath;
    int port = 0;
    int managementPort = 0;
    string username;
    string password;
    string[] env;
}

/// Result returned when starting a RabbitMQ broker process.
struct RabbitMQStartResult
{
    int pid = 0;
}

/// Hooks allowing tests to stub RabbitMQ process lifecycle operations.
struct RabbitMQRuntimeHooks
{
    RabbitMQStartResult delegate(RabbitMQServerSpec) startServer;
    void delegate(RabbitMQRuntimeState) stopServer;
    bool delegate(string) pingAddress;
    SysTime delegate() now;

    /// Returns a copy with unset delegates filled by production implementations.
    RabbitMQRuntimeHooks withDefaults() const @trusted
    {
        RabbitMQRuntimeHooks copy = this;
        if (copy.startServer is null)
            copy.startServer = (RabbitMQServerSpec spec) => startRabbitMQServer(spec);
        if (copy.stopServer is null)
            copy.stopServer = (RabbitMQRuntimeState st) { stopRabbitMQRuntime(st); };
        if (copy.pingAddress is null)
            copy.pingAddress = (string addr) => pingRabbitMQAddress(addr);
        if (copy.now is null)
            copy.now = () => Clock.currTime(UTC());
        return copy;
    }
}

/// Starts or reuses the configured RabbitMQ broker for `ctx.environment`.
RabbitMQRuntimeState ensureManagedRabbitMQStarted(
    ServiceContext ctx,
    RabbitMQRuntimeHooks hooks,
    out bool alreadyStarted
) @trusted
{
    hooks = hooks.withDefaults();
    auto configured = ctx.environment.rabbitMQ();
    if (configured is null || configured.version_.strip().length == 0)
    {
        alreadyStarted = true;
        return RabbitMQRuntimeState.init;
    }

    auto state = loadLiveRabbitMQStateForEnvironment(ctx.rootDir, ctx.environment, hooks.pingAddress);
    if (state !is null)
    {
        alreadyStarted = true;
        return *state;
    }

    auto spec = buildRabbitMQServerSpec(ctx);
    auto result = hooks.startServer(spec);

    RabbitMQRuntimeState stateValue;
    stateValue.environmentName = ctx.environment.name;
    stateValue.version_ = spec.version_;
    stateValue.port = spec.port;
    stateValue.managementPort = spec.managementPort;
    stateValue.username = spec.username;
    stateValue.passwordHash = rabbitMQPasswordHash(spec.password);
    stateValue.pid = result.pid;
    stateValue.target = spec.target;
    stateValue.erlangHome = spec.erlangHome;
    stateValue.dataDir = spec.dataDir;
    stateValue.runDir = spec.runDir;
    stateValue.logPath = spec.logPath;
    stateValue.startedAt = hooks.now().toUTC();

    try
    {
        writeRabbitMQState(rabbitMQStatePath(ctx.rootDir, ctx.environment.name), stateValue);
    }
    catch (Exception e)
    {
        try
        {
            hooks.stopServer(stateValue);
        }
        catch (Exception) {} // Best-effort cleanup if persisting state fails.
        throw e;
    }

    alreadyStarted = false;
    return stateValue;
}

/// Gracefully stops the active environment's RabbitMQ broker and removes its state file.
RabbitMQRuntimeState stopManagedRabbitMQ(
    ServiceContext ctx,
    RabbitMQRuntimeHooks hooks,
    out bool alreadyStopped
) @trusted
{
    hooks = hooks.withDefaults();

    auto state = loadLiveRabbitMQState(ctx.rootDir, ctx.environment.name, hooks.pingAddress);
    if (state is null)
    {
        alreadyStopped = true;
        return RabbitMQRuntimeState.init;
    }

    hooks.stopServer(*state);
    removeRabbitMQStateFile(rabbitMQStatePath(ctx.rootDir, ctx.environment.name));

    alreadyStopped = false;
    return *state;
}

private void removeRabbitMQStateFile(string path) @trusted
{
    if (!exists(path) && !isSymlinkPath(path))
        return;
    try
    {
        remove(path);
    }
    catch (Exception e)
    {
        throw new Exception("remove rabbitmq state: " ~ e.msg);
    }
}

/// Resolves RabbitMQ and its managed Erlang dependency into a launch spec.
RabbitMQServerSpec buildRabbitMQServerSpec(ServiceContext ctx) @trusted
{
    if (currentOS() != "windows" || currentArch() != "amd64")
        throw new Exception("rabbitmq is only supported on windows/amd64");

    auto cfg = ctx.environment.rabbitMQ();
    if (cfg is null || cfg.version_.strip().length == 0)
        throw new Exception(format(`environment "%s" does not define rabbitmq`, ctx.environment.name));

    auto target = ctx.resolveInstalledTool(toolRabbitMQ, cfg.version_);
    string erl;
    try
    {
        erl = ctx.resolveInstalledTool(toolErlang, "27");
    }
    catch (Exception e)
    {
        throw new Exception("resolve rabbitmq erlang dependency: " ~ e.msg);
    }

    auto env = ctx.runtimeEnv();
    RabbitMQServerSpec spec;
    spec.environmentName = ctx.environment.name;
    spec.version_ = cfg.version_.strip();
    spec.target = target;
    spec.controlTarget = buildPath(dirName(target), "rabbitmqctl.bat");
    spec.erlangHome = dirName(dirName(erl));
    spec.dataDir = rabbitMQDataPath(ctx.rootDir, ctx.environment.name);
    spec.runDir = rabbitMQRunPath(ctx.rootDir, ctx.environment.name);
    spec.logPath = rabbitMQLogPath(ctx.rootDir, ctx.environment.name);
    spec.port = effectiveRabbitMQPort(cfg);
    spec.managementPort = effectiveRabbitMQManagementPort(cfg);
    spec.username = effectiveRabbitMQUsername(cfg);
    spec.password = effectiveRabbitMQPassword(cfg);
    spec.env = env;
    return spec;
}

/// Writes isolated broker config files, launches RabbitMQ, waits for AMQP readiness, and reconciles credentials.
RabbitMQStartResult startRabbitMQServer(RabbitMQServerSpec spec) @trusted
{
    prepareRabbitMQRuntime(spec);

    auto logFile = openServiceLog(spec.logPath);
    scope (exit)
    {
        if (logFile.isOpen)
            logFile.close();
    }

    int pid = 0;
    try
    {
        pid = spawnDetachedServiceProcess(spec.target, null, logFile, rabbitMQRuntimeEnvironment(spec));
    }
    catch (Exception e)
    {
        throw new Exception("start rabbitmq: " ~ e.msg);
    }

    try
    {
        waitForRabbitMQAddress(spec.port, rabbitMQStartupTimeout, null);
    }
    catch (Exception e)
    {
        try { stopServicePID(pid); } catch (Exception) {} // Best-effort cleanup on startup timeout.
        throw new Exception(format("start rabbitmq on %s: %s (see %s)", rabbitMQAddress(spec.port), e.msg, spec.logPath));
    }

    try
    {
        reconcileRabbitMQCredentials(spec);
    }
    catch (Exception e)
    {
        try { stopServicePID(pid); } catch (Exception) {} // Best-effort cleanup if credential setup fails.
        throw e;
    }

    auto pidFile = buildPath(spec.runDir, "rabbitmq.pid");
    if (exists(pidFile))
    {
        try
        {
            auto brokerPID = to!int(readText(pidFile).strip());
            if (brokerPID > 0)
                pid = brokerPID;
        }
        catch (Exception) {} // Fall back to spawned process PID when pid file is unreadable or pending.
    }

    return RabbitMQStartResult(pid);
}

private void prepareRabbitMQRuntime(RabbitMQServerSpec spec) @trusted
{
    if (!exists(spec.dataDir))
    {
        try
        {
            mkdirRecurse(spec.dataDir);
        }
        catch (Exception e)
        {
            throw new Exception("create rabbitmq data directory: " ~ e.msg);
        }
    }
    if (!exists(spec.runDir))
    {
        try
        {
            mkdirRecurse(spec.runDir);
        }
        catch (Exception e)
        {
            throw new Exception("create rabbitmq runtime directory: " ~ e.msg);
        }
    }

    auto cookiePath = buildPath(spec.runDir, ".erlang.cookie");
    if (!exists(cookiePath) && !isSymlinkPath(cookiePath))
    {
        try
        {
            writeTextAtomic(cookiePath, randomCryptoHex(32), octal!600);
        }
        catch (Exception e)
        {
            throw new Exception("write rabbitmq erlang cookie: " ~ e.msg);
        }
    }

    auto configuration = format(
        "listeners.tcp.1 = %s:%d\nmanagement.tcp.ip = %s\nmanagement.tcp.port = %d\ndefault_user_tags.administrator = true\n",
        RabbitMQListenHost,
        spec.port,
        RabbitMQListenHost,
        spec.managementPort
    );
    try
    {
        writeTextAtomic(buildPath(spec.runDir, "rabbitmq.conf"), configuration, octal!600);
    }
    catch (Exception e)
    {
        throw new Exception("write rabbitmq configuration: " ~ e.msg);
    }

    try
    {
        writeTextAtomic(buildPath(spec.runDir, "enabled_plugins"), "[rabbitmq_management].\n", octal!644);
    }
    catch (Exception e)
    {
        throw new Exception("write rabbitmq enabled plugins: " ~ e.msg);
    }
}

/// Prepares the active broker identity and configuration for CLI dispatch helpers.
string[] prepareRabbitMQDispatchEnvironment(ServiceContext ctx) @trusted
{
    auto spec = buildRabbitMQServerSpec(ctx);
    prepareRabbitMQRuntime(spec);
    return rabbitMQRuntimeEnvironment(spec);
}

/// Constructs an isolated environment slice defining node name, mnesia base, config, plugins, PID, cookie, and PATH.
string[] rabbitMQRuntimeEnvironment(RabbitMQServerSpec spec) @trusted
{
    auto env = spec.env.dup;
    auto nodeName = "polka_" ~ sanitizeRabbitMQNodeName(spec.environmentName) ~ "@localhost";

    env = setEnvironmentSliceValue(env, "ERLANG_HOME", spec.erlangHome);
    env = setEnvironmentSliceValue(env, "RABBITMQ_NODENAME", nodeName);
    env = setEnvironmentSliceValue(env, "RABBITMQ_MNESIA_BASE", spec.dataDir);
    env = setEnvironmentSliceValue(env, "RABBITMQ_LOGS", spec.logPath);
    env = setEnvironmentSliceValue(env, "RABBITMQ_CONFIG_FILE", buildPath(spec.runDir, "rabbitmq"));
    env = setEnvironmentSliceValue(env, "RABBITMQ_ENABLED_PLUGINS_FILE", buildPath(spec.runDir, "enabled_plugins"));
    env = setEnvironmentSliceValue(env, "RABBITMQ_PID_FILE", buildPath(spec.runDir, "rabbitmq.pid"));
    env = setEnvironmentSliceValue(env, "RABBITMQ_ERLANG_COOKIE", readRabbitMQCookie(buildPath(spec.runDir, ".erlang.cookie")));

    if (spec.username.length > 0)
    {
        env = setEnvironmentSliceValue(env, "RABBITMQ_DEFAULT_USER", spec.username);
        env = setEnvironmentSliceValue(env, "RABBITMQ_DEFAULT_PASS", spec.password);
    }

    auto sep = currentOS() == "windows" ? ";" : ":";
    auto currentPath = environmentSliceValue(env, "PATH");
    env = setEnvironmentSliceValue(env, "PATH", buildPath(spec.erlangHome, "bin") ~ sep ~ currentPath);
    return env;
}

private void reconcileRabbitMQCredentials(RabbitMQServerSpec spec) @trusted
{
    auto env = rabbitMQRuntimeEnvironment(spec);
    string output;
    Exception lastErr;
    auto deadline = Clock.currTime() + rabbitMQStartupTimeout;
    bool ready = false;

    while (Clock.currTime() < deadline)
    {
        try
        {
            output = runRabbitMQControl(spec.controlTarget, env, null, "list_users", "-q");
            ready = true;
            break;
        }
        catch (Exception e)
        {
            lastErr = e;
        }
        Thread.sleep(rabbitMQPollInterval);
    }

    if (!ready)
        throw new Exception("wait for rabbitmq control plane: " ~ (lastErr is null ? "timeout" : lastErr.msg));

    bool userExists = false;
    foreach (line; output.split('\n'))
    {
        auto fields = line.split();
        if (fields.length > 0 && fields[0] == spec.username)
        {
            userExists = true;
            break;
        }
    }

    auto command = userExists ? "change_password" : "add_user";
    try
    {
        runRabbitMQControl(spec.controlTarget, env, spec.password ~ "\n", command, spec.username);
    }
    catch (Exception e)
    {
        throw new Exception("configure rabbitmq credentials: " ~ e.msg);
    }

    runRabbitMQControl(spec.controlTarget, env, null, "set_user_tags", spec.username, "administrator");
    runRabbitMQControl(spec.controlTarget, env, null, "set_permissions", "-p", "/", spec.username, ".*", ".*", ".*");
}

private string runRabbitMQControl(string target, const(string)[] env, string stdinText, string[] args...) @trusted
{
    import std.string : join;

    string output;
    int status = 0;
    try
    {
        status = runServiceCommandSync(target, args, output, stdinText, null, env);
    }
    catch (Exception e)
    {
        throw new Exception(format("rabbitmqctl %s: %s (%s)", args.join(" "), e.msg, output.strip()));
    }

    if (status != 0)
        throw new Exception(format("rabbitmqctl %s: exit status %d (%s)", args.join(" "), status, output.strip()));

    return output;
}

/// Attempts graceful `rabbitmqctl shutdown` and falls back to forcefully stopping the PID on timeout.
void stopRabbitMQRuntime(RabbitMQRuntimeState state) @trusted
{
    RabbitMQServerSpec spec;
    spec.environmentName = state.environmentName;
    spec.target = state.target;
    spec.controlTarget = buildPath(dirName(state.target), "rabbitmqctl.bat");
    spec.erlangHome = state.erlangHome;
    spec.dataDir = state.dataDir;
    spec.runDir = state.runDir;
    spec.logPath = state.logPath;
    spec.port = state.port;
    spec.managementPort = state.managementPort;
    spec.env = currentEnvList();

    try
    {
        runRabbitMQControl(spec.controlTarget, rabbitMQRuntimeEnvironment(spec), null, "shutdown");
    }
    catch (Exception) {} // Ignore graceful shutdown error and verify/fallback below.

    auto deadline = Clock.currTime() + rabbitMQShutdownTimeout;
    while (Clock.currTime() < deadline)
    {
        if (!pingRabbitMQAddress(rabbitMQAddress(state.port)))
            return;
        Thread.sleep(rabbitMQPollInterval);
    }

    stopServicePID(state.pid);
}

int effectiveRabbitMQPort(const(RabbitMQConfig)* value) @safe pure nothrow @nogc
{
    if (value is null || value.port == 0)
        return DefaultRabbitMQPort;
    return value.port;
}

int effectiveRabbitMQManagementPort(const(RabbitMQConfig)* value) @safe pure nothrow @nogc
{
    if (value is null || value.managementPort == 0)
        return DefaultRabbitMQManagementPort;
    return value.managementPort;
}

string effectiveRabbitMQUsername(const(RabbitMQConfig)* value) @safe pure
{
    if (value is null || value.username.strip().length == 0)
        return DefaultRabbitMQUsername;
    return value.username.strip();
}

string effectiveRabbitMQPassword(const(RabbitMQConfig)* value) @safe pure
{
    if (value is null || value.password.strip().length == 0)
        return DefaultRabbitMQPassword;
    return value.password.strip();
}

string rabbitMQAddress(int port) @safe
{
    return joinHostPort(RabbitMQListenHost, port);
}

string rabbitMQURLForConfig(const(RabbitMQConfig)* value) @safe
{
    return "amqp://" ~ rabbitMQAddress(effectiveRabbitMQPort(value));
}

string rabbitMQManagementURLForConfig(const(RabbitMQConfig)* value) @safe
{
    return "http://" ~ rabbitMQAddress(effectiveRabbitMQManagementPort(value));
}

string rabbitMQStatePath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, "run", "rabbitmq", environmentName ~ ".json");
}

string rabbitMQRunPath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, "run", "rabbitmq", environmentName);
}

string rabbitMQDataPath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, "data", "rabbitmq", environmentName);
}

string rabbitMQLogPath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rabbitMQRunPath(rootDir, environmentName), "rabbitmq.log");
}

/// Computes a SHA-256 fingerprint for a RabbitMQ password without persisting plaintext.
string rabbitMQPasswordHash(string value) @safe pure
{
    return sha256Hex(cast(const(ubyte)[]) value.strip());
}

/// Loads persisted RabbitMQ state, returning `null` if the state file does not exist.
RabbitMQRuntimeState* loadRabbitMQState(string path) @trusted
{
    if (!exists(path) && !isSymlinkPath(path))
        return null;

    string raw;
    try
    {
        raw = readText(path);
    }
    catch (Exception e)
    {
        throw new Exception("read rabbitmq state: " ~ e.msg);
    }

    JSONValue root;
    try
    {
        root = parseJSON(raw);
    }
    catch (Exception e)
    {
        throw new Exception("decode rabbitmq state: " ~ e.msg);
    }

    auto st = new RabbitMQRuntimeState();
    st.environmentName = jsonStringField(root, "environment");
    st.version_ = jsonStringField(root, "version");
    st.port = cast(int) jsonLongField(root, "port", 0);
    st.managementPort = cast(int) jsonLongField(root, "management_port", 0);
    st.username = jsonStringField(root, "username");
    st.passwordHash = jsonStringField(root, "password_hash");
    st.pid = cast(int) jsonLongField(root, "pid", 0);
    st.target = jsonStringField(root, "target");
    st.erlangHome = jsonStringField(root, "erlang_home");
    st.dataDir = jsonStringField(root, "data_dir");
    st.runDir = jsonStringField(root, "run_dir");
    st.logPath = jsonStringField(root, "log_path");
    st.startedAt = parseServiceTimestamp(jsonStringField(root, "started_at"));
    return st;
}

/// Persists non-secret RabbitMQ state with `0600` file permissions.
void writeRabbitMQState(string path, RabbitMQRuntimeState state) @trusted
{
    JSONValue root = JSONValue(cast(JSONValue[string]) null);
    root["environment"] = state.environmentName;
    root["version"] = state.version_;
    root["port"] = state.port;
    root["management_port"] = state.managementPort;
    root["username"] = state.username;
    root["password_hash"] = state.passwordHash;
    root["pid"] = state.pid;
    root["target"] = state.target;
    root["erlang_home"] = state.erlangHome;
    root["data_dir"] = state.dataDir;
    root["run_dir"] = state.runDir;
    root["log_path"] = state.logPath;
    root["started_at"] = formatServiceTimestamp(state.startedAt);

    writeServiceJSONFile(path, root, octal!600);
}

RabbitMQRuntimeState* loadLiveRabbitMQState(
    string rootDir,
    string environmentName,
    bool delegate(string) ping = null
) @trusted
{
    auto path = rabbitMQStatePath(rootDir, environmentName);
    auto state = loadRabbitMQState(path);
    if (state is null)
        return null;

    if (rabbitMQPingFunc(ping)(rabbitMQAddress(state.port)))
        return state;

    removeRabbitMQStateFile(path);
    return null;
}

RabbitMQRuntimeState* loadLiveRabbitMQStateForEnvironment(
    string rootDir,
    Environment environment,
    bool delegate(string) ping = null
) @trusted
{
    auto state = loadLiveRabbitMQState(rootDir, environment.name, ping);
    if (state is null)
        return null;
    if (rabbitMQStateMatchesEnvironment(*state, environment))
        return state;

    throw new Exception(format(`rabbitmq for environment "%s" is already running with different settings`, environment.name));
}

bool rabbitMQStateMatchesEnvironment(RabbitMQRuntimeState state, Environment environment) @safe pure
{
    auto cfg = environment.rabbitMQ();
    return cfg !is null &&
        state.version_.strip() == cfg.version_.strip() &&
        state.port == effectiveRabbitMQPort(cfg) &&
        state.managementPort == effectiveRabbitMQManagementPort(cfg) &&
        state.username == effectiveRabbitMQUsername(cfg) &&
        state.passwordHash == rabbitMQPasswordHash(effectiveRabbitMQPassword(cfg));
}

bool pingRabbitMQAddress(string address) @trusted
{
    return pingTCPAddress(address, dur!"msecs"(150));
}

void waitForRabbitMQAddress(int port, Duration timeout, bool delegate(string) ping) @trusted
{
    auto p = rabbitMQPingFunc(ping);
    auto deadline = Clock.currTime() + timeout;
    while (Clock.currTime() < deadline)
    {
        if (p(rabbitMQAddress(port)))
            return;
        Thread.sleep(rabbitMQPollInterval);
    }

    throw new Exception(format("timed out waiting for %s", rabbitMQAddress(port)));
}

private bool delegate(string) rabbitMQPingFunc(bool delegate(string) ping) @trusted
{
    if (ping is null)
        return (string addr) => pingRabbitMQAddress(addr);
    return ping;
}

private string sanitizeRabbitMQNodeName(string value) @safe pure
{
    auto trimmed = value.strip().toLower();
    auto b = appender!string();
    foreach (char ch; trimmed)
    {
        if ((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_')
            b.put(ch);
        else
            b.put('_');
    }
    if (b.data.length == 0)
        return "default";
    return b.data;
}

private string readRabbitMQCookie(string path) @trusted
{
    if (!exists(path))
        return "";
    try
    {
        return readText(path).strip();
    }
    catch (Exception)
    {
        return "";
    }
}

unittest
{
    import std.algorithm : canFind;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;
    import polka.support.yaml : YamlValue;

    // TestRabbitMQDefaultsAndURLs
    assert(effectiveRabbitMQPort(null) == 5672);
    assert(effectiveRabbitMQManagementPort(null) == 15672);
    assert(rabbitMQURLForConfig(null) == "amqp://127.0.0.1:5672");
    assert(rabbitMQManagementURLForConfig(null) == "http://127.0.0.1:15672");

    // TestWriteRabbitMQStateDoesNotPersistPassword
    {
        auto root = createTempDir("polka-rmq-state-");
        scope (exit) rmdirRecurseQuiet(root);
        auto path = buildPath(root, "state.json");
        RabbitMQRuntimeState st;
        st.environmentName = "demo";
        st.version_ = "4.3";
        st.port = 5672;
        st.managementPort = 15672;
        st.username = "polka";
        st.passwordHash = rabbitMQPasswordHash("top-secret");
        st.startedAt = Clock.currTime(UTC());
        writeRabbitMQState(path, st);

        auto data = readText(path);
        assert(!data.canFind("top-secret"));
    }

    // TestRabbitMQStateMatchesEnvironmentIncludesCredentialsAndPorts
    {
        Environment env;
        env.name = "demo";
        env.tools = ["rabbitmq": "4.3"];
        env.settings = [
            "rabbitmq": [
                "port": YamlValue.fromInt(5673),
                "management-port": YamlValue.fromInt(15673),
                "username": YamlValue.fromString("polka"),
                "password": YamlValue.fromString("secret"),
            ],
        ];
        RabbitMQRuntimeState st;
        st.version_ = "4.3";
        st.port = 5673;
        st.managementPort = 15673;
        st.username = "polka";
        st.passwordHash = rabbitMQPasswordHash("secret");
        assert(rabbitMQStateMatchesEnvironment(st, env));

        env.setSettingString("rabbitmq", "password", "changed");
        assert(!rabbitMQStateMatchesEnvironment(st, env));
    }

    // TestPrepareRabbitMQRuntimeWritesPluginAndNoPasswordInConfig
    {
        auto runDir = createTempDir("polka-rmq-run-");
        scope (exit) rmdirRecurseQuiet(runDir);
        RabbitMQServerSpec spec;
        spec.environmentName = "demo";
        spec.dataDir = buildPath(runDir, "data");
        spec.runDir = buildPath(runDir, "run");
        spec.logPath = buildPath(runDir, "rabbitmq.log");
        spec.port = 5672;
        spec.managementPort = 15672;
        spec.username = "polka";
        spec.password = "secret";
        prepareRabbitMQRuntime(spec);

        auto configData = readText(buildPath(spec.runDir, "rabbitmq.conf"));
        assert(!configData.canFind("secret") && configData.canFind("127.0.0.1:5672"));
        auto plugins = readText(buildPath(spec.runDir, "enabled_plugins"));
        assert(plugins.canFind("rabbitmq_management"));
        assert(exists(buildPath(spec.runDir, ".erlang.cookie")));
    }

    // Platform guard on non-windows/amd64
    static if (currentOS() != "windows" || currentArch() != "amd64")
    {
        ServiceContext ctx;
        ctx.environment.name = "demo";
        ctx.environment.tools = ["rabbitmq": "4.3"];
        bool threwUnsupported = false;
        try { buildRabbitMQServerSpec(ctx); }
        catch (Exception e) { threwUnsupported = e.msg.canFind("windows/amd64"); }
        assert(threwUnsupported);
    }
}
