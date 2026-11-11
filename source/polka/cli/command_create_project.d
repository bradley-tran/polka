module polka.cli.command_create_project;

// Implements `polka create-project <package> [directory] [composer-args...]`:
// scaffolds a new Composer application via Polka's internal PHP+Composer runtime
// using `--no-install --no-scripts`, initializes `polka.yaml` for the detected
// framework (or plain default), installs project tools, prepares the managed
// database before running Composer's post-create scripts, and completes Composer setup.

import std.algorithm : canFind;
import std.file : getcwd;
import std.format : format;
import std.path : dirName, relativePath;
import std.string : indexOf, startsWith, strip;

import polka.backend : InitOptions, newProjectStore, Store, TextWriter, writeOutf;
import polka.cli.command_env : InstallCommandInput, runInstall;
import polka.cli.composer_hooks : runPostComposerHook;
import polka.cli.composer_scripts : composerCommandWorkingDir, executeTargetWithComposerScriptSupport;
import polka.cli.env_helpers : defaultEnvironmentName;
import polka.cli.hooks : startDatabaseServiceHook, StartHookContext;
import polka.cli.internal_tools : ensureInternalPHPAndPHAR, runInternalPHAR;
import polka.cli.shell_env : prepareShellEnvironment;
import polka.cli.types :
    applyManagedPHPRuntimeConfig,
    CliStatusException,
    dispatchPHARRequiresManagedPHP,
    joinPathList,
    lookupEnvValue,
    quoteGoString,
    replaceEnvValue;
import polka.plugins : ComposerCreateProjectDirectory;
import polka.service : currentEnvList;
import polka.support.fs : currentOS;
import polka.tools : Composer, FrankenPHP, PHP, PHPZTS;

/// Detects `-h` / `--help` manually because flag parsing is disabled for Composer pass-through.
bool wantsCreateProjectHelp(const(string)[] args) @safe pure
{
    foreach (arg; args)
    {
        auto trimmed = arg.strip();
        if (trimmed == "-h" || trimmed == "--help")
            return true;
    }
    return false;
}

/// Runs the full `polka create-project` lifecycle against internal Composer and
/// the newly initialized project environment.
void runCreateProject(TextWriter stdout, TextWriter stderr, ref Store store, const(string)[] args) @trusted
{
    string[] positionals;
    string[] options;
    splitCreateProjectArgs(args, positionals, options);
    if (positionals.length == 0)
    {
        throw new CliStatusException(
            1,
            "create-project requires a composer package argument, such as laravel/laravel"
        );
    }

    auto pkg = positionals[0];
    string workingDir;
    try
    {
        workingDir = getcwd();
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, "resolve current working directory: " ~ e.msg);
    }

    auto targetDir = ComposerCreateProjectDirectory(
        ["create-project"] ~ positionals,
        composerCommandWorkingDir(workingDir, args)
    );
    if (targetDir.length == 0)
    {
        throw new CliStatusException(
            1,
            format("cannot determine the create-project target directory from %s", quoteGoString(pkg))
        );
    }

    string phpPath;
    string composerPath;
    string[] env;
    try
    {
        ensureInternalPHPAndPHAR(stdout, store, Composer, phpPath, composerPath, env);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    // Download the scaffold without installing dependencies or running scripts yet.
    auto scaffoldArgs = ["create-project", "--no-install", "--no-scripts"] ~ args;
    int exitCode = 0;
    try
    {
        exitCode = runInternalPHAR(stdout, stderr, env, phpPath, composerPath, scaffoldArgs);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }
    if (exitCode != 0)
    {
        // Composer already printed its error; propagate its non-zero status silently.
        throw new CliStatusException(exitCode, "", false, "", false);
    }

    string framework;
    try
    {
        framework = store.detectFramework(pkg, targetDir);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    auto projectStore = newProjectStore(targetDir);
    projectStore.registry = store.registry;
    projectStore.plugins = store.plugins;
    projectStore.cacheDir = store.cacheDir;
    projectStore.downloader = store.downloader;

    try
    {
        if (framework.length > 0)
            projectStore.initWithFrameworkOptions(framework, InitOptions.init);
        else
            projectStore.initWithOptions(InitOptions.init);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    auto current = projectStore.current();
    if (current !is null && current.tools.length > 0)
    {
        try
        {
            runInstall(stdout, stderr, projectStore, InstallCommandInput(defaultEnvironmentName));
        }
        catch (Exception e)
        {
            throw new CliStatusException(1, e.msg);
        }
    }

    // Configure framework `.env` / DB secrets before starting the managed DB and scripts.
    string[] hookArgs = ["create-project", pkg, targetDir];
    try
    {
        runPostComposerHook(projectStore, "composer", hookArgs, workingDir);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    if (current !is null && !options.canFind("--no-scripts"))
    {
        try
        {
            startDatabaseServiceHook(StartHookContext(null, null, projectStore, *current));
        }
        catch (Exception e)
        {
            throw new CliStatusException(1, "prepare database for Composer scripts: " ~ e.msg);
        }
    }

    auto completionArgs = ["create-project", "--working-dir", targetDir] ~ options;
    try
    {
        exitCode = completeCreateProject(stdout, stderr, projectStore, phpPath, composerPath, completionArgs);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }
    if (exitCode != 0)
        throw new CliStatusException(exitCode, "", false, "", false);

    // Reapply framework hooks in case Composer scripts regenerated config files.
    try
    {
        runPostComposerHook(projectStore, "composer", hookArgs, workingDir);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    if (framework.length > 0)
        writeOutf(stdout, "Created %s project at %s\n", framework, targetDir);
    else
        writeOutf(stdout, "Created project at %s (no framework detected; run `polka init <framework>` to change the config)\n", targetDir);

    writeOutf(stdout, "Next steps: cd %s && polka sh\n", relativeOrAbsolutePath(workingDir, targetDir));
}

/// Separates positional args (`package`, optional `directory`/`version`) from
/// Composer flags, omitting root-download-only options from the second pass.
void splitCreateProjectArgs(const(string)[] args, out string[] positionals, out string[] options) @safe pure
{
    bool positionalOnly = false;
    for (size_t index = 0; index < args.length; index++)
    {
        auto arg = args[index];
        auto trimmed = arg.strip();
        if (trimmed.length == 0)
            continue;
        if (positionalOnly || !trimmed.startsWith("-"))
        {
            positionals ~= arg;
            continue;
        }
        if (trimmed == "--")
        {
            positionalOnly = true;
            continue;
        }

        auto eqIdx = trimmed.indexOf('=');
        auto name = eqIdx >= 0 ? trimmed[0 .. eqIdx] : trimmed;
        bool hasValue = eqIdx >= 0;

        bool rootOnly = false;
        bool consumesValue = false;
        switch (name)
        {
            case "--stability":
            case "-s":
            case "--repository":
            case "--repository-url":
            case "--working-dir":
            case "-d":
                rootOnly = true;
                consumesValue = true;
                break;
            case "--add-repository":
            case "--no-secure-http":
            case "--keep-vcs":
            case "--remove-vcs":
            case "--ask":
                rootOnly = true;
                break;
            case "--prefer-install":
            case "--audit-format":
            case "--ignore-platform-req":
                consumesValue = true;
                break;
            default:
                rootOnly = name.startsWith("-d") || name.startsWith("-s");
                break;
        }

        if (!rootOnly)
            options ~= arg;
        if (consumesValue && !hasValue && index + 1 < args.length)
        {
            index++;
            if (!rootOnly)
                options ~= args[index];
        }
    }
}

/// Completes dependency installation and Composer scripts using the project's
/// installed PHP runtime and environment overlays (falling back to internal hosts
/// when the config specifies no tools).
int completeCreateProject(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    string phpPath,
    string composerPath,
    const(string)[] args
) @trusted
{
    auto current = store.current();
    if (current !is null)
    {
        if (current.toolVersion(PHP).length > 0
            || current.toolVersion(PHPZTS).length > 0
            || current.toolVersion(FrankenPHP).length > 0)
        {
            phpPath = store.resolveTool("php");
        }
        if (current.toolVersion(Composer).length > 0)
            composerPath = store.resolveTool("composer");
    }

    auto goos = currentOS();
    auto env = prepareShellEnvironment(goos, currentEnvList(), store, store.projectDir, defaultEnvironmentName);
    env = applyManagedPHPRuntimeConfig(goos, env, phpPath);

    // Ensure internal PHP remains on PATH when Composer scripts shell out to bare `php`.
    string pathKey, pathValue;
    lookupEnvValue(goos, env, "PATH", pathKey, pathValue);
    if (pathKey.length == 0)
        pathKey = "PATH";
    env = replaceEnvValue(goos, env, pathKey, joinPathList(goos, pathValue, dirName(phpPath)));

    auto target = composerPath;
    const(string)[] runArgs = args;
    if (dispatchPHARRequiresManagedPHP("composer", composerPath))
    {
        target = phpPath;
        runArgs = [composerPath] ~ args;
    }

    return executeTargetWithComposerScriptSupport(goos, true, stdout, stderr, env, store.projectDir, target, runArgs);
}

/// Returns `target` relative to `base` unless climbing upward (`..`) is required.
string relativeOrAbsolutePath(string base, string target) @safe
{
    auto rel = relativePath(target, base);
    if (rel.length == 0 || rel.startsWith(".."))
        return target;
    return rel;
}

unittest
{
    string[] pos, opts;
    splitCreateProjectArgs([
        "laravel/laravel",
        "demo",
        "--stability=dev",
        "-s", "alpha",
        "--working-dir", "/tmp",
        "--no-scripts",
        "--prefer-install", "dist",
        "--",
        "--flag-positional",
    ], pos, opts);
    assert(pos == ["laravel/laravel", "demo", "--flag-positional"]);
    assert(opts == ["--no-scripts", "--prefer-install", "dist"]);

    assert(wantsCreateProjectHelp(["laravel/laravel", "--help"]));
    assert(wantsCreateProjectHelp(["-h"]));
    assert(!wantsCreateProjectHelp(["laravel/laravel", "--no-scripts"]));

    assert(relativeOrAbsolutePath("/workspace", "/workspace/demo") == "demo");
    assert(relativeOrAbsolutePath("/workspace/sub", "/other/demo") == "/other/demo");
}
