module polka.config.workers;

import std.algorithm : canFind, sort;
import std.array : appender;
import std.format : format;
import std.regex : matchFirst, regex;
import std.string : strip, toLower;

/// Restricts worker names to filesystem- and log-friendly identifiers.
private auto validWorkerNameRe() @safe
{
    return regex(`^[A-Za-z0-9][A-Za-z0-9._-]*$`);
}

/// YAML shape of one background worker definition under `workers`.
struct WorkerConfig
{
    string command;
    int replicas = 0;
    string dir;
    string[string] env;

    bool opEquals(const WorkerConfig other) const @safe pure nothrow
    {
        if (command != other.command || replicas != other.replicas || dir != other.dir)
            return false;
        if (env.length != other.env.length)
            return false;
        foreach (k, v; env)
        {
            auto p = k in other.env;
            if (p is null || *p != v)
                return false;
        }
        return true;
    }
}

/// Canonicalizes environment variable names (trims keys, returns null if empty).
string[string] normalizeEnvironmentVariables(const(string[string]) values) @safe pure
{
    if (values.length == 0)
        return null;
    string[string] normalized;
    foreach (key, value; values)
    {
        normalized[key.strip()] = value;
    }
    return normalized;
}

/// Canonicalizes worker definitions: names lowercased/trimmed, empty dropped, negative replicas clamped to 0.
WorkerConfig[string] normalizeWorkersConfig(const(WorkerConfig[string]) workers) @safe pure
{
    if (workers.length == 0)
        return null;

    WorkerConfig[string] normalized;
    foreach (name, worker; workers)
    {
        auto normalizedName = name.strip().toLower();
        auto command = worker.command.strip();
        if (normalizedName.length == 0 || command.length == 0)
            continue;

        int replicas = worker.replicas;
        if (replicas < 0)
            replicas = 0;
        normalized[normalizedName] = WorkerConfig(
            command,
            replicas,
            worker.dir.strip(),
            normalizeEnvironmentVariables(worker.env)
        );
    }
    if (normalized.length == 0)
        return null;
    return normalized;
}

/// Validates worker definitions; throws on invalid name, missing/unsplittable command, or bad env key.
void validateWorkersConfig(const(WorkerConfig[string]) workers) @safe
{
    auto re = validWorkerNameRe();
    foreach (name; sortedWorkerNames(workers))
    {
        if (name.matchFirst(re).empty)
            throw new Exception(format(`invalid worker name "%s": use letters, numbers, dots, dashes, or underscores`, name));

        auto worker = workers[name];
        if (worker.command.strip().length == 0)
            throw new Exception(format(`worker "%s" requires a command`, name));

        try
        {
            splitWorkerCommand(worker.command);
        }
        catch (Exception e)
        {
            throw new Exception(format(`worker "%s": %s`, name, e.msg));
        }

        foreach (key, _; worker.env)
        {
            auto trimmed = key.strip();
            if (trimmed.length == 0 || trimmed.canFind('='))
                throw new Exception(format(`worker "%s" has an invalid environment variable name "%s"`, name, key));
        }
    }
}

/// Resolves the replica count for a worker, defaulting to 1 when unset (<= 0).
int effectiveWorkerReplicas(const WorkerConfig worker) @safe pure nothrow @nogc
{
    return worker.replicas <= 0 ? 1 : worker.replicas;
}

/// Returns worker names in sorted deterministic order (null if empty).
string[] sortedWorkerNames(const(WorkerConfig[string]) workers) @safe pure
{
    if (workers.length == 0)
        return null;
    string[] names;
    names.reserve(workers.length);
    foreach (name, _; workers)
        names ~= name;
    names.sort();
    return names;
}

/// Splits a worker command string into argv, honoring single and double quotes.
string[] splitWorkerCommand(string command) @safe pure
{
    string[] args;
    auto current = appender!string();
    char quote = 0;
    bool inArg = false;

    foreach (char r; command)
    {
        if (quote != 0)
        {
            if (r == quote)
                quote = 0;
            else
                current.put(r);
        }
        else if (r == '\'' || r == '"')
        {
            quote = r;
            inArg = true;
        }
        else if (r == ' ' || r == '\t' || r == '\n' || r == '\r')
        {
            if (inArg)
            {
                args ~= current.data;
                current = appender!string();
                inArg = false;
            }
        }
        else
        {
            current.put(r);
            inArg = true;
        }
    }
    if (quote != 0)
        throw new Exception(format(`worker command "%s" has an unbalanced quote`, command));
    if (inArg)
        args ~= current.data;
    if (args.length == 0)
        throw new Exception("worker command must not be empty");
    return args;
}

unittest
{
    // TestSplitWorkerCommand
    assert(splitWorkerCommand("php artisan queue:work") == ["php", "artisan", "queue:work"]);
    assert(splitWorkerCommand("  php \t artisan   queue:work \n") == ["php", "artisan", "queue:work"]);
    assert(splitWorkerCommand(`php artisan "queue:work --sleep 3"`) == ["php", "artisan", "queue:work --sleep 3"]);
    assert(splitWorkerCommand("php 'my script.php'") == ["php", "my script.php"]);
    assert(splitWorkerCommand(`php --flag="a b"`) == ["php", "--flag=a b"]);

    try { splitWorkerCommand("   "); assert(0); } catch (Exception e) { assert(e.msg.canFind("must not be empty")); }
    try { splitWorkerCommand(`php "artisan`); assert(0); } catch (Exception e) { assert(e.msg.canFind("unbalanced quote")); }

    // TestNormalizeWorkersConfig
    WorkerConfig[string] raw;
    raw[" Queue "] = WorkerConfig("  php artisan queue:work  ", 2, " . ", [" TRIES ": "3"]);
    raw["empty"] = WorkerConfig("   ");
    raw["  "] = WorkerConfig("php run");
    raw["negative"] = WorkerConfig("php run", -3);

    auto normalized = normalizeWorkersConfig(raw);
    assert(normalized.length == 2);
    assert(normalized["queue"] == WorkerConfig("php artisan queue:work", 2, ".", ["TRIES": "3"]));
    assert(normalized["negative"] == WorkerConfig("php run", 0));
    assert(normalizeWorkersConfig(null) is null);

    WorkerConfig[string] allDropped;
    allDropped["gone"] = WorkerConfig(" ");
    assert(normalizeWorkersConfig(allDropped) is null);

    // TestValidateWorkersConfig
    WorkerConfig[string] valid;
    valid["queue"] = WorkerConfig("php artisan queue:work", 0, "", ["TRIES": "3"]);
    validateWorkersConfig(valid);
    validateWorkersConfig(null);

    try { validateWorkersConfig(["bad name": WorkerConfig("php run")]); assert(0); } catch (Exception e) { assert(e.msg.canFind("invalid worker name")); }
    try { validateWorkersConfig(["queue": WorkerConfig()]); assert(0); } catch (Exception e) { assert(e.msg.canFind("requires a command")); }
    try { validateWorkersConfig(["queue": WorkerConfig(`php "broken`)]); assert(0); } catch (Exception e) { assert(e.msg.canFind("unbalanced quote")); }
    try { validateWorkersConfig(["queue": WorkerConfig("php run", 0, "", ["BAD=KEY": "1"])]); assert(0); } catch (Exception e) { assert(e.msg.canFind("invalid environment variable name")); }

    // TestEffectiveWorkerReplicas & SortedWorkerNames
    assert(effectiveWorkerReplicas(WorkerConfig()) == 1);
    assert(effectiveWorkerReplicas(WorkerConfig("", 3)) == 3);
    assert(sortedWorkerNames(["scheduler": WorkerConfig("b"), "queue": WorkerConfig("a")]) == ["queue", "scheduler"]);
    assert(sortedWorkerNames(null) is null);
}
