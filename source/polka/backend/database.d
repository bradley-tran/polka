module polka.backend.database;

import std.format : format;
import std.string : strip, toLower;

import polka.config :
    DatabaseConfig,
    Environment,
    mergeDatabaseConfig_ = mergeDatabaseConfig,
    normalizeDatabaseConfig_ = normalizeDatabaseConfig;
import polka.tools : validateVersion;
import polka.tools.ids : MariaDB, MySQL, PostgreSQL;

/// Merges override database settings into an existing config without mutating either input.
DatabaseConfig* mergeDatabaseConfig(const(DatabaseConfig)* existing, const(DatabaseConfig)* overrideCfg) @safe pure
{
    return mergeDatabaseConfig_(existing, overrideCfg);
}

/// Normalizes a `DatabaseConfig` (lowercased/trimmed engine, trimmed version), returning `null` if empty.
DatabaseConfig* normalizeDatabaseConfig(const(DatabaseConfig)* database) @safe pure
{
    return normalizeDatabaseConfig_(database);
}

/// Normalizes a database engine name, throwing on unsupported values.
string normalizeDatabaseEngine(string engine) @safe
{
    auto trimmed = engine.strip().toLower();
    switch (trimmed)
    {
        case "":
            return "";
        case MySQL:
        case MariaDB:
        case PostgreSQL:
            return trimmed;
        default:
            throw new Exception(format(`unsupported database engine "%s"`, engine));
    }
}

/// Validates configured database fields (engine, version, and port range).
void validateDatabaseConfig(const(DatabaseConfig)* database) @safe
{
    if (database is null)
        return;

    auto engine = normalizeDatabaseEngine(database.engine);
    if (engine.length == 0 || database.version_.strip().length == 0)
        throw new Exception("database configuration requires both engine and version");
    validateVersion(engine, database.version_);
    if (database.port != 0 && (database.port < 1 || database.port > 65535))
        throw new Exception("database port must be between 1 and 65535");
}

/// Propagates `database.version_` into `environment.tools[engine]` when engine is a managed DB.
Environment setDatabaseToolVersion(Environment environment, const(DatabaseConfig)* database) @safe pure
{
    if (database is null || database.version_.strip().length == 0)
        return environment;

    auto engine = database.engine.strip().toLower();
    switch (engine)
    {
        case MySQL:
        case MariaDB:
        case PostgreSQL:
            environment.setToolVersion(engine, database.version_.strip());
            break;
        default:
            break;
    }
    return environment;
}

unittest
{
    assert(normalizeDatabaseEngine(" MySQL ") == MySQL);
    assert(normalizeDatabaseEngine("MariaDB") == MariaDB);
    assert(normalizeDatabaseEngine("postgresql") == PostgreSQL);
    assert(normalizeDatabaseEngine("   ") == "");

    bool threw = false;
    try { normalizeDatabaseEngine("oracle"); } catch (Exception) { threw = true; }
    assert(threw);

    Environment env;
    auto db = new DatabaseConfig(" PostgreSQL ", " 17 ", 5432);
    env = setDatabaseToolVersion(env, db);
    assert(env.toolVersion(PostgreSQL) == "17");
    validateDatabaseConfig(db);
}
