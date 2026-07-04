package cli

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math/big"
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
	"polka/config"
	"polka/plugins"
	"polka/service"
)

const (
	defaultServeHostname    = "localhost"
	defaultServePort        = 8000
	servePollInterval       = 100 * time.Millisecond
	serveStartupTimeout     = 5 * time.Second
	serveRuntimeRoot        = "run"
	serveRuntimeSubdir      = "serve"
	serveStateFileName      = "state.json"
	serveLogFileName        = "serve.log"
	serveTLSSubdir          = "cert"
	serveTLSCACertName      = "polka-local-ca.crt"
	serveTLSCAKeyName       = "polka-local-ca.key"
	serveTLSCertFileName    = "polka-local.crt"
	serveTLSKeyFileName     = "polka-local.key"
	managedOpenSSLConfig    = "extras/ssl/openssl.cnf"
	phpServeRouterName      = "php-router.php"
	frankenPHPCaddyfileName = "Caddyfile"
	serveProxyHost          = "127.0.0.1"
	serveShutdownTimeout    = 5 * time.Second
)

var (
	runPHPRuntimeServeFunc         = runPHPRuntimeServe
	runNginxServeFunc              = runNginxServe
	runApacheServeFunc             = runApacheServe
	runFrankenPHPServeFunc         = runFrankenPHPServe
	startBackgroundPHPRuntimeServe = startPHPRuntimeServeInBackground
	startBackgroundNginxServe      = startNginxServeInBackground
	startBackgroundApacheServe     = startApacheServeInBackground
	startBackgroundFrankenPHPServe = startFrankenPHPServeInBackground
	stopServeRuntimeFunc           = stopServeRuntime
	pingServeAddressFunc           = pingServeAddress
	serveNowFunc                   = time.Now
)

type serveCommandInput struct {
	Docroot string
	Server  string
	Watch   bool
}

type serveRuntimeState = service.ServeRuntimeState
type serveAppLayout = service.AppLayout
type serverEndpoint = service.Endpoint

type serveTLSConfig struct {
	Enabled            bool
	CertificatePath    string
	CertificateKeyPath string
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
		Use:     "serve [docroot]",
		Aliases: []string{"start"},
		Args:    maximumArgsError("start/serve accepts at most one docroot", 1),
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

			ctx.exitCode = runStart(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, input)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.Server, "server", "", "server address override in HOST:PORT form")
	cmd.Flags().BoolVar(&input.Watch, "watch", false, "keep the webserver attached to the current terminal")
	configureCommand(cmd, startUsage)

	return cmd
}

func runStart(stdout, stderr io.Writer, store backend.Store, input serveCommandInput) int {
	restoreTLSWarning := setTLSCAKeyFallbackWarningWriter(stderr)
	defer restoreTLSWarning()

	current, err := store.Current()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if current == nil {
		fmt.Fprintln(stderr, "error: no active environment selected")
		return 1
	}

	endpoint, err := resolveServerEndpoint(current.Server, input.Server)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	serverType, err := resolveEnvironmentServerType(*current)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if endpoint.HTTPS && serverType == config.ServerTypePHP {
		fmt.Fprintln(stderr, "error: https is not supported by the PHP webserver; use nginx, apache, or frankenphp")
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
	hooks := defaultCLIHookRegistry()
	if err := hooks.StartServices(startHookContext{
		Stdout:      stdout,
		Stderr:      stderr,
		Store:       store,
		Environment: *current,
	}); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	liveState, err := loadWebServerState(store.RootDir, current.Name)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if liveState != nil {
		if serveStateMatches(*liveState, endpoint, layout.Docroot, serverType) {
			fmt.Fprintf(stdout, "Webserver for environment %q is already running at %s.\n", current.Name, serveStateURL(*liveState))
			return 0
		}

		fmt.Fprintf(stderr, "error: environment %q already has a running %s at %s; run `polka stop` before starting a different webserver\n", current.Name, serveRuntimeLabel(liveState.ServerKind), serveStateURL(*liveState))
		return 1
	}

	traefikProxy, err := prepareTraefikServeProxy(store, *current, endpoint)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if traefikProxy.Active && input.Watch {
		fmt.Fprintf(stdout, "Reverse proxying through Traefik at %s.\n", traefikProxy.URL)
	}

	exitCode, err := hooks.StartWebserver(webserverStartHookContext{
		Stdout:      stdout,
		Stderr:      stderr,
		Store:       store,
		Environment: *current,
		ServerType:  serverType,
		Input:       input,
		Endpoint:    endpoint,
		Layout:      layout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if traefikProxy.Active && !input.Watch {
		fmt.Fprintf(stdout, "Reverse proxying through Traefik at %s.\n", traefikProxy.URL)
	}

	return exitCode
}

// traefikServeProxy captures whether a managed Traefik front proxy is active for
// the current serve and the URL it is reachable at.
type traefikServeProxy struct {
	Active bool
	URL    string
}

// prepareTraefikServeProxy points a running managed Traefik at the environment
// webserver. It is a no-op when the environment does not configure Traefik or
// when Traefik is not installed and running, so serving still works without it.
func prepareTraefikServeProxy(store backend.Store, environment backend.Environment, endpoint serverEndpoint) (traefikServeProxy, error) {
	if environment.Traefik == nil || strings.TrimSpace(environment.Traefik.Version) == "" {
		return traefikServeProxy{}, nil
	}

	liveState, err := loadLiveTraefikState(store.RootDir, environment.Name)
	if err != nil {
		return traefikServeProxy{}, err
	}
	if liveState == nil {
		// Traefik is configured but not installed or not running; serve directly.
		return traefikServeProxy{}, nil
	}

	host, _, err := net.SplitHostPort(endpoint.Address)
	if err != nil || strings.TrimSpace(host) == "" {
		host = defaultServeHostname
	}

	// When the project serves HTTPS, terminate TLS at the Traefik entrypoint with
	// Polka's local certificate so the proxy URL is https and trusted by the
	// local CA, matching the webserver it fronts.
	scheme := "http"
	var tlsConfig *service.TraefikTLSConfig
	if endpoint.HTTPS {
		certPath, keyPath, err := ensureGlobalTLSCertificateRuntimeKey(store.CacheDir, service.TraefikRuntimeDir(store.RootDir, environment.Name), host)
		if err != nil {
			return traefikServeProxy{}, err
		}
		tlsConfig = &service.TraefikTLSConfig{CertificatePath: certPath, KeyPath: keyPath}
		scheme = "https"
	}

	upstreamURL := serverEndpointURL(endpoint)
	if err := service.WriteTraefikDynamicConfig(store.RootDir, environment.Name, upstreamURL, endpoint.HTTPS, tlsConfig); err != nil {
		return traefikServeProxy{}, err
	}

	return traefikServeProxy{
		Active: true,
		URL:    scheme + "://" + net.JoinHostPort(host, strconv.Itoa(liveState.Port)),
	}, nil
}

// resolveEnvironmentServerType returns the configured webserver provider.
// An omitted type preserves the legacy nginx-then-PHP selection behavior.
func resolveEnvironmentServerType(environment backend.Environment) (string, error) {
	serverType := ""
	if environment.Server != nil {
		serverType = strings.ToLower(strings.TrimSpace(environment.Server.Type))
	}
	if serverType == "" {
		if strings.TrimSpace(environment.NginxVersion) != "" {
			serverType = config.ServerTypeNginx
		} else {
			serverType = config.ServerTypePHP
		}
	}

	switch serverType {
	case config.ServerTypePHP:
		if !backend.HasPHPCLI(environment) {
			return "", fmt.Errorf("environment %q selects the PHP webserver but does not define a PHP CLI provider", environment.Name)
		}
	case config.ServerTypeNginx:
		if strings.TrimSpace(environment.NginxVersion) == "" {
			return "", fmt.Errorf("environment %q selects nginx but does not define an nginx version", environment.Name)
		}
		if backend.PrimaryPHPVersion(environment) == "" {
			return "", fmt.Errorf("environment %q selects nginx but does not define a php or php-zts version", environment.Name)
		}
	case config.ServerTypeApache:
		if strings.TrimSpace(environment.ApacheVersion) == "" {
			return "", fmt.Errorf("environment %q selects apache but does not define an apache version", environment.Name)
		}
		if backend.PrimaryPHPVersion(environment) == "" {
			return "", fmt.Errorf("environment %q selects apache but does not define a php or php-zts version", environment.Name)
		}
	case config.ServerTypeFrankenPHP:
		if strings.TrimSpace(environment.FrankenPHPVersion) == "" {
			return "", fmt.Errorf("environment %q selects frankenphp but does not define a frankenphp version", environment.Name)
		}
	default:
		return "", fmt.Errorf("unsupported server type %q: use php, nginx, apache, or frankenphp", serverType)
	}

	return serverType, nil
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

// runFrankenPHPServe runs FrankenPHP attached to the current terminal.
func runFrankenPHPServe(stdout, stderr io.Writer, store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout) (int, error) {
	frankenPHPTarget, err := store.ResolveTool(config.ServerTypeFrankenPHP)
	if err != nil {
		return 0, err
	}
	env, err := resolveFrankenPHPRuntimeEnvironment(store, frankenPHPTarget)
	if err != nil {
		return 0, err
	}
	runtimeDir := service.ToolLogRoot(store.RootDir, config.ServerTypeFrankenPHP, environment.Name)
	configPath, err := prepareFrankenPHPServeRuntime(store.CacheDir, runtimeDir, endpoint, layout)
	if err != nil {
		return 0, err
	}

	args := []string{"run", "--config", configPath, "--adapter", "caddyfile"}
	return executeTargetWithEnv(stdout, stderr, env, frankenPHPTarget, args)
}

// startFrankenPHPServeInBackground starts and records the managed FrankenPHP process.
func startFrankenPHPServeInBackground(store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout) (serveRuntimeState, error) {
	runtimeDir := service.ToolLogRoot(store.RootDir, config.ServerTypeFrankenPHP, environment.Name)
	return startFrankenPHPServeInBackgroundAt(store, environment, endpoint, layout, runtimeDir)
}

func startFrankenPHPServeInBackgroundAt(store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout, runtimeDir string) (serveRuntimeState, error) {
	frankenPHPTarget, err := store.ResolveTool(config.ServerTypeFrankenPHP)
	if err != nil {
		return serveRuntimeState{}, err
	}
	env, err := resolveFrankenPHPRuntimeEnvironment(store, frankenPHPTarget)
	if err != nil {
		return serveRuntimeState{}, err
	}
	configPath, err := prepareFrankenPHPServeRuntime(store.CacheDir, runtimeDir, endpoint, layout)
	if err != nil {
		return serveRuntimeState{}, err
	}
	logPath := filepath.Join(runtimeDir, serveLogFileName)
	logFile, err := openServeLog(logPath)
	if err != nil {
		return serveRuntimeState{}, err
	}

	args := []string{"run", "--config", configPath, "--adapter", "caddyfile"}
	command, err := prepareCommand(frankenPHPTarget, args)
	if err != nil {
		_ = logFile.Close()
		return serveRuntimeState{}, err
	}
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = env

	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return serveRuntimeState{}, fmt.Errorf("start FrankenPHP webserver: %w", err)
	}
	if err := waitForServeAddress(serveProbeAddress(endpoint.Address), serveStartupTimeout); err != nil {
		stopServeProcess(command.Process)
		_ = logFile.Close()
		return serveRuntimeState{}, fmt.Errorf("start FrankenPHP webserver on %s: %w (see %s)", endpoint.Address, err, logPath)
	}

	state := serveRuntimeState{
		EnvironmentName: environment.Name,
		ServerKind:      config.ServerTypeFrankenPHP,
		ServerScheme:    endpoint.Scheme,
		ServerAddress:   endpoint.Address,
		Docroot:         layout.Docroot,
		RuntimeDir:      runtimeDir,
		LogPath:         logPath,
		ConfigPath:      configPath,
		PrimaryPID:      command.Process.Pid,
		StartedAt:       serveNowFunc().UTC(),
	}
	_ = logFile.Close()
	_ = command.Process.Release()

	return state, nil
}

// resolveFrankenPHPRuntimeEnvironment points the Windows bundle at its managed php.ini.
func resolveFrankenPHPRuntimeEnvironment(store backend.Store, frankenPHPTarget string) ([]string, error) {
	env, err := resolveRuntimeEnvironment(runtime.GOOS, os.Environ(), store)
	if err != nil {
		return nil, err
	}
	return applyManagedPHPRuntimeConfig(runtime.GOOS, env, frankenPHPTarget)
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

func apacheServeArgs(apacheRoot, configPath string) []string {
	return []string{"-d", apacheRoot, "-f", configPath, "-DFOREGROUND"}
}

func resolveApacheServerRoot(apacheTarget string) string {
	dir := filepath.Dir(strings.TrimSpace(apacheTarget))
	if strings.EqualFold(filepath.Base(dir), "bin") {
		return filepath.Dir(dir)
	}

	return dir
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

func resolveServerEndpoint(config *backend.ServerConfig, override string) (serverEndpoint, error) {
	address, err := resolveServeAddress(config, override)
	if err != nil {
		return serverEndpoint{}, err
	}

	scheme := "http"
	https := config != nil && config.HTTPS
	if https {
		scheme = "https"
	}

	return serverEndpoint{Scheme: scheme, Address: address, HTTPS: https}, nil
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

func serverEndpointURL(endpoint serverEndpoint) string {
	scheme := strings.TrimSpace(endpoint.Scheme)
	if scheme == "" {
		scheme = "http"
	}

	return scheme + "://" + strings.TrimSpace(endpoint.Address)
}

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

// prepareFrankenPHPServeRuntime writes the generated Caddyfile and TLS material references.
func prepareFrankenPHPServeRuntime(cacheDir, runtimeDir string, endpoint serverEndpoint, layout serveAppLayout) (string, error) {
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return "", fmt.Errorf("create FrankenPHP runtime directory: %w", err)
	}

	host, _, err := net.SplitHostPort(endpoint.Address)
	if err != nil {
		return "", fmt.Errorf("parse serve address %q: %w", endpoint.Address, err)
	}
	tlsConfig := serveTLSConfig{}
	if endpoint.HTTPS {
		certPath, keyPath, err := ensureGlobalTLSCertificateRuntimeKey(cacheDir, runtimeDir, host)
		if err != nil {
			return "", err
		}
		tlsConfig = serveTLSConfig{
			Enabled:            true,
			CertificatePath:    certPath,
			CertificateKeyPath: keyPath,
		}
	}

	configPath := filepath.Join(runtimeDir, frankenPHPCaddyfileName)
	configData := renderFrankenPHPCaddyfile(endpoint, layout, tlsConfig)
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		return "", fmt.Errorf("write FrankenPHP Caddyfile: %w", err)
	}

	return configPath, nil
}

// renderFrankenPHPCaddyfile renders a self-contained development server configuration.
func renderFrankenPHPCaddyfile(endpoint serverEndpoint, layout serveAppLayout, tlsConfig serveTLSConfig) []byte {
	var builder strings.Builder
	builder.WriteString("{\n")
	builder.WriteString("\tadmin off\n")
	builder.WriteString("\tauto_https disable_redirects\n")
	builder.WriteString("}\n\n")
	builder.WriteString(endpoint.Scheme)
	builder.WriteString("://")
	builder.WriteString(endpoint.Address)
	builder.WriteString(" {\n")
	builder.WriteString("\troot * ")
	builder.WriteString(strconv.Quote(filepath.ToSlash(layout.Docroot)))
	builder.WriteString("\n")
	builder.WriteString("\tencode zstd br gzip\n")
	if tlsConfig.Enabled {
		builder.WriteString("\ttls ")
		builder.WriteString(strconv.Quote(filepath.ToSlash(tlsConfig.CertificatePath)))
		builder.WriteString(" ")
		builder.WriteString(strconv.Quote(filepath.ToSlash(tlsConfig.CertificateKeyPath)))
		builder.WriteString("\n")
	}
	builder.WriteString("\tphp_server\n")
	builder.WriteString("}\n")

	return []byte(builder.String())
}

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

func writeApacheLoadModule(builder *strings.Builder, modulesDir, module, fileName string) {
	builder.WriteString("LoadModule ")
	builder.WriteString(module)
	builder.WriteString(" ")
	builder.WriteString(quoteApachePath(filepath.Join(modulesDir, fileName)))
	builder.WriteString("\n")
}

func writeApacheMPMModule(builder *strings.Builder, modulesDir string) {
	if runtime.GOOS == "windows" {
		return
	}

	writeApacheLoadModule(builder, modulesDir, "mpm_event_module", "mod_mpm_event.so")
}

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

func ensureGlobalTLSCertificate(cacheDir, host string) (string, string, error) {
	return ensureGlobalTLSCertificateWithOptions(cacheDir, host, defaultTLSCAKeyStorageOptions())
}

func ensureGlobalTLSCertificateRuntimeKey(cacheDir, runtimeDir, host string) (string, string, error) {
	return ensureGlobalTLSCertificateRuntimeKeyWithOptions(cacheDir, runtimeDir, host, defaultTLSCAKeyStorageOptions())
}

func ensureGlobalTLSCertificateRuntimeKeyWithOptions(cacheDir, runtimeDir, host string, storageOptions tlsCAKeyStorageOptions) (string, string, error) {
	certPath, keyPath, err := ensureGlobalTLSCertificateWithOptions(cacheDir, host, storageOptions)
	if err != nil {
		return "", "", err
	}
	runtimeKeyPath := filepath.Join(runtimeDir, serveTLSKeyFileName)
	if err := materializeTLSPrivateKeyFile(keyPath, runtimeKeyPath, storageOptions); err != nil {
		return "", "", fmt.Errorf("write local tls runtime private key: %w", err)
	}

	return certPath, runtimeKeyPath, nil
}

func ensureGlobalTLSCertificateWithOptions(cacheDir, host string, storageOptions tlsCAKeyStorageOptions) (string, string, error) {
	certPath, keyPath := globalTLSCertificatePaths(cacheDir)
	caCertPath, caKeyPath := globalTLSCACertificatePaths(cacheDir)
	hasCA := certificateFileExists(caCertPath) && certificateFileExists(caKeyPath)
	if certificateFileExists(certPath) && certificateFileExists(keyPath) && hasCA && serverCertificateCoversHost(certPath, host) {
		return certPath, keyPath, nil
	}
	if hasCA {
		return createGlobalTLSServerCertificateWithOptions(cacheDir, host, storageOptions)
	}

	return createGlobalTLSCertificateWithOptions(cacheDir, host, storageOptions)
}

func regenerateGlobalTLSCertificate(cacheDir string) (string, string, error) {
	return regenerateGlobalTLSCertificateWithOptions(cacheDir, defaultTLSCAKeyStorageOptions())
}

func regenerateGlobalTLSCertificateWithOptions(cacheDir string, storageOptions tlsCAKeyStorageOptions) (string, string, error) {
	certPath, keyPath := globalTLSCertificatePaths(cacheDir)
	caCertPath, caKeyPath := globalTLSCACertificatePaths(cacheDir)
	for _, path := range []string{certPath, keyPath, caCertPath, caKeyPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("remove existing tls certificate file %s: %w", path, err)
		}
	}

	return createGlobalTLSCertificateWithOptions(cacheDir, "", storageOptions)
}

func createGlobalTLSCertificate(cacheDir, host string) (string, string, error) {
	return createGlobalTLSCertificateWithOptions(cacheDir, host, defaultTLSCAKeyStorageOptions())
}

func createGlobalTLSCertificateWithOptions(cacheDir, host string, storageOptions tlsCAKeyStorageOptions) (string, string, error) {
	certPath, _ := globalTLSCertificatePaths(cacheDir)
	caCertPath, caKeyPath := globalTLSCACertificatePaths(cacheDir)
	certDir := filepath.Dir(certPath)
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create local tls certificate directory: %w", err)
	}

	caPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", fmt.Errorf("generate local ca private key: %w", err)
	}
	caSerialNumber, err := randomTLSSerialNumber()
	if err != nil {
		return "", "", err
	}

	now := serveNowFunc().UTC()
	caTemplate := x509.Certificate{
		SerialNumber: caSerialNumber,
		Subject: pkix.Name{
			CommonName: "Polka Local Development CA",
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caPrivateKey.PublicKey, caPrivateKey)
	if err != nil {
		return "", "", fmt.Errorf("generate local ca certificate: %w", err)
	}

	if err := writePEMFile(caCertPath, 0o644, "CERTIFICATE", caDER); err != nil {
		return "", "", fmt.Errorf("write local ca certificate: %w", err)
	}
	if err := writeTLSPrivateKeyFile(caKeyPath, caPrivateKey, storageOptions); err != nil {
		return "", "", fmt.Errorf("write local ca private key: %w", err)
	}

	return createGlobalTLSServerCertificateWithCA(cacheDir, host, &caTemplate, caPrivateKey, storageOptions)
}

func createGlobalTLSServerCertificate(cacheDir, host string) (string, string, error) {
	return createGlobalTLSServerCertificateWithOptions(cacheDir, host, defaultTLSCAKeyStorageOptions())
}

func createGlobalTLSServerCertificateWithOptions(cacheDir, host string, storageOptions tlsCAKeyStorageOptions) (string, string, error) {
	caCertificate, caPrivateKey, err := loadGlobalTLSCAWithOptions(cacheDir, storageOptions)
	if err != nil {
		return "", "", err
	}

	return createGlobalTLSServerCertificateWithCA(cacheDir, host, caCertificate, caPrivateKey, storageOptions)
}

func createGlobalTLSServerCertificateWithCA(cacheDir, host string, caCertificate *x509.Certificate, caPrivateKey *rsa.PrivateKey, storageOptions tlsCAKeyStorageOptions) (string, string, error) {
	certPath, keyPath := globalTLSCertificatePaths(cacheDir)
	serverPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", fmt.Errorf("generate local tls private key: %w", err)
	}
	serverSerialNumber, err := randomTLSSerialNumber()
	if err != nil {
		return "", "", err
	}
	dnsNames, ipAddresses := serverCertificateNames(host)
	now := serveNowFunc().UTC()
	serverTemplate := x509.Certificate{
		SerialNumber: serverSerialNumber,
		Subject: pkix.Name{
			CommonName: firstServerCertificateCommonName(dnsNames, ipAddresses),
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.AddDate(2, 0, 0),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, &serverTemplate, caCertificate, &serverPrivateKey.PublicKey, caPrivateKey)
	if err != nil {
		return "", "", fmt.Errorf("generate local tls certificate: %w", err)
	}

	if err := writePEMFile(certPath, 0o644, "CERTIFICATE", serverDER); err != nil {
		return "", "", fmt.Errorf("write local tls certificate: %w", err)
	}
	if err := writeTLSPrivateKeyFile(keyPath, serverPrivateKey, storageOptions); err != nil {
		return "", "", fmt.Errorf("write local tls private key: %w", err)
	}

	return certPath, keyPath, nil
}

func loadGlobalTLSCA(cacheDir string) (*x509.Certificate, *rsa.PrivateKey, error) {
	return loadGlobalTLSCAWithOptions(cacheDir, defaultTLSCAKeyStorageOptions())
}

func loadGlobalTLSCAWithOptions(cacheDir string, storageOptions tlsCAKeyStorageOptions) (*x509.Certificate, *rsa.PrivateKey, error) {
	caCertPath, caKeyPath := globalTLSCACertificatePaths(cacheDir)
	caCertificate, err := readCertificateFile(caCertPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read local ca certificate: %w", err)
	}
	caPrivateKey, err := readTLSPrivateKeyFile(caKeyPath, storageOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("read local ca private key: %w", err)
	}

	return caCertificate, caPrivateKey, nil
}

func serverCertificateCoversHost(certificatePath, host string) bool {
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	if trimmed == "" {
		return true
	}
	certificate, err := readCertificateFile(certificatePath)
	if err != nil {
		return false
	}

	return certificate.VerifyHostname(trimmed) == nil && certificateHasExactHost(certificate, trimmed)
}

func certificateHasExactHost(certificate *x509.Certificate, host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		for _, candidate := range certificate.IPAddresses {
			if candidate.Equal(ip) {
				return true
			}
		}

		return false
	}
	for _, candidate := range certificate.DNSNames {
		if strings.EqualFold(candidate, host) {
			return true
		}
	}

	return false
}

func serverCertificateNames(host string) ([]string, []net.IP) {
	dnsNames := []string{"localhost", "*.localhost"}
	ipAddresses := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	if trimmed == "" {
		return dnsNames, ipAddresses
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		for _, existing := range ipAddresses {
			if existing.Equal(ip) {
				return dnsNames, ipAddresses
			}
		}

		return dnsNames, append(ipAddresses, ip)
	}
	for _, existing := range dnsNames {
		if strings.EqualFold(existing, trimmed) {
			return dnsNames, ipAddresses
		}
	}

	return append(dnsNames, trimmed), ipAddresses
}

func firstServerCertificateCommonName(dnsNames []string, ipAddresses []net.IP) string {
	for _, name := range dnsNames {
		if name != "" && !strings.HasPrefix(name, "*.") {
			return name
		}
	}
	if len(ipAddresses) > 0 {
		return ipAddresses[0].String()
	}

	return "localhost"
}

func randomTLSSerialNumber() (*big.Int, error) {
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("generate tls serial number: %w", err)
	}

	return serialNumber, nil
}

func writePEMFile(path string, mode os.FileMode, blockType string, data []byte) error {
	pemData := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: data})

	return os.WriteFile(path, pemData, mode)
}

func readCertificateFile(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("missing CERTIFICATE PEM block in %s", path)
	}

	return x509.ParseCertificate(block.Bytes)
}

func globalTLSCertificatePaths(cacheDir string) (string, string) {
	certDir := globalTLSCertificateDir(cacheDir)
	certPath := filepath.Join(certDir, serveTLSCertFileName)
	keyPath := filepath.Join(certDir, serveTLSKeyFileName)

	return certPath, keyPath
}

func globalTLSCACertificatePaths(cacheDir string) (string, string) {
	certDir := globalTLSCertificateDir(cacheDir)
	certPath := filepath.Join(certDir, serveTLSCACertName)
	keyPath := filepath.Join(certDir, serveTLSCAKeyName)

	return certPath, keyPath
}

func globalTLSCertificateDir(cacheDir string) string {
	cleanCacheDir := filepath.Clean(cacheDir)
	base := filepath.Base(cleanCacheDir)
	if strings.EqualFold(base, "cache") && strings.EqualFold(filepath.Base(filepath.Dir(cleanCacheDir)), "polka") {
		return filepath.Join(filepath.Dir(cleanCacheDir), serveTLSSubdir)
	}

	return filepath.Join(cleanCacheDir, "polka", serveTLSSubdir)
}

func certificateFileExists(path string) bool {
	fileInfo, err := os.Stat(path)
	return err == nil && !fileInfo.IsDir()
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

func renderApacheDirectoryIndex(layout serveAppLayout) string {
	return renderNginxIndexNames(layout)
}

func quoteNginxPath(path string) string {
	return strconv.Quote(filepath.ToSlash(path))
}

func quoteApachePath(path string) string {
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

func serveRuntimeLabel(kind string) string {
	trimmed := strings.TrimSpace(kind)
	if trimmed == "" {
		return "webserver"
	}

	return trimmed + " webserver"
}

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

func serveStateURL(state serveRuntimeState) string {
	return serverEndpointURL(serverEndpoint{
		Scheme:  strings.TrimSpace(state.ServerScheme),
		Address: strings.TrimSpace(state.ServerAddress),
	})
}

func serveStateProbeAddress(state serveRuntimeState) string {
	return serveProbeAddress(strings.TrimSpace(state.ServerAddress))
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
		return "", fmt.Errorf("start requires a docroot argument or docroot in the current environment file")
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
