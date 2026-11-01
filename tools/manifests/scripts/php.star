# Resolves php and php-zts runtime builds, and adds the Windows payloads that
# native extension builds need. Only Windows has prebuilt downloads.

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
