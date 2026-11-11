module polka.service.context;

import std.file : exists, isDir;
import std.format : format;
import std.path : buildNormalizedPath, buildPath;
import std.string : strip, toLower;

import polka.config;
import polka.service.process : currentEnvList;
import polka.tools;

/// Tool identifier aliases mirrored across `polka.service`.
enum string toolPHP = PHP;
enum string toolNginx = Nginx;
enum string toolMailpit = Mailpit;
enum string toolPHPMyAdmin = PHPMyAdmin;
enum string toolMeilisearch = Meilisearch;
enum string toolRedis = Redis;
enum string toolRabbitMQ = RabbitMQ;
enum string toolErlang = Erlang;
enum string toolTraefik = Traefik;
enum string toolMySQL = MySQL;
enum string toolMariaDB = MariaDB;
enum string toolPostgreSQL = PostgreSQL;

/// Returned certificate and private key paths for managed HTTPS listeners.
struct TLSKeyPair
{
    string certPath;
    string keyPath;
}

/// Carries project-local paths and adapters needed by managed service runtimes
/// without introducing a dependency on `polka.backend`.
struct ServiceContext
{
    string projectDir;
    string rootDir;
    string envsDir;
    string binDir;
    string cacheDir;
    Environment environment;
    Registry registry;
    string[] delegate() runtimeEnvFn;
    TLSKeyPair delegate(string host) tlsCertFn;
    void delegate(string msg) warnFn;

    /// Returns the explicit tool registry or constructs the built-in default registry.
    Registry effectiveRegistry()
    {
        if (registry !is null)
            return registry;
        return newDefaultRegistry();
    }

    /// Formats a warning message and forwards it to `warnFn` when configured.
    void warnf(Args...)(string fmtStr, Args args)
    {
        if (warnFn is null)
            return;
        warnFn(format(fmtStr, args));
    }

    /// Emits a plain warning string to `warnFn` when configured.
    void warn(string msg)
    {
        if (warnFn !is null)
            warnFn(msg);
    }

    /// Returns the runtime environment variables slice (`KEY=VALUE`).
    string[] runtimeEnv()
    {
        if (runtimeEnvFn is null)
            return currentEnvList();
        return runtimeEnvFn();
    }

    /// Resolves certificate and key paths for `host` using the configured TLS resolver.
    TLSKeyPair tlsCertificate(string host)
    {
        if (tlsCertFn is null)
            throw new Exception("tls certificate resolver is not configured");
        return tlsCertFn(host);
    }

    /// Reports whether `tool` is registered in the effective registry.
    bool hasTool(string tool)
    {
        ToolPlugin p;
        return effectiveRegistry().plugin(tool, p);
    }

    /// Warns and returns `true` when a configured service has no registered tool plugin.
    bool skipMissingTool(string serviceName, string tool)
    {
        if (hasTool(tool))
            return false;
        warnf(`Skipping %s for environment "%s": matching tool "%s" is not registered.` ~ "\n", serviceName, environment.name, tool);
        return true;
    }

    /// Resolves the installed executable path for `tool` at `version_`.
    string resolveInstalledTool(string tool, string version_)
    {
        return polka.service.context.resolveInstalledTool(effectiveRegistry(), envsDir, tool, version_);
    }
}

alias Context = ServiceContext;

/// Resolves the installed executable for a manifest tool under `root/<tool>/<version>`.
string resolveInstalledTool(Registry registry, string root, string tool, string version_)
{
    if (registry is null)
        registry = newDefaultRegistry();

    auto candidates = registry.installCandidates(root, tool, version_);
    if (candidates.length == 0)
        throw new Exception(format(`unsupported tool "%s"`, tool));

    foreach (candidate; candidates)
    {
        if (!exists(candidate))
            continue;
        if (isDir(candidate))
            continue;
        return candidate;
    }

    throw new Exception(format(`%s version "%s" is not installed under %s`, tool, version_, buildNormalizedPath(buildPath(root, tool, version_))));
}

/// Returns the predictable log directory root `<rootDir>/run/<tool>/<environment>` for a managed tool.
string toolLogRoot(string rootDir, string tool, string environmentName) @safe pure
{
    auto name = environmentName.strip();
    if (name.length == 0)
        name = "current";

    return buildPath(rootDir, "run", tool.strip().toLower(), name);
}
