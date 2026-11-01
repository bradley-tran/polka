# Resolves PostgreSQL major labels through the postgresql.org versions index.
# Windows downloads come from EnterpriseDB. Linux downloads come from Zonky's
# Maven artifacts, which the manifest unwraps with download.assets.*.unwrap.

def _release_versions(entries):
    """Returns major.minor labels for releases upstream still supports."""
    versions = []
    for entry in entries:
        major = str(entry.get("major", "")).strip()
        minor = str(entry.get("latestMinor", "")).strip()
        if entry.get("supported") and major and minor:
            versions.append(major + "." + minor)
    return versions

def _maven_version(label):
    """Zonky publishes three-part versions, so 17.6 becomes 17.6.0."""
    if label.count(".") == 1:
        return label + ".0"
    return label

def resolve(ctx):
    resolved = ctx.requested
    # A full major.minor label is used as given, without contacting upstream.
    if "." not in resolved:
        resolved = version.best(_release_versions(http.get_json(ctx.vars["versions-index"])), resolved)
        if resolved == None:
            fail("resolve postgresql version %r: no matching release found" % ctx.requested)

    return {
        "version": resolved,
        "values": {"maven_version": _maven_version(resolved)},
    }
