package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestSplitWorkerCommand covers argv splitting including quoted arguments and
// the empty/unbalanced error cases.
func TestSplitWorkerCommand(t *testing.T) {
	for _, test := range []struct {
		name    string
		command string
		want    []string
		wantErr string
	}{
		{name: "plain", command: "php artisan queue:work", want: []string{"php", "artisan", "queue:work"}},
		{name: "extra whitespace", command: "  php \t artisan   queue:work \n", want: []string{"php", "artisan", "queue:work"}},
		{name: "double quoted", command: `php artisan "queue:work --sleep 3"`, want: []string{"php", "artisan", "queue:work --sleep 3"}},
		{name: "single quoted", command: "php 'my script.php'", want: []string{"php", "my script.php"}},
		{name: "quote inside argument", command: `php --flag="a b"`, want: []string{"php", "--flag=a b"}},
		{name: "empty", command: "   ", wantErr: "must not be empty"},
		{name: "unbalanced quote", command: `php "artisan`, wantErr: "unbalanced quote"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := SplitWorkerCommand(test.command)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("SplitWorkerCommand(%q) error = %v, want %q", test.command, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SplitWorkerCommand(%q) error = %v", test.command, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("SplitWorkerCommand(%q) = %#v, want %#v", test.command, got, test.want)
			}
		})
	}
}

// TestNormalizeWorkersConfig covers name canonicalization, entry dropping, and
// the nil-when-empty contract that keeps the workers key omitted on save.
func TestNormalizeWorkersConfig(t *testing.T) {
	normalized := NormalizeWorkersConfig(map[string]WorkerConfig{
		" Queue ":  {Command: "  php artisan queue:work  ", Replicas: 2, Dir: " . ", Env: map[string]string{" TRIES ": "3"}},
		"empty":    {Command: "   "},
		"  ":       {Command: "php run"},
		"negative": {Command: "php run", Replicas: -3},
	})

	want := map[string]WorkerConfig{
		"queue":    {Command: "php artisan queue:work", Replicas: 2, Dir: ".", Env: map[string]string{"TRIES": "3"}},
		"negative": {Command: "php run", Replicas: 0},
	}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("NormalizeWorkersConfig() = %#v, want %#v", normalized, want)
	}

	if NormalizeWorkersConfig(nil) != nil {
		t.Fatal("NormalizeWorkersConfig(nil) != nil, want nil")
	}
	if NormalizeWorkersConfig(map[string]WorkerConfig{"gone": {Command: " "}}) != nil {
		t.Fatal("NormalizeWorkersConfig(all dropped) != nil, want nil")
	}
}

// TestValidateWorkersConfig covers the rejection cases for names, commands,
// and environment variable keys.
func TestValidateWorkersConfig(t *testing.T) {
	for _, test := range []struct {
		name    string
		workers map[string]WorkerConfig
		wantErr string
	}{
		{name: "valid", workers: map[string]WorkerConfig{"queue": {Command: "php artisan queue:work", Env: map[string]string{"TRIES": "3"}}}},
		{name: "empty", workers: nil},
		{name: "invalid name", workers: map[string]WorkerConfig{"bad name": {Command: "php run"}}, wantErr: "invalid worker name"},
		{name: "missing command", workers: map[string]WorkerConfig{"queue": {}}, wantErr: "requires a command"},
		{name: "unbalanced command", workers: map[string]WorkerConfig{"queue": {Command: `php "broken`}}, wantErr: "unbalanced quote"},
		{name: "invalid env key", workers: map[string]WorkerConfig{"queue": {Command: "php run", Env: map[string]string{"BAD=KEY": "1"}}}, wantErr: "invalid environment variable name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateWorkersConfig(test.workers)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateWorkersConfig() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateWorkersConfig() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

// TestEffectiveWorkerReplicas checks the default-of-one behavior.
func TestEffectiveWorkerReplicas(t *testing.T) {
	if got := EffectiveWorkerReplicas(WorkerConfig{}); got != 1 {
		t.Fatalf("EffectiveWorkerReplicas(unset) = %d, want 1", got)
	}
	if got := EffectiveWorkerReplicas(WorkerConfig{Replicas: 3}); got != 3 {
		t.Fatalf("EffectiveWorkerReplicas(3) = %d, want 3", got)
	}
}

// TestSortedWorkerNames checks the deterministic ordering helper.
func TestSortedWorkerNames(t *testing.T) {
	names := SortedWorkerNames(map[string]WorkerConfig{
		"scheduler": {Command: "b"},
		"queue":     {Command: "a"},
	})
	if !reflect.DeepEqual(names, []string{"queue", "scheduler"}) {
		t.Fatalf("SortedWorkerNames() = %#v, want sorted names", names)
	}
	if SortedWorkerNames(nil) != nil {
		t.Fatal("SortedWorkerNames(nil) != nil, want nil")
	}
}

// TestWorkersRoundTripThroughFileTypes checks that workers survive both the
// polka.yaml and polka.<name>.yaml conversion paths and NormalizeEnvironment.
func TestWorkersRoundTripThroughFileTypes(t *testing.T) {
	workers := map[string]WorkerConfig{
		"queue":     {Command: "php artisan queue:work", Replicas: 2, Dir: "app", Env: map[string]string{"TRIES": "3"}},
		"scheduler": {Command: "php artisan schedule:work"},
	}

	environment := ProjectFileToEnvironment("default", ProjectFile{Workers: workers})
	if !reflect.DeepEqual(environment.Workers, workers) {
		t.Fatalf("ProjectFileToEnvironment().Workers = %#v, want %#v", environment.Workers, workers)
	}
	projectFile := ProjectFileFromEnvironment(1, ".polka", environment)
	if !reflect.DeepEqual(projectFile.Workers, workers) {
		t.Fatalf("ProjectFileFromEnvironment().Workers = %#v, want %#v", projectFile.Workers, workers)
	}

	named := EnvironmentFileToEnvironment("data", EnvironmentFile{Workers: workers})
	if !reflect.DeepEqual(named.Workers, workers) {
		t.Fatalf("EnvironmentFileToEnvironment().Workers = %#v, want %#v", named.Workers, workers)
	}
	namedFile := EnvironmentFileFromEnvironment(named)
	if !reflect.DeepEqual(namedFile.Workers, workers) {
		t.Fatalf("EnvironmentFileFromEnvironment().Workers = %#v, want %#v", namedFile.Workers, workers)
	}

	normalized := NormalizeEnvironment("default", Environment{Workers: workers})
	if !reflect.DeepEqual(normalized.Workers, workers) {
		t.Fatalf("NormalizeEnvironment().Workers = %#v, want %#v", normalized.Workers, workers)
	}
}
