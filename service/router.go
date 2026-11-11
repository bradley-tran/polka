package service

// This file implements the machine-global router: a single Traefik process
// shared by every project that routes requests by Host header to each
// project environment's webserver and HTTP UIs, which bind leased loopback
// ports. Projects register one route per environment as a pair of files in
// the router directory: a Traefik file-provider document under dynamic/ and
// a JSON sidecar under routes/ that records ownership for conflict checks.
// Traefik watches dynamic/ and hot-reloads, so registering a route never
// restarts the shared process.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"polka/tools"
)

const (
	// RouterListenHost keeps the router on loopback so development sites are
	// never exposed to the local network.
	RouterListenHost       = "127.0.0.1"
	DefaultRouterHTTPPort  = 8080
	DefaultRouterHTTPSPort = 8443

	routerSettingsFileName    = "router.yaml"
	routerStateFileName       = "state.json"
	routerLogFileName         = "traefik.log"
	routerDynamicDirName      = "dynamic"
	routerRoutesDirName       = "routes"
	routerCertsDirName        = "certs"
	routerLockDirName         = "routes.lock.d"
	routerWebEntrypoint       = "web"
	routerWebSecureEntrypoint = "websecure"
	routerPollInterval        = 100 * time.Millisecond
	routerStartupTimeout      = 10 * time.Second
	routerShutdownTimeout     = 5 * time.Second
	routerNotFoundBody        = "404 page not found"
)

// Route backend names. The empty name is the environment webserver; the
// others are served on a subdomain of the route host, such as
// mailpit.blog.localhost.
const (
	RouteBackendWeb         = ""
	RouteBackendMailpit     = "mailpit"
	RouteBackendPHPMyAdmin  = "pma"
	RouteBackendMeilisearch = "meilisearch"
	RouteBackendRabbitMQ    = "rabbitmq"
)

// routeHostPattern restricts route hostnames to lowercase DNS labels so they
// can be embedded in Traefik rules without escaping.
var routeHostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// RouterSettings is the user-editable global router configuration stored in
// router.yaml. Port changes take effect on the next router (re)start.
type RouterSettings struct {
	HTTPPort  int `yaml:"http-port"`
	HTTPSPort int `yaml:"https-port"`
	PortMin   int `yaml:"lease-port-min"`
	PortMax   int `yaml:"lease-port-max"`
}

// DefaultRouterSettings returns the settings written on first use.
func DefaultRouterSettings() RouterSettings {
	return RouterSettings{
		HTTPPort:  DefaultRouterHTTPPort,
		HTTPSPort: DefaultRouterHTTPSPort,
		PortMin:   DefaultLeasePortMin,
		PortMax:   DefaultLeasePortMax,
	}
}

// LeaseRange returns the configured port lease range.
func (s RouterSettings) LeaseRange() PortRange {
	return PortRange{Min: s.PortMin, Max: s.PortMax}
}

// validate rejects unusable or overlapping port settings.
func (s RouterSettings) validate() error {
	for name, port := range map[string]int{"http-port": s.HTTPPort, "https-port": s.HTTPSPort} {
		if port < 1 || port > 65535 {
			return fmt.Errorf("%s must be between 1 and 65535", name)
		}
	}
	if s.HTTPPort == s.HTTPSPort {
		return fmt.Errorf("http-port and https-port must differ")
	}
	leases := s.LeaseRange()
	if !leases.Valid() {
		return fmt.Errorf("lease-port-min and lease-port-max must form a valid port range")
	}
	for _, port := range []int{s.HTTPPort, s.HTTPSPort} {
		if port >= leases.Min && port <= leases.Max {
			return fmt.Errorf("router port %d must lie outside the lease range %d-%d", port, leases.Min, leases.Max)
		}
	}

	return nil
}

// RouterSettingsPath returns the router.yaml path under a router directory.
func RouterSettingsPath(dir string) string {
	return filepath.Join(dir, routerSettingsFileName)
}

// LoadRouterSettings reads router.yaml, writing the defaults on first use so
// users can find and edit the file. Omitted keys keep their defaults.
func LoadRouterSettings(dir string) (RouterSettings, error) {
	path := RouterSettingsPath(dir)
	settings := DefaultRouterSettings()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := writeRouterSettings(path, settings); err != nil {
			return RouterSettings{}, err
		}
		return settings, nil
	}
	if err != nil {
		return RouterSettings{}, fmt.Errorf("read router settings %s: %w", path, err)
	}
	if err := yaml.UnmarshalWithOptions(data, &settings, yaml.Strict()); err != nil {
		return RouterSettings{}, fmt.Errorf("decode router settings %s: %w", path, err)
	}
	if err := settings.validate(); err != nil {
		return RouterSettings{}, fmt.Errorf("router settings %s: %w", path, err)
	}

	return settings, nil
}

// writeRouterSettings persists settings with an explanatory header.
func writeRouterSettings(path string, settings RouterSettings) error {
	data, err := yaml.Marshal(settings)
	if err != nil {
		return fmt.Errorf("encode router settings: %w", err)
	}
	header := "# Polka global router settings, shared by every project on this machine.\n" +
		"# http-port/https-port: where the router listens on 127.0.0.1; run `polka router restart` after changing them.\n" +
		"# lease-port-min/lease-port-max: the range automatically leased service ports come from.\n"

	return writeFileAtomic(path, append([]byte(header), data...), 0o644)
}

// RouterState records the running shared router process.
type RouterState struct {
	PID       int       `json:"pid"`
	Version   string    `json:"version"`
	HTTPPort  int       `json:"http_port"`
	HTTPSPort int       `json:"https_port"`
	LogPath   string    `json:"log_path,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// RouterSpec describes one router process launch.
type RouterSpec struct {
	Dir       string
	Target    string
	Version   string
	HTTPPort  int
	HTTPSPort int
	LogPath   string
}

// RouterStartResult carries the started router process ID.
type RouterStartResult struct {
	PID int
}

// RouterHooks are the process seams for the router lifecycle.
type RouterHooks struct {
	StartServer func(RouterSpec) (RouterStartResult, error)
	StopServer  func(RouterState) error
	PingAddress func(string) bool
	PIDIsLive   func(int) bool
	Now         func() time.Time
}

// RouterStartSummary reports how EnsureRouterStarted found the router.
type RouterStartSummary struct {
	State          RouterState
	AlreadyRunning bool
	// RestartRequired is set when the running router uses a different
	// version or ports than requested; it keeps serving until restarted.
	RestartRequired bool
}

func (hooks RouterHooks) withDefaults() RouterHooks {
	if hooks.StartServer == nil {
		hooks.StartServer = StartRouterServer
	}
	if hooks.StopServer == nil {
		hooks.StopServer = StopRouterServer
	}
	if hooks.PingAddress == nil {
		hooks.PingAddress = pingTCPAddress
	}
	if hooks.PIDIsLive == nil {
		hooks.PIDIsLive = servicePIDIsLive
	}
	if hooks.Now == nil {
		hooks.Now = time.Now
	}

	return hooks
}

// RouterAddress returns the loopback address of a router entrypoint port.
func RouterAddress(port int) string {
	return net.JoinHostPort(RouterListenHost, strconv.Itoa(port))
}

// RouterStatePath returns the router state file path.
func RouterStatePath(dir string) string {
	return filepath.Join(dir, routerStateFileName)
}

// RouterLogPath returns the shared router log path.
func RouterLogPath(dir string) string {
	return filepath.Join(dir, routerLogFileName)
}

// RouterDynamicDir returns the directory Traefik's file provider watches.
func RouterDynamicDir(dir string) string {
	return filepath.Join(dir, routerDynamicDirName)
}

// RouteCertificatePaths returns where a route's certificate and key live.
func RouteCertificatePaths(dir, routeID string) (string, string) {
	certs := filepath.Join(dir, routerCertsDirName)
	return filepath.Join(certs, routeID+".crt"), filepath.Join(certs, routeID+".key")
}

// RouterArgs configures Traefik entirely from the command line: plain HTTP
// and TLS entrypoints on loopback plus a file provider watching dynamic/.
func RouterArgs(spec RouterSpec) []string {
	return []string{
		"--entrypoints." + routerWebEntrypoint + ".address=" + RouterAddress(spec.HTTPPort),
		"--entrypoints." + routerWebSecureEntrypoint + ".address=" + RouterAddress(spec.HTTPSPort),
		"--providers.file.directory=" + RouterDynamicDir(spec.Dir),
		"--providers.file.watch=true",
		"--global.checknewversion=false",
		"--global.sendanonymoususage=false",
	}
}

// EnsureRouterStarted starts the shared router unless a live one exists.
// A live router with a different version or ports keeps running and is
// reported through RestartRequired instead of failing the caller.
func EnsureRouterStarted(dir, target, version string, settings RouterSettings, hooks RouterHooks) (RouterStartSummary, error) {
	hooks = hooks.withDefaults()
	state, err := LoadLiveRouterState(dir, hooks)
	if err != nil {
		return RouterStartSummary{}, err
	}
	if state != nil {
		restart := strings.TrimSpace(state.Version) != strings.TrimSpace(version) ||
			state.HTTPPort != settings.HTTPPort || state.HTTPSPort != settings.HTTPSPort
		return RouterStartSummary{State: *state, AlreadyRunning: true, RestartRequired: restart}, nil
	}

	spec := RouterSpec{
		Dir:       dir,
		Target:    target,
		Version:   version,
		HTTPPort:  settings.HTTPPort,
		HTTPSPort: settings.HTTPSPort,
		LogPath:   RouterLogPath(dir),
	}
	result, err := hooks.StartServer(spec)
	if err != nil {
		return RouterStartSummary{}, err
	}
	started := RouterState{
		PID:       result.PID,
		Version:   version,
		HTTPPort:  spec.HTTPPort,
		HTTPSPort: spec.HTTPSPort,
		LogPath:   spec.LogPath,
		StartedAt: hooks.Now().UTC(),
	}
	if err := writeRouterState(RouterStatePath(dir), started); err != nil {
		_ = hooks.StopServer(started)
		return RouterStartSummary{}, err
	}

	return RouterStartSummary{State: started}, nil
}

// StopRouter stops the shared router. The boolean reports that nothing was running.
func StopRouter(dir string, hooks RouterHooks) (RouterState, bool, error) {
	hooks = hooks.withDefaults()
	state, err := LoadLiveRouterState(dir, hooks)
	if err != nil {
		return RouterState{}, false, err
	}
	if state == nil {
		return RouterState{}, true, nil
	}
	if err := hooks.StopServer(*state); err != nil {
		return RouterState{}, false, err
	}
	if err := os.Remove(RouterStatePath(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return RouterState{}, false, fmt.Errorf("remove router state: %w", err)
	}

	return *state, false, nil
}

// LoadLiveRouterState returns the recorded router when its process is alive
// and its HTTP entrypoint accepts connections, clearing stale state. Both
// checks are required because the router port is shared machine-wide and
// could be held by an unrelated process.
func LoadLiveRouterState(dir string, hooks RouterHooks) (*RouterState, error) {
	hooks = hooks.withDefaults()
	path := RouterStatePath(dir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read router state %s: %w", path, err)
	}
	var state RouterState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode router state %s: %w", path, err)
	}
	if state.PID > 0 && hooks.PIDIsLive(state.PID) && hooks.PingAddress(RouterAddress(state.HTTPPort)) {
		return &state, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale router state: %w", err)
	}

	return nil, nil
}

// writeRouterState persists the router state atomically.
func writeRouterState(path string, state RouterState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode router state: %w", err)
	}

	return writeFileAtomic(path, append(data, '\n'), 0o644)
}

// StartRouterServer launches Traefik and waits for its HTTP entrypoint.
func StartRouterServer(spec RouterSpec) (RouterStartResult, error) {
	if err := ensureAddressesFree("router", pingTCPAddress, RouterAddress(spec.HTTPPort), RouterAddress(spec.HTTPSPort)); err != nil {
		return RouterStartResult{}, fmt.Errorf("%w; choose other ports in %s", err, RouterSettingsPath(spec.Dir))
	}
	if err := os.MkdirAll(RouterDynamicDir(spec.Dir), 0o755); err != nil {
		return RouterStartResult{}, fmt.Errorf("create router config directory: %w", err)
	}
	logFile, err := openServiceLog(spec.LogPath)
	if err != nil {
		return RouterStartResult{}, err
	}
	defer logFile.Close()

	command, err := prepareServiceCommand(spec.Target, RouterArgs(spec))
	if err != nil {
		return RouterStartResult{}, err
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		return RouterStartResult{}, fmt.Errorf("start router: %w", err)
	}

	deadline := time.Now().Add(routerStartupTimeout)
	for !pingTCPAddress(RouterAddress(spec.HTTPPort)) {
		if time.Now().After(deadline) {
			stopServiceProcess(command.Process)
			return RouterStartResult{}, fmt.Errorf("router did not start listening on %s within %s (see %s)", RouterAddress(spec.HTTPPort), routerStartupTimeout, spec.LogPath)
		}
		time.Sleep(routerPollInterval)
	}
	pid := command.Process.Pid
	_ = command.Process.Release()

	return RouterStartResult{PID: pid}, nil
}

// StopRouterServer stops the router process and waits for its port to close.
func StopRouterServer(state RouterState) error {
	if err := stopServicePID(state.PID); err != nil {
		return err
	}
	deadline := time.Now().Add(routerShutdownTimeout)
	for pingTCPAddress(RouterAddress(state.HTTPPort)) {
		if time.Now().After(deadline) {
			return fmt.Errorf("router did not stop listening on %s within %s", RouterAddress(state.HTTPPort), routerShutdownTimeout)
		}
		time.Sleep(routerPollInterval)
	}

	return nil
}

// pingTCPAddress reports whether the address accepts TCP connections.
func pingTCPAddress(address string) bool {
	connection, err := net.DialTimeout("tcp", address, routerPollInterval)
	if err != nil {
		return false
	}
	_ = connection.Close()

	return true
}

// RouteBackend is one HTTP listener of a project environment.
type RouteBackend struct {
	// Name is RouteBackendWeb for the webserver or a service subdomain.
	Name string `json:"name"`
	Port int    `json:"port"`
}

// Route is everything the router needs to reach one project environment.
type Route struct {
	ID          string         `json:"id"`
	Root        string         `json:"root"`
	Environment string         `json:"environment"`
	Host        string         `json:"host"`
	HTTPS       bool           `json:"https"`
	Backends    []RouteBackend `json:"backends"`
	CertFile    string         `json:"cert_file,omitempty"`
	KeyFile     string         `json:"key_file,omitempty"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// Hostname returns the public hostname of one backend of the route.
func (route Route) Hostname(backend RouteBackend) string {
	if backend.Name == RouteBackendWeb {
		return route.Host
	}

	return backend.Name + "." + route.Host
}

// Hostnames returns every public hostname the route serves.
func (route Route) Hostnames() []string {
	names := make([]string, 0, len(route.Backends))
	for _, backend := range route.Backends {
		names = append(names, route.Hostname(backend))
	}

	return names
}

// PublicURL returns the URL a browser uses for one hostname of the route.
func PublicURL(host string, https bool, settings RouterSettings) string {
	if https {
		return "https://" + net.JoinHostPort(host, strconv.Itoa(settings.HTTPSPort))
	}

	return "http://" + net.JoinHostPort(host, strconv.Itoa(settings.HTTPPort))
}

// RouteID derives a stable, Traefik-safe identifier for one project
// environment. The hash of the absolute state root keeps two projects with the
// same directory name apart.
func RouteID(rootDir, environment string) string {
	root := normalizeLeaseRoot(rootDir)
	digest := sha256.Sum256([]byte(strings.ToLower(root)))
	project := filepath.Base(filepath.Dir(root))

	return sanitizeRouteName(project) + "-" + sanitizeRouteName(normalizeLeaseEnvironment(environment)) + "-" + hex.EncodeToString(digest[:4])
}

// sanitizeRouteName keeps letters, digits, and dashes, lowercased.
func sanitizeRouteName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "project"
	}

	return b.String()
}

// ValidateRouteHost rejects hostnames that cannot be routed safely.
func ValidateRouteHost(host string) error {
	if !routeHostPattern.MatchString(host) || len(host) > 253 {
		return fmt.Errorf("route hostname %q must be a lowercase DNS name such as blog.localhost", host)
	}

	return nil
}

// RenderRouteConfig renders the Traefik file-provider document for a route.
// HTTPS routes serve on the TLS entrypoint and redirect plain HTTP to it.
func RenderRouteConfig(route Route, settings RouterSettings) []byte {
	var b strings.Builder
	b.WriteString("# Managed by Polka for " + strconv.Quote(route.Root) + " (environment " + strconv.Quote(route.Environment) + "). Do not edit.\n")
	b.WriteString("http:\n")
	b.WriteString("  routers:\n")
	for _, backend := range route.Backends {
		name := routeComponentName(route, backend)
		rule := "Host(`" + route.Hostname(backend) + "`)"
		b.WriteString("    " + name + ":\n")
		b.WriteString("      rule: " + strconv.Quote(rule) + "\n")
		if route.HTTPS {
			b.WriteString("      entryPoints: [" + routerWebSecureEntrypoint + "]\n")
		} else {
			b.WriteString("      entryPoints: [" + routerWebEntrypoint + "]\n")
		}
		b.WriteString("      service: " + name + "\n")
		if route.HTTPS {
			b.WriteString("      tls: {}\n")
			b.WriteString("    " + name + "-redirect:\n")
			b.WriteString("      rule: " + strconv.Quote(rule) + "\n")
			b.WriteString("      entryPoints: [" + routerWebEntrypoint + "]\n")
			b.WriteString("      middlewares: [" + route.ID + "-https]\n")
			b.WriteString("      service: " + name + "\n")
		}
	}
	if route.HTTPS {
		b.WriteString("  middlewares:\n")
		b.WriteString("    " + route.ID + "-https:\n")
		b.WriteString("      redirectScheme:\n")
		b.WriteString("        scheme: https\n")
		b.WriteString("        port: " + strconv.Quote(strconv.Itoa(settings.HTTPSPort)) + "\n")
	}
	b.WriteString("  services:\n")
	for _, backend := range route.Backends {
		b.WriteString("    " + routeComponentName(route, backend) + ":\n")
		b.WriteString("      loadBalancer:\n")
		b.WriteString("        servers:\n")
		b.WriteString("          - url: " + strconv.Quote("http://"+net.JoinHostPort(RouterListenHost, strconv.Itoa(backend.Port))) + "\n")
	}
	if route.HTTPS && route.CertFile != "" && route.KeyFile != "" {
		b.WriteString("tls:\n")
		b.WriteString("  certificates:\n")
		b.WriteString("    - certFile: " + strconv.Quote(filepath.ToSlash(route.CertFile)) + "\n")
		b.WriteString("      keyFile: " + strconv.Quote(filepath.ToSlash(route.KeyFile)) + "\n")
	}

	return []byte(b.String())
}

// routeComponentName names the Traefik router and service of one backend.
func routeComponentName(route Route, backend RouteBackend) string {
	if backend.Name == RouteBackendWeb {
		return route.ID
	}

	return route.ID + "-" + backend.Name
}

// RouteConflictError reports a hostname already routed to another project
// environment.
type RouteConflictError struct {
	Host  string
	Owner Route
}

func (e *RouteConflictError) Error() string {
	return fmt.Sprintf("hostname %s is already routed to environment %q of %s; set a different server.hostname", e.Host, e.Owner.Environment, filepath.Dir(e.Owner.Root))
}

// RegisterRoute validates and publishes a route. It fails with a
// *RouteConflictError when another project environment owns any of its
// hostnames. Files are written atomically so Traefik never reads a partial
// document.
func RegisterRoute(dir string, route Route, settings RouterSettings, now time.Time) error {
	route.Root = normalizeLeaseRoot(route.Root)
	route.Environment = normalizeLeaseEnvironment(route.Environment)
	if route.ID == "" {
		route.ID = RouteID(route.Root, route.Environment)
	}
	if err := ValidateRouteHost(route.Host); err != nil {
		return err
	}
	if len(route.Backends) == 0 {
		return fmt.Errorf("route %s has no backends", route.ID)
	}
	route.UpdatedAt = now.UTC()

	lock, err := tools.AcquireDirLock(filepath.Join(dir, routerLockDirName))
	if err != nil {
		return fmt.Errorf("lock router routes: %w", err)
	}
	defer lock.Release()

	routes, err := ListRoutes(dir)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, host := range route.Hostnames() {
		wanted[host] = true
	}
	for _, existing := range routes {
		if existing.ID == route.ID {
			continue
		}
		for _, host := range existing.Hostnames() {
			if wanted[host] {
				return &RouteConflictError{Host: host, Owner: existing}
			}
		}
	}

	sidecar, err := json.MarshalIndent(route, "", "  ")
	if err != nil {
		return fmt.Errorf("encode route %s: %w", route.ID, err)
	}
	if err := writeFileAtomic(routeSidecarPath(dir, route.ID), append(sidecar, '\n'), 0o644); err != nil {
		return err
	}

	return writeFileAtomic(routeConfigPath(dir, route.ID), RenderRouteConfig(route, settings), 0o644)
}

// UnregisterRoute removes a project environment's route. It returns the
// number of routes that remain and whether a route was removed.
func UnregisterRoute(dir, rootDir, environment string) (int, bool, error) {
	id := RouteID(rootDir, environment)
	lock, err := tools.AcquireDirLock(filepath.Join(dir, routerLockDirName))
	if err != nil {
		return 0, false, fmt.Errorf("lock router routes: %w", err)
	}
	defer lock.Release()

	removed := false
	for _, path := range []string{routeConfigPath(dir, id), routeSidecarPath(dir, id)} {
		err := os.Remove(path)
		switch {
		case err == nil:
			removed = true
		case !errors.Is(err, os.ErrNotExist):
			return 0, false, fmt.Errorf("remove route file %s: %w", path, err)
		}
	}
	routes, err := ListRoutes(dir)
	if err != nil {
		return 0, removed, err
	}

	return len(routes), removed, nil
}

// LookupRoute returns the registered route of a project environment.
func LookupRoute(dir, rootDir, environment string) (*Route, error) {
	route, err := readRoute(routeSidecarPath(dir, RouteID(rootDir, environment)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &route, nil
}

// ListRoutes returns every registered route sorted by host.
func ListRoutes(dir string) ([]Route, error) {
	entries, err := os.ReadDir(filepath.Join(dir, routerRoutesDirName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read router routes: %w", err)
	}
	routes := make([]Route, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		route, err := readRoute(filepath.Join(dir, routerRoutesDirName, entry.Name()))
		if err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Host < routes[j].Host })

	return routes, nil
}

// readRoute decodes one route sidecar.
func readRoute(path string) (Route, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Route{}, err
	}
	var route Route
	if err := json.Unmarshal(data, &route); err != nil {
		return Route{}, fmt.Errorf("decode route %s: %w", path, err)
	}

	return route, nil
}

func routeSidecarPath(dir, id string) string {
	return filepath.Join(dir, routerRoutesDirName, id+".json")
}

func routeConfigPath(dir, id string) string {
	return filepath.Join(RouterDynamicDir(dir), id+".yml")
}

// routeProbeClient never follows redirects, so an HTTPS route's redirect
// counts as a routed response.
var routeProbeClient = &http.Client{
	Timeout: time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// WaitForRoute polls the router's HTTP entrypoint with the route's Host
// header until Traefik has loaded the route, recognized by any response other
// than Traefik's own "404 page not found".
func WaitForRoute(httpPort int, host string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if routeIsLoaded(httpPort, host) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("router did not load the route for %s within %s", host, timeout)
		}
		time.Sleep(routerPollInterval)
	}
}

// routeIsLoaded issues one probe request for the host.
func routeIsLoaded(httpPort int, host string) bool {
	request, err := http.NewRequest(http.MethodGet, "http://"+RouterAddress(httpPort)+"/", nil)
	if err != nil {
		return false
	}
	request.Host = host
	response, err := routeProbeClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		return true
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64))
	if err != nil {
		return false
	}

	return strings.TrimSpace(string(body)) != routerNotFoundBody
}
