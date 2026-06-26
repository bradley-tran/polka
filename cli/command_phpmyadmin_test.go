package cli

import (
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
