package config

import (
	"reflect"
	"testing"
)

// TestPECLExtensionsYAMLRoundTrip verifies ordinary legacy PECL packages stay
// scalar while packages with reproducible build answers use an object.
func TestPECLExtensionsYAMLRoundTrip(t *testing.T) {
	raw := map[string]any{
		"Redis": "6.2.0",
		"imagick": map[string]any{
			"version": "3.8.0",
			"configure-options": map[string]any{
				"--with-imagick": "/opt/imagemagick",
			},
		},
	}
	extensions := PECLExtensionsFromYAML(raw)
	want := map[string]PECLExtensionConfig{
		"redis":   {Version: "6.2.0"},
		"imagick": {Version: "3.8.0", ConfigureOptions: map[string]string{"with-imagick": "/opt/imagemagick"}},
	}
	if !reflect.DeepEqual(extensions, want) {
		t.Fatalf("PECLExtensionsFromYAML() = %#v, want %#v", extensions, want)
	}
	roundTrip := PECLExtensionsFileValue(extensions)
	if roundTrip["redis"] != "6.2.0" {
		t.Fatalf("scalar PECL value = %#v, want 6.2.0", roundTrip["redis"])
	}
	structured, ok := roundTrip["imagick"].(map[string]any)
	if !ok || structured["version"] != "3.8.0" {
		t.Fatalf("structured PECL value = %#v", roundTrip["imagick"])
	}

	environment := ProjectFileToEnvironment("default", ProjectFile{PECLExtensions: raw})
	file := ProjectFileFromEnvironment(1, ".polka", environment)
	if !reflect.DeepEqual(PECLExtensionsFromYAML(file.PECLExtensions), want) {
		t.Fatalf("project file round trip = %#v, want %#v", file.PECLExtensions, want)
	}
}

// TestValidatePECLExtensionSpec covers bare package names and exact versions.
func TestValidatePECLExtensionSpec(t *testing.T) {
	for _, name := range []string{"redis", "mongodb", "open_telemetry"} {
		if err := ValidatePECLExtensionPackage(name); err != nil {
			t.Fatalf("ValidatePECLExtensionPackage(%q) error = %v", name, err)
		}
	}
	for _, name := range []string{"", "vendor/name", "bad name"} {
		if err := ValidatePECLExtensionPackage(name); err == nil {
			t.Fatalf("ValidatePECLExtensionPackage(%q) error = nil", name)
		}
	}
	for _, version := range []string{"*", "6.2.0", "1.0.0RC1"} {
		if err := ValidatePECLExtensionVersion("redis", version); err != nil {
			t.Fatalf("ValidatePECLExtensionVersion(%q) error = %v", version, err)
		}
	}
	for _, version := range []string{"", "^6.0", ">=1", "1 2"} {
		if err := ValidatePECLExtensionVersion("redis", version); err == nil {
			t.Fatalf("ValidatePECLExtensionVersion(%q) error = nil", version)
		}
	}
}
