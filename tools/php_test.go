package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"polka/config"
)

func TestSelectPHPWindowsAssetUsesStrictFlavor(t *testing.T) {
	release := phpWindowsRelease{
		Version: "8.4.8",
		Variants: map[string]phpWindowsVariant{
			"nts-vs17-x64": {Zip: phpWindowsAsset{Path: "php-nts.zip"}},
			"ts-vs17-x64":  {Zip: phpWindowsAsset{Path: "php-ts.zip"}},
		},
	}

	nts, err := selectPHPWindowsAsset(release, false)
	if err != nil || nts.Path != "php-nts.zip" {
		t.Fatalf("selectPHPWindowsAsset(NTS) = %#v, %v", nts, err)
	}
	zts, err := selectPHPWindowsAsset(release, true)
	if err != nil || zts.Path != "php-ts.zip" {
		t.Fatalf("selectPHPWindowsAsset(ZTS) = %#v, %v", zts, err)
	}
}

// TestPHPWindowsReleaseIndexDecodesDevelPack verifies the release-index field
// used by extension SDK provisioning survives the custom release decoder.
func TestPHPWindowsReleaseIndexDecodesDevelPack(t *testing.T) {
	var index phpWindowsReleaseIndex
	err := json.Unmarshal([]byte(`{
		"8.4": {
			"version": "8.4.24",
			"nts-vs17-x64": {
				"zip": {"path": "php-8.4.24-nts.zip", "sha256": "runtime"},
				"devel_pack": {"path": "php-devel-pack-8.4.24-nts.zip", "sha256": "devel"}
			}
		}
	}`), &index)
	if err != nil {
		t.Fatalf("json.Unmarshal(release index) error = %v", err)
	}
	variant, err := selectPHPWindowsVariant(index["8.4"], false)
	if err != nil {
		t.Fatalf("selectPHPWindowsVariant() error = %v", err)
	}
	if variant.Zip.Path != "php-8.4.24-nts.zip" || variant.DevelPack.Path != "php-devel-pack-8.4.24-nts.zip" || variant.DevelPack.SHA256 != "devel" {
		t.Fatalf("selected variant = %#v, want matching runtime and devel pack", variant)
	}
}

// TestPHPBuildToolDependencies verifies the SDK signal adds internal Windows
// build payloads for either PHP runtime flavor and remains inert otherwise.
func TestPHPBuildToolDependencies(t *testing.T) {
	if dependencies := phpPlugin().(ToolDependencyProvider).Dependencies(config.Environment{PHPVersion: "8.4"}); dependencies != nil {
		t.Fatalf("Dependencies(flag off) = %#v, want nil", dependencies)
	}

	tests := []struct {
		name        string
		plugin      Plugin
		environment config.Environment
	}{
		{name: "nts", plugin: phpPlugin(), environment: config.Environment{PHPVersion: "8.4", PHPBuildTools: true}},
		{name: "zts", plugin: phpZTSPlugin(), environment: config.Environment{PHPZTSVersion: "8.4", PHPBuildTools: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies := test.plugin.(ToolDependencyProvider).Dependencies(test.environment)
			if runtime.GOOS != "windows" {
				if dependencies != nil {
					t.Fatalf("Dependencies(non-Windows) = %#v, want nil", dependencies)
				}
				return
			}
			want := []InstallRequest{{Tool: PHPDevel, Version: "8.4"}, {Tool: PHPSDK, Version: DefaultPHPSDKVersion}}
			if !reflect.DeepEqual(dependencies, want) {
				t.Fatalf("Dependencies() = %#v, want %#v", dependencies, want)
			}
		})
	}
}

// TestSelectPHPWindowsVariantPairsZTSDevelPack checks TS PHP never falls back
// to the NTS development archive.
func TestSelectPHPWindowsVariantPairsZTSDevelPack(t *testing.T) {
	release := phpWindowsRelease{
		Version: "8.4.24",
		Variants: map[string]phpWindowsVariant{
			"nts-vs17-x64": {Zip: phpWindowsAsset{Path: "php-nts.zip"}, DevelPack: phpWindowsAsset{Path: "php-devel-nts.zip"}},
			"ts-vs17-x64":  {Zip: phpWindowsAsset{Path: "php-ts.zip"}, DevelPack: phpWindowsAsset{Path: "php-devel-ts.zip"}},
		},
	}

	variant, err := selectPHPWindowsVariant(release, true)
	if err != nil {
		t.Fatalf("selectPHPWindowsVariant(ZTS) error = %v", err)
	}
	if variant.Zip.Path != "php-ts.zip" || variant.DevelPack.Path != "php-devel-ts.zip" {
		t.Fatalf("selected ZTS variant = %#v, want TS runtime/devel pair", variant)
	}
}

// TestParsePHPDevelCacheVersion checks the cache-only flavor discriminator
// never leaks into PHP release-index resolution.
func TestParsePHPDevelCacheVersion(t *testing.T) {
	for _, test := range []struct {
		input          string
		wantVersion    string
		wantThreadSafe bool
	}{
		{input: "8.4-nts", wantVersion: "8.4"},
		{input: "8.4-zts", wantVersion: "8.4", wantThreadSafe: true},
		{input: "8.4", wantVersion: "8.4"},
	} {
		version, threadSafe := parsePHPDevelCacheVersion(test.input)
		if version != test.wantVersion || threadSafe != test.wantThreadSafe {
			t.Fatalf("parsePHPDevelCacheVersion(%q) = %q, %v; want %q, %v", test.input, version, threadSafe, test.wantVersion, test.wantThreadSafe)
		}
	}
}

func TestSelectPHPWindowsAssetDoesNotCrossFlavor(t *testing.T) {
	release := phpWindowsRelease{
		Version: "8.4.8",
		Variants: map[string]phpWindowsVariant{
			"ts-vs17-x64": {Zip: phpWindowsAsset{Path: "php-ts.zip"}},
		},
	}

	_, err := selectPHPWindowsAsset(release, false)
	if err == nil || !strings.Contains(err.Error(), "NTS") {
		t.Fatalf("selectPHPWindowsAsset(NTS) error = %v, want strict NTS failure", err)
	}
}

func TestEnsureInstalledPHPOpenSSLConfigWritesFallback(t *testing.T) {
	envsDir := t.TempDir()

	path, err := ensureInstalledPHPOpenSSLConfig(envsDir, PHP, "8.4")
	if err != nil {
		t.Fatalf("ensureInstalledPHPOpenSSLConfig() error = %v", err)
	}
	if path != filepath.Join(envsDir, PHP, "8.4", filepath.FromSlash(phpOpenSSLConfigPath)) {
		t.Fatalf("openssl config path = %q, want project-local PHP config path", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(openssl.cnf) error = %v", err)
	}
	config := string(data)
	for _, want := range []string{"HOME = .", "[req]", "distinguished_name = req_distinguished_name", "[req_distinguished_name]"} {
		if !strings.Contains(config, want) {
			t.Fatalf("openssl.cnf = %q, want %q", config, want)
		}
	}
}

func TestEnsureInstalledPHPOpenSSLConfigPreservesExisting(t *testing.T) {
	envsDir := t.TempDir()
	path := installedPHPOpenSSLConfigPath(envsDir, PHP, "8.4")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(openssl config dir) error = %v", err)
	}
	if err := os.WriteFile(path, []byte("custom-openssl-config\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(openssl.cnf) error = %v", err)
	}

	got, err := ensureInstalledPHPOpenSSLConfig(envsDir, PHP, "8.4")
	if err != nil {
		t.Fatalf("ensureInstalledPHPOpenSSLConfig() error = %v", err)
	}
	if got != path {
		t.Fatalf("openssl config path = %q, want %q", got, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(openssl.cnf) error = %v", err)
	}
	if string(data) != "custom-openssl-config\n" {
		t.Fatalf("openssl.cnf = %q, want existing contents preserved", data)
	}
}
