module polka.tools.manifest;

import std.algorithm : any, canFind, sort, startsWith;
import std.file : exists, isDir;
import std.format : format;
import std.path : buildPath, isAbsolute;
import std.regex : regex;
import std.string : icmp, strip, toLower;

import polka.config;
import polka.support.fs : cleanPath, currentArch, currentOS, fromSlashPath, toSlashPath;
import polka.support.http : HTTPClient;
import polka.support.yaml : parseYAML, YamlKind, YamlValue;
import polka.tools.download_io : downloadManifestAsset, resolveManifestDownloadAsset;
import polka.tools.ids;
import polka.tools.manifest_request;
import polka.tools.manifest_rules;
import polka.tools.registry;
import polka.tools.types;

/// Returns raw YAML contents for a built-in manifest (`tools/manifests/<name>.yaml`) embedded at compile time.
string builtinManifestYAML(string name) @safe pure
{
    switch (name.strip())
    {
        case "apache":      return import("apache.yaml");
        case "composer":    return import("composer.yaml");
        case "erlang":      return import("erlang.yaml");
        case "frankenphp":  return import("frankenphp.yaml");
        case "mago":        return import("mago.yaml");
        case "mailpit":     return import("mailpit.yaml");
        case "mariadb":     return import("mariadb.yaml");
        case "meilisearch": return import("meilisearch.yaml");
        case "mysql":       return import("mysql.yaml");
        case "nginx":       return import("nginx.yaml");
        case "nodejs":      return import("nodejs.yaml");
        case "php-devel":   return import("php-devel.yaml");
        case "phpmyadmin":  return import("phpmyadmin.yaml");
        case "php-sdk":     return import("php-sdk.yaml");
        case "php":         return import("php.yaml");
        case "php-zts":     return import("php-zts.yaml");
        case "pie":         return import("pie.yaml");
        case "postgresql":  return import("postgresql.yaml");
        case "rabbitmq":    return import("rabbitmq.yaml");
        case "redis":       return import("redis.yaml");
        case "roadrunner":  return import("roadrunner.yaml");
        case "sqlite":      return import("sqlite.yaml");
        case "traefik":     return import("traefik.yaml");
        default:
            throw new Exception("read builtin tool manifest manifests/" ~ name.strip() ~ ".yaml: not found");
    }
}

/// Constructs a `ToolPlugin` from an embedded manifest and Go/D runtime hooks.
ToolPlugin newManifestPlugin(string name, PluginHooks hooks = PluginHooks.init) @safe
{
    auto manifest = loadBuiltinManifest(name);
    return manifestToPlugin(manifest, hooks);
}

/// Loads, parses, and validates an embedded built-in manifest by tool name.
PluginManifest loadBuiltinManifest(string name) @trusted
{
    auto data = builtinManifestYAML(name);
    return parsePluginManifest(data);
}

/// Parses and validates a declarative tool manifest YAML payload.
PluginManifest parsePluginManifest(string data) @trusted
{
    auto root = parseYAML(data);
    if (!root.isMapping() && !root.isNull())
        throw new Exception("parse tool manifest: expected YAML mapping");

    PluginManifest manifest;
    if (root.isMapping())
    {
        manifest.id = scalarText(root.get("id"));
        auto internalVal = root.get("internal-only");
        if (internalVal.kind == YamlKind.bool_)
            manifest.internalOnly = internalVal.boolVal;

        manifest.exclusiveWith = stringListFromYaml(root.get("exclusive-with"));

        auto vc = root.get("version-constraint");
        if (vc.isMapping())
        {
            manifest.versionConstraint.min = scalarText(vc.get("min"));
            manifest.versionConstraint.below = scalarText(vc.get("below"));
        }

        auto depsVal = root.get("dependencies");
        if (depsVal.isSequence())
        {
            foreach (item; depsVal.seq)
            {
                if (!item.isMapping())
                    continue;
                manifest.dependencies ~= ManifestDependency(
                    scalarText(item.get("tool")),
                    scalarText(item.get("version"))
                );
            }
        }

        auto wrapVal = root.get("wrappers");
        if (wrapVal.isMapping())
        {
            foreach (entry; wrapVal.map)
            {
                ManifestWrapper w;
                if (entry.value.isMapping())
                {
                    w.target = platformPathsFromYaml(entry.value.get("target"));
                    w.args = stringListFromYaml(entry.value.get("args"));
                    auto skipVal = entry.value.get("skip-if-exists");
                    if (skipVal.kind == YamlKind.bool_)
                        w.skipIfExists = skipVal.boolVal;
                }
                manifest.wrappers[entry.key] = w;
            }
        }

        auto settingsVal = root.get("settings");
        if (settingsVal.isMapping())
        {
            foreach (entry; settingsVal.map)
            {
                auto rawType = scalarText(entry.value);
                SettingType st;
                if (!parseSettingType(rawType, st))
                {
                    manifest.rawSettingsInvalidTypeError = format(`tool manifest "%s" settings.%s has unsupported type "%s"`,
                        manifest.id.strip().toLower(), entry.key.strip().toLower(), rawType);
                    break;
                }
                manifest.settings[entry.key] = st;
            }
        }

        manifest.settingsFrom = scalarText(root.get("settings-from"));
        manifest.phpExtensions = stringListFromYaml(root.get("php-extensions"));
        manifest.installCandidates = platformPathsFromYaml(root.get("install-candidates"));
        manifest.dispatchCommands = stringListFromYaml(root.get("dispatch-commands"));
        manifest.cleanupCommands = stringListFromYaml(root.get("cleanup-commands"));
        manifest.activeCommands = stringListFromYaml(root.get("active-commands"));

        auto dcVal = root.get("dispatch-candidates");
        if (dcVal.isMapping())
        {
            foreach (entry; dcVal.map)
                manifest.dispatchCandidates[entry.key] = platformPathsFromYaml(entry.value);
        }

        auto logsVal = root.get("logs");
        if (logsVal.isMapping())
        {
            foreach (entry; logsVal.map)
                manifest.logs[entry.key] = stringListFromYaml(entry.value);
        }

        auto dlVal = root.get("download");
        if (dlVal.isMapping())
        {
            auto catVal = dlVal.get("catalog");
            if (catVal.isMapping() && catVal.map.length > 0)
                manifest.download.hasCatalog = true;

            auto ghVal = dlVal.get("github");
            if (ghVal.isMapping())
            {
                manifest.download.github.owner = scalarText(ghVal.get("owner"));
                manifest.download.github.repo = scalarText(ghVal.get("repo"));
                manifest.download.github.tagPrefix = scalarText(ghVal.get("tag-prefix"));
                manifest.download.github.mode = scalarText(ghVal.get("mode"));
            }

            auto riVal = dlVal.get("release-index");
            if (riVal.isMapping())
            {
                manifest.download.releaseIndex.url = scalarText(riVal.get("url"));
                manifest.download.releaseIndex.pattern = scalarText(riVal.get("pattern"));
            }

            auto assetsVal = dlVal.get("assets");
            if (assetsVal.isMapping())
            {
                foreach (entry; assetsVal.map)
                {
                    manifest.download.assets[entry.key] = parseDownloadAsset(
                        manifest.id.strip().toLower(),
                        entry.key,
                        entry.value
                    );
                }
            }
        }
    }

    validatePluginManifest(manifest);

    manifest.phpExtensions = normalizeManifestPHPExtensions(manifest.phpExtensions);
    manifest.settings = normalizeManifestSettings(manifest.settings);
    manifest.settingsFrom = manifest.settingsFrom.strip().toLower();
    manifest.exclusiveWith = normalizeCommands(manifest.exclusiveWith);
    manifest.versionConstraint.min = manifest.versionConstraint.min.strip();
    manifest.versionConstraint.below = manifest.versionConstraint.below.strip();
    manifest.dependencies = normalizeManifestDependencies(manifest.dependencies);
    manifest.wrappers = normalizeManifestWrappers(manifest.wrappers);
    manifest.download.github.mode = manifest.download.github.mode.strip().toLower();

    return manifest;
}

private DownloadAsset parseDownloadAsset(string toolID, string platform, const YamlValue node) @safe
{
    DownloadAsset asset;
    if (!node.isMapping())
        return asset;
    asset.fileName = scalarText(node.get("filename"));
    asset.sourceFileName = scalarText(node.get("source-filename"));
    asset.url = scalarText(node.get("url"));
    asset.installPath = scalarText(node.get("install-path"));
    asset.checksum = scalarText(node.get("checksum"));
    asset.checksumURL = scalarText(node.get("checksum-url"));

    auto rawAlg = scalarText(node.get("checksum-algorithm"));
    ChecksumAlgorithm alg;
    if (!parseChecksumAlgorithm(rawAlg, alg))
        throw new Exception(format(`tool manifest "%s" download assets %s has unsupported checksum algorithm "%s"`, toolID, platform, rawAlg));
    asset.checksumAlgorithm = alg;

    auto rawFmt = scalarText(node.get("archive-format"));
    ArchiveFormat fmt;
    if (!parseArchiveFormat(rawFmt, fmt))
        throw new Exception(format(`tool manifest "%s" download assets %s has unsupported archive format "%s"`, toolID, platform, rawFmt));
    asset.archiveFormat = fmt;

    return asset;
}

private void validatePluginManifest(const PluginManifest m) @trusted
{
    auto id = m.id.strip().toLower();
    if (id.length == 0)
        throw new Exception("tool manifest id cannot be empty");
    if (!isValidName(id))
        throw new Exception(format(`invalid tool manifest id "%s": use letters, numbers, dots, dashes, or underscores`, m.id));

    foreach (idx, ext; m.phpExtensions)
    {
        try
        {
            validatePHPExtensionName(ext);
        }
        catch (Exception e)
        {
            throw new Exception(format(`tool manifest "%s" php-extensions[%d]: %s`, id, idx, e.msg));
        }
    }

    if (m.installCandidates.length == 0)
        throw new Exception(format(`tool manifest "%s" requires install-candidates`, id));

    if (m.internalOnly)
    {
        if (m.dispatchCommands.length > 0 || m.activeCommands.length > 0 || m.cleanupCommands.length > 0 || m.dispatchCandidates.length > 0)
            throw new Exception(format(`tool manifest "%s" is internal-only and cannot define dispatch, active, or cleanup commands`, id));
        if (m.settings.length > 0 || m.rawSettingsInvalidTypeError.length > 0 || m.settingsFrom.strip().length > 0 || m.exclusiveWith.length > 0)
            throw new Exception(format(`tool manifest "%s" is internal-only and cannot define settings or exclusive-with`, id));
    }

    if (m.rawSettingsInvalidTypeError.length > 0)
        throw new Exception(m.rawSettingsInvalidTypeError);

    if (m.settings.length > 0 && m.settingsFrom.strip().length > 0)
        throw new Exception(format(`tool manifest "%s" cannot define both settings and settings-from`, id));

    auto settingsFrom = m.settingsFrom.strip().toLower();
    if (settingsFrom.length > 0)
    {
        if (!isValidName(settingsFrom))
            throw new Exception(format(`tool manifest "%s" has invalid settings-from "%s"`, id, m.settingsFrom));
        if (settingsFrom == id)
            throw new Exception(format(`tool manifest "%s" settings-from cannot reference itself`, id));
    }

    foreach (key, settingType; m.settings)
    {
        auto normalizedKey = key.strip().toLower();
        if (normalizedKey.length == 0 || !isValidName(normalizedKey))
            throw new Exception(format(`tool manifest "%s" has invalid settings key "%s"`, id, key));
    }

    foreach (idx, peer; m.exclusiveWith)
    {
        auto normalizedPeer = peer.strip().toLower();
        if (normalizedPeer.length == 0 || !isValidName(normalizedPeer))
            throw new Exception(format(`tool manifest "%s" exclusive-with[%d] has invalid tool id "%s"`, id, idx, peer));
        if (normalizedPeer == id)
            throw new Exception(format(`tool manifest "%s" exclusive-with[%d] cannot reference itself`, id, idx));
    }

    if (!m.versionConstraint.isZero())
    {
        auto minVer = m.versionConstraint.min.strip();
        auto belowVer = m.versionConstraint.below.strip();
        if (minVer.length > 0)
        {
            try { validateVersion(id, minVer); }
            catch (Exception e) { throw new Exception(format(`tool manifest "%s" version-constraint.min: %s`, id, e.msg)); }
        }
        if (belowVer.length > 0)
        {
            try { validateVersion(id, belowVer); }
            catch (Exception e) { throw new Exception(format(`tool manifest "%s" version-constraint.below: %s`, id, e.msg)); }
        }
        if (minVer.length > 0 && belowVer.length > 0 && compareVersions(minVer, belowVer) >= 0)
            throw new Exception(format(`tool manifest "%s" version-constraint min "%s" must be lower than below "%s"`, id, minVer, belowVer));
    }

    foreach (idx, dep; m.dependencies)
    {
        auto depTool = dep.tool.strip().toLower();
        if (depTool.length == 0 || !isValidName(depTool))
            throw new Exception(format(`tool manifest "%s" dependencies[%d] has invalid tool id "%s"`, id, idx, dep.tool));
        if (depTool == id)
            throw new Exception(format(`tool manifest "%s" dependencies[%d] cannot reference itself`, id, idx));
        try { validateVersion(depTool, dep.version_); }
        catch (Exception e) { throw new Exception(format(`tool manifest "%s" dependencies[%d]: %s`, id, idx, e.msg)); }
    }

    foreach (rawName, wrapper; m.wrappers)
    {
        auto name = rawName.strip().toLower();
        if (name.length == 0 || !isValidName(name))
            throw new Exception(format(`tool manifest "%s" has invalid wrappers key "%s"`, id, rawName));
        if (wrapper.target.length == 0)
            throw new Exception(format(`tool manifest "%s" wrappers.%s requires target`, id, name));
        validateManifestPlatformPaths(id, "wrappers." ~ name ~ ".target", wrapper.target);
        foreach (idx, arg; wrapper.args)
        {
            if (!isValidWrapperArg(arg.strip()))
                throw new Exception(format(`tool manifest "%s" wrappers.%s.args[%d] has invalid argument "%s"`, id, name, idx, arg));
        }
    }

    validateManifestPlatformPaths(id, "install-candidates", m.installCandidates);

    foreach (cmd, paths; m.dispatchCandidates)
    {
        if (cmd.strip().length == 0)
            throw new Exception(format(`tool manifest "%s" has empty dispatch-candidates command`, id));
        validateManifestPlatformPaths(id, "dispatch-candidates." ~ cmd, paths);
    }

    foreach (level, paths; m.logs)
    {
        auto normalizedLevel = normalizeLogLevel(level);
        if (normalizedLevel.length == 0)
            throw new Exception(format(`tool manifest "%s" logs has empty level`, id));
        if (!validLogLevel(normalizedLevel))
            throw new Exception(format(`tool manifest "%s" logs has unsupported level "%s"`, id, level));
        if (paths.length == 0)
            throw new Exception(format(`tool manifest "%s" logs.%s requires at least one path`, id, normalizedLevel));
        foreach (idx, path; paths)
        {
            if (path.strip().length == 0)
                throw new Exception(format(`tool manifest "%s" logs.%s[%d] requires path`, id, normalizedLevel, idx));
            try
            {
                validateManifestLogPath(path);
            }
            catch (Exception e)
            {
                throw new Exception(format(`tool manifest "%s" logs.%s[%d]: %s`, id, normalizedLevel, idx, e.msg));
            }
        }
    }

    if (m.download.hasCatalog)
        throw new Exception(format(`tool manifest "%s" uses deprecated download.catalog; use download.assets`, id));

    if (m.download.hasGitHub())
    {
        if (m.download.github.owner.strip().length == 0)
            throw new Exception(format(`tool manifest "%s" download.github requires owner`, id));
        if (m.download.github.repo.strip().length == 0)
            throw new Exception(format(`tool manifest "%s" download.github requires repo`, id));
        switch (m.download.github.mode.strip().toLower())
        {
            case "":
            case "releases":
            case "tag":
                break;
            default:
                throw new Exception(format(`tool manifest "%s" download.github has unsupported mode "%s"`, id, m.download.github.mode));
        }
    }
    else if (m.download.github.mode.strip().length > 0)
    {
        throw new Exception(format(`tool manifest "%s" download.github.mode requires owner and repo`, id));
    }

    if (m.download.hasReleaseIndex())
    {
        import polka.tools.download_io : compileManifestRegex, renderDownloadTemplate;
        if (m.download.releaseIndex.url.strip().length == 0)
            throw new Exception(format(`tool manifest "%s" download.release-index requires url`, id));
        try
        {
            renderDownloadTemplate("download.release-index.url", m.download.releaseIndex.url, ["requested": "1.0"]);
        }
        catch (Exception e)
        {
            throw new Exception(format(`tool manifest "%s" %s`, id, e.msg));
        }
        if (m.download.releaseIndex.pattern.strip().length == 0)
            throw new Exception(format(`tool manifest "%s" download.release-index requires pattern`, id));
        try
        {
            compileManifestRegex(m.download.releaseIndex.pattern);
        }
        catch (Exception e)
        {
            throw new Exception(format(`tool manifest "%s" download.release-index has invalid pattern: %s`, id, e.msg));
        }
    }

    foreach (platform, asset; m.download.assets)
    {
        if (!validDownloadPlatform(platform))
            throw new Exception(format(`tool manifest "%s" download assets has unsupported platform "%s"`, id, platform));
        validateDownloadAsset(id, platform, asset);
        if (asset.checksumAlgorithm == ChecksumAlgorithm.embeddedCommitID)
        {
            if (!m.download.hasGitHub())
                throw new Exception(format(`tool manifest "%s" download assets %s with %s requires download.github`, id, platform, cast(string) ChecksumAlgorithm.embeddedCommitID));
            if (asset.archiveFormat != ArchiveFormat.zip)
                throw new Exception(format(`tool manifest "%s" download assets %s with %s requires archive-format %s`, id, platform, cast(string) ChecksumAlgorithm.embeddedCommitID, cast(string) ArchiveFormat.zip));
        }
    }
}

private void validateManifestPlatformPaths(string tool, string field, const ManifestPlatformPaths paths) @safe
{
    foreach (platform, candidates; paths)
    {
        if (platform.strip().length == 0)
            throw new Exception(format(`tool manifest "%s" has empty %s platform`, tool, field));
        if (candidates.length == 0)
            throw new Exception(format(`tool manifest "%s" %s platform "%s" has no paths`, tool, field, platform));
        foreach (candidate; candidates)
        {
            try
            {
                validateManifestRelativePath(candidate);
            }
            catch (Exception e)
            {
                throw new Exception(format(`tool manifest "%s" %s platform "%s": %s`, tool, field, platform, e.msg));
            }
        }
    }
}

private void validateManifestRelativePath(string candidate) @safe
{
    validateManifestContainedPath(candidate, "candidate path", "install directory");
}

private void validateManifestLogPath(string path) @safe
{
    validateManifestContainedPath(path, "log path", "tool log root");
}

private void validateManifestContainedPath(string candidate, string label, string rootLabel) @safe
{
    auto trimmed = candidate.strip();
    if (trimmed.length == 0)
        throw new Exception(label ~ " cannot be empty");
    if (isAbsolute(trimmed) || trimmed.startsWith("/") || trimmed.startsWith(`\`))
        throw new Exception(format(`%s "%s" must be relative`, label, candidate));

    auto clean = toSlashPath(cleanPath(toSlashPath(trimmed)));
    if (clean == "." || clean == ".." || clean.startsWith("../"))
        throw new Exception(format(`%s "%s" must stay inside the %s`, label, candidate, rootLabel));
}

private void validateDownloadAsset(string tool, string platform, const DownloadAsset asset) @safe
{
    if (asset.fileName.strip().length == 0)
        throw new Exception(format(`tool manifest "%s" download assets %s requires filename`, tool, platform));
    if (asset.url.strip().length == 0)
        throw new Exception(format(`tool manifest "%s" download assets %s requires url`, tool, platform));

    switch (asset.checksumAlgorithm)
    {
        case ChecksumAlgorithm.none:
        case ChecksumAlgorithm.md5:
        case ChecksumAlgorithm.sha256:
        case ChecksumAlgorithm.sha3_256:
        case ChecksumAlgorithm.embeddedCommitID:
            break;
        default:
            throw new Exception(format(`tool manifest "%s" download assets %s has unsupported checksum algorithm "%s"`, tool, platform, cast(string) asset.checksumAlgorithm));
    }

    if (asset.installPath.strip().length > 0)
    {
        try
        {
            validateManifestRelativePath(asset.installPath);
        }
        catch (Exception e)
        {
            throw new Exception(format(`tool manifest "%s" download assets %s install-path: %s`, tool, platform, e.msg));
        }
        if (asset.archiveFormat != ArchiveFormat.none)
            throw new Exception(format(`tool manifest "%s" download assets %s cannot define archive-format with install-path`, tool, platform));
    }
    else
    {
        switch (asset.archiveFormat)
        {
            case ArchiveFormat.zip:
            case ArchiveFormat.tarGz:
            case ArchiveFormat.tarXz:
                break;
            default:
                throw new Exception(format(`tool manifest "%s" download assets %s has unsupported archive format "%s"`, tool, platform, cast(string) asset.archiveFormat));
        }
    }

    if (asset.checksum.strip().length > 0)
        throw new Exception(format(`tool manifest "%s" download assets %s uses deprecated checksum; use checksum-url or resolver-provided checksum`, tool, platform));
}

private bool validDownloadPlatform(string platform) @safe pure
{
    switch (platform.strip())
    {
        case "windows-amd64":
        case "linux-amd64":
        case "all":
            return true;
        default:
            return false;
    }
}

/// Converts a validated `PluginManifest` and optional runtime hooks into a `ToolPlugin`.
ToolPlugin manifestToPlugin(const PluginManifest m, PluginHooks hooks = PluginHooks.init) @safe
{
    auto manifestID = m.id.strip().toLower();
    auto manifestCopy = m;

    auto downloadFn = hooks.download;
    if (downloadFn is null && m.download.assets.length > 0)
    {
        downloadFn = (DownloadContext ctx) {
            downloadManifestRequest(ctx, manifestCopy);
        };
    }

    auto rawVersionFn = manifestVersionFunc(m);
    auto versionFn = rawVersionFn;
    auto validateFn = hooks.validate;
    if (m.internalOnly)
    {
        versionFn = (const Environment) => "";
        validateFn = internalOnlyValidateFunc(manifestID, rawVersionFn, hooks.validate);
    }

    auto depsFn = hooks.dependencies;
    if (depsFn is null && m.dependencies.length > 0)
    {
        InstallRequest[] declaredDeps;
        foreach (dep; m.dependencies)
            declaredDeps ~= InstallRequest(dep.tool.strip().toLower(), dep.version_.strip());
        depsFn = (const Environment env) {
            if (rawVersionFn(env).strip().length == 0)
                return cast(InstallRequest[]) null;
            return declaredDeps.dup;
        };
    }

    auto settings = normalizeManifestSettings(m.settings);
    auto settingsNamespace = m.settingsFrom.strip().toLower();
    if (settingsNamespace.length == 0 && settings.length > 0)
        settingsNamespace = manifestID;

    auto plugin = new BuiltinPlugin();
    plugin.idValue = manifestID;
    plugin.versionFn = versionFn;
    plugin.internalOnlyValue = m.internalOnly;
    plugin.internalVersionFn = rawVersionFn;
    plugin.versionConstraint = m.versionConstraint;
    plugin.wrappers = normalizeManifestWrappers(m.wrappers);
    plugin.executablePathsFn = manifestExecutablePathsFunc(m);
    plugin.settingsMap = settings;
    plugin.settingsNamespaceValue = settingsNamespace;
    plugin.exclusiveWithList = normalizeCommands(m.exclusiveWith);
    plugin.phpExtensionsMap = manifestPHPExtensionMap(m.phpExtensions);
    plugin.validateHook = validateFn;
    plugin.installCandidatesFn = manifestInstallCandidatesFunc(m);
    plugin.dispatchCommandsList = normalizeCommands(m.dispatchCommands);
    plugin.cleanupCommandsList = normalizeCommands(m.cleanupCommands);
    plugin.activeCommandsFn = manifestActiveCommandsFunc(m);
    plugin.dispatchCandidatesFn = manifestDispatchCandidatesFunc(m);
    plugin.logEntries = normalizeLogEntries(m.logs);
    plugin.downloadHook = downloadFn;
    plugin.postInstallHook = hooks.postInstall;
    plugin.dependenciesHook = depsFn;
    return plugin;
}

/// Resolves `tool`'s built-in manifest asset for `resolvedVersion` and downloads/caches it.
void downloadBuiltinManifestToolResolved(
    HTTPClient client,
    string cacheDir,
    string tool,
    string requestedVersion,
    string resolvedVersion,
    const(string[string]) values = null
) @trusted
{
    auto m = loadBuiltinManifest(tool);
    auto asset = resolveManifestDownloadAsset(
        tool, m.download.assets, requestedVersion, resolvedVersion, resolvedVersion,
        currentOS(), currentArch(), values
    );
    downloadManifestAsset(client, cacheDir, tool, requestedVersion, resolvedVersion, asset);
}

/// Resolves the first existing executable binary for a manifest-based built-in tool under `root`.
string resolveBuiltinInstalledTool(string root, string tool, string ver) @trusted
{
    auto m = loadBuiltinManifest(tool);
    auto candidates = manifestCandidatePaths(buildPath(root, tool, ver), m.installCandidates, currentOS(), currentArch());
    if (candidates.length == 0)
        throw new Exception(format(`unsupported tool "%s"`, tool));
    foreach (candidate; candidates)
    {
        if (exists(candidate) && !isDir(candidate))
            return candidate;
    }
    throw new Exception(format(`%s version "%s" is not installed under %s`, tool, ver, buildPath(root, tool, ver)));
}

private ManifestDependency[] normalizeManifestDependencies(const(ManifestDependency)[] dependencies) @safe pure
{
    if (dependencies.length == 0)
        return null;
    ManifestDependency[] normalized;
    foreach (dep; dependencies)
        normalized ~= ManifestDependency(dep.tool.strip().toLower(), dep.version_.strip());
    return normalized;
}

private ManifestWrapper[string] normalizeManifestWrappers(const(ManifestWrapper[string]) wrappers) @safe pure
{
    if (wrappers.length == 0)
        return null;
    ManifestWrapper[string] normalized;
    foreach (rawName, wrapper; wrappers)
    {
        auto name = rawName.strip().toLower();
        if (name.length == 0)
            continue;
        string[] args;
        foreach (arg; wrapper.args)
        {
            auto trimmed = arg.strip();
            if (trimmed.length > 0)
                args ~= trimmed;
        }
        ManifestWrapper nw;
        nw.target = copyPlatformPaths(wrapper.target);
        nw.args = args;
        nw.skipIfExists = wrapper.skipIfExists;
        normalized[name] = nw;
    }
    return normalized.length == 0 ? null : normalized;
}

private string[] delegate(string) manifestExecutablePathsFunc(const PluginManifest m) @safe
{
    auto installPaths = copyPlatformPaths(m.installCandidates);
    auto dispatchCommands = m.dispatchCandidates.keys;
    dispatchCommands.sort();
    ManifestPlatformPaths[string] dispatchCandidates;
    foreach (cmd, paths; m.dispatchCandidates)
        dispatchCandidates[cmd] = copyPlatformPaths(paths);

    return (string installDir) {
        if (installDir.strip().length == 0)
            return cast(string[]) null;
        auto paths = manifestCandidatePaths(installDir, installPaths, currentOS(), currentArch());
        foreach (cmd; dispatchCommands)
            paths ~= manifestCandidatePaths(installDir, dispatchCandidates[cmd], currentOS(), currentArch());
        return paths;
    };
}

private void delegate(const Environment) internalOnlyValidateFunc(
    string id,
    string delegate(const Environment) rawVersion,
    void delegate(const Environment) next
) @safe
{
    return (const Environment env) {
        if (rawVersion(env).strip().length > 0)
            throw new Exception(id ~ " is managed internally by polka and cannot be configured in a project environment");
        if (next !is null)
            next(env);
    };
}

private string delegate(const Environment) manifestVersionFunc(const PluginManifest m) @safe pure
{
    auto id = m.id.strip().toLower();
    return (const Environment env) {
        switch (id)
        {
            case MySQL:
            case MariaDB:
            case PostgreSQL:
                return databaseToolVersion(env, id);
            default:
                return env.toolVersion(id);
        }
    };
}

private string[] delegate(string, string) manifestInstallCandidatesFunc(const PluginManifest m) @safe
{
    auto tool = m.id.strip().toLower();
    auto paths = copyPlatformPaths(m.installCandidates);
    return (string root, string ver) => manifestCandidatePaths(buildPath(root, tool, ver), paths, currentOS(), currentArch());
}

private string[] delegate(const Environment) manifestActiveCommandsFunc(const PluginManifest m) @safe
{
    auto versionFn = manifestVersionFunc(m);
    auto activeCmds = normalizeCommands(m.activeCommands);
    auto dispatchCmds = normalizeCommands(m.dispatchCommands);

    return (const Environment env) {
        if (versionFn(env).strip().length == 0)
            return cast(string[]) null;
        if (activeCmds.length > 0)
            return copyStrings(activeCmds);
        return copyStrings(dispatchCmds);
    };
}

private string[] delegate(string, string, string) manifestDispatchCandidatesFunc(const PluginManifest m) @safe
{
    auto tool = m.id.strip().toLower();
    ManifestPlatformPaths[string] dc;
    foreach (cmd, paths; m.dispatchCandidates)
        dc[cmd.strip().toLower()] = copyPlatformPaths(paths);
    auto installPaths = copyPlatformPaths(m.installCandidates);

    return (string root, string executable, string ver) {
        auto normalizedExecutable = executable.strip().toLower();
        auto installDir = buildPath(root, tool, ver);
        if (auto paths = normalizedExecutable in dc)
            return manifestCandidatePaths(installDir, *paths, currentOS(), currentArch());
        return manifestCandidatePaths(installDir, installPaths, currentOS(), currentArch());
    };
}

private string[] normalizeCommands(const(string)[] commands) @safe pure
{
    string[] normalized;
    foreach (cmd; commands)
    {
        auto trimmed = cmd.strip().toLower();
        if (trimmed.length > 0)
            normalized ~= trimmed;
    }
    return normalized.length == 0 ? null : normalized;
}

private LogEntry[] normalizeLogEntries(const ManifestLogs logs) @safe pure
{
    if (logs.length == 0)
        return null;
    LogEntry[] normalized;
    foreach (level; [LogLevelInfo, LogLevelError, LogLevelDebug])
    {
        foreach (rawLevel, paths; logs)
        {
            if (normalizeLogLevel(rawLevel) != level)
                continue;
            foreach (path; paths)
                normalized ~= LogEntry(path.strip(), level);
        }
    }
    return normalized;
}

package string[] normalizeManifestPHPExtensions(const(string)[] extensions) @safe pure
{
    string[] normalized;
    bool[string] seen;
    foreach (ext; extensions)
    {
        auto name = ext.strip().toLower();
        if (name in seen)
            continue;
        seen[name] = true;
        normalized ~= name;
    }
    return normalized;
}

package bool[string] manifestPHPExtensionMap(const(string)[] extensions) @safe pure
{
    if (extensions.length == 0)
        return null;
    bool[string] result;
    foreach (ext; extensions)
        result[ext] = true;
    return result;
}

private string scalarText(const YamlValue node) @safe pure
{
    if (node.isNull())
        return "";
    return node.asScalarText();
}

private string[] stringListFromYaml(const YamlValue node) @safe pure
{
    if (!node.isSequence())
        return null;
    string[] outList;
    foreach (item; node.seq)
    {
        if (item.isNull())
            outList ~= "";
        else
            outList ~= item.asScalarText();
    }
    return outList;
}

private ManifestPlatformPaths platformPathsFromYaml(const YamlValue node) @safe pure
{
    if (!node.isMapping())
        return null;
    ManifestPlatformPaths outMap;
    foreach (entry; node.map)
        outMap[entry.key] = stringListFromYaml(entry.value);
    return outMap;
}

unittest
{
    import polka.tools.download_io : resolveManifestDownloadAsset;

    // TestBuiltinManifestsLoad
    foreach (tool; [
        PHP, PHPZTS, PHPDevel, PHPSDK, FrankenPHP, Composer, PIE, NodeJS, Mago, Nginx,
        Apache, Mailpit, Meilisearch, Redis, Erlang, RabbitMQ, Traefik, RoadRunner,
        PHPMyAdmin, MySQL, MariaDB, PostgreSQL, SQLite,
    ])
    {
        auto m = loadBuiltinManifest(tool);
        assert(m.id == tool, tool);
    }

    // TestPHPBuildToolManifestsAreInternalOnly
    foreach (tool; [PHPDevel, PHPSDK])
    {
        auto m = loadBuiltinManifest(tool);
        assert(m.internalOnly);
        assert(m.dispatchCommands.length == 0 && m.dispatchCandidates.length == 0);
    }

    // TestPHPSDKManifestResolvesTaggedSourceArchive
    {
        auto m = loadBuiltinManifest(PHPSDK);
        auto tag = m.download.github.tagPrefix ~ DefaultPHPSDKVersion;
        auto asset = resolveManifestDownloadAsset(PHPSDK, m.download.assets, DefaultPHPSDKVersion, DefaultPHPSDKVersion, tag, "windows", "amd64", null);
        assert(asset.url == "https://github.com/php/php-sdk-binary-tools/archive/refs/tags/php-sdk-2.8.3.zip");
        assert(asset.checksumAlgorithm == ChecksumAlgorithm.embeddedCommitID);
        assert(asset.archiveFormat == ArchiveFormat.zip);
    }

    // TestParsePluginManifestRequiresID & RejectsInvalidID
    bool threwMissingID = false;
    try { parsePluginManifest("install-candidates:\n  all:\n    - bin/demo\n"); }
    catch (Exception e) { threwMissingID = e.msg.canFind("id cannot be empty"); }
    assert(threwMissingID);

    bool threwBadID = false;
    try { parsePluginManifest("id: \"not a tool\"\ninstall-candidates:\n  all:\n    - bin/demo\n"); }
    catch (Exception e) { threwBadID = e.msg.canFind("invalid tool manifest id"); }
    assert(threwBadID);

    // TestParsePluginManifestNormalizesPHPExtensions
    {
        auto m = parsePluginManifest("id: demo\nphp-extensions:\n  - \" OpenSSL \"\n  - openssl\n  - PDO_MySQL\ninstall-candidates:\n  all:\n    - bin/demo\n");
        assert(m.phpExtensions == ["openssl", "pdo_mysql"]);
        auto p = manifestToPlugin(m);
        auto exts = p.phpExtensions();
        exts["openssl"] = false;
        assert(p.phpExtensions()["openssl"] == true);
    }

    // TestParsePluginManifestRejectsInvalidPHPExtensions
    foreach (badExt; ["", "bad extension"])
    {
        bool threw = false;
        try { parsePluginManifest("id: demo\nphp-extensions:\n  - \"" ~ badExt ~ "\"\ninstall-candidates:\n  all:\n    - bin/demo\n"); }
        catch (Exception e) { threw = e.msg.canFind("php-extensions[0]"); }
        assert(threw);
    }

    // TestParsePluginManifestRejectsUnknownDownloadPlatform & DeprecatedCatalog
    {
        bool threwPlat = false;
        try
        {
            parsePluginManifest("id: demo\ninstall-candidates:\n  all:\n    - bin/demo\ndownload:\n  assets:\n    darwin-arm64:\n      filename: d.tgz\n      url: https://x/d.tgz\n      archive-format: tar.gz\n");
        }
        catch (Exception e) { threwPlat = e.msg.canFind("unsupported platform"); }
        assert(threwPlat);

        bool threwCat = false;
        try
        {
            parsePluginManifest("id: demo\ninstall-candidates:\n  all:\n    - bin/demo\ndownload:\n  catalog:\n    1.2.3:\n      windows-amd64:\n        filename: d.zip\n");
        }
        catch (Exception e) { threwCat = e.msg.canFind("deprecated download.catalog"); }
        assert(threwCat);
    }

    // TestParsePluginManifestBuildsLogs
    {
        auto m = parsePluginManifest("id: demo\ninstall-candidates:\n  all:\n    - bin/demo\nlogs:\n  info:\n    - demo.log\n  debug:\n    - debug/demo.log\n");
        auto p = manifestToPlugin(m);
        assert(p.logs() == [LogEntry("demo.log", LogLevelInfo), LogEntry("debug/demo.log", LogLevelDebug)]);
    }

    // TestParsePluginManifestRejectsInvalidLogs
    {
        import std.file : tempDir;
        auto absPath = toSlashPath(buildPath(tempDir(), "demo.log"));
        struct LogCase { string yaml; string want; }
        foreach (tc; [
            LogCase("  info:\n    - \"\"\n", "requires path"),
            LogCase("  info:\n    - " ~ absPath ~ "\n", "must be relative"),
            LogCase("  info:\n    - ../demo.log\n", "must stay inside the tool log root"),
            LogCase("  \"\":\n    - demo.log\n", "empty level"),
            LogCase("  trace:\n    - demo.log\n", "unsupported level"),
            LogCase("  info:\n", "requires at least one path"),
        ])
        {
            bool threw = false;
            try { parsePluginManifest("id: demo\ninstall-candidates:\n  all:\n    - bin/demo\nlogs:\n" ~ tc.yaml); }
            catch (Exception e) { threw = e.msg.canFind(tc.want); }
            assert(threw, tc.want);
        }
    }

    // TestManifestPluginBuildsCandidatesAndDispatch & DispatchFallback & MariaDB version
    {
        auto m = parsePluginManifest("id: php\ninstall-candidates:\n  all:\n    - bin/demo\ndispatch-commands:\n  - demo\ndispatch-candidates:\n  demo:\n    all:\n      - shims/demo\n");
        auto p = manifestToPlugin(m);
        Environment env;
        env.tools = [PHP: "8.4"];
        assert(p.version_(env) == "8.4");
        assert(p.installCandidates("root", "1.2.3") == [buildPath("root", "php", "1.2.3", "bin", "demo")]);
        assert(p.dispatchCandidates("root", "demo", "1.2.3") == [buildPath("root", "php", "1.2.3", "shims", "demo")]);

        auto m2 = parsePluginManifest("id: demo\ninstall-candidates:\n  all:\n    - bin/demo\ndispatch-commands:\n  - foo\n");
        auto p2 = manifestToPlugin(m2);
        assert(p2.dispatchCandidates("root", "foo", "1.2.3") == [buildPath("root", "demo", "1.2.3", "bin", "demo")]);

        auto m3 = parsePluginManifest("id: mariadb\ninstall-candidates:\n  all:\n    - bin/mariadb\n");
        auto p3 = manifestToPlugin(m3);
        Environment env3;
        env3.tools = [MariaDB: "11.8"];
        env3.database = new DatabaseConfig(MariaDB, "11.8");
        assert(p3.version_(env3) == "11.8");
    }

    // TestParsePluginManifestValidatesSettingsAndExclusiveWith & DeclarativeFields
    struct ValidationCase { string yaml; string want; }
    foreach (tc; [
        ValidationCase("id: demo\nsettings:\n  port: integer\ninstall-candidates:\n  all:\n    - bin/demo\n", "unsupported type"),
        ValidationCase("id: demo\nsettings:\n  port: port\nsettings-from: other\ninstall-candidates:\n  all:\n    - bin/demo\n", "cannot define both settings and settings-from"),
        ValidationCase("id: demo\nsettings-from: demo\ninstall-candidates:\n  all:\n    - bin/demo\n", "settings-from cannot reference itself"),
        ValidationCase("id: demo\nexclusive-with:\n  - demo\ninstall-candidates:\n  all:\n    - bin/demo\n", "exclusive-with[0] cannot reference itself"),
        ValidationCase("id: demo\ninternal-only: true\nsettings:\n  port: port\ninstall-candidates:\n  all:\n    - bin/demo\n", "internal-only and cannot define settings"),
        ValidationCase("id: demo\nversion-constraint:\n  min: \"5.0\"\n  below: \"4.0\"\ninstall-candidates:\n  all:\n    - bin/demo\n", "version-constraint min"),
        ValidationCase("id: demo\ndependencies:\n  - tool: demo\n    version: \"1.0\"\ninstall-candidates:\n  all:\n    - bin/demo\n", "dependencies[0] cannot reference itself"),
        ValidationCase("id: demo\ndependencies:\n  - tool: helper\ninstall-candidates:\n  all:\n    - bin/demo\n", "dependencies[0]: helper version cannot be empty"),
        ValidationCase("id: demo\nwrappers:\n  cli:\n    target:\n      all:\n        - bin/demo\n    args:\n      - \"sub command\"\ninstall-candidates:\n  all:\n    - bin/demo\n", "wrappers.cli.args[0] has invalid argument"),
        ValidationCase("id: demo\nwrappers:\n  cli:\n    target:\n      all:\n        - ../outside\ninstall-candidates:\n  all:\n    - bin/demo\n", "wrappers.cli.target"),
        ValidationCase("id: demo\ninstall-candidates:\n  all:\n    - bin/demo\ndownload:\n  github:\n    owner: acme\n    repo: demo\n    mode: branches\n", "unsupported mode"),
        ValidationCase("id: demo\ninstall-candidates:\n  all:\n    - bin/demo\ndownload:\n  release-index:\n    url: https://example.test/{version}/index.html\n    pattern: 'demo-([0-9]+\\.[0-9]+\\.[0-9]+)'\n", `unknown template value "version"`),
        ValidationCase("id: demo\ninstall-candidates:\n  all:\n    - bin/demo\ndownload:\n  assets:\n    windows-amd64:\n      filename: demo.zip\n      url: https://example.test/demo.zip\n      checksum-algorithm: embedded-commit-id\n      archive-format: zip\n", "requires download.github"),
        ValidationCase("id: demo\ninstall-candidates:\n  all:\n    - bin/demo\ndownload:\n  github:\n    owner: acme\n    repo: demo\n  assets:\n    linux-amd64:\n      filename: demo.tar.gz\n      url: https://example.test/demo.tar.gz\n      checksum-algorithm: embedded-commit-id\n      archive-format: tar.gz\n", "requires archive-format zip"),
    ])
    {
        bool threw = false;
        try { parsePluginManifest(tc.yaml); }
        catch (Exception e) { threw = e.msg.canFind(tc.want); }
        assert(threw, tc.want);
    }
}
