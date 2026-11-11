module polka.tools.manifest_request;

import std.string : strip, toLower;

import polka.support.fs : currentArch, currentOS;
import polka.support.http : HTTPClient;
import polka.tools.download_io;
import polka.tools.github_archive;
import polka.tools.manifest_rules;
import polka.tools.types;

/// Resolved version, tag, asset, and optional commit SHA for downloading a manifest-declared tool.
struct ManifestRequestPlan
{
    string resolvedVersion;
    string tag;
    DownloadAsset asset;
    string commit;
}

/// Resolves version, asset, digests, and optional GitHub tag commit for a manifest tool request.
ManifestRequestPlan resolveManifestRequest(
    HTTPClient client,
    const PluginManifest m,
    string requestedVersion,
    string goos,
    string goarch
) @trusted
{
    auto tool = m.id.strip().toLower();
    string resolvedVersion;
    string tag;
    GitHubReleaseAsset[] githubAssets;
    resolveManifestDownloadVersion(client, tool, requestedVersion, m.download, resolvedVersion, tag, githubAssets);

    if (!constraintAllowsResolved(m.versionConstraint, resolvedVersion))
        throw new Exception(formatConstraintError(m.versionConstraint, tool, resolvedVersion));

    auto asset = resolveManifestDownloadAsset(tool, m.download.assets, requestedVersion, resolvedVersion, tag, goos, goarch, null);
    if (githubAssets.length > 0)
        applyGitHubAssetDigest(asset, githubAssets);

    ManifestRequestPlan plan;
    plan.resolvedVersion = resolvedVersion;
    plan.tag = tag;
    plan.asset = asset;

    if (asset.checksumAlgorithm == ChecksumAlgorithm.embeddedCommitID)
        plan.commit = resolveGitHubTagCommit(client, tool, m.download.github, tag);

    return plan;
}

/// Downloads and caches a tool payload described by `m` using `ctx`.
void downloadManifestRequest(DownloadContext ctx, const PluginManifest m) @trusted
{
    auto tool = m.id.strip().toLower();
    auto plan = resolveManifestRequest(ctx.client, m, ctx.version_, currentOS(), currentArch());
    if (plan.asset.checksumAlgorithm == ChecksumAlgorithm.embeddedCommitID)
    {
        downloadGitHubSourceArchive(ctx.client, ctx.cacheDir, tool, ctx.version_, plan.resolvedVersion, plan.asset, plan.commit);
        return;
    }

    downloadManifestAsset(ctx.client, ctx.cacheDir, tool, ctx.version_, plan.resolvedVersion, plan.asset);
}
