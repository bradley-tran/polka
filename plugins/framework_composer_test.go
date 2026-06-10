package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"polka/config"
	"polka/tools"
)

func TestLaravelPostComposerUpdatesDotenvDatabaseSecrets(t *testing.T) {
	projectDir := t.TempDir()
	appRoot := filepath.Join(projectDir, "laravel")
	if err := os.MkdirAll(filepath.Join(appRoot, "public"), 0o755); err != nil {
		t.Fatalf("MkdirAll(app) error = %v", err)
	}
	envPath := filepath.Join(appRoot, ".env")
	envData := strings.Join([]string{
		"APP_NAME=Laravel",
		"DB_CONNECTION=sqlite",
		"# DB_HOST=127.0.0.1",
		"# DB_PORT=3306",
		"# DB_DATABASE=laravel",
		"# DB_USERNAME=root",
		"# DB_PASSWORD=",
		"",
	}, "\n")
	if err := os.WriteFile(envPath, []byte(envData), 0o644); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}

	laravel, ok := NewDefaultRegistry().Framework(Laravel)
	if !ok {
		t.Fatal("Framework(laravel) ok = false, want true")
	}
	err := laravel.PostComposer(PostComposerContext{
		ProjectDir: projectDir,
		Environment: config.Environment{
			Framework: Laravel,
			Docroot:   "laravel/public",
		},
		Database: &DatabaseCredentials{
			Host:         "127.0.0.1",
			Port:         3307,
			DatabaseName: "demo",
			User:         "polka",
			Password:     "secret",
		},
	})
	if err != nil {
		t.Fatalf("PostComposer() error = %v", err)
	}

	updated, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile(.env) error = %v", err)
	}
	text := string(updated)
	for _, expected := range []string{
		"APP_NAME=Laravel",
		"DB_CONNECTION=mysql",
		"DB_HOST=127.0.0.1",
		"DB_PORT=3307",
		"DB_DATABASE=demo",
		"DB_USERNAME=polka",
		"DB_PASSWORD=secret",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf(".env = %q, want %q", text, expected)
		}
	}
}

func TestSymfonyPostComposerWritesDatabaseURLToEnvLocal(t *testing.T) {
	projectDir := t.TempDir()
	appRoot := filepath.Join(projectDir, "site")
	if err := os.MkdirAll(filepath.Join(appRoot, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll(app) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(appRoot, "bin", "console"), []byte("console\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(console) error = %v", err)
	}

	symfony, ok := NewDefaultRegistry().Framework(Symfony)
	if !ok {
		t.Fatal("Framework(symfony) ok = false, want true")
	}
	err := symfony.PostComposer(PostComposerContext{
		ProjectDir: projectDir,
		Environment: config.Environment{
			Framework: Symfony,
			Docroot:   "site/public",
			Database: &config.DatabaseConfig{
				Engine:  tools.MariaDB,
				Version: "11.8",
			},
		},
		Database: &DatabaseCredentials{
			Host:         "127.0.0.1",
			Port:         3307,
			DatabaseName: "demo_app",
			User:         "polka",
			Password:     "secret",
		},
	})
	if err != nil {
		t.Fatalf("PostComposer() error = %v", err)
	}

	envLocal, err := os.ReadFile(filepath.Join(appRoot, ".env.local"))
	if err != nil {
		t.Fatalf("ReadFile(.env.local) error = %v", err)
	}
	expected := `DATABASE_URL="mysql://polka:secret@127.0.0.1:3307/demo_app?charset=utf8mb4&serverVersion=mariadb-11.8"`
	if !strings.Contains(string(envLocal), expected) {
		t.Fatalf(".env.local = %q, want %q", string(envLocal), expected)
	}
}
