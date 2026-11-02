package tools

import (
	"slices"
	"strings"
	"testing"
)

// buildManifestFixture returns a manifest with the given build block and a
// script declaring the build_sources hook.
func buildManifestFixture(build string) string {
	return "id: demo\ninstall-candidates:\n  all:\n    - demo\nscript:\n  file: scripts/demo.star\n  hooks: [build_sources]\n" + build
}

// TestManifestBuildSchemaValidation covers the static checks on build blocks, so
// a mistake in a recipe fails at load instead of partway through a long compile.
func TestManifestBuildSchemaValidation(t *testing.T) {
	stage := "  stages:\n    - name: demo\n      source: demo\n"
	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{name: "valid", manifest: buildManifestFixture("build:\n  platforms: [linux-amd64]\n" + stage)},
		{name: "valid with compiler alternatives", manifest: buildManifestFixture("build:\n  platforms: [linux-amd64]\n  requires:\n    commands: [\"c++|g++\"]\n" + stage)},
		{name: "hook without build", manifest: buildManifestFixture(""), want: "requires a build block"},
		{name: "build without hook", manifest: "id: demo\ninstall-candidates:\n  all:\n    - demo\nbuild:\n  platforms: [linux-amd64]\n" + stage, want: "requires script hook build_sources"},
		{name: "no platforms", manifest: buildManifestFixture("build:\n" + stage), want: "requires platforms"},
		{name: "bad platform", manifest: buildManifestFixture("build:\n  platforms: [plan9-amd64]\n" + stage), want: "unsupported platform"},
		{name: "no stages", manifest: buildManifestFixture("build:\n  platforms: [linux-amd64]\n"), want: "requires stages"},
		{name: "bad command", manifest: buildManifestFixture("build:\n  platforms: [linux-amd64]\n  requires:\n    commands: [\"sh -c evil\"]\n" + stage), want: "invalid command"},
		{name: "library without detection", manifest: buildManifestFixture("build:\n  platforms: [linux-amd64]\n  requires:\n    libraries:\n      - name: x\n" + stage), want: "requires pkg-config or header"},
		{name: "duplicate stage", manifest: buildManifestFixture("build:\n  platforms: [linux-amd64]\n" + stage + "    - name: demo\n      source: demo\n"), want: "duplicates name"},
		{name: "unknown placeholder", manifest: buildManifestFixture("build:\n  platforms: [linux-amd64]\n  stages:\n    - name: demo\n      source: demo\n      configure: [\"--x={nope}\"]\n"), want: "unknown template value"},
		{name: "bad prefix kind", manifest: buildManifestFixture("build:\n  platforms: [linux-amd64]\n  stages:\n    - name: demo\n      source: demo\n      prefix: elsewhere\n"), want: "unsupported prefix"},
		{name: "escaping copy", manifest: buildManifestFixture("build:\n  platforms: [linux-amd64]\n" + stage + "  package:\n    copy:\n      - from: ../secret\n        to: demo\n"), want: "must stay inside"},
		{name: "escaping remove", manifest: buildManifestFixture("build:\n  platforms: [linux-amd64]\n" + stage + "  package:\n    remove: [../../home]\n"), want: "must stay inside"},
		{name: "escaping verify", manifest: buildManifestFixture("build:\n  platforms: [linux-amd64]\n" + stage + "  verify:\n    - [../bin/sh]\n"), want: "must stay inside"},
		{name: "empty verify", manifest: buildManifestFixture("build:\n  platforms: [linux-amd64]\n" + stage + "  verify:\n    - []\n"), want: "requires a command"},
		{name: "extra configure without from", manifest: buildManifestFixture("build:\n  extra-configure: [--x]\n  platforms: [linux-amd64]\n" + stage), want: "requires build.from"},
		{name: "from with other fields", manifest: buildManifestFixture("build:\n  from: php\n  platforms: [linux-amd64]\n"), want: "cannot be combined"},
		{name: "from itself", manifest: buildManifestFixture("build:\n  from: demo\n"), want: "cannot reference itself"},
		{name: "unknown key", manifest: buildManifestFixture("build:\n  platform: [linux-amd64]\n" + stage), want: "platform"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parsePluginManifest([]byte(test.manifest))
			if test.want == "" {
				if err != nil {
					t.Fatalf("parsePluginManifest() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parsePluginManifest() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestBuildableToolsMatchTheLinuxDownloadGap pins which tools have recipes. Every
// one of them lacks a Linux download, so a recipe never shadows an upstream payload.
func TestBuildableToolsMatchTheLinuxDownloadGap(t *testing.T) {
	registry := NewDefaultRegistry()

	want := []string{Apache, Nginx, PHP, PHPZTS, Redis}
	slices.Sort(want)
	if got := registry.BuildableTools("linux", "amd64"); !slices.Equal(got, want) {
		t.Fatalf("BuildableTools(linux) = %v, want %v", got, want)
	}
	if got := registry.BuildableTools("windows", "amd64"); len(got) != 0 {
		t.Fatalf("BuildableTools(windows) = %v, want none", got)
	}
	for _, tool := range want {
		manifest, err := loadBuiltinManifest(tool)
		if err != nil {
			t.Fatalf("loadBuiltinManifest(%s) error = %v", tool, err)
		}
		for _, key := range downloadPlatformKeys("linux", "amd64") {
			if _, ok := manifest.Download.Assets[key]; ok {
				t.Fatalf("%s has both a recipe and a %s download asset", tool, key)
			}
		}
	}
}

// TestPHPZTSRecipeExtendsPHPRecipe verifies php-zts reuses php's recipe with only
// thread safety added, and that merging leaves php's own recipe untouched.
func TestPHPZTSRecipeExtendsPHPRecipe(t *testing.T) {
	php, err := loadBuiltinManifest(PHP)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(php) error = %v", err)
	}
	zts, err := loadBuiltinManifest(PHPZTS)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(php-zts) error = %v", err)
	}

	phpConfigure := php.Build.Stages[php.Build.configureStageIndex()].Configure
	ztsConfigure := zts.Build.Stages[zts.Build.configureStageIndex()].Configure
	if slices.Contains(phpConfigure, "--enable-zts") {
		t.Fatalf("php configure = %v, want no --enable-zts", phpConfigure)
	}
	if want := append(slices.Clone(phpConfigure), "--enable-zts"); !slices.Equal(ztsConfigure, want) {
		t.Fatalf("php-zts configure = %v, want %v", ztsConfigure, want)
	}
}

// TestPHPRecipeBuildsCuratedExtensionsShared keeps the recipe aligned with what
// internal PHP enables: each curated extension must be built as a loadable module
// (or be requested through a module flag) so filtering by ext/ does not drop it.
func TestPHPRecipeBuildsCuratedExtensionsShared(t *testing.T) {
	php, err := loadBuiltinManifest(PHP)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(php) error = %v", err)
	}
	configure := strings.Join(php.Build.Stages[0].Configure, " ")

	flagNames := map[string]string{"pdo_sqlite": "pdo-sqlite", "pdo_mysql": "pdo-mysql", "pdo_pgsql": "pdo-pgsql"}
	for _, extension := range broadInternalPHPExtensions {
		name := extension
		if flag, ok := flagNames[extension]; ok {
			name = flag
		}
		if !strings.Contains(configure, "-"+name+"=shared") {
			t.Fatalf("php recipe does not build %s as a shared module", extension)
		}
	}
}

// TestApacheRecipeLoadsEveryConfiguredModule verifies the recipe's verify step
// loads the modules, since a module that fails to resolve its symbols leaves httpd
// able to start and print its version.
func TestApacheRecipeLoadsEveryConfiguredModule(t *testing.T) {
	apache, err := loadBuiltinManifest(Apache)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(apache) error = %v", err)
	}

	loaded := ""
	for _, check := range apache.Build.Verify {
		loaded += strings.Join(check, " ")
	}
	for _, module := range []string{"mpm_event", "authz_core", "authz_host", "dir", "mime", "log_config", "rewrite", "proxy", "proxy_fcgi", "socache_shmcb", "ssl"} {
		if !strings.Contains(loaded, "LoadModule "+module+"_module modules/mod_"+module+".so") {
			t.Fatalf("apache verify does not load %s", module)
		}
	}
}
