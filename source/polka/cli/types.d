module polka.cli.types;

// Core CLI types, error formatting, environment slice manipulation,
// path/file predicates, managed PHP runtime overlays, and subprocess I/O
// shared across the polka.cli command package.

import core.stdc.errno : errno;
import core.thread : Thread;
import std.algorithm : canFind, endsWith, startsWith;
import std.array : appender;
import std.conv : to;
import std.datetime : Clock, SysTime, UTC;
import std.exception : collectException;
import std.file : exists, mkdirRecurse, rmdirRecurse;
import std.format : format;
import std.path : buildPath, dirName, dirSeparator, extension, isAbsolute;
import std.process : Config_ = Config, environment, Pid, pipe, ProcessException, spawnProcess, wait;
import std.stdio : File, stdin, stdout_ = stdout, stderr_ = stderr;
import std.string : icmp, indexOf, replace, split, strip, toLower, toStringz;

import polka.backend :
    BufferTextWriter,
    ConfigNotExistException,
    defaultStore,
    FileTextWriter,
    isConfigNotExist,
    isNotExistErrno,
    statPathExists,
    Store,
    storeForRoot,
    storeForWorkingDirectory,
    TextWriter,
    writeOut,
    writeOutf,
    writeOutln;
import polka.service : currentEnvList, envListToAA, prepareServiceCommandArgs;
import polka.support.fs : cleanPath, currentOS, pathsEqual;

version (Posix)
{
    import core.sys.posix.fcntl : O_RDONLY, open;
    import core.sys.posix.unistd : close, read_ = read;
}
version (Windows)
{
    import std.stdio : _fileno;
}

/// Bundled OpenSSL configuration path relative to a managed PHP installation.
enum string managedOpenSSLConfig = "extras/ssl/openssl.cnf";

/// Test seam controlling wall-clock timestamps across serve/TLS/service commands.
/// When `null`, callers fall back to `Clock.currTime(UTC())`.
SysTime delegate() serveNowFunc;

/// Returns the current UTC timestamp via `serveNowFunc` or `Clock.currTime(UTC())`.
SysTime callServeNow() @trusted
{
    if (serveNowFunc !is null)
        return serveNowFunc().toUTC();
    return Clock.currTime(UTC());
}

/// Reports whether dispatching `tool` targeting `target` must execute via managed PHP CLI.
bool dispatchPHARRequiresManagedPHP(string tool, string target) @safe pure
{
    if (!target.strip().toLower().endsWith(".phar"))
        return false;

    switch (tool.strip().toLower())
    {
        case "composer":
            return true;
        default:
            return false;
    }
}

/// Reports whether `value` contains an OS directory separator or `/`.
bool hasPathSeparator(string value) @safe pure nothrow @nogc
{
    foreach (c; value)
    {
        if (c == '/' || c == '\\' || c == dirSeparator[0])
            return true;
    }
    return false;
}

/// Resolves an executable target for `polka exec` or worker commands using shell `PATH` / `PATHEXT`.
string resolveExecTarget(string goos, string command, const(string)[] env) @safe
{
    auto trimmed = command.strip();
    if (trimmed.length == 0)
        throw new Exception("exec command cannot be empty");
    if (isAbsolute(trimmed) || hasPathSeparator(trimmed))
        return trimmed;

    string k, pathValue;
    lookupEnvValue(goos, env, "PATH", k, pathValue);
    foreach (pathEntry; splitPathList(goos, pathValue))
    {
        foreach (candidate; execCandidates(goos, pathEntry, trimmed, env))
        {
            if (fileExists(candidate))
                return candidate;
        }
    }

    throw new Exception(format(`command "%s" not found in Polka shell PATH`, command));
}

/// Splits a platform `PATH` string on `:` (POSIX) or `;` (Windows), dropping blank segments.
string[] splitPathList(string goos, string pathValue) @safe pure
{
    auto parts = pathValue.split(pathListSeparator(goos));
    string[] entries;
    entries.reserve(parts.length);
    foreach (part; parts)
    {
        auto trimmed = part.strip();
        if (trimmed.length > 0)
            entries ~= trimmed;
    }
    return entries;
}

/// Generates candidate executable paths inside `dir` honoring Windows `PATHEXT` when extensionless.
string[] execCandidates(string goos, string dir, string command, const(string)[] env) @safe pure
{
    auto base = buildPath(dir, command);
    if (goos != "windows")
        return [base];
    if (extension(command).length != 0)
        return [base];

    auto exts = windowsPathExts(env);
    string[] candidates;
    candidates.reserve(exts.length + 1);
    foreach (ext; exts)
        candidates ~= base ~ ext;
    candidates ~= base;
    return candidates;
}

/// Extracts lowercased Windows executable extensions from `PATHEXT` or defaults to `.com;.exe;.bat;.cmd`.
string[] windowsPathExts(const(string)[] env) @safe pure
{
    string k, value;
    bool ok = lookupEnvValue("windows", env, "PATHEXT", k, value);
    if (!ok || value.strip().length == 0)
        value = ".COM;.EXE;.BAT;.CMD";

    auto parts = value.split(";");
    string[] result;
    result.reserve(parts.length);
    foreach (part; parts)
    {
        auto trimmed = part.strip();
        if (trimmed.length == 0)
            continue;
        result ~= trimmed.toLower();
    }
    return result;
}

/// Process-exit error matching Go CLI's `statusError` semantics.
/// When `hasErr` is true, `handleRunError` prints `error: <msg>` to stderr
/// and optionally prints `usage` after a blank line before returning `code`.
final class CliStatusException : Exception
{
    int code = 1;
    bool showUsage = false;
    string usage;
    bool hasErr = true;

    this(int code, string msg, bool showUsage = false, string usage = "", bool hasErr = true, string file = __FILE__, size_t line = __LINE__) @safe pure nothrow
    {
        super(msg, file, line);
        this.code = code;
        this.showUsage = showUsage;
        this.usage = usage;
        this.hasErr = hasErr && msg.length > 0;
    }
}

/// Carries output streams, root override flag value, and propagated child exit code.
struct CommandContext
{
    TextWriter stdout;
    TextWriter stderr;
    string rootDir;
    int exitCode = 0;

    /// Resolves the project store for normal commands (`--root` or CWD discovery).
    Store store() const
    {
        return resolveStore(rootDir);
    }

    /// Resolves the store anchored directly at CWD for `init`.
    Store initStore() const
    {
        return resolveInitStore(rootDir);
    }
}

/// Resolves an explicit `--root` store or discovers the project store from CWD.
Store resolveStore(string root)
{
    if (root.strip().length > 0)
        return storeForRoot(root);
    return defaultStore();
}

/// Resolves a store anchored directly at the current working directory.
Store resolveInitStore(string root)
{
    return storeForWorkingDirectory(root);
}

/// Formats `s` using Go `%q`-style double quotes for exact error message parity.
string quoteGoString(string s) @safe pure
{
    auto buf = appender!string();
    buf.put('"');
    foreach (char c; s)
    {
        switch (c)
        {
            case '\\': buf.put(`\\`); break;
            case '"':  buf.put(`\"`); break;
            case '\n': buf.put(`\n`); break;
            case '\r': buf.put(`\r`); break;
            case '\t': buf.put(`\t`); break;
            default:
                if (c < 0x20 || c == 0x7f)
                    buf.put(format(`\x%02x`, cast(ubyte) c));
                else
                    buf.put(c);
                break;
        }
    }
    buf.put('"');
    return buf.data;
}

/// Unquotes a double-quoted Go/dotenv scalar (`"..."`), validating escape sequences.
string unquoteDoubleQuotedString(string quoted) @safe pure
{
    if (quoted.length < 2 || quoted[0] != '"' || quoted[$ - 1] != '"')
        throw new Exception("invalid double-quoted syntax");

    auto inner = quoted[1 .. $ - 1];
    auto buf = appender!string();
    for (size_t i = 0; i < inner.length; i++)
    {
        char c = inner[i];
        if (c == '\n' || c == '"')
            throw new Exception("invalid unescaped character in quoted string");
        if (c != '\\')
        {
            buf.put(c);
            continue;
        }

        i++;
        if (i >= inner.length)
            throw new Exception("unterminated escape sequence");

        char esc = inner[i];
        switch (esc)
        {
            case 'a':  buf.put('\a'); break;
            case 'b':  buf.put('\b'); break;
            case 'f':  buf.put('\f'); break;
            case 'n':  buf.put('\n'); break;
            case 'r':  buf.put('\r'); break;
            case 't':  buf.put('\t'); break;
            case 'v':  buf.put('\v'); break;
            case '\\': buf.put('\\'); break;
            case '"':  buf.put('"'); break;
            case 'x':
            {
                if (i + 2 >= inner.length)
                    throw new Exception("short \\x escape");
                uint val = parseHexDigits(inner[i + 1 .. i + 3]);
                buf.put(cast(char) val);
                i += 2;
                break;
            }
            case 'u':
            {
                if (i + 4 >= inner.length)
                    throw new Exception("short \\u escape");
                uint cp = parseHexDigits(inner[i + 1 .. i + 5]);
                buf.put(cast(dchar) cp);
                i += 4;
                break;
            }
            case 'U':
            {
                if (i + 8 >= inner.length)
                    throw new Exception("short \\U escape");
                uint cp = parseHexDigits(inner[i + 1 .. i + 9]);
                buf.put(cast(dchar) cp);
                i += 8;
                break;
            }
            case '0': .. case '7':
            {
                if (i + 2 >= inner.length)
                    throw new Exception("short octal escape");
                uint v = 0;
                foreach (k; 0 .. 3)
                {
                    char oc = inner[i + k];
                    if (oc < '0' || oc > '7')
                        throw new Exception("invalid octal escape");
                    v = (v << 3) | (oc - '0');
                }
                if (v > 255)
                    throw new Exception("octal escape out of byte range");
                buf.put(cast(char) v);
                i += 2;
                break;
            }
            default:
                throw new Exception(format("unknown escape \\%s", esc));
        }
    }
    return buf.data;
}

private uint parseHexDigits(string hex) @safe pure
{
    uint val = 0;
    foreach (char c; hex)
    {
        uint d;
        if (c >= '0' && c <= '9')
            d = c - '0';
        else if (c >= 'a' && c <= 'f')
            d = 10 + (c - 'a');
        else if (c >= 'A' && c <= 'F')
            d = 10 + (c - 'A');
        else
            throw new Exception("invalid hex digit in escape");
        val = (val << 4) | d;
    }
    return val;
}

/// Prints a formatted CLI error line (`error: ...`) to `stderr`.
void writeError(TextWriter stderr, string message)
{
    writeOutln(stderr, "error: " ~ message);
}

/// Prints a printf-formatted CLI error line (`error: ...`) to `stderr`.
void writeErrorf(Args...)(TextWriter stderr, string fmtStr, Args args)
{
    writeOutf(stderr, "error: " ~ fmtStr ~ "\n", args);
}

/// Joins path fragments and normalizes separators without hitting disk.
string cleanJoinPath(string base, string[] parts...) @safe
{
    string p = base;
    foreach (part; parts)
        p = buildPath(p, part);
    return cleanPath(p);
}

/// Returns the PATH separator (`;` on Windows, `:` on POSIX).
string pathListSeparator(string goos) @safe pure nothrow @nogc
{
    return goos == "windows" ? ";" : ":";
}

/// Joins non-empty path entries using the target platform's list separator.
string joinPathList(string goos, string[] entries...) @safe pure
{
    string[] nonEmpty;
    nonEmpty.reserve(entries.length);
    foreach (entry; entries)
    {
        auto trimmed = entry.strip();
        if (trimmed.length > 0)
            nonEmpty ~= trimmed;
    }

    auto sep = pathListSeparator(goos);
    auto buf = appender!string();
    foreach (idx, item; nonEmpty)
    {
        if (idx > 0)
            buf.put(sep);
        buf.put(item);
    }
    return buf.data;
}

/// Compares environment variable names according to platform casing rules.
bool envKeysEqual(string goos, string left, string right) @safe pure
{
    if (goos == "windows")
        return icmp(left, right) == 0;
    return left == right;
}

/// Looks up `key` inside `"KEY=VALUE"` entries using target platform casing rules.
/// Note: returns the first matching entry in slice order (matching Go `lookupEnvValue`).
bool lookupEnvValue(string goos, const(string)[] env, string key, out string outKey, out string outVal) @safe pure
{
    foreach (entry; env)
    {
        auto eq = entry.indexOf('=');
        if (eq < 0)
            continue;
        auto entryKey = entry[0 .. eq];
        auto entryVal = entry[eq + 1 .. $];
        if (envKeysEqual(goos, entryKey, key))
        {
            outKey = entryKey;
            outVal = entryVal;
            return true;
        }
    }
    outKey = "";
    outVal = "";
    return false;
}

/// Replaces `key` in `"KEY=VALUE"` entries (preserving existing key casing on first match,
/// dropping duplicate entries) or appends `key=value` when absent.
string[] replaceEnvValue(string goos, const(string)[] env, string key, string value) @safe pure
{
    string[] updated;
    updated.reserve(env.length + 1);
    bool replaced = false;

    foreach (entry; env)
    {
        auto eq = entry.indexOf('=');
        if (eq < 0)
        {
            updated ~= entry;
            continue;
        }
        auto entryKey = entry[0 .. eq];
        if (!envKeysEqual(goos, entryKey, key))
        {
            updated ~= entry;
            continue;
        }
        if (replaced)
            continue;

        updated ~= entryKey ~ "=" ~ value;
        replaced = true;
    }

    if (!replaced)
        updated ~= key ~ "=" ~ value;

    return updated;
}

/// Checks whether `path` exists as a non-directory file, throwing on directories
/// or OS errors other than ENOENT.
bool regularFileExists(string path) @trusted
{
    bool isDirFlag = false;
    if (!statPathExists(path, isDirFlag))
        return false;
    if (isDirFlag)
        throw new Exception("path is a directory");
    return true;
}

/// Returns true if `path` exists on disk as a non-directory entry (ignoring stat errors).
bool fileExists(string path) @trusted
{
    if (path.strip().length == 0)
        return false;
    try
    {
        bool isDirFlag = false;
        if (!statPathExists(path, isDirFlag))
            return false;
        return !isDirFlag;
    }
    catch (Exception)
    {
        return false;
    }
}

/// Returns true if `path` exists on disk as a directory (ignoring stat errors).
bool directoryExists(string path) @trusted
{
    if (path.strip().length == 0)
        return false;
    try
    {
        bool isDirFlag = false;
        if (!statPathExists(path, isDirFlag))
            return false;
        return isDirFlag;
    }
    catch (Exception)
    {
        return false;
    }
}

/// Compares two paths lexically using OS path casing semantics.
bool samePath(string left, string right) @safe
{
    return pathsEqual(left, right);
}

/// Deletes `path` recursively if present and recreates it with mode 0755.
void recreateDirectory(string path) @trusted
{
    if (exists(path))
        rmdirRecurse(path);
    mkdirRecurse(path);
}

/// Escapes `%` as `%%` for safe interpolation into `.cmd` / `.bat` wrappers.
string escapeWindowsShimValue(string value) @safe pure
{
    return value.replace("%", "%%");
}

/// Reads the first line of `path` (up to the first `\n`, matching Go `ReadString('\n')`).
string readScriptFirstLine(string path) @trusted
{
    try
    {
        auto f = File(path, "rb");
        scope (exit) f.close();
        auto app = appender!string();
        ubyte[256] buf;
        while (true)
        {
            auto chunk = f.rawRead(buf[]);
            if (chunk.length == 0)
                break;
            foreach (idx, b; chunk)
            {
                if (b == '\n')
                {
                    app.put(cast(const(char)[]) chunk[0 .. idx + 1]);
                    return app.data;
                }
            }
            app.put(cast(const(char)[]) chunk);
        }
        return app.data;
    }
    catch (Exception e)
    {
        throw new Exception(format("open %s: %s", path, e.msg));
    }
}

/// Checks whether a script shebang line invokes one of the given interpreter names.
bool shebangUsesProgram(string line, string[] names...) @safe pure
{
    auto trimmed = line.strip();
    if (!trimmed.startsWith("#!"))
        return false;

    auto fields = splitWhitespace(trimmed[2 .. $].strip());
    if (fields.length == 0)
        return false;

    import std.path : baseName;
    auto interpreter = fields[0];
    if (icmp(baseName(interpreter), "env") == 0)
    {
        foreach (field; fields[1 .. $])
        {
            if (field.startsWith("-") || field.canFind("="))
                continue;
            interpreter = field;
            break;
        }
    }

    auto base = baseName(interpreter).strip().toLower();
    foreach (name; names)
    {
        auto lowerName = name.strip().toLower();
        if (base == lowerName || base == lowerName ~ ".exe")
            return true;
    }
    return false;
}

/// Reports whether `line` starts with a PHP shebang (`php` or `env php`).
bool isPHPShebang(string line) @safe pure
{
    return shebangUsesProgram(line, "php");
}

/// Reports whether `line` starts with a POSIX `sh` or `bash` shebang.
bool isPOSIXShellShebang(string line) @safe pure
{
    return shebangUsesProgram(line, "sh", "bash");
}

private string[] splitWhitespace(string s) @safe pure
{
    string[] parts;
    size_t i = 0;
    while (i < s.length)
    {
        while (i < s.length && isAsciiSpace(s[i]))
            i++;
        if (i >= s.length)
            break;
        size_t start = i;
        while (i < s.length && !isAsciiSpace(s[i]))
            i++;
        parts ~= s[start .. i];
    }
    return parts;
}

private bool isAsciiSpace(char c) @safe pure nothrow @nogc
{
    return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f';
}

/// Overlays generated `PHPRC` (`php.ini`) and `OPENSSL_CONF` (`extras/ssl/openssl.cnf`)
/// when present beside a managed PHP executable.
string[] applyManagedPHPRuntimeConfig(string goos, const(string)[] env, string phpTarget) @trusted
{
    string[] updated = env.dup;

    auto phpIniPath = buildPath(dirName(phpTarget), "php.ini");
    bool iniExists = false;
    try
    {
        iniExists = regularFileExists(phpIniPath);
    }
    catch (Exception e)
    {
        throw new Exception(format("stat managed php.ini %s: %s", phpIniPath, e.msg));
    }
    if (iniExists)
        updated = replaceEnvValue(goos, updated, "PHPRC", phpIniPath);

    string opensslConfigPath;
    if (managedPHPOpenSSLConfigPath(phpTarget, opensslConfigPath))
        updated = replaceEnvValue(goos, updated, "OPENSSL_CONF", opensslConfigPath);

    return updated;
}

/// Locates `extras/ssl/openssl.cnf` beside (or one directory above) `phpTarget`.
bool managedPHPOpenSSLConfigPath(string phpTarget, out string foundPath) @trusted
{
    auto phpDir = dirName(phpTarget);
    auto candidates = [
        buildPath(phpDir, "extras", "ssl", "openssl.cnf"),
        buildPath(dirName(phpDir), "extras", "ssl", "openssl.cnf"),
    ];
    bool[string] seen;
    foreach (candidate; candidates)
    {
        auto cleanCandidate = cleanPath(candidate);
        if ((cleanCandidate in seen) !is null)
            continue;
        seen[cleanCandidate] = true;

        bool existsFlag = false;
        try
        {
            existsFlag = regularFileExists(cleanCandidate);
        }
        catch (Exception e)
        {
            throw new Exception(format("stat managed openssl config %s: %s", cleanCandidate, e.msg));
        }
        if (existsFlag)
        {
            foundPath = cleanCandidate;
            return true;
        }
    }

    foundPath = "";
    return false;
}

/// Reports whether `exception` wraps a missing-file condition (either backend
/// `ConfigNotExistException`, `FileException`, or `std.stdio.File` `ErrnoException`
/// with ENOENT / ERROR_FILE_NOT_FOUND).
bool isMissingFileError(Throwable err) @trusted nothrow @nogc
{
    if (isConfigNotExist(err))
        return true;
    import std.exception : ErrnoException;
    import std.file : FileException;
    if (auto fe = cast(FileException) err)
        return isNotExistErrno(fe.errno);
    if (auto ee = cast(ErrnoException) err)
        return isNotExistErrno(ee.errno);
    return false;
}

/// Streaming input reader abstraction mirroring Go's `io.Reader` for `executeTargetWithIO`.
interface ByteReader
{
    /// Reads up to `buf.length` bytes into `buf`, returning byte count (`0` on EOF).
    ptrdiff_t read(ubyte[] buf);
}

/// `ByteReader` backed by an in-memory byte slice.
final class SliceByteReader : ByteReader
{
    private const(ubyte)[] data;
    private size_t offset = 0;

    this(const(ubyte)[] data) @safe pure nothrow
    {
        this.data = data;
    }

    override ptrdiff_t read(ubyte[] buf) @safe pure nothrow
    {
        if (offset >= data.length || buf.length == 0)
            return 0;
        size_t avail = data.length - offset;
        size_t n = buf.length < avail ? buf.length : avail;
        buf[0 .. n] = data[offset .. offset + n];
        offset += n;
        return cast(ptrdiff_t) n;
    }
}

/// `ByteReader` backed by an open `std.stdio.File`.
final class FileByteReader : ByteReader
{
    private File file;

    this(File f)
    {
        this.file = f;
    }

    override ptrdiff_t read(ubyte[] buf) @trusted
    {
        if (buf.length == 0)
            return 0;
        auto slice = file.rawRead(buf);
        return cast(ptrdiff_t) slice.length;
    }

    /// Closes the wrapped file handle if it is still open.
    void closeStream() @trusted
    {
        if (file.isOpen)
            file.close();
    }
}

/// Executes `target` with `args`, streaming to `stdout`/`stderr` and inheriting process env.
int executeTarget(TextWriter stdout, TextWriter stderr, string target, const(string)[] args)
{
    return executeTargetWithIO(stdout, stderr, null, null, target, args);
}

/// Executes `target` with `args` and explicit environment slice `env`.
int executeTargetWithEnv(TextWriter stdout, TextWriter stderr, const(string)[] env, string target, const(string)[] args)
{
    return executeTargetWithIO(stdout, stderr, null, env, target, args);
}

/// Executes `target` with explicit stdin/stdout/stderr and environment, returning
/// the child exit code or throwing on launch failures (not non-zero child exits).
int executeTargetWithIO(
    TextWriter stdout,
    TextWriter stderr,
    ByteReader stdinReader,
    const(string)[] env,
    string target,
    const(string)[] args
) @trusted
{
    auto cmdArgs = prepareServiceCommandArgs(target, args);

    bool hasCustomEnv = (env !is null);
    string[string] envAA;
    if (hasCustomEnv)
    {
        bool dummy = false;
        envAA = envListToAA(env, dummy);
    }

    Config_ cfg = Config_.none;
    if (hasCustomEnv)
        cfg |= Config_.newEnv;

    File stdinStream;
    File stdinWriteEnd;
    bool hasStdinPipe = false;
    if (stdinReader !is null)
    {
        auto sp = pipe();
        stdinStream = sp.readEnd;
        stdinWriteEnd = sp.writeEnd;
        hasStdinPipe = true;
    }
    else if (stdin.isOpen)
    {
        stdinStream = stdin;
        cfg |= Config_.retainStdin;
    }
    else
    {
        stdinStream = openNullRead();
    }

    bool directOut = isStdStreamWriter(stdout, stdout_);
    bool directErr = isStdStreamWriter(stderr, stderr_);

    File outWriteEnd;
    File outReadEnd;
    bool hasOutPipe = false;
    if (directOut)
    {
        outWriteEnd = stdout_;
        cfg |= Config_.retainStdout;
    }
    else
    {
        auto op = pipe();
        outReadEnd = op.readEnd;
        outWriteEnd = op.writeEnd;
        hasOutPipe = true;
    }

    File errWriteEnd;
    File errReadEnd;
    bool hasErrPipe = false;
    if (directErr)
    {
        errWriteEnd = stderr_;
        cfg |= Config_.retainStderr;
    }
    else
    {
        auto ep = pipe();
        errReadEnd = ep.readEnd;
        errWriteEnd = ep.writeEnd;
        hasErrPipe = true;
    }

    Pid pid;
    try
    {
        pid = spawnProcess(cmdArgs, stdinStream, outWriteEnd, errWriteEnd, envAA, cfg);
    }
    catch (ProcessException e)
    {
        if (hasStdinPipe)
            collectException(stdinWriteEnd.close());
        if (hasOutPipe)
            collectException(outReadEnd.close());
        if (hasErrPipe)
            collectException(errReadEnd.close());
        throw new Exception(format("run %s: %s", target, e.msg));
    }

    Thread stdinThread;
    if (hasStdinPipe)
    {
        stdinThread = spawnStdinPumpThread(stdinReader, stdinWriteEnd);
        stdinThread.start();
    }

    Thread outThread;
    if (hasOutPipe)
    {
        outThread = spawnOutputPumpThread(outReadEnd, stdout);
        outThread.start();
    }

    Thread errThread;
    if (hasErrPipe)
    {
        errThread = spawnOutputPumpThread(errReadEnd, stderr);
        errThread.start();
    }

    if (stdinThread !is null)
        stdinThread.join();
    if (outThread !is null)
        outThread.join();
    if (errThread !is null)
        errThread.join();

    return wait(pid);
}

private final class StdinPumpThread : Thread
{
    private ByteReader reader;
    private File writeEnd;

    this(ByteReader reader, File writeEnd) @trusted
    {
        this.reader = reader;
        this.writeEnd = writeEnd;
        super(&runLoop);
    }

    private void runLoop()
    {
        try
        {
            ubyte[32768] buf;
            while (true)
            {
                auto n = reader.read(buf[]);
                if (n <= 0)
                    break;
                writeEnd.rawWrite(buf[0 .. cast(size_t) n]);
            }
        }
        catch (Exception) {}
        finally
        {
            collectException(writeEnd.close());
        }
    }
}

private final class OutputPumpThread : Thread
{
    private File readEnd;
    private TextWriter dest;

    this(File readEnd, TextWriter dest) @trusted
    {
        this.readEnd = readEnd;
        this.dest = dest;
        super(&runLoop);
    }

    private void runLoop()
    {
        try
        {
            foreach (chunk; readEnd.byChunk(4096))
                writeOut(dest, cast(const(char)[]) chunk);
        }
        catch (Exception) {}
        finally
        {
            collectException(readEnd.close());
        }
    }
}

private Thread spawnStdinPumpThread(ByteReader reader, File writeEnd) @trusted
{
    return new StdinPumpThread(reader, writeEnd);
}

private Thread spawnOutputPumpThread(File readEnd, TextWriter dest) @trusted
{
    return new OutputPumpThread(readEnd, dest);
}

private bool isStdStreamWriter(TextWriter writer, File stdFile) @trusted
{
    auto fw = cast(FileTextWriter) writer;
    if (fw is null)
        return false;
    try
    {
        auto f = fw.streamFile();
        if (!f.isOpen || !stdFile.isOpen)
            return false;
        return f.fileno == stdFile.fileno;
    }
    catch (Exception)
    {
        return false;
    }
}

private File openNullRead() @trusted
{
    version (Windows)
        return File("NUL", "rb");
    else
        return File("/dev/null", "rb");
}

unittest
{
    import std.exception : assertThrown;

    // Verify quoteGoString matches Go %q escaping for specials and control bytes.
    assert(quoteGoString(`hello "world"`) == `"hello \"world\""`);
    assert(quoteGoString("a\nb\tc\\d\x01") == `"a\nb\tc\\d\x01"`);

    // Verify unquoteDoubleQuotedString decodes standard escapes cleanly.
    assert(unquoteDoubleQuotedString(`"hello \"world\"\n\t\\x"`) == "hello \"world\"\n\t\\x");
    assert(unquoteDoubleQuotedString(`"\u0041\102\x43"`) == "ABC");
    assertThrown!Exception(unquoteDoubleQuotedString(`"unterminated\`));

    // Verify platform-aware environment key lookup & replacement.
    string k, v;
    assert(lookupEnvValue("windows", ["App_Env=dev", "PATH=/bin"], "app_env", k, v));
    assert(k == "App_Env" && v == "dev");
    assert(!lookupEnvValue("linux", ["App_Env=dev"], "app_env", k, v));

    auto replaced = replaceEnvValue("windows", ["App_Env=dev", "APP_ENV=dup", "OTHER=1"], "app_env", "prod");
    assert(replaced == ["App_Env=prod", "OTHER=1"]);
    assert(joinPathList("windows", "a", "", "b") == "a;b");
    assert(joinPathList("linux", "a", "", "b") == "a:b");

    // Verify shebang parser for php, env php, sh, and negative inputs.
    assert(isPHPShebang("#!/usr/bin/env -S php\n"));
    assert(isPHPShebang("#!/usr/local/bin/php.exe"));
    assert(isPOSIXShellShebang("#!/bin/sh\n"));
    assert(!isPHPShebang("#!/usr/bin/python3"));

    // Verify executeTargetWithIO streams stdin/stdout/stderr and captures exit codes.
    version (Posix)
    {
        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();
        auto code = executeTargetWithIO(
            outBuf,
            errBuf,
            new SliceByteReader(cast(const(ubyte)[]) "piped-input"),
            ["GREETING=hi"],
            "/bin/sh",
            ["-c", `read line; printf "%s:%s" "$GREETING" "$line"; printf "err" >&2; exit 7`]
        );
        assert(code == 7);
        assert(outBuf.text() == "hi:piped-input");
        assert(errBuf.text() == "err");
    }
}
