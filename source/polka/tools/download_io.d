module polka.tools.download_io;

import std.algorithm : canFind, startsWith, stripRight;
import std.conv : to;
import std.file : exists, mkdirRecurse, write;
import std.format : format;
import std.json;
import std.path : buildPath, dirName;
import std.regex : Captures, matchAll, Regex, regex, replaceAll;
import std.string : icmp, strip, toLower;

import polka.support.fs : createTempDir, octal, rmdirRecurseQuiet, setFileMode;
import polka.support.http : CurlHTTPClient, HTTPClient, HTTPResponse;
import polka.tools.cache;
import polka.tools.checksum;
import polka.tools.manifest_rules;
import polka.tools.types;

/// Base GitHub REST API URL (overridable in unit tests for offline mocking).
package string githubAPIBaseURL = "https://api.github.com";

/// GitHub release JSON representation.
struct GitHubRelease
{
    string tagName;
    bool draft = false;
    bool prerelease = false;
    GitHubReleaseAsset[] assets;
}

/// GitHub release asset JSON representation.
struct GitHubReleaseAsset
{
    string name;
    string browserDownloadURL;
    string digest;
}

/// Returns `client` if non-null, otherwise a default libcurl-backed client.
HTTPClient effectiveHTTPClient(HTTPClient client) @safe
{
    if (client !is null)
        return client;
    auto curl = new CurlHTTPClient();
    curl.timeoutSeconds = 600;
    return curl;
}

/// Downloads text content from `url` with Polka's default headers.
string downloadText(HTTPClient client, string url, string description) @trusted
{
    client = effectiveHTTPClient(client);
    auto resp = client.get(url, ["User-Agent": "polka"]);
    if (resp.statusCode != 200)
        throw new Exception(format("download %s %s: unexpected status %d", description, url, resp.statusCode));
    return resp.bodyText();
}

/// Downloads and parses JSON content from `url` with GitHub API accept header.
JSONValue downloadJSON(HTTPClient client, string url, string description) @trusted
{
    client = effectiveHTTPClient(client);
    auto resp = client.get(url, [
        "User-Agent": "polka",
        "Accept": "application/vnd.github+json",
    ]);
    if (resp.statusCode != 200)
        throw new Exception(format("download %s %s: unexpected status %d", description, url, resp.statusCode));

    try
    {
        return parseJSON(resp.bodyText());
    }
    catch (Exception e)
    {
        throw new Exception(format("decode %s %s: %s", description, url, e.msg));
    }
}

/// Downloads binary content from `url` and writes it to `targetPath`.
void downloadFile(HTTPClient client, string url, string targetPath) @trusted
{
    client = effectiveHTTPClient(client);
    auto resp = client.get(url, ["User-Agent": "polka"]);
    if (resp.statusCode != 200)
        throw new Exception(format("download %s: unexpected status %d", url, resp.statusCode));

    auto parent = dirName(targetPath);
    if (parent.length > 0 && !exists(parent))
        mkdirRecurse(parent);

    write(targetPath, resp.body);
    setFileMode(targetPath, octal!644);
}

/// Fetches and parses a sidecar checksum file for `sourceFileName`.
string downloadChecksumValue(
    HTTPClient client,
    string checksumURL,
    ChecksumAlgorithm algorithm,
    string sourceFileName
) @trusted
{
    auto checksumData = downloadText(client, checksumURL, "checksum");
    try
    {
        return parseChecksumValueForAlgorithm(algorithm, checksumData, sourceFileName);
    }
    catch (Exception e)
    {
        throw new Exception(format("parse checksum %s: %s", checksumURL, e.msg));
    }
}

/// Downloads, checksum-verifies, and caches a resolved tool asset.
void downloadManifestAsset(
    HTTPClient client,
    string cacheDir,
    string tool,
    string requestedVersion,
    string downloadedVersion,
    DownloadAsset asset
) @trusted
{
    auto cacheSubdir = buildPath(cacheDir, tool);
    if (!exists(cacheSubdir))
        mkdirRecurse(cacheSubdir);

    auto stagingDir = createTempDir(requestedVersion ~ "-tmp-", cacheSubdir);
    scope (exit) rmdirRecurseQuiet(stagingDir);

    auto archivePath = buildPath(stagingDir, asset.fileName);
    downloadFile(client, asset.url, archivePath);

    auto checksum = asset.checksum.strip();
    if (checksum.length == 0 && asset.checksumURL.strip().length > 0)
    {
        checksum = downloadChecksumValue(client, asset.checksumURL, asset.checksumAlgorithm, asset.sourceFileName);
    }

    switch (asset.checksumAlgorithm)
    {
        case ChecksumAlgorithm.md5:
        case ChecksumAlgorithm.sha256:
        case ChecksumAlgorithm.sha3_256:
            if (checksum.length == 0)
            {
                throw new Exception(format(`%s asset "%s" declares %s checksum but none was published`,
                    tool, asset.sourceFileName, cast(string) asset.checksumAlgorithm));
            }
            break;
        default:
            break;
    }

    if (checksum.length > 0)
    {
        verifyFileChecksum(asset.checksumAlgorithm, checksum, archivePath);
        asset.checksum = checksum;
    }

    if (asset.installPath.strip().length > 0)
    {
        cacheFilePayload(
            cacheDir,
            tool,
            requestedVersion,
            downloadedVersion,
            asset.fileName,
            asset.url,
            asset.installPath,
            asset.checksumAlgorithm,
            asset.checksum,
            archivePath
        );
        return;
    }

    cacheArchivePayload(cacheDir, tool, requestedVersion, downloadedVersion, asset, archivePath);
}

/// Alias matching Go's `downloadDatabaseAsset`.
void downloadDatabaseAsset(
    HTTPClient client,
    string cacheDir,
    string tool,
    string ver,
    DatabaseDownloadAsset asset
) @trusted
{
    downloadManifestAsset(client, cacheDir, tool, ver, ver, asset);
}

/// Resolves a requested partial or exact version against manifest GitHub or release-index rules.
void resolveManifestDownloadVersion(
    HTTPClient client,
    string tool,
    string requestedVersion,
    const ManifestDownload download,
    out string resolvedVersion,
    out string tag,
    out GitHubReleaseAsset[] githubAssets
) @trusted
{
    import polka.tools.ids : validateVersion;

    requestedVersion = requestedVersion.strip();
    if (requestedVersion.length == 0)
        throw new Exception(tool ~ " version cannot be empty");

    if (!download.hasGitHub())
    {
        if (download.hasReleaseIndex())
        {
            auto normalizedRequested = requestedVersion.startsWith("v") ? requestedVersion[1 .. $] : requestedVersion;
            if (!versionNeedsResolution(normalizedRequested))
            {
                resolvedVersion = normalizedRequested;
                tag = normalizedRequested;
                githubAssets = null;
                return;
            }
            try
            {
                auto resolved = resolveReleaseIndexVersion(client, download.releaseIndex, normalizedRequested);
                resolvedVersion = resolved;
                tag = resolved;
                githubAssets = null;
                return;
            }
            catch (Exception e)
            {
                throw new Exception(format(`resolve %s version "%s": %s`, tool, requestedVersion, e.msg));
            }
        }

        resolvedVersion = requestedVersion;
        tag = requestedVersion;
        githubAssets = null;
        return;
    }

    if (icmp(download.github.mode.strip(), "tag") == 0)
    {
        auto prefix = download.github.tagPrefix.strip();
        resolvedVersion = requestedVersion.startsWith(prefix) ? requestedVersion[prefix.length .. $] : requestedVersion;
        if (resolvedVersion.length == 0)
            throw new Exception(tool ~ " version cannot be empty");
        validateVersion(tool, resolvedVersion);
        tag = prefix ~ resolvedVersion;
        githubAssets = null;
        return;
    }

    try
    {
        resolveGitHubReleaseVersion(client, download.github, requestedVersion, resolvedVersion, tag, githubAssets);
    }
    catch (Exception e)
    {
        throw new Exception(format(`resolve %s version "%s": %s`, tool, requestedVersion, e.msg));
    }
}

/// Fetches a release-index page and selects the best matching release version.
string resolveReleaseIndexVersion(
    HTTPClient client,
    const ManifestReleaseIndexDownload config,
    string requested
) @trusted
{
    auto indexURL = renderDownloadTemplate("download.release-index.url", config.url, [
        "requested": requested.strip(),
    ]);
    auto page = downloadText(client, indexURL, "release index");
    return selectReleaseIndexVersion(page, config.pattern, requested);
}

/// Compiles `pattern`, supporting leading inline flag prefixes like `(?i)`, `(?m)`, or `(?s)`.
Regex!char compileManifestRegex(string pattern) @safe
{
    string pat = pattern;
    string flags = "";
    if (pat.startsWith("(?") && pat.length > 3)
    {
        auto close = -1;
        foreach (idx; 2 .. pat.length)
        {
            char c = pat[idx];
            if (c == ')')
            {
                close = cast(int) idx;
                break;
            }
            if (c == 'i' || c == 'm' || c == 's')
            {
                flags ~= c;
            }
            else
            {
                close = -1;
                break;
            }
        }
        if (close > 2)
            pat = pat[close + 1 .. $];
    }
    return regex(pat, flags);
}

/// Scans `page` with regex `pattern` and returns the highest compatible version matching `requested`.
string selectReleaseIndexVersion(string page, string pattern, string requested) @safe
{
    requested = requested.strip();
    if (requested.length == 0)
        throw new Exception("requested version is empty");

    auto matcher = compileManifestRegex(pattern);
    bool allowPrerelease = requested.canFind("-");
    bool[string] seen;
    string best = "";

    foreach (cap; page.matchAll(matcher))
    {
        if (cap.length < 2)
            continue;
        auto ver = cap[1].strip();
        if (ver.length == 0)
            continue;
        if (ver in seen)
            continue;
        seen[ver] = true;

        if (!versionMatchesRequest(ver, requested))
            continue;
        if (!allowPrerelease && ver.canFind("-"))
            continue;
        if (best.length == 0 || compareVersions(ver, best) > 0)
            best = ver;
    }

    if (best.length == 0)
        throw new Exception("no matching release found");
    return best;
}

/// Selects the highest matching version from an explicit list of candidate versions.
string resolveMatchingVersion(const(string)[] versions, string requested) @safe
{
    requested = requested.strip();
    if (requested.startsWith("v"))
        requested = requested[1 .. $];
    requested = requested.strip();
    if (requested.length == 0)
        throw new Exception("requested version is empty");

    string[] candidates;
    foreach (rawVer; versions)
    {
        auto norm = rawVer.strip();
        if (norm.startsWith("v"))
            norm = norm[1 .. $];
        norm = norm.strip();
        if (versionMatchesRequest(norm, requested))
            candidates ~= norm;
    }
    if (candidates.length == 0)
        throw new Exception("no matching release found");

    string best = candidates[0];
    foreach (candidate; candidates[1 .. $])
    {
        if (compareVersions(candidate, best) > 0)
            best = candidate;
    }
    return best;
}

/// Selects and renders a platform download asset from `assets` for `goos`/`goarch`.
DownloadAsset resolveManifestDownloadAsset(
    string tool,
    const(DownloadAsset[string]) assets,
    string requestedVersion,
    string resolvedVersion,
    string tag,
    string goos,
    string goarch,
    const(string[string]) values
) @safe
{
    if (assets.length == 0)
        throw new Exception(tool ~ " does not define automatic downloads");

    foreach (key; downloadPlatformKeys(goos, goarch))
    {
        if (auto assetPtr = key in assets)
        {
            try
            {
                return renderDownloadAsset(*assetPtr, requestedVersion, resolvedVersion, tag, values);
            }
            catch (Exception e)
            {
                throw new Exception(format("render %s download asset for %s: %s", tool, key, e.msg));
            }
        }
    }

    throw new Exception(format(`%s version "%s" is not available for %s/%s`, tool, resolvedVersion, goos, goarch));
}

/// Substitutes `{version}`, `{tag}`, `{requested}`, `{major}`, `{minor}`, `{major_minor}`,
/// and custom `values` across asset fields.
DownloadAsset renderDownloadAsset(
    DownloadAsset asset,
    string requestedVersion,
    string resolvedVersion,
    string tag,
    const(string[string]) values
) @safe
{
    auto templateValues = derivedTemplateValues(resolvedVersion);
    templateValues["requested"] = requestedVersion.strip();
    templateValues["version"] = resolvedVersion.strip();
    templateValues["tag"] = tag.strip();
    if (templateValues["tag"].length == 0)
        templateValues["tag"] = templateValues["version"];
    if (values.length > 0)
    {
        foreach (k, v; values)
            templateValues[k] = v;
    }

    asset.fileName = renderDownloadTemplate("filename", asset.fileName, templateValues);
    asset.url = renderDownloadTemplate("url", asset.url, templateValues);
    asset.checksumURL = renderDownloadTemplate("checksum-url", asset.checksumURL, templateValues);
    asset.sourceFileName = renderDownloadTemplate("source-filename", asset.sourceFileName, templateValues);
    asset.installPath = renderDownloadTemplate("install-path", asset.installPath, templateValues);
    if (asset.sourceFileName.strip().length == 0)
        asset.sourceFileName = asset.fileName;

    return asset;
}

/// Renders `{placeholder}` expressions in `value`, failing if an unknown placeholder appears.
string renderDownloadTemplate(string field, string value, const(string[string]) values) @safe
{
    if (value.strip().length == 0)
        return value.strip();

    auto pattern = regex(`\{([A-Za-z0-9_]+)\}`);
    string missing = "";
    auto rendered = replaceAll!((Captures!string cap) {
        auto key = cap[1];
        if (auto p = key in values)
            return *p;
        if (missing.length == 0)
            missing = key;
        return cap.hit;
    })(value, pattern);

    if (missing.length > 0)
        throw new Exception(format(`%s references unknown template value "%s"`, field, missing));
    return rendered;
}

/// Resolves a requested version against paginated GitHub releases.
void resolveGitHubReleaseVersion(
    HTTPClient client,
    const ManifestGitHubDownload config,
    string requested,
    out string resolvedVersion,
    out string selectedTag,
    out GitHubReleaseAsset[] selectedAssets
) @trusted
{
    auto prefix = config.tagPrefix.strip();
    auto normRequested = requested.strip();
    if (prefix.length > 0 && normRequested.startsWith(prefix))
        normRequested = normRequested[prefix.length .. $];

    auto releases = fetchGitHubReleases(client, config);
    bool allowPrerelease = normRequested.strip().canFind("-");

    bool found = false;
    GitHubRelease bestRelease;
    string bestVersion = "";

    foreach (release; releases)
    {
        if (release.draft)
            continue;
        if (release.prerelease && !allowPrerelease)
            continue;

        auto ver = versionFromGitHubTag(release.tagName, config.tagPrefix);
        if (!versionMatchesRequest(ver, normRequested))
            continue;

        if (!found || compareVersions(ver, bestVersion) > 0)
        {
            bestRelease = release;
            bestVersion = ver;
            found = true;
        }
    }

    if (!found)
        throw new Exception("no matching release found");

    resolvedVersion = bestVersion;
    selectedTag = bestRelease.tagName.strip();
    selectedAssets = bestRelease.assets;
}

/// Fetches all GitHub releases via paginated `/repos/{owner}/{repo}/releases` API calls.
GitHubRelease[] fetchGitHubReleases(HTTPClient client, const ManifestGitHubDownload config) @trusted
{
    auto owner = config.owner.strip();
    auto repo = config.repo.strip();
    if (owner.length == 0 || repo.length == 0)
        throw new Exception("github download resolver requires owner and repo");

    GitHubRelease[] releases;
    auto base = githubAPIBaseURL.stripRight('/');
    for (int page = 1; ; page++)
    {
        auto url = format("%s/repos/%s/%s/releases?per_page=100&page=%d", base, owner, repo, page);
        auto payload = downloadJSON(client, url, "github releases");
        if (payload.type != JSONType.array)
            throw new Exception(format("decode github releases %s: expected JSON array", url));

        auto arr = payload.array;
        foreach (item; arr)
        {
            if (item.type != JSONType.object)
                continue;
            GitHubRelease rel;
            rel.tagName = jsonStringField(item, "tag_name");
            rel.draft = jsonBoolField(item, "draft", false);
            rel.prerelease = jsonBoolField(item, "prerelease", false);

            if (auto assetsVal = "assets" in item.object)
            {
                if (assetsVal.type == JSONType.array)
                {
                    foreach (aItem; assetsVal.array)
                    {
                        if (aItem.type != JSONType.object)
                            continue;
                        GitHubReleaseAsset ga;
                        ga.name = jsonStringField(aItem, "name");
                        ga.browserDownloadURL = jsonStringField(aItem, "browser_download_url");
                        ga.digest = jsonStringField(aItem, "digest");
                        rel.assets ~= ga;
                    }
                }
            }
            releases ~= rel;
        }

        if (arr.length < 100)
            break;
    }

    return releases;
}

private string versionFromGitHubTag(string tag, string tagPrefix) @safe pure
{
    auto ver = tag.strip();
    auto prefix = tagPrefix.strip();
    if (prefix.length > 0 && ver.startsWith(prefix))
        ver = ver[prefix.length .. $];
    return ver.strip();
}

/// Applies a matching GitHub release asset's digest to `asset` when compatible.
void applyGitHubAssetDigest(ref DownloadAsset asset, const(GitHubReleaseAsset)[] githubAssets) @safe
{
    auto sourceFileName = normalizeChecksumFileName(asset.sourceFileName);
    foreach (githubAsset; githubAssets)
    {
        if (normalizeChecksumFileName(githubAsset.name) != sourceFileName)
            continue;

        ChecksumAlgorithm algorithm;
        string checksum;
        if (!parseGitHubAssetDigest(githubAsset.digest, algorithm, checksum))
            return;

        if (asset.checksumAlgorithm != ChecksumAlgorithm.none && asset.checksumAlgorithm != algorithm)
            return;

        asset.checksumAlgorithm = algorithm;
        asset.checksum = checksum;
        return;
    }
}

private bool parseGitHubAssetDigest(string digest, out ChecksumAlgorithm algorithm, out string checksum) @safe
{
    import std.string : indexOf;
    auto trimmed = digest.strip();
    auto colon = trimmed.indexOf(':');
    if (colon <= 0)
        return false;

    auto algText = trimmed[0 .. colon].strip().toLower();
    auto csText = trimmed[colon + 1 .. $].strip();

    ChecksumAlgorithm parsed;
    if (!parseChecksumAlgorithm(algText, parsed))
        return false;

    switch (parsed)
    {
        case ChecksumAlgorithm.md5:
        case ChecksumAlgorithm.sha256:
        case ChecksumAlgorithm.sha3_256:
            break;
        default:
            return false;
    }

    try
    {
        string norm;
        size_t sz;
        validateChecksumValue(parsed, csText, norm, sz);
        algorithm = parsed;
        checksum = norm;
        return true;
    }
    catch (Exception)
    {
        return false;
    }
}

unittest
{
    import std.algorithm : canFind;
    import std.array : replicate;
    import polka.support.http : MockHTTPClient;

    // TestSelectReleaseIndexVersionPrefersStablePatchForMinorLabel
    auto page =
        "<a href=\"https://getcomposer.org/download/2.10.0-RC2/composer.phar\">rc</a>\n" ~
        "<a href=\"https://getcomposer.org/download/2.9.8/composer.phar\">stable</a>\n" ~
        "<a href=\"https://getcomposer.org/download/2.8.12/composer.phar\">minor-latest</a>\n" ~
        "<a href=\"https://getcomposer.org/download/2.8.11/composer.phar\">minor-older</a>\n" ~
        "<a href=\"https://getcomposer.org/download/2.2.28/composer.phar\">lts</a>";
    auto pattern = `(?:https://getcomposer\.org)?/download/([0-9]+(?:\.[0-9]+){1,2}(?:-[0-9A-Za-z.-]+)?)/composer\.phar`;
    assert(selectReleaseIndexVersion(page, pattern, "2.8") == "2.8.12");
    assert(selectReleaseIndexVersion(page, pattern, "2") == "2.9.8");

    // TestDownloadTextRetriesTransientStatus via MockHTTPClient
    int attempts = 0;
    auto mockClient = new MockHTTPClient((string url, string[string] hdrs) {
        attempts++;
        if (attempts == 1)
            return HTTPResponse(503, null, null);
        return HTTPResponse(200, null, cast(ubyte[]) "ready");
    });
    auto got = downloadText(mockClient, "https://example.test/data", "retry test");
    assert(got == "ready");
    assert(attempts == 2);

    // TestManifestDownloadVersionAllowsRequestedPrerelease
    auto origGH = githubAPIBaseURL;
    scope (exit) githubAPIBaseURL = origGH;
    githubAPIBaseURL = "https://mock.github";
    auto preClient = new MockHTTPClient((string url, string[string] hdrs) {
        return HTTPResponse(200, null, cast(ubyte[]) `[
            {"tag_name":"1.4.5","assets":[{"name":"pie.phar"}]},
            {"tag_name":"1.4.6-RC1","prerelease":true,"assets":[{"name":"pie.phar"}]}
        ]`);
    });
    ManifestDownload md;
    md.github = ManifestGitHubDownload("php", "pie", "", "");
    string resolvedVer, resolvedTag;
    GitHubReleaseAsset[] ghAssets;
    resolveManifestDownloadVersion(preClient, "pie", "1.4.6-RC1", md, resolvedVer, resolvedTag, ghAssets);
    assert(resolvedVer == "1.4.6-RC1" && resolvedTag == "1.4.6-RC1");

    // TestDownloadManifestAssetRequiresDeclaredChecksumAndUsesChecksumURLFallback
    auto payloadBytes = cast(const(ubyte)[]) "verified binary\n";
    auto sum = checksumBytes(ChecksumAlgorithm.sha256, payloadBytes);
    auto csClient = new MockHTTPClient((string url, string[string] hdrs) {
        if (url.canFind("/tool.bin"))
            return HTTPResponse(200, null, payloadBytes.dup);
        if (url.canFind("/SHASUMS256.txt"))
            return HTTPResponse(200, null, cast(ubyte[]) (sum ~ "  tool.bin\n"));
        return HTTPResponse(404, null, null);
    });

    DownloadAsset missingCS;
    missingCS.fileName = "tool.bin";
    missingCS.sourceFileName = "tool.bin";
    missingCS.url = "https://mock.dl/tool.bin";
    missingCS.installPath = "bin/tool";
    missingCS.checksumAlgorithm = ChecksumAlgorithm.sha256;

    auto tmpCache = createTempDir("polka-dl-cs-");
    scope (exit) rmdirRecurseQuiet(tmpCache);

    bool sawMissingErr = false;
    try
    {
        downloadManifestAsset(csClient, tmpCache, "demo", "1.0", "1.0.0", missingCS);
    }
    catch (Exception e)
    {
        sawMissingErr = e.msg.canFind("declares sha256 checksum but none was published");
    }
    assert(sawMissingErr);

    auto fallbackCS = missingCS;
    fallbackCS.checksumURL = "https://mock.dl/SHASUMS256.txt";
    downloadManifestAsset(csClient, tmpCache, "demo", "1.0", "1.0.0", fallbackCS);
}
