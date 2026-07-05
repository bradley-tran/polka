package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"polka/config"
)

// TestRedisPluginUsesConfiguredVersionAndManifest verifies the Redis plugin
// reads its version from the environment and exposes both managed binaries and
// its log.
func TestRedisPluginUsesConfiguredVersionAndManifest(t *testing.T) {
	plugin := redisPlugin()
	if version := plugin.Version(config.Environment{Redis: &config.RedisConfig{Version: "8.8.0"}}); version != "8.8.0" {
		t.Fatalf("Version() = %q, want 8.8.0", version)
	}
	if commands := plugin.DispatchCommands(); !reflect.DeepEqual(commands, []string{"redis-server", "redis-cli"}) {
		t.Fatalf("DispatchCommands() = %#v, want redis-server and redis-cli", commands)
	}
	if logs := plugin.Logs(); len(logs) != 1 || logs[0].Path != "redis.log" || logs[0].Level != LogLevelInfo {
		t.Fatalf("Logs() = %#v, want info redis.log", logs)
	}
}

// TestRedisManifestResolvesWindowsSourceArchive verifies the manifest points
// Windows installs at the community redis-windows source-archive zip whose tag
// equals the requested version.
func TestRedisManifestResolvesWindowsSourceArchive(t *testing.T) {
	manifest, err := loadBuiltinManifest(Redis)
	if err != nil {
		t.Fatalf("loadBuiltinManifest() error = %v", err)
	}

	windowsAsset, err := resolveManifestDownloadAsset(Redis, manifest.Download.Assets, "8.8.0", "8.8.0", "8.8.0", "windows", "amd64", nil)
	if err != nil {
		t.Fatalf("resolveManifestDownloadAsset(windows) error = %v", err)
	}
	if windowsAsset.FileName != "redis-windows-8.8.0.zip" || windowsAsset.ArchiveFormat != archiveFormatZip {
		t.Fatalf("windows asset = %#v, want versioned zip archive", windowsAsset)
	}
	if windowsAsset.URL != "https://github.com/zkteco-home/redis-windows/archive/refs/tags/8.8.0.zip" {
		t.Fatalf("windows asset url = %q, want tag source archive", windowsAsset.URL)
	}
	if windowsAsset.ChecksumAlgorithm != checksumAlgorithmNone {
		t.Fatalf("windows asset checksum = %q, want none", windowsAsset.ChecksumAlgorithm)
	}

	// Redis is Windows-only for now, so the linux platform has no download asset.
	if _, err := resolveManifestDownloadAsset(Redis, manifest.Download.Assets, "8.8.0", "8.8.0", "8.8.0", "linux", "amd64", nil); err == nil {
		t.Fatal("resolveManifestDownloadAsset(linux) error = nil, want unavailable platform error")
	}
}

// TestRedisManifestDispatchCandidatesResolveCLI verifies redis-server resolves
// the install candidate while redis-cli resolves its dedicated dispatch
// candidate.
func TestRedisManifestDispatchCandidatesResolveCLI(t *testing.T) {
	plugin := redisPlugin()

	install := plugin.InstallCandidates("root", "8.8.0")
	wantInstall := []string{filepath.Join("root", "redis", "8.8.0", redisServerBinary())}
	if !reflect.DeepEqual(install, wantInstall) {
		t.Fatalf("InstallCandidates() = %#v, want %#v", install, wantInstall)
	}

	cli := plugin.DispatchCandidates("root", "redis-cli", "8.8.0")
	wantCLI := []string{filepath.Join("root", "redis", "8.8.0", redisCLIBinary())}
	if !reflect.DeepEqual(cli, wantCLI) {
		t.Fatalf("DispatchCandidates(redis-cli) = %#v, want %#v", cli, wantCLI)
	}
}

// TestRedisPluginValidatesVersion checks that an empty version is accepted while
// a malformed version and an out-of-range port are rejected.
func TestRedisPluginValidatesVersion(t *testing.T) {
	plugin := redisPlugin()
	if err := plugin.Validate(config.Environment{}); err != nil {
		t.Fatalf("Validate(empty) error = %v, want nil", err)
	}
	if err := plugin.Validate(config.Environment{Redis: &config.RedisConfig{Version: "bad version"}}); err == nil {
		t.Fatal("Validate(invalid version) error = nil, want error")
	}
	if err := plugin.Validate(config.Environment{Redis: &config.RedisConfig{Version: "8.8.0", Port: 70000}}); err == nil {
		t.Fatal("Validate(invalid port) error = nil, want error")
	}
}

// TestRedisPostInstallMakesBothBinariesExecutable verifies the post-install hook
// marks the extracted redis-server and redis-cli binaries executable on Unix.
func TestRedisPostInstallMakesBothBinariesExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not use Unix executable permission bits")
	}

	installDir := t.TempDir()
	server := filepath.Join(installDir, "redis-server")
	cli := filepath.Join(installDir, "redis-cli")
	for _, path := range []string{server, cli} {
		if err := os.WriteFile(path, []byte("binary"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	plugin := redisPlugin()
	if err := plugin.PostInstall(InstallContext{Result: InstallResult{TargetPath: server}}); err != nil {
		t.Fatalf("PostInstall() error = %v", err)
	}
	for _, path := range []string{server, cli} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%s) error = %v", path, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("installed mode for %s = %o, want executable", path, info.Mode().Perm())
		}
	}
}

func redisServerBinary() string {
	if runtime.GOOS == "windows" {
		return "redis-server.exe"
	}

	return "redis-server"
}

func redisCLIBinary() string {
	if runtime.GOOS == "windows" {
		return "redis-cli.exe"
	}

	return "redis-cli"
}
