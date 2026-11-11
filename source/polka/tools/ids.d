module polka.tools.ids;

import std.regex : matchFirst, regex;
import std.string : strip;

/// Managed tool identifier and dispatch command constants.
enum string PHP = "php";
enum string PHPZTS = "php-zts";
enum string PHPDevel = "php-devel";
enum string PHPSDK = "php-sdk";
enum string FrankenPHP = "frankenphp";
enum string Composer = "composer";
enum string PIE = "pie";
enum string NodeJS = "nodejs";
enum string Node = "node";
enum string NPM = "npm";
enum string NPX = "npx";
enum string Yarn = "yarn";
enum string Mago = "mago";
enum string Nginx = "nginx";
enum string Apache = "apache";
enum string Mailpit = "mailpit";
enum string PHPMyAdmin = "phpmyadmin";
enum string Meilisearch = "meilisearch";
enum string Redis = "redis";
enum string RedisCLI = "redis-cli";
enum string RabbitMQ = "rabbitmq";
enum string Erlang = "erlang";
enum string Traefik = "traefik";
enum string RoadRunner = "roadrunner";
enum string MySQL = "mysql";
enum string MariaDB = "mariadb";
enum string PostgreSQL = "postgresql";
enum string PSQL = "psql";
enum string SQLite = "sqlite";

/// Pinned binary tools release paired with managed Windows PHP extension builds.
enum string DefaultPHPSDKVersion = "2.8.3";

/// Checks whether `name` is a valid tool/extension/setting identifier.
bool isValidName(string name) @safe
{
    auto re = regex(`^[A-Za-z0-9][A-Za-z0-9._-]*$`);
    return !name.matchFirst(re).empty;
}

/// Checks whether `ver` matches allowed version syntax.
bool isValidVersionFormat(string ver) @safe
{
    auto re = regex(`^[A-Za-z0-9][A-Za-z0-9._-]*$`);
    return !ver.matchFirst(re).empty;
}

/// Validates a configured tool version, throwing a contextual error on failure.
void validateVersion(string tool, string ver) @safe
{
    auto trimmed = ver.strip();
    if (trimmed.length == 0)
        throw new Exception(tool ~ " version cannot be empty");
    if (!isValidVersionFormat(trimmed))
    {
        import std.format : format;
        throw new Exception(format(`invalid %s version "%s": use letters, numbers, dots, dashes, or underscores`, tool, ver));
    }
}

/// Reports whether `port` is a valid TCP/UDP port number (1..65535).
bool validPort(int port) @safe pure nothrow @nogc
{
    return port >= 1 && port <= 65535;
}
