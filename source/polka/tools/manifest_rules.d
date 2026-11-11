module polka.tools.manifest_rules;

import std.algorithm : startsWith;
import std.format : format;
import std.regex : matchFirst, regex;
import std.string : join, split, strip;

import polka.tools.types;

/// Reports whether a partial version label (e.g. `"4"` or `"4.1"`) can still satisfy
/// the `[min, below)` constraint once resolved.
bool constraintAllowsLabel(const ManifestVersionConstraint c, string label) @safe pure
{
    auto normalized = trimVersionVPrefix(label);
    if (normalized.length == 0 || c.isZero())
        return true;

    auto minVer = c.min.strip();
    if (minVer.length > 0)
    {
        auto truncatedMin = truncateVersionParts(minVer, versionPartCount(normalized));
        if (compareVersions(normalized, truncatedMin) < 0)
            return false;
    }

    auto belowVer = c.below.strip();
    if (belowVer.length > 0)
    {
        if (compareVersions(normalized, belowVer) >= 0)
            return false;
    }

    return true;
}

/// Reports whether a fully resolved version satisfies `[min, below)`.
bool constraintAllowsResolved(const ManifestVersionConstraint c, string ver) @safe pure
{
    auto normalized = trimVersionVPrefix(ver);
    if (normalized.length == 0 || c.isZero())
        return true;

    auto minVer = c.min.strip();
    if (minVer.length > 0 && compareVersions(normalized, minVer) < 0)
        return false;

    auto belowVer = c.below.strip();
    if (belowVer.length > 0 && compareVersions(normalized, belowVer) >= 0)
        return false;

    return true;
}

/// Formats the user-facing constraint violation message for `tool` and `ver`.
string formatConstraintError(const ManifestVersionConstraint c, string tool, string ver) @safe
{
    auto minVer = c.min.strip();
    auto belowVer = c.below.strip();
    if (minVer.length > 0 && belowVer.length > 0)
        return format(`%s version "%s" is unsupported: requires >= %s and < %s`, tool, ver, minVer, belowVer);
    if (minVer.length > 0)
        return format(`%s version "%s" is unsupported: requires >= %s`, tool, ver, minVer);
    if (belowVer.length > 0)
        return format(`%s version "%s" is unsupported: requires < %s`, tool, ver, belowVer);
    return "";
}

private string trimVersionVPrefix(string raw) @safe pure
{
    auto trimmed = raw.strip();
    if (trimmed.startsWith("v"))
        return trimmed[1 .. $].strip();
    return trimmed;
}

/// Validates a declarative wrapper literal argument token so no shell escaping is needed.
bool isValidWrapperArg(string arg) @safe
{
    auto re = regex(`^[A-Za-z0-9._:=/@+-]+$`);
    return !arg.matchFirst(re).empty;
}

/// Computes built-in `{major}`, `{minor}`, and `{major_minor}` template values from a version.
string[string] derivedTemplateValues(string ver) @safe pure
{
    auto trimmed = trimVersionVPrefix(ver);
    string[string] values = [
        "major": "",
        "minor": "",
        "major_minor": versionMajorMinor(trimmed),
    ];
    if (trimmed.length == 0)
        return values;

    auto parts = trimmed.split(".");
    if (parts.length >= 1)
        values["major"] = parts[0];
    if (parts.length >= 2)
        values["minor"] = parts[1];
    return values;
}

/// Renders the platform-specific wrapper script invoking `targetFile` with `args`.
string wrapperScript(string goos, string targetFile, const(string)[] args) @safe pure
{
    string argSuffix = "";
    if (args.length > 0)
        argSuffix = " " ~ args.join(" ");

    if (goos.strip() == "windows")
    {
        return "@echo off\r\n" ~
            "setlocal\r\n" ~
            `call "%~dp0` ~ targetFile ~ `"` ~ argSuffix ~ " %*\r\n" ~
            "exit /b %ERRORLEVEL%\r\n";
    }

    return "#!/bin/sh\n" ~
        "set -eu\n" ~
        `SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)` ~ "\n" ~
        `exec "$SCRIPT_DIR/` ~ targetFile ~ `"` ~ argSuffix ~ " \"$@\"\n";
}

unittest
{
    import std.algorithm : canFind;

    // TestManifestVersionConstraintAllowsLabelAndResolved
    auto constraint = ManifestVersionConstraint("4.0.4", "5");

    struct LabelCase { string label; bool want; }
    foreach (tc; [
        LabelCase("", true),
        LabelCase("4", true),
        LabelCase("v4", true),
        LabelCase("4.0", true),
        LabelCase("4.0.4", true),
        LabelCase("4.3", true),
        LabelCase("4.0.3", false),
        LabelCase("3", false),
        LabelCase("3.13", false),
        LabelCase("5", false),
        LabelCase("5.0", false),
        LabelCase("5.1.0", false),
    ])
    {
        assert(constraintAllowsLabel(constraint, tc.label) == tc.want, tc.label);
    }

    struct ResolvedCase { string ver; bool want; }
    foreach (tc; [
        ResolvedCase("4.0.4", true),
        ResolvedCase("v4.3.2", true),
        ResolvedCase("4.99.99", true),
        ResolvedCase("4.0.3", false),
        ResolvedCase("4.0", false),
        ResolvedCase("3.13.7", false),
        ResolvedCase("5.0.0", false),
    ])
    {
        assert(constraintAllowsResolved(constraint, tc.ver) == tc.want, tc.ver);
    }

    auto msg = formatConstraintError(constraint, "rabbitmq", "3.13");
    assert(msg.canFind("requires >= 4.0.4 and < 5"));

    // TestDerivedTemplateValues
    auto got = derivedTemplateValues("v8.4.9");
    assert(got["major"] == "8");
    assert(got["minor"] == "4");
    assert(got["major_minor"] == "8.4");

    auto single = derivedTemplateValues("24");
    assert(single["major"] == "24" && single["minor"] == "" && single["major_minor"] == "24");

    // TestWrapperScriptRendersWindowsAndPOSIX
    auto win = wrapperScript("windows", "corepack.cmd", ["yarn"]);
    assert(win == "@echo off\r\nsetlocal\r\ncall \"%~dp0corepack.cmd\" yarn %*\r\nexit /b %ERRORLEVEL%\r\n");

    auto posix = wrapperScript("linux", "frankenphp", ["php-cli"]);
    assert(posix == "#!/bin/sh\nset -eu\nSCRIPT_DIR=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" && pwd)\nexec \"$SCRIPT_DIR/frankenphp\" php-cli \"$@\"\n");
}
