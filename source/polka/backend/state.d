module polka.backend.state;

import std.algorithm : canFind, sort;
import std.file : exists, mkdirRecurse, readText;
import std.format : format;
import std.json : JSONType, JSONValue, parseJSON;
import std.path : buildPath;

import polka.support.fs : octal, writeTextAtomic;

/// Project-local manifest filename recording successfully installed tools inside `.polka/envs`.
enum string installStateFileName = "installed.json";

/// Current schema version of `installed.json`.
enum int installStateVersion = 1;

/// Tracks which tool versions have been extracted and post-installed into `.polka/envs`,
/// enabling unchanged tools to be skipped on subsequent `polka install` runs.
struct InstallState
{
    int version_ = installStateVersion;
    string[][string] tools;

    /// Reports whether `(tool, ver)` is recorded as installed.
    bool has(string tool, string ver) const @safe pure
    {
        if (auto versions = tool in tools)
        {
            foreach (installed; *versions)
            {
                if (installed == ver)
                    return true;
            }
        }
        return false;
    }

    /// Marks `(tool, ver)` as installed, deduplicating entries and sorting versions.
    void record(string tool, string ver) @safe
    {
        if (has(tool, ver))
            return;
        auto list = (tool in tools) !is null ? tools[tool].dup : null;
        list ~= ver;
        list.sort();
        tools[tool] = list;
    }
}

/// Returns `<envsDir>/installed.json`.
string installStateFilePath(string envsDir) @safe pure
{
    return buildPath(envsDir, installStateFileName);
}

/// Reads `<envsDir>/installed.json`, returning an empty state when missing or corrupt.
InstallState readInstallStateFrom(string envsDir) @trusted
{
    auto path = installStateFilePath(envsDir);
    if (!exists(path))
        return InstallState(installStateVersion);

    try
    {
        auto root = parseJSON(readText(path));
        if (root.type != JSONType.object)
            return InstallState(installStateVersion);

        auto verNode = "version" in root;
        if (verNode is null || (verNode.type != JSONType.integer && verNode.type != JSONType.uinteger))
            return InstallState(installStateVersion);
        if (verNode.integer != installStateVersion)
            return InstallState(installStateVersion);

        InstallState state;
        state.version_ = installStateVersion;
        if (auto toolsNode = "tools" in root)
        {
            if (toolsNode.type == JSONType.object)
            {
                foreach (tool, arr; toolsNode.object)
                {
                    if (arr.type != JSONType.array)
                        continue;
                    string[] versions;
                    foreach (elem; arr.array)
                    {
                        if (elem.type == JSONType.string)
                            versions ~= elem.str;
                    }
                    if (versions.length > 0)
                        state.tools[tool] = versions;
                }
            }
        }
        return state;
    }
    catch (Exception)
    {
        return InstallState(installStateVersion);
    }
}

/// Atomically persists `<envsDir>/installed.json`.
void writeInstallStateTo(string envsDir, const InstallState state) @trusted
{
    if (!exists(envsDir))
        mkdirRecurse(envsDir);

    JSONValue root = JSONValue.emptyObject;
    root["version"] = state.version_;
    if (state.tools.length > 0)
    {
        JSONValue toolsObj = JSONValue.emptyObject;
        auto keys = state.tools.keys;
        keys.sort();
        foreach (k; keys)
        {
            JSONValue arr = JSONValue.emptyArray;
            foreach (v; state.tools[k])
                arr.array ~= JSONValue(v);
            toolsObj[k] = arr;
        }
        root["tools"] = toolsObj;
    }

    writeTextAtomic(installStateFilePath(envsDir), root.toPrettyString() ~ "\n", octal!644);
}

unittest
{
    import std.file : write;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    // TestReadInstallStateMissingFileReturnsEmptyState
    {
        auto root = createTempDir("polka-state-miss-");
        scope (exit) rmdirRecurseQuiet(root);
        auto envsDir = buildPath(root, "envs");
        auto st = readInstallStateFrom(envsDir);
        assert(st.version_ == installStateVersion);
        assert(st.tools.length == 0);
    }

    // TestInstallStateWriteReadRoundTrip
    {
        auto root = createTempDir("polka-state-rt-");
        scope (exit) rmdirRecurseQuiet(root);
        auto envsDir = buildPath(root, "envs");

        InstallState st;
        st.record("php", "8.4");
        st.record("composer", "2.8");
        writeInstallStateTo(envsDir, st);

        auto loaded = readInstallStateFrom(envsDir);
        assert(loaded.has("php", "8.4") && loaded.has("composer", "2.8"));
        assert(!loaded.has("php", "8.3"));
    }

    // TestReadInstallStateToleratesCorruptFile
    {
        auto root = createTempDir("polka-state-corrupt-");
        scope (exit) rmdirRecurseQuiet(root);
        auto envsDir = buildPath(root, "envs");
        mkdirRecurse(envsDir);
        write(installStateFilePath(envsDir), "{not json");

        auto loaded = readInstallStateFrom(envsDir);
        assert(loaded.tools.length == 0);
    }

    // TestInstallStateRecordDeduplicates
    {
        InstallState st;
        st.record("php", "8.4");
        st.record("php", "8.4");
        st.record("php", "8.3");
        assert(st.tools["php"] == ["8.3", "8.4"]);
    }
}
