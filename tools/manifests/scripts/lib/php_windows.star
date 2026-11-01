# Selects builds from the windows.php.net release index. The php, php-zts, and
# php-devel scripts share this so a runtime and its devel pack always come from
# the same variant.

# Newest compiler first. Flavors never cross: a thread-safe request must not
# fall back to a non-thread-safe build, or the reverse.
_COMPILERS = ["vs17", "vs16", "vc15"]

def _series(label):
    """Returns the major.minor release index key, so 8.4.5 becomes 8.4."""
    return ".".join(label.split(".")[:2])

def _asset_path(variant, key):
    """Returns the path of one of a variant's assets, or an empty string."""
    if type(variant) != "dict":
        return ""
    asset = variant.get(key)
    if type(asset) != "dict":
        return ""
    return asset.get("path", "").strip()

def select_build(index_url, requested, thread_safe):
    """Returns (release version, variant) for a requested runtime version.

    A request resolves to the current release of its series, so 8.4.5 selects
    whatever 8.4 release the index publishes.
    """
    release = http.get_json(index_url).get(_series(requested))
    if release == None:
        fail("php version %r is not available in the Windows release index" % requested)

    flavor = "ts" if thread_safe else "nts"
    for compiler in _COMPILERS:
        variant = release.get("%s-%s-x64" % (flavor, compiler))
        if _asset_path(variant, "zip"):
            return release["version"], variant
    fail("no compatible Windows PHP %s binary found for %s on x64" % (flavor.upper(), release["version"]))

def has_asset(variant, key):
    """Reports whether a variant publishes the asset with a checksum."""
    return bool(_asset_path(variant, key)) and bool(variant[key].get("sha256", "").strip())

def asset_plan(index_url, version_label, variant, key):
    """Returns the resolve result for one asset of a selected variant."""
    path = _asset_path(variant, key)
    return {
        "version": version_label,
        "url": url.resolve(index_url, path),
        "checksum": variant[key].get("sha256"),
        "values": {"php_filename": path.split("/")[-1]},
    }
