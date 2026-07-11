package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakePIEHostPHPScript builds a fake php runtime for PIE tests: -nm prints
// builtinModules, -m prints loadedModules, and any other invocation (the PIE
// PHAR run) echoes its arguments followed by extraLine so version parsing can
// be exercised. Every invocation appends its arguments to the file named by
// POLKA_TEST_PHP_CAPTURE_PATH when set.
func fakePIEHostPHPScript(builtinModules, loadedModules []string, extraLine string) []byte {
	if runtime.GOOS == "windows" {
		lines := []string{
			"@echo off",
			"if not \"%POLKA_TEST_PHP_CAPTURE_PATH%\"==\"\" echo %* >> \"%POLKA_TEST_PHP_CAPTURE_PATH%\"",
			"if \"%1\"==\"-nm\" goto builtin",
			"if \"%1\"==\"-m\" goto loaded",
			"echo fake-php %*",
		}
		if extraLine != "" {
			lines = append(lines, "echo "+extraLine)
		}
		lines = append(lines, "exit /b 0", ":builtin")
		for _, module := range builtinModules {
			lines = append(lines, "echo "+module)
		}
		lines = append(lines, "exit /b 0", ":loaded")
		for _, module := range loadedModules {
			lines = append(lines, "echo "+module)
		}
		lines = append(lines, "exit /b 0", "")
		return []byte(strings.Join(lines, "\r\n"))
	}

	script := []string{
		"#!/usr/bin/env sh",
		"if [ -n \"${POLKA_TEST_PHP_CAPTURE_PATH:-}\" ]; then printf '%s\\n' \"$*\" >> \"$POLKA_TEST_PHP_CAPTURE_PATH\"; fi",
		"if [ \"$1\" = \"-nm\" ]; then",
	}
	for _, module := range builtinModules {
		script = append(script, "  printf '"+module+"\\n'")
	}
	script = append(script, "  exit 0", "fi", "if [ \"$1\" = \"-m\" ]; then")
	for _, module := range loadedModules {
		script = append(script, "  printf '"+module+"\\n'")
	}
	script = append(script, "  exit 0", "fi", "printf 'fake-php %s\\n' \"$*\"")
	if extraLine != "" {
		script = append(script, "printf '"+extraLine+"\\n'")
	}
	script = append(script, "exit 0", "")
	return []byte(strings.Join(script, "\n"))
}

// setupExtTestProject seeds the cache with a fake php (project and internal
// host) plus a fake internal pie, writes a demo environment on php 8.4, and
// installs it. It returns the project dir, root, and capture file path.
func setupExtTestProject(t *testing.T, phpScript []byte) (projectDir, root, capturePath string) {
	t.Helper()

	projectDir = t.TempDir()
	root = filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	capturePath = filepath.Join(projectDir, "php-invocations.log")
	t.Setenv("POLKA_TEST_PHP_CAPTURE_PATH", capturePath)

	writeCachedPHP(t, cacheDir, "8.4", phpScript)
	writeCachedPIE(t, cacheDir, "1", []byte("pie phar\n"))

	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {PHP: "8.4"},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	return projectDir, root, capturePath
}

// TestRunExtInstallRecordsExplicitVersion verifies ext install drives the
// internal PIE against the project PHP, records the explicit constraint under
// php-extensions, and regenerates the runtime php.ini.
func TestRunExtInstallRecordsExplicitVersion(t *testing.T) {
	projectDir, root, capturePath := setupExtTestProject(t, fakePIEHostPHPScript([]string{"mbstring"}, []string{"mbstring"}, ""))
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "ext", "install", "xdebug/xdebug:3.4.1", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(ext install) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Installed xdebug/xdebug 3.4.1 for 'demo' environment") {
		t.Fatalf("Run(ext install) stdout = %q, want install summary", stdout.String())
	}

	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("ReadFile(capture) error = %v", err)
	}
	invocations := string(captured)
	if !strings.Contains(invocations, "pie.phar") || !strings.Contains(invocations, "install") {
		t.Fatalf("php invocations = %q, want pie.phar install call", invocations)
	}
	if !strings.Contains(invocations, "--with-php-path=") || !strings.Contains(invocations, "--skip-enable-extension") {
		t.Fatalf("php invocations = %q, want --with-php-path and --skip-enable-extension", invocations)
	}
	if !strings.Contains(invocations, "xdebug/xdebug:3.4.1") {
		t.Fatalf("php invocations = %q, want versioned package spec", invocations)
	}

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	if got := environment.PHPExtensions["xdebug/xdebug"]; got != "3.4.1" {
		t.Fatalf("php-extensions = %#v, want xdebug/xdebug: 3.4.1", environment.PHPExtensions)
	}

	// Xdebug is a Zend extension and must load via zend_extension.
	phpIni, err := os.ReadFile(projectInstalledPHPConfigPath(root, "8.4"))
	if err != nil {
		t.Fatalf("ReadFile(php.ini) error = %v", err)
	}
	if !strings.Contains(string(phpIni), "zend_extension=xdebug") {
		t.Fatalf("php.ini = %q, want zend_extension=xdebug", string(phpIni))
	}
}

// TestRunExtInstallParsesVersionFromPIEOutput verifies the recorded version
// comes from PIE's install output when no constraint is given.
func TestRunExtInstallParsesVersionFromPIEOutput(t *testing.T) {
	projectDir, root, _ := setupExtTestProject(t, fakePIEHostPHPScript([]string{"mbstring"}, []string{"mbstring"}, "Installed xdebug/xdebug:3.4.2"))
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "ext", "install", "xdebug/xdebug", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(ext install) code = %d, stderr = %q", code, stderr.String())
	}

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	if got := environment.PHPExtensions["xdebug/xdebug"]; got != "3.4.2" {
		t.Fatalf("php-extensions = %#v, want parsed version 3.4.2", environment.PHPExtensions)
	}
}

// TestRunExtInstallFallsBackToWildcardVersion verifies an unparseable PIE
// output records * with a pinning hint.
func TestRunExtInstallFallsBackToWildcardVersion(t *testing.T) {
	projectDir, root, _ := setupExtTestProject(t, fakePIEHostPHPScript([]string{"mbstring"}, []string{"mbstring"}, ""))
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "ext", "install", "acme/demo", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(ext install) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "recording * (latest)") {
		t.Fatalf("Run(ext install) stderr = %q, want wildcard hint", stderr.String())
	}

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	if got := environment.PHPExtensions["acme/demo"]; got != "*" {
		t.Fatalf("php-extensions = %#v, want acme/demo: *", environment.PHPExtensions)
	}
}

// TestRunExtRemoveDeletesEntry verifies ext remove uninstalls through PIE,
// deletes the config entry, and drops the module from php.ini.
func TestRunExtRemoveDeletesEntry(t *testing.T) {
	projectDir, root, capturePath := setupExtTestProject(t, fakePIEHostPHPScript([]string{"mbstring"}, []string{"mbstring"}, ""))
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "ext", "install", "xdebug/xdebug:3.4.1", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(ext install) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "ext", "remove", "xdebug/xdebug", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(ext remove) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Removed xdebug/xdebug from 'demo' environment") {
		t.Fatalf("Run(ext remove) stdout = %q, want removal summary", stdout.String())
	}

	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("ReadFile(capture) error = %v", err)
	}
	if !strings.Contains(string(captured), "uninstall") {
		t.Fatalf("php invocations = %q, want pie uninstall call", string(captured))
	}

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	if _, exists := environment.PHPExtensions["xdebug/xdebug"]; exists {
		t.Fatalf("php-extensions = %#v, want xdebug/xdebug removed", environment.PHPExtensions)
	}

	phpIni, err := os.ReadFile(projectInstalledPHPConfigPath(root, "8.4"))
	if err != nil {
		t.Fatalf("ReadFile(php.ini) error = %v", err)
	}
	if strings.Contains(string(phpIni), "extension=xdebug") {
		t.Fatalf("php.ini = %q, want xdebug removed", string(phpIni))
	}
}

// TestRunExtRequiresStandalonePHP verifies ext install rejects environments
// without a standalone php/php-zts runtime.
func TestRunExtRequiresStandalonePHP(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	t.Setenv("POLKA_CACHE_DIR", filepath.Join(projectDir, "global-cache"))
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

	if code := Run(stdout, stderr, []string{"--root", root, "ext", "install", "xdebug/xdebug", "--env", "demo"}); code == 0 {
		t.Fatal("Run(ext install) code = 0, want failure for FrankenPHP-only environment")
	}
	if !strings.Contains(stderr.String(), "standalone php") {
		t.Fatalf("Run(ext install) stderr = %q, want standalone php requirement", stderr.String())
	}
}

// TestRunExtRejectsInvalidSpecs verifies package and action validation.
func TestRunExtRejectsInvalidSpecs(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	t.Setenv("POLKA_CACHE_DIR", filepath.Join(projectDir, "global-cache"))

	for _, args := range [][]string{
		{"ext", "install", "xdebug"},
		{"ext", "install", "xdebug/xdebug:"},
		{"ext", "frobnicate", "xdebug/xdebug"},
		{"ext", "remove", "xdebug/xdebug:3.4.1"},
	} {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		if code := Run(stdout, stderr, append([]string{"--root", root}, args...)); code == 0 {
			t.Fatalf("Run(%v) code = 0, want validation failure", args)
		}
	}
}

// TestParseExtSpecRoutesProviders verifies slash package names use PIE while
// bare legacy package names use PECL.
func TestParseExtSpecRoutesProviders(t *testing.T) {
	provider, pkg, version, err := parseExtSpec("xdebug/xdebug:^3.4")
	if err != nil || provider != extProviderPIE || pkg != "xdebug/xdebug" || version != "^3.4" {
		t.Fatalf("parseExtSpec(PIE) = %v, %q, %q, %v", provider, pkg, version, err)
	}
	provider, pkg, version, err = parseExtSpec("redis:6.2.0")
	if err != nil || provider != extProviderPECL || pkg != "redis" || version != "6.2.0" {
		t.Fatalf("parseExtSpec(PECL) = %v, %q, %q, %v", provider, pkg, version, err)
	}
}

// TestParsePECLConfigureOptions validates repeatable NAME=VALUE flags.
func TestParsePECLConfigureOptions(t *testing.T) {
	options, err := parsePECLConfigureOptions([]string{"--with-imagick=/opt/imagemagick", "enable-foo=no"})
	if err != nil || options["with-imagick"] != "/opt/imagemagick" || options["enable-foo"] != "no" {
		t.Fatalf("parsePECLConfigureOptions() = %#v, %v", options, err)
	}
	if _, err := parsePECLConfigureOptions([]string{"broken"}); err == nil {
		t.Fatal("parsePECLConfigureOptions(broken) error = nil")
	}
}
