module polka.cli.tls_private_key;

// Storage, AES-256-GCM encryption, and OS-keyring integration for local HTTPS
// RSA private keys (CA and server). Keys are persisted as encrypted PEM blocks
// whose 32-byte AES keys live in the OS keyring, with optional plaintext fallback.

import std.algorithm : canFind;
import std.base64 : Base64;
import std.conv : to;
import std.file : mkdirRecurse, readText, write;
import std.format : format;
import std.json : JSONType, JSONValue, parseJSON;
import std.path : absolutePath, dirName;

import polka.backend : TextWriter, writeOutf;
import polka.cli.keyring : isKeyringNotFound, keyringGet, keyringSet;
import polka.cli.types : quoteGoString;
import polka.support.crypto :
    aes256GcmOpen,
    aes256GcmSeal,
    aesGcmNonceSize,
    pemDecode,
    pemEncode,
    randomBytes,
    sha256Hex,
    validateRSAPrivateKeyPKCS1DER;
import polka.support.fs : cleanPath, octal, setFileMode;

enum string tlsPrivateKeyringService = "polka";
enum string tlsPrivateKeyringAccountPrefix = "local-https-rsa-key:";
enum string tlsLegacyCAKeyringAccountPrefix = "local-https-ca:";
enum string tlsEncryptedPrivateKeyPEMType = "POLKA ENCRYPTED RSA PRIVATE KEY";
enum string tlsLegacyEncryptedCAKeyPEMType = "POLKA ENCRYPTED CA PRIVATE KEY";
enum int tlsPrivateKeyVersion = 1;
enum string tlsPrivateKeyAlgorithm = "AES-256-GCM";
enum size_t tlsPrivateKeyEncryptionKeySize = 32;
enum string tlsCAEncryptedKeyPEMType = tlsEncryptedPrivateKeyPEMType;

/// Selects how Polka stores generated TLS private keys on disk.
enum TLSCAKeyStoragePolicy
{
    flexible = 0,
    encryptedRequired = 1,
    plaintextRequired = 2,
}

/// Controls whether TLS private key writes require keyring-backed encryption,
/// require plaintext, or fall back to plaintext with a warning.
struct TLSCAKeyStorageOptions
{
    TLSCAKeyStoragePolicy policy = TLSCAKeyStoragePolicy.flexible;
    void delegate(string message) warnFn;

    void warnf(Args...)(string fmtStr, Args args) const @trusted
    {
        if (warnFn !is null)
            warnFn(format(fmtStr, args));
    }
}

/// Active warning sink installed during flexible HTTPS serving operations.
void delegate(string message) tlsCAKeyFallbackWarnf;

/// Installs a temporary warning sink for flexible TLS key generation, restoring
/// the prior sink when the returned closure is invoked.
void delegate() setTLSCAKeyFallbackWarningWriter(TextWriter stderr) @trusted
{
    auto oldWarnf = tlsCAKeyFallbackWarnf;
    if (stderr is null)
    {
        tlsCAKeyFallbackWarnf = null;
    }
    else
    {
        tlsCAKeyFallbackWarnf = (string msg) {
            writeOutf(stderr, "warning: %s", msg);
        };
    }

    return () { tlsCAKeyFallbackWarnf = oldWarnf; };
}

/// Returns default storage options using flexible policy and the active warning sink.
TLSCAKeyStorageOptions defaultTLSCAKeyStorageOptions() @trusted
{
    return TLSCAKeyStorageOptions(TLSCAKeyStoragePolicy.flexible, tlsCAKeyFallbackWarnf);
}

/// Writes a single PEM block with strict permissions to `path`.
void writePEMFile(string path, uint mode, string blockType, const(ubyte)[] data) @trusted
{
    auto pemText = pemEncode(blockType, data);
    write(path, pemText);
    setFileMode(path, mode);
}

/// Persists a PKCS#1 DER RSA private key to `path` according to `options.policy`.
void writeTLSPrivateKeyFile(string path, const(ubyte)[] keyDER, TLSCAKeyStorageOptions options) @trusted
{
    final switch (options.policy)
    {
        case TLSCAKeyStoragePolicy.plaintextRequired:
            writePEMFile(path, octal!600, "RSA PRIVATE KEY", keyDER);
            return;
        case TLSCAKeyStoragePolicy.encryptedRequired:
            writeEncryptedTLSPrivateKeyFile(path, keyDER);
            return;
        case TLSCAKeyStoragePolicy.flexible:
            try
            {
                writeEncryptedTLSPrivateKeyFile(path, keyDER);
            }
            catch (Exception e)
            {
                options.warnf("OS keyring is unavailable; writing plaintext Polka local HTTPS private key: %s\n", e.msg);
                writePEMFile(path, octal!600, "RSA PRIVATE KEY", keyDER);
            }
            return;
    }
}

/// Reads an encrypted Polka TLS key or legacy plaintext RSA private key, returning PKCS#1 DER bytes.
ubyte[] readTLSPrivateKeyFile(string path, TLSCAKeyStorageOptions options) @trusted
{
    auto text = readText(path);
    string blockType;
    ubyte[] blockBytes;
    if (!pemDecode(text, blockType, blockBytes))
        throw new Exception(format("missing PEM block in %s", path));

    switch (blockType)
    {
        case "RSA PRIVATE KEY":
            if (options.policy == TLSCAKeyStoragePolicy.encryptedRequired)
                throw new Exception(format("local HTTPS private key %s is plaintext; regenerate it with encrypted storage", path));
            validateRSAPrivateKeyPKCS1DER(blockBytes);
            return blockBytes;
        case tlsEncryptedPrivateKeyPEMType:
            return decryptTLSPrivateKey(path, blockBytes, &tlsKeyringAccount);
        case tlsLegacyEncryptedCAKeyPEMType:
            return decryptTLSPrivateKey(path, blockBytes, &tlsLegacyCAKeyringAccount);
        default:
            throw new Exception(format("missing RSA PRIVATE KEY or %s PEM block in %s", tlsEncryptedPrivateKeyPEMType, path));
    }
}

/// Alias preserving CA-key naming parity.
void writeTLSCAPrivateKeyFile(string path, const(ubyte)[] keyDER, TLSCAKeyStorageOptions options) @trusted
{
    writeTLSPrivateKeyFile(path, keyDER, options);
}

/// Alias preserving CA-key naming parity.
ubyte[] readTLSCAPrivateKeyFile(string path, TLSCAKeyStorageOptions options) @trusted
{
    return readTLSPrivateKeyFile(path, options);
}

/// Decodes an encrypted (or plaintext) TLS key from `sourcePath` and writes a
/// plaintext `RSA PRIVATE KEY` copy at `targetPath` for webservers needing PEM files.
void materializeTLSPrivateKeyFile(string sourcePath, string targetPath, TLSCAKeyStorageOptions options) @trusted
{
    auto keyDER = readTLSPrivateKeyFile(sourcePath, options);
    try
    {
        mkdirRecurse(dirName(targetPath));
    }
    catch (Exception e)
    {
        throw new Exception("create local HTTPS private key runtime directory: " ~ e.msg);
    }
    writePEMFile(targetPath, octal!600, "RSA PRIVATE KEY", keyDER);
}

/// Encrypts `keyDER` with AES-256-GCM using a key stored in the OS keyring.
private void writeEncryptedTLSPrivateKeyFile(string path, const(ubyte)[] keyDER) @trusted
{
    auto keyID = tlsKeyringAccount(path);
    ubyte[] encryptionKey;
    try
    {
        encryptionKey = ensureTLSPrivateKeyEncryptionKey(keyID);
    }
    catch (Exception e)
    {
        throw new Exception("store local HTTPS private key encryption key in OS keyring: " ~ e.msg);
    }

    auto nonce = randomBytes(aesGcmNonceSize);
    auto ciphertext = aes256GcmSeal(encryptionKey, nonce, keyDER, cast(const(ubyte)[]) keyID);

    JSONValue payload = JSONValue.emptyObject;
    payload["version"] = tlsPrivateKeyVersion;
    payload["algorithm"] = tlsPrivateKeyAlgorithm;
    payload["key_id"] = keyID;
    payload["nonce"] = Base64.encode(nonce);
    payload["ciphertext"] = Base64.encode(ciphertext);

    auto jsonBytes = cast(const(ubyte)[]) payload.toString();
    auto pemText = pemEncode(tlsEncryptedPrivateKeyPEMType, jsonBytes);
    write(path, pemText);
    setFileMode(path, octal!600);
}

/// Decrypts an AES-256-GCM Polka key JSON payload and validates the PKCS#1 DER key.
private ubyte[] decryptTLSPrivateKey(
    string path,
    const(ubyte)[] data,
    string function(string) @trusted accountFn
) @trusted
{
    JSONValue payload;
    try
    {
        payload = parseJSON(cast(const(char)[]) data);
    }
    catch (Exception e)
    {
        throw new Exception("decode encrypted local HTTPS private key metadata: " ~ e.msg);
    }

    if (payload.type != JSONType.object)
        throw new Exception("decode encrypted local HTTPS private key metadata: expected JSON object");

    long ver = 0;
    if (auto vp = "version" in payload)
    {
        if (vp.type == JSONType.integer)
            ver = vp.integer;
        else if (vp.type == JSONType.uinteger)
            ver = cast(long) vp.uinteger;
    }
    if (ver != tlsPrivateKeyVersion)
        throw new Exception(format("unsupported encrypted local HTTPS private key version %d", ver));

    string alg = "";
    if (auto ap = "algorithm" in payload)
    {
        if (ap.type == JSONType.string)
            alg = ap.str;
    }
    if (alg != tlsPrivateKeyAlgorithm)
        throw new Exception(format("unsupported encrypted local HTTPS private key algorithm %s", quoteGoString(alg)));

    string payloadKeyID = "";
    if (auto kp = "key_id" in payload)
    {
        if (kp.type == JSONType.string)
            payloadKeyID = kp.str;
    }
    auto expectedKeyID = accountFn(path);
    if (payloadKeyID != expectedKeyID)
        throw new Exception(format(
            "encrypted local HTTPS private key belongs to %s, want %s",
            quoteGoString(payloadKeyID),
            quoteGoString(expectedKeyID)
        ));

    string nonceB64 = "";
    if (auto np = "nonce" in payload)
    {
        if (np.type == JSONType.string)
            nonceB64 = np.str;
    }
    ubyte[] nonce;
    try
    {
        nonce = Base64.decode(nonceB64);
    }
    catch (Exception e)
    {
        throw new Exception("decode encrypted local HTTPS private key nonce: " ~ e.msg);
    }

    string cipherB64 = "";
    if (auto cp = "ciphertext" in payload)
    {
        if (cp.type == JSONType.string)
            cipherB64 = cp.str;
    }
    ubyte[] ciphertext;
    try
    {
        ciphertext = Base64.decode(cipherB64);
    }
    catch (Exception e)
    {
        throw new Exception("decode encrypted local HTTPS private key ciphertext: " ~ e.msg);
    }

    ubyte[] encryptionKey;
    try
    {
        encryptionKey = loadTLSPrivateKeyEncryptionKey(expectedKeyID);
    }
    catch (Exception e)
    {
        throw new Exception("read local HTTPS private key encryption key from OS keyring: " ~ e.msg);
    }

    ubyte[] plaintext;
    try
    {
        plaintext = aes256GcmOpen(encryptionKey, nonce, ciphertext, cast(const(ubyte)[]) expectedKeyID);
    }
    catch (Exception e)
    {
        throw new Exception("decrypt local HTTPS private key: " ~ e.msg);
    }

    validateRSAPrivateKeyPKCS1DER(plaintext);
    return plaintext;
}

private ubyte[] ensureTLSPrivateKeyEncryptionKey(string keyID) @trusted
{
    try
    {
        return loadTLSPrivateKeyEncryptionKey(keyID);
    }
    catch (Exception e)
    {
        if (!isKeyringNotFound(e))
            throw e;
    }

    auto key = randomBytes(tlsPrivateKeyEncryptionKeySize);
    auto encodedKey = Base64.encode(key);
    keyringSet(tlsPrivateKeyringService, keyID, encodedKey.idup);
    return key;
}

private ubyte[] loadTLSPrivateKeyEncryptionKey(string keyID) @trusted
{
    auto encodedKey = keyringGet(tlsPrivateKeyringService, keyID);
    ubyte[] key;
    try
    {
        key = Base64.decode(encodedKey);
    }
    catch (Exception e)
    {
        throw new Exception("decode local HTTPS private key encryption key: " ~ e.msg);
    }
    if (key.length != tlsPrivateKeyEncryptionKeySize)
        throw new Exception(format(
            "local HTTPS private key encryption key has %d bytes, want %d",
            key.length, tlsPrivateKeyEncryptionKeySize
        ));
    return key;
}

/// Derives a deterministic keyring account ID from the normalized absolute key file path.
string tlsKeyringAccount(string path) @trusted
{
    auto abs = cleanPath(absolutePath(path));
    return tlsPrivateKeyringAccountPrefix ~ sha256Hex(cast(const(ubyte)[]) abs);
}

/// Derives the legacy CA keyring account ID from the key file's parent directory.
string tlsLegacyCAKeyringAccount(string path) @trusted
{
    auto absDir = cleanPath(absolutePath(dirName(path)));
    return tlsLegacyCAKeyringAccountPrefix ~ sha256Hex(cast(const(ubyte)[]) absDir);
}

unittest
{
    import std.file : readText;
    import std.path : buildPath;
    import std.string : strip;
    import polka.cli.keyring : keyringMockInit, keyringMockInitWithError;
    import polka.support.crypto : generateRSAPrivateKeyPKCS1DER;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    // Encrypts and decrypts private keys when OS keyring works.
    {
        keyringMockInit();
        scope (exit) keyringMockInit();

        auto tmp = createTempDir("polka-tlskey-enc-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto key = generateRSAPrivateKeyPKCS1DER();
        auto path = buildPath(tmp, "polka-local-ca.key");

        writeTLSCAPrivateKeyFile(path, key, TLSCAKeyStorageOptions(TLSCAKeyStoragePolicy.encryptedRequired));
        auto data = readText(path);
        assert(!data.canFind("-----BEGIN RSA PRIVATE KEY-----"));
        assert(data.canFind(tlsCAEncryptedKeyPEMType));

        auto got = readTLSCAPrivateKeyFile(path, TLSCAKeyStorageOptions(TLSCAKeyStoragePolicy.flexible));
        assert(got == key);
    }

    // Reads legacy plaintext RSA private keys in flexible mode.
    {
        auto tmp = createTempDir("polka-tlskey-plain-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto key = generateRSAPrivateKeyPKCS1DER();
        auto path = buildPath(tmp, "polka-local-ca.key");

        writePEMFile(path, octal!600, "RSA PRIVATE KEY", key);
        auto got = readTLSCAPrivateKeyFile(path, TLSCAKeyStorageOptions(TLSCAKeyStoragePolicy.flexible));
        assert(got == key);
    }

    // Fails in encryptedRequired mode when the OS keyring errors.
    {
        keyringMockInitWithError(new Exception("no keyring"));
        scope (exit) keyringMockInit();

        auto tmp = createTempDir("polka-tlskey-fail-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto key = generateRSAPrivateKeyPKCS1DER();
        auto path = buildPath(tmp, "polka-local-ca.key");

        bool threw = false;
        try
        {
            writeTLSCAPrivateKeyFile(path, key, TLSCAKeyStorageOptions(TLSCAKeyStoragePolicy.encryptedRequired));
        }
        catch (Exception e)
        {
            threw = e.msg.canFind("no keyring");
        }
        assert(threw);
    }

    // Materializes plaintext runtime key from encrypted storage.
    {
        keyringMockInit();
        scope (exit) keyringMockInit();

        auto tmp = createTempDir("polka-tlskey-mat-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto key = generateRSAPrivateKeyPKCS1DER();
        auto srcPath = buildPath(tmp, "cache", "polka-local.key");
        auto dstPath = buildPath(tmp, "run", "polka-local.key");
        mkdirRecurse(dirName(srcPath));
        writeTLSPrivateKeyFile(srcPath, key, TLSCAKeyStorageOptions(TLSCAKeyStoragePolicy.encryptedRequired));

        materializeTLSPrivateKeyFile(srcPath, dstPath, TLSCAKeyStorageOptions(TLSCAKeyStoragePolicy.flexible));
        auto runtimeText = readText(dstPath);
        assert(runtimeText.canFind("-----BEGIN RSA PRIVATE KEY-----"));
    }

    // PlaintextRequired ignores OS keyring errors completely.
    {
        keyringMockInitWithError(new Exception("no keyring"));
        scope (exit) keyringMockInit();

        auto tmp = createTempDir("polka-tlskey-pReq-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto key = generateRSAPrivateKeyPKCS1DER();
        auto path = buildPath(tmp, "polka-local-ca.key");

        writeTLSCAPrivateKeyFile(path, key, TLSCAKeyStorageOptions(TLSCAKeyStoragePolicy.plaintextRequired));
        assert(readText(path).canFind("-----BEGIN RSA PRIVATE KEY-----"));
    }

    // Flexible mode falls back to plaintext and emits warning when keyring fails.
    {
        keyringMockInitWithError(new Exception("no keyring"));
        scope (exit) keyringMockInit();

        auto tmp = createTempDir("polka-tlskey-flex-");
        scope (exit) rmdirRecurseQuiet(tmp);
        auto key = generateRSAPrivateKeyPKCS1DER();
        auto path = buildPath(tmp, "polka-local-ca.key");
        string[] warnings;

        writeTLSCAPrivateKeyFile(path, key, TLSCAKeyStorageOptions(
            TLSCAKeyStoragePolicy.flexible,
            (string msg) { warnings ~= msg.strip(); }
        ));
        assert(readText(path).canFind("-----BEGIN RSA PRIVATE KEY-----"));
        assert(warnings.length == 1 && warnings[0].canFind("OS keyring is unavailable"));
    }
}
