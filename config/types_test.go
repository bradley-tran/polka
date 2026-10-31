package config

import "testing"

func TestPostgreSQLToolAndDatabaseRoundTrip(t *testing.T) {
	environment := Environment{
		Name:     "demo",
		Tools:    ToolsConfig{"postgresql": "17"},
		Database: &DatabaseConfig{Engine: "postgresql", Version: "17", Port: 5544},
	}
	file := EnvironmentFileFromEnvironment(environment)
	if file.Tools == nil || file.Tools["postgresql"] != "17" {
		t.Fatalf("EnvironmentFileFromEnvironment().Tools = %#v, want postgresql 17", file.Tools)
	}
	if file.Database == nil || file.Database.Engine != "postgresql" || file.Database.Port != 5544 || file.Database.Version != "" {
		t.Fatalf("EnvironmentFileFromEnvironment().Database = %#v, want postgresql runtime settings", file.Database)
	}

	roundTrip := EnvironmentFileToEnvironment("demo", file)
	if roundTrip.ToolVersion("postgresql") != "17" || roundTrip.Database == nil || roundTrip.Database.Version != "17" {
		t.Fatalf("EnvironmentFileToEnvironment() = %#v, want restored postgresql version", roundTrip)
	}
}

func TestTraefikToolAndSettingsRoundTrip(t *testing.T) {
	environment := NormalizeEnvironment("demo", Environment{
		Name:     "demo",
		Tools:    ToolsConfig{"traefik": "3.3"},
		Settings: SettingsConfig{"traefik": {"port": 8090}},
	})

	// The version belongs under tools; the port belongs under settings.
	file := EnvironmentFileFromEnvironment(environment)
	if file.Tools == nil || file.Tools["traefik"] != "3.3" {
		t.Fatalf("EnvironmentFileFromEnvironment().Tools = %#v, want traefik 3.3", file.Tools)
	}
	if file.Settings == nil || file.Settings["traefik"] == nil || file.Settings["traefik"]["port"] != 8090 {
		t.Fatalf("EnvironmentFileFromEnvironment().Settings = %#v, want traefik port 8090", file.Settings)
	}

	roundTrip := EnvironmentFileToEnvironment("demo", file)
	traefik := roundTrip.Traefik()
	if traefik == nil || traefik.Version != "3.3" || traefik.Port != 8090 {
		t.Fatalf("EnvironmentFileToEnvironment().Traefik() = %#v, want restored version and port", traefik)
	}
}

func TestRedisToolAndSettingsRoundTrip(t *testing.T) {
	environment := NormalizeEnvironment("demo", Environment{
		Name:     "demo",
		Tools:    ToolsConfig{"redis": "8.8.0"},
		Settings: SettingsConfig{"redis": {"port": 6380, "password": "local-dev-password"}},
	})

	// The version belongs under tools; the port and password belong under settings.
	file := EnvironmentFileFromEnvironment(environment)
	if file.Tools == nil || file.Tools["redis"] != "8.8.0" {
		t.Fatalf("EnvironmentFileFromEnvironment().Tools = %#v, want redis 8.8.0", file.Tools)
	}
	if file.Settings == nil || file.Settings["redis"] == nil || file.Settings["redis"]["port"] != 6380 || file.Settings["redis"]["password"] != "local-dev-password" {
		t.Fatalf("EnvironmentFileFromEnvironment().Settings = %#v, want redis port and password", file.Settings)
	}

	roundTrip := EnvironmentFileToEnvironment("demo", file)
	redis := roundTrip.Redis()
	if redis == nil || redis.Version != "8.8.0" || redis.Port != 6380 || redis.Password != "local-dev-password" {
		t.Fatalf("EnvironmentFileToEnvironment().Redis() = %#v, want restored version, port, and password", redis)
	}
}

// TestRabbitMQToolAndSettingsRoundTrip verifies the split tools/settings schema.
func TestRabbitMQToolAndSettingsRoundTrip(t *testing.T) {
	environment := Environment{
		Name:  "demo",
		Tools: ToolsConfig{"rabbitmq": "4.3"},
		Settings: SettingsConfig{
			"rabbitmq": {
				"port":            5673,
				"management-port": 15673,
				"username":        "polka",
				"password":        "secret",
			},
		},
	}
	file := EnvironmentFileFromEnvironment(environment)
	if file.Tools == nil || file.Tools["rabbitmq"] != "4.3" {
		t.Fatalf("tools.rabbitmq = %#v, want 4.3", file.Tools)
	}
	if file.Settings == nil || file.Settings["rabbitmq"] == nil || file.Settings["rabbitmq"]["management-port"] != 15673 {
		t.Fatalf("settings.rabbitmq = %#v, want configured settings", file.Settings)
	}
	roundTrip := EnvironmentFileToEnvironment("demo", file)
	rabbitMQ := roundTrip.RabbitMQ()
	if rabbitMQ == nil || rabbitMQ.Username != "polka" || rabbitMQ.Password != "secret" {
		t.Fatalf("round-trip rabbitmq = %#v", rabbitMQ)
	}
}

func TestPHPZTSConfigRoundTrip(t *testing.T) {
	environment := ProjectFileToEnvironment("default", ProjectFile{
		Tools: ToolsConfig{"php-zts": "8.4"},
	})
	if environment.ToolVersion("php-zts") != "8.4" || environment.ToolVersion("php") != "" {
		t.Fatalf("ProjectFileToEnvironment() = %#v, want php-zts 8.4", environment)
	}

	file := ProjectFileFromEnvironment(1, ".polka", environment)
	if file.Tools == nil || file.Tools["php-zts"] != "8.4" || file.Tools["php"] != "" {
		t.Fatalf("ProjectFileFromEnvironment() tools = %#v, want php-zts 8.4", file.Tools)
	}
	tool, version := PrimaryPHPTool(environment)
	if tool != "php-zts" || version != "8.4" {
		t.Fatalf("PrimaryPHPTool() = %q, %q, want php-zts, 8.4", tool, version)
	}
}

func TestFrankenPHPAndServerTypeConfigRoundTrip(t *testing.T) {
	environment := ProjectFileToEnvironment("default", ProjectFile{
		Tools:  ToolsConfig{"frankenphp": " 1.12 "},
		Server: &ServerConfig{Type: " FrankenPHP "},
	})
	normalized := NormalizeEnvironment("default", environment)
	if normalized.ToolVersion("frankenphp") != "1.12" || normalized.Server == nil || normalized.Server.Type != ServerTypeFrankenPHP {
		t.Fatalf("NormalizeEnvironment() = %#v, want FrankenPHP 1.12 server", normalized)
	}

	file := ProjectFileFromEnvironment(1, ".polka", normalized)
	if file.Tools == nil || file.Tools["frankenphp"] != "1.12" {
		t.Fatalf("ProjectFileFromEnvironment() tools = %#v, want FrankenPHP 1.12", file.Tools)
	}
	if file.Server == nil || file.Server.Type != ServerTypeFrankenPHP {
		t.Fatalf("ProjectFileFromEnvironment() server = %#v, want frankenphp type", file.Server)
	}
}

func TestPHPMemoryLimitConfigRoundTrip(t *testing.T) {
	environment := ProjectFileToEnvironment("default", ProjectFile{
		Tools:       ToolsConfig{"php": "8.4"},
		MemoryLimit: " 512m ",
	})
	if environment.MemoryLimit != "512M" {
		t.Fatalf("ProjectFileToEnvironment() memory-limit = %q, want 512M", environment.MemoryLimit)
	}

	file := ProjectFileFromEnvironment(1, ".polka", environment)
	if file.MemoryLimit != "512M" {
		t.Fatalf("ProjectFileFromEnvironment() memory-limit = %#v, want 512M", file.MemoryLimit)
	}

	environmentFile := EnvironmentFileFromEnvironment(Environment{Name: "demo", Tools: ToolsConfig{"php": "8.4"}, MemoryLimit: " -1 "})
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
		Tools:  ToolsConfig{"apache": " 2.4 "},
		Server: &ServerConfig{Type: " Apache "},
	})
	normalized := NormalizeEnvironment("default", environment)
	if normalized.ToolVersion("apache") != "2.4" || normalized.Server == nil || normalized.Server.Type != ServerTypeApache {
		t.Fatalf("NormalizeEnvironment() = %#v, want Apache 2.4 server", normalized)
	}

	file := ProjectFileFromEnvironment(1, ".polka", normalized)
	if file.Tools == nil || file.Tools["apache"] != "2.4" {
		t.Fatalf("ProjectFileFromEnvironment() tools = %#v, want Apache 2.4", file.Tools)
	}
	if file.Server == nil || file.Server.Type != ServerTypeApache {
		t.Fatalf("ProjectFileFromEnvironment() server = %#v, want apache type", file.Server)
	}
}

func TestPHPCLIProviderFallsBackToFrankenPHP(t *testing.T) {
	tool, version := PHPCLIProvider(Environment{Tools: ToolsConfig{"frankenphp": "1.12"}})
	if tool != ServerTypeFrankenPHP || version != "1.12" {
		t.Fatalf("PHPCLIProvider() = %q, %q, want frankenphp, 1.12", tool, version)
	}
	if !HasPHPCLI(Environment{Tools: ToolsConfig{"frankenphp": "1.12"}}) {
		t.Fatal("HasPHPCLI() = false, want FrankenPHP fallback")
	}

	tool, version = PHPCLIProvider(Environment{Tools: ToolsConfig{"php": "8.4", "frankenphp": "1.12"}})
	if tool != "php" || version != "8.4" {
		t.Fatalf("PHPCLIProvider(mixed) = %q, %q, want php, 8.4", tool, version)
	}
}

// TestPHPBuildToolsSettingRoundTrip verifies the extension SDK signal survives
// the public project-file conversion boundary.
func TestPHPBuildToolsSettingRoundTrip(t *testing.T) {
	environment := ProjectFileToEnvironment("default", ProjectFile{
		Settings: SettingsConfig{
			"php": {"extension-sdk": true},
		},
	})
	if !environment.PHPBuildTools() {
		t.Fatal("ProjectFileToEnvironment() PHPBuildTools() = false, want true")
	}

	file := ProjectFileFromEnvironment(1, ".polka", environment)
	if file.Settings == nil || file.Settings["php"] == nil || file.Settings["php"]["extension-sdk"] != true {
		t.Fatalf("ProjectFileFromEnvironment() settings = %#v, want extension-sdk true", file.Settings)
	}

	disabled := ProjectFileFromEnvironment(1, ".polka", Environment{})
	if disabled.Settings != nil {
		t.Fatalf("ProjectFileFromEnvironment(disabled) settings = %#v, want nil", disabled.Settings)
	}
}

// TestUnknownToolAndSettingsRoundTrip verifies that arbitrary tool IDs and
// settings namespaces survive config normalization and file conversion without
// requiring dedicated Go struct fields.
func TestUnknownToolAndSettingsRoundTrip(t *testing.T) {
	file := EnvironmentFile{
		Tools: ToolsConfig{" CustomTool ": " 2.1.0 "},
		Settings: SettingsConfig{
			" CustomTool ": {
				"Port":    uint64(9090),
				"Token":   "  secret-token  ",
				"Enabled": true,
				"Empty":   "   ",
				"Zero":    0,
				"False":   false,
			},
		},
	}

	environment := EnvironmentFileToEnvironment("demo", file)
	if got := environment.ToolVersion("customtool"); got != "2.1.0" {
		t.Fatalf("ToolVersion(customtool) = %q, want 2.1.0", got)
	}
	if got := environment.SettingInt("customtool", "port"); got != 9090 {
		t.Fatalf("SettingInt(customtool, port) = %d, want 9090", got)
	}
	if got := environment.SettingString("customtool", "token"); got != "secret-token" {
		t.Fatalf("SettingString(customtool, token) = %q, want secret-token", got)
	}
	if !environment.SettingBool("customtool", "enabled") {
		t.Fatal("SettingBool(customtool, enabled) = false, want true")
	}

	out := EnvironmentFileFromEnvironment(environment)
	if out.Tools["customtool"] != "2.1.0" {
		t.Fatalf("round-trip tools = %#v, want customtool 2.1.0", out.Tools)
	}
	ns := out.Settings["customtool"]
	if ns == nil || ns["port"] != 9090 || ns["token"] != "secret-token" || ns["enabled"] != true {
		t.Fatalf("round-trip settings = %#v", out.Settings)
	}
	if _, exists := ns["empty"]; exists {
		t.Fatalf("expected empty string setting to be dropped, got %#v", ns)
	}
	if _, exists := ns["zero"]; exists {
		t.Fatalf("expected zero int setting to be dropped, got %#v", ns)
	}
	if _, exists := ns["false"]; exists {
		t.Fatalf("expected false bool setting to be dropped, got %#v", ns)
	}
}

// TestServiceViewsNilAndHTTPSInheritance verifies that typed service views
// return nil when unconfigured and inherit HTTPS from the environment when set.
func TestServiceViewsNilAndHTTPSInheritance(t *testing.T) {
	empty := Environment{HTTPS: true}
	if empty.Mailpit() != nil || empty.PHPMyAdmin() != nil || empty.Meilisearch() != nil || empty.Redis() != nil || empty.RabbitMQ() != nil || empty.Traefik() != nil {
		t.Fatalf("expected nil service views on unconfigured HTTPS environment")
	}

	configured := Environment{
		HTTPS: true,
		Tools: ToolsConfig{"mailpit": "1.30", "phpmyadmin": "5.2"},
	}
	if mailpit := configured.Mailpit(); mailpit == nil || !mailpit.HTTPS || mailpit.Version != "1.30" {
		t.Fatalf("Mailpit() = %#v, want version 1.30 with HTTPS true", mailpit)
	}
	if pma := configured.PHPMyAdmin(); pma == nil || !pma.HTTPS || pma.Version != "5.2" {
		t.Fatalf("PHPMyAdmin() = %#v, want version 5.2 with HTTPS true", pma)
	}
}
