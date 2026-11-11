module polka.tools.checksum;

import std.algorithm : any, startsWith, endsWith;
import std.format : format;
import std.path : baseName;
import std.string : icmp, indexOf, replace, split, strip, toLower, toUpper;

import polka.support.crypto : fileDigestHex, md5Hex, sha256Hex, sha3_256Hex;
import polka.tools.types;

/// Returns expected digest byte length for a hash checksum algorithm.
size_t checksumByteSize(ChecksumAlgorithm algorithm) @safe pure
{
    switch (algorithm)
    {
        case ChecksumAlgorithm.md5:
            return 16;
        case ChecksumAlgorithm.sha256:
        case ChecksumAlgorithm.sha3_256:
            return 32;
        default:
            throw new Exception(format(`unsupported checksum algorithm "%s"`, cast(string) algorithm));
    }
}

/// Computes the hex checksum and byte size of `filePath`.
void checksumFile(ChecksumAlgorithm algorithm, string filePath, out string checksum, out long size) @trusted
{
    checksum = fileDigestHex(cast(string) algorithm, filePath, size);
}

/// Computes the hex checksum of an in-memory buffer for `algorithm`.
string checksumBytes(ChecksumAlgorithm algorithm, const(ubyte)[] data) @safe
{
    switch (algorithm)
    {
        case ChecksumAlgorithm.md5:
            return md5Hex(data);
        case ChecksumAlgorithm.sha256:
            return sha256Hex(data);
        case ChecksumAlgorithm.sha3_256:
            return sha3_256Hex(data);
        default:
            throw new Exception(format(`unsupported checksum algorithm "%s"`, cast(string) algorithm));
    }
}

/// Validates that `checksum` is a well-formed hex digest of the right byte length for `algorithm`.
void validateChecksumValue(ChecksumAlgorithm algorithm, string checksum, out string normalized, out size_t byteSize) @safe
{
    byteSize = checksumByteSize(algorithm);
    normalized = checksum.strip();
    if (normalized.length != byteSize * 2)
        throw new Exception(format("invalid %s checksum length %d", cast(string) algorithm, normalized.length));

    foreach (char c; normalized)
    {
        bool isHex = (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F');
        if (!isHex)
            throw new Exception(format("invalid %s checksum encoding: invalid hex digit '%s'", cast(string) algorithm, c));
    }
}

/// Verifies `filePath` matches `expected` under SHA-256.
void verifyChecksum(string expected, string filePath) @trusted
{
    verifyFileChecksum(ChecksumAlgorithm.sha256, expected, filePath);
}

/// Verifies `filePath` against `expected` using `algorithm`.
void verifyFileChecksum(ChecksumAlgorithm algorithm, string expected, string filePath) @trusted
{
    if (algorithm == ChecksumAlgorithm.none)
        return;

    string normExpected;
    size_t expectedBytes;
    validateChecksumValue(algorithm, expected, normExpected, expectedBytes);

    long size = 0;
    string actual;
    checksumFile(algorithm, filePath, actual, size);
    if (icmp(normExpected, actual) != 0)
        throw new Exception(format("checksum mismatch for %s: want %s, got %s", filePath, normExpected, actual));
}

/// Parses a standard single SHA-256 checksum string or checksum file text.
string parseChecksumValue(string value) @safe
{
    return parseChecksumValueForAlgorithm(ChecksumAlgorithm.sha256, value, "");
}

/// Parses a checksum response for `algorithm`, optionally matching `sourceFileName`.
string parseChecksumValueForAlgorithm(ChecksumAlgorithm algorithm, string value, string sourceFileName) @safe
{
    auto normalizedSourceFileName = normalizeChecksumFileName(sourceFileName);
    string labeledChecksum;
    bool handledLabel = false;
    if (parseLabeledChecksumValueForAlgorithm(algorithm, value, normalizedSourceFileName, labeledChecksum, handledLabel))
    {
        return labeledChecksum;
    }

    auto lines = value.strip().split("\n");
    string firstChecksum = "";
    foreach (line; lines)
    {
        auto fields = splitFieldsWhitespace(line.strip());
        if (fields.length == 0)
            continue;

        auto checksum = fields[0];
        if (firstChecksum.length == 0)
            firstChecksum = checksum;

        if (normalizedSourceFileName.length == 0 || checksumLineMatchesSource(fields[1 .. $], normalizedSourceFileName))
        {
            string norm;
            size_t sz;
            validateChecksumValue(algorithm, checksum, norm, sz);
            return norm;
        }
    }

    if (normalizedSourceFileName.length > 0)
        throw new Exception(format("no checksum found for %s", sourceFileName));
    if (firstChecksum.length == 0)
        throw new Exception("empty checksum response");

    string normFirst;
    size_t szFirst;
    validateChecksumValue(algorithm, firstChecksum, normFirst, szFirst);
    return normFirst;
}

/// Normalizes checksum target file names (stripping `*`, `./`, trailing `:`, backslashes, dir prefixes).
string normalizeChecksumFileName(string fileName) @safe
{
    auto normalized = fileName.strip();
    if (normalized.length == 0)
        return "";
    if (normalized.startsWith("*"))
        normalized = normalized[1 .. $];
    if (normalized.startsWith("./"))
        normalized = normalized[2 .. $];
    normalized = normalized.replace("\\", "/");
    if (normalized.endsWith(":"))
        normalized = normalized[0 .. $ - 1];
    // Extract basename after forward slash
    auto trimmed = normalized.strip();
    if (trimmed.length == 0)
        return "";
    return baseName(trimmed).strip();
}

private bool checksumLineMatchesSource(const(string)[] fields, string sourceFileName) @safe
{
    foreach (field; fields)
    {
        if (normalizeChecksumFileName(field) == sourceFileName)
            return true;
    }
    return false;
}

private bool parseLabeledChecksumValueForAlgorithm(
    ChecksumAlgorithm algorithm,
    string value,
    string normalizedSourceFileName,
    out string result,
    out bool matchedLabel
) @safe
{
    string label;
    if (!checksumLabelForAlgorithm(algorithm, label))
        return false;

    auto lines = value.strip().split("\n");
    foreach (idx, rawLine; lines)
    {
        auto trimmedLine = rawLine;
        if (trimmedLine.startsWith("\ufeff"))
            trimmedLine = trimmedLine[3 .. $];
        auto fields = splitFieldsWhitespace(trimmedLine.strip());
        if (fields.length == 0)
            continue;

        auto firstUpper = fields[0].toUpper();
        if (firstUpper.endsWith(":"))
            firstUpper = firstUpper[0 .. $ - 1];
        if (firstUpper != label ~ "-CHECKSUM")
            continue;

        if (normalizedSourceFileName.length > 0 && !checksumLineMatchesSource(fields[1 .. $], normalizedSourceFileName))
            continue;

        matchedLabel = true;
        foreach (valueLine; lines[idx + 1 .. $])
        {
            auto valueFields = splitFieldsWhitespace(valueLine.strip());
            if (valueFields.length == 0)
                continue;
            size_t sz;
            validateChecksumValue(algorithm, valueFields[0], result, sz);
            return true;
        }

        throw new Exception(format("missing %s checksum value", cast(string) algorithm));
    }

    return false;
}

private bool checksumLabelForAlgorithm(ChecksumAlgorithm algorithm, out string label) @safe pure
{
    switch (algorithm)
    {
        case ChecksumAlgorithm.md5:
            label = "MD5";
            return true;
        case ChecksumAlgorithm.sha256:
            label = "SHA256";
            return true;
        case ChecksumAlgorithm.sha3_256:
            label = "SHA3-256";
            return true;
        default:
            label = "";
            return false;
    }
}

package string[] splitFieldsWhitespace(string s) @safe pure
{
    string[] parts;
    size_t i = 0;
    while (i < s.length)
    {
        while (i < s.length && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n' || s[i] == '\v' || s[i] == '\f'))
            i++;
        if (i >= s.length)
            break;
        size_t start = i;
        while (i < s.length && !(s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n' || s[i] == '\v' || s[i] == '\f'))
            i++;
        parts ~= s[start .. i];
    }
    return parts;
}

unittest
{
    import std.array : replicate;

    // TestParseChecksumValueAcceptsSha256sumFormat
    auto expected = replicate("a", 64);
    assert(parseChecksumValue(expected ~ "  composer.phar\n") == expected);

    // TestParseChecksumValueAcceptsApacheLoungeFormat
    auto expectedApache = replicate("A", 64);
    auto checksumData = "\ufeffChecksums created with GPGHash\n\n" ~
        "SHA1-Checksum for: httpd-2.4.68-260617-Win64-VS18.zip:\n" ~
        replicate("B", 40) ~ "\n\n" ~
        "SHA256-Checksum for: httpd-2.4.68-260617-Win64-VS18.zip:\n" ~
        expectedApache;
    assert(parseChecksumValueForAlgorithm(ChecksumAlgorithm.sha256, checksumData, "httpd-2.4.68-260617-Win64-VS18.zip") == expectedApache);
}
