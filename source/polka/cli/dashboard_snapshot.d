module polka.cli.dashboard_snapshot;

// Polls and renders a point-in-time snapshot of the active environment's
// webserver, managed services, and worker processes for `polka dashboard`.

import core.time : Duration, dur;
import std.array : appender;
import std.datetime : DateTime, SysTime;
import std.format : format;
import std.string : strip;

import polka.backend : Environment, Store;
import polka.cli.command_db : callPingDatabaseAddress;
import polka.cli.command_mailpit : loadLiveMailpitState;
import polka.cli.command_meilisearch : loadLiveMeilisearchState;
import polka.cli.command_phpmyadmin : loadLivePHPMyAdminState;
import polka.cli.command_rabbitmq : loadLiveRabbitMQState;
import polka.cli.command_redis : loadLiveRedisState;
import polka.cli.command_traefik : loadLiveTraefikState;
import polka.cli.command_workers : loadLiveWorkersState, workerProcessSummary;
import polka.cli.serve_runtime : loadWebServerState, serveRuntimeLabel, serveStateURL;
import polka.config : effectiveWorkerReplicas;
import polka.service :
    loadLiveManagedDatabaseState,
    mailpitUIURL,
    meilisearchURL,
    rabbitMQManagementURLForConfig,
    rabbitMQURLForConfig,
    redisURL,
    ServeRuntimeState,
    traefikURL,
    WorkersRuntimeState;

/// Dashboard row status labels matching `polka status`.
enum string dashboardStatusRunning = "running";
enum string dashboardStatusStopped = "stopped";
enum string dashboardStatusUnset = "unset";

/// Interval at which `polka dashboard` refreshes its live snapshot.
enum Duration dashboardPollInterval = dur!"seconds"(2);

/// Display state of one managed service in `DashboardSnapshot`.
struct DashboardServiceRow
{
    string name;
    string status;
    string detail; // URL or address summary; empty when not running
    int pid = 0;
    SysTime startedAt;
}

/// Complete live runtime snapshot polled once per tick by `polka dashboard`.
struct DashboardSnapshot
{
    string environmentName;
    SysTime takenAt;
    bool noEnvironment = false;
    ServeRuntimeState* webServer;
    DashboardServiceRow[] services;
    WorkersRuntimeState* workers;
    int workersConfigured = 0; // total configured worker replicas
    bool workersUnset = false;
    string[] errors;

    /// Records a transient loader/probe failure in the dashboard footer so
    /// one failing service never aborts the live view.
    void recordError(string name, Throwable err) @safe
    {
        errors ~= format("%s: %s", name, err.msg);
    }
}

/// Polls live runtime state for the active environment using the same loaders
/// backing `polka status`, keeping stale state files cleaned up automatically.
DashboardSnapshot gatherDashboardSnapshot(ref Store store, SysTime now) @trusted
{
    DashboardSnapshot snapshot;
    snapshot.takenAt = now;

    Environment* current;
    try
    {
        current = store.current();
    }
    catch (Exception e)
    {
        snapshot.recordError("environment", e);
        return snapshot;
    }
    if (current is null)
    {
        snapshot.noEnvironment = true;
        return snapshot;
    }

    auto env = *current;
    snapshot.environmentName = env.name;

    try
    {
        snapshot.webServer = loadWebServerState(store.rootDir, env.name);
    }
    catch (Exception e)
    {
        snapshot.recordError("webserver", e);
    }

    snapshot.services = [
        gatherDatabaseRow(store, env, snapshot),
        gatherMailpitRow(store, env, snapshot),
        gatherPHPMyAdminRow(store, env, snapshot),
        gatherMeilisearchRow(store, env, snapshot),
        gatherRedisRow(store, env, snapshot),
        gatherRabbitMQRow(store, env, snapshot),
        gatherTraefikRow(store, env, snapshot),
    ];

    if (env.workers.length == 0)
    {
        snapshot.workersUnset = true;
        return snapshot;
    }
    foreach (_, worker; env.workers)
        snapshot.workersConfigured += effectiveWorkerReplicas(worker);

    try
    {
        snapshot.workers = loadLiveWorkersState(store.rootDir, env.name);
    }
    catch (Exception e)
    {
        snapshot.recordError("workers", e);
        return snapshot;
    }

    return snapshot;
}

private DashboardServiceRow gatherDatabaseRow(
    ref Store store,
    const Environment env,
    ref DashboardSnapshot snapshot
) @trusted
{
    DashboardServiceRow row = DashboardServiceRow("database", dashboardStatusUnset);
    if (env.database is null || env.database.engine.strip().length == 0)
        return row;

    row.status = dashboardStatusStopped;
    try
    {
        auto st = loadLiveManagedDatabaseState(store.rootDir, env.name, (string addr) => callPingDatabaseAddress(addr));
        if (st is null)
            return row;
        row.status = dashboardStatusRunning;
        row.detail = format("%s:%s@%d", st.engine, st.version_, st.port);
        row.pid = st.pid;
        row.startedAt = st.startedAt;
    }
    catch (Exception e)
    {
        snapshot.recordError(row.name, e);
    }
    return row;
}

private DashboardServiceRow gatherMailpitRow(
    ref Store store,
    const Environment env,
    ref DashboardSnapshot snapshot
) @trusted
{
    DashboardServiceRow row = DashboardServiceRow("mailpit", dashboardStatusUnset);
    if (env.toolVersion("mailpit").strip().length == 0)
        return row;

    row.status = dashboardStatusStopped;
    try
    {
        auto st = loadLiveMailpitState(store.rootDir, env.name);
        if (st is null)
            return row;
        row.status = dashboardStatusRunning;
        row.detail = format("smtp=%d ui=%s", st.smtpPort, mailpitUIURL(*st));
        row.pid = st.pid;
        row.startedAt = st.startedAt;
    }
    catch (Exception e)
    {
        snapshot.recordError(row.name, e);
    }
    return row;
}

private DashboardServiceRow gatherPHPMyAdminRow(
    ref Store store,
    const Environment env,
    ref DashboardSnapshot snapshot
) @trusted
{
    DashboardServiceRow row = DashboardServiceRow("phpmyadmin", dashboardStatusUnset);
    if (env.toolVersion("phpmyadmin").strip().length == 0)
        return row;

    row.status = dashboardStatusStopped;
    try
    {
        auto st = loadLivePHPMyAdminState(store.rootDir, env.name);
        if (st is null)
            return row;
        row.status = dashboardStatusRunning;
        row.detail = serveStateURL(*st);
        row.pid = st.primaryPID;
        row.startedAt = st.startedAt;
    }
    catch (Exception e)
    {
        snapshot.recordError(row.name, e);
    }
    return row;
}

private DashboardServiceRow gatherMeilisearchRow(
    ref Store store,
    const Environment env,
    ref DashboardSnapshot snapshot
) @trusted
{
    DashboardServiceRow row = DashboardServiceRow("meilisearch", dashboardStatusUnset);
    if (env.toolVersion("meilisearch").strip().length == 0)
        return row;

    row.status = dashboardStatusStopped;
    try
    {
        auto st = loadLiveMeilisearchState(store.rootDir, env.name);
        if (st is null)
            return row;
        row.status = dashboardStatusRunning;
        row.detail = meilisearchURL(*st);
        row.pid = st.pid;
        row.startedAt = st.startedAt;
    }
    catch (Exception e)
    {
        snapshot.recordError(row.name, e);
    }
    return row;
}

private DashboardServiceRow gatherRedisRow(
    ref Store store,
    const Environment env,
    ref DashboardSnapshot snapshot
) @trusted
{
    DashboardServiceRow row = DashboardServiceRow("redis", dashboardStatusUnset);
    if (env.toolVersion("redis").strip().length == 0)
        return row;

    row.status = dashboardStatusStopped;
    try
    {
        auto st = loadLiveRedisState(store.rootDir, env.name);
        if (st is null)
            return row;
        row.status = dashboardStatusRunning;
        row.detail = redisURL(*st);
        row.pid = st.pid;
        row.startedAt = st.startedAt;
    }
    catch (Exception e)
    {
        snapshot.recordError(row.name, e);
    }
    return row;
}

private DashboardServiceRow gatherRabbitMQRow(
    ref Store store,
    const Environment env,
    ref DashboardSnapshot snapshot
) @trusted
{
    DashboardServiceRow row = DashboardServiceRow("rabbitmq", dashboardStatusUnset);
    auto rabbitMQ = env.rabbitMQ();
    if (rabbitMQ is null || rabbitMQ.version_.strip().length == 0)
        return row;

    row.status = dashboardStatusStopped;
    try
    {
        auto st = loadLiveRabbitMQState(store.rootDir, env.name);
        if (st is null)
            return row;
        row.status = dashboardStatusRunning;
        row.detail = format("amqp=%s ui=%s", rabbitMQURLForConfig(rabbitMQ), rabbitMQManagementURLForConfig(rabbitMQ));
        row.pid = st.pid;
        row.startedAt = st.startedAt;
    }
    catch (Exception e)
    {
        snapshot.recordError(row.name, e);
    }
    return row;
}

private DashboardServiceRow gatherTraefikRow(
    ref Store store,
    const Environment env,
    ref DashboardSnapshot snapshot
) @trusted
{
    DashboardServiceRow row = DashboardServiceRow("traefik", dashboardStatusUnset);
    if (env.toolVersion("traefik").strip().length == 0)
        return row;

    row.status = dashboardStatusStopped;
    try
    {
        auto st = loadLiveTraefikState(store.rootDir, env.name);
        if (st is null)
            return row;
        row.status = dashboardStatusRunning;
        row.detail = traefikURL(*st);
        row.pid = st.pid;
        row.startedAt = st.startedAt;
    }
    catch (Exception e)
    {
        snapshot.recordError(row.name, e);
    }
    return row;
}

private string styleBold(string s) @safe pure
{
    return "\033[1m" ~ s ~ "\033[0m";
}

private string styleDim(string s) @safe pure
{
    return "\033[2m" ~ s ~ "\033[0m";
}

private string styleRunning(string s) @safe pure
{
    return "\033[38;5;42m" ~ s ~ "\033[0m";
}

private string styleStopped(string s) @safe pure
{
    return "\033[38;5;214m" ~ s ~ "\033[0m";
}

private string styleError(string s) @safe pure
{
    return "\033[38;5;203m" ~ s ~ "\033[0m";
}

/// Pads status text to fixed column width BEFORE wrapping in ANSI color sequences
/// so column alignment is unaffected by escape codes.
private string dashboardStatusCell(string status) @safe pure
{
    auto padded = format("%-8s", status);
    switch (status)
    {
        case dashboardStatusRunning:
            return styleRunning(padded);
        case dashboardStatusStopped:
            return styleStopped(padded);
        default:
            return styleDim(padded);
    }
}

/// Renders one `DashboardSnapshot` as the full-screen terminal view.
string renderDashboard(const DashboardSnapshot snapshot) @safe
{
    auto b = appender!string();

    if (snapshot.noEnvironment)
    {
        b.put(styleBold("polka dashboard"));
        b.put("\n\nNo active environment selected.\n\n");
        b.put(styleDim("q quit"));
        b.put("\n");
        return b.data;
    }

    auto header = format("polka dashboard — environment %s", snapshot.environmentName);
    b.put(styleBold(header));
    auto dt = cast(DateTime) snapshot.takenAt;
    b.put(styleDim(format("   updated %02d:%02d:%02d", dt.hour, dt.minute, dt.second)));
    b.put("\n\n");

    b.put(styleBold("Webserver"));
    b.put("\n");
    if (snapshot.webServer is null)
    {
        b.put(format("  %-12s %s\n", "", dashboardStatusCell(dashboardStatusStopped)));
    }
    else
    {
        auto st = *snapshot.webServer;
        b.put(format(
            "  %-12s %s %s  pid %d  up %s\n",
            serveRuntimeLabel(st.serverKind),
            dashboardStatusCell(dashboardStatusRunning),
            serveStateURL(st),
            st.primaryPID,
            formatUptime(snapshot.takenAt, st.startedAt)
        ));
    }
    b.put("\n");

    b.put(styleBold("Services"));
    b.put("\n");
    foreach (row; snapshot.services)
    {
        b.put(format("  %-12s %s", row.name, dashboardStatusCell(row.status)));
        if (row.status == dashboardStatusRunning)
        {
            b.put(format(
                " %s  pid %d  up %s",
                row.detail,
                row.pid,
                formatUptime(snapshot.takenAt, row.startedAt)
            ));
        }
        b.put("\n");
    }
    b.put("\n");

    renderDashboardWorkers(b, snapshot);

    foreach (msg; snapshot.errors)
    {
        b.put(styleError(format("error: %s", msg)));
        b.put("\n");
    }
    if (snapshot.errors.length > 0)
        b.put("\n");

    b.put(styleDim(format("refreshes every %ds — q quit", dashboardPollInterval.total!"seconds")));
    b.put("\n");

    return b.data;
}

private void renderDashboardWorkers(ref typeof(appender!string()) b, const DashboardSnapshot snapshot) @safe
{
    if (snapshot.workersUnset)
    {
        b.put(styleBold("Workers"));
        b.put("\n  ");
        b.put(styleDim(dashboardStatusUnset));
        b.put("\n\n");
        return;
    }

    if (snapshot.workers is null || snapshot.workers.processes.length == 0)
    {
        b.put(styleBold(format("Workers (0/%d)", snapshot.workersConfigured)));
        b.put("\n  ");
        b.put(styleStopped(dashboardStatusStopped));
        b.put("\n\n");
        return;
    }

    auto st = *snapshot.workers;
    b.put(styleBold(format(
        "Workers (%d/%d: %s)",
        st.processes.length,
        snapshot.workersConfigured,
        workerProcessSummary(st)
    )));
    b.put("\n");
    foreach (proc; st.processes)
    {
        b.put(format(
            "  %-12s %s  pid %d  up %s\n",
            format("%s #%d", proc.name, proc.replica),
            dashboardStatusCell(dashboardStatusRunning),
            proc.pid,
            formatUptime(snapshot.takenAt, proc.startedAt)
        ));
    }
    b.put("\n");
}

/// Formats elapsed time since `startedAt` (`42s`, `12m`, `2h13m`, `3d2h`, or `-` for zero/future).
string formatUptime(SysTime now, SysTime startedAt) @safe
{
    if (startedAt == SysTime.init || now < startedAt)
        return "-";

    auto elapsed = now - startedAt;
    if (elapsed < dur!"minutes"(1))
        return format("%ds", elapsed.total!"seconds");
    if (elapsed < dur!"hours"(1))
        return format("%dm", elapsed.total!"minutes");
    if (elapsed < dur!"hours"(24))
        return format("%dh%dm", elapsed.total!"hours", elapsed.total!"minutes" % 60);

    auto hours = elapsed.total!"hours";
    return format("%dd%dh", hours / 24, hours % 24);
}

unittest
{
    import std.algorithm : canFind;
    import std.datetime : Clock, DateTime, UTC;
    import std.file : mkdirRecurse, write;
    import std.path : buildPath;
    import std.process : thisProcessID;
    import polka.backend : newProjectStore;
    import polka.cli.command_db : pingDatabaseAddressFunc;
    import polka.cli.command_workers : workerPIDIsLiveFunc;
    import polka.cli.serve_runtime : pingServeAddressFunc, serveStatePath, writeServeState;
    import polka.cli.test_helpers : writeTestActiveEnvironment;
    import polka.service :
        databaseAddress,
        databaseStatePath,
        ManagedDatabaseRuntimeState,
        WorkerProcessState,
        WorkersRuntimeState,
        workersStatePath,
        writeManagedDatabaseState,
        writeWorkersState;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    void writeDashboardTestProject(string projectDir)
    {
        write(buildPath(projectDir, "polka.yaml"), "version: 0.2\nroot: .polka\n");
        write(buildPath(projectDir, "polka.demo.yaml"),
            "tools:\n" ~
            "  php: \"8.4\"\n" ~
            "  mysql: \"8.0\"\n" ~
            "database:\n" ~
            "  engine: mysql\n" ~
            "  port: 3307\n" ~
            "server:\n" ~
            "  hostname: localhost\n" ~
            "  port: 8080\n" ~
            "workers:\n" ~
            "  queue:\n" ~
            "    command: php artisan queue:work\n"
        );
        writeTestActiveEnvironment(buildPath(projectDir, ".polka"), "demo");
    }

    DashboardServiceRow serviceRowByName(DashboardSnapshot snap, string name)
    {
        foreach (row; snap.services)
        {
            if (row.name == name)
                return row;
        }
        assert(false, "missing service row " ~ name);
    }

    // TestGatherDashboardSnapshotReportsStoppedAndUnsetServices
    {
        auto projectDir = createTempDir("polka-dash-stopped-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        writeDashboardTestProject(projectDir);
        auto store = newProjectStore(projectDir);

        auto snap = gatherDashboardSnapshot(store, Clock.currTime(UTC()));
        assert(snap.errors.length == 0);
        assert(snap.environmentName == "demo");
        assert(snap.webServer is null);
        assert(serviceRowByName(snap, "database").status == dashboardStatusStopped);
        foreach (name; ["mailpit", "phpmyadmin", "meilisearch", "redis", "rabbitmq", "traefik"])
            assert(serviceRowByName(snap, name).status == dashboardStatusUnset, name);
        assert(!snap.workersUnset);
        assert(snap.workersConfigured == 1);
        assert(snap.workers is null);
    }

    // TestGatherDashboardSnapshotReportsRunningServices
    {
        auto projectDir = createTempDir("polka-dash-running-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto root = buildPath(projectDir, ".polka");
        writeDashboardTestProject(projectDir);

        auto now = Clock.currTime(UTC());
        ServeRuntimeState serveSt;
        serveSt.environmentName = "demo";
        serveSt.serverKind = "php";
        serveSt.serverScheme = "http";
        serveSt.serverAddress = "localhost:8080";
        serveSt.docroot = buildPath(projectDir, "public");
        serveSt.primaryPID = 1010;
        serveSt.startedAt = now - dur!"minutes"(90);
        writeServeState(serveStatePath(root, "demo"), serveSt);

        ManagedDatabaseRuntimeState dbSt;
        dbSt.environmentName = "demo";
        dbSt.engine = "mysql";
        dbSt.version_ = "8.0";
        dbSt.port = 3307;
        dbSt.pid = cast(int) thisProcessID;
        dbSt.startedAt = now - dur!"hours"(1);
        writeManagedDatabaseState(databaseStatePath(root, "demo"), dbSt);

        WorkersRuntimeState wkSt;
        wkSt.environmentName = "demo";
        wkSt.processes = [
            WorkerProcessState("queue", 1, 5001, "php artisan queue:work", "", now - dur!"minutes"(12)),
        ];
        writeWorkersState(workersStatePath(root, "demo"), wkSt);

        auto oldPingServe = pingServeAddressFunc;
        auto oldPingDB = pingDatabaseAddressFunc;
        auto oldWorkerLive = workerPIDIsLiveFunc;
        scope (exit)
        {
            pingServeAddressFunc = oldPingServe;
            pingDatabaseAddressFunc = oldPingDB;
            workerPIDIsLiveFunc = oldWorkerLive;
        }
        pingServeAddressFunc = (string addr) => addr == "localhost:8080";
        pingDatabaseAddressFunc = (string addr) => addr == databaseAddress(3307);
        workerPIDIsLiveFunc = (int pid) => pid == 5001;

        auto store = newProjectStore(projectDir);
        auto snap = gatherDashboardSnapshot(store, now);
        assert(snap.errors.length == 0);
        assert(snap.webServer !is null && snap.webServer.primaryPID == 1010);

        auto dbRow = serviceRowByName(snap, "database");
        assert(dbRow.status == dashboardStatusRunning);
        assert(dbRow.detail == "mysql:8.0@3307");

        assert(snap.workers !is null && snap.workers.processes.length == 1);
        assert(snap.workersConfigured == 1);
    }

    // TestRenderDashboard
    {
        auto now = SysTime(DateTime(2026, 7, 12, 15, 4, 5), UTC());
        DashboardSnapshot snap;
        snap.environmentName = "demo";
        snap.takenAt = now;
        auto ws = new ServeRuntimeState();
        ws.environmentName = "demo";
        ws.serverKind = "nginx";
        ws.serverScheme = "http";
        ws.serverAddress = "localhost:8080";
        ws.primaryPID = 1010;
        ws.startedAt = now - dur!"minutes"(90);
        snap.webServer = ws;
        snap.services = [
            DashboardServiceRow("database", dashboardStatusRunning, "mysql:8.0@3307", 1201, now - dur!"hours"(1)),
            DashboardServiceRow("mailpit", dashboardStatusStopped),
            DashboardServiceRow("redis", dashboardStatusUnset),
        ];
        auto wks = new WorkersRuntimeState();
        wks.environmentName = "demo";
        wks.processes = [
            WorkerProcessState("queue", 1, 5001, "", "", now - dur!"minutes"(12)),
        ];
        snap.workers = wks;
        snap.workersConfigured = 2;

        auto rendered = renderDashboard(snap);
        foreach (expected; [
            "polka dashboard — environment demo",
            "updated 15:04:05",
            "http://localhost:8080",
            "pid 1010",
            "up 1h30m",
            "mysql:8.0@3307",
            "mailpit",
            "stopped",
            "unset",
            "Workers (1/2: queue)",
            "queue #1",
            "up 12m",
            "q quit",
        ])
        {
            assert(rendered.canFind(expected), "missing " ~ expected ~ " in " ~ rendered);
        }
    }

    // TestRenderDashboardWithoutEnvironment
    {
        DashboardSnapshot snap;
        snap.noEnvironment = true;
        assert(renderDashboard(snap).canFind("No active environment selected."));
    }

    // TestFormatUptime
    {
        auto now = SysTime(DateTime(2026, 7, 12, 12, 0, 0), UTC());
        assert(formatUptime(now, SysTime.init) == "-");
        assert(formatUptime(now, now + dur!"minutes"(1)) == "-");
        assert(formatUptime(now, now - dur!"seconds"(42)) == "42s");
        assert(formatUptime(now, now - dur!"minutes"(12)) == "12m");
        assert(formatUptime(now, now - dur!"hours"(2) - dur!"minutes"(13)) == "2h13m");
        assert(formatUptime(now, now - dur!"days"(3) - dur!"hours"(2)) == "3d2h");
    }
}
