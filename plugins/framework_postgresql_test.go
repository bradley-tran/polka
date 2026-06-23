package plugins

import (
	"strings"
	"testing"

	"polka/config"
	"polka/tools"
)

func postgreSQLFrameworkContext() RuntimeEnvContext {
	return RuntimeEnvContext{
		Environment: config.Environment{PostgreSQLVersion: "17", Database: &config.DatabaseConfig{Engine: tools.PostgreSQL, Version: "17"}},
		Database: &DatabaseCredentials{
			Engine:       tools.PostgreSQL,
			Version:      "17",
			Host:         "127.0.0.1",
			Port:         5432,
			DatabaseName: "demo",
			User:         "polka",
			Password:     "secret",
		},
	}
}

func TestFrameworksRenderPostgreSQLRuntimeSettings(t *testing.T) {
	registry := NewDefaultRegistry()
	ctx := postgreSQLFrameworkContext()

	laravel, _ := registry.Framework(Laravel)
	if got := laravel.RuntimeEnv(ctx)["DB_CONNECTION"]; got != "pgsql" {
		t.Fatalf("Laravel DB_CONNECTION = %q, want pgsql", got)
	}
	codeIgniter, _ := registry.Framework(CodeIgniter)
	if got := codeIgniter.RuntimeEnv(ctx)["database.default.DBDriver"]; got != "Postgre" {
		t.Fatalf("CodeIgniter DBDriver = %q, want Postgre", got)
	}
	symfony, _ := registry.Framework(Symfony)
	if got := symfony.RuntimeEnv(ctx)["DATABASE_URL"]; !strings.HasPrefix(got, "postgresql://") || !strings.Contains(got, "serverVersion=17") {
		t.Fatalf("Symfony DATABASE_URL = %q, want PostgreSQL URL", got)
	}
}

func TestFrameworksRenderPostgreSQLComposerSettings(t *testing.T) {
	credentials := postgreSQLFrameworkContext().Database
	cake := cakePHPDatasourceConfigs(credentials)["default"]
	if cake["driver"].value != "Cake\\Database\\Driver\\Postgres" || cake["encoding"].value != "utf8" {
		t.Fatalf("CakePHP datasource = %#v, want PostgreSQL driver", cake)
	}
	drupal := renderDrupalPolkaSettings(credentials)
	if !strings.Contains(drupal, "'driver' => 'pgsql'") || !strings.Contains(drupal, "Drupal\\\\pgsql") {
		t.Fatalf("Drupal settings = %q, want pgsql driver", drupal)
	}
}

func TestFrameworkPostgreSQLExtensionsAndCompatibility(t *testing.T) {
	registry := NewDefaultRegistry()
	environment := config.Environment{Framework: Laravel, Database: &config.DatabaseConfig{Engine: tools.PostgreSQL, Version: "17"}}
	laravel, _ := registry.Framework(Laravel)
	extensions := registry.ToolRegistry().PHPExtensions(environment)
	if !extensions["pgsql"] || !extensions["pdo_pgsql"] || extensions["mysqli"] || extensions["pdo_mysql"] {
		t.Fatalf("PostgreSQL tool extensions = %#v", extensions)
	}
	if laravel.PHPExtensions()["pgsql"] || laravel.PHPExtensions()["pdo_pgsql"] {
		t.Fatalf("Laravel extensions = %#v, want database drivers owned by tools", laravel.PHPExtensions())
	}

	environment.Framework = WordPress
	err := registry.ValidateEnvironment(environment)
	if err == nil || !strings.Contains(err.Error(), "does not support database engine") {
		t.Fatalf("ValidateEnvironment(WordPress/PostgreSQL) error = %v, want incompatibility", err)
	}
}
