module polka.cli.command_meilisearch;

// CLI service wrappers and test seams for managed Meilisearch runtime lifecycle.

import std.datetime : Clock, SysTime, UTC;
import std.functional : toDelegate;

import polka.backend : Environment, MeilisearchConfig, Store;
import polka.cli.service_context : managedServiceContext;
import polka.service :
    ensureManagedMeilisearchStartedService = ensureManagedMeilisearchStarted,
    loadLiveMeilisearchStateService = loadLiveMeilisearchState,
    MeilisearchRuntimeHooks,
    MeilisearchRuntimeState,
    MeilisearchServerSpec,
    MeilisearchStartResult,
    pingMeilisearchAddress,
    startMeilisearchServer,
    stopManagedMeilisearchService = stopManagedMeilisearch,
    stopMeilisearchRuntime;

/// Test seams for Meilisearch process lifecycle operations.
MeilisearchStartResult delegate(MeilisearchServerSpec) startMeilisearchServerFunc;
void delegate(MeilisearchRuntimeState) stopMeilisearchRuntimeFunc;
bool delegate(string) pingMeilisearchAddressFunc;
SysTime delegate() meilisearchNowFunc;

alias meilisearchRuntimeState = MeilisearchRuntimeState;
alias meilisearchServerSpec = MeilisearchServerSpec;
alias meilisearchStartResult = MeilisearchStartResult;

/// Starts Meilisearch for `environment` if configured and not already live.
MeilisearchRuntimeState ensureManagedMeilisearchStarted(Store store, Environment environment, out bool alreadyStarted) @trusted
{
    return ensureManagedMeilisearchStartedService(
        managedServiceContext(store, environment, null),
        meilisearchRuntimeHooks(),
        alreadyStarted
    );
}

/// Stops the live Meilisearch instance for `environmentName`.
MeilisearchRuntimeState stopManagedMeilisearch(Store store, string environmentName, out bool alreadyStopped) @trusted
{
    Environment env;
    env.name = environmentName;
    return stopManagedMeilisearchService(
        managedServiceContext(store, env, null),
        meilisearchRuntimeHooks(),
        alreadyStopped
    );
}

/// Builds `MeilisearchRuntimeHooks` wired to the CLI test seams.
MeilisearchRuntimeHooks meilisearchRuntimeHooks() @trusted
{
    MeilisearchRuntimeHooks h;
    h.startServer = startMeilisearchServerFunc !is null ? startMeilisearchServerFunc : toDelegate(&startMeilisearchServer);
    h.stopServer = stopMeilisearchRuntimeFunc !is null ? stopMeilisearchRuntimeFunc : toDelegate(&stopMeilisearchRuntime);
    h.pingAddress = pingMeilisearchAddressFunc !is null ? pingMeilisearchAddressFunc : toDelegate(&pingMeilisearchAddress);
    h.now = meilisearchNowFunc !is null ? meilisearchNowFunc : () => Clock.currTime(UTC());
    return h;
}

/// Loads persisted Meilisearch state if still live according to `pingMeilisearchAddressFunc`.
MeilisearchRuntimeState* loadLiveMeilisearchState(string rootDir, string environmentName) @trusted
{
    auto pingFn = pingMeilisearchAddressFunc !is null ? pingMeilisearchAddressFunc : toDelegate(&pingMeilisearchAddress);
    return loadLiveMeilisearchStateService(rootDir, environmentName, pingFn);
}

unittest
{
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    auto tmp = createTempDir("polka-cli-meili-");
    scope (exit) rmdirRecurseQuiet(tmp);
    assert(loadLiveMeilisearchState(tmp, "demo") is null);
    auto h = meilisearchRuntimeHooks();
    assert(h.startServer !is null && h.stopServer !is null && h.pingAddress !is null && h.now !is null);
}
