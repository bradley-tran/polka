package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for selecting the project with -C/--dir and for following the state
// directory configured by polka.yaml's root key.

// requirePathMissing fails when path exists.
func requirePathMissing(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(%s) error = %v, want not exist", path, err)
	}
}

// TestRunDirFlagSelectsProjectOutsideWorkingDirectory verifies both flag
// spellings open the named project and leave the working directory untouched.
func TestRunDirFlagSelectsProjectOutsideWorkingDirectory(t *testing.T) {
	projectDir := t.TempDir()
	callerDir := t.TempDir()
	chdirTest(t, callerDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"-C", projectDir, "init"}); code != 0 {
		t.Fatalf("Run(-C init) code = %d, stderr = %q", code, stderr.String())
	}
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "config", "tools.php", "8.4"}); code != 0 {
		t.Fatalf("Run(--dir config) code = %d, stderr = %q", code, stderr.String())
	}

	if environment := readTestEnvironmentConfig(t, projectDir, defaultEnvironmentName); environment.PHP != "8.4" {
		t.Fatalf("environment = %#v, want php 8.4 in the selected project", environment)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin")); err != nil {
		t.Fatalf("Stat(project .polka/bin) error = %v", err)
	}
	requirePathMissing(t, filepath.Join(callerDir, "polka.yaml"))
	requirePathMissing(t, filepath.Join(callerDir, ".polka"))
}

// TestRunDirFlagRejectsMissingDirectory verifies a mistyped project path fails
// instead of creating a new project.
func TestRunDirFlagRejectsMissingDirectory(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "missing")

	for _, command := range []string{"status", "init"} {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		if code := Run(stdout, stderr, []string{"--dir", missingDir, command}); code != 1 {
			t.Fatalf("Run(--dir missing %s) code = %d, want 1", command, code)
		}
		if !strings.Contains(stderr.String(), "does not exist") {
			t.Fatalf("Run(--dir missing %s) stderr = %q, want missing directory error", command, stderr.String())
		}
	}
	requirePathMissing(t, missingDir)
}

// TestRunRejectsRemovedRootFlag verifies --root is no longer accepted now that
// the state directory comes from polka.yaml.
func TestRunRejectsRemovedRootFlag(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", t.TempDir(), "status"}); code == 0 {
		t.Fatal("Run(--root status) code = 0, want failure")
	}
	if !strings.Contains(stderr.String(), "unknown flag: --root") {
		t.Fatalf("Run(--root status) stderr = %q, want unknown flag error", stderr.String())
	}
}

// TestRunInitFollowsExistingConfigRoot verifies init creates the state
// directory and shims where an existing polka.yaml's root key points.
func TestRunInitFollowsExistingConfigRoot(t *testing.T) {
	projectDir := t.TempDir()
	writeTestConfigFile(t, projectDir, testConfigFile{Root: "var/polka"})
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"-C", projectDir, "init"}); code != 0 {
		t.Fatalf("Run(init) code = %d, stderr = %q", code, stderr.String())
	}

	root := filepath.Join(projectDir, "var", "polka")
	if !strings.Contains(stdout.String(), "Initialized Polka at "+root) {
		t.Fatalf("Run(init) stdout = %q, want configured root %q", stdout.String(), root)
	}
	if _, err := os.Stat(filepath.Join(root, "bin", "polka")); err != nil {
		t.Fatalf("Stat(configured root dispatcher) error = %v", err)
	}
	requirePathMissing(t, filepath.Join(projectDir, ".polka"))
}
