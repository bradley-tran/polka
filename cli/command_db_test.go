package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	if !strings.Contains(output, "fake-mysql") || !strings.Contains(output, "--version") || !strings.Contains(output, "--defaults-extra-file=") || !strings.Contains(output, "--protocol=tcp") || !strings.Contains(output, "--host=127.0.0.1") || !strings.Contains(output, "--port=3306") {
		t.Fatalf("Run(db) output = %q, want managed credential defaults and TCP connection arguments", output)
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
	if !strings.Contains(output, "--defaults-extra-file=") || !strings.Contains(output, "--host=db.internal") || !strings.Contains(output, "--port=4406") || !strings.Contains(output, "--protocol=tcp") {
		t.Fatalf("Run(db explicit connection) output = %q, want explicit connection arguments forwarded with managed credential defaults", output)
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
	if !strings.Contains(output, "fake-mysql") || !strings.Contains(output, " status") || !strings.Contains(output, "--defaults-extra-file=") || !strings.Contains(output, "--host=127.0.0.1") || !strings.Contains(output, "--port=3306") {
		t.Fatalf("Run(db client) output = %q, want reserved word forwarded with managed credential defaults", output)
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
	fakeMySQLAdmin := cachedDatabaseAdminPath(cacheDir, "mysql", "8.4")
	if err := os.MkdirAll(filepath.Dir(fakeMySQLAdmin), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache mysqladmin) error = %v", err)
	}
	if err := os.WriteFile(fakeMySQLAdmin, fakeDatabaseScript("mysqladmin"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache mysqladmin) error = %v", err)
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
	if startedSpec.AdminTarget == "" || startedSpec.DefaultsFile == "" || startedSpec.BootstrapSQLFile == "" {
		t.Fatalf("started spec = %#v, want admin target and credential/bootstrap assets", startedSpec)
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
	if state.AdminTarget == "" || state.DefaultsFile == "" {
		t.Fatalf("database state = %#v, want native shutdown fields", state)
	}

	credentials, err := loadDatabaseCredentials(databaseCredentialStatePath(root, "demo"))
	if err != nil {
		t.Fatalf("loadDatabaseCredentials() error = %v", err)
	}
	if credentials.User != dbManagedUserName || credentials.Password == "" || credentials.Port != 3307 {
		t.Fatalf("credentials = %#v, want managed user with generated password on port 3307", credentials)
	}
	defaultsData, err := os.ReadFile(databaseDefaultsFilePath(root, "demo"))
	if err != nil {
		t.Fatalf("ReadFile(defaults) error = %v", err)
	}
	if !strings.Contains(string(defaultsData), "user="+dbManagedUserName) || !strings.Contains(string(defaultsData), "protocol=tcp") {
		t.Fatalf("defaults file = %q, want managed client config", string(defaultsData))
	}
	bootstrapData, err := os.ReadFile(databaseBootstrapSQLPath(root, "demo"))
	if err != nil {
		t.Fatalf("ReadFile(bootstrap sql) error = %v", err)
	}
	if !strings.Contains(string(bootstrapData), "CREATE USER IF NOT EXISTS '"+dbManagedUserName+"'") || !strings.Contains(string(bootstrapData), credentials.Password) {
		t.Fatalf("bootstrap sql = %q, want managed bootstrap statements", string(bootstrapData))
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
	if stoppedState.AdminTarget == "" || stoppedState.DefaultsFile == "" {
		t.Fatalf("stopped state = %#v, want native shutdown target and defaults file", stoppedState)
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