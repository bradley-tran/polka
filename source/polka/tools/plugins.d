module polka.tools.plugins;

import polka.config;
import polka.tools.apache;
import polka.tools.frankenphp;
import polka.tools.ids;
import polka.tools.manifest;
import polka.tools.php;
import polka.tools.php_devel;
import polka.tools.phpmyadmin_config;
import polka.tools.postgresql;
import polka.tools.rabbitmq;
import polka.tools.registry;
import polka.tools.sqlite;
import polka.tools.types;

/// Returns the internal Windows `php-sdk` build tooling plugin.
ToolPlugin phpSDKPlugin() @safe { return newManifestPlugin(PHPSDK); }

/// Returns the Composer package manager plugin.
ToolPlugin composerPlugin() @safe { return newManifestPlugin(Composer); }

/// Returns the internal PIE PHP extension installer plugin.
ToolPlugin piePlugin() @safe { return newManifestPlugin(PIE); }

/// Returns the Node.js runtime plugin exposing `node`, `npm`, `npx`, and `yarn`.
ToolPlugin nodeJSPlugin() @safe { return newManifestPlugin(NodeJS); }

/// Returns the Mago PHP static analysis and formatting toolchain plugin.
ToolPlugin magoPlugin() @safe { return newManifestPlugin(Mago); }

/// Returns the Nginx web server plugin.
ToolPlugin nginxPlugin() @safe { return newManifestPlugin(Nginx); }

/// Returns the Mailpit SMTP catch-all mail server plugin.
ToolPlugin mailpitPlugin() @safe { return newManifestPlugin(Mailpit); }

/// Returns the Meilisearch search engine plugin.
ToolPlugin meilisearchPlugin() @safe { return newManifestPlugin(Meilisearch); }

/// Returns the Redis in-memory data store plugin.
ToolPlugin redisPlugin() @safe { return newManifestPlugin(Redis); }

/// Returns the RoadRunner PHP application server plugin (managed tool only; not a `server.type`).
ToolPlugin roadRunnerPlugin() @safe { return newManifestPlugin(RoadRunner); }

/// Returns the Traefik reverse proxy plugin.
ToolPlugin traefikPlugin() @safe { return newManifestPlugin(Traefik); }

/// Returns the MySQL database server plugin.
ToolPlugin mysqlPlugin() @safe { return newManifestPlugin(MySQL); }

/// Returns the MariaDB database server plugin.
ToolPlugin mariaDBPlugin() @safe { return newManifestPlugin(MariaDB); }

/// Returns Polka's default built-in plugins in canonical install/display order.
ToolPlugin[] defaultPlugins() @safe
{
    return [
        phpPlugin(),
        phpZTSPlugin(),
        phpDevelPlugin(),
        phpSDKPlugin(),
        frankenPHPPlugin(),
        composerPlugin(),
        piePlugin(),
        nodeJSPlugin(),
        magoPlugin(),
        nginxPlugin(),
        apachePlugin(),
        mailpitPlugin(),
        meilisearchPlugin(),
        redisPlugin(),
        erlangPlugin(),
        rabbitMQPlugin(),
        traefikPlugin(),
        roadRunnerPlugin(),
        phpMyAdminPlugin(),
        mysqlPlugin(),
        mariaDBPlugin(),
        postgreSQLPlugin(),
        sqlitePlugin(),
    ];
}

/// Constructs a `Registry` pre-populated with all built-in Polka plugins.
Registry newDefaultRegistry() @trusted
{
    return new Registry(defaultPlugins());
}

unittest
{
    import std.algorithm : canFind;
    import std.file : exists, mkdirRecurse, write;
    import std.path : buildPath;
    import polka.support.fs : createTempDir, currentOS, getFileMode, octal, rmdirRecurseQuiet, setFileMode;
    import polka.support.yaml : YamlValue;
    import polka.tools.download_io : resolveManifestDownloadAsset;

    static string redisServerBinary()
    {
        return currentOS() == "windows" ? "redis-server.exe" : "redis-server";
    }

    static string redisCLIBinary()
    {
        return currentOS() == "windows" ? "redis-cli.exe" : "redis-cli";
    }

    // TestDefaultRegistryInstallRequestsUseConfiguredToolOrder
    {
        auto registry = newDefaultRegistry();
        Environment env;
        env.https = true;
        env.tools = [
            PHP: "8.4",
            FrankenPHP: "1.12",
            Composer: "2.8",
            PIE: "1.4",
            NodeJS: "24",
            Mago: "1.27",
            Nginx: "1.30",
            Apache: "2.4",
            PHPMyAdmin: "5.2",
            Mailpit: "1.30",
            Meilisearch: "1.48",
            Redis: "8.8.0",
            MySQL: "8.4",
            MariaDB: "11.8",
            PostgreSQL: "17",
            SQLite: "3.53",
        ];
        env.settings = [
            PHPMyAdmin: ["port": YamlValue.fromInt(8081)],
            Meilisearch: ["port": YamlValue.fromInt(7701), "master-key": YamlValue.fromString("dev-key")],
            Redis: ["port": YamlValue.fromInt(6380), "password": YamlValue.fromString("dev-password")],
        ];
        env.database = new DatabaseConfig(MariaDB, "11.8");

        string[] got;
        foreach (req; registry.installRequests(env))
            got ~= req.tool ~ ":" ~ req.version_;
        assert(got == [
            "php:8.4",
            "frankenphp:1.12",
            "composer:2.8",
            "nodejs:24",
            "mago:1.27",
            "nginx:1.30",
            "apache:2.4",
            "mailpit:1.30",
            "meilisearch:1.48",
            "redis:8.8.0",
            "phpmyadmin:5.2",
            "mysql:8.4",
            "mariadb:11.8",
            "postgresql:17",
            "sqlite:3.53",
        ]);
    }

    // TestRegistryRejectsDuplicatePlugins
    {
        auto plugins = defaultPlugins();
        bool threw = false;
        try { new Registry(plugins[0], plugins[0]); }
        catch (Exception) { threw = true; }
        assert(threw);
    }

    // TestDefaultRegistryKeepsNodeJSConfigOnly
    {
        auto registry = newDefaultRegistry();
        auto nodeReq = registry.resolveDispatchRequest(Node);
        assert(nodeReq.configTool == NodeJS && nodeReq.executable == Node);
        auto yarnReq = registry.resolveDispatchRequest(Yarn);
        assert(yarnReq.configTool == NodeJS && yarnReq.executable == Yarn);

        bool threwNodeJS = false;
        try { registry.resolveDispatchRequest(NodeJS); }
        catch (Exception) { threwNodeJS = true; }
        assert(threwNodeJS);

        Environment env;
        env.tools = [NodeJS: "24"];
        assert(registry.activeCommandNames(&env) == [Node, NPM, NPX, Yarn]);
        assert(registry.cleanupCommandNames().canFind(NodeJS));
    }

    // TestDefaultRegistryResolvesApacheDispatchCommands
    {
        auto registry = newDefaultRegistry();
        Environment env;
        env.tools = [Apache: "2.4"];
        foreach (cmd; [Apache, "httpd"])
        {
            auto req = registry.resolveDispatchRequestForEnvironment(cmd, env);
            assert(req.configTool == Apache && req.executable == cmd);
        }
    }

    // TestDefaultRegistrySelectsConfiguredPHPProvider
    {
        auto registry = newDefaultRegistry();
        struct Case { string[string] tools; string wantTool; }
        foreach (tc; [
            Case([PHP: "8.4"], PHP),
            Case([PHPZTS: "8.4"], PHPZTS),
            Case([FrankenPHP: "1.12"], FrankenPHP),
            Case([PHP: "8.4", FrankenPHP: "1.12"], PHP),
            Case([PHPZTS: "8.4", FrankenPHP: "1.12"], PHPZTS),
        ])
        {
            Environment env;
            env.tools = tc.tools.dup;
            auto req = registry.resolveDispatchRequestForEnvironment(PHP, env);
            assert(req.configTool == tc.wantTool && req.executable == PHP);
        }
    }

    // TestDefaultRegistryRejectsMultiplePHPRuntimes
    {
        auto registry = newDefaultRegistry();
        Environment env;
        env.tools = [PHP: "8.4", PHPZTS: "8.4"];
        bool threw = false;
        try { registry.validateEnvironment(env); }
        catch (Exception e) { threw = e.msg.canFind("mutually exclusive"); }
        assert(threw);
    }

    // TestDefaultRegistryValidatesPHPMyAdmin, Meilisearch, Redis, OPcache Configs
    {
        auto registry = newDefaultRegistry();

        void assertInvalidEnv(Environment env, string desc)
        {
            bool threw = false;
            try { registry.validateEnvironment(env); }
            catch (Exception) { threw = true; }
            assert(threw, desc);
        }

        Environment pmaBadPort;
        pmaBadPort.tools = [PHPMyAdmin: "5.2"];
        pmaBadPort.settings = [PHPMyAdmin: ["port": YamlValue.fromInt(70000)]];
        assertInvalidEnv(pmaBadPort, "phpmyadmin invalid port");

        Environment pmaNoVer;
        pmaNoVer.settings = [PHPMyAdmin: ["port": YamlValue.fromInt(8081)]];
        assertInvalidEnv(pmaNoVer, "phpmyadmin without version");

        Environment meiliBadPort;
        meiliBadPort.tools = [Meilisearch: "1.48"];
        meiliBadPort.settings = [Meilisearch: ["port": YamlValue.fromInt(70000)]];
        assertInvalidEnv(meiliBadPort, "meilisearch invalid port");

        Environment meiliNoVer;
        meiliNoVer.settings = [Meilisearch: ["port": YamlValue.fromInt(7700)]];
        assertInvalidEnv(meiliNoVer, "meilisearch without version");

        Environment meiliEnv;
        meiliEnv.tools = [Meilisearch: "1.48"];
        assert(registry.activeCommandNames(&meiliEnv) == [Meilisearch]);

        Environment redisBadPort;
        redisBadPort.tools = [Redis: "8.8.0"];
        redisBadPort.settings = [Redis: ["port": YamlValue.fromInt(70000)]];
        assertInvalidEnv(redisBadPort, "redis invalid port");

        Environment redisNoVer;
        redisNoVer.settings = [Redis: ["port": YamlValue.fromInt(6379)]];
        assertInvalidEnv(redisNoVer, "redis without version");

        Environment redisEnv;
        redisEnv.tools = [Redis: "8.8.0"];
        assert(registry.activeCommandNames(&redisEnv) == ["redis-server", "redis-cli"]);

        Environment badPreset;
        badPreset.tools = [PHP: "8.4"];
        badPreset.opcachePreset = "staging";
        assertInvalidEnv(badPreset, "opcache invalid preset");

        Environment badDirective;
        badDirective.tools = [PHP: "8.4"];
        badDirective.opcacheConfig = ["zend_extension": "opcache"];
        assertInvalidEnv(badDirective, "opcache invalid directive");
    }

    // TestDefaultRegistrySettingsSchemasAndExclusivePeers
    {
        auto registry = newDefaultRegistry();
        SettingType[string] schema;
        string[] owners;

        assert(registry.settingsSchema(PHP, schema, owners));
        assert(schema == ["extension-sdk": SettingType.bool_]);
        assert(owners == [PHP, PHPZTS]);

        assert(registry.settingsSchema(Redis, schema, owners));
        assert(schema == ["port": SettingType.port, "password": SettingType.string_]);
        assert(owners == [Redis]);

        assert(!registry.settingsSchema(Composer, schema, owners));
        assert(registry.exclusivePeers(PHP) == [PHPZTS]);
        assert(registry.exclusivePeers(PHPZTS) == [PHP]);
    }

    // TestPIEManifestIsInternalOnly & TestRegistryInternalToolAccessors
    {
        auto registry = newDefaultRegistry();
        ToolPlugin pie;
        assert(registry.plugin(PIE, pie));
        assert(pie.dispatchCommands().length == 0);

        Environment pieEnv;
        pieEnv.tools = [PIE: "1.4"];
        assert(pie.version_(pieEnv).length == 0);
        assert(pie.activeCommands(pieEnv).length == 0);

        bool sawManaged = false;
        try { pie.validate(pieEnv); }
        catch (Exception e) { sawManaged = e.msg.canFind("managed internally"); }
        assert(sawManaged);
        pie.validate(Environment.init);

        assert(registry.internalOnly(PIE));
        assert(!registry.internalOnly(Composer));
        assert(!registry.internalOnly("unknown"));

        Environment accessEnv;
        accessEnv.tools = [PIE: "1.4", Composer: "2.9", PHP: "8.5"];
        assert(registry.internalVersion(PIE, accessEnv) == "1.4");
        assert(registry.internalVersion(Composer, accessEnv) == "2.9");
        assert(registry.internalVersion(PHP, accessEnv) == "8.5");
        assert(registry.internalVersion(PIE, Environment.init).length == 0);
    }

    // TestParsePluginManifestRejectsInternalOnlyCommands
    {
        bool threwInternal = false;
        try
        {
            parsePluginManifest("id: demo\ninternal-only: true\ninstall-candidates:\n  all:\n    - bin/demo\ndispatch-commands:\n  - demo\n");
        }
        catch (Exception e) { threwInternal = e.msg.canFind("internal-only"); }
        assert(threwInternal);

        parsePluginManifest("id: demo\ninternal-only: true\ninstall-candidates:\n  all:\n    - bin/demo\n");
    }

    // TestRabbitMQManifestAndDependency & TestInstallRequestLayersRejectCycles
    {
        auto registry = newDefaultRegistry();
        ToolPlugin rmq;
        assert(registry.plugin(RabbitMQ, rmq));
        assert(rmq.dispatchCommands() == [
            "rabbitmq-server", "rabbitmqctl", "rabbitmq-diagnostics",
            "rabbitmq-plugins", "rabbitmq-queues", "rabbitmq-streams", "rabbitmq-upgrade"
        ]);
        auto depProvider = cast(ToolDependencyProvider) rmq;
        assert(depProvider !is null && depProvider.dependencies(Environment.init).length == 0);

        Environment rmqEnv;
        rmqEnv.tools = [RabbitMQ: "4.3"];
        auto layers = registry.installRequestLayers(rmqEnv);
        assert(layers.length == 2 && layers[0].length == 1);
        assert(layers[0][0] == InstallRequest(Erlang, "27"));
        assert(layers[1][0].tool == RabbitMQ);

        class CyclePlugin : ToolPlugin, ToolDependencyProvider
        {
            string idVal;
            InstallRequest[] depsVal;
            this(string i, InstallRequest[] d) { idVal = i; depsVal = d; }
            string id() const { return idVal; }
            string version_(const Environment) const { return ""; }
            bool[string] phpExtensions() const { return null; }
            void validate(const Environment) const {}
            string[] installCandidates(string, string) const { return null; }
            string[] dispatchCommands() const { return null; }
            string[] cleanupCommands() const { return null; }
            string[] activeCommands(const Environment) const { return null; }
            string[] dispatchCandidates(string, string, string) const { return null; }
            LogEntry[] logs() const { return null; }
            void download(DownloadContext) const {}
            void postInstall(InstallContext) const {}
            InstallRequest[] dependencies(const Environment) const { return depsVal.dup; }
        }

        auto cycReg = new Registry(
            new CyclePlugin("a", [InstallRequest("b", "1")]),
            new CyclePlugin("b", [InstallRequest("a", "1")])
        );
        bool sawCycle = false;
        try { cycReg.installRequestLayers(Environment.init, [InstallRequest("a", "1")]); }
        catch (Exception e) { sawCycle = e.msg.canFind("cycle"); }
        assert(sawCycle);
    }

    // TestPostgreSQLPluginUsesPSQLDispatch & InstallCandidates & TestPHPMyAdminAllowsPostgreSQL
    {
        auto registry = newDefaultRegistry();
        auto req = registry.resolveDispatchRequest(PSQL);
        assert(req.configTool == PostgreSQL && req.executable == PSQL);

        Environment pgEnv;
        pgEnv.tools = [PostgreSQL: "17"];
        assert(registry.activeCommandNames(&pgEnv).canFind(PSQL));

        auto candidates = registry.dispatchCandidates("root", PostgreSQL, PSQL, "17");
        assert(candidates.length > 0);
        auto wantSuffix = buildPath("root", PostgreSQL, "17", "bin", "psql");
        bool found = false;
        foreach (c; candidates)
        {
            if (c.canFind(wantSuffix))
                found = true;
        }
        assert(found);

        Environment pmaPg;
        pmaPg.tools = [PostgreSQL: "17", PHPMyAdmin: "5.2"];
        pmaPg.database = new DatabaseConfig(PostgreSQL, "17");
        registry.validateEnvironment(pmaPg);
    }

    // TestRedisPlugin suite
    {
        auto plugin = redisPlugin();
        Environment env;
        env.tools = [Redis: "8.8.0"];
        assert(plugin.version_(env) == "8.8.0");
        assert(plugin.dispatchCommands() == ["redis-server", "redis-cli"]);
        assert(plugin.logs() == [LogEntry("redis.log", LogLevelInfo)]);

        auto m = loadBuiltinManifest(Redis);
        auto win = resolveManifestDownloadAsset(Redis, m.download.assets, "8.8.0", "8.8.0", "8.8.0", "windows", "amd64", null);
        assert(win.fileName == "redis-windows-8.8.0.zip" && win.archiveFormat == ArchiveFormat.zip);
        assert(win.url == "https://github.com/zkteco-home/redis-windows/archive/refs/tags/8.8.0.zip");
        assert(win.checksumAlgorithm == ChecksumAlgorithm.embeddedCommitID);

        bool threwLinux = false;
        try { resolveManifestDownloadAsset(Redis, m.download.assets, "8.8.0", "8.8.0", "8.8.0", "linux", "amd64", null); }
        catch (Exception) { threwLinux = true; }
        assert(threwLinux);

        assert(plugin.installCandidates("root", "8.8.0") == [buildPath("root", "redis", "8.8.0", redisServerBinary())]);
        assert(plugin.dispatchCandidates("root", "redis-cli", "8.8.0") == [buildPath("root", "redis", "8.8.0", redisCLIBinary())]);

        plugin.validate(Environment.init);
        bool threwBadVer = false;
        Environment badVerEnv;
        badVerEnv.tools = [Redis: "bad version"];
        try { plugin.validate(badVerEnv); }
        catch (Exception) { threwBadVer = true; }
        assert(threwBadVer);

        version (Posix)
        {
            auto installDir = createTempDir("polka-redis-exec-");
            scope (exit) rmdirRecurseQuiet(installDir);
            auto serverPath = buildPath(installDir, "redis-server");
            auto cliPath = buildPath(installDir, "redis-cli");
            foreach (p; [serverPath, cliPath])
            {
                write(p, "binary");
                setFileMode(p, octal!644);
            }
            InstallContext ctx;
            ctx.result.targetPath = serverPath;
            plugin.postInstall(ctx);
            foreach (p; [serverPath, cliPath])
                assert((getFileMode(p) & octal!111) != 0);
        }
    }

    // TestRoadRunnerPlugin suite
    {
        auto plugin = roadRunnerPlugin();
        Environment env;
        env.tools = [RoadRunner: "2024.1.5"];
        assert(plugin.version_(env) == "2024.1.5");
        assert(plugin.dispatchCommands() == ["rr"]);
        assert(plugin.logs() == [LogEntry("serve.log", LogLevelDebug)]);

        auto m = loadBuiltinManifest(RoadRunner);
        auto win = resolveManifestDownloadAsset(RoadRunner, m.download.assets, "2024.1.5", "2024.1.5", "v2024.1.5", "windows", "amd64", null);
        assert(win.fileName == "roadrunner-2024.1.5-windows-amd64.zip" && win.archiveFormat == ArchiveFormat.zip);
        auto lin = resolveManifestDownloadAsset(RoadRunner, m.download.assets, "2024.1.5", "2024.1.5", "v2024.1.5", "linux", "amd64", null);
        assert(lin.fileName == "roadrunner-2024.1.5-linux-amd64.tar.gz" && lin.archiveFormat == ArchiveFormat.tarGz);

        plugin.validate(Environment.init);
        bool threwBad = false;
        Environment badEnv;
        badEnv.tools = [RoadRunner: "bad version"];
        try { plugin.validate(badEnv); }
        catch (Exception) { threwBad = true; }
        assert(threwBad);

        version (Posix)
        {
            auto dir = createTempDir("polka-rr-exec-");
            scope (exit) rmdirRecurseQuiet(dir);
            auto target = buildPath(dir, "rr");
            write(target, "binary");
            setFileMode(target, octal!644);
            InstallContext ctx;
            ctx.result.targetPath = target;
            plugin.postInstall(ctx);
            assert((getFileMode(target) & octal!111) != 0);
        }
    }
}
