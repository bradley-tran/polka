package config

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// TestManifestVersionRoundTrip verifies both accepted YAML scalar styles are
// decoded textually and written back as the canonical plain 0.2 version.
func TestManifestVersionRoundTrip(t *testing.T) {
	for _, version := range []string{"0.2", `"0.2"`, "'0.2'"} {
		t.Run(version, func(t *testing.T) {
			data := []byte("version: " + version + "\nroot: .polka\n")
			if err := ValidateProjectManifestVersion(data, "polka.yaml"); err != nil {
				t.Fatal(err)
			}
			var file ProjectFile
			if err := yaml.Unmarshal(data, &file); err != nil {
				t.Fatal(err)
			}
			if file.Version != CurrentManifestVersion {
				t.Fatalf("version = %q", file.Version)
			}
			encoded, err := yaml.Marshal(file)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(encoded), "version: 0.2\n") {
				t.Fatalf("noncanonical version: %s", encoded)
			}
		})
	}
}

// TestManifestVersionRejectsObsoleteSchemas includes representations that would
// be lost by numeric conversion, as well as absent and structured versions.
func TestManifestVersionRejectsObsoleteSchemas(t *testing.T) {
	for _, data := range []string{"", "root: .polka", "version: 0.1", `version: "0.1"`, "version: 1", "version: 0.20", "version: 0.3", "version: null", "version: true", "version: [0.2]", "version: {major: 0, minor: 2}"} {
		t.Run(data, func(t *testing.T) {
			err := ValidateProjectManifestVersion([]byte(data), "project/polka.yaml")
			if err == nil || !strings.Contains(err.Error(), "project/polka.yaml") {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
	if _, err := yaml.Marshal(ProjectFile{Version: "0.1"}); err == nil {
		t.Fatal("writer accepted obsolete schema")
	}
}
