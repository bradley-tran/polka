package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"polka/config"
)

// TestPHPBuildToolDependencies verifies the SDK signal adds internal Windows
// build payloads for either PHP runtime flavor and remains inert otherwise.
func TestPHPBuildToolDependencies(t *testing.T) {
	if dependencies, err := builtinTestPlugin(t, PHP).(ToolDependencyProvider).Dependencies(config.Environment{Tools: config.ToolsConfig{PHP: "8.4"}}); err != nil || dependencies != nil {
		t.Fatalf("Dependencies(flag off) = %#v, %v, want nil", dependencies, err)
	}

	tests := []struct {
		name        string
		plugin      Plugin
		environment config.Environment
	}{
		{name: "nts", plugin: builtinTestPlugin(t, PHP), environment: config.Environment{Tools: config.ToolsConfig{PHP: "8.4"}, Settings: config.SettingsConfig{PHP: {"extension-sdk": true}}}},
		{name: "zts", plugin: builtinTestPlugin(t, PHPZTS), environment: config.Environment{Tools: config.ToolsConfig{PHPZTS: "8.4"}, Settings: config.SettingsConfig{PHP: {"extension-sdk": true}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, err := test.plugin.(ToolDependencyProvider).Dependencies(test.environment)
			if err != nil {
				t.Fatalf("Dependencies() error = %v", err)
			}
			if runtime.GOOS != "windows" {
				if dependencies != nil {
					t.Fatalf("Dependencies(non-Windows) = %#v, want nil", dependencies)
				}
				return
			}
			want := []InstallRequest{{Tool: PHPDevel, Version: "8.4"}, {Tool: PHPSDK, Version: DefaultPHPSDKVersion}}
			if !reflect.DeepEqual(dependencies, want) {
				t.Fatalf("Dependencies() = %#v, want %#v", dependencies, want)
			}
		})
	}
}

func TestEnsureInstalledPHPOpenSSLConfigWritesFallback(t *testing.T) {
	envsDir := t.TempDir()

	path, err := ensureInstalledPHPOpenSSLConfig(envsDir, PHP, "8.4")
	if err != nil {
		t.Fatalf("ensureInstalledPHPOpenSSLConfig() error = %v", err)
	}
	if path != filepath.Join(envsDir, PHP, "8.4", filepath.FromSlash(phpOpenSSLConfigPath)) {
		t.Fatalf("openssl config path = %q, want project-local PHP config path", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(openssl.cnf) error = %v", err)
	}
	config := string(data)
	for _, want := range []string{"HOME = .", "[req]", "distinguished_name = req_distinguished_name", "[req_distinguished_name]"} {
		if !strings.Contains(config, want) {
			t.Fatalf("openssl.cnf = %q, want %q", config, want)
		}
	}
}

func TestEnsureInstalledPHPOpenSSLConfigPreservesExisting(t *testing.T) {
	envsDir := t.TempDir()
	path := installedPHPOpenSSLConfigPath(envsDir, PHP, "8.4")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(openssl config dir) error = %v", err)
	}
	if err := os.WriteFile(path, []byte("custom-openssl-config\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(openssl.cnf) error = %v", err)
	}

	got, err := ensureInstalledPHPOpenSSLConfig(envsDir, PHP, "8.4")
	if err != nil {
		t.Fatalf("ensureInstalledPHPOpenSSLConfig() error = %v", err)
	}
	if got != path {
		t.Fatalf("openssl config path = %q, want %q", got, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(openssl.cnf) error = %v", err)
	}
	if string(data) != "custom-openssl-config\n" {
		t.Fatalf("openssl.cnf = %q, want existing contents preserved", data)
	}
}
