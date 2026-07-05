package cli

// This file contains the nginx serve lifecycle: foreground and background
// startup of nginx with its php-cgi upstream, plus nginx config generation
// (framework-provided or the generic front-controller config).

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"polka/backend"
	"polka/config"
	"polka/plugins"
	"polka/service"
)

// Test seams for the nginx webserver lifecycle.
var (
	runNginxServeFunc         = runNginxServe
	startBackgroundNginxServe = startNginxServeInBackground
)

// runNginxServe runs nginx attached to the current terminal, backed by a
// php-cgi upstream that is stopped when nginx exits.
func runNginxServe(stdout, stderr io.Writer, store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout) (int, error) {
	if backend.PrimaryPHPVersion(environment) == "" {
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
	env, err = applyManagedPHPRuntimeConfig(runtime.GOOS, env, phpTarget)
	if err != nil {
		return 0, err
	}

	backendAddress, err := reserveServeBackendAddress()
	if err != nil {
		return 0, err
	}

	runtimeDir := service.ToolLogRoot(store.RootDir, "nginx", environment.Name)
	configPath, phpLogPath, err := prepareNginxServeRuntime(store.CacheDir, runtimeDir, environment, endpoint, layout, backendAddress)
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

// startNginxServeInBackground starts and records the managed nginx webserver
// with its php-cgi upstream.
func startNginxServeInBackground(store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout) (serveRuntimeState, error) {
	return startNginxServeInBackgroundAt(store, environment, endpoint, layout, service.ToolLogRoot(store.RootDir, "nginx", environment.Name))
}

func startNginxServeInBackgroundAt(store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout, runtimeDir string) (serveRuntimeState, error) {
	if backend.PrimaryPHPVersion(environment) == "" {
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
	env, err = applyManagedPHPRuntimeConfig(runtime.GOOS, env, phpTarget)
	if err != nil {
		return serveRuntimeState{}, err
	}

	backendAddress, err := reserveServeBackendAddress()
	if err != nil {
		return serveRuntimeState{}, err
	}

	configPath, phpLogPath, err := prepareNginxServeRuntime(store.CacheDir, runtimeDir, environment, endpoint, layout, backendAddress)
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

	if err := waitForServeAddress(serveProbeAddress(endpoint.Address), serveStartupTimeout); err != nil {
		stopServeProcess(nginxCommand.Process)
		stopServeProcess(phpCommand.Process)
		_ = nginxLogFile.Close()
		return serveRuntimeState{}, fmt.Errorf("start nginx webserver on %s: %w (see %s and %s)", endpoint.Address, err, nginxLogPath, phpLogPath)
	}

	state := serveRuntimeState{
		EnvironmentName: environment.Name,
		ServerKind:      config.ServerTypeNginx,
		ServerScheme:    endpoint.Scheme,
		ServerAddress:   endpoint.Address,
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

// prepareNginxServeRuntime creates the nginx runtime directory tree, provisions
// TLS material when needed, and writes the generated nginx config. It returns
// the config path and the php-cgi upstream log path.
func prepareNginxServeRuntime(cacheDir, runtimeDir string, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout, backendAddress string) (string, string, error) {
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

	host, portText, err := net.SplitHostPort(endpoint.Address)
	if err != nil {
		return "", "", fmt.Errorf("parse serve address %q: %w", endpoint.Address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", "", fmt.Errorf("parse serve port %q: %w", portText, err)
	}
	tlsConfig := serveTLSConfig{}
	if endpoint.HTTPS {
		certPath, keyPath, err := ensureGlobalTLSCertificateRuntimeKey(cacheDir, runtimeDir, host)
		if err != nil {
			return "", "", err
		}
		tlsConfig = serveTLSConfig{
			Enabled:            true,
			CertificatePath:    certPath,
			CertificateKeyPath: keyPath,
		}
	}

	configPath := filepath.Join(runtimeDir, "nginx.conf")
	phpLogPath := filepath.Join(runtimeDir, "php.log")
	config, err := renderFrameworkNginxServeConfig(environment, host, port, layout, backendAddress, tlsConfig)
	if err != nil {
		return "", "", err
	}
	if config == nil {
		config = renderNginxServeConfig(host, port, layout, backendAddress, tlsConfig)
	}
	if err := os.WriteFile(configPath, config, 0o644); err != nil {
		return "", "", fmt.Errorf("write nginx config: %w", err)
	}

	return configPath, phpLogPath, nil
}

// renderFrameworkNginxServeConfig asks the environment's framework plugin for
// an nginx config. It returns nil bytes when no framework is configured or the
// plugin does not handle nginx config generation.
func renderFrameworkNginxServeConfig(environment backend.Environment, host string, port int, layout serveAppLayout, backendAddress string, tlsConfig serveTLSConfig) ([]byte, error) {
	if strings.TrimSpace(environment.Framework) == "" {
		return nil, nil
	}

	plugin, ok := backend.NewDefaultPluginRegistry().Framework(environment.Framework)
	if !ok {
		return nil, fmt.Errorf("unsupported framework %q", environment.Framework)
	}
	result, handled, err := plugin.NginxConfig(plugins.NginxConfigContext{
		Environment:           environment,
		Host:                  host,
		Port:                  port,
		Docroot:               layout.Docroot,
		IndexNames:            renderNginxIndexNames(layout),
		TryFilesFallback:      renderNginxTryFilesFallback(layout),
		FastCGIIndex:          renderNginxFastCGIIndex(layout),
		BackendAddress:        backendAddress,
		TLSEnabled:            tlsConfig.Enabled,
		TLSCertificatePath:    tlsConfig.CertificatePath,
		TLSCertificateKeyPath: tlsConfig.CertificateKeyPath,
	})
	if err != nil {
		return nil, err
	}
	if !handled {
		return nil, nil
	}

	return result.Config, nil
}

// renderNginxServeConfig renders the generic front-controller nginx config.
func renderNginxServeConfig(host string, port int, layout serveAppLayout, backendAddress string, tlsConfig serveTLSConfig) []byte {
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
	if tlsConfig.Enabled {
		builder.WriteString(" ssl")
	}
	builder.WriteString(";\n")
	builder.WriteString("        server_name ")
	builder.WriteString(serverName)
	builder.WriteString(";\n")
	if tlsConfig.Enabled {
		builder.WriteString("        ssl_certificate ")
		builder.WriteString(quoteNginxPath(tlsConfig.CertificatePath))
		builder.WriteString(";\n")
		builder.WriteString("        ssl_certificate_key ")
		builder.WriteString(quoteNginxPath(tlsConfig.CertificateKeyPath))
		builder.WriteString(";\n")
		builder.WriteString("        ssl_protocols TLSv1.2 TLSv1.3;\n")
	}
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

// renderNginxListenAddress renders the listen directive address: local-only
// hostnames bind to the loopback proxy host, IPs bind directly, and other
// hostnames bind to the bare port on all interfaces.
func renderNginxListenAddress(host string, port int) string {
	trimmed := strings.TrimSpace(host)
	if isLocalOnlyServeHostname(trimmed) {
		return net.JoinHostPort(serveProxyHost, strconv.Itoa(port))
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		return net.JoinHostPort(trimmed, strconv.Itoa(port))
	}

	return strconv.Itoa(port)
}

// isLocalOnlyServeHostname reports whether the host is localhost, a
// localhost subdomain, or a loopback IP.
func isLocalOnlyServeHostname(host string) bool {
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(trimmed, defaultServeHostname) || strings.HasSuffix(strings.ToLower(trimmed), "."+defaultServeHostname) {
		return true
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		return ip.IsLoopback()
	}

	return false
}

// writeServeNginxMIMETypes renders the shared MIME table as an nginx types block body.
func writeServeNginxMIMETypes(builder *strings.Builder, indent string) {
	for _, mimeType := range serveStaticMIMETypes {
		builder.WriteString(indent)
		builder.WriteString(mimeType.ContentType)
		builder.WriteString(" ")
		builder.WriteString(strings.Join(mimeType.Extensions, " "))
		builder.WriteString(";\n")
	}
}

// renderNginxIndexNames renders the index directive value, preferring the
// front controller when one exists.
func renderNginxIndexNames(layout serveAppLayout) string {
	indices := []string{}
	if layout.FrontControllerIndex != "" {
		indices = append(indices, layout.FrontControllerIndex)
	}
	indices = append(indices, "index.html")

	return strings.Join(indices, " ")
}

// renderNginxTryFilesFallback renders the try_files fallback: the front
// controller when one exists, otherwise a 404.
func renderNginxTryFilesFallback(layout serveAppLayout) string {
	if layout.FrontControllerWebPath == "" {
		return "=404"
	}

	return layout.FrontControllerWebPath + "$is_args$args"
}

// renderNginxFastCGIIndex renders the fastcgi_index directive value.
func renderNginxFastCGIIndex(layout serveAppLayout) string {
	if layout.FrontControllerIndex == "" {
		return "index.php"
	}

	return layout.FrontControllerIndex
}

// quoteNginxPath quotes a filesystem path for use in nginx config.
func quoteNginxPath(path string) string {
	return strconv.Quote(filepath.ToSlash(path))
}

// ensureServePrefix appends a trailing path separator, which nginx requires
// for its -p prefix argument.
func ensureServePrefix(path string) string {
	if strings.HasSuffix(path, string(os.PathSeparator)) {
		return path
	}

	return path + string(os.PathSeparator)
}
