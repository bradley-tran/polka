module polka.backend.store_tests;

// Integration and behavioral unit tests covering Store lifecycle, config
// preservation, tool/dependency downloads & installs, framework/preset init,
// internal tools, PIE/PECL integration, workers, and MSVC capture.

version (unittest):

import std.algorithm : canFind, sort, startsWith;
import std.array : appender;
import std.file : chdir, exists, getcwd, isDir, mkdirRecurse, read, readText, remove, write;
import std.format : format;
import std.json : JSONType, JSONValue, parseJSON;
import std.path : baseName, buildPath, dirName, extension, relativePath;
import std.process : environment, execute;
import std.string : indexOf, replace, strip, toLower;

import polka.backend.aliases;
import polka.backend.configure_value;
import polka.backend.database;
import polka.backend.internal_tools;
import polka.backend.msvc;
import polka.backend.pecl;
import polka.backend.pie;
import polka.backend.schema;
import polka.backend.state;
import polka.backend.store;
import polka.config;
import polka.support.archive : ArchiveEntrySpec, buildZipBytes;
import polka.support.crypto : sha256Hex;
import polka.support.fs : createTempDir, currentExecutablePath, currentOS, fromSlashPath, octal, rmdirRecurseQuiet, setFileMode, toSlashPath;
import polka.support.http : HTTPResponse, MockHTTPClient;
import polka.support.yaml : emitYAML, YamlValue;
import polka.tools;
import polka.tools.ids;

// ============================================================================
// Deterministic Test Doubles & Fixture Builders
// ============================================================================

/// Fake `ToolDownloader` wrapping a test delegate.
private final class FakeDownloader : ToolDownloader
{
    void delegate(string cacheDir, string tool, string ver) fn;

    this(void delegate(string cacheDir, string tool, string ver) fn)
    {
        this.fn = fn;
    }

    void download(string cacheDir, string tool, string ver)
    {
        fn(cacheDir, tool, ver);
    }
}

/// Synthetic plugin declaring logs on a separate dispatch command alias.
private final class LogAliasTestPlugin : ToolPlugin
{
    string id() const { return "alias-tool"; }
    string version_(const Environment) const { return "1.0"; }
    bool[string] phpExtensions() const { return null; }
    void validate(const Environment) const {}
    string[] installCandidates(string, string) const { return null; }
    string[] dispatchCommands() const { return ["alias-bin"]; }
    string[] cleanupCommands() const { return ["alias-bin"]; }
    string[] activeCommands(const Environment) const { return ["alias-bin"]; }
    string[] dispatchCandidates(string, string, string) const { return null; }
    LogEntry[] logs() const { return [LogEntry("default.log", LogLevelInfo)]; }
    void download(ToolDownloadContext) const {}
    void postInstall(ToolInstallContext) const {}
}

/// Synthetic `ToolPlugin` + `ToolSettingsProvider` testing custom plugin extensibility.
private final class FakeAcmePlugin : ToolPlugin, ToolSettingsProvider
{
    string id() const { return "acme"; }
    string version_(const Environment env) const { return env.toolVersion("acme"); }
    bool[string] phpExtensions() const { return null; }
    void validate(const Environment) const {}
    string[] installCandidates(string root, string ver) const { return [buildPath(root, "acme")]; }
    string[] dispatchCommands() const { return ["acme"]; }
    string[] cleanupCommands() const { return ["acme"]; }
    string[] activeCommands(const Environment) const { return ["acme"]; }
    string[] dispatchCandidates(string root, string exe, string ver) const { return [buildPath(root, "acme")]; }
    LogEntry[] logs() const { return null; }
    void download(ToolDownloadContext) const {}
    void postInstall(ToolInstallContext) const {}

    SettingType[string] settings() const
    {
        return [
            "port": SettingType.port,
            "token": SettingType.string_,
            "tls": SettingType.bool_,
        ];
    }

    string settingsNamespace() const { return "acme"; }
    string[] exclusiveWith() const { return null; }
}

private void assertPathExists(string path)
{
    assert(exists(path), format("expected path to exist: %s", path));
}

private void assertPathMissing(string path)
{
    assert(!exists(path), format("expected path to be missing: %s", path));
}

private string cachedPayloadPath(string cacheDir, string tool, string ver, string fileName, out string relPath)
{
    relPath = toSlashPath(buildPath(ver, fileName));
    return buildPath(cacheDir, tool, fromSlashPath(relPath));
}

private string cachedToolRelativePath(string cacheDir, string tool, string ver, string path)
{
    return toSlashPath(relativePath(path, buildPath(cacheDir, tool, ver)));
}

private struct TestCacheVersionMeta
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

private void writeTestCacheMetadata(string cacheDir, string tool, string ver, TestCacheVersionMeta entry)
{
    auto metadataPath = buildPath(cacheDir, tool, "metadata.json");
    JSONValue versions = JSONValue.emptyObject;
    if (exists(metadataPath))
    {
        auto existing = parseJSON(readText(metadataPath));
        if (auto p = "versions" in existing)
        {
            if (p.type == JSONType.object)
                versions = *p;
        }
    }

    JSONValue entryObj = JSONValue.emptyObject;
    entryObj["downloadedVersion"] = entry.downloadedVersion;
    entryObj["payloadKind"] = entry.payloadKind;
    entryObj["payloadPath"] = entry.payloadPath;
    entryObj["fileName"] = entry.fileName;
    if (entry.installPath.length > 0)
        entryObj["installPath"] = entry.installPath;
    entryObj["sourceUrl"] = entry.sourceURL;
    if (entry.archiveFormat.length > 0)
        entryObj["archiveFormat"] = entry.archiveFormat;
    entryObj["checksumAlgorithm"] = entry.checksumAlgorithm;
    entryObj["checksum"] = entry.checksum;
    entryObj["size"] = entry.size;
    entryObj["downloadedAt"] = "2026-01-01T00:00:00Z";
    versions[ver] = entryObj;

    JSONValue root = JSONValue.emptyObject;
    root["schemaVersion"] = 1;
    root["tool"] = tool;
    root["versions"] = versions;

    mkdirRecurse(dirName(metadataPath));
    write(metadataPath, root.toPrettyString() ~ "\n");
}

private string writeCachedArchivePayload(string cacheDir, string tool, string ver, const(ubyte)[][string] files)
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

private string writeCachedFilePayload(string cacheDir, string tool, string ver, string fileName, string installPath, const(ubyte)[] data)
{
    string payloadRel;
    auto payloadPath = cachedPayloadPath(cacheDir, tool, ver, fileName, payloadRel);
    mkdirRecurse(dirName(payloadPath));
    write(payloadPath, data);

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

private string cachedFakePHPPath(string cacheDir, string tool, string ver)
{
    auto candidates = toolInstallCandidatesIn(cacheDir, tool, ver);
    if (currentOS() == "windows")
    {
        foreach (candidate; candidates)
        {
            auto ext = extension(candidate).toLower();
            if (ext == ".cmd" || ext == ".bat")
                return candidate;
        }
    }
    return candidates[0];
}

private immutable(ubyte)[] fakePHPModuleListScript(const(string)[] modules)
{
    auto b = appender!string();
    if (currentOS() == "windows")
    {
        b.put("@echo off\r\n");
        b.put("if \"%1\"==\"-nm\" (\r\n");
        b.put("  echo [PHP Modules]\r\n");
        foreach (mod; modules)
        {
            b.put("  echo ");
            b.put(mod);
            b.put("\r\n");
        }
        b.put("  echo [Zend Modules]\r\n");
        b.put("  exit /b 0\r\n");
        b.put(")\r\n");
        b.put("echo fake-php %*\r\n");
        return cast(immutable(ubyte)[]) b.data;
    }

    b.put("#!/usr/bin/env sh\n");
    b.put("if [ \"$1\" = \"-nm\" ]; then\n");
    b.put("  printf '%s\\n' '[PHP Modules]'\n");
    foreach (mod; modules)
    {
        b.put("  printf '%s\\n' '");
        b.put(mod);
        b.put("'\n");
    }
    b.put("  printf '%s\\n' '[Zend Modules]'\n");
    b.put("  exit 0\n");
    b.put("fi\n");
    b.put("printf 'fake-php %s\\n' \"$*\"\n");
    return cast(immutable(ubyte)[]) b.data;
}

private immutable(ubyte)[] fakeFrankenPHPModuleListScript(const(string)[] modules)
{
    auto b = appender!string();
    b.put("#!/bin/sh\n");
    b.put("if [ \"$1\" = \"php-cli\" ]; then shift; fi\n");
    b.put("if [ \"$1\" = \"-nm\" ]; then\n");
    b.put("  printf '[PHP Modules]\\nCore\\n[Zend Modules]\\n'\n");
    foreach (mod; modules)
    {
        b.put("  printf '%s\\n' ");
        b.put(shellLiteral(mod));
        b.put("\n");
    }
    b.put("  exit 0\n");
    b.put("fi\n");
    b.put("exit 1\n");
    return cast(immutable(ubyte)[]) b.data;
}

private string cachedNodeJSCorepackPath(string root, string ver)
{
    if (currentOS() == "windows")
        return buildPath(root, toolNodeJS, ver, "corepack.cmd");
    return buildPath(root, toolNodeJS, ver, "bin", "corepack");
}

private string writeCachedNodeJSCommand(string cacheDir, string ver, string command)
{
    foreach (candidate; dispatchExecutableCandidatesIn(cacheDir, toolNodeJS, command, ver))
        return candidate;
    return buildPath(cacheDir, toolNodeJS, ver, command);
}

private string writeCachedTool(string cacheDir, string tool, string ver)
{
    auto path = toolInstallCandidatesIn(cacheDir, tool, ver)[0];
    if (tool == toolPHP || tool == toolPHPZTS)
        path = cachedFakePHPPath(cacheDir, tool, ver);

    auto relPath = cachedToolRelativePath(cacheDir, tool, ver, path);
    if (tool == toolPHP || tool == toolPHPZTS)
    {
        const(ubyte)[][string] files = [
            relPath: fakePHPModuleListScript(null),
            "extras/ssl/cacert.pem": cast(const(ubyte)[]) "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n",
        ];
        return writeCachedArchivePayload(cacheDir, tool, ver, files);
    }
    if (tool == toolComposer)
        return writeCachedFilePayload(cacheDir, tool, ver, "composer.phar", "bin/composer.phar", cast(const(ubyte)[]) "composer\n");
    if (tool == toolPIE)
        return writeCachedFilePayload(cacheDir, tool, ver, "pie.phar", "bin/pie.phar", cast(const(ubyte)[]) "pie\n");

    const(ubyte)[][string] files = [
        relPath: cast(const(ubyte)[]) "placeholder\n",
    ];
    if (tool == toolFrankenPHP)
    {
        auto phpName = "php";
        if (currentOS() == "windows")
            phpName = "php.cmd";
        else
            files[relPath] = fakeFrankenPHPModuleListScript(["Zend OPcache"]);
        files[phpName] = fakePHPModuleListScript(["Zend OPcache"]);
        files["extras/ssl/cacert.pem"] = cast(const(ubyte)[]) "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n";
    }
    if (tool == toolNodeJS)
    {
        foreach (cmd; [toolNode, toolNPM, toolNPX])
        {
            foreach (candidate; dispatchExecutableCandidatesIn(cacheDir, toolNodeJS, cmd, ver))
            {
                files[cachedToolRelativePath(cacheDir, toolNodeJS, ver, candidate)] = cast(const(ubyte)[])(cmd ~ "\n");
                break;
            }
        }
        files[cachedToolRelativePath(cacheDir, toolNodeJS, ver, cachedNodeJSCorepackPath(cacheDir, ver))] = cast(const(ubyte)[]) "corepack\n";
    }
    return writeCachedArchivePayload(cacheDir, tool, ver, files);
}

private string writeCachedPHPToolWithBuiltInModules(string cacheDir, string ver, const(string)[] modules)
{
    auto path = cachedFakePHPPath(cacheDir, toolPHP, ver);
    const(ubyte)[][string] files = [
        cachedToolRelativePath(cacheDir, toolPHP, ver, path): fakePHPModuleListScript(modules),
        "extras/ssl/cacert.pem": cast(const(ubyte)[]) "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n",
    ];
    return writeCachedArchivePayload(cacheDir, toolPHP, ver, files);
}

private void writeCachedInternalPHP(string cacheDir, string ver, const(string)[] modules...)
{
    auto phpPath = cachedFakePHPPath(cacheDir, toolPHP, ver);
    auto relPath = cachedToolRelativePath(cacheDir, toolPHP, ver, phpPath);
    const(ubyte)[][string] files = [
        relPath: fakePHPModuleListScript(null),
        "extras/ssl/cacert.pem": cast(const(ubyte)[]) "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n",
    ];
    foreach (name; modules)
    {
        auto fileName = currentOS() == "windows" ? ("php_" ~ name ~ ".dll") : (name ~ ".so");
        files["ext/" ~ fileName] = cast(const(ubyte)[]) "module\n";
    }
    writeCachedArchivePayload(cacheDir, toolPHP, ver, files);
}

// ============================================================================
// Store Core Tests (store_test.go parity)
// ============================================================================

unittest
{
    // TestStoreInitInstallsDispatcherShimsWithoutToolShims
    {
        auto projectDir = createTempDir("polka-st-init-");
        scope (exit) rmdirRecurseQuiet(projectDir);

        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        store.init();

        assertPathExists(store.rootDir);
        assertPathExists(store.binDir);
        assertPathExists(store.configFile);
        assertPathExists(buildPath(store.binDir, dispatcherBinaryName));
        assertPathExists(buildPath(store.binDir, dispatcherBatchFileName));

        foreach (cmd; [
            "php", toolComposer, toolPIE, toolNode, toolNPM, toolNPX,
            toolYarn, toolNodeJS, toolMago, toolMySQL, toolMariaDB,
        ])
        {
            assertPathMissing(buildPath(store.binDir, cmd));
            assertPathMissing(buildPath(store.binDir, cmd ~ ".cmd"));
        }

        auto configData = readText(store.configFile);
        assert(configData.canFind("root: .polka"));
        assert(configData.canFind("https: true"));
        assert(configData.canFind("hostname: " ~ projectLocalHostname(projectDir)));

        auto dispatcherShim = readText(buildPath(store.binDir, dispatcherBinaryName));
        auto selfPath = currentExecutablePath();
        assert(dispatcherShim.canFind("POLKA_DISPATCHER"));
        assert(dispatcherShim.canFind(toSlashPath(selfPath)));
    }

    // TestStoreInitWithOptionsWritesDocroot
    {
        auto projectDir = createTempDir("polka-st-doc1-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.initWithOptions(InitOptions(" public "));
        auto cfg = store.readConfig();
        assert(cfg.environments[defaultEnvironmentName].docroot == "public");
    }

    // TestStoreInitWithOptionsUpdatesExistingDocroot
    {
        auto projectDir = createTempDir("polka-st-doc2-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        auto cfg = store.defaultConfig();
        Environment def;
        def.docroot = "web";
        def.tools = [toolPHP: "8.4"];
        cfg.environments[defaultEnvironmentName] = def;
        store.writeConfig(cfg);

        store.initWithOptions(InitOptions("public"));
        auto loaded = store.readConfig();
        auto env = loaded.environments[defaultEnvironmentName];
        assert(env.docroot == "public" && env.toolVersion(toolPHP) == "8.4");
    }

    // TestStoreInitWithFrameworkOptionsOverridesDocroot
    {
        auto projectDir = createTempDir("polka-st-fwdoc-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.initWithFrameworkOptions("drupal", InitOptions(" drupal/web "));

        auto cfg = store.readConfig();
        auto env = cfg.environments[defaultEnvironmentName];
        assert(env.framework == "drupal" && env.docroot == "drupal/web");
        assert(env.toolVersion(toolComposer) == "2.8" && env.toolVersion(toolNodeJS) == "24" && env.toolVersion(toolNginx) == "1.30");
    }

    // TestStoreInitWithPHPExtensionPreset
    {
        auto tmpRoot = createTempDir("polka-st-extpre-");
        scope (exit) rmdirRecurseQuiet(tmpRoot);
        auto projectDir = buildPath(tmpRoot, "my-ext");
        mkdirRecurse(projectDir);
        auto store = newProjectStore(projectDir);

        auto result = store.initWithPresetOptions("php-extension", InitOptions.init);
        assert(result.scaffoldWritten == ["composer.json"] && result.scaffoldSkipped.length == 0);

        auto loaded = store.readConfig();
        auto env = loaded.environments[defaultEnvironmentName];
        assert(env.toolVersion(toolPHP) == "8.4" && env.toolVersion(toolComposer) == "2.8" && env.phpBuildTools());
        assert(env.memoryLimit == "-1");
        foreach (k, want; [
            "NO_INTERACTION": "1",
            "REPORT_EXIT_STATUS": "1",
            "USE_ZEND_ALLOC": "0",
            "ZEND_DONT_UNLOAD_MODULES": "1",
        ])
            assert(env.envVars.get(k, "") == want);

        assert(env.framework.length == 0 && env.docroot.length == 0 && !env.https && env.server is null && env.database is null);

        auto composer = readText(buildPath(projectDir, "composer.json"));
        foreach (want; [`"name": "vendor/my-ext"`, `"type": "php-ext"`, `"php": "^8.4"`, `"extension-name": "my_ext"`])
            assert(composer.canFind(want), composer);
    }

    // TestStorePHPExtensionPresetInstallRequests
    {
        auto projectDir = createTempDir("polka-st-extreq-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.initWithPresetOptions("php-extension", InitOptions.init);

        auto requests = store.installRequests(defaultEnvironmentName);
        string[string] versions;
        foreach (req; requests)
            versions[req.tool] = req.version_;
        assert(versions.get(toolPHP, "") == "8.4" && versions.get(toolComposer, "") == "2.8");
        if (currentOS() == "windows")
            assert(versions.get(toolPHPDevel, "") == "8.4" && versions.get(toolPHPSDK, "") == DefaultPHPSDKVersion);
        else
            assert(versions.get(toolPHPDevel, "") == "" && versions.get(toolPHPSDK, "") == "");
    }

    // TestStoreInitWithPHPExtensionPresetKeepsComposer
    {
        auto projectDir = createTempDir("polka-st-extkeep-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto composerPath = buildPath(projectDir, "composer.json");
        enum original = `{"name":"mine/keep"}`;
        write(composerPath, original);
        auto store = newProjectStore(projectDir);

        auto result = store.initWithPresetOptions("php-extension", InitOptions("", "acme/ignored"));
        assert(result.scaffoldSkipped == ["composer.json"] && result.scaffoldWritten.length == 0);
        assert(readText(composerPath) == original);
    }

    // TestStorePresetDefaults
    {
        auto projectDir = createTempDir("polka-st-predef-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        auto env = store.presetDefaults("php-extension");
        assert(env.phpBuildTools());

        bool threw = false;
        try { store.presetDefaults("unsupported-preset-id"); }
        catch (Exception e) { threw = e.msg.canFind("unsupported project preset"); }
        assert(threw);
    }

    // TestStorePresetInitRejectsExistingConfigBeforeCreatingState
    {
        auto projectDir = createTempDir("polka-st-preexist-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        write(store.configFile, "version: 0.2\nroot: .polka\n");

        bool threw = false;
        try { store.initWithPresetOptions("php-extension", InitOptions.init); }
        catch (Exception e) { threw = e.msg.canFind("already exists"); }
        assert(threw);
        assertPathMissing(store.rootDir);
        assertPathMissing(buildPath(projectDir, "composer.json"));
    }

    // TestStoreInitResolutionListsFrameworksAndPresets
    {
        auto projectDir = createTempDir("polka-st-initres-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        bool threw = false;
        try { store.initWithPresetOptions("yii", InitOptions.init); }
        catch (Exception e)
        {
            threw = e.msg.canFind("unsupported framework or preset")
                && e.msg.canFind("laravel")
                && e.msg.canFind("php-extension");
        }
        assert(threw);
    }

    // TestStoreFrameworkInitHasNoScaffold
    {
        auto projectDir = createTempDir("polka-st-fwnoscaf-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        auto result = store.initWithPresetOptions("laravel", InitOptions.init);
        assert(result.scaffoldWritten.length == 0 && result.scaffoldSkipped.length == 0);
        assertPathMissing(buildPath(projectDir, "composer.json"));
    }

    // TestValidatePresetScaffoldTargetRejectsInitState
    {
        foreach (target; ["polka.yaml", "polka.dev.yaml", ".polka/data"])
        {
            bool threw = false;
            try { validatePresetScaffoldTarget(target); }
            catch (Exception e) { threw = e.msg.canFind("reserved for init"); }
            assert(threw, target);
        }
    }

    // TestPHPBuildToolPathEntries & TestPHPRuntimePrefix
    {
        auto projectDir = createTempDir("polka-st-sdkpaths-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        Environment nts;
        nts.tools = [toolPHP: "8.4"];
        nts.setSettingBool(toolPHP, "extension-sdk", true);

        Environment zts;
        zts.tools = [toolPHPZTS: "8.4"];
        zts.setSettingBool(toolPHP, "extension-sdk", true);

        auto paths = store.phpBuildToolPathEntries(nts);
        if (currentOS() != "windows")
        {
            assert(paths.length == 0);
            assert(store.phpRuntimePrefix(nts).length == 0);
        }
        else
        {
            assert(paths == [
                buildPath(store.envsDir, toolPHPDevel, "8.4"),
                buildPath(store.envsDir, toolPHPSDK, DefaultPHPSDKVersion),
                buildPath(store.envsDir, toolPHPSDK, DefaultPHPSDKVersion, "msys2", "usr", "bin"),
            ]);
            assert(store.phpRuntimePrefix(nts) == buildPath(store.envsDir, toolPHP, "8.4"));
            assert(store.phpRuntimePrefix(zts) == buildPath(store.envsDir, toolPHPZTS, "8.4"));
            Environment noSDK;
            noSDK.tools = [toolPHP: "8.4"];
            assert(store.phpRuntimePrefix(noSDK).length == 0);
        }
    }

    // TestProjectLocalHostnameNormalizesDirectoryName
    {
        auto projectDir = buildPath("/tmp", "My Demo_Project");
        assert(projectLocalHostname(projectDir) == "my-demo-project.localhost");
    }

    // TestStoreUseSyncsManagedBinariesForCurrentEnvironment
    {
        auto projectDir = createTempDir("polka-st-usebin-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");

        store.configure("php-only", "8.4");
        auto dbCfg = new DatabaseConfig(toolMariaDB, "11.4");
        store.configure("db-only", "", "", "", dbCfg);

        store.use("php-only");
        assertPathExists(buildPath(store.binDir, toolPHP));
        assertPathExists(buildPath(store.binDir, toolPHP ~ ".cmd"));
        foreach (cmd; [toolComposer, toolPIE, toolNode, toolNPM, toolNPX, toolYarn, toolMago, toolMySQL, toolMariaDB])
        {
            assertPathMissing(buildPath(store.binDir, cmd));
            assertPathMissing(buildPath(store.binDir, cmd ~ ".cmd"));
        }

        store.use("db-only");
        assertPathMissing(buildPath(store.binDir, toolPHP));
        assertPathMissing(buildPath(store.binDir, toolPHP ~ ".cmd"));
        assertPathExists(buildPath(store.binDir, toolMariaDB));
        assertPathExists(buildPath(store.binDir, toolMariaDB ~ ".cmd"));
        assertPathMissing(buildPath(store.binDir, toolMySQL));
        assertPathMissing(buildPath(store.binDir, toolMySQL ~ ".cmd"));
    }

    // TestStoreUseStoresActiveEnvironmentOutsideConfig
    {
        auto projectDir = createTempDir("polka-st-useact-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        write(store.configFile, "version: 0.2\nroot: .polka\ntools:\n  php: \"8.4\"\n");
        write(store.environmentConfigFile("web"), "tools:\n  php: \"8.3\"\n");
        store.init();
        store.use("web");

        assert(!readText(store.configFile).canFind("current:"));
        assert(readText(store.activeEnvironmentPath()).strip() == "web");
    }

    // TestStoreUseDefaultClearsActiveEnvironmentOverride
    {
        auto projectDir = createTempDir("polka-st-usedef-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.configure(defaultEnvironmentName, "8.4");
        store.configure("demo", "8.3");
        store.use("demo");
        store.use(defaultEnvironmentName);

        assertPathMissing(store.activeEnvironmentPath());
        auto cur = store.current();
        assert(cur !is null && cur.name == defaultEnvironmentName && cur.toolVersion(toolPHP) == "8.4");
    }

    // TestStoreRejectsLegacyEnvironmentsConfig
    {
        auto projectDir = createTempDir("polka-st-leg-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        write(store.configFile, "version: 0.2\nroot: .polka\nenvironments:\n  demo:\n    php: \"8.4\"\n");

        bool threw = false;
        try { store.list(); }
        catch (Exception e) { threw = e.msg.canFind("legacy environments config"); }
        assert(threw);
    }

    // TestStoreReadsAndWritesFrameworkConfig
    {
        auto projectDir = createTempDir("polka-st-fwcfg-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        auto cfg = store.defaultConfig();
        Environment def;
        def.framework = "Drupal";
        def.tools = [toolPHP: "8.4"];
        Environment app;
        app.framework = "Laravel";
        app.tools = [toolPHP: "8.4"];
        cfg.environments[defaultEnvironmentName] = def;
        cfg.environments["app"] = app;
        store.writeConfig(cfg);

        assert(readText(store.configFile).canFind("framework: drupal"));
        assert(readText(store.environmentConfigFile("app")).canFind("framework: laravel"));

        auto loaded = store.readConfig();
        assert(loaded.environments[defaultEnvironmentName].framework == "drupal");
        assert(loaded.environments["app"].framework == "laravel");
    }

    // TestStoreReadsAndWritesOPcacheConfig
    {
        auto projectDir = createTempDir("polka-st-opcfg-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        auto cfg = store.defaultConfig();
        Environment def;
        def.tools = [toolPHP: "8.4", toolComposer: "2.8"];
        def.memoryLimit = " 512m ";
        def.opcachePreset = "Production";
        def.opcacheConfig = [" OPcache.Revalidate_Freq ": " 2 "];
        def.phpExtensions = ["opcache": true];

        Environment app;
        app.tools = [toolPHP: "8.3"];
        app.memoryLimit = " -1 ";
        app.opcachePreset = "Dev";
        app.opcacheConfig = [" OPcache.Enable_Cli ": " true "];

        cfg.environments[defaultEnvironmentName] = def;
        cfg.environments["app"] = app;
        store.writeConfig(cfg);

        auto projectCfg = readText(store.configFile);
        assert(projectCfg.canFind("opcache-preset: production") && !projectCfg.canFind("Production"));
        assert(projectCfg.canFind("memory-limit: 512M"));

        auto appCfg = readText(store.environmentConfigFile("app"));
        assert(appCfg.canFind("opcache-preset: dev") && !appCfg.canFind("OPcache.Enable_Cli"));
        assert(appCfg.canFind(`memory-limit: "-1"`), appCfg);

        auto loaded = store.readConfig();
        assert(loaded.environments[defaultEnvironmentName].opcachePreset == "production");
        assert(loaded.environments[defaultEnvironmentName].memoryLimit == "512M");
        assert(loaded.environments[defaultEnvironmentName].opcacheConfig["opcache.revalidate_freq"] == "2");
        assert(loaded.environments["app"].opcachePreset == "dev");
        assert(loaded.environments["app"].opcacheConfig["opcache.enable_cli"] == "true");
        assert(loaded.environments["app"].memoryLimit == "-1");
    }

    // TestStoreRejectsUnknownFrameworkConfig
    {
        auto projectDir = createTempDir("polka-st-badfw-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        write(store.configFile, "version: 0.2\nroot: .polka\nframework: yii\n");

        bool threw = false;
        try { store.readConfig(); }
        catch (Exception e)
        {
            threw = e.msg.canFind("unsupported framework")
                && e.msg.canFind("cakephp, codeigniter, drupal, laravel, symfony, wordpress");
        }
        assert(threw);
    }

    // TestStoreRemoveRejectsDefaultEnvironment
    {
        auto projectDir = createTempDir("polka-st-rmdef-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.init();

        bool threw = false;
        try { store.remove(defaultEnvironmentName); }
        catch (Exception e) { threw = e.msg.canFind("cannot be removed"); }
        assert(threw);
    }
}

unittest
{
    // TestStoreInstallCopiesToolIntoVersionedLayout
    {
        auto projectDir = createTempDir("polka-st-instphp-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        auto cachePHP = writeCachedTool(store.cacheDir, toolPHP, "8.4");

        store.configure("demo", "8.4");
        auto results = store.install("demo");
        assert(results.length == 1);
        auto r = results[0];
        assert(r.tool == toolPHP && r.version_ == "8.4" && !r.downloaded && r.cachePath == cachePHP);
        assertPathExists(r.targetPath);
        assert(r.targetPath.canFind(buildPath("envs", "php", "8.4")));
        assertPathExists(buildPath(store.envsDir, toolPHP, "8.4", fromSlashPath("extras/ssl/openssl.cnf")));
    }

    // TestStoreInstallToolCopiesExplicitVersionIntoLayout
    {
        auto projectDir = createTempDir("polka-st-insttool-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        auto cachePHP = writeCachedTool(store.cacheDir, toolPHP, "8.4");

        auto r = store.installTool(defaultEnvironmentName, toolPHP, "8.4");
        assert(r.tool == toolPHP && r.version_ == "8.4" && !r.downloaded && r.cachePath == cachePHP);
        assertPathExists(r.targetPath);
        assert(r.targetPath.canFind(buildPath("envs", toolPHP, "8.4")));
        assert(store.readConfig().environments[defaultEnvironmentName].toolVersion(toolPHP) == "8.4");
    }

    // TestStoreInstallsAndConfiguresFrankenPHP
    {
        auto projectDir = createTempDir("polka-st-franken-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedTool(store.cacheDir, toolFrankenPHP, "1.12");

        auto cfg = store.defaultConfig();
        cfg.environments["demo"] = Environment("demo");
        store.writeConfig(cfg);

        auto r = store.installTool("demo", toolFrankenPHP, "1.12");
        assert(r.tool == toolFrankenPHP && r.targetPath.canFind(buildPath("frankenphp", "1.12")));
        store.use("demo");

        assert(store.resolveTool(toolFrankenPHP) == r.targetPath);
        auto phpTarget = store.resolveTool(toolPHP);
        assert(dirName(phpTarget) == dirName(r.targetPath));
        assertPathExists(buildPath(store.binDir, "php"));
        assertPathExists(buildPath(store.binDir, "php.cmd"));
        assertPathExists(buildPath(store.envsDir, toolFrankenPHP, "1.12", fromSlashPath("extras/ssl/openssl.cnf")));

        auto env = store.configureValue("demo", "server.type", " FrankenPHP ");
        assert(env.toolVersion(toolFrankenPHP) == "1.12" && env.server !is null && env.server.type == "frankenphp");

        bool threw = false;
        try { store.configureValue("demo", "server.type", "caddy"); }
        catch (Exception e) { threw = e.msg.canFind("unsupported server type"); }
        assert(threw);
    }

    // TestStoreInstallConfiguresFrankenPHPForFrameworkWithoutStandalonePHP
    {
        auto projectDir = createTempDir("polka-st-frfw-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedTool(store.cacheDir, toolFrankenPHP, "1.12");

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.framework = "laravel";
        demo.tools = [toolFrankenPHP: "1.12"];
        demo.opcachePreset = "dev";
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto r = store.installTool("demo", toolFrankenPHP, "1.12");
        auto ini = readText(buildPath(dirName(r.targetPath), "php.ini"));
        foreach (d; ["extension=openssl", "extension=mbstring", "opcache.enable=1"])
            assert(ini.canFind(d), ini);
    }

    // TestStoreInstallToolSwitchesToPHPZTSAndResolvesPHP
    {
        auto projectDir = createTempDir("polka-st-zts1-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedTool(store.cacheDir, toolPHPZTS, "8.4");

        auto cfg = store.defaultConfig();
        Environment def;
        def.tools = [toolPHP: "8.3"];
        cfg.environments[defaultEnvironmentName] = def;
        store.writeConfig(cfg);

        auto r = store.installTool(defaultEnvironmentName, toolPHPZTS, "8.4");
        assert(r.tool == toolPHPZTS && r.targetPath.canFind(buildPath("php-zts", "8.4")));
        auto env = store.current();
        assert(env.toolVersion(toolPHP) == "" && env.toolVersion(toolPHPZTS) == "8.4");
        assert(store.resolveTool("php") == r.targetPath);
        assertPathExists(buildPath(store.binDir, "php"));
        assertPathExists(buildPath(store.binDir, "php.cmd"));
        assertPathExists(buildPath(store.envsDir, toolPHPZTS, "8.4", fromSlashPath("extras/ssl/openssl.cnf")));
    }

    // TestStoreInstallToolConfiguresPHPZTSInItsOwnLayout
    {
        auto projectDir = createTempDir("polka-st-zts2-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedTool(store.cacheDir, toolPHPZTS, "8.4");

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.phpExtensions = ["xdebug": false];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto r = store.installTool("demo", toolPHPZTS, "8.4");
        auto phpIniPath = buildPath(dirName(r.targetPath), "php.ini");
        auto ini = readText(phpIniPath);
        assert(ini.canFind(";zend_extension=xdebug"));
        assert(phpIniPath.canFind(buildPath("php-zts", "8.4")));
    }

    // TestStoreConfigureValueSwitchesPrimaryPHPRuntime
    {
        auto projectDir = createTempDir("polka-st-swphp-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [toolPHP: "8.3"];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto env = store.configureValue("demo", "tools.php-zts", "8.4");
        assert(env.toolVersion(toolPHP) == "" && env.toolVersion(toolPHPZTS) == "8.4");
        env = store.configureValue("demo", "tools.php", "8.2");
        assert(env.toolVersion(toolPHP) == "8.2" && env.toolVersion(toolPHPZTS) == "");
    }

    // TestStoreInstallToolAppliesEnvironmentPostInstallSettings
    {
        auto projectDir = createTempDir("polka-st-postset-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedTool(store.cacheDir, toolPHP, "8.4");
        mkdirRecurse(buildPath(store.cacheDir, toolPHP, "8.4", "ext"));

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.memoryLimit = "512m";
        demo.phpExtensions = ["openssl": true, "xdebug": false];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto r = store.installTool("demo", toolPHP, "8.4");
        auto ini = readText(buildPath(dirName(r.targetPath), "php.ini"));
        assert(ini.canFind("extension=openssl"));
        assert(ini.canFind("memory_limit=512M"));
        assert(ini.canFind("curl.cainfo=") && ini.canFind("openssl.cafile="));
        assert(ini.canFind(";zend_extension=xdebug"));
        assert(store.readConfig().environments["demo"].toolVersion(toolPHP) == "8.4");
    }

    // TestStoreInstallToolSkipsBuiltInPHPExtensions
    {
        auto projectDir = createTempDir("polka-st-builtin-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedPHPToolWithBuiltInModules(store.cacheDir, "8.4", ["OpenSSL", "zip", "Zend OPcache"]);
        mkdirRecurse(buildPath(store.cacheDir, toolPHP, "8.4", "ext"));

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.phpExtensions = ["opcache": true, "openssl": true, "xdebug": false, "zip": true];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto r = store.installTool("demo", toolPHP, "8.4");
        auto ini = readText(buildPath(dirName(r.targetPath), "php.ini"));
        foreach (skipped; ["extension=openssl", "extension=zip", "zend_extension=opcache"])
            assert(!ini.canFind(skipped), ini);
        assert(ini.canFind(";zend_extension=xdebug"));
    }

    // TestStoreInstallAppliesOPcachePresetFrameworkAndUserConfig
    {
        auto projectDir = createTempDir("polka-st-opfull-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedTool(store.cacheDir, toolPHP, "8.4");

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.framework = "drupal";
        demo.tools = [toolPHP: "8.4"];
        demo.opcachePreset = "production";
        demo.opcacheConfig = ["opcache.enable_cli": "1", "opcache.validate_timestamps": "1"];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto results = store.install("demo");
        assert(results.length == 1);
        auto ini = readText(buildPath(dirName(results[0].targetPath), "php.ini"));
        foreach (want; [
            "opcache.enable=1",
            "opcache.enable_cli=1",
            "opcache.file_update_protection=0",
            "opcache.save_comments=1",
            "opcache.validate_timestamps=1",
        ])
            assert(ini.canFind(want), ini);
        assert(!ini.canFind("opcache.revalidate_freq"));
    }

    // TestStoreInstallAppliesFrameworkPHPExtensionsWithoutConfig
    {
        auto projectDir = createTempDir("polka-st-fwext-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedTool(store.cacheDir, toolPHP, "8.4");

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.framework = "drupal";
        demo.tools = [toolPHP: "8.4"];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto results = store.install("demo");
        assert(results.length == 1);
        auto ini = readText(buildPath(dirName(results[0].targetPath), "php.ini"));
        foreach (want; ["extension=gd", "extension=mbstring", "zend_extension=opcache"])
            assert(ini.canFind(want), ini);
        assert(store.readConfig().environments["demo"].phpExtensions.length == 0);
    }

    // TestStoreInstallFrameworkPHPExtensionsHonorUserOverrides
    {
        auto projectDir = createTempDir("polka-st-fwov-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedTool(store.cacheDir, toolPHP, "8.4");

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.framework = "drupal";
        demo.tools = [toolPHP: "8.4"];
        demo.phpExtensions = ["gd": false, "xdebug": true];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto results = store.install("demo");
        assert(results.length == 1);
        auto ini = readText(buildPath(dirName(results[0].targetPath), "php.ini"));
        foreach (want; [";extension=gd", "extension=mbstring", "zend_extension=xdebug"])
            assert(ini.canFind(want), ini);
    }

    // TestStoreToolPHPExtensionsHonorUserOverrides & TestStoreFrameworkOPcacheConfigHonorsUserOverrides
    {
        auto projectDir = createTempDir("polka-st-plugov-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        Environment env;
        env.framework = "drupal";
        env.tools = [toolMariaDB: "11.8"];
        env.phpExtensions = ["pdo_mysql": false];
        auto eff = store.withPluginPHPExtensions(env);
        assert(eff.phpExtensions.get("gd", false) && eff.phpExtensions.get("mysqli", false));
        auto pdo = "pdo_mysql" in eff.phpExtensions;
        assert(pdo !is null && !(*pdo));

        Environment opcEnv;
        opcEnv.framework = "drupal";
        opcEnv.opcacheConfig = ["opcache.save_comments": "0"];
        auto effOpc = store.withFrameworkOPcacheConfig(opcEnv);
        assert(effOpc.opcacheConfig["opcache.save_comments"] == "0");
    }

    // TestStoreInstallPreservesExistingConfigComments
    {
        auto projectDir = createTempDir("polka-st-c1-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedTool(store.cacheDir, toolPHP, "8.4");

        enum demoData = "# keep this comment\ntools:\n  php: \"8.4\"\n";
        write(store.environmentConfigFile("demo"), demoData);
        write(store.configFile, "version: 0.2\nroot: .polka\n");

        store.install("demo");
        assert(readText(store.environmentConfigFile("demo")) == demoData);
    }

    // TestStoreConfigurePreservesExistingConfigComments
    {
        auto projectDir = createTempDir("polka-st-c2-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        write(store.configFile, "# top comment\nversion: 0.2\nroot: .polka\n");
        write(store.environmentConfigFile("demo"), "# environment comment\ntools:\n  # php version comment\n  php: \"8.4\" # inline php comment\n");

        store.configure("demo", "8.3");
        auto updated = readText(store.environmentConfigFile("demo"));
        foreach (c; ["# environment comment", "# php version comment", "# inline php comment"])
            assert(updated.canFind(c), updated);
        assert(readText(store.configFile).canFind("# top comment"));
        assert(store.readConfig().environments["demo"].toolVersion(toolPHP) == "8.3");
    }

    // TestStoreConfigureValuePreservesExistingConfigComments
    {
        auto projectDir = createTempDir("polka-st-c3-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        write(store.configFile, "version: 0.2\nroot: .polka\n");
        write(store.environmentConfigFile("demo"), "# environment comment\ntools:\n  # mailpit version comment\n  mailpit: \"1.30\" # inline mailpit comment\n");

        auto env = store.configureValue("demo", "settings.mailpit.smtp-port", "1125");
        assert(env.mailpit() !is null && env.mailpit().version_ == "1.30" && env.mailpit().smtpPort == 1125);

        auto updated = readText(store.environmentConfigFile("demo"));
        foreach (c; ["# environment comment", "# mailpit version comment", "# inline mailpit comment"])
            assert(updated.canFind(c), updated);
        assert(updated.canFind("settings:\n  mailpit:\n    smtp-port: 1125"), updated);
    }

    // TestStoreConfigureValueInfersDatabaseVersionFromTool
    {
        auto projectDir = createTempDir("polka-st-dbinf-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        store.configureValue("data", "tools.mysql", "8.0");
        store.configureValue("data", "database.engine", "mysql");
        auto env = store.configureValue("data", "database.port", "3306");
        assert(env.database !is null && env.database.engine == "mysql" && env.database.version_ == "8.0" && env.database.port == 3306);

        auto text = readText(store.environmentConfigFile("data"));
        assert(text.canFind("tools:\n  mysql: \"8.0\"") && text.canFind("database:\n  engine: mysql\n  port: 3306"));
        assert(!text.canFind("  version:"));
    }

    // TestStoreConfigureValueInfersPostgreSQLVersionFromTool
    {
        auto projectDir = createTempDir("polka-st-pginf-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        store.configureValue("data", "tools.postgresql", "17");
        auto env = store.configureValue("data", "database.engine", "postgresql");
        assert(env.database !is null && env.database.engine == toolPostgreSQL && env.database.version_ == "17");

        auto text = readText(store.environmentConfigFile("data"));
        assert(text.canFind("tools:\n  postgresql: \"17\"") && text.canFind("database:\n  engine: postgresql"));
    }

    // TestStoreConfigureValueRejectsPIEToolVersion
    {
        auto projectDir = createTempDir("polka-st-rejpie-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        bool threw = false;
        try { store.configureValue("demo", "tools.pie", "1.4"); }
        catch (Exception e) { threw = e.msg.canFind("managed internally"); }
        assert(threw);
    }

    // TestStoreConfigureValueSetsApacheToolAndServerType
    {
        auto projectDir = createTempDir("polka-st-apcv-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        auto env = store.configureValue("demo", "tools.apache", "2.4");
        assert(env.toolVersion(toolApache) == "2.4");
        env = store.configureValue("demo", "server.type", " Apache ");
        assert(env.server !is null && env.server.type == "apache");
    }

    // TestStoreConfigureValueValidatesBeforeWrite
    {
        auto projectDir = createTempDir("polka-st-valbw-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        write(store.configFile, "version: 0.2\nroot: .polka\n");
        enum original = "tools:\n  php: \"8.4\"\n";
        write(store.environmentConfigFile("demo"), original);

        bool threw = false;
        try { store.configureValue("demo", "tools.php", "bad version!"); }
        catch (Exception) { threw = true; }
        assert(threw);
        assert(readText(store.environmentConfigFile("demo")) == original);
    }
}

unittest
{
    // TestStoreInstallDownloadsWhenCacheMissing
    {
        auto projectDir = createTempDir("polka-st-dlmiss-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        store.downloader = new FakeDownloader((string cdir, string tool, string ver) {
            writeCachedTool(cdir, tool, ver);
        });

        store.configure("demo", "8.4", "2.8", "24");
        auto results = store.install("demo");
        assert(results.length == 3);
        foreach (r; results)
        {
            assert(r.downloaded);
            assertPathExists(r.targetPath);
        }
    }

    // TestStoreInstallTreatsExtractedCacheWithoutMetadataAsMissing
    {
        auto projectDir = createTempDir("polka-st-nometa-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        auto extractedPHP = cachedFakePHPPath(store.cacheDir, toolPHP, "8.4");
        mkdirRecurse(dirName(extractedPHP));
        write(extractedPHP, fakePHPModuleListScript(null));
        setFileMode(extractedPHP, octal!755);

        int downloadCalls = 0;
        store.downloader = new FakeDownloader((string cdir, string tool, string ver) {
            downloadCalls++;
            writeCachedTool(cdir, tool, ver);
        });

        store.configure("demo", "8.4");
        auto results = store.install("demo");
        assert(downloadCalls == 1);
        assert(results.length == 1 && results[0].downloaded && results[0].cachePath != extractedPHP);
    }

    // TestStoreInstallRedownloadsCacheWithChecksumMismatch
    {
        auto projectDir = createTempDir("polka-st-badsum-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        auto cachedPayload = writeCachedTool(store.cacheDir, toolPHP, "8.4");
        write(cachedPayload, "corrupt payload\n");

        int downloadCalls = 0;
        store.downloader = new FakeDownloader((string cdir, string tool, string ver) {
            downloadCalls++;
            writeCachedTool(cdir, tool, ver);
        });

        store.configure("demo", "8.4");
        auto results = store.install("demo");
        assert(downloadCalls == 1 && results.length == 1 && results[0].downloaded);
    }

    // TestStoreInstallWithProgressReportsStages
    {
        auto projectDir = createTempDir("polka-st-prog-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedTool(store.cacheDir, toolPHP, "8.4");
        store.downloader = new FakeDownloader((string cdir, string tool, string ver) {
            writeCachedTool(cdir, tool, ver);
        });

        auto dbCfg = new DatabaseConfig(toolMySQL, "8.4");
        store.configure("demo", "8.4", "", "", dbCfg);

        string[] events;
        store.installWithProgress("demo", InstallOptions.init, (InstallProgress p) {
            events ~= format("%d/%d %s %s %s", p.index, p.total, p.tool, p.version_, cast(string) p.stage);
        });

        string[] want = [
            "1/2 php 8.4 using cache",
            "1/2 php 8.4 installing",
            "1/2 php 8.4 configuring",
            "1/2 php 8.4 installed",
            "2/2 mysql 8.4 downloading",
            "2/2 mysql 8.4 installing",
            "2/2 mysql 8.4 installed",
        ];
        events.sort();
        want.sort();
        assert(events == want, format("events=%s want=%s", events, want));
    }

    // TestStoreInstallDownloadsConfiguredDatabase
    {
        auto projectDir = createTempDir("polka-st-db1-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        store.downloader = new FakeDownloader((string cdir, string tool, string ver) {
            writeCachedTool(cdir, tool, ver);
        });

        store.configure("demo", "", "", "", new DatabaseConfig(toolMySQL, "8.4"));
        auto results = store.install("demo");
        assert(results.length == 1 && results[0].tool == toolMySQL && results[0].version_ == "8.4" && results[0].downloaded);
        assertPathExists(results[0].targetPath);
        assert(results[0].targetPath.canFind(buildPath("envs", toolMySQL, "8.4")));

        bool threw = false;
        try { store.resolveTool(toolMySQL); }
        catch (Exception e) { threw = e.msg.canFind(`environment "default" does not define a mysql version`); }
        assert(threw);

        store.use("demo");
        assert(store.resolveTool(toolMySQL) == results[0].targetPath);
    }

    // TestStoreInstallDownloadsConfiguredSQLite
    {
        auto projectDir = createTempDir("polka-st-sqlite-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        store.downloader = new FakeDownloader((string cdir, string tool, string ver) {
            writeCachedTool(cdir, tool, ver);
        });

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [toolSQLite: "3.53"];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto results = store.install("demo");
        assert(results.length == 1 && results[0].tool == toolSQLite && results[0].version_ == "3.53");
        store.use("demo");
        assert(store.resolveTool("sqlite3") == results[0].targetPath);
    }

    // TestStoreInstallDownloadsMultipleConfiguredDatabases
    {
        auto projectDir = createTempDir("polka-st-multidb-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        store.downloader = new FakeDownloader((string cdir, string tool, string ver) {
            writeCachedTool(cdir, tool, ver);
        });

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [toolMySQL: "8.4", toolMariaDB: "11.8", toolNodeJS: "24"];
        demo.database = new DatabaseConfig(toolMariaDB, "11.8", 3307);
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto results = store.install("demo");
        assert(results.length == 3);
        string[] got;
        foreach (r; results)
            got ~= r.tool ~ ":" ~ r.version_;
        assert(got == ["nodejs:24", "mysql:8.4", "mariadb:11.8"]);
    }

    // TestStoreInstallDownloadsConfiguredNginx
    {
        auto projectDir = createTempDir("polka-st-nginx-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        store.downloader = new FakeDownloader((string cdir, string tool, string ver) {
            writeCachedTool(cdir, tool, ver);
        });

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [toolNginx: "1.30"];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto results = store.install("demo");
        assert(results.length == 1 && results[0].tool == toolNginx && results[0].version_ == "1.30" && results[0].downloaded);
        assertPathExists(results[0].targetPath);
        assert(results[0].targetPath.canFind(buildPath("envs", toolNginx, "1.30")));

        store.use("demo");
        assert(store.resolveTool(toolNginx) == results[0].targetPath);
        assertPathExists(buildPath(store.binDir, toolNginx));
        assertPathExists(buildPath(store.binDir, toolNginx ~ ".cmd"));
    }

    // TestStoreInstallDownloadsConfiguredApache
    {
        auto projectDir = createTempDir("polka-st-apache-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        store.downloader = new FakeDownloader((string cdir, string tool, string ver) {
            writeCachedTool(cdir, tool, ver);
        });

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [toolApache: "2.4"];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto results = store.install("demo");
        assert(results.length == 1 && results[0].tool == toolApache && results[0].version_ == "2.4" && results[0].downloaded);
        assertPathExists(results[0].targetPath);
        assert(results[0].targetPath.canFind(buildPath("envs", toolApache, "2.4")));

        store.use("demo");
        assert(store.resolveTool(toolApache) == results[0].targetPath);
        assert(store.resolveTool("httpd") == results[0].targetPath);
        assertPathExists(buildPath(store.binDir, toolApache));
        assertPathExists(buildPath(store.binDir, toolApache ~ ".cmd"));
        assertPathExists(buildPath(store.binDir, "httpd"));
        assertPathExists(buildPath(store.binDir, "httpd.cmd"));
    }

    // TestStoreInstallDownloadsApacheLoungeLayout (Windows-only)
    version (Windows)
    {
        auto projectDir = createTempDir("polka-st-apwin-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        store.downloader = new FakeDownloader((string cdir, string tool, string ver) {
            const(ubyte)[][string] files = [
                "ReadMe.txt": cast(const(ubyte)[]) "apache lounge readme\n",
                "Apache24/bin/httpd.exe": cast(const(ubyte)[]) "placeholder\n",
                "Apache24/modules/mod_ssl.so": cast(const(ubyte)[]) "placeholder\n",
            ];
            writeCachedArchivePayload(cdir, tool, ver, files);
        });
        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [toolApache: "2.4"];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto results = store.install("demo");
        auto wantTarget = buildPath(store.envsDir, toolApache, "2.4", "Apache24", "bin", "httpd.exe");
        assert(results.length == 1 && results[0].targetPath == wantTarget);
        store.use("demo");
        assert(store.resolveTool(toolApache) == wantTarget);
        assert(store.resolveTool("httpd") == wantTarget);
    }

    // TestStoreInstallDownloadsConfiguredMago
    {
        auto projectDir = createTempDir("polka-st-mago-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        store.downloader = new FakeDownloader((string cdir, string tool, string ver) {
            writeCachedTool(cdir, tool, ver);
        });

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [toolMago: "1.27"];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto results = store.install("demo");
        assert(results.length == 1 && results[0].tool == toolMago && results[0].version_ == "1.27" && results[0].downloaded);
        assertPathExists(results[0].targetPath);
        store.use("demo");
        assert(store.resolveTool(toolMago) == results[0].targetPath);
        assertPathExists(buildPath(store.binDir, toolMago));
        assertPathExists(buildPath(store.binDir, toolMago ~ ".cmd"));
    }

    // TestStoreInstallRejectsConfiguredPIE
    {
        auto projectDir = createTempDir("polka-st-rejp-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        store.downloader = new FakeDownloader((string cdir, string tool, string ver) {
            writeCachedTool(cdir, tool, ver);
        });

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [toolPHP: "8.4", toolPIE: "1.4"];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        bool threw = false;
        try { store.install("demo"); }
        catch (Exception e) { threw = e.msg.canFind("managed internally"); }
        assert(threw);
        assertPathMissing(buildPath(store.envsDir, toolPIE));
        assertPathMissing(buildPath(store.binDir, toolPIE));
        assertPathMissing(buildPath(store.binDir, toolPIE ~ ".cmd"));
    }

    // TestStoreInstallDownloadsConfiguredMailpit, Meilisearch, Traefik, PHPMyAdmin
    foreach (tc; [
        [toolMailpit, "1.30"],
        [toolMeilisearch, "1.48"],
        [toolTraefik, "3.3"],
    ])
    {
        auto tool = tc[0];
        auto ver = tc[1];
        auto projectDir = createTempDir("polka-st-svc-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        store.downloader = new FakeDownloader((string cdir, string t, string v) {
            writeCachedTool(cdir, t, v);
        });

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [tool: ver];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto results = store.install("demo");
        assert(results.length == 1 && results[0].tool == tool && results[0].version_ == ver && results[0].downloaded);
        assertPathExists(results[0].targetPath);
        store.use("demo");
        assert(store.resolveTool(tool) == results[0].targetPath);
        assertPathExists(buildPath(store.binDir, tool));
        assertPathExists(buildPath(store.binDir, tool ~ ".cmd"));
    }

    // TestStoreInstallDownloadsConfiguredPHPMyAdmin
    {
        auto projectDir = createTempDir("polka-st-pma-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        store.downloader = new FakeDownloader((string cdir, string tool, string ver) {
            writeCachedTool(cdir, tool, ver);
        });

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [toolPHPMyAdmin: "5.2"];
        demo.setSettingInt(toolPHPMyAdmin, "port", 8082);
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto results = store.install("demo");
        assert(results.length == 1 && results[0].tool == toolPHPMyAdmin && results[0].version_ == "5.2" && results[0].downloaded);
        assertPathExists(results[0].targetPath);
        assert(results[0].targetPath.canFind(buildPath("envs", toolPHPMyAdmin, "5.2")));
        store.use("demo");
        assertPathMissing(buildPath(store.binDir, toolPHPMyAdmin));
        assertPathMissing(buildPath(store.binDir, toolPHPMyAdmin ~ ".cmd"));
    }

    // TestStoreInstallCopiesConfiguredNodeJSIntoVersionedLayout
    {
        auto projectDir = createTempDir("polka-st-node-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        auto cacheNodeJS = writeCachedTool(store.cacheDir, toolNodeJS, "24");
        writeCachedNodeJSCommand(store.cacheDir, "24", toolNPM);
        writeCachedNodeJSCommand(store.cacheDir, "24", toolNPX);
        writeCachedNodeJSCommand(store.cacheDir, "24", toolNode);

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [toolNodeJS: "24"];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);
        store.writeActiveEnvironmentName("demo");

        auto results = store.install("demo");
        assert(results.length == 1 && results[0].tool == toolNodeJS && results[0].version_ == "24" && results[0].cachePath == cacheNodeJS);
        assertPathExists(results[0].targetPath);
        assert(results[0].targetPath.canFind(buildPath("envs", toolNodeJS, "24")));

        foreach (cmd; [toolNode, toolNPM, toolNPX, toolYarn])
        {
            auto resolved = store.resolveTool(cmd);
            assertPathExists(resolved);
            assert(resolved.canFind(buildPath("envs", toolNodeJS, "24")));
            if (cmd == toolYarn)
                assert(readText(resolved).toLower().canFind("corepack"));
        }

        bool threw = false;
        try { store.resolveTool(toolNodeJS); }
        catch (Exception) { threw = true; }
        assert(threw);

        foreach (cmd; [toolNode, toolNPM, toolNPX, toolYarn])
        {
            assertPathExists(buildPath(store.binDir, cmd));
            assertPathExists(buildPath(store.binDir, cmd ~ ".cmd"));
        }
        assertPathMissing(buildPath(store.binDir, toolNodeJS));
        assertPathMissing(buildPath(store.binDir, toolNodeJS ~ ".cmd"));
    }

    // TestWindowsDispatchBinarySupportsCopiedShimWithInheritedDispatcher (Windows-only)
    version (Windows)
    {
        auto projectDir = createTempDir("polka-st-winshim-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        store.configure("demo", "", "", "24");
        store.use("demo");

        auto origData = readText(buildPath(store.binDir, toolNPX ~ ".cmd"));
        auto copiedDir = buildPath(projectDir, "copied-shims");
        mkdirRecurse(copiedDir);
        auto copiedPath = buildPath(copiedDir, toolNPX ~ ".cmd");
        write(copiedPath, origData);
        setFileMode(copiedPath, octal!755);

        auto capturePath = buildPath(projectDir, "dispatch-capture.txt");
        auto fakeDisp = buildPath(projectDir, "fake-dispatcher.cmd");
        write(fakeDisp,
            "@echo off\r\n" ~
            "> \"%POLKA_TEST_CAPTURE%\" echo args:%*\r\n" ~
            ">> \"%POLKA_TEST_CAPTURE%\" echo dispatcher:%POLKA_TOOL_DISPATCHER%\r\n" ~
            ">> \"%POLKA_TEST_CAPTURE%\" echo root:%POLKA_TOOL_ROOT%\r\n" ~
            "exit /b 0\r\n");

        auto res = execute(
            ["cmd.exe", "/c", copiedPath, "create-vite"],
            [
                "POLKA_TOOL_DISPATCHER": fakeDisp,
                "POLKA_TOOL_ROOT": store.rootDir,
                "POLKA_TEST_CAPTURE": capturePath,
            ],
            std.process.Config.none,
            size_t.max,
            projectDir
        );
        assert(res.status == 0);
        auto capture = readText(capturePath);
        assert(capture.canFind(`args:--root "` ~ store.rootDir ~ `" dispatch npx create-vite`));
        assert(capture.canFind("dispatcher:" ~ fakeDisp));
        assert(capture.canFind("root:" ~ store.rootDir));
    }
}

unittest
{
    // TestStoreInstallWritesPHPExtensionConfig
    {
        auto projectDir = createTempDir("polka-st-phpext-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedTool(store.cacheDir, toolPHP, "8.4");
        mkdirRecurse(buildPath(store.cacheDir, toolPHP, "8.4", "ext"));

        store.configure("demo", "8.4");
        auto cfg = store.loadConfig();
        auto env = cfg.environments["demo"];
        env.phpExtensions = [" OpenSSL ": true, "xdebug": false];
        cfg.environments["demo"] = env;
        store.writeConfig(cfg);

        auto results = store.install("demo");
        assert(results.length == 1);
        auto ini = readText(buildPath(dirName(results[0].targetPath), "php.ini"));
        assert(ini.canFind(`extension_dir="../ext"`));
        assert(ini.canFind("extension=openssl"));
        assert(ini.canFind(";zend_extension=xdebug"));
        assert(ini.indexOf("extension=openssl") < ini.indexOf(";zend_extension=xdebug"));
        assert(!ini.canFind(" OpenSSL "));
    }

    // TestStoreInstallEnablesComposerPHPExtensionsByDefault
    {
        auto projectDir = createTempDir("polka-st-compext-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedTool(store.cacheDir, toolPHP, "8.4");
        writeCachedTool(store.cacheDir, toolComposer, "2.8");
        writeCachedTool(store.cacheDir, toolNodeJS, "24");
        mkdirRecurse(buildPath(store.cacheDir, toolPHP, "8.4", "ext"));

        store.configure("demo", "8.4", "2.8", "24");
        auto results = store.install("demo");
        assert(results.length == 3);

        auto ini = readText(buildPath(store.rootDir, "envs", "php", "8.4", "bin", "php.ini"));
        assert(ini.canFind("extension=openssl") && ini.canFind("extension=zip"));
    }

    // TestStoreInstallComposerDefaultsHonorExplicitFalse
    {
        auto projectDir = createTempDir("polka-st-compfalse-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        writeCachedTool(store.cacheDir, toolPHP, "8.4");
        writeCachedTool(store.cacheDir, toolComposer, "2.8");
        writeCachedTool(store.cacheDir, toolNodeJS, "24");
        mkdirRecurse(buildPath(store.cacheDir, toolPHP, "8.4", "ext"));

        store.configure("demo", "8.4", "2.8", "24");
        auto cfg = store.loadConfig();
        auto env = cfg.environments["demo"];
        env.phpExtensions = ["openssl": false];
        cfg.environments["demo"] = env;
        store.writeConfig(cfg);

        store.install("demo");
        auto ini = readText(buildPath(store.rootDir, "envs", "php", "8.4", "bin", "php.ini"));
        assert(ini.canFind(";extension=openssl") && ini.canFind("extension=zip"));
    }

    // TestStoreInstallRejectsPHPExtensionsWithoutPHP, OPcacheWithoutPHP, MemoryLimitWithoutPHP
    {
        auto projectDir = createTempDir("polka-st-nophp-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");

        {
            auto cfg = store.defaultConfig();
            Environment demo;
            demo.tools = [toolComposer: "2.8"];
            demo.phpExtensions = ["openssl": true];
            cfg.environments["demo"] = demo;
            store.writeConfig(cfg);
            bool threw = false;
            try { store.install("demo"); }
            catch (Exception e) { threw = e.msg.canFind("php-extensions"); }
            assert(threw);
        }
        {
            auto cfg = store.defaultConfig();
            Environment demo;
            demo.tools = [toolComposer: "2.8"];
            demo.opcachePreset = "dev";
            cfg.environments["demo"] = demo;
            store.writeConfig(cfg);
            bool threw = false;
            try { store.install("demo"); }
            catch (Exception e) { threw = e.msg.canFind("OPcache config"); }
            assert(threw);
        }
        {
            auto cfg = store.defaultConfig();
            Environment demo;
            demo.tools = [toolComposer: "2.8"];
            demo.memoryLimit = "512M";
            cfg.environments["demo"] = demo;
            store.writeConfig(cfg);
            bool threw = false;
            try { store.install("demo"); }
            catch (Exception e) { threw = e.msg.canFind("memory-limit"); }
            assert(threw);
        }
    }

    // TestStoreCurrentNormalizesServerConfig
    {
        auto projectDir = createTempDir("polka-st-srvnorm-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [toolPHP: "8.4"];
        demo.https = true;
        demo.server = new ServerConfig("", " localhost ", 8080, false);
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);
        store.writeActiveEnvironmentName("demo");

        auto cur = store.current();
        assert(cur !is null && cur.server !is null);
        assert(cur.server.hostname == "localhost" && cur.server.port == 8080 && cur.server.https);
    }

    // TestStoreCurrentNormalizesPHPMyAdminConfig & InheritsRootHTTPS & WriteConfigPersistsHTTPSAtRoot
    {
        auto projectDir = createTempDir("polka-st-httpsroot-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.https = true;
        demo.server = new ServerConfig("", "localhost", 8443, true);
        demo.tools = [toolMailpit: "1.30", toolPHPMyAdmin: " 5.2 "];
        demo.setSettingInt(toolMailpit, "smtp-port", 1125);
        demo.setSettingInt(toolMailpit, "ui-port", 8125);
        demo.setSettingInt(toolPHPMyAdmin, "port", 8082);
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);
        store.writeActiveEnvironmentName("demo");

        auto cur = store.current();
        assert(cur !is null && cur.https);
        assert(cur.server !is null && cur.server.https);
        assert(cur.mailpit() !is null && cur.mailpit().https);
        assert(cur.phpMyAdmin() !is null && cur.phpMyAdmin().version_ == "5.2" && cur.phpMyAdmin().port == 8082 && cur.phpMyAdmin().https);

        auto text = readText(store.environmentConfigFile("demo"));
        assert(text.canFind("https: true\n"), text);
        assert(!text.canFind("    https: true") && !text.canFind("  https: true"));
    }

    // TestStoreWritesToolSettingsSeparatelyFromVersions
    {
        auto projectDir = createTempDir("polka-st-sepwr-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [toolMailpit: "1.30", toolPHPMyAdmin: "5.2", toolMeilisearch: "1.48"];
        demo.setSettingInt(toolMailpit, "smtp-port", 1125);
        demo.setSettingInt(toolMailpit, "ui-port", 8125);
        demo.setSettingInt(toolPHPMyAdmin, "port", 8082);
        demo.setSettingInt(toolMeilisearch, "port", 7701);
        demo.setSettingString(toolMeilisearch, "master-key", "local-dev-key");
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);

        auto text = readText(store.environmentConfigFile("demo"));
        assert(text.canFind("tools:\n  mailpit: \"1.30\"\n  meilisearch: \"1.48\"\n  phpmyadmin: \"5.2\""), text);
        assert(text.canFind("settings:\n  mailpit:\n    smtp-port: 1125\n    ui-port: 8125\n  meilisearch:\n    master-key: local-dev-key\n    port: 7701\n  phpmyadmin:\n    port: 8082"), text);
        assert(!text.canFind("    version:"));
    }

    // TestStoreReadsToolSettingsSeparatedFromVersions
    foreach (envName; [defaultEnvironmentName, "demo"])
    {
        auto projectDir = createTempDir("polka-st-seprd-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        auto bodyBlock =
            "tools:\n  mailpit: \"1.30\"\n  phpmyadmin: \"5.2\"\n  meilisearch: \"1.48\"\n" ~
            "settings:\n  mailpit:\n    smtp-port: 1125\n    ui-port: 8125\n" ~
            "  phpmyadmin:\n    port: 8082\n  meilisearch:\n    port: 7701\n    master-key: local-dev-key\n";

        if (envName == defaultEnvironmentName)
            write(store.configFile, "version: 0.2\nroot: .polka\n" ~ bodyBlock);
        else
        {
            write(store.configFile, "version: 0.2\nroot: .polka\n");
            write(store.environmentConfigFile("demo"), bodyBlock);
        }

        bool ok = false;
        auto env = store.readEnvironment(envName, ok);
        assert(ok);
        assert(env.mailpit() !is null && env.mailpit().version_ == "1.30" && env.mailpit().smtpPort == 1125 && env.mailpit().uiPort == 8125);
        assert(env.phpMyAdmin() !is null && env.phpMyAdmin().version_ == "5.2" && env.phpMyAdmin().port == 8082);
        assert(env.meilisearch() !is null && env.meilisearch().version_ == "1.48" && env.meilisearch().port == 7701 && env.meilisearch().masterKey == "local-dev-key");
    }

    // TestStoreCurrentNormalizesEnvironmentVariables
    {
        auto projectDir = createTempDir("polka-st-envvars-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        auto cfg = store.defaultConfig();
        Environment demo;
        demo.tools = [toolPHP: "8.4"];
        demo.envFile = " .env.local ";
        demo.envVars = [" APP_ENV ": "development", "FEATURE_FLAG": "1"];
        cfg.environments["demo"] = demo;
        store.writeConfig(cfg);
        store.writeActiveEnvironmentName("demo");

        auto cur = store.current();
        assert(cur !is null && cur.envFile == ".env.local" && cur.envVars.length == 2);
        assert(cur.envVars["APP_ENV"] == "development" && cur.envVars["FEATURE_FLAG"] == "1");
    }

    // TestStoreCreateRejectsExistingEnvironment
    {
        auto projectDir = createTempDir("polka-st-createex-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");

        auto env = store.create("demo", "8.4", "2.8", "24");
        assert(env.toolVersion(toolPHP) == "8.4" && env.toolVersion(toolComposer) == "2.8");

        bool threw = false;
        try { store.create("demo", "8.3", "2.7", "22"); }
        catch (Exception e) { threw = e.msg.canFind("already exists"); }
        assert(threw);

        auto created = store.readConfig().environments["demo"];
        assert(created.toolVersion(toolPHP) == "8.4" && created.toolVersion(toolComposer) == "2.8");
    }

    // TestStoreCreatePreservesExistingConfigComments
    {
        auto projectDir = createTempDir("polka-st-createc-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        write(store.configFile, "# project comment\nversion: 0.2\nroot: .polka\n");
        write(store.environmentConfigFile("demo"), "# existing environment comment\ntools:\n  php: \"8.4\"\n");

        store.create("api", "8.3", "2.7", "22");
        assert(readText(store.configFile).canFind("# project comment"));
        assert(readText(store.environmentConfigFile("demo")).canFind("# existing environment comment"));
        auto created = store.readConfig().environments["api"];
        assert(created.toolVersion(toolPHP) == "8.3" && created.toolVersion(toolComposer) == "2.7");
    }

    // TestStoreConfigureLifecycleUsesVersionLabels
    {
        auto projectDir = createTempDir("polka-st-life-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.cacheDir = buildPath(projectDir, "global-cache");
        auto phpCachePath = writeCachedTool(store.cacheDir, toolPHP, "8.4");
        auto compCachePath = writeCachedTool(store.cacheDir, toolComposer, "2.8");
        writeCachedTool(store.cacheDir, toolNodeJS, "24");

        auto env = store.configure("api", "8.4", "2.8", "24");
        assert(env.toolVersion(toolPHP) == "8.4" && env.toolVersion(toolComposer) == "2.8");

        store.configure("web", "8.3", "", "22");
        writeCachedTool(store.cacheDir, toolPHP, "8.3");

        auto installResults = store.install("api");
        assert(installResults.length == 3);

        auto envs = store.list();
        assert(envs.length == 3 && envs[0].name == "api" && envs[1].name == defaultEnvironmentName && envs[2].name == "web");
        assert(envs[0].toolVersion(toolPHP) == "8.4" && envs[0].toolVersion(toolComposer) == "2.8");

        store.use("api");
        auto cur = store.current();
        assert(cur !is null && cur.name == "api" && cur.toolVersion(toolPHP) == "8.4" && cur.toolVersion(toolComposer) == "2.8");
        assert(store.resolveTool("php").canFind(buildPath("envs", "php", "8.4")));
        assert(store.resolveTool("composer").canFind(buildPath("envs", "composer", "2.8")));

        store.remove("api");
        cur = store.current();
        assert(cur !is null && cur.name == defaultEnvironmentName);

        envs = store.list();
        assert(envs.length == 2 && envs[0].name == defaultEnvironmentName && envs[1].name == "web");

        auto cfgText = readText(store.configFile);
        assert(!cfgText.canFind("current:"));
        assert(!cfgText.canFind(phpCachePath) && !cfgText.canFind(compCachePath));
        assert(store.readConfig().environments["web"].toolVersion(toolPHP) == "8.3");
    }

    // TestStoreRemovePreservesRemainingConfigComments
    {
        auto projectDir = createTempDir("polka-st-rmc-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        write(store.configFile, "# project comment\nversion: 0.2\nroot: .polka\n");
        write(store.environmentConfigFile("demo"), "tools:\n  php: \"8.4\"\n");
        write(store.environmentConfigFile("web"), "# keep this environment comment\ntools:\n  php: \"8.3\" # keep this inline comment\n");
        store.writeActiveEnvironmentName("demo");
        store.init();

        store.remove("demo");
        assert(readText(store.configFile).canFind("# project comment"));
        auto webUpdated = readText(store.environmentConfigFile("web"));
        assert(webUpdated.canFind("# keep this environment comment") && webUpdated.canFind("# keep this inline comment"));
        auto cfg = store.readConfig();
        assert(("demo" in cfg.environments) is null && cfg.environments["web"].toolVersion(toolPHP) == "8.3");
    }

    // TestStoreConfigureNormalizesDatabaseConfig, Writes/Reads Database Tool Version
    {
        auto projectDir = createTempDir("polka-st-dbcfg-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        auto env = store.configure("data", "", "", "", new DatabaseConfig(" MySQL ", " 8.0 ", 3306));
        assert(env.database !is null && env.database.engine == toolMySQL && env.database.version_ == "8.0" && env.database.port == 3306);
        auto stored = store.readConfig().environments["data"].database;
        assert(stored !is null && stored.engine == toolMySQL && stored.version_ == "8.0" && stored.port == 3306);

        store.configure("maria", "", "", "", new DatabaseConfig(toolMariaDB, "11.8", 3307));
        auto mariaText = readText(store.environmentConfigFile("maria"));
        assert(mariaText.canFind("tools:\n  mariadb: \"11.8\"") && mariaText.canFind("database:\n  engine: mariadb\n  port: 3307"));
        assert(!mariaText.canFind("  version:"));
    }

    // TestStoreReadsDatabaseToolVersionAndRootRuntimeConfig
    {
        auto projectDir = createTempDir("polka-st-dbrd-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        write(store.environmentConfigFile("data"), "tools:\n  mysql: \"8.4\"\n  mariadb: \"11.8\"\ndatabase:\n  engine: mariadb\n  port: 3307\n");
        write(store.configFile, "version: 0.2\nroot: .polka\n");

        bool ok = false;
        auto env = store.readEnvironment("data", ok);
        assert(ok && env.database !is null && env.database.engine == toolMariaDB && env.database.version_ == "11.8" && env.database.port == 3307);
        assert(env.toolVersion(toolMySQL) == "8.4" && env.toolVersion(toolMariaDB) == "11.8");
    }

    // TestStoreRejectsNonVersionToolsAndOrphanSettings
    {
        struct SchemaCase { string cfg; string wantErr; }
        foreach (tc; [
            SchemaCase("tools:\n  mailpit:\n    version: \"1.30\"\n    smtp-port: 1025\n", "tools.mailpit must be a scalar version label"),
            SchemaCase("tools:\n  phpmyadmin:\n    version: \"5.2\"\n    port: 8082\n", "tools.phpmyadmin must be a scalar version label"),
            SchemaCase("tools:\n  meilisearch:\n    version: \"1.48\"\n    port: 7700\n", "tools.meilisearch must be a scalar version label"),
            SchemaCase("tools:\n  database:\n    engine: mysql\n    version: \"8.0\"\n    port: 3306\n", "tools.database is no longer supported"),
            SchemaCase("tools:\n  database: \"8.0\"\n", "tools.database is no longer supported"),
            SchemaCase("settings:\n  mailpit:\n    ui-port: 8025\n", "settings.mailpit requires tools.mailpit"),
            SchemaCase("settings:\n  phpmyadmin:\n    port: 8082\n", "settings.phpmyadmin requires tools.phpmyadmin"),
            SchemaCase("settings:\n  meilisearch:\n    port: 7700\n", "settings.meilisearch requires tools.meilisearch"),
            SchemaCase("opcache-preset: staging\n", "unsupported opcache-preset"),
            SchemaCase("opcache-preset: true\n", "opcache-preset must be one of none, dev, or production"),
            SchemaCase("memory-limit: 1.5G\n", "invalid memory-limit"),
            SchemaCase("memory-limit:\n  value: 512M\n", "memory-limit must be a scalar"),
            SchemaCase("opcache-config: true\n", "opcache-config must be a mapping"),
            SchemaCase("opcache-config:\n  zend_extension: opcache\n", "unsupported opcache-config.zend_extension key"),
            SchemaCase("opcache-config:\n  opcache.enable:\n    nested: true\n", "opcache-config.opcache.enable must be a scalar"),
        ])
        {
            auto projectDir = createTempDir("polka-st-badschema-");
            scope (exit) rmdirRecurseQuiet(projectDir);
            auto store = newProjectStore(projectDir);
            write(store.configFile, "version: 0.2\nroot: .polka\n");
            write(store.environmentConfigFile("data"), tc.cfg);

            bool ok = false;
            bool threw = false;
            try { store.readEnvironment("data", ok); }
            catch (Exception e) { threw = e.msg.canFind(tc.wantErr); }
            assert(threw, tc.wantErr);
        }
    }

    // TestResolveToolRequiresConfiguredVersion & TestStoreResolveToolLogs*
    {
        auto projectDir = createTempDir("polka-st-reslog-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        write(store.configFile, "version: 0.2\nroot: .polka\n");
        write(store.environmentConfigFile("demo"), "tools:\n  composer: 2.8\n");
        store.writeActiveEnvironmentName("demo");

        bool threw = false;
        try { store.resolveTool("php"); }
        catch (Exception) { threw = true; }
        assert(threw);

        // Nginx & Apache logs
        foreach (pair; [[toolNginx, "1.30"], [toolApache, "2.4"]])
        {
            auto cfg = store.defaultConfig();
            Environment demo;
            demo.tools = [pair[0]: pair[1]];
            cfg.environments["demo"] = demo;
            store.writeConfig(cfg);
            store.writeActiveEnvironmentName("demo");

            auto logs = store.resolveToolLogs(pair[0], "error");
            assert(logs == [ToolLogEntry(buildPath(store.rootDir, "run", pair[0], "demo", "logs", "error.log"), "error")]);
        }

        // Invalid log requests
        {
            auto cfg = store.defaultConfig();
            Environment demo;
            demo.tools = [toolPHP: "8.4"];
            cfg.environments["demo"] = demo;
            store.writeConfig(cfg);
            store.writeActiveEnvironmentName("demo");

            struct LogErrCase { string tool; string level; string wantErr; }
            foreach (tc; [
                LogErrCase("php", "trace", "invalid log level"),
                LogErrCase("database", "", "unsupported tool"),
                LogErrCase("mailpit", "", "does not define a mailpit version"),
                LogErrCase("php", "", "does not declare logs"),
            ])
            {
                bool sawErr = false;
                try { store.resolveToolLogs(tc.tool, tc.level); }
                catch (Exception e) { sawErr = e.msg.canFind(tc.wantErr); }
                assert(sawErr, tc.wantErr);
            }
        }

        // Dispatch aliases for logs
        {
            auto aliasProjectDir = createTempDir("polka-st-logalias-");
            scope (exit) rmdirRecurseQuiet(aliasProjectDir);
            auto aliasStore = newProjectStore(aliasProjectDir);
            auto reg = newToolRegistry(new LogAliasTestPlugin());
            aliasStore.plugins = reg;
            auto logs = aliasStore.resolveToolLogs("alias-bin", "");
            assert(logs == [ToolLogEntry(buildPath(aliasStore.rootDir, "run", "alias-tool", "default", "default.log"), "info")]);
        }
    }

    // Store discovery tests (TestDefaultStore* and TestStoreForRoot*)
    {
        auto origWD = getcwd();
        scope (exit) chdir(origWD);

        // Working directory default
        {
            auto projectDir = createTempDir("polka-st-cwd1-");
            scope (exit) rmdirRecurseQuiet(projectDir);
            chdir(projectDir);
            auto store = defaultStore();
            assert(store.rootDir == buildPath(projectDir, ".polka"));
            assert(store.configFile == buildPath(projectDir, "polka.yaml"));
        }

        // Parent project discovery from nested dir
        {
            auto projectDir = createTempDir("polka-st-cwd2-");
            scope (exit) rmdirRecurseQuiet(projectDir);
            auto nestedDir = buildPath(projectDir, "web", "modules");
            mkdirRecurse(nestedDir);
            write(buildPath(projectDir, "polka.yaml"), "version: 0.2\nroot: .polka\n");
            chdir(nestedDir);
            auto store = defaultStore();
            assert(store.rootDir == buildPath(projectDir, ".polka"));
            assert(store.configFile == buildPath(projectDir, "polka.yaml"));
        }

        // Configured nested root + StoreForRoot ancestor config
        {
            auto projectDir = createTempDir("polka-st-cwd3-");
            scope (exit) rmdirRecurseQuiet(projectDir);
            auto nestedDir = buildPath(projectDir, "test-site", "web");
            mkdirRecurse(nestedDir);
            write(buildPath(projectDir, "polka.yaml"), "version: 0.2\nroot: test-site/.polka\n");
            write(buildPath(projectDir, "polka.demo.yaml"), "tools:\n  php: \"8.4\"\n");

            chdir(nestedDir);
            auto store = defaultStore();
            assert(store.projectDir == projectDir);
            assert(store.rootDir == buildPath(projectDir, "test-site", ".polka"));
            assert(store.configFile == buildPath(projectDir, "polka.yaml"));

            auto rootStore = storeForRoot(buildPath(projectDir, "test-site", ".polka"));
            assert(rootStore.projectDir == projectDir);
            assert(rootStore.configFile == buildPath(projectDir, "polka.yaml"));
            rootStore.writeActiveEnvironmentName("demo");
            auto cur = rootStore.current();
            assert(cur !is null && cur.name == "demo");
        }

        // Ignore nested runtime .polka when parent has polka.yaml
        {
            auto projectDir = createTempDir("polka-st-cwd4-");
            scope (exit) rmdirRecurseQuiet(projectDir);
            auto nestedDir = buildPath(projectDir, "drupal");
            mkdirRecurse(buildPath(nestedDir, ".polka", "run"));
            write(buildPath(projectDir, "polka.yaml"), "version: 0.2\nroot: .polka\n");
            chdir(nestedDir);
            auto store = defaultStore();
            assert(store.projectDir == projectDir && store.rootDir == buildPath(projectDir, ".polka"));
        }
    }
}

// ============================================================================
// Internal Tools Store Tests (internal_tools_test.go parity)
// ============================================================================

unittest
{
    auto origTools = environment.get("POLKA_TOOLS_DIR", "");
    auto origCache = environment.get("POLKA_CACHE_DIR", "");
    bool hadTools = environment.get("POLKA_TOOLS_DIR", null) !is null;
    bool hadCache = environment.get("POLKA_CACHE_DIR", null) !is null;
    scope (exit)
    {
        if (hadTools) environment["POLKA_TOOLS_DIR"] = origTools;
        else environment.remove("POLKA_TOOLS_DIR");
        if (hadCache) environment["POLKA_CACHE_DIR"] = origCache;
        else environment.remove("POLKA_CACHE_DIR");
    }

    // TestEnsureInternalToolInstallsAndRecords
    {
        auto projectDir = createTempDir("polka-it-inst-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto toolsDir = buildPath(projectDir, "internal-tools");
        auto cacheDir = buildPath(projectDir, "global-cache");
        environment["POLKA_TOOLS_DIR"] = toolsDir;
        environment["POLKA_CACHE_DIR"] = cacheDir;

        auto store = newProjectStore(projectDir);
        store.cacheDir = cacheDir;
        writeCachedTool(cacheDir, toolPIE, "1");

        auto targetPath = store.ensureInternalTool(toolPIE);
        assert(targetPath.startsWith(buildPath(toolsDir, toolPIE, "1")));
        assertPathExists(targetPath);

        auto internalEnv = readInternalEnvironment(buildPath(toolsDir, internalConfigFileName));
        assert(internalEnv.toolVersion(toolPIE) == DefaultPIEVersion);
        assert(store.internalStore().readInstallState().has(toolPIE, "1"));

        store.downloader = new FakeDownloader((string, string tool, string ver) {
            throw new Exception(format("unexpected download of %s %s", tool, ver));
        });
        assert(store.ensureInternalTool(toolPIE) == targetPath);
    }

    // TestEnsureInternalToolConfiguresBroadPHPExtensions
    {
        auto projectDir = createTempDir("polka-it-broad-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto toolsDir = buildPath(projectDir, "internal-tools");
        auto cacheDir = buildPath(projectDir, "global-cache");
        environment["POLKA_TOOLS_DIR"] = toolsDir;
        environment["POLKA_CACHE_DIR"] = cacheDir;

        auto store = newProjectStore(projectDir);
        store.cacheDir = cacheDir;
        writeCachedInternalPHP(cacheDir, DefaultPHPVersion, "intl", "gd");

        store.ensureInternalTool(toolPHP);
        auto phpIni = readText(buildPath(toolsDir, toolPHP, DefaultPHPVersion, "bin", "php.ini"));
        assert(phpIni.canFind("extension=intl") && phpIni.canFind("extension=gd"));
        assert(!phpIni.canFind("extension=soap"));
        assert(phpIni.canFind("extension=openssl") && phpIni.canFind("extension=curl"));
        assert(phpIni.canFind("curl.cainfo=") && phpIni.canFind("openssl.cafile="));
    }

    // TestEnsureInternalToolHonorsConfiguredVersion
    {
        auto projectDir = createTempDir("polka-it-cfgver-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto toolsDir = buildPath(projectDir, "internal-tools");
        auto cacheDir = buildPath(projectDir, "global-cache");
        environment["POLKA_TOOLS_DIR"] = toolsDir;
        environment["POLKA_CACHE_DIR"] = cacheDir;

        auto store = newProjectStore(projectDir);
        store.cacheDir = cacheDir;
        writeCachedTool(cacheDir, toolPIE, "2.5");

        Environment internalEnv;
        internalEnv.name = internalEnvironmentName;
        internalEnv.tools = [toolPIE: "2.5"];
        writeInternalEnvironment(buildPath(toolsDir, internalConfigFileName), internalEnv);

        auto targetPath = store.ensureInternalTool(toolPIE);
        assert(targetPath.canFind(buildPath(toolPIE, "2.5")));
    }

    // TestEnsureInternalToolRecordsExplicitVersion
    {
        auto projectDir = createTempDir("polka-it-expver-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto toolsDir = buildPath(projectDir, "internal-tools");
        auto cacheDir = buildPath(projectDir, "global-cache");
        environment["POLKA_TOOLS_DIR"] = toolsDir;
        environment["POLKA_CACHE_DIR"] = cacheDir;

        auto store = newProjectStore(projectDir);
        store.cacheDir = cacheDir;
        writeCachedTool(cacheDir, toolComposer, "2.9");

        auto targetPath = store.ensureInternalTool(toolComposer, "2.9");
        assertPathExists(targetPath);
        auto internalEnv = readInternalEnvironment(buildPath(toolsDir, internalConfigFileName));
        assert(internalEnv.toolVersion(toolComposer) == "2.9");
    }

    // TestEnsureInternalToolErrors
    {
        auto projectDir = createTempDir("polka-it-errs-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        environment["POLKA_TOOLS_DIR"] = buildPath(projectDir, "internal-tools");
        environment["POLKA_CACHE_DIR"] = buildPath(projectDir, "global-cache");
        auto store = newProjectStore(projectDir);

        bool threwUnknown = false;
        try { store.ensureInternalTool("unknown-tool"); }
        catch (Exception) { threwUnknown = true; }
        assert(threwUnknown);

        bool threwNoVer = false;
        try { store.ensureInternalTool(toolNginx); }
        catch (Exception e) { threwNoVer = e.msg.canFind("internal version"); }
        assert(threwNoVer);
    }
}

// ============================================================================
// Configure Value, Manifest Version & Workers Store Tests
// ============================================================================

unittest
{
    // TestStoreConfigureValueSetsPHPBuildTools & AllowsPHPZTS & RejectsInvalid
    {
        auto projectDir = createTempDir("polka-cv-sdk-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        store.configureValue("default", "tools.php", "8.4");
        auto env = store.configureValue("default", "settings.php.extension-sdk", "true");
        assert(env.phpBuildTools());
        assert(readText(store.configFile).canFind("settings:\n  php:\n    extension-sdk: true"));

        env = store.configureValue("default", "settings.php.extension-sdk", "false");
        assert(!env.phpBuildTools());

        bool threwBool = false;
        try { store.configureValue("default", "settings.php.extension-sdk", "maybe"); }
        catch (Exception e) { threwBool = e.msg.canFind("requires a boolean value"); }
        assert(threwBool);

        bool threwKey = false;
        try { store.configureValue("default", "settings.php.unknown", "true"); }
        catch (Exception e) { threwKey = e.msg.canFind("unsupported config key"); }
        assert(threwKey);

        store.configureValue("default", "tools.php-zts", "8.4");
        env = store.configureValue("default", "settings.php.extension-sdk", "true");
        assert(env.phpBuildTools() && env.toolVersion(toolPHPZTS) == "8.4");
    }

    // TestCustomToolPluginExtensibility
    {
        auto projectDir = createTempDir("polka-cv-acme-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto pluginsList = newDefaultToolRegistry().plugins().dup ~ cast(ToolPlugin) new FakeAcmePlugin();
        auto registry = newToolRegistry(pluginsList);

        auto store = newProjectStore(projectDir);
        store.plugins = registry;

        store.configureValue("default", "tools.acme", "1.2");
        store.configureValue("default", "settings.acme.port", "9090");
        store.configureValue("default", "settings.acme.token", "secret-token");
        auto env = store.configureValue("default", "settings.acme.tls", "true");

        assert(env.toolVersion("acme") == "1.2");
        assert(env.settingInt("acme", "port") == 9090);
        assert(env.settingString("acme", "token") == "secret-token");
        assert(env.settingBool("acme", "tls"));

        auto reloaded = store.current();
        assert(reloaded !is null && reloaded.toolVersion("acme") == "1.2"
            && reloaded.settingInt("acme", "port") == 9090
            && reloaded.settingString("acme", "token") == "secret-token"
            && reloaded.settingBool("acme", "tls"));

        bool threw = false;
        try { store.configureValue("default", "settings.acme.unknown", "1"); }
        catch (Exception) { threw = true; }
        assert(threw);
    }

    // TestFrameworkDefaultsPassEnvironmentFileSchema
    {
        auto projectDir = createTempDir("polka-cv-schema-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);

        foreach (id; store.supportedFrameworks())
        {
            auto env = store.frameworkDefaults(id);
            auto yamlDoc = environmentFileToYAML(environmentFileFromEnvironment(env));
            validateEnvironmentFileSchema(emitYAML(yamlDoc), store.toolRegistry());
        }
        foreach (id; store.supportedPresets())
        {
            auto env = store.presetDefaults(id);
            auto yamlDoc = environmentFileToYAML(environmentFileFromEnvironment(env));
            validateEnvironmentFileSchema(emitYAML(yamlDoc), store.toolRegistry());
        }
    }

    // TestLegacyProjectManifestCannotBeBypassed
    foreach (header; ["version: 0.1\n", "version: 1\n", ""])
    {
        auto projectDir = createTempDir("polka-mv-store-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        auto original = header ~ "root: .polka\ntools:\n  php: '8.4'\n";
        write(store.configFile, original);
        enum named = "tools:\n  php: '8.4'\n";
        auto demoPath = buildPath(store.projectDir, "polka.demo.yaml");
        write(demoPath, named);

        foreach (check; [
            () { bool ok; store.readEnvironment("default", ok); },
            () { bool ok; store.readEnvironment("demo", ok); },
            () { storeForRoot(store.rootDir); },
            () { store.writeEnvironmentConfig("demo", Environment("demo")); },
        ])
        {
            bool saw = false;
            try { check(); }
            catch (Exception e) { saw = e.msg.canFind("delete " ~ store.configFile) && e.msg.canFind("polka init"); }
            assert(saw);
        }

        assert(readText(store.configFile) == original);
        assert(readText(demoPath) == named);

        remove(store.configFile);
        store.init();
        assert(readText(store.configFile).startsWith("version: 0.2\n"));
        bool ok = false;
        store.readEnvironment("demo", ok);
        assert(ok);
    }

    // TestStoreRejectsInvalidWorkersSchema
    {
        struct WorkerErrCase { string cfg; string wantErr; }
        foreach (tc; [
            WorkerErrCase("workers: queue\n", "workers must be a mapping"),
            WorkerErrCase("workers:\n  \"bad name\":\n    command: php run\n", "invalid workers.bad name key"),
            WorkerErrCase("workers:\n  queue: php artisan queue:work\n", "workers.queue must be a mapping"),
            WorkerErrCase("workers:\n  queue:\n    replicas: 2\n", "workers.queue.command must be a non-empty string"),
            WorkerErrCase("workers:\n  queue:\n    command: \"  \"\n", "workers.queue.command must be a non-empty string"),
            WorkerErrCase("workers:\n  queue:\n    command: php \"artisan\n", "workers.queue.command is invalid"),
            WorkerErrCase("workers:\n  queue:\n    command: php artisan queue:work\n    restart: always\n", "unsupported workers.queue.restart key"),
            WorkerErrCase("workers:\n  queue:\n    command: php artisan queue:work\n    replicas: 0\n", "workers.queue.replicas must be a positive integer"),
            WorkerErrCase("workers:\n  queue:\n    command: php artisan queue:work\n    replicas: two\n", "workers.queue.replicas must be a positive integer"),
            WorkerErrCase("workers:\n  queue:\n    command: php artisan queue:work\n    dir: \"\"\n", "workers.queue.dir must be a non-empty single-line string"),
            WorkerErrCase("workers:\n  queue:\n    command: php artisan queue:work\n    env: TRIES=3\n", "workers.queue.env must be a mapping"),
            WorkerErrCase("workers:\n  queue:\n    command: php artisan queue:work\n    env:\n      TRIES:\n        nested: true\n", "workers.queue.env.TRIES must be a scalar value"),
        ])
        {
            auto projectDir = createTempDir("polka-st-werr-");
            scope (exit) rmdirRecurseQuiet(projectDir);
            auto store = newProjectStore(projectDir);
            write(store.configFile, "version: 0.2\nroot: .polka\n");
            write(store.environmentConfigFile("data"), tc.cfg);

            bool ok = false;
            bool threw = false;
            try { store.readEnvironment("data", ok); }
            catch (Exception e) { threw = e.msg.canFind(tc.wantErr); }
            assert(threw, tc.wantErr);
        }
    }

    // TestStoreReadsWorkersConfig
    {
        auto projectDir = createTempDir("polka-st-wrd-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        write(store.configFile, "version: 0.2\nroot: .polka\n");
        write(store.environmentConfigFile("data"),
            "workers:\n  queue:\n    command: php artisan queue:work\n    replicas: 2\n    dir: app\n" ~
            "    env:\n      TRIES: \"3\"\n  scheduler:\n    command: php artisan schedule:work\n");

        bool ok = false;
        auto env = store.readEnvironment("data", ok);
        assert(ok);
        auto queue = env.workers["queue"];
        assert(queue.command == "php artisan queue:work" && queue.replicas == 2 && queue.dir == "app" && queue.env["TRIES"] == "3");
        auto sched = env.workers["scheduler"];
        assert(sched.command == "php artisan schedule:work" && sched.replicas == 0);
    }

    // TestStoreInitWithFrameworkWritesWorkers
    foreach (tc; [
        ["laravel", "queue", "php artisan queue:work"],
        ["symfony", "messenger", "php bin/console messenger:consume async"],
    ])
    {
        auto projectDir = createTempDir("polka-st-wfw-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.initWithFramework(tc[0]);

        auto cfg = store.loadConfig();
        auto env = cfg.environments[defaultEnvironmentName];
        auto w = env.workers[tc[1]];
        assert(w.command == tc[2]);
        auto data = readText(store.configFile);
        assert(data.canFind("workers:") && data.canFind(tc[2]));
    }

    // TestStoreWritesWorkersConfig
    {
        auto projectDir = createTempDir("polka-st-wwr-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        store.init();

        auto cfg = store.loadConfig();
        auto env = cfg.environments[defaultEnvironmentName];
        env.workers = ["queue": WorkerConfig("php artisan queue:work", 2)];
        cfg.environments[defaultEnvironmentName] = env;
        store.writeConfig(cfg);

        auto reloaded = store.loadConfig();
        auto w = reloaded.environments[defaultEnvironmentName].workers["queue"];
        assert(w.command == "php artisan queue:work" && w.replicas == 2);
    }
}

// ============================================================================
// MSVC Detection & PECL Store Integration Tests
// ============================================================================

unittest
{
    // TestDetectMSVCToolchainCapturesAndReuses (cross-platform offline stubbing of detectMSVCToolchain)
    {
        auto projectDir = createTempDir("polka-msvc-det-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto vsRoot = createTempDir("polka-vs-inst-");
        scope (exit) rmdirRecurseQuiet(vsRoot);
        auto vswhere = buildPath(vsRoot, "vswhere.exe");
        write(vswhere, "");
        auto instPath = buildPath(vsRoot, "BuildTools");
        auto vcvars = buildPath(instPath, "VC", "Auxiliary", "Build", "vcvarsall.bat");
        mkdirRecurse(dirName(vcvars));
        write(vcvars, "");

        auto origLoc = msvcVSWhereLocationHook;
        auto origList = msvcListInstancesHook;
        auto origCap = msvcCaptureVCVarsHook;
        auto origMaj = msvcDetectPHPMajorHook;
        auto origTs = msvcDetectPHPToolsetHook;
        scope (exit)
        {
            msvcVSWhereLocationHook = origLoc;
            msvcListInstancesHook = origList;
            msvcCaptureVCVarsHook = origCap;
            msvcDetectPHPMajorHook = origMaj;
            msvcDetectPHPToolsetHook = origTs;
        }

        int captures = 0;
        msvcVSWhereLocationHook = () => [vswhere];
        msvcListInstancesHook = (string _) => [MSVCInstance(instPath, "17.14.1", "Visual Studio Build Tools 2022", false, "17.14.2")];
        msvcCaptureVCVarsHook = (string _a, string _b, string _c) {
            captures++;
            return MSVCCaptureResult(
                [`Path=C:\Windows`],
                [`Path=C:\VS\bin\Hostx64\x64;C:\Windows`, `INCLUDE=C:\VS\include`, `VCToolsInstallDir=C:\VS\Tools\`]
            );
        };
        msvcDetectPHPMajorHook = (string _) => 0;
        msvcDetectPHPToolsetHook = (string _) => "";

        auto store = newProjectStore(projectDir);
        Environment env;
        env.name = "default";
        env.setSettingBool(toolPHP, "extension-sdk", true);

        auto stateFile = store.msvcStateFile("default");
        auto tc1 = store.detectMSVCToolchain(env, stateFile, false);
        writeMSVCToolchain(stateFile, tc1);
        store.detectMSVCToolchain(env, stateFile, false);
        auto tc3 = store.detectMSVCToolchain(env, stateFile, true);
        assert(captures == 2);
        assert(tc3.version_ == "17.14.2" && tc3.arch == msvcTargetArch);
        assert(tc3.prepend["PATH"] == [`C:\VS\bin\Hostx64\x64`]);

        // Windows-only full syncMSVCToolchain + warning clearing path
        version (Windows)
        {
            captures = 0;
            remove(stateFile);
            foreach (force; [false, false, true])
                store.syncMSVCToolchain(env, force, (string msg) { assert(false, msg); });
            assert(captures == 2);
            auto loaded = store.msvcToolchain(env);
            assert(loaded !is null && loaded.version_ == "17.14.2");
            assert(store.msvcToolchain(Environment("default")) is null);

            msvcListInstancesHook = (string _) => cast(MSVCInstance[]) null;
            string[] warns;
            store.syncMSVCToolchain(env, false, (string msg) { warns ~= msg; });
            assert(warns.length == 1 && warns[0].canFind("no Visual Studio installation"));
            assert(store.msvcToolchain(env) is null);
        }
    }

    // TestWithInstalledPECLExtensionsFoldsTrackedModules
    {
        auto tmp = createTempDir("polka-pecl-fold-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto store = newStore(buildPath(tmp, ".polka"));
        auto installRoot = buildPath(store.envsDir, toolPHP, "8.4");
        mkdirRecurse(installRoot);

        peclInstallState st;
        st.packages = [
            "redis": peclInstalledPackage("6.2.0", "redis", false, null),
            "xdebug": peclInstalledPackage("3.4.1", "xdebug", true, null),
        ];
        writePECLInstallState(installRoot, st);

        Environment env;
        env.tools = [toolPHP: "8.4"];
        env.phpExtensions = ["redis": false];
        env.peclExtensions = [
            "redis": PECLExtensionConfig("6.2.0"),
            "xdebug": PECLExtensionConfig("3.4.1"),
        ];

        auto merged = store.withInstalledPECLExtensions(env);
        assert(!merged.phpExtensions["redis"]);
        assert(merged.phpExtensions["xdebug"] && merged.zendExtensions["xdebug"]);
    }

    // TestResolvePECLPackageOffline exercises REST XML metadata resolution & PHP compatibility via Store
    version (Posix)
    {
        auto projectDir = createTempDir("polka-pecl-res-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        auto installRoot = buildPath(store.envsDir, toolPHP, "8.4", "bin");
        mkdirRecurse(installRoot);
        auto phpPath = buildPath(installRoot, "php");
        write(phpPath, "#!/bin/sh\nif [ \"$1\" = \"-n\" ]; then echo \"8.4.1~8.4~0~x64\"; exit 0; fi\nexit 0\n");
        setFileMode(phpPath, octal!755);

        auto origREST = peclRESTBaseURL;
        scope (exit) peclRESTBaseURL = origREST;
        peclRESTBaseURL = "https://mock.pecl/rest";

        auto client = new MockHTTPClient((string url, string[string]) {
            if (url == "https://mock.pecl/rest/r/redis/6.2.0.xml")
                return HTTPResponse(200, null, cast(ubyte[]) `<r><v>6.2.0</v><st>stable</st><g>https://mock.pecl/get/redis-6.2.0.tgz</g></r>`);
            if (url == "https://mock.pecl/rest/r/redis/package.6.2.0.xml")
                return HTTPResponse(200, null, cast(ubyte[]) `<package><name>redis</name><version><release>6.2.0</release></version><dependencies><required><php><min>8.0.0</min><max>8.4.99</max></php></required></dependencies><providesextension>redis</providesextension><extsrcrelease><configureoption name="enable-redis-igbinary" default="no" prompt="enable igbinary"/></extsrcrelease></package>`);
            return HTTPResponse(404, null, null);
        });
        store.downloader = new HTTPToolDownloader(client);

        auto resolution = store.resolvePECLPackage(phpPath, "redis", "6.2.0");
        assert(resolution.packageName == "redis" && resolution.version_ == "6.2.0" && resolution.module_ == "redis");
        assert(resolution.downloadURL == "https://mock.pecl/get/redis-6.2.0.tgz");
        assert("enable-redis-igbinary" in resolution.configure);
    }

    // TestInstallAndRemovePECLExtensionWindows (Windows-only full DLL install/remove integration)
    version (Windows)
    {
        auto archiveData = buildZipBytes([
            ArchiveEntrySpec("php_redis.dll", cast(const(ubyte)[]) "redis-dll"),
            ArchiveEntrySpec("redis_dependency.dll", cast(const(ubyte)[]) "dependency-dll"),
        ]);

        auto client = new MockHTTPClient((string url, string[string]) {
            switch (url)
            {
                case "https://mock.pecl/rest/r/redis/6.2.0.xml":
                    return HTTPResponse(200, null, cast(ubyte[]) `<r><v>6.2.0</v><st>stable</st><g>ignored-on-windows</g></r>`);
                case "https://mock.pecl/rest/r/redis/package.6.2.0.xml":
                    return HTTPResponse(200, null, cast(ubyte[]) `<package><name>redis</name><version><release>6.2.0</release></version><dependencies><required><php><min>8.0.0</min><max>8.4.99</max></php></required></dependencies><providesextension>redis</providesextension><extsrcrelease/></package>`);
                case "https://mock.pecl/windows/redis/6.2.0/":
                    return HTTPResponse(200, null, cast(ubyte[]) `<a href="php_redis-6.2.0-8.4-nts-vs17-x64.zip">DLL</a>`);
                case "https://mock.pecl/windows/redis/6.2.0/php_redis-6.2.0-8.4-nts-vs17-x64.zip":
                    return HTTPResponse(200, null, archiveData.dup);
                default:
                    return HTTPResponse(404, null, null);
            }
        });

        auto origREST = peclRESTBaseURL;
        auto origWin = peclWindowsBaseURL;
        scope (exit)
        {
            peclRESTBaseURL = origREST;
            peclWindowsBaseURL = origWin;
        }
        peclRESTBaseURL = "https://mock.pecl/rest";
        peclWindowsBaseURL = "https://mock.pecl/windows";

        auto projectDir = createTempDir("polka-pecl-win-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");
        auto store = newStore(root);
        store.downloader = new HTTPToolDownloader(client);

        Environment env;
        env.name = defaultEnvironmentName;
        env.tools = [toolPHP: "8.4"];
        writeYAML(store.configFile, projectFileFromEnvironment("0.2", ".polka", env));

        auto installRoot = buildPath(store.envsDir, toolPHP, "8.4");
        mkdirRecurse(installRoot);
        auto phpPath = buildPath(installRoot, "php.cmd");
        write(phpPath, "@echo off\r\nif \"%1\"==\"-n\" echo 8.4.1~8.4~0~x64& exit /b 0\r\nif \"%1\"==\"-m\" echo Core& exit /b 0\r\nexit /b 0\r\n");

        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();
        auto res = store.installPECLExtension(outBuf, errBuf, "default", "redis", "6.2.0", null);
        assert(res.version_ == "6.2.0" && res.module_ == "redis");

        auto modulePath = buildPath(installRoot, "ext", "php_redis.dll");
        assertPathExists(modulePath);
        store.removePECLExtension(errBuf, "default", "redis");
        assertPathMissing(modulePath);
    }
}
