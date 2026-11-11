module polka.cli.internal_tools;

// On-demand provisioning and execution helpers for machine-global internal tools
// (internal PHP, composer, pie) stored outside project configuration files.

import std.format : format;

import polka.backend :
    InstallProgress,
    InstallProgressDownloading,
    InstallProgressInstalled,
    Store,
    TextWriter,
    writeOutf;
import polka.tools : PHP;
import polka.cli.types : applyManagedPHPRuntimeConfig, executeTargetWithEnv;
import polka.service : currentEnvList;
import polka.support.fs : currentOS;

/// Emits download/install progress lines during internal tool provisioning.
void delegate(InstallProgress) internalToolProgress(TextWriter stdout) @safe
{
    return (InstallProgress progress) {
        if (progress.stage == InstallProgressDownloading)
            writeOutf(stdout, "Downloading internal %s %s...\n", progress.tool, progress.version_);
        else if (progress.stage == InstallProgressInstalled)
            writeOutf(stdout, "Installed internal %s %s\n", progress.tool, progress.version_);
    };
}

/// Ensures one internal tool (such as `php`, `composer`, or `pie`) is present in
/// the global tools directory, downloading and caching it if missing.
string ensureInternalTool(TextWriter stdout, ref Store store, string tool)
{
    return store.ensureInternalTool(tool, "", internalToolProgress(stdout));
}

/// Provisions the internal PHP runtime and named PHAR tool (`composer` or `pie`)
/// into the global tools directory and overlays the internal PHP runtime config
/// (`PHPRC` and `OPENSSL_CONF`) onto the current process environment.
void ensureInternalPHPAndPHAR(
    TextWriter stdout,
    ref Store store,
    string pharTool,
    out string phpPath,
    out string pharPath,
    out string[] env
)
{
    phpPath = ensureInternalTool(stdout, store, PHP);
    pharPath = ensureInternalTool(stdout, store, pharTool);
    env = applyManagedPHPRuntimeConfig(currentOS(), currentEnvList(), phpPath);
}

/// Executes `<internal-php> <phar> <args...>` streaming output to `stdout`/`stderr`
/// and returns the child exit code.
int runInternalPHAR(
    TextWriter stdout,
    TextWriter stderr,
    const(string)[] env,
    string phpPath,
    string pharPath,
    const(string)[] args
)
{
    string[] fullArgs;
    fullArgs.reserve(1 + args.length);
    fullArgs ~= pharPath;
    fullArgs ~= args;
    return executeTargetWithEnv(stdout, stderr, env, phpPath, fullArgs);
}

unittest
{
    import std.algorithm : canFind;
    import polka.backend : BufferTextWriter;

    auto buf = new BufferTextWriter();
    auto cb = internalToolProgress(buf);
    cb(InstallProgress(1, 1, "php", "8.4.1", InstallProgressDownloading));
    cb(InstallProgress(1, 1, "php", "8.4.1", InstallProgressInstalled));

    auto text = buf.text();
    assert(text.canFind("Downloading internal php 8.4.1...\n"));
    assert(text.canFind("Installed internal php 8.4.1\n"));
}
