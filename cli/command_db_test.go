package cli

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"polka/backend"
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
	if !strings.Contains(output, "fake-mysql") || !strings.Contains(output, "--version") || !strings.Contains(output, "--defaults-extra-file=") || !strings.Contains(output, "--protocol=tcp") || !strings.Contains(output, "--host=127.0.0.1") || !strings.Contains(output, "--port=3306") || !strings.Contains(output, "--database=demo") {
		t.Fatalf("Run(db) output = %q, want managed credential defaults, TCP connection arguments, and default database selection", output)
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
	if !strings.Contains(output, "--defaults-extra-file=") || !strings.Contains(output, "--host=db.internal") || !strings.Contains(output, "--port=4406") || !strings.Contains(output, "--protocol=tcp") || !strings.Contains(output, "--database=demo") {
		t.Fatalf("Run(db explicit connection) output = %q, want explicit connection arguments forwarded with managed credential defaults and default database selection", output)
	}
	if strings.Contains(output, "--host=127.0.0.1") || strings.Contains(output, "--port=3307") {
		t.Fatalf("Run(db explicit connection) output = %q, want injected defaults suppressed", output)
	}
}

func TestRunDBTranslatesDatabaseNameOverride(t *testing.T) {
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
	if code := Run(stdout, stderr, []string{"--root", root, "db", "--db-name", "reporting", "--version"}); code != 0 {
		t.Fatalf("Run(db --db-name) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "--database=reporting") {
		t.Fatalf("Run(db --db-name) output = %q, want override translated to native database selection", output)
	}
	if strings.Contains(output, "--database=demo") || strings.Contains(output, "--db-name") {
		t.Fatalf("Run(db --db-name) output = %q, want only native database selection forwarded", output)
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
	if !strings.Contains(output, "fake-mysql") || !strings.Contains(output, " status") || !strings.Contains(output, "--defaults-extra-file=") || !strings.Contains(output, "--host=127.0.0.1") || !strings.Contains(output, "--port=3306") || !strings.Contains(output, "--database=demo") {
		t.Fatalf("Run(db client) output = %q, want reserved word forwarded with managed credential defaults and default database selection", output)
	}
}

func TestRunDBImportAcceptsSQLAndGzip(t *testing.T) {
	testCases := []struct {
		name       string
		args       []string
		fileName   string
		compressed bool
		database   string
	}{
		{name: "sql", fileName: "import.sql", database: "demo"},
		{name: "sql gzip", fileName: "import.sql.gz", compressed: true, database: "demo"},
		{name: "override db", args: []string{"--db-name", "reporting"}, fileName: "import-override.sql", database: "reporting"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			projectDir := t.TempDir()
			root := filepath.Join(projectDir, ".polka")
			cacheDir := filepath.Join(projectDir, "global-cache")
			capturePath := filepath.Join(projectDir, "captured.sql")
			t.Setenv("Polka_CACHE_DIR", cacheDir)
			t.Setenv("POLKA_TEST_DB_CAPTURE_PATH", capturePath)
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			fakeMySQL := cachedDatabasePath(cacheDir, "mysql", "8.4")
			if err := os.MkdirAll(filepath.Dir(fakeMySQL), 0o755); err != nil {
				t.Fatalf("MkdirAll(cache mysql) error = %v", err)
			}
			if err := os.WriteFile(fakeMySQL, fakeDatabaseCaptureScript("mysql"), 0o755); err != nil {
				t.Fatalf("WriteFile(cache mysql) error = %v", err)
			}

			if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--db-engine", "mysql", "--db-version", "8.4"}); code != 0 {
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
			environment.EnvVars = map[string]string{"POLKA_TEST_DB_CAPTURE_PATH": capturePath}
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

			importSQL := "CREATE DATABASE demo;\n"
			importPath := filepath.Join(projectDir, testCase.fileName)
			if testCase.compressed {
				file, err := os.Create(importPath)
				if err != nil {
					t.Fatalf("Create(import) error = %v", err)
				}
				writer := gzip.NewWriter(file)
				if _, err := writer.Write([]byte(importSQL)); err != nil {
					_ = writer.Close()
					_ = file.Close()
					t.Fatalf("Write(gzip import) error = %v", err)
				}
				if err := writer.Close(); err != nil {
					_ = file.Close()
					t.Fatalf("Close(gzip writer) error = %v", err)
				}
				if err := file.Close(); err != nil {
					t.Fatalf("Close(import file) error = %v", err)
				}
			} else {
				if err := os.WriteFile(importPath, []byte(importSQL), 0o644); err != nil {
					t.Fatalf("WriteFile(import) error = %v", err)
				}
			}

			stdout.Reset()
			stderr.Reset()
			runArgs := []string{"--root", root, "db", "import"}
			runArgs = append(runArgs, testCase.args...)
			runArgs = append(runArgs, importPath)
			if code := Run(stdout, stderr, runArgs); code != 0 {
				t.Fatalf("Run(db import) code = %d, stderr = %q", code, stderr.String())
			}

			captured, err := os.ReadFile(capturePath)
			if err != nil {
				t.Fatalf("ReadFile(capture) error = %v", err)
			}
			if string(captured) != importSQL {
				t.Fatalf("captured import = %q, want %q", string(captured), importSQL)
			}
			if !strings.Contains(stdout.String(), "fake-mysql") || !strings.Contains(stdout.String(), "Imported database dump") || !strings.Contains(stdout.String(), "--database="+testCase.database) {
				t.Fatalf("Run(db import) stdout = %q, want client execution, database selection, and import summary", stdout.String())
			}
		})
	}
}

func TestRunDBExportWritesSQLAndGzip(t *testing.T) {
	testCases := []struct {
		name       string
		args       []string
		fileName   string
		compressed bool
		database   string
	}{
		{name: "sql", fileName: "export.sql", database: "demo"},
		{name: "sql gzip", fileName: "export.sql.gz", compressed: true, database: "demo"},
		{name: "override db", args: []string{"--db-name=reporting"}, fileName: "export-override.sql", database: "reporting"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			projectDir := t.TempDir()
			root := filepath.Join(projectDir, ".polka")
			cacheDir := filepath.Join(projectDir, "global-cache")
			t.Setenv("Polka_CACHE_DIR", cacheDir)
			t.Setenv("POLKA_TEST_DB_DUMP_OUTPUT", "CREATE DATABASE demo;\n")
			dumpCapturePath := filepath.Join(projectDir, testCase.name+"-dump-args.txt")
			t.Setenv("POLKA_TEST_DB_DUMP_CAPTURE_PATH", dumpCapturePath)
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			fakeMySQL := cachedDatabasePath(cacheDir, "mysql", "8.4")
			if err := os.MkdirAll(filepath.Dir(fakeMySQL), 0o755); err != nil {
				t.Fatalf("MkdirAll(cache mysql) error = %v", err)
			}
			if err := os.WriteFile(fakeMySQL, fakeDatabaseScript("mysql"), 0o755); err != nil {
				t.Fatalf("WriteFile(cache mysql) error = %v", err)
			}

			fakeDump := cachedDatabaseDumpPath(cacheDir, "mysql", "8.4")
			if err := os.MkdirAll(filepath.Dir(fakeDump), 0o755); err != nil {
				t.Fatalf("MkdirAll(cache mysqldump) error = %v", err)
			}
			if err := os.WriteFile(fakeDump, fakeDatabaseDumpScript(), 0o755); err != nil {
				t.Fatalf("WriteFile(cache mysqldump) error = %v", err)
			}

			if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--db-engine", "mysql", "--db-version", "8.4"}); code != 0 {
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
			environment.EnvVars = map[string]string{"POLKA_TEST_DB_DUMP_CAPTURE_PATH": dumpCapturePath, "POLKA_TEST_DB_DUMP_OUTPUT": "CREATE DATABASE demo;\n"}
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

			exportPath := filepath.Join(projectDir, testCase.fileName)
			stdout.Reset()
			stderr.Reset()
			runArgs := []string{"--root", root, "db", "export"}
			runArgs = append(runArgs, testCase.args...)
			runArgs = append(runArgs, exportPath)
			if code := Run(stdout, stderr, runArgs); code != 0 {
				t.Fatalf("Run(db export) code = %d, stderr = %q", code, stderr.String())
			}

			dumpArgs, err := os.ReadFile(dumpCapturePath)
			if err != nil {
				t.Fatalf("ReadFile(dump args) error = %v", err)
			}
			if !strings.Contains(string(dumpArgs), "--databases "+testCase.database) {
				t.Fatalf("dump args = %q, want selected database %q", string(dumpArgs), testCase.database)
			}

			var exportData []byte
			if testCase.compressed {
				file, err := os.Open(exportPath)
				if err != nil {
					t.Fatalf("Open(export gzip) error = %v", err)
				}
				reader, err := gzip.NewReader(file)
				if err != nil {
					_ = file.Close()
					t.Fatalf("NewReader(export gzip) error = %v", err)
				}
				exportData, err = io.ReadAll(reader)
				if err != nil {
					_ = reader.Close()
					_ = file.Close()
					t.Fatalf("ReadAll(export gzip) error = %v", err)
				}
				if err := reader.Close(); err != nil {
					_ = file.Close()
					t.Fatalf("Close(export gzip reader) error = %v", err)
				}
				if err := file.Close(); err != nil {
					t.Fatalf("Close(export gzip file) error = %v", err)
				}
			} else {
				var err error
				exportData, err = os.ReadFile(exportPath)
				if err != nil {
					t.Fatalf("ReadFile(export) error = %v", err)
				}
			}

			if string(exportData) != "CREATE DATABASE demo;\n" {
				t.Fatalf("export data = %q, want dump contents", string(exportData))
			}
			if !strings.Contains(stdout.String(), "Exported database dump") {
				t.Fatalf("Run(db export) stdout = %q, want export summary", stdout.String())
			}
		})
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
	if credentials.User != dbManagedUserName || credentials.Password == "" || credentials.Port != 3307 || credentials.DatabaseName != "demo" {
		t.Fatalf("credentials = %#v, want managed user with generated password and default database on port 3307", credentials)
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
	if !strings.Contains(string(bootstrapData), "CREATE DATABASE IF NOT EXISTS `demo`;") || !strings.Contains(string(bootstrapData), "CREATE USER IF NOT EXISTS '"+dbManagedUserName+"'") || !strings.Contains(string(bootstrapData), credentials.Password) {
		t.Fatalf("bootstrap sql = %q, want managed bootstrap statements and default database creation", string(bootstrapData))
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

func TestResolveDatabaseDataPathKeepsLegacyDataForMatchingEngine(t *testing.T) {
	root := t.TempDir()
	legacy := legacyDatabaseDataPath(root, "demo")
	if err := os.MkdirAll(filepath.Join(legacy, "#innodb_redo"), 0o755); err != nil {
		t.Fatalf("MkdirAll(#innodb_redo) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "ibdata1"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(ibdata1) error = %v", err)
	}

	resolved := dbResolvedEnvironment{
		Environment: backend.Environment{Name: "demo"},
		Database:    &backend.DatabaseConfig{Engine: "mysql", Version: "8.4"},
	}

	dataPath, err := resolveDatabaseDataPath(root, resolved)
	if err != nil {
		t.Fatalf("resolveDatabaseDataPath() error = %v", err)
	}
	if dataPath != legacy {
		t.Fatalf("resolveDatabaseDataPath() = %q, want legacy path %q", dataPath, legacy)
	}
}

func TestResolveDatabaseDataPathAvoidsLegacyMySQLDataForMariaDB(t *testing.T) {
	root := t.TempDir()
	legacy := legacyDatabaseDataPath(root, "demo")
	if err := os.MkdirAll(filepath.Join(legacy, "#innodb_redo"), 0o755); err != nil {
		t.Fatalf("MkdirAll(#innodb_redo) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "ibdata1"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(ibdata1) error = %v", err)
	}

	resolved := dbResolvedEnvironment{
		Environment: backend.Environment{Name: "demo"},
		Database:    &backend.DatabaseConfig{Engine: "mariadb", Version: "11.8"},
	}

	dataPath, err := resolveDatabaseDataPath(root, resolved)
	if err != nil {
		t.Fatalf("resolveDatabaseDataPath() error = %v", err)
	}
	want := databaseDataPath(root, "demo", "mariadb", "11.8")
	if dataPath != want {
		t.Fatalf("resolveDatabaseDataPath() = %q, want scoped path %q", dataPath, want)
	}
}

func TestLoadLiveDatabaseStateForResolvedRejectsMismatchedLiveState(t *testing.T) {
	root := t.TempDir()
	if err := writeDatabaseState(databaseStatePath(root, "demo"), dbRuntimeState{
		EnvironmentName: "demo",
		Engine:          "mysql",
		Version:         "8.4",
		Port:            3306,
	}); err != nil {
		t.Fatalf("writeDatabaseState() error = %v", err)
	}

	oldPing := pingDatabaseAddressFunc
	t.Cleanup(func() {
		pingDatabaseAddressFunc = oldPing
	})
	pingDatabaseAddressFunc = func(address string) bool {
		return address == databaseAddress(3306)
	}

	resolved := dbResolvedEnvironment{
		Environment: backend.Environment{Name: "demo"},
		Database:    &backend.DatabaseConfig{Engine: "mariadb", Version: "11.8", Port: 3306},
	}

	state, err := loadLiveDatabaseStateForResolved(root, resolved)
	if err == nil {
		t.Fatalf("loadLiveDatabaseStateForResolved() error = nil, want mismatch error")
	}
	if state != nil {
		t.Fatalf("loadLiveDatabaseStateForResolved() state = %#v, want nil", state)
	}
	if !strings.Contains(err.Error(), "stop the running database first") {
		t.Fatalf("loadLiveDatabaseStateForResolved() error = %q, want mismatch guidance", err.Error())
	}
}

func TestDatabaseInitializeArgsForMariaDBUsesInstallDBFlags(t *testing.T) {
	args := databaseInitializeArgs(dbServerSpec{
		Engine:  "mariadb",
		DataDir: filepath.Join("tmp", "mariadb"),
		Port:    3306,
	})

	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--initialize-insecure") || strings.Contains(joined, "auth-root-authentication-method") {
		t.Fatalf("databaseInitializeArgs(mariadb) = %q, want install-db compatible args", joined)
	}
	if !strings.Contains(joined, "--datadir=") || !strings.Contains(joined, "--port=3306") {
		t.Fatalf("databaseInitializeArgs(mariadb) = %q, want datadir and port args", joined)
	}
}

func TestDatabaseServerInitializedRequiresSystemDatabase(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "ibdata1"), []byte("partial\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(ibdata1) error = %v", err)
	}

	initialized, err := databaseServerInitialized(dbServerSpec{Engine: "mariadb", DataDir: dataDir})
	if err != nil {
		t.Fatalf("databaseServerInitialized(partial) error = %v", err)
	}
	if initialized {
		t.Fatalf("databaseServerInitialized(partial) = true, want false")
	}

	if err := os.MkdirAll(filepath.Join(dataDir, "mysql"), 0o755); err != nil {
		t.Fatalf("MkdirAll(mysql) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "mysql", "db.frm"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(mysql/db.frm) error = %v", err)
	}

	initialized, err = databaseServerInitialized(dbServerSpec{Engine: "mariadb", DataDir: dataDir})
	if err != nil {
		t.Fatalf("databaseServerInitialized(complete) error = %v", err)
	}
	if !initialized {
		t.Fatalf("databaseServerInitialized(complete) = false, want true")
	}
}
