module polka.cli.cli;

// Top-level Polka CLI router and flag parser matching Go Cobra command,
// persistent `--root`, help, shortcut expansion, and exit code parity.

import std.algorithm : startsWith;
import std.conv : ConvException, to;
import std.format : format;
import std.string : indexOf, strip, toLower;

import polka.backend : Store, TextWriter, writeOut, writeOutln;
import polka.cli.command_cert : executeCertInstallCommand;
import polka.cli.command_config_form : runConfigForm;
import polka.cli.command_create_project : runCreateProject, wantsCreateProjectHelp;
import polka.cli.command_dashboard : runDashboard;
import polka.cli.command_db : runDB;
import polka.cli.command_dispatch : runDispatch;
import polka.cli.command_env :
    buildDatabaseInput,
    ConfigCommandInput,
    defaultNewComposerVersion,
    defaultNewNodeJSVersion,
    defaultNewPHPVersion,
    InitCommandInput,
    InstallCommandInput,
    NewCommandInput,
    normalizeInitCommandInput,
    parseInstallToolVersion,
    runConfig,
    runInit,
    runInstall,
    runList,
    runNew,
    runRemove,
    runStatus,
    runUse;
import polka.cli.command_exec : runExec;
import polka.cli.command_ext : dispatchExtCommand;
import polka.cli.command_logs : runLogs;
import polka.cli.command_serve : runStart;
import polka.cli.command_sh : runSh;
import polka.cli.command_shortcut : shortcutSpecs, ShortcutSpec;
import polka.cli.command_stop : runStop;
import polka.cli.hooks : ServeCommandInput;
import polka.cli.types : CliStatusException, CommandContext, quoteGoString;
import polka.cli.usage;

/// Main CLI entry point matching Go's `cli.Run(stdout, stderr, args) int`.
/// Parses persistent flags, expands shortcuts, dispatches commands, formats errors,
/// and maps `CliStatusException` / exceptions to deterministic exit codes.
int run(TextWriter stdout, TextWriter stderr, const(string)[] args) @trusted
{
    CommandContext ctx = CommandContext(stdout, stderr);
    try
    {
        return runWithContext(stdout, stderr, ctx, args);
    }
    catch (CliStatusException se)
    {
        if (se.hasErr && se.msg.length > 0)
        {
            writeOutln(stderr, "error: " ~ se.msg);
            if (se.showUsage && se.usage.length > 0)
            {
                writeOutln(stderr, "");
                writeOut(stderr, se.usage);
            }
        }
        return se.code;
    }
    catch (Exception e)
    {
        writeOutln(stderr, "error: " ~ e.msg);
        return 1;
    }
}

private struct FlagSpec
{
    string name;
    string shorthand;
    bool isBool = false;
    bool repeatable = false;
}

private struct ParsedFlags
{
    string[string] singleValues;
    string[][string] multiValues;
    bool[string] changed;
    string[] positionals;
    bool wantsHelp = false;

    bool isChanged(string name) const @safe pure nothrow
    {
        return (name in changed) !is null;
    }

    string getString(string name, string fallback = "") const @safe pure nothrow
    {
        if (auto p = name in singleValues)
            return *p;
        return fallback;
    }

    bool getBool(string name) const @safe pure nothrow
    {
        if (auto p = name in singleValues)
            return *p == "true";
        return false;
    }

    const(string)[] getMulti(string name) const @safe pure nothrow
    {
        if (auto p = name in multiValues)
            return *p;
        return null;
    }
}

/// Core router allowing shortcut expansion to preserve already-parsed `ctx.rootDir`.
private int runWithContext(
    TextWriter stdout,
    TextWriter stderr,
    ref CommandContext ctx,
    const(string)[] args
) @trusted
{
    bool rootHelp = false;
    size_t cmdIndex = args.length;

    // Parse root persistent flags until the first positional command token.
    size_t i = 0;
    while (i < args.length)
    {
        auto arg = args[i];
        if (arg == "--")
        {
            i++;
            cmdIndex = i;
            break;
        }
        if (arg == "-h" || arg == "--help")
        {
            rootHelp = true;
            i++;
            continue;
        }
        if (arg.startsWith("--help="))
        {
            rootHelp = parseBoolFlagValue("help", arg["--help=".length .. $], 2, rootUsage);
            i++;
            continue;
        }
        if (arg == "--root")
        {
            if (i + 1 >= args.length)
                throw new CliStatusException(2, "flag needs an argument: --root", true, rootUsage);
            ctx.rootDir = args[i + 1];
            i += 2;
            continue;
        }
        if (arg.startsWith("--root="))
        {
            ctx.rootDir = arg["--root=".length .. $];
            i++;
            continue;
        }
        if (arg.length > 1 && arg[0] == '-')
        {
            if (arg.startsWith("--"))
            {
                auto eq = arg.indexOf('=');
                auto name = eq >= 0 ? arg[2 .. eq] : arg[2 .. $];
                throw new CliStatusException(2, format("unknown flag: --%s", name), true, rootUsage);
            }
            auto ch = arg[1 .. 2];
            throw new CliStatusException(2, format("unknown shorthand flag: '%s' in %s", ch, arg), true, rootUsage);
        }

        cmdIndex = i;
        break;
    }

    if (cmdIndex >= args.length)
    {
        writeOut(stdout, rootUsage);
        return 0;
    }

    auto cmdName = args[cmdIndex];
    auto rest = args[cmdIndex + 1 .. $];

    if (cmdName == "help")
        return runHelpCommand(stdout, stderr, rest);

    // Check top-level shortcuts (DisableFlagParsing=true; re-dispatch via root).
    foreach (spec; shortcutSpecs())
    {
        if (cmdName == spec.name)
        {
            if (rootHelp)
            {
                if (spec.usage.length > 0)
                    writeOut(stdout, spec.usage);
                else
                    writeOut(stdout, execUsage);
                return 0;
            }
            return runWithContext(stdout, stderr, ctx, spec.prefix ~ rest);
        }
    }

    switch (cmdName)
    {
        case "init":
            return dispatchInitCommand(stdout, ctx, rest, rootHelp);

        case "env":
            return dispatchEnvCommand(stdout, stderr, ctx, rest, rootHelp);

        case "create-project":
            if (rootHelp || wantsCreateProjectHelp(rest))
            {
                writeOut(stdout, createProjectUsage);
                return 0;
            }
            auto st = ctx.initStore();
            runCreateProject(stdout, stderr, st, rest);
            return 0;

        case "ext":
            return dispatchExtRouter(stdout, stderr, ctx, rest, rootHelp);

        case "cert-install":
            return dispatchCertInstallRouter(stdout, stderr, ctx, rest, rootHelp);

        case "db":
            if (rootHelp || (rest.length == 1 && (rest[0] == "-h" || rest[0] == "--help" || rest[0] == "help")))
            {
                writeOut(stdout, dbUsage);
                return 0;
            }
            auto st = ctx.store();
            return runDB(stdout, stderr, st, rest);

        case "serve":
        case "start":
            return dispatchServeRouter(stdout, stderr, ctx, rest, rootHelp);

        case "stop":
            return dispatchStopRouter(stdout, stderr, ctx, rest, rootHelp);

        case "exec":
            if (rootHelp)
            {
                writeOut(stdout, execUsage);
                return 0;
            }
            auto st = ctx.store();
            return runExec(stdout, stderr, st, rest);

        case "sh":
        case "shell":
            return dispatchShRouter(stdout, stderr, ctx, rest, rootHelp);

        case "logs":
            return dispatchLogsRouter(stdout, stderr, ctx, rest, rootHelp);

        case "status":
        case "info":
            return dispatchStatusRouter(stdout, stderr, ctx, rest, rootHelp);

        case "dashboard":
            return dispatchDashboardRouter(stdout, stderr, ctx, rest, rootHelp);

        case "dispatch":
            auto st = ctx.store();
            return runDispatch(stdout, stderr, rest, st);

        default:
            throw new CliStatusException(
                2,
                format("unknown command %s", quoteGoString(cmdName.strip())),
                true,
                rootUsage
            );
    }
}

private int dispatchInitCommand(
    TextWriter stdout,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(
        rest,
        [FlagSpec("docroot"), FlagSpec("package")],
        ctx,
        1,
        "",
        initialHelp
    );
    if (parsed.wantsHelp)
    {
        writeOut(stdout, initUsage);
        return 0;
    }
    if (parsed.positionals.length > 1)
        throw new CliStatusException(1, "init accepts at most one framework or preset argument");

    InitCommandInput input;
    if (parsed.positionals.length > 0)
        input.preset = parsed.positionals[0].strip();
    input.docroot = parsed.getString("docroot");
    input.packageName = parsed.getString("package");
    input.docrootChanged = parsed.isChanged("docroot");
    input.packageChanged = parsed.isChanged("package");

    InitCommandInput normalized;
    try
    {
        normalized = normalizeInitCommandInput(input);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    auto st = ctx.initStore();
    runInit(stdout, st, normalized);
    return 0;
}

private int dispatchEnvCommand(
    TextWriter stdout,
    TextWriter stderr,
    ref CommandContext ctx,
    const(string)[] args,
    bool initialHelp
) @trusted
{
    bool envHelp = initialHelp;
    size_t subIndex = args.length;

    size_t i = 0;
    while (i < args.length)
    {
        auto arg = args[i];
        if (arg == "--")
        {
            i++;
            subIndex = i;
            break;
        }
        if (arg == "-h" || arg == "--help")
        {
            envHelp = true;
            i++;
            continue;
        }
        if (arg.startsWith("--help="))
        {
            envHelp = parseBoolFlagValue("help", arg["--help=".length .. $], 2, rootUsage);
            i++;
            continue;
        }
        if (arg == "--root")
        {
            if (i + 1 >= args.length)
                throw new CliStatusException(2, "flag needs an argument: --root", true, rootUsage);
            ctx.rootDir = args[i + 1];
            i += 2;
            continue;
        }
        if (arg.startsWith("--root="))
        {
            ctx.rootDir = arg["--root=".length .. $];
            i++;
            continue;
        }
        if (arg.length > 1 && arg[0] == '-')
        {
            if (arg.startsWith("--"))
            {
                auto eq = arg.indexOf('=');
                auto name = eq >= 0 ? arg[2 .. eq] : arg[2 .. $];
                throw new CliStatusException(2, format("unknown flag: --%s", name), true, rootUsage);
            }
            auto ch = arg[1 .. 2];
            throw new CliStatusException(2, format("unknown shorthand flag: '%s' in %s", ch, arg), true, rootUsage);
        }

        subIndex = i;
        break;
    }

    if (subIndex >= args.length)
    {
        writeOut(stdout, envUsage);
        return 0;
    }

    auto subName = args[subIndex];
    auto rest = args[subIndex + 1 .. $];

    switch (subName)
    {
        case "new":
            return dispatchEnvNewCommand(stdout, ctx, rest, envHelp);
        case "config":
            return dispatchEnvConfigCommand(stdout, stderr, ctx, rest, envHelp);
        case "install":
            return dispatchEnvInstallCommand(stdout, stderr, ctx, rest, envHelp);
        case "list":
            return dispatchEnvListCommand(stdout, ctx, rest, envHelp);
        case "use":
            return dispatchEnvUseCommand(stdout, stderr, ctx, rest, envHelp);
        case "remove":
            return dispatchEnvRemoveCommand(stdout, ctx, rest, envHelp);
        default:
            throw new CliStatusException(
                2,
                format("unknown command %s", quoteGoString(subName.strip())),
                true,
                envUsage
            );
    }
}

private int dispatchEnvNewCommand(
    TextWriter stdout,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(
        rest,
        [
            FlagSpec("php"),
            FlagSpec("composer"),
            FlagSpec("nodejs"),
            FlagSpec("db-engine"),
            FlagSpec("db-version"),
            FlagSpec("db-port"),
        ],
        ctx,
        1,
        "",
        initialHelp
    );
    if (parsed.wantsHelp)
    {
        writeOut(stdout, newUsage);
        return 0;
    }
    if (parsed.positionals.length != 1)
        throw new CliStatusException(1, "new requires exactly one environment name");

    NewCommandInput input;
    input.name = parsed.positionals[0].strip();
    input.phpVersion = parsed.getString("php", defaultNewPHPVersion);
    input.composerVersion = parsed.getString("composer", defaultNewComposerVersion);
    input.nodeJSVersion = parsed.getString("nodejs", defaultNewNodeJSVersion);
    input.databaseEngine = parsed.getString("db-engine");
    input.databaseVersion = parsed.getString("db-version");

    if (parsed.isChanged("db-port"))
    {
        auto rawPort = parsed.getString("db-port");
        try
        {
            input.databasePort = to!int(rawPort.strip());
        }
        catch (ConvException)
        {
            throw new CliStatusException(
                1,
                format(`invalid argument %s for "--db-port" flag`, quoteGoString(rawPort))
            );
        }
    }

    if (input.phpVersion.strip().length == 0)
        throw new CliStatusException(1, "--php requires a non-empty value");
    if (input.composerVersion.strip().length == 0)
        throw new CliStatusException(1, "--composer requires a non-empty value");
    if (parsed.isChanged("nodejs") && input.nodeJSVersion.strip().length == 0)
        throw new CliStatusException(1, "--nodejs requires a non-empty value");

    try
    {
        input.database = buildDatabaseInput(
            parsed.isChanged("db-engine"),
            parsed.isChanged("db-version"),
            parsed.isChanged("db-port"),
            input.databaseEngine,
            input.databaseVersion,
            input.databasePort
        );
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    auto st = ctx.store();
    runNew(stdout, st, input);
    return 0;
}

private int dispatchEnvConfigCommand(
    TextWriter stdout,
    TextWriter stderr,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(rest, [FlagSpec("env")], ctx, 1, "", initialHelp);
    if (parsed.wantsHelp)
    {
        writeOut(stdout, configUsage);
        return 0;
    }
    if (parsed.positionals.length != 0 && parsed.positionals.length != 2)
        throw new CliStatusException(1, "config requires a key and value, or no arguments for the interactive form");

    auto st = ctx.store();
    auto envName = parsed.getString("env");
    if (parsed.positionals.length == 0)
    {
        runConfigForm(stdout, stderr, st, envName);
        return 0;
    }

    ConfigCommandInput input;
    input.name = envName;
    input.key = parsed.positionals[0];
    input.value = parsed.positionals[1];
    runConfig(stdout, stderr, st, input);
    return 0;
}

private int dispatchEnvInstallCommand(
    TextWriter stdout,
    TextWriter stderr,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(
        rest,
        [FlagSpec("env"), FlagSpec("force", "", true)],
        ctx,
        1,
        "",
        initialHelp
    );
    if (parsed.wantsHelp)
    {
        writeOut(stdout, installUsage);
        return 0;
    }
    if (parsed.positionals.length > 1)
        throw new CliStatusException(1, "install accepts at most one tool:version argument");

    InstallCommandInput input;
    input.name = parsed.getString("env");
    input.force = parsed.getBool("force");
    if (parsed.positionals.length > 0)
    {
        string tool;
        string ver;
        try
        {
            parseInstallToolVersion(parsed.positionals[0], tool, ver);
        }
        catch (Exception e)
        {
            throw new CliStatusException(1, e.msg);
        }
        input.tool = tool;
        input.version_ = ver;
    }

    auto st = ctx.store();
    runInstall(stdout, stderr, st, input);
    return 0;
}

private int dispatchEnvListCommand(
    TextWriter stdout,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(rest, null, ctx, 1, "", initialHelp);
    if (parsed.wantsHelp)
    {
        writeOut(stdout, listUsage);
        return 0;
    }
    if (parsed.positionals.length != 0)
        throw new CliStatusException(1, "list does not take arguments");

    auto st = ctx.store();
    runList(stdout, st);
    return 0;
}

private int dispatchEnvUseCommand(
    TextWriter stdout,
    TextWriter stderr,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(rest, null, ctx, 1, "", initialHelp);
    if (parsed.wantsHelp)
    {
        writeOut(stdout, useUsage);
        return 0;
    }
    if (parsed.positionals.length != 1)
        throw new CliStatusException(1, "use requires exactly one environment name");

    auto st = ctx.store();
    runUse(stdout, stderr, st, parsed.positionals[0].strip());
    return 0;
}

private int dispatchEnvRemoveCommand(
    TextWriter stdout,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(rest, null, ctx, 1, "", initialHelp);
    if (parsed.wantsHelp)
    {
        writeOut(stdout, removeUsage);
        return 0;
    }
    if (parsed.positionals.length != 1)
        throw new CliStatusException(1, "remove requires exactly one environment name");

    auto st = ctx.store();
    runRemove(stdout, st, parsed.positionals[0].strip());
    return 0;
}

private int dispatchExtRouter(
    TextWriter stdout,
    TextWriter stderr,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(
        rest,
        [
            FlagSpec("env"),
            FlagSpec("configure-option", "", false, true),
            FlagSpec("clear-configure-options", "", true),
        ],
        ctx,
        1,
        "",
        initialHelp
    );
    if (parsed.wantsHelp)
    {
        writeOut(stdout, extUsage);
        return 0;
    }
    if (parsed.positionals.length != 2)
        throw new CliStatusException(1, "ext requires an action and one extension package");

    auto st = ctx.store();
    dispatchExtCommand(
        stdout,
        stderr,
        st,
        parsed.getString("env"),
        parsed.positionals,
        parsed.getMulti("configure-option"),
        parsed.getBool("clear-configure-options")
    );
    return 0;
}

private int dispatchCertInstallRouter(
    TextWriter stdout,
    TextWriter stderr,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(
        rest,
        [FlagSpec("no-encryption", "", true)],
        ctx,
        1,
        "",
        initialHelp
    );
    if (parsed.wantsHelp)
    {
        writeOut(stdout, certInstallUsage);
        return 0;
    }
    if (parsed.positionals.length != 0)
        throw new CliStatusException(1, "cert-install accepts no arguments");

    const(string)[] certArgs = parsed.getBool("no-encryption") ? ["--no-encryption"] : null;
    return executeCertInstallCommand(ctx, stdout, stderr, certArgs);
}

private int dispatchServeRouter(
    TextWriter stdout,
    TextWriter stderr,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(
        rest,
        [FlagSpec("server"), FlagSpec("watch", "", true)],
        ctx,
        1,
        "",
        initialHelp
    );
    if (parsed.wantsHelp)
    {
        writeOut(stdout, startUsage);
        return 0;
    }
    if (parsed.positionals.length > 1)
        throw new CliStatusException(1, "start/serve accepts at most one docroot");

    ServeCommandInput input;
    if (parsed.positionals.length > 0)
        input.docroot = parsed.positionals[0].strip();
    input.server = parsed.getString("server").strip();
    if (parsed.isChanged("server") && input.server.length == 0)
        throw new CliStatusException(1, "--server requires a non-empty value");
    input.watch = parsed.getBool("watch");

    auto st = ctx.store();
    return runStart(stdout, stderr, st, input);
}

private int dispatchStopRouter(
    TextWriter stdout,
    TextWriter stderr,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(rest, null, ctx, 1, "", initialHelp);
    if (parsed.wantsHelp)
    {
        writeOut(stdout, stopUsage);
        return 0;
    }
    if (parsed.positionals.length != 0)
        throw new CliStatusException(1, "stop does not take arguments");

    auto st = ctx.store();
    return runStop(stdout, stderr, st);
}

private int dispatchShRouter(
    TextWriter stdout,
    TextWriter stderr,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(rest, null, ctx, 1, "", initialHelp);
    if (parsed.wantsHelp)
    {
        writeOut(stdout, shUsage);
        return 0;
    }
    if (parsed.positionals.length != 0)
        throw new CliStatusException(1, "sh/shell does not take arguments");

    auto st = ctx.store();
    return runSh(stdout, stderr, st);
}

private int dispatchLogsRouter(
    TextWriter stdout,
    TextWriter stderr,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(
        rest,
        [FlagSpec("level"), FlagSpec("follow", "f", true)],
        ctx,
        1,
        "",
        initialHelp
    );
    if (parsed.wantsHelp)
    {
        writeOut(stdout, logsUsage);
        return 0;
    }
    if (parsed.positionals.length != 1)
        throw new CliStatusException(1, "logs requires exactly one tool name");

    auto st = ctx.store();
    return runLogs(stdout, stderr, st, parsed.positionals[0], parsed.getString("level"), parsed.getBool("follow"));
}

private int dispatchStatusRouter(
    TextWriter stdout,
    TextWriter stderr,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(rest, null, ctx, 1, "", initialHelp);
    if (parsed.wantsHelp)
    {
        writeOut(stdout, statusUsage);
        return 0;
    }
    if (parsed.positionals.length != 0)
        throw new CliStatusException(1, "status/info does not take arguments");

    auto st = ctx.store();
    runStatus(stdout, stderr, st);
    return 0;
}

private int dispatchDashboardRouter(
    TextWriter stdout,
    TextWriter stderr,
    ref CommandContext ctx,
    const(string)[] rest,
    bool initialHelp
) @trusted
{
    auto parsed = parseCommandFlags(rest, null, ctx, 1, "", initialHelp);
    if (parsed.wantsHelp)
    {
        writeOut(stdout, dashboardUsage);
        return 0;
    }
    if (parsed.positionals.length != 0)
        throw new CliStatusException(1, "dashboard does not take arguments");

    auto st = ctx.store();
    runDashboard(stdout, stderr, st);
    return 0;
}

/// Resolves `polka help [topic...]` to the corresponding usage text.
private int runHelpCommand(TextWriter stdout, TextWriter stderr, const(string)[] args) @trusted
{
    // Strip any -h / --help / --root flags passed to `help`.
    string[] topics;
    for (size_t i = 0; i < args.length; i++)
    {
        auto arg = args[i];
        if (arg == "-h" || arg == "--help")
            continue;
        if (arg == "--root")
        {
            if (i + 1 < args.length)
                i++;
            continue;
        }
        if (arg.startsWith("--root="))
            continue;
        topics ~= arg;
    }

    if (topics.length == 0)
    {
        writeOut(stdout, rootUsage);
        return 0;
    }

    switch (topics[0])
    {
        case "init":
            writeOut(stdout, initUsage);
            return 0;
        case "create-project":
            writeOut(stdout, createProjectUsage);
            return 0;
        case "env":
            if (topics.length == 1)
            {
                writeOut(stdout, envUsage);
                return 0;
            }
            switch (topics[1])
            {
                case "new":     writeOut(stdout, newUsage); return 0;
                case "config":  writeOut(stdout, configUsage); return 0;
                case "install": writeOut(stdout, installUsage); return 0;
                case "list":    writeOut(stdout, listUsage); return 0;
                case "use":     writeOut(stdout, useUsage); return 0;
                case "remove":  writeOut(stdout, removeUsage); return 0;
                default:
                    writeOutln(stderr, format("Unknown help topic [`%s`]", topics[1]));
                    writeOut(stdout, envUsage);
                    return 0;
            }
        case "new":          writeOut(stdout, newUsage); return 0;
        case "config":       writeOut(stdout, configUsage); return 0;
        case "install":      writeOut(stdout, installUsage); return 0;
        case "list":         writeOut(stdout, listUsage); return 0;
        case "use":          writeOut(stdout, useUsage); return 0;
        case "remove":       writeOut(stdout, removeUsage); return 0;
        case "ext":          writeOut(stdout, extUsage); return 0;
        case "cert-install": writeOut(stdout, certInstallUsage); return 0;
        case "db":           writeOut(stdout, dbUsage); return 0;
        case "serve":
        case "start":        writeOut(stdout, startUsage); return 0;
        case "stop":         writeOut(stdout, stopUsage); return 0;
        case "exec":
        case "php":
        case "composer":
        case "node":
        case "npm":
        case "drush":        writeOut(stdout, execUsage); return 0;
        case "sh":
        case "shell":        writeOut(stdout, shUsage); return 0;
        case "logs":         writeOut(stdout, logsUsage); return 0;
        case "status":
        case "info":         writeOut(stdout, statusUsage); return 0;
        case "dashboard":    writeOut(stdout, dashboardUsage); return 0;
        default:
            writeOutln(stderr, format("Unknown help topic [`%s`]", topics[0]));
            writeOut(stdout, rootUsage);
            return 0;
    }
}

/// Parses interleaved GNU/`pflag`-style long and short flags along with
/// persistent `--root` and `-h`/`--help`, stopping flag parsing at `--`.
private ParsedFlags parseCommandFlags(
    const(string)[] args,
    const(FlagSpec)[] specs,
    ref CommandContext ctx,
    int errorCode,
    string errorUsage,
    bool initialHelp
) @safe
{
    ParsedFlags result;
    result.wantsHelp = initialHelp;

    for (size_t i = 0; i < args.length; i++)
    {
        auto arg = args[i];
        if (arg == "--")
        {
            if (i + 1 < args.length)
                result.positionals ~= args[i + 1 .. $];
            break;
        }

        if (arg.startsWith("--"))
        {
            auto eq = arg.indexOf('=');
            auto flagName = eq >= 0 ? arg[2 .. eq] : arg[2 .. $];
            bool hasInline = (eq >= 0);
            auto inlineVal = hasInline ? arg[eq + 1 .. $] : "";

            if (flagName == "help")
            {
                bool val = hasInline
                    ? parseBoolFlagValue(flagName, inlineVal, errorCode, errorUsage)
                    : true;
                if (val)
                    result.wantsHelp = true;
                continue;
            }
            if (flagName == "root")
            {
                string val;
                if (hasInline)
                {
                    val = inlineVal;
                }
                else
                {
                    if (i + 1 >= args.length)
                        throw new CliStatusException(errorCode, "flag needs an argument: --root", errorUsage.length > 0, errorUsage);
                    val = args[++i];
                }
                ctx.rootDir = val;
                result.changed["root"] = true;
                continue;
            }

            ptrdiff_t matchedIdx = -1;
            foreach (idx, spec; specs)
            {
                if (spec.name == flagName)
                {
                    matchedIdx = cast(ptrdiff_t) idx;
                    break;
                }
            }
            if (matchedIdx < 0)
            {
                throw new CliStatusException(
                    errorCode,
                    format("unknown flag: --%s", flagName),
                    errorUsage.length > 0,
                    errorUsage
                );
            }

            auto matched = specs[matchedIdx];

            if (matched.isBool)
            {
                bool val = hasInline
                    ? parseBoolFlagValue(matched.name, inlineVal, errorCode, errorUsage)
                    : true;
                result.singleValues[matched.name] = val ? "true" : "false";
                result.changed[matched.name] = true;
            }
            else
            {
                string val;
                if (hasInline)
                {
                    val = inlineVal;
                }
                else
                {
                    if (i + 1 >= args.length)
                    {
                        throw new CliStatusException(
                            errorCode,
                            format("flag needs an argument: --%s", matched.name),
                            errorUsage.length > 0,
                            errorUsage
                        );
                    }
                    val = args[++i];
                }
                if (matched.repeatable)
                    result.multiValues[matched.name] ~= val;
                else
                    result.singleValues[matched.name] = val;
                result.changed[matched.name] = true;
            }
            continue;
        }

        if (arg.length > 1 && arg[0] == '-')
        {
            for (size_t k = 1; k < arg.length; k++)
            {
                auto ch = arg[k .. k + 1];
                if (ch == "h")
                {
                    if (k + 1 < arg.length && arg[k + 1] == '=')
                    {
                        bool val = parseBoolFlagValue("help", arg[k + 2 .. $], errorCode, errorUsage);
                        if (val)
                            result.wantsHelp = true;
                        break;
                    }
                    result.wantsHelp = true;
                    continue;
                }

                ptrdiff_t matchedIdx = -1;
                foreach (idx, spec; specs)
                {
                    if (spec.shorthand == ch)
                    {
                        matchedIdx = cast(ptrdiff_t) idx;
                        break;
                    }
                }
                if (matchedIdx < 0)
                {
                    throw new CliStatusException(
                        errorCode,
                        format("unknown shorthand flag: '%s' in %s", ch, arg),
                        errorUsage.length > 0,
                        errorUsage
                    );
                }

                auto matched = specs[matchedIdx];

                if (matched.isBool)
                {
                    bool val = true;
                    if (k + 1 < arg.length && arg[k + 1] == '=')
                    {
                        val = parseBoolFlagValue(matched.name, arg[k + 2 .. $], errorCode, errorUsage);
                        result.singleValues[matched.name] = val ? "true" : "false";
                        result.changed[matched.name] = true;
                        break;
                    }
                    result.singleValues[matched.name] = "true";
                    result.changed[matched.name] = true;
                }
                else
                {
                    string val;
                    if (k + 1 < arg.length)
                    {
                        val = arg[k + 1] == '=' ? arg[k + 2 .. $] : arg[k + 1 .. $];
                    }
                    else
                    {
                        if (i + 1 >= args.length)
                        {
                            throw new CliStatusException(
                                errorCode,
                                format("flag needs an argument: '%s' in %s", ch, arg),
                                errorUsage.length > 0,
                                errorUsage
                            );
                        }
                        val = args[++i];
                    }
                    if (matched.repeatable)
                        result.multiValues[matched.name] ~= val;
                    else
                        result.singleValues[matched.name] = val;
                    result.changed[matched.name] = true;
                    break;
                }
            }
            continue;
        }

        result.positionals ~= arg;
    }

    return result;
}

private bool parseBoolFlagValue(string name, string raw, int errorCode, string errorUsage) @safe
{
    switch (raw.toLower())
    {
        case "1":
        case "t":
        case "true":
            return true;
        case "0":
        case "f":
        case "false":
            return false;
        default:
            throw new CliStatusException(
                errorCode,
                format(`invalid argument %s for "--%s" flag`, quoteGoString(raw), name),
                errorUsage.length > 0,
                errorUsage
            );
    }
}

version (unittest)
{
    import std.algorithm : canFind, count;
    import std.file : exists, mkdirRecurse, readText, write;
    import std.path : buildPath, dirName;
    import std.process : environment;

    import polka.backend : BufferTextWriter;
    import polka.cli.test_helpers;
    import polka.config : Config, CurrentManifestVersion, Environment;
    import polka.support.fs : createTempDir, currentOS, octal, rmdirRecurseQuiet, setFileMode;

    private void runTestConfigValue(string root, string envName, string key, string value)
    {
        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        auto code = run(stdout, stderr, ["--root", root, "config", "--env", envName, key, value]);
        assert(code == 0, format("run(config %s=%s) code=%d stderr=%s", key, value, code, stderr.text));
    }

    private string[] setupExtTestProject(const(ubyte)[] phpScript)
    {
        auto projectDir = createTempDir("polka-ext-cli-");
        auto root = buildPath(projectDir, ".polka");
        auto cacheDir = buildPath(projectDir, "global-cache");
        environment["POLKA_CACHE_DIR"] = cacheDir;
        auto capturePath = buildPath(projectDir, "php-invocations.log");
        environment["POLKA_TEST_PHP_CAPTURE_PATH"] = capturePath;

        writeCachedPHP(cacheDir, "8.4", phpScript);
        writeCachedPIE(cacheDir, "1", cast(const(ubyte)[]) "pie phar\n");

        Config cfg;
        cfg.version_ = CurrentManifestVersion;
        cfg.root = ".polka";
        Environment demo;
        demo.name = "demo";
        demo.tools["php"] = "8.4";
        cfg.environments["demo"] = demo;
        writeTestConfigFile(projectDir, cfg);
        writeTestActiveEnvironment(root, "demo");

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        auto code = run(stdout, stderr, ["--root", root, "install", "--env", "demo"]);
        assert(code == 0, format("install code=%d stderr=%s", code, stderr.text));
        return [projectDir, root, capturePath];
    }
}

// Root, help, unknown command/flag exit code parity tests.
unittest
{
    auto stdout = new BufferTextWriter();
    auto stderr = new BufferTextWriter();

    assert(run(stdout, stderr, null) == 0);
    assert(stdout.text.canFind("The polka CLI manages isolated PHP virtual environments."));

    stdout.clear();
    stderr.clear();
    assert(run(stdout, stderr, ["--help"]) == 0);
    assert(stdout.text.canFind("Usage:\n  polka [--root PATH]"));

    stdout.clear();
    stderr.clear();
    assert(run(stdout, stderr, ["bogus"]) == 2);
    assert(stderr.text.canFind(`unknown command "bogus"`));
    assert(stderr.text.canFind("polka [--root PATH]"));

    stdout.clear();
    stderr.clear();
    assert(run(stdout, stderr, ["--bogus"]) == 2);
    assert(stderr.text.canFind("unknown flag: --bogus"));

    stdout.clear();
    stderr.clear();
    assert(run(stdout, stderr, ["env"]) == 0);
    assert(stdout.text.canFind("polka env <command>"));

    stdout.clear();
    stderr.clear();
    assert(run(stdout, stderr, ["env", "bogus"]) == 2);
    assert(stderr.text.canFind(`unknown command "bogus"`));

    stdout.clear();
    stderr.clear();
    assert(run(stdout, stderr, ["help", "env", "new"]) == 0);
    assert(stdout.text.canFind("polka new <name>"));

    stdout.clear();
    stderr.clear();
    assert(run(stdout, stderr, ["help", "ext"]) == 0);
    assert(stdout.text.canFind("polka ext install"));
}

// Shortcut expansion tests ported from Go `cli/command_shortcut_test.go`.
unittest
{
    // TestShortcutPHPExpandsToExec
    {
        auto projectDir = createTempDir("polka-sc-php-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");

        Config cfg;
        cfg.version_ = CurrentManifestVersion;
        cfg.root = ".polka";
        Environment demo;
        demo.name = "demo";
        cfg.environments["demo"] = demo;
        writeTestConfigFile(projectDir, cfg);
        writeTestActiveEnvironment(root, "demo");
        writeFakePHPInRoot(root);

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["--root", root, "php", "-v"]) == 0, stderr.text);
        assert(stdout.text.canFind("fake-php") && stdout.text.canFind("-v"));
    }

    // TestShortcutHiddenNodeExpandsToExec
    {
        auto projectDir = createTempDir("polka-sc-node-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");

        Config cfg;
        cfg.version_ = CurrentManifestVersion;
        cfg.root = ".polka";
        Environment demo;
        demo.name = "demo";
        cfg.environments["demo"] = demo;
        writeTestConfigFile(projectDir, cfg);
        writeTestActiveEnvironment(root, "demo");

        mkdirRecurse(buildPath(root, "bin"));
        auto nodePath = buildPath(root, "bin", currentOS() == "windows" ? "node.cmd" : "node");
        write(nodePath, fakePHPScript());
        setFileMode(nodePath, octal!755);

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["--root", root, "node", "polka-shortcut-marker"]) == 0, stderr.text);
        assert(!stderr.text.canFind("unknown command"));
        assert(stdout.text.canFind("polka-shortcut-marker"));
    }

    // TestShortcutEnvInstallMatchesTopLevelInstall
    {
        auto projectDir = createTempDir("polka-sc-inst-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");
        auto cacheDir = buildPath(projectDir, "global-cache");
        auto guard = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);

        writeCachedPHP(cacheDir, "8.4", fakePHPScript());
        runTestConfigValue(root, "demo", "tools.php", "8.4");

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["--root", root, "use", "demo"]) == 0, stderr.text);

        stdout.clear();
        stderr.clear();
        assert(run(stdout, stderr, ["--root", root, "env", "install"]) == 0, stderr.text);
        assert(stdout.text.canFind("Installing demo environment"));
        assert(stdout.text.canFind("Installed 'demo' environment"));
    }

    // TestShortcutRemoveExpandsToEnvRemove
    {
        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["remove"]) == 1);
        assert(!stderr.text.canFind("unknown command"));
        assert(stderr.text.canFind("remove requires exactly one environment name"));
    }
}

// Install command E2E tests ported from Go `cli/command_install_test.go`.
unittest
{
    // TestRunInstallSecondRunReportsUnchanged
    {
        auto projectDir = createTempDir("polka-inst-unch-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");
        auto cacheDir = buildPath(projectDir, "global-cache");
        auto guard = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);

        writeCachedPHP(cacheDir, "8.4", fakePHPScript());
        runTestConfigValue(root, "demo", "tools.php", "8.4");

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["--root", root, "install", "--env", "demo"]) == 0, stderr.text);
        assert(!stdout.text.canFind("(unchanged)"));

        auto sentinel = buildPath(root, "envs", "php", "8.4", "sentinel.txt");
        write(sentinel, "keep\n");

        stdout.clear();
        stderr.clear();
        assert(run(stdout, stderr, ["--root", root, "install", "--env", "demo"]) == 0, stderr.text);
        assert(stdout.text.canFind("php 8.4\t(unchanged)"));
        assert(exists(sentinel));

        stdout.clear();
        stderr.clear();
        assert(run(stdout, stderr, ["--root", root, "install", "--env", "demo", "--force"]) == 0, stderr.text);
        assert(!stdout.text.canFind("(unchanged)"));
        assert(!exists(sentinel));
    }

    // TestRunInstallProvisionsPIEExtensions
    {
        auto projectDir = createTempDir("polka-inst-pie-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");
        auto cacheDir = buildPath(projectDir, "global-cache");
        auto capturePath = buildPath(projectDir, "php-invocations.log");
        auto g1 = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
        auto g2 = EnvVarGuard("POLKA_TEST_PHP_CAPTURE_PATH", capturePath);

        writeCachedPHP(cacheDir, "8.4", fakePIEHostPHPScript(["mbstring"], ["mbstring"]));
        writeCachedPIE(cacheDir, "1", cast(const(ubyte)[]) "pie phar\n");

        Config cfg;
        cfg.version_ = CurrentManifestVersion;
        cfg.root = ".polka";
        Environment demo;
        demo.name = "demo";
        demo.tools["php"] = "8.4";
        demo.phpExtensions["mbstring"] = true;
        demo.pieExtensions["xdebug/xdebug"] = "3.4.1";
        cfg.environments["demo"] = demo;
        writeTestConfigFile(projectDir, cfg);
        writeTestActiveEnvironment(root, "demo");

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["--root", root, "install", "--env", "demo"]) == 0, stderr.text);

        auto invocations = readText(capturePath);
        assert(invocations.canFind("--skip-enable-extension") && invocations.canFind("xdebug/xdebug:3.4.1"));
        assert(invocations.count("--skip-enable-extension") == 1);

        auto phpIni = readText(projectInstalledPHPConfigPath(root, "8.4"));
        assert(phpIni.canFind("zend_extension=xdebug"));
    }

    // TestRunInstallSkipsLoadedPIEExtensions
    {
        auto projectDir = createTempDir("polka-inst-pie-skip-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");
        auto cacheDir = buildPath(projectDir, "global-cache");
        auto capturePath = buildPath(projectDir, "php-invocations.log");
        auto g1 = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);
        auto g2 = EnvVarGuard("POLKA_TEST_PHP_CAPTURE_PATH", capturePath);

        writeCachedPHP(cacheDir, "8.4", fakePIEHostPHPScript(["mbstring"], ["mbstring", "xdebug"]));
        writeCachedPIE(cacheDir, "1", cast(const(ubyte)[]) "pie phar\n");

        Config cfg;
        cfg.version_ = CurrentManifestVersion;
        cfg.root = ".polka";
        Environment demo;
        demo.name = "demo";
        demo.tools["php"] = "8.4";
        demo.pieExtensions["xdebug/xdebug"] = "3.4.1";
        cfg.environments["demo"] = demo;
        writeTestConfigFile(projectDir, cfg);
        writeTestActiveEnvironment(root, "demo");

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["--root", root, "install", "--env", "demo"]) == 0, stderr.text);
        auto invocations = readText(capturePath);
        assert(!invocations.canFind("--skip-enable-extension"));
    }

    // TestRunInstallRejectsInternalOnlyTool & TestRunInstallUnknownToolLeavesConfigUnchanged
    {
        auto projectDir = createTempDir("polka-inst-rej-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");
        auto cacheDir = buildPath(projectDir, "global-cache");
        auto guard = EnvVarGuard("POLKA_CACHE_DIR", cacheDir);

        runTestConfigValue(root, "demo", "tools.php", "8.4");
        auto cfgBefore = readText(buildPath(projectDir, "polka.yaml"));

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["--root", root, "install", "pie:1", "--env", "demo"]) != 0);
        assert(stderr.text.canFind("managed internally"));

        stdout.clear();
        stderr.clear();
        assert(run(stdout, stderr, ["--root", root, "install", "bogus:1.0", "--env", "demo"]) != 0);
        assert(stderr.text.canFind("unsupported tool"));
        assert(readText(buildPath(projectDir, "polka.yaml")) == cfgBefore);
    }
}

// Extension command E2E tests ported from Go `cli/command_ext_test.go`.
unittest
{
    // TestRunExtInstallRecordsExplicitVersion
    {
        auto g1 = EnvVarGuard("POLKA_CACHE_DIR", "", true);
        auto g2 = EnvVarGuard("POLKA_TEST_PHP_CAPTURE_PATH", "", true);
        auto paths = setupExtTestProject(fakePIEHostPHPScript(["mbstring"], ["mbstring"]));
        auto projectDir = paths[0];
        auto root = paths[1];
        auto capturePath = paths[2];
        scope (exit) rmdirRecurseQuiet(projectDir);

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["--root", root, "ext", "install", "xdebug/xdebug:3.4.1", "--env", "demo"]) == 0, stderr.text);
        assert(stdout.text.canFind("Installed xdebug/xdebug 3.4.1 for 'demo' environment"));

        auto invocations = readText(capturePath);
        assert(invocations.canFind("pie.phar") && invocations.canFind("install"));
        assert(invocations.canFind("--with-php-path=") && invocations.canFind("--skip-enable-extension"));
        assert(invocations.canFind("xdebug/xdebug:3.4.1"));

        auto env = readTestEnvironmentConfig(projectDir, "demo");
        assert(env.pieExtensions.get("xdebug/xdebug", "") == "3.4.1");
        assert(readText(projectInstalledPHPConfigPath(root, "8.4")).canFind("zend_extension=xdebug"));
    }

    // TestRunExtInstallParsesVersionFromPIEOutput
    {
        auto g1 = EnvVarGuard("POLKA_CACHE_DIR", "", true);
        auto g2 = EnvVarGuard("POLKA_TEST_PHP_CAPTURE_PATH", "", true);
        auto paths = setupExtTestProject(fakePIEHostPHPScript(["mbstring"], ["mbstring"], "Installed xdebug/xdebug:3.4.2"));
        auto projectDir = paths[0];
        auto root = paths[1];
        scope (exit) rmdirRecurseQuiet(projectDir);

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["--root", root, "ext", "install", "xdebug/xdebug", "--env", "demo"]) == 0, stderr.text);
        auto env = readTestEnvironmentConfig(projectDir, "demo");
        assert(env.pieExtensions.get("xdebug/xdebug", "") == "3.4.2");
    }

    // TestRunExtInstallFallsBackToWildcardVersion
    {
        auto g1 = EnvVarGuard("POLKA_CACHE_DIR", "", true);
        auto g2 = EnvVarGuard("POLKA_TEST_PHP_CAPTURE_PATH", "", true);
        auto paths = setupExtTestProject(fakePIEHostPHPScript(["mbstring"], ["mbstring"]));
        auto projectDir = paths[0];
        auto root = paths[1];
        scope (exit) rmdirRecurseQuiet(projectDir);

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["--root", root, "ext", "install", "acme/demo", "--env", "demo"]) == 0, stderr.text);
        assert(stderr.text.canFind("recording * (latest)"));
        auto env = readTestEnvironmentConfig(projectDir, "demo");
        assert(env.pieExtensions.get("acme/demo", "") == "*");
    }

    // TestRunExtRemoveDeletesEntry
    {
        auto g1 = EnvVarGuard("POLKA_CACHE_DIR", "", true);
        auto g2 = EnvVarGuard("POLKA_TEST_PHP_CAPTURE_PATH", "", true);
        auto paths = setupExtTestProject(fakePIEHostPHPScript(["mbstring"], ["mbstring"]));
        auto projectDir = paths[0];
        auto root = paths[1];
        auto capturePath = paths[2];
        scope (exit) rmdirRecurseQuiet(projectDir);

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["--root", root, "ext", "install", "xdebug/xdebug:3.4.1", "--env", "demo"]) == 0, stderr.text);
        stdout.clear();
        stderr.clear();
        assert(run(stdout, stderr, ["--root", root, "ext", "remove", "xdebug/xdebug", "--env", "demo"]) == 0, stderr.text);
        assert(stdout.text.canFind("Removed xdebug/xdebug from 'demo' environment"));
        assert(readText(capturePath).canFind("uninstall"));

        auto env = readTestEnvironmentConfig(projectDir, "demo");
        assert(("xdebug/xdebug" in env.pieExtensions) is null);
        assert(!readText(projectInstalledPHPConfigPath(root, "8.4")).canFind("extension=xdebug"));
    }

    // TestRunExtRequiresStandalonePHP & TestRunExtRejectsInvalidSpecs
    {
        auto projectDir = createTempDir("polka-ext-err-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");
        auto guard = EnvVarGuard("POLKA_CACHE_DIR", buildPath(projectDir, "global-cache"));

        Config cfg;
        cfg.version_ = CurrentManifestVersion;
        cfg.root = ".polka";
        Environment demo;
        demo.name = "demo";
        demo.tools["frankenphp"] = "1.12";
        cfg.environments["demo"] = demo;
        writeTestConfigFile(projectDir, cfg);
        writeTestActiveEnvironment(root, "demo");

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["--root", root, "ext", "install", "xdebug/xdebug", "--env", "demo"]) != 0);
        assert(stderr.text.canFind("standalone php"));

        foreach (badArgs; [
            ["ext", "install", "xdebug"],
            ["ext", "install", "xdebug/xdebug:"],
            ["ext", "frobnicate", "xdebug/xdebug"],
            ["ext", "remove", "xdebug/xdebug:3.4.1"],
        ])
        {
            stdout.clear();
            stderr.clear();
            assert(run(stdout, stderr, ["--root", root] ~ badArgs) != 0);
        }
    }
}

// Config form and Dashboard CLI non-TTY fallback/arg validation tests.
unittest
{
    // TestRunConfigWithoutArgsRequiresTerminal & TestRunConfigRejectsSingleArgument
    {
        auto projectDir = createTempDir("polka-cfg-cli-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");
        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();

        assert(run(stdout, stderr, ["--root", root, "config"]) == 1);
        assert(stderr.text.canFind("interactive config requires a terminal"));

        stdout.clear();
        stderr.clear();
        assert(run(stdout, stderr, ["--root", root, "config", "tools.php"]) == 1);
        assert(stderr.text.canFind("config requires a key and value, or no arguments for the interactive form"));

        stdout.clear();
        stderr.clear();
        assert(run(stdout, stderr, ["--root", root, "config", "--php", "8.4"]) != 0);
        assert(stderr.text.canFind("unknown flag: --php"));
    }

    // TestRunDashboardFallsBackToStatusWithoutTerminal & TestRunDashboardRejectsArguments
    {
        auto projectDir = createTempDir("polka-dash-cli-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");

        write(buildPath(projectDir, "polka.yaml"), "version: \"0.2\"\nroot: .polka\n");
        write(buildPath(projectDir, "polka.demo.yaml"), "tools:\n  php: \"8.4\"\nserver:\n  hostname: localhost\n  port: 8080\n");
        writeTestActiveEnvironment(root, "demo");

        auto stdout = new BufferTextWriter();
        auto stderr = new BufferTextWriter();
        assert(run(stdout, stderr, ["--root", root, "dashboard"]) == 0, stderr.text);
        assert(stdout.text.canFind("environment demo"));
        assert(stdout.text.canFind("webserver stopped"));

        stdout.clear();
        stderr.clear();
        assert(run(stdout, stderr, ["--root", root, "dashboard", "extra"]) == 1);
        assert(stderr.text.canFind("dashboard does not take arguments"));
    }
}
