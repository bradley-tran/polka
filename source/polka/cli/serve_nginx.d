module polka.cli.serve_nginx;

// Nginx serve lifecycle: foreground and background startup of nginx with its
// `php-cgi` upstream, plus nginx config generation (framework-provided or the
// generic front-controller configuration).

import std.array : appender;
import std.conv : to;
import std.file : mkdirRecurse, write;
import std.format : format;
import std.path : baseName, buildPath, dirSeparator;
import std.socket : AddressFamily, parseAddress;
import std.string : endsWith, icmp, join, strip, toLower;

import polka.backend :
    Environment,
    FrameworkPlugin,
    newDefaultPluginRegistry,
    primaryPHPVersion,
    Store,
    TextWriter;
import polka.cli.env_runtime : resolveRuntimeEnvironment;
import polka.cli.serve_runtime :
    defaultServeHostname,
    openServeLog,
    reserveServeBackendAddress,
    resolvePHPCGITarget,
    serveLogFileName,
    serveProbeAddress,
    serveProxyHost,
    serveStartupTimeout,
    serveStaticMIMETypes,
    stopServePID,
    stopServePIDQuietly,
    waitForServeAddress;
import polka.cli.serve_tls :
    ensureGlobalTLSCertificateRuntimeKey,
    ServeTLSConfig;
import polka.cli.types :
    applyManagedPHPRuntimeConfig,
    callServeNow,
    executeTargetWithEnv,
    quoteGoString;
import polka.config : ServerTypeNginx;
import polka.plugins : NginxConfigContext, NginxConfigResult;
import polka.service :
    AppLayout,
    currentEnvList,
    Endpoint,
    ServeRuntimeState,
    spawnDetachedServiceProcess,
    toolLogRoot;
import polka.support.fs : currentOS, toSlashPath;
import polka.support.net : joinHostPort, splitHostPort;

/// Test seams for the nginx webserver lifecycle.
int delegate(TextWriter stdout, TextWriter stderr, Store store, Environment environment, Endpoint endpoint, AppLayout layout) runNginxServeFunc;
ServeRuntimeState delegate(Store store, Environment environment, Endpoint endpoint, AppLayout layout) startBackgroundNginxServe;

/// Calls `runNginxServeFunc` or `runNginxServe`.
int callRunNginxServe(TextWriter stdout, TextWriter stderr, Store store, Environment envConfig, Endpoint endpoint, AppLayout layout) @trusted
{
    if (runNginxServeFunc !is null)
        return runNginxServeFunc(stdout, stderr, store, envConfig, endpoint, layout);
    return runNginxServe(stdout, stderr, store, envConfig, endpoint, layout);
}

/// Calls `startBackgroundNginxServe` or `startNginxServeInBackground`.
ServeRuntimeState callStartBackgroundNginxServe(Store store, Environment envConfig, Endpoint endpoint, AppLayout layout) @trusted
{
    if (startBackgroundNginxServe !is null)
        return startBackgroundNginxServe(store, envConfig, endpoint, layout);
    return startNginxServeInBackground(store, envConfig, endpoint, layout);
}

/// Runs nginx attached to the current terminal, backed by a `php-cgi` upstream
/// that is stopped when nginx exits.
int runNginxServe(
    TextWriter stdout,
    TextWriter stderr,
    Store store,
    Environment envConfig,
    Endpoint endpoint,
    AppLayout layout
) @trusted
{
    if (primaryPHPVersion(envConfig).strip().length == 0)
        throw new Exception(format(`environment "%s" defines nginx but does not define a php version`, envConfig.name));

    auto phpTarget = store.resolveTool("php");
    auto phpCGITarget = resolvePHPCGITarget(phpTarget);
    auto nginxTarget = store.resolveTool("nginx");
    auto env = resolveRuntimeEnvironment(currentOS(), currentEnvList(), store);
    env = applyManagedPHPRuntimeConfig(currentOS(), env, phpTarget);

    auto backendAddress = reserveServeBackendAddress();
    auto runtimeDir = toolLogRoot(store.rootDir, "nginx", envConfig.name);
    string configPath, phpLogPath;
    prepareNginxServeRuntime(store.cacheDir, runtimeDir, envConfig, endpoint, layout, backendAddress, configPath, phpLogPath);

    auto phpLogFile = openServeLog(phpLogPath);
    scope (exit)
    {
        if (phpLogFile.isOpen)
            phpLogFile.close();
    }

    int phpPID = 0;
    try
    {
        phpPID = spawnDetachedServiceProcess(phpCGITarget, ["-b", backendAddress], phpLogFile, env);
    }
    catch (Exception e)
    {
        throw new Exception("start php-cgi upstream: " ~ e.msg);
    }
    scope (exit)
    {
        if (phpPID > 0)
            stopServePIDQuietly(phpPID); // Best-effort cleanup on exit.
    }

    try
    {
        waitForServeAddress(backendAddress, serveStartupTimeout);
    }
    catch (Exception e)
    {
        throw new Exception(format("start php-cgi upstream on %s: %s (see %s)", backendAddress, e.msg, phpLogPath));
    }

    auto nginxArgs = ["-p", ensureServePrefix(runtimeDir), "-c", baseName(configPath), "-g", "daemon off;"];
    return executeTargetWithEnv(stdout, stderr, env, nginxTarget, nginxArgs);
}

/// Starts and records the managed nginx webserver with its `php-cgi` upstream.
ServeRuntimeState startNginxServeInBackground(
    Store store,
    Environment envConfig,
    Endpoint endpoint,
    AppLayout layout
) @trusted
{
    return startNginxServeInBackgroundAt(
        store,
        envConfig,
        endpoint,
        layout,
        toolLogRoot(store.rootDir, "nginx", envConfig.name)
    );
}

/// Starts the managed nginx webserver and its `php-cgi` upstream inside `runtimeDir`.
ServeRuntimeState startNginxServeInBackgroundAt(
    Store store,
    Environment envConfig,
    Endpoint endpoint,
    AppLayout layout,
    string runtimeDir
) @trusted
{
    if (primaryPHPVersion(envConfig).strip().length == 0)
        throw new Exception(format(`environment "%s" defines nginx but does not define a php version`, envConfig.name));

    auto phpTarget = store.resolveTool("php");
    auto phpCGITarget = resolvePHPCGITarget(phpTarget);
    auto nginxTarget = store.resolveTool("nginx");
    auto env = resolveRuntimeEnvironment(currentOS(), currentEnvList(), store);
    env = applyManagedPHPRuntimeConfig(currentOS(), env, phpTarget);

    auto backendAddress = reserveServeBackendAddress();
    string configPath, phpLogPath;
    prepareNginxServeRuntime(store.cacheDir, runtimeDir, envConfig, endpoint, layout, backendAddress, configPath, phpLogPath);

    auto phpLogFile = openServeLog(phpLogPath);
    scope (exit)
    {
        if (phpLogFile.isOpen)
            phpLogFile.close();
    }

    int phpPID = 0;
    try
    {
        phpPID = spawnDetachedServiceProcess(phpCGITarget, ["-b", backendAddress], phpLogFile, env);
    }
    catch (Exception e)
    {
        throw new Exception("start php-cgi upstream: " ~ e.msg);
    }

    try
    {
        waitForServeAddress(backendAddress, serveStartupTimeout);
    }
    catch (Exception e)
    {
        try { stopServePID(phpPID); } catch (Exception) {}
        throw new Exception(format("start php-cgi upstream on %s: %s (see %s)", backendAddress, e.msg, phpLogPath));
    }

    auto nginxLogPath = buildPath(runtimeDir, serveLogFileName);
    auto nginxLogFile = openServeLog(nginxLogPath);
    scope (exit)
    {
        if (nginxLogFile.isOpen)
            nginxLogFile.close();
    }

    auto nginxArgs = ["-p", ensureServePrefix(runtimeDir), "-c", baseName(configPath), "-g", "daemon off;"];
    int nginxPID = 0;
    try
    {
        nginxPID = spawnDetachedServiceProcess(nginxTarget, nginxArgs, nginxLogFile, env);
    }
    catch (Exception e)
    {
        try { stopServePID(phpPID); } catch (Exception) {}
        throw new Exception("start nginx webserver: " ~ e.msg);
    }

    try
    {
        waitForServeAddress(serveProbeAddress(endpoint.address), serveStartupTimeout);
    }
    catch (Exception e)
    {
        try { stopServePID(nginxPID); } catch (Exception) {}
        try { stopServePID(phpPID); } catch (Exception) {}
        throw new Exception(format("start nginx webserver on %s: %s (see %s and %s)", endpoint.address, e.msg, nginxLogPath, phpLogPath));
    }

    ServeRuntimeState state;
    state.environmentName = envConfig.name;
    state.serverKind = ServerTypeNginx;
    state.serverScheme = endpoint.scheme;
    state.serverAddress = endpoint.address;
    state.docroot = layout.docroot;
    state.runtimeDir = runtimeDir;
    state.logPath = nginxLogPath;
    state.backendLogPath = phpLogPath;
    state.configPath = configPath;
    state.primaryPID = nginxPID;
    state.secondaryPID = phpPID;
    state.startedAt = callServeNow().toUTC();
    return state;
}

/// Creates the nginx runtime directory tree, provisions TLS material when
/// needed, and writes the generated nginx config.
void prepareNginxServeRuntime(
    string cacheDir,
    string runtimeDir,
    Environment envConfig,
    Endpoint endpoint,
    AppLayout layout,
    string backendAddress,
    out string configPath,
    out string phpLogPath
) @trusted
{
    auto tempRoot = buildPath(runtimeDir, "temp");
    auto logsDir = buildPath(runtimeDir, "logs");
    foreach (p; [
        runtimeDir,
        logsDir,
        buildPath(tempRoot, "client_body"),
        buildPath(tempRoot, "proxy"),
        buildPath(tempRoot, "fastcgi"),
        buildPath(tempRoot, "uwsgi"),
        buildPath(tempRoot, "scgi"),
    ])
    {
        try
        {
            mkdirRecurse(p);
        }
        catch (Exception e)
        {
            throw new Exception("create serve runtime directory: " ~ e.msg);
        }
    }

    string host, portText;
    if (!splitHostPort(endpoint.address, host, portText))
        throw new Exception(format(`parse serve address "%s": invalid host:port`, endpoint.address));

    int port = 0;
    try
    {
        port = to!int(portText);
    }
    catch (Exception e)
    {
        throw new Exception(format(`parse serve port "%s": %s`, portText, e.msg));
    }

    ServeTLSConfig tlsConfig;
    if (endpoint.https)
    {
        auto kp = ensureGlobalTLSCertificateRuntimeKey(cacheDir, runtimeDir, host);
        tlsConfig = ServeTLSConfig(true, kp.certPath, kp.keyPath);
    }

    configPath = buildPath(runtimeDir, "nginx.conf");
    phpLogPath = buildPath(runtimeDir, "php.log");
    auto config = renderFrameworkNginxServeConfig(envConfig, host, port, layout, backendAddress, tlsConfig);
    if (config is null)
        config = cast(ubyte[]) renderNginxServeConfig(host, port, layout, backendAddress, tlsConfig);

    try
    {
        write(configPath, config);
    }
    catch (Exception e)
    {
        throw new Exception("write nginx config: " ~ e.msg);
    }
}

/// Asks the environment's framework plugin for an nginx config. Returns `null`
/// when no framework is configured or the plugin does not handle nginx config.
ubyte[] renderFrameworkNginxServeConfig(
    Environment envConfig,
    string host,
    int port,
    AppLayout layout,
    string backendAddress,
    ServeTLSConfig tlsConfig
) @trusted
{
    if (envConfig.framework.strip().length == 0)
        return null;

    FrameworkPlugin plugin;
    if (!newDefaultPluginRegistry().framework(envConfig.framework, plugin))
        throw new Exception(format(`unsupported framework "%s"`, envConfig.framework));

    NginxConfigContext ctx;
    ctx.environment = envConfig;
    ctx.host = host;
    ctx.port = port;
    ctx.docroot = layout.docroot;
    ctx.indexNames = renderNginxIndexNames(layout);
    ctx.tryFilesFallback = renderNginxTryFilesFallback(layout);
    ctx.fastCGIIndex = renderNginxFastCGIIndex(layout);
    ctx.backendAddress = backendAddress;
    ctx.tlsEnabled = tlsConfig.enabled;
    ctx.tlsCertificatePath = tlsConfig.certificatePath;
    ctx.tlsCertificateKeyPath = tlsConfig.certificateKeyPath;

    NginxConfigResult res;
    if (!plugin.nginxConfig(ctx, res))
        return null;
    return res.config;
}

/// Renders the generic front-controller nginx config.
string renderNginxServeConfig(
    string host,
    int port,
    AppLayout layout,
    string backendAddress,
    ServeTLSConfig tlsConfig
) @safe
{
    auto listenAddress = renderNginxListenAddress(host, port);
    auto serverName = host.strip();
    if (serverName.length == 0)
        serverName = "_";

    auto b = appender!string();
    b.put("worker_processes 1;\n");
    b.put("pid nginx.pid;\n");
    b.put("events {\n");
    b.put("    worker_connections 1024;\n");
    b.put("}\n");
    b.put("http {\n");
    b.put("    access_log logs/access.log;\n");
    b.put("    error_log logs/error.log notice;\n");
    b.put("    types {\n");
    writeServeNginxMIMETypes(b, "        ");
    b.put("    }\n");
    b.put("    default_type application/octet-stream;\n");
    b.put("    client_body_temp_path temp/client_body;\n");
    b.put("    proxy_temp_path temp/proxy;\n");
    b.put("    fastcgi_temp_path temp/fastcgi;\n");
    b.put("    uwsgi_temp_path temp/uwsgi;\n");
    b.put("    scgi_temp_path temp/scgi;\n");
    b.put("    server {\n");
    b.put("        listen ");
    b.put(listenAddress);
    if (tlsConfig.enabled)
        b.put(" ssl");
    b.put(";\n");
    b.put("        server_name ");
    b.put(serverName);
    b.put(";\n");
    if (tlsConfig.enabled)
    {
        b.put("        ssl_certificate ");
        b.put(quoteNginxPath(tlsConfig.certificatePath));
        b.put(";\n");
        b.put("        ssl_certificate_key ");
        b.put(quoteNginxPath(tlsConfig.certificateKeyPath));
        b.put(";\n");
        b.put("        ssl_protocols TLSv1.2 TLSv1.3;\n");
    }
    b.put("        index ");
    b.put(renderNginxIndexNames(layout));
    b.put(";\n");
    b.put("        root ");
    b.put(quoteNginxPath(layout.docroot));
    b.put(";\n");
    b.put("        location / {\n");
    b.put("            try_files $uri $uri/ ");
    b.put(renderNginxTryFilesFallback(layout));
    b.put(";\n");
    b.put("        }\n");
    b.put("        location ~ \\.php(?:$|/) {\n");
    b.put("            fastcgi_split_path_info ^(.+?\\.php)(/.*)$;\n");
    b.put("            try_files $fastcgi_script_name =404;\n");
    b.put("            fastcgi_pass ");
    b.put(backendAddress);
    b.put(";\n");
    b.put("            fastcgi_index ");
    b.put(renderNginxFastCGIIndex(layout));
    b.put(";\n");
    b.put("            fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;\n");
    b.put("            fastcgi_param SCRIPT_NAME $fastcgi_script_name;\n");
    b.put("            fastcgi_param DOCUMENT_ROOT $document_root;\n");
    b.put("            fastcgi_param QUERY_STRING $query_string;\n");
    b.put("            fastcgi_param REQUEST_METHOD $request_method;\n");
    b.put("            fastcgi_param CONTENT_TYPE $content_type;\n");
    b.put("            fastcgi_param CONTENT_LENGTH $content_length;\n");
    b.put("            fastcgi_param REQUEST_URI $request_uri;\n");
    b.put("            fastcgi_param DOCUMENT_URI $document_uri;\n");
    b.put("            fastcgi_param SERVER_PROTOCOL $server_protocol;\n");
    b.put("            fastcgi_param REQUEST_SCHEME $scheme;\n");
    b.put("            fastcgi_param HTTPS $https if_not_empty;\n");
    b.put("            fastcgi_param GATEWAY_INTERFACE CGI/1.1;\n");
    b.put("            fastcgi_param SERVER_SOFTWARE nginx/$nginx_version;\n");
    b.put("            fastcgi_param REMOTE_ADDR $remote_addr;\n");
    b.put("            fastcgi_param REMOTE_PORT $remote_port;\n");
    b.put("            fastcgi_param REMOTE_USER $remote_user;\n");
    b.put("            fastcgi_param SERVER_ADDR $server_addr;\n");
    b.put("            fastcgi_param SERVER_PORT $server_port;\n");
    b.put("            fastcgi_param SERVER_NAME $server_name;\n");
    b.put("            fastcgi_param PATH_INFO $fastcgi_path_info;\n");
    b.put("            fastcgi_param PATH_TRANSLATED $document_root$fastcgi_path_info;\n");
    b.put("            fastcgi_param REDIRECT_STATUS 200;\n");
    b.put("        }\n");
    b.put("        location ~ /\\.ht {\n");
    b.put("            deny all;\n");
    b.put("        }\n");
    b.put("    }\n");
    b.put("}\n");
    return b.data;
}

/// Renders the `listen` directive address: local-only hostnames bind to the
/// loopback proxy host, IPs bind directly, and other hostnames bind to the
/// bare port on all interfaces.
string renderNginxListenAddress(string host, int port) @safe
{
    auto trimmed = host.strip();
    if (isLocalOnlyServeHostname(trimmed))
        return joinHostPort(serveProxyHost, port);

    bool isLoopback = false;
    if (tryParseIP(trimmed, isLoopback))
        return joinHostPort(trimmed, port);

    return to!string(port);
}

/// Reports whether `host` is `localhost`, a `.localhost` subdomain, or a loopback IP.
bool isLocalOnlyServeHostname(string host) @safe
{
    auto trimmed = stripHostBrackets(host);
    auto lower = trimmed.toLower();
    if (lower == defaultServeHostname || lower.endsWith("." ~ defaultServeHostname))
        return true;

    bool isLoopback = false;
    if (tryParseIP(trimmed, isLoopback))
        return isLoopback;

    return false;
}

private void writeServeNginxMIMETypes(T)(ref T b, string indent)
{
    foreach (mimeType; serveStaticMIMETypes)
    {
        b.put(indent);
        b.put(mimeType.contentType);
        b.put(" ");
        b.put(mimeType.extensions.join(" "));
        b.put(";\n");
    }
}

/// Renders the `index` directive value, preferring the front controller when present.
string renderNginxIndexNames(AppLayout layout) @safe pure
{
    string[] indices;
    if (layout.frontControllerIndex.length > 0)
        indices ~= layout.frontControllerIndex;
    indices ~= "index.html";
    return indices.join(" ");
}

/// Renders the `try_files` fallback: the front controller when one exists, or `=404`.
string renderNginxTryFilesFallback(AppLayout layout) @safe pure
{
    if (layout.frontControllerWebPath.length == 0)
        return "=404";
    return layout.frontControllerWebPath ~ "$is_args$args";
}

/// Renders the `fastcgi_index` directive value.
string renderNginxFastCGIIndex(AppLayout layout) @safe pure
{
    if (layout.frontControllerIndex.length == 0)
        return "index.php";
    return layout.frontControllerIndex;
}

/// Quotes a filesystem path for use in nginx config.
string quoteNginxPath(string path) @safe pure
{
    return quoteGoString(toSlashPath(path));
}

/// Appends a trailing path separator, which nginx requires for its `-p` prefix argument.
string ensureServePrefix(string path) @safe pure
{
    if (path.endsWith(dirSeparator))
        return path;
    return path ~ dirSeparator;
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

private bool tryParseIP(string host, out bool isLoopback) @trusted
{
    version (Windows)
    {
        import core.sys.windows.winsock2 : sockaddr_in, sockaddr_in6;
    }
    else
    {
        import core.sys.posix.netinet.in_ : sockaddr_in, sockaddr_in6;
    }

    isLoopback = false;
    auto trimmed = stripHostBrackets(host);
    if (trimmed.length == 0)
        return false;
    try
    {
        auto parsed = parseAddress(trimmed);
        if (parsed.addressFamily == AddressFamily.INET && parsed.nameLen >= sockaddr_in.sizeof)
        {
            auto sa = cast(const(sockaddr_in)*) parsed.name;
            auto octets = (cast(const(ubyte)*) &sa.sin_addr)[0 .. 4];
            isLoopback = octets[0] == 127;
            return true;
        }
        if (parsed.addressFamily == AddressFamily.INET6 && parsed.nameLen >= sockaddr_in6.sizeof)
        {
            auto sa6 = cast(const(sockaddr_in6)*) parsed.name;
            auto octets = (cast(const(ubyte)*) &sa6.sin6_addr)[0 .. 16];
            bool loop = true;
            foreach (i; 0 .. 15)
            {
                if (octets[i] != 0)
                {
                    loop = false;
                    break;
                }
            }
            if (octets[15] != 1)
                loop = false;
            // Also recognize IPv4-mapped IPv6 loopback (::ffff:127.0.0.0/8), matching Go net.IP.IsLoopback.
            if (!loop)
            {
                bool mapped = true;
                foreach (i; 0 .. 10)
                {
                    if (octets[i] != 0)
                    {
                        mapped = false;
                        break;
                    }
                }
                if (mapped && octets[10] == 0xff && octets[11] == 0xff && octets[12] == 127)
                    loop = true;
            }
            isLoopback = loop;
            return true;
        }
    }
    catch (Exception) {}
    return false;
}

unittest
{
    import std.algorithm : canFind;

    assert(isLocalOnlyServeHostname("localhost"));
    assert(isLocalOnlyServeHostname("app.localhost"));
    assert(isLocalOnlyServeHostname("127.0.0.1"));
    assert(isLocalOnlyServeHostname("::1"));
    assert(!isLocalOnlyServeHostname("example.test"));

    assert(renderNginxListenAddress("app.localhost", 8080) == "127.0.0.1:8080");
    assert(renderNginxListenAddress("192.168.1.10", 8080) == "192.168.1.10:8080");
    assert(renderNginxListenAddress("0.0.0.0", 8080) == "0.0.0.0:8080");
    assert(renderNginxListenAddress("example.test", 8080) == "8080");

    auto cfg = renderNginxServeConfig(
        "app.localhost",
        8443,
        AppLayout("/srv/public", "index.php", "/index.php", "index.php"),
        "127.0.0.1:19000",
        ServeTLSConfig(true, "/tmp/cert.crt", "/tmp/cert.key")
    );
    assert(cfg.canFind("listen 127.0.0.1:8443 ssl;"));
    assert(cfg.canFind(`ssl_certificate "/tmp/cert.crt";`));
    assert(cfg.canFind(`fastcgi_pass 127.0.0.1:19000;`));
}
