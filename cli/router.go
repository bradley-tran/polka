package cli

// This file connects projects to the machine-global router: deciding whether
// an environment is routed, provisioning and starting the shared Traefik,
// registering and removing the environment's route (with its certificate),
// reporting router status, and the `polka router` command.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v3"

	"polka/backend"
	"polka/service"
	"polka/tools"
)

// Test seams for the global router lifecycle.
var (
	startRouterServerFunc   = service.StartRouterServer
	stopRouterServerFunc    = service.StopRouterServer
	pingRouterAddressFunc   func(string) bool
	routerPIDIsLiveFunc     func(int) bool
	routerNowFunc           = time.Now
	ensureRouterTraefikFunc = ensureRouterTraefik
	waitForRouteFunc        = service.WaitForRoute
	ensureRouteCertFunc     = ensureRouteCertificate
)

// routeWaitTimeout bounds how long serve waits for Traefik to load a route.
const routeWaitTimeout = 3 * time.Second

// routerRuntimeHooks returns the router process hooks, using the service
// defaults for any seam a test leaves nil.
func routerRuntimeHooks() service.RouterHooks {
	return service.RouterHooks{
		StartServer: startRouterServerFunc,
		StopServer:  stopRouterServerFunc,
		PingAddress: pingRouterAddressFunc,
		PIDIsLive:   routerPIDIsLiveFunc,
		Now:         routerNowFunc,
	}
}

// environmentRouterDir returns the router directory when the environment is
// served through the global router: routing is enabled for this process and
// the environment has not opted out with `server.router: false`.
func environmentRouterDir(store backend.Store, environment backend.Environment) (string, bool) {
	dir := routerDirFunc(store)
	if dir == "" || !environment.Server.UsesRouter() {
		return "", false
	}

	return dir, true
}

// routerSettingsFromState returns settings whose ports match a running router.
func routerSettingsFromState(state service.RouterState) service.RouterSettings {
	settings := service.DefaultRouterSettings()
	settings.HTTPPort = state.HTTPPort
	settings.HTTPSPort = state.HTTPSPort

	return settings
}

// routeHost returns the hostname an environment is routed at: server.hostname
// when set, else <project-dir>.localhost, prefixed with the environment name
// for every environment except the default one.
func routeHost(store backend.Store, environment backend.Environment) (string, error) {
	host := ""
	if environment.Server != nil {
		host = strings.ToLower(strings.TrimSpace(environment.Server.Hostname))
	}
	if host == "" {
		host = backend.ProjectLocalHostname(store.ProjectDir)
	}
	name := strings.ToLower(strings.TrimSpace(environment.Name))
	if name != "" && name != defaultEnvironmentName {
		host = name + "." + host
	}
	if err := service.ValidateRouteHost(host); err != nil {
		return "", err
	}

	return host, nil
}

// environmentServerURL returns the URL users reach the environment's site
// at: the router URL when routed, else the directly bound server URL. The
// boolean reports whether the URL goes through the router.
func environmentServerURL(store backend.Store, environment backend.Environment) (string, bool, error) {
	if dir, routed := environmentRouterDir(store, environment); routed {
		settings, err := service.LoadRouterSettings(dir)
		if err != nil {
			return "", false, err
		}
		host, err := routeHost(store, environment)
		if err != nil {
			return "", false, err
		}

		return service.PublicURL(host, environment.HTTPS, settings), true, nil
	}
	endpoint, err := resolveServerEndpoint(environment.Server, "")
	if err != nil {
		return "", false, err
	}

	return serverEndpointURL(endpoint), false, nil
}

// withoutServiceTLS returns a copy of a routed environment whose services
// serve plain HTTP on loopback: the router terminates TLS in front of them.
func withoutServiceTLS(environment backend.Environment) backend.Environment {
	environment.HTTPS = false
	if environment.Server != nil {
		server := *environment.Server
		server.HTTPS = false
		environment.Server = &server
	}

	return environment
}

// runtimeServiceEnvironment returns the environment managed services run
// with: unpinned ports leased and, when routed, service TLS turned off.
func runtimeServiceEnvironment(store backend.Store, environment backend.Environment) (backend.Environment, error) {
	leased, err := leaseEnvironmentPorts(store, environment)
	if err != nil {
		return environment, err
	}
	if _, routed := environmentRouterDir(store, environment); routed {
		leased = withoutServiceTLS(leased)
	}

	return leased, nil
}

// ensureRouterTraefik provisions the router's Traefik as an internal tool.
func ensureRouterTraefik(stdout io.Writer, store backend.Store) (string, string, error) {
	version, err := store.InternalToolVersion(tools.Traefik)
	if err != nil {
		return "", "", err
	}
	target, err := store.EnsureInternalTool(tools.Traefik, version, internalToolProgress(stdout))
	if err != nil {
		return "", "", err
	}

	return target, version, nil
}

// ensureRouterRunning provisions and starts the shared router when needed.
func ensureRouterRunning(stdout, stderr io.Writer, store backend.Store, dir string, settings service.RouterSettings) (service.RouterState, error) {
	target, version, err := ensureRouterTraefikFunc(stdout, store)
	if err != nil {
		return service.RouterState{}, err
	}
	summary, err := service.EnsureRouterStarted(dir, target, version, settings, routerRuntimeHooks())
	if err != nil {
		return service.RouterState{}, err
	}
	if !summary.AlreadyRunning {
		fmt.Fprintf(stdout, "Started the global router on %s (http) and %s (https).\n", service.RouterAddress(summary.State.HTTPPort), service.RouterAddress(summary.State.HTTPSPort))
	}
	if summary.RestartRequired {
		fmt.Fprintf(stderr, "warning: the running global router differs from %s; run `polka router restart` to apply it.\n", service.RouterSettingsPath(dir))
	}

	return summary.State, nil
}

// serveRouting describes how one serve reaches its environment through the
// router: where the webserver binds and which public host it answers on.
type serveRouting struct {
	Dir      string
	Settings service.RouterSettings
	Host     string
	HTTPS    bool
	WebPort  int
}

// resolveServeRouting decides whether this serve goes through the router.
// It returns nil for opted-out environments and for an explicit --server
// address, which keeps its meaning of binding the webserver directly.
func resolveServeRouting(store backend.Store, environment backend.Environment, input serveCommandInput) (*serveRouting, error) {
	dir, routed := environmentRouterDir(store, environment)
	if !routed || strings.TrimSpace(input.Server) != "" {
		return nil, nil
	}
	settings, err := service.LoadRouterSettings(dir)
	if err != nil {
		return nil, err
	}
	host, err := routeHost(store, environment)
	if err != nil {
		return nil, err
	}
	webPort, err := service.NewPortRegistry(dir, settings.LeaseRange()).Lease(store.RootDir, environment.Name, service.PortKeyWeb)
	if err != nil {
		return nil, fmt.Errorf("lease webserver port: %w", err)
	}

	return &serveRouting{Dir: dir, Settings: settings, Host: host, HTTPS: environment.HTTPS, WebPort: webPort}, nil
}

// bindEndpoint is the loopback address the routed webserver listens on.
func (r serveRouting) bindEndpoint() serverEndpoint {
	scheme := "http"
	port := r.Settings.HTTPPort
	if r.HTTPS {
		scheme = "https"
		port = r.Settings.HTTPSPort
	}

	return serverEndpoint{
		Scheme:  "http",
		Address: net.JoinHostPort(serveProxyHost, strconv.Itoa(r.WebPort)),
		Public:  &service.PublicOrigin{Scheme: scheme, Host: r.Host, Port: port},
	}
}

// route builds the environment's route: the webserver plus the HTTP UIs of
// every configured service, at their (leased) ports.
func (r serveRouting) route(store backend.Store, environment backend.Environment) service.Route {
	route := service.Route{
		ID:          service.RouteID(store.RootDir, environment.Name),
		Root:        store.RootDir,
		Environment: environment.Name,
		Host:        r.Host,
		HTTPS:       r.HTTPS,
		Backends:    []service.RouteBackend{{Name: service.RouteBackendWeb, Port: r.WebPort}},
	}
	route.Backends = append(route.Backends, serviceRouteBackends(environment)...)

	return route
}

// serviceRouteBackends returns the HTTP UIs of the configured services.
func serviceRouteBackends(environment backend.Environment) []service.RouteBackend {
	backends := []service.RouteBackend{}
	if mailpit := environment.Mailpit(); mailpit != nil && strings.TrimSpace(mailpit.Version) != "" {
		backends = append(backends, service.RouteBackend{Name: service.RouteBackendMailpit, Port: service.EffectiveMailpitUIPort(mailpit)})
	}
	if phpMyAdmin := environment.PHPMyAdmin(); phpMyAdmin != nil && strings.TrimSpace(phpMyAdmin.Version) != "" {
		backends = append(backends, service.RouteBackend{Name: service.RouteBackendPHPMyAdmin, Port: service.EffectivePHPMyAdminPort(phpMyAdmin)})
	}
	if meilisearch := environment.Meilisearch(); meilisearch != nil && strings.TrimSpace(meilisearch.Version) != "" {
		backends = append(backends, service.RouteBackend{Name: service.RouteBackendMeilisearch, Port: service.EffectiveMeilisearchPort(meilisearch)})
	}
	if rabbitMQ := environment.RabbitMQ(); rabbitMQ != nil && strings.TrimSpace(rabbitMQ.Version) != "" {
		backends = append(backends, service.RouteBackend{Name: service.RouteBackendRabbitMQ, Port: service.EffectiveRabbitMQManagementPort(rabbitMQ)})
	}

	return backends
}

// registerServeRoute publishes the environment's route, issuing its
// certificate for HTTPS, and waits briefly for Traefik to load it.
func registerServeRoute(stderr io.Writer, store backend.Store, environment backend.Environment, routing serveRouting) (service.Route, error) {
	leased, err := leaseEnvironmentPorts(store, environment)
	if err != nil {
		return service.Route{}, err
	}
	route := routing.route(store, leased)
	if routing.HTTPS {
		certFile, keyFile, err := ensureRouteCertFunc(store.CacheDir, routing.Dir, route.ID, routing.Host)
		if err != nil {
			return service.Route{}, err
		}
		route.CertFile, route.KeyFile = certFile, keyFile
	}
	if err := service.RegisterRoute(routing.Dir, route, routing.Settings, routerNowFunc()); err != nil {
		return service.Route{}, err
	}
	if err := waitForRouteFunc(routing.Settings.HTTPPort, routing.Host, routeWaitTimeout); err != nil {
		fmt.Fprintf(stderr, "warning: %v; see %s\n", err, service.RouterLogPath(routing.Dir))
	}

	return route, nil
}

// writeRouteURLs prints the public URLs of a route's service UIs.
func writeRouteURLs(stdout io.Writer, route service.Route, settings service.RouterSettings) {
	for _, backend := range route.Backends {
		if backend.Name == service.RouteBackendWeb {
			continue
		}
		fmt.Fprintf(stdout, "  %s: %s\n", routeBackendLabel(backend.Name), service.PublicURL(route.Hostname(backend), route.HTTPS, settings))
	}
}

// routeBackendLabel names a route backend for users.
func routeBackendLabel(name string) string {
	switch name {
	case service.RouteBackendMailpit:
		return "Mailpit"
	case service.RouteBackendPHPMyAdmin:
		return "phpMyAdmin"
	case service.RouteBackendMeilisearch:
		return "Meilisearch"
	case service.RouteBackendRabbitMQ:
		return "RabbitMQ management"
	default:
		return "webserver"
	}
}

// unregisterServeRoute removes the environment's route and stops the shared
// router once no project routes through it any more.
func unregisterServeRoute(stdout io.Writer, store backend.Store, environmentName string) error {
	dir := routerDirFunc(store)
	if dir == "" {
		return nil
	}
	remaining, removed, err := service.UnregisterRoute(dir, store.RootDir, environmentName)
	if err != nil {
		return err
	}
	if removed {
		fmt.Fprintf(stdout, "Removed the router route for environment %q.\n", environmentName)
	}
	if remaining > 0 {
		return nil
	}
	_, alreadyStopped, err := service.StopRouter(dir, routerRuntimeHooks())
	if err != nil {
		return err
	}
	if !alreadyStopped {
		fmt.Fprintln(stdout, "Stopped the global router; no project routes through it.")
	}

	return nil
}

// stopRouterRouteHook runs during `polka stop` after the webserver and
// services stop. It also cleans up a per-project Traefik left running by an
// older Polka release.
func stopRouterRouteHook(ctx stopHookContext) error {
	stopLegacyProjectTraefik(ctx.Stdout, ctx.Stderr, ctx.Store, ctx.Environment.Name)

	return unregisterServeRoute(ctx.Stdout, ctx.Store, ctx.Environment.Name)
}

// legacyTraefikState is the state file shape of the removed per-project
// Traefik service, read only to stop a process an older release started.
type legacyTraefikState struct {
	PID int `json:"pid"`
}

// stopLegacyProjectTraefik stops a per-project Traefik recorded under
// <root>/run/traefik/<env>.json and removes its runtime files. Failures are
// warnings: the legacy process must never block stopping the environment.
func stopLegacyProjectTraefik(stdout, stderr io.Writer, store backend.Store, environmentName string) {
	statePath := filepath.Join(store.RootDir, "run", "traefik", environmentName+".json")
	var state legacyTraefikState
	if err := readJSONFile(statePath, &state); err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "warning: read legacy traefik state: %v\n", err)
		}
		return
	}
	if err := stopServePID(state.PID); err != nil {
		fmt.Fprintf(stderr, "warning: stop legacy per-project traefik: %v\n", err)
		return
	}
	for _, path := range []string{statePath, filepath.Join(store.RootDir, "run", "traefik", environmentName)} {
		if err := os.RemoveAll(path); err != nil {
			fmt.Fprintf(stderr, "warning: remove legacy traefik files: %v\n", err)
		}
	}
	fmt.Fprintf(stdout, "Stopped the legacy per-project Traefik for environment %q; the global router replaces it.\n", environmentName)
}

// readJSONFile decodes a JSON file into value, returning os errors unwrapped
// so callers can test for a missing file.
func readJSONFile(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}

	return nil
}

// statusRouterRuntimeHook reports the shared router and this environment's
// public URLs in `polka status`.
func statusRouterRuntimeHook(ctx statusHookContext) error {
	dir, routed := environmentRouterDir(ctx.Store, ctx.Environment)
	if !routed {
		_, _ = fmt.Fprintln(ctx.Stdout, "router unset")
		return nil
	}
	state, err := service.LoadLiveRouterState(dir, routerRuntimeHooks())
	if err != nil {
		return err
	}
	if state == nil {
		_, _ = fmt.Fprintln(ctx.Stdout, "router stopped")
		return nil
	}
	routes, err := service.ListRoutes(dir)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(ctx.Stdout, "router running http=%d https=%d routes=%d\n", state.HTTPPort, state.HTTPSPort, len(routes))
	route, err := service.LookupRoute(dir, ctx.Store.RootDir, ctx.Environment.Name)
	if err != nil || route == nil {
		return err
	}
	settings := routerSettingsFromState(*state)
	for _, backend := range route.Backends {
		_, _ = fmt.Fprintf(ctx.Stdout, "route %s %s\n", routeBackendStatusName(backend.Name), service.PublicURL(route.Hostname(backend), route.HTTPS, settings))
	}

	return nil
}

// routeBackendStatusName is the stable status token for a route backend.
func routeBackendStatusName(name string) string {
	if name == service.RouteBackendWeb {
		return "webserver"
	}

	return name
}

// newRouterCommand builds `polka router [status|stop|restart|logs]`.
func newRouterCommand(ctx *commandContext) *ucli.Command {
	var follow bool
	cmd := &ucli.Command{
		Name: "router",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{
				Name:        "follow",
				Aliases:     []string{"f"},
				Destination: &follow,
				Usage:       "follow the router log (logs only)",
			},
		},
		Action: func(runCtx context.Context, cmd *ucli.Command) error {
			if err := requireMaximumArgs(cmd, 1, "router accepts at most one subcommand"); err != nil {
				return err
			}
			store, err := ctx.initStore()
			if err != nil {
				return &statusError{code: 1, err: err}
			}
			action := "status"
			if cmd.Args().Len() > 0 {
				action = strings.ToLower(strings.TrimSpace(cmd.Args().First()))
			}
			ctx.exitCode = runRouter(runCtx, ctx.stdout, ctx.stderr, store, action, follow)
			return nil
		},
	}
	configureCommand(cmd, routerUsage)

	return cmd
}

// runRouter executes one router subcommand.
func runRouter(ctx context.Context, stdout, stderr io.Writer, store backend.Store, action string, follow bool) int {
	dir := routerDirFunc(store)
	if dir == "" {
		fmt.Fprintln(stderr, "error: the global router is not available")
		return 1
	}
	var err error
	switch action {
	case "status":
		err = writeRouterStatus(stdout, store, dir)
	case "stop":
		var alreadyStopped bool
		_, alreadyStopped, err = service.StopRouter(dir, routerRuntimeHooks())
		if err == nil && alreadyStopped {
			fmt.Fprintln(stdout, "The global router is already stopped.")
		} else if err == nil {
			fmt.Fprintln(stdout, "Stopped the global router. Routes are kept and served again on the next `polka serve`.")
		}
	case "restart":
		err = restartRouter(stdout, stderr, store, dir)
	case "logs":
		return runRouterLogs(ctx, stdout, stderr, dir, follow)
	default:
		fmt.Fprintf(stderr, "error: unknown router subcommand %q; use status, stop, restart, or logs\n", action)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	return 0
}

// writeRouterStatus prints the router state, every route, and the current
// project's port leases.
func writeRouterStatus(stdout io.Writer, store backend.Store, dir string) error {
	settings, err := service.LoadRouterSettings(dir)
	if err != nil {
		return err
	}
	state, err := service.LoadLiveRouterState(dir, routerRuntimeHooks())
	if err != nil {
		return err
	}
	if state == nil {
		fmt.Fprintf(stdout, "router stopped (configured http=%d https=%d in %s)\n", settings.HTTPPort, settings.HTTPSPort, service.RouterSettingsPath(dir))
	} else {
		settings = routerSettingsFromState(*state)
		fmt.Fprintf(stdout, "router running http=%d https=%d pid %d\n", state.HTTPPort, state.HTTPSPort, state.PID)
	}

	routes, err := service.ListRoutes(dir)
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		fmt.Fprintln(stdout, "routes none")
	}
	for _, route := range routes {
		fmt.Fprintf(stdout, "route %s -> %s (environment %s)\n", service.PublicURL(route.Host, route.HTTPS, settings), filepath.Dir(route.Root), route.Environment)
		for _, backend := range route.Backends {
			fmt.Fprintf(stdout, "  %s %s -> 127.0.0.1:%d\n", routeBackendStatusName(backend.Name), route.Hostname(backend), backend.Port)
		}
	}

	if strings.TrimSpace(store.ProjectDir) == "" {
		return nil
	}
	leases, err := service.NewPortRegistry(dir, settings.LeaseRange()).Leases(store.RootDir)
	if err != nil {
		return err
	}
	for _, lease := range leases {
		fmt.Fprintf(stdout, "lease %s %s 127.0.0.1:%d\n", lease.Environment, lease.Key, lease.Port)
	}

	return nil
}

// restartRouter stops the router and starts it again with the current
// router.yaml when any project still routes through it.
func restartRouter(stdout, stderr io.Writer, store backend.Store, dir string) error {
	settings, err := service.LoadRouterSettings(dir)
	if err != nil {
		return err
	}
	if _, _, err := service.StopRouter(dir, routerRuntimeHooks()); err != nil {
		return err
	}
	routes, err := service.ListRoutes(dir)
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		fmt.Fprintln(stdout, "No project routes through the global router; it will start on the next `polka serve`.")
		return nil
	}
	for _, route := range routes {
		// Redirect ports live in the route files, so re-render them for
		// the current settings before Traefik loads them.
		if err := service.RegisterRoute(dir, route, settings, routerNowFunc()); err != nil {
			return err
		}
	}
	_, err = ensureRouterRunning(stdout, stderr, store, dir, settings)

	return err
}

// runRouterLogs prints or follows the shared router log.
func runRouterLogs(ctx context.Context, stdout, stderr io.Writer, dir string, follow bool) int {
	path := service.RouterLogPath(dir)
	if follow {
		if _, err := followLogFiles(ctx, stdout, []string{path}, logFollowPollInterval); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		return 0
	}
	wrote, err := writeLogFile(stdout, path)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if !wrote {
		fmt.Fprintf(stderr, "error: no router log found at %s\n", path)
		return 1
	}

	return 0
}
