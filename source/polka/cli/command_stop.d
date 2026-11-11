module polka.cli.command_stop;

// Implementation of `polka stop`: stops the active environment's running
// webserver and all managed services in reverse dependency order.

import polka.backend : Environment, Store, TextWriter, writeOutln;
import polka.cli.hooks : defaultCLIHookRegistry, StopHookContext;
import polka.cli.types : writeError;

/// Stops all running managed processes for the active project environment.
int runStop(TextWriter stdout, TextWriter stderr, ref Store store) @trusted
{
    Environment* current;
    try
    {
        current = store.current();
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }
    if (current is null)
    {
        writeOutln(stderr, "error: no active environment selected");
        return 1;
    }

    try
    {
        defaultCLIHookRegistry().stop(StopHookContext(stdout, stderr, store, *current));
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }

    return 0;
}

unittest
{
    import std.algorithm : canFind;
    import std.file : exists;
    import std.path : buildPath;
    import std.process : thisProcessID;
    import polka.backend : BufferTextWriter, newProjectStore;
    import polka.cli.command_db : pingDatabaseAddressFunc, stopDatabaseServerFunc;
    import polka.cli.serve_runtime :
        pingServeAddressFunc,
        serveStatePath,
        stopServeRuntimeFunc,
        writeServeState;
    import polka.service :
        databaseAddress,
        databaseStatePath,
        ManagedDatabaseRuntimeState,
        ServeRuntimeState,
        writeManagedDatabaseState;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    auto projectDir = createTempDir("polka-stop-cmd-");
    scope (exit) rmdirRecurseQuiet(projectDir);

    auto store = newProjectStore(projectDir);
    store.init();
    store.configureValue("default", "tools.php", "8.4");
    store.configureValue("default", "tools.mysql", "8.4");
    store.configureValue("default", "database.engine", "mysql");
    store.configureValue("default", "database.port", "3307");

    ServeRuntimeState serveSt;
    serveSt.environmentName = "default";
    serveSt.serverKind = "php";
    serveSt.serverAddress = "localhost:8080";
    serveSt.docroot = buildPath(projectDir, "public");
    serveSt.primaryPID = 4242;
    writeServeState(serveStatePath(store.rootDir, "default"), serveSt);

    ManagedDatabaseRuntimeState dbSt;
    dbSt.environmentName = "default";
    dbSt.engine = "mysql";
    dbSt.version_ = "8.4";
    dbSt.port = 3307;
    dbSt.pid = cast(int) thisProcessID;
    dbSt.adminTarget = "mysqladmin";
    dbSt.defaultsFile = "defaults.cnf";
    writeManagedDatabaseState(databaseStatePath(store.rootDir, "default"), dbSt);

    auto oldStopServe = stopServeRuntimeFunc;
    auto oldPingServe = pingServeAddressFunc;
    auto oldStopDB = stopDatabaseServerFunc;
    auto oldPingDB = pingDatabaseAddressFunc;
    scope (exit)
    {
        stopServeRuntimeFunc = oldStopServe;
        pingServeAddressFunc = oldPingServe;
        stopDatabaseServerFunc = oldStopDB;
        pingDatabaseAddressFunc = oldPingDB;
    }

    bool serveAlive = true;
    bool dbAlive = true;
    stopServeRuntimeFunc = (ServeRuntimeState) { serveAlive = false; };
    pingServeAddressFunc = (string addr) => addr == "localhost:8080" && serveAlive;
    stopDatabaseServerFunc = (ManagedDatabaseRuntimeState) { dbAlive = false; };
    pingDatabaseAddressFunc = (string addr) => addr == databaseAddress(3307) && dbAlive;

    auto outBuf = new BufferTextWriter();
    auto errBuf = new BufferTextWriter();
    assert(runStop(outBuf, errBuf, store) == 0, errBuf.text());
    assert(outBuf.text().canFind(`Stopped php webserver for environment "default".`));
    assert(outBuf.text().canFind(`Stopped mysql for environment "default".`));
    assert(!exists(serveStatePath(store.rootDir, "default")));
    assert(!exists(databaseStatePath(store.rootDir, "default")));
}
