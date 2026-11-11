module polka.plugins.framework_actions;

import std.algorithm : canFind;
import std.conv : to;
import std.file : FileException, exists, mkdirRecurse, readText, remove, rename, write;
import std.format : format;
import std.path : absolutePath, baseName, buildNormalizedPath, buildPath, dirName, isRooted, relativePath;
import std.regex : Captures, regex, replaceAll;
import std.string : fromStringz, indexOf, replace, startsWith, strip, toStringz;

import polka.config;
import polka.plugins.ids;
import polka.plugins.framework_composer;
import polka.plugins.framework_php;
import polka.plugins.manifest_values;
import polka.support.fs : currentOS, fromSlashPath, octal, randomHexSuffix, removeQuiet, setFileMode, toSlashPath;
import polka.support.yaml;

version (Posix)
{
    import core.stdc.errno : ENOENT, errno;
    import core.sys.posix.stdlib : realpath;
}
version (Windows)
{
    import core.sys.windows.winbase : GetFileAttributesW, GetLastError, SetFileAttributesW, FILE_ATTRIBUTE_DIRECTORY, FILE_ATTRIBUTE_READONLY, INVALID_FILE_ATTRIBUTES;
    import core.sys.windows.winerror : ERROR_FILE_NOT_FOUND, ERROR_PATH_NOT_FOUND;
    import std.utf : toUTF16z;
}

/// Regex matching declarative template placeholders `{{name}}`.
package(polka.plugins) auto frameworkPlaceholderRe() @safe
{
    return regex(`\{\{([A-Za-z0-9_.-]+)\}\}`);
}

/// Executes declarative `post-composer` actions in memory before committing files.
void frameworkManifestPostComposer(
    const PostComposerContext ctx,
    const FrameworkManifest m,
    const FrameworkAssetSource files
) @trusted
{
    if (m.postComposer.actions.length == 0)
        return;

    auto state = new FrameworkValueState(m, ctx.environment, ctx.database);
    if (m.postComposer.requiresDatabase && state.database is null)
        return;

    string root;
    try
    {
        root = frameworkComposerAppRoot(ctx, m.appRoot);
    }
    catch (Exception e)
    {
        throw new Exception(format("framework %s application root: %s", formatGoQuoted(m.id), e.msg));
    }

    foreach (i, action; m.postComposer.actions)
    {
        auto base = root;
        if (action.base == "docroot")
            base = frameworkDocrootPath(ctx, root, m.appRoot.publicDir);
        try
        {
            runFrameworkAction(action, base, state, files);
        }
        catch (Exception e)
        {
            throw new Exception(format("framework %s action %d (%s, %s): %s",
                formatGoQuoted(m.id), i, action.type, action.path, e.msg));
        }
    }
}

/// Resolves symlink ancestors as well as lexical containment inside `base`.
/// Writes go through contained symlinks instead of replacing the symlink itself.
string frameworkContainedPath(string base, string relative) @trusted
{
    validateFrameworkManifestRelativePath(relative, "action path", false);
    auto absBase = cleanAbsolutePath(base);
    auto target = buildNormalizedPath(buildPath(absBase, fromSlashPath(relative)));
    auto resolvedBase = frameworkExistingPath(absBase);
    auto resolvedTarget = frameworkExistingPath(target);
    auto rel = relativePath(resolvedTarget, resolvedBase);
    auto slashRel = toSlashPath(rel);
    if (isRooted(rel) || slashRel == ".." || slashRel.startsWith("../"))
        throw new Exception("action path escapes its base");
    return resolvedTarget;
}

/// Resolves symlinks in the nearest existing ancestor of a possibly missing output path,
/// propagating failures other than non-existence (`ENOENT`).
string frameworkExistingPath(string filename) @trusted
{
    version (Posix)
    {
        char[4096] buf;
        auto res = realpath(filename.toStringz, buf.ptr);
        if (res !is null)
            return fromStringz(res).idup;
        auto err = errno;
        if (err != ENOENT)
            throw new Exception(format("resolve path %s: errno %d", filename, err));
    }
    else
    {
        if (exists(filename))
            return buildNormalizedPath(absolutePath(filename));
    }

    auto parent = dirName(filename);
    if (parent == filename || parent.length == 0)
        throw new Exception(format("resolve path %s: does not exist", filename));
    auto resolvedParent = frameworkExistingPath(parent);
    return buildNormalizedPath(buildPath(resolvedParent, baseName(filename)));
}

/// Attempts to read `filename`; returns `false` only if missing (`ENOENT`),
/// propagating any other error (such as directory / permission failures).
private bool tryReadFrameworkFile(string filename, out string contents) @trusted
{
    try
    {
        contents = readText(filename);
        return true;
    }
    catch (FileException e)
    {
        version (Posix)
        {
            if (e.errno == ENOENT)
                return false;
        }
        else version (Windows)
        {
            if (e.errno == ERROR_FILE_NOT_FOUND || e.errno == ERROR_PATH_NOT_FOUND)
                return false;
        }
        throw e;
    }
}

/// Reads the target file, falling back to an application fallback or embedded template.
/// Only file absence is recoverable.
string readFrameworkActionFile(
    const FrameworkActionManifest a,
    string base,
    string target,
    const FrameworkAssetSource files
) @trusted
{
    string data;
    if (tryReadFrameworkFile(target, data))
        return data;

    if (a.fallback.length > 0)
    {
        auto filename = frameworkContainedPath(base, a.fallback);
        if (tryReadFrameworkFile(filename, data))
            return data;
    }
    if (a.fallbackTemplateFile.length > 0)
        return frameworkTemplate(files, a.fallbackTemplateFile);
    if (a.type == "php-include")
        return "<?php\n";
    if (a.type != "dotenv")
        throw new Exception("missing PHP file and no fallback-template-file");
    return "";
}

/// Resolves all expressions and edits one complete target in memory before atomic write.
void runFrameworkAction(
    const FrameworkActionManifest a,
    string base,
    FrameworkValueState state,
    const FrameworkAssetSource files
) @trusted
{
    auto target = frameworkContainedPath(base, a.path);
    auto keys = orderedFrameworkKeys(a.values, a.order);
    YamlValue[string] values;
    foreach (key; keys)
    {
        try
        {
            values[key] = state.eval(a.values[key]);
        }
        catch (Exception e)
        {
            throw new Exception(format("resolve value %s: %s", formatGoQuoted(key), e.msg));
        }
    }

    string contents;
    if (a.type == "write-file")
        contents = frameworkTemplate(files, a.templateFile);
    else
        contents = readFrameworkActionFile(a, base, target, files);

    uint mode = octal!600;
    switch (a.type)
    {
        case "dotenv":
        {
            DotenvAssignment[] assignments;
            assignments.reserve(keys.length);
            foreach (key; keys)
                assignments ~= DotenvAssignment(key, frameworkScalarText(values[key]));
            auto eol = phpEOL(contents);
            contents = upsertDotenvAssignments(contents, assignments);
            if (eol == "\r\n")
                contents = contents.replace("\n", eol);
            break;
        }
        case "php-array":
        {
            foreach (update; a.updates)
            {
                foreach (key; orderedFrameworkKeys(update.values, update.order))
                {
                    YamlValue val;
                    try
                    {
                        val = state.eval(update.values[key]);
                    }
                    catch (Exception e)
                    {
                        throw new Exception(format("resolve array value %s: %s", formatGoQuoted(key), e.msg));
                    }
                    auto literal = renderPHPScalar(val);
                    contents = patchPHPArrayValue(contents, update.path ~ key, literal);
                }
            }
            break;
        }
        case "php-constants":
        {
            string[string] literals;
            foreach (key; keys)
                literals[key] = renderPHPScalar(values[key]);
            contents = patchPHPConstants(contents, literals, keys, a.insertBefore);
            break;
        }
        case "write-file":
        {
            string[string] bindings;
            foreach (key; keys)
            {
                if (a.encoding == "php-literal")
                    bindings[key] = renderPHPScalar(values[key]);
                else
                    bindings[key] = frameworkScalarText(values[key]);
            }
            // Replace once against the template so placeholder-like values never trigger a second pass.
            contents = replaceAll!((Captures!string m) {
                auto name = m[1];
                auto p = name in bindings;
                return p !is null ? *p : "";
            })(contents, frameworkPlaceholderRe());
            break;
        }
        case "php-include":
        {
            mode = octal!644;
            contents = patchPHPInclude(contents, a.include_, a.marker);
            break;
        }
        default:
            throw new Exception(format("unsupported action type %s", formatGoQuoted(a.type)));
    }

    writeFrameworkFile(target, cast(const(ubyte)[]) contents, mode);
}

/// Reads an embedded template asset by filename.
string frameworkTemplate(const FrameworkAssetSource files, string name) @safe
{
    return files.readFile("manifests/templates/" ~ name);
}

/// Atomically stages output beside `target` and cleans up temporary files on failure.
void writeFrameworkFile(string target, const(ubyte)[] data, uint mode) @trusted
{
    auto dir = dirName(target);
    if (dir.length > 0 && dir != "." && !exists(dir))
        mkdirRecurse(dir);

    auto staged = buildPath(dir, ".polka-config-" ~ randomHexSuffix(8));
    bool removeStaged = true;
    scope (exit)
    {
        if (removeStaged)
            removeQuiet(staged);
    }

    write(staged, data);
    setFileMode(staged, mode);
    replaceFrameworkFile(staged, target);
    removeStaged = false;
}

/// Replaces `target` with `staged`, temporarily clearing/restoring Windows read-only attributes.
void replaceFrameworkFile(string staged, string target) @trusted
{
    version (Windows)
    {
        auto wp = target.toUTF16z;
        auto attrs = GetFileAttributesW(wp);
        if (attrs != INVALID_FILE_ATTRIBUTES
            && (attrs & FILE_ATTRIBUTE_DIRECTORY) == 0
            && (attrs & FILE_ATTRIBUTE_READONLY) != 0)
        {
            if (SetFileAttributesW(wp, attrs & ~FILE_ATTRIBUTE_READONLY) == 0)
                throw new Exception(format("make framework config writable for replacement: win32 error %d", GetLastError()));

            Exception renameErr = null;
            try
            {
                rename(staged, target);
            }
            catch (Exception e)
            {
                renameErr = new Exception(format("rename %s to %s: %s", staged, target, e.msg));
            }
            if (SetFileAttributesW(target.toUTF16z, attrs) == 0)
            {
                auto restoreMsg = format("restore framework config read-only attribute: win32 error %d", GetLastError());
                if (renameErr !is null)
                    throw new Exception(renameErr.msg ~ "; " ~ restoreMsg);
                throw new Exception(restoreMsg);
            }
            if (renameErr !is null)
                throw renameErr;
            return;
        }
        else if (attrs == INVALID_FILE_ATTRIBUTES)
        {
            auto err = GetLastError();
            if (err != ERROR_FILE_NOT_FOUND && err != ERROR_PATH_NOT_FOUND)
                throw new Exception(format("stat framework config before replacement: win32 error %d", err));
        }
    }

    try
    {
        rename(staged, target);
    }
    catch (Exception e)
    {
        throw new Exception(format("rename %s to %s: %s", staged, target, e.msg));
    }
}

unittest
{
    import polka.support.fs : createTempDir, getFileMode, rmdirRecurseQuiet;

    // Test replaceFrameworkFile failure preserves original file & mode on Posix
    auto dir = createTempDir("polka-actions-test-");
    scope (exit) rmdirRecurseQuiet(dir);

    auto target = buildPath(dir, "settings.php");
    write(target, "original settings");
    setFileMode(target, octal!444);

    bool threwRename = false;
    try
    {
        replaceFrameworkFile(buildPath(dir, "missing-stage"), target);
    }
    catch (Exception e)
    {
        threwRename = e.msg.canFind("rename");
    }
    assert(threwRename);
    assert(readText(target) == "original settings");
    assert((getFileMode(target) & octal!200) == 0);
    setFileMode(target, octal!600);
}
