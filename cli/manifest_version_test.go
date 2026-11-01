package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLIRejectsLegacyManifest verifies both discovered and explicit roots,
// including named environments, and preserves user files on every failure.
func TestCLIRejectsLegacyManifest(t *testing.T) {
	dir := t.TempDir()
	chdirTest(t, dir)
	path := filepath.Join(dir, "polka.yaml")
	data := []byte("version: 0.1\nroot: .polka\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "polka.demo.yaml"), []byte("tools:\n  php: '8.4'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"status"}, {"--root", filepath.Join(dir, ".polka"), "status"}, {"--root", filepath.Join(dir, ".polka"), "config", "--env", "demo", "tools.php", "8.5"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(&stdout, &stderr, args); code == 0 {
			t.Fatalf("%v accepted obsolete config", args)
		}
		if !strings.Contains(stderr.String(), "polka init") || !strings.Contains(stderr.String(), path) {
			t.Fatalf("unhelpful error: %s", &stderr)
		}
	}
	unchanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, unchanged) {
		t.Fatal("CLI rewrote legacy manifest")
	}
}
