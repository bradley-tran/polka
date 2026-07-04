package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"polka/config"
)

// TestRoadRunnerPluginUsesConfiguredVersionAndManifest verifies the RoadRunner
// plugin reads its version from the environment and exposes the rr shim and log.
func TestRoadRunnerPluginUsesConfiguredVersionAndManifest(t *testing.T) {
	plugin := roadRunnerPlugin()
	if version := plugin.Version(config.Environment{RoadRunnerVersion: "2024.1.5"}); version != "2024.1.5" {
		t.Fatalf("Version() = %q, want 2024.1.5", version)
	}
	if commands := plugin.DispatchCommands(); len(commands) != 1 || commands[0] != "rr" {
		t.Fatalf("DispatchCommands() = %#v, want rr", commands)
	}
	if logs := plugin.Logs(); len(logs) != 1 || logs[0].Path != "serve.log" || logs[0].Level != LogLevelDebug {
		t.Fatalf("Logs() = %#v, want debug serve.log", logs)
	}
}

// TestRoadRunnerManifestResolvesOfficialAssets verifies the manifest substitutes
// the version and tag into the official RoadRunner archive names per platform.
func TestRoadRunnerManifestResolvesOfficialAssets(t *testing.T) {
	manifest, err := loadBuiltinManifest(RoadRunner)
	if err != nil {
		t.Fatalf("loadBuiltinManifest() error = %v", err)
	}

	windowsAsset, err := resolveManifestDownloadAsset(RoadRunner, manifest.Download.Assets, "2024.1.5", "2024.1.5", "v2024.1.5", "windows", "amd64", nil)
	if err != nil {
		t.Fatalf("resolveManifestDownloadAsset(windows) error = %v", err)
	}
	if windowsAsset.FileName != "roadrunner-2024.1.5-windows-amd64.zip" || windowsAsset.ArchiveFormat != archiveFormatZip {
		t.Fatalf("windows asset = %#v, want versioned zip archive", windowsAsset)
	}

	linuxAsset, err := resolveManifestDownloadAsset(RoadRunner, manifest.Download.Assets, "2024.1.5", "2024.1.5", "v2024.1.5", "linux", "amd64", nil)
	if err != nil {
		t.Fatalf("resolveManifestDownloadAsset(linux) error = %v", err)
	}
	if linuxAsset.FileName != "roadrunner-2024.1.5-linux-amd64.tar.gz" || linuxAsset.ArchiveFormat != archiveFormatTarGz {
		t.Fatalf("linux asset = %#v, want versioned tar.gz archive", linuxAsset)
	}
}

// TestRoadRunnerPluginValidatesVersion checks that an empty version is accepted
// while a malformed version is rejected.
func TestRoadRunnerPluginValidatesVersion(t *testing.T) {
	plugin := roadRunnerPlugin()
	if err := plugin.Validate(config.Environment{}); err != nil {
		t.Fatalf("Validate(empty) error = %v, want nil", err)
	}
	if err := plugin.Validate(config.Environment{RoadRunnerVersion: "bad version"}); err == nil {
		t.Fatal("Validate(invalid) error = nil, want error")
	}
}

// TestRoadRunnerPostInstallMakesBinaryExecutable verifies the post-install hook
// marks the extracted binary executable on Unix platforms.
func TestRoadRunnerPostInstallMakesBinaryExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not use Unix executable permission bits")
	}

	target := filepath.Join(t.TempDir(), "rr")
	if err := os.WriteFile(target, []byte("binary"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	plugin := roadRunnerPlugin()
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
