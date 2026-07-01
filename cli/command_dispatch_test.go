package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunConfigSetsVersionLabelsAndDispatchesPhp(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())
	writeCachedComposer(t, cacheDir, "2.8", []byte("composer\n"))

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")
	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.composer", "2.8")

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	if environment.PHP != "8.4" || environment.Composer != "2.8" {
		t.Fatalf("environment = %#v, want version labels for demo", environment)
	}
	configData, err := os.ReadFile(testEnvironmentConfigPath(projectDir, "demo"))
	if err != nil {
		t.Fatalf("ReadFile(demo config) error = %v", err)
	}
	if strings.Contains(string(configData), fakePHP) {
		t.Fatalf("config contents = %q, want versions rather than paths", string(configData))
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(projectInstalledPHPPath(root, "8.4")); err != nil {
		t.Fatalf("Stat(installed php) error = %v", err)
	}
	installOutput := stdout.String()
	expectedProgress := []string{
		"php 8.4: using cache",
		"php 8.4: installing",
		"php 8.4: configuring",
		"php 8.4: installed",
		"composer 2.8: using cache",
		"composer 2.8: installing",
		"composer 2.8: installed",
		"Installed 'demo' environment",
	}
	for _, expected := range expectedProgress {
		if !strings.Contains(installOutput, expected) {
			t.Fatalf("Run(install) stdout = %q, want %q", installOutput, expected)
		}
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "dispatch", "php", "-v", "--ini"}); code != 0 {
		t.Fatalf("Run(dispatch) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "fake-php -v --ini") {
		t.Fatalf("Run(dispatch) output = %q, want forwarded arguments", output)
	}
}

func TestRunDispatchFallsBackToFrankenPHPBundledPHP(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedFrankenPHP(t, cacheDir, "1.12")
	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {FrankenPHP: "1.12"},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")

	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install FrankenPHP) code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "bin", "php.cmd")); err != nil {
		t.Fatalf("Stat(php shim) error = %v", err)
	}
	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "dispatch", "php", "-v"}); code != 0 {
		t.Fatalf("Run(dispatch php fallback) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "fake-frankenphp-php -v") {
		t.Fatalf("Run(dispatch php fallback) stdout = %q, want bundled FrankenPHP CLI", stdout.String())
	}
}

func TestRunDispatchLoadsProjectAndConfiguredEnvironmentVariables(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScriptWithEnv("APP_ENV"))
	if err := os.MkdirAll(filepath.Join(projectDir, "config"), 0o755); err != nil {
		t.Fatalf("MkdirAll(config) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte("APP_ENV=project\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(project .env) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "config", ".env.local"), []byte("APP_ENV=file\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(env-file) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				EnvFile: "config/.env.local",
				EnvVars: map[string]string{"APP_ENV": "config"},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")

	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "dispatch", "php", "-v"}); code != 0 {
		t.Fatalf("Run(dispatch) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "fake-php -v config") {
		t.Fatalf("Run(dispatch) output = %q, want env-vars to override env-file and project .env", output)
	}
}

func TestRunDispatchRunsPostComposerHookForLaravelInstall(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedComposerExecutable(t, cacheDir, "2.8", fakeToolScript("composer"))
	appRoot := filepath.Join(projectDir, "laravel")
	if err := os.MkdirAll(filepath.Join(appRoot, "public"), 0o755); err != nil {
		t.Fatalf("MkdirAll(app public) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(appRoot, ".env"), []byte("APP_NAME=Laravel\nDB_CONNECTION=sqlite\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			defaultEnvironmentName: {
				Framework: "laravel",
				Composer:  "2.8",
				MariaDB:   "11.8",
				Docroot:   "laravel/public",
				Database:  &testDatabaseConfig{Engine: "mariadb", Version: "11.8", Port: 3307},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)

	if code := Run(stdout, stderr, []string{"--root", root, "install", "composer:2.8"}); code != 0 {
		t.Fatalf("Run(install composer) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "dispatch", "composer", "install"}); code != 0 {
		t.Fatalf("Run(dispatch composer install) code = %d, stderr = %q", code, stderr.String())
	}

	updated, err := os.ReadFile(filepath.Join(appRoot, ".env"))
	if err != nil {
		t.Fatalf("ReadFile(.env) error = %v", err)
	}
	text := string(updated)
	for _, expected := range []string{
		"DB_CONNECTION=mysql",
		"DB_HOST=127.0.0.1",
		"DB_PORT=3307",
		"DB_DATABASE=default",
		"DB_USERNAME=polka",
		"DB_PASSWORD=",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf(".env = %q, want %q", text, expected)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "secrets", "db", "default.json")); err != nil {
		t.Fatalf("Stat(database credentials) error = %v", err)
	}
}

func TestRunDispatchRunsPostComposerHookForCakePHPCreateProject(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	appRoot := filepath.Join(projectDir, "cake")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	t.Setenv("POLKA_TEST_CREATE_PROJECT_DIR", appRoot)
	chdirTest(t, projectDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedComposerExecutable(t, cacheDir, "2.8", fakeCakePHPCreateProjectComposerScript())
	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			defaultEnvironmentName: {
				Framework: "cakephp",
				Composer:  "2.8",
				MariaDB:   "11.8",
				Docroot:   "cake/webroot",
				Database:  &testDatabaseConfig{Engine: "mariadb", Version: "11.8", Port: 3307},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)

	if code := Run(stdout, stderr, []string{"--root", root, "install", "composer:2.8"}); code != 0 {
		t.Fatalf("Run(install composer) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "dispatch", "composer", "create-project", "cakephp/app", "cake"}); code != 0 {
		t.Fatalf("Run(dispatch composer create-project) code = %d, stderr = %q", code, stderr.String())
	}

	assertCakePHPAppLocalUsesManagedDatabase(t, filepath.Join(appRoot, "config", "app_local.php"), "3307")
}

func TestRunDispatchRunsPostComposerHookExceptComposerExitTwo(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		wantHook bool
	}{
		{name: "generic failure", exitCode: 1, wantHook: true},
		{name: "dependency solver failure", exitCode: 2, wantHook: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectDir := t.TempDir()
			root := filepath.Join(projectDir, ".polka")
			cacheDir := filepath.Join(projectDir, "global-cache")
			appRoot := filepath.Join(projectDir, "cake")
			t.Setenv("POLKA_CACHE_DIR", cacheDir)
			t.Setenv("POLKA_TEST_CREATE_PROJECT_DIR", appRoot)
			chdirTest(t, projectDir)
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			writeCachedComposerExecutable(t, cacheDir, "2.8", fakeCakePHPCreateProjectComposerScriptWithExit(test.exitCode))
			config := testConfigFile{
				Version: 1,
				Root:    ".polka",
				Environments: map[string]testEnvironmentConfig{
					defaultEnvironmentName: {
						Framework: "cakephp",
						Composer:  "2.8",
						MariaDB:   "11.8",
						Docroot:   "cake/webroot",
						Database:  &testDatabaseConfig{Engine: "mariadb", Version: "11.8", Port: 3307},
					},
				},
			}
			writeTestConfigFile(t, projectDir, config)

			if code := Run(stdout, stderr, []string{"--root", root, "install", "composer:2.8"}); code != 0 {
				t.Fatalf("Run(install composer) code = %d, stderr = %q", code, stderr.String())
			}
			stdout.Reset()
			stderr.Reset()

			if code := Run(stdout, stderr, []string{"--root", root, "dispatch", "composer", "create-project", "cakephp/app", "cake"}); code != test.exitCode {
				t.Fatalf("Run(dispatch composer create-project) code = %d, want %d, stderr = %q", code, test.exitCode, stderr.String())
			}

			appLocalPath := filepath.Join(appRoot, "config", "app_local.php")
			if test.wantHook {
				assertCakePHPAppLocalUsesManagedDatabase(t, appLocalPath, "3307")
				return
			}

			data, err := os.ReadFile(appLocalPath)
			if err != nil {
				t.Fatalf("ReadFile(app_local.php) error = %v", err)
			}
			text := string(data)
			if !strings.Contains(text, "'driver' => 'Cake\\Database\\Driver\\Sqlite'") {
				t.Fatalf("app_local.php = %q, want original SQLite config", text)
			}
			if strings.Contains(text, "'driver' => 'Cake\\\\Database\\\\Driver\\\\Mysql'") {
				t.Fatalf("app_local.php = %q, want post-composer hook skipped", text)
			}
		})
	}
}

func TestRunDispatchRunsPIEPHARThroughManagedPHP(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())
	writeCachedPIE(t, cacheDir, "1.4", []byte("pie phar\n"))

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP: "8.4",
				PIE: "1.4",
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")

	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "dispatch", "pie", "install", "asgrim/example-pie-extension"}); code != 0 {
		t.Fatalf("Run(dispatch pie) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "fake-php") || !strings.Contains(output, "pie.phar install asgrim/example-pie-extension") {
		t.Fatalf("Run(dispatch pie) stdout = %q, want pie.phar executed through php", output)
	}
}

func TestRunDispatchUsesNodeAliasesAndRejectsNodeJSKey(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.nodejs", "24")
	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	for _, command := range []string{"node", "npm", "npx"} {
		path := projectInstalledNodeJSCommandPath(root, "24", command)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, fakeToolScript(command), 0o755); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	for _, command := range []string{"node", "npm", "npx"} {
		stdout.Reset()
		stderr.Reset()
		if code := Run(stdout, stderr, []string{"--root", root, "dispatch", command, "--version"}); code != 0 {
			t.Fatalf("Run(dispatch %s) code = %d, stderr = %q", command, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "fake-"+command+" --version") {
			t.Fatalf("Run(dispatch %s) stdout = %q, want forwarded arguments", command, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "dispatch", "nodejs", "--version"}); code == 0 {
		t.Fatal("Run(dispatch nodejs) code = 0, want unsupported tool error")
	}
	if !strings.Contains(stderr.String(), "unsupported tool \"nodejs\"") {
		t.Fatalf("Run(dispatch nodejs) stderr = %q, want unsupported tool error", stderr.String())
	}
}
