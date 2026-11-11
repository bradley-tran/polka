module polka.cli.serve_tls;

// Local development TLS asset management for HTTPS serving:
// generates and caches the Polka local development CA and shared server certificate
// covering localhost and project serve hostnames, and materializes plaintext runtime
// keys where required by Nginx, Apache, FrankenPHP, or Traefik.

import std.algorithm : canFind, endsWith;
import std.file : exists, isDir, mkdirRecurse, readText, remove;
import std.format : format;
import std.path : baseName, buildPath, dirName;
import std.socket : parseAddress;
import std.string : icmp, strip, toLower;

import polka.cli.tls_private_key :
    defaultTLSCAKeyStorageOptions,
    materializeTLSPrivateKeyFile,
    readTLSPrivateKeyFile,
    TLSCAKeyStorageOptions,
    TLSCAKeyStoragePolicy,
    writePEMFile,
    writeTLSPrivateKeyFile;
import polka.cli.types : callServeNow, isMissingFileError;
import polka.support.crypto :
    generateLocalCACertificate,
    generateServerCertificate,
    ParsedCertificate,
    parseX509CertificatePEM,
    pemDecode;
import polka.support.fs : cleanPath, octal;

enum string serveTLSSubdir = "cert";
enum string serveTLSCACertName = "polka-local-ca.crt";
enum string serveTLSCAKeyName = "polka-local-ca.key";
enum string serveTLSCertFileName = "polka-local.crt";
enum string serveTLSKeyFileName = "polka-local.key";

/// Resolved TLS certificate and key file paths referenced by generated webserver configs.
struct ServeTLSConfig
{
    bool enabled = false;
    string certificatePath;
    string certificateKeyPath;
}

/// Pair of global or runtime TLS certificate/key paths.
struct TLSCertKeyPaths
{
    string certPath;
    string keyPath;
}

/// Ensures the global server certificate covers `host`, creating CA/cert assets as needed.
TLSCertKeyPaths ensureGlobalTLSCertificate(string cacheDir, string host) @trusted
{
    return ensureGlobalTLSCertificateWithOptions(cacheDir, host, defaultTLSCAKeyStorageOptions());
}

/// Ensures the shared certificate covers `host` and materializes a plaintext runtime
/// private key inside `runtimeDir` for webserver processes.
TLSCertKeyPaths ensureGlobalTLSCertificateRuntimeKey(string cacheDir, string runtimeDir, string host) @trusted
{
    return ensureGlobalTLSCertificateRuntimeKeyWithOptions(cacheDir, runtimeDir, host, defaultTLSCAKeyStorageOptions());
}

/// Variant of `ensureGlobalTLSCertificateRuntimeKey` with explicit key storage options.
TLSCertKeyPaths ensureGlobalTLSCertificateRuntimeKeyWithOptions(
    string cacheDir,
    string runtimeDir,
    string host,
    TLSCAKeyStorageOptions storageOptions
) @trusted
{
    auto paths = ensureGlobalTLSCertificateWithOptions(cacheDir, host, storageOptions);
    auto runtimeKeyPath = buildPath(runtimeDir, serveTLSKeyFileName);
    try
    {
        materializeTLSPrivateKeyFile(paths.keyPath, runtimeKeyPath, storageOptions);
    }
    catch (Exception e)
    {
        throw new Exception("write local tls runtime private key: " ~ e.msg);
    }
    return TLSCertKeyPaths(paths.certPath, runtimeKeyPath);
}

/// Returns the shared certificate and encrypted/plaintext key paths, issuing or
/// regenerating them when absent or when `host` is not yet in SANs.
TLSCertKeyPaths ensureGlobalTLSCertificateWithOptions(
    string cacheDir,
    string host,
    TLSCAKeyStorageOptions storageOptions
) @trusted
{
    auto serverPaths = globalTLSCertificatePaths(cacheDir);
    auto caPaths = globalTLSCACertificatePaths(cacheDir);
    bool hasCA = certificateFileExists(caPaths.certPath) && certificateFileExists(caPaths.keyPath);
    if (certificateFileExists(serverPaths.certPath) &&
        certificateFileExists(serverPaths.keyPath) &&
        hasCA &&
        serverCertificateCoversHost(serverPaths.certPath, host))
    {
        return serverPaths;
    }
    if (hasCA)
        return createGlobalTLSServerCertificateWithOptions(cacheDir, host, storageOptions);

    return createGlobalTLSCertificateWithOptions(cacheDir, host, storageOptions);
}

/// Removes existing CA/server cert and key files and issues a fresh local CA + server certificate.
TLSCertKeyPaths regenerateGlobalTLSCertificate(string cacheDir) @trusted
{
    return regenerateGlobalTLSCertificateWithOptions(cacheDir, defaultTLSCAKeyStorageOptions());
}

/// Variant of `regenerateGlobalTLSCertificate` with explicit key storage options.
TLSCertKeyPaths regenerateGlobalTLSCertificateWithOptions(
    string cacheDir,
    TLSCAKeyStorageOptions storageOptions
) @trusted
{
    auto serverPaths = globalTLSCertificatePaths(cacheDir);
    auto caPaths = globalTLSCACertificatePaths(cacheDir);
    foreach (p; [serverPaths.certPath, serverPaths.keyPath, caPaths.certPath, caPaths.keyPath])
    {
        try
        {
            if (exists(p))
                remove(p);
        }
        catch (Exception e)
        {
            if (!isMissingFileError(e))
                throw new Exception(format("remove existing tls certificate file %s: %s", p, e.msg));
        }
    }

    return createGlobalTLSCertificateWithOptions(cacheDir, "", storageOptions);
}

/// Creates a fresh local CA and server certificate covering `host`.
TLSCertKeyPaths createGlobalTLSCertificate(string cacheDir, string host) @trusted
{
    return createGlobalTLSCertificateWithOptions(cacheDir, host, defaultTLSCAKeyStorageOptions());
}

/// Creates a fresh local CA + CA keypair and issues a signed server certificate for `host`.
TLSCertKeyPaths createGlobalTLSCertificateWithOptions(
    string cacheDir,
    string host,
    TLSCAKeyStorageOptions storageOptions
) @trusted
{
    auto serverPaths = globalTLSCertificatePaths(cacheDir);
    auto caPaths = globalTLSCACertificatePaths(cacheDir);
    auto certDir = dirName(serverPaths.certPath);
    try
    {
        mkdirRecurse(certDir);
    }
    catch (Exception e)
    {
        throw new Exception("create local tls certificate directory: " ~ e.msg);
    }

    auto now = callServeNow();
    auto caPair = generateLocalCACertificate(now);

    try
    {
        string blockType;
        ubyte[] caDER;
        if (!pemDecode(caPair.certPEM, blockType, caDER))
            throw new Exception("failed to decode generated local CA PEM");
        writePEMFile(caPaths.certPath, octal!644, "CERTIFICATE", caDER);
    }
    catch (Exception e)
    {
        throw new Exception("write local ca certificate: " ~ e.msg);
    }

    try
    {
        writeTLSPrivateKeyFile(caPaths.keyPath, caPair.keyPKCS1DER, storageOptions);
    }
    catch (Exception e)
    {
        throw new Exception("write local ca private key: " ~ e.msg);
    }

    return createGlobalTLSServerCertificateWithCA(cacheDir, host, caPair.certPEM, caPair.keyPKCS1DER, storageOptions);
}

/// Issues a new server certificate signed by the existing local CA using default key options.
TLSCertKeyPaths createGlobalTLSServerCertificate(string cacheDir, string host) @trusted
{
    return createGlobalTLSServerCertificateWithOptions(cacheDir, host, defaultTLSCAKeyStorageOptions());
}

/// Loads the cached local CA and signs a server certificate covering `host`.
TLSCertKeyPaths createGlobalTLSServerCertificateWithOptions(
    string cacheDir,
    string host,
    TLSCAKeyStorageOptions storageOptions
) @trusted
{
    string caCertPEM;
    ubyte[] caKeyDER;
    loadGlobalTLSCAWithOptions(cacheDir, storageOptions, caCertPEM, caKeyDER);
    return createGlobalTLSServerCertificateWithCA(cacheDir, host, caCertPEM, caKeyDER, storageOptions);
}

private TLSCertKeyPaths createGlobalTLSServerCertificateWithCA(
    string cacheDir,
    string host,
    string caCertPEM,
    const(ubyte)[] caKeyDER,
    TLSCAKeyStorageOptions storageOptions
) @trusted
{
    auto serverPaths = globalTLSCertificatePaths(cacheDir);
    string[] dnsNames;
    string[] ipAddresses;
    serverCertificateNames(host, dnsNames, ipAddresses);

    auto serverPair = generateServerCertificate(caCertPEM, caKeyDER, dnsNames, ipAddresses, callServeNow());

    try
    {
        string blockType;
        ubyte[] serverDER;
        if (!pemDecode(serverPair.certPEM, blockType, serverDER))
            throw new Exception("failed to decode generated server certificate PEM");
        writePEMFile(serverPaths.certPath, octal!644, "CERTIFICATE", serverDER);
    }
    catch (Exception e)
    {
        throw new Exception("write local tls certificate: " ~ e.msg);
    }

    try
    {
        writeTLSPrivateKeyFile(serverPaths.keyPath, serverPair.keyPKCS1DER, storageOptions);
    }
    catch (Exception e)
    {
        throw new Exception("write local tls private key: " ~ e.msg);
    }

    return serverPaths;
}

/// Reads the cached local CA certificate PEM and private key PKCS#1 DER bytes.
void loadGlobalTLSCAWithOptions(
    string cacheDir,
    TLSCAKeyStorageOptions storageOptions,
    out string caCertPEM,
    out ubyte[] caKeyDER
) @trusted
{
    auto caPaths = globalTLSCACertificatePaths(cacheDir);
    try
    {
        auto parsed = readCertificateFile(caPaths.certPath);
        caCertPEM = parsed.pem;
    }
    catch (Exception e)
    {
        throw new Exception("read local ca certificate: " ~ e.msg);
    }

    try
    {
        caKeyDER = readTLSPrivateKeyFile(caPaths.keyPath, storageOptions);
    }
    catch (Exception e)
    {
        throw new Exception("read local ca private key: " ~ e.msg);
    }
}

/// Reports whether the stored server certificate explicitly verifies and lists `host`.
bool serverCertificateCoversHost(string certificatePath, string host) @trusted
{
    auto trimmed = stripHostBrackets(host);
    if (trimmed.length == 0)
        return true;

    ParsedCertificate cert;
    try
    {
        cert = readCertificateFile(certificatePath);
    }
    catch (Exception)
    {
        return false;
    }

    return cert.verifyHostname(trimmed) && cert.hasExactHost(trimmed);
}

/// Reports whether `cert` lists `host` as an exact SAN DNS name or IP address.
bool certificateHasExactHost(const ParsedCertificate cert, string host) @trusted
{
    return cert.hasExactHost(stripHostBrackets(host));
}

/// Computes the DNS names (`localhost`, `*.localhost`, optional host) and IP SANs
/// (`127.0.0.1`, `::1`, optional IP) for the shared development certificate.
void serverCertificateNames(string host, out string[] dnsNames, out string[] ipAddresses) @trusted
{
    dnsNames = ["localhost", "*.localhost"];
    ipAddresses = ["127.0.0.1", "::1"];

    auto trimmed = stripHostBrackets(host);
    if (trimmed.length == 0)
        return;

    string parsedIP;
    if (tryCanonicalIP(trimmed, parsedIP))
    {
        foreach (existing; ipAddresses)
        {
            if (existing == parsedIP)
                return;
        }
        ipAddresses ~= parsedIP;
        return;
    }

    foreach (existing; dnsNames)
    {
        if (icmp(existing, trimmed) == 0)
            return;
    }
    dnsNames ~= trimmed;
}

/// Reads and parses a PEM-encoded X.509 certificate file.
ParsedCertificate readCertificateFile(string path) @trusted
{
    auto text = readText(path);
    string blockType;
    ubyte[] unused;
    if (!pemDecode(text, blockType, unused) || blockType != "CERTIFICATE")
        throw new Exception(format("missing CERTIFICATE PEM block in %s", path));
    return parseX509CertificatePEM(text);
}

/// Returns the shared server certificate and private key paths under `cacheDir`.
TLSCertKeyPaths globalTLSCertificatePaths(string cacheDir) @safe
{
    auto certDir = globalTLSCertificateDir(cacheDir);
    return TLSCertKeyPaths(
        buildPath(certDir, serveTLSCertFileName),
        buildPath(certDir, serveTLSKeyFileName)
    );
}

/// Returns the local CA certificate and private key paths under `cacheDir`.
TLSCertKeyPaths globalTLSCACertificatePaths(string cacheDir) @safe
{
    auto certDir = globalTLSCertificateDir(cacheDir);
    return TLSCertKeyPaths(
        buildPath(certDir, serveTLSCACertName),
        buildPath(certDir, serveTLSCAKeyName)
    );
}

/// Returns the machine-global certificate directory, placed alongside `polka/cache`
/// (`.../polka/cert`) or `<cacheDir>/polka/cert`.
string globalTLSCertificateDir(string cacheDir) @safe
{
    auto cleanCacheDir = cleanPath(cacheDir);
    auto base = baseName(cleanCacheDir);
    auto parent = dirName(cleanCacheDir);
    if (icmp(base, "cache") == 0 && icmp(baseName(parent), "polka") == 0)
        return buildPath(parent, serveTLSSubdir);
    return buildPath(cleanCacheDir, "polka", serveTLSSubdir);
}

/// Reports whether `path` exists and is a regular non-directory file.
bool certificateFileExists(string path) @trusted
{
    try
    {
        return exists(path) && !isDir(path);
    }
    catch (Exception)
    {
        return false;
    }
}

private string stripHostBrackets(string host) @safe pure
{
    auto trimmed = host.strip();
    while (trimmed.length > 0 && (trimmed[0] == '[' || trimmed[0] == ']'))
        trimmed = trimmed[1 .. $];
    while (trimmed.length > 0 && (trimmed[$ - 1] == '[' || trimmed[$ - 1] == ']'))
        trimmed = trimmed[0 .. $ - 1];
    return trimmed;
}

private bool tryCanonicalIP(string s, out string canonical) @trusted
{
    canonical = "";
    try
    {
        canonical = parseAddress(s).toAddrString();
        return canonical.length > 0;
    }
    catch (Exception)
    {
        return false;
    }
}

unittest
{
    import polka.cli.keyring : keyringMockInit;
    import polka.cli.test_helpers : assertEncryptedTLSKeyFile;
    import polka.support.crypto : certificateSerialHex, verifyX509Chain;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    keyringMockInit();
    scope (exit) keyringMockInit();

    // TestGlobalTLSCertificateCoversLocalhostNames
    {
        auto cacheDir = createTempDir("polka-tls-cov-");
        scope (exit) rmdirRecurseQuiet(cacheDir);

        auto paths = ensureGlobalTLSCertificate(cacheDir, "site.localhost");
        assert(paths.certPath == buildPath(cacheDir, "polka", serveTLSSubdir, serveTLSCertFileName));
        assert(paths.keyPath == buildPath(cacheDir, "polka", serveTLSSubdir, serveTLSKeyFileName));
        assertEncryptedTLSKeyFile(paths.keyPath);

        auto caPaths = globalTLSCACertificatePaths(cacheDir);
        auto cert = readCertificateFile(paths.certPath);
        auto caCert = readCertificateFile(caPaths.certPath);
        assert(caCert.isCA);
        assert(!cert.isCA);
        assert(cert.verifyHostname("localhost"));
        assert(cert.verifyHostname("site.localhost"));
        assert(cert.verifyHostname("127.0.0.1"));
        assert(verifyX509Chain(cert.pem, caCert.pem));
    }

    // TestEnsureGlobalTLSCertificateUsesExistingPlaintextCAKey
    {
        auto cacheDir = createTempDir("polka-tls-plainca-");
        scope (exit) rmdirRecurseQuiet(cacheDir);

        regenerateGlobalTLSCertificateWithOptions(cacheDir, TLSCAKeyStorageOptions(TLSCAKeyStoragePolicy.plaintextRequired));
        auto caPaths = globalTLSCACertificatePaths(cacheDir);
        auto caBefore = certificateSerialHex(readCertificateFile(caPaths.certPath).pem);
        assert(readText(caPaths.keyPath).canFind("-----BEGIN RSA PRIVATE KEY-----"));

        auto srvPaths = ensureGlobalTLSCertificate(cacheDir, "plain.localhost");
        auto caAfter = certificateSerialHex(readCertificateFile(caPaths.certPath).pem);
        assert(caBefore == caAfter);
        auto cert = readCertificateFile(srvPaths.certPath);
        assert(cert.verifyHostname("plain.localhost"));
    }

    // TestEnsureGlobalTLSCertificateRegeneratesServerCertificateForExactHost
    {
        auto cacheDir = createTempDir("polka-tls-regen-");
        scope (exit) rmdirRecurseQuiet(cacheDir);

        regenerateGlobalTLSCertificate(cacheDir);
        auto caPaths = globalTLSCACertificatePaths(cacheDir);
        auto caBefore = certificateSerialHex(readCertificateFile(caPaths.certPath).pem);

        auto srvPaths = ensureGlobalTLSCertificate(cacheDir, "test-site.localhost");
        auto caAfter = certificateSerialHex(readCertificateFile(caPaths.certPath).pem);
        assert(caBefore == caAfter);
        auto cert = readCertificateFile(srvPaths.certPath);
        assert(cert.verifyHostname("test-site.localhost"));
        assert(certificateHasExactHost(cert, "test-site.localhost"));
    }

    // TestGlobalTLSCertificateDirUsesDefaultCacheSibling
    {
        auto cacheDir = buildPath("cache-root", "polka", "cache");
        assert(globalTLSCertificateDir(cacheDir) == buildPath("cache-root", "polka", serveTLSSubdir));
    }
}
