module polka.cli.env_helpers;

// Shared environment resolution, runtime warnings, and service status label
// formatters used by `hooks`, `command_env`, `command_ext`, and `dashboard_snapshot`.

import std.format : format;
import std.string : strip;

import polka.backend :
    DatabaseConfig,
    Environment,
    MailpitConfig,
    MeilisearchConfig,
    phpCLIProvider,
    PHPMyAdminConfig,
    primaryPHPTool,
    RabbitMQConfig,
    RedisConfig,
    Store,
    TextWriter,
    TraefikConfig,
    writeOutf;
import polka.cli.types : quoteGoString;
import polka.service :
    effectiveMailpitSMTPPort,
    mailpitUIURLForConfig,
    meilisearchURLForConfig,
    phpMyAdminPostgreSQLWarning,
    phpMyAdminUIURLForConfig,
    phpMyAdminUsesPostgreSQL,
    rabbitMQManagementURLForConfig,
    rabbitMQURLForConfig,
    redisURLForConfig,
    traefikURLForConfig;

/// Default environment name assumed when no active environment exists yet.
enum string defaultEnvironmentName = "default";

/// Resolves `--env <name>`, or the current environment, or falls back to
/// `"default"` while emitting `"<action> <name> environment\n"` on `stdout`.
void resolveCommandEnvironmentName(
    TextWriter stdout,
    ref Store store,
    string name,
    string action,
    out string resolvedName,
    out bool setCurrent
) @trusted
{
    auto trimmedName = name.strip();
    if (trimmedName.length > 0)
    {
        resolvedName = trimmedName;
        setCurrent = false;
        return;
    }

    auto current = store.current();
    if (current !is null)
    {
        writeOutf(stdout, "%s %s environment\n", action, current.name);
        resolvedName = current.name;
        setCurrent = false;
        return;
    }

    writeOutf(stdout, "%s %s environment\n", action, defaultEnvironmentName);
    resolvedName = defaultEnvironmentName;
    setCurrent = true;
}

/// Returns the named normalized environment from `store.list()` or throws.
Environment environmentByName(ref Store store, string name) @trusted
{
    auto environments = store.list();
    foreach (env; environments)
    {
        if (env.name == name)
            return env;
    }
    throw new Exception(format("environment %s does not exist", quoteGoString(name)));
}

/// Warns when an environment configures both a standalone PHP runtime and FrankenPHP.
void writePHPCLIRuntimeWarning(TextWriter stderr, Environment environment) @trusted
{
    string phpTool, phpVersion;
    primaryPHPTool(environment, phpTool, phpVersion);
    auto frankenPHPVersion = environment.toolVersion("frankenphp").strip();
    if (phpTool.length == 0 || frankenPHPVersion.length == 0)
        return;

    writeOutf(
        stderr,
        "warning: environment %s selects %s %s for the php shim instead of FrankenPHP %s's bundled PHP; their PHP versions may differ\n",
        quoteGoString(environment.name),
        phpTool,
        phpVersion,
        frankenPHPVersion
    );
}

/// Warns when an environment enables phpMyAdmin alongside a PostgreSQL database.
void writePHPMyAdminPostgreSQLWarning(TextWriter stderr, Environment environment) @trusted
{
    if (!phpMyAdminUsesPostgreSQL(environment))
        return;

    writeOutf(stderr, "warning: %s", phpMyAdminPostgreSQLWarning(environment.name));
}

/// Formats the PHP CLI provider summary shown in `polka list`.
string phpCLIListLabel(Environment environment) @safe
{
    string tool, version_;
    phpCLIProvider(environment, tool, version_);
    if (tool == "frankenphp")
        return "frankenphp:" ~ version_;

    return labelOrUnset(version_);
}

/// Returns `value` or `"unset"` when blank.
string labelOrUnset(string value) @safe pure
{
    if (value.strip().length == 0)
        return "unset";
    return value;
}

/// Formats `engine:version[@port]` or `"unset"`.
string labelDatabase(const(DatabaseConfig)* database) @safe
{
    if (database is null)
        return "unset";

    auto label = format("%s:%s", database.engine, database.version_);
    if (database.port != 0)
        label = format("%s@%d", label, database.port);
    return label;
}

/// Formats Mailpit status summary or `"unset"`.
string labelMailpit(const(MailpitConfig)* mailpit) @safe
{
    if (mailpit is null)
        return "unset";
    return format("%s smtp=%d ui=%s", mailpit.version_, effectiveMailpitSMTPPort(mailpit), mailpitUIURLForConfig(mailpit));
}

/// Formats phpMyAdmin status summary or `"unset"`.
string labelPHPMyAdmin(const(PHPMyAdminConfig)* phpMyAdmin) @safe
{
    if (phpMyAdmin is null)
        return "unset";
    return format("%s ui=%s", phpMyAdmin.version_, phpMyAdminUIURLForConfig(phpMyAdmin));
}

/// Formats Meilisearch status summary or `"unset"`.
string labelMeilisearch(const(MeilisearchConfig)* meilisearch) @safe
{
    if (meilisearch is null)
        return "unset";
    auto auth = meilisearch.masterKey.strip().length > 0 ? "enabled" : "disabled";
    return format("%s http=%s auth=%s", meilisearch.version_, meilisearchURLForConfig(meilisearch), auth);
}

/// Formats Redis status summary or `"unset"`.
string labelRedis(const(RedisConfig)* redis) @safe
{
    if (redis is null)
        return "unset";
    auto auth = redis.password.strip().length > 0 ? "enabled" : "disabled";
    return format("%s url=%s auth=%s", redis.version_, redisURLForConfig(redis), auth);
}

/// Formats RabbitMQ status summary or `"unset"`.
string labelRabbitMQ(const(RabbitMQConfig)* rabbitMQ) @safe
{
    if (rabbitMQ is null)
        return "unset";
    return format(
        "%s amqp=%s management=%s auth=enabled",
        rabbitMQ.version_,
        rabbitMQURLForConfig(rabbitMQ),
        rabbitMQManagementURLForConfig(rabbitMQ)
    );
}

/// Formats Traefik status summary or `"unset"`.
string labelTraefik(const(TraefikConfig)* traefik) @safe
{
    if (traefik is null)
        return "unset";
    return format("%s http=%s", traefik.version_, traefikURLForConfig(traefik));
}

unittest
{
    assert(labelOrUnset("") == "unset");
    assert(labelOrUnset("  ") == "unset");
    assert(labelOrUnset("8.4") == "8.4");

    assert(labelDatabase(null) == "unset");
    auto db = DatabaseConfig("mysql", "8.4", 3307);
    assert(labelDatabase(&db) == "mysql:8.4@3307");

    auto mp = MailpitConfig("1.30", 1125, 8125);
    assert(labelMailpit(&mp) == "1.30 smtp=1125 ui=http://127.0.0.1:8125");

    auto ms = MeilisearchConfig("1.48", 7701, "secret");
    assert(labelMeilisearch(&ms) == "1.48 http=http://127.0.0.1:7701 auth=enabled");

    auto rd = RedisConfig("8.0", 6380, "");
    assert(labelRedis(&rd) == "8.0 url=redis://127.0.0.1:6380 auth=disabled");

    auto rmq = RabbitMQConfig("4.1", 5672, 15672, "guest", "guest");
    assert(labelRabbitMQ(&rmq) == "4.1 amqp=amqp://127.0.0.1:5672 management=http://127.0.0.1:15672 auth=enabled");

    auto tr = TraefikConfig("3.3", 8090);
    assert(labelTraefik(&tr) == "3.3 http=http://127.0.0.1:8090");

    auto pma = PHPMyAdminConfig("5.2", 8081);
    assert(labelPHPMyAdmin(&pma) == "5.2 ui=http://127.0.0.1:8081");
}
