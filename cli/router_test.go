package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"polka/backend"
	"polka/service"
)

// fakeGlobalRouter replaces the router process seams for one test. The
// router "runs" from StartServer until StopServer and never touches the
// network; Traefik provisioning and the route probe are stubbed too. It
// returns a pointer reporting whether the fake router is running.
func fakeGlobalRouter(t *testing.T) *bool {
	t.Helper()
	running := false
	oldStart, oldStop := startRouterServerFunc, stopRouterServerFunc
	oldPing, oldPID := pingRouterAddressFunc, routerPIDIsLiveFunc
	oldTraefik, oldWait := ensureRouterTraefikFunc, waitForRouteFunc
	t.Cleanup(func() {
		startRouterServerFunc, stopRouterServerFunc = oldStart, oldStop
		pingRouterAddressFunc, routerPIDIsLiveFunc = oldPing, oldPID
		ensureRouterTraefikFunc, waitForRouteFunc = oldTraefik, oldWait
	})
	startRouterServerFunc = func(service.RouterSpec) (service.RouterStartResult, error) {
		running = true
		return service.RouterStartResult{PID: 5150}, nil
	}
	stopRouterServerFunc = func(service.RouterState) error {
		running = false
		return nil
	}
	pingRouterAddressFunc = func(string) bool { return running }
	routerPIDIsLiveFunc = func(int) bool { return running }
	ensureRouterTraefikFunc = func(_ io.Writer, _ backend.Store) (string, string, error) {
		return "traefik", "3.3", nil
	}
	waitForRouteFunc = func(int, string, time.Duration) error { return nil }

	return &running
}

// fakeBackgroundPHPServe records the address the PHP webserver binds and
// treats it as listening until stopped.
func fakeBackgroundPHPServe(t *testing.T) *string {
	t.Helper()
	bound := ""
	listening := map[string]bool{}
	oldStart, oldPing, oldStop := startBackgroundPHPRuntimeServe, pingServeAddressFunc, stopServeRuntimeFunc
	t.Cleanup(func() {
		startBackgroundPHPRuntimeServe, pingServeAddressFunc, stopServeRuntimeFunc = oldStart, oldPing, oldStop
	})
	startBackgroundPHPRuntimeServe = func(_ backend.Store, _ backend.Environment, serverAddress string, _ serveAppLayout) (serveRuntimeState, error) {
		bound = serverAddress
		listening[serverAddress] = true
		return serveRuntimeState{PrimaryPID: 4242}, nil
	}
	pingServeAddressFunc = func(address string) bool { return listening[address] }
	stopServeRuntimeFunc = func(state serveRuntimeState) error {
		listening[state.ServerAddress] = false
		return nil
	}

	return &bound
}

// writeRoutedTestProject writes a PHP project with HTTPS and an explicit
// hostname, selecting the demo environment.
func writeRoutedTestProject(t *testing.T) string {
	t.Helper()
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: "0.2",
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {PHP: "8.4", Docroot: "public", HTTPS: true, Server: &testServerConfig{Hostname: "blog.localhost"}},
		},
	})
	writeTestActiveEnvironment(t, filepath.Join(projectDir, ".polka"), "demo")

	return projectDir
}

// TestRunServeRoutesThroughGlobalRouter verifies the full routed lifecycle:
// serve binds the webserver to a leased loopback port behind the router,
// publishes an HTTPS route with its own certificate, reports router URLs in
// status, and stop removes the route and stops the now unused router.
func TestRunServeRoutesThroughGlobalRouter(t *testing.T) {
	t.Setenv("POLKA_CACHE_DIR", t.TempDir())
	routerDir := useTestRouterDir(t)
	running := fakeGlobalRouter(t)
	bound := fakeBackgroundPHPServe(t)
	projectDir := writeRoutedTestProject(t)
	root := filepath.Join(projectDir, ".polka")
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}

	// HTTPS with PHP's built-in server works behind the router.
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "serve"}); code != 0 {
		t.Fatalf("Run(serve) code = %d, stderr = %q", code, stderr.String())
	}
	webPort, ok, err := service.NewPortRegistry(routerDir, service.DefaultPortRange()).Lookup(root, "demo", service.PortKeyWeb)
	if err != nil || !ok {
		t.Fatalf("web lease = %d %v %v, want leased port", webPort, ok, err)
	}
	if want := "127.0.0.1:" + strconv.Itoa(webPort); *bound != want {
		t.Fatalf("webserver bound %q, want leased loopback %q", *bound, want)
	}
	if !*running {
		t.Fatal("global router is not running after serve")
	}
	if !strings.Contains(stdout.String(), "at https://demo.blog.localhost:8443.") {
		t.Fatalf("Run(serve) stdout = %q, want routed public URL", stdout.String())
	}

	route, err := service.LookupRoute(routerDir, root, "demo")
	if err != nil || route == nil {
		t.Fatalf("LookupRoute() = %+v %v, want registered route", route, err)
	}
	if route.Host != "demo.blog.localhost" || !route.HTTPS || route.Backends[0].Port != webPort {
		t.Fatalf("route = %+v, want https demo.blog.localhost to the web lease", route)
	}
	certificate, err := readCertificateFile(route.CertFile)
	if err != nil {
		t.Fatalf("read route certificate: %v", err)
	}
	if certificate.VerifyHostname("demo.blog.localhost") != nil || certificate.VerifyHostname("mailpit.demo.blog.localhost") != nil {
		t.Fatalf("route certificate names = %v, want host and service subdomains", certificate.DNSNames)
	}

	stdout.Reset()
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "status"}); code != 0 {
		t.Fatalf("Run(status) code = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{
		"server https://demo.blog.localhost:8443 (via router)\n",
		"webserver running https://demo.blog.localhost:8443\n",
		"router running http=8080 https=8443 routes=1\n",
		"route webserver https://demo.blog.localhost:8443\n",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("Run(status) stdout = %q, want %q", stdout.String(), want)
		}
	}

	// Serving again while running re-publishes the route without restarting.
	stdout.Reset()
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "serve"}); code != 0 {
		t.Fatalf("Run(serve again) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "is already running at https://demo.blog.localhost:8443.") {
		t.Fatalf("Run(serve again) stdout = %q, want already running at the public URL", stdout.String())
	}

	stdout.Reset()
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "stop"}); code != 0 {
		t.Fatalf("Run(stop) code = %d, stderr = %q", code, stderr.String())
	}
	if route, err := service.LookupRoute(routerDir, root, "demo"); err != nil || route != nil {
		t.Fatalf("route after stop = %+v %v, want removed", route, err)
	}
	if *running {
		t.Fatal("global router still running after its last route was removed")
	}
	if !strings.Contains(stdout.String(), "Stopped the global router") {
		t.Fatalf("Run(stop) stdout = %q, want router stop message", stdout.String())
	}
}

// TestRunServeServerFlagBypassesRouter verifies --server binds directly and
// never starts the router or publishes a route.
func TestRunServeServerFlagBypassesRouter(t *testing.T) {
	routerDir := useTestRouterDir(t)
	running := fakeGlobalRouter(t)
	bound := fakeBackgroundPHPServe(t)
	projectDir := writeRoutedTestProject(t)
	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: "0.2",
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {PHP: "8.4", Docroot: "public"},
		},
	})
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--dir", projectDir, "serve", "--server", "localhost:8123"}); code != 0 {
		t.Fatalf("Run(serve --server) code = %d, stderr = %q", code, stderr.String())
	}
	if *bound != "localhost:8123" || *running {
		t.Fatalf("bound = %q router running = %v, want direct bind without router", *bound, *running)
	}
	if routes, err := service.ListRoutes(routerDir); err != nil || len(routes) != 0 {
		t.Fatalf("ListRoutes() = %+v %v, want none", routes, err)
	}
}

// TestRunServeRouterOptOutBindsDirectly verifies server.router: false keeps
// the classic direct bind.
func TestRunServeRouterOptOutBindsDirectly(t *testing.T) {
	useTestRouterDir(t)
	running := fakeGlobalRouter(t)
	bound := fakeBackgroundPHPServe(t)
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), []byte("version: \"0.2\"\nroot: .polka\ntools:\n  php: \"8.4\"\ndocroot: public\nserver:\n  port: 8124\n  router: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--dir", projectDir, "serve"}); code != 0 {
		t.Fatalf("Run(serve) code = %d, stderr = %q", code, stderr.String())
	}
	if *bound != "localhost:8124" || *running {
		t.Fatalf("bound = %q router running = %v, want direct bind without router", *bound, *running)
	}
}

// TestRunServeFallsBackWhenRouterUnavailable verifies serving continues on a
// direct bind with a warning when the router cannot be provisioned.
func TestRunServeFallsBackWhenRouterUnavailable(t *testing.T) {
	useTestRouterDir(t)
	fakeGlobalRouter(t)
	ensureRouterTraefikFunc = func(io.Writer, backend.Store) (string, string, error) {
		return "", "", errors.New("offline")
	}
	bound := fakeBackgroundPHPServe(t)
	projectDir := writeRoutedTestProject(t)
	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: "0.2",
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {PHP: "8.4", Docroot: "public", Server: &testServerConfig{Hostname: "localhost", Port: 8125}},
		},
	})
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--dir", projectDir, "serve"}); code != 0 {
		t.Fatalf("Run(serve) code = %d, stderr = %q", code, stderr.String())
	}
	if *bound != "localhost:8125" {
		t.Fatalf("bound = %q, want configured direct address", *bound)
	}
	if !strings.Contains(stderr.String(), "global router is unavailable") {
		t.Fatalf("stderr = %q, want router fallback warning", stderr.String())
	}
}

// TestRunServeReportsRouteHostConflict verifies two projects cannot claim the
// same hostname.
func TestRunServeReportsRouteHostConflict(t *testing.T) {
	t.Setenv("POLKA_CACHE_DIR", t.TempDir())
	useTestRouterDir(t)
	fakeGlobalRouter(t)
	fakeBackgroundPHPServe(t)
	first := writeRoutedTestProject(t)
	second := writeRoutedTestProject(t)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--dir", first, "serve"}); code != 0 {
		t.Fatalf("Run(serve first) code = %d, stderr = %q", code, stderr.String())
	}
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--dir", second, "serve"}); code == 0 {
		t.Fatal("Run(serve second) code = 0, want hostname conflict")
	}
	if !strings.Contains(stderr.String(), "already routed") || !strings.Contains(stderr.String(), "server.hostname") {
		t.Fatalf("stderr = %q, want conflict naming server.hostname", stderr.String())
	}
}

// TestRouteHost covers hostname derivation for routed environments.
func TestRouteHost(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "My Shop")
	store := backend.NewProjectStore(projectDir)
	for _, test := range []struct {
		environment backend.Environment
		want        string
	}{
		{environment: backend.Environment{Name: "default"}, want: "my-shop.localhost"},
		{environment: backend.Environment{Name: "staging"}, want: "staging.my-shop.localhost"},
		{environment: backend.Environment{Name: "default", Server: &backend.ServerConfig{Hostname: "Blog.localhost"}}, want: "blog.localhost"},
	} {
		got, err := routeHost(store, test.environment)
		if err != nil || got != test.want {
			t.Fatalf("routeHost(%+v) = %q %v, want %q", test.environment, got, err, test.want)
		}
	}
	if _, err := routeHost(store, backend.Environment{Name: "default", Server: &backend.ServerConfig{Hostname: "127.0.0.1:80"}}); err == nil {
		t.Fatal("routeHost(invalid hostname) error = nil, want validation error")
	}
}

// TestProxiedWebserverConfigsForwardHTTPS verifies every webserver tells PHP
// the request is HTTPS when the router terminated TLS in front of it.
func TestProxiedWebserverConfigsForwardHTTPS(t *testing.T) {
	layout := serveAppLayout{Docroot: "/site/public", FrontControllerRelative: "index.php", FrontControllerWebPath: "/index.php", FrontControllerIndex: "index.php"}
	forwarded := serveTLSConfig{ForwardedHTTPS: true}

	nginx := string(renderNginxServeConfig("127.0.0.1", 21000, layout, "127.0.0.1:21001", forwarded))
	if !strings.Contains(nginx, "listen 127.0.0.1:21000;") || !strings.Contains(nginx, "fastcgi_param HTTPS on;") || !strings.Contains(nginx, "fastcgi_param REQUEST_SCHEME https;") {
		t.Fatalf("nginx config = %s, want plain loopback listen with forwarded HTTPS", nginx)
	}
	if direct := string(renderNginxServeConfig("127.0.0.1", 21000, layout, "127.0.0.1:21001", serveTLSConfig{})); strings.Contains(direct, "HTTPS on") {
		t.Fatal("direct nginx config forces HTTPS on")
	}

	apache := string(renderApacheServeConfig("/apache", "/run", "127.0.0.1", 21000, layout, "127.0.0.1:21001", forwarded))
	if !strings.Contains(apache, "mod_env.so") || !strings.Contains(apache, "SetEnv HTTPS on") || strings.Contains(apache, "SSLEngine") {
		t.Fatalf("apache config = %s, want forwarded HTTPS without local TLS", apache)
	}

	endpoint := serverEndpoint{Scheme: "http", Address: "127.0.0.1:21000", Public: &service.PublicOrigin{Scheme: "https", Host: "blog.localhost", Port: 8443}}
	caddy := string(renderFrankenPHPCaddyfile(endpoint, layout, forwarded))
	for _, want := range []string{"auto_https off", "http://:21000 {", "bind 127.0.0.1", "env HTTPS on"} {
		if !strings.Contains(caddy, want) {
			t.Fatalf("Caddyfile = %s, want %q", caddy, want)
		}
	}

	router := string(renderPHPRuntimeRouter(layout))
	if !strings.Contains(router, "HTTP_X_FORWARDED_PROTO") || !strings.Contains(router, "$remoteAddress === '127.0.0.1'") {
		t.Fatalf("php router = %s, want loopback-only forwarded HTTPS handling", router)
	}
}

// TestEnsureRouteCertificateReusesAndReissues verifies route certificates
// cover the host and its subdomains, are reused, and are re-issued for a new
// host.
func TestEnsureRouteCertificateReusesAndReissues(t *testing.T) {
	cacheDir := t.TempDir()
	routerDir := t.TempDir()

	certPath, keyPath, err := ensureRouteCertificate(cacheDir, routerDir, "blog-default-1", "blog.localhost")
	if err != nil {
		t.Fatalf("ensureRouteCertificate() error = %v", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("route key missing: %v", err)
	}
	first, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureRouteCertificate(cacheDir, routerDir, "blog-default-1", "blog.localhost"); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, again) {
		t.Fatal("ensureRouteCertificate() re-issued a current certificate")
	}

	if _, _, err := ensureRouteCertificate(cacheDir, routerDir, "blog-default-1", "shop.localhost"); err != nil {
		t.Fatal(err)
	}
	certificate, err := readCertificateFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if certificate.VerifyHostname("pma.shop.localhost") != nil {
		t.Fatalf("re-issued certificate names = %v, want shop.localhost subdomains", certificate.DNSNames)
	}
}

// TestRunRouterStatusAndStop verifies `polka router` lists routes and
// leases, and that stop keeps routes for the next serve.
func TestRunRouterStatusAndStop(t *testing.T) {
	t.Setenv("POLKA_CACHE_DIR", t.TempDir())
	routerDir := useTestRouterDir(t)
	running := fakeGlobalRouter(t)
	fakeBackgroundPHPServe(t)
	projectDir := writeRoutedTestProject(t)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "serve"}); code != 0 {
		t.Fatalf("Run(serve) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "router"}); code != 0 {
		t.Fatalf("Run(router) code = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{"router running http=8080 https=8443 pid 5150", "route https://demo.blog.localhost:8443 -> ", "lease demo web 127.0.0.1:"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("Run(router) stdout = %q, want %q", stdout.String(), want)
		}
	}

	stdout.Reset()
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "router", "stop"}); code != 0 {
		t.Fatalf("Run(router stop) code = %d, stderr = %q", code, stderr.String())
	}
	if *running {
		t.Fatal("router still running after `polka router stop`")
	}
	if routes, err := service.ListRoutes(routerDir); err != nil || len(routes) != 1 {
		t.Fatalf("ListRoutes() after router stop = %d %v, want the route kept", len(routes), err)
	}

	if code := Run(stdout, stderr, []string{"--dir", projectDir, "router", "bogus"}); code != 2 {
		t.Fatalf("Run(router bogus) code = %d, want 2", code)
	}
}
