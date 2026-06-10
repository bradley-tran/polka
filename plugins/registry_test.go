package plugins

import (
	"reflect"
	"testing"

	"polka/config"
	"polka/tools"
)

func TestDefaultRegistrySupportsFrameworkPlugins(t *testing.T) {
	registry := NewDefaultRegistry()

	if got, want := registry.SupportedFrameworks(), []string{Drupal, Laravel, Symfony, WordPress}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SupportedFrameworks() = %#v, want %#v", got, want)
	}
	for _, id := range []string{Drupal, WordPress, Laravel, Symfony} {
		if _, ok := registry.Framework(id); !ok {
			t.Fatalf("Framework(%q) ok = false, want true", id)
		}
	}
	if err := registry.ValidateFramework("cakephp"); err == nil {
		t.Fatal("ValidateFramework(cakephp) error = nil, want unsupported framework error")
	}
}

func TestRegistryRejectsDuplicateFrameworkPlugins(t *testing.T) {
	frameworks := DefaultFrameworkPlugins()
	if _, err := NewRegistry(tools.NewDefaultRegistry(), frameworks[0], frameworks[0]); err == nil {
		t.Fatal("NewRegistry(duplicate framework) error = nil, want duplicate framework error")
	}
}

func TestFrameworkDefaultsUseExpectedPresetMatrix(t *testing.T) {
	registry := NewDefaultRegistry()

	drupal, ok := registry.Framework(Drupal)
	if !ok {
		t.Fatal("Framework(drupal) ok = false, want true")
	}
	drupalDefaults := drupal.Defaults()
	if drupalDefaults.Framework != Drupal || drupalDefaults.Docroot != "web" || drupalDefaults.ComposerVersion != "2.8" || drupalDefaults.NodeJSVersion != "24" || drupalDefaults.Mailpit == nil {
		t.Fatalf("Drupal defaults = %#v, want full Drupal preset", drupalDefaults)
	}
	if drupalDefaults.OPcachePreset != config.OPcachePresetDev || drupalDefaults.OPcacheConfig["opcache.save_comments"] != "1" {
		t.Fatalf("Drupal OPcache defaults = %q %#v, want dev preset with save_comments", drupalDefaults.OPcachePreset, drupalDefaults.OPcacheConfig)
	}
	assertExtensionsEnabled(t, drupalDefaults.PHPExtensions, []string{
		"curl",
		"dom",
		"fileinfo",
		"gd",
		"intl",
		"mbstring",
		"mysqli",
		"opcache",
		"openssl",
		"pdo_mysql",
		"pdo_sqlite",
		"simplexml",
		"sqlite3",
		"xmlreader",
		"xsl",
		"zip",
		"zlib",
	})

	laravel, ok := registry.Framework(Laravel)
	if !ok {
		t.Fatal("Framework(laravel) ok = false, want true")
	}
	laravelDefaults := laravel.Defaults()
	if laravelDefaults.Framework != Laravel || laravelDefaults.Docroot != "public" || laravelDefaults.ComposerVersion != "2.8" || laravelDefaults.NodeJSVersion != "24" || laravelDefaults.Mailpit == nil {
		t.Fatalf("Laravel defaults = %#v, want full Laravel preset", laravelDefaults)
	}
	if laravelDefaults.OPcachePreset != config.OPcachePresetDev || len(laravelDefaults.OPcacheConfig) != 0 {
		t.Fatalf("Laravel OPcache defaults = %q %#v, want dev preset only", laravelDefaults.OPcachePreset, laravelDefaults.OPcacheConfig)
	}
	assertExtensionsEnabled(t, laravelDefaults.PHPExtensions, []string{
		"bcmath",
		"curl",
		"dom",
		"fileinfo",
		"gd",
		"intl",
		"mbstring",
		"mysqli",
		"opcache",
		"openssl",
		"pdo_mysql",
		"pdo_sqlite",
		"simplexml",
		"sqlite3",
		"xmlreader",
		"xsl",
		"zip",
	})

	symfony, ok := registry.Framework(Symfony)
	if !ok {
		t.Fatal("Framework(symfony) ok = false, want true")
	}
	symfonyDefaults := symfony.Defaults()
	if symfonyDefaults.Framework != Symfony || symfonyDefaults.Docroot != "public" || symfonyDefaults.ComposerVersion != "2.8" || symfonyDefaults.NodeJSVersion != "24" || symfonyDefaults.Mailpit == nil {
		t.Fatalf("Symfony defaults = %#v, want full Symfony preset", symfonyDefaults)
	}
	if symfonyDefaults.OPcachePreset != config.OPcachePresetDev || len(symfonyDefaults.OPcacheConfig) != 0 {
		t.Fatalf("Symfony OPcache defaults = %q %#v, want dev preset only", symfonyDefaults.OPcachePreset, symfonyDefaults.OPcacheConfig)
	}
	assertExtensionsEnabled(t, symfonyDefaults.PHPExtensions, []string{
		"ctype",
		"curl",
		"dom",
		"fileinfo",
		"gd",
		"iconv",
		"intl",
		"mbstring",
		"mysqli",
		"opcache",
		"openssl",
		"pdo_mysql",
		"pdo_sqlite",
		"session",
		"simplexml",
		"sqlite3",
		"tokenizer",
		"xmlreader",
		"zip",
	})

	wordpress, ok := registry.Framework(WordPress)
	if !ok {
		t.Fatal("Framework(wordpress) ok = false, want true")
	}
	wordpressDefaults := wordpress.Defaults()
	if wordpressDefaults.Framework != WordPress || wordpressDefaults.Docroot != "." || wordpressDefaults.ComposerVersion != "" || wordpressDefaults.NodeJSVersion != "" || wordpressDefaults.Mailpit != nil {
		t.Fatalf("WordPress defaults = %#v, want lighter WordPress preset", wordpressDefaults)
	}
	if wordpressDefaults.PHPVersion != "8.4" || wordpressDefaults.NginxVersion != "1.30" || wordpressDefaults.MariaDBVersion != "11.8" || wordpressDefaults.PHPMyAdmin == nil {
		t.Fatalf("WordPress defaults = %#v, want PHP/nginx/MariaDB/phpMyAdmin", wordpressDefaults)
	}
	if wordpressDefaults.OPcachePreset != config.OPcachePresetDev || len(wordpressDefaults.OPcacheConfig) != 0 {
		t.Fatalf("WordPress OPcache defaults = %q %#v, want dev preset only", wordpressDefaults.OPcachePreset, wordpressDefaults.OPcacheConfig)
	}
	assertExtensionsEnabled(t, wordpressDefaults.PHPExtensions, []string{
		"bcmath",
		"curl",
		"dom",
		"exif",
		"fileinfo",
		"ftp",
		"gd",
		"iconv",
		"intl",
		"mbstring",
		"mysqli",
		"opcache",
		"openssl",
		"pdo_mysql",
		"shmop",
		"simplexml",
		"sockets",
		"sodium",
		"xmlreader",
		"xsl",
		"zip",
		"zlib",
	})
}

func assertExtensionsEnabled(t *testing.T, extensions map[string]bool, expected []string) {
	t.Helper()

	for _, extension := range expected {
		if !extensions[extension] {
			t.Fatalf("php-extensions = %#v, want %s enabled", extensions, extension)
		}
	}
}

func TestFrameworkRuntimeEnvUsesDatabaseCredentials(t *testing.T) {
	registry := NewDefaultRegistry()
	laravel, ok := registry.Framework(Laravel)
	if !ok {
		t.Fatal("Framework(laravel) ok = false, want true")
	}

	values := laravel.RuntimeEnv(RuntimeEnvContext{
		Database: &DatabaseCredentials{
			Host:         "127.0.0.1",
			Port:         3307,
			DatabaseName: "demo",
			User:         "polka",
			Password:     "secret",
		},
	})
	for key, want := range map[string]string{
		"DB_CONNECTION": "mysql",
		"DB_HOST":       "127.0.0.1",
		"DB_PORT":       "3307",
		"DB_DATABASE":   "demo",
		"DB_USERNAME":   "polka",
		"DB_PASSWORD":   "secret",
	} {
		if values[key] != want {
			t.Fatalf("RuntimeEnv()[%s] = %q, want %q", key, values[key], want)
		}
	}
}

func TestSymfonyRuntimeEnvUsesDatabaseURL(t *testing.T) {
	registry := NewDefaultRegistry()
	symfony, ok := registry.Framework(Symfony)
	if !ok {
		t.Fatal("Framework(symfony) ok = false, want true")
	}

	values := symfony.RuntimeEnv(RuntimeEnvContext{
		Environment: config.Environment{
			Framework: Symfony,
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
	if got, want := values["DATABASE_URL"], "mysql://polka:secret@127.0.0.1:3307/demo_app?charset=utf8mb4&serverVersion=mariadb-11.8"; got != want {
		t.Fatalf("RuntimeEnv()[DATABASE_URL] = %q, want %q", got, want)
	}
}
