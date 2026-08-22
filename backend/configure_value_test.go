package backend

import (
	"os"
	"strings"
	"testing"
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
	if !environment.PHPBuildTools {
		t.Fatal("ConfigureValue(extension-sdk true) PHPBuildTools = false, want true")
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
	if environment.PHPBuildTools {
		t.Fatal("ConfigureValue(extension-sdk false) PHPBuildTools = true, want false")
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
	if !environment.PHPBuildTools || environment.PHPZTSVersion != "8.4" {
		t.Fatalf("environment = %#v, want PHP ZTS with extension SDK", environment)
	}
}
