module polka.cli.command_sh;

// Implementation of `polka sh` / `polka shell`: resolves the interactive
// platform shell, builds Polka's PATH/environment/toolchain overlay, emits the
// banner, and spawns the shell process (or test seam).

import std.file : getcwd;
import std.process : environment;

import polka.backend : Store, TextWriter;
import polka.cli.shell_env :
    currentShellEnvironmentName,
    prepareShellEnvironment,
    resolveInteractiveShell,
    writeShellBanner;
import polka.cli.types : executeTargetWithEnv, writeError, writeErrorf;
import polka.service : currentEnvList;
import polka.support.fs : currentOS;

/// Test seam overriding interactive shell execution during unit tests.
int delegate(TextWriter stdout, TextWriter stderr, const(string)[] env, string target, const(string)[] args)
    launchInteractiveShellFunc;

/// Default interactive shell launcher delegating directly to `executeTargetWithEnv`.
int launchInteractiveShell(
    TextWriter stdout,
    TextWriter stderr,
    const(string)[] env,
    string target,
    const(string)[] args
) @trusted
{
    return executeTargetWithEnv(stdout, stderr, env, target, args);
}

private int callLaunchInteractiveShell(
    TextWriter stdout,
    TextWriter stderr,
    const(string)[] env,
    string target,
    const(string)[] args
) @trusted
{
    if (launchInteractiveShellFunc !is null)
        return launchInteractiveShellFunc(stdout, stderr, env, target, args);
    return launchInteractiveShell(stdout, stderr, env, target, args);
}

/// Prints the Polka shell banner and launches an interactive shell configured with
/// Polka's environment PATH, `.env` overlays, and prompt metadata.
int runSh(TextWriter stdout, TextWriter stderr, ref Store store) @trusted
{
    string target;
    string[] args;
    resolveInteractiveShell(currentOS(), environment.get("SHELL", ""), target, args);

    string environmentName;
    try
    {
        environmentName = currentShellEnvironmentName(store);
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }

    string workingDir;
    try
    {
        workingDir = getcwd();
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "resolve current working directory: %s", e.msg);
        return 1;
    }

    string[] env;
    try
    {
        env = prepareShellEnvironment(currentOS(), currentEnvList(), store, workingDir, environmentName);
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }

    writeShellBanner(stdout, environmentName);

    try
    {
        return callLaunchInteractiveShell(stdout, stderr, env, target, args);
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }
}

unittest
{
    import std.algorithm : canFind;
    import polka.backend : BufferTextWriter, newProjectStore;
    import polka.cli.types : lookupEnvValue;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    auto projectDir = createTempDir("polka-sh-cmd-");
    scope (exit) rmdirRecurseQuiet(projectDir);

    auto store = newProjectStore(projectDir);
    store.init();

    auto oldLaunch = launchInteractiveShellFunc;
    scope (exit) launchInteractiveShellFunc = oldLaunch;

    string capturedTarget;
    const(string)[] capturedEnv;
    const(string)[] capturedArgs;
    launchInteractiveShellFunc = (TextWriter _out, TextWriter _err, const(string)[] env, string target, const(string)[] args) {
        capturedEnv = env;
        capturedTarget = target;
        capturedArgs = args;
        return 0;
    };

    auto outBuf = new BufferTextWriter();
    auto errBuf = new BufferTextWriter();
    assert(runSh(outBuf, errBuf, store) == 0);
    assert(errBuf.text().length == 0);
    assert(outBuf.text() == "Opened Polka shell for environment default\n");

    string k, v;
    assert(lookupEnvValue(currentOS(), capturedEnv, "POLKA_PROMPT_ENVIRONMENT", k, v) && v == "default");
    assert(lookupEnvValue(currentOS(), capturedEnv, "POLKA_PROMPT_ROOT", k, v) && v == projectDir);
    assert(lookupEnvValue(currentOS(), capturedEnv, "PATH", k, v) && v.canFind(store.binDir));
    assert(capturedTarget.length > 0 && capturedArgs.length > 0);
}
