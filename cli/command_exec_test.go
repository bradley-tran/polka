package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"polka/backend"
)

func TestRunExecRunsCommandWithShellEnvironment(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll(root/bin) error = %v", err)
	}
	phpPath := filepath.Join(root, "bin", "php")
	if runtime.GOOS == "windows" {
		phpPath += ".cmd"
	}
	if err := os.WriteFile(phpPath, fakePHPScriptWithEnv("APP_ENV"), 0o755); err != nil {
		t.Fatalf("WriteFile(root php) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte("APP_ENV=project\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(project .env) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, "config"), 0o755); err != nil {
		t.Fatalf("MkdirAll(config) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "config", ".env.local"), []byte("APP_ENV=file\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(env-file) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				EnvFile: "config/.env.local",
				EnvVars: map[string]string{"APP_ENV": "config"},
			},
		},
	}
	configData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	configData = append(configData, '\n')
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	writeTestActiveEnvironment(t, root, "demo")

	if code := Run(stdout, stderr, []string{"--root", root, "exec", "php", "-v"}); code != 0 {
		t.Fatalf("Run(exec php) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "fake-php -v config") {
		t.Fatalf("Run(exec php) stdout = %q, want forwarded command with shell environment", stdout.String())
	}
}

func TestRunExecUsesNearestNestedVendorBin(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	nestedProjectDir := filepath.Join(projectDir, "drupal")
	nestedVendorBinDir := filepath.Join(nestedProjectDir, "vendor", "bin")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	configData := []byte("version: 1\nroot: .polka\nenvironments:\n  demo: {}\n")
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	writeTestActiveEnvironment(t, root, "demo")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll(root/bin) error = %v", err)
	}
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(filepath.Join(root, "bin", "php.cmd"), fakePHPScript(), 0o755); err != nil {
			t.Fatalf("WriteFile(root php) error = %v", err)
		}
	} else {
		if err := os.WriteFile(filepath.Join(root, "bin", "php"), fakePHPScript(), 0o755); err != nil {
			t.Fatalf("WriteFile(root php) error = %v", err)
		}
	}
	if err := os.MkdirAll(nestedVendorBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(vendor/bin) error = %v", err)
	}
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(filepath.Join(nestedVendorBinDir, "drush"), []byte("#!/usr/bin/env sh\nexec \"$DRUSH_PHP\" \"$@\"\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(drush launcher) error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(nestedVendorBinDir, "drush.php"), fakePHPScript(), 0o755); err != nil {
			t.Fatalf("WriteFile(drush.php) error = %v", err)
		}
	} else {
		if err := os.WriteFile(filepath.Join(nestedVendorBinDir, "drush"), fakePHPScript(), 0o755); err != nil {
			t.Fatalf("WriteFile(drush) error = %v", err)
		}
	}

	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})
	if err := os.MkdirAll(nestedProjectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested project) error = %v", err)
	}
	if err := os.Chdir(nestedProjectDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "exec", "drush", "status"}); code != 0 {
		t.Fatalf("Run(exec drush) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "fake-php") || !strings.Contains(stdout.String(), "status") {
		t.Fatalf("Run(exec drush) stdout = %q, want nested vendor command output", stdout.String())
	}
}

func TestRunExecRequiresCommand(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	store := backend.NewStore(filepath.Join(t.TempDir(), ".polka"))

	if code := runExec(stdout, stderr, store, nil); code != 2 {
		t.Fatalf("runExec() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "exec requires a command") {
		t.Fatalf("runExec() stderr = %q, want missing command error", stderr.String())
	}
}

func TestResolveExecTargetUsesWindowsPathExt(t *testing.T) {
	projectDir := t.TempDir()
	shimDir := filepath.Join(projectDir, "vendor-bin")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(shimDir) error = %v", err)
	}
	targetPath := filepath.Join(shimDir, "drush.cmd")
	if err := os.WriteFile(targetPath, []byte("@echo off\r\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(drush.cmd) error = %v", err)
	}
	env := []string{"PATH=" + shimDir, "PATHEXT=.CMD;.EXE"}

	target, err := resolveExecTarget("windows", "drush", env)
	if err != nil {
		t.Fatalf("resolveExecTarget() error = %v", err)
	}
	if target != targetPath {
		t.Fatalf("resolveExecTarget() = %q, want %q", target, targetPath)
	}
}
