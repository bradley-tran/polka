module polka.tools.frankenphp;

import std.file : exists, isDir;
import std.format : format;
import std.path : buildPath, dirName;

import polka.config;
import polka.tools.ids;
import polka.tools.manifest;
import polka.tools.php;
import polka.tools.php_extensions;
import polka.tools.types;

/// Returns the FrankenPHP server plugin exposing both `frankenphp` and bundled `php` dispatch.
ToolPlugin frankenPHPPlugin() @safe
{
    PluginHooks hooks;
    hooks.postInstall = (InstallContext ctx) {
        configureInstalledFrankenPHP(ctx);
    };
    return newManifestPlugin(FrankenPHP, hooks);
}

/// Configures PHP OpenSSL/CA-bundle assets and `php.ini` alongside an installed FrankenPHP binary.
void configureInstalledFrankenPHP(const InstallContext ctx) @trusted
{
    auto installDir = dirName(ctx.result.targetPath);
    ensureInstalledPHPOpenSSLConfigAt(installDir);

    auto phpConfig = effectivePHPConfigForInstall(ctx.environment);
    if (phpConfigNeedsCABundle(phpConfig))
        phpConfig.caBundlePath = ensureInstalledPHPCABundle(ctx.envsDir, ctx.result.tool, ctx.result.version_);
    if (phpConfig.isZero())
        return;

    auto phpPath = resolveFrankenPHPPHPExecutable(installDir);
    configurePHPConfigAt(
        phpPath,
        buildPath(installDir, "ext"),
        buildPath(installDir, "php.ini"),
        phpConfig
    );
}

/// Locates the bundled PHP CLI shim or binary inside a FrankenPHP install directory.
string resolveFrankenPHPPHPExecutable(string installDir) @trusted
{
    foreach (name; ["php.exe", "php.cmd", "php.bat", "php"])
    {
        auto candidate = buildPath(installDir, name);
        if (exists(candidate) && !isDir(candidate))
            return candidate;
    }
    throw new Exception(format("frankenphp does not include a bundled PHP CLI in %s", installDir));
}

unittest
{
    import std.algorithm : canFind;
    import std.file : readText, write;
    import polka.support.fs : createTempDir, currentOS, getFileMode, octal, rmdirRecurseQuiet, setFileMode;
    import polka.tools.download_io : resolveManifestDownloadAsset;
    import polka.tools.post_install : writeManifestWrappers;

    // TestFrankenPHPPluginUsesConfiguredVersionAndManifest
    {
        auto plugin = frankenPHPPlugin();
        Environment env;
        env.tools = [FrankenPHP: "1.12"];
        assert(plugin.version_(env) == "1.12");
        assert(plugin.dispatchCommands() == [FrankenPHP, PHP]);
        assert(plugin.logs() == [LogEntry("serve.log", LogLevelDebug)]);
    }

    // TestFrankenPHPManifestWrapperUsesPHPCLISubcommand
    {
        auto installDir = createTempDir("polka-franken-wrap-");
        scope (exit) rmdirRecurseQuiet(installDir);
        write(buildPath(installDir, FrankenPHP), "binary");
        setFileMode(buildPath(installDir, FrankenPHP), octal!755);

        auto m = loadBuiltinManifest(FrankenPHP);
        writeManifestWrappers(installDir, m.wrappers, "linux", "amd64");
        auto p = buildPath(installDir, PHP);
        auto data = readText(p);
        assert(data.canFind(`frankenphp" php-cli "$@"`), data);
        version (Posix)
            assert((getFileMode(p) & octal!111) != 0);
    }

    // TestFrankenPHPManifestResolvesOfficialAssets
    {
        auto m = loadBuiltinManifest(FrankenPHP);
        auto win = resolveManifestDownloadAsset(FrankenPHP, m.download.assets, "1.12", "1.12.4", "v1.12.4", "windows", "amd64", null);
        assert(win.fileName == "frankenphp-windows-x86_64.zip" && win.archiveFormat == ArchiveFormat.zip);
        auto lin = resolveManifestDownloadAsset(FrankenPHP, m.download.assets, "1.12", "1.12.4", "v1.12.4", "linux", "amd64", null);
        assert(lin.fileName == "frankenphp-linux-x86_64" && lin.installPath == "frankenphp");
    }

    // TestFrankenPHPPostInstallMakesBinaryExecutable
    version (Posix)
    {
        auto dir = createTempDir("polka-franken-exec-");
        scope (exit) rmdirRecurseQuiet(dir);
        auto target = buildPath(dir, "frankenphp");
        write(target, "binary");
        setFileMode(target, octal!644);
        InstallContext execCtx;
        execCtx.result.targetPath = target;
        frankenPHPPlugin().postInstall(execCtx);
        assert((getFileMode(target) & octal!111) != 0);
    }

    // TestConfigureInstalledFrankenPHPWritesPHPIni
    {
        auto envsRoot = createTempDir("polka-franken-ini-");
        scope (exit) rmdirRecurseQuiet(envsRoot);
        import std.file : mkdirRecurse;
        auto installDir = buildPath(envsRoot, FrankenPHP, "1.12");
        mkdirRecurse(installDir);

        auto frankenTarget = buildPath(installDir, currentOS() == "windows" ? "frankenphp.exe" : "frankenphp");
        write(frankenTarget, "binary");
        setFileMode(frankenTarget, octal!755);

        if (currentOS() == "windows")
        {
            auto p = buildPath(installDir, "php.cmd");
            write(p, "@echo off\r\nif \"%1\"==\"-nm\" (\r\n  echo [PHP Modules]\r\n  echo Core\r\n  exit /b 0\r\n)\r\nexit /b 1\r\n");
            setFileMode(p, octal!755);
        }
        else
        {
            auto p = buildPath(installDir, "php");
            write(p, "#!/bin/sh\nif [ \"$1\" = \"-nm\" ]; then\n  printf '[PHP Modules]\\nCore\\n'\n  exit 0\nfi\nexit 1\n");
            setFileMode(p, octal!755);
        }

        InstallContext ctx;
        ctx.envsDir = envsRoot;
        ctx.environment.phpExtensions = ["mbstring": true];
        ctx.environment.opcachePreset = "dev";
        ctx.result = InstallResult(FrankenPHP, "1.12", "", frankenTarget);
        configureInstalledFrankenPHP(ctx);

        auto ini = readText(buildPath(installDir, "php.ini"));
        assert(ini.canFind(`extension_dir="ext"`), ini);
        assert(ini.canFind("extension=mbstring"), ini);
        assert(ini.canFind("opcache.enable=1"), ini);
    }
}
