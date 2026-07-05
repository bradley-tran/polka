package cli

// This file contains the webserver-agnostic serve runtime machinery shared by
// every managed webserver kind: runtime/state directory layout, serve state
// persistence, startup probing, process shutdown, log handling, and the PHP
// runtime environment overlays. Per-server lifecycle lives in serve_php.go,
// serve_nginx.go, serve_apache.go, and serve_frankenphp.go; the serve command
// itself lives in command_serve.go.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"polka/backend"
	"polka/service"
)

const (
	servePollInterval    = 100 * time.Millisecond
	serveStartupTimeout  = 5 * time.Second
	serveRuntimeRoot     = "run"
	serveRuntimeSubdir   = "serve"
	serveStateFileName   = "state.json"
	serveLogFileName     = "serve.log"
	managedOpenSSLConfig = "extras/ssl/openssl.cnf"
	serveProxyHost       = "127.0.0.1"
	serveShutdownTimeout = 5 * time.Second
)

// Test seams for the shared serve runtime helpers.
var (
	stopServeRuntimeFunc = stopServeRuntime
	pingServeAddressFunc = pingServeAddress
	serveNowFunc         = time.Now
)

type serveRuntimeState = service.ServeRuntimeState
type serveAppLayout = service.AppLayout
type serverEndpoint = service.Endpoint

// serveStaticMIMEType maps one content type to the file extensions served with it.
type serveStaticMIMEType struct {
	ContentType string
	Extensions  []string
}

// serveStaticMIMETypes is the shared static-file MIME table rendered into the
// PHP router, nginx config, and Apache config so all webservers agree.
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

// applyManagedPHPRuntimeConfig overlays generated PHP runtime files when present.
func applyManagedPHPRuntimeConfig(goos string, env []string, phpTarget string) ([]string, error) {
	updated := env

	phpIniPath := filepath.Join(filepath.Dir(phpTarget), "php.ini")
	exists, err := regularFileExists(phpIniPath)
	if err != nil {
		return nil, fmt.Errorf("stat managed php.ini %s: %w", phpIniPath, err)
	}
	if exists {
		updated = replaceEnvValue(goos, updated, "PHPRC", phpIniPath)
	}

	opensslConfigPath, exists, err := managedPHPOpenSSLConfigPath(phpTarget)
	if err != nil {
		return nil, err
	}
	if exists {
		updated = replaceEnvValue(goos, updated, "OPENSSL_CONF", opensslConfigPath)
	}

	return updated, nil
}

// managedPHPOpenSSLConfigPath locates the managed openssl.cnf bundled next to
// (or one directory above) the PHP executable, when it exists.
func managedPHPOpenSSLConfigPath(phpTarget string) (string, bool, error) {
	phpDir := filepath.Dir(phpTarget)
	candidates := []string{
		filepath.Join(phpDir, filepath.FromSlash(managedOpenSSLConfig)),
		filepath.Join(filepath.Dir(phpDir), filepath.FromSlash(managedOpenSSLConfig)),
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		cleanCandidate := filepath.Clean(candidate)
		if seen[cleanCandidate] {
			continue
		}
		seen[cleanCandidate] = true

		exists, err := regularFileExists(cleanCandidate)
		if err != nil {
			return "", false, fmt.Errorf("stat managed openssl config %s: %w", cleanCandidate, err)
		}
		if exists {
			return cleanCandidate, true, nil
		}
	}

	return "", false, nil
}

// resolvePHPCGITarget locates the php-cgi executable installed next to the PHP CLI.
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

// serveProbeAddress rewrites subdomain-of-localhost hostnames to the loopback
// proxy host so startup and shutdown probes work without DNS support.
func serveProbeAddress(address string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return address
	}
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	if strings.HasSuffix(strings.ToLower(trimmed), "."+defaultServeHostname) {
		return net.JoinHostPort(serveProxyHost, port)
	}

	return address
}

// reserveServeBackendAddress grabs a free loopback port for the php-cgi upstream.
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

// serveRuntimeDir returns the per-environment serve runtime directory.
func serveRuntimeDir(rootDir, environmentName string) string {
	name := strings.TrimSpace(environmentName)
	if name == "" {
		name = "current"
	}

	return filepath.Join(rootDir, serveRuntimeRoot, serveRuntimeSubdir, name)
}

// serveStatePath returns the serve state file path for an environment.
func serveStatePath(rootDir, environmentName string) string {
	return filepath.Join(serveRuntimeDir(rootDir, environmentName), serveStateFileName)
}

// resolveServeAppLayout inspects the docroot for a front controller and
// captures the derived paths used by webserver config generation.
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

// waitForServeAddress polls until the address accepts TCP connections or the
// timeout elapses.
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

// pingServeAddress reports whether the address currently accepts TCP connections.
func pingServeAddress(address string) bool {
	connection, err := net.DialTimeout("tcp", address, servePollInterval)
	if err != nil {
		return false
	}
	_ = connection.Close()

	return true
}

// loadWebServerState loads the recorded serve state for an environment and
// clears it when the recorded server is no longer listening.
func loadWebServerState(rootDir, environmentName string) (*serveRuntimeState, error) {
	path := serveStatePath(rootDir, environmentName)
	state, err := loadServeState(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(state.ServerAddress) != "" && pingServeAddressFunc(serveStateProbeAddress(*state)) {
		return state, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale serve state: %w", err)
	}

	return nil, nil
}

// loadServeState reads and decodes one serve state file.
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

// writeServeState persists the serve state file for a background webserver.
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

// stopManagedServe stops the recorded background webserver for an environment.
// The boolean result reports whether there was nothing to stop.
func stopManagedServe(store backend.Store, environmentName string) (serveRuntimeState, bool, error) {
	state, err := loadWebServerState(store.RootDir, environmentName)
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

// stopServeRuntime terminates the recorded serve processes and waits for the
// server address to stop accepting connections.
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
		if !pingServeAddressFunc(serveStateProbeAddress(state)) {
			return nil
		}
		time.Sleep(servePollInterval)
	}
	if !pingServeAddressFunc(serveStateProbeAddress(state)) {
		return nil
	}

	return fmt.Errorf("webserver did not stop listening on %s within %s", state.ServerAddress, serveShutdownTimeout)
}

// stopServePID force-terminates one process (and its children on Windows),
// treating already-gone processes as success.
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

// openServeLog creates (or truncates) a serve log file, creating parent directories.
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

// serveRuntimeLabel renders a human-readable webserver label for messages.
func serveRuntimeLabel(kind string) string {
	trimmed := strings.TrimSpace(kind)
	if trimmed == "" {
		return "webserver"
	}

	return trimmed + " webserver"
}

// serveStateMatches reports whether a recorded serve state matches the
// requested server kind, endpoint, and docroot.
func serveStateMatches(state serveRuntimeState, endpoint serverEndpoint, docroot, serverType string) bool {
	stateScheme := strings.TrimSpace(state.ServerScheme)
	if stateScheme == "" {
		stateScheme = "http"
	}
	return strings.EqualFold(strings.TrimSpace(state.ServerKind), strings.TrimSpace(serverType)) &&
		strings.EqualFold(stateScheme, strings.TrimSpace(endpoint.Scheme)) &&
		strings.EqualFold(strings.TrimSpace(state.ServerAddress), strings.TrimSpace(endpoint.Address)) &&
		filepath.Clean(state.Docroot) == filepath.Clean(docroot)
}

// serveStateURL renders the URL recorded in a serve state.
func serveStateURL(state serveRuntimeState) string {
	return serverEndpointURL(serverEndpoint{
		Scheme:  strings.TrimSpace(state.ServerScheme),
		Address: strings.TrimSpace(state.ServerAddress),
	})
}

// serveStateProbeAddress returns the probe address for a recorded serve state.
func serveStateProbeAddress(state serveRuntimeState) string {
	return serveProbeAddress(strings.TrimSpace(state.ServerAddress))
}

// isMissingServeProcessOutput reports whether process termination output
// indicates the process was already gone.
func isMissingServeProcessOutput(output string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(output))
	return trimmed == "" ||
		strings.Contains(trimmed, "not found") ||
		strings.Contains(trimmed, "no running instance") ||
		strings.Contains(trimmed, "process already finished") ||
		strings.Contains(trimmed, "no such process")
}

// stopServeProcess force-terminates a process handle, ignoring failures; it is
// used for cleanup after partial startup.
func stopServeProcess(process *os.Process) {
	if process == nil {
		return
	}

	_ = stopServePID(process.Pid)
	_ = process.Release()
}
