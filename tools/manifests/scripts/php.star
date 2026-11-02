# Resolves php and php-zts runtime builds, and adds the Windows payloads that
# native extension builds need. Only Windows has prebuilt downloads; other
# platforms compile from the source releases build_sources locates.

load("php_windows.star", "asset_plan", "select_build")

def dependencies(env):
    if env.os != "windows" or not env.setting("extension-sdk"):
        return []
    return [
        {"tool": "php-devel", "version": env.version},
        {"tool": "php-sdk", "version": env.vars["php-sdk-version"]},
    ]

def resolve(ctx):
    if ctx.os != "windows":
        fail("automatic php download is only implemented on Windows")

    index_url = ctx.vars["release-index"]
    label, variant = select_build(index_url, ctx.requested, ctx.vars["flavor"] == "zts")
    return asset_plan(index_url, label, variant, "zip")

def build_sources(ctx):
    release = http.get_json("%s?json&version=%s" % (ctx.vars["source-releases"], ctx.requested))
    if "error" in release or "version" not in release:
        fail("resolve php version %r: no matching source release found" % ctx.requested)
    if not version.matches(release["version"], ctx.requested):
        fail("resolve php version %r: php.net returned %s" % (ctx.requested, release["version"]))

    for source in release["source"]:
        if source["filename"].endswith(".tar.gz"):
            return {
                "version": release["version"],
                "sources": {
                    "php": {
                        "url": ctx.vars["source-base"] + source["filename"],
                        "filename": source["filename"],
                        "checksum_algorithm": "sha256",
                        "checksum": source["sha256"],
                    },
                },
            }
    fail("resolve php version %r: php.net lists no tar.gz source" % ctx.requested)
