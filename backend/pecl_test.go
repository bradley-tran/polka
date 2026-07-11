package backend

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"polka/config"
)

// TestResolvePECLWindowsArchive selects the archive matching PHP series,
// thread-safety mode, architecture, and release.
func TestResolvePECLWindowsArchive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(strings.Join([]string{
			`<a href="php_redis-6.2.0-8.4-ts-vs17-x64.zip">TS</a>`,
			`<a href="php_redis-6.2.0-8.4-nts-vs17-x64.zip">NTS</a>`,
			`<a href="php_redis-6.2.0-8.3-nts-vs16-x64.zip">old PHP</a>`,
		}, "\n")))
	}))
	defer server.Close()

	original := peclWindowsBaseURL
	peclWindowsBaseURL = server.URL
	t.Cleanup(func() { peclWindowsBaseURL = original })
	url, err := resolvePECLWindowsArchive(server.Client(), "redis", "6.2.0", "redis", peclPHPInfo{Series: "8.4", Arch: "x64"})
	if err != nil {
		t.Fatalf("resolvePECLWindowsArchive() error = %v", err)
	}
	if !strings.HasSuffix(url, "php_redis-6.2.0-8.4-nts-vs17-x64.zip") {
		t.Fatalf("archive URL = %q", url)
	}
}

// TestPECLPHPVersionCompatible verifies package min/max/exclude constraints.
func TestPECLPHPVersionCompatible(t *testing.T) {
	if !peclPHPVersionCompatible("8.4.1", "8.1.0", "8.4.99", nil) {
		t.Fatal("compatible PHP version rejected")
	}
	if peclPHPVersionCompatible("8.0.30", "8.1.0", "", nil) {
		t.Fatal("PHP below minimum accepted")
	}
	if peclPHPVersionCompatible("8.4.1", "", "", []string{"8.4.1"}) {
		t.Fatal("excluded PHP version accepted")
	}
}

// TestValidatePECLConfigureOptions rejects answers not declared by package.xml.
func TestValidatePECLConfigureOptions(t *testing.T) {
	resolution := peclResolution{Package: "imagick", Version: "3.8.0", Configure: map[string]peclConfigureOption{
		"with-imagick": {Name: "with-imagick"},
	}}
	if err := validatePECLConfigureOptions(resolution, map[string]string{"with-imagick": "/opt"}); err != nil {
		t.Fatalf("validatePECLConfigureOptions() error = %v", err)
	}
	if err := validatePECLConfigureOptions(resolution, map[string]string{"unknown": "yes"}); err == nil {
		t.Fatal("validatePECLConfigureOptions(unknown) error = nil")
	}
}

// TestValidatePECLExtensionsSchema covers scalar and structured public config
// while rejecting boolean package versions and unknown object fields.
func TestValidatePECLExtensionsSchema(t *testing.T) {
	valid := []byte("pecl-extensions:\n  redis: 6.2.0\n  imagick:\n    version: 3.8.0\n    configure-options:\n      with-imagick: autodetect\n")
	if err := validateEnvironmentFileSchema(valid); err != nil {
		t.Fatalf("validateEnvironmentFileSchema(valid PECL) error = %v", err)
	}
	for _, invalid := range [][]byte{
		[]byte("pecl-extensions:\n  redis: true\n"),
		[]byte("pecl-extensions:\n  redis:\n    version: 6.2.0\n    unknown: value\n"),
	} {
		if err := validateEnvironmentFileSchema(invalid); err == nil {
			t.Fatalf("validateEnvironmentFileSchema(%q) error = nil", invalid)
		}
	}
}

// TestWithInstalledPECLExtensionsFoldsTrackedModules verifies generated PHP
// config includes tracked modules while explicit boolean disables still win.
func TestWithInstalledPECLExtensionsFoldsTrackedModules(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), ".polka"))
	installRoot := filepath.Join(store.EnvsDir, toolPHP, "8.4")
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	state := peclInstallState{Packages: map[string]peclInstalledPackage{
		"redis":  {Version: "6.2.0", Module: "redis"},
		"xdebug": {Version: "3.4.1", Module: "xdebug", Zend: true},
	}}
	if err := writePECLInstallState(installRoot, state); err != nil {
		t.Fatal(err)
	}
	environment := config.Environment{
		PHPVersion:    "8.4",
		PHPExtensions: map[string]bool{"redis": false},
		PECLExtensions: map[string]config.PECLExtensionConfig{
			"redis":  {Version: "6.2.0"},
			"xdebug": {Version: "3.4.1"},
		},
	}
	merged := store.withInstalledPECLExtensions(environment)
	if merged.PHPExtensions["redis"] {
		t.Fatal("explicit redis=false override was lost")
	}
	if !merged.PHPExtensions["xdebug"] || !merged.ZendExtensions["xdebug"] {
		t.Fatalf("merged PECL runtime config = %#v, %#v", merged.PHPExtensions, merged.ZendExtensions)
	}
}

// TestInstallAndRemovePECLExtensionWindows exercises official metadata
// resolution, DLL installation, state recording, and safe removal.
func TestInstallAndRemovePECLExtensionWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PECL DLL integration")
	}
	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	module, err := zipWriter.Create("php_redis.dll")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = module.Write([]byte("redis-dll"))
	dependency, err := zipWriter.Create("redis_dependency.dll")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = dependency.Write([]byte("dependency-dll"))
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/rest/r/redis/6.2.0.xml":
			_, _ = writer.Write([]byte(`<r><v>6.2.0</v><st>stable</st><g>ignored-on-windows</g></r>`))
		case "/rest/r/redis/package.6.2.0.xml":
			_, _ = writer.Write([]byte(`<package><name>redis</name><version><release>6.2.0</release></version><dependencies><required><php><min>8.0.0</min><max>8.4.99</max></php></required></dependencies><providesextension>redis</providesextension><extsrcrelease/></package>`))
		case "/windows/redis/6.2.0/":
			_, _ = writer.Write([]byte(`<a href="php_redis-6.2.0-8.4-nts-vs17-x64.zip">DLL</a>`))
		case "/windows/redis/6.2.0/php_redis-6.2.0-8.4-nts-vs17-x64.zip":
			_, _ = writer.Write(archive.Bytes())
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	originalREST, originalWindows := peclRESTBaseURL, peclWindowsBaseURL
	peclRESTBaseURL, peclWindowsBaseURL = server.URL+"/rest", server.URL+"/windows"
	t.Cleanup(func() {
		peclRESTBaseURL, peclWindowsBaseURL = originalREST, originalWindows
	})

	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	store := NewStore(root)
	store.Downloader = HTTPToolDownloader{Client: server.Client()}
	environment := config.Environment{Name: "default", PHPVersion: "8.4"}
	if err := writeYAML(store.ConfigFile, config.ProjectFileFromEnvironment(1, ".polka", environment)); err != nil {
		t.Fatal(err)
	}
	installRoot := filepath.Join(store.EnvsDir, toolPHP, "8.4")
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	phpPath := filepath.Join(installRoot, "php.cmd")
	phpScript := strings.Join([]string{
		"@echo off",
		"if \"%1\"==\"-n\" echo 8.4.1~8.4~0~x64& exit /b 0",
		"if \"%1\"==\"-m\" echo Core& exit /b 0",
		"exit /b 0",
	}, "\r\n")
	if err := os.WriteFile(phpPath, []byte(phpScript), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := store.InstallPECLExtension(&bytes.Buffer{}, &bytes.Buffer{}, "default", "redis", "6.2.0", nil)
	if err != nil {
		t.Fatalf("InstallPECLExtension() error = %v", err)
	}
	if result.Version != "6.2.0" || result.Module != "redis" {
		t.Fatalf("InstallPECLExtension() = %#v", result)
	}
	modulePath := filepath.Join(installRoot, "ext", "php_redis.dll")
	if _, err := os.Stat(modulePath); err != nil {
		t.Fatalf("installed module: %v", err)
	}
	if _, err := store.RemovePECLExtension(&bytes.Buffer{}, "default", "redis"); err != nil {
		t.Fatalf("RemovePECLExtension() error = %v", err)
	}
	if _, err := os.Stat(modulePath); !os.IsNotExist(err) {
		t.Fatalf("module still exists after removal: %v", err)
	}
}
