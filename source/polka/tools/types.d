module polka.tools.types;

import std.algorithm : any, sort;
import std.conv : to;
import std.format : format;
import std.path : buildPath;
import std.string : indexOf, split, strip, toLower;

import polka.config;
import polka.support.fs : fromSlashPath;
import polka.support.http : HTTPClient;
import polka.support.yaml : YamlValue;
import polka.tools.ids : isValidName;

/// Log severity level constants produced by managed tool manifests.
enum string LogLevelInfo = "info";
enum string LogLevelError = "error";
enum string LogLevelDebug = "debug";

/// Returns the canonical lowercase log level string.
string normalizeLogLevel(string level) @safe pure
{
    return level.strip().toLower();
}

/// Reports whether `level` is a manifest-supported log level.
bool validLogLevel(string level) @safe pure
{
    switch (normalizeLogLevel(level))
    {
        case LogLevelInfo:
        case LogLevelError:
        case LogLevelDebug:
            return true;
        default:
            return false;
    }
}

/// Scalar schema type of one `settings.<namespace>.<key>` entry.
enum SettingType : string
{
    string_ = "string",
    bool_ = "bool",
    port = "port",
}

/// Converts a raw manifest string into a validated `SettingType`.
bool parseSettingType(string raw, out SettingType parsed) @safe pure
{
    switch (raw.strip().toLower())
    {
        case "string":
            parsed = SettingType.string_;
            return true;
        case "bool":
            parsed = SettingType.bool_;
            return true;
        case "port":
            parsed = SettingType.port;
            return true;
        default:
            parsed = SettingType.string_;
            return false;
    }
}

/// Checksum algorithm identifiers supported by tool manifests and cache metadata.
enum ChecksumAlgorithm : string
{
    none = "",
    md5 = "md5",
    sha256 = "sha256",
    sha3_256 = "sha3-256",
    /// Verifies GitHub source-archive ZIP comment commit SHA instead of a file hash.
    embeddedCommitID = "embedded-commit-id",
}

/// Parses a checksum algorithm name from manifest or cache JSON.
bool parseChecksumAlgorithm(string raw, out ChecksumAlgorithm alg) @safe pure
{
    switch (raw.strip().toLower())
    {
        case "":
            alg = ChecksumAlgorithm.none;
            return true;
        case "md5":
            alg = ChecksumAlgorithm.md5;
            return true;
        case "sha256":
            alg = ChecksumAlgorithm.sha256;
            return true;
        case "sha3-256":
            alg = ChecksumAlgorithm.sha3_256;
            return true;
        case "embedded-commit-id":
            alg = ChecksumAlgorithm.embeddedCommitID;
            return true;
        default:
            alg = ChecksumAlgorithm.none;
            return false;
    }
}

/// Supported archive formats for downloaded tool payloads.
enum ArchiveFormat : string
{
    none = "",
    zip = "zip",
    tarGz = "tar.gz",
    tarXz = "tar.xz",
}

/// Parses an archive format name from manifest or cache JSON.
bool parseArchiveFormat(string raw, out ArchiveFormat fmt) @safe pure
{
    switch (raw.strip().toLower())
    {
        case "":
            fmt = ArchiveFormat.none;
            return true;
        case "zip":
            fmt = ArchiveFormat.zip;
            return true;
        case "tar.gz":
            fmt = ArchiveFormat.tarGz;
            return true;
        case "tar.xz":
            fmt = ArchiveFormat.tarXz;
            return true;
        default:
            fmt = ArchiveFormat.none;
            return false;
    }
}

/// Context passed to `ToolPlugin.download`.
struct DownloadContext
{
    HTTPClient client;
    string cacheDir;
    string tool;
    string version_;
}

/// Outcome of installing (or skipping) one managed tool.
struct InstallResult
{
    string tool;
    string version_;
    string cachePath;
    string targetPath;
    bool downloaded = false;
    bool skipped = false;
}

/// Context passed to `ToolPlugin.postInstall`.
struct InstallContext
{
    string projectDir;
    string rootDir;
    string envsDir;
    string binDir;
    string cacheDir;
    Environment environment;
    InstallResult result;
}

/// Tool installation tuple used for dependency ordering and batch execution.
struct InstallRequest
{
    string tool;
    string version_;

    bool opEquals(const InstallRequest other) const @safe pure nothrow @nogc
    {
        return tool == other.tool && version_ == other.version_;
    }
}

/// One project-local log file produced by a managed tool.
struct LogEntry
{
    string path;
    string level;

    bool opEquals(const LogEntry other) const @safe pure nothrow @nogc
    {
        return path == other.path && level == other.level;
    }
}

/// Abstract interface for managed tools installed and dispatched by Polka.
interface ToolPlugin
{
    string id() const;
    string version_(const Environment environment) const;
    bool[string] phpExtensions() const;
    void validate(const Environment environment) const;
    string[] installCandidates(string root, string ver) const;
    string[] dispatchCommands() const;
    string[] cleanupCommands() const;
    string[] activeCommands(const Environment environment) const;
    string[] dispatchCandidates(string root, string executable, string ver) const;
    LogEntry[] logs() const;
    void download(DownloadContext ctx) const;
    void postInstall(InstallContext ctx) const;
}

/// Optional interface for tools that require prerequisite managed payloads.
interface ToolDependencyProvider
{
    InstallRequest[] dependencies(const Environment environment) const;
}

/// Optional interface for tools declaring versionless settings schemas or mutual exclusions.
interface ToolSettingsProvider
{
    SettingType[string] settings() const;
    string settingsNamespace() const;
    string[] exclusiveWith() const;
}

/// Optional interface for internal-only tools excluded from user project configs.
interface InternalToolInfo
{
    bool internalOnly() const;
    string internalVersion(const Environment environment) const;
}

/// Downloader abstraction implemented by `HTTPDownloader`.
interface Downloader
{
    void download(string cacheDir, string tool, string ver);
}

/// Tool resolution result for shims and `polka <command>` dispatch.
struct DispatchRequest
{
    string configTool;
    string executable;
    ToolPlugin plugin;
}

/// Tool resolution result for `polka logs`.
struct LogRequest
{
    string configTool;
    ToolPlugin plugin;
}

// ============================================================================
// Declarative Manifest Data Structures
// ============================================================================

alias ManifestPlatformPaths = string[][string];
alias ManifestLogs = string[][string];

/// Optional `[min, below)` version range constraint for a tool manifest.
struct ManifestVersionConstraint
{
    string min;
    string below;

    /// Reports whether neither bound is configured.
    bool isZero() const @safe pure
    {
        return min.strip().length == 0 && below.strip().length == 0;
    }
}

/// Managed tool prerequisite resolved when the declaring tool is enabled.
struct ManifestDependency
{
    string tool;
    string version_;
}

/// Command wrapper script specification generated next to target binaries.
struct ManifestWrapper
{
    ManifestPlatformPaths target;
    string[] args;
    bool skipIfExists = false;
}

/// GitHub release or tag download metadata in a tool manifest.
struct ManifestGitHubDownload
{
    string owner;
    string repo;
    string tagPrefix;
    string mode;
}

/// HTML/JSON release-index scraper download configuration.
struct ManifestReleaseIndexDownload
{
    string url;
    string pattern;
}

/// Platform download asset definition in a tool manifest.
struct DownloadAsset
{
    string fileName;
    string sourceFileName;
    string url;
    string installPath;
    string checksum;
    string checksumURL;
    ChecksumAlgorithm checksumAlgorithm = ChecksumAlgorithm.none;
    ArchiveFormat archiveFormat = ArchiveFormat.none;
}

alias DatabaseDownloadAsset = DownloadAsset;

/// Download section of a declarative tool manifest.
struct ManifestDownload
{
    ManifestGitHubDownload github;
    ManifestReleaseIndexDownload releaseIndex;
    DownloadAsset[string] assets;
    bool hasCatalog = false;

    bool hasGitHub() const @safe pure
    {
        return github.owner.strip().length > 0 || github.repo.strip().length > 0;
    }

    bool hasReleaseIndex() const @safe pure
    {
        return releaseIndex.url.strip().length > 0 || releaseIndex.pattern.strip().length > 0;
    }
}

/// Parsed and validated declarative tool manifest (`tools/manifests/*.yaml`).
struct PluginManifest
{
    string id;
    bool internalOnly = false;
    string[] exclusiveWith;
    ManifestVersionConstraint versionConstraint;
    ManifestDependency[] dependencies;
    ManifestWrapper[string] wrappers;
    SettingType[string] settings;
    string rawSettingsInvalidTypeError;
    string settingsFrom;
    string[] phpExtensions;
    ManifestPlatformPaths installCandidates;
    string[] dispatchCommands;
    string[] cleanupCommands;
    string[] activeCommands;
    ManifestPlatformPaths[string] dispatchCandidates;
    ManifestLogs logs;
    ManifestDownload download;
}

/// Hooks provided by Go/D tool plugins to augment declarative manifests.
struct PluginHooks
{
    void delegate(const Environment) validate;
    void delegate(DownloadContext) download;
    void delegate(InstallContext) postInstall;
    InstallRequest[] delegate(const Environment) dependencies;
}

// ============================================================================
// Shared Platform, Version & Collection Helpers
// ============================================================================

/// Returns `<os>-<arch>`.
string platformKey(string goos, string goarch) @safe pure
{
    return goos.strip() ~ "-" ~ goarch.strip();
}

/// Returns ordered platform keys for download asset selection.
string[] downloadPlatformKeys(string goos, string goarch) @safe pure
{
    string[] keys;
    if (goos.strip().length > 0 && goarch.strip().length > 0)
        keys ~= platformKey(goos, goarch);
    keys ~= "all";
    return keys;
}

/// Returns a defensive copy of a string slice (returns null if empty).
string[] copyStrings(const(string)[] values) @safe pure
{
    if (values.length == 0)
        return null;
    string[] copied = values.dup;
    return copied;
}

/// Returns a defensive copy of a `SettingType[string]` map.
SettingType[string] copySettingTypes(const(SettingType[string]) values) @safe pure
{
    if (values.length == 0)
        return null;
    SettingType[string] copied;
    foreach (k, v; values)
        copied[k] = v;
    return copied;
}

/// Returns a defensive copy of a `LogEntry[]` slice.
LogEntry[] copyLogEntries(const(LogEntry)[] values) @safe pure
{
    if (values.length == 0)
        return null;
    return values.dup;
}

/// Returns a deep mutable copy of a platform path mapping.
ManifestPlatformPaths copyPlatformPaths(const(ManifestPlatformPaths) src) @safe pure
{
    if (src.length == 0)
        return null;
    ManifestPlatformPaths copied;
    foreach (k, v; src)
        copied[k] = copyStrings(v);
    return copied;
}

/// Selects matching relative paths from a platform map (`os-arch`, `os`, `all`).
string[] selectManifestPlatformPaths(const(ManifestPlatformPaths) platformPaths, string goos, string goarch) @safe pure
{
    foreach (key; [platformKey(goos, goarch), goos.strip(), "all"])
    {
        if (auto paths = key in platformPaths)
            return copyStrings(*paths);
    }
    return null;
}

/// Resolves relative manifest candidate paths under `baseDir` for `goos`/`goarch`.
string[] manifestCandidatePaths(string baseDir, const(ManifestPlatformPaths) platformPaths, string goos, string goarch) @safe
{
    auto relativePaths = selectManifestPlatformPaths(platformPaths, goos, goarch);
    if (relativePaths.length == 0)
        return null;
    string[] candidates;
    candidates.reserve(relativePaths.length);
    foreach (rel; relativePaths)
        candidates ~= buildPath(baseDir, fromSlashPath(rel));
    return candidates;
}

/// Compares dotted numeric/lexical version strings segment-by-segment.
int compareVersions(string left, string right) @safe pure
{
    auto leftParts = left.strip().split(".");
    auto rightParts = right.strip().split(".");
    auto count = leftParts.length > rightParts.length ? leftParts.length : rightParts.length;

    foreach (idx; 0 .. count)
    {
        string leftPart = idx < leftParts.length ? leftParts[idx] : "0";
        string rightPart = idx < rightParts.length ? rightParts[idx] : "0";

        int leftVal = 0;
        int rightVal = 0;
        bool leftIsNum = tryParseDecimalInt(leftPart, leftVal);
        bool rightIsNum = tryParseDecimalInt(rightPart, rightVal);

        if (leftIsNum && rightIsNum)
        {
            if (leftVal > rightVal)
                return 1;
            if (leftVal < rightVal)
                return -1;
        }
        else
        {
            if (leftPart > rightPart)
                return 1;
            if (leftPart < rightPart)
                return -1;
        }
    }
    return 0;
}

private bool tryParseDecimalInt(string s, out int value) @safe pure nothrow @nogc
{
    if (s.length == 0)
        return false;
    int sign = 1;
    size_t start = 0;
    if (s[0] == '-')
    {
        sign = -1;
        start = 1;
    }
    else if (s[0] == '+')
    {
        start = 1;
    }
    if (start >= s.length)
        return false;
    long acc = 0;
    foreach (char c; s[start .. $])
    {
        if (c < '0' || c > '9')
            return false;
        acc = acc * 10 + (c - '0');
        if (acc > int.max)
            return false;
    }
    value = cast(int)(acc * sign);
    return true;
}

/// Returns true if `ver` equals `requested` or has `requested.` prefix.
bool versionMatchesRequest(string ver, string requested) @safe pure
{
    import std.algorithm : startsWith;
    return ver == requested || ver.startsWith(requested ~ ".");
}

/// Reports whether a version string is partial (fewer than 3 numeric parts, no `-`).
bool versionNeedsResolution(string ver) @safe pure
{
    import std.algorithm : canFind, count;
    auto trimmed = ver.strip();
    if (trimmed.length == 0)
        return false;
    return !trimmed.canFind("-") && trimmed.count(".") < 2;
}

/// Returns `major.minor` from a dotted version string, or the full trimmed string if single-part.
string versionMajorMinor(string ver) @safe pure
{
    auto trimmed = ver.strip();
    auto parts = trimmed.split(".");
    if (parts.length >= 2)
        return parts[0] ~ "." ~ parts[1];
    return trimmed;
}

/// Returns the dot-separated segment count of a version string.
size_t versionPartCount(string ver) @safe pure
{
    import std.algorithm : count;
    auto trimmed = ver.strip();
    if (trimmed.length == 0)
        return 0;
    return trimmed.count(".") + 1;
}

/// Truncates `ver` to at most `count` dot-separated parts.
string truncateVersionParts(string ver, size_t count) @safe pure
{
    import std.string : join;
    auto trimmed = ver.strip();
    if (count == 0 || trimmed.length == 0)
        return trimmed;
    auto parts = trimmed.split(".");
    if (parts.length <= count)
        return trimmed;
    return parts[0 .. count].join(".");
}

/// Validates a PHP extension module name identifier.
void validatePHPExtensionName(string name) @safe
{
    auto trimmed = name.strip();
    if (trimmed.length == 0)
        throw new Exception("php extension name cannot be empty");
    if (!isValidName(trimmed))
        throw new Exception(format(`invalid php extension name "%s": use letters, numbers, dots, dashes, or underscores`, name));
}
