package tools

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"polka/config"
)

func TestBuiltinManifestsLoad(t *testing.T) {
	for _, tool := range []string{PHP, Composer, NodeJS, Mago, Nginx, Mailpit, PHPMyAdmin, MySQL, MariaDB} {
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
version:
  source: php
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
version:
  source: php
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
version:
  source: php
install-candidates:
  all:
    - bin/demo
download:
  catalog:
    1.2.3:
      darwin-arm64:
        filename: demo.tar.gz
        url: https://example.test/demo.tar.gz
        archive-format: tar.gz
`))
	if err == nil || !strings.Contains(err.Error(), "unsupported platform") {
		t.Fatalf("parsePluginManifest(unknown platform) error = %v, want platform error", err)
	}
}

func TestManifestPluginBuildsCandidatesAndDispatch(t *testing.T) {
	manifest, err := parsePluginManifest([]byte(`
id: demo
version:
  source: php
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
	wantInstallCandidates := []string{filepath.Join("root", "demo", "1.2.3", "bin", "demo")}
	if !reflect.DeepEqual(installCandidates, wantInstallCandidates) {
		t.Fatalf("InstallCandidates() = %#v, want %#v", installCandidates, wantInstallCandidates)
	}

	dispatchCandidates := plugin.DispatchCandidates("root", "demo", "1.2.3")
	wantDispatchCandidates := []string{filepath.Join("root", "demo", "1.2.3", "shims", "demo")}
	if !reflect.DeepEqual(dispatchCandidates, wantDispatchCandidates) {
		t.Fatalf("DispatchCandidates() = %#v, want %#v", dispatchCandidates, wantDispatchCandidates)
	}
}

func TestManifestDownloadCatalogResolvesVersionAndPlatform(t *testing.T) {
	manifest, err := parsePluginManifest([]byte(`
id: demo
version:
  source: php
install-candidates:
  all:
    - bin/demo
download:
  catalog:
    1.2.3:
      windows-amd64:
        filename: demo.zip
        url: https://example.test/demo.zip
        checksum: 0123456789abcdef0123456789abcdef
        checksum-algorithm: md5
        archive-format: zip
`))
	if err != nil {
		t.Fatalf("parsePluginManifest() error = %v", err)
	}

	resolvedVersion, asset, err := resolveDownloadCatalogAsset("demo", manifest.Download.Catalog, "1.2", "windows", "amd64")
	if err != nil {
		t.Fatalf("resolveDownloadCatalogAsset() error = %v", err)
	}
	if resolvedVersion != "1.2.3" {
		t.Fatalf("resolved version = %q, want 1.2.3", resolvedVersion)
	}
	if asset.FileName != "demo.zip" || asset.ChecksumAlgorithm != checksumAlgorithmMD5 || asset.ArchiveFormat != archiveFormatZip {
		t.Fatalf("asset = %#v, want manifest archive asset", asset)
	}
}
