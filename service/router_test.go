package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testRouterSettings returns default router settings for tests.
func testRouterSettings() RouterSettings {
	return DefaultRouterSettings()
}

// TestRouterArgsBindLoopbackEntrypointsAndWatchDynamicDir verifies the static
// Traefik configuration passed on the command line.
func TestRouterArgsBindLoopbackEntrypointsAndWatchDynamicDir(t *testing.T) {
	dir := t.TempDir()
	args := RouterArgs(RouterSpec{Dir: dir, HTTPPort: 8080, HTTPSPort: 8443})
	want := []string{
		"--entrypoints.web.address=127.0.0.1:8080",
		"--entrypoints.websecure.address=127.0.0.1:8443",
		"--providers.file.directory=" + filepath.Join(dir, "dynamic"),
		"--providers.file.watch=true",
		"--global.checknewversion=false",
		"--global.sendanonymoususage=false",
	}
	if strings.Join(args, "\n") != strings.Join(want, "\n") {
		t.Fatalf("RouterArgs() = %q, want %q", args, want)
	}
}

// TestLoadRouterSettingsWritesDefaultsAndValidates verifies first use writes
// router.yaml, edits are honored, and invalid settings are rejected.
func TestLoadRouterSettingsWritesDefaultsAndValidates(t *testing.T) {
	dir := t.TempDir()
	settings, err := LoadRouterSettings(dir)
	if err != nil {
		t.Fatalf("LoadRouterSettings() error = %v", err)
	}
	if settings != DefaultRouterSettings() {
		t.Fatalf("LoadRouterSettings() = %+v, want defaults", settings)
	}
	if _, err := os.Stat(RouterSettingsPath(dir)); err != nil {
		t.Fatalf("router.yaml not written: %v", err)
	}

	if err := os.WriteFile(RouterSettingsPath(dir), []byte("http-port: 9080\nhttps-port: 9443\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, err = LoadRouterSettings(dir)
	if err != nil {
		t.Fatalf("LoadRouterSettings(edited) error = %v", err)
	}
	if settings.HTTPPort != 9080 || settings.HTTPSPort != 9443 || settings.LeaseRange() != DefaultPortRange() {
		t.Fatalf("LoadRouterSettings(edited) = %+v, want edited ports and default range", settings)
	}

	for _, invalid := range []string{
		"http-port: 9080\nhttps-port: 9080\n",
		"http-port: 0\n",
		"http-port: 20001\n",
		"lease-port-min: 30000\nlease-port-max: 29000\n",
		"unknown: 1\n",
	} {
		if err := os.WriteFile(RouterSettingsPath(dir), []byte(invalid), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRouterSettings(dir); err == nil {
			t.Fatalf("LoadRouterSettings(%q) error = nil, want validation error", invalid)
		}
	}
}

// TestRouteIDIsStableAndDistinguishesProjects verifies route IDs are
// deterministic, sanitized, and differ for same-named projects.
func TestRouteIDIsStableAndDistinguishesProjects(t *testing.T) {
	base := t.TempDir()
	rootA := filepath.Join(base, "a", "My Blog", ".polka")
	rootB := filepath.Join(base, "b", "My Blog", ".polka")

	id := RouteID(rootA, "default")
	if id != RouteID(rootA, "default") {
		t.Fatal("RouteID() is not stable")
	}
	if !strings.HasPrefix(id, "my-blog-default-") {
		t.Fatalf("RouteID() = %q, want sanitized project and environment prefix", id)
	}
	if id == RouteID(rootB, "default") {
		t.Fatal("RouteID() collides for different projects with the same name")
	}
	if id == RouteID(rootA, "staging") {
		t.Fatal("RouteID() collides for different environments")
	}
}

// TestRenderRouteConfigHTTPS verifies HTTPS routes serve every backend on
// the TLS entrypoint with a redirect from plain HTTP and the route cert.
func TestRenderRouteConfigHTTPS(t *testing.T) {
	route := Route{
		ID: "blog-default-1234", Root: "/p/blog/.polka", Environment: "default", Host: "blog.localhost", HTTPS: true,
		Backends: []RouteBackend{{Name: RouteBackendWeb, Port: 21000}, {Name: RouteBackendMailpit, Port: 21001}},
		CertFile: `C:\router\certs\blog.crt`, KeyFile: `C:\router\certs\blog.key`,
	}
	config := string(RenderRouteConfig(route, testRouterSettings()))
	for _, want := range []string{
		"    blog-default-1234:\n      rule: \"Host(`blog.localhost`)\"\n      entryPoints: [websecure]\n      service: blog-default-1234\n      tls: {}\n",
		"    blog-default-1234-redirect:\n      rule: \"Host(`blog.localhost`)\"\n      entryPoints: [web]\n      middlewares: [blog-default-1234-https]\n",
		"    blog-default-1234-mailpit:\n      rule: \"Host(`mailpit.blog.localhost`)\"\n",
		"        scheme: https\n        port: \"8443\"\n",
		"          - url: \"http://127.0.0.1:21000\"\n",
		"          - url: \"http://127.0.0.1:21001\"\n",
		"    - certFile: \"C:/router/certs/blog.crt\"\n      keyFile: \"C:/router/certs/blog.key\"\n",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("RenderRouteConfig() = %s\nmissing %q", config, want)
		}
	}
}

// TestRenderRouteConfigHTTP verifies plain HTTP routes use only the web
// entrypoint, with no redirect or certificate.
func TestRenderRouteConfigHTTP(t *testing.T) {
	route := Route{ID: "shop-default-1", Host: "shop.localhost", Backends: []RouteBackend{{Port: 21005}}}
	config := string(RenderRouteConfig(route, testRouterSettings()))
	if !strings.Contains(config, "entryPoints: [web]") || strings.Contains(config, "websecure") || strings.Contains(config, "redirect") || strings.Contains(config, "tls") {
		t.Fatalf("RenderRouteConfig(http) = %s, want web entrypoint only", config)
	}
}

// TestRegisterRouteDetectsHostConflicts verifies a hostname can belong to
// only one project environment, while re-registering the same route works.
func TestRegisterRouteDetectsHostConflicts(t *testing.T) {
	dir := t.TempDir()
	rootA := filepath.Join(t.TempDir(), "a", ".polka")
	rootB := filepath.Join(t.TempDir(), "b", ".polka")
	now := time.Now()
	first := Route{Root: rootA, Environment: "default", Host: "blog.localhost", Backends: []RouteBackend{{Port: 21000}}}

	if err := RegisterRoute(dir, first, testRouterSettings(), now); err != nil {
		t.Fatalf("RegisterRoute(first) error = %v", err)
	}
	first.Backends[0].Port = 21009
	if err := RegisterRoute(dir, first, testRouterSettings(), now); err != nil {
		t.Fatalf("RegisterRoute(update) error = %v", err)
	}

	err := RegisterRoute(dir, Route{Root: rootB, Environment: "default", Host: "blog.localhost", Backends: []RouteBackend{{Port: 21001}}}, testRouterSettings(), now)
	var conflict *RouteConflictError
	if !errors.As(err, &conflict) || conflict.Host != "blog.localhost" {
		t.Fatalf("RegisterRoute(conflict) error = %v, want RouteConflictError", err)
	}

	// A service subdomain collides with another project's webserver host.
	err = RegisterRoute(dir, Route{Root: rootB, Environment: "default", Host: "localhost", Backends: []RouteBackend{{Name: "blog", Port: 21002}}}, testRouterSettings(), now)
	if !errors.As(err, &conflict) {
		t.Fatalf("RegisterRoute(subdomain conflict) error = %v, want RouteConflictError", err)
	}

	routes, err := ListRoutes(dir)
	if err != nil || len(routes) != 1 || routes[0].Backends[0].Port != 21009 {
		t.Fatalf("ListRoutes() = %+v, %v, want the single updated route", routes, err)
	}
	if _, err := os.Stat(routeConfigPath(dir, routes[0].ID)); err != nil {
		t.Fatalf("route config missing: %v", err)
	}
}

// TestRegisterRouteRejectsInvalidRoutes covers hostname and backend validation.
func TestRegisterRouteRejectsInvalidRoutes(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(t.TempDir(), "p", ".polka")
	for _, route := range []Route{
		{Root: root, Host: "Blog.localhost", Backends: []RouteBackend{{Port: 1}}},
		{Root: root, Host: "blog`.localhost", Backends: []RouteBackend{{Port: 1}}},
		{Root: root, Host: "blog.localhost"},
	} {
		if err := RegisterRoute(dir, route, testRouterSettings(), time.Now()); err == nil {
			t.Fatalf("RegisterRoute(%+v) error = nil, want validation error", route)
		}
	}
}

// TestUnregisterRouteReportsRemaining verifies route removal and the
// remaining count used to decide whether to stop the router.
func TestUnregisterRouteReportsRemaining(t *testing.T) {
	dir := t.TempDir()
	rootA := filepath.Join(t.TempDir(), "a", ".polka")
	rootB := filepath.Join(t.TempDir(), "b", ".polka")
	for _, route := range []Route{
		{Root: rootA, Environment: "default", Host: "a.localhost", Backends: []RouteBackend{{Port: 21000}}},
		{Root: rootB, Environment: "default", Host: "b.localhost", Backends: []RouteBackend{{Port: 21001}}},
	} {
		if err := RegisterRoute(dir, route, testRouterSettings(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	remaining, removed, err := UnregisterRoute(dir, rootA, "default")
	if err != nil || !removed || remaining != 1 {
		t.Fatalf("UnregisterRoute(a) = %d %v %v, want 1 true nil", remaining, removed, err)
	}
	if route, err := LookupRoute(dir, rootA, "default"); err != nil || route != nil {
		t.Fatalf("LookupRoute(a) = %+v %v, want removed", route, err)
	}
	remaining, removed, err = UnregisterRoute(dir, rootA, "default")
	if err != nil || removed || remaining != 1 {
		t.Fatalf("UnregisterRoute(a again) = %d %v %v, want 1 false nil", remaining, removed, err)
	}
	remaining, _, err = UnregisterRoute(dir, rootB, "default")
	if err != nil || remaining != 0 {
		t.Fatalf("UnregisterRoute(b) remaining = %d %v, want 0", remaining, err)
	}
}

// TestEnsureRouterStartedLifecycle covers starting, reusing a live router,
// reporting configuration drift, and stopping.
func TestEnsureRouterStartedLifecycle(t *testing.T) {
	dir := t.TempDir()
	live := map[int]bool{}
	started := 0
	hooks := RouterHooks{
		StartServer: func(spec RouterSpec) (RouterStartResult, error) {
			started++
			live[4321] = true
			return RouterStartResult{PID: 4321}, nil
		},
		StopServer:  func(state RouterState) error { delete(live, state.PID); return nil },
		PIDIsLive:   func(pid int) bool { return live[pid] },
		PingAddress: func(string) bool { return len(live) > 0 },
		Now:         func() time.Time { return time.Unix(100, 0) },
	}
	settings := testRouterSettings()

	summary, err := EnsureRouterStarted(dir, "traefik", "3.3", settings, hooks)
	if err != nil || summary.AlreadyRunning || summary.State.PID != 4321 || summary.State.HTTPPort != 8080 {
		t.Fatalf("EnsureRouterStarted(first) = %+v %v, want started", summary, err)
	}
	summary, err = EnsureRouterStarted(dir, "traefik", "3.3", settings, hooks)
	if err != nil || !summary.AlreadyRunning || summary.RestartRequired || started != 1 {
		t.Fatalf("EnsureRouterStarted(second) = %+v %v started=%d, want reuse", summary, err, started)
	}
	changed := settings
	changed.HTTPPort = 9080
	summary, err = EnsureRouterStarted(dir, "traefik", "3.3", changed, hooks)
	if err != nil || !summary.AlreadyRunning || !summary.RestartRequired {
		t.Fatalf("EnsureRouterStarted(changed ports) = %+v %v, want restart required", summary, err)
	}

	state, alreadyStopped, err := StopRouter(dir, hooks)
	if err != nil || alreadyStopped || state.PID != 4321 {
		t.Fatalf("StopRouter() = %+v %v %v, want stopped", state, alreadyStopped, err)
	}
	if _, err := os.Stat(RouterStatePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("router state remains after stop: %v", err)
	}
	if _, alreadyStopped, err := StopRouter(dir, hooks); err != nil || !alreadyStopped {
		t.Fatalf("StopRouter(again) = %v %v, want already stopped", alreadyStopped, err)
	}
}

// TestLoadLiveRouterStateRequiresLivePID verifies a router port held by an
// unrelated process does not count as the Polka router.
func TestLoadLiveRouterStateRequiresLivePID(t *testing.T) {
	dir := t.TempDir()
	if err := writeRouterState(RouterStatePath(dir), RouterState{PID: 99, HTTPPort: 8080, HTTPSPort: 8443}); err != nil {
		t.Fatal(err)
	}
	state, err := LoadLiveRouterState(dir, RouterHooks{
		PIDIsLive:   func(int) bool { return false },
		PingAddress: func(string) bool { return true },
	})
	if err != nil || state != nil {
		t.Fatalf("LoadLiveRouterState() = %+v %v, want nil for a dead PID", state, err)
	}
	if _, err := os.Stat(RouterStatePath(dir)); !os.IsNotExist(err) {
		t.Fatal("stale router state was not removed")
	}
}

// TestWaitForRouteDistinguishesTraefikNotFound verifies the probe treats
// Traefik's own 404 as "not loaded" but an application 404 as loaded.
func TestWaitForRouteDistinguishesTraefikNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "loaded.localhost":
			w.WriteHeader(http.StatusOK)
		case "app404.localhost":
			http.Error(w, "missing page", http.StatusNotFound)
		default:
			http.Error(w, routerNotFoundBody, http.StatusNotFound)
		}
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}

	if err := WaitForRoute(port, "loaded.localhost", time.Second); err != nil {
		t.Fatalf("WaitForRoute(loaded) error = %v", err)
	}
	if err := WaitForRoute(port, "app404.localhost", time.Second); err != nil {
		t.Fatalf("WaitForRoute(app 404) error = %v", err)
	}
	if err := WaitForRoute(port, "unknown.localhost", 300*time.Millisecond); err == nil {
		t.Fatal("WaitForRoute(unknown) error = nil, want timeout")
	}
}

// TestPublicURL covers router URL rendering for both schemes.
func TestPublicURL(t *testing.T) {
	settings := testRouterSettings()
	if got := PublicURL("blog.localhost", true, settings); got != "https://blog.localhost:8443" {
		t.Fatalf("PublicURL(https) = %q", got)
	}
	if got := PublicURL("blog.localhost", false, settings); got != "http://blog.localhost:8080" {
		t.Fatalf("PublicURL(http) = %q", got)
	}
}
