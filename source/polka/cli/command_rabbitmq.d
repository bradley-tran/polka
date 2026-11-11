module polka.cli.command_rabbitmq;

// CLI service wrappers and test seams for managed RabbitMQ runtime lifecycle.

import std.datetime : Clock, SysTime, UTC;
import std.functional : toDelegate;

import polka.backend : Environment, RabbitMQConfig, Store;
import polka.cli.service_context : managedServiceContext;
import polka.service :
    ensureManagedRabbitMQStartedService = ensureManagedRabbitMQStarted,
    loadLiveRabbitMQStateService = loadLiveRabbitMQState,
    pingRabbitMQAddress,
    RabbitMQRuntimeHooks,
    RabbitMQRuntimeState,
    RabbitMQServerSpec,
    RabbitMQStartResult,
    startRabbitMQServer,
    stopManagedRabbitMQService = stopManagedRabbitMQ,
    stopRabbitMQRuntime;

/// Test seams for RabbitMQ process lifecycle operations.
RabbitMQStartResult delegate(RabbitMQServerSpec) startRabbitMQServerFunc;
void delegate(RabbitMQRuntimeState) stopRabbitMQRuntimeFunc;
bool delegate(string) pingRabbitMQAddressFunc;
SysTime delegate() rabbitMQNowFunc;

alias rabbitMQRuntimeState = RabbitMQRuntimeState;
alias rabbitMQServerSpec = RabbitMQServerSpec;
alias rabbitMQStartResult = RabbitMQStartResult;

/// Starts RabbitMQ for `environment` if configured and not already live.
RabbitMQRuntimeState ensureManagedRabbitMQStarted(Store store, Environment environment, out bool alreadyStarted) @trusted
{
    return ensureManagedRabbitMQStartedService(
        managedServiceContext(store, environment, null),
        rabbitMQRuntimeHooks(),
        alreadyStarted
    );
}

/// Stops the live RabbitMQ broker for `environmentName`.
RabbitMQRuntimeState stopManagedRabbitMQ(Store store, string environmentName, out bool alreadyStopped) @trusted
{
    Environment env;
    env.name = environmentName;
    return stopManagedRabbitMQService(
        managedServiceContext(store, env, null),
        rabbitMQRuntimeHooks(),
        alreadyStopped
    );
}

/// Builds `RabbitMQRuntimeHooks` wired to the CLI test seams.
RabbitMQRuntimeHooks rabbitMQRuntimeHooks() @trusted
{
    RabbitMQRuntimeHooks h;
    h.startServer = startRabbitMQServerFunc !is null ? startRabbitMQServerFunc : toDelegate(&startRabbitMQServer);
    h.stopServer = stopRabbitMQRuntimeFunc !is null ? stopRabbitMQRuntimeFunc : toDelegate(&stopRabbitMQRuntime);
    h.pingAddress = pingRabbitMQAddressFunc !is null ? pingRabbitMQAddressFunc : toDelegate(&pingRabbitMQAddress);
    h.now = rabbitMQNowFunc !is null ? rabbitMQNowFunc : () => Clock.currTime(UTC());
    return h;
}

/// Loads persisted RabbitMQ state if still live according to `pingRabbitMQAddressFunc`.
RabbitMQRuntimeState* loadLiveRabbitMQState(string rootDir, string environmentName) @trusted
{
    auto pingFn = pingRabbitMQAddressFunc !is null ? pingRabbitMQAddressFunc : toDelegate(&pingRabbitMQAddress);
    return loadLiveRabbitMQStateService(rootDir, environmentName, pingFn);
}

unittest
{
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    auto tmp = createTempDir("polka-cli-rmq-");
    scope (exit) rmdirRecurseQuiet(tmp);
    assert(loadLiveRabbitMQState(tmp, "demo") is null);
    auto h = rabbitMQRuntimeHooks();
    assert(h.startServer !is null && h.stopServer !is null && h.pingAddress !is null && h.now !is null);
}
