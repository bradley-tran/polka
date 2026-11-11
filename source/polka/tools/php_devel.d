module polka.tools.php_devel;

import std.algorithm : canFind, endsWith;
import std.file : exists, isDir, mkdirRecurse, readText, write;
import std.format : format;
import std.path : baseName, buildPath, dirName, extension;
import std.regex : matchFirst, regex;
import std.string : join, strip;

import polka.support.fs : createTempDir, currentOS, octal, rmdirRecurseQuiet, setFileMode;
import polka.support.http : HTTPClient;
import polka.tools.cache : cacheArchivePayload;
import polka.tools.checksum : verifyChecksum;
import polka.tools.download_io : downloadFile;
import polka.tools.ids;
import polka.tools.manifest;
import polka.tools.php;
import polka.tools.types;

enum string polkaPHPDevelPatchMarker = "// Polka auto-patch: dynamically resolve PHP_PREFIX to the matching Polka PHP runtime";

/// Returns the internal Windows `php-devel` plugin with post-install `phpize` patching.
ToolPlugin phpDevelPlugin() @safe
{
    PluginHooks hooks;
    hooks.download = (DownloadContext ctx) {
        downloadPHPDevel(ctx.client, ctx.cacheDir, ctx.version_);
    };
    hooks.postInstall = (InstallContext ctx) {
        postInstallPHPDevel(ctx);
    };
    return newManifestPlugin(PHPDevel, hooks);
}

/// Applies Polka dynamic `PHP_PREFIX` patches after installing the Windows PHP devel pack.
void postInstallPHPDevel(const InstallContext ctx) @trusted
{
    auto targetDir = resolvePHPDevelTargetDir(ctx);
    if (targetDir.length == 0)
        return;
    patchPHPDevel(targetDir);
}

private string resolvePHPDevelTargetDir(const InstallContext ctx) @trusted
{
    auto target = ctx.result.targetPath.strip();
    if (target.length > 0)
    {
        if (exists(target) && !isDir(target))
            return dirName(target);
        if (extension(target).length > 0)
            return dirName(target);
        return target;
    }
    if (ctx.envsDir.length > 0 && ctx.result.version_.length > 0)
        return buildPath(ctx.envsDir, PHPDevel, ctx.result.version_);
    return "";
}

/// Updates Windows `php-devel` scripts so `PHP_PREFIX` dynamically locates the matching Polka PHP runtime.
void patchPHPDevel(string targetDir) @trusted
{
    targetDir = targetDir.strip();
    if (targetDir.length == 0 || !exists(targetDir))
        return;
    if (!isDir(targetDir))
        targetDir = dirName(targetDir);

    try
    {
        patchPHPDevelScript(targetDir, buildPath("script", "phpize.js"), &patchPhpizeJS);
    }
    catch (Exception e)
    {
        throw new Exception("patch phpize.js: " ~ e.msg);
    }

    try
    {
        patchPHPDevelScript(targetDir, buildPath("script", "config.w32.phpize.in"), &patchConfigW32PhpizeIn);
    }
    catch (Exception e)
    {
        throw new Exception("patch config.w32.phpize.in: " ~ e.msg);
    }
}

private void patchPHPDevelScript(
    string targetDir,
    string relativePath,
    string function(string) @safe patchFn
) @trusted
{
    auto fullPath = buildPath(targetDir, relativePath);
    if (!exists(fullPath))
        throw new Exception(format(`php-devel file "%s" not found under %s`, relativePath, targetDir));

    auto content = readText(fullPath);
    if (content.canFind(polkaPHPDevelPatchMarker))
        return;

    auto patched = patchFn(content);
    if (patched == content)
        return;

    write(fullPath, patched);
    setFileMode(fullPath, octal!644);
}

private string detectLineEnding(string s) @safe pure
{
    return s.canFind("\r\n") ? "\r\n" : "\n";
}

private string patchPhpizeJS(string content) @safe
{
    if (content.canFind(polkaPHPDevelPatchMarker))
        return content;

    auto nl = detectLineEnding(content);
    auto snippet = nl ~ polkaPHPDevelPatchMarker ~ nl ~
        "(function() {" ~ nl ~
        "\ttry {" ~ nl ~
        "\t\tvar envShell = WScript.CreateObject(\"WScript.Shell\");" ~ nl ~
        "\t\tvar envPrefix = envShell.Environment(\"Process\").Item(\"PHP_PREFIX\");" ~ nl ~
        "\t\tif (envPrefix && FSO.FileExists(FSO.BuildPath(envPrefix, \"php.exe\"))) {" ~ nl ~
        "\t\t\tPHP_PREFIX = envPrefix;" ~ nl ~
        "\t\t\treturn;" ~ nl ~
        "\t\t}" ~ nl ~
        "\t\tvar polkaEnvsDir = FSO.GetParentFolderName(FSO.GetParentFolderName(PHP_DIR));" ~ nl ~
        "\t\tvar polkaFlavor = (typeof(PHP_ZTS) == \"string\" && PHP_ZTS.toLowerCase() == \"yes\") ? \"php-zts\" : \"php\";" ~ nl ~
        "\t\tvar polkaCandidate = FSO.BuildPath(FSO.BuildPath(polkaEnvsDir, polkaFlavor), PHP_VERSION + \".\" + PHP_MINOR_VERSION);" ~ nl ~
        "\t\tif (FSO.FileExists(FSO.BuildPath(polkaCandidate, \"php.exe\"))) {" ~ nl ~
        "\t\t\tPHP_PREFIX = polkaCandidate;" ~ nl ~
        "\t\t}" ~ nl ~
        "\t} catch (e) {}" ~ nl ~
        "})();";

    auto rePhpizePHPDir = regex(`^(var\s+PHP_DIR\s*=\s*FSO\.GetParentFolderName\(WScript\.ScriptFullName\)\.replace\([^\)]+\);?\r?)$`, "m");
    auto m = matchFirst(content, rePhpizePHPDir);
    if (m.empty)
        throw new Exception("could not find PHP_DIR anchor line in phpize.js");

    return m.pre ~ m.hit ~ snippet ~ m.post;
}

private string patchConfigW32PhpizeIn(string content) @safe
{
    if (content.canFind(polkaPHPDevelPatchMarker))
        return content;

    auto nl = detectLineEnding(content);
    auto replacement = "if (PHP_PREFIX == '' || PHP_PREFIX == \"C:\\\\php\") {" ~ nl ~
        "\t" ~ polkaPHPDevelPatchMarker ~ nl ~
        "\ttry {" ~ nl ~
        "\t\tvar envShell = WshShell;" ~ nl ~
        "\t\tvar envPrefix = envShell.Environment(\"Process\").Item(\"PHP_PREFIX\");" ~ nl ~
        "\t\tif (envPrefix && FSO.FileExists(FSO.BuildPath(envPrefix, \"php.exe\"))) {" ~ nl ~
        "\t\t\tPHP_PREFIX = envPrefix;" ~ nl ~
        "\t\t} else {" ~ nl ~
        "\t\t\tvar polkaEnvsDir = FSO.GetParentFolderName(FSO.GetParentFolderName(PHP_DIR));" ~ nl ~
        "\t\t\tvar polkaFlavor = (typeof(PHP_ZTS) == \"string\" && PHP_ZTS.toLowerCase() == \"yes\") ? \"php-zts\" : \"php\";" ~ nl ~
        "\t\t\tvar polkaCandidate = FSO.BuildPath(FSO.BuildPath(polkaEnvsDir, polkaFlavor), PHP_VERSION + \".\" + PHP_MINOR_VERSION);" ~ nl ~
        "\t\t\tif (FSO.FileExists(FSO.BuildPath(polkaCandidate, \"php.exe\"))) {" ~ nl ~
        "\t\t\t\tPHP_PREFIX = polkaCandidate;" ~ nl ~
        "\t\t\t}" ~ nl ~
        "\t\t}" ~ nl ~
        "\t} catch (e) {}" ~ nl ~
        "\tif (PHP_PREFIX == '') {" ~ nl ~
        "\t\tPHP_PREFIX = \"C:\\\\php\";" ~ nl ~
        "\t}" ~ nl ~
        "\tif (PHP_DEBUG == \"yes\")" ~ nl ~
        "\t\tPHP_PREFIX += \"\\\\debug\";" ~ nl ~
        "}" ~ nl ~
        "DEFINE('PHP_PREFIX', PHP_PREFIX);";

    auto reConfigW32Prefix = regex(`if\s*\(\s*PHP_PREFIX\s*==\s*''\s*\)\s*\{[\s\S]*?DEFINE\(\s*'PHP_PREFIX'\s*,\s*PHP_PREFIX\s*\);`);
    auto m = matchFirst(content, reConfigW32Prefix);
    if (m.empty)
        throw new Exception("could not find PHP_PREFIX block in config.w32.phpize.in");

    return m.pre ~ replacement ~ m.post;
}

/// Downloads the Windows `devel_pack` paired with the selected NTS/ZTS PHP variant.
void downloadPHPDevel(HTTPClient client, string cacheDir, string ver) @trusted
{
    if (currentOS() != "windows")
        throw new Exception("automatic php-devel download is only implemented on Windows");

    auto parsed = parsePHPDevelCacheVersion(ver);
    auto runtimeVersion = parsed.version_;
    bool threadSafe = parsed.threadSafe;

    mkdirRecurse(buildPath(cacheDir, PHPDevel));
    auto index = fetchPHPWindowsReleaseIndex(client);
    auto relPtr = phpSeries(runtimeVersion) in index;
    if (relPtr is null)
        throw new Exception(format(`php version "%s" is not available in the Windows release index`, runtimeVersion));

    auto variant = selectPHPWindowsVariant(*relPtr, threadSafe);
    auto asset = variant.develPack;
    if (asset.path.strip().length == 0 || asset.sha256.strip().length == 0)
        throw new Exception(format("Windows PHP %s release does not publish a matching devel pack", relPtr.version_));

    auto stagingDir = createTempDir(ver ~ "-tmp-", buildPath(cacheDir, PHPDevel));
    scope (exit) rmdirRecurseQuiet(stagingDir);

    auto fileName = baseName(asset.path);
    auto archivePath = buildPath(stagingDir, fileName);
    auto archiveURL = format("%s/%s", phpWindowsBaseURL, asset.path);
    downloadFile(client, archiveURL, archivePath);
    verifyChecksum(asset.sha256, archivePath);

    DownloadAsset dlAsset;
    dlAsset.fileName = fileName;
    dlAsset.url = archiveURL;
    dlAsset.checksum = asset.sha256;
    dlAsset.checksumAlgorithm = ChecksumAlgorithm.sha256;
    dlAsset.archiveFormat = ArchiveFormat.zip;
    cacheArchivePayload(cacheDir, PHPDevel, ver, relPtr.version_, dlAsset, archivePath);
}

/// Splits cache flavor suffixes (`"8.4-zts"` / `"8.4-nts"`) from user-facing PHP versions.
auto parsePHPDevelCacheVersion(string ver) @safe pure
{
    struct ParsedDevelVersion { string version_; bool threadSafe; }
    auto trimmed = ver.strip();
    if (trimmed.endsWith("-zts"))
        return ParsedDevelVersion(trimmed[0 .. $ - 4], true);
    if (trimmed.endsWith("-nts"))
        return ParsedDevelVersion(trimmed[0 .. $ - 4], false);
    return ParsedDevelVersion(trimmed, false);
}

unittest
{
    // TestParsePHPDevelCacheVersion
    {
        auto a = parsePHPDevelCacheVersion("8.4-nts");
        assert(a.version_ == "8.4" && !a.threadSafe);
        auto b = parsePHPDevelCacheVersion("8.4-zts");
        assert(b.version_ == "8.4" && b.threadSafe);
        auto c = parsePHPDevelCacheVersion("8.4");
        assert(c.version_ == "8.4" && !c.threadSafe);
    }

    // TestPatchPHPDevel & idempotency
    {
        auto tempDir = createTempDir("polka-devel-");
        scope (exit) rmdirRecurseQuiet(tempDir);
        auto scriptDir = buildPath(tempDir, "script");
        mkdirRecurse(scriptDir);

        auto rawPhpizeJS = [
            `var PHP_PREFIX="C:\\php"`,
            `var PHP_ZTS="No"`,
            `var VC_VERSION=1944`,
            `var PHP_VERSION=8`,
            `var PHP_MINOR_VERSION=4`,
            `var FSO = WScript.CreateObject("Scripting.FileSystemObject");`,
            `re = /\\script/i;`,
            `var PHP_DIR=FSO.GetParentFolderName(WScript.ScriptFullName).replace(re,"");`,
            ``,
            `var modules = "";`,
            `C.WriteLine("var PHP_PREFIX = " + '"' + PHP_PREFIX.replace(new RegExp('(["\\\\])', "g"), '\\$1') + '"');`,
        ].join("\r\n");

        auto rawConfigW32 = [
            `// configure script header`,
            `if (PHP_PREFIX == '') {`,
            "\tPHP_PREFIX = \"C:\\\\php\";",
            "\tif (PHP_DEBUG == \"yes\")",
            "\t\tPHP_PREFIX += \"\\\\debug\";",
            `}`,
            `DEFINE('PHP_PREFIX', PHP_PREFIX);`,
            `DEFINE("BASE_INCLUDES", "/I " + PHP_DIR + "/include");`,
        ].join("\r\n");

        auto phpizePath = buildPath(scriptDir, "phpize.js");
        auto configW32Path = buildPath(scriptDir, "config.w32.phpize.in");
        write(phpizePath, rawPhpizeJS);
        write(configW32Path, rawConfigW32);

        patchPHPDevel(tempDir);

        auto patchedPhpize = readText(phpizePath);
        assert(patchedPhpize.canFind(polkaPHPDevelPatchMarker));
        assert(patchedPhpize.canFind("var polkaEnvsDir = FSO.GetParentFolderName(FSO.GetParentFolderName(PHP_DIR));"));

        auto patchedW32 = readText(configW32Path);
        assert(patchedW32.canFind(polkaPHPDevelPatchMarker));
        assert(patchedW32.canFind(`if (PHP_PREFIX == '' || PHP_PREFIX == "C:\\php")`));

        // Idempotency
        patchPHPDevel(tempDir);
        assert(readText(phpizePath) == patchedPhpize);
        assert(readText(configW32Path) == patchedW32);
    }

    // TestPatchPHPDevelMissingDirectory
    {
        auto tempDir = createTempDir("polka-devel-missing-");
        scope (exit) rmdirRecurseQuiet(tempDir);
        patchPHPDevel(buildPath(tempDir, "does-not-exist"));
    }

    // TestPatchPHPDevelExecutableFilePath
    {
        auto tempDir = createTempDir("polka-devel-exe-");
        scope (exit) rmdirRecurseQuiet(tempDir);
        auto scriptDir = buildPath(tempDir, "script");
        mkdirRecurse(scriptDir);

        auto phpizeBat = buildPath(tempDir, "phpize.bat");
        write(phpizeBat, "@echo off\r\n");

        write(buildPath(scriptDir, "phpize.js"), "re = /\\\\script/i;\r\nvar PHP_DIR=FSO.GetParentFolderName(WScript.ScriptFullName).replace(re,\"\");\r\n");
        write(buildPath(scriptDir, "config.w32.phpize.in"), "if (PHP_PREFIX == '') {\r\n\tPHP_PREFIX = \"C:\\\\php\";\r\n}\r\nDEFINE('PHP_PREFIX', PHP_PREFIX);\r\n");

        patchPHPDevel(phpizeBat);
        assert(readText(buildPath(scriptDir, "phpize.js")).canFind(polkaPHPDevelPatchMarker));

        InstallContext ctx;
        ctx.result = InstallResult(PHPDevel, "8.4", "", phpizeBat);
        postInstallPHPDevel(ctx);
    }
}
