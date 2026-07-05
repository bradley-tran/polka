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

func TestRedisServerArgsIncludeDirBindPortAndOptionalPassword(t *testing.T) {
	spec := RedisServerSpec{
		DataDir:  filepath.Join("root", "data", "redis", "demo"),
		Port:     6380,
		Password: "local-dev-password",
	}

	got := RedisServerArgs(spec)
	want := []string{
		"--dir", spec.DataDir,
		"--bind", "127.0.0.1",
		"--port", "6380",
		"--requirepass", "local-dev-password",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RedisServerArgs() = %#v, want %#v", got, want)
	}

	spec.Password = ""
	got = RedisServerArgs(spec)
	want = []string{"--dir", spec.DataDir, "--bind", "127.0.0.1", "--port", "6380"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RedisServerArgs(no password) = %#v, want %#v", got, want)
	}
}

func TestEffectiveRedisPortDefaultsTo6379(t *testing.T) {
	if got := EffectiveRedisPort(nil); got != DefaultRedisPort {
		t.Fatalf("EffectiveRedisPort(nil) = %d, want %d", got, DefaultRedisPort)
	}
	if got := EffectiveRedisPort(&RedisConfig{}); got != DefaultRedisPort {
		t.Fatalf("EffectiveRedisPort(zero) = %d, want %d", got, DefaultRedisPort)
	}
	if got := EffectiveRedisPort(&RedisConfig{Port: 6390}); got != 6390 {
		t.Fatalf("EffectiveRedisPort(6390) = %d, want 6390", got)
	}
}

func TestRedisURLUsesRedisScheme(t *testing.T) {
	if got := RedisURL(RedisRuntimeState{Port: 6379}); got != "redis://127.0.0.1:6379" {
		t.Fatalf("RedisURL() = %q, want redis://127.0.0.1:6379", got)
	}
}

func TestRedisStateMatchesEnvironmentIncludesPortAndPasswordHash(t *testing.T) {
	state := RedisRuntimeState{
		Version:      "8.8.0",
		Port:         6380,
		AuthEnabled:  true,
		PasswordHash: RedisPasswordHash("local-dev-password"),
	}
	environment := config.Environment{
		Redis: &config.RedisConfig{
			Version:  "8.8.0",
			Port:     6380,
			Password: "local-dev-password",
		},
	}
	if !RedisStateMatchesEnvironment(state, environment) {
		t.Fatalf("RedisStateMatchesEnvironment() = false, want true")
	}

	environment.Redis.Password = "other-password"
	if RedisStateMatchesEnvironment(state, environment) {
		t.Fatalf("RedisStateMatchesEnvironment(other password) = true, want false")
	}
	environment.Redis.Password = "local-dev-password"
	environment.Redis.Port = 6381
	if RedisStateMatchesEnvironment(state, environment) {
		t.Fatalf("RedisStateMatchesEnvironment(other port) = true, want false")
	}
}

func TestWriteRedisStateDoesNotPersistPlaintextPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := RedisRuntimeState{
		EnvironmentName: "demo",
		Version:         "8.8.0",
		Port:            6380,
		AuthEnabled:     true,
		PasswordHash:    RedisPasswordHash("local-dev-password"),
	}
	if err := WriteRedisState(path, state); err != nil {
		t.Fatalf("WriteRedisState() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(state) error = %v", err)
	}
	if strings.Contains(string(data), "local-dev-password") {
		t.Fatalf("state = %q, want no plaintext password", string(data))
	}
	loaded, err := LoadRedisState(path)
	if err != nil {
		t.Fatalf("LoadRedisState() error = %v", err)
	}
	if loaded.PasswordHash != state.PasswordHash || !loaded.AuthEnabled {
		t.Fatalf("loaded state = %#v, want auth hash", loaded)
	}
}

func TestEnsureManagedRedisStartedAndStop(t *testing.T) {
	rootDir := t.TempDir()
	envsDir := filepath.Join(rootDir, "envs")
	version := "8.8.0"
	target := filepath.Join(envsDir, toolRedis, version, redisExecutableName())
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll(target dir) error = %v", err)
	}
	if err := os.WriteFile(target, []byte("redis-server"), 0o755); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}

	running := map[string]bool{}
	ctx := Context{
		RootDir: rootDir,
		EnvsDir: envsDir,
		Environment: config.Environment{
			Name: "demo",
			Redis: &config.RedisConfig{
				Version:  version,
				Port:     6380,
				Password: "local-dev-password",
			},
		},
	}
	hooks := RedisRuntimeHooks{
		StartServer: func(spec RedisServerSpec) (RedisStartResult, error) {
			if spec.Target != target || spec.DataDir != RedisDataPath(rootDir, "demo") || spec.Password != "local-dev-password" {
				t.Fatalf("spec = %#v, want resolved target, data dir, and password", spec)
			}
			running[RedisAddress(spec.Port)] = true
			return RedisStartResult{PID: 4321}, nil
		},
		StopServer: func(state RedisRuntimeState) error {
			running[RedisAddress(state.Port)] = false
			return nil
		},
		PingAddress: func(address string) bool {
			return running[address]
		},
		Now: func() time.Time {
			return time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
		},
	}

	state, alreadyStarted, err := EnsureManagedRedisStarted(ctx, hooks)
	if err != nil {
		t.Fatalf("EnsureManagedRedisStarted() error = %v", err)
	}
	if alreadyStarted || state.PID != 4321 || !state.AuthEnabled || state.PasswordHash != RedisPasswordHash("local-dev-password") {
		t.Fatalf("started state = %#v, alreadyStarted = %v", state, alreadyStarted)
	}

	state, alreadyStopped, err := StopManagedRedis(ctx, hooks)
	if err != nil {
		t.Fatalf("StopManagedRedis() error = %v", err)
	}
	if alreadyStopped || state.PID != 4321 {
		t.Fatalf("stopped state = %#v, alreadyStopped = %v", state, alreadyStopped)
	}
	if _, err := os.Stat(RedisStatePath(rootDir, "demo")); !os.IsNotExist(err) {
		t.Fatalf("state file err = %v, want removed state", err)
	}
}

func redisExecutableName() string {
	if runtime.GOOS == "windows" {
		return "redis-server.exe"
	}

	return "redis-server"
}
