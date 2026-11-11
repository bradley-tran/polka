module polka.cli.command_logs;

// Implementation of `polka logs <tool>`: resolves tool log paths filtered by
// optional severity level, prints existing files, or follows log output while
// detecting file rotation (size shrink, removal, or underlying file replacement).

import core.sync.mutex : Mutex;
import core.thread : Thread;
import core.time : Duration, dur;
import std.exception : collectException;
import std.format : format;
import std.stdio : File, SEEK_SET;
import std.string : strip;

import polka.backend : Store, TextWriter, ToolLogEntry, writeOut;
import polka.cli.types : isMissingFileError, writeError, writeErrorf;

version (Posix)
{
    import core.sys.posix.sys.stat : fstat, stat_t;
}
version (Windows)
{
    import core.sys.windows.winbase : BY_HANDLE_FILE_INFORMATION, GetFileInformationByHandle;
    import core.sys.windows.winnt : HANDLE;
    import std.stdio : _get_osfhandle;
}

/// Polling interval when following log output.
enum Duration logFollowPollInterval = dur!"msecs"(100);

/// Optional test seam providing a cooperative cancellation controller when
/// `runLogs` is invoked without an explicit handle (e.g., through `polka.cli.run`).
LogFollowControl delegate() createLogFollowControlFunc;

/// Resolved `logs` command options.
struct LogsCommandInput
{
    string level;
    bool follow = false;
}

/// Thread-safe cooperative cancellation handle for `followLogFiles` / `runLogs`.
final class LogFollowControl
{
    private Mutex mu;
    private bool stopped = false;

    this() @trusted
    {
        mu = new Mutex();
    }

    /// Signals any active `followLogFiles` loop to stop and return cleanly.
    void cancel() @trusted
    {
        synchronized (mu)
        {
            stopped = true;
        }
    }

    /// Reports whether cancellation has been requested.
    bool isCancelled() @trusted
    {
        synchronized (mu)
        {
            return stopped;
        }
    }
}

/// Resolves and streams logs for `tool` (optionally filtered by `level`),
/// either printing once or polling continuously when `follow` is true.
int runLogs(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    string tool,
    string level,
    bool follow,
    LogFollowControl cancel = null
) @trusted
{
    ToolLogEntry[] entries;
    try
    {
        entries = store.resolveToolLogs(tool, level);
    }
    catch (Exception e)
    {
        writeError(stderr, e.msg);
        return 1;
    }

    if (follow)
    {
        if (cancel is null && createLogFollowControlFunc !is null)
            cancel = createLogFollowControlFunc();

        string[] paths;
        paths.reserve(entries.length);
        foreach (entry; entries)
            paths ~= entry.path;

        bool foundAny = false;
        try
        {
            foundAny = followLogFiles(stdout, paths, logFollowPollInterval, cancel);
        }
        catch (Exception e)
        {
            writeError(stderr, e.msg);
            return 1;
        }
        if (!foundAny)
        {
            writeErrorf(stderr, "no log files found for %s%s", tool.strip(), logLevelSuffix(level));
            return 1;
        }
        return 0;
    }

    bool wroteAny = false;
    foreach (entry; entries)
    {
        try
        {
            auto wrote = writeLogFile(stdout, entry.path);
            wroteAny = wroteAny || wrote;
        }
        catch (Exception e)
        {
            writeError(stderr, e.msg);
            return 1;
        }
    }
    if (!wroteAny)
    {
        writeErrorf(stderr, "no log files found for %s%s", tool.strip(), logLevelSuffix(level));
        return 1;
    }

    return 0;
}

/// Captures OS file identity and size for log rotation detection.
private struct LogFileIdentity
{
    bool valid = false;
    ulong dev = 0;
    ulong ino = 0;
    ulong size = 0;
}

private LogFileIdentity inspectOpenLogFile(ref File f) @trusted
{
    LogFileIdentity id;
    version (Posix)
    {
        stat_t st;
        if (fstat(f.fileno, &st) != 0)
            return id;
        id.valid = true;
        id.dev = cast(ulong) st.st_dev;
        id.ino = cast(ulong) st.st_ino;
        id.size = cast(ulong) st.st_size;
        return id;
    }
    else version (Windows)
    {
        auto h = cast(HANDLE) _get_osfhandle(f.fileno);
        BY_HANDLE_FILE_INFORMATION info;
        if (GetFileInformationByHandle(h, &info) == 0)
            return id;
        id.valid = true;
        id.dev = cast(ulong) info.dwVolumeSerialNumber;
        id.ino = (cast(ulong) info.nFileIndexHigh << 32) | cast(ulong) info.nFileIndexLow;
        id.size = (cast(ulong) info.nFileSizeHigh << 32) | cast(ulong) info.nFileSizeLow;
        return id;
    }
    else
    {
        id.valid = true;
        id.size = f.size;
        return id;
    }
}

private bool sameLogIdentity(const LogFileIdentity a, const LogFileIdentity b) @safe pure nothrow @nogc
{
    return a.valid && b.valid && a.dev == b.dev && a.ino == b.ino;
}

/// Tracks the last-read offset and OS file identity for one followed log file.
struct FollowedLog
{
    string path;
    ulong offset = 0;
    LogFileIdentity info;

    /// Copies unread bytes from `path` into `stdout`. Missing files reset
    /// stored identity so recreated rotated files start at byte 0.
    bool writeAvailable(TextWriter stdout) @trusted
    {
        File file;
        try
        {
            file = File(path, "rb");
        }
        catch (Exception e)
        {
            if (isMissingFileError(e))
            {
                offset = 0;
                info = LogFileIdentity.init;
                return false;
            }
            throw new Exception(format("open log %s: %s", path, e.msg));
        }

        auto closeFile = () @trusted {
            collectException(file.close());
        };

        auto currentInfo = inspectOpenLogFile(file);
        if (!currentInfo.valid)
        {
            closeFile();
            throw new Exception(format("stat log %s: failed", path));
        }

        ulong start = offset;
        if (!info.valid || !sameLogIdentity(info, currentInfo) || currentInfo.size < start)
            start = 0;

        try
        {
            file.seek(cast(long) start, SEEK_SET);
        }
        catch (Exception e)
        {
            closeFile();
            throw new Exception(format("seek log %s: %s", path, e.msg));
        }

        ulong written = 0;
        try
        {
            ubyte[8192] buf;
            while (true)
            {
                auto chunk = file.rawRead(buf[]);
                if (chunk.length == 0)
                    break;
                writeOut(stdout, cast(const(char)[]) chunk);
                written += chunk.length;
            }
        }
        catch (Exception e)
        {
            closeFile();
            throw new Exception(format("read log %s: %s", path, e.msg));
        }

        try
        {
            file.close();
        }
        catch (Exception e)
        {
            throw new Exception(format("close log %s: %s", path, e.msg));
        }

        offset = start + written;
        info = currentInfo;
        return true;
    }
}

/// Prints current contents of existing `paths`, then polls for appended output
/// until `cancel` is signalled (or returns `false` immediately if no files exist).
bool followLogFiles(
    TextWriter stdout,
    const(string)[] paths,
    Duration interval,
    LogFollowControl cancel = null
) @trusted
{
    FollowedLog[] logs;
    logs.reserve(paths.length);
    bool foundAny = false;
    foreach (p; paths)
    {
        auto fl = FollowedLog(p);
        auto ex = fl.writeAvailable(stdout);
        foundAny = foundAny || ex;
        logs ~= fl;
    }
    if (!foundAny)
        return false;

    while (cancel is null || !cancel.isCancelled())
    {
        Thread.sleep(interval);
        if (cancel !is null && cancel.isCancelled())
            break;

        foreach (idx; 0 .. logs.length)
            logs[idx].writeAvailable(stdout);
    }

    return true;
}

/// Streams a single log file to `stdout`; returns `false` when the file is absent.
bool writeLogFile(TextWriter stdout, string path) @trusted
{
    File file;
    try
    {
        file = File(path, "rb");
    }
    catch (Exception e)
    {
        if (isMissingFileError(e))
            return false;
        throw new Exception(format("open log %s: %s", path, e.msg));
    }

    Exception readErr;
    try
    {
        ubyte[8192] buf;
        while (true)
        {
            auto chunk = file.rawRead(buf[]);
            if (chunk.length == 0)
                break;
            writeOut(stdout, cast(const(char)[]) chunk);
        }
    }
    catch (Exception e)
    {
        readErr = new Exception(format("read log %s: %s", path, e.msg));
    }

    try
    {
        file.close();
    }
    catch (Exception e)
    {
        if (readErr is null)
            throw new Exception(format("close log %s: %s", path, e.msg));
    }

    if (readErr !is null)
        throw readErr;
    return true;
}

/// Formats optional `" at level <level>"` suffix for missing-log error messages.
string logLevelSuffix(string level) @safe pure
{
    auto trimmed = level.strip();
    if (trimmed.length == 0)
        return "";
    return " at level " ~ trimmed;
}

unittest
{
    import std.algorithm : canFind;
    import std.array : appender;
    import std.file : append, mkdirRecurse, remove, write;
    import std.path : buildPath, dirName;
    import polka.backend : BufferTextWriter, newProjectStore;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    static void writeLog(string path, string content)
    {
        mkdirRecurse(dirName(path));
        write(path, content);
    }

    // Prints all nginx logs and supports `--level` filtering.
    {
        auto projectDir = createTempDir("polka-logs-print-");
        scope (exit) rmdirRecurseQuiet(projectDir);

        auto store = newProjectStore(projectDir);
        store.init();
        store.configureValue("default", "tools.nginx", "1.30");

        writeLog(buildPath(store.rootDir, "run", "nginx", "default", "logs", "access.log"), "access\n");
        writeLog(buildPath(store.rootDir, "run", "nginx", "default", "logs", "error.log"), "error\n");
        writeLog(buildPath(store.rootDir, "run", "nginx", "default", "serve.log"), "debug-serve\n");
        writeLog(buildPath(store.rootDir, "run", "nginx", "default", "php.log"), "debug-php\n");

        auto outAll = new BufferTextWriter();
        auto errAll = new BufferTextWriter();
        assert(runLogs(outAll, errAll, store, "nginx", "", false) == 0, errAll.text());
        assert(outAll.text() == "access\nerror\ndebug-serve\ndebug-php\n");

        auto outErr = new BufferTextWriter();
        auto errErr = new BufferTextWriter();
        assert(runLogs(outErr, errErr, store, "nginx", "error", false) == 0, errErr.text());
        assert(outErr.text() == "error\n");
    }

    // Missing logs and invalid levels return non-zero with expected messages.
    {
        auto projectDir = createTempDir("polka-logs-missing-");
        scope (exit) rmdirRecurseQuiet(projectDir);

        auto store = newProjectStore(projectDir);
        store.init();
        store.configureValue("default", "tools.nginx", "1.30");

        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();
        assert(runLogs(outBuf, errBuf, store, "nginx", "", false) == 1);
        assert(errBuf.text().canFind("no log files found for nginx"));

        auto errInvalid = new BufferTextWriter();
        assert(runLogs(outBuf, errInvalid, store, "nginx", "trace", false) == 1);
        assert(errInvalid.text().canFind("invalid log level"));
    }

    // Rotation detection resets read offset when truncated or replaced.
    {
        auto dir = createTempDir("polka-logs-rotate-");
        scope (exit) rmdirRecurseQuiet(dir);

        auto logFile = buildPath(dir, "app.log");
        writeLog(logFile, "first-longer-line\n");
        auto outBuf = new BufferTextWriter();
        auto fl = FollowedLog(logFile);
        assert(fl.writeAvailable(outBuf));
        append(logFile, "second\n");
        assert(fl.writeAvailable(outBuf));
        assert(outBuf.text() == "first-longer-line\nsecond\n");

        // Truncate/rotate smaller file: must read from offset 0.
        write(logFile, "rot\n");
        assert(fl.writeAvailable(outBuf));
        assert(outBuf.text() == "first-longer-line\nsecond\nrot\n");

        // Unlink and recreate: must read fresh contents from offset 0.
        remove(logFile);
        assert(!fl.writeAvailable(outBuf));
        write(logFile, "reborn\n");
        assert(fl.writeAvailable(outBuf));
        assert(outBuf.text() == "first-longer-line\nsecond\nrot\nreborn\n");
    }

    // Follow mode captures appended writes and exits when cancelled.
    {
        auto dir = createTempDir("polka-logs-follow-");
        scope (exit) rmdirRecurseQuiet(dir);
        auto logFile = buildPath(dir, "follow.log");
        writeLog(logFile, "initial\n");

        auto cancel = new LogFollowControl();
        auto writerMu = new Mutex();
        bool appendedOnce = false;

        final class FollowCaptureWriter : TextWriter
        {
            private typeof(appender!string()) buf;

            this()
            {
                buf = appender!string();
            }

            override void write(const(char)[] chunk) @trusted
            {
                synchronized (writerMu)
                {
                    buf.put(chunk);
                    auto txt = buf.data;
                    if (!appendedOnce && txt.canFind("initial\n"))
                    {
                        appendedOnce = true;
                        auto t = new Thread(() {
                            append(logFile, "appended\n");
                        });
                        t.isDaemon = true;
                        t.start();
                    }
                    if (txt.canFind("appended\n"))
                        cancel.cancel();
                }
            }

            string text() @trusted
            {
                synchronized (writerMu)
                {
                    return buf.data;
                }
            }
        }

        auto cap = new FollowCaptureWriter();
        auto found = followLogFiles(cap, [logFile], dur!"msecs"(20), cancel);
        assert(found);
        assert(cap.text() == "initial\nappended\n");
    }
}
