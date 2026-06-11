package backend

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
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
	assertPathMissing(t, filepath.Join(store.BinDir, toolMago))
	assertPathMissing(t, filepath.Join(store.BinDir, toolMago+".cmd"))
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
	if !strings.Contains(string(configData), "https: true") {
		t.Fatalf("config contents = %q, want https enabled by default", string(configData))
	}
	if !strings.Contains(string(configData), "hostname: "+projectLocalHostname(projectDir)) {
		t.Fatalf("config contents = %q, want project-local hostname", string(configData))
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

func TestStoreInitWithOptionsWritesDocroot(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	if err := store.InitWithOptions(InitOptions{Docroot: " public "}); err != nil {
		t.Fatalf("InitWithOptions() error = %v", err)
	}

	config, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	environment := config.Environments[defaultEnvironmentName]
	if environment.Docroot != "public" {
		t.Fatalf("environment = %#v, want overridden docroot", environment)
	}
}

func TestStoreInitWithOptionsUpdatesExistingDocroot(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	config := store.defaultConfig()
	config.Environments[defaultEnvironmentName] = Environment{Docroot: "web", PHPVersion: "8.4"}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	if err := store.InitWithOptions(InitOptions{Docroot: "public"}); err != nil {
		t.Fatalf("InitWithOptions() error = %v", err)
	}

	loadedConfig, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	environment := loadedConfig.Environments[defaultEnvironmentName]
	if environment.Docroot != "public" || environment.PHPVersion != "8.4" {
		t.Fatalf("environment = %#v, want updated docroot with existing values preserved", environment)
	}
}

func TestStoreInitWithFrameworkOptionsOverridesDocroot(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	if err := store.InitWithFrameworkOptions("drupal", InitOptions{Docroot: " drupal/web "}); err != nil {
		t.Fatalf("InitWithFrameworkOptions() error = %v", err)
	}

	config, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	environment := config.Environments[defaultEnvironmentName]
	if environment.Framework != "drupal" || environment.Docroot != "drupal/web" {
		t.Fatalf("environment = %#v, want drupal framework with overridden docroot", environment)
	}
	if environment.ComposerVersion != "2.8" || environment.NodeJSVersion != "24" || environment.NginxVersion != "1.30" {
		t.Fatalf("environment = %#v, want other Drupal preset values preserved", environment)
	}
}

func TestProjectLocalHostnameNormalizesDirectoryName(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "My Demo_Project")

	if got, want := projectLocalHostname(projectDir), "my-demo-project.localhost"; got != want {
		t.Fatalf("projectLocalHostname() = %q, want %q", got, want)
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
	assertPathMissing(t, filepath.Join(store.BinDir, toolMago))
	assertPathMissing(t, filepath.Join(store.BinDir, toolMago+".cmd"))
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

func TestStoreUseStoresActiveEnvironmentOutsideConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	configData := []byte(strings.Join([]string{
		"version: 1",
		"root: .polka",
		"tools:",
		"  php: \"8.4\"",
		"",
	}, "\n"))
	if err := os.WriteFile(store.ConfigFile, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	webConfigData := []byte(strings.Join([]string{
		"tools:",
		"  php: \"8.3\"",
		"",
	}, "\n"))
	if err := os.WriteFile(store.environmentConfigFile("web"), webConfigData, 0o644); err != nil {
		t.Fatalf("WriteFile(web config) error = %v", err)
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
	if strings.Contains(string(updatedConfig), "current:") {
		t.Fatalf("config after Use() = %q, want no current entry", string(updatedConfig))
	}
	activeName, err := os.ReadFile(store.activeEnvironmentPath())
	if err != nil {
		t.Fatalf("ReadFile(active environment) error = %v", err)
	}
	if strings.TrimSpace(string(activeName)) != "web" {
		t.Fatalf("active environment = %q, want web", string(activeName))
	}
}

func TestStoreUseDefaultClearsActiveEnvironmentOverride(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	if _, err := store.Configure(defaultEnvironmentName, "8.4", "", nil); err != nil {
		t.Fatalf("Configure(default) error = %v", err)
	}
	if _, err := store.Configure("demo", "8.3", "", nil); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}
	if err := store.Use("demo"); err != nil {
		t.Fatalf("Use(demo) error = %v", err)
	}

	if err := store.Use(defaultEnvironmentName); err != nil {
		t.Fatalf("Use(default) error = %v", err)
	}

	if _, err := os.Stat(store.activeEnvironmentPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(active environment) error = %v, want missing active override", err)
	}
	current, err := store.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if current == nil || current.Name != defaultEnvironmentName || current.PHPVersion != "8.4" {
		t.Fatalf("Current() = %#v, want default php 8.4", current)
	}
}

func TestStoreRejectsLegacyEnvironmentsConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	configData := []byte("version: 1\nroot: .polka\nenvironments:\n  demo:\n    php: \"8.4\"\n")
	if err := os.WriteFile(store.ConfigFile, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	if _, err := store.List(); err == nil {
		t.Fatal("List() error = nil, want legacy config rejection")
	} else if !strings.Contains(err.Error(), "legacy environments config") {
		t.Fatalf("List() error = %v, want legacy config rejection", err)
	}
}

func TestStoreReadsAndWritesFrameworkConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	config := store.defaultConfig()
	config.Environments[defaultEnvironmentName] = Environment{Framework: "Drupal", PHPVersion: "8.4"}
	config.Environments["app"] = Environment{Framework: "Laravel", PHPVersion: "8.4"}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	projectConfig, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(project config) error = %v", err)
	}
	if !strings.Contains(string(projectConfig), "framework: drupal") {
		t.Fatalf("project config = %q, want normalized default framework", string(projectConfig))
	}
	appConfig, err := os.ReadFile(store.environmentConfigFile("app"))
	if err != nil {
		t.Fatalf("ReadFile(app config) error = %v", err)
	}
	if !strings.Contains(string(appConfig), "framework: laravel") {
		t.Fatalf("app config = %q, want normalized named framework", string(appConfig))
	}

	loaded, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if loaded.Environments[defaultEnvironmentName].Framework != "drupal" || loaded.Environments["app"].Framework != "laravel" {
		t.Fatalf("frameworks = default:%q app:%q, want normalized framework IDs", loaded.Environments[defaultEnvironmentName].Framework, loaded.Environments["app"].Framework)
	}
}

func TestStoreReadsAndWritesOPcacheConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	config := store.defaultConfig()
	config.Environments[defaultEnvironmentName] = Environment{
		PHPVersion:      "8.4",
		OPcachePreset:   "Production",
		OPcacheConfig:   map[string]string{" OPcache.Revalidate_Freq ": " 2 "},
		PHPExtensions:   map[string]bool{"opcache": true},
		ComposerVersion: "2.8",
	}
	config.Environments["app"] = Environment{
		PHPVersion:    "8.3",
		OPcachePreset: "Dev",
		OPcacheConfig: map[string]string{" OPcache.Enable_Cli ": " true "},
	}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	projectConfig, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(project config) error = %v", err)
	}
	if !strings.Contains(string(projectConfig), "opcache-preset: production") || strings.Contains(string(projectConfig), "Production") {
		t.Fatalf("project config = %q, want normalized production OPcache preset", string(projectConfig))
	}
	appConfig, err := os.ReadFile(store.environmentConfigFile("app"))
	if err != nil {
		t.Fatalf("ReadFile(app config) error = %v", err)
	}
	if !strings.Contains(string(appConfig), "opcache-preset: dev") || strings.Contains(string(appConfig), "OPcache.Enable_Cli") {
		t.Fatalf("app config = %q, want normalized dev OPcache config", string(appConfig))
	}

	loaded, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if loaded.Environments[defaultEnvironmentName].OPcachePreset != "production" {
		t.Fatalf("default opcache-preset = %q, want production", loaded.Environments[defaultEnvironmentName].OPcachePreset)
	}
	if loaded.Environments[defaultEnvironmentName].OPcacheConfig["opcache.revalidate_freq"] != "2" {
		t.Fatalf("default opcache-config = %#v, want normalized revalidate_freq", loaded.Environments[defaultEnvironmentName].OPcacheConfig)
	}
	if loaded.Environments["app"].OPcachePreset != "dev" || loaded.Environments["app"].OPcacheConfig["opcache.enable_cli"] != "true" {
		t.Fatalf("app OPcache config = %q %#v, want normalized dev config", loaded.Environments["app"].OPcachePreset, loaded.Environments["app"].OPcacheConfig)
	}
}

func TestStoreRejectsUnknownFrameworkConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	if err := os.WriteFile(store.ConfigFile, []byte("version: 1\nroot: .polka\nframework: cakephp\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	if _, err := store.readConfig(); err == nil {
		t.Fatal("readConfig() error = nil, want unsupported framework error")
	} else if !strings.Contains(err.Error(), "unsupported framework") || !strings.Contains(err.Error(), "drupal, laravel, symfony, wordpress") {
		t.Fatalf("readConfig() error = %v, want supported framework list", err)
	}
}

func TestStoreRemoveRejectsDefaultEnvironment(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	if err := store.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if err := store.Remove(defaultEnvironmentName); err == nil {
		t.Fatal("Remove(default) error = nil, want default removal rejection")
	} else if !strings.Contains(err.Error(), "cannot be removed") {
		t.Fatalf("Remove(default) error = %v, want default removal rejection", err)
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

func TestStoreInstallToolCopiesExplicitVersionIntoLayout(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	cachePHP := writeCachedTool(t, store.CacheDir, toolPHP, "8.4")

	result, err := store.InstallTool(defaultEnvironmentName, toolPHP, "8.4")
	if err != nil {
		t.Fatalf("InstallTool(default, php, 8.4) error = %v", err)
	}
	if result.Tool != toolPHP || result.Version != "8.4" {
		t.Fatalf("InstallTool(default, php, 8.4) = %#v, want php 8.4", result)
	}
	if result.Downloaded {
		t.Fatalf("InstallTool(default, php, 8.4) Downloaded = true, want cache hit")
	}
	if result.CachePath != cachePHP {
		t.Fatalf("InstallTool(default, php, 8.4) CachePath = %q, want %q", result.CachePath, cachePHP)
	}
	assertPathExists(t, result.TargetPath)
	if !strings.Contains(result.TargetPath, filepath.Join("envs", toolPHP, "8.4")) {
		t.Fatalf("InstallTool(default, php, 8.4) target = %q, want versioned env path", result.TargetPath)
	}
	config, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if config.Environments[defaultEnvironmentName].PHPVersion != "8.4" {
		t.Fatalf("default environment php version = %q, want 8.4", config.Environments[defaultEnvironmentName].PHPVersion)
	}
}

func TestStoreInstallToolAppliesEnvironmentPostInstallSettings(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")
	if err := os.MkdirAll(filepath.Join(store.CacheDir, toolPHP, "8.4", "ext"), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache ext) error = %v", err)
	}

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{
		PHPExtensions: map[string]bool{"openssl": true, "xdebug": false},
	}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	result, err := store.InstallTool("demo", toolPHP, "8.4")
	if err != nil {
		t.Fatalf("InstallTool(demo, php, 8.4) error = %v", err)
	}
	phpIniData, err := os.ReadFile(filepath.Join(filepath.Dir(result.TargetPath), "php.ini"))
	if err != nil {
		t.Fatalf("ReadFile(installed php.ini) error = %v", err)
	}
	phpIni := string(phpIniData)
	if !strings.Contains(phpIni, "extension=openssl") {
		t.Fatalf("php.ini = %q, want enabled openssl extension", phpIni)
	}
	if !strings.Contains(phpIni, "curl.cainfo=") || !strings.Contains(phpIni, "openssl.cafile=") {
		t.Fatalf("php.ini = %q, want TLS CA bundle directives", phpIni)
	}
	if !strings.Contains(phpIni, ";extension=xdebug") {
		t.Fatalf("php.ini = %q, want disabled xdebug extension", phpIni)
	}
	loadedConfig, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if loadedConfig.Environments["demo"].PHPVersion != "8.4" {
		t.Fatalf("demo environment php version = %q, want 8.4", loadedConfig.Environments["demo"].PHPVersion)
	}
}

func TestStoreInstallToolSkipsBuiltInPHPExtensions(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedPHPToolWithBuiltInModules(t, store.CacheDir, "8.4", []string{"OpenSSL", "zip", "Zend OPcache"})
	if err := os.MkdirAll(filepath.Join(store.CacheDir, toolPHP, "8.4", "ext"), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache ext) error = %v", err)
	}

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{
		PHPExtensions: map[string]bool{
			"opcache": true,
			"openssl": true,
			"xdebug":  false,
			"zip":     true,
		},
	}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	result, err := store.InstallTool("demo", toolPHP, "8.4")
	if err != nil {
		t.Fatalf("InstallTool(demo, php, 8.4) error = %v", err)
	}
	phpIniData, err := os.ReadFile(filepath.Join(filepath.Dir(result.TargetPath), "php.ini"))
	if err != nil {
		t.Fatalf("ReadFile(installed php.ini) error = %v", err)
	}

	phpIni := string(phpIniData)
	for _, skipped := range []string{"extension=openssl", "extension=zip", "zend_extension=opcache"} {
		if strings.Contains(phpIni, skipped) {
			t.Fatalf("php.ini = %q, want built-in %s skipped", phpIni, skipped)
		}
	}
	if !strings.Contains(phpIni, ";extension=xdebug") {
		t.Fatalf("php.ini = %q, want non-built-in disabled extension kept", phpIni)
	}
}

func TestStoreInstallAppliesOPcachePresetFrameworkAndUserConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{
		Framework:     "drupal",
		PHPVersion:    "8.4",
		OPcachePreset: "production",
		OPcacheConfig: map[string]string{
			"opcache.enable_cli":          "1",
			"opcache.validate_timestamps": "1",
		},
	}
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

	phpIniData, err := os.ReadFile(filepath.Join(filepath.Dir(results[0].TargetPath), "php.ini"))
	if err != nil {
		t.Fatalf("ReadFile(installed php.ini) error = %v", err)
	}
	phpIni := string(phpIniData)
	for _, want := range []string{
		"opcache.enable=1",
		"opcache.enable_cli=1",
		"opcache.file_update_protection=0",
		"opcache.save_comments=1",
		"opcache.validate_timestamps=1",
	} {
		if !strings.Contains(phpIni, want) {
			t.Fatalf("php.ini = %q, want %s", phpIni, want)
		}
	}
	if strings.Contains(phpIni, "opcache.revalidate_freq") {
		t.Fatalf("php.ini = %q, want no dev-only revalidate_freq for production preset", phpIni)
	}
}

func TestStoreInstallAppliesFrameworkPHPExtensionsWithoutConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{
		Framework:  "drupal",
		PHPVersion: "8.4",
	}
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

	phpIniData, err := os.ReadFile(filepath.Join(filepath.Dir(results[0].TargetPath), "php.ini"))
	if err != nil {
		t.Fatalf("ReadFile(installed php.ini) error = %v", err)
	}
	phpIni := string(phpIniData)
	for _, want := range []string{"extension=gd", "extension=mbstring", "extension=pdo_mysql", "zend_extension=opcache"} {
		if !strings.Contains(phpIni, want) {
			t.Fatalf("php.ini = %q, want framework default %s", phpIni, want)
		}
	}

	loadedConfig, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if len(loadedConfig.Environments["demo"].PHPExtensions) != 0 {
		t.Fatalf("stored php-extensions = %#v, want framework defaults not written", loadedConfig.Environments["demo"].PHPExtensions)
	}
}

func TestStoreInstallFrameworkPHPExtensionsHonorUserOverrides(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{
		Framework:  "drupal",
		PHPVersion: "8.4",
		PHPExtensions: map[string]bool{
			"gd":     false,
			"xdebug": true,
		},
	}
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

	phpIniData, err := os.ReadFile(filepath.Join(filepath.Dir(results[0].TargetPath), "php.ini"))
	if err != nil {
		t.Fatalf("ReadFile(installed php.ini) error = %v", err)
	}
	phpIni := string(phpIniData)
	for _, want := range []string{";extension=gd", "extension=mbstring", "extension=xdebug"} {
		if !strings.Contains(phpIni, want) {
			t.Fatalf("php.ini = %q, want merged extension entry %s", phpIni, want)
		}
	}
}

func TestStoreFrameworkOPcacheConfigHonorsUserOverrides(t *testing.T) {
	store := NewProjectStore(t.TempDir())

	environment := store.withFrameworkOPcacheConfig(Environment{
		Framework: "drupal",
		OPcacheConfig: map[string]string{
			"opcache.save_comments": "0",
		},
	})

	if environment.OPcacheConfig["opcache.save_comments"] != "0" {
		t.Fatalf("OPcache config = %#v, want user override for save_comments", environment.OPcacheConfig)
	}
}

func TestStoreInstallPreservesExistingConfigComments(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")

	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")
	configData := []byte(strings.Join([]string{
		"# keep this comment",
		"tools:",
		"  php: \"8.4\"",
		"",
	}, "\n"))
	if err := os.WriteFile(store.environmentConfigFile("demo"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	if err := os.WriteFile(store.ConfigFile, []byte("version: 1\nroot: .polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(project config) error = %v", err)
	}

	if _, err := store.Install("demo"); err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}

	updatedConfig, err := os.ReadFile(store.environmentConfigFile("demo"))
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
		"",
	}, "\n"))
	if err := os.WriteFile(store.ConfigFile, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	demoConfigData := []byte(strings.Join([]string{
		"# environment comment",
		"tools:",
		"  # php version comment",
		"  php: \"8.4\" # inline php comment",
		"",
	}, "\n"))
	if err := os.WriteFile(store.environmentConfigFile("demo"), demoConfigData, 0o644); err != nil {
		t.Fatalf("WriteFile(demo config) error = %v", err)
	}

	if _, err := store.Configure("demo", "8.3", "", nil); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}

	updatedConfig, err := os.ReadFile(store.environmentConfigFile("demo"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	for _, comment := range []string{"# environment comment", "# php version comment", "# inline php comment"} {
		if !strings.Contains(string(updatedConfig), comment) {
			t.Fatalf("config after Configure() = %q, want preserved comment %q", string(updatedConfig), comment)
		}
	}
	projectConfig, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(project config) error = %v", err)
	}
	if !strings.Contains(string(projectConfig), "# top comment") {
		t.Fatalf("project config after Configure() = %q, want preserved top comment", string(projectConfig))
	}
	config, err := store.readConfig()
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if config.Environments["demo"].PHPVersion != "8.3" {
		t.Fatalf("config.Environments[demo].PHPVersion = %q, want 8.3", config.Environments["demo"].PHPVersion)
	}
}

func TestStoreConfigureValuePreservesExistingConfigComments(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	if err := os.WriteFile(store.ConfigFile, []byte("version: 1\nroot: .polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(project config) error = %v", err)
	}
	configData := []byte(strings.Join([]string{
		"# environment comment",
		"tools:",
		"  # mailpit version comment",
		"  mailpit: \"1.30\" # inline mailpit comment",
		"",
	}, "\n"))
	if err := os.WriteFile(store.environmentConfigFile("demo"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(demo config) error = %v", err)
	}

	environment, err := store.ConfigureValue("demo", "settings.mailpit.smtp-port", "1125")
	if err != nil {
		t.Fatalf("ConfigureValue(demo) error = %v", err)
	}
	if environment.Mailpit == nil || environment.Mailpit.Version != "1.30" || environment.Mailpit.SMTPPort != 1125 {
		t.Fatalf("environment.Mailpit = %#v, want version and smtp port", environment.Mailpit)
	}

	updatedConfig, err := os.ReadFile(store.environmentConfigFile("demo"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	for _, comment := range []string{"# environment comment", "# mailpit version comment", "# inline mailpit comment"} {
		if !strings.Contains(string(updatedConfig), comment) {
			t.Fatalf("config after ConfigureValue() = %q, want preserved comment %q", string(updatedConfig), comment)
		}
	}
	if !strings.Contains(string(updatedConfig), "settings:\n  mailpit:\n    smtp-port: 1125") {
		t.Fatalf("config after ConfigureValue() = %q, want mailpit settings", string(updatedConfig))
	}
}

func TestStoreConfigureValueInfersDatabaseVersionFromTool(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	if _, err := store.ConfigureValue("data", "tools.mysql", "8.0"); err != nil {
		t.Fatalf("ConfigureValue(tools.mysql) error = %v", err)
	}
	if _, err := store.ConfigureValue("data", "database.engine", "mysql"); err != nil {
		t.Fatalf("ConfigureValue(database.engine) error = %v", err)
	}
	environment, err := store.ConfigureValue("data", "database.port", "3306")
	if err != nil {
		t.Fatalf("ConfigureValue(database.port) error = %v", err)
	}
	if environment.Database == nil || environment.Database.Engine != "mysql" || environment.Database.Version != "8.0" || environment.Database.Port != 3306 {
		t.Fatalf("environment.Database = %#v, want mysql 8.0 on 3306", environment.Database)
	}

	configData, err := os.ReadFile(store.environmentConfigFile("data"))
	if err != nil {
		t.Fatalf("ReadFile(data config) error = %v", err)
	}
	configText := string(configData)
	if !strings.Contains(configText, "tools:\n  mysql: \"8.0\"") || !strings.Contains(configText, "database:\n  engine: mysql\n  port: 3306") {
		t.Fatalf("config = %q, want tools mysql and root database settings", configText)
	}
	if strings.Contains(configText, "  version:") {
		t.Fatalf("config = %q, want no root-level database version", configText)
	}
}

func TestStoreConfigureValueValidatesBeforeWrite(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	if err := os.WriteFile(store.ConfigFile, []byte("version: 1\nroot: .polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(project config) error = %v", err)
	}
	configData := []byte("tools:\n  php: \"8.4\"\n")
	if err := os.WriteFile(store.environmentConfigFile("demo"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(demo config) error = %v", err)
	}

	if _, err := store.ConfigureValue("demo", "tools.php", "bad version!"); err == nil {
		t.Fatal("ConfigureValue(invalid version) error = nil, want validation error")
	}
	updatedConfig, err := os.ReadFile(store.environmentConfigFile("demo"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	if string(updatedConfig) != string(configData) {
		t.Fatalf("config after failed ConfigureValue() = %q, want original %q", string(updatedConfig), string(configData))
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
	sort.Strings(progressEvents)
	sort.Strings(want)

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
		t.Fatal("ResolveTool(mysql) error = nil, want default environment missing mysql error")
	} else if !strings.Contains(err.Error(), "environment \"default\" does not define a mysql version") {
		t.Fatalf("ResolveTool(mysql) error = %v, want default environment missing mysql error", err)
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

func TestStoreInstallDownloadsConfiguredSQLite(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	store.Downloader = fakeDownloader(func(cacheDir, tool, version string) error {
		_ = writeCachedTool(t, cacheDir, tool, version)
		return nil
	})

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{SQLiteVersion: "3.53"}
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
	if result.Tool != toolSQLite || result.Version != "3.53" {
		t.Fatalf("Install(demo) result = %#v, want sqlite 3.53", result)
	}
	if err := store.Use("demo"); err != nil {
		t.Fatalf("Use(demo) error = %v", err)
	}

	resolvedPath, err := store.ResolveTool("sqlite3")
	if err != nil {
		t.Fatalf("ResolveTool(sqlite3) error = %v", err)
	}
	if resolvedPath != result.TargetPath {
		t.Fatalf("ResolveTool(sqlite3) = %q, want %q", resolvedPath, result.TargetPath)
	}
}

func TestStoreInstallDownloadsMultipleConfiguredDatabases(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	store.Downloader = fakeDownloader(func(cacheDir, tool, version string) error {
		_ = writeCachedTool(t, cacheDir, tool, version)
		return nil
	})

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{
		MySQLVersion:   "8.4",
		MariaDBVersion: "11.8",
		Database:       &DatabaseConfig{Engine: toolMariaDB, Version: "11.8", Port: 3307},
	}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	results, err := store.Install("demo")
	if err != nil {
		t.Fatalf("Install(demo) error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Install(demo) length = %d, want 2", len(results))
	}
	got := []string{
		results[0].Tool + ":" + results[0].Version,
		results[1].Tool + ":" + results[1].Version,
	}
	want := []string{"mysql:8.4", "mariadb:11.8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Install(demo) results = %#v, want %#v", got, want)
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

func TestStoreInstallDownloadsConfiguredMago(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	store.Downloader = fakeDownloader(func(cacheDir, tool, version string) error {
		_ = writeCachedTool(t, cacheDir, tool, version)
		return nil
	})

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{MagoVersion: "1.27"}
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
	if result.Tool != toolMago || result.Version != "1.27" {
		t.Fatalf("Install(demo) result = %#v, want mago 1.27", result)
	}
	if !result.Downloaded {
		t.Fatalf("Install(demo) Downloaded = false, want true after cache miss")
	}
	assertPathExists(t, result.TargetPath)
	if !strings.Contains(result.TargetPath, filepath.Join("envs", toolMago, "1.27")) {
		t.Fatalf("Install(demo) target = %q, want versioned mago env path", result.TargetPath)
	}

	if err := store.Use("demo"); err != nil {
		t.Fatalf("Use(demo) error = %v", err)
	}
	resolvedPath, err := store.ResolveTool(toolMago)
	if err != nil {
		t.Fatalf("ResolveTool(mago) error = %v", err)
	}
	if resolvedPath != result.TargetPath {
		t.Fatalf("ResolveTool(mago) = %q, want %q", resolvedPath, result.TargetPath)
	}
	assertPathExists(t, filepath.Join(store.BinDir, toolMago))
	assertPathExists(t, filepath.Join(store.BinDir, toolMago+".cmd"))
}

func TestStoreInstallDownloadsConfiguredMailpit(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	store.Downloader = fakeDownloader(func(cacheDir, tool, version string) error {
		_ = writeCachedTool(t, cacheDir, tool, version)
		return nil
	})

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{Mailpit: &MailpitConfig{Version: "1.30"}}
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
	if result.Tool != toolMailpit || result.Version != "1.30" {
		t.Fatalf("Install(demo) result = %#v, want mailpit 1.30", result)
	}
	if !result.Downloaded {
		t.Fatalf("Install(demo) Downloaded = false, want true after cache miss")
	}
	assertPathExists(t, result.TargetPath)

	if err := store.Use("demo"); err != nil {
		t.Fatalf("Use(demo) error = %v", err)
	}
	resolvedPath, err := store.ResolveTool(toolMailpit)
	if err != nil {
		t.Fatalf("ResolveTool(mailpit) error = %v", err)
	}
	if resolvedPath != result.TargetPath {
		t.Fatalf("ResolveTool(mailpit) = %q, want %q", resolvedPath, result.TargetPath)
	}
	assertPathExists(t, filepath.Join(store.BinDir, toolMailpit))
	assertPathExists(t, filepath.Join(store.BinDir, toolMailpit+".cmd"))
}

func TestStoreInstallDownloadsConfiguredPHPMyAdmin(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	store.Downloader = fakeDownloader(func(cacheDir, tool, version string) error {
		_ = writeCachedTool(t, cacheDir, tool, version)
		return nil
	})

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{PHPMyAdmin: &PHPMyAdminConfig{Version: "5.2", Port: 8082}}
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
	if result.Tool != toolPHPMyAdmin || result.Version != "5.2" {
		t.Fatalf("Install(demo) result = %#v, want phpmyadmin 5.2", result)
	}
	if !result.Downloaded {
		t.Fatalf("Install(demo) Downloaded = false, want true after cache miss")
	}
	assertPathExists(t, result.TargetPath)
	if !strings.Contains(result.TargetPath, filepath.Join("envs", toolPHPMyAdmin, "5.2")) {
		t.Fatalf("Install(demo) target = %q, want versioned phpmyadmin env path", result.TargetPath)
	}

	if err := store.Use("demo"); err != nil {
		t.Fatalf("Use(demo) error = %v", err)
	}
	assertPathMissing(t, filepath.Join(store.BinDir, toolPHPMyAdmin))
	assertPathMissing(t, filepath.Join(store.BinDir, toolPHPMyAdmin+".cmd"))
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
	config.Environments["demo"] = Environment{NodeJSVersion: "24"}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}
	if err := store.writeActiveEnvironmentName("demo"); err != nil {
		t.Fatalf("writeActiveEnvironmentName() error = %v", err)
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

func TestStoreInstallRejectsOPcacheConfigWithoutPHP(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{
		ComposerVersion: "2.8",
		OPcachePreset:   "dev",
	}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	if _, err := store.Install("demo"); err == nil {
		t.Fatal("Install(demo) error = nil, want OPcache validation error")
	} else if !strings.Contains(err.Error(), "OPcache config") {
		t.Fatalf("Install(demo) error = %v, want OPcache validation error", err)
	}
}

func TestStoreCurrentNormalizesServerConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{
		PHPVersion: "8.4",
		HTTPS:      true,
		Server: &ServerConfig{
			Hostname: " localhost ",
			Port:     8080,
		},
	}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}
	if err := store.writeActiveEnvironmentName("demo"); err != nil {
		t.Fatalf("writeActiveEnvironmentName() error = %v", err)
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

func TestStoreCurrentNormalizesPHPMyAdminConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{
		HTTPS: true,
		PHPMyAdmin: &PHPMyAdminConfig{
			Version: " 5.2 ",
			Port:    8082,
		},
	}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}
	if err := store.writeActiveEnvironmentName("demo"); err != nil {
		t.Fatalf("writeActiveEnvironmentName() error = %v", err)
	}

	current, err := store.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if current == nil || current.PHPMyAdmin == nil {
		t.Fatalf("Current() = %#v, want phpmyadmin config", current)
	}
	if current.PHPMyAdmin.Version != "5.2" || current.PHPMyAdmin.Port != 8082 || !current.PHPMyAdmin.HTTPS {
		t.Fatalf("Current().PHPMyAdmin = %#v, want normalized version, port, and https", current.PHPMyAdmin)
	}
}

func TestStoreCurrentInheritsRootHTTPS(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{
		HTTPS:      true,
		Server:     &ServerConfig{Hostname: "localhost", Port: 8443},
		Mailpit:    &MailpitConfig{Version: "1.30", SMTPPort: 1125, UIPort: 8125},
		PHPMyAdmin: &PHPMyAdminConfig{Version: "5.2", Port: 8082},
	}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}
	if err := store.writeActiveEnvironmentName("demo"); err != nil {
		t.Fatalf("writeActiveEnvironmentName() error = %v", err)
	}

	current, err := store.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if current == nil || !current.HTTPS {
		t.Fatalf("Current() = %#v, want root https enabled", current)
	}
	if current.Server == nil || !current.Server.HTTPS {
		t.Fatalf("Current().Server = %#v, want inherited https", current.Server)
	}
	if current.Mailpit == nil || !current.Mailpit.HTTPS {
		t.Fatalf("Current().Mailpit = %#v, want inherited https", current.Mailpit)
	}
	if current.PHPMyAdmin == nil || !current.PHPMyAdmin.HTTPS {
		t.Fatalf("Current().PHPMyAdmin = %#v, want inherited https", current.PHPMyAdmin)
	}
}

func TestStoreWriteConfigPersistsHTTPSAtEnvironmentRoot(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{
		HTTPS:      true,
		Server:     &ServerConfig{Hostname: "localhost", Port: 8443, HTTPS: true},
		Mailpit:    &MailpitConfig{Version: "1.30", SMTPPort: 1125, UIPort: 8125, HTTPS: true},
		PHPMyAdmin: &PHPMyAdminConfig{Version: "5.2", Port: 8082, HTTPS: true},
	}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	data, err := os.ReadFile(store.environmentConfigFile("demo"))
	if err != nil {
		t.Fatalf("ReadFile(demo config) error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "\nhttps: true\n") {
		t.Fatalf("config = %q, want root-level https", text)
	}
	if strings.Contains(text, "    https: true") || strings.Contains(text, "  https: true") {
		t.Fatalf("config = %q, want no nested https keys", text)
	}
}

func TestStoreWritesToolSettingsSeparatelyFromVersions(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	config := store.defaultConfig()
	config.Environments["demo"] = Environment{
		Mailpit:    &MailpitConfig{Version: "1.30", SMTPPort: 1125, UIPort: 8125},
		PHPMyAdmin: &PHPMyAdminConfig{Version: "5.2", Port: 8082},
	}
	if err := store.writeConfig(config); err != nil {
		t.Fatalf("writeConfig() error = %v", err)
	}

	data, err := os.ReadFile(store.environmentConfigFile("demo"))
	if err != nil {
		t.Fatalf("ReadFile(demo config) error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "tools:\n  mailpit: \"1.30\"\n  phpmyadmin: \"5.2\"") {
		t.Fatalf("config = %q, want mailpit and phpmyadmin version labels under tools", text)
	}
	if !strings.Contains(text, "settings:\n  mailpit:\n    smtp-port: 1125\n    ui-port: 8125\n  phpmyadmin:\n    port: 8082") {
		t.Fatalf("config = %q, want mailpit and phpmyadmin ports under settings", text)
	}
	if strings.Contains(text, "    version:") {
		t.Fatalf("config = %q, want no nested tool version entries", text)
	}
}

func TestStoreReadsToolSettingsSeparatedFromVersions(t *testing.T) {
	for _, test := range []struct {
		name     string
		envName  string
		writeEnv func(t *testing.T, store Store, data []byte)
	}{
		{
			name:    "default",
			envName: defaultEnvironmentName,
			writeEnv: func(t *testing.T, store Store, data []byte) {
				t.Helper()
				if err := os.WriteFile(store.ConfigFile, data, 0o644); err != nil {
					t.Fatalf("WriteFile(project config) error = %v", err)
				}
			},
		},
		{
			name:    "named",
			envName: "demo",
			writeEnv: func(t *testing.T, store Store, data []byte) {
				t.Helper()
				if err := os.WriteFile(store.ConfigFile, []byte("version: 1\nroot: .polka\n"), 0o644); err != nil {
					t.Fatalf("WriteFile(project config) error = %v", err)
				}
				if err := os.WriteFile(store.environmentConfigFile("demo"), data, 0o644); err != nil {
					t.Fatalf("WriteFile(demo config) error = %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectDir := t.TempDir()
			store := NewProjectStore(projectDir)
			configData := []byte(strings.Join([]string{
				"version: 1",
				"root: .polka",
				"tools:",
				"  mailpit: \"1.30\"",
				"  phpmyadmin: \"5.2\"",
				"settings:",
				"  mailpit:",
				"    smtp-port: 1125",
				"    ui-port: 8125",
				"  phpmyadmin:",
				"    port: 8082",
				"",
			}, "\n"))
			if test.envName != defaultEnvironmentName {
				configData = []byte(strings.Join([]string{
					"tools:",
					"  mailpit: \"1.30\"",
					"  phpmyadmin: \"5.2\"",
					"settings:",
					"  mailpit:",
					"    smtp-port: 1125",
					"    ui-port: 8125",
					"  phpmyadmin:",
					"    port: 8082",
					"",
				}, "\n"))
			}
			test.writeEnv(t, store, configData)

			environment, ok, err := store.readEnvironment(test.envName)
			if err != nil {
				t.Fatalf("readEnvironment(%s) error = %v", test.envName, err)
			}
			if !ok {
				t.Fatalf("readEnvironment(%s) ok = false, want true", test.envName)
			}
			if environment.Mailpit == nil || environment.Mailpit.Version != "1.30" || environment.Mailpit.SMTPPort != 1125 || environment.Mailpit.UIPort != 8125 {
				t.Fatalf("environment.Mailpit = %#v, want version and configured ports", environment.Mailpit)
			}
			if environment.PHPMyAdmin == nil || environment.PHPMyAdmin.Version != "5.2" || environment.PHPMyAdmin.Port != 8082 {
				t.Fatalf("environment.PHPMyAdmin = %#v, want version and configured port", environment.PHPMyAdmin)
			}
		})
	}
}

func TestStoreCurrentNormalizesEnvironmentVariables(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	config := store.defaultConfig()
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
	if err := store.writeActiveEnvironmentName("demo"); err != nil {
		t.Fatalf("writeActiveEnvironmentName() error = %v", err)
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
		"",
	}, "\n"))
	if err := os.WriteFile(store.ConfigFile, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	demoConfigData := []byte(strings.Join([]string{
		"# existing environment comment",
		"tools:",
		"  php: \"8.4\"",
		"",
	}, "\n"))
	if err := os.WriteFile(store.environmentConfigFile("demo"), demoConfigData, 0o644); err != nil {
		t.Fatalf("WriteFile(demo config) error = %v", err)
	}

	if _, err := store.Create("api", "8.3", "2.7", nil); err != nil {
		t.Fatalf("Create(api) error = %v", err)
	}

	updatedConfig, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	if !strings.Contains(string(updatedConfig), "# project comment") {
		t.Fatalf("config after Create() = %q, want preserved project comment", string(updatedConfig))
	}
	updatedDemoConfig, err := os.ReadFile(store.environmentConfigFile("demo"))
	if err != nil {
		t.Fatalf("ReadFile(demo config) error = %v", err)
	}
	if !strings.Contains(string(updatedDemoConfig), "# existing environment comment") {
		t.Fatalf("demo config after Create() = %q, want preserved existing environment comment", string(updatedDemoConfig))
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
	if len(environments) != 3 {
		t.Fatalf("List() length = %d, want 3", len(environments))
	}
	if environments[0].Name != "api" || environments[1].Name != defaultEnvironmentName || environments[2].Name != "web" {
		t.Fatalf("List() names = %#v, want sorted names including default", environments)
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
	if current == nil || current.Name != defaultEnvironmentName {
		t.Fatalf("Current() after remove = %#v, want default environment", current)
	}

	environments, err = store.List()
	if err != nil {
		t.Fatalf("List() after remove error = %v", err)
	}
	if len(environments) != 2 || environments[0].Name != defaultEnvironmentName || environments[1].Name != "web" {
		t.Fatalf("List() after remove = %#v, want default and web", environments)
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
		"",
	}, "\n"))
	if err := os.WriteFile(store.ConfigFile, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	demoConfigData := []byte("tools:\n  php: \"8.4\"\n")
	if err := os.WriteFile(store.environmentConfigFile("demo"), demoConfigData, 0o644); err != nil {
		t.Fatalf("WriteFile(demo config) error = %v", err)
	}
	webConfigData := []byte(strings.Join([]string{
		"# keep this environment comment",
		"tools:",
		"  php: \"8.3\" # keep this inline comment",
		"",
	}, "\n"))
	if err := os.WriteFile(store.environmentConfigFile("web"), webConfigData, 0o644); err != nil {
		t.Fatalf("WriteFile(web config) error = %v", err)
	}
	if err := store.writeActiveEnvironmentName("demo"); err != nil {
		t.Fatalf("writeActiveEnvironmentName() error = %v", err)
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
	if !strings.Contains(string(updatedConfig), "# project comment") {
		t.Fatalf("config after Remove() = %q, want preserved project comment", string(updatedConfig))
	}
	updatedWebConfig, err := os.ReadFile(store.environmentConfigFile("web"))
	if err != nil {
		t.Fatalf("ReadFile(web config) error = %v", err)
	}
	for _, comment := range []string{"# keep this environment comment", "# keep this inline comment"} {
		if !strings.Contains(string(updatedWebConfig), comment) {
			t.Fatalf("web config after Remove() = %q, want preserved comment %q", string(updatedWebConfig), comment)
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

func TestStoreWritesDatabaseToolVersionAndRootRuntimeConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	if _, err := store.Configure("data", "", "", &DatabaseConfig{Engine: toolMariaDB, Version: "11.8", Port: 3307}); err != nil {
		t.Fatalf("Configure(data) error = %v", err)
	}

	configData, err := os.ReadFile(store.environmentConfigFile("data"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	configText := string(configData)
	if !strings.Contains(configText, "tools:\n  mariadb: \"11.8\"") {
		t.Fatalf("config = %q, want mariadb version under tools", configText)
	}
	if !strings.Contains(configText, "database:\n  engine: mariadb\n  port: 3307") {
		t.Fatalf("config = %q, want root-level database engine and port", configText)
	}
	if strings.Contains(configText, "  version:") {
		t.Fatalf("config = %q, want no root-level database version entry", configText)
	}
}

func TestStoreReadsDatabaseToolVersionAndRootRuntimeConfig(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	configData := []byte(strings.Join([]string{
		"tools:",
		"  mysql: \"8.4\"",
		"  mariadb: \"11.8\"",
		"database:",
		"  engine: mariadb",
		"  port: 3307",
		"",
	}, "\n"))
	if err := os.WriteFile(store.environmentConfigFile("data"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	if err := os.WriteFile(store.ConfigFile, []byte("version: 1\nroot: .polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(project config) error = %v", err)
	}

	environment, ok, err := store.readEnvironment("data")
	if err != nil {
		t.Fatalf("readEnvironment(data) error = %v", err)
	}
	if !ok {
		t.Fatal("readEnvironment(data) ok = false, want true")
	}
	if environment.Database == nil || environment.Database.Engine != toolMariaDB || environment.Database.Version != "11.8" || environment.Database.Port != 3307 {
		t.Fatalf("environment.Database = %#v, want mariadb 11.8 on port 3307", environment.Database)
	}
	if environment.MySQLVersion != "8.4" || environment.MariaDBVersion != "11.8" {
		t.Fatalf("environment database tool versions = mysql:%q mariadb:%q, want mysql:8.4 mariadb:11.8", environment.MySQLVersion, environment.MariaDBVersion)
	}
}

func TestStoreRejectsNonVersionToolsAndOrphanSettings(t *testing.T) {
	for _, test := range []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name: "nested mailpit",
			config: strings.Join([]string{
				"tools:",
				"  mailpit:",
				"    version: \"1.30\"",
				"    smtp-port: 1025",
				"",
			}, "\n"),
			wantErr: "tools.mailpit must be a scalar version label",
		},
		{
			name: "nested phpmyadmin",
			config: strings.Join([]string{
				"tools:",
				"  phpmyadmin:",
				"    version: \"5.2\"",
				"    port: 8082",
				"",
			}, "\n"),
			wantErr: "tools.phpmyadmin must be a scalar version label",
		},
		{
			name: "tools database object",
			config: strings.Join([]string{
				"tools:",
				"  database:",
				"    engine: mysql",
				"    version: \"8.0\"",
				"    port: 3306",
				"",
			}, "\n"),
			wantErr: "tools.database is no longer supported",
		},
		{
			name: "tools database scalar",
			config: strings.Join([]string{
				"tools:",
				"  database: \"8.0\"",
				"",
			}, "\n"),
			wantErr: "tools.database is no longer supported",
		},
		{
			name: "orphan mailpit settings",
			config: strings.Join([]string{
				"settings:",
				"  mailpit:",
				"    ui-port: 8025",
				"",
			}, "\n"),
			wantErr: "settings.mailpit requires tools.mailpit",
		},
		{
			name: "orphan phpmyadmin settings",
			config: strings.Join([]string{
				"settings:",
				"  phpmyadmin:",
				"    port: 8082",
				"",
			}, "\n"),
			wantErr: "settings.phpmyadmin requires tools.phpmyadmin",
		},
		{
			name: "invalid opcache preset",
			config: strings.Join([]string{
				"opcache-preset: staging",
				"",
			}, "\n"),
			wantErr: "unsupported opcache-preset",
		},
		{
			name: "non string opcache preset",
			config: strings.Join([]string{
				"opcache-preset: true",
				"",
			}, "\n"),
			wantErr: "opcache-preset must be one of none, dev, or production",
		},
		{
			name: "scalar opcache config",
			config: strings.Join([]string{
				"opcache-config: true",
				"",
			}, "\n"),
			wantErr: "opcache-config must be a mapping",
		},
		{
			name: "unsupported opcache config key",
			config: strings.Join([]string{
				"opcache-config:",
				"  zend_extension: opcache",
				"",
			}, "\n"),
			wantErr: "unsupported opcache-config.zend_extension key",
		},
		{
			name: "nested opcache config value",
			config: strings.Join([]string{
				"opcache-config:",
				"  opcache.enable:",
				"    nested: true",
				"",
			}, "\n"),
			wantErr: "opcache-config.opcache.enable must be a scalar",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectDir := t.TempDir()
			store := NewProjectStore(projectDir)
			if err := os.WriteFile(store.ConfigFile, []byte("version: 1\nroot: .polka\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(project config) error = %v", err)
			}
			if err := os.WriteFile(store.environmentConfigFile("data"), []byte(test.config), 0o644); err != nil {
				t.Fatalf("WriteFile(config) error = %v", err)
			}

			_, _, err := store.readEnvironment("data")
			if err == nil {
				t.Fatal("readEnvironment(data) error = nil, want schema error")
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("readEnvironment(data) error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestResolveToolRequiresConfiguredVersion(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)

	projectConfig := "version: 1\nroot: .polka\n"
	if err := os.WriteFile(store.ConfigFile, []byte(projectConfig), 0o644); err != nil {
		t.Fatalf("WriteFile(project config) error = %v", err)
	}
	demoConfig := "tools:\n  composer: 2.8\n"
	if err := os.WriteFile(store.environmentConfigFile("demo"), []byte(demoConfig), 0o644); err != nil {
		t.Fatalf("WriteFile(demo config) error = %v", err)
	}
	if err := store.writeActiveEnvironmentName("demo"); err != nil {
		t.Fatalf("writeActiveEnvironmentName() error = %v", err)
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
	configData := []byte("version: 1\nroot: test-site/.polka\n")
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	demoConfigData := []byte("tools:\n  php: \"8.4\"\n")
	if err := os.WriteFile(filepath.Join(projectDir, "polka.demo.yaml"), demoConfigData, 0o644); err != nil {
		t.Fatalf("WriteFile(demo config) error = %v", err)
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
	if err := store.writeActiveEnvironmentName("demo"); err != nil {
		t.Fatalf("writeActiveEnvironmentName() error = %v", err)
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
	if tool == toolPHP {
		path = cachedFakePHPPath(cacheDir, version)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if tool == toolPHP {
		if err := os.WriteFile(path, fakePHPModuleListScript(nil), 0o755); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
		writeCachedPHPCABundle(t, cacheDir, version)

		return path
	}
	if err := os.WriteFile(path, []byte("placeholder\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}

	return path
}

func writeCachedPHPToolWithBuiltInModules(t *testing.T, cacheDir, version string, modules []string) string {
	t.Helper()

	path := cachedFakePHPPath(cacheDir, version)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, fakePHPModuleListScript(modules), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	writeCachedPHPCABundle(t, cacheDir, version)

	return path
}

func writeCachedPHPCABundle(t *testing.T, cacheDir, version string) {
	t.Helper()

	path := filepath.Join(cacheDir, toolPHP, version, "extras", "ssl", "cacert.pem")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func cachedFakePHPPath(cacheDir, version string) string {
	candidates := toolInstallCandidatesIn(cacheDir, toolPHP, version)
	if runtime.GOOS == "windows" {
		for _, candidate := range candidates {
			extension := strings.ToLower(filepath.Ext(candidate))
			if extension == ".cmd" || extension == ".bat" {
				return candidate
			}
		}
	}

	return candidates[0]
}

func fakePHPModuleListScript(modules []string) []byte {
	if runtime.GOOS == "windows" {
		var builder strings.Builder
		builder.WriteString("@echo off\r\n")
		builder.WriteString("if \"%1\"==\"-nm\" (\r\n")
		builder.WriteString("  echo [PHP Modules]\r\n")
		for _, module := range modules {
			builder.WriteString("  echo ")
			builder.WriteString(module)
			builder.WriteString("\r\n")
		}
		builder.WriteString("  echo [Zend Modules]\r\n")
		builder.WriteString("  exit /b 0\r\n")
		builder.WriteString(")\r\n")
		builder.WriteString("echo fake-php %*\r\n")

		return []byte(builder.String())
	}

	var builder strings.Builder
	builder.WriteString("#!/usr/bin/env sh\n")
	builder.WriteString("if [ \"$1\" = \"-nm\" ]; then\n")
	builder.WriteString("  printf '%s\\n' '[PHP Modules]'\n")
	for _, module := range modules {
		builder.WriteString("  printf '%s\\n' '")
		builder.WriteString(module)
		builder.WriteString("'\n")
	}
	builder.WriteString("  printf '%s\\n' '[Zend Modules]'\n")
	builder.WriteString("  exit 0\n")
	builder.WriteString("fi\n")
	builder.WriteString("printf 'fake-php %s\\n' \"$*\"\n")

	return []byte(builder.String())
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
