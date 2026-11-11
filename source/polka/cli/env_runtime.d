module polka.cli.env_runtime;

// Runtime environment resolution and .env file parser combining OS env,
// project `.env`, configured `env-file`, framework credentials/variables,
// PostgreSQL PGPASSFILE, and explicit `env-vars` overrides.

import std.algorithm : sort, startsWith;
import std.ascii : isWhite;
import std.file : exists, readText;
import std.format : format;
import std.path : buildPath, isAbsolute;
import std.string : icmp, indexOf, splitLines, strip;

import polka.backend :
    Config,
    ConfigNotExistException,
    Environment,
    FrameworkPlugin,
    normalizePHPMemoryLimit,
    Store;
import polka.cli.types :
    isMissingFileError,
    lookupEnvValue,
    regularFileExists,
    replaceEnvValue,
    unquoteDoubleQuotedString;
import polka.plugins :
    FrameworkDatabaseCredentials = DatabaseCredentials,
    RuntimeEnvContext;
import polka.service :
    databaseCredentialStatePath,
    DatabaseListenHost,
    databasePGPassFilePath,
    effectiveDatabasePort,
    loadManagedDatabaseCredentials,
    ManagedDatabaseCredentials;
import polka.support.fs : cleanPath, fromSlashPath;

/// Default root-relative dotenv file loaded automatically before active environment overlays.
enum string defaultProjectEnvFileName = ".env";

/// Parsed `KEY=VALUE` pair from a dotenv file.
struct EnvironmentVariable
{
    string name;
    string value;
}

/// Composes the effective runtime environment for `store` following precedence:
/// inherited env < `.env` < `current.env-file` < framework env (incl. DB creds & PGPASSFILE) < `current.env-vars`.
string[] resolveRuntimeEnvironment(string goos, const(string)[] inherited, ref Store store)
{
    string[] resolved = inherited.dup;

    auto autoEnvPath = buildPath(store.projectDir, defaultProjectEnvFileName);
    bool autoEnvExists = false;
    try
    {
        autoEnvExists = regularFileExists(autoEnvPath);
    }
    catch (Exception e)
    {
        throw new Exception(format("stat project env file %s: %s", autoEnvPath, e.msg));
    }
    if (autoEnvExists)
    {
        try
        {
            resolved = overlayEnvironmentFile(goos, resolved, autoEnvPath);
        }
        catch (Exception e)
        {
            throw new Exception(format("load project env file %s: %s", autoEnvPath, e.msg));
        }
    }

    auto current = store.current();
    if (current is null)
        return resolved;

    if (current.envFile.strip().length > 0)
    {
        auto configuredPath = resolveConfiguredEnvironmentFilePath(store.projectDir, current.envFile);
        try
        {
            resolved = overlayEnvironmentFile(goos, resolved, configuredPath);
        }
        catch (Exception e)
        {
            throw new Exception(format(
                `load configured env-file "%s" for environment "%s": %s`,
                current.envFile, current.name, e.msg
            ));
        }
    }

    auto frameworkValues = frameworkRuntimeEnvironmentVariables(store, *current);
    try
    {
        resolved = overlayEnvironmentVariables(goos, resolved, frameworkValues);
    }
    catch (Exception e)
    {
        throw new Exception(format(`load framework env-vars for environment "%s": %s`, current.name, e.msg));
    }

    if (current.database !is null && icmp(current.database.engine.strip(), "postgresql") == 0)
    {
        auto pgPassPath = databasePGPassFilePath(store.rootDir, current.name);
        bool pgPassExists = false;
        try
        {
            pgPassExists = regularFileExists(pgPassPath);
        }
        catch (Exception e)
        {
            throw new Exception(format("stat postgresql password file %s: %s", pgPassPath, e.msg));
        }
        if (pgPassExists)
        {
            try
            {
                resolved = overlayEnvironmentVariables(goos, resolved, ["PGPASSFILE": pgPassPath]);
            }
            catch (Exception e)
            {
                throw new Exception(format(`load postgresql client environment for "%s": %s`, current.name, e.msg));
            }
        }
    }

    try
    {
        resolved = overlayEnvironmentVariables(goos, resolved, current.envVars);
    }
    catch (Exception e)
    {
        throw new Exception(format(`load configured env-vars for environment "%s": %s`, current.name, e.msg));
    }

    return resolved;
}

/// Evaluates framework-contributed runtime environment variables (if `environment.framework` is set).
string[string] frameworkRuntimeEnvironmentVariables(ref Store store, const Environment environment)
{
    if (environment.framework.strip().length == 0)
        return null;

    FrameworkPlugin plugin;
    if (!store.frameworkPlugin(environment.framework, plugin))
    {
        import std.string : join;
        throw new Exception(format(
            `unsupported framework "%s"; supported frameworks: %s`,
            environment.framework, store.supportedFrameworks().join(", ")
        ));
    }

    auto credentials = frameworkDatabaseCredentials(store.rootDir, environment);
    return plugin.runtimeEnv(RuntimeEnvContext(cast(Environment) environment, credentials));
}

/// Loads persisted managed database credentials for `environment` if present, returning `null` when absent.
FrameworkDatabaseCredentials* frameworkDatabaseCredentials(string rootDir, const Environment environment)
{
    if (environment.database is null || environment.database.engine.strip().length == 0)
        return null;

    ManagedDatabaseCredentials credentials;
    try
    {
        credentials = loadManagedDatabaseCredentials(databaseCredentialStatePath(rootDir, environment.name));
    }
    catch (Exception e)
    {
        if (isMissingFileError(e))
            return null;
        throw e;
    }

    return frameworkDatabaseCredentialsFromManaged(environment, credentials);
}

/// Projects `ManagedDatabaseCredentials` into plugin-facing `DatabaseCredentials`.
FrameworkDatabaseCredentials* frameworkDatabaseCredentialsFromManaged(
    const Environment environment,
    const ManagedDatabaseCredentials credentials
) @safe pure
{
    int port = credentials.port;
    if (port == 0)
        port = effectiveDatabasePort(environment.database);

    auto dbName = credentials.databaseName.strip();
    if (dbName.length == 0)
        dbName = environment.name;

    auto result = new FrameworkDatabaseCredentials();
    result.engine = environment.database.engine;
    result.version_ = environment.database.version_;
    result.host = DatabaseListenHost;
    result.port = port;
    result.databaseName = dbName;
    result.user = credentials.user;
    result.password = credentials.password;
    return result;
}

/// Resolves a configured `env-file` path relative to `projectDir` unless already absolute.
string resolveConfiguredEnvironmentFilePath(string projectDir, string configuredPath) @trusted
{
    auto trimmed = configuredPath.strip();
    if (trimmed.length == 0)
        return "";
    auto nativePath = fromSlashPath(trimmed);
    if (isAbsolute(nativePath))
        return cleanPath(nativePath);
    return cleanPath(buildPath(projectDir, nativePath));
}

/// Parses and overlays all `KEY=VALUE` assignments from `path` onto `env`.
string[] overlayEnvironmentFile(string goos, const(string)[] env, string path)
{
    auto variables = readEnvironmentFile(path);
    string[] updated = env.dup;
    foreach (v; variables)
        updated = replaceEnvValue(goos, updated, v.name, v.value);
    return updated;
}

/// Validates variable names and deterministically overlays `values` (sorted by key) onto `env`.
string[] overlayEnvironmentVariables(string goos, const(string)[] env, const(string[string]) values) @safe
{
    if (values.length == 0)
        return env.dup;

    auto keys = values.keys;
    keys.sort();

    string[] updated = env.dup;
    foreach (rawKey; keys)
    {
        auto key = rawKey.strip();
        validateEnvironmentVariableName(key);
        updated = replaceEnvValue(goos, updated, key, values[rawKey]);
    }
    return updated;
}

/// Reads and parses a dotenv file (`#` comments, optional `export`, quotes, inline comments).
EnvironmentVariable[] readEnvironmentFile(string path) @trusted
{
    auto text = readText(path);
    EnvironmentVariable[] variables;

    size_t lineNumber = 1;
    // Normalize CRLF so Windows dotenv files parse without stray \r tokens.
    foreach (rawLine; text.splitLines())
    {
        scope (exit) lineNumber++;
        auto trimmed = rawLine.strip();
        if (trimmed.length == 0 || trimmed.startsWith("#"))
            continue;
        if (trimmed.startsWith("export "))
            trimmed = trimmed["export ".length .. $].strip();

        auto eq = trimmed.indexOf('=');
        if (eq < 0)
            throw new Exception(format("parse %s:%d: expected KEY=VALUE", path, lineNumber));

        auto key = trimmed[0 .. eq].strip();
        auto rawValue = trimmed[eq + 1 .. $];
        try
        {
            validateEnvironmentVariableName(key);
            auto value = parseEnvironmentVariableValue(rawValue);
            variables ~= EnvironmentVariable(key, value);
        }
        catch (Exception e)
        {
            throw new Exception(format("parse %s:%d: %s", path, lineNumber, e.msg));
        }
    }

    return variables;
}

/// Decodes a single dotenv assignment RHS (double-quoted, single-quoted, or unquoted with `#` comment stripping).
string parseEnvironmentVariableValue(string raw) @safe pure
{
    auto trimmed = raw.strip();
    if (trimmed.length == 0)
        return "";
    if (trimmed.startsWith(`"`))
        return parseDoubleQuotedEnvironmentValue(trimmed);
    if (trimmed.startsWith("'"))
        return parseSingleQuotedEnvironmentValue(trimmed);
    return stripEnvironmentInlineComment(trimmed);
}

private string parseDoubleQuotedEnvironmentValue(string value) @safe pure
{
    bool escaped = false;
    for (size_t index = 1; index < value.length; index++)
    {
        char current = value[index];
        if (!escaped && current == '\\')
        {
            escaped = true;
            continue;
        }
        if (!escaped && current == '"')
        {
            auto remainder = value[index + 1 .. $].strip();
            if (remainder.length > 0 && !remainder.startsWith("#"))
                throw new Exception(format(`unexpected trailing content "%s"`, remainder));
            try
            {
                return unquoteDoubleQuotedString(value[0 .. index + 1]);
            }
            catch (Exception e)
            {
                throw new Exception("decode quoted value: " ~ e.msg);
            }
        }
        escaped = false;
    }

    throw new Exception("unterminated quoted value");
}

private string parseSingleQuotedEnvironmentValue(string value) @safe pure
{
    for (size_t index = 1; index < value.length; index++)
    {
        if (value[index] != '\'')
            continue;
        auto remainder = value[index + 1 .. $].strip();
        if (remainder.length > 0 && !remainder.startsWith("#"))
            throw new Exception(format(`unexpected trailing content "%s"`, remainder));
        return value[1 .. index];
    }

    throw new Exception("unterminated quoted value");
}

private string stripEnvironmentInlineComment(string value) @safe pure
{
    foreach (index, char current; value)
    {
        if (current != '#')
            continue;
        if (index == 0)
            return "";
        if (isWhite(value[index - 1]))
            return value[0 .. index].strip();
    }
    return value.strip();
}

/// Validates a dotenv / environment variable key name.
void validateEnvironmentVariableName(string name) @safe pure
{
    auto trimmed = name.strip();
    if (trimmed.length == 0)
        throw new Exception("environment variable name cannot be empty");
    if (trimmed.indexOf('=') >= 0)
        throw new Exception(format(`environment variable name "%s" cannot contain =`, name));
    if (trimmed.indexOf('\0') >= 0)
        throw new Exception(format(`environment variable name "%s" cannot contain NUL`, name));
}

unittest
{
    import std.file : mkdirRecurse, write;
    import std.path : buildPath;
    import polka.backend : newProjectStore;
    import polka.cli.test_helpers : writeTestActiveEnvironment, writeTestConfigFile;
    import polka.config : DatabaseConfig;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    // Parse dotenv file with comments, quotes, export keyword, inline comments, and empty values.
    {
        auto tempDir = createTempDir("polka-env-parse-");
        scope (exit) rmdirRecurseQuiet(tempDir);

        auto path = buildPath(tempDir, ".env");
        auto contents = "# comment\n" ~
            "export APP_ENV=development\n" ~
            "QUOTED=\"hello world\"\n" ~
            "SINGLE='a # b'\n" ~
            "TRAILING=value # comment\n" ~
            "EMPTY=\n";
        write(path, contents);

        auto variables = readEnvironmentFile(path);
        assert(variables.length == 5);
        assert(variables[0] == EnvironmentVariable("APP_ENV", "development"));
        assert(variables[1] == EnvironmentVariable("QUOTED", "hello world"));
        assert(variables[2] == EnvironmentVariable("SINGLE", "a # b"));
        assert(variables[3] == EnvironmentVariable("TRAILING", "value"));
        assert(variables[4] == EnvironmentVariable("EMPTY", ""));
    }

    // Precedence: inherited env < project .env < configured env-file < configured env-vars.
    {
        auto projectDir = createTempDir("polka-env-prec-");
        scope (exit) rmdirRecurseQuiet(projectDir);

        auto store = newProjectStore(projectDir);
        write(buildPath(projectDir, ".env"), "APP_ENV=project\nSHARED=project\nPROJECT_ONLY=1\n");
        mkdirRecurse(buildPath(projectDir, "config"));
        write(buildPath(projectDir, "config", ".env.local"), "APP_ENV=file\nSHARED=file\nFILE_ONLY=1\n");

        Config cfg;
        cfg.version_ = "0.2";
        cfg.root = ".polka";
        Environment demo;
        demo.name = "demo";
        demo.tools["php"] = "8.4";
        demo.envFile = "config/.env.local";
        demo.envVars = ["APP_ENV": "config", "CONFIG_ONLY": "1", "SHARED": "config"];
        cfg.environments["demo"] = demo;
        writeTestConfigFile(projectDir, cfg);
        writeTestActiveEnvironment(buildPath(projectDir, ".polka"), "demo");

        auto resolved = resolveRuntimeEnvironment("linux", ["APP_ENV=os", "OS_ONLY=1"], store);
        string k, v;
        assert(lookupEnvValue("linux", resolved, "APP_ENV", k, v) && v == "config");
        assert(lookupEnvValue("linux", resolved, "SHARED", k, v) && v == "config");
        assert(lookupEnvValue("linux", resolved, "PROJECT_ONLY", k, v) && v == "1");
        assert(lookupEnvValue("linux", resolved, "FILE_ONLY", k, v) && v == "1");
        assert(lookupEnvValue("linux", resolved, "CONFIG_ONLY", k, v) && v == "1");
        assert(lookupEnvValue("linux", resolved, "OS_ONLY", k, v) && v == "1");
    }

    // Windows env key replacement matches case-insensitively while preserving original casing.
    {
        import std.string : indexOf;
        import polka.cli.types : envKeysEqual;

        auto projectDir = createTempDir("polka-env-win-");
        scope (exit) rmdirRecurseQuiet(projectDir);

        auto store = newProjectStore(projectDir);
        Config cfg;
        cfg.version_ = "0.2";
        cfg.root = ".polka";
        Environment demo;
        demo.name = "demo";
        demo.tools["php"] = "8.4";
        demo.envVars = ["app_env": "config"];
        cfg.environments["demo"] = demo;
        writeTestConfigFile(projectDir, cfg);
        writeTestActiveEnvironment(buildPath(projectDir, ".polka"), "demo");

        auto resolved = resolveRuntimeEnvironment("windows", ["App_ENV=os"], store);
        string key, value;
        assert(lookupEnvValue("windows", resolved, "APP_ENV", key, value));
        assert(key == "App_ENV");
        assert(value == "config");

        int count = 0;
        foreach (entry; resolved)
        {
            auto eq = entry.indexOf('=');
            if (eq >= 0 && envKeysEqual("windows", entry[0 .. eq], "APP_ENV"))
                count++;
        }
        assert(count == 1);
    }

    // Framework database credentials are applied after OS/.env and before explicit env-vars.
    {
        import polka.support.fs : octal, setFileMode;

        auto projectDir = createTempDir("polka-env-fw-");
        scope (exit) rmdirRecurseQuiet(projectDir);

        auto root = buildPath(projectDir, ".polka");
        auto store = newProjectStore(projectDir);

        Config cfg;
        cfg.version_ = "0.2";
        cfg.root = ".polka";
        Environment demo;
        demo.name = "demo";
        demo.framework = "laravel";
        demo.tools["php"] = "8.4";
        demo.database = new DatabaseConfig("mariadb", "11.8", 3307);
        demo.envVars = ["DB_HOST": "configured-host", "DB_PASSWORD": "configured-password"];
        cfg.environments["demo"] = demo;
        writeTestConfigFile(projectDir, cfg);
        writeTestActiveEnvironment(root, "demo");

        import std.path : dirName;
        auto credentialsPath = databaseCredentialStatePath(root, "demo");
        mkdirRecurse(dirName(credentialsPath));
        auto credentials = "{\n" ~
            "  \"environment\": \"demo\",\n" ~
            "  \"engine\": \"mariadb\",\n" ~
            "  \"version\": \"11.8\",\n" ~
            "  \"database\": \"demo\",\n" ~
            "  \"user\": \"polka\",\n" ~
            "  \"password\": \"generated-password\",\n" ~
            "  \"port\": 3307\n" ~
            "}\n";
        write(credentialsPath, credentials);
        setFileMode(credentialsPath, octal!600);

        auto resolved = resolveRuntimeEnvironment("linux", ["DB_USERNAME=os"], store);
        string k, val;
        assert(lookupEnvValue("linux", resolved, "DB_CONNECTION", k, val) && val == "mysql");
        assert(lookupEnvValue("linux", resolved, "DB_HOST", k, val) && val == "configured-host");
        assert(lookupEnvValue("linux", resolved, "DB_PORT", k, val) && val == "3307");
        assert(lookupEnvValue("linux", resolved, "DB_DATABASE", k, val) && val == "demo");
        assert(lookupEnvValue("linux", resolved, "DB_USERNAME", k, val) && val == "polka");
        assert(lookupEnvValue("linux", resolved, "DB_PASSWORD", k, val) && val == "configured-password");
    }
}
