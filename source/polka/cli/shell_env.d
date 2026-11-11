module polka.cli.shell_env;

// Interactive and exec shell session context construction:
// discovers vendor/bin (and builds temporary Windows PHP wrappers for extensionless
// Composer proxies), overlays MSVC toolchain variables, applies runtime `.env` /
// framework overlays, and builds ordered PATH entries.

import std.algorithm : sort, startsWith;
import std.file : dirEntries, SpanMode, write;
import std.format : format;
import std.path : absolutePath, baseName, buildPath, dirName, extension;
import std.string : join, strip;

import polka.backend : MSVCToolchain, Store, TextWriter, writeOutf;
import polka.cli.env_runtime : resolveRuntimeEnvironment;
import polka.cli.types :
    directoryExists,
    envKeysEqual,
    escapeWindowsShimValue,
    fileExists,
    isMissingFileError,
    isPHPShebang,
    isPOSIXShellShebang,
    joinPathList,
    lookupEnvValue,
    readScriptFirstLine,
    recreateDirectory,
    replaceEnvValue,
    samePath;
import polka.support.fs : cleanPath, octal, setFileMode;

/// Shell runtime constants shared by `polka sh`, `polka exec`, and shell environment helpers.
enum string defaultWindowsShell = "powershell.exe";
enum string vendorDirectoryName = "vendor";
enum string binDirectoryName = "bin";
enum string posixInteractiveArg = "-i";
enum string polkaPromptRootEnv = "POLKA_PROMPT_ROOT";
enum string polkaPromptEnvEnv = "POLKA_PROMPT_ENVIRONMENT";
enum string shellRuntimeDirName = "run";
enum string shellSupportDirName = "shell";
enum string vendorShimDirName = "vendor-bin";

/// Resolved PATH prefixes and optional Windows extension-SDK toolchain for a shell session.
struct ShellSessionContext
{
    string environmentName;
    string rootDir;
    string promptRoot;
    string vendorProjectDir;
    string vendorBinDir;
    string vendorShimDir;
    string[] pathEntries;
    /// Captured Visual Studio developer environment when `extension-sdk` is enabled.
    MSVCToolchain* toolchain;
    /// Directory of the configured PHP runtime when `extension-sdk` is enabled on Windows.
    string phpPrefix;
}

/// Metadata for a single discovered extensionless PHP wrapper target inside `vendor/bin`.
struct VendorBinPHPTarget
{
    string commandName;
    string targetPath;
}

/// Resolves the target OS interactive shell binary and launch flags.
void resolveInteractiveShell(string goos, string shellEnv, out string target, out string[] args) @safe pure
{
    if (goos == "windows")
    {
        target = defaultWindowsShell;
        args = windowsInteractiveShellArgs();
        return;
    }

    auto trimmedShell = shellEnv.strip();
    if (trimmedShell.length > 0)
    {
        target = trimmedShell;
        args = [posixInteractiveArg];
        return;
    }

    target = "/bin/sh";
    args = [posixInteractiveArg];
}

/// Returns the active environment name for shell prompts/banners (`"none"` if unset).
string currentShellEnvironmentName(ref Store store)
{
    auto current = store.current();
    if (current is null || current.name.strip().length == 0)
        return "none";
    return current.name;
}

/// Prints the startup banner before launching `polka sh`.
void writeShellBanner(TextWriter stdout, string environmentName)
{
    if (stdout is null)
        return;
    writeOutf(stdout, "Opened Polka shell for environment %s\n", environmentName);
}

/// Returns PowerShell arguments installing Polka's relative-path prompt function.
string[] windowsInteractiveShellArgs() @safe pure
{
    return ["-NoLogo", "-NoExit", "-Command", windowsPromptCommand()];
}

/// Renders the PowerShell prompt snippet reflecting `POLKA_PROMPT_ROOT` and `POLKA_PROMPT_ENVIRONMENT`.
string windowsPromptCommand() @safe pure
{
    return [
        "& {",
        "function global:Get-PolkaPromptPath {",
        "param([string]$root, [string]$current)",
        "$normalizedRoot = [System.IO.Path]::GetFullPath($root).TrimEnd('\\', '/')",
        "$normalizedCurrent = [System.IO.Path]::GetFullPath($current).TrimEnd('\\', '/')",
        "if ($normalizedCurrent.Equals($normalizedRoot, [System.StringComparison]::OrdinalIgnoreCase)) { return '.' }",
        "$rootPrefix = $normalizedRoot + [System.IO.Path]::DirectorySeparatorChar",
        "if ($normalizedCurrent.StartsWith($rootPrefix, [System.StringComparison]::OrdinalIgnoreCase)) { return $normalizedCurrent.Substring($rootPrefix.Length).Replace('\\', '/') }",
        "if ([System.IO.Path]::GetPathRoot($normalizedCurrent).Equals([System.IO.Path]::GetPathRoot($normalizedRoot), [System.StringComparison]::OrdinalIgnoreCase)) {",
        "$rootUri = New-Object System.Uri(($normalizedRoot + [System.IO.Path]::DirectorySeparatorChar))",
        "$currentUri = New-Object System.Uri(($normalizedCurrent + [System.IO.Path]::DirectorySeparatorChar))",
        "$relative = [System.Uri]::UnescapeDataString($rootUri.MakeRelativeUri($currentUri).ToString()).TrimEnd('/')",
        "if (-not [string]::IsNullOrWhiteSpace($relative)) { return $relative }",
        "}",
        "return $normalizedCurrent.Replace('\\', '/')",
        "}",
        "function global:prompt {",
        "$root = $env:" ~ polkaPromptRootEnv,
        "$name = $env:" ~ polkaPromptEnvEnv,
        "if ([string]::IsNullOrWhiteSpace($name)) { $name = 'none' }",
        "if ([string]::IsNullOrWhiteSpace($root)) { ('polka . (' + $name + ') > '); return }",
        "$current = (Get-Location).ProviderPath",
        "$relative = Get-PolkaPromptPath $root $current",
        "('polka ' + $relative + ' (' + $name + ') > ')",
        "}",
        "}",
    ].join("\n");
}

/// Builds the full process environment slice for `polka sh` and `polka exec`.
string[] prepareShellEnvironment(
    string goos,
    const(string)[] env,
    ref Store store,
    string workingDir,
    string environmentName
)
{
    auto context = buildShellSessionContext(goos, store, workingDir, environmentName);
    return buildShellExecutionEnvironment(goos, env, store, context);
}

/// Applies MSVC toolchain overlays (if configured), runtime `.env` / framework vars,
/// ordered PATH composition, `PHP_PREFIX`, and prompt metadata variables.
string[] buildShellExecutionEnvironment(
    string goos,
    const(string)[] env,
    ref Store store,
    const ShellSessionContext context
)
{
    // MSVC variables apply before project env files so user-configured values (INCLUDE, LIB, etc.) still win.
    auto baseEnv = applyMSVCToolchainEnvironment(goos, env, context.toolchain);
    auto resolvedEnv = resolveRuntimeEnvironment(goos, baseEnv, store);

    string pathKey, systemPath;
    lookupEnvValue(goos, resolvedEnv, "PATH", pathKey, systemPath);
    if (pathKey.length == 0)
        pathKey = "PATH";

    string[] pathEntries;
    pathEntries.reserve(context.pathEntries.length + 1);
    pathEntries ~= context.pathEntries;
    pathEntries ~= systemPath;
    auto resolvedPath = joinPathList(goos, pathEntries);

    auto updatedEnv = replaceEnvValue(goos, resolvedEnv, pathKey, resolvedPath);
    updatedEnv = replaceEnvValue(goos, updatedEnv, polkaPromptEnvEnv, context.environmentName);
    if (context.phpPrefix.length > 0)
    {
        string k, v;
        if (!lookupEnvValue(goos, updatedEnv, "PHP_PREFIX", k, v))
            updatedEnv = replaceEnvValue(goos, updatedEnv, "PHP_PREFIX", context.phpPrefix);
    }

    return replaceEnvValue(goos, updatedEnv, polkaPromptRootEnv, context.promptRoot);
}

/// Discovers paths, Windows vendor shims, PHP build tools, and MSVC toolchain for a shell session.
ShellSessionContext buildShellSessionContext(
    string goos,
    ref Store store,
    string workingDir,
    string environmentName
)
{
    string rootDir;
    try
    {
        rootDir = cleanPath(absolutePath(store.rootDir));
    }
    catch (Exception e)
    {
        throw new Exception("resolve Polka root directory: " ~ e.msg);
    }

    string binDir;
    try
    {
        binDir = cleanPath(absolutePath(store.binDir));
    }
    catch (Exception e)
    {
        throw new Exception("resolve Polka bin directory: " ~ e.msg);
    }

    auto shellProjectDir = shellPathRoot(rootDir);
    auto vendorProjectDir = discoverVendorProjectDir(workingDir, shellProjectDir);
    auto vendorBinDir = buildPath(vendorProjectDir, vendorDirectoryName, binDirectoryName);
    auto promptRoot = shellPromptRoot(shellProjectDir);

    ShellSessionContext context;
    context.environmentName = environmentName;
    context.rootDir = rootDir;
    context.promptRoot = promptRoot;
    context.vendorProjectDir = vendorProjectDir;
    context.vendorBinDir = vendorBinDir;
    context.pathEntries = [binDir];

    if (goos == "windows")
    {
        context.vendorShimDir = prepareWindowsVendorBinPHPSupport(vendorBinDir, rootDir);
        if (context.vendorShimDir.length > 0)
            context.pathEntries ~= context.vendorShimDir;
    }

    context.pathEntries ~= vendorBinDir;

    if (environmentName != "none")
    {
        auto environments = store.list();
        foreach (envItem; environments)
        {
            if (envItem.name == environmentName)
            {
                context.pathEntries ~= store.phpBuildToolPathEntries(envItem);
                context.phpPrefix = store.phpRuntimePrefix(envItem);
                auto toolchain = store.msvcToolchain(envItem);
                if (toolchain !is null)
                {
                    context.toolchain = toolchain;
                    if (auto pathVals = "PATH" in toolchain.prepend)
                        context.pathEntries ~= *pathVals;
                }
                break;
            }
        }
    }

    return context;
}

/// Overlays a captured MSVC developer environment onto `env` (`PATH` entries are composed separately).
string[] applyMSVCToolchainEnvironment(string goos, const(string)[] env, const(MSVCToolchain)* toolchain) @safe
{
    if (toolchain is null)
        return env.dup;

    string[] updated = env.dup;
    auto setKeys = toolchain.set.keys;
    setKeys.sort();
    foreach (key; setKeys)
        updated = replaceEnvValue(goos, updated, key, toolchain.set[key]);

    string[] prependKeys;
    foreach (key; toolchain.prepend.keys)
    {
        if (!envKeysEqual(goos, key, "PATH"))
            prependKeys ~= key;
    }
    prependKeys.sort();

    foreach (key; prependKeys)
    {
        string entryKey, current;
        lookupEnvValue(goos, updated, key, entryKey, current);
        if (entryKey.length == 0)
            entryKey = key;

        string[] entries;
        entries.reserve(toolchain.prepend[key].length + 1);
        entries ~= toolchain.prepend[key];
        entries ~= current;
        updated = replaceEnvValue(goos, updated, entryKey, joinPathList(goos, entries));
    }

    return updated;
}

/// Walks upward from `workingDir` up to `projectRoot` looking for `vendor/bin`.
string discoverVendorProjectDir(string workingDir, string projectRoot) @trusted
{
    string currentDir;
    try
    {
        currentDir = cleanPath(absolutePath(workingDir));
    }
    catch (Exception e)
    {
        throw new Exception("resolve vendor project directory: " ~ e.msg);
    }

    string resolvedProjectRoot;
    try
    {
        resolvedProjectRoot = cleanPath(absolutePath(projectRoot));
    }
    catch (Exception e)
    {
        throw new Exception("resolve Polka project root: " ~ e.msg);
    }

    while (true)
    {
        if (directoryExists(buildPath(currentDir, vendorDirectoryName, binDirectoryName)))
            return currentDir;
        if (samePath(currentDir, resolvedProjectRoot))
            return resolvedProjectRoot;

        auto parentDir = dirName(currentDir);
        if (parentDir == currentDir)
            return resolvedProjectRoot;
        currentDir = parentDir;
    }
}

/// Prepends `<root>/bin` and `<project>/vendor/bin` to PATH inside `env`.
string[] buildShellEnvironment(string goos, const(string)[] env, const Store store) @trusted
{
    string pathKey, systemPath;
    lookupEnvValue(goos, env, "PATH", pathKey, systemPath);
    if (pathKey.length == 0)
        pathKey = "PATH";

    auto shellProjectDir = shellPathRoot(store.rootDir);
    auto resolvedPath = joinPathList(
        goos,
        store.binDir,
        buildPath(shellProjectDir, vendorDirectoryName, binDirectoryName),
        systemPath
    );
    return replaceEnvValue(goos, env, pathKey, resolvedPath);
}

/// Returns the parent directory of `<root>` (e.g. project root for `.polka`).
string shellPathRoot(string rootDir) @safe
{
    return dirName(cleanPath(rootDir));
}

/// Scans `vendor/bin` on Windows for extensionless PHP proxies/launchers and generates `.cmd` shims.
string prepareWindowsVendorBinPHPSupport(string vendorBinDir, string rootDir) @trusted
{
    VendorBinPHPTarget[] targets;
    try
    {
        targets = windowsVendorBinPHPTargets(vendorBinDir);
    }
    catch (Exception e)
    {
        if (isMissingFileError(e))
            return "";
        throw new Exception("scan vendor/bin commands: " ~ e.msg);
    }

    if (targets.length == 0)
        return "";

    auto shimDir = buildPath(rootDir, shellRuntimeDirName, shellSupportDirName, vendorShimDirName);
    try
    {
        recreateDirectory(shimDir);
    }
    catch (Exception e)
    {
        throw new Exception("prepare vendor php shim directory: " ~ e.msg);
    }

    foreach (target; targets)
        writeWindowsVendorPHPShim(shimDir, target.commandName, target.targetPath);

    return shimDir;
}

/// Returns sorted extensionless PHP script targets discovered under `vendorBinDir`.
VendorBinPHPTarget[] windowsVendorBinPHPTargets(string vendorBinDir) @trusted
{
    VendorBinPHPTarget[] targets;
    foreach (entry; dirEntries(vendorBinDir, SpanMode.shallow))
    {
        if (entry.isDir)
            continue;
        auto name = baseName(entry.name);
        if (extension(name).length != 0)
            continue;

        auto commandPath = buildPath(vendorBinDir, name);
        auto targetPath = resolveVendorBinPHPTarget(commandPath);
        if (targetPath.length > 0)
            targets ~= VendorBinPHPTarget(name, targetPath);
    }

    targets.sort!((a, b) => a.commandName < b.commandName);
    return targets;
}

/// Identifies a PHP entry script from either a PHP shebang or a POSIX shell proxy with sibling `.php`.
string resolveVendorBinPHPTarget(string commandPath) @trusted
{
    auto line = readScriptFirstLine(commandPath);
    if (isPHPShebang(line))
        return commandPath;
    if (!isPOSIXShellShebang(line))
        return "";

    auto siblingPHPTarget = commandPath ~ ".php";
    if (fileExists(siblingPHPTarget))
        return siblingPHPTarget;

    return "";
}

private string shellPromptRoot(string projectDir) @trusted
{
    try
    {
        return cleanPath(absolutePath(projectDir));
    }
    catch (Exception e)
    {
        throw new Exception("resolve prompt root: " ~ e.msg);
    }
}

private void writeWindowsVendorPHPShim(string shimDir, string commandName, string targetPath) @trusted
{
    auto shimPath = buildPath(shimDir, commandName ~ ".cmd");
    auto contents = "@echo off\r\n" ~
        "setlocal\r\n" ~
        `call php "` ~ escapeWindowsShimValue(targetPath) ~ `" %*` ~ "\r\n" ~
        "exit /b %ERRORLEVEL%\r\n";

    try
    {
        write(shimPath, contents);
        setFileMode(shimPath, octal!755);
    }
    catch (Exception e)
    {
        throw new Exception(format(`write vendor php shim "%s": %s`, shimPath, e.msg));
    }
}

unittest
{
    // Verify Windows vendor/bin shim creation works cleanly cross-platform when invoked with "windows".
    import std.algorithm : canFind;
    import std.file : exists, mkdirRecurse, readText, write;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    auto tmp = createTempDir("polka-shell-env-");
    scope (exit) rmdirRecurseQuiet(tmp);

    auto vendorBin = buildPath(tmp, "vendor", "bin");
    auto rootDir = buildPath(tmp, ".polka");
    mkdirRecurse(vendorBin);
    write(buildPath(vendorBin, "phpunit"), "#!/usr/bin/env php\n<?php\n");
    write(buildPath(vendorBin, "sail"), "#!/usr/bin/env sh\nexec php vendor/bin/sail.php\n");
    write(buildPath(vendorBin, "sail.php"), "<?php\n");

    auto shimDir = prepareWindowsVendorBinPHPSupport(vendorBin, rootDir);
    assert(exists(buildPath(shimDir, "phpunit.cmd")));
    assert(exists(buildPath(shimDir, "sail.cmd")));
    assert(readText(buildPath(shimDir, "sail.cmd")).canFind(buildPath(vendorBin, "sail.php")));
}
