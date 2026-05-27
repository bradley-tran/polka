package backend

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStoreInitInstallsDispatcherShimsWithoutToolShims(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")

	if err := store.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	assertPathExists(t, store.RootDir)
	assertPathExists(t, store.BinDir)
	assertPathExists(t, store.ConfigFile)
	assertPathExists(t, filepath.Join(store.BinDir, dispatcherBinaryName))
	assertPathExists(t, filepath.Join(store.BinDir, dispatcherBatchFileName))
	assertPathExists(t, filepath.Join(store.RootDir, sessionStartFileName))
	assertPathExists(t, filepath.Join(store.RootDir, sessionStopFileName))
	assertPathExists(t, filepath.Join(store.RootDir, sessionStartFileName+powerShellExtension))
	assertPathExists(t, filepath.Join(store.RootDir, sessionStopFileName+powerShellExtension))
	assertPathMissing(t, filepath.Join(store.BinDir, "php"))
	assertPathMissing(t, filepath.Join(store.BinDir, "php.cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolComposer))
	assertPathMissing(t, filepath.Join(store.BinDir, toolComposer+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNode))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNode+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNPM))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNPM+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNPX))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNPX+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNodeJS))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNodeJS+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolMySQL))
	assertPathMissing(t, filepath.Join(store.BinDir, toolMySQL+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolMariaDB))
	assertPathMissing(t, filepath.Join(store.BinDir, toolMariaDB+".cmd"))

	configData, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	if !strings.Contains(string(configData), "root: .polka") {
		t.Fatalf("config contents = %q, want root entry for .polka", string(configData))
	}
	dispatcherShim, err := os.ReadFile(filepath.Join(store.BinDir, dispatcherBinaryName))
	if err != nil {
		t.Fatalf("ReadFile(dispatcher shim) error = %v", err)
	}
	selfPath, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable() error = %v", err)
	}
	if !strings.Contains(string(dispatcherShim), "POLKA_DISPATCHER") {
		t.Fatalf("dispatcher shim = %q, want POLKA_DISPATCHER override support", string(dispatcherShim))
	}
	if !strings.Contains(string(dispatcherShim), filepath.ToSlash(selfPath)) {
		t.Fatalf("dispatcher shim = %q, want current executable path %q", string(dispatcherShim), filepath.ToSlash(selfPath))
	}
	shellSessionStart, err := os.ReadFile(filepath.Join(store.RootDir, sessionStartFileName))
	if err != nil {
		t.Fatalf("ReadFile(session-start) error = %v", err)
	}
	if !strings.Contains(string(shellSessionStart), filepath.ToSlash(filepath.Join(store.RootDir, binDirectoryName, dispatcherBinaryName))) {
		t.Fatalf("session-start = %q, want dispatcher path", string(shellSessionStart))
	}
	if !strings.Contains(string(shellSessionStart), "session start") {
		t.Fatalf("session-start = %q, want session start invocation", string(shellSessionStart))
	}
	shellSessionStop, err := os.ReadFile(filepath.Join(store.RootDir, sessionStopFileName))
	if err != nil {
		t.Fatalf("ReadFile(session-stop) error = %v", err)
	}
	if !strings.Contains(string(shellSessionStop), "session stop") {
		t.Fatalf("session-stop = %q, want session stop invocation", string(shellSessionStop))
	}
	powerShellSessionStart, err := os.ReadFile(filepath.Join(store.RootDir, sessionStartFileName+powerShellExtension))
	if err != nil {
		t.Fatalf("ReadFile(session-start.ps1) error = %v", err)
	}
	if !strings.Contains(string(powerShellSessionStart), filepath.Join(store.RootDir, binDirectoryName, dispatcherBatchFileName)) {
		t.Fatalf("session-start.ps1 = %q, want dispatcher batch path", string(powerShellSessionStart))
	}
	if !strings.Contains(string(powerShellSessionStart), "session start") {
		t.Fatalf("session-start.ps1 = %q, want session start invocation", string(powerShellSessionStart))
	}
	powerShellSessionStop, err := os.ReadFile(filepath.Join(store.RootDir, sessionStopFileName+powerShellExtension))
	if err != nil {
		t.Fatalf("ReadFile(session-stop.ps1) error = %v", err)
	}
	if !strings.Contains(string(powerShellSessionStop), "session stop") {
		t.Fatalf("session-stop.ps1 = %q, want session stop invocation", string(powerShellSessionStop))
	}
}

func TestStoreUseSyncsManagedBinariesForCurrentEnvironment(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")

	if _, err := store.Configure("php-only", "8.4", "", nil); err != nil {
		t.Fatalf("Configure(php-only) error = %v", err)
	}
	if _, err := store.Configure("db-only", "", "", &DatabaseConfig{Engine: toolMariaDB, Version: "11.4"}); err != nil {
		t.Fatalf("Configure(db-only) error = %v", err)
	}

	if err := store.Use("php-only"); err != nil {
		t.Fatalf("Use(php-only) error = %v", err)
	}
	assertPathExists(t, filepath.Join(store.BinDir, toolPHP))
	assertPathExists(t, filepath.Join(store.BinDir, toolPHP+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolComposer))
	assertPathMissing(t, filepath.Join(store.BinDir, toolComposer+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNode))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNode+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNPM))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNPM+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNPX))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNPX+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolMySQL))
	assertPathMissing(t, filepath.Join(store.BinDir, toolMySQL+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolMariaDB))
	assertPathMissing(t, filepath.Join(store.BinDir, toolMariaDB+".cmd"))

	if err := store.Use("db-only"); err != nil {
		t.Fatalf("Use(db-only) error = %v", err)
	}
	assertPathMissing(t, filepath.Join(store.BinDir, toolPHP))
	assertPathMissing(t, filepath.Join(store.BinDir, toolPHP+".cmd"))
	assertPathExists(t, filepath.Join(store.BinDir, toolMariaDB))
	assertPathExists(t, filepath.Join(store.BinDir, toolMariaDB+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolMySQL))
	assertPathMissing(t, filepath.Join(store.BinDir, toolMySQL+".cmd"))
}

func TestStoreUsePreservesExistingConfigComments(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	configData := []byte(strings.Join([]string{
		"version: 1",
		"root: .polka",
		"current: demo # active environment",
		"environments:",
		"  demo:",
		"    php: \"8.4\"",
		"  web:",
		"    php: \"8.3\"",
		"",
	}, "\n"))
	if err := os.WriteFile(store.ConfigFile, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if err := store.Use("web"); err != nil {
		t.Fatalf("Use(web) error = %v", err)
	}

	updatedConfig, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	if !strings.Contains(string(updatedConfig), "# active environment") {
		t.Fatalf("config after Use() = %q, want current comment preserved", string(updatedConfig))
	}
	config, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if config.Current != "web" {
		t.Fatalf("config.Current = %q, want web", config.Current)
	}
}

func TestStoreInstallCopiesToolIntoVersionedLayout(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	cachePHP := writeCachedTool(t, store.CacheDir, toolPHP, "8.4")

	if _, err := store.Configure("demo", "8.4", "", nil); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}

	results, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Install(demo) length = %d, want 1", len(results))
	}
	result := results[0]
	if result.Tool != toolPHP || result.Version != "8.4" {
		t.Fatalf("Install(demo) = %#v, want tool/version populated", result)
	}
	if result.Downloaded {
		t.Fatalf("Install(demo) Downloaded = true, want cache hit")
	}
	if result.CachePath != cachePHP {
		t.Fatalf("Install(demo) CachePath = %q, want %q", result.CachePath, cachePHP)
	}
	assertPathExists(t, result.TargetPath)
	if !strings.Contains(result.TargetPath, filepath.Join("envs", "php", "8.4")) {
		t.Fatalf("Install(demo) target = %q, want versioned env path", result.TargetPath)
	}
}

func TestStoreInstallPreservesExistingConfigComments(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")

	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")
	configData := []byte(strings.Join([]string{
		"# keep this comment",
		"version: 1",
		"root: .polka",
		"current: demo",
		"environments:",
		"  demo:",
		"    php: \"8.4\"",
		"",
	}, "\n"))
	if err := os.WriteFile(store.ConfigFile, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	if _, err := store.Install("demo"); err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}

	updatedConfig, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	if string(updatedConfig) != string(configData) {
		t.Fatalf("config after install = %q, want original bytes %q", string(updatedConfig), string(configData))
	}
}

func TestStoreConfigurePreservesExistingConfigComments(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	configData := []byte(strings.Join([]string{
		"# top comment",
		"version: 1",
		"root: .polka",
		"current: demo",
		"environments:",
		"  demo:",
		"    # php version comment",
		"    php: \"8.4\" # inline php comment",
		"",
	}, "\n"))
	if err := os.WriteFile(store.ConfigFile, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	if _, err := store.Configure("demo", "8.3", "", nil); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}

	updatedConfig, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	for _, comment := range []string{"# top comment", "# php version comment", "# inline php comment"} {
		if !strings.Contains(string(updatedConfig), comment) {
			t.Fatalf("config after Configure() = %q, want preserved comment %q", string(updatedConfig), comment)
		}
	}
	config, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if config.Environments["demo"].PHPVersion != "8.3" {
		t.Fatalf("config.Environments[demo].PHPVersion = %q, want 8.3", config.Environments["demo"].PHPVersion)
	}
}

func TestStoreInstallDownloadsWhenCacheMissing(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	store.Downloader = fakeDownloader(func(cacheDir, tool, version string) error {
		_ = writeCachedTool(t, cacheDir, tool, version)
		return nil
	})

	if _, err := store.Configure("demo", "8.4", "2.8", nil); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}

	results, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Install(demo) length = %d, want 2", len(results))
	}
	for _, result := range results {
		if !result.Downloaded {
			t.Fatalf("Install(demo) result = %#v, want downloaded=true after cache miss", result)
		}
		assertPathExists(t, result.TargetPath)
	}
}

func TestStoreInstallWithProgressReportsStages(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")
	store.Downloader = fakeDownloader(func(cacheDir, tool, version string) error {
		_ = writeCachedTool(t, cacheDir, tool, version)
		return nil
	})

	if _, err := store.Configure("demo", "8.4", "", &DatabaseConfig{Engine: toolMySQL, Version: "8.4"}); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}

	progressEvents := make([]string, 0, 6)
	if _, err := store.InstallWithProgress("demo", func(progress InstallProgress) {
		progressEvents = append(progressEvents, fmt.Sprintf("%d/%d %s %s %s", progress.Index, progress.Total, progress.Tool, progress.Version, progress.Stage))
	}); err != nil {
		t.Fatalf("InstallWithProgress(demo) error = %v", err)
	}

	want := []string{
		"1/2 php 8.4 using cache",
		"1/2 php 8.4 installing",
		"1/2 php 8.4 installed",
		"2/2 mysql 8.4 downloading",
		"2/2 mysql 8.4 installing",
		"2/2 mysql 8.4 installed",
	}
	if len(progressEvents) != len(want) {
		t.Fatalf("InstallWithProgress(demo) events = %#v, want %#v", progressEvents, want)
	}
	for index, expected := range want {
		if progressEvents[index] != expected {
			t.Fatalf("InstallWithProgress(demo) events = %#v, want %#v", progressEvents, want)
		}
	}
}

func TestStoreInstallDownloadsConfiguredDatabase(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	store.Downloader = fakeDownloader(func(cacheDir, tool, version string) error {
		_ = writeCachedTool(t, cacheDir, tool, version)
		return nil
	})

	if _, err := store.Configure("demo", "", "", &DatabaseConfig{Engine: toolMySQL, Version: "8.4"}); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}

	results, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Install(demo) length = %d, want 1", len(results))
	}
	result := results[0]
	if result.Tool != toolMySQL || result.Version != "8.4" {
		t.Fatalf("Install(demo) result = %#v, want mysql 8.4", result)
	}
	if !result.Downloaded {
		t.Fatalf("Install(demo) Downloaded = false, want true after cache miss")
	}
	assertPathExists(t, result.TargetPath)
	if !strings.Contains(result.TargetPath, filepath.Join("envs", toolMySQL, "8.4")) {
		t.Fatalf("Install(demo) target = %q, want versioned mysql env path", result.TargetPath)
	}
	if _, err := store.ResolveTool(toolMySQL); err == nil {
		t.Fatal("ResolveTool(mysql) error = nil, want no active environment selected")
	}

	if err := store.Use("demo"); err != nil {
		t.Fatalf("Use(demo) error = %v", err)
	}

	resolvedPath, err := store.ResolveTool(toolMySQL)
	if err != nil {
		t.Fatalf("ResolveTool(mysql) error = %v", err)
	}
	if resolvedPath != result.TargetPath {
		t.Fatalf("ResolveTool(mysql) = %q, want %q", resolvedPath, result.TargetPath)
	}
}

func TestStoreInstallDownloadsConfiguredNginx(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	store.Downloader = fakeDownloader(func(cacheDir, tool, version string) error {
		_ = writeCachedTool(t, cacheDir, tool, version)
		return nil
	})

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{NginxVersion: "1.30"}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	results, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Install(demo) length = %d, want 1", len(results))
	}
	result := results[0]
	if result.Tool != toolNginx || result.Version != "1.30" {
		t.Fatalf("Install(demo) result = %#v, want nginx 1.30", result)
	}
	if !result.Downloaded {
		t.Fatalf("Install(demo) Downloaded = false, want true after cache miss")
	}
	assertPathExists(t, result.TargetPath)
	if !strings.Contains(result.TargetPath, filepath.Join("envs", toolNginx, "1.30")) {
		t.Fatalf("Install(demo) target = %q, want versioned nginx env path", result.TargetPath)
	}

	if err := store.Use("demo"); err != nil {
		t.Fatalf("Use(demo) error = %v", err)
	}
	resolvedPath, err := store.ResolveTool(toolNginx)
	if err != nil {
		t.Fatalf("ResolveTool(nginx) error = %v", err)
	}
	if resolvedPath != result.TargetPath {
		t.Fatalf("ResolveTool(nginx) = %q, want %q", resolvedPath, result.TargetPath)
	}
	assertPathExists(t, filepath.Join(store.BinDir, toolNginx))
	assertPathExists(t, filepath.Join(store.BinDir, toolNginx+".cmd"))
}

func TestStoreInstallCopiesConfiguredNodeJSIntoVersionedLayout(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	cacheNodeJS := writeCachedTool(t, store.CacheDir, toolNodeJS, "24")
	writeCachedNodeJSCommand(t, store.CacheDir, "24", toolNPM)
	writeCachedNodeJSCommand(t, store.CacheDir, "24", toolNPX)
	writeCachedNodeJSCommand(t, store.CacheDir, "24", toolNode)

	config := store.defaultConfig()
	config.Current = "demo"
	config.Environments["demo"] = Environment{NodeJSVersion: "24"}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	results, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Install(demo) length = %d, want 1", len(results))
	}

	result := results[0]
	if result.Tool != toolNodeJS || result.Version != "24" {
		t.Fatalf("Install(demo) result = %#v, want nodejs 24", result)
	}
	if result.CachePath != cacheNodeJS {
		t.Fatalf("Install(demo) CachePath = %q, want %q", result.CachePath, cacheNodeJS)
	}
	assertPathExists(t, result.TargetPath)
	if !strings.Contains(result.TargetPath, filepath.Join("envs", toolNodeJS, "24")) {
		t.Fatalf("Install(demo) target = %q, want versioned nodejs env path", result.TargetPath)
	}

	for _, tool := range []string{toolNode, toolNPM, toolNPX} {
		resolvedPath, err := store.ResolveTool(tool)
		if err != nil {
			t.Fatalf("ResolveTool(%s) error = %v", tool, err)
		}
		assertPathExists(t, resolvedPath)
		if !strings.Contains(resolvedPath, filepath.Join("envs", toolNodeJS, "24")) {
			t.Fatalf("ResolveTool(%s) = %q, want versioned nodejs env path", tool, resolvedPath)
		}
	}
	if _, err := store.ResolveTool(toolNodeJS); err == nil {
		t.Fatal("ResolveTool(nodejs) error = nil, want unsupported tool error")
	}

	assertPathExists(t, filepath.Join(store.BinDir, toolNode))
	assertPathExists(t, filepath.Join(store.BinDir, toolNode+".cmd"))
	assertPathExists(t, filepath.Join(store.BinDir, toolNPM))
	assertPathExists(t, filepath.Join(store.BinDir, toolNPM+".cmd"))
	assertPathExists(t, filepath.Join(store.BinDir, toolNPX))
	assertPathExists(t, filepath.Join(store.BinDir, toolNPX+".cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNodeJS))
	assertPathMissing(t, filepath.Join(store.BinDir, toolNodeJS+".cmd"))
}

func TestWindowsDispatchBinarySupportsCopiedShimWithInheritedDispatcher(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only shim regression")
	}

	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")

	if _, err := store.ConfigureWithNodeJS("demo", "", "", "24", nil); err != nil {
		t.Fatalf("ConfigureWithNodeJS(demo) error = %v", err)
	}
	if err := store.Use("demo"); err != nil {
		t.Fatalf("Use(demo) error = %v", err)
	}

	originalShimPath := filepath.Join(store.BinDir, toolNPX+".cmd")
	originalShimData, err := os.ReadFile(originalShimPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", originalShimPath, err)
	}

	copiedShimDir := filepath.Join(projectDir, "copied-shims")
	if err := os.MkdirAll(copiedShimDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(copied-shims) error = %v", err)
	}
	copiedShimPath := filepath.Join(copiedShimDir, toolNPX+".cmd")
	if err := os.WriteFile(copiedShimPath, originalShimData, 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", copiedShimPath, err)
	}

	capturePath := filepath.Join(projectDir, "dispatch-capture.txt")
	fakeDispatcherPath := filepath.Join(projectDir, "fake-dispatcher.cmd")
	fakeDispatcherData := "@echo off\r\n" +
		"> \"%POLKA_TEST_CAPTURE%\" echo args:%*\r\n" +
		">> \"%POLKA_TEST_CAPTURE%\" echo dispatcher:%POLKA_TOOL_DISPATCHER%\r\n" +
		">> \"%POLKA_TEST_CAPTURE%\" echo root:%POLKA_TOOL_ROOT%\r\n" +
		"exit /b 0\r\n"
	if err := os.WriteFile(fakeDispatcherPath, []byte(fakeDispatcherData), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", fakeDispatcherPath, err)
	}

	command := exec.Command("cmd.exe", "/c", copiedShimPath, "create-vite")
	command.Dir = projectDir
	command.Env = append(os.Environ(),
		"POLKA_TOOL_DISPATCHER="+fakeDispatcherPath,
		"POLKA_TOOL_ROOT="+store.RootDir,
		"POLKA_TEST_CAPTURE="+capturePath,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("copied shim output = %q, error = %v", string(output), err)
	}

	captureData, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", capturePath, err)
	}
	capture := string(captureData)
	if !strings.Contains(capture, "args:--root \""+store.RootDir+"\" dispatch npx create-vite") {
		t.Fatalf("capture = %q, want copied shim to dispatch through inherited root", capture)
	}
	if !strings.Contains(capture, "dispatcher:"+fakeDispatcherPath) {
		t.Fatalf("capture = %q, want inherited dispatcher path", capture)
	}
	if !strings.Contains(capture, "root:"+store.RootDir) {
		t.Fatalf("capture = %q, want inherited root path", capture)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("Stat(%s) error = nil, want path to be missing", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("Stat(%s) error = %v, want not exists", path, err)
	}
}

func TestStoreInstallWritesPHPExtensionConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")
	if err := os.MkdirAll(filepath.Join(store.CacheDir, toolPHP, "8.4", "ext"), 0o755); err != nil {
		t.Fatalf("MkdirAll(ext) error = %v", err)
	}

	if _, err := store.Configure("demo", "8.4", "", nil); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}

	config, err := store.loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	environment := config.Environments["demo"]
	environment.PHPExtensions = map[string]bool{" OpenSSL ": true, "xdebug": false}
	config.Environments["demo"] = environment
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	results, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Install(demo) length = %d, want 1", len(results))
	}

	phpIniPath := filepath.Join(filepath.Dir(results[0].TargetPath), "php.ini")
	phpIniData, err := os.ReadFile(phpIniPath)
	if err != nil {
		t.Fatalf("ReadFile(php.ini) error = %v", err)
	}
	phpIni := string(phpIniData)
	if !strings.Contains(phpIni, "extension_dir=\"../ext\"") {
		t.Fatalf("php.ini = %q, want extension_dir entry", phpIni)
	}
	if !strings.Contains(phpIni, "extension=openssl") {
		t.Fatalf("php.ini = %q, want enabled openssl extension", phpIni)
	}
	if !strings.Contains(phpIni, ";extension=xdebug") {
		t.Fatalf("php.ini = %q, want disabled xdebug extension", phpIni)
	}
	if strings.Index(phpIni, "extension=openssl") > strings.Index(phpIni, ";extension=xdebug") {
		t.Fatalf("php.ini = %q, want sorted extension entries", phpIni)
	}
	if strings.Contains(phpIni, " OpenSSL ") {
		t.Fatalf("php.ini = %q, want normalized extension names", phpIni)
	}
}

func TestStoreInstallEnablesComposerPHPExtensionsByDefault(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")
	writeCachedTool(t, store.CacheDir, toolComposer, "2.8")
	if err := os.MkdirAll(filepath.Join(store.CacheDir, toolPHP, "8.4", "ext"), 0o755); err != nil {
		t.Fatalf("MkdirAll(ext) error = %v", err)
	}

	if _, err := store.Configure("demo", "8.4", "2.8", nil); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}

	results, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Install(demo) length = %d, want 2", len(results))
	}

	phpIniData, err := os.ReadFile(filepath.Join(store.RootDir, "envs", "php", "8.4", "bin", "php.ini"))
	if err != nil {
		t.Fatalf("ReadFile(php.ini) error = %v", err)
	}
	phpIni := string(phpIniData)
	if !strings.Contains(phpIni, "extension=openssl") {
		t.Fatalf("php.ini = %q, want enabled openssl extension", phpIni)
	}
	if !strings.Contains(phpIni, "extension=zip") {
		t.Fatalf("php.ini = %q, want enabled zip extension", phpIni)
	}
}

func TestStoreInstallComposerDefaultsHonorExplicitFalse(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")
	writeCachedTool(t, store.CacheDir, toolComposer, "2.8")
	if err := os.MkdirAll(filepath.Join(store.CacheDir, toolPHP, "8.4", "ext"), 0o755); err != nil {
		t.Fatalf("MkdirAll(ext) error = %v", err)
	}

	if _, err := store.Configure("demo", "8.4", "2.8", nil); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}

	config, err := store.loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	environment := config.Environments["demo"]
	environment.PHPExtensions = map[string]bool{"openssl": false}
	config.Environments["demo"] = environment
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	if _, err := store.Install("demo"); err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}

	phpIniData, err := os.ReadFile(filepath.Join(store.RootDir, "envs", "php", "8.4", "bin", "php.ini"))
	if err != nil {
		t.Fatalf("ReadFile(php.ini) error = %v", err)
	}
	phpIni := string(phpIniData)
	if !strings.Contains(phpIni, ";extension=openssl") {
		t.Fatalf("php.ini = %q, want disabled openssl extension", phpIni)
	}
	if !strings.Contains(phpIni, "extension=zip") {
		t.Fatalf("php.ini = %q, want enabled zip extension", phpIni)
	}
}

func TestStoreInstallRejectsPHPExtensionsWithoutPHP(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{
		ComposerVersion: "2.8",
		PHPExtensions:   map[string]bool{"openssl": true},
	}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	if _, err := store.Install("demo"); err == nil {
		t.Fatal("Install(demo) error = nil, want php-extensions validation error")
	} else if !strings.Contains(err.Error(), "php-extensions") {
		t.Fatalf("Install(demo) error = %v, want php-extensions validation error", err)
	}
}

func TestStoreCurrentNormalizesServerConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	config := store.defaultConfig()
	config.Current = "demo"
	config.Environments["demo"] = Environment{
		PHPVersion: "8.4",
		Server: &ServerConfig{
			Hostname: " localhost ",
			Port:     8080,
			HTTPS:    true,
		},
	}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	current, err := store.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if current == nil || current.Server == nil {
		t.Fatalf("Current() = %#v, want server config", current)
	}
	if current.Server.Hostname != "localhost" || current.Server.Port != 8080 || !current.Server.HTTPS {
		t.Fatalf("Current().Server = %#v, want normalized hostname, port, and https", current.Server)
	}
}

func TestStoreCurrentNormalizesEnvironmentVariables(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	config := store.defaultConfig()
	config.Current = "demo"
	config.Environments["demo"] = Environment{
		PHPVersion: "8.4",
		EnvFile:    " .env.local ",
		EnvVars: map[string]string{
			" APP_ENV ":    "development",
			"FEATURE_FLAG": "1",
		},
	}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	current, err := store.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if current == nil {
		t.Fatal("Current() = nil, want environment")
	}
	if current.EnvFile != ".env.local" {
		t.Fatalf("Current().EnvFile = %q, want %q", current.EnvFile, ".env.local")
	}
	if len(current.EnvVars) != 2 {
		t.Fatalf("Current().EnvVars = %#v, want two normalized keys", current.EnvVars)
	}
	if current.EnvVars["APP_ENV"] != "development" {
		t.Fatalf("Current().EnvVars[APP_ENV] = %q, want %q", current.EnvVars["APP_ENV"], "development")
	}
	if current.EnvVars["FEATURE_FLAG"] != "1" {
		t.Fatalf("Current().EnvVars[FEATURE_FLAG] = %q, want %q", current.EnvVars["FEATURE_FLAG"], "1")
	}
}

func TestStoreCreateRejectsExistingEnvironment(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")

	environment, err := store.Create("demo", "8.4", "2.8", nil)
	if err != nil {
		t.Fatalf("Create(demo) error = %v", err)
	}
	if environment.PHPVersion != "8.4" || environment.ComposerVersion != "2.8" {
		t.Fatalf("Create(demo) = %#v, want configured versions", environment)
	}

	if _, err := store.Create("demo", "8.3", "2.7", nil); err == nil {
		t.Fatal("Create(demo) second call error = nil, want already exists error")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Create(demo) second call error = %v, want already exists error", err)
	}

	configData, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	config, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	created := config.Environments["demo"]
	if created.PHPVersion != "8.4" || created.ComposerVersion != "2.8" {
		t.Fatalf("config = %q, want first create values preserved", string(configData))
	}
}

func TestStoreCreatePreservesExistingConfigComments(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	configData := []byte(strings.Join([]string{
		"# project comment",
		"version: 1",
		"root: .polka",
		"current: demo",
		"environments:",
		"  # existing environment comment",
		"  demo:",
		"    php: \"8.4\"",
		"",
	}, "\n"))
	if err := os.WriteFile(store.ConfigFile, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	if _, err := store.Create("api", "8.3", "2.7", nil); err != nil {
		t.Fatalf("Create(api) error = %v", err)
	}

	updatedConfig, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	for _, comment := range []string{"# project comment", "# existing environment comment"} {
		if !strings.Contains(string(updatedConfig), comment) {
			t.Fatalf("config after Create() = %q, want preserved comment %q", string(updatedConfig), comment)
		}
	}
	config, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	created := config.Environments["api"]
	if created.PHPVersion != "8.3" || created.ComposerVersion != "2.7" {
		t.Fatalf("created environment = %#v, want api php/composer versions", created)
	}
}

func TestStoreConfigureLifecycleUsesVersionLabels(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	phpCachePath := writeCachedTool(t, store.CacheDir, toolPHP, "8.4")
	composerCachePath := writeCachedTool(t, store.CacheDir, toolComposer, "2.8")

	environment, err := store.Configure("api", "8.4", "2.8", nil)
	if err != nil {
		t.Fatalf("Configure(api) error = %v", err)
	}
	if environment.PHPVersion != "8.4" || environment.ComposerVersion != "2.8" {
		t.Fatalf("Configure(api) = %#v, want version labels", environment)
	}

	if _, err := store.Configure("web", "8.3", "", nil); err != nil {
		t.Fatalf("Configure(web) error = %v", err)
	}
	writeCachedTool(t, store.CacheDir, toolPHP, "8.3")

	installResults, err := store.Install("api")
	if err != nil {
		t.Fatalf("Install(api) error = %v", err)
	}
	if len(installResults) != 2 {
		t.Fatalf("Install(api) length = %d, want 2", len(installResults))
	}

	environments, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(environments) != 2 {
		t.Fatalf("List() length = %d, want 2", len(environments))
	}
	if environments[0].Name != "api" || environments[1].Name != "web" {
		t.Fatalf("List() names = %#v, want sorted names", environments)
	}
	if environments[0].PHPVersion != "8.4" || environments[0].ComposerVersion != "2.8" {
		t.Fatalf("List() api = %#v, want configured version labels", environments[0])
	}

	if err := store.Use("api"); err != nil {
		t.Fatalf("Use() error = %v", err)
	}

	current, err := store.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if current == nil || current.Name != "api" {
		t.Fatalf("Current() = %#v, want active environment %q", current, "api")
	}
	if current.PHPVersion != "8.4" || current.ComposerVersion != "2.8" {
		t.Fatalf("Current() = %#v, want configured version labels", current)
	}

	target, err := store.ResolveTool("php")
	if err != nil {
		t.Fatalf("ResolveTool(php) error = %v", err)
	}
	if !strings.Contains(target, filepath.Join("envs", "php", "8.4")) {
		t.Fatalf("ResolveTool(php) = %q, want project env path", target)
	}

	composerTarget, err := store.ResolveTool("composer")
	if err != nil {
		t.Fatalf("ResolveTool(composer) error = %v", err)
	}
	if !strings.Contains(composerTarget, filepath.Join("envs", "composer", "2.8")) {
		t.Fatalf("ResolveTool(composer) = %q, want project env path", composerTarget)
	}

	if err := store.Remove("api"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	current, err = store.Current()
	if err != nil {
		t.Fatalf("Current() after remove error = %v", err)
	}
	if current != nil {
		t.Fatalf("Current() after remove = %#v, want nil", current)
	}

	environments, err = store.List()
	if err != nil {
		t.Fatalf("List() after remove error = %v", err)
	}
	if len(environments) != 1 || environments[0].Name != "web" {
		t.Fatalf("List() after remove = %#v, want only web", environments)
	}

	configData, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(config after remove) error = %v", err)
	}
	if strings.Contains(string(configData), "current:") {
		t.Fatalf("config after remove = %q, want current entry cleared", string(configData))
	}
	parsedConfig, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if parsedConfig.Environments["web"].PHPVersion != "8.3" {
		t.Fatalf("readConfig() web PHPVersion = %q, want %q", parsedConfig.Environments["web"].PHPVersion, "8.3")
	}
	if strings.Contains(string(configData), phpCachePath) || strings.Contains(string(configData), composerCachePath) {
		t.Fatalf("config after remove = %q, want version labels rather than tool paths", string(configData))
	}
}

func TestStoreRemovePreservesRemainingConfigComments(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	configData := []byte(strings.Join([]string{
		"# project comment",
		"version: 1",
		"root: .polka",
		"current: demo",
		"environments:",
		"  demo:",
		"    php: \"8.4\"",
		"  # keep this environment comment",
		"  web:",
		"    php: \"8.3\" # keep this inline comment",
		"",
	}, "\n"))
	if err := os.WriteFile(store.ConfigFile, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if err := store.Remove("demo"); err != nil {
		t.Fatalf("Remove(demo) error = %v", err)
	}

	updatedConfig, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	for _, comment := range []string{"# project comment", "# keep this environment comment", "# keep this inline comment"} {
		if !strings.Contains(string(updatedConfig), comment) {
			t.Fatalf("config after Remove() = %q, want preserved comment %q", string(updatedConfig), comment)
		}
	}
	config, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if _, ok := config.Environments["demo"]; ok {
		t.Fatalf("config.Environments still contains demo after Remove(): %#v", config.Environments)
	}
	if config.Environments["web"].PHPVersion != "8.3" {
		t.Fatalf("config.Environments[web].PHPVersion = %q, want 8.3", config.Environments["web"].PHPVersion)
	}
}

func TestStoreConfigureNormalizesDatabaseConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	environment, err := store.Configure("data", "", "", &DatabaseConfig{Engine: " MySQL ", Version: " 8.0 ", Port: 3306})
	if err != nil {
		t.Fatalf("Configure(data) error = %v", err)
	}
	if environment.Database == nil {
		t.Fatalf("Configure(data) = %#v, want database config", environment)
	}
	if environment.Database.Engine != toolMySQL || environment.Database.Version != "8.0" || environment.Database.Port != 3306 {
		t.Fatalf("Configure(data).Database = %#v, want normalized database config", environment.Database)
	}

	config, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	stored := config.Environments["data"].Database
	if stored == nil || stored.Engine != toolMySQL || stored.Version != "8.0" || stored.Port != 3306 {
		t.Fatalf("stored database = %#v, want normalized database config", stored)
	}
}

func TestResolveToolRequiresConfiguredVersion(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	config := `version: 1
root: .polka
current: demo
environments:
  demo:
    composer: 2.8
`
	if err := os.WriteFile(store.ConfigFile, []byte(config), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	if _, err := store.ResolveTool("php"); err == nil {
		t.Fatal("ResolveTool(php) error = nil, want missing php command error")
	}
}

func TestDefaultStoreUsesDotPolkaInWorkingDirectory(t *testing.T) {
	projectDir := t.TempDir()
	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})

	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	store, err := DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore() error = %v", err)
	}

	if store.RootDir != filepath.Join(projectDir, ".polka") {
		t.Fatalf("DefaultStore() RootDir = %q, want %q", store.RootDir, filepath.Join(projectDir, ".polka"))
	}
	if store.ConfigFile != filepath.Join(projectDir, "polka.yaml") {
		t.Fatalf("DefaultStore() ConfigFile = %q, want %q", store.ConfigFile, filepath.Join(projectDir, "polka.yaml"))
	}
}

func TestDefaultStoreDiscoversParentProjectFromNestedDirectory(t *testing.T) {
	projectDir := t.TempDir()
	nestedDir := filepath.Join(projectDir, "web", "modules")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), []byte("version: 1\nroot: .polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})

	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	store, err := DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore() error = %v", err)
	}

	if store.RootDir != filepath.Join(projectDir, ".polka") {
		t.Fatalf("DefaultStore() RootDir = %q, want %q", store.RootDir, filepath.Join(projectDir, ".polka"))
	}
	if store.ConfigFile != filepath.Join(projectDir, "polka.yaml") {
		t.Fatalf("DefaultStore() ConfigFile = %q, want %q", store.ConfigFile, filepath.Join(projectDir, "polka.yaml"))
	}
}

func TestDefaultStoreUsesConfiguredNestedRootFromProjectConfig(t *testing.T) {
	projectDir := t.TempDir()
	nestedDir := filepath.Join(projectDir, "test-site", "web")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), []byte("version: 1\nroot: test-site/.polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})

	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	store, err := DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore() error = %v", err)
	}

	if store.ProjectDir != projectDir {
		t.Fatalf("DefaultStore() ProjectDir = %q, want %q", store.ProjectDir, projectDir)
	}
	if store.RootDir != filepath.Join(projectDir, "test-site", ".polka") {
		t.Fatalf("DefaultStore() RootDir = %q, want %q", store.RootDir, filepath.Join(projectDir, "test-site", ".polka"))
	}
	if store.ConfigFile != filepath.Join(projectDir, "polka.yaml") {
		t.Fatalf("DefaultStore() ConfigFile = %q, want %q", store.ConfigFile, filepath.Join(projectDir, "polka.yaml"))
	}
}

func TestStoreForRootUsesConfiguredNestedRootFromAncestorConfig(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, "test-site", ".polka")
	configData := []byte("version: 1\nroot: test-site/.polka\ncurrent: demo\nenvironments:\n  demo:\n    php: \"8.4\"\n")
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	store, err := StoreForRoot(root)
	if err != nil {
		t.Fatalf("StoreForRoot() error = %v", err)
	}

	if store.ProjectDir != projectDir {
		t.Fatalf("StoreForRoot() ProjectDir = %q, want %q", store.ProjectDir, projectDir)
	}
	if store.RootDir != root {
		t.Fatalf("StoreForRoot() RootDir = %q, want %q", store.RootDir, root)
	}
	if store.ConfigFile != filepath.Join(projectDir, "polka.yaml") {
		t.Fatalf("StoreForRoot() ConfigFile = %q, want %q", store.ConfigFile, filepath.Join(projectDir, "polka.yaml"))
	}
	current, err := store.Current()
	if err != nil {
		t.Fatalf("store.Current() error = %v", err)
	}
	if current == nil || current.Name != "demo" {
		t.Fatalf("store.Current() = %#v, want demo", current)
	}
}

func TestDefaultStoreIgnoresNestedRuntimeDotPolkaWhenParentHasProjectConfig(t *testing.T) {
	projectDir := t.TempDir()
	nestedDir := filepath.Join(projectDir, "drupal")
	if err := os.MkdirAll(filepath.Join(nestedDir, ".polka", "run"), 0o755); err != nil {
		t.Fatalf("MkdirAll(nested runtime) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), []byte("version: 1\nroot: .polka\ncurrent: default\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})

	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	store, err := DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore() error = %v", err)
	}

	if store.ProjectDir != projectDir {
		t.Fatalf("DefaultStore() ProjectDir = %q, want %q", store.ProjectDir, projectDir)
	}
	if store.RootDir != filepath.Join(projectDir, ".polka") {
		t.Fatalf("DefaultStore() RootDir = %q, want %q", store.RootDir, filepath.Join(projectDir, ".polka"))
	}
}

func writeCachedTool(t *testing.T, cacheDir, tool, version string) string {
	t.Helper()

	path := toolInstallCandidatesIn(cacheDir, tool, version)[0]
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("placeholder\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}

	return path
}

func writeCachedNodeJSCommand(t *testing.T, cacheDir, version, command string) string {
	t.Helper()

	path := filepath.Join(cacheDir, toolNodeJS, version)
	if runtime.GOOS == "windows" {
		if command == toolNode {
			path = filepath.Join(path, command+".cmd")
		} else {
			path = filepath.Join(path, command+".cmd")
		}
	} else {
		path = filepath.Join(path, "bin", command)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(command+"\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}

	return path
}

type fakeDownloader func(cacheDir, tool, version string) error

func (f fakeDownloader) Download(cacheDir, tool, version string) error {
	return f(cacheDir, tool, version)
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
}
