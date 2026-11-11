module polka.support.net;

import core.time : Duration, dur;
import std.algorithm : canFind;
import std.conv : to;
import std.format : format;
import std.socket : Address, getAddress, ProtocolType, Socket, SocketException, SocketOption, SocketOptionLevel, SocketSet, SocketType;
import std.string : indexOf, lastIndexOf, strip;

/// Splits a network address of the form `host:port` or `[host]:port` into host and port components.
/// Matches Go's `net.SplitHostPort` validation semantics.
bool splitHostPort(string address, out string host, out string port) @safe pure
{
    auto trimmed = address.strip();
    if (trimmed.length == 0)
        return false;

    auto lastColon = trimmed.lastIndexOf(':');
    if (lastColon < 0)
        return false;

    string hostPart;
    if (trimmed[0] == '[')
    {
        auto closeBracket = trimmed.indexOf(']');
        if (closeBracket < 0 || closeBracket + 1 != lastColon)
            return false;
        hostPart = trimmed[1 .. closeBracket];
    }
    else
    {
        auto prefix = trimmed[0 .. lastColon];
        if (prefix.canFind(':') || prefix.canFind('[') || prefix.canFind(']'))
            return false;
        hostPart = prefix;
    }

    auto portPart = trimmed[lastColon + 1 .. $];
    if (portPart.canFind('[') || portPart.canFind(']') || portPart.canFind(':'))
        return false;

    host = hostPart;
    port = portPart;
    return true;
}

/// Joins `host` and `port` into `host:port` or `[host]:port` (if `host` contains `:`).
string joinHostPort(string host, string port) @safe pure
{
    if (host.canFind(':'))
        return "[" ~ host ~ "]:" ~ port;
    return host ~ ":" ~ port;
}

/// Overload accepting a numeric TCP/UDP port.
string joinHostPort(string host, int port) @safe
{
    return joinHostPort(host, to!string(port));
}

/// Probes whether a TCP listener at `address` accepts connections within `timeout`.
bool pingTCPAddress(string address, Duration timeout = dur!"msecs"(150)) @trusted
{
    string host, portStr;
    if (!splitHostPort(address, host, portStr) || portStr.length == 0)
        return false;

    ushort portNum;
    try
    {
        auto parsed = to!int(portStr);
        if (parsed <= 0 || parsed > 65535)
            return false;
        portNum = cast(ushort) parsed;
    }
    catch (Exception)
    {
        return false;
    }

    if (host.length == 0)
        host = "127.0.0.1";

    Address[] resolved;
    try
    {
        resolved = getAddress(host, portNum);
    }
    catch (SocketException)
    {
        return false;
    }
    catch (Exception)
    {
        return false;
    }

    foreach (addr; resolved)
    {
        Socket sock;
        try
        {
            sock = new Socket(addr.addressFamily, SocketType.STREAM, ProtocolType.TCP);
        }
        catch (SocketException)
        {
            continue;
        }

        import std.exception : collectException;
        scope (exit) collectException(sock.close());

        try
        {
            sock.blocking = false;
            sock.connect(addr);

            auto writeSet = new SocketSet(1);
            auto errSet = new SocketSet(1);
            writeSet.add(sock);
            errSet.add(sock);

            auto ready = Socket.select(null, writeSet, errSet, timeout);
            if (ready <= 0)
                continue;
            if (!writeSet.isSet(sock) && !errSet.isSet(sock))
                continue;

            int sockErr = 0;
            sock.getOption(SocketOptionLevel.SOCKET, SocketOption.ERROR, sockErr);
            if (sockErr == 0)
                return true;
        }
        catch (SocketException)
        {
            continue;
        }
    }

    return false;
}

unittest
{
    import std.socket : InternetAddress, TcpSocket;

    string host, port;
    assert(splitHostPort("127.0.0.1:8080", host, port));
    assert(host == "127.0.0.1" && port == "8080");

    assert(splitHostPort("[::1]:5432", host, port));
    assert(host == "::1" && port == "5432");

    assert(splitHostPort(":3306", host, port));
    assert(host == "" && port == "3306");

    assert(!splitHostPort("127.0.0.1", host, port));
    assert(!splitHostPort("::1:5432", host, port));
    assert(!splitHostPort("[::1:5432", host, port));

    assert(joinHostPort("127.0.0.1", 3306) == "127.0.0.1:3306");
    assert(joinHostPort("::1", 3306) == "[::1]:3306");

    // Verify TCP probe returns true on a bound listening socket and false once closed.
    auto listener = new TcpSocket();
    listener.bind(new InternetAddress("127.0.0.1", 0));
    listener.listen(4);
    auto boundPort = (cast(InternetAddress) listener.localAddress).port;
    auto addr = joinHostPort("127.0.0.1", boundPort);
    assert(pingTCPAddress(addr, dur!"msecs"(200)));

    listener.close();
    assert(!pingTCPAddress(addr, dur!"msecs"(200)));
}
