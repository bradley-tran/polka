package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"polka/backend"
)

func TestStartPHPMyAdminServeUsesSelectedFrankenPHPForHTTPS(t *testing.T) {
	oldPHP := startPHPMyAdminPHPRuntimeServeFunc
	oldNginx := startPHPMyAdminNginxServeFunc
	oldApache := startPHPMyAdminApacheServeFunc
	oldFrankenPHP := startPHPMyAdminFrankenPHPServeFunc
	t.Cleanup(func() {
		startPHPMyAdminPHPRuntimeServeFunc = oldPHP
		startPHPMyAdminNginxServeFunc = oldNginx
		startPHPMyAdminApacheServeFunc = oldApache
		startPHPMyAdminFrankenPHPServeFunc = oldFrankenPHP
	})

	phpCalls := 0
	nginxCalls := 0
	apacheCalls := 0
	frankenPHPCalls := 0
	startPHPMyAdminPHPRuntimeServeFunc = func(backend.Store, backend.Environment, string, serveAppLayout, string) (serveRuntimeState, error) {
		phpCalls++
		return serveRuntimeState{}, nil
	}
	startPHPMyAdminNginxServeFunc = func(backend.Store, backend.Environment, serverEndpoint, serveAppLayout, string) (serveRuntimeState, error) {
		nginxCalls++
		return serveRuntimeState{}, nil
	}
	startPHPMyAdminApacheServeFunc = func(backend.Store, backend.Environment, serverEndpoint, serveAppLayout, string) (serveRuntimeState, error) {
		apacheCalls++
		return serveRuntimeState{}, nil
	}
	startPHPMyAdminFrankenPHPServeFunc = func(_ backend.Store, _ backend.Environment, endpoint serverEndpoint, _ serveAppLayout, runtimeDir string) (serveRuntimeState, error) {
		frankenPHPCalls++
		if !endpoint.HTTPS || endpoint.Address != "127.0.0.1:8082" || runtimeDir != "phpmyadmin-runtime" {
			t.Fatalf("FrankenPHP phpMyAdmin context = %#v, %q, want HTTPS endpoint and runtime dir", endpoint, runtimeDir)
		}
		return serveRuntimeState{ServerKind: "frankenphp"}, nil
	}

	state, err := startPHPMyAdminServe(
		backend.Store{},
		backend.Environment{
			Name:              "demo",
			FrankenPHPVersion: "1.12",
			NginxVersion:      "1.30",
			Server:            &backend.ServerConfig{Type: "frankenphp"},
		},
		serverEndpoint{Scheme: "https", Address: "127.0.0.1:8082", HTTPS: true},
		serveAppLayout{Docroot: "phpmyadmin"},
		"phpmyadmin-runtime",
	)
	if err != nil {
		t.Fatalf("startPHPMyAdminServe() error = %v", err)
	}
	if state.ServerKind != "frankenphp" || phpCalls != 0 || nginxCalls != 0 || apacheCalls != 0 || frankenPHPCalls != 1 {
		t.Fatalf("serve result = %#v, calls php:%d nginx:%d apache:%d frankenphp:%d, want only FrankenPHP", state, phpCalls, nginxCalls, apacheCalls, frankenPHPCalls)
	}
}

func TestStartPHPMyAdminServeUsesSelectedApacheForHTTPS(t *testing.T) {
	oldPHP := startPHPMyAdminPHPRuntimeServeFunc
	oldNginx := startPHPMyAdminNginxServeFunc
	oldApache := startPHPMyAdminApacheServeFunc
	oldFrankenPHP := startPHPMyAdminFrankenPHPServeFunc
	t.Cleanup(func() {
		startPHPMyAdminPHPRuntimeServeFunc = oldPHP
		startPHPMyAdminNginxServeFunc = oldNginx
		startPHPMyAdminApacheServeFunc = oldApache
		startPHPMyAdminFrankenPHPServeFunc = oldFrankenPHP
	})

	phpCalls := 0
	nginxCalls := 0
	apacheCalls := 0
	frankenPHPCalls := 0
	startPHPMyAdminPHPRuntimeServeFunc = func(backend.Store, backend.Environment, string, serveAppLayout, string) (serveRuntimeState, error) {
		phpCalls++
		return serveRuntimeState{}, nil
	}
	startPHPMyAdminNginxServeFunc = func(backend.Store, backend.Environment, serverEndpoint, serveAppLayout, string) (serveRuntimeState, error) {
		nginxCalls++
		return serveRuntimeState{}, nil
	}
	startPHPMyAdminApacheServeFunc = func(_ backend.Store, _ backend.Environment, endpoint serverEndpoint, _ serveAppLayout, runtimeDir string) (serveRuntimeState, error) {
		apacheCalls++
		if !endpoint.HTTPS || endpoint.Address != "127.0.0.1:8082" || runtimeDir != "phpmyadmin-runtime" {
			t.Fatalf("Apache phpMyAdmin context = %#v, %q, want HTTPS endpoint and runtime dir", endpoint, runtimeDir)
		}
		return serveRuntimeState{ServerKind: "apache"}, nil
	}
	startPHPMyAdminFrankenPHPServeFunc = func(backend.Store, backend.Environment, serverEndpoint, serveAppLayout, string) (serveRuntimeState, error) {
		frankenPHPCalls++
		return serveRuntimeState{}, nil
	}

	state, err := startPHPMyAdminServe(
		backend.Store{},
		backend.Environment{
			Name:          "demo",
			PHPVersion:    "8.4",
			ApacheVersion: "2.4",
			NginxVersion:  "1.30",
			Server:        &backend.ServerConfig{Type: "apache"},
		},
		serverEndpoint{Scheme: "https", Address: "127.0.0.1:8082", HTTPS: true},
		serveAppLayout{Docroot: "phpmyadmin"},
		"phpmyadmin-runtime",
	)
	if err != nil {
		t.Fatalf("startPHPMyAdminServe() error = %v", err)
	}
	if state.ServerKind != "apache" || phpCalls != 0 || nginxCalls != 0 || apacheCalls != 1 || frankenPHPCalls != 0 {
		t.Fatalf("serve result = %#v, calls php:%d nginx:%d apache:%d frankenphp:%d, want only Apache", state, phpCalls, nginxCalls, apacheCalls, frankenPHPCalls)
	}
}

func TestStartPHPMyAdminServeUsesRuntimeTLSKeyForEncryptedGlobalKey(t *testing.T) {
	oldNginx := startPHPMyAdminNginxServeFunc
	t.Cleanup(func() {
		startPHPMyAdminNginxServeFunc = oldNginx
	})

	root := t.TempDir()
	runtimeDir := filepath.Join(root, "run", "phpmyadmin", "demo")
	docroot := filepath.Join(root, "envs", "phpmyadmin", "5.2")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(docroot, "index.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(index.php) error = %v", err)
	}

	store := backend.Store{
		RootDir:  root,
		CacheDir: filepath.Join(root, "cache"),
	}
	environment := backend.Environment{
		Name:         "demo",
		PHPVersion:   "8.4",
		NginxVersion: "1.30",
		Server:       &backend.ServerConfig{Type: "nginx"},
	}
	endpoint := serverEndpoint{Scheme: "https", Address: "127.0.0.1:8082", HTTPS: true}
	layout, err := resolveServeAppLayout(docroot)
	if err != nil {
		t.Fatalf("resolveServeAppLayout() error = %v", err)
	}

	startPHPMyAdminNginxServeFunc = func(store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout, runtimeDir string) (serveRuntimeState, error) {
		configPath, _, err := prepareNginxServeRuntime(store.CacheDir, runtimeDir, environment, endpoint, layout, "127.0.0.1:19000")
		if err != nil {
			return serveRuntimeState{}, err
		}

		return serveRuntimeState{
			ServerKind:    "nginx",
			ServerScheme:  endpoint.Scheme,
			ServerAddress: endpoint.Address,
			Docroot:       layout.Docroot,
			RuntimeDir:    runtimeDir,
			ConfigPath:    configPath,
			PrimaryPID:    6262,
		}, nil
	}

	state, err := startPHPMyAdminServe(store, environment, endpoint, layout, runtimeDir)
	if err != nil {
		t.Fatalf("startPHPMyAdminServe() error = %v", err)
	}
	globalKeyPath := filepath.Join(store.CacheDir, "polka", serveTLSSubdir, serveTLSKeyFileName)
	runtimeKeyPath := filepath.Join(runtimeDir, serveTLSKeyFileName)
	assertEncryptedTLSKeyFile(t, globalKeyPath)
	assertPlaintextTLSKeyFile(t, runtimeKeyPath)

	configData, err := os.ReadFile(state.ConfigPath)
	if err != nil {
		t.Fatalf("ReadFile(phpMyAdmin nginx config) error = %v", err)
	}
	config := string(configData)
	if strings.Contains(config, quoteNginxPath(globalKeyPath)) {
		t.Fatalf("phpMyAdmin nginx config = %q, want no encrypted global key path", config)
	}
	if !strings.Contains(config, "ssl_certificate_key "+quoteNginxPath(runtimeKeyPath)+";") {
		t.Fatalf("phpMyAdmin nginx config = %q, want runtime key path", config)
	}
}
