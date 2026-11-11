module polka.backend.internal_tools;

import std.file : mkdirRecurse;
import std.format : format;
import std.path : buildPath, dirName;
import std.process : environment;
import std.string : strip;

import polka.backend.aliases;
import polka.backend.schema;
import polka.config :
    environmentFileFromEnvironment,
    environmentFileFromYAML,
    environmentFileToEnvironment,
    EnvironmentFile;
import polka.support.fs : cleanPath, fromSlashPath, userCacheDir;
import polka.support.yaml : parseYAML;
import polka.tools : InstallRequest, internalPHPExtensions;

/// Reserved environment tracking machine-global internal tool versions.
enum string internalEnvironmentName = "_internal";

/// Manifest filename inside the global internal tools directory.
enum string internalConfigFileName = "polka." ~ internalEnvironmentName ~ ".yaml";

/// Returns the machine-global directory for internally provisioned tools (`<dir>/<tool>/<version>`).
string internalToolsDir(string projectDir) @trusted
{
    auto overrideDir = environment.get("POLKA_TOOLS_DIR", "").strip();
    if (overrideDir.length > 0)
        return cleanPath(fromSlashPath(overrideDir));

    auto cacheOverride = environment.get("POLKA_CACHE_DIR", "").strip();
    if (cacheOverride.length > 0)
        return buildPath(cleanPath(fromSlashPath(cacheOverride)), "internal-tools");

    try
    {
        auto cd = userCacheDir();
        if (cd.length > 0)
            return buildPath(cd, "polka", "tools");
    }
    catch (Exception) {}

    return buildPath(projectDir, ".polka-tools");
}

/// Loads the reserved `_internal` environment from `path`; returns an empty `_internal` environment when absent.
Environment readInternalEnvironment(string path) @trusted
{
    string data;
    try
    {
        data = readTextStrict(path);
    }
    catch (ConfigNotExistException)
    {
        Environment empty;
        empty.name = internalEnvironmentName;
        return empty;
    }
    catch (Exception e)
    {
        throw new Exception(format("read internal tools config %s: %s", path, e.msg));
    }

    EnvironmentFile envFile;
    if (data.length > 0)
    {
        try
        {
            validateEnvironmentFileSchema(data, null);
            envFile = environmentFileFromYAML(parseYAML(data));
        }
        catch (Exception e)
        {
            throw new Exception(format("decode internal tools config %s: %s", path, e.msg));
        }
    }

    return environmentFileToEnvironment(internalEnvironmentName, envFile);
}

/// Persists the reserved `_internal` environment configuration atomically via comment-preserving YAML.
void writeInternalEnvironment(string path, const Environment env) @trusted
{
    auto parent = dirName(path);
    try
    {
        mkdirRecurse(parent);
    }
    catch (Exception e)
    {
        throw new Exception("create internal tools directory: " ~ e.msg);
    }

    writeYAML(path, environmentFileFromEnvironment(env));
}

/// Records an internal tool version on `_internal` (bypassing project-only restriction on `pie`).
Environment environmentWithInternalToolVersion(Environment env, string tool, string ver) @trusted
{
    if (tool == toolPIE)
    {
        env.setToolVersion(toolPIE, ver);
        return env;
    }
    return environmentWithInstallRequest(null, env, InstallRequest(tool, ver));
}

/// Builds the synthetic PHP environment used when configuring internal PHP for Composer & PIE.
Environment internalPHPEnvironment(string tool, string ver, string installDir) @trusted
{
    Environment env;
    env.name = internalEnvironmentName;
    env.phpExtensions = internalPHPExtensions(installDir);
    env.setToolVersion(tool, ver);
    return env;
}

unittest
{
    import std.file : exists;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    // TestInternalToolsDirResolution
    {
        auto projectDir = createTempDir("polka-int-tools-dir-");
        scope (exit) rmdirRecurseQuiet(projectDir);

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

        auto toolsOver = buildPath(projectDir, "tools-override");
        auto cacheOver = buildPath(projectDir, "cache-override");
        environment["POLKA_TOOLS_DIR"] = toolsOver;
        environment["POLKA_CACHE_DIR"] = cacheOver;
        assert(internalToolsDir(projectDir) == toolsOver);

        environment["POLKA_TOOLS_DIR"] = "";
        assert(internalToolsDir(projectDir) == buildPath(cacheOver, "internal-tools"));
    }

    // TestInternalEnvironmentNameReserved
    {
        bool threw = false;
        try { validateName(internalEnvironmentName); } catch (Exception) { threw = true; }
        assert(threw);
    }

    // TestReadInternalEnvironmentMissingFile
    {
        auto tmp = createTempDir("polka-int-missing-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto missingPath = buildPath(tmp, internalConfigFileName);
        auto env = readInternalEnvironment(missingPath);
        assert(env.name == internalEnvironmentName && env.toolVersion(toolPIE) == "");
        assert(!exists(missingPath));
    }
}
