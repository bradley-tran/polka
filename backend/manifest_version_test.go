package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLegacyProjectManifestCannotBeBypassed checks direct store access as well
// as explicit-root discovery, without relying on CLI initialization to validate.
func TestLegacyProjectManifestCannotBeBypassed(t *testing.T) {
	for _, header := range []string{"version: 0.1\n", "version: 1\n", ""} {
		t.Run(header, func(t *testing.T) {
			store := NewProjectStore(t.TempDir())
			original := header + "root: .polka\ntools:\n  php: '8.4'\n"
			if err := os.WriteFile(store.ConfigFile, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			named := "tools:\n  php: '8.4'\n"
			path := filepath.Join(store.ProjectDir, "polka.demo.yaml")
			if err := os.WriteFile(path, []byte(named), 0o644); err != nil {
				t.Fatal(err)
			}
			checks := []struct {
				name string
				run  func() error
			}{
				{"default", func() error { _, _, err := store.readEnvironment("default"); return err }},
				{"named", func() error { _, _, err := store.readEnvironment("demo"); return err }},
				{"explicit root", func() error { _, err := StoreForRoot(store.RootDir); return err }},
				{"named write", func() error { return store.writeEnvironmentConfig("demo", Environment{Name: "demo"}) }},
			}
			for _, check := range checks {
				err := check.run()
				if err == nil || !strings.Contains(err.Error(), "delete "+store.ConfigFile) || !strings.Contains(err.Error(), "polka init") {
					t.Fatalf("%s error = %v", check.name, err)
				}
			}
			for filename, expected := range map[string]string{store.ConfigFile: original, path: named} {
				data, err := os.ReadFile(filename)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != expected {
					t.Fatalf("rejected operation changed %s", filename)
				}
			}
			if err := os.Remove(store.ConfigFile); err != nil {
				t.Fatal(err)
			}
			if err := store.Init(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(store.ConfigFile)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(data), "version: 0.2\n") {
				t.Fatalf("init generated %q", data)
			}
			if _, _, err := store.readEnvironment("demo"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
