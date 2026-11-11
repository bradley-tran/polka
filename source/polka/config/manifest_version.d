module polka.config.manifest_version;

import std.algorithm : canFind, startsWith;
import std.format : format;
import polka.support.yaml : emitYAML, parseYAML, YamlException, YamlKind, YamlValue;

/// CurrentManifestVersion is the only supported project manifest schema.
enum string CurrentManifestVersion = "0.2";

/// Decodes a project manifest version node while preserving exact scalar spelling (e.g. "0.2" vs "0.20").
string decodeManifestVersion(const YamlValue node) @safe
{
    if (node.kind == YamlKind.string_)
        return node.strVal;
    if (node.kind == YamlKind.int_ || node.kind == YamlKind.float_)
        return node.rawScalar;
    throw new YamlException("project manifest version must be a string or numeric scalar");
}

/// Marshals the canonical plain scalar `0.2`, rejecting any other version.
YamlValue marshalManifestVersion(string v) @safe
{
    if (v != CurrentManifestVersion)
        throw new Exception(format(`unsupported project manifest version "%s"`, v));
    return YamlValue.fromRawPlain(v);
}

/// Checks the header before any environment decoding so obsolete schemas cannot be coerced.
void validateProjectManifestVersion(const(ubyte)[] data, string path) @safe
{
    auto text = () @trusted { return (cast(const(char)[]) data).idup; }();
    validateProjectManifestVersion(text, path);
}

/// Checks the header before any environment decoding so obsolete schemas cannot be coerced.
void validateProjectManifestVersion(string data, string path) @safe
{
    YamlValue root;
    try
    {
        root = parseYAML(data);
    }
    catch (Exception e)
    {
        throw new Exception(format(
            "decode project manifest %s: %s; delete %s and run polka init (or polka init <framework>) to regenerate it",
            path, e.msg, path));
    }

    string ver = "";
    if (!root.isNull())
    {
        if (!root.isMapping())
        {
            throw new Exception(format(
                "decode project manifest %s: expected YAML mapping; delete %s and run polka init (or polka init <framework>) to regenerate it",
                path, path));
        }
        if (root.hasKey("version"))
        {
            auto vNode = root.get("version");
            try
            {
                ver = decodeManifestVersion(vNode);
            }
            catch (Exception e)
            {
                throw new Exception(format(
                    "decode project manifest %s: %s; delete %s and run polka init (or polka init <framework>) to regenerate it",
                    path, e.msg, path));
            }
        }
    }

    if (ver != CurrentManifestVersion)
    {
        throw new Exception(format(
            `unsupported project manifest version "%s" in %s; expected %s; delete %s and run polka init (or polka init <framework>) to regenerate it`,
            ver, path, CurrentManifestVersion, path));
    }
}

unittest
{
    import std.exception : assertThrown;

    // TestManifestVersionRoundTrip
    foreach (ver; ["0.2", `"0.2"`, `'0.2'`])
    {
        string data = "version: " ~ ver ~ "\nroot: .polka\n";
        validateProjectManifestVersion(data, "polka.yaml");
        auto root = parseYAML(data);
        auto parsedVer = decodeManifestVersion(root.get("version"));
        assert(parsedVer == CurrentManifestVersion);
        root.set("version", marshalManifestVersion(parsedVer));
        auto encoded = emitYAML(root);
        assert(encoded.startsWith("version: 0.2\n"));
    }

    // TestManifestVersionRejectsObsoleteSchemas
    foreach (data; [
        "", "root: .polka", "version: 0.1", `version: "0.1"`, "version: 1",
        "version: 0.20", "version: 0.3", "version: null", "version: true",
        "version: [0.2]", "version: {major: 0, minor: 2}"
    ])
    {
        bool threw = false;
        try
        {
            validateProjectManifestVersion(data, "project/polka.yaml");
        }
        catch (Exception e)
        {
            threw = true;
            assert(e.msg.canFind("project/polka.yaml"));
        }
        assert(threw, "expected error for: " ~ data);
    }

    assertThrown!Exception(marshalManifestVersion("0.1"));
}
