package tools

import (
	"reflect"
	"strings"
	"testing"

	"polka/config"
)

// TestRabbitMQManifestAndDependency verifies commands and automatic OTP ordering.
func TestRabbitMQManifestAndDependency(t *testing.T) {
	plugin, ok := NewDefaultRegistry().Plugin(RabbitMQ)
	if !ok {
		t.Fatal("rabbitmq plugin is not registered")
	}
	if got := plugin.DispatchCommands(); !reflect.DeepEqual(got, []string{"rabbitmq-server", "rabbitmqctl", "rabbitmq-diagnostics", "rabbitmq-plugins", "rabbitmq-queues", "rabbitmq-streams", "rabbitmq-upgrade"}) {
		t.Fatalf("rabbitmq dispatch commands = %#v", got)
	}
	if deps, err := plugin.(ToolDependencyProvider).Dependencies(config.Environment{}); err != nil || len(deps) != 0 {
		t.Fatalf("Dependencies(unconfigured) = %#v, %v, want none", deps, err)
	}
	layers, err := NewDefaultRegistry().InstallRequestLayers(config.Environment{Tools: config.ToolsConfig{RabbitMQ: "4.3"}}, nil)
	if err != nil {
		t.Fatalf("InstallRequestLayers() error = %v", err)
	}
	if len(layers) != 2 || len(layers[0]) != 1 || layers[0][0] != (InstallRequest{Tool: Erlang, Version: "27"}) || layers[1][0].Tool != RabbitMQ {
		t.Fatalf("InstallRequestLayers() = %#v, want erlang then rabbitmq", layers)
	}
}

// TestValidateRabbitMQConfig covers manifest version constraints, port ranges,
// and broker-specific cross-field validation rules.
func TestValidateRabbitMQConfig(t *testing.T) {
	plugin := builtinTestPlugin(t, RabbitMQ)
	for _, test := range []struct {
		name string
		env  config.Environment
		want string
	}{
		{
			name: "valid",
			env:  config.Environment{Tools: config.ToolsConfig{RabbitMQ: "4.3"}},
		},
		{
			name: "same ports",
			env: config.Environment{
				Tools:    config.ToolsConfig{RabbitMQ: "4.3"},
				Settings: config.SettingsConfig{RabbitMQ: {"port": 5672, "management-port": 5672}},
			},
			want: "must be different",
		},
		{
			name: "unsupported major",
			env:  config.Environment{Tools: config.ToolsConfig{RabbitMQ: "3.13"}},
			want: "requires >= 4.0.4 and < 5",
		},
		{
			name: "username with default password",
			env: config.Environment{
				Tools:    config.ToolsConfig{RabbitMQ: "4.3"},
				Settings: config.SettingsConfig{RabbitMQ: {"username": "demo"}},
			},
		},
		{
			name: "multiline credentials",
			env: config.Environment{
				Tools:    config.ToolsConfig{RabbitMQ: "4.3"},
				Settings: config.SettingsConfig{RabbitMQ: {"username": "demo\ninjected"}},
			},
			want: "single-line",
		},
		{
			name: "settings without version",
			env: config.Environment{
				Settings: config.SettingsConfig{RabbitMQ: {"port": 5672}},
			},
			want: "rabbitmq configuration requires version",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := plugin.Validate(test.env)
			if test.want == "" && err != nil {
				t.Fatalf("plugin.Validate() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("plugin.Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestRabbitMQWindowsAssets verifies the supported platform and asset template.
func TestRabbitMQWindowsAssets(t *testing.T) {
	manifest, err := loadBuiltinManifest(RabbitMQ)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := resolveManifestDownloadAsset(RabbitMQ, manifest.Download.Assets, "4.3", "4.3.2", "v4.3.2", "windows", "amd64", nil)
	if err != nil {
		t.Fatal(err)
	}
	if asset.FileName != "rabbitmq-server-windows-4.3.2.zip" || asset.ArchiveFormat != archiveFormatZip {
		t.Fatalf("rabbitmq asset = %#v", asset)
	}
	if _, err := resolveManifestDownloadAsset(RabbitMQ, manifest.Download.Assets, "4.3", "4.3.2", "v4.3.2", "linux", "amd64", nil); err == nil {
		t.Fatal("linux rabbitmq asset unexpectedly resolved")
	}
}

type dependencyPlugin struct {
	testPlugin
	dependencies []InstallRequest
}

func (plugin dependencyPlugin) Dependencies(config.Environment) ([]InstallRequest, error) {
	return plugin.dependencies, nil
}

// TestInstallRequestLayersRejectCycles verifies dependency graph validation.
func TestInstallRequestLayersRejectCycles(t *testing.T) {
	registry, err := NewRegistry(
		dependencyPlugin{testPlugin: testPlugin{id: "a"}, dependencies: []InstallRequest{{Tool: "b", Version: "1"}}},
		dependencyPlugin{testPlugin: testPlugin{id: "b"}, dependencies: []InstallRequest{{Tool: "a", Version: "1"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.InstallRequestLayers(config.Environment{}, []InstallRequest{{Tool: "a", Version: "1"}}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("InstallRequestLayers(cycle) error = %v", err)
	}
}
