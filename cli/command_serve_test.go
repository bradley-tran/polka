package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"polka/backend"
)

func TestRunServeUsesCurrentServerConfig(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScriptWithEnv("APP_ENV"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	docroot := filepath.Join(projectDir, "named-project", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}
	configPath := filepath.Join(projectDir, "polka.yaml")
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte("APP_ENV=project\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(project .env) error = %v", err)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	environment := config.Environments["demo"]
	environment.Server = &testServerConfig{Hostname: "localhost", Port: 8080}
	environment.EnvVars = map[string]string{"APP_ENV": "serve"}
	config.Environments["demo"] = environment
	updatedConfig, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	updatedConfig = append(updatedConfig, '\n')
	if err := os.WriteFile(configPath, updatedConfig, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "serve", "--watch", filepath.Join("named-project", "public")}); code != 0 {
		t.Fatalf("Run(serve) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "-S localhost:8080") {
		t.Fatalf("Run(serve) output = %q, want configured server address", output)
	}
	if !strings.Contains(output, "serve") {
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
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	docroot := filepath.Join(projectDir, "configured-project", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Current: "demo",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				Docroot: filepath.ToSlash(filepath.Join("configured-project", "public")),
				Server:  &testServerConfig{Hostname: "localhost", Port: 8080},
			},
		},
	}
	configData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	configData = append(configData, '\n')
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "serve", "--watch"}); code != 0 {
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
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
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
		Current: "demo",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				Docroot: filepath.ToSlash(filepath.Join("configured-project", "public")),
				Server:  &testServerConfig{Hostname: "localhost", Port: 8080},
			},
		},
	}
	configData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	configData = append(configData, '\n')
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "serve", "--watch", filepath.Join("override-project", "public")}); code != 0 {
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
		Current: "demo",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP: "8.4",
			},
		},
	}
	configData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	configData = append(configData, '\n')
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "serve"}); code != 1 {
		t.Fatalf("Run(serve without docroot) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "docroot argument or environments.<name>.docroot") {
		t.Fatalf("Run(serve without docroot) stderr = %q, want docroot config guidance", stderr.String())
	}
}

func TestRunServeAllowsServerOverride(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}
	configPath := filepath.Join(projectDir, "polka.yaml")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	environment := config.Environments["demo"]
	environment.Server = &testServerConfig{Hostname: "localhost", Port: 8080}
	config.Environments["demo"] = environment
	updatedConfig, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	updatedConfig = append(updatedConfig, '\n')
	if err := os.WriteFile(configPath, updatedConfig, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "serve", "--watch", "--server", "127.0.0.1:9001", filepath.Join("site", "public")}); code != 0 {
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
		Current: "demo",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:    "8.4",
				Nginx:  "1.30",
				Server: &testServerConfig{Hostname: "localhost", Port: 8080},
			},
		},
	}
	configData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	configData = append(configData, '\n')
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

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
	runNginxServeFunc = func(stdout, stderr io.Writer, store backend.Store, environment backend.Environment, endpoint serveEndpoint, layout serveAppLayout) (int, error) {
		nginxCalls++
		gotAddress = endpoint.Address
		gotDocroot = layout.Docroot
		_, _ = io.WriteString(stdout, "fake-nginx "+endpoint.Address+" -t "+layout.Docroot+"\n")
		return 0, nil
	}

	if code := Run(stdout, stderr, []string{"--root", root, "serve", "--watch", filepath.Join("site", "public")}); code != 0 {
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

func TestPreparePHPRuntimeServeRuntimeCreatesRouter(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "run", "serve", "php")
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
	runtimeDir := filepath.Join(t.TempDir(), "run", "serve", "demo")
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
	configPath, phpLogPath, err := prepareNginxServeRuntime(runtimeDir, serveEndpoint{Scheme: "http", Address: "localhost:8080"}, layout, "127.0.0.1:9000")
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

func TestPrepareNginxServeRuntimeCreatesHTTPSConfigForLocalhostHostname(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "run", "serve", "demo")
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

	configPath, phpLogPath, err := prepareNginxServeRuntime(runtimeDir, serveEndpoint{Scheme: "https", Address: "site.localhost:8443", HTTPS: true}, layout, "127.0.0.1:9000")
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
	if _, err := os.Stat(filepath.Join(runtimeDir, serveTLSSubdir, "site.localhost.crt")); err != nil {
		t.Fatalf("Stat(generated cert) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, serveTLSSubdir, "site.localhost.key")); err != nil {
		t.Fatalf("Stat(generated key) error = %v", err)
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
		Current: "demo",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				Docroot: filepath.ToSlash(filepath.Join("site", "public")),
				Server:  &testServerConfig{Hostname: "site.localhost", Port: 8443, HTTPS: true},
			},
		},
	}
	configData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	configData = append(configData, '\n')
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "serve"}); code != 1 {
		t.Fatalf("Run(serve https without nginx) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "server.https requires nginx") {
		t.Fatalf("Run(serve https without nginx) stderr = %q, want nginx guidance", stderr.String())
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
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	fakeMySQL := cachedDatabasePath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQL), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysql) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQL, fakeDatabaseScript("mysql"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysql) error = %v", err)
	}
	fakeMySQLServer := cachedDatabaseServerPath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQLServer), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysqld) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQLServer, fakeDatabaseScript("mysqld"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysqld) error = %v", err)
	}
	fakeMySQLAdmin := cachedDatabaseAdminPath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQLAdmin), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysqladmin) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQLAdmin, fakeDatabaseScript("mysqladmin"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysqladmin) error = %v", err)
	}
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4", "--db-engine", "mysql", "--db-version", "8.4", "--db-port", "3307"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
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
	if code := Run(stdout, stderr, []string{"--root", root, "serve", "--watch", filepath.Join("site", "public")}); code != 0 {
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
		Current: "demo",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				Docroot: filepath.ToSlash(filepath.Join("site", "public")),
				Server:  &testServerConfig{Hostname: "localhost", Port: 8080},
			},
		},
	}
	configData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	configData = append(configData, '\n')
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

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
	if code := Run(stdout, stderr, []string{"--root", root, "serve"}); code != 0 {
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
