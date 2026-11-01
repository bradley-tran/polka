package plugins

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"polka/config"
)

// TestPHPArrayPatchesPreserveSource covers both array syntaxes, nested calls,
// comments, escaped keys, inline arrays, and missing branches without PHP exec.
func TestPHPArrayPatchesPreserveSource(t *testing.T) {
	for _, test := range []struct {
		name, source    string
		path            []string
		want, preserved string
	}{
		{"short", "<?php\nreturn ['host' => env('OLD'), 'custom' => call(']')];\n", []string{"host"}, "'host' => 'new'", "'custom' => call(']')"},
		{"long", "<?php\r\nreturn array(\r\n  'nested' => array('host' => 'old'), // custom\r\n);\r\n", []string{"nested", "host"}, "'host' => 'new'", "), // custom\r\n"},
		{"missing", "<?php\nreturn [\n  'custom' => true // untouched\n];\n", []string{"Database", "primary", "host"}, "'host' => 'new'", "'custom' => true, // untouched\n"},
		{"inline missing", "<?php return ['custom' => true];", []string{"host"}, "'host' => 'new'", "'custom' => true,"},
		{"empty", "<?php return array();", []string{"nested", "host"}, "'host' => 'new'", "return array("},
		{"comments", "<?php\n// return ['host' => 'example'];\nreturn ['host' /* note */ => 'old' /* keep */];\n", []string{"host"}, "=> 'new' /* keep */", "// return ['host' => 'example'];"},
		{"escaped key", "<?php return ['a\\'b' => 'old', 'custom' => false];", []string{"a'b"}, "'a\\'b' => 'new'", "'custom' => false"},
		{"variable named return", "<?php $return = 'custom'; return ['host' => 'old'];", []string{"host"}, "'host' => 'new'", "$return = 'custom';"},
	} {
		t.Run(test.name, func(t *testing.T) {
			updated, err := patchPHPArrayValue(test.source, test.path, "'new'")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{test.want, test.preserved} {
				if !strings.Contains(updated, want) {
					t.Fatalf("updated %q missing %q", updated, want)
				}
			}
			if strings.Contains(test.source, "\r\n") && strings.Contains(strings.ReplaceAll(updated, "\r\n", ""), "\n") {
				t.Fatal("introduced LF into CRLF source")
			}
			again, err := patchPHPArrayValue(updated, test.path, "'new'")
			if err != nil || again != updated {
				t.Fatalf("non-idempotent update: %q, %v", again, err)
			}
		})
	}
}

// TestPHPConstantsPatchesPreserveSource distinguishes actual global calls from
// comments, strings, dynamic function calls, methods, and namespaced functions.
func TestPHPConstantsPatchesPreserveSource(t *testing.T) {
	source := "<?php\r\n// define('HOST', 'example');\r\n$example = \"define('HOST', 'string')\";\r\n$define('HOST', 'dynamic');\r\n$obj->define('HOST', 'method');\r\nCustom\\define('HOST', 'namespace');\r\ndefine(\r\n  \"HOST\", /* preserve */ getenv('HOST')\r\n); // end\r\ndefine('BOOTSTRAP', true);\r\nrequire 'bootstrap.php';\r\n?>trailing html"
	values := map[string]string{"HOST": "'new'", "PASSWORD": "'secret'"}
	updated, err := patchPHPConstants(source, values, []string{"HOST", "PASSWORD"}, "BOOTSTRAP")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"// define('HOST', 'example');", "$example = \"define('HOST', 'string')\";", "$define('HOST', 'dynamic');", "$obj->define('HOST', 'method');", "Custom\\define('HOST', 'namespace');", `"HOST", /* preserve */ 'new'`, "// end\r\n", "define( 'PASSWORD', 'secret' );\r\ndefine('BOOTSTRAP', true);", "?>trailing html"} {
		if !strings.Contains(updated, want) {
			t.Fatalf("updated %q missing %q", updated, want)
		}
	}
	again, err := patchPHPConstants(updated, values, []string{"HOST", "PASSWORD"}, "BOOTSTRAP")
	if err != nil || again != updated {
		t.Fatalf("non-idempotent constants: %q, %v", again, err)
	}
	for _, source := range []string{"<?php\nrequire 'bootstrap.php';\n", "<?php\n?>html", "<?php"} {
		updated, err := patchPHPConstants(source, values, []string{"HOST", "PASSWORD"}, "ABSENT")
		if err != nil {
			t.Fatal(err)
		}
		if i := strings.Index(updated, "require"); i >= 0 && strings.Index(updated, "PASSWORD") > i {
			t.Fatalf("inserted after bootstrap: %s", updated)
		}
		if i := strings.Index(updated, "?>"); i >= 0 && strings.Index(updated, "PASSWORD") > i {
			t.Fatalf("inserted outside PHP: %s", updated)
		}
	}
}

// TestPHPIncludeUpdatesManagedBlock verifies existing markers are refreshed
// once, quoted marker examples are ignored, and closed PHP files stay valid.
func TestPHPIncludeUpdatesManagedBlock(t *testing.T) {
	source := "<?php\r\n$custom = true;\r\n?>html"
	updated, err := patchPHPInclude(source, "managed.php", "settings")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(updated, "// <polka:settings>") > strings.Index(updated, "?>") {
		t.Fatal("include inserted outside PHP")
	}
	if !strings.Contains(updated, "$custom = true;\r\n") {
		t.Fatal("custom settings changed")
	}
	again, err := patchPHPInclude(updated, "rotated.php", "settings")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(again, "// <polka:settings>") != 1 || strings.Contains(again, "managed.php") {
		t.Fatalf("managed block not refreshed: %s", again)
	}
	if _, err := patchPHPInclude("<?php\n// <polka:settings>\n", "managed.php", "settings"); err == nil {
		t.Fatal("incomplete marker accepted")
	}
}

// TestPHPIncludeFollowsAppendedSettings keeps managed credentials effective
// when an installer appends configuration after an existing include block.
func TestPHPIncludeFollowsAppendedSettings(t *testing.T) {
	for _, test := range []struct {
		name, eol, suffix string
	}{
		{"LF", "\n", ""},
		{"CRLF", "\r\n", ""},
		{"closing tag", "\n", "?>trailing html"},
		{"reopened PHP", "\n", "?>preserved html<?php\n$extra = true;\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, err := patchPHPInclude("<?php"+test.eol, "old.php", "settings")
			if err != nil {
				t.Fatal(err)
			}
			assignment := "$databases['default']['default'] = ['password' => 'stale'];"
			source += assignment + test.eol + "// Installer settings must survive." + test.eol + test.suffix
			updated, err := patchPHPInclude(source, "managed.php", "settings")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Index(updated, "// <polka:settings>") < strings.Index(updated, assignment) {
				t.Fatalf("managed include precedes installer credentials: %s", updated)
			}
			for _, preserved := range []string{assignment, "// Installer settings must survive.", test.suffix} {
				if !strings.Contains(updated, preserved) {
					t.Fatalf("updated source lost %q: %s", preserved, updated)
				}
			}
			if strings.Contains(updated, "old.php") || strings.Count(updated, "// <polka:settings>") != 1 {
				t.Fatalf("managed include was duplicated or not refreshed: %s", updated)
			}
			if test.eol == "\r\n" && strings.Contains(strings.ReplaceAll(updated, "\r\n", ""), "\n") {
				t.Fatal("introduced LF into CRLF source")
			}
			again, err := patchPHPInclude(updated, "managed.php", "settings")
			if err != nil || again != updated {
				t.Fatalf("non-idempotent include: %q, %v", again, err)
			}
		})
	}
}

// TestUnsafePHPFilesRemainUntouched verifies failures from the public hook,
// including computed containers and malformed defines, before target writes.
func TestUnsafePHPFilesRemainUntouched(t *testing.T) {
	for _, test := range []struct{ id, path, source string }{
		{CakePHP, "config/app_local.php", "<?php return array_merge(['custom' => true], load_config());\n"},
		{CakePHP, "config/app_local.php", "<?php return ['Datasources' => load_config(), 'custom' => true];\n"},
		{CakePHP, "config/app_local.php", "<?php return ['Datasources' => [], 'Datasources' => []];\n"},
		{CakePHP, "config/app_local.php", "<?php return ['custom' => true;\n"},
		{WordPress, "wp-config.php", "<?php define($dynamic_key, 'custom');\n"},
		{WordPress, "wp-config.php", "<?php define('DB_NAME', 'a'); define('DB_NAME', 'b');\n"},
	} {
		t.Run(test.source, func(t *testing.T) {
			plugin := newManifestFrameworkPlugin(test.id)
			dir := t.TempDir()
			filename := filepath.Join(dir, filepath.FromSlash(test.path))
			writePluginTestFile(t, filename, test.source)
			ctx := PostComposerContext{ProjectDir: dir, Environment: plugin.Defaults(), Database: &DatabaseCredentials{Engine: "mariadb", DatabaseName: "demo", User: "user", Password: "secret"}}
			if err := plugin.PostComposer(ctx); err == nil {
				t.Fatal("unsafe PHP shape accepted")
			}
			if data := readPluginTestFile(t, filename); !bytes.Equal(data, []byte(test.source)) {
				t.Fatal("failed hook replaced user config")
			}
			files, err := filepath.Glob(filepath.Join(filepath.Dir(filename), ".polka-config-*"))
			if err != nil || len(files) != 0 {
				t.Fatalf("temporary files leaked: %v, %v", files, err)
			}
		})
	}
}

// TestFrameworkActionPathsAndFailures checks explicit Composer targets, unsafe
// symlink destinations, and fallback read failures without template replacement.
func TestFrameworkActionPathsAndFailures(t *testing.T) {
	t.Run("composer target", func(t *testing.T) {
		dir := t.TempDir()
		writePluginTestFile(t, filepath.Join(dir, "site", "artisan"), "marker")
		writePluginTestFile(t, filepath.Join(dir, "site", ".env.example"), "APP_NAME=Target\n")
		plugin := newManifestFrameworkPlugin(Laravel)
		ctx := PostComposerContext{ProjectDir: dir, WorkingDir: dir, Args: []string{"create-project", "laravel/laravel", "site"}, Environment: config.Environment{Docroot: "public"}, Database: &DatabaseCredentials{Engine: "mysql", DatabaseName: "demo", User: "user", Password: "secret"}}
		if err := plugin.PostComposer(ctx); err != nil {
			t.Fatal(err)
		}
		if data := readPluginTestFile(t, filepath.Join(dir, "site", ".env")); !bytes.Contains(data, []byte("APP_NAME=Target\n")) {
			t.Fatal("target template was not used")
		}
		if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
			t.Fatalf("wrote outside Composer target: %v", err)
		}
	})
	t.Run("fallback read failure", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "config", "app_local.example.php"), 0o755); err != nil {
			t.Fatal(err)
		}
		plugin := newManifestFrameworkPlugin(CakePHP)
		ctx := PostComposerContext{ProjectDir: dir, Environment: plugin.Defaults(), Database: &DatabaseCredentials{DatabaseName: "demo", User: "user"}}
		if err := plugin.PostComposer(ctx); err == nil {
			t.Fatal("fallback read error ignored")
		}
		if _, err := os.Stat(filepath.Join(dir, "config", "app_local.php")); !os.IsNotExist(err) {
			t.Fatalf("wrote after fallback failure: %v", err)
		}
	})
	t.Run("symlink escape", func(t *testing.T) {
		base, outside := t.TempDir(), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(base, "linked")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := frameworkContainedPath(base, "linked/config.php"); err == nil {
			t.Fatal("symlink escape accepted")
		}
	})
	t.Run("contained symlink", func(t *testing.T) {
		base := t.TempDir()
		original := filepath.Join(base, "actual.env")
		writePluginTestFile(t, original, "original")
		link := filepath.Join(base, "linked.env")
		if err := os.Symlink(original, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		target, err := frameworkContainedPath(base, "linked.env")
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFrameworkFile(target, []byte("updated"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(link)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("update replaced the symlink")
		}
		if string(readPluginTestFile(t, original)) != "updated" {
			t.Fatal("link target was not updated")
		}
	})
}

// TestMultiDirectoryPublicRoot verifies declarative app layouts are not limited
// to the single-directory suffixes used by the original built-in frameworks.
func TestMultiDirectoryPublicRoot(t *testing.T) {
	project := t.TempDir()
	app := filepath.Join(project, "site")
	for _, docroot := range []string{"site/web/public", filepath.Join(app, "web", "public")} {
		root := frameworkAppRootFromDocroot(project, docroot, "web/public")
		if root != app {
			t.Fatalf("root(%q) = %q, want %q", docroot, root, app)
		}
		ctx := PostComposerContext{ProjectDir: project, Environment: config.Environment{Docroot: docroot}}
		if got := frameworkDocrootPath(ctx, root, "web/public"); got != filepath.Join(app, "web", "public") {
			t.Fatalf("docroot(%q) = %q", docroot, got)
		}
	}
	if frameworkPathHasSuffix("site/notpublic", "public") {
		t.Fatal("matched a partial directory name")
	}
}
