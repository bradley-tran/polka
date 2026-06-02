package tools

import (
	"errors"
	"reflect"
	"testing"

	"polka/config"
)

func TestDefaultRegistryInstallRequestsUseConfiguredToolOrder(t *testing.T) {
	registry := NewDefaultRegistry()
	environment := config.Environment{
		PHPVersion:      "8.4",
		ComposerVersion: "2.8",
		NodeJSVersion:   "24",
		NginxVersion:    "1.30",
		Mailpit:         &config.MailpitConfig{Version: "1.30"},
		Database:        &config.DatabaseConfig{Engine: MariaDB, Version: "11.8"},
	}

	requests := registry.InstallRequests(environment)
	got := make([]string, 0, len(requests))
	for _, request := range requests {
		got = append(got, request.Tool+":"+request.Version)
	}

	want := []string{
		"php:8.4",
		"composer:2.8",
		"nodejs:24",
		"nginx:1.30",
		"mailpit:1.30",
		"mariadb:11.8",
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
