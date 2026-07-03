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
