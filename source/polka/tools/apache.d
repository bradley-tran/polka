module polka.tools.apache;

import std.algorithm : startsWith;
import std.conv : to;
import std.format : format;
import std.regex : matchAll, regex;
import std.string : indexOf, join, lastIndexOf, replace, split, strip;

import polka.support.fs : currentArch, currentOS;
import polka.support.http : HTTPClient;
import polka.tools.download_io;
import polka.tools.ids;
import polka.tools.manifest;
import polka.tools.types;

/// Apache Lounge download listing page URL (overridable in unit tests).
package string apacheLoungeDownloadURL = "https://www.apachelounge.com/download/";

/// Discovered Apache Lounge Windows binary release entry.
struct ApacheLoungeRelease
{
    string version_;
    string build;
    string vs;
    string url;
    string checksumURL;
}

/// Returns the Apache plugin backed by its manifest and Apache Lounge release discovery.
ToolPlugin apachePlugin() @safe
{
    PluginHooks hooks;
    hooks.download = (DownloadContext ctx) {
        downloadApache(ctx.client, ctx.cacheDir, ctx.version_);
    };
    return newManifestPlugin(Apache, hooks);
}

/// Resolves and downloads the matching Apache release archive into cache.
void downloadApache(HTTPClient client, string cacheDir, string ver) @trusted
{
    string resolvedVersion;
    DownloadAsset asset;
    resolveApacheDownloadAsset(client, ver, currentOS(), currentArch(), resolvedVersion, asset);
    downloadManifestAsset(client, cacheDir, Apache, ver, resolvedVersion, asset);
}

/// Resolves Apache Lounge release metadata and manifest download asset for `goos`/`goarch`.
void resolveApacheDownloadAsset(
    HTTPClient client,
    string requestedVersion,
    string goos,
    string goarch,
    out string resolvedVersion,
    out DownloadAsset asset
) @trusted
{
    auto release = resolveApacheLoungeRelease(client, requestedVersion);
    auto manifest = loadBuiltinManifest(Apache);
    asset = resolveManifestDownloadAsset(
        Apache,
        manifest.download.assets,
        requestedVersion,
        release.version_,
        release.version_,
        goos,
        goarch,
        ["build": release.build, "vs": release.vs]
    );
    applyApacheLoungeReleaseAsset(asset, release);
    resolvedVersion = release.version_;
}

/// Fetches Apache Lounge's download page and returns the newest release matching `requested`.
ApacheLoungeRelease resolveApacheLoungeRelease(HTTPClient client, string requested) @trusted
{
    auto trimmed = requested.strip();
    if (trimmed.length == 0)
        throw new Exception("apache version cannot be empty");

    auto page = downloadText(client, apacheLoungeDownloadURL, "apache lounge download page");
    ApacheLoungeRelease best;
    foreach (release; apacheLoungePageReleases(page))
    {
        if (!versionMatchesRequest(release.version_, trimmed))
            continue;
        if (best.version_.length == 0 || compareApacheLoungeRelease(release, best) > 0)
            best = release;
    }
    if (best.version_.length == 0)
        throw new Exception(format(`resolve apache version "%s": no matching Apache Lounge release found`, trimmed));
    return best;
}

/// Parses Apache Lounge HTML preferring real href targets over bare filenames.
ApacheLoungeRelease[] apacheLoungePageReleases(string page) @safe
{
    auto hrefPattern = regex(`href=["']([^"']*?(httpd-([0-9]+\.[0-9]+\.[0-9]+)-([0-9]+)-Win64-VS([0-9]+)\.zip))["']`, "i");
    auto barePattern = regex(`httpd-([0-9]+\.[0-9]+\.[0-9]+)-([0-9]+)-Win64-VS([0-9]+)\.zip`, "i");

    bool[string] seen;
    ApacheLoungeRelease[] releases;

    foreach (m; matchAll(page, hrefPattern))
    {
        ApacheLoungeRelease rel;
        rel.version_ = m[3].strip();
        rel.build = m[4].strip();
        rel.vs = m[5].strip();
        if (rel.version_.length == 0 || rel.build.length == 0 || rel.vs.length == 0)
            continue;
        rel.url = resolveApacheLoungePageURL(m[1]);
        if (rel.url.length > 0)
            rel.checksumURL = rel.url ~ ".txt";

        auto key = apacheLoungeReleaseKey(rel);
        if (key in seen)
            continue;
        seen[key] = true;
        releases ~= rel;
    }

    foreach (m; matchAll(page, barePattern))
    {
        ApacheLoungeRelease rel;
        rel.version_ = m[1].strip();
        rel.build = m[2].strip();
        rel.vs = m[3].strip();
        if (rel.version_.length == 0 || rel.build.length == 0 || rel.vs.length == 0)
            continue;

        auto key = apacheLoungeReleaseKey(rel);
        if (key in seen)
            continue;
        seen[key] = true;
        releases ~= rel;
    }

    return releases;
}

private string apacheLoungeReleaseKey(const ApacheLoungeRelease r) @safe pure
{
    return r.version_ ~ "\x00" ~ r.build ~ "\x00" ~ r.vs;
}

/// Unescapes HTML entities and resolves relative links against `apacheLoungeDownloadURL`.
string resolveApacheLoungePageURL(string rawURL) @safe
{
    auto cleaned = unescapeHTMLEntities(rawURL).strip();
    if (cleaned.length == 0)
        return "";
    return resolveURLReference(apacheLoungeDownloadURL, cleaned);
}

/// Attaches the discovered live archive URL and `.txt` SHA-256 checksum sidecar onto `asset`.
void applyApacheLoungeReleaseAsset(ref DownloadAsset asset, const ApacheLoungeRelease release) @safe
{
    if (release.url.strip().length > 0)
        asset.url = release.url.strip();
    if (release.checksumURL.strip().length > 0)
    {
        asset.checksumURL = release.checksumURL.strip();
        asset.checksumAlgorithm = ChecksumAlgorithm.sha256;
    }
}

/// Compares Apache Lounge releases by semantic version, build stamp, and VC/VS toolset.
int compareApacheLoungeRelease(const ApacheLoungeRelease left, const ApacheLoungeRelease right) @safe pure
{
    auto cmp = compareVersions(left.version_, right.version_);
    if (cmp != 0)
        return cmp;
    cmp = compareNumericString(left.build, right.build);
    if (cmp != 0)
        return cmp;
    return compareNumericString(left.vs, right.vs);
}

private int compareNumericString(string left, string right) @safe pure
{
    auto lt = left.strip();
    auto rt = right.strip();
    try
    {
        auto lv = to!long(lt);
        auto rv = to!long(rt);
        if (lv > rv) return 1;
        if (lv < rv) return -1;
        return 0;
    }
    catch (Exception)
    {
        if (lt > rt) return 1;
        if (lt < rt) return -1;
        return 0;
    }
}

private string unescapeHTMLEntities(string s) @safe pure
{
    return s.replace("&amp;", "&")
            .replace("&lt;", "<")
            .replace("&gt;", ">")
            .replace("&quot;", `"`)
            .replace("&#39;", "'")
            .replace("&#x27;", "'");
}

/// Minimal RFC 3986 relative URI reference resolver against `baseURL`.
package string resolveURLReference(string baseURL, string refURL) @safe pure
{
    // If refURL already carries a scheme (`http://`, `https://`, etc.), return as-is.
    auto schemeIdx = refURL.indexOf("://");
    if (schemeIdx > 0)
        return refURL;

    // Parse scheme://authority from baseURL.
    auto baseSchemeIdx = baseURL.indexOf("://");
    if (baseSchemeIdx <= 0)
        return refURL;
    auto scheme = baseURL[0 .. baseSchemeIdx];
    auto afterScheme = baseURL[baseSchemeIdx + 3 .. $];
    auto slashIdx = afterScheme.indexOf('/');
    auto authority = slashIdx >= 0 ? afterScheme[0 .. slashIdx] : afterScheme;
    auto basePath = slashIdx >= 0 ? afterScheme[slashIdx .. $] : "/";

    if (refURL.startsWith("//"))
        return scheme ~ ":" ~ refURL;

    // Strip query/fragment from basePath if present.
    auto qIdx = basePath.indexOf('?');
    if (qIdx >= 0)
        basePath = basePath[0 .. qIdx];
    auto fIdx = basePath.indexOf('#');
    if (fIdx >= 0)
        basePath = basePath[0 .. fIdx];

    if (refURL.startsWith("?") || refURL.startsWith("#"))
        return scheme ~ "://" ~ authority ~ basePath ~ refURL;

    // Separate ref path and trailing ?query/#fragment.
    string refPath = refURL;
    string suffix = "";
    auto rq = refPath.indexOf('?');
    auto rf = refPath.indexOf('#');
    size_t cut = size_t.max;
    if (rq >= 0) cut = rq;
    if (rf >= 0 && rf < cut) cut = rf;
    if (cut != size_t.max)
    {
        suffix = refPath[cut .. $];
        refPath = refPath[0 .. cut];
    }

    string merged;
    if (refPath.startsWith("/"))
    {
        merged = refPath;
    }
    else
    {
        auto lastSlash = basePath.lastIndexOf('/');
        auto dir = lastSlash >= 0 ? basePath[0 .. lastSlash + 1] : "/";
        merged = dir ~ refPath;
    }

    // Normalize dot segments.
    string[] stack;
    bool trailingSlash = merged.length > 0 && merged[$ - 1] == '/';
    foreach (seg; merged.split('/'))
    {
        if (seg.length == 0 || seg == ".")
            continue;
        if (seg == "..")
        {
            if (stack.length > 0)
                stack = stack[0 .. $ - 1];
        }
        else
        {
            stack ~= seg;
        }
    }
    auto cleanPath = "/" ~ stack.join("/");
    if (trailingSlash && cleanPath.length > 1)
        cleanPath ~= "/";

    return scheme ~ "://" ~ authority ~ cleanPath ~ suffix;
}

unittest
{
    assert(resolveURLReference("http://127.0.0.1:8080/download/", "/download/VS18/httpd.zip")
        == "http://127.0.0.1:8080/download/VS18/httpd.zip");
    assert(resolveURLReference("https://www.apachelounge.com/download/", "VS18/binaries/httpd.zip")
        == "https://www.apachelounge.com/download/VS18/binaries/httpd.zip");
    assert(resolveURLReference("http://127.0.0.1:8080", "/download/VS18/httpd.zip")
        == "http://127.0.0.1:8080/download/VS18/httpd.zip");
}
