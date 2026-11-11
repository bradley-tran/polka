module polka.plugins.framework_composer;

import std.algorithm : canFind, countUntil;
import std.ascii : isAlpha, isDigit, isWhite;
import std.conv : to;
import std.file : exists;
import std.format : format;
import std.path : absolutePath, baseName, buildNormalizedPath, buildPath, dirName, isRooted;
import std.string : endsWith, icmp, indexOf, lastIndexOf, replace, split, startsWith, strip, stripLeft, toLower;
import std.uni : isAlphaNum, isUniAlpha = isAlpha, isNumber, isWhite_ = isWhite;

import polka.config;
import polka.plugins.ids;
import polka.support.fs : currentOS, toSlashPath, fromSlashPath;

version (Posix)
{
    import core.stdc.errno : ENOENT, errno;
    import core.sys.posix.sys.stat : S_IFMT, S_IFREG, stat, stat_t;
}
version (Windows)
{
    import core.sys.windows.winbase : GetFileAttributesW, GetLastError, INVALID_FILE_ATTRIBUTES, FILE_ATTRIBUTE_DIRECTORY;
    import core.sys.windows.winerror : ERROR_FILE_NOT_FOUND, ERROR_PATH_NOT_FOUND;
    import std.utf : toUTF16z;
}

/// One managed dotenv scalar assignment.
struct DotenvAssignment
{
    string name;
    string value;
}

/// Updates managed dotenv keys while retaining unrelated lines and comments.
string upsertDotenvAssignments(string contents, const(DotenvAssignment)[] assignments) @safe
{
    DotenvAssignment[string] valueByName;
    foreach (assignment; assignments)
        valueByName[assignment.name] = assignment;

    auto normalized = contents.replace("\r\n", "\n");
    if (normalized.endsWith("\n"))
        normalized = normalized[0 .. $ - 1];

    string[] lines;
    if (normalized.length > 0)
        lines = normalized.split("\n");

    bool[string] activeNames;
    foreach (line; lines)
    {
        string name;
        bool commented;
        if (parseDotenvAssignmentLine(line, name, commented) && !commented)
            activeNames[name] = true;
    }

    bool[string] written;
    foreach (index, line; lines)
    {
        string name;
        bool commented;
        if (!parseDotenvAssignmentLine(line, name, commented))
            continue;

        auto p = name in valueByName;
        if (p is null)
            continue;
        if (commented && ((name in activeNames) !is null || (name in written) !is null))
            continue;

        lines[index] = renderDotenvAssignment(*p);
        written[name] = true;
    }

    string[] missing;
    foreach (assignment; assignments)
    {
        if ((assignment.name in written) is null)
            missing ~= renderDotenvAssignment(assignment);
    }
    if (missing.length > 0 && lines.length > 0 && lines[$ - 1].strip().length > 0)
        lines ~= "";
    lines ~= missing;

    import std.array : join;
    return lines.join("\n") ~ "\n";
}

/// Recognizes active, exported, and commented dotenv assignment lines.
bool parseDotenvAssignmentLine(string line, out string name, out bool commented) @safe
{
    auto trimmed = stripLeftWhiteUnicode(line);
    commented = false;
    if (trimmed.startsWith("#"))
    {
        commented = true;
        trimmed = stripLeftWhiteUnicode(trimmed[1 .. $]);
    }
    if (trimmed.startsWith("export "))
        trimmed = stripLeftWhiteUnicode(trimmed["export ".length .. $]);

    auto eqIdx = trimmed.indexOf('=');
    if (eqIdx < 0)
        return false;
    name = trimmed[0 .. eqIdx].strip();
    return name.length > 0;
}

/// Renders `NAME=VALUE` with appropriate dotenv quoting.
string renderDotenvAssignment(DotenvAssignment assignment) @safe pure
{
    return assignment.name ~ "=" ~ renderDotenvValue(assignment.value);
}

/// Quotes and escapes characters with dotenv interpolation semantics.
string renderDotenvValue(string value) @safe pure
{
    if (value.length == 0 || dotenvValueNeedsQuotes(value))
    {
        auto escaped = value
            .replace(`\`, `\\`)
            .replace(`"`, `\"`)
            .replace("\n", `\n`)
            .replace("\r", `\r`)
            .replace("\t", `\t`)
            .replace("$", `\$`);
        return `"` ~ escaped ~ `"`;
    }
    return value;
}

/// Reports whether `value` contains characters requiring quotes in a `.env` file.
bool dotenvValueNeedsQuotes(string value) @safe pure
{
    foreach (dchar ch; value)
    {
        if (isUniAlpha(ch) || isNumber(ch))
            continue;
        switch (ch)
        {
            case '_', '-', '.', '/', ':', '@':
                continue;
            default:
                return true;
        }
    }
    return false;
}

private string stripLeftWhiteUnicode(string s) @safe pure
{
    import std.utf : decode;
    size_t i = 0;
    while (i < s.length)
    {
        size_t next = i;
        dchar ch = decode(s, next);
        if (!isWhite_(ch))
            break;
        i = next;
    }
    return s[i .. $];
}

/// Escapes data using PHP single-quoted string rules.
string phpSingleQuotedString(string value) @safe pure
{
    auto escaped = value.replace(`\`, `\\`).replace(`'`, `\'`);
    return "'" ~ escaped ~ "'";
}

/// Checks whether any of `markers` exists under `dir` as a regular file.
/// Missing paths are non-matches (`false`); other filesystem failures throw.
bool frameworkMarkersExist(string dir, const(string)[] markers) @trusted
{
    foreach (marker; markers)
    {
        if (dir.canFind('\0') || marker.canFind('\0'))
            throw new Exception("stat marker: invalid NUL byte in path");

        auto filename = buildPath(dir, fromSlashPath(marker));
        version (Posix)
        {
            import std.string : toStringz;
            stat_t st;
            if (stat(filename.toStringz, &st) != 0)
            {
                if (errno == ENOENT)
                    continue;
                throw new Exception(format("stat marker %s: errno %d", filename, errno));
            }
            if ((st.st_mode & S_IFMT) == S_IFREG)
                return true;
        }
        else version (Windows)
        {
            auto attrs = GetFileAttributesW(filename.toUTF16z);
            if (attrs == INVALID_FILE_ATTRIBUTES)
            {
                auto err = GetLastError();
                if (err == ERROR_FILE_NOT_FOUND || err == ERROR_PATH_NOT_FOUND)
                    continue;
                throw new Exception(format("stat marker %s: win32 error %d", filename, err));
            }
            if ((attrs & FILE_ATTRIBUTE_DIRECTORY) == 0)
                return true;
        }
    }
    return false;
}

/// Resolves application root using Composer target, docroot suffix, working dir, and project dir precedence.
string frameworkComposerAppRoot(const PostComposerContext ctx, const FrameworkAppRootConfig root) @trusted
{
    string[] candidates;
    if (auto target = composerCreateProjectDirectory(ctx.args, ctx.workingDir))
    {
        if (target.length > 0)
            candidates ~= target;
    }
    if (auto docroot = frameworkAppRootFromDocroot(ctx.projectDir, ctx.environment.docroot, root.publicDir))
    {
        if (docroot.length > 0)
            candidates ~= docroot;
    }
    if (auto wdir = cleanAbsolutePath(ctx.workingDir))
    {
        if (wdir.length > 0)
            candidates ~= wdir;
    }
    if (auto pdir = cleanAbsolutePath(ctx.projectDir))
    {
        if (pdir.length > 0)
            candidates ~= pdir;
    }
    foreach (candidate; dedupePaths(candidates))
    {
        if (frameworkMarkersExist(candidate, root.markers))
            return candidate;
    }
    if (candidates.length > 0)
        return candidates[0];
    return ".";
}

/// Removes a declared public-directory suffix (including multi-directory layouts) from absolute or relative docroots.
string frameworkAppRootFromDocroot(string projectDir, string docroot, string publicDir) @safe
{
    projectDir = cleanAbsolutePath(projectDir);
    auto trimmed = docroot.strip();
    if (trimmed.length == 0)
        return projectDir;

    auto cleanDocroot = buildNormalizedPath(fromSlashPath(trimmed));
    auto appRoot = cleanDocroot;
    if (publicDir != "." && frameworkPathHasSuffix(cleanDocroot, publicDir))
    {
        auto suffix = buildNormalizedPath(fromSlashPath(publicDir));
        while (suffix != "." && suffix.length > 0)
        {
            appRoot = dirName(appRoot);
            suffix = dirName(suffix);
        }
    }
    if (isRooted(appRoot))
        return buildNormalizedPath(appRoot);
    return buildNormalizedPath(buildPath(projectDir, appRoot));
}

/// Compares complete path segments instead of raw string suffixes so `"notpublic"` never matches `"public"`.
bool frameworkPathHasSuffix(string filename, string suffix) @safe
{
    filename = buildNormalizedPath(filename);
    suffix = buildNormalizedPath(fromSlashPath(suffix));
    if (suffix == ".")
        return filename == ".";
    while (suffix != "." && suffix.length > 0 && dirName(suffix) != suffix)
    {
        if (icmp(baseName(filename), baseName(suffix)) != 0)
            return false;
        auto nextF = dirName(filename);
        auto nextS = dirName(suffix);
        if (nextF == filename && nextS != suffix)
            return false;
        filename = nextF;
        suffix = nextS;
    }
    return suffix == ".";
}

/// Resolves the document root path honoring explicit custom docroots and normal framework suffixes.
string frameworkDocrootPath(const PostComposerContext ctx, string appRoot, string publicDir) @safe
{
    auto trimmed = ctx.environment.docroot.strip();
    if (trimmed.length == 0)
        return buildNormalizedPath(buildPath(appRoot, fromSlashPath(publicDir)));
    auto cleanDocroot = buildNormalizedPath(fromSlashPath(trimmed));
    if (isRooted(cleanDocroot))
        return cleanDocroot;
    if (frameworkPathHasSuffix(cleanDocroot, publicDir))
        return buildNormalizedPath(buildPath(appRoot, fromSlashPath(publicDir)));
    return buildNormalizedPath(buildPath(cleanAbsolutePath(ctx.projectDir), cleanDocroot));
}

/// Parses the Composer `create-project` target directory relative to `workingDir`.
/// `args` must include the `create-project` verb.
string composerCreateProjectDirectory(const(string)[] args, string workingDir) @safe
{
    ptrdiff_t commandIndex = -1;
    foreach (index, arg; args)
    {
        if (icmp(arg.strip(), "create-project") == 0)
        {
            commandIndex = cast(ptrdiff_t) index;
            break;
        }
    }
    if (commandIndex == -1)
        return "";

    string[] positionals;
    foreach (arg; args[commandIndex + 1 .. $])
    {
        auto trimmed = arg.strip();
        if (trimmed.length == 0 || trimmed.startsWith("-"))
            continue;
        positionals ~= trimmed;
    }
    if (positionals.length == 0)
        return "";

    string directory;
    if (positionals.length > 1)
        directory = positionals[1];
    else
        directory = composerPackageDirectoryName(positionals[0]);

    if (directory.strip().length == 0)
        return "";
    if (isRooted(directory))
        return buildNormalizedPath(directory);

    auto base = cleanAbsolutePath(workingDir);
    if (base.length == 0)
        base = ".";
    return buildNormalizedPath(buildPath(base, fromSlashPath(directory)));
}

/// Public PascalCase alias matching Go's `ComposerCreateProjectDirectory`.
alias ComposerCreateProjectDirectory = composerCreateProjectDirectory;

/// Removes vendor and version constraint parts from a Composer package name.
string composerPackageDirectoryName(string name) @safe pure
{
    auto trimmed = name.strip();
    while (trimmed.startsWith("/"))
        trimmed = trimmed[1 .. $];
    while (trimmed.endsWith("/"))
        trimmed = trimmed[0 .. $ - 1];
    if (trimmed.length == 0)
        return "";
    auto slash = trimmed.lastIndexOf('/');
    if (slash >= 0)
        trimmed = trimmed[slash + 1 .. $];
    auto colon = trimmed.indexOf(':');
    if (colon >= 0)
        trimmed = trimmed[0 .. colon];
    return trimmed;
}

/// Normalizes root candidates to clean absolute paths, retaining a clean relative fallback.
string cleanAbsolutePath(string path) @safe
{
    auto trimmed = path.strip();
    if (trimmed.length == 0)
        return "";
    try
    {
        return buildNormalizedPath(absolutePath(trimmed));
    }
    catch (Exception)
    {
        return buildNormalizedPath(trimmed);
    }
}

/// Preserves candidate precedence while deduplicating (case-insensitively on Windows).
string[] dedupePaths(const(string)[] paths) @safe
{
    bool[string] seen;
    string[] deduped;
    foreach (p; paths)
    {
        auto clean = cleanAbsolutePath(p);
        if (clean.length == 0)
            continue;
        auto key = currentOS() == "windows" ? clean.toLower() : clean;
        if ((key in seen) !is null)
            continue;
        seen[key] = true;
        deduped ~= clean;
    }
    return deduped;
}

unittest
{
    // Dotenv escaping and upsert preservation
    auto envIn = "APP_NAME=Acme\n# DB_HOST=old\nDB_HOST=active\n";
    auto envOut = upsertDotenvAssignments(envIn, [
        DotenvAssignment("DB_HOST", "127.0.0.1"),
        DotenvAssignment("DB_PASSWORD", "a\\b\"$x\ny"),
    ]);
    assert(envOut.canFind("APP_NAME=Acme\n"));
    assert(envOut.canFind("# DB_HOST=old\n"));
    assert(envOut.canFind("DB_HOST=127.0.0.1\n"));
    assert(envOut.canFind(`DB_PASSWORD="a\\b\"\$x\ny"`));

    assert(phpSingleQuotedString(`a'\b`) == `'a\'\\b'`);
    assert(composerPackageDirectoryName("laravel/laravel:^12.0") == "laravel");

    assert(frameworkPathHasSuffix("site/web/public", "web/public"));
    assert(!frameworkPathHasSuffix("site/notpublic", "public"));

    // NUL byte check in frameworkMarkersExist must throw instead of silently returning false
    bool threwNul = false;
    try { frameworkMarkersExist("bad\0directory", ["artisan"]); } catch (Exception) { threwNul = true; }
    assert(threwNul);
}
