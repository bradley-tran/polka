module polka.cli.command_mailpit;

// CLI service wrappers and test seams for managed Mailpit runtime lifecycle.

import std.datetime : Clock, SysTime, UTC;
import std.functional : toDelegate;

import polka.backend : Environment, MailpitConfig, Store;
import polka.cli.service_context : managedServiceContext;
import polka.service :
    ensureManagedMailpitStartedService = ensureManagedMailpitStarted,
    loadLiveMailpitStateService = loadLiveMailpitState,
    MailpitRuntimeHooks,
    MailpitRuntimeState,
    MailpitServerSpec,
    MailpitStartResult,
    pingMailpitAddress,
    startMailpitServer,
    stopManagedMailpitService = stopManagedMailpit,
    stopMailpitRuntime;

/// Test seams for Mailpit process lifecycle operations.
MailpitStartResult delegate(MailpitServerSpec) startMailpitServerFunc;
void delegate(MailpitRuntimeState) stopMailpitRuntimeFunc;
bool delegate(string) pingMailpitAddressFunc;
SysTime delegate() mailpitNowFunc;

alias mailpitRuntimeState = MailpitRuntimeState;
alias mailpitServerSpec = MailpitServerSpec;
alias mailpitStartResult = MailpitStartResult;

/// Starts Mailpit for `environment` if configured and not already live.
MailpitRuntimeState ensureManagedMailpitStarted(Store store, Environment environment, out bool alreadyStarted) @trusted
{
    return ensureManagedMailpitStartedService(
        managedServiceContext(store, environment, null),
        mailpitRuntimeHooks(),
        alreadyStarted
    );
}

/// Stops the live Mailpit instance for `environmentName`.
MailpitRuntimeState stopManagedMailpit(Store store, string environmentName, out bool alreadyStopped) @trusted
{
    Environment env;
    env.name = environmentName;
    return stopManagedMailpitService(
        managedServiceContext(store, env, null),
        mailpitRuntimeHooks(),
        alreadyStopped
    );
}

/// Builds `MailpitRuntimeHooks` wired to the CLI test seams (falling back to service defaults).
MailpitRuntimeHooks mailpitRuntimeHooks() @trusted
{
    MailpitRuntimeHooks h;
    h.startServer = startMailpitServerFunc !is null ? startMailpitServerFunc : toDelegate(&startMailpitServer);
    h.stopServer = stopMailpitRuntimeFunc !is null ? stopMailpitRuntimeFunc : toDelegate(&stopMailpitRuntime);
    h.pingAddress = pingMailpitAddressFunc !is null ? pingMailpitAddressFunc : toDelegate(&pingMailpitAddress);
    h.now = mailpitNowFunc !is null ? mailpitNowFunc : () => Clock.currTime(UTC());
    return h;
}

/// Loads persisted Mailpit state if still live according to `pingMailpitAddressFunc`.
MailpitRuntimeState* loadLiveMailpitState(string rootDir, string environmentName) @trusted
{
    auto pingFn = pingMailpitAddressFunc !is null ? pingMailpitAddressFunc : toDelegate(&pingMailpitAddress);
    return loadLiveMailpitStateService(rootDir, environmentName, pingFn);
}

unittest
{
    import std.datetime : DateTime;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    auto tmp = createTempDir("polka-cli-mailpit-");
    scope (exit) rmdirRecurseQuiet(tmp);
    assert(loadLiveMailpitState(tmp, "demo") is null);

    auto h = mailpitRuntimeHooks();
    assert(h.startServer !is null && h.stopServer !is null && h.pingAddress !is null && h.now !is null);
    auto oldNow = mailpitNowFunc;
    scope (exit) mailpitNowFunc = oldNow;
    mailpitNowFunc = () => SysTime(DateTime(2026, 7, 1, 0, 0, 0), UTC());
    assert(mailpitRuntimeHooks().now() == SysTime(DateTime(2026, 7, 1, 0, 0, 0), UTC()));
}
