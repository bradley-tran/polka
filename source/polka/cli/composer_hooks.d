module polka.cli.composer_hooks;

// Framework post-composer lifecycle hooks triggered after composer install,
// update, or create-project completes (excluding Composer solver failures).

import std.algorithm : canFind, startsWith;
import std.format : format;
import std.path : absolutePath, baseName, buildPath, extension;
import std.string : icmp, join, strip, toLower;

import polka.backend : DatabaseConfig, Environment, FrameworkPlugin, Store;
import polka.cli.env_runtime : frameworkDatabaseCredentialsFromManaged;
import polka.cli.types : samePath;
import polka.plugins :
    FrameworkDatabaseCredentials = DatabaseCredentials,
    PostComposerContext;
import polka.service :
    ensureManagedDatabaseCredentialAssets,
    ResolvedDatabaseEnvironment;

/// Runs the active environment's framework post-composer hook when `tool` is `composer`
/// and `args` invokes a lifecycle command (`install`, `update`, `create-project`).
void runPostComposerHook(ref Store store, string tool, const(string)[] args, string workingDir)
{
    if (icmp(tool.strip(), "composer") != 0 || !isPostComposerCommand(args))
        return;

    auto current = store.current();
    if (current is null || current.framework.strip().length == 0)
        return;

    FrameworkPlugin plugin;
    if (!store.frameworkPlugin(current.framework, plugin))
    {
        throw new Exception(format(
            `unsupported framework "%s"; supported frameworks: %s`,
            current.framework, store.supportedFrameworks().join(", ")
        ));
    }

    auto credentials = ensurePostComposerDatabaseCredentials(store, *current);
    plugin.postComposer(PostComposerContext(
        *current,
        store.projectDir,
        workingDir,
        args.dup,
        credentials,
    ));
}

/// Triggers post-composer hooks after `polka exec` runs an unmanaged Composer binary
/// (skipping when `target` is Polka's own shim, which dispatches hooks itself).
void runPostExecComposerHook(
    ref Store store,
    string command,
    const(string)[] args,
    string workingDir,
    string target
)
{
    if (!isComposerExecutable(command) || isPolkaManagedCommandShim(store, target, "composer"))
        return;

    runPostComposerHook(store, "composer", args, workingDir);
}

/// Skips post-composer hooks only for Composer dependency solver failures (`exitCode == 2`).
bool shouldRunPostComposerHook(int exitCode) @safe pure nothrow @nogc
{
    return exitCode != 2;
}

/// Reports whether `args` invokes `install`, `update`, or `create-project`.
bool isPostComposerCommand(const(string)[] args) @safe pure
{
    switch (composerCommand(args))
    {
        case "install":
        case "update":
        case "create-project":
            return true;
        default:
            return false;
    }
}

/// Returns the first non-option subcommand token in a Composer argument list.
string composerCommand(const(string)[] args) @safe pure
{
    bool skipNext = false;
    foreach (arg; args)
    {
        auto trimmed = arg.strip();
        if (trimmed.length == 0)
            continue;
        if (skipNext)
        {
            skipNext = false;
            continue;
        }
        if (trimmed.startsWith("-"))
        {
            skipNext = composerOptionConsumesValue(trimmed);
            continue;
        }
        return trimmed.toLower();
    }
    return "";
}

private bool composerOptionConsumesValue(string option) @safe pure
{
    if (option.canFind("="))
        return false;
    return option == "-d" || option == "--working-dir";
}

/// Reports whether `command` refers to `composer`, `composer.phar`, `composer.cmd`, etc.
bool isComposerExecutable(string command) @safe pure
{
    auto name = baseName(command).strip().toLower();
    if (name.length == 0)
        return false;
    if (name == "composer.phar")
        return true;

    auto ext = extension(name).toLower();
    if (ext.length > 0)
        name = name[0 .. $ - ext.length];

    return name == "composer";
}

/// Reports whether `target` resolves to one of Polka's generated shims in `<root>/bin`.
bool isPolkaManagedCommandShim(const Store store, string target, string command) @trusted
{
    auto trimmed = target.strip();
    if (trimmed.length == 0)
        return false;

    string absoluteTarget;
    try
    {
        absoluteTarget = absolutePath(trimmed);
    }
    catch (Exception)
    {
        return false;
    }

    foreach (name; [command, command ~ ".cmd", command ~ ".bat", command ~ ".exe"])
    {
        try
        {
            auto candidate = absolutePath(buildPath(store.binDir, name));
            if (samePath(absoluteTarget, candidate))
                return true;
        }
        catch (Exception)
        {
            continue;
        }
    }
    return false;
}

/// Generates or loads managed database credentials so post-composer framework actions
/// can configure `.env` / framework database config files immediately.
FrameworkDatabaseCredentials* ensurePostComposerDatabaseCredentials(ref Store store, const Environment env)
{
    if (env.database is null || env.database.engine.strip().length == 0)
        return null;

    auto resolved = ResolvedDatabaseEnvironment(cast(Environment) env, cast(DatabaseConfig*) env.database);
    auto credentials = ensureManagedDatabaseCredentialAssets(store.rootDir, resolved);
    return frameworkDatabaseCredentialsFromManaged(env, credentials);
}

unittest
{
    // TestIsPostComposerCommandDetectsComposerLifecycleCommands
    assert(isPostComposerCommand(["install"]));
    assert(isPostComposerCommand(["update"]));
    assert(isPostComposerCommand(["create-project", "laravel/laravel", "site"]));
    assert(isPostComposerCommand(["--working-dir", "site", "install"]));
    assert(!isPostComposerCommand(["require", "vendor/install"]));

    // TestShouldRunPostComposerHookSkipsOnlyComposerSolverFailures
    assert(shouldRunPostComposerHook(0));
    assert(shouldRunPostComposerHook(1));
    assert(!shouldRunPostComposerHook(2));
}
