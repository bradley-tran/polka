package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"polka/config"
)

func TestBuiltinManifestsLoad(t *testing.T) {
	for _, tool := range []string{PHP, PHPZTS, FrankenPHP, Composer, PIE, NodeJS, Mago, Nginx, Apache, Mailpit, PHPMyAdmin, MySQL, MariaDB, PostgreSQL, SQLite} {
		t.Run(tool, func(t *testing.T) {
			manifest, err := loadBuiltinManifest(tool)
			if err != nil {
				t.Fatalf("loadBuiltinManifest(%s) error = %v", tool, err)
			}
			if manifest.ID != tool {
				t.Fatalf("loadBuiltinManifest(%s) id = %q, want %q", tool, manifest.ID, tool)
			}
		})
	}
}

func TestParsePluginManifestRequiresID(t *testing.T) {
	_, err := parsePluginManifest([]byte(`
install-candidates:
  all:
    - bin/demo
`))
	if err == nil || !strings.Contains(err.Error(), "id cannot be empty") {
		t.Fatalf("parsePluginManifest(missing id) error = %v, want missing id error", err)
	}
}

func TestParsePluginManifestRejectsInvalidID(t *testing.T) {
	_, err := parsePluginManifest([]byte(`
id: "not a tool"
install-candidates:
  all:
    - bin/demo
`))
	if err == nil || !strings.Contains(err.Error(), "invalid tool manifest id") {
		t.Fatalf("parsePluginManifest(invalid id) error = %v, want invalid id error", err)
	}
}

func TestParsePluginManifestNormalizesPHPExtensions(t *testing.T) {
	manifest, err := parsePluginManifest([]byte(`
id: demo
php-extensions:
  - " OpenSSL "
  - openssl
  - PDO_MySQL
install-candidates:
  all:
    - bin/demo
`))
	if err != nil {
		t.Fatalf("parsePluginManifest() error = %v", err)
	}
	if want := []string{"openssl", "pdo_mysql"}; !reflect.DeepEqual(manifest.PHPExtensions, want) {
		t.Fatalf("php-extensions = %#v, want %#v", manifest.PHPExtensions, want)
	}

	plugin, err := manifest.toPlugin(pluginHooks{})
	if err != nil {
		t.Fatalf("toPlugin() error = %v", err)
	}
	extensions := plugin.PHPExtensions()
	extensions["openssl"] = false
	if !plugin.PHPExtensions()["openssl"] {
		t.Fatalf("PHPExtensions() = %#v, want defensive copy", plugin.PHPExtensions())
	}
}

func TestParsePluginManifestRejectsInvalidPHPExtensions(t *testing.T) {
	for _, extension := range []string{"", "bad extension"} {
		_, err := parsePluginManifest([]byte(`
id: demo
php-extensions:
  - "` + extension + `"
install-candidates:
  all:
    - bin/demo
`))
		if err == nil || !strings.Contains(err.Error(), "php-extensions[0]") {
			t.Fatalf("parsePluginManifest(%q) error = %v, want extension validation error", extension, err)
		}
	}
}

func TestBuiltinToolPHPExtensions(t *testing.T) {
	registry := NewDefaultRegistry()
	tests := map[string][]string{
		Composer:   {"openssl", "zip"},
		MySQL:      {"mysqli", "pdo_mysql"},
		MariaDB:    {"mysqli", "pdo_mysql"},
		PostgreSQL: {"pgsql", "pdo_pgsql"},
		SQLite:     {"pdo_sqlite", "sqlite3"},
	}
	for tool, expected := range tests {
		plugin, ok := registry.Plugin(tool)
		if !ok {
			t.Fatalf("Plugin(%q) ok = false", tool)
		}
		for _, extension := range expected {
			if !plugin.PHPExtensions()[extension] {
				t.Fatalf("Plugin(%q).PHPExtensions() = %#v, want %s", tool, plugin.PHPExtensions(), extension)
			}
		}
	}
}

func TestRegistryPHPExtensionsUsesConfiguredTools(t *testing.T) {
	registry := NewDefaultRegistry()
	if extensions := registry.PHPExtensions(config.Environment{PHPVersion: "8.4"}); extensions != nil {
		t.Fatalf("PHPExtensions(unconfigured) = %#v, want nil", extensions)
	}

	extensions := registry.PHPExtensions(config.Environment{
		ComposerVersion:   "2.8",
		MySQLVersion:      "8.4",
		MariaDBVersion:    "11.8",
		PostgreSQLVersion: "17",
		SQLiteVersion:     "3.50",
	})
	for _, extension := range []string{"openssl", "zip", "mysqli", "pdo_mysql", "pgsql", "pdo_pgsql", "pdo_sqlite", "sqlite3"} {
		if !extensions[extension] {
			t.Fatalf("PHPExtensions() = %#v, want %s", extensions, extension)
		}
	}
}

func TestParsePluginManifestRejectsUnknownDownloadPlatform(t *testing.T) {
	_, err := parsePluginManifest([]byte(`
id: demo
install-candidates:
  all:
    - bin/demo
download:
  assets:
    darwin-arm64:
      filename: demo.tar.gz
      url: https://example.test/demo.tar.gz
      archive-format: tar.gz
`))
	if err == nil || !strings.Contains(err.Error(), "unsupported platform") {
		t.Fatalf("parsePluginManifest(unknown platform) error = %v, want platform error", err)
	}
}

func TestParsePluginManifestRejectsDeprecatedDownloadCatalog(t *testing.T) {
	_, err := parsePluginManifest([]byte(`
id: demo
install-candidates:
  all:
    - bin/demo
download:
  catalog:
    1.2.3:
      windows-amd64:
        filename: demo.zip
        url: https://example.test/demo.zip
        archive-format: zip
`))
	if err == nil || !strings.Contains(err.Error(), "deprecated download.catalog") {
		t.Fatalf("parsePluginManifest(deprecated catalog) error = %v, want catalog error", err)
	}
}

func TestParsePluginManifestBuildsLogs(t *testing.T) {
	manifest, err := parsePluginManifest([]byte(`
id: demo
install-candidates:
  all:
    - bin/demo
logs:
  info:
    - demo.log
  debug:
    - debug/demo.log
`))
	if err != nil {
		t.Fatalf("parsePluginManifest() error = %v", err)
	}

	plugin, err := manifest.toPlugin(pluginHooks{})
	if err != nil {
		t.Fatalf("toPlugin() error = %v", err)
	}
	want := []LogEntry{
		{Path: "demo.log", Level: LogLevelInfo},
		{Path: "debug/demo.log", Level: LogLevelDebug},
	}
	if !reflect.DeepEqual(plugin.Logs(), want) {
		t.Fatalf("Logs() = %#v, want %#v", plugin.Logs(), want)
	}
}

func TestParsePluginManifestRejectsInvalidLogs(t *testing.T) {
	absolutePath := filepath.ToSlash(filepath.Join(os.TempDir(), "demo.log"))
	tests := []struct {
		name    string
		logYAML string
		wantErr string
	}{
		{
			name: "missing path",
			logYAML: `
  info:
    - ""
`,
			wantErr: "requires path",
		},
		{
			name: "absolute path",
			logYAML: `
  info:
    - ` + absolutePath + `
`,
			wantErr: "must be relative",
		},
		{
			name: "escaping path",
			logYAML: `
  info:
    - ../demo.log
`,
			wantErr: "must stay inside the tool log root",
		},
		{
			name: "empty level",
			logYAML: `
  "":
    - demo.log
`,
			wantErr: "empty level",
		},
		{
			name: "invalid level",
			logYAML: `
  trace:
    - demo.log
`,
			wantErr: "unsupported level",
		},
		{
			name: "empty paths",
			logYAML: `
  info:
`,
			wantErr: "requires at least one path",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parsePluginManifest([]byte(`
id: demo
install-candidates:
  all:
    - bin/demo
logs:` + test.logYAML))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("parsePluginManifest() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestManifestPluginBuildsCandidatesAndDispatch(t *testing.T) {
	manifest, err := parsePluginManifest([]byte(`
id: php
install-candidates:
  all:
    - bin/demo
dispatch-commands:
  - demo
dispatch-candidates:
  demo:
    all:
      - shims/demo
`))
	if err != nil {
		t.Fatalf("parsePluginManifest() error = %v", err)
	}

	plugin, err := manifest.toPlugin(pluginHooks{})
	if err != nil {
		t.Fatalf("toPlugin() error = %v", err)
	}
	if version := plugin.Version(config.Environment{PHPVersion: "8.4"}); version != "8.4" {
		t.Fatalf("Version() = %q, want 8.4", version)
	}

	installCandidates := plugin.InstallCandidates("root", "1.2.3")
	wantInstallCandidates := []string{filepath.Join("root", "php", "1.2.3", "bin", "demo")}
	if !reflect.DeepEqual(installCandidates, wantInstallCandidates) {
		t.Fatalf("InstallCandidates() = %#v, want %#v", installCandidates, wantInstallCandidates)
	}

	dispatchCandidates := plugin.DispatchCandidates("root", "demo", "1.2.3")
	wantDispatchCandidates := []string{filepath.Join("root", "php", "1.2.3", "shims", "demo")}
	if !reflect.DeepEqual(dispatchCandidates, wantDispatchCandidates) {
		t.Fatalf("DispatchCandidates() = %#v, want %#v", dispatchCandidates, wantDispatchCandidates)
	}
}

func TestManifestVersionFuncInfersDatabaseToolVersion(t *testing.T) {
	manifest, err := parsePluginManifest([]byte(`
id: mariadb
install-candidates:
  all:
    - bin/mariadb
`))
	if err != nil {
		t.Fatalf("parsePluginManifest() error = %v", err)
	}

	plugin, err := manifest.toPlugin(pluginHooks{})
	if err != nil {
		t.Fatalf("toPlugin() error = %v", err)
	}
	version := plugin.Version(config.Environment{
		MariaDBVersion: "11.8",
		Database:       &config.DatabaseConfig{Engine: MariaDB, Version: "11.8"},
	})
	if version != "11.8" {
		t.Fatalf("Version() = %q, want 11.8", version)
	}
}

func TestManifestDownloadTemplateResolvesPlatform(t *testing.T) {
	manifest, err := parsePluginManifest([]byte(`
id: demo
install-candidates:
  all:
    - bin/demo
download:
  assets:
    windows-amd64:
      filename: demo-{version}.zip
      url: https://example.test/{tag}/demo-{version}.zip
      checksum-url: https://example.test/{tag}/checksums.txt
      checksum-algorithm: md5
      archive-format: zip
`))
	if err != nil {
		t.Fatalf("parsePluginManifest() error = %v", err)
	}

	asset, err := resolveManifestDownloadAsset("demo", manifest.Download.Assets, "1.2", "1.2.3", "v1.2.3", "windows", "amd64", nil)
	if err != nil {
		t.Fatalf("resolveManifestDownloadAsset() error = %v", err)
	}
	if asset.FileName != "demo-1.2.3.zip" || asset.ChecksumAlgorithm != checksumAlgorithmMD5 || asset.ArchiveFormat != archiveFormatZip {
		t.Fatalf("asset = %#v, want rendered manifest archive asset", asset)
	}
	if asset.URL != "https://example.test/v1.2.3/demo-1.2.3.zip" || asset.ChecksumURL != "https://example.test/v1.2.3/checksums.txt" {
		t.Fatalf("asset URLs = (%q, %q), want rendered template URLs", asset.URL, asset.ChecksumURL)
	}
}
