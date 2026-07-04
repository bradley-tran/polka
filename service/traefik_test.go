package service

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"polka/config"
)

func TestTraefikServerArgsIncludeEntrypointDashboardAndConfigDir(t *testing.T) {
	spec := TraefikServerSpec{
		ConfigDir: filepath.Join("root", "run", "traefik", "demo", "dynamic"),
		Port:      8090,
	}

	got := TraefikServerArgs(spec)
	want := []string{
		"--entrypoints.traefik.address=127.0.0.1:8090",
		"--api.dashboard=true",
		"--api.insecure=true",
		"--ping=true",
		"--providers.file.directory=" + spec.ConfigDir,
		"--providers.file.watch=true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TraefikServerArgs() = %#v, want %#v", got, want)
	}

	spec.ConfigDir = ""
	got = TraefikServerArgs(spec)
	want = []string{
		"--entrypoints.traefik.address=127.0.0.1:8090",
		"--api.dashboard=true",
		"--api.insecure=true",
		"--ping=true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TraefikServerArgs(no config dir) = %#v, want %#v", got, want)
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
