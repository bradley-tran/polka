module polka.service.traefik;

import core.thread : Thread;
import core.time : Duration, dur;
import std.array : appender;
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
import polka.support.fs : currentOS, isSymlinkPath, octal, toSlashPath, writeTextAtomic;
import polka.support.net : joinHostPort, pingTCPAddress;
import polka.tools.cache : jsonLongField, jsonStringField;

enum string TraefikListenHost = "127.0.0.1";
enum int DefaultTraefikPort = 8080;
enum string managedTraefikStateDirectory = "run";
enum string managedTraefikStateSubdirectory = "traefik";
enum string managedTraefikConfigSubdirName = "dynamic";
enum string managedTraefikDynamicFileName = "polka.yml";
enum string traefikWebEntrypoint = "web";
enum string managedTraefikLogFileName = "traefik.log";
enum Duration managedTraefikPollInterval = dur!"msecs"(100);
enum Duration managedTraefikStartupTimeout = dur!"seconds"(10);
enum Duration managedTraefikShutdownTimeout = dur!"seconds"(5);

/// Persisted runtime state for one managed Traefik reverse-proxy instance.
struct TraefikRuntimeState
{
    string environmentName;
    string version_;
    int port = 0;
    int pid = 0;
    string configDir;
    string logPath;
    SysTime startedAt;
}

/// Describes a project-local Traefik process launch.
struct TraefikServerSpec
{
    string environmentName;
    string version_;
    string target;
    string configDir;
    string logPath;
    int port = 0;
    string[] env;
}

/// Result returned when starting a Traefik server process.
struct TraefikStartResult
{
    int pid = 0;
}

/// Certificate/key PEM paths used when Traefik terminates TLS on its web entrypoint.
struct TraefikTLSConfig
{
    string certificatePath;
    string keyPath;
}

/// Hooks allowing tests to stub Traefik process lifecycle calls.
struct TraefikRuntimeHooks
{
    TraefikStartResult delegate(TraefikServerSpec) startServer;
    void delegate(TraefikRuntimeState) stopServer;
    bool delegate(string) pingAddress;
    SysTime delegate() now;

    /// Returns a copy with unset delegates filled by production implementations.
    TraefikRuntimeHooks withDefaults() const @trusted
    {
        TraefikRuntimeHooks copy = this;
        if (copy.startServer is null)
            copy.startServer = (TraefikServerSpec spec) => startTraefikServer(spec);
        if (copy.stopServer is null)
            copy.stopServer = (TraefikRuntimeState st) { stopTraefikRuntime(st); };
        if (copy.pingAddress is null)
            copy.pingAddress = (string addr) => pingTraefikAddress(addr);
        if (copy.now is null)
            copy.now = () => Clock.currTime(UTC());
        return copy;
    }
}

/// Starts or reuses the configured Traefik reverse proxy for `ctx.environment`.
TraefikRuntimeState ensureManagedTraefikStarted(
    ServiceContext ctx,
    TraefikRuntimeHooks hooks,
    out bool alreadyStarted
) @trusted
{
    hooks = hooks.withDefaults();
    auto cfg = ctx.environment.traefik();
    if (cfg is null || cfg.version_.strip().length == 0)
    {
        alreadyStarted = true;
        return TraefikRuntimeState.init;
    }

    auto state = loadLiveTraefikStateForEnvironment(ctx.rootDir, ctx.environment, hooks.pingAddress);
    if (state !is null)
    {
        alreadyStarted = true;
        return *state;
    }

    auto spec = buildTraefikServerSpec(ctx);
    auto result = hooks.startServer(spec);

    TraefikRuntimeState startedState;
    startedState.environmentName = ctx.environment.name;
    startedState.version_ = spec.version_;
    startedState.port = spec.port;
    startedState.pid = result.pid;
    startedState.configDir = spec.configDir;
    startedState.logPath = spec.logPath;
    startedState.startedAt = hooks.now().toUTC();

    try
    {
        writeTraefikState(traefikStatePath(ctx.rootDir, ctx.environment.name), startedState);
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

/// Stops the active environment's Traefik proxy and deletes its state file.
TraefikRuntimeState stopManagedTraefik(
    ServiceContext ctx,
    TraefikRuntimeHooks hooks,
    out bool alreadyStopped
) @trusted
{
    hooks = hooks.withDefaults();

    auto state = loadLiveTraefikState(ctx.rootDir, ctx.environment.name, hooks.pingAddress);
    if (state is null)
    {
        alreadyStopped = true;
        return TraefikRuntimeState.init;
    }

    hooks.stopServer(*state);
    removeTraefikStateFile(traefikStatePath(ctx.rootDir, ctx.environment.name));

    alreadyStopped = false;
    return *state;
}

private void removeTraefikStateFile(string path) @trusted
{
    if (!exists(path) && !isSymlinkPath(path))
        return;
    try
    {
        remove(path);
    }
    catch (Exception e)
    {
        throw new Exception("remove traefik state: " ~ e.msg);
    }
}

/// Resolves the Traefik executable, dynamic config directory, and port into a launch spec.
TraefikServerSpec buildTraefikServerSpec(ServiceContext ctx) @trusted
{
    auto environment = ctx.environment;
    auto cfg = environment.traefik();
    if (cfg is null)
        throw new Exception(format(`environment "%s" does not define traefik`, environment.name));

    auto ver = cfg.version_.strip();
    if (ver.length == 0)
        throw new Exception(format(`environment "%s" defines traefik but does not define a version`, environment.name));

    auto target = ctx.resolveInstalledTool(toolTraefik, ver);
    auto env = ctx.runtimeEnv();

    TraefikServerSpec spec;
    spec.environmentName = environment.name;
    spec.version_ = ver;
    spec.target = target;
    spec.configDir = traefikConfigDir(ctx.rootDir, environment.name);
    spec.logPath = traefikLogPath(ctx.rootDir, environment.name);
    spec.port = effectiveTraefikPort(cfg);
    spec.env = env;
    return spec;
}

/// Launches `traefik` detached and waits for it to begin listening on its configured entrypoint.
TraefikStartResult startTraefikServer(TraefikServerSpec spec) @trusted
{
    if (!exists(spec.configDir))
    {
        try
        {
            mkdirRecurse(spec.configDir);
        }
        catch (Exception e)
        {
            throw new Exception("create traefik config directory: " ~ e.msg);
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
        pid = spawnDetachedServiceProcess(spec.target, traefikServerArgs(spec), logFile, spec.env);
    }
    catch (Exception e)
    {
        throw new Exception("start traefik: " ~ e.msg);
    }

    auto address = traefikAddress(spec.port);
    try
    {
        waitForTraefikAddress(spec.port, managedTraefikStartupTimeout, null);
    }
    catch (Exception e)
    {
        try { stopServicePID(pid); } catch (Exception) {} // Best-effort cleanup on startup timeout.
        throw new Exception(format("start traefik on %s: %s (see %s)", address, e.msg, spec.logPath));
    }

    return TraefikStartResult(pid);
}

/// Builds the CLI flags configuring Traefik's `web` entrypoint and dynamic file provider directory.
string[] traefikServerArgs(TraefikServerSpec spec) @safe
{
    auto args = [
        "--entrypoints." ~ traefikWebEntrypoint ~ ".address=" ~ traefikAddress(spec.port),
    ];
    if (spec.configDir.strip().length > 0)
    {
        args ~= [
            "--providers.file.directory=" ~ spec.configDir,
            "--providers.file.watch=true",
        ];
    }
    return args;
}

/// Writes the managed dynamic config file routing Traefik's `web` entrypoint to `upstreamURL`.
void writeTraefikDynamicConfig(
    string rootDir,
    string environmentName,
    string upstreamURL,
    bool insecureBackend,
    const(TraefikTLSConfig)* tls = null
) @trusted
{
    auto dir = traefikConfigDir(rootDir, environmentName);
    if (!exists(dir))
    {
        try
        {
            mkdirRecurse(dir);
        }
        catch (Exception e)
        {
            throw new Exception("create traefik config directory: " ~ e.msg);
        }
    }

    auto path = buildPath(dir, managedTraefikDynamicFileName);
    auto content = renderTraefikDynamicConfig(traefikRouterName(environmentName), upstreamURL, insecureBackend, tls);
    try
    {
        writeTextAtomic(path, content, octal!644);
    }
    catch (Exception e)
    {
        throw new Exception(format("write traefik dynamic config %s: %s", path, e.msg));
    }
}

/// Renders a Traefik file-provider YAML document routing `/` on the `web` entrypoint to `upstreamURL`.
string renderTraefikDynamicConfig(
    string routerName,
    string upstreamURL,
    bool insecureBackend,
    const(TraefikTLSConfig)* tls = null
) @safe pure
{
    auto b = appender!string();
    b.put("# Managed by Polka. Routes the Traefik web entrypoint to the environment webserver.\n");
    b.put("http:\n");
    b.put("  routers:\n");
    b.put("    " ~ routerName ~ ":\n");
    b.put("      rule: \"PathPrefix(`/`)\"\n");
    b.put("      entryPoints:\n");
    b.put("        - " ~ traefikWebEntrypoint ~ "\n");
    b.put("      service: " ~ routerName ~ "\n");
    if (tls !is null)
        b.put("      tls: {}\n");
    b.put("  services:\n");
    b.put("    " ~ routerName ~ ":\n");
    b.put("      loadBalancer:\n");
    b.put("        servers:\n");
    b.put("          - url: \"" ~ upstreamURL ~ "\"\n");
    if (insecureBackend)
    {
        b.put("        serversTransport: " ~ routerName ~ "\n");
        b.put("  serversTransports:\n");
        b.put("    " ~ routerName ~ ":\n");
        b.put("      insecureSkipVerify: true\n");
    }
    if (tls !is null)
    {
        b.put("tls:\n");
        b.put("  certificates:\n");
        b.put("    - certFile: \"" ~ toSlashPath(tls.certificatePath) ~ "\"\n");
        b.put("      keyFile: \"" ~ toSlashPath(tls.keyPath) ~ "\"\n");
    }
    return b.data;
}

/// Returns the managed Traefik dynamic configuration file path `<rootDir>/run/traefik/<env>/dynamic/polka.yml`.
string traefikDynamicConfigPath(string rootDir, string environmentName) @safe pure
{
    return buildPath(traefikConfigDir(rootDir, environmentName), managedTraefikDynamicFileName);
}

/// Derives a Traefik-safe router/service identifier (`polka-<sanitized>`) from `environmentName`.
string traefikRouterName(string environmentName) @safe pure
{
    auto name = environmentName.strip();
    if (name.length == 0)
        name = "default";

    auto b = appender!string();
    b.put("polka-");
    foreach (char ch; name)
    {
        if ((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-')
            b.put(ch);
        else
            b.put('-');
    }
    return b.data;
}

/// Terminates the running Traefik process and waits for its TCP socket to close.
void stopTraefikRuntime(TraefikRuntimeState state) @trusted
{
    stopServicePID(state.pid);

    auto deadline = Clock.currTime() + managedTraefikShutdownTimeout;
    while (Clock.currTime() < deadline)
    {
        if (!traefikStateIsLive(state, null))
            return;
        Thread.sleep(managedTraefikPollInterval);
    }
    if (!traefikStateIsLive(state, null))
        return;

    throw new Exception(format("traefik did not stop listening on %s within %s", traefikAddress(state.port), managedTraefikShutdownTimeout));
}

/// Loads live Traefik state and fails if the running version or port drifted from `environment`.
TraefikRuntimeState* loadLiveTraefikStateForEnvironment(
    string rootDir,
    Environment environment,
    bool delegate(string) ping = null
) @trusted
{
    auto state = loadLiveTraefikState(rootDir, environment.name, ping);
    if (state is null)
        return null;
    if (traefikStateMatchesEnvironment(*state, environment))
        return state;

    auto cfg = environment.traefik();
    throw new Exception(format(
        `environment "%s" still has a running traefik %s on %s, but the current traefik is %s on %s; stop the running traefik first`,
        environment.name,
        state.version_,
        traefikAddress(state.port),
        cfg is null ? "" : cfg.version_.strip(),
        traefikAddress(effectiveTraefikPort(cfg))
    ));
}

/// Loads persisted Traefik state if still live, removing stale state files otherwise.
TraefikRuntimeState* loadLiveTraefikState(
    string rootDir,
    string environmentName,
    bool delegate(string) ping = null
) @trusted
{
    auto path = traefikStatePath(rootDir, environmentName);
    if (!exists(path) && !isSymlinkPath(path))
        return null;

    auto state = loadTraefikState(path);
    if (state !is null && traefikStateIsLive(*state, ping))
        return state;

    removeTraefikStateFile(path);
    return null;
}

/// Reads and decodes a persisted Traefik runtime state JSON file.
TraefikRuntimeState* loadTraefikState(string path) @trusted
{
    auto raw = readText(path);
    JSONValue root;
    try
    {
        root = parseJSON(raw);
    }
    catch (Exception e)
    {
        throw new Exception(format("decode traefik state %s: %s", path, e.msg));
    }

    auto st = new TraefikRuntimeState();
    st.environmentName = jsonStringField(root, "environment");
    st.version_ = jsonStringField(root, "version");
    st.port = cast(int) jsonLongField(root, "port", 0);
    st.pid = cast(int) jsonLongField(root, "pid", 0);
    st.configDir = jsonStringField(root, "config_dir");
    st.logPath = jsonStringField(root, "log_path");
    st.startedAt = parseServiceTimestamp(jsonStringField(root, "started_at"));
    return st;
}

/// Writes a Traefik runtime state JSON file with 0644 permissions.
void writeTraefikState(string path, TraefikRuntimeState state) @trusted
{
    JSONValue root = JSONValue(cast(JSONValue[string]) null);
    root["environment"] = state.environmentName;
    root["version"] = state.version_;
    root["port"] = state.port;
    root["pid"] = state.pid;
    root["config_dir"] = state.configDir;
    if (state.logPath.length > 0)
        root["log_path"] = state.logPath;
    root["started_at"] = formatServiceTimestamp(state.startedAt);

    writeServiceJSONFile(path, root, octal!644);
}

int effectiveTraefikPort(const(TraefikConfig)* traefik) @safe pure nothrow @nogc
{
    if (traefik is null || traefik.port == 0)
        return DefaultTraefikPort;
    return traefik.port;
}

string traefikAddress(int port) @safe
{
    return joinHostPort(TraefikListenHost, port);
}

string traefikURL(TraefikRuntimeState state) @trusted
{
    TraefikConfig cfg;
    cfg.port = state.port;
    return traefikURLForConfig(&cfg);
}

string traefikURLForConfig(const(TraefikConfig)* traefik) @safe
{
    if (traefik is null)
        return "";
    return "http://" ~ traefikAddress(effectiveTraefikPort(traefik));
}

string traefikStatePath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedTraefikStateDirectory, managedTraefikStateSubdirectory, environmentName ~ ".json");
}

/// Returns the per-environment Traefik runtime directory storing dynamic config and TLS keys.
string traefikRuntimeDir(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedTraefikStateDirectory, managedTraefikStateSubdirectory, environmentName);
}

string traefikConfigDir(string rootDir, string environmentName) @safe pure
{
    return buildPath(traefikRuntimeDir(rootDir, environmentName), managedTraefikConfigSubdirName);
}

string traefikLogPath(string rootDir, string environmentName) @safe pure
{
    return buildPath(toolLogRoot(rootDir, toolTraefik, environmentName), managedTraefikLogFileName);
}

void waitForTraefikAddress(int port, Duration timeout, bool delegate(string) ping) @trusted
{
    auto p = traefikPingFunc(ping);
    auto deadline = Clock.currTime() + timeout;
    while (Clock.currTime() < deadline)
    {
        if (p(traefikAddress(port)))
            return;
        Thread.sleep(managedTraefikPollInterval);
    }

    throw new Exception(format("server did not start listening within %s", timeout));
}

/// Reports whether persisted `state` still matches Traefik's configured version and port.
bool traefikStateMatchesEnvironment(TraefikRuntimeState state, Environment environment) @safe pure
{
    auto cfg = environment.traefik();
    if (cfg is null)
        return false;

    return state.version_.strip() == cfg.version_.strip() &&
        state.port == effectiveTraefikPort(cfg);
}

bool traefikStateIsLive(TraefikRuntimeState state, bool delegate(string) ping = null) @trusted
{
    return traefikPingFunc(ping)(traefikAddress(state.port));
}

bool pingTraefikAddress(string address) @trusted
{
    return pingTCPAddress(address, managedTraefikPollInterval);
}

private bool delegate(string) traefikPingFunc(bool delegate(string) ping) @trusted
{
    if (ping is null)
        return (string addr) => pingTraefikAddress(addr);
    return ping;
}

unittest
{
    import std.algorithm : canFind;
    import std.file : write;
    import std.path : dirName;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet, setFileMode;
    import polka.support.yaml : YamlValue;

    static string traefikExecutableName()
    {
        return currentOS() == "windows" ? "traefik.exe" : "traefik";
    }

    // TestTraefikServerArgsIncludeWebEntrypointAndConfigDir
    {
        TraefikServerSpec spec;
        spec.configDir = buildPath("root", "run", "traefik", "demo", "dynamic");
        spec.port = 8090;

        assert(traefikServerArgs(spec) == [
            "--entrypoints.web.address=127.0.0.1:8090",
            "--providers.file.directory=" ~ spec.configDir,
            "--providers.file.watch=true",
        ]);

        spec.configDir = "";
        assert(traefikServerArgs(spec) == ["--entrypoints.web.address=127.0.0.1:8090"]);
    }

    // TestRenderTraefikDynamicConfigRoutesToUpstream
    {
        auto got = renderTraefikDynamicConfig("polka-demo", "http://localhost:8000", false, null);
        foreach (want; [
            "http://localhost:8000",
            "entryPoints:\n        - web",
            "service: polka-demo",
            "PathPrefix(`/`)",
        ])
        {
            assert(got.canFind(want));
        }
        assert(!got.canFind("insecureSkipVerify") && !got.canFind("tls:"));

        auto backendTLS = renderTraefikDynamicConfig("polka-demo", "https://localhost:8000", true, null);
        assert(backendTLS.canFind("insecureSkipVerify: true") && backendTLS.canFind("serversTransport: polka-demo"));
    }

    // TestRenderTraefikDynamicConfigTerminatesTLS
    {
        TraefikTLSConfig tls = TraefikTLSConfig(buildPath("root", "cert.crt"), buildPath("root", "cert.key"));
        auto got = renderTraefikDynamicConfig("polka-demo", "https://localhost:8000", true, &tls);
        foreach (want; [
            "      tls: {}",
            "tls:\n  certificates:",
            `certFile: "root/cert.crt"`,
            `keyFile: "root/cert.key"`,
        ])
        {
            assert(got.canFind(want));
        }
    }

    // TestWriteTraefikDynamicConfigWritesManagedFile
    {
        auto rootDir = createTempDir("polka-traefik-dyn-");
        scope (exit) rmdirRecurseQuiet(rootDir);
        writeTraefikDynamicConfig(rootDir, "demo", "http://localhost:8000", false, null);
        auto data = readText(traefikDynamicConfigPath(rootDir, "demo"));
        assert(data.canFind("http://localhost:8000"));
    }

    // TestTraefikRouterNameSanitizesEnvironment
    assert(traefikRouterName("blog/staging") == "polka-blog-staging");
    assert(traefikRouterName("") == "polka-default");

    // TestTraefikStateMatchesEnvironmentIncludesPort
    {
        TraefikRuntimeState st;
        st.version_ = "3.3";
        st.port = 8090;

        Environment env;
        env.tools = ["traefik": "3.3"];
        env.settings = ["traefik": ["port": YamlValue.fromInt(8090)]];
        assert(traefikStateMatchesEnvironment(st, env));

        env.setSettingInt("traefik", "port", 8091);
        assert(!traefikStateMatchesEnvironment(st, env));

        env.setSettingInt("traefik", "port", 8090);
        env.setToolVersion("traefik", "3.4");
        assert(!traefikStateMatchesEnvironment(st, env));
    }

    // TestEffectiveTraefikPortFallsBackToDefault
    {
        assert(effectiveTraefikPort(null) == DefaultTraefikPort);
        TraefikConfig custom;
        custom.port = 9000;
        assert(effectiveTraefikPort(&custom) == 9000);
    }

    // TestEnsureManagedTraefikStartedAndStop
    {
        auto rootDir = createTempDir("polka-traefik-run-");
        scope (exit) rmdirRecurseQuiet(rootDir);
        auto envsDir = buildPath(rootDir, "envs");
        auto ver = "3.3";
        auto target = buildPath(envsDir, toolTraefik, ver, traefikExecutableName());
        mkdirRecurse(dirName(target));
        write(target, "traefik");
        version (Posix)
            setFileMode(target, octal!755);

        bool[string] running;
        ServiceContext ctx;
        ctx.rootDir = rootDir;
        ctx.envsDir = envsDir;
        ctx.environment.name = "demo";
        ctx.environment.tools = ["traefik": ver];
        ctx.environment.settings = ["traefik": ["port": YamlValue.fromInt(8090)]];

        TraefikRuntimeHooks hooks;
        hooks.startServer = (TraefikServerSpec spec) {
            assert(spec.target == target && spec.configDir == traefikConfigDir(rootDir, "demo"));
            running[traefikAddress(spec.port)] = true;
            return TraefikStartResult(4321);
        };
        hooks.stopServer = (TraefikRuntimeState st) {
            running[traefikAddress(st.port)] = false;
        };
        hooks.pingAddress = (string addr) => addr in running ? running[addr] : false;
        hooks.now = () => SysTime(DateTime(2026, 6, 30, 12, 0, 0), UTC());

        bool alreadyStarted = false;
        auto started = ensureManagedTraefikStarted(ctx, hooks, alreadyStarted);
        assert(!alreadyStarted && started.pid == 4321 && started.port == 8090);
        assert(exists(traefikStatePath(rootDir, "demo")));

        ensureManagedTraefikStarted(ctx, hooks, alreadyStarted);
        assert(alreadyStarted);

        bool alreadyStopped = false;
        stopManagedTraefik(ctx, hooks, alreadyStopped);
        assert(!alreadyStopped);
        assert(!exists(traefikStatePath(rootDir, "demo")));
    }
}
