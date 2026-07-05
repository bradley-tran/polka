package service

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"polka/config"
	"polka/tools"
)

func TestRedisServerArgsIncludeLoopbackPortDataDirAndForegroundMode(t *testing.T) {
	spec := RedisServerSpec{
		DataDir: filepath.Join("root", "data", "redis", "demo"),
		Port:    6380,
	}

	got := RedisServerArgs(spec)
	want := []string{
		"--bind", "127.0.0.1",
		"--port", "6380",
		"--dir", spec.DataDir,
		"--daemonize", "no",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RedisServerArgs() = %#v, want %#v", got, want)
	}
}

func TestRedisStateMatchesEnvironmentIncludesPort(t *testing.T) {
	state := RedisRuntimeState{
		Version: "8.8",
		Port:    6380,
	}
	environment := config.Environment{
		Redis: &config.RedisConfig{Version: "8.8", Port: 6380},
	}
	if !RedisStateMatchesEnvironment(state, environment) {
		t.Fatalf("RedisStateMatchesEnvironment() = false, want true")
	}

	environment.Redis.Port = 6381
	if RedisStateMatchesEnvironment(state, environment) {
		t.Fatalf("RedisStateMatchesEnvironment(other port) = true, want false")
	}
	environment.Redis.Port = 6380
	environment.Redis.Version = "8.7"
	if RedisStateMatchesEnvironment(state, environment) {
		t.Fatalf("RedisStateMatchesEnvironment(other version) = true, want false")
	}
}

func TestLoadLiveRedisStateRemovesStaleState(t *testing.T) {
	rootDir := t.TempDir()
	path := RedisStatePath(rootDir, "demo")
	if err := WriteRedisState(path, RedisRuntimeState{
		EnvironmentName: "demo",
		Version:         "8.8",
		Port:            6380,
		PID:             1234,
	}); err != nil {
		t.Fatalf("WriteRedisState() error = %v", err)
	}

	state, err := LoadLiveRedisState(rootDir, "demo", func(string) bool { return false })
	if err != nil {
		t.Fatalf("LoadLiveRedisState() error = %v", err)
	}
	if state != nil {
		t.Fatalf("LoadLiveRedisState() = %#v, want nil", state)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Stat(redis state) error = %v, want removed stale state", err)
	}
}

func TestEnsureManagedRedisStartedAndStop(t *testing.T) {
	rootDir := t.TempDir()
	envsDir := filepath.Join(rootDir, "envs")
	version := "8.8"
	target := filepath.Join(envsDir, toolRedis, version, "bin", "redis-server")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll(target dir) error = %v", err)
	}
	if err := os.WriteFile(target, []byte("redis"), 0o755); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}

	running := map[string]bool{}
	ctx := Context{
		RootDir:  rootDir,
		EnvsDir:  envsDir,
		Registry: redisServiceTestRegistry(t),
		Environment: config.Environment{
			Name:  "demo",
			Redis: &config.RedisConfig{Version: version, Port: 6380},
		},
	}
	hooks := RedisRuntimeHooks{
		StartServer: func(spec RedisServerSpec) (RedisStartResult, error) {
			if spec.Target != target || spec.DataDir != RedisDataPath(rootDir, "demo") || spec.Port != 6380 {
				t.Fatalf("spec = %#v, want resolved target, data dir, and port", spec)
			}
			running[RedisAddress(spec.Port)] = true
			return RedisStartResult{PID: 1234}, nil
		},
		StopServer: func(state RedisRuntimeState) error {
			running[RedisAddress(state.Port)] = false
			return nil
		},
		PingAddress: func(address string) bool {
			return running[address]
		},
		Now: func() time.Time {
			return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
		},
	}

	state, alreadyStarted, err := EnsureManagedRedisStarted(ctx, hooks)
	if err != nil {
		t.Fatalf("EnsureManagedRedisStarted() error = %v", err)
	}
	if alreadyStarted || state.PID != 1234 || state.Port != 6380 {
		t.Fatalf("started state = %#v, alreadyStarted = %v", state, alreadyStarted)
	}

	state, alreadyStopped, err := StopManagedRedis(ctx, hooks)
	if err != nil {
		t.Fatalf("StopManagedRedis() error = %v", err)
	}
	if alreadyStopped || state.PID != 1234 {
		t.Fatalf("stopped state = %#v, alreadyStopped = %v", state, alreadyStopped)
	}
	if _, err := os.Stat(RedisStatePath(rootDir, "demo")); !os.IsNotExist(err) {
		t.Fatalf("state file err = %v, want removed state", err)
	}
}

func redisServiceTestRegistry(t *testing.T) *tools.Registry {
	t.Helper()

	registry, err := tools.NewRegistry(redisServiceTestPlugin{})
	if err != nil {
		t.Fatalf("NewRegistry(redis service test plugin) error = %v", err)
	}

	return registry
}

type redisServiceTestPlugin struct{}

func (redisServiceTestPlugin) ID() string { return toolRedis }

func (redisServiceTestPlugin) Version(environment config.Environment) string {
	if environment.Redis == nil {
		return ""
	}

	return environment.Redis.Version
}

func (redisServiceTestPlugin) PHPExtensions() map[string]bool { return nil }

func (redisServiceTestPlugin) Validate(config.Environment) error { return nil }

func (redisServiceTestPlugin) InstallCandidates(root, version string) []string {
	return []string{filepath.Join(root, toolRedis, version, "bin", "redis-server")}
}

func (redisServiceTestPlugin) DispatchCommands() []string { return nil }

func (redisServiceTestPlugin) CleanupCommands() []string { return nil }

func (redisServiceTestPlugin) ActiveCommands(config.Environment) []string { return nil }

func (redisServiceTestPlugin) DispatchCandidates(root, executable, version string) []string {
	return nil
}

func (redisServiceTestPlugin) Logs() []tools.LogEntry { return nil }

func (redisServiceTestPlugin) Download(tools.DownloadContext) error { return nil }

func (redisServiceTestPlugin) PostInstall(tools.InstallContext) error { return nil }
