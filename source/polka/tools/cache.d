module polka.tools.cache;

import core.thread : Thread;
import core.time : dur;
import std.algorithm : startsWith;
import std.datetime : Clock, SysTime, UTC;
import std.file : copy, exists, getAttributes, getSize, isDir, isFile, mkdirRecurse, readText, remove, rename, rmdirRecurse, timeLastModified, write;
import std.format : format;
import std.json;
import std.path : baseName, buildNormalizedPath, buildPath, dirName, isAbsolute;
import std.string : icmp, replace, strip, toLower;

import polka.support.archive : collapseSingleDirectory, extractTarGz, extractTarXz, extractZip;
import polka.support.fs : cleanPath, createTempDir, currentProcessPIDString, fromSlashPath, getFileMode, isPathWithin, octal, removeQuiet, replaceFile, setFileMode, toSlashPath, tryCreateExclusiveLockFile, writeTextAtomic;
import polka.tools.checksum;
import polka.tools.types;

enum string cacheMetadataFileName = "metadata.json";
enum string cacheMetadataLockFileName = ".metadata.lock";
enum int cacheMetadataSchemaVersion = 1;
enum string payloadKindArchive = "archive";
enum string payloadKindFile = "file";

private enum long cacheLockTimeoutMs = 30_000;
private enum long cacheLockStaleAfterSeconds = 10 * 60;

/// Exception indicating an absent, incomplete, or corrupt cache entry.
class CacheMissException : Exception
{
    this(string msg, string file = __FILE__, size_t line = __LINE__) @safe pure nothrow
    {
        super("cache miss: " ~ msg, file, line);
    }
}

/// Reports whether `err` represents an absent or invalid cache entry.
bool isCacheMiss(Throwable err) @safe pure nothrow @nogc
{
    return (cast(CacheMissException) err) !is null;
}

/// Persisted metadata entry for one requested version of a cached tool.
struct ToolCacheVersion
{
    string downloadedVersion;
    string payloadKind;
    string payloadPath;
    string fileName;
    string installPath;
    string sourceURL;
    ArchiveFormat archiveFormat = ArchiveFormat.none;
    ChecksumAlgorithm checksumAlgorithm = ChecksumAlgorithm.none;
    string checksum;
    long size = 0;
    string downloadedAt;
}

/// Persisted metadata index file stored at `<cache>/<tool>/metadata.json`.
struct ToolCacheMetadata
{
    int schemaVersion = cacheMetadataSchemaVersion;
    string tool;
    ToolCacheVersion[string] versions;
}

/// Describes a validated cached payload for a requested tool version.
struct CachedPayload
{
    string tool;
    string requestedVersion;
    string downloadedVersion;
    string payloadPath;
    string payloadKind;
}

/// Exclusive advisory file lock on a directory to serialize cross-process installs/cache writes.
final class DirLock
{
    private string lockPath;
    private bool released = false;

    package this(string path) @safe pure nothrow
    {
        this.lockPath = path;
    }

    /// Removes the lock file; safe to call multiple times or on null references.
    void release() @trusted
    {
        if (released || lockPath.length == 0)
            return;
        released = true;
        removeQuiet(lockPath);
    }
}

/// Acquires an exclusive lock file inside `dir`, creating `dir` when needed.
DirLock acquireDirLock(string dir) @trusted
{
    if (!exists(dir))
        mkdirRecurse(dir);
    return acquireCacheLock(dir);
}

private DirLock acquireCacheLock(string toolDir) @trusted
{
    auto lockPath = buildPath(toolDir, cacheMetadataLockFileName);
    auto deadline = Clock.currTime() + dur!"msecs"(cacheLockTimeoutMs);
    auto pidContent = currentProcessPIDString() ~ "\n";

    while (true)
    {
        if (tryCreateExclusiveLockFile(lockPath, pidContent))
            return new DirLock(lockPath);

        removeStaleCacheLock(lockPath);
        if (Clock.currTime() > deadline)
            throw new Exception("timeout waiting for cache metadata lock " ~ lockPath);

        Thread.sleep(dur!"msecs"(100));
    }
}

private void removeStaleCacheLock(string lockPath) @trusted
{
    try
    {
        if (!exists(lockPath))
            return;
        auto modTime = timeLastModified(lockPath);
        if ((Clock.currTime() - modTime) > dur!"seconds"(cacheLockStaleAfterSeconds))
            removeQuiet(lockPath);
    }
    catch (Exception) {}
}

/// Returns the validated cached payload for a requested tool version, or throws `CacheMissException`.
CachedPayload cachedToolPayload(string cacheDir, string tool, string ver) @trusted
{
    ToolCacheVersion entry;
    return cachedToolPayloadWithEntry(cacheDir, tool, ver, entry);
}

/// Materializes a validated cached payload into `targetDir`.
CachedPayload installCachedToolPayload(string cacheDir, string targetDir, string tool, string ver) @trusted
{
    ToolCacheVersion entry;
    auto payload = cachedToolPayloadWithEntry(cacheDir, tool, ver, entry);

    switch (entry.payloadKind)
    {
        case payloadKindArchive:
            extractArchiveRaw(payload.payloadPath, targetDir, entry.archiveFormat);
            collapseSingleDirectory(targetDir);
            break;
        case payloadKindFile:
        {
            if (entry.installPath.strip().length == 0)
                throw new Exception(format("cache metadata for %s %s does not define installPath", tool, ver));
            auto targetPath = safeRelativePath(targetDir, entry.installPath);
            copyCachedPayloadFile(payload.payloadPath, targetPath);
            break;
        }
        default:
            throw new Exception(format(`cache metadata for %s %s has unsupported payload kind "%s"`, tool, ver, entry.payloadKind));
    }

    return payload;
}

/// Extracts an archive into `targetDir` without collapsing a single top-level directory.
void extractArchiveRaw(string archivePath, string targetDir, ArchiveFormat fmt) @trusted
{
    switch (fmt)
    {
        case ArchiveFormat.zip:
            extractZip(archivePath, targetDir, false);
            break;
        case ArchiveFormat.tarGz:
            extractTarGz(archivePath, targetDir, false);
            break;
        case ArchiveFormat.tarXz:
            extractTarXz(archivePath, targetDir, false);
            break;
        default:
            throw new Exception(format(`unsupported archive format "%s"`, cast(string) fmt));
    }
}

/// Caches an archive staging file and updates metadata, returning the final cache payload path.
string cacheArchivePayload(
    string cacheDir,
    string tool,
    string requestedVersion,
    string downloadedVersion,
    DownloadAsset asset,
    string stagingPath
) @trusted
{
    ToolCacheVersion entry;
    entry.downloadedVersion = downloadedVersion.strip();
    entry.payloadKind = payloadKindArchive;
    entry.fileName = asset.fileName.strip();
    entry.sourceURL = asset.url.strip();
    entry.archiveFormat = asset.archiveFormat;
    entry.checksumAlgorithm = asset.checksumAlgorithm;
    entry.checksum = asset.checksum.strip();
    entry.downloadedAt = Clock.currTime(UTC()).toISOExtString();
    return cacheDownloadedPayload(cacheDir, tool, requestedVersion, entry, stagingPath);
}

/// Caches a single-file binary staging payload and updates metadata.
string cacheFilePayload(
    string cacheDir,
    string tool,
    string requestedVersion,
    string downloadedVersion,
    string fileName,
    string sourceURL,
    string installPath,
    ChecksumAlgorithm algorithm,
    string checksum,
    string stagingPath
) @trusted
{
    ToolCacheVersion entry;
    entry.downloadedVersion = downloadedVersion.strip();
    entry.payloadKind = payloadKindFile;
    entry.fileName = fileName.strip();
    entry.installPath = toSlashPath(installPath.strip());
    entry.sourceURL = sourceURL.strip();
    entry.checksumAlgorithm = algorithm;
    entry.checksum = checksum.strip();
    entry.downloadedAt = Clock.currTime(UTC()).toISOExtString();
    return cacheDownloadedPayload(cacheDir, tool, requestedVersion, entry, stagingPath);
}

private string cacheDownloadedPayload(
    string cacheDir,
    string tool,
    string requestedVersion,
    ToolCacheVersion entry,
    string stagingPath
) @trusted
{
    tool = tool.strip().toLower();
    requestedVersion = requestedVersion.strip();
    if (tool.length == 0)
        throw new Exception("cache tool cannot be empty");
    if (requestedVersion.length == 0)
        throw new Exception(tool ~ " cache version cannot be empty");
    if (entry.downloadedVersion.strip().length == 0)
        entry.downloadedVersion = requestedVersion;
    if (entry.fileName.strip().length == 0)
        throw new Exception(tool ~ " cache payload filename cannot be empty");
    if (baseName(entry.fileName) != entry.fileName || toSlashPath(entry.fileName) != entry.fileName)
        throw new Exception(format(`%s cache payload filename "%s" must not contain directories`, tool, entry.fileName));

    if (entry.checksumAlgorithm == ChecksumAlgorithm.none || entry.checksum.strip().length == 0)
    {
        string computed;
        long sz;
        checksumFile(ChecksumAlgorithm.sha256, stagingPath, computed, sz);
        entry.checksumAlgorithm = ChecksumAlgorithm.sha256;
        entry.checksum = computed;
        entry.size = sz;
    }
    else
    {
        string normChecksum;
        size_t expectedBytes;
        validateChecksumValue(entry.checksumAlgorithm, entry.checksum, normChecksum, expectedBytes);
        verifyFileChecksum(entry.checksumAlgorithm, normChecksum, stagingPath);
        entry.checksum = normChecksum;
        entry.size = regularFileSize(stagingPath);
    }

    auto toolDir = buildPath(cacheDir, tool);
    if (!exists(toolDir))
        mkdirRecurse(toolDir);

    auto lock = acquireCacheLock(toolDir);
    scope (exit) lock.release();

    auto payloadRelativePath = toSlashPath(requestedVersion) ~ "/" ~ entry.fileName;
    auto payloadPath = safeRelativePath(toolDir, payloadRelativePath);
    auto payloadDir = dirName(payloadPath);

    if (exists(payloadDir))
        rmdirRecurse(payloadDir);
    mkdirRecurse(payloadDir);

    moveStagingFile(stagingPath, payloadPath);

    if (entry.payloadKind == payloadKindFile)
        markCachePayloadExecutable(payloadPath);

    entry.payloadPath = payloadRelativePath;
    auto metadata = readToolCacheMetadataForUpdate(toolDir, tool);
    metadata.versions[requestedVersion] = entry;
    writeToolCacheMetadata(toolDir, metadata);

    return payloadPath;
}

private void moveStagingFile(string srcPath, string dstPath) @trusted
{
    try
    {
        rename(srcPath, dstPath);
        return;
    }
    catch (Exception) {}

    copy(srcPath, dstPath);
    removeQuiet(srcPath);
}

private void markCachePayloadExecutable(string path) @trusted
{
    version (Posix)
    {
        auto mode = getFileMode(path);
        setFileMode(path, mode | octal!111);
    }
}

private long regularFileSize(string path) @trusted
{
    if (!exists(path))
        throw new Exception("stat " ~ path ~ ": file does not exist");
    if (isDir(path))
        throw new Exception(path ~ " is a directory");
    return cast(long) getSize(path);
}

private CachedPayload cachedToolPayloadWithEntry(
    string cacheDir,
    string tool,
    string ver,
    out ToolCacheVersion entry
) @trusted
{
    tool = tool.strip().toLower();
    ver = ver.strip();
    auto toolDir = buildPath(cacheDir, tool);

    auto metadata = readToolCacheMetadata(toolDir, tool);
    auto entryPtr = ver in metadata.versions;
    if (entryPtr is null)
        throw new CacheMissException(format("%s %s has no metadata entry", tool, ver));
    entry = *entryPtr;

    string payloadPath;
    try
    {
        payloadPath = safeRelativePath(toolDir, entry.payloadPath);
    }
    catch (Exception e)
    {
        throw new CacheMissException(format("%s %s has invalid payload path: %s", tool, ver, e.msg));
    }

    validateCachedPayloadFile(payloadPath, entry);

    CachedPayload result;
    result.tool = tool;
    result.requestedVersion = ver;
    result.downloadedVersion = entry.downloadedVersion;
    result.payloadPath = payloadPath;
    result.payloadKind = entry.payloadKind;
    return result;
}

/// Reads and validates `<toolDir>/metadata.json`. Throws `CacheMissException` on missing/corrupt/unsupported data.
ToolCacheMetadata readToolCacheMetadata(string toolDir, string tool) @trusted
{
    auto path = buildPath(toolDir, cacheMetadataFileName);
    if (!exists(path))
        throw new CacheMissException(tool ~ " metadata is missing");

    string raw;
    try
    {
        raw = readText(path);
    }
    catch (Exception e)
    {
        throw new Exception(format("read %s cache metadata: %s", tool, e.msg));
    }

    JSONValue root;
    try
    {
        root = parseJSON(raw);
    }
    catch (Exception e)
    {
        throw new CacheMissException(format("parse %s cache metadata: %s", tool, e.msg));
    }

    if (root.type != JSONType.object)
        throw new CacheMissException(format("parse %s cache metadata: expected object", tool));

    ToolCacheMetadata metadata;
    metadata.schemaVersion = cast(int) jsonLongField(root, "schemaVersion", 0);
    metadata.tool = jsonStringField(root, "tool");

    if (metadata.schemaVersion != cacheMetadataSchemaVersion)
        throw new CacheMissException(format("%s cache metadata schema version %d is unsupported", tool, metadata.schemaVersion));
    if (icmp(metadata.tool.strip(), tool.strip()) != 0)
        throw new CacheMissException(format(`%s cache metadata is for tool "%s"`, tool, metadata.tool));

    if (auto versPtr = "versions" in root.object)
    {
        if (versPtr.type == JSONType.object)
        {
            foreach (k, v; versPtr.object)
            {
                if (v.type != JSONType.object)
                    continue;
                ToolCacheVersion item;
                item.downloadedVersion = jsonStringField(v, "downloadedVersion");
                item.payloadKind = jsonStringField(v, "payloadKind");
                item.payloadPath = jsonStringField(v, "payloadPath");
                item.fileName = jsonStringField(v, "fileName");
                item.installPath = jsonStringField(v, "installPath");
                item.sourceURL = jsonStringField(v, "sourceUrl");

                ArchiveFormat af;
                parseArchiveFormat(jsonStringField(v, "archiveFormat"), af);
                item.archiveFormat = af;

                ChecksumAlgorithm alg;
                parseChecksumAlgorithm(jsonStringField(v, "checksumAlgorithm"), alg);
                item.checksumAlgorithm = alg;

                item.checksum = jsonStringField(v, "checksum");
                item.size = jsonLongField(v, "size", 0);
                item.downloadedAt = jsonStringField(v, "downloadedAt");
                metadata.versions[k] = item;
            }
        }
    }

    return metadata;
}

private ToolCacheMetadata readToolCacheMetadataForUpdate(string toolDir, string tool) @trusted
{
    try
    {
        return readToolCacheMetadata(toolDir, tool);
    }
    catch (Exception)
    {
        ToolCacheMetadata emptyMeta;
        emptyMeta.schemaVersion = cacheMetadataSchemaVersion;
        emptyMeta.tool = tool.strip().toLower();
        return emptyMeta;
    }
}

private void writeToolCacheMetadata(string toolDir, const ToolCacheMetadata metadata) @trusted
{
    if (!exists(toolDir))
        mkdirRecurse(toolDir);

    JSONValue root = JSONValue(cast(JSONValue[string]) null);
    root["schemaVersion"] = metadata.schemaVersion;
    root["tool"] = metadata.tool;

    JSONValue versionsObj = JSONValue(cast(JSONValue[string]) null);
    foreach (k, v; metadata.versions)
    {
        JSONValue entryObj = JSONValue(cast(JSONValue[string]) null);
        entryObj["downloadedVersion"] = v.downloadedVersion;
        entryObj["payloadKind"] = v.payloadKind;
        entryObj["payloadPath"] = v.payloadPath;
        entryObj["fileName"] = v.fileName;
        if (v.installPath.length > 0)
            entryObj["installPath"] = v.installPath;
        entryObj["sourceUrl"] = v.sourceURL;
        if (v.archiveFormat != ArchiveFormat.none)
            entryObj["archiveFormat"] = cast(string) v.archiveFormat;
        entryObj["checksumAlgorithm"] = cast(string) v.checksumAlgorithm;
        entryObj["checksum"] = v.checksum;
        entryObj["size"] = v.size;
        entryObj["downloadedAt"] = v.downloadedAt;
        versionsObj[k] = entryObj;
    }
    root["versions"] = versionsObj;

    auto jsonText = root.toPrettyString() ~ "\n";
    auto targetPath = buildPath(toolDir, cacheMetadataFileName);
    writeTextAtomic(targetPath, jsonText, octal!644);
}

private void validateCachedPayloadFile(string payloadPath, const ToolCacheVersion entry) @trusted
{
    if (!exists(payloadPath))
        throw new CacheMissException("cached payload " ~ payloadPath ~ " is missing");
    if (isDir(payloadPath))
        throw new CacheMissException("cached payload " ~ payloadPath ~ " is a directory");

    auto actualSize = cast(long) getSize(payloadPath);
    if (entry.size > 0 && actualSize != entry.size)
        throw new CacheMissException("cached payload " ~ payloadPath ~ " size mismatch");

    if (entry.checksumAlgorithm == ChecksumAlgorithm.none || entry.checksum.strip().length == 0)
        throw new CacheMissException("cached payload " ~ payloadPath ~ " has no checksum");

    try
    {
        verifyFileChecksum(entry.checksumAlgorithm, entry.checksum, payloadPath);
    }
    catch (Exception e)
    {
        throw new CacheMissException("cached payload " ~ payloadPath ~ " failed checksum validation: " ~ e.msg);
    }
}

private void copyCachedPayloadFile(string sourcePath, string targetPath) @trusted
{
    if (!exists(sourcePath))
        throw new Exception("stat cached payload " ~ sourcePath ~ ": missing");
    auto mode = getFileMode(sourcePath);

    auto parent = dirName(targetPath);
    if (parent.length > 0 && !exists(parent))
        mkdirRecurse(parent);

    copy(sourcePath, targetPath);
    setFileMode(targetPath, mode);
}

/// Validates and joins `relativePath` inside `root`, rejecting escapes or absolute paths.
string safeRelativePath(string root, string relativePath) @safe
{
    auto trimmed = relativePath.strip();
    if (trimmed.length == 0)
        throw new Exception("relative path cannot be empty");
    if (isAbsolute(trimmed) || trimmed.startsWith("/") || trimmed.startsWith(`\`))
        throw new Exception(format(`relative path "%s" must not be absolute`, relativePath));

    auto cleanRoot = cleanPath(root);
    auto cleanJoined = cleanPath(buildPath(cleanRoot, fromSlashPath(trimmed)));
    if (!isPathWithin(cleanRoot, cleanJoined))
        throw new Exception(format(`relative path "%s" escapes root`, relativePath));
    return cleanJoined;
}

package(polka) string jsonStringField(const JSONValue obj, string key) @trusted
{
    if (obj.type != JSONType.object)
        return "";
    if (auto p = key in obj.object)
    {
        if (p.type == JSONType.string)
            return p.str;
    }
    return "";
}

package(polka) bool jsonBoolField(const JSONValue obj, string key, bool defaultVal = false) @trusted
{
    if (obj.type != JSONType.object)
        return defaultVal;
    if (auto p = key in obj.object)
    {
        if (p.type == JSONType.true_)
            return true;
        if (p.type == JSONType.false_)
            return false;
    }
    return defaultVal;
}

package(polka) long jsonLongField(const JSONValue obj, string key, long defaultVal = 0) @trusted
{
    if (obj.type != JSONType.object)
        return defaultVal;
    if (auto p = key in obj.object)
    {
        if (p.type == JSONType.integer)
            return p.integer;
        if (p.type == JSONType.uinteger)
            return cast(long) p.uinteger;
    }
    return defaultVal;
}
