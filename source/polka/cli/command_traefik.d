module polka.cli.command_traefik;

// CLI service wrappers and test seams for managed Traefik reverse-proxy lifecycle.

import std.datetime : Clock, SysTime, UTC;
import std.functional : toDelegate;

import polka.backend : Environment, Store, TraefikConfig;
import polka.cli.service_context : managedServiceContext;
import polka.service :
    ensureManagedTraefikStartedService = ensureManagedTraefikStarted,
    loadLiveTraefikStateService = loadLiveTraefikState,
    pingTraefikAddress,
    startTraefikServer,
    stopManagedTraefikService = stopManagedTraefik,
    stopTraefikRuntime,
    TraefikRuntimeHooks,
    TraefikRuntimeState,
    TraefikServerSpec,
    TraefikStartResult;

/// Test seams for Traefik process lifecycle operations.
TraefikStartResult delegate(TraefikServerSpec) startTraefikServerFunc;
void delegate(TraefikRuntimeState) stopTraefikRuntimeFunc;
bool delegate(string) pingTraefikAddressFunc;
SysTime delegate() traefikNowFunc;

alias traefikRuntimeState = TraefikRuntimeState;
alias traefikServerSpec = TraefikServerSpec;
alias traefikStartResult = TraefikStartResult;

/// Starts Traefik for `environment` if configured and not already live.
TraefikRuntimeState ensureManagedTraefikStarted(Store store, Environment environment, out bool alreadyStarted) @trusted
{
    return ensureManagedTraefikStartedService(
        managedServiceContext(store, environment, null),
        traefikRuntimeHooks(),
        alreadyStarted
    );
}

/// Stops the live Traefik reverse proxy for `environmentName`.
TraefikRuntimeState stopManagedTraefik(Store store, string environmentName, out bool alreadyStopped) @trusted
{
    Environment env;
    env.name = environmentName;
    return stopManagedTraefikService(
        managedServiceContext(store, env, null),
        traefikRuntimeHooks(),
        alreadyStopped
    );
}

/// Builds `TraefikRuntimeHooks` wired to the CLI test seams.
TraefikRuntimeHooks traefikRuntimeHooks() @trusted
{
    TraefikRuntimeHooks h;
    h.startServer = startTraefikServerFunc !is null ? startTraefikServerFunc : toDelegate(&startTraefikServer);
    h.stopServer = stopTraefikRuntimeFunc !is null ? stopTraefikRuntimeFunc : toDelegate(&stopTraefikRuntime);
    h.pingAddress = pingTraefikAddressFunc !is null ? pingTraefikAddressFunc : toDelegate(&pingTraefikAddress);
    h.now = traefikNowFunc !is null ? traefikNowFunc : () => Clock.currTime(UTC());
    return h;
}

/// Loads persisted Traefik state if still live according to `pingTraefikAddressFunc`.
TraefikRuntimeState* loadLiveTraefikState(string rootDir, string environmentName) @trusted
{
    auto pingFn = pingTraefikAddressFunc !is null ? pingTraefikAddressFunc : toDelegate(&pingTraefikAddress);
    return loadLiveTraefikStateService(rootDir, environmentName, pingFn);
}

unittest
{
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    auto tmp = createTempDir("polka-cli-traefik-");
    scope (exit) rmdirRecurseQuiet(tmp);
    assert(loadLiveTraefikState(tmp, "demo") is null);
    auto h = traefikRuntimeHooks();
    assert(h.startServer !is null && h.stopServer !is null && h.pingAddress !is null && h.now !is null);
}
