package cli

// This file contains the FrankenPHP serve lifecycle: foreground and background
// startup plus Caddyfile generation.

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"polka/backend"
	"polka/config"
	"polka/service"
)

const frankenPHPCaddyfileName = "Caddyfile"

// Test seams for the FrankenPHP webserver lifecycle.
var (
	runFrankenPHPServeFunc         = runFrankenPHPServe
	startBackgroundFrankenPHPServe = startFrankenPHPServeInBackground
)

// runFrankenPHPServe runs FrankenPHP attached to the current terminal.
func runFrankenPHPServe(stdout, stderr io.Writer, store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout) (int, error) {
	frankenPHPTarget, err := store.ResolveTool(config.ServerTypeFrankenPHP)
	if err != nil {
		return 0, err
	}
	env, err := resolveFrankenPHPRuntimeEnvironment(store, frankenPHPTarget)
	if err != nil {
		return 0, err
	}
	runtimeDir := service.ToolLogRoot(store.RootDir, config.ServerTypeFrankenPHP, environment.Name)
	configPath, err := prepareFrankenPHPServeRuntime(store.CacheDir, runtimeDir, endpoint, layout)
	if err != nil {
		return 0, err
	}

	args := []string{"run", "--config", configPath, "--adapter", "caddyfile"}
	return executeTargetWithEnv(stdout, stderr, env, frankenPHPTarget, args)
}

// startFrankenPHPServeInBackground starts and records the managed FrankenPHP process.
func startFrankenPHPServeInBackground(store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout) (serveRuntimeState, error) {
	runtimeDir := service.ToolLogRoot(store.RootDir, config.ServerTypeFrankenPHP, environment.Name)
	return startFrankenPHPServeInBackgroundAt(store, environment, endpoint, layout, runtimeDir)
}

func startFrankenPHPServeInBackgroundAt(store backend.Store, environment backend.Environment, endpoint serverEndpoint, layout serveAppLayout, runtimeDir string) (serveRuntimeState, error) {
	frankenPHPTarget, err := store.ResolveTool(config.ServerTypeFrankenPHP)
	if err != nil {
		return serveRuntimeState{}, err
	}
	env, err := resolveFrankenPHPRuntimeEnvironment(store, frankenPHPTarget)
	if err != nil {
		return serveRuntimeState{}, err
	}
	configPath, err := prepareFrankenPHPServeRuntime(store.CacheDir, runtimeDir, endpoint, layout)
	if err != nil {
		return serveRuntimeState{}, err
	}
	logPath := filepath.Join(runtimeDir, serveLogFileName)
	logFile, err := openServeLog(logPath)
	if err != nil {
		return serveRuntimeState{}, err
	}

	args := []string{"run", "--config", configPath, "--adapter", "caddyfile"}
	command, err := prepareCommand(frankenPHPTarget, args)
	if err != nil {
		_ = logFile.Close()
		return serveRuntimeState{}, err
	}
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = env

	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return serveRuntimeState{}, fmt.Errorf("start FrankenPHP webserver: %w", err)
	}
	if err := waitForServeAddress(serveProbeAddress(endpoint.Address), serveStartupTimeout); err != nil {
		stopServeProcess(command.Process)
		_ = logFile.Close()
		return serveRuntimeState{}, fmt.Errorf("start FrankenPHP webserver on %s: %w (see %s)", endpoint.Address, err, logPath)
	}

	state := serveRuntimeState{
		EnvironmentName: environment.Name,
		ServerKind:      config.ServerTypeFrankenPHP,
		ServerScheme:    endpoint.Scheme,
		ServerAddress:   endpoint.Address,
		Docroot:         layout.Docroot,
		RuntimeDir:      runtimeDir,
		LogPath:         logPath,
		ConfigPath:      configPath,
		PrimaryPID:      command.Process.Pid,
		StartedAt:       serveNowFunc().UTC(),
	}
	_ = logFile.Close()
	_ = command.Process.Release()

	return state, nil
}

// resolveFrankenPHPRuntimeEnvironment points the Windows bundle at its managed php.ini.
func resolveFrankenPHPRuntimeEnvironment(store backend.Store, frankenPHPTarget string) ([]string, error) {
	env, err := resolveRuntimeEnvironment(runtime.GOOS, os.Environ(), store)
	if err != nil {
		return nil, err
	}
	return applyManagedPHPRuntimeConfig(runtime.GOOS, env, frankenPHPTarget)
}

// prepareFrankenPHPServeRuntime writes the generated Caddyfile and TLS material references.
func prepareFrankenPHPServeRuntime(cacheDir, runtimeDir string, endpoint serverEndpoint, layout serveAppLayout) (string, error) {
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return "", fmt.Errorf("create FrankenPHP runtime directory: %w", err)
	}

	host, _, err := net.SplitHostPort(endpoint.Address)
	if err != nil {
		return "", fmt.Errorf("parse serve address %q: %w", endpoint.Address, err)
	}
	tlsConfig := serveTLSConfig{}
	if endpoint.HTTPS {
		certPath, keyPath, err := ensureGlobalTLSCertificateRuntimeKey(cacheDir, runtimeDir, host)
		if err != nil {
			return "", err
		}
		tlsConfig = serveTLSConfig{
			Enabled:            true,
			CertificatePath:    certPath,
			CertificateKeyPath: keyPath,
		}
	}

	configPath := filepath.Join(runtimeDir, frankenPHPCaddyfileName)
	configData := renderFrankenPHPCaddyfile(endpoint, layout, tlsConfig)
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		return "", fmt.Errorf("write FrankenPHP Caddyfile: %w", err)
	}

	return configPath, nil
}

// renderFrankenPHPCaddyfile renders a self-contained development server configuration.
func renderFrankenPHPCaddyfile(endpoint serverEndpoint, layout serveAppLayout, tlsConfig serveTLSConfig) []byte {
	var builder strings.Builder
	builder.WriteString("{\n")
	builder.WriteString("\tadmin off\n")
	builder.WriteString("\tauto_https disable_redirects\n")
	builder.WriteString("}\n\n")
	builder.WriteString(endpoint.Scheme)
	builder.WriteString("://")
	builder.WriteString(endpoint.Address)
	builder.WriteString(" {\n")
	builder.WriteString("\troot * ")
	builder.WriteString(strconv.Quote(filepath.ToSlash(layout.Docroot)))
	builder.WriteString("\n")
	builder.WriteString("\tencode zstd br gzip\n")
	if tlsConfig.Enabled {
		builder.WriteString("\ttls ")
		builder.WriteString(strconv.Quote(filepath.ToSlash(tlsConfig.CertificatePath)))
		builder.WriteString(" ")
		builder.WriteString(strconv.Quote(filepath.ToSlash(tlsConfig.CertificateKeyPath)))
		builder.WriteString("\n")
	}
	builder.WriteString("\tphp_server\n")
	builder.WriteString("}\n")

	return []byte(builder.String())
}
