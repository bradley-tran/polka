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
