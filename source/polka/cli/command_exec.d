module polka.cli.command_exec;

// Implementation of `polka exec <command> [args...]`: composes the Polka
// shell execution environment, resolves targets on the composed PATH/PATHEXT,
// wraps Composer invocations with cross-platform script permission/wrapper
// support, and runs post-composer framework hooks on non-solver-failure exits.

import std.file : getcwd;

import polka.backend : Store, TextWriter, writeOutln;
import polka.cli.composer_hooks :
    isComposerExecutable,
    runPostExecComposerHook,
    shouldRunPostComposerHook;
import polka.cli.composer_scripts : executeTargetWithComposerScriptSupport;
import polka.cli.shell_env :
    buildShellExecutionEnvironment,
    buildShellSessionContext,
    currentShellEnvironmentName;
import polka.cli.types : resolveExecTarget, writeError, writeErrorf;
import polka.service : currentEnvList;
import polka.support.fs : currentOS;

/// Executes an arbitrary command inside Polka's composed shell environment.
/// Returns `2` when no command is supplied, `1` on preparation/launch failures,
/// or the child process's exit code.
int runExec(TextWriter stdout, TextWriter stderr, ref Store store, const(string)[] args) @trusted
{
    if (args.length == 0)
    {
        writeOutln(stderr, "error: exec requires a command");
        return 2;
    }

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

    string target;
    string[] resolvedEnv;
    try
    {
        auto context = buildShellSessionContext(currentOS(), store, workingDir, environmentName);
        resolvedEnv = buildShellExecutionEnvironment(currentOS(), currentEnvList(), store, context);
        target = resolveExecTarget(currentOS(), args[0], resolvedEnv);
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }

    int exitCode = 0;
    try
    {
        exitCode = executeTargetWithComposerScriptSupport(
            currentOS(),
            isComposerExecutable(args[0]),
            stdout,
            stderr,
            resolvedEnv,
            workingDir,
            target,
            args[1 .. $]
        );
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }

    if (shouldRunPostComposerHook(exitCode))
    {
        try
        {
            runPostExecComposerHook(store, args[0], args[1 .. $], workingDir, target);
        }
        catch (Exception e)
        {
            writeError(stderr, e.msg);
            return 1;
        }
    }

    return exitCode;
}

unittest
{
    import std.algorithm : canFind;
    import std.file : mkdirRecurse, write;
    import std.path : buildPath;
    import polka.backend : BufferTextWriter, newProjectStore;
    import polka.support.fs : createTempDir, octal, rmdirRecurseQuiet, setFileMode;

    // Missing command exits with code 2 matching CLI arg validation.
    {
        auto projectDir = createTempDir("polka-exec-empty-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto store = newProjectStore(projectDir);
        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();

        assert(runExec(outBuf, errBuf, store, null) == 2);
        assert(errBuf.text().canFind("exec requires a command"));
    }

    // Executes tool from vendor/bin with composed POLKA_PROMPT_ENVIRONMENT.
    version (Posix)
    {
        auto projectDir = createTempDir("polka-exec-ok-");
        scope (exit) rmdirRecurseQuiet(projectDir);

        auto store = newProjectStore(projectDir);
        store.init();

        auto vendorBin = buildPath(projectDir, "vendor", "bin");
        mkdirRecurse(vendorBin);
        auto toolPath = buildPath(vendorBin, "hello-exec");
        write(toolPath, "#!/bin/sh\nprintf 'env=%s arg=%s\\n' \"$POLKA_PROMPT_ENVIRONMENT\" \"$1\"\n");
        setFileMode(toolPath, octal!755);

        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();
        auto code = runExec(outBuf, errBuf, store, ["hello-exec", "world"]);
        assert(code == 0, errBuf.text());
        assert(outBuf.text() == "env=default arg=world\n");
    }
}
