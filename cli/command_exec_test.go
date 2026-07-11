package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")

	if code := Run(stdout, stderr, []string{"--root", root, "exec", "php", "-v"}); code != 0 {
		t.Fatalf("Run(exec php) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "fake-php -v config") {
		t.Fatalf("Run(exec php) stdout = %q, want forwarded command with shell environment", stdout.String())
	}
}

// TestRunExecComposerRunsExtensionlessPHPScriptOnWindows covers the complete
// cmd.exe path used by a legacy Composer script such as bin/console.
func TestRunExecComposerRunsExtensionlessPHPScriptOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows command resolution behavior")
	}

	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	binDir := filepath.Join(projectDir, "bin")
	systemBinDir := filepath.Join(projectDir, "system-bin")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	chdirTest(t, projectDir)
	t.Setenv("PATH", systemBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, dir := range []string{filepath.Join(root, "bin"), binDir, systemBinDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "php.cmd"), fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(root php) error = %v", err)
	}
	consolePath := filepath.Join(binDir, "console")
	if err := os.WriteFile(consolePath, []byte("#!/usr/bin/env php\n<?php\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(bin/console) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "composer.json"), []byte(`{"scripts":{"post-cmd":"bin/console cache:clear"}}`), 0o644); err != nil {
		t.Fatalf("WriteFile(composer.json) error = %v", err)
	}
	composerScript := []byte("@echo off\r\ncall bin\\console cache:clear\r\nexit /b %ERRORLEVEL%\r\n")
	if err := os.WriteFile(filepath.Join(systemBinDir, "composer.cmd"), composerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(composer.cmd) error = %v", err)
	}
	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")

	if code := Run(stdout, stderr, []string{"--root", root, "exec", "composer", "run-script", "post-cmd"}); code != 0 {
		t.Fatalf("Run(exec composer) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "fake-php") || !strings.Contains(stdout.String(), "cache:clear") {
		t.Fatalf("Run(exec composer) stdout = %q, want Composer script run through managed PHP", stdout.String())
	}
	if _, err := os.Stat(consolePath + ".cmd"); !os.IsNotExist(err) {
		t.Fatalf("Stat(bin/console.cmd) error = %v, want temporary wrapper removed", err)
	}
}

func TestRunExecUsesNearestNestedVendorBin(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	nestedProjectDir := filepath.Join(projectDir, "drupal")
	nestedVendorBinDir := filepath.Join(nestedProjectDir, "vendor", "bin")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {},
		},
	})
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

func TestRunExecRunsPostComposerHookForCakePHPCreateProject(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	appRoot := filepath.Join(projectDir, "cake")
	systemBinDir := filepath.Join(projectDir, "system-bin")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	t.Setenv("POLKA_TEST_CREATE_PROJECT_DIR", appRoot)
	t.Setenv("PATH", systemBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	chdirTest(t, projectDir)

	if err := os.MkdirAll(systemBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(system-bin) error = %v", err)
	}
	composerPath := filepath.Join(systemBinDir, "composer")
	if runtime.GOOS == "windows" {
		composerPath += ".cmd"
	}
	if err := os.WriteFile(composerPath, fakeCakePHPCreateProjectComposerScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(composer) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			defaultEnvironmentName: {
				Framework: "cakephp",
				MariaDB:   "11.8",
				Docroot:   "cake/webroot",
				Database:  &testDatabaseConfig{Engine: "mariadb", Version: "11.8", Port: 3307},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)

	if code := Run(stdout, stderr, []string{"--root", root, "exec", "composer", "create-project", "cakephp/app", "cake"}); code != 0 {
		t.Fatalf("Run(exec composer create-project) code = %d, stderr = %q", code, stderr.String())
	}

	assertCakePHPAppLocalUsesManagedDatabase(t, filepath.Join(appRoot, "config", "app_local.php"), "3307")
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
