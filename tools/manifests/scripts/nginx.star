# Locates nginx source releases for `polka build`. nginx publishes PGP signatures
# but no checksums, so the source is marked unverified and relies on TLS.

_SOURCE_PATTERN = r"nginx-([0-9]+\.[0-9]+\.[0-9]+)\.tar\.gz"

def build_sources(ctx):
    index_url = ctx.vars["source-index"]
    versions = [match[1] for match in re.find_all(_SOURCE_PATTERN, http.get(index_url))]

    resolved = version.best(versions, ctx.requested)
    if resolved == None:
        fail("resolve nginx version %r: no matching source release found" % ctx.requested)

    filename = "nginx-%s.tar.gz" % resolved
    return {
        "version": resolved,
        "sources": {
            "nginx": {
                "url": url.resolve(index_url, filename),
                "filename": filename,
                "unverified": True,
            },
        },
    }
