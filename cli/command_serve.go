package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"polka/backend"
)

const (
	defaultServeHostname = "localhost"
	defaultServePort     = 8000
	servePollInterval    = 100 * time.Millisecond
	serveStartupTimeout  = 5 * time.Second
	serveRuntimeRoot     = "run"
	serveRuntimeSubdir   = "serve"
	serveStateFileName   = "state.json"
	serveLogFileName     = "serve.log"
	phpServeRouterName   = "php-router.php"
	serveProxyHost       = "127.0.0.1"
	serveShutdownTimeout = 5 * time.Second
)

var (
	runPHPRuntimeServeFunc         = runPHPRuntimeServe
	runNginxServeFunc              = runNginxServe
	startBackgroundPHPRuntimeServe = startPHPRuntimeServeInBackground
	startBackgroundNginxServe      = startNginxServeInBackground
	stopServeRuntimeFunc           = stopServeRuntime
	pingServeAddressFunc           = pingServeAddress
	serveNowFunc                   = time.Now
)

type serveCommandInput struct {
	Docroot string
	Server  string
	Watch   bool
}

type serveRuntimeState struct {
	EnvironmentName string    `json:"environment"`
	ServerKind      string    `json:"server_kind"`
	ServerAddress   string    `json:"server_address"`
	Docroot         string    `json:"docroot"`
	RuntimeDir      string    `json:"runtime_dir,omitempty"`
	LogPath         string    `json:"log_path,omitempty"`
	BackendLogPath  string    `json:"backend_log_path,omitempty"`
	ConfigPath      string    `json:"config_path,omitempty"`
	RouterPath      string    `json:"router_path,omitempty"`
	PrimaryPID      int       `json:"primary_pid"`
	SecondaryPID    int       `json:"secondary_pid,omitempty"`
	StartedAt       time.Time `json:"started_at"`
}

type serveAppLayout struct {
	Docroot                 string
	FrontControllerRelative string
	FrontControllerWebPath  string
	FrontControllerIndex    string
}

type serveStaticMIMEType struct {
	ContentType string
	Extensions  []string
}

var serveStaticMIMETypes = []serveStaticMIMEType{
	{ContentType: "text/html", Extensions: []string{"html", "htm", "shtml"}},
	{ContentType: "text/css", Extensions: []string{"css"}},
	{ContentType: "text/xml", Extensions: []string{"xml"}},
	{ContentType: "image/gif", Extensions: []string{"gif"}},
	{ContentType: "image/jpeg", Extensions: []string{"jpeg", "jpg"}},
	{ContentType: "application/javascript", Extensions: []string{"js", "mjs"}},
	{ContentType: "application/atom+xml", Extensions: []string{"atom"}},
	{ContentType: "application/rss+xml", Extensions: []string{"rss"}},
	{ContentType: "text/mathml", Extensions: []string{"mml"}},
	{ContentType: "text/plain", Extensions: []string{"txt"}},
	{ContentType: "text/vnd.sun.j2me.app-descriptor", Extensions: []string{"jad"}},
	{ContentType: "text/vnd.wap.wml", Extensions: []string{"wml"}},
	{ContentType: "text/x-component", Extensions: []string{"htc"}},
	{ContentType: "image/avif", Extensions: []string{"avif"}},
	{ContentType: "image/png", Extensions: []string{"png"}},
	{ContentType: "image/svg+xml", Extensions: []string{"svg", "svgz"}},
	{ContentType: "image/tiff", Extensions: []string{"tif", "tiff"}},
	{ContentType: "image/webp", Extensions: []string{"webp"}},
	{ContentType: "image/x-icon", Extensions: []string{"ico"}},
	{ContentType: "font/woff", Extensions: []string{"woff"}},
	{ContentType: "font/woff2", Extensions: []string{"woff2"}},
	{ContentType: "application/json", Extensions: []string{"json", "map"}},
	{ContentType: "application/pdf", Extensions: []string{"pdf"}},
	{ContentType: "application/wasm", Extensions: []string{"wasm"}},
	{ContentType: "application/xml", Extensions: []string{"xsl", "xslt"}},
	{ContentType: "application/zip", Extensions: []string{"zip"}},
	{ContentType: "application/octet-stream", Extensions: []string{"bin", "exe", "dll"}},
}

func newServeCommand(ctx *commandContext) *cobra.Command {
	var input serveCommandInput

	cmd := &cobra.Command{
		Use:  "serve [docroot]",
		Args: maximumArgsError("serve accepts at most one docroot", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			input.Docroot = ""
			if len(args) > 0 {
				input.Docroot = strings.TrimSpace(args[0])
			}
			input.Server = strings.TrimSpace(input.Server)
			if cmd.Flags().Changed("server") && input.Server == "" {
				return &statusError{code: 1, err: fmt.Errorf("--server requires a non-empty value")}
			}

			ctx.exitCode = runServe(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, input)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.Server, "server", "", "server address override in HOST:PORT form")
	cmd.Flags().BoolVar(&input.Watch, "watch", false, "keep the webserver attached to the current terminal")
	configureCommand(cmd, serveUsage)

	return cmd
}

func runServe(stdout, stderr io.Writer, store backend.Store, input serveCommandInput) int {
	current, err := store.Current()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if current == nil {
		fmt.Fprintln(stderr, "error: no active environment selected")
		return 1
	}

	serverAddress, err := resolveServeAddress(current.Server, input.Server)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	docroot, err := resolveServeDocroot(store.ProjectDir, current.Docroot, input.Docroot)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	layout, err := resolveServeAppLayout(docroot)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if current.Database != nil && strings.TrimSpace(current.Database.Engine) != "" {
		resolved := dbResolvedEnvironment{Environment: *current, Database: current.Database}
		if _, _, err := ensureManagedDatabaseStarted(store, resolved); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}

	liveState, err := loadLiveServeState(store.RootDir, current.Name)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if liveState != nil {
		if serveStateMatches(*liveState, serverAddress, layout.Docroot, current.NginxVersion != "") {
			fmt.Fprintf(stdout, "Webserver for environment %q is already running at http://%s.\n", current.Name, liveState.ServerAddress)
			return 0
		}

		fmt.Fprintf(stderr, "error: environment %q already has a running %s at http://%s; run `polka stop` before starting a different webserver\n", current.Name, serveRuntimeLabel(liveState.ServerKind), liveState.ServerAddress)
		return 1
	}

	if input.Watch {
		var exitCode int
		if current.NginxVersion != "" {
			_, _ = fmt.Fprintf(stdout, "nginx webserver started at http://%s\n", serverAddress)
			exitCode, err = runNginxServeFunc(stdout, stderr, store, *current, serverAddress, layout)
		} else {
			exitCode, err = runPHPRuntimeServeFunc(stdout, stderr, store, serverAddress, layout)
		}
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}

		return exitCode
	}

	var startedState serveRuntimeState
	if current.NginxVersion != "" {
		startedState, err = startBackgroundNginxServe(store, *current, serverAddress, layout)
	} else {
		startedState, err = startBackgroundPHPRuntimeServe(store, *current, serverAddress, layout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if strings.TrimSpace(startedState.EnvironmentName) == "" {
		startedState.EnvironmentName = current.Name
	}
	if strings.TrimSpace(startedState.ServerKind) == "" {
		startedState.ServerKind = desiredServeKind(current.NginxVersion != "")
	}
	if strings.TrimSpace(startedState.ServerAddress) == "" {
		startedState.ServerAddress = serverAddress
	}
	if strings.TrimSpace(startedState.Docroot) == "" {
		startedState.Docroot = layout.Docroot
	}
	if startedState.StartedAt.IsZero() {
		startedState.StartedAt = serveNowFunc().UTC()
	}
	if err := writeServeState(serveStatePath(store.RootDir, current.Name), startedState); err != nil {
		_ = stopServeRuntimeFunc(startedState)
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Started %s for environment %q at http://%s.\n", serveRuntimeLabel(startedState.ServerKind), current.Name, serverAddress)
	fmt.Fprintln(stdout, "Run `polka stop` to stop it.")
	return 0
}

func runPHPRuntimeServe(stdout, stderr io.Writer, store backend.Store, serverAddress string, layout serveAppLayout) (int, error) {
	phpTarget, err := store.ResolveTool("php")
	if err != nil {
		return 0, err
	}
	env, err := resolveRuntimeEnvironment(runtime.GOOS, os.Environ(), store)
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

func startPHPRuntimeServeInBackground(store backend.Store, environment backend.Environment, serverAddress string, layout serveAppLayout) (serveRuntimeState, error) {
	phpTarget, err := store.ResolveTool("php")
	if err != nil {
		return serveRuntimeState{}, err
	}
	env, err := resolveRuntimeEnvironment(runtime.GOOS, os.Environ(), store)
	if err != nil {
		return serveRuntimeState{}, err
	}
	runtimeDir := serveRuntimeDir(store.RootDir, environment.Name)
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
		ServerKind:      desiredServeKind(false),
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

func runNginxServe(stdout, stderr io.Writer, store backend.Store, environment backend.Environment, serverAddress string, layout serveAppLayout) (int, error) {
	if strings.TrimSpace(environment.PHPVersion) == "" {
		return 0, fmt.Errorf("environment %q defines nginx but does not define a php version", environment.Name)
	}

	phpTarget, err := store.ResolveTool("php")
	if err != nil {
		return 0, err
	}
	phpCGITarget, err := resolvePHPCGITarget(phpTarget)
	if err != nil {
		return 0, err
	}
	nginxTarget, err := store.ResolveTool("nginx")
	if err != nil {
		return 0, err
	}
	env, err := resolveRuntimeEnvironment(runtime.GOOS, os.Environ(), store)
	if err != nil {
		return 0, err
	}

	backendAddress, err := reserveServeBackendAddress()
	if err != nil {
		return 0, err
	}

	runtimeDir := serveRuntimeDir(store.RootDir, environment.Name)
	configPath, phpLogPath, err := prepareNginxServeRuntime(runtimeDir, serverAddress, layout, backendAddress)
	if err != nil {
		return 0, err
	}

	phpLogFile, err := os.OpenFile(phpLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open php serve log: %w", err)
	}
	defer phpLogFile.Close()

	phpCommand, err := prepareCommand(phpCGITarget, []string{"-b", backendAddress})
	if err != nil {
		return 0, err
	}
	phpCommand.Stdout = phpLogFile
	phpCommand.Stderr = phpLogFile
	phpCommand.Env = env

	if err := phpCommand.Start(); err != nil {
		return 0, fmt.Errorf("start php-cgi upstream: %w", err)
	}
	defer stopServeProcess(phpCommand.Process)

	if err := waitForServeAddress(backendAddress, serveStartupTimeout); err != nil {
		return 0, fmt.Errorf("start php-cgi upstream on %s: %w (see %s)", backendAddress, err, phpLogPath)
	}

	nginxArgs := []string{"-p", ensureServePrefix(runtimeDir), "-c", filepath.Base(configPath), "-g", "daemon off;"}
	return executeTargetWithEnv(stdout, stderr, env, nginxTarget, nginxArgs)
}

func startNginxServeInBackground(store backend.Store, environment backend.Environment, serverAddress string, layout serveAppLayout) (serveRuntimeState, error) {
	if strings.TrimSpace(environment.PHPVersion) == "" {
		return serveRuntimeState{}, fmt.Errorf("environment %q defines nginx but does not define a php version", environment.Name)
	}

	phpTarget, err := store.ResolveTool("php")
	if err != nil {
		return serveRuntimeState{}, err
	}
	phpCGITarget, err := resolvePHPCGITarget(phpTarget)
	if err != nil {
		return serveRuntimeState{}, err
	}
	nginxTarget, err := store.ResolveTool("nginx")
	if err != nil {
		return serveRuntimeState{}, err
	}
	env, err := resolveRuntimeEnvironment(runtime.GOOS, os.Environ(), store)
	if err != nil {
		return serveRuntimeState{}, err
	}

	backendAddress, err := reserveServeBackendAddress()
	if err != nil {
		return serveRuntimeState{}, err
	}

	runtimeDir := serveRuntimeDir(store.RootDir, environment.Name)
	configPath, phpLogPath, err := prepareNginxServeRuntime(runtimeDir, serverAddress, layout, backendAddress)
	if err != nil {
		return serveRuntimeState{}, err
	}

	phpLogFile, err := openServeLog(phpLogPath)
	if err != nil {
		return serveRuntimeState{}, err
	}
	defer phpLogFile.Close()

	phpCommand, err := prepareCommand(phpCGITarget, []string{"-b", backendAddress})
	if err != nil {
		return serveRuntimeState{}, err
	}
	phpCommand.Stdout = phpLogFile
	phpCommand.Stderr = phpLogFile
	phpCommand.Env = env

	if err := phpCommand.Start(); err != nil {
		return serveRuntimeState{}, fmt.Errorf("start php-cgi upstream: %w", err)
	}
	phpPID := phpCommand.Process.Pid

	if err := waitForServeAddress(backendAddress, serveStartupTimeout); err != nil {
		stopServeProcess(phpCommand.Process)
		return serveRuntimeState{}, fmt.Errorf("start php-cgi upstream on %s: %w (see %s)", backendAddress, err, phpLogPath)
	}

	nginxLogPath := filepath.Join(runtimeDir, serveLogFileName)
	nginxLogFile, err := openServeLog(nginxLogPath)
	if err != nil {
		stopServeProcess(phpCommand.Process)
		return serveRuntimeState{}, err
	}

	nginxArgs := []string{"-p", ensureServePrefix(runtimeDir), "-c", filepath.Base(configPath), "-g", "daemon off;"}
	nginxCommand, err := prepareCommand(nginxTarget, nginxArgs)
	if err != nil {
		_ = nginxLogFile.Close()
		stopServeProcess(phpCommand.Process)
		return serveRuntimeState{}, err
	}
	nginxCommand.Stdout = nginxLogFile
	nginxCommand.Stderr = nginxLogFile
	nginxCommand.Env = env

	if err := nginxCommand.Start(); err != nil {
		_ = nginxLogFile.Close()
		stopServeProcess(phpCommand.Process)
		return serveRuntimeState{}, fmt.Errorf("start nginx webserver: %w", err)
	}

	if err := waitForServeAddress(serverAddress, serveStartupTimeout); err != nil {
		stopServeProcess(nginxCommand.Process)
		stopServeProcess(phpCommand.Process)
		_ = nginxLogFile.Close()
		return serveRuntimeState{}, fmt.Errorf("start nginx webserver on %s: %w (see %s and %s)", serverAddress, err, nginxLogPath, phpLogPath)
	}

	state := serveRuntimeState{
		EnvironmentName: environment.Name,
		ServerKind:      desiredServeKind(true),
		ServerAddress:   serverAddress,
		Docroot:         layout.Docroot,
		RuntimeDir:      runtimeDir,
		LogPath:         nginxLogPath,
		BackendLogPath:  phpLogPath,
		ConfigPath:      configPath,
		PrimaryPID:      nginxCommand.Process.Pid,
		SecondaryPID:    phpPID,
		StartedAt:       serveNowFunc().UTC(),
	}
	_ = nginxLogFile.Close()
	_ = phpCommand.Process.Release()
	_ = nginxCommand.Process.Release()

	return state, nil
}

func resolvePHPCGITarget(phpTarget string) (string, error) {
	trimmed := strings.TrimSpace(phpTarget)
	if trimmed == "" {
		return "", fmt.Errorf("php target cannot be empty")
	}

	searchDirs := []string{filepath.Dir(trimmed)}
	if strings.EqualFold(filepath.Base(searchDirs[0]), "bin") {
		searchDirs = append(searchDirs, filepath.Dir(searchDirs[0]))
	}

	candidates := make([]string, 0, len(searchDirs)*3)
	for _, dir := range searchDirs {
		if runtime.GOOS == "windows" {
			candidates = append(candidates,
				filepath.Join(dir, "php-cgi.exe"),
				filepath.Join(dir, "php-cgi.cmd"),
				filepath.Join(dir, "php-cgi.bat"),
			)
			continue
		}

		candidates = append(candidates,
			filepath.Join(dir, "php-cgi"),
			filepath.Join(dir, "bin", "php-cgi"),
		)
	}

	for _, candidate := range candidates {
		fileInfo, err := os.Stat(candidate)
		switch {
		case err == nil && !fileInfo.IsDir():
			return candidate, nil
		case err == nil && fileInfo.IsDir():
			continue
		case os.IsNotExist(err):
			continue
		case err != nil:
			return "", fmt.Errorf("stat php-cgi candidate %s: %w", candidate, err)
		}
	}

	return "", fmt.Errorf("php-cgi executable was not found next to %s", phpTarget)
}

func resolveServeAddress(config *backend.ServerConfig, override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		host, port, err := splitServerAddress(override)
		if err != nil {
			return "", err
		}

		return net.JoinHostPort(host, strconv.Itoa(port)), nil
	}

	host := defaultServeHostname
	port := defaultServePort
	if config != nil {
		if strings.TrimSpace(config.Hostname) != "" {
			host = strings.TrimSpace(config.Hostname)
		}
		if config.Port != 0 {
			port = config.Port
		}
	}
	if err := validateServeHostAndPort(host, port); err != nil {
		return "", err
	}

	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func reserveServeBackendAddress() (string, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(serveProxyHost, "0"))
	if err != nil {
		return "", fmt.Errorf("reserve php upstream address: %w", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", fmt.Errorf("release php upstream address: %w", err)
	}

	return address, nil
}

func serveRuntimeDir(rootDir, environmentName string) string {
	name := strings.TrimSpace(environmentName)
	if name == "" {
		name = "current"
	}

	return filepath.Join(rootDir, serveRuntimeRoot, serveRuntimeSubdir, name)
}

func serveStatePath(rootDir, environmentName string) string {
	return filepath.Join(serveRuntimeDir(rootDir, environmentName), serveStateFileName)
}

func servePHPRuntimeDir(rootDir, docroot string) string {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(filepath.Clean(docroot)))

	return serveRuntimeDir(rootDir, fmt.Sprintf("php-%08x", hasher.Sum32()))
}

func resolveServeAppLayout(docroot string) (serveAppLayout, error) {
	layout := serveAppLayout{Docroot: docroot}
	frontControllerPath := filepath.Join(docroot, "index.php")
	fileInfo, err := os.Stat(frontControllerPath)
	switch {
	case err == nil && !fileInfo.IsDir():
		layout.FrontControllerRelative = "index.php"
		layout.FrontControllerWebPath = "/index.php"
		layout.FrontControllerIndex = "index.php"
	case err == nil && fileInfo.IsDir():
		return serveAppLayout{}, fmt.Errorf("front controller %q is a directory", frontControllerPath)
	case os.IsNotExist(err):
		return layout, nil
	case err != nil:
		return serveAppLayout{}, fmt.Errorf("stat front controller: %w", err)
	}

	return layout, nil
}

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

func prepareNginxServeRuntime(runtimeDir, serverAddress string, layout serveAppLayout, backendAddress string) (string, string, error) {
	tempRoot := filepath.Join(runtimeDir, "temp")
	logsDir := filepath.Join(runtimeDir, "logs")
	tempDirs := []string{
		filepath.Join(tempRoot, "client_body"),
		filepath.Join(tempRoot, "proxy"),
		filepath.Join(tempRoot, "fastcgi"),
		filepath.Join(tempRoot, "uwsgi"),
		filepath.Join(tempRoot, "scgi"),
	}
	for _, path := range append([]string{runtimeDir, logsDir}, tempDirs...) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return "", "", fmt.Errorf("create serve runtime directory: %w", err)
		}
	}

	host, portText, err := net.SplitHostPort(serverAddress)
	if err != nil {
		return "", "", fmt.Errorf("parse serve address %q: %w", serverAddress, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", "", fmt.Errorf("parse serve port %q: %w", portText, err)
	}

	configPath := filepath.Join(runtimeDir, "nginx.conf")
	phpLogPath := filepath.Join(runtimeDir, "php.log")
	config := renderNginxServeConfig(host, port, layout, backendAddress)
	if err := os.WriteFile(configPath, config, 0o644); err != nil {
		return "", "", fmt.Errorf("write nginx config: %w", err)
	}

	return configPath, phpLogPath, nil
}

func renderNginxServeConfig(host string, port int, layout serveAppLayout, backendAddress string) []byte {
	listenAddress := renderNginxListenAddress(host, port)
	serverName := strings.TrimSpace(host)
	if serverName == "" {
		serverName = "_"
	}

	var builder strings.Builder
	builder.WriteString("worker_processes 1;\n")
	builder.WriteString("pid nginx.pid;\n")
	builder.WriteString("events {\n")
	builder.WriteString("    worker_connections 1024;\n")
	builder.WriteString("}\n")
	builder.WriteString("http {\n")
	builder.WriteString("    access_log logs/access.log;\n")
	builder.WriteString("    error_log logs/error.log notice;\n")
	builder.WriteString("    types {\n")
	writeServeNginxMIMETypes(&builder, "        ")
	builder.WriteString("    }\n")
	builder.WriteString("    default_type application/octet-stream;\n")
	builder.WriteString("    client_body_temp_path temp/client_body;\n")
	builder.WriteString("    proxy_temp_path temp/proxy;\n")
	builder.WriteString("    fastcgi_temp_path temp/fastcgi;\n")
	builder.WriteString("    uwsgi_temp_path temp/uwsgi;\n")
	builder.WriteString("    scgi_temp_path temp/scgi;\n")
	builder.WriteString("    server {\n")
	builder.WriteString("        listen ")
	builder.WriteString(listenAddress)
	builder.WriteString(";\n")
	builder.WriteString("        server_name ")
	builder.WriteString(serverName)
	builder.WriteString(";\n")
	builder.WriteString("        index ")
	builder.WriteString(renderNginxIndexNames(layout))
	builder.WriteString(";\n")
	builder.WriteString("        root ")
	builder.WriteString(quoteNginxPath(layout.Docroot))
	builder.WriteString(";\n")
	builder.WriteString("        location / {\n")
	builder.WriteString("            try_files $uri $uri/ ")
	builder.WriteString(renderNginxTryFilesFallback(layout))
	builder.WriteString(";\n")
	builder.WriteString("        }\n")
	builder.WriteString("        location ~ \\.php(?:$|/) {\n")
	builder.WriteString("            fastcgi_split_path_info ^(.+?\\.php)(/.*)$;\n")
	builder.WriteString("            try_files $fastcgi_script_name =404;\n")
	builder.WriteString("            fastcgi_pass ")
	builder.WriteString(backendAddress)
	builder.WriteString(";\n")
	builder.WriteString("            fastcgi_index ")
	builder.WriteString(renderNginxFastCGIIndex(layout))
	builder.WriteString(";\n")
	builder.WriteString("            fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;\n")
	builder.WriteString("            fastcgi_param SCRIPT_NAME $fastcgi_script_name;\n")
	builder.WriteString("            fastcgi_param DOCUMENT_ROOT $document_root;\n")
	builder.WriteString("            fastcgi_param QUERY_STRING $query_string;\n")
	builder.WriteString("            fastcgi_param REQUEST_METHOD $request_method;\n")
	builder.WriteString("            fastcgi_param CONTENT_TYPE $content_type;\n")
	builder.WriteString("            fastcgi_param CONTENT_LENGTH $content_length;\n")
	builder.WriteString("            fastcgi_param REQUEST_URI $request_uri;\n")
	builder.WriteString("            fastcgi_param DOCUMENT_URI $document_uri;\n")
	builder.WriteString("            fastcgi_param SERVER_PROTOCOL $server_protocol;\n")
	builder.WriteString("            fastcgi_param REQUEST_SCHEME $scheme;\n")
	builder.WriteString("            fastcgi_param HTTPS $https if_not_empty;\n")
	builder.WriteString("            fastcgi_param GATEWAY_INTERFACE CGI/1.1;\n")
	builder.WriteString("            fastcgi_param SERVER_SOFTWARE nginx/$nginx_version;\n")
	builder.WriteString("            fastcgi_param REMOTE_ADDR $remote_addr;\n")
	builder.WriteString("            fastcgi_param REMOTE_PORT $remote_port;\n")
	builder.WriteString("            fastcgi_param REMOTE_USER $remote_user;\n")
	builder.WriteString("            fastcgi_param SERVER_ADDR $server_addr;\n")
	builder.WriteString("            fastcgi_param SERVER_PORT $server_port;\n")
	builder.WriteString("            fastcgi_param SERVER_NAME $server_name;\n")
	builder.WriteString("            fastcgi_param PATH_INFO $fastcgi_path_info;\n")
	builder.WriteString("            fastcgi_param PATH_TRANSLATED $document_root$fastcgi_path_info;\n")
	builder.WriteString("            fastcgi_param REDIRECT_STATUS 200;\n")
	builder.WriteString("        }\n")
	builder.WriteString("        location ~ /\\.ht {\n")
	builder.WriteString("            deny all;\n")
	builder.WriteString("        }\n")
	builder.WriteString("    }\n")
	builder.WriteString("}\n")

	return []byte(builder.String())
}

func renderNginxListenAddress(host string, port int) string {
	trimmed := strings.TrimSpace(host)
	if strings.EqualFold(trimmed, defaultServeHostname) {
		return net.JoinHostPort(serveProxyHost, strconv.Itoa(port))
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		return net.JoinHostPort(trimmed, strconv.Itoa(port))
	}

	return strconv.Itoa(port)
}

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

func writeServeNginxMIMETypes(builder *strings.Builder, indent string) {
	for _, mimeType := range serveStaticMIMETypes {
		builder.WriteString(indent)
		builder.WriteString(mimeType.ContentType)
		builder.WriteString(" ")
		builder.WriteString(strings.Join(mimeType.Extensions, " "))
		builder.WriteString(";\n")
	}
}

func renderNginxIndexNames(layout serveAppLayout) string {
	indices := []string{}
	if layout.FrontControllerIndex != "" {
		indices = append(indices, layout.FrontControllerIndex)
	}
	indices = append(indices, "index.html")

	return strings.Join(indices, " ")
}

func renderNginxTryFilesFallback(layout serveAppLayout) string {
	if layout.FrontControllerWebPath == "" {
		return "=404"
	}

	return layout.FrontControllerWebPath + "$is_args$args"
}

func renderNginxFastCGIIndex(layout serveAppLayout) string {
	if layout.FrontControllerIndex == "" {
		return "index.php"
	}

	return layout.FrontControllerIndex
}

func quoteNginxPath(path string) string {
	return strconv.Quote(filepath.ToSlash(path))
}

func ensureServePrefix(path string) string {
	if strings.HasSuffix(path, string(os.PathSeparator)) {
		return path
	}

	return path + string(os.PathSeparator)
}

func waitForServeAddress(address string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pingServeAddressFunc(address) {
			return nil
		}

		time.Sleep(servePollInterval)
	}

	return fmt.Errorf("server did not start listening within %s", timeout)
}

func pingServeAddress(address string) bool {
	connection, err := net.DialTimeout("tcp", address, servePollInterval)
	if err != nil {
		return false
	}
	_ = connection.Close()

	return true
}

func loadLiveServeState(rootDir, environmentName string) (*serveRuntimeState, error) {
	path := serveStatePath(rootDir, environmentName)
	state, err := loadServeState(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(state.ServerAddress) != "" && pingServeAddressFunc(state.ServerAddress) {
		return state, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale serve state: %w", err)
	}

	return nil, nil
}

func loadServeState(path string) (*serveRuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state serveRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode serve state %s: %w", path, err)
	}

	return &state, nil
}

func writeServeState(path string, state serveRuntimeState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create serve state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode serve state: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write serve state %s: %w", path, err)
	}

	return nil
}

func stopManagedServe(store backend.Store, environmentName string) (serveRuntimeState, bool, error) {
	state, err := loadLiveServeState(store.RootDir, environmentName)
	if err != nil {
		return serveRuntimeState{}, false, err
	}
	if state == nil {
		return serveRuntimeState{}, true, nil
	}
	if err := stopServeRuntimeFunc(*state); err != nil {
		return serveRuntimeState{}, false, err
	}
	if err := os.Remove(serveStatePath(store.RootDir, environmentName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return serveRuntimeState{}, false, fmt.Errorf("remove serve state: %w", err)
	}

	return *state, false, nil
}

func stopServeRuntime(state serveRuntimeState) error {
	if err := stopServePID(state.PrimaryPID); err != nil {
		return err
	}
	if state.SecondaryPID != 0 && state.SecondaryPID != state.PrimaryPID {
		if err := stopServePID(state.SecondaryPID); err != nil {
			return err
		}
	}
	if strings.TrimSpace(state.ServerAddress) == "" {
		return nil
	}

	deadline := time.Now().Add(serveShutdownTimeout)
	for time.Now().Before(deadline) {
		if !pingServeAddressFunc(state.ServerAddress) {
			return nil
		}
		time.Sleep(servePollInterval)
	}
	if !pingServeAddressFunc(state.ServerAddress) {
		return nil
	}

	return fmt.Errorf("webserver did not stop listening on %s within %s", state.ServerAddress, serveShutdownTimeout)
}

func stopServePID(pid int) error {
	if pid <= 0 {
		return nil
	}
	if runtime.GOOS == "windows" {
		output, err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").CombinedOutput()
		if err == nil || isMissingServeProcessOutput(string(output)) {
			return nil
		}

		return fmt.Errorf("stop serve process %d: %w (%s)", pid, err, strings.TrimSpace(string(output)))
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := process.Kill(); err != nil && !isMissingServeProcessOutput(err.Error()) {
		return fmt.Errorf("stop serve process %d: %w", pid, err)
	}
	_ = process.Release()

	return nil
}

func openServeLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create serve log directory: %w", err)
	}
	logFile, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open serve log %s: %w", path, err)
	}

	return logFile, nil
}

func desiredServeKind(useNginx bool) string {
	if useNginx {
		return "nginx"
	}

	return "php"
}

func serveRuntimeLabel(kind string) string {
	trimmed := strings.TrimSpace(kind)
	if trimmed == "" {
		return "webserver"
	}

	return trimmed + " webserver"
}

func serveStateMatches(state serveRuntimeState, serverAddress, docroot string, useNginx bool) bool {
	return strings.EqualFold(strings.TrimSpace(state.ServerKind), desiredServeKind(useNginx)) &&
		strings.EqualFold(strings.TrimSpace(state.ServerAddress), strings.TrimSpace(serverAddress)) &&
		filepath.Clean(state.Docroot) == filepath.Clean(docroot)
}

func isMissingServeProcessOutput(output string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(output))
	return trimmed == "" ||
		strings.Contains(trimmed, "not found") ||
		strings.Contains(trimmed, "no running instance") ||
		strings.Contains(trimmed, "process already finished") ||
		strings.Contains(trimmed, "no such process")
}

func stopServeProcess(process *os.Process) {
	if process == nil {
		return
	}

	_ = stopServePID(process.Pid)
	_ = process.Release()
}

func splitServerAddress(value string) (string, int, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return "", 0, fmt.Errorf("invalid --server value %q: use HOST:PORT", value)
	}

	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, fmt.Errorf("invalid --server value %q: use HOST:PORT", value)
	}
	if err := validateServeHostAndPort(host, port); err != nil {
		return "", 0, err
	}

	return host, port, nil
}

func validateServeHostAndPort(host string, port int) error {
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("server hostname cannot be empty")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("server port must be between 1 and 65535")
	}

	return nil
}

func resolveServeDocroot(projectDir, configuredDocroot, overrideDocroot string) (string, error) {
	trimmed := strings.TrimSpace(overrideDocroot)
	if trimmed == "" {
		trimmed = strings.TrimSpace(configuredDocroot)
	}
	if trimmed == "" {
		return "", fmt.Errorf("serve requires a docroot argument or environments.<name>.docroot in polka.yaml")
	}

	resolved := filepath.Clean(trimmed)
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(projectDir, resolved)
	}

	fileInfo, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat docroot: %w", err)
	}
	if !fileInfo.IsDir() {
		return "", fmt.Errorf("docroot %q is not a directory", trimmed)
	}

	return resolved, nil
}
