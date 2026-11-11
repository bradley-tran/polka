module polka.support.http;

import core.stdc.string : memcpy;
import core.thread : Thread;
import core.time : dur, MonoTime;
import etc.c.curl;
import std.algorithm : startsWith;
import std.array : appender;
import std.conv : to;
import std.datetime : Clock, DateTime, Month, SysTime, UTC;
import std.format : format;
import std.string : indexOf, split, strip, toLower, toStringz;

struct HTTPResponse
{
    int statusCode = 0;
    string[string] headers;
    ubyte[] body;

    string bodyText() const @safe pure
    {
        return (cast(const(char)[]) body).idup;
    }

    string header(string name) const @safe pure
    {
        auto target = name.toLower();
        foreach (k, v; headers)
        {
            if (k.toLower() == target)
                return v;
        }
        return "";
    }
}

class HTTPException : Exception
{
    int statusCode;
    this(string msg, int statusCode = 0, string file = __FILE__, size_t line = __LINE__) @safe pure nothrow
    {
        super(msg, file, line);
        this.statusCode = statusCode;
    }
}

interface HTTPClient
{
    HTTPResponse get(string url, string[string] headers = null);
}

/// Default libcurl-backed HTTP client supporting redirects, custom headers, and retry-backoff.
class CurlHTTPClient : HTTPClient
{
    int maxAttempts = 3;
    long timeoutSeconds = 120;

    HTTPResponse get(string url, string[string] headers = null) @trusted
    {
        Exception lastErr;
        foreach (attempt; 0 .. maxAttempts)
        {
            try
            {
                auto resp = performSingleGet(url, headers);
                if (shouldRetryStatus(resp.statusCode) && attempt + 1 < maxAttempts)
                {
                    auto delayMs = retryDelayMs(resp.header("Retry-After"), attempt);
                    Thread.sleep(dur!"msecs"(delayMs));
                    continue;
                }
                return resp;
            }
            catch (Exception e)
            {
                lastErr = e;
                if (attempt + 1 < maxAttempts)
                {
                    auto delayMs = 250L << attempt;
                    Thread.sleep(dur!"msecs"(delayMs));
                    continue;
                }
                throw e;
            }
        }
        if (lastErr !is null)
            throw lastErr;
        throw new HTTPException("HTTP request failed for " ~ url);
    }

    private HTTPResponse performSingleGet(string url, string[string] reqHeaders) @trusted
    {
        auto curl = curl_easy_init();
        if (curl is null)
            throw new HTTPException("curl_easy_init failed");
        scope (exit) curl_easy_cleanup(curl);

        struct RecvState
        {
            typeof(appender!(ubyte[])()) bodyBuf;
            string[string] headers;
        }
        RecvState state;
        state.bodyBuf = appender!(ubyte[])();

        static extern (C) size_t writeCallback(char* ptr, size_t size, size_t nmemb, void* userdata) nothrow
        {
            auto st = cast(RecvState*) userdata;
            size_t total = size * nmemb;
            if (total > 0)
                st.bodyBuf.put((cast(const(ubyte)*) ptr)[0 .. total]);
            return total;
        }

        static extern (C) size_t headerCallback(char* ptr, size_t size, size_t nmemb, void* userdata) nothrow
        {
            auto st = cast(RecvState*) userdata;
            size_t total = size * nmemb;
            try
            {
                auto line = (cast(const(char)*) ptr)[0 .. total].strip();
                if (line.startsWith("HTTP/"))
                {
                    // Reset headers on redirect hops
                    st.headers = null;
                }
                else
                {
                    auto colon = line.indexOf(':');
                    if (colon > 0)
                    {
                        auto k = line[0 .. colon].strip().idup;
                        auto v = line[colon + 1 .. $].strip().idup;
                        st.headers[k] = v;
                    }
                }
            }
            catch (Exception) {}
            return total;
        }

        curl_easy_setopt(curl, CurlOption.url, url.toStringz);
        curl_easy_setopt(curl, CurlOption.followlocation, 1L);
        curl_easy_setopt(curl, CurlOption.maxredirs, 10L);
        curl_easy_setopt(curl, CurlOption.timeout, timeoutSeconds);
        curl_easy_setopt(curl, CurlOption.connecttimeout, 30L);
        curl_easy_setopt(curl, CurlOption.useragent, "polka".toStringz);
        curl_easy_setopt(curl, CurlOption.writefunction, &writeCallback);
        curl_easy_setopt(curl, CurlOption.writedata, &state);
        curl_easy_setopt(curl, CurlOption.headerfunction, &headerCallback);
        curl_easy_setopt(curl, CurlOption.headerdata, &state);

        curl_slist* headerList = null;
        scope (exit)
        {
            if (headerList !is null)
                curl_slist_free_all(headerList);
        }
        foreach (k, v; reqHeaders)
        {
            headerList = curl_slist_append(headerList, (k ~ ": " ~ v).toStringz);
        }
        if (headerList !is null)
            curl_easy_setopt(curl, CurlOption.httpheader, headerList);

        auto res = curl_easy_perform(curl);
        if (res != CurlError.ok)
        {
            import std.string : fromStringz;
            auto errText = fromStringz(curl_easy_strerror(res)).idup;
            throw new HTTPException("GET " ~ url ~ ": " ~ errText);
        }

        long code = 0;
        curl_easy_getinfo(curl, CurlInfo.response_code, &code);

        HTTPResponse resp;
        resp.statusCode = cast(int) code;
        resp.headers = state.headers;
        resp.body = state.bodyBuf.data;
        return resp;
    }
}

bool shouldRetryStatus(int code) @safe pure nothrow @nogc
{
    return code == 408 || code == 425 || code == 429 || code == 500 || code == 502 || code == 503 || code == 504;
}

long retryDelayMs(string retryAfter, int attempt) @safe
{
    auto trimmed = retryAfter.strip();
    if (trimmed.length > 0)
    {
        try
        {
            long secs = to!long(trimmed);
            if (secs > 0)
                return secs * 1000;
            return 0;
        }
        catch (Exception) {}
    }
    return 250L << attempt;
}

/// Mock HTTP client for deterministic offline unit tests.
class MockHTTPClient : HTTPClient
{
    alias Handler = HTTPResponse delegate(string url, string[string] headers);
    Handler handler;
    int maxAttempts = 3;

    this(Handler h) @safe pure nothrow
    {
        this.handler = h;
    }

    HTTPResponse get(string url, string[string] headers = null)
    {
        foreach (attempt; 0 .. maxAttempts)
        {
            auto resp = handler(url, headers);
            if (shouldRetryStatus(resp.statusCode) && attempt + 1 < maxAttempts)
                continue;
            return resp;
        }
        return handler(url, headers);
    }
}

unittest
{
    int calls = 0;
    auto mock = new MockHTTPClient((string url, string[string] hdrs) {
        calls++;
        if (calls == 1)
            return HTTPResponse(503, null, null);
        return HTTPResponse(200, ["Content-Type": "text/plain"], cast(ubyte[]) "ok");
    });
    auto resp = mock.get("https://example.test/data");
    assert(calls == 2);
    assert(resp.statusCode == 200);
    assert(resp.bodyText() == "ok");
}
