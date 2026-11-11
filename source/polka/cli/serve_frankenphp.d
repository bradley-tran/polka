module polka.cli.serve_frankenphp;

// FrankenPHP serve lifecycle: foreground and background startup plus Caddyfile generation.

import std.array : appender;
import std.file : mkdirRecurse, write;
import std.format : format;
import std.path : buildPath;

import polka.backend : Environment, Store, TextWriter;
import polka.cli.env_runtime : resolveRuntimeEnvironment;
import polka.cli.serve_runtime :
    openServeLog,
    serveLogFileName,
    serveProbeAddress,
    serveStartupTimeout,
    stopServePID,
    waitForServeAddress;
import polka.cli.serve_tls :
    ensureGlobalTLSCertificateRuntimeKey,
    ServeTLSConfig;
import polka.cli.types :
    applyManagedPHPRuntimeConfig,
    callServeNow,
    executeTargetWithEnv,
    quoteGoString;
import polka.config : ServerTypeFrankenPHP;
import polka.service :
    AppLayout,
    currentEnvList,
    Endpoint,
    ServeRuntimeState,
    spawnDetachedServiceProcess,
    toolLogRoot;
import polka.support.fs : currentOS, toSlashPath;
import polka.support.net : splitHostPort;

enum string frankenPHPCaddyfileName = "Caddyfile";

/// Test seams for the FrankenPHP webserver lifecycle.
int delegate(TextWriter stdout, TextWriter stderr, Store store, Environment environment, Endpoint endpoint, AppLayout layout) runFrankenPHPServeFunc;
ServeRuntimeState delegate(Store store, Environment environment, Endpoint endpoint, AppLayout layout) startBackgroundFrankenPHPServe;

/// Calls `runFrankenPHPServeFunc` or `runFrankenPHPServe`.
int callRunFrankenPHPServe(TextWriter stdout, TextWriter stderr, Store store, Environment envConfig, Endpoint endpoint, AppLayout layout) @trusted
{
    if (runFrankenPHPServeFunc !is null)
        return runFrankenPHPServeFunc(stdout, stderr, store, envConfig, endpoint, layout);
    return runFrankenPHPServe(stdout, stderr, store, envConfig, endpoint, layout);
}

/// Calls `startBackgroundFrankenPHPServe` or `startFrankenPHPServeInBackground`.
ServeRuntimeState callStartBackgroundFrankenPHPServe(Store store, Environment envConfig, Endpoint endpoint, AppLayout layout) @trusted
{
    if (startBackgroundFrankenPHPServe !is null)
        return startBackgroundFrankenPHPServe(store, envConfig, endpoint, layout);
    return startFrankenPHPServeInBackground(store, envConfig, endpoint, layout);
}

/// Runs FrankenPHP attached to the current terminal.
int runFrankenPHPServe(
    TextWriter stdout,
    TextWriter stderr,
    Store store,
    Environment envConfig,
    Endpoint endpoint,
    AppLayout layout
) @trusted
{
    auto frankenPHPTarget = store.resolveTool(ServerTypeFrankenPHP);
    auto env = resolveFrankenPHPRuntimeEnvironment(store, frankenPHPTarget);
    auto runtimeDir = toolLogRoot(store.rootDir, ServerTypeFrankenPHP, envConfig.name);
    auto configPath = prepareFrankenPHPServeRuntime(store.cacheDir, runtimeDir, endpoint, layout);

    return executeTargetWithEnv(stdout, stderr, env, frankenPHPTarget, ["run", "--config", configPath, "--adapter", "caddyfile"]);
}

/// Starts and records the managed FrankenPHP process.
ServeRuntimeState startFrankenPHPServeInBackground(
    Store store,
    Environment envConfig,
    Endpoint endpoint,
    AppLayout layout
) @trusted
{
    return startFrankenPHPServeInBackgroundAt(
        store,
        envConfig,
        endpoint,
        layout,
        toolLogRoot(store.rootDir, ServerTypeFrankenPHP, envConfig.name)
    );
}

/// Starts the managed FrankenPHP process detached inside `runtimeDir`.
ServeRuntimeState startFrankenPHPServeInBackgroundAt(
    Store store,
    Environment envConfig,
    Endpoint endpoint,
    AppLayout layout,
    string runtimeDir
) @trusted
{
    auto frankenPHPTarget = store.resolveTool(ServerTypeFrankenPHP);
    auto env = resolveFrankenPHPRuntimeEnvironment(store, frankenPHPTarget);
    auto configPath = prepareFrankenPHPServeRuntime(store.cacheDir, runtimeDir, endpoint, layout);

    auto logPath = buildPath(runtimeDir, serveLogFileName);
    auto logFile = openServeLog(logPath);
    scope (exit)
    {
        if (logFile.isOpen)
            logFile.close();
    }

    int pid = 0;
    try
    {
        pid = spawnDetachedServiceProcess(
            frankenPHPTarget,
            ["run", "--config", configPath, "--adapter", "caddyfile"],
            logFile,
            env
        );
    }
    catch (Exception e)
    {
        throw new Exception("start FrankenPHP webserver: " ~ e.msg);
    }

    try
    {
        waitForServeAddress(serveProbeAddress(endpoint.address), serveStartupTimeout);
    }
    catch (Exception e)
    {
        try { stopServePID(pid); } catch (Exception) {}
        throw new Exception(format("start FrankenPHP webserver on %s: %s (see %s)", endpoint.address, e.msg, logPath));
    }

    ServeRuntimeState state;
    state.environmentName = envConfig.name;
    state.serverKind = ServerTypeFrankenPHP;
    state.serverScheme = endpoint.scheme;
    state.serverAddress = endpoint.address;
    state.docroot = layout.docroot;
    state.runtimeDir = runtimeDir;
    state.logPath = logPath;
    state.configPath = configPath;
    state.primaryPID = pid;
    state.startedAt = callServeNow().toUTC();
    return state;
}

/// Resolves the runtime environment pointing FrankenPHP at the managed `php.ini`.
string[] resolveFrankenPHPRuntimeEnvironment(Store store, string frankenPHPTarget) @trusted
{
    auto env = resolveRuntimeEnvironment(currentOS(), currentEnvList(), store);
    return applyManagedPHPRuntimeConfig(currentOS(), env, frankenPHPTarget);
}

/// Writes the generated `Caddyfile` and TLS runtime assets into `runtimeDir`.
string prepareFrankenPHPServeRuntime(
    string cacheDir,
    string runtimeDir,
    Endpoint endpoint,
    AppLayout layout
) @trusted
{
    try
    {
        mkdirRecurse(runtimeDir);
    }
    catch (Exception e)
    {
        throw new Exception("create FrankenPHP runtime directory: " ~ e.msg);
    }

    string host, portText;
    if (!splitHostPort(endpoint.address, host, portText))
        throw new Exception(format(`parse serve address "%s": invalid host:port`, endpoint.address));

    ServeTLSConfig tlsConfig;
    if (endpoint.https)
    {
        auto kp = ensureGlobalTLSCertificateRuntimeKey(cacheDir, runtimeDir, host);
        tlsConfig = ServeTLSConfig(true, kp.certPath, kp.keyPath);
    }

    auto configPath = buildPath(runtimeDir, frankenPHPCaddyfileName);
    auto configData = renderFrankenPHPCaddyfile(endpoint, layout, tlsConfig);
    try
    {
        write(configPath, configData);
    }
    catch (Exception e)
    {
        throw new Exception("write FrankenPHP Caddyfile: " ~ e.msg);
    }

    return configPath;
}

/// Renders a self-contained Caddyfile development server configuration.
string renderFrankenPHPCaddyfile(Endpoint endpoint, AppLayout layout, ServeTLSConfig tlsConfig) @safe pure
{
    auto b = appender!string();
    b.put("{\n");
    b.put("\tadmin off\n");
    b.put("\tauto_https disable_redirects\n");
    b.put("}\n\n");
    b.put(endpoint.scheme);
    b.put("://");
    b.put(endpoint.address);
    b.put(" {\n");
    b.put("\troot * ");
    b.put(quoteGoString(toSlashPath(layout.docroot)));
    b.put("\n");
    b.put("\tencode zstd br gzip\n");
    if (tlsConfig.enabled)
    {
        b.put("\ttls ");
        b.put(quoteGoString(toSlashPath(tlsConfig.certificatePath)));
        b.put(" ");
        b.put(quoteGoString(toSlashPath(tlsConfig.certificateKeyPath)));
        b.put("\n");
    }
    if (layout.frontControllerRelative.length == 0 || layout.frontControllerRelative == "index.php")
    {
        b.put("\tphp_server\n");
    }
    else
    {
        auto frontController = toSlashPath(layout.frontControllerRelative);
        b.put("\tphp_server {\n");
        b.put("\t\ttry_files {path} {path}/");
        b.put(frontController);
        b.put(" ");
        b.put(frontController);
        b.put("\n");
        b.put("\t}\n");
    }
    b.put("}\n");
    return b.data;
}

unittest
{
    import std.algorithm : canFind;

    auto caddy = renderFrankenPHPCaddyfile(
        Endpoint("https", "app.localhost:8443", true),
        AppLayout("/srv/www", "app.php", "/app.php", "app.php"),
        ServeTLSConfig(true, "/tmp/cert.crt", "/tmp/cert.key")
    );
    assert(caddy.canFind("https://app.localhost:8443 {"));
    assert(caddy.canFind(`tls "/tmp/cert.crt" "/tmp/cert.key"`));
    assert(caddy.canFind(`try_files {path} {path}/app.php app.php`));
}
