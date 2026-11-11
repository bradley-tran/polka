module polka.tools.database;

import std.format : format;

import polka.support.http : HTTPClient;
import polka.tools.download_io;
import polka.tools.ids;
import polka.tools.manifest;
import polka.tools.postgresql;
import polka.tools.types;

/// Resolves database-specific download metadata when custom resolution is required (currently PostgreSQL).
void resolveDatabaseDownloadAsset(
    HTTPClient client,
    string tool,
    string requestedVersion,
    string goos,
    string goarch,
    out string resolvedVersion,
    out DownloadAsset asset
) @trusted
{
    if (tool != PostgreSQL)
        throw new Exception(format(`unsupported database tool "%s"`, tool));

    resolvedVersion = resolvePostgreSQLReleaseVersion(client, requestedVersion);
    auto manifest = loadBuiltinManifest(tool);
    asset = resolveManifestDownloadAsset(
        tool,
        manifest.download.assets,
        requestedVersion,
        resolvedVersion,
        resolvedVersion,
        goos,
        goarch,
        ["maven_version": postgreSQLMavenVersion(resolvedVersion)]
    );
}

unittest
{
    import std.algorithm : canFind;
    import polka.support.http : HTTPResponse, MockHTTPClient;

    // TestResolvePostgreSQLDownloadAssets
    auto client = new MockHTTPClient((string url, string[string] hdrs) => HTTPResponse(404, null, null));
    struct Case { string goos; string fileName; ArchiveFormat fmt; }
    foreach (tc; [
        Case("windows", "postgresql-17.6-1-windows-x64-binaries.zip", ArchiveFormat.zip),
        Case("linux", "embedded-postgres-binaries-linux-amd64-17.6.0.jar", ArchiveFormat.zip),
    ])
    {
        string resolved;
        DownloadAsset asset;
        resolveDatabaseDownloadAsset(client, PostgreSQL, "17.6", tc.goos, "amd64", resolved, asset);
        assert(resolved == "17.6");
        assert(asset.fileName == tc.fileName, asset.fileName);
        assert(asset.archiveFormat == tc.fmt);
        if (tc.goos == "windows")
            assert(asset.url.canFind("/postgresql/" ~ tc.fileName));
        else
            assert(asset.url.canFind("/embedded-postgres-binaries-linux-amd64/"));
    }
}
