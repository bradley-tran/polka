package service

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"polka/config"
)

func TestTraefikServerArgsIncludeWebEntrypointAndConfigDir(t *testing.T) {
	spec := TraefikServerSpec{
		ConfigDir: filepath.Join("root", "run", "traefik", "demo", "dynamic"),
		Port:      8090,
	}

	got := TraefikServerArgs(spec)
	want := []string{
		"--entrypoints.web.address=127.0.0.1:8090",
		"--providers.file.directory=" + spec.ConfigDir,
		"--providers.file.watch=true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TraefikServerArgs() = %#v, want %#v", got, want)
	}

	spec.ConfigDir = ""
	got = TraefikServerArgs(spec)
	want = []string{"--entrypoints.web.address=127.0.0.1:8090"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TraefikServerArgs(no config dir) = %#v, want %#v", got, want)
	}
}

func TestRenderTraefikDynamicConfigRoutesToUpstream(t *testing.T) {
	// A plain HTTP upstream needs no insecure transport and no TLS termination.
	got := string(RenderTraefikDynamicConfig("polka-demo", "http://localhost:8000", false, nil))
	for _, want := range []string{
		"http://localhost:8000",
		"entryPoints:\n        - web",
		"service: polka-demo",
		"PathPrefix(`/`)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderTraefikDynamicConfig() = %q, want to contain %q", got, want)
		}
	}
	if strings.Contains(got, "insecureSkipVerify") || strings.Contains(got, "tls:") {
		t.Fatalf("RenderTraefikDynamicConfig(http) = %q, want no insecure transport or tls", got)
	}

	// A TLS upstream (Polka's self-signed cert) skips verification.
	backendTLS := string(RenderTraefikDynamicConfig("polka-demo", "https://localhost:8000", true, nil))
	if !strings.Contains(backendTLS, "insecureSkipVerify: true") || !strings.Contains(backendTLS, "serversTransport: polka-demo") {
		t.Fatalf("RenderTraefikDynamicConfig(https backend) = %q, want insecure transport", backendTLS)
	}
}

func TestRenderTraefikDynamicConfigTerminatesTLS(t *testing.T) {
	got := string(RenderTraefikDynamicConfig("polka-demo", "https://localhost:8000", true, &TraefikTLSConfig{
		CertificatePath: filepath.Join("root", "cert.crt"),
		KeyPath:         filepath.Join("root", "cert.key"),
	}))
	for _, want := range []string{
		"      tls: {}",
		"tls:\n  certificates:",
		"certFile: \"root/cert.crt\"",
		"keyFile: \"root/cert.key\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderTraefikDynamicConfig(tls) = %q, want to contain %q", got, want)
		}
	}
}

func TestWriteTraefikDynamicConfigWritesManagedFile(t *testing.T) {
	rootDir := t.TempDir()
	if err := WriteTraefikDynamicConfig(rootDir, "demo", "http://localhost:8000", false, nil); err != nil {
		t.Fatalf("WriteTraefikDynamicConfig() error = %v", err)
	}
	path := TraefikDynamicConfigPath(rootDir, "demo")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(dynamic config) error = %v", err)
	}
	if !strings.Contains(string(data), "http://localhost:8000") {
		t.Fatalf("dynamic config = %q, want upstream URL", string(data))
	}
}

func TestTraefikRouterNameSanitizesEnvironment(t *testing.T) {
	if got := TraefikRouterName("blog/staging"); got != "polka-blog-staging" {
		t.Fatalf("TraefikRouterName(blog/staging) = %q, want polka-blog-staging", got)
	}
	if got := TraefikRouterName(""); got != "polka-default" {
		t.Fatalf("TraefikRouterName(empty) = %q, want polka-default", got)
	}
}

func TestTraefikStateMatchesEnvironmentIncludesPort(t *testing.T) {
	state := TraefikRuntimeState{Version: "3.3", Port: 8090}
	environment := config.Environment{
		Traefik: &config.TraefikConfig{Version: "3.3", Port: 8090},
	}
	if !TraefikStateMatchesEnvironment(state, environment) {
		t.Fatalf("TraefikStateMatchesEnvironment() = false, want true")
	}

	environment.Traefik.Port = 8091
	if TraefikStateMatchesEnvironment(state, environment) {
		t.Fatalf("TraefikStateMatchesEnvironment(other port) = true, want false")
	}
	environment.Traefik.Port = 8090
	environment.Traefik.Version = "3.4"
	if TraefikStateMatchesEnvironment(state, environment) {
		t.Fatalf("TraefikStateMatchesEnvironment(other version) = true, want false")
	}
}

func TestEffectiveTraefikPortFallsBackToDefault(t *testing.T) {
	if got := EffectiveTraefikPort(nil); got != DefaultTraefikPort {
		t.Fatalf("EffectiveTraefikPort(nil) = %d, want %d", got, DefaultTraefikPort)
	}
	if got := EffectiveTraefikPort(&config.TraefikConfig{Port: 9000}); got != 9000 {
		t.Fatalf("EffectiveTraefikPort(9000) = %d, want 9000", got)
	}
}

func TestEnsureManagedTraefikStartedAndStop(t *testing.T) {
	rootDir := t.TempDir()
	envsDir := filepath.Join(rootDir, "envs")
	version := "3.3"
	target := filepath.Join(envsDir, toolTraefik, version, traefikExecutableName())
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll(target dir) error = %v", err)
	}
	if err := os.WriteFile(target, []byte("traefik"), 0o755); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}

	running := map[string]bool{}
	ctx := Context{
		RootDir: rootDir,
		EnvsDir: envsDir,
		Environment: config.Environment{
			Name:    "demo",
			Traefik: &config.TraefikConfig{Version: version, Port: 8090},
		},
	}
	hooks := TraefikRuntimeHooks{
		StartServer: func(spec TraefikServerSpec) (TraefikStartResult, error) {
			if spec.Target != target || spec.ConfigDir != TraefikConfigDir(rootDir, "demo") {
				t.Fatalf("spec = %#v, want resolved target and config dir", spec)
			}
			running[TraefikAddress(spec.Port)] = true
			return TraefikStartResult{PID: 4321}, nil
		},
		StopServer: func(state TraefikRuntimeState) error {
			running[TraefikAddress(state.Port)] = false
			return nil
		},
		PingAddress: func(address string) bool {
			return running[address]
		},
		Now: func() time.Time {
			return time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
		},
	}

	state, alreadyStarted, err := EnsureManagedTraefikStarted(ctx, hooks)
	if err != nil {
		t.Fatalf("EnsureManagedTraefikStarted() error = %v", err)
	}
	if alreadyStarted || state.PID != 4321 || state.Port != 8090 {
		t.Fatalf("started state = %#v, alreadyStarted = %v", state, alreadyStarted)
	}
	if _, err := os.Stat(TraefikStatePath(rootDir, "demo")); err != nil {
		t.Fatalf("Stat(state) error = %v, want persisted state", err)
	}

	// A second call finds the live service and reports it already started.
	_, alreadyStarted, err = EnsureManagedTraefikStarted(ctx, hooks)
	if err != nil {
		t.Fatalf("EnsureManagedTraefikStarted(second) error = %v", err)
	}
	if !alreadyStarted {
		t.Fatalf("second EnsureManagedTraefikStarted alreadyStarted = false, want true")
	}

	_, alreadyStopped, err := StopManagedTraefik(ctx, hooks)
	if err != nil {
		t.Fatalf("StopManagedTraefik() error = %v", err)
	}
	if alreadyStopped {
		t.Fatalf("StopManagedTraefik alreadyStopped = true, want false")
	}
	if _, err := os.Stat(TraefikStatePath(rootDir, "demo")); !os.IsNotExist(err) {
		t.Fatalf("state file err = %v, want removed state", err)
	}
}

func traefikExecutableName() string {
	if runtime.GOOS == "windows" {
		return "traefik.exe"
	}

	return "traefik"
}
