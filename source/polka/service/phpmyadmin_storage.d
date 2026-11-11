module polka.service.phpmyadmin_storage;

import std.file : exists, isDir, readText;
import std.format : format;
import std.path : buildPath;
import std.string : icmp, strip;

import polka.config;
import polka.service.context;
import polka.service.database;
import polka.service.process;
import polka.support.fs : fromSlashPath, isSymlinkPath;
import polka.support.net : joinHostPort;

enum string PHPMyAdminListenHost = "127.0.0.1";
enum int DefaultPHPMyAdminPort = 8081;
enum string phpMyAdminStorageCreateTablesSQLPath = "sql/create_tables.sql";

/// Returns the configured phpMyAdmin HTTP/HTTPS port or the default (`8081`).
int effectivePHPMyAdminPort(const(PHPMyAdminConfig)* phpMyAdmin) @safe pure nothrow @nogc
{
    if (phpMyAdmin is null || phpMyAdmin.port == 0)
        return DefaultPHPMyAdminPort;
    return phpMyAdmin.port;
}

/// Returns the loopback bind address `127.0.0.1:<port>` for phpMyAdmin.
string phpMyAdminAddress(int port) @safe
{
    return joinHostPort(PHPMyAdminListenHost, port);
}

/// Resolves and verifies the installed phpMyAdmin docroot under `<envsDir>/phpmyadmin/<version>`.
string resolvePHPMyAdminDocroot(string envsDir, string version_) @trusted
{
    auto trimmedVersion = version_.strip();
    if (trimmedVersion.length == 0)
        throw new Exception("phpmyadmin version cannot be empty");

    auto docroot = buildPath(envsDir, toolPHPMyAdmin, trimmedVersion);
    auto indexPath = buildPath(docroot, "index.php");
    if (!exists(indexPath))
        throw new Exception(format(`phpmyadmin version "%s" is not installed under %s`, trimmedVersion, docroot));
    if (isDir(indexPath))
        throw new Exception(format(`phpmyadmin version "%s" has a directory where index.php was expected under %s`, trimmedVersion, docroot));

    return docroot;
}

/// Starts the configured MySQL/MariaDB database (if needed) and imports phpMyAdmin's
/// `create_tables.sql` storage schema, or emits a warning and skips when PostgreSQL is paired.
void ensurePHPMyAdminStorageConfigured(
    ServiceContext ctx,
    Environment environment,
    DatabaseRuntimeHooks hooks
) @trusted
{
    auto pma = environment.phpMyAdmin();
    if (pma is null || pma.version_.strip().length == 0)
        return;
    if (environment.database is null || environment.database.engine.strip().length == 0)
        return;
    if (phpMyAdminUsesPostgreSQL(environment))
    {
        warnPHPMyAdminPostgreSQL(ctx, environment);
        return;
    }

    auto docroot = resolvePHPMyAdminDocroot(ctx.envsDir, pma.version_);
    auto createTablesPath = buildPath(docroot, fromSlashPath(phpMyAdminStorageCreateTablesSQLPath));
    if (!exists(createTablesPath) && !isSymlinkPath(createTablesPath))
        throw new Exception(format("stat phpmyadmin storage SQL %s: no such file or directory", createTablesPath));
    if (isDir(createTablesPath))
        throw new Exception(format("phpmyadmin storage SQL %s is a directory", createTablesPath));

    auto resolved = ResolvedDatabaseEnvironment(environment, environment.database);
    ManagedDatabaseRuntimeState state;
    try
    {
        bool unused;
        state = ensureManagedDatabaseStarted(ctx, resolved, hooks, unused);
    }
    catch (Exception e)
    {
        throw new Exception("start database for phpmyadmin storage: " ~ e.msg);
    }

    importPHPMyAdminStorageSQL(ctx, resolved, state, createTablesPath);
}

/// Reports whether `environment` pairs phpMyAdmin with PostgreSQL, which phpMyAdmin cannot manage directly.
bool phpMyAdminUsesPostgreSQL(Environment environment) @safe pure
{
    auto pma = environment.phpMyAdmin();
    return pma !is null &&
        pma.version_.strip().length > 0 &&
        environment.database !is null &&
        icmp(environment.database.engine.strip(), toolPostgreSQL) == 0;
}

/// Formats the user-facing warning when phpMyAdmin is paired with PostgreSQL.
string phpMyAdminPostgreSQLWarning(string environmentName) @safe
{
    auto name = environmentName.strip();
    if (name.length == 0)
        name = "current";

    return format(
        `environment "%s" configures phpMyAdmin with PostgreSQL; phpMyAdmin only supports MySQL/MariaDB, so Polka will start phpMyAdmin without managed PostgreSQL login or storage integration` ~ "\n",
        name
    );
}

/// Emits the PostgreSQL/phpMyAdmin compatibility warning on `ctx` when applicable.
void warnPHPMyAdminPostgreSQL(ServiceContext ctx, Environment environment) @trusted
{
    if (!phpMyAdminUsesPostgreSQL(environment))
        return;
    ctx.warnf("%s", phpMyAdminPostgreSQLWarning(environment.name));
}

private void importPHPMyAdminStorageSQL(
    ServiceContext ctx,
    ResolvedDatabaseEnvironment resolved,
    ManagedDatabaseRuntimeState state,
    string sqlPath
) @trusted
{
    import std.conv : to;

    auto target = ctx.resolveInstalledTool(resolved.database.engine, resolved.database.version_);
    ensureManagedDatabaseCredentialAssets(ctx.rootDir, resolved);

    string sqlText;
    try
    {
        sqlText = readText(sqlPath);
    }
    catch (Exception e)
    {
        throw new Exception(format("open phpmyadmin storage SQL %s: %s", sqlPath, e.msg));
    }

    auto port = state.port;
    if (port == 0)
        port = effectiveDatabasePort(resolved.database);

    auto args = [
        "--defaults-extra-file=" ~ databaseDefaultsFilePath(ctx.rootDir, resolved.environment.name),
        "--protocol=tcp",
        "--host=" ~ DatabaseListenHost,
        "--port=" ~ to!string(port),
    ];

    string output;
    int status = 0;
    try
    {
        status = runServiceCommandSync(target, args, output, sqlText);
    }
    catch (Exception e)
    {
        auto detail = output.strip();
        if (detail.length == 0)
            throw new Exception("import phpmyadmin storage SQL: " ~ e.msg);
        throw new Exception(format("import phpmyadmin storage SQL: %s (%s)", e.msg, detail));
    }

    if (status != 0)
    {
        auto detail = output.strip();
        if (detail.length == 0)
            throw new Exception(format("import phpmyadmin storage SQL: exit status %d", status));
        throw new Exception(format("import phpmyadmin storage SQL: exit status %d (%s)", status, detail));
    }
}

unittest
{
    import std.array : appender;
    import std.algorithm : canFind;
    import std.file : mkdirRecurse, write;
    import std.path : dirName;
    import std.process : env = environment, thisProcessID;
    import polka.support.fs : createTempDir, currentOS, octal, rmdirRecurseQuiet, setFileMode;
    import polka.tools : newDefaultRegistry;

    static string writeFakeDatabaseClient(string envsDir, string engine, string ver)
    {
        auto clientPath = buildPath(envsDir, engine, ver, "bin", engine);
        if (currentOS() == "windows")
            clientPath ~= ".cmd";
        mkdirRecurse(dirName(clientPath));
        string script;
        if (currentOS() == "windows")
        {
            script = "@echo off\r\n" ~
                "echo %* > \"%POLKA_TEST_DB_ARGS_CAPTURE_PATH%\"\r\n" ~
                "powershell -NoProfile -Command \"[IO.File]::WriteAllText($env:POLKA_TEST_DB_STDIN_CAPTURE_PATH, [Console]::In.ReadToEnd())\"\r\n";
        }
        else
        {
            script = "#!/usr/bin/env sh\n" ~
                "printf '%s\\n' \"$*\" > \"$POLKA_TEST_DB_ARGS_CAPTURE_PATH\"\n" ~
                "cat > \"$POLKA_TEST_DB_STDIN_CAPTURE_PATH\"\n";
        }
        write(clientPath, script);
        version (Posix)
            setFileMode(clientPath, octal!755);
        return clientPath;
    }

    // TestEnsurePHPMyAdminStorageConfiguredImportsCreateTablesSQL
    {
        auto projectDir = createTempDir("polka-pma-storage-");
        scope (exit) rmdirRecurseQuiet(projectDir);

        auto rootDir = buildPath(projectDir, ".polka");
        auto envsDir = buildPath(rootDir, "envs");
        auto dbCfg = new DatabaseConfig(toolMySQL, "8.4", 3307);
        Environment environment;
        environment.name = "demo";
        environment.database = dbCfg;
        environment.tools = ["phpmyadmin": "5.2"];

        ServiceContext ctx;
        ctx.projectDir = projectDir;
        ctx.rootDir = rootDir;
        ctx.envsDir = envsDir;
        ctx.environment = environment;
        ctx.registry = newDefaultRegistry();
        ctx.tlsCertFn = (string) => TLSKeyPair("", "");

        writeFakeDatabaseClient(envsDir, toolMySQL, "8.4");
        auto createTablesPath = buildPath(envsDir, toolPHPMyAdmin, "5.2", "sql", "create_tables.sql");
        mkdirRecurse(dirName(createTablesPath));
        auto createTablesSQL = "CREATE DATABASE IF NOT EXISTS `phpmyadmin`;\nUSE phpmyadmin;\nCREATE TABLE IF NOT EXISTS `pma__relation` (`id` int);\n";
        write(createTablesPath, createTablesSQL);
        write(buildPath(envsDir, toolPHPMyAdmin, "5.2", "index.php"), "<?php\n");

        ManagedDatabaseRuntimeState st;
        st.environmentName = environment.name;
        st.engine = toolMySQL;
        st.version_ = "8.4";
        st.port = 3307;
        st.pid = thisProcessID;
        writeManagedDatabaseState(databaseStatePath(rootDir, environment.name), st);

        auto argsCapturePath = buildPath(projectDir, "db-args.txt");
        auto stdinCapturePath = buildPath(projectDir, "db-stdin.sql");

        auto hadArgsEnv = "POLKA_TEST_DB_ARGS_CAPTURE_PATH" in env;
        auto prevArgsEnv = env.get("POLKA_TEST_DB_ARGS_CAPTURE_PATH", "");
        auto hadStdinEnv = "POLKA_TEST_DB_STDIN_CAPTURE_PATH" in env;
        auto prevStdinEnv = env.get("POLKA_TEST_DB_STDIN_CAPTURE_PATH", "");
        env["POLKA_TEST_DB_ARGS_CAPTURE_PATH"] = argsCapturePath;
        env["POLKA_TEST_DB_STDIN_CAPTURE_PATH"] = stdinCapturePath;
        scope (exit)
        {
            if (hadArgsEnv)
                env["POLKA_TEST_DB_ARGS_CAPTURE_PATH"] = prevArgsEnv;
            else
                env.remove("POLKA_TEST_DB_ARGS_CAPTURE_PATH");
            if (hadStdinEnv)
                env["POLKA_TEST_DB_STDIN_CAPTURE_PATH"] = prevStdinEnv;
            else
                env.remove("POLKA_TEST_DB_STDIN_CAPTURE_PATH");
        }

        DatabaseRuntimeHooks hooks;
        hooks.pingAddress = (string address) => address == databaseAddress(3307);
        ensurePHPMyAdminStorageConfigured(ctx, environment, hooks);

        auto importedSQL = readText(stdinCapturePath);
        assert(importedSQL == createTablesSQL);

        auto args = readText(argsCapturePath);
        foreach (expected; [
            "--defaults-extra-file=" ~ databaseDefaultsFilePath(rootDir, environment.name),
            "--protocol=tcp",
            "--host=127.0.0.1",
            "--port=3307",
        ])
        {
            assert(args.canFind(expected));
        }
    }

    // TestEnsurePHPMyAdminStorageConfiguredWarnsAndSkipsPostgreSQL
    {
        auto dbCfg = new DatabaseConfig(toolPostgreSQL, "17", 5432);
        Environment environment;
        environment.name = "demo";
        environment.database = dbCfg;
        environment.tools = ["phpmyadmin": "5.2"];

        auto warnings = appender!string();
        ServiceContext ctx;
        ctx.environment = environment;
        ctx.warnFn = (string msg) { warnings.put(msg); };

        bool startedDatabase = false;
        DatabaseRuntimeHooks hooks;
        hooks.startServer = (ManagedDatabaseServerSpec) {
            startedDatabase = true;
            return ManagedDatabaseStartResult.init;
        };

        ensurePHPMyAdminStorageConfigured(ctx, environment, hooks);
        assert(!startedDatabase);
        assert(warnings.data.canFind("phpMyAdmin with PostgreSQL"));
    }
}
