module polka.plugins.preset;

import std.algorithm : canFind;
import std.format : format;
import std.path : baseName, buildNormalizedPath;
import std.regex : regex, replaceAll;
import std.string : replace, strip, stripRight, toLower;

import polka.config;
import polka.plugins.ids;
import polka.support.yaml;

/// Compile-time embedded preset manifests and scaffold templates matching Go `embed.FS`.
private immutable string[string] builtinProjectPresetAssetMap = [
    "manifests/presets/php-extension.yaml":                         import("php-extension.yaml"),
    "manifests/presets/templates/php-extension.composer.json":      import("php-extension.composer.json"),
];

/// Scaffold entry declaration within a project preset manifest.
struct ProjectPresetScaffoldManifest
{
    string path;
    string templateFile;
}

/// Decoded project preset manifest (`plugins/manifests/presets/*.yaml`).
struct ProjectPresetManifest
{
    string id;
    string description;
    EnvironmentFile defaults;
    ProjectPresetScaffoldManifest[] scaffold;
}

private struct ProjectPresetScaffold
{
    string path;
    string templateText;
}

/// Built-in `ProjectPreset` implementation backed by embedded templates and defaults.
final class BuiltinProjectPreset : ProjectPreset
{
    private string idValue;
    private string descriptionValue;
    private Environment defaultsValue;
    private ProjectPresetScaffold[] scaffoldList;

    this(string id, string description = "", Environment defaults = Environment.init, ProjectPresetScaffold[] scaffold = null) @safe
    {
        this.idValue = id;
        this.descriptionValue = description;
        this.defaultsValue = normalizeEnvironment("", defaults);
        this.scaffoldList = scaffold;
    }

    string id() const @safe pure
    {
        return idValue;
    }

    string description() const @safe pure
    {
        return descriptionValue;
    }

    Environment defaults() const @safe pure
    {
        return normalizeEnvironment("", defaultsValue);
    }

    /// Renders embedded templates using literal placeholder substitution.
    ScaffoldFile[] scaffoldFiles(const ScaffoldContext ctx) const @safe
    {
        string extensionName, packageName;
        derivePresetNames(ctx.projectDir, extensionName, packageName);
        auto explicitPkg = ctx.packageName.strip().toLower();
        if (explicitPkg.length > 0)
            packageName = explicitPkg;

        auto phpConstraint = "*";
        auto ver = ctx.phpVersion.strip();
        if (ver.length > 0)
            phpConstraint = "^" ~ ver;

        ScaffoldFile[] files;
        files.reserve(scaffoldList.length);
        foreach (item; scaffoldList)
        {
            auto content = item.templateText
                .replace("{{package}}", packageName)
                .replace("{{extension}}", extensionName)
                .replace("{{php-constraint}}", phpConstraint);
            files ~= ScaffoldFile(item.path, (cast(const(ubyte)[]) content).dup);
        }
        return files;
    }
}

/// Returns built-in project presets in registration order.
ProjectPreset[] defaultProjectPresets() @safe
{
    return [newManifestProjectPreset(PHPExtension)];
}

/// Parses and constructs a built-in project preset from embedded manifests.
ProjectPreset newManifestProjectPreset(string name) @safe
{
    auto manifest = loadBuiltinProjectPresetManifest(name);
    return projectPresetManifestToPreset(manifest);
}

/// Reads and parses a built-in project preset manifest YAML document.
ProjectPresetManifest loadBuiltinProjectPresetManifest(string name) @safe
{
    auto manifestPath = "manifests/presets/" ~ name.strip() ~ ".yaml";
    auto p = manifestPath in builtinProjectPresetAssetMap;
    if (p is null)
        throw new Exception(format("read builtin project preset manifest %s: file does not exist", manifestPath));
    return parseProjectPresetManifest(*p);
}

/// Parses and validates a project preset manifest YAML payload.
ProjectPresetManifest parseProjectPresetManifest(string data) @safe
{
    YamlValue root;
    try
    {
        root = parseYAML(data);
    }
    catch (Exception e)
    {
        throw new Exception(format("parse project preset manifest: %s", e.msg));
    }

    ProjectPresetManifest manifest;
    if (!root.isNull())
    {
        if (!root.isMapping())
            throw new Exception("parse project preset manifest: expected YAML mapping");

        foreach (entry; root.map)
        {
            switch (entry.key)
            {
                case "id":
                    if (!entry.value.isNull()) manifest.id = entry.value.asString();
                    break;
                case "description":
                    if (!entry.value.isNull()) manifest.description = entry.value.asString();
                    break;
                case "defaults":
                    if (!entry.value.isNull()) manifest.defaults = environmentFileFromYAML(entry.value, false);
                    break;
                case "scaffold":
                    if (!entry.value.isNull())
                    {
                        if (!entry.value.isSequence())
                            throw new Exception("parse project preset manifest: scaffold must be a sequence");
                        foreach (item; entry.value.seq)
                        {
                            if (!item.isMapping())
                                throw new Exception("parse project preset manifest: scaffold item must be a mapping");
                            ProjectPresetScaffoldManifest sm;
                            foreach (se; item.map)
                            {
                                switch (se.key)
                                {
                                    case "path":
                                        if (!se.value.isNull()) sm.path = se.value.asString();
                                        break;
                                    case "template-file":
                                        if (!se.value.isNull()) sm.templateFile = se.value.asString();
                                        break;
                                    default:
                                        break;
                                }
                            }
                            manifest.scaffold ~= sm;
                        }
                    }
                    break;
                default:
                    break;
            }
        }
    }

    validateProjectPresetManifest(manifest);
    manifest.id = manifest.id.strip().toLower();
    manifest.description = manifest.description.strip();
    foreach (ref item; manifest.scaffold)
    {
        item.path = item.path.strip();
        item.templateFile = item.templateFile.strip();
    }
    return manifest;
}

/// Validates project preset invariants including non-framework constraint and scaffold containment.
void validateProjectPresetManifest(const ProjectPresetManifest m) @safe
{
    auto id = m.id.strip().toLower();
    if (id.length == 0)
        throw new Exception("project preset manifest id cannot be empty");
    if (!isValidPluginID(id))
        throw new Exception(format("invalid project preset manifest id %s: use letters, numbers, dots, dashes, or underscores",
            formatGoQuoted(m.id)));
    if (m.defaults.framework.strip().length > 0)
        throw new Exception(format("project preset manifest %s defaults.framework must be empty", formatGoQuoted(id)));

    try
    {
        validateWorkersConfig(m.defaults.workers);
    }
    catch (Exception e)
    {
        throw new Exception(format("project preset manifest %s: %s", formatGoQuoted(id), e.msg));
    }

    foreach (scaffold; m.scaffold)
    {
        try
        {
            validateProjectPresetScaffoldPath(scaffold.path);
        }
        catch (Exception e)
        {
            throw new Exception(format("project preset manifest %s: %s", formatGoQuoted(id), e.msg));
        }

        auto templateFile = scaffold.templateFile.strip();
        if (templateFile.length == 0)
            throw new Exception(format("project preset manifest %s scaffold template-file cannot be empty", formatGoQuoted(id)));
        if (templateFile.canFind('/') || templateFile.canFind('\\') || templateFile == "." || templateFile == "..")
        {
            throw new Exception(format("project preset manifest %s scaffold template-file %s must be a filename",
                formatGoQuoted(id), formatGoQuoted(scaffold.templateFile)));
        }
        auto templatePath = "manifests/presets/templates/" ~ templateFile;
        if ((templatePath in builtinProjectPresetAssetMap) is null)
        {
            throw new Exception(format("project preset manifest %s read scaffold template %s: file does not exist",
                formatGoQuoted(id), templatePath));
        }
    }
}

/// Converts a validated `ProjectPresetManifest` to a `ProjectPreset`.
ProjectPreset projectPresetManifestToPreset(const ProjectPresetManifest m) @safe
{
    auto defaults = normalizeEnvironment("", environmentFileToEnvironment("", m.defaults));
    ProjectPresetScaffold[] scaffold;
    scaffold.reserve(m.scaffold.length);
    foreach (item; m.scaffold)
    {
        auto templatePath = "manifests/presets/templates/" ~ item.templateFile;
        auto p = templatePath in builtinProjectPresetAssetMap;
        if (p is null)
            throw new Exception(format("read project preset scaffold template %s: file does not exist", templatePath));
        scaffold ~= ProjectPresetScaffold(item.path, *p);
    }
    return new BuiltinProjectPreset(m.id, m.description, defaults, scaffold);
}

/// Turns a project directory basename into valid Composer package and PHP extension identifiers.
void derivePresetNames(string projectDir, out string extensionName, out string packageName) @safe
{
    auto base = baseName(buildNormalizedPath(projectDir)).strip().toLower();

    auto ext = replaceAll(base, regex(`[^a-z0-9_]+`), "_");
    ext = replaceAll(ext, regex(`_+`), "_");
    ext = stripChars(ext, '_');
    if (ext.length == 0 || (ext[0] >= '0' && ext[0] <= '9'))
        ext = "my_extension";

    auto pkgSuffix = replaceAll(base, regex(`[^a-z0-9]+`), "-");
    pkgSuffix = replaceAll(pkgSuffix, regex(`-+`), "-");
    pkgSuffix = stripChars(pkgSuffix, '-');
    if (pkgSuffix.length == 0)
        pkgSuffix = "my-extension";

    extensionName = ext;
    packageName = "vendor/" ~ pkgSuffix;
}

private string stripChars(string s, char ch) @safe pure nothrow @nogc
{
    size_t start = 0;
    size_t end = s.length;
    while (start < end && s[start] == ch)
        start++;
    while (end > start && s[end - 1] == ch)
        end--;
    return s[start .. end];
}

unittest
{
    import std.json : JSONValue, parseJSON;

    // TestBuiltinProjectPresetManifestLoads
    {
        auto manifest = loadBuiltinProjectPresetManifest(PHPExtension);
        assert(manifest.id == PHPExtension);
        assert(manifest.defaults.framework == "");

        auto preset = newManifestProjectPreset(PHPExtension);
        auto defaults = preset.defaults();
        assert(defaults.toolVersion("php") == "8.4");
        assert(defaults.toolVersion("composer") == "2.8");
        assert(defaults.phpBuildTools());
        assert(defaults.docroot == "" && !defaults.https && defaults.server is null && defaults.database is null);
        assert(defaults.memoryLimit == "-1");
    }

    // TestProjectPresetScaffoldRendering
    {
        auto preset = newManifestProjectPreset(PHPExtension);
        struct Case { string projectDir, packageName, phpVersion, wantPackage, wantExt, wantPHP; }
        foreach (tc; [
            Case("my-ext", "", "8.4", "vendor/my-ext", "my_ext", "^8.4"),
            Case("PHP-Foo", "", "", "vendor/php-foo", "php_foo", "*"),
            Case("9lives", "", "", "vendor/9lives", "my_extension", "*"),
            Case("-", "", "", "vendor/my-extension", "my_extension", "*"),
            Case("ignored", "acme/native-ext", "", "acme/native-ext", "ignored", "*"),
        ])
        {
            auto files = preset.scaffoldFiles(ScaffoldContext(tc.projectDir, tc.packageName, tc.phpVersion));
            assert(files.length == 1 && files[0].path == "composer.json");
            auto doc = parseJSON(cast(const(char)[]) files[0].content);
            assert(doc["name"].str == tc.wantPackage);
            assert(doc["php-ext"]["extension-name"].str == tc.wantExt);
            assert(doc["require"]["php"].str == tc.wantPHP);
            validatePIEExtensionPackage(doc["name"].str);
        }
    }

    // TestParseProjectPresetManifestRejectsInvalidDefinitions
    {
        struct RejectCase { string name, yaml, wantErr; }
        foreach (tc; [
            RejectCase("missing id", "defaults: {}", "id cannot be empty"),
            RejectCase("invalid id", "id: bad\\nvalue", "invalid project preset manifest id"),
            RejectCase("claims framework", "id: demo\ndefaults:\n  framework: laravel", "defaults.framework must be empty"),
            RejectCase("absolute path", "id: demo\nscaffold:\n  - path: /tmp/file\n    template-file: php-extension.composer.json", "must be slash-separated and relative"),
            RejectCase("dot segment", "id: demo\nscaffold:\n  - path: nested/../file\n    template-file: php-extension.composer.json", "must not contain"),
            RejectCase("reserved config", "id: demo\nscaffold:\n  - path: polka.dev.yaml\n    template-file: php-extension.composer.json", "reserved for init"),
            RejectCase("reserved root", "id: demo\nscaffold:\n  - path: .polka/data\n    template-file: php-extension.composer.json", "reserved for init"),
            RejectCase("missing template", "id: demo\nscaffold:\n  - path: file\n    template-file: absent", "read scaffold template"),
        ])
        {
            bool matched = false;
            try
            {
                parseProjectPresetManifest(tc.yaml);
            }
            catch (Exception e)
            {
                matched = e.msg.canFind(tc.wantErr);
            }
            assert(matched, "case " ~ tc.name ~ " did not return expected error: " ~ tc.wantErr);
        }
    }

    // TestDefaultProjectPresetsHaveStableIDs
    {
        auto presets = defaultProjectPresets();
        assert(presets.length == 1 && presets[0].id() == PHPExtension);
    }
}
