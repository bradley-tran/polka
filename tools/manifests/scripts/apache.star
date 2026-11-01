# Resolves Apache httpd Windows builds from the Apache Lounge download page.

# Archive links; preferred because they carry the real published path.
_HREF_PATTERN = r"""(?i)href=["']([^"']*?(httpd-([0-9]+\.[0-9]+\.[0-9]+)-([0-9]+)-Win64-VS([0-9]+)\.zip))["']"""

# Bare archive names, used when the page mentions a build without a link.
_FILE_PATTERN = r"(?i)httpd-([0-9]+\.[0-9]+\.[0-9]+)-([0-9]+)-Win64-VS([0-9]+)\.zip"

def _releases(page):
    """Lists unique (version, build, vs) releases, linked ones first."""
    seen = {}
    releases = []
    candidates = []
    for match in re.find_all(_HREF_PATTERN, page):
        candidates.append({"version": match[3], "build": match[4], "vs": match[5], "href": match[1]})
    for match in re.find_all(_FILE_PATTERN, page):
        candidates.append({"version": match[1], "build": match[2], "vs": match[3], "href": ""})

    for release in candidates:
        key = (release["version"], release["build"], release["vs"])
        if key in seen:
            continue
        seen[key] = True
        releases.append(release)
    return releases

def _compare_numeric(left, right):
    """Compares numeric strings by value, falling back to text order."""
    if left.isdigit() and right.isdigit():
        left_value, right_value = int(left), int(right)
        if left_value > right_value:
            return 1
        if left_value < right_value:
            return -1
        return 0
    if left > right:
        return 1
    if left < right:
        return -1
    return 0

def _compare_release(left, right):
    """Orders releases by version, then build date, then compiler."""
    result = version.compare(left["version"], right["version"])
    if result != 0:
        return result
    result = _compare_numeric(left["build"], right["build"])
    if result != 0:
        return result
    return _compare_numeric(left["vs"], right["vs"])

def resolve(ctx):
    page_url = ctx.vars["download-page"]
    best = None
    for release in _releases(http.get(page_url)):
        if not version.matches(release["version"], ctx.requested):
            continue
        if best == None or _compare_release(release, best) > 0:
            best = release
    if best == None:
        fail("resolve apache version %r: no matching Apache Lounge release found" % ctx.requested)

    plan = {
        "version": best["version"],
        "values": {"build": best["build"], "vs": best["vs"]},
    }
    href = html.unescape(best["href"]).strip()
    if href:
        # Follow the published link and its .txt SHA-256 sidecar instead of
        # rebuilding the path from the manifest template.
        archive_url = url.resolve(page_url, href)
        plan["url"] = archive_url
        plan["checksum_url"] = archive_url + ".txt"
        plan["checksum_algorithm"] = "sha256"
    return plan
