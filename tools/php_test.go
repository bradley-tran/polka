package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
