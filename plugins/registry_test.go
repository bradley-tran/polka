package plugins

import (
	"reflect"
	"testing"

	"polka/tools"
)

func TestDefaultRegistrySupportsFrameworkPlugins(t *testing.T) {
	registry := NewDefaultRegistry()

	if got, want := registry.SupportedFrameworks(), []string{Drupal, Laravel, WordPress}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SupportedFrameworks() = %#v, want %#v", got, want)
	}
	for _, id := range []string{Drupal, WordPress, Laravel} {
		if _, ok := registry.Framework(id); !ok {
			t.Fatalf("Framework(%q) ok = false, want true", id)
		}
	}
	if err := registry.ValidateFramework("symfony"); err == nil {
		t.Fatal("ValidateFramework(symfony) error = nil, want unsupported framework error")
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
