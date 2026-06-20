package config

import "testing"

func TestPHPZTSConfigRoundTrip(t *testing.T) {
	environment := ProjectFileToEnvironment("default", ProjectFile{
		Tools: &ToolsConfig{PHPZTSVersion: "8.4"},
	})
	if environment.PHPZTSVersion != "8.4" || environment.PHPVersion != "" {
		t.Fatalf("ProjectFileToEnvironment() = %#v, want php-zts 8.4", environment)
	}

	file := ProjectFileFromEnvironment(1, ".polka", environment)
	if file.Tools == nil || file.Tools.PHPZTSVersion != "8.4" || file.Tools.PHPVersion != "" {
		t.Fatalf("ProjectFileFromEnvironment() tools = %#v, want php-zts 8.4", file.Tools)
	}
	tool, version := PrimaryPHPTool(environment)
	if tool != "php-zts" || version != "8.4" {
		t.Fatalf("PrimaryPHPTool() = %q, %q, want php-zts, 8.4", tool, version)
	}
}

func TestFrankenPHPAndServerTypeConfigRoundTrip(t *testing.T) {
	environment := ProjectFileToEnvironment("default", ProjectFile{
		Tools:  &ToolsConfig{FrankenPHPVersion: " 1.12 "},
		Server: &ServerConfig{Type: " FrankenPHP "},
	})
	normalized := NormalizeEnvironment("default", environment)
	if normalized.FrankenPHPVersion != "1.12" || normalized.Server == nil || normalized.Server.Type != ServerTypeFrankenPHP {
		t.Fatalf("NormalizeEnvironment() = %#v, want FrankenPHP 1.12 server", normalized)
	}

	file := ProjectFileFromEnvironment(1, ".polka", normalized)
	if file.Tools == nil || file.Tools.FrankenPHPVersion != "1.12" {
		t.Fatalf("ProjectFileFromEnvironment() tools = %#v, want FrankenPHP 1.12", file.Tools)
	}
	if file.Server == nil || file.Server.Type != ServerTypeFrankenPHP {
		t.Fatalf("ProjectFileFromEnvironment() server = %#v, want frankenphp type", file.Server)
	}
}
