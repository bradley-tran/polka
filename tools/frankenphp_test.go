package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"polka/config"
)

func TestFrankenPHPPluginUsesConfiguredVersionAndManifest(t *testing.T) {
	plugin := frankenPHPPlugin()
	if version := plugin.Version(config.Environment{FrankenPHPVersion: "1.12"}); version != "1.12" {
		t.Fatalf("Version() = %q, want 1.12", version)
	}
	if commands := plugin.DispatchCommands(); len(commands) != 1 || commands[0] != FrankenPHP {
		t.Fatalf("DispatchCommands() = %#v, want frankenphp", commands)
	}
	if logs := plugin.Logs(); len(logs) != 1 || logs[0].Path != "serve.log" || logs[0].Level != LogLevelDebug {
		t.Fatalf("Logs() = %#v, want debug serve.log", logs)
	}
}

func TestFrankenPHPManifestResolvesOfficialAssets(t *testing.T) {
	manifest, err := loadBuiltinManifest(FrankenPHP)
	if err != nil {
		t.Fatalf("loadBuiltinManifest() error = %v", err)
	}

	windowsAsset, err := resolveManifestDownloadAsset(FrankenPHP, manifest.Download.Assets, "1.12", "1.12.4", "v1.12.4", "windows", "amd64", nil)
	if err != nil {
		t.Fatalf("resolveManifestDownloadAsset(windows) error = %v", err)
	}
	if windowsAsset.FileName != "frankenphp-windows-x86_64.zip" || windowsAsset.ArchiveFormat != archiveFormatZip {
		t.Fatalf("windows asset = %#v, want official x86_64 zip", windowsAsset)
	}

	linuxAsset, err := resolveManifestDownloadAsset(FrankenPHP, manifest.Download.Assets, "1.12", "1.12.4", "v1.12.4", "linux", "amd64", nil)
	if err != nil {
		t.Fatalf("resolveManifestDownloadAsset(linux) error = %v", err)
	}
	if linuxAsset.FileName != "frankenphp-linux-x86_64" || linuxAsset.InstallPath != "frankenphp" {
		t.Fatalf("linux asset = %#v, want official static binary", linuxAsset)
	}
}

func TestFrankenPHPPostInstallMakesBinaryExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not use Unix executable permission bits")
	}

	target := filepath.Join(t.TempDir(), "frankenphp")
	if err := os.WriteFile(target, []byte("binary"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	plugin := frankenPHPPlugin()
	if err := plugin.PostInstall(InstallContext{Result: InstallResult{TargetPath: target}}); err != nil {
		t.Fatalf("PostInstall() error = %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed mode = %o, want executable", info.Mode().Perm())
	}
}

func TestConfigureInstalledWindowsFrankenPHPWritesPHPIni(t *testing.T) {
	installDir := t.TempDir()
	frankenPHPTarget := filepath.Join(installDir, "frankenphp")
	if runtime.GOOS == "windows" {
		frankenPHPTarget += ".exe"
	}
	if err := os.WriteFile(frankenPHPTarget, []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile(frankenphp) error = %v", err)
	}
	writeFakeFrankenPHPPHP(t, installDir)

	err := configureInstalledWindowsFrankenPHP(InstallContext{
		EnvsDir: filepath.Dir(filepath.Dir(installDir)),
		Environment: config.Environment{
			PHPExtensions: map[string]bool{"mbstring": true},
		},
		Result: InstallResult{
			Tool:       FrankenPHP,
			Version:    "1.12",
			TargetPath: frankenPHPTarget,
		},
	})
	if err != nil {
		t.Fatalf("configureInstalledWindowsFrankenPHP() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(installDir, "php.ini"))
	if err != nil {
		t.Fatalf("ReadFile(php.ini) error = %v", err)
	}
	ini := string(data)
	if !strings.Contains(ini, "extension_dir=\"ext\"") || !strings.Contains(ini, "extension=mbstring") {
		t.Fatalf("php.ini = %q, want bundled extension configuration", ini)
	}
}

func writeFakeFrankenPHPPHP(t *testing.T, installDir string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		path := filepath.Join(installDir, "php.cmd")
		contents := "@echo off\r\nif \"%1\"==\"-nm\" (\r\n  echo [PHP Modules]\r\n  echo Core\r\n  exit /b 0\r\n)\r\nexit /b 1\r\n"
		if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
			t.Fatalf("WriteFile(php.cmd) error = %v", err)
		}
		return
	}

	path := filepath.Join(installDir, "php")
	contents := "#!/bin/sh\nif [ \"$1\" = \"-nm\" ]; then\n  printf '[PHP Modules]\\nCore\\n'\n  exit 0\nfi\nexit 1\n"
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("WriteFile(php) error = %v", err)
	}
}
