package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"polka/config"
	"polka/tools"
)

func postgreSQLFrameworkContext() RuntimeEnvContext {
	return RuntimeEnvContext{
		Environment: config.Environment{Tools: config.ToolsConfig{tools.PostgreSQL: "17"}, Database: &config.DatabaseConfig{Engine: tools.PostgreSQL, Version: "17"}},
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
	if got := runtimeEnvForTest(t, laravel, ctx)["DB_CONNECTION"]; got != "pgsql" {
		t.Fatalf("Laravel DB_CONNECTION = %q, want pgsql", got)
	}
	codeIgniter, _ := registry.Framework(CodeIgniter)
	if got := runtimeEnvForTest(t, codeIgniter, ctx)["database.default.DBDriver"]; got != "Postgre" {
		t.Fatalf("CodeIgniter DBDriver = %q, want Postgre", got)
	}
	symfony, _ := registry.Framework(Symfony)
	if got := runtimeEnvForTest(t, symfony, ctx)["DATABASE_URL"]; !strings.HasPrefix(got, "postgresql://") || !strings.Contains(got, "serverVersion=17") {
		t.Fatalf("Symfony DATABASE_URL = %q, want PostgreSQL URL", got)
	}
}

// TestFrameworksRenderPostgreSQLComposerSettings verifies the public action
// pipeline rather than framework-specific rendering helpers.
func TestFrameworksRenderPostgreSQLComposerSettings(t *testing.T) {
	for _, test := range []struct {
		id, path string
		expected []string
	}{
		{CakePHP, "config/app_local.php", []string{"Cake\\\\Database\\\\Driver\\\\Postgres", "'encoding' => 'utf8'"}},
		{Drupal, "web/sites/default/settings.polka.php", []string{"'driver' => 'pgsql'", "Drupal\\\\pgsql"}},
	} {
		t.Run(test.id, func(t *testing.T) {
			plugin, ok := NewDefaultRegistry().Framework(test.id)
			if !ok {
				t.Fatal("framework missing")
			}
			dir := t.TempDir()
			ctx := postgreSQLFrameworkContext()
			ctx.Environment.Docroot = plugin.Defaults().Docroot
			if err := plugin.PostComposer(PostComposerContext{ProjectDir: dir, Environment: ctx.Environment, Database: ctx.Database}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(test.path)))
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range test.expected {
				if !strings.Contains(string(data), expected) {
					t.Fatalf("output %q missing %q", data, expected)
				}
			}
		})
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
