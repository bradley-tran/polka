package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadInstallStateMissingFileReturnsEmptyState(t *testing.T) {
	store := NewProjectStore(t.TempDir())

	state := store.readInstallState()
	if state.Version != installStateVersion {
		t.Fatalf("readInstallState() version = %d, want %d", state.Version, installStateVersion)
	}
	if len(state.Tools) != 0 {
		t.Fatalf("readInstallState() tools = %#v, want empty", state.Tools)
	}
}

func TestInstallStateWriteReadRoundTrip(t *testing.T) {
	store := NewProjectStore(t.TempDir())

	state := installState{Version: installStateVersion}
	state.record(toolPHP, "8.4")
	state.record(toolComposer, "2.8")
	if err := store.writeInstallState(state); err != nil {
		t.Fatalf("writeInstallState() error = %v", err)
	}

	loaded := store.readInstallState()
	if !loaded.has(toolPHP, "8.4") || !loaded.has(toolComposer, "2.8") {
		t.Fatalf("readInstallState() = %#v, want recorded php 8.4 and composer 2.8", loaded)
	}
	if loaded.has(toolPHP, "8.3") {
		t.Fatalf("readInstallState() has php 8.3 = true, want false")
	}
}

func TestReadInstallStateToleratesCorruptFile(t *testing.T) {
	store := NewProjectStore(t.TempDir())
	if err := os.MkdirAll(store.EnvsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(envs) error = %v", err)
	}
	if err := os.WriteFile(store.installStateFile(), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile(corrupt state) error = %v", err)
	}

	state := store.readInstallState()
	if len(state.Tools) != 0 {
		t.Fatalf("readInstallState(corrupt) tools = %#v, want empty", state.Tools)
	}
}

func TestInstallStateRecordDeduplicates(t *testing.T) {
	state := installState{Version: installStateVersion}
	state.record(toolPHP, "8.4")
	state.record(toolPHP, "8.4")
	state.record(toolPHP, "8.3")

	if len(state.Tools[toolPHP]) != 2 {
		t.Fatalf("record() versions = %#v, want two deduplicated entries", state.Tools[toolPHP])
	}
	if state.Tools[toolPHP][0] != "8.3" || state.Tools[toolPHP][1] != "8.4" {
		t.Fatalf("record() versions = %#v, want sorted [8.3 8.4]", state.Tools[toolPHP])
	}
}

func TestStoreInstallSkipsUnchangedTools(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")

	if _, err := store.Configure("demo", "8.4", "", "", nil); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}
	firstResults, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}
	if len(firstResults) != 1 || firstResults[0].Skipped {
		t.Fatalf("Install(demo) results = %#v, want one non-skipped result", firstResults)
	}

	sentinel := filepath.Join(store.EnvsDir, toolPHP, "8.4", "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(sentinel) error = %v", err)
	}

	secondResults, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) second run error = %v", err)
	}
	if len(secondResults) != 1 {
		t.Fatalf("Install(demo) second run results = %#v, want one result", secondResults)
	}
	result := secondResults[0]
	if !result.Skipped || result.Downloaded || result.CachePath != "" {
		t.Fatalf("Install(demo) second run result = %#v, want skipped result", result)
	}
	if result.TargetPath != firstResults[0].TargetPath {
		t.Fatalf("Install(demo) second run target = %q, want %q", result.TargetPath, firstResults[0].TargetPath)
	}
	assertPathExists(t, sentinel)
}

func TestStoreInstallForceReinstallsUnchangedTools(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")

	if _, err := store.Configure("demo", "8.4", "", "", nil); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}
	if _, err := store.Install("demo"); err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}

	sentinel := filepath.Join(store.EnvsDir, toolPHP, "8.4", "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("discard\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(sentinel) error = %v", err)
	}

	results, err := store.InstallWithProgress("demo", InstallOptions{Force: true}, nil)
	if err != nil {
		t.Fatalf("InstallWithProgress(demo, force) error = %v", err)
	}
	if len(results) != 1 || results[0].Skipped {
		t.Fatalf("InstallWithProgress(demo, force) results = %#v, want reinstalled result", results)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("Stat(sentinel) error = %v, want sentinel removed by forced reinstall", err)
	}
}

func TestStoreInstallReinstallsWhenInstalledToolIsMissing(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")

	if _, err := store.Configure("demo", "8.4", "", "", nil); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}
	firstResults, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(store.EnvsDir, toolPHP, "8.4")); err != nil {
		t.Fatalf("RemoveAll(installed php) error = %v", err)
	}

	results, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) after removal error = %v", err)
	}
	if len(results) != 1 || results[0].Skipped {
		t.Fatalf("Install(demo) after removal results = %#v, want reinstalled result", results)
	}
	assertPathExists(t, firstResults[0].TargetPath)
}

func TestStoreInstallSkippedToolStillAppliesPostInstallSettings(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")

	if _, err := store.Configure("demo", "8.4", "", "", nil); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}
	firstResults, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}

	config, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	environment := config.Environments["demo"]
	environment.MemoryLimit = "512m"
	config.Environments["demo"] = environment
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	results, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) second run error = %v", err)
	}
	if len(results) != 1 || !results[0].Skipped {
		t.Fatalf("Install(demo) second run results = %#v, want skipped result", results)
	}
	phpIniData, err := os.ReadFile(filepath.Join(filepath.Dir(firstResults[0].TargetPath), "php.ini"))
	if err != nil {
		t.Fatalf("ReadFile(installed php.ini) error = %v", err)
	}
	if !strings.Contains(string(phpIniData), "memory_limit=512M") {
		t.Fatalf("php.ini = %q, want memory_limit applied to skipped install", string(phpIniData))
	}
}

func TestStoreInstallRecordsSuccessesOnPartialFailure(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")
	store.Downloader = fakeDownloader(func(cacheDir, tool, version string) error {
		return fmt.Errorf("download %s %s failed", tool, version)
	})

	if _, err := store.Configure("demo", "8.4", "", "", &DatabaseConfig{Engine: toolMySQL, Version: "8.4"}); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}

	if _, err := store.Install("demo"); err == nil {
		t.Fatal("Install(demo) error = nil, want mysql download failure")
	}

	state := store.readInstallState()
	if !state.has(toolPHP, "8.4") {
		t.Fatalf("install state = %#v, want php 8.4 recorded despite mysql failure", state)
	}
	if state.has(toolMySQL, "8.4") {
		t.Fatalf("install state = %#v, want mysql 8.4 not recorded", state)
	}
}

func TestStoreInstallToolRevertsConfigOnFailure(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")

	if _, err := store.Configure(defaultEnvironmentName, "8.4", "", "", nil); err != nil {
		t.Fatalf("Configure(default) error = %v", err)
	}
	configBefore, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(config before) error = %v", err)
	}

	store.Downloader = fakeDownloader(func(cacheDir, tool, version string) error {
		return fmt.Errorf("download %s %s failed", tool, version)
	})
	if _, err := store.InstallTool(defaultEnvironmentName, toolPHP, "9.9"); err == nil {
		t.Fatal("InstallTool(default, php, 9.9) error = nil, want download failure")
	}

	configAfter, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(config after) error = %v", err)
	}
	if string(configAfter) != string(configBefore) {
		t.Fatalf("config after failed install = %q, want unchanged %q", string(configAfter), string(configBefore))
	}
	if store.readInstallState().has(toolPHP, "9.9") {
		t.Fatal("install state has php 9.9 = true, want false after failed install")
	}
}

func TestStoreInstallToolAlwaysReinstallsExplicitVersion(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")

	if _, err := store.InstallTool(defaultEnvironmentName, toolPHP, "8.4"); err != nil {
		t.Fatalf("InstallTool(default, php, 8.4) error = %v", err)
	}
	sentinel := filepath.Join(store.EnvsDir, toolPHP, "8.4", "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("discard\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(sentinel) error = %v", err)
	}

	result, err := store.InstallTool(defaultEnvironmentName, toolPHP, "8.4")
	if err != nil {
		t.Fatalf("InstallTool(default, php, 8.4) second run error = %v", err)
	}
	if result.Skipped {
		t.Fatalf("InstallTool(default, php, 8.4) second run = %#v, want reinstalled result", result)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("Stat(sentinel) error = %v, want sentinel removed by explicit reinstall", err)
	}
}
