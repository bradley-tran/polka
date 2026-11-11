module polka.cli.serve_apache;

// Apache serve lifecycle: foreground and background startup of `httpd` with
// its `php-cgi` upstream, plus `httpd.conf` generation.

import std.array : appender;
import std.conv : to;
import std.file : mkdirRecurse, write;
import std.format : format;
import std.path : baseName, buildPath, dirName;
import std.string : icmp, strip;

import polka.backend :
    Environment,
    primaryPHPVersion,
    Store,
    TextWriter;
import polka.cli.env_runtime : resolveRuntimeEnvironment;
import polka.cli.serve_nginx :
    renderNginxIndexNames,
    renderNginxListenAddress;
import polka.cli.serve_runtime :
    defaultServeHostname,
    openServeLog,
    reserveServeBackendAddress,
    resolvePHPCGITarget,
    serveLogFileName,
    serveProbeAddress,
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
import polka.config : ServerTypeApache;
import polka.service :
    AppLayout,
    currentEnvList,
    Endpoint,
    ServeRuntimeState,
    spawnDetachedServiceProcess,
    toolLogRoot;
import polka.support.fs : currentOS, toSlashPath;
import polka.support.net : joinHostPort, splitHostPort;

/// Test seams for the Apache webserver lifecycle.
int delegate(TextWriter stdout, TextWriter stderr, Store store, Environment environment, Endpoint endpoint, AppLayout layout) runApacheServeFunc;
ServeRuntimeState delegate(Store store, Environment environment, Endpoint endpoint, AppLayout layout) startBackgroundApacheServe;

/// Calls `runApacheServeFunc` or `runApacheServe`.
int callRunApacheServe(TextWriter stdout, TextWriter stderr, Store store, Environment envConfig, Endpoint endpoint, AppLayout layout) @trusted
{
    if (runApacheServeFunc !is null)
        return runApacheServeFunc(stdout, stderr, store, envConfig, endpoint, layout);
    return runApacheServe(stdout, stderr, store, envConfig, endpoint, layout);
}

/// Calls `startBackgroundApacheServe` or `startApacheServeInBackground`.
ServeRuntimeState callStartBackgroundApacheServe(Store store, Environment envConfig, Endpoint endpoint, AppLayout layout) @trusted
{
    if (startBackgroundApacheServe !is null)
        return startBackgroundApacheServe(store, envConfig, endpoint, layout);
    return startApacheServeInBackground(store, envConfig, endpoint, layout);
}

/// Runs Apache attached to the current terminal, backed by a `php-cgi`
/// upstream that is stopped when Apache exits.
int runApacheServe(
    TextWriter stdout,
    TextWriter stderr,
    Store store,
    Environment envConfig,
    Endpoint endpoint,
    AppLayout layout
) @trusted
{
    if (primaryPHPVersion(envConfig).strip().length == 0)
        throw new Exception(format(`environment "%s" defines apache but does not define a php version`, envConfig.name));

    auto phpTarget = store.resolveTool("php");
    auto phpCGITarget = resolvePHPCGITarget(phpTarget);
    auto apacheTarget = store.resolveTool(ServerTypeApache);
    auto apacheRoot = resolveApacheServerRoot(apacheTarget);
    auto env = resolveRuntimeEnvironment(currentOS(), currentEnvList(), store);
    env = applyManagedPHPRuntimeConfig(currentOS(), env, phpTarget);

    auto backendAddress = reserveServeBackendAddress();
    auto runtimeDir = toolLogRoot(store.rootDir, ServerTypeApache, envConfig.name);
    string configPath, phpLogPath;
    prepareApacheServeRuntime(store.cacheDir, runtimeDir, apacheRoot, endpoint, layout, backendAddress, configPath, phpLogPath);

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

    return executeTargetWithEnv(stdout, stderr, env, apacheTarget, apacheServeArgs(apacheRoot, configPath));
}

/// Starts and records the managed Apache webserver with its `php-cgi` upstream.
ServeRuntimeState startApacheServeInBackground(
    Store store,
    Environment envConfig,
    Endpoint endpoint,
    AppLayout layout
) @trusted
{
    return startApacheServeInBackgroundAt(
        store,
        envConfig,
        endpoint,
        layout,
        toolLogRoot(store.rootDir, ServerTypeApache, envConfig.name)
    );
}

/// Starts the managed Apache webserver and its `php-cgi` upstream inside `runtimeDir`.
ServeRuntimeState startApacheServeInBackgroundAt(
    Store store,
    Environment envConfig,
    Endpoint endpoint,
    AppLayout layout,
    string runtimeDir
) @trusted
{
    if (primaryPHPVersion(envConfig).strip().length == 0)
        throw new Exception(format(`environment "%s" defines apache but does not define a php version`, envConfig.name));

    auto phpTarget = store.resolveTool("php");
    auto phpCGITarget = resolvePHPCGITarget(phpTarget);
    auto apacheTarget = store.resolveTool(ServerTypeApache);
    auto apacheRoot = resolveApacheServerRoot(apacheTarget);
    auto env = resolveRuntimeEnvironment(currentOS(), currentEnvList(), store);
    env = applyManagedPHPRuntimeConfig(currentOS(), env, phpTarget);

    auto backendAddress = reserveServeBackendAddress();
    string configPath, phpLogPath;
    prepareApacheServeRuntime(store.cacheDir, runtimeDir, apacheRoot, endpoint, layout, backendAddress, configPath, phpLogPath);

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

    auto apacheLogPath = buildPath(runtimeDir, serveLogFileName);
    auto apacheLogFile = openServeLog(apacheLogPath);
    scope (exit)
    {
        if (apacheLogFile.isOpen)
            apacheLogFile.close();
    }

    int apachePID = 0;
    try
    {
        apachePID = spawnDetachedServiceProcess(apacheTarget, apacheServeArgs(apacheRoot, configPath), apacheLogFile, env);
    }
    catch (Exception e)
    {
        try { stopServePID(phpPID); } catch (Exception) {}
        throw new Exception("start apache webserver: " ~ e.msg);
    }

    try
    {
        waitForServeAddress(serveProbeAddress(endpoint.address), serveStartupTimeout);
    }
    catch (Exception e)
    {
        try { stopServePID(apachePID); } catch (Exception) {}
        try { stopServePID(phpPID); } catch (Exception) {}
        throw new Exception(format("start apache webserver on %s: %s (see %s and %s)", endpoint.address, e.msg, apacheLogPath, phpLogPath));
    }

    ServeRuntimeState state;
    state.environmentName = envConfig.name;
    state.serverKind = ServerTypeApache;
    state.serverScheme = endpoint.scheme;
    state.serverAddress = endpoint.address;
    state.docroot = layout.docroot;
    state.runtimeDir = runtimeDir;
    state.logPath = apacheLogPath;
    state.backendLogPath = phpLogPath;
    state.configPath = configPath;
    state.primaryPID = apachePID;
    state.secondaryPID = phpPID;
    state.startedAt = callServeNow().toUTC();
    return state;
}

/// Builds the `httpd` arguments for a managed foreground run.
string[] apacheServeArgs(string apacheRoot, string configPath) @safe pure
{
    return ["-d", apacheRoot, "-f", configPath, "-DFOREGROUND"];
}

/// Derives the Apache server root from the `httpd` executable path, stepping
/// out of a `bin` directory when present.
string resolveApacheServerRoot(string apacheTarget) @safe pure
{
    auto dir = dirName(apacheTarget.strip());
    if (icmp(baseName(dir), "bin") == 0)
        return dirName(dir);
    return dir;
}

/// Creates Apache runtime directories, provisions TLS material when needed,
/// and writes the generated `httpd.conf`.
void prepareApacheServeRuntime(
    string cacheDir,
    string runtimeDir,
    string apacheRoot,
    Endpoint endpoint,
    AppLayout layout,
    string backendAddress,
    out string configPath,
    out string phpLogPath
) @trusted
{
    auto logsDir = buildPath(runtimeDir, "logs");
    try
    {
        mkdirRecurse(logsDir);
    }
    catch (Exception e)
    {
        throw new Exception("create serve runtime directory: " ~ e.msg);
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

    configPath = buildPath(runtimeDir, "httpd.conf");
    phpLogPath = buildPath(runtimeDir, "php.log");
    auto config = renderApacheServeConfig(apacheRoot, runtimeDir, host, port, layout, backendAddress, tlsConfig);
    try
    {
        write(configPath, config);
    }
    catch (Exception e)
    {
        throw new Exception("write apache config: " ~ e.msg);
    }
}

/// Renders a self-contained `httpd.conf` that proxies PHP requests to the
/// `php-cgi` upstream and rewrites unmatched requests to the front controller.
string renderApacheServeConfig(
    string apacheRoot,
    string runtimeDir,
    string host,
    int port,
    AppLayout layout,
    string backendAddress,
    ServeTLSConfig tlsConfig
) @safe
{
    auto listenAddress = renderNginxListenAddress(host, port);
    auto serverHost = host.strip();
    if (serverHost.length == 0)
        serverHost = defaultServeHostname;
    auto serverName = joinHostPort(serverHost, port);
    auto modulesDir = buildPath(apacheRoot, "modules");
    auto logsDir = buildPath(runtimeDir, "logs");

    auto b = appender!string();
    b.put("ServerRoot ");
    b.put(quoteApachePath(apacheRoot));
    b.put("\n");
    b.put("PidFile ");
    b.put(quoteApachePath(buildPath(runtimeDir, "httpd.pid")));
    b.put("\n");
    b.put("ServerName ");
    b.put(serverName);
    b.put("\n");
    b.put("Listen ");
    b.put(listenAddress);
    b.put("\n");
    b.put("LogLevel warn\n");
    b.put("LogFormat \"%h %l %u %t \\\"%r\\\" %>s %b\" common\n");
    b.put("EnableSendfile Off\n");
    writeApacheMPMModule(b, modulesDir);
    writeApacheLoadModule(b, modulesDir, "authz_core_module", "mod_authz_core.so");
    writeApacheLoadModule(b, modulesDir, "authz_host_module", "mod_authz_host.so");
    writeApacheLoadModule(b, modulesDir, "dir_module", "mod_dir.so");
    writeApacheLoadModule(b, modulesDir, "mime_module", "mod_mime.so");
    writeApacheLoadModule(b, modulesDir, "log_config_module", "mod_log_config.so");
    writeApacheLoadModule(b, modulesDir, "rewrite_module", "mod_rewrite.so");
    writeApacheLoadModule(b, modulesDir, "proxy_module", "mod_proxy.so");
    writeApacheLoadModule(b, modulesDir, "proxy_fcgi_module", "mod_proxy_fcgi.so");
    if (tlsConfig.enabled)
    {
        writeApacheLoadModule(b, modulesDir, "socache_shmcb_module", "mod_socache_shmcb.so");
        writeApacheLoadModule(b, modulesDir, "ssl_module", "mod_ssl.so");
    }
    b.put("\n");
    b.put("AddDefaultCharset Off\n");
    writeServeApacheMIMETypes(b, "");
    b.put("\n");
    b.put("<VirtualHost *:");
    b.put(to!string(port));
    b.put(">\n");
    b.put("    ServerName ");
    b.put(serverName);
    b.put("\n");
    b.put("    DocumentRoot ");
    b.put(quoteApachePath(layout.docroot));
    b.put("\n");
    b.put("    ErrorLog ");
    b.put(quoteApachePath(buildPath(logsDir, "error.log")));
    b.put("\n");
    b.put("    CustomLog ");
    b.put(quoteApachePath(buildPath(logsDir, "access.log")));
    b.put(" common\n");
    b.put("    DirectoryIndex ");
    b.put(renderApacheDirectoryIndex(layout));
    b.put("\n");
    if (tlsConfig.enabled)
    {
        b.put("    SSLEngine on\n");
        b.put("    SSLCertificateFile ");
        b.put(quoteApachePath(tlsConfig.certificatePath));
        b.put("\n");
        b.put("    SSLCertificateKeyFile ");
        b.put(quoteApachePath(tlsConfig.certificateKeyPath));
        b.put("\n");
        b.put("    SSLProtocol all -SSLv3 -TLSv1 -TLSv1.1\n");
    }
    b.put("    <Directory ");
    b.put(quoteApachePath(layout.docroot));
    b.put(">\n");
    b.put("        Options FollowSymLinks\n");
    b.put("        AllowOverride All\n");
    b.put("        Require all granted\n");
    if (layout.frontControllerRelative.length > 0)
    {
        b.put("        RewriteEngine On\n");
        b.put("        RewriteCond %{REQUEST_FILENAME} !-f\n");
        b.put("        RewriteCond %{REQUEST_FILENAME} !-d\n");
        b.put("        RewriteRule ^ ");
        b.put(toSlashPath(layout.frontControllerRelative));
        b.put(" [QSA,L]\n");
    }
    b.put("    </Directory>\n");
    b.put("    <FilesMatch \"^\\.ht\">\n");
    b.put("        Require all denied\n");
    b.put("    </FilesMatch>\n");
    b.put("    ProxyFCGIBackendType GENERIC\n");
    b.put("    ProxyFCGISetEnvIf \"true\" SCRIPT_FILENAME \"%{reqenv:DOCUMENT_ROOT}%{reqenv:SCRIPT_NAME}\"\n");
    b.put("    <FilesMatch \"\\.php$\">\n");
    b.put("        SetHandler \"proxy:fcgi://");
    b.put(backendAddress);
    b.put("/\"\n");
    b.put("    </FilesMatch>\n");
    b.put("</VirtualHost>\n");
    return b.data;
}

private void writeApacheLoadModule(T)(ref T b, string modulesDir, string modName, string fileName)
{
    b.put("LoadModule ");
    b.put(modName);
    b.put(" ");
    b.put(quoteApachePath(buildPath(modulesDir, fileName)));
    b.put("\n");
}

private void writeApacheMPMModule(T)(ref T b, string modulesDir)
{
    if (currentOS() == "windows")
        return;
    writeApacheLoadModule(b, modulesDir, "mpm_event_module", "mod_mpm_event.so");
}

private void writeServeApacheMIMETypes(T)(ref T b, string indent)
{
    foreach (mimeType; serveStaticMIMETypes)
    {
        b.put(indent);
        b.put("AddType ");
        b.put(mimeType.contentType);
        foreach (ext; mimeType.extensions)
        {
            b.put(" .");
            b.put(ext);
        }
        b.put("\n");
    }
}

/// Renders the `DirectoryIndex` value; matches nginx's index directive.
string renderApacheDirectoryIndex(AppLayout layout) @safe pure
{
    return renderNginxIndexNames(layout);
}

/// Quotes a filesystem path for use in `httpd.conf`.
string quoteApachePath(string path) @safe pure
{
    return quoteGoString(toSlashPath(path));
}

unittest
{
    import std.algorithm : canFind;

    assert(resolveApacheServerRoot("/opt/apache/bin/httpd") == "/opt/apache");
    assert(resolveApacheServerRoot("/opt/apache/httpd") == "/opt/apache");

    auto conf = renderApacheServeConfig(
        "/opt/apache",
        "/tmp/run/apache/demo",
        "site.localhost",
        8443,
        AppLayout("/var/www/public", "index.php", "/index.php", "index.php"),
        "127.0.0.1:19001",
        ServeTLSConfig(true, "/tmp/cert.crt", "/tmp/cert.key")
    );
    assert(conf.canFind(`Listen 127.0.0.1:8443`));
    assert(conf.canFind(`SSLCertificateFile "/tmp/cert.crt"`));
    assert(conf.canFind(`SetHandler "proxy:fcgi://127.0.0.1:19001/"`));
}
