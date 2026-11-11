module polka.cli.command_phpmyadmin;

// CLI runtime bridge for managed phpMyAdmin: selects HTTP (PHP built-in) vs
// HTTPS (FrankenPHP, nginx, or Apache) serving backends and wires CLI serve
// lifecycle hooks into `polka.service`.

import std.format : format;

import polka.backend : Environment, hasPHPCLI, PHPMyAdminConfig, Store;
import polka.cli.serve_apache : startApacheServeInBackgroundAt;
import polka.cli.serve_frankenphp : startFrankenPHPServeInBackgroundAt;
import polka.cli.serve_nginx : startNginxServeInBackgroundAt;
import polka.cli.serve_php : startPHPRuntimeServeInBackgroundAt;
import polka.cli.serve_runtime :
    callPingServeAddress,
    callStopServeRuntime,
    resolveEnvironmentServerType,
    resolveServeAppLayout;
import polka.cli.service_context : managedServiceContext;
import polka.cli.types : callServeNow;
import polka.config :
    ServerTypeApache,
    ServerTypeFrankenPHP,
    ServerTypeNginx;
import polka.service :
    AppLayout,
    DatabaseRuntimeHooks,
    Endpoint,
    ensurePHPMyAdminStorageConfigured,
    loadServicePHPMyAdminState = loadLivePHPMyAdminState,
    PHPMyAdminRuntimeHooks,
    ServeRuntimeState,
    ServiceContext,
    startServicePHPMyAdmin = ensureManagedPHPMyAdminStarted,
    stopServicePHPMyAdmin = stopManagedPHPMyAdmin;

/// Test seams for stubbing phpMyAdmin storage setup and backend webservers.
ServeRuntimeState delegate(Store, Environment, Endpoint, AppLayout, string) startPHPMyAdminServeFunc;
void delegate(ServiceContext, Environment, DatabaseRuntimeHooks) ensurePHPMyAdminStorageConfiguredFunc;
ServeRuntimeState delegate(Store, Environment, string, AppLayout, string) startPHPMyAdminPHPRuntimeServeFunc;
ServeRuntimeState delegate(Store, Environment, Endpoint, AppLayout, string) startPHPMyAdminNginxServeFunc;
ServeRuntimeState delegate(Store, Environment, Endpoint, AppLayout, string) startPHPMyAdminApacheServeFunc;
ServeRuntimeState delegate(Store, Environment, Endpoint, AppLayout, string) startPHPMyAdminFrankenPHPServeFunc;

/// Calls `ensurePHPMyAdminStorageConfiguredFunc` when set, otherwise `ensurePHPMyAdminStorageConfigured`.
void callEnsurePHPMyAdminStorageConfigured(
    ServiceContext ctx,
    Environment environment,
    DatabaseRuntimeHooks dbHooks
) @trusted
{
    if (ensurePHPMyAdminStorageConfiguredFunc !is null)
    {
        ensurePHPMyAdminStorageConfiguredFunc(ctx, environment, dbHooks);
        return;
    }
    ensurePHPMyAdminStorageConfigured(ctx, environment, dbHooks);
}

private ServeRuntimeState callStartPHPMyAdminServe(
    Store store,
    Environment environment,
    Endpoint endpoint,
    AppLayout layout,
    string runtimeDir
) @trusted
{
    if (startPHPMyAdminServeFunc !is null)
        return startPHPMyAdminServeFunc(store, environment, endpoint, layout, runtimeDir);
    return startPHPMyAdminServe(store, environment, endpoint, layout, runtimeDir);
}

private ServeRuntimeState callStartPHPMyAdminPHPRuntimeServe(
    Store store,
    Environment environment,
    string address,
    AppLayout layout,
    string runtimeDir
) @trusted
{
    if (startPHPMyAdminPHPRuntimeServeFunc !is null)
        return startPHPMyAdminPHPRuntimeServeFunc(store, environment, address, layout, runtimeDir);
    return startPHPRuntimeServeInBackgroundAt(store, environment, address, layout, runtimeDir);
}

private ServeRuntimeState callStartPHPMyAdminNginxServe(
    Store store,
    Environment environment,
    Endpoint endpoint,
    AppLayout layout,
    string runtimeDir
) @trusted
{
    if (startPHPMyAdminNginxServeFunc !is null)
        return startPHPMyAdminNginxServeFunc(store, environment, endpoint, layout, runtimeDir);
    return startNginxServeInBackgroundAt(store, environment, endpoint, layout, runtimeDir);
}

private ServeRuntimeState callStartPHPMyAdminApacheServe(
    Store store,
    Environment environment,
    Endpoint endpoint,
    AppLayout layout,
    string runtimeDir
) @trusted
{
    if (startPHPMyAdminApacheServeFunc !is null)
        return startPHPMyAdminApacheServeFunc(store, environment, endpoint, layout, runtimeDir);
    return startApacheServeInBackgroundAt(store, environment, endpoint, layout, runtimeDir);
}

private ServeRuntimeState callStartPHPMyAdminFrankenPHPServe(
    Store store,
    Environment environment,
    Endpoint endpoint,
    AppLayout layout,
    string runtimeDir
) @trusted
{
    if (startPHPMyAdminFrankenPHPServeFunc !is null)
        return startPHPMyAdminFrankenPHPServeFunc(store, environment, endpoint, layout, runtimeDir);
    return startFrankenPHPServeInBackgroundAt(store, environment, endpoint, layout, runtimeDir);
}

/// Ensures the managed phpMyAdmin webserver for `environment` is running.
ServeRuntimeState ensureManagedPHPMyAdminStarted(
    ref Store store,
    Environment environment,
    out bool alreadyStarted
) @trusted
{
    return startServicePHPMyAdmin(
        managedServiceContext(store, environment, null),
        phpMyAdminRuntimeHooks(store),
        alreadyStarted
    );
}

/// Stops the managed phpMyAdmin webserver for `environmentName` if running.
ServeRuntimeState stopManagedPHPMyAdmin(
    ref Store store,
    string environmentName,
    out bool alreadyStopped
) @trusted
{
    Environment env;
    env.name = environmentName;
    return stopServicePHPMyAdmin(
        managedServiceContext(store, env, null),
        phpMyAdminRuntimeHooks(store),
        alreadyStopped
    );
}

/// Builds `PHPMyAdminRuntimeHooks` delegating layout, start, stop, ping, and clock
/// through the CLI serve layer.
PHPMyAdminRuntimeHooks phpMyAdminRuntimeHooks(Store store) @trusted
{
    auto storeCopy = store;
    PHPMyAdminRuntimeHooks hooks;
    hooks.resolveLayout = (string docroot) => resolveServeAppLayout(docroot);
    hooks.startServe = (Environment env, Endpoint ep, AppLayout layout, string runtimeDir) =>
        callStartPHPMyAdminServe(storeCopy, env, ep, layout, runtimeDir);
    hooks.stopServe = (ServeRuntimeState st) { callStopServeRuntime(st); };
    hooks.pingAddress = (string addr) => callPingServeAddress(addr);
    hooks.now = () => callServeNow();
    return hooks;
}

/// Launches the background webserver serving phpMyAdmin for `environment`.
ServeRuntimeState startPHPMyAdminServe(
    Store store,
    Environment environment,
    Endpoint endpoint,
    AppLayout layout,
    string runtimeDir
) @trusted
{
    if (!hasPHPCLI(environment))
    {
        throw new Exception(format(
            `environment "%s" defines phpmyadmin but does not define a PHP CLI provider`,
            environment.name
        ));
    }

    if (endpoint.https)
    {
        auto serverType = resolveEnvironmentServerType(environment);
        switch (serverType)
        {
            case ServerTypeFrankenPHP:
                return callStartPHPMyAdminFrankenPHPServe(store, environment, endpoint, layout, runtimeDir);
            case ServerTypeNginx:
                return callStartPHPMyAdminNginxServe(store, environment, endpoint, layout, runtimeDir);
            case ServerTypeApache:
                return callStartPHPMyAdminApacheServe(store, environment, endpoint, layout, runtimeDir);
            default:
                throw new Exception("https requires nginx, Apache, or FrankenPHP in the current environment");
        }
    }

    return callStartPHPMyAdminPHPRuntimeServe(store, environment, endpoint.address, layout, runtimeDir);
}

/// Loads persisted phpMyAdmin state when its endpoint is still accepting TCP connections.
ServeRuntimeState* loadLivePHPMyAdminState(string rootDir, string environmentName) @trusted
{
    return loadServicePHPMyAdminState(rootDir, environmentName, (string addr) => callPingServeAddress(addr));
}

unittest
{
    import std.algorithm : canFind;
    import std.file : mkdirRecurse, readText, write;
    import std.path : buildPath;
    import polka.backend : ServerConfig;
    import polka.cli.serve_nginx : prepareNginxServeRuntime, quoteNginxPath;
    import polka.cli.serve_tls : serveTLSKeyFileName, serveTLSSubdir;
    import polka.cli.test_helpers : assertEncryptedTLSKeyFile, assertPlaintextTLSKeyFile;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    // 1. TestStartPHPMyAdminServeUsesSelectedFrankenPHPForHTTPS
    {
        auto oldPHP = startPHPMyAdminPHPRuntimeServeFunc;
        auto oldNginx = startPHPMyAdminNginxServeFunc;
        auto oldApache = startPHPMyAdminApacheServeFunc;
        auto oldFranken = startPHPMyAdminFrankenPHPServeFunc;
        scope (exit)
        {
            startPHPMyAdminPHPRuntimeServeFunc = oldPHP;
            startPHPMyAdminNginxServeFunc = oldNginx;
            startPHPMyAdminApacheServeFunc = oldApache;
            startPHPMyAdminFrankenPHPServeFunc = oldFranken;
        }

        int phpCalls, nginxCalls, apacheCalls, frankenCalls;
        startPHPMyAdminPHPRuntimeServeFunc = (Store _s, Environment _e, string _a, AppLayout _l, string _r) {
            phpCalls++;
            return ServeRuntimeState.init;
        };
        startPHPMyAdminNginxServeFunc = (Store _s, Environment _e, Endpoint _ep, AppLayout _l, string _r) {
            nginxCalls++;
            return ServeRuntimeState.init;
        };
        startPHPMyAdminApacheServeFunc = (Store _s, Environment _e, Endpoint _ep, AppLayout _l, string _r) {
            apacheCalls++;
            return ServeRuntimeState.init;
        };
        startPHPMyAdminFrankenPHPServeFunc = (Store _s, Environment _e, Endpoint ep, AppLayout _l, string rtDir) {
            frankenCalls++;
            assert(ep.https && ep.address == "127.0.0.1:8082" && rtDir == "phpmyadmin-runtime");
            ServeRuntimeState st;
            st.serverKind = "frankenphp";
            return st;
        };

        Environment env;
        env.name = "demo";
        env.tools = ["frankenphp": "1.12", "nginx": "1.30"];
        env.server = new ServerConfig();
        env.server.type = "frankenphp";

        Store emptyStore;
        auto st = startPHPMyAdminServe(
            emptyStore,
            env,
            Endpoint("https", "127.0.0.1:8082", true),
            AppLayout("phpmyadmin"),
            "phpmyadmin-runtime"
        );
        assert(st.serverKind == "frankenphp");
        assert(phpCalls == 0 && nginxCalls == 0 && apacheCalls == 0 && frankenCalls == 1);
    }

    // 2. TestStartPHPMyAdminServeUsesSelectedApacheForHTTPS
    {
        auto oldPHP = startPHPMyAdminPHPRuntimeServeFunc;
        auto oldNginx = startPHPMyAdminNginxServeFunc;
        auto oldApache = startPHPMyAdminApacheServeFunc;
        auto oldFranken = startPHPMyAdminFrankenPHPServeFunc;
        scope (exit)
        {
            startPHPMyAdminPHPRuntimeServeFunc = oldPHP;
            startPHPMyAdminNginxServeFunc = oldNginx;
            startPHPMyAdminApacheServeFunc = oldApache;
            startPHPMyAdminFrankenPHPServeFunc = oldFranken;
        }

        int phpCalls, nginxCalls, apacheCalls, frankenCalls;
        startPHPMyAdminPHPRuntimeServeFunc = (Store _s, Environment _e, string _a, AppLayout _l, string _r) {
            phpCalls++;
            return ServeRuntimeState.init;
        };
        startPHPMyAdminNginxServeFunc = (Store _s, Environment _e, Endpoint _ep, AppLayout _l, string _r) {
            nginxCalls++;
            return ServeRuntimeState.init;
        };
        startPHPMyAdminApacheServeFunc = (Store _s, Environment _e, Endpoint ep, AppLayout _l, string rtDir) {
            apacheCalls++;
            assert(ep.https && ep.address == "127.0.0.1:8082" && rtDir == "phpmyadmin-runtime");
            ServeRuntimeState st;
            st.serverKind = "apache";
            return st;
        };
        startPHPMyAdminFrankenPHPServeFunc = (Store _s, Environment _e, Endpoint _ep, AppLayout _l, string _r) {
            frankenCalls++;
            return ServeRuntimeState.init;
        };

        Environment env;
        env.name = "demo";
        env.tools = ["php": "8.4", "apache": "2.4", "nginx": "1.30"];
        env.server = new ServerConfig();
        env.server.type = "apache";

        Store emptyStore;
        auto st = startPHPMyAdminServe(
            emptyStore,
            env,
            Endpoint("https", "127.0.0.1:8082", true),
            AppLayout("phpmyadmin"),
            "phpmyadmin-runtime"
        );
        assert(st.serverKind == "apache");
        assert(phpCalls == 0 && nginxCalls == 0 && apacheCalls == 1 && frankenCalls == 0);
    }

    // 3. TestStartPHPMyAdminServeUsesRuntimeTLSKeyForEncryptedGlobalKey
    {
        auto oldNginx = startPHPMyAdminNginxServeFunc;
        scope (exit) startPHPMyAdminNginxServeFunc = oldNginx;

        auto root = createTempDir("polka-pma-tls-");
        scope (exit) rmdirRecurseQuiet(root);

        auto runtimeDir = buildPath(root, "run", "phpmyadmin", "demo");
        auto docroot = buildPath(root, "envs", "phpmyadmin", "5.2");
        mkdirRecurse(docroot);
        write(buildPath(docroot, "index.php"), "<?php\n");

        Store store;
        store.rootDir = root;
        store.cacheDir = buildPath(root, "cache");

        Environment env;
        env.name = "demo";
        env.tools = ["php": "8.4", "nginx": "1.30"];
        env.server = new ServerConfig();
        env.server.type = "nginx";

        auto ep = Endpoint("https", "127.0.0.1:8082", true);
        auto layout = resolveServeAppLayout(docroot);

        startPHPMyAdminNginxServeFunc = (Store st, Environment environment, Endpoint endpoint, AppLayout lyt, string rtDir) {
            string cfgPath, phpLogPath;
            prepareNginxServeRuntime(st.cacheDir, rtDir, environment, endpoint, lyt, "127.0.0.1:19000", cfgPath, phpLogPath);
            ServeRuntimeState state;
            state.serverKind = "nginx";
            state.serverScheme = endpoint.scheme;
            state.serverAddress = endpoint.address;
            state.docroot = lyt.docroot;
            state.runtimeDir = rtDir;
            state.configPath = cfgPath;
            state.primaryPID = 6262;
            return state;
        };

        auto state = startPHPMyAdminServe(store, env, ep, layout, runtimeDir);
        auto globalKeyPath = buildPath(store.cacheDir, "polka", serveTLSSubdir, serveTLSKeyFileName);
        auto runtimeKeyPath = buildPath(runtimeDir, serveTLSKeyFileName);
        assertEncryptedTLSKeyFile(globalKeyPath);
        assertPlaintextTLSKeyFile(runtimeKeyPath);

        auto cfg = readText(state.configPath);
        assert(!cfg.canFind(quoteNginxPath(globalKeyPath)));
        assert(cfg.canFind("ssl_certificate_key " ~ quoteNginxPath(runtimeKeyPath) ~ ";"));
    }
}
