package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunLogsPrintsAllExistingNginxLogs(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeLogsTestConfig(t, projectDir)
	writeTestLogFile(t, filepath.Join(root, "run", "nginx", "demo", "logs", "access.log"), "access\n")
	writeTestLogFile(t, filepath.Join(root, "run", "nginx", "demo", "logs", "error.log"), "error\n")
	writeTestLogFile(t, filepath.Join(root, "run", "nginx", "demo", "serve.log"), "debug-serve\n")
	writeTestLogFile(t, filepath.Join(root, "run", "nginx", "demo", "php.log"), "debug-php\n")

	if code := Run(stdout, stderr, []string{"--root", root, "logs", "nginx"}); code != 0 {
		t.Fatalf("Run(logs nginx) code = %d, stderr = %q", code, stderr.String())
	}
	want := "access\nerror\ndebug-serve\ndebug-php\n"
	if stdout.String() != want {
		t.Fatalf("Run(logs nginx) stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunLogsFiltersByLevel(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeLogsTestConfig(t, projectDir)
	writeTestLogFile(t, filepath.Join(root, "run", "nginx", "demo", "logs", "access.log"), "access\n")
	writeTestLogFile(t, filepath.Join(root, "run", "nginx", "demo", "logs", "error.log"), "error\n")

	if code := Run(stdout, stderr, []string{"--root", root, "logs", "nginx", "--level", "error"}); code != 0 {
		t.Fatalf("Run(logs nginx --level error) code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "error\n" {
		t.Fatalf("Run(logs nginx --level error) stdout = %q, want error log only", stdout.String())
	}
}

func TestRunLogsPrintsFrankenPHPServeLog(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {FrankenPHP: "1.12"},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")
	writeTestLogFile(t, filepath.Join(root, "run", "frankenphp", "demo", "serve.log"), "frankenphp-debug\n")

	if code := Run(stdout, stderr, []string{"--root", root, "logs", "frankenphp"}); code != 0 {
		t.Fatalf("Run(logs frankenphp) code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "frankenphp-debug\n" {
		t.Fatalf("Run(logs frankenphp) stdout = %q, want serve log", stdout.String())
	}
}

func TestRunLogsErrorsWhenNoMatchingFilesExist(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeLogsTestConfig(t, projectDir)

	if code := Run(stdout, stderr, []string{"--root", root, "logs", "nginx"}); code == 0 {
		t.Fatal("Run(logs nginx) code = 0, want error")
	}
	if !strings.Contains(stderr.String(), "no log files found for nginx") {
		t.Fatalf("Run(logs nginx) stderr = %q, want missing logs error", stderr.String())
	}
}

func TestRunLogsRejectsInvalidLevel(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeLogsTestConfig(t, projectDir)

	if code := Run(stdout, stderr, []string{"--root", root, "logs", "nginx", "--level", "trace"}); code == 0 {
		t.Fatal("Run(logs nginx --level trace) code = 0, want error")
	}
	if !strings.Contains(stderr.String(), "invalid log level") {
		t.Fatalf("Run(logs nginx --level trace) stderr = %q, want invalid level error", stderr.String())
	}
}

func writeLogsTestConfig(t *testing.T, projectDir string) {
	t.Helper()

	root := filepath.Join(projectDir, ".polka")
	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {Nginx: "1.30"},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")
}

func writeTestLogFile(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}
