package config

import "testing"

func TestPostgreSQLToolAndDatabaseRoundTrip(t *testing.T) {
	environment := Environment{
		Name:              "demo",
		PostgreSQLVersion: "17",
		Database:          &DatabaseConfig{Engine: "postgresql", Version: "17", Port: 5544},
	}
	file := EnvironmentFileFromEnvironment(environment)
	if file.Tools == nil || file.Tools.PostgreSQLVersion != "17" {
		t.Fatalf("EnvironmentFileFromEnvironment().Tools = %#v, want postgresql 17", file.Tools)
	}
	if file.Database == nil || file.Database.Engine != "postgresql" || file.Database.Port != 5544 || file.Database.Version != "" {
		t.Fatalf("EnvironmentFileFromEnvironment().Database = %#v, want postgresql runtime settings", file.Database)
	}

	roundTrip := EnvironmentFileToEnvironment("demo", file)
	if roundTrip.PostgreSQLVersion != "17" || roundTrip.Database == nil || roundTrip.Database.Version != "17" {
		t.Fatalf("EnvironmentFileToEnvironment() = %#v, want restored postgresql version", roundTrip)
	}
}

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

func TestPHPCLIProviderFallsBackToFrankenPHP(t *testing.T) {
	tool, version := PHPCLIProvider(Environment{FrankenPHPVersion: "1.12"})
	if tool != ServerTypeFrankenPHP || version != "1.12" {
		t.Fatalf("PHPCLIProvider() = %q, %q, want frankenphp, 1.12", tool, version)
	}
	if !HasPHPCLI(Environment{FrankenPHPVersion: "1.12"}) {
		t.Fatal("HasPHPCLI() = false, want FrankenPHP fallback")
	}

	tool, version = PHPCLIProvider(Environment{PHPVersion: "8.4", FrankenPHPVersion: "1.12"})
	if tool != "php" || version != "8.4" {
		t.Fatalf("PHPCLIProvider(mixed) = %q, %q, want php, 8.4", tool, version)
	}
}
