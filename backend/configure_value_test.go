package backend

import (
	"os"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"polka/config"
	"polka/tools"
)

// TestStoreConfigureValueSetsPHPBuildTools checks the scalar config command
// path and the persisted extension-sdk YAML representation.
func TestStoreConfigureValueSetsPHPBuildTools(t *testing.T) {
	store := NewProjectStore(t.TempDir())
	if _, err := store.ConfigureValue("default", "tools.php", "8.4"); err != nil {
		t.Fatalf("ConfigureValue(tools.php) error = %v", err)
	}

	environment, err := store.ConfigureValue("default", "settings.php.extension-sdk", "true")
	if err != nil {
		t.Fatalf("ConfigureValue(extension-sdk true) error = %v", err)
	}
	if !environment.PHPBuildTools() {
		t.Fatal("ConfigureValue(extension-sdk true) PHPBuildTools() = false, want true")
	}
	data, err := os.ReadFile(store.ConfigFile)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	if !strings.Contains(string(data), "settings:\n  php:\n    extension-sdk: true") {
		t.Fatalf("config = %q, want settings.php.extension-sdk true", data)
	}

	environment, err = store.ConfigureValue("default", "settings.php.extension-sdk", "false")
	if err != nil {
		t.Fatalf("ConfigureValue(extension-sdk false) error = %v", err)
	}
	if environment.PHPBuildTools() {
		t.Fatal("ConfigureValue(extension-sdk false) PHPBuildTools() = true, want false")
	}
}

// TestStoreConfigureValueRejectsInvalidPHPSettings verifies both value and key
// validation happen before an unsupported setting reaches the config file.
func TestStoreConfigureValueRejectsInvalidPHPSettings(t *testing.T) {
	store := NewProjectStore(t.TempDir())

	if _, err := store.ConfigureValue("default", "settings.php.extension-sdk", "maybe"); err == nil || !strings.Contains(err.Error(), "requires a boolean value") {
		t.Fatalf("ConfigureValue(non-boolean) error = %v, want boolean error", err)
	}
	if _, err := store.ConfigureValue("default", "settings.php.unknown", "true"); err == nil || !strings.Contains(err.Error(), "unsupported config key") {
		t.Fatalf("ConfigureValue(unknown PHP setting) error = %v, want unsupported-key error", err)
	}
}

// TestStoreConfigureValueAllowsPHPZTSExtensionSDK verifies the shared PHP
// settings block accepts either standalone runtime flavor.
func TestStoreConfigureValueAllowsPHPZTSExtensionSDK(t *testing.T) {
	store := NewProjectStore(t.TempDir())
	if _, err := store.ConfigureValue("default", "tools.php-zts", "8.4"); err != nil {
		t.Fatalf("ConfigureValue(tools.php-zts) error = %v", err)
	}
	environment, err := store.ConfigureValue("default", "settings.php.extension-sdk", "true")
	if err != nil {
		t.Fatalf("ConfigureValue(extension-sdk for php-zts) error = %v", err)
	}
	if !environment.PHPBuildTools() || environment.ToolVersion(toolPHPZTS) != "8.4" {
		t.Fatalf("environment = %#v, want PHP ZTS with extension SDK", environment)
	}
}

// fakeAcmePlugin is a synthetic ToolPlugin that also implements
// tools.ToolSettingsProvider to verify that registering a new plugin with
// settings works end-to-end without editing config or backend.
type fakeAcmePlugin struct{}

func (fakeAcmePlugin) ID() string                     { return "acme" }
func (fakeAcmePlugin) Version(env Environment) string { return env.ToolVersion("acme") }
func (fakeAcmePlugin) PHPExtensions() map[string]bool { return nil }
func (fakeAcmePlugin) Validate(Environment) error     { return nil }
func (fakeAcmePlugin) InstallCandidates(root, version string) []string {
	return []string{root + "/acme"}
}
func (fakeAcmePlugin) DispatchCommands() []string              { return []string{"acme"} }
func (fakeAcmePlugin) CleanupCommands() []string               { return []string{"acme"} }
func (fakeAcmePlugin) ActiveCommands(env Environment) []string { return []string{"acme"} }
func (fakeAcmePlugin) DispatchCandidates(root, exe, ver string) []string {
	return []string{root + "/acme"}
}
func (fakeAcmePlugin) Logs() []ToolLogEntry                 { return nil }
func (fakeAcmePlugin) Download(ToolDownloadContext) error   { return nil }
func (fakeAcmePlugin) PostInstall(ToolInstallContext) error { return nil }
func (fakeAcmePlugin) Settings() map[string]tools.SettingType {
	return map[string]tools.SettingType{
		"port":  tools.SettingTypePort,
		"token": tools.SettingTypeString,
		"tls":   tools.SettingTypeBool,
	}
}
func (fakeAcmePlugin) SettingsNamespace() string { return "acme" }
func (fakeAcmePlugin) ExclusiveWith() []string   { return nil }

var _ tools.ToolSettingsProvider = fakeAcmePlugin{}

// TestCustomToolPluginExtensibility verifies that registering a custom tool
// plugin implementing ToolSettingsProvider allows configuring tools.<id>,
// configuring settings.<id>.<key>, validating raw YAML schemas, and
// round-tripping through polka.yaml with zero changes to config or backend.
func TestCustomToolPluginExtensibility(t *testing.T) {
	pluginsList := append([]ToolPlugin{}, NewDefaultToolRegistry().Plugins()...)
	pluginsList = append(pluginsList, fakeAcmePlugin{})
	registry, err := NewToolRegistry(pluginsList...)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	store := NewProjectStore(t.TempDir())
	store.Plugins = registry

	if _, err := store.ConfigureValue("default", "tools.acme", "1.2"); err != nil {
		t.Fatalf("ConfigureValue(tools.acme) error = %v", err)
	}
	if _, err := store.ConfigureValue("default", "settings.acme.port", "9090"); err != nil {
		t.Fatalf("ConfigureValue(settings.acme.port) error = %v", err)
	}
	if _, err := store.ConfigureValue("default", "settings.acme.token", "secret-token"); err != nil {
		t.Fatalf("ConfigureValue(settings.acme.token) error = %v", err)
	}
	environment, err := store.ConfigureValue("default", "settings.acme.tls", "true")
	if err != nil {
		t.Fatalf("ConfigureValue(settings.acme.tls) error = %v", err)
	}
	if environment.ToolVersion("acme") != "1.2" {
		t.Fatalf("ToolVersion(acme) = %q, want 1.2", environment.ToolVersion("acme"))
	}
	if environment.SettingInt("acme", "port") != 9090 {
		t.Fatalf("SettingInt(acme, port) = %d, want 9090", environment.SettingInt("acme", "port"))
	}
	if environment.SettingString("acme", "token") != "secret-token" {
		t.Fatalf("SettingString(acme, token) = %q, want secret-token", environment.SettingString("acme", "token"))
	}
	if !environment.SettingBool("acme", "tls") {
		t.Fatalf("SettingBool(acme, tls) = false, want true")
	}

	reloaded, err := store.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if reloaded.ToolVersion("acme") != "1.2" ||
		reloaded.SettingInt("acme", "port") != 9090 ||
		reloaded.SettingString("acme", "token") != "secret-token" ||
		!reloaded.SettingBool("acme", "tls") {
		t.Fatalf("reloaded environment = %#v, want persisted acme tool and settings", reloaded)
	}

	if _, err := store.ConfigureValue("default", "settings.acme.unknown", "1"); err == nil {
		t.Fatal("ConfigureValue(settings.acme.unknown) error = nil, want error")
	}
}

// TestFrameworkDefaultsPassEnvironmentFileSchema verifies that all built-in
// framework and preset defaults serialize into valid YAML schemas according to
// validateEnvironmentFileSchema.
func TestFrameworkDefaultsPassEnvironmentFileSchema(t *testing.T) {
	store := NewProjectStore(t.TempDir())
	for _, id := range store.SupportedFrameworks() {
		env, err := store.FrameworkDefaults(id)
		if err != nil {
			t.Fatalf("FrameworkDefaults(%q) error = %v", id, err)
		}
		data, err := yaml.Marshal(config.EnvironmentFileFromEnvironment(env))
		if err != nil {
			t.Fatalf("yaml.Marshal(%q) error = %v", id, err)
		}
		if err := validateEnvironmentFileSchema(data, store.ToolRegistry()); err != nil {
			t.Fatalf("validateEnvironmentFileSchema(%q) error = %v\n%s", id, err, data)
		}
	}
	for _, id := range store.SupportedPresets() {
		env, err := store.PresetDefaults(id)
		if err != nil {
			t.Fatalf("PresetDefaults(%q) error = %v", id, err)
		}
		data, err := yaml.Marshal(config.EnvironmentFileFromEnvironment(env))
		if err != nil {
			t.Fatalf("yaml.Marshal(%q) error = %v", id, err)
		}
		if err := validateEnvironmentFileSchema(data, store.ToolRegistry()); err != nil {
			t.Fatalf("validateEnvironmentFileSchema(%q) error = %v\n%s", id, err, data)
		}
	}
}
