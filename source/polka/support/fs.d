module polka.support.fs;

import std.algorithm : any, canFind;
import std.conv : to;
import std.exception : enforce;
import std.file : exists, isDir, isFile, mkdirRecurse, read, rename, remove, rmdirRecurse, tempDir, write;
import std.path : absolutePath, buildNormalizedPath, buildPath, dirName, pathSplitter, relativePath;
import std.process : environment;
import std.random : uniform;
import std.string : endsWith, indexOf, replace, startsWith, strip, toLower;

version (Posix)
{
    import core.sys.posix.fcntl : O_CREAT, O_EXCL, O_RDONLY, O_WRONLY, open;
    import core.sys.posix.sys.stat : chmod, lstat, stat_t, S_IFLNK, S_IFMT;
    import core.sys.posix.unistd : close, getpid, readlink, write_ = write;
}
version (Windows)
{
    import core.sys.windows.winbase : GetCurrentProcessId, GetFileAttributesW, SetFileAttributesW;
    import core.sys.windows.winnt : FILE_ATTRIBUTE_READONLY;
    import std.utf : toUTF16z;
}

/// Returns the current operating system name matching Go's runtime.GOOS conventions.
string currentOS() @safe pure nothrow @nogc
{
    version (Windows)
        return "windows";
    else version (OSX)
        return "darwin";
    else version (linux)
        return "linux";
    else
        return "unknown";
}

/// Returns the current CPU architecture matching Go's runtime.GOARCH conventions.
string currentArch() @safe pure nothrow @nogc
{
    version (X86_64)
        return "amd64";
    else version (AArch64)
        return "arm64";
    else version (X86)
        return "386";
    else version (ARM)
        return "arm";
    else
        return "unknown";
}

/// Returns true when running on Windows.
bool isWindowsOS() @safe pure nothrow @nogc
{
    return currentOS() == "windows";
}

/// Normalizes path separators to forward slashes.
string toSlashPath(string p) @safe pure
{
    return p.replace("\\", "/");
}

/// Converts forward slashes to the native OS path separator.
string fromSlashPath(string p) @safe pure
{
    version (Windows)
        return p.replace("/", "\\");
    else
        return p;
}

/// Normalizes a filesystem path cleanly without requiring existence on disk.
string cleanPath(string p) @safe
{
    if (p.length == 0)
        return ".";
    return buildNormalizedPath(p);
}

/// Returns true if path exists and is a regular file (follows symlinks like os.Stat).
bool isRegularFile(string p) @trusted
{
    if (p.length == 0 || !exists(p))
        return false;
    return isFile(p);
}

/// Returns true if path exists and is a directory (follows symlinks like os.Stat).
bool isDirectory(string p) @trusted
{
    if (p.length == 0 || !exists(p))
        return false;
    return isDir(p);
}

/// Returns true if path itself is a symlink (without following it).
bool isSymlinkPath(string p) @trusted
{
    version (Posix)
    {
        import std.string : toStringz;
        stat_t st;
        if (lstat(p.toStringz, &st) != 0)
            return false;
        return (st.st_mode & S_IFMT) == S_IFLNK;
    }
    else
    {
        return false;
    }
}

/// Gets POSIX permission mode bits for path (or 0644 on Windows).
uint getFileMode(string p) @trusted
{
    version (Posix)
    {
        import std.file : getAttributes;
        return getAttributes(p) & 0xFFFF;
    }
    else
    {
        return 0x1A4; // 0644
    }
}

/// Sets file permissions on POSIX; handles read-only toggle on Windows.
void setFileMode(string p, uint mode) @trusted
{
    version (Posix)
    {
        import std.string : toStringz;
        import core.stdc.errno : errno;
        if (chmod(p.toStringz, cast(int)(mode & 0xFFFF)) != 0)
        {
            throw new Exception("chmod failed on " ~ p ~ ": errno " ~ errno.to!string);
        }
    }
    else version (Windows)
    {
        auto wp = p.toUTF16z;
        auto attrs = GetFileAttributesW(wp);
        if (attrs != uint.max)
        {
            if ((mode & 0x80) == 0) // no owner write bit
                SetFileAttributesW(wp, attrs | FILE_ATTRIBUTE_READONLY);
            else
                SetFileAttributesW(wp, attrs & ~FILE_ATTRIBUTE_READONLY);
        }
    }
}

/// Atomically writes bytes to `targetPath` using a sibling temporary file and rename.
void writeFileAtomic(string targetPath, const(ubyte)[] data, uint mode = octal!644) @trusted
{
    auto dir = dirName(targetPath);
    if (dir.length > 0 && dir != "." && !exists(dir))
        mkdirRecurse(dir);

    auto tmpPath = buildPath(dir, ".polka-tmp-" ~ randomHexSuffix(8));
    bool cleanup = true;
    scope (exit)
    {
        if (cleanup)
            removeQuiet(tmpPath);
    }

    write(tmpPath, data);
    setFileMode(tmpPath, mode);
    replaceFile(tmpPath, targetPath);
    cleanup = false;
}

/// Quietly removes a file if it exists, ignoring errors.
void removeQuiet(string p) @trusted nothrow
{
    try
    {
        if (p.length > 0 && exists(p))
            remove(p);
    }
    catch (Exception) {}
}

/// Quietly removes a directory recursively if it exists, ignoring errors.
void rmdirRecurseQuiet(string p) @trusted nothrow
{
    try
    {
        if (p.length > 0 && exists(p))
            rmdirRecurse(p);
    }
    catch (Exception) {}
}

/// Atomically writes UTF-8 text to `targetPath`.
void writeTextAtomic(string targetPath, string content, uint mode = octal!644) @trusted
{
    writeFileAtomic(targetPath, cast(const(ubyte)[]) content, mode);
}

/// Replaces `dst` with `src`, handling Windows overwrite and read-only semantics.
void replaceFile(string src, string dst) @trusted
{
    version (Windows)
    {
        if (exists(dst))
        {
            auto attrs = GetFileAttributesW(dst.toUTF16z);
            if (attrs != uint.max && (attrs & FILE_ATTRIBUTE_READONLY) != 0)
                SetFileAttributesW(dst.toUTF16z, attrs & ~FILE_ATTRIBUTE_READONLY);
            try remove(dst); catch (Exception) {}
        }
        rename(src, dst);
    }
    else
    {
        rename(src, dst);
    }
}

/// Creates a unique temporary directory under system tempDir (or specified base) and returns its path.
string createTempDir(string prefix = "polka-", string baseDir = null) @trusted
{
    auto base = baseDir.length > 0 ? baseDir : tempDir();
    if (!exists(base))
        mkdirRecurse(base);
    foreach (_; 0 .. 128)
    {
        auto candidate = buildPath(base, prefix ~ randomHexSuffix(8));
        if (!exists(candidate))
        {
            mkdirRecurse(candidate);
            return candidate;
        }
    }
    throw new Exception("failed to allocate temporary directory under " ~ base);
}

/// Returns the user cache directory matching Go's os.UserCacheDir().
string userCacheDir() @trusted
{
    version (Windows)
    {
        auto localAppData = environment.get("LocalAppData", "");
        if (localAppData.length > 0)
            return localAppData;
        return environment.get("USERPROFILE", "");
    }
    else version (OSX)
    {
        auto home = environment.get("HOME", "");
        return home.length > 0 ? buildPath(home, "Library", "Caches") : "";
    }
    else
    {
        auto xdg = environment.get("XDG_CACHE_HOME", "");
        if (xdg.length > 0)
            return xdg;
        auto home = environment.get("HOME", "");
        return home.length > 0 ? buildPath(home, ".cache") : "";
    }
}

/// Returns the path of the currently running executable.
string currentExecutablePath() @trusted
{
    import core.runtime : Runtime;
    auto args = Runtime.args;
    version (linux)
    {
        char[4096] buf;
        import std.string : fromStringz;
        auto len = readlink("/proc/self/exe".ptr, buf.ptr, buf.length - 1);
        if (len > 0)
        {
            buf[len] = 0;
            return buf[0 .. len].idup;
        }
    }
    if (args.length > 0 && args[0].length > 0)
        return absolutePath(args[0]);
    return "polka";
}

/// Case-insensitive path comparison on Windows, exact on POSIX.
bool pathsEqual(string a, string b) @safe
{
    auto ca = cleanPath(a);
    auto cb = cleanPath(b);
    version (Windows)
        return toLower(ca) == toLower(cb);
    else
        return ca == cb;
}

/// Checks if `target` is contained within `baseDir` without escaping via `..`.
bool isPathWithin(string baseDir, string target) @safe
{
    auto absBase = cleanPath(absolutePath(baseDir));
    auto absTarget = cleanPath(absolutePath(target));
    auto rel = relativePath(absTarget, absBase);
    if (rel == "." || rel.length == 0)
        return true;
    auto norm = toSlashPath(rel);
    return norm != ".." && !norm.startsWith("../");
}

/// Resolves symlinks for the nearest existing ancestor of `p`, then joins the remaining tail.
string resolveExistingPath(string p) @trusted
{
    auto abs = cleanPath(absolutePath(p));
    string[] tail;
    string cur = abs;
    while (true)
    {
        if (exists(cur) || isSymlinkPath(cur))
        {
            auto resolved = evalSymlinksImpl(cur);
            foreach_reverse (seg; tail)
                resolved = buildPath(resolved, seg);
            return cleanPath(resolved);
        }
        auto parent = dirName(cur);
        if (parent == cur || parent.length == 0)
            return abs;
        import std.path : baseName;
        tail ~= baseName(cur);
        cur = parent;
    }
}

private string evalSymlinksImpl(string p) @trusted
{
    version (Posix)
    {
        import core.sys.posix.stdlib : realpath;
        import std.string : toStringz, fromStringz;
        char[4096] buf;
        auto res = realpath(p.toStringz, buf.ptr);
        if (res !is null)
            return fromStringz(res).idup;
    }
    return cleanPath(absolutePath(p));
}

/// Generates an N-hex-character random lowercase string.
string randomHexSuffix(size_t hexChars) @trusted
{
    static immutable char[] hex = "0123456789abcdef";
    char[] outBuf = new char[hexChars];
    foreach (i; 0 .. hexChars)
        outBuf[i] = hex[uniform(0, 16)];
    return outBuf.idup;
}

/// Atomically creates a lock file with O_CREATE|O_EXCL; returns true if acquired, false if already exists.
bool tryCreateExclusiveLockFile(string lockPath, string contents) @trusted
{
    auto dir = dirName(lockPath);
    if (dir.length > 0 && dir != "." && !exists(dir))
        mkdirRecurse(dir);

    version (Posix)
    {
        import std.string : toStringz;
        import core.stdc.errno : EEXIST, errno;
        int fd = open(lockPath.toStringz, O_CREAT | O_EXCL | O_WRONLY, octal!644);
        if (fd < 0)
        {
            if (errno == EEXIST)
                return false;
            throw new Exception("open lock file " ~ lockPath ~ " failed: errno " ~ errno.to!string);
        }
        scope (exit) close(fd);
        if (contents.length > 0)
            write_(fd, contents.ptr, contents.length);
        return true;
    }
    else
    {
        if (exists(lockPath))
            return false;
        write(lockPath, contents);
        return true;
    }
}

/// Returns current process ID as string.
string currentProcessPIDString() @trusted
{
    version (Posix)
        return getpid().to!string;
    else version (Windows)
        return GetCurrentProcessId().to!string;
}

/// Octal literal helper (`octal!644` -> `0x1A4`).
template octal(uint n)
{
    enum uint octal = parseOctal(n);
}

private pure uint parseOctal(uint n) @safe @nogc nothrow
{
    uint result = 0;
    uint mult = 1;
    while (n > 0)
    {
        result += (n % 10) * mult;
        mult *= 8;
        n /= 10;
    }
    return result;
}

unittest
{
    assert(octal!644 == 420);
    assert(octal!755 == 493);
    assert(octal!600 == 384);
    assert(isPathWithin("/tmp/a", "/tmp/a/b/c"));
    assert(!isPathWithin("/tmp/a", "/tmp/b"));
}
