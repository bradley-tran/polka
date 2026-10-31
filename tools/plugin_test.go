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
	// PIE is internal-only: a configured pie version never yields a project
	// install request (it would also fail validation; see manifest tests).
	environment := config.Environment{
		HTTPS: true,
		Tools: config.ToolsConfig{
			PHP:         "8.4",
			FrankenPHP:  "1.12",
			Composer:    "2.8",
			PIE:         "1.4",
			NodeJS:      "24",
			Mago:        "1.27",
			Nginx:       "1.30",
			Apache:      "2.4",
			PHPMyAdmin:  "5.2",
			Mailpit:     "1.30",
			Meilisearch: "1.48",
			Redis:       "8.8.0",
			MySQL:       "8.4",
			MariaDB:     "11.8",
			PostgreSQL:  "17",
			SQLite:      "3.53",
		},
		Settings: config.SettingsConfig{
			PHPMyAdmin:  {"port": 8081},
			Meilisearch: {"port": 7701, "master-key": "dev-key"},
			Redis:       {"port": 6380, "password": "dev-password"},
		},
		Database: &config.DatabaseConfig{Engine: MariaDB, Version: "11.8"},
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
		"nodejs:24",
		"mago:1.27",
		"nginx:1.30",
		"apache:2.4",
		"mailpit:1.30",
		"meilisearch:1.48",
		"redis:8.8.0",
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
	request, err = registry.ResolveDispatchRequest(Yarn)
	if err != nil {
		t.Fatalf("ResolveDispatchRequest(yarn) error = %v", err)
	}
	if request.ConfigTool != NodeJS || request.Executable != Yarn {
		t.Fatalf("ResolveDispatchRequest(yarn) = %#v, want nodejs/yarn", request)
	}

	if _, err := registry.ResolveDispatchRequest(NodeJS); err == nil {
		t.Fatal("ResolveDispatchRequest(nodejs) error = nil, want unsupported tool error")
	}

	environment := config.Environment{Tools: config.ToolsConfig{NodeJS: "24"}}
	active := registry.ActiveCommandNames(&environment)
	if !reflect.DeepEqual(active, []string{Node, NPM, NPX, Yarn}) {
		t.Fatalf("ActiveCommandNames(nodejs env) = %#v, want node/npm/npx/yarn", active)
	}

	cleanup := registry.CleanupCommandNames()
	if !containsString(cleanup, NodeJS) {
		t.Fatalf("CleanupCommandNames() = %#v, want legacy nodejs cleanup entry", cleanup)
	}
}

func TestDefaultRegistryResolvesApacheDispatchCommands(t *testing.T) {
	registry := NewDefaultRegistry()
	environment := config.Environment{Tools: config.ToolsConfig{Apache: "2.4"}}

	for _, command := range []string{Apache, "httpd"} {
		request, err := registry.ResolveDispatchRequestForEnvironment(command, environment)
		if err != nil {
			t.Fatalf("ResolveDispatchRequestForEnvironment(%s) error = %v", command, err)
		}
		if request.ConfigTool != Apache || request.Executable != command {
			t.Fatalf("request for %s = %#v, want apache/%s", command, request, command)
		}
	}
}

func TestDefaultRegistrySelectsConfiguredPHPProvider(t *testing.T) {
	registry := NewDefaultRegistry()
	tests := []struct {
		name        string
		environment config.Environment
		wantTool    string
	}{
		{name: "NTS", environment: config.Environment{Tools: config.ToolsConfig{PHP: "8.4"}}, wantTool: PHP},
		{name: "ZTS", environment: config.Environment{Tools: config.ToolsConfig{PHPZTS: "8.4"}}, wantTool: PHPZTS},
		{name: "FrankenPHP fallback", environment: config.Environment{Tools: config.ToolsConfig{FrankenPHP: "1.12"}}, wantTool: FrankenPHP},
		{name: "NTS before FrankenPHP", environment: config.Environment{Tools: config.ToolsConfig{PHP: "8.4", FrankenPHP: "1.12"}}, wantTool: PHP},
		{name: "ZTS before FrankenPHP", environment: config.Environment{Tools: config.ToolsConfig{PHPZTS: "8.4", FrankenPHP: "1.12"}}, wantTool: PHPZTS},
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
	err := registry.ValidateEnvironment(config.Environment{Tools: config.ToolsConfig{PHP: "8.4", PHPZTS: "8.4"}})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("ValidateEnvironment() error = %v, want mutually exclusive error", err)
	}
}

func TestDefaultRegistryValidatesPHPMyAdminConfig(t *testing.T) {
	registry := NewDefaultRegistry()

	if err := registry.ValidateEnvironment(config.Environment{Tools: config.ToolsConfig{PHPMyAdmin: "5.2"}, Settings: config.SettingsConfig{PHPMyAdmin: {"port": 70000}}}); err == nil {
		t.Fatal("ValidateEnvironment(phpmyadmin invalid port) error = nil, want port validation error")
	}
	if err := registry.ValidateEnvironment(config.Environment{Settings: config.SettingsConfig{PHPMyAdmin: {"port": 8081}}}); err == nil {
		t.Fatal("ValidateEnvironment(phpmyadmin without version) error = nil, want version validation error")
	}
}

func TestDefaultRegistryValidatesMeilisearchConfig(t *testing.T) {
	registry := NewDefaultRegistry()

	if err := registry.ValidateEnvironment(config.Environment{Tools: config.ToolsConfig{Meilisearch: "1.48"}, Settings: config.SettingsConfig{Meilisearch: {"port": 70000}}}); err == nil {
		t.Fatal("ValidateEnvironment(meilisearch invalid port) error = nil, want port validation error")
	}
	if err := registry.ValidateEnvironment(config.Environment{Settings: config.SettingsConfig{Meilisearch: {"port": 7700}}}); err == nil {
		t.Fatal("ValidateEnvironment(meilisearch without version) error = nil, want version validation error")
	}

	environment := config.Environment{Tools: config.ToolsConfig{Meilisearch: "1.48"}}
	active := registry.ActiveCommandNames(&environment)
	if !reflect.DeepEqual(active, []string{Meilisearch}) {
		t.Fatalf("ActiveCommandNames(meilisearch env) = %#v, want meilisearch", active)
	}
}

func TestDefaultRegistryValidatesRedisConfig(t *testing.T) {
	registry := NewDefaultRegistry()

	if err := registry.ValidateEnvironment(config.Environment{Tools: config.ToolsConfig{Redis: "8.8.0"}, Settings: config.SettingsConfig{Redis: {"port": 70000}}}); err == nil {
		t.Fatal("ValidateEnvironment(redis invalid port) error = nil, want port validation error")
	}
	if err := registry.ValidateEnvironment(config.Environment{Settings: config.SettingsConfig{Redis: {"port": 6379}}}); err == nil {
		t.Fatal("ValidateEnvironment(redis without version) error = nil, want version validation error")
	}

	// Redis ships two managed binaries, so it exposes both dispatch commands.
	environment := config.Environment{Tools: config.ToolsConfig{Redis: "8.8.0"}}
	active := registry.ActiveCommandNames(&environment)
	if !reflect.DeepEqual(active, []string{"redis-server", "redis-cli"}) {
		t.Fatalf("ActiveCommandNames(redis env) = %#v, want redis-server and redis-cli", active)
	}
}

func TestDefaultRegistryValidatesOPcacheConfig(t *testing.T) {
	registry := NewDefaultRegistry()

	if err := registry.ValidateEnvironment(config.Environment{Tools: config.ToolsConfig{PHP: "8.4"}, OPcachePreset: "staging"}); err == nil {
		t.Fatal("ValidateEnvironment(opcache invalid preset) error = nil, want preset validation error")
	}
	if err := registry.ValidateEnvironment(config.Environment{Tools: config.ToolsConfig{PHP: "8.4"}, OPcacheConfig: map[string]string{"zend_extension": "opcache"}}); err == nil {
		t.Fatal("ValidateEnvironment(opcache invalid directive) error = nil, want directive validation error")
	}
}

// TestDefaultRegistrySettingsSchemasAndExclusivePeers verifies that manifest
// settings schemas, shared settings-from namespaces, and exclusive-with peers
// are exposed through the registry.
func TestDefaultRegistrySettingsSchemasAndExclusivePeers(t *testing.T) {
	registry := NewDefaultRegistry()

	phpSchema, phpOwners, ok := registry.SettingsSchema(PHP)
	if !ok {
		t.Fatal("SettingsSchema(php) ok = false, want true")
	}
	if !reflect.DeepEqual(phpSchema, map[string]SettingType{"extension-sdk": SettingTypeBool}) {
		t.Fatalf("SettingsSchema(php) = %#v, want extension-sdk: bool", phpSchema)
	}
	if !reflect.DeepEqual(phpOwners, []string{PHP, PHPZTS}) {
		t.Fatalf("SettingsSchema(php) owners = %#v, want [php php-zts]", phpOwners)
	}

	redisSchema, redisOwners, ok := registry.SettingsSchema(Redis)
	if !ok {
		t.Fatal("SettingsSchema(redis) ok = false, want true")
	}
	wantRedis := map[string]SettingType{"port": SettingTypePort, "password": SettingTypeString}
	if !reflect.DeepEqual(redisSchema, wantRedis) || !reflect.DeepEqual(redisOwners, []string{Redis}) {
		t.Fatalf("SettingsSchema(redis) = (%#v, %#v), want (%#v, [redis])", redisSchema, redisOwners, wantRedis)
	}

	if _, _, ok := registry.SettingsSchema(Composer); ok {
		t.Fatal("SettingsSchema(composer) ok = true, want false for tool without settings")
	}

	if peers := registry.ExclusivePeers(PHP); !reflect.DeepEqual(peers, []string{PHPZTS}) {
		t.Fatalf("ExclusivePeers(php) = %#v, want [php-zts]", peers)
	}
	if peers := registry.ExclusivePeers(PHPZTS); !reflect.DeepEqual(peers, []string{PHP}) {
		t.Fatalf("ExclusivePeers(php-zts) = %#v, want [php]", peers)
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
