package plugins

import (
	"strings"
	"testing"
)

func TestBuiltinFrameworkManifestsLoad(t *testing.T) {
	for _, framework := range []string{CakePHP, CodeIgniter, Drupal, WordPress, Laravel, Symfony} {
		t.Run(framework, func(t *testing.T) {
			manifest, err := loadBuiltinFrameworkManifest(framework)
			if err != nil {
				t.Fatalf("loadBuiltinFrameworkManifest(%s) error = %v", framework, err)
			}
			if manifest.ID != framework {
				t.Fatalf("loadBuiltinFrameworkManifest(%s) id = %q, want %q", framework, manifest.ID, framework)
			}
			if manifest.Defaults.Framework != framework {
				t.Fatalf("manifest defaults.framework = %q, want %q", manifest.Defaults.Framework, framework)
			}
		})
	}
}

// TestBuiltinFrameworkManifestWorkerDefaults checks the shipped worker
// presets for frameworks that define background workers.
func TestBuiltinFrameworkManifestWorkerDefaults(t *testing.T) {
	laravel, err := loadBuiltinFrameworkManifest(Laravel)
	if err != nil {
		t.Fatalf("loadBuiltinFrameworkManifest(laravel) error = %v", err)
	}
	if got := laravel.Defaults.Workers["queue"].Command; got != "php artisan queue:work" {
		t.Fatalf("laravel workers.queue.command = %q, want queue:work preset", got)
	}

	symfony, err := loadBuiltinFrameworkManifest(Symfony)
	if err != nil {
		t.Fatalf("loadBuiltinFrameworkManifest(symfony) error = %v", err)
	}
	if got := symfony.Defaults.Workers["messenger"].Command; got != "php bin/console messenger:consume async" {
		t.Fatalf("symfony workers.messenger.command = %q, want messenger:consume preset", got)
	}

	// Worker defaults must survive the plugin conversion used by init.
	plugin := newManifestFrameworkPlugin(Laravel)
	if got := plugin.Defaults().Workers["queue"].Command; got != "php artisan queue:work" {
		t.Fatalf("laravel plugin Defaults().Workers = %q, want queue:work preset", got)
	}
}

// TestParseFrameworkManifestRejectsInvalidWorkers checks that manifests with
// broken worker definitions fail to load.
func TestParseFrameworkManifestRejectsInvalidWorkers(t *testing.T) {
	_, err := parseFrameworkManifest([]byte(`
id: demo
defaults:
  docroot: public
  workers:
    queue:
      replicas: 2
`))
	if err == nil || !strings.Contains(err.Error(), "requires a command") {
		t.Fatalf("parseFrameworkManifest(worker without command) error = %v, want command error", err)
	}

	_, err = parseFrameworkManifest([]byte(`
id: demo
defaults:
  docroot: public
  workers:
    "bad name":
      command: php run
`))
	if err == nil || !strings.Contains(err.Error(), "invalid worker name") {
		t.Fatalf("parseFrameworkManifest(invalid worker name) error = %v, want name error", err)
	}
}

// TestBuiltinFrameworkManifestCronDefaults checks the shipped cron presets
// and that they survive the plugin conversion used by init.
func TestBuiltinFrameworkManifestCronDefaults(t *testing.T) {
	for _, test := range []struct {
		framework    string
		job          string
		wantSchedule string
		wantCommand  string
	}{
		{framework: Laravel, job: "scheduler", wantSchedule: "* * * * *", wantCommand: "php artisan schedule:run"},
		{framework: Drupal, job: "drupal", wantSchedule: "@hourly", wantCommand: "php vendor/bin/drush cron"},
	} {
		t.Run(test.framework, func(t *testing.T) {
			manifest, err := loadBuiltinFrameworkManifest(test.framework)
			if err != nil {
				t.Fatalf("loadBuiltinFrameworkManifest(%s) error = %v", test.framework, err)
			}
			job := manifest.Defaults.Cron[test.job]
			if job.Schedule != test.wantSchedule || job.Command != test.wantCommand {
				t.Fatalf("%s cron.%s = %#v, want %q %q", test.framework, test.job, job, test.wantSchedule, test.wantCommand)
			}

			plugin := newManifestFrameworkPlugin(test.framework)
			if got := plugin.Defaults().Cron[test.job].Command; got != test.wantCommand {
				t.Fatalf("%s plugin Defaults().Cron[%s].Command = %q, want %q", test.framework, test.job, got, test.wantCommand)
			}
		})
	}
}

// TestParseFrameworkManifestRejectsInvalidCron checks that manifests with
// broken cron definitions fail to load.
func TestParseFrameworkManifestRejectsInvalidCron(t *testing.T) {
	_, err := parseFrameworkManifest([]byte(`
id: demo
defaults:
  docroot: public
  cron:
    tick:
      schedule: "61 * * * *"
      command: php run
`))
	if err == nil || !strings.Contains(err.Error(), "outside 0-59") {
		t.Fatalf("parseFrameworkManifest(invalid schedule) error = %v, want schedule error", err)
	}

	_, err = parseFrameworkManifest([]byte(`
id: demo
defaults:
  docroot: public
  cron:
    tick:
      command: php run
`))
	if err == nil || !strings.Contains(err.Error(), "requires a schedule") {
		t.Fatalf("parseFrameworkManifest(missing schedule) error = %v, want schedule error", err)
	}
}

func TestParseFrameworkManifestRequiresID(t *testing.T) {
	_, err := parseFrameworkManifest([]byte(`
defaults:
  docroot: public
`))
	if err == nil || !strings.Contains(err.Error(), "id cannot be empty") {
		t.Fatalf("parseFrameworkManifest(missing id) error = %v, want missing id error", err)
	}
}

func TestParseFrameworkManifestRejectsInvalidID(t *testing.T) {
	_, err := parseFrameworkManifest([]byte(`
id: "not a framework"
defaults:
  docroot: public
`))
	if err == nil || !strings.Contains(err.Error(), "invalid framework manifest id") {
		t.Fatalf("parseFrameworkManifest(invalid id) error = %v, want invalid id error", err)
	}
}

func TestParseFrameworkManifestRejectsDefaultFrameworkMismatch(t *testing.T) {
	_, err := parseFrameworkManifest([]byte(`
id: laravel
defaults:
  framework: symfony
  docroot: public
`))
	if err == nil || !strings.Contains(err.Error(), "defaults.framework must match id") {
		t.Fatalf("parseFrameworkManifest(framework mismatch) error = %v, want mismatch error", err)
	}
}

func TestParseFrameworkManifestRequiresDocroot(t *testing.T) {
	_, err := parseFrameworkManifest([]byte(`
id: demo
defaults:
  tools:
    php: "8.4"
`))
	if err == nil || !strings.Contains(err.Error(), "defaults.docroot cannot be empty") {
		t.Fatalf("parseFrameworkManifest(missing docroot) error = %v, want docroot error", err)
	}
}
