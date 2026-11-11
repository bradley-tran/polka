module polka.support.crypto;

import core.stdc.config : c_long, c_ulong;
import core.stdc.stdint;
import core.stdc.string : memcpy, strlen;
import core.stdc.time : time_t;
import std.algorithm : any, canFind, startsWith;
import std.array : appender;
import std.base64 : Base64;
import std.conv : to;
import std.datetime : Clock, SysTime;
import std.digest.md : md5Of, toHexString, LetterCase;
import std.digest.sha : sha1Of, sha256Of;
import std.format : format;
import std.string : indexOf, join, splitLines, strip, toLower, toStringz;

// ============================================================================
// OpenSSL 3 libcrypto C bindings
// ============================================================================
private extern (C) nothrow @nogc
{
    struct EVP_MD;
    struct EVP_MD_CTX;
    struct EVP_CIPHER;
    struct EVP_CIPHER_CTX;
    struct EVP_PKEY;
    struct EVP_PKEY_CTX;
    struct BIO;
    struct BIO_METHOD;
    struct X509;
    struct X509_NAME;
    struct X509_EXTENSION;
    struct X509_REQ;
    struct X509_CRL;
    struct X509_STORE;
    struct X509_STORE_CTX;
    struct ASN1_INTEGER;
    struct ASN1_TIME;
    struct ASN1_STRING;
    struct BIGNUM;
    struct OPENSSL_STACK;
    struct CONF;

    struct X509V3_CTX
    {
        int flags;
        X509* issuer_cert;
        X509* subject_cert;
        X509_REQ* subject_req;
        X509_CRL* crl;
        void* db_meth;
        void* db;
        EVP_PKEY* issuer_pkey;
    }

    struct GENERAL_NAME
    {
        int type;
        void* d;
    }

    enum int NID_rsaEncryption = 6;
    enum int EVP_PKEY_RSA = NID_rsaEncryption;
    enum int MBSTRING_ASC = 0x1001;
    enum int EVP_CTRL_AEAD_SET_IVLEN = 0x9;
    enum int EVP_CTRL_AEAD_GET_TAG = 0x10;
    enum int EVP_CTRL_AEAD_SET_TAG = 0x11;
    enum int BIO_CTRL_INFO = 3;

    enum int NID_subject_key_identifier = 82;
    enum int NID_key_usage = 83;
    enum int NID_subject_alt_name = 85;
    enum int NID_basic_constraints = 87;
    enum int NID_authority_key_identifier = 90;
    enum int NID_ext_key_usage = 126;

    enum int GEN_DNS = 2;
    enum int GEN_IPADD = 7;

    // Random & Digest
    int RAND_bytes(ubyte* buf, int num);
    const(EVP_MD)* EVP_md5();
    const(EVP_MD)* EVP_sha1();
    const(EVP_MD)* EVP_sha256();
    const(EVP_MD)* EVP_sha3_256();
    EVP_MD_CTX* EVP_MD_CTX_new();
    void EVP_MD_CTX_free(EVP_MD_CTX* ctx);
    int EVP_DigestInit_ex(EVP_MD_CTX* ctx, const(EVP_MD)* type, void* impl);
    int EVP_DigestUpdate(EVP_MD_CTX* ctx, const(void)* d, size_t cnt);
    int EVP_DigestFinal_ex(EVP_MD_CTX* ctx, ubyte* md, uint* s);
    int EVP_Digest(const(void)* data, size_t count, ubyte* md, uint* size, const(EVP_MD)* type, void* impl);

    // Cipher (AES-256-GCM)
    const(EVP_CIPHER)* EVP_aes_256_gcm();
    EVP_CIPHER_CTX* EVP_CIPHER_CTX_new();
    void EVP_CIPHER_CTX_free(EVP_CIPHER_CTX* c);
    int EVP_CIPHER_CTX_ctrl(EVP_CIPHER_CTX* ctx, int type, int arg, void* ptr);
    int EVP_EncryptInit_ex(EVP_CIPHER_CTX* ctx, const(EVP_CIPHER)* cipher, void* impl, const(ubyte)* key, const(ubyte)* iv);
    int EVP_EncryptUpdate(EVP_CIPHER_CTX* ctx, ubyte* out_, int* outl, const(ubyte)* in_, int inl);
    int EVP_EncryptFinal_ex(EVP_CIPHER_CTX* ctx, ubyte* out_, int* outl);
    int EVP_DecryptInit_ex(EVP_CIPHER_CTX* ctx, const(EVP_CIPHER)* cipher, void* impl, const(ubyte)* key, const(ubyte)* iv);
    int EVP_DecryptUpdate(EVP_CIPHER_CTX* ctx, ubyte* out_, int* outl, const(ubyte)* in_, int inl);
    int EVP_DecryptFinal_ex(EVP_CIPHER_CTX* ctx, ubyte* outm, int* outl);

    // Keys
    EVP_PKEY* EVP_PKEY_Q_keygen(void* libctx, const(char)* propq, const(char)* type, ...);
    void EVP_PKEY_free(EVP_PKEY* pkey);
    int i2d_PrivateKey(const(EVP_PKEY)* a, ubyte** pp);
    EVP_PKEY* d2i_PrivateKey(int type, EVP_PKEY** a, const(ubyte)** pp, c_long length);

    // BIO & PEM
    const(BIO_METHOD)* BIO_s_mem();
    BIO* BIO_new(const(BIO_METHOD)* type);
    BIO* BIO_new_mem_buf(const(void)* buf, int len);
    void BIO_free(BIO* a);
    c_long BIO_ctrl(BIO* bp, int cmd, c_long larg, void* parg);
    int PEM_write_bio_X509(BIO* bp, const(X509)* x);
    X509* PEM_read_bio_X509(BIO* bp, X509** x, void* cb, void* u);
    EVP_PKEY* PEM_read_bio_PrivateKey(BIO* bp, EVP_PKEY** x, void* cb, void* u);

    // BIGNUM & ASN1
    BIGNUM* BN_bin2bn(const(ubyte)* s, int len, BIGNUM* ret);
    void BN_free(BIGNUM* a);
    char* BN_bn2hex(const(BIGNUM)* a);
    ASN1_INTEGER* BN_to_ASN1_INTEGER(const(BIGNUM)* bn, ASN1_INTEGER* ai);
    BIGNUM* ASN1_INTEGER_to_BN(const(ASN1_INTEGER)* ai, BIGNUM* bn);
    void ASN1_INTEGER_free(ASN1_INTEGER* a);
    ASN1_TIME* ASN1_TIME_set(ASN1_TIME* s, time_t t);
    void ASN1_TIME_free(ASN1_TIME* a);
    int ASN1_TIME_diff(int* pday, int* psec, const(ASN1_TIME)* from, const(ASN1_TIME)* to);
    const(ubyte)* ASN1_STRING_get0_data(const(ASN1_STRING)* x);
    int ASN1_STRING_length(const(ASN1_STRING)* x);

    // X509
    X509* X509_new();
    void X509_free(X509* a);
    int X509_set_version(X509* x, c_long version_);
    const(ASN1_INTEGER)* X509_get0_serialNumber(const(X509)* x);
    int X509_set_serialNumber(X509* x, ASN1_INTEGER* serial);
    X509_NAME* X509_get_subject_name(const(X509)* a);
    int X509_set_issuer_name(X509* x, const(X509_NAME)* name);
    int X509_NAME_add_entry_by_txt(X509_NAME* name, const(char)* field, int type, const(ubyte)* bytes, int len, int loc, int set);
    int X509_set1_notBefore(X509* x, const(ASN1_TIME)* tm);
    int X509_set1_notAfter(X509* x, const(ASN1_TIME)* tm);
    const(ASN1_TIME)* X509_get0_notAfter(const(X509)* x);
    int X509_set_pubkey(X509* x, EVP_PKEY* pkey);
    int X509_sign(X509* x, EVP_PKEY* pkey, const(EVP_MD)* md);
    int X509_add_ext(X509* x, X509_EXTENSION* ex, int loc);
    void X509_EXTENSION_free(X509_EXTENSION* ex);
    int X509_digest(const(X509)* data, const(EVP_MD)* type, ubyte* md, uint* len);
    int X509_check_ca(X509* x);
    int X509_check_host(X509* x, const(char)* chk, size_t chklen, uint flags, char** peername);
    int X509_check_ip_asc(X509* x, const(char)* ipasc, uint flags);
    void* X509_get_ext_d2i(const(X509)* x, int nid, int* crit, int* idx);
    X509_STORE* X509_STORE_new();
    void X509_STORE_free(X509_STORE* v);
    int X509_STORE_add_cert(X509_STORE* ctx, X509* x);
    X509_STORE_CTX* X509_STORE_CTX_new();
    void X509_STORE_CTX_free(X509_STORE_CTX* ctx);
    int X509_STORE_CTX_init(X509_STORE_CTX* ctx, X509_STORE* store, X509* x509, void* chain);
    int X509_verify_cert(X509_STORE_CTX* ctx);

    void X509V3_set_ctx(X509V3_CTX* ctx, X509* issuer, X509* subject, X509_REQ* req, X509_CRL* crl, int flags);
    X509_EXTENSION* X509V3_EXT_conf_nid(CONF* conf, X509V3_CTX* ctx, int ext_nid, const(char)* value);

    int OPENSSL_sk_num(const(OPENSSL_STACK)* st);
    void* OPENSSL_sk_value(const(OPENSSL_STACK)* st, int i);
    alias ExternCFreeFunc = extern (C) void function(void*) nothrow @nogc;
    void OPENSSL_sk_pop_free(OPENSSL_STACK* st, ExternCFreeFunc func);
    void GENERAL_NAME_free(GENERAL_NAME* a);
    void CRYPTO_free(void* str, const(char)* file, int line);
}

// ============================================================================
// Hashing & Random
// ============================================================================

string md5Hex(const(ubyte)[] data) @safe pure
{
    auto digest = md5Of(data);
    return toHexString!(LetterCase.lower)(digest).idup;
}

string sha1Hex(const(ubyte)[] data) @safe pure
{
    auto digest = sha1Of(data);
    return toHexString!(LetterCase.lower)(digest).idup;
}

string sha256Hex(const(ubyte)[] data) @safe pure
{
    auto digest = sha256Of(data);
    return toHexString!(LetterCase.lower)(digest).idup;
}

ubyte[32] sha256Bytes(const(ubyte)[] data) @safe pure
{
    return sha256Of(data);
}

string sha3_256Hex(const(ubyte)[] data) @trusted
{
    ubyte[32] digest;
    uint outLen = 0;
    auto ptr = data.length > 0 ? data.ptr : cast(const(ubyte)*) "".ptr;
    if (EVP_Digest(ptr, data.length, digest.ptr, &outLen, EVP_sha3_256(), null) != 1 || outLen != 32)
        throw new Exception("failed to compute SHA3-256 digest");
    return toHexString!(LetterCase.lower)(digest).idup;
}

/// Streams `filePath` and returns lowercase hex digest for `"md5"`, `"sha256"`, or `"sha3-256"`.
string fileDigestHex(string algName, string filePath, out long fileSize) @trusted
{
    import std.stdio : File;

    const(EVP_MD)* md;
    switch (algName.toLower().strip())
    {
        case "md5":
            md = EVP_md5();
            break;
        case "sha256":
            md = EVP_sha256();
            break;
        case "sha3-256":
            md = EVP_sha3_256();
            break;
        default:
            throw new Exception("unsupported checksum algorithm \"" ~ algName ~ "\"");
    }

    auto ctx = EVP_MD_CTX_new();
    if (ctx is null)
        throw new Exception("EVP_MD_CTX_new failed");
    scope (exit) EVP_MD_CTX_free(ctx);

    if (EVP_DigestInit_ex(ctx, md, null) != 1)
        throw new Exception("EVP_DigestInit_ex failed");

    auto f = File(filePath, "rb");
    long total = 0;
    foreach (chunk; f.byChunk(65536))
    {
        if (chunk.length > 0)
        {
            if (EVP_DigestUpdate(ctx, chunk.ptr, chunk.length) != 1)
                throw new Exception("EVP_DigestUpdate failed");
            total += cast(long) chunk.length;
        }
    }
    fileSize = total;

    ubyte[64] digest;
    uint outLen = 0;
    if (EVP_DigestFinal_ex(ctx, digest.ptr, &outLen) != 1)
        throw new Exception("EVP_DigestFinal_ex failed");
    return toHexString!(LetterCase.lower)(digest[0 .. outLen]).idup;
}

ubyte[] randomBytes(size_t count) @trusted
{
    if (count == 0)
        return null;
    ubyte[] buf = new ubyte[count];
    if (RAND_bytes(buf.ptr, cast(int) count) != 1)
        throw new Exception("OpenSSL RAND_bytes failed");
    return buf;
}

string randomCryptoHex(size_t numBytes) @trusted
{
    static immutable char[] hex = "0123456789abcdef";
    auto raw = randomBytes(numBytes);
    char[] outBuf = new char[raw.length * 2];
    foreach (i, b; raw)
    {
        outBuf[i * 2] = hex[b >> 4];
        outBuf[i * 2 + 1] = hex[b & 0x0f];
    }
    return outBuf.idup;
}

// ============================================================================
// AES-256-GCM Seal / Open (Go crypto/cipher compatible)
// ============================================================================

enum size_t aesGcmNonceSize = 12;
enum size_t aesGcmTagSize = 16;

ubyte[] aes256GcmSeal(const(ubyte)[] key32, const(ubyte)[] nonce, const(ubyte)[] plaintext, const(ubyte)[] aad = null) @trusted
{
    if (key32.length != 32)
        throw new Exception("AES-256-GCM requires a 32-byte key");
    if (nonce.length == 0)
        throw new Exception("AES-256-GCM requires a non-empty nonce");

    auto ctx = EVP_CIPHER_CTX_new();
    if (ctx is null)
        throw new Exception("EVP_CIPHER_CTX_new failed");
    scope (exit) EVP_CIPHER_CTX_free(ctx);

    if (EVP_EncryptInit_ex(ctx, EVP_aes_256_gcm(), null, null, null) != 1)
        throw new Exception("EVP_EncryptInit_ex failed");
    if (EVP_CIPHER_CTX_ctrl(ctx, EVP_CTRL_AEAD_SET_IVLEN, cast(int) nonce.length, null) != 1)
        throw new Exception("EVP_CTRL_AEAD_SET_IVLEN failed");
    if (EVP_EncryptInit_ex(ctx, null, null, key32.ptr, nonce.ptr) != 1)
        throw new Exception("EVP_EncryptInit_ex key/iv failed");

    int len = 0;
    if (aad.length > 0)
    {
        if (EVP_EncryptUpdate(ctx, null, &len, aad.ptr, cast(int) aad.length) != 1)
            throw new Exception("EVP_EncryptUpdate AAD failed");
    }

    ubyte[] outBuf = new ubyte[plaintext.length + aesGcmTagSize];
    int ciphertextLen = 0;
    if (plaintext.length > 0)
    {
        if (EVP_EncryptUpdate(ctx, outBuf.ptr, &len, plaintext.ptr, cast(int) plaintext.length) != 1)
            throw new Exception("EVP_EncryptUpdate failed");
        ciphertextLen = len;
    }

    if (EVP_EncryptFinal_ex(ctx, outBuf.ptr + ciphertextLen, &len) != 1)
        throw new Exception("EVP_EncryptFinal_ex failed");
    ciphertextLen += len;

    if (EVP_CIPHER_CTX_ctrl(ctx, EVP_CTRL_AEAD_GET_TAG, aesGcmTagSize, outBuf.ptr + ciphertextLen) != 1)
        throw new Exception("EVP_CTRL_AEAD_GET_TAG failed");

    return outBuf[0 .. ciphertextLen + aesGcmTagSize];
}

ubyte[] aes256GcmOpen(const(ubyte)[] key32, const(ubyte)[] nonce, const(ubyte)[] ciphertextWithTag, const(ubyte)[] aad = null) @trusted
{
    if (key32.length != 32)
        throw new Exception("AES-256-GCM requires a 32-byte key");
    if (nonce.length == 0)
        throw new Exception("AES-256-GCM requires a non-empty nonce");
    if (ciphertextWithTag.length < aesGcmTagSize)
        throw new Exception("ciphertext shorter than GCM tag");

    auto cipherLen = ciphertextWithTag.length - aesGcmTagSize;
    auto cipherPtr = cipherLen > 0 ? ciphertextWithTag.ptr : null;
    auto tagCopy = ciphertextWithTag[cipherLen .. $].dup;

    auto ctx = EVP_CIPHER_CTX_new();
    if (ctx is null)
        throw new Exception("EVP_CIPHER_CTX_new failed");
    scope (exit) EVP_CIPHER_CTX_free(ctx);

    if (EVP_DecryptInit_ex(ctx, EVP_aes_256_gcm(), null, null, null) != 1)
        throw new Exception("EVP_DecryptInit_ex failed");
    if (EVP_CIPHER_CTX_ctrl(ctx, EVP_CTRL_AEAD_SET_IVLEN, cast(int) nonce.length, null) != 1)
        throw new Exception("EVP_CTRL_AEAD_SET_IVLEN failed");
    if (EVP_DecryptInit_ex(ctx, null, null, key32.ptr, nonce.ptr) != 1)
        throw new Exception("EVP_DecryptInit_ex key/iv failed");

    int len = 0;
    if (aad.length > 0)
    {
        if (EVP_DecryptUpdate(ctx, null, &len, aad.ptr, cast(int) aad.length) != 1)
            throw new Exception("EVP_DecryptUpdate AAD failed");
    }

    ubyte[] plain = new ubyte[cipherLen];
    int plainLen = 0;
    if (cipherLen > 0)
    {
        if (EVP_DecryptUpdate(ctx, plain.ptr, &len, cipherPtr, cast(int) cipherLen) != 1)
            throw new Exception("EVP_DecryptUpdate failed");
        plainLen = len;
    }

    if (EVP_CIPHER_CTX_ctrl(ctx, EVP_CTRL_AEAD_SET_TAG, aesGcmTagSize, tagCopy.ptr) != 1)
        throw new Exception("EVP_CTRL_AEAD_SET_TAG failed");

    if (EVP_DecryptFinal_ex(ctx, plain.ptr + plainLen, &len) <= 0)
        throw new Exception("cipher: message authentication failed");
    plainLen += len;

    return plain[0 .. plainLen];
}

// ============================================================================
// PEM Utilities
// ============================================================================

string pemEncode(string blockType, const(ubyte)[] bytes) @safe
{
    auto b64 = Base64.encode(bytes);
    auto buf = appender!string();
    buf.put("-----BEGIN ");
    buf.put(blockType);
    buf.put("-----\n");
    for (size_t i = 0; i < b64.length; i += 64)
    {
        size_t end = i + 64 < b64.length ? i + 64 : b64.length;
        buf.put(b64[i .. end]);
        buf.put('\n');
    }
    buf.put("-----END ");
    buf.put(blockType);
    buf.put("-----\n");
    return buf.data;
}

bool pemDecode(string pemText, out string blockType, out ubyte[] derBytes) @safe
{
    auto lines = pemText.splitLines();
    bool inBlock = false;
    auto b64 = appender!string();
    foreach (rawLine; lines)
    {
        auto line = rawLine.strip();
        if (!inBlock)
        {
            if (line.startsWith("-----BEGIN ") && line.length > 16 && line[$-5 .. $] == "-----")
            {
                blockType = line[11 .. $ - 5];
                inBlock = true;
            }
        }
        else
        {
            if (line == "-----END " ~ blockType ~ "-----")
            {
                try
                {
                    derBytes = Base64.decode(b64.data);
                    return true;
                }
                catch (Exception)
                    return false;
            }
            if (line.length > 0)
                b64.put(line);
        }
    }
    return false;
}

// ============================================================================
// RSA Keys & X.509 Certificates
// ============================================================================

struct KeyPairPEM
{
    string certPEM;
    ubyte[] keyPKCS1DER;
}

struct ParsedCertificate
{
    string sha1Hex;
    bool isCA;
    bool expired;
    string[] dnsNames;
    string[] ipAddresses;
    string pem;

    bool verifyHostname(string host) const @trusted
    {
        auto x = parseX509Handle(pem);
        if (x is null)
            return false;
        scope (exit) X509_free(x);
        if (isIPAddressString(host))
            return X509_check_ip_asc(x, host.toStringz, 0) == 1;
        return X509_check_host(x, host.ptr, host.length, 0, null) == 1;
    }

    bool hasExactHost(string host) const @trusted
    {
        if (isIPAddressString(host))
        {
            import std.socket : parseAddress;
            string targetCanon = host;
            try
            {
                targetCanon = parseAddress(host).toAddrString();
            }
            catch (Exception) {}

            foreach (ip; ipAddresses)
            {
                string ipCanon = ip;
                try
                {
                    ipCanon = parseAddress(ip).toAddrString();
                }
                catch (Exception) {}
                if (ipCanon == targetCanon)
                    return true;
            }
            return false;
        }
        auto low = host.toLower();
        foreach (dns; dnsNames)
        {
            if (dns.toLower() == low)
                return true;
        }
        return false;
    }
}

private bool isIPAddressString(string s) @safe
{
    import std.socket : parseAddress, SocketException;
    if (s.length == 0)
        return false;
    try
    {
        parseAddress(s);
        return true;
    }
    catch (SocketException)
    {
        return false;
    }
    catch (Exception)
    {
        return false;
    }
}

private string formatIPBytes(const(ubyte)[] bytes) @safe
{
    if (bytes.length == 4)
        return format("%d.%d.%d.%d", bytes[0], bytes[1], bytes[2], bytes[3]);
    if (bytes.length == 16)
    {
        // Canonical IPv6 check for ::1 and general v6
        bool allZeroPrefix = true;
        foreach (i; 0 .. 15)
        {
            if (bytes[i] != 0)
            {
                allZeroPrefix = false;
                break;
            }
        }
        if (allZeroPrefix && bytes[15] == 1)
            return "::1";
        ushort[8] groups;
        foreach (i; 0 .. 8)
            groups[i] = cast(ushort)((bytes[i * 2] << 8) | bytes[i * 2 + 1]);
        return format("%x:%x:%x:%x:%x:%x:%x:%x",
            groups[0], groups[1], groups[2], groups[3],
            groups[4], groups[5], groups[6], groups[7]);
    }
    return "";
}

private X509* parseX509Handle(string certPEM) @trusted
{
    auto bio = BIO_new_mem_buf(certPEM.ptr, cast(int) certPEM.length);
    if (bio is null)
        return null;
    scope (exit) BIO_free(bio);
    return PEM_read_bio_X509(bio, null, null, null);
}

private string serializeX509ToPEM(X509* x) @trusted
{
    auto bio = BIO_new(BIO_s_mem());
    if (bio is null)
        throw new Exception("BIO_new failed");
    scope (exit) BIO_free(bio);
    if (PEM_write_bio_X509(bio, x) != 1)
        throw new Exception("PEM_write_bio_X509 failed");
    char* dataPtr = null;
    auto dataLen = BIO_ctrl(bio, BIO_CTRL_INFO, 0, &dataPtr);
    if (dataLen <= 0 || dataPtr is null)
        throw new Exception("BIO_get_mem_data failed");
    return dataPtr[0 .. cast(size_t) dataLen].idup;
}

private ubyte[] pkeyToPKCS1DER(EVP_PKEY* pkey) @trusted
{
    int len = i2d_PrivateKey(pkey, null);
    if (len <= 0)
        throw new Exception("i2d_PrivateKey length failed");
    ubyte[] der = new ubyte[len];
    ubyte* p = der.ptr;
    if (i2d_PrivateKey(pkey, &p) != len)
        throw new Exception("i2d_PrivateKey failed");
    return der;
}

private EVP_PKEY* pkeyFromPKCS1DER(const(ubyte)[] der) @trusted
{
    const(ubyte)* p = der.ptr;
    auto pkey = d2i_PrivateKey(EVP_PKEY_RSA, null, &p, cast(c_long) der.length);
    if (pkey is null)
        throw new Exception("d2i_PrivateKey failed");
    return pkey;
}

ubyte[] parseRSAPrivateKeyPEM(string pemText) @trusted
{
    string blockType;
    ubyte[] der;
    if (!pemDecode(pemText, blockType, der))
        throw new Exception("decode RSA private key PEM");
    if (blockType == "RSA PRIVATE KEY")
    {
        auto pkey = pkeyFromPKCS1DER(der);
        EVP_PKEY_free(pkey);
        return der;
    }
    auto bio = BIO_new_mem_buf(pemText.ptr, cast(int) pemText.length);
    if (bio is null)
        throw new Exception("BIO_new_mem_buf failed");
    scope (exit) BIO_free(bio);
    auto pkey = PEM_read_bio_PrivateKey(bio, null, null, null);
    if (pkey is null)
        throw new Exception("parse RSA private key failed");
    scope (exit) EVP_PKEY_free(pkey);
    return pkeyToPKCS1DER(pkey);
}

ParsedCertificate parseX509CertificatePEM(string certPEM, SysTime now = Clock.currTime()) @trusted
{
    auto x = parseX509Handle(certPEM);
    if (x is null)
        throw new Exception("failed to parse X509 certificate PEM");
    scope (exit) X509_free(x);

    ParsedCertificate info;
    info.pem = certPEM;

    // SHA-1 thumbprint
    ubyte[20] sha1;
    uint sha1Len = 0;
    if (X509_digest(x, EVP_sha1(), sha1.ptr, &sha1Len) == 1 && sha1Len == 20)
        info.sha1Hex = toHexString!(LetterCase.lower)(sha1).idup;

    info.isCA = X509_check_ca(x) > 0;

    // Check expiration
    auto notAfter = X509_get0_notAfter(x);
    if (notAfter !is null)
    {
        auto nowTime = ASN1_TIME_set(null, cast(time_t) now.toUnixTime());
        if (nowTime !is null)
        {
            scope (exit) ASN1_TIME_free(nowTime);
            int days = 0, secs = 0;
            if (ASN1_TIME_diff(&days, &secs, nowTime, notAfter) == 1)
                info.expired = (days < 0 || secs < 0);
        }
    }

    // Extract SAN DNS names & IP addresses
    auto sanStack = cast(OPENSSL_STACK*) X509_get_ext_d2i(x, NID_subject_alt_name, null, null);
    if (sanStack !is null)
    {
        scope (exit) OPENSSL_sk_pop_free(sanStack, cast(ExternCFreeFunc) &GENERAL_NAME_free);
        int count = OPENSSL_sk_num(sanStack);
        foreach (i; 0 .. count)
        {
            auto gen = cast(GENERAL_NAME*) OPENSSL_sk_value(sanStack, i);
            if (gen is null)
                continue;
            if (gen.type == GEN_DNS)
            {
                auto str = cast(const(ASN1_STRING)*) gen.d;
                auto ptr = ASN1_STRING_get0_data(str);
                auto slen = ASN1_STRING_length(str);
                if (ptr !is null && slen > 0)
                    info.dnsNames ~= (cast(const(char)*) ptr)[0 .. slen].idup;
            }
            else if (gen.type == GEN_IPADD)
            {
                auto str = cast(const(ASN1_STRING)*) gen.d;
                auto ptr = ASN1_STRING_get0_data(str);
                auto slen = ASN1_STRING_length(str);
                if (ptr !is null && slen > 0)
                {
                    auto ipStr = formatIPBytes(ptr[0 .. slen]);
                    if (ipStr.length > 0)
                        info.ipAddresses ~= ipStr;
                }
            }
        }
    }

    return info;
}

private void setRandomSerial(X509* cert) @trusted
{
    auto serialBytes = randomBytes(16);
    serialBytes[0] &= 0x7f; // positive 127-bit integer
    if (serialBytes[0] == 0)
        serialBytes[0] = 1;
    auto bn = BN_bin2bn(serialBytes.ptr, cast(int) serialBytes.length, null);
    if (bn is null)
        throw new Exception("BN_bin2bn failed");
    scope (exit) BN_free(bn);
    auto ai = BN_to_ASN1_INTEGER(bn, null);
    if (ai is null)
        throw new Exception("BN_to_ASN1_INTEGER failed");
    scope (exit) ASN1_INTEGER_free(ai);
    if (X509_set_serialNumber(cert, ai) != 1)
        throw new Exception("X509_set_serialNumber failed");
}

private void addX509Ext(X509* cert, X509* issuer, int nid, string val) @trusted
{
    X509V3_CTX ctx;
    X509V3_set_ctx(&ctx, issuer, cert, null, null, 0);
    auto ext = X509V3_EXT_conf_nid(null, &ctx, nid, val.toStringz);
    if (ext is null)
        throw new Exception("X509V3_EXT_conf_nid failed for " ~ val);
    scope (exit) X509_EXTENSION_free(ext);
    if (X509_add_ext(cert, ext, -1) != 1)
        throw new Exception("X509_add_ext failed for " ~ val);
}

private EVP_PKEY* generateRSAKey2048() @trusted
{
    return EVP_PKEY_Q_keygen(null, null, "RSA".ptr, cast(size_t) 2048);
}

/// Generates a fresh 2048-bit RSA private key encoded in PKCS#1 DER format.
ubyte[] generateRSAPrivateKeyPKCS1DER() @trusted
{
    auto pkey = generateRSAKey2048();
    if (pkey is null)
        throw new Exception("EVP_RSA_gen(2048) failed");
    scope (exit) EVP_PKEY_free(pkey);
    return pkeyToPKCS1DER(pkey);
}

/// Validates that `der` is a well-formed PKCS#1 RSA private key.
void validateRSAPrivateKeyPKCS1DER(const(ubyte)[] der) @trusted
{
    auto pkey = pkeyFromPKCS1DER(der);
    EVP_PKEY_free(pkey);
}

KeyPairPEM generateLocalCACertificate(SysTime now = Clock.currTime()) @trusted
{
    auto pkey = generateRSAKey2048();
    if (pkey is null)
        throw new Exception("EVP_RSA_gen(2048) failed");
    scope (exit) EVP_PKEY_free(pkey);

    auto cert = X509_new();
    if (cert is null)
        throw new Exception("X509_new failed");
    scope (exit) X509_free(cert);

    X509_set_version(cert, 2); // X.509 v3
    setRandomSerial(cert);

    auto notBeforeUnix = cast(time_t)(now.toUnixTime() - 3600);
    auto notAfterUnix = cast(time_t)(now.toUnixTime() + 5L * 365 * 24 * 3600);
    auto nb = ASN1_TIME_set(null, notBeforeUnix);
    auto na = ASN1_TIME_set(null, notAfterUnix);
    scope (exit) { ASN1_TIME_free(nb); ASN1_TIME_free(na); }
    X509_set1_notBefore(cert, nb);
    X509_set1_notAfter(cert, na);

    auto subj = X509_get_subject_name(cert);
    string cn = "Polka Local Development CA";
    X509_NAME_add_entry_by_txt(subj, "CN", MBSTRING_ASC, cast(const(ubyte)*) cn.ptr, cast(int) cn.length, -1, 0);
    X509_set_issuer_name(cert, subj);
    X509_set_pubkey(cert, pkey);

    addX509Ext(cert, cert, NID_basic_constraints, "critical,CA:TRUE");
    addX509Ext(cert, cert, NID_key_usage, "critical,keyCertSign,cRLSign");
    addX509Ext(cert, cert, NID_subject_key_identifier, "hash");

    if (X509_sign(cert, pkey, EVP_sha256()) <= 0)
        throw new Exception("X509_sign CA failed");

    return KeyPairPEM(serializeX509ToPEM(cert), pkeyToPKCS1DER(pkey));
}

KeyPairPEM generateServerCertificate(
    string caCertPEM,
    const(ubyte)[] caKeyPKCS1DER,
    string[] dnsNames,
    string[] ipAddresses,
    SysTime now = Clock.currTime()
) @trusted
{
    auto caCert = parseX509Handle(caCertPEM);
    if (caCert is null)
        throw new Exception("invalid CA certificate PEM");
    scope (exit) X509_free(caCert);

    auto caKey = pkeyFromPKCS1DER(caKeyPKCS1DER);
    scope (exit) EVP_PKEY_free(caKey);

    auto serverKey = generateRSAKey2048();
    if (serverKey is null)
        throw new Exception("EVP_RSA_gen(2048) failed");
    scope (exit) EVP_PKEY_free(serverKey);

    auto cert = X509_new();
    if (cert is null)
        throw new Exception("X509_new failed");
    scope (exit) X509_free(cert);

    X509_set_version(cert, 2);
    setRandomSerial(cert);

    auto notBeforeUnix = cast(time_t)(now.toUnixTime() - 3600);
    auto notAfterUnix = cast(time_t)(now.toUnixTime() + 2L * 365 * 24 * 3600);
    auto nb = ASN1_TIME_set(null, notBeforeUnix);
    auto na = ASN1_TIME_set(null, notAfterUnix);
    scope (exit) { ASN1_TIME_free(nb); ASN1_TIME_free(na); }
    X509_set1_notBefore(cert, nb);
    X509_set1_notAfter(cert, na);

    auto subj = X509_get_subject_name(cert);
    string cn = dnsNames.length > 0 ? dnsNames[0] : "localhost";
    X509_NAME_add_entry_by_txt(subj, "CN", MBSTRING_ASC, cast(const(ubyte)*) cn.ptr, cast(int) cn.length, -1, 0);
    X509_set_issuer_name(cert, X509_get_subject_name(caCert));
    X509_set_pubkey(cert, serverKey);

    addX509Ext(cert, caCert, NID_basic_constraints, "CA:FALSE");
    addX509Ext(cert, caCert, NID_key_usage, "critical,digitalSignature,keyEncipherment");
    addX509Ext(cert, caCert, NID_ext_key_usage, "serverAuth");
    addX509Ext(cert, caCert, NID_subject_key_identifier, "hash");
    addX509Ext(cert, caCert, NID_authority_key_identifier, "keyid,issuer");

    string[] sanParts;
    foreach (dns; dnsNames)
        sanParts ~= "DNS:" ~ dns;
    foreach (ip; ipAddresses)
        sanParts ~= "IP:" ~ ip;
    if (sanParts.length > 0)
        addX509Ext(cert, caCert, NID_subject_alt_name, sanParts.join(","));

    if (X509_sign(cert, caKey, EVP_sha256()) <= 0)
        throw new Exception("X509_sign server cert failed");

    return KeyPairPEM(serializeX509ToPEM(cert), pkeyToPKCS1DER(serverKey));
}

/// Returns the lowercase hexadecimal serial number of an X.509 certificate PEM.
string certificateSerialHex(string certPEM) @trusted
{
    auto x = parseX509Handle(certPEM);
    if (x is null)
        throw new Exception("failed to parse X509 certificate PEM");
    scope (exit) X509_free(x);

    auto serial = X509_get0_serialNumber(x);
    if (serial is null)
        throw new Exception("certificate has no serial number");
    auto bn = ASN1_INTEGER_to_BN(serial, null);
    if (bn is null)
        throw new Exception("ASN1_INTEGER_to_BN failed");
    scope (exit) BN_free(bn);

    auto hexPtr = BN_bn2hex(bn);
    if (hexPtr is null)
        throw new Exception("BN_bn2hex failed");
    scope (exit) CRYPTO_free(hexPtr, __FILE__.ptr, __LINE__);

    return hexPtr[0 .. strlen(hexPtr)].idup.toLower();
}

/// Verifies that `leafCertPEM` chains to and is validly signed by `caCertPEM`.
bool verifyX509Chain(string leafCertPEM, string caCertPEM) @trusted
{
    auto leaf = parseX509Handle(leafCertPEM);
    if (leaf is null)
        return false;
    scope (exit) X509_free(leaf);

    auto ca = parseX509Handle(caCertPEM);
    if (ca is null)
        return false;
    scope (exit) X509_free(ca);

    auto store = X509_STORE_new();
    if (store is null)
        return false;
    scope (exit) X509_STORE_free(store);

    if (X509_STORE_add_cert(store, ca) != 1)
        return false;

    auto ctx = X509_STORE_CTX_new();
    if (ctx is null)
        return false;
    scope (exit) X509_STORE_CTX_free(ctx);

    if (X509_STORE_CTX_init(ctx, store, leaf, null) != 1)
        return false;

    return X509_verify_cert(ctx) == 1;
}

unittest
{
    assert(sha3_256Hex(cast(const(ubyte)[]) "abc") ==
        "3a985da74fe225b2045c172d6bd390bd855f086e3e9d525b46bfe24511431532");

    auto key = randomBytes(32);
    auto nonce = randomBytes(aesGcmNonceSize);
    auto sealed = aes256GcmSeal(key, nonce, cast(const(ubyte)[]) "secret-payload", cast(const(ubyte)[]) "aad-ctx");
    auto opened = aes256GcmOpen(key, nonce, sealed, cast(const(ubyte)[]) "aad-ctx");
    assert(cast(string) opened == "secret-payload");

    auto ca = generateLocalCACertificate();
    auto caInfo = parseX509CertificatePEM(ca.certPEM);
    assert(caInfo.isCA);
    assert(!caInfo.expired);
    assert(caInfo.sha1Hex.length == 40);
    assert(certificateSerialHex(ca.certPEM).length > 0);

    auto srv = generateServerCertificate(ca.certPEM, ca.keyPKCS1DER, ["localhost", "*.localhost", "blog.localhost"], ["127.0.0.1", "::1"]);
    auto srvInfo = parseX509CertificatePEM(srv.certPEM);
    assert(!srvInfo.isCA);
    assert(srvInfo.verifyHostname("blog.localhost"));
    assert(srvInfo.verifyHostname("127.0.0.1"));
    assert(srvInfo.hasExactHost("blog.localhost"));
    assert(!srvInfo.hasExactHost("other.localhost"));
    assert(verifyX509Chain(srv.certPEM, ca.certPEM));

    auto otherCA = generateLocalCACertificate();
    assert(!verifyX509Chain(srv.certPEM, otherCA.certPEM));
    assert(certificateSerialHex(ca.certPEM) != certificateSerialHex(otherCA.certPEM));
}
