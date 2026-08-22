package plugins

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"polka/config"
)

// TestBuiltinProjectPresetManifestLoads verifies the shipped extension preset
// can be parsed and converted into its init-only plugin form.
func TestBuiltinProjectPresetManifestLoads(t *testing.T) {
	manifest, err := loadBuiltinProjectPresetManifest(PHPExtension)
	if err != nil {
		t.Fatalf("loadBuiltinProjectPresetManifest(%s) error = %v", PHPExtension, err)
	}
	if manifest.ID != PHPExtension {
		t.Fatalf("manifest ID = %q, want %q", manifest.ID, PHPExtension)
	}
	if manifest.Defaults.Framework != "" {
		t.Fatalf("manifest defaults.framework = %q, want empty", manifest.Defaults.Framework)
	}

	preset := newManifestProjectPreset(PHPExtension)
	defaults := preset.Defaults()
	if defaults.PHPVersion != "8.4" || defaults.ComposerVersion != "2.8" || !defaults.PHPBuildTools {
		t.Fatalf("preset Defaults() = %#v, want PHP, Composer, and extension SDK", defaults)
	}
	if defaults.Docroot != "" || defaults.HTTPS || defaults.Server != nil || defaults.Database != nil {
		t.Fatalf("preset Defaults() = %#v, want no web or database settings", defaults)
	}
	if defaults.MemoryLimit != "-1" {
		t.Fatalf("preset memory limit = %q, want -1", defaults.MemoryLimit)
	}
}

// TestProjectPresetScaffoldRendering checks that directory-derived and
// explicitly supplied names produce valid PIE Composer manifests.
func TestProjectPresetScaffoldRendering(t *testing.T) {
	preset := newManifestProjectPreset(PHPExtension)
	tests := []struct {
		projectDir  string
		packageName string
		phpVersion  string
		wantPackage string
		wantExt     string
		wantPHP     string
	}{
		{projectDir: "my-ext", phpVersion: "8.4", wantPackage: "vendor/my-ext", wantExt: "my_ext", wantPHP: "^8.4"},
		{projectDir: "PHP-Foo", wantPackage: "vendor/php-foo", wantExt: "php_foo", wantPHP: "*"},
		{projectDir: "9lives", wantPackage: "vendor/9lives", wantExt: "my_extension", wantPHP: "*"},
		{projectDir: "-", wantPackage: "vendor/my-extension", wantExt: "my_extension", wantPHP: "*"},
		{projectDir: "ignored", packageName: "acme/native-ext", wantPackage: "acme/native-ext", wantExt: "ignored", wantPHP: "*"},
	}

	for _, test := range tests {
		t.Run(test.projectDir+test.packageName, func(t *testing.T) {
			files := preset.ScaffoldFiles(ScaffoldContext{
				ProjectDir:  test.projectDir,
				PackageName: test.packageName,
				PHPVersion:  test.phpVersion,
			})
			if len(files) != 1 || files[0].Path != "composer.json" {
				t.Fatalf("ScaffoldFiles() = %#v, want composer.json", files)
			}
			var manifest struct {
				Name    string            `json:"name"`
				Require map[string]string `json:"require"`
				PHPExt  struct {
					ExtensionName string `json:"extension-name"`
				} `json:"php-ext"`
			}
			if err := json.Unmarshal(files[0].Content, &manifest); err != nil {
				t.Fatalf("json.Unmarshal(scaffold) error = %v", err)
			}
			if manifest.Name != test.wantPackage || manifest.PHPExt.ExtensionName != test.wantExt || manifest.Require["php"] != test.wantPHP {
				t.Fatalf("rendered manifest = %#v, want package %q, extension %q, PHP %q", manifest, test.wantPackage, test.wantExt, test.wantPHP)
			}
			if err := config.ValidatePIEExtensionPackage(manifest.Name); err != nil {
				t.Fatalf("derived package %q is invalid: %v", manifest.Name, err)
			}
		})
	}
}

// TestParseProjectPresetManifestRejectsInvalidDefinitions covers preset-only
// invariants that framework manifests do not share.
func TestParseProjectPresetManifestRejectsInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{name: "missing id", yaml: "defaults: {}", wantErr: "id cannot be empty"},
		{name: "invalid id", yaml: "id: bad\\nvalue", wantErr: "invalid project preset manifest id"},
		{name: "claims framework", yaml: "id: demo\ndefaults:\n  framework: laravel", wantErr: "defaults.framework must be empty"},
		{name: "absolute path", yaml: "id: demo\nscaffold:\n  - path: /tmp/file\n    template-file: php-extension.composer.json", wantErr: "must be slash-separated and relative"},
		{name: "dot segment", yaml: "id: demo\nscaffold:\n  - path: nested/../file\n    template-file: php-extension.composer.json", wantErr: "must not contain"},
		{name: "reserved config", yaml: "id: demo\nscaffold:\n  - path: polka.dev.yaml\n    template-file: php-extension.composer.json", wantErr: "reserved for init"},
		{name: "reserved root", yaml: "id: demo\nscaffold:\n  - path: .polka/data\n    template-file: php-extension.composer.json", wantErr: "reserved for init"},
		{name: "missing template", yaml: "id: demo\nscaffold:\n  - path: file\n    template-file: absent", wantErr: "read scaffold template"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseProjectPresetManifest([]byte(test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("parseProjectPresetManifest() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

// TestDefaultProjectPresetsHaveStableIDs guards the set used by init help and
// unsupported-preset diagnostics.
func TestDefaultProjectPresetsHaveStableIDs(t *testing.T) {
	presets := DefaultProjectPresets()
	ids := make([]string, 0, len(presets))
	for _, preset := range presets {
		ids = append(ids, preset.ID())
	}
	if !reflect.DeepEqual(ids, []string{PHPExtension}) {
		t.Fatalf("DefaultProjectPresets() IDs = %#v, want php-extension", ids)
	}
}
