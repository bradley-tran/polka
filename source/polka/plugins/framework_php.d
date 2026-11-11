module polka.plugins.framework_php;

import std.algorithm : canFind, sort;
import std.array : appender, join;
import std.conv : to;
import std.format : format;
import std.math : isInfinity, isNaN;
import std.string : icmp, indexOf, lastIndexOf, replace, startsWith, strip, stripLeft, toLower;

import polka.plugins.framework_composer;
import polka.plugins.ids : formatGoQuoted;
import polka.support.yaml;

/// PHP token retaining exact byte spans so edits leave all unrelated source intact.
struct PHPToken
{
    string text;
    string kind;
    size_t start = 0;
    size_t end = 0;
}

/// Scanned PHP token stream and line/block comments.
struct PHPDocument
{
    PHPToken[] tokens;
    PHPToken[] comments;
}

/// Recognizes PHP tags, strings, and comments before punctuation, avoiding matches
/// against example configuration inside comments or quoted application text.
PHPDocument scanPHP(string src) @safe
{
    PHPDocument doc;
    bool inPHP = false;
    for (size_t i = 0; i < src.length;)
    {
        if (!inPHP)
        {
            auto n = src[i .. $].indexOf("<?");
            if (n < 0)
                break;
            i += cast(size_t) n;
            if (!src[i .. $].startsWith("<?php"))
                throw new Exception("unsupported PHP opening tag");
            doc.tokens ~= PHPToken("<?php", "open", i, i + 5);
            i += 5;
            inPHP = true;
            continue;
        }
        auto start = i;
        auto c = src[i];
        if (c == ' ' || c == '\t' || c == '\r' || c == '\n')
        {
            i++;
            continue;
        }
        if (src[i .. $].startsWith("?>"))
        {
            doc.tokens ~= PHPToken("?>", "close", i, i + 2);
            i += 2;
            inPHP = false;
            continue;
        }
        if (c == '#' || src[i .. $].startsWith("//"))
        {
            while (i < src.length && src[i] != '\n' && src[i] != '\r' && !src[i .. $].startsWith("?>"))
                i++;
            doc.comments ~= PHPToken(src[start .. i], "comment", start, i);
            continue;
        }
        if (src[i .. $].startsWith("/*"))
        {
            auto n = src[i + 2 .. $].indexOf("*/");
            if (n < 0)
                throw new Exception("unterminated PHP comment");
            i += cast(size_t) n + 4;
            doc.comments ~= PHPToken(src[start .. i], "comment", start, i);
            continue;
        }
        if (src[i .. $].startsWith("<<<") || c == '`')
            throw new Exception("unsupported PHP string syntax");
        if (c == '\'' || c == '"')
        {
            i++;
            bool closed = false;
            while (i < src.length)
            {
                if (src[i] == '\\')
                {
                    i += 2;
                    continue;
                }
                if (src[i] == c)
                {
                    i++;
                    closed = true;
                    break;
                }
                i++;
            }
            if (!closed)
                throw new Exception("unterminated PHP string");
            doc.tokens ~= PHPToken(src[start .. i], "string", start, i);
            continue;
        }
        if (isPHPIdentifier(cast(ubyte) c))
        {
            i++;
            while (i < src.length && (isPHPIdentifier(cast(ubyte) src[i]) || (src[i] >= '0' && src[i] <= '9')))
                i++;
            doc.tokens ~= PHPToken(src[start .. i], "identifier", start, i);
            continue;
        }
        i++;
        foreach (op; ["=>", "->", "::", "..."])
        {
            if (src[start .. $].startsWith(op))
            {
                i = start + op.length;
                break;
            }
        }
        doc.tokens ~= PHPToken(src[start .. i], "symbol", start, i);
    }
    if (doc.tokens.length == 0)
        throw new Exception("configuration has no PHP code");

    for (size_t i = 0; i < doc.tokens.length; i++)
    {
        auto txt = doc.tokens[i].text;
        if (txt == "[" || txt == "(" || txt == "{")
            i = phpMatching(doc.tokens, i);
        else if (txt == "]" || txt == ")" || txt == "}")
            throw new Exception("unbalanced PHP brackets");
    }
    return doc;
}

bool isPHPIdentifier(ubyte c) @safe pure nothrow @nogc
{
    return c == '_' || c >= 128 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z');
}

/// Decodes a non-interpolated quoted key using PHP's quote rules.
string phpLiteralKey(const PHPToken t) @safe pure
{
    if (t.kind != "string")
        throw new Exception("PHP configuration keys must be literal strings");
    char quote = t.text[0];
    auto buf = appender!string();
    for (size_t i = 1; i + 1 < t.text.length; i++)
    {
        char c = t.text[i];
        if (quote == '"' && c == '$')
            throw new Exception("interpolated PHP keys are unsupported");
        if (c == '\\' && i + 2 < t.text.length)
        {
            char next = t.text[i + 1];
            if (next == quote || next == '\\' || (quote == '"' && next == '$'))
            {
                buf.put(next);
                i++;
                continue;
            }
            if (quote == '"')
            {
                if (next == 'n') { buf.put('\n'); i++; continue; }
                if (next == 'r') { buf.put('\r'); i++; continue; }
                if (next == 't') { buf.put('\t'); i++; continue; }
                throw new Exception("unsupported PHP key escape");
            }
        }
        buf.put(c);
    }
    return buf.data;
}

/// Balances all bracket kinds starting at `open`, returning the index of the matching closer.
size_t phpMatching(const(PHPToken)[] tokens, size_t open) @safe pure
{
    string[] stack;
    for (size_t i = open; i < tokens.length; i++)
    {
        switch (tokens[i].text)
        {
            case "[", "(", "{":
                stack ~= tokens[i].text;
                break;
            case "]", ")", "}":
                if (stack.length == 0)
                    throw new Exception("unbalanced PHP brackets");
                string want;
                if (tokens[i].text == "]") want = "[";
                else if (tokens[i].text == ")") want = "(";
                else want = "{";
                if (stack[$ - 1] != want)
                    throw new Exception("unbalanced PHP brackets");
                stack.length -= 1;
                if (stack.length == 0)
                    return i;
                break;
            default:
                break;
        }
    }
    throw new Exception("unterminated PHP expression");
}

struct PHPSegmentSpan
{
    size_t first;
    size_t last;
}

/// Splits comma-delimited expressions without entering nested calls or arrays.
PHPSegmentSpan[] phpSegments(const(PHPToken)[] tokens, size_t start, size_t end) @safe pure
{
    PHPSegmentSpan[] segments;
    size_t begin = start;
    for (size_t i = start; i < end; i++)
    {
        switch (tokens[i].text)
        {
            case "[", "(", "{":
                auto closeIdx = phpMatching(tokens, i);
                if (closeIdx >= end)
                    throw new Exception("unbalanced PHP expression");
                i = closeIdx;
                break;
            case "]", ")", "}":
                throw new Exception("unbalanced PHP expression");
            case ",":
                if (i == begin)
                    throw new Exception("empty PHP expression");
                segments ~= PHPSegmentSpan(begin, i);
                begin = i + 1;
                break;
            default:
                break;
        }
    }
    if (begin < end)
        segments ~= PHPSegmentSpan(begin, end);
    return segments;
}

struct PHPArrayEntry
{
    string key;
    size_t first = 0;
    size_t last = 0;
}

struct PHPArray
{
    size_t open = 0;
    size_t close = 0;
    PHPArrayEntry[] entries;
}

/// Parses literal PHP array containers (`[...]` or `array(...)`).
/// Dynamic keys, duplicate keys, and spread/computed expressions are refused.
PHPArray parsePHPArray(const(PHPToken)[] tokens, size_t start, size_t end) @safe
{
    size_t open = start;
    if (open < end && icmp(tokens[open].text, "array") == 0)
        open++;
    if (open >= end || (tokens[open].text != "[" && tokens[open].text != "(")
        || (open == start && tokens[open].text != "["))
    {
        throw new Exception("configuration path is not a literal PHP array");
    }
    auto closeIdx = phpMatching(tokens, open);
    if (closeIdx + 1 != end)
        throw new Exception("computed PHP arrays cannot be patched safely");
    auto segments = phpSegments(tokens, open + 1, closeIdx);
    PHPArray arr;
    arr.open = open;
    arr.close = closeIdx;
    bool[string] seen;
    foreach (segment; segments)
    {
        auto first = segment.first;
        auto last = segment.last;
        if (last - first < 3 || tokens[first + 1].text != "=>")
            throw new Exception("configuration array requires literal string keys");
        auto key = phpLiteralKey(tokens[first]);
        if ((key in seen) !is null)
            throw new Exception(format("duplicate PHP array key %s", formatGoQuoted(key)));
        seen[key] = true;
        arr.entries ~= PHPArrayEntry(key, first + 2, last);
    }
    return arr;
}

/// Finds a single top-level literal `return [...];` statement, skipping returns
/// within functions and conditionals. Executable expressions are refused.
PHPArray phpReturnedArray(const(PHPToken)[] tokens) @safe
{
    bool hasFound = false;
    PHPArray found;
    for (size_t i = 0; i < tokens.length; i++)
    {
        auto txt = tokens[i].text;
        if (txt == "[" || txt == "(" || txt == "{")
        {
            i = phpMatching(tokens, i);
            continue;
        }
        if (icmp(txt, "return") != 0)
            continue;
        if (i > 0)
        {
            auto prev = tokens[i - 1].text;
            if (prev == "$" || prev == "->" || prev == "::")
                continue;
        }
        if (hasFound)
            throw new Exception("multiple top-level PHP returns are unsupported");

        size_t start = i + 1;
        size_t open = start;
        if (open < tokens.length && icmp(tokens[open].text, "array") == 0)
            open++;
        if (open >= tokens.length || (tokens[open].text != "[" && tokens[open].text != "("))
            throw new Exception("configuration must return a literal PHP array");
        auto closeIdx = phpMatching(tokens, open);
        if (closeIdx + 1 >= tokens.length || tokens[closeIdx + 1].text != ";")
            throw new Exception("computed PHP return expressions are unsupported");
        found = parsePHPArray(tokens, start, closeIdx + 1);
        hasFound = true;
        i = closeIdx + 1;
    }
    if (!hasFound)
        throw new Exception("configuration must return a literal PHP array");
    return found;
}

string phpEOL(string src) @safe pure nothrow
{
    return src.indexOf("\r\n") >= 0 ? "\r\n" : "\n";
}

size_t phpLineStart(string src, size_t pos) @safe pure
{
    if (pos == 0)
        return 0;
    auto idx = src[0 .. pos].lastIndexOf('\n');
    return idx < 0 ? 0 : cast(size_t)(idx + 1);
}

string phpIndent(string src, size_t pos) @safe pure
{
    auto line = src[phpLineStart(src, pos) .. pos];
    size_t i = 0;
    while (i < line.length && (line[i] == ' ' || line[i] == '\t'))
        i++;
    return line[0 .. i];
}

/// Renders newly inserted literal branches without rewriting existing containers.
string phpNestedValue(const(string)[] keys, string value, string indent, string eol) @safe pure
{
    if (keys.length == 0)
        return value;
    return "[" ~ eol ~ indent ~ "    " ~ phpSingleQuotedString(keys[0]) ~ " => "
        ~ phpNestedValue(keys[1 .. $], value, indent ~ "    ", eol) ~ "," ~ eol ~ indent ~ "]";
}

/// Changes one leaf in memory. Callers write only after all updates succeed.
string patchPHPArrayValue(string src, const(string)[] path, string value) @safe
{
    if (path.length == 0)
        throw new Exception("PHP update path cannot be empty");
    auto doc = scanPHP(src);
    auto tokens = doc.tokens;
    auto array = phpReturnedArray(tokens);
    foreach (depth, key; path)
    {
        PHPArrayEntry* entry = null;
        foreach (i; 0 .. array.entries.length)
        {
            if (array.entries[i].key == key)
            {
                entry = &array.entries[i];
                break;
            }
        }
        if (entry is null)
        {
            auto closePos = tokens[array.close].start;
            if (array.entries.length > 0 && tokens[array.close - 1].text != ",")
            {
                auto last = array.entries[$ - 1];
                auto endPos = tokens[last.last - 1].end;
                src = src[0 .. endPos] ~ "," ~ src[endPos .. $];
                closePos++;
            }
            auto eol = phpEOL(src);
            auto baseIndent = phpIndent(src, tokens[array.open].start);
            auto indent = baseIndent ~ "    ";
            if (array.entries.length > 0)
            {
                auto firstKey = tokens[array.entries[0].first - 2].start;
                auto candidate = phpIndent(src, firstKey);
                if (candidate.length > baseIndent.length)
                    indent = candidate;
            }
            auto assignment = indent ~ phpSingleQuotedString(key) ~ " => "
                ~ phpNestedValue(path[depth + 1 .. $], value, indent, eol) ~ "," ~ eol;
            auto lineStart = phpLineStart(src, closePos);
            if (src[lineStart .. closePos].strip().length == 0)
                return src[0 .. lineStart] ~ assignment ~ src[lineStart .. $];
            return src[0 .. closePos] ~ eol ~ assignment ~ baseIndent ~ src[closePos .. $];
        }
        if (depth == path.length - 1)
            return src[0 .. tokens[entry.first].start] ~ value ~ src[tokens[entry.last - 1].end .. $];
        try
        {
            array = parsePHPArray(tokens, entry.first, entry.last);
        }
        catch (Exception e)
        {
            throw new Exception(format("array path %s: %s", formatGoQuoted(key), e.msg));
        }
    }
    return src;
}

struct PHPEdit
{
    size_t start = 0;
    size_t end = 0;
    string replacement;
}

/// Applies nonoverlapping source spans from right to left.
string applyPHPEdits(string src, PHPEdit[] edits) @safe
{
    edits.sort!((a, b) {
        if (a.start != b.start)
            return a.start > b.start;
        return a.end > b.end;
    });
    auto previous = src.length;
    foreach (edit; edits)
    {
        if (edit.end < edit.start || edit.end > previous)
            throw new Exception("overlapping PHP edits");
        src = src[0 .. edit.start] ~ edit.replacement ~ src[edit.end .. $];
        previous = edit.start;
    }
    return src;
}

/// Replaces only the second argument of literal `define(...)` calls, preserving
/// comments, spacing, quote styles on keys, and unrelated defines.
string patchPHPConstants(string src, const(string[string]) values, const(string)[] order, string anchor) @safe
{
    auto doc = scanPHP(src);
    auto tokens = doc.tokens;
    PHPEdit[] edits;
    bool[string] written;
    ptrdiff_t insertAt = -1;
    size_t fallback = src.length;
    int depth = 0;

    for (size_t i = 0; i < tokens.length; i++)
    {
        auto t = tokens[i];
        if (t.text == "{")
            depth++;
        if (t.text == "}")
            depth--;
        auto lowerText = t.text.toLower();
        if (depth == 0 && (t.kind == "close"
            || lowerText == "require" || lowerText == "require_once"
            || lowerText == "include" || lowerText == "include_once"))
        {
            if (t.start < fallback)
                fallback = t.start;
        }
        if (icmp(t.text, "define") != 0 || i + 1 >= tokens.length || tokens[i + 1].text != "(")
            continue;
        if (i > 0)
        {
            auto prev = tokens[i - 1].text;
            if (prev == "$" || prev == "->" || prev == "::" || prev == "function")
                continue;
        }
        if (i > 1 && tokens[i - 1].text == "\\" && tokens[i - 2].kind == "identifier")
            continue;

        auto closeIdx = phpMatching(tokens, i + 1);
        PHPSegmentSpan[] args;
        try
        {
            args = phpSegments(tokens, i + 2, closeIdx);
        }
        catch (Exception)
        {
            throw new Exception("unsupported PHP define arguments");
        }
        if (args.length < 2 || args.length > 3 || args[0].last - args[0].first != 1)
            throw new Exception("unsupported PHP define arguments");

        auto key = phpLiteralKey(tokens[args[0].first]);
        if (key == anchor && insertAt < 0 && depth == 0)
            insertAt = cast(ptrdiff_t) t.start;
        if (auto pVal = key in values)
        {
            if ((key in written) !is null)
                throw new Exception(format("duplicate PHP constant %s", formatGoQuoted(key)));
            written[key] = true;
            edits ~= PHPEdit(tokens[args[1].first].start, tokens[args[1].last - 1].end, *pVal);
        }
        i = closeIdx;
    }

    size_t insertPos = insertAt < 0 ? fallback : cast(size_t) insertAt;
    auto eol = phpEOL(src);
    string[] missing;
    foreach (key; order)
    {
        if ((key in written) is null)
            missing ~= "define( " ~ phpSingleQuotedString(key) ~ ", " ~ values[key] ~ " );";
    }
    if (missing.length > 0)
    {
        auto lineStart = phpLineStart(src, insertPos);
        if (src[lineStart .. insertPos].strip().length == 0)
            insertPos = lineStart;
        auto block = missing.join(eol) ~ eol;
        if (insertPos > 0 && src[insertPos - 1] != '\n')
            block = eol ~ block;
        edits ~= PHPEdit(insertPos, insertPos, block);
    }
    return applyPHPEdits(src, edits);
}

/// Inserts or moves the managed include block after application settings so that
/// installer-written credentials cannot override it.
string patchPHPInclude(string src, string includeFile, string marker) @safe
{
    auto doc = scanPHP(src);
    auto startMarker = "// <polka:" ~ marker ~ ">";
    auto endMarker = "// </polka:" ~ marker ~ ">";
    ptrdiff_t start = -1;
    ptrdiff_t end = -1;
    foreach (comment; doc.comments)
    {
        if (comment.text == startMarker)
        {
            if (start >= 0)
                throw new Exception("duplicate PHP include marker");
            start = cast(ptrdiff_t) comment.start;
        }
        else if (comment.text == endMarker)
        {
            if (end >= 0)
                throw new Exception("duplicate PHP include marker");
            end = cast(ptrdiff_t) comment.end;
        }
    }
    if ((start >= 0) != (end >= 0) || (end >= 0 && end < start))
        throw new Exception("incomplete PHP include markers");

    auto eol = phpEOL(src);
    auto fileExpr = phpSingleQuotedString("/" ~ includeFile);
    auto block = [
        startMarker,
        "if (file_exists(__DIR__ . " ~ fileExpr ~ ")) {",
        "  include __DIR__ . " ~ fileExpr ~ ";",
        "}",
        endMarker,
    ].join(eol);

    size_t position = src.length;
    foreach (token; doc.tokens)
    {
        if (token.kind == "open")
            position = src.length;
        if (token.kind == "close")
            position = token.start;
    }

    if (start >= 0)
    {
        auto uStart = cast(size_t) start;
        auto uEnd = cast(size_t) end;
        if (src[uEnd .. position].strip().length == 0)
            return src[0 .. uStart] ~ block ~ src[uEnd .. $];
        if (src[uEnd .. $].startsWith(eol))
            uEnd += eol.length;
        src = src[0 .. uStart] ~ src[uEnd .. $];
        position -= (uEnd - uStart);
    }
    if (position > 0 && src[position - 1] != '\n')
        block = eol ~ block;
    return src[0 .. position] ~ block ~ eol ~ src[position .. $];
}

/// Converts a managed YAML scalar value into a safe PHP source literal.
/// `null`, `bool`, and finite numeric literals retain their types; strings are single-quoted.
string renderPHPScalar(const YamlValue value) @safe
{
    final switch (value.kind)
    {
        case YamlKind.null_:
            return "null";
        case YamlKind.string_:
            return phpSingleQuotedString(value.strVal);
        case YamlKind.bool_:
            return value.boolVal ? "true" : "false";
        case YamlKind.int_:
            return value.intVal.to!string;
        case YamlKind.float_:
            if (isNaN(value.floatVal) || isInfinity(value.floatVal))
                throw new Exception("non-finite PHP numeric value");
            return format("%g", value.floatVal);
        case YamlKind.sequence:
        case YamlKind.mapping:
            throw new Exception("unsupported PHP scalar type");
    }
}

unittest
{
    import std.algorithm : count;

    // TestPHPArrayPatchesPreserveSource
    struct ArrayCase { string name, source; string[] path; string want, preserved; }
    foreach (tc; [
        ArrayCase("short", "<?php\nreturn ['host' => env('OLD'), 'custom' => call(']')];\n", ["host"], "'host' => 'new'", "'custom' => call(']')"),
        ArrayCase("long", "<?php\r\nreturn array(\r\n  'nested' => array('host' => 'old'), // custom\r\n);\r\n", ["nested", "host"], "'host' => 'new'", "), // custom\r\n"),
        ArrayCase("missing", "<?php\nreturn [\n  'custom' => true // untouched\n];\n", ["Database", "primary", "host"], "'host' => 'new'", "'custom' => true, // untouched\n"),
        ArrayCase("inline missing", "<?php return ['custom' => true];", ["host"], "'host' => 'new'", "'custom' => true,"),
        ArrayCase("empty", "<?php return array();", ["nested", "host"], "'host' => 'new'", "return array("),
        ArrayCase("comments", "<?php\n// return ['host' => 'example'];\nreturn ['host' /* note */ => 'old' /* keep */];\n", ["host"], "=> 'new' /* keep */", "// return ['host' => 'example'];"),
        ArrayCase("escaped key", "<?php return ['a\\'b' => 'old', 'custom' => false];", ["a'b"], "'a\\'b' => 'new'", "'custom' => false"),
        ArrayCase("variable named return", "<?php $return = 'custom'; return ['host' => 'old'];", ["host"], "'host' => 'new'", "$return = 'custom';"),
    ])
    {
        auto updated = patchPHPArrayValue(tc.source, tc.path, "'new'");
        assert(updated.canFind(tc.want));
        assert(updated.canFind(tc.preserved));
        if (tc.source.canFind("\r\n"))
            assert(!updated.replace("\r\n", "").canFind("\n"));
        auto again = patchPHPArrayValue(updated, tc.path, "'new'");
        assert(again == updated);
    }

    // TestPHPConstantsPatchesPreserveSource
    {
        auto source = "<?php\r\n// define('HOST', 'example');\r\n$example = \"define('HOST', 'string')\";\r\n$define('HOST', 'dynamic');\r\n$obj->define('HOST', 'method');\r\nCustom\\define('HOST', 'namespace');\r\ndefine(\r\n  \"HOST\", /* preserve */ getenv('HOST')\r\n); // end\r\ndefine('BOOTSTRAP', true);\r\nrequire 'bootstrap.php';\r\n?>trailing html";
        auto values = ["HOST": "'new'", "PASSWORD": "'secret'"];
        auto updated = patchPHPConstants(source, values, ["HOST", "PASSWORD"], "BOOTSTRAP");
        foreach (want; [
            "// define('HOST', 'example');",
            `$example = "define('HOST', 'string')";`,
            "$define('HOST', 'dynamic');",
            "$obj->define('HOST', 'method');",
            `Custom\define('HOST', 'namespace');`,
            `"HOST", /* preserve */ 'new'`,
            "// end\r\n",
            "define( 'PASSWORD', 'secret' );\r\ndefine('BOOTSTRAP', true);",
            "?>trailing html",
        ])
        {
            assert(updated.canFind(want));
        }
        assert(patchPHPConstants(updated, values, ["HOST", "PASSWORD"], "BOOTSTRAP") == updated);

        foreach (src; ["<?php\nrequire 'bootstrap.php';\n", "<?php\n?>html", "<?php"])
        {
            auto upd = patchPHPConstants(src, values, ["HOST", "PASSWORD"], "ABSENT");
            auto reqIdx = upd.indexOf("require");
            if (reqIdx >= 0)
                assert(upd.indexOf("PASSWORD") < reqIdx);
            auto closeIdx = upd.indexOf("?>");
            if (closeIdx >= 0)
                assert(upd.indexOf("PASSWORD") < closeIdx);
        }
    }

    // TestPHPIncludeUpdatesManagedBlock
    {
        auto source = "<?php\r\n$custom = true;\r\n?>html";
        auto updated = patchPHPInclude(source, "managed.php", "settings");
        assert(updated.indexOf("// <polka:settings>") < updated.indexOf("?>"));
        assert(updated.canFind("$custom = true;\r\n"));
        auto again = patchPHPInclude(updated, "rotated.php", "settings");
        assert(again.count("// <polka:settings>") == 1 && !again.canFind("managed.php"));

        bool threw = false;
        try { patchPHPInclude("<?php\n// <polka:settings>\n", "managed.php", "settings"); } catch (Exception) { threw = true; }
        assert(threw);
    }

    // TestPHPIncludeFollowsAppendedSettings
    struct IncludeCase { string name, eol, suffix; }
    foreach (tc; [
        IncludeCase("LF", "\n", ""),
        IncludeCase("CRLF", "\r\n", ""),
        IncludeCase("closing tag", "\n", "?>trailing html"),
        IncludeCase("reopened PHP", "\n", "?>preserved html<?php\n$extra = true;\n"),
    ])
    {
        auto src = patchPHPInclude("<?php" ~ tc.eol, "old.php", "settings");
        auto assignment = "$databases['default']['default'] = ['password' => 'stale'];";
        src ~= assignment ~ tc.eol ~ "// Installer settings must survive." ~ tc.eol ~ tc.suffix;
        auto updated = patchPHPInclude(src, "managed.php", "settings");
        assert(updated.indexOf("// <polka:settings>") > updated.indexOf(assignment));
        assert(updated.canFind(assignment));
        assert(updated.canFind("// Installer settings must survive."));
        if (tc.suffix.length > 0)
            assert(updated.canFind(tc.suffix));
        assert(!updated.canFind("old.php") && updated.count("// <polka:settings>") == 1);
        if (tc.eol == "\r\n")
            assert(!updated.replace("\r\n", "").canFind("\n"));
        assert(patchPHPInclude(updated, "managed.php", "settings") == updated);
    }

    // renderPHPScalar checks
    assert(renderPHPScalar(YamlValue.nullValue()) == "null");
    assert(renderPHPScalar(YamlValue.fromBool(true)) == "true");
    assert(renderPHPScalar(YamlValue.fromBool(false)) == "false");
    assert(renderPHPScalar(YamlValue.fromInt(42)) == "42");
    assert(renderPHPScalar(YamlValue.fromFloat(4.5)) == "4.5");
    assert(renderPHPScalar(YamlValue.fromString(`a'\b`)) == `'a\'\\b'`);

    bool threwInf = false;
    try { renderPHPScalar(YamlValue.fromFloat(double.infinity)); } catch (Exception) { threwInf = true; }
    assert(threwInf);

    bool threwNaN = false;
    try { renderPHPScalar(YamlValue.fromFloat(double.nan)); } catch (Exception) { threwNaN = true; }
    assert(threwNaN);
}
