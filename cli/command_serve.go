package cli

// This file contains the serve/start command itself: flag and argument
// resolution, the start workflow, and the optional Traefik front proxy setup.
// The webserver runtime handling it drives lives in serve_runtime.go and the
// per-server serve_php.go, serve_nginx.go, serve_apache.go, and
// serve_frankenphp.go files; TLS assets live in serve_tls.go.

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
	"polka/config"
	"polka/service"
)

const (
	defaultServeHostname = "localhost"
	defaultServePort     = 8000
)

// serveCommandInput carries the resolved serve command flags and arguments.
type serveCommandInput struct {
	Docroot string
	Server  string
	Watch   bool
}

// newServeCommand builds the serve/start command.
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

// runStart resolves the environment webserver configuration, starts configured
// services, and hands off to the webserver start hooks.
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

// resolveServerEndpoint combines the configured server address (or override)
// with the configured HTTPS setting.
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

// resolveServeAddress picks the serve address from the override flag, the
// environment config, or the defaults, and validates it.
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

// serverEndpointURL renders an endpoint as a URL, defaulting to http.
func serverEndpointURL(endpoint serverEndpoint) string {
	scheme := strings.TrimSpace(endpoint.Scheme)
	if scheme == "" {
		scheme = "http"
	}

	return scheme + "://" + strings.TrimSpace(endpoint.Address)
}

// splitServerAddress parses and validates a HOST:PORT value.
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

// validateServeHostAndPort rejects empty hostnames and out-of-range ports.
func validateServeHostAndPort(host string, port int) error {
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("server hostname cannot be empty")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("server port must be between 1 and 65535")
	}

	return nil
}

// resolveServeDocroot resolves the docroot from the argument or environment
// config, makes it absolute relative to the project, and requires a directory.
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
