module polka.plugins.manifest;

import std.algorithm : canFind;
import std.format : format;
import std.regex : matchAll;
import std.string : strip, toLower;

import polka.config;
import polka.plugins.ids;
import polka.plugins.framework_actions;
import polka.plugins.framework_composer;
import polka.plugins.manifest_values;
import polka.support.yaml;

/// Compile-time embedded framework manifests and templates matching Go `embed.FS`.
private immutable string[string] builtinFrameworkAssetMap = [
    "manifests/cakephp.yaml":                      import("cakephp.yaml"),
    "manifests/codeigniter.yaml":                  import("codeigniter.yaml"),
    "manifests/drupal.yaml":                       import("drupal.yaml"),
    "manifests/laravel.yaml":                      import("laravel.yaml"),
    "manifests/symfony.yaml":                      import("symfony.yaml"),
    "manifests/wordpress.yaml":                    import("wordpress.yaml"),
    "manifests/templates/drupal-settings.php":     import("drupal-settings.php"),
    "manifests/templates/php-return-array.php":    import("php-return-array.php"),
    "manifests/templates/wordpress-config.php":    import("wordpress-config.php"),
];

/// Returns the built-in framework manifest & template asset source.
FrameworkAssetSource builtinFrameworkAssetSource() @safe pure
{
    string[string] mutableMap;
    foreach (k, v; builtinFrameworkAssetMap)
        mutableMap[k] = v;
    return new MemoryFrameworkAssetSource(mutableMap);
}

/// FrameworkPlugin implementation backed by a validated declarative `FrameworkManifest`.
final class BuiltinFrameworkPlugin : FrameworkPlugin
{
    private string idValue;
    private Environment defaultsValue;
    private bool[string] phpExtensionsMap;
    private bool[string] databaseEnginesSet;
    private string[string] opcacheConfigMap;
    private FrameworkManifest manifestValue;
    private const(FrameworkAssetSource) filesValue;
    private bool delegate(const NginxConfigContext, out NginxConfigResult) nginxHook;

    this(
        string id,
        Environment defaults,
        bool[string] phpExtensions,
        bool[string] databaseEngines,
        string[string] opcacheConfig,
        const FrameworkManifest manifest,
        const FrameworkAssetSource files,
        bool delegate(const NginxConfigContext, out NginxConfigResult) nginx = null
    ) @trusted
    {
        this.idValue = id;
        this.defaultsValue = normalizeEnvironment("", defaults);
        this.phpExtensionsMap = phpExtensions;
        this.databaseEnginesSet = databaseEngines;
        this.opcacheConfigMap = opcacheConfig;
        this.manifestValue = cast(FrameworkManifest) manifest;
        this.filesValue = files;
        this.nginxHook = nginx;
    }

    string id() const @safe pure
    {
        return idValue;
    }

    Environment defaults() const @safe pure
    {
        if (defaultsValue.framework.strip().length == 0)
        {
            Environment fallback;
            fallback.framework = idValue.strip().toLower();
            return fallback;
        }
        return normalizeEnvironment("", defaultsValue);
    }

    bool[string] phpExtensions() const @safe pure
    {
        return copyBoolMap(phpExtensionsMap);
    }

    /// Rejects database engines unsupported by this framework.
    void validateEnvironment(const Environment environment) const @safe
    {
        if (environment.database is null || databaseEnginesSet.length == 0)
            return;
        auto engine = environment.database.engine.strip().toLower();
        if (engine.length == 0 || (engine in databaseEnginesSet) !is null)
            return;
        throw new Exception(format("framework %s does not support database engine %s",
            formatGoQuoted(idValue), formatGoQuoted(engine)));
    }

    string[string] opcacheConfig() const @safe pure
    {
        return copyStringMap(opcacheConfigMap);
    }

    /// Returns isolated detection metadata so callers cannot mutate the registry.
    FrameworkDetection detection() const @safe pure
    {
        return manifestValue.detection.clone();
    }

    string[string] runtimeEnv(const RuntimeEnvContext ctx) const @safe
    {
        return frameworkManifestRuntimeEnv(ctx, manifestValue);
    }

    void postComposer(const PostComposerContext ctx) const @trusted
    {
        frameworkManifestPostComposer(ctx, manifestValue, filesValue);
    }

    bool nginxConfig(const NginxConfigContext ctx, out NginxConfigResult result) const
    {
        if (nginxHook is null)
        {
            result = NginxConfigResult.init;
            return false;
        }
        return nginxHook(ctx, result);
    }
}

/// Returns built-in framework plugins parsed from embedded manifests.
FrameworkPlugin[] defaultFrameworkPlugins() @safe
{
    return loadFrameworkPlugins(builtinFrameworkAssetSource());
}

/// Parses and returns a built-in framework plugin by name (primarily for focused tests).
FrameworkPlugin newManifestFrameworkPlugin(string name) @safe
{
    auto m = loadBuiltinFrameworkManifest(name);
    return frameworkManifestToPlugin(m, builtinFrameworkAssetSource());
}

/// Reads and validates a single embedded framework manifest by name.
FrameworkManifest loadBuiltinFrameworkManifest(string name) @safe
{
    auto filename = "manifests/" ~ name.strip() ~ ".yaml";
    auto files = builtinFrameworkAssetSource();
    string data;
    try
    {
        data = files.readFile(filename);
    }
    catch (Exception e)
    {
        throw new Exception(format("read builtin framework manifest %s: %s", filename, e.msg));
    }
    return parseFrameworkManifest(data);
}

/// Enumerates and loads all framework manifests in `files` without a hardcoded registry list.
FrameworkPlugin[] loadFrameworkPlugins(const FrameworkAssetSource files) @safe
{
    auto names = files.listManifests();
    FrameworkPlugin[] plugins;
    plugins.reserve(names.length);
    bool[string] seen;

    foreach (name; names)
    {
        string data;
        try
        {
            data = files.readFile(name);
        }
        catch (Exception e)
        {
            throw new Exception(format("read framework manifest %s: %s", name, e.msg));
        }
        FrameworkManifest m;
        try
        {
            m = parseFrameworkManifestWithFiles(data, files);
        }
        catch (Exception e)
        {
            throw new Exception(format("framework manifest %s: %s", name, e.msg));
        }
        if ((m.id in seen) !is null)
            throw new Exception(format("duplicate framework manifest %s", formatGoQuoted(m.id)));
        seen[m.id] = true;
        plugins ~= frameworkManifestToPlugin(m, files);
    }
    return plugins;
}

/// Validates a framework manifest YAML document against built-in template assets.
FrameworkManifest parseFrameworkManifest(string data) @safe
{
    return parseFrameworkManifestWithFiles(data, builtinFrameworkAssetSource());
}

/// Decodes and strictly validates a framework manifest YAML document against `files`.
FrameworkManifest parseFrameworkManifestWithFiles(string data, const FrameworkAssetSource files) @safe
{
    YamlValue root;
    try
    {
        root = parseYAML(data);
    }
    catch (Exception e)
    {
        throw new Exception(format("parse framework manifest: %s", e.msg));
    }

    FrameworkManifest m;
    if (!root.isNull())
    {
        if (!root.isMapping())
            throw new Exception("parse framework manifest: root must be a YAML mapping");

        foreach (entry; root.map)
        {
            switch (entry.key)
            {
                case "id":
                    if (!entry.value.isNull()) m.id = entry.value.asString();
                    break;
                case "defaults":
                    if (!entry.value.isNull()) m.defaults = environmentFileFromYAML(entry.value, true);
                    break;
                case "php-extensions":
                    m.phpExtensions = parseStringListYAML(entry.value, "php-extensions");
                    break;
                case "database-engines":
                    m.databaseEngines = parseStringListYAML(entry.value, "database-engines");
                    break;
                case "opcache-config":
                    m.opcacheConfig = parseStringMapYAML(entry.value, "opcache-config");
                    break;
                case "detection":
                    if (!entry.value.isNull()) m.detection = parseDetectionYAML(entry.value);
                    break;
                case "app-root":
                    if (!entry.value.isNull()) m.appRoot = parseAppRootYAML(entry.value);
                    break;
                case "values":
                    m.values = parseFrameworkValueMapYAML(entry.value, "values");
                    break;
                case "runtime-env":
                    if (!entry.value.isNull()) m.runtimeEnv = parseRuntimeEnvYAML(entry.value);
                    break;
                case "post-composer":
                    if (!entry.value.isNull()) m.postComposer = parsePostComposerYAML(entry.value);
                    break;
                default:
                    throw new Exception(format("parse framework manifest: unknown field %s", formatGoQuoted(entry.key)));
            }
        }
    }

    m.id = m.id.strip().toLower();
    try
    {
        validateFrameworkManifest(m, files);
    }
    catch (Exception e)
    {
        throw new Exception(format("framework manifest %s: %s", formatGoQuoted(m.id), e.msg));
    }
    m.defaults.framework = m.id;
    m.opcacheConfig = normalizeOPcacheConfig(m.opcacheConfig);
    return m;
}

/// Validates manifest metadata, portable relative paths, value graph, and all actions.
void validateFrameworkManifest(const FrameworkManifest m, const FrameworkAssetSource files) @safe
{
    if (m.id.length == 0)
        throw new Exception("framework manifest id cannot be empty");
    if (!isValidPluginID(m.id))
        throw new Exception(format("invalid framework manifest id %s", formatGoQuoted(m.id)));
    auto defFramework = m.defaults.framework.strip().toLower();
    if (defFramework.length > 0 && defFramework != m.id)
        throw new Exception("defaults.framework must match id");
    if (m.defaults.docroot.strip().length == 0)
        throw new Exception("defaults.docroot cannot be empty");
    validateWorkersConfig(m.defaults.workers);
    m.detection.validate();

    if (m.appRoot.publicDir.length > 0)
        validateFrameworkManifestRelativePath(m.appRoot.publicDir, "app-root.public-dir", true);
    else if (m.postComposer.actions.length > 0)
        throw new Exception("app-root.public-dir cannot be empty");

    foreach (marker; m.appRoot.markers)
        validateFrameworkManifestRelativePath(marker, "app-root.markers", false);

    foreach (engine; m.databaseEngines)
    {
        if (engine != "mysql" && engine != "mariadb" && engine != "postgresql" && engine != "sqlite")
            throw new Exception(format("unsupported database engine %s", formatGoQuoted(engine)));
    }

    validateFrameworkManifestValues(m);

    foreach (i, action; m.postComposer.actions)
    {
        try
        {
            validateFrameworkAction(action, files);
        }
        catch (Exception e)
        {
            throw new Exception(format("post-composer.actions[%d]: %s", i, e.msg));
        }
    }
}

/// Validates an action discriminator and its allowed/required fields.
void validateFrameworkAction(const FrameworkActionManifest a, const FrameworkAssetSource files) @safe
{
    if (a.type != "dotenv" && a.type != "php-array" && a.type != "php-constants"
        && a.type != "write-file" && a.type != "php-include")
    {
        throw new Exception(format("unsupported action type %s", formatGoQuoted(a.type)));
    }
    if (a.base.length > 0 && a.base != "app-root" && a.base != "docroot")
        throw new Exception(format("unsupported base %s", formatGoQuoted(a.base)));

    validateFrameworkManifestRelativePath(a.path, "path", false);
    if (a.fallback.length > 0)
        validateFrameworkManifestRelativePath(a.fallback, "fallback", false);
    if (a.include_.length > 0)
        validateFrameworkManifestRelativePath(a.include_, "include", false);

    foreach (templateName; [a.templateFile, a.fallbackTemplateFile])
    {
        if (templateName.length == 0)
            continue;
        validateFrameworkManifestRelativePath(templateName, "template-file", false);
        string data;
        try
        {
            data = files.readFile("manifests/templates/" ~ templateName);
        }
        catch (Exception e)
        {
            throw new Exception(format("read template %s: %s", formatGoQuoted(templateName), e.msg));
        }
        if (templateName == a.fallbackTemplateFile && !data.matchAll(frameworkPlaceholderRe()).empty)
            throw new Exception("fallback-template-file must contain literal content without placeholders");
    }

    validateFrameworkOrder(a.order, a.values);

    if (a.type != "php-array" && a.updates.length != 0)
        throw new Exception("updates requires php-array");
    if (a.type != "php-constants" && a.insertBefore.length != 0)
        throw new Exception("insert-before requires php-constants");
    if (a.type != "php-include" && (a.include_.length != 0 || a.marker.length != 0))
        throw new Exception("include and marker require php-include");
    if (a.type != "write-file" && (a.templateFile.length != 0 || a.encoding.length != 0))
        throw new Exception("template-file and encoding require write-file");
    if ((a.type == "php-array" || a.type == "php-include") && (a.values.length != 0 || a.order.length != 0))
        throw new Exception(format("values and order are not supported for %s", a.type));

    switch (a.type)
    {
        case "php-array":
            if (a.updates.length == 0)
                throw new Exception("php-array requires updates");
            foreach (u; a.updates)
            {
                foreach (key; u.path)
                {
                    if (key.length == 0)
                        throw new Exception("array path keys cannot be empty");
                }
                if (u.values.length == 0)
                    throw new Exception("array update requires values");
                validateFrameworkOrder(u.order, u.values);
            }
            break;
        case "php-include":
            if (a.include_.length == 0 || a.marker.length == 0 || !isValidPluginID(a.marker))
                throw new Exception("php-include requires include and a valid marker");
            break;
        case "write-file":
            if (a.templateFile.length == 0)
                throw new Exception("write-file requires template-file");
            if (a.encoding != "text" && a.encoding != "php-literal")
                throw new Exception("write-file encoding must be text or php-literal");
            if (a.fallback.length != 0 || a.fallbackTemplateFile.length != 0)
                throw new Exception("write-file does not support fallbacks");
            auto data = files.readFile("manifests/templates/" ~ a.templateFile);
            foreach (m; data.matchAll(frameworkPlaceholderRe()))
            {
                if ((m[1] in a.values) is null)
                    throw new Exception(format("unknown template placeholder %s", formatGoQuoted(m[1])));
            }
            break;
        default:
            if (a.values.length == 0)
                throw new Exception(format("%s requires values", a.type));
            break;
    }
}

/// Rejects misspelled or repeated output keys in `order:`.
void validateFrameworkOrder(const(string)[] order, const(FrameworkValue[string]) values) @safe pure
{
    bool[string] seen;
    foreach (key; order)
    {
        if ((key in values) is null || (key in seen) !is null)
            throw new Exception(format("order contains unknown or duplicate key %s", formatGoQuoted(key)));
        seen[key] = true;
    }
}

/// Converts a validated `FrameworkManifest` into a `BuiltinFrameworkPlugin`.
FrameworkPlugin frameworkManifestToPlugin(
    const FrameworkManifest m,
    const FrameworkAssetSource files = null
) @safe
{
    auto resolvedFiles = files !is null ? files : builtinFrameworkAssetSource();
    auto defaults = environmentFileToEnvironment("", m.defaults);
    defaults.opcacheConfig = normalizeOPcacheConfig(m.opcacheConfig);
    return new BuiltinFrameworkPlugin(
        m.id,
        normalizeEnvironment("", defaults),
        phpExtensionMap(m.phpExtensions),
        stringSet(m.databaseEngines),
        normalizeOPcacheConfig(m.opcacheConfig),
        m,
        resolvedFiles,
    );
}

/// Canonicalizes identifiers for read-only membership checks.
bool[string] stringSet(const(string)[] values) @safe pure
{
    bool[string] set;
    foreach (val; values)
    {
        auto v = val.strip().toLower();
        if (v.length > 0)
            set[v] = true;
    }
    return set;
}

// ============================================================================
// Strict YAML Structural Decoders
// ============================================================================

private string[] parseStringListYAML(const YamlValue node, string label) @safe
{
    if (node.isNull())
        return null;
    if (!node.isSequence())
        throw new Exception(format("%s must be a sequence", label));
    string[] items;
    items.reserve(node.seq.length);
    foreach (item; node.seq)
        items ~= item.isNull() ? "" : item.asString();
    return items;
}

private string[string] parseStringMapYAML(const YamlValue node, string label) @safe
{
    if (node.isNull())
        return null;
    if (!node.isMapping())
        throw new Exception(format("%s must be a mapping", label));
    string[string] result;
    foreach (entry; node.map)
        result[entry.key] = entry.value.isNull() ? "" : entry.value.asString();
    return result;
}

private FrameworkValue[string] parseFrameworkValueMapYAML(const YamlValue node, string label) @safe
{
    if (node.isNull())
        return null;
    if (!node.isMapping())
        throw new Exception(format("%s must be a mapping", label));
    FrameworkValue[string] result;
    foreach (entry; node.map)
        result[entry.key] = parseFrameworkValueYAML(entry.value);
    return result;
}

private FrameworkDetection parseDetectionYAML(const YamlValue node) @safe
{
    if (!node.isMapping())
        throw new Exception("detection must be a mapping");
    FrameworkDetection d;
    foreach (entry; node.map)
    {
        switch (entry.key)
        {
            case "package-prefixes":
                d.packagePrefixes = parseStringListYAML(entry.value, "detection.package-prefixes");
                break;
            case "package-substrings":
                d.packageSubstrings = parseStringListYAML(entry.value, "detection.package-substrings");
                break;
            case "markers":
                d.markers = parseStringListYAML(entry.value, "detection.markers");
                break;
            case "priority":
                if (!entry.value.isNull()) d.priority = cast(int) entry.value.asInt();
                break;
            default:
                throw new Exception(format("parse framework manifest: unknown detection field %s", formatGoQuoted(entry.key)));
        }
    }
    return d;
}

private FrameworkAppRootConfig parseAppRootYAML(const YamlValue node) @safe
{
    if (!node.isMapping())
        throw new Exception("app-root must be a mapping");
    FrameworkAppRootConfig a;
    foreach (entry; node.map)
    {
        switch (entry.key)
        {
            case "public-dir":
                if (!entry.value.isNull()) a.publicDir = entry.value.asString();
                break;
            case "markers":
                a.markers = parseStringListYAML(entry.value, "app-root.markers");
                break;
            default:
                throw new Exception(format("parse framework manifest: unknown app-root field %s", formatGoQuoted(entry.key)));
        }
    }
    return a;
}

private FrameworkRuntimeEnvManifest parseRuntimeEnvYAML(const YamlValue node) @safe
{
    if (!node.isMapping())
        throw new Exception("runtime-env must be a mapping");
    FrameworkRuntimeEnvManifest r;
    foreach (entry; node.map)
    {
        switch (entry.key)
        {
            case "requires-database":
                if (!entry.value.isNull()) r.requiresDatabase = entry.value.asBool();
                break;
            case "values":
                r.values = parseFrameworkValueMapYAML(entry.value, "runtime-env.values");
                break;
            default:
                throw new Exception(format("parse framework manifest: unknown runtime-env field %s", formatGoQuoted(entry.key)));
        }
    }
    return r;
}

private FrameworkPostComposerManifest parsePostComposerYAML(const YamlValue node) @safe
{
    if (!node.isMapping())
        throw new Exception("post-composer must be a mapping");
    FrameworkPostComposerManifest p;
    foreach (entry; node.map)
    {
        switch (entry.key)
        {
            case "requires-database":
                if (!entry.value.isNull()) p.requiresDatabase = entry.value.asBool();
                break;
            case "actions":
                if (!entry.value.isNull())
                {
                    if (!entry.value.isSequence())
                        throw new Exception("post-composer.actions must be a sequence");
                    foreach (item; entry.value.seq)
                        p.actions ~= parseActionYAML(item);
                }
                break;
            default:
                throw new Exception(format("parse framework manifest: unknown post-composer field %s", formatGoQuoted(entry.key)));
        }
    }
    return p;
}

private FrameworkActionManifest parseActionYAML(const YamlValue node) @safe
{
    if (!node.isMapping())
        throw new Exception("post-composer action must be a mapping");
    FrameworkActionManifest a;
    foreach (entry; node.map)
    {
        switch (entry.key)
        {
            case "type":
                if (!entry.value.isNull()) a.type = entry.value.asString();
                break;
            case "base":
                if (!entry.value.isNull()) a.base = entry.value.asString();
                break;
            case "path":
                if (!entry.value.isNull()) a.path = entry.value.asString();
                break;
            case "fallback":
                if (!entry.value.isNull()) a.fallback = entry.value.asString();
                break;
            case "fallback-template-file":
                if (!entry.value.isNull()) a.fallbackTemplateFile = entry.value.asString();
                break;
            case "template-file":
                if (!entry.value.isNull()) a.templateFile = entry.value.asString();
                break;
            case "encoding":
                if (!entry.value.isNull()) a.encoding = entry.value.asString();
                break;
            case "values":
                a.values = parseFrameworkValueMapYAML(entry.value, "action.values");
                break;
            case "order":
                a.order = parseStringListYAML(entry.value, "action.order");
                break;
            case "updates":
                if (!entry.value.isNull())
                {
                    if (!entry.value.isSequence())
                        throw new Exception("action.updates must be a sequence");
                    foreach (item; entry.value.seq)
                        a.updates ~= parseArrayUpdateYAML(item);
                }
                break;
            case "insert-before":
                if (!entry.value.isNull()) a.insertBefore = entry.value.asString();
                break;
            case "include":
                if (!entry.value.isNull()) a.include_ = entry.value.asString();
                break;
            case "marker":
                if (!entry.value.isNull()) a.marker = entry.value.asString();
                break;
            default:
                throw new Exception(format("parse framework manifest: unknown action field %s", formatGoQuoted(entry.key)));
        }
    }
    return a;
}

private FrameworkArrayUpdate parseArrayUpdateYAML(const YamlValue node) @safe
{
    if (!node.isMapping())
        throw new Exception("array update must be a mapping");
    FrameworkArrayUpdate u;
    foreach (entry; node.map)
    {
        switch (entry.key)
        {
            case "path":
                u.path = parseStringListYAML(entry.value, "update.path");
                break;
            case "values":
                u.values = parseFrameworkValueMapYAML(entry.value, "update.values");
                break;
            case "order":
                u.order = parseStringListYAML(entry.value, "update.order");
                break;
            default:
                throw new Exception(format("parse framework manifest: unknown array update field %s", formatGoQuoted(entry.key)));
        }
    }
    return u;
}
