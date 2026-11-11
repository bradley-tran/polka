module polka.tools.post_install;

import std.algorithm : sort;
import std.file : exists, isDir, mkdirRecurse, write;
import std.format : format;
import std.path : baseName, buildPath, dirName;
import std.string : endsWith, strip, toLower;

import polka.support.fs : currentOS, getFileMode, isWindowsOS, octal, rmdirRecurseQuiet, setFileMode;
import polka.tools.manifest_rules;
import polka.tools.types;

/// Determines the root installation directory for a `postInstall` hook, preferring
/// `<envsDir>/<tool>/<version>` when present and falling back to `dirName(targetPath)`.
string resolvePostInstallDir(string tool, const InstallContext ctx) @trusted
{
    auto envsDir = ctx.envsDir.strip();
    auto ver = ctx.result.version_.strip();
    auto targetPath = ctx.result.targetPath.strip();

    if (envsDir.length > 0 && ver.length > 0)
    {
        auto candidate = buildPath(envsDir, tool, ver);
        if (targetPath.length == 0)
            return candidate;
        if (exists(candidate) && isDir(candidate))
            return candidate;
    }
    if (targetPath.length > 0)
        return dirName(targetPath);

    return "";
}

/// Generates declarative wrapper scripts next to their first existing target binary.
string[] writeManifestWrappers(
    string installDir,
    const(ManifestWrapper[string]) wrappers,
    string goos,
    string goarch
) @trusted
{
    if (installDir.strip().length == 0 || wrappers.length == 0)
        return null;

    auto names = wrappers.keys;
    names.sort();

    string[] written;
    foreach (name; names)
    {
        auto spec = wrappers[name];
        auto candidates = manifestCandidatePaths(installDir, spec.target, goos, goarch);
        if (candidates.length == 0)
            continue;

        string targetPath;
        if (!findExistingManifestFile(candidates, targetPath))
            continue;

        auto targetDir = dirName(targetPath);
        if (spec.skipIfExists && wrapperCommandExists(installDir, targetDir, name, goos))
            continue;

        auto wrapperPath = buildPath(targetDir, name);
        if (goos.strip() == "windows" && !wrapperPath.toLower().endsWith(".cmd"))
            wrapperPath ~= ".cmd";

        auto wrapperDir = dirName(wrapperPath);
        if (!exists(wrapperDir))
            mkdirRecurse(wrapperDir);

        auto contents = wrapperScript(goos, baseName(targetPath), spec.args);
        write(wrapperPath, contents);
        setFileMode(wrapperPath, octal!755);
        written ~= wrapperPath;
    }

    return written;
}

private bool findExistingManifestFile(const(string)[] candidates, out string found) @trusted
{
    foreach (candidate; candidates)
    {
        if (!exists(candidate))
            continue;
        if (isDir(candidate))
            continue;
        found = candidate;
        return true;
    }
    found = "";
    return false;
}

private bool wrapperCommandExists(string installDir, string targetDir, string command, string goos) @trusted
{
    auto dirs = [targetDir, installDir, buildPath(installDir, "bin")];
    auto names = goos.strip() == "windows"
        ? [command ~ ".cmd", command ~ ".exe", command ~ ".bat", command]
        : [command];

    bool[string] seen;
    foreach (dir; dirs)
    {
        if (dir.strip().length == 0)
            continue;
        foreach (name; names)
        {
            auto candidate = buildPath(dir, name);
            if (candidate in seen)
                continue;
            seen[candidate] = true;
            if (exists(candidate) && !isDir(candidate))
                return true;
        }
    }
    return false;
}

/// Ensures installed target binaries, dispatch candidates, and wrappers have execute permissions on POSIX.
void ensureInstalledExecutables(const(string)[] paths) @trusted
{
    if (isWindowsOS())
        return;

    bool[string] seen;
    foreach (rawPath; paths)
    {
        auto path = rawPath.strip();
        if (path.length == 0 || (path in seen))
            continue;
        seen[path] = true;

        if (!exists(path) || isDir(path))
            continue;

        auto mode = getFileMode(path);
        setFileMode(path, mode | octal!755);
    }
}

unittest
{
    import std.algorithm : canFind;
    import std.file : readText, rmdirRecurse;
    import polka.support.fs : createTempDir;

    // TestWriteManifestWrappersPlacesScriptNextToResolvedTarget
    {
        auto installDir = createTempDir("polka-wrap-win-");
        scope (exit) rmdirRecurseQuiet(installDir);

        auto binDir = buildPath(installDir, "bin");
        mkdirRecurse(binDir);
        write(buildPath(binDir, "corepack.cmd"), "@echo off\r\n");

        ManifestWrapper w;
        w.target = ["windows-amd64": ["bin/corepack.cmd", "bin/corepack"], "linux-amd64": ["bin/corepack"]];
        w.args = ["yarn"];
        w.skipIfExists = true;
        auto written = writeManifestWrappers(installDir, ["yarn": w], "windows", "amd64");
        auto wantPath = buildPath(binDir, "yarn.cmd");
        assert(written == [wantPath]);
        assert(readText(wantPath).canFind(`call "%~dp0corepack.cmd" yarn %*`));
    }

    // TestWriteManifestWrappersSkipsWhenTargetOrExistingCommandPresent
    {
        ManifestWrapper w;
        w.target = ["linux-amd64": ["bin/corepack", "corepack"]];
        w.args = ["yarn"];
        w.skipIfExists = true;
        ManifestWrapper[string] wrappers = ["yarn": w];

        auto emptyDir = createTempDir("polka-wrap-empty-");
        scope (exit) rmdirRecurseQuiet(emptyDir);
        assert(writeManifestWrappers(emptyDir, wrappers, "linux", "amd64").length == 0);

        auto installDir = createTempDir("polka-wrap-existing-");
        scope (exit) rmdirRecurseQuiet(installDir);
        write(buildPath(installDir, "corepack"), "bundled");
        write(buildPath(installDir, "yarn"), "bundled");

        assert(writeManifestWrappers(installDir, wrappers, "linux", "amd64").length == 0);
        assert(readText(buildPath(installDir, "yarn")) == "bundled");
    }

    // TestEnsureInstalledExecutablesMarksFilesExecutable
    version (Posix)
    {
        auto dir = createTempDir("polka-chmod-");
        scope (exit) rmdirRecurseQuiet(dir);

        auto binary = buildPath(dir, "tool");
        write(binary, "binary");
        setFileMode(binary, octal!644);
        ensureInstalledExecutables([binary, buildPath(dir, "missing")]);
        assert((getFileMode(binary) & octal!111) != 0);
    }
}
