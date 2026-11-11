module polka.cli.command_ext;

// Implementation of `polka ext <install|remove> <spec>`: routes vendor/name
// specs to PIE and bare legacy specs to PECL, validates configure options,
// parses PIE's resolved installed version, persists extensions into `polka.yaml`,
// and synchronizes the active runtime `php.ini`.

import std.array : appender;
import std.format : format;
import std.regex : matchFirst, regex;
import std.string : indexOf, strip, toLower;

import polka.backend :
    PECLExtensionConfig,
    pieExtensionInstallArgs,
    pieExtensionUninstallArgs,
    primaryPHPTool,
    Store,
    TextWriter,
    writeOutf,
    writeOutln;
import polka.cli.env_helpers : environmentByName, resolveCommandEnvironmentName;
import polka.cli.internal_tools : internalToolProgress;
import polka.cli.types : CliStatusException, quoteGoString;
import polka.config :
    validatePECLExtensionPackage,
    validatePECLExtensionVersion,
    validatePIEExtensionPackage,
    validatePIEExtensionVersion;

/// Extension package provider resolved from `<spec>`.
enum ExtProvider
{
    pie = 0,
    pecl = 1,
}

/// Dispatches `polka ext <install|remove> <spec>`. Throws `CliStatusException`
/// on validation/tool errors matching Go's `statusError` semantics.
void dispatchExtCommand(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    string envName,
    const(string)[] args,
    const(string)[] configureOptions,
    bool clearConfigureOptions
) @trusted
{
    auto action = args[0].strip().toLower();
    switch (action)
    {
        case "install":
            runExtInstall(stdout, stderr, store, envName, args[1], configureOptions, clearConfigureOptions);
            return;
        case "remove":
        case "uninstall":
            if (configureOptions.length > 0 || clearConfigureOptions)
                throw new CliStatusException(1, "configure options are only valid with ext install");
            runExtRemove(stdout, stderr, store, envName, args[1]);
            return;
        default:
            throw new CliStatusException(
                1,
                format("unsupported ext action %s: use install or remove", quoteGoString(args[0]))
            );
    }
}

/// Installs a PIE (`vendor/name[:constraint]`) or legacy PECL extension for an environment.
void runExtInstall(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    string envName,
    string spec,
    const(string)[] optionValues,
    bool clearOptions
) @trusted
{
    ExtProvider provider;
    string pkg;
    string constraint;
    try
    {
        provider = parseExtSpec(spec, pkg, constraint);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    string resolvedName;
    bool setCurrent = false;
    try
    {
        resolveCommandEnvironmentName(stdout, store, envName, "Configuring", resolvedName, setCurrent);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    if (provider == ExtProvider.pecl)
    {
        runPECLExtInstall(stdout, stderr, store, resolvedName, setCurrent, pkg, constraint, optionValues, clearOptions);
        return;
    }
    if (optionValues.length > 0 || clearOptions)
    {
        throw new CliStatusException(
            1,
            format("--configure-option is only supported for legacy PECL packages; PIE package %s does not use it", pkg)
        );
    }

    string projectPHP;
    try
    {
        projectPHP = resolveExtTargetPHP(store, resolvedName);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    // Tee PIE output so we can parse the installed version while streaming live output.
    final class TeeWriter : TextWriter
    {
        private TextWriter primary;
        private typeof(appender!string()) buf;

        this(TextWriter primary)
        {
            this.primary = primary;
            this.buf = appender!string();
        }

        override void write(const(char)[] chunk) @trusted
        {
            if (primary !is null)
                primary.write(chunk);
            buf.put(chunk);
        }

        string text() const @safe pure
        {
            return buf.data;
        }
    }

    auto outTee = new TeeWriter(stdout);
    auto errTee = new TeeWriter(stderr);
    int exitCode = 0;
    try
    {
        exitCode = store.runPIE(
            outTee,
            errTee,
            pieExtensionInstallArgs(projectPHP, pkg, constraint),
            internalToolProgress(stdout)
        );
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }
    if (exitCode != 0)
    {
        // PIE already streamed its failure output; propagate its exit status silently.
        throw new CliStatusException(exitCode, "", false, "", false);
    }

    auto combinedOutput = outTee.text() ~ errTee.text();
    auto version_ = constraint;
    if (version_.length == 0)
        version_ = parsePIEInstalledVersion(combinedOutput, pkg);
    if (version_.length == 0)
    {
        version_ = "*";
        writeOutf(
            stderr,
            "warning: could not determine the installed %s version; recording * (latest). Pin it with polka ext install %s:<version>\n",
            pkg,
            pkg
        );
    }

    try
    {
        store.configureValue(resolvedName, "php-extensions." ~ pkg, version_);
        if (setCurrent)
            store.use(resolvedName);
        store.syncPHPRuntimeConfig(resolvedName);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    writeOutf(stdout, "Installed %s %s for '%s' environment\n", pkg, version_, resolvedName);
}

/// Uninstalls a PIE or legacy PECL extension and updates config + `php.ini`.
void runExtRemove(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    string envName,
    string spec
) @trusted
{
    ExtProvider provider;
    string pkg;
    string constraint;
    try
    {
        provider = parseExtSpec(spec, pkg, constraint);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }
    if (constraint.length > 0)
    {
        throw new CliStatusException(
            1,
            format("ext remove takes a package without a version, such as %s", pkg)
        );
    }

    string resolvedName;
    bool setCurrent = false;
    try
    {
        resolveCommandEnvironmentName(stdout, store, envName, "Configuring", resolvedName, setCurrent);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    if (provider == ExtProvider.pecl)
    {
        runPECLExtRemove(stdout, stderr, store, resolvedName, setCurrent, pkg);
        return;
    }

    string projectPHP;
    try
    {
        projectPHP = resolveExtTargetPHP(store, resolvedName);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    int exitCode = 0;
    try
    {
        exitCode = store.runPIE(
            stdout,
            stderr,
            pieExtensionUninstallArgs(projectPHP, pkg),
            internalToolProgress(stdout)
        );
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }
    if (exitCode != 0)
        throw new CliStatusException(exitCode, "", false, "", false);

    try
    {
        // Empty string deletes the extension key from php-extensions.
        store.configureValue(resolvedName, "php-extensions." ~ pkg, "");
        if (setCurrent)
            store.use(resolvedName);
        store.syncPHPRuntimeConfig(resolvedName);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    writeOutf(stdout, "Removed %s from '%s' environment\n", pkg, resolvedName);
}

/// Legacy PECL installation handler.
void runPECLExtInstall(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    string environmentName,
    bool setCurrent,
    string packageName,
    string version_,
    const(string)[] optionValues,
    bool clearOptions
) @trusted
{
    string[string] options;
    try
    {
        options = parsePECLConfigureOptions(optionValues);
        if (optionValues.length == 0 && !clearOptions)
        {
            auto env = environmentByName(store, environmentName);
            if (auto configured = packageName in env.peclExtensions)
                options = configured.configureOptions.dup;
        }
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    writeOutln(stderr, "warning: PECL support is legacy and discouraged; prefer a PIE vendor/name package when available");

    string installedVersion;
    try
    {
        auto res = store.installPECLExtension(stdout, stderr, environmentName, packageName, version_, options);
        installedVersion = res.version_;
        store.configurePECLExtension(environmentName, packageName, PECLExtensionConfig(installedVersion, options));
        if (setCurrent)
            store.use(environmentName);
        store.syncPHPRuntimeConfig(environmentName);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    writeOutf(stdout, "Installed legacy PECL %s %s for '%s' environment\n", packageName, installedVersion, environmentName);
}

/// Legacy PECL removal handler.
void runPECLExtRemove(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    string environmentName,
    bool setCurrent,
    string packageName
) @trusted
{
    try
    {
        store.removePECLExtension(stderr, environmentName, packageName);
        store.configurePECLExtension(environmentName, packageName, PECLExtensionConfig.init);
        if (setCurrent)
            store.use(environmentName);
        store.syncPHPRuntimeConfig(environmentName);
    }
    catch (Exception e)
    {
        throw new CliStatusException(1, e.msg);
    }

    writeOutf(stdout, "Removed legacy PECL %s from '%s' environment\n", packageName, environmentName);
}

/// Resolves the installed standalone `php` / `php-zts` binary path required by PIE.
string resolveExtTargetPHP(ref Store store, string name) @trusted
{
    auto environment = environmentByName(store, name);
    string tool, ver;
    primaryPHPTool(environment, tool, ver);
    if (tool.length == 0)
    {
        throw new Exception(format(
            "environment %s does not define a standalone php or php-zts runtime; PIE cannot target FrankenPHP's embedded PHP",
            quoteGoString(name)
        ));
    }

    try
    {
        return store.resolveInstalledTool(tool, ver);
    }
    catch (Exception)
    {
        throw new Exception(format(
            "%s %s is not installed for environment %s; run `polka install` first",
            tool,
            ver,
            quoteGoString(name)
        ));
    }
}

/// Splits and validates a PIE `vendor/name[:version]` specification.
void parseExtPackageSpec(string spec, out string pkg, out string constraint) @safe
{
    auto trimmed = spec.strip().toLower();
    pkg = trimmed;
    constraint = "";
    auto idx = trimmed.indexOf(':');
    if (idx >= 0)
    {
        pkg = trimmed[0 .. idx].strip();
        constraint = trimmed[idx + 1 .. $].strip();
        if (constraint.length == 0)
            throw new Exception(format("extension spec %s has an empty version constraint", quoteGoString(spec)));
    }

    validatePIEExtensionPackage(pkg);
    if (constraint.length > 0)
        validatePIEExtensionVersion(pkg, constraint);
}

/// Routes extension specs containing `/` to PIE and bare names to legacy PECL.
ExtProvider parseExtSpec(string spec, out string pkg, out string version_) @safe
{
    auto trimmed = spec.strip();
    auto packagePart = trimmed;
    auto idx = trimmed.indexOf(':');
    if (idx >= 0)
        packagePart = trimmed[0 .. idx];
    if (packagePart.indexOf('/') >= 0)
    {
        parseExtPackageSpec(spec, pkg, version_);
        return ExtProvider.pie;
    }

    parsePECLExtPackageSpec(spec, pkg, version_);
    return ExtProvider.pecl;
}

/// Splits and validates a legacy PECL `name[:version]` specification.
void parsePECLExtPackageSpec(string spec, out string pkg, out string version_) @safe
{
    auto trimmed = spec.strip();
    pkg = trimmed;
    version_ = "";
    auto idx = trimmed.indexOf(':');
    if (idx >= 0)
    {
        pkg = trimmed[0 .. idx].strip();
        version_ = trimmed[idx + 1 .. $].strip();
        if (version_.length == 0)
            throw new Exception(format("extension spec %s has an empty version", quoteGoString(spec)));
    }
    pkg = pkg.toLower();
    validatePECLExtensionPackage(pkg);
    if (version_.length > 0)
        validatePECLExtensionVersion(pkg, version_);
}

/// Parses repeatable `--configure-option NAME=VALUE` flags into a normalized map.
string[string] parsePECLConfigureOptions(const(string)[] values) @safe
{
    if (values.length == 0)
        return null;

    string[string] options;
    foreach (value; values)
    {
        auto eq = value.indexOf('=');
        if (eq < 0)
            throw new Exception(format("configure option %s must use NAME=VALUE", quoteGoString(value)));

        auto rawName = value[0 .. eq].strip();
        while (rawName.length > 0 && rawName[0] == '-')
            rawName = rawName[1 .. $];
        auto name = rawName.toLower();
        if (name.length == 0)
            throw new Exception(format("configure option %s must use NAME=VALUE", quoteGoString(value)));
        if ((name in options) !is null)
            throw new Exception(format("configure option %s was provided more than once", quoteGoString(name)));

        options[name] = value[eq + 1 .. $].strip();
    }
    return options;
}

/// Extracts the resolved version string from PIE install output
/// (e.g. `vendor/name:1.2.3`, `vendor/name 1.2.3`, or `vendor/name@v1.2.3`).
string parsePIEInstalledVersion(string output, string pkg) @trusted
{
    auto escaped = regexEscapeLiteral(pkg);
    auto re = regex(escaped ~ `[:@\s]+v?([0-9][0-9A-Za-z.+-]*)`, "i");
    auto m = matchFirst(output, re);
    if (m.empty || m.length < 2)
        return "";
    return m[1].strip();
}

private string regexEscapeLiteral(string literal) @safe pure
{
    auto buf = appender!string();
    foreach (char c; literal)
    {
        switch (c)
        {
            case '\\':
            case '.':
            case '+':
            case '*':
            case '?':
            case '(':
            case ')':
            case '|':
            case '[':
            case ']':
            case '{':
            case '}':
            case '^':
            case '$':
                buf.put('\\');
                buf.put(c);
                break;
            default:
                buf.put(c);
                break;
        }
    }
    return buf.data;
}

unittest
{
    import std.exception : assertThrown;

    // TestParseExtSpecRoutesProviders
    {
        string pkg, ver;
        assert(parseExtSpec("xdebug/xdebug:^3.4", pkg, ver) == ExtProvider.pie);
        assert(pkg == "xdebug/xdebug" && ver == "^3.4");

        assert(parseExtSpec("redis:6.2.0", pkg, ver) == ExtProvider.pecl);
        assert(pkg == "redis" && ver == "6.2.0");

        assertThrown!Exception(parseExtSpec("xdebug/xdebug:", pkg, ver));
        assertThrown!Exception(parseExtSpec("redis:", pkg, ver));
    }

    // TestParsePECLConfigureOptions
    {
        auto options = parsePECLConfigureOptions(["--with-imagick=/opt/imagemagick", "enable-foo=no"]);
        assert(options["with-imagick"] == "/opt/imagemagick");
        assert(options["enable-foo"] == "no");
        assertThrown!Exception(parsePECLConfigureOptions(["broken"]));
        assertThrown!Exception(parsePECLConfigureOptions(["enable-foo=no", "--enable-foo=yes"]));
    }

    // TestParsePIEInstalledVersion
    {
        assert(parsePIEInstalledVersion("Installed xdebug/xdebug:3.4.2", "xdebug/xdebug") == "3.4.2");
        assert(parsePIEInstalledVersion("Extracting xdebug/xdebug@v3.4.1 (done)", "xdebug/xdebug") == "3.4.1");
        assert(parsePIEInstalledVersion("Installing xdebug/xdebug 3.5.0-beta1", "xdebug/xdebug") == "3.5.0-beta1");
        assert(parsePIEInstalledVersion("nothing matching here", "xdebug/xdebug") == "");
    }
}
