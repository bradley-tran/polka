module polka.support.archive;

import core.stdc.stdint;
import std.algorithm : any, endsWith, startsWith;
import std.array : appender;
import std.conv : to;
import std.file : dirEntries, exists, isDir, isFile, mkdirRecurse, read, remove, rename, rmdirRecurse, SpanMode, write;
import std.path : absolutePath, baseName, buildNormalizedPath, buildPath, dirName, relativePath;
import std.string : indexOf, strip, stripRight, toLower;
import std.zip : ArchiveMember, ZipArchive;
import std.zlib : Compress, HeaderFormat, UnCompress, ZlibException;

import polka.support.fs : cleanPath, createTempDir, fromSlashPath, isPathWithin, octal, removeQuiet, replaceFile, setFileMode, toSlashPath;

// ============================================================================
// liblzma C bindings (from /usr/include/lzma/base.h and container.h)
// ============================================================================
private extern (C) nothrow @nogc
{
    enum int LZMA_OK = 0;
    enum int LZMA_STREAM_END = 1;
    enum int LZMA_RUN = 0;
    enum int LZMA_FINISH = 3;
    enum uint LZMA_CONCATENATED = 0x08;
    enum uint LZMA_PRESET_DEFAULT = 6;
    enum int LZMA_CHECK_CRC64 = 4;

    struct lzma_stream
    {
        const(ubyte)* next_in;
        size_t avail_in;
        ulong total_in;

        ubyte* next_out;
        size_t avail_out;
        ulong total_out;

        const(void)* allocator;
        void* internal;

        void* reserved_ptr1;
        void* reserved_ptr2;
        void* reserved_ptr3;
        void* reserved_ptr4;

        ulong seek_pos;
        ulong reserved_int2;
        size_t reserved_int3;
        size_t reserved_int4;
        int reserved_enum1;
        int reserved_enum2;
    }

    int lzma_stream_decoder(lzma_stream* strm, ulong memlimit, uint flags);
    int lzma_easy_encoder(lzma_stream* strm, uint preset, int check);
    int lzma_code(lzma_stream* strm, int action);
    void lzma_end(lzma_stream* strm);
}

// ============================================================================
// Compression / Decompression (Gzip & XZ)
// ============================================================================

ubyte[] compressGzip(const(ubyte)[] data) @trusted
{
    auto comp = new Compress(HeaderFormat.gzip);
    auto out1 = cast(ubyte[]) comp.compress(data);
    auto out2 = cast(ubyte[]) comp.flush();
    return out1 ~ out2;
}

ubyte[] decompressGzip(const(ubyte)[] data) @trusted
{
    auto uncomp = new UnCompress(HeaderFormat.gzip);
    auto out1 = cast(ubyte[]) uncomp.uncompress(data);
    auto out2 = cast(ubyte[]) uncomp.flush();
    return out1 ~ out2;
}

ubyte[] decompressXZ(const(ubyte)[] data) @trusted
{
    lzma_stream strm = lzma_stream.init;
    auto rc = lzma_stream_decoder(&strm, ulong.max, LZMA_CONCATENATED);
    if (rc != LZMA_OK)
        throw new Exception("lzma_stream_decoder failed with code " ~ rc.to!string);
    scope (exit) lzma_end(&strm);

    strm.next_in = data.length > 0 ? data.ptr : null;
    strm.avail_in = data.length;

    auto result = appender!(ubyte[])();
    ubyte[65536] outBuf;

    int action = LZMA_RUN;
    while (true)
    {
        if (strm.avail_in == 0)
            action = LZMA_FINISH;

        strm.next_out = outBuf.ptr;
        strm.avail_out = outBuf.length;

        rc = lzma_code(&strm, action);
        size_t produced = outBuf.length - strm.avail_out;
        if (produced > 0)
            result.put(outBuf[0 .. produced]);

        if (rc == LZMA_STREAM_END)
            break;
        if (rc != LZMA_OK)
            throw new Exception("xz decompression failed with code " ~ rc.to!string);
    }

    return result.data;
}

/// Compresses `data` into a `.xz` stream using liblzma.
ubyte[] compressXZ(const(ubyte)[] data, uint preset = 1) @trusted
{
    lzma_stream strm = lzma_stream.init;
    auto rc = lzma_easy_encoder(&strm, preset, LZMA_CHECK_CRC64);
    if (rc != LZMA_OK)
        throw new Exception("lzma_easy_encoder failed with code " ~ rc.to!string);
    scope (exit) lzma_end(&strm);

    strm.next_in = data.length > 0 ? data.ptr : null;
    strm.avail_in = data.length;

    auto result = appender!(ubyte[])();
    ubyte[65536] outBuf;

    int action = LZMA_RUN;
    while (true)
    {
        if (strm.avail_in == 0)
            action = LZMA_FINISH;

        strm.next_out = outBuf.ptr;
        strm.avail_out = outBuf.length;

        rc = lzma_code(&strm, action);
        size_t produced = outBuf.length - strm.avail_out;
        if (produced > 0)
            result.put(outBuf[0 .. produced]);

        if (rc == LZMA_STREAM_END)
            break;
        if (rc != LZMA_OK)
            throw new Exception("xz compression failed with code " ~ rc.to!string);
    }

    return result.data;
}

/// Single entry descriptor for archive creation helpers.
struct ArchiveEntrySpec
{
    string name;
    const(ubyte)[] content;
    uint mode = octal!755;
    bool isDir = false;
}

/// Builds a USTAR tar archive byte buffer from `entries`.
ubyte[] buildTarBytes(const(ArchiveEntrySpec)[] entries) @trusted
{
    import std.format : format;
    auto buf = appender!(ubyte[])();
    foreach (e; entries)
    {
        ubyte[512] hdr = 0;
        string name = e.name;
        string prefix = "";
        if (name.length > 100)
        {
            auto slash = name.indexOf('/');
            if (slash > 0 && slash < 155 && name.length - slash - 1 <= 100)
            {
                prefix = name[0 .. slash];
                name = name[slash + 1 .. $];
            }
        }
        auto nBytes = cast(const(ubyte)[]) name;
        auto copyLen = nBytes.length > 100 ? 100 : nBytes.length;
        hdr[0 .. copyLen] = nBytes[0 .. copyLen];

        auto modeStr = format("%07o\0", e.isDir ? octal!755 : e.mode);
        hdr[100 .. 108] = cast(const(ubyte)[]) modeStr;
        hdr[108 .. 116] = cast(const(ubyte)[]) "0000000\0";
        hdr[116 .. 124] = cast(const(ubyte)[]) "0000000\0";
        ulong sz = e.isDir ? 0 : e.content.length;
        auto sizeStr = format("%011o\0", sz);
        hdr[124 .. 136] = cast(const(ubyte)[]) sizeStr;
        hdr[136 .. 148] = cast(const(ubyte)[]) "00000000000\0";
        hdr[148 .. 156] = cast(ubyte) ' ';
        hdr[156] = cast(ubyte)(e.isDir ? '5' : '0');
        hdr[257 .. 263] = cast(const(ubyte)[]) "ustar\0";
        hdr[263 .. 265] = cast(const(ubyte)[]) "00";
        if (prefix.length > 0)
        {
            auto pBytes = cast(const(ubyte)[]) prefix;
            auto pLen = pBytes.length > 155 ? 155 : pBytes.length;
            hdr[345 .. 345 + pLen] = pBytes[0 .. pLen];
        }

        ulong sum = 0;
        foreach (b; hdr)
            sum += b;
        auto sumStr = format("%06o\0 ", sum);
        hdr[148 .. 156] = cast(const(ubyte)[]) sumStr;

        buf.put(hdr[]);
        if (!e.isDir && e.content.length > 0)
        {
            buf.put(e.content);
            size_t rem = e.content.length % 512;
            if (rem != 0)
            {
                ubyte[512] pad = 0;
                buf.put(pad[0 .. 512 - rem]);
            }
        }
    }
    ubyte[1024] trailer = 0;
    buf.put(trailer[]);
    return buf.data;
}

/// Builds a ZIP archive byte buffer with optional EOCD comment.
ubyte[] buildZipBytes(const(ArchiveEntrySpec)[] entries, string comment = "") @trusted
{
    auto za = new ZipArchive();
    if (comment.length > 0)
        za.comment = comment;
    foreach (e; entries)
    {
        auto m = new ArchiveMember();
        m.name = toSlashPath(e.name);
        if (e.isDir)
        {
            if (!m.name.endsWith("/"))
                m.name ~= "/";
            m.expandedData = [];
            m.fileAttributes = octal!40755;
        }
        else
        {
            m.expandedData = e.content.dup;
            m.fileAttributes = octal!100000 | (e.mode & octal!777);
        }
        za.addMember(m);
    }
    return cast(ubyte[]) za.build();
}

/// Builds a .tar.gz byte buffer from `entries`.
ubyte[] buildTarGzBytes(const(ArchiveEntrySpec)[] entries) @trusted
{
    return compressGzip(buildTarBytes(entries));
}

/// Builds a .tar.xz byte buffer from `entries`.
ubyte[] buildTarXZBytes(const(ArchiveEntrySpec)[] entries) @trusted
{
    return compressXZ(buildTarBytes(entries));
}

// ============================================================================
// Zip Extraction & Comment Reading
// ============================================================================

/// Returns the ZIP archive EOCD comment string.
string readZipComment(const(ubyte)[] zipData) @trusted
{
    auto za = new ZipArchive(cast(void[]) zipData);
    return za.comment;
}

string readZipFileComment(string path) @trusted
{
    auto raw = cast(ubyte[]) read(path);
    return readZipComment(raw);
}

/// Computes a safe contained destination path inside `targetDir`, throwing if `entryName` escapes.
string archiveDestinationPath(string targetDir, string entryName) @safe
{
    auto cleanTarget = cleanPath(absolutePath(targetDir));
    auto rel = entryName.fromSlashPath();
    if (rel.length == 0)
        return cleanTarget;

    // Strip leading slashes/drive
    auto slash = toSlashPath(entryName);
    if (slash.startsWith("/") || (slash.length >= 2 && slash[1] == ':'))
        throw new Exception("illegal archive entry path: " ~ entryName);

    auto dest = cleanPath(buildPath(cleanTarget, rel));
    if (!isPathWithin(cleanTarget, dest))
        throw new Exception("illegal archive entry path: " ~ entryName);
    return dest;
}

/// Extracts a ZIP archive into `targetDir` and collapses a single top-level directory if present.
void extractZip(string archivePath, string targetDir, bool collapseSingleTopDir = true) @trusted
{
    auto raw = cast(ubyte[]) read(archivePath);
    extractZipBytes(raw, targetDir, collapseSingleTopDir);
}

void extractZipBytes(const(ubyte)[] zipData, string targetDir, bool collapseSingleTopDir = true) @trusted
{
    auto cleanTarget = cleanPath(absolutePath(targetDir));
    mkdirRecurse(cleanTarget);

    auto za = new ZipArchive(cast(void[]) zipData);
    foreach (name, member; za.directory)
    {
        auto dest = archiveDestinationPath(cleanTarget, name);
        if (dest == cleanTarget)
            continue;

        bool isDirEntry = name.endsWith("/") || name.endsWith("\\");
        uint mode = member.fileAttributes & 0xFFFF;
        // Check POSIX symlink (0120000 = 0xA000)
        bool isSymlink = (mode & 0xF000) == 0xA000;
        if (!isDirEntry && (mode & 0xF000) == 0x4000)
            isDirEntry = true;

        if (isDirEntry)
        {
            mkdirRecurse(dest);
            continue;
        }

        auto content = za.expand(member);
        auto parent = dirName(dest);
        if (!exists(parent))
            mkdirRecurse(parent);

        if (isSymlink)
        {
            string linkTarget = cast(string) content;
            createContainedSymlink(cleanTarget, dest, linkTarget);
            continue;
        }

        write(dest, content);
        uint perm = mode & 0x1FF;
        if (perm != 0)
            setFileMode(dest, perm);
    }

    if (collapseSingleTopDir)
        collapseSingleDirectory(cleanTarget);
}

// ============================================================================
// POSIX Tar / Pax / GNU Stream Extraction
// ============================================================================

void extractTarGz(string archivePath, string targetDir, bool collapseSingleTopDir = true) @trusted
{
    auto gzData = cast(ubyte[]) read(archivePath);
    auto tarData = decompressGzip(gzData);
    extractTarBytes(tarData, targetDir, collapseSingleTopDir);
}

void extractTarXz(string archivePath, string targetDir, bool collapseSingleTopDir = true) @trusted
{
    auto xzData = cast(ubyte[]) read(archivePath);
    auto tarData = decompressXZ(xzData);
    extractTarBytes(tarData, targetDir, collapseSingleTopDir);
}

void extractTarBytes(const(ubyte)[] tarData, string targetDir, bool collapseSingleTopDir = true) @trusted
{
    auto cleanTarget = cleanPath(absolutePath(targetDir));
    mkdirRecurse(cleanTarget);

    string nextPaxPath;
    string nextPaxLink;
    long nextPaxSize = -1;
    string nextGnuLongName;
    string nextGnuLongLink;

    size_t offset = 0;
    while (offset + 512 <= tarData.length)
    {
        auto hdr = tarData[offset .. offset + 512];
        offset += 512;

        // End of archive: 512 zero bytes
        bool allZero = true;
        foreach (b; hdr)
        {
            if (b != 0)
            {
                allZero = false;
                break;
            }
        }
        if (allZero)
            break;

        // Verify header checksum
        long recordedSum = parseTarOctal(hdr[148 .. 156]);
        long unsignedSum = 0;
        foreach (i; 0 .. 512)
        {
            ubyte b = (i >= 148 && i < 156) ? cast(ubyte) ' ' : hdr[i];
            unsignedSum += b;
        }
        if (recordedSum != unsignedSum)
            throw new Exception("invalid tar header checksum");

        string name = extractTarCString(hdr[0 .. 100]);
        string prefix = extractTarCString(hdr[345 .. 500]);
        if (prefix.length > 0)
            name = prefix ~ "/" ~ name;

        uint mode = cast(uint) parseTarOctal(hdr[100 .. 108]);
        ulong size = cast(ulong) parseTarOctal(hdr[124 .. 136]);
        char typeFlag = cast(char) hdr[156];
        string linkName = extractTarCString(hdr[157 .. 257]);

        if (nextPaxSize >= 0)
        {
            size = cast(ulong) nextPaxSize;
            nextPaxSize = -1;
        }

        size_t paddedSize = cast(size_t)((size + 511) / 512) * 512;
        if (offset + paddedSize > tarData.length)
            throw new Exception("truncated tar entry payload for " ~ name);
        auto payload = tarData[offset .. offset + cast(size_t) size];
        offset += paddedSize;

        // Handle GNU/PAX metadata records
        if (typeFlag == 'L')
        {
            nextGnuLongName = stripTrailingNul(cast(string) payload);
            continue;
        }
        if (typeFlag == 'K')
        {
            nextGnuLongLink = stripTrailingNul(cast(string) payload);
            continue;
        }
        if (typeFlag == 'x')
        {
            parsePaxRecords(cast(string) payload, nextPaxPath, nextPaxLink, nextPaxSize);
            continue;
        }
        if (typeFlag == 'g')
        {
            // Global extended header - skip
            continue;
        }

        if (nextGnuLongName.length > 0)
        {
            name = nextGnuLongName;
            nextGnuLongName = null;
        }
        if (nextGnuLongLink.length > 0)
        {
            linkName = nextGnuLongLink;
            nextGnuLongLink = null;
        }
        if (nextPaxPath.length > 0)
        {
            name = nextPaxPath;
            nextPaxPath = null;
        }
        if (nextPaxLink.length > 0)
        {
            linkName = nextPaxLink;
            nextPaxLink = null;
        }

        auto dest = archiveDestinationPath(cleanTarget, name);
        if (dest == cleanTarget)
            continue;

        switch (typeFlag)
        {
            case '5': // Directory
                mkdirRecurse(dest);
                break;
            case '0':
            case '\0': // Regular file
            {
                auto parent = dirName(dest);
                if (!exists(parent))
                    mkdirRecurse(parent);
                write(dest, payload);
                uint perm = mode & 0x1FF;
                if (perm != 0)
                    setFileMode(dest, perm);
                break;
            }
            case '2': // Symlink
            {
                auto parent = dirName(dest);
                if (!exists(parent))
                    mkdirRecurse(parent);
                createContainedSymlink(cleanTarget, dest, linkName);
                break;
            }
            case '1': // Hard link
            {
                auto linkTarget = archiveDestinationPath(cleanTarget, linkName);
                auto parent = dirName(dest);
                if (!exists(parent))
                    mkdirRecurse(parent);
                createHardLinkOrCopy(linkTarget, dest);
                break;
            }
            default:
                throw new Exception("unsupported tar entry type '" ~ typeFlag ~ "' for " ~ name);
        }
    }

    if (collapseSingleTopDir)
        collapseSingleDirectory(cleanTarget);
}

private void createContainedSymlink(string cleanTarget, string dest, string linkTarget) @trusted
{
    auto resolved = cleanPath(buildPath(dirName(dest), linkTarget.fromSlashPath()));
    if (!isPathWithin(cleanTarget, resolved))
        throw new Exception("illegal archive symlink target: " ~ linkTarget);

    version (Posix)
    {
        import std.file : symlink;
        removeQuiet(dest);
        symlink(linkTarget, dest);
    }
    else
    {
        // On Windows fallback if symlink cannot be created
        if (exists(resolved) && isFile(resolved))
            write(dest, read(resolved));
    }
}

private void createHardLinkOrCopy(string target, string dest) @trusted
{
    version (Posix)
    {
        import core.sys.posix.unistd : link;
        import std.string : toStringz;
        removeQuiet(dest);
        if (link(target.toStringz, dest.toStringz) == 0)
            return;
    }
    if (exists(target) && isFile(target))
        write(dest, read(target));
    else
        throw new Exception("tar hard link target missing: " ~ target);
}

private string extractTarCString(const(ubyte)[] field) @safe pure
{
    foreach (i, b; field)
    {
        if (b == 0)
            return (cast(const(char)[]) field[0 .. i]).idup;
    }
    return (cast(const(char)[]) field).idup;
}

private string stripTrailingNul(string s) @safe pure
{
    while (s.length > 0 && s[$ - 1] == '\0')
        s = s[0 .. $ - 1];
    return s;
}

private long parseTarOctal(const(ubyte)[] field) @safe pure
{
    // Check GNU base-256 extension
    if (field.length > 0 && (field[0] & 0x80) != 0)
    {
        long val = 0;
        foreach (i, b; field)
        {
            if (i == 0)
                val = b & 0x7F;
            else
                val = (val << 8) | b;
        }
        return val;
    }

    long result = 0;
    bool seenDigit = false;
    foreach (b; field)
    {
        if (b == 0 || b == ' ')
        {
            if (seenDigit)
                break;
            continue;
        }
        if (b < '0' || b > '7')
            throw new Exception("invalid octal value in tar header");
        seenDigit = true;
        result = (result << 3) | (b - '0');
    }
    return result;
}

private void parsePaxRecords(string payload, ref string paxPath, ref string paxLink, ref long paxSize) @safe
{
    size_t i = 0;
    while (i < payload.length)
    {
        auto sp = payload.indexOf(' ', i);
        if (sp < 0)
            break;
        size_t recLen = to!size_t(payload[i .. sp]);
        if (recLen == 0 || i + recLen > payload.length)
            break;
        auto record = payload[sp + 1 .. i + recLen];
        if (record.length > 0 && record[$ - 1] == '\n')
            record = record[0 .. $ - 1];
        auto eq = record.indexOf('=');
        if (eq > 0)
        {
            auto key = record[0 .. eq];
            auto val = record[eq + 1 .. $];
            if (key == "path")
                paxPath = val;
            else if (key == "linkpath")
                paxLink = val;
            else if (key == "size")
                paxSize = to!long(val);
        }
        i += recLen;
    }
}

/// If `targetDir` contains a single entry which is a directory, lifts its children up into `targetDir`.
void collapseSingleDirectory(string targetDir) @trusted
{
    string[] entries;
    foreach (entry; dirEntries(targetDir, SpanMode.shallow))
        entries ~= entry.name;

    if (entries.length != 1 || !isDir(entries[0]))
        return;

    auto nested = entries[0];
    // Move to a temporary sibling inside targetDir if a child shares the same name as `nested`!
    string tmpNested = buildPath(targetDir, ".polka-collapse-" ~ baseName(nested));
    while (exists(tmpNested))
        tmpNested ~= "_";
    rename(nested, tmpNested);

    foreach (child; dirEntries(tmpNested, SpanMode.shallow))
    {
        auto dst = buildPath(targetDir, baseName(child.name));
        rename(child.name, dst);
    }
    rmdirRecurse(tmpNested);
}

unittest
{
    // Gzip roundtrip
    auto original = cast(const(ubyte)[]) "hello polka gzip compression test";
    auto gz = compressGzip(original);
    assert(decompressGzip(gz) == original);

    // Zip comment + extraction + single-dir collapse
    auto tmpDir = createTempDir("polka-arch-test-");
    scope (exit) rmdirRecurse(tmpDir);

    auto za = new ZipArchive();
    za.comment = "0123456789abcdef0123456789abcdef01234567";
    auto m = new ArchiveMember();
    m.name = "pkg-1.0/bin/tool";
    m.expandedData = cast(ubyte[]) "#!/bin/sh\necho hi\n";
    m.fileAttributes = octal!100755;
    za.addMember(m);
    auto zipBytes = cast(ubyte[]) za.build();

    assert(readZipComment(zipBytes) == "0123456789abcdef0123456789abcdef01234567");
    extractZipBytes(zipBytes, tmpDir);
    assert(exists(buildPath(tmpDir, "bin", "tool")));

    // XZ + tar.xz + tar.gz roundtrip
    assert(decompressXZ(compressXZ(original)) == original);
    auto tgzDir = createTempDir("polka-tgz-test-");
    scope (exit) rmdirRecurse(tgzDir);
    extractTarBytes(decompressGzip(buildTarGzBytes([ArchiveEntrySpec("pkg-1.0/bin/app", original)])), tgzDir, true);
    assert(cast(ubyte[]) read(buildPath(tgzDir, "bin", "app")) == original);

    auto txzDir = createTempDir("polka-txz-test-");
    scope (exit) rmdirRecurse(txzDir);
    extractTarBytes(decompressXZ(buildTarXZBytes([ArchiveEntrySpec("pkg-1.0/bin/xzapp", original)])), txzDir, true);
    assert(cast(ubyte[]) read(buildPath(txzDir, "bin", "xzapp")) == original);
}
