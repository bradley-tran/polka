module polka.backend.schema;

import std.algorithm : canFind, startsWith;
import std.array : appender, join;
import std.file : exists, write;
import std.format : format;
import std.path : buildPath, dirName, extension, isAbsolute;
import std.regex : matchFirst, regex, replaceAll;
import std.string : endsWith, icmp, indexOf, replace, strip, toLower;

import polka.backend.aliases;
import polka.config :
    hasPHPCLI,
    normalizeOPcachePreset,
    normalizePHPMemoryLimit,
    OPcachePresetDev,
    OPcachePresetProduction,
    primaryPHPTool,
    projectFileToYAML,
    environmentFileToYAML,
    ProjectFile,
    EnvironmentFile,
    splitWorkerCommand,
    validatePECLExtensionPackage,
    validatePECLExtensionVersion,
    validatePHPMemoryLimit;
import polka.service : toolLogRoot;
import polka.support.fs :
    cleanPath,
    currentOS,
    fromSlashPath,
    isPathWithin,
    octal,
    setFileMode,
    toSlashPath,
    userCacheDir;
import polka.support.yaml : CommentMap, emitYAML, parseYAML, parseYAMLWithComments, YamlKind, YamlValue;
import polka.tools : InstallRequest, SettingType;

/// Polka project layout, config, and dispatcher constants.
enum string defaultRootDirectoryName = ".polka";
enum string configFileName = "polka.yaml";
enum string namedConfigPrefix = "polka.";
enum string namedConfigSuffix = ".yaml";
enum string defaultEnvironmentName = "default";
enum string binDirectoryName = "bin";
enum string envsDirectoryName = "envs";
enum string runDirectoryName = "run";
enum string currentEnvironmentName = "current";
enum string configVersion = CurrentManifestVersion;
enum string dispatcherBinaryName = "polka";
enum string dispatcherBatchFileName = "polka.cmd";

/// Default managed tool versions used by `polka new` and on-demand internal provisioning.
enum string DefaultPHPVersion = "8.4";
enum string DefaultComposerVersion = "2.8";
enum string DefaultNodeJSVersion = "24";
enum string DefaultPIEVersion = "1";

/// Returns the default version for on-demand provisioning of an internal tool, or empty if none.
string defaultInternalToolVersion(string tool) @safe pure nothrow @nogc
{
    switch (tool)
    {
        case toolPHP:
        case toolPHPZTS:
            return DefaultPHPVersion;
        case toolComposer:
            return DefaultComposerVersion;
        case toolNodeJS:
            return DefaultNodeJSVersion;
        case toolPIE:
            return DefaultPIEVersion;
        default:
            return "";
    }
}

/// Validates an environment / key identifier (`^[A-Za-z0-9][A-Za-z0-9._-]*$`).
bool isValidBackendName(string s) @safe
{
    auto re = regex(`^[A-Za-z0-9][A-Za-z0-9._-]*$`);
    return !s.matchFirst(re).empty;
}

/// Formats `s` using Go `%q`-style double quotes for exact error message parity.
package(polka.backend) string quoteGoString(string s) @safe pure
{
    auto buf = appender!string();
    buf.put('"');
    foreach (char c; s)
    {
        switch (c)
        {
            case '\\': buf.put(`\\`); break;
            case '"':  buf.put(`\"`); break;
            case '\n': buf.put(`\n`); break;
            case '\r': buf.put(`\r`); break;
            case '\t': buf.put(`\t`); break;
            default:
                if (c < 0x20 || c == 0x7f)
                    buf.put(format(`\x%02x`, cast(ubyte) c));
                else
                    buf.put(c);
                break;
        }
    }
    buf.put('"');
    return buf.data;
}

/// Validates a project environment name.
void validateName(string name) @safe
{
    auto trimmed = name.strip();
    if (trimmed.length == 0)
        throw new Exception("environment name cannot be empty");
    if (!isValidBackendName(trimmed))
        throw new Exception(format("invalid environment name %s: use letters, numbers, dots, dashes, or underscores",
            quoteGoString(name)));
}

/// Validates a managed tool version label.
void validateVersion(string tool, string ver) @safe
{
    auto trimmed = ver.strip();
    if (trimmed.length == 0)
        throw new Exception(format("%s version cannot be empty", tool));
    if (!isValidBackendName(trimmed))
        throw new Exception(format("invalid %s version %s: use letters, numbers, dots, dashes, or underscores",
            tool, quoteGoString(ver)));
}

/// Derives `<dirname>.localhost` hostname from a project directory basename.
string projectLocalHostname(string projectDir) @safe
{
    import std.path : baseName;

    auto base = baseName(cleanPath(projectDir)).strip().toLower();
    auto re = regex(`[^a-z0-9-]+`);
    auto label = replaceAll(base, re, "-");
    while (label.length > 0 && label[0] == '-')
        label = label[1 .. $];
    while (label.length > 0 && label[$ - 1] == '-')
        label = label[0 .. $ - 1];
    if (label.length == 0 || label == ".")
        label = "project";
    return label ~ ".localhost";
}

/// Resolves a configured `root` field relative to `projectDir` (or `.polka` if empty).
string resolveConfiguredRootDir(string projectDir, string configuredRoot) @trusted
{
    auto trimmedRoot = configuredRoot.strip();
    if (trimmedRoot.length == 0)
        return buildPath(projectDir, defaultRootDirectoryName);
    if (isAbsolute(trimmedRoot))
        return cleanPath(trimmedRoot);
    return cleanPath(buildPath(projectDir, fromSlashPath(trimmedRoot)));
}

/// Extracts `<name>` from `polka.<name>.yaml`, ignoring `polka.yaml` itself.
bool namedEnvironmentNameFromFile(string fileName, out string name) @safe pure
{
    if (fileName == configFileName || !fileName.startsWith(namedConfigPrefix) || !fileName.endsWith(namedConfigSuffix))
    {
        name = "";
        return false;
    }
    name = fileName[namedConfigPrefix.length .. $ - namedConfigSuffix.length];
    return true;
}

/// Reports whether raw YAML text defines a specific top-level mapping key.
bool yamlHasTopLevelKey(string yamlText, string key) @safe
{
    auto stripped = yamlText.strip();
    if (stripped.length == 0)
        return false;
    auto root = parseYAML(yamlText);
    if (root.isNull())
        return false;
    if (!root.isMapping())
        throw new Exception("expected YAML mapping");
    return root.hasKey(key);
}

/// Overload accepting raw YAML bytes.
bool yamlHasTopLevelKey(const(ubyte)[] data, string key) @trusted
{
    return yamlHasTopLevelKey((cast(const(char)[]) data).idup, key);
}

/// Returns the standardized error for legacy multi-environment `polka.yaml` files.
Exception legacyConfigError(string path) @safe pure
{
    return new Exception(format(
        "legacy environments config in %s is no longer supported; move default settings to polka.yaml and named settings to polka.<name>.yaml",
        path
    ));
}

// ============================================================================
// Environment Config Schema Validators
// ============================================================================

/// Validates raw environment YAML structure against tool, extension, setting, and worker rules.
void validateEnvironmentFileSchema(string yamlText, ToolRegistry registry) @trusted
{
    if (registry is null)
        registry = newDefaultToolRegistry();
    if (yamlText.strip().length == 0)
        return;

    auto raw = parseYAML(yamlText);
    if (raw.isNull())
        return;
    if (!raw.isMapping())
        throw new Exception("config must be a YAML mapping");

    YamlValue toolsMap;
    bool hasTools = rawMapForKey(raw, "tools", toolsMap);
    if (hasTools)
        validateToolsSchema(toolsMap, registry);

    YamlValue settingsMap;
    bool hasSettings = rawMapForKey(raw, "settings", settingsMap);
    if (hasSettings)
        validateSettingsSchema(settingsMap, toolsMap, registry);

    if (auto p = raw.ptr("opcache-preset"))
        validateOPcachePresetSchema(*p);
    if (auto p = raw.ptr("memory-limit"))
        validatePHPMemoryLimitSchema(*p);

    YamlValue opcacheConfig;
    if (rawMapForKey(raw, "opcache-config", opcacheConfig))
        validateOPcacheConfigSchema(opcacheConfig);

    YamlValue phpExtensions;
    if (rawMapForKey(raw, "php-extensions", phpExtensions))
        validatePHPExtensionsSchema(phpExtensions);

    YamlValue peclExtensions;
    if (rawMapForKey(raw, "pecl-extensions", peclExtensions))
        validatePECLExtensionsSchema(peclExtensions);

    YamlValue workersMap;
    if (rawMapForKey(raw, "workers", workersMap))
        validateWorkersSchema(workersMap);
}

/// Overload accepting raw YAML bytes.
void validateEnvironmentFileSchema(const(ubyte)[] data, ToolRegistry registry) @trusted
{
    validateEnvironmentFileSchema(cast(string) data, registry);
}

/// Checks bundled extension booleans and PIE `vendor/name` version strings.
void validatePHPExtensionsSchema(const YamlValue values) @safe
{
    foreach (entry; values.map)
    {
        if (entry.key.canFind('/'))
        {
            if (!isYAMLVersionScalar(entry.value))
            {
                throw new Exception(format(
                    "php-extensions.%s must be a version constraint for PIE-managed extensions",
                    entry.key
                ));
            }
            continue;
        }
        if (entry.value.kind != YamlKind.bool_)
        {
            throw new Exception(format(
                "php-extensions.%s must be true or false; use a vendor/name key for PIE-managed extensions",
                entry.key
            ));
        }
    }
}

/// Validates legacy PECL entries (version scalar or object with `version` and `configure-options`).
void validatePECLExtensionsSchema(const YamlValue values) @safe
{
    foreach (entry; values.map)
    {
        auto name = entry.key;
        validatePECLExtensionPackage(name);

        if (isYAMLVersionScalar(entry.value))
        {
            validatePECLExtensionVersion(name, yamlVersionScalarString(entry.value));
            continue;
        }
        if (!entry.value.isMapping())
        {
            throw new Exception(format(
                "pecl-extensions.%s must be a version or an object with version and configure-options",
                name
            ));
        }
        foreach (sub; entry.value.map)
        {
            if (sub.key != "version" && sub.key != "configure-options")
            {
                throw new Exception(format("pecl-extensions.%s has unsupported key %s",
                    name, quoteGoString(sub.key)));
            }
        }
        auto verPtr = entry.value.ptr("version");
        if (verPtr is null || !isYAMLVersionScalar(*verPtr))
            throw new Exception(format("pecl-extensions.%s.version must be an exact version or *", name));
        validatePECLExtensionVersion(name, yamlVersionScalarString(*verPtr));

        if (auto rawOptsPtr = entry.value.ptr("configure-options"))
        {
            if (!rawOptsPtr.isMapping())
                throw new Exception(format("pecl-extensions.%s.configure-options must be a map", name));
            foreach (opt; rawOptsPtr.map)
            {
                auto trimmedOpt = trimDashes(opt.key.strip());
                if (trimmedOpt.length == 0 || !isYAMLSettingScalar(opt.value))
                {
                    throw new Exception(format("pecl-extensions.%s.configure-options.%s must be a scalar value",
                        name, opt.key));
                }
            }
        }
    }
}

private string trimDashes(string s) @safe pure nothrow @nogc
{
    while (s.length > 0 && s[0] == '-')
        s = s[1 .. $];
    while (s.length > 0 && s[$ - 1] == '-')
        s = s[0 .. $ - 1];
    return s;
}

/// Converts a validated version scalar YAML node to a trimmed string.
string yamlVersionScalarString(const YamlValue value) @safe pure
{
    if (value.isNull())
        return "*";
    return value.asScalarText().strip();
}

/// Checks every key in `tools` is a registered tool plugin with a scalar version label.
void validateToolsSchema(const YamlValue toolsMap, ToolRegistry registry) @trusted
{
    foreach (entry; toolsMap.map)
    {
        if (entry.key == "database")
        {
            throw new Exception(
                "tools.database is no longer supported; use tools.mysql or tools.mariadb for versions and root database for runtime settings"
            );
        }
        ToolPlugin dummy;
        if (!registry.plugin(entry.key, dummy))
            throw new Exception(format("unsupported tools.%s key", entry.key));
        if (!isYAMLVersionScalar(entry.value))
            throw new Exception(format("tools.%s must be a scalar version label", entry.key));
    }
}

/// Validates `settings.<namespace>.<key>` against plugin schemas in `registry`.
void validateSettingsSchema(const YamlValue settings, const YamlValue toolsMap, ToolRegistry registry) @trusted
{
    foreach (entry; settings.map)
    {
        auto key = entry.key;
        if (!entry.value.isMapping())
            throw new Exception(format("settings.%s must be a mapping", key));

        SettingType[string] schema;
        string[] owners;
        if (!registry.settingsSchema(key, schema, owners))
            throw new Exception(format("unsupported settings.%s key", key));

        bool hasOwner = false;
        foreach (owner; owners)
        {
            if (hasConfiguredToolVersion(toolsMap, owner))
            {
                hasOwner = true;
                break;
            }
        }
        if (!hasOwner)
            throw new Exception(format("settings.%s requires tools.%s", key, owners.join(" or tools.")));

        foreach (settingEntry; entry.value.map)
        {
            auto settingKey = settingEntry.key;
            auto stPtr = settingKey in schema;
            if (stPtr is null)
                throw new Exception(format("unsupported settings.%s.%s key", key, settingKey));
            if (!isYAMLSettingScalar(settingEntry.value))
                throw new Exception(format("settings.%s.%s must be a scalar value", key, settingKey));
            if (*stPtr == SettingType.bool_ && settingEntry.value.kind != YamlKind.bool_)
                throw new Exception(format("settings.%s.%s must be true or false", key, settingKey));
        }
    }
}

/// Validates worker definitions (`command`, optional `replicas`, `dir`, `env`).
void validateWorkersSchema(const YamlValue workers) @safe
{
    foreach (entry; workers.map)
    {
        auto name = entry.key;
        if (!isValidBackendName(name))
        {
            throw new Exception(format(
                "invalid workers.%s key: use letters, numbers, dots, dashes, or underscores",
                name
            ));
        }
        if (!entry.value.isMapping())
            throw new Exception(format("workers.%s must be a mapping", name));

        bool hasCommand = false;
        foreach (field; entry.value.map)
        {
            switch (field.key)
            {
                case "command":
                    if (field.value.kind != YamlKind.string_ || field.value.strVal.strip().length == 0)
                        throw new Exception(format("workers.%s.command must be a non-empty string", name));
                    try
                    {
                        splitWorkerCommand(field.value.strVal);
                    }
                    catch (Exception e)
                    {
                        throw new Exception(format("workers.%s.command is invalid: %s", name, e.msg));
                    }
                    hasCommand = true;
                    break;
                case "replicas":
                    if (!isPositiveYAMLInteger(field.value))
                        throw new Exception(format("workers.%s.replicas must be a positive integer", name));
                    break;
                case "dir":
                    if (field.value.kind != YamlKind.string_
                        || field.value.strVal.strip().length == 0
                        || field.value.strVal.canFind('\r')
                        || field.value.strVal.canFind('\n'))
                    {
                        throw new Exception(format("workers.%s.dir must be a non-empty single-line string", name));
                    }
                    break;
                case "env":
                    if (!field.value.isMapping())
                        throw new Exception(format("workers.%s.env must be a mapping", name));
                    foreach (envEntry; field.value.map)
                    {
                        if (!isYAMLSettingScalar(envEntry.value))
                            throw new Exception(format("workers.%s.env.%s must be a scalar value", name, envEntry.key));
                    }
                    break;
                default:
                    throw new Exception(format("unsupported workers.%s.%s key", name, field.key));
            }
        }
        if (!hasCommand)
            throw new Exception(format("workers.%s.command must be a non-empty string", name));
    }
}

/// Reports whether a decoded YAML value is an integer strictly greater than zero.
bool isPositiveYAMLInteger(const YamlValue value) @safe pure nothrow @nogc
{
    return value.kind == YamlKind.int_ && value.intVal > 0;
}

/// Validates `opcache-preset` schema value.
void validateOPcachePresetSchema(const YamlValue value) @safe
{
    if (value.kind != YamlKind.string_)
        throw new Exception("opcache-preset must be one of none, dev, or production");

    auto preset = value.strVal;
    switch (normalizeOPcachePreset(preset))
    {
        case "":
        case OPcachePresetDev:
        case OPcachePresetProduction:
            return;
        default:
            throw new Exception(format("unsupported opcache-preset %s: use none, dev, or production",
                quoteGoString(preset)));
    }
}

/// Validates `memory-limit` YAML scalar type and PHP shorthand syntax.
void validatePHPMemoryLimitSchema(const YamlValue value) @safe
{
    if (value.kind != YamlKind.string_ && value.kind != YamlKind.int_)
        throw new Exception("memory-limit must be a scalar string or integer value");
    validatePHPMemoryLimit(value.asScalarText());
}

/// Validates `opcache-config` directive keys and single-line scalar values.
void validateOPcacheConfigSchema(const YamlValue settings) @safe
{
    foreach (entry; settings.map)
    {
        validateOPcacheConfigKey(entry.key);
        if (!isYAMLOPcacheScalar(entry.value))
        {
            throw new Exception(format(
                "opcache-config.%s must be a scalar string, number, or boolean value",
                entry.key
            ));
        }
        if (entry.value.kind == YamlKind.string_
            && (entry.value.strVal.canFind('\r') || entry.value.strVal.canFind('\n')))
        {
            throw new Exception(format("opcache-config.%s must be a single-line scalar value", entry.key));
        }
    }
}

/// Checks `opcache-config.<directive>` uses `opcache.*` and allowed characters.
void validateOPcacheConfigKey(string key) @safe
{
    auto normalized = key.strip().toLower();
    if (!normalized.startsWith("opcache.") || normalized == "opcache.")
        throw new Exception(format("unsupported opcache-config.%s key: use opcache.* directive names", key));
    if (!isValidBackendName(normalized))
    {
        throw new Exception(format(
            "invalid opcache-config.%s key: use letters, numbers, dots, dashes, or underscores",
            key
        ));
    }
}

private bool rawMapForKey(const YamlValue raw, string key, out YamlValue mapping) @safe pure
{
    if (!raw.hasKey(key))
    {
        mapping = YamlValue.nullValue();
        return false;
    }
    auto p = raw.ptr(key);
    if (p is null || !p.isMapping())
        throw new Exception(format("%s must be a mapping", key));
    mapping = p.clone();
    return true;
}

private bool hasConfiguredToolVersion(const YamlValue toolsMap, string key) @safe pure
{
    if (!toolsMap.isMapping())
        return false;
    auto p = toolsMap.ptr(key);
    if (p is null)
        return false;
    if (p.kind == YamlKind.string_)
        return p.strVal.strip().length > 0;
    return isYAMLVersionScalar(*p);
}

bool isYAMLVersionScalar(const YamlValue value) @safe pure nothrow @nogc
{
    return value.kind == YamlKind.string_
        || value.kind == YamlKind.int_
        || value.kind == YamlKind.float_;
}

bool isYAMLSettingScalar(const YamlValue value) @safe pure nothrow @nogc
{
    return isYAMLVersionScalar(value) || value.kind == YamlKind.bool_;
}

bool isYAMLOPcacheScalar(const YamlValue value) @safe pure nothrow @nogc
{
    return isYAMLSettingScalar(value);
}

// ============================================================================
// Comment-Preserving YAML Persistence
// ============================================================================

/// Serializes `value` (`YamlValue`) preserving comments from any existing file at `path`.
string marshalYAMLValue(string path, ref const YamlValue value) @trusted
{
    string existingData;
    try
    {
        existingData = readTextStrict(path);
    }
    catch (ConfigNotExistException)
    {
        return emitYAML(value);
    }

    if (existingData.strip().length == 0)
        return emitYAML(value);

    CommentMap comments;
    try
    {
        parseYAMLWithComments(existingData, comments);
    }
    catch (Exception e)
    {
        throw new Exception("decode existing YAML comments: " ~ e.msg);
    }

    if (comments.length == 0)
        return emitYAML(value);
    return emitYAML(value, comments);
}

/// Writes `value` (`YamlValue`) to `path` with mode 0644 and trailing newline, preserving comments.
void writeYAMLValue(string path, ref const YamlValue value) @trusted
{
    auto data = marshalYAMLValue(path, value);
    if (data.length == 0 || data[$ - 1] != '\n')
        data ~= "\n";
    write(path, data);
    setFileMode(path, octal!644);
}

/// Writes a `ProjectFile` to `path` preserving existing YAML comments.
void writeYAML(string path, const ProjectFile file) @trusted
{
    auto doc = projectFileToYAML(file);
    writeYAMLValue(path, doc);
}

/// Writes an `EnvironmentFile` to `path` preserving existing YAML comments.
void writeYAML(string path, const EnvironmentFile file) @trusted
{
    auto doc = environmentFileToYAML(file);
    writeYAMLValue(path, doc);
}

// ============================================================================
// Install / Dispatch / Log / Shim Helpers
// ============================================================================

/// Validates and normalizes an explicit `polka install <tool> <version>` request.
InstallRequest normalizeInstallRequest(ToolRegistry registry, string tool, string ver) @trusted
{
    auto normalizedTool = tool.strip().toLower();
    auto normalizedVersion = ver.strip();
    if (normalizedTool.length == 0)
        throw new Exception("tool cannot be empty");

    ToolPlugin dummy;
    if (!registry.plugin(normalizedTool, dummy))
        throw new Exception(format("unsupported tool %s", quoteGoString(tool)));
    if (registry.internalOnly(normalizedTool))
    {
        throw new Exception(format(
            "%s is managed internally by polka and cannot be installed into a project environment",
            normalizedTool
        ));
    }
    validateVersion(normalizedTool, normalizedVersion);
    return InstallRequest(normalizedTool, normalizedVersion);
}

/// Returns a copy of `environment` updated with `request`, clearing exclusive peers and syncing DB version.
Environment environmentWithInstallRequest(ToolRegistry registry, Environment environment, InstallRequest request) @trusted
{
    if (registry is null)
        registry = newDefaultToolRegistry();

    environment.setToolVersion(request.tool, request.version_);
    if (request.version_.strip().length > 0)
    {
        foreach (peer; registry.exclusivePeers(request.tool))
            environment.setToolVersion(peer, "");
    }
    if (environment.database !is null && icmp(environment.database.engine.strip(), request.tool) == 0)
        environment.database.version_ = request.version_;

    return environment;
}

/// Validates environment install invariants prior to downloading/extracting payloads.
void validateInstallEnvironment(
    string name,
    const Environment environment,
    const(InstallRequest)[] requests,
    ToolRegistry registry
) @trusted
{
    auto hasCLI = hasPHPCLI(environment);
    auto includesPHP = installRequestsIncludeTool(requests, toolPHP)
        || installRequestsIncludeTool(requests, toolPHPZTS)
        || installRequestsIncludeTool(requests, toolFrankenPHP);

    if ((environment.phpExtensions.length > 0 || environment.pieExtensions.length > 0 || environment.peclExtensions.length > 0)
        && !hasCLI && !includesPHP)
    {
        throw new Exception(format(
            "environment %s defines php-extensions but does not define a PHP CLI provider",
            quoteGoString(name)
        ));
    }

    if (environment.pieExtensions.length > 0)
    {
        string pTool, pVer;
        primaryPHPTool(environment, pTool, pVer);
        if (pTool.length == 0)
        {
            throw new Exception(format(
                "environment %s defines PIE-managed php-extensions but no standalone php or php-zts runtime; PIE cannot target FrankenPHP's embedded PHP",
                quoteGoString(name)
            ));
        }
    }
    if (environment.peclExtensions.length > 0)
    {
        string pTool, pVer;
        primaryPHPTool(environment, pTool, pVer);
        if (pTool.length == 0)
        {
            throw new Exception(format(
                "environment %s defines legacy pecl-extensions but no standalone php or php-zts runtime; PECL cannot target FrankenPHP's embedded PHP",
                quoteGoString(name)
            ));
        }
    }
    if (normalizePHPMemoryLimit(environment.memoryLimit).length > 0 && !hasCLI && !includesPHP)
    {
        throw new Exception(format(
            "environment %s defines memory-limit but does not define a PHP CLI provider",
            quoteGoString(name)
        ));
    }
    if (environmentHasOPcacheConfig(environment) && !hasCLI && !includesPHP)
    {
        throw new Exception(format(
            "environment %s defines OPcache config but does not define a PHP CLI provider",
            quoteGoString(name)
        ));
    }
    registry.validateEnvironment(environment);
}

/// Reports whether `environment` configures an OPcache preset or any explicit `opcache-config` directive.
bool environmentHasOPcacheConfig(const Environment environment) @safe pure
{
    return normalizeOPcachePreset(environment.opcachePreset).length > 0 || environment.opcacheConfig.length > 0;
}

/// Reports whether `requests` contains a request matching `tool` case-insensitively.
bool installRequestsIncludeTool(const(InstallRequest)[] requests, string tool) @safe pure
{
    auto target = tool.strip();
    foreach (req; requests)
    {
        if (icmp(req.tool.strip(), target) == 0)
            return true;
    }
    return false;
}

/// Distinguishes thread-safe vs non-thread-safe `php-devel` cache payloads.
string installCacheVersion(const Environment environment, string tool, string ver) @safe pure
{
    if (tool != toolPHPDevel)
        return ver;
    if (environment.toolVersion(toolPHPZTS).strip().length > 0)
        return ver ~ "-zts";
    return ver ~ "-nts";
}

/// Resolves and contains a declared tool log template path inside the tool log root.
string resolveToolLogPath(string rootDir, string tool, string environmentName, string pathTemplate) @trusted
{
    auto relPath = pathTemplate.strip();
    if (relPath.length == 0)
        throw new Exception("tool log path cannot be empty");
    auto nativeRel = fromSlashPath(relPath);
    if (isAbsolute(nativeRel))
        throw new Exception(format("tool log path %s must be relative", quoteGoString(pathTemplate)));

    auto root = cleanPath(toolLogRoot(rootDir, tool, environmentName));
    auto candidate = cleanPath(buildPath(root, nativeRel));
    if (candidate == root || !isPathWithin(root, candidate))
        throw new Exception(format("tool log path %s must stay inside the tool log root", quoteGoString(pathTemplate)));
    return candidate;
}

/// Returns the default tool cache directory (respecting `POLKA_CACHE_DIR`).
string defaultCacheDir(string projectDir) @trusted
{
    auto overrideDir = getProcessEnvVar("POLKA_CACHE_DIR").strip();
    if (overrideDir.length > 0)
        return cleanPath(fromSlashPath(overrideDir));

    try
    {
        auto cd = userCacheDir();
        if (cd.length > 0)
            return buildPath(cd, "polka", "cache");
    }
    catch (Exception) {}

    return buildPath(projectDir, ".polka-cache");
}

private string getProcessEnvVar(string key) @trusted
{
    import std.process : environment;
    return environment.get(key, "");
}

/// Generated shim/dispatcher binary entry under `.polka/bin`.
struct InstalledBinary
{
    string name;
    uint mode = octal!755;
    string contents;
}

InstalledBinary[] dispatcherBinaries(string selfPath) @safe pure
{
    return [
        shellDispatcherBinary(selfPath),
        windowsDispatcherBinary(selfPath),
    ];
}

InstalledBinary shellDispatcherBinary(string selfPath) @safe pure
{
    return InstalledBinary(
        dispatcherBinaryName,
        octal!755,
        "#!/usr/bin/env sh\n"
            ~ "set -eu\n"
            ~ "POLKA_EXE=${POLKA_DISPATCHER:-}\n"
            ~ "if [ -z \"$POLKA_EXE\" ]; then\n"
            ~ "  POLKA_EXE=" ~ shellLiteral(toSlashPath(selfPath)) ~ "\n"
            ~ "fi\n"
            ~ "if [ ! -f \"$POLKA_EXE\" ]; then\n"
            ~ "  printf '%s\\n' 'Polka executable not found. Re-run \"polka init\" or set POLKA_DISPATCHER.' >&2\n"
            ~ "  exit 1\n"
            ~ "fi\n"
            ~ "exec \"$POLKA_EXE\" \"$@\"\n"
    );
}

InstalledBinary windowsDispatcherBinary(string selfPath) @safe pure
{
    return InstalledBinary(
        dispatcherBatchFileName,
        octal!755,
        "@echo off\r\n"
            ~ "setlocal\r\n"
            ~ "set \"POLKA_EXE=%POLKA_DISPATCHER%\"\r\n"
            ~ "if not defined POLKA_EXE set \"POLKA_EXE=" ~ escapeWindowsBatchValue(selfPath) ~ "\"\r\n"
            ~ "if not exist \"%POLKA_EXE%\" (\r\n"
            ~ "  >&2 echo Polka executable not found. Re-run polka init or set POLKA_DISPATCHER.\r\n"
            ~ "  exit /b 1\r\n"
            ~ ")\r\n"
            ~ "\"%POLKA_EXE%\" %*\r\n"
            ~ "exit /b %ERRORLEVEL%\r\n"
    );
}

InstalledBinary shellDispatchBinary(string tool) @safe pure
{
    return InstalledBinary(
        tool,
        octal!755,
        "#!/usr/bin/env sh\n"
            ~ "set -eu\n"
            ~ "SCRIPT_DIR=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" && pwd)\n"
            ~ "ROOT_DIR=$(CDPATH= cd -- \"$SCRIPT_DIR/..\" && pwd)\n"
            ~ "DISPATCHER=${POLKA_TOOL_DISPATCHER:-$SCRIPT_DIR/" ~ dispatcherBinaryName ~ "}\n"
            ~ "DISPATCH_ROOT=${POLKA_TOOL_ROOT:-$ROOT_DIR}\n"
            ~ "if [ ! -f \"$DISPATCHER\" ]; then\n"
            ~ "  printf '%s\\n' 'Polka dispatcher shim not found in .polka/bin. Re-run \"polka init\".' >&2\n"
            ~ "  exit 1\n"
            ~ "fi\n"
            ~ "export POLKA_TOOL_DISPATCHER=\"$DISPATCHER\"\n"
            ~ "export POLKA_TOOL_ROOT=\"$DISPATCH_ROOT\"\n"
            ~ "exec \"$DISPATCHER\" --root \"$DISPATCH_ROOT\" dispatch " ~ tool ~ " \"$@\"\n"
    );
}

InstalledBinary windowsDispatchBinary(string tool) @safe pure
{
    return InstalledBinary(
        tool ~ ".cmd",
        octal!755,
        "@echo off\r\n"
            ~ "setlocal\r\n"
            ~ "set \"SCRIPT_DIR=%~dp0\"\r\n"
            ~ "set \"ROOT_DIR=%SCRIPT_DIR%..\"\r\n"
            ~ "set \"DISPATCHER=%POLKA_TOOL_DISPATCHER%\"\r\n"
            ~ "if not defined DISPATCHER set \"DISPATCHER=%SCRIPT_DIR%" ~ dispatcherBatchFileName ~ "\"\r\n"
            ~ "set \"DISPATCH_ROOT=%POLKA_TOOL_ROOT%\"\r\n"
            ~ "if not defined DISPATCH_ROOT set \"DISPATCH_ROOT=%ROOT_DIR%\"\r\n"
            ~ "if not exist \"%DISPATCHER%\" (\r\n"
            ~ "  >&2 echo Polka dispatcher shim not found in .polka\\bin. Re-run polka init.\r\n"
            ~ "  exit /b 1\r\n"
            ~ ")\r\n"
            ~ "set \"POLKA_TOOL_DISPATCHER=%DISPATCHER%\"\r\n"
            ~ "set \"POLKA_TOOL_ROOT=%DISPATCH_ROOT%\"\r\n"
            ~ "call \"%DISPATCHER%\" --root \"%DISPATCH_ROOT%\" dispatch " ~ tool ~ " %*\r\n"
            ~ "exit /b %ERRORLEVEL%\r\n"
    );
}

string shellLiteral(string value) @safe pure
{
    return "'" ~ value.replace("'", "'\"'\"'") ~ "'";
}

string escapeWindowsBatchValue(string value) @safe pure
{
    return value.replace("%", "%%");
}

string dispatcherBinaryFileName() @safe pure nothrow
{
    return currentOS() == "windows" ? dispatcherBatchFileName : dispatcherBinaryName;
}

unittest
{
    // validateName / validateVersion / installCacheVersion / projectLocalHostname
    validateName("staging-1.0");
    bool threw = false;
    try { validateName("_internal"); } catch (Exception) { threw = true; }
    assert(threw);

    Environment ztsEnv;
    ztsEnv.tools = ["php-zts": "8.4"];
    assert(installCacheVersion(ztsEnv, toolPHPDevel, "8.4") == "8.4-zts");
    Environment ntsEnv;
    ntsEnv.tools = ["php": "8.4"];
    assert(installCacheVersion(ntsEnv, toolPHPDevel, "8.4") == "8.4-nts");
    assert(installCacheVersion(ntsEnv, toolPHP, "8.4") == "8.4");

    assert(projectLocalHostname("/tmp/My_App!") == "my-app.localhost");
    assert(projectLocalHostname("/") == "project.localhost");

    // validateEnvironmentFileSchema checks
    validateEnvironmentFileSchema("tools:\n  php: \"8.4\"\nsettings:\n  php:\n    extension-sdk: true\n", null);
    bool schemaThrew = false;
    try { validateEnvironmentFileSchema("tools:\n  database: \"8.4\"\n", null); }
    catch (Exception e) { schemaThrew = e.msg.canFind("tools.database is no longer supported"); }
    assert(schemaThrew);

    schemaThrew = false;
    try { validateEnvironmentFileSchema("memory-limit: 1.5\n", null); }
    catch (Exception e) { schemaThrew = e.msg.canFind("memory-limit must be a scalar string or integer"); }
    assert(schemaThrew);

    schemaThrew = false;
    try { validateEnvironmentFileSchema("workers:\n  queue:\n    replicas: 0\n    command: php run\n", null); }
    catch (Exception e) { schemaThrew = e.msg.canFind("positive integer"); }
    assert(schemaThrew);
}
