module polka.cli.command_cert;

// Implements `polka cert-install`, regenerating the Polka local development CA
// and installing/replacing its certificate in the OS user trust store.

import std.algorithm : canFind;
import std.format : format;
import std.functional : toDelegate;
import std.path : absolutePath, buildPath;
import std.process : environment, execute;
import std.string : strip, toLower, toUpper;

import polka.backend : Store, TextWriter, writeOutf, writeOutln;
import polka.cli.serve_tls :
    globalTLSCACertificatePaths,
    readCertificateFile,
    regenerateGlobalTLSCertificateWithOptions;
import polka.cli.tls_private_key :
    TLSCAKeyStorageOptions,
    TLSCAKeyStoragePolicy;
import polka.cli.types : CommandContext, isMissingFileError, writeErrorf;
import polka.support.crypto : ParsedCertificate;
import polka.support.fs : currentOS;

/// Test seams replacing OS trust-store commands.
string delegate(string certificatePath) installCertificateToTrustStoreFunc;
string delegate(const ParsedCertificate certificate) removeCertificateFromTrustStoreFunc;

/// Parsed CLI options for `polka cert-install`.
struct CertInstallInput
{
    bool noEncryption = false;
}

/// Regenerates the local CA (encrypted by default or plaintext with `--no-encryption`),
/// removes any stale cached CA from the OS trust store, and installs the new CA.
int runCertInstall(TextWriter stdout, TextWriter stderr, ref Store store, CertInstallInput input) @trusted
{
    auto storageOptions = TLSCAKeyStorageOptions(TLSCAKeyStoragePolicy.encryptedRequired);
    if (input.noEncryption)
    {
        storageOptions.policy = TLSCAKeyStoragePolicy.plaintextRequired;
        writeOutln(stderr, "warning: writing Polka local HTTPS private keys without OS keyring encryption");
    }

    try
    {
        ParsedCertificate oldCACert;
        if (readExistingGlobalTLSCACertificate(store.cacheDir, oldCACert))
        {
            auto rmFn = removeCertificateFromTrustStoreFunc !is null
                ? removeCertificateFromTrustStoreFunc
                : toDelegate(&removeCertificateFromTrustStore);
            rmFn(oldCACert);
        }

        regenerateGlobalTLSCertificateWithOptions(store.cacheDir, storageOptions);
        auto certificatePath = globalTLSCACertificatePaths(store.cacheDir).certPath;

        auto instFn = installCertificateToTrustStoreFunc !is null
            ? installCertificateToTrustStoreFunc
            : toDelegate(&installCertificateToTrustStore);
        auto trustStore = instFn(certificatePath);

        writeOutf(stdout, "Installed Polka local HTTPS CA certificate into the %s trust store.\n", trustStore);
        writeOutf(stdout, "CA certificate: %s\n", certificatePath);
        return 0;
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }
}

/// Dispatches `cert-install` flag parsing within the command router.
int executeCertInstallCommand(ref CommandContext ctx, TextWriter stdout, TextWriter stderr, const(string)[] args) @trusted
{
    CertInstallInput input;
    foreach (arg; args)
    {
        if (arg == "--no-encryption")
            input.noEncryption = true;
        else if (arg == "--no-encryption=true")
            input.noEncryption = true;
        else if (arg == "--no-encryption=false")
            input.noEncryption = false;
        else if (arg.length > 0 && arg[0] == '-')
        {
            writeErrorf(stderr, "unknown flag: %s", arg);
            return 1;
        }
        else
        {
            writeOutln(stderr, "error: cert-install accepts no arguments");
            return 1;
        }
    }

    try
    {
        auto store = ctx.store();
        return runCertInstall(stdout, stderr, store, input);
    }
    catch (Exception e)
    {
        writeErrorf(stderr, "%s", e.msg);
        return 1;
    }
}

/// Loads an existing cached CA certificate when present and valid.
bool readExistingGlobalTLSCACertificate(string cacheDir, out ParsedCertificate cert) @trusted
{
    auto certificatePath = globalTLSCACertificatePaths(cacheDir).certPath;
    try
    {
        cert = readCertificateFile(certificatePath);
        return true;
    }
    catch (Exception)
    {
        // Missing or malformed legacy CA files cannot identify a trust-store entry
        // and must not block regeneration of fresh certificates.
        return false;
    }
}

/// Installs the CA certificate at `certificatePath` into the platform's user trust store.
string installCertificateToTrustStore(string certificatePath) @trusted
{
    auto absPath = absolutePath(certificatePath);
    auto os = currentOS();
    switch (os)
    {
        case "windows":
            runTrustStoreCommand(["certutil", "-user", "-addstore", "Root", absPath]);
            return "current user Root";
        case "darwin":
        {
            auto homeDir = environment.get("HOME", "");
            if (homeDir.strip().length == 0)
                throw new Exception("resolve user home directory: HOME not set");
            auto keychainPath = buildPath(homeDir, "Library", "Keychains", "login.keychain-db");
            runTrustStoreCommand(["security", "add-trusted-cert", "-r", "trustRoot", "-k", keychainPath, absPath]);
            return "login keychain";
        }
        default:
            throw new Exception(format(
                "automatic trust-store installation is not supported on %s; install %s manually",
                os, absPath
            ));
    }
}

/// Removes `certificate` (matched by SHA-1 thumbprint) from the platform user trust store.
string removeCertificateFromTrustStore(const ParsedCertificate certificate) @trusted
{
    auto thumbprint = certificateSHA1Thumbprint(certificate);
    if (thumbprint.length == 0)
        throw new Exception("remove certificate from trust store: missing certificate");

    auto os = currentOS();
    switch (os)
    {
        case "windows":
            runTrustStoreCommandAllowMissing(["certutil", "-user", "-delstore", "Root", thumbprint]);
            return "current user Root";
        case "darwin":
        {
            auto homeDir = environment.get("HOME", "");
            if (homeDir.strip().length == 0)
                throw new Exception("resolve user home directory: HOME not set");
            auto keychainPath = buildPath(homeDir, "Library", "Keychains", "login.keychain-db");
            runTrustStoreCommandAllowMissing(["security", "delete-certificate", "-Z", thumbprint, keychainPath]);
            return "login keychain";
        }
        default:
            return "";
    }
}

/// Returns the uppercase hex SHA-1 fingerprint formatted for certutil/security.
string certificateSHA1Thumbprint(const ParsedCertificate certificate) @safe pure
{
    return certificate.sha1Hex.toUpper();
}

private void runTrustStoreCommand(const(string)[] argv) @trusted
{
    auto res = execute(argv);
    if (res.status == 0)
        return;
    throw new Exception(formatTrustStoreCommandError(argv[0], res.status, res.output.strip()));
}

private void runTrustStoreCommandAllowMissing(const(string)[] argv) @trusted
{
    auto res = execute(argv);
    if (res.status == 0)
        return;
    auto msg = res.output.strip();
    if (trustStoreCertificateMissing(msg))
        return;
    throw new Exception(formatTrustStoreCommandError(argv[0], res.status, msg));
}

private string formatTrustStoreCommandError(string name, int exitStatus, string message) @safe pure
{
    if (message.length == 0)
        return format("run %s: exit status %d", name, exitStatus);
    return format("run %s: exit status %d (%s)", name, exitStatus, message);
}

private bool trustStoreCertificateMissing(string message) @safe pure
{
    auto normalized = message.toLower();
    foreach (fragment; [
        "0x80092004",
        "crypt_e_not_found",
        "cannot find object or property",
        "cannot find the requested object",
        "the specified item could not be found",
        "could not be found in the keychain",
        "unable to find certificate",
    ])
    {
        if (normalized.canFind(fragment))
            return true;
    }
    return false;
}

unittest
{
    assert(trustStoreCertificateMissing("CertUtil: -delstore command FAILED: 0x80092004 (-2146885628 CRYPT_E_NOT_FOUND)"));
    assert(trustStoreCertificateMissing("security: The specified item could not be found in the keychain."));
    assert(!trustStoreCertificateMissing("permission denied"));
}
