package backend

import (
	"fmt"
	"os"
	"path/filepath"
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
	assertPathMissing(t, filepath.Join(store.BinDir, "php"))
	assertPathMissing(t, filepath.Join(store.BinDir, "php.cmd"))
	assertPathMissing(t, filepath.Join(store.BinDir, toolComposer))
	assertPathMissing(t, filepath.Join(store.BinDir, toolComposer+".cmd"))
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
	if current.Server.Hostname != "localhost" || current.Server.Port != 8080 {
		t.Fatalf("Current().Server = %#v, want normalized hostname and port", current.Server)
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
