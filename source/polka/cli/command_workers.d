module polka.cli.command_workers;

// CLI integration for managed background worker processes: builds the
// shell-equivalent environment (`polka sh` / `polka exec`), resolves managed
// tools or shell-PATH binaries to their real executables (avoiding shim
// wrappers that would orphan worker PIDs), and formats worker summaries.

import std.algorithm : sort;
import std.datetime : Clock, SysTime, UTC;
import std.format : format;
import std.path : isAbsolute;
import std.string : icmp, join, strip, toLower;

import polka.backend : Environment, Store;
import polka.cli.service_context : managedServiceContext;
import polka.cli.shell_env :
    buildShellExecutionEnvironment,
    buildShellSessionContext;
import polka.cli.types :
    applyManagedPHPRuntimeConfig,
    dispatchPHARRequiresManagedPHP,
    hasPathSeparator,
    resolveExecTarget;
import polka.service :
    currentEnvList,
    loadServiceWorkersState = loadLiveWorkersState,
    ServiceContext,
    servicePIDIsLive,
    startWorkerProcess,
    stopServiceManagedWorkers = stopManagedWorkers,
    stopWorkerProcess,
    WorkerProcessState,
    WorkersRuntimeHooks,
    WorkersRuntimeState,
    WorkerSpec,
    WorkerStartResult,
    WorkerTargetResolution;
import polka.support.fs : currentOS;

/// Test seams mirroring other CLI service wrappers; when non-null, tests use
/// these to stub worker spawning, stopping, liveness checks, and time.
WorkerStartResult delegate(WorkerSpec, int) startWorkerProcessFunc;
void delegate(WorkerProcessState) stopWorkerProcessFunc;
bool delegate(int) workerPIDIsLiveFunc;
SysTime delegate() workersNowFunc;

private WorkerStartResult delegate(WorkerSpec, int) resolvedStartWorkerFunc() @trusted
{
    if (startWorkerProcessFunc !is null)
        return startWorkerProcessFunc;
    return (WorkerSpec spec, int replica) => startWorkerProcess(spec, replica);
}

private void delegate(WorkerProcessState) resolvedStopWorkerFunc() @trusted
{
    if (stopWorkerProcessFunc !is null)
        return stopWorkerProcessFunc;
    return (WorkerProcessState st) { stopWorkerProcess(st); };
}

private bool delegate(int) resolvedWorkerPIDIsLiveFunc() @trusted
{
    if (workerPIDIsLiveFunc !is null)
        return workerPIDIsLiveFunc;
    return (int pid) => servicePIDIsLive(pid);
}

private SysTime delegate() resolvedWorkersNowFunc() @trusted
{
    if (workersNowFunc !is null)
        return workersNowFunc;
    return () => Clock.currTime(UTC());
}

/// Wires CLI-specific shell environment and target resolution into
/// `WorkersRuntimeHooks` for `polka.service`.
WorkersRuntimeHooks workersRuntimeHooks(Store store, Environment environment) @trusted
{
    auto storeCopy = store;
    auto envName = environment.name;
    WorkersRuntimeHooks hooks;
    hooks.startWorker = resolvedStartWorkerFunc();
    hooks.stopWorker = resolvedStopWorkerFunc();
    hooks.pidIsLive = resolvedWorkerPIDIsLiveFunc();
    hooks.buildEnv = (ServiceContext) => buildWorkerBaseEnvironment(storeCopy, envName);
    hooks.resolveTarget = (ServiceContext, string[] argv, string[] env) => resolveWorkerTarget(storeCopy, argv, env);
    hooks.now = resolvedWorkersNowFunc();
    return hooks;
}

/// Stops all live worker processes recorded for `environment` and deletes its state file.
WorkersRuntimeState stopManagedWorkers(ref Store store, Environment environment, out bool alreadyStopped) @trusted
{
    return stopServiceManagedWorkers(
        managedServiceContext(store, environment, null),
        workersRuntimeHooks(store, environment),
        alreadyStopped
    );
}

/// Loads persisted live worker state for `environmentName`, returning `null` if stale or absent.
WorkersRuntimeState* loadLiveWorkersState(string rootDir, string environmentName) @trusted
{
    return loadServiceWorkersState(rootDir, environmentName, resolvedWorkerPIDIsLiveFunc());
}

/// Builds the execution environment matching `polka sh` and `polka exec`.
string[] buildWorkerBaseEnvironment(ref Store store, string environmentName) @trusted
{
    auto ctx = buildShellSessionContext(currentOS(), store, store.projectDir, environmentName);
    return buildShellExecutionEnvironment(currentOS(), currentEnvList(), store, ctx);
}

/// Maps a worker command's `argv[0]` to a concrete executable path (resolving
/// managed tools to the underlying binary rather than bin-dir shims so recorded
/// PIDs track the real worker process) and applies PHP runtime config when needed.
WorkerTargetResolution resolveWorkerTarget(
    ref Store store,
    const(string)[] argv,
    const(string)[] env
) @trusted
{
    if (argv.length == 0)
        throw new Exception("worker command cannot be empty");

    auto command = argv[0].strip();
    auto args = argv[1 .. $].dup;
    if (isAbsolute(command) || hasPathSeparator(command))
        return WorkerTargetResolution(command, args, env.dup);

    string target;
    try
    {
        target = store.resolveTool(command);
    }
    catch (Exception)
    {
        // Not a managed tool: fall back to the shell PATH (vendor binaries, system commands).
        auto fallback = resolveExecTarget(currentOS(), command, env);
        return WorkerTargetResolution(fallback, args, env.dup);
    }

    auto updatedEnv = env.dup;
    bool usesManagedPHP = icmp(command, "php") == 0 || icmp(command, "frankenphp") == 0;
    if (dispatchPHARRequiresManagedPHP(command, target))
    {
        string phpTarget;
        try
        {
            phpTarget = store.resolveTool("php");
        }
        catch (Exception e)
        {
            throw new Exception(format("resolve php for %s: %s", command.toLower(), e.msg));
        }

        string[] prepended;
        prepended.reserve(args.length + 1);
        prepended ~= target;
        prepended ~= args;
        args = prepended;
        target = phpTarget;
        usesManagedPHP = true;
    }

    if (usesManagedPHP)
        updatedEnv = applyManagedPHPRuntimeConfig(currentOS(), updatedEnv, target);

    return WorkerTargetResolution(target, args, updatedEnv);
}

/// Renders live worker replicas as `"queue x2, scheduler"` with names sorted for stable output.
string workerProcessSummary(const WorkersRuntimeState state) @safe
{
    int[string] counts;
    foreach (proc; state.processes)
        counts[proc.name] = counts.get(proc.name, 0) + 1;

    auto names = counts.keys;
    names.sort();

    string[] parts;
    parts.reserve(names.length);
    foreach (name; names)
    {
        auto count = counts[name];
        if (count > 1)
            parts ~= format("%s x%d", name, count);
        else
            parts ~= name;
    }

    return parts.join(", ");
}

unittest
{
    import std.file : mkdirRecurse, write;
    import std.path : buildPath;
    import polka.backend : newProjectStore;
    import polka.support.fs : createTempDir, octal, rmdirRecurseQuiet, setFileMode;

    // TestWorkerProcessSummary checks the human-readable replica summary.
    WorkersRuntimeState state;
    state.processes = [
        WorkerProcessState("scheduler", 1),
        WorkerProcessState("queue", 1),
        WorkerProcessState("queue", 2),
    ];
    assert(workerProcessSummary(state) == "queue x2, scheduler");

    // TestResolveWorkerTarget checks path passthrough and the shell-PATH fallback.
    auto projectDir = createTempDir("polka-workers-cli-");
    scope (exit) rmdirRecurseQuiet(projectDir);
    auto store = newProjectStore(projectDir);

    auto absPath = buildPath(projectDir, "bin", "worker");
    auto resAbs = resolveWorkerTarget(store, [absPath, "--flag"], ["PATH="]);
    assert(resAbs.target == absPath);
    assert(resAbs.args == ["--flag"]);

    auto binDir = buildPath(projectDir, "tools");
    mkdirRecurse(binDir);
    auto executable = buildPath(binDir, currentOS() == "windows" ? "mytool.bat" : "mytool");
    write(executable, "#!/bin/sh\n");
    setFileMode(executable, octal!755);

    auto resPath = resolveWorkerTarget(store, ["mytool", "run"], ["PATH=" ~ binDir]);
    assert(resPath.target == executable);
    assert(resPath.args == ["run"]);
}
