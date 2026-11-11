module polka.tools.registry;

import std.algorithm : sort;
import std.format : format;
import std.string : icmp, strip, toLower;

import polka.config;
import polka.support.fs : currentArch, currentOS;
import polka.tools.ids;
import polka.tools.manifest_rules;
import polka.tools.post_install;
import polka.tools.types;

/// Normalizes a database engine string, validating against `mysql`, `mariadb`, or `postgresql`.
string normalizeDatabaseEngine(string engine) @safe
{
    auto trimmed = engine.strip().toLower();
    switch (trimmed)
    {
        case "":
            return "";
        case MySQL:
        case MariaDB:
        case PostgreSQL:
            return trimmed;
        default:
            throw new Exception(format(`unsupported database engine "%s"`, engine));
    }
}

/// Validates configured database settings (engine, version, port).
void validateDatabaseConfig(const(DatabaseConfig)* database) @safe
{
    if (database is null)
        return;

    auto engine = normalizeDatabaseEngine(database.engine);
    if (engine.length == 0 || database.version_.strip().length == 0)
        throw new Exception("database configuration requires both engine and version");
    validateVersion(engine, database.version_);
    if (database.port != 0 && !validPort(database.port))
        throw new Exception("database port must be between 1 and 65535");
}

/// Normalizes manifest setting schema maps to lowercase keys.
SettingType[string] normalizeManifestSettings(const(SettingType[string]) settings) @safe pure
{
    if (settings.length == 0)
        return null;
    SettingType[string] normalized;
    foreach (k, v; settings)
    {
        auto nk = k.strip().toLower();
        if (nk.length == 0)
            continue;
        normalized[nk] = v;
    }
    return normalized.length == 0 ? null : normalized;
}

/// Declarative + hook-backed `ToolPlugin` implementation produced by tool manifests.
class BuiltinPlugin : ToolPlugin, ToolDependencyProvider, ToolSettingsProvider, InternalToolInfo
{
    string idValue;
    string delegate(const Environment) versionFn;
    bool internalOnlyValue = false;
    string delegate(const Environment) internalVersionFn;
    ManifestVersionConstraint versionConstraint;
    ManifestWrapper[string] wrappers;
    string[] delegate(string installDir) executablePathsFn;
    SettingType[string] settingsMap;
    string settingsNamespaceValue;
    string[] exclusiveWithList;
    bool[string] phpExtensionsMap;
    void delegate(const Environment) validateHook;
    string[] delegate(string root, string ver) installCandidatesFn;
    string[] dispatchCommandsList;
    string[] cleanupCommandsList;
    string[] delegate(const Environment) activeCommandsFn;
    string[] delegate(string root, string executable, string ver) dispatchCandidatesFn;
    LogEntry[] logEntries;
    void delegate(DownloadContext) downloadHook;
    void delegate(InstallContext) postInstallHook;
    InstallRequest[] delegate(const Environment) dependenciesHook;

    string id() const @safe pure
    {
        return idValue;
    }

    bool internalOnly() const @safe pure
    {
        return internalOnlyValue;
    }

    string internalVersion(const Environment environment) const
    {
        if (internalVersionFn is null)
            return "";
        return internalVersionFn(environment).strip();
    }

    SettingType[string] settings() const @safe pure
    {
        return copySettingTypes(settingsMap);
    }

    string settingsNamespace() const @safe pure
    {
        return settingsNamespaceValue;
    }

    string[] exclusiveWith() const @safe pure
    {
        return copyStrings(exclusiveWithList);
    }

    string version_(const Environment environment) const
    {
        if (versionFn is null)
            return "";
        return versionFn(environment).strip();
    }

    bool[string] phpExtensions() const @safe pure
    {
        if (phpExtensionsMap.length == 0)
            return null;
        bool[string] copyMap;
        foreach (k, v; phpExtensionsMap)
            copyMap[k] = v;
        return copyMap;
    }

    InstallRequest[] dependencies(const Environment environment) const
    {
        if (dependenciesHook is null)
            return null;
        auto deps = dependenciesHook(environment);
        return deps.length == 0 ? null : deps.dup;
    }

    void validate(const Environment environment) const
    {
        validateManifestRules(environment);
        if (validateHook !is null)
            validateHook(environment);
    }

    private void validateManifestRules(const Environment environment) const
    {
        auto ver = version_(environment);
        auto hasSettings = hasConfiguredSettings(environment);
        if (ver.length == 0 && !hasSettings)
            return;
        if (ver.length == 0 && !hasExclusivePeerVersion(environment))
            throw new Exception(idValue ~ " configuration requires version");
        if (ver.length > 0)
        {
            validateVersion(idValue, ver);
            if (!constraintAllowsLabel(versionConstraint, ver))
                throw new Exception(formatConstraintError(versionConstraint, idValue, ver));
        }
        if (settingsMap.length > 0 && settingsNamespaceValue.length > 0)
        {
            auto keys = settingsMap.keys;
            keys.sort();
            foreach (key; keys)
            {
                if (settingsMap[key] != SettingType.port)
                    continue;
                auto port = environment.settingInt(settingsNamespaceValue, key);
                if (port != 0 && !validPort(port))
                    throw new Exception(format("%s %s must be between 1 and 65535", settingsNamespaceValue, key));
            }
        }
    }

    private bool hasConfiguredSettings(const Environment environment) const
    {
        if (settingsMap.length == 0 || settingsNamespaceValue.length == 0)
            return false;
        foreach (key, settingType; settingsMap)
        {
            final switch (settingType)
            {
                case SettingType.port:
                    if (environment.settingInt(settingsNamespaceValue, key) != 0)
                        return true;
                    break;
                case SettingType.string_:
                    if (environment.settingString(settingsNamespaceValue, key).length > 0)
                        return true;
                    break;
                case SettingType.bool_:
                    if (environment.settingBool(settingsNamespaceValue, key))
                        return true;
                    break;
            }
        }
        return false;
    }

    private bool hasExclusivePeerVersion(const Environment environment) const
    {
        foreach (peer; exclusiveWithList)
        {
            if (environment.toolVersion(peer).strip().length > 0)
                return true;
        }
        return false;
    }

    string[] installCandidates(string root, string ver) const
    {
        if (installCandidatesFn is null)
            return null;
        return installCandidatesFn(root, ver);
    }

    string[] dispatchCommands() const @safe pure
    {
        return copyStrings(dispatchCommandsList);
    }

    string[] cleanupCommands() const @safe pure
    {
        if (cleanupCommandsList.length > 0)
            return copyStrings(cleanupCommandsList);
        return dispatchCommands();
    }

    string[] activeCommands(const Environment environment) const
    {
        if (activeCommandsFn !is null)
            return copyStrings(activeCommandsFn(environment));
        if (version_(environment).length == 0)
            return null;
        return dispatchCommands();
    }

    string[] dispatchCandidates(string root, string executable, string ver) const
    {
        if (dispatchCandidatesFn is null)
        {
            if (icmp(executable.strip(), idValue) == 0)
                return installCandidates(root, ver);
            return null;
        }
        return dispatchCandidatesFn(root, executable, ver);
    }

    LogEntry[] logs() const @safe pure
    {
        return copyLogEntries(logEntries);
    }

    void download(DownloadContext ctx) const
    {
        if (downloadHook is null)
            throw new Exception(format(`unsupported tool "%s"`, idValue));
        downloadHook(ctx);
    }

    void postInstall(InstallContext ctx) const
    {
        auto installDir = resolvePostInstallDir(idValue, ctx);
        auto writtenWrappers = writeManifestWrappers(installDir, wrappers, currentOS(), currentArch());

        string[] execPaths = [ctx.result.targetPath];
        if (executablePathsFn !is null && installDir.length > 0)
            execPaths ~= executablePathsFn(installDir);
        execPaths ~= writtenWrappers;
        ensureInstalledExecutables(execPaths);

        if (postInstallHook !is null)
            postInstallHook(ctx);
    }
}

/// Ordered collection of registered `ToolPlugin` instances.
class Registry
{
    private ToolPlugin[] pluginsList;
    private ToolPlugin[string] byID;

    /// Creates a registry containing `plugins` in registration order.
    this(ToolPlugin[] plugins...)
    {
        foreach (p; plugins)
            register(p);
    }

    /// Registers one `ToolPlugin`, rejecting nulls, invalid IDs, and duplicates.
    void register(ToolPlugin plugin)
    {
        if (plugin is null)
            throw new Exception("tool plugin cannot be nil");

        auto rawId = plugin.id();
        auto id = rawId.strip().toLower();
        if (id.length == 0)
            throw new Exception("tool plugin id cannot be empty");
        if (!isValidName(id))
            throw new Exception(format(`invalid tool plugin id "%s": use letters, numbers, dots, dashes, or underscores`, rawId));
        if (id in byID)
            throw new Exception(format(`duplicate tool plugin "%s"`, id));

        pluginsList ~= plugin;
        byID[id] = plugin;
    }

    /// Returns registered plugins in declaration order.
    ToolPlugin[] plugins() pure
    {
        if (pluginsList.length == 0)
            return null;
        return pluginsList.dup;
    }

    /// Looks up a registered plugin by tool ID (case-insensitive).
    bool plugin(string id, out ToolPlugin outPlugin) pure
    {
        auto p = id.strip().toLower() in byID;
        if (p is null)
        {
            outPlugin = null;
            return false;
        }
        outPlugin = *p;
        return true;
    }

    /// Returns the settings schema declared for `namespace` and ordered owning tool IDs.
    bool settingsSchema(string namespace, out SettingType[string] schema, out string[] owners)
    {
        auto target = namespace.strip().toLower();
        if (target.length == 0)
            return false;

        bool[string] seenOwners;
        foreach (p; pluginsList)
        {
            if (internalOnly(p.id()))
                continue;
            auto provider = cast(ToolSettingsProvider) p;
            if (provider is null)
                continue;

            auto pluginID = p.id().strip().toLower();
            auto pluginSettings = normalizeManifestSettings(provider.settings());
            auto pluginNamespace = provider.settingsNamespace().strip().toLower();
            if (pluginNamespace.length == 0 && pluginSettings.length > 0)
                pluginNamespace = pluginID;
            if (pluginNamespace != target)
                continue;

            if (pluginSettings.length > 0 && schema.length == 0)
                schema = pluginSettings;
            if (!(pluginID in seenOwners))
            {
                seenOwners[pluginID] = true;
                owners ~= pluginID;
            }
        }

        if (schema.length == 0)
        {
            owners = null;
            return false;
        }
        schema = copySettingTypes(schema);
        return true;
    }

    /// Returns mutually exclusive peer tool IDs for `id` (both declared and reverse edges).
    string[] exclusivePeers(string id)
    {
        auto target = id.strip().toLower();
        if (target.length == 0)
            return null;

        string[] peers;
        bool[string] seen;
        void addPeer(string peer)
        {
            auto norm = peer.strip().toLower();
            if (norm.length == 0 || norm == target || (norm in seen))
                return;
            seen[norm] = true;
            peers ~= norm;
        }

        ToolPlugin targetPlugin;
        if (plugin(target, targetPlugin))
        {
            if (auto provider = cast(ToolSettingsProvider) targetPlugin)
            {
                foreach (peer; provider.exclusiveWith())
                    addPeer(peer);
            }
        }

        foreach (p; pluginsList)
        {
            auto provider = cast(ToolSettingsProvider) p;
            if (provider is null)
                continue;
            auto pluginID = p.id().strip().toLower();
            foreach (peer; provider.exclusiveWith())
            {
                if (icmp(peer.strip(), target) == 0)
                    addPeer(pluginID);
            }
        }

        return peers.length == 0 ? null : peers;
    }

    /// Validates all plugin and database rules for `environment`.
    void validateEnvironment(const Environment environment)
    {
        foreach (p; pluginsList)
        {
            if (p.version_(environment).strip().length > 0)
            {
                foreach (peerID; exclusivePeers(p.id()))
                {
                    ToolPlugin peer;
                    if (plugin(peerID, peer) && peer.version_(environment).strip().length > 0)
                        throw new Exception(format("%s and %s versions are mutually exclusive", p.id(), peer.id()));
                }
            }
            p.validate(environment);
        }

        if (environment.database !is null)
        {
            auto engine = environment.database.engine.strip().toLower();
            if (engine.length > 0)
            {
                ToolPlugin dummy;
                if (!plugin(engine, dummy))
                    throw new Exception(format(`unsupported database engine "%s"`, environment.database.engine));
            }
            validateDatabaseConfig(environment.database);
        }
    }

    /// Returns ordered install requests for tools configured in `environment`.
    InstallRequest[] installRequests(const Environment environment)
    {
        InstallRequest[] requests;
        foreach (p; pluginsList)
        {
            auto ver = p.version_(environment).strip();
            if (ver.length > 0)
                requests ~= InstallRequest(p.id(), ver);
        }
        return requests;
    }

    /// Computes topological dependency installation layers from `environment` configured tools.
    InstallRequest[][] installRequestLayers(const Environment environment)
    {
        return installRequestLayers(environment, installRequests(environment));
    }

    /// Computes topological dependency installation layers rooted at explicit `roots`.
    InstallRequest[][] installRequestLayers(const Environment environment, const(InstallRequest)[] roots)
    {
        InstallRequest[string] requests;
        size_t[string] order;
        size_t nextOrder = 0;
        string[][string] depsMap;
        bool[string] visiting;
        bool[string] visited;

        void visit(const InstallRequest req)
        {
            auto id = req.tool.strip().toLower();
            auto ver = req.version_.strip();
            if (id.length == 0 || ver.length == 0)
                throw new Exception("install dependency requires tool and version");
            if (id in visiting && visiting[id])
                throw new Exception(format(`managed tool dependency cycle includes "%s"`, id));
            if (auto prev = id in requests)
            {
                if (prev.version_ != req.version_)
                    throw new Exception(format(`managed tool "%s" requires conflicting versions "%s" and "%s"`, id, prev.version_, req.version_));
            }
            requests[id] = InstallRequest(id, ver);
            if (!(id in order))
                order[id] = nextOrder++;
            if (id in visited)
                return;

            ToolPlugin p;
            if (!plugin(id, p))
                throw new Exception(format(`unsupported tool dependency "%s"`, id));

            visiting[id] = true;
            if (auto provider = cast(ToolDependencyProvider) p)
            {
                foreach (dep; provider.dependencies(environment))
                {
                    auto depID = dep.tool.strip().toLower();
                    depsMap[id] ~= depID;
                    visit(dep);
                }
            }
            visiting[id] = false;
            visited[id] = true;
        }

        foreach (root; roots)
            visit(root);

        bool[string] remaining;
        foreach (id, _; requests)
            remaining[id] = true;

        InstallRequest[][] layers;
        while (remaining.length > 0)
        {
            InstallRequest[] layer;
            foreach (id, _; remaining)
            {
                bool ready = true;
                if (auto depPtr = id in depsMap)
                {
                    foreach (dep; *depPtr)
                    {
                        if (dep in remaining)
                        {
                            ready = false;
                            break;
                        }
                    }
                }
                if (ready)
                    layer ~= requests[id];
            }
            if (layer.length == 0)
                throw new Exception("managed tool dependency cycle detected");

            layer.sort!((a, b) => order[a.tool] < order[b.tool]);
            foreach (req; layer)
                remaining.remove(req.tool);
            layers ~= layer;
        }
        return layers;
    }

    /// Returns the union of PHP extensions required by configured non-internal tools.
    bool[string] phpExtensions(const Environment environment)
    {
        bool[string] extensions;
        foreach (p; pluginsList)
        {
            if (p.version_(environment).strip().length == 0)
                continue;
            foreach (name, enabled; p.phpExtensions())
            {
                if (enabled)
                    extensions[name] = true;
            }
        }
        return extensions.length == 0 ? null : extensions;
    }

    /// Reports whether `id` is reserved for internal polka-managed environments only.
    bool internalOnly(string id)
    {
        ToolPlugin p;
        if (!plugin(id, p))
            return false;
        auto info = cast(InternalToolInfo) p;
        return info !is null && info.internalOnly();
    }

    /// Resolves `id`'s raw version from an internal `_internal` environment.
    string internalVersion(string id, const Environment environment)
    {
        ToolPlugin p;
        if (!plugin(id, p))
            return "";
        if (auto info = cast(InternalToolInfo) p)
            return info.internalVersion(environment);
        return p.version_(environment).strip();
    }

    /// Resolves a CLI dispatch command to its owning plugin.
    DispatchRequest resolveDispatchRequest(string tool)
    {
        auto trimmed = tool.strip().toLower();
        foreach (p; pluginsList)
        {
            foreach (cmd; p.dispatchCommands())
            {
                if (icmp(cmd.strip(), trimmed) == 0)
                    return DispatchRequest(p.id(), cmd.strip().toLower(), p);
            }
        }
        throw new Exception(format(`unsupported tool "%s"`, tool));
    }

    /// Resolves a dispatch command, preferring a provider configured in `environment`.
    DispatchRequest resolveDispatchRequestForEnvironment(string tool, const Environment environment)
    {
        auto trimmed = tool.strip().toLower();
        bool hasFallback = false;
        DispatchRequest fallback;

        foreach (p; pluginsList)
        {
            foreach (cmd; p.dispatchCommands())
            {
                if (icmp(cmd.strip(), trimmed) == 0)
                {
                    auto req = DispatchRequest(p.id(), cmd.strip().toLower(), p);
                    if (p.version_(environment).strip().length > 0)
                        return req;
                    if (!hasFallback)
                    {
                        fallback = req;
                        hasFallback = true;
                    }
                }
            }
        }
        if (hasFallback)
            return fallback;
        throw new Exception(format(`unsupported tool "%s"`, tool));
    }

    /// Resolves either a manifest tool ID or a dispatch command to its owning plugin for logs.
    LogRequest resolveLogRequest(string tool)
    {
        auto trimmed = tool.strip().toLower();
        ToolPlugin p;
        if (plugin(trimmed, p))
            return LogRequest(p.id(), p);
        auto req = resolveDispatchRequest(trimmed);
        return LogRequest(req.configTool, req.plugin);
    }

    string[] installCandidates(string root, string tool, string ver)
    {
        ToolPlugin p;
        if (!plugin(tool, p))
            return null;
        return p.installCandidates(root, ver);
    }

    string[] dispatchCandidates(string root, string configTool, string executable, string ver)
    {
        ToolPlugin p;
        if (!plugin(configTool, p))
            return null;
        return p.dispatchCandidates(root, executable, ver);
    }

    string[] cleanupCommandNames()
    {
        return uniqueToolCommands(pluginsList, p => p.cleanupCommands());
    }

    string[] activeCommandNames(const(Environment)* environment)
    {
        if (environment is null)
            return null;
        return uniqueToolCommands(pluginsList, p => p.activeCommands(*environment));
    }
}

private string[] uniqueToolCommands(
    ToolPlugin[] plugins,
    scope string[] delegate(ToolPlugin) commandList
)
{
    bool[string] seen;
    string[] commands;
    foreach (p; plugins)
    {
        foreach (cmd; commandList(p))
        {
            auto norm = cmd.strip().toLower();
            if (norm.length == 0 || (norm in seen))
                continue;
            seen[norm] = true;
            commands ~= norm;
        }
    }
    return commands;
}

/// Convenience factory matching `new Registry(...)`.
Registry newRegistry(ToolPlugin[] plugins...)
{
    return new Registry(plugins);
}

unittest
{
    import std.algorithm : canFind;
    import polka.support.fs : currentOS;

    // TestBuiltinPluginValidateChainsManifestRulesAndCustomHook
    bool hookCalled = false;
    auto plugin = new BuiltinPlugin();
    plugin.idValue = "demo";
    plugin.versionFn = (const Environment env) => env.toolVersion("demo");
    plugin.settingsMap = ["port": SettingType.port];
    plugin.versionConstraint = ManifestVersionConstraint("2.0.0", "");
    plugin.validateHook = (const Environment env) {
        hookCalled = true;
        throw new Exception("custom validate error");
    };

    Environment failEnv;
    failEnv.tools = ["demo": "1.5"];
    bool sawConstraint = false;
    try
    {
        plugin.validate(failEnv);
    }
    catch (Exception e)
    {
        sawConstraint = e.msg.canFind("requires >= 2.0.0");
    }
    assert(sawConstraint && !hookCalled);

    Environment passEnv;
    passEnv.tools = ["demo": "2.1"];
    bool sawCustom = false;
    try
    {
        plugin.validate(passEnv);
    }
    catch (Exception e)
    {
        sawCustom = e.msg.canFind("custom validate error");
    }
    assert(sawCustom && hookCalled);
}
