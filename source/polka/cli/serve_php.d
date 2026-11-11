module polka.cli.serve_php;

// PHP built-in webserver serve lifecycle: foreground and background startup plus
// generation of the router script that serves static files and routes requests
// to the front controller.

import std.array : appender;
import std.file : mkdirRecurse, write;
import std.format : format;
import std.path : buildPath;

import polka.backend : Environment, Store, TextWriter;
import polka.cli.env_runtime : resolveRuntimeEnvironment;
import polka.cli.serve_runtime :
    openServeLog,
    serveLogFileName,
    serveRuntimeDir,
    serveStartupTimeout,
    serveStaticMIMETypes,
    stopServePID,
    waitForServeAddress;
import polka.cli.types :
    applyManagedPHPRuntimeConfig,
    callServeNow,
    executeTargetWithEnv,
    quoteGoString;
import polka.config : ServerTypePHP;
import polka.service :
    AppLayout,
    currentEnvList,
    ServeRuntimeState,
    spawnDetachedServiceProcess;
import polka.support.fs : cleanPath, currentOS, toSlashPath;

enum string phpServeRouterName = "php-router.php";

/// Test seams for the PHP built-in webserver lifecycle.
int delegate(TextWriter stdout, TextWriter stderr, Store store, string serverAddress, AppLayout layout) runPHPRuntimeServeFunc;
ServeRuntimeState delegate(Store store, Environment environment, string serverAddress, AppLayout layout) startBackgroundPHPRuntimeServe;

/// Calls `runPHPRuntimeServeFunc` or the default `runPHPRuntimeServe`.
int callRunPHPRuntimeServe(TextWriter stdout, TextWriter stderr, Store store, string serverAddress, AppLayout layout) @trusted
{
    if (runPHPRuntimeServeFunc !is null)
        return runPHPRuntimeServeFunc(stdout, stderr, store, serverAddress, layout);
    return runPHPRuntimeServe(stdout, stderr, store, serverAddress, layout);
}

/// Calls `startBackgroundPHPRuntimeServe` or `startPHPRuntimeServeInBackground`.
ServeRuntimeState callStartBackgroundPHPRuntimeServe(Store store, Environment environment, string serverAddress, AppLayout layout) @trusted
{
    if (startBackgroundPHPRuntimeServe !is null)
        return startBackgroundPHPRuntimeServe(store, environment, serverAddress, layout);
    return startPHPRuntimeServeInBackground(store, environment, serverAddress, layout);
}

/// Runs the PHP built-in webserver attached to the current terminal.
int runPHPRuntimeServe(TextWriter stdout, TextWriter stderr, Store store, string serverAddress, AppLayout layout) @trusted
{
    auto phpTarget = store.resolveTool("php");
    auto env = resolveRuntimeEnvironment(currentOS(), currentEnvList(), store);
    env = applyManagedPHPRuntimeConfig(currentOS(), env, phpTarget);

    auto runtimeDir = servePHPRuntimeDir(store.rootDir, layout.docroot);
    auto routerPath = preparePHPRuntimeServeRuntime(runtimeDir, layout);
    return executeTargetWithEnv(stdout, stderr, env, phpTarget, ["-S", serverAddress, "-t", layout.docroot, routerPath]);
}

/// Starts and records the managed PHP built-in webserver in the environment's default runtime dir.
ServeRuntimeState startPHPRuntimeServeInBackground(
    Store store,
    Environment environmentConfig,
    string serverAddress,
    AppLayout layout
) @trusted
{
    return startPHPRuntimeServeInBackgroundAt(
        store,
        environmentConfig,
        serverAddress,
        layout,
        serveRuntimeDir(store.rootDir, environmentConfig.name)
    );
}

/// Starts the managed PHP built-in webserver detached in `runtimeDir` and waits for readiness.
ServeRuntimeState startPHPRuntimeServeInBackgroundAt(
    Store store,
    Environment environmentConfig,
    string serverAddress,
    AppLayout layout,
    string runtimeDir
) @trusted
{
    auto phpTarget = store.resolveTool("php");
    auto env = resolveRuntimeEnvironment(currentOS(), currentEnvList(), store);
    env = applyManagedPHPRuntimeConfig(currentOS(), env, phpTarget);
    auto routerPath = preparePHPRuntimeServeRuntime(runtimeDir, layout);

    auto logPath = buildPath(runtimeDir, serveLogFileName);
    auto logFile = openServeLog(logPath);
    scope (exit)
    {
        if (logFile.isOpen)
            logFile.close();
    }

    int pid = 0;
    try
    {
        pid = spawnDetachedServiceProcess(phpTarget, ["-S", serverAddress, "-t", layout.docroot, routerPath], logFile, env);
    }
    catch (Exception e)
    {
        throw new Exception("start php webserver: " ~ e.msg);
    }

    try
    {
        waitForServeAddress(serverAddress, serveStartupTimeout);
    }
    catch (Exception e)
    {
        try { stopServePID(pid); } catch (Exception) {} // Best-effort teardown on startup timeout.
        throw new Exception(format("start php webserver on %s: %s (see %s)", serverAddress, e.msg, logPath));
    }

    ServeRuntimeState state;
    state.environmentName = environmentConfig.name;
    state.serverKind = ServerTypePHP;
    state.serverAddress = serverAddress;
    state.docroot = layout.docroot;
    state.runtimeDir = runtimeDir;
    state.logPath = logPath;
    state.routerPath = routerPath;
    state.primaryPID = pid;
    state.startedAt = callServeNow().toUTC();
    return state;
}

/// Returns a docroot-keyed runtime directory for foreground PHP serves so
/// concurrent serves of different docroots do not collide.
string servePHPRuntimeDir(string rootDir, string docroot) @trusted
{
    auto hash = fnv1a32(cast(const(ubyte)[]) cleanPath(docroot));
    return serveRuntimeDir(rootDir, format("php-%08x", hash));
}

/// Writes the generated router script into `runtimeDir` and returns its path.
string preparePHPRuntimeServeRuntime(string runtimeDir, AppLayout layout) @trusted
{
    try
    {
        mkdirRecurse(runtimeDir);
    }
    catch (Exception e)
    {
        throw new Exception("create serve runtime directory: " ~ e.msg);
    }

    auto routerPath = buildPath(runtimeDir, phpServeRouterName);
    auto router = renderPHPRuntimeRouter(layout);
    try
    {
        write(routerPath, router);
    }
    catch (Exception e)
    {
        throw new Exception("write php serve router: " ~ e.msg);
    }
    return routerPath;
}

/// Renders the router script used with `php -S`: serves static files using the
/// shared MIME table, guards against traversal outside docroot, and falls back
/// to the front controller.
string renderPHPRuntimeRouter(AppLayout layout) @safe pure
{
    auto b = appender!string();
    b.put("<?php\n");
    b.put("declare(strict_types=1);\n\n");
    b.put("$docroot = ");
    b.put(quoteGoString(toSlashPath(layout.docroot)));
    b.put(";\n");
    b.put("$frontControllerRelative = ");
    b.put(quoteGoString(toSlashPath(layout.frontControllerRelative)));
    b.put(";\n");
    b.put("$docrootReal = realpath($docroot);\n");
    b.put("if ($docrootReal === false) {\n");
    b.put("    header(($_SERVER['SERVER_PROTOCOL'] ?? 'HTTP/1.1') . ' 500 Internal Server Error');\n");
    b.put("    echo 'Document root does not exist.';\n");
    b.put("    return true;\n");
    b.put("}\n");
    b.put("$requestUri = $_SERVER['REQUEST_URI'] ?? '/';\n");
    b.put("$requestPath = (string) (parse_url($requestUri, PHP_URL_PATH) ?? '/');\n");
    b.put("$relativePath = ltrim(str_replace('/', DIRECTORY_SEPARATOR, rawurldecode($requestPath)), DIRECTORY_SEPARATOR);\n");
    b.put("$targetPath = $docroot;\n");
    b.put("if ($relativePath !== '') {\n");
    b.put("    $targetPath .= DIRECTORY_SEPARATOR . $relativePath;\n");
    b.put("}\n");
    b.put("$targetReal = realpath($targetPath);\n");
    b.put("$docrootPrefix = rtrim($docrootReal, DIRECTORY_SEPARATOR) . DIRECTORY_SEPARATOR;\n");
    b.put("if ($targetReal !== false && $targetReal !== $docrootReal && strncmp($targetReal, $docrootPrefix, strlen($docrootPrefix)) !== 0) {\n");
    b.put("    header(($_SERVER['SERVER_PROTOCOL'] ?? 'HTTP/1.1') . ' 403 Forbidden');\n");
    b.put("    return true;\n");
    b.put("}\n");
    b.put("if ($targetReal !== false && is_file($targetReal)) {\n");
    b.put("    $extension = strtolower(pathinfo($targetReal, PATHINFO_EXTENSION));\n");
    b.put("    if ($extension === 'php') {\n");
    b.put("        return false;\n");
    b.put("    }\n");
    b.put("    $mimeTypes = [\n");
    writeServePHPMIMETypes(b, "        ");
    b.put("    ];\n");
    b.put("    $mimeType = $mimeTypes[$extension] ?? 'application/octet-stream';\n");
    b.put("    header('Content-Type: ' . $mimeType);\n");
    b.put("    $size = filesize($targetReal);\n");
    b.put("    if ($size !== false) {\n");
    b.put("        header('Content-Length: ' . (string) $size);\n");
    b.put("    }\n");
    b.put("    if (strcasecmp($_SERVER['REQUEST_METHOD'] ?? 'GET', 'HEAD') !== 0) {\n");
    b.put("        readfile($targetReal);\n");
    b.put("    }\n");
    b.put("    return true;\n");
    b.put("}\n");
    b.put("$scriptRelative = $frontControllerRelative;\n");
    b.put("if (str_contains($requestPath, '.php')) {\n");
    b.put("    $path = $requestPath;\n");
    b.put("    do {\n");
    b.put("        $path = dirname($path);\n");
    b.put("        if (str_ends_with($path, '.php')) {\n");
    b.put("            $candidate = ltrim(str_replace('/', DIRECTORY_SEPARATOR, $path), DIRECTORY_SEPARATOR);\n");
    b.put("            $candidateReal = realpath($docroot . DIRECTORY_SEPARATOR . $candidate);\n");
    b.put("            if ($candidateReal !== false && is_file($candidateReal) && strncmp($candidateReal, $docrootPrefix, strlen($docrootPrefix)) === 0) {\n");
    b.put("                $scriptRelative = str_replace(DIRECTORY_SEPARATOR, '/', $candidate);\n");
    b.put("                break;\n");
    b.put("            }\n");
    b.put("        }\n");
    b.put("    } while ($path !== '/' && $path !== '.');\n");
    b.put("}\n");
    b.put("if ($scriptRelative === '') {\n");
    b.put("    return false;\n");
    b.put("}\n");
    b.put("$scriptAbsolute = $docroot . DIRECTORY_SEPARATOR . str_replace('/', DIRECTORY_SEPARATOR, $scriptRelative);\n");
    b.put("$scriptReal = realpath($scriptAbsolute);\n");
    b.put("if ($scriptReal === false || !is_file($scriptReal) || strncmp($scriptReal, $docrootPrefix, strlen($docrootPrefix)) !== 0) {\n");
    b.put("    return false;\n");
    b.put("}\n");
    b.put("$scriptWebPath = '/' . ltrim(str_replace(DIRECTORY_SEPARATOR, '/', $scriptRelative), '/');\n");
    b.put("$_SERVER['SCRIPT_FILENAME'] = $scriptReal;\n");
    b.put("$_SERVER['SCRIPT_NAME'] = $scriptWebPath;\n");
    b.put("$_SERVER['PHP_SELF'] = $scriptWebPath;\n");
    b.put("require $scriptReal;\n");
    b.put("return true;\n");
    return b.data;
}

private void writeServePHPMIMETypes(T)(ref T b, string indent)
{
    foreach (mimeType; serveStaticMIMETypes)
    {
        foreach (ext; mimeType.extensions)
        {
            b.put(indent);
            b.put("'");
            b.put(ext);
            b.put("' => '");
            b.put(mimeType.contentType);
            b.put("',\n");
        }
    }
}

/// Computes the 32-bit FNV-1a hash matching Go's `hash/fnv.New32a()`.
private uint fnv1a32(const(ubyte)[] data) @safe pure nothrow @nogc
{
    uint hash = 0x811c9dc5u;
    foreach (b; data)
    {
        hash ^= b;
        hash *= 0x01000193u;
    }
    return hash;
}

unittest
{
    import std.algorithm : canFind;

    // Known FNV-1a 32-bit test vector ("foobar" -> 0xbf9cf968).
    assert(fnv1a32(cast(const(ubyte)[]) "foobar") == 0xbf9cf968u);

    auto router = renderPHPRuntimeRouter(AppLayout("/var/www/public", "index.php", "/index.php", "index.php"));
    assert(router.canFind(`$docroot = "/var/www/public";`));
    assert(router.canFind(`'css' => 'text/css',`));
    assert(router.canFind(`require $scriptReal;`));
}
