package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"polka/service"
)

// dashboardServiceRowByName returns the named service row, failing the test
// when it is absent.
func dashboardServiceRowByName(t *testing.T, snapshot dashboardSnapshot, name string) dashboardServiceRow {
	t.Helper()

	for _, row := range snapshot.Services {
		if row.Name == name {
			return row
		}
	}

	t.Fatalf("gatherDashboardSnapshot() missing service row %q", name)
	return dashboardServiceRow{}
}

// writeDashboardTestProject writes a project whose "demo" environment defines
// a webserver endpoint, a managed MySQL database, and one queue worker, and
// marks "demo" active.
func writeDashboardTestProject(t *testing.T, projectDir string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), []byte("version: 1\nroot: .polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(polka.yaml) error = %v", err)
	}
	demoConfig := strings.Join([]string{
		"tools:",
		"  php: \"8.4\"",
		"  mysql: \"8.0\"",
		"database:",
		"  engine: mysql",
		"  port: 3307",
		"server:",
		"  hostname: localhost",
		"  port: 8080",
		"workers:",
		"  queue:",
		"    command: php artisan queue:work",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(projectDir, "polka.demo.yaml"), []byte(demoConfig), 0o644); err != nil {
		t.Fatalf("WriteFile(polka.demo.yaml) error = %v", err)
	}
	writeTestActiveEnvironment(t, filepath.Join(projectDir, ".polka"), "demo")
}

func TestGatherDashboardSnapshotReportsStoppedAndUnsetServices(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	writeDashboardTestProject(t, projectDir)

	store, err := resolveStore(root)
	if err != nil {
		t.Fatalf("resolveStore() error = %v", err)
	}

	snapshot := gatherDashboardSnapshot(store, time.Now())
	if len(snapshot.Errors) != 0 {
		t.Fatalf("gatherDashboardSnapshot() errors = %v, want none", snapshot.Errors)
	}
	if snapshot.EnvironmentName != "demo" {
		t.Fatalf("snapshot environment = %q, want %q", snapshot.EnvironmentName, "demo")
	}
	if snapshot.WebServer != nil {
		t.Errorf("snapshot webserver = %+v, want nil (stopped)", snapshot.WebServer)
	}
	// The database is configured but not running; the other services are not
	// configured at all.
	if row := dashboardServiceRowByName(t, snapshot, "database"); row.Status != dashboardStatusStopped {
		t.Errorf("database row status = %q, want %q", row.Status, dashboardStatusStopped)
	}
	for _, name := range []string{"mailpit", "phpmyadmin", "meilisearch", "redis", "traefik"} {
		if row := dashboardServiceRowByName(t, snapshot, name); row.Status != dashboardStatusUnset {
			t.Errorf("%s row status = %q, want %q", name, row.Status, dashboardStatusUnset)
		}
	}
	if snapshot.WorkersUnset {
		t.Errorf("snapshot workers unset = true, want configured workers detected")
	}
	if snapshot.WorkersConfigured != 1 {
		t.Errorf("snapshot workers configured = %d, want 1", snapshot.WorkersConfigured)
	}
	if snapshot.Workers != nil {
		t.Errorf("snapshot workers = %+v, want nil (stopped)", snapshot.Workers)
	}
}

func TestGatherDashboardSnapshotReportsRunningServices(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	writeDashboardTestProject(t, projectDir)

	now := time.Now().UTC()
	if err := writeServeState(serveStatePath(root, "demo"), serveRuntimeState{
		EnvironmentName: "demo",
		ServerKind:      "php",
		ServerScheme:    "http",
		ServerAddress:   "localhost:8080",
		Docroot:         filepath.Join(projectDir, "public"),
		PrimaryPID:      1010,
		StartedAt:       now.Add(-90 * time.Minute),
	}); err != nil {
		t.Fatalf("writeServeState() error = %v", err)
	}
	if err := writeDatabaseState(databaseStatePath(root, "demo"), dbRuntimeState{
		EnvironmentName: "demo",
		Engine:          "mysql",
		Version:         "8.0",
		Port:            3307,
		PID:             os.Getpid(),
		StartedAt:       now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("writeDatabaseState() error = %v", err)
	}
	if err := service.WriteWorkersState(service.WorkersStatePath(root, "demo"), service.WorkersRuntimeState{
		EnvironmentName: "demo",
		Processes: []service.WorkerProcessState{
			{Name: "queue", Replica: 1, PID: 5001, Command: "php artisan queue:work", StartedAt: now.Add(-12 * time.Minute)},
		},
	}); err != nil {
		t.Fatalf("WriteWorkersState() error = %v", err)
	}

	oldPingServe := pingServeAddressFunc
	oldPingDatabase := pingDatabaseAddressFunc
	oldPIDIsLive := workerPIDIsLiveFunc
	t.Cleanup(func() {
		pingServeAddressFunc = oldPingServe
		pingDatabaseAddressFunc = oldPingDatabase
		workerPIDIsLiveFunc = oldPIDIsLive
	})
	pingServeAddressFunc = func(address string) bool { return address == "localhost:8080" }
	pingDatabaseAddressFunc = func(address string) bool { return address == databaseAddress(3307) }
	workerPIDIsLiveFunc = func(pid int) bool { return pid == 5001 }

	store, err := resolveStore(root)
	if err != nil {
		t.Fatalf("resolveStore() error = %v", err)
	}

	snapshot := gatherDashboardSnapshot(store, now)
	if len(snapshot.Errors) != 0 {
		t.Fatalf("gatherDashboardSnapshot() errors = %v, want none", snapshot.Errors)
	}
	if snapshot.WebServer == nil || snapshot.WebServer.PrimaryPID != 1010 {
		t.Fatalf("snapshot webserver = %+v, want running state with pid 1010", snapshot.WebServer)
	}

	databaseRow := dashboardServiceRowByName(t, snapshot, "database")
	if databaseRow.Status != dashboardStatusRunning {
		t.Errorf("database row status = %q, want %q", databaseRow.Status, dashboardStatusRunning)
	}
	if databaseRow.Detail != "mysql:8.0@3307" {
		t.Errorf("database row detail = %q, want %q", databaseRow.Detail, "mysql:8.0@3307")
	}

	if snapshot.Workers == nil || len(snapshot.Workers.Processes) != 1 {
		t.Fatalf("snapshot workers = %+v, want one live process", snapshot.Workers)
	}
	if snapshot.WorkersConfigured != 1 {
		t.Errorf("snapshot workers configured = %d, want 1", snapshot.WorkersConfigured)
	}
}

func TestRenderDashboard(t *testing.T) {
	now := time.Date(2026, 7, 12, 15, 4, 5, 0, time.UTC)
	snapshot := dashboardSnapshot{
		EnvironmentName: "demo",
		TakenAt:         now,
		WebServer: &serveRuntimeState{
			EnvironmentName: "demo",
			ServerKind:      "nginx",
			ServerScheme:    "http",
			ServerAddress:   "localhost:8080",
			PrimaryPID:      1010,
			StartedAt:       now.Add(-90 * time.Minute),
		},
		Services: []dashboardServiceRow{
			{Name: "database", Status: dashboardStatusRunning, Detail: "mysql:8.0@3307", PID: 1201, StartedAt: now.Add(-time.Hour)},
			{Name: "mailpit", Status: dashboardStatusStopped},
			{Name: "redis", Status: dashboardStatusUnset},
		},
		Workers: &workersRuntimeState{
			EnvironmentName: "demo",
			Processes: []service.WorkerProcessState{
				{Name: "queue", Replica: 1, PID: 5001, StartedAt: now.Add(-12 * time.Minute)},
			},
		},
		WorkersConfigured: 2,
	}

	output := renderDashboard(snapshot)
	for _, expected := range []string{
		"polka dashboard — environment demo",
		"updated 15:04:05",
		"http://localhost:8080",
		"pid 1010",
		"up 1h30m",
		"mysql:8.0@3307",
		"mailpit",
		"stopped",
		"unset",
		"Workers (1/2: queue)",
		"queue #1",
		"up 12m",
		"q quit",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("renderDashboard() = %q, want it to contain %q", output, expected)
		}
	}
}

func TestRenderDashboardWithoutEnvironment(t *testing.T) {
	output := renderDashboard(dashboardSnapshot{NoEnvironment: true})
	if !strings.Contains(output, "No active environment selected.") {
		t.Errorf("renderDashboard() = %q, want no-environment message", output)
	}
}

func TestFormatUptime(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		startedAt time.Time
		want      string
	}{
		{name: "zero start", startedAt: time.Time{}, want: "-"},
		{name: "future start", startedAt: now.Add(time.Minute), want: "-"},
		{name: "seconds", startedAt: now.Add(-42 * time.Second), want: "42s"},
		{name: "minutes", startedAt: now.Add(-12 * time.Minute), want: "12m"},
		{name: "hours", startedAt: now.Add(-(2*time.Hour + 13*time.Minute)), want: "2h13m"},
		{name: "days", startedAt: now.Add(-(3*24*time.Hour + 2*time.Hour)), want: "3d2h"},
	}
	for _, test := range tests {
		if got := formatUptime(now, test.startedAt); got != test.want {
			t.Errorf("formatUptime(%s) = %q, want %q", test.name, got, test.want)
		}
	}
}

// TestRunDashboardFallsBackToStatusWithoutTerminal covers the non-TTY path:
// buffers are not terminals, so dashboard prints the one-shot status output.
func TestRunDashboardFallsBackToStatusWithoutTerminal(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	writeDashboardTestProject(t, projectDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "dashboard"}); code != 0 {
		t.Fatalf("Run(dashboard) code = %d, stderr = %q", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "environment demo") {
		t.Errorf("Run(dashboard) stdout = %q, want status environment line", output)
	}
	if !strings.Contains(output, "webserver stopped") {
		t.Errorf("Run(dashboard) stdout = %q, want webserver status line", output)
	}
}

func TestRunDashboardRejectsArguments(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", t.TempDir(), "dashboard", "extra"}); code != 1 {
		t.Fatalf("Run(dashboard extra) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "dashboard does not take arguments") {
		t.Errorf("Run(dashboard extra) stderr = %q, want argument error", stderr.String())
	}
}
