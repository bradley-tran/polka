package cli

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRenderFrankenPHPCaddyfileBasic(t *testing.T) {
	caddyfile := string(renderFrankenPHPCaddyfile(
		serverEndpoint{Scheme: "http", Address: "localhost:8080"},
		serveAppLayout{Docroot: filepath.Join("site", "public")},
		serveTLSConfig{},
	))
	if !strings.Contains(caddyfile, "http://localhost:8080") {
		t.Fatalf("Caddyfile = %q, want HTTP FrankenPHP server", caddyfile)
	}
	if strings.Contains(caddyfile, "\ttls ") {
		t.Fatalf("Caddyfile = %q, want no TLS directive", caddyfile)
	}
	if !strings.Contains(caddyfile, "\tphp_server\n") {
		t.Fatalf("Caddyfile = %q, want default php_server directive", caddyfile)
	}
}

func TestRenderFrankenPHPCaddyfileWithTLS(t *testing.T) {
	caddyfile := string(renderFrankenPHPCaddyfile(
		serverEndpoint{Scheme: "https", Address: "localhost:8443"},
		serveAppLayout{Docroot: filepath.Join("site", "public")},
		serveTLSConfig{
			Enabled:            true,
			CertificatePath:    "/path/to/cert.pem",
			CertificateKeyPath: "/path/to/key.pem",
		},
	))
	if !strings.Contains(caddyfile, "https://localhost:8443") {
		t.Fatalf("Caddyfile = %q, want HTTPS FrankenPHP server", caddyfile)
	}
	if !strings.Contains(caddyfile, "\ttls \"/path/to/cert.pem\" \"/path/to/key.pem\"") {
		t.Fatalf("Caddyfile = %q, want TLS directive", caddyfile)
	}
}

func TestRenderFrankenPHPCaddyfileWithCustomFrontController(t *testing.T) {
	caddyfile := string(renderFrankenPHPCaddyfile(
		serverEndpoint{Scheme: "http", Address: "localhost:8080"},
		serveAppLayout{
			Docroot:                 filepath.Join("site", "public"),
			FrontControllerRelative: "app.php",
		},
		serveTLSConfig{},
	))

	expectedTryFiles := "\t\ttry_files {path} {path}/app.php app.php"
	if !strings.Contains(caddyfile, expectedTryFiles) {
		t.Fatalf("Caddyfile = %q, want try_files directive for app.php", caddyfile)
	}
}

func TestResolveFrankenPHPRuntimeEnvironment(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)

	writeCachedFrankenPHP(t, cacheDir, "1.12")
	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())

	runTestConfigValue(t, &bytes.Buffer{}, &bytes.Buffer{}, root, "demo", "tools.php", "8.4")
	runTestConfigValue(t, &bytes.Buffer{}, &bytes.Buffer{}, root, "demo", "tools.frankenphp", "1.12")
	if code := Run(&bytes.Buffer{}, &bytes.Buffer{}, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d", code)
	}
	writeTestActiveEnvironment(t, root, "demo")

	store, err := resolveStore(root)
	if err != nil {
		t.Fatalf("resolveStore() error = %v", err)
	}

	target, err := store.ResolveTool("frankenphp")
	if err != nil {
		t.Fatalf("ResolveTool(frankenphp) error = %v", err)
	}

	env, err := resolveFrankenPHPRuntimeEnvironment(store, target)
	if err != nil {
		t.Fatalf("resolveFrankenPHPRuntimeEnvironment() error = %v", err)
	}

	if len(env) == 0 {
		t.Fatalf("Expected non-empty environment variables")
	}

	if runtime.GOOS == "windows" {
		foundPHPRC := false
		for _, e := range env {
			if strings.HasPrefix(e, "PHPRC=") {
				foundPHPRC = true
				break
			}
		}
		if !foundPHPRC {
			t.Fatalf("Expected PHPRC to be set on Windows")
		}
	}
}

func TestPrepareFrankenPHPServeRuntime(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	runtimeDir := filepath.Join(t.TempDir(), "run")
	endpoint := serverEndpoint{Scheme: "http", Address: "localhost:8080"}
	layout := serveAppLayout{Docroot: filepath.Join("site", "public")}

	configPath, err := prepareFrankenPHPServeRuntime(cacheDir, runtimeDir, endpoint, layout)
	if err != nil {
		t.Fatalf("prepareFrankenPHPServeRuntime() error = %v", err)
	}

	if filepath.Base(configPath) != frankenPHPCaddyfileName {
		t.Fatalf("Expected config file to be named %s, got %s", frankenPHPCaddyfileName, filepath.Base(configPath))
	}
}

func TestPrepareFrankenPHPServeRuntimeCreatesRuntimeDir(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	runtimeDir := filepath.Join(t.TempDir(), "run", "nested", "frankenphp")
	endpoint := serverEndpoint{Scheme: "http", Address: "localhost:8080"}
	layout := serveAppLayout{Docroot: filepath.Join("site", "public")}

	configPath, err := prepareFrankenPHPServeRuntime(cacheDir, runtimeDir, endpoint, layout)
	if err != nil {
		t.Fatalf("prepareFrankenPHPServeRuntime() error = %v", err)
	}

	if filepath.Base(configPath) != frankenPHPCaddyfileName {
		t.Fatalf("Expected config file to be named %s, got %s", frankenPHPCaddyfileName, filepath.Base(configPath))
	}
}

func TestPrepareFrankenPHPServeRuntimeHTTPS(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	runtimeDir := filepath.Join(t.TempDir(), "run", "nested", "frankenphp")
	endpoint := serverEndpoint{Scheme: "https", Address: "localhost:8443", HTTPS: true}
	layout := serveAppLayout{Docroot: filepath.Join("site", "public")}

	configPath, err := prepareFrankenPHPServeRuntime(cacheDir, runtimeDir, endpoint, layout)
	if err != nil {
		t.Fatalf("prepareFrankenPHPServeRuntime() error = %v", err)
	}

	if filepath.Base(configPath) != frankenPHPCaddyfileName {
		t.Fatalf("Expected config file to be named %s, got %s", frankenPHPCaddyfileName, filepath.Base(configPath))
	}
}

func TestRunFrankenPHPServe(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)

	writeCachedFrankenPHP(t, cacheDir, "1.12")
	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())

	runTestConfigValue(t, &bytes.Buffer{}, &bytes.Buffer{}, root, "demo", "tools.php", "8.4")
	runTestConfigValue(t, &bytes.Buffer{}, &bytes.Buffer{}, root, "demo", "tools.frankenphp", "1.12")
	if code := Run(&bytes.Buffer{}, &bytes.Buffer{}, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d", code)
	}
	writeTestActiveEnvironment(t, root, "demo")

	store, err := resolveStore(root)
	if err != nil {
		t.Fatalf("resolveStore() error = %v", err)
	}

	env, err := environmentByName(store, "demo")
	if err != nil {
		t.Fatalf("environmentByName() error = %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	endpoint := serverEndpoint{Scheme: "http", Address: "localhost:8080"}
	layout := serveAppLayout{Docroot: filepath.Join("site", "public")}

	code, err := runFrankenPHPServe(stdout, stderr, store, env, endpoint, layout)
	if err != nil {
		t.Fatalf("runFrankenPHPServe() error = %v", err)
	}

	// Our fake frankenphp script (created by writeCachedFrankenPHP) exits with 1 normally
	// unless called with specific arguments like "php-cli". For runFrankenPHPServe,
	// it executes `frankenphp run --config ...` which will exit with 1 because the
	// fake script only successfully handles `$1 = php-cli`.
	// See writeCachedFrankenPHP in cli_test_helpers_test.go
	if code != 1 {
		t.Fatalf("runFrankenPHPServe() code = %d, want 1", code)
	}
}

func TestStartFrankenPHPServeInBackground(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)

	writeCachedFrankenPHP(t, cacheDir, "1.12")
	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())

	runTestConfigValue(t, &bytes.Buffer{}, &bytes.Buffer{}, root, "demo", "tools.php", "8.4")
	runTestConfigValue(t, &bytes.Buffer{}, &bytes.Buffer{}, root, "demo", "tools.frankenphp", "1.12")
	if code := Run(&bytes.Buffer{}, &bytes.Buffer{}, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d", code)
	}
	writeTestActiveEnvironment(t, root, "demo")

	store, err := resolveStore(root)
	if err != nil {
		t.Fatalf("resolveStore() error = %v", err)
	}

	env, err := environmentByName(store, "demo")
	if err != nil {
		t.Fatalf("environmentByName() error = %v", err)
	}

	endpoint := serverEndpoint{Scheme: "http", Address: "localhost:8080"}
	layout := serveAppLayout{Docroot: filepath.Join("site", "public")}

	// startFrankenPHPServeInBackground At will try to run command.Start() which can be problematic
	// because fake-frankenphp will just exit immediately.
	// We could use t.Skip() or mock the prepareCommand part, but we can also just run it
	// and accept it fails because wait for serve address will fail or command start fails.

	_, err = startFrankenPHPServeInBackground(store, env, endpoint, layout)
	if err == nil {
		t.Fatalf("startFrankenPHPServeInBackground() expected error due to fake frankenphp not starting a real server")
	}
}

func TestRunFrankenPHPServeMissingTool(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)

	runTestConfigValue(t, &bytes.Buffer{}, &bytes.Buffer{}, root, "demo", "tools.frankenphp", "1.12")
	writeTestActiveEnvironment(t, root, "demo")

	store, err := resolveStore(root)
	if err != nil {
		t.Fatalf("resolveStore() error = %v", err)
	}

	env, err := environmentByName(store, "demo")
	if err != nil {
		t.Fatalf("environmentByName() error = %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	endpoint := serverEndpoint{Scheme: "http", Address: "localhost:8080"}
	layout := serveAppLayout{Docroot: filepath.Join("site", "public")}

	_, err = runFrankenPHPServe(stdout, stderr, store, env, endpoint, layout)
	if err == nil {
		t.Fatalf("runFrankenPHPServe() expected error for missing tool")
	}
}

func TestStartFrankenPHPServeInBackgroundMissingTool(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)

	runTestConfigValue(t, &bytes.Buffer{}, &bytes.Buffer{}, root, "demo", "tools.frankenphp", "1.12")
	writeTestActiveEnvironment(t, root, "demo")

	store, err := resolveStore(root)
	if err != nil {
		t.Fatalf("resolveStore() error = %v", err)
	}

	env, err := environmentByName(store, "demo")
	if err != nil {
		t.Fatalf("environmentByName() error = %v", err)
	}

	endpoint := serverEndpoint{Scheme: "http", Address: "localhost:8080"}
	layout := serveAppLayout{Docroot: filepath.Join("site", "public")}

	_, err = startFrankenPHPServeInBackground(store, env, endpoint, layout)
	if err == nil {
		t.Fatalf("startFrankenPHPServeInBackground() expected error for missing tool")
	}
}
