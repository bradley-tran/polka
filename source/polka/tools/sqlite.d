module polka.tools.sqlite;

import std.algorithm : canFind, startsWith;
import std.conv : to;
import std.format : format;
import std.path : baseName;
import std.string : replace, split, strip;

import polka.support.fs : currentArch, currentOS;
import polka.support.http : HTTPClient;
import polka.tools.download_io;
import polka.tools.ids;
import polka.tools.manifest;
import polka.tools.types;

/// Official SQLite downloads index page URL (overridable in unit tests).
package string sqliteDownloadPageURL = "https://www.sqlite.org/download.html";

/// Parsed `PRODUCT,...` csv line from SQLite's download page HTML comments.
struct SQLiteDownloadProduct
{
    string version_;
    string relativeURL;
    string sha3;
}

/// Returns the SQLite `ToolPlugin` backed by its manifest and `PRODUCT,` HTML index scraper.
ToolPlugin sqlitePlugin() @safe
{
    PluginHooks hooks;
    hooks.download = (DownloadContext ctx) {
        downloadSQLite(ctx.client, ctx.cacheDir, ctx.version_);
    };
    return newManifestPlugin(SQLite, hooks);
}

/// Resolves and downloads SQLite CLI tools for the current platform into cache.
void downloadSQLite(HTTPClient client, string cacheDir, string ver) @trusted
{
    string resolvedVersion;
    DownloadAsset asset;
    resolveSQLiteDownloadAsset(client, ver, currentOS(), currentArch(), resolvedVersion, asset);
    downloadManifestAsset(client, cacheDir, SQLite, ver, resolvedVersion, asset);
}

/// Resolves SQLite product metadata and renders its download asset with embedded SHA3-256 checksum.
void resolveSQLiteDownloadAsset(
    HTTPClient client,
    string requestedVersion,
    string goos,
    string goarch,
    out string resolvedVersion,
    out DownloadAsset asset
) @trusted
{
    auto product = resolveSQLiteDownloadProduct(client, requestedVersion, platformKey(goos, goarch));
    auto manifest = loadBuiltinManifest(SQLite);
    asset = resolveManifestDownloadAsset(
        SQLite,
        manifest.download.assets,
        requestedVersion,
        product.version_,
        product.version_,
        goos,
        goarch,
        [
            "sqlite_numeric": sqliteNumericVersion(product.version_),
            "sqlite_relative_url": product.relativeURL,
        ]
    );
    asset.checksum = product.sha3;
    resolvedVersion = product.version_;
}

/// Downloads `sqliteDownloadPageURL` and finds the newest product entry matching `requested` and `platform`.
SQLiteDownloadProduct resolveSQLiteDownloadProduct(HTTPClient client, string requested, string platform) @trusted
{
    auto trimmed = requested.strip();
    if (trimmed.length == 0)
        throw new Exception("sqlite version cannot be empty");

    auto page = downloadText(client, sqliteDownloadPageURL, "sqlite download page");
    auto products = parseSQLiteDownloadProducts(page);
    auto sourceFileName = sqliteSourceFileNameForPlatform(platform);
    if (sourceFileName.length == 0)
        throw new Exception("sqlite is not available for " ~ platform);

    bool found = false;
    SQLiteDownloadProduct best;
    foreach (product; products)
    {
        if (!baseName(product.relativeURL).canFind(sourceFileName))
            continue;
        if (!versionMatchesRequest(product.version_, trimmed))
            continue;
        if (!found || compareVersions(product.version_, best.version_) > 0)
        {
            best = product;
            found = true;
        }
    }
    if (!found)
        throw new Exception(format(`resolve sqlite version "%s": no matching release found`, trimmed));
    return best;
}

/// Extracts `PRODUCT,<ver>,<relURL>,<size>,<sha3>` records from SQLite's download page text.
SQLiteDownloadProduct[] parseSQLiteDownloadProducts(string page) @safe pure
{
    SQLiteDownloadProduct[] products;
    foreach (rawLine; page.split('\n'))
    {
        auto line = rawLine.strip();
        if (!line.startsWith("PRODUCT,"))
            continue;
        auto fields = line.split(',');
        if (fields.length < 5)
            continue;
        products ~= SQLiteDownloadProduct(
            fields[1].strip(),
            fields[2].strip(),
            fields[4].strip()
        );
    }
    return products;
}

private string sqliteSourceFileNameForPlatform(string platform) @safe pure
{
    switch (platform.strip())
    {
        case "windows-amd64":
            return "sqlite-tools-win-x64-";
        case "linux-amd64":
            return "sqlite-tools-linux-x64-";
        default:
            return "";
    }
}

/// Formats dotted `M.mm.pp` into SQLite's 7-digit archive number (e.g. `3.53.2` -> `3530200`).
string sqliteNumericVersion(string ver) @safe pure
{
    auto trimmed = ver.strip();
    auto parts = trimmed.split('.');
    if (parts.length < 3)
        return trimmed.replace(".", "");
    try
    {
        auto major = to!int(parts[0]);
        auto minor = to!int(parts[1]);
        auto patch = to!int(parts[2]);
        return format("%d%02d%02d00", major, minor, patch);
    }
    catch (Exception)
    {
        return trimmed.replace(".", "");
    }
}

unittest
{
    assert(sqliteNumericVersion("3.53.2") == "3530200");
}
