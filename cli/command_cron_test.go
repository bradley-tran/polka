package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"polka/backend"
	"polka/config"
	"polka/service"
)

// writeCronTestProject writes a project whose active "demo" environment
// defines two cron jobs. The tick job runs a project-relative script so its
// target resolves without any installed tool.
func writeCronTestProject(t *testing.T, projectDir string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), []byte("version: 0.2\nroot: .polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(polka.yaml) error = %v", err)
	}
	demoConfig := strings.Join([]string{
		"cron:",
		"  tick:",
		"    schedule: \"*/5 * * * *\"",
		"    command: bin/tick --verbose",
		"    env:",
		"      TICK: \"1\"",
		"  backup:",
		"    schedule: \"@daily\"",
		"    command: bin/backup",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(projectDir, "polka.demo.yaml"), []byte(demoConfig), 0o644); err != nil {
		t.Fatalf("WriteFile(polka.demo.yaml) error = %v", err)
	}
	writeTestActiveEnvironment(t, filepath.Join(projectDir, ".polka"), "demo")
}

// stubCronRunner replaces the runner seams with an in-memory fake and returns
// the set of live PIDs plus the stopped PIDs it records.
func stubCronRunner(t *testing.T) (map[int]bool, *[]int) {
	t.Helper()

	oldStart, oldStop, oldLive, oldExecutable := startCronRunnerFunc, stopCronRunnerFunc, cronPIDIsLiveFunc, polkaExecutableFunc
	t.Cleanup(func() {
		startCronRunnerFunc, stopCronRunnerFunc, cronPIDIsLiveFunc, polkaExecutableFunc = oldStart, oldStop, oldLive, oldExecutable
	})

	live := map[int]bool{}
	stopped := []int{}
	nextPID := 7000
	startCronRunnerFunc = func(service.CronRunnerSpec) (int, error) {
		nextPID++
		live[nextPID] = true
		return nextPID, nil
	}
	stopCronRunnerFunc = func(state service.CronRunnerState) error {
		delete(live, state.PID)
		stopped = append(stopped, state.PID)
		return nil
	}
	cronPIDIsLiveFunc = func(pid int) bool { return live[pid] }
	polkaExecutableFunc = func() (string, error) { return "/opt/polka/polka", nil }

	return live, &stopped
}

// TestStartServicesStartsCronRunner checks the serve path: the runner starts
// through the hooks and the start summary names its jobs, and a second serve
// reports it as already running.
func TestStartServicesStartsCronRunner(t *testing.T) {
	projectDir := t.TempDir()
	writeCronTestProject(t, projectDir)
	stubCronRunner(t)
	store := backend.NewProjectStore(projectDir)
	current, err := store.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	hookContext := startHookContext{Stdout: stdout, Stderr: stderr, Store: store, Environment: *current}
	if err := defaultCLIHookRegistry().StartServices(hookContext); err != nil {
		t.Fatalf("StartServices() error = %v, stderr = %q", err, stderr.String())
	}
	if want := `Started the cron runner for environment "demo" (2 job(s): backup, tick).`; !strings.Contains(stdout.String(), want) {
		t.Fatalf("StartServices() stdout = %q, want %q", stdout.String(), want)
	}

	stdout.Reset()
	if err := defaultCLIHookRegistry().StartServices(hookContext); err != nil {
		t.Fatalf("StartServices(second) error = %v", err)
	}
	if want := `Cron runner for environment "demo" is already running.`; !strings.Contains(stdout.String(), want) {
		t.Fatalf("StartServices(second) stdout = %q, want %q", stdout.String(), want)
	}
}

// TestRunStopStopsCronRunner checks that `polka stop` stops a live runner and
// removes its state.
func TestRunStopStopsCronRunner(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	writeCronTestProject(t, projectDir)
	live, stopped := stubCronRunner(t)
	live[4321] = true
	if err := service.WriteCronState(service.CronStatePath(root, "demo"), service.CronRunnerState{EnvironmentName: "demo", PID: 4321, Jobs: []string{"backup", "tick"}}); err != nil {
		t.Fatalf("WriteCronState() error = %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "stop"}); code != 0 {
		t.Fatalf("Run(stop) code = %d, stderr = %q", code, stderr.String())
	}
	if !reflect.DeepEqual(*stopped, []int{4321}) {
		t.Fatalf("stopped PIDs = %#v, want [4321]", *stopped)
	}
	if !strings.Contains(stdout.String(), `Stopped the cron runner for environment "demo".`) {
		t.Fatalf("Run(stop) stdout = %q, want cron stop summary", stdout.String())
	}
	if _, err := os.Stat(service.CronStatePath(root, "demo")); !os.IsNotExist(err) {
		t.Fatalf("Stat(cron state) error = %v, want not exists", err)
	}
}

// TestRunStopReportsCronAlreadyStopped checks the summary when cron is
// configured but no runner is live.
func TestRunStopReportsCronAlreadyStopped(t *testing.T) {
	projectDir := t.TempDir()
	writeCronTestProject(t, projectDir)
	stubCronRunner(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "stop"}); code != 0 {
		t.Fatalf("Run(stop) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `Cron runner for environment "demo" is already stopped.`) {
		t.Fatalf("Run(stop) stdout = %q, want cron already-stopped summary", stdout.String())
	}
}

// TestStatusCronHooks checks the config and runtime status lines in every
// state: unset, configured and stopped, and running.
func TestStatusCronHooks(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewProjectStore(projectDir)
	live, _ := stubCronRunner(t)

	stdout := &bytes.Buffer{}
	ctx := statusHookContext{Stdout: stdout, Store: store, Environment: backend.Environment{Name: "demo"}}
	if err := statusCronConfigHook(ctx); err != nil {
		t.Fatalf("statusCronConfigHook(unset) error = %v", err)
	}
	if err := statusCronRuntimeHook(ctx); err != nil {
		t.Fatalf("statusCronRuntimeHook(unset) error = %v", err)
	}
	if got := stdout.String(); got != "cron unset\ncron unset\n" {
		t.Fatalf("status output = %q, want cron unset lines", got)
	}

	ctx.Environment.Cron = map[string]config.CronJobConfig{
		"tick":   {Schedule: "*/5 * * * *", Command: "php tick.php"},
		"backup": {Schedule: "@daily", Command: "php backup.php"},
	}
	stdout.Reset()
	if err := statusCronConfigHook(ctx); err != nil {
		t.Fatalf("statusCronConfigHook() error = %v", err)
	}
	if want := "cron backup @daily: php backup.php\ncron tick */5 * * * *: php tick.php\n"; stdout.String() != want {
		t.Fatalf("config status = %q, want %q", stdout.String(), want)
	}

	stdout.Reset()
	if err := statusCronRuntimeHook(ctx); err != nil {
		t.Fatalf("statusCronRuntimeHook(stopped) error = %v", err)
	}
	if stdout.String() != "cron stopped\n" {
		t.Fatalf("runtime status = %q, want cron stopped", stdout.String())
	}

	live[555] = true
	if err := service.WriteCronState(service.CronStatePath(store.RootDir, "demo"), service.CronRunnerState{EnvironmentName: "demo", PID: 555, Jobs: []string{"backup", "tick"}}); err != nil {
		t.Fatalf("WriteCronState() error = %v", err)
	}
	stdout.Reset()
	if err := statusCronRuntimeHook(ctx); err != nil {
		t.Fatalf("statusCronRuntimeHook(running) error = %v", err)
	}
	if stdout.String() != "cron running (pid 555, 2 job(s))\n" {
		t.Fatalf("runtime status = %q, want cron running", stdout.String())
	}
}

// TestRunCronList checks the listing: runner state, header, next runs, and
// last results from the run history.
func TestRunCronList(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	writeCronTestProject(t, projectDir)
	stubCronRunner(t)
	oldNow := cronNowFunc
	t.Cleanup(func() { cronNowFunc = oldNow })
	now := time.Date(2026, 11, 15, 10, 2, 0, 0, time.Local)
	cronNowFunc = func() time.Time { return now }

	history := service.CronRunsState{EnvironmentName: "demo", Jobs: map[string]service.CronJobRunState{
		"tick": {LastStart: time.Date(2026, 11, 15, 10, 0, 0, 0, time.Local), LastExitCode: 2},
	}}
	if err := service.WriteCronRunsState(service.CronRunsPath(root, "demo"), history); err != nil {
		t.Fatalf("WriteCronRunsState() error = %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "cron"}); code != 0 {
		t.Fatalf("Run(cron) code = %d, stderr = %q", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{
		`Cron runner for environment "demo" is stopped; polka serve starts it.`,
		"JOB",
		"backup  @daily",
		"2026-11-16 00:00",
		"never",
		"tick    */5 * * * *",
		"2026-11-15 10:05",
		"2026-11-15 10:00 (exit 2)",
		"bin/tick --verbose",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("Run(cron) stdout = %q, want it to contain %q", output, want)
		}
	}
}

// TestRunCronListWithoutJobs checks the hint shown when nothing is configured.
func TestRunCronListWithoutJobs(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), []byte("version: 0.2\nroot: .polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(polka.yaml) error = %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "cron", "list"}); code != 0 {
		t.Fatalf("Run(cron list) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `No cron jobs configured for environment "default"`) {
		t.Fatalf("Run(cron list) stdout = %q, want no-jobs hint", stdout.String())
	}
}

// TestRunCronRunRunsOneJob checks that `polka cron run` resolves the job like
// the runner would and returns the job's exit code.
func TestRunCronRunRunsOneJob(t *testing.T) {
	projectDir := t.TempDir()
	writeCronTestProject(t, projectDir)
	oldRun := runCronJobOnceFunc
	t.Cleanup(func() { runCronJobOnceFunc = oldRun })
	var got service.CronJobSpec
	runCronJobOnceFunc = func(_, _ io.Writer, spec service.CronJobSpec) (int, error) {
		got = spec
		return 4, nil
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "cron", "run", "tick"}); code != 4 {
		t.Fatalf("Run(cron run tick) code = %d, want 4, stderr = %q", code, stderr.String())
	}
	if got.Name != "tick" || got.Target != "bin/tick" || !reflect.DeepEqual(got.Args, []string{"--verbose"}) {
		t.Fatalf("job spec = %#v, want tick resolved to bin/tick --verbose", got)
	}
	if got.Dir != projectDir {
		t.Fatalf("job dir = %q, want project dir %q", got.Dir, projectDir)
	}
	if !containsEnvEntry(got.Env, "TICK=1") {
		t.Fatalf("job env = %#v, want TICK=1", got.Env)
	}
}

// TestRunCronRunRejectsUnknownJob checks the error for an undefined job and a
// missing job name.
func TestRunCronRunRejectsUnknownJob(t *testing.T) {
	projectDir := t.TempDir()
	writeCronTestProject(t, projectDir)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "cron", "run", "nope"}); code != 1 {
		t.Fatalf("Run(cron run nope) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), `cron job "nope" is not defined for environment "demo"`) {
		t.Fatalf("Run(cron run nope) stderr = %q, want undefined job error", stderr.String())
	}

	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "cron", "run"}); code != 2 {
		t.Fatalf("Run(cron run) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "cron run requires a job name") {
		t.Fatalf("Run(cron run) stderr = %q, want missing job error", stderr.String())
	}

	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "cron", "bogus"}); code != 2 {
		t.Fatalf("Run(cron bogus) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unknown cron subcommand "bogus"`) {
		t.Fatalf("Run(cron bogus) stderr = %q, want unknown subcommand error", stderr.String())
	}
}

// TestRunCronLogs checks printing every cron log, one job's log, and the
// errors for an unknown job and for logs that do not exist yet.
func TestRunCronLogs(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	writeCronTestProject(t, projectDir)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "cron", "logs"}); code != 1 {
		t.Fatalf("Run(cron logs, none) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no cron logs found") {
		t.Fatalf("Run(cron logs, none) stderr = %q, want no-logs error", stderr.String())
	}

	writeCronTestLog(t, service.CronRunnerLogPath(root, "demo"), "runner started\n")
	writeCronTestLog(t, service.CronJobLogPath(root, "demo", "tick"), "tick output\n")

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "cron", "logs"}); code != 0 {
		t.Fatalf("Run(cron logs) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "runner started") || !strings.Contains(stdout.String(), "tick output") {
		t.Fatalf("Run(cron logs) stdout = %q, want runner and job logs", stdout.String())
	}

	stdout.Reset()
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "cron", "logs", "tick"}); code != 0 {
		t.Fatalf("Run(cron logs tick) code = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "runner started") || !strings.Contains(stdout.String(), "tick output") {
		t.Fatalf("Run(cron logs tick) stdout = %q, want only the tick log", stdout.String())
	}

	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "cron", "logs", "nope"}); code != 1 {
		t.Fatalf("Run(cron logs nope) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), `cron job "nope" is not defined`) {
		t.Fatalf("Run(cron logs nope) stderr = %q, want undefined job error", stderr.String())
	}
}

// TestBuildCronRunnerSpec checks the command line serve uses to launch the
// runner.
func TestBuildCronRunnerSpec(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewProjectStore(projectDir)
	stubCronRunner(t)

	spec, err := buildCronRunnerSpec(store, "demo")
	if err != nil {
		t.Fatalf("buildCronRunnerSpec() error = %v", err)
	}
	if spec.Target != "/opt/polka/polka" {
		t.Fatalf("spec.Target = %q, want the polka executable", spec.Target)
	}
	if want := []string{"-C", store.ProjectDir, "cron", "runner", "--env", "demo"}; !reflect.DeepEqual(spec.Args, want) {
		t.Fatalf("spec.Args = %#v, want %#v", spec.Args, want)
	}
	if spec.LogPath != service.CronRunnerLogPath(store.RootDir, "demo") || spec.Dir != store.ProjectDir {
		t.Fatalf("spec = %#v, want runner log path and project dir", spec)
	}
}

// TestRunCronRunnerRefusesInactiveEnvironment checks that a runner launched
// for one environment exits when another is active, because managed tools
// resolve against the active environment.
func TestRunCronRunnerRefusesInactiveEnvironment(t *testing.T) {
	projectDir := t.TempDir()
	writeCronTestProject(t, projectDir)
	store := backend.NewProjectStore(projectDir)

	output := &bytes.Buffer{}
	if code := runCronRunner(t.Context(), output, store, "other"); code != 1 {
		t.Fatalf("runCronRunner(other) code = %d, want 1", code)
	}
	if !strings.Contains(output.String(), `started for environment "other", but "demo" is active`) {
		t.Fatalf("runCronRunner(other) output = %q, want inactive environment message", output.String())
	}
}

// TestRenderDashboardCron checks the cron section in each state.
func TestRenderDashboardCron(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 2, 0, 0, time.Local)
	unset := renderDashboard(dashboardSnapshot{EnvironmentName: "demo", TakenAt: now, WorkersUnset: true, CronUnset: true})
	if !strings.Contains(unset, "Cron") || !strings.Contains(unset, "unset") {
		t.Fatalf("renderDashboard(unset) = %q, want an unset cron section", unset)
	}

	output := renderDashboard(dashboardSnapshot{
		EnvironmentName: "demo",
		TakenAt:         now,
		WorkersUnset:    true,
		Cron:            &service.CronRunnerState{PID: 4321, StartedAt: now.Add(-5 * time.Minute)},
		CronJobs: []dashboardCronRow{
			{Name: "tick", Schedule: "*/5 * * * *", Next: time.Date(2026, 11, 15, 10, 5, 0, 0, time.Local), Last: service.CronJobRunState{LastStart: time.Date(2026, 11, 15, 10, 0, 0, 0, time.Local), LastExitCode: 0}},
			{Name: "backup", Schedule: "@daily", Next: time.Date(2026, 11, 16, 0, 0, 0, 0, time.Local)},
		},
	})
	for _, want := range []string{"pid 4321", "up 5m", "tick", "next 11-15 10:05", "last 11-15 10:00 exit 0", "backup", "last never"} {
		if !strings.Contains(output, want) {
			t.Errorf("renderDashboard() = %q, want it to contain %q", output, want)
		}
	}
}

// writeCronTestLog writes a log file, creating its directory.
func writeCronTestLog(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

// containsEnvEntry reports whether env holds the exact KEY=value entry.
func containsEnvEntry(env []string, entry string) bool {
	for _, value := range env {
		if value == entry {
			return true
		}
	}
	return false
}
