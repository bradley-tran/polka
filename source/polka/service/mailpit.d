module polka.service.mailpit;

import core.thread : Thread;
import core.time : Duration, dur;
import std.datetime : Clock, SysTime, UTC;
import std.file : exists, readText, remove;
import std.format : format;
import std.json : JSONValue, parseJSON;
import std.path : buildPath;
import std.string : icmp, strip;

import polka.config;
import polka.service.context;
import polka.service.mailpit_config;
import polka.service.process;
import polka.support.fs : isSymlinkPath, octal;
import polka.support.net : pingTCPAddress;
import polka.tools.cache : jsonLongField, jsonStringField;

enum string managedMailpitStateDirectory = "run";
enum string managedMailpitStateSubdirectory = "mailpit";
enum string managedMailpitLogFileName = "mailpit.log";
enum Duration managedMailpitPollInterval = dur!"msecs"(100);
enum Duration managedMailpitStartupTimeout = dur!"seconds"(5);
enum Duration managedMailpitShutdownTimeout = dur!"seconds"(5);

/// Persisted runtime state for one Mailpit instance.
struct MailpitRuntimeState
{
    string environmentName;
    string version_;
    int smtpPort = 0;
    int uiPort = 0;
    string uiScheme;
    int pid = 0;
    string logPath;
    string tlsCertPath;
    string tlsKeyPath;
    SysTime startedAt;
}

/// Describes a Mailpit process launch.
struct MailpitServerSpec
{
    string environmentName;
    string version_;
    string target;
    int smtpPort = 0;
    int uiPort = 0;
    bool https = false;
    string tlsCertPath;
    string tlsKeyPath;
    string logPath;
    string[] env;
}

/// Result returned when starting a Mailpit server process.
struct MailpitStartResult
{
    int pid = 0;
}

/// Hooks allowing tests to intercept Mailpit process lifecycle calls.
struct MailpitRuntimeHooks
{
    MailpitStartResult delegate(MailpitServerSpec) startServer;
    void delegate(MailpitRuntimeState) stopServer;
    bool delegate(string) pingAddress;
    SysTime delegate() now;

    /// Returns a copy with unset hooks filled in by production implementations.
    MailpitRuntimeHooks withDefaults() const @trusted
    {
        MailpitRuntimeHooks copy = this;
        if (copy.startServer is null)
            copy.startServer = (MailpitServerSpec spec) => startMailpitServer(spec);
        if (copy.stopServer is null)
            copy.stopServer = (MailpitRuntimeState st) { stopMailpitRuntime(st); };
        if (copy.pingAddress is null)
            copy.pingAddress = (string addr) => pingMailpitAddress(addr);
        if (copy.now is null)
            copy.now = () => Clock.currTime(UTC());
        return copy;
    }
}

/// Ensures Mailpit is started for `ctx.environment` if configured, returning `(state, alreadyStarted)`.
MailpitRuntimeState ensureManagedMailpitStarted(
    ServiceContext ctx,
    MailpitRuntimeHooks hooks,
    out bool alreadyStarted
) @trusted
{
    hooks = hooks.withDefaults();
    auto cfg = ctx.environment.mailpit();
    if (cfg is null || cfg.version_.strip().length == 0)
    {
        alreadyStarted = true;
        return MailpitRuntimeState.init;
    }

    auto state = loadLiveMailpitStateForEnvironment(ctx.rootDir, ctx.environment, hooks.pingAddress);
    if (state !is null)
    {
        alreadyStarted = true;
        return *state;
    }

    auto spec = buildMailpitServerSpec(ctx);
    auto result = hooks.startServer(spec);

    MailpitRuntimeState startedState;
    startedState.environmentName = ctx.environment.name;
    startedState.version_ = spec.version_;
    startedState.smtpPort = spec.smtpPort;
    startedState.uiPort = spec.uiPort;
    startedState.uiScheme = mailpitUIScheme(spec.https);
    startedState.pid = result.pid;
    startedState.logPath = spec.logPath;
    startedState.tlsCertPath = spec.tlsCertPath;
    startedState.tlsKeyPath = spec.tlsKeyPath;
    startedState.startedAt = hooks.now().toUTC();

    try
    {
        writeMailpitState(mailpitStatePath(ctx.rootDir, ctx.environment.name), startedState);
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

/// Gracefully stops the active environment's Mailpit service and clears persisted state.
MailpitRuntimeState stopManagedMailpit(
    ServiceContext ctx,
    MailpitRuntimeHooks hooks,
    out bool alreadyStopped
) @trusted
{
    hooks = hooks.withDefaults();

    auto state = loadLiveMailpitState(ctx.rootDir, ctx.environment.name, hooks.pingAddress);
    if (state is null)
    {
        alreadyStopped = true;
        return MailpitRuntimeState.init;
    }

    hooks.stopServer(*state);
    removeMailpitStateFile(mailpitStatePath(ctx.rootDir, ctx.environment.name));

    alreadyStopped = false;
    return *state;
}

private void removeMailpitStateFile(string path) @trusted
{
    if (!exists(path) && !isSymlinkPath(path))
        return;
    try
    {
        remove(path);
    }
    catch (Exception e)
    {
        throw new Exception("remove mailpit state: " ~ e.msg);
    }
}

/// Resolves the Mailpit binary, TLS assets (when HTTPS enabled), and ports into a launch spec.
MailpitServerSpec buildMailpitServerSpec(ServiceContext ctx) @trusted
{
    auto environment = ctx.environment;
    auto cfg = environment.mailpit();
    if (cfg is null)
        throw new Exception(format(`environment "%s" does not define mailpit`, environment.name));

    auto ver = cfg.version_.strip();
    if (ver.length == 0)
        throw new Exception(format(`environment "%s" defines mailpit but does not define a version`, environment.name));

    auto target = ctx.resolveInstalledTool(toolMailpit, ver);
    auto env = ctx.runtimeEnv();
    string certPath, keyPath;
    if (cfg.https)
    {
        auto kp = ctx.tlsCertificate(MailpitListenHost);
        certPath = kp.certPath;
        keyPath = kp.keyPath;
    }

    MailpitServerSpec spec;
    spec.environmentName = environment.name;
    spec.version_ = ver;
    spec.target = target;
    spec.smtpPort = effectiveMailpitSMTPPort(cfg);
    spec.uiPort = effectiveMailpitUIPort(cfg);
    spec.https = cfg.https;
    spec.tlsCertPath = certPath;
    spec.tlsKeyPath = keyPath;
    spec.logPath = mailpitLogPath(ctx.rootDir, environment.name);
    spec.env = env;
    return spec;
}

/// Launches Mailpit and waits until both SMTP and UI ports accept TCP connections.
MailpitStartResult startMailpitServer(MailpitServerSpec spec) @trusted
{
    auto logFile = openServiceLog(spec.logPath);
    scope (exit)
    {
        if (logFile.isOpen)
            logFile.close();
    }

    foreach (address; [mailpitAddress(spec.smtpPort), mailpitAddress(spec.uiPort)])
    {
        if (pingMailpitAddress(address))
            throw new Exception(format("mailpit cannot start because %s is already in use", address));
    }

    int pid = 0;
    try
    {
        pid = spawnDetachedServiceProcess(spec.target, mailpitServerArgs(spec), logFile, spec.env);
    }
    catch (Exception e)
    {
        throw new Exception("start mailpit: " ~ e.msg);
    }

    try
    {
        waitForMailpitAddresses(spec.smtpPort, spec.uiPort, managedMailpitStartupTimeout, null);
    }
    catch (Exception e)
    {
        try { stopServicePID(pid); } catch (Exception) {} // Best-effort teardown on startup timeout.
        throw new Exception(format("start mailpit on smtp %s and ui %s: %s (see %s)", mailpitAddress(spec.smtpPort), mailpitAddress(spec.uiPort), e.msg, spec.logPath));
    }

    return MailpitStartResult(pid);
}

/// Builds the CLI flags passed to `mailpit`.
string[] mailpitServerArgs(MailpitServerSpec spec) @safe
{
    auto args = [
        "--smtp", mailpitAddress(spec.smtpPort),
        "--listen", mailpitAddress(spec.uiPort),
    ];
    if (spec.https)
    {
        args ~= [
            "--ui-tls-cert", spec.tlsCertPath,
            "--ui-tls-key", spec.tlsKeyPath,
            "--smtp-tls-cert", spec.tlsCertPath,
            "--smtp-tls-key", spec.tlsKeyPath,
        ];
    }
    return args;
}

/// Terminates the Mailpit process and waits for its SMTP and UI sockets to close.
void stopMailpitRuntime(MailpitRuntimeState state) @trusted
{
    stopServicePID(state.pid);

    auto deadline = Clock.currTime() + managedMailpitShutdownTimeout;
    while (Clock.currTime() < deadline)
    {
        if (!mailpitStateIsLive(state, null))
            return;
        Thread.sleep(managedMailpitPollInterval);
    }
    if (!mailpitStateIsLive(state, null))
        return;

    throw new Exception(format("mailpit did not stop listening on %s or %s within %s", mailpitAddress(state.smtpPort), mailpitAddress(state.uiPort), managedMailpitShutdownTimeout));
}

/// Loads live Mailpit state and fails if the running version/ports/HTTPS mode drifted from `environment`.
MailpitRuntimeState* loadLiveMailpitStateForEnvironment(
    string rootDir,
    Environment environment,
    bool delegate(string) ping = null
) @trusted
{
    auto state = loadLiveMailpitState(rootDir, environment.name, ping);
    if (state is null)
        return null;
    if (mailpitStateMatchesEnvironment(*state, environment))
        return state;

    auto cfg = environment.mailpit();
    throw new Exception(format(
        `environment "%s" still has a running mailpit %s on smtp %s and ui %s, but the current mailpit is %s; stop the running mailpit first`,
        environment.name,
        state.version_,
        mailpitAddress(state.smtpPort),
        mailpitAddress(state.uiPort),
        cfg is null ? "" : cfg.version_.strip()
    ));
}

/// Loads persisted Mailpit state if still live, removing stale files otherwise.
MailpitRuntimeState* loadLiveMailpitState(
    string rootDir,
    string environmentName,
    bool delegate(string) ping = null
) @trusted
{
    auto path = mailpitStatePath(rootDir, environmentName);
    if (!exists(path) && !isSymlinkPath(path))
        return null;

    auto state = loadMailpitState(path);
    if (state !is null && mailpitStateIsLive(*state, mailpitPingFunc(ping)))
        return state;

    removeMailpitStateFile(path);
    return null;
}

/// Reads and decodes a persisted Mailpit state JSON file.
MailpitRuntimeState* loadMailpitState(string path) @trusted
{
    auto raw = readText(path);
    JSONValue root;
    try
    {
        root = parseJSON(raw);
    }
    catch (Exception e)
    {
        throw new Exception(format("decode mailpit state %s: %s", path, e.msg));
    }

    auto st = new MailpitRuntimeState();
    st.environmentName = jsonStringField(root, "environment");
    st.version_ = jsonStringField(root, "version");
    st.smtpPort = cast(int) jsonLongField(root, "smtp_port", 0);
    st.uiPort = cast(int) jsonLongField(root, "ui_port", 0);
    st.uiScheme = jsonStringField(root, "ui_scheme");
    st.pid = cast(int) jsonLongField(root, "pid", 0);
    st.logPath = jsonStringField(root, "log_path");
    st.tlsCertPath = jsonStringField(root, "tls_cert_path");
    st.tlsKeyPath = jsonStringField(root, "tls_key_path");
    st.startedAt = parseServiceTimestamp(jsonStringField(root, "started_at"));
    return st;
}

/// Persists `state` to `path`.
void writeMailpitState(string path, MailpitRuntimeState state) @trusted
{
    JSONValue root = JSONValue(cast(JSONValue[string]) null);
    root["environment"] = state.environmentName;
    root["version"] = state.version_;
    root["smtp_port"] = state.smtpPort;
    root["ui_port"] = state.uiPort;
    if (state.uiScheme.length > 0)
        root["ui_scheme"] = state.uiScheme;
    root["pid"] = state.pid;
    if (state.logPath.length > 0)
        root["log_path"] = state.logPath;
    if (state.tlsCertPath.length > 0)
        root["tls_cert_path"] = state.tlsCertPath;
    if (state.tlsKeyPath.length > 0)
        root["tls_key_path"] = state.tlsKeyPath;
    root["started_at"] = formatServiceTimestamp(state.startedAt);

    writeServiceJSONFile(path, root, octal!644);
}

string mailpitStatePath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedMailpitStateDirectory, managedMailpitStateSubdirectory, environmentName ~ ".json");
}

string mailpitLogPath(string rootDir, string environmentName) @safe pure
{
    return buildPath(toolLogRoot(rootDir, toolMailpit, environmentName), managedMailpitLogFileName);
}

string mailpitUIURL(MailpitRuntimeState state) @trusted
{
    MailpitConfig cfg;
    cfg.uiPort = state.uiPort;
    cfg.https = mailpitStateHTTPS(state);
    return mailpitUIURLForConfig(&cfg);
}

string mailpitUIURLForConfig(const(MailpitConfig)* mailpit) @safe
{
    if (mailpit is null)
        return "";
    return mailpitUIScheme(mailpit.https) ~ "://" ~ mailpitAddress(effectiveMailpitUIPort(mailpit));
}

string mailpitUIScheme(bool https) @safe pure nothrow @nogc
{
    return https ? "https" : "http";
}

void waitForMailpitAddresses(int smtpPort, int uiPort, Duration timeout, bool delegate(string) ping) @trusted
{
    auto deadline = Clock.currTime() + timeout;
    while (Clock.currTime() < deadline)
    {
        if (mailpitPortsAreLive(smtpPort, uiPort, ping))
            return;
        Thread.sleep(managedMailpitPollInterval);
    }

    throw new Exception(format("server did not start listening within %s", timeout));
}

bool mailpitStateMatchesEnvironment(MailpitRuntimeState state, Environment environment) @safe pure
{
    auto cfg = environment.mailpit();
    if (cfg is null)
        return false;

    return state.version_.strip() == cfg.version_.strip() &&
        state.smtpPort == effectiveMailpitSMTPPort(cfg) &&
        state.uiPort == effectiveMailpitUIPort(cfg) &&
        mailpitStateHTTPS(state) == cfg.https;
}

bool mailpitStateHTTPS(MailpitRuntimeState state) @safe pure
{
    return icmp(state.uiScheme.strip(), "https") == 0 || state.tlsCertPath.strip().length > 0;
}

bool mailpitStateIsLive(MailpitRuntimeState state, bool delegate(string) ping = null) @trusted
{
    return servicePIDIsLive(state.pid) && mailpitPortsAreLive(state.smtpPort, state.uiPort, ping);
}

bool mailpitPortsAreLive(int smtpPort, int uiPort, bool delegate(string) ping = null) @trusted
{
    auto p = mailpitPingFunc(ping);
    return p(mailpitAddress(smtpPort)) && p(mailpitAddress(uiPort));
}

bool pingMailpitAddress(string address) @trusted
{
    return pingTCPAddress(address, managedMailpitPollInterval);
}

private bool delegate(string) mailpitPingFunc(bool delegate(string) ping) @trusted
{
    if (ping is null)
        return (string addr) => pingMailpitAddress(addr);
    return ping;
}

unittest
{
    import std.algorithm : canFind;
    import std.socket : InternetAddress, TcpSocket;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    // TestLoadLiveMailpitStateRemovesStateWhenPIDIsNotLive
    {
        auto root = createTempDir("polka-mailpit-stale-");
        scope (exit) rmdirRecurseQuiet(root);
        auto statePath = mailpitStatePath(root, "demo");
        MailpitRuntimeState st;
        st.environmentName = "demo";
        st.version_ = "1.30";
        st.smtpPort = 1025;
        st.uiPort = 8025;
        writeMailpitState(statePath, st);

        auto loaded = loadLiveMailpitState(root, "demo", (string) => true);
        assert(loaded is null);
        assert(!exists(statePath));
    }

    // TestStartMailpitServerRejectsOccupiedPort
    {
        auto listener = new TcpSocket();
        scope (exit) listener.close();
        listener.bind(new InternetAddress("127.0.0.1", 0));
        listener.listen(4);
        auto boundPort = (cast(InternetAddress) listener.localAddress).port;

        auto logRoot = createTempDir("polka-mailpit-port-");
        scope (exit) rmdirRecurseQuiet(logRoot);
        MailpitServerSpec spec;
        spec.smtpPort = boundPort;
        spec.uiPort = 8025;
        spec.logPath = mailpitLogPath(logRoot, "demo");

        bool threwOccupied = false;
        try { startMailpitServer(spec); }
        catch (Exception e) { threwOccupied = e.msg.canFind("already in use"); }
        assert(threwOccupied);
    }
}
