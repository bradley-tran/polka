package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestRunStopStopsManagedDatabaseWhenWebserverAlreadyStopped(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	writeStopTestConfig(t, projectDir, testEnvironmentConfig{
		PHP: "8.4",
		Database: &testDatabaseConfig{
			Engine:  "mysql",
			Version: "8.4",
			Port:    3307,
		},
	})
	if err := writeDatabaseState(databaseStatePath(root, "demo"), dbRuntimeState{
		EnvironmentName: "demo",
		Engine:          "mysql",
		Version:         "8.4",
		Port:            3307,
		PID:             7878,
		AdminTarget:     "mysqladmin",
		DefaultsFile:    "defaults.cnf",
	}); err != nil {
		t.Fatalf("writeDatabaseState() error = %v", err)
	}

	oldStopDatabase := stopDatabaseServerFunc
	oldPingDatabase := pingDatabaseAddressFunc
	t.Cleanup(func() {
		stopDatabaseServerFunc = oldStopDatabase
		pingDatabaseAddressFunc = oldPingDatabase
	})

	running := map[string]bool{databaseAddress(3307): true}
	stopCalls := 0
	stoppedState := dbRuntimeState{}
	stopDatabaseServerFunc = func(state dbRuntimeState) error {
		stopCalls++
		stoppedState = state
		running[databaseAddress(state.Port)] = false
		return nil
	}
	pingDatabaseAddressFunc = func(address string) bool {
		return running[address]
	}

	if code := Run(stdout, stderr, []string{"--root", root, "stop"}); code != 0 {
		t.Fatalf("Run(stop) code = %d, stderr = %q", code, stderr.String())
	}
	if stopCalls != 1 {
		t.Fatalf("database stop calls = %d, want 1", stopCalls)
	}
	if stoppedState.Engine != "mysql" || stoppedState.Port != 3307 {
		t.Fatalf("stopped database state = %#v, want mysql on port 3307", stoppedState)
	}
	if !strings.Contains(stdout.String(), "Webserver for environment \"demo\" is already stopped.") {
		t.Fatalf("Run(stop) stdout = %q, want webserver already stopped message", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Stopped mysql for environment \"demo\".") {
		t.Fatalf("Run(stop) stdout = %q, want database stop summary", stdout.String())
	}
	if _, err := os.Stat(databaseStatePath(root, "demo")); !os.IsNotExist(err) {
		t.Fatalf("Stat(database state) error = %v, want not exists", err)
	}
}

func TestRunStopStopsWebserverAndDatabase(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	writeStopTestConfig(t, projectDir, testEnvironmentConfig{
		PHP: "8.4",
		Database: &testDatabaseConfig{
			Engine:  "mysql",
			Version: "8.4",
			Port:    3307,
		},
	})
	if err := writeServeState(serveStatePath(root, "demo"), serveRuntimeState{
		EnvironmentName: "demo",
		ServerKind:      "php",
		ServerAddress:   "localhost:8080",
		Docroot:         filepath.Join(projectDir, "site", "public"),
		PrimaryPID:      4242,
	}); err != nil {
		t.Fatalf("writeServeState() error = %v", err)
	}
	if err := writeDatabaseState(databaseStatePath(root, "demo"), dbRuntimeState{
		EnvironmentName: "demo",
		Engine:          "mysql",
		Version:         "8.4",
		Port:            3307,
		PID:             7878,
		AdminTarget:     "mysqladmin",
		DefaultsFile:    "defaults.cnf",
	}); err != nil {
		t.Fatalf("writeDatabaseState() error = %v", err)
	}

	oldStopServe := stopServeRuntimeFunc
	oldPingServe := pingServeAddressFunc
	oldStopDatabase := stopDatabaseServerFunc
	oldPingDatabase := pingDatabaseAddressFunc
	t.Cleanup(func() {
		stopServeRuntimeFunc = oldStopServe
		pingServeAddressFunc = oldPingServe
		stopDatabaseServerFunc = oldStopDatabase
		pingDatabaseAddressFunc = oldPingDatabase
	})

	serveRunning := map[string]bool{"localhost:8080": true}
	databaseRunning := map[string]bool{databaseAddress(3307): true}
	serveStopCalls := 0
	databaseStopCalls := 0
	stopServeRuntimeFunc = func(state serveRuntimeState) error {
		serveStopCalls++
		serveRunning[state.ServerAddress] = false
		return nil
	}
	pingServeAddressFunc = func(address string) bool {
		return serveRunning[address]
	}
	stopDatabaseServerFunc = func(state dbRuntimeState) error {
		databaseStopCalls++
		databaseRunning[databaseAddress(state.Port)] = false
		return nil
	}
	pingDatabaseAddressFunc = func(address string) bool {
		return databaseRunning[address]
	}

	if code := Run(stdout, stderr, []string{"--root", root, "stop"}); code != 0 {
		t.Fatalf("Run(stop) code = %d, stderr = %q", code, stderr.String())
	}
	if serveStopCalls != 1 {
		t.Fatalf("serve stop calls = %d, want 1", serveStopCalls)
	}
	if databaseStopCalls != 1 {
		t.Fatalf("database stop calls = %d, want 1", databaseStopCalls)
	}
	if !strings.Contains(stdout.String(), "Stopped php webserver for environment \"demo\".") {
		t.Fatalf("Run(stop) stdout = %q, want webserver stop summary", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Stopped mysql for environment \"demo\".") {
		t.Fatalf("Run(stop) stdout = %q, want database stop summary", stdout.String())
	}
	if _, err := os.Stat(serveStatePath(root, "demo")); !os.IsNotExist(err) {
		t.Fatalf("Stat(serve state) error = %v, want not exists", err)
	}
	if _, err := os.Stat(databaseStatePath(root, "demo")); !os.IsNotExist(err) {
		t.Fatalf("Stat(database state) error = %v, want not exists", err)
	}
}

func writeStopTestConfig(t *testing.T, projectDir string, environment testEnvironmentConfig) {
	t.Helper()

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Current: "demo",
		Environments: map[string]testEnvironmentConfig{
			"demo": environment,
		},
	}
	configData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	configData = append(configData, '\n')
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
}
