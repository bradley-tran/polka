module polka.tools.downloader;

import std.format : format;

import polka.support.http : HTTPClient;
import polka.tools.download_io : effectiveHTTPClient;
import polka.tools.plugins : newDefaultRegistry;
import polka.tools.registry : Registry;
import polka.tools.types : DownloadContext, Downloader, ToolPlugin;

/// Production `Downloader` dispatching downloads via registered `ToolPlugin` hooks.
class HTTPDownloader : Downloader
{
    HTTPClient client;
    Registry plugins;

    /// Constructs an `HTTPDownloader` with optional custom HTTP client and plugin registry.
    this(HTTPClient client = null, Registry plugins = null) @safe pure nothrow
    {
        this.client = client;
        this.plugins = plugins;
    }

    /// Downloads and caches `tool` at `ver` into `cacheDir`.
    void download(string cacheDir, string tool, string ver)
    {
        auto effClient = effectiveHTTPClient(client);
        auto registry = plugins !is null ? plugins : newDefaultRegistry();

        ToolPlugin plugin;
        if (!registry.plugin(tool, plugin))
            throw new Exception(format(`unsupported tool "%s"`, tool));

        plugin.download(DownloadContext(effClient, cacheDir, tool, ver));
    }
}

unittest
{
    import std.algorithm : canFind;
    import std.array : replicate;
    import std.file : exists, read;
    import std.path : buildPath;
    import polka.config : Environment;
    import polka.support.archive : ArchiveEntrySpec, buildTarGzBytes, buildTarXZBytes, buildZipBytes;
    import polka.support.fs : createTempDir, fromSlashPath, rmdirRecurseQuiet;
    import polka.support.http : HTTPResponse, MockHTTPClient;
    import polka.tools.apache : apacheLoungeDownloadURL, resolveApacheDownloadAsset;
    import polka.tools.cache : cachedToolPayload, installCachedToolPayload, payloadKindArchive, readToolCacheMetadata;
    import polka.tools.checksum : checksumBytes;
    import polka.tools.download_io : downloadDatabaseAsset, githubAPIBaseURL;
    import polka.tools.ids;
    import polka.tools.manifest : loadBuiltinManifest;
    import polka.tools.manifest_request : downloadManifestRequest, resolveManifestRequest;
    import polka.tools.registry : newRegistry;
    import polka.tools.sqlite : resolveSQLiteDownloadAsset, sqliteDownloadPageURL;
    import polka.tools.types;

    // Helper: loads a built-in manifest with an overridden release-index URL for offline tests.
    static PluginManifest loadManifestWithReleaseIndex(string tool, string indexURL)
    {
        auto m = loadBuiltinManifest(tool);
        m.download.releaseIndex.url = indexURL;
        return m;
    }

    class MockHookPlugin : ToolPlugin
    {
        string idVal;
        void delegate(DownloadContext) hook;
        this(string idVal, void delegate(DownloadContext) hook) { this.idVal = idVal; this.hook = hook; }
        string id() const { return idVal; }
        string version_(const Environment) const { return ""; }
        bool[string] phpExtensions() const { return null; }
        void validate(const Environment) const {}
        string[] installCandidates(string, string) const { return null; }
        string[] dispatchCommands() const { return null; }
        string[] cleanupCommands() const { return null; }
        string[] activeCommands(const Environment) const { return null; }
        string[] dispatchCandidates(string, string, string) const { return null; }
        LogEntry[] logs() const { return null; }
        void download(DownloadContext ctx) const { hook(ctx); }
        void postInstall(InstallContext) const {}
    }

    // TestHTTPDownloaderRunsRegisteredPluginDownloadHook
    {
        DownloadContext called;
        auto reg = newRegistry(new MockHookPlugin("demo", (DownloadContext ctx) {
            called = ctx;
        }));
        (new HTTPDownloader(null, reg)).download("cache-root", "demo", "1.2.3");
        assert(called.client !is null && called.cacheDir == "cache-root" && called.tool == "demo" && called.version_ == "1.2.3");
    }

    // TestHTTPDownloaderReturnsPluginDownloadError
    {
        auto reg = newRegistry(new MockHookPlugin("demo", (DownloadContext ctx) {
            throw new Exception("download failed");
        }));
        bool threw = false;
        try { (new HTTPDownloader(null, reg)).download("cache-root", "demo", "1.2.3"); }
        catch (Exception e) { threw = e.msg == "download failed"; }
        assert(threw);
    }

    // TestResolveDatabaseDownloadAssetSupportsSeriesLabels
    {
        auto dbClient = new MockHTTPClient((string url, string[string] hdrs) {
            if (url.canFind("/mysql/8.4.html"))
                return HTTPResponse(200, null, cast(ubyte[]) "<a href=\"mysql-8.4.8-winx64.zip\">m</a>\n<a href=\"mysql-8.4.9-winx64.zip\">m</a>\n");
            if (url.canFind("/mariadb/"))
                return HTTPResponse(200, null, cast(ubyte[]) "<a href=\"mariadb-11.4.10/\">m</a>\n<a href=\"mariadb-11.4.11/\">m</a>\n<a href=\"mariadb-11.8.7/\">m</a>\n");
            return HTTPResponse(404, null, null);
        });
        auto manifests = [
            MySQL: loadManifestWithReleaseIndex(MySQL, "https://mock.db/mysql/{requested}.html"),
            MariaDB: loadManifestWithReleaseIndex(MariaDB, "https://mock.db/mariadb/"),
        ];
        struct DBCase { string tool; string ver; string goos; string wantVer; string wantFile; ChecksumAlgorithm wantAlg; }
        foreach (tc; [
            DBCase(MySQL, "8.4", "windows", "8.4.9", "mysql-8.4.9-winx64.zip", ChecksumAlgorithm.md5),
            DBCase(MySQL, "8.4", "linux", "8.4.9", "mysql-8.4.9-linux-glibc2.17-x86_64.tar.xz", ChecksumAlgorithm.md5),
            DBCase(MariaDB, "11.4", "windows", "11.4.11", "mariadb-11.4.11-winx64.zip", ChecksumAlgorithm.sha256),
            DBCase(MariaDB, "11.4", "linux", "11.4.11", "mariadb-11.4.11-linux-systemd-x86_64.tar.gz", ChecksumAlgorithm.sha256),
            DBCase(MariaDB, "11.8", "windows", "11.8.7", "mariadb-11.8.7-winx64.zip", ChecksumAlgorithm.sha256),
            DBCase(MariaDB, "11.8", "linux", "11.8.7", "mariadb-11.8.7-linux-systemd-x86_64.tar.gz", ChecksumAlgorithm.sha256),
        ])
        {
            auto plan = resolveManifestRequest(dbClient, manifests[tc.tool], tc.ver, tc.goos, "amd64");
            assert(plan.resolvedVersion == tc.wantVer);
            assert(plan.asset.fileName == tc.wantFile);
            assert(plan.asset.checksumAlgorithm == tc.wantAlg);
        }
    }

    // TestResolveSQLiteDownloadAssetSupportsSeriesLabels
    {
        auto origURL = sqliteDownloadPageURL;
        scope (exit) sqliteDownloadPageURL = origURL;
        sqliteDownloadPageURL = "https://mock.sqlite";

        auto sqClient = new MockHTTPClient((string url, string[string] hdrs) {
            return HTTPResponse(200, null, cast(ubyte[]) (
                "PRODUCT,3.53.2,2026/sqlite-tools-linux-x64-3530200.zip,4262741," ~ replicate("a", 64) ~ "\n" ~
                "PRODUCT,3.53.2,2026/sqlite-tools-win-x64-3530200.zip,6556897," ~ replicate("b", 64) ~ "\n"
            ));
        });

        string rVer;
        DownloadAsset asset;
        resolveSQLiteDownloadAsset(sqClient, "3.53", "windows", "amd64", rVer, asset);
        assert(rVer == "3.53.2" && asset.fileName == "sqlite-tools-win-x64-3530200.zip" && asset.checksumAlgorithm == ChecksumAlgorithm.sha3_256);

        resolveSQLiteDownloadAsset(sqClient, "3.53", "linux", "amd64", rVer, asset);
        assert(rVer == "3.53.2" && asset.fileName == "sqlite-tools-linux-x64-3530200.zip" && asset.checksumAlgorithm == ChecksumAlgorithm.sha3_256);
    }

    // TestResolveNginxDownloadAssetSupportsSeriesLabels
    {
        auto nxClient = new MockHTTPClient((string url, string[string] hdrs) {
            return HTTPResponse(200, null, cast(ubyte[]) (
                "<a href=\"nginx-1.30.1.zip\">nginx-1.30.1.zip</a>\n" ~
                "<a href=\"nginx-1.30.2.zip\">nginx-1.30.2.zip</a>\n" ~
                "<a href=\"nginx-1.28.3.zip\">nginx-1.28.3.zip</a>\n"
            ));
        });
        auto m = loadManifestWithReleaseIndex(Nginx, "https://mock.nginx");
        auto p1 = resolveManifestRequest(nxClient, m, "1.30", "windows", "amd64");
        assert(p1.resolvedVersion == "1.30.2" && p1.asset.fileName == "nginx-1.30.2.zip" && p1.asset.checksumAlgorithm == ChecksumAlgorithm.none);
        auto p2 = resolveManifestRequest(nxClient, m, "1.28", "windows", "amd64");
        assert(p2.resolvedVersion == "1.28.3" && p2.asset.fileName == "nginx-1.28.3.zip");
    }

    // TestResolveApacheDownloadAssetSupportsApacheLoungeSeriesLabels
    {
        auto origAL = apacheLoungeDownloadURL;
        scope (exit) apacheLoungeDownloadURL = origAL;
        apacheLoungeDownloadURL = "https://mock.apache";

        auto apClient = new MockHTTPClient((string url, string[string] hdrs) {
            return HTTPResponse(200, null, cast(ubyte[]) (
                "<a href=\"httpd-2.4.67-260101-Win64-VS17.zip\">older</a>\n" ~
                "<a href=\"/download/VS18/binaries/httpd-2.4.68-260610-Win64-VS18.zip\">new</a>\n" ~
                "<a href=\"/download/VS18/binaries/httpd-2.4.68-260617-Win64-VS18.zip\">newer-build</a>\n" ~
                "<a href=\"httpd-2.4.68-260617-Win32-VS18.zip\">wrong-arch</a>\n"
            ));
        });
        string rVer;
        DownloadAsset apAsset;
        resolveApacheDownloadAsset(apClient, "2.4", "windows", "amd64", rVer, apAsset);
        assert(rVer == "2.4.68" && apAsset.fileName == "httpd-2.4.68-260617-Win64-VS18.zip");
        auto wantURL = "https://mock.apache/download/VS18/binaries/httpd-2.4.68-260617-Win64-VS18.zip";
        assert(apAsset.url == wantURL && apAsset.checksumURL == wantURL ~ ".txt");
        assert(apAsset.checksumAlgorithm == ChecksumAlgorithm.sha256 && apAsset.archiveFormat == ArchiveFormat.zip);
    }

    // TestResolveNodeJSDownloadAssetSupportsSeriesLabels
    {
        auto nodeClient = new MockHTTPClient((string url, string[string] hdrs) {
            return HTTPResponse(200, null, cast(ubyte[]) `[{"version":"v24.16.0"},{"version":"v24.15.0"},{"version":"v22.22.3"}]`);
        });
        auto m = loadManifestWithReleaseIndex(NodeJS, "https://mock.node/index.json");
        struct NodeCase { string ver; string goos; string wantVer; string wantFile; }
        foreach (tc; [
            NodeCase("24", "windows", "24.16.0", "node-v24.16.0-win-x64.zip"),
            NodeCase("24", "linux", "24.16.0", "node-v24.16.0-linux-x64.tar.xz"),
            NodeCase("22", "windows", "22.22.3", "node-v22.22.3-win-x64.zip"),
            NodeCase("22", "linux", "22.22.3", "node-v22.22.3-linux-x64.tar.xz"),
            NodeCase("v22.22.3", "windows", "22.22.3", "node-v22.22.3-win-x64.zip"),
        ])
        {
            auto plan = resolveManifestRequest(nodeClient, m, tc.ver, tc.goos, "amd64");
            assert(plan.resolvedVersion == tc.wantVer && plan.asset.fileName == tc.wantFile);
            assert(plan.asset.checksumAlgorithm == ChecksumAlgorithm.sha256);
        }
    }

    // GitHub releases tests: Mailpit, Traefik, Meilisearch, Mago, RabbitMQ constraint, PHP-SDK tag mode, EmbeddedCommitID
    {
        auto origGH = githubAPIBaseURL;
        scope (exit) githubAPIBaseURL = origGH;
        githubAPIBaseURL = "https://mock.github";

        // Mailpit
        auto mailClient = new MockHTTPClient((string url, string[string] hdrs) {
            return HTTPResponse(200, null, cast(ubyte[]) (`[
                {"tag_name":"v1.30.1","assets":[
                    {"name":"mailpit-windows-amd64.zip","digest":"sha256:` ~ replicate("c", 64) ~ `"},
                    {"name":"mailpit-linux-amd64.tar.gz","digest":"sha256:` ~ replicate("d", 64) ~ `"}
                ]},
                {"tag_name":"v1.30.0","assets":[]}
            ]`));
        });
        auto mailM = loadBuiltinManifest(Mailpit);
        auto mailWin = resolveManifestRequest(mailClient, mailM, "1.30", "windows", "amd64");
        assert(mailWin.resolvedVersion == "1.30.1" && mailWin.asset.fileName == "mailpit-1.30.1-windows-amd64.zip" && mailWin.asset.archiveFormat == ArchiveFormat.zip);
        assert(mailWin.asset.checksumAlgorithm == ChecksumAlgorithm.sha256 && mailWin.asset.checksum.length > 0);
        auto mailLin = resolveManifestRequest(mailClient, mailM, "1.30", "linux", "amd64");
        assert(mailLin.resolvedVersion == "1.30.1" && mailLin.asset.fileName == "mailpit-1.30.1-linux-amd64.tar.gz" && mailLin.asset.archiveFormat == ArchiveFormat.tarGz);

        // Traefik
        auto traClient = new MockHTTPClient((string url, string[string] hdrs) {
            return HTTPResponse(200, null, cast(ubyte[]) (`[
                {"tag_name":"v3.3.1","assets":[
                    {"name":"traefik_v3.3.1_windows_amd64.zip","digest":"sha256:` ~ replicate("e", 64) ~ `"},
                    {"name":"traefik_v3.3.1_linux_amd64.tar.gz","digest":"sha256:` ~ replicate("f", 64) ~ `"}
                ]},
                {"tag_name":"v3.3.0","assets":[]}
            ]`));
        });
        auto traM = loadBuiltinManifest(Traefik);
        auto traWin = resolveManifestRequest(traClient, traM, "3.3", "windows", "amd64");
        assert(traWin.resolvedVersion == "3.3.1" && traWin.asset.fileName == "traefik-3.3.1-windows-amd64.zip" && traWin.asset.archiveFormat == ArchiveFormat.zip);
        auto traLin = resolveManifestRequest(traClient, traM, "3.3", "linux", "amd64");
        assert(traLin.resolvedVersion == "3.3.1" && traLin.asset.fileName == "traefik-3.3.1-linux-amd64.tar.gz" && traLin.asset.archiveFormat == ArchiveFormat.tarGz);

        // Meilisearch direct binaries
        auto meiliClient = new MockHTTPClient((string url, string[string] hdrs) {
            return HTTPResponse(200, null, cast(ubyte[]) (`[{
                "tag_name":"v1.48.3",
                "assets":[
                    {"name":"meilisearch-windows-amd64.exe","digest":"sha256:` ~ replicate("a", 64) ~ `"},
                    {"name":"meilisearch-linux-amd64","digest":"sha256:` ~ replicate("b", 64) ~ `"}
                ]
            }]`));
        });
        auto meiliM = loadBuiltinManifest(Meilisearch);
        auto meiliWin = resolveManifestRequest(meiliClient, meiliM, "1.48", "windows", "amd64");
        assert(meiliWin.resolvedVersion == "1.48.3" && meiliWin.asset.fileName == "meilisearch-windows-amd64.exe" && meiliWin.asset.installPath == "meilisearch.exe" && meiliWin.asset.archiveFormat == ArchiveFormat.none);
        auto meiliLin = resolveManifestRequest(meiliClient, meiliM, "1.48", "linux", "amd64");
        assert(meiliLin.resolvedVersion == "1.48.3" && meiliLin.asset.fileName == "meilisearch-linux-amd64" && meiliLin.asset.installPath == "meilisearch" && meiliLin.asset.archiveFormat == ArchiveFormat.none);

        // Mago
        auto magoClient = new MockHTTPClient((string url, string[string] hdrs) {
            return HTTPResponse(200, null, cast(ubyte[]) (`[
                {"tag_name":"1.27.0","assets":[
                    {"name":"mago-1.27.0-x86_64-pc-windows-msvc.zip","digest":"sha256:` ~ replicate("e", 64) ~ `"},
                    {"name":"mago-1.27.0-x86_64-unknown-linux-gnu.tar.gz","digest":"sha256:` ~ replicate("f", 64) ~ `"}
                ]},
                {"tag_name":"1.27.1-RC1","prerelease":true,"assets":[]}
            ]`));
        });
        auto magoM = loadBuiltinManifest(Mago);
        auto magoWin = resolveManifestRequest(magoClient, magoM, "1.27", "windows", "amd64");
        assert(magoWin.resolvedVersion == "1.27.0" && magoWin.asset.fileName == "mago-1.27.0-x86_64-pc-windows-msvc.zip" && magoWin.asset.archiveFormat == ArchiveFormat.zip);
        auto magoLin = resolveManifestRequest(magoClient, magoM, "1.27", "linux", "amd64");
        assert(magoLin.resolvedVersion == "1.27.0" && magoLin.asset.fileName == "mago-1.27.0-x86_64-unknown-linux-gnu.tar.gz" && magoLin.asset.archiveFormat == ArchiveFormat.tarGz);

        // TestResolveManifestRequestGitHubTagModeSkipsReleasesEndpoint
        auto sdkCommit = "0123456789abcdef0123456789abcdef01234567";
        bool releasesCalled = false;
        auto sdkClient = new MockHTTPClient((string url, string[string] hdrs) {
            if (url.canFind("/releases"))
            {
                releasesCalled = true;
                return HTTPResponse(500, null, null);
            }
            if (url.canFind("/commits/php-sdk-" ~ DefaultPHPSDKVersion))
                return HTTPResponse(200, null, cast(ubyte[]) (`{"sha":"` ~ sdkCommit ~ `"}`));
            return HTTPResponse(404, null, null);
        });
        auto sdkPlan = resolveManifestRequest(sdkClient, loadBuiltinManifest(PHPSDK), DefaultPHPSDKVersion, "windows", "amd64");
        assert(!releasesCalled);
        assert(sdkPlan.resolvedVersion == DefaultPHPSDKVersion && sdkPlan.tag == "php-sdk-" ~ DefaultPHPSDKVersion && sdkPlan.commit == sdkCommit);

        // TestResolveManifestRequestEnforcesVersionConstraintAfterResolution
        auto rmqClient = new MockHTTPClient((string url, string[string] hdrs) {
            return HTTPResponse(200, null, cast(ubyte[]) (`[{"tag_name":"v4.0.2","assets":[{"name":"rabbitmq-server-windows-4.0.2.zip","digest":"sha256:` ~ replicate("a", 64) ~ `"}]}]`));
        });
        bool sawConstraint = false;
        try { resolveManifestRequest(rmqClient, loadBuiltinManifest(RabbitMQ), "4.0", "windows", "amd64"); }
        catch (Exception e) { sawConstraint = e.msg.canFind("requires >= 4.0.4 and < 5"); }
        assert(sawConstraint);

        // TestDownloadManifestRequestVerifiesEmbeddedCommitID
        auto matchingZip = buildZipBytes([ArchiveEntrySpec("php-sdk-binary-tools-php-sdk-2.8.3/phpsdk-starter.bat", cast(const(ubyte)[]) "@echo off\r\n")], sdkCommit);
        auto mismatchedZip = buildZipBytes([ArchiveEntrySpec("php-sdk-binary-tools-php-sdk-2.8.3/phpsdk-starter.bat", cast(const(ubyte)[]) "@echo off\r\n")], replicate("f", 40));
        bool serveMismatch = false;
        auto commitArchiveClient = new MockHTTPClient((string url, string[string] hdrs) {
            if (url.canFind("/commits/php-sdk-2.8.3"))
                return HTTPResponse(200, null, cast(ubyte[]) (`{"sha":"` ~ sdkCommit ~ `"}`));
            if (url.canFind("/archive/php-sdk-2.8.3.zip"))
                return HTTPResponse(200, null, serveMismatch ? mismatchedZip.dup : matchingZip.dup);
            return HTTPResponse(404, null, null);
        });
        auto sdkM = loadBuiltinManifest(PHPSDK);
        auto sdkAsset = sdkM.download.assets["windows-amd64"];
        sdkAsset.url = "https://mock.archive/archive/{tag}.zip";
        sdkM.download.assets = ["all": sdkAsset];

        auto sdkCache1 = createTempDir("polka-sdk-c1-");
        scope (exit) rmdirRecurseQuiet(sdkCache1);
        downloadManifestRequest(DownloadContext(commitArchiveClient, sdkCache1, PHPSDK, "2.8.3"), sdkM);
        assert(cachedToolPayload(sdkCache1, PHPSDK, "2.8.3").downloadedVersion == "2.8.3");

        serveMismatch = true;
        auto sdkCache2 = createTempDir("polka-sdk-c2-");
        scope (exit) rmdirRecurseQuiet(sdkCache2);
        bool sawCommitMismatch = false;
        try { downloadManifestRequest(DownloadContext(commitArchiveClient, sdkCache2, PHPSDK, "2.8.3"), sdkM); }
        catch (Exception e) { sawCommitMismatch = e.msg.canFind("archive commit"); }
        assert(sawCommitMismatch);
    }

    // TestResolvePHPMyAdminDownloadAssetSupportsSeriesLabels
    {
        auto pmaClient = new MockHTTPClient((string url, string[string] hdrs) {
            return HTTPResponse(200, null, cast(ubyte[]) "<a href=\"phpMyAdmin-5.2.2-all-languages.zip\">old</a>\n<a href=\"phpMyAdmin-5.2.3-all-languages.zip\">new</a>\n");
        });
        auto m = loadManifestWithReleaseIndex(PHPMyAdmin, "https://mock.pma");
        auto plan = resolveManifestRequest(pmaClient, m, "5.2", "windows", "amd64");
        assert(plan.resolvedVersion == "5.2.3" && plan.asset.fileName == "phpMyAdmin-5.2.3-all-languages.zip");
        assert(plan.asset.archiveFormat == ArchiveFormat.zip && plan.asset.checksumAlgorithm == ChecksumAlgorithm.sha256);
    }

    // TestDownloadManifestFileAssetSupportsSeriesLabelsAndVerifiesDigest
    {
        auto origGH = githubAPIBaseURL;
        scope (exit) githubAPIBaseURL = origGH;
        githubAPIBaseURL = "https://mock.github";

        auto payload = cast(const(ubyte)[]) "manifest file payload\n";
        auto sum = checksumBytes(ChecksumAlgorithm.sha256, payload);
        auto pieClient = new MockHTTPClient((string url, string[string] hdrs) {
            if (url.canFind("/repos/php/pie/releases"))
                return HTTPResponse(200, null, cast(ubyte[]) (`[
                    {"tag_name":"1.4.5","assets":[{"name":"pie.phar","digest":"sha256:` ~ sum ~ `"}]},
                    {"tag_name":"1.4.6-RC1","prerelease":true,"assets":[{"name":"pie.phar"}]}
                ]`));
            if (url.canFind("/downloads/1.4.5/pie.phar"))
                return HTTPResponse(200, null, payload.dup);
            return HTTPResponse(404, null, null);
        });
        PluginManifest pieM;
        pieM.id = PIE;
        pieM.download.github = ManifestGitHubDownload("php", "pie", "", "");
        DownloadAsset pieAsset;
        pieAsset.fileName = "pie.phar";
        pieAsset.url = "https://mock.dl/downloads/{tag}/pie.phar";
        pieAsset.installPath = "bin/pie.phar";
        pieAsset.checksumAlgorithm = ChecksumAlgorithm.sha256;
        pieM.download.assets = ["all": pieAsset];

        auto cacheDir = createTempDir("polka-pie-cache-");
        scope (exit) rmdirRecurseQuiet(cacheDir);
        downloadManifestRequest(DownloadContext(pieClient, cacheDir, PIE, "1.4"), pieM);
        auto cached = cachedToolPayload(cacheDir, PIE, "1.4");
        assert(cast(ubyte[]) read(cached.payloadPath) == payload);
        auto meta = readToolCacheMetadata(buildPath(cacheDir, PIE), PIE);
        auto entry = meta.versions["1.4"];
        assert(entry.downloadedVersion == "1.4.5" && entry.installPath == "bin/pie.phar" && entry.checksum == sum);
    }

    // TestDownloadDatabaseAssetCachesSupportedArchives
    {
        struct ArchCase { string ver; string fileName; ArchiveFormat fmt; ChecksumAlgorithm alg; ubyte[] data; string expectedPath; }
        foreach (tc; [
            ArchCase("8.4", "mysql-8.4.9-winx64.zip", ArchiveFormat.zip, ChecksumAlgorithm.md5,
                buildZipBytes([ArchiveEntrySpec("mysql-8.4.9-winx64/bin/mysql.exe", cast(const(ubyte)[]) "mysql")]),
                buildPath("bin", "mysql.exe")),
            ArchCase("11.4", "mariadb-11.4.11-linux-systemd-x86_64.tar.gz", ArchiveFormat.tarGz, ChecksumAlgorithm.sha256,
                buildTarGzBytes([ArchiveEntrySpec("mariadb-11.4.11-linux-systemd-x86_64/bin/mariadb", cast(const(ubyte)[]) "mariadb")]),
                buildPath("bin", "mariadb")),
            ArchCase("8.4", "mysql-8.4.9-linux-glibc2.17-x86_64.tar.xz", ArchiveFormat.tarXz, ChecksumAlgorithm.md5,
                buildTarXZBytes([ArchiveEntrySpec("mysql-8.4.9-linux-glibc2.17-x86_64/bin/mysql", cast(const(ubyte)[]) "mysql")]),
                buildPath("bin", "mysql")),
        ])
        {
            auto dlClient = new MockHTTPClient((string url, string[string] hdrs) {
                return HTTPResponse(200, null, tc.data.dup);
            });
            auto cacheDir = createTempDir("polka-arch-dl-");
            scope (exit) rmdirRecurseQuiet(cacheDir);

            DownloadAsset asset;
            asset.fileName = tc.fileName;
            asset.url = "https://mock.arch/" ~ tc.fileName;
            asset.checksum = checksumBytes(tc.alg, tc.data);
            asset.checksumAlgorithm = tc.alg;
            asset.archiveFormat = tc.fmt;

            downloadDatabaseAsset(dlClient, cacheDir, MySQL, tc.ver, asset);
            auto cached = cachedToolPayload(cacheDir, MySQL, tc.ver);
            assert(cached.payloadKind == payloadKindArchive);
            assert(!exists(buildPath(cacheDir, MySQL, tc.ver, tc.expectedPath)));

            auto targetDir = createTempDir("polka-arch-inst-");
            scope (exit) rmdirRecurseQuiet(targetDir);
            installCachedToolPayload(cacheDir, targetDir, MySQL, tc.ver);
            assert(exists(buildPath(targetDir, tc.expectedPath)));
        }
    }

    // TestDownloadDatabaseAssetMetadataTracksMultipleVersions
    {
        auto zipData = buildZipBytes([ArchiveEntrySpec("mysql-test/bin/mysql.exe", cast(const(ubyte)[]) "mysql")]);
        auto cl = new MockHTTPClient((string url, string[string] hdrs) {
            return HTTPResponse(200, null, zipData.dup);
        });
        auto cacheDir = createTempDir("polka-multi-ver-");
        scope (exit) rmdirRecurseQuiet(cacheDir);

        foreach (ver; ["8.3", "8.4"])
        {
            DownloadAsset asset;
            asset.fileName = "mysql-" ~ ver ~ ".zip";
            asset.url = "https://mock.arch/mysql-" ~ ver ~ ".zip";
            asset.checksum = checksumBytes(ChecksumAlgorithm.sha256, zipData);
            asset.checksumAlgorithm = ChecksumAlgorithm.sha256;
            asset.archiveFormat = ArchiveFormat.zip;
            downloadDatabaseAsset(cl, cacheDir, MySQL, ver, asset);
        }

        auto meta = readToolCacheMetadata(buildPath(cacheDir, MySQL), MySQL);
        foreach (ver; ["8.3", "8.4"])
        {
            assert(ver in meta.versions);
            auto entry = meta.versions[ver];
            assert(entry.downloadedVersion == ver && entry.payloadKind == payloadKindArchive && entry.archiveFormat == ArchiveFormat.zip);
            assert(exists(buildPath(cacheDir, MySQL, fromSlashPath(entry.payloadPath))));
        }
    }
}
