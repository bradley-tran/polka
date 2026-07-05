package cli

// This file contains the Apache serve lifecycle: foreground and background
// startup of httpd with its php-cgi upstream, plus httpd.conf generation.

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
	"polka/service"
)

// Test seams for the Apache webserver lifecycle.
var (
	runApacheServeFunc         = runApacheServe
	startBackgroundApacheServe = startApacheServeInBackground
)

// runApacheServe runs Apache attached to the current terminal, backed by a
// php-cgi upstream that is stopped when Apache exits.
func runApacheServe(stdout, stderr io.Writer, store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout) (int, error) {
	if backend.PrimaryPHPVersion(environment) == "" {
		return 0, fmt.Errorf("environment %q defines apache but does not define a php version", environment.Name)
	}

	phpTarget, err := store.ResolveTool("php")
	if err != nil {
		return 0, err
	}
	phpCGITarget, err := resolvePHPCGITarget(phpTarget)
	if err != nil {
		return 0, err
	}
	apacheTarget, err := store.ResolveTool(config.ServerTypeApache)
	if err != nil {
		return 0, err
	}
	apacheRoot := resolveApacheServerRoot(apacheTarget)
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

	runtimeDir := service.ToolLogRoot(store.RootDir, config.ServerTypeApache, environment.Name)
	configPath, phpLogPath, err := prepareApacheServeRuntime(store.CacheDir, runtimeDir, apacheRoot, endpoint, layout, backendAddress)
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

	return executeTargetWithEnv(stdout, stderr, env, apacheTarget, apacheServeArgs(apacheRoot, configPath))
}

// startApacheServeInBackground starts and records the managed Apache webserver
// with its php-cgi upstream.
func startApacheServeInBackground(store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout) (serveRuntimeState, error) {
	return startApacheServeInBackgroundAt(store, environment, endpoint, layout, service.ToolLogRoot(store.RootDir, config.ServerTypeApache, environment.Name))
}

func startApacheServeInBackgroundAt(store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout, runtimeDir string) (serveRuntimeState, error) {
	if backend.PrimaryPHPVersion(environment) == "" {
		return serveRuntimeState{}, fmt.Errorf("environment %q defines apache but does not define a php version", environment.Name)
	}

	phpTarget, err := store.ResolveTool("php")
	if err != nil {
		return serveRuntimeState{}, err
	}
	phpCGITarget, err := resolvePHPCGITarget(phpTarget)
	if err != nil {
		return serveRuntimeState{}, err
	}
	apacheTarget, err := store.ResolveTool(config.ServerTypeApache)
	if err != nil {
		return serveRuntimeState{}, err
	}
	apacheRoot := resolveApacheServerRoot(apacheTarget)
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

	configPath, phpLogPath, err := prepareApacheServeRuntime(store.CacheDir, runtimeDir, apacheRoot, endpoint, layout, backendAddress)
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

	apacheLogPath := filepath.Join(runtimeDir, serveLogFileName)
	apacheLogFile, err := openServeLog(apacheLogPath)
	if err != nil {
		stopServeProcess(phpCommand.Process)
		return serveRuntimeState{}, err
	}

	apacheCommand, err := prepareCommand(apacheTarget, apacheServeArgs(apacheRoot, configPath))
	if err != nil {
		_ = apacheLogFile.Close()
		stopServeProcess(phpCommand.Process)
		return serveRuntimeState{}, err
	}
	apacheCommand.Stdout = apacheLogFile
	apacheCommand.Stderr = apacheLogFile
	apacheCommand.Env = env

	if err := apacheCommand.Start(); err != nil {
		_ = apacheLogFile.Close()
		stopServeProcess(phpCommand.Process)
		return serveRuntimeState{}, fmt.Errorf("start apache webserver: %w", err)
	}

	if err := waitForServeAddress(serveProbeAddress(endpoint.Address), serveStartupTimeout); err != nil {
		stopServeProcess(apacheCommand.Process)
		stopServeProcess(phpCommand.Process)
		_ = apacheLogFile.Close()
		return serveRuntimeState{}, fmt.Errorf("start apache webserver on %s: %w (see %s and %s)", endpoint.Address, err, apacheLogPath, phpLogPath)
	}

	state := serveRuntimeState{
		EnvironmentName: environment.Name,
		ServerKind:      config.ServerTypeApache,
		ServerScheme:    endpoint.Scheme,
		ServerAddress:   endpoint.Address,
		Docroot:         layout.Docroot,
		RuntimeDir:      runtimeDir,
		LogPath:         apacheLogPath,
		BackendLogPath:  phpLogPath,
		ConfigPath:      configPath,
		PrimaryPID:      apacheCommand.Process.Pid,
		SecondaryPID:    phpPID,
		StartedAt:       serveNowFunc().UTC(),
	}
	_ = apacheLogFile.Close()
	_ = phpCommand.Process.Release()
	_ = apacheCommand.Process.Release()

	return state, nil
}

// apacheServeArgs builds the httpd arguments for a managed foreground run.
func apacheServeArgs(apacheRoot, configPath string) []string {
	return []string{"-d", apacheRoot, "-f", configPath, "-DFOREGROUND"}
}

// resolveApacheServerRoot derives the Apache server root from the httpd
// executable path, stepping out of a bin directory when present.
func resolveApacheServerRoot(apacheTarget string) string {
	dir := filepath.Dir(strings.TrimSpace(apacheTarget))
	if strings.EqualFold(filepath.Base(dir), "bin") {
		return filepath.Dir(dir)
	}

	return dir
}

// prepareApacheServeRuntime creates the Apache runtime directories, provisions
// TLS material when needed, and writes the generated httpd.conf. It returns
// the config path and the php-cgi upstream log path.
func prepareApacheServeRuntime(cacheDir, runtimeDir, apacheRoot string, endpoint serverEndpoint, layout serveAppLayout, backendAddress string) (string, string, error) {
	logsDir := filepath.Join(runtimeDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create serve runtime directory: %w", err)
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

	configPath := filepath.Join(runtimeDir, "httpd.conf")
	phpLogPath := filepath.Join(runtimeDir, "php.log")
	config := renderApacheServeConfig(apacheRoot, runtimeDir, host, port, layout, backendAddress, tlsConfig)
	if err := os.WriteFile(configPath, config, 0o644); err != nil {
		return "", "", fmt.Errorf("write apache config: %w", err)
	}

	return configPath, phpLogPath, nil
}

// renderApacheServeConfig renders a self-contained httpd.conf that proxies PHP
// requests to the php-cgi upstream and rewrites to the front controller.
func renderApacheServeConfig(apacheRoot, runtimeDir, host string, port int, layout serveAppLayout, backendAddress string, tlsConfig serveTLSConfig) []byte {
	listenAddress := renderNginxListenAddress(host, port)
	serverName := strings.TrimSpace(host)
	if serverName == "" {
		serverName = defaultServeHostname
	}
	serverName = net.JoinHostPort(serverName, strconv.Itoa(port))
	modulesDir := filepath.Join(apacheRoot, "modules")
	logsDir := filepath.Join(runtimeDir, "logs")

	var builder strings.Builder
	builder.WriteString("ServerRoot ")
	builder.WriteString(quoteApachePath(apacheRoot))
	builder.WriteString("\n")
	builder.WriteString("PidFile ")
	builder.WriteString(quoteApachePath(filepath.Join(runtimeDir, "httpd.pid")))
	builder.WriteString("\n")
	builder.WriteString("ServerName ")
	builder.WriteString(serverName)
	builder.WriteString("\n")
	builder.WriteString("Listen ")
	builder.WriteString(listenAddress)
	builder.WriteString("\n")
	builder.WriteString("LogLevel warn\n")
	builder.WriteString("LogFormat \"%h %l %u %t \\\"%r\\\" %>s %b\" common\n")
	builder.WriteString("EnableSendfile Off\n")
	writeApacheMPMModule(&builder, modulesDir)
	writeApacheLoadModule(&builder, modulesDir, "authz_core_module", "mod_authz_core.so")
	writeApacheLoadModule(&builder, modulesDir, "authz_host_module", "mod_authz_host.so")
	writeApacheLoadModule(&builder, modulesDir, "dir_module", "mod_dir.so")
	writeApacheLoadModule(&builder, modulesDir, "mime_module", "mod_mime.so")
	writeApacheLoadModule(&builder, modulesDir, "log_config_module", "mod_log_config.so")
	writeApacheLoadModule(&builder, modulesDir, "rewrite_module", "mod_rewrite.so")
	writeApacheLoadModule(&builder, modulesDir, "proxy_module", "mod_proxy.so")
	writeApacheLoadModule(&builder, modulesDir, "proxy_fcgi_module", "mod_proxy_fcgi.so")
	if tlsConfig.Enabled {
		writeApacheLoadModule(&builder, modulesDir, "socache_shmcb_module", "mod_socache_shmcb.so")
		writeApacheLoadModule(&builder, modulesDir, "ssl_module", "mod_ssl.so")
	}
	builder.WriteString("\n")
	builder.WriteString("AddDefaultCharset Off\n")
	writeServeApacheMIMETypes(&builder, "")
	builder.WriteString("\n")
	builder.WriteString("<VirtualHost *:")
	builder.WriteString(strconv.Itoa(port))
	builder.WriteString(">\n")
	builder.WriteString("    ServerName ")
	builder.WriteString(serverName)
	builder.WriteString("\n")
	builder.WriteString("    DocumentRoot ")
	builder.WriteString(quoteApachePath(layout.Docroot))
	builder.WriteString("\n")
	builder.WriteString("    ErrorLog ")
	builder.WriteString(quoteApachePath(filepath.Join(logsDir, "error.log")))
	builder.WriteString("\n")
	builder.WriteString("    CustomLog ")
	builder.WriteString(quoteApachePath(filepath.Join(logsDir, "access.log")))
	builder.WriteString(" common\n")
	builder.WriteString("    DirectoryIndex ")
	builder.WriteString(renderApacheDirectoryIndex(layout))
	builder.WriteString("\n")
	if tlsConfig.Enabled {
		builder.WriteString("    SSLEngine on\n")
		builder.WriteString("    SSLCertificateFile ")
		builder.WriteString(quoteApachePath(tlsConfig.CertificatePath))
		builder.WriteString("\n")
		builder.WriteString("    SSLCertificateKeyFile ")
		builder.WriteString(quoteApachePath(tlsConfig.CertificateKeyPath))
		builder.WriteString("\n")
		builder.WriteString("    SSLProtocol all -SSLv3 -TLSv1 -TLSv1.1\n")
	}
	builder.WriteString("    <Directory ")
	builder.WriteString(quoteApachePath(layout.Docroot))
	builder.WriteString(">\n")
	builder.WriteString("        Options FollowSymLinks\n")
	builder.WriteString("        AllowOverride All\n")
	builder.WriteString("        Require all granted\n")
	if layout.FrontControllerRelative != "" {
		builder.WriteString("        RewriteEngine On\n")
		builder.WriteString("        RewriteCond %{REQUEST_FILENAME} !-f\n")
		builder.WriteString("        RewriteCond %{REQUEST_FILENAME} !-d\n")
		builder.WriteString("        RewriteRule ^ ")
		builder.WriteString(filepath.ToSlash(layout.FrontControllerRelative))
		builder.WriteString(" [QSA,L]\n")
	}
	builder.WriteString("    </Directory>\n")
	builder.WriteString("    <FilesMatch \"^\\.ht\">\n")
	builder.WriteString("        Require all denied\n")
	builder.WriteString("    </FilesMatch>\n")
	builder.WriteString("    ProxyFCGIBackendType GENERIC\n")
	builder.WriteString("    ProxyFCGISetEnvIf \"true\" SCRIPT_FILENAME \"%{reqenv:DOCUMENT_ROOT}%{reqenv:SCRIPT_NAME}\"\n")
	builder.WriteString("    <FilesMatch \"\\.php$\">\n")
	builder.WriteString("        SetHandler \"proxy:fcgi://")
	builder.WriteString(backendAddress)
	builder.WriteString("/\"\n")
	builder.WriteString("    </FilesMatch>\n")
	builder.WriteString("</VirtualHost>\n")

	return []byte(builder.String())
}

// writeApacheLoadModule renders one LoadModule directive.
func writeApacheLoadModule(builder *strings.Builder, modulesDir, module, fileName string) {
	builder.WriteString("LoadModule ")
	builder.WriteString(module)
	builder.WriteString(" ")
	builder.WriteString(quoteApachePath(filepath.Join(modulesDir, fileName)))
	builder.WriteString("\n")
}

// writeApacheMPMModule loads the event MPM on non-Windows platforms; the
// Windows httpd build has its MPM compiled in.
func writeApacheMPMModule(builder *strings.Builder, modulesDir string) {
	if runtime.GOOS == "windows" {
		return
	}

	writeApacheLoadModule(builder, modulesDir, "mpm_event_module", "mod_mpm_event.so")
}

// writeServeApacheMIMETypes renders the shared MIME table as AddType directives.
func writeServeApacheMIMETypes(builder *strings.Builder, indent string) {
	for _, mimeType := range serveStaticMIMETypes {
		builder.WriteString(indent)
		builder.WriteString("AddType ")
		builder.WriteString(mimeType.ContentType)
		for _, extension := range mimeType.Extensions {
			builder.WriteString(" .")
			builder.WriteString(extension)
		}
		builder.WriteString("\n")
	}
}

// renderApacheDirectoryIndex renders the DirectoryIndex value; it matches the
// nginx index directive so all webservers behave alike.
func renderApacheDirectoryIndex(layout serveAppLayout) string {
	return renderNginxIndexNames(layout)
}

// quoteApachePath quotes a filesystem path for use in httpd.conf.
func quoteApachePath(path string) string {
	return strconv.Quote(filepath.ToSlash(path))
}
