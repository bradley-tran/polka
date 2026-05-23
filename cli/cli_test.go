package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
)

type testConfigFile struct {
	Version      int                              `yaml:"version,omitempty"`
	Root         string                           `yaml:"root,omitempty"`
	Current      string                           `yaml:"current,omitempty"`
	Environments map[string]testEnvironmentConfig `yaml:"environments"`
}

type testEnvironmentConfig struct {
	PHP           string              `yaml:"php"`
	Composer      string              `yaml:"composer"`
	Database      *testDatabaseConfig `yaml:"database,omitempty"`
	PHPExtensions map[string]bool     `yaml:"php-extensions,omitempty"`
	Server        *testServerConfig   `yaml:"server,omitempty"`
}

type testDatabaseConfig struct {
	Engine  string `yaml:"engine,omitempty"`
	Version string `yaml:"version,omitempty"`
	Port    int    `yaml:"port,omitempty"`
}

type testServerConfig struct {
	Hostname string `yaml:"hostname,omitempty"`
	Port     int    `yaml:"port,omitempty"`
}

func TestRunConfigSetsVersionLabelsAndDispatchesPhp(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	fakeComposer := cachedComposerPath(cacheDir, "2.8")
	if err := os.MkdirAll(filepath.Dir(fakeComposer), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache composer) error = %v", err)
	}
	if err := os.WriteFile(fakeComposer, []byte("composer\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache composer) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4", "--composer", "2.8"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	if config.Environments["demo"].PHP != "8.4" || config.Environments["demo"].Composer != "2.8" {
		t.Fatalf("config = %#v, want version labels for demo", config)
	}
	if strings.Contains(string(configData), fakePHP) {
		t.Fatalf("config contents = %q, want versions rather than paths", string(configData))
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(projectInstalledPHPPath(root, "8.4")); err != nil {
		t.Fatalf("Stat(installed php) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "Installed demo") {
		t.Fatalf("Run(install) stdout = %q, want install summary", stdout.String())
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

func TestRunInitUsesDotPolkaByDefault(t *testing.T) {
	projectDir := t.TempDir()
	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})

	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"init"}); code != 0 {
		t.Fatalf("Run(init) code = %d, stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "php.cmd")); err != nil {
		t.Fatalf("Stat(.polka/bin/php.cmd) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "mysql.cmd")); err != nil {
		t.Fatalf("Stat(.polka/bin/mysql.cmd) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "mariadb.cmd")); err != nil {
		t.Fatalf("Stat(.polka/bin/mariadb.cmd) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "polka.exe")); err != nil {
		t.Fatalf("Stat(.polka/bin/polka.exe) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "polka.yaml")); err != nil {
		t.Fatalf("Stat(polka.yaml) error = %v", err)
	}
	if !strings.Contains(stdout.String(), filepath.Join(projectDir, ".polka")) {
		t.Fatalf("Run(init) stdout = %q, want .polka path", stdout.String())
	}
}

func TestRunNewUsesDefaultVersions(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "new", "demo"}); code != 0 {
		t.Fatalf("Run(new) code = %d, stderr = %q", code, stderr.String())
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	if config.Environments["demo"].PHP != "8.4" || config.Environments["demo"].Composer != "2.8" {
		t.Fatalf("config = %#v, want default versions for demo", config)
	}
	if !strings.Contains(stdout.String(), "Created demo") {
		t.Fatalf("Run(new) stdout = %q, want created summary", stdout.String())
	}
}

func TestRunConfigPersistsDatabaseSettings(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--db-engine", "mysql", "--db-version", "8.0", "--db-port", "3306"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	stored := config.Environments["demo"].Database
	if stored == nil {
		t.Fatalf("config = %#v, want database config for demo", config)
	}
	if stored.Engine != "mysql" || stored.Version != "8.0" || stored.Port != 3306 {
		t.Fatalf("database = %#v, want mysql 8.0 on port 3306", stored)
	}
	if !strings.Contains(stdout.String(), "db=mysql:8.0@3306") {
		t.Fatalf("Run(config) stdout = %q, want database summary", stdout.String())
	}
}

func TestRunDBDispatchesConfiguredDatabaseTool(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakeMySQL := cachedDatabasePath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQL), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysql) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQL, fakeDatabaseScript("mysql"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysql) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--db-engine", "mysql", "--db-version", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "--version"}); code != 0 {
		t.Fatalf("Run(db) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "fake-mysql") || !strings.Contains(output, "--version") || !strings.Contains(output, "--protocol=tcp") || !strings.Contains(output, "--host=127.0.0.1") || !strings.Contains(output, "--port=3306") {
		t.Fatalf("Run(db) output = %q, want injected managed database connection arguments", output)
	}
}

func TestRunDBPreservesExplicitConnectionArguments(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakeMySQL := cachedDatabasePath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQL), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysql) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQL, fakeDatabaseScript("mysql"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysql) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--db-engine", "mysql", "--db-version", "8.4", "--db-port", "3307"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "--host=db.internal", "--port=4406", "--protocol=tcp", "--version"}); code != 0 {
		t.Fatalf("Run(db explicit connection) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "--host=db.internal") || !strings.Contains(output, "--port=4406") || !strings.Contains(output, "--protocol=tcp") {
		t.Fatalf("Run(db explicit connection) output = %q, want explicit connection arguments forwarded", output)
	}
	if strings.Contains(output, "--host=127.0.0.1") || strings.Contains(output, "--port=3307") {
		t.Fatalf("Run(db explicit connection) output = %q, want injected defaults suppressed", output)
	}
}

func TestRunDBClientSubcommandDispatchesReservedWord(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakeMySQL := cachedDatabasePath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQL), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysql) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQL, fakeDatabaseScript("mysql"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysql) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--db-engine", "mysql", "--db-version", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "client", "status"}); code != 0 {
		t.Fatalf("Run(db client) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "fake-mysql") || !strings.Contains(output, " status") || !strings.Contains(output, "--host=127.0.0.1") || !strings.Contains(output, "--port=3306") {
		t.Fatalf("Run(db client) output = %q, want reserved word forwarded with managed connection defaults", output)
	}
}

func TestRunDBLifecycleSubcommandsManageState(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakeMySQL := cachedDatabasePath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQL), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysql) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQL, fakeDatabaseScript("mysql"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysql) error = %v", err)
	}
	fakeMySQLServer := cachedDatabaseServerPath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQLServer), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysqld) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQLServer, fakeDatabaseScript("mysqld"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysqld) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--db-engine", "mysql", "--db-version", "8.4", "--db-port", "3307"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	oldInitialize := initializeDatabaseServerFunc
	oldStart := startDatabaseServerFunc
	oldStop := stopDatabaseServerFunc
	oldPing := pingDatabaseAddressFunc
	oldNow := dbNowFunc
	t.Cleanup(func() {
		initializeDatabaseServerFunc = oldInitialize
		startDatabaseServerFunc = oldStart
		stopDatabaseServerFunc = oldStop
		pingDatabaseAddressFunc = oldPing
		dbNowFunc = oldNow
	})

	running := map[string]bool{}
	initialized := 0
	started := 0
	stopped := 0
	var startedSpec dbServerSpec
	var stoppedState dbRuntimeState

	initializeDatabaseServerFunc = func(spec dbServerSpec) error {
		initialized++
		if err := os.MkdirAll(spec.DataDir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(spec.DataDir, "initialized.txt"), []byte("ok\n"), 0o644)
	}
	startDatabaseServerFunc = func(spec dbServerSpec) (dbStartResult, error) {
		started++
		startedSpec = spec
		running[databaseAddress(spec.Port)] = true
		return dbStartResult{PID: 4242}, nil
	}
	stopDatabaseServerFunc = func(state dbRuntimeState) error {
		stopped++
		stoppedState = state
		running[databaseAddress(state.Port)] = false
		return nil
	}
	pingDatabaseAddressFunc = func(address string) bool {
		return running[address]
	}
	dbNowFunc = func() time.Time {
		return time.Date(2026, time.May, 23, 12, 0, 0, 0, time.UTC)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "start"}); code != 0 {
		t.Fatalf("Run(db start) code = %d, stderr = %q", code, stderr.String())
	}
	if initialized != 1 || started != 1 {
		t.Fatalf("initialize/start counts = %d/%d, want 1/1", initialized, started)
	}
	if startedSpec.Port != 3307 {
		t.Fatalf("started port = %d, want 3307", startedSpec.Port)
	}
	if !strings.Contains(stdout.String(), "Started mysql") {
		t.Fatalf("Run(db start) stdout = %q, want start summary", stdout.String())
	}

	state, err := loadDatabaseState(databaseStatePath(root, "demo"))
	if err != nil {
		t.Fatalf("loadDatabaseState() error = %v", err)
	}
	if state.PID != 4242 || state.Port != 3307 || state.Engine != "mysql" {
		t.Fatalf("database state = %#v, want mysql pid 4242 on port 3307", state)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "status"}); code != 0 {
		t.Fatalf("Run(db status) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "is running") || !strings.Contains(stdout.String(), "3307") {
		t.Fatalf("Run(db status) stdout = %q, want running status on configured port", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "stop"}); code != 0 {
		t.Fatalf("Run(db stop) code = %d, stderr = %q", code, stderr.String())
	}
	if stopped != 1 {
		t.Fatalf("stop count = %d, want 1", stopped)
	}
	if stoppedState.PID != 4242 {
		t.Fatalf("stopped state pid = %d, want 4242", stoppedState.PID)
	}
	if !strings.Contains(stdout.String(), "Stopped mysql") {
		t.Fatalf("Run(db stop) stdout = %q, want stop summary", stdout.String())
	}
	if _, err := os.Stat(databaseStatePath(root, "demo")); !os.IsNotExist(err) {
		t.Fatalf("Stat(database state) error = %v, want not exists", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "db", "status"}); code != 0 {
		t.Fatalf("Run(db status after stop) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "is stopped") || !strings.Contains(stdout.String(), "3307") {
		t.Fatalf("Run(db status after stop) stdout = %q, want stopped status on configured port", stdout.String())
	}
}

func TestRunInstallAppliesPHPExtensionsFromConfigFile(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, "php", "8.4", "ext"), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache ext) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	configPath := filepath.Join(projectDir, "polka.yaml")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	environment := config.Environments["demo"]
	environment.PHPExtensions = map[string]bool{"openssl": true, "xdebug": false}
	config.Environments["demo"] = environment
	updatedConfig, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	updatedConfig = append(updatedConfig, '\n')
	if err := os.WriteFile(configPath, updatedConfig, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	phpIniData, err := os.ReadFile(projectInstalledPHPConfigPath(root, "8.4"))
	if err != nil {
		t.Fatalf("ReadFile(installed php.ini) error = %v", err)
	}
	phpIni := string(phpIniData)
	if !strings.Contains(phpIni, "extension=openssl") {
		t.Fatalf("php.ini = %q, want enabled openssl extension", phpIni)
	}
	if !strings.Contains(phpIni, ";extension=xdebug") {
		t.Fatalf("php.ini = %q, want disabled xdebug extension", phpIni)
	}
	if !strings.Contains(stdout.String(), "Installed demo") {
		t.Fatalf("Run(install) stdout = %q, want install summary", stdout.String())
	}
}

func TestRunInstallEnablesComposerPHPExtensionsByDefault(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	fakeComposer := cachedComposerPath(cacheDir, "2.8")
	if err := os.MkdirAll(filepath.Dir(fakeComposer), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache composer) error = %v", err)
	}
	if err := os.WriteFile(fakeComposer, []byte("composer\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache composer) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, "php", "8.4", "ext"), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache ext) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4", "--composer", "2.8"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	phpIniData, err := os.ReadFile(projectInstalledPHPConfigPath(root, "8.4"))
	if err != nil {
		t.Fatalf("ReadFile(installed php.ini) error = %v", err)
	}
	phpIni := string(phpIniData)
	if !strings.Contains(phpIni, "extension=openssl") {
		t.Fatalf("php.ini = %q, want enabled openssl extension", phpIni)
	}
	if !strings.Contains(phpIni, "extension=zip") {
		t.Fatalf("php.ini = %q, want enabled zip extension", phpIni)
	}
}

func TestRunServeUsesCurrentServerConfig(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	docroot := filepath.Join(projectDir, "named-project", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}
	configPath := filepath.Join(projectDir, "polka.yaml")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	environment := config.Environments["demo"]
	environment.Server = &testServerConfig{Hostname: "localhost", Port: 8080}
	config.Environments["demo"] = environment
	updatedConfig, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	updatedConfig = append(updatedConfig, '\n')
	if err := os.WriteFile(configPath, updatedConfig, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "serve", filepath.Join("named-project", "public")}); code != 0 {
		t.Fatalf("Run(serve) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "-S localhost:8080") {
		t.Fatalf("Run(serve) output = %q, want configured server address", output)
	}
	if !strings.Contains(output, "-t "+docroot) {
		t.Fatalf("Run(serve) output = %q, want resolved docroot", output)
	}
}

func TestRunServeAllowsServerOverride(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}
	configPath := filepath.Join(projectDir, "polka.yaml")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	environment := config.Environments["demo"]
	environment.Server = &testServerConfig{Hostname: "localhost", Port: 8080}
	config.Environments["demo"] = environment
	updatedConfig, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	updatedConfig = append(updatedConfig, '\n')
	if err := os.WriteFile(configPath, updatedConfig, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "serve", "--server", "127.0.0.1:9001", filepath.Join("site", "public")}); code != 0 {
		t.Fatalf("Run(serve) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "-S 127.0.0.1:9001") {
		t.Fatalf("Run(serve) output = %q, want overridden server address", output)
	}
	if strings.Contains(output, "-S localhost:8080") {
		t.Fatalf("Run(serve) output = %q, want CLI override to win", output)
	}
	if !strings.Contains(output, "-t "+docroot) {
		t.Fatalf("Run(serve) output = %q, want resolved docroot", output)
	}
}

func TestRunServeStartsConfiguredDatabaseBeforePhp(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	fakeMySQL := cachedDatabasePath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQL), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysql) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQL, fakeDatabaseScript("mysql"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysql) error = %v", err)
	}
	fakeMySQLServer := cachedDatabaseServerPath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQLServer), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysqld) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQLServer, fakeDatabaseScript("mysqld"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysqld) error = %v", err)
	}
	docroot := filepath.Join(projectDir, "site", "public")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatalf("MkdirAll(docroot) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4", "--db-engine", "mysql", "--db-version", "8.4", "--db-port", "3307"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	oldInitialize := initializeDatabaseServerFunc
	oldStart := startDatabaseServerFunc
	oldPing := pingDatabaseAddressFunc
	oldNow := dbNowFunc
	t.Cleanup(func() {
		initializeDatabaseServerFunc = oldInitialize
		startDatabaseServerFunc = oldStart
		pingDatabaseAddressFunc = oldPing
		dbNowFunc = oldNow
	})

	running := map[string]bool{}
	initialized := 0
	started := 0
	var startedSpec dbServerSpec
	initializeDatabaseServerFunc = func(spec dbServerSpec) error {
		initialized++
		if err := os.MkdirAll(spec.DataDir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(spec.DataDir, "initialized.txt"), []byte("ok\n"), 0o644)
	}
	startDatabaseServerFunc = func(spec dbServerSpec) (dbStartResult, error) {
		started++
		startedSpec = spec
		running[databaseAddress(spec.Port)] = true
		return dbStartResult{PID: 7878}, nil
	}
	pingDatabaseAddressFunc = func(address string) bool {
		return running[address]
	}
	dbNowFunc = func() time.Time {
		return time.Date(2026, time.May, 23, 15, 0, 0, 0, time.UTC)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "serve", filepath.Join("site", "public")}); code != 0 {
		t.Fatalf("Run(serve with db) code = %d, stderr = %q", code, stderr.String())
	}

	if initialized != 1 || started != 1 {
		t.Fatalf("initialize/start counts = %d/%d, want 1/1", initialized, started)
	}
	if startedSpec.Port != 3307 {
		t.Fatalf("started port = %d, want 3307", startedSpec.Port)
	}
	output := stdout.String()
	if !strings.Contains(output, "fake-php") || !strings.Contains(output, "-t "+docroot) {
		t.Fatalf("Run(serve with db) output = %q, want php command output after database start", output)
	}
	state, err := loadDatabaseState(databaseStatePath(root, "demo"))
	if err != nil {
		t.Fatalf("loadDatabaseState() error = %v", err)
	}
	if state.PID != 7878 || state.Port != 3307 {
		t.Fatalf("database state = %#v, want pid 7878 on port 3307", state)
	}
}

func TestRunCreateIsUnknownCommand(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"create", "demo"}); code != 2 {
		t.Fatalf("Run(create) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command \"create\"") {
		t.Fatalf("Run(create) stderr = %q, want unknown command message", stderr.String())
	}
}

func cachedPHPPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "php", version, "bin", "php.cmd")
	}

	return filepath.Join(root, "php", version, "bin", "php")
}

func cachedComposerPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "composer", version, "bin", "composer.cmd")
	}

	return filepath.Join(root, "composer", version, "bin", "composer.phar")
}

func cachedDatabasePath(root, tool, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, tool, version, "bin", tool+".cmd")
	}

	return filepath.Join(root, tool, version, "bin", tool)
}

func cachedDatabaseServerPath(root, tool, version string) string {
	serverName := "mysqld"
	if tool == "mariadb" {
		serverName = "mariadbd"
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(root, tool, version, "bin", serverName+".exe")
	}

	return filepath.Join(root, tool, version, "bin", serverName)
}

func projectInstalledPHPPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "envs", "php", version, "bin", "php.cmd")
	}

	return filepath.Join(root, "envs", "php", version, "bin", "php")
}

func projectInstalledPHPConfigPath(root, version string) string {
	return filepath.Join(filepath.Dir(projectInstalledPHPPath(root, version)), "php.ini")
}

func fakePHPScript() []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-php %*\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-php %s\n' \"$*\"\n")
}

func fakeDatabaseScript(name string) []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-" + name + " %*\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-" + name + " %s\n' \"$*\"\n")
}
