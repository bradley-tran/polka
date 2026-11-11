module polka.backend.store;

import core.sync.mutex : Mutex;
import core.thread : Thread;
import std.algorithm : canFind, sort, startsWith;
import std.array : join;
import std.file : dirEntries, exists, getcwd, isDir, mkdirRecurse, remove, SpanMode, write;
import std.format : format;
import std.path : absolutePath, baseName, buildPath, dirName, isAbsolute;
import std.process : environment;
import std.string : endsWith, icmp, strip, toLower;

import polka.backend.aliases;
import polka.backend.configure_value;
import polka.backend.database : mergeDatabaseConfig, normalizeDatabaseConfig, setDatabaseToolVersion;
import polka.backend.internal_tools;
import polka.backend.msvc;
import polka.backend.pecl;
import polka.backend.pie;
import polka.backend.schema;
import polka.backend.state;
import polka.config :
    cloneEnvironment,
    environmentFileFromEnvironment,
    environmentFileFromYAML,
    environmentFileToEnvironment,
    EnvironmentFile,
    normalizeEnvironment_ = normalizeEnvironment,
    normalizePHPExtensions,
    normalizePIEExtensions,
    primaryPHPTool,
    primaryPHPVersion,
    projectFileFromEnvironment,
    projectFileFromYAML,
    projectFileToEnvironment,
    ProjectFile,
    validatePECLExtensionPackage,
    validatePECLExtensionVersion,
    validatePIEExtensionPackage,
    validateProjectManifestVersion;
import polka.plugins : defaultFrameworkPlugins, defaultProjectPresets;
import polka.support.fs :
    cleanPath,
    createTempDir,
    currentArch,
    currentExecutablePath,
    currentOS,
    fromSlashPath,
    isPathWithin,
    octal,
    pathsEqual,
    rmdirRecurseQuiet,
    setFileMode,
    toSlashPath,
    writeFileAtomic;
import polka.support.http : CurlHTTPClient, HTTPClient, HTTPResponse;
import polka.support.yaml : parseYAML;
import polka.tools :
    acquireDirLock,
    cachedToolPayload,
    DefaultPHPSDKVersion,
    effectivePHPConfigForInstall,
    installCachedToolPayload,
    installedPHPModules,
    normalizeLogLevel,
    pieExtensionModuleName,
    resyncInstalledPHPRuntimeConfig,
    validLogLevel;

/// Progress phase reported during managed tool/extension installation.
enum InstallProgressStage : string
{
    usingCache  = "using cache",
    downloading = "downloading",
    installing  = "installing",
    configuring = "configuring",
    installed   = "installed",
    skipped     = "unchanged",
}

alias InstallProgressUsingCache  = InstallProgressStage.usingCache;
alias InstallProgressDownloading = InstallProgressStage.downloading;
alias InstallProgressInstalling  = InstallProgressStage.installing;
alias InstallProgressConfiguring = InstallProgressStage.configuring;
alias InstallProgressInstalled   = InstallProgressStage.installed;
alias InstallProgressSkipped     = InstallProgressStage.skipped;

/// Single tool or extension progress update event.
struct InstallProgress
{
    int index = 0;
    int total = 0;
    string tool;
    string version_;
    InstallProgressStage stage = InstallProgressStage.installing;
}

/// Adjusts environment install behavior (forced reinstalls, non-fatal warnings).
struct InstallOptions
{
    bool force = false;
    void delegate(string) warn;
}

/// Customizes project initialization via `polka init` / `polka new`.
struct InitOptions
{
    string docroot;
    string packageName;
}

/// Reports preset scaffold files written or preserved during initialization.
struct InitResult
{
    string[] scaffoldWritten;
    string[] scaffoldSkipped;
}

/// Invokes `report` when non-null with `stage` populated on `progress`.
void emitInstallProgress(
    void delegate(InstallProgress) report,
    InstallProgress progress,
    InstallProgressStage stage
)
{
    if (report is null)
        return;
    progress.stage = stage;
    report(progress);
}

/// Central project environment store coordinating config files, downloads, installs, shims, PECL/PIE, and MSVC state.
struct Store
{
    string projectDir;
    string rootDir;
    string envsDir;
    string binDir;
    string configFile;
    string cacheDir;
    ToolDownloader downloader;
    ToolRegistry plugins;
    PluginRegistry registry;

    /// Returns the active managed tool registry.
    ToolRegistry toolRegistry()
    {
        if (plugins !is null)
            return plugins;
        if (registry !is null)
            return registry.toolRegistry();
        return newDefaultToolRegistry();
    }

    /// Returns the active plugin registry (frameworks, presets, tools).
    PluginRegistry pluginRegistry()
    {
        if (registry !is null)
            return registry;
        auto reg = new PluginRegistry(toolRegistry(), defaultFrameworkPlugins());
        foreach (preset; defaultProjectPresets())
            reg.registerPreset(preset);
        return reg;
    }

    /// Returns registered framework IDs in sorted order.
    string[] supportedFrameworks()
    {
        return pluginRegistry().supportedFrameworks();
    }

    /// Detects framework ID for `pkg` inside `dir`.
    string detectFramework(string pkg, string dir)
    {
        return pluginRegistry().detectFramework(pkg, dir);
    }

    /// Returns registered project preset IDs in sorted order.
    string[] supportedPresets()
    {
        return pluginRegistry().supportedPresets();
    }

    /// Resolves a framework plugin by ID.
    bool frameworkPlugin(string id, out FrameworkPlugin outPlugin)
    {
        return pluginRegistry().framework(id, outPlugin);
    }

    /// Returns a validated default `Environment` for a framework.
    Environment frameworkDefaults(string id)
    {
        auto frameworkID = id.strip().toLower();
        pluginRegistry().validateFramework(frameworkID);

        FrameworkPlugin plugin;
        if (!pluginRegistry().framework(frameworkID, plugin))
        {
            throw new Exception(format(
                "unsupported framework %s; supported frameworks: %s",
                quoteGoString(id), supportedFrameworks().join(", ")
            ));
        }

        auto env = normalizeEnvironment(defaultEnvironmentName, plugin.defaults());
        toolRegistry().validateEnvironment(env);
        return env;
    }

    /// Returns a validated default `Environment` for a project preset.
    Environment presetDefaults(string id)
    {
        auto presetID = id.strip().toLower();
        ProjectPreset preset;
        if (!pluginRegistry().preset(presetID, preset))
        {
            throw new Exception(format(
                "unsupported project preset %s; supported presets: %s",
                quoteGoString(id), supportedPresets().join(", ")
            ));
        }

        auto env = normalizeEnvironment(defaultEnvironmentName, preset.defaults());
        toolRegistry().validateEnvironment(env);
        return env;
    }

    /// Returns Windows PHP extension SDK PATH entries when enabled.
    string[] phpBuildToolPathEntries(const Environment env) const
    {
        if (!env.phpBuildTools() || currentOS() != "windows")
            return null;
        auto ver = primaryPHPVersion(env).strip();
        if (ver.length == 0)
            return null;
        auto sdkDir = buildPath(envsDir, toolPHPSDK, DefaultPHPSDKVersion);
        return [
            buildPath(envsDir, toolPHPDevel, ver),
            sdkDir,
            buildPath(sdkDir, "msys2", "usr", "bin"),
        ];
    }

    /// Returns local directory of configured PHP runtime on Windows when build tools are enabled.
    string phpRuntimePrefix(const Environment env) const
    {
        if (!env.phpBuildTools() || currentOS() != "windows")
            return "";
        string tool, ver;
        primaryPHPTool(env, tool, ver);
        if (tool.length == 0 || ver.length == 0)
            return "";
        return buildPath(envsDir, tool, ver);
    }

    /// Returns the global internal tools directory for this store.
    string internalToolsDir() const
    {
        return polka.backend.internal_tools.internalToolsDir(projectDir);
    }

    package(polka.backend) Store internalStore() const
    {
        auto toolsDir = internalToolsDir();
        Store s = cast() this;
        s.envsDir = toolsDir;
        s.configFile = buildPath(toolsDir, internalConfigFileName);
        return s;
    }

    private Environment resolveInitPreset(string id, out bool isFramework)
    {
        auto trimmed = id.strip().toLower();
        FrameworkPlugin fw;
        if (pluginRegistry().framework(trimmed, fw))
        {
            isFramework = true;
            return frameworkDefaults(trimmed);
        }
        ProjectPreset preset;
        if (pluginRegistry().preset(trimmed, preset))
        {
            isFramework = false;
            return presetDefaults(trimmed);
        }
        throw new Exception(format(
            "unsupported framework or preset %s; supported frameworks: %s; supported presets: %s",
            quoteGoString(id), supportedFrameworks().join(", "), supportedPresets().join(", ")
        ));
    }

    /// Initializes `.polka` and writes `polka.yaml` if absent.
    void init()
    {
        initWithOptions(InitOptions.init);
    }

    /// Initializes project with docroot / package options.
    void initWithOptions(InitOptions options)
    {
        initInternal("", options);
    }

    /// Initializes project from a framework ID.
    void initWithFramework(string framework)
    {
        initWithFrameworkOptions(framework, InitOptions.init);
    }

    /// Initializes project from a framework ID with options.
    void initWithFrameworkOptions(string framework, InitOptions options)
    {
        initWithPresetOptions(framework, options);
    }

    /// Initializes project from either a framework or project preset, writing preset scaffold files.
    InitResult initWithPresetOptions(string id, InitOptions options)
    {
        return initInternal(id, options);
    }

    private InitResult initInternal(string id, InitOptions options)
    {
        InitResult result;
        auto docroot = options.docroot.strip();
        auto packageName = options.packageName.strip().toLower();

        bool hasInitialEnv = false;
        Environment initialEnv;
        ProjectPreset projectPreset = null;
        bool isFramework = false;

        if (id.strip().length > 0)
        {
            initialEnv = resolveInitPreset(id, isFramework);
            hasInitialEnv = true;
            if (packageName.length > 0)
            {
                if (isFramework)
                    throw new Exception("--package can only be used with a project preset");
                validatePIEExtensionPackage(packageName);
            }
            if (docroot.length > 0)
                initialEnv.docroot = docroot;

            bool cfgIsDir = false;
            if (statPathExists(configFile, cfgIsDir))
            {
                throw new Exception(format(
                    "config file %s already exists; framework or preset init would overwrite it",
                    configFile
                ));
            }

            if (!isFramework)
            {
                if (!pluginRegistry().preset(id, projectPreset))
                    throw new Exception(format("resolve project preset %s after validation", quoteGoString(id)));
            }
        }
        else if (packageName.length > 0)
        {
            throw new Exception("--package requires a project preset");
        }

        try
        {
            mkdirRecurse(envsDir);
        }
        catch (Exception e)
        {
            throw new Exception("create environment root: " ~ e.msg);
        }
        try
        {
            mkdirRecurse(binDir);
        }
        catch (Exception e)
        {
            throw new Exception("create binary root: " ~ e.msg);
        }

        try
        {
            auto loadedConfig = readConfig();
            if (!hasInitialEnv && docroot.length > 0)
            {
                auto env = cloneEnvironment(loadedConfig.environments.get(defaultEnvironmentName, Environment(defaultEnvironmentName)));
                env.docroot = docroot;
                loadedConfig.environments[defaultEnvironmentName] = env;
                try
                {
                    writeConfig(loadedConfig);
                }
                catch (Exception e)
                {
                    throw new Exception("write config file: " ~ e.msg);
                }
            }
        }
        catch (ConfigNotExistException)
        {
            auto cfg = defaultConfig();
            if (hasInitialEnv)
            {
                if (isFramework)
                    initialEnv = withInitDefaults(cloneEnvironment(initialEnv));
                cfg.environments[defaultEnvironmentName] = initialEnv;
            }
            else if (docroot.length > 0)
            {
                auto env = cloneEnvironment(cfg.environments[defaultEnvironmentName]);
                env.docroot = docroot;
                cfg.environments[defaultEnvironmentName] = env;
            }
            try
            {
                writeConfig(cfg);
            }
            catch (Exception e)
            {
                throw new Exception("write config file: " ~ e.msg);
            }
        }

        if (projectPreset !is null)
            result = writePresetScaffold(projectPreset, initialEnv, options);

        try
        {
            installBinaries();
        }
        catch (Exception e)
        {
            throw new Exception("install binaries: " ~ e.msg);
        }

        return result;
    }

    private InitResult writePresetScaffold(ProjectPreset preset, const Environment env, InitOptions options)
    {
        InitResult result;
        auto files = preset.scaffoldFiles(ScaffoldContext(
            projectDir,
            options.packageName,
            env.toolVersion(toolPHP)
        ));

        foreach (file; files)
        {
            validatePresetScaffoldTarget(file.path);
            auto target = cleanPath(buildPath(projectDir, fromSlashPath(file.path)));
            if (!isPathWithin(projectDir, target))
                throw new Exception(format("preset scaffold path %s escapes project directory", quoteGoString(file.path)));

            auto parent = dirName(target);
            try
            {
                if (!exists(parent))
                    mkdirRecurse(parent);
            }
            catch (Exception e)
            {
                throw new Exception(format("create scaffold parent for %s: %s", file.path, e.msg));
            }

            bool created;
            try
            {
                created = tryCreateExclusiveFile(target, file.content, octal!644);
            }
            catch (Exception e)
            {
                throw new Exception(format("write scaffold file %s: %s", file.path, e.msg));
            }

            auto slashPath = toSlashPath(file.path);
            if (!created)
                result.scaffoldSkipped ~= slashPath;
            else
                result.scaffoldWritten ~= slashPath;
        }

        return result;
    }

    /// Returns all configured environments sorted by name.
    Environment[] list()
    {
        auto cfg = loadConfig();
        auto names = cfg.environments.keys;
        names.sort();

        Environment[] environments;
        environments.reserve(names.length);
        foreach (name; names)
            environments ~= normalizeEnvironment(name, cfg.environments[name]);
        return environments;
    }

    /// Creates a new named environment, failing if it already exists.
    Environment create(
        string name,
        string phpVersion = "",
        string composerVersion = "",
        string nodeJSVersion = "",
        const(DatabaseConfig)* database = null
    )
    {
        return writeEnvironment(name, phpVersion, composerVersion, nodeJSVersion, database, false);
    }

    /// Creates or updates a project environment configuration.
    Environment configure(
        string name,
        string phpVersion = "",
        string composerVersion = "",
        string nodeJSVersion = "",
        const(DatabaseConfig)* database = null
    )
    {
        return writeEnvironment(name, phpVersion, composerVersion, nodeJSVersion, database, true);
    }

    private Environment writeEnvironment(
        string name,
        string phpVersion,
        string composerVersion,
        string nodeJSVersion,
        const(DatabaseConfig)* database,
        bool allowUpdate
    )
    {
        init();
        validateName(name);

        phpVersion = phpVersion.strip();
        composerVersion = composerVersion.strip();
        nodeJSVersion = nodeJSVersion.strip();
        auto normDB = normalizeDatabaseConfig(database);

        if (phpVersion.length > 0)
            validateVersion(toolPHP, phpVersion);
        if (composerVersion.length > 0)
            validateVersion(toolComposer, composerVersion);
        if (nodeJSVersion.length > 0)
            validateVersion(toolNodeJS, nodeJSVersion);

        bool envExists = false;
        auto stored = readEnvironment(name, envExists);
        if (envExists && !allowUpdate)
            throw new Exception(format("environment %s already exists", quoteGoString(name)));

        auto env = normalizeEnvironment(name, cloneEnvironment(stored));
        if (phpVersion.length > 0)
            env.setToolVersion(toolPHP, phpVersion);
        if (composerVersion.length > 0)
            env.setToolVersion(toolComposer, composerVersion);
        if (nodeJSVersion.length > 0)
            env.setToolVersion(toolNodeJS, nodeJSVersion);
        if (normDB !is null)
        {
            env.database = mergeDatabaseConfig(env.database, normDB);
            env = setDatabaseToolVersion(env, normDB);
        }

        if (env.tools.length == 0 && env.database is null)
        {
            throw new Exception(
                "environment requires at least one of php, php-zts, frankenphp, roadrunner, composer, nodejs, mago, nginx, apache, mysql, mariadb, postgresql, sqlite, phpmyadmin, database, mailpit, meilisearch, redis, rabbitmq, or traefik"
            );
        }
        toolRegistry().validateEnvironment(env);

        try
        {
            writeEnvironmentConfig(name, env);
        }
        catch (Exception e)
        {
            throw new Exception("write config file: " ~ e.msg);
        }
        auto cfg = loadConfig();
        try
        {
            syncManagedBinaries(cfg);
        }
        catch (Exception e)
        {
            throw new Exception("sync managed binaries: " ~ e.msg);
        }

        return env;
    }

    /// Creates or updates a single schema-aware config key on `name`.
    Environment configureValue(string name, string key, string value)
    {
        init();
        validateName(name);

        auto path = parseConfigValuePath(key);
        bool existsFlag = false;
        auto stored = readEnvironment(name, existsFlag);

        auto reg = toolRegistry();
        auto env = normalizeEnvironment(name, cloneEnvironment(stored));
        applyConfigValue(reg, env, path, value);
        env = normalizeConfigValueEnvironment(env);
        validateEnvironmentFramework(env);
        reg.validateEnvironment(env);

        try
        {
            writeEnvironmentConfig(name, env);
        }
        catch (Exception e)
        {
            throw new Exception("write config file: " ~ e.msg);
        }
        auto cfg = loadConfig();
        try
        {
            syncManagedBinaries(cfg);
        }
        catch (Exception e)
        {
            throw new Exception("sync managed binaries: " ~ e.msg);
        }

        return env;
    }

    /// Records a resolved legacy PECL extension (including configure options), or removes it if `extension.version_` is empty.
    Environment configurePECLExtension(string name, string packageName, const PECLExtensionConfig extension)
    {
        init();
        validateName(name);
        packageName = packageName.strip().toLower();
        validatePECLExtensionPackage(packageName);

        bool existsFlag = false;
        auto stored = readEnvironment(name, existsFlag);
        auto env = normalizeEnvironment(name, cloneEnvironment(stored));
        if (extension.version_.strip().length == 0)
        {
            if (packageName in env.peclExtensions)
                env.peclExtensions.remove(packageName);
        }
        else
        {
            validatePECLExtensionVersion(packageName, extension.version_);
            PECLExtensionConfig cloned;
            cloned.version_ = extension.version_;
            if (extension.configureOptions.length > 0)
            {
                foreach (k, v; extension.configureOptions)
                    cloned.configureOptions[k] = v;
            }
            env.peclExtensions[packageName] = cloned;
        }

        env = normalizeConfigValueEnvironment(env);
        validateEnvironmentFramework(env);
        toolRegistry().validateEnvironment(env);

        try
        {
            writeEnvironmentConfig(name, env);
        }
        catch (Exception e)
        {
            throw new Exception("write config file: " ~ e.msg);
        }
        auto cfg = loadConfig();
        try
        {
            syncManagedBinaries(cfg);
        }
        catch (Exception e)
        {
            throw new Exception("sync managed binaries: " ~ e.msg);
        }

        return env;
    }

    /// Installs all tools, PIE/PECL extensions, and MSVC state for `name`.
    InstallResult[] install(string name)
    {
        return installInternal(name, InstallOptions.init, null);
    }

    /// Installs environment tools with options and progress reporting.
    InstallResult[] installWithProgress(
        string name,
        InstallOptions options,
        void delegate(InstallProgress) report = null
    )
    {
        return installInternal(name, options, report);
    }

    /// Installs one explicit `(tool, ver)` version and persists it onto `name` on success.
    InstallResult installTool(string name, string tool, string ver)
    {
        return installToolWithProgress(name, tool, ver, null);
    }

    /// Installs one explicit `(tool, ver)` with progress reporting before updating config & shims.
    InstallResult installToolWithProgress(
        string name,
        string tool,
        string ver,
        void delegate(InstallProgress) report = null
    )
    {
        ToolRegistry reg;
        auto env = installEnvironment(name, reg);
        auto request = normalizeInstallRequest(reg, tool, ver);
        env = environmentWithInstallRequest(reg, cloneEnvironment(env), request);
        validateInstallEnvironment(name, env, [request], reg);

        auto results = installRequestsInternal(env, [request], true, report);
        if (results.length == 0)
            throw new Exception(format("no install result produced for %s %s", request.tool, request.version_));

        if (request.tool == toolPHP || request.tool == toolPHPZTS)
        {
            installPIEExtensions(env, report);
            installPECLExtensions(env, report);
            syncPECLRuntimeConfig(env);
        }

        try
        {
            writeEnvironmentConfig(name, env);
        }
        catch (Exception e)
        {
            throw new Exception("write config file: " ~ e.msg);
        }
        auto cfg = loadConfig();
        try
        {
            syncManagedBinaries(cfg);
        }
        catch (Exception e)
        {
            throw new Exception("sync managed binaries: " ~ e.msg);
        }

        foreach (res; results)
        {
            if (res.tool == request.tool)
                return res;
        }
        throw new Exception(format("no install result produced for %s %s", request.tool, request.version_));
    }

    /// Returns topologically ordered install requests for `name` without downloading or installing.
    InstallRequest[] installRequests(string name)
    {
        validateName(name);
        bool ok = false;
        auto env = readEnvironment(name, ok);
        if (!ok)
            throw new Exception(format("environment %s does not exist", quoteGoString(name)));

        auto normalized = normalizeEnvironment(name, env);
        auto layers = toolRegistry().installRequestLayers(normalized);
        InstallRequest[] result;
        foreach (layer; layers)
        {
            foreach (req; layer)
                result ~= InstallRequest(req.tool, req.version_);
        }
        return result;
    }

    private InstallResult[] installInternal(
        string name,
        InstallOptions options,
        void delegate(InstallProgress) report
    )
    {
        ToolRegistry reg;
        auto env = installEnvironment(name, reg);
        auto requests = reg.installRequests(env);
        if (requests.length == 0)
            throw new Exception(format("environment %s does not define any installable tool versions", quoteGoString(name)));

        validateInstallEnvironment(name, env, requests, reg);
        verifyPHPBuildTools(env);

        auto results = installRequestsInternal(env, requests, options.force, report);
        installPIEExtensions(env, report);
        installPECLExtensions(env, report);
        syncPECLRuntimeConfig(env);
        syncMSVCToolchain(env, options.force, options.warn);

        return results;
    }

    /// Verifies host-provided native build commands on Linux when `php-extensions` SDK mode is enabled.
    package(polka.backend) void verifyPHPBuildTools(const Environment env)
    {
        if (!env.phpBuildTools() || currentOS() == "windows")
            return;
        if (currentOS() != "linux")
            throw new Exception("PHP extension SDK provisioning is only supported on Windows and Linux");

        string tool, ver;
        primaryPHPTool(env, tool, ver);
        string phpPath;
        if (tool.length > 0 && ver.length > 0)
        {
            try
            {
                phpPath = resolveInstalledTool(tool, ver);
            }
            catch (Exception) {}
        }
        if (phpPath.length == 0)
        {
            try
            {
                phpPath = lookPath("php");
            }
            catch (Exception e)
            {
                throw new Exception("PHP extension SDK requires php on PATH: " ~ e.msg);
            }
        }

        string phpize, phpConfig;
        resolvePHPBuildTools(dirName(phpPath), phpPath, "PHP extension SDK", phpize, phpConfig);
    }

    package(polka.backend) Environment installEnvironment(string name, out ToolRegistry reg)
    {
        init();
        validateName(name);
        bool ok = false;
        auto env = readEnvironment(name, ok);
        if (!ok)
            throw new Exception(format("environment %s does not exist", quoteGoString(name)));

        reg = toolRegistry();
        return normalizeEnvironment(name, env);
    }

    private InstallResult[] installRequestsInternal(
        const Environment env,
        const(InstallRequest)[] requests,
        bool force,
        void delegate(InstallProgress) report
    )
    {
        auto layers = toolRegistry().installRequestLayers(env, requests);
        size_t total = 0;
        foreach (layer; layers)
            total += layer.length;

        InstallResult[] results;
        results.reserve(total);
        size_t offset = 0;
        foreach (layer; layers)
        {
            results ~= installRequestLayer(env, layer, force, report, cast(int) offset, cast(int) total);
            offset += layer.length;
        }
        return results;
    }

    private InstallResult[] installRequestLayer(
        const Environment env,
        const(InstallRequest)[] requests,
        bool force,
        void delegate(InstallProgress) report,
        int offset,
        int total
    )
    {
        auto installEnv = withFrameworkPHPConfig(withInstalledPECLExtensions(cloneEnvironment(env)));
        auto installPHPConfig = effectivePHPConfigForInstall(installEnv);
        auto reg = toolRegistry();
        auto state = readInstallState();

        auto results = new InstallResult[requests.length];
        auto errs = new Throwable[requests.length];
        auto mu = new Mutex();

        void safeReport(InstallProgress progress)
        {
            if (report is null)
                return;
            mu.lock();
            scope (exit) mu.unlock();
            report(progress);
        }

        void runOne(size_t i, InstallRequest req)
        {
            try
            {
                ToolPlugin plugin;
                if (!reg.plugin(req.tool, plugin))
                    throw new Exception(format("unsupported tool %s", quoteGoString(req.tool)));

                InstallProgress baseProgress;
                baseProgress.index = offset + cast(int) i + 1;
                baseProgress.total = total;
                baseProgress.tool = req.tool;
                baseProgress.version_ = req.version_;

                auto cacheVer = installCacheVersion(installEnv, req.tool, req.version_);
                bool skipped = false;
                if (!force && req.tool != toolPHPDevel && state.has(req.tool, req.version_))
                {
                    try
                    {
                        auto targetPath = resolveInstalledTool(req.tool, req.version_);
                        skipped = true;
                        results[i] = InstallResult(req.tool, req.version_, "", targetPath, false, true);
                    }
                    catch (Exception) {}
                }

                if (!skipped)
                {
                    bool downloaded = false;
                    auto cachedPayloadPath = ensureCachedTool(req.tool, cacheVer, (InstallProgressStage stage) {
                        auto p = baseProgress;
                        p.stage = stage;
                        safeReport(p);
                    }, downloaded);

                    baseProgress.stage = InstallProgressInstalling;
                    safeReport(baseProgress);

                    auto targetPath = installToolFromCacheVersion(req.tool, cacheVer, req.version_);
                    results[i] = InstallResult(req.tool, req.version_, cachedPayloadPath, targetPath, downloaded, false);
                }

                if (req.tool == toolPHP || req.tool == toolPHPZTS || req.tool == toolFrankenPHP)
                {
                    if (!installPHPConfig.isZero())
                    {
                        baseProgress.stage = InstallProgressConfiguring;
                        safeReport(baseProgress);
                    }
                }

                plugin.postInstall(ToolInstallContext(
                    projectDir,
                    rootDir,
                    envsDir,
                    binDir,
                    cacheDir,
                    cloneEnvironment(installEnv),
                    results[i]
                ));

                baseProgress.stage = skipped ? InstallProgressSkipped : InstallProgressInstalled;
                safeReport(baseProgress);
            }
            catch (Throwable e)
            {
                errs[i] = e;
            }
        }

        if (requests.length <= 1)
        {
            foreach (i, req; requests)
                runOne(i, req);
        }
        else
        {
            // Allocate a distinct closure frame per iteration because D loop locals share one stack slot.
            Thread makeWorker(size_t idx, InstallRequest item)
            {
                return new Thread(() { runOne(idx, item); });
            }
            auto threads = new Thread[requests.length];
            foreach (i, req; requests)
            {
                threads[i] = makeWorker(i, req);
                threads[i].start();
            }
            foreach (t; threads)
                t.join();
        }

        // Record all fully installed tools before surfacing errors so partial installs resume cleanly.
        bool stateChanged = false;
        foreach (i, err; errs)
        {
            if (err !is null || results[i].skipped || results[i].tool.length == 0)
                continue;
            if (!state.has(results[i].tool, results[i].version_))
            {
                state.record(results[i].tool, results[i].version_);
                stateChanged = true;
            }
        }

        Throwable stateErr = null;
        if (stateChanged)
        {
            try
            {
                writeInstallState(state);
            }
            catch (Throwable e)
            {
                stateErr = new Exception("write install state: " ~ e.msg);
            }
        }

        foreach (err; errs)
        {
            if (err !is null)
            {
                if (stateErr !is null)
                    throw new Exception(err.msg ~ "\n" ~ stateErr.msg);
                throw err;
            }
        }
        if (stateErr !is null)
            throw stateErr;

        return results;
    }

    package(polka.backend) Environment withFrameworkPHPConfig(Environment env)
    {
        env = withPluginPHPExtensions(env);
        return withFrameworkOPcacheConfig(env);
    }

    package(polka.backend) Environment withPluginPHPExtensions(Environment env)
    {
        bool[string] merged;
        if (env.framework.strip().length > 0)
        {
            FrameworkPlugin plugin;
            if (frameworkPlugin(env.framework, plugin))
            {
                foreach (name, enabled; plugin.phpExtensions())
                    merged[name] = enabled;
            }
        }
        foreach (name, enabled; toolRegistry().phpExtensions(env))
            merged[name] = enabled;
        foreach (name, enabled; env.phpExtensions)
            merged[name] = enabled;

        if (merged.length == 0)
            return env;

        auto updated = cloneEnvironment(env);
        updated.phpExtensions = normalizePHPExtensions(merged);
        return updated;
    }

    package(polka.backend) Environment withFrameworkOPcacheConfig(Environment env)
    {
        if (env.framework.strip().length == 0)
            return env;
        FrameworkPlugin plugin;
        if (!frameworkPlugin(env.framework, plugin))
            return env;
        auto fwCfg = plugin.opcacheConfig();
        if (fwCfg.length == 0)
            return env;

        string[string] merged;
        foreach (k, v; fwCfg)
            merged[k] = v;
        foreach (k, v; env.opcacheConfig)
            merged[k] = v;

        auto updated = cloneEnvironment(env);
        updated.opcacheConfig = normalizeOPcacheConfig(merged);
        return updated;
    }

    package(polka.backend) string installToolFromCache(string tool, string ver)
    {
        return installToolFromCacheVersion(tool, ver, ver);
    }

    package(polka.backend) string installToolFromCacheVersion(string tool, string cacheVer, string installVer)
    {
        auto projectInstallDir = buildPath(envsDir, tool, installVer);
        auto parentDir = dirName(projectInstallDir);
        try
        {
            if (!exists(parentDir))
                mkdirRecurse(parentDir);
        }
        catch (Exception e)
        {
            throw new Exception("create project install parent directory: " ~ e.msg);
        }

        string stagingDir;
        try
        {
            stagingDir = createTempDir(installVer ~ "-tmp-", parentDir);
        }
        catch (Exception e)
        {
            throw new Exception("create project install staging directory: " ~ e.msg);
        }
        scope (exit) rmdirRecurseQuiet(stagingDir);

        try
        {
            installCachedToolPayload(cacheDir, stagingDir, tool, cacheVer);
        }
        catch (Exception e)
        {
            throw new Exception("install cached tool payload: " ~ e.msg);
        }

        rmdirRecurseQuiet(projectInstallDir);
        try
        {
            import std.file : rename;
            rename(stagingDir, projectInstallDir);
        }
        catch (Exception e)
        {
            throw new Exception("finalize project install directory: " ~ e.msg);
        }

        return resolveInstalledTool(tool, installVer);
    }

    package(polka.backend) string ensureCachedTool(
        string tool,
        string ver,
        void delegate(InstallProgressStage) report,
        out bool downloaded
    )
    {
        try
        {
            auto cached = cachedToolPayload(cacheDir, tool, ver);
            if (report !is null)
                report(InstallProgressUsingCache);
            downloaded = false;
            return cached.payloadPath;
        }
        catch (Exception e)
        {
            if (!isCacheMiss(e))
                throw e;
        }

        auto dl = downloader !is null ? downloader : new HTTPToolDownloader(null, toolRegistry());
        if (report !is null)
            report(InstallProgressDownloading);
        dl.download(cacheDir, tool, ver);

        auto cached = cachedToolPayload(cacheDir, tool, ver);
        downloaded = true;
        return cached.payloadPath;
    }

    /// Switches the active project environment and refreshes managed command shims.
    void use(string name)
    {
        validateName(name);
        bool ok = false;
        readEnvironment(name, ok);
        if (!ok)
            throw new Exception(format("environment %s does not exist", quoteGoString(name)));

        if (name == defaultEnvironmentName)
        {
            try
            {
                clearActiveEnvironmentName();
            }
            catch (Exception e)
            {
                throw new Exception("clear active environment: " ~ e.msg);
            }
        }
        else
        {
            try
            {
                writeActiveEnvironmentName(name);
            }
            catch (Exception e)
            {
                throw new Exception("write active environment: " ~ e.msg);
            }
        }

        auto cfg = loadConfig();
        try
        {
            syncManagedBinaries(cfg);
        }
        catch (Exception e)
        {
            throw new Exception("sync managed binaries: " ~ e.msg);
        }
    }

    /// Returns the currently selected normalized environment (`default` unless switched via `use`).
    Environment* current()
    {
        auto name = activeEnvironmentName();
        if (name.length == 0)
            name = defaultEnvironmentName;

        bool ok = false;
        auto env = readEnvironment(name, ok);
        if (!ok)
            throw new Exception(format("current environment %s is not defined", quoteGoString(name)));

        auto normalized = new Environment();
        *normalized = normalizeEnvironment(name, env);
        return normalized;
    }

    /// Removes a named environment (`polka.<name>.yaml`), resetting active env to default if it was active.
    void remove(string name)
    {
        validateName(name);
        if (name == defaultEnvironmentName)
        {
            throw new Exception(format(
                "environment %s is stored in %s and cannot be removed",
                quoteGoString(defaultEnvironmentName), baseName(configFile)
            ));
        }

        bool ok = false;
        readEnvironment(name, ok);
        if (!ok)
            throw new Exception(format("environment %s does not exist", quoteGoString(name)));

        auto currentName = activeEnvironmentName();
        try
        {
            removeFileIgnoreNotExist(environmentConfigFile(name));
        }
        catch (Exception e)
        {
            throw new Exception("remove config file: " ~ e.msg);
        }

        if (currentName == name)
        {
            try
            {
                clearActiveEnvironmentName();
            }
            catch (Exception e)
            {
                throw new Exception("clear active environment: " ~ e.msg);
            }
        }

        auto cfg = loadConfig();
        try
        {
            syncManagedBinaries(cfg);
        }
        catch (Exception e)
        {
            throw new Exception("sync managed binaries: " ~ e.msg);
        }
    }

    /// Resolves the installed executable path for a managed tool or dispatch alias in the active environment.
    string resolveTool(string tool)
    {
        auto cur = current();
        if (cur is null)
            throw new Exception("no active environment selected");

        auto reg = toolRegistry();
        reg.validateEnvironment(*cur);
        auto request = reg.resolveDispatchRequestForEnvironment(tool, *cur);

        auto ver = request.plugin.version_(*cur).strip();
        if (ver.length == 0)
        {
            throw new Exception(format(
                "environment %s does not define a %s version",
                quoteGoString(cur.name), request.configTool
            ));
        }
        validateVersion(request.configTool, ver);

        if (request.configTool == request.executable)
            return resolveInstalledTool(request.configTool, ver);
        return resolveInstalledDispatchExecutable(request.configTool, request.executable, ver);
    }

    /// Returns declared log file entries for `tool` (filtered by optional `level`) in the active environment.
    ToolLogEntry[] resolveToolLogs(string tool, string level = "")
    {
        auto normalizedLevel = normalizeLogLevel(level);
        if (normalizedLevel.length > 0 && !validLogLevel(normalizedLevel))
            throw new Exception(format("invalid log level %s: use info, error, or debug", quoteGoString(level)));

        auto request = toolRegistry().resolveLogRequest(tool);
        auto cur = current();
        if (cur is null)
            throw new Exception("no active environment selected");

        auto ver = request.plugin.version_(*cur).strip();
        if (ver.length == 0)
        {
            throw new Exception(format(
                "environment %s does not define a %s version",
                quoteGoString(cur.name), request.configTool
            ));
        }
        validateVersion(request.configTool, ver);

        auto entries = request.plugin.logs();
        if (entries.length == 0)
            throw new Exception(format("tool %s does not declare logs", quoteGoString(request.configTool)));

        ToolLogEntry[] resolved;
        foreach (entry; entries)
        {
            auto entryLevel = normalizeLogLevel(entry.level);
            if (normalizedLevel.length > 0 && entryLevel != normalizedLevel)
                continue;
            auto path = resolveToolLogPath(rootDir, request.configTool, cur.name, entry.path);
            resolved ~= ToolLogEntry(path, entryLevel);
        }
        if (resolved.length == 0)
            throw new Exception(format("tool %s does not declare %s logs", quoteGoString(request.configTool), normalizedLevel));

        return resolved;
    }

    package(polka.backend) Config loadConfig()
    {
        Config loaded;
        try
        {
            loaded = readConfig();
        }
        catch (ConfigNotExistException)
        {
            return defaultConfig();
        }

        if (loaded.version_.length == 0)
            loaded.version_ = configVersion;
        if (loaded.root.strip().length == 0)
            loaded.root = defaultConfig().root;
        if (!(defaultEnvironmentName in loaded.environments))
            loaded.environments[defaultEnvironmentName] = Environment(defaultEnvironmentName);
        return loaded;
    }

    package(polka.backend) Config readConfig()
    {
        ProjectFile pf;
        Environment defEnv;
        readProjectFile(pf, defEnv);

        Config loaded;
        loaded.version_ = pf.version_;
        loaded.root = pf.root;
        loaded.environments[defaultEnvironmentName] = defEnv;

        foreach (name; namedEnvironmentNames())
            loaded.environments[name] = readNamedEnvironmentFile(name);
        return loaded;
    }

    package(polka.backend) Environment readEnvironment(string name, out bool ok)
    {
        validateName(name);
        if (name == defaultEnvironmentName)
        {
            try
            {
                ProjectFile pf;
                Environment env;
                readProjectFile(pf, env);
                ok = true;
                return env;
            }
            catch (ConfigNotExistException)
            {
                ok = true;
                return Environment(defaultEnvironmentName);
            }
        }

        auto path = environmentConfigFile(name);
        bool isDirFlag = false;
        if (!statPathExists(path, isDirFlag))
        {
            ok = false;
            return Environment.init;
        }
        ok = true;
        return readNamedEnvironmentFile(name);
    }

    package(polka.backend) void readProjectFile(out ProjectFile pfOut, out Environment envOut)
    {
        string data;
        try
        {
            data = readTextStrict(configFile);
        }
        catch (ConfigNotExistException e)
        {
            throw e;
        }
        catch (Exception e)
        {
            throw new Exception("read config file: " ~ e.msg);
        }

        validateProjectManifestVersion(data, configFile);

        bool hasLegacy = false;
        try
        {
            hasLegacy = yamlHasTopLevelKey(data, "environments");
        }
        catch (Exception e)
        {
            throw new Exception("decode config file: " ~ e.msg);
        }
        if (hasLegacy)
            throw legacyConfigError(configFile);

        try
        {
            validateEnvironmentFileSchema(data, toolRegistry());
        }
        catch (Exception e)
        {
            throw new Exception("decode config file: " ~ e.msg);
        }

        try
        {
            auto parsed = parseYAML(data);
            if (!parsed.isNull())
                pfOut = projectFileFromYAML(parsed);
        }
        catch (Exception e)
        {
            throw new Exception("decode config file: " ~ e.msg);
        }

        envOut = projectFileToEnvironment(defaultEnvironmentName, pfOut);
        try
        {
            validateEnvironmentFramework(envOut);
        }
        catch (Exception e)
        {
            throw new Exception("decode config file: " ~ e.msg);
        }
    }

    package(polka.backend) Environment readNamedEnvironmentFile(string name)
    {
        validateProjectManifestVersionHook();
        auto path = environmentConfigFile(name);

        string data;
        try
        {
            data = readTextStrict(path);
        }
        catch (Exception e)
        {
            throw new Exception(format("read config file %s: %s", path, e.msg));
        }

        if (data.length == 0)
            return environmentFileToEnvironment(name, EnvironmentFile.init);

        bool hasLegacy = false;
        try
        {
            hasLegacy = yamlHasTopLevelKey(data, "environments");
        }
        catch (Exception e)
        {
            throw new Exception(format("decode config file %s: %s", path, e.msg));
        }
        if (hasLegacy)
            throw legacyConfigError(path);

        try
        {
            validateEnvironmentFileSchema(data, toolRegistry());
        }
        catch (Exception e)
        {
            throw new Exception(format("decode config file %s: %s", path, e.msg));
        }

        EnvironmentFile ef;
        try
        {
            auto parsed = parseYAML(data);
            if (!parsed.isNull())
                ef = environmentFileFromYAML(parsed);
        }
        catch (Exception e)
        {
            throw new Exception(format("decode config file %s: %s", path, e.msg));
        }

        auto env = environmentFileToEnvironment(name, ef);
        try
        {
            validateEnvironmentFramework(env);
        }
        catch (Exception e)
        {
            throw new Exception(format("decode config file %s: %s", path, e.msg));
        }
        return env;
    }

    package(polka.backend) void validateEnvironmentFramework(const Environment env)
    {
        pluginRegistry().validateEnvironment(env);
    }

    package(polka.backend) void writeConfig(Config loadedConfig)
    {
        if (loadedConfig.version_.length == 0)
            loadedConfig.version_ = configVersion;
        if (loadedConfig.root.strip().length == 0)
            loadedConfig.root = relativeRootDir();

        auto defEnv = loadedConfig.environments.get(defaultEnvironmentName, Environment(defaultEnvironmentName));
        writeYAML(configFile, projectFileFromEnvironment(loadedConfig.version_, loadedConfig.root, defEnv));

        string[] names;
        foreach (name, _; loadedConfig.environments)
        {
            if (name != defaultEnvironmentName)
                names ~= name;
        }
        names.sort();
        foreach (name; names)
            writeYAML(environmentConfigFile(name), environmentFileFromEnvironment(loadedConfig.environments[name]));
    }

    package(polka.backend) void writeEnvironmentConfig(string name, const Environment env)
    {
        if (name == defaultEnvironmentName)
        {
            ProjectFile pf;
            try
            {
                Environment dummy;
                readProjectFile(pf, dummy);
            }
            catch (ConfigNotExistException)
            {
                pf = ProjectFile(configVersion, relativeRootDir());
            }
            if (pf.version_.length == 0)
                pf.version_ = configVersion;
            if (pf.root.strip().length == 0)
                pf.root = relativeRootDir();

            writeYAML(configFile, projectFileFromEnvironment(pf.version_, pf.root, env));
            return;
        }

        validateProjectManifestVersionHook();
        writeYAML(environmentConfigFile(name), environmentFileFromEnvironment(env));
    }

    private void validateProjectManifestVersionHook()
    {
        string data;
        try
        {
            data = readTextStrict(configFile);
        }
        catch (ConfigNotExistException)
        {
            return;
        }
        catch (Exception e)
        {
            throw new Exception(format("read project manifest %s: %s", configFile, e.msg));
        }
        validateProjectManifestVersion(data, configFile);
    }

    package(polka.backend) string[] namedEnvironmentNames()
    {
        string[] names;
        try
        {
            foreach (entry; dirEntries(projectDir, SpanMode.shallow))
            {
                if (entry.isDir)
                    continue;
                auto fname = baseName(entry.name);
                string envName;
                if (!namedEnvironmentNameFromFile(fname, envName))
                    continue;
                if (envName == defaultEnvironmentName)
                {
                    throw new Exception(format(
                        "environment name %s is reserved for %s",
                        quoteGoString(defaultEnvironmentName), baseName(configFile)
                    ));
                }
                try
                {
                    validateName(envName);
                }
                catch (Exception e)
                {
                    throw new Exception(format("invalid environment config file %s: %s", quoteGoString(fname), e.msg));
                }
                names ~= envName;
            }
        }
        catch (Exception e)
        {
            if (e.msg.startsWith("environment name ") || e.msg.startsWith("invalid environment config file "))
                throw e;
            throw new Exception("read project directory: " ~ e.msg);
        }
        names.sort();
        return names;
    }

    package(polka.backend) string environmentConfigFile(string name) const
    {
        if (name == defaultEnvironmentName)
            return configFile;
        return buildPath(projectDir, namedConfigPrefix ~ name ~ namedConfigSuffix);
    }

    package(polka.backend) string activeEnvironmentPath() const
    {
        return buildPath(rootDir, runDirectoryName, currentEnvironmentName);
    }

    package(polka.backend) string activeEnvironmentName() const
    {
        string data;
        try
        {
            data = readTextStrict(activeEnvironmentPath());
        }
        catch (ConfigNotExistException)
        {
            return "";
        }
        catch (Exception e)
        {
            throw new Exception("read active environment: " ~ e.msg);
        }

        auto name = data.strip();
        if (name.length == 0)
            return "";
        try
        {
            validateName(name);
        }
        catch (Exception e)
        {
            throw new Exception("read active environment: " ~ e.msg);
        }
        return name;
    }

    package(polka.backend) void writeActiveEnvironmentName(string name) const
    {
        auto path = activeEnvironmentPath();
        auto dir = dirName(path);
        try
        {
            if (!exists(dir))
                mkdirRecurse(dir);
        }
        catch (Exception e)
        {
            throw new Exception("create runtime state directory: " ~ e.msg);
        }
        write(path, name.strip() ~ "\n");
        setFileMode(path, octal!644);
    }

    package(polka.backend) void clearActiveEnvironmentName() const
    {
        removeFileIgnoreNotExist(activeEnvironmentPath());
    }

    package(polka.backend) Config defaultConfig() const
    {
        Config cfg;
        cfg.version_ = configVersion;
        cfg.root = relativeRootDir();
        cfg.environments[defaultEnvironmentName] = withInitDefaults(Environment(defaultEnvironmentName));
        return cfg;
    }

    package(polka.backend) Environment withInitDefaults(Environment env) const
    {
        env.https = true;
        if (env.server is null)
            env.server = new ServerConfig();
        if (env.server.hostname.strip().length == 0)
            env.server.hostname = projectLocalHostname(projectDir);
        return env;
    }

    package(polka.backend) string relativeRootDir() const
    {
        import std.path : relativePath;
        try
        {
            return toSlashPath(relativePath(cleanPath(absolutePath(rootDir)), cleanPath(absolutePath(projectDir))));
        }
        catch (Exception)
        {
            return toSlashPath(rootDir);
        }
    }

    package(polka.backend) Environment normalizeEnvironment(string name, const Environment env) const
    {
        return normalizeEnvironment_(name, env);
    }

    /// Resolves an installed executable binary under `.polka/envs`.
    string resolveInstalledTool(string tool, string ver)
    {
        return resolveInstalledToolIn(envsDir, tool, ver);
    }

    /// Resolves an installed dispatch executable binary under `.polka/envs`.
    string resolveInstalledDispatchExecutable(string configTool, string executable, string ver)
    {
        return resolveInstalledDispatchExecutableIn(envsDir, configTool, executable, ver);
    }

    package(polka.backend) string resolveInstalledToolIn(string root, string tool, string ver)
    {
        auto candidates = toolRegistry().installCandidates(root, tool, ver);
        if (candidates.length == 0)
            throw new Exception(format("unsupported tool %s", quoteGoString(tool)));

        foreach (candidate; candidates)
        {
            bool isDirFlag = false;
            try
            {
                if (statPathExists(candidate, isDirFlag))
                {
                    if (!isDirFlag)
                        return candidate;
                }
            }
            catch (Exception e)
            {
                throw new Exception(format("stat %s: %s", candidate, e.msg));
            }
        }
        throw new Exception(format(
            "%s version %s is not installed under %s",
            tool, quoteGoString(ver), buildPath(root, tool, ver)
        ));
    }

    package(polka.backend) string resolveInstalledDispatchExecutableIn(
        string root,
        string configTool,
        string executable,
        string ver
    )
    {
        return resolveInstalledDispatchExecutableWithRegistry(toolRegistry(), root, configTool, executable, ver);
    }

    package(polka.backend) void installBinaries()
    {
        installDispatcherBinaries();
        syncManagedBinaries(loadConfig());
    }

    package(polka.backend) void syncManagedBinaries(const Config cfg)
    {
        foreach (binary; managedBinaries())
        {
            auto binaryPath = buildPath(binDir, binary.name);
            try
            {
                removeFileIgnoreNotExist(binaryPath);
            }
            catch (Exception e)
            {
                throw new Exception(format("remove binary %s: %s", quoteGoString(binary.name), e.msg));
            }
        }

        auto activeEnv = activeEnvironmentFromConfig(cfg);
        foreach (binary; managedBinariesForEnvironment(activeEnv))
        {
            auto binaryPath = buildPath(binDir, binary.name);
            try
            {
                write(binaryPath, binary.contents);
                setFileMode(binaryPath, binary.mode);
            }
            catch (Exception e)
            {
                throw new Exception(format("write binary %s: %s", quoteGoString(binary.name), e.msg));
            }
        }
    }

    package(polka.backend) void installDispatcherBinaries()
    {
        auto selfPath = currentExecutablePath();
        foreach (binary; dispatcherBinaries(selfPath))
        {
            auto targetPath = buildPath(binDir, binary.name);
            try
            {
                write(targetPath, binary.contents);
                setFileMode(targetPath, binary.mode);
            }
            catch (Exception e)
            {
                throw new Exception(format("write dispatcher shim %s: %s", quoteGoString(binary.name), e.msg));
            }
        }
    }

    package(polka.backend) InstalledBinary[] managedBinaries()
    {
        auto commands = toolRegistry().cleanupCommandNames();
        InstalledBinary[] binaries;
        binaries.reserve(commands.length * 2);
        foreach (cmd; commands)
            binaries ~= [shellDispatchBinary(cmd), windowsDispatchBinary(cmd)];
        return binaries;
    }

    package(polka.backend) InstalledBinary[] managedBinariesForEnvironment(const(Environment)* env)
    {
        auto tools = toolRegistry().activeCommandNames(env);
        InstalledBinary[] binaries;
        binaries.reserve(tools.length * 2);
        foreach (t; tools)
            binaries ~= [shellDispatchBinary(t), windowsDispatchBinary(t)];
        return binaries;
    }

    package(polka.backend) Environment* activeEnvironmentFromConfig(const Config cfg)
    {
        auto name = activeEnvironmentName();
        if (name.length == 0)
            name = defaultEnvironmentName;

        auto p = name in cfg.environments;
        if (p is null)
            return null;

        auto res = new Environment();
        *res = normalizeEnvironment(name, *p);
        return res;
    }

    // ------------------------------------------------------------------------
    // Install State (state.go parity)
    // ------------------------------------------------------------------------

    package(polka.backend) InstallState readInstallState() const
    {
        return readInstallStateFrom(envsDir);
    }

    package(polka.backend) void writeInstallState(const InstallState state) const
    {
        writeInstallStateTo(envsDir, state);
    }

    // ------------------------------------------------------------------------
    // Internal Tool Provisioning (internal_tools.go parity)
    // ------------------------------------------------------------------------

    /// Provisions an internal tool into the global tools directory and returns its executable path.
    string ensureInternalTool(
        string tool,
        string ver = "",
        void delegate(InstallProgress) report = null
    )
    {
        auto normalizedTool = tool.strip().toLower();
        auto reg = toolRegistry();
        ToolPlugin plugin;
        if (!reg.plugin(normalizedTool, plugin))
            throw new Exception(format("unsupported tool %s", quoteGoString(tool)));

        auto internal = internalStore();
        auto env = readInternalEnvironment(internal.configFile);

        auto requested = ver.strip();
        if (requested.length == 0)
            requested = reg.internalVersion(normalizedTool, env);
        if (requested.length == 0)
            requested = defaultInternalToolVersion(normalizedTool);
        if (requested.length == 0)
            throw new Exception(format("tool %s does not define an internal version", quoteGoString(tool)));
        validateVersion(normalizedTool, requested);

        if (reg.internalVersion(normalizedTool, env) != requested)
        {
            env = environmentWithInternalToolVersion(cloneEnvironment(env), normalizedTool, requested);
            writeInternalEnvironment(internal.configFile, env);
        }

        string fastPath;
        if (internal.resolvedInternalInstall(normalizedTool, requested, fastPath))
            return fastPath;

        auto lock = acquireDirLock(buildPath(internal.envsDir, normalizedTool));
        scope (exit) lock.release();

        if (internal.resolvedInternalInstall(normalizedTool, requested, fastPath))
            return fastPath;

        auto progress = InstallProgress(1, 1, normalizedTool, requested, InstallProgressStage.installing);
        bool downloaded = false;
        internal.ensureCachedTool(normalizedTool, requested, (InstallProgressStage stage) {
            emitInstallProgress(report, progress, stage);
        }, downloaded);

        emitInstallProgress(report, progress, InstallProgressInstalling);
        auto targetPath = internal.installToolFromCache(normalizedTool, requested);

        if (normalizedTool == toolPHP || normalizedTool == toolPHPZTS)
        {
            emitInstallProgress(report, progress, InstallProgressConfiguring);
            plugin.postInstall(ToolInstallContext(
                projectDir,
                internal.envsDir,
                internal.envsDir,
                null,
                internal.cacheDir,
                internalPHPEnvironment(normalizedTool, requested, buildPath(internal.envsDir, normalizedTool, requested)),
                InstallResult(normalizedTool, requested, "", targetPath, downloaded, false)
            ));
        }

        auto state = internal.readInstallState();
        state.record(normalizedTool, requested);
        internal.writeInstallState(state);
        emitInstallProgress(report, progress, InstallProgressInstalled);

        return targetPath;
    }

    package(polka.backend) bool resolvedInternalInstall(string tool, string ver, out string targetPath)
    {
        if (!readInstallState().has(tool, ver))
            return false;
        try
        {
            targetPath = resolveInstalledTool(tool, ver);
            return true;
        }
        catch (Exception)
        {
            return false;
        }
    }

    // ------------------------------------------------------------------------
    // PIE Provisioning & Runtime Config Sync (pie.go parity)
    // ------------------------------------------------------------------------

    /// Executes `<internal-php> pie.phar args...` streaming output and returning its exit code.
    int runPIE(
        TextWriter stdout,
        TextWriter stderr,
        const(string)[] args,
        void delegate(InstallProgress) report = null
    )
    {
        auto phpPath = ensureInternalTool(toolPHP, "", report);
        auto piePath = ensureInternalTool(toolPIE, "", report);
        auto cmdArgs = prepareInternalPHPCommandArgs(phpPath, [piePath] ~ args);

        string[] baseEnv;
        foreach (k, v; environment.toAA())
            baseEnv ~= k ~ "=" ~ v;
        auto envList = internalPHPProcessEnv(baseEnv, phpPath);

        try
        {
            return runCommandStreaming(cmdArgs, null, envList, stdout, stderr);
        }
        catch (Exception e)
        {
            throw new Exception("run pie: " ~ e.msg);
        }
    }

    package(polka.backend) void installPIEExtensions(
        const Environment env,
        void delegate(InstallProgress) report
    )
    {
        auto pieExts = normalizePIEExtensions(env.pieExtensions);
        if (pieExts.length == 0)
            return;

        string tool, ver;
        primaryPHPTool(env, tool, ver);
        if (tool.length == 0)
        {
            throw new Exception(format(
                "environment %s defines PIE-managed php-extensions but no standalone php or php-zts runtime; PIE cannot target FrankenPHP's embedded PHP",
                quoteGoString(env.name)
            ));
        }

        auto projectPHP = resolveInstalledTool(tool, ver);
        auto modules = installedPHPModules(projectPHP);

        void delegate(InstallProgress) internalReport = null;
        if (report !is null)
        {
            internalReport = (InstallProgress p) {
                p.tool = "internal " ~ p.tool;
                report(p);
            };
        }

        auto names = pieExts.keys;
        names.sort();

        foreach (idx, pkg; names)
        {
            auto constraint = pieExts[pkg];
            auto progress = InstallProgress(cast(int) idx + 1, cast(int) names.length, pkg, constraint);
            auto mod = pieExtensionModuleName(pkg);

            if (modules.get(mod, false))
            {
                emitInstallProgress(report, progress, InstallProgressSkipped);
                continue;
            }
            if (auto p = mod in env.phpExtensions)
            {
                if (!(*p))
                {
                    emitInstallProgress(report, progress, InstallProgressSkipped);
                    continue;
                }
            }

            emitInstallProgress(report, progress, InstallProgressInstalling);
            auto output = new BufferTextWriter();
            auto exitCode = runPIE(output, output, pieExtensionInstallArgs(projectPHP, pkg, constraint), internalReport);
            if (exitCode != 0)
                throw new Exception(format("pie install %s failed with exit code %d:\n%s", pkg, exitCode, output.text().strip()));
            emitInstallProgress(report, progress, InstallProgressInstalled);
        }
    }

    /// Regenerates `php.ini` for `name`'s installed standalone PHP runtime.
    void syncPHPRuntimeConfig(string name)
    {
        ToolRegistry reg;
        auto env = installEnvironment(name, reg);
        string tool, ver;
        primaryPHPTool(env, tool, ver);
        if (tool.length == 0)
            throw new Exception(format("environment %s does not define a standalone php or php-zts runtime", quoteGoString(name)));

        resyncInstalledPHPRuntimeConfig(envsDir, tool, ver, withFrameworkPHPConfig(withInstalledPECLExtensions(cloneEnvironment(env))));
    }

    // ------------------------------------------------------------------------
    // Legacy PECL Installation & Removal (pecl.go Store methods)
    // ------------------------------------------------------------------------

    /// Installs one legacy PECL package against `environmentName`'s standalone PHP runtime.
    PECLExtensionResult installPECLExtension(
        TextWriter stdout,
        TextWriter stderr,
        string environmentName,
        string packageName,
        string requestedVersion,
        const(string[string]) configureOptions = null
    )
    {
        ToolRegistry reg;
        auto env = installEnvironment(environmentName, reg);
        string tool, ver;
        auto projectPHP = resolvePECLTargetPHP(env, tool, ver);
        auto resolution = resolvePECLPackage(projectPHP, packageName, requestedVersion);
        validatePECLConfigureOptions(resolution, configureOptions);
        rejectPECLPIEConflict(env, resolution.module_);

        if (currentOS() == "windows" && configureOptions.length > 0)
            writeOutln(stderr, "warning: PECL configure options are persisted for Linux builds but do not affect the prebuilt Windows DLL");

        installResolvedPECLExtension(stdout, stderr, projectPHP, tool, ver, resolution, configureOptions);
        return PECLExtensionResult(resolution.packageName, resolution.version_, resolution.module_, resolution.zend);
    }

    /// Removes tracked files for a legacy PECL package, leaving modified or unknown files with a warning.
    PECLExtensionResult removePECLExtension(
        TextWriter stderr,
        string environmentName,
        string packageName
    )
    {
        ToolRegistry reg;
        auto env = installEnvironment(environmentName, reg);
        string tool, ver;
        resolvePECLTargetPHP(env, tool, ver);

        auto installRoot = buildPath(envsDir, tool, ver);
        auto state = readPECLInstallState(installRoot);
        packageName = packageName.strip().toLower();
        auto recordPtr = packageName in state.packages;
        if (recordPtr is null)
        {
            writeOutf(stderr, "warning: no tracked PECL files found for %s; removing its config entry only\n", packageName);
            return PECLExtensionResult(packageName, "", packageName, false);
        }

        auto record = *recordPtr;
        foreach (file; record.files)
        {
            if (peclFileReferencedByOtherPackage(state, packageName, file.path))
                continue;
            auto path = peclStateArtifactPath(installRoot, file.path);
            string actual;
            try
            {
                actual = fileSHA256(path);
            }
            catch (ConfigNotExistException)
            {
                continue;
            }
            if (icmp(actual, file.sha256) != 0)
            {
                writeOutf(stderr, "warning: leaving modified PECL artifact %s\n", path);
                continue;
            }
            try
            {
                remove(path);
            }
            catch (Exception e)
            {
                throw new Exception(format("remove PECL artifact %s: %s; stop the environment and retry", path, e.msg));
            }
        }

        state.packages.remove(packageName);
        writePECLInstallState(installRoot, state);
        return PECLExtensionResult(packageName, record.version_, record.module_, record.zend);
    }

    package(polka.backend) string resolvePECLTargetPHP(const Environment env, out string tool, out string ver)
    {
        primaryPHPTool(env, tool, ver);
        if (tool.length == 0)
        {
            throw new Exception(format(
                "environment %s does not define a standalone php or php-zts runtime; legacy PECL cannot target FrankenPHP's embedded PHP",
                quoteGoString(env.name)
            ));
        }
        try
        {
            return resolveInstalledTool(tool, ver);
        }
        catch (Exception)
        {
            throw new Exception(format(
                "%s %s is not installed for environment %s; run `polka install` first",
                tool, ver, quoteGoString(env.name)
            ));
        }
    }

    package(polka.backend) peclResolution resolvePECLPackage(
        string phpPath,
        string packageName,
        string requestedVersion
    )
    {
        packageName = packageName.strip().toLower();
        validatePECLExtensionPackage(packageName);
        requestedVersion = requestedVersion.strip();
        if (requestedVersion.length == 0)
            requestedVersion = "*";
        validatePECLExtensionVersion(packageName, requestedVersion);

        auto phpInfo = inspectPECLPHP(phpPath);
        auto client = peclHTTPClient();

        string[] versions = [requestedVersion];
        if (requestedVersion == "*")
        {
            peclReleaseList releases;
            try
            {
                releases = parsePECLReleaseList(peclDownloadText(client, peclRESTURL("r", packageName, "allreleases.xml")));
            }
            catch (Exception e)
            {
                throw new Exception(format("resolve legacy PECL package %s: %s", packageName, e.msg));
            }
            versions = null;
            foreach (rel; releases.releases)
            {
                if (icmp(rel.state.strip(), "stable") == 0)
                    versions ~= rel.version_.strip();
            }
        }

        Exception lastErr;
        foreach (candidate; versions)
        {
            try
            {
                auto resolution = resolvePECLRelease(client, packageName, candidate, phpInfo);
                validatePECLPackageDependencies(phpPath, resolution);
                return resolution;
            }
            catch (Exception e)
            {
                if (requestedVersion != "*")
                    throw e;
                lastErr = e;
            }
        }
        if (lastErr !is null)
        {
            throw new Exception(format(
                "no usable stable %s PECL release for PHP %s: %s",
                packageName, phpInfo.version_, lastErr.msg
            ));
        }
        throw new Exception(format(
            "no stable %s PECL release is compatible with PHP %s on %s/%s",
            packageName, phpInfo.version_, currentOS(), currentArch()
        ));
    }

    private void installResolvedPECLExtension(
        TextWriter stdout,
        TextWriter stderr,
        string phpPath,
        string tool,
        string phpVersion,
        const peclResolution resolution,
        const(string[string]) options
    )
    {
        auto installRoot = buildPath(envsDir, tool, phpVersion);
        auto state = readPECLInstallState(installRoot);
        auto peclCacheBase = buildPath(cacheDir, "pecl");
        string staging;
        try
        {
            if (!exists(peclCacheBase))
                mkdirRecurse(peclCacheBase);
            staging = createTempDir(resolution.packageName ~ "-", peclCacheBase);
        }
        catch (Exception e)
        {
            throw new Exception("create PECL staging directory: " ~ e.msg);
        }
        scope (exit) rmdirRecurseQuiet(staging);

        string[] artifacts;
        auto goos = currentOS();
        if (goos == "windows")
            artifacts = installPECLWindowsPayload(stdout, installRoot, phpPath, resolution, staging, state);
        else if (goos == "linux")
            artifacts = installPECLLinuxPayload(stdout, stderr, installRoot, phpPath, resolution, options, staging, state);
        else
            throw new Exception(format("legacy PECL installation is not supported on %s/%s", goos, currentArch()));

        import std.path : relativePath;
        peclInstalledFile[] files;
        files.reserve(artifacts.length);
        foreach (artifact; artifacts)
        {
            auto digest = fileSHA256(artifact);
            if (!isPathWithin(installRoot, artifact))
                throw new Exception(format("PECL artifact %s is outside PHP install root", artifact));
            auto rel = relativePath(cleanPath(absolutePath(artifact)), cleanPath(absolutePath(installRoot)));
            files ~= peclInstalledFile(toSlashPath(rel), digest);
        }

        peclInstalledPackage old;
        bool hadOld = (resolution.packageName in state.packages) !is null;
        if (hadOld)
            old = state.packages[resolution.packageName];

        state.packages[resolution.packageName] = peclInstalledPackage(resolution.version_, resolution.module_, resolution.zend, files);
        writePECLInstallState(installRoot, state);

        if (hadOld)
        {
            foreach (oldFile; old.files)
            {
                if (peclRecordContainsPath(files, oldFile.path)
                    || peclFileReferencedByOtherPackage(state, resolution.packageName, oldFile.path))
                {
                    continue;
                }
                try
                {
                    auto path = peclStateArtifactPath(installRoot, oldFile.path);
                    auto actual = fileSHA256(path);
                    if (icmp(actual, oldFile.sha256) == 0)
                        removeFileIgnoreNotExist(path);
                }
                catch (Exception) {}
            }
        }
    }

    private string[] installPECLWindowsPayload(
        TextWriter stdout,
        string installRoot,
        string phpPath,
        const peclResolution resolution,
        string staging,
        const peclInstallState state
    )
    {
        auto archive = cachedPECLPayload(resolution.packageName, resolution.version_, baseName(fromSlashPath(resolution.windowsURL)), resolution.windowsURL);
        auto extracted = buildPath(staging, "payload");
        extractPECLZip(archive, extracted);

        auto extensionName = "php_" ~ resolution.module_.toLower() ~ ".dll";
        string moduleSource;
        try
        {
            moduleSource = findPECLFile(extracted, extensionName);
        }
        catch (Exception e)
        {
            throw new Exception(format("PECL Windows archive does not contain %s: %s", extensionName, e.msg));
        }

        auto extDir = buildPath(installRoot, "ext");
        if (!exists(extDir))
            mkdirRecurse(extDir);

        auto moduleTarget = buildPath(extDir, extensionName);
        string[] artifacts;
        if (installPECLArtifact(moduleSource, moduleTarget, installRoot, state, resolution.packageName, false))
            artifacts ~= moduleTarget;

        auto phpDir = dirName(phpPath);
        string[] entries;
        foreach (entry; dirEntries(extracted, SpanMode.depth))
            entries ~= entry.name;
        entries.sort();

        foreach (path; entries)
        {
            if (isDir(path))
                continue;
            auto name = baseName(path);
            if (!name.toLower().endsWith(".dll") || icmp(name, extensionName) == 0)
                continue;
            auto target = buildPath(phpDir, name);
            if (installPECLArtifact(path, target, installRoot, state, resolution.packageName, true))
                artifacts ~= target;
        }

        writeOutf(stdout, "Installed legacy PECL binary %s %s for PHP\n", resolution.packageName, resolution.version_);
        return artifacts;
    }

    private string[] installPECLLinuxPayload(
        TextWriter stdout,
        TextWriter stderr,
        string installRoot,
        string phpPath,
        const peclResolution resolution,
        const(string[string]) overrides,
        string staging,
        const peclInstallState state
    )
    {
        auto fileName = resolution.packageName ~ "-" ~ resolution.version_ ~ ".tgz";
        auto archive = cachedPECLPayload(resolution.packageName, resolution.version_, fileName, resolution.downloadURL);
        auto sourceRoot = buildPath(staging, "source");
        extractPECLTarGz(archive, sourceRoot);

        auto buildRoot = findPECLBuildRoot(sourceRoot);
        verifyPECLPackageFiles(buildRoot, resolution.document.contents);

        string phpize, phpConfig;
        resolvePECLBuildTools(installRoot, phpPath, phpize, phpConfig);
        runPECLBuildCommand(stdout, stderr, buildRoot, phpize);

        auto configure = buildPath(buildRoot, "configure");
        string[] args = ["--with-php-config=" ~ phpConfig];
        auto optionNames = resolution.configure.keys;
        optionNames.sort();
        foreach (optName; optionNames)
        {
            auto val = resolution.configure[optName].defaultValue.strip();
            if (auto over = optName in overrides)
                val = (*over).strip();
            args ~= "--" ~ optName ~ "=" ~ val;
        }

        runPECLBuildCommand(stdout, stderr, buildRoot, configure, args);
        runPECLBuildCommand(stdout, stderr, buildRoot, "make");

        string moduleSource;
        try
        {
            moduleSource = findPECLFile(buildPath(buildRoot, "modules"), resolution.module_ ~ ".so");
        }
        catch (Exception e)
        {
            throw new Exception(format("locate built PECL module %s.so: %s", resolution.module_, e.msg));
        }

        auto extDir = buildPath(installRoot, "ext");
        if (!exists(extDir))
            mkdirRecurse(extDir);

        auto target = buildPath(extDir, resolution.module_ ~ ".so");
        bool tracked = installPECLArtifact(moduleSource, target, installRoot, state, resolution.packageName, false);
        if (!tracked)
            throw new Exception(format("built PECL module %s was not installed", resolution.module_));
        return [target];
    }

    package(polka.backend) string cachedPECLPayload(
        string packageName,
        string ver,
        string fileName,
        string url
    )
    {
        if (url.strip().length == 0)
            throw new Exception(format("PECL %s %s does not publish a download URL for %s", packageName, ver, currentOS()));

        auto directory = buildPath(cacheDir, "pecl", packageName, ver);
        auto path = buildPath(directory, baseName(fromSlashPath(fileName)));
        auto checksumPath = path ~ ".sha256";

        try
        {
            auto expected = readTextStrict(checksumPath).strip();
            auto actual = fileSHA256(path);
            if (expected.length > 0 && icmp(expected, actual) == 0)
                return path;
        }
        catch (Exception) {}

        if (!exists(directory))
            mkdirRecurse(directory);

        HTTPResponse resp;
        try
        {
            resp = peclHTTPClient().get(url);
        }
        catch (Exception e)
        {
            throw new Exception(format("download PECL payload %s: %s", url, e.msg));
        }
        if (resp.statusCode != 200)
            throw new Exception(format("download PECL payload %s: unexpected status %d", url, resp.statusCode));

        writeFileAtomic(path, resp.body, octal!644);
        auto digest = fileSHA256(path);
        write(checksumPath, digest ~ "\n");
        return path;
    }

    package(polka.backend) HTTPClient peclHTTPClient()
    {
        if (auto dl = cast(HTTPToolDownloader) downloader)
        {
            if (dl.client !is null)
                return dl.client;
        }
        auto client = new CurlHTTPClient();
        client.timeoutSeconds = 600;
        return client;
    }

    package(polka.backend) void installPECLExtensions(
        const Environment env,
        void delegate(InstallProgress) report
    )
    {
        if (env.peclExtensions.length == 0)
            return;

        string tool, phpVersion;
        auto projectPHP = resolvePECLTargetPHP(env, tool, phpVersion);
        auto installRoot = buildPath(envsDir, tool, phpVersion);
        auto state = readPECLInstallState(installRoot);
        auto modules = installedPHPModules(projectPHP);

        auto names = env.peclExtensions.keys;
        names.sort();

        foreach (idx, name; names)
        {
            auto ext = env.peclExtensions[name];
            auto progress = InstallProgress(cast(int) idx + 1, cast(int) names.length, "pecl:" ~ name, ext.version_);
            if (auto rec = name in state.packages)
            {
                if ((ext.version_ == "*" || rec.version_ == ext.version_)
                    && modules.get(rec.module_, false)
                    && peclRecordFilesValid(installRoot, *rec))
                {
                    emitInstallProgress(report, progress, InstallProgressSkipped);
                    continue;
                }
            }

            emitInstallProgress(report, progress, InstallProgressInstalling);
            auto resolution = resolvePECLPackage(projectPHP, name, ext.version_);
            validatePECLConfigureOptions(resolution, ext.configureOptions);
            rejectPECLPIEConflict(env, resolution.module_);

            try
            {
                installResolvedPECLExtension(null, null, projectPHP, tool, phpVersion, resolution, ext.configureOptions);
            }
            catch (Exception e)
            {
                throw new Exception(format("install legacy PECL extension %s: %s", name, e.msg));
            }
            emitInstallProgress(report, progress, InstallProgressInstalled);
        }
    }

    package(polka.backend) Environment withInstalledPECLExtensions(Environment env)
    {
        string tool, ver;
        primaryPHPTool(env, tool, ver);
        if (tool.length == 0 || env.peclExtensions.length == 0)
            return env;

        peclInstallState state;
        try
        {
            state = readPECLInstallState(buildPath(envsDir, tool, ver));
        }
        catch (Exception)
        {
            return env;
        }

        auto updated = cloneEnvironment(env);
        foreach (name, _; updated.peclExtensions)
        {
            auto rec = name in state.packages;
            if (rec is null || rec.module_.strip().length == 0)
                continue;
            if ((rec.module_ in updated.phpExtensions) is null)
                updated.phpExtensions[rec.module_] = true;
            if (rec.zend)
                updated.zendExtensions[rec.module_] = true;
        }
        return updated;
    }

    package(polka.backend) void syncPECLRuntimeConfig(const Environment env)
    {
        string tool, ver;
        primaryPHPTool(env, tool, ver);
        if (tool.length == 0)
            return;

        resyncInstalledPHPRuntimeConfig(envsDir, tool, ver, withFrameworkPHPConfig(withInstalledPECLExtensions(cloneEnvironment(env))));
    }

    // ------------------------------------------------------------------------
    // MSVC Toolchain Capture & Discovery (msvc.go Store methods)
    // ------------------------------------------------------------------------

    /// Returns the captured MSVC developer environment for `env`, or `null` when absent/disabled.
    MSVCToolchain* msvcToolchain(const Environment env)
    {
        if (!env.phpBuildTools() || currentOS() != "windows")
            return null;
        return readMSVCToolchain(msvcStateFile(env.name));
    }

    package(polka.backend) string msvcStateFile(string environmentName) const
    {
        return msvcStateFilePath(envsDir, environmentName);
    }

    package(polka.backend) void syncMSVCToolchain(
        const Environment env,
        bool force,
        void delegate(string) warn
    )
    {
        if (!env.phpBuildTools() || currentOS() != "windows")
            return;
        auto statePath = msvcStateFile(env.name);

        MSVCToolchain toolchain;
        try
        {
            toolchain = detectMSVCToolchain(env, statePath, force);
        }
        catch (Exception e)
        {
            try
            {
                removeFileIgnoreNotExist(statePath);
            }
            catch (Exception remErr)
            {
                throw new Exception(format("remove stale MSVC environment %s: %s", statePath, remErr.msg));
            }
            if (warn !is null)
            {
                warn(format(
                    "%s; cl.exe and other MSVC tools will not be available in polka sh/exec. Install Visual Studio or Build Tools with the \"Desktop development with C++\" workload, then re-run polka install",
                    e.msg
                ));
            }
            return;
        }

        writeMSVCToolchain(statePath, toolchain);
    }

    package(polka.backend) MSVCToolchain detectMSVCToolchain(
        const Environment env,
        string statePath,
        bool force
    )
    {
        auto vswhere = findVSWhere();
        auto instances = msvcListInstancesHook(vswhere);

        int preferredMajor = 0;
        string targetToolset;
        string tool, ver;
        primaryPHPTool(env, tool, ver);
        if (tool.length > 0 && ver.length > 0)
        {
            try
            {
                auto phpPath = resolveInstalledTool(tool, ver);
                preferredMajor = msvcDetectPHPMajorHook(phpPath);
                targetToolset = msvcDetectPHPToolsetHook(phpPath);
            }
            catch (Exception) {}
        }

        MSVCInstance instance;
        if (!selectMSVCInstance(instances, preferredMajor, instance))
            throw new Exception("no Visual Studio installation with the MSVC x64 C++ build tools was found");

        auto vcvarsall = buildPath(instance.installationPath, "VC", "Auxiliary", "Build", "vcvarsall.bat");
        if (!regularFileExistsAt(vcvarsall))
            throw new Exception(format("Visual Studio at %s has no %s", instance.installationPath, vcvarsall));

        auto selectedToolset = selectMSVCToolset(instance.installationPath, targetToolset);
        auto vcvarsArgs = selectedToolset.length > 0 ? ("-vcvars_ver=" ~ selectedToolset) : "";

        if (!force)
        {
            try
            {
                auto existing = readMSVCToolchain(statePath);
                if (existing !is null
                    && icmp(existing.installationPath, instance.installationPath) == 0
                    && existing.installationVersion == instance.installationVersion
                    && existing.arch == msvcTargetArch
                    && (selectedToolset.length == 0 || existing.set.get("VCToolsVersion", "").startsWith(selectedToolset)))
                {
                    return *existing;
                }
            }
            catch (Exception) {}
        }

        auto cap = msvcCaptureVCVarsHook(vcvarsall, msvcTargetArch, vcvarsArgs);
        string[string] setVars;
        string[][string] prependVars;
        diffMSVCEnvironment(cap.baseline, cap.captured, setVars, prependVars);
        if (("VCToolsInstallDir" in setVars) is null)
            throw new Exception(format("%s %s did not configure the MSVC toolchain", vcvarsall, msvcTargetArch));

        auto displayVersion = instance.productDisplayVersion.strip();
        if (displayVersion.length == 0)
            displayVersion = instance.installationVersion;

        return MSVCToolchain(
            instance.displayName,
            displayVersion,
            instance.installationPath,
            instance.installationVersion,
            vcvarsall,
            msvcTargetArch,
            setVars,
            prependVars
        );
    }

    package(polka.backend) string findVSWhere() const
    {
        return polka.backend.msvc.findVSWhere(envsDir);
    }
}

// ============================================================================
// Store Discovery, Construction & Validation Helpers
// ============================================================================

/// Discovers the project directory from CWD and opens its configured `Store`.
Store defaultStore()
{
    auto workingDir = getcwd();
    auto projectDir = discoverProjectDir(workingDir);
    auto s = newProjectStore(projectDir);
    auto cfg = s.loadConfig();
    return newStore(projectDir, resolveConfiguredRootDir(projectDir, cfg.root));
}

/// Returns a store anchored at CWD (or `root` if non-empty) without walking parent directories.
Store storeForWorkingDirectory(string root = "")
{
    auto workingDir = getcwd();
    auto projectDir = cleanPath(absolutePath(workingDir));
    if (root.strip().length == 0)
        return newProjectStore(projectDir);
    auto cleanRoot = cleanPath(absolutePath(root));
    return newStore(projectDir, cleanRoot);
}

/// Discovers the owning project directory for `root` by walking up from `dirName(root)`.
Store storeForRoot(string root)
{
    auto cleanRoot = cleanPath(absolutePath(root));
    auto projectDir = discoverProjectDirForRoot(cleanRoot);
    return newStore(projectDir, cleanRoot);
}

/// Walks upward from `workingDir` looking for a regular `polka.yaml`; defaults back to `workingDir`.
string discoverProjectDir(string workingDir)
{
    auto currentDir = cleanPath(absolutePath(workingDir));
    auto originalDir = currentDir;

    while (true)
    {
        auto configPath = buildPath(currentDir, configFileName);
        bool isDirFlag = false;
        try
        {
            if (statPathExists(configPath, isDirFlag))
            {
                if (!isDirFlag)
                    return currentDir;
                // Ignore directories named polka.yaml and keep walking upward.
            }
        }
        catch (Exception e)
        {
            throw new Exception(format("stat %s: %s", configPath, e.msg));
        }

        auto parentDir = dirName(currentDir);
        if (parentDir == currentDir)
            return originalDir;
        currentDir = parentDir;
    }
}

/// Walks upward from `dirName(root)` to find the `polka.yaml` whose resolved root matches `root`.
string discoverProjectDirForRoot(string root)
{
    auto cleanRoot = cleanPath(absolutePath(root));
    auto currentDir = dirName(cleanRoot);
    auto originalDir = currentDir;

    while (true)
    {
        auto configPath = buildPath(currentDir, configFileName);
        bool isDirFlag = false;
        try
        {
            if (statPathExists(configPath, isDirFlag))
            {
                if (!isDirFlag)
                {
                    auto cfg = newStore(currentDir, cleanRoot).readConfig();
                    if (pathsEqual(resolveConfiguredRootDir(currentDir, cfg.root), cleanRoot))
                        return currentDir;
                }
            }
        }
        catch (Exception e)
        {
            throw new Exception(format("stat %s: %s", configPath, e.msg));
        }

        auto parentDir = dirName(currentDir);
        if (parentDir == currentDir)
            return originalDir;
        currentDir = parentDir;
    }
}

/// Constructs a `Store` for `projectDir` using `<projectDir>/.polka`.
Store newProjectStore(string projectDir)
{
    return newStore(projectDir, buildPath(projectDir, defaultRootDirectoryName));
}

/// Constructs a `Store` rooted at `root` with `projectDir = dirName(root)`.
Store newStore(string root)
{
    auto cleanRoot = cleanPath(root);
    auto projDir = dirName(cleanRoot);
    return newStore(projDir, cleanRoot);
}

/// Constructs a `Store` binding `projectDir` and `root`.
Store newStore(string projectDir, string root)
{
    auto cleanProjectDir = cleanPath(projectDir);
    auto cleanRoot = cleanPath(root);

    Store s;
    s.projectDir = cleanProjectDir;
    s.rootDir = cleanRoot;
    s.envsDir = buildPath(cleanRoot, envsDirectoryName);
    s.binDir = buildPath(cleanRoot, binDirectoryName);
    s.configFile = buildPath(cleanProjectDir, configFileName);
    s.cacheDir = defaultCacheDir(cleanProjectDir);
    s.plugins = newDefaultToolRegistry();
    s.registry = newDefaultPluginRegistry();
    return s;
}

/// Validates a relative preset scaffold target path against escape, separators, and reserved paths.
package(polka.backend) void validatePresetScaffoldTarget(string target)
{
    auto trimmed = target.strip();
    bool hasWinDrive = trimmed.length >= 2 && trimmed[1] == ':';
    if (trimmed.length == 0 || isAbsolute(trimmed) || hasWinDrive || trimmed.canFind('\\'))
        throw new Exception(format("preset scaffold path %s must be slash-separated and relative", quoteGoString(target)));

    import std.string : split;
    foreach (segment; trimmed.split('/'))
    {
        if (segment.length == 0 || segment == "." || segment == "..")
            throw new Exception(format("preset scaffold path %s contains an unsafe segment", quoteGoString(target)));
    }

    auto lower = trimmed.toLower();
    if (lower == configFileName
        || (lower.startsWith(namedConfigPrefix) && lower.endsWith(namedConfigSuffix))
        || lower == defaultRootDirectoryName
        || lower.startsWith(defaultRootDirectoryName ~ "/"))
    {
        throw new Exception(format("preset scaffold path %s is reserved for init", quoteGoString(target)));
    }
}

package(polka.backend) string resolveInstalledDispatchExecutableIn(
    string root,
    string configTool,
    string executable,
    string ver
)
{
    return resolveInstalledDispatchExecutableWithRegistry(newDefaultToolRegistry(), root, configTool, executable, ver);
}

package(polka.backend) string resolveInstalledDispatchExecutableWithRegistry(
    ToolRegistry registry,
    string root,
    string configTool,
    string executable,
    string ver
)
{
    auto candidates = registry.dispatchCandidates(root, configTool, executable, ver);
    if (candidates.length == 0)
        throw new Exception(format("unsupported tool %s", quoteGoString(executable)));

    foreach (candidate; candidates)
    {
        bool isDirFlag = false;
        try
        {
            if (statPathExists(candidate, isDirFlag))
            {
                if (!isDirFlag)
                    return candidate;
            }
        }
        catch (Exception e)
        {
            throw new Exception(format("stat %s: %s", candidate, e.msg));
        }
    }

    throw new Exception(format(
        "%s version %s does not include a %s command under %s",
        configTool, quoteGoString(ver), executable, buildPath(root, configTool, ver)
    ));
}

package(polka.backend) string[] toolInstallCandidatesIn(string root, string tool, string ver)
{
    return newDefaultToolRegistry().installCandidates(root, tool, ver);
}

package(polka.backend) string[] dispatchExecutableCandidatesIn(
    string root,
    string configTool,
    string executable,
    string ver
)
{
    return newDefaultToolRegistry().dispatchCandidates(root, configTool, executable, ver);
}

package(polka.backend) string[] managedToolsForEnvironment(const(Environment)* env)
{
    return newDefaultToolRegistry().activeCommandNames(env);
}

package(polka.backend) struct ToolRequest
{
    string configTool;
    string executable;
}

package(polka.backend) ToolRequest resolveToolRequest(string tool)
{
    auto req = newDefaultToolRegistry().resolveDispatchRequest(tool);
    return ToolRequest(req.configTool, req.executable);
}
