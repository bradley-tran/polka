package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestSplitPHPExtensionsFromYAML verifies bundled toggles and PIE version
// entries separate correctly, including scalar coercion for YAML numbers and
// nil values.
func TestSplitPHPExtensionsFromYAML(t *testing.T) {
	bundled, pie := SplitPHPExtensionsFromYAML(map[string]any{
		"MBString":      true,
		"apcu":          false,
		"Xdebug/Xdebug": "3.4.1",
		"acme/float":    1.2,
		"acme/int":      3,
		"acme/latest":   nil,
	})

	wantBundled := map[string]bool{"mbstring": true, "apcu": false}
	if !reflect.DeepEqual(bundled, wantBundled) {
		t.Fatalf("bundled = %#v, want %#v", bundled, wantBundled)
	}
	wantPIE := map[string]string{
		"xdebug/xdebug": "3.4.1",
		"acme/float":    "1.2",
		"acme/int":      "3",
		"acme/latest":   "*",
	}
	if !reflect.DeepEqual(pie, wantPIE) {
		t.Fatalf("pie = %#v, want %#v", pie, wantPIE)
	}

	if bundled, pie := SplitPHPExtensionsFromYAML(nil); bundled != nil || pie != nil {
		t.Fatalf("SplitPHPExtensionsFromYAML(nil) = %#v, %#v, want nil, nil", bundled, pie)
	}
}

// TestPHPExtensionsFileValueRoundTrip verifies the merger restores the shared
// YAML wire shape and survives a project-file conversion round trip.
func TestPHPExtensionsFileValueRoundTrip(t *testing.T) {
	values := PHPExtensionsFileValue(
		map[string]bool{"mbstring": true},
		map[string]string{"xdebug/xdebug": "3.4.1"},
	)
	want := map[string]any{"mbstring": true, "xdebug/xdebug": "3.4.1"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("PHPExtensionsFileValue() = %#v, want %#v", values, want)
	}
	if PHPExtensionsFileValue(nil, nil) != nil {
		t.Fatal("PHPExtensionsFileValue(nil, nil) != nil")
	}

	environment := ProjectFileToEnvironment("default", ProjectFile{PHPExtensions: want})
	if !environment.PHPExtensions["mbstring"] {
		t.Fatalf("environment.PHPExtensions = %#v, want mbstring toggle", environment.PHPExtensions)
	}
	if environment.PIEExtensions["xdebug/xdebug"] != "3.4.1" {
		t.Fatalf("environment.PIEExtensions = %#v, want xdebug entry", environment.PIEExtensions)
	}

	file := ProjectFileFromEnvironment(1, ".polka", environment)
	if !reflect.DeepEqual(file.PHPExtensions, want) {
		t.Fatalf("round-tripped php-extensions = %#v, want %#v", file.PHPExtensions, want)
	}
}

// TestNormalizePIEExtensions verifies key lowercasing, value trimming, and
// empty-entry removal.
func TestNormalizePIEExtensions(t *testing.T) {
	normalized := NormalizePIEExtensions(map[string]string{
		"Xdebug/Xdebug": " 3.4.1 ",
		"acme/empty":    "   ",
		"":              "1.0",
	})
	want := map[string]string{"xdebug/xdebug": "3.4.1"}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("NormalizePIEExtensions() = %#v, want %#v", normalized, want)
	}
	if NormalizePIEExtensions(map[string]string{"a/b": " "}) != nil {
		t.Fatal("NormalizePIEExtensions(all empty) != nil")
	}
}

// TestValidatePIEExtensionPackage exercises the Composer package-name shape.
func TestValidatePIEExtensionPackage(t *testing.T) {
	for _, valid := range []string{"xdebug/xdebug", "open-telemetry/ext.otel", "a1/b2"} {
		if err := ValidatePIEExtensionPackage(valid); err != nil {
			t.Fatalf("ValidatePIEExtensionPackage(%q) error = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []string{"", "xdebug", "/name", "vendor/", "Vendor Name/pkg", "vendor//pkg"} {
		if err := ValidatePIEExtensionPackage(invalid); err == nil {
			t.Fatalf("ValidatePIEExtensionPackage(%q) error = nil, want error", invalid)
		}
	}
}

// TestValidatePIEExtensionVersion exercises constraint sanity checks.
func TestValidatePIEExtensionVersion(t *testing.T) {
	for _, valid := range []string{"3.4.1", "*", "^1.2", ">=2.0,<3.0", "~1.0"} {
		if err := ValidatePIEExtensionVersion("acme/demo", valid); err != nil {
			t.Fatalf("ValidatePIEExtensionVersion(%q) error = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []string{"", "true", "false", "1.0 2.0", "1.0\n2.0"} {
		if err := ValidatePIEExtensionVersion("acme/demo", invalid); err == nil {
			t.Fatalf("ValidatePIEExtensionVersion(%q) error = nil, want error", invalid)
		}
	}
	if err := ValidatePIEExtensionVersion("acme/demo", "true"); err == nil || !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("ValidatePIEExtensionVersion(true) error = %v, want constraint hint", err)
	}
}
