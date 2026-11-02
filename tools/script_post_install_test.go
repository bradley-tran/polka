package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"polka/config"
	"polka/script"
)

// postInstallFixture is a demo tool whose post_install hook is scripts/demo.star.
const postInstallFixture = `id: demo
install-candidates:
  all:
    - demo
script:
  file: scripts/demo.star
  hooks: [post_install]
`

// newPostInstallInstall creates an install directory holding the tool's target
// binary and returns its path with an InstallContext pointing at it.
func newPostInstallInstall(t *testing.T) (string, InstallContext) {
	t.Helper()
	installDir := t.TempDir()
	target := filepath.Join(installDir, "demo")
	if err := os.WriteFile(target, []byte("demo"), 0o755); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}

	return installDir, InstallContext{
		RootDir: t.TempDir(),
		Result:  InstallResult{Tool: "demo", Version: "1.0", TargetPath: target},
	}
}

// runPostInstallScript runs source as a post_install hook through the plugin.
func runPostInstallScript(t *testing.T, source string, ctx InstallContext) error {
	t.Helper()
	plugin, err := loadScriptFixture(t, postInstallFixture, source).toPlugin(pluginHooks{})
	if err != nil {
		t.Fatalf("toPlugin() error = %v", err)
	}

	return plugin.PostInstall(ctx)
}

// installFileNames lists the files under dir, slash-separated and relative.
func installFileNames(t *testing.T, dir string) []string {
	t.Helper()
	names := []string{}
	err := filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if relative != "." {
			names = append(names, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir() error = %v", err)
	}

	return names
}

// TestPostInstallAppliesPlan verifies directories and files land in the install
// directory, including parents a written file needs.
func TestPostInstallAppliesPlan(t *testing.T) {
	installDir, ctx := newPostInstallInstall(t)
	source := "def post_install(ctx):\n    return {\"mkdir\": [\"cache/data\"], \"write\": {\"conf/app.ini\": \"version=\" + ctx.version + \"\\n\"}}\n"
	if err := runPostInstallScript(t, source, ctx); err != nil {
		t.Fatalf("PostInstall() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(installDir, "conf", "app.ini"))
	if err != nil || string(data) != "version=1.0\n" {
		t.Fatalf("conf/app.ini = %q, %v", data, err)
	}
	if info, err := os.Stat(filepath.Join(installDir, "cache", "data")); err != nil || !info.IsDir() {
		t.Fatalf("Stat(cache/data) = %v, %v, want directory", info, err)
	}
}

// TestPostInstallRejectsBadPlans verifies an invalid plan fails and writes
// nothing, even when the invalid part comes after a valid one.
func TestPostInstallRejectsBadPlans(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "traversal", body: `{"write": {"ok.txt": "x", "../evil.txt": "x"}}`, want: "must be a clean slash-separated path"},
		{name: "nested traversal", body: `{"write": {"ok.txt": "x", "a/../../evil.txt": "x"}}`, want: "must be a clean slash-separated path"},
		{name: "absolute", body: `{"write": {"ok.txt": "x", "/evil.txt": "x"}}`, want: "must be a clean slash-separated path"},
		{name: "backslash", body: `{"write": {"ok.txt": "x", "..\\evil.txt": "x"}}`, want: "must be a clean slash-separated path"},
		{name: "install root", body: `{"mkdir": ["."], "write": {"ok.txt": "x"}}`, want: "must be a clean slash-separated path"},
		{name: "mkdir traversal", body: `{"write": {"ok.txt": "x"}, "mkdir": ["../evil"]}`, want: "must be a clean slash-separated path"},
		{name: "unknown key", body: `{"write": {"ok.txt": "x"}, "delete": ["demo"]}`, want: "unsupported key"},
		{name: "not a dict", body: `["ok.txt"]`, want: "must be a dict"},
		{name: "none", body: `None`, want: "must be a dict"},
		{name: "write not a dict", body: `{"write": ["ok.txt"]}`, want: "write must be a dict"},
		{name: "content not a string", body: `{"write": {"ok.txt": 1}}`, want: "must be a string"},
		{name: "mkdir not a list", body: `{"mkdir": "tmp"}`, want: "mkdir must be a list"},
		{name: "mkdir entry not a string", body: `{"mkdir": [1]}`, want: "must be a string"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installDir, ctx := newPostInstallInstall(t)
			err := runPostInstallScript(t, "def post_install(ctx):\n    return "+test.body+"\n", ctx)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PostInstall() error = %v, want containing %q", err, test.want)
			}
			if names := installFileNames(t, installDir); len(names) != 1 || names[0] != "demo" {
				t.Fatalf("install directory = %#v, want untouched", names)
			}
		})
	}
}

// linkDirectory links link to target. Creating a symlink on Windows needs a
// privilege most accounts lack, so it falls back to a junction, which is the
// escape a Windows install is more likely to contain anyway.
func linkDirectory(t *testing.T, target, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if err == nil {
		return
	}
	if runtime.GOOS != "windows" {
		t.Fatalf("Symlink() error = %v", err)
	}
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J error = %v: %s", err, output)
	}
}

// TestPostInstallRefusesSymlinkEscape verifies a symlink in the install cannot
// redirect a read or write outside it, and that a refused write leaves the
// plan's other files unwritten.
func TestPostInstallRefusesSymlinkEscape(t *testing.T) {
	installDir, ctx := newPostInstallInstall(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile(secret) error = %v", err)
	}
	linkDirectory(t, outside, filepath.Join(installDir, "link"))

	err := runPostInstallScript(t, "def post_install(ctx):\n    return {\"write\": {\"a.txt\": \"x\", \"link/evil.txt\": \"x\"}}\n", ctx)
	if err == nil {
		t.Fatal("PostInstall(write through link) error = nil, want refusal")
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.txt")); !os.IsNotExist(err) {
		t.Fatalf("Stat(outside/evil.txt) error = %v, want not written", err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("Stat(a.txt) error = %v, want the plan to write nothing", err)
	}

	err = runPostInstallScript(t, "def post_install(ctx):\n    return {\"write\": {\"copy.txt\": ctx.read_text(\"link/secret.txt\")}}\n", ctx)
	if err == nil {
		t.Fatal("PostInstall(read through link) error = nil, want refusal")
	}
	if _, err := os.Stat(filepath.Join(installDir, "copy.txt")); !os.IsNotExist(err) {
		t.Fatalf("Stat(copy.txt) error = %v, want not written", err)
	}
}

// TestPostInstallReadText covers reading install files: contents, missing
// files as None, and the path rules shared with writes.
func TestPostInstallReadText(t *testing.T) {
	installDir, ctx := newPostInstallInstall(t)
	if err := os.WriteFile(filepath.Join(installDir, "input.txt"), []byte("hello\r\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(input) error = %v", err)
	}
	source := "def post_install(ctx):\n    missing = ctx.read_text(\"nope.txt\")\n    return {\"write\": {\"out.txt\": ctx.read_text(\"input.txt\") + str(missing)}}\n"
	if err := runPostInstallScript(t, source, ctx); err != nil {
		t.Fatalf("PostInstall() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(installDir, "out.txt"))
	if err != nil || string(data) != "hello\r\nNone" {
		t.Fatalf("out.txt = %q, %v, want bytes preserved", data, err)
	}

	err = runPostInstallScript(t, "def post_install(ctx):\n    ctx.read_text(\"../outside.txt\")\n    return {}\n", ctx)
	if err == nil || !strings.Contains(err.Error(), "must be a clean slash-separated path") {
		t.Fatalf("PostInstall(traversal read) error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(installDir, "huge.txt"), make([]byte, script.MaxReadTextBytes+1), 0o644); err != nil {
		t.Fatalf("WriteFile(huge) error = %v", err)
	}
	err = runPostInstallScript(t, "def post_install(ctx):\n    ctx.read_text(\"huge.txt\")\n    return {}\n", ctx)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("PostInstall(huge read) error = %v, want size failure", err)
	}
}

// TestPostInstallContextExcludesCredentials verifies the database value carries
// only engine and port, so new config fields never reach scripts by default.
func TestPostInstallContextExcludesCredentials(t *testing.T) {
	_, ctx := newPostInstallInstall(t)
	ctx.Environment = config.Environment{Name: "demo", Database: &config.DatabaseConfig{Engine: " MariaDB ", Version: "11.8", Port: 3307}}

	source := "def post_install(ctx):\n    if ctx.database.engine != \"mariadb\" or ctx.database.port != 3307 or ctx.environment != \"demo\":\n        fail(\"unexpected ctx\")\n    return {\"write\": {\"attrs.txt\": \",\".join(dir(ctx.database))}}\n"
	if err := runPostInstallScript(t, source, ctx); err != nil {
		t.Fatalf("PostInstall() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(ctx.Result.TargetPath), "attrs.txt"))
	if err != nil {
		t.Fatalf("ReadFile(attrs.txt) error = %v", err)
	}
	if string(data) != "engine,port" {
		t.Fatalf("database attributes = %q, want only engine and port", data)
	}
}

// TestPostInstallHookHasNoNetwork verifies post_install cannot use http.
func TestPostInstallHookHasNoNetwork(t *testing.T) {
	_, ctx := newPostInstallInstall(t)
	err := runPostInstallScript(t, "def post_install(ctx):\n    http.get(\"http://127.0.0.1:1/\")\n    return {}\n", ctx)
	if err == nil || !strings.Contains(err.Error(), "only available in the resolve and build_sources hooks") {
		t.Fatalf("PostInstall() error = %v, want http refusal", err)
	}
}

// TestPostInstallModulesAreGated verifies path and crypto only work during
// post_install, in other hooks and at the top level alike.
func TestPostInstallModulesAreGated(t *testing.T) {
	for _, call := range []string{`path.join("a", "b")`, `crypto.random_base64(8)`} {
		t.Run(call, func(t *testing.T) {
			plugin, err := loadScriptFixture(t, validateFixtureManifest, "def validate(env):\n    "+call+"\n").toPlugin(pluginHooks{})
			if err != nil {
				t.Fatalf("toPlugin() error = %v", err)
			}
			err = plugin.Validate(config.Environment{Tools: config.ToolsConfig{"demo": "1"}})
			if err == nil || !strings.Contains(err.Error(), "only available in the post_install hook") {
				t.Fatalf("Validate() error = %v, want module refusal", err)
			}

			manifest := loadScriptFixture(t, postInstallFixture, "VALUE = "+call+"\n\ndef post_install(ctx):\n    return {}\n")
			if _, err := manifest.scriptModule(); err == nil || !strings.Contains(err.Error(), "only available in the post_install hook") {
				t.Fatalf("scriptModule() error = %v, want module refusal at the top level", err)
			}
		})
	}
}

// TestCryptoRandomBase64Bounds verifies the size limits and output length.
func TestCryptoRandomBase64Bounds(t *testing.T) {
	for _, size := range []int{0, -1, scriptRandomMaxBytes + 1} {
		_, ctx := newPostInstallInstall(t)
		source := fmt.Sprintf("def post_install(ctx):\n    crypto.random_base64(%d)\n    return {}\n", size)
		if err := runPostInstallScript(t, source, ctx); err == nil || !strings.Contains(err.Error(), "size must be between") {
			t.Fatalf("random_base64(%d) error = %v, want bounds failure", size, err)
		}
	}
}

// TestPostInstallSkipsMissingInstall verifies an absent install directory is
// not an error and does not compile the script.
func TestPostInstallSkipsMissingInstall(t *testing.T) {
	manifest := loadScriptFixture(t, postInstallFixture, "def post_install(ctx):\n    fail(\"must not run\")\n")
	manifest.loadScript = func() (*script.Module, error) {
		t.Fatal("script compiled for a missing install directory")
		return nil, nil
	}
	plugin, err := manifest.toPlugin(pluginHooks{})
	if err != nil {
		t.Fatalf("toPlugin() error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing", "demo")
	if err := plugin.PostInstall(InstallContext{Result: InstallResult{Tool: "demo", Version: "1.0", TargetPath: missing}}); err != nil {
		t.Fatalf("PostInstall() error = %v, want nil", err)
	}
}

// TestPostInstallHookConflicts verifies a tool cannot implement post_install in
// both Go and Starlark.
func TestPostInstallHookConflicts(t *testing.T) {
	manifest := loadScriptFixture(t, postInstallFixture, "def post_install(ctx):\n    return {}\n")
	_, err := manifest.toPlugin(pluginHooks{postInstall: func(InstallContext) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "post_install") {
		t.Fatalf("toPlugin() error = %v, want hook conflict", err)
	}
}

// phpSingleQuoted quotes value the way generated phpMyAdmin configs do.
func phpSingleQuoted(value string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `'`, `\'`) + "'"
}

// readGolden reads a file captured from the Go implementations the scripts
// replaced, so these tests prove the ports match byte for byte.
func readGolden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", name, err)
	}

	return string(data)
}

// TestPHPMyAdminConfigMatchesGolden compares the script's output with the Go
// renderer it replaced, for every branch of the managed database logic.
func TestPHPMyAdminConfigMatchesGolden(t *testing.T) {
	tests := []struct {
		golden      string
		environment config.Environment
		credentials bool
	}{
		{golden: "mariadb-managed", environment: config.Environment{Name: "demo", Database: &config.DatabaseConfig{Engine: MariaDB, Version: "11.8", Port: 3307}}, credentials: true},
		{golden: "mysql-default", environment: config.Environment{Name: "demo", Database: &config.DatabaseConfig{Engine: " MySQL ", Version: "8.4"}}, credentials: true},
		{golden: "mysql-unnamed", environment: config.Environment{Database: &config.DatabaseConfig{Engine: MySQL, Version: "8.4"}}},
		{golden: "no-database", environment: config.Environment{Name: "demo"}},
		{golden: "postgresql", environment: config.Environment{Name: "demo", Database: &config.DatabaseConfig{Engine: PostgreSQL, Version: "17", Port: 5432}}},
	}
	secretLine := regexp.MustCompile(`(\$cfg\['blowfish_secret'\] = ')([A-Za-z0-9+/]{32})(';)`)

	for _, test := range tests {
		t.Run(test.golden, func(t *testing.T) {
			installDir := t.TempDir()
			target := filepath.Join(installDir, "index.php")
			if err := os.WriteFile(target, []byte("<?php\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(index.php) error = %v", err)
			}
			rootDir := t.TempDir()
			plugin := builtinTestPlugin(t, PHPMyAdmin)
			ctx := InstallContext{
				RootDir:     rootDir,
				Environment: test.environment,
				Result:      InstallResult{Tool: PHPMyAdmin, Version: "5.2", TargetPath: target},
			}
			if err := plugin.PostInstall(ctx); err != nil {
				t.Fatalf("PostInstall() error = %v", err)
			}

			data, err := os.ReadFile(filepath.Join(installDir, "config.inc.php"))
			if err != nil {
				t.Fatalf("ReadFile(config.inc.php) error = %v", err)
			}
			got := string(data)
			if !secretLine.MatchString(got) {
				t.Fatalf("config.inc.php = %q, want a generated 32 character secret", got)
			}
			got = secretLine.ReplaceAllString(got, "${1}@@SECRET@@${3}")
			if test.credentials {
				credentials := phpSingleQuoted(filepath.Join(rootDir, "secrets", "db", "demo.json"))
				if !strings.Contains(got, credentials) {
					t.Fatalf("config.inc.php = %q, want credentials path %s", got, credentials)
				}
				got = strings.ReplaceAll(got, credentials, "'@@CREDENTIALS@@'")
			}
			if want := readGolden(t, "phpmyadmin/"+test.golden+".inc.php"); got != want {
				t.Fatalf("config.inc.php differs from the Go renderer\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}

			if info, err := os.Stat(filepath.Join(installDir, "tmp")); err != nil || !info.IsDir() {
				t.Fatalf("Stat(tmp) = %v, %v, want directory", info, err)
			}
		})
	}
}

// TestPHPMyAdminSecretIsRegenerated verifies each install gets a fresh secret.
func TestPHPMyAdminSecretIsRegenerated(t *testing.T) {
	installDir := t.TempDir()
	target := filepath.Join(installDir, "index.php")
	if err := os.WriteFile(target, []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(index.php) error = %v", err)
	}
	plugin := builtinTestPlugin(t, PHPMyAdmin)
	ctx := InstallContext{RootDir: t.TempDir(), Result: InstallResult{Tool: PHPMyAdmin, Version: "5.2", TargetPath: target}}

	read := func() string {
		t.Helper()
		if err := plugin.PostInstall(ctx); err != nil {
			t.Fatalf("PostInstall() error = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(installDir, "config.inc.php"))
		if err != nil {
			t.Fatalf("ReadFile(config.inc.php) error = %v", err)
		}
		return string(data)
	}
	if first, second := read(), read(); first == second {
		t.Fatal("two installs produced the same config, want a fresh blowfish secret")
	}
}

// phpDevelGoldenInstall lays out a devel pack from golden inputs and returns
// its directory and the phpize.bat target an install would report.
func phpDevelGoldenInstall(t *testing.T, variant string) (string, string) {
	t.Helper()
	installDir := t.TempDir()
	scriptDir := filepath.Join(installDir, "script")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(script) error = %v", err)
	}
	for _, name := range []string{"phpize.js", "config.w32.phpize.in"} {
		input := readGolden(t, "php-devel/"+variant+"/"+name)
		if err := os.WriteFile(filepath.Join(scriptDir, name), []byte(input), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	target := filepath.Join(installDir, "phpize.bat")
	if err := os.WriteFile(target, []byte("@echo off\r\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(phpize.bat) error = %v", err)
	}

	return installDir, target
}

// TestPHPDevelPatchMatchesGolden compares the script's patches with the Go code
// they replaced, for both line endings, and checks they apply only once.
func TestPHPDevelPatchMatchesGolden(t *testing.T) {
	for _, variant := range []string{"lf", "crlf"} {
		t.Run(variant, func(t *testing.T) {
			installDir, target := phpDevelGoldenInstall(t, variant)
			plugin := builtinTestPlugin(t, PHPDevel)
			ctx := InstallContext{Result: InstallResult{Tool: PHPDevel, Version: "8.4", TargetPath: target}}

			// The hook also runs on skipped installs, so a second run must not
			// change anything.
			for run := range 2 {
				if err := plugin.PostInstall(ctx); err != nil {
					t.Fatalf("PostInstall() run %d error = %v", run, err)
				}
				for _, name := range []string{"phpize.js", "config.w32.phpize.in"} {
					data, err := os.ReadFile(filepath.Join(installDir, "script", name))
					if err != nil {
						t.Fatalf("ReadFile(%s) error = %v", name, err)
					}
					if want := readGolden(t, "php-devel/"+variant+"/"+name+".want"); string(data) != want {
						t.Fatalf("run %d: %s differs from the Go patch\n--- got ---\n%q\n--- want ---\n%q", run, name, data, want)
					}
				}
			}
		})
	}
}

// TestPHPDevelPatchFailures verifies a damaged devel pack fails clearly and
// leaves every file as it was, including ones that could have been patched.
func TestPHPDevelPatchFailures(t *testing.T) {
	plugin := builtinTestPlugin(t, PHPDevel)

	t.Run("missing file", func(t *testing.T) {
		installDir, target := phpDevelGoldenInstall(t, "lf")
		if err := os.Remove(filepath.Join(installDir, "script", "config.w32.phpize.in")); err != nil {
			t.Fatalf("Remove() error = %v", err)
		}
		err := plugin.PostInstall(InstallContext{Result: InstallResult{Tool: PHPDevel, Version: "8.4", TargetPath: target}})
		if err == nil || !strings.Contains(err.Error(), "script/config.w32.phpize.in") {
			t.Fatalf("PostInstall() error = %v, want missing file", err)
		}
		data, err := os.ReadFile(filepath.Join(installDir, "script", "phpize.js"))
		if err != nil {
			t.Fatalf("ReadFile(phpize.js) error = %v", err)
		}
		if want := readGolden(t, "php-devel/lf/phpize.js"); string(data) != want {
			t.Fatal("phpize.js changed even though the plan failed")
		}
	})

	t.Run("missing anchor", func(t *testing.T) {
		installDir, target := phpDevelGoldenInstall(t, "lf")
		if err := os.WriteFile(filepath.Join(installDir, "script", "phpize.js"), []byte("// no anchor\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		err := plugin.PostInstall(InstallContext{Result: InstallResult{Tool: PHPDevel, Version: "8.4", TargetPath: target}})
		if err == nil || !strings.Contains(err.Error(), "could not find PHP_DIR anchor") {
			t.Fatalf("PostInstall() error = %v, want missing anchor", err)
		}
	})
}

// dependenciesFixture is a demo tool whose dependencies hook is scripts/demo.star.
const dependenciesFixture = `id: demo
install-candidates:
  all:
    - demo
script:
  file: scripts/demo.star
  hooks: [dependencies]
  vars:
    helper-version: "2.0"
`

// TestDependenciesHookResult checks a valid result, including that an empty
// list reads as no dependencies and a tool with no version skips the hook.
func TestDependenciesHookResult(t *testing.T) {
	plugin, err := loadScriptFixture(t, dependenciesFixture, "def dependencies(env):\n    return [{\"tool\": \" Helper \", \"version\": env.vars[\"helper-version\"]}, {\"tool\": \"other\", \"version\": env.version}]\n").toPlugin(pluginHooks{})
	if err != nil {
		t.Fatalf("toPlugin() error = %v", err)
	}
	provider := plugin.(ToolDependencyProvider)

	requests, err := provider.Dependencies(config.Environment{Tools: config.ToolsConfig{"demo": "1.5"}})
	if err != nil {
		t.Fatalf("Dependencies() error = %v", err)
	}
	if len(requests) != 2 || requests[0] != (InstallRequest{Tool: "helper", Version: "2.0"}) || requests[1] != (InstallRequest{Tool: "other", Version: "1.5"}) {
		t.Fatalf("Dependencies() = %#v", requests)
	}

	empty, err := loadScriptFixture(t, dependenciesFixture, "def dependencies(env):\n    return []\n").toPlugin(pluginHooks{})
	if err != nil {
		t.Fatalf("toPlugin(empty) error = %v", err)
	}
	if requests, err := empty.(ToolDependencyProvider).Dependencies(config.Environment{Tools: config.ToolsConfig{"demo": "1.5"}}); err != nil || requests != nil {
		t.Fatalf("Dependencies(empty) = %#v, %v, want nil", requests, err)
	}

	manifest := loadScriptFixture(t, dependenciesFixture, "def dependencies(env):\n    fail(\"must not run\")\n")
	manifest.loadScript = func() (*script.Module, error) {
		t.Fatal("script compiled for a tool with no version")
		return nil, nil
	}
	unconfigured, err := manifest.toPlugin(pluginHooks{})
	if err != nil {
		t.Fatalf("toPlugin(unconfigured) error = %v", err)
	}
	if requests, err := unconfigured.(ToolDependencyProvider).Dependencies(config.Environment{}); err != nil || requests != nil {
		t.Fatalf("Dependencies(unconfigured) = %#v, %v, want nil", requests, err)
	}
}

// TestDependenciesHookRejectsBadResults covers the result validation, and that
// fail() reaches the user verbatim.
func TestDependenciesHookRejectsBadResults(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "not a list", body: `return {"tool": "a", "version": "1"}`, want: "must be a list"},
		{name: "none", body: `return None`, want: "must be a list"},
		{name: "entry not a dict", body: `return ["a"]`, want: "must be a dict"},
		{name: "unknown key", body: `return [{"tool": "a", "version": "1", "optional": True}]`, want: "unsupported key"},
		{name: "tool not a string", body: `return [{"tool": 1, "version": "1"}]`, want: "must be a string"},
		{name: "missing tool", body: `return [{"version": "1"}]`, want: "invalid tool"},
		{name: "bad tool", body: `return [{"tool": "../a", "version": "1"}]`, want: "invalid tool"},
		{name: "self", body: `return [{"tool": "demo", "version": "1"}]`, want: "itself"},
		{name: "missing version", body: `return [{"tool": "a"}]`, want: "version"},
		{name: "bad version", body: `return [{"tool": "a", "version": "../1"}]`, want: "version"},
		{name: "fail", body: `fail("needs a helper")`, want: "needs a helper"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plugin, err := loadScriptFixture(t, dependenciesFixture, "def dependencies(env):\n    "+test.body+"\n").toPlugin(pluginHooks{})
			if err != nil {
				t.Fatalf("toPlugin() error = %v", err)
			}
			_, err = plugin.(ToolDependencyProvider).Dependencies(config.Environment{Tools: config.ToolsConfig{"demo": "1"}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Dependencies() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestDependenciesHookFailureReachesInstallLayers verifies a script failure
// aborts dependency resolution instead of silently dropping the dependencies.
func TestDependenciesHookFailureReachesInstallLayers(t *testing.T) {
	plugin, err := loadScriptFixture(t, dependenciesFixture, "def dependencies(env):\n    fail(\"needs a helper\")\n").toPlugin(pluginHooks{})
	if err != nil {
		t.Fatalf("toPlugin() error = %v", err)
	}
	registry, err := NewRegistry(plugin)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	_, err = registry.InstallRequestLayers(config.Environment{Tools: config.ToolsConfig{"demo": "1"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "needs a helper") {
		t.Fatalf("InstallRequestLayers() error = %v, want the script failure", err)
	}
}

// TestDependenciesHookConflicts verifies dependencies cannot be declared twice.
func TestDependenciesHookConflicts(t *testing.T) {
	manifest := loadScriptFixture(t, dependenciesFixture, "def dependencies(env):\n    return []\n")
	_, err := manifest.toPlugin(pluginHooks{dependencies: func(config.Environment) ([]InstallRequest, error) { return nil, nil }})
	if err == nil || !strings.Contains(err.Error(), "dependencies") {
		t.Fatalf("toPlugin() error = %v, want Go hook conflict", err)
	}

	data := []byte(dependenciesFixture + "dependencies:\n  - tool: helper\n    version: \"1\"\n")
	if _, err := parsePluginManifest(data); err == nil || !strings.Contains(err.Error(), "cannot be combined with manifest dependencies") {
		t.Fatalf("parsePluginManifest() error = %v, want manifest conflict", err)
	}
}

// TestPHPDependenciesPerPlatform checks the php rule on both platforms from any
// host, for both runtime flavors, including the shared extension-sdk setting.
func TestPHPDependenciesPerPlatform(t *testing.T) {
	sdk := config.SettingsConfig{PHP: {"extension-sdk": true}}
	tests := []struct {
		name        string
		tool        string
		goos        string
		environment config.Environment
		want        []InstallRequest
	}{
		{name: "nts windows sdk", tool: PHP, goos: "windows", environment: config.Environment{Tools: config.ToolsConfig{PHP: "8.4"}, Settings: sdk}, want: []InstallRequest{{Tool: PHPDevel, Version: "8.4"}, {Tool: PHPSDK, Version: DefaultPHPSDKVersion}}},
		{name: "zts windows sdk", tool: PHPZTS, goos: "windows", environment: config.Environment{Tools: config.ToolsConfig{PHPZTS: "8.3"}, Settings: sdk}, want: []InstallRequest{{Tool: PHPDevel, Version: "8.3"}, {Tool: PHPSDK, Version: DefaultPHPSDKVersion}}},
		{name: "windows without sdk", tool: PHP, goos: "windows", environment: config.Environment{Tools: config.ToolsConfig{PHP: "8.4"}}},
		{name: "linux with sdk", tool: PHP, goos: "linux", environment: config.Environment{Tools: config.ToolsConfig{PHP: "8.4"}, Settings: sdk}},
		{name: "no version", tool: PHP, goos: "windows", environment: config.Environment{Settings: sdk}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest, err := loadBuiltinManifest(test.tool)
			if err != nil {
				t.Fatalf("loadBuiltinManifest() error = %v", err)
			}
			settings := manifest.Settings
			if len(settings) == 0 {
				settings = manifest.sharedSettings
			}
			hook := scriptDependenciesFunc(manifest, PHP, settings, manifestVersionFunc(manifest), test.goos, "amd64")
			got, err := hook(test.environment)
			if err != nil {
				t.Fatalf("hook error = %v", err)
			}
			if fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("dependencies = %#v, want %#v", got, test.want)
			}
		})
	}
}

// TestPHPSDKVersionMatchesDefault fails if the manifests' SDK version drifts
// from the constant backend uses for internal tool defaults.
func TestPHPSDKVersionMatchesDefault(t *testing.T) {
	for _, tool := range []string{PHP, PHPZTS} {
		manifest, err := loadBuiltinManifest(tool)
		if err != nil {
			t.Fatalf("loadBuiltinManifest(%s) error = %v", tool, err)
		}
		if got := manifest.Script.Vars["php-sdk-version"]; got != DefaultPHPSDKVersion {
			t.Fatalf("%s php-sdk-version = %q, want DefaultPHPSDKVersion %q", tool, got, DefaultPHPSDKVersion)
		}
	}
}

// TestPHPDispatchDoesNotCompileScript protects command dispatch latency: php
// runs constantly, so validating and resolving it must never parse Starlark.
func TestPHPDispatchDoesNotCompileScript(t *testing.T) {
	for _, tool := range []string{PHP, PHPZTS} {
		manifest, err := loadBuiltinManifest(tool)
		if err != nil {
			t.Fatalf("loadBuiltinManifest(%s) error = %v", tool, err)
		}
		manifest.loadScript = func() (*script.Module, error) {
			t.Fatalf("%s script compiled on the dispatch path", tool)
			return nil, nil
		}
		plugin, err := manifest.toPlugin(goToolHooks()[tool])
		if err != nil {
			t.Fatalf("toPlugin(%s) error = %v", tool, err)
		}
		registry, err := NewRegistry(plugin)
		if err != nil {
			t.Fatalf("NewRegistry(%s) error = %v", tool, err)
		}
		environment := config.Environment{Tools: config.ToolsConfig{tool: "8.4"}, Settings: config.SettingsConfig{PHP: {"extension-sdk": true}}}
		if err := plugin.Validate(environment); err != nil {
			t.Fatalf("Validate(%s) error = %v", tool, err)
		}
		if _, err := registry.ResolveDispatchRequestForEnvironment("php", environment); err != nil {
			t.Fatalf("ResolveDispatchRequestForEnvironment(%s) error = %v", tool, err)
		}
	}
}
