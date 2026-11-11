module polka.tools.postgresql;

import std.algorithm : canFind;
import std.file : exists, isDir;
import std.format : format;
import std.json;
import std.path : buildPath;
import std.string : strip;

import polka.support.archive : extractZip;
import polka.support.fs : createTempDir, currentArch, currentOS, rmdirRecurseQuiet;
import polka.support.http : HTTPClient;
import polka.tools.cache : cacheArchivePayload, jsonBoolField, jsonStringField;
import polka.tools.download_io;
import polka.tools.ids;
import polka.tools.manifest;
import polka.tools.types;

/// PostgreSQL official supported versions JSON feed (overridable in unit tests).
package string postgreSQLVersionsURL = "https://www.postgresql.org/versions.json";

/// Returns the PostgreSQL plugin backed by its manifest and platform download logic.
ToolPlugin postgreSQLPlugin() @safe
{
    PluginHooks hooks;
    hooks.download = (DownloadContext ctx) {
        downloadPostgreSQL(ctx.client, ctx.cacheDir, ctx.version_);
    };
    return newManifestPlugin(PostgreSQL, hooks);
}

/// Resolves the minor PostgreSQL version and downloads/caches the platform archive.
void downloadPostgreSQL(HTTPClient client, string cacheDir, string ver) @trusted
{
    auto resolvedVersion = resolvePostgreSQLReleaseVersion(client, ver);
    if (currentOS() == "linux" && currentArch() == "amd64")
    {
        downloadEmbeddedPostgreSQLLinux(client, cacheDir, ver, resolvedVersion);
        return;
    }

    downloadBuiltinManifestToolResolved(client, cacheDir, PostgreSQL, ver, resolvedVersion, null);
}

/// Unwraps the portable `.txz` tarball inside Zonky's embedded-postgres Maven JAR and caches it.
void downloadEmbeddedPostgreSQLLinux(
    HTTPClient client,
    string cacheDir,
    string requestedVersion,
    string resolvedVersion
) @trusted
{
    auto manifest = loadBuiltinManifest(PostgreSQL);
    auto mavenVersion = postgreSQLMavenVersion(resolvedVersion);
    auto asset = resolveManifestDownloadAsset(
        PostgreSQL,
        manifest.download.assets,
        requestedVersion,
        resolvedVersion,
        resolvedVersion,
        "linux",
        "amd64",
        ["maven_version": mavenVersion]
    );

    auto stagingDir = createTempDir("polka-postgresql-");
    scope (exit) rmdirRecurseQuiet(stagingDir);

    auto jarPath = buildPath(stagingDir, asset.fileName);
    downloadFile(client, asset.url, jarPath);
    auto payloadPath = extractEmbeddedPostgreSQLPayload(jarPath, buildPath(stagingDir, "extracted"));

    asset.fileName = "postgresql-" ~ resolvedVersion ~ "-linux-amd64.tar.xz";
    asset.archiveFormat = ArchiveFormat.tarXz;
    cacheArchivePayload(cacheDir, PostgreSQL, requestedVersion, resolvedVersion, asset, payloadPath);
}

/// Extracts `postgres-linux-x86_64.txz` from `jarPath` into `targetDir` and verifies its presence.
string extractEmbeddedPostgreSQLPayload(string jarPath, string targetDir) @trusted
{
    try
    {
        extractZip(jarPath, targetDir, false);
    }
    catch (Exception e)
    {
        throw new Exception("extract embedded postgresql archive: " ~ e.msg);
    }

    auto payloadPath = buildPath(targetDir, "postgres-linux-x86_64.txz");
    if (!exists(payloadPath) || isDir(payloadPath))
        throw new Exception("locate embedded postgresql payload: missing postgres-linux-x86_64.txz");
    return payloadPath;
}

/// Appends `.0` when `version` is `major.minor`, matching Maven artifact versions (`17.6` -> `17.6.0`).
string postgreSQLMavenVersion(string ver) @safe pure
{
    import std.algorithm : count;
    auto trimmed = ver.strip();
    if (trimmed.count('.') == 1)
        return trimmed ~ ".0";
    return trimmed;
}

/// Resolves a major label (`"17"`) against `postgreSQLVersionsURL` while accepting `major.minor` verbatim.
string resolvePostgreSQLReleaseVersion(HTTPClient client, string requested) @trusted
{
    auto trimmed = requested.strip();
    if (trimmed.length == 0)
        throw new Exception("postgresql version cannot be empty");
    if (trimmed.canFind("."))
        return trimmed;

    auto page = downloadText(client, postgreSQLVersionsURL, "postgresql versions index");
    JSONValue decoded;
    try
    {
        decoded = parseJSON(page);
    }
    catch (Exception e)
    {
        throw new Exception("parse postgresql versions index: " ~ e.msg);
    }
    if (decoded.type != JSONType.array)
        throw new Exception("parse postgresql versions index: expected JSON array");

    string[] versions;
    foreach (entry; decoded.array)
    {
        if (entry.type != JSONType.object)
            continue;
        auto supported = jsonBoolField(entry, "supported", false);
        auto major = jsonStringField(entry, "major").strip();
        auto minor = jsonStringField(entry, "latestMinor").strip();
        if (!supported || major.length == 0 || minor.length == 0)
            continue;
        versions ~= major ~ "." ~ minor;
    }

    try
    {
        return resolveMatchingVersion(versions, trimmed);
    }
    catch (Exception e)
    {
        throw new Exception(format(`resolve postgresql version "%s": %s`, trimmed, e.msg));
    }
}

unittest
{
    import std.file : readText, write;
    import polka.support.archive : ArchiveEntrySpec, buildTarXZBytes, buildZipBytes, extractTarXz;
    import polka.support.http : HTTPResponse, MockHTTPClient;

    // TestExtractEmbeddedPostgreSQLPayload
    {
        auto inner = buildTarXZBytes([ArchiveEntrySpec("bin/psql", cast(const(ubyte)[]) "psql")]);
        auto jar = buildZipBytes([ArchiveEntrySpec("postgres-linux-x86_64.txz", inner)]);
        auto tmp = createTempDir("polka-pg-jar-");
        scope (exit) rmdirRecurseQuiet(tmp);

        auto jarPath = buildPath(tmp, "postgres.jar");
        write(jarPath, jar);
        auto extracted = buildPath(tmp, "extracted");
        auto payloadPath = extractEmbeddedPostgreSQLPayload(jarPath, extracted);

        auto installDir = buildPath(tmp, "install");
        extractTarXz(payloadPath, installDir, false);
        assert(readText(buildPath(installDir, "bin", "psql")) == "psql");
    }

    // TestResolvePostgreSQLReleaseVersion
    {
        auto client = new MockHTTPClient((string url, string[string] hdrs) {
            return HTTPResponse(200, null, cast(ubyte[]) `[
  {"major":"17","latestMinor":"4","supported":true},
  {"major":"17","latestMinor":"6","supported":true},
  {"major":"16","latestMinor":"10","supported":true},
  {"major":"15","latestMinor":"14","supported":false}
]`);
        });
        auto saved = postgreSQLVersionsURL;
        postgreSQLVersionsURL = "https://mock.pg/versions.json";
        scope (exit) postgreSQLVersionsURL = saved;

        assert(resolvePostgreSQLReleaseVersion(client, "17") == "17.6");
        assert(resolvePostgreSQLReleaseVersion(client, "17.4") == "17.4");
    }
}
