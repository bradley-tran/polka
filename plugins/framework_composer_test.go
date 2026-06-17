package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"polka/config"
	"polka/tools"
)

func TestCakePHPPostComposerUpdatesAppLocalDatabaseConfig(t *testing.T) {
	projectDir := t.TempDir()
	appRoot := filepath.Join(projectDir, "cake")
	configDir := filepath.Join(appRoot, "config")
	if err := os.MkdirAll(filepath.Join(appRoot, "webroot"), 0o755); err != nil {
		t.Fatalf("MkdirAll(app) error = %v", err)
	}
	appLocal := strings.Join([]string{
		"<?php",
		"declare(strict_types=1);",
		"",
		"return [",
		"    'debug' => true,",
		"    'Datasources' => [",
		"        'default' => [",
		"            'host' => 'localhost',",
		"            'username' => 'my_app',",
		"            'password' => 'secret',",
		"            'database' => 'my_app',",
		"        ],",
		"    ],",
		"];",
		"",
	}, "\n")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(config) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "app_local.php"), []byte(appLocal), 0o644); err != nil {
		t.Fatalf("WriteFile(app_local.php) error = %v", err)
	}

	cakePHP, ok := NewDefaultRegistry().Framework(CakePHP)
	if !ok {
		t.Fatal("Framework(cakephp) ok = false, want true")
	}
	err := cakePHP.PostComposer(PostComposerContext{
		ProjectDir: projectDir,
		Environment: config.Environment{
			Framework: CakePHP,
			Docroot:   "cake/webroot",
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

	updated, err := os.ReadFile(filepath.Join(configDir, "app_local.php"))
	if err != nil {
		t.Fatalf("ReadFile(app_local.php) error = %v", err)
	}
	text := string(updated)
	for _, expected := range []string{
		"'debug' => true,",
		"'default' => [",
		"'test' => [",
		"'debug_kit' => [",
		"'className' => 'Cake\\\\Database\\\\Connection',",
		"'driver' => 'Cake\\\\Database\\\\Driver\\\\Mysql',",
		"'host' => '127.0.0.1',",
		"'port' => '3307',",
		"'username' => 'polka',",
		"'password' => 'secret',",
		"'database' => 'demo',",
		"'encoding' => 'utf8mb4',",
		"'url' => null,",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("app_local.php = %q, want %q", text, expected)
		}
	}
	if strings.Contains(text, "Sqlite") || strings.Contains(text, "sqlite://") {
		t.Fatalf("app_local.php = %q, want SQLite removed from generated datasources", text)
	}
}

func TestCodeIgniterPostComposerUpdatesDotenvDatabaseSecrets(t *testing.T) {
	projectDir := t.TempDir()
	appRoot := filepath.Join(projectDir, "site")
	if err := os.MkdirAll(filepath.Join(appRoot, "public"), 0o755); err != nil {
		t.Fatalf("MkdirAll(app) error = %v", err)
	}
	envData := strings.Join([]string{
		"CI_ENVIRONMENT = development",
		"# database.default.hostname = localhost",
		"# database.default.database = ci4",
		"# database.default.username = root",
		"# database.default.password = root",
		"# database.default.DBDriver = MySQLi",
		"# database.default.port = 3306",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(appRoot, "env"), []byte(envData), 0o644); err != nil {
		t.Fatalf("WriteFile(env) error = %v", err)
	}

	codeIgniter, ok := NewDefaultRegistry().Framework(CodeIgniter)
	if !ok {
		t.Fatal("Framework(codeigniter) ok = false, want true")
	}
	err := codeIgniter.PostComposer(PostComposerContext{
		ProjectDir: projectDir,
		Environment: config.Environment{
			Framework: CodeIgniter,
			Docroot:   "site/public",
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

	updated, err := os.ReadFile(filepath.Join(appRoot, ".env"))
	if err != nil {
		t.Fatalf("ReadFile(.env) error = %v", err)
	}
	text := string(updated)
	for _, expected := range []string{
		"CI_ENVIRONMENT = development",
		"database.default.hostname=127.0.0.1",
		"database.default.port=3307",
		"database.default.database=demo",
		"database.default.username=polka",
		"database.default.password=secret",
		"database.default.DBDriver=MySQLi",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf(".env = %q, want %q", text, expected)
		}
	}
}

func TestDrupalPostComposerWritesPolkaSettingsFile(t *testing.T) {
	projectDir := t.TempDir()
	settingsDir := filepath.Join(projectDir, "drupal", "web", "sites", "default")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(settings dir) error = %v", err)
	}
	defaultSettings := "<?php\n$settings['hash_salt'] = 'sample';\n"
	if err := os.WriteFile(filepath.Join(settingsDir, "default.settings.php"), []byte(defaultSettings), 0o644); err != nil {
		t.Fatalf("WriteFile(default.settings.php) error = %v", err)
	}

	drupal, ok := NewDefaultRegistry().Framework(Drupal)
	if !ok {
		t.Fatal("Framework(drupal) ok = false, want true")
	}
	err := drupal.PostComposer(PostComposerContext{
		ProjectDir: projectDir,
		Environment: config.Environment{
			Framework: Drupal,
			Docroot:   "drupal/web",
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

	settings, err := os.ReadFile(filepath.Join(settingsDir, "settings.php"))
	if err != nil {
		t.Fatalf("ReadFile(settings.php) error = %v", err)
	}
	if !strings.Contains(string(settings), polkaDrupalSettingsFile) {
		t.Fatalf("settings.php = %q, want Polka include", string(settings))
	}

	polkaSettings, err := os.ReadFile(filepath.Join(settingsDir, polkaDrupalSettingsFile))
	if err != nil {
		t.Fatalf("ReadFile(settings.polka.php) error = %v", err)
	}
	text := string(polkaSettings)
	for _, expected := range []string{
		"$databases['default']['default'] = [",
		"'database' => 'demo'",
		"'username' => 'polka'",
		"'password' => 'secret'",
		"'host' => '127.0.0.1'",
		"'port' => '3307'",
		"'driver' => 'mysql'",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("settings.polka.php = %q, want %q", text, expected)
		}
	}
}

func TestWordPressPostComposerUpdatesConfigDatabaseConstants(t *testing.T) {
	projectDir := t.TempDir()
	appRoot := filepath.Join(projectDir, "wordpress")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(app root) error = %v", err)
	}
	sample := strings.Join([]string{
		"<?php",
		"define( 'DB_NAME', 'database_name_here' );",
		"define( 'DB_USER', 'username_here' );",
		"define( 'DB_PASSWORD', 'password_here' );",
		"define( 'DB_HOST', 'localhost' );",
		"define( 'DB_CHARSET', 'utf8' );",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(appRoot, "wp-config-sample.php"), []byte(sample), 0o644); err != nil {
		t.Fatalf("WriteFile(wp-config-sample.php) error = %v", err)
	}

	wordpress, ok := NewDefaultRegistry().Framework(WordPress)
	if !ok {
		t.Fatal("Framework(wordpress) ok = false, want true")
	}
	err := wordpress.PostComposer(PostComposerContext{
		ProjectDir: projectDir,
		Environment: config.Environment{
			Framework: WordPress,
			Docroot:   "wordpress",
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

	configData, err := os.ReadFile(filepath.Join(appRoot, "wp-config.php"))
	if err != nil {
		t.Fatalf("ReadFile(wp-config.php) error = %v", err)
	}
	text := string(configData)
	for _, expected := range []string{
		"define( 'DB_NAME', 'demo' );",
		"define( 'DB_USER', 'polka' );",
		"define( 'DB_PASSWORD', 'secret' );",
		"define( 'DB_HOST', '127.0.0.1:3307' );",
		"define( 'DB_CHARSET', 'utf8' );",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("wp-config.php = %q, want %q", text, expected)
		}
	}
}

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
