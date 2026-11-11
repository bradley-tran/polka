module polka.cli.serve_runtime;

// Webserver-agnostic serve runtime machinery shared by every managed webserver
// kind: runtime/state directory layout, serve state persistence, endpoint &
// docroot resolution, startup probing, process shutdown, log handling, and the
// shared static MIME table.

import core.thread : Thread;
import core.time : Duration, dur;
import std.algorithm : canFind;
import std.conv : to;
import std.datetime : Clock, SysTime, UTC;
import std.exception : collectException;
import std.file : exists, isDir, isFile, mkdirRecurse, readText, remove;
import std.format : format;
import std.functional : toDelegate;
import std.json : JSONValue, parseJSON;
import std.path : baseName, buildPath, dirName, extension, isAbsolute;
import std.process : execute;
import std.socket : InternetAddress, TcpSocket;
import std.stdio : File;
import std.string : icmp, strip, toLower;

import polka.backend :
    Environment,
    hasPHPCLI,
    primaryPHPVersion,
    ServerConfig,
    Store;
import polka.cli.types :
    isMissingFileError;
import polka.config :
    ServerTypeApache,
    ServerTypeFrankenPHP,
    ServerTypeNginx,
    ServerTypePHP;
import polka.service :
    AppLayout,
    Endpoint,
    endpointURL,
    formatServiceTimestamp,
    parseServiceTimestamp,
    serveProbeAddressService = serveProbeAddress,
    ServeRuntimeState,
    serveStateProbeAddressService = serveStateProbeAddress,
    serveStateURLService = serveStateURL,
    writeServiceJSONFile;
import polka.support.fs : cleanPath, currentOS, octal, toSlashPath;
import polka.support.net : joinHostPort, pingTCPAddress, splitHostPort;
import polka.tools.cache : jsonLongField, jsonStringField;

enum string defaultServeHostname = "localhost";
enum int defaultServePort = 8000;
enum Duration servePollInterval = dur!"msecs"(100);
enum Duration serveStartupTimeout = dur!"seconds"(5);
enum string serveRuntimeRoot = "run";
enum string serveRuntimeSubdir = "serve";
enum string serveStateFileName = "state.json";
enum string serveLogFileName = "serve.log";
enum string serveProxyHost = "127.0.0.1";
enum Duration serveShutdownTimeout = dur!"seconds"(5);

/// Test seams for the shared serve runtime helpers.
void delegate(ServeRuntimeState) stopServeRuntimeFunc;
bool delegate(string) pingServeAddressFunc;

/// Alias matching Go's `serveAppLayout` and `serverEndpoint` naming for readability.
alias serveAppLayout = AppLayout;
alias serverEndpoint = Endpoint;

/// Calls `stopServeRuntimeFunc` or defaults to `stopServeRuntime`.
void callStopServeRuntime(ServeRuntimeState state) @trusted
{
    if (stopServeRuntimeFunc !is null)
    {
        stopServeRuntimeFunc(state);
        return;
    }
    stopServeRuntime(state);
}

/// Calls `pingServeAddressFunc` or defaults to `pingServeAddress`.
bool callPingServeAddress(string address) @trusted
{
    if (pingServeAddressFunc !is null)
        return pingServeAddressFunc(address);
    return pingServeAddress(address);
}

/// Maps one content type to the file extensions served with it.
struct ServeStaticMIMEType
{
    string contentType;
    const(string)[] extensions;
}

/// Shared static-file MIME table rendered into the PHP router, nginx config,
/// and Apache config so all webservers agree.
immutable ServeStaticMIMEType[] serveStaticMIMETypes = [
    ServeStaticMIMEType("text/html", ["html", "htm", "shtml"]),
    ServeStaticMIMEType("text/css", ["css"]),
    ServeStaticMIMEType("text/xml", ["xml"]),
    ServeStaticMIMEType("image/gif", ["gif"]),
    ServeStaticMIMEType("image/jpeg", ["jpeg", "jpg"]),
    ServeStaticMIMEType("application/javascript", ["js", "mjs"]),
    ServeStaticMIMEType("application/atom+xml", ["atom"]),
    ServeStaticMIMEType("application/rss+xml", ["rss"]),
    ServeStaticMIMEType("text/mathml", ["mml"]),
    ServeStaticMIMEType("text/plain", ["txt"]),
    ServeStaticMIMEType("text/vnd.sun.j2me.app-descriptor", ["jad"]),
    ServeStaticMIMEType("text/vnd.wap.wml", ["wml"]),
    ServeStaticMIMEType("text/x-component", ["htc"]),
    ServeStaticMIMEType("image/avif", ["avif"]),
    ServeStaticMIMEType("image/png", ["png"]),
    ServeStaticMIMEType("image/svg+xml", ["svg", "svgz"]),
    ServeStaticMIMEType("image/tiff", ["tif", "tiff"]),
    ServeStaticMIMEType("image/webp", ["webp"]),
    ServeStaticMIMEType("image/x-icon", ["ico"]),
    ServeStaticMIMEType("font/woff", ["woff"]),
    ServeStaticMIMEType("font/woff2", ["woff2"]),
    ServeStaticMIMEType("application/json", ["json", "map"]),
    ServeStaticMIMEType("application/pdf", ["pdf"]),
    ServeStaticMIMEType("application/wasm", ["wasm"]),
    ServeStaticMIMEType("application/xml", ["xsl", "xslt"]),
    ServeStaticMIMEType("application/zip", ["zip"]),
    ServeStaticMIMEType("application/octet-stream", ["bin", "exe", "dll"]),
];

/// Locates the `php-cgi` executable installed next to the PHP CLI.
string resolvePHPCGITarget(string phpTarget) @trusted
{
    auto trimmed = phpTarget.strip();
    if (trimmed.length == 0)
        throw new Exception("php target cannot be empty");

    string[] searchDirs = [dirName(trimmed)];
    if (icmp(baseName(searchDirs[0]), "bin") == 0)
        searchDirs ~= dirName(searchDirs[0]);

    string[] candidates;
    foreach (dir; searchDirs)
    {
        if (currentOS() == "windows")
        {
            candidates ~= [
                buildPath(dir, "php-cgi.exe"),
                buildPath(dir, "php-cgi.cmd"),
                buildPath(dir, "php-cgi.bat"),
            ];
            continue;
        }
        candidates ~= [
            buildPath(dir, "php-cgi"),
            buildPath(dir, "bin", "php-cgi"),
        ];
    }

    foreach (candidate; candidates)
    {
        try
        {
            if (exists(candidate) && !isDir(candidate))
                return candidate;
        }
        catch (Exception e)
        {
            throw new Exception(format("stat php-cgi candidate %s: %s", candidate, e.msg));
        }
    }

    throw new Exception(format("php-cgi executable was not found next to %s", phpTarget));
}

/// Rewrites subdomain-of-localhost hostnames to the loopback proxy host so
/// startup and shutdown probes work without DNS support.
string serveProbeAddress(string address) @safe
{
    return serveProbeAddressService(address);
}

/// Grabs a free loopback port for the `php-cgi` upstream.
string reserveServeBackendAddress() @trusted
{
    TcpSocket listener;
    try
    {
        listener = new TcpSocket();
        listener.bind(new InternetAddress(serveProxyHost, 0));
        auto bound = cast(InternetAddress) listener.localAddress;
        auto addr = joinHostPort(serveProxyHost, bound.port);
        listener.close();
        return addr;
    }
    catch (Exception e)
    {
        if (listener !is null)
            collectException(listener.close());
        throw new Exception("reserve php upstream address: " ~ e.msg);
    }
}

/// Returns the per-environment serve runtime directory.
string serveRuntimeDir(string rootDir, string environmentName) @safe pure
{
    auto name = environmentName.strip();
    if (name.length == 0)
        name = "current";
    return buildPath(rootDir, serveRuntimeRoot, serveRuntimeSubdir, name);
}

/// Returns the serve state file path for an environment.
string serveStatePath(string rootDir, string environmentName) @safe pure
{
    return buildPath(serveRuntimeDir(rootDir, environmentName), serveStateFileName);
}

/// Inspects a directory docroot for `index.php` or treats a regular PHP file
/// as an explicit front controller rooted in its parent directory.
AppLayout resolveServeAppLayout(string docroot) @trusted
{
    if (!exists(docroot))
        throw new Exception(format("stat docroot: %s: no such file or directory", docroot));

    if (!isDir(docroot))
    {
        if (!isFile(docroot))
            throw new Exception(format(`docroot "%s" is neither a directory nor a regular file`, docroot));
        if (icmp(extension(docroot), ".php") != 0)
            throw new Exception(format(`docroot file "%s" is not a PHP front controller`, docroot));

        auto frontController = baseName(docroot);
        return AppLayout(
            dirName(docroot),
            frontController,
            "/" ~ toSlashPath(frontController),
            frontController,
        );
    }

    auto layout = AppLayout(docroot, "", "", "");
    auto frontControllerPath = buildPath(docroot, "index.php");
    if (!exists(frontControllerPath))
        return layout;
    if (isDir(frontControllerPath))
        throw new Exception(format(`front controller "%s" is a directory`, frontControllerPath));

    layout.frontControllerRelative = "index.php";
    layout.frontControllerWebPath = "/index.php";
    layout.frontControllerIndex = "index.php";
    return layout;
}

/// Polls until `address` accepts TCP connections or `timeout` elapses.
void waitForServeAddress(string address, Duration timeout) @trusted
{
    auto deadline = Clock.currTime() + timeout;
    while (Clock.currTime() < deadline)
    {
        if (callPingServeAddress(address))
            return;
        Thread.sleep(servePollInterval);
    }

    throw new Exception(format("server did not start listening within %s", timeout));
}

/// Reports whether `address` currently accepts TCP connections.
bool pingServeAddress(string address) @trusted
{
    return pingTCPAddress(address, servePollInterval);
}

/// Loads the recorded serve state for an environment and clears it when the
/// recorded server is no longer listening.
ServeRuntimeState* loadWebServerState(string rootDir, string environmentName) @trusted
{
    auto path = serveStatePath(rootDir, environmentName);
    if (!exists(path))
        return null;

    auto state = loadServeState(path);
    if (state.serverAddress.strip().length > 0 && callPingServeAddress(serveStateProbeAddress(*state)))
        return state;

    if (exists(path))
    {
        try
        {
            remove(path);
        }
        catch (Exception e)
        {
            if (!isMissingFileError(e))
                throw new Exception("remove stale serve state: " ~ e.msg);
        }
    }
    return null;
}

/// Reads and decodes one serve state JSON file.
ServeRuntimeState* loadServeState(string path) @trusted
{
    string raw = readText(path);
    JSONValue root;
    try
    {
        root = parseJSON(raw);
    }
    catch (Exception e)
    {
        throw new Exception(format("decode serve state %s: %s", path, e.msg));
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

/// Persists the serve state JSON file for a background webserver.
void writeServeState(string path, ServeRuntimeState state) @trusted
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

/// Stops the recorded background webserver for `environmentName`. Sets
/// `alreadyStopped = true` when there was nothing to stop.
ServeRuntimeState stopManagedServe(Store store, string environmentName, out bool alreadyStopped) @trusted
{
    auto state = loadWebServerState(store.rootDir, environmentName);
    if (state is null)
    {
        alreadyStopped = true;
        return ServeRuntimeState.init;
    }

    callStopServeRuntime(*state);
    auto path = serveStatePath(store.rootDir, environmentName);
    if (exists(path))
    {
        try
        {
            remove(path);
        }
        catch (Exception e)
        {
            if (!isMissingFileError(e))
                throw new Exception("remove serve state: " ~ e.msg);
        }
    }

    alreadyStopped = false;
    return *state;
}

/// Terminates recorded serve processes and waits for the server address to
/// stop accepting TCP connections.
void stopServeRuntime(ServeRuntimeState state) @trusted
{
    stopServePID(state.primaryPID);
    if (state.secondaryPID != 0 && state.secondaryPID != state.primaryPID)
        stopServePID(state.secondaryPID);

    if (state.serverAddress.strip().length == 0)
        return;

    auto probeAddr = serveStateProbeAddress(state);
    auto deadline = Clock.currTime() + serveShutdownTimeout;
    while (Clock.currTime() < deadline)
    {
        if (!callPingServeAddress(probeAddr))
            return;
        Thread.sleep(servePollInterval);
    }
    if (!callPingServeAddress(probeAddr))
        return;

    throw new Exception(format("webserver did not stop listening on %s within %s", state.serverAddress, serveShutdownTimeout));
}

/// Force-terminates one process (and its children on Windows), treating
/// already-gone processes as success.
void stopServePID(int pid) @trusted
{
    if (pid <= 0)
        return;

    version (Windows)
    {
        auto res = execute(["taskkill", "/PID", to!string(pid), "/T", "/F"]);
        if (res.status == 0 || isMissingServeProcessOutput(res.output))
            return;
        throw new Exception(format("stop serve process %d: exit status %d (%s)", pid, res.status, res.output.strip()));
    }
    else version (Posix)
    {
        import core.stdc.errno : ENOENT, errno, ESRCH;
        import core.sys.posix.signal : kill, SIGKILL;
        import core.sys.posix.sys.wait : waitpid, WNOHANG;

        if (kill(pid, SIGKILL) != 0)
        {
            auto err = errno;
            if (err == ESRCH || err == ENOENT)
                return;
            throw new Exception(format("stop serve process %d: errno %d", pid, err));
        }
        int status = 0;
        waitpid(pid, &status, WNOHANG);
    }
}

/// Best-effort wrapper around `stopServePID` safe for use inside `scope (exit)`.
void stopServePIDQuietly(int pid) @trusted nothrow
{
    try
    {
        stopServePID(pid);
    }
    catch (Exception)
    {
    }
}

/// Creates (or truncates) a serve log file, creating parent directories.
File openServeLog(string path) @trusted
{
    auto dir = dirName(path);
    try
    {
        mkdirRecurse(dir);
    }
    catch (Exception e)
    {
        throw new Exception("create serve log directory: " ~ e.msg);
    }

    try
    {
        return File(path, "wb");
    }
    catch (Exception e)
    {
        throw new Exception(format("open serve log %s: %s", path, e.msg));
    }
}

/// Renders a human-readable webserver label for messages.
string serveRuntimeLabel(string kind) @safe pure
{
    auto trimmed = kind.strip();
    if (trimmed.length == 0)
        return "webserver";
    return trimmed ~ " webserver";
}

/// Reports whether a recorded serve state matches the requested server kind,
/// endpoint, and docroot.
bool serveStateMatches(ServeRuntimeState state, Endpoint endpoint, string docroot, string serverType) @safe
{
    auto stateScheme = state.serverScheme.strip();
    if (stateScheme.length == 0)
        stateScheme = "http";
    return icmp(state.serverKind.strip(), serverType.strip()) == 0 &&
        icmp(stateScheme, endpoint.scheme.strip()) == 0 &&
        icmp(state.serverAddress.strip(), endpoint.address.strip()) == 0 &&
        cleanPath(state.docroot) == cleanPath(docroot);
}

/// Renders the URL recorded in a serve state.
string serveStateURL(ServeRuntimeState state) @safe pure
{
    return serveStateURLService(state);
}

/// Returns the probe address for a recorded serve state.
string serveStateProbeAddress(ServeRuntimeState state) @safe
{
    return serveStateProbeAddressService(state);
}

/// Reports whether process termination output indicates the process was already gone.
bool isMissingServeProcessOutput(string output) @safe pure
{
    auto trimmed = output.strip().toLower();
    return trimmed.length == 0 ||
        trimmed.canFind("not found") ||
        trimmed.canFind("no running instance") ||
        trimmed.canFind("process already finished") ||
        trimmed.canFind("no such process");
}

// ============================================================================
// Endpoint & Docroot Resolution (shared between `start`/`serve` & `phpmyadmin`)
// ============================================================================

/// Returns the configured webserver provider; an omitted type preserves the
/// legacy nginx-then-PHP selection behavior.
string resolveEnvironmentServerType(const Environment environment) @safe
{
    string serverType;
    if (environment.server !is null)
        serverType = environment.server.type.strip().toLower();
    if (serverType.length == 0)
    {
        if (environment.toolVersion("nginx").strip().length > 0)
            serverType = ServerTypeNginx;
        else
            serverType = ServerTypePHP;
    }

    switch (serverType)
    {
        case ServerTypePHP:
            if (!hasPHPCLI(environment))
                throw new Exception(format(`environment "%s" selects the PHP webserver but does not define a PHP CLI provider`, environment.name));
            break;
        case ServerTypeNginx:
            if (environment.toolVersion("nginx").strip().length == 0)
                throw new Exception(format(`environment "%s" selects nginx but does not define an nginx version`, environment.name));
            if (primaryPHPVersion(environment).strip().length == 0)
                throw new Exception(format(`environment "%s" selects nginx but does not define a php or php-zts version`, environment.name));
            break;
        case ServerTypeApache:
            if (environment.toolVersion("apache").strip().length == 0)
                throw new Exception(format(`environment "%s" selects apache but does not define an apache version`, environment.name));
            if (primaryPHPVersion(environment).strip().length == 0)
                throw new Exception(format(`environment "%s" selects apache but does not define a php or php-zts version`, environment.name));
            break;
        case ServerTypeFrankenPHP:
            if (environment.toolVersion("frankenphp").strip().length == 0)
                throw new Exception(format(`environment "%s" selects frankenphp but does not define a frankenphp version`, environment.name));
            break;
        default:
            throw new Exception(format(`unsupported server type "%s": use php, nginx, apache, or frankenphp`, serverType));
    }

    return serverType;
}

/// Combines the configured server address (or `--server` override) with HTTPS.
Endpoint resolveServerEndpoint(const(ServerConfig)* cfg, string overrideVal) @safe
{
    auto address = resolveServeAddress(cfg, overrideVal);
    bool https = cfg !is null && cfg.https;
    auto scheme = https ? "https" : "http";
    return Endpoint(scheme, address, https);
}

/// Picks the serve address from the override flag, the environment config, or
/// the defaults, and validates it.
string resolveServeAddress(const(ServerConfig)* cfg, string overrideVal) @safe
{
    if (overrideVal.strip().length > 0)
    {
        string host;
        int port = 0;
        splitServerAddress(overrideVal, host, port);
        return joinHostPort(host, port);
    }

    string host = defaultServeHostname;
    int port = defaultServePort;
    if (cfg !is null)
    {
        if (cfg.hostname.strip().length > 0)
            host = cfg.hostname.strip();
        if (cfg.port != 0)
            port = cfg.port;
    }
    validateServeHostAndPort(host, port);
    return joinHostPort(host, port);
}

/// Renders an endpoint as a URL, defaulting to `http`.
string serverEndpointURL(Endpoint endpoint) @safe pure
{
    return endpointURL(endpoint);
}

/// Parses and validates a `HOST:PORT` value.
void splitServerAddress(string value, out string host, out int port) @safe
{
    string portText;
    if (!splitHostPort(value.strip(), host, portText))
        throw new Exception(format(`invalid --server value "%s": use HOST:PORT`, value));

    try
    {
        port = to!int(portText);
    }
    catch (Exception)
    {
        throw new Exception(format(`invalid --server value "%s": use HOST:PORT`, value));
    }

    validateServeHostAndPort(host, port);
}

/// Rejects empty hostnames and out-of-range ports.
void validateServeHostAndPort(string host, int port) @safe pure
{
    if (host.strip().length == 0)
        throw new Exception("server hostname cannot be empty");
    if (port < 1 || port > 65535)
        throw new Exception("server port must be between 1 and 65535");
}

/// Resolves the docroot from the CLI argument or environment config, making it
/// absolute relative to `projectDir`, and verifies it is a dir or regular file.
string resolveServeDocroot(string projectDir, string configuredDocroot, string overrideDocroot) @trusted
{
    auto trimmed = overrideDocroot.strip();
    if (trimmed.length == 0)
        trimmed = configuredDocroot.strip();
    if (trimmed.length == 0)
        throw new Exception("start requires a docroot argument or docroot in the current environment file");

    auto resolved = cleanPath(trimmed);
    if (!isAbsolute(resolved))
        resolved = cleanPath(buildPath(projectDir, resolved));

    if (!exists(resolved))
        throw new Exception(format("stat docroot: %s: no such file or directory", resolved));
    if (!isDir(resolved) && !isFile(resolved))
        throw new Exception(format(`docroot "%s" is neither a directory nor a regular file`, trimmed));

    return resolved;
}

unittest
{
    import std.algorithm : startsWith;
    import std.file : write;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    // TestReserveServeBackendAddressProducesLoopbackPort
    {
        auto addr = reserveServeBackendAddress();
        assert(addr.startsWith("127.0.0.1:"));
    }

    // TestResolveServeAppLayoutDirectoryAndPHPFile
    {
        auto tmp = createTempDir("polka-serve-layout-");
        scope (exit) rmdirRecurseQuiet(tmp);

        auto pubDir = buildPath(tmp, "public");
        mkdirRecurse(pubDir);
        auto dirOnly = resolveServeAppLayout(pubDir);
        assert(dirOnly.docroot == pubDir && dirOnly.frontControllerRelative == "");

        write(buildPath(pubDir, "index.php"), "<?php\n");
        auto dirWithIndex = resolveServeAppLayout(pubDir);
        assert(dirWithIndex.docroot == pubDir);
        assert(dirWithIndex.frontControllerRelative == "index.php");
        assert(dirWithIndex.frontControllerWebPath == "/index.php");

        auto customFront = buildPath(pubDir, "app.php");
        write(customFront, "<?php\n");
        auto fileLayout = resolveServeAppLayout(customFront);
        assert(fileLayout.docroot == pubDir);
        assert(fileLayout.frontControllerRelative == "app.php");
        assert(fileLayout.frontControllerWebPath == "/app.php");
    }

    // TestServeStateRoundTripAndStaleCleanup
    {
        auto tmp = createTempDir("polka-serve-state-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto statePath = serveStatePath(tmp, "demo");

        ServeRuntimeState st;
        st.environmentName = "demo";
        st.serverKind = ServerTypeNginx;
        st.serverScheme = "https";
        st.serverAddress = "site.localhost:8443";
        st.docroot = buildPath(tmp, "public");
        st.primaryPID = 1234;
        st.secondaryPID = 1235;
        writeServeState(statePath, st);

        auto oldPing = pingServeAddressFunc;
        scope (exit) pingServeAddressFunc = oldPing;

        bool live = true;
        pingServeAddressFunc = (string addr) {
            assert(addr == "127.0.0.1:8443");
            return live;
        };

        auto loaded = loadWebServerState(tmp, "demo");
        assert(loaded !is null && loaded.serverKind == ServerTypeNginx && loaded.secondaryPID == 1235);

        live = false;
        assert(loadWebServerState(tmp, "demo") is null);
        assert(!exists(statePath));
    }

    // TestResolveEnvironmentServerTypeAndEndpoint
    {
        Environment env;
        env.name = "demo";
        env.tools = ["php": "8.4"];
        assert(resolveEnvironmentServerType(env) == ServerTypePHP);

        env.tools["nginx"] = "1.30";
        assert(resolveEnvironmentServerType(env) == ServerTypeNginx);

        env.server = new ServerConfig(ServerTypeFrankenPHP, "app.localhost", 8443, true);
        env.tools["frankenphp"] = "1.12";
        assert(resolveEnvironmentServerType(env) == ServerTypeFrankenPHP);

        auto ep = resolveServerEndpoint(env.server, "");
        assert(ep.scheme == "https" && ep.address == "app.localhost:8443" && ep.https);
        assert(serverEndpointURL(ep) == "https://app.localhost:8443");

        auto overridden = resolveServerEndpoint(env.server, "127.0.0.1:9000");
        assert(overridden.address == "127.0.0.1:9000" && overridden.https);
    }
}
