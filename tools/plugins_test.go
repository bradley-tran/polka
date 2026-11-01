package tools

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"polka/config"
)

// builtinTestPlugin returns the registered plugin for a built-in tool.
func builtinTestPlugin(t *testing.T, id string) ToolPlugin {
	t.Helper()
	plugin, ok := NewDefaultRegistry().Plugin(id)
	if !ok {
		t.Fatalf("no built-in plugin for %q", id)
	}

	return plugin
}

// discoveryManifest is the smallest valid manifest for a tool id.
func discoveryManifest(id, extra string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("id: " + id + "\ninstall-candidates:\n  all:\n    - " + id + "\n" + extra)}
}

// discoverFromFS runs discovery with loadManifest over fsys.
func discoverFromFS(fsys fs.FS, hooks map[string]pluginHooks) ([]ToolPlugin, error) {
	return discoverManifestPlugins(fsys, func(name string) (pluginManifest, error) {
		return loadManifest(fsys, name)
	}, hooks)
}

// TestDiscoveryOrdersByPriorityThenID verifies the registration order that
// decides shared dispatch commands.
func TestDiscoveryOrdersByPriorityThenID(t *testing.T) {
	plugins, err := discoverFromFS(fstest.MapFS{
		"alpha.yaml":   discoveryManifest("alpha", ""),
		"beta.yaml":    discoveryManifest("beta", "dispatch-priority: 10\n"),
		"gamma.yaml":   discoveryManifest("gamma", "dispatch-priority: 20\n"),
		"delta.yaml":   discoveryManifest("delta", ""),
		"ignored.star": {Data: []byte("not a manifest")},
	}, nil)
	if err != nil {
		t.Fatalf("discoverFromFS() error = %v", err)
	}

	got := make([]string, 0, len(plugins))
	for _, plugin := range plugins {
		got = append(got, plugin.ID())
	}
	if want := "gamma,beta,alpha,delta"; strings.Join(got, ",") != want {
		t.Fatalf("plugin order = %v, want %s", got, want)
	}
}

// TestDiscoveryNeedsNoGoCode verifies a manifest and script are all a tool
// needs: nothing outside the filesystem mentions the tool.
func TestDiscoveryNeedsNoGoCode(t *testing.T) {
	plugins, err := discoverFromFS(fstest.MapFS{
		"demo.yaml":         discoveryManifest("demo", "script:\n  file: scripts/demo.star\n  hooks: [validate]\n"),
		"scripts/demo.star": {Data: []byte("def validate(env):\n    fail(\"demo rejected \" + env.version)\n")},
	}, nil)
	if err != nil {
		t.Fatalf("discoverFromFS() error = %v", err)
	}
	if len(plugins) != 1 || plugins[0].ID() != "demo" {
		t.Fatalf("plugins = %#v, want only demo", plugins)
	}
	err = plugins[0].Validate(config.Environment{Tools: config.ToolsConfig{"demo": "1"}})
	if err == nil || err.Error() != "demo rejected 1" {
		t.Fatalf("Validate() error = %v, want the script to run", err)
	}
}

// TestDiscoveryRejectsInconsistencies covers a manifest whose id differs from
// its file name and Go hooks that no manifest claims.
func TestDiscoveryRejectsInconsistencies(t *testing.T) {
	if _, err := discoverFromFS(fstest.MapFS{"demo.yaml": discoveryManifest("other", "")}, nil); err == nil || !strings.Contains(err.Error(), `must declare id "demo"`) {
		t.Fatalf("discoverFromFS(mismatched id) error = %v", err)
	}

	hooks := map[string]pluginHooks{"missing": {}}
	if _, err := discoverFromFS(fstest.MapFS{"demo.yaml": discoveryManifest("demo", "")}, hooks); err == nil || !strings.Contains(err.Error(), `tool "missing", which has no manifest`) {
		t.Fatalf("discoverFromFS(orphan hooks) error = %v", err)
	}
}

// TestBuiltinGoHooksAllHaveManifests verifies the built-in hook table cannot
// silently stop applying when a manifest is renamed.
func TestBuiltinGoHooksAllHaveManifests(t *testing.T) {
	registry := NewDefaultRegistry()
	for id := range goToolHooks() {
		if _, ok := registry.Plugin(id); !ok {
			t.Fatalf("goToolHooks() references %q, which is not registered", id)
		}
	}
}

// TestPHPCommandProviderPrecedence pins who provides `php`, which alphabetical
// registration alone would hand to frankenphp.
func TestPHPCommandProviderPrecedence(t *testing.T) {
	registry := NewDefaultRegistry()
	tests := []struct {
		name  string
		tools config.ToolsConfig
		want  string
	}{
		{name: "nothing configured falls back to php", want: PHP},
		{name: "php", tools: config.ToolsConfig{PHP: "8.4"}, want: PHP},
		{name: "php-zts", tools: config.ToolsConfig{PHPZTS: "8.4"}, want: PHPZTS},
		{name: "frankenphp alone", tools: config.ToolsConfig{FrankenPHP: "1.12"}, want: FrankenPHP},
		{name: "php over frankenphp", tools: config.ToolsConfig{PHP: "8.4", FrankenPHP: "1.12"}, want: PHP},
		{name: "php-zts over frankenphp", tools: config.ToolsConfig{PHPZTS: "8.4", FrankenPHP: "1.12"}, want: PHPZTS},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := registry.ResolveDispatchRequestForEnvironment("php", config.Environment{Tools: test.tools})
			if err != nil {
				t.Fatalf("ResolveDispatchRequestForEnvironment() error = %v", err)
			}
			if request.ConfigTool != test.want {
				t.Fatalf("php provider = %q, want %q", request.ConfigTool, test.want)
			}
		})
	}

	request, err := registry.ResolveDispatchRequest("php")
	if err != nil {
		t.Fatalf("ResolveDispatchRequest() error = %v", err)
	}
	if request.ConfigTool != PHP {
		t.Fatalf("ResolveDispatchRequest(php) provider = %q, want php", request.ConfigTool)
	}
}

// TestSharedCommandsHaveDeliberateWinners fails when two tools start sharing a
// command without distinct priorities, which would pick a provider by name.
func TestSharedCommandsHaveDeliberateWinners(t *testing.T) {
	fsys := builtinManifestFS()
	files, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		t.Fatalf("fs.Glob() error = %v", err)
	}

	providers := map[string]map[int]string{}
	for _, file := range files {
		manifest, err := loadBuiltinManifest(strings.TrimSuffix(file, ".yaml"))
		if err != nil {
			t.Fatalf("loadBuiltinManifest(%s) error = %v", file, err)
		}
		for _, command := range manifest.DispatchCommands {
			if providers[command] == nil {
				providers[command] = map[int]string{}
			}
			if other, clash := providers[command][manifest.DispatchPriority]; clash {
				t.Fatalf("%s and %s both provide %q at dispatch-priority %d", other, manifest.ID, command, manifest.DispatchPriority)
			}
			providers[command][manifest.DispatchPriority] = manifest.ID
		}
	}
}

// TestBuiltinManifestsAreAllRegistered verifies discovery found every tool the
// repo embeds, and that tool IDs used by Go callers still resolve.
func TestBuiltinManifestsAreAllRegistered(t *testing.T) {
	registry := NewDefaultRegistry()
	for _, id := range []string{
		PHP, PHPZTS, PHPDevel, PHPSDK, FrankenPHP, Composer, PIE, NodeJS, Mago, Nginx, Apache, Mailpit,
		Meilisearch, Redis, Erlang, RabbitMQ, Traefik, RoadRunner, PHPMyAdmin, MySQL, MariaDB, PostgreSQL, SQLite,
	} {
		if _, ok := registry.Plugin(id); !ok {
			t.Fatalf("tool %q is not registered", id)
		}
	}
}
