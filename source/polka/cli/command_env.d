module polka.cli.command_env;

// Core project/environment lifecycle commands: `init`, `env new`, `env config`,
// `env install`, `env list`, `env use`, `status`/`info`, and `env remove`.

import std.algorithm : sort;
import std.format : format;
import std.string : indexOf, strip, toLower;

import polka.backend :
    DatabaseConfig,
    DefaultComposerVersion,
    DefaultNodeJSVersion,
    DefaultPHPVersion,
    Environment,
    FrameworkPlugin,
    InitOptions,
    InstallOptions,
    InstallProgress,
    InstallRequest,
    InstallResult,
    Store,
    TextWriter,
    writeOutf,
    writeOutln;
import polka.cli.env_helpers :
    defaultEnvironmentName,
    environmentByName,
    labelDatabase,
    labelOrUnset,
    phpCLIListLabel,
    resolveCommandEnvironmentName,
    writePHPCLIRuntimeWarning,
    writePHPMyAdminPostgreSQLWarning;
import polka.cli.hooks : defaultCLIHookRegistry, StatusHookContext;
import polka.cli.serve_runtime : resolveServerEndpoint, serverEndpointURL;
import polka.cli.spinner : InstallSpinner;
import polka.config : validatePIEExtensionPackage;

/// Default tool versions applied by `polka new` / `polka env new`.
enum string defaultNewPHPVersion = DefaultPHPVersion;
enum string defaultNewComposerVersion = DefaultComposerVersion;
enum string defaultNewNodeJSVersion = DefaultNodeJSVersion;

/// Normalized inputs for `polka init [framework|preset]`.
struct InitCommandInput
{
    string preset;
    string docroot;
    string packageName;
    bool docrootChanged = false;
    bool packageChanged = false;
}

/// Resolved options for `polka new <name>`.
struct NewCommandInput
{
    string name;
    string phpVersion = defaultNewPHPVersion;
    string composerVersion = defaultNewComposerVersion;
    string nodeJSVersion = defaultNewNodeJSVersion;
    DatabaseConfig* database;
    string databaseEngine;
    string databaseVersion;
    int databasePort = 0;
}

/// Inputs for `polka config [--env NAME] <key> <value>`.
struct ConfigCommandInput
{
    string name;
    string key;
    string value;
}

/// Inputs for `polka install [tool:version] [--env NAME] [--force]`.
struct InstallCommandInput
{
    string name;
    string tool;
    string version_;
    bool force = false;
}

/// Initializes `.polka`, `polka.yaml` (plain or framework/preset), and managed shims.
void runInit(TextWriter stdout, ref Store store, InitCommandInput input) @trusted
{
    InitOptions options;
    if (input.docrootChanged)
        options.docroot = input.docroot;
    if (input.packageChanged)
        options.packageName = input.packageName;

    if (input.preset.length > 0)
    {
        if (input.packageChanged)
        {
            FrameworkPlugin dummy;
            if (store.frameworkPlugin(input.preset, dummy))
                throw new Exception("--package can only be used with a project preset");
        }
        auto result = store.initWithPresetOptions(input.preset, options);
        writeOutf(stdout, "Initialized Polka %s project at %s with config %s\n", input.preset.toLower(), store.rootDir, store.configFile);
        if (input.preset == "php-extension")
        {
            if (initResultContains(result.scaffoldWritten, "composer.json"))
                writeOutln(stdout, "Wrote composer.json (type: php-ext) for PIE");
            else if (initResultContains(result.scaffoldSkipped, "composer.json"))
                writeOutln(stdout, "Kept existing composer.json");
            writeOutln(stdout, "Next steps: set name/description/license in composer.json, then polka install");
        }
        return;
    }

    store.initWithOptions(options);
    writeOutf(stdout, "Initialized Polka at %s with config %s\n", store.rootDir, store.configFile);
}

/// Normalizes and validates `init` flag/argument combinations.
InitCommandInput normalizeInitCommandInput(InitCommandInput input) @safe
{
    input.preset = input.preset.strip().toLower();
    input.docroot = input.docroot.strip();
    input.packageName = input.packageName.strip().toLower();

    if (input.docrootChanged && input.docroot.length == 0)
        throw new Exception("--docroot requires a non-empty value");
    if (input.packageChanged)
    {
        if (input.packageName.length == 0)
            throw new Exception("--package requires a non-empty value");
        if (input.preset.length == 0)
            throw new Exception("--package requires a project preset");
        validatePIEExtensionPackage(input.packageName);
    }
    return input;
}

private bool initResultContains(const(string)[] paths, string target) @safe pure nothrow
{
    foreach (p; paths)
    {
        if (p == target)
            return true;
    }
    return false;
}

/// Installs either an explicit `tool:version` or all configured tools, PIE/PECL
/// extensions, and MSVC state for an environment, rendering progress via `InstallSpinner`.
void runInstall(TextWriter stdout, TextWriter stderr, ref Store store, InstallCommandInput input) @trusted
{
    string resolvedName;
    bool setCurrent = false;
    resolveCommandEnvironmentName(stdout, store, input.name, "Installing", resolvedName, setCurrent);
    input.name = resolvedName;

    InstallRequest[] requests;
    if (input.tool.length > 0)
    {
        requests = [InstallRequest(input.tool, input.version_)];
    }
    else
    {
        // Determine ordered requests upfront so the spinner can pre-register all rows.
        requests = store.installRequests(input.name);
    }

    auto spinner = new InstallSpinner(stdout, requests.length);
    foreach (req; requests)
        spinner.register(req.tool, req.version_);

    if (input.tool.length == 0)
    {
        // Pre-register PIE/PECL extension rows if config loads; lookup failures surface below.
        try
        {
            auto envPreview = environmentByName(store, input.name);
            foreach (pkg; sortedPIEExtensionPackages(envPreview))
                spinner.register(pkg, envPreview.pieExtensions[pkg]);
            foreach (pkg; sortedPECLExtensionPackages(envPreview))
                spinner.register("pecl:" ~ pkg, envPreview.peclExtensions[pkg].version_);
        }
        catch (Exception) {}
    }

    spinner.printInitialLines();
    spinner.start();

    InstallResult[] results;
    string[] warnings;
    try
    {
        if (input.tool.length > 0)
        {
            auto result = store.installToolWithProgress(
                input.name,
                input.tool,
                input.version_,
                (InstallProgress progress) {
                    spinner.update(progress.tool, progress.version_, progress.stage);
                }
            );
            results = [result];
        }
        else
        {
            InstallOptions options;
            options.force = input.force;
            // Buffer non-fatal warnings until the spinner stops so lines do not tear.
            options.warn = (string message) { warnings ~= message; };
            results = store.installWithProgress(
                input.name,
                options,
                (InstallProgress progress) {
                    spinner.update(progress.tool, progress.version_, progress.stage);
                }
            );
        }
    }
    catch (Exception e)
    {
        spinner.stop();
        throw e;
    }
    spinner.stop();

    if (setCurrent)
        store.use(input.name);

    auto environment = environmentByName(store, input.name);
    writePHPCLIRuntimeWarning(stderr, environment);
    writePHPMyAdminPostgreSQLWarning(stderr, environment);
    foreach (w; warnings)
        writeOutf(stderr, "warning: %s\n", w);

    if (input.tool.length > 0)
        writeOutf(stdout, "Installed %s:%s for '%s' environment\n", input.tool, input.version_, input.name);
    else
        writeOutf(stdout, "Installed '%s' environment\n", input.name);

    foreach (res; results)
    {
        if (res.skipped)
            writeOutf(stdout, "%s %s\t(unchanged)\n", res.tool, res.version_);
        else if (res.downloaded)
            writeOutf(stdout, "%s %s\n", res.tool, res.version_);
        else
            writeOutf(stdout, "%s %s\t(cached)\n", res.tool, res.version_);
    }

    if (input.tool.length == 0)
    {
        auto toolchain = store.msvcToolchain(environment);
        if (toolchain !is null)
            writeOutf(stdout, "msvc %s\t(%s)\n", toolchain.version_, toolchain.displayName);
    }
}

/// Creates a new named environment with default or explicit tool versions.
void runNew(TextWriter stdout, ref Store store, NewCommandInput input) @trusted
{
    auto environment = store.create(
        input.name,
        input.phpVersion,
        input.composerVersion,
        input.nodeJSVersion,
        input.database
    );
    writeOutf(
        stdout,
        "Created %s\tphp=%s\tcomposer=%s\tnodejs=%s\tdb=%s\n",
        environment.name,
        labelOrUnset(environment.toolVersion("php")),
        labelOrUnset(environment.toolVersion("composer")),
        labelOrUnset(environment.toolVersion("nodejs")),
        labelDatabase(environment.database)
    );
}

/// Sets one config key/value pair on the target (or active/default) environment.
void runConfig(TextWriter stdout, TextWriter stderr, ref Store store, ConfigCommandInput input) @trusted
{
    string resolvedName;
    bool setCurrent = false;
    resolveCommandEnvironmentName(stdout, store, input.name, "Configuring", resolvedName, setCurrent);
    input.name = resolvedName;

    auto environment = store.configureValue(input.name, input.key, input.value);
    if (setCurrent)
        store.use(input.name);

    writePHPCLIRuntimeWarning(stderr, environment);
    writePHPMyAdminPostgreSQLWarning(stderr, environment);
    writeOutf(stdout, "Configured %s\t%s=%s\n", environment.name, input.key.strip(), input.value);
}

/// Lists all defined environments with the active one starred (`*`).
void runList(TextWriter stdout, ref Store store) @trusted
{
    auto environments = store.list();
    auto current = store.current();

    if (environments.length == 0)
    {
        writeOutln(stdout, "No environments found. Run `polka config tools.php <version>` to add one.");
        return;
    }

    foreach (env; environments)
    {
        auto marker = (current !is null && current.name == env.name) ? "*" : " ";
        writeOutf(
            stdout,
            "%s %s\tphp=%s\tcomposer=%s\tnodejs=%s\tdb=%s\n",
            marker,
            env.name,
            phpCLIListLabel(env),
            labelOrUnset(env.toolVersion("composer")),
            labelOrUnset(env.toolVersion("nodejs")),
            labelDatabase(env.database)
        );
    }
}

/// Selects `name` as the active project environment and refreshes command shims.
void runUse(TextWriter stdout, TextWriter stderr, ref Store store, string name) @trusted
{
    store.use(name);
    auto current = store.current();
    if (current !is null)
    {
        writePHPCLIRuntimeWarning(stderr, *current);
        writePHPMyAdminPostgreSQLWarning(stderr, *current);
    }
    writeOutf(stdout, "Selected %s\n", name);
}

/// Prints the active environment's configured tools, endpoint URL, and live service statuses.
void runStatus(TextWriter stdout, TextWriter stderr, ref Store store) @trusted
{
    auto current = store.current();
    if (current is null)
    {
        writeOutln(stdout, "No active environment selected.");
        return;
    }

    auto endpoint = resolveServerEndpoint(current.server, "");
    writeOutf(stdout, "environment %s\n", current.name);

    auto hooks = defaultCLIHookRegistry();
    auto ctx = StatusHookContext(stdout, stderr, store, *current);
    hooks.writeConfigStatus(ctx);
    writeOutf(stdout, "server %s\n", serverEndpointURL(endpoint));
    hooks.writeRuntimeStatus(ctx);
}

/// Removes a named environment definition (`polka.<name>.yaml`).
void runRemove(TextWriter stdout, ref Store store, string name) @trusted
{
    store.remove(name);
    writeOutf(stdout, "Removed %s\n", name);
}

/// Returns the environment's PIE-managed extension packages in sorted order.
string[] sortedPIEExtensionPackages(const Environment env) @safe pure
{
    auto pkgs = env.pieExtensions.keys;
    pkgs.sort();
    return pkgs;
}

/// Returns the environment's legacy PECL packages in sorted order.
string[] sortedPECLExtensionPackages(const Environment env) @safe pure
{
    auto pkgs = env.peclExtensions.keys;
    pkgs.sort();
    return pkgs;
}

/// Splits `<tool>:<version>` for `polka install <tool:version>`, rejecting malformed forms.
void parseInstallToolVersion(string value, out string tool, out string version_) @safe
{
    auto trimmed = value.strip();
    auto idx = trimmed.indexOf(':');
    if (idx < 0)
    {
        throw new Exception(
            "install argument must be TOOL:VERSION, for example php:8.4; use --env NAME to select an environment"
        );
    }

    auto rawTool = trimmed[0 .. idx].strip();
    auto rawVer = trimmed[idx + 1 .. $].strip();
    if (rawTool.length == 0 || rawVer.length == 0 || rawVer.indexOf(':') >= 0)
    {
        throw new Exception(
            "install argument must be TOOL:VERSION, for example php:8.4; use --env NAME to select an environment"
        );
    }

    tool = rawTool.toLower();
    version_ = rawVer;
}

/// Validates and builds `DatabaseConfig*` from `polka new --db-*` flags.
DatabaseConfig* buildDatabaseInput(
    bool engineChanged,
    bool versionChanged,
    bool portChanged,
    string engine,
    string version_,
    int port
) @safe
{
    if (!engineChanged && !versionChanged && !portChanged)
        return null;

    if (engineChanged && engine.strip().length == 0)
        throw new Exception("--db-engine requires a non-empty value");
    if (versionChanged && version_.strip().length == 0)
        throw new Exception("--db-version requires a non-empty value");
    if (engineChanged != versionChanged)
        throw new Exception("--db-engine and --db-version must be provided together");
    if (portChanged && (port < 1 || port > 65535))
        throw new Exception("--db-port must be between 1 and 65535");

    return new DatabaseConfig(engine.strip(), version_.strip(), port);
}

unittest
{
    import std.exception : assertThrown;

    string t, v;
    parseInstallToolVersion("php:8.4", t, v);
    assert(t == "php" && v == "8.4");
    assertThrown!Exception(parseInstallToolVersion("php", t, v));
    assertThrown!Exception(parseInstallToolVersion("php:", t, v));
    assertThrown!Exception(parseInstallToolVersion(":8.4", t, v));
    assertThrown!Exception(parseInstallToolVersion("php:8.4:extra", t, v));

    assert(buildDatabaseInput(false, false, false, "", "", 0) is null);
    auto db = buildDatabaseInput(true, true, true, "mysql", "8.4", 3307);
    assert(db !is null && db.engine == "mysql" && db.version_ == "8.4" && db.port == 3307);
    assertThrown!Exception(buildDatabaseInput(true, false, false, "mysql", "", 0));
    assertThrown!Exception(buildDatabaseInput(true, true, true, "mysql", "8.4", 70000));
}
