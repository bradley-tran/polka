module polka.cli.command_dispatch;

// Internal `polka dispatch <tool> [args...]` implementation and managed shim
// target resolution, including managed PHP runtime configuration injection,
// PHAR host wrapping for Composer, RabbitMQ broker environment setup, and
// post-composer framework hooks.

import std.file : getcwd;
import std.format : format;
import std.string : icmp, startsWith, strip, toLower;

import polka.backend : Store, TextWriter;
import polka.cli.composer_hooks :
    runPostComposerHook,
    shouldRunPostComposerHook;
import polka.cli.composer_scripts : executeTargetWithComposerScriptSupport;
import polka.cli.env_runtime : resolveRuntimeEnvironment;
import polka.cli.service_context : managedServiceContext;
import polka.cli.types :
    applyManagedPHPRuntimeConfig,
    dispatchPHARRequiresManagedPHP,
    writeError,
    writeErrorf;
import polka.service :
    currentEnvList,
    prepareRabbitMQDispatchEnvironment;
import polka.support.fs : currentOS;

/// Executes `polka dispatch <tool> [args...]` against the active environment store.
int runDispatch(TextWriter stdout, TextWriter stderr, const(string)[] args, ref Store store) @trusted
{
    if (args.length == 0)
    {
        writeError(stderr, "dispatch requires a tool name");
        return 2;
    }

    string[] env;
    try
    {
        env = resolveRuntimeEnvironment(currentOS(), currentEnvList(), store);
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    auto tool = args[0].strip();
    string target;
    try
    {
        target = store.resolveTool(tool);
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
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

    auto composerArgs = args[1 .. $].dup;
    const(string)[] dispatchArgs = args[1 .. $];
    bool usesManagedPHP = icmp(tool, "php") == 0 || icmp(tool, "frankenphp") == 0;
    if (dispatchPHARRequiresManagedPHP(tool, target))
    {
        string phpTarget;
        try
        {
            phpTarget = store.resolveTool("php");
        }
        catch (Exception e)
        {
            writeErrorf(stderr, "resolve php for %s: %s", tool.toLower(), e.msg);
            return 1;
        }

        string[] prepended;
        prepended.reserve(dispatchArgs.length + 1);
        prepended ~= target;
        prepended ~= dispatchArgs;
        dispatchArgs = prepended;
        target = phpTarget;
        usesManagedPHP = true;
    }

    if (usesManagedPHP)
    {
        try
        {
            env = applyManagedPHPRuntimeConfig(currentOS(), env, target);
        }
        catch (Exception e)
        {
            writeErrorf(stderr, "%s", e.msg);
            return 1;
        }
    }

    if (tool.toLower().startsWith("rabbitmq"))
    {
        try
        {
            auto current = store.current();
            if (current is null)
                throw new Exception("no active environment");
            env = prepareRabbitMQDispatchEnvironment(managedServiceContext(store, *current, stderr));
        }
        catch (Exception e)
        {
            writeErrorf(stderr, "prepare rabbitmq runtime: %s", e.msg);
            return 1;
        }
    }

    int exitCode = 0;
    try
    {
        exitCode = executeTargetWithComposerScriptSupport(
            currentOS(),
            icmp(tool, "composer") == 0,
            stdout,
            stderr,
            env,
            workingDir,
            target,
            dispatchArgs
        );
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }

    if (shouldRunPostComposerHook(exitCode))
    {
        try
        {
            runPostComposerHook(store, tool, composerArgs, workingDir);
        }
        catch (Exception e)
        {
            writeErrorf(stderr, "%s", e.msg);
            return 1;
        }
    }

    return exitCode;
}
