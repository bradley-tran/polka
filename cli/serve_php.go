package cli

// This file contains the PHP built-in webserver serve lifecycle: foreground
// and background startup plus generation of the router script that serves
// static files and routes requests to the front controller.

import (
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"polka/backend"
	"polka/config"
)

const phpServeRouterName = "php-router.php"

// Test seams for the PHP built-in webserver lifecycle.
var (
	runPHPRuntimeServeFunc         = runPHPRuntimeServe
	startBackgroundPHPRuntimeServe = startPHPRuntimeServeInBackground
)

// runPHPRuntimeServe runs the PHP built-in webserver attached to the current terminal.
func runPHPRuntimeServe(stdout, stderr io.Writer, store backend.Store, serverAddress string, layout serveAppLayout) (int, error) {
	phpTarget, err := store.ResolveTool("php")
	if err != nil {
		return 0, err
	}
	env, err := resolveRuntimeEnvironment(runtime.GOOS, os.Environ(), store)
	if err != nil {
		return 0, err
	}
	env, err = applyManagedPHPRuntimeConfig(runtime.GOOS, env, phpTarget)
	if err != nil {
		return 0, err
	}
	runtimeDir := servePHPRuntimeDir(store.RootDir, layout.Docroot)
	routerPath, err := preparePHPRuntimeServeRuntime(runtimeDir, layout)
	if err != nil {
		return 0, err
	}

	return executeTargetWithEnv(stdout, stderr, env, phpTarget, []string{"-S", serverAddress, "-t", layout.Docroot, routerPath})
}

// startPHPRuntimeServeInBackground starts and records the managed PHP built-in webserver.
func startPHPRuntimeServeInBackground(store backend.Store, environment backend.Environment, serverAddress string, layout serveAppLayout) (serveRuntimeState, error) {
	return startPHPRuntimeServeInBackgroundAt(store, environment, serverAddress, layout, serveRuntimeDir(store.RootDir, environment.Name))
}

func startPHPRuntimeServeInBackgroundAt(store backend.Store, environment backend.Environment, serverAddress string, layout serveAppLayout, runtimeDir string) (serveRuntimeState, error) {
	phpTarget, err := store.ResolveTool("php")
	if err != nil {
		return serveRuntimeState{}, err
	}
	env, err := resolveRuntimeEnvironment(runtime.GOOS, os.Environ(), store)
	if err != nil {
		return serveRuntimeState{}, err
	}
	env, err = applyManagedPHPRuntimeConfig(runtime.GOOS, env, phpTarget)
	if err != nil {
		return serveRuntimeState{}, err
	}
	routerPath, err := preparePHPRuntimeServeRuntime(runtimeDir, layout)
	if err != nil {
		return serveRuntimeState{}, err
	}
	logPath := filepath.Join(runtimeDir, serveLogFileName)
	logFile, err := openServeLog(logPath)
	if err != nil {
		return serveRuntimeState{}, err
	}

	command, err := prepareCommand(phpTarget, []string{"-S", serverAddress, "-t", layout.Docroot, routerPath})
	if err != nil {
		_ = logFile.Close()
		return serveRuntimeState{}, err
	}
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = env

	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return serveRuntimeState{}, fmt.Errorf("start php webserver: %w", err)
	}

	if err := waitForServeAddress(serverAddress, serveStartupTimeout); err != nil {
		stopServeProcess(command.Process)
		_ = logFile.Close()
		return serveRuntimeState{}, fmt.Errorf("start php webserver on %s: %w (see %s)", serverAddress, err, logPath)
	}

	state := serveRuntimeState{
		EnvironmentName: environment.Name,
		ServerKind:      config.ServerTypePHP,
		ServerAddress:   serverAddress,
		Docroot:         layout.Docroot,
		RuntimeDir:      runtimeDir,
		LogPath:         logPath,
		RouterPath:      routerPath,
		PrimaryPID:      command.Process.Pid,
		StartedAt:       serveNowFunc().UTC(),
	}
	_ = logFile.Close()
	_ = command.Process.Release()

	return state, nil
}

// servePHPRuntimeDir returns a docroot-keyed runtime directory for foreground
// PHP serves, so concurrent serves of different docroots do not collide.
func servePHPRuntimeDir(rootDir, docroot string) string {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(filepath.Clean(docroot)))

	return serveRuntimeDir(rootDir, fmt.Sprintf("php-%08x", hasher.Sum32()))
}

// preparePHPRuntimeServeRuntime writes the generated router script into the runtime directory.
func preparePHPRuntimeServeRuntime(runtimeDir string, layout serveAppLayout) (string, error) {
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return "", fmt.Errorf("create serve runtime directory: %w", err)
	}

	routerPath := filepath.Join(runtimeDir, phpServeRouterName)
	router := renderPHPRuntimeRouter(layout)
	if err := os.WriteFile(routerPath, router, 0o644); err != nil {
		return "", fmt.Errorf("write php serve router: %w", err)
	}

	return routerPath, nil
}

// renderPHPRuntimeRouter renders the router script used with `php -S`: it
// serves static files with the shared MIME table, guards against path
// traversal outside the docroot, and falls back to the front controller.
func renderPHPRuntimeRouter(layout serveAppLayout) []byte {
	var builder strings.Builder
	builder.WriteString("<?php\n")
	builder.WriteString("declare(strict_types=1);\n\n")
	builder.WriteString("$docroot = ")
	builder.WriteString(strconv.Quote(filepath.ToSlash(layout.Docroot)))
	builder.WriteString(";\n")
	builder.WriteString("$frontControllerRelative = ")
	builder.WriteString(strconv.Quote(filepath.ToSlash(layout.FrontControllerRelative)))
	builder.WriteString(";\n")
	builder.WriteString("$docrootReal = realpath($docroot);\n")
	builder.WriteString("if ($docrootReal === false) {\n")
	builder.WriteString("    header(($_SERVER['SERVER_PROTOCOL'] ?? 'HTTP/1.1') . ' 500 Internal Server Error');\n")
	builder.WriteString("    echo 'Document root does not exist.';\n")
	builder.WriteString("    return true;\n")
	builder.WriteString("}\n")
	builder.WriteString("$requestUri = $_SERVER['REQUEST_URI'] ?? '/';\n")
	builder.WriteString("$requestPath = (string) (parse_url($requestUri, PHP_URL_PATH) ?? '/');\n")
	builder.WriteString("$relativePath = ltrim(str_replace('/', DIRECTORY_SEPARATOR, rawurldecode($requestPath)), DIRECTORY_SEPARATOR);\n")
	builder.WriteString("$targetPath = $docroot;\n")
	builder.WriteString("if ($relativePath !== '') {\n")
	builder.WriteString("    $targetPath .= DIRECTORY_SEPARATOR . $relativePath;\n")
	builder.WriteString("}\n")
	builder.WriteString("$targetReal = realpath($targetPath);\n")
	builder.WriteString("$docrootPrefix = rtrim($docrootReal, DIRECTORY_SEPARATOR) . DIRECTORY_SEPARATOR;\n")
	builder.WriteString("if ($targetReal !== false && $targetReal !== $docrootReal && strncmp($targetReal, $docrootPrefix, strlen($docrootPrefix)) !== 0) {\n")
	builder.WriteString("    header(($_SERVER['SERVER_PROTOCOL'] ?? 'HTTP/1.1') . ' 403 Forbidden');\n")
	builder.WriteString("    return true;\n")
	builder.WriteString("}\n")
	builder.WriteString("if ($targetReal !== false && is_file($targetReal)) {\n")
	builder.WriteString("    $extension = strtolower(pathinfo($targetReal, PATHINFO_EXTENSION));\n")
	builder.WriteString("    if ($extension === 'php') {\n")
	builder.WriteString("        return false;\n")
	builder.WriteString("    }\n")
	builder.WriteString("    $mimeTypes = [\n")
	writeServePHPMIMETypes(&builder, "        ")
	builder.WriteString("    ];\n")
	builder.WriteString("    $mimeType = $mimeTypes[$extension] ?? 'application/octet-stream';\n")
	builder.WriteString("    header('Content-Type: ' . $mimeType);\n")
	builder.WriteString("    $size = filesize($targetReal);\n")
	builder.WriteString("    if ($size !== false) {\n")
	builder.WriteString("        header('Content-Length: ' . (string) $size);\n")
	builder.WriteString("    }\n")
	builder.WriteString("    if (strcasecmp($_SERVER['REQUEST_METHOD'] ?? 'GET', 'HEAD') !== 0) {\n")
	builder.WriteString("        readfile($targetReal);\n")
	builder.WriteString("    }\n")
	builder.WriteString("    return true;\n")
	builder.WriteString("}\n")
	builder.WriteString("$scriptRelative = $frontControllerRelative;\n")
	builder.WriteString("if (str_contains($requestPath, '.php')) {\n")
	builder.WriteString("    $path = $requestPath;\n")
	builder.WriteString("    do {\n")
	builder.WriteString("        $path = dirname($path);\n")
	builder.WriteString("        if (str_ends_with($path, '.php')) {\n")
	builder.WriteString("            $candidate = ltrim(str_replace('/', DIRECTORY_SEPARATOR, $path), DIRECTORY_SEPARATOR);\n")
	builder.WriteString("            $candidateReal = realpath($docroot . DIRECTORY_SEPARATOR . $candidate);\n")
	builder.WriteString("            if ($candidateReal !== false && is_file($candidateReal) && strncmp($candidateReal, $docrootPrefix, strlen($docrootPrefix)) === 0) {\n")
	builder.WriteString("                $scriptRelative = str_replace(DIRECTORY_SEPARATOR, '/', $candidate);\n")
	builder.WriteString("                break;\n")
	builder.WriteString("            }\n")
	builder.WriteString("        }\n")
	builder.WriteString("    } while ($path !== '/' && $path !== '.');\n")
	builder.WriteString("}\n")
	builder.WriteString("if ($scriptRelative === '') {\n")
	builder.WriteString("    return false;\n")
	builder.WriteString("}\n")
	builder.WriteString("$scriptAbsolute = $docroot . DIRECTORY_SEPARATOR . str_replace('/', DIRECTORY_SEPARATOR, $scriptRelative);\n")
	builder.WriteString("$scriptReal = realpath($scriptAbsolute);\n")
	builder.WriteString("if ($scriptReal === false || !is_file($scriptReal) || strncmp($scriptReal, $docrootPrefix, strlen($docrootPrefix)) !== 0) {\n")
	builder.WriteString("    return false;\n")
	builder.WriteString("}\n")
	builder.WriteString("$scriptWebPath = '/' . ltrim(str_replace(DIRECTORY_SEPARATOR, '/', $scriptRelative), '/');\n")
	builder.WriteString("$_SERVER['SCRIPT_FILENAME'] = $scriptReal;\n")
	builder.WriteString("$_SERVER['SCRIPT_NAME'] = $scriptWebPath;\n")
	builder.WriteString("$_SERVER['PHP_SELF'] = $scriptWebPath;\n")
	builder.WriteString("require $scriptReal;\n")
	builder.WriteString("return true;\n")

	return []byte(builder.String())
}

// writeServePHPMIMETypes renders the shared MIME table as a PHP array literal body.
func writeServePHPMIMETypes(builder *strings.Builder, indent string) {
	for _, mimeType := range serveStaticMIMETypes {
		for _, extension := range mimeType.Extensions {
			builder.WriteString(indent)
			builder.WriteString("'")
			builder.WriteString(extension)
			builder.WriteString("' => '")
			builder.WriteString(mimeType.ContentType)
			builder.WriteString("',\n")
		}
	}
}
