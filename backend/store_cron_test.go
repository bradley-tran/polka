package backend

import (
	"os"
	"strings"
	"testing"

	"polka/config"
)

// TestStoreRejectsInvalidCronSchema checks the raw-YAML schema validation for
// the cron key, mirroring the workers schema tests.
func TestStoreRejectsInvalidCronSchema(t *testing.T) {
	for _, test := range []struct {
		name    string
		lines   []string
		wantErr string
	}{
		{name: "scalar cron", lines: []string{"cron: tick"}, wantErr: "cron must be a mapping"},
		{name: "invalid job name", lines: []string{"cron:", "  \"bad name\":", "    schedule: \"@hourly\"", "    command: php run"}, wantErr: "invalid cron.bad name key"},
		{name: "scalar job entry", lines: []string{"cron:", "  tick: php run"}, wantErr: "cron.tick must be a mapping"},
		{name: "missing schedule", lines: []string{"cron:", "  tick:", "    command: php run"}, wantErr: "cron.tick.schedule must be a non-empty string"},
		{name: "non string schedule", lines: []string{"cron:", "  tick:", "    schedule: 5", "    command: php run"}, wantErr: "cron.tick.schedule must be a non-empty string"},
		{name: "invalid schedule", lines: []string{"cron:", "  tick:", "    schedule: \"61 * * * *\"", "    command: php run"}, wantErr: "cron.tick.schedule is invalid"},
		{name: "missing command", lines: []string{"cron:", "  tick:", "    schedule: \"@hourly\""}, wantErr: "cron.tick.command must be a non-empty string"},
		{name: "empty command", lines: []string{"cron:", "  tick:", "    schedule: \"@hourly\"", "    command: \"  \""}, wantErr: "cron.tick.command must be a non-empty string"},
		{name: "unbalanced command quote", lines: []string{"cron:", "  tick:", "    schedule: \"@hourly\"", "    command: php \"run"}, wantErr: "cron.tick.command is invalid"},
		{name: "unsupported key", lines: []string{"cron:", "  tick:", "    schedule: \"@hourly\"", "    command: php run", "    replicas: 2"}, wantErr: "unsupported cron.tick.replicas key"},
		{name: "empty dir", lines: []string{"cron:", "  tick:", "    schedule: \"@hourly\"", "    command: php run", "    dir: \"\""}, wantErr: "cron.tick.dir must be a non-empty single-line string"},
		{name: "scalar env", lines: []string{"cron:", "  tick:", "    schedule: \"@hourly\"", "    command: php run", "    env: A=1"}, wantErr: "cron.tick.env must be a mapping"},
		{name: "nested env value", lines: []string{"cron:", "  tick:", "    schedule: \"@hourly\"", "    command: php run", "    env:", "      A:", "        nested: true"}, wantErr: "cron.tick.env.A must be a scalar value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectDir := t.TempDir()
			store := NewProjectStore(projectDir)
			if err := os.WriteFile(store.ConfigFile, []byte("version: 0.2\nroot: .polka\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(project config) error = %v", err)
			}
			content := strings.Join(append(test.lines, ""), "\n")
			if err := os.WriteFile(store.environmentConfigFile("data"), []byte(content), 0o644); err != nil {
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

// TestStoreReadsCronConfig checks that a valid cron block reaches the
// internal environment model normalized.
func TestStoreReadsCronConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	if err := os.WriteFile(store.ConfigFile, []byte("version: 0.2\nroot: .polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(project config) error = %v", err)
	}
	environmentConfig := strings.Join([]string{
		"cron:",
		"  scheduler:",
		"    schedule: \"*  * * * *\"",
		"    command: php artisan schedule:run",
		"    dir: app",
		"    env:",
		"      A: \"1\"",
		"  drupal:",
		"    schedule: \"@hourly\"",
		"    command: php vendor/bin/drush cron",
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
	scheduler, exists := environment.Cron["scheduler"]
	if !exists || scheduler.Schedule != "* * * * *" || scheduler.Command != "php artisan schedule:run" || scheduler.Dir != "app" || scheduler.Env["A"] != "1" {
		t.Fatalf("cron.scheduler = %#v, want parsed cron job", scheduler)
	}
	if drupal := environment.Cron["drupal"]; drupal.Schedule != "@hourly" || drupal.Command != "php vendor/bin/drush cron" {
		t.Fatalf("cron.drupal = %#v, want parsed cron job", drupal)
	}
}

// TestStoreWritesCronConfig checks the save path: an environment with cron
// jobs persists them through writeConfig and reads back identically.
func TestStoreWritesCronConfig(t *testing.T) {
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
	environment.Cron = map[string]config.CronJobConfig{
		"tick": {Schedule: "*/5 * * * *", Command: "php tick.php"},
	}
	cfg.Environments[defaultEnvironmentName] = environment
	if err := store.writeConfig(cfg); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	reloaded, err := store.loadConfig()
	if err != nil {
		t.Fatalf("loadConfig(reloaded) error = %v", err)
	}
	job := reloaded.Environments[defaultEnvironmentName].Cron["tick"]
	if job.Schedule != "*/5 * * * *" || job.Command != "php tick.php" {
		t.Fatalf("reloaded cron.tick = %#v, want persisted job", job)
	}
}

// TestStoreInitWithFrameworkWritesCron checks that framework-provided cron
// defaults land in the generated polka.yaml.
func TestStoreInitWithFrameworkWritesCron(t *testing.T) {
	for _, test := range []struct {
		framework    string
		job          string
		wantSchedule string
		wantCommand  string
	}{
		{framework: "laravel", job: "scheduler", wantSchedule: "* * * * *", wantCommand: "php artisan schedule:run"},
		{framework: "drupal", job: "drupal", wantSchedule: "@hourly", wantCommand: "php vendor/bin/drush cron"},
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
			job, exists := cfg.Environments[defaultEnvironmentName].Cron[test.job]
			if !exists || job.Schedule != test.wantSchedule || job.Command != test.wantCommand {
				t.Fatalf("cron.%s = %#v, want %q %q", test.job, job, test.wantSchedule, test.wantCommand)
			}
		})
	}
}
