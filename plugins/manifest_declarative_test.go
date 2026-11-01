package plugins

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"polka/config"
)

// TestManifestOnlyFramework exercises registration, detection, every file
// action, and runtime bindings for a framework with no Go implementation.
func TestManifestOnlyFramework(t *testing.T) {
	files := fstest.MapFS{
		"manifests/acme.yaml": {Data: []byte(`
id: acme
defaults: {docroot: public}
database-engines: [mysql, mariadb, postgresql]
detection:
  package-prefixes: [acme/]
  markers: [acme.lock]
app-root:
  public-dir: public
  markers: [acme.lock]
values:
  driver: {by-engine: {mysql: mysql, mariadb: mysql, postgresql: pgsql}}
runtime-env:
  requires-database: true
  values:
    ACME_DRIVER: {from: values.driver}
    ACME_PASSWORD: {from: database.password}
post-composer:
  requires-database: true
  actions:
    - type: dotenv
      path: .env
      fallback: .env.example
      values:
        ACME_DRIVER: {from: runtime-env.ACME_DRIVER}
        ACME_PASSWORD: {from: runtime-env.ACME_PASSWORD}
    - type: php-array
      path: config.php
      fallback-template-file: array.php
      updates:
        - path: [Database, primary]
          values:
            driver: {from: values.driver}
            password: {from: database.password}
            url: null
    - type: php-constants
      path: constants.php
      fallback-template-file: constants.php
      insert-before: BOOTSTRAP
      values:
        DATABASE_PASSWORD: {from: database.password}
    - type: write-file
      base: docroot
      path: managed.php
      template-file: managed.php
      encoding: php-literal
      values:
        password: {from: database.password}
        driver: {from: values.driver}
    - type: php-include
      base: docroot
      path: settings.php
      fallback: settings.example.php
      include: managed.php
      marker: acme
`)},
		"manifests/templates/array.php":     {Data: []byte("<?php\r\nreturn array(\r\n  'custom' => keep_this(), // user setting\r\n);\r\n")},
		"manifests/templates/constants.php": {Data: []byte("<?php\n// user comment\ndefine('BOOTSTRAP', true);\n")},
		"manifests/templates/managed.php":   {Data: []byte("<?php\n$password = {{password}};\n$driver = {{driver}};\n")},
	}
	frameworks, err := loadFrameworkPlugins(files)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(nil, frameworks...)
	if err != nil {
		t.Fatal(err)
	}
	plugin, ok := registry.Framework("acme")
	if !ok {
		t.Fatal("manifest did not register")
	}
	if got, err := registry.DetectFramework("ACME/app:^2", t.TempDir()); err != nil || got != "acme" {
		t.Fatalf("package detection = %q, %v", got, err)
	}
	dir := t.TempDir()
	app := filepath.Join(dir, "site")
	public := filepath.Join(app, "public")
	if err := os.MkdirAll(public, 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginTestFile(t, filepath.Join(app, "acme.lock"), "marker")
	writePluginTestFile(t, filepath.Join(app, ".env.example"), "APP_NAME=Acme\r\n# ACME_DRIVER=old\r\n")
	writePluginTestFile(t, filepath.Join(public, "settings.example.php"), "<?php\n$custom = true;\n")
	if got, err := registry.DetectFramework("unknown/app", app); err != nil || got != "acme" {
		t.Fatalf("marker detection = %q, %v", got, err)
	}
	password := "a'\\b\"${secret}\n{{driver}}"
	ctx := PostComposerContext{ProjectDir: dir, Environment: config.Environment{Framework: "acme", Docroot: "site/public"}, Database: &DatabaseCredentials{Engine: "postgresql", DatabaseName: "demo", User: "user", Password: password}}
	values := runtimeEnvForTest(t, plugin, RuntimeEnvContext{Environment: ctx.Environment, Database: ctx.Database})
	if values["ACME_DRIVER"] != "pgsql" || values["ACME_PASSWORD"] != password {
		t.Fatalf("runtime values = %#v", values)
	}
	if err := plugin.PostComposer(ctx); err != nil {
		t.Fatal(err)
	}
	expected := map[string][]string{
		"site/.env":                {"APP_NAME=Acme\r\n", "ACME_DRIVER=pgsql\r\n", `\${secret}`, `\n{{driver}}`},
		"site/config.php":          {"'custom' => keep_this(), // user setting\r\n", "'driver' => 'pgsql'", "'url' => null", "'password' => " + phpSingleQuotedString(password)},
		"site/constants.php":       {"// user comment", "define( 'DATABASE_PASSWORD', " + phpSingleQuotedString(password) + " );", "define('BOOTSTRAP', true);"},
		"site/public/managed.php":  {"$password = " + phpSingleQuotedString(password) + ";", "$driver = 'pgsql';"},
		"site/public/settings.php": {"$custom = true;", "// <polka:acme>", "__DIR__ . '/managed.php'"},
	}
	before := map[string][]byte{}
	for name, parts := range expected {
		data := readPluginTestFile(t, filepath.Join(dir, filepath.FromSlash(name)))
		for _, part := range parts {
			if !bytes.Contains(data, []byte(part)) {
				t.Fatalf("%s missing %q:\n%s", name, part, data)
			}
		}
		before[name] = data
	}
	constants := string(before["site/constants.php"])
	if strings.Index(constants, "DATABASE_PASSWORD") > strings.Index(constants, "define('BOOTSTRAP'") {
		t.Fatal("missing constants inserted after bootstrap")
	}
	if err := plugin.PostComposer(ctx); err != nil {
		t.Fatal(err)
	}
	for name, original := range before {
		if data := readPluginTestFile(t, filepath.Join(dir, filepath.FromSlash(name))); !bytes.Equal(data, original) {
			t.Fatalf("second run changed %s", name)
		}
	}
	ctx.Database.Password = "rotated"
	if err := plugin.PostComposer(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readPluginTestFile(t, filepath.Join(app, "config.php"))), "'password' => 'rotated'") {
		t.Fatal("credential rotation was not applied")
	}
	if values := runtimeEnvForTest(t, plugin, RuntimeEnvContext{}); len(values) != 0 {
		t.Fatalf("unguarded variables without database: %v", values)
	}
	empty := t.TempDir()
	if err := plugin.PostComposer(PostComposerContext{ProjectDir: empty}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(empty)
	if err != nil || len(entries) != 0 {
		t.Fatalf("guarded actions created files: %v, %v", entries, err)
	}
}

// TestDeclarativeManifestValidation checks failure at load time, including
// references hidden in unused branches and old strategy declarations.
func TestDeclarativeManifestValidation(t *testing.T) {
	base := "id: demo\ndefaults: {docroot: public}\napp-root: {public-dir: public}\n"
	for _, test := range []struct{ name, tail, want string }{
		{"unknown field", "unknown: true", "unknown"},
		{"legacy runtime", "runtime-env: {database: {keys: standard}}", "database"},
		{"legacy composer", "post-composer: {strategy: dotenv}", "strategy"},
		{"unknown source", "values: {x: {from: database.token}}", "unknown reference"},
		{"unknown variable", "runtime-env: {values: {X: {from: values.missing}}}", "unknown reference"},
		{"cycle", "values: {a: {from: values.b}, b: {from: values.a}}", "reference cycle"},
		{"runtime cycle", "runtime-env: {values: {X: {from: runtime-env.X}}}", "reference cycle"},
		{"conflicting binding", "values: {x: {from: database.host, by-engine: {mysql: x}}}", "exactly one"},
		{"misspelled binding", "values: {x: {form: database.host}}", "form"},
		{"non scalar", "values: {x: [a, b]}", "scalar"},
		{"unknown formatter", "values: {x: {format: {kind: shell}}}", "unsupported formatter"},
		{"bad formatter fields", "values: {x: {format: {kind: string, value: x, host: x}}}", "unexpected fields"},
		{"missing URL path", "values: {x: {format: {kind: url, scheme: mysql, host: localhost}}}", "requires scheme and path"},
		{"unknown engine", "values: {x: {by-engine: {oracle: x}}}", "unsupported by-engine"},
		{"missing engine", "database-engines: [mysql, postgresql]\nvalues: {x: {by-engine: {mysql: x}}}", "missing declared"},
		{"unused branch", "values: {x: {by-engine: {mysql: x, postgresql: {from: values.missing}}}}", "unknown reference"},
		{"bad env key", "runtime-env: {values: {'BAD=KEY': literal}}", "invalid runtime"},
		{"generic marker", "detection: {markers: [composer.json]}", "not distinctive"},
		{"unsafe marker", "detection: {markers: [../marker]}", "relative path"},
		{"unsafe public dir", "app-root: {public-dir: ../public}", "relative path"},
		{"unknown action", "post-composer: {actions: [{type: shell, path: file}]}", "unsupported action"},
		{"escaped target", "post-composer: {actions: [{type: dotenv, path: ../.env, values: {X: y}}]}", "relative path"},
		{"drive target", "post-composer: {actions: [{type: dotenv, path: 'C:/file', values: {X: y}}]}", "relative path"},
		{"backslash target", "post-composer: {actions: [{type: dotenv, path: 'a\\file', values: {X: y}}]}", "relative path"},
		{"ambiguous Windows path", "post-composer: {actions: [{type: dotenv, path: '.. /file', values: {X: y}}]}", "trailing dots or spaces"},
		{"escaped fallback", "post-composer: {actions: [{type: dotenv, path: .env, fallback: ../env, values: {X: y}}]}", "relative path"},
		{"missing template", "post-composer: {actions: [{type: php-array, path: file.php, fallback-template-file: missing.php}]}", "read template"},
		{"unknown placeholder", "post-composer: {actions: [{type: write-file, path: file.php, template-file: test.php, encoding: text}]}", "unknown template placeholder"},
		{"fallback placeholder", "post-composer: {actions: [{type: php-constants, path: file.php, fallback-template-file: test.php, values: {DB: value}}]}", "without placeholders"},
		{"missing encoding", "post-composer: {actions: [{type: write-file, path: file.php, template-file: test.php}]}", "encoding"},
		{"conflicting action", "post-composer: {actions: [{type: dotenv, path: .env, values: {X: y}, include: file.php}]}", "require php-include"},
		{"unknown order", "post-composer: {actions: [{type: dotenv, path: .env, values: {X: y}, order: [Y]}]}", "unknown or duplicate"},
		{"duplicate order", "post-composer: {actions: [{type: dotenv, path: .env, values: {X: y}, order: [X, X]}]}", "unknown or duplicate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := fstest.MapFS{"manifests/templates/test.php": {Data: []byte("{{missing}}")}}
			header := base
			if strings.HasPrefix(test.tail, "app-root:") {
				header = strings.Replace(header, "app-root: {public-dir: public}\n", "", 1)
			}
			_, err := parseFrameworkManifestWithFiles([]byte(header+test.tail+"\n"), files)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestFrameworkRuntimeFormatters exercises encoding and version policy through
// the migrated Symfony manifest, including defaults and tool-version fallback.
func TestFrameworkRuntimeFormatters(t *testing.T) {
	plugin := newManifestFrameworkPlugin(Symfony)
	for _, test := range []struct{ engine, version, toolVersion, host, want string }{
		{"mariadb", "11.8", "", "127.0.0.1", "mysql://a%40b:p%3A%2F%3F%23%25@127.0.0.1:3306/a%2Fb?charset=utf8mb4&serverVersion=mariadb-11.8.0"},
		{"mariadb", "mariadb-11.8.2", "", "127.0.0.1", "serverVersion=mariadb-11.8.2"},
		{"mariadb", "", "", "127.0.0.1", "serverVersion=mariadb-11.8.0"},
		{"mysql", "", "8.4", "127.0.0.1", "serverVersion=8.4"},
		{"mysql", "", "", "127.0.0.1", "serverVersion=mysql"},
		{"postgresql", "17", "", "::1", "postgresql://a%40b:p%3A%2F%3F%23%25@[::1]:5432/a%2Fb?charset=utf8&serverVersion=17"},
	} {
		t.Run(test.engine+test.version+test.toolVersion, func(t *testing.T) {
			ctx := RuntimeEnvContext{Environment: config.Environment{Database: &config.DatabaseConfig{Engine: test.engine, Version: test.version}, Tools: config.ToolsConfig{test.engine: test.toolVersion}}, Database: &DatabaseCredentials{Engine: test.engine, Host: test.host, DatabaseName: "a/b", User: "a@b", Password: "p:/?#%"}}
			values := runtimeEnvForTest(t, plugin, ctx)
			if !strings.Contains(values["DATABASE_URL"], test.want) {
				t.Fatalf("URL = %q, want %q", values["DATABASE_URL"], test.want)
			}
			if ctx.Database.Port != 0 {
				t.Fatal("hook mutated caller credentials")
			}
		})
	}
}

// TestConfiguredDatabaseVersionPrecedence prevents stale credential metadata
// from changing the framework URL when project configuration selects a version.
func TestConfiguredDatabaseVersionPrecedence(t *testing.T) {
	plugin := newManifestFrameworkPlugin(Symfony)
	for _, test := range []struct{ configured, tool, credentials, want string }{
		{"11.9", "12.0", "11.8", "mariadb-11.9.0"},
		{"", "12.0", "11.8", "mariadb-12.0.0"},
		{"", "", "11.8", "mariadb-11.8.0"},
	} {
		ctx := RuntimeEnvContext{Environment: config.Environment{Database: &config.DatabaseConfig{Engine: "mariadb", Version: test.configured}, Tools: config.ToolsConfig{"mariadb": test.tool}}, Database: &DatabaseCredentials{Engine: "mariadb", Version: test.credentials}}
		values := runtimeEnvForTest(t, plugin, ctx)
		if !strings.Contains(values["DATABASE_URL"], "serverVersion="+test.want) {
			t.Fatalf("version precedence = %q, want %q", values["DATABASE_URL"], test.want)
		}
	}
}

// TestStructuredValuesWithoutDatabase confirms static declarations do not need
// credentials and preserve scalar types until the requested text conversion.
func TestStructuredValuesWithoutDatabase(t *testing.T) {
	m, err := parseFrameworkManifest([]byte(`
id: static
defaults: {docroot: public}
values:
  empty: null
  fallback: {from: values.empty, default: ready}
runtime-env:
  values:
    STATUS: {from: values.fallback}
    COUNT: {format: {kind: string, value: 42}}
    FLAG: true
`))
	if err != nil {
		t.Fatal(err)
	}
	plugin, err := m.toPlugin()
	if err != nil {
		t.Fatal(err)
	}
	values := runtimeEnvForTest(t, plugin, RuntimeEnvContext{})
	if !reflect.DeepEqual(values, map[string]string{"STATUS": "ready", "COUNT": "42", "FLAG": "true"}) {
		t.Fatalf("static values = %#v", values)
	}
	for _, value := range []any{nil, true, false, 42, uint64(43), 4.5, "a'\\b"} {
		if _, err := renderPHPScalar(value); err != nil {
			t.Fatalf("scalar %v: %v", value, err)
		}
	}
}

// TestRuntimeBindingErrorsReturnToCaller checks evaluation failures, including
// invalid ports, without exposing managed secret values in diagnostics.
func TestRuntimeBindingErrorsReturnToCaller(t *testing.T) {
	plugin := newManifestFrameworkPlugin(Symfony)
	ctx := RuntimeEnvContext{Database: &DatabaseCredentials{Engine: "mysql", Port: -1, Password: "never-print-this-secret"}}
	if _, err := plugin.RuntimeEnv(ctx); err == nil || strings.Contains(err.Error(), ctx.Database.Password) {
		t.Fatalf("runtime error = %v", err)
	}
	m, err := parseFrameworkManifest([]byte("id: unguarded\ndefaults: {docroot: public}\nruntime-env: {values: {HOST: {from: database.host}}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	unguarded, err := m.toPlugin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unguarded.RuntimeEnv(RuntimeEnvContext{}); err == nil {
		t.Fatal("missing database reference did not return an error")
	}
}

// TestBuiltinFrameworkEngineMatrix checks runtime and Composer outputs for all
// declared built-in engine combinations through the shared public interfaces.
func TestBuiltinFrameworkEngineMatrix(t *testing.T) {
	paths := map[string]string{CakePHP: "config/app_local.php", CodeIgniter: ".env", Drupal: "web/sites/default/settings.polka.php", WordPress: "wp-config.php", Laravel: ".env", Symfony: ".env.local"}
	for _, plugin := range DefaultFrameworkPlugins() {
		for _, engine := range []string{"mysql", "mariadb", "postgresql"} {
			t.Run(plugin.ID()+"/"+engine, func(t *testing.T) {
				environment := plugin.Defaults()
				environment.Database = &config.DatabaseConfig{Engine: engine}
				if err := plugin.ValidateEnvironment(environment); err != nil {
					if plugin.ID() != WordPress || engine != "postgresql" {
						t.Fatal(err)
					}
					return
				}
				db := &DatabaseCredentials{Engine: engine, DatabaseName: "matrix_db", User: "matrix_user", Password: "matrix_password"}
				values := runtimeEnvForTest(t, plugin, RuntimeEnvContext{Environment: environment, Database: db})
				if len(values) == 0 {
					t.Fatal("no runtime bindings")
				}
				dir := t.TempDir()
				if err := plugin.PostComposer(PostComposerContext{ProjectDir: dir, Environment: environment, Database: db}); err != nil {
					t.Fatal(err)
				}
				data := readPluginTestFile(t, filepath.Join(dir, filepath.FromSlash(paths[plugin.ID()])))
				for _, value := range []string{db.DatabaseName, db.User, db.Password} {
					if !bytes.Contains(data, []byte(value)) {
						t.Fatalf("output missing %q: %s", value, data)
					}
				}
				if strings.HasSuffix(paths[plugin.ID()], ".php") {
					if _, err := scanPHP(string(data)); err != nil {
						t.Fatalf("generated PHP: %v", err)
					}
				}
			})
		}
	}
}

// TestDetectionPriorityAndIsolation verifies category precedence, stable ties,
// metadata copies, and errors instead of silently swallowing bad markers.
func TestDetectionPriorityAndIsolation(t *testing.T) {
	var frameworks []FrameworkPlugin
	for _, data := range []string{
		"id: a\ndetection: {package-prefixes: [vendor/], markers: [a.lock], priority: 1}\n",
		"id: b\ndetection: {package-substrings: [vendor], markers: [b.lock], priority: 100}\n",
		"id: c\ndetection: {package-prefixes: [vendor/], markers: [c.lock], priority: 1}\n",
	} {
		m, err := parseFrameworkManifest([]byte(data + "defaults: {docroot: public}\n"))
		if err != nil {
			t.Fatal(err)
		}
		p, err := m.toPlugin()
		if err != nil {
			t.Fatal(err)
		}
		frameworks = append(frameworks, p)
	}
	registry, err := NewRegistry(nil, frameworks...)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writePluginTestFile(t, filepath.Join(dir, "b.lock"), "")
	if got, err := registry.DetectFramework("vendor/app", dir); err != nil || got != "a" {
		t.Fatalf("prefix/tie detection = %q, %v", got, err)
	}
	copy := frameworks[0].Detection()
	copy.PackagePrefixes[0] = "mutated/"
	if reflect.DeepEqual(copy, frameworks[0].Detection()) {
		t.Fatal("Detection exposed mutable registry data")
	}
	if got, err := registry.DetectFramework("unknown", dir); err != nil || got != "b" {
		t.Fatalf("marker priority = %q, %v", got, err)
	}
	// A NUL in a caller-supplied directory is a reliable stat failure on all OSes.
	if _, err := registry.DetectFramework("unknown", "bad\x00directory"); err == nil {
		t.Fatal("stat error was swallowed")
	}
}

// writePluginTestFile checks fixture writes used by filesystem behavior tests.
func writePluginTestFile(t *testing.T, filename, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readPluginTestFile checks reads before assertions inspect generated bytes.
func readPluginTestFile(t *testing.T, filename string) []byte {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
