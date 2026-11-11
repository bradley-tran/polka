module polka.cli.test_helpers;

// Shared offline deterministic fixtures and RAII state guards for CLI unit
// and integration tests, mirroring Go's `cli/cli_test_helpers_test.go`.

version (unittest):

import std.algorithm : canFind, endsWith, sort, startsWith;
import std.conv : to;
import std.file : chdir, dirEntries, exists, getcwd, isDir, mkdirRecurse, read, readText, SpanMode, write;
import std.format : format;
import std.json : JSONType, JSONValue, parseJSON;
import std.path : baseName, buildPath, dirName, relativePath;
import std.process : environment;
import std.string : strip;

import polka.config :
    cloneEnvironment,
    Config,
    CurrentManifestVersion,
    Environment,
    EnvironmentFile,
    environmentFileFromEnvironment,
    environmentFileFromYAML,
    environmentFileToEnvironment,
    environmentFileToYAML,
    ProjectFile,
    projectFileFromEnvironment,
    projectFileFromYAML,
    projectFileToEnvironment,
    projectFileToYAML;
import polka.support.archive : ArchiveEntrySpec, buildZipBytes;
import polka.support.crypto : sha256Hex;
import polka.support.fs : currentOS, fromSlashPath, octal, setFileMode, toSlashPath;
import polka.support.yaml : emitYAML, parseYAML;

/// Changes CWD for the lifetime of a scope, automatically restoring `originalDir`.
struct ChdirGuard
{
    private string originalDir;
    private bool active = false;

    this(string targetDir)
    {
        originalDir = getcwd();
        chdir(targetDir);
        active = true;
    }

    ~this()
    {
        if (active && originalDir.length > 0 && exists(originalDir))
            chdir(originalDir);
    }
}

/// Saves and restores one process environment variable cleanly on scope exit.
struct EnvVarGuard
{
    private string name;
    private string originalValue;
    private bool hadOriginal = false;

    this(string varName, string newValue, bool unset = false)
    {
        name = varName;
        auto existing = environment.get(varName, null);
        if (existing !is null)
        {
            hadOriginal = true;
            originalValue = existing;
        }
        if (unset)
            environment.remove(varName);
        else
            environment[varName] = newValue;
    }

    ~this()
    {
        if (name.length == 0)
            return;
        if (hadOriginal)
            environment[name] = originalValue;
        else
            environment.remove(name);
    }
}

/// Writes `polka.yaml` and any named `polka.<name>.yaml` files from `config`.
void writeTestConfigFile(string projectDir, const Config config)
{
    auto ver = config.version_.length > 0 ? config.version_ : CurrentManifestVersion;
    auto root = config.root.strip().length > 0 ? config.root : ".polka";

    Environment defaultEnv;
    defaultEnv.name = "default";
    if (auto p = "default" in config.environments)
        defaultEnv = cloneEnvironment(*p);

    auto pf = projectFileFromEnvironment(ver, root, defaultEnv);
    auto pfYaml = projectFileToYAML(pf);
    write(buildPath(projectDir, "polka.yaml"), emitYAML(pfYaml));

    foreach (name, env; config.environments)
    {
        if (name == "default")
            continue;
        writeTestEnvironmentConfig(projectDir, name, env);
    }
}

/// Writes either `polka.yaml` (preserving version & root) or `polka.<name>.yaml`.
void writeTestEnvironmentConfig(string projectDir, string name, const Environment env)
{
    if (name == "default")
    {
        auto path = buildPath(projectDir, "polka.yaml");
        string ver = CurrentManifestVersion;
        string root = ".polka";
        if (exists(path))
        {
            auto existing = projectFileFromYAML(parseYAML(readText(path)));
            if (existing.version_.length > 0) ver = existing.version_;
            if (existing.root.length > 0) root = existing.root;
        }
        auto pf = projectFileFromEnvironment(ver, root, env);
        auto pfYaml = projectFileToYAML(pf);
        write(path, emitYAML(pfYaml));
        return;
    }

    auto ef = environmentFileFromEnvironment(env);
    auto efYaml = environmentFileToYAML(ef);
    write(testEnvironmentConfigPath(projectDir, name), emitYAML(efYaml));
}

/// Reads `polka.yaml` and all `polka.<name>.yaml` files under `projectDir` into `Config`.
Config readTestConfigFile(string projectDir)
{
    auto pf = projectFileFromYAML(parseYAML(readText(buildPath(projectDir, "polka.yaml"))));
    Config cfg;
    cfg.version_ = pf.version_;
    cfg.root = pf.root;
    cfg.environments["default"] = projectFileToEnvironment("default", pf);

    foreach (entry; dirEntries(projectDir, SpanMode.shallow))
    {
        if (entry.isDir)
            continue;
        string envName;
        if (testNamedEnvironmentName(baseName(entry.name), envName))
            cfg.environments[envName] = readTestEnvironmentConfig(projectDir, envName);
    }
    return cfg;
}

/// Reads a single environment definition (`"default"` or named) from `projectDir`.
Environment readTestEnvironmentConfig(string projectDir, string name)
{
    if (name == "default")
    {
        auto pf = projectFileFromYAML(parseYAML(readText(buildPath(projectDir, "polka.yaml"))));
        return projectFileToEnvironment("default", pf);
    }
    auto ef = environmentFileFromYAML(parseYAML(readText(testEnvironmentConfigPath(projectDir, name))), false);
    return environmentFileToEnvironment(name, ef);
}

string testEnvironmentConfigPath(string projectDir, string name) @safe pure
{
    if (name == "default")
        return buildPath(projectDir, "polka.yaml");
    return buildPath(projectDir, "polka." ~ name ~ ".yaml");
}

bool testNamedEnvironmentName(string fileName, out string name) @safe pure
{
    name = "";
    if (fileName == "polka.yaml" || !fileName.startsWith("polka.") || !fileName.endsWith(".yaml"))
        return false;
    name = fileName["polka.".length .. $ - ".yaml".length];
    return true;
}

string readTestActiveEnvironment(string root)
{
    return readText(buildPath(root, "run", "current")).strip();
}

void writeTestActiveEnvironment(string root, string name)
{
    auto path = buildPath(root, "run", "current");
    mkdirRecurse(dirName(path));
    write(path, name ~ "\n");
}

string cachedPHPPath(string root, string ver) @safe pure
{
    if (currentOS() == "windows")
        return buildPath(root, "php", ver, "bin", "php.cmd");
    return buildPath(root, "php", ver, "bin", "php");
}

void writeCachedPHPCABundle(string root, string ver)
{
    auto path = buildPath(root, "php", ver, "extras", "ssl", "cacert.pem");
    mkdirRecurse(dirName(path));
    write(path, "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n");
}

string cachedComposerPath(string root, string ver) @safe pure
{
    if (currentOS() == "windows")
        return buildPath(root, "composer", ver, "bin", "composer.cmd");
    return buildPath(root, "composer", ver, "bin", "composer.phar");
}

string cachedComposerExecutablePath(string root, string ver) @safe pure
{
    if (currentOS() == "windows")
        return buildPath(root, "composer", ver, "bin", "composer.cmd");
    return buildPath(root, "composer", ver, "bin", "composer");
}

string cachedPIEPath(string root, string ver) @safe pure
{
    return buildPath(root, "pie", ver, "bin", "pie.phar");
}

string cachedNodeJSPath(string root, string ver) @safe pure
{
    if (currentOS() == "windows")
        return buildPath(root, "nodejs", ver, "node.exe");
    return buildPath(root, "nodejs", ver, "bin", "node");
}

string projectInstalledNodeJSCommandPath(string root, string ver, string command) @safe pure
{
    if (currentOS() == "windows")
        return buildPath(root, "envs", "nodejs", ver, command ~ ".cmd");
    return buildPath(root, "envs", "nodejs", ver, "bin", command);
}

string projectInstalledNodeJSPath(string root, string ver) @safe pure
{
    if (currentOS() == "windows")
        return buildPath(root, "envs", "nodejs", ver, "node.exe");
    return buildPath(root, "envs", "nodejs", ver, "bin", "node");
}

string cachedDatabasePath(string root, string tool, string ver) @safe pure
{
    if (currentOS() == "windows")
        return buildPath(root, tool, ver, "bin", tool ~ ".cmd");
    return buildPath(root, tool, ver, "bin", tool);
}

string cachedMailpitPath(string root, string ver) @safe pure
{
    if (currentOS() == "windows")
        return buildPath(root, "mailpit", ver, "mailpit.exe");
    return buildPath(root, "mailpit", ver, "mailpit");
}

string cachedDatabaseServerPath(string root, string tool, string ver) @safe pure
{
    auto serverName = tool == "mariadb" ? "mariadbd" : "mysqld";
    if (currentOS() == "windows")
        return buildPath(root, tool, ver, "bin", serverName ~ ".exe");
    return buildPath(root, tool, ver, "bin", serverName);
}

string cachedDatabaseAdminPath(string root, string tool, string ver) @safe pure
{
    auto adminName = tool == "mariadb" ? "mariadb-admin" : "mysqladmin";
    if (currentOS() == "windows")
        return buildPath(root, tool, ver, "bin", adminName ~ ".exe");
    return buildPath(root, tool, ver, "bin", adminName);
}

string cachedDatabaseDumpPath(string root, string tool, string ver) @safe pure
{
    auto dumpName = tool == "mariadb" ? "mariadb-dump" : "mysqldump";
    if (currentOS() == "windows")
        return buildPath(root, tool, ver, "bin", dumpName ~ ".cmd");
    return buildPath(root, tool, ver, "bin", dumpName);
}

string projectInstalledPHPPath(string root, string ver) @safe pure
{
    if (currentOS() == "windows")
        return buildPath(root, "envs", "php", ver, "bin", "php.cmd");
    return buildPath(root, "envs", "php", ver, "bin", "php");
}

string projectInstalledPHPConfigPath(string root, string ver) @safe pure
{
    return buildPath(dirName(projectInstalledPHPPath(root, ver)), "php.ini");
}

string writeCachedPHP(string cacheDir, string ver, const(ubyte)[] script)
{
    auto phpPath = cachedPHPPath(cacheDir, ver);
    const(ubyte)[][string] files;
    files[cachedToolRelativePath(cacheDir, "php", ver, phpPath)] = script;
    files["extras/ssl/cacert.pem"] = cast(const(ubyte)[]) "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n";
    return writeCachedArchivePayload(cacheDir, "php", ver, files);
}

string writeCachedFrankenPHP(string cacheDir, string ver)
{
    const(ubyte)[][string] files;
    if (currentOS() == "windows")
    {
        files["frankenphp.exe"] = cast(const(ubyte)[]) "placeholder\n";
        files["php.cmd"] = cast(const(ubyte)[]) "@echo off\r\necho fake-frankenphp-php %*\r\n";
    }
    else
    {
        files["frankenphp"] = cast(const(ubyte)[]) "#!/bin/sh\nif [ \"$1\" = \"php-cli\" ]; then\n  shift\n  printf 'fake-frankenphp-php %s\\n' \"$*\"\n  exit 0\nfi\nexit 1\n";
    }
    return writeCachedArchivePayload(cacheDir, "frankenphp", ver, files);
}

string writeCachedComposer(string cacheDir, string ver, const(ubyte)[] data)
{
    return writeCachedFilePayload(cacheDir, "composer", ver, "composer.phar", "bin/composer.phar", data);
}

string writeCachedComposerExecutable(string cacheDir, string ver, const(ubyte)[] data)
{
    auto installPath = cachedToolRelativePath(cacheDir, "composer", ver, cachedComposerExecutablePath(cacheDir, ver));
    return writeCachedFilePayload(cacheDir, "composer", ver, baseName(installPath), installPath, data);
}

string writeCachedPIE(string cacheDir, string ver, const(ubyte)[] data)
{
    return writeCachedFilePayload(cacheDir, "pie", ver, "pie.phar", "bin/pie.phar", data);
}

string writeCachedDatabaseTool(
    string cacheDir,
    string tool,
    string ver,
    bool includeDump,
    bool includeServer,
    bool includeAdmin
)
{
    const(ubyte)[][string] files;
    files[cachedToolRelativePath(cacheDir, tool, ver, cachedDatabasePath(cacheDir, tool, ver))] = fakeDatabaseScript(tool);
    if (includeDump)
        files[cachedToolRelativePath(cacheDir, tool, ver, cachedDatabaseDumpPath(cacheDir, tool, ver))] = fakeDatabaseDumpScript();
    if (includeServer)
    {
        auto serverName = tool == "mariadb" ? "mariadbd" : "mysqld";
        files[cachedToolRelativePath(cacheDir, tool, ver, cachedDatabaseServerPath(cacheDir, tool, ver))] = fakeDatabaseScript(serverName);
    }
    if (includeAdmin)
    {
        auto adminName = tool == "mariadb" ? "mariadb-admin" : "mysqladmin";
        files[cachedToolRelativePath(cacheDir, tool, ver, cachedDatabaseAdminPath(cacheDir, tool, ver))] = fakeDatabaseScript(adminName);
    }
    return writeCachedArchivePayload(cacheDir, tool, ver, files);
}

struct TestCacheVersionMeta
{
    string downloadedVersion;
    string payloadKind;
    string payloadPath;
    string fileName;
    string installPath;
    string sourceURL;
    string archiveFormat;
    string checksumAlgorithm;
    string checksum;
    long size = 0;
}

string writeCachedArchivePayload(string cacheDir, string tool, string ver, const(ubyte)[][string] files)
{
    auto fileName = tool ~ "-" ~ ver ~ "-test.zip";
    string payloadRel;
    auto payloadPath = cachedPayloadPath(cacheDir, tool, ver, fileName, payloadRel);

    auto paths = files.keys;
    paths.sort();
    ArchiveEntrySpec[] specs;
    specs.reserve(paths.length);
    foreach (p; paths)
        specs ~= ArchiveEntrySpec(toSlashPath(p), files[p], octal!755);

    auto archiveData = buildZipBytes(specs);
    mkdirRecurse(dirName(payloadPath));
    write(payloadPath, archiveData);

    TestCacheVersionMeta meta;
    meta.downloadedVersion = ver;
    meta.payloadKind = "archive";
    meta.payloadPath = payloadRel;
    meta.fileName = fileName;
    meta.sourceURL = "https://example.test/" ~ fileName;
    meta.archiveFormat = "zip";
    meta.checksumAlgorithm = "sha256";
    meta.checksum = sha256Hex(archiveData);
    meta.size = cast(long) archiveData.length;
    writeTestCacheMetadata(cacheDir, tool, ver, meta);
    return payloadPath;
}

string writeCachedFilePayload(
    string cacheDir,
    string tool,
    string ver,
    string fileName,
    string installPath,
    const(ubyte)[] data
)
{
    string payloadRel;
    auto payloadPath = cachedPayloadPath(cacheDir, tool, ver, fileName, payloadRel);
    mkdirRecurse(dirName(payloadPath));
    write(payloadPath, data);
    setFileMode(payloadPath, octal!755);

    TestCacheVersionMeta meta;
    meta.downloadedVersion = ver;
    meta.payloadKind = "file";
    meta.payloadPath = payloadRel;
    meta.fileName = fileName;
    meta.installPath = toSlashPath(installPath);
    meta.sourceURL = "https://example.test/" ~ fileName;
    meta.checksumAlgorithm = "sha256";
    meta.checksum = sha256Hex(data);
    meta.size = cast(long) data.length;
    writeTestCacheMetadata(cacheDir, tool, ver, meta);
    return payloadPath;
}

private string cachedPayloadPath(string cacheDir, string tool, string ver, string fileName, out string relPath) @safe pure
{
    relPath = toSlashPath(buildPath(ver, fileName));
    return buildPath(cacheDir, tool, fromSlashPath(relPath));
}

string cachedToolRelativePath(string cacheDir, string tool, string ver, string path) @safe
{
    return toSlashPath(relativePath(path, buildPath(cacheDir, tool, ver)));
}

private void writeTestCacheMetadata(string cacheDir, string tool, string ver, TestCacheVersionMeta entry)
{
    auto metadataPath = buildPath(cacheDir, tool, "metadata.json");
    JSONValue versions = JSONValue.emptyObject;
    if (exists(metadataPath))
    {
        auto existing = parseJSON(readText(metadataPath));
        if (auto v = "versions" in existing)
        {
            if (v.type == JSONType.object)
                versions = *v;
        }
    }

    JSONValue obj = JSONValue.emptyObject;
    obj["downloadedVersion"] = entry.downloadedVersion;
    obj["payloadKind"] = entry.payloadKind;
    obj["payloadPath"] = entry.payloadPath;
    obj["fileName"] = entry.fileName;
    if (entry.installPath.length > 0)
        obj["installPath"] = entry.installPath;
    obj["sourceUrl"] = entry.sourceURL;
    if (entry.archiveFormat.length > 0)
        obj["archiveFormat"] = entry.archiveFormat;
    obj["checksumAlgorithm"] = entry.checksumAlgorithm;
    obj["checksum"] = entry.checksum;
    obj["size"] = entry.size;
    obj["downloadedAt"] = "2026-01-01T00:00:00Z";
    versions[ver] = obj;

    JSONValue root = JSONValue.emptyObject;
    root["schemaVersion"] = 1;
    root["tool"] = tool;
    root["versions"] = versions;

    mkdirRecurse(dirName(metadataPath));
    write(metadataPath, root.toPrettyString() ~ "\n");
}

const(ubyte)[] fakePHPScript() @safe pure
{
    if (currentOS() == "windows")
        return cast(const(ubyte)[]) "@echo off\r\necho fake-php %*\r\n";
    return cast(const(ubyte)[]) "#!/usr/bin/env sh\nprintf 'fake-php %s\n' \"$*\"\n";
}

const(ubyte)[] fakePHPScriptWithEnv(string varName) @safe pure
{
    if (currentOS() == "windows")
        return cast(const(ubyte)[]) ("@echo off\r\necho fake-php %* %" ~ varName ~ "%\r\n");
    return cast(const(ubyte)[]) ("#!/usr/bin/env sh\nprintf 'fake-php %s %s\n' \"$*\" \"$" ~ varName ~ "\"\n");
}

const(ubyte)[] fakeToolScript(string name) @safe pure
{
    if (currentOS() == "windows")
        return cast(const(ubyte)[]) ("@echo off\r\necho fake-" ~ name ~ " %*\r\n");
    return cast(const(ubyte)[]) ("#!/usr/bin/env sh\nprintf 'fake-" ~ name ~ " %s\n' \"$*\"\n");
}

const(ubyte)[] fakeCakePHPCreateProjectComposerScript(int exitCode = 0) @safe pure
{
    auto appLocal = "<?php\n" ~
        "declare(strict_types=1);\n\n" ~
        "return [\n" ~
        "    'Datasources' => [\n" ~
        "        'default' => [\n" ~
        "            'driver' => 'Cake\\\\Database\\\\Driver\\\\Sqlite',\n" ~
        "            'database' => 'tmp/test.sqlite',\n" ~
        "        ],\n" ~
        "    ],\n" ~
        "];\n";
    if (currentOS() == "windows")
    {
        return cast(const(ubyte)[]) (
            "@echo off\r\n" ~
            "set \"dir=%POLKA_TEST_CREATE_PROJECT_DIR%\"\r\n" ~
            "mkdir \"%dir%\\config\" >nul 2>nul\r\n" ~
            "mkdir \"%dir%\\webroot\" >nul 2>nul\r\n" ~
            "> \"%dir%\\config\\app_local.php\" echo ^<?php\r\n" ~
            ">> \"%dir%\\config\\app_local.php\" echo declare^(strict_types=1^);\r\n" ~
            ">> \"%dir%\\config\\app_local.php\" echo(\r\n" ~
            ">> \"%dir%\\config\\app_local.php\" echo return [\r\n" ~
            ">> \"%dir%\\config\\app_local.php\" echo     'Datasources' =^> [\r\n" ~
            ">> \"%dir%\\config\\app_local.php\" echo         'default' =^> [\r\n" ~
            ">> \"%dir%\\config\\app_local.php\" echo             'driver' =^> 'Cake\\Database\\Driver\\Sqlite',\r\n" ~
            ">> \"%dir%\\config\\app_local.php\" echo             'database' =^> 'tmp/test.sqlite',\r\n" ~
            ">> \"%dir%\\config\\app_local.php\" echo         ],\r\n" ~
            ">> \"%dir%\\config\\app_local.php\" echo     ],\r\n" ~
            ">> \"%dir%\\config\\app_local.php\" echo ];\r\n" ~
            "echo fake-composer %*\r\n" ~
            "exit /b " ~ to!string(exitCode) ~ "\r\n"
        );
    }
    return cast(const(ubyte)[]) (
        "#!/usr/bin/env sh\nset -eu\ndir=${POLKA_TEST_CREATE_PROJECT_DIR:?}\nmkdir -p \"$dir/config\" \"$dir/webroot\"\n" ~
        "cat > \"$dir/config/app_local.php\" <<'EOF'\n" ~ appLocal ~ "EOF\n" ~
        "printf 'fake-composer %s\n' \"$*\"\nexit " ~ to!string(exitCode) ~ "\n"
    );
}

void assertCakePHPAppLocalUsesManagedDatabase(string path, string port)
{
    auto text = readText(path);
    foreach (expected; [
        `'default' => [`,
        `'test' => [`,
        `'debug_kit' => [`,
        `'className' => 'Cake\\Database\\Connection',`,
        `'driver' => 'Cake\\Database\\Driver\\Mysql',`,
        `'host' => '127.0.0.1',`,
        `'port' => '` ~ port ~ `',`,
        `'username' => 'polka',`,
        `'database' => 'default',`,
        `'encoding' => 'utf8mb4',`,
        `'url' => null,`,
    ])
    {
        assert(text.canFind(expected), format("app_local.php missing %s in %s", expected, text));
    }
    assert(!text.canFind("Sqlite") && !text.canFind("sqlite://"));
}

const(ubyte)[] fakeDatabaseScript(string name) @safe pure
{
    if (currentOS() == "windows")
        return cast(const(ubyte)[]) ("@echo off\r\necho fake-" ~ name ~ " %*\r\n");
    return cast(const(ubyte)[]) ("#!/usr/bin/env sh\nprintf 'fake-" ~ name ~ " %s\n' \"$*\"\n");
}

const(ubyte)[] fakeDatabaseCaptureScript(string name) @safe pure
{
    if (currentOS() == "windows")
    {
        return cast(const(ubyte)[]) (
            "@echo off\r\necho fake-" ~ name ~ " %*\r\n" ~
            `powershell -NoProfile -Command "[IO.File]::WriteAllText($env:POLKA_TEST_DB_CAPTURE_PATH, [Console]::In.ReadToEnd())"` ~ "\r\n"
        );
    }
    return cast(const(ubyte)[]) ("#!/usr/bin/env sh\nprintf 'fake-" ~ name ~ " %s\n' \"$*\"\ncat > \"$POLKA_TEST_DB_CAPTURE_PATH\"\n");
}

const(ubyte)[] fakeDatabaseDumpScript() @safe pure
{
    if (currentOS() == "windows")
    {
        return cast(const(ubyte)[]) (
            "@echo off\r\n" ~
            `if not "%POLKA_TEST_DB_DUMP_CAPTURE_PATH%"=="" echo %* > "%POLKA_TEST_DB_DUMP_CAPTURE_PATH%"` ~ "\r\n" ~
            `powershell -NoProfile -Command "[Console]::Out.Write($env:POLKA_TEST_DB_DUMP_OUTPUT)"` ~ "\r\n"
        );
    }
    return cast(const(ubyte)[]) (
        "#!/usr/bin/env sh\n" ~
        "if [ -n \"$POLKA_TEST_DB_DUMP_CAPTURE_PATH\" ]; then\n  printf '%s\n' \"$*\" > \"$POLKA_TEST_DB_DUMP_CAPTURE_PATH\"\nfi\n" ~
        "printf '%s' \"$POLKA_TEST_DB_DUMP_OUTPUT\"\n"
    );
}

void assertEncryptedTLSKeyFile(string path)
{
    auto text = readText(path);
    assert(!text.canFind("-----BEGIN RSA PRIVATE KEY-----"), format("%s = %s, want encrypted key", path, text));
    assert(text.canFind("POLKA ENCRYPTED RSA PRIVATE KEY"), format("%s = %s, want encrypted key PEM type", path, text));
}

void assertPlaintextTLSKeyFile(string path)
{
    auto text = readText(path);
    assert(text.canFind("-----BEGIN RSA PRIVATE KEY-----"), format("%s = %s, want plaintext RSA PRIVATE KEY", path, text));
}

/// Builds a fake PHP binary for PIE extension tests (`-nm`, `-m`, and pie.phar logging).
const(ubyte)[] fakePIEHostPHPScript(
    const(string)[] builtinModules,
    const(string)[] loadedModules,
    string extraLine = ""
) @safe pure
{
    import std.array : join;

    if (currentOS() == "windows")
    {
        string[] lines = [
            "@echo off",
            `if not "%POLKA_TEST_PHP_CAPTURE_PATH%"=="" echo %* >> "%POLKA_TEST_PHP_CAPTURE_PATH%"`,
            `if "%1"=="-nm" goto builtin`,
            `if "%1"=="-m" goto loaded`,
            "echo fake-php %*",
        ];
        if (extraLine.length > 0)
            lines ~= "echo " ~ extraLine;
        lines ~= ["exit /b 0", ":builtin"];
        foreach (m; builtinModules)
            lines ~= "echo " ~ m;
        lines ~= ["exit /b 0", ":loaded"];
        foreach (m; loadedModules)
            lines ~= "echo " ~ m;
        lines ~= ["exit /b 0", ""];
        return cast(const(ubyte)[]) lines.join("\r\n");
    }

    string[] script = [
        "#!/usr/bin/env sh",
        `if [ -n "${POLKA_TEST_PHP_CAPTURE_PATH:-}" ]; then printf '%s\n' "$*" >> "$POLKA_TEST_PHP_CAPTURE_PATH"; fi`,
        `if [ "$1" = "-nm" ]; then`,
    ];
    foreach (m; builtinModules)
        script ~= "  printf '" ~ m ~ "\\n'";
    script ~= ["  exit 0", "fi", `if [ "$1" = "-m" ]; then`];
    foreach (m; loadedModules)
        script ~= "  printf '" ~ m ~ "\\n'";
    script ~= ["  exit 0", "fi", `printf 'fake-php %s\n' "$*"`];
    if (extraLine.length > 0)
        script ~= "printf '" ~ extraLine ~ "\\n'";
    script ~= ["exit 0", ""];
    return cast(const(ubyte)[]) script.join("\n");
}

/// Builds a fake internal PHP that answers `-nm`/`-m` normally but fails all
/// other invocations with `exitCode` (representing a failing Composer run).
const(ubyte)[] fakeFailingCreateProjectHostPHPScript(int exitCode) @safe pure
{
    import std.array : join;

    auto code = to!string(exitCode);
    if (currentOS() == "windows")
    {
        return cast(const(ubyte)[]) [
            "@echo off",
            `if "%1"=="-nm" exit /b 0`,
            `if "%1"=="-m" exit /b 0`,
            "echo composer failure 1>&2",
            "exit /b " ~ code,
            "",
        ].join("\r\n");
    }

    return cast(const(ubyte)[]) (
        "#!/usr/bin/env sh\n" ~
        `case "$1" in` ~ "\n" ~
        "-nm|-m) exit 0 ;;\n" ~
        "esac\n" ~
        "echo 'composer failure' >&2\n" ~
        "exit " ~ code ~ "\n"
    );
}

/// Builds a fake PHP driving the two-phase Laravel `create-project` lifecycle
/// (initial `--no-install --no-scripts` scaffold followed by full project execution).
const(ubyte)[] fakeLaravelCreateProjectLifecyclePHP() @safe pure
{
    import std.array : join;

    if (currentOS() == "windows")
    {
        return cast(const(ubyte)[]) [
            "@echo off",
            `if "%1"=="-nm" exit /b 0`,
            `if "%1"=="-m" exit /b 0`,
            `set "dir=%POLKA_TEST_CREATE_PROJECT_DIR%"`,
            `if not "%~2"=="create-project" exit /b 81`,
            `if "%~3"=="--no-install" goto scaffold`,
            `if not "%~3"=="--working-dir" exit /b 82`,
            `if not "%~4"=="%dir%" exit /b 82`,
            `if not "%PHPRC%"=="%POLKA_TEST_CREATE_PROJECT_INI%" exit /b 84`,
            `if not exist "%POLKA_TEST_CREATE_PROJECT_NODE%" exit /b 85`,
            `if not exist "%dir%\.polka\envs\installed.json" exit /b 86`,
            `echo ;%PATH%; | findstr /C:";%dir%\.polka\bin;" >nul || exit /b 87`,
            `set "skipScripts=false"`,
            `set "skipInstall=false"`,
            `for %%a in (%*) do if "%%~a"=="--no-scripts" set "skipScripts=true"`,
            `for %%a in (%*) do if "%%~a"=="--no-install" set "skipInstall=true"`,
            `if not "%skipScripts%"=="%POLKA_TEST_CREATE_PROJECT_SKIP_SCRIPTS%" exit /b 88`,
            `if not "%skipInstall%"=="%POLKA_TEST_CREATE_PROJECT_SKIP_INSTALL%" exit /b 89`,
            `if not "%DB_CONNECTION%"=="mysql" exit /b 91`,
            `if not "%DB_HOST%"=="127.0.0.1" exit /b 92`,
            `if not "%DB_PORT%"=="3306" exit /b 92`,
            `if not "%DB_DATABASE%"=="default" exit /b 92`,
            `if not "%DB_USERNAME%"=="polka" exit /b 92`,
            `if "%DB_PASSWORD%"=="" exit /b 92`,
            `if not exist "%dir%\.env" exit /b 93`,
            `if "%skipScripts%"=="false" if not exist "%dir%\.polka\run\db\default.json" exit /b 94`,
            `copy /y "%dir%\.env" "%dir%\migration.env" >nul || exit /b 95`,
            `if "%skipScripts%"=="false" echo done>"%dir%\scripts-ran"`,
            `if "%skipInstall%"=="false" echo done>"%dir%\dependencies-installed"`,
            `echo complete>>"%dir%\lifecycle.log"`,
            "exit /b %POLKA_TEST_CREATE_PROJECT_EXIT%",
            ":scaffold",
            `if not "%~4"=="--no-scripts" exit /b 90`,
            `mkdir "%dir%" >nul 2>nul`,
            `echo scaffold>"%dir%\lifecycle.log"`,
            `echo {}>"%dir%\composer.json"`,
            `echo php>"%dir%\artisan"`,
            `echo DB_CONNECTION=sqlite>"%dir%\.env.example"`,
            "exit /b 0",
            "",
        ].join("\r\n");
    }

    return cast(const(ubyte)[]) `#!/usr/bin/env sh
set -eu
case "$1" in -nm|-m) exit 0 ;; esac
dir=${POLKA_TEST_CREATE_PROJECT_DIR:?}
[ "$2" = create-project ] || exit 81
if [ "$3" = --no-install ]; then
    [ "$4" = --no-scripts ] || exit 90
    mkdir -p "$dir"
    echo scaffold > "$dir/lifecycle.log"
    echo '{}' > "$dir/composer.json"
    echo php > "$dir/artisan"
    echo DB_CONNECTION=sqlite > "$dir/.env.example"
    exit 0
fi
[ "$3" = --working-dir ] || exit 82
[ "$4" = "$dir" ] || exit 82
[ "$0" = "$POLKA_TEST_CREATE_PROJECT_PHP" ] || exit 83
[ "$PHPRC" = "$POLKA_TEST_CREATE_PROJECT_INI" ] || exit 84
[ -f "$POLKA_TEST_CREATE_PROJECT_NODE" ] || exit 85
[ -f "$dir/.polka/envs/installed.json" ] || exit 86
case "$PATH" in "$dir/.polka/bin:"*) ;; *) exit 87 ;; esac
skipScripts=false
skipInstall=false
for arg in "$@"; do
    case "$arg" in
        --no-scripts) skipScripts=true ;;
        --no-install) skipInstall=true ;;
    esac
done
[ "$skipScripts" = "$POLKA_TEST_CREATE_PROJECT_SKIP_SCRIPTS" ] || exit 88
[ "$skipInstall" = "$POLKA_TEST_CREATE_PROJECT_SKIP_INSTALL" ] || exit 89
[ "${DB_CONNECTION:-}" = mysql ] || exit 91
[ "${DB_HOST:-}" = 127.0.0.1 ] || exit 92
[ "${DB_PORT:-}" = 3306 ] || exit 92
[ "${DB_DATABASE:-}" = default ] || exit 92
[ "${DB_USERNAME:-}" = polka ] || exit 92
[ -n "${DB_PASSWORD:-}" ] || exit 92
[ -f "$dir/.env" ] || exit 93
if [ "$skipScripts" = false ]; then
    [ -f "$dir/.polka/run/db/default.json" ] || exit 94
fi
cp "$dir/.env" "$dir/migration.env"
if [ "$skipScripts" = false ]; then echo done > "$dir/scripts-ran"; fi
if [ "$skipInstall" = false ]; then echo done > "$dir/dependencies-installed"; fi
echo complete >> "$dir/lifecycle.log"
exit "$POLKA_TEST_CREATE_PROJECT_EXIT"
`;
}

/// Writes a fake `php` (or `php.cmd` on Windows) binary into `<root>/bin`.
void writeFakePHPInRoot(string root)
{
    auto binDir = buildPath(root, "bin");
    mkdirRecurse(binDir);
    auto phpPath = buildPath(binDir, currentOS() == "windows" ? "php.cmd" : "php");
    write(phpPath, fakePHPScript());
    setFileMode(phpPath, octal!755);
}
