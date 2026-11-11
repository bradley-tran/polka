module polka.tools.php_extensions;

import core.thread : Thread;
import std.algorithm : sort, startsWith;
import std.array : appender;
import std.file : exists, isDir, write;
import std.format : format;
import std.path : buildPath, dirName, extension, relativePath;
import std.process : Config, pipeProcess, Redirect, wait;
import std.string : endsWith, lastIndexOf, representation, split, strip, toLower;

import polka.config;
import polka.support.fs : currentOS, octal, setFileMode, toSlashPath;
import polka.tools.ids;
import polka.tools.manifest;
import polka.tools.types;

/// Effective generated `php.ini` configuration for an environment.
struct PHPInstallConfig
{
    bool[string] extensions;
    bool[string] zendExtensions;
    string memoryLimit;
    string[string] opcacheConfig;
    string caBundlePath;

    /// Reports whether no `php.ini` content needs to be generated.
    bool isZero() const @safe pure
    {
        return extensions.length == 0
            && zendExtensions.length == 0
            && memoryLimit.strip().length == 0
            && opcacheConfig.length == 0
            && caBundlePath.strip().length == 0;
    }
}

/// Computes all generated `php.ini` settings from `environment`.
PHPInstallConfig effectivePHPConfigForInstall(const Environment environment) @safe
{
    PHPInstallConfig cfg;
    cfg.extensions = effectivePHPExtensionsForInstall(environment);
    cfg.zendExtensions = normalizePHPExtensions(environment.zendExtensions);
    cfg.memoryLimit = normalizePHPMemoryLimit(environment.memoryLimit);
    cfg.opcacheConfig = effectiveOPcacheConfigForInstall(environment);
    return cfg;
}

/// Folds PIE-managed extension packages into the bundled extension toggles unless explicitly overridden.
bool[string] effectivePHPExtensionsForInstall(const Environment environment) @safe pure
{
    auto extensions = normalizePHPExtensions(environment.phpExtensions);
    if (environment.pieExtensions.length == 0)
        return extensions;

    foreach (pkg, _; normalizePIEExtensions(environment.pieExtensions))
    {
        auto mod = pieExtensionModuleName(pkg);
        if (mod.length == 0)
            continue;
        if (!(mod in extensions))
            extensions[mod] = true;
    }
    return extensions;
}

/// Curated extension set enabled on the internally provisioned PHP runtime for Composer & PIE.
static immutable string[] broadInternalPHPExtensions = [
    // TLS + archives (baseline, also forced on below).
    "curl", "openssl", "mbstring", "zip", "bz2",
    // Common web/framework extensions.
    "intl", "gd", "exif", "fileinfo", "ftp", "gettext",
    "gmp", "bcmath", "iconv", "calendar", "soap", "sockets",
    "sodium", "tidy", "xsl", "ldap",
    // Database drivers.
    "sqlite3", "pdo_sqlite", "mysqli", "pdo_mysql", "pgsql", "pdo_pgsql",
];

/// Baseline extensions always forced enabled on Polka's internal PHP runtime.
static immutable string[] internalPHPTLSBaseline = ["curl", "openssl", "mbstring", "zip"];

/// Returns curated internal extensions whose module files exist under `<installDir>/ext`, plus TLS baseline.
bool[string] internalPHPExtensions(string installDir) @trusted
{
    auto extensionDir = buildPath(installDir, "ext");
    bool[string] extensions;
    foreach (name; broadInternalPHPExtensions)
    {
        if (sharedPHPExtensionExists(extensionDir, name))
            extensions[name] = true;
    }
    foreach (name; internalPHPTLSBaseline)
        extensions[name] = true;
    return extensions;
}

private bool sharedPHPExtensionExists(string extensionDir, string name) @trusted
{
    auto fileName = currentOS() == "windows"
        ? "php_" ~ name ~ ".dll"
        : name ~ ".so";
    auto p = buildPath(extensionDir, fileName);
    return exists(p) && !isDir(p);
}

/// Derives the PHP extension module name from a `vendor/name` PIE package identifier.
string pieExtensionModuleName(string pkg) @safe pure
{
    auto name = pkg.strip().toLower();
    auto idx = name.lastIndexOf('/');
    if (idx >= 0)
        name = name[idx + 1 .. $];
    return name;
}

/// Overlays explicit `opcache-config` directives onto the configured preset defaults.
string[string] effectiveOPcacheConfigForInstall(const Environment environment) @safe pure
{
    auto effective = opcachePresetConfig(environment.opcachePreset);
    if (environment.opcacheConfig.length == 0)
        return effective;
    foreach (name, value; environment.opcacheConfig)
        effective[name.strip().toLower()] = value.strip();
    return effective;
}

void configureInstalledPHPExtensions(string envsDir, string ver, const(bool[string]) extensions) @trusted
{
    PHPInstallConfig cfg;
    foreach (k, v; extensions)
        cfg.extensions[k] = v;
    configureInstalledPHPConfig(envsDir, ver, cfg);
}

void configureInstalledPHPConfig(string envsDir, string ver, PHPInstallConfig phpConfig) @trusted
{
    configureInstalledPHPConfigForTool(envsDir, PHP, ver, phpConfig);
}

/// Writes `php.ini` for `tool`/`version` only when `phpConfig` is non-empty.
void configureInstalledPHPConfigForTool(string envsDir, string tool, string ver, PHPInstallConfig phpConfig) @trusted
{
    if (phpConfig.isZero())
        return;
    writeInstalledPHPConfigForTool(envsDir, tool, ver, phpConfig);
}

/// Writes `php.ini` unconditionally (so shrinking config clears previously generated directives).
void writeInstalledPHPConfigForTool(string envsDir, string tool, string ver, PHPInstallConfig phpConfig) @trusted
{
    auto phpPath = resolveBuiltinInstalledTool(envsDir, tool, ver);
    auto phpDir = dirName(phpPath);
    configurePHPConfigAt(
        phpPath,
        buildPath(envsDir, tool, ver, "ext"),
        buildPath(phpDir, "php.ini"),
        phpConfig
    );
}

/// Renders and writes `php.ini` at `phpIniPath` for `phpPath`, skipping compiled-in extensions.
void configurePHPConfigAt(string phpPath, string extensionPath, string phpIniPath, PHPInstallConfig phpConfig) @trusted
{
    auto phpDir = dirName(phpIniPath);
    string extensionDir;
    try
    {
        extensionDir = relativePath(extensionPath, phpDir);
    }
    catch (Exception)
    {
        extensionDir = extensionPath;
    }

    if (phpConfig.extensions.length > 0)
    {
        auto builtInExtensions = installedPHPBuiltInExtensions(phpPath);
        phpConfig.extensions = skipBuiltInPHPExtensions(phpConfig.extensions, builtInExtensions);
    }

    auto configData = renderPHPConfig(toSlashPath(extensionDir), phpConfig);
    write(phpIniPath, configData);
    setFileMode(phpIniPath, octal!644);
}

ubyte[] renderPHPExtensionConfig(string extensionDir, const(bool[string]) extensions) @safe
{
    PHPInstallConfig cfg;
    foreach (k, v; extensions)
        cfg.extensions[k] = v;
    return renderPHPConfig(extensionDir, cfg);
}

/// Queries `php -nm` to discover modules compiled into the binary without reading `php.ini`.
bool[string] installedPHPBuiltInExtensions(string phpPath) @trusted
{
    return listPHPModules(phpPath, ["-nm"], null, "list built-in PHP extensions");
}

/// Queries `php -m` with `PHPRC=<phpDir>/php.ini` when present so PIE modules are included on Linux/macOS too.
bool[string] installedPHPModules(string phpPath) @trusted
{
    string[string] env;
    auto iniPath = buildPath(dirName(phpPath), "php.ini");
    if (exists(iniPath) && !isDir(iniPath))
        env = ["PHPRC": iniPath];
    return listPHPModules(phpPath, ["-m"], env, "list installed PHP modules");
}

private bool[string] listPHPModules(
    string phpPath,
    const(string)[] args,
    const(string[string]) env,
    string label
) @trusted
{
    auto cmdArgs = preparePHPCommandArgs(phpPath, args);
    auto pipes = pipeProcess(cmdArgs, Redirect.stdout | Redirect.stderr, env, Config.none);

    auto stderrBuf = appender!string();
    auto stderrThread = new Thread({
        char[4096] buf;
        while (!pipes.stderr.eof)
        {
            auto chunk = pipes.stderr.rawRead(buf);
            if (chunk.length == 0)
                break;
            stderrBuf.put(chunk);
        }
    });
    stderrThread.start();

    auto stdoutBuf = appender!(ubyte[])();
    ubyte[4096] outChunk;
    while (!pipes.stdout.eof)
    {
        auto chunk = pipes.stdout.rawRead(outChunk);
        if (chunk.length == 0)
            break;
        stdoutBuf.put(chunk);
    }

    stderrThread.join();
    auto exitCode = wait(pipes.pid);
    if (exitCode != 0)
    {
        auto detail = stderrBuf.data.strip();
        if (detail.length > 0)
            throw new Exception(format("%s: exit status %d: %s", label, exitCode, detail));
        throw new Exception(format("%s: exit status %d", label, exitCode));
    }

    return parsePHPModuleList(stdoutBuf.data);
}

/// Wraps `.cmd`/`.bat` scripts on Windows with `cmd.exe /c`.
string[] preparePHPCommandArgs(string target, const(string)[] args) @safe pure
{
    auto trimmed = target.strip();
    if (trimmed.length == 0)
        throw new Exception("php target cannot be empty");

    string[] full;
    if (currentOS() == "windows")
    {
        auto ext = extension(trimmed).toLower();
        if (ext == ".cmd" || ext == ".bat")
        {
            full = ["cmd.exe", "/c", trimmed] ~ args;
            return full;
        }
    }
    full = [trimmed] ~ args;
    return full;
}

/// Parses `php -m` / `php -nm` output lines into normalized lowercase module keys.
bool[string] parsePHPModuleList(const(ubyte)[] output) @trusted pure
{
    bool[string] modules;
    auto text = cast(const(char)[]) output;
    foreach (rawLine; text.split('\n'))
    {
        auto normalized = normalizePHPModuleName(cast(string) rawLine);
        if (normalized.length > 0)
            modules[normalized] = true;
    }
    return modules;
}

/// Normalizes a module line from `php -m` (ignoring `[Section]` headers and stripping `"Zend "` prefix).
string normalizePHPModuleName(string name) @safe pure
{
    auto normalized = name.strip().toLower();
    if (normalized.length == 0 || (normalized.startsWith("[") && normalized.endsWith("]")))
        return "";
    if (normalized.startsWith("zend "))
        normalized = normalized[5 .. $].strip();
    return normalized;
}

/// Filters out extension entries already compiled into PHP statically.
bool[string] skipBuiltInPHPExtensions(
    const(bool[string]) extensions,
    const(bool[string]) builtInExtensions
) @safe pure
{
    if (extensions.length == 0 || builtInExtensions.length == 0)
    {
        bool[string] copyMap;
        foreach (k, v; extensions)
            copyMap[k] = v;
        return copyMap;
    }

    bool[string] filtered;
    foreach (name, enabled; extensions)
    {
        if (auto p = normalizePHPModuleName(name) in builtInExtensions)
        {
            if (*p)
                continue;
        }
        filtered[name] = enabled;
    }
    return filtered;
}

private bool isZendPHPExtension(string name) @safe pure
{
    auto norm = name.strip().toLower();
    return norm == "opcache" || norm == "xdebug";
}

/// Renders deterministic `php.ini` content from `extensionDir` and `phpConfig`.
ubyte[] renderPHPConfig(string extensionDir, const PHPInstallConfig phpConfig) @safe
{
    string[] names;
    names.reserve(phpConfig.extensions.length);
    foreach (name, _; phpConfig.extensions)
    {
        validatePHPExtensionName(name);
        names ~= name;
    }
    names.sort();

    string[] opcacheNames;
    string[string] opcacheConfig;
    opcacheNames.reserve(phpConfig.opcacheConfig.length);
    foreach (name, value; phpConfig.opcacheConfig)
    {
        validateOPcacheDirective(name, value);
        auto norm = name.strip().toLower();
        opcacheNames ~= norm;
        opcacheConfig[norm] = value.strip();
    }
    opcacheNames.sort();

    auto builder = appender!string();
    builder.put("; Generated by Polka. Re-run polka install after editing php-extensions, pecl-extensions, memory-limit, or OPcache config.\n");
    builder.put("[PHP]\n");
    if (names.length > 0)
    {
        builder.put(`extension_dir="`);
        builder.put(extensionDir);
        builder.put("\"\n");
    }

    auto memoryLimit = normalizePHPMemoryLimit(phpConfig.memoryLimit);
    if (memoryLimit.length > 0)
    {
        validatePHPMemoryLimit(memoryLimit);
        builder.put("memory_limit=");
        builder.put(memoryLimit);
        builder.put('\n');
    }

    auto caBundlePath = phpConfig.caBundlePath.strip();
    if (caBundlePath.length > 0)
    {
        auto iniPath = phpINIPathValue(caBundlePath);
        builder.put(`curl.cainfo="`);
        builder.put(iniPath);
        builder.put("\"\n");
        builder.put(`openssl.cafile="`);
        builder.put(iniPath);
        builder.put("\"\n");
    }

    foreach (name; names)
    {
        bool enabled = phpConfig.extensions[name];
        auto zendPtr = name.strip().toLower() in phpConfig.zendExtensions;
        bool isZend = isZendPHPExtension(name) || (zendPtr !is null && *zendPtr);
        if (isZend)
            builder.put(enabled ? "zend_extension=" : ";zend_extension=");
        else
            builder.put(enabled ? "extension=" : ";extension=");
        builder.put(name);
        builder.put('\n');
    }

    if (opcacheNames.length > 0)
    {
        if (names.length > 0)
            builder.put('\n');
        builder.put("[opcache]\n");
        foreach (name; opcacheNames)
        {
            builder.put(name);
            builder.put('=');
            builder.put(opcacheConfig[name]);
            builder.put('\n');
        }
    }

    return builder.data.representation.dup;
}

private string phpINIPathValue(string p) @safe pure
{
    auto trimmed = p.strip();
    if (trimmed.length == 0)
        return "";
    foreach (char c; trimmed)
    {
        if (c == '\r' || c == '\n' || c == '"')
            throw new Exception(format(`invalid PHP ini path "%s": paths cannot contain quotes or newlines`, p));
    }
    return toSlashPath(trimmed);
}

/// Validates an `opcache.*` ini directive key and single-line scalar value.
void validateOPcacheDirective(string name, string value) @safe
{
    auto trimmed = name.strip().toLower();
    if (!trimmed.startsWith("opcache.") || trimmed == "opcache.")
        throw new Exception(format(`invalid OPcache directive "%s": use opcache.* names`, name));
    if (!isValidName(trimmed))
        throw new Exception(format(`invalid OPcache directive "%s": use letters, numbers, dots, dashes, or underscores`, name));
    foreach (char c; value)
    {
        if (c == '\r' || c == '\n')
            throw new Exception(format(`invalid OPcache value for "%s": values must be single-line scalars`, name));
    }
}

unittest
{
    import std.algorithm : canFind;
    import std.file : mkdirRecurse;
    import std.string : indexOf;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    bool phpIniHasLine(string ini, string line)
    {
        foreach (cur; ini.split('\n'))
        {
            if (cur == line)
                return true;
        }
        return false;
    }

    // TestRenderPHPConfigCombinesExtensionsAndOPcache
    {
        PHPInstallConfig cfg;
        cfg.extensions = ["opcache": true, "openssl": false, "zip": true];
        cfg.opcacheConfig = ["OPcache.Validate_Timestamps": "1", "opcache.enable": "1"];
        cfg.memoryLimit = "512m";
        cfg.caBundlePath = "/opt/polka/cacert.pem";

        auto phpIni = cast(string) renderPHPConfig("../ext", cfg);
        foreach (want; [
            `extension_dir="../ext"`,
            "memory_limit=512M",
            `curl.cainfo="/opt/polka/cacert.pem"`,
            `openssl.cafile="/opt/polka/cacert.pem"`,
            ";extension=openssl",
            "zend_extension=opcache",
            "extension=zip",
            "[opcache]",
            "opcache.enable=1",
            "opcache.validate_timestamps=1",
        ])
        {
            assert(phpIni.canFind(want), want);
        }
        assert(phpIni.indexOf(";extension=openssl") < phpIni.indexOf("extension=zip"));
        assert(!phpIniHasLine(phpIni, "extension=opcache") && !phpIniHasLine(phpIni, ";extension=opcache"));
        assert(phpIni.indexOf("opcache.enable=1") < phpIni.indexOf("opcache.validate_timestamps=1"));
    }

    // TestRenderPHPConfigDisablesOPcacheAsZendExtension
    {
        PHPInstallConfig cfg;
        cfg.extensions = ["opcache": false];
        auto phpIni = cast(string) renderPHPConfig("../ext", cfg);
        assert(phpIni.canFind(";zend_extension=opcache"));
        assert(!phpIniHasLine(phpIni, "extension=opcache") && !phpIniHasLine(phpIni, ";extension=opcache"));
    }

    // TestRenderPHPConfigLoadsZendExtensionsWithZendDirective
    {
        PHPInstallConfig cfg;
        cfg.extensions = ["xdebug": true, "apcu": true];
        auto phpIni = cast(string) renderPHPConfig("../ext", cfg);
        assert(phpIniHasLine(phpIni, "zend_extension=xdebug"));
        assert(!phpIniHasLine(phpIni, "extension=xdebug"));
        assert(phpIniHasLine(phpIni, "extension=apcu"));
    }

    // TestRenderMetadataDefinedZendExtension
    {
        PHPInstallConfig cfg;
        cfg.extensions = ["custom_zend": true];
        cfg.zendExtensions = ["custom_zend": true];
        auto phpIni = cast(string) renderPHPConfig("../ext", cfg);
        assert(phpIniHasLine(phpIni, "zend_extension=custom_zend"));
    }

    // TestRenderPHPConfigSupportsOPcacheOnly & MemoryLimitOnly
    {
        PHPInstallConfig opOnly;
        opOnly.opcacheConfig = ["opcache.enable": "1"];
        auto opIni = cast(string) renderPHPConfig("../ext", opOnly);
        assert(!opIni.canFind("extension_dir"));
        assert(opIni.canFind("[opcache]\nopcache.enable=1\n"));

        PHPInstallConfig memOnly;
        memOnly.memoryLimit = "-1";
        auto memIni = cast(string) renderPHPConfig("../ext", memOnly);
        assert(!memIni.canFind("extension_dir") && !memIni.canFind("[opcache]"));
        assert(memIni.canFind("[PHP]\nmemory_limit=-1\n"));
    }

    // TestParsePHPModuleListNormalizesBuiltInExtensions
    {
        auto modules = parsePHPModuleList(cast(const(ubyte)[]) "[PHP Modules]\nCore\nOpenSSL\nzip\n[Zend Modules]\nZend OPcache\n");
        foreach (name; ["core", "openssl", "zip", "opcache"])
            assert(name in modules && modules[name], name);
        assert(!("[php modules]" in modules));
    }

    // TestSkipBuiltInPHPExtensions
    {
        auto filtered = skipBuiltInPHPExtensions(
            ["OpenSSL": true, "opcache": true, "xdebug": false, "zip": true],
            ["openssl": true, "opcache": true]
        );
        assert(!("OpenSSL" in filtered));
        assert(!("opcache" in filtered));
        assert(("zip" in filtered) && filtered["zip"]);
        assert(("xdebug" in filtered) && !filtered["xdebug"]);
    }

    // TestInternalPHPExtensionsFiltersByAvailability
    {
        auto installDir = createTempDir("polka-int-php-");
        scope (exit) rmdirRecurseQuiet(installDir);
        auto extDir = buildPath(installDir, "ext");
        mkdirRecurse(extDir);
        foreach (name; ["intl", "gd", "notcurated"])
        {
            auto fn = currentOS() == "windows" ? "php_" ~ name ~ ".dll" : name ~ ".so";
            write(buildPath(extDir, fn), "module\n");
        }
        auto exts = internalPHPExtensions(installDir);
        assert(exts.get("intl", false) && exts.get("gd", false));
        assert(!exts.get("soap", false));
        assert(!exts.get("notcurated", false));
        foreach (b; ["curl", "openssl", "mbstring", "zip"])
            assert(exts.get(b, false), b);
    }

    // TestPIEExtensionModuleName & TestEffectivePHPExtensionsFoldsPIEModules
    {
        assert(pieExtensionModuleName("xdebug/xdebug") == "xdebug");
        assert(pieExtensionModuleName("Vendor/MyExt") == "myext");
        assert(pieExtensionModuleName("  apcu/apcu   ") == "apcu");
        assert(pieExtensionModuleName("plain") == "plain");

        Environment env;
        env.phpExtensions = ["mbstring": true, "apcu": false];
        env.pieExtensions = ["xdebug/xdebug": "3.4.1", "acme/apcu": "1.0"];
        auto effective = effectivePHPExtensionsForInstall(env);
        assert(effective.get("mbstring", false));
        assert(effective.get("xdebug", false));
        assert(!effective.get("apcu", true));
    }
}
