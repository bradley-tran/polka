package tools

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"polka/config"
)

// resolveFixtureManifest returns a demo manifest whose resolve hook lives in
// scripts/demo.star. extra is appended verbatim as additional YAML.
func resolveFixtureManifest(extra string) string {
	return `id: demo
install-candidates:
  all:
    - demo
download:
  assets:
    all:
      filename: demo-{version}-{build}.zip
      url: https://example.com/demo-{version}.zip
      archive-format: zip
script:
  file: scripts/demo.star
  hooks: [resolve]
  vars:
    download-page: https://example.com/index
` + extra
}

// validateFixtureManifest is a demo manifest with typed settings whose
// validate hook lives in scripts/demo.star.
const validateFixtureManifest = `id: demo
settings:
  port: port
  label: string
  enabled: bool
install-candidates:
  all:
    - demo
script:
  file: scripts/demo.star
  hooks: [validate]
`

// scriptFixture builds an in-memory manifest directory holding demo.yaml and
// scripts/demo.star, the layout external loaders will use.
func scriptFixture(manifest, source string) fstest.MapFS {
	return fstest.MapFS{
		"demo.yaml":         {Data: []byte(manifest)},
		"scripts/demo.star": {Data: []byte(source)},
	}
}

// loadScriptFixture loads the demo manifest from scriptFixture.
func loadScriptFixture(t *testing.T, manifest, source string) pluginManifest {
	t.Helper()
	loaded, err := loadManifest(scriptFixture(manifest, source), "demo")
	if err != nil {
		t.Fatalf("loadManifest() error = %v", err)
	}

	return loaded
}

// resolveFixture runs the demo resolve hook for version 1 on linux/amd64.
func resolveFixture(t *testing.T, client *http.Client, manifest, source string) (manifestRequestPlan, error) {
	t.Helper()
	return resolveManifestRequest(client, loadScriptFixture(t, manifest, source), "1", "linux", "amd64")
}

// TestManifestScriptSchemaValidation covers static script block checks.
func TestManifestScriptSchemaValidation(t *testing.T) {
	base := "id: demo\ninstall-candidates:\n  all:\n    - demo\n"
	assets := "download:\n  assets:\n    all:\n      filename: demo.zip\n      url: https://example.com/demo.zip\n      archive-format: zip\n"
	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{name: "missing file", manifest: base + "script:\n  hooks: [validate]\n", want: "script requires file"},
		{name: "escaping path", manifest: base + "script:\n  file: ../demo.star\n  hooks: [validate]\n", want: "must stay inside"},
		{name: "absolute path", manifest: base + "script:\n  file: /demo.star\n  hooks: [validate]\n", want: "script"},
		{name: "wrong extension", manifest: base + "script:\n  file: demo.py\n  hooks: [validate]\n", want: ".star extension"},
		{name: "no hooks", manifest: base + "script:\n  file: demo.star\n", want: "at least one hook"},
		{name: "unknown hook", manifest: base + "script:\n  file: demo.star\n  hooks: [install]\n", want: `unsupported hook "install"`},
		{name: "duplicate hook", manifest: base + "script:\n  file: demo.star\n  hooks: [validate, Validate]\n", want: "duplicates hook"},
		{name: "invalid vars key", manifest: base + "script:\n  file: demo.star\n  hooks: [validate]\n  vars:\n    \"bad key\": x\n", want: "invalid script.vars key"},
		{name: "resolve without assets", manifest: base + "script:\n  file: demo.star\n  hooks: [resolve]\n", want: "requires download.assets"},
		{name: "resolve with github", manifest: base + assets + "  github:\n    owner: o\n    repo: r\n" + "script:\n  file: demo.star\n  hooks: [resolve]\n", want: "cannot be combined"},
		{name: "resolve with release index", manifest: base + assets + "  release-index:\n    url: https://example.com/{requested}\n    pattern: (x)\n" + "script:\n  file: demo.star\n  hooks: [resolve]\n", want: "cannot be combined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parsePluginManifest([]byte(test.manifest))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parsePluginManifest() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestManifestScriptNormalization verifies hooks and vars keys are lowercased.
func TestManifestScriptNormalization(t *testing.T) {
	manifest := loadScriptFixture(t, strings.Replace(validateFixtureManifest, "hooks: [validate]", "hooks: [ Validate ]\n  vars:\n    Page-URL: x", 1), "def validate(env):\n    pass\n")

	if !manifest.Script.hasHook(scriptHookValidate) {
		t.Fatalf("Script.Hooks = %#v, want validate", manifest.Script.Hooks)
	}
	if manifest.Script.Vars["page-url"] != "x" {
		t.Fatalf("Script.Vars = %#v, want lowercased page-url", manifest.Script.Vars)
	}
}

// TestLoadManifestRequiresScriptFile verifies a missing script fails at load.
func TestLoadManifestRequiresScriptFile(t *testing.T) {
	fsys := fstest.MapFS{"demo.yaml": {Data: []byte(validateFixtureManifest)}}

	_, err := loadManifest(fsys, "demo")
	if err == nil || !strings.Contains(err.Error(), "scripts/demo.star") {
		t.Fatalf("loadManifest() error = %v, want missing script error", err)
	}
}

// TestToPluginRejectsScriptConflicts verifies script and Go hooks cannot both
// implement the same hook, and scripts need a manifest filesystem.
func TestToPluginRejectsScriptConflicts(t *testing.T) {
	resolveManifest := loadScriptFixture(t, resolveFixtureManifest(""), "def resolve(ctx):\n    pass\n")
	if _, err := resolveManifest.toPlugin(pluginHooks{download: func(DownloadContext) error { return nil }}); err == nil || !strings.Contains(err.Error(), "script resolve hook") {
		t.Fatalf("toPlugin(resolve + download) error = %v", err)
	}

	validateManifest := loadScriptFixture(t, validateFixtureManifest, "def validate(env):\n    pass\n")
	if _, err := validateManifest.toPlugin(pluginHooks{validate: func(config.Environment) error { return nil }}); err == nil || !strings.Contains(err.Error(), "script validate hook") {
		t.Fatalf("toPlugin(validate + validate) error = %v", err)
	}

	parsed, err := parsePluginManifest([]byte(validateFixtureManifest))
	if err != nil {
		t.Fatalf("parsePluginManifest() error = %v", err)
	}
	if _, err := parsed.toPlugin(pluginHooks{}); err == nil || !strings.Contains(err.Error(), "requires loading the manifest from a filesystem") {
		t.Fatalf("toPlugin(bytes-only manifest) error = %v", err)
	}
}

// TestToolScriptBindChecks covers hook/function consistency checks run when
// a script is first loaded.
func TestToolScriptBindChecks(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "declared hook missing", source: "def _helper():\n    pass\n", want: `declares hook "validate" but does not define it`},
		{name: "hook not a function", source: "validate = 1\n", want: "does not define it as a function"},
		{name: "wrong arity", source: "def validate(env, extra):\n    pass\n", want: "exactly one parameter"},
		{name: "varargs", source: "def validate(*args):\n    pass\n", want: "exactly one parameter"},
		{name: "undeclared hook", source: "def validate(env):\n    pass\ndef resolve(ctx):\n    pass\n", want: `hook "resolve" that is not declared`},
		{name: "public helper", source: "def validate(env):\n    pass\ndef helper():\n    pass\n", want: `unsupported public function "helper"`},
		{name: "top-level http", source: "PAGE = http.get(\"https://example.com\")\ndef validate(env):\n    pass\n", want: "only available in the resolve and build_sources hooks"},
		{name: "syntax error", source: "def validate(env):\n    return (\n", want: "compile script scripts/demo.star"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := loadScriptFixture(t, validateFixtureManifest, test.source)
			_, err := manifest.scriptModule()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("scriptModule() error = %v, want containing %q", err, test.want)
			}
		})
	}

	manifest := loadScriptFixture(t, validateFixtureManifest, "LIMIT = 3\ndef _helper():\n    pass\ndef validate(env):\n    pass\n")
	if _, err := manifest.scriptModule(); err != nil {
		t.Fatalf("scriptModule() with constants and private helpers error = %v", err)
	}
}

// TestResolveScriptBuildsDownloadPlan verifies a successful resolve hook: ctx
// fields, script vars, http.get, template values, and asset overrides.
func TestResolveScriptBuildsDownloadPlan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("1.2.3\n"))
	}))
	defer server.Close()

	source := `
def resolve(ctx):
    found = http.get(ctx.vars["download-page"]).strip()
    return {
        "version": found,
        "tag": "v" + found,
        "values": {"build": ctx.platform + "-" + ctx.requested + "-" + ctx.tool},
        "url": "https://mirror.example.com/demo.zip",
        "checksum_algorithm": "sha256",
        "checksum": "a" * 64,
        "checksum_url": None,
    }
`
	manifest := strings.Replace(resolveFixtureManifest(""), "https://example.com/index", server.URL, 1)
	plan, err := resolveFixture(t, server.Client(), manifest, source)
	if err != nil {
		t.Fatalf("resolveManifestRequest() error = %v", err)
	}
	if plan.ResolvedVersion != "1.2.3" || plan.Tag != "v1.2.3" {
		t.Fatalf("plan version/tag = %q/%q, want 1.2.3/v1.2.3", plan.ResolvedVersion, plan.Tag)
	}
	if plan.Asset.FileName != "demo-1.2.3-linux-amd64-1-demo.zip" {
		t.Fatalf("plan file = %q", plan.Asset.FileName)
	}
	if plan.Asset.URL != "https://mirror.example.com/demo.zip" {
		t.Fatalf("plan URL = %q, want override", plan.Asset.URL)
	}
	if plan.Asset.ChecksumAlgorithm != checksumAlgorithmSHA256 || plan.Asset.Checksum != strings.Repeat("a", 64) {
		t.Fatalf("plan checksum = %q %q", plan.Asset.ChecksumAlgorithm, plan.Asset.Checksum)
	}
}

// TestResolveScriptRejectsInvalidPlans covers result validation, user-facing
// fail() errors, and script bugs.
func TestResolveScriptRejectsInvalidPlans(t *testing.T) {
	tests := []struct {
		name     string
		extra    string
		source   string
		want     string
		wantNot  string
		wantFull string
	}{
		{name: "not a dict", source: "def resolve(ctx):\n    return \"1.0\"\n", want: "result must be a dict"},
		{name: "missing version", source: "def resolve(ctx):\n    return {\"values\": {\"build\": \"1\"}}\n", want: "result requires version"},
		{name: "path traversal version", source: "def resolve(ctx):\n    return {\"version\": \"../x\"}\n", want: "invalid demo version"},
		{name: "invalid tag", source: "def resolve(ctx):\n    return {\"version\": \"1.0\", \"tag\": \"a/b\"}\n", want: "demo script tag"},
		{name: "unknown key", source: "def resolve(ctx):\n    return {\"version\": \"1.0\", \"mirror\": \"x\"}\n", want: `unsupported key "mirror"`},
		{name: "non-string field", source: "def resolve(ctx):\n    return {\"version\": 1}\n", want: "version must be a string"},
		{name: "non-string value", source: "def resolve(ctx):\n    return {\"version\": \"1.0\", \"values\": {\"build\": 1}}\n", want: `values["build"] must be a string`},
		{name: "shadowed placeholder", source: "def resolve(ctx):\n    return {\"version\": \"1.0\", \"values\": {\"build\": \"1\", \"version\": \"2\"}}\n", want: `built-in placeholder "version"`},
		{name: "invalid values key", source: "def resolve(ctx):\n    return {\"version\": \"1.0\", \"values\": {\"build\": \"1\", \"a-b\": \"2\"}}\n", want: "letters, numbers, or underscores"},
		{name: "non-http url", source: "def resolve(ctx):\n    return {\"version\": \"1.0\", \"values\": {\"build\": \"1\"}, \"url\": \"file:///etc/passwd\"}\n", want: "absolute http or https URL"},
		{name: "relative checksum url", source: "def resolve(ctx):\n    return {\"version\": \"1.0\", \"values\": {\"build\": \"1\"}, \"checksum_url\": \"/sum.txt\"}\n", want: "checksum_url"},
		{name: "unsupported algorithm", source: "def resolve(ctx):\n    return {\"version\": \"1.0\", \"values\": {\"build\": \"1\"}, \"checksum_algorithm\": \"embedded-commit-id\"}\n", want: "unsupported checksum_algorithm"},
		{name: "checksum without algorithm", source: "def resolve(ctx):\n    return {\"version\": \"1.0\", \"values\": {\"build\": \"1\"}, \"checksum\": \"ab\"}\n", want: "requires a checksum algorithm"},
		{name: "malformed checksum", source: "def resolve(ctx):\n    return {\"version\": \"1.0\", \"values\": {\"build\": \"1\"}, \"checksum_algorithm\": \"sha256\", \"checksum\": \"zz\"}\n", want: "invalid sha256 checksum"},
		{name: "missing template value", source: "def resolve(ctx):\n    return {\"version\": \"1.0\"}\n", want: `unknown template value "build"`},
		{name: "version constraint", extra: "version-constraint:\n  below: \"2\"\n", source: "def resolve(ctx):\n    return {\"version\": \"2.1\", \"values\": {\"build\": \"1\"}}\n", want: "requires < 2"},
		{name: "fail is verbatim", source: "def resolve(ctx):\n    fail(\"no demo release for %s\" % ctx.requested)\n", wantFull: "no demo release for 1"},
		{name: "script bug has backtrace", source: "def resolve(ctx):\n    return {}[\"missing\"]\n", want: "Traceback"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveFixture(t, nil, resolveFixtureManifest(test.extra), test.source)
			if err == nil {
				t.Fatal("resolveManifestRequest() unexpectedly succeeded")
			}
			if test.wantFull != "" && err.Error() != test.wantFull {
				t.Fatalf("resolveManifestRequest() error = %q, want exactly %q", err, test.wantFull)
			}
			if test.want != "" && !strings.Contains(err.Error(), test.want) {
				t.Fatalf("resolveManifestRequest() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestResolveScriptHTTPErrorsAreUserFacing verifies network failures surface
// without a Starlark backtrace and http rejects non-http schemes.
func TestResolveScriptHTTPErrorsAreUserFacing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	manifest := strings.Replace(resolveFixtureManifest(""), "https://example.com/index", server.URL, 1)
	_, err := resolveFixture(t, server.Client(), manifest, "def resolve(ctx):\n    return {\"version\": http.get(ctx.vars[\"download-page\"])}\n")
	if err == nil || !strings.Contains(err.Error(), "download demo release metadata") || strings.Contains(err.Error(), "Traceback") {
		t.Fatalf("resolve with 404 error = %v, want user-facing download error", err)
	}

	_, err = resolveFixture(t, nil, resolveFixtureManifest(""), "def resolve(ctx):\n    return {\"version\": http.get(\"file:///etc/passwd\")}\n")
	if err == nil || !strings.Contains(err.Error(), "absolute http or https URL") {
		t.Fatalf("resolve with file URL error = %v", err)
	}
}

// TestValidateScriptHook covers env accessors, fail() messages, and skipping
// the hook for unconfigured tools.
func TestValidateScriptHook(t *testing.T) {
	source := `
def validate(env):
    if env.version != "1.0":
        fail("unexpected version " + env.version)
    if env.tool_version("other") != "2.0":
        fail("unexpected other version")
    if env.setting("label") == "bad" or env.setting("enabled"):
        fail("unexpected settings")
    if env.setting("port") == 1234:
        fail("demo port 1234 is reserved")
`
	manifest := loadScriptFixture(t, validateFixtureManifest, source)
	plugin, err := manifest.toPlugin(pluginHooks{})
	if err != nil {
		t.Fatalf("toPlugin() error = %v", err)
	}

	configured := func(port int) config.Environment {
		return config.Environment{
			Tools:    config.ToolsConfig{"demo": "1.0", "other": "2.0"},
			Settings: config.SettingsConfig{"demo": {"port": port}},
		}
	}
	if err := plugin.Validate(configured(1234)); err == nil || err.Error() != "demo port 1234 is reserved" {
		t.Fatalf("Validate(port 1234) error = %v, want verbatim fail message", err)
	}
	if err := plugin.Validate(configured(8080)); err != nil {
		t.Fatalf("Validate(port 8080) error = %v", err)
	}
	if err := plugin.Validate(config.Environment{}); err != nil {
		t.Fatalf("Validate(unconfigured) error = %v", err)
	}
}

// TestValidateScriptHookErrors covers lazy compilation, return-value checks,
// undeclared settings, and the missing http module.
func TestValidateScriptHookErrors(t *testing.T) {
	configured := config.Environment{Tools: config.ToolsConfig{"demo": "1.0"}}
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "syntax error", source: "def validate(env):\n    return (\n", want: "compile script"},
		{name: "non-None result", source: "def validate(env):\n    return \"bad\"\n", want: "must return None"},
		{name: "undeclared setting", source: "def validate(env):\n    env.setting(\"missing\")\n", want: `declares no setting "missing" (declared: enabled, label, port)`},
		{name: "http unavailable", source: "def validate(env):\n    http.get(\"https://example.com\")\n", want: "only available in the resolve and build_sources hooks"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plugin, err := loadScriptFixture(t, validateFixtureManifest, test.source).toPlugin(pluginHooks{})
			if err != nil {
				t.Fatalf("toPlugin() error = %v", err)
			}
			// Unconfigured tools never compile or run the script.
			if err := plugin.Validate(config.Environment{}); err != nil {
				t.Fatalf("Validate(unconfigured) error = %v", err)
			}
			if err := plugin.Validate(configured); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate(configured) error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestBuiltinToolScriptsLoad compiles and binds every embedded tool script
// and rejects scripts no manifest references.
func TestBuiltinToolScriptsLoad(t *testing.T) {
	fsys := builtinManifestFS()
	manifestNames, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		t.Fatalf("fs.Glob(manifests) error = %v", err)
	}
	referenced := map[string]bool{}
	for _, name := range manifestNames {
		manifest, err := loadBuiltinManifest(strings.TrimSuffix(name, ".yaml"))
		if err != nil {
			t.Fatalf("loadBuiltinManifest(%s) error = %v", name, err)
		}
		if manifest.Script.File == "" {
			continue
		}
		referenced[manifest.Script.File] = true
		if _, err := manifest.scriptModule(); err != nil {
			t.Fatalf("%s script error = %v", name, err)
		}
	}

	scripts, err := fs.Glob(fsys, "scripts/*.star")
	if err != nil {
		t.Fatalf("fs.Glob(scripts) error = %v", err)
	}
	if len(scripts) < 3 {
		t.Fatalf("embedded scripts = %#v, want at least sqlite, apache, rabbitmq", scripts)
	}
	for _, file := range scripts {
		if !referenced[path.Clean(file)] {
			t.Fatalf("embedded script %s is not referenced by any manifest", file)
		}
	}

	// Library files are never named by a manifest, so an unused one can only
	// be found by looking for a load() of it in another script.
	libraries, err := fs.Glob(fsys, scriptLibraryDir+"/*.star")
	if err != nil {
		t.Fatalf("fs.Glob(libraries) error = %v", err)
	}
	for _, library := range libraries {
		statement := "load(\"" + path.Base(library) + "\""
		used := false
		for _, file := range append(append([]string{}, scripts...), libraries...) {
			data, err := fs.ReadFile(fsys, file)
			if err != nil {
				t.Fatalf("ReadFile(%s) error = %v", file, err)
			}
			if file != library && strings.Contains(string(data), statement) {
				used = true
			}
		}
		if !used {
			t.Fatalf("embedded library %s is not loaded by any script", library)
		}
	}
}

// TestScriptLoadResolvesInsideLibraryDir verifies tool scripts can share code
// from the library directory but cannot load other scripts.
func TestScriptLoadResolvesInsideLibraryDir(t *testing.T) {
	manifest := "id: demo\ninstall-candidates:\n  all:\n    - demo\nscript:\n  file: scripts/demo.star\n  hooks: [validate]\n"
	tests := []struct {
		name    string
		module  string
		wantErr string
	}{
		{name: "library", module: "shared.star"},
		{name: "sibling escape", module: "../other.star", wantErr: "must be a .star path"},
		{name: "not in library", module: "other.star", wantErr: "load script scripts/lib/other.star"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"demo.yaml":               {Data: []byte(manifest)},
				"scripts/demo.star":       {Data: []byte("load(\"" + test.module + "\", \"check\")\n\ndef validate(env):\n    check()\n")},
				"scripts/other.star":      {Data: []byte("def check():\n    pass\n")},
				"scripts/lib/shared.star": {Data: []byte("def check():\n    pass\n")},
			}
			loaded, err := loadManifest(fsys, "demo")
			if err != nil {
				t.Fatalf("loadManifest() error = %v", err)
			}
			_, err = loaded.scriptModule()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("scriptModule() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("scriptModule() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

// TestManifestRejectsUnknownKeys verifies a misspelled key fails instead of
// silently disabling what it was meant to configure.
func TestManifestRejectsUnknownKeys(t *testing.T) {
	data := []byte("id: demo\ninstall-candidates:\n  all:\n    - demo\nscirpt:\n  file: scripts/demo.star\n")
	if _, err := parsePluginManifest(data); err == nil || !strings.Contains(err.Error(), "scirpt") {
		t.Fatalf("parsePluginManifest() error = %v, want unknown key error", err)
	}
}

// TestValidateHookReadsSharedSettings verifies a settings-from tool's hook can
// read the namespace it shares, using the referenced manifest's schema.
func TestValidateHookReadsSharedSettings(t *testing.T) {
	fsys := fstest.MapFS{
		"base.yaml":         {Data: []byte("id: base\nsettings:\n  flag: bool\ninstall-candidates:\n  all:\n    - base\n")},
		"demo.yaml":         {Data: []byte("id: demo\nsettings-from: base\ninstall-candidates:\n  all:\n    - demo\nscript:\n  file: scripts/demo.star\n  hooks: [validate]\n")},
		"scripts/demo.star": {Data: []byte("def validate(env):\n    if env.setting(\"flag\"):\n        fail(\"flag is on\")\n")},
	}
	loaded, err := loadManifest(fsys, "demo")
	if err != nil {
		t.Fatalf("loadManifest() error = %v", err)
	}
	plugin, err := loaded.toPlugin(pluginHooks{})
	if err != nil {
		t.Fatalf("toPlugin() error = %v", err)
	}

	environment := config.Environment{
		Tools:    config.ToolsConfig{"demo": "1"},
		Settings: config.SettingsConfig{"base": {"flag": true}},
	}
	if err := plugin.Validate(environment); err == nil || err.Error() != "flag is on" {
		t.Fatalf("Validate() error = %v, want the script to read the shared setting", err)
	}
}

// TestLoadManifestRejectsBadSharedSettings verifies a script tool's settings-from
// must name a manifest that exists and declares settings.
func TestLoadManifestRejectsBadSharedSettings(t *testing.T) {
	manifest := "id: demo\nsettings-from: base\ninstall-candidates:\n  all:\n    - demo\nscript:\n  file: scripts/demo.star\n  hooks: [validate]\n"
	tests := []struct {
		name string
		base string
		want string
	}{
		{name: "missing", want: "settings-from"},
		{name: "no settings", base: "id: base\ninstall-candidates:\n  all:\n    - base\n", want: "declares no settings"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"demo.yaml":         {Data: []byte(manifest)},
				"scripts/demo.star": {Data: []byte("def validate(env):\n    pass\n")},
			}
			if test.base != "" {
				fsys["base.yaml"] = &fstest.MapFile{Data: []byte(test.base)}
			}
			if _, err := loadManifest(fsys, "demo"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadManifest() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestBuiltinScriptModuleIsShared verifies repeated registry construction
// reuses one compiled module per built-in script.
func TestBuiltinScriptModuleIsShared(t *testing.T) {
	first, err := loadBuiltinManifest(SQLite)
	if err != nil {
		t.Fatalf("loadBuiltinManifest() error = %v", err)
	}
	second, err := loadBuiltinManifest(SQLite)
	if err != nil {
		t.Fatalf("loadBuiltinManifest() error = %v", err)
	}
	firstModule, err := first.scriptModule()
	if err != nil {
		t.Fatalf("scriptModule() error = %v", err)
	}
	secondModule, err := second.scriptModule()
	if err != nil {
		t.Fatalf("scriptModule() error = %v", err)
	}
	if firstModule != secondModule {
		t.Fatal("built-in script module was compiled twice")
	}
}

// TestScriptVersionModule covers version.compare, matches, and best.
func TestScriptVersionModule(t *testing.T) {
	source := `
def resolve(ctx):
    checks = [
        version.compare("1.10", "1.9") == 1,
        version.compare("1.0", "1") == 0,
        version.matches("3.53.2", "3.53"),
        not version.matches("3.530", "3.53"),
        version.best(["3.52.0", "v3.53.1", "3.53.2"], "3.53") == "3.53.2",
        version.best(["3.52.0"], "3.53") == None,
    ]
    if not all(checks):
        fail("version checks = %r" % checks)
    return {"version": "1.0", "values": {"build": "1"}}
`
	if _, err := resolveFixture(t, nil, resolveFixtureManifest(""), source); err != nil {
		t.Fatalf("resolve with version module error = %v", err)
	}
}
