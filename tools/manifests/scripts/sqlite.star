# Resolves SQLite command-line tool releases from the machine-readable
# PRODUCT lines embedded in the sqlite.org download page.

# Archive name prefixes per platform; other platforms have no prebuilt tools.
_SOURCE_PREFIXES = {
    "windows-amd64": "sqlite-tools-win-x64-",
    "linux-amd64": "sqlite-tools-linux-x64-",
}

def _products(page):
    """Parses `PRODUCT,<version>,<relative url>,<size>,<sha3>` lines."""
    products = []
    for line in page.split("\n"):
        line = line.strip()
        if not line.startswith("PRODUCT,"):
            continue
        fields = line.split(",")
        if len(fields) < 5:
            continue
        products.append({
            "version": fields[1].strip(),
            "relative_url": fields[2].strip(),
            "sha3": fields[4].strip(),
        })
    return products

def _pad2(number):
    """Zero-pads a non-negative integer to at least two digits."""
    text = str(number)
    if len(text) < 2:
        return "0" + text
    return text

def _numeric_version(label):
    """Converts 3.53.2 to SQLite's archive numbering, 3530200."""
    parts = label.split(".")
    if len(parts) < 3 or not (parts[0].isdigit() and parts[1].isdigit() and parts[2].isdigit()):
        return label.replace(".", "")
    return str(int(parts[0])) + _pad2(int(parts[1])) + _pad2(int(parts[2])) + "00"

def resolve(ctx):
    prefix = _SOURCE_PREFIXES.get(ctx.platform)
    if not prefix:
        fail("sqlite is not available for %s" % ctx.platform)

    best = None
    for product in _products(http.get(ctx.vars["download-page"])):
        if prefix not in product["relative_url"].split("/")[-1]:
            continue
        if not version.matches(product["version"], ctx.requested):
            continue
        if best == None or version.compare(product["version"], best["version"]) > 0:
            best = product
    if best == None:
        fail("resolve sqlite version %r: no matching release found" % ctx.requested)

    return {
        "version": best["version"],
        "values": {
            "sqlite_numeric": _numeric_version(best["version"]),
            "sqlite_relative_url": best["relative_url"],
        },
        "checksum": best["sha3"],
    }
