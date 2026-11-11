module polka.cli.hooks;

// Ordered service startup, webserver selection, stop, config status, and
// runtime status hook registries mirroring Go's `cli/hooks.go`.

import std.datetime : UTC;
import std.format : format;
import std.string : strip;

import polka.backend :
    Environment,
    phpCLIProvider,
    Store,
    TextWriter,
    writeOutf,
    writeOutln;
import polka.cli.command_db :
    callPingDatabaseAddress,
    dbResolvedEnvironment,
    dbRuntimeHooks,
    ensureManagedDatabaseStarted;
import polka.cli.command_mailpit :
    ensureManagedMailpitStarted,
    loadLiveMailpitState,
    mailpitRuntimeHooks,
    stopManagedMailpit;
import polka.cli.command_meilisearch :
    ensureManagedMeilisearchStarted,
    loadLiveMeilisearchState,
    meilisearchRuntimeHooks,
    stopManagedMeilisearch;
import polka.cli.command_phpmyadmin :
    callEnsurePHPMyAdminStorageConfigured,
    ensureManagedPHPMyAdminStarted,
    loadLivePHPMyAdminState,
    phpMyAdminRuntimeHooks,
    stopManagedPHPMyAdmin;
import polka.cli.command_rabbitmq :
    ensureManagedRabbitMQStarted,
    loadLiveRabbitMQState,
    rabbitMQRuntimeHooks,
    stopManagedRabbitMQ;
import polka.cli.command_redis :
    ensureManagedRedisStarted,
    loadLiveRedisState,
    redisRuntimeHooks,
    stopManagedRedis;
import polka.cli.command_traefik :
    ensureManagedTraefikStarted,
    loadLiveTraefikState,
    stopManagedTraefik,
    traefikRuntimeHooks;
import polka.cli.command_workers :
    loadLiveWorkersState,
    stopManagedWorkers,
    workerProcessSummary,
    workersRuntimeHooks;
import polka.cli.env_helpers :
    labelDatabase,
    labelMailpit,
    labelMeilisearch,
    labelOrUnset,
    labelPHPMyAdmin,
    labelRabbitMQ,
    labelRedis,
    labelTraefik;
import polka.cli.serve_apache :
    callRunApacheServe,
    callStartBackgroundApacheServe;
import polka.cli.serve_frankenphp :
    callRunFrankenPHPServe,
    callStartBackgroundFrankenPHPServe;
import polka.cli.serve_nginx :
    callRunNginxServe,
    callStartBackgroundNginxServe;
import polka.cli.serve_php :
    callRunPHPRuntimeServe,
    callStartBackgroundPHPRuntimeServe;
import polka.cli.serve_runtime :
    callStopServeRuntime,
    loadWebServerState,
    serveAppLayout,
    serverEndpoint,
    serverEndpointURL,
    serveRuntimeLabel,
    serveStatePath,
    serveStateURL,
    stopManagedServe,
    writeServeState;
import polka.cli.service_context : managedServiceContext;
import polka.cli.types : callServeNow, quoteGoString;
import polka.config :
    effectiveWorkerReplicas,
    primaryPHPVersion,
    ServerTypeApache,
    ServerTypeFrankenPHP,
    ServerTypeNginx,
    ServerTypePHP,
    sortedWorkerNames;
import polka.service :
    AppLayout,
    DatabaseRuntimeHooks,
    defaultManager,
    Endpoint,
    loadLiveManagedDatabaseState,
    mailpitUIURL,
    meilisearchURL,
    rabbitMQManagementURLForConfig,
    rabbitMQURLForConfig,
    redisURL,
    RuntimeHooks,
    ServeRuntimeState,
    ServiceContext,
    StopManagedDatabase = stopManagedDatabase,
    StopResult,
    traefikURL;

/// Resolved `serve` / `start` command flags and positional argument.
struct ServeCommandInput
{
    string docroot;
    string server;
    bool watch = false;
}

/// Context passed to service startup hooks.
struct StartHookContext
{
    TextWriter stdout;
    TextWriter stderr;
    Store store;
    Environment environment;
}

/// Context passed to webserver startup hooks.
struct WebserverStartHookContext
{
    TextWriter stdout;
    TextWriter stderr;
    Store store;
    Environment environment;
    string serverType;
    ServeCommandInput input;
    serverEndpoint endpoint;
    serveAppLayout layout;
}

/// Context passed to stop hooks.
struct StopHookContext
{
    TextWriter stdout;
    TextWriter stderr;
    Store store;
    Environment environment;
}

/// Context passed to config and runtime status hooks.
struct StatusHookContext
{
    TextWriter stdout;
    TextWriter stderr;
    Store store;
    Environment environment;
}

/// Named service start hook descriptor.
struct StartServiceHook
{
    string id;
    void delegate(StartHookContext) run;
}

/// Named webserver start hook descriptor.
struct WebserverStartHook
{
    string id;
    bool delegate(WebserverStartHookContext) matches;
    int delegate(WebserverStartHookContext) run;
}

/// Named stop hook descriptor.
struct StopHook
{
    string id;
    void delegate(StopHookContext) run;
}

/// Named status reporting hook descriptor.
struct StatusHook
{
    string id;
    void delegate(StatusHookContext) run;
}

/// Holds the ordered hook chains for service startup, webserver dispatch,
/// shutdown, configuration summary, and live runtime status reporting.
struct CLIHookRegistry
{
    StartServiceHook[] startServicesHooks;
    WebserverStartHook[] webservers;
    StopHook[] stopHooks;
    StatusHook[] configStatusHooks;
    StatusHook[] runtimeStatusHooks;

    /// Starts all environment-configured background services in dependency order
    /// via `polka.service.defaultManager` and summarizes phpMyAdmin / worker launches.
    void startServices(StartHookContext ctx) @trusted
    {
        RuntimeHooks hooks;
        hooks.database = dbRuntimeHooks();
        hooks.mailpit = mailpitRuntimeHooks();
        hooks.meilisearch = meilisearchRuntimeHooks();
        hooks.redis = redisRuntimeHooks();
        hooks.rabbitMQ = rabbitMQRuntimeHooks();
        hooks.traefik = traefikRuntimeHooks();
        hooks.phpMyAdmin = phpMyAdminRuntimeHooks(ctx.store);
        hooks.workers = workersRuntimeHooks(ctx.store, ctx.environment);
        hooks.ensurePHPMyAdminStorage = (ServiceContext svcCtx, Environment env, DatabaseRuntimeHooks dbHooks) {
            callEnsurePHPMyAdminStorageConfigured(svcCtx, env, dbHooks);
        };

        auto result = defaultManager().start(
            managedServiceContext(ctx.store, ctx.environment, ctx.stderr),
            hooks
        );
        if (result.phpMyAdmin !is null)
        {
            if (result.phpMyAdmin.alreadyStarted)
            {
                writeOutf(
                    ctx.stdout,
                    "phpMyAdmin for environment %s is already running at %s.\n",
                    quoteGoString(ctx.environment.name),
                    serveStateURL(result.phpMyAdmin.state)
                );
            }
            else
            {
                writeOutf(
                    ctx.stdout,
                    "Started phpMyAdmin for environment %s at %s.\n",
                    quoteGoString(ctx.environment.name),
                    serveStateURL(result.phpMyAdmin.state)
                );
            }
        }
        if (result.workers !is null)
        {
            if (result.workers.alreadyStarted)
            {
                writeOutf(
                    ctx.stdout,
                    "Workers for environment %s are already running.\n",
                    quoteGoString(ctx.environment.name)
                );
            }
            else if (result.workers.state.processes.length > 0)
            {
                // When every replica failed to launch, failures were already
                // warned; nothing to summarize.
                writeOutf(
                    ctx.stdout,
                    "Started %d worker process(es) for environment %s (%s).\n",
                    result.workers.state.processes.length,
                    quoteGoString(ctx.environment.name),
                    workerProcessSummary(result.workers.state)
                );
            }
        }
    }

    /// Selects and starts the webserver provider configured in `ctx.serverType`.
    int startWebserver(WebserverStartHookContext ctx) @trusted
    {
        foreach (hook; webservers)
        {
            if (hook.matches(ctx))
                return hook.run(ctx);
        }
        throw new Exception(format(
            "environment %s does not define a supported webserver",
            quoteGoString(ctx.environment.name)
        ));
    }

    /// Stops the active webserver first, then delegates managed service shutdown
    /// in reverse dependency order to `polka.service.defaultManager`.
    void stop(StopHookContext ctx) @trusted
    {
        foreach (hook; stopHooks)
        {
            if (hook.id != "webserver")
                continue;
            hook.run(ctx);
        }

        RuntimeHooks hooks;
        hooks.database = dbRuntimeHooks();
        hooks.mailpit = mailpitRuntimeHooks();
        hooks.meilisearch = meilisearchRuntimeHooks();
        hooks.redis = redisRuntimeHooks();
        hooks.rabbitMQ = rabbitMQRuntimeHooks();
        hooks.traefik = traefikRuntimeHooks();
        hooks.phpMyAdmin = phpMyAdminRuntimeHooks(ctx.store);
        hooks.workers = workersRuntimeHooks(ctx.store, ctx.environment);

        auto result = defaultManager().stop(
            managedServiceContext(ctx.store, ctx.environment, ctx.stderr),
            hooks
        );
        writeManagedServiceStopSummary(ctx, result);
    }

    /// Emits all configured tool/service summary lines in registry order.
    void writeConfigStatus(StatusHookContext ctx) @trusted
    {
        foreach (hook; configStatusHooks)
            hook.run(ctx);
    }

    /// Warns on missing runtime tools and emits live process status lines.
    void writeRuntimeStatus(StatusHookContext ctx) @trusted
    {
        defaultManager().warnMissingRuntimeTools(
            managedServiceContext(ctx.store, ctx.environment, ctx.stderr)
        );
        foreach (hook; runtimeStatusHooks)
            hook.run(ctx);
    }
}

/// Constructs the canonical ordered `CLIHookRegistry`.
CLIHookRegistry defaultCLIHookRegistry() @trusted
{
    CLIHookRegistry r;
    r.startServicesHooks = [
        StartServiceHook("database", (StartHookContext c) => startDatabaseServiceHook(c)),
        StartServiceHook("mailpit", (StartHookContext c) => startMailpitServiceHook(c)),
        StartServiceHook("meilisearch", (StartHookContext c) => startMeilisearchServiceHook(c)),
        StartServiceHook("redis", (StartHookContext c) => startRedisServiceHook(c)),
        StartServiceHook("rabbitmq", (StartHookContext c) => startRabbitMQServiceHook(c)),
        StartServiceHook("traefik", (StartHookContext c) => startTraefikServiceHook(c)),
        StartServiceHook("phpmyadmin", (StartHookContext c) => startPHPMyAdminServiceHook(c)),
    ];
    r.webservers = [
        WebserverStartHook("nginx", (WebserverStartHookContext c) => environmentUsesNginx(c), (WebserverStartHookContext c) => startNginxWebserverHook(c)),
        WebserverStartHook("apache", (WebserverStartHookContext c) => environmentUsesApache(c), (WebserverStartHookContext c) => startApacheWebserverHook(c)),
        WebserverStartHook("frankenphp", (WebserverStartHookContext c) => environmentUsesFrankenPHP(c), (WebserverStartHookContext c) => startFrankenPHPWebserverHook(c)),
        WebserverStartHook("php", (WebserverStartHookContext c) => environmentUsesPHPWebserver(c), (WebserverStartHookContext c) => startPHPWebserverHook(c)),
    ];
    r.stopHooks = [
        StopHook("webserver", (StopHookContext c) => stopWebserverHook(c)),
        StopHook("workers", (StopHookContext c) => stopWorkersServiceHook(c)),
        StopHook("phpmyadmin", (StopHookContext c) => stopPHPMyAdminServiceHook(c)),
        StopHook("traefik", (StopHookContext c) => stopTraefikServiceHook(c)),
        StopHook("redis", (StopHookContext c) => stopRedisServiceHook(c)),
        StopHook("rabbitmq", (StopHookContext c) => stopRabbitMQServiceHook(c)),
        StopHook("meilisearch", (StopHookContext c) => stopMeilisearchServiceHook(c)),
        StopHook("database", (StopHookContext c) => stopDatabaseServiceHook(c)),
        StopHook("mailpit", (StopHookContext c) => stopMailpitServiceHook(c)),
    ];
    r.configStatusHooks = [
        StatusHook("php", (StatusHookContext c) => statusPHPConfigHook(c)),
        StatusHook("composer", (StatusHookContext c) => statusComposerConfigHook(c)),
        StatusHook("pie", (StatusHookContext c) => statusPIEConfigHook(c)),
        StatusHook("nodejs", (StatusHookContext c) => statusNodeJSConfigHook(c)),
        StatusHook("mago", (StatusHookContext c) => statusMagoConfigHook(c)),
        StatusHook("nginx", (StatusHookContext c) => statusNginxConfigHook(c)),
        StatusHook("apache", (StatusHookContext c) => statusApacheConfigHook(c)),
        StatusHook("frankenphp", (StatusHookContext c) => statusFrankenPHPConfigHook(c)),
        StatusHook("roadrunner", (StatusHookContext c) => statusRoadRunnerConfigHook(c)),
        StatusHook("sqlite", (StatusHookContext c) => statusSQLiteConfigHook(c)),
        StatusHook("meilisearch", (StatusHookContext c) => statusMeilisearchConfigHook(c)),
        StatusHook("redis", (StatusHookContext c) => statusRedisConfigHook(c)),
        StatusHook("rabbitmq", (StatusHookContext c) => statusRabbitMQConfigHook(c)),
        StatusHook("traefik", (StatusHookContext c) => statusTraefikConfigHook(c)),
        StatusHook("phpmyadmin", (StatusHookContext c) => statusPHPMyAdminConfigHook(c)),
        StatusHook("database", (StatusHookContext c) => statusDatabaseConfigHook(c)),
        StatusHook("mailpit", (StatusHookContext c) => statusMailpitConfigHook(c)),
        StatusHook("workers", (StatusHookContext c) => statusWorkersConfigHook(c)),
    ];
    r.runtimeStatusHooks = [
        StatusHook("webserver", (StatusHookContext c) => statusWebserverRuntimeHook(c)),
        StatusHook("workers", (StatusHookContext c) => statusWorkersRuntimeHook(c)),
        StatusHook("phpmyadmin", (StatusHookContext c) => statusPHPMyAdminRuntimeHook(c)),
        StatusHook("meilisearch", (StatusHookContext c) => statusMeilisearchRuntimeHook(c)),
        StatusHook("redis", (StatusHookContext c) => statusRedisRuntimeHook(c)),
        StatusHook("rabbitmq", (StatusHookContext c) => statusRabbitMQRuntimeHook(c)),
        StatusHook("traefik", (StatusHookContext c) => statusTraefikRuntimeHook(c)),
        StatusHook("database", (StatusHookContext c) => statusDatabaseRuntimeHook(c)),
        StatusHook("mailpit", (StatusHookContext c) => statusMailpitRuntimeHook(c)),
    ];
    return r;
}

private void writeManagedServiceStopSummary(StopHookContext ctx, StopResult result) @trusted
{
    if (result.workers !is null)
    {
        if (result.workers.alreadyStopped)
        {
            if (ctx.environment.workers.length > 0)
                writeOutf(ctx.stdout, "Workers for environment %s are already stopped.\n", quoteGoString(ctx.environment.name));
        }
        else
        {
            writeOutf(
                ctx.stdout,
                "Stopped %d worker process(es) for environment %s.\n",
                result.workers.state.processes.length,
                quoteGoString(ctx.environment.name)
            );
        }
    }

    if (result.phpMyAdmin !is null)
    {
        if (result.phpMyAdmin.alreadyStopped)
        {
            if (ctx.environment.toolVersion("phpmyadmin").strip().length > 0)
                writeOutf(ctx.stdout, "phpMyAdmin for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        }
        else
        {
            writeOutf(ctx.stdout, "Stopped phpMyAdmin for environment %s.\n", quoteGoString(ctx.environment.name));
        }
    }

    if (result.meilisearch !is null)
    {
        if (result.meilisearch.alreadyStopped)
        {
            if (ctx.environment.toolVersion("meilisearch").strip().length > 0)
                writeOutf(ctx.stdout, "Meilisearch for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        }
        else
        {
            writeOutf(ctx.stdout, "Stopped Meilisearch for environment %s.\n", quoteGoString(ctx.environment.name));
        }
    }

    if (result.redis !is null)
    {
        if (result.redis.alreadyStopped)
        {
            if (ctx.environment.toolVersion("redis").strip().length > 0)
                writeOutf(ctx.stdout, "Redis for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        }
        else
        {
            writeOutf(ctx.stdout, "Stopped Redis for environment %s.\n", quoteGoString(ctx.environment.name));
        }
    }

    if (result.rabbitMQ !is null)
    {
        if (result.rabbitMQ.alreadyStopped)
        {
            if (ctx.environment.toolVersion("rabbitmq").strip().length > 0)
                writeOutf(ctx.stdout, "RabbitMQ for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        }
        else
        {
            writeOutf(ctx.stdout, "Stopped RabbitMQ for environment %s.\n", quoteGoString(ctx.environment.name));
        }
    }

    if (result.traefik !is null)
    {
        if (result.traefik.alreadyStopped)
        {
            if (ctx.environment.toolVersion("traefik").strip().length > 0)
                writeOutf(ctx.stdout, "Traefik for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        }
        else
        {
            writeOutf(ctx.stdout, "Stopped Traefik for environment %s.\n", quoteGoString(ctx.environment.name));
        }
    }

    if (result.database !is null)
    {
        if (result.database.alreadyStopped)
        {
            if (ctx.environment.database !is null && ctx.environment.database.engine.strip().length > 0)
                writeOutf(ctx.stdout, "Database for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        }
        else
        {
            writeOutf(
                ctx.stdout,
                "Stopped %s for environment %s.\n",
                result.database.state.engine,
                quoteGoString(ctx.environment.name)
            );
        }
    }

    if (result.mailpit !is null)
    {
        if (result.mailpit.alreadyStopped)
        {
            if (ctx.environment.toolVersion("mailpit").strip().length > 0)
                writeOutf(ctx.stdout, "Mailpit for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        }
        else
        {
            writeOutf(ctx.stdout, "Stopped mailpit for environment %s.\n", quoteGoString(ctx.environment.name));
        }
    }
}

/// Starts the environment's managed database service hook.
void startDatabaseServiceHook(StartHookContext ctx) @trusted
{
    if (ctx.environment.database is null || ctx.environment.database.engine.strip().length == 0)
        return;

    auto resolved = dbResolvedEnvironment(ctx.environment, ctx.environment.database);
    bool unused;
    ensureManagedDatabaseStarted(ctx.store, resolved, unused);
}

private void startMailpitServiceHook(StartHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("mailpit").strip().length == 0)
        return;
    bool unused;
    ensureManagedMailpitStarted(ctx.store, ctx.environment, unused);
}

private void startMeilisearchServiceHook(StartHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("meilisearch").strip().length == 0)
        return;
    bool unused;
    ensureManagedMeilisearchStarted(ctx.store, ctx.environment, unused);
}

private void startRedisServiceHook(StartHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("redis").strip().length == 0)
        return;
    bool unused;
    ensureManagedRedisStarted(ctx.store, ctx.environment, unused);
}

private void startRabbitMQServiceHook(StartHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("rabbitmq").strip().length == 0)
        return;
    bool unused;
    ensureManagedRabbitMQStarted(ctx.store, ctx.environment, unused);
}

private void startTraefikServiceHook(StartHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("traefik").strip().length == 0)
        return;
    bool unused;
    ensureManagedTraefikStarted(ctx.store, ctx.environment, unused);
}

private void startPHPMyAdminServiceHook(StartHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("phpmyadmin").strip().length == 0)
        return;

    callEnsurePHPMyAdminStorageConfigured(
        managedServiceContext(ctx.store, ctx.environment, ctx.stderr),
        ctx.environment,
        dbRuntimeHooks()
    );

    bool alreadyStarted = false;
    auto state = ensureManagedPHPMyAdminStarted(ctx.store, ctx.environment, alreadyStarted);
    if (alreadyStarted)
    {
        writeOutf(
            ctx.stdout,
            "phpMyAdmin for environment %s is already running at %s.\n",
            quoteGoString(ctx.environment.name),
            serveStateURL(state)
        );
        return;
    }

    writeOutf(
        ctx.stdout,
        "Started phpMyAdmin for environment %s at %s.\n",
        quoteGoString(ctx.environment.name),
        serveStateURL(state)
    );
}

private bool environmentUsesNginx(WebserverStartHookContext ctx) @safe pure nothrow @nogc
{
    return ctx.serverType == ServerTypeNginx;
}

private bool environmentUsesApache(WebserverStartHookContext ctx) @safe pure nothrow @nogc
{
    return ctx.serverType == ServerTypeApache;
}

private bool environmentUsesFrankenPHP(WebserverStartHookContext ctx) @safe pure nothrow @nogc
{
    return ctx.serverType == ServerTypeFrankenPHP;
}

private bool environmentUsesPHPWebserver(WebserverStartHookContext ctx) @safe pure nothrow @nogc
{
    return ctx.serverType == ServerTypePHP;
}

private int startPHPWebserverHook(WebserverStartHookContext ctx) @trusted
{
    if (ctx.input.watch)
        return callRunPHPRuntimeServe(ctx.stdout, ctx.stderr, ctx.store, ctx.endpoint.address, ctx.layout);

    auto startedState = callStartBackgroundPHPRuntimeServe(ctx.store, ctx.environment, ctx.endpoint.address, ctx.layout);
    return finishBackgroundWebserverStart(ctx, startedState);
}

private int startNginxWebserverHook(WebserverStartHookContext ctx) @trusted
{
    if (primaryPHPVersion(ctx.environment).length == 0)
    {
        throw new Exception(format(
            "environment %s defines nginx but does not define a php version",
            quoteGoString(ctx.environment.name)
        ));
    }

    if (ctx.input.watch)
    {
        writeOutf(ctx.stdout, "nginx webserver started at %s\n", serverEndpointURL(ctx.endpoint));
        return callRunNginxServe(ctx.stdout, ctx.stderr, ctx.store, ctx.environment, ctx.endpoint, ctx.layout);
    }

    auto startedState = callStartBackgroundNginxServe(ctx.store, ctx.environment, ctx.endpoint, ctx.layout);
    return finishBackgroundWebserverStart(ctx, startedState);
}

private int startApacheWebserverHook(WebserverStartHookContext ctx) @trusted
{
    if (primaryPHPVersion(ctx.environment).length == 0)
    {
        throw new Exception(format(
            "environment %s defines apache but does not define a php version",
            quoteGoString(ctx.environment.name)
        ));
    }

    if (ctx.input.watch)
    {
        writeOutf(ctx.stdout, "apache webserver started at %s\n", serverEndpointURL(ctx.endpoint));
        return callRunApacheServe(ctx.stdout, ctx.stderr, ctx.store, ctx.environment, ctx.endpoint, ctx.layout);
    }

    auto startedState = callStartBackgroundApacheServe(ctx.store, ctx.environment, ctx.endpoint, ctx.layout);
    return finishBackgroundWebserverStart(ctx, startedState);
}

private int startFrankenPHPWebserverHook(WebserverStartHookContext ctx) @trusted
{
    if (ctx.input.watch)
    {
        writeOutf(ctx.stdout, "FrankenPHP webserver started at %s\n", serverEndpointURL(ctx.endpoint));
        return callRunFrankenPHPServe(ctx.stdout, ctx.stderr, ctx.store, ctx.environment, ctx.endpoint, ctx.layout);
    }

    auto startedState = callStartBackgroundFrankenPHPServe(ctx.store, ctx.environment, ctx.endpoint, ctx.layout);
    return finishBackgroundWebserverStart(ctx, startedState);
}

private int finishBackgroundWebserverStart(WebserverStartHookContext ctx, ServeRuntimeState startedState) @trusted
{
    if (startedState.environmentName.strip().length == 0)
        startedState.environmentName = ctx.environment.name;
    if (startedState.serverKind.strip().length == 0)
        startedState.serverKind = ctx.serverType;
    if (startedState.serverScheme.strip().length == 0)
        startedState.serverScheme = ctx.endpoint.scheme;
    if (startedState.serverAddress.strip().length == 0)
        startedState.serverAddress = ctx.endpoint.address;
    if (startedState.docroot.strip().length == 0)
        startedState.docroot = ctx.layout.docroot;
    if (startedState.startedAt == startedState.startedAt.init)
        startedState.startedAt = callServeNow().toUTC();

    try
    {
        writeServeState(serveStatePath(ctx.store.rootDir, ctx.environment.name), startedState);
    }
    catch (Exception e)
    {
        try
        {
            callStopServeRuntime(startedState);
        }
        catch (Exception) {}
        throw e;
    }

    writeOutf(
        ctx.stdout,
        "Started %s for environment %s at %s.\n",
        serveRuntimeLabel(startedState.serverKind),
        quoteGoString(ctx.environment.name),
        serveStateURL(startedState)
    );
    writeOutln(ctx.stdout, "Run `polka stop` to stop it.");
    return 0;
}

private void stopWebserverHook(StopHookContext ctx) @trusted
{
    bool alreadyStopped = false;
    auto state = stopManagedServe(ctx.store, ctx.environment.name, alreadyStopped);
    if (alreadyStopped)
    {
        writeOutf(ctx.stdout, "Webserver for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        return;
    }

    writeOutf(
        ctx.stdout,
        "Stopped %s for environment %s.\n",
        serveRuntimeLabel(state.serverKind),
        quoteGoString(ctx.environment.name)
    );
}

private void stopDatabaseServiceHook(StopHookContext ctx) @trusted
{
    if (ctx.environment.database is null || ctx.environment.database.engine.strip().length == 0)
        return;

    auto resolved = dbResolvedEnvironment(ctx.environment, ctx.environment.database);
    bool alreadyStopped = false;
    auto state = StopManagedDatabase(
        managedServiceContext(ctx.store, ctx.environment, ctx.stderr),
        resolved,
        dbRuntimeHooks(),
        alreadyStopped
    );
    if (alreadyStopped)
    {
        writeOutf(ctx.stdout, "Database for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        return;
    }

    writeOutf(ctx.stdout, "Stopped %s for environment %s.\n", state.engine, quoteGoString(ctx.environment.name));
}

private void stopMailpitServiceHook(StopHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("mailpit").strip().length == 0)
        return;

    bool alreadyStopped = false;
    stopManagedMailpit(ctx.store, ctx.environment.name, alreadyStopped);
    if (alreadyStopped)
    {
        writeOutf(ctx.stdout, "Mailpit for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        return;
    }
    writeOutf(ctx.stdout, "Stopped mailpit for environment %s.\n", quoteGoString(ctx.environment.name));
}

private void stopMeilisearchServiceHook(StopHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("meilisearch").strip().length == 0)
        return;

    bool alreadyStopped = false;
    stopManagedMeilisearch(ctx.store, ctx.environment.name, alreadyStopped);
    if (alreadyStopped)
    {
        writeOutf(ctx.stdout, "Meilisearch for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        return;
    }
    writeOutf(ctx.stdout, "Stopped Meilisearch for environment %s.\n", quoteGoString(ctx.environment.name));
}

private void stopRedisServiceHook(StopHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("redis").strip().length == 0)
        return;

    bool alreadyStopped = false;
    stopManagedRedis(ctx.store, ctx.environment.name, alreadyStopped);
    if (alreadyStopped)
    {
        writeOutf(ctx.stdout, "Redis for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        return;
    }
    writeOutf(ctx.stdout, "Stopped Redis for environment %s.\n", quoteGoString(ctx.environment.name));
}

private void stopRabbitMQServiceHook(StopHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("rabbitmq").strip().length == 0)
        return;

    bool alreadyStopped = false;
    stopManagedRabbitMQ(ctx.store, ctx.environment.name, alreadyStopped);
    if (alreadyStopped)
    {
        writeOutf(ctx.stdout, "RabbitMQ for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        return;
    }
    writeOutf(ctx.stdout, "Stopped RabbitMQ for environment %s.\n", quoteGoString(ctx.environment.name));
}

private void stopTraefikServiceHook(StopHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("traefik").strip().length == 0)
        return;

    bool alreadyStopped = false;
    stopManagedTraefik(ctx.store, ctx.environment.name, alreadyStopped);
    if (alreadyStopped)
    {
        writeOutf(ctx.stdout, "Traefik for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        return;
    }
    writeOutf(ctx.stdout, "Stopped Traefik for environment %s.\n", quoteGoString(ctx.environment.name));
}

private void stopPHPMyAdminServiceHook(StopHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("phpmyadmin").strip().length == 0)
        return;

    bool alreadyStopped = false;
    stopManagedPHPMyAdmin(ctx.store, ctx.environment.name, alreadyStopped);
    if (alreadyStopped)
    {
        writeOutf(ctx.stdout, "phpMyAdmin for environment %s is already stopped.\n", quoteGoString(ctx.environment.name));
        return;
    }
    writeOutf(ctx.stdout, "Stopped phpMyAdmin for environment %s.\n", quoteGoString(ctx.environment.name));
}

private void stopWorkersServiceHook(StopHookContext ctx) @trusted
{
    if (ctx.environment.workers.length == 0)
        return;

    bool alreadyStopped = false;
    auto state = stopManagedWorkers(ctx.store, ctx.environment, alreadyStopped);
    if (alreadyStopped)
    {
        writeOutf(ctx.stdout, "Workers for environment %s are already stopped.\n", quoteGoString(ctx.environment.name));
        return;
    }
    writeOutf(
        ctx.stdout,
        "Stopped %d worker process(es) for environment %s.\n",
        state.processes.length,
        quoteGoString(ctx.environment.name)
    );
}

private void statusWorkersConfigHook(StatusHookContext ctx) @trusted
{
    if (ctx.environment.workers.length == 0)
    {
        writeOutln(ctx.stdout, "workers unset");
        return;
    }

    foreach (name; sortedWorkerNames(ctx.environment.workers))
    {
        auto worker = ctx.environment.workers[name];
        auto replicas = effectiveWorkerReplicas(worker);
        if (replicas > 1)
            writeOutf(ctx.stdout, "worker %s: %s (replicas %d)\n", name, worker.command, replicas);
        else
            writeOutf(ctx.stdout, "worker %s: %s\n", name, worker.command);
    }
}

private void statusWorkersRuntimeHook(StatusHookContext ctx) @trusted
{
    if (ctx.environment.workers.length == 0)
    {
        writeOutln(ctx.stdout, "workers unset");
        return;
    }

    auto liveWorkersState = loadLiveWorkersState(ctx.store.rootDir, ctx.environment.name);
    if (liveWorkersState is null)
    {
        writeOutln(ctx.stdout, "workers stopped");
        return;
    }

    int configured = 0;
    foreach (_, worker; ctx.environment.workers)
        configured += effectiveWorkerReplicas(worker);
    writeOutf(ctx.stdout, "workers running %d/%d\n", liveWorkersState.processes.length, configured);
}

private void statusPHPConfigHook(StatusHookContext ctx) @trusted
{
    string phpTool, phpVersion;
    phpCLIProvider(ctx.environment, phpTool, phpVersion);
    if (phpTool.length == 0)
        phpTool = "php";
    if (phpTool == ServerTypeFrankenPHP)
    {
        writeOutf(ctx.stdout, "php via frankenphp %s\n", phpVersion);
        return;
    }
    writeOutf(ctx.stdout, "%s %s\n", phpTool, labelOrUnset(phpVersion));
}

private void statusComposerConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "composer %s\n", labelOrUnset(ctx.environment.toolVersion("composer")));
}

private void statusPIEConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "pie %s\n", labelOrUnset(ctx.environment.toolVersion("pie")));
}

private void statusNodeJSConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "nodejs %s\n", labelOrUnset(ctx.environment.toolVersion("nodejs")));
}

private void statusMagoConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "mago %s\n", labelOrUnset(ctx.environment.toolVersion("mago")));
}

private void statusNginxConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "nginx %s\n", labelOrUnset(ctx.environment.toolVersion("nginx")));
}

private void statusApacheConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "apache %s\n", labelOrUnset(ctx.environment.toolVersion("apache")));
}

private void statusFrankenPHPConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "frankenphp %s\n", labelOrUnset(ctx.environment.toolVersion("frankenphp")));
}

private void statusRoadRunnerConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "roadrunner %s\n", labelOrUnset(ctx.environment.toolVersion("roadrunner")));
}

private void statusSQLiteConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "sqlite %s\n", labelOrUnset(ctx.environment.toolVersion("sqlite")));
}

private void statusMeilisearchConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "meilisearch %s\n", labelMeilisearch(ctx.environment.meilisearch()));
}

private void statusRedisConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "redis %s\n", labelRedis(ctx.environment.redis()));
}

private void statusRabbitMQConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "rabbitmq %s\n", labelRabbitMQ(ctx.environment.rabbitMQ()));
}

private void statusTraefikConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "traefik %s\n", labelTraefik(ctx.environment.traefik()));
}

private void statusPHPMyAdminConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "phpmyadmin %s\n", labelPHPMyAdmin(ctx.environment.phpMyAdmin()));
}

private void statusDatabaseConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "database %s\n", labelDatabase(ctx.environment.database));
}

private void statusMailpitConfigHook(StatusHookContext ctx) @trusted
{
    writeOutf(ctx.stdout, "mailpit %s\n", labelMailpit(ctx.environment.mailpit()));
}

private void statusWebserverRuntimeHook(StatusHookContext ctx) @trusted
{
    auto webServerState = loadWebServerState(ctx.store.rootDir, ctx.environment.name);
    if (webServerState is null)
    {
        writeOutln(ctx.stdout, "webserver stopped");
        return;
    }

    writeOutf(ctx.stdout, "webserver running %s\n", serveStateURL(*webServerState));
}

private void statusDatabaseRuntimeHook(StatusHookContext ctx) @trusted
{
    if (ctx.environment.database is null || ctx.environment.database.engine.strip().length == 0)
    {
        writeOutln(ctx.stdout, "database-server unset");
        return;
    }

    auto liveDatabaseState = loadLiveManagedDatabaseState(
        ctx.store.rootDir,
        ctx.environment.name,
        (string addr) => callPingDatabaseAddress(addr)
    );
    if (liveDatabaseState is null)
    {
        writeOutln(ctx.stdout, "database-server stopped");
        return;
    }

    writeOutf(
        ctx.stdout,
        "database-server running %s:%s@%d\n",
        liveDatabaseState.engine,
        liveDatabaseState.version_,
        liveDatabaseState.port
    );
}

private void statusPHPMyAdminRuntimeHook(StatusHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("phpmyadmin").strip().length == 0)
    {
        writeOutln(ctx.stdout, "phpmyadmin-server unset");
        return;
    }

    auto livePHPMyAdminState = loadLivePHPMyAdminState(ctx.store.rootDir, ctx.environment.name);
    if (livePHPMyAdminState is null)
    {
        writeOutln(ctx.stdout, "phpmyadmin-server stopped");
        return;
    }

    writeOutf(ctx.stdout, "phpmyadmin-server running %s\n", serveStateURL(*livePHPMyAdminState));
}

private void statusMeilisearchRuntimeHook(StatusHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("meilisearch").strip().length == 0)
    {
        writeOutln(ctx.stdout, "meilisearch-server unset");
        return;
    }

    auto liveMeilisearchState = loadLiveMeilisearchState(ctx.store.rootDir, ctx.environment.name);
    if (liveMeilisearchState is null)
    {
        writeOutln(ctx.stdout, "meilisearch-server stopped");
        return;
    }

    writeOutf(ctx.stdout, "meilisearch-server running %s\n", meilisearchURL(*liveMeilisearchState));
}

private void statusRedisRuntimeHook(StatusHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("redis").strip().length == 0)
    {
        writeOutln(ctx.stdout, "redis-server unset");
        return;
    }

    auto liveRedisState = loadLiveRedisState(ctx.store.rootDir, ctx.environment.name);
    if (liveRedisState is null)
    {
        writeOutln(ctx.stdout, "redis-server stopped");
        return;
    }

    writeOutf(ctx.stdout, "redis-server running %s\n", redisURL(*liveRedisState));
}

private void statusRabbitMQRuntimeHook(StatusHookContext ctx) @trusted
{
    auto rabbitMQ = ctx.environment.rabbitMQ();
    if (rabbitMQ is null || rabbitMQ.version_.strip().length == 0)
    {
        writeOutln(ctx.stdout, "rabbitmq-server unset");
        return;
    }

    auto state = loadLiveRabbitMQState(ctx.store.rootDir, ctx.environment.name);
    if (state is null)
    {
        writeOutln(ctx.stdout, "rabbitmq-server stopped");
        return;
    }

    writeOutf(
        ctx.stdout,
        "rabbitmq-server running amqp=%s management=%s\n",
        rabbitMQURLForConfig(rabbitMQ),
        rabbitMQManagementURLForConfig(rabbitMQ)
    );
}

private void statusTraefikRuntimeHook(StatusHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("traefik").strip().length == 0)
    {
        writeOutln(ctx.stdout, "traefik-server unset");
        return;
    }

    auto liveTraefikState = loadLiveTraefikState(ctx.store.rootDir, ctx.environment.name);
    if (liveTraefikState is null)
    {
        writeOutln(ctx.stdout, "traefik-server stopped");
        return;
    }

    writeOutf(ctx.stdout, "traefik-server running %s\n", traefikURL(*liveTraefikState));
}

private void statusMailpitRuntimeHook(StatusHookContext ctx) @trusted
{
    if (ctx.environment.toolVersion("mailpit").strip().length == 0)
    {
        writeOutln(ctx.stdout, "mailpit-server unset");
        return;
    }

    auto liveMailpitState = loadLiveMailpitState(ctx.store.rootDir, ctx.environment.name);
    if (liveMailpitState is null)
    {
        writeOutln(ctx.stdout, "mailpit-server stopped");
        return;
    }

    writeOutf(
        ctx.stdout,
        "mailpit-server running smtp=%d ui=%s\n",
        liveMailpitState.smtpPort,
        mailpitUIURL(*liveMailpitState)
    );
}

unittest
{
    import std.algorithm : map;
    import std.array : array;

    auto registry = defaultCLIHookRegistry();

    // TestDefaultCLIHookRegistryLifecycleOrder
    assert(registry.startServicesHooks.map!(h => h.id).array == [
        "database", "mailpit", "meilisearch", "redis", "rabbitmq", "traefik", "phpmyadmin",
    ]);
    assert(registry.webservers.map!(h => h.id).array == [
        "nginx", "apache", "frankenphp", "php",
    ]);
    assert(registry.stopHooks.map!(h => h.id).array == [
        "webserver", "workers", "phpmyadmin", "traefik", "redis", "rabbitmq", "meilisearch", "database", "mailpit",
    ]);
    assert(registry.runtimeStatusHooks.map!(h => h.id).array == [
        "webserver", "workers", "phpmyadmin", "meilisearch", "redis", "rabbitmq", "traefik", "database", "mailpit",
    ]);

    // TestDefaultCLIHookRegistryConfigStatusOrder
    assert(registry.configStatusHooks.map!(h => h.id).array == [
        "php", "composer", "pie", "nodejs", "mago", "nginx", "apache", "frankenphp", "roadrunner", "sqlite",
        "meilisearch", "redis", "rabbitmq", "traefik", "phpmyadmin", "database", "mailpit", "workers",
    ]);
}
