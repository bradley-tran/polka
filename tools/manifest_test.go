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
	for _, tool := range []string{PHP, Composer, PIE, NodeJS, Mago, Nginx, Mailpit, PHPMyAdmin, MySQL, MariaDB, SQLite} {
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
