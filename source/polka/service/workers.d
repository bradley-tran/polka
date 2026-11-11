module polka.service.workers;

import core.thread : Thread;
import core.time : Duration, dur;
import std.algorithm : sort;
import std.conv : to;
import std.datetime : Clock, DateTime, SysTime, UTC;
import std.file : exists, readText, remove;
import std.format : format;
import std.json : JSONType, JSONValue, parseJSON;
import std.path : buildPath, isAbsolute;
import std.string : strip;

import polka.config;
import polka.service.context;
import polka.service.process;
import polka.support.fs : cleanPath, isSymlinkPath, octal;
import polka.tools.cache : jsonLongField, jsonStringField;

enum string managedWorkersStateDirectory = "run";
enum string managedWorkersStateSubdirectory = "workers";
enum Duration managedWorkersPollInterval = dur!"msecs"(50);

/// Duration `startWorkerProcess` watches a newly spawned worker so commands that
/// fail immediately (typos, missing scripts) fail loudly rather than recording a dead PID.
enum Duration managedWorkersStartupGrace = dur!"msecs"(750);

/// Persisted runtime state for one worker replica.
struct WorkerProcessState
{
    string name;
    int replica = 0;
    int pid = 0;
    string command;
    string logPath;
    SysTime startedAt;

    bool opEquals(const WorkerProcessState other) const @safe pure nothrow
    {
        return name == other.name &&
            replica == other.replica &&
            pid == other.pid &&
            command == other.command &&
            logPath == other.logPath &&
            startedAt == other.startedAt;
    }
}

/// Persisted runtime state for all workers belonging to an environment.
struct WorkersRuntimeState
{
    string environmentName;
    WorkerProcessState[] processes;

    bool opEquals(const WorkersRuntimeState other) const @safe pure nothrow
    {
        return environmentName == other.environmentName && processes == other.processes;
    }
}

/// Resolved launch specification for all replicas of one configured worker.
struct WorkerSpec
{
    string environmentName;
    string name;
    string command;
    string target;
    string[] args;
    string dir;
    int replicas = 0;
    string logDir;
    string[] env;
}

/// Result returned when starting one worker replica.
struct WorkerStartResult
{
    int pid = 0;
    string logPath;
}

/// Result returned by `WorkersRuntimeHooks.resolveTarget`.
struct WorkerTargetResolution
{
    string target;
    string[] args;
    string[] env;
}

/// Hooks allowing callers (`polka.cli`) and tests to replace worker process side effects
/// while preserving `service -> tools -> config -> support` layering.
struct WorkersRuntimeHooks
{
    WorkerStartResult delegate(WorkerSpec, int replica) startWorker;
    void delegate(WorkerProcessState) stopWorker;
    bool delegate(int pid) pidIsLive;
    string[] delegate(ServiceContext) buildEnv;
    WorkerTargetResolution delegate(ServiceContext ctx, string[] argv, string[] env) resolveTarget;
    SysTime delegate() now;

    /// Returns a copy with unset delegates filled by default implementations.
    WorkersRuntimeHooks withDefaults() const @trusted
    {
        WorkersRuntimeHooks copy = this;
        if (copy.startWorker is null)
            copy.startWorker = (WorkerSpec spec, int replica) => startWorkerProcess(spec, replica);
        if (copy.stopWorker is null)
            copy.stopWorker = (WorkerProcessState st) { stopWorkerProcess(st); };
        if (copy.pidIsLive is null)
            copy.pidIsLive = (int pid) => servicePIDIsLive(pid);
        if (copy.buildEnv is null)
            copy.buildEnv = (ServiceContext ctx) => ctx.runtimeEnv();
        if (copy.resolveTarget is null)
            copy.resolveTarget = (ServiceContext, string[] argv, string[] env) => defaultResolveWorkerTarget(argv, env);
        if (copy.now is null)
            copy.now = () => Clock.currTime(UTC());
        return copy;
    }
}

/// Starts or reconciles configured workers for `ctx.environment`.
WorkersRuntimeState ensureManagedWorkersStarted(
    ServiceContext ctx,
    WorkersRuntimeHooks hooks,
    out bool alreadyStarted
) @trusted
{
    hooks = hooks.withDefaults();
    auto environment = ctx.environment;
    if (environment.workers.length == 0)
    {
        alreadyStarted = true;
        return WorkersRuntimeState.init;
    }

    auto state = loadLiveWorkersState(ctx.rootDir, environment.name, hooks.pidIsLive);
    if (state !is null && workersStateMatchesEnvironment(*state, environment))
    {
        alreadyStarted = true;
        return *state;
    }

    // Reconcile: live state is missing, partial, or built from an older workers configuration.
    if (state !is null)
    {
        stopWorkerProcesses(ctx, state.processes, hooks);
        removeWorkersState(ctx.rootDir, environment.name);
    }

    auto specs = buildWorkerSpecs(ctx, hooks);

    // Individual worker start failures emit warnings rather than aborting other workers or `serve`.
    WorkerProcessState[] started;
    foreach (spec; specs)
    {
        foreach (replica; 1 .. spec.replicas + 1)
        {
            WorkerStartResult res;
            try
            {
                res = hooks.startWorker(spec, replica);
            }
            catch (Exception e)
            {
                ctx.warnf(`Worker "%s" replica %d for environment "%s" failed to start: %s.` ~ "\n", spec.name, replica, environment.name, e.msg);
                continue;
            }

            WorkerProcessState ps;
            ps.name = spec.name;
            ps.replica = replica;
            ps.pid = res.pid;
            ps.command = spec.command;
            ps.logPath = res.logPath;
            ps.startedAt = hooks.now().toUTC();
            started ~= ps;
        }
    }

    if (started.length == 0)
    {
        alreadyStarted = false;
        return WorkersRuntimeState(environment.name, null);
    }

    auto startedState = WorkersRuntimeState(environment.name, started);
    try
    {
        writeWorkersState(workersStatePath(ctx.rootDir, environment.name), startedState);
    }
    catch (Exception e)
    {
        stopWorkerProcesses(ctx, started, hooks);
        throw e;
    }

    alreadyStarted = false;
    return startedState;
}

/// Stops all recorded live worker replicas for `ctx.environment` and deletes the state file.
WorkersRuntimeState stopManagedWorkers(
    ServiceContext ctx,
    WorkersRuntimeHooks hooks,
    out bool alreadyStopped
) @trusted
{
    hooks = hooks.withDefaults();

    auto state = loadLiveWorkersState(ctx.rootDir, ctx.environment.name, hooks.pidIsLive);
    if (state is null)
    {
        alreadyStopped = true;
        return WorkersRuntimeState.init;
    }

    stopWorkerProcesses(ctx, state.processes, hooks);
    removeWorkersState(ctx.rootDir, ctx.environment.name);

    alreadyStopped = false;
    return *state;
}

/// Converts configured workers into sorted, launch-ready `WorkerSpec` records.
WorkerSpec[] buildWorkerSpecs(ServiceContext ctx, WorkersRuntimeHooks hooks) @trusted
{
    hooks = hooks.withDefaults();
    auto environment = ctx.environment;
    auto baseEnv = hooks.buildEnv(ctx);

    auto names = sortedWorkerNames(environment.workers);
    WorkerSpec[] specs;
    specs.reserve(names.length);

    foreach (name; names)
    {
        auto worker = environment.workers[name];
        string[] argv;
        try
        {
            argv = splitWorkerCommand(worker.command);
        }
        catch (Exception e)
        {
            ctx.warnf(`Skipping worker "%s" for environment "%s": %s.` ~ "\n", name, environment.name, e.msg);
            continue;
        }

        // Per-worker env entries follow base env so `envListToAA` gives worker values precedence.
        auto env = baseEnv.dup;
        foreach (key; sortedWorkerEnvKeys(worker.env))
            env ~= key ~ "=" ~ worker.env[key];

        WorkerTargetResolution resolved;
        try
        {
            resolved = hooks.resolveTarget(ctx, argv, env);
        }
        catch (Exception e)
        {
            ctx.warnf(`Skipping worker "%s" for environment "%s": %s.` ~ "\n", name, environment.name, e.msg);
            continue;
        }

        WorkerSpec spec;
        spec.environmentName = environment.name;
        spec.name = name;
        spec.command = worker.command.strip();
        spec.target = resolved.target;
        spec.args = resolved.args;
        spec.dir = resolveWorkerDir(ctx.projectDir, worker.dir);
        spec.replicas = effectiveWorkerReplicas(worker);
        spec.logDir = workersLogDir(ctx.rootDir, environment.name);
        spec.env = resolved.env;
        specs ~= spec;
    }

    return specs;
}

/// Launches one worker replica detached, logging output to `<logDir>/<name>-<replica>.log`
/// and supervising it over `managedWorkersStartupGrace` to detect immediate exits.
WorkerStartResult startWorkerProcess(WorkerSpec spec, int replica) @trusted
{
    auto logPath = workerLogPath(spec.logDir, spec.name, replica);
    auto logFile = openServiceLog(logPath);
    scope (exit)
    {
        if (logFile.isOpen)
            logFile.close();
    }

    auto env = spec.env.dup;
    env ~= "POLKA_WORKER_NAME=" ~ spec.name;
    env ~= "POLKA_WORKER_REPLICA=" ~ to!string(replica);

    int pid = 0;
    try
    {
        pid = spawnDetachedServiceProcess(spec.target, spec.args, logFile, env, spec.dir);
    }
    catch (Exception e)
    {
        throw new Exception("start worker: " ~ e.msg);
    }

    auto deadline = Clock.currTime() + managedWorkersStartupGrace;
    while (Clock.currTime() < deadline)
    {
        if (!servicePIDIsLive(pid))
        {
            try { stopServicePID(pid); } catch (Exception) {} // Best-effort cleanup of exited worker.
            throw new Exception(format("worker %s replica %d exited immediately (see %s)", spec.name, replica, logPath));
        }
        Thread.sleep(managedWorkersPollInterval);
    }

    return WorkerStartResult(pid, logPath);
}

/// Terminates one recorded worker replica.
void stopWorkerProcess(WorkerProcessState state) @trusted
{
    stopServicePID(state.pid);
}

/// Reports whether a worker process PID is still live.
bool workerPIDIsLive(int pid) @trusted
{
    return servicePIDIsLive(pid);
}

/// Reports whether persisted `processes` match configured workers (names, replica counts, commands).
bool workersStateMatchesEnvironment(WorkersRuntimeState state, Environment environment) @safe
{
    struct LiveWorker
    {
        int replicas = 0;
        string command;
    }

    LiveWorker[string] live;
    foreach (process; state.processes)
    {
        auto entryPtr = process.name in live;
        if (entryPtr !is null)
        {
            if (entryPtr.replicas > 0 && entryPtr.command != process.command)
                return false;
            entryPtr.replicas++;
            entryPtr.command = process.command;
        }
        else
        {
            live[process.name] = LiveWorker(1, process.command);
        }
    }

    if (live.length != environment.workers.length)
        return false;

    foreach (name, worker; environment.workers)
    {
        auto entryPtr = name in live;
        if (entryPtr is null)
            return false;
        if (entryPtr.replicas != effectiveWorkerReplicas(worker))
            return false;
        if (entryPtr.command != worker.command.strip())
            return false;
    }

    return true;
}

/// Loads persisted worker state and prunes dead replicas; deletes the state file when none remain live.
WorkersRuntimeState* loadLiveWorkersState(
    string rootDir,
    string environmentName,
    bool delegate(int) pidIsLive = null
) @trusted
{
    if (pidIsLive is null)
        pidIsLive = (int pid) => servicePIDIsLive(pid);

    auto path = workersStatePath(rootDir, environmentName);
    if (!exists(path) && !isSymlinkPath(path))
        return null;

    auto state = loadWorkersState(path);
    if (state is null)
        return null;

    WorkerProcessState[] liveProcesses;
    foreach (process; state.processes)
    {
        if (pidIsLive(process.pid))
            liveProcesses ~= process;
    }

    if (liveProcesses.length == 0)
    {
        if (exists(path) || isSymlinkPath(path))
        {
            try
            {
                remove(path);
            }
            catch (Exception e)
            {
                throw new Exception("remove stale workers state: " ~ e.msg);
            }
        }
        return null;
    }

    state.processes = liveProcesses;
    return state;
}

/// Reads and decodes a persisted workers runtime state JSON file.
WorkersRuntimeState* loadWorkersState(string path) @trusted
{
    auto raw = readText(path);
    JSONValue root;
    try
    {
        root = parseJSON(raw);
    }
    catch (Exception e)
    {
        throw new Exception(format("decode workers state %s: %s", path, e.msg));
    }

    auto st = new WorkersRuntimeState();
    st.environmentName = jsonStringField(root, "environment");

    if (root.type == JSONType.object)
    {
        auto procsPtr = "processes" in root.object;
        if (procsPtr !is null && procsPtr.type == JSONType.array)
        {
            foreach (elem; procsPtr.array)
            {
                WorkerProcessState ps;
                ps.name = jsonStringField(elem, "name");
                ps.replica = cast(int) jsonLongField(elem, "replica", 0);
                ps.pid = cast(int) jsonLongField(elem, "pid", 0);
                ps.command = jsonStringField(elem, "command");
                ps.logPath = jsonStringField(elem, "log_path");
                ps.startedAt = parseServiceTimestamp(jsonStringField(elem, "started_at"));
                st.processes ~= ps;
            }
        }
    }
    return st;
}

/// Writes `state` as formatted JSON with 0644 permissions.
void writeWorkersState(string path, WorkersRuntimeState state) @trusted
{
    JSONValue[] processJSONs;
    processJSONs.reserve(state.processes.length);
    foreach (p; state.processes)
    {
        JSONValue item = JSONValue(cast(JSONValue[string]) null);
        item["name"] = p.name;
        item["replica"] = p.replica;
        item["pid"] = p.pid;
        item["command"] = p.command;
        if (p.logPath.length > 0)
            item["log_path"] = p.logPath;
        item["started_at"] = formatServiceTimestamp(p.startedAt);
        processJSONs ~= item;
    }

    JSONValue root = JSONValue(cast(JSONValue[string]) null);
    root["environment"] = state.environmentName;
    root["processes"] = JSONValue(processJSONs);

    writeServiceJSONFile(path, root, octal!644);
}

string workersStatePath(string rootDir, string environmentName) @safe pure
{
    return buildPath(rootDir, managedWorkersStateDirectory, managedWorkersStateSubdirectory, environmentName ~ ".json");
}

string workersLogDir(string rootDir, string environmentName) @safe pure
{
    return toolLogRoot(rootDir, managedWorkersStateSubdirectory, environmentName);
}

string workerLogPath(string logDir, string name, int replica) @safe
{
    return buildPath(logDir, name ~ "-" ~ to!string(replica) ~ ".log");
}

private void stopWorkerProcesses(ServiceContext ctx, const(WorkerProcessState)[] processes, WorkersRuntimeHooks hooks) @trusted
{
    foreach (process; processes)
    {
        try
        {
            hooks.stopWorker(process);
        }
        catch (Exception e)
        {
            ctx.warnf(`Failed to stop worker "%s" replica %d (pid %d) for environment "%s": %s.` ~ "\n",
                process.name, process.replica, process.pid, ctx.environment.name, e.msg);
        }
    }
}

private void removeWorkersState(string rootDir, string environmentName) @trusted
{
    auto path = workersStatePath(rootDir, environmentName);
    if (!exists(path) && !isSymlinkPath(path))
        return;
    try
    {
        remove(path);
    }
    catch (Exception e)
    {
        throw new Exception("remove workers state: " ~ e.msg);
    }
}

private string resolveWorkerDir(string projectDir, string dir) @safe
{
    auto trimmed = dir.strip();
    if (trimmed.length == 0)
        return projectDir;
    if (isAbsolute(trimmed))
        return cleanPath(trimmed);
    return buildPath(projectDir, trimmed);
}

private string[] sortedWorkerEnvKeys(const(string[string]) env) @safe
{
    if (env.length == 0)
        return null;
    auto keys = env.keys;
    keys.sort();
    return keys;
}

private WorkerTargetResolution defaultResolveWorkerTarget(string[] argv, string[] env) @safe pure
{
    return WorkerTargetResolution(argv[0], argv.length > 1 ? argv[1 .. $].dup : null, env);
}

unittest
{
    import std.array : appender;
    import std.algorithm : canFind, endsWith;
    import std.path : dirName;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    final class WorkersTestHarness
    {
        int nextPID = 100;
        bool[int] livePIDs;
        string[] started;
        int[] stopped;
        string failOn;

        WorkersRuntimeHooks hooks()
        {
            WorkersRuntimeHooks h;
            h.startWorker = (WorkerSpec spec, int replica) {
                auto key = format("%s#%d", spec.name, replica);
                if (failOn == key)
                    throw new Exception("boom");
                nextPID++;
                livePIDs[nextPID] = true;
                started ~= key;
                return WorkerStartResult(nextPID, workerLogPath(spec.logDir, spec.name, replica));
            };
            h.stopWorker = (WorkerProcessState state) {
                livePIDs.remove(state.pid);
                stopped ~= state.pid;
            };
            h.pidIsLive = (int pid) => pid in livePIDs ? livePIDs[pid] : false;
            h.buildEnv = (ServiceContext) => ["BASE=1"];
            h.now = () => SysTime(DateTime(2026, 7, 8, 12, 0, 0), UTC());
            return h;
        }
    }

    static ServiceContext makeWorkersContext(string tempRoot, WorkerConfig[string] workers, out string delegate() warningsOut)
    {
        auto rootDir = buildPath(tempRoot, ".polka");
        auto warnings = appender!string();
        warningsOut = () => warnings.data;

        ServiceContext ctx;
        ctx.projectDir = tempRoot;
        ctx.rootDir = rootDir;
        ctx.environment.name = "default";
        ctx.environment.workers = workers;
        ctx.warnFn = (string msg) { warnings.put(msg); };
        return ctx;
    }

    // TestEnsureManagedWorkersStartedStartsConfiguredReplicas
    {
        auto tmp = createTempDir("polka-workers-start-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto harness = new WorkersTestHarness();
        string delegate() warnings;
        auto ctx = makeWorkersContext(tmp, [
            "scheduler": WorkerConfig("php artisan schedule:work"),
            "queue": WorkerConfig("php artisan queue:work", 2),
        ], warnings);

        bool alreadyStarted = false;
        auto state = ensureManagedWorkersStarted(ctx, harness.hooks(), alreadyStarted);
        assert(!alreadyStarted);
        assert(harness.started == ["queue#1", "queue#2", "scheduler#1"]);
        assert(state.processes.length == 3 && state.environmentName == "default");
        assert(state.processes[0].command == "php artisan queue:work" && state.processes[0].replica == 1);
        assert(state.processes[2].logPath.endsWith("scheduler-1.log"));

        auto persisted = loadWorkersState(workersStatePath(ctx.rootDir, "default"));
        assert(persisted !is null && *persisted == state);
    }

    // TestEnsureManagedWorkersStartedIsIdempotent
    {
        auto tmp = createTempDir("polka-workers-idem-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto harness = new WorkersTestHarness();
        string delegate() warnings;
        auto ctx = makeWorkersContext(tmp, [
            "queue": WorkerConfig("php artisan queue:work", 2),
        ], warnings);

        bool alreadyStarted = false;
        ensureManagedWorkersStarted(ctx, harness.hooks(), alreadyStarted);
        auto startsAfterFirst = harness.started.length;

        auto state = ensureManagedWorkersStarted(ctx, harness.hooks(), alreadyStarted);
        assert(alreadyStarted);
        assert(harness.started.length == startsAfterFirst);
        assert(state.processes.length == 2);
    }

    // TestEnsureManagedWorkersStartedReconcilesDeadReplica
    {
        auto tmp = createTempDir("polka-workers-dead-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto harness = new WorkersTestHarness();
        string delegate() warnings;
        auto ctx = makeWorkersContext(tmp, [
            "queue": WorkerConfig("php artisan queue:work", 2),
        ], warnings);

        bool alreadyStarted = false;
        auto first = ensureManagedWorkersStarted(ctx, harness.hooks(), alreadyStarted);
        harness.livePIDs.remove(first.processes[0].pid);
        auto survivorPID = first.processes[1].pid;

        auto state = ensureManagedWorkersStarted(ctx, harness.hooks(), alreadyStarted);
        assert(!alreadyStarted);
        assert(harness.stopped == [survivorPID]);
        assert(state.processes.length == 2);
        foreach (proc; state.processes)
            assert(proc.pid != survivorPID);
    }

    // TestEnsureManagedWorkersStartedReconcilesChangedCommand
    {
        auto tmp = createTempDir("polka-workers-chg-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto harness = new WorkersTestHarness();
        string delegate() warnings;
        auto ctx = makeWorkersContext(tmp, [
            "queue": WorkerConfig("php artisan queue:work"),
        ], warnings);

        bool alreadyStarted = false;
        auto first = ensureManagedWorkersStarted(ctx, harness.hooks(), alreadyStarted);
        ctx.environment.workers = ["queue": WorkerConfig("php artisan queue:work --tries=3")];

        auto state = ensureManagedWorkersStarted(ctx, harness.hooks(), alreadyStarted);
        assert(!alreadyStarted);
        assert(harness.stopped == [first.processes[0].pid]);
        assert(state.processes[0].command == "php artisan queue:work --tries=3");
    }

    // TestEnsureManagedWorkersStartedWarnsOnReplicaFailure
    {
        auto tmp = createTempDir("polka-workers-partfail-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto harness = new WorkersTestHarness();
        harness.failOn = "queue#2";
        string delegate() warnings;
        auto ctx = makeWorkersContext(tmp, [
            "queue": WorkerConfig("php artisan queue:work", 2),
            "scheduler": WorkerConfig("php artisan schedule:work"),
        ], warnings);

        bool alreadyStarted = false;
        auto state = ensureManagedWorkersStarted(ctx, harness.hooks(), alreadyStarted);
        assert(!alreadyStarted);
        assert(warnings().canFind(`Worker "queue" replica 2 for environment "default" failed to start`));
        assert(harness.started == ["queue#1", "scheduler#1"]);
        assert(harness.stopped.length == 0);
        assert(state.processes.length == 2);

        auto persisted = loadWorkersState(workersStatePath(ctx.rootDir, "default"));
        assert(persisted !is null && persisted.processes.length == 2);
    }

    // TestEnsureManagedWorkersStartedWarnsWhenAllReplicasFail
    {
        auto tmp = createTempDir("polka-workers-allfail-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto harness = new WorkersTestHarness();
        harness.failOn = "queue#1";
        string delegate() warnings;
        auto ctx = makeWorkersContext(tmp, [
            "queue": WorkerConfig("php artisan queue:work"),
        ], warnings);

        bool alreadyStarted = false;
        auto state = ensureManagedWorkersStarted(ctx, harness.hooks(), alreadyStarted);
        assert(!alreadyStarted && state.processes.length == 0);
        assert(warnings().canFind(`Worker "queue" replica 1 for environment "default" failed to start`));
        assert(!exists(workersStatePath(ctx.rootDir, "default")));
    }

    // TestBuildWorkerSpecsSkipsBrokenWorkerWithWarning
    {
        auto tmp = createTempDir("polka-workers-broken-");
        scope (exit) rmdirRecurseQuiet(tmp);
        string delegate() warnings;
        auto ctx = makeWorkersContext(tmp, [
            "broken": WorkerConfig(`php "unbalanced`),
            "queue": WorkerConfig("php artisan queue:work"),
        ], warnings);

        WorkersRuntimeHooks hooks;
        hooks.buildEnv = (ServiceContext) => null;
        auto specs = buildWorkerSpecs(ctx, hooks);
        assert(specs.length == 1 && specs[0].name == "queue");
        assert(warnings().canFind(`Skipping worker "broken" for environment "default"`));
    }

    // TestStopManagedWorkers
    {
        auto tmp = createTempDir("polka-workers-stop-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto harness = new WorkersTestHarness();
        string delegate() warnings;
        auto ctx = makeWorkersContext(tmp, [
            "queue": WorkerConfig("php artisan queue:work", 2),
        ], warnings);

        bool alreadyStarted = false;
        auto started = ensureManagedWorkersStarted(ctx, harness.hooks(), alreadyStarted);

        bool alreadyStopped = false;
        auto state = stopManagedWorkers(ctx, harness.hooks(), alreadyStopped);
        assert(!alreadyStopped);
        assert(state.processes.length == 2 && harness.stopped.length == 2);
        assert(harness.stopped[0] == started.processes[0].pid && harness.stopped[1] == started.processes[1].pid);
        assert(!exists(workersStatePath(ctx.rootDir, "default")));

        stopManagedWorkers(ctx, harness.hooks(), alreadyStopped);
        assert(alreadyStopped);
    }

    // TestBuildWorkerSpecs
    {
        auto tmp = createTempDir("polka-workers-specs-");
        scope (exit) rmdirRecurseQuiet(tmp);
        string delegate() warnings;
        auto ctx = makeWorkersContext(tmp, [
            "queue": WorkerConfig("php artisan queue:work", 2, "app", ["B": "2", "A": "1"]),
            "scheduler": WorkerConfig(`php "my script.php"`),
        ], warnings);

        string[][] resolvedArgv;
        WorkersRuntimeHooks hooks;
        hooks.buildEnv = (ServiceContext) => ["BASE=1"];
        hooks.resolveTarget = (ServiceContext, string[] argv, string[] env) {
            resolvedArgv ~= argv;
            return WorkerTargetResolution("/opt/php", argv[1 .. $].dup, env);
        };

        auto specs = buildWorkerSpecs(ctx, hooks);
        assert(specs.length == 2 && specs[0].name == "queue" && specs[1].name == "scheduler");
        auto queue = specs[0];
        assert(queue.target == "/opt/php" && queue.args == ["artisan", "queue:work"]);
        assert(queue.replicas == 2 && specs[1].replicas == 1);
        assert(queue.dir == buildPath(ctx.projectDir, "app") && specs[1].dir == ctx.projectDir);
        assert(queue.env == ["BASE=1", "A=1", "B=2"]);
        assert(resolvedArgv[1] == ["php", "my script.php"]);
    }

    // TestWorkersStateMatchesEnvironment
    {
        Environment environment;
        environment.name = "default";
        environment.workers = ["queue": WorkerConfig("php artisan queue:work", 2)];

        WorkersRuntimeState matching;
        matching.environmentName = "default";
        matching.processes = [
            WorkerProcessState("queue", 1, 101, "php artisan queue:work"),
            WorkerProcessState("queue", 2, 102, "php artisan queue:work"),
        ];
        assert(workersStateMatchesEnvironment(matching, environment));

        auto missingReplica = WorkersRuntimeState("default", matching.processes[0 .. 1].dup);
        assert(!workersStateMatchesEnvironment(missingReplica, environment));

        WorkersRuntimeState changedCommand;
        changedCommand.environmentName = "default";
        changedCommand.processes = [
            WorkerProcessState("queue", 1, 101, "php artisan queue:work --old"),
            WorkerProcessState("queue", 2, 102, "php artisan queue:work --old"),
        ];
        assert(!workersStateMatchesEnvironment(changedCommand, environment));

        WorkersRuntimeState extraWorker;
        extraWorker.environmentName = "default";
        extraWorker.processes = matching.processes ~ WorkerProcessState("stale", 1, 103, "php old");
        assert(!workersStateMatchesEnvironment(extraWorker, environment));
    }

    // TestLoadLiveWorkersStatePrunesDeadReplicas
    {
        auto tmp = createTempDir("polka-workers-prune-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto rootDir = buildPath(tmp, ".polka");
        auto path = workersStatePath(rootDir, "default");

        WorkersRuntimeState st;
        st.environmentName = "default";
        st.processes = [
            WorkerProcessState("queue", 1, 101, "php run"),
            WorkerProcessState("queue", 2, 102, "php run"),
        ];
        writeWorkersState(path, st);

        auto live = loadLiveWorkersState(rootDir, "default", (int pid) => pid == 102);
        assert(live !is null && live.processes.length == 1 && live.processes[0].pid == 102);

        auto none = loadLiveWorkersState(rootDir, "default", (int) => false);
        assert(none is null);
        assert(!exists(path));
    }

    // Real process quick-exit test on POSIX: detects immediate exit inside startup grace window.
    version (Posix)
    {
        auto tmp = createTempDir("polka-workers-real-");
        scope (exit) rmdirRecurseQuiet(tmp);
        WorkerSpec spec;
        spec.environmentName = "default";
        spec.name = "failing";
        spec.command = "/bin/sh -c 'exit 1'";
        spec.target = "/bin/sh";
        spec.args = ["-c", "exit 1"];
        spec.dir = tmp;
        spec.replicas = 1;
        spec.logDir = workersLogDir(tmp, "default");

        bool threwExit = false;
        try { startWorkerProcess(spec, 1); }
        catch (Exception e) { threwExit = e.msg.canFind("exited immediately"); }
        assert(threwExit);
    }
}
