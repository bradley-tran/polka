module polka.backend.aliases;

import core.stdc.errno : errno;
import core.sync.mutex : Mutex;
import core.thread : Thread;
import std.algorithm : any, canFind, startsWith;
import std.array : appender;
import std.conv : to;
import std.exception : collectException;
import std.file : exists, FileException, isDir, isFile, mkdirRecurse, read, remove;
import std.format : format;
import std.path : buildPath, dirName, extension, isAbsolute;
import std.process : Config_ = Config, environment, Pid, pipe, ProcessException, spawnProcess, wait;
import std.stdio : File;
import std.string : icmp, indexOf, split, strip, toLower, toStringz;

public import polka.config :
    Config,
    CurrentManifestVersion,
    DatabaseConfig,
    Environment,
    EnvironmentFile,
    hasPHPCLI,
    MailpitConfig,
    MeilisearchConfig,
    normalizePHPMemoryLimit,
    normalizePHPExtensions,
    normalizeOPcacheConfig,
    normalizeOPcachePreset,
    OPcachePresetDev,
    OPcachePresetNone,
    OPcachePresetProduction,
    PECLExtensionConfig,
    phpCLIProvider,
    PHPMyAdminConfig,
    primaryPHPTool,
    primaryPHPVersion,
    ProjectFile,
    RabbitMQConfig,
    RedisConfig,
    ServerConfig,
    SettingsConfig,
    splitWorkerCommand,
    ToolsConfig,
    TraefikConfig,
    WorkerConfig;

public import polka.plugins :
    FrameworkPlugin,
    PluginRegistry = Registry,
    ProjectPreset,
    ScaffoldContext;

public import polka.tools :
    CacheMissException,
    DefaultPHPSDKVersion,
    HTTPToolDownloader = HTTPDownloader,
    InstallRequest,
    InstallResult,
    isCacheMiss,
    LogEntry,
    ToolDependencyProvider,
    ToolDispatchRequest = DispatchRequest,
    ToolDownloadContext = DownloadContext,
    ToolDownloader = Downloader,
    ToolInstallContext = InstallContext,
    ToolLogEntry = LogEntry,
    ToolPlugin,
    ToolRegistry = Registry;

import polka.plugins : newDefaultRegistry_ = newDefaultRegistry;
import polka.support.fs : currentOS, octal;
import polka.tools : newDefaultRegistry_Tools = newDefaultRegistry, newRegistry_Tools = newRegistry;
import polka.tools.ids;

version (Posix)
{
    import core.stdc.errno : EEXIST, ENOENT;
    import core.sys.posix.fcntl : O_CREAT, O_EXCL, O_WRONLY, open;
    import core.sys.posix.sys.stat : S_IFDIR, S_IFMT, stat, stat_t;
    import core.sys.posix.unistd : access, close, write_ = write, X_OK;
}
else version (Windows)
{
    import core.sys.windows.winbase :
        CloseHandle,
        CREATE_NEW,
        CreateFileW,
        GetFileAttributesW,
        GetLastError,
        INVALID_HANDLE_VALUE,
        WriteFile;
    import core.sys.windows.winnt :
        FILE_ATTRIBUTE_DIRECTORY,
        FILE_ATTRIBUTE_NORMAL,
        GENERIC_WRITE,
        INVALID_FILE_ATTRIBUTES;
    import core.sys.windows.winerror :
        ERROR_ALREADY_EXISTS,
        ERROR_FILE_EXISTS,
        ERROR_FILE_NOT_FOUND,
        ERROR_PATH_NOT_FOUND;
    import std.utf : toUTF16z;
}

/// Package-internal aliases matching Go tool constant names.
package(polka.backend):
enum string toolPHP = PHP;
enum string toolPHPZTS = PHPZTS;
enum string toolPHPDevel = PHPDevel;
enum string toolPHPSDK = PHPSDK;
enum string toolFrankenPHP = FrankenPHP;
enum string toolComposer = Composer;
enum string toolPIE = PIE;
enum string toolNodeJS = NodeJS;
enum string toolNode = Node;
enum string toolNPM = NPM;
enum string toolNPX = NPX;
enum string toolYarn = Yarn;
enum string toolMago = Mago;
enum string toolNginx = Nginx;
enum string toolApache = Apache;
enum string toolMailpit = Mailpit;
enum string toolPHPMyAdmin = PHPMyAdmin;
enum string toolMeilisearch = Meilisearch;
enum string toolRedis = Redis;
enum string toolRedisCLI = RedisCLI;
enum string toolRabbitMQ = RabbitMQ;
enum string toolErlang = Erlang;
enum string toolTraefik = Traefik;
enum string toolRoadRunner = RoadRunner;
enum string toolMySQL = MySQL;
enum string toolMariaDB = MariaDB;
enum string toolPostgreSQL = PostgreSQL;
enum string toolPSQL = PSQL;
enum string toolSQLite = SQLite;

public:

/// Typed exception thrown when a config or state file does not exist (`os.ErrNotExist` parity).
class ConfigNotExistException : Exception
{
    this(string msg, string file = __FILE__, size_t line = __LINE__) @safe pure nothrow
    {
        super(msg, file, line);
    }
}

/// Reports whether `err` represents a missing config or state file (`os.IsNotExist` / `os.ErrNotExist`).
bool isConfigNotExist(Throwable err) @safe pure nothrow @nogc
{
    return (cast(ConfigNotExistException) err) !is null;
}

/// Constructs a `ToolRegistry` containing `plugins`.
ToolRegistry newToolRegistry(ToolPlugin[] plugins...)
{
    return newRegistry_Tools(plugins);
}

/// Returns the default `ToolRegistry` containing all built-in manifest tool plugins.
ToolRegistry newDefaultToolRegistry()
{
    return newDefaultRegistry_Tools();
}

/// Returns the default `PluginRegistry` containing all built-in framework and preset plugins.
PluginRegistry newDefaultPluginRegistry()
{
    return newDefaultRegistry_();
}

// ============================================================================
// Streaming Text Writer Abstraction (io.Writer equivalent)
// ============================================================================

/// Destination stream interface matching Go `io.Writer` semantics for live command/extension output.
interface TextWriter
{
    void write(const(char)[] chunk);
}

/// Thread-safe in-memory buffer implementing `TextWriter` (like `bytes.Buffer`).
final class BufferTextWriter : TextWriter
{
    private Mutex mu;
    private typeof(appender!string()) buf;

    this()
    {
        mu = new Mutex();
        buf = appender!string();
    }

    override void write(const(char)[] chunk)
    {
        if (chunk.length == 0)
            return;
        mu.lock();
        scope (exit) mu.unlock();
        buf.put(chunk);
    }

    string text()
    {
        mu.lock();
        scope (exit) mu.unlock();
        return buf.data.idup;
    }

    void clear()
    {
        mu.lock();
        scope (exit) mu.unlock();
        buf = appender!string();
    }
}

/// Thread-safe `std.stdio.File`-backed `TextWriter`.
final class FileTextWriter : TextWriter
{
    private Mutex mu;
    private File file;

    this(File f)
    {
        mu = new Mutex();
        file = f;
    }

    /// Exposes the underlying `std.stdio.File` for direct subprocess descriptor redirection.
    File streamFile()
    {
        return file;
    }

    override void write(const(char)[] chunk)
    {
        if (chunk.length == 0)
            return;
        mu.lock();
        scope (exit) mu.unlock();
        file.rawWrite(chunk);
        file.flush();
    }
}

/// Fan-out writer mirroring `io.MultiWriter`.
final class MultiTextWriter : TextWriter
{
    private TextWriter[] writers;

    this(TextWriter[] writers...)
    {
        foreach (w; writers)
        {
            if (w !is null)
                this.writers ~= w;
        }
    }

    override void write(const(char)[] chunk)
    {
        if (chunk.length == 0)
            return;
        foreach (w; writers)
            w.write(chunk);
    }
}

/// Writes `text` to `w` when `w` is non-null.
void writeOut(TextWriter w, const(char)[] text)
{
    if (w !is null && text.length > 0)
        w.write(text);
}

/// Writes `text` followed by `\n` to `w` when `w` is non-null.
void writeOutln(TextWriter w, const(char)[] text)
{
    if (w is null)
        return;
    w.write(text);
    w.write("\n");
}

/// Formats and writes to `w` when `w` is non-null.
void writeOutf(Args...)(TextWriter w, string fmtStr, Args args)
{
    if (w is null)
        return;
    w.write(format(fmtStr, args));
}

// ============================================================================
// Filesystem & Subprocess Helpers
// ============================================================================

/// Returns true if `errCode` represents a missing file/path on the host OS.
bool isNotExistErrno(uint errCode) @safe pure nothrow @nogc
{
    version (Posix)
    {
        return errCode == ENOENT;
    }
    else version (Windows)
    {
        return errCode == ERROR_FILE_NOT_FOUND || errCode == ERROR_PATH_NOT_FOUND;
    }
    else
    {
        return false;
    }
}

/// Reads all bytes from `path`, throwing `ConfigNotExistException` when `path` does not exist
/// while preserving other OS errors (permission denied, directory read, etc.).
ubyte[] readFileBytesStrict(string path) @trusted
{
    try
    {
        return cast(ubyte[]) read(path);
    }
    catch (FileException e)
    {
        if (isNotExistErrno(e.errno))
            throw new ConfigNotExistException(e.msg);
        throw e;
    }
}

/// Reads UTF-8/raw text from `path`, throwing `ConfigNotExistException` if absent.
string readTextStrict(string path) @trusted
{
    auto bytes = readFileBytesStrict(path);
    return (cast(const(char)[]) bytes).idup;
}

/// Stats `path` (following symlinks like `os.Stat`). Returns `false` if `path` does not exist,
/// `true` if it exists (setting `isDirOut`), or throws `FileException` on OS errors such as EACCES.
bool statPathExists(string path, out bool isDirOut) @trusted
{
    isDirOut = false;
    version (Posix)
    {
        stat_t st;
        if (stat(path.toStringz, &st) != 0)
        {
            auto err = errno;
            if (err == ENOENT)
                return false;
            throw new FileException(path, format("stat %s: errno %d", path, err));
        }
        isDirOut = (st.st_mode & S_IFMT) == S_IFDIR;
        return true;
    }
    else version (Windows)
    {
        auto attrs = GetFileAttributesW(path.toUTF16z);
        if (attrs == INVALID_FILE_ATTRIBUTES)
        {
            auto err = GetLastError();
            if (err == ERROR_FILE_NOT_FOUND || err == ERROR_PATH_NOT_FOUND)
                return false;
            throw new FileException(path, format("stat %s: error %d", path, err));
        }
        isDirOut = (attrs & FILE_ATTRIBUTE_DIRECTORY) != 0;
        return true;
    }
    else
    {
        if (!exists(path))
            return false;
        isDirOut = isDir(path);
        return true;
    }
}

/// Reports whether `path` exists as a regular non-directory file (ignoring stat errors).
bool regularFileExistsAt(string path) @trusted
{
    if (path.strip().length == 0)
        return false;
    try
    {
        bool dir = false;
        if (!statPathExists(path, dir))
            return false;
        return !dir;
    }
    catch (Exception)
    {
        return false;
    }
}

/// Removes `path` if present, ignoring ENOENT while propagating real errors.
void removeFileIgnoreNotExist(string path) @trusted
{
    try
    {
        remove(path);
    }
    catch (FileException e)
    {
        if (isNotExistErrno(e.errno))
            return;
        throw e;
    }
}

/// Atomically creates `path` with `O_CREAT|O_EXCL` parity, writing `content`.
/// Returns `false` when the destination already exists (`EEXIST`), `true` when written.
bool tryCreateExclusiveFile(string path, const(ubyte)[] content, uint mode = octal!644) @trusted
{
    version (Posix)
    {
        int fd = open(path.toStringz, O_WRONLY | O_CREAT | O_EXCL, cast(int) mode);
        if (fd < 0)
        {
            auto err = errno;
            if (err == EEXIST)
                return false;
            throw new Exception(format("create %s: errno %d", path, err));
        }

        size_t offset = 0;
        bool failed = false;
        Exception writeErr;
        while (offset < content.length)
        {
            auto n = write_(fd, content.ptr + offset, content.length - offset);
            if (n <= 0)
            {
                writeErr = new Exception(format("write %s: errno %d", path, errno));
                failed = true;
                break;
            }
            offset += cast(size_t) n;
        }
        if (close(fd) != 0 && !failed)
            throw new Exception(format("close %s: errno %d", path, errno));
        if (failed)
            throw writeErr;
        return true;
    }
    else version (Windows)
    {
        auto handle = CreateFileW(
            path.toUTF16z,
            GENERIC_WRITE,
            0,
            null,
            CREATE_NEW,
            FILE_ATTRIBUTE_NORMAL,
            null
        );
        if (handle == INVALID_HANDLE_VALUE)
        {
            auto err = GetLastError();
            if (err == ERROR_FILE_EXISTS || err == ERROR_ALREADY_EXISTS)
                return false;
            throw new Exception(format("create %s: win32 error %d", path, err));
        }
        scope (exit) CloseHandle(handle);

        size_t offset = 0;
        while (offset < content.length)
        {
            uint written = 0;
            uint chunk = cast(uint) (content.length - offset);
            if (!WriteFile(handle, content.ptr + offset, chunk, &written, null))
                throw new Exception(format("write %s: win32 error %d", path, GetLastError()));
            offset += written;
        }
        return true;
    }
}

/// Resolves an executable on `PATH` using `exec.LookPath` semantics.
string lookPath(string file) @trusted
{
    auto name = file.strip();
    if (name.length == 0)
        throw new Exception(`"" : executable file not found in $PATH`);

    version (Windows)
    {
        static bool isWinExec(string candidate) @trusted
        {
            if (!regularFileExistsAt(candidate))
                return false;
            return true;
        }

        string[] exts = [".exe", ".cmd", ".bat", ".com"];
        auto pathExt = environment.get("PATHEXT", "");
        if (pathExt.strip().length > 0)
        {
            exts = null;
            foreach (e; pathExt.split(';'))
            {
                auto norm = e.strip().toLower();
                if (norm.length > 0)
                    exts ~= norm.startsWith(".") ? norm : ("." ~ norm);
            }
        }

        bool hasExt = extension(name).length > 0;
        if (name.canFind('\\') || name.canFind('/') || (name.length >= 2 && name[1] == ':'))
        {
            if (hasExt && isWinExec(name))
                return name;
            foreach (ext; exts)
            {
                if (isWinExec(name ~ ext))
                    return name ~ ext;
            }
            throw new Exception(format("%s: executable file not found", file));
        }

        auto pathVal = environment.get("PATH", "");
        foreach (dir; pathVal.split(';'))
        {
            auto d = dir.strip();
            if (d.length == 0)
                continue;
            auto candidate = buildPath(d, name);
            if (hasExt && isWinExec(candidate))
                return candidate;
            foreach (ext; exts)
            {
                if (isWinExec(candidate ~ ext))
                    return candidate ~ ext;
            }
        }
        throw new Exception(format(`exec: "%s": executable file not found in %%PATH%%`, file));
    }
    else
    {
        static bool isPosixExec(string candidate) @trusted
        {
            if (!regularFileExistsAt(candidate))
                return false;
            return access(candidate.toStringz, X_OK) == 0;
        }

        if (name.canFind('/'))
        {
            if (isPosixExec(name))
                return name;
            throw new Exception(format(`exec: "%s": no such file or permission denied`, file));
        }

        auto pathVal = environment.get("PATH", "");
        foreach (dir; pathVal.split(':'))
        {
            auto d = dir.length == 0 ? "." : dir;
            auto candidate = buildPath(d, name);
            if (isPosixExec(candidate))
                return candidate;
        }
        throw new Exception(format(`exec: "%s": executable file not found in $PATH`, file));
    }
}

private File openNullReadFile() @trusted
{
    version (Windows)
        return File("NUL", "rb");
    else
        return File("/dev/null", "rb");
}

private File openNullWriteFile() @trusted
{
    version (Windows)
        return File("NUL", "wb");
    else
        return File("/dev/null", "wb");
}

private string[string] envListToMap(const(string)[] envList, out bool hasCustomEnv) @safe pure
{
    if (envList is null)
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
            foreach (ek, _; aa)
            {
                if (ek != key && ek.toLower() == key.toLower())
                    stale ~= ek;
            }
            foreach (sk; stale)
                aa.remove(sk);
        }
        aa[key] = val;
    }
    return aa;
}

/// Runs `cmdArgs` synchronously, streaming stdout and stderr chunks concurrently into `stdout`
/// and `stderr` (which may be `null`, shared, or distinct). Returns the child exit status.
int runCommandStreaming(
    const(string)[] cmdArgs,
    string workDir,
    const(string)[] envList,
    TextWriter stdout,
    TextWriter stderr,
    string stdinText = null
) @trusted
{
    if (cmdArgs.length == 0 || cmdArgs[0].strip().length == 0)
        throw new Exception("command cannot be empty");

    bool hasCustomEnv = false;
    auto envAA = envListToMap(envList, hasCustomEnv);
    Config_ cfg = Config_.none;
    if (hasCustomEnv)
        cfg |= Config_.newEnv;

    File stdinStream;
    File stdinWriteEnd;
    bool hasStdinPipe = false;
    if (stdinText.length > 0)
    {
        auto sp = pipe();
        stdinStream = sp.readEnd;
        stdinWriteEnd = sp.writeEnd;
        hasStdinPipe = true;
    }
    else
    {
        stdinStream = openNullReadFile();
    }

    auto outPipe = pipe();
    auto errPipe = pipe();

    Pid pid;
    try
    {
        pid = spawnProcess(
            cmdArgs,
            stdinStream,
            outPipe.writeEnd,
            errPipe.writeEnd,
            envAA,
            cfg,
            workDir.length > 0 ? workDir : null
        );
    }
    catch (ProcessException e)
    {
        if (hasStdinPipe)
            collectException(stdinWriteEnd.close());
        collectException(outPipe.readEnd.close());
        collectException(errPipe.readEnd.close());
        throw new Exception(e.msg);
    }

    Thread stdinThread;
    if (hasStdinPipe)
    {
        auto payload = stdinText;
        stdinThread = new Thread({
            try
            {
                stdinWriteEnd.rawWrite(cast(const(ubyte)[]) payload);
            }
            catch (Exception) {}
            finally
            {
                collectException(stdinWriteEnd.close());
            }
        });
        stdinThread.start();
    }

    auto outRead = outPipe.readEnd;
    auto errRead = errPipe.readEnd;

    auto outThread = new Thread({
        try
        {
            foreach (chunk; outRead.byChunk(4096))
                writeOut(stdout, cast(const(char)[]) chunk);
        }
        catch (Exception) {}
        finally
        {
            collectException(outRead.close());
        }
    });

    auto errThread = new Thread({
        try
        {
            foreach (chunk; errRead.byChunk(4096))
                writeOut(stderr, cast(const(char)[]) chunk);
        }
        catch (Exception) {}
        finally
        {
            collectException(errRead.close());
        }
    });

    outThread.start();
    errThread.start();

    if (stdinThread !is null)
        stdinThread.join();
    outThread.join();
    errThread.join();

    return wait(pid);
}

/// Runs `cmdArgs` synchronously, returning stdout (with stderr captured for errors).
int runCommandCapture(
    const(string)[] cmdArgs,
    string workDir,
    const(string)[] envList,
    out string stdoutOut,
    out string stderrOut
) @trusted
{
    auto outBuf = new BufferTextWriter();
    auto errBuf = new BufferTextWriter();
    auto code = runCommandStreaming(cmdArgs, workDir, envList, outBuf, errBuf);
    stdoutOut = outBuf.text();
    stderrOut = errBuf.text();
    return code;
}

/// Replaces or appends `key` inside `"KEY=VALUE"` environment entries.
/// Keys compare case-insensitively on Windows and case-sensitively on Posix.
string[] setProcessEnvValue(const(string)[] envList, string key, string value) @safe pure
{
    string[] result;
    result.reserve(envList.length + 1);
    bool replaced = false;
    foreach (entry; envList)
    {
        auto eq = entry.indexOf('=');
        if (eq > 0)
        {
            auto name = entry[0 .. eq];
            version (Windows)
                bool match = icmp(name, key) == 0;
            else
                bool match = name == key;
            if (match)
            {
                if (!replaced)
                {
                    result ~= key ~ "=" ~ value;
                    replaced = true;
                }
                continue;
            }
        }
        result ~= entry;
    }
    if (!replaced)
        result ~= key ~ "=" ~ value;
    return result;
}

/// Reads an environment variable value from `"KEY=VALUE"` entries.
/// Keys compare case-insensitively on Windows and case-sensitively on Posix.
string getProcessEnvValue(const(string)[] envList, string key) @safe pure
{
    string val = "";
    foreach (entry; envList)
    {
        auto eq = entry.indexOf('=');
        if (eq > 0)
        {
            auto name = entry[0 .. eq];
            version (Windows)
                bool match = icmp(name, key) == 0;
            else
                bool match = name == key;
            if (match)
                val = entry[eq + 1 .. $];
        }
    }
    return val;
}
