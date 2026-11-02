package plugins

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"polka/config"
	"polka/script"
)

// frameworkScriptFixture is a demo manifest whose hooks come from one script.
// %s is the script.hooks list.
const frameworkScriptFixture = `id: demo
defaults: {docroot: public}
database-engines: [mysql, mariadb, postgresql]
app-root: {public-dir: public, markers: [composer.json]}
values:
  driver: {by-engine: {mysql: mysql, mariadb: mysql, postgresql: pgsql}}
runtime-env:
  requires-database: true
  values:
    DEMO_PASSWORD: {from: database.password}
script:
  file: scripts/demo.star
  hooks: [%s]
post-composer:
  requires-database: true
`

// frameworkScriptTestDatabase returns credentials whose secret fields are easy
// to search for in output that must not contain them.
func frameworkScriptTestDatabase() *DatabaseCredentials {
	return &DatabaseCredentials{Engine: "mariadb", Version: "11.8", Host: "127.0.0.1", Port: 3307, DatabaseName: "secret_name", User: "secret_user", Password: "secret_password"}
}

// loadFrameworkScriptFixture builds a plugin from the demo manifest and source.
func loadFrameworkScriptFixture(t *testing.T, hooks, source string) builtinFrameworkPlugin {
	t.Helper()
	files := fstest.MapFS{
		"manifests/scripts/demo.star":       {Data: []byte(source)},
		"manifests/scripts/lib/shared.star": {Data: []byte("KEY = \"FROM_LIBRARY\"\n")},
		"manifests/scripts/other.star":      {Data: []byte("KEY = \"FROM_OTHER\"\n")},
		"manifests/templates/test.php":      {Data: []byte("<?php\nreturn {{value}};\n")},
	}
	m, err := parseFrameworkManifestWithFiles(fmt.Appendf(nil, frameworkScriptFixture, hooks), files)
	if err != nil {
		t.Fatalf("parseFrameworkManifestWithFiles() error = %v", err)
	}
	plugin, err := m.pluginWithFiles(files)
	if err != nil {
		t.Fatalf("pluginWithFiles() error = %v", err)
	}
	return plugin.(builtinFrameworkPlugin)
}

// frameworkScriptContext returns a post-Composer context for a fresh project.
func frameworkScriptContext(t *testing.T) PostComposerContext {
	t.Helper()
	return PostComposerContext{
		ProjectDir:  t.TempDir(),
		Environment: config.Environment{Name: "default", Framework: "demo", Docroot: "public"},
		Database:    frameworkScriptTestDatabase(),
	}
}

// dotenvPlan renders a post_composer source returning one dotenv action.
func dotenvPlan(values string) string {
	return "def post_composer(ctx):\n    return {\"actions\": [{\"type\": \"dotenv\", \"path\": \".env\", \"values\": " + values + "}]}\n"
}

// runDotenvPlan runs a dotenv plan and returns its evaluated literal values.
func runDotenvPlan(t *testing.T, values string) map[string]frameworkValue {
	t.Helper()
	ctx := frameworkScriptContext(t)
	actions, err := frameworkScriptActions(ctx, loadFrameworkScriptFixture(t, "post_composer", dotenvPlan(values)), ctx.Database, ctx.ProjectDir)
	if err != nil {
		t.Fatalf("frameworkScriptActions() error = %v", err)
	}
	return actions[0].Values
}

// TestFrameworkScriptPlanRoundTrip verifies plans survive their trip through
// JSON and the YAML decoder, including values that mean something else in YAML
// and the difference between a string and the scalar it resembles.
func TestFrameworkScriptPlanRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name, expr string
		want       any
	}{
		{"backslash", `"Cake\\Database\\Driver\\Mysql"`, `Cake\Database\Driver\Mysql`},
		{"quotes", `"a'b\"c"`, `a'b"c`},
		{"dollar", `"$HOME ${x}"`, `$HOME ${x}`},
		{"hash", `"a #b"`, `a #b`},
		{"colon", `"key: value"`, `key: value`},
		{"newline", `"line\nbreak"`, "line\nbreak"},
		{"crlf and tab", `"a\r\n\tb"`, "a\r\n\tb"},
		{"placeholder", `"{{value}}"`, `{{value}}`},
		{"flow mapping text", `"{from: database.password}"`, `{from: database.password}`},
		{"flow sequence text", `"[a, b]"`, `[a, b]`},
		{"indicators", `"- !tag &anchor *alias %20 | > ?"`, `- !tag &anchor *alias %20 | > ?`},
		{"non-ASCII", `"héllo ✓ 😀"`, `héllo ✓ 😀`},
		{"surrounding spaces", `"  spaced  "`, `  spaced  `},
		{"empty string", `""`, ``},
		{"tilde string", `"~"`, `~`},
		{"null string", `"null"`, `null`},
		{"none", `None`, nil},
		{"numeric string", `"3306"`, `3306`},
		{"integer", `3306`, uint64(3306)},
		{"negative integer", `-1`, int64(-1)},
		{"float", `1.5`, 1.5},
		{"boolean string", `"true"`, `true`},
		{"boolean", `True`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, ok := runDotenvPlan(t, `{"KEY": `+test.expr+`}`)["KEY"]
			if !ok {
				t.Fatal("plan lost KEY")
			}
			if !reflect.DeepEqual(value.literal, test.want) {
				t.Fatalf("literal = %#v, want %#v", value.literal, test.want)
			}
		})
	}
}

// TestFrameworkScriptPlanKeepsBindings verifies a plan decodes the structured
// binding forms rather than treating them as literals.
func TestFrameworkScriptPlanKeepsBindings(t *testing.T) {
	values := runDotenvPlan(t, `{
        "FROM": {"from": "database.password"},
        "DEFAULT": {"from": "database.version", "default": "11.8"},
        "ENGINE": {"by-engine": {"mysql": "a", "mariadb": "b", "postgresql": "c"}},
        "FORMAT": {"format": {"kind": "host-port", "host": {"from": "database.host"}, "port": {"from": "database.port"}, "omit-port": "3306"}},
    }`)
	if values["FROM"].From != "database.password" {
		t.Fatalf("FROM = %#v", values["FROM"])
	}
	if values["DEFAULT"].Default == nil || values["DEFAULT"].Default.literal != "11.8" {
		t.Fatalf("DEFAULT = %#v", values["DEFAULT"])
	}
	if values["ENGINE"].ByEngine["postgresql"].literal != "c" {
		t.Fatalf("ENGINE = %#v", values["ENGINE"])
	}
	if values["FORMAT"].Format == nil || values["FORMAT"].Format.OmitPort != "3306" {
		t.Fatalf("FORMAT = %#v", values["FORMAT"])
	}
}

// TestFrameworkScriptManifestValidation checks script blocks fail at load time,
// before anything compiles.
func TestFrameworkScriptManifestValidation(t *testing.T) {
	base := "id: demo\ndefaults: {docroot: public}\napp-root: {public-dir: public}\n"
	for _, test := range []struct{ name, tail, want string }{
		{"no file", "script: {hooks: [post_composer]}", "requires file"},
		{"no hooks", "script: {file: scripts/demo.star}", "at least one hook"},
		{"unknown hook", "script: {file: scripts/demo.star, hooks: [runtime_env]}", "unsupported hook"},
		{"hook is case sensitive", "script: {file: scripts/demo.star, hooks: [Post_Composer]}", "unsupported hook"},
		{"duplicate hook", "script: {file: scripts/demo.star, hooks: [post_composer, post_composer]}", "duplicates hook"},
		{"bad extension", "script: {file: scripts/demo.py, hooks: [post_composer]}", ".star"},
		{"missing file", "script: {file: scripts/missing.star, hooks: [post_composer]}", "script.file"},
		{"escaping file", "script: {file: ../demo.star, hooks: [post_composer]}", "relative path"},
		{"absolute file", "script: {file: /demo.star, hooks: [post_composer]}", "relative path"},
		{"backslash file", "script: {file: 'scripts\\demo.star', hooks: [post_composer]}", "relative path"},
		{"vars rejected", "script: {file: scripts/demo.star, hooks: [post_composer], vars: {a: b}}", "vars"},
		{"hook with actions", "script: {file: scripts/demo.star, hooks: [post_composer]}\npost-composer: {actions: [{type: dotenv, path: .env, values: {X: y}}]}", "cannot be combined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := fstest.MapFS{"manifests/scripts/demo.star": {Data: []byte("def post_composer(ctx):\n    return {}\n")}, "manifests/scripts/demo.py": {Data: []byte("")}}
			_, err := parseFrameworkManifestWithFiles([]byte(base+test.tail+"\n"), files)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	t.Run("hook requires public dir", func(t *testing.T) {
		files := fstest.MapFS{"manifests/scripts/demo.star": {Data: []byte("")}}
		manifest := "id: demo\ndefaults: {docroot: public}\nscript: {file: scripts/demo.star, hooks: [post_composer]}\n"
		_, err := parseFrameworkManifestWithFiles([]byte(manifest), files)
		if err == nil || !strings.Contains(err.Error(), "public-dir") {
			t.Fatalf("error = %v, want public-dir", err)
		}
	})

	t.Run("nginx hook does not require public dir", func(t *testing.T) {
		files := fstest.MapFS{"manifests/scripts/demo.star": {Data: []byte("")}}
		manifest := "id: demo\ndefaults: {docroot: public}\nscript: {file: scripts/demo.star, hooks: [nginx_config]}\n"
		if _, err := parseFrameworkManifestWithFiles([]byte(manifest), files); err != nil {
			t.Fatalf("error = %v", err)
		}
	})
}

// TestFrameworkScriptBinding checks a script against its declared hooks and the
// modules frameworks may use.
func TestFrameworkScriptBinding(t *testing.T) {
	hook := "def post_composer(ctx):\n    return {}\n"
	for _, test := range []struct{ name, hooks, source, want string }{
		{"valid", "post_composer", hook, ""},
		{"helpers and constants", "post_composer", "LIMIT = 3\ndef _helper():\n    pass\n" + hook, ""},
		{"version module", "post_composer", "LATEST = version.compare(\"1.2\", \"1.10\")\n" + hook, ""},
		{"generic modules", "post_composer", "A = re.search(\"a\", \"a\")\nB = json.encode({})\nC = html.unescape(\"&amp;\")\nD = url.resolve(\"http://a/\", \"b\")\n" + hook, ""},
		{"library load", "post_composer", "load(\"shared.star\", \"KEY\")\n" + hook, ""},
		{"load outside library", "post_composer", "load(\"../other.star\", \"KEY\")\n" + hook, "must be a .star path"},
		{"hook missing", "post_composer", "def _helper():\n    pass\n", `declares hook "post_composer" but does not define it`},
		{"undeclared hook", "post_composer", hook + "def nginx_config(ctx):\n    return None\n", `defines hook "nginx_config" that is not declared`},
		{"public helper", "post_composer", hook + "def helper():\n    pass\n", `unsupported public function "helper"`},
		{"tool hook name", "post_composer", hook + "def post_install(ctx):\n    pass\n", `unsupported public function "post_install"`},
		{"wrong arity", "post_composer", "def post_composer():\n    return {}\n", "exactly one parameter"},
		{"no network", "post_composer", "def post_composer(ctx):\n    return http.get(\"http://example.com\")\n", "undefined: http"},
		{"no secret generation", "post_composer", "def post_composer(ctx):\n    return crypto.random_base64(8)\n", "undefined: crypto"},
		{"no host paths", "post_composer", "def post_composer(ctx):\n    return path.join(\"a\")\n", "undefined: path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := loadFrameworkScriptFixture(t, test.hooks, test.source).scriptModule()
			if test.want == "" {
				if err != nil {
					t.Fatalf("scriptModule() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("scriptModule() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestFrameworkScriptPlanApplies runs a plan through the existing action code,
// resolving bindings the script could not read itself.
func TestFrameworkScriptPlanApplies(t *testing.T) {
	ctx := frameworkScriptContext(t)
	source := "load(\"shared.star\", \"KEY\")\n\n" + dotenvPlan(`{
        KEY: "from script",
        "DEMO_DRIVER": {"from": "values.driver"},
        "DEMO_PASSWORD": {"from": "runtime-env.DEMO_PASSWORD"},
        "DEMO_USER": {"from": "database.user"},
    }`)
	if err := loadFrameworkScriptFixture(t, "post_composer", source).PostComposer(ctx); err != nil {
		t.Fatalf("PostComposer() error = %v", err)
	}
	got := string(readPluginTestFile(t, filepath.Join(ctx.ProjectDir, ".env")))
	for _, want := range []string{`FROM_LIBRARY="from script"`, "DEMO_DRIVER=mysql", "DEMO_PASSWORD=secret_password", "DEMO_USER=secret_user"} {
		if !strings.Contains(got, want) {
			t.Fatalf(".env = %q, want %q", got, want)
		}
	}
}

// TestFrameworkScriptEmptyPlans verifies a hook may decide there is nothing to do.
func TestFrameworkScriptEmptyPlans(t *testing.T) {
	for _, plan := range []string{`{}`, `{"actions": []}`, `{"actions": None}`} {
		ctx := frameworkScriptContext(t)
		source := "def post_composer(ctx):\n    return " + plan + "\n"
		if err := loadFrameworkScriptFixture(t, "post_composer", source).PostComposer(ctx); err != nil {
			t.Fatalf("PostComposer(%s) error = %v", plan, err)
		}
		entries, err := os.ReadDir(ctx.ProjectDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("PostComposer(%s) wrote %v", plan, entries)
		}
	}
}

// TestFrameworkScriptRejectsInvalidPlans checks each plan is refused as a whole.
// A valid action precedes the invalid one, and must not be applied.
func TestFrameworkScriptRejectsInvalidPlans(t *testing.T) {
	valid := `{"type": "dotenv", "path": "first.env", "values": {"A": "b"}}`
	for _, test := range []struct{ name, result, want string }{
		{"none", `None`, "must be a dict"},
		{"list", `[` + valid + `]`, "must be a dict"},
		{"unknown key", `{"actions": [` + valid + `], "write": {}}`, "write"},
		{"function value", `{"actions": [` + valid + `], "helper": _helper}`, "cannot encode"},
		{"non-string key", `{1: "a"}`, "dict has int key"},
		{"unknown action key", `{"actions": [` + valid + `, {"type": "dotenv", "path": ".env", "values": {"A": "b"}, "mode": "0777"}]}`, "mode"},
		{"unknown action", `{"actions": [` + valid + `, {"type": "shell", "path": "x"}]}`, "unsupported action"},
		{"snake case key", `{"actions": [` + valid + `, {"type": "php-array", "path": "x.php", "fallback_template_file": "test.php"}]}`, "fallback_template_file"},
		{"escaping path", `{"actions": [` + valid + `, {"type": "dotenv", "path": "../x", "values": {"A": "b"}}]}`, "relative path"},
		{"unknown reference", `{"actions": [` + valid + `, {"type": "dotenv", "path": ".env", "values": {"A": {"from": "values.missing"}}}]}`, "unknown reference"},
		{"unknown secret", `{"actions": [` + valid + `, {"type": "dotenv", "path": ".env", "values": {"A": {"from": "database.token"}}}]}`, "unknown reference"},
		{"uncovered engine", `{"actions": [` + valid + `, {"type": "dotenv", "path": ".env", "values": {"A": {"by-engine": {"mysql": "b"}}}}]}`, "missing declared"},
		{"conflicting binding", `{"actions": [` + valid + `, {"type": "dotenv", "path": ".env", "values": {"A": {"from": "database.host", "by-engine": {}}}}]}`, "exactly one"},
		{"non-scalar value", `{"actions": [` + valid + `, {"type": "dotenv", "path": ".env", "values": {"A": ["b"]}}]}`, "scalar"},
		{"bad dotenv name", `{"actions": [` + valid + `, {"type": "dotenv", "path": ".env", "values": {"BAD=KEY": "b"}}]}`, "invalid dotenv"},
		{"missing template", `{"actions": [` + valid + `, {"type": "write-file", "path": "x.php", "template-file": "missing.php", "encoding": "text"}]}`, "read template"},
		{"unknown placeholder", `{"actions": [` + valid + `, {"type": "write-file", "path": "x.php", "template-file": "test.php", "encoding": "text"}]}`, "unknown template placeholder"},
		{"unknown order", `{"actions": [` + valid + `, {"type": "dotenv", "path": ".env", "values": {"A": "b"}, "order": ["C"]}]}`, "unknown or duplicate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := frameworkScriptContext(t)
			source := "def _helper():\n    pass\n\ndef post_composer(ctx):\n    return " + test.result + "\n"
			err := loadFrameworkScriptFixture(t, "post_composer", source).PostComposer(ctx)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PostComposer() error = %v, want %q", err, test.want)
			}
			if _, statErr := os.Stat(filepath.Join(ctx.ProjectDir, "first.env")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("first action ran despite the invalid plan: stat error = %v", statErr)
			}
		})
	}
}

// TestFrameworkScriptContextHasNoSecrets protects the rule that credentials
// never enter Starlark: a script printing its whole context learns none of them.
func TestFrameworkScriptContextHasNoSecrets(t *testing.T) {
	ctx := frameworkScriptContext(t)
	source := "def post_composer(ctx):\n    fail(str(ctx) + str(dir(ctx)) + str(dir(ctx.database)))\n"
	err := loadFrameworkScriptFixture(t, "post_composer", source).PostComposer(ctx)
	if err == nil {
		t.Fatal("PostComposer() error = nil, want the context in a failure")
	}
	for _, secret := range []string{"secret_password", "secret_user", "secret_name", "password", "user"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("hook context exposes %q: %s", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "mariadb") {
		t.Fatalf("hook context = %s, want the database engine", err)
	}
}

// TestFrameworkScriptContextMembers checks the values a post_composer hook sees.
func TestFrameworkScriptContextMembers(t *testing.T) {
	ctx := frameworkScriptContext(t)
	ctx.Environment.Tools = config.ToolsConfig{"php": "8.4"}
	source := dotenvPlan(`{
        "FRAMEWORK": ctx.framework,
        "ENVIRONMENT": ctx.environment,
        "OS": ctx.os,
        "ARCH": ctx.arch,
        "PLATFORM": ctx.platform,
        "ENGINE": ctx.database.engine,
        "VERSION": ctx.database.version,
        "HOST": ctx.database.host,
        "PORT": ctx.database.port,
        "PHP": ctx.tool_version("php"),
        "MISSING": ctx.tool_version("redis"),
    }`)
	actions, err := frameworkScriptActions(ctx, loadFrameworkScriptFixture(t, "post_composer", source), ctx.Database, ctx.ProjectDir)
	if err != nil {
		t.Fatalf("frameworkScriptActions() error = %v", err)
	}
	want := map[string]any{
		"FRAMEWORK": "demo", "ENVIRONMENT": "default", "OS": runtime.GOOS, "ARCH": runtime.GOARCH,
		"PLATFORM": runtime.GOOS + "-" + runtime.GOARCH, "ENGINE": "mariadb", "VERSION": "11.8",
		"HOST": "127.0.0.1", "PORT": uint64(3307), "PHP": "8.4", "MISSING": "",
	}
	for key, value := range want {
		if got := actions[0].Values[key].literal; !reflect.DeepEqual(got, value) {
			t.Errorf("%s = %#v, want %#v", key, got, value)
		}
	}
}

// TestFrameworkScriptDatabaseGuard verifies a block guarded by requires-database
// is skipped without compiling, and that an unguarded hook sees None.
func TestFrameworkScriptDatabaseGuard(t *testing.T) {
	t.Run("guarded", func(t *testing.T) {
		plugin := loadFrameworkScriptFixture(t, "post_composer", "def post_composer(ctx):\n    fail(\"must not run\")\n")
		plugin.loadScript = func() (*script.Module, error) {
			t.Fatal("script compiled without database credentials")
			return nil, nil
		}
		ctx := frameworkScriptContext(t)
		ctx.Database = nil
		if err := plugin.PostComposer(ctx); err != nil {
			t.Fatalf("PostComposer() error = %v", err)
		}
	})

	t.Run("unguarded", func(t *testing.T) {
		plugin := loadFrameworkScriptFixture(t, "post_composer", "def post_composer(ctx):\n    if ctx.database != None:\n        fail(\"want no database\")\n    return {}\n")
		plugin.manifest.PostComposer.RequiresDatabase = false
		ctx := frameworkScriptContext(t)
		ctx.Database = nil
		if err := plugin.PostComposer(ctx); err != nil {
			t.Fatalf("PostComposer() error = %v", err)
		}
	})
}

// TestFrameworkScriptReadText checks ctx.read_text stays inside its base and
// reports absence as None.
func TestFrameworkScriptReadText(t *testing.T) {
	readSource := func(call string) string {
		return "def post_composer(ctx):\n    fail(\"read: \" + str(" + call + "))\n"
	}
	ctx := frameworkScriptContext(t)
	writePluginTestFile(t, filepath.Join(ctx.ProjectDir, "composer.lock"), "app contents")
	writePluginTestFile(t, filepath.Join(ctx.ProjectDir, "public", "index.php"), "docroot contents")

	for _, test := range []struct{ name, call, want string }{
		{"app root by default", `ctx.read_text("composer.lock")`, "read: app contents"},
		{"explicit app root", `ctx.read_text("composer.lock", base="app-root")`, "read: app contents"},
		{"docroot", `ctx.read_text("index.php", base="docroot")`, "read: docroot contents"},
		{"missing file", `ctx.read_text("missing.txt")`, "read: None"},
		{"not in docroot", `ctx.read_text("composer.lock", base="docroot")`, "read: None"},
		{"unknown base", `ctx.read_text("composer.lock", base="root")`, "unsupported base"},
		{"escaping path", `ctx.read_text("../secret")`, "relative path"},
		{"rooted path", `ctx.read_text("/etc/passwd")`, "relative path"},
		{"backslash path", `ctx.read_text("a\\b")`, "relative path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := loadFrameworkScriptFixture(t, "post_composer", readSource(test.call)).PostComposer(ctx)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PostComposer() error = %v, want %q", err, test.want)
			}
		})
	}

	t.Run("missing docroot", func(t *testing.T) {
		ctx := frameworkScriptContext(t)
		err := loadFrameworkScriptFixture(t, "post_composer", readSource(`ctx.read_text("index.php", base="docroot")`)).PostComposer(ctx)
		if err == nil || !strings.Contains(err.Error(), "read: None") {
			t.Fatalf("PostComposer() error = %v, want None", err)
		}
	})

	t.Run("oversize file", func(t *testing.T) {
		ctx := frameworkScriptContext(t)
		if err := os.WriteFile(filepath.Join(ctx.ProjectDir, "huge.txt"), make([]byte, script.MaxReadTextBytes+1), 0o644); err != nil {
			t.Fatal(err)
		}
		err := loadFrameworkScriptFixture(t, "post_composer", readSource(`ctx.read_text("huge.txt")`)).PostComposer(ctx)
		var failErr *script.FailError
		if !errors.As(err, &failErr) || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("PostComposer() error = %v, want a user-facing size failure", err)
		}
	})

	t.Run("link escape", func(t *testing.T) {
		ctx := frameworkScriptContext(t)
		outside := t.TempDir()
		writePluginTestFile(t, filepath.Join(outside, "secret.txt"), "outside contents")
		linkOutsideDirectory(t, outside, filepath.Join(ctx.ProjectDir, "linked"))
		err := loadFrameworkScriptFixture(t, "post_composer", readSource(`ctx.read_text("linked/secret.txt")`)).PostComposer(ctx)
		if err == nil || strings.Contains(err.Error(), "outside contents") || strings.Contains(err.Error(), "read: ") {
			t.Fatalf("PostComposer() error = %v, want the link refused", err)
		}
	})
}

// linkOutsideDirectory links link to target. Windows only allows symlinks with
// a privilege most accounts lack, so it falls back to a directory junction,
// which escapes a directory the same way.
func linkOutsideDirectory(t *testing.T, target, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if err == nil {
		return
	}
	if runtime.GOOS != "windows" {
		t.Skipf("cannot create symlink: %v", err)
	}
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("cannot create junction: %v: %s", err, output)
	}
}

// TestBuiltinFrameworkScriptsLoad compiles every embedded framework script and
// rejects scripts no manifest references, because a script only runs when its
// hook does.
func TestBuiltinFrameworkScriptsLoad(t *testing.T) {
	referenced := map[string]bool{}
	for _, plugin := range DefaultFrameworkPlugins() {
		builtin, ok := plugin.(builtinFrameworkPlugin)
		if !ok {
			t.Fatalf("framework %q is a %T", plugin.ID(), plugin)
		}
		if !builtin.manifest.Script.hasHook(frameworkHookPostComposer) {
			t.Fatalf("framework %q declares no post_composer hook", plugin.ID())
		}
		if _, err := builtin.scriptModule(); err != nil {
			t.Fatalf("framework %q script error = %v", plugin.ID(), err)
		}
		referenced[frameworkManifestDir+"/"+builtin.manifest.Script.File] = true
	}

	scripts, err := fs.Glob(builtinFrameworkManifestFiles, frameworkManifestDir+"/scripts/*.star")
	if err != nil {
		t.Fatalf("fs.Glob(scripts) error = %v", err)
	}
	if len(scripts) == 0 {
		t.Fatal("no embedded framework scripts")
	}
	for _, file := range scripts {
		if !referenced[file] {
			t.Fatalf("embedded script %s is not referenced by any manifest", file)
		}
	}
}

// TestFrameworkDispatchPathDoesNotCompileScript protects command dispatch
// latency. Shims resolve the runtime environment and validate config on every
// call, so the plugin methods those steps reach must not parse Starlark.
// PostComposer is exempt, because it only follows Composer's install, update,
// and create-project commands.
func TestFrameworkDispatchPathDoesNotCompileScript(t *testing.T) {
	for _, plugin := range DefaultFrameworkPlugins() {
		builtin, ok := plugin.(builtinFrameworkPlugin)
		if !ok {
			t.Fatalf("framework %q is a %T", plugin.ID(), plugin)
		}
		// A plugin without a script would pass without testing anything.
		if builtin.loadScript == nil {
			t.Fatalf("framework %q has no script loader", plugin.ID())
		}
		builtin.loadScript = func() (*script.Module, error) {
			t.Fatalf("framework %q script compiled on the dispatch path", plugin.ID())
			return nil, nil
		}

		environment := builtin.Defaults()
		environment.Database = &config.DatabaseConfig{Engine: "mariadb"}
		if err := builtin.ValidateEnvironment(environment); err != nil {
			t.Fatalf("ValidateEnvironment(%s) error = %v", plugin.ID(), err)
		}
		values := runtimeEnvForTest(t, builtin, RuntimeEnvContext{Environment: environment, Database: frameworkScriptTestDatabase()})
		if len(values) == 0 {
			t.Fatalf("RuntimeEnv(%s) returned no values", plugin.ID())
		}
		builtin.Detection()
		builtin.PHPExtensions()
		builtin.OPcacheConfig()
		// Serving a framework that declares no nginx_config must not compile either.
		if _, handled, err := builtin.NginxConfig(NginxConfigContext{}); handled || err != nil {
			t.Fatalf("NginxConfig(%s) handled = %v, error = %v", plugin.ID(), handled, err)
		}
	}
}

// TestFrameworkScriptErrors separates deliberate failures from script bugs.
func TestFrameworkScriptErrors(t *testing.T) {
	t.Run("fail is verbatim", func(t *testing.T) {
		err := loadFrameworkScriptFixture(t, "post_composer", "def post_composer(ctx):\n    fail(\"unsupported layout\")\n").PostComposer(frameworkScriptContext(t))
		var failErr *script.FailError
		if !errors.As(err, &failErr) || err.Error() != "unsupported layout" {
			t.Fatalf("PostComposer() error = %#v, want the message verbatim", err)
		}
	})

	t.Run("bug has a backtrace", func(t *testing.T) {
		err := loadFrameworkScriptFixture(t, "post_composer", "def post_composer(ctx):\n    return {}[\"missing\"]\n").PostComposer(frameworkScriptContext(t))
		if err == nil {
			t.Fatal("PostComposer() error = nil")
		}
		for _, want := range []string{`framework "demo" script scripts/demo.star`, "Traceback", "post_composer"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("PostComposer() error = %v, want %q", err, want)
			}
		}
	})

	t.Run("invalid plan names the script", func(t *testing.T) {
		err := loadFrameworkScriptFixture(t, "post_composer", "def post_composer(ctx):\n    return None\n").PostComposer(frameworkScriptContext(t))
		if err == nil || !strings.Contains(err.Error(), `framework "demo" script scripts/demo.star post_composer: result must be a dict`) {
			t.Fatalf("PostComposer() error = %v", err)
		}
	})

	t.Run("step budget is enforced", func(t *testing.T) {
		source := "def post_composer(ctx):\n    total = 0\n    for _ in range(100000000):\n        total += 1\n    return {}\n"
		err := loadFrameworkScriptFixture(t, "post_composer", source).PostComposer(frameworkScriptContext(t))
		if err == nil || !strings.Contains(err.Error(), "too many steps") {
			t.Fatalf("PostComposer() error = %v, want a step budget failure", err)
		}
	})
}

// TestFrameworkScriptNginxConfig covers the nginx_config result contract.
func TestFrameworkScriptNginxConfig(t *testing.T) {
	context := NginxConfigContext{
		Environment: config.Environment{Name: "default"}, Host: "demo.test", Port: 8443, Docroot: "/app/public",
		IndexNames: "index.php", TryFilesFallback: "/index.php?$query_string", FastCGIIndex: "index.php",
		BackendAddress: "127.0.0.1:9000", TLSEnabled: true, TLSCertificatePath: "/tls/cert.pem", TLSCertificateKeyPath: "/tls/key.pem",
		DefaultConfig: "generic config",
	}
	for _, test := range []struct {
		name, body  string
		wantHandled bool
		wantConfig  string
		wantErr     string
	}{
		{name: "none keeps generic", body: "return None"},
		{name: "replacement", body: `return "server {}"`, wantHandled: true, wantConfig: "server {}"},
		{name: "edits default config", body: `return ctx.default_config.replace("generic", "custom")`, wantHandled: true, wantConfig: "custom config"},
		{
			name: "context members", body: `return " ".join([ctx.framework, ctx.environment, ctx.host, str(ctx.port), ctx.docroot, ctx.index_names, ctx.try_files_fallback, ctx.fastcgi_index, ctx.backend_address, ctx.tls.certificate, ctx.tls.certificate_key])`, wantHandled: true,
			wantConfig: "demo default demo.test 8443 /app/public index.php /index.php?$query_string index.php 127.0.0.1:9000 /tls/cert.pem /tls/key.pem",
		},
		{name: "wrong type", body: "return 1", wantErr: "must be a string or None, got int"},
		{name: "empty", body: `return "  "`, wantErr: "cannot be empty"},
		{name: "fail", body: `fail("no nginx support")`, wantErr: "no nginx support"},
		{name: "bug", body: "return ctx.missing", wantErr: "Traceback"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plugin := loadFrameworkScriptFixture(t, "nginx_config", "def nginx_config(ctx):\n    "+test.body+"\n")
			result, handled, err := plugin.NginxConfig(context)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("NginxConfig() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NginxConfig() error = %v", err)
			}
			if handled != test.wantHandled || string(result.Config) != test.wantConfig {
				t.Fatalf("NginxConfig() = %q, %v, want %q, %v", result.Config, handled, test.wantConfig, test.wantHandled)
			}
		})
	}

	t.Run("tls is none when disabled", func(t *testing.T) {
		plugin := loadFrameworkScriptFixture(t, "nginx_config", "def nginx_config(ctx):\n    return str(ctx.tls)\n")
		result, _, err := plugin.NginxConfig(NginxConfigContext{})
		if err != nil || string(result.Config) != "None" {
			t.Fatalf("NginxConfig() = %q, %v, want None", result.Config, err)
		}
	})

	t.Run("undeclared hook does not compile", func(t *testing.T) {
		plugin := loadFrameworkScriptFixture(t, "post_composer", "def post_composer(ctx):\n    return {}\n")
		plugin.loadScript = func() (*script.Module, error) {
			t.Fatal("script compiled for an undeclared hook")
			return nil, nil
		}
		if _, handled, err := plugin.NginxConfig(context); handled || err != nil {
			t.Fatalf("NginxConfig() handled = %v, error = %v", handled, err)
		}
	})
}

// TestFrameworkScriptCompilesOnce verifies repeated hook calls share one module.
func TestFrameworkScriptCompilesOnce(t *testing.T) {
	plugin := loadFrameworkScriptFixture(t, "post_composer", "def post_composer(ctx):\n    return {}\n")
	first, err := plugin.scriptModule()
	if err != nil {
		t.Fatal(err)
	}
	second, err := plugin.scriptModule()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("scriptModule() compiled the script twice")
	}
}
