module polka.plugins.manifest_values;

import std.algorithm : canFind, sort;
import std.array : Appender, appender, join;
import std.conv : to;
import std.format : format;
import std.string : indexOf, split, startsWith, strip, toLower;

import polka.config;
import polka.plugins.ids;
import polka.plugins.framework_composer;
import polka.plugins.framework_php;
import polka.support.yaml;

/// Structured expression binding a framework runtime or config value.
/// A `FrameworkValue` is either a scalar literal (`hasLiteral == true`) or
/// holds an expression with `from`, `byEngine`, or `format`, plus optional `defaultValue`.
struct FrameworkValue
{
    bool hasLiteral = false;
    YamlValue literal;
    string from;
    bool hasByEngine = false;
    FrameworkValue[string] byEngine;
    FrameworkFormat* format;
    FrameworkValue* defaultValue;

    /// Deep-copies a `FrameworkValue`.
    FrameworkValue clone() const @safe pure
    {
        FrameworkValue copy;
        copy.hasLiteral = hasLiteral;
        copy.literal = literal.clone();
        copy.from = from;
        copy.hasByEngine = hasByEngine;
        foreach (k, ref v; byEngine)
            copy.byEngine[k] = v.clone();
        if (format !is null)
        {
            copy.format = new FrameworkFormat();
            *copy.format = format.clone();
        }
        if (defaultValue !is null)
        {
            copy.defaultValue = new FrameworkValue();
            *copy.defaultValue = defaultValue.clone();
        }
        return copy;
    }

    /// Enumerates immediate child operands in deterministic key order for validation.
    const(FrameworkValue)[] children() const @safe
    {
        const(FrameworkValue)[] outChildren;
        if (defaultValue !is null)
            outChildren ~= *defaultValue;

        auto engineKeys = byEngine.keys;
        engineKeys.sort();
        foreach (k; engineKeys)
            outChildren ~= byEngine[k];

        if (format !is null)
        {
            foreach (child; [format.value, format.host, format.port, format.scheme, format.user, format.password, format.path])
            {
                if (child !is null)
                    outChildren ~= *child;
            }
            auto qKeys = format.query.keys;
            qKeys.sort();
            foreach (k; qKeys)
                outChildren ~= format.query[k];
        }
        return outChildren;
    }

    /// Recursively validates formatter kinds, operands, and engine branches.
    void validate() const @safe
    {
        foreach (engine, _; byEngine)
        {
            if (engine != "mysql" && engine != "mariadb" && engine != "postgresql" && engine != "sqlite")
                throw new Exception(.format("unsupported by-engine key %s", formatGoQuoted(engine)));
        }

        if (format !is null)
        {
            auto f = *format;
            if (f.kind == "string" || f.kind == "version")
            {
                if (f.value is null)
                    throw new Exception(.format("%s formatter requires value", f.kind));
                if (f.host !is null || f.port !is null || f.scheme !is null || f.user !is null
                    || f.password !is null || f.path !is null || f.query.length != 0 || f.omitPort.length != 0)
                {
                    throw new Exception(.format("unexpected fields in %s formatter", f.kind));
                }
                if (f.kind == "string" && (f.prefix.length != 0 || f.stripPrefix.length != 0 || f.minParts != 0))
                    throw new Exception("version fields require version formatter");
                if (f.minParts < 0 || f.minParts > 8)
                    throw new Exception("version min-parts must be between 0 and 8");
            }
            else if (f.kind == "host-port" || f.kind == "url")
            {
                if (f.host is null)
                    throw new Exception(.format("%s formatter requires host", f.kind));
                if (f.kind == "url" && (f.scheme is null || f.path is null))
                    throw new Exception("url formatter requires scheme and path");
                if (f.value !is null || f.prefix.length != 0 || f.stripPrefix.length != 0 || f.minParts != 0)
                    throw new Exception(.format("unexpected fields in %s formatter", f.kind));
                if (f.kind == "host-port" && (f.scheme !is null || f.user !is null || f.password !is null
                    || f.path !is null || f.query.length != 0))
                {
                    throw new Exception("URL fields require url formatter");
                }
                if (f.kind == "url" && f.omitPort.length != 0)
                    throw new Exception("omit-port requires host-port formatter");
                if (f.omitPort.length != 0)
                {
                    int port;
                    try
                    {
                        port = f.omitPort.to!int;
                    }
                    catch (Exception)
                    {
                        throw new Exception("omit-port must be a valid port");
                    }
                    if (port < 1 || port > 65535)
                        throw new Exception("omit-port must be a valid port");
                }
            }
            else
            {
                throw new Exception(.format("unsupported formatter %s", formatGoQuoted(f.kind)));
            }
        }

        foreach (child; children())
            child.validate();
    }
}

/// Typed reusable transformation declared inside a framework manifest `format:` block.
struct FrameworkFormat
{
    string kind;
    FrameworkValue* value;
    FrameworkValue* host;
    FrameworkValue* port;
    FrameworkValue* scheme;
    FrameworkValue* user;
    FrameworkValue* password;
    FrameworkValue* path;
    FrameworkValue[string] query;
    string omitPort;
    string prefix;
    string stripPrefix;
    int minParts = 0;

    FrameworkFormat clone() const @safe pure
    {
        FrameworkFormat copy;
        copy.kind = kind;
        copy.value = cloneValuePtr(value);
        copy.host = cloneValuePtr(host);
        copy.port = cloneValuePtr(port);
        copy.scheme = cloneValuePtr(scheme);
        copy.user = cloneValuePtr(user);
        copy.password = cloneValuePtr(password);
        copy.path = cloneValuePtr(path);
        foreach (k, ref v; query)
            copy.query[k] = v.clone();
        copy.omitPort = omitPort;
        copy.prefix = prefix;
        copy.stripPrefix = stripPrefix;
        copy.minParts = minParts;
        return copy;
    }
}

private FrameworkValue* cloneValuePtr(const(FrameworkValue)* src) @safe pure
{
    if (src is null)
        return null;
    auto p = new FrameworkValue();
    *p = src.clone();
    return p;
}

private T* boxValue(T)(T v) @safe pure
{
    auto p = new T();
    *p = v;
    return p;
}

/// Framework runtime environment declarations.
struct FrameworkRuntimeEnvManifest
{
    bool requiresDatabase = false;
    FrameworkValue[string] values;
}

/// Leaf path update declaration within a `php-array` post-composer action.
struct FrameworkArrayUpdate
{
    string[] path;
    FrameworkValue[string] values;
    string[] order;
}

/// Discriminated union for one declarative `post-composer` step.
struct FrameworkActionManifest
{
    string type;
    string base;
    string path;
    string fallback;
    string fallbackTemplateFile;
    string templateFile;
    string encoding;
    FrameworkValue[string] values;
    string[] order;
    FrameworkArrayUpdate[] updates;
    string insertBefore;
    string include_;
    string marker;
}

/// Post-composer pipeline declaration.
struct FrameworkPostComposerManifest
{
    bool requiresDatabase = false;
    FrameworkActionManifest[] actions;
}

/// Immutable framework declarations parsed from `plugins/manifests/*.yaml`.
struct FrameworkManifest
{
    string id;
    EnvironmentFile defaults;
    string[] phpExtensions;
    string[] databaseEngines;
    string[string] opcacheConfig;
    FrameworkDetection detection;
    FrameworkAppRootConfig appRoot;
    FrameworkValue[string] values;
    FrameworkRuntimeEnvManifest runtimeEnv;
    FrameworkPostComposerManifest postComposer;
}

/// Strictly decodes a `FrameworkValue` from YAML without losing scalar types.
FrameworkValue parseFrameworkValueYAML(const YamlValue node) @safe
{
    if (!node.isMapping())
    {
        if (node.kind == YamlKind.sequence)
            throw new Exception("value must be a scalar or a structured binding");
        FrameworkValue lit;
        lit.hasLiteral = true;
        lit.literal = node.clone();
        return lit;
    }

    FrameworkValue v;
    foreach (entry; node.map)
    {
        switch (entry.key)
        {
            case "from":
                if (!entry.value.isNull())
                    v.from = entry.value.asString();
                break;
            case "by-engine":
                if (!entry.value.isNull())
                {
                    if (!entry.value.isMapping())
                        throw new Exception("by-engine must be a mapping");
                    v.hasByEngine = true;
                    foreach (be; entry.value.map)
                        v.byEngine[be.key] = parseFrameworkValueYAML(be.value);
                }
                break;
            case "format":
                if (!entry.value.isNull())
                    v.format = boxValue(parseFrameworkFormatYAML(entry.value));
                break;
            case "default":
                if (!entry.value.isNull())
                    v.defaultValue = boxValue(parseFrameworkValueYAML(entry.value));
                break;
            default:
                throw new Exception(format("unknown field %s", formatGoQuoted(entry.key)));
        }
    }

    int forms = (v.from.length > 0 ? 1 : 0) + (v.hasByEngine ? 1 : 0) + (v.format !is null ? 1 : 0);
    if (forms != 1)
        throw new Exception("binding requires exactly one of from, by-engine, or format");
    return v;
}

/// Strictly decodes a `FrameworkFormat` mapping from YAML.
FrameworkFormat parseFrameworkFormatYAML(const YamlValue node) @safe
{
    if (!node.isMapping())
        throw new Exception("format must be a mapping");

    FrameworkFormat f;
    foreach (entry; node.map)
    {
        switch (entry.key)
        {
            case "kind":
                if (!entry.value.isNull()) f.kind = entry.value.asString();
                break;
            case "value":
                if (!entry.value.isNull()) f.value = boxValue(parseFrameworkValueYAML(entry.value));
                break;
            case "host":
                if (!entry.value.isNull()) f.host = boxValue(parseFrameworkValueYAML(entry.value));
                break;
            case "port":
                if (!entry.value.isNull()) f.port = boxValue(parseFrameworkValueYAML(entry.value));
                break;
            case "scheme":
                if (!entry.value.isNull()) f.scheme = boxValue(parseFrameworkValueYAML(entry.value));
                break;
            case "user":
                if (!entry.value.isNull()) f.user = boxValue(parseFrameworkValueYAML(entry.value));
                break;
            case "password":
                if (!entry.value.isNull()) f.password = boxValue(parseFrameworkValueYAML(entry.value));
                break;
            case "path":
                if (!entry.value.isNull()) f.path = boxValue(parseFrameworkValueYAML(entry.value));
                break;
            case "query":
                if (!entry.value.isNull())
                {
                    if (!entry.value.isMapping())
                        throw new Exception("format.query must be a mapping");
                    foreach (qe; entry.value.map)
                        f.query[qe.key] = parseFrameworkValueYAML(qe.value);
                }
                break;
            case "omit-port":
                if (!entry.value.isNull()) f.omitPort = entry.value.asString();
                break;
            case "prefix":
                if (!entry.value.isNull()) f.prefix = entry.value.asString();
                break;
            case "strip-prefix":
                if (!entry.value.isNull()) f.stripPrefix = entry.value.asString();
                break;
            case "min-parts":
                if (!entry.value.isNull()) f.minParts = cast(int) entry.value.asInt();
                break;
            default:
                throw new Exception(format("unknown format field %s", formatGoQuoted(entry.key)));
        }
    }
    return f;
}

/// Validates value references, cycles, declared engine coverage, and action values as a graph.
void validateFrameworkManifestValues(const FrameworkManifest m) @safe
{
    FrameworkValue[string] all;
    foreach (key, ref value; m.values)
        all["values." ~ key] = value.clone();

    foreach (key, ref value; m.runtimeEnv.values)
    {
        if (!validFrameworkVariable(key))
            throw new Exception(format("invalid runtime variable name %s", formatGoQuoted(key)));
        all["runtime-env." ~ key] = value.clone();
    }

    int[string] state;

    struct GraphChecker
    {
        const FrameworkManifest m;
        FrameworkValue[string] all;
        int[string] state;

        void check(const FrameworkValue v) @safe
        {
            v.validate();
            if (v.hasByEngine)
            {
                foreach (engine; m.databaseEngines)
                {
                    if ((engine in v.byEngine) is null)
                        throw new Exception(format("by-engine is missing declared database engine %s", formatGoQuoted(engine)));
                }
            }
            if (v.from.length > 0)
            {
                if (v.from.startsWith("database."))
                {
                    auto field = v.from["database.".length .. $];
                    if (field != "engine" && field != "version" && field != "host" && field != "port"
                        && field != "name" && field != "user" && field != "password")
                    {
                        throw new Exception(format("unknown reference %s", formatGoQuoted(v.from)));
                    }
                }
                else if ((v.from in all) is null)
                {
                    throw new Exception(format("unknown reference %s", formatGoQuoted(v.from)));
                }
                else
                {
                    visit(v.from);
                }
            }
            foreach (child; v.children())
                check(child);
        }

        void visit(string key) @safe
        {
            auto st = key in state;
            if (st !is null && *st == 1)
                throw new Exception(format("reference cycle at %s", formatGoQuoted(key)));
            if (st !is null && *st == 2)
                return;
            state[key] = 1;
            try
            {
                check(all[key]);
            }
            catch (Exception e)
            {
                throw new Exception(format("%s: %s", key, e.msg));
            }
            state[key] = 2;
        }
    }

    auto checker = GraphChecker(m, all, state);

    auto sortedAllKeys = all.keys;
    sortedAllKeys.sort();
    foreach (key; sortedAllKeys)
        checker.visit(key);

    foreach (i, action; m.postComposer.actions)
    {
        auto actionValKeys = action.values.keys;
        actionValKeys.sort();
        foreach (key; actionValKeys)
        {
            if (action.type == "dotenv" && !validFrameworkVariable(key))
                throw new Exception(format("invalid dotenv variable name %s", formatGoQuoted(key)));
            try
            {
                checker.check(action.values[key]);
            }
            catch (Exception e)
            {
                throw new Exception(format("action %d value %s: %s", i, formatGoQuoted(key), e.msg));
            }
        }
        foreach (update; action.updates)
        {
            auto uKeys = update.values.keys;
            uKeys.sort();
            foreach (key; uKeys)
            {
                try
                {
                    checker.check(update.values[key]);
                }
                catch (Exception e)
                {
                    throw new Exception(format("action %d: %s", i, e.msg));
                }
            }
        }
    }
}

/// Validates runtime env and dotenv variable names (including dotted CodeIgniter keys).
bool validFrameworkVariable(string key) @safe pure nothrow @nogc
{
    if (key.length == 0)
        return false;
    foreach (i, char c; key)
    {
        if ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
            || (i > 0 && ((c >= '0' && c <= '9') || c == '.')))
        {
            continue;
        }
        return false;
    }
    return true;
}

/// Per-invocation evaluator state with memoized reference cache and private normalized database copy.
final class FrameworkValueState
{
    const FrameworkManifest manifest;
    DatabaseCredentials* database;
    YamlValue[string] memo;

    this(const FrameworkManifest m, const RuntimeEnvContext ctx) @safe pure
    {
        this(m, ctx.environment, ctx.database);
    }

    this(const FrameworkManifest m, const Environment env, const(DatabaseCredentials)* rawDB) @safe pure
    {
        manifest = m;
        DatabaseCredentials* db = null;
        if (rawDB !is null)
        {
            DatabaseCredentials copy = *rawDB;
            if (copy.engine.length == 0 && env.database !is null)
                copy.engine = env.database.engine;
            db = normalizeDatabaseCredentials(copy);
        }
        if (db !is null)
        {
            if (db.engine.length == 0)
                db.engine = "mysql";
            if (env.database !is null && env.database.version_.length > 0)
                db.version_ = env.database.version_;
            else if (auto ver = env.toolVersion(db.engine))
            {
                if (ver.length > 0)
                    db.version_ = ver;
            }
        }
        database = db;
    }

    /// Resolves a validated reference selector, memoizing named values for this invocation.
    YamlValue reference(string refName) @safe
    {
        if (auto cached = refName in memo)
            return cached.clone();

        if (refName.startsWith("database."))
        {
            if (database is null)
                throw new Exception(format("reference %s requires managed database credentials", formatGoQuoted(refName)));
            auto db = database;
            auto field = refName["database.".length .. $];
            switch (field)
            {
                case "engine":   return YamlValue.fromString(db.engine);
                case "version":  return YamlValue.fromString(db.version_);
                case "host":     return YamlValue.fromString(db.host);
                case "port":     return YamlValue.fromString(db.port.to!string);
                case "name":     return YamlValue.fromString(db.databaseName);
                case "user":     return YamlValue.fromString(db.user);
                case "password": return YamlValue.fromString(db.password);
                default:
                    throw new Exception(format("unknown reference %s", formatGoQuoted(refName)));
            }
        }

        const(FrameworkValue)* found = null;
        if (refName.startsWith("values."))
            found = refName["values.".length .. $] in manifest.values;
        else if (refName.startsWith("runtime-env."))
            found = refName["runtime-env.".length .. $] in manifest.runtimeEnv.values;

        if (found is null)
            throw new Exception(format("unknown reference %s", formatGoQuoted(refName)));

        YamlValue result;
        try
        {
            result = eval(*found);
        }
        catch (Exception e)
        {
            throw new Exception(format("resolve %s: %s", refName, e.msg));
        }
        memo[refName] = result.clone();
        return result;
    }

    /// Evaluates one scalar expression and applies an optional null/empty default.
    YamlValue eval(const FrameworkValue v) @safe
    {
        YamlValue result = v.hasLiteral ? v.literal.clone() : YamlValue.nullValue();
        if (v.from.length > 0)
        {
            result = reference(v.from);
        }
        else if (v.hasByEngine)
        {
            if (database is null)
                throw new Exception("by-engine requires managed database credentials");
            auto branch = database.engine in v.byEngine;
            if (branch is null)
                throw new Exception(.format("by-engine has no value for engine %s", formatGoQuoted(database.engine)));
            result = eval(*branch);
        }
        else if (v.format !is null)
        {
            result = YamlValue.fromString(formatValue(*v.format));
        }

        if (v.defaultValue !is null
            && (result.kind == YamlKind.null_
                || (result.kind == YamlKind.string_ && result.strVal.length == 0)))
        {
            return eval(*v.defaultValue);
        }
        return result;
    }

    /// Resolves an optional operand to text without exposing values in errors.
    string text(const(FrameworkValue)* v) @safe
    {
        if (v is null)
            return "";
        return frameworkScalarText(eval(*v));
    }

    /// Evaluates typed formatter expressions with RFC-3986 / Go `net/url` parity.
    string formatValue(const FrameworkFormat f) @safe
    {
        if (f.kind == "string" || f.kind == "version")
        {
            auto val = text(f.value);
            if (f.kind == "string")
                return val;
            val = val.strip();
            if (val.length == 0)
                return "";
            if (f.stripPrefix.length > 0 && val.length >= f.stripPrefix.length
                && val[0 .. f.stripPrefix.length].toLower() == f.stripPrefix.toLower())
            {
                val = val[f.stripPrefix.length .. $].strip();
            }
            auto parts = val.split(".");
            while (cast(int) parts.length < f.minParts)
                parts ~= "0";
            return f.prefix ~ parts.join(".");
        }

        auto host = text(f.host);
        auto port = text(f.port);
        auto scheme = text(f.scheme);
        auto user = text(f.user);
        auto password = text(f.password);
        auto pathname = text(f.path);

        if (host.length == 0)
            throw new Exception(.format("%s formatter requires a nonempty host", f.kind));
        if (port.length > 0)
        {
            int n;
            try
            {
                n = port.to!int;
            }
            catch (Exception)
            {
                throw new Exception(.format("%s formatter requires a valid port", f.kind));
            }
            if (n < 1 || n > 65535)
                throw new Exception(.format("%s formatter requires a valid port", f.kind));
        }
        if (f.kind == "host-port")
        {
            if (port.length == 0 || port == f.omitPort)
                return host;
            return netJoinHostPort(stripBrackets(host), port);
        }

        if (port.length > 0)
            host = netJoinHostPort(stripBrackets(host), port);
        else if (host.canFind(':'))
            host = "[" ~ stripBrackets(host) ~ "]";

        string[string] queryMap;
        foreach (k, val; f.query)
        {
            auto t = text(&val);
            if (t.length > 0)
                queryMap[k] = t;
        }

        if (scheme.length == 0 || host.length == 0)
            throw new Exception("url formatter requires nonempty scheme and host");

        auto urlBuf = appender!string();
        urlBuf.put(scheme);
        urlBuf.put("://");
        if (f.user !is null || f.password !is null)
        {
            urlBuf.put(encodeUserPassword(user));
            urlBuf.put(':');
            urlBuf.put(encodeUserPassword(password));
            urlBuf.put('@');
        }
        urlBuf.put(host);
        urlBuf.put('/');
        urlBuf.put(encodePathSegment(pathname));

        if (queryMap.length > 0)
        {
            auto qKeys = queryMap.keys;
            qKeys.sort();
            urlBuf.put('?');
            foreach (idx, qk; qKeys)
            {
                if (idx > 0)
                    urlBuf.put('&');
                urlBuf.put(encodeQueryComponent(qk));
                urlBuf.put('=');
                urlBuf.put(encodeQueryComponent(queryMap[qk]));
            }
        }
        return urlBuf.data;
    }
}

/// Converts a scalar YAML value into runtime env / dotenv text representation.
string frameworkScalarText(const YamlValue value) @safe
{
    if (value.kind == YamlKind.null_)
        return "";
    if (value.kind == YamlKind.float_)
        return format("%g", value.floatVal);
    return value.asString();
}

/// Orders map keys with explicit `order` prefix first, then sorted remaining keys.
string[] orderedFrameworkKeys(const(FrameworkValue[string]) values, const(string)[] order) @safe pure
{
    string[] keys = order.dup;
    auto allKeys = values.keys;
    allKeys.sort();
    foreach (key; allKeys)
    {
        if (!order.canFind(key))
            keys ~= key;
    }
    return keys;
}

/// Evaluates declared `runtime-env` bindings for a `RuntimeEnvContext`.
string[string] frameworkManifestRuntimeEnv(const RuntimeEnvContext ctx, const FrameworkManifest m) @safe
{
    auto state = new FrameworkValueState(m, ctx);
    if (m.runtimeEnv.requiresDatabase && state.database is null)
        return null;

    string[string] result;
    auto keys = m.runtimeEnv.values.keys;
    keys.sort();
    foreach (key; keys)
    {
        YamlValue val;
        try
        {
            val = state.reference("runtime-env." ~ key);
        }
        catch (Exception e)
        {
            throw new Exception(format("framework %s: %s", formatGoQuoted(m.id), e.msg));
        }
        result[key] = frameworkScalarText(val);
    }
    return result;
}

// ============================================================================
// Go net/url / net.JoinHostPort Parity Helpers
// ============================================================================

private string stripBrackets(string host) @safe pure
{
    size_t start = 0;
    size_t end = host.length;
    while (start < end && (host[start] == '[' || host[start] == ']'))
        start++;
    while (end > start && (host[end - 1] == '[' || host[end - 1] == ']'))
        end--;
    return host[start .. end];
}

private string netJoinHostPort(string host, string port) @safe pure
{
    if (host.canFind(':'))
        return "[" ~ host ~ "]:" ~ port;
    return host ~ ":" ~ port;
}

private bool isUnreservedURLByte(ubyte c) @safe pure nothrow @nogc
{
    return (c >= 'A' && c <= 'Z')
        || (c >= 'a' && c <= 'z')
        || (c >= '0' && c <= '9')
        || c == '-' || c == '_' || c == '.' || c == '~';
}

private void appendUpperHexPercent(ref Appender!string buf, ubyte c) @safe pure
{
    static immutable char[] hex = "0123456789ABCDEF";
    buf.put('%');
    buf.put(hex[(c >> 4) & 0xF]);
    buf.put(hex[c & 0xF]);
}

/// Matches Go `net/url.escape(s, encodeUserPassword)`.
private string encodeUserPassword(string s) @safe pure
{
    auto buf = appender!string();
    foreach (ubyte c; cast(const(ubyte)[]) s)
    {
        if (isUnreservedURLByte(c) || c == '$' || c == '&' || c == '+'
            || c == ',' || c == ';' || c == '=')
        {
            buf.put(cast(char) c);
        }
        else
        {
            appendUpperHexPercent(buf, c);
        }
    }
    return buf.data;
}

/// Matches Go `net/url.PathEscape(s)` (`encodePathSegment`).
private string encodePathSegment(string s) @safe pure
{
    auto buf = appender!string();
    foreach (ubyte c; cast(const(ubyte)[]) s)
    {
        if (isUnreservedURLByte(c) || c == '$' || c == '&' || c == '+'
            || c == ',' || c == ';' || c == '=' || c == ':' || c == '@')
        {
            buf.put(cast(char) c);
        }
        else
        {
            appendUpperHexPercent(buf, c);
        }
    }
    return buf.data;
}

/// Matches Go `net/url.QueryEscape(s)` (`encodeQueryComponent`).
private string encodeQueryComponent(string s) @safe pure
{
    auto buf = appender!string();
    foreach (ubyte c; cast(const(ubyte)[]) s)
    {
        if (c == ' ')
            buf.put('+');
        else if (isUnreservedURLByte(c))
            buf.put(cast(char) c);
        else
            appendUpperHexPercent(buf, c);
    }
    return buf.data;
}

unittest
{
    assert(encodeUserPassword("a@b") == "a%40b");
    assert(encodeUserPassword("p:/?#%") == "p%3A%2F%3F%23%25");
    assert(encodePathSegment("a/b") == "a%2Fb");
    assert(encodeQueryComponent("mariadb 11.8") == "mariadb+11.8");
    assert(netJoinHostPort(stripBrackets("::1"), "5432") == "[::1]:5432");
    assert(netJoinHostPort(stripBrackets("[::1]"), "5432") == "[::1]:5432");
    assert(netJoinHostPort("127.0.0.1", "3306") == "127.0.0.1:3306");
    assert(validFrameworkVariable("database.default.hostname"));
    assert(!validFrameworkVariable("BAD=KEY"));
}
