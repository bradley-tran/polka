module polka.service.phpmyadmin;

import std.datetime : Clock, SysTime, UTC;
import std.file : exists, readText, remove;
import std.format : format;
import std.json : JSONValue, parseJSON;
import std.path : buildPath;
import std.string : endsWith, icmp, strip, toLower;

import polka.config;
import polka.service.context;
import polka.service.phpmyadmin_storage;
import polka.service.process;
import polka.support.fs : isSymlinkPath, octal;
import polka.support.net : joinHostPort, splitHostPort;
import polka.tools.cache : jsonLongField, jsonStringField;

enum string managedPHPMyAdminStateDirectory = "run";
enum string managedPHPMyAdminStateSubdirectory = "phpmyadmin";
enum string defaultServeHostname = "localhost";
enum string serveProxyHost = "127.0.0.1";

/// Persisted runtime state shape used by web-backed services such as phpMyAdmin and `polka serve`.
struct ServeRuntimeState
{
    string environmentName;
    string version_;
    string serverKind;
    string serverScheme;
    string serverAddress;
    string docroot;
    string runtimeDir;
    string logPath;
    string backendLogPath;
    string configPath;
    string routerPath;
    int primaryPID = 0;
    int secondaryPID = 0;
    SysTime startedAt;
}

/// Discovered document root and front controller paths for serving an application.
struct AppLayout
{
    string docroot;
    string frontControllerRelative;
    string frontControllerWebPath;
    string frontControllerIndex;
}

/// Parsed scheme, listen/bind address, and TLS flag for a web endpoint.
struct Endpoint
{
    string scheme;
    string address;
    bool https = false;
}

/// Runtime hooks supplied by the web-server runner layer (`polka.cli`) to start,
/// probe, and stop phpMyAdmin's managed web server without introducing a circular import.
struct PHPMyAdminRuntimeHooks
{
    AppLayout delegate(string docroot) resolveLayout;
    ServeRuntimeState delegate(Environment environment, Endpoint endpoint, AppLayout layout, string runtimeDir) startServe;
    void delegate(ServeRuntimeState) stopServe;
    bool delegate(string) pingAddress;
    SysTime delegate() now;

    /// Returns a copy with unset delegates populated by conservative defaults.
    PHPMyAdminRuntimeHooks withDefaults() const @trusted
    {
        PHPMyAdminRuntimeHooks copy = this;
        if (copy.resolveLayout is null)
        {
            copy.resolveLayout = (string) {
                throw new Exception("phpmyadmin layout resolver is not configured");
            };
        }
        if (copy.startServe is null)
        {
            copy.startServe = (Environment, Endpoint, AppLayout, string) {
                throw new Exception("phpmyadmin web runtime starter is not configured");
            };
        }
        if (copy.stopServe is null)
            copy.stopServe = (ServeRuntimeState) {};
        if (copy.now is null)
            copy.now = () => Clock.currTime(UTC());
        return copy;
    }
}

/// Starts or reuses the managed phpMyAdmin web runtime for `ctx.environment`.
ServeRuntimeState ensureManagedPHPMyAdminStarted(
    ServiceContext ctx,
    PHPMyAdminRuntimeHooks hooks,
    out bool alreadyStarted
) @trusted
{
    hooks = hooks.withDefaults();
    auto environment = ctx.environment;
    auto pma = environment.phpMyAdmin();
    if (pma is null || pma.version_.strip().length == 0)
    {
        alreadyStarted = true;
        return ServeRuntimeState.init;
    }

    auto state = loadLivePHPMyAdminStateForEnvironment(ctx.rootDir, environment, hooks.pingAddress);
    if (state !is null)
    {
        alreadyStarted = true;
        return *state;
    }

    auto ver = pma.version_.strip();
    auto docroot = resolvePHPMyAdminDocroot(ctx.envsDir, ver);
    auto layout = hooks.resolveLayout(docroot);
    auto endpoint = phpMyAdminEndpoint(pma);
    auto runtimeDir = phpMyAdminRuntimeDir(ctx.rootDir, environment.name);

    auto startedState = hooks.startServe(environment, endpoint, layout, runtimeDir);
    startedState.environmentName = environment.name;
    startedState.version_ = ver;
    if (startedState.serverKind.strip().length == 0)
        startedState.serverKind = desiredServeKind(endpoint.https);
    if (startedState.serverScheme.strip().length == 0)
        startedState.serverScheme = endpoint.scheme;
    if (startedState.serverAddress.strip().length == 0)
        startedState.serverAddress = endpoint.address;
    if (startedState.docroot.strip().length == 0)
        startedState.docroot = layout.docroot;
    if (startedState.startedAt == SysTime.init)
        startedState.startedAt = hooks.now().toUTC();
    else
        startedState.startedAt = startedState.startedAt.toUTC();

    try
    {
        writePHPMyAdminState(phpMyAdminStatePath(ctx.rootDir, environment.name), startedState);
    }
    catch (Exception e)
    {
        try
        {
            hooks.stopServe(startedState);
        }
        catch (Exception) {} // Best-effort cleanup if persisting state fails.
        throw e;
    }

    alreadyStarted = false;
    return startedState;
}

/// Stops a running managed phpMyAdmin web server and removes its state file.
ServeRuntimeState stopManagedPHPMyAdmin(
    ServiceContext ctx,
    PHPMyAdminRuntimeHooks hooks,
    out bool alreadyStopped
) @trusted
{
    hooks = hooks.withDefaults();

    auto state = loadLivePHPMyAdminState(ctx.rootDir, ctx.environment.name, hooks.pingAddress);
    if (state is null)
    {
        alreadyStopped = true;
        return ServeRuntimeState.init;
    }

    hooks.stopServe(*state);
    removePHPMyAdminStateFile(phpMyAdminStatePath(ctx.rootDir, ctx.environment.name));

    alreadyStopped = false;
    return *state;
}

private void removePHPMyAdminStateFile(string path) @trusted
{
    if (!exists(path) && !isSymlinkPath(path))
        return;
    try
    {
        remove(path);
    }
    catch (Exception e)
    {
        throw new Exception("remove phpmyadmin state: " ~ e.msg);
    }
}

/// Loads live phpMyAdmin state and rejects version, scheme, server-kind, or address drift.
ServeRuntimeState* loadLivePHPMyAdminStateForEnvironment(
    string rootDir,
    Environment environment,
    bool delegate(string) ping = null
) @trusted
{
    auto state = loadLivePHPMyAdminState(rootDir, environment.name, ping);
    if (state is null)
        return null;
    if (phpMyAdminStateMatchesEnvironment(*state, environment))
        return state;

    auto pma = environment.phpMyAdmin();
    throw new Exception(format(
        `environment "%s" still has a running phpmyadmin %s at %s, but the current phpmyadmin is %s at %s; stop the running phpmyadmin first`,
        environment.name,
        state.version_,
        serveStateURL(*state),
        pma is null ? "" : pma.version_.strip(),
        endpointURL(phpMyAdminEndpoint(pma))
    ));
}

/// Loads persisted phpMyAdmin state if `ping` confirms it is live, pruning stale files otherwise.
ServeRuntimeState* loadLivePHPMyAdminState(
    string rootDir,
    string environmentName,
    bool delegate(string) ping = null
) @trusted
{
    auto path = phpMyAdminStatePath(rootDir, environmentName);
    if (!exists(path) && !isSymlinkPath(path))
        return null;

    auto state = loadPHPMyAdminState(path);
    if (state !is null && state.serverAddress.strip().length > 0 && phpMyAdminPingFunc(ping)(serveStateProbeAddress(*state)))
        return state;

    if (exists(path) || isSymlinkPath(path))
    {
        try
        {
            remove(path);
        }
        catch (Exception e)
        {
            throw new Exception("remove stale phpmyadmin state: " ~ e.msg);
        }
    }
    return null;
}

/// Reads and decodes a persisted `ServeRuntimeState` JSON file for phpMyAdmin.
ServeRuntimeState* loadPHPMyAdminState(string path) @trusted
{
    auto raw = readText(path);
    JSONValue root;
    try
    {
        root = parseJSON(raw);
    }
    catch (Exception e)
    {
        throw new Exception(format("decode phpmyadmin state %s: %s", path, e.msg));
    }

    auto st = new ServeRuntimeState();
    st.environmentName = jsonStringField(root, "environment");
    st.version_ = jsonStringField(root, "version");
    st.serverKind = jsonStringField(root, "server_kind");
    st.serverScheme = jsonStringField(root, "server_scheme");
    st.serverAddress = jsonStringField(root, "server_address");
    st.docroot = jsonStringField(root, "docroot");
    st.runtimeDir = jsonStringField(root, "runtime_dir");
    st.logPath = jsonStringField(root, "log_path");
    st.backendLogPath = jsonStringField(root, "backend_log_path");
    st.configPath = jsonStringField(root, "config_path");
    st.routerPath = jsonStringField(root, "router_path");
    st.primaryPID = cast(int) jsonLongField(root, "primary_pid", 0);
    st.secondaryPID = cast(int) jsonLongField(root, "secondary_pid", 0);
    st.startedAt = parseServiceTimestamp(jsonStringField(root, "started_at"));
    return st;
}

/// Writes a phpMyAdmin `ServeRuntimeState` JSON file with 0644 permissions.
void writePHPMyAdminState(string path, ServeRuntimeState state) @trusted
{
    JSONValue root = JSONValue(cast(JSONValue[string]) null);
    root["environment"] = state.environmentName;
    if (state.version_.length > 0)
        root["version"] = state.version_;
    root["server_kind"] = state.serverKind;
    if (state.serverScheme.length > 0)
        root["server_scheme"] = state.serverScheme;
    root["server_address"] = state.serverAddress;
    root["docroot"] = state.docroot;
    if (state.runtimeDir.length > 0)
        root["runtime_dir"] = state.runtimeDir;
    if (state.logPath.length > 0)
        root["log_path"] = state.logPath;
    if (state.backendLogPath.length > 0)
        root["backend_log_path"] = state.backendLogPath;
    if (state.configPath.length > 0)
        root["config_path"] = state.configPath;
    if (state.routerPath.length > 0)
        root["router_path"] = state.routerPath;
    root["primary_pid"] = state.primaryPID;
    if (state.secondaryPID != 0)
        root["secondary_pid"] = state.secondaryPID;
    root["started_at"] = formatServiceTimestamp(state.startedAt);

    writeServiceJSONFile(path, root, octal!644);
}

/// Reports whether `state` still matches the configured phpMyAdmin version, scheme, kind, and address.
bool phpMyAdminStateMatchesEnvironment(ServeRuntimeState state, Environment environment) @safe
{
    auto pma = environment.phpMyAdmin();
    if (pma is null)
        return false;

    auto endpoint = phpMyAdminEndpoint(pma);
    return state.version_.strip() == pma.version_.strip() &&
        icmp(state.serverKind.strip(), desiredServeKind(endpoint.https)) == 0 &&
        icmp(state.serverAddress.strip(), endpoint.address.strip()) == 0 &&
        icmp(state.serverScheme.strip(), endpoint.scheme.strip()) == 0;
}

string phpMyAdminStatePath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedPHPMyAdminStateDirectory, managedPHPMyAdminStateSubdirectory, environmentName ~ ".json");
}

string phpMyAdminRuntimeDir(string rootDir, string environmentName) @safe pure
{
    auto name = environmentName.strip();
    if (name.length == 0)
        name = "current";
    return buildPath(rootDir, managedPHPMyAdminStateDirectory, managedPHPMyAdminStateSubdirectory, name);
}

string phpMyAdminUIURLForConfig(const(PHPMyAdminConfig)* phpMyAdmin) @safe
{
    if (phpMyAdmin is null)
        return "";
    return phpMyAdminUIScheme(phpMyAdmin.https) ~ "://" ~ phpMyAdminAddress(effectivePHPMyAdminPort(phpMyAdmin));
}

Endpoint phpMyAdminEndpoint(const(PHPMyAdminConfig)* phpMyAdmin) @safe
{
    bool https = phpMyAdmin !is null && phpMyAdmin.https;
    auto scheme = phpMyAdminUIScheme(https);
    return Endpoint(scheme, phpMyAdminAddress(effectivePHPMyAdminPort(phpMyAdmin)), https);
}

string phpMyAdminUIScheme(bool https) @safe pure nothrow @nogc
{
    return https ? "https" : "http";
}

string desiredServeKind(bool useNginx) @safe pure nothrow @nogc
{
    return useNginx ? "nginx" : "php";
}

string endpointURL(Endpoint endpoint) @safe pure
{
    auto scheme = endpoint.scheme.strip();
    if (scheme.length == 0)
        scheme = "http";
    return scheme ~ "://" ~ endpoint.address.strip();
}

string serveStateURL(ServeRuntimeState state) @safe pure
{
    return endpointURL(Endpoint(state.serverScheme.strip(), state.serverAddress.strip(), false));
}

string serveStateProbeAddress(ServeRuntimeState state) @safe
{
    return serveProbeAddress(state.serverAddress.strip());
}

/// Maps wildcard `*.localhost:<port>` hosts to loopback `127.0.0.1:<port>` for reliable TCP probing.
string serveProbeAddress(string address) @safe
{
    string host, port;
    if (!splitHostPort(address.strip(), host, port))
        return address;
    auto trimmed = host.strip().toLower();
    if (trimmed.endsWith("." ~ defaultServeHostname))
        return joinHostPort(serveProxyHost, port);
    return address;
}

private bool delegate(string) phpMyAdminPingFunc(bool delegate(string) ping) @trusted
{
    if (ping is null)
        return (string) => false;
    return ping;
}

unittest
{
    import std.file : mkdirRecurse, write;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;
    import polka.support.yaml : YamlValue;

    assert(serveProbeAddress("app.localhost:8000") == "127.0.0.1:8000");
    assert(serveProbeAddress("127.0.0.1:8081") == "127.0.0.1:8081");
    assert(desiredServeKind(false) == "php" && desiredServeKind(true) == "nginx");

    // Round-trip start, drift detection, and stop using runtime hooks.
    auto rootDir = createTempDir("polka-pma-run-");
    scope (exit) rmdirRecurseQuiet(rootDir);
    auto envsDir = buildPath(rootDir, "envs");
    auto docroot = buildPath(envsDir, toolPHPMyAdmin, "5.2");
    mkdirRecurse(docroot);
    write(buildPath(docroot, "index.php"), "<?php\n");

    bool[string] live;
    ServiceContext ctx;
    ctx.rootDir = rootDir;
    ctx.envsDir = envsDir;
    ctx.environment.name = "demo";
    ctx.environment.https = true;
    ctx.environment.tools = ["phpmyadmin": "5.2"];
    ctx.environment.settings = ["phpmyadmin": ["port": YamlValue.fromInt(8089)]];

    PHPMyAdminRuntimeHooks hooks;
    hooks.resolveLayout = (string d) => AppLayout(d, "index.php", "/index.php", "index.php");
    hooks.startServe = (Environment env, Endpoint ep, AppLayout layout, string runDir) {
        live[ep.address] = true;
        ServeRuntimeState st;
        st.primaryPID = 9001;
        st.runtimeDir = runDir;
        return st;
    };
    hooks.stopServe = (ServeRuntimeState st) { live[st.serverAddress] = false; };
    hooks.pingAddress = (string addr) => addr in live ? live[addr] : false;

    bool alreadyStarted = false;
    auto started = ensureManagedPHPMyAdminStarted(ctx, hooks, alreadyStarted);
    assert(!alreadyStarted && started.serverKind == "nginx" && started.serverScheme == "https" && started.serverAddress == "127.0.0.1:8089");

    // Idempotent second call while running.
    ensureManagedPHPMyAdminStarted(ctx, hooks, alreadyStarted);
    assert(alreadyStarted);

    // Stop clears persisted state.
    bool alreadyStopped = false;
    auto stopped = stopManagedPHPMyAdmin(ctx, hooks, alreadyStopped);
    assert(!alreadyStopped && stopped.primaryPID == 9001);
    assert(!exists(phpMyAdminStatePath(rootDir, "demo")));
}
