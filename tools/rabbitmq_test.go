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
	layers, err := NewDefaultRegistry().InstallRequestLayers(config.Environment{RabbitMQ: &config.RabbitMQConfig{Version: "4.3"}}, nil)
	if err != nil {
		t.Fatalf("InstallRequestLayers() error = %v", err)
	}
	if len(layers) != 2 || len(layers[0]) != 1 || layers[0][0] != (InstallRequest{Tool: Erlang, Version: "27"}) || layers[1][0].Tool != RabbitMQ {
		t.Fatalf("InstallRequestLayers() = %#v, want erlang then rabbitmq", layers)
	}
}

// TestValidateRabbitMQConfig covers broker-specific validation rules.
func TestValidateRabbitMQConfig(t *testing.T) {
	for _, test := range []struct {
		name  string
		value *config.RabbitMQConfig
		want  string
	}{
		{name: "valid", value: &config.RabbitMQConfig{Version: "4.3"}},
		{name: "same ports", value: &config.RabbitMQConfig{Version: "4.3", Port: 5672, ManagementPort: 5672}, want: "must be different"},
		{name: "unsupported major", value: &config.RabbitMQConfig{Version: "3.13"}, want: "4.x"},
		{name: "username with default password", value: &config.RabbitMQConfig{Version: "4.3", Username: "demo"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateRabbitMQConfig(test.value)
			if test.want == "" && err != nil {
				t.Fatalf("validateRabbitMQConfig() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("validateRabbitMQConfig() error = %v, want %q", err, test.want)
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

func (plugin dependencyPlugin) Dependencies(config.Environment) []InstallRequest {
	return plugin.dependencies
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
