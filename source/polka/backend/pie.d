module polka.backend.pie;

import std.algorithm : startsWith;
import std.format : format;
import std.path : buildPath, dirName, extension;
import std.string : icmp, strip, toLower;

import polka.backend.aliases;
import polka.support.fs : currentOS;

/// Builds PIE `install` arguments targeting a specific PHP executable with `--skip-enable-extension`.
string[] pieExtensionInstallArgs(string projectPHPPath, string pkg, string ver) @safe pure
{
    auto spec = pkg;
    auto trimmed = ver.strip();
    if (trimmed.length > 0 && trimmed != "*")
        spec = pkg ~ ":" ~ trimmed;

    return ["install", "--with-php-path=" ~ projectPHPPath, "--skip-enable-extension", spec];
}

/// Builds PIE `uninstall` arguments targeting a specific PHP executable.
string[] pieExtensionUninstallArgs(string projectPHPPath, string pkg) @safe pure
{
    return ["uninstall", "--with-php-path=" ~ projectPHPPath, pkg];
}

/// Prepares `cmdArgs` for executing a PHP CLI target (wrapping `.cmd`/`.bat` shims via `cmd.exe /c` on Windows).
string[] prepareInternalPHPCommandArgs(string target, const(string)[] args) @safe pure
{
    auto trimmed = target.strip();
    if (trimmed.length == 0)
        throw new Exception("php target cannot be empty");

    if (currentOS() == "windows")
    {
        auto ext = extension(trimmed).toLower();
        if (ext == ".cmd" || ext == ".bat")
            return ["cmd.exe", "/c", trimmed] ~ args;
    }

    return [trimmed] ~ args;
}

/// Builds the child environment for internal PHP (`PHPRC` and `OPENSSL_CONF` when present).
string[] internalPHPProcessEnv(const(string)[] base, string phpPath) @trusted
{
    string[] env = base !is null ? base.dup : null;
    auto phpDir = dirName(phpPath);

    auto iniPath = buildPath(phpDir, "php.ini");
    if (regularFileExistsAt(iniPath))
        env = setProcessEnvValue(env, "PHPRC", iniPath);

    auto opensslCandidates = [
        buildPath(phpDir, "extras", "ssl", "openssl.cnf"),
        buildPath(dirName(phpDir), "extras", "ssl", "openssl.cnf"),
    ];
    foreach (candidate; opensslCandidates)
    {
        if (regularFileExistsAt(candidate))
        {
            env = setProcessEnvValue(env, "OPENSSL_CONF", candidate);
            break;
        }
    }

    return env;
}

unittest
{
    import std.algorithm : canFind;
    import std.file : mkdirRecurse, write;
    import polka.support.fs : createTempDir, octal, rmdirRecurseQuiet;

    // TestPIEExtensionInstallArgs
    assert(pieExtensionInstallArgs("/path/to/php", "foo/bar", "")
        == ["install", "--with-php-path=/path/to/php", "--skip-enable-extension", "foo/bar"]);
    assert(pieExtensionInstallArgs("/path/to/php", "foo/bar", "  \t  ")
        == ["install", "--with-php-path=/path/to/php", "--skip-enable-extension", "foo/bar"]);
    assert(pieExtensionInstallArgs("/path/to/php", "foo/bar", "*")
        == ["install", "--with-php-path=/path/to/php", "--skip-enable-extension", "foo/bar"]);
    assert(pieExtensionInstallArgs("/path/to/php", "foo/bar", "^1.2")
        == ["install", "--with-php-path=/path/to/php", "--skip-enable-extension", "foo/bar:^1.2"]);

    // TestPIEExtensionUninstallArgs
    assert(pieExtensionUninstallArgs("/path/to/php", "foo/bar")
        == ["uninstall", "--with-php-path=/path/to/php", "foo/bar"]);

    // TestPrepareInternalPHPCommandArgs
    bool emptyThrew = false;
    try { prepareInternalPHPCommandArgs("  ", ["arg1"]); } catch (Exception) { emptyThrew = true; }
    assert(emptyThrew);
    assert(prepareInternalPHPCommandArgs("php", ["arg1", "arg2"]) == ["php", "arg1", "arg2"]);

    // TestSetProcessEnvValue
    {
        auto env = ["VAR1=val1", "VAR2=val2"];
        assert(setProcessEnvValue(env, "VAR3", "val3").canFind("VAR3=val3"));
        auto rep = setProcessEnvValue(env, "VAR2", "newval2");
        assert(rep.canFind("VAR2=newval2") && !rep.canFind("VAR2=val2"));
        auto bad = ["VAR1=val1", "BADFORMAT"];
        assert(setProcessEnvValue(bad, "VAR2", "val2").canFind("VAR2=val2"));
    }

    // TestInternalPHPProcessEnv
    {
        auto dir = createTempDir("polka-pie-env-");
        scope (exit) rmdirRecurseQuiet(dir);
        auto phpPath = buildPath(dir, "php");

        auto baseEnv = ["EXISTING=val"];
        assert(internalPHPProcessEnv(baseEnv, phpPath) == baseEnv);

        auto iniPath = buildPath(dir, "php.ini");
        write(iniPath, "");
        assert(internalPHPProcessEnv(baseEnv, phpPath).canFind("PHPRC=" ~ iniPath));

        auto sslDir = buildPath(dir, "extras", "ssl");
        mkdirRecurse(sslDir);
        auto sslPath = buildPath(sslDir, "openssl.cnf");
        write(sslPath, "");
        auto env3 = internalPHPProcessEnv(baseEnv, phpPath);
        assert(env3.canFind("OPENSSL_CONF=" ~ sslPath));
    }
}
