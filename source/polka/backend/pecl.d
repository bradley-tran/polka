module polka.backend.pecl;

import std.algorithm : canFind, sort, startsWith;
import std.array : appender, join;
import std.conv : to;
import std.exception : collectException;
import std.file : copy, dirEntries, exists, isDir, mkdirRecurse, read, remove, SpanMode, write;
import std.format : format;
import std.json : JSONType, JSONValue, parseJSON;
import std.path : absolutePath, baseName, buildPath, dirName, extension;
import std.regex : matchAll, regex;
import std.string : endsWith, icmp, indexOf, lastIndexOf, replace, split, strip, toLower, toUpper;
import std.zip : ZipArchive;

import polka.backend.aliases;
import polka.backend.pie : prepareInternalPHPCommandArgs;
import polka.backend.schema : quoteGoString, validateEnvironmentFileSchema;
import polka.config :
    Environment,
    normalizePHPExtensions,
    validatePECLExtensionPackage,
    validatePECLExtensionVersion;
import polka.support.archive : decompressGzip;
import polka.support.crypto : fileDigestHex;
import polka.support.fs :
    cleanPath,
    createTempDir,
    currentArch,
    currentOS,
    fromSlashPath,
    isPathWithin,
    octal,
    pathsEqual,
    removeQuiet,
    replaceFile,
    rmdirRecurseQuiet,
    setFileMode,
    toSlashPath,
    writeTextAtomic;
import polka.support.http : CurlHTTPClient, HTTPClient, HTTPResponse;
import polka.support.xml : parseXML, XmlElement;
import polka.tools : installedPHPModules, pieExtensionModuleName;

/// Package-internal overridable endpoints matching Go's `pecl.go` variables for deterministic tests.
package(polka.backend)
{
    string peclRESTBaseURL = "https://pecl.php.net/rest";
    string peclWindowsBaseURL = "https://downloads.php.net/~windows/pecl/releases";
}

/// Filename inside `.polka/envs/<php-tool>/<version>/` recording tracked legacy PECL artifacts.
enum string peclInstallStateFileName = "pecl-installed.json";

/// Describes a resolved and installed legacy PECL package.
struct PECLExtensionResult
{
    string packageName;
    string version_;
    string module_;
    bool zend = false;
}

package(polka.backend) struct peclPHPInfo
{
    string version_;
    string series;
    bool zts = false;
    string arch;
    string compiler;
}

package(polka.backend) struct peclReleaseItem
{
    string version_;
    string state;
}

package(polka.backend) struct peclReleaseList
{
    peclReleaseItem[] releases;
}

package(polka.backend) struct peclReleaseDocument
{
    string version_;
    string state;
    string downloadURL;
}

package(polka.backend) struct peclConfigureOption
{
    string name;
    string defaultValue;
    string prompt;
}

package(polka.backend) struct peclPackageFile
{
    string name;
    string md5;
}

package(polka.backend) struct peclPackageDir
{
    string name;
    peclPackageFile[] files;
    peclPackageDir[] dirs;
}

package(polka.backend) struct peclPackageDocument
{
    string name;
    string releaseVersion;
    string phpMin;
    string phpMax;
    string[] phpExcluded;
    string[] requiredExtensions;
    string[] requiredPackages;
    string providesExtension;
    bool hasExtSource = false;
    peclConfigureOption[] extOptions;
    bool hasZendExtSource = false;
    peclConfigureOption[] zendOptions;
    peclPackageDir contents;
}

package(polka.backend) struct peclResolution
{
    string packageName;
    string version_;
    string module_;
    bool zend = false;
    string downloadURL;
    string windowsURL;
    peclConfigureOption[string] configure;
    peclPackageDocument document;
}

package(polka.backend) struct peclInstalledFile
{
    string path;
    string sha256;
}

package(polka.backend) struct peclInstalledPackage
{
    string version_;
    string module_;
    bool zend = false;
    peclInstalledFile[] files;
}

package(polka.backend) struct peclInstallState
{
    peclInstalledPackage[string] packages;
}

/// Inspects a PHP executable to obtain version, series, ZTS mode, pointer architecture, and Windows compiler.
package(polka.backend) peclPHPInfo inspectPECLPHP(string phpPath) @trusted
{
    enum string script = `echo PHP_VERSION,"~",PHP_MAJOR_VERSION,".",PHP_MINOR_VERSION,"~",PHP_ZTS ? "1" : "0","~",PHP_INT_SIZE === 8 ? "x64" : "x86";`;
    auto cmdArgs = prepareInternalPHPCommandArgs(phpPath, ["-n", "-r", script]);

    string outStr, errStr;
    int exitCode = 0;
    try
    {
        exitCode = runCommandCapture(cmdArgs, null, null, outStr, errStr);
    }
    catch (Exception e)
    {
        throw new Exception(format("inspect target PHP: %s", e.msg));
    }
    if (exitCode != 0)
    {
        auto errTrimmed = errStr.strip();
        throw new Exception(format("inspect target PHP: exit status %d: %s", exitCode, errTrimmed));
    }

    auto trimmedOut = outStr.strip();
    auto parts = trimmedOut.split('~');
    if (parts.length != 4)
        throw new Exception(format("inspect target PHP: unexpected output %s", quoteGoString(trimmedOut)));

    peclPHPInfo info;
    info.version_ = parts[0];
    info.series = parts[1];
    info.zts = parts[2] == "1";
    info.arch = parts[3];

    if (currentOS() == "windows")
    {
        try
        {
            auto infoArgs = prepareInternalPHPCommandArgs(phpPath, ["-n", "-i"]);
            string infoOut, infoErr;
            if (runCommandCapture(infoArgs, null, null, infoOut, infoErr) == 0)
                info.compiler = parsePECLWindowsCompiler(infoOut);
        }
        catch (Exception) {}
    }

    return info;
}

/// Maps `php -i` compiler strings to PECL Windows archive tags (`vs17`, `vs16`, `vc15`).
package(polka.backend) string parsePECLWindowsCompiler(string output) @safe pure
{
    auto lower = output.toLower();
    if (lower.canFind("visual c++ 2022") || lower.canFind("msvc17"))
        return "vs17";
    if (lower.canFind("visual c++ 2019") || lower.canFind("msvc16"))
        return "vs16";
    if (lower.canFind("visual c++ 2017") || lower.canFind("msvc15"))
        return "vc15";
    return "";
}

/// Validates declared required PHP extensions and packages before building/installing a PECL release.
package(polka.backend) void validatePECLPackageDependencies(string phpPath, const peclResolution resolution) @trusted
{
    auto modules = installedPHPModules(phpPath);
    foreach (depName; resolution.document.requiredExtensions)
    {
        auto name = depName.strip().toLower();
        if (name.length > 0 && !modules.get(name, false))
        {
            throw new Exception(format(
                "PECL %s %s requires PHP extension %s; enable or install it first",
                resolution.packageName, resolution.version_, name
            ));
        }
    }
    foreach (depName; resolution.document.requiredPackages)
    {
        auto name = depName.strip().toLower();
        if (name.length > 0 && name != resolution.packageName && !modules.get(name, false))
        {
            throw new Exception(format(
                "PECL %s %s requires PECL package %s; install it explicitly first",
                resolution.packageName, resolution.version_, name
            ));
        }
    }
}

/// Fetches release + package XML metadata from PECL REST endpoints and resolves Windows DLL URL on Windows.
package(polka.backend) peclResolution resolvePECLRelease(
    HTTPClient client,
    string packageName,
    string ver,
    const peclPHPInfo phpInfo
) @trusted
{
    auto release = parsePECLReleaseDocument(peclDownloadText(client, peclRESTURL("r", packageName, ver ~ ".xml")));
    auto document = parsePECLPackageDocument(peclDownloadText(client, peclRESTURL("r", packageName, "package." ~ ver ~ ".xml")));

    if (!peclPHPVersionCompatible(phpInfo.version_, document.phpMin, document.phpMax, document.phpExcluded))
    {
        throw new Exception(format(
            "PECL %s %s does not support PHP %s",
            packageName, ver, phpInfo.version_
        ));
    }

    auto mod = document.providesExtension.strip().toLower();
    if (mod.length == 0)
        mod = packageName;

    peclResolution resolution;
    resolution.packageName = packageName;
    resolution.version_ = release.version_.strip();
    if (resolution.version_.length == 0)
        resolution.version_ = ver;
    resolution.module_ = mod;
    resolution.zend = document.hasZendExtSource;
    resolution.downloadURL = release.downloadURL.strip();
    resolution.document = document;

    const(peclConfigureOption)[] options;
    if (document.hasExtSource)
        options = document.extOptions;
    if (document.hasZendExtSource)
        options = document.zendOptions;
    foreach (opt; options)
    {
        auto normName = normalizePECLConfigureOptionName(opt.name);
        if (normName.length > 0)
            resolution.configure[normName] = opt;
    }

    if (currentOS() == "windows")
        resolution.windowsURL = resolvePECLWindowsArchive(client, packageName, resolution.version_, mod, phpInfo);

    return resolution;
}

/// Scrapes the Windows PECL DLL directory index to select a matching zip archive URL.
package(polka.backend) string resolvePECLWindowsArchive(
    HTTPClient client,
    string packageName,
    string ver,
    string module_,
    const peclPHPInfo phpInfo
) @trusted
{
    auto base = stripRightSlash(peclWindowsBaseURL) ~ "/" ~ packageName ~ "/" ~ ver ~ "/";
    auto resp = client.get(base);
    if (resp.statusCode != 200)
        throw new Exception(format("PECL %s %s has no Windows DLL archive", packageName, ver));

    auto htmlBody = resp.bodyText();
    auto mode = phpInfo.zts ? "ts" : "nts";
    auto needle = "-" ~ phpInfo.series.toLower() ~ "-" ~ mode ~ "-";

    string[] candidates;
    auto pattern = regex(`(?i)href=["']([^"']+)["']`);
    foreach (m; matchAll(htmlBody, pattern))
    {
        auto href = unescapeHTMLEntities(m[1]).strip();
        auto baseHref = baseName(fromSlashPath(href));
        auto lower = baseHref.toLower();
        bool compilerMatches = phpInfo.compiler.length == 0 || lower.canFind("-" ~ phpInfo.compiler.toLower() ~ "-");
        if (compilerMatches
            && lower.endsWith("-" ~ phpInfo.arch.toLower() ~ ".zip")
            && lower.canFind(needle)
            && lower.startsWith("php_"))
        {
            candidates ~= href;
        }
    }

    if (candidates.length == 0)
    {
        throw new Exception(format(
            "PECL %s %s has no Windows %s %s %s DLL",
            packageName, ver, phpInfo.series, mode.toUpper(), phpInfo.arch
        ));
    }

    candidates.sort();
    return base ~ stripLeftSlash(candidates[$ - 1]);
}

/// Downloads an XML/text document from PECL REST endpoints.
package(polka.backend) string peclDownloadText(HTTPClient client, string url) @trusted
{
    auto eff = client !is null ? client : new CurlHTTPClient();
    auto resp = eff.get(url);
    if (resp.statusCode != 200)
        throw new Exception(format("download %s: unexpected status %d", url, resp.statusCode));
    return resp.bodyText();
}

/// Builds a PECL REST URL joined with `/`.
package(polka.backend) string peclRESTURL(const(string)[] parts...) @safe
{
    return stripRightSlash(peclRESTBaseURL) ~ "/" ~ parts.join("/");
}

/// Parses `allreleases.xml` into a `peclReleaseList`.
package(polka.backend) peclReleaseList parsePECLReleaseList(string xmlText) @trusted
{
    XmlElement root;
    try
    {
        root = parseXML(xmlText);
    }
    catch (Exception e)
    {
        throw new Exception("decode PECL release list XML: " ~ e.msg);
    }

    peclReleaseList list;
    foreach (ref child; root.children)
    {
        if (child.name == "r")
            list.releases ~= peclReleaseItem(child.childText("v"), child.childText("s"));
    }
    return list;
}

/// Parses `<version>.xml` (`<r><v>...</v><st>...</st><g>...</g></r>`).
package(polka.backend) peclReleaseDocument parsePECLReleaseDocument(string xmlText) @trusted
{
    XmlElement root;
    try
    {
        root = parseXML(xmlText);
    }
    catch (Exception e)
    {
        throw new Exception("decode PECL release XML: " ~ e.msg);
    }

    peclReleaseDocument doc;
    doc.version_ = root.childText("v");
    doc.state = root.childText("st");
    doc.downloadURL = root.childText("g");
    return doc;
}

/// Parses `package.<version>.xml` into `peclPackageDocument`.
package(polka.backend) peclPackageDocument parsePECLPackageDocument(string xmlText) @trusted
{
    XmlElement root;
    try
    {
        root = parseXML(xmlText);
    }
    catch (Exception e)
    {
        throw new Exception("decode PECL package XML: " ~ e.msg);
    }

    peclPackageDocument doc;
    doc.name = root.childText("name");
    if (auto verNode = root.child("version"))
        doc.releaseVersion = verNode.childText("release");

    if (auto depsNode = root.child("dependencies"))
    {
        if (auto reqNode = depsNode.child("required"))
        {
            if (auto phpNode = reqNode.child("php"))
            {
                doc.phpMin = phpNode.childText("min");
                doc.phpMax = phpNode.childText("max");
                foreach (ref c; phpNode.children)
                {
                    if (c.name == "exclude")
                        doc.phpExcluded ~= c.text;
                }
            }
            foreach (ref c; reqNode.children)
            {
                if (c.name == "extension")
                    doc.requiredExtensions ~= c.childText("name");
                else if (c.name == "package")
                    doc.requiredPackages ~= c.childText("name");
            }
        }
    }

    doc.providesExtension = root.childText("providesextension");

    if (auto extNode = root.child("extsrcrelease"))
    {
        doc.hasExtSource = true;
        doc.extOptions = parsePECLConfigureOptions(*extNode);
    }
    if (auto zendNode = root.child("zendextsrcrelease"))
    {
        doc.hasZendExtSource = true;
        doc.zendOptions = parsePECLConfigureOptions(*zendNode);
    }

    if (auto contentsNode = root.child("contents"))
    {
        if (auto dirNode = contentsNode.child("dir"))
            doc.contents = parsePECLPackageDir(*dirNode);
    }

    return doc;
}

private peclConfigureOption[] parsePECLConfigureOptions(ref const XmlElement elem) @safe
{
    peclConfigureOption[] opts;
    foreach (ref c; elem.children)
    {
        if (c.name == "configureoption")
        {
            opts ~= peclConfigureOption(
                c.attr("name"),
                c.attr("default"),
                c.attr("prompt")
            );
        }
    }
    return opts;
}

private peclPackageDir parsePECLPackageDir(ref const XmlElement elem) @safe
{
    peclPackageDir dir;
    dir.name = elem.attr("name");
    foreach (ref c; elem.children)
    {
        if (c.name == "file")
            dir.files ~= peclPackageFile(c.attr("name"), c.attr("md5sum"));
        else if (c.name == "dir")
            dir.dirs ~= parsePECLPackageDir(c);
    }
    return dir;
}

/// Reports whether `ver` satisfies `<min>`, `<max>`, and `<exclude>` PHP dependency constraints.
package(polka.backend) bool peclPHPVersionCompatible(
    string ver,
    string minVer,
    string maxVer,
    const(string)[] excluded
) @safe
{
    if (minVer.strip().length > 0 && comparePECLVersions(ver, minVer) < 0)
        return false;
    if (maxVer.strip().length > 0 && comparePECLVersions(ver, maxVer) > 0)
        return false;
    foreach (excl; excluded)
    {
        if (comparePECLVersions(ver, excl) == 0)
            return false;
    }
    return true;
}

/// Numerically compares numeric segments of two PECL/PHP version strings.
package(polka.backend) int comparePECLVersions(string left, string right) @safe
{
    static int[] parseNumericParts(string value) @safe
    {
        int[] parts;
        foreach (m; matchAll(value, regex(`[0-9]+`)))
        {
            try
            {
                parts ~= m.hit.to!int;
            }
            catch (Exception)
            {
                parts ~= 0;
            }
        }
        return parts;
    }

    auto leftParts = parseNumericParts(left);
    auto rightParts = parseNumericParts(right);
    auto count = leftParts.length > rightParts.length ? leftParts.length : rightParts.length;
    foreach (idx; 0 .. count)
    {
        int lp = idx < leftParts.length ? leftParts[idx] : 0;
        int rp = idx < rightParts.length ? rightParts[idx] : 0;
        if (lp < rp)
            return -1;
        if (lp > rp)
            return 1;
    }
    return 0;
}

/// Validates user-supplied PECL configure options against options declared by `package.xml`.
package(polka.backend) void validatePECLConfigureOptions(
    const peclResolution resolution,
    const(string[string]) options
) @safe
{
    foreach (name, _; options)
    {
        auto norm = normalizePECLConfigureOptionName(name);
        if ((norm in resolution.configure) is null)
        {
            throw new Exception(format(
                "PECL %s %s does not declare configure option %s",
                resolution.packageName, resolution.version_, quoteGoString(name)
            ));
        }
    }
}

/// Normalizes a PECL configure option flag name by stripping leading dashes and lowercasing.
package(polka.backend) string normalizePECLConfigureOptionName(string name) @safe pure
{
    auto s = name.strip().toLower();
    while (s.length > 0 && s[0] == '-')
        s = s[1 .. $];
    return s;
}

/// Rejects configuring both a legacy PECL package and a PIE package that provide the same module.
package(polka.backend) void rejectPECLPIEConflict(const Environment env, string module_) @safe pure
{
    auto mod = module_.strip().toLower();
    foreach (pkg, _; env.pieExtensions)
    {
        if (pieExtensionModuleName(pkg) == mod)
        {
            throw new Exception(format(
                "PHP module %s is configured through both PIE package %s and legacy PECL; remove one provider",
                mod, pkg
            ));
        }
    }
}

/// Loads tracked legacy PECL package records from `<installRoot>/pecl-installed.json`.
package(polka.backend) peclInstallState readPECLInstallState(string installRoot) @trusted
{
    peclInstallState state;
    auto path = buildPath(installRoot, peclInstallStateFileName);
    string raw;
    try
    {
        raw = readTextStrict(path);
    }
    catch (ConfigNotExistException)
    {
        return state;
    }

    try
    {
        auto root = parseJSON(raw);
        if (root.type != JSONType.object)
            throw new Exception("expected JSON object");

        if (auto pkgsNode = "packages" in root)
        {
            if (pkgsNode.type == JSONType.object)
            {
                foreach (pkgName, pkgObj; pkgsNode.object)
                {
                    if (pkgObj.type != JSONType.object)
                        continue;
                    peclInstalledPackage pkg;
                    if (auto v = "version" in pkgObj) if (v.type == JSONType.string) pkg.version_ = v.str;
                    if (auto v = "module" in pkgObj) if (v.type == JSONType.string) pkg.module_ = v.str;
                    if (auto v = "zend" in pkgObj) pkg.zend = (v.type == JSONType.true_);
                    if (auto filesNode = "files" in pkgObj)
                    {
                        if (filesNode.type == JSONType.array)
                        {
                            foreach (fileObj; filesNode.array)
                            {
                                if (fileObj.type != JSONType.object)
                                    continue;
                                peclInstalledFile f;
                                if (auto vp = "path" in fileObj) if (vp.type == JSONType.string) f.path = vp.str;
                                if (auto vs = "sha256" in fileObj) if (vs.type == JSONType.string) f.sha256 = vs.str;
                                pkg.files ~= f;
                            }
                        }
                    }
                    state.packages[pkgName] = pkg;
                }
            }
        }
        return state;
    }
    catch (Exception e)
    {
        throw new Exception("decode PECL install state: " ~ e.msg);
    }
}

/// Persists (or removes when empty) `<installRoot>/pecl-installed.json`.
package(polka.backend) void writePECLInstallState(string installRoot, const peclInstallState state) @trusted
{
    auto path = buildPath(installRoot, peclInstallStateFileName);
    if (state.packages.length == 0)
    {
        removeFileIgnoreNotExist(path);
        return;
    }

    JSONValue root = JSONValue.emptyObject;
    JSONValue pkgsObj = JSONValue.emptyObject;
    auto pkgKeys = state.packages.keys;
    pkgKeys.sort();
    foreach (k; pkgKeys)
    {
        auto pkg = state.packages[k];
        JSONValue pkgObj = JSONValue.emptyObject;
        pkgObj["version"] = pkg.version_;
        pkgObj["module"] = pkg.module_;
        if (pkg.zend)
            pkgObj["zend"] = true;

        JSONValue filesArr = JSONValue.emptyArray;
        foreach (f; pkg.files)
        {
            JSONValue fObj = JSONValue.emptyObject;
            fObj["path"] = f.path;
            fObj["sha256"] = f.sha256;
            filesArr.array ~= fObj;
        }
        pkgObj["files"] = filesArr;
        pkgsObj[k] = pkgObj;
    }
    root["packages"] = pkgsObj;

    try
    {
        writeTextAtomic(path, root.toPrettyString() ~ "\n", octal!644);
    }
    catch (Exception e)
    {
        throw new Exception("write PECL install state: " ~ e.msg);
    }
}

/// Validates that `relative` stays contained inside `installRoot` and returns its cleaned path.
package(polka.backend) string peclStateArtifactPath(string installRoot, string relative) @trusted
{
    auto root = cleanPath(installRoot);
    auto path = cleanPath(buildPath(root, fromSlashPath(relative)));
    if (!isPathWithin(root, path))
        throw new Exception(format("PECL state path %s escapes PHP install root", quoteGoString(relative)));
    return path;
}

/// Reports whether another tracked PECL package references `path`.
package(polka.backend) bool peclFileReferencedByOtherPackage(
    const peclInstallState state,
    string packageName,
    string path
) @safe
{
    foreach (name, record; state.packages)
    {
        if (name == packageName)
            continue;
        if (peclRecordContainsPath(record.files, path))
            return true;
    }
    return false;
}

/// Reports whether `files` contains a path equal (case-insensitively after cleaning) to `path`.
package(polka.backend) bool peclRecordContainsPath(const(peclInstalledFile)[] files, string path) @safe
{
    auto target = cleanPath(path);
    foreach (f; files)
    {
        if (icmp(cleanPath(f.path), target) == 0)
            return true;
    }
    return false;
}

/// Computes lowercase SHA-256 hex digest of `path`, throwing `ConfigNotExistException` when absent.
package(polka.backend) string fileSHA256(string path) @trusted
{
    bool isDirFlag = false;
    if (!statPathExists(path, isDirFlag))
        throw new ConfigNotExistException(format("open %s: no such file or directory", path));
    if (isDirFlag)
        throw new Exception(format("read %s: is a directory", path));
    long sz = 0;
    return fileDigestHex("sha256", path, sz);
}

/// Computes lowercase MD5 hex digest of `path`, throwing `ConfigNotExistException` when absent.
package(polka.backend) string fileMD5(string path) @trusted
{
    bool isDirFlag = false;
    if (!statPathExists(path, isDirFlag))
        throw new ConfigNotExistException(format("open %s: no such file or directory", path));
    if (isDirFlag)
        throw new Exception(format("read %s: is a directory", path));
    long sz = 0;
    return fileDigestHex("md5", path, sz);
}

/// Copies `source` atomically to `target`, preserving readable retry advice on Windows file lock errors.
package(polka.backend) void copyPECLArtifact(string source, string target) @trusted
{
    auto parent = dirName(target);
    if (!exists(parent))
        mkdirRecurse(parent);

    auto stageDir = parent;
    auto tmpDir = createTempDir("pecl-artifact-", stageDir);
    scope (exit) rmdirRecurseQuiet(tmpDir);

    auto tmpFile = buildPath(tmpDir, baseName(target));
    copy(source, tmpFile);
    setFileMode(tmpFile, octal!644);
    try
    {
        replaceFile(tmpFile, target);
    }
    catch (Exception e)
    {
        throw new Exception(format(
            "install PECL artifact %s: %s; stop the environment and retry",
            target, e.msg
        ));
    }
}

/// Installs one PECL artifact while protecting existing unowned files from unintended overwrites.
package(polka.backend) bool installPECLArtifact(
    string source,
    string target,
    string installRoot,
    const peclInstallState state,
    string packageName,
    bool allowShared
) @trusted
{
    auto cleanRoot = cleanPath(installRoot);
    auto cleanTarget = cleanPath(target);
    if (!isPathWithin(cleanRoot, cleanTarget))
        throw new Exception(format("PECL artifact %s is outside PHP install root", target));

    import std.path : relativePath;
    auto relNative = relativePath(cleanPath(absolutePath(cleanTarget)), cleanPath(absolutePath(cleanRoot)));
    auto relSlash = toSlashPath(relNative);

    bool targetIsDir = false;
    bool targetExists = statPathExists(target, targetIsDir);
    if (!targetExists)
    {
        copyPECLArtifact(source, target);
        return true;
    }

    bool ownedByPackage = false;
    if (auto pkgPtr = packageName in state.packages)
        ownedByPackage = peclRecordContainsPath(pkgPtr.files, relSlash);
    bool ownedByOther = peclFileReferencedByOtherPackage(state, packageName, relSlash);

    if (ownedByPackage)
    {
        copyPECLArtifact(source, target);
        return true;
    }

    auto sourceHash = fileSHA256(source);
    auto targetHash = fileSHA256(target);
    if (icmp(sourceHash, targetHash) != 0)
        throw new Exception(format("refusing to overwrite unowned PECL artifact %s", target));

    if (allowShared && ownedByOther)
        return true;
    if (allowShared)
        return false;

    throw new Exception(format("refusing to adopt untracked PECL module %s", target));
}

/// Extracts a PECL Windows zip archive into `targetDir` without collapsing top-level directories.
package(polka.backend) void extractPECLZip(string archivePath, string targetDir) @trusted
{
    auto raw = cast(ubyte[]) read(archivePath);
    auto za = new ZipArchive(cast(void[]) raw);
    mkdirRecurse(targetDir);

    foreach (name, member; za.directory)
    {
        auto entryPath = safePECLArchivePath(targetDir, name);
        bool isDirEntry = name.endsWith("/") || name.endsWith("\\");
        uint mode = member.fileAttributes & 0xFFFF;
        if (!isDirEntry && (mode & 0xF000) == 0x4000)
            isDirEntry = true;

        if (isDirEntry)
        {
            mkdirRecurse(entryPath);
            continue;
        }

        auto parent = dirName(entryPath);
        if (!exists(parent))
            mkdirRecurse(parent);
        write(entryPath, za.expand(member));
    }
}

/// Extracts a PECL `.tgz` source tarball into `targetDir` without collapsing top-level directories.
package(polka.backend) void extractPECLTarGz(string archivePath, string targetDir) @trusted
{
    auto gzData = cast(const(ubyte)[]) read(archivePath);
    auto tarData = decompressGzip(gzData);
    mkdirRecurse(targetDir);

    string nextPaxPath;
    string nextGnuLongName;

    size_t offset = 0;
    while (offset + 512 <= tarData.length)
    {
        auto hdr = tarData[offset .. offset + 512];
        offset += 512;

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

        long recordedSum = parsePECLTarOctal(hdr[148 .. 156]);
        long unsignedSum = 0;
        foreach (i; 0 .. 512)
        {
            ubyte b = (i >= 148 && i < 156) ? cast(ubyte) ' ' : hdr[i];
            unsignedSum += b;
        }
        if (recordedSum != unsignedSum)
            throw new Exception("invalid tar header checksum");

        string name = extractPECLTarCString(hdr[0 .. 100]);
        string prefix = extractPECLTarCString(hdr[345 .. 500]);
        if (prefix.length > 0)
            name = prefix ~ "/" ~ name;

        uint mode = cast(uint) parsePECLTarOctal(hdr[100 .. 108]);
        ulong size = cast(ulong) parsePECLTarOctal(hdr[124 .. 136]);
        char typeFlag = cast(char) hdr[156];

        size_t paddedSize = cast(size_t)((size + 511) / 512) * 512;
        if (offset + paddedSize > tarData.length)
            throw new Exception("truncated tar entry payload for " ~ name);
        auto payload = tarData[offset .. offset + cast(size_t) size];
        offset += paddedSize;

        if (typeFlag == 'L')
        {
            auto s = (cast(const(char)[]) payload).idup;
            while (s.length > 0 && s[$ - 1] == '\0')
                s = s[0 .. $ - 1];
            nextGnuLongName = s;
            continue;
        }
        if (typeFlag == 'x')
        {
            parsePECLPaxPath(cast(string) payload, nextPaxPath);
            continue;
        }
        if (typeFlag == 'g')
            continue;

        if (nextGnuLongName.length > 0)
        {
            name = nextGnuLongName;
            nextGnuLongName = null;
        }
        if (nextPaxPath.length > 0)
        {
            name = nextPaxPath;
            nextPaxPath = null;
        }

        auto entryPath = safePECLArchivePath(targetDir, name);
        if (typeFlag == '5')
        {
            mkdirRecurse(entryPath);
        }
        else if (typeFlag == '0' || typeFlag == '\0')
        {
            auto parent = dirName(entryPath);
            if (!exists(parent))
                mkdirRecurse(parent);
            write(entryPath, payload);
            uint perm = mode & octal!777;
            if (perm != 0)
                setFileMode(entryPath, perm);
        }
    }
}

private string extractPECLTarCString(const(ubyte)[] field) @safe pure
{
    foreach (i, b; field)
    {
        if (b == 0)
            return (cast(const(char)[]) field[0 .. i]).idup;
    }
    return (cast(const(char)[]) field).idup;
}

private long parsePECLTarOctal(const(ubyte)[] field) @safe pure
{
    if (field.length > 0 && (field[0] & 0x80) != 0)
    {
        long val = 0;
        foreach (i, b; field)
            val = (i == 0) ? (b & 0x7F) : ((val << 8) | b);
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

private void parsePECLPaxPath(string payload, ref string paxPath) @safe
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
        if (eq > 0 && record[0 .. eq] == "path")
            paxPath = record[eq + 1 .. $];
        i += recLen;
    }
}

/// Validates that an archive entry path stays within `root`, throwing Go-identical escape errors.
package(polka.backend) string safePECLArchivePath(string root, string entry) @trusted
{
    auto slash = toSlashPath(entry);
    if (slash.startsWith("/") || (slash.length >= 2 && slash[1] == ':'))
        throw new Exception(format("PECL archive entry %s escapes staging directory", quoteGoString(entry)));

    auto cleanRoot = cleanPath(root);
    auto path = cleanPath(buildPath(cleanRoot, fromSlashPath(entry)));
    if (!isPathWithin(cleanRoot, path))
        throw new Exception(format("PECL archive entry %s escapes staging directory", quoteGoString(entry)));
    return path;
}

/// Recursively locates the first non-directory file matching `name` case-insensitively under `root`.
package(polka.backend) string findPECLFile(string root, string name) @trusted
{
    string[] entries;
    foreach (entry; dirEntries(root, SpanMode.depth))
        entries ~= entry.name;
    entries.sort();

    foreach (candidate; entries)
    {
        if (!isDir(candidate) && icmp(baseName(candidate), name) == 0)
            return candidate;
    }
    throw new ConfigNotExistException(format("file %s not found under %s", name, root));
}

/// Locates the directory containing `config.m4` inside extracted PECL sources.
package(polka.backend) string findPECLBuildRoot(string root) @trusted
{
    try
    {
        auto configM4 = findPECLFile(root, "config.m4");
        return dirName(configM4);
    }
    catch (Exception e)
    {
        throw new Exception("PECL source archive has no config.m4: " ~ e.msg);
    }
}

/// Verifies published MD5 digests for every extracted `<file>` declared in `package.xml`.
package(polka.backend) void verifyPECLPackageFiles(string root, const peclPackageDir directory) @trusted
{
    void verify(const peclPackageDir current)
    {
        foreach (file; current.files)
        {
            if (file.md5.strip().length == 0)
                continue;
            auto path = safePECLArchivePath(root, file.name);
            string actual;
            try
            {
                actual = fileMD5(path);
            }
            catch (Exception e)
            {
                throw new Exception(format("verify PECL package file %s: %s", file.name, e.msg));
            }
            if (icmp(actual, file.md5.strip()) != 0)
                throw new Exception(format("PECL package checksum mismatch for %s", file.name));
        }
        foreach (child; current.dirs)
            verify(child);
    }

    verify(directory);
}

/// Resolves host extension build tools (`phpize`, `php-config`, `make`, `autoconf`, `cc`/`gcc`) for legacy PECL builds.
package(polka.backend) void resolvePECLBuildTools(
    string installRoot,
    string phpPath,
    out string phpizeOut,
    out string phpConfigOut
) @trusted
{
    resolvePHPBuildTools(installRoot, phpPath, "legacy PECL Linux builds", phpizeOut, phpConfigOut);
}

/// Verifies `phpize`/`php-config` match `phpPath` and checks required C toolchain commands on `PATH`.
package(polka.backend) void resolvePHPBuildTools(
    string installRoot,
    string phpPath,
    string purpose,
    out string phpizeOut,
    out string phpConfigOut
) @trusted
{
    try
    {
        phpizeOut = findPECLBuildTool(installRoot, "phpize");
    }
    catch (Exception e)
    {
        throw new Exception(format("%s require phpize matching %s: %s", purpose, phpPath, e.msg));
    }

    try
    {
        phpConfigOut = findPECLBuildTool(installRoot, "php-config");
    }
    catch (Exception e)
    {
        throw new Exception(format("%s require php-config matching %s: %s", purpose, phpPath, e.msg));
    }

    auto target = inspectPECLPHP(phpPath);
    string cfgOut, cfgErr;
    int cfgCode = 0;
    try
    {
        cfgCode = runCommandCapture([phpConfigOut, "--version"], null, null, cfgOut, cfgErr);
    }
    catch (Exception e)
    {
        throw new Exception(format("inspect php-config %s for %s: %s", phpConfigOut, purpose, e.msg));
    }
    if (cfgCode != 0)
        throw new Exception(format("inspect php-config %s for %s: exit status %d: %s", phpConfigOut, purpose, cfgCode, cfgErr.strip()));

    auto reportedVer = cfgOut.strip();
    if (!reportedVer.startsWith(target.series ~ ".") && reportedVer != target.series)
    {
        throw new Exception(format(
            "php-config %s reports PHP %s but target PHP is %s",
            phpConfigOut, reportedVer, target.version_
        ));
    }

    foreach (tool; ["make", "autoconf"])
    {
        try
        {
            lookPath(tool);
        }
        catch (Exception)
        {
            throw new Exception(format("%s require %s on PATH", purpose, tool));
        }
    }

    bool hasCompiler = false;
    try
    {
        lookPath("cc");
        hasCompiler = true;
    }
    catch (Exception) {}

    if (!hasCompiler)
    {
        try
        {
            lookPath("gcc");
            hasCompiler = true;
        }
        catch (Exception) {}
    }

    if (!hasCompiler)
        throw new Exception(format("%s require a C compiler (cc or gcc) on PATH", purpose));
}

private string findPECLBuildTool(string installRoot, string name) @trusted
{
    foreach (candidate; [buildPath(installRoot, name), buildPath(installRoot, "bin", name)])
    {
        if (regularFileExistsAt(candidate))
            return candidate;
    }
    return lookPath(name);
}

/// Runs one PECL build step (`phpize`, `configure`, `make`) inside `directory`, streaming output.
package(polka.backend) void runPECLBuildCommand(
    TextWriter stdout,
    TextWriter stderr,
    string directory,
    string target,
    const(string)[] args...
) @trusted
{
    string[] cmdArgs = [target];
    cmdArgs ~= args;
    int code = 0;
    try
    {
        code = runCommandStreaming(cmdArgs, directory, null, stdout, stderr);
    }
    catch (Exception e)
    {
        throw new Exception(format("run PECL build command %s: %s", baseName(target), e.msg));
    }
    if (code != 0)
        throw new Exception(format("run PECL build command %s: exit status %d", baseName(target), code));
}

/// Returns true if all recorded files for `record` exist inside `installRoot` with matching SHA-256 digests.
package(polka.backend) bool peclRecordFilesValid(string installRoot, const peclInstalledPackage record) @trusted
{
    if (record.files.length == 0)
        return false;
    foreach (f; record.files)
    {
        try
        {
            auto path = peclStateArtifactPath(installRoot, f.path);
            auto digest = fileSHA256(path);
            if (icmp(digest, f.sha256) != 0)
                return false;
        }
        catch (Exception)
        {
            return false;
        }
    }
    return true;
}

private string stripLeftSlash(string s) @safe pure nothrow @nogc
{
    while (s.length > 0 && s[0] == '/')
        s = s[1 .. $];
    return s;
}

private string stripRightSlash(string s) @safe pure nothrow @nogc
{
    while (s.length > 0 && s[$ - 1] == '/')
        s = s[0 .. $ - 1];
    return s;
}

private string unescapeHTMLEntities(string s) @safe
{
    if (!s.canFind('&'))
        return s;

    auto buf = appender!string();
    for (size_t i = 0; i < s.length; i++)
    {
        if (s[i] == '&')
        {
            auto semi = s.indexOf(';', i + 1);
            if (semi > 0 && semi - i <= 12)
            {
                auto ent = s[i + 1 .. semi];
                switch (ent)
                {
                    case "amp":  buf.put('&');  i = semi; continue;
                    case "lt":   buf.put('<');  i = semi; continue;
                    case "gt":   buf.put('>');  i = semi; continue;
                    case "quot": buf.put('"');  i = semi; continue;
                    case "apos": buf.put('\''); i = semi; continue;
                    default:
                        if (ent.length >= 2 && ent[0] == '#')
                        {
                            try
                            {
                                uint code = 0;
                                if (ent.length >= 3 && (ent[1] == 'x' || ent[1] == 'X'))
                                    code = to!uint(ent[2 .. $], 16);
                                else
                                    code = to!uint(ent[1 .. $], 10);
                                if (code > 0 && code <= 0x10FFFF)
                                {
                                    buf.put(cast(dchar) code);
                                    i = semi;
                                    continue;
                                }
                            }
                            catch (Exception) {}
                        }
                        break;
                }
            }
        }
        buf.put(s[i]);
    }
    return buf.data;
}

unittest
{
    import polka.support.http : MockHTTPClient;

    // TestResolvePECLWindowsArchive
    {
        auto origURL = peclWindowsBaseURL;
        peclWindowsBaseURL = "https://mock.pecl.win/releases";
        scope (exit) peclWindowsBaseURL = origURL;

        auto mock = new MockHTTPClient((string url, string[string] hdrs) {
            return HTTPResponse(200, null, cast(ubyte[]) [
                `<a href="php_redis-6.2.0-8.4-ts-vs17-x64.zip">TS</a>`,
                `<a href="php_redis-6.2.0-8.4-nts-vs17-x64.zip">NTS</a>`,
                `<a href="php_redis-6.2.0-8.3-nts-vs16-x64.zip">old PHP</a>`,
            ].join("\n"));
        });

        auto url = resolvePECLWindowsArchive(mock, "redis", "6.2.0", "redis", peclPHPInfo("", "8.4", false, "x64", ""));
        assert(url.endsWith("php_redis-6.2.0-8.4-nts-vs17-x64.zip"), url);
    }

    // TestPECLPHPVersionCompatible
    {
        assert(peclPHPVersionCompatible("8.4.1", "8.1.0", "8.4.99", null));
        assert(!peclPHPVersionCompatible("8.0.30", "8.1.0", "", null));
        assert(!peclPHPVersionCompatible("8.4.1", "", "", ["8.4.1"]));
    }

    // TestValidatePECLConfigureOptions
    {
        peclResolution resolution;
        resolution.packageName = "imagick";
        resolution.version_ = "3.8.0";
        resolution.configure = ["with-imagick": peclConfigureOption("with-imagick", "", "")];

        validatePECLConfigureOptions(resolution, ["with-imagick": "/opt"]);

        bool threwUnknown = false;
        try
        {
            validatePECLConfigureOptions(resolution, ["unknown": "yes"]);
        }
        catch (Exception)
        {
            threwUnknown = true;
        }
        assert(threwUnknown);
    }

    // TestValidatePECLExtensionsSchema
    {
        string valid = "pecl-extensions:\n  redis: 6.2.0\n  imagick:\n    version: 3.8.0\n    configure-options:\n      with-imagick: autodetect\n";
        validateEnvironmentFileSchema(valid, null);

        foreach (invalid; [
            "pecl-extensions:\n  redis: true\n",
            "pecl-extensions:\n  redis:\n    version: 6.2.0\n    unknown: value\n",
        ])
        {
            bool threwInvalid = false;
            try
            {
                validateEnvironmentFileSchema(invalid, null);
            }
            catch (Exception)
            {
                threwInvalid = true;
            }
            assert(threwInvalid);
        }
    }

    // TestPECLXMLAndArchiveHelpers
    {
        auto relList = parsePECLReleaseList(`<?xml version="1.0" encoding="UTF-8"?><a><r><v>6.2.0</v><s>stable</s></r><r><v>6.3.0RC1</v><s>beta</s></r></a>`);
        assert(relList.releases.length == 2);
        assert(relList.releases[0].version_ == "6.2.0" && relList.releases[0].state == "stable");

        auto doc = parsePECLPackageDocument(`<?xml version="1.0"?>
            <package>
                <name>imagick</name>
                <version><release>3.8.0</release></version>
                <dependencies><required>
                    <php><min>8.0.0</min><max>8.4.99</max><exclude>8.2.0</exclude></php>
                    <extension><name>spl</name></extension>
                    <package><name>pear</name></package>
                </required></dependencies>
                <providesextension>imagick</providesextension>
                <extsrcrelease>
                    <configureoption name="with-imagick" default="autodetect" prompt="Prefix?"/>
                </extsrcrelease>
                <contents><dir name="/">
                    <file name="config.m4" md5sum="d41d8cd98f00b204e9800998ecf8427e"/>
                </dir></contents>
            </package>`);
        assert(doc.name == "imagick" && doc.releaseVersion == "3.8.0");
        assert(doc.phpMin == "8.0.0" && doc.phpExcluded == ["8.2.0"]);
        assert(doc.requiredExtensions == ["spl"] && doc.requiredPackages == ["pear"]);
        assert(doc.hasExtSource && !doc.hasZendExtSource && doc.extOptions.length == 1);
        assert(doc.contents.files.length == 1 && doc.contents.files[0].name == "config.m4");

        bool threwEscape = false;
        try
        {
            safePECLArchivePath("/tmp/stage", "../evil");
        }
        catch (Exception e)
        {
            threwEscape = e.msg.canFind("escapes staging directory");
        }
        assert(threwEscape);
    }

    // TestResolvePHPBuildTools (mirrors backend/php_build_tools_test.go)
    version (linux)
    {
        import std.process : environment;

        auto binDir = createTempDir("polka-php-build-tools-");
        scope (exit) rmdirRecurseQuiet(binDir);

        foreach (name, content; [
            "php":        "#!/bin/sh\nprintf '8.4.24~8.4~0~x64\\n'\n",
            "phpize":     "#!/bin/sh\nexit 0\n",
            "php-config": "#!/bin/sh\nprintf '8.4.24\\n'\n",
            "make":       "#!/bin/sh\nexit 0\n",
            "autoconf":   "#!/bin/sh\nexit 0\n",
            "cc":         "#!/bin/sh\nexit 0\n",
        ])
        {
            auto p = buildPath(binDir, name);
            write(p, content);
            setFileMode(p, octal!755);
        }

        auto origPath = environment.get("PATH", "");
        bool hadPath = environment.get("PATH", null) !is null;
        scope (exit)
        {
            if (hadPath)
                environment["PATH"] = origPath;
            else
                environment.remove("PATH");
        }
        environment["PATH"] = binDir;

        auto phpPath = buildPath(binDir, "php");
        string phpize, phpConfig;
        resolvePHPBuildTools(binDir, phpPath, "PHP extension SDK", phpize, phpConfig);
        assert(phpize == buildPath(binDir, "phpize"));
        assert(phpConfig == buildPath(binDir, "php-config"));
    }
}
