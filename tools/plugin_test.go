package tools

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"polka/config"
)

func TestDefaultRegistryInstallRequestsUseConfiguredToolOrder(t *testing.T) {
	registry := NewDefaultRegistry()
	environment := config.Environment{
		PHPVersion:        "8.4",
		FrankenPHPVersion: "1.12",
		ComposerVersion:   "2.8",
		PIEVersion:        "1.4",
		NodeJSVersion:     "24",
		MagoVersion:       "1.27",
		NginxVersion:      "1.30",
		PHPMyAdmin:        &config.PHPMyAdminConfig{Version: "5.2", Port: 8081, HTTPS: true},
		Mailpit:           &config.MailpitConfig{Version: "1.30"},
		MySQLVersion:      "8.4",
		MariaDBVersion:    "11.8",
		PostgreSQLVersion: "17",
		SQLiteVersion:     "3.53",
		Database:          &config.DatabaseConfig{Engine: MariaDB, Version: "11.8"},
	}

	requests := registry.InstallRequests(environment)
	got := make([]string, 0, len(requests))
	for _, request := range requests {
		got = append(got, request.Tool+":"+request.Version)
	}

	want := []string{
		"php:8.4",
		"frankenphp:1.12",
		"composer:2.8",
		"pie:1.4",
		"nodejs:24",
		"mago:1.27",
		"nginx:1.30",
		"mailpit:1.30",
		"phpmyadmin:5.2",
		"mysql:8.4",
		"mariadb:11.8",
		"postgresql:17",
		"sqlite:3.53",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InstallRequests() = %#v, want %#v", got, want)
	}
}

func TestRegistryRejectsDuplicatePlugins(t *testing.T) {
	plugins := DefaultPlugins()
	if _, err := NewRegistry(plugins[0], plugins[0]); err == nil {
		t.Fatal("NewRegistry(duplicate) error = nil, want duplicate plugin error")
	}
}

func TestDefaultRegistryKeepsNodeJSConfigOnly(t *testing.T) {
	registry := NewDefaultRegistry()

	request, err := registry.ResolveDispatchRequest(Node)
	if err != nil {
		t.Fatalf("ResolveDispatchRequest(node) error = %v", err)
	}
	if request.ConfigTool != NodeJS || request.Executable != Node {
		t.Fatalf("ResolveDispatchRequest(node) = %#v, want nodejs/node", request)
	}

	if _, err := registry.ResolveDispatchRequest(NodeJS); err == nil {
		t.Fatal("ResolveDispatchRequest(nodejs) error = nil, want unsupported tool error")
	}

	environment := config.Environment{NodeJSVersion: "24"}
	active := registry.ActiveCommandNames(&environment)
	if !reflect.DeepEqual(active, []string{Node, NPM, NPX}) {
		t.Fatalf("ActiveCommandNames(nodejs env) = %#v, want node/npm/npx", active)
	}

	cleanup := registry.CleanupCommandNames()
	if !containsString(cleanup, NodeJS) {
		t.Fatalf("CleanupCommandNames() = %#v, want legacy nodejs cleanup entry", cleanup)
	}
}

func TestDefaultRegistrySelectsConfiguredPHPProvider(t *testing.T) {
	registry := NewDefaultRegistry()
	tests := []struct {
		name        string
		environment config.Environment
		wantTool    string
	}{
		{name: "NTS", environment: config.Environment{PHPVersion: "8.4"}, wantTool: PHP},
		{name: "ZTS", environment: config.Environment{PHPZTSVersion: "8.4"}, wantTool: PHPZTS},
		{name: "FrankenPHP fallback", environment: config.Environment{FrankenPHPVersion: "1.12"}, wantTool: FrankenPHP},
		{name: "NTS before FrankenPHP", environment: config.Environment{PHPVersion: "8.4", FrankenPHPVersion: "1.12"}, wantTool: PHP},
		{name: "ZTS before FrankenPHP", environment: config.Environment{PHPZTSVersion: "8.4", FrankenPHPVersion: "1.12"}, wantTool: PHPZTS},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := registry.ResolveDispatchRequestForEnvironment(PHP, test.environment)
			if err != nil {
				t.Fatalf("ResolveDispatchRequestForEnvironment(php) error = %v", err)
			}
			if request.ConfigTool != test.wantTool || request.Executable != PHP {
				t.Fatalf("request = %#v, want %s/php", request, test.wantTool)
			}
		})
	}
}

func TestDefaultRegistryRejectsMultiplePHPRuntimes(t *testing.T) {
	registry := NewDefaultRegistry()
	err := registry.ValidateEnvironment(config.Environment{PHPVersion: "8.4", PHPZTSVersion: "8.4"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("ValidateEnvironment() error = %v, want mutually exclusive error", err)
	}
}

func TestDefaultRegistryValidatesPHPMyAdminConfig(t *testing.T) {
	registry := NewDefaultRegistry()

	if err := registry.ValidateEnvironment(config.Environment{PHPMyAdmin: &config.PHPMyAdminConfig{Version: "5.2", Port: 70000}}); err == nil {
		t.Fatal("ValidateEnvironment(phpmyadmin invalid port) error = nil, want port validation error")
	}
	if err := registry.ValidateEnvironment(config.Environment{PHPMyAdmin: &config.PHPMyAdminConfig{Port: 8081}}); err == nil {
		t.Fatal("ValidateEnvironment(phpmyadmin without version) error = nil, want version validation error")
	}
}

func TestDefaultRegistryValidatesOPcacheConfig(t *testing.T) {
	registry := NewDefaultRegistry()

	if err := registry.ValidateEnvironment(config.Environment{PHPVersion: "8.4", OPcachePreset: "staging"}); err == nil {
		t.Fatal("ValidateEnvironment(opcache invalid preset) error = nil, want preset validation error")
	}
	if err := registry.ValidateEnvironment(config.Environment{PHPVersion: "8.4", OPcacheConfig: map[string]string{"zend_extension": "opcache"}}); err == nil {
		t.Fatal("ValidateEnvironment(opcache invalid directive) error = nil, want directive validation error")
	}
}

func TestHTTPDownloaderRunsRegisteredPluginDownloadHook(t *testing.T) {
	var called DownloadContext
	plugin := testPlugin{
		id: "demo",
		download: func(ctx DownloadContext) error {
			called = ctx
			return nil
		},
	}
	registry, err := NewRegistry(plugin)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	if err := (HTTPDownloader{Plugins: registry}).Download("cache-root", "demo", "1.2.3"); err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if called.Client == nil || called.CacheDir != "cache-root" || called.Tool != "demo" || called.Version != "1.2.3" {
		t.Fatalf("download hook context = %#v, want populated context", called)
	}
}

func TestHTTPDownloaderReturnsPluginDownloadError(t *testing.T) {
	wantErr := errors.New("download failed")
	registry, err := NewRegistry(testPlugin{
		id: "demo",
		download: func(ctx DownloadContext) error {
			return wantErr
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	if err := (HTTPDownloader{Plugins: registry}).Download("cache-root", "demo", "1.2.3"); !errors.Is(err, wantErr) {
		t.Fatalf("Download() error = %v, want %v", err, wantErr)
	}
}

type testPlugin struct {
	id       string
	download func(DownloadContext) error
}

func (p testPlugin) ID() string {
	return p.id
}

func (p testPlugin) Version(config.Environment) string {
	return ""
}

func (p testPlugin) PHPExtensions() map[string]bool {
	return nil
}

func (p testPlugin) Validate(config.Environment) error {
	return nil
}

func (p testPlugin) InstallCandidates(root, version string) []string {
	return nil
}

func (p testPlugin) DispatchCommands() []string {
	return nil
}

func (p testPlugin) CleanupCommands() []string {
	return nil
}

func (p testPlugin) ActiveCommands(config.Environment) []string {
	return nil
}

func (p testPlugin) DispatchCandidates(root, executable, version string) []string {
	return nil
}

func (p testPlugin) Logs() []LogEntry {
	return nil
}

func (p testPlugin) Download(ctx DownloadContext) error {
	return p.download(ctx)
}

func (p testPlugin) PostInstall(InstallContext) error {
	return nil
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}

	return false
}
