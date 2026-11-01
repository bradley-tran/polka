package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDrupalPostComposerUpdatesReadOnlySettings reproduces Drupal's protected
// settings.php on Windows, where replacing a read-only file fails at rename.
func TestDrupalPostComposerUpdatesReadOnlySettings(t *testing.T) {
	plugin := newManifestFrameworkPlugin(Drupal)
	dir := t.TempDir()
	settings := filepath.Join(dir, "web", "sites", "default", "settings.php")
	writeReadOnlyFrameworkTarget(t, settings, "<?php\n$settings['hash_salt'] = 'keep';\n")
	ctx := PostComposerContext{
		ProjectDir:  dir,
		Environment: plugin.Defaults(),
		Database: &DatabaseCredentials{
			Engine: "mariadb", DatabaseName: "demo", User: "polka", Password: "secret",
		},
	}
	for i := 0; i < 2; i++ {
		if err := plugin.PostComposer(ctx); err != nil {
			t.Fatalf("PostComposer() run %d: %v", i, err)
		}
		data := string(readPluginTestFile(t, settings))
		if !strings.Contains(data, "$settings['hash_salt'] = 'keep';") {
			t.Fatal("update changed unrelated Drupal settings")
		}
		if strings.Count(data, "// <polka:settings-polka>") != 1 {
			t.Fatalf("expected one managed include block: %s", data)
		}
		assertFrameworkTargetReadOnly(t, settings)
	}
	staged, err := filepath.Glob(filepath.Join(filepath.Dir(settings), ".polka-config-*"))
	if err != nil || len(staged) != 0 {
		t.Fatalf("staged files remain: %v, %v", staged, err)
	}
}

// TestFrameworkReplaceFailureRestoresReadOnly verifies a failed rename leaves
// the original bytes and read-only protection in place, without deleting it.
func TestFrameworkReplaceFailureRestoresReadOnly(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "settings.php")
	writeReadOnlyFrameworkTarget(t, target, "original settings")
	err := replaceFrameworkFile(filepath.Join(dir, "missing-stage"), target)
	if err == nil || !strings.Contains(err.Error(), "rename") {
		t.Fatalf("replaceFrameworkFile() error = %v, want rename failure", err)
	}
	if data := string(readPluginTestFile(t, target)); data != "original settings" {
		t.Fatalf("failed replacement changed original: %q", data)
	}
	assertFrameworkTargetReadOnly(t, target)
}

// writeReadOnlyFrameworkTarget creates a protected fixture and makes it
// writable during cleanup so Windows can remove the test's temporary directory.
func writeReadOnlyFrameworkTarget(t *testing.T, filename, contents string) {
	t.Helper()
	writePluginTestFile(t, filename, contents)
	t.Cleanup(func() {
		if err := os.Chmod(filename, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("restore writable test fixture: %v", err)
		}
	})
	if err := os.Chmod(filename, 0o444); err != nil {
		t.Fatal(err)
	}
	assertFrameworkTargetReadOnly(t, filename)
}

// assertFrameworkTargetReadOnly checks the write bit used by Go to represent
// the Windows read-only attribute, independently of other permission bits.
func assertFrameworkTargetReadOnly(t *testing.T, filename string) {
	t.Helper()
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o200 != 0 {
		t.Fatalf("%s lost its read-only attribute", filename)
	}
}
