package backend

import (
	"errors"
	"reflect"
	"testing"
)

func TestDefaultToolRegistryInstallRequestsUseConfiguredToolOrder(t *testing.T) {
	registry := NewDefaultToolRegistry()
	environment := Environment{
		PHPVersion:      "8.4",
		ComposerVersion: "2.8",
		NodeJSVersion:   "24",
		NginxVersion:    "1.30",
		Mailpit:         &MailpitConfig{Version: "1.30"},
		Database:        &DatabaseConfig{Engine: toolMariaDB, Version: "11.8"},
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

func TestToolRegistryRejectsDuplicatePlugins(t *testing.T) {
	if _, err := NewToolRegistry(phpToolPlugin(), phpToolPlugin()); err == nil {
		t.Fatal("NewToolRegistry(duplicate) error = nil, want duplicate plugin error")
	}
}

func TestDefaultToolRegistryKeepsNodeJSConfigOnly(t *testing.T) {
	registry := NewDefaultToolRegistry()

	request, err := registry.ResolveDispatchRequest(toolNode)
	if err != nil {
		t.Fatalf("ResolveDispatchRequest(node) error = %v", err)
	}
	if request.ConfigTool != toolNodeJS || request.Executable != toolNode {
		t.Fatalf("ResolveDispatchRequest(node) = %#v, want nodejs/node", request)
	}

	if _, err := registry.ResolveDispatchRequest(toolNodeJS); err == nil {
		t.Fatal("ResolveDispatchRequest(nodejs) error = nil, want unsupported tool error")
	}

	environment := Environment{NodeJSVersion: "24"}
	active := registry.ActiveCommandNames(&environment)
	if !reflect.DeepEqual(active, []string{toolNode, toolNPM, toolNPX}) {
		t.Fatalf("ActiveCommandNames(nodejs env) = %#v, want node/npm/npx", active)
	}

	cleanup := registry.CleanupCommandNames()
	if !containsString(cleanup, toolNodeJS) {
		t.Fatalf("CleanupCommandNames() = %#v, want legacy nodejs cleanup entry", cleanup)
	}
}

func TestHTTPToolDownloaderRunsRegisteredPluginDownloadHook(t *testing.T) {
	var called ToolDownloadContext
	plugin := testToolPlugin{
		id: "demo",
		download: func(ctx ToolDownloadContext) error {
			called = ctx
			return nil
		},
	}
	registry, err := NewToolRegistry(plugin)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	if err := (HTTPToolDownloader{Plugins: registry}).Download("cache-root", "demo", "1.2.3"); err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if called.Client == nil || called.CacheDir != "cache-root" || called.Tool != "demo" || called.Version != "1.2.3" {
		t.Fatalf("download hook context = %#v, want populated context", called)
	}
}

func TestHTTPToolDownloaderReturnsPluginDownloadError(t *testing.T) {
	wantErr := errors.New("download failed")
	registry, err := NewToolRegistry(testToolPlugin{
		id: "demo",
		download: func(ctx ToolDownloadContext) error {
			return wantErr
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	if err := (HTTPToolDownloader{Plugins: registry}).Download("cache-root", "demo", "1.2.3"); !errors.Is(err, wantErr) {
		t.Fatalf("Download() error = %v, want %v", err, wantErr)
	}
}

type testToolPlugin struct {
	id       string
	download func(ToolDownloadContext) error
}

func (p testToolPlugin) ID() string {
	return p.id
}

func (p testToolPlugin) Version(Environment) string {
	return ""
}

func (p testToolPlugin) Validate(Environment) error {
	return nil
}

func (p testToolPlugin) InstallCandidates(root, version string) []string {
	return nil
}

func (p testToolPlugin) DispatchCommands() []string {
	return nil
}

func (p testToolPlugin) CleanupCommands() []string {
	return nil
}

func (p testToolPlugin) ActiveCommands(Environment) []string {
	return nil
}

func (p testToolPlugin) DispatchCandidates(root, executable, version string) []string {
	return nil
}

func (p testToolPlugin) Download(ctx ToolDownloadContext) error {
	return p.download(ctx)
}

func (p testToolPlugin) PostInstall(ToolInstallContext) error {
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
