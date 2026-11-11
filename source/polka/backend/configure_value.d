module polka.backend.configure_value;

import std.algorithm : canFind;
import std.array : join, split;
import std.conv : to;
import std.format : format;
import std.string : icmp, strip, toLower;

import polka.backend.aliases;
import polka.backend.database : mergeDatabaseConfig, normalizeDatabaseConfig;
import polka.backend.schema;
import polka.config :
    normalizeEnvironment,
    ServerTypeApache,
    ServerTypeFrankenPHP,
    ServerTypeNginx,
    ServerTypePHP,
    validatePECLExtensionPackage,
    validatePECLExtensionVersion,
    validatePIEExtensionPackage,
    validatePIEExtensionVersion;
import polka.tools : SettingType;

/// Splits a dotted `polka config` key path while preserving dots inside extension package or OPcache directive names.
string[] parseConfigValuePath(string key) @safe
{
    auto trimmed = key.strip();
    if (trimmed.length == 0)
        throw new Exception("config key cannot be empty");

    if (trimmed.length > "opcache-config.".length && trimmed[0 .. "opcache-config.".length] == "opcache-config.")
    {
        auto directive = trimmed["opcache-config.".length .. $].strip();
        if (directive.length == 0)
            throw new Exception(format("config key %s must include an OPcache directive", quoteGoString(key)));
        return ["opcache-config", directive];
    }

    if (trimmed.length > "php-extensions.".length && trimmed[0 .. "php-extensions.".length] == "php-extensions.")
    {
        auto name = trimmed["php-extensions.".length .. $].strip();
        if (name.length == 0)
            throw new Exception(format("config key %s must include a PHP extension name", quoteGoString(key)));
        return ["php-extensions", name];
    }

    if (trimmed.length > "pecl-extensions.".length && trimmed[0 .. "pecl-extensions.".length] == "pecl-extensions.")
    {
        auto name = trimmed["pecl-extensions.".length .. $].strip();
        if (name.length == 0)
            throw new Exception(format("config key %s must include a PECL extension name", quoteGoString(key)));
        return ["pecl-extensions", name];
    }

    auto path = trimmed.split('.');
    foreach (seg; path)
    {
        if (seg.strip().length == 0)
            throw new Exception(format("config key %s contains an empty path segment", quoteGoString(key)));
    }
    return path;
}

/// Applies a validated key/value edit onto `environment`.
void applyConfigValue(ToolRegistry registry, ref Environment environment, const(string)[] path, string value) @trusted
{
    if (path.length == 1)
    {
        applyTopLevelConfigValue(environment, path[0], value);
        return;
    }

    switch (path[0])
    {
        case "tools":
            applyToolConfigValue(registry, environment, path, value);
            break;
        case "database":
            applyDatabaseConfigValue(environment, path, value);
            break;
        case "server":
            applyServerConfigValue(environment, path, value);
            break;
        case "settings":
            applySettingsConfigValue(registry, environment, path, value);
            break;
        case "env-vars":
            applyEnvVarConfigValue(environment, path, value);
            break;
        case "php-extensions":
            applyPHPExtensionConfigValue(environment, path, value);
            break;
        case "pecl-extensions":
            applyPECLExtensionConfigValue(environment, path, value);
            break;
        case "opcache-config":
            applyOPcacheConfigValue(environment, path, value);
            break;
        default:
            throw unsupportedConfigKey(path);
    }
}

private void applyPECLExtensionConfigValue(ref Environment environment, const(string)[] path, string value) @safe
{
    if (path.length != 2)
        throw unsupportedConfigKey(path);

    auto name = path[1].strip().toLower();
    validatePECLExtensionPackage(name);

    auto ver = value.strip();
    if (ver.length == 0)
    {
        if (name in environment.peclExtensions)
            environment.peclExtensions.remove(name);
        return;
    }
    validatePECLExtensionVersion(name, ver);
    environment.peclExtensions[name] = PECLExtensionConfig(ver);
}

private void applyTopLevelConfigValue(ref Environment environment, string key, string value) @safe
{
    switch (key)
    {
        case "framework":
            environment.framework = value.strip();
            break;
        case "docroot":
            environment.docroot = value.strip();
            break;
        case "https":
            environment.https = parseConfigBoolValue(key, value);
            break;
        case "env-file":
            environment.envFile = value.strip();
            break;
        case "memory-limit":
            environment.memoryLimit = value.strip();
            break;
        case "opcache-preset":
            environment.opcachePreset = value.strip();
            break;
        default:
            throw unsupportedConfigKey([key]);
    }
}

private void applyToolConfigValue(ToolRegistry registry, ref Environment environment, const(string)[] path, string value) @trusted
{
    if (path.length != 2)
        throw unsupportedConfigKey(path);
    if (registry is null)
        registry = newDefaultToolRegistry();

    auto tool = path[1].strip().toLower();
    if (tool == toolPIE)
    {
        throw new Exception(
            "pie is managed internally by polka and cannot be configured in a project environment; use 'polka ext' to manage PHP extensions"
        );
    }
    ToolPlugin dummy;
    if (!registry.plugin(tool, dummy))
        throw unsupportedConfigKey(path);
    if (registry.internalOnly(tool))
        throw new Exception(format("%s is managed internally by polka and cannot be configured in a project environment", tool));

    auto ver = value.strip();
    environment.setToolVersion(tool, ver);
    if (ver.length > 0)
    {
        foreach (peer; registry.exclusivePeers(tool))
            environment.setToolVersion(peer, "");
    }
}

private void applyDatabaseConfigValue(ref Environment environment, const(string)[] path, string value) @safe
{
    if (path.length != 2)
        throw unsupportedConfigKey(path);
    if (environment.database is null)
        environment.database = new DatabaseConfig();

    switch (path[1])
    {
        case "engine":
            environment.database.engine = value.strip();
            break;
        case "port":
            environment.database.port = parseConfigPortValue(path.join("."), value);
            break;
        default:
            throw unsupportedConfigKey(path);
    }
}

private void applyServerConfigValue(ref Environment environment, const(string)[] path, string value) @safe
{
    if (path.length != 2)
        throw unsupportedConfigKey(path);
    if (environment.server is null)
        environment.server = new ServerConfig();

    switch (path[1])
    {
        case "type":
            auto typeName = value.strip().toLower();
            switch (typeName)
            {
                case "":
                case ServerTypePHP:
                case ServerTypeNginx:
                case ServerTypeApache:
                case ServerTypeFrankenPHP:
                    environment.server.type = typeName;
                    break;
                default:
                    throw new Exception(format("unsupported server type %s: use php, nginx, apache, or frankenphp",
                        quoteGoString(value)));
            }
            break;
        case "hostname":
            environment.server.hostname = value.strip();
            break;
        case "port":
            environment.server.port = parseConfigPortValue(path.join("."), value);
            break;
        default:
            throw unsupportedConfigKey(path);
    }
}

private void applySettingsConfigValue(ToolRegistry registry, ref Environment environment, const(string)[] path, string value) @trusted
{
    if (path.length != 3)
        throw unsupportedConfigKey(path);
    if (registry is null)
        registry = newDefaultToolRegistry();

    auto namespace = path[1].strip().toLower();
    auto settingKey = path[2].strip().toLower();
    SettingType[string] schema;
    string[] owners;
    if (!registry.settingsSchema(namespace, schema, owners))
        throw unsupportedConfigKey(path);
    auto stPtr = settingKey in schema;
    if (stPtr is null)
        throw unsupportedConfigKey(path);

    final switch (*stPtr)
    {
        case SettingType.bool_:
            environment.setSettingBool(namespace, settingKey, parseConfigBoolValue(path.join("."), value));
            break;
        case SettingType.port:
            environment.setSettingInt(namespace, settingKey, parseConfigPortValue(path.join("."), value));
            break;
        case SettingType.string_:
            environment.setSettingString(namespace, settingKey, value.strip());
            break;
    }
}

private void applyEnvVarConfigValue(ref Environment environment, const(string)[] path, string value) @safe
{
    if (path.length != 2)
        throw unsupportedConfigKey(path);
    auto name = path[1].strip();
    if (name.length == 0)
        throw unsupportedConfigKey(path);
    environment.envVars[name] = value;
}

private void applyPHPExtensionConfigValue(ref Environment environment, const(string)[] path, string value) @safe
{
    if (path.length != 2)
        throw unsupportedConfigKey(path);
    auto name = path[1].strip();
    if (name.length == 0)
        throw unsupportedConfigKey(path);

    if (name.canFind('/'))
    {
        auto normalized = name.toLower();
        validatePIEExtensionPackage(normalized);
        auto ver = value.strip();
        if (ver.length == 0)
        {
            if (normalized in environment.pieExtensions)
                environment.pieExtensions.remove(normalized);
            return;
        }
        validatePIEExtensionVersion(normalized, ver);
        environment.pieExtensions[normalized] = ver;
        return;
    }

    auto enabled = parseConfigBoolValue(path.join("."), value);
    environment.phpExtensions[name] = enabled;
}

private void applyOPcacheConfigValue(ref Environment environment, const(string)[] path, string value) @safe
{
    if (path.length != 2)
        throw unsupportedConfigKey(path);
    auto directive = path[1].strip();
    if (directive.length == 0)
        throw unsupportedConfigKey(path);
    environment.opcacheConfig[directive] = value.strip();
}

/// Normalizes an edited `Environment`, synchronizing inferred database engine and version from tools.
Environment normalizeConfigValueEnvironment(Environment environment) @safe pure
{
    environment = normalizeEnvironment(environment.name, environment);
    if (environment.database is null)
        return environment;

    auto mysqlVersion = environment.toolVersion(toolMySQL).strip();
    auto mariaDBVersion = environment.toolVersion(toolMariaDB).strip();
    auto postgreSQLVersion = environment.toolVersion(toolPostgreSQL).strip();

    auto db = new DatabaseConfig(
        environment.database.engine,
        environment.database.version_,
        environment.database.port
    );
    switch (db.engine.strip().toLower())
    {
        case "":
            if (mysqlVersion.length > 0 && mariaDBVersion.length == 0 && postgreSQLVersion.length == 0)
            {
                db.engine = toolMySQL;
                db.version_ = mysqlVersion;
            }
            else if (mariaDBVersion.length > 0 && mysqlVersion.length == 0 && postgreSQLVersion.length == 0)
            {
                db.engine = toolMariaDB;
                db.version_ = mariaDBVersion;
            }
            else if (postgreSQLVersion.length > 0 && mysqlVersion.length == 0 && mariaDBVersion.length == 0)
            {
                db.engine = toolPostgreSQL;
                db.version_ = postgreSQLVersion;
            }
            break;
        case toolMySQL:
            db.version_ = mysqlVersion;
            break;
        case toolMariaDB:
            db.version_ = mariaDBVersion;
            break;
        case toolPostgreSQL:
            db.version_ = postgreSQLVersion;
            break;
        default:
            break;
    }
    environment.database = normalizeDatabaseConfig(db);
    return normalizeEnvironment(environment.name, environment);
}

int parseConfigPortValue(string key, string value) @safe
{
    int port;
    try
    {
        port = value.strip().to!int;
    }
    catch (Exception)
    {
        throw new Exception(format("%s requires an integer value", key));
    }
    if (port != 0 && (port < 1 || port > 65535))
        throw new Exception(format("%s must be 0 or between 1 and 65535", key));
    return port;
}

bool parseConfigBoolValue(string key, string value) @safe
{
    // Match Go strconv.ParseBool: "1", "t", "T", "TRUE", "true", "True",
    // "0", "f", "F", "FALSE", "false", "False".
    switch (value.strip())
    {
        case "1":
        case "t":
        case "T":
        case "TRUE":
        case "true":
        case "True":
            return true;
        case "0":
        case "f":
        case "F":
        case "FALSE":
        case "false":
        case "False":
            return false;
        default:
            throw new Exception(format("%s requires a boolean value", key));
    }
}

Exception unsupportedConfigKey(const(string)[] path) @safe pure
{
    return new Exception(format("unsupported config key %s", quoteGoString(path.join("."))));
}

unittest
{
    assert(parseConfigValuePath("php-extensions.vendor/my.ext") == ["php-extensions", "vendor/my.ext"]);
    assert(parseConfigValuePath("opcache-config.opcache.enable") == ["opcache-config", "opcache.enable"]);
    assert(parseConfigValuePath("settings.php.extension-sdk") == ["settings", "php", "extension-sdk"]);
    assert(parseConfigBoolValue("k", "true") && !parseConfigBoolValue("k", "0"));
    bool badBool = false;
    try { parseConfigBoolValue("k", "maybe"); } catch (Exception e) { badBool = e.msg.canFind("requires a boolean value"); }
    assert(badBool);
    assert(parseConfigPortValue("database.port", "3306") == 3306);
}
