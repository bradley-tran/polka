package tools

import (
	"strings"
	"testing"

	"polka/config"
)

// TestPIEManifestIsInternalOnly verifies the pie manifest neutralizes every
// user-facing surface: no dispatch commands, no reported version, and a
// validation error for configs that pin tools.pie.
func TestPIEManifestIsInternalOnly(t *testing.T) {
	registry := NewDefaultRegistry()
	plugin, ok := registry.Plugin(PIE)
	if !ok {
		t.Fatal("Plugin(pie) not found")
	}

	if len(plugin.DispatchCommands()) != 0 {
		t.Fatalf("DispatchCommands() = %v, want none for internal-only pie", plugin.DispatchCommands())
	}
	environment := config.Environment{PIEVersion: "1.4"}
	if version := plugin.Version(environment); version != "" {
		t.Fatalf("Version() = %q, want empty for internal-only pie", version)
	}
	if commands := plugin.ActiveCommands(environment); len(commands) != 0 {
		t.Fatalf("ActiveCommands() = %v, want none for internal-only pie", commands)
	}
	err := plugin.Validate(environment)
	if err == nil || !strings.Contains(err.Error(), "managed internally") {
		t.Fatalf("Validate(pie version set) error = %v, want managed-internally error", err)
	}
	if err := plugin.Validate(config.Environment{}); err != nil {
		t.Fatalf("Validate(no pie version) error = %v, want nil", err)
	}
}

// TestRegistryInternalToolAccessors verifies the registry surfaces
// internal-only flags and raw internal versions read from an environment.
func TestRegistryInternalToolAccessors(t *testing.T) {
	registry := NewDefaultRegistry()

	if !registry.InternalOnly(PIE) {
		t.Fatal("InternalOnly(pie) = false, want true")
	}
	if registry.InternalOnly(Composer) {
		t.Fatal("InternalOnly(composer) = true, want false")
	}
	if registry.InternalOnly("unknown") {
		t.Fatal("InternalOnly(unknown) = true, want false")
	}

	environment := config.Environment{PIEVersion: "1.4", ComposerVersion: "2.9", PHPVersion: "8.5"}
	if got := registry.InternalVersion(PIE, environment); got != "1.4" {
		t.Fatalf("InternalVersion(pie) = %q, want raw configured version 1.4", got)
	}
	if got := registry.InternalVersion(Composer, environment); got != "2.9" {
		t.Fatalf("InternalVersion(composer) = %q, want 2.9", got)
	}
	if got := registry.InternalVersion(PHP, environment); got != "8.5" {
		t.Fatalf("InternalVersion(php) = %q, want 8.5", got)
	}
	// An empty environment pins nothing: version resolution falls back to the
	// backend's shared default versions instead of any manifest metadata.
	if got := registry.InternalVersion(PIE, config.Environment{}); got != "" {
		t.Fatalf("InternalVersion(pie, empty) = %q, want empty", got)
	}
}

// TestParsePluginManifestRejectsInternalOnlyCommands verifies internal-only
// manifests cannot declare user-facing command surfaces.
func TestParsePluginManifestRejectsInternalOnlyCommands(t *testing.T) {
	manifest := []byte(strings.Join([]string{
		"id: demo",
		"internal-only: true",
		"install-candidates:",
		"  all:",
		"    - bin/demo",
		"dispatch-commands:",
		"  - demo",
	}, "\n"))
	if _, err := parsePluginManifest(manifest); err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("parsePluginManifest(internal-only with dispatch) error = %v, want internal-only rejection", err)
	}

	// An internal-only manifest without commands is valid: no version metadata
	// is required on the manifest anymore.
	valid := []byte(strings.Join([]string{
		"id: demo",
		"internal-only: true",
		"install-candidates:",
		"  all:",
		"    - bin/demo",
	}, "\n"))
	if _, err := parsePluginManifest(valid); err != nil {
		t.Fatalf("parsePluginManifest(internal-only without commands) error = %v, want nil", err)
	}
}

// TestPIEExtensionModuleName verifies module derivation from package names.
func TestPIEExtensionModuleName(t *testing.T) {
	cases := map[string]string{
		"xdebug/xdebug":  "xdebug",
		"Vendor/MyExt":   "myext",
		"  apcu/apcu   ": "apcu",
		"plain":          "plain",
	}
	for pkg, want := range cases {
		if got := PIEExtensionModuleName(pkg); got != want {
			t.Fatalf("PIEExtensionModuleName(%q) = %q, want %q", pkg, got, want)
		}
	}
}

// TestEffectivePHPExtensionsFoldsPIEModules verifies PIE-managed packages
// enable their module in the generated extension set unless a bundled toggle
// explicitly disables it.
func TestEffectivePHPExtensionsFoldsPIEModules(t *testing.T) {
	environment := config.Environment{
		PHPExtensions: map[string]bool{"mbstring": true, "apcu": false},
		PIEExtensions: map[string]string{"xdebug/xdebug": "3.4.1", "acme/apcu": "1.0"},
	}

	extensions := EffectivePHPExtensionsForInstall(environment)
	if !extensions["mbstring"] {
		t.Fatalf("extensions = %#v, want mbstring enabled", extensions)
	}
	if !extensions["xdebug"] {
		t.Fatalf("extensions = %#v, want xdebug folded in from PIE entry", extensions)
	}
	if extensions["apcu"] {
		t.Fatalf("extensions = %#v, want explicit apcu toggle to win over PIE entry", extensions)
	}
}
