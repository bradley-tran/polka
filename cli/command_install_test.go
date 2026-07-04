package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInstallSecondRunReportsUnchanged(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())
	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "(unchanged)") {
		t.Fatalf("Run(install) stdout = %q, want no unchanged marker on first install", stdout.String())
	}

	sentinel := filepath.Join(root, "envs", "php", "8.4", "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(sentinel) error = %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install second) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "php 8.4\t(unchanged)") {
		t.Fatalf("Run(install second) stdout = %q, want unchanged marker", stdout.String())
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("Stat(sentinel) error = %v, want install dir untouched", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo", "--force"}); code != 0 {
		t.Fatalf("Run(install --force) code = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "(unchanged)") {
		t.Fatalf("Run(install --force) stdout = %q, want no unchanged marker", stdout.String())
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("Stat(sentinel) error = %v, want sentinel removed by forced reinstall", err)
	}
}

// TestRunInstallProvisionsPIEExtensions verifies polka install reproduces
// vendor/name php-extensions entries via the internal PIE when the module is
// not yet loadable, and skips them when PHP already loads the module.
func TestRunInstallProvisionsPIEExtensions(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	capturePath := filepath.Join(projectDir, "php-invocations.log")
	t.Setenv("POLKA_TEST_PHP_CAPTURE_PATH", capturePath)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	// The fake PHP loads mbstring but not xdebug, so the xdebug/xdebug entry
	// must trigger a PIE install.
	writeCachedPHP(t, cacheDir, "8.4", fakePIEHostPHPScript([]string{"mbstring"}, []string{"mbstring"}, ""))
	writeCachedPIE(t, cacheDir, "1", []byte("pie phar\n"))

	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:           "8.4",
				PHPExtensions: map[string]any{"mbstring": true, "xdebug/xdebug": "3.4.1"},
			},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")

	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("ReadFile(capture) error = %v", err)
	}
	invocations := string(captured)
	if !strings.Contains(invocations, "--skip-enable-extension") || !strings.Contains(invocations, "xdebug/xdebug:3.4.1") {
		t.Fatalf("php invocations = %q, want PIE install of xdebug/xdebug:3.4.1", invocations)
	}
	if count := strings.Count(invocations, "--skip-enable-extension"); count != 1 {
		t.Fatalf("php invocations = %q, want exactly one PIE install, got %d", invocations, count)
	}

	// The generated php.ini loads the PIE-managed module.
	phpIni, err := os.ReadFile(projectInstalledPHPConfigPath(root, "8.4"))
	if err != nil {
		t.Fatalf("ReadFile(php.ini) error = %v", err)
	}
	if !strings.Contains(string(phpIni), "extension=xdebug") {
		t.Fatalf("php.ini = %q, want extension=xdebug", string(phpIni))
	}
}

// TestRunInstallSkipsLoadedPIEExtensions verifies no PIE install runs when
// the environment's PHP already loads the module.
func TestRunInstallSkipsLoadedPIEExtensions(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	capturePath := filepath.Join(projectDir, "php-invocations.log")
	t.Setenv("POLKA_TEST_PHP_CAPTURE_PATH", capturePath)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePIEHostPHPScript([]string{"mbstring"}, []string{"mbstring", "xdebug"}, ""))
	writeCachedPIE(t, cacheDir, "1", []byte("pie phar\n"))

	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:           "8.4",
				PHPExtensions: map[string]any{"xdebug/xdebug": "3.4.1"},
			},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")

	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("ReadFile(capture) error = %v", err)
	}
	if strings.Contains(string(captured), "--skip-enable-extension") {
		t.Fatalf("php invocations = %q, want no PIE install for already-loaded module", string(captured))
	}
}

// TestRunInstallRejectsInternalOnlyTool verifies pie cannot be installed as
// an explicit tool:version argument.
func TestRunInstallRejectsInternalOnlyTool(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	t.Setenv("POLKA_CACHE_DIR", filepath.Join(projectDir, "global-cache"))
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")
	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "install", "pie:1", "--env", "demo"}); code == 0 {
		t.Fatal("Run(install pie:1) code = 0, want internal-only rejection")
	}
	if !strings.Contains(stderr.String(), "managed internally") {
		t.Fatalf("Run(install pie:1) stderr = %q, want managed-internally message", stderr.String())
	}
}

func TestRunInstallUnknownToolLeavesConfigUnchanged(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())
	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")

	configPath := filepath.Join(projectDir, "polka.yaml")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config before) error = %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "bogus:1.0", "--env", "demo"}); code == 0 {
		t.Fatal("Run(install bogus:1.0) code = 0, want unsupported tool failure")
	}
	if !strings.Contains(stderr.String(), "unsupported tool") {
		t.Fatalf("Run(install bogus:1.0) stderr = %q, want unsupported tool error", stderr.String())
	}

	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config after) error = %v", err)
	}
	if string(configAfter) != string(configBefore) {
		t.Fatalf("config after failed install = %q, want unchanged %q", string(configAfter), string(configBefore))
	}
}
