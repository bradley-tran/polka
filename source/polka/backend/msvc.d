module polka.backend.msvc;

import std.algorithm : any, sort, startsWith;
import std.array : appender;
import std.conv : to;
import std.file : dirEntries, exists, isDir, mkdirRecurse, read, remove, rmdirRecurse, SpanMode, write;
import std.format : format;
import std.json : JSONType, JSONValue, parseJSON;
import std.path : baseName, buildPath, dirName;
import std.process : environment;
import std.stdio : File;
import std.string : icmp, indexOf, replace, split, strip, toLower, toUpper;

import polka.backend.aliases : ConfigNotExistException, getProcessEnvValue, lookPath, preparePath = regularFileExistsAt, readFileBytesStrict, readTextStrict, removeFileIgnoreNotExist, runCommandCapture;
import polka.config : Environment, primaryPHPTool;
import polka.support.fs : cleanPath, createTempDir, currentOS, octal, rmdirRecurseQuiet, writeTextAtomic;
import polka.tools : DefaultPHPSDKVersion;
import polka.tools.ids : PHPSDK;

/// Per-environment MSVC capture subdirectory inside `.polka/envs`.
enum string msvcStateDirName = "msvc";
/// Default vcvarsall target architecture (PHP Windows SDK builds are x64-only).
enum string msvcTargetArch = "x64";
/// Required Visual Studio component providing cl.exe/link.exe/dumpbin.exe.
enum string msvcVCToolsComponent = "Microsoft.VisualStudio.Component.VC.Tools.x86.x64";
/// Sentinel separating baseline and post-vcvarsall `set` dumps.
enum string msvcCaptureMarker = "::POLKA-MSVC-CAPTURE::";

private bool isMSVCListVariable(string upperKey) @safe pure nothrow @nogc
{
    switch (upperKey)
    {
        case "PATH":
        case "INCLUDE":
        case "LIB":
        case "LIBPATH":
        case "EXTERNAL_INCLUDE":
            return true;
        default:
            return false;
    }
}

private bool isMSVCIgnoredVariable(string upperKey) @safe pure nothrow @nogc
{
    switch (upperKey)
    {
        case "PROMPT":
        case "POLKA_VCVARSALL":
        case "POLKA_VCVARS_ARCH":
        case "POLKA_VCVARS_ARGS":
            return true;
        default:
            return false;
    }
}

/// Visual Studio developer environment captured by `polka install` on Windows
/// so `polka sh` and `polka exec` can run native extension builds directly.
struct MSVCToolchain
{
    string displayName;
    string version_;
    string installationPath;
    string installationVersion;
    string vcvarsall;
    string arch;
    string[string] set;
    string[][string] prepend;
}

/// Subset of `vswhere.exe -format json` output used to select a Visual Studio instance.
struct MSVCInstance
{
    string installationPath;
    string installationVersion;
    string displayName;
    bool isPrerelease = false;
    string productDisplayVersion;
}

/// Captured baseline/post-vcvarsall environment pairs returned by `captureVCVarsHook`.
struct MSVCCaptureResult
{
    string[] baseline;
    string[] captured;
}

// Package hooks replaceable by tests to avoid depending on host Visual Studio.
package(polka.backend)
{
    MSVCInstance[] delegate(string vswhere) msvcListInstancesHook;
    MSVCCaptureResult delegate(string vcvarsall, string arch, string args) msvcCaptureVCVarsHook;
    int delegate(string phpPath) msvcDetectPHPMajorHook;
    string delegate(string phpPath) msvcDetectPHPToolsetHook;
    string[] delegate() msvcVSWhereLocationHook;
}

static this()
{
    import std.functional : toDelegate;
    msvcListInstancesHook = toDelegate(&listMSVCInstances);
    msvcCaptureVCVarsHook = toDelegate(&captureMSVCVCVars);
    msvcDetectPHPMajorHook = toDelegate(&detectPHPCompilerMajor);
    msvcDetectPHPToolsetHook = toDelegate(&detectPHPToolset);
    msvcVSWhereLocationHook = toDelegate(&defaultVSWhereLocations);
}

/// Returns the `.polka/envs/msvc/<environment>.json` state file path.
string msvcStateFilePath(string envsDir, string environmentName) @safe pure
{
    return buildPath(envsDir, msvcStateDirName, environmentName ~ ".json");
}

/// Loads a previously captured `MSVCToolchain`, or returns `null` when absent.
MSVCToolchain* readMSVCToolchain(string path) @trusted
{
    string raw;
    try
    {
        raw = readTextStrict(path);
    }
    catch (ConfigNotExistException)
    {
        return null;
    }
    catch (Exception e)
    {
        throw new Exception(format("read MSVC environment %s: %s", path, e.msg));
    }

    try
    {
        auto obj = parseJSON(raw);
        if (obj.type != JSONType.object)
            throw new Exception("expected JSON object");

        auto tc = new MSVCToolchain();
        if (auto v = "display-name" in obj) if (v.type == JSONType.string) tc.displayName = v.str;
        if (auto v = "version" in obj) if (v.type == JSONType.string) tc.version_ = v.str;
        if (auto v = "installation-path" in obj) if (v.type == JSONType.string) tc.installationPath = v.str;
        if (auto v = "installation-version" in obj) if (v.type == JSONType.string) tc.installationVersion = v.str;
        if (auto v = "vcvarsall" in obj) if (v.type == JSONType.string) tc.vcvarsall = v.str;
        if (auto v = "arch" in obj) if (v.type == JSONType.string) tc.arch = v.str;

        if (auto setNode = "set" in obj)
        {
            if (setNode.type == JSONType.object)
            {
                foreach (k, v; setNode.object)
                {
                    if (v.type == JSONType.string)
                        tc.set[k] = v.str;
                }
            }
        }
        if (auto prepNode = "prepend" in obj)
        {
            if (prepNode.type == JSONType.object)
            {
                foreach (k, arr; prepNode.object)
                {
                    if (arr.type != JSONType.array)
                        continue;
                    string[] vals;
                    foreach (item; arr.array)
                    {
                        if (item.type == JSONType.string)
                            vals ~= item.str;
                    }
                    tc.prepend[k] = vals;
                }
            }
        }
        return tc;
    }
    catch (Exception e)
    {
        throw new Exception(format("parse MSVC environment %s: %s", path, e.msg));
    }
}

/// Persists a captured `MSVCToolchain` to disk.
void writeMSVCToolchain(string path, const MSVCToolchain toolchain) @trusted
{
    auto dir = dirName(path);
    try
    {
        if (!exists(dir))
            mkdirRecurse(dir);
    }
    catch (Exception e)
    {
        throw new Exception("create MSVC environment dir: " ~ e.msg);
    }

    JSONValue obj = JSONValue.emptyObject;
    obj["display-name"] = toolchain.displayName;
    obj["version"] = toolchain.version_;
    obj["installation-path"] = toolchain.installationPath;
    obj["installation-version"] = toolchain.installationVersion;
    obj["vcvarsall"] = toolchain.vcvarsall;
    obj["arch"] = toolchain.arch;

    if (toolchain.set.length > 0)
    {
        JSONValue setObj = JSONValue.emptyObject;
        auto keys = toolchain.set.keys;
        keys.sort();
        foreach (k; keys)
            setObj[k] = toolchain.set[k];
        obj["set"] = setObj;
    }
    if (toolchain.prepend.length > 0)
    {
        JSONValue prepObj = JSONValue.emptyObject;
        auto keys = toolchain.prepend.keys;
        keys.sort();
        foreach (k; keys)
        {
            JSONValue arr = JSONValue.emptyArray;
            foreach (entry; toolchain.prepend[k])
                arr.array ~= JSONValue(entry);
            prepObj[k] = arr;
        }
        obj["prepend"] = prepObj;
    }

    try
    {
        writeTextAtomic(path, obj.toPrettyString() ~ "\n", octal!644);
    }
    catch (Exception e)
    {
        throw new Exception(format("write MSVC environment %s: %s", path, e.msg));
    }
}

/// Locates `vswhere.exe` on Windows or inside the bundled PHP SDK.
string findVSWhere(string envsDir) @trusted
{
    auto candidates = msvcVSWhereLocationHook().dup;
    candidates ~= buildPath(envsDir, PHPSDK, DefaultPHPSDKVersion, "bin", "vswhere.exe");
    foreach (candidate; candidates)
    {
        if (preparePath(candidate))
            return candidate;
    }
    throw new Exception("vswhere.exe was not found, so no Visual Studio installation could be located");
}

/// Returns the fixed Visual Studio Installer `vswhere.exe` path.
string[] defaultVSWhereLocations() @trusted
{
    auto pf = environment.get("ProgramFiles(x86)", "").strip();
    if (pf.length == 0)
        pf = `C:\Program Files (x86)`;
    return [buildPath(pf, "Microsoft Visual Studio", "Installer", "vswhere.exe")];
}

/// Queries `vswhere.exe` for Visual Studio instances with x64 C++ build tools.
MSVCInstance[] listMSVCInstances(string vswhere) @trusted
{
    string stdoutStr, stderrStr;
    int code = 0;
    try
    {
        code = runCommandCapture(
            [vswhere, "-products", "*", "-prerelease", "-requires", msvcVCToolsComponent, "-format", "json", "-utf8", "-nologo"],
            null,
            null,
            stdoutStr,
            stderrStr
        );
    }
    catch (Exception e)
    {
        throw new Exception(format("run %s: %s", vswhere, e.msg));
    }
    if (code != 0)
        throw new Exception(format("run %s: exit status %d: %s", vswhere, code, stderrStr.strip()));
    return parseMSVCInstances(stdoutStr);
}

/// Parses `vswhere.exe -format json` output into `MSVCInstance[]`.
MSVCInstance[] parseMSVCInstances(string jsonText) @trusted
{
    try
    {
        auto parsed = parseJSON(jsonText.strip());
        if (parsed.type == JSONType.null_)
            return null;
        if (parsed.type != JSONType.array)
            throw new Exception("expected JSON array");

        MSVCInstance[] instances;
        foreach (elem; parsed.array)
        {
            if (elem.type != JSONType.object)
                continue;
            MSVCInstance inst;
            if (auto v = "installationPath" in elem) if (v.type == JSONType.string) inst.installationPath = v.str;
            if (auto v = "installationVersion" in elem) if (v.type == JSONType.string) inst.installationVersion = v.str;
            if (auto v = "displayName" in elem) if (v.type == JSONType.string) inst.displayName = v.str;
            if (auto v = "isPrerelease" in elem)
                inst.isPrerelease = (v.type == JSONType.true_);
            if (auto cat = "catalog" in elem)
            {
                if (cat.type == JSONType.object)
                {
                    if (auto pdv = "productDisplayVersion" in *cat)
                        if (pdv.type == JSONType.string)
                            inst.productDisplayVersion = pdv.str;
                }
            }
            instances ~= inst;
        }
        return instances;
    }
    catch (Exception e)
    {
        throw new Exception("parse vswhere output: " ~ e.msg);
    }
}

/// Chooses the best Visual Studio instance (stable > prerelease, matching major > newest).
bool selectMSVCInstance(const(MSVCInstance)[] instances, int preferredMajor, out MSVCInstance selected) @safe pure
{
    MSVCInstance[] candidates;
    foreach (inst; instances)
    {
        if (inst.installationPath.strip().length > 0)
            candidates ~= inst;
    }
    if (candidates.length == 0)
        return false;

    // Bubble/insertion stable sort so relative order on exact ties mirrors sort.SliceStable.
    for (size_t i = 1; i < candidates.length; i++)
    {
        auto cur = candidates[i];
        size_t j = i;
        while (j > 0 && instanceBetterThan(cur, candidates[j - 1], preferredMajor))
        {
            candidates[j] = candidates[j - 1];
            j--;
        }
        candidates[j] = cur;
    }

    selected = candidates[0];
    return true;
}

private bool instanceBetterThan(const MSVCInstance left, const MSVCInstance right, int preferredMajor) @safe pure
{
    if (left.isPrerelease != right.isPrerelease)
        return !left.isPrerelease;
    bool leftMatch = preferredMajor > 0 && versionMajor(left.installationVersion) == preferredMajor;
    bool rightMatch = preferredMajor > 0 && versionMajor(right.installationVersion) == preferredMajor;
    if (leftMatch != rightMatch)
        return leftMatch;
    return compareDottedVersions(left.installationVersion, right.installationVersion) > 0;
}

/// Extracts leading numeric major version segment, returning 0 on parse failure.
int versionMajor(string ver) @safe pure
{
    auto trimmed = ver.strip();
    auto dot = trimmed.indexOf('.');
    auto part = dot >= 0 ? trimmed[0 .. dot] : trimmed;
    int val = 0;
    if (!tryParseInt(part, val))
        return 0;
    return val;
}

/// Compares dotted numeric version strings segment-by-segment.
int compareDottedVersions(string left, string right) @safe pure
{
    auto leftParts = left.strip().split('.');
    auto rightParts = right.strip().split('.');
    auto n = leftParts.length > rightParts.length ? leftParts.length : rightParts.length;
    foreach (idx; 0 .. n)
    {
        int lv = 0;
        int rv = 0;
        if (idx < leftParts.length)
            tryParseInt(leftParts[idx], lv);
        if (idx < rightParts.length)
            tryParseInt(rightParts[idx], rv);
        if (lv != rv)
            return lv > rv ? 1 : -1;
    }
    return 0;
}

private bool tryParseInt(string s, out int value) @safe pure nothrow @nogc
{
    auto trimmed = s;
    while (trimmed.length > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t'))
        trimmed = trimmed[1 .. $];
    while (trimmed.length > 0 && (trimmed[$ - 1] == ' ' || trimmed[$ - 1] == '\t'))
        trimmed = trimmed[0 .. $ - 1];
    if (trimmed.length == 0)
        return false;
    bool neg = false;
    size_t i = 0;
    if (trimmed[0] == '-')
    {
        neg = true;
        i = 1;
    }
    else if (trimmed[0] == '+')
    {
        i = 1;
    }
    if (i >= trimmed.length)
        return false;
    long acc = 0;
    foreach (char c; trimmed[i .. $])
    {
        if (c < '0' || c > '9')
            return false;
        acc = acc * 10 + (c - '0');
        if (acc > int.max)
            return false;
    }
    value = cast(int)(neg ? -acc : acc);
    return true;
}

/// Parses `Compiler => Visual C++ YYYY` / `MSVC17` / `(vs17)` / `vc15` from `php -i` text.
package(polka.backend) string parsePECLWindowsCompilerFromInfo(string infoText) @safe
{
    import std.algorithm : canFind;
    import std.regex : matchFirst, regex;

    // Matches Go's pecl.go parsePECLWindowsCompiler behavior: vs17/vs16/vc15,
    // plus parenthesized or token compiler tags (e.g. (vs17)).
    auto lower = infoText.toLower();
    if (lower.canFind("visual c++ 2022") || lower.canFind("msvc17"))
        return "vs17";
    if (lower.canFind("visual c++ 2019") || lower.canFind("msvc16"))
        return "vs16";
    if (lower.canFind("visual c++ 2017") || lower.canFind("msvc15"))
        return "vc15";

    try
    {
        auto m = infoText.matchFirst(regex(`(?im)^Compiler\s*=>.*\((v[sc][0-9]{2})\)|Compiler\s*=>.*\b(v[sc][0-9]{2})\b`));
        if (!m.empty)
        {
            if (m[1].length > 0) return m[1].toLower();
            if (m[2].length > 0) return m[2].toLower();
        }
    }
    catch (Exception) {}
    return "";
}

/// Runs `php -n -i` to determine the Visual Studio major version PHP was built with.
int detectPHPCompilerMajor(string phpPath) @trusted
{
    if (phpPath.strip().length == 0)
        return 0;
    try
    {
        auto envList = buildInternalPHPCommandEnv();
        string stdoutStr, stderrStr;
        auto code = runCommandCapture([phpPath, "-n", "-i"], null, envList, stdoutStr, stderrStr);
        if (code != 0)
            return 0;
        auto compiler = parsePECLWindowsCompilerFromInfo(stdoutStr);
        if (compiler.length < 3)
            return 0;
        int major = 0;
        if (!tryParseInt(compiler[2 .. $], major))
            return 0;
        return major;
    }
    catch (Exception)
    {
        return 0;
    }
}

private string[] buildInternalPHPCommandEnv() @trusted
{
    auto aa = environment.toAA();
    string[] env;
    foreach (k, v; aa)
        env ~= k ~ "=" ~ v;
    return env;
}

/// Pure-D PE32/PE32+ header reader extracting `MajorLinkerVersion.MinorLinkerVersion` (e.g. `"14.44"`).
string detectPHPToolset(string phpPath) @trusted
{
    auto target = phpPath.strip();
    if (target.length == 0)
        return "";

    try
    {
        if (exists(target) && isDir(target))
        {
            foreach (candidate; ["php.exe", "php8.dll"])
            {
                auto full = buildPath(target, candidate);
                if (preparePath(full))
                {
                    target = full;
                    break;
                }
            }
        }
        if (!preparePath(target))
            return "";

        auto data = cast(const(ubyte)[]) read(target);
        if (data.length < 0x40 || data[0] != 'M' || data[1] != 'Z')
            return "";

        uint peOffset = readLE32(data, 0x3C);
        // PE signature ("PE\0\0", 4 bytes) + COFF header (20 bytes) + at least 4 bytes of OptionalHeader
        if (peOffset > data.length || peOffset + 28 > data.length)
            return "";
        if (data[peOffset] != 'P' || data[peOffset + 1] != 'E' || data[peOffset + 2] != 0 || data[peOffset + 3] != 0)
            return "";

        size_t optStart = cast(size_t) peOffset + 24;
        ushort magic = readLE16(data, optStart);
        // 0x010B = PE32, 0x020B = PE32+. In both layouts, MajorLinkerVersion is byte +2, Minor is byte +3.
        if (magic != 0x010B && magic != 0x020B)
            return "";

        ubyte major = data[optStart + 2];
        ubyte minor = data[optStart + 3];
        if (major == 0 && minor == 0)
            return "";
        return format("%d.%d", major, minor);
    }
    catch (Exception)
    {
        return "";
    }
}

private ushort readLE16(const(ubyte)[] data, size_t offset) @safe pure nothrow @nogc
{
    return cast(ushort)(data[offset] | (cast(ushort) data[offset + 1] << 8));
}

private uint readLE32(const(ubyte)[] data, size_t offset) @safe pure nothrow @nogc
{
    return cast(uint)(data[offset])
        | (cast(uint) data[offset + 1] << 8)
        | (cast(uint) data[offset + 2] << 16)
        | (cast(uint) data[offset + 3] << 24);
}

/// Checks if `targetToolset` (e.g. `"14.44"`) exists under `<installationPath>/VC/Tools/MSVC`.
string selectMSVCToolset(string installationPath, string targetToolset) @trusted
{
    auto trimmed = targetToolset.strip();
    if (trimmed.length == 0 || installationPath.length == 0)
        return "";

    auto toolsDir = buildPath(installationPath, "VC", "Tools", "MSVC");
    if (!exists(toolsDir))
        return "";
    try
    {
        foreach (entry; dirEntries(toolsDir, SpanMode.shallow))
        {
            if (!entry.isDir)
                continue;
            auto name = baseName(entry.name);
            if (name == trimmed || name.startsWith(trimmed ~ "."))
                return trimmed;
        }
    }
    catch (Exception) {}
    return "";
}

/// Invokes `vcvarsall.bat` via `cmd.exe /d /u /c` and captures baseline + post-vcvarsall env vars.
MSVCCaptureResult captureMSVCVCVars(string vcvarsall, string arch, string args) @trusted
{
    auto tempDir = createTempDir("polka-msvc-");
    scope (exit) rmdirRecurseQuiet(tempDir);

    auto script = [
        "@echo off",
        "set",
        "echo " ~ msvcCaptureMarker,
        `call "%POLKA_VCVARSALL%" %POLKA_VCVARS_ARCH% %POLKA_VCVARS_ARGS% 1>&2`,
        "if errorlevel 1 exit /b 1",
        "set",
        "",
    ].joinLinesCRLF();

    auto scriptPath = buildPath(tempDir, "capture.bat");
    write(scriptPath, script);

    auto aa = environment.toAA();
    string[] envList;
    foreach (k, v; aa)
        envList ~= k ~ "=" ~ v;
    envList ~= "POLKA_VCVARSALL=" ~ vcvarsall;
    envList ~= "POLKA_VCVARS_ARCH=" ~ arch;
    envList ~= "POLKA_VCVARS_ARGS=" ~ args;
    envList ~= "VSCMD_SKIP_SENDTELEMETRY=1";

    string stdoutStr, stderrStr;
    auto code = runCommandCapture(["cmd.exe", "/d", "/u", "/c", scriptPath], null, envList, stdoutStr, stderrStr);
    if (code != 0)
    {
        auto cleanErr = stderrStr.replace("\0", "").strip();
        throw new Exception(format("run %s %s: exit status %d: %s", vcvarsall, arch, code, cleanErr));
    }

    return splitMSVCCapture(decodeUTF16LE(cast(const(ubyte)[]) stdoutStr));
}

private string joinLinesCRLF(const(string)[] lines) @safe pure
{
    auto buf = appender!string();
    foreach (i, l; lines)
    {
        if (i > 0)
            buf.put("\r\n");
        buf.put(l);
    }
    return buf.data;
}

/// Splits captured UTF-16LE-decoded `set` output around `msvcCaptureMarker`.
MSVCCaptureResult splitMSVCCapture(string output) @safe
{
    auto idx = output.indexOf(msvcCaptureMarker);
    if (idx < 0)
        throw new Exception("MSVC capture output is missing its marker");
    auto before = output[0 .. idx];
    auto after = output[idx + msvcCaptureMarker.length .. $];
    return MSVCCaptureResult(splitEnvironmentLines(before), splitEnvironmentLines(after));
}

private string[] splitEnvironmentLines(string output) @safe pure
{
    string[] entries;
    auto normalized = output.replace("\r\n", "\n");
    foreach (rawLine; normalized.split('\n'))
    {
        auto line = rawLine;
        while (line.length > 0 && line[$ - 1] == '\r')
            line = line[0 .. $ - 1];
        auto eq = line.indexOf('=');
        if (eq > 0 && line[0 .. eq].strip().length > 0)
            entries ~= line;
    }
    return entries;
}

/// Decodes little-endian UTF-16 bytes (`cmd.exe /u` output) into UTF-8, falling back to raw bytes.
string decodeUTF16LE(const(ubyte)[] data) @safe pure
{
    if (data.length == 0 || (data.length % 2) != 0)
        return (cast(const(char)[]) data).idup;

    auto buf = appender!string();
    for (size_t i = 0; i + 1 < data.length; i += 2)
    {
        ushort unit = cast(ushort)(data[i] | (cast(ushort) data[i + 1] << 8));
        if (unit >= 0xD800 && unit <= 0xDBFF && i + 3 < data.length)
        {
            ushort low = cast(ushort)(data[i + 2] | (cast(ushort) data[i + 3] << 8));
            if (low >= 0xDC00 && low <= 0xDFFF)
            {
                dchar cp = cast(dchar)((((cast(uint)(unit - 0xD800)) << 10) | cast(uint)(low - 0xDC00)) + 0x10000);
                buf.put(cp);
                i += 2;
                continue;
            }
        }
        if (unit >= 0xD800 && unit <= 0xDFFF)
            buf.put('\uFFFD');
        else
            buf.put(cast(dchar) unit);
    }
    return buf.data;
}

/// Diffs baseline and post-`vcvarsall` environment dumps into verbatim `set` vars
/// and added list-entry `prepend` prefixes (`PATH`, `INCLUDE`, `LIB`, ...).
void diffMSVCEnvironment(
    const(string)[] baseline,
    const(string)[] captured,
    out string[string] set,
    out string[][string] prepend
) @safe
{
    string[string] baseValues;
    foreach (entry; baseline)
    {
        auto eq = entry.indexOf('=');
        if (eq < 0)
            continue;
        auto key = entry[0 .. eq];
        auto value = entry[eq + 1 .. $];
        baseValues[key.toUpper()] = value;
    }

    foreach (entry; captured)
    {
        auto eq = entry.indexOf('=');
        if (eq < 0)
            continue;
        auto key = entry[0 .. eq];
        auto value = entry[eq + 1 .. $];
        auto upper = key.toUpper();
        if (isMSVCIgnoredVariable(upper) || key.startsWith("__"))
            continue;

        auto basePtr = upper in baseValues;
        if (basePtr !is null && *basePtr == value)
            continue;
        string baseValue = basePtr !is null ? *basePtr : "";

        if (isMSVCListVariable(upper))
        {
            auto added = addedListEntries(splitSemicolonList(baseValue), splitSemicolonList(value));
            if (added.length > 0)
                prepend[upper] = added;
            continue;
        }
        set[key] = value;
    }
}

private string[] splitSemicolonList(string value) @safe pure
{
    string[] entries;
    foreach (entry; value.split(';'))
    {
        auto trimmed = entry.strip();
        if (trimmed.length > 0)
            entries ~= trimmed;
    }
    return entries;
}

private string[] addedListEntries(const(string)[] baseline, const(string)[] captured) @safe
{
    bool[string] seen;
    foreach (entry; baseline)
        seen[cleanPath(entry).toLower()] = true;

    string[] added;
    foreach (entry; captured)
    {
        auto key = cleanPath(entry).toLower();
        if (key in seen)
            continue;
        seen[key] = true;
        added ~= entry;
    }
    return added;
}

unittest
{
    // TestSelectMSVCInstancePrefersStableMatchingMajor
    {
        auto instances = [
            MSVCInstance(`C:\VS\18-preview`, "18.11.1", "", true),
            MSVCInstance(`C:\VS\18`, "18.10.12217.157"),
            MSVCInstance(`C:\VS\17`, "17.14.36301.6"),
            MSVCInstance(`C:\VS\16`, "16.11.5"),
        ];

        MSVCInstance got;
        assert(selectMSVCInstance(instances, 17, got) && got.installationPath == `C:\VS\17`);
        assert(selectMSVCInstance(instances, 0, got) && got.installationPath == `C:\VS\18`);
        assert(selectMSVCInstance(instances, 15, got) && got.installationPath == `C:\VS\18`);
    }

    // TestSelectMSVCInstanceFallsBackToPrerelease
    {
        MSVCInstance got;
        assert(selectMSVCInstance([MSVCInstance(`C:\VS\Preview`, "18.0", "", true)], 17, got)
            && got.installationPath == `C:\VS\Preview`);
        assert(!selectMSVCInstance(null, 17, got));
    }

    // TestParseMSVCInstancesReadsVSWhereJSON
    {
        string jsonText = `[{"installationPath":"C:\\VS\\BuildTools","installationVersion":"17.14.1","displayName":"Visual Studio Build Tools 2022","isPrerelease":false,"catalog":{"productDisplayVersion":"17.14.2"}}]`;
        auto instances = parseMSVCInstances(jsonText);
        assert(instances.length == 1);
        assert(instances[0].installationPath == `C:\VS\BuildTools`);
        assert(instances[0].productDisplayVersion == "17.14.2");
    }

    // TestDiffMSVCEnvironmentSplitsSetAndPrepend
    {
        auto baseline = [
            `Path=C:\Windows;C:\Tools`,
            `PROMPT=$P$G`,
            `UNCHANGED=1`,
        ];
        auto captured = [
            `Path=C:\VS\bin;c:\windows;C:\Windows;C:\Tools`,
            `INCLUDE=C:\VS\include;C:\SDK\ucrt;`,
            `PROMPT=$P$G`,
            `UNCHANGED=1`,
            `VCToolsInstallDir=C:\VS\Tools\MSVC\14.44\`,
            `__VSCMD_PREINIT_PATH=C:\Windows;C:\Tools`,
            `POLKA_VCVARSALL=C:\VS\vcvarsall.bat`,
        ];
        string[string] set;
        string[][string] prepend;
        diffMSVCEnvironment(baseline, captured, set, prepend);
        assert(set == ["VCToolsInstallDir": `C:\VS\Tools\MSVC\14.44\`]);
        assert(prepend["PATH"] == [`C:\VS\bin`]);
        assert(prepend["INCLUDE"] == [`C:\VS\include`, `C:\SDK\ucrt`]);
        assert(prepend.length == 2);
    }

    // TestSplitMSVCCaptureDecodesUTF16
    {
        import std.utf : toUTF16;
        string text = "A=1\r\nNAME=Bảo\r\n" ~ msvcCaptureMarker ~ " \r\nA=1\r\nVCToolsInstallDir=C:\\VS\r\n";
        auto units = toUTF16(text);
        ubyte[] encoded;
        encoded.reserve(units.length * 2);
        foreach (u; units)
        {
            encoded ~= cast(ubyte)(u & 0xFF);
            encoded ~= cast(ubyte)((u >> 8) & 0xFF);
        }
        auto splitRes = splitMSVCCapture(decodeUTF16LE(encoded));
        assert(splitRes.baseline == ["A=1", "NAME=Bảo"]);
        assert(splitRes.captured == ["A=1", `VCToolsInstallDir=C:\VS`]);

        bool threw = false;
        try { splitMSVCCapture("A=1"); } catch (Exception) { threw = true; }
        assert(threw);
    }

    // TestSelectMSVCToolset
    {
        auto tmp = createTempDir("polka-msvc-ts-");
        scope (exit) rmdirRecurseQuiet(tmp);
        mkdirRecurse(buildPath(tmp, "VC", "Tools", "MSVC", "14.44.35207"));
        mkdirRecurse(buildPath(tmp, "VC", "Tools", "MSVC", "14.51.36231"));
        assert(selectMSVCToolset(tmp, "14.44") == "14.44");
        assert(selectMSVCToolset(tmp, "14.51.36231") == "14.51.36231");
        assert(selectMSVCToolset(tmp, "14.38") == "");
        assert(selectMSVCToolset(tmp, "") == "");
    }

    // TestDetectPHPToolset (empty, invalid, and synthetic PE32+)
    {
        assert(detectPHPToolset("") == "");
        auto tmp = createTempDir("polka-pe-test-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto bad = buildPath(tmp, "not-a-pe.exe");
        write(bad, "random data");
        assert(detectPHPToolset(bad) == "");

        // Synthetic minimal PE32+ image with e_lfanew = 0x80 and LinkerVersion = 14.44
        ubyte[] pe = new ubyte[256];
        pe[0] = 'M';
        pe[1] = 'Z';
        pe[0x3C] = 0x80; // e_lfanew = 0x80
        pe[0x80] = 'P';
        pe[0x81] = 'E';
        pe[0x82] = 0;
        pe[0x83] = 0;
        // OptionalHeader starts at 0x80 + 24 = 0x98
        pe[0x98] = 0x0B;
        pe[0x99] = 0x02; // 0x020B = PE32+
        pe[0x9A] = 14;   // MajorLinkerVersion
        pe[0x9B] = 44;   // MinorLinkerVersion
        auto peFile = buildPath(tmp, "php.exe");
        write(peFile, pe);
        assert(detectPHPToolset(peFile) == "14.44");
        assert(detectPHPToolset(tmp) == "14.44");
    }
}
