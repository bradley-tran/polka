module polka.cli.command_redis;

// CLI service wrappers and test seams for managed Redis runtime lifecycle.

import std.datetime : Clock, SysTime, UTC;
import std.functional : toDelegate;

import polka.backend : Environment, RedisConfig, Store;
import polka.cli.service_context : managedServiceContext;
import polka.service :
    ensureManagedRedisStartedService = ensureManagedRedisStarted,
    loadLiveRedisStateService = loadLiveRedisState,
    pingRedisAddress,
    RedisRuntimeHooks,
    RedisRuntimeState,
    RedisServerSpec,
    RedisStartResult,
    startRedisServer,
    stopManagedRedisService = stopManagedRedis,
    stopRedisRuntime;

/// Test seams for Redis process lifecycle operations.
RedisStartResult delegate(RedisServerSpec) startRedisServerFunc;
void delegate(RedisRuntimeState) stopRedisRuntimeFunc;
bool delegate(string) pingRedisAddressFunc;
SysTime delegate() redisNowFunc;

alias redisRuntimeState = RedisRuntimeState;
alias redisServerSpec = RedisServerSpec;
alias redisStartResult = RedisStartResult;

/// Starts Redis for `environment` if configured and not already live.
RedisRuntimeState ensureManagedRedisStarted(Store store, Environment environment, out bool alreadyStarted) @trusted
{
    return ensureManagedRedisStartedService(
        managedServiceContext(store, environment, null),
        redisRuntimeHooks(),
        alreadyStarted
    );
}

/// Stops the live Redis instance for `environmentName`.
RedisRuntimeState stopManagedRedis(Store store, string environmentName, out bool alreadyStopped) @trusted
{
    Environment env;
    env.name = environmentName;
    return stopManagedRedisService(
        managedServiceContext(store, env, null),
        redisRuntimeHooks(),
        alreadyStopped
    );
}

/// Builds `RedisRuntimeHooks` wired to the CLI test seams.
RedisRuntimeHooks redisRuntimeHooks() @trusted
{
    RedisRuntimeHooks h;
    h.startServer = startRedisServerFunc !is null ? startRedisServerFunc : toDelegate(&startRedisServer);
    h.stopServer = stopRedisRuntimeFunc !is null ? stopRedisRuntimeFunc : toDelegate(&stopRedisRuntime);
    h.pingAddress = pingRedisAddressFunc !is null ? pingRedisAddressFunc : toDelegate(&pingRedisAddress);
    h.now = redisNowFunc !is null ? redisNowFunc : () => Clock.currTime(UTC());
    return h;
}

/// Loads persisted Redis state if still live according to `pingRedisAddressFunc`.
RedisRuntimeState* loadLiveRedisState(string rootDir, string environmentName) @trusted
{
    auto pingFn = pingRedisAddressFunc !is null ? pingRedisAddressFunc : toDelegate(&pingRedisAddress);
    return loadLiveRedisStateService(rootDir, environmentName, pingFn);
}

unittest
{
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    auto tmp = createTempDir("polka-cli-redis-");
    scope (exit) rmdirRecurseQuiet(tmp);
    assert(loadLiveRedisState(tmp, "demo") is null);
    auto h = redisRuntimeHooks();
    assert(h.startServer !is null && h.stopServer !is null && h.pingAddress !is null && h.now !is null);
}
