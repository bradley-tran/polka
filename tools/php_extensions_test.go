package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"polka/config"
)

func TestRenderPHPConfigCombinesExtensionsAndOPcache(t *testing.T) {
	configData, err := renderPHPConfig("../ext", PHPInstallConfig{
		Extensions: map[string]bool{
			"opcache": true,
			"openssl": false,
			"zip":     true,
		},
		OPcacheConfig: map[string]string{
			"OPcache.Validate_Timestamps": "1",
			"opcache.enable":              "1",
		},
		MemoryLimit:  "512m",
		CABundlePath: "/opt/polka/cacert.pem",
	})
	if err != nil {
		t.Fatalf("renderPHPConfig() error = %v", err)
	}

	phpIni := string(configData)
	for _, want := range []string{
		"extension_dir=\"../ext\"",
		"memory_limit=512M",
		"curl.cainfo=\"/opt/polka/cacert.pem\"",
		"openssl.cafile=\"/opt/polka/cacert.pem\"",
		";extension=openssl",
		"zend_extension=opcache",
		"extension=zip",
		"[opcache]",
		"opcache.enable=1",
		"opcache.validate_timestamps=1",
	} {
		if !strings.Contains(phpIni, want) {
			t.Fatalf("php.ini = %q, want %s", phpIni, want)
		}
	}
	if strings.Index(phpIni, ";extension=openssl") > strings.Index(phpIni, "extension=zip") {
		t.Fatalf("php.ini = %q, want sorted extension entries", phpIni)
	}
	if phpIniHasLine(phpIni, "extension=opcache") || phpIniHasLine(phpIni, ";extension=opcache") {
		t.Fatalf("php.ini = %q, want opcache as zend_extension only", phpIni)
	}
	if strings.Index(phpIni, "opcache.enable=1") > strings.Index(phpIni, "opcache.validate_timestamps=1") {
		t.Fatalf("php.ini = %q, want sorted OPcache entries", phpIni)
	}
}

func TestPHPConfigNeedsCABundleForTLSClientExtensions(t *testing.T) {
	for _, extension := range []string{"curl", "OpenSSL"} {
		config := PHPInstallConfig{Extensions: map[string]bool{extension: true}}
		if !phpConfigNeedsCABundle(config) {
			t.Fatalf("phpConfigNeedsCABundle(%s) = false, want true", extension)
		}
	}

	config := PHPInstallConfig{Extensions: map[string]bool{"openssl": false, "zip": true}}
	if phpConfigNeedsCABundle(config) {
		t.Fatalf("phpConfigNeedsCABundle(%#v) = true, want false", config.Extensions)
	}
}

func TestRenderPHPConfigDisablesOPcacheAsZendExtension(t *testing.T) {
	configData, err := renderPHPConfig("../ext", PHPInstallConfig{
		Extensions: map[string]bool{
			"opcache": false,
		},
	})
	if err != nil {
		t.Fatalf("renderPHPConfig() error = %v", err)
	}

	phpIni := string(configData)
	if !strings.Contains(phpIni, ";zend_extension=opcache") {
		t.Fatalf("php.ini = %q, want disabled OPcache zend_extension", phpIni)
	}
	if phpIniHasLine(phpIni, "extension=opcache") || phpIniHasLine(phpIni, ";extension=opcache") {
		t.Fatalf("php.ini = %q, want no normal OPcache extension entry", phpIni)
	}
}

// TestRenderPHPConfigLoadsZendExtensionsWithZendDirective verifies known Zend
// extensions such as xdebug render as zend_extension lines (PIE-installed
// xdebug would fail to load through a plain extension line), while regular
// extensions keep the extension directive.
func TestRenderPHPConfigLoadsZendExtensionsWithZendDirective(t *testing.T) {
	configData, err := renderPHPConfig("../ext", PHPInstallConfig{
		Extensions: map[string]bool{
			"xdebug": true,
			"apcu":   true,
		},
	})
	if err != nil {
		t.Fatalf("renderPHPConfig() error = %v", err)
	}

	phpIni := string(configData)
	if !phpIniHasLine(phpIni, "zend_extension=xdebug") {
		t.Fatalf("php.ini = %q, want xdebug loaded as zend_extension", phpIni)
	}
	if phpIniHasLine(phpIni, "extension=xdebug") {
		t.Fatalf("php.ini = %q, want no normal xdebug extension entry", phpIni)
	}
	if !phpIniHasLine(phpIni, "extension=apcu") {
		t.Fatalf("php.ini = %q, want apcu loaded as a regular extension", phpIni)
	}
}

// TestRenderMetadataDefinedZendExtension verifies legacy PECL package
// metadata can mark Zend modules beyond the built-in known-module list.
func TestRenderMetadataDefinedZendExtension(t *testing.T) {
	configData, err := renderPHPConfig("../ext", PHPInstallConfig{
		Extensions:     map[string]bool{"custom_zend": true},
		ZendExtensions: map[string]bool{"custom_zend": true},
	})
	if err != nil {
		t.Fatalf("renderPHPConfig() error = %v", err)
	}
	if !phpIniHasLine(string(configData), "zend_extension=custom_zend") {
		t.Fatalf("php.ini = %q, want metadata-defined Zend extension", string(configData))
	}
}

func TestRenderPHPConfigSupportsOPcacheOnly(t *testing.T) {
	configData, err := renderPHPConfig("../ext", PHPInstallConfig{
		OPcacheConfig: map[string]string{
			"opcache.enable": "1",
		},
	})
	if err != nil {
		t.Fatalf("renderPHPConfig() error = %v", err)
	}

	phpIni := string(configData)
	if strings.Contains(phpIni, "extension_dir") {
		t.Fatalf("php.ini = %q, want no extension_dir for OPcache-only config", phpIni)
	}
	if !strings.Contains(phpIni, "[opcache]\nopcache.enable=1\n") {
		t.Fatalf("php.ini = %q, want OPcache section", phpIni)
	}
}

func TestRenderPHPConfigSupportsMemoryLimitOnly(t *testing.T) {
	configData, err := renderPHPConfig("../ext", PHPInstallConfig{
		MemoryLimit: "-1",
	})
	if err != nil {
		t.Fatalf("renderPHPConfig() error = %v", err)
	}

	phpIni := string(configData)
	if strings.Contains(phpIni, "extension_dir") || strings.Contains(phpIni, "[opcache]") {
		t.Fatalf("php.ini = %q, want memory-limit only config", phpIni)
	}
	if !strings.Contains(phpIni, "[PHP]\nmemory_limit=-1\n") {
		t.Fatalf("php.ini = %q, want memory_limit directive", phpIni)
	}
}

// TestRenderPHPConfigRequireExtensionDirWithoutExtensions verifies a runtime
// that must declare its extension directory gets one with nothing to load.
func TestRenderPHPConfigRequireExtensionDirWithoutExtensions(t *testing.T) {
	configData, err := renderPHPConfig("/opt/php/ext", PHPInstallConfig{RequireExtensionDir: true})
	if err != nil {
		t.Fatalf("renderPHPConfig() error = %v", err)
	}
	if !phpIniHasLine(string(configData), `extension_dir="/opt/php/ext"`) {
		t.Fatalf("php.ini = %q, want extension_dir line", string(configData))
	}
}

// TestPHPInstallConfigIsZeroHonorsRequireExtensionDir verifies the requirement
// alone is enough to generate a php.ini.
func TestPHPInstallConfigIsZeroHonorsRequireExtensionDir(t *testing.T) {
	if !(PHPInstallConfig{}).IsZero() {
		t.Fatal("empty config IsZero() = false, want true")
	}
	if (PHPInstallConfig{RequireExtensionDir: true}).IsZero() {
		t.Fatal("RequireExtensionDir config IsZero() = true, want false")
	}
}

// TestInstalledPHPExtensionDirValue pins the platform split: Linux needs an
// absolute path because PHP resolves relative values against the working
// directory, while other platforms keep the existing relative form.
func TestInstalledPHPExtensionDirValue(t *testing.T) {
	root := t.TempDir()
	phpDir := filepath.Join(root, "bin")
	extensionPath := filepath.Join(root, "ext")

	got, err := installedPHPExtensionDirValue("linux", phpDir, extensionPath)
	if err != nil {
		t.Fatalf("installedPHPExtensionDirValue(linux) error = %v", err)
	}
	if want := filepath.ToSlash(extensionPath); got != want {
		t.Fatalf("installedPHPExtensionDirValue(linux) = %q, want %q", got, want)
	}

	got, err = installedPHPExtensionDirValue("windows", phpDir, extensionPath)
	if err != nil {
		t.Fatalf("installedPHPExtensionDirValue(windows) error = %v", err)
	}
	if want := filepath.Join("..", "ext"); got != want {
		t.Fatalf("installedPHPExtensionDirValue(windows) = %q, want %q", got, want)
	}
}

// TestInstalledPHPExtensionDirValueRejectsUnsafePath verifies a path that would
// corrupt the quoted ini value fails instead of writing a broken php.ini.
func TestInstalledPHPExtensionDirValueRejectsUnsafePath(t *testing.T) {
	if _, err := installedPHPExtensionDirValue("linux", "/php/bin", `/php/ex"t`); err == nil {
		t.Fatal("installedPHPExtensionDirValue() error = nil, want rejection of a quote")
	}
}

// TestPHPInstallRequiresExtensionDir verifies only a Linux install that ships
// its own ext directory needs the directive.
func TestPHPInstallRequiresExtensionDir(t *testing.T) {
	installDir := t.TempDir()

	assertRequires := func(goos string, want bool) {
		t.Helper()
		got, err := phpInstallRequiresExtensionDir(goos, installDir)
		if err != nil {
			t.Fatalf("phpInstallRequiresExtensionDir(%s) error = %v", goos, err)
		}
		if got != want {
			t.Fatalf("phpInstallRequiresExtensionDir(%s) = %v, want %v", goos, got, want)
		}
	}

	assertRequires("linux", false)
	if err := os.Mkdir(filepath.Join(installDir, "ext"), 0o755); err != nil {
		t.Fatalf("Mkdir(ext) error = %v", err)
	}
	assertRequires("linux", true)
	assertRequires("windows", false)
}

// TestSyncInstalledPHPRuntimeConfigWritesExtensionDirForBuiltLinuxPHP verifies
// an environment with no PHP settings still gets an absolute extension_dir when
// the install has an ext directory, and stays ini-less without one.
func TestSyncInstalledPHPRuntimeConfigWritesExtensionDirForBuiltLinuxPHP(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the requirement only applies to Linux installs")
	}

	envsDir := t.TempDir()
	installDir := filepath.Join(envsDir, PHP, "8.4")
	phpPath := filepath.Join(installDir, "bin", "php")
	if err := os.MkdirAll(filepath.Dir(phpPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(bin) error = %v", err)
	}
	if err := os.WriteFile(phpPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(php) error = %v", err)
	}
	iniPath := filepath.Join(installDir, "bin", "php.ini")

	if err := SyncInstalledPHPRuntimeConfig(envsDir, PHP, "8.4", config.Environment{}); err != nil {
		t.Fatalf("SyncInstalledPHPRuntimeConfig(no ext) error = %v", err)
	}
	if _, err := os.Stat(iniPath); !os.IsNotExist(err) {
		t.Fatalf("Stat(php.ini) error = %v, want not-exist for an ini-less install", err)
	}

	extensionPath := filepath.Join(installDir, "ext")
	if err := os.Mkdir(extensionPath, 0o755); err != nil {
		t.Fatalf("Mkdir(ext) error = %v", err)
	}
	if err := SyncInstalledPHPRuntimeConfig(envsDir, PHP, "8.4", config.Environment{}); err != nil {
		t.Fatalf("SyncInstalledPHPRuntimeConfig(ext) error = %v", err)
	}
	data, err := os.ReadFile(iniPath)
	if err != nil {
		t.Fatalf("ReadFile(php.ini) error = %v", err)
	}
	if want := `extension_dir="` + filepath.ToSlash(extensionPath) + `"`; !phpIniHasLine(string(data), want) {
		t.Fatalf("php.ini = %q, want line %s", string(data), want)
	}
}

func TestParsePHPModuleListNormalizesBuiltInExtensions(t *testing.T) {
	modules := parsePHPModuleList([]byte("[PHP Modules]\nCore\nOpenSSL\nzip\n[Zend Modules]\nZend OPcache\n"))

	for _, name := range []string{"core", "openssl", "zip", "opcache"} {
		if !modules[name] {
			t.Fatalf("parsePHPModuleList() = %#v, want %s", modules, name)
		}
	}
	if modules["[php modules]"] {
		t.Fatalf("parsePHPModuleList() = %#v, want section headers skipped", modules)
	}
}

func TestSkipBuiltInPHPExtensions(t *testing.T) {
	extensions := map[string]bool{
		"OpenSSL": true,
		"opcache": true,
		"xdebug":  false,
		"zip":     true,
	}
	builtInExtensions := map[string]bool{
		"openssl": true,
		"opcache": true,
	}

	filtered := skipBuiltInPHPExtensions(extensions, builtInExtensions)
	if filtered["OpenSSL"] {
		t.Fatalf("skipBuiltInPHPExtensions() = %#v, want OpenSSL skipped", filtered)
	}
	if filtered["opcache"] {
		t.Fatalf("skipBuiltInPHPExtensions() = %#v, want opcache skipped", filtered)
	}
	if !filtered["zip"] {
		t.Fatalf("skipBuiltInPHPExtensions() = %#v, want zip kept", filtered)
	}
	if enabled, ok := filtered["xdebug"]; !ok || enabled {
		t.Fatalf("skipBuiltInPHPExtensions() = %#v, want disabled xdebug kept", filtered)
	}
}

func phpIniHasLine(phpIni, line string) bool {
	for _, current := range strings.Split(phpIni, "\n") {
		if current == line {
			return true
		}
	}

	return false
}
