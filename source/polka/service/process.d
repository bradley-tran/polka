module polka.service.process;

import core.stdc.errno : errno;
import core.thread : Thread;
import core.time : dur;
import std.algorithm : canFind, startsWith;
import std.conv : to;
import std.datetime : Clock, DateTime, SysTime, UTC;
import std.exception : collectException;
import std.file : exists, mkdirRecurse, readText;
import std.format : format;
import std.json : JSONType, JSONValue, parseJSON;
import std.path : baseName, dirName, extension;
import std.process : Config, environment, execute, Pid, ProcessException, spawnProcess, wait;
import std.stdio : File;
import std.string : indexOf, lastIndexOf, split, strip, toLower;

import polka.support.fs : currentOS, octal, writeTextAtomic;

version (Posix)
{
    import core.stdc.errno : ECHILD, EINTR, EPERM, ESRCH;
    import core.sys.posix.signal : kill, SIGKILL;
    import core.sys.posix.sys.wait : waitpid, WNOHANG;
}

/// Validates `target` and constructs the command argument vector, wrapping
/// `.cmd` / `.bat` scripts with `cmd.exe /c` on Windows.
string[] prepareServiceCommandArgs(string target, const(string)[] args) @safe pure
{
    auto trimmed = target.strip();
    if (trimmed.length == 0)
        throw new Exception("dispatch target cannot be empty");

    string[] cmdArgs;
    version (Windows)
    {
        auto ext = extension(trimmed).toLower();
        if (ext == ".cmd" || ext == ".bat")
        {
            cmdArgs = ["cmd.exe", "/c", trimmed] ~ args.dup;
            return cmdArgs;
        }
    }

    cmdArgs.reserve(1 + args.length);
    cmdArgs ~= trimmed;
    foreach (a; args)
        cmdArgs ~= a;
    return cmdArgs;
}

/// Opens (creating parent directories as needed) a service log file for either
/// truncated or append output.
File openServiceLog(string path, bool append = false) @trusted
{
    auto dir = dirName(path);
    if (dir.length > 0 && dir != "." && !exists(dir))
    {
        try
        {
            mkdirRecurse(dir);
        }
        catch (Exception e)
        {
            throw new Exception("create service log directory: " ~ e.msg);
        }
    }

    try
    {
        return File(path, append ? "ab" : "wb");
    }
    catch (Exception e)
    {
        throw new Exception("open service log " ~ path ~ ": " ~ e.msg);
    }
}

/// Returns the current process environment as `"KEY=VALUE"` entries.
string[] currentEnvList() @trusted
{
    auto aa = environment.toAA();
    string[] result;
    result.reserve(aa.length);
    foreach (k, v; aa)
        result ~= k ~ "=" ~ v;
    return result;
}

/// Converts a `"KEY=VALUE"` environment slice into a D associative array, preserving
/// last-entry-wins duplicate behavior (case-insensitive on Windows).
string[string] envListToAA(const(string)[] envList, out bool hasCustomEnv) @safe pure
{
    if (envList.length == 0)
    {
        hasCustomEnv = false;
        return null;
    }

    hasCustomEnv = true;
    string[string] aa;
    foreach (entry; envList)
    {
        auto eq = entry.indexOf('=');
        if (eq <= 0)
            continue;
        auto key = entry[0 .. eq];
        auto val = entry[eq + 1 .. $];

        version (Windows)
        {
            string[] stale;
            foreach (existingKey, _; aa)
            {
                if (existingKey != key && existingKey.toLower() == key.toLower())
                    stale ~= existingKey;
            }
            foreach (sk; stale)
                aa.remove(sk);
        }

        aa[key] = val;
    }
    return aa;
}

/// Returns the value of `key` (case-insensitive) from an `"KEY=VALUE"` environment slice.
string environmentSliceValue(const(string)[] env, string key) @safe pure
{
    auto target = key.toLower();
    string found = "";
    foreach (item; env)
    {
        auto eq = item.indexOf('=');
        if (eq <= 0)
            continue;
        if (item[0 .. eq].toLower() == target)
            found = item[eq + 1 .. $];
    }
    return found;
}

/// Returns an updated `"KEY=VALUE"` environment slice where `key` is set to `value`.
string[] setEnvironmentSliceValue(const(string)[] env, string key, string value) @safe pure
{
    auto target = key.toLower();
    string[] copy;
    copy.reserve(env.length + 1);
    bool updated = false;
    foreach (item; env)
    {
        auto eq = item.indexOf('=');
        if (eq > 0 && item[0 .. eq].toLower() == target)
        {
            if (!updated)
            {
                copy ~= key ~ "=" ~ value;
                updated = true;
            }
            continue;
        }
        copy ~= item;
    }
    if (!updated)
        copy ~= key ~ "=" ~ value;
    return copy;
}

/// Opens the platform null device for detaching child standard input.
private File openNullInput() @trusted
{
    version (Windows)
        return File("NUL", "rb");
    else
        return File("/dev/null", "rb");
}

/// Spawns a detached background service process with stdout and stderr sent to
/// `logFile` and stdin connected to the null device. Returns its OS PID.
int spawnDetachedServiceProcess(
    string target,
    const(string)[] args,
    File logFile,
    const(string)[] envList = null,
    string workDir = null
) @trusted
{
    auto cmdArgs = prepareServiceCommandArgs(target, args);
    bool hasCustomEnv = false;
    auto envAA = envListToAA(envList, hasCustomEnv);

    auto cfg = Config.detached | Config.retainStdout | Config.retainStderr;
    if (hasCustomEnv)
        cfg |= Config.newEnv;

    auto nullIn = openNullInput();
    scope (exit)
    {
        if (nullIn.isOpen)
            nullIn.close();
    }

    try
    {
        auto pid = spawnProcess(
            cmdArgs,
            nullIn,
            logFile,
            logFile,
            envAA,
            cfg,
            workDir.length > 0 ? workDir : null
        );
        return pid.processID;
    }
    catch (ProcessException e)
    {
        throw new Exception(e.msg);
    }
}

/// Runs a service helper command synchronously and waits for completion.
/// When `logFile` is non-null, stdout and stderr write directly to it and
/// `combinedOutput` remains empty; otherwise combined output is captured and returned.
int runServiceCommandSync(
    string target,
    const(string)[] args,
    out string combinedOutput,
    string stdinText = null,
    File* logFile = null,
    const(string)[] envList = null,
    string workDir = null
) @trusted
{
    auto cmdArgs = prepareServiceCommandArgs(target, args);
    bool hasCustomEnv = false;
    auto envAA = envListToAA(envList, hasCustomEnv);

    Config cfg = Config.none;
    if (hasCustomEnv)
        cfg |= Config.newEnv;

    // Fast path using std.process.execute when neither custom stdin nor a log File is needed.
    if (stdinText.length == 0 && logFile is null)
    {
        auto res = execute(cmdArgs, envAA, cfg, size_t.max, workDir.length > 0 ? workDir : null);
        combinedOutput = res.output;
        return res.status;
    }

    // Create a pipe if we have stdin text, or open /dev/null if empty.
    import std.process : pipe;
    File stdinStream;
    bool closeStdinAfter = true;
    File readPipeEnd;
    File writePipeEnd;
    bool hasStdinPipe = false;

    if (stdinText.length > 0)
    {
        auto p = pipe();
        readPipeEnd = p.readEnd;
        writePipeEnd = p.writeEnd;
        stdinStream = readPipeEnd;
        hasStdinPipe = true;
        closeStdinAfter = false; // spawnProcess closes child read end for us
    }
    else
    {
        stdinStream = openNullInput();
        closeStdinAfter = false;
    }

    File stdoutStream;
    File stderrStream;
    File outReadEnd;
    bool hasOutPipe = false;
    if (logFile !is null)
    {
        stdoutStream = *logFile;
        stderrStream = *logFile;
        cfg |= Config.retainStdout | Config.retainStderr;
    }
    else
    {
        auto outP = pipe();
        outReadEnd = outP.readEnd;
        stdoutStream = outP.writeEnd;
        stderrStream = outP.writeEnd;
        hasOutPipe = true;
    }

    Pid pid;
    try
    {
        pid = spawnProcess(
            cmdArgs,
            stdinStream,
            stdoutStream,
            stderrStream,
            envAA,
            cfg,
            workDir.length > 0 ? workDir : null
        );
    }
    catch (ProcessException e)
    {
        if (hasStdinPipe)
        {
            try { writePipeEnd.close(); } catch (Exception) {}
        }
        if (hasOutPipe)
        {
            try { outReadEnd.close(); } catch (Exception) {}
        }
        throw new Exception(e.msg);
    }

    if (hasStdinPipe)
    {
        // Write small SQL/password payload in a helper thread if needed or directly when < pipe buffer;
        // using a writer thread prevents deadlock if both stdin and stdout are large.
        auto payload = stdinText;
        auto writerThread = new Thread({
            try
            {
                if (payload.length > 0)
                    writePipeEnd.rawWrite(cast(const(ubyte)[]) payload);
            }
            catch (Exception) {}
            finally
            {
                collectException(writePipeEnd.close());
            }
        });
        writerThread.start();
        scope (exit) collectException(writerThread.join());

        if (hasOutPipe)
        {
            import std.array : appender;
            auto buf = appender!string();
            foreach (chunk; outReadEnd.byChunk(4096))
                buf.put(cast(const(char)[]) chunk);
            combinedOutput = buf.data;
        }
        return wait(pid);
    }

    if (hasOutPipe)
    {
        import std.array : appender;
        auto buf = appender!string();
        foreach (chunk; outReadEnd.byChunk(4096))
            buf.put(cast(const(char)[]) chunk);
        combinedOutput = buf.data;
    }
    return wait(pid);
}

/// Reports whether a process with `pid` appears to be alive on the host OS.
/// Reaps direct exited children and treats Linux zombie states (`Z`/`X`) as dead.
bool servicePIDIsLive(int pid) @trusted
{
    if (pid <= 0)
        return false;

    version (Windows)
    {
        auto pidText = to!string(pid);
        try
        {
            auto res = execute(["tasklist", "/FI", "PID eq " ~ pidText, "/FO", "CSV", "/NH"]);
            if (res.status != 0)
                return false;
            return res.output.canFind(`"` ~ pidText ~ `"`);
        }
        catch (Exception)
        {
            return false;
        }
    }
    else version (Posix)
    {
        int status = 0;
        auto waited = waitpid(pid, &status, WNOHANG);
        if (waited == pid)
            return false; // child just terminated and was reaped
        if (waited == 0)
            return true;  // direct child is still running

        // Either not our direct child (ECHILD) or interrupted; probe signal 0.
        if (kill(pid, 0) != 0)
        {
            auto err = errno;
            if (err != EPERM)
                return false;
        }

        version (linux)
        {
            if (isLinuxZombiePID(pid))
                return false;
        }
        return true;
    }
    else
    {
        return false;
    }
}

version (linux)
private bool isLinuxZombiePID(int pid) @trusted
{
    auto statPath = format("/proc/%d/stat", pid);
    if (!exists(statPath))
        return true;
    try
    {
        auto content = readText(statPath);
        auto rparen = content.lastIndexOf(')');
        if (rparen < 0 || rparen + 1 >= content.length)
            return false;
        auto rest = content[rparen + 1 .. $].strip();
        if (rest.length == 0)
            return false;
        return rest[0] == 'Z' || rest[0] == 'X' || rest[0] == 'x';
    }
    catch (Exception)
    {
        return false;
    }
}

/// Forcefully stops a process identified by `pid`, ignoring processes that already exited.
void stopServicePID(int pid) @trusted
{
    if (pid <= 0)
        return;

    version (Windows)
    {
        auto pidText = to!string(pid);
        try
        {
            auto res = execute(["taskkill", "/PID", pidText, "/T", "/F"]);
            if (res.status == 0 || isMissingServiceProcessOutput(res.output))
                return;
            throw new Exception(format("stop service process %d: exit status %d (%s)", pid, res.status, res.output.strip()));
        }
        catch (ProcessException e)
        {
            throw new Exception(format("stop service process %d: %s", pid, e.msg));
        }
    }
    else version (Posix)
    {
        if (kill(pid, SIGKILL) != 0)
        {
            auto err = errno;
            if (err == ESRCH)
                return;
            throw new Exception(format("stop service process %d: errno %d", pid, err));
        }
        int status = 0;
        waitpid(pid, &status, WNOHANG);
    }
}

/// Reports whether command/OS output indicates a missing/already-finished process.
bool isMissingServiceProcessOutput(string output) @safe pure
{
    auto trimmed = output.strip().toLower();
    return trimmed.length == 0 ||
        trimmed.canFind("not found") ||
        trimmed.canFind("no running instance") ||
        trimmed.canFind("process already finished") ||
        trimmed.canFind("no such process");
}

// ============================================================================
// Shared JSON & Timestamp Persistence Helpers
// ============================================================================

/// Formats a `SysTime` as an ISO-8601 UTC string (e.g. `2026-06-30T12:00:00Z`).
string formatServiceTimestamp(SysTime ts) @trusted
{
    if (ts == SysTime.init)
        return "";
    return ts.toUTC().toISOExtString();
}

/// Parses an ISO-8601 timestamp back into a `SysTime` (returns `SysTime.init` when empty).
SysTime parseServiceTimestamp(string text) @trusted
{
    auto trimmed = text.strip();
    if (trimmed.length == 0)
        return SysTime.init;
    try
    {
        return SysTime.fromISOExtString(trimmed);
    }
    catch (Exception)
    {
        return SysTime.init;
    }
}

/// Writes a JSON object formatted with 2-space indentation and trailing newline.
void writeServiceJSONFile(string path, JSONValue root, uint mode = octal!644) @trusted
{
    auto dir = dirName(path);
    if (dir.length > 0 && dir != "." && !exists(dir))
    {
        try
        {
            mkdirRecurse(dir);
        }
        catch (Exception e)
        {
            throw new Exception("create directory " ~ dir ~ ": " ~ e.msg);
        }
    }

    auto text = root.toPrettyString() ~ "\n";
    writeTextAtomic(path, text, mode);
}

unittest
{
    import std.file : remove;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    assert(prepareServiceCommandArgs("/usr/bin/redis", ["--port", "6379"]) == ["/usr/bin/redis", "--port", "6379"]);
    bool threwEmpty = false;
    try { prepareServiceCommandArgs("   ", null); }
    catch (Exception) { threwEmpty = true; }
    assert(threwEmpty);

    assert(isMissingServiceProcessOutput(""));
    assert(isMissingServiceProcessOutput("ERROR: Process not found."));
    assert(isMissingServiceProcessOutput("kill: No such process"));
    assert(!isMissingServiceProcessOutput("operation not permitted"));

    bool custom = false;
    auto aa = envListToAA(["FOO=1", "BAR=2", "FOO=3"], custom);
    assert(custom && aa["FOO"] == "3" && aa["BAR"] == "2");
    assert(environmentSliceValue(["A=x", "a=y"], "a") == "y");
    assert(setEnvironmentSliceValue(["A=1", "B=2"], "A", "9") == ["A=9", "B=2"]);

    // Verify timestamp round-trip
    auto ts = SysTime(DateTime(2026, 6, 30, 12, 0, 0), UTC());
    assert(parseServiceTimestamp(formatServiceTimestamp(ts)) == ts);
    assert(formatServiceTimestamp(SysTime.init) == "");

    // Verify live process detection & stop on a real short-lived child.
    version (Posix)
    {
        auto tmp = createTempDir("polka-proc-test-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto logPath = tmp ~ "/child.log";
        auto f = openServiceLog(logPath);
        int pid = 0;
        scope (exit)
        {
            if (f.isOpen)
                f.close();
            if (pid > 0)
                stopServicePID(pid);
        }

        pid = spawnDetachedServiceProcess("/bin/sh", ["-c", "echo started; sleep 30"], f, ["TEST_ENV=ok"]);
        f.close();
        assert(pid > 0);
        assert(servicePIDIsLive(pid));

        // Wait briefly for child shell to flush "started" before killing it.
        bool sawOutput = false;
        foreach (_; 0 .. 40)
        {
            if (readText(logPath).canFind("started"))
            {
                sawOutput = true;
                break;
            }
            Thread.sleep(dur!"msecs"(25));
        }
        assert(sawOutput);

        stopServicePID(pid);
        // Wait briefly for init/kernel cleanup after SIGKILL
        foreach (_; 0 .. 20)
        {
            if (!servicePIDIsLive(pid))
                break;
            Thread.sleep(dur!"msecs"(25));
        }
        assert(!servicePIDIsLive(pid));
    }
}
