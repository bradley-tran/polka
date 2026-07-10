package backend

import (
	"os"
	"strings"
	"testing"

	"polka/config"
)

// TestStoreRejectsInvalidWorkersSchema checks the raw-YAML schema validation
// for the workers key, mirroring the tools/settings schema tests.
func TestStoreRejectsInvalidWorkersSchema(t *testing.T) {
	for _, test := range []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name: "scalar workers",
			config: strings.Join([]string{
				"workers: queue",
				"",
			}, "\n"),
			wantErr: "workers must be a mapping",
		},
		{
			name: "invalid worker name",
			config: strings.Join([]string{
				"workers:",
				"  \"bad name\":",
				"    command: php run",
				"",
			}, "\n"),
			wantErr: "invalid workers.bad name key",
		},
		{
			name: "scalar worker entry",
			config: strings.Join([]string{
				"workers:",
				"  queue: php artisan queue:work",
				"",
			}, "\n"),
			wantErr: "workers.queue must be a mapping",
		},
		{
			name: "missing command",
			config: strings.Join([]string{
				"workers:",
				"  queue:",
				"    replicas: 2",
				"",
			}, "\n"),
			wantErr: "workers.queue.command must be a non-empty string",
		},
		{
			name: "empty command",
			config: strings.Join([]string{
				"workers:",
				"  queue:",
				"    command: \"  \"",
				"",
			}, "\n"),
			wantErr: "workers.queue.command must be a non-empty string",
		},
		{
			name: "unbalanced command quote",
			config: strings.Join([]string{
				"workers:",
				"  queue:",
				"    command: php \"artisan",
				"",
			}, "\n"),
			wantErr: "workers.queue.command is invalid",
		},
		{
			name: "unsupported worker key",
			config: strings.Join([]string{
				"workers:",
				"  queue:",
				"    command: php artisan queue:work",
				"    restart: always",
				"",
			}, "\n"),
			wantErr: "unsupported workers.queue.restart key",
		},
		{
			name: "zero replicas",
			config: strings.Join([]string{
				"workers:",
				"  queue:",
				"    command: php artisan queue:work",
				"    replicas: 0",
				"",
			}, "\n"),
			wantErr: "workers.queue.replicas must be a positive integer",
		},
		{
			name: "non integer replicas",
			config: strings.Join([]string{
				"workers:",
				"  queue:",
				"    command: php artisan queue:work",
				"    replicas: two",
				"",
			}, "\n"),
			wantErr: "workers.queue.replicas must be a positive integer",
		},
		{
			name: "empty dir",
			config: strings.Join([]string{
				"workers:",
				"  queue:",
				"    command: php artisan queue:work",
				"    dir: \"\"",
				"",
			}, "\n"),
			wantErr: "workers.queue.dir must be a non-empty single-line string",
		},
		{
			name: "scalar env",
			config: strings.Join([]string{
				"workers:",
				"  queue:",
				"    command: php artisan queue:work",
				"    env: TRIES=3",
				"",
			}, "\n"),
			wantErr: "workers.queue.env must be a mapping",
		},
		{
			name: "nested env value",
			config: strings.Join([]string{
				"workers:",
				"  queue:",
				"    command: php artisan queue:work",
				"    env:",
				"      TRIES:",
				"        nested: true",
				"",
			}, "\n"),
			wantErr: "workers.queue.env.TRIES must be a scalar value",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectDir := t.TempDir()
			store := NewProjectStore(projectDir)
			if err := os.WriteFile(store.ConfigFile, []byte("version: 1\nroot: .polka\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(project config) error = %v", err)
			}
			if err := os.WriteFile(store.environmentConfigFile("data"), []byte(test.config), 0o644); err != nil {
				t.Fatalf("WriteFile(config) error = %v", err)
			}

			_, _, err := store.readEnvironment("data")
			if err == nil {
				t.Fatal("readEnvironment(data) error = nil, want schema error")
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("readEnvironment(data) error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

// TestStoreReadsWorkersConfig checks that a valid workers block round-trips
// into the internal environment model.
func TestStoreReadsWorkersConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	if err := os.WriteFile(store.ConfigFile, []byte("version: 1\nroot: .polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(project config) error = %v", err)
	}
	environmentConfig := strings.Join([]string{
		"workers:",
		"  queue:",
		"    command: php artisan queue:work",
		"    replicas: 2",
		"    dir: app",
		"    env:",
		"      TRIES: \"3\"",
		"  scheduler:",
		"    command: php artisan schedule:work",
		"",
	}, "\n")
	if err := os.WriteFile(store.environmentConfigFile("data"), []byte(environmentConfig), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	environment, ok, err := store.readEnvironment("data")
	if err != nil {
		t.Fatalf("readEnvironment(data) error = %v", err)
	}
	if !ok {
		t.Fatal("readEnvironment(data) ok = false, want true")
	}
	queue, exists := environment.Workers["queue"]
	if !exists || queue.Command != "php artisan queue:work" || queue.Replicas != 2 || queue.Dir != "app" || queue.Env["TRIES"] != "3" {
		t.Fatalf("workers.queue = %#v, want parsed worker config", queue)
	}
	scheduler, exists := environment.Workers["scheduler"]
	if !exists || scheduler.Command != "php artisan schedule:work" || scheduler.Replicas != 0 {
		t.Fatalf("workers.scheduler = %#v, want parsed worker config", scheduler)
	}
}

// TestStoreInitWithFrameworkWritesWorkers checks that framework-provided
// worker defaults land in the generated polka.yaml.
func TestStoreInitWithFrameworkWritesWorkers(t *testing.T) {
	for _, test := range []struct {
		framework   string
		worker      string
		wantCommand string
	}{
		{framework: "laravel", worker: "queue", wantCommand: "php artisan queue:work"},
		{framework: "symfony", worker: "messenger", wantCommand: "php bin/console messenger:consume async"},
	} {
		t.Run(test.framework, func(t *testing.T) {
			projectDir := t.TempDir()
			store := NewProjectStore(projectDir)
			if err := store.InitWithFramework(test.framework); err != nil {
				t.Fatalf("InitWithFramework(%s) error = %v", test.framework, err)
			}

			cfg, err := store.loadConfig()
			if err != nil {
				t.Fatalf("loadConfig() error = %v", err)
			}
			environment, exists := cfg.Environments[defaultEnvironmentName]
			if !exists {
				t.Fatalf("environments = %#v, want default environment", cfg.Environments)
			}
			worker, exists := environment.Workers[test.worker]
			if !exists || worker.Command != test.wantCommand {
				t.Fatalf("workers.%s = %#v, want command %q", test.worker, worker, test.wantCommand)
			}

			data, err := os.ReadFile(store.ConfigFile)
			if err != nil {
				t.Fatalf("ReadFile(polka.yaml) error = %v", err)
			}
			if !strings.Contains(string(data), "workers:") || !strings.Contains(string(data), test.wantCommand) {
				t.Fatalf("polka.yaml = %q, want workers block with %q", string(data), test.wantCommand)
			}
		})
	}
}

// TestStoreWritesWorkersConfig checks the save path: an environment with
// workers persists them through writeConfig and reads back identically.
func TestStoreWritesWorkersConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	cfg, err := store.loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	environment := cfg.Environments[defaultEnvironmentName]
	environment.Workers = map[string]config.WorkerConfig{
		"queue": {Command: "php artisan queue:work", Replicas: 2},
	}
	cfg.Environments[defaultEnvironmentName] = environment
	if err := store.writeConfig(cfg); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	reloaded, err := store.loadConfig()
	if err != nil {
		t.Fatalf("loadConfig(reloaded) error = %v", err)
	}
	worker := reloaded.Environments[defaultEnvironmentName].Workers["queue"]
	if worker.Command != "php artisan queue:work" || worker.Replicas != 2 {
		t.Fatalf("reloaded workers.queue = %#v, want persisted worker", worker)
	}
}
