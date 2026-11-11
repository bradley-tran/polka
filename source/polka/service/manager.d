module polka.service.manager;

import std.string : strip;

import polka.config;
import polka.service.context;
import polka.service.database;
import polka.service.mailpit;
import polka.service.meilisearch;
import polka.service.phpmyadmin;
import polka.service.phpmyadmin_storage;
import polka.service.rabbitmq;
import polka.service.redis;
import polka.service.traefik;
import polka.service.workers;

/// Coordinates ordered startup and shutdown across all environment-managed services.
struct ServiceManager
{
    /// Starts all services configured in `ctx.environment` in dependency order:
    /// database -> mailpit -> meilisearch -> redis -> rabbitmq -> traefik -> phpmyadmin -> workers.
    StartResult start(ServiceContext ctx, RuntimeHooks hooks = RuntimeHooks.init) const @trusted
    {
        auto dbCfg = ctx.environment.database;
        if (dbCfg !is null && dbCfg.engine.strip().length > 0)
        {
            if (!ctx.skipMissingTool("database", dbCfg.engine))
            {
                auto resolved = ResolvedDatabaseEnvironment(ctx.environment, dbCfg);
                bool unused;
                ensureManagedDatabaseStarted(ctx, resolved, hooks.database, unused);
            }
        }

        auto mailCfg = ctx.environment.mailpit();
        if (mailCfg !is null && mailCfg.version_.strip().length > 0)
        {
            if (!ctx.skipMissingTool("mailpit", toolMailpit))
            {
                bool unused;
                ensureManagedMailpitStarted(ctx, hooks.mailpit, unused);
            }
        }

        StartResult result;
        auto meiliCfg = ctx.environment.meilisearch();
        if (meiliCfg !is null && meiliCfg.version_.strip().length > 0)
        {
            if (!ctx.skipMissingTool("meilisearch", toolMeilisearch))
            {
                bool alreadyStarted = false;
                auto state = ensureManagedMeilisearchStarted(ctx, hooks.meilisearch, alreadyStarted);
                result.meilisearch = new MeilisearchStartSummary(state, alreadyStarted);
            }
        }

        auto redisCfg = ctx.environment.redis();
        if (redisCfg !is null && redisCfg.version_.strip().length > 0)
        {
            if (!ctx.skipMissingTool("redis", toolRedis))
            {
                bool alreadyStarted = false;
                auto state = ensureManagedRedisStarted(ctx, hooks.redis, alreadyStarted);
                result.redis = new RedisStartSummary(state, alreadyStarted);
            }
        }

        auto rmqCfg = ctx.environment.rabbitMQ();
        if (rmqCfg !is null && rmqCfg.version_.strip().length > 0)
        {
            if (!ctx.skipMissingTool("rabbitmq", toolRabbitMQ))
            {
                bool alreadyStarted = false;
                auto state = ensureManagedRabbitMQStarted(ctx, hooks.rabbitMQ, alreadyStarted);
                result.rabbitMQ = new RabbitMQStartSummary(state, alreadyStarted);
            }
        }

        auto traefikCfg = ctx.environment.traefik();
        if (traefikCfg !is null && traefikCfg.version_.strip().length > 0)
        {
            if (!ctx.skipMissingTool("traefik", toolTraefik))
            {
                bool alreadyStarted = false;
                auto state = ensureManagedTraefikStarted(ctx, hooks.traefik, alreadyStarted);
                result.traefik = new TraefikStartSummary(state, alreadyStarted);
            }
        }

        auto pmaCfg = ctx.environment.phpMyAdmin();
        if (pmaCfg !is null && pmaCfg.version_.strip().length > 0)
        {
            if (!ctx.skipMissingTool("phpmyadmin", toolPHPMyAdmin))
            {
                if (dbCfg is null || dbCfg.engine.strip().length == 0 || ctx.hasTool(dbCfg.engine))
                {
                    auto ensureStorage = hooks.ensurePHPMyAdminStorage;
                    if (ensureStorage is null)
                        ensureStorage = (ServiceContext c, Environment env, DatabaseRuntimeHooks dbHooks) {
                            ensurePHPMyAdminStorageConfigured(c, env, dbHooks);
                        };
                    ensureStorage(ctx, ctx.environment, hooks.database);
                }

                bool alreadyStarted = false;
                auto state = ensureManagedPHPMyAdminStarted(ctx, hooks.phpMyAdmin, alreadyStarted);
                result.phpMyAdmin = new PHPMyAdminStartResult(state, alreadyStarted);
            }
        }

        // Workers start last so backing services are already accepting connections.
        // Worker errors warn rather than aborting `serve` or other services.
        if (ctx.environment.workers.length > 0)
        {
            try
            {
                bool alreadyStarted = false;
                auto state = ensureManagedWorkersStarted(ctx, hooks.workers, alreadyStarted);
                result.workers = new WorkersStartSummary(state, alreadyStarted);
            }
            catch (Exception e)
            {
                ctx.warnf(`Skipping workers for environment "%s": %s.` ~ "\n", ctx.environment.name, e.msg);
            }
        }

        return result;
    }

    /// Stops all live services for `ctx.environment` in reverse dependency order:
    /// workers -> phpmyadmin -> meilisearch -> redis -> rabbitmq -> traefik -> database -> mailpit.
    StopResult stop(ServiceContext ctx, RuntimeHooks hooks = RuntimeHooks.init) const @trusted
    {
        StopResult result;

        // Workers stop first so consumers exit before their backing datastores shut down.
        try
        {
            bool alreadyStopped = false;
            auto state = stopManagedWorkers(ctx, hooks.workers, alreadyStopped);
            result.workers = new StopWorkersResult(state, alreadyStopped);
        }
        catch (Exception e)
        {
            ctx.warnf(`Failed to stop workers for environment "%s": %s.` ~ "\n", ctx.environment.name, e.msg);
        }

        {
            bool alreadyStopped = false;
            auto state = stopManagedPHPMyAdmin(ctx, hooks.phpMyAdmin, alreadyStopped);
            result.phpMyAdmin = new StopServeResult(state, alreadyStopped);
        }

        {
            bool alreadyStopped = false;
            auto state = stopManagedMeilisearch(ctx, hooks.meilisearch, alreadyStopped);
            result.meilisearch = new StopMeilisearchResult(state, alreadyStopped);
        }

        {
            bool alreadyStopped = false;
            auto state = stopManagedRedis(ctx, hooks.redis, alreadyStopped);
            result.redis = new StopRedisResult(state, alreadyStopped);
        }

        {
            bool alreadyStopped = false;
            auto state = stopManagedRabbitMQ(ctx, hooks.rabbitMQ, alreadyStopped);
            result.rabbitMQ = new StopRabbitMQResult(state, alreadyStopped);
        }

        {
            bool alreadyStopped = false;
            auto state = stopManagedTraefik(ctx, hooks.traefik, alreadyStopped);
            result.traefik = new StopTraefikResult(state, alreadyStopped);
        }

        {
            bool alreadyStopped = false;
            auto state = stopManagedDatabaseForEnvironment(ctx, hooks.database, alreadyStopped);
            result.database = new StopDatabaseResult(state, alreadyStopped);
        }

        {
            bool alreadyStopped = false;
            auto state = stopManagedMailpit(ctx, hooks.mailpit, alreadyStopped);
            result.mailpit = new StopMailpitResult(state, alreadyStopped);
        }

        return result;
    }

    /// Emits warnings for configured runtime services lacking registered tool plugins
    /// or unsupported pairings (e.g. phpMyAdmin with PostgreSQL).
    void warnMissingRuntimeTools(ServiceContext ctx) const @trusted
    {
        warnPHPMyAdminPostgreSQL(ctx, ctx.environment);

        auto pmaCfg = ctx.environment.phpMyAdmin();
        if (pmaCfg !is null && pmaCfg.version_.strip().length > 0)
            ctx.skipMissingTool("phpmyadmin", toolPHPMyAdmin);

        auto dbCfg = ctx.environment.database;
        if (dbCfg !is null && dbCfg.engine.strip().length > 0)
            ctx.skipMissingTool("database", dbCfg.engine);

        auto mailCfg = ctx.environment.mailpit();
        if (mailCfg !is null && mailCfg.version_.strip().length > 0)
            ctx.skipMissingTool("mailpit", toolMailpit);

        auto meiliCfg = ctx.environment.meilisearch();
        if (meiliCfg !is null && meiliCfg.version_.strip().length > 0)
            ctx.skipMissingTool("meilisearch", toolMeilisearch);

        auto redisCfg = ctx.environment.redis();
        if (redisCfg !is null && redisCfg.version_.strip().length > 0)
            ctx.skipMissingTool("redis", toolRedis);

        auto rmqCfg = ctx.environment.rabbitMQ();
        if (rmqCfg !is null && rmqCfg.version_.strip().length > 0)
            ctx.skipMissingTool("rabbitmq", toolRabbitMQ);

        auto traefikCfg = ctx.environment.traefik();
        if (traefikCfg !is null && traefikCfg.version_.strip().length > 0)
            ctx.skipMissingTool("traefik", toolTraefik);
    }
}

alias Manager = ServiceManager;

/// Returns a default `ServiceManager` instance.
ServiceManager defaultServiceManager() @safe pure nothrow @nogc
{
    return ServiceManager.init;
}

alias defaultManager = defaultServiceManager;

/// Aggregate runtime hook bundle passed to `ServiceManager.start` / `stop`.
struct RuntimeHooks
{
    DatabaseRuntimeHooks database;
    MailpitRuntimeHooks mailpit;
    MeilisearchRuntimeHooks meilisearch;
    RedisRuntimeHooks redis;
    RabbitMQRuntimeHooks rabbitMQ;
    TraefikRuntimeHooks traefik;
    PHPMyAdminRuntimeHooks phpMyAdmin;
    WorkersRuntimeHooks workers;
    void delegate(ServiceContext, Environment, DatabaseRuntimeHooks) ensurePHPMyAdminStorage;
}

struct StartResult
{
    MeilisearchStartSummary* meilisearch;
    RedisStartSummary* redis;
    RabbitMQStartSummary* rabbitMQ;
    TraefikStartSummary* traefik;
    PHPMyAdminStartResult* phpMyAdmin;
    WorkersStartSummary* workers;
}

struct MeilisearchStartSummary
{
    MeilisearchRuntimeState state;
    bool alreadyStarted = false;
}

struct RedisStartSummary
{
    RedisRuntimeState state;
    bool alreadyStarted = false;
}

struct RabbitMQStartSummary
{
    RabbitMQRuntimeState state;
    bool alreadyStarted = false;
}

struct TraefikStartSummary
{
    TraefikRuntimeState state;
    bool alreadyStarted = false;
}

struct PHPMyAdminStartResult
{
    ServeRuntimeState state;
    bool alreadyStarted = false;
}

struct WorkersStartSummary
{
    WorkersRuntimeState state;
    bool alreadyStarted = false;
}

struct StopResult
{
    WorkersStopResult* workers;
    StopServeResult* phpMyAdmin;
    StopMeilisearchResult* meilisearch;
    StopRedisResult* redis;
    StopRabbitMQResult* rabbitMQ;
    StopTraefikResult* traefik;
    StopDatabaseResult* database;
    StopMailpitResult* mailpit;
}

alias WorkersStopResult = StopWorkersResult;

struct StopServeResult
{
    ServeRuntimeState state;
    bool alreadyStopped = false;
}

struct StopDatabaseResult
{
    ManagedDatabaseRuntimeState state;
    bool alreadyStopped = false;
}

struct StopMailpitResult
{
    MailpitRuntimeState state;
    bool alreadyStopped = false;
}

struct StopMeilisearchResult
{
    MeilisearchRuntimeState state;
    bool alreadyStopped = false;
}

struct StopRedisResult
{
    RedisRuntimeState state;
    bool alreadyStopped = false;
}

struct StopRabbitMQResult
{
    RabbitMQRuntimeState state;
    bool alreadyStopped = false;
}

struct StopTraefikResult
{
    TraefikRuntimeState state;
    bool alreadyStopped = false;
}

struct StopWorkersResult
{
    WorkersRuntimeState state;
    bool alreadyStopped = false;
}

unittest
{
    import std.array : appender;
    import std.algorithm : canFind;
    import std.datetime : Clock, UTC;
    import std.file : exists;
    import std.format : format;
    import std.path : buildPath;
    import std.process : thisProcessID;
    import polka.service.mailpit_config : mailpitAddress;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;
    import polka.tools : newRegistry;

    static ServiceContext makeManagerContext(string tempRoot, Environment environment, out string delegate() warningsOut)
    {
        auto rootDir = buildPath(tempRoot, ".polka");
        auto warnings = appender!string();
        warningsOut = () => warnings.data;

        ServiceContext ctx;
        ctx.projectDir = tempRoot;
        ctx.rootDir = rootDir;
        ctx.envsDir = buildPath(rootDir, "envs");
        ctx.environment = environment;
        ctx.registry = newRegistry(); // empty registry so configured tools are unregistered
        ctx.warnFn = (string msg) { warnings.put(msg.strip()); };
        return ctx;
    }

    // TestManagerStartSkipsConfiguredMailpitWithoutMatchingTool
    {
        auto tmp = createTempDir("polka-mgr-mailpit-");
        scope (exit) rmdirRecurseQuiet(tmp);
        Environment env;
        env.name = "demo";
        env.tools = ["mailpit": "1.30"];
        string delegate() warnings;
        auto ctx = makeManagerContext(tmp, env, warnings);

        bool started = false;
        RuntimeHooks hooks;
        hooks.mailpit.startServer = (MailpitServerSpec) {
            started = true;
            return MailpitStartResult.init;
        };
        defaultServiceManager().start(ctx, hooks);
        assert(!started);
        assert(warnings().canFind(`matching tool "mailpit" is not registered`));
    }

    // TestManagerStartSkipsConfiguredPHPMyAdminWithoutMatchingTool
    {
        auto tmp = createTempDir("polka-mgr-pma-");
        scope (exit) rmdirRecurseQuiet(tmp);
        Environment env;
        env.name = "demo";
        env.tools = ["phpmyadmin": "5.2"];
        string delegate() warnings;
        auto ctx = makeManagerContext(tmp, env, warnings);

        bool started = false;
        RuntimeHooks hooks;
        hooks.phpMyAdmin.startServe = (Environment, Endpoint, AppLayout, string) {
            started = true;
            return ServeRuntimeState.init;
        };
        auto result = defaultServiceManager().start(ctx, hooks);
        assert(result.phpMyAdmin is null && !started);
        assert(warnings().canFind(`matching tool "phpmyadmin" is not registered`));
    }

    // TestManagerStartSkipsConfiguredDatabaseWithoutMatchingTool
    {
        auto tmp = createTempDir("polka-mgr-db-");
        scope (exit) rmdirRecurseQuiet(tmp);
        Environment env;
        env.name = "demo";
        env.database = new DatabaseConfig("mysql", "8.4");
        string delegate() warnings;
        auto ctx = makeManagerContext(tmp, env, warnings);

        bool started = false;
        RuntimeHooks hooks;
        hooks.database.startServer = (ManagedDatabaseServerSpec) {
            started = true;
            return ManagedDatabaseStartResult.init;
        };
        defaultServiceManager().start(ctx, hooks);
        assert(!started);
        assert(warnings().canFind(`matching tool "mysql" is not registered`));
    }

    // TestManagerStopCleansExistingStateWithoutMatchingTool
    {
        auto tmp = createTempDir("polka-mgr-stop-");
        scope (exit) rmdirRecurseQuiet(tmp);
        Environment env;
        env.name = "demo";
        string delegate() warnings;
        auto ctx = makeManagerContext(tmp, env, warnings);

        MailpitRuntimeState st;
        st.environmentName = "demo";
        st.version_ = "1.30";
        st.smtpPort = 1125;
        st.uiPort = 8125;
        st.pid = thisProcessID;
        st.startedAt = Clock.currTime(UTC());
        writeMailpitState(mailpitStatePath(ctx.rootDir, "demo"), st);

        bool stopped = false;
        RuntimeHooks hooks;
        hooks.mailpit.pingAddress = (string address) => address == mailpitAddress(1125) || address == mailpitAddress(8125);
        hooks.mailpit.stopServer = (MailpitRuntimeState) { stopped = true; };

        auto result = defaultServiceManager().stop(ctx, hooks);
        assert(stopped);
        assert(result.mailpit !is null && !result.mailpit.alreadyStopped);
        assert(!exists(mailpitStatePath(ctx.rootDir, "demo")));
    }

    // TestManagerStartsWorkersEvenWhenPHPMyAdminSkipped
    {
        auto tmp = createTempDir("polka-mgr-wrk-start-");
        scope (exit) rmdirRecurseQuiet(tmp);
        Environment env;
        env.name = "demo";
        env.tools = ["phpmyadmin": "5.2"];
        env.workers = ["queue": WorkerConfig("php artisan queue:work")];
        string delegate() warnings;
        auto ctx = makeManagerContext(tmp, env, warnings);

        int nextPID = 200;
        bool[int] live;
        RuntimeHooks hooks;
        hooks.workers.startWorker = (WorkerSpec spec, int replica) {
            nextPID++;
            live[nextPID] = true;
            return WorkerStartResult(nextPID, workerLogPath(spec.logDir, spec.name, replica));
        };
        hooks.workers.pidIsLive = (int pid) => pid in live ? live[pid] : false;
        hooks.workers.buildEnv = (ServiceContext) => ["BASE=1"];

        auto result = defaultServiceManager().start(ctx, hooks);
        assert(warnings().canFind(`matching tool "phpmyadmin" is not registered`));
        assert(result.workers !is null && !result.workers.alreadyStarted && result.workers.state.processes.length == 1);
    }

    // TestManagerStopStopsWorkersFirst
    {
        auto tmp = createTempDir("polka-mgr-wrk-order-");
        scope (exit) rmdirRecurseQuiet(tmp);
        Environment env;
        env.name = "demo";
        env.workers = ["queue": WorkerConfig("php artisan queue:work")];
        string delegate() warnings;
        auto ctx = makeManagerContext(tmp, env, warnings);

        bool[int] live;
        WorkersRuntimeHooks wrkHooks;
        wrkHooks.startWorker = (WorkerSpec spec, int replica) {
            live[501] = true;
            return WorkerStartResult(501, workerLogPath(spec.logDir, spec.name, replica));
        };
        wrkHooks.stopWorker = (WorkerProcessState st) { live.remove(st.pid); };
        wrkHooks.pidIsLive = (int pid) => pid in live ? live[pid] : false;
        wrkHooks.buildEnv = (ServiceContext) => ["BASE=1"];

        bool alreadyStarted = false;
        ensureManagedWorkersStarted(ctx, wrkHooks, alreadyStarted);

        MailpitRuntimeState mp;
        mp.environmentName = "demo";
        mp.version_ = "1.30";
        mp.smtpPort = 1125;
        mp.uiPort = 8125;
        mp.pid = thisProcessID;
        mp.startedAt = Clock.currTime(UTC());
        writeMailpitState(mailpitStatePath(ctx.rootDir, "demo"), mp);

        string[] order;
        RuntimeHooks hooks;
        hooks.workers = wrkHooks;
        hooks.workers.stopWorker = (WorkerProcessState st) {
            order ~= "workers";
            live.remove(st.pid);
        };
        hooks.mailpit.pingAddress = (string address) => address == mailpitAddress(1125) || address == mailpitAddress(8125);
        hooks.mailpit.stopServer = (MailpitRuntimeState) { order ~= "mailpit"; };

        auto result = defaultServiceManager().stop(ctx, hooks);
        assert(result.workers !is null && !result.workers.alreadyStopped);
        assert(order == ["workers", "mailpit"]);
    }
}
