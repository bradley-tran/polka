# Resolves Apache httpd Windows builds from the Apache Lounge download page, and
# locates the upstream source releases `polka build` compiles on other platforms.

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

def _source_versions(index_url, name):
    """Lists the versions with a tar.gz source and a sha256 in a release index."""
    pattern = r"%s-([0-9]+\.[0-9]+\.[0-9]+)\.tar\.gz\.sha256" % name
    return [match[1] for match in re.find_all(pattern, http.get(index_url))]

def _source(index_url, name, resolved):
    filename = "%s-%s.tar.gz" % (name, resolved)
    source_url = url.resolve(index_url, filename)
    return {
        "url": source_url,
        "filename": filename,
        "checksum_algorithm": "sha256",
        "checksum_url": source_url + ".sha256",
    }

def build_sources(ctx):
    # The current index only lists the newest release, so a pinned older version
    # is found in the archive. The archive is the slower host, so it comes second.
    index_url = ctx.vars["source-index"]
    resolved = version.best(_source_versions(index_url, "httpd"), ctx.requested)
    if resolved == None:
        index_url = ctx.vars["source-archive"]
        resolved = version.best(_source_versions(index_url, "httpd"), ctx.requested)
    if resolved == None:
        fail("resolve apache version %r: no matching source release found" % ctx.requested)

    apr_index = ctx.vars["apr-index"]
    apr = version.best(_source_versions(apr_index, "apr"), "1")
    apr_util = version.best(_source_versions(apr_index, "apr-util"), "1")
    if apr == None or apr_util == None:
        fail("resolve apache dependencies: no APR 1.x source release found")

    return {
        "version": resolved,
        "sources": {
            "httpd": _source(index_url, "httpd", resolved),
            "apr": _source(apr_index, "apr", apr),
            "apr-util": _source(apr_index, "apr-util", apr_util),
        },
    }
