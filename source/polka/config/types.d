module polka.config.types;

import std.algorithm : canFind, sort;
import std.conv : to;
import std.format : format;
import std.regex : matchFirst, regex;
import std.string : icmp, strip, toLower, toUpper;

import polka.config.manifest_version;
import polka.config.workers;
import polka.support.yaml;

enum string OPcachePresetNone = "none";
enum string OPcachePresetDev = "dev";
enum string OPcachePresetProduction = "production";
enum string ServerTypePHP = "php";
enum string ServerTypeNginx = "nginx";
enum string ServerTypeApache = "apache";
enum string ServerTypeFrankenPHP = "frankenphp";

alias ToolsConfig = string[string];
alias SettingsConfig = YamlValue[string][string];

struct DatabaseConfig
{
    string engine;
    string version_;
    int port = 0;
}

struct MailpitConfig
{
    string version_;
    int smtpPort = 0;
    int uiPort = 0;
    bool https = false;
}

struct PHPMyAdminConfig
{
    string version_;
    int port = 0;
    bool https = false;
}

struct MeilisearchConfig
{
    string version_;
    int port = 0;
    string masterKey;
}

struct RedisConfig
{
    string version_;
    int port = 0;
    string password;
}

struct RabbitMQConfig
{
    string version_;
    int port = 0;
    int managementPort = 0;
    string username;
    string password;
}

struct TraefikConfig
{
    string version_;
    int port = 0;
}

struct ServerConfig
{
    string type;
    string hostname;
    int port = 0;
    bool https = false;
}

struct PECLExtensionConfig
{
    string version_;
    string[string] configureOptions;

    bool opEquals(const PECLExtensionConfig other) const @safe pure nothrow
    {
        if (version_ != other.version_ || configureOptions.length != other.configureOptions.length)
            return false;
        foreach (k, v; configureOptions)
        {
            auto p = k in other.configureOptions;
            if (p is null || *p != v)
                return false;
        }
        return true;
    }
}

struct ProjectFile
{
    string version_;
    string root;
    string framework;
    ToolsConfig tools;
    SettingsConfig settings;
    string docroot;
    bool https = false;
    string envFile;
    string[string] envVars;
    DatabaseConfig* database;
    YamlValue memoryLimit;
    YamlValue[string] phpExtensions;
    YamlValue[string] peclExtensions;
    string opcachePreset;
    YamlValue[string] opcacheConfig;
    ServerConfig* server;
    WorkerConfig[string] workers;
}

struct EnvironmentFile
{
    string framework;
    ToolsConfig tools;
    SettingsConfig settings;
    string docroot;
    bool https = false;
    string envFile;
    string[string] envVars;
    DatabaseConfig* database;
    YamlValue memoryLimit;
    YamlValue[string] phpExtensions;
    YamlValue[string] peclExtensions;
    string opcachePreset;
    YamlValue[string] opcacheConfig;
    ServerConfig* server;
    WorkerConfig[string] workers;
}

struct Environment
{
    string name;
    string framework;
    ToolsConfig tools;
    SettingsConfig settings;
    string docroot;
    bool https = false;
    string envFile;
    string[string] envVars;
    DatabaseConfig* database;
    string memoryLimit;
    bool[string] phpExtensions;
    string[string] pieExtensions;
    PECLExtensionConfig[string] peclExtensions;
    bool[string] zendExtensions;
    string opcachePreset;
    string[string] opcacheConfig;
    ServerConfig* server;
    WorkerConfig[string] workers;

    string toolVersion(string tool) const @safe pure
    {
        if (tools.length == 0)
            return "";
        auto key = tool.strip().toLower();
        if (key.length == 0)
            return "";
        if (auto p = key in tools)
            return (*p).strip();
        foreach (rawKey, ver; tools)
        {
            if (icmp(rawKey.strip(), key) == 0)
                return ver.strip();
        }
        return "";
    }

    void setToolVersion(string tool, string ver) @safe pure
    {
        auto key = tool.strip().toLower();
        if (key.length == 0)
            return;
        auto trimmed = ver.strip();
        if (trimmed.length == 0)
        {
            if (tools.length == 0)
                return;
            tools.remove(key);
            string[] toRemove;
            foreach (rawKey, _; tools)
            {
                if (icmp(rawKey.strip(), key) == 0)
                    toRemove ~= rawKey;
            }
            foreach (rk; toRemove)
                tools.remove(rk);
            if (tools.length == 0)
                tools = null;
            return;
        }
        string[] stale;
        foreach (rawKey, _; tools)
        {
            if (rawKey != key && icmp(rawKey.strip(), key) == 0)
                stale ~= rawKey;
        }
        foreach (sk; stale)
            tools.remove(sk);
        tools[key] = trimmed;
    }

    bool hasSetting(string namespace, string key, out YamlValue val) const @safe pure
    {
        if (settings.length == 0)
            return false;
        auto ns = namespace.strip().toLower();
        auto k = key.strip().toLower();
        if (ns.length == 0 || k.length == 0)
            return false;
        if (auto nsMap = ns in settings)
        {
            if (auto v = k in *nsMap)
            {
                val = v.clone();
                return true;
            }
            foreach (rawKey, v; *nsMap)
            {
                if (icmp(rawKey.strip(), k) == 0)
                {
                    val = v.clone();
                    return true;
                }
            }
            return false;
        }
        foreach (rawNS, nsMap; settings)
        {
            if (icmp(rawNS.strip(), ns) != 0)
                continue;
            if (auto v = k in nsMap)
            {
                val = v.clone();
                return true;
            }
            foreach (rawKey, v; nsMap)
            {
                if (icmp(rawKey.strip(), k) == 0)
                {
                    val = v.clone();
                    return true;
                }
            }
        }
        return false;
    }

    string settingString(string namespace, string key) const @safe pure
    {
        YamlValue val;
        if (!hasSetting(namespace, key, val) || val.isNull())
            return "";
        if (val.isScalar())
            return val.asScalarText().strip();
        return "";
    }

    int settingInt(string namespace, string key) const @safe pure
    {
        YamlValue val;
        if (!hasSetting(namespace, key, val) || val.isNull())
            return 0;
        if (val.kind == YamlKind.int_)
            return cast(int) val.intVal;
        return 0;
    }

    bool settingBool(string namespace, string key) const @safe pure
    {
        YamlValue val;
        if (!hasSetting(namespace, key, val) || val.isNull())
            return false;
        if (val.kind == YamlKind.bool_)
            return val.boolVal;
        return false;
    }

    void setSetting(string namespace, string key, YamlValue value) @safe pure
    {
        auto ns = namespace.strip().toLower();
        auto k = key.strip().toLower();
        if (ns.length == 0 || k.length == 0)
            return;
        YamlValue normalized;
        if (!normalizeSettingValue(value, normalized))
        {
            if (settings.length == 0)
                return;
            if (auto nsMap = ns in settings)
            {
                (*nsMap).remove(k);
                string[] staleKeys;
                foreach (rk, _; *nsMap)
                {
                    if (icmp(rk.strip(), k) == 0)
                        staleKeys ~= rk;
                }
                foreach (sk; staleKeys)
                    (*nsMap).remove(sk);
                if (nsMap.length == 0)
                    settings.remove(ns);
            }
            if (settings.length == 0)
                settings = null;
            return;
        }
        if (ns !in settings)
            settings[ns] = null;
        string[] stale;
        foreach (rk, _; settings[ns])
        {
            if (rk != k && icmp(rk.strip(), k) == 0)
                stale ~= rk;
        }
        foreach (sk; stale)
            settings[ns].remove(sk);
        settings[ns][k] = normalized;
    }

    void setSettingBool(string namespace, string key, bool enabled) @safe pure
    {
        setSetting(namespace, key, YamlValue.fromBool(enabled));
    }

    void setSettingInt(string namespace, string key, int v) @safe pure
    {
        setSetting(namespace, key, YamlValue.fromInt(v));
    }

    void setSettingString(string namespace, string key, string s) @safe pure
    {
        setSetting(namespace, key, YamlValue.fromString(s));
    }

    bool phpBuildTools() const @safe pure
    {
        return settingBool("php", "extension-sdk");
    }

    MailpitConfig* mailpit() const @safe pure
    {
        auto ver = toolVersion("mailpit");
        auto smtpPort = settingInt("mailpit", "smtp-port");
        auto uiPort = settingInt("mailpit", "ui-port");
        if (ver.length == 0 && smtpPort == 0 && uiPort == 0)
            return null;
        return new MailpitConfig(ver, smtpPort, uiPort, https);
    }

    PHPMyAdminConfig* phpMyAdmin() const @safe pure
    {
        auto ver = toolVersion("phpmyadmin");
        auto port = settingInt("phpmyadmin", "port");
        if (ver.length == 0 && port == 0)
            return null;
        return new PHPMyAdminConfig(ver, port, https);
    }

    MeilisearchConfig* meilisearch() const @safe pure
    {
        auto ver = toolVersion("meilisearch");
        auto port = settingInt("meilisearch", "port");
        auto masterKey = settingString("meilisearch", "master-key");
        if (ver.length == 0 && port == 0 && masterKey.length == 0)
            return null;
        return new MeilisearchConfig(ver, port, masterKey);
    }

    RedisConfig* redis() const @safe pure
    {
        auto ver = toolVersion("redis");
        auto port = settingInt("redis", "port");
        auto password = settingString("redis", "password");
        if (ver.length == 0 && port == 0 && password.length == 0)
            return null;
        return new RedisConfig(ver, port, password);
    }

    RabbitMQConfig* rabbitMQ() const @safe pure
    {
        auto ver = toolVersion("rabbitmq");
        auto port = settingInt("rabbitmq", "port");
        auto mgmtPort = settingInt("rabbitmq", "management-port");
        auto user = settingString("rabbitmq", "username");
        auto pass = settingString("rabbitmq", "password");
        if (ver.length == 0 && port == 0 && mgmtPort == 0 && user.length == 0 && pass.length == 0)
            return null;
        return new RabbitMQConfig(ver, port, mgmtPort, user, pass);
    }

    TraefikConfig* traefik() const @safe pure
    {
        auto ver = toolVersion("traefik");
        auto port = settingInt("traefik", "port");
        if (ver.length == 0 && port == 0)
            return null;
        return new TraefikConfig(ver, port);
    }
}

struct Config
{
    string version_;
    string root;
    Environment[string] environments;
}

// ============================================================================
// PHP Provider Helpers
// ============================================================================

void primaryPHPTool(const Environment env, out string tool, out string ver) @safe pure
{
    ver = env.toolVersion("php-zts");
    if (ver.length > 0)
    {
        tool = "php-zts";
        return;
    }
    ver = env.toolVersion("php");
    if (ver.length > 0)
    {
        tool = "php";
        return;
    }
    tool = "";
    ver = "";
}

string primaryPHPVersion(const Environment env) @safe pure
{
    string tool, ver;
    primaryPHPTool(env, tool, ver);
    return ver;
}

void phpCLIProvider(const Environment env, out string tool, out string ver) @safe pure
{
    primaryPHPTool(env, tool, ver);
    if (tool.length > 0)
        return;
    ver = env.toolVersion(ServerTypeFrankenPHP);
    if (ver.length > 0)
    {
        tool = ServerTypeFrankenPHP;
        return;
    }
    tool = "";
    ver = "";
}

bool hasPHPCLI(const Environment env) @safe pure
{
    string tool, ver;
    phpCLIProvider(env, tool, ver);
    return tool.length > 0;
}

// ============================================================================
// Conversions Between File Structs and Environment
// ============================================================================

Environment projectFileToEnvironment(string name, const ProjectFile file) @safe
{
    return environmentFromFileParts(
        name,
        file.framework,
        file.tools,
        file.settings,
        file.docroot,
        file.https,
        file.envFile,
        file.envVars,
        file.database,
        file.memoryLimit,
        file.phpExtensions,
        file.peclExtensions,
        file.opcachePreset,
        file.opcacheConfig,
        file.server,
        file.workers
    );
}

ProjectFile projectFileFromEnvironment(string ver, string root, const Environment env) @safe
{
    ProjectFile f;
    f.version_ = ver;
    f.root = root;
    f.framework = env.framework.strip().toLower();
    f.tools = toolsConfigFromEnvironment(env);
    f.settings = settingsConfigFromEnvironment(env);
    f.docroot = env.docroot;
    f.https = env.https;
    f.envFile = env.envFile;
    f.envVars = dupStringMap(env.envVars);
    f.database = databaseRuntimeConfigFromEnvironment(env);
    f.memoryLimit = phpMemoryLimitFileValue(env.memoryLimit);
    f.phpExtensions = phpExtensionsFileValue(env.phpExtensions, env.pieExtensions);
    f.peclExtensions = peclExtensionsFileValue(env.peclExtensions);
    f.opcachePreset = normalizeOPcachePreset(env.opcachePreset);
    f.opcacheConfig = opcacheFileConfigFromEnvironment(env);
    f.server = serverConfigFromEnvironment(env);
    f.workers = normalizeWorkersConfig(env.workers);
    return f;
}

Environment environmentFileToEnvironment(string name, const EnvironmentFile file) @safe
{
    return environmentFromFileParts(
        name,
        file.framework,
        file.tools,
        file.settings,
        file.docroot,
        file.https,
        file.envFile,
        file.envVars,
        file.database,
        file.memoryLimit,
        file.phpExtensions,
        file.peclExtensions,
        file.opcachePreset,
        file.opcacheConfig,
        file.server,
        file.workers
    );
}

EnvironmentFile environmentFileFromEnvironment(const Environment env) @safe
{
    EnvironmentFile f;
    f.framework = env.framework.strip().toLower();
    f.tools = toolsConfigFromEnvironment(env);
    f.settings = settingsConfigFromEnvironment(env);
    f.docroot = env.docroot;
    f.https = env.https;
    f.envFile = env.envFile;
    f.envVars = dupStringMap(env.envVars);
    f.database = databaseRuntimeConfigFromEnvironment(env);
    f.memoryLimit = phpMemoryLimitFileValue(env.memoryLimit);
    f.phpExtensions = phpExtensionsFileValue(env.phpExtensions, env.pieExtensions);
    f.peclExtensions = peclExtensionsFileValue(env.peclExtensions);
    f.opcachePreset = normalizeOPcachePreset(env.opcachePreset);
    f.opcacheConfig = opcacheFileConfigFromEnvironment(env);
    f.server = serverConfigFromEnvironment(env);
    f.workers = normalizeWorkersConfig(env.workers);
    return f;
}

private Environment environmentFromFileParts(
    string name,
    string framework,
    const(ToolsConfig) tools,
    const(SettingsConfig) settings,
    string docroot,
    bool https,
    string envFile,
    const(string[string]) envVars,
    const(DatabaseConfig)* database,
    const YamlValue memoryLimit,
    const(YamlValue[string]) phpExtensions,
    const(YamlValue[string]) peclExtensions,
    string opcachePreset,
    const(YamlValue[string]) opcacheConfig,
    const(ServerConfig)* server,
    const(WorkerConfig[string]) workers
) @safe
{
    bool[string] bundledExts;
    string[string] pieExts;
    splitPHPExtensionsFromYAML(phpExtensions, bundledExts, pieExts);
    auto normalizedTools = normalizeToolsConfig(tools);

    Environment env;
    env.name = name;
    env.framework = framework.strip().toLower();
    env.tools = normalizedTools;
    env.settings = normalizeSettingsConfig(settings);
    env.docroot = docroot;
    env.https = https;
    env.envFile = envFile;
    env.envVars = dupStringMap(envVars);
    if (database !is null)
        env.database = new DatabaseConfig(database.engine, database.version_, database.port);
    env.memoryLimit = normalizePHPMemoryLimit(phpMemoryLimitValueString(memoryLimit));
    env.phpExtensions = bundledExts;
    env.pieExtensions = pieExts;
    env.peclExtensions = peclExtensionsFromYAML(peclExtensions);
    env.opcachePreset = opcachePreset;
    env.opcacheConfig = normalizeOPcacheConfigFromYAML(opcacheConfig);
    if (server !is null)
        env.server = new ServerConfig(server.type, server.hostname, server.port, server.https);
    env.workers = normalizeWorkersConfig(workers);

    if (normalizedTools.length > 0)
        env.database = primaryDatabaseConfigFromTools(normalizedTools, env.database);
    env = populateDatabaseToolVersion(env);
    return inheritEnvironmentHTTPS(env);
}

// ============================================================================
// Normalization Helpers
// ============================================================================

ToolsConfig toolsConfigFromEnvironment(const Environment env) @safe pure
{
    auto populated = populateDatabaseToolVersion(cloneEnvironment(env));
    return normalizeToolsConfig(populated.tools);
}

SettingsConfig settingsConfigFromEnvironment(const Environment env) @safe pure
{
    return normalizeSettingsConfig(env.settings);
}

ServerConfig* serverConfigFromEnvironment(const Environment env) @safe pure
{
    if (env.server is null)
        return null;
    auto srv = new ServerConfig(
        env.server.type.strip().toLower(),
        env.server.hostname.strip(),
        env.server.port,
        false
    );
    if (srv.type.length == 0 && srv.hostname.length == 0 && srv.port == 0)
        return null;
    return srv;
}

ToolsConfig normalizeToolsConfig(const(ToolsConfig) tools) @safe pure
{
    if (tools.length == 0)
        return null;
    ToolsConfig normalized;
    foreach (rawKey, rawVer; tools)
    {
        auto key = rawKey.strip().toLower();
        auto ver = rawVer.strip();
        if (key.length == 0 || ver.length == 0)
            continue;
        normalized[key] = ver;
    }
    return normalized.length == 0 ? null : normalized;
}

SettingsConfig normalizeSettingsConfig(const(SettingsConfig) settings) @safe pure
{
    if (settings.length == 0)
        return null;
    SettingsConfig normalized;
    foreach (rawNS, rawValues; settings)
    {
        auto ns = rawNS.strip().toLower();
        if (ns.length == 0 || rawValues.length == 0)
            continue;
        YamlValue[string] values;
        foreach (rawKey, rawVal; rawValues)
        {
            auto key = rawKey.strip().toLower();
            if (key.length == 0)
                continue;
            YamlValue normVal;
            if (normalizeSettingValue(rawVal, normVal))
                values[key] = normVal;
        }
        if (values.length > 0)
            normalized[ns] = values;
    }
    return normalized.length == 0 ? null : normalized;
}

private bool normalizeSettingValue(const YamlValue val, out YamlValue normalized) @safe pure
{
    if (val.isNull())
        return false;
    if (val.kind == YamlKind.bool_)
    {
        if (!val.boolVal)
            return false;
        normalized = YamlValue.fromBool(true);
        return true;
    }
    if (val.kind == YamlKind.string_)
    {
        auto trimmed = val.strVal.strip();
        if (trimmed.length == 0)
            return false;
        normalized = YamlValue.fromString(trimmed);
        return true;
    }
    if (val.kind == YamlKind.int_)
    {
        if (val.intVal == 0)
            return false;
        normalized = YamlValue.fromInt(val.intVal);
        return true;
    }
    normalized = val.clone();
    return true;
}

Environment populateDatabaseToolVersion(Environment env) @safe pure
{
    if (env.database is null || env.database.version_.strip().length == 0)
        return env;
    auto engine = env.database.engine.strip().toLower();
    if (engine == "mysql" || engine == "mariadb" || engine == "postgresql")
    {
        if (env.toolVersion(engine).length == 0)
            env.setToolVersion(engine, env.database.version_);
    }
    return env;
}

string databaseToolVersion(const Environment env, string engine) @safe pure
{
    auto normalized = engine.strip().toLower();
    if (normalized == "mysql" || normalized == "mariadb" || normalized == "postgresql")
    {
        auto ver = env.toolVersion(normalized);
        if (ver.length > 0)
            return ver;
    }
    else
    {
        return "";
    }
    if (env.database !is null && icmp(env.database.engine.strip(), normalized) == 0)
        return env.database.version_.strip();
    return "";
}

DatabaseConfig* databaseRuntimeConfigFromEnvironment(const Environment env) @safe pure
{
    if (env.database is null)
        return null;
    auto runtime = new DatabaseConfig(env.database.engine, "", env.database.port);
    if (runtime.engine.length == 0 && runtime.port == 0)
        return null;
    return runtime;
}

DatabaseConfig* primaryDatabaseConfigFromTools(const(ToolsConfig) tools, const(DatabaseConfig)* database) @safe pure
{
    auto normalizedTools = normalizeToolsConfig(tools);
    if (normalizedTools.length == 0)
    {
        if (database is null)
            return null;
        return new DatabaseConfig(database.engine, database.version_, database.port);
    }

    auto merged = new DatabaseConfig();
    if (database !is null)
        *merged = *database;

    string mysqlVer = "mysql" in normalizedTools ? normalizedTools["mysql"] : "";
    string mariaVer = "mariadb" in normalizedTools ? normalizedTools["mariadb"] : "";
    string pgVer = "postgresql" in normalizedTools ? normalizedTools["postgresql"] : "";

    if (merged.engine.length == 0)
    {
        if (mysqlVer.length > 0 && mariaVer.length == 0 && pgVer.length == 0)
            merged.engine = "mysql";
        else if (mariaVer.length > 0 && mysqlVer.length == 0 && pgVer.length == 0)
            merged.engine = "mariadb";
        else if (pgVer.length > 0 && mysqlVer.length == 0 && mariaVer.length == 0)
            merged.engine = "postgresql";
    }
    if (merged.version_.length == 0)
    {
        switch (merged.engine.strip().toLower())
        {
            case "mysql":      merged.version_ = mysqlVer; break;
            case "mariadb":    merged.version_ = mariaVer; break;
            case "postgresql": merged.version_ = pgVer; break;
            default: break;
        }
    }
    return normalizeDatabaseConfig(merged);
}

Environment normalizeEnvironment(string name, const Environment env) @safe pure
{
    Environment normalized;
    normalized.name = name;
    normalized.framework = env.framework.strip().toLower();
    normalized.tools = normalizeToolsConfig(env.tools);
    normalized.settings = normalizeSettingsConfig(env.settings);
    normalized.docroot = env.docroot.strip();
    normalized.https = env.https;
    normalized.envFile = env.envFile.strip();
    normalized.envVars = normalizeEnvironmentVariables(env.envVars);
    normalized.database = normalizeDatabaseConfig(env.database);
    normalized.memoryLimit = normalizePHPMemoryLimit(env.memoryLimit);
    normalized.phpExtensions = normalizePHPExtensions(env.phpExtensions);
    normalized.pieExtensions = normalizePIEExtensions(env.pieExtensions);
    normalized.peclExtensions = normalizePECLExtensions(env.peclExtensions);
    normalized.zendExtensions = normalizePHPExtensions(env.zendExtensions);
    normalized.opcachePreset = normalizeOPcachePreset(env.opcachePreset);
    normalized.opcacheConfig = normalizeOPcacheConfig(env.opcacheConfig);
    normalized.server = normalizeServerConfig(env.server);
    normalized.workers = normalizeWorkersConfig(env.workers);

    return populateDatabaseToolVersion(inheritEnvironmentHTTPS(normalized));
}

Environment cloneEnvironment(const Environment env) @safe pure
{
    Environment copy;
    copy.name = env.name;
    copy.framework = env.framework;
    if (env.tools.length > 0)
    {
        foreach (k, v; env.tools)
            copy.tools[k] = v;
    }
    if (env.settings.length > 0)
    {
        foreach (ns, m; env.settings)
        {
            YamlValue[string] sub;
            foreach (k, v; m)
                sub[k] = v.clone();
            copy.settings[ns] = sub;
        }
    }
    copy.docroot = env.docroot;
    copy.https = env.https;
    copy.envFile = env.envFile;
    copy.envVars = dupStringMap(env.envVars);
    if (env.database !is null)
        copy.database = new DatabaseConfig(env.database.engine, env.database.version_, env.database.port);
    copy.memoryLimit = env.memoryLimit;
    if (env.phpExtensions.length > 0)
    {
        foreach (k, v; env.phpExtensions)
            copy.phpExtensions[k] = v;
    }
    copy.pieExtensions = dupStringMap(env.pieExtensions);
    if (env.peclExtensions.length > 0)
    {
        foreach (k, v; env.peclExtensions)
            copy.peclExtensions[k] = PECLExtensionConfig(v.version_, dupStringMap(v.configureOptions));
    }
    if (env.zendExtensions.length > 0)
    {
        foreach (k, v; env.zendExtensions)
            copy.zendExtensions[k] = v;
    }
    copy.opcachePreset = env.opcachePreset;
    copy.opcacheConfig = dupStringMap(env.opcacheConfig);
    if (env.server !is null)
        copy.server = new ServerConfig(env.server.type, env.server.hostname, env.server.port, env.server.https);
    copy.workers = normalizeWorkersConfig(env.workers);
    return copy;
}

private string[string] dupStringMap(const(string[string]) src) @safe pure
{
    if (src.length == 0)
        return null;
    string[string] dst;
    foreach (k, v; src)
        dst[k] = v;
    return dst;
}

Environment inheritEnvironmentHTTPS(Environment env) @safe pure
{
    if (!env.https)
        return env;
    if (env.server is null)
        env.server = new ServerConfig();
    env.server.https = true;
    return env;
}

DatabaseConfig* mergeDatabaseConfig(const(DatabaseConfig)* existing, const(DatabaseConfig)* overrideCfg) @safe pure
{
    if (existing is null && overrideCfg is null)
        return null;
    auto merged = new DatabaseConfig();
    if (existing !is null)
        *merged = *existing;
    if (overrideCfg !is null)
    {
        if (overrideCfg.engine.length > 0)
            merged.engine = overrideCfg.engine;
        if (overrideCfg.version_.length > 0)
            merged.version_ = overrideCfg.version_;
        if (overrideCfg.port != 0)
            merged.port = overrideCfg.port;
    }
    return normalizeDatabaseConfig(merged);
}

DatabaseConfig* normalizeDatabaseConfig(const(DatabaseConfig)* database) @safe pure
{
    if (database is null)
        return null;
    auto normalized = new DatabaseConfig(
        database.engine.strip().toLower(),
        database.version_.strip(),
        database.port
    );
    if (normalized.engine.length == 0 && normalized.version_.length == 0 && normalized.port == 0)
        return null;
    return normalized;
}

ServerConfig* normalizeServerConfig(const(ServerConfig)* server) @safe pure
{
    if (server is null)
        return null;
    auto normalized = new ServerConfig(
        server.type.strip().toLower(),
        server.hostname.strip(),
        server.port,
        server.https
    );
    if (normalized.type.length == 0 && normalized.hostname.length == 0 && normalized.port == 0 && !normalized.https)
        return null;
    return normalized;
}

bool[string] normalizePHPExtensions(const(bool[string]) extensions) @safe pure
{
    if (extensions.length == 0)
        return null;
    bool[string] normalized;
    foreach (name, enabled; extensions)
        normalized[name.strip().toLower()] = enabled;
    return normalized;
}

string[string] normalizePIEExtensions(const(string[string]) extensions) @safe pure
{
    if (extensions.length == 0)
        return null;
    string[string] normalized;
    foreach (name, ver; extensions)
    {
        auto tn = name.strip().toLower();
        auto tv = ver.strip();
        if (tn.length == 0 || tv.length == 0)
            continue;
        normalized[tn] = tv;
    }
    return normalized.length == 0 ? null : normalized;
}

PECLExtensionConfig[string] normalizePECLExtensions(const(PECLExtensionConfig[string]) extensions) @safe pure
{
    if (extensions.length == 0)
        return null;
    PECLExtensionConfig[string] normalized;
    foreach (name, ext; extensions)
    {
        auto pkgName = name.strip().toLower();
        auto ver = ext.version_.strip();
        if (pkgName.length == 0 || ver.length == 0)
            continue;
        string[string] opts;
        foreach (opt, val; ext.configureOptions)
        {
            auto k = opt.strip().toLower();
            while (k.length > 0 && k[0] == '-')
                k = k[1 .. $];
            if (k.length > 0)
                opts[k] = val.strip();
        }
        normalized[pkgName] = PECLExtensionConfig(ver, opts.length == 0 ? null : opts);
    }
    return normalized.length == 0 ? null : normalized;
}

PECLExtensionConfig[string] peclExtensionsFromYAML(const(YamlValue[string]) values) @safe
{
    if (values.length == 0)
        return null;
    PECLExtensionConfig[string] extensions;
    foreach (name, val; values)
    {
        PECLExtensionConfig ext;
        if (val.isMapping())
        {
            ext.version_ = peclExtensionVersionString(val.get("version"));
            auto rawOpts = val.get("configure-options");
            if (rawOpts.isMapping())
            {
                string[string] opts;
                foreach (entry; rawOpts.map)
                {
                    opts[entry.key] = entry.value.isScalar() ? entry.value.asScalarText().strip() : "";
                }
                ext.configureOptions = opts;
            }
        }
        else
        {
            ext.version_ = peclExtensionVersionString(val);
        }
        extensions[name] = ext;
    }
    return normalizePECLExtensions(extensions);
}

YamlValue[string] peclExtensionsFileValue(const(PECLExtensionConfig[string]) extensions) @safe pure
{
    auto normalized = normalizePECLExtensions(extensions);
    if (normalized.length == 0)
        return null;
    YamlValue[string] values;
    foreach (name, ext; normalized)
    {
        if (ext.configureOptions.length == 0)
        {
            values[name] = YamlValue.fromString(ext.version_);
            continue;
        }
        auto obj = YamlValue.fromMapping();
        obj.set("version", YamlValue.fromString(ext.version_));
        auto optsMap = YamlValue.fromMapping();
        string[] optKeys = ext.configureOptions.keys;
        optKeys.sort();
        foreach (ok; optKeys)
            optsMap.set(ok, YamlValue.fromString(ext.configureOptions[ok]));
        obj.set("configure-options", optsMap);
        values[name] = obj;
    }
    return values;
}

private string peclExtensionVersionString(const YamlValue val) @safe pure
{
    if (val.isNull())
        return "*";
    return val.asScalarText().strip();
}

void splitPHPExtensionsFromYAML(
    const(YamlValue[string]) values,
    out bool[string] bundled,
    out string[string] pie
) @safe
{
    if (values.length == 0)
        return;

    foreach (key, val; values)
    {
        auto name = key.strip().toLower();
        if (name.length == 0)
            continue;
        if (name.canFind('/'))
        {
            pie[name] = pieExtensionVersionString(val);
            continue;
        }
        if (val.kind == YamlKind.bool_)
        {
            bundled[name] = val.boolVal;
            continue;
        }
        auto text = val.isNull() ? "" : val.asScalarText().strip();
        bundled[name] = icmp(text, "false") != 0;
    }
}

YamlValue[string] phpExtensionsFileValue(const(bool[string]) bundled, const(string[string]) pie) @safe pure
{
    if (bundled.length == 0 && pie.length == 0)
        return null;
    YamlValue[string] values;
    foreach (name, enabled; normalizePHPExtensions(bundled))
        values[name] = YamlValue.fromBool(enabled);
    foreach (name, ver; normalizePIEExtensions(pie))
        values[name] = YamlValue.fromString(ver);
    return values;
}

private string pieExtensionVersionString(const YamlValue val) @safe pure
{
    if (val.isNull())
        return "*";
    return val.asScalarText().strip();
}

private auto validPIEExtensionPackageRe() @safe
{
    return regex(`^[a-z0-9]([_.-]?[a-z0-9]+)*/[a-z0-9](([_.]|-{1,2})?[a-z0-9]+)*$`);
}

void validatePIEExtensionPackage(string name) @safe
{
    auto trimmed = name.strip().toLower();
    if (trimmed.length == 0)
        throw new Exception("php extension package cannot be empty");
    if (trimmed.matchFirst(validPIEExtensionPackageRe()).empty)
        throw new Exception(format(`invalid php extension package "%s": use the Composer vendor/name shape, such as xdebug/xdebug`, name));
}

void validatePIEExtensionVersion(string pkg, string ver) @safe pure
{
    auto trimmed = ver.strip();
    if (trimmed.length == 0)
        throw new Exception(format("php extension %s requires a version constraint; use * for the latest version", pkg));
    if (icmp(trimmed, "true") == 0 || icmp(trimmed, "false") == 0)
        throw new Exception(format(`invalid version "%s" for php extension %s: use a Composer version constraint such as 1.2 or *`, ver, pkg));
    foreach (char c; trimmed)
    {
        if (c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '"' || c == '\'')
            throw new Exception(format(`invalid version "%s" for php extension %s: constraints must be single-line scalars`, ver, pkg));
    }
}

private auto validPECLExtensionPackageRe() @safe
{
    return regex(`^[a-z0-9][a-z0-9._-]*$`);
}

void validatePECLExtensionPackage(string name) @safe
{
    auto trimmed = name.strip().toLower();
    if (trimmed.length == 0)
        throw new Exception("PECL extension package cannot be empty");
    if (trimmed.matchFirst(validPECLExtensionPackageRe()).empty)
        throw new Exception(format(`invalid PECL extension package "%s": use a bare package name such as redis; prefer a PIE vendor/name package when available`, name));
}

void validatePECLExtensionVersion(string pkg, string ver) @safe pure
{
    auto trimmed = ver.strip();
    if (trimmed.length == 0)
        throw new Exception(format("PECL extension %s requires a version; use * for the latest compatible stable release", pkg));
    if (trimmed == "*")
        return;
    foreach (char c; trimmed)
    {
        if (c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '"' || c == '\''
            || c == '^' || c == '~' || c == '<' || c == '>' || c == ',')
        {
            throw new Exception(format(`invalid PECL version "%s" for %s: use an exact release such as 6.2.0 or *`, ver, pkg));
        }
    }
}

string normalizePHPMemoryLimit(string value) @safe pure
{
    auto normalized = value.strip();
    if (normalized.length < 2)
        return normalized;
    char unit = normalized[$ - 1];
    if (unit == 'k' || unit == 'm' || unit == 'g')
        return normalized[0 .. $ - 1] ~ [cast(char)(unit - 'a' + 'A')];
    return normalized;
}

void validatePHPMemoryLimit(string value) @safe pure
{
    auto normalized = normalizePHPMemoryLimit(value);
    if (normalized.length == 0 || normalized == "-1")
        return;
    auto digits = normalized;
    char unit = normalized[$ - 1];
    if (unit == 'K' || unit == 'M' || unit == 'G')
        digits = normalized[0 .. $ - 1];
    if (digits.length == 0)
        throw new Exception(format(`invalid memory-limit "%s": use -1, bytes, or PHP shorthand such as 512M`, value));
    foreach (char c; digits)
    {
        if (c < '0' || c > '9')
            throw new Exception(format(`invalid memory-limit "%s": use -1, bytes, or PHP shorthand such as 512M`, value));
    }
}

string normalizeOPcachePreset(string preset) @safe pure
{
    auto normalized = preset.strip().toLower();
    if (normalized == OPcachePresetNone)
        return "";
    return normalized;
}

string[string] normalizeOPcacheConfig(const(string[string]) values) @safe pure
{
    if (values.length == 0)
        return null;
    string[string] normalized;
    foreach (k, v; values)
        normalized[k.strip().toLower()] = v.strip();
    return normalized;
}

string[string] normalizeOPcacheConfigFromYAML(const(YamlValue[string]) values) @safe pure
{
    if (values.length == 0)
        return null;
    string[string] normalized;
    foreach (k, v; values)
        normalized[k.strip().toLower()] = v.isNull() ? "" : v.asScalarText().strip();
    return normalized;
}

YamlValue[string] opcacheFileConfigFromEnvironment(const Environment env) @safe pure
{
    if (env.opcacheConfig.length == 0)
        return null;
    auto cfg = normalizeOPcacheConfig(env.opcacheConfig);
    YamlValue[string] values;
    foreach (k, v; cfg)
        values[k] = YamlValue.fromString(v);
    return values;
}

string[string] opcachePresetConfig(string preset) @safe pure
{
    switch (normalizeOPcachePreset(preset))
    {
        case OPcachePresetDev:
            return [
                "opcache.enable": "1",
                "opcache.file_update_protection": "0",
                "opcache.revalidate_freq": "0",
                "opcache.validate_timestamps": "1",
            ];
        case OPcachePresetProduction:
            return [
                "opcache.enable": "1",
                "opcache.file_update_protection": "0",
                "opcache.validate_timestamps": "0",
            ];
        default:
            return null;
    }
}

private string phpMemoryLimitValueString(const YamlValue val) @safe pure
{
    if (val.isNull())
        return "";
    return val.asScalarText().strip();
}

private YamlValue phpMemoryLimitFileValue(string value) @safe pure
{
    auto normalized = normalizePHPMemoryLimit(value);
    if (normalized.length == 0)
        return YamlValue.nullValue();
    return YamlValue.fromString(normalized);
}

// ============================================================================
// YAML Serialization & Deserialization for ProjectFile / EnvironmentFile
// ============================================================================

private YamlValue serializeCommonEnvironmentFields(
    YamlValue root,
    string framework,
    const(ToolsConfig) tools,
    const(SettingsConfig) settings,
    string docroot,
    bool https,
    string envFile,
    const(string[string]) envVars,
    const(DatabaseConfig)* database,
    const YamlValue memoryLimit,
    const(YamlValue[string]) phpExtensions,
    const(YamlValue[string]) peclExtensions,
    string opcachePreset,
    const(YamlValue[string]) opcacheConfig,
    const(ServerConfig)* server,
    const(WorkerConfig[string]) workers
) @safe
{
    if (framework.length > 0)
        root.set("framework", YamlValue.fromString(framework));
    if (tools.length > 0)
    {
        auto m = YamlValue.fromMapping();
        string[] keys = tools.keys;
        keys.sort();
        foreach (k; keys)
            m.set(k, YamlValue.fromString(tools[k]));
        root.set("tools", m);
    }
    if (settings.length > 0)
    {
        auto sm = YamlValue.fromMapping();
        string[] nsKeys = settings.keys;
        nsKeys.sort();
        foreach (ns; nsKeys)
        {
            auto sub = YamlValue.fromMapping();
            string[] keys = settings[ns].keys;
            keys.sort();
            foreach (k; keys)
                sub.set(k, settings[ns][k].clone());
            sm.set(ns, sub);
        }
        root.set("settings", sm);
    }
    if (docroot.length > 0)
        root.set("docroot", YamlValue.fromString(docroot));
    if (https)
        root.set("https", YamlValue.fromBool(true));
    if (envFile.length > 0)
        root.set("env-file", YamlValue.fromString(envFile));
    if (envVars.length > 0)
    {
        auto m = YamlValue.fromMapping();
        string[] keys = envVars.keys;
        keys.sort();
        foreach (k; keys)
            m.set(k, YamlValue.fromString(envVars[k]));
        root.set("env-vars", m);
    }
    if (database !is null && (database.engine.length > 0 || database.version_.length > 0 || database.port != 0))
    {
        auto dm = YamlValue.fromMapping();
        if (database.engine.length > 0)
            dm.set("engine", YamlValue.fromString(database.engine));
        if (database.version_.length > 0)
            dm.set("version", YamlValue.fromString(database.version_));
        if (database.port != 0)
            dm.set("port", YamlValue.fromInt(database.port));
        root.set("database", dm);
    }
    if (!memoryLimit.isNull())
        root.set("memory-limit", memoryLimit.clone());
    if (phpExtensions.length > 0)
    {
        auto m = YamlValue.fromMapping();
        string[] keys = phpExtensions.keys;
        keys.sort();
        foreach (k; keys)
            m.set(k, phpExtensions[k].clone());
        root.set("php-extensions", m);
    }
    if (peclExtensions.length > 0)
    {
        auto m = YamlValue.fromMapping();
        string[] keys = peclExtensions.keys;
        keys.sort();
        foreach (k; keys)
            m.set(k, peclExtensions[k].clone());
        root.set("pecl-extensions", m);
    }
    if (opcachePreset.length > 0)
        root.set("opcache-preset", YamlValue.fromString(opcachePreset));
    if (opcacheConfig.length > 0)
    {
        auto m = YamlValue.fromMapping();
        string[] keys = opcacheConfig.keys;
        keys.sort();
        foreach (k; keys)
            m.set(k, opcacheConfig[k].clone());
        root.set("opcache-config", m);
    }
    if (server !is null && (server.type.length > 0 || server.hostname.length > 0 || server.port != 0 || server.https))
    {
        auto sm = YamlValue.fromMapping();
        if (server.type.length > 0)
            sm.set("type", YamlValue.fromString(server.type));
        if (server.hostname.length > 0)
            sm.set("hostname", YamlValue.fromString(server.hostname));
        if (server.port != 0)
            sm.set("port", YamlValue.fromInt(server.port));
        if (server.https)
            sm.set("https", YamlValue.fromBool(true));
        root.set("server", sm);
    }
    if (workers.length > 0)
    {
        auto wm = YamlValue.fromMapping();
        foreach (name; sortedWorkerNames(workers))
        {
            auto w = workers[name];
            auto entry = YamlValue.fromMapping();
            entry.set("command", YamlValue.fromString(w.command));
            if (w.replicas != 0)
                entry.set("replicas", YamlValue.fromInt(w.replicas));
            if (w.dir.length > 0)
                entry.set("dir", YamlValue.fromString(w.dir));
            if (w.env.length > 0)
            {
                auto em = YamlValue.fromMapping();
                string[] envKeys = w.env.keys;
                envKeys.sort();
                foreach (ek; envKeys)
                    em.set(ek, YamlValue.fromString(w.env[ek]));
                entry.set("env", em);
            }
            wm.set(name, entry);
        }
        root.set("workers", wm);
    }
    return root;
}

YamlValue projectFileToYAML(const ProjectFile file) @safe
{
    auto root = YamlValue.fromMapping();
    root.set("version", marshalManifestVersion(file.version_));
    root.set("root", YamlValue.fromString(file.root));
    return serializeCommonEnvironmentFields(
        root,
        file.framework,
        file.tools,
        file.settings,
        file.docroot,
        file.https,
        file.envFile,
        file.envVars,
        file.database,
        file.memoryLimit,
        file.phpExtensions,
        file.peclExtensions,
        file.opcachePreset,
        file.opcacheConfig,
        file.server,
        file.workers
    );
}

YamlValue environmentFileToYAML(const EnvironmentFile file) @safe
{
    auto root = YamlValue.fromMapping();
    return serializeCommonEnvironmentFields(
        root,
        file.framework,
        file.tools,
        file.settings,
        file.docroot,
        file.https,
        file.envFile,
        file.envVars,
        file.database,
        file.memoryLimit,
        file.phpExtensions,
        file.peclExtensions,
        file.opcachePreset,
        file.opcacheConfig,
        file.server,
        file.workers
    );
}

ProjectFile projectFileFromYAML(const YamlValue root) @safe
{
    ProjectFile file;
    if (root.isNull())
        return file;
    if (!root.isMapping())
        throw new YamlException("project file must be a YAML mapping");

    if (root.hasKey("version"))
        file.version_ = decodeManifestVersion(root.get("version"));
    if (root.hasKey("root"))
        file.root = root.get("root").asString();

    auto envFile = environmentFileFromYAML(root);
    file.framework = envFile.framework;
    file.tools = envFile.tools;
    file.settings = envFile.settings;
    file.docroot = envFile.docroot;
    file.https = envFile.https;
    file.envFile = envFile.envFile;
    file.envVars = envFile.envVars;
    file.database = envFile.database;
    file.memoryLimit = envFile.memoryLimit;
    file.phpExtensions = envFile.phpExtensions;
    file.peclExtensions = envFile.peclExtensions;
    file.opcachePreset = envFile.opcachePreset;
    file.opcacheConfig = envFile.opcacheConfig;
    file.server = envFile.server;
    file.workers = envFile.workers;
    return file;
}

EnvironmentFile environmentFileFromYAML(const YamlValue root, bool strict = false) @safe
{
    EnvironmentFile file;
    if (root.isNull())
        return file;
    if (!root.isMapping())
        throw new YamlException("environment file must be a YAML mapping");

    foreach (entry; root.map)
    {
        auto k = entry.key;
        auto v = entry.value;
        switch (k)
        {
            case "version":
            case "root":
                if (strict)
                    throw new YamlException("unknown field: " ~ k);
                break;
            case "framework":
                if (!v.isNull()) file.framework = v.asString();
                break;
            case "tools":
                if (!v.isNull())
                {
                    if (!v.isMapping()) throw new YamlException("tools must be a mapping");
                    foreach (te; v.map)
                        file.tools[te.key] = te.value.isNull() ? "" : te.value.asScalarText();
                }
                break;
            case "settings":
                if (!v.isNull())
                {
                    if (!v.isMapping()) throw new YamlException("settings must be a mapping");
                    foreach (nse; v.map)
                    {
                        if (!nse.value.isMapping()) throw new YamlException("settings." ~ nse.key ~ " must be a mapping");
                        YamlValue[string] sub;
                        foreach (ke; nse.value.map)
                            sub[ke.key] = ke.value.clone();
                        file.settings[nse.key] = sub;
                    }
                }
                break;
            case "docroot":
                if (!v.isNull()) file.docroot = v.asString();
                break;
            case "https":
                if (!v.isNull()) file.https = v.asBool();
                break;
            case "env-file":
                if (!v.isNull()) file.envFile = v.asString();
                break;
            case "env-vars":
                if (!v.isNull())
                {
                    if (!v.isMapping()) throw new YamlException("env-vars must be a mapping");
                    foreach (ee; v.map)
                        file.envVars[ee.key] = ee.value.isNull() ? "" : ee.value.asScalarText();
                }
                break;
            case "database":
                if (!v.isNull())
                {
                    if (!v.isMapping()) throw new YamlException("database must be a mapping");
                    auto db = new DatabaseConfig();
                    foreach (de; v.map)
                    {
                        switch (de.key)
                        {
                            case "engine":  if (!de.value.isNull()) db.engine = de.value.asScalarText(); break;
                            case "version": if (!de.value.isNull()) db.version_ = de.value.asScalarText(); break;
                            case "port":    if (!de.value.isNull()) db.port = cast(int) de.value.asInt(); break;
                            default:
                                if (strict) throw new YamlException("unknown database field: " ~ de.key);
                                break;
                        }
                    }
                    file.database = db;
                }
                break;
            case "memory-limit":
                file.memoryLimit = v.clone();
                break;
            case "php-extensions":
                if (!v.isNull())
                {
                    if (!v.isMapping()) throw new YamlException("php-extensions must be a mapping");
                    foreach (pe; v.map)
                        file.phpExtensions[pe.key] = pe.value.clone();
                }
                break;
            case "pecl-extensions":
                if (!v.isNull())
                {
                    if (!v.isMapping()) throw new YamlException("pecl-extensions must be a mapping");
                    foreach (pe; v.map)
                        file.peclExtensions[pe.key] = pe.value.clone();
                }
                break;
            case "opcache-preset":
                if (!v.isNull()) file.opcachePreset = v.asScalarText();
                break;
            case "opcache-config":
                if (!v.isNull())
                {
                    if (!v.isMapping()) throw new YamlException("opcache-config must be a mapping");
                    foreach (oe; v.map)
                        file.opcacheConfig[oe.key] = oe.value.clone();
                }
                break;
            case "server":
                if (!v.isNull())
                {
                    if (!v.isMapping()) throw new YamlException("server must be a mapping");
                    auto srv = new ServerConfig();
                    foreach (se; v.map)
                    {
                        switch (se.key)
                        {
                            case "type":     if (!se.value.isNull()) srv.type = se.value.asScalarText(); break;
                            case "hostname": if (!se.value.isNull()) srv.hostname = se.value.asScalarText(); break;
                            case "port":     if (!se.value.isNull()) srv.port = cast(int) se.value.asInt(); break;
                            case "https":    if (!se.value.isNull()) srv.https = se.value.asBool(); break;
                            default:
                                if (strict) throw new YamlException("unknown server field: " ~ se.key);
                                break;
                        }
                    }
                    file.server = srv;
                }
                break;
            case "workers":
                if (!v.isNull())
                {
                    if (!v.isMapping()) throw new YamlException("workers must be a mapping");
                    foreach (we; v.map)
                    {
                        if (!we.value.isMapping()) throw new YamlException("workers." ~ we.key ~ " must be a mapping");
                        WorkerConfig wc;
                        foreach (wf; we.value.map)
                        {
                            switch (wf.key)
                            {
                                case "command":  if (!wf.value.isNull()) wc.command = wf.value.asScalarText(); break;
                                case "replicas": if (!wf.value.isNull()) wc.replicas = cast(int) wf.value.asInt(); break;
                                case "dir":      if (!wf.value.isNull()) wc.dir = wf.value.asScalarText(); break;
                                case "env":
                                    if (!wf.value.isNull())
                                    {
                                        if (!wf.value.isMapping()) throw new YamlException("worker env must be a mapping");
                                        foreach (ef; wf.value.map)
                                            wc.env[ef.key] = ef.value.isNull() ? "" : ef.value.asScalarText();
                                    }
                                    break;
                                default:
                                    if (strict) throw new YamlException("unknown worker field: " ~ wf.key);
                                    break;
                            }
                        }
                        file.workers[we.key] = wc;
                    }
                }
                break;
            default:
                if (strict)
                    throw new YamlException("unknown environment field: " ~ k);
                break;
        }
    }
    return file;
}

unittest
{
    // TestPostgreSQLToolAndDatabaseRoundTrip
    Environment env;
    env.name = "demo";
    env.tools = ["postgresql": "17"];
    env.database = new DatabaseConfig("postgresql", "17", 5544);
    auto file = environmentFileFromEnvironment(env);
    assert(file.tools["postgresql"] == "17");
    assert(file.database !is null && file.database.engine == "postgresql" && file.database.port == 5544 && file.database.version_ == "");
    auto roundTrip = environmentFileToEnvironment("demo", file);
    assert(roundTrip.toolVersion("postgresql") == "17" && roundTrip.database !is null && roundTrip.database.version_ == "17");

    // TestTraefikToolAndSettingsRoundTrip
    Environment trEnv;
    trEnv.tools = ["traefik": "3.3"];
    trEnv.settings = ["traefik": ["port": YamlValue.fromInt(8090)]];
    trEnv = normalizeEnvironment("demo", trEnv);
    auto trFile = environmentFileFromEnvironment(trEnv);
    assert(trFile.tools["traefik"] == "3.3");
    assert(trFile.settings["traefik"]["port"].asInt() == 8090);
    auto trRt = environmentFileToEnvironment("demo", trFile);
    auto trView = trRt.traefik();
    assert(trView !is null && trView.version_ == "3.3" && trView.port == 8090);

    // TestPHPZTSConfigRoundTrip
    ProjectFile ztsPF;
    ztsPF.tools = ["php-zts": "8.4"];
    auto ztsEnv = projectFileToEnvironment("default", ztsPF);
    assert(ztsEnv.toolVersion("php-zts") == "8.4" && ztsEnv.toolVersion("php") == "");
    string pTool, pVer;
    primaryPHPTool(ztsEnv, pTool, pVer);
    assert(pTool == "php-zts" && pVer == "8.4");

    // TestValidatePHPMemoryLimit
    foreach (ok; ["", "-1", "0", "128M", "512m", "1024K", "1G", "134217728"])
        validatePHPMemoryLimit(ok);
    foreach (bad; ["-2", "1.5G", "128MB", "many"])
    {
        bool threw = false;
        try { validatePHPMemoryLimit(bad); } catch (Exception) { threw = true; }
        assert(threw);
    }

    // TestSplitPHPExtensionsFromYAML
    bool[string] bundled;
    string[string] pie;
    splitPHPExtensionsFromYAML([
        "MBString": YamlValue.fromBool(true),
        "apcu": YamlValue.fromBool(false),
        "Xdebug/Xdebug": YamlValue.fromString("3.4.1"),
        "acme/float": YamlValue.fromFloat(1.2),
        "acme/int": YamlValue.fromInt(3),
        "acme/latest": YamlValue.nullValue(),
    ], bundled, pie);
    assert(bundled == ["mbstring": true, "apcu": false]);
    assert(pie == ["xdebug/xdebug": "3.4.1", "acme/float": "1.2", "acme/int": "3", "acme/latest": "*"]);

    // TestValidatePIEExtensionPackage & Version
    foreach (v; ["xdebug/xdebug", "open-telemetry/ext.otel", "a1/b2"])
        validatePIEExtensionPackage(v);
    foreach (inv; ["", "xdebug", "/name", "vendor/", "Vendor Name/pkg", "vendor//pkg"])
    {
        bool threw = false;
        try { validatePIEExtensionPackage(inv); } catch (Exception) { threw = true; }
        assert(threw);
    }
    foreach (v; ["3.4.1", "*", "^1.2", ">=2.0,<3.0", "~1.0"])
        validatePIEExtensionVersion("acme/demo", v);
    foreach (inv; ["", "true", "false", "1.0 2.0", "1.0\n2.0"])
    {
        bool threw = false;
        try { validatePIEExtensionVersion("acme/demo", inv); } catch (Exception) { threw = true; }
        assert(threw);
    }

    // TestPECLExtensionsYAMLRoundTrip
    auto imagickObj = YamlValue.fromMapping();
    imagickObj.set("version", YamlValue.fromString("3.8.0"));
    auto opts = YamlValue.fromMapping();
    opts.set("--with-imagick", YamlValue.fromString("/opt/imagemagick"));
    imagickObj.set("configure-options", opts);
    auto peclMap = peclExtensionsFromYAML([
        "Redis": YamlValue.fromString("6.2.0"),
        "imagick": imagickObj,
    ]);
    assert(peclMap["redis"].version_ == "6.2.0");
    assert(peclMap["imagick"].version_ == "3.8.0");
    assert(peclMap["imagick"].configureOptions["with-imagick"] == "/opt/imagemagick");

    // TestServiceViewsNilAndHTTPSInheritance
    Environment emptyHttps;
    emptyHttps.https = true;
    assert(emptyHttps.mailpit() is null && emptyHttps.phpMyAdmin() is null
        && emptyHttps.meilisearch() is null && emptyHttps.redis() is null
        && emptyHttps.rabbitMQ() is null && emptyHttps.traefik() is null);

    Environment configuredHttps;
    configuredHttps.https = true;
    configuredHttps.tools = ["mailpit": "1.30", "phpmyadmin": "5.2"];
    auto mpView = configuredHttps.mailpit();
    assert(mpView !is null && mpView.https && mpView.version_ == "1.30");
    auto pmaView = configuredHttps.phpMyAdmin();
    assert(pmaView !is null && pmaView.https && pmaView.version_ == "5.2");

    // TestRedisToolAndSettingsRoundTrip
    Environment rEnv;
    rEnv.tools = ["redis": "8.8.0"];
    rEnv.settings = ["redis": ["port": YamlValue.fromInt(6380), "password": YamlValue.fromString("local-dev-password")]];
    rEnv = normalizeEnvironment("demo", rEnv);
    auto rFile = environmentFileFromEnvironment(rEnv);
    assert(rFile.tools["redis"] == "8.8.0");
    assert(rFile.settings["redis"]["port"].asInt() == 6380);
    assert(rFile.settings["redis"]["password"].asString() == "local-dev-password");
    auto rRt = environmentFileToEnvironment("demo", rFile);
    auto rView = rRt.redis();
    assert(rView !is null && rView.version_ == "8.8.0" && rView.port == 6380 && rView.password == "local-dev-password");

    // TestRabbitMQToolAndSettingsRoundTrip
    Environment rmqEnv;
    rmqEnv.name = "demo";
    rmqEnv.tools = ["rabbitmq": "4.3"];
    rmqEnv.settings = ["rabbitmq": [
        "port": YamlValue.fromInt(5673),
        "management-port": YamlValue.fromInt(15673),
        "username": YamlValue.fromString("polka"),
        "password": YamlValue.fromString("secret"),
    ]];
    auto rmqFile = environmentFileFromEnvironment(rmqEnv);
    assert(rmqFile.tools["rabbitmq"] == "4.3");
    assert(rmqFile.settings["rabbitmq"]["management-port"].asInt() == 15673);
    auto rmqRt = environmentFileToEnvironment("demo", rmqFile);
    auto rmqView = rmqRt.rabbitMQ();
    assert(rmqView !is null && rmqView.username == "polka" && rmqView.password == "secret");

    // TestFrankenPHPAndServerTypeConfigRoundTrip
    ProjectFile fpPF;
    fpPF.tools = ["frankenphp": " 1.12 "];
    fpPF.server = new ServerConfig(" FrankenPHP ");
    auto fpNorm = normalizeEnvironment("default", projectFileToEnvironment("default", fpPF));
    assert(fpNorm.toolVersion("frankenphp") == "1.12" && fpNorm.server !is null && fpNorm.server.type == ServerTypeFrankenPHP);
    auto fpOut = projectFileFromEnvironment("0.2", ".polka", fpNorm);
    assert(fpOut.tools["frankenphp"] == "1.12" && fpOut.server !is null && fpOut.server.type == ServerTypeFrankenPHP);

    // TestApacheAndServerTypeConfigRoundTrip
    ProjectFile apPF;
    apPF.tools = ["apache": " 2.4 "];
    apPF.server = new ServerConfig(" Apache ");
    auto apNorm = normalizeEnvironment("default", projectFileToEnvironment("default", apPF));
    assert(apNorm.toolVersion("apache") == "2.4" && apNorm.server !is null && apNorm.server.type == ServerTypeApache);
    auto apOut = projectFileFromEnvironment("0.2", ".polka", apNorm);
    assert(apOut.tools["apache"] == "2.4" && apOut.server !is null && apOut.server.type == ServerTypeApache);

    // TestPHPMemoryLimitConfigRoundTrip
    ProjectFile mlPF;
    mlPF.tools = ["php": "8.4"];
    mlPF.memoryLimit = YamlValue.fromString(" 512m ");
    auto mlEnv = projectFileToEnvironment("default", mlPF);
    assert(mlEnv.memoryLimit == "512M");
    auto mlOut = projectFileFromEnvironment("0.2", ".polka", mlEnv);
    assert(mlOut.memoryLimit.asString() == "512M");

    // TestPHPCLIProviderFallsBackToFrankenPHP
    Environment fcliEnv;
    fcliEnv.tools = ["frankenphp": "1.12"];
    string cliTool, cliVer;
    phpCLIProvider(fcliEnv, cliTool, cliVer);
    assert(cliTool == ServerTypeFrankenPHP && cliVer == "1.12");
    assert(hasPHPCLI(fcliEnv));
    Environment mixedCli;
    mixedCli.tools = ["php": "8.4", "frankenphp": "1.12"];
    phpCLIProvider(mixedCli, cliTool, cliVer);
    assert(cliTool == "php" && cliVer == "8.4");

    // TestPHPBuildToolsSettingRoundTrip
    ProjectFile sdkPF;
    sdkPF.settings = ["php": ["extension-sdk": YamlValue.fromBool(true)]];
    auto sdkEnv = projectFileToEnvironment("default", sdkPF);
    assert(sdkEnv.phpBuildTools());
    auto sdkOut = projectFileFromEnvironment("0.2", ".polka", sdkEnv);
    assert(sdkOut.settings["php"]["extension-sdk"].asBool() == true);
    assert(projectFileFromEnvironment("0.2", ".polka", Environment()).settings is null);

    // TestUnknownToolAndSettingsRoundTrip
    EnvironmentFile unkFile;
    unkFile.tools = [" CustomTool ": " 2.1.0 "];
    unkFile.settings = [" CustomTool ": [
        "Port": YamlValue.fromInt(9090),
        "Token": YamlValue.fromString("  secret-token  "),
        "Enabled": YamlValue.fromBool(true),
        "Empty": YamlValue.fromString("   "),
        "Zero": YamlValue.fromInt(0),
        "False": YamlValue.fromBool(false),
    ]];
    auto unkEnv = environmentFileToEnvironment("demo", unkFile);
    assert(unkEnv.toolVersion("customtool") == "2.1.0");
    assert(unkEnv.settingInt("customtool", "port") == 9090);
    assert(unkEnv.settingString("customtool", "token") == "secret-token");
    assert(unkEnv.settingBool("customtool", "enabled"));
    auto unkOut = environmentFileFromEnvironment(unkEnv);
    assert(unkOut.tools["customtool"] == "2.1.0");
    assert(unkOut.settings["customtool"]["port"].asInt() == 9090);
    assert(unkOut.settings["customtool"]["token"].asString() == "secret-token");
    assert(unkOut.settings["customtool"]["enabled"].asBool() == true);
    assert("empty" !in unkOut.settings["customtool"]);
    assert("zero" !in unkOut.settings["customtool"]);
    assert("false" !in unkOut.settings["customtool"]);

    // TestWorkersRoundTripThroughFileTypes + full YAML round-trip
    WorkerConfig[string] workersMap;
    workersMap["queue"] = WorkerConfig("php artisan queue:work", 2, "app", ["TRIES": "3"]);
    workersMap["scheduler"] = WorkerConfig("php artisan schedule:work");
    ProjectFile wPF;
    wPF.version_ = CurrentManifestVersion;
    wPF.root = ".polka";
    wPF.workers = workersMap;
    auto yamlNode = projectFileToYAML(wPF);
    auto yamlText = emitYAML(yamlNode);
    auto decodedPF = projectFileFromYAML(parseYAML(yamlText));
    assert(decodedPF.version_ == "0.2" && decodedPF.root == ".polka");
    assert(decodedPF.workers.length == 2);
    assert(decodedPF.workers["queue"] == workersMap["queue"]);
    assert(decodedPF.workers["scheduler"] == workersMap["scheduler"]);
}
