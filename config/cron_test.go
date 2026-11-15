package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestNormalizeCronConfig covers name canonicalization, schedule whitespace
// collapsing, entry dropping, and the nil-when-empty contract that keeps the
// cron key omitted on save.
func TestNormalizeCronConfig(t *testing.T) {
	normalized := NormalizeCronConfig(map[string]CronJobConfig{
		" Scheduler ": {Schedule: "  *  * * * * ", Command: "  php artisan schedule:run ", Dir: " . ", Env: map[string]string{" A ": "1"}},
		"empty":       {Schedule: "@hourly", Command: "  "},
		"  ":          {Schedule: "@hourly", Command: "php run"},
	})

	want := map[string]CronJobConfig{
		"scheduler": {Schedule: "* * * * *", Command: "php artisan schedule:run", Dir: ".", Env: map[string]string{"A": "1"}},
	}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("NormalizeCronConfig() = %#v, want %#v", normalized, want)
	}

	if got := NormalizeCronConfig(map[string]CronJobConfig{"x": {Command: " "}}); got != nil {
		t.Fatalf("NormalizeCronConfig(all dropped) = %#v, want nil", got)
	}
	if got := NormalizeCronConfig(nil); got != nil {
		t.Fatalf("NormalizeCronConfig(nil) = %#v, want nil", got)
	}
}

// TestValidateCronConfig checks the error reported for each invalid job
// definition and that a valid definition passes.
func TestValidateCronConfig(t *testing.T) {
	valid := map[string]CronJobConfig{
		"scheduler": {Schedule: "* * * * *", Command: "php artisan schedule:run"},
		"drupal":    {Schedule: "@hourly", Command: "php vendor/bin/drush cron", Env: map[string]string{"A": "1"}},
	}
	if err := ValidateCronConfig(valid); err != nil {
		t.Fatalf("ValidateCronConfig(valid) error = %v", err)
	}

	for _, test := range []struct {
		name    string
		jobs    map[string]CronJobConfig
		wantErr string
	}{
		{name: "bad name", jobs: map[string]CronJobConfig{"bad name": {Schedule: "@hourly", Command: "php run"}}, wantErr: "invalid cron job name"},
		{name: "missing schedule", jobs: map[string]CronJobConfig{"a": {Command: "php run"}}, wantErr: "requires a schedule"},
		{name: "bad schedule", jobs: map[string]CronJobConfig{"a": {Schedule: "61 * * * *", Command: "php run"}}, wantErr: "outside 0-59"},
		{name: "missing command", jobs: map[string]CronJobConfig{"a": {Schedule: "@hourly"}}, wantErr: "requires a command"},
		{name: "unbalanced command", jobs: map[string]CronJobConfig{"a": {Schedule: "@hourly", Command: `php "run`}}, wantErr: "unbalanced quote"},
		{name: "bad env name", jobs: map[string]CronJobConfig{"a": {Schedule: "@hourly", Command: "php run", Env: map[string]string{"A=B": "1"}}}, wantErr: "invalid environment variable name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateCronConfig(test.jobs)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateCronConfig() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

// TestCronConfigRoundTripsThroughEnvironmentFiles checks that cron jobs
// survive conversion between both file shapes and the environment model.
func TestCronConfigRoundTripsThroughEnvironmentFiles(t *testing.T) {
	jobs := map[string]CronJobConfig{"tick": {Schedule: "*/5 * * * *", Command: "php tick.php"}}

	environment := EnvironmentFileToEnvironment("blog", EnvironmentFile{Docroot: "public", Cron: jobs})
	if !reflect.DeepEqual(environment.Cron, jobs) {
		t.Fatalf("EnvironmentFileToEnvironment().Cron = %#v, want %#v", environment.Cron, jobs)
	}
	if got := EnvironmentFileFromEnvironment(environment).Cron; !reflect.DeepEqual(got, jobs) {
		t.Fatalf("EnvironmentFileFromEnvironment().Cron = %#v, want %#v", got, jobs)
	}

	project := ProjectFileToEnvironment("default", ProjectFile{Docroot: "public", Cron: jobs})
	if got := ProjectFileFromEnvironment(CurrentManifestVersion, ".polka", project).Cron; !reflect.DeepEqual(got, jobs) {
		t.Fatalf("ProjectFileFromEnvironment().Cron = %#v, want %#v", got, jobs)
	}
	if got := NormalizeEnvironment("default", project).Cron; !reflect.DeepEqual(got, jobs) {
		t.Fatalf("NormalizeEnvironment().Cron = %#v, want %#v", got, jobs)
	}
}
