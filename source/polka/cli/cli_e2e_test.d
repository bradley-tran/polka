module polka.cli.cli_e2e_test;

// End-to-end CLI parity tests covering all `polka.cli.run(...)` commands not
// already exercised inside `cli.d`: manifest version checks, `init` / `new` /
// `config` / `install` / `status` / `info`, `create-project`, `cert-install`,
// `db` (MySQL/MariaDB/PostgreSQL), `serve` / `start` / `stop` + workers, `sh` /
// `shell`, `logs`, `exec`, and `dispatch`.

version (unittest):

import core.sync.mutex : Mutex;
import core.thread : Thread;
import core.time : dur;
import std.algorithm : canFind, endsWith, startsWith;
import std.array : appender;
import std.conv : to;
import std.datetime : DateTime, SysTime, UTC;
import std.file : append, exists, isDir, isFile, mkdirRecurse, read, readText, write;
import std.format : format;
import std.path : baseName, buildPath, dirName;
import std.process : environment;
import std.string : indexOf, strip, toLower;
import std.zlib : Compress, HeaderFormat, UnCompress, Z_FINISH;

import polka.backend :
    BufferTextWriter,
    Config,
    CurrentManifestVersion,
    DatabaseConfig,
    DefaultComposerVersion,
    DefaultNodeJSVersion,
    DefaultPHPVersion,
    Environment,
    newStore,
    PECLExtensionConfig,
    ServerConfig,
    Store,
    TextWriter,
    WorkerConfig;
import polka.cli.cli : run;
import polka.cli.command_cert :
    certificateSHA1Thumbprint,
    installCertificateToTrustStoreFunc,
    removeCertificateFromTrustStoreFunc;
import polka.cli.command_db :
    dbNowFunc,
    dbServerSpec,
    dbStartResult,
    initializeDatabaseServerFunc,
    pingDatabaseAddressFunc,
    startDatabaseServerFunc,
    stopDatabaseServerFunc;
import polka.cli.command_logs : createLogFollowControlFunc, LogFollowControl;
import polka.cli.command_mailpit :
    mailpitNowFunc,
    mailpitRuntimeState,
    mailpitServerSpec,
    mailpitStartResult,
    pingMailpitAddressFunc,
    startMailpitServerFunc,
    stopMailpitRuntimeFunc;
import polka.cli.command_meilisearch :
    meilisearchNowFunc,
    meilisearchRuntimeState,
    meilisearchServerSpec,
    meilisearchStartResult,
    pingMeilisearchAddressFunc,
    startMeilisearchServerFunc,
    stopMeilisearchRuntimeFunc;
import polka.cli.command_phpmyadmin :
    ensurePHPMyAdminStorageConfiguredFunc,
    startPHPMyAdminNginxServeFunc,
    startPHPMyAdminPHPRuntimeServeFunc;
import polka.cli.command_sh : launchInteractiveShellFunc;
import polka.cli.command_workers :
    startWorkerProcessFunc,
    stopWorkerProcessFunc,
    workerPIDIsLiveFunc,
    workersNowFunc;
import polka.cli.serve_apache : runApacheServeFunc, startBackgroundApacheServe;
import polka.cli.serve_frankenphp : runFrankenPHPServeFunc, startBackgroundFrankenPHPServe;
import polka.cli.serve_nginx : prepareNginxServeRuntime, quoteNginxPath, runNginxServeFunc, startBackgroundNginxServe;
import polka.cli.serve_php : runPHPRuntimeServeFunc, startBackgroundPHPRuntimeServe;
import polka.cli.serve_runtime :
    pingServeAddressFunc,
    serveAppLayout,
    serverEndpoint,
    serveStatePath,
    stopServeRuntimeFunc,
    writeServeState;
import polka.cli.serve_tls :
    globalTLSCACertificatePaths,
    globalTLSCertificatePaths,
    readCertificateFile,
    serveTLSCACertName,
    serveTLSCAKeyName,
    serveTLSCertFileName,
    serveTLSKeyFileName,
    serveTLSSubdir;
import polka.cli.shell_env :
    polkaPromptEnvEnv,
    polkaPromptRootEnv,
    resolveInteractiveShell;
import polka.cli.test_helpers;
import polka.cli.keyring : keyringMockInit;
import polka.cli.types : serveNowFunc;
import polka.service :
    AppLayout,
    databaseAddress,
    databaseBootstrapSQLPath,
    databaseCredentialStatePath,
    databaseDefaultsFilePath,
    databasePasswordFilePath,
    databasePGPassFilePath,
    databaseStatePath,
    DatabaseRuntimeHooks,
    Endpoint,
    loadManagedDatabaseCredentials,
    loadManagedDatabaseState,
    ManagedDatabaseRuntimeState,
    mailpitAddress,
    mailpitStatePath,
    MailpitRuntimeState,
    meilisearchAddress,
    meilisearchMasterKeyHash,
    meilisearchStatePath,
    MeilisearchRuntimeState,
    phpMyAdminStatePath,
    ServeRuntimeState,
    ServiceContext,
    WorkerProcessState,
    WorkersRuntimeState,
    WorkerSpec,
    WorkerStartResult,
    workersStatePath,
    writeMailpitState,
    writeManagedDatabaseState,
    writeMeilisearchState,
    writeWorkersState;
import polka.support.crypto : ParsedCertificate;
import polka.support.fs :
    createTempDir,
    currentOS,
    octal,
    rmdirRecurseQuiet,
    setFileMode;
import polka.support.yaml : YamlValue;

// ============================================================================
// Shared E2E Helpers
// ============================================================================

/// Returns the platform-specific generated shim filename inside `.polka/bin`.
private string platformShimName(string tool) @safe pure
{
    return currentOS() == "windows" ? tool ~ ".cmd" : tool;
}

/// Returns the platform-specific executable name for a generic tool binary.
private string platformExeName(string name) @safe pure
{
    return currentOS() == "windows" ? name ~ ".exe" : name;
}

/// Writes an executable fake binary into `path`, creating parent directories.
private void writeExecutableFile(string path, const(ubyte)[] data) @trusted
{
    mkdirRecurse(dirName(path));
    write(path, data);
    setFileMode(path, octal!755);
}

/// Reads and checks CLI exit status with informative failure context.
private int runCLI(out string outText, out string errText, const(string)[] args) @trusted
{
    auto stdout = new BufferTextWriter();
    auto stderr = new BufferTextWriter();
    auto code = run(stdout, stderr, args);
    outText = stdout.text;
    errText = stderr.text;
    return code;
}

/// Compresses UTF-8 SQL bytes into gzip format for `.sql.gz` import tests.
private const(ubyte)[] gzipBytes(const(ubyte)[] raw) @trusted
{
    auto comp = new Compress(HeaderFormat.gzip);
    ubyte[] result;
    result ~= cast(const(ubyte)[]) comp.compress(raw);
    result ~= cast(const(ubyte)[]) comp.flush(Z_FINISH);
    return result;
}

/// Decompresses gzip bytes back to UTF-8 text for `.sql.gz` export assertions.
private string gunzipText(const(ubyte)[] gz) @trusted
{
    auto uncomp = new UnCompress(HeaderFormat.gzip);
    ubyte[] result;
    result ~= cast(const(ubyte)[]) uncomp.uncompress(gz);
    result ~= cast(const(ubyte)[]) uncomp.flush();
    return cast(string) result;
}

/// Seeds a minimal valid cached PostgreSQL payload containing `bin/psql`,
/// `bin/pg_dump`, `bin/postgres`, `bin/initdb`, `bin/pg_ctl`, and `bin/createdb`.
private string writeCachedPostgreSQLTool(
    string cacheDir,
    string ver,
    bool includeDump = true,
    bool includeServer = true
) @trusted
{
    const(ubyte)[][string] files;
    auto binDir = "bin/";
    files[binDir ~ platformExeName("psql")] = fakeDatabaseScript("psql");
    if (includeDump)
        files[binDir ~ platformExeName("pg_dump")] = fakeDatabaseDumpScript();
    if (includeServer)
    {
        files[binDir ~ platformExeName("postgres")] = fakeDatabaseScript("postgres");
        files[binDir ~ platformExeName("initdb")] = fakeDatabaseScript("initdb");
        files[binDir ~ platformExeName("pg_ctl")] = fakeDatabaseScript("pg_ctl");
        files[binDir ~ platformExeName("createdb")] = fakeDatabaseScript("createdb");
    }
    return writeCachedArchivePayload(cacheDir, "postgresql", ver, files);
}

/// Seeds offline cache archives required by framework `create-project` tests
/// (PHP, Composer, Node.js, Nginx, MariaDB, MySQL, Mailpit, and phpMyAdmin).
private void seedCreateProjectToolCaches(
    string cacheDir,
    const(ubyte)[] phpScript,
    bool useComposerPhar,
    const(ubyte)[] composerPayload
) @trusted
{
    foreach (phpVer; ["8.4", DefaultPHPVersion])
        writeCachedPHP(cacheDir, phpVer, phpScript);

    foreach (compVer; ["2.8", DefaultComposerVersion])
    {
        if (useComposerPhar)
            writeCachedComposer(cacheDir, compVer, composerPayload);
        else
            writeCachedComposerExecutable(cacheDir, compVer, composerPayload);
    }

    foreach (nodeVer; ["24", DefaultNodeJSVersion])
    {
        const(ubyte)[][string] nodeFiles;
        auto relNode = cachedToolRelativePath(cacheDir, "nodejs", nodeVer, cachedNodeJSPath(cacheDir, nodeVer));
        nodeFiles[relNode] = fakeToolScript("node");
        nodeFiles["README"] = cast(const(ubyte)[]) "test tool payload\n";
        writeCachedArchivePayload(cacheDir, "nodejs", nodeVer, nodeFiles);
    }

    const(ubyte)[][string] nginxFiles;
    nginxFiles[currentOS() == "windows" ? "nginx.exe" : "sbin/nginx"] = fakeToolScript("nginx");
    nginxFiles["README"] = cast(const(ubyte)[]) "test tool payload\n";
    writeCachedArchivePayload(cacheDir, "nginx", "1.30", nginxFiles);

    writeCachedDatabaseTool(cacheDir, "mariadb", "11.8", true, true, true);
    writeCachedDatabaseTool(cacheDir, "mysql", "8.4", true, true, true);

    const(ubyte)[][string] mailpitFiles;
    mailpitFiles[platformExeName("mailpit")] = fakeToolScript("mailpit");
    writeCachedArchivePayload(cacheDir, "mailpit", "1.30", mailpitFiles);

    const(ubyte)[][string] pmaFiles;
    pmaFiles["index.php"] = cast(const(ubyte)[]) "<?php echo 'pma';\n";
    writeCachedArchivePayload(cacheDir, "phpmyadmin", "5.2", pmaFiles);
}

// ============================================================================
// 1. Legacy Manifest Version Rejection (`manifest_version_test.go`)
// ============================================================================

unittest
{
    auto projectDir = createTempDir("polka-e2e-manifest-v1-");
    scope (exit) rmdirRecurseQuiet(projectDir);
    auto cwdGuard = ChdirGuard(projectDir);

    auto root = buildPath(projectDir, ".polka");
    mkdirRecurse(root);
    auto v1Main = "version: \"0.1\"\nroot: .polka\ntools:\n  php: \"8.4\"\n";
    auto v1Demo = "tools:\n  php: \"8.3\"\n";
    auto mainPath = buildPath(projectDir, "polka.yaml");
    auto demoPath = buildPath(projectDir, "polka.demo.yaml");
    write(mainPath, v1Main);
    write(demoPath, v1Demo);

    string outText, errText;
    foreach (cmd; [
        ["--root", root, "list"],
        ["--root", root, "use", "demo"],
        ["--root", root, "config", "tools.php", "8.5"],
        ["--root", root, "install"],
        ["--root", root, "exec", "php", "-v"],
    ])
    {
        auto code = runCLI(outText, errText, cmd);
        assert(code == 1, format("expected code 1 for %s, got %d (%s)", cmd, code, errText));
        assert(errText.canFind("0.1") || errText.canFind("0.2") || errText.canFind("manifest"), errText);
    }

    // Legacy files must never be rewritten on rejection.
    assert(readText(mainPath) == v1Main);
    assert(readText(demoPath) == v1Demo);
}

// ============================================================================
// 2. `init`, `config`, `new`, `install`, `status` / `info` (`command_env_test.go`)
// ============================================================================

// `init` defaults, no parent discovery, custom `--root`, all 6 framework presets,
// `--docroot` overrides, `php-extension` preset + `--package`, and error cases.
unittest
{
    // A. Parent project exists; nested subdir `init` stays local and creates default `.polka` + shims.
    {
        auto outerDir = createTempDir("polka-e2e-init-nested-");
        scope (exit) rmdirRecurseQuiet(outerDir);
        write(buildPath(outerDir, "polka.yaml"), "version: \"0.2\"\nroot: .polka\n");
        mkdirRecurse(buildPath(outerDir, ".polka"));

        auto innerDir = buildPath(outerDir, "subapp");
        mkdirRecurse(innerDir);
        auto cwdGuard = ChdirGuard(innerDir);

        string outText, errText;
        assert(runCLI(outText, errText, ["init"]) == 0, errText);
        assert(outText.canFind("Initialized Polka at"));
        assert(outText.canFind(buildPath(innerDir, ".polka")), outText);
        assert(exists(buildPath(innerDir, "polka.yaml")));
        assert(exists(buildPath(innerDir, ".polka", "bin", platformShimName("polka"))));
        assert(!exists(buildPath(innerDir, ".polka", "bin", platformShimName("php"))));
        assert(!exists(buildPath(outerDir, ".polka", "bin")));

        auto cfg = readTestConfigFile(innerDir);
        assert(cfg.version_ == CurrentManifestVersion);
        assert(cfg.root == ".polka");
        auto defEnv = readTestEnvironmentConfig(innerDir, "default");
        assert(defEnv.https && defEnv.server !is null && defEnv.server.hostname == "subapp.localhost");

        // Second `init <framework>` in the same directory must fail without overwriting.
        auto code2 = runCLI(outText, errText, ["init", "drupal"]);
        assert(code2 == 1);
        assert(errText.canFind("already exists"), errText);
    }

    // B. `--root custom-root init` writes custom root in `polka.yaml`.
    {
        auto projectDir = createTempDir("polka-e2e-init-root-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto cwdGuard = ChdirGuard(projectDir);

        string outText, errText;
        assert(runCLI(outText, errText, ["--root", "custom-root", "init"]) == 0, errText);
        auto cfg = readTestConfigFile(projectDir);
        assert(cfg.root == "custom-root");
        assert(exists(buildPath(projectDir, "custom-root", "bin", platformShimName("polka"))));
    }

    // C. All 6 framework presets, custom `--docroot`, blank `--docroot` rejection, and unknown framework.
    {
        struct PresetCase
        {
            string framework;
            string expectedDocroot;
        }
        auto cases = [
            PresetCase("cakephp", "webroot"),
            PresetCase("codeigniter", "public"),
            PresetCase("drupal", "web"),
            PresetCase("laravel", "public"),
            PresetCase("symfony", "public"),
            PresetCase("wordpress", "."),
        ];

        foreach (c; cases)
        {
            auto dir = createTempDir("polka-e2e-fw-");
            scope (exit) rmdirRecurseQuiet(dir);
            auto cwdGuard = ChdirGuard(dir);

            string outText, errText;
            assert(runCLI(outText, errText, ["init", c.framework]) == 0, format("%s: %s", c.framework, errText));
            assert(outText.canFind(format("Initialized Polka %s project", c.framework)), outText);

            auto env = readTestEnvironmentConfig(dir, "default");
            assert(env.framework == c.framework);
            assert(env.docroot == c.expectedDocroot);
        }

        // `--docroot` override on a framework preset.
        {
            auto dir = createTempDir("polka-e2e-fw-docroot-");
            scope (exit) rmdirRecurseQuiet(dir);
            auto cwdGuard = ChdirGuard(dir);

            string outText, errText;
            assert(runCLI(outText, errText, ["init", "laravel", "--docroot", "public/web"]) == 0, errText);
            assert(readTestEnvironmentConfig(dir, "default").docroot == "public/web");
        }

        // Reject blank `--docroot` and unknown framework `"yii"`.
        {
            auto dir = createTempDir("polka-e2e-fw-invalid-");
            scope (exit) rmdirRecurseQuiet(dir);
            auto cwdGuard = ChdirGuard(dir);

            string outText, errText;
            assert(runCLI(outText, errText, ["init", "laravel", "--docroot", ""]) == 1);
            assert(errText.canFind("--docroot requires a non-empty value"), errText);

            assert(runCLI(outText, errText, ["init", "yii"]) == 1);
            foreach (expected; ["cakephp", "codeigniter", "drupal", "laravel", "symfony", "wordpress", "supported presets: php-extension"])
                assert(errText.canFind(expected), format("missing %s in %s", expected, errText));

            // `--package` cannot be combined with framework or bare `init`.
            assert(runCLI(outText, errText, ["init", "--package", "vendor/ext"]) == 1);
            assert(errText.canFind("--package requires a project preset"), errText);

            assert(runCLI(outText, errText, ["init", "laravel", "--package", "vendor/ext"]) == 1);
            assert(errText.canFind("--package can only be used with a project preset"), errText);
        }
    }

    // D. `php-extension` preset with `--package`, keeping existing `composer.json`, and invalid `--package`.
    {
        auto dir1 = createTempDir("polka-e2e-phpext-new-");
        scope (exit) rmdirRecurseQuiet(dir1);
        auto cwd1 = ChdirGuard(dir1);

        string outText, errText;
        assert(runCLI(outText, errText, ["init", "php-extension", "--package", "acme/awesome-ext"]) == 0, errText);
        assert(outText.canFind("Wrote composer.json (type: php-ext) for PIE"), outText);
        auto composerText = readText(buildPath(dir1, "composer.json"));
        assert(composerText.canFind(`"acme/awesome-ext"`), composerText);
        assert(composerText.canFind(`"php-ext"`), composerText);

        auto dir2 = createTempDir("polka-e2e-phpext-keep-");
        scope (exit) rmdirRecurseQuiet(dir2);
        auto cwd2 = ChdirGuard(dir2);
        auto sentinelComposer = "{\n  \"name\": \"existing/pkg\"\n}\n";
        write(buildPath(dir2, "composer.json"), sentinelComposer);

        assert(runCLI(outText, errText, ["init", "php-extension"]) == 0, errText);
        assert(outText.canFind("Kept existing composer.json"), outText);
        assert(readText(buildPath(dir2, "composer.json")) == sentinelComposer);

        auto dir3 = createTempDir("polka-e2e-phpext-badpkg-");
        scope (exit) rmdirRecurseQuiet(dir3);
        auto cwd3 = ChdirGuard(dir3);
        assert(runCLI(outText, errText, ["init", "php-extension", "--package", "not-a-valid-package"]) == 1);
        assert(errText.strip().length > 0);
    }
}

// `config` resolution, dot-keys, `database.*` canonical YAML layout, warnings, and atomic validation.
unittest
{
    auto projectDir = createTempDir("polka-e2e-config-");
    scope (exit) rmdirRecurseQuiet(projectDir);
    auto cwdGuard = ChdirGuard(projectDir);
    auto root = buildPath(projectDir, ".polka");

    // Initialize manually without an active selection yet to verify fallback to `default`.
    Config initial;
    initial.version_ = CurrentManifestVersion;
    initial.root = ".polka";
    Environment defEnv;
    defEnv.name = "default";
    initial.environments["default"] = defEnv;
    writeTestConfigFile(projectDir, initial);
    mkdirRecurse(root);

    string outText, errText;
    // Unset active environment falls back to `default` and configures it.
    assert(runCLI(outText, errText, ["--root", root, "config", "tools.php", "8.4"]) == 0, errText);
    assert(outText.canFind("Configuring default environment"));
    assert(outText.canFind("Configured default"));

    // Explicit `--env default` updates `default`.
    assert(runCLI(outText, errText, ["--root", root, "config", "--env", "default", "memory-limit", "128M"]) == 0, errText);

    // Dot-keys for mailpit, meilisearch, phpmyadmin, env-vars, php/pecl extensions, opcache-config.
    assert(runCLI(outText, errText, ["--root", root, "config", "tools.mailpit", "1.30"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "settings.mailpit.smtp-port", "1125"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "settings.mailpit.ui-port", "8125"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "https", "true"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "tools.meilisearch", "1.48"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "settings.meilisearch.port", "7701"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "settings.meilisearch.master-key", "super-secret"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "env-vars.APP_ENV", "local"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "php-extensions.intl", "true"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "pecl-extensions.redis", "6.0.2"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "opcache-config.opcache.jit", "tracing"]) == 0, errText);

    // Configure database via `tools.mysql` + `database.*` and verify YAML stores version under `tools:`.
    assert(runCLI(outText, errText, ["--root", root, "config", "tools.mysql", "8.0"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "database.engine", "mysql"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "database.port", "3306"]) == 0, errText);

    auto rawYAML = readText(buildPath(projectDir, "polka.yaml"));
    assert(rawYAML.canFind("mysql:") && rawYAML.canFind("8.0"), rawYAML);
    assert(rawYAML.canFind("database:\n  engine: mysql\n  port: 3306"), rawYAML);

    auto loaded = readTestEnvironmentConfig(projectDir, "default");
    assert(loaded.memoryLimit == "128M");
    assert(loaded.envVars["APP_ENV"] == "local");
    assert(loaded.phpExtensions["intl"]);
    assert(loaded.peclExtensions["redis"].version_ == "6.0.2");
    assert(loaded.opcacheConfig["opcache.jit"] == "tracing");
    auto mp = loaded.mailpit();
    assert(mp !is null && mp.version_ == "1.30" && mp.smtpPort == 1125 && mp.uiPort == 8125 && mp.https);
    auto ms = loaded.meilisearch();
    assert(ms !is null && ms.version_ == "1.48" && ms.port == 7701 && ms.masterKey == "super-secret");

    // Switching database to postgresql + adding phpmyadmin emits warning.
    assert(runCLI(outText, errText, ["--root", root, "config", "tools.postgresql", "17"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "database.engine", "postgresql"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "tools.phpmyadmin", "5.2"]) == 0, errText);
    assert(errText.canFind("warning:") && errText.toLower().canFind("phpmyadmin") && errText.canFind("PostgreSQL"), errText);

    // Adding frankenphp alongside php emits warning about differing PHP versions.
    assert(runCLI(outText, errText, ["--root", root, "config", "tools.frankenphp", "1.12"]) == 0, errText);
    assert(errText.canFind("warning:") && errText.canFind("FrankenPHP"), errText);

    // Reject internal `tools.pie`, `--php` flag, and invalid keys/values without mutating YAML.
    auto beforeYAML = readText(buildPath(projectDir, "polka.yaml"));
    assert(runCLI(outText, errText, ["--root", root, "config", "tools.pie", "1.0"]) == 1);
    assert(errText.canFind("internal tool") || errText.canFind("pie"), errText);

    assert(runCLI(outText, errText, ["--root", root, "config", "--php", "8.4"]) == 1);
    assert(errText.canFind("unknown flag: --php"), errText);
    assert(runCLI(outText, errText, ["--root", root, "config", "bogus.key", "val"]) == 1);
    assert(runCLI(outText, errText, ["--root", root, "config", "settings.meilisearch.port", "not-a-port"]) == 1);
    assert(readText(buildPath(projectDir, "polka.yaml")) == beforeYAML);
}

// `new` defaults, PostgreSQL flags, reserved `"default"` rejection; `install`
// omitted env, nested root, `php:8.4`, `php-extensions` + Composer defaults; `status`/`info`.
unittest
{
    auto projectDir = createTempDir("polka-e2e-new-install-");
    scope (exit) rmdirRecurseQuiet(projectDir);
    auto cacheDir = buildPath(projectDir, "cache");
    auto envCache = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
    auto cwdGuard = ChdirGuard(projectDir);

    auto nestedRoot = buildPath(projectDir, "test-site", ".polka");
    writeCachedPHP(cacheDir, "8.4", fakePIEHostPHPScript(null, null));
    writeCachedPHP(cacheDir, DefaultPHPVersion, fakePIEHostPHPScript(null, null));
    writeCachedComposer(cacheDir, DefaultComposerVersion, cast(const(ubyte)[]) "composer phar\n");

    const(ubyte)[][string] nodeFiles;
    nodeFiles[cachedToolRelativePath(cacheDir, "nodejs", DefaultNodeJSVersion, cachedNodeJSPath(cacheDir, DefaultNodeJSVersion))] =
        fakeToolScript("node");
    writeCachedArchivePayload(cacheDir, "nodejs", DefaultNodeJSVersion, nodeFiles);
    writeCachedPostgreSQLTool(cacheDir, "17");

    // Initialize project with nested root `test-site/.polka`.
    string outText, errText;
    assert(runCLI(outText, errText, ["--root", "test-site/.polka", "init"]) == 0, errText);

    // `new feature` gets defaults; reserved `default` rejected.
    assert(runCLI(outText, errText, ["--root", nestedRoot, "new", "feature"]) == 0, errText);
    assert(outText.canFind("Created feature"), outText);
    auto featureEnv = readTestEnvironmentConfig(projectDir, "feature");
    assert(featureEnv.toolVersion("php") == DefaultPHPVersion);
    assert(featureEnv.toolVersion("composer") == DefaultComposerVersion);
    assert(featureEnv.toolVersion("nodejs") == DefaultNodeJSVersion);

    assert(runCLI(outText, errText, ["--root", nestedRoot, "new", "default"]) == 1);
    assert(errText.canFind("reserved") || errText.canFind("already exists") || errText.canFind("default"), errText);

    // `new pg` with PostgreSQL flags.
    assert(runCLI(outText, errText, [
        "--root", nestedRoot, "new", "pg",
        "--db-engine", "postgresql", "--db-version", "17", "--db-port", "5544",
    ]) == 0, errText);
    assert(outText.canFind("db=postgresql:17@5544"), outText);
    auto pgEnv = readTestEnvironmentConfig(projectDir, "pg");
    assert(pgEnv.database !is null && pgEnv.database.engine == "postgresql" && pgEnv.database.port == 5544);

    // Configure `demo` with `php`, `composer`, `intl`, and `meilisearch` for status/install tests.
    assert(runCLI(outText, errText, ["--root", nestedRoot, "new", "demo", "--php", "8.4"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", nestedRoot, "config", "--env", "demo", "php-extensions.intl", "true"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", nestedRoot, "use", "demo"]) == 0, errText);

    // Omitted `--env` installs active `demo` inside nested `test-site/.polka`.
    assert(runCLI(outText, errText, ["install"]) == 0, errText);
    assert(outText.canFind("Installing demo environment"), outText);
    assert(outText.canFind("Installed 'demo' environment"), outText);
    assert(exists(projectInstalledPHPPath(nestedRoot, "8.4")));

    // `php.ini` includes configured `intl` plus Composer runtime extensions (`openssl`, `zip`).
    auto phpINI = readText(projectInstalledPHPConfigPath(nestedRoot, "8.4"));
    assert(phpINI.canFind("extension=intl") || phpINI.canFind("intl"), phpINI);
    assert(phpINI.canFind("openssl"), phpINI);
    assert(phpINI.canFind("zip"), phpINI);

    // Explicit `<tool>:<version>` installs `php:8.4`; bare `demo` is rejected.
    assert(runCLI(outText, errText, ["--root", nestedRoot, "install", "php:8.4", "--env", "demo"]) == 0, errText);
    assert(outText.canFind("Installed php:8.4 for 'demo' environment"), outText);
    assert(runCLI(outText, errText, ["--root", nestedRoot, "install", "demo"]) == 1);
    assert(errText.canFind("TOOL:VERSION"), errText);

    // `status` and `info` alias report identical output, including running states & hidden master key.
    assert(runCLI(outText, errText, ["--root", nestedRoot, "config", "--env", "demo", "tools.meilisearch", "1.48"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", nestedRoot, "config", "--env", "demo", "settings.meilisearch.port", "7701"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", nestedRoot, "config", "--env", "demo", "settings.meilisearch.master-key", "hidden-key-value"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", nestedRoot, "config", "--env", "demo", "tools.mailpit", "1.30"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", nestedRoot, "config", "--env", "demo", "tools.mysql", "8.4"]) == 0, errText);
    assert(runCLI(outText, errText, ["--root", nestedRoot, "config", "--env", "demo", "database.engine", "mysql"]) == 0, errText);

    auto oldPingServe = pingServeAddressFunc;
    auto oldPingDB = pingDatabaseAddressFunc;
    auto oldPingMailpit = pingMailpitAddressFunc;
    auto oldPingMeili = pingMeilisearchAddressFunc;
    scope (exit)
    {
        pingServeAddressFunc = oldPingServe;
        pingDatabaseAddressFunc = oldPingDB;
        pingMailpitAddressFunc = oldPingMailpit;
        pingMeilisearchAddressFunc = oldPingMeili;
    }

    import std.process : thisProcessID;
    auto selfPID = cast(int) thisProcessID;

    ServeRuntimeState serveSt;
    serveSt.environmentName = "demo";
    serveSt.serverKind = "php";
    serveSt.serverScheme = "http";
    serveSt.serverAddress = "127.0.0.1:8000";
    serveSt.docroot = buildPath(projectDir, "public");
    serveSt.primaryPID = selfPID;
    writeServeState(serveStatePath(nestedRoot, "demo"), serveSt);

    ManagedDatabaseState:
    {
        import polka.service : ManagedDatabaseRuntimeState;
        ManagedDatabaseRuntimeState dbSt;
        dbSt.environmentName = "demo";
        dbSt.engine = "mysql";
        dbSt.version_ = "8.4";
        dbSt.port = 3306;
        dbSt.pid = selfPID;
        writeManagedDatabaseState(databaseStatePath(nestedRoot, "demo"), dbSt);
    }

    MailpitRuntimeState mpSt;
    mpSt.environmentName = "demo";
    mpSt.version_ = "1.30";
    mpSt.smtpPort = 1025;
    mpSt.uiPort = 8025;
    mpSt.uiScheme = "http";
    mpSt.pid = selfPID;
    writeMailpitState(mailpitStatePath(nestedRoot, "demo"), mpSt);

    MeilisearchRuntimeState msSt;
    msSt.environmentName = "demo";
    msSt.version_ = "1.48";
    msSt.port = 7701;
    msSt.pid = selfPID;
    msSt.authEnabled = true;
    msSt.masterKeyHash = meilisearchMasterKeyHash("hidden-key-value");
    writeMeilisearchState(meilisearchStatePath(nestedRoot, "demo"), msSt);

    pingServeAddressFunc = (string) => true;
    pingDatabaseAddressFunc = (string) => true;
    pingMailpitAddressFunc = (string) => true;
    pingMeilisearchAddressFunc = (string) => true;

    string statusOut, statusErr, infoOut, infoErr;
    assert(runCLI(statusOut, statusErr, ["--root", nestedRoot, "status"]) == 0, statusErr);
    assert(runCLI(infoOut, infoErr, ["--root", nestedRoot, "info"]) == 0, infoErr);
    assert(statusOut == infoOut);
    foreach (line; [
        "environment demo",
        "php 8.4",
        "pie unset",
        "meilisearch 1.48 http=http://127.0.0.1:7701 auth=enabled",
        "webserver running http://127.0.0.1:8000",
        "database-server running mysql:8.4@3306",
        "mailpit-server running smtp=1025 ui=http://127.0.0.1:8025",
        "meilisearch-server running http://127.0.0.1:7701",
    ])
    {
        assert(statusOut.canFind(line), format("status output missing %s:\n%s", line, statusOut));
    }
    assert(!statusOut.canFind("hidden-key-value"), "status must never reveal plaintext master key");
}

// ============================================================================
// 3. `create-project` Lifecycle (`command_create_project_test.go`)
// ============================================================================

// CakePHP happy path + initial failure + unknown package fallback + missing arg error.
unittest
{
    auto oldInitDB = initializeDatabaseServerFunc;
    auto oldStartDB = startDatabaseServerFunc;
    auto oldPingDB = pingDatabaseAddressFunc;
    scope (exit)
    {
        initializeDatabaseServerFunc = oldInitDB;
        startDatabaseServerFunc = oldStartDB;
        pingDatabaseAddressFunc = oldPingDB;
    }

    // CakePHP happy path.
    {
        auto workDir = createTempDir("polka-e2e-cp-cake-");
        scope (exit) rmdirRecurseQuiet(workDir);
        auto cacheDir = buildPath(workDir, "cache");
        auto targetDir = buildPath(workDir, "cake-app");

        auto gCache = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
        auto gDir = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_DIR", targetDir);
        auto cwdGuard = ChdirGuard(workDir);

        seedCreateProjectToolCaches(cacheDir, fakePIEHostPHPScript(null, null), false, fakeCakePHPCreateProjectComposerScript());

        bool dbInitCalled = false;
        bool dbStartCalled = false;
        bool dbLive = false;
        initializeDatabaseServerFunc = (dbServerSpec) { dbInitCalled = true; };
        startDatabaseServerFunc = (dbServerSpec) {
            dbStartCalled = true;
            dbLive = true;
            import std.process : thisProcessID;
            return dbStartResult(cast(int) thisProcessID);
        };
        pingDatabaseAddressFunc = (string) => dbLive;

        string outText, errText;
        assert(runCLI(outText, errText, ["create-project", "cakephp/app", "cake-app"]) == 0, errText);
        assert(outText.canFind("Created cakephp project at"), outText);
        assert(dbInitCalled && dbStartCalled);
        assert(exists(buildPath(targetDir, "polka.yaml")));
        assert(exists(buildPath(targetDir, ".polka")));
        assertCakePHPAppLocalUsesManagedDatabase(buildPath(targetDir, "config", "app_local.php"), "3306");
    }

    // Initial scaffold composer exit `3` leaves no `polka.yaml` behind.
    {
        auto workDir = createTempDir("polka-e2e-cp-fail-init-");
        scope (exit) rmdirRecurseQuiet(workDir);
        auto cacheDir = buildPath(workDir, "cache");
        auto gCache = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
        auto cwdGuard = ChdirGuard(workDir);

        seedCreateProjectToolCaches(cacheDir, fakeFailingCreateProjectHostPHPScript(3), true, cast(const(ubyte)[]) "composer phar");

        string outText, errText;
        auto code = runCLI(outText, errText, ["create-project", "laravel/laravel", "bad-app"]);
        assert(code == 3, format("expected exit 3, got %d (stderr=%s)", code, errText));
        assert(!exists(buildPath(workDir, "bad-app", "polka.yaml")));
    }

    // Unknown package falls back cleanly to generic project initialization.
    {
        auto workDir = createTempDir("polka-e2e-cp-generic-");
        scope (exit) rmdirRecurseQuiet(workDir);
        auto cacheDir = buildPath(workDir, "cache");
        auto targetDir = buildPath(workDir, "custom-app");
        auto gCache = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
        auto cwdGuard = ChdirGuard(workDir);

        writeCachedPHP(cacheDir, DefaultPHPVersion, fakePIEHostPHPScript(null, null));
        writeCachedComposer(cacheDir, DefaultComposerVersion, cast(const(ubyte)[]) "composer phar");

        string outText, errText;
        assert(runCLI(outText, errText, ["create-project", "acme/custom-app"]) == 0, errText);
        assert(outText.canFind("no framework detected"), outText);
        assert(exists(buildPath(targetDir, "polka.yaml")));

        // Missing package fails with error.
        assert(runCLI(outText, errText, ["create-project"]) == 1);
        assert(errText.canFind("requires a composer package argument"), errText);
    }
}

// Laravel two-phase `create-project` across flag combinations plus error/failure propagation.
unittest
{
    auto oldInitDB = initializeDatabaseServerFunc;
    auto oldStartDB = startDatabaseServerFunc;
    auto oldPingDB = pingDatabaseAddressFunc;
    scope (exit)
    {
        initializeDatabaseServerFunc = oldInitDB;
        startDatabaseServerFunc = oldStartDB;
        pingDatabaseAddressFunc = oldPingDB;
    }

    struct Variant
    {
        const(string)[] extraFlags;
        bool expectScripts;
        bool expectInstall;
        bool expectDBState;
    }

    auto variants = [
        Variant(null, true, true, true),
        Variant(["--no-scripts"], false, true, false),
        Variant(["--no-install"], true, false, true),
        Variant(["--no-scripts", "--no-install", "--no-dev"], false, false, false),
    ];

    foreach (idx, v; variants)
    {
        auto workDir = createTempDir("polka-e2e-cp-lar-");
        scope (exit) rmdirRecurseQuiet(workDir);
        auto cacheDir = buildPath(workDir, "cache");
        auto targetDir = buildPath(workDir, "laravel-app");

        auto gCache = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
        auto gDir = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_DIR", targetDir);
        auto gPHP = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_PHP", projectInstalledPHPPath(buildPath(targetDir, ".polka"), "8.4"));
        auto gINI = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_INI", projectInstalledPHPConfigPath(buildPath(targetDir, ".polka"), "8.4"));
        auto gNode = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_NODE", projectInstalledNodeJSPath(buildPath(targetDir, ".polka"), "24"));
        auto gSkipScripts = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_SKIP_SCRIPTS", v.expectScripts ? "false" : "true");
        auto gSkipInstall = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_SKIP_INSTALL", v.expectInstall ? "false" : "true");
        auto gExit = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_EXIT", "0");
        auto cwdGuard = ChdirGuard(workDir);

        seedCreateProjectToolCaches(cacheDir, fakeLaravelCreateProjectLifecyclePHP(), true, cast(const(ubyte)[]) "composer phar");

        bool dbLive = false;
        initializeDatabaseServerFunc = (dbServerSpec) {};
        startDatabaseServerFunc = (dbServerSpec) {
            dbLive = true;
            import std.process : thisProcessID;
            return dbStartResult(cast(int) thisProcessID);
        };
        pingDatabaseAddressFunc = (string) => dbLive;

        string[] cmd = ["create-project", "laravel/laravel", "laravel-app"];
        cmd ~= v.extraFlags;
        string outText, errText;
        auto gotCode = runCLI(outText, errText, cmd);
        assert(gotCode == 0, format("variant %d code=%d stdout=%s stderr=%s", idx, gotCode, outText, errText));
        assert(outText.canFind("Created laravel project at"), outText);

        auto log = readText(buildPath(targetDir, "lifecycle.log"));
        assert(log.canFind("scaffold") && log.canFind("complete"), log);
        assert(exists(buildPath(targetDir, ".env")));
        assert(exists(buildPath(targetDir, "migration.env")));
        auto dotenv = readText(buildPath(targetDir, ".env"));
        assert(dotenv.canFind("DB_CONNECTION=mysql"), dotenv);
        assert(dotenv.canFind("DB_USERNAME=polka"), dotenv);
        assert(exists(buildPath(targetDir, "scripts-ran")) == v.expectScripts);
        assert(exists(buildPath(targetDir, "dependencies-installed")) == v.expectInstall);
        assert(exists(databaseStatePath(buildPath(targetDir, ".polka"), "default")) == v.expectDBState);
    }

    // Tool install failure aborts before DB startup; DB startup failure aborts; exit 7 propagates.
    {
        auto workDir = createTempDir("polka-e2e-cp-abort-");
        scope (exit) rmdirRecurseQuiet(workDir);
        auto cacheDir = buildPath(workDir, "cache");
        auto targetDir = buildPath(workDir, "laravel-app");

        auto gCache = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
        auto gDir = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_DIR", targetDir);
        auto cwdGuard = ChdirGuard(workDir);

        // Seed all Laravel tool caches offline, then corrupt `nodejs` archive so materialization
        // fails deterministically without involving the network.
        seedCreateProjectToolCaches(cacheDir, fakeLaravelCreateProjectLifecyclePHP(), true, cast(const(ubyte)[]) "composer phar");
        writeCachedArchivePayload(cacheDir, "nodejs", DefaultNodeJSVersion, ["missing.txt": cast(const(ubyte)[]) "no node executable\n"]);
        bool dbCalled = false;
        initializeDatabaseServerFunc = (dbServerSpec) { dbCalled = true; };

        string outText, errText;
        assert(runCLI(outText, errText, ["create-project", "laravel/laravel", "laravel-app"]) == 1);
        assert(!dbCalled);
        assert(errText.canFind("nodejs"), errText);
    }

    {
        auto workDir = createTempDir("polka-e2e-cp-dbfail-");
        scope (exit) rmdirRecurseQuiet(workDir);
        auto cacheDir = buildPath(workDir, "cache");
        auto targetDir = buildPath(workDir, "laravel-app");

        auto gCache = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
        auto gDir = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_DIR", targetDir);
        auto gPHP = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_PHP", projectInstalledPHPPath(buildPath(targetDir, ".polka"), "8.4"));
        auto gINI = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_INI", projectInstalledPHPConfigPath(buildPath(targetDir, ".polka"), "8.4"));
        auto gNode = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_NODE", projectInstalledNodeJSPath(buildPath(targetDir, ".polka"), "24"));
        auto gSkipScripts = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_SKIP_SCRIPTS", "false");
        auto gSkipInstall = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_SKIP_INSTALL", "false");
        auto gExit = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_EXIT", "7");
        auto cwdGuard = ChdirGuard(workDir);

        seedCreateProjectToolCaches(cacheDir, fakeLaravelCreateProjectLifecyclePHP(), true, cast(const(ubyte)[]) "composer phar");

        // DB startup failure aborts.
        initializeDatabaseServerFunc = (dbServerSpec) {
            throw new Exception("boom db init");
        };
        string outText, errText;
        assert(runCLI(outText, errText, ["create-project", "laravel/laravel", "laravel-app"]) == 1);
        assert(errText.canFind("boom db init"), errText);

        // Fresh project dir with DB OK, completion phase exit `7` propagates.
        auto targetDir2 = buildPath(workDir, "laravel-app-exit7");
        auto gDir2 = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_DIR", targetDir2);
        auto gPHP2 = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_PHP", projectInstalledPHPPath(buildPath(targetDir2, ".polka"), "8.4"));
        auto gINI2 = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_INI", projectInstalledPHPConfigPath(buildPath(targetDir2, ".polka"), "8.4"));
        auto gNode2 = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_NODE", projectInstalledNodeJSPath(buildPath(targetDir2, ".polka"), "24"));

        bool dbLive = false;
        initializeDatabaseServerFunc = (dbServerSpec) {};
        startDatabaseServerFunc = (dbServerSpec) {
            dbLive = true;
            import std.process : thisProcessID;
            return dbStartResult(cast(int) thisProcessID);
        };
        pingDatabaseAddressFunc = (string) => dbLive;

        assert(runCLI(outText, errText, ["create-project", "laravel/laravel", "laravel-app-exit7"]) == 7, errText);
    }
}

// ============================================================================
// 4. `cert-install` (`command_cert_test.go`)
// ============================================================================

unittest
{
    auto oldInstall = installCertificateToTrustStoreFunc;
    auto oldRemove = removeCertificateFromTrustStoreFunc;
    scope (exit)
    {
        installCertificateToTrustStoreFunc = oldInstall;
        removeCertificateFromTrustStoreFunc = oldRemove;
    }

    auto workDir = createTempDir("polka-e2e-cert-");
    scope (exit) rmdirRecurseQuiet(workDir);
    auto projectDir = buildPath(workDir, "project");
    auto cacheDir = buildPath(workDir, "cache");
    mkdirRecurse(projectDir);
    auto gCache = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
    keyringMockInit();

    Config cfg;
    cfg.version_ = CurrentManifestVersion;
    cfg.root = ".polka";
    writeTestConfigFile(projectDir, cfg);
    auto root = buildPath(projectDir, ".polka");

    string[] order;
    string installedPath;
    string removedThumbprint;

    installCertificateToTrustStoreFunc = (string certPath) {
        order ~= "install";
        installedPath = certPath;
        return "test trust store";
    };
    removeCertificateFromTrustStoreFunc = (const ParsedCertificate cert) {
        order ~= "remove";
        removedThumbprint = certificateSHA1Thumbprint(cert);
        return "test trust store";
    };

    // Initial install encrypts keys using keyring mock.
    string outText, errText;
    assert(runCLI(outText, errText, ["--root", root, "cert-install"]) == 0, errText);
    assert(order == ["install"]);
    assert(outText.canFind("test trust store"), outText);

    auto caPaths = globalTLSCACertificatePaths(cacheDir);
    auto srvPaths = globalTLSCertificatePaths(cacheDir);
    assert(installedPath == caPaths.certPath);
    assertEncryptedTLSKeyFile(caPaths.keyPath);
    assertEncryptedTLSKeyFile(srvPaths.keyPath);

    auto firstCA = readCertificateFile(caPaths.certPath);
    auto firstThumb = certificateSHA1Thumbprint(firstCA);
    assert(firstThumb.length == 40);

    // Second `cert-install` removes the old CA thumbprint BEFORE installing a fresh one.
    order = null;
    assert(runCLI(outText, errText, ["--root", root, "cert-install"]) == 0, errText);
    assert(order == ["remove", "install"], format("order=%s", order));
    assert(removedThumbprint == firstThumb);
    auto secondCA = readCertificateFile(caPaths.certPath);
    assert(certificateSHA1Thumbprint(secondCA) != firstThumb);

    // `--no-encryption` writes plaintext RSA private keys and emits warning on stderr.
    order = null;
    assert(runCLI(outText, errText, ["--root", root, "cert-install", "--no-encryption"]) == 0, errText);
    assert(errText.canFind("warning:") && errText.canFind("without OS keyring encryption"), errText);
    assertPlaintextTLSKeyFile(caPaths.keyPath);
    assertPlaintextTLSKeyFile(srvPaths.keyPath);

    // Reject positional arguments.
    assert(runCLI(outText, errText, ["--root", root, "cert-install", "extra"]) == 1);
    assert(errText.canFind("cert-install accepts no arguments"), errText);
}

// ============================================================================
// 5. `db` Client, Export, Import, Lifecycle & PostgreSQL (`command_db*.go`)
// ============================================================================

// MySQL/MariaDB client dispatch, overrides, `--db-name`, `db client status`, import/export, lifecycle.
unittest
{
    auto oldInitDB = initializeDatabaseServerFunc;
    auto oldStartDB = startDatabaseServerFunc;
    auto oldStopDB = stopDatabaseServerFunc;
    auto oldPingDB = pingDatabaseAddressFunc;
    auto oldNowDB = dbNowFunc;
    scope (exit)
    {
        initializeDatabaseServerFunc = oldInitDB;
        startDatabaseServerFunc = oldStartDB;
        stopDatabaseServerFunc = oldStopDB;
        pingDatabaseAddressFunc = oldPingDB;
        dbNowFunc = oldNowDB;
    }

    auto projectDir = createTempDir("polka-e2e-db-mysql-");
    scope (exit) rmdirRecurseQuiet(projectDir);
    auto cacheDir = buildPath(projectDir, "cache");
    auto root = buildPath(projectDir, ".polka");
    auto gCache = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
    auto cwdGuard = ChdirGuard(projectDir);

    writeCachedDatabaseTool(cacheDir, "mysql", "8.4", true, true, true);

    Config cfg;
    cfg.version_ = CurrentManifestVersion;
    cfg.root = ".polka";
    Environment demo;
    demo.name = "demo";
    demo.tools["mysql"] = "8.4";
    demo.database = new DatabaseConfig("mysql", "8.4", 3307);
    cfg.environments["demo"] = demo;
    writeTestConfigFile(projectDir, cfg);
    writeTestActiveEnvironment(root, "demo");

    string outText, errText;
    assert(runCLI(outText, errText, ["--root", root, "install", "--env", "demo"]) == 0, errText);

    // Default client dispatch injects defaults-file, protocol=tcp, host, port, and database=demo.
    assert(runCLI(outText, errText, ["--root", root, "db"]) == 0, errText);
    assert(outText.canFind("--defaults-extra-file=" ~ databaseDefaultsFilePath(root, "demo")), outText);
    assert(outText.canFind("--protocol=tcp"), outText);
    assert(outText.canFind("--host=127.0.0.1"), outText);
    assert(outText.canFind("--port=3307"), outText);
    assert(outText.canFind("--database=demo"), outText);

    // Explicit native overrides & `--db-name` translation + `db client status`.
    assert(runCLI(outText, errText, [
        "--root", root, "db", "--host=10.0.0.2", "--port=3399", "--protocol=tcp", "--db-name", "analytics",
    ]) == 0, errText);
    assert(outText.canFind("--host=10.0.0.2"), outText);
    assert(outText.canFind("--port=3399"), outText);
    assert(outText.canFind("--database=analytics"), outText);

    assert(runCLI(outText, errText, ["--root", root, "db", "client", "status"]) == 0, errText);
    assert(outText.canFind("fake-mysql") && outText.canFind("status"), outText);

    // Seed capture target for `db import` (.sql and .sql.gz).
    auto installedClient = buildPath(root, "envs", "mysql", "8.4", "bin", platformExeName("mysql"));
    writeExecutableFile(installedClient, fakeDatabaseCaptureScript("mysql"));

    auto capturePath = buildPath(projectDir, "captured-import.sql");
    auto gCap = EnvVarGuard("POLKA_TEST_DB_CAPTURE_PATH", capturePath);

    auto plainSQL = buildPath(projectDir, "seed.sql");
    write(plainSQL, "CREATE TABLE t1 (id INT);\n");
    assert(runCLI(outText, errText, ["--root", root, "db", "import", plainSQL]) == 0, errText);
    assert(outText.canFind(`into environment "demo"`), outText);
    assert(readText(capturePath) == "CREATE TABLE t1 (id INT);\n");

    auto gzSQL = buildPath(projectDir, "seed.sql.gz");
    write(gzSQL, gzipBytes(cast(const(ubyte)[]) "INSERT INTO t1 VALUES (42);\n"));
    assert(runCLI(outText, errText, ["--root", root, "db", "import", "--db-name", "customdb", gzSQL]) == 0, errText);
    assert(outText.canFind("--database=customdb"), outText);
    assert(readText(capturePath) == "INSERT INTO t1 VALUES (42);\n");

    // `db export` (.sql and .sql.gz) atomically captures dump script output.
    auto dumpCaptureArgs = buildPath(projectDir, "dump-args.log");
    auto gDumpArgs = EnvVarGuard("POLKA_TEST_DB_DUMP_CAPTURE_PATH", dumpCaptureArgs);
    auto gDumpOut = EnvVarGuard("POLKA_TEST_DB_DUMP_OUTPUT", "-- dump payload\nSELECT 1;\n");

    auto outSQL = buildPath(projectDir, "backup.sql");
    assert(runCLI(outText, errText, ["--root", root, "db", "export", outSQL]) == 0, errText);
    assert(readText(outSQL) == "-- dump payload\nSELECT 1;\n");
    assert(readText(dumpCaptureArgs).canFind("--databases demo"), readText(dumpCaptureArgs));

    auto outGZ = buildPath(projectDir, "backup.sql.gz");
    assert(runCLI(outText, errText, ["--root", root, "db", "export", "--db-name", "reporting", outGZ]) == 0, errText);
    assert(gunzipText(cast(const(ubyte)[]) read(outGZ)) == "-- dump payload\nSELECT 1;\n");
    assert(readText(dumpCaptureArgs).canFind("--databases reporting"), readText(dumpCaptureArgs));

    // `db start` -> `db status` -> `db stop` lifecycle + generated secrets/defaults/bootstrap files.
    import std.process : thisProcessID;
    auto fakePID = cast(int) thisProcessID;
    bool dbAlive = false;
    initializeDatabaseServerFunc = (dbServerSpec spec) {
        assert(spec.engine == "mysql" && spec.port == 3307);
        assert(exists(databaseBootstrapSQLPath(root, "demo")));
        assert(exists(databaseDefaultsFilePath(root, "demo")));
        assert(exists(databaseCredentialStatePath(root, "demo")));
    };
    startDatabaseServerFunc = (dbServerSpec spec) {
        dbAlive = true;
        return dbStartResult(fakePID);
    };
    stopDatabaseServerFunc = (ManagedDatabaseRuntimeState st) {
        dbAlive = false;
    };
    pingDatabaseAddressFunc = (string addr) => addr == databaseAddress(3307) && dbAlive;
    dbNowFunc = () => SysTime(DateTime(2026, 8, 1, 12, 0, 0), UTC());

    assert(runCLI(outText, errText, ["--root", root, "db", "start"]) == 0, errText);
    assert(outText.canFind(`Started mysql for environment "demo" on 127.0.0.1:3307.`), outText);
    assert(exists(databaseStatePath(root, "demo")));
    auto creds = loadManagedDatabaseCredentials(databaseCredentialStatePath(root, "demo"));
    assert(creds.user == "polka" && creds.databaseName == "demo" && creds.password.length >= 16);

    assert(runCLI(outText, errText, ["--root", root, "db", "status"]) == 0, errText);
    assert(outText.canFind(`Database for environment "demo" is running on 127.0.0.1:3307`), outText);

    assert(runCLI(outText, errText, ["--root", root, "db", "stop"]) == 0, errText);
    assert(outText.canFind(`Stopped mysql for environment "demo".`), outText);
    assert(!exists(databaseStatePath(root, "demo")));
}

// PostgreSQL `db` client, export, import, start, and secret files (`pgpass`, `password`).
unittest
{
    auto oldInitDB = initializeDatabaseServerFunc;
    auto oldStartDB = startDatabaseServerFunc;
    auto oldStopDB = stopDatabaseServerFunc;
    auto oldPingDB = pingDatabaseAddressFunc;
    scope (exit)
    {
        initializeDatabaseServerFunc = oldInitDB;
        startDatabaseServerFunc = oldStartDB;
        stopDatabaseServerFunc = oldStopDB;
        pingDatabaseAddressFunc = oldPingDB;
    }

    auto projectDir = createTempDir("polka-e2e-db-pg-");
    scope (exit) rmdirRecurseQuiet(projectDir);
    auto cacheDir = buildPath(projectDir, "cache");
    auto root = buildPath(projectDir, ".polka");
    auto gCache = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
    auto cwdGuard = ChdirGuard(projectDir);

    writeCachedPostgreSQLTool(cacheDir, "17");

    Config cfg;
    cfg.version_ = CurrentManifestVersion;
    cfg.root = ".polka";
    Environment demo;
    demo.name = "demo";
    demo.tools["postgresql"] = "17";
    demo.database = new DatabaseConfig("postgresql", "17", 5544);
    cfg.environments["demo"] = demo;
    writeTestConfigFile(projectDir, cfg);
    writeTestActiveEnvironment(root, "demo");

    string outText, errText;
    assert(runCLI(outText, errText, ["--root", root, "install", "--env", "demo"]) == 0, errText);

    // Client dispatch injects `--host=127.0.0.1`, `--port=5544`, `--username=polka`, `--dbname=demo`.
    assert(runCLI(outText, errText, ["--root", root, "db", "--db-name", "appdb"]) == 0, errText);
    assert(outText.canFind("fake-psql"), outText);
    assert(outText.canFind("--host=127.0.0.1"), outText);
    assert(outText.canFind("--port=5544"), outText);
    assert(outText.canFind("--username=polka"), outText);
    assert(outText.canFind("--dbname=appdb"), outText);
    assert(exists(databasePGPassFilePath(root, "demo")));

    // PostgreSQL export & start.
    auto dumpCaptureArgs = buildPath(projectDir, "pg-dump-args.log");
    auto gDumpArgs = EnvVarGuard("POLKA_TEST_DB_DUMP_CAPTURE_PATH", dumpCaptureArgs);
    auto gDumpOut = EnvVarGuard("POLKA_TEST_DB_DUMP_OUTPUT", "-- pg dump\nSELECT 1;\n");

    auto pgOut = buildPath(projectDir, "pg.sql");
    assert(runCLI(outText, errText, ["--root", root, "db", "export", pgOut]) == 0, errText);
    assert(readText(pgOut) == "-- pg dump\nSELECT 1;\n");
    assert(readText(dumpCaptureArgs).canFind("--format=plain"), readText(dumpCaptureArgs));

    import std.process : thisProcessID;
    bool pgAlive = false;
    initializeDatabaseServerFunc = (dbServerSpec spec) {
        assert(spec.engine == "postgresql" && spec.port == 5544);
        assert(exists(databasePasswordFilePath(root, "demo")));
    };
    startDatabaseServerFunc = (dbServerSpec spec) {
        pgAlive = true;
        return dbStartResult(cast(int) thisProcessID);
    };
    stopDatabaseServerFunc = (ManagedDatabaseRuntimeState st) { pgAlive = false; };
    pingDatabaseAddressFunc = (string addr) => addr == databaseAddress(5544) && pgAlive;

    assert(runCLI(outText, errText, ["--root", root, "db", "start"]) == 0, errText);
    assert(outText.canFind(`Started postgresql for environment "demo" on 127.0.0.1:5544.`));
    assert(runCLI(outText, errText, ["--root", root, "db", "stop"]) == 0, errText);
    assert(outText.canFind(`Stopped postgresql for environment "demo".`));
}

// ============================================================================
// 6. `serve` / `start` / `stop` + Services & Workers (`command_serve_test.go`, `command_workers_test.go`, `command_stop_test.go`)
// ============================================================================

// Foreground `--watch` (EnvVars override, configured/positional docroot, missing docroot,
// `--server`, nginx/apache/frankenphp selection, HTTPS rejection on PHP built-in server).
unittest
{
    auto oldPHPWatch = runPHPRuntimeServeFunc;
    auto oldNginxWatch = runNginxServeFunc;
    auto oldApacheWatch = runApacheServeFunc;
    auto oldFrankenWatch = runFrankenPHPServeFunc;
    scope (exit)
    {
        runPHPRuntimeServeFunc = oldPHPWatch;
        runNginxServeFunc = oldNginxWatch;
        runApacheServeFunc = oldApacheWatch;
        runFrankenPHPServeFunc = oldFrankenWatch;
    }

    auto projectDir = createTempDir("polka-e2e-serve-watch-");
    scope (exit) rmdirRecurseQuiet(projectDir);
    auto root = buildPath(projectDir, ".polka");
    auto webDir = buildPath(projectDir, "public");
    auto altDir = buildPath(projectDir, "custom-docroot");
    mkdirRecurse(webDir);
    mkdirRecurse(altDir);
    write(buildPath(webDir, "index.php"), "<?php echo 'home';\n");
    write(buildPath(altDir, "index.php"), "<?php echo 'alt';\n");

    Config cfg;
    cfg.version_ = CurrentManifestVersion;
    cfg.root = ".polka";
    Environment demo;
    demo.name = "demo";
    demo.tools = ["php": "8.4"];
    demo.docroot = "public";
    demo.envVars = ["APP_MODE": "serve-test"];
    demo.server = new ServerConfig("", "localhost", 8089, false);
    cfg.environments["demo"] = demo;
    writeTestConfigFile(projectDir, cfg);
    writeTestActiveEnvironment(root, "demo");

    // 1. Implicit fallback to PHP built-in server with `--watch` and configured docroot.
    string seenPHPAddr, seenPHPDocroot;
    runPHPRuntimeServeFunc = (TextWriter stdout, TextWriter stderr, Store st, string addr, AppLayout layout) {
        auto cur = st.current();
        assert(cur !is null && cur.envVars["APP_MODE"] == "serve-test");
        seenPHPAddr = addr;
        seenPHPDocroot = layout.docroot;
        return 0;
    };

    string outText, errText;
    assert(runCLI(outText, errText, ["--root", root, "serve", "--watch"]) == 0, errText);
    assert(seenPHPAddr == "localhost:8089");
    assert(seenPHPDocroot == webDir);

    // 2. Positional docroot overrides configured docroot; missing docroot fails cleanly.
    assert(runCLI(outText, errText, ["--root", root, "serve", "--watch", "custom-docroot"]) == 0, errText);
    assert(seenPHPDocroot == altDir);

    assert(runCLI(outText, errText, ["--root", root, "serve", "--watch", "does-not-exist"]) == 1);
    assert(errText.canFind("does not exist") || errText.canFind("docroot"), errText);

    // 3. `--server` overrides to nginx, apache, frankenphp; explicit HTTP overrides config.
    string seenNginx, seenApache, seenFranken;
    runNginxServeFunc = (TextWriter outW, TextWriter errW, Store st, Environment env, Endpoint ep, AppLayout lyt) {
        seenNginx = ep.scheme ~ "://" ~ ep.address;
        return 0;
    };
    runApacheServeFunc = (TextWriter outW, TextWriter errW, Store st, Environment env, Endpoint ep, AppLayout lyt) {
        seenApache = ep.scheme ~ "://" ~ ep.address;
        return 0;
    };
    runFrankenPHPServeFunc = (TextWriter outW, TextWriter errW, Store st, Environment env, Endpoint ep, AppLayout lyt) {
        seenFranken = ep.scheme ~ "://" ~ ep.address;
        return 0;
    };

    assert(runCLI(outText, errText, ["--root", root, "serve", "--watch", "--server", "http://127.0.0.1:9001"]) == 1);
    assert(errText.canFind("use HOST:PORT"), errText);

    assert(runCLI(outText, errText, ["--root", root, "serve", "--watch", "--server", "127.0.0.1:9001"]) == 0, errText);
    assert(seenPHPAddr == "127.0.0.1:9001");

    demo.tools = ["php": "8.4", "nginx": "1.30", "apache": "2.4", "frankenphp": "1.12"];
    demo.server.type = "nginx";
    writeTestEnvironmentConfig(projectDir, "demo", demo);
    assert(runCLI(outText, errText, ["--root", root, "serve", "--watch"]) == 0, errText);
    assert(seenNginx == "http://localhost:8089");

    demo.server.type = "apache";
    writeTestEnvironmentConfig(projectDir, "demo", demo);
    assert(runCLI(outText, errText, ["--root", root, "serve", "--watch"]) == 0, errText);
    assert(seenApache == "http://localhost:8089");

    demo.server.type = "frankenphp";
    writeTestEnvironmentConfig(projectDir, "demo", demo);
    assert(runCLI(outText, errText, ["--root", root, "serve", "--watch"]) == 0, errText);
    assert(seenFranken == "http://localhost:8089");

    // 4. HTTPS rejected when server resolves to PHP built-in webserver.
    demo.https = true;
    demo.server.type = "php";
    demo.server.https = true;
    writeTestEnvironmentConfig(projectDir, "demo", demo);
    assert(runCLI(outText, errText, ["--root", root, "serve", "--watch"]) == 1);
    assert(errText.canFind("https is not supported by the PHP webserver"), errText);
}

// Background `start` + service dependency startup (MySQL, Mailpit, Meilisearch, phpMyAdmin HTTP+HTTPS,
// Workers) and `stop` teardown including already-stopped summaries.
unittest
{
    auto oldStartPHP = startBackgroundPHPRuntimeServe;
    auto oldStopServe = stopServeRuntimeFunc;
    auto oldPingServe = pingServeAddressFunc;
    auto oldServeNow = serveNowFunc;

    auto oldInitDB = initializeDatabaseServerFunc;
    auto oldStartDB = startDatabaseServerFunc;
    auto oldStopDB = stopDatabaseServerFunc;
    auto oldPingDB = pingDatabaseAddressFunc;

    auto oldStartMailpit = startMailpitServerFunc;
    auto oldStopMailpit = stopMailpitRuntimeFunc;
    auto oldPingMailpit = pingMailpitAddressFunc;

    auto oldStartMeili = startMeilisearchServerFunc;
    auto oldStopMeili = stopMeilisearchRuntimeFunc;
    auto oldPingMeili = pingMeilisearchAddressFunc;

    auto oldEnsurePMA = ensurePHPMyAdminStorageConfiguredFunc;
    auto oldPMAPHP = startPHPMyAdminPHPRuntimeServeFunc;
    auto oldPMANginx = startPHPMyAdminNginxServeFunc;

    auto oldStartWorker = startWorkerProcessFunc;
    auto oldStopWorker = stopWorkerProcessFunc;
    auto oldWorkerLive = workerPIDIsLiveFunc;

    scope (exit)
    {
        startBackgroundPHPRuntimeServe = oldStartPHP;
        stopServeRuntimeFunc = oldStopServe;
        pingServeAddressFunc = oldPingServe;
        serveNowFunc = oldServeNow;

        initializeDatabaseServerFunc = oldInitDB;
        startDatabaseServerFunc = oldStartDB;
        stopDatabaseServerFunc = oldStopDB;
        pingDatabaseAddressFunc = oldPingDB;

        startMailpitServerFunc = oldStartMailpit;
        stopMailpitRuntimeFunc = oldStopMailpit;
        pingMailpitAddressFunc = oldPingMailpit;

        startMeilisearchServerFunc = oldStartMeili;
        stopMeilisearchRuntimeFunc = oldStopMeili;
        pingMeilisearchAddressFunc = oldPingMeili;

        ensurePHPMyAdminStorageConfiguredFunc = oldEnsurePMA;
        startPHPMyAdminPHPRuntimeServeFunc = oldPMAPHP;
        startPHPMyAdminNginxServeFunc = oldPMANginx;

        startWorkerProcessFunc = oldStartWorker;
        stopWorkerProcessFunc = oldStopWorker;
        workerPIDIsLiveFunc = oldWorkerLive;
    }

    auto projectDir = createTempDir("polka-e2e-start-stop-");
    scope (exit) rmdirRecurseQuiet(projectDir);
    auto cacheDir = buildPath(projectDir, "cache");
    auto root = buildPath(projectDir, ".polka");
    auto gCache = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
    keyringMockInit();

    auto publicDir = buildPath(projectDir, "public");
    mkdirRecurse(publicDir);
    write(buildPath(publicDir, "index.php"), "<?php echo 'ok';\n");

    // Seed installed tool executables so ServiceContext finds MySQL, Mailpit, Meilisearch, phpMyAdmin, PHP, Nginx.
    writeExecutableFile(projectInstalledPHPPath(root, "8.4"), fakePHPScript());
    writeExecutableFile(buildPath(root, "envs", "mysql", "8.4", "bin", platformExeName("mysqld")), fakeDatabaseScript("mysqld"));
    writeExecutableFile(buildPath(root, "envs", "mysql", "8.4", "bin", platformExeName("mysqladmin")), fakeDatabaseScript("mysqladmin"));
    writeExecutableFile(buildPath(root, "envs", "mailpit", "1.30", platformExeName("mailpit")), fakeToolScript("mailpit"));
    writeExecutableFile(buildPath(root, "envs", "meilisearch", "1.48", platformExeName("meilisearch")), fakeToolScript("meilisearch"));
    writeExecutableFile(buildPath(root, "envs", "phpmyadmin", "5.2", "index.php"), cast(const(ubyte)[]) "<?php echo 'pma';\n");
    writeExecutableFile(buildPath(root, "envs", "nginx", "1.30", currentOS() == "windows" ? "nginx.exe" : "sbin/nginx"), fakeToolScript("nginx"));

    Config cfg;
    cfg.version_ = CurrentManifestVersion;
    cfg.root = ".polka";
    Environment demo;
    demo.name = "demo";
    demo.docroot = "public";
    demo.tools = [
        "php": "8.4",
        "nginx": "1.30",
        "mysql": "8.4",
        "mailpit": "1.30",
        "meilisearch": "1.48",
        "phpmyadmin": "5.2",
    ];
    demo.database = new DatabaseConfig("mysql", "8.4", 3307);
    demo.setSettingInt("mailpit", "smtp-port", 1125);
    demo.setSettingInt("mailpit", "ui-port", 8125);
    demo.setSettingInt("meilisearch", "port", 7701);
    demo.setSettingString("meilisearch", "master-key", "local-master-key");
    demo.setSettingInt("phpmyadmin", "port", 8082);
    demo.server = new ServerConfig("php", "localhost", 8000, false);
    WorkerConfig wc;
    wc.command = "php artisan queue:work";
    wc.replicas = 2;
    demo.workers["queue"] = wc;
    cfg.environments["demo"] = demo;
    writeTestConfigFile(projectDir, cfg);
    writeTestActiveEnvironment(root, "demo");

    import std.process : thisProcessID;
    auto selfPID = cast(int) thisProcessID;

    string[] startOrder;
    bool webAlive = false, dbAlive = false, mailpitAlive = false, meiliAlive = false, pmaAlive = false;
    bool[int] liveWorkerPIDs;

    initializeDatabaseServerFunc = (dbServerSpec spec) { startOrder ~= "db-init"; };
    startDatabaseServerFunc = (dbServerSpec spec) {
        startOrder ~= "mysql";
        dbAlive = true;
        return dbStartResult(selfPID);
    };
    stopDatabaseServerFunc = (ManagedDatabaseRuntimeState st) { dbAlive = false; };
    pingDatabaseAddressFunc = (string addr) => addr == databaseAddress(3307) && dbAlive;

    startMailpitServerFunc = (mailpitServerSpec spec) {
        startOrder ~= "mailpit";
        mailpitAlive = true;
        return mailpitStartResult(selfPID);
    };
    stopMailpitRuntimeFunc = (mailpitRuntimeState st) { mailpitAlive = false; };
    pingMailpitAddressFunc = (string addr) => mailpitAlive;

    startMeilisearchServerFunc = (meilisearchServerSpec spec) {
        startOrder ~= "meilisearch";
        assert(spec.masterKey == "local-master-key");
        meiliAlive = true;
        return meilisearchStartResult(selfPID);
    };
    stopMeilisearchRuntimeFunc = (meilisearchRuntimeState st) { meiliAlive = false; };
    pingMeilisearchAddressFunc = (string addr) => addr == meilisearchAddress(7701) && meiliAlive;

    ensurePHPMyAdminStorageConfiguredFunc = (ServiceContext sc, Environment env, DatabaseRuntimeHooks hooks) {
        startOrder ~= "pma-storage";
    };
    startPHPMyAdminPHPRuntimeServeFunc = (Store stStore, Environment env, string addr, AppLayout lyt, string rtDir) {
        startOrder ~= "phpmyadmin";
        pmaAlive = true;
        ServeRuntimeState st;
        st.environmentName = env.name;
        st.serverKind = "php";
        st.serverScheme = "http";
        st.serverAddress = addr;
        st.docroot = lyt.docroot;
        st.runtimeDir = rtDir;
        st.primaryPID = selfPID;
        return st;
    };

    int nextWorkerPID = 7100;
    startWorkerProcessFunc = (WorkerSpec spec, int replica) {
        if (replica == 1)
            startOrder ~= "workers";
        auto pid = nextWorkerPID++;
        liveWorkerPIDs[pid] = true;
        return WorkerStartResult(pid, buildPath(spec.logDir, format("%s-%d.log", spec.name, replica)));
    };
    stopWorkerProcessFunc = (WorkerProcessState ps) {
        liveWorkerPIDs.remove(ps.pid);
    };
    workerPIDIsLiveFunc = (int pid) => (pid in liveWorkerPIDs) !is null;

    startBackgroundPHPRuntimeServe = (Store stStore, Environment env, string addr, AppLayout lyt) {
        startOrder ~= "webserver";
        webAlive = true;
        ServeRuntimeState st;
        st.environmentName = env.name;
        st.serverKind = "php";
        st.serverScheme = "http";
        st.serverAddress = addr;
        st.docroot = lyt.docroot;
        st.primaryPID = selfPID;
        return st;
    };
    stopServeRuntimeFunc = (ServeRuntimeState st) {
        if (st.serverAddress == "127.0.0.1:8082" || st.serverAddress == "localhost:8082")
            pmaAlive = false;
        else
            webAlive = false;
    };
    pingServeAddressFunc = (string addr) {
        if (addr == "127.0.0.1:8082" || addr == "localhost:8082")
            return pmaAlive;
        return webAlive;
    };
    serveNowFunc = () => SysTime(DateTime(2026, 8, 2, 10, 0, 0), UTC());

    // `polka start` starts all services in order BEFORE starting the webserver.
    string outText, errText;
    assert(runCLI(outText, errText, ["--root", root, "start"]) == 0, errText);
    assert(startOrder == ["db-init", "mysql", "mailpit", "meilisearch", "pma-storage", "phpmyadmin", "workers", "webserver"],
        format("unexpected startup order: %s", startOrder));
    assert(outText.canFind(`Started phpMyAdmin for environment "demo" at http://127.0.0.1:8082.`), outText);
    assert(outText.canFind(`Started 2 worker process(es) for environment "demo"`), outText);
    assert(outText.canFind(`Started php webserver for environment "demo" at http://localhost:8000.`), outText);
    assert(exists(serveStatePath(root, "demo")));
    assert(exists(workersStatePath(root, "demo")));
    assert(exists(phpMyAdminStatePath(root, "demo")));

    // First `polka stop` tears down webserver + every running service and worker replica.
    assert(runCLI(outText, errText, ["--root", root, "stop"]) == 0, errText);
    foreach (msg; [
        `Stopped php webserver for environment "demo".`,
        `Stopped 2 worker process(es) for environment "demo".`,
        `Stopped phpMyAdmin for environment "demo".`,
        `Stopped Meilisearch for environment "demo".`,
        `Stopped mysql for environment "demo".`,
        `Stopped mailpit for environment "demo".`,
    ])
    {
        assert(outText.canFind(msg), format("stop missing %s:\n%s", msg, outText));
    }

    // Second `polka stop` prints already-stopped messages cleanly.
    assert(runCLI(outText, errText, ["--root", root, "stop"]) == 0, errText);
    foreach (msg; [
        `Webserver for environment "demo" is already stopped.`,
        `Workers for environment "demo" are already stopped.`,
        `phpMyAdmin for environment "demo" is already stopped.`,
        `Meilisearch for environment "demo" is already stopped.`,
        `Database for environment "demo" is already stopped.`,
        `Mailpit for environment "demo" is already stopped.`,
    ])
    {
        assert(outText.canFind(msg), format("second stop missing %s:\n%s", msg, outText));
    }
}

// ============================================================================
// 7. `sh` & `shell` (`command_sh_test.go`)
// ============================================================================

unittest
{
    auto oldShellFunc = launchInteractiveShellFunc;
    scope (exit) launchInteractiveShellFunc = oldShellFunc;

    auto projectDir = createTempDir("polka-e2e-sh-");
    scope (exit) rmdirRecurseQuiet(projectDir);
    auto nestedRoot = buildPath(projectDir, "test-site", ".polka");
    auto cwdGuard = ChdirGuard(projectDir);

    Config cfg;
    cfg.version_ = CurrentManifestVersion;
    cfg.root = "test-site/.polka";
    Environment demo;
    demo.name = "demo";
    demo.tools["php"] = "8.4";
    demo.envVars["POLKA_SH_VAR"] = "hello-shell";
    cfg.environments["demo"] = demo;
    writeTestConfigFile(projectDir, cfg);
    writeTestActiveEnvironment(nestedRoot, "demo");
    writeExecutableFile(projectInstalledPHPPath(nestedRoot, "8.4"), fakePHPScript());

    auto store = newStore(projectDir, nestedRoot);
    store.init();
    store.use("demo");

    string expectedTarget;
    string[] expectedArgs;
    resolveInteractiveShell(currentOS(), environment.get("SHELL", ""), expectedTarget, expectedArgs);

    int calls = 0;
    string seenTarget;
    string[] seenArgs;
    string[] seenEnv;
    launchInteractiveShellFunc = (TextWriter outW, TextWriter errW, const(string)[] env, string target, const(string)[] args) {
        calls++;
        seenTarget = target;
        seenArgs = args.dup;
        seenEnv = env.dup;
        return 0;
    };

    string outText, errText;
    assert(runCLI(outText, errText, ["--root", nestedRoot, "sh"]) == 0, errText);
    assert(calls == 1);
    assert(seenTarget == expectedTarget && seenArgs == expectedArgs);
    assert(outText.canFind("Opened Polka shell for environment demo"), outText);

    bool foundPath = false, foundRootEnv = false, foundEnvName = false, foundCustom = false;
    auto binDir = buildPath(nestedRoot, "bin");
    auto testSiteDir = dirName(nestedRoot);
    foreach (entry; seenEnv)
    {
        if (entry.startsWith("PATH=") && entry.canFind(binDir))
            foundPath = true;
        if (entry == polkaPromptRootEnv ~ "=" ~ testSiteDir)
            foundRootEnv = true;
        if (entry == polkaPromptEnvEnv ~ "=demo")
            foundEnvName = true;
        if (entry == "POLKA_SH_VAR=hello-shell")
            foundCustom = true;
    }
    assert(foundPath && foundRootEnv && foundEnvName && foundCustom);

    // `shell` alias dispatches identically; extra positional args rejected with exit `1`.
    assert(runCLI(outText, errText, ["--root", nestedRoot, "shell"]) == 0, errText);
    assert(calls == 2);
    assert(runCLI(outText, errText, ["--root", nestedRoot, "sh", "extra"]) == 1);
    assert(errText.canFind("sh/shell does not take arguments"), errText);
}

// ============================================================================
// 8. `logs` (`command_logs_test.go`)
// ============================================================================

unittest
{
    auto oldCreateControl = createLogFollowControlFunc;
    scope (exit) createLogFollowControlFunc = oldCreateControl;

    auto projectDir = createTempDir("polka-e2e-logs-");
    scope (exit) rmdirRecurseQuiet(projectDir);
    auto root = buildPath(projectDir, ".polka");

    Config cfg;
    cfg.version_ = CurrentManifestVersion;
    cfg.root = ".polka";
    Environment demo;
    demo.name = "demo";
    demo.tools = ["nginx": "1.30", "frankenphp": "1.12"];
    cfg.environments["demo"] = demo;
    writeTestConfigFile(projectDir, cfg);
    writeTestActiveEnvironment(root, "demo");

    auto nginxAccess = buildPath(root, "run", "nginx", "demo", "logs", "access.log");
    auto nginxError = buildPath(root, "run", "nginx", "demo", "logs", "error.log");
    auto frankenLog = buildPath(root, "run", "frankenphp", "demo", "serve.log");
    mkdirRecurse(dirName(nginxAccess));
    mkdirRecurse(dirName(frankenLog));
    write(nginxAccess, "initial\n");
    write(nginxError, "err-line\n");
    write(frankenLog, "franken-line\n");

    string outText, errText;
    // Basic nginx, `--level error`, frankenphp logs.
    assert(runCLI(outText, errText, ["--root", root, "logs", "nginx"]) == 0, errText);
    assert(outText.canFind("initial\n") && outText.canFind("err-line\n"), outText);

    assert(runCLI(outText, errText, ["--root", root, "logs", "nginx", "--level", "error"]) == 0, errText);
    assert(outText == "err-line\n", outText);

    assert(runCLI(outText, errText, ["--root", root, "logs", "frankenphp"]) == 0, errText);
    assert(outText == "franken-line\n", outText);

    // Invalid log level and missing tool logs return exit `1`.
    assert(runCLI(outText, errText, ["--root", root, "logs", "nginx", "--level", "trace"]) == 1);
    assert(errText.canFind("invalid log level"), errText);

    assert(runCLI(outText, errText, ["--root", root, "logs", "apache"]) == 1);
    assert(errText.length > 0);

    // `-f` and `--follow` through full `polka.cli.run` using cooperative cancellation and a background appender.
    foreach (flag; ["-f", "--follow"])
    {
        write(nginxAccess, "initial\n");

        auto cancel = new LogFollowControl();
        auto writerMu = new Mutex();
        bool appendedOnce = false;
        Thread appendThread;

        final class FollowTestWriter : TextWriter
        {
            private typeof(appender!string()) buf;

            this()
            {
                buf = appender!string();
            }

            override void write(const(char)[] chunk) @trusted
            {
                synchronized (writerMu)
                {
                    buf.put(chunk);
                    auto currentText = buf.data;
                    if (!appendedOnce && currentText.canFind("initial\n"))
                    {
                        appendedOnce = true;
                        appendThread = new Thread(() {
                            append(nginxAccess, "appended\n");
                        });
                        appendThread.start();
                    }
                    if (currentText.canFind("appended\n"))
                        cancel.cancel();
                }
            }

            string text() @trusted
            {
                synchronized (writerMu)
                {
                    return buf.data;
                }
            }
        }

        scope (exit)
        {
            cancel.cancel();
            if (appendThread !is null)
                appendThread.join();
        }

        createLogFollowControlFunc = () => cancel;
        auto followWriter = new FollowTestWriter();
        auto followErr = new BufferTextWriter();
        assert(run(followWriter, followErr, ["--root", root, "logs", "nginx", "--level", "info", flag]) == 0, followErr.text);
        assert(followWriter.text() == "initial\nappended\n", followWriter.text());
    }
}

// ============================================================================
// 9. `exec` & `dispatch` (`command_exec_test.go`, `command_dispatch_test.go`)
// ============================================================================

// `exec` env precedence (`.env` < `env-file` < `env-vars`), nearest nested `vendor/bin`,
// and `exec composer create-project` post-hook.
unittest
{
    auto projectDir = createTempDir("polka-e2e-exec-");
    scope (exit) rmdirRecurseQuiet(projectDir);
    auto root = buildPath(projectDir, ".polka");

    write(buildPath(projectDir, ".env"), "TEST_PRECEDENCE=from-dotenv\nDOTENV_ONLY=1\n");
    write(buildPath(projectDir, ".env.local"), "TEST_PRECEDENCE=from-env-file\n");

    Config cfg;
    cfg.version_ = CurrentManifestVersion;
    cfg.root = ".polka";
    Environment demo;
    demo.name = "demo";
    demo.tools["php"] = "8.4";
    demo.envFile = ".env.local";
    demo.envVars["TEST_PRECEDENCE"] = "from-env-vars";
    cfg.environments["demo"] = demo;
    writeTestConfigFile(projectDir, cfg);
    writeTestActiveEnvironment(root, "demo");

    // Place mock `php` directly in `.polka/bin` (rather than generating a `.polka/bin/polka`
    // shim pointing back to `polka-test-application`, which under `dub test` runs tests).
    writeExecutableFile(buildPath(root, "bin", currentOS() == "windows" ? "php.cmd" : "php"),
        fakePHPScriptWithEnv("TEST_PRECEDENCE"));

    // Verify `env-vars` overrides `env-file` overrides `.env` under `.polka/bin` resolution.
    auto nestedCWD = buildPath(projectDir, "apps", "site");
    auto vendorDrush = buildPath(nestedCWD, "vendor", "bin", platformShimName("drush").endsWith(".cmd") ? "drush.cmd" : "drush");
    writeExecutableFile(vendorDrush, fakeToolScript("drush"));
    auto cwdGuard = ChdirGuard(nestedCWD);

    string outText, errText;
    assert(runCLI(outText, errText, ["--root", root, "exec", "php", "-v"]) == 0, errText);
    assert(outText.canFind("from-env-vars"), outText);

    // Nearest `vendor/bin/drush` runs cleanly.
    assert(runCLI(outText, errText, ["--root", root, "exec", "drush", "status"]) == 0, errText);
    assert(outText.canFind("fake-drush status"), outText);

    // `exec composer create-project cakephp/app cake` triggers CakePHP post-exec hook
    // when Composer runs via PATH (`system-bin`), matching Go's `command_exec_test.go`.
    auto systemBinDir = buildPath(projectDir, "system-bin");
    auto pathSep = currentOS() == "windows" ? ";" : ":";
    auto gPath = EnvVarGuard("PATH", systemBinDir ~ pathSep ~ environment.get("PATH", ""));
    auto composerBin = buildPath(systemBinDir, currentOS() == "windows" ? "composer.cmd" : "composer");
    writeExecutableFile(composerBin, fakeCakePHPCreateProjectComposerScript(0));

    auto cakeDir = buildPath(nestedCWD, "cake");
    auto gCake = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_DIR", cakeDir);
    Environment cakeDefault;
    cakeDefault.name = "default";
    cakeDefault.framework = "cakephp";
    cakeDefault.tools["mysql"] = "8.4";
    cakeDefault.docroot = "cake/webroot";
    cakeDefault.database = new DatabaseConfig("mysql", "8.4", 3307);
    writeTestEnvironmentConfig(projectDir, "default", cakeDefault);
    writeTestActiveEnvironment(root, "default");

    assert(runCLI(outText, errText, ["--root", root, "exec", "composer", "create-project", "cakephp/app", "cake"]) == 0, errText);
    assertCakePHPAppLocalUsesManagedDatabase(buildPath(cakeDir, "config", "app_local.php"), "3307");
}

// `dispatch` (PHP + `OPENSSL_CONF`, FrankenPHP PHP fallback, env loading, Laravel `composer install`,
// CakePHP exit 0/1 vs exit 2 hook behavior, rejection of `pie`/`nodejs`, and `node`/`npm`/`npx`/`yarn`).
unittest
{
    // A. `dispatch php` sets OPENSSL_CONF & loads `.env` < `env-file` < `env-vars`; `pie`/`nodejs` rejected.
    {
        auto projectDir = createTempDir("polka-e2e-dispatch-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");
        auto cwdGuard = ChdirGuard(projectDir);

        write(buildPath(projectDir, ".env"), "APP_PREF=dotenv\n");
        write(buildPath(projectDir, ".env.custom"), "APP_PREF=envfile\n");

        Config cfg;
        cfg.version_ = CurrentManifestVersion;
        cfg.root = ".polka";
        Environment demo;
        demo.name = "demo";
        demo.tools = ["php": "8.4", "nodejs": "24"];
        demo.envFile = ".env.custom";
        demo.envVars["APP_PREF"] = "envvars";
        cfg.environments["demo"] = demo;
        writeTestConfigFile(projectDir, cfg);
        writeTestActiveEnvironment(root, "demo");

        auto phpBin = projectInstalledPHPPath(root, "8.4");
        auto sslConfDir = buildPath(dirName(phpBin), "extras", "ssl");
        mkdirRecurse(sslConfDir);
        write(buildPath(sslConfDir, "openssl.cnf"), "openssl_conf = default_conf\n");
        writeExecutableFile(phpBin, fakePHPScriptWithEnv("OPENSSL_CONF"));

        string outText, errText;
        assert(runCLI(outText, errText, ["--root", root, "dispatch", "php", "-v"]) == 0, errText);
        assert(outText.canFind("openssl.cnf"), outText);

        // `pie` is internal and `nodejs` must be invoked via `node`/`npm`/`npx`/`yarn`.
        assert(runCLI(outText, errText, ["--root", root, "dispatch", "pie"]) == 1);
        assert(runCLI(outText, errText, ["--root", root, "dispatch", "nodejs"]) == 1);

        // Node subcommands resolve to their executables under `envs/nodejs/<ver>`.
        foreach (cmd; ["node", "npm", "npx", "yarn"])
            writeExecutableFile(projectInstalledNodeJSCommandPath(root, "24", cmd), fakeToolScript(cmd));

        foreach (cmd; ["node", "npm", "npx", "yarn"])
        {
            assert(runCLI(outText, errText, ["--root", root, "dispatch", cmd, "--version"]) == 0,
                format("%s: %s", cmd, errText));
            assert(outText.canFind("fake-" ~ cmd), outText);
        }
    }

    // B. FrankenPHP PHP CLI fallback when standalone `php` is not set.
    {
        auto projectDir = createTempDir("polka-e2e-dispatch-franken-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto cacheDir = buildPath(projectDir, "cache");
        auto gCache = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
        auto root = buildPath(projectDir, ".polka");
        auto cwdGuard = ChdirGuard(projectDir);

        writeCachedFrankenPHP(cacheDir, "1.12");

        Config cfg;
        cfg.version_ = CurrentManifestVersion;
        cfg.root = ".polka";
        Environment demo;
        demo.name = "demo";
        demo.tools["frankenphp"] = "1.12";
        cfg.environments["demo"] = demo;
        writeTestConfigFile(projectDir, cfg);
        writeTestActiveEnvironment(root, "demo");

        string outText, errText;
        assert(runCLI(outText, errText, ["--root", root, "install", "--env", "demo"]) == 0, errText);
        assert(runCLI(outText, errText, ["--root", root, "dispatch", "php", "-v"]) == 0, errText);
        assert(outText.canFind("fake-frankenphp-php") && outText.canFind("-v"), outText);
    }

    // C. Laravel `dispatch composer install` & CakePHP hook on exit `0` / `1` vs skipped on `2`.
    {
        auto projectDir = createTempDir("polka-e2e-dispatch-hooks-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");
        auto cwdGuard = ChdirGuard(projectDir);

        write(buildPath(projectDir, "artisan"), "<?php\n");
        write(buildPath(projectDir, ".env.example"), "DB_CONNECTION=sqlite\n");

        Config cfg;
        cfg.version_ = CurrentManifestVersion;
        cfg.root = ".polka";
        Environment envCfg;
        envCfg.name = "default";
        envCfg.framework = "laravel";
        envCfg.tools = ["php": "8.4", "composer": "2.8", "mysql": "8.4"];
        envCfg.database = new DatabaseConfig("mysql", "8.4", 3307);
        cfg.environments["default"] = envCfg;
        writeTestConfigFile(projectDir, cfg);

        writeExecutableFile(projectInstalledPHPPath(root, "8.4"), fakePHPScript());
        auto composerBin = buildPath(root, "envs", "composer", "2.8", "bin",
            currentOS() == "windows" ? "composer.cmd" : "composer");
        writeExecutableFile(composerBin, fakeToolScript("composer"));

        string outText, errText;
        assert(runCLI(outText, errText, ["--root", root, "dispatch", "composer", "install"]) == 0, errText);
        auto dotenv = readText(buildPath(projectDir, ".env"));
        assert(dotenv.canFind("DB_CONNECTION=mysql"), dotenv);
        assert(dotenv.canFind("DB_PORT=3307"), dotenv);

        // CakePHP exit 1 runs post-composer hook; exit 2 skips hook.
        envCfg.framework = "cakephp";
        writeTestEnvironmentConfig(projectDir, "default", envCfg);
        foreach (exitCode; [1, 2])
        {
            auto subDir = buildPath(projectDir, format("cake-exit-%d", exitCode));
            mkdirRecurse(subDir);
            auto gCake = EnvVarGuard("POLKA_TEST_CREATE_PROJECT_DIR", subDir);
            writeExecutableFile(composerBin, fakeCakePHPCreateProjectComposerScript(exitCode));

            auto gotCode = runCLI(outText, errText, [
                "--root", root, "dispatch", "composer", "create-project", "cakephp/app", subDir,
            ]);
            assert(gotCode == exitCode, format("got=%d want=%d stderr=%s", gotCode, exitCode, errText));

            auto appLocalPath = buildPath(subDir, "config", "app_local.php");
            assert(exists(appLocalPath));
            if (exitCode == 1)
                assertCakePHPAppLocalUsesManagedDatabase(appLocalPath, "3307");
            else
                assert(readText(appLocalPath).canFind("Sqlite"), "exit code 2 must skip CakePHP rewrite hook");
        }
    }
}
