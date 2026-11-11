module polka.cli.command_serve;

// Implementation of `polka serve` / `polka start`: resolves serve endpoint
// and document root, starts environment services, configures the optional
// Traefik front proxy, and launches the configured webserver (foreground or detached).

import std.string : strip;

import polka.backend : Environment, Store, TextWriter, writeOutf, writeOutln;
import polka.cli.command_traefik : loadLiveTraefikState;
import polka.cli.hooks :
    defaultCLIHookRegistry,
    ServeCommandInput,
    StartHookContext,
    WebserverStartHookContext;
import polka.cli.serve_runtime :
    defaultServeHostname,
    loadWebServerState,
    resolveEnvironmentServerType,
    resolveServeAppLayout,
    resolveServeDocroot,
    resolveServerEndpoint,
    serveAppLayout,
    serverEndpoint,
    serverEndpointURL,
    serveRuntimeLabel,
    serveStateMatches,
    serveStateURL;
import polka.cli.serve_tls : ensureGlobalTLSCertificateRuntimeKey;
import polka.cli.tls_private_key : setTLSCAKeyFallbackWarningWriter;
import polka.cli.types : quoteGoString, writeError;
import polka.config : ServerTypePHP;
import polka.service :
    AppLayout,
    Endpoint,
    ServeRuntimeState,
    traefikRuntimeDir,
    TraefikTLSConfig,
    writeTraefikDynamicConfig;
import polka.support.net : joinHostPort, splitHostPort;

/// Reports whether a managed Traefik front proxy is active for the current
/// serve session and the public proxy URL it exposes.
struct TraefikServeProxy
{
    bool active = false;
    string url;
}

/// Resolves the environment webserver configuration, starts configured
/// backing services, configures Traefik proxying when active, and delegates
/// webserver startup to `defaultCLIHookRegistry`.
int runStart(TextWriter stdout, TextWriter stderr, ref Store store, ServeCommandInput input) @trusted
{
    auto restoreTLSWarning = setTLSCAKeyFallbackWarningWriter(stderr);
    scope (exit) restoreTLSWarning();

    Environment* current;
    try
    {
        current = store.current();
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }
    if (current is null)
    {
        writeOutln(stderr, "error: no active environment selected");
        return 1;
    }

    Endpoint endpoint;
    string serverType;
    AppLayout layout;
    try
    {
        endpoint = resolveServerEndpoint(current.server, input.server);
        serverType = resolveEnvironmentServerType(*current);
        if (endpoint.https && serverType == ServerTypePHP)
            throw new Exception("https is not supported by the PHP webserver; use nginx, apache, or frankenphp");

        auto docroot = resolveServeDocroot(store.projectDir, current.docroot, input.docroot);
        layout = resolveServeAppLayout(docroot);
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }

    auto hooks = defaultCLIHookRegistry();
    try
    {
        hooks.startServices(StartHookContext(stdout, stderr, store, *current));
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }

    try
    {
        auto liveState = loadWebServerState(store.rootDir, current.name);
        if (liveState !is null)
        {
            if (serveStateMatches(*liveState, endpoint, layout.docroot, serverType))
            {
                writeOutf(
                    stdout,
                    "Webserver for environment %s is already running at %s.\n",
                    quoteGoString(current.name),
                    serveStateURL(*liveState)
                );
                return 0;
            }

            writeOutf(
                stderr,
                "error: environment %s already has a running %s at %s; run `polka stop` before starting a different webserver\n",
                quoteGoString(current.name),
                serveRuntimeLabel(liveState.serverKind),
                serveStateURL(*liveState)
            );
            return 1;
        }
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }

    TraefikServeProxy traefikProxy;
    try
    {
        traefikProxy = prepareTraefikServeProxy(store, *current, endpoint);
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }

    if (traefikProxy.active && input.watch)
        writeOutf(stdout, "Reverse proxying through Traefik at %s.\n", traefikProxy.url);

    int exitCode = 0;
    try
    {
        exitCode = hooks.startWebserver(WebserverStartHookContext(
            stdout,
            stderr,
            store,
            *current,
            serverType,
            input,
            endpoint,
            layout
        ));
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }

    if (traefikProxy.active && !input.watch)
        writeOutf(stdout, "Reverse proxying through Traefik at %s.\n", traefikProxy.url);

    return exitCode;
}

/// Points a live managed Traefik instance at the active environment webserver.
/// Returns `TraefikServeProxy(false)` when Traefik is unconfigured or not running.
TraefikServeProxy prepareTraefikServeProxy(ref Store store, Environment environment, Endpoint endpoint) @trusted
{
    if (environment.toolVersion("traefik").strip().length == 0)
        return TraefikServeProxy.init;

    auto liveState = loadLiveTraefikState(store.rootDir, environment.name);
    if (liveState is null)
        return TraefikServeProxy.init;

    string host, portPart;
    if (!splitHostPort(endpoint.address, host, portPart) || host.strip().length == 0)
        host = defaultServeHostname;

    // Terminate TLS at Traefik with Polka's trusted local cert when serving HTTPS.
    auto scheme = "http";
    TraefikTLSConfig tlsBuf;
    TraefikTLSConfig* tlsPtr = null;
    if (endpoint.https)
    {
        auto kp = ensureGlobalTLSCertificateRuntimeKey(
            store.cacheDir,
            traefikRuntimeDir(store.rootDir, environment.name),
            host
        );
        tlsBuf = TraefikTLSConfig(kp.certPath, kp.keyPath);
        tlsPtr = &tlsBuf;
        scheme = "https";
    }

    auto upstreamURL = serverEndpointURL(endpoint);
    writeTraefikDynamicConfig(store.rootDir, environment.name, upstreamURL, endpoint.https, tlsPtr);

    return TraefikServeProxy(true, scheme ~ "://" ~ joinHostPort(host, liveState.port));
}

unittest
{
    import std.algorithm : canFind;
    import std.file : exists, mkdirRecurse, readText, write;
    import std.path : buildPath;
    import polka.backend : BufferTextWriter, newProjectStore;
    import polka.cli.command_traefik : pingTraefikAddressFunc;
    import polka.cli.serve_php : startBackgroundPHPRuntimeServe;
    import polka.cli.serve_runtime : pingServeAddressFunc;
    import polka.service : TraefikRuntimeState, traefikAddress, traefikStatePath, writeTraefikState;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    auto projectDir = createTempDir("polka-serve-cmd-");
    scope (exit) rmdirRecurseQuiet(projectDir);

    auto publicDir = buildPath(projectDir, "public");
    mkdirRecurse(publicDir);
    write(buildPath(publicDir, "index.php"), "<?php echo 'ok';\n");

    auto store = newProjectStore(projectDir);
    store.init();
    store.configureValue("default", "https", "false");
    store.configureValue("default", "server.hostname", "localhost");
    store.configureValue("default", "tools.php", "8.4");
    store.configureValue("default", "tools.traefik", "3.3");
    store.configureValue("default", "settings.traefik.port", "8090");

    // Seed a live Traefik state behind test ping seam and fake PHP background server.
    writeTraefikState(
        traefikStatePath(store.rootDir, "default"),
        TraefikRuntimeState("default", "3.3", 8090, 5555)
    );

    auto oldPingTraefik = pingTraefikAddressFunc;
    auto oldStartPHP = startBackgroundPHPRuntimeServe;
    auto oldPingServe = pingServeAddressFunc;
    scope (exit)
    {
        pingTraefikAddressFunc = oldPingTraefik;
        startBackgroundPHPRuntimeServe = oldStartPHP;
        pingServeAddressFunc = oldPingServe;
    }

    pingTraefikAddressFunc = (string addr) => addr == traefikAddress(8090);
    pingServeAddressFunc = (string addr) => addr == "127.0.0.1:8000";
    startBackgroundPHPRuntimeServe = (Store _s, Environment env, string serverAddress, AppLayout layout) {
        ServeRuntimeState st;
        st.environmentName = env.name;
        st.serverKind = ServerTypePHP;
        st.serverScheme = "http";
        st.serverAddress = serverAddress;
        st.docroot = layout.docroot;
        st.primaryPID = 4444;
        return st;
    };

    auto outBuf = new BufferTextWriter();
    auto errBuf = new BufferTextWriter();
    auto code = runStart(outBuf, errBuf, store, ServeCommandInput("public"));
    assert(code == 0, errBuf.text());
    assert(outBuf.text().canFind(`Started php webserver for environment "default" at http://localhost:8000.`));
    assert(outBuf.text().canFind("Reverse proxying through Traefik at http://localhost:8090."));
}
