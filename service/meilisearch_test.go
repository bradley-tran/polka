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

func TestMeilisearchServerArgsIncludeDataAddressAndOptionalMasterKey(t *testing.T) {
	spec := MeilisearchServerSpec{
		DataDir:   filepath.Join("root", "data", "meilisearch", "demo"),
		Port:      7701,
		MasterKey: "local-dev-key",
	}

	got := MeilisearchServerArgs(spec)
	want := []string{
		"--db-path", spec.DataDir,
		"--http-addr", "127.0.0.1:7701",
		"--master-key", "local-dev-key",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MeilisearchServerArgs() = %#v, want %#v", got, want)
	}

	spec.MasterKey = ""
	got = MeilisearchServerArgs(spec)
	want = []string{"--db-path", spec.DataDir, "--http-addr", "127.0.0.1:7701"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MeilisearchServerArgs(no key) = %#v, want %#v", got, want)
	}
}

func TestMeilisearchStateMatchesEnvironmentIncludesPortAndMasterKeyHash(t *testing.T) {
	state := MeilisearchRuntimeState{
		Version:       "1.48",
		Port:          7701,
		AuthEnabled:   true,
		MasterKeyHash: MeilisearchMasterKeyHash("local-dev-key"),
	}
	environment := config.Environment{
		Meilisearch: &config.MeilisearchConfig{
			Version:   "1.48",
			Port:      7701,
			MasterKey: "local-dev-key",
		},
	}
	if !MeilisearchStateMatchesEnvironment(state, environment) {
		t.Fatalf("MeilisearchStateMatchesEnvironment() = false, want true")
	}

	environment.Meilisearch.MasterKey = "other-key"
	if MeilisearchStateMatchesEnvironment(state, environment) {
		t.Fatalf("MeilisearchStateMatchesEnvironment(other key) = true, want false")
	}
	environment.Meilisearch.MasterKey = "local-dev-key"
	environment.Meilisearch.Port = 7702
	if MeilisearchStateMatchesEnvironment(state, environment) {
		t.Fatalf("MeilisearchStateMatchesEnvironment(other port) = true, want false")
	}
}

func TestWriteMeilisearchStateDoesNotPersistPlaintextMasterKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := MeilisearchRuntimeState{
		EnvironmentName: "demo",
		Version:         "1.48",
		Port:            7701,
		AuthEnabled:     true,
		MasterKeyHash:   MeilisearchMasterKeyHash("local-dev-key"),
	}
	if err := WriteMeilisearchState(path, state); err != nil {
		t.Fatalf("WriteMeilisearchState() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(state) error = %v", err)
	}
	if strings.Contains(string(data), "local-dev-key") {
		t.Fatalf("state = %q, want no plaintext master key", string(data))
	}
	loaded, err := LoadMeilisearchState(path)
	if err != nil {
		t.Fatalf("LoadMeilisearchState() error = %v", err)
	}
	if loaded.MasterKeyHash != state.MasterKeyHash || !loaded.AuthEnabled {
		t.Fatalf("loaded state = %#v, want auth hash", loaded)
	}
}

func TestEnsureManagedMeilisearchStartedAndStop(t *testing.T) {
	rootDir := t.TempDir()
	envsDir := filepath.Join(rootDir, "envs")
	version := "1.48"
	target := filepath.Join(envsDir, toolMeilisearch, version, meilisearchExecutableName())
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll(target dir) error = %v", err)
	}
	if err := os.WriteFile(target, []byte("meilisearch"), 0o755); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}

	running := map[string]bool{}
	ctx := Context{
		RootDir: rootDir,
		EnvsDir: envsDir,
		Environment: config.Environment{
			Name: "demo",
			Meilisearch: &config.MeilisearchConfig{
				Version:   version,
				Port:      7701,
				MasterKey: "local-dev-key",
			},
		},
	}
	hooks := MeilisearchRuntimeHooks{
		StartServer: func(spec MeilisearchServerSpec) (MeilisearchStartResult, error) {
			if spec.Target != target || spec.DataDir != MeilisearchDataPath(rootDir, "demo") || spec.MasterKey != "local-dev-key" {
				t.Fatalf("spec = %#v, want resolved target, data dir, and master key", spec)
			}
			running[MeilisearchAddress(spec.Port)] = true
			return MeilisearchStartResult{PID: 1234}, nil
		},
		StopServer: func(state MeilisearchRuntimeState) error {
			running[MeilisearchAddress(state.Port)] = false
			return nil
		},
		PingAddress: func(address string) bool {
			return running[address]
		},
		Now: func() time.Time {
			return time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
		},
	}

	state, alreadyStarted, err := EnsureManagedMeilisearchStarted(ctx, hooks)
	if err != nil {
		t.Fatalf("EnsureManagedMeilisearchStarted() error = %v", err)
	}
	if alreadyStarted || state.PID != 1234 || !state.AuthEnabled || state.MasterKeyHash != MeilisearchMasterKeyHash("local-dev-key") {
		t.Fatalf("started state = %#v, alreadyStarted = %v", state, alreadyStarted)
	}

	state, alreadyStopped, err := StopManagedMeilisearch(ctx, hooks)
	if err != nil {
		t.Fatalf("StopManagedMeilisearch() error = %v", err)
	}
	if alreadyStopped || state.PID != 1234 {
		t.Fatalf("stopped state = %#v, alreadyStopped = %v", state, alreadyStopped)
	}
	if _, err := os.Stat(MeilisearchStatePath(rootDir, "demo")); !os.IsNotExist(err) {
		t.Fatalf("state file err = %v, want removed state", err)
	}
}

func meilisearchExecutableName() string {
	if runtime.GOOS == "windows" {
		return "meilisearch.exe"
	}

	return "meilisearch"
}
