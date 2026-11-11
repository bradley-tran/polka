module polka.tools.github_archive;

import std.file : exists, mkdirRecurse;
import std.format : format;
import std.path : buildPath;
import std.string : icmp, strip, stripRight;

import polka.support.archive : readZipFileComment;
import polka.support.fs : createTempDir, rmdirRecurseQuiet;
import polka.support.http : HTTPClient;
import polka.tools.cache : cacheArchivePayload, jsonStringField;
import polka.tools.download_io : downloadFile, downloadJSON, githubAPIBaseURL;
import polka.tools.types : DownloadAsset, ManifestGitHubDownload;

/// Resolves the commit SHA underlying a lightweight or annotated GitHub tag.
string resolveGitHubTagCommit(
    HTTPClient client,
    string tool,
    const ManifestGitHubDownload github,
    string tag
) @trusted
{
    auto owner = github.owner.strip();
    auto repo = github.repo.strip();
    if (owner.length == 0 || repo.length == 0)
        throw new Exception(tool ~ " download resolver requires github owner and repo");

    auto trimmedTag = tag.strip();
    if (trimmedTag.length == 0)
        throw new Exception(tool ~ " download resolver requires a resolved tag");

    auto url = format("%s/repos/%s/%s/commits/%s", githubAPIBaseURL.stripRight("/"), owner, repo, trimmedTag);
    auto commitJson = downloadJSON(client, url, "github tag commit");
    auto sha = jsonStringField(commitJson, "sha").strip();
    if (sha.length == 0)
        throw new Exception(format(`github tag "%s" resolved to an empty commit sha`, trimmedTag));
    return sha;
}

/// Downloads a tag source zip archive, verifies its embedded commit SHA, and stores it in cache.
void downloadGitHubSourceArchive(
    HTTPClient client,
    string cacheDir,
    string tool,
    string requestedVersion,
    string resolvedVersion,
    DownloadAsset asset,
    string commit
) @trusted
{
    auto toolCacheDir = buildPath(cacheDir, tool);
    if (!exists(toolCacheDir))
        mkdirRecurse(toolCacheDir);

    auto stagingDir = createTempDir(requestedVersion ~ "-tmp-", toolCacheDir);
    scope (exit) rmdirRecurseQuiet(stagingDir);

    auto archivePath = buildPath(stagingDir, asset.fileName);
    downloadFile(client, asset.url, archivePath);
    verifyGitHubArchiveCommit(tool, archivePath, commit);

    cacheArchivePayload(cacheDir, tool, requestedVersion, resolvedVersion, asset, archivePath);
}

/// Verifies GitHub's archive EOCD comment against the expected commit SHA resolved for the tag.
void verifyGitHubArchiveCommit(string tool, string archivePath, string commit) @trusted
{
    string comment;
    try
    {
        comment = readZipFileComment(archivePath).strip();
    }
    catch (Exception e)
    {
        throw new Exception(format("open %s archive %s: %s", tool, archivePath, e.msg));
    }

    auto expected = commit.strip();
    if (comment.length == 0)
        throw new Exception(format("%s archive %s has no embedded commit to verify against tag commit %s", tool, archivePath, expected));
    if (icmp(comment, expected) != 0)
        throw new Exception(format("%s archive commit %s does not match expected tag commit %s", tool, comment, expected));
}

unittest
{
    import std.algorithm : canFind;
    import std.file : write;
    import std.string : toUpper;
    import polka.support.archive : ArchiveEntrySpec, buildZipBytes;

    // TestVerifyRedisArchiveCommit
    auto commit = "abc123def4567890abc123def4567890abc12345";
    auto tmp = createTempDir("polka-gh-archive-");
    scope (exit) rmdirRecurseQuiet(tmp);

    string writeZipWithComment(string name, string cmt)
    {
        auto p = buildPath(tmp, name);
        auto data = buildZipBytes([ArchiveEntrySpec("redis-windows-8.8.0/redis-server.exe", cast(const(ubyte)[]) "binary")], cmt);
        write(p, data);
        return p;
    }

    // Matching case-insensitive
    verifyGitHubArchiveCommit("redis", writeZipWithComment("match.zip", commit.toUpper()), commit);

    // Mismatch
    bool threwMismatch = false;
    try
    {
        verifyGitHubArchiveCommit("redis", writeZipWithComment("mismatch.zip", "0000000000000000000000000000000000000000"), commit);
    }
    catch (Exception e)
    {
        threwMismatch = e.msg.canFind("archive commit");
    }
    assert(threwMismatch);

    // Empty comment
    bool threwEmpty = false;
    try
    {
        verifyGitHubArchiveCommit("redis", writeZipWithComment("empty.zip", ""), commit);
    }
    catch (Exception e)
    {
        threwEmpty = e.msg.canFind("has no embedded commit");
    }
    assert(threwEmpty);
}
