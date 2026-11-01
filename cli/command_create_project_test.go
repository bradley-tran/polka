package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"polka/backend"
	"polka/config"
	"polka/plugins"
	"polka/service"
	"polka/tools"
)

// setupCreateProjectCache seeds the cache with a fake internal PHP host that
// scaffolds a CakePHP-shaped app when run (standing in for php executing
// composer.phar create-project), a fake composer PHAR and framework tool payloads.
func setupCreateProjectCache(t *testing.T, workingDir string, exitCode int) {
	t.Helper()

	cacheDir := filepath.Join(t.TempDir(), "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	writeCachedPHP(t, cacheDir, "8.4", fakeCakePHPCreateProjectComposerScriptWithExit(exitCode))
	writeCachedComposer(t, cacheDir, "2.8", []byte("composer phar\n"))
	writeCachedDatabaseTool(t, cacheDir, "mariadb", "11.8", true, true, true)
	registry := tools.NewDefaultRegistry()
	for tool, version := range map[string]string{"nodejs": "24", "nginx": "1.30", "mailpit": "1.30", "phpmyadmin": "5.2"} {
		candidate := registry.InstallCandidates(cacheDir, tool, version)[0]
		// Keep bin/ from being mistaken for an archive wrapper directory and
		// flattened when the executable is the fixture's only nested file.
		files := map[string][]byte{
			cachedToolRelativePath(t, cacheDir, tool, version, candidate): fakeToolScript(tool),
			"README": []byte("test tool payload\n"),
		}
		writeCachedArchivePayload(t, cacheDir, tool, version, files)
	}
	t.Setenv("POLKA_TEST_CREATE_PROJECT_DIR", filepath.Join(workingDir, "demo"))
	stubCreateProjectDatabase(t)
}

// stubCreateProjectDatabase exercises managed database provisioning without
// launching the placeholder server executables stored in the test cache.
func stubCreateProjectDatabase(t *testing.T) {
	t.Helper()
	oldInitialize, oldStart, oldPing := initializeDatabaseServerFunc, startDatabaseServerFunc, pingDatabaseAddressFunc
	t.Cleanup(func() {
		initializeDatabaseServerFunc, startDatabaseServerFunc, pingDatabaseAddressFunc = oldInitialize, oldStart, oldPing
	})
	initializeDatabaseServerFunc = func(spec dbServerSpec) error {
		return os.MkdirAll(spec.DataDir, 0o755)
	}
	startDatabaseServerFunc = func(spec dbServerSpec) (dbStartResult, error) {
		return dbStartResult{PID: os.Getpid()}, nil
	}
	pingDatabaseAddressFunc = func(string) bool { return false }
}

// createProjectRegistryFramework overrides only registry metadata. Delegated
// behavior makes losing the active registry during target init observable.
type createProjectRegistryFramework struct{ plugins.FrameworkPlugin }

func (createProjectRegistryFramework) ID() string { return "custom" }
func (p createProjectRegistryFramework) Defaults() config.Environment {
	defaults := p.FrameworkPlugin.Defaults()
	defaults.Framework = p.ID()
	return defaults
}
func (createProjectRegistryFramework) Detection() plugins.FrameworkDetection {
	return plugins.FrameworkDetection{PackagePrefixes: []string{"custom/"}}
}

// TestCreateProjectKeepsActiveRegistry checks detection, initialization and the
// post-hook all use the caller's framework registry instead of built-in globals.
func TestCreateProjectKeepsActiveRegistry(t *testing.T) {
	dir := t.TempDir()
	setupCreateProjectCache(t, dir, 0)
	chdirTest(t, dir)
	cake, ok := plugins.NewDefaultRegistry().Framework(plugins.CakePHP)
	if !ok {
		t.Fatal("missing built-in fixture")
	}
	registry, err := plugins.NewRegistry(nil, createProjectRegistryFramework{cake})
	if err != nil {
		t.Fatal(err)
	}
	store := backend.NewProjectStore(dir)
	store.Registry = registry
	store.Plugins = registry.ToolRegistry()
	var stdout, stderr bytes.Buffer
	if err := runCreateProject(&stdout, &stderr, store, []string{"custom/app", "demo"}); err != nil {
		t.Fatalf("create project: %v, %s", err, &stderr)
	}
	file := readTestConfigFile(t, filepath.Join(dir, "demo"))
	if file.Version != "0.2" || file.Environments[defaultEnvironmentName].Framework != "custom" {
		t.Fatalf("target config = %#v", file)
	}
	data, err := os.ReadFile(filepath.Join(dir, "demo", "config", "app_local.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "'username' => 'polka'") {
		t.Fatalf("target post-hook did not run: %s", data)
	}
}

// TestRunCreateProjectScaffoldsAndInitializesPolka verifies the happy path:
// internal php + composer are provisioned, the app is scaffolded, polka.yaml
// gets the detected framework preset, and the post-composer hook rewrites the
// framework's database config.
func TestRunCreateProjectScaffoldsAndInitializesPolka(t *testing.T) {
	workingDir := t.TempDir()
	setupCreateProjectCache(t, workingDir, 0)
	chdirTest(t, workingDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"create-project", "cakephp/app", "demo"}); code != 0 {
		t.Fatalf("Run(create-project) code = %d, stderr = %q", code, stderr.String())
	}

	targetDir := filepath.Join(workingDir, "demo")
	if !strings.Contains(stdout.String(), "Created cakephp project at "+targetDir) {
		t.Fatalf("Run(create-project) stdout = %q, want cakephp creation summary", stdout.String())
	}
	if !strings.Contains(stdout.String(), "cd demo && polka sh") {
		t.Fatalf("Run(create-project) stdout = %q, want next-step hint", stdout.String())
	}

	// polka.yaml carries the framework preset and the .polka state dir exists.
	config := readTestConfigFile(t, targetDir)
	environment := config.Environments[defaultEnvironmentName]
	if environment.Framework != "cakephp" {
		t.Fatalf("polka.yaml framework = %q, want cakephp", environment.Framework)
	}
	if environment.PHP == "" || environment.Composer == "" {
		t.Fatalf("polka.yaml tools = %#v, want framework preset versions", environment)
	}
	if _, err := os.Stat(filepath.Join(targetDir, ".polka")); err != nil {
		t.Fatalf("Stat(.polka) error = %v, want state dir initialized", err)
	}

	// The post-composer hook replaced the scaffolded SQLite datasource with
	// the managed database credentials.
	appLocal, err := os.ReadFile(filepath.Join(targetDir, "config", "app_local.php"))
	if err != nil {
		t.Fatalf("ReadFile(app_local.php) error = %v", err)
	}
	if !strings.Contains(string(appLocal), "'username' => 'polka'") {
		t.Fatalf("app_local.php = %q, want managed database credentials", string(appLocal))
	}
	if strings.Contains(string(appLocal), "Sqlite") {
		t.Fatalf("app_local.php = %q, want scaffolded SQLite datasource replaced", string(appLocal))
	}
}

// TestRunCreateProjectDefersLaravelHooks verifies the first Composer invocation
// cannot run scripts or install dependencies, and the second uses installed
// project tools, PHP config and database credentials. Explicit skip flags survive
// the continuation and disable database startup when no scripts will run.
func TestRunCreateProjectDefersLaravelHooks(t *testing.T) {
	for _, flags := range [][]string{nil, {"--no-scripts"}, {"--no-install"}, {"--no-scripts", "--no-install", "--no-dev"}} {
		t.Run(strings.Join(append([]string{"default"}, flags...), " "), func(t *testing.T) {
			workingDir := t.TempDir()
			targetDir := setupLaravelCreateProjectCache(t, workingDir)
			t.Setenv("POLKA_TEST_CREATE_PROJECT_SKIP_SCRIPTS", strconv.FormatBool(slices.Contains(flags, "--no-scripts")))
			t.Setenv("POLKA_TEST_CREATE_PROJECT_SKIP_INSTALL", strconv.FormatBool(slices.Contains(flags, "--no-install")))
			chdirTest(t, workingDir)
			var stdout, stderr bytes.Buffer
			args := append([]string{"create-project", "--stability", "stable", "laravel/laravel", "demo"}, flags...)
			if code := Run(&stdout, &stderr, args); code != 0 {
				t.Fatalf("create-project exit = %d, stderr = %s", code, &stderr)
			}
			data, err := os.ReadFile(filepath.Join(targetDir, "lifecycle.log"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Fields(string(data)); !slices.Equal(got, []string{"scaffold", "complete"}) {
				t.Fatalf("Composer phases = %q", got)
			}
			for name, want := range map[string]bool{
				"scripts-ran":            !slices.Contains(flags, "--no-scripts"),
				"dependencies-installed": !slices.Contains(flags, "--no-install"),
			} {
				_, err := os.Stat(filepath.Join(targetDir, name))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if (err == nil) != want {
					t.Fatalf("%s exists = %t, want %t", name, err == nil, want)
				}
			}
			config := readTestConfigFile(t, targetDir)
			if config.Environments[defaultEnvironmentName].Framework != "laravel" {
				t.Fatalf("target config = %#v", config)
			}
			_, err = os.Stat(service.DatabaseStatePath(filepath.Join(targetDir, ".polka"), defaultEnvironmentName))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if (err == nil) == slices.Contains(flags, "--no-scripts") {
				t.Fatalf("database started = %t with flags %q", err == nil, flags)
			}
			// Inspect the copy captured inside the Composer invocation, before
			// the final framework hook can hide an ordering regression.
			credentials, err := service.LoadManagedDatabaseCredentials(service.DatabaseCredentialStatePath(filepath.Join(targetDir, ".polka"), defaultEnvironmentName))
			if err != nil {
				t.Fatal(err)
			}
			variables, err := readEnvironmentFile(filepath.Join(targetDir, "migration.env"))
			if err != nil {
				t.Fatal(err)
			}
			migrationEnv := make(map[string]string, len(variables))
			for _, variable := range variables {
				migrationEnv[variable.Name] = variable.Value
			}
			for key, want := range map[string]string{
				"DB_CONNECTION": "mysql",
				"DB_HOST":       service.DatabaseListenHost,
				"DB_PORT":       strconv.Itoa(credentials.Port),
				"DB_DATABASE":   credentials.DatabaseName,
				"DB_USERNAME":   credentials.User,
				"DB_PASSWORD":   credentials.Password,
			} {
				if migrationEnv[key] != want {
					t.Fatalf("migration .env %s does not match managed database credentials", key)
				}
			}
			if credentials.Engine != "mariadb" || credentials.Version != "11.8" {
				t.Fatalf("migration database = %s:%s, want mariadb:11.8", credentials.Engine, credentials.Version)
			}
			dotenv, err := os.ReadFile(filepath.Join(targetDir, ".env"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(dotenv), "DB_CONNECTION=mysql") {
				t.Fatalf("Laravel post-hook did not update target .env: %s", dotenv)
			}
		})
	}
}

// setupLaravelCreateProjectCache prepares a strict Composer lifecycle fixture
// with known project executable paths, separate from the internal runtime.
func setupLaravelCreateProjectCache(t *testing.T, workingDir string) string {
	t.Helper()
	setupCreateProjectCache(t, workingDir, 0)
	writeCachedPHP(t, os.Getenv("POLKA_CACHE_DIR"), "8.4", fakeLaravelCreateProjectLifecyclePHP())
	targetDir := filepath.Join(workingDir, "demo")
	root := filepath.Join(targetDir, ".polka")
	t.Setenv("POLKA_TEST_CREATE_PROJECT_PHP", projectInstalledPHPPath(root, "8.4"))
	t.Setenv("POLKA_TEST_CREATE_PROJECT_INI", projectInstalledPHPConfigPath(root, "8.4"))
	t.Setenv("POLKA_TEST_CREATE_PROJECT_NODE", projectInstalledNodeJSPath(root, "24"))
	t.Setenv("POLKA_TEST_CREATE_PROJECT_SKIP_SCRIPTS", "false")
	t.Setenv("POLKA_TEST_CREATE_PROJECT_SKIP_INSTALL", "false")
	t.Setenv("POLKA_TEST_CREATE_PROJECT_EXIT", "0")
	return targetDir
}

// TestRunCreateProjectStopsBeforeHooksOnInstallFailure ensures a failed tool
// install leaves the scaffold available for repair without running Composer.
func TestRunCreateProjectStopsBeforeHooksOnInstallFailure(t *testing.T) {
	workingDir := t.TempDir()
	targetDir := setupLaravelCreateProjectCache(t, workingDir)
	// A valid cached archive without the executable fails materialization
	// deterministically, without involving the network.
	writeCachedArchivePayload(t, os.Getenv("POLKA_CACHE_DIR"), "nodejs", "24", map[string][]byte{"missing.txt": []byte("no node executable\n")})
	chdirTest(t, workingDir)
	var stdout, stderr bytes.Buffer
	if code := Run(&stdout, &stderr, []string{"create-project", "laravel/laravel", "demo"}); code != 1 {
		t.Fatalf("create-project exit = %d, want install failure; stderr = %s", code, &stderr)
	}
	data, err := os.ReadFile(filepath.Join(targetDir, "lifecycle.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "scaffold" || strings.Contains(stdout.String(), "Created laravel project") {
		t.Fatalf("Composer ran after install failure: log = %s, stdout = %s", data, &stdout)
	}
	if !strings.Contains(stderr.String(), "nodejs") {
		t.Fatalf("missing tool install error: %s", &stderr)
	}
}

// TestRunCreateProjectStopsBeforeHooksOnDatabaseFailure prevents migrations
// from running against an unavailable database or falling back to SQLite.
func TestRunCreateProjectStopsBeforeHooksOnDatabaseFailure(t *testing.T) {
	for _, stage := range []string{"initialize", "start"} {
		t.Run(stage, func(t *testing.T) {
			workingDir := t.TempDir()
			targetDir := setupLaravelCreateProjectCache(t, workingDir)
			databaseErr := errors.New("database unavailable")
			if stage == "initialize" {
				initializeDatabaseServerFunc = func(dbServerSpec) error { return databaseErr }
			} else {
				startDatabaseServerFunc = func(dbServerSpec) (dbStartResult, error) { return dbStartResult{}, databaseErr }
			}
			chdirTest(t, workingDir)
			var stdout, stderr bytes.Buffer
			if code := Run(&stdout, &stderr, []string{"create-project", "laravel/laravel", "demo"}); code != 1 {
				t.Fatalf("create-project exit = %d, want database failure", code)
			}
			data, err := os.ReadFile(filepath.Join(targetDir, "lifecycle.log"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(data)) != "scaffold" || strings.Contains(stdout.String(), "Created laravel project") {
				t.Fatalf("Composer ran after database failure: log = %s", data)
			}
			if !strings.Contains(stderr.String(), "prepare database for Composer scripts: database unavailable") {
				t.Fatalf("missing database setup error: %s", &stderr)
			}
		})
	}
}

// TestRunCreateProjectPropagatesCompletionFailure checks that failure in the
// deferred Composer phase keeps its exit code and does not report success.
func TestRunCreateProjectPropagatesCompletionFailure(t *testing.T) {
	workingDir := t.TempDir()
	setupLaravelCreateProjectCache(t, workingDir)
	t.Setenv("POLKA_TEST_CREATE_PROJECT_EXIT", "7")
	chdirTest(t, workingDir)
	var stdout, stderr bytes.Buffer
	if code := Run(&stdout, &stderr, []string{"create-project", "laravel/laravel", "demo"}); code != 7 {
		t.Fatalf("create-project exit = %d, want 7; stderr = %s", code, &stderr)
	}
	if strings.Contains(stdout.String(), "Created laravel project") || strings.Contains(stdout.String(), "Next steps:") {
		t.Fatalf("success reported after Composer failure: %s", &stdout)
	}
}

// TestSplitCreateProjectArgs verifies option values never become a package or
// directory, and continuation options preserve caller-supplied skip flags.
func TestSplitCreateProjectArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		positionals []string
		options     []string
	}{
		{name: "empty"},
		{name: "package", args: []string{"laravel/laravel"}, positionals: []string{"laravel/laravel"}},
		{
			name:        "separate values",
			args:        []string{"--repository", "https://example.test", "--stability", "dev", "laravel/laravel", "demo", "^12", "--prefer-install", "source", "--ignore-platform-req", "ext-zip", "--no-dev", "--no-scripts", "--no-install"},
			positionals: []string{"laravel/laravel", "demo", "^12"},
			options:     []string{"--prefer-install", "source", "--ignore-platform-req", "ext-zip", "--no-dev", "--no-scripts", "--no-install"},
		},
		{
			name:        "inline values",
			args:        []string{"-sdev", "-dparent", "--repository-url=https://example.test", "laravel/laravel", "demo", "--audit-format=json", "--prefer-dist", "--no-plugins", "-n", "-vvv"},
			positionals: []string{"laravel/laravel", "demo"},
			options:     []string{"--audit-format=json", "--prefer-dist", "--no-plugins", "-n", "-vvv"},
		},
		{
			name:        "root options",
			args:        []string{"-s", "stable", "--working-dir", "parent", "--add-repository", "--no-secure-http", "--keep-vcs", "--remove-vcs", "--ask", "laravel/laravel", "demo"},
			positionals: []string{"laravel/laravel", "demo"},
		},
		{name: "separator", args: []string{"--", "laravel/laravel", "--demo"}, positionals: []string{"laravel/laravel", "--demo"}},
		{name: "missing value", args: []string{"--prefer-install"}, options: []string{"--prefer-install"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			positionals, options := splitCreateProjectArgs(test.args)
			if !slices.Equal(positionals, test.positionals) || !slices.Equal(options, test.options) {
				t.Fatalf("split = %q, %q; want %q, %q", positionals, options, test.positionals, test.options)
			}
		})
	}
}

// fakeLaravelCreateProjectLifecyclePHP stands in for PHP running Composer. It
// rejects early hooks and verifies the runtime and tools at continuation time.
func fakeLaravelCreateProjectLifecyclePHP() []byte {
	if runtime.GOOS == "windows" {
		return []byte(strings.Join([]string{
			"@echo off",
			"if \"%~1\"==\"-nm\" exit /b 0",
			"if \"%~1\"==\"-m\" exit /b 0",
			"set \"dir=%POLKA_TEST_CREATE_PROJECT_DIR%\"",
			"if not \"%~2\"==\"create-project\" exit /b 81",
			"if \"%~3\"==\"--no-install\" goto scaffold",
			"if not \"%~3\"==\"--working-dir\" exit /b 82",
			"if not \"%~4\"==\"%dir%\" exit /b 82",
			"if not \"%~f0\"==\"%POLKA_TEST_CREATE_PROJECT_PHP%\" exit /b 83",
			"if not \"%PHPRC%\"==\"%POLKA_TEST_CREATE_PROJECT_INI%\" exit /b 84",
			"if not exist \"%POLKA_TEST_CREATE_PROJECT_NODE%\" exit /b 85",
			"if not exist \"%dir%\\.polka\\envs\\installed.json\" exit /b 86",
			"set \"managedPath=false\"",
			"for %%p in (\"%PATH:;=\" \"%\") do if /I \"%%~p\"==\"%dir%\\.polka\\bin\" set \"managedPath=true\"",
			"if \"%managedPath%\"==\"false\" exit /b 87",
			"set \"skipScripts=false\"",
			"set \"skipInstall=false\"",
			"for %%a in (%*) do if \"%%~a\"==\"--no-scripts\" set \"skipScripts=true\"",
			"for %%a in (%*) do if \"%%~a\"==\"--no-install\" set \"skipInstall=true\"",
			"if not \"%skipScripts%\"==\"%POLKA_TEST_CREATE_PROJECT_SKIP_SCRIPTS%\" exit /b 88",
			"if not \"%skipInstall%\"==\"%POLKA_TEST_CREATE_PROJECT_SKIP_INSTALL%\" exit /b 89",
			"if not \"%DB_CONNECTION%\"==\"mysql\" exit /b 91",
			"if not \"%DB_HOST%\"==\"127.0.0.1\" exit /b 92",
			"if not \"%DB_PORT%\"==\"3306\" exit /b 92",
			"if not \"%DB_DATABASE%\"==\"default\" exit /b 92",
			"if not \"%DB_USERNAME%\"==\"polka\" exit /b 92",
			"if \"%DB_PASSWORD%\"==\"\" exit /b 92",
			"if not exist \"%dir%\\.env\" exit /b 93",
			"if \"%skipScripts%\"==\"false\" if not exist \"%dir%\\.polka\\run\\db\\default.json\" exit /b 94",
			"copy /y \"%dir%\\.env\" \"%dir%\\migration.env\" >nul || exit /b 95",
			"if \"%skipScripts%\"==\"false\" echo done>\"%dir%\\scripts-ran\"",
			"if \"%skipInstall%\"==\"false\" echo done>\"%dir%\\dependencies-installed\"",
			"echo complete>>\"%dir%\\lifecycle.log\"",
			"exit /b %POLKA_TEST_CREATE_PROJECT_EXIT%",
			":scaffold",
			"if not \"%~4\"==\"--no-scripts\" exit /b 90",
			"mkdir \"%dir%\" >nul 2>nul",
			"echo scaffold>\"%dir%\\lifecycle.log\"",
			"echo {}>\"%dir%\\composer.json\"",
			"echo php>\"%dir%\\artisan\"",
			"echo DB_CONNECTION=sqlite>\"%dir%\\.env.example\"",
			"exit /b 0",
			"",
		}, "\r\n"))
	}
	return []byte(`#!/usr/bin/env sh
set -eu
case "$1" in -nm|-m) exit 0 ;; esac
dir=${POLKA_TEST_CREATE_PROJECT_DIR:?}
[ "$2" = create-project ] || exit 81
if [ "$3" = --no-install ]; then
    [ "$4" = --no-scripts ] || exit 90
    mkdir -p "$dir"
    echo scaffold > "$dir/lifecycle.log"
    echo '{}' > "$dir/composer.json"
    echo php > "$dir/artisan"
    echo DB_CONNECTION=sqlite > "$dir/.env.example"
    exit 0
fi
[ "$3" = --working-dir ] || exit 82
[ "$4" = "$dir" ] || exit 82
[ "$0" = "$POLKA_TEST_CREATE_PROJECT_PHP" ] || exit 83
[ "$PHPRC" = "$POLKA_TEST_CREATE_PROJECT_INI" ] || exit 84
[ -f "$POLKA_TEST_CREATE_PROJECT_NODE" ] || exit 85
[ -f "$dir/.polka/envs/installed.json" ] || exit 86
case "$PATH" in "$dir/.polka/bin:"*) ;; *) exit 87 ;; esac
skipScripts=false
skipInstall=false
for arg in "$@"; do
    case "$arg" in
        --no-scripts) skipScripts=true ;;
        --no-install) skipInstall=true ;;
    esac
done
[ "$skipScripts" = "$POLKA_TEST_CREATE_PROJECT_SKIP_SCRIPTS" ] || exit 88
[ "$skipInstall" = "$POLKA_TEST_CREATE_PROJECT_SKIP_INSTALL" ] || exit 89
[ "${DB_CONNECTION:-}" = mysql ] || exit 91
[ "${DB_HOST:-}" = 127.0.0.1 ] || exit 92
[ "${DB_PORT:-}" = 3306 ] || exit 92
[ "${DB_DATABASE:-}" = default ] || exit 92
[ "${DB_USERNAME:-}" = polka ] || exit 92
[ -n "${DB_PASSWORD:-}" ] || exit 92
[ -f "$dir/.env" ] || exit 93
if [ "$skipScripts" = false ]; then
    [ -f "$dir/.polka/run/db/default.json" ] || exit 94
fi
cp "$dir/.env" "$dir/migration.env"
if [ "$skipScripts" = false ]; then echo done > "$dir/scripts-ran"; fi
if [ "$skipInstall" = false ]; then echo done > "$dir/dependencies-installed"; fi
echo complete >> "$dir/lifecycle.log"
exit "$POLKA_TEST_CREATE_PROJECT_EXIT"
`)
}

// fakeFailingCreateProjectHostPHPScript builds a fake internal php that
// answers module listings normally (so internal PHP provisioning succeeds)
// but fails any other invocation, standing in for a failing composer run.
func fakeFailingCreateProjectHostPHPScript(exitCode int) []byte {
	code := strconv.Itoa(exitCode)
	if runtime.GOOS == "windows" {
		return []byte(strings.Join([]string{
			"@echo off",
			"if \"%1\"==\"-nm\" exit /b 0",
			"if \"%1\"==\"-m\" exit /b 0",
			"echo composer failure 1>&2",
			"exit /b " + code,
			"",
		}, "\r\n"))
	}

	return []byte("#!/usr/bin/env sh\ncase \"$1\" in\n-nm|-m) exit 0 ;;\nesac\necho 'composer failure' >&2\nexit " + code + "\n")
}

// TestRunCreateProjectPropagatesComposerFailure verifies a composer failure
// exits with composer's code and leaves no polka.yaml behind.
func TestRunCreateProjectPropagatesComposerFailure(t *testing.T) {
	workingDir := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	writeCachedPHP(t, cacheDir, "8.4", fakeFailingCreateProjectHostPHPScript(3))
	writeCachedComposer(t, cacheDir, "2.8", []byte("composer phar\n"))
	chdirTest(t, workingDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"create-project", "cakephp/app", "demo"}); code != 3 {
		t.Fatalf("Run(create-project) code = %d, want composer exit code 3", code)
	}
	if _, err := os.Stat(filepath.Join(workingDir, "demo", "polka.yaml")); !os.IsNotExist(err) {
		t.Fatalf("Stat(polka.yaml) error = %v, want no polka init after composer failure", err)
	}
}

// TestRunCreateProjectWithoutDetectedFramework verifies unknown packages fall
// back to a plain polka init with a hint.
func TestRunCreateProjectWithoutDetectedFramework(t *testing.T) {
	workingDir := t.TempDir()
	setupCreateProjectCache(t, workingDir, 0)
	// The fake scaffolds a CakePHP layout; point the scaffold somewhere the
	// marker checks don't find framework files for the acme package.
	t.Setenv("POLKA_TEST_CREATE_PROJECT_DIR", filepath.Join(workingDir, "blog", "unrelated"))
	chdirTest(t, workingDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"create-project", "acme/blog"}); code != 0 {
		t.Fatalf("Run(create-project) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no framework detected") {
		t.Fatalf("Run(create-project) stdout = %q, want no-framework hint", stdout.String())
	}

	config := readTestConfigFile(t, filepath.Join(workingDir, "blog"))
	if framework := config.Environments[defaultEnvironmentName].Framework; framework != "" {
		t.Fatalf("polka.yaml framework = %q, want empty", framework)
	}
}

// TestRunCreateProjectRequiresPackage verifies the package argument is
// mandatory.
func TestRunCreateProjectRequiresPackage(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"create-project"}); code == 0 {
		t.Fatal("Run(create-project) code = 0, want failure without package")
	}
	if !strings.Contains(stderr.String(), "composer package") {
		t.Fatalf("Run(create-project) stderr = %q, want package requirement", stderr.String())
	}
}
