# Locates Redis source releases for `polka build`. Redis publishes its release
# hashes in a separate repository, with one line per release.

_HASH_LINE = r"(?m)^hash redis-([0-9]+\.[0-9]+\.[0-9]+)\.tar\.gz sha256 ([0-9a-fA-F]{64}) "

def build_sources(ctx):
    checksums = {}
    for match in re.find_all(_HASH_LINE, http.get(ctx.vars["source-hashes"])):
        checksums[match[1]] = match[2]

    resolved = version.best(list(checksums.keys()), ctx.requested)
    if resolved == None:
        fail("resolve redis version %r: no matching source release found" % ctx.requested)

    filename = "redis-%s.tar.gz" % resolved
    return {
        "version": resolved,
        "sources": {
            "redis": {
                # The hash list links over plain HTTP; the same host serves HTTPS.
                "url": ctx.vars["source-base"] + filename,
                "filename": filename,
                "checksum_algorithm": "sha256",
                "checksum": checksums[resolved],
            },
        },
    }
