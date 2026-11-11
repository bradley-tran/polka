module polka.support.yaml;

import core.stdc.stdint;
import core.stdc.string : strlen;
import std.algorithm : any, canFind, startsWith;
import std.array : appender;
import std.conv : to;
import std.exception : enforce;
import std.format : format;
import std.math : isInfinity, isNaN;
import std.string : indexOf, lastIndexOf, splitLines, strip, stripLeft, stripRight, toLower;

// ============================================================================
// libyaml C bindings (from /usr/include/yaml.h)
// ============================================================================
private extern (C) nothrow @nogc
{
    alias yaml_char_t = ubyte;

    enum yaml_encoding_t : int
    {
        YAML_ANY_ENCODING = 0,
        YAML_UTF8_ENCODING = 1,
        YAML_UTF16LE_ENCODING = 2,
        YAML_UTF16BE_ENCODING = 3,
    }

    enum yaml_scalar_style_t : int
    {
        YAML_ANY_SCALAR_STYLE = 0,
        YAML_PLAIN_SCALAR_STYLE = 1,
        YAML_SINGLE_QUOTED_SCALAR_STYLE = 2,
        YAML_DOUBLE_QUOTED_SCALAR_STYLE = 3,
        YAML_LITERAL_SCALAR_STYLE = 4,
        YAML_FOLDED_SCALAR_STYLE = 5,
    }

    enum yaml_sequence_style_t : int
    {
        YAML_ANY_SEQUENCE_STYLE = 0,
        YAML_BLOCK_SEQUENCE_STYLE = 1,
        YAML_FLOW_SEQUENCE_STYLE = 2,
    }

    enum yaml_mapping_style_t : int
    {
        YAML_ANY_MAPPING_STYLE = 0,
        YAML_BLOCK_MAPPING_STYLE = 1,
        YAML_FLOW_MAPPING_STYLE = 2,
    }

    enum yaml_event_type_t : int
    {
        YAML_NO_EVENT = 0,
        YAML_STREAM_START_EVENT = 1,
        YAML_STREAM_END_EVENT = 2,
        YAML_DOCUMENT_START_EVENT = 3,
        YAML_DOCUMENT_END_EVENT = 4,
        YAML_ALIAS_EVENT = 5,
        YAML_SCALAR_EVENT = 6,
        YAML_SEQUENCE_START_EVENT = 7,
        YAML_SEQUENCE_END_EVENT = 8,
        YAML_MAPPING_START_EVENT = 9,
        YAML_MAPPING_END_EVENT = 10,
    }

    struct yaml_mark_t
    {
        size_t index;
        size_t line;
        size_t column;
    }

    struct yaml_version_directive_t
    {
        int major;
        int minor;
    }

    struct yaml_tag_directive_t
    {
        yaml_char_t* handle;
        yaml_char_t* prefix;
    }

    struct yaml_event_t
    {
        yaml_event_type_t type;
        union Data
        {
            struct StreamStart
            {
                yaml_encoding_t encoding;
            }
            StreamStart stream_start;

            struct DocumentStart
            {
                yaml_version_directive_t* version_directive;
                struct TagDirectives
                {
                    yaml_tag_directive_t* start;
                    yaml_tag_directive_t* end;
                }
                TagDirectives tag_directives;
                int implicit;
            }
            DocumentStart document_start;

            struct DocumentEnd
            {
                int implicit;
            }
            DocumentEnd document_end;

            struct Alias
            {
                yaml_char_t* anchor;
            }
            Alias alias_;

            struct Scalar
            {
                yaml_char_t* anchor;
                yaml_char_t* tag;
                yaml_char_t* value;
                size_t length;
                int plain_implicit;
                int quoted_implicit;
                yaml_scalar_style_t style;
            }
            Scalar scalar;

            struct SequenceStart
            {
                yaml_char_t* anchor;
                yaml_char_t* tag;
                int implicit;
                yaml_sequence_style_t style;
            }
            SequenceStart sequence_start;

            struct MappingStart
            {
                yaml_char_t* anchor;
                yaml_char_t* tag;
                int implicit;
                yaml_mapping_style_t style;
            }
            MappingStart mapping_start;
        }
        Data data;
        yaml_mark_t start_mark;
        yaml_mark_t end_mark;
    }

    struct yaml_parser_t
    {
        int error;
        const(char)* problem;
        size_t problem_offset;
        int problem_value;
        yaml_mark_t problem_mark;
        const(char)* context;
        yaml_mark_t context_mark;
        ubyte[1024] _opaque_rest;
    }

    int yaml_parser_initialize(yaml_parser_t* parser);
    void yaml_parser_delete(yaml_parser_t* parser);
    void yaml_parser_set_input_string(yaml_parser_t* parser, const(yaml_char_t)* input, size_t size);
    int yaml_parser_parse(yaml_parser_t* parser, yaml_event_t* event);
    void yaml_event_delete(yaml_event_t* event);
}

// ============================================================================
// Public YAML DOM & Comment API
// ============================================================================

class YamlException : Exception
{
    this(string msg, string file = __FILE__, size_t line = __LINE__) @safe pure nothrow
    {
        super(msg, file, line);
    }
}

enum YamlKind
{
    null_,
    bool_,
    int_,
    float_,
    string_,
    sequence,
    mapping,
}

struct YamlMapEntry
{
    string key;
    YamlValue value;
    size_t keyLine;
}

struct CommentEntry
{
    string[] head;
    string inline;
}

alias CommentMap = CommentEntry[string];

struct YamlValue
{
    YamlKind kind = YamlKind.null_;
    bool boolVal = false;
    long intVal = 0;
    double floatVal = 0.0;
    string strVal;
    string rawScalar;
    bool quoted = false;
    bool rawPlain = false;
    YamlValue[] seq;
    YamlMapEntry[] map;
    size_t startLine = 0;
    size_t endLine = 0;

    static YamlValue nullValue() @safe pure nothrow
    {
        return YamlValue(YamlKind.null_);
    }

    static YamlValue fromBool(bool b) @safe pure nothrow
    {
        YamlValue v;
        v.kind = YamlKind.bool_;
        v.boolVal = b;
        v.rawScalar = b ? "true" : "false";
        return v;
    }

    static YamlValue fromInt(long i) @safe pure
    {
        YamlValue v;
        v.kind = YamlKind.int_;
        v.intVal = i;
        v.rawScalar = i.to!string;
        return v;
    }

    static YamlValue fromFloat(double f) @safe pure
    {
        YamlValue v;
        v.kind = YamlKind.float_;
        v.floatVal = f;
        v.rawScalar = format("%g", f);
        return v;
    }

    static YamlValue fromString(string s) @safe pure nothrow
    {
        YamlValue v;
        v.kind = YamlKind.string_;
        v.strVal = s;
        v.rawScalar = s;
        return v;
    }

    static YamlValue fromRawPlain(string s) @safe pure nothrow
    {
        YamlValue v;
        v.kind = YamlKind.string_;
        v.strVal = s;
        v.rawScalar = s;
        v.rawPlain = true;
        return v;
    }

    static YamlValue fromSequence(YamlValue[] items = null) @safe pure nothrow
    {
        YamlValue v;
        v.kind = YamlKind.sequence;
        v.seq = items;
        return v;
    }

    static YamlValue fromMapping(YamlMapEntry[] entries = null) @safe pure nothrow
    {
        YamlValue v;
        v.kind = YamlKind.mapping;
        v.map = entries;
        return v;
    }

    bool isNull() const @safe pure nothrow @nogc
    {
        return kind == YamlKind.null_;
    }

    bool isScalar() const @safe pure nothrow @nogc
    {
        return kind != YamlKind.sequence && kind != YamlKind.mapping;
    }

    bool isSequence() const @safe pure nothrow @nogc
    {
        return kind == YamlKind.sequence;
    }

    bool isMapping() const @safe pure nothrow @nogc
    {
        return kind == YamlKind.mapping;
    }

    bool hasKey(string key) const @safe pure nothrow @nogc
    {
        if (kind != YamlKind.mapping)
            return false;
        foreach (ref entry; map)
        {
            if (entry.key == key)
                return true;
        }
        return false;
    }

    const(YamlValue)* ptr(string key) const @trusted pure nothrow @nogc
    {
        if (kind != YamlKind.mapping)
            return null;
        foreach (ref entry; map)
        {
            if (entry.key == key)
                return &entry.value;
        }
        return null;
    }

    YamlValue clone() const @safe pure nothrow
    {
        YamlValue copy;
        copy.kind = kind;
        copy.boolVal = boolVal;
        copy.intVal = intVal;
        copy.floatVal = floatVal;
        copy.strVal = strVal;
        copy.rawScalar = rawScalar;
        copy.quoted = quoted;
        copy.rawPlain = rawPlain;
        copy.startLine = startLine;
        copy.endLine = endLine;
        if (seq.length > 0)
        {
            copy.seq.length = seq.length;
            foreach (i, ref item; seq)
                copy.seq[i] = item.clone();
        }
        if (map.length > 0)
        {
            copy.map.length = map.length;
            foreach (i, ref entry; map)
                copy.map[i] = YamlMapEntry(entry.key, entry.value.clone(), entry.keyLine);
        }
        return copy;
    }

    YamlValue get(string key, YamlValue fallback = YamlValue.nullValue()) const @safe pure nothrow
    {
        if (kind != YamlKind.mapping)
            return fallback;
        foreach (ref entry; map)
        {
            if (entry.key == key)
                return entry.value.clone();
        }
        return fallback;
    }

    void set(string key, YamlValue val) @safe pure nothrow
    {
        if (kind != YamlKind.mapping)
            kind = YamlKind.mapping;
        foreach (ref entry; map)
        {
            if (entry.key == key)
            {
                entry.value = val;
                return;
            }
        }
        map ~= YamlMapEntry(key, val, 0);
    }

    void removeKey(string key) @safe pure nothrow
    {
        if (kind != YamlKind.mapping)
            return;
        YamlMapEntry[] next;
        foreach (entry; map)
        {
            if (entry.key != key)
                next ~= entry;
        }
        map = next;
    }

    string asString() const @safe pure
    {
        final switch (kind)
        {
            case YamlKind.null_:
                return "";
            case YamlKind.bool_:
                return boolVal ? "true" : "false";
            case YamlKind.int_:
                return intVal.to!string;
            case YamlKind.float_:
                return rawScalar.length > 0 ? rawScalar : format("%g", floatVal);
            case YamlKind.string_:
                return strVal;
            case YamlKind.sequence:
            case YamlKind.mapping:
                throw new YamlException("expected scalar YAML value");
        }
    }

    string asScalarText() const @safe pure
    {
        if (rawScalar.length > 0)
            return rawScalar;
        return asString();
    }

    bool asBool() const @safe pure
    {
        if (kind == YamlKind.bool_)
            return boolVal;
        throw new YamlException("expected boolean YAML value");
    }

    long asInt() const @safe pure
    {
        if (kind == YamlKind.int_)
            return intVal;
        throw new YamlException("expected integer YAML value");
    }

    string[] keys() const @safe pure nothrow
    {
        if (kind != YamlKind.mapping)
            return null;
        string[] outKeys;
        outKeys.reserve(map.length);
        foreach (ref entry; map)
            outKeys ~= entry.key;
        return outKeys;
    }
}

/// Parses a YAML string into a YamlValue DOM tree.
YamlValue parseYAML(string input) @trusted
{
    CommentMap unused;
    return parseYAMLImpl(input, false, unused);
}

/// Parses a YAML string and extracts path-indexed comments into `comments`.
YamlValue parseYAMLWithComments(string input, out CommentMap comments) @trusted
{
    return parseYAMLImpl(input, true, comments);
}

private YamlValue parseYAMLImpl(string input, bool extractComments, out CommentMap comments) @trusted
{
    yaml_parser_t parser;
    if (!yaml_parser_initialize(&parser))
        throw new YamlException("failed to initialize libyaml parser");
    scope (exit) yaml_parser_delete(&parser);

    yaml_parser_set_input_string(&parser, cast(const(yaml_char_t)*) input.ptr, input.length);

    YamlValue[string] anchors;
    bool inDoc = false;
    bool done = false;
    YamlValue root = YamlValue.nullValue();
    bool hasRoot = false;

    YamlValue nextEventNode(ref yaml_event_t ev, ref bool consumed)
    {
        switch (ev.type)
        {
            case yaml_event_type_t.YAML_SCALAR_EVENT:
            {
                auto val = scalarFromEvent(ev);
                auto anchor = cstr(ev.data.scalar.anchor);
                if (anchor.length > 0)
                    anchors[anchor] = val;
                consumed = true;
                return val;
            }
            case yaml_event_type_t.YAML_ALIAS_EVENT:
            {
                auto anchor = cstr(ev.data.alias_.anchor);
                if (auto p = anchor in anchors)
                {
                    consumed = true;
                    return *p;
                }
                throw new YamlException("unknown YAML alias: *" ~ anchor);
            }
            case yaml_event_type_t.YAML_SEQUENCE_START_EVENT:
            {
                auto anchor = cstr(ev.data.sequence_start.anchor);
                auto startL = ev.start_mark.line;
                YamlValue[] items;
                while (true)
                {
                    yaml_event_t childEv;
                    parseNext(&parser, &childEv);
                    scope (exit) yaml_event_delete(&childEv);
                    if (childEv.type == yaml_event_type_t.YAML_SEQUENCE_END_EVENT)
                    {
                        auto seqVal = YamlValue.fromSequence(items);
                        seqVal.startLine = startL;
                        seqVal.endLine = childEv.end_mark.line;
                        if (anchor.length > 0)
                            anchors[anchor] = seqVal;
                        consumed = true;
                        return seqVal;
                    }
                    bool childConsumed = false;
                    items ~= nextEventNode(childEv, childConsumed);
                }
            }
            case yaml_event_type_t.YAML_MAPPING_START_EVENT:
            {
                auto anchor = cstr(ev.data.mapping_start.anchor);
                auto startL = ev.start_mark.line;
                YamlMapEntry[] entries;
                while (true)
                {
                    yaml_event_t keyEv;
                    parseNext(&parser, &keyEv);
                    bool keyDeleted = false;
                    scope (exit) if (!keyDeleted) yaml_event_delete(&keyEv);
                    if (keyEv.type == yaml_event_type_t.YAML_MAPPING_END_EVENT)
                    {
                        auto mapVal = YamlValue.fromMapping(entries);
                        mapVal.startLine = startL;
                        mapVal.endLine = keyEv.end_mark.line;
                        if (anchor.length > 0)
                            anchors[anchor] = mapVal;
                        consumed = true;
                        return mapVal;
                    }
                    auto keyLine = keyEv.start_mark.line;
                    bool kc = false;
                    auto keyNode = nextEventNode(keyEv, kc);
                    yaml_event_delete(&keyEv);
                    keyDeleted = true;
                    if (!keyNode.isScalar())
                        throw new YamlException("YAML mapping key must be a scalar");
                    string keyStr = keyNode.isNull() ? "" : keyNode.asString();

                    yaml_event_t valEv;
                    parseNext(&parser, &valEv);
                    scope (exit) yaml_event_delete(&valEv);
                    bool vc = false;
                    auto valNode = nextEventNode(valEv, vc);
                    entries ~= YamlMapEntry(keyStr, valNode, keyLine);
                }
            }
            default:
                throw new YamlException("unexpected YAML event type " ~ (cast(int) ev.type).to!string);
        }
    }

    while (!done)
    {
        yaml_event_t ev;
        parseNext(&parser, &ev);
        scope (exit) yaml_event_delete(&ev);

        switch (ev.type)
        {
            case yaml_event_type_t.YAML_STREAM_START_EVENT:
                break;
            case yaml_event_type_t.YAML_STREAM_END_EVENT:
                done = true;
                break;
            case yaml_event_type_t.YAML_DOCUMENT_START_EVENT:
                if (hasRoot)
                    throw new YamlException("multiple YAML documents are not supported");
                inDoc = true;
                break;
            case yaml_event_type_t.YAML_DOCUMENT_END_EVENT:
                inDoc = false;
                break;
            default:
                if (!inDoc)
                    throw new YamlException("unexpected YAML token outside document");
                bool c = false;
                root = nextEventNode(ev, c);
                hasRoot = true;
                break;
        }
    }

    if (extractComments && input.length > 0)
        comments = collectComments(input, root);

    return root;
}

private void parseNext(yaml_parser_t* parser, yaml_event_t* ev) @trusted
{
    if (!yaml_parser_parse(parser, ev))
    {
        string prob = parser.problem !is null ? to!string(parser.problem) : "syntax error";
        throw new YamlException(format("yaml parse error at line %d, column %d: %s",
            parser.problem_mark.line + 1, parser.problem_mark.column + 1, prob));
    }
}

private string cstr(const(yaml_char_t)* ptr) @trusted
{
    if (ptr is null)
        return null;
    auto s = cast(const(char)*) ptr;
    return s[0 .. strlen(s)].idup;
}

private YamlValue scalarFromEvent(ref yaml_event_t ev) @trusted
{
    auto ptr = cast(const(char)*) ev.data.scalar.value;
    string text = ptr !is null ? ptr[0 .. ev.data.scalar.length].idup : "";
    auto tag = cstr(ev.data.scalar.tag);
    auto style = ev.data.scalar.style;

    YamlValue v;
    v.rawScalar = text;
    v.startLine = ev.start_mark.line;
    v.endLine = ev.end_mark.line;
    v.quoted = (style != yaml_scalar_style_t.YAML_PLAIN_SCALAR_STYLE);

    if (tag == "tag:yaml.org,2002:str" || style != yaml_scalar_style_t.YAML_PLAIN_SCALAR_STYLE)
    {
        v.kind = YamlKind.string_;
        v.strVal = text;
        return v;
    }

    // Plain scalar type resolution (YAML 1.1 / 1.2 compatible with goccy/go-yaml)
    if (text.length == 0 || text == "~" || text == "null" || text == "Null" || text == "NULL")
    {
        v.kind = YamlKind.null_;
        return v;
    }
    if (text == "true" || text == "True" || text == "TRUE")
    {
        v.kind = YamlKind.bool_;
        v.boolVal = true;
        return v;
    }
    if (text == "false" || text == "False" || text == "FALSE")
    {
        v.kind = YamlKind.bool_;
        v.boolVal = false;
        return v;
    }
    long parsedInt;
    if (tryParseYamlInt(text, parsedInt))
    {
        v.kind = YamlKind.int_;
        v.intVal = parsedInt;
        return v;
    }
    double parsedFloat;
    if (tryParseYamlFloat(text, parsedFloat))
    {
        v.kind = YamlKind.float_;
        v.floatVal = parsedFloat;
        return v;
    }

    v.kind = YamlKind.string_;
    v.strVal = text;
    return v;
}

private bool tryParseYamlInt(string s, out long val) @safe pure
{
    if (s.length == 0)
        return false;
    string body = s;
    bool neg = false;
    if (body[0] == '+' || body[0] == '-')
    {
        neg = (body[0] == '-');
        body = body[1 .. $];
    }
    if (body.length == 0)
        return false;

    if (body.startsWith("0x") || body.startsWith("0X"))
    {
        auto hex = body[2 .. $];
        if (hex.length == 0)
            return false;
        foreach (c; hex)
        {
            if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')))
                return false;
        }
        try
        {
            val = to!long(hex, 16);
            if (neg) val = -val;
            return true;
        }
        catch (Exception)
            return false;
    }
    if (body.startsWith("0o") || body.startsWith("0O"))
    {
        auto oct = body[2 .. $];
        if (oct.length == 0)
            return false;
        foreach (c; oct)
        {
            if (c < '0' || c > '7')
                return false;
        }
        try
        {
            val = to!long(oct, 8);
            if (neg) val = -val;
            return true;
        }
        catch (Exception)
            return false;
    }

    foreach (c; body)
    {
        if (c < '0' || c > '9')
            return false;
    }
    // Note: YAML octal `0123` or decimal: in YAML 1.2 / goccy, leading zeros parse as decimal unless 0o.
    try
    {
        val = to!long(s);
        return true;
    }
    catch (Exception)
        return false;
}

private bool tryParseYamlFloat(string s, out double val) @safe pure
{
    if (s.length == 0)
        return false;
    if (s == ".inf" || s == ".Inf" || s == ".INF" || s == "+.inf" || s == "+.Inf" || s == "+.INF")
    {
        val = double.infinity;
        return true;
    }
    if (s == "-.inf" || s == "-.Inf" || s == "-.INF")
    {
        val = -double.infinity;
        return true;
    }
    if (s == ".nan" || s == ".NaN" || s == ".NAN")
    {
        val = double.nan;
        return true;
    }

    string body = s;
    if (body[0] == '+' || body[0] == '-')
        body = body[1 .. $];
    if (body.length == 0)
        return false;

    bool hasDot = false;
    bool hasExp = false;
    bool hasDigit = false;
    size_t i = 0;
    while (i < body.length)
    {
        char c = body[i];
        if (c >= '0' && c <= '9')
        {
            hasDigit = true;
            i++;
        }
        else if (c == '.' && !hasDot && !hasExp)
        {
            hasDot = true;
            i++;
        }
        else if ((c == 'e' || c == 'E') && !hasExp && hasDigit)
        {
            hasExp = true;
            i++;
            if (i < body.length && (body[i] == '+' || body[i] == '-'))
                i++;
            if (i >= body.length || body[i] < '0' || body[i] > '9')
                return false;
        }
        else
        {
            return false;
        }
    }
    if (!hasDigit || (!hasDot && !hasExp))
        return false;
    // Reject trailing dot without exponent or leading dot without digits if weird, though YAML allows `1.` and `.5`
    try
    {
        val = to!double(s);
        return true;
    }
    catch (Exception)
        return false;
}

// ============================================================================
// Comment Extraction & Path-based Preservation
// ============================================================================

private CommentMap collectComments(string input, ref const YamlValue root) @safe
{
    CommentMap result;
    auto lines = input.splitLines();
    if (lines.length == 0)
        return result;

    bool[] claimed = new bool[lines.length];

    void scanNode(ref const YamlValue node, string path, size_t nodeKeyLine, bool hasKeyLine)
    {
        if (hasKeyLine && nodeKeyLine < lines.length)
        {
            // Head comments immediately above keyLine (including contiguous comment block)
            string[] heads;
            long idx = cast(long) nodeKeyLine - 1;
            while (idx >= 0)
            {
                if (claimed[idx])
                    break;
                auto trimmed = lines[idx].strip();
                if (trimmed.startsWith("#"))
                {
                    heads = [trimmed] ~ heads;
                    claimed[idx] = true;
                    idx--;
                }
                else
                {
                    break;
                }
            }

            // Inline comment on the value's line (for scalars) or keyLine (for maps/seqs)
            string inlineCmt;
            size_t inlineLine = node.isScalar() ? node.startLine : nodeKeyLine;
            if (inlineLine < lines.length)
                inlineCmt = extractInlineComment(lines[inlineLine]);

            if (heads.length > 0 || inlineCmt.length > 0)
                result[path] = CommentEntry(heads, inlineCmt);
        }

        if (node.kind == YamlKind.mapping)
        {
            foreach (ref entry; node.map)
            {
                string childPath = path ~ "." ~ entry.key;
                scanNode(entry.value, childPath, entry.keyLine, true);
            }
        }
        else if (node.kind == YamlKind.sequence)
        {
            foreach (idx, ref item; node.seq)
            {
                string childPath = format("%s[%d]", path, idx);
                scanNode(item, childPath, item.startLine, false);
            }
        }
    }

    scanNode(root, "$", 0, false);
    return result;
}

private string extractInlineComment(string line) @safe pure
{
    bool inSingle = false;
    bool inDouble = false;
    bool escaped = false;
    for (size_t i = 0; i < line.length; i++)
    {
        char c = line[i];
        if (inDouble)
        {
            if (escaped)
                escaped = false;
            else if (c == '\\')
                escaped = true;
            else if (c == '"')
                inDouble = false;
        }
        else if (inSingle)
        {
            if (c == '\'')
            {
                if (i + 1 < line.length && line[i + 1] == '\'')
                    i++;
                else
                    inSingle = false;
            }
        }
        else
        {
            if (c == '\'')
                inSingle = true;
            else if (c == '"')
                inDouble = true;
            else if (c == '#' && (i == 0 || line[i - 1] == ' ' || line[i - 1] == '\t'))
                return line[i .. $].stripRight();
        }
    }
    return null;
}

// ============================================================================
// YAML Emitter (2-space indent + CommentMap support)
// ============================================================================

/// Emits a YamlValue tree to YAML text, optionally re-attaching comments from `comments`.
string emitYAML(ref const YamlValue root, const CommentMap comments = null) @safe
{
    if (root.isNull() && root.kind == YamlKind.null_)
        return "";

    struct Emitter
    {
        typeof(appender!string()) buf;
        const(CommentMap) comments;

        void emitIndent(size_t depth) @safe
        {
            foreach (_; 0 .. depth * 2)
                buf.put(' ');
        }

        void emitScalar(ref const YamlValue val) @safe
        {
            final switch (val.kind)
            {
                case YamlKind.null_:
                    buf.put("null");
                    break;
                case YamlKind.bool_:
                    buf.put(val.boolVal ? "true" : "false");
                    break;
                case YamlKind.int_:
                    buf.put(val.intVal.to!string);
                    break;
                case YamlKind.float_:
                    if (isNaN(val.floatVal))
                        buf.put(".nan");
                    else if (isInfinity(val.floatVal))
                        buf.put(val.floatVal < 0 ? "-.inf" : ".inf");
                    else if (val.rawScalar.length > 0)
                        buf.put(val.rawScalar);
                    else
                        buf.put(format("%g", val.floatVal));
                    break;
                case YamlKind.string_:
                    if (val.rawPlain && val.rawScalar.length > 0)
                        buf.put(val.rawScalar);
                    else
                        buf.put(formatYamlString(val.strVal));
                    break;
                case YamlKind.sequence:
                case YamlKind.mapping:
                    break;
            }
        }

        void emitNode(ref const YamlValue node, string path, size_t depth, bool inlineAfterKey) @safe
        {
            const(CommentEntry)* cmt = comments !is null ? (path in comments) : null;

            if (node.isScalar())
            {
                if (!inlineAfterKey)
                    emitIndent(depth);
                emitScalar(node);
                if (cmt !is null && cmt.inline.length > 0)
                {
                    buf.put(' ');
                    buf.put(cmt.inline);
                }
                buf.put('\n');
                return;
            }

            if (node.kind == YamlKind.sequence)
            {
                if (node.seq.length == 0)
                {
                    if (!inlineAfterKey)
                        emitIndent(depth);
                    buf.put("[]");
                    if (cmt !is null && cmt.inline.length > 0)
                    {
                        buf.put(' ');
                        buf.put(cmt.inline);
                    }
                    buf.put('\n');
                    return;
                }
                if (inlineAfterKey)
                {
                    if (cmt !is null && cmt.inline.length > 0)
                    {
                        buf.put(' ');
                        buf.put(cmt.inline);
                    }
                    buf.put('\n');
                }
                foreach (idx, ref item; node.seq)
                {
                    emitIndent(depth);
                    buf.put("- ");
                    if (item.isScalar())
                    {
                        emitScalar(item);
                        buf.put('\n');
                    }
                    else if (item.kind == YamlKind.mapping && item.map.length > 0)
                    {
                        string itemPath = format("%s[%d]", path, idx);
                        foreach (mIdx, ref entry; item.map)
                        {
                            if (mIdx > 0)
                                emitIndent(depth + 1);
                            emitMappingEntry(entry, itemPath ~ "." ~ entry.key, depth + 1, mIdx == 0);
                        }
                    }
                    else if (item.kind == YamlKind.mapping)
                    {
                        buf.put("{}\n");
                    }
                    else
                    {
                        buf.put('\n');
                        emitNode(item, format("%s[%d]", path, idx), depth + 1, false);
                    }
                }
                return;
            }

            if (node.kind == YamlKind.mapping)
            {
                if (node.map.length == 0)
                {
                    if (!inlineAfterKey)
                        emitIndent(depth);
                    buf.put("{}");
                    if (cmt !is null && cmt.inline.length > 0)
                    {
                        buf.put(' ');
                        buf.put(cmt.inline);
                    }
                    buf.put('\n');
                    return;
                }
                if (inlineAfterKey)
                {
                    if (cmt !is null && cmt.inline.length > 0)
                    {
                        buf.put(' ');
                        buf.put(cmt.inline);
                    }
                    buf.put('\n');
                }
                foreach (ref entry; node.map)
                {
                    string childPath = path ~ "." ~ entry.key;
                    emitMappingEntry(entry, childPath, depth, false);
                }
            }
        }

        void emitMappingEntry(ref const YamlMapEntry entry, string childPath, size_t depth, bool skipIndent) @safe
        {
            const(CommentEntry)* childCmt = comments !is null ? (childPath in comments) : null;
            if (childCmt !is null && childCmt.head.length > 0)
            {
                foreach (h; childCmt.head)
                {
                    emitIndent(depth);
                    buf.put(h);
                    buf.put('\n');
                }
            }
            if (!skipIndent)
                emitIndent(depth);
            buf.put(formatYamlKey(entry.key));
            buf.put(':');
            if (entry.value.isScalar() || (entry.value.isSequence() && entry.value.seq.length == 0)
                || (entry.value.isMapping() && entry.value.map.length == 0))
            {
                buf.put(' ');
            }
            emitNode(entry.value, childPath, depth + 1, true);
        }
    }

    auto e = Emitter(appender!string(), comments);
    e.emitNode(root, "$", 0, false);
    return e.buf.data;
}

private string formatYamlKey(string key) @safe pure
{
    if (key.length == 0)
        return `""`;
    if (needsYamlQuoting(key))
        return quoteYamlDouble(key);
    return key;
}

private string formatYamlString(string s) @safe pure
{
    if (s.length == 0)
        return `""`;
    if (needsYamlQuoting(s))
        return quoteYamlDouble(s);
    return s;
}

private bool needsYamlQuoting(string s) @safe pure
{
    if (s.length == 0)
        return true;
    if (s[0] == ' ' || s[0] == '\t' || s[$ - 1] == ' ' || s[$ - 1] == '\t')
        return true;

    // Quote if it would parse back as null, bool, int, or float
    auto low = s.toLower();
    if (low == "null" || low == "~" || low == "true" || low == "false"
        || low == "yes" || low == "no" || low == "on" || low == "off"
        || low == ".inf" || low == "+.inf" || low == "-.inf" || low == ".nan")
        return true;

    long tmpInt;
    if (tryParseYamlInt(s, tmpInt))
        return true;
    double tmpFloat;
    if (tryParseYamlFloat(s, tmpFloat))
        return true;

    // Leading indicators
    char first = s[0];
    if (first == '-' || first == '?' || first == ':' || first == ',' || first == '[' || first == ']'
        || first == '{' || first == '}' || first == '#' || first == '&' || first == '*'
        || first == '!' || first == '|' || first == '>' || first == '\'' || first == '"'
        || first == '%' || first == '@' || first == '`')
        return true;

    foreach (i, char c; s)
    {
        if (c < 0x20 || c == 0x7f || c == '"' || c == '\\')
            return true;
        if (c == ':' && (i + 1 == s.length || s[i + 1] == ' ' || s[i + 1] == '\t'))
            return true;
        if (c == '#' && i > 0 && (s[i - 1] == ' ' || s[i - 1] == '\t'))
            return true;
    }
    return false;
}

private string quoteYamlDouble(string s) @safe pure
{
    auto buf = appender!string();
    buf.put('"');
    foreach (char c; s)
    {
        switch (c)
        {
            case '"':  buf.put(`\"`); break;
            case '\\': buf.put(`\\`); break;
            case '\n': buf.put(`\n`); break;
            case '\r': buf.put(`\r`); break;
            case '\t': buf.put(`\t`); break;
            default:
                if (c < 0x20 || c == 0x7f)
                    buf.put(format(`\x%02x`, cast(ubyte) c));
                else
                    buf.put(c);
                break;
        }
    }
    buf.put('"');
    return buf.data;
}

unittest
{
    auto doc = parseYAML("version: 0.2\nroot: .polka\ntools:\n  php: \"8.4\"\n");
    assert(doc.isMapping());
    assert(doc.get("version").kind == YamlKind.float_);
    assert(doc.get("version").rawScalar == "0.2");
    assert(doc.get("tools").get("php").kind == YamlKind.string_);
    assert(doc.get("tools").get("php").asString() == "8.4");

    // Comment preservation test
    string commented = "# environment comment\ntools:\n  # php version comment\n  php: \"8.4\" # inline php comment\n";
    CommentMap cmts;
    auto parsed = parseYAMLWithComments(commented, cmts);
    parsed.get("tools");
    auto outYaml = emitYAML(parsed, cmts);
    assert(outYaml.canFind("# environment comment"));
    assert(outYaml.canFind("# php version comment"));
    assert(outYaml.canFind("# inline php comment"));
    assert(outYaml.canFind(`php: "8.4"`));
}
