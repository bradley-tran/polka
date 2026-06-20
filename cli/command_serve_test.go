package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"polka/backend"
	"polka/service"
)

func TestRunServeUsesCurrentServerConfig(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScriptWithEnv("APP_ENV"))
	docroot := filepath.Join(projectDir, "named-project", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte("APP_ENV=project\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(project .env) error = %v", err)
	}
	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	environment.Server = &testServerConfig{Hostname: "localhost", Port: 8080}
	environment.EnvVars = map[string]string{"APP_ENV": "start"}
	writeTestEnvironmentConfig(t, projectDir, "demo", environment)

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "start", "--watch", filepath.Join("named-project", "public")}); code != 0 {
		t.Fatalf("Run(serve) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "-S localhost:8080") {
		t.Fatalf("Run(serve) output = %q, want configured server address", output)
	}
	if !strings.Contains(output, "start") {
		t.Fatalf("Run(serve) output = %q, want environment variable override in php runtime", output)
	}
	if !strings.Contains(output, "-t "+docroot) {
		t.Fatalf("Run(serve) output = %q, want resolved docroot", output)
	}
	if !strings.Contains(output, phpServeRouterName) {
		t.Fatalf("Run(serve) output = %q, want generated php router argument", output)
	}
}

func TestRunServeUsesConfiguredDocrootWhenArgumentOmitted(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())
	docroot := filepath.Join(projectDir, "configured-project", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				Docroot: filepath.ToSlash(filepath.Join("configured-project", "public")),
				Server:  &testServerConfig{Hostname: "localhost", Port: 8080},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "start", "--watch"}); code != 0 {
		t.Fatalf("Run(serve without arg) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "-S localhost:8080") {
		t.Fatalf("Run(serve without arg) output = %q, want configured server address", output)
	}
	if !strings.Contains(output, "-t "+docroot) {
		t.Fatalf("Run(serve without arg) output = %q, want configured docroot", output)
	}
}

func TestRunServeArgumentOverridesConfiguredDocroot(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())
	configuredDocroot := filepath.Join(projectDir, "configured-project", "public")
	if err := os.MkdirAll(configuredDocroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(configured docroot) error = %v", err)
	}
	overrideDocroot := filepath.Join(projectDir, "override-project", "public")
	if err := os.MkdirAll(overrideDocroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(override docroot) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				Docroot: filepath.ToSlash(filepath.Join("configured-project", "public")),
				Server:  &testServerConfig{Hostname: "localhost", Port: 8080},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "start", "--watch", filepath.Join("override-project", "public")}); code != 0 {
		t.Fatalf("Run(serve with override) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "-t "+overrideDocroot) {
		t.Fatalf("Run(serve with override) output = %q, want CLI docroot override", output)
	}
	if strings.Contains(output, "-t "+configuredDocroot) {
		t.Fatalf("Run(serve with override) output = %q, want configured docroot to be ignored", output)
	}
}

func TestRunServeRequiresDocrootArgumentOrConfig(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP: "8.4",
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")

	if code := Run(stdout, stderr, []string{"--root", root, "start"}); code != 1 {
		t.Fatalf("Run(serve without docroot) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "docroot argument or docroot in the current environment file") {
		t.Fatalf("Run(serve without docroot) stderr = %q, want docroot config guidance", stderr.String())
	}
}

func TestRunServeAllowsServerOverride(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")
	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	environment.Server = &testServerConfig{Hostname: "localhost", Port: 8080}
	writeTestEnvironmentConfig(t, projectDir, "demo", environment)

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "start", "--watch", "--server", "127.0.0.1:9001", filepath.Join("site", "public")}); code != 0 {
		t.Fatalf("Run(serve) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "-S 127.0.0.1:9001") {
		t.Fatalf("Run(serve) output = %q, want overridden server address", output)
	}
	if strings.Contains(output, "-S localhost:8080") {
		t.Fatalf("Run(serve) output = %q, want CLI override to win", output)
	}
	if !strings.Contains(output, "-t "+docroot) {
		t.Fatalf("Run(serve) output = %q, want resolved docroot", output)
	}
}

func TestRunServeUsesNginxWhenConfigured(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:    "8.4",
				Nginx:  "1.30",
				Server: &testServerConfig{Hostname: "localhost", Port: 8080},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")

	oldPHPServe := runPHPRuntimeServeFunc
	oldNginxServe := runNginxServeFunc
	t.Cleanup(func() {
		runPHPRuntimeServeFunc = oldPHPServe
		runNginxServeFunc = oldNginxServe
	})

	phpCalls := 0
	nginxCalls := 0
	gotAddress := ""
	gotDocroot := ""
	runPHPRuntimeServeFunc = func(stdout, stderr io.Writer, store backend.Store, serverAddress string, layout serveAppLayout) (int, error) {
		phpCalls++
		return 0, nil
	}
	runNginxServeFunc = func(stdout, stderr io.Writer, store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout) (int, error) {
		nginxCalls++
		gotAddress = endpoint.Address
		gotDocroot = layout.Docroot
		_, _ = io.WriteString(stdout, "fake-nginx "+endpoint.Address+" -t "+layout.Docroot+"\n")
		return 0, nil
	}

	if code := Run(stdout, stderr, []string{"--root", root, "start", "--watch", filepath.Join("site", "public")}); code != 0 {
		t.Fatalf("Run(serve with nginx) code = %d, stderr = %q", code, stderr.String())
	}
	if phpCalls != 0 {
		t.Fatalf("php serve calls = %d, want nginx branch only", phpCalls)
	}
	if nginxCalls != 1 {
		t.Fatalf("nginx serve calls = %d, want 1", nginxCalls)
	}
	if gotAddress != "localhost:8080" {
		t.Fatalf("nginx serve address = %q, want %q", gotAddress, "localhost:8080")
	}
	if gotDocroot != docroot {
		t.Fatalf("nginx docroot = %q, want %q", gotDocroot, docroot)
	}
	if !strings.Contains(stdout.String(), "fake-nginx localhost:8080 -t "+docroot) {
		t.Fatalf("Run(serve with nginx) output = %q, want nginx serve output", stdout.String())
	}
	if !strings.Contains(stdout.String(), "nginx webserver started at http://localhost:8080") {
		t.Fatalf("Run(serve with nginx) output = %q, want nginx startup info line", stdout.String())
	}
}

func TestRunServeUsesExplicitFrankenPHPWhenNginxIsConfigured(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:        "8.4",
				Nginx:      "1.30",
				FrankenPHP: "1.12",
				Server:     &testServerConfig{Type: "frankenphp", Hostname: "localhost", Port: 8080},
			},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")

	oldPHPServe := runPHPRuntimeServeFunc
	oldNginxServe := runNginxServeFunc
	oldFrankenPHPServe := runFrankenPHPServeFunc
	t.Cleanup(func() {
		runPHPRuntimeServeFunc = oldPHPServe
		runNginxServeFunc = oldNginxServe
		runFrankenPHPServeFunc = oldFrankenPHPServe
	})

	phpCalls := 0
	nginxCalls := 0
	frankenPHPCalls := 0
	runPHPRuntimeServeFunc = func(stdout, stderr io.Writer, store backend.Store, serverAddress string, layout serveAppLayout) (int, error) {
		phpCalls++
		return 0, nil
	}
	runNginxServeFunc = func(stdout, stderr io.Writer, store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout) (int, error) {
		nginxCalls++
		return 0, nil
	}
	runFrankenPHPServeFunc = func(stdout, stderr io.Writer, store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout) (int, error) {
		frankenPHPCalls++
		if endpoint.Address != "localhost:8080" || layout.Docroot != docroot {
			t.Fatalf("FrankenPHP context = %#v, %#v, want configured endpoint and docroot", endpoint, layout)
		}
		return 0, nil
	}

	if code := Run(stdout, stderr, []string{"--root", root, "start", "--watch", filepath.Join("site", "public")}); code != 0 {
		t.Fatalf("Run(serve with FrankenPHP) code = %d, stderr = %q", code, stderr.String())
	}
	if phpCalls != 0 || nginxCalls != 0 || frankenPHPCalls != 1 {
		t.Fatalf("serve calls = php:%d nginx:%d frankenphp:%d, want only FrankenPHP", phpCalls, nginxCalls, frankenPHPCalls)
	}
}

func TestResolveEnvironmentServerTypePreservesLegacySelection(t *testing.T) {
	tests := []struct {
		name        string
		environment backend.Environment
		want        string
		wantErr     string
	}{
		{name: "legacy php", environment: backend.Environment{Name: "demo", PHPVersion: "8.4"}, want: "php"},
		{name: "legacy nginx", environment: backend.Environment{Name: "demo", PHPVersion: "8.4", NginxVersion: "1.30", FrankenPHPVersion: "1.12"}, want: "nginx"},
		{name: "explicit frankenphp", environment: backend.Environment{Name: "demo", FrankenPHPVersion: "1.12", Server: &backend.ServerConfig{Type: "frankenphp"}}, want: "frankenphp"},
		{name: "missing frankenphp", environment: backend.Environment{Name: "demo", Server: &backend.ServerConfig{Type: "frankenphp"}}, wantErr: "does not define a frankenphp version"},
		{name: "invalid type", environment: backend.Environment{Name: "demo", Server: &backend.ServerConfig{Type: "apache"}}, wantErr: "unsupported server type"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveEnvironmentServerType(test.environment)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("resolveEnvironmentServerType() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("resolveEnvironmentServerType() = %q, %v, want %q", got, err, test.want)
			}
		})
	}
}

func TestPrepareFrankenPHPServeRuntimeUsesPolkaTLS(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "run", "frankenphp")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	docroot := filepath.Join(t.TempDir(), "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	configPath, err := prepareFrankenPHPServeRuntime(cacheDir, runtimeDir, serverEndpoint{
		Scheme:  "https",
		Address: "site.localhost:8443",
		HTTPS:   true,
	}, serveAppLayout{Docroot: docroot})
	if err != nil {
		t.Fatalf("prepareFrankenPHPServeRuntime() error = %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(Caddyfile) error = %v", err)
	}
	caddyfile := string(data)
	certPath, keyPath := globalTLSCertificatePaths(cacheDir)
	for _, want := range []string{
		"admin off",
		"auto_https disable_redirects",
		"https://site.localhost:8443",
		"root * " + strconv.Quote(filepath.ToSlash(docroot)),
		"tls " + strconv.Quote(filepath.ToSlash(certPath)) + " " + strconv.Quote(filepath.ToSlash(keyPath)),
		"php_server",
	} {
		if !strings.Contains(caddyfile, want) {
			t.Fatalf("Caddyfile = %q, want %q", caddyfile, want)
		}
	}
}

func TestRenderFrankenPHPCaddyfileSupportsHTTP(t *testing.T) {
	caddyfile := string(renderFrankenPHPCaddyfile(
		serverEndpoint{Scheme: "http", Address: "localhost:8080"},
		serveAppLayout{Docroot: filepath.Join("site", "public")},
		serveTLSConfig{},
	))
	if !strings.Contains(caddyfile, "http://localhost:8080") || !strings.Contains(caddyfile, "php_server") {
		t.Fatalf("Caddyfile = %q, want HTTP FrankenPHP server", caddyfile)
	}
	if strings.Contains(caddyfile, "\ttls ") {
		t.Fatalf("Caddyfile = %q, want no TLS directive", caddyfile)
	}
}

func TestServeStateMatchesResolvedServerType(t *testing.T) {
	state := serveRuntimeState{
		ServerKind:    "frankenphp",
		ServerScheme:  "https",
		ServerAddress: "localhost:8443",
		Docroot:       filepath.Join("site", "public"),
	}
	endpoint := serverEndpoint{Scheme: "https", Address: "localhost:8443", HTTPS: true}
	if !serveStateMatches(state, endpoint, state.Docroot, "frankenphp") {
		t.Fatal("serveStateMatches() = false, want matching FrankenPHP state")
	}
	if serveStateMatches(state, endpoint, state.Docroot, "nginx") {
		t.Fatal("serveStateMatches() = true, want server type mismatch")
	}
}

func TestApplyFrankenPHPRuntimeConfigSetsManagedPHPRC(t *testing.T) {
	installDir := t.TempDir()
	target := filepath.Join(installDir, "frankenphp.exe")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile(frankenphp) error = %v", err)
	}
	phpIniPath := filepath.Join(installDir, "php.ini")
	if err := os.WriteFile(phpIniPath, []byte("extension=openssl\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(php.ini) error = %v", err)
	}

	env, err := applyFrankenPHPRuntimeConfig("windows", []string{"APP_ENV=test", "PHPRC=system.ini"}, target)
	if err != nil {
		t.Fatalf("applyFrankenPHPRuntimeConfig() error = %v", err)
	}
	want := "PHPRC=" + phpIniPath
	if !containsEnvironmentEntryFold(env, want) {
		t.Fatalf("environment = %#v, want %q", env, want)
	}
}

func containsEnvironmentEntryFold(env []string, want string) bool {
	for _, entry := range env {
		if strings.EqualFold(entry, want) {
			return true
		}
	}

	return false
}

func TestPreparePHPRuntimeServeRuntimeCreatesRouter(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "run", "start", "php")
	docroot := filepath.Join(runtimeDir, "docroot")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(docroot, "index.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(index.php) error = %v", err)
	}
	layout, err := resolveServeAppLayout(docroot)
	if err != nil {
		t.Fatalf("resolveServeAppLayout() error = %v", err)
	}

	routerPath, err := preparePHPRuntimeServeRuntime(runtimeDir, layout)
	if err != nil {
		t.Fatalf("preparePHPRuntimeServeRuntime() error = %v", err)
	}
	if routerPath != filepath.Join(runtimeDir, phpServeRouterName) {
		t.Fatalf("router path = %q, want %q", routerPath, filepath.Join(runtimeDir, phpServeRouterName))
	}
	routerData, err := os.ReadFile(routerPath)
	if err != nil {
		t.Fatalf("ReadFile(router) error = %v", err)
	}
	router := string(routerData)
	if !strings.Contains(router, filepath.ToSlash(docroot)) {
		t.Fatalf("php router = %q, want embedded docroot", router)
	}
	if !strings.Contains(router, "'css' => 'text/css'") {
		t.Fatalf("php router = %q, want css mime type mapping", router)
	}
	if !strings.Contains(router, "'js' => 'application/javascript'") {
		t.Fatalf("php router = %q, want js mime type mapping", router)
	}
	if !strings.Contains(router, "readfile($targetReal);") {
		t.Fatalf("php router = %q, want static file passthrough", router)
	}
	if strings.Contains(router, ".ht.router.php") {
		t.Fatalf("php router = %q, want no nested app router delegation", router)
	}
	if !strings.Contains(router, "$frontControllerRelative = \"index.php\";") {
		t.Fatalf("php router = %q, want resolved front controller", router)
	}
	if !strings.Contains(router, "$scriptRelative = $frontControllerRelative;") {
		t.Fatalf("php router = %q, want front controller fallback", router)
	}
	if !strings.Contains(router, "$_SERVER['SCRIPT_FILENAME'] = $scriptReal;") {
		t.Fatalf("php router = %q, want rewritten script filename", router)
	}
	if !strings.Contains(router, "require $scriptReal;") {
		t.Fatalf("php router = %q, want front controller require", router)
	}
}

func TestPrepareNginxServeRuntimeCreatesLogsPath(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "run", "start", "demo")
	docroot := filepath.Join(runtimeDir, "docroot")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(docroot, "index.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(index.php) error = %v", err)
	}
	layout, err := resolveServeAppLayout(docroot)
	if err != nil {
		t.Fatalf("resolveServeAppLayout() error = %v", err)
	}
	configPath, phpLogPath, err := prepareNginxServeRuntime(filepath.Join(runtimeDir, "root"), runtimeDir, backend.Environment{}, serverEndpoint{Scheme: "http", Address: "localhost:8080"}, layout, "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("prepareNginxServeRuntime() error = %v", err)
	}
	if configPath != filepath.Join(runtimeDir, "nginx.conf") {
		t.Fatalf("config path = %q, want %q", configPath, filepath.Join(runtimeDir, "nginx.conf"))
	}
	if phpLogPath != filepath.Join(runtimeDir, "php.log") {
		t.Fatalf("php log path = %q, want %q", phpLogPath, filepath.Join(runtimeDir, "php.log"))
	}
	if info, err := os.Stat(filepath.Join(runtimeDir, "logs")); err != nil {
		t.Fatalf("Stat(logs dir) error = %v", err)
	} else if !info.IsDir() {
		t.Fatalf("logs path mode = %v, want directory", info.Mode())
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	config := string(configData)
	if !strings.Contains(config, "fastcgi_pass 127.0.0.1:9000;") {
		t.Fatalf("nginx config = %q, want fastcgi upstream", config)
	}
	if !strings.Contains(config, "text/css css;") {
		t.Fatalf("nginx config = %q, want css mime type mapping", config)
	}
	if !strings.Contains(config, "application/javascript js mjs;") {
		t.Fatalf("nginx config = %q, want js mime type mapping", config)
	}
	if !strings.Contains(config, "try_files $uri $uri/ /index.php$is_args$args;") {
		t.Fatalf("nginx config = %q, want shared front controller fallback", config)
	}
	if !strings.Contains(config, "fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;") {
		t.Fatalf("nginx config = %q, want script filename fastcgi param", config)
	}
	if strings.Contains(config, "proxy_pass") {
		t.Fatalf("nginx config = %q, want no proxy_pass", config)
	}
}

func TestPrepareNginxServeRuntimeFrameworkFallsBackToGenericConfig(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "run", "start", "demo")
	docroot := filepath.Join(runtimeDir, "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(docroot, "index.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(index.php) error = %v", err)
	}
	layout, err := resolveServeAppLayout(docroot)
	if err != nil {
		t.Fatalf("resolveServeAppLayout() error = %v", err)
	}

	configPath, _, err := prepareNginxServeRuntime(filepath.Join(runtimeDir, "root"), runtimeDir, backend.Environment{Framework: "laravel"}, serverEndpoint{Scheme: "http", Address: "localhost:8080"}, layout, "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("prepareNginxServeRuntime() error = %v", err)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	config := string(configData)
	if !strings.Contains(config, "try_files $uri $uri/ /index.php$is_args$args;") || !strings.Contains(config, "fastcgi_pass 127.0.0.1:9000;") {
		t.Fatalf("nginx config = %q, want generic front-controller config", config)
	}
}

func TestPrepareNginxServeRuntimeCreatesHTTPSConfigForLocalhostHostname(t *testing.T) {
	rootDir := t.TempDir()
	runtimeDir := filepath.Join(rootDir, "run", "start", "demo")
	docroot := filepath.Join(runtimeDir, "docroot")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(docroot, "index.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(index.php) error = %v", err)
	}
	layout, err := resolveServeAppLayout(docroot)
	if err != nil {
		t.Fatalf("resolveServeAppLayout() error = %v", err)
	}

	configPath, phpLogPath, err := prepareNginxServeRuntime(rootDir, runtimeDir, backend.Environment{}, serverEndpoint{Scheme: "https", Address: "site.localhost:8443", HTTPS: true}, layout, "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("prepareNginxServeRuntime() error = %v", err)
	}
	if phpLogPath != filepath.Join(runtimeDir, "php.log") {
		t.Fatalf("php log path = %q, want %q", phpLogPath, filepath.Join(runtimeDir, "php.log"))
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	config := string(configData)
	if !strings.Contains(config, "listen 127.0.0.1:8443 ssl;") {
		t.Fatalf("nginx config = %q, want loopback https listener for .localhost hostname", config)
	}
	if !strings.Contains(config, "server_name site.localhost;") {
		t.Fatalf("nginx config = %q, want configured server_name", config)
	}
	if !strings.Contains(config, "ssl_certificate ") || !strings.Contains(config, "ssl_certificate_key ") {
		t.Fatalf("nginx config = %q, want generated certificate directives", config)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "polka", serveTLSSubdir, serveTLSCertFileName)); err != nil {
		t.Fatalf("Stat(generated cert) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "polka", serveTLSSubdir, serveTLSKeyFileName)); err != nil {
		t.Fatalf("Stat(generated key) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "polka", serveTLSSubdir, serveTLSCACertName)); err != nil {
		t.Fatalf("Stat(generated ca cert) error = %v", err)
	}
}

func TestRunServeRejectsHTTPSWithoutNginx(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				Docroot: filepath.ToSlash(filepath.Join("site", "public")),
				HTTPS:   true,
				Server:  &testServerConfig{Hostname: "site.localhost", Port: 8443},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")

	if code := Run(stdout, stderr, []string{"--root", root, "start"}); code != 1 {
		t.Fatalf("Run(serve https without nginx) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "use nginx or frankenphp") {
		t.Fatalf("Run(serve https with PHP) stderr = %q, want managed HTTPS server guidance", stderr.String())
	}
}

func TestServeProbeAddressUsesLoopbackForLocalhostSubdomains(t *testing.T) {
	if got := serveProbeAddress("site.localhost:8443"); got != "127.0.0.1:8443" {
		t.Fatalf("serveProbeAddress() = %q, want loopback address", got)
	}
	if got := serveProbeAddress("example.test:8443"); got != "example.test:8443" {
		t.Fatalf("serveProbeAddress() = %q, want non-local hostname unchanged", got)
	}
}

func TestResolveServeAppLayoutAllowsMissingFrontController(t *testing.T) {
	docroot := t.TempDir()

	layout, err := resolveServeAppLayout(docroot)
	if err != nil {
		t.Fatalf("resolveServeAppLayout() error = %v", err)
	}
	if layout.Docroot != docroot {
		t.Fatalf("layout docroot = %q, want %q", layout.Docroot, docroot)
	}
	if layout.FrontControllerRelative != "" {
		t.Fatalf("front controller relative = %q, want empty", layout.FrontControllerRelative)
	}
	if layout.FrontControllerWebPath != "" {
		t.Fatalf("front controller web path = %q, want empty", layout.FrontControllerWebPath)
	}
}

func TestResolvePHPCGITargetUsesSiblingBinary(t *testing.T) {
	phpDir := filepath.Join(t.TempDir(), "php")
	phpTarget := filepath.Join(phpDir, "php")
	phpCGITarget := filepath.Join(phpDir, "php-cgi")
	if runtime.GOOS == "windows" {
		phpTarget += ".exe"
		phpCGITarget += ".exe"
	}
	if err := os.MkdirAll(filepath.Dir(phpTarget), 0o755); err != nil {
		t.Fatalf("MkdirAll(php dir) error = %v", err)
	}
	if err := os.WriteFile(phpTarget, []byte("php\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(php target) error = %v", err)
	}
	if err := os.WriteFile(phpCGITarget, []byte("php-cgi\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(php-cgi target) error = %v", err)
	}

	resolved, err := resolvePHPCGITarget(phpTarget)
	if err != nil {
		t.Fatalf("resolvePHPCGITarget() error = %v", err)
	}
	if resolved != phpCGITarget {
		t.Fatalf("resolvePHPCGITarget() = %q, want %q", resolved, phpCGITarget)
	}
}

func TestRunServeStartsConfiguredDatabaseBeforePhp(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())
	writeCachedDatabaseTool(t, cacheDir, "mysql", "8.4", false, true, true)
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")
	runTestMySQLConfig(t, stdout, stderr, root, "demo", "8.4", 3307)

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	oldInitialize := initializeDatabaseServerFunc
	oldStart := startDatabaseServerFunc
	oldPing := pingDatabaseAddressFunc
	oldNow := dbNowFunc
	t.Cleanup(func() {
		initializeDatabaseServerFunc = oldInitialize
		startDatabaseServerFunc = oldStart
		pingDatabaseAddressFunc = oldPing
		dbNowFunc = oldNow
	})

	running := map[string]bool{}
	initialized := 0
	started := 0
	var startedSpec dbServerSpec
	initializeDatabaseServerFunc = func(spec dbServerSpec) error {
		initialized++
		if err := os.MkdirAll(spec.DataDir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(spec.DataDir, "initialized.txt"), []byte("ok\n"), 0o644)
	}
	startDatabaseServerFunc = func(spec dbServerSpec) (dbStartResult, error) {
		started++
		startedSpec = spec
		running[databaseAddress(spec.Port)] = true
		return dbStartResult{PID: 7878}, nil
	}
	pingDatabaseAddressFunc = func(address string) bool {
		return running[address]
	}
	dbNowFunc = func() time.Time {
		return time.Date(2026, time.May, 23, 15, 0, 0, 0, time.UTC)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "start", "--watch", filepath.Join("site", "public")}); code != 0 {
		t.Fatalf("Run(serve with db) code = %d, stderr = %q", code, stderr.String())
	}

	if initialized != 1 || started != 1 {
		t.Fatalf("initialize/start counts = %d/%d, want 1/1", initialized, started)
	}
	if startedSpec.Port != 3307 {
		t.Fatalf("started port = %d, want 3307", startedSpec.Port)
	}
	if startedSpec.AdminTarget == "" || startedSpec.DefaultsFile == "" || startedSpec.BootstrapSQLFile == "" {
		t.Fatalf("started spec = %#v, want credential/bootstrap assets for serve", startedSpec)
	}
	output := stdout.String()
	if !strings.Contains(output, "fake-php") || !strings.Contains(output, "-t "+docroot) {
		t.Fatalf("Run(serve with db) output = %q, want php command output after database start", output)
	}
	state, err := loadDatabaseState(databaseStatePath(root, "demo"))
	if err != nil {
		t.Fatalf("loadDatabaseState() error = %v", err)
	}
	if state.PID != 7878 || state.Port != 3307 {
		t.Fatalf("database state = %#v, want pid 7878 on port 3307", state)
	}
}

func TestRunServeStartsConfiguredMailpitBeforeWebserver(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	t.Setenv("POLKA_CACHE_DIR", filepath.Join(projectDir, "global-cache"))
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				Nginx:   "1.30",
				Docroot: filepath.ToSlash(filepath.Join("site", "public")),
				HTTPS:   true,
				Mailpit: &testMailpitConfig{
					Version:  "1.30",
					SMTPPort: 1125,
					UIPort:   8125,
				},
				Server: &testServerConfig{Hostname: "localhost", Port: 8080},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")
	for _, path := range []string{
		projectInstalledPHPPath(root, "8.4"),
		filepath.Join(root, "envs", "mailpit", "1.30", "mailpit"),
	} {
		if runtime.GOOS == "windows" && strings.HasSuffix(path, "mailpit") {
			path += ".exe"
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("placeholder\n"), 0o755); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	oldStartMailpit := startMailpitServerFunc
	oldStopMailpit := stopMailpitRuntimeFunc
	oldPingMailpit := pingMailpitAddressFunc
	oldMailpitNow := mailpitNowFunc
	oldStartNginx := startBackgroundNginxServe
	oldPingServe := pingServeAddressFunc
	t.Cleanup(func() {
		startMailpitServerFunc = oldStartMailpit
		stopMailpitRuntimeFunc = oldStopMailpit
		pingMailpitAddressFunc = oldPingMailpit
		mailpitNowFunc = oldMailpitNow
		startBackgroundNginxServe = oldStartNginx
		pingServeAddressFunc = oldPingServe
	})

	order := []string{}
	mailpitRunning := map[string]bool{}
	serveRunning := map[string]bool{}
	var startedSpec mailpitServerSpec
	startMailpitServerFunc = func(spec mailpitServerSpec) (mailpitStartResult, error) {
		order = append(order, "mailpit")
		startedSpec = spec
		mailpitRunning[service.MailpitAddress(spec.SMTPPort)] = true
		mailpitRunning[service.MailpitAddress(spec.UIPort)] = true
		return mailpitStartResult{PID: 5656}, nil
	}
	stopMailpitRuntimeFunc = func(state mailpitRuntimeState) error {
		mailpitRunning[service.MailpitAddress(state.SMTPPort)] = false
		mailpitRunning[service.MailpitAddress(state.UIPort)] = false
		return nil
	}
	pingMailpitAddressFunc = func(address string) bool {
		return mailpitRunning[address]
	}
	mailpitNowFunc = func() time.Time {
		return time.Date(2026, time.May, 29, 12, 0, 0, 0, time.UTC)
	}
	startBackgroundNginxServe = func(store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout) (serveRuntimeState, error) {
		order = append(order, "web")
		serveRunning[endpoint.Address] = true
		return serveRuntimeState{PrimaryPID: 4242}, nil
	}
	pingServeAddressFunc = func(address string) bool {
		return serveRunning[address]
	}

	if code := Run(stdout, stderr, []string{"--root", root, "start"}); code != 0 {
		t.Fatalf("Run(serve with mailpit) code = %d, stderr = %q", code, stderr.String())
	}
	if strings.Join(order, ",") != "mailpit,web" {
		t.Fatalf("start order = %v, want mailpit before webserver", order)
	}
	if startedSpec.SMTPPort != 1125 || startedSpec.UIPort != 8125 || !startedSpec.HTTPS {
		t.Fatalf("started mailpit spec = %#v, want configured ports", startedSpec)
	}
	if startedSpec.TLSCertPath == "" || startedSpec.TLSKeyPath == "" {
		t.Fatalf("started mailpit spec = %#v, want generated tls paths", startedSpec)
	}
	state, err := loadMailpitState(mailpitStatePath(root, "demo"))
	if err != nil {
		t.Fatalf("loadMailpitState() error = %v", err)
	}
	if state.PID != 5656 || state.SMTPPort != 1125 || state.UIPort != 8125 || state.UIScheme != "https" {
		t.Fatalf("mailpit state = %#v, want pid 5656, https, and configured ports", state)
	}
}

func TestRunServeStartsConfiguredPHPMyAdminBeforeWebserver(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				Docroot: filepath.ToSlash(filepath.Join("site", "public")),
				PHPMyAdmin: &testPHPMyAdminConfig{
					Version: "5.2",
					Port:    8082,
				},
				Server: &testServerConfig{Hostname: "localhost", Port: 8080},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")
	phpMyAdminIndex := filepath.Join(root, "envs", "phpmyadmin", "5.2", "index.php")
	if err := os.MkdirAll(filepath.Dir(phpMyAdminIndex), 0o755); err != nil {
		t.Fatalf("MkdirAll(phpmyadmin docroot) error = %v", err)
	}
	if err := os.WriteFile(phpMyAdminIndex, []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(phpmyadmin index) error = %v", err)
	}

	oldStartPHPMyAdmin := startPHPMyAdminServeFunc
	oldEnsurePHPMyAdminStorage := ensurePHPMyAdminStorageConfiguredFunc
	oldStartPHP := startBackgroundPHPRuntimeServe
	oldPingServe := pingServeAddressFunc
	oldNow := serveNowFunc
	t.Cleanup(func() {
		startPHPMyAdminServeFunc = oldStartPHPMyAdmin
		ensurePHPMyAdminStorageConfiguredFunc = oldEnsurePHPMyAdminStorage
		startBackgroundPHPRuntimeServe = oldStartPHP
		pingServeAddressFunc = oldPingServe
		serveNowFunc = oldNow
	})

	order := []string{}
	running := map[string]bool{}
	var phpMyAdminEndpoint serverEndpoint
	var phpMyAdminDocroot string
	ensurePHPMyAdminStorageConfiguredFunc = func(ctx service.Context, environment backend.Environment, hooks service.DatabaseRuntimeHooks) error {
		order = append(order, "storage")
		return nil
	}
	startPHPMyAdminServeFunc = func(store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout, runtimeDir string) (serveRuntimeState, error) {
		order = append(order, "phpmyadmin")
		phpMyAdminEndpoint = endpoint
		phpMyAdminDocroot = layout.Docroot
		running[endpoint.Address] = true
		return serveRuntimeState{
			EnvironmentName: environment.Name,
			ServerKind:      map[bool]string{false: "php", true: "nginx"}[endpoint.HTTPS],
			ServerScheme:    endpoint.Scheme,
			ServerAddress:   endpoint.Address,
			Docroot:         layout.Docroot,
			RuntimeDir:      runtimeDir,
			PrimaryPID:      6262,
			StartedAt:       serveNowFunc().UTC(),
		}, nil
	}
	startBackgroundPHPRuntimeServe = func(store backend.Store, environment backend.Environment, serverAddress string, layout serveAppLayout) (serveRuntimeState, error) {
		order = append(order, "web")
		running[serverAddress] = true
		return serveRuntimeState{PrimaryPID: 4242}, nil
	}
	pingServeAddressFunc = func(address string) bool {
		return running[address]
	}
	serveNowFunc = func() time.Time {
		return time.Date(2026, time.June, 2, 12, 0, 0, 0, time.UTC)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "start"}); code != 0 {
		t.Fatalf("Run(serve with phpmyadmin) code = %d, stderr = %q", code, stderr.String())
	}
	if strings.Join(order, ",") != "storage,phpmyadmin,web" {
		t.Fatalf("start order = %v, want phpmyadmin storage before phpmyadmin and webserver", order)
	}
	if phpMyAdminEndpoint.Address != "127.0.0.1:8082" || phpMyAdminEndpoint.Scheme != "http" {
		t.Fatalf("phpmyadmin endpoint = %#v, want http://127.0.0.1:8082", phpMyAdminEndpoint)
	}
	if phpMyAdminDocroot != filepath.Join(root, "envs", "phpmyadmin", "5.2") {
		t.Fatalf("phpmyadmin docroot = %q, want installed phpmyadmin docroot", phpMyAdminDocroot)
	}
	state, err := loadPHPMyAdminState(phpMyAdminStatePath(root, "demo"))
	if err != nil {
		t.Fatalf("loadPHPMyAdminState() error = %v", err)
	}
	if state.Version != "5.2" || state.ServerAddress != "127.0.0.1:8082" || state.PrimaryPID != 6262 {
		t.Fatalf("phpmyadmin state = %#v, want version, endpoint, and pid", state)
	}
	if !strings.Contains(stdout.String(), "Started phpMyAdmin for environment \"demo\" at http://127.0.0.1:8082.\n") {
		t.Fatalf("Run(serve with phpmyadmin) stdout = %q, want phpMyAdmin URL", stdout.String())
	}
}

func TestMailpitServerArgsEnableUITLSAndSMTPStartTLS(t *testing.T) {
	args := mailpitServerArgs(mailpitServerSpec{
		SMTPPort:    1125,
		UIPort:      8125,
		HTTPS:       true,
		TLSCertPath: "cert.pem",
		TLSKeyPath:  "key.pem",
	})

	want := []string{
		"--smtp", "127.0.0.1:1125",
		"--listen", "127.0.0.1:8125",
		"--ui-tls-cert", "cert.pem",
		"--ui-tls-key", "key.pem",
		"--smtp-tls-cert", "cert.pem",
		"--smtp-tls-key", "key.pem",
	}
	if strings.Join(args, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mailpitServerArgs() = %#v, want %#v", args, want)
	}
}

func TestRunServeStartsInBackgroundByDefaultAndStopStopsIt(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				Docroot: filepath.ToSlash(filepath.Join("site", "public")),
				Server:  &testServerConfig{Hostname: "localhost", Port: 8080},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")

	oldStartPHP := startBackgroundPHPRuntimeServe
	oldStopServe := stopServeRuntimeFunc
	oldPing := pingServeAddressFunc
	oldNow := serveNowFunc
	t.Cleanup(func() {
		startBackgroundPHPRuntimeServe = oldStartPHP
		stopServeRuntimeFunc = oldStopServe
		pingServeAddressFunc = oldPing
		serveNowFunc = oldNow
	})

	running := map[string]bool{}
	startCalls := 0
	stopCalls := 0
	stoppedState := serveRuntimeState{}
	startBackgroundPHPRuntimeServe = func(store backend.Store, environment backend.Environment, serverAddress string, layout serveAppLayout) (serveRuntimeState, error) {
		startCalls++
		running[serverAddress] = true
		return serveRuntimeState{
			PrimaryPID: 4242,
			RuntimeDir: serveRuntimeDir(store.RootDir, environment.Name),
			LogPath:    filepath.Join(serveRuntimeDir(store.RootDir, environment.Name), serveLogFileName),
		}, nil
	}
	stopServeRuntimeFunc = func(state serveRuntimeState) error {
		stopCalls++
		stoppedState = state
		running[state.ServerAddress] = false
		return nil
	}
	pingServeAddressFunc = func(address string) bool {
		return running[address]
	}
	serveNowFunc = func() time.Time {
		return time.Date(2026, time.May, 27, 12, 0, 0, 0, time.UTC)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "start"}); code != 0 {
		t.Fatalf("Run(serve background) code = %d, stderr = %q", code, stderr.String())
	}
	if startCalls != 1 {
		t.Fatalf("start calls = %d, want 1", startCalls)
	}
	if !strings.Contains(stdout.String(), "Started php webserver for environment \"demo\" at http://localhost:8080.") {
		t.Fatalf("Run(serve background) stdout = %q, want background start summary", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Run `polka stop` to stop it.") {
		t.Fatalf("Run(serve background) stdout = %q, want stop guidance", stdout.String())
	}

	state, err := loadServeState(serveStatePath(root, "demo"))
	if err != nil {
		t.Fatalf("loadServeState() error = %v", err)
	}
	if state.PrimaryPID != 4242 || state.ServerAddress != "localhost:8080" || state.Docroot != docroot {
		t.Fatalf("serve state = %#v, want background runtime metadata", state)
	}
	if state.ServerKind != "php" {
		t.Fatalf("serve state kind = %q, want php", state.ServerKind)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "stop"}); code != 0 {
		t.Fatalf("Run(stop) code = %d, stderr = %q", code, stderr.String())
	}
	if stopCalls != 1 {
		t.Fatalf("stop calls = %d, want 1", stopCalls)
	}
	if stoppedState.PrimaryPID != 4242 || stoppedState.ServerAddress != "localhost:8080" {
		t.Fatalf("stopped state = %#v, want persisted runtime state", stoppedState)
	}
	if !strings.Contains(stdout.String(), "Stopped php webserver for environment \"demo\".") {
		t.Fatalf("Run(stop) stdout = %q, want stop summary", stdout.String())
	}
	if _, err := os.Stat(serveStatePath(root, "demo")); !os.IsNotExist(err) {
		t.Fatalf("Stat(serve state) error = %v, want not exists", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "stop"}); code != 0 {
		t.Fatalf("Run(stop after stop) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already stopped") {
		t.Fatalf("Run(stop after stop) stdout = %q, want already stopped message", stdout.String())
	}
}
