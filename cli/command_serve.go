package cli

// This file contains the serve/start command itself: flag and argument
// resolution and the start workflow, including routing through the global
// router (whose project-facing helpers live in router.go).
// The webserver runtime handling it drives lives in serve_runtime.go and the
// per-server serve_php.go, serve_nginx.go, serve_apache.go, and
// serve_frankenphp.go files; TLS assets live in serve_tls.go.

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	ucli "github.com/urfave/cli/v3"

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
func newServeCommand(ctx *commandContext) *ucli.Command {
	var input serveCommandInput

	cmd := &ucli.Command{
		Name:    "serve",
		Aliases: []string{"start"},
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "server",
				Destination: &input.Server,
				Usage:       "server address override in HOST:PORT form",
			},
			&ucli.BoolFlag{
				Name:        "watch",
				Destination: &input.Watch,
				Usage:       "keep the webserver attached to the current terminal",
			},
		},
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if err := requireMaximumArgs(cmd, 1, "start/serve accepts at most one docroot"); err != nil {
				return err
			}
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			input.Docroot = ""
			if cmd.Args().Len() > 0 {
				input.Docroot = strings.TrimSpace(cmd.Args().First())
			}
			input.Server = strings.TrimSpace(input.Server)
			if cmd.IsSet("server") && input.Server == "" {
				return &statusError{code: 1, err: fmt.Errorf("--server requires a non-empty value")}
			}

			ctx.exitCode = runStart(ctx.stdout, ctx.stderr, store, input)
			return nil
		},
	}
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
	routing, err := resolveServeRouting(store, *current, input)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	// Behind the router TLS terminates at Traefik, so every webserver,
	// including PHP's built-in one, serves plain HTTP.
	if routing == nil && endpoint.HTTPS && serverType == config.ServerTypePHP {
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

	if routing != nil {
		if _, err := ensureRouterRunning(stdout, stderr, store, routing.Dir, routing.Settings); err != nil {
			// Serving must not depend on the router: fall back to binding
			// the webserver directly, as an opted-out environment would.
			fmt.Fprintf(stderr, "warning: the global router is unavailable, serving directly instead: %v\n", err)
			routing = nil
			if endpoint.HTTPS && serverType == config.ServerTypePHP {
				fmt.Fprintln(stderr, "error: https is not supported by the PHP webserver without the global router; use nginx, apache, or frankenphp")
				return 1
			}
		} else {
			endpoint = routing.bindEndpoint()
		}
	}

	liveState, err := loadWebServerState(store.RootDir, current.Name)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if liveState != nil {
		if serveStateMatches(*liveState, endpoint, layout.Docroot, serverType) {
			// Re-publish the route: the router may have been restarted or
			// its routes removed since the webserver started.
			if routing != nil {
				route, err := registerServeRoute(stderr, store, *current, *routing)
				if err != nil {
					fmt.Fprintf(stderr, "error: %v\n", err)
					return 1
				}
				fmt.Fprintf(stdout, "Webserver for environment %q is already running at %s.\n", current.Name, serveStateURL(*liveState))
				writeRouteURLs(stdout, route, routing.Settings)
				return 0
			}
			fmt.Fprintf(stdout, "Webserver for environment %q is already running at %s.\n", current.Name, serveStateURL(*liveState))
			return 0
		}

		fmt.Fprintf(stderr, "error: environment %q already has a running %s at %s; run `polka stop` before starting a different webserver\n", current.Name, serveRuntimeLabel(liveState.ServerKind), serveStateURL(*liveState))
		return 1
	}

	// Publish the route before the webserver starts so it is reachable as
	// soon as it listens; Traefik answers 502 until then.
	var route service.Route
	if routing != nil {
		route, err = registerServeRoute(stderr, store, *current, *routing)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		if input.Watch {
			fmt.Fprintf(stdout, "Serving environment %q at %s.\n", current.Name, routing.bindEndpoint().Public.URL())
			writeRouteURLs(stdout, route, routing.Settings)
			// A foreground webserver owns its route only while it runs.
			defer func() {
				if err := unregisterServeRoute(stdout, store, current.Name); err != nil {
					fmt.Fprintf(stderr, "warning: remove router route: %v\n", err)
				}
			}()
		}
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
		if routing != nil && !input.Watch {
			if unregisterErr := unregisterServeRoute(io.Discard, store, current.Name); unregisterErr != nil {
				fmt.Fprintf(stderr, "warning: remove router route: %v\n", unregisterErr)
			}
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if routing != nil && !input.Watch {
		writeRouteURLs(stdout, route, routing.Settings)
	}

	return exitCode
}

// resolveEnvironmentServerType returns the configured webserver provider.
// An omitted type preserves the legacy nginx-then-PHP selection behavior.
func resolveEnvironmentServerType(environment backend.Environment) (string, error) {
	serverType := ""
	if environment.Server != nil {
		serverType = strings.ToLower(strings.TrimSpace(environment.Server.Type))
	}
	if serverType == "" {
		if strings.TrimSpace(environment.ToolVersion("nginx")) != "" {
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
		if strings.TrimSpace(environment.ToolVersion("nginx")) == "" {
			return "", fmt.Errorf("environment %q selects nginx but does not define an nginx version", environment.Name)
		}
		if backend.PrimaryPHPVersion(environment) == "" {
			return "", fmt.Errorf("environment %q selects nginx but does not define a php or php-zts version", environment.Name)
		}
	case config.ServerTypeApache:
		if strings.TrimSpace(environment.ToolVersion("apache")) == "" {
			return "", fmt.Errorf("environment %q selects apache but does not define an apache version", environment.Name)
		}
		if backend.PrimaryPHPVersion(environment) == "" {
			return "", fmt.Errorf("environment %q selects apache but does not define a php or php-zts version", environment.Name)
		}
	case config.ServerTypeFrankenPHP:
		if strings.TrimSpace(environment.ToolVersion("frankenphp")) == "" {
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
// config and makes it absolute relative to the project. A regular PHP file is
// accepted as an explicit front controller; resolveServeAppLayout converts it
// to the containing document root before webserver startup.
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
	if !fileInfo.IsDir() && !fileInfo.Mode().IsRegular() {
		return "", fmt.Errorf("docroot %q is neither a directory nor a regular file", trimmed)
	}

	return resolved, nil
}
