package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakePHPInRoot writes a fake php binary into <root>/bin so shortcut and
// exec tests can resolve "php" through the Polka shell PATH.
func writeFakePHPInRoot(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll(root/bin) error = %v", err)
	}
	phpPath := filepath.Join(root, "bin", "php")
	if runtime.GOOS == "windows" {
		phpPath += ".cmd"
	}
	if err := os.WriteFile(phpPath, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(root php) error = %v", err)
	}
}

// TestShortcutPHPExpandsToExec verifies that the "php" shortcut prepends "exec",
// running php through the same PATH resolution as "polka exec php".
func TestShortcutPHPExpandsToExec(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeTestConfigFile(t, projectDir, testConfigFile{
		Version:      1,
		Root:         ".polka",
		Environments: map[string]testEnvironmentConfig{"demo": {}},
	})
	writeTestActiveEnvironment(t, root, "demo")
	writeFakePHPInRoot(t, root)

	if code := Run(stdout, stderr, []string{"--root", root, "php", "-v"}); code != 0 {
		t.Fatalf("Run(php) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "fake-php") || !strings.Contains(stdout.String(), "-v") {
		t.Fatalf("Run(php) stdout = %q, want forwarded php command output", stdout.String())
	}
}

// TestShortcutHiddenNodeExpandsToExec verifies that a hidden tool shortcut still
// expands to "exec" rather than being rejected as an unknown command. A fake node
// binary in <root>/bin takes precedence on the shell PATH, so its output proves the
// shortcut expanded and ran through exec.
func TestShortcutHiddenNodeExpandsToExec(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeTestConfigFile(t, projectDir, testConfigFile{
		Version:      1,
		Root:         ".polka",
		Environments: map[string]testEnvironmentConfig{"demo": {}},
	})
	writeTestActiveEnvironment(t, root, "demo")
	nodePath := filepath.Join(root, "bin", "node")
	if runtime.GOOS == "windows" {
		nodePath += ".cmd"
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll(root/bin) error = %v", err)
	}
	if err := os.WriteFile(nodePath, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(root node) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "node", "polka-shortcut-marker"}); code != 0 {
		t.Fatalf("Run(node) code = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("Run(node) stderr = %q, want exec expansion, not unknown command", stderr.String())
	}
	if !strings.Contains(stdout.String(), "polka-shortcut-marker") {
		t.Fatalf("Run(node) stdout = %q, want forwarded node command output", stdout.String())
	}
}

// TestShortcutEnvInstallMatchesTopLevelInstall verifies that "env install" and the
// top-level "install" shortcut produce the same result for the current environment.
func TestShortcutEnvInstallMatchesTopLevelInstall(t *testing.T) {
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
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "env", "install"}); code != 0 {
		t.Fatalf("Run(env install) code = %d, stderr = %q", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "Installing demo environment") {
		t.Fatalf("Run(env install) stdout = %q, want current environment banner", output)
	}
	if !strings.Contains(output, "Installed 'demo' environment") {
		t.Fatalf("Run(env install) stdout = %q, want install summary", output)
	}
}

// TestShortcutRemoveExpandsToEnvRemove verifies that the top-level "remove" shortcut
// reaches the env remove command by asserting its argument validation fires. This
// path stops at validation before a store is built, so it exercises the wiring
// without provisioning tools.
func TestShortcutRemoveExpandsToEnvRemove(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"remove"}); code != 1 {
		t.Fatalf("Run(remove) code = %d, want 1", code)
	}
	if strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("Run(remove) stderr = %q, want env remove expansion, not unknown command", stderr.String())
	}
	if !strings.Contains(stderr.String(), "remove requires exactly one environment name") {
		t.Fatalf("Run(remove) stderr = %q, want env remove argument validation", stderr.String())
	}
}

// TestEnvCommandWithoutSubcommandPrintsUsage verifies that "polka env" alone prints
// the env usage text and that an unknown env subcommand fails with exit code 2.
func TestEnvCommandWithoutSubcommandPrintsUsage(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := Run(stdout, stderr, []string{"env"}); code != 0 {
		t.Fatalf("Run(env) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "polka env <command>") {
		t.Fatalf("Run(env) stdout = %q, want env usage", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"env", "bogus"}); code != 2 {
		t.Fatalf("Run(env bogus) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("Run(env bogus) stderr = %q, want unknown command error", stderr.String())
	}
}
