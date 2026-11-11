module polka.tools.resolve;

import std.file : exists, isDir;
import std.format : format;
import std.path : buildPath;

import polka.tools.registry;

/// Resolves the first existing executable binary for `tool` at `version` under `root`.
string resolveInstalledTool(Registry registry, string root, string tool, string ver) @trusted
{
    auto candidates = registry.installCandidates(root, tool, ver);
    if (candidates.length == 0)
        throw new Exception(format(`unsupported tool "%s"`, tool));

    foreach (candidate; candidates)
    {
        if (exists(candidate) && !isDir(candidate))
            return candidate;
    }

    throw new Exception(format(`%s version "%s" is not installed under %s`, tool, ver, buildPath(root, tool, ver)));
}
