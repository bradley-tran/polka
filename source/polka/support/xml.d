module polka.support.xml;

import std.algorithm : startsWith;
import std.array : appender;
import std.string : indexOf, strip;

class XmlException : Exception
{
    this(string msg, string file = __FILE__, size_t line = __LINE__) @safe pure nothrow
    {
        super(msg, file, line);
    }
}

struct XmlElement
{
    string name;
    string[string] attrs;
    string text;
    XmlElement[] children;

    const(XmlElement)* child(string childName) const @trusted pure nothrow
    {
        foreach (ref c; children)
        {
            if (c.name == childName)
                return &c;
        }
        return null;
    }

    string childText(string childName) const @safe pure
    {
        auto c = child(childName);
        return c !is null ? c.text : "";
    }

    string attr(string attrName) const @safe pure nothrow
    {
        if (auto p = attrName in attrs)
            return *p;
        return "";
    }
}

/// Parses an XML document string and returns the root element.
XmlElement parseXML(string xml) @safe
{
    size_t pos = 0;

    void skipWS()
    {
        while (pos < xml.length && (xml[pos] == ' ' || xml[pos] == '\t' || xml[pos] == '\r' || xml[pos] == '\n'))
            pos++;
    }

    void skipCommentsAndPIs()
    {
        while (pos < xml.length)
        {
            skipWS();
            if (pos + 4 <= xml.length && xml[pos .. pos + 4] == "<!--")
            {
                auto end = xml.indexOf("-->", pos + 4);
                if (end < 0)
                    throw new XmlException("unterminated XML comment");
                pos = end + 3;
            }
            else if (pos + 2 <= xml.length && xml[pos .. pos + 2] == "<?")
            {
                auto end = xml.indexOf("?>", pos + 2);
                if (end < 0)
                    throw new XmlException("unterminated XML processing instruction");
                pos = end + 2;
            }
            else if (pos + 9 <= xml.length && xml[pos .. pos + 9] == "<!DOCTYPE")
            {
                auto end = xml.indexOf('>', pos + 9);
                if (end < 0)
                    throw new XmlException("unterminated DOCTYPE");
                pos = end + 1;
            }
            else
            {
                break;
            }
        }
    }

    // Strip UTF-8 BOM if present
    if (xml.length >= 3 && xml[0] == 0xEF && xml[1] == 0xBB && xml[2] == 0xBF)
        pos = 3;

    skipCommentsAndPIs();
    if (pos >= xml.length || xml[pos] != '<')
        throw new XmlException("expected XML root element");

    XmlElement parseElement()
    {
        if (pos >= xml.length || xml[pos] != '<')
            throw new XmlException("expected '<'");
        pos++; // skip '<'

        size_t nameStart = pos;
        while (pos < xml.length && xml[pos] != ' ' && xml[pos] != '\t' && xml[pos] != '\r'
            && xml[pos] != '\n' && xml[pos] != '>' && xml[pos] != '/')
        {
            pos++;
        }
        if (pos == nameStart)
            throw new XmlException("empty XML tag name");

        XmlElement el;
        el.name = xml[nameStart .. pos];

        // Parse attributes
        while (pos < xml.length)
        {
            skipWS();
            if (pos >= xml.length)
                throw new XmlException("unterminated XML tag <" ~ el.name ~ ">");
            if (xml[pos] == '/')
            {
                pos++;
                if (pos >= xml.length || xml[pos] != '>')
                    throw new XmlException("expected '/>' in <" ~ el.name ~ ">");
                pos++;
                return el;
            }
            if (xml[pos] == '>')
            {
                pos++;
                break;
            }

            size_t attrStart = pos;
            while (pos < xml.length && xml[pos] != '=' && xml[pos] != ' ' && xml[pos] != '\t' && xml[pos] != '>')
                pos++;
            string attrName = xml[attrStart .. pos];
            skipWS();
            if (pos >= xml.length || xml[pos] != '=')
                throw new XmlException("expected '=' after attribute " ~ attrName);
            pos++;
            skipWS();
            if (pos >= xml.length || (xml[pos] != '"' && xml[pos] != '\''))
                throw new XmlException("expected quote for attribute " ~ attrName);
            char quote = xml[pos++];
            size_t valStart = pos;
            while (pos < xml.length && xml[pos] != quote)
                pos++;
            if (pos >= xml.length)
                throw new XmlException("unterminated attribute value");
            string attrVal = unescapeXmlEntities(xml[valStart .. pos]);
            pos++;
            el.attrs[attrName] = attrVal;
        }

        // Parse element children & text
        auto textBuf = appender!string();
        while (pos < xml.length)
        {
            if (pos + 9 <= xml.length && xml[pos .. pos + 9] == "<![CDATA[")
            {
                auto end = xml.indexOf("]]>", pos + 9);
                if (end < 0)
                    throw new XmlException("unterminated CDATA");
                textBuf.put(xml[pos + 9 .. end]);
                pos = end + 3;
            }
            else if (pos + 4 <= xml.length && xml[pos .. pos + 4] == "<!--")
            {
                auto end = xml.indexOf("-->", pos + 4);
                if (end < 0)
                    throw new XmlException("unterminated comment");
                pos = end + 3;
            }
            else if (pos + 2 <= xml.length && xml[pos .. pos + 2] == "</")
            {
                pos += 2;
                auto closeEnd = xml.indexOf('>', pos);
                if (closeEnd < 0)
                    throw new XmlException("unterminated closing tag");
                auto closeName = xml[pos .. closeEnd].strip();
                if (closeName != el.name)
                    throw new XmlException("mismatched closing tag </" ~ closeName ~ "> for <" ~ el.name ~ ">");
                pos = closeEnd + 1;
                el.text = textBuf.data;
                return el;
            }
            else if (xml[pos] == '<')
            {
                el.children ~= parseElement();
            }
            else
            {
                size_t textStart = pos;
                while (pos < xml.length && xml[pos] != '<')
                    pos++;
                textBuf.put(unescapeXmlEntities(xml[textStart .. pos]));
            }
        }

        throw new XmlException("unexpected EOF before </" ~ el.name ~ ">");
    }

    return parseElement();
}

private string unescapeXmlEntities(string s) @safe pure
{
    if (s.indexOf('&') < 0)
        return s;
    auto buf = appender!string();
    for (size_t i = 0; i < s.length; i++)
    {
        if (s[i] == '&')
        {
            auto semi = s.indexOf(';', i + 1);
            if (semi > 0 && semi - i <= 10)
            {
                auto ent = s[i + 1 .. semi];
                switch (ent)
                {
                    case "amp":  buf.put('&');  i = semi; continue;
                    case "lt":   buf.put('<');  i = semi; continue;
                    case "gt":   buf.put('>');  i = semi; continue;
                    case "quot": buf.put('"');  i = semi; continue;
                    case "apos": buf.put('\''); i = semi; continue;
                    default: break;
                }
            }
        }
        buf.put(s[i]);
    }
    return buf.data;
}

unittest
{
    string sample = `<?xml version="1.0" encoding="UTF-8" ?>
    <a version="2.0">
        <r><v>6.2.0</v><s>stable</s></r>
        <r><v>6.3.0RC1</v><s>beta</s></r>
    </a>`;
    auto root = parseXML(sample);
    assert(root.name == "a");
    assert(root.attr("version") == "2.0");
    assert(root.children.length == 2);
    assert(root.children[0].childText("v") == "6.2.0");
    assert(root.children[0].childText("s") == "stable");
}
