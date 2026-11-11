module polka.cli.keyring;

// Native OS keyring adapter replacing go-keyring:
// - Linux/BSD: secret-tool (libsecret / org.freedesktop.secrets)
// - macOS: /usr/bin/security (Keychain Services)
// - Windows: PowerShell DPAPI-free Windows Credential Manager or Advapi32
// Under `version (unittest)`, initializes an in-memory mock store by default.

import core.sync.mutex : Mutex;
import std.conv : to;
import std.format : format;
import std.string : strip, stripRight, toLower;

import polka.backend : BufferTextWriter, lookPath, runCommandStreaming;

/// Error thrown when a secret does not exist in the keyring (`keyring.ErrNotFound`).
final class KeyringNotFoundException : Exception
{
    this(string msg = "secret not found in keyring", string file = __FILE__, size_t line = __LINE__) @safe pure nothrow
    {
        super(msg, file, line);
    }
}

/// Reports whether `err` represents a missing keyring entry.
bool isKeyringNotFound(Throwable err) @safe pure nothrow @nogc
{
    return (cast(KeyringNotFoundException) err) !is null;
}

private struct KeyringState
{
    bool mockEnabled = false;
    Exception mockError;
    string[string][string] mockStore;
}

private __gshared Mutex keyringMu;
private __gshared KeyringState keyringState;

shared static this()
{
    keyringMu = new Mutex();
    version (unittest)
    {
        keyringMockInit();
    }
}

/// Switches keyring operations to an empty in-memory mock store.
void keyringMockInit() @trusted
{
    keyringMu.lock();
    scope (exit) keyringMu.unlock();
    keyringState.mockEnabled = true;
    keyringState.mockError = null;
    keyringState.mockStore = null;
}

/// Switches keyring operations to a mock mode that fails every call with `err`.
void keyringMockInitWithError(Exception err) @trusted
{
    keyringMu.lock();
    scope (exit) keyringMu.unlock();
    keyringState.mockEnabled = true;
    keyringState.mockError = err;
    keyringState.mockStore = null;
}

/// Disables the in-memory mock and restores native OS keyring access.
void keyringUseSystem() @trusted
{
    keyringMu.lock();
    scope (exit) keyringMu.unlock();
    keyringState.mockEnabled = false;
    keyringState.mockError = null;
    keyringState.mockStore = null;
}

/// Retrieves a password from the active keyring backend.
string keyringGet(string service, string user) @trusted
{
    {
        keyringMu.lock();
        scope (exit) keyringMu.unlock();
        if (keyringState.mockEnabled)
        {
            if (keyringState.mockError !is null)
                throw keyringState.mockError;
            auto svc = service in keyringState.mockStore;
            if (svc !is null)
            {
                if (auto val = user in *svc)
                    return *val;
            }
            throw new KeyringNotFoundException();
        }
    }

    return osKeyringGet(service, user);
}

/// Stores or replaces a password in the active keyring backend.
void keyringSet(string service, string user, string password) @trusted
{
    {
        keyringMu.lock();
        scope (exit) keyringMu.unlock();
        if (keyringState.mockEnabled)
        {
            if (keyringState.mockError !is null)
                throw keyringState.mockError;
            if ((service in keyringState.mockStore) is null)
                keyringState.mockStore[service] = null;
            keyringState.mockStore[service][user] = password;
            return;
        }
    }

    osKeyringSet(service, user, password);
}

/// Removes a stored password from the active keyring backend.
void keyringDelete(string service, string user) @trusted
{
    {
        keyringMu.lock();
        scope (exit) keyringMu.unlock();
        if (keyringState.mockEnabled)
        {
            if (keyringState.mockError !is null)
                throw keyringState.mockError;
            auto svc = service in keyringState.mockStore;
            if (svc !is null && (user in *svc) !is null)
            {
                (*svc).remove(user);
                return;
            }
            throw new KeyringNotFoundException();
        }
    }

    osKeyringDelete(service, user);
}

version (OSX)
{
    private string osKeyringGet(string service, string user) @trusted
    {
        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();
        auto code = runCommandStreaming(
            ["/usr/bin/security", "find-generic-password", "-s", service, "-a", user, "-w"],
            null, null, outBuf, errBuf
        );
        if (code == 44)
            throw new KeyringNotFoundException();
        if (code != 0)
            throw new Exception(format("macOS keychain lookup failed (exit %d): %s", code, errBuf.text().strip()));
        return outBuf.text().stripRight("\r\n");
    }

    private void osKeyringSet(string service, string user, string password) @trusted
    {
        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();
        auto code = runCommandStreaming(
            ["/usr/bin/security", "add-generic-password", "-U", "-s", service, "-a", user, "-w", password],
            null, null, outBuf, errBuf
        );
        if (code != 0)
            throw new Exception(format("macOS keychain store failed (exit %d): %s", code, errBuf.text().strip()));
    }

    private void osKeyringDelete(string service, string user) @trusted
    {
        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();
        auto code = runCommandStreaming(
            ["/usr/bin/security", "delete-generic-password", "-s", service, "-a", user],
            null, null, outBuf, errBuf
        );
        if (code == 44)
            throw new KeyringNotFoundException();
        if (code != 0)
            throw new Exception(format("macOS keychain delete failed (exit %d): %s", code, errBuf.text().strip()));
    }
}
else version (Windows)
{
    import core.stdc.string : memcpy;
    import core.sys.windows.windef : BOOL, BYTE, DWORD, FILETIME, LPWSTR;
    import core.sys.windows.winbase : GetLastError;
    import std.utf : toUTF16, toUTF16z, toUTF8;

    private enum DWORD CRED_TYPE_GENERIC = 1;
    private enum DWORD CRED_PERSIST_LOCAL_MACHINE = 2;
    private enum DWORD ERROR_NOT_FOUND = 1168;

    private struct CREDENTIALW
    {
        DWORD Flags;
        DWORD Type;
        LPWSTR TargetName;
        LPWSTR Comment;
        FILETIME LastWritten;
        DWORD CredentialBlobSize;
        BYTE* CredentialBlob;
        DWORD Persist;
        DWORD AttributeCount;
        void* Attributes;
        LPWSTR TargetAlias;
        LPWSTR UserName;
    }

    private extern (Windows) nothrow @nogc
    {
        BOOL CredReadW(const(wchar)* TargetName, DWORD Type, DWORD Flags, CREDENTIALW** Credential);
        BOOL CredWriteW(CREDENTIALW* Credential, DWORD Flags);
        BOOL CredDeleteW(const(wchar)* TargetName, DWORD Type, DWORD Flags);
        void CredFree(void* Buffer);
    }

    private string windowsTargetName(string service, string user) @safe pure
    {
        return service ~ ":" ~ user;
    }

    private string osKeyringGet(string service, string user) @trusted
    {
        CREDENTIALW* cred = null;
        auto target = windowsTargetName(service, user).toUTF16z;
        if (!CredReadW(target, CRED_TYPE_GENERIC, 0, &cred))
        {
            auto err = GetLastError();
            if (err == ERROR_NOT_FOUND)
                throw new KeyringNotFoundException();
            throw new Exception(format("CredReadW failed: win32 error %d", err));
        }
        scope (exit) CredFree(cred);
        if (cred.CredentialBlob is null || cred.CredentialBlobSize == 0)
            return "";
        return (cast(const(char)[]) cred.CredentialBlob[0 .. cred.CredentialBlobSize]).idup;
    }

    private void osKeyringSet(string service, string user, string password) @trusted
    {
        auto targetW = windowsTargetName(service, user).toUTF16z;
        auto userW = user.toUTF16z;
        CREDENTIALW cred;
        cred.Type = CRED_TYPE_GENERIC;
        cred.TargetName = cast(LPWSTR) targetW;
        cred.UserName = cast(LPWSTR) userW;
        cred.Persist = CRED_PERSIST_LOCAL_MACHINE;
        cred.CredentialBlobSize = cast(DWORD) password.length;
        cred.CredentialBlob = cast(BYTE*) (password.length > 0 ? password.ptr : null);
        if (!CredWriteW(&cred, 0))
            throw new Exception(format("CredWriteW failed: win32 error %d", GetLastError()));
    }

    private void osKeyringDelete(string service, string user) @trusted
    {
        auto targetW = windowsTargetName(service, user).toUTF16z;
        if (!CredDeleteW(targetW, CRED_TYPE_GENERIC, 0))
        {
            auto err = GetLastError();
            if (err == ERROR_NOT_FOUND)
                throw new KeyringNotFoundException();
            throw new Exception(format("CredDeleteW failed: win32 error %d", err));
        }
    }
}
else
{
    private string osKeyringGet(string service, string user) @trusted
    {
        auto tool = lookPath("secret-tool");
        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();
        auto code = runCommandStreaming(
            [tool, "lookup", "service", service, "username", user],
            null, null, outBuf, errBuf
        );
        if (code == 1 && errBuf.text().strip().length == 0)
            throw new KeyringNotFoundException();
        if (code != 0)
            throw new Exception(format("secret-tool lookup failed (exit %d): %s", code, errBuf.text().strip()));
        return outBuf.text().stripRight("\r\n");
    }

    private void osKeyringSet(string service, string user, string password) @trusted
    {
        auto tool = lookPath("secret-tool");
        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();
        auto code = runCommandStreaming(
            [tool, "store", "--label=" ~ service, "service", service, "username", user],
            null, null, outBuf, errBuf, password
        );
        if (code != 0)
            throw new Exception(format("secret-tool store failed (exit %d): %s", code, errBuf.text().strip()));
    }

    private void osKeyringDelete(string service, string user) @trusted
    {
        auto tool = lookPath("secret-tool");
        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();
        auto code = runCommandStreaming(
            [tool, "clear", "service", service, "username", user],
            null, null, outBuf, errBuf
        );
        if (code != 0)
            throw new Exception(format("secret-tool clear failed (exit %d): %s", code, errBuf.text().strip()));
    }
}

unittest
{
    import std.exception : assertThrown;

    keyringMockInit();
    scope (exit) keyringMockInit();

    assertThrown!KeyringNotFoundException(keyringGet("polka-tls-key", "abc"));

    keyringSet("polka-tls-key", "abc", "secret-val");
    assert(keyringGet("polka-tls-key", "abc") == "secret-val");

    keyringDelete("polka-tls-key", "abc");
    assertThrown!KeyringNotFoundException(keyringGet("polka-tls-key", "abc"));

    keyringMockInitWithError(new Exception("no keyring"));
    bool threwCustom = false;
    try
    {
        keyringGet("polka-tls-key", "abc");
    }
    catch (Exception e)
    {
        threwCustom = e.msg == "no keyring" && !isKeyringNotFound(e);
    }
    assert(threwCustom);
}
