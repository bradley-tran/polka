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

func TestTraefikToolAndSettingsRoundTrip(t *testing.T) {
	environment := NormalizeEnvironment("demo", Environment{
		Name:    "demo",
		Traefik: &TraefikConfig{Version: "3.3", Port: 8090},
	})

	// The version belongs under tools; the port belongs under settings.
	file := EnvironmentFileFromEnvironment(environment)
	if file.Tools == nil || file.Tools.TraefikVersion != "3.3" {
		t.Fatalf("EnvironmentFileFromEnvironment().Tools = %#v, want traefik 3.3", file.Tools)
	}
	if file.Settings == nil || file.Settings.Traefik == nil || file.Settings.Traefik.Port != 8090 {
		t.Fatalf("EnvironmentFileFromEnvironment().Settings = %#v, want traefik port 8090", file.Settings)
	}

	roundTrip := EnvironmentFileToEnvironment("demo", file)
	if roundTrip.Traefik == nil || roundTrip.Traefik.Version != "3.3" || roundTrip.Traefik.Port != 8090 {
		t.Fatalf("EnvironmentFileToEnvironment().Traefik = %#v, want restored version and port", roundTrip.Traefik)
	}
}

func TestRedisToolAndSettingsRoundTrip(t *testing.T) {
	environment := NormalizeEnvironment("demo", Environment{
		Name:  "demo",
		Redis: &RedisConfig{Version: "8.8.0", Port: 6380, Password: "local-dev-password"},
	})

	// The version belongs under tools; the port and password belong under settings.
	file := EnvironmentFileFromEnvironment(environment)
	if file.Tools == nil || file.Tools.RedisVersion != "8.8.0" {
		t.Fatalf("EnvironmentFileFromEnvironment().Tools = %#v, want redis 8.8.0", file.Tools)
	}
	if file.Settings == nil || file.Settings.Redis == nil || file.Settings.Redis.Port != 6380 || file.Settings.Redis.Password != "local-dev-password" {
		t.Fatalf("EnvironmentFileFromEnvironment().Settings = %#v, want redis port and password", file.Settings)
	}

	roundTrip := EnvironmentFileToEnvironment("demo", file)
	if roundTrip.Redis == nil || roundTrip.Redis.Version != "8.8.0" || roundTrip.Redis.Port != 6380 || roundTrip.Redis.Password != "local-dev-password" {
		t.Fatalf("EnvironmentFileToEnvironment().Redis = %#v, want restored version, port, and password", roundTrip.Redis)
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

func TestPHPMemoryLimitConfigRoundTrip(t *testing.T) {
	environment := ProjectFileToEnvironment("default", ProjectFile{
		Tools:       &ToolsConfig{PHPVersion: "8.4"},
		MemoryLimit: " 512m ",
	})
	if environment.MemoryLimit != "512M" {
		t.Fatalf("ProjectFileToEnvironment() memory-limit = %q, want 512M", environment.MemoryLimit)
	}

	file := ProjectFileFromEnvironment(1, ".polka", environment)
	if file.MemoryLimit != "512M" {
		t.Fatalf("ProjectFileFromEnvironment() memory-limit = %#v, want 512M", file.MemoryLimit)
	}

	environmentFile := EnvironmentFileFromEnvironment(Environment{Name: "demo", PHPVersion: "8.4", MemoryLimit: " -1 "})
	if environmentFile.MemoryLimit != "-1" {
		t.Fatalf("EnvironmentFileFromEnvironment() memory-limit = %#v, want -1", environmentFile.MemoryLimit)
	}
}

func TestValidatePHPMemoryLimit(t *testing.T) {
	for _, value := range []string{"", "-1", "0", "128M", "512m", "1024K", "1G", "134217728"} {
		if err := ValidatePHPMemoryLimit(value); err != nil {
			t.Fatalf("ValidatePHPMemoryLimit(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"-2", "1.5G", "128MB", "many"} {
		if err := ValidatePHPMemoryLimit(value); err == nil {
			t.Fatalf("ValidatePHPMemoryLimit(%q) error = nil, want validation error", value)
		}
	}
}

func TestApacheAndServerTypeConfigRoundTrip(t *testing.T) {
	environment := ProjectFileToEnvironment("default", ProjectFile{
		Tools:  &ToolsConfig{ApacheVersion: " 2.4 "},
		Server: &ServerConfig{Type: " Apache "},
	})
	normalized := NormalizeEnvironment("default", environment)
	if normalized.ApacheVersion != "2.4" || normalized.Server == nil || normalized.Server.Type != ServerTypeApache {
		t.Fatalf("NormalizeEnvironment() = %#v, want Apache 2.4 server", normalized)
	}

	file := ProjectFileFromEnvironment(1, ".polka", normalized)
	if file.Tools == nil || file.Tools.ApacheVersion != "2.4" {
		t.Fatalf("ProjectFileFromEnvironment() tools = %#v, want Apache 2.4", file.Tools)
	}
	if file.Server == nil || file.Server.Type != ServerTypeApache {
		t.Fatalf("ProjectFileFromEnvironment() server = %#v, want apache type", file.Server)
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
