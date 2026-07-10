package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"polka/backend"
	"polka/config"
	"polka/service"
)

// writeWorkersTestProject writes a project config whose "demo" environment
// defines one queue worker, and marks "demo" active.
func writeWorkersTestProject(t *testing.T, projectDir string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), []byte("version: 1\nroot: .polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(polka.yaml) error = %v", err)
	}
	demoConfig := strings.Join([]string{
		"tools:",
		"  php: \"8.4\"",
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

// TestRunStopStopsWorkersWhenConfigured checks that `polka stop` stops
// recorded worker replicas through the injected hooks and removes the state.
func TestRunStopStopsWorkersWhenConfigured(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	writeWorkersTestProject(t, projectDir)

	state := service.WorkersRuntimeState{
		EnvironmentName: "demo",
		Processes: []service.WorkerProcessState{
			{Name: "queue", Replica: 1, PID: 4242, Command: "php artisan queue:work"},
		},
	}
	if err := service.WriteWorkersState(service.WorkersStatePath(root, "demo"), state); err != nil {
		t.Fatalf("WriteWorkersState() error = %v", err)
	}

	oldStopWorker := stopWorkerProcessFunc
	oldPIDIsLive := workerPIDIsLiveFunc
	t.Cleanup(func() {
		stopWorkerProcessFunc = oldStopWorker
		workerPIDIsLiveFunc = oldPIDIsLive
	})

	live := map[int]bool{4242: true}
	stopped := []int{}
	stopWorkerProcessFunc = func(process service.WorkerProcessState) error {
		stopped = append(stopped, process.PID)
		delete(live, process.PID)
		return nil
	}
	workerPIDIsLiveFunc = func(pid int) bool { return live[pid] }

	if code := Run(stdout, stderr, []string{"--root", root, "stop"}); code != 0 {
		t.Fatalf("Run(stop) code = %d, stderr = %q", code, stderr.String())
	}
	if len(stopped) != 1 || stopped[0] != 4242 {
		t.Fatalf("stopped PIDs = %#v, want [4242]", stopped)
	}
	if !strings.Contains(stdout.String(), "Stopped 1 worker process(es) for environment \"demo\".") {
		t.Fatalf("Run(stop) stdout = %q, want workers stop summary", stdout.String())
	}
	if _, err := os.Stat(service.WorkersStatePath(root, "demo")); !os.IsNotExist(err) {
		t.Fatalf("Stat(workers state) error = %v, want not exists", err)
	}
}

// TestRunStopReportsWorkersAlreadyStopped checks the already-stopped summary
// when workers are configured but nothing is running.
func TestRunStopReportsWorkersAlreadyStopped(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	writeWorkersTestProject(t, projectDir)

	if code := Run(stdout, stderr, []string{"--root", root, "stop"}); code != 0 {
		t.Fatalf("Run(stop) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Workers for environment \"demo\" are already stopped.") {
		t.Fatalf("Run(stop) stdout = %q, want workers already-stopped summary", stdout.String())
	}
}

// TestWorkerProcessSummary checks the human-readable replica summary.
func TestWorkerProcessSummary(t *testing.T) {
	summary := workerProcessSummary(workersRuntimeState{
		Processes: []service.WorkerProcessState{
			{Name: "scheduler", Replica: 1},
			{Name: "queue", Replica: 1},
			{Name: "queue", Replica: 2},
		},
	})
	if summary != "queue x2, scheduler" {
		t.Fatalf("workerProcessSummary() = %q, want %q", summary, "queue x2, scheduler")
	}
}

// TestResolveWorkerTarget checks path passthrough and the shell-PATH fallback
// for commands that are not managed tools.
func TestResolveWorkerTarget(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewProjectStore(projectDir)

	absolute := filepath.Join(projectDir, "bin", "worker")
	target, args, _, err := resolveWorkerTarget(store, []string{absolute, "--flag"}, []string{"PATH="})
	if err != nil {
		t.Fatalf("resolveWorkerTarget(absolute) error = %v", err)
	}
	if target != absolute || len(args) != 1 || args[0] != "--flag" {
		t.Fatalf("resolveWorkerTarget(absolute) = %q %#v, want passthrough", target, args)
	}

	binDir := filepath.Join(projectDir, "tools")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(tools) error = %v", err)
	}
	executable := filepath.Join(binDir, "mytool")
	if runtime.GOOS == "windows" {
		executable += ".bat"
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(mytool) error = %v", err)
	}

	target, args, _, err = resolveWorkerTarget(store, []string{"mytool", "run"}, []string{"PATH=" + binDir})
	if err != nil {
		t.Fatalf("resolveWorkerTarget(path fallback) error = %v", err)
	}
	if target != executable || len(args) != 1 || args[0] != "run" {
		t.Fatalf("resolveWorkerTarget(path fallback) = %q %#v, want %q", target, args, executable)
	}
}

// TestStatusWorkersHooks checks the config and runtime status lines.
func TestStatusWorkersHooks(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewProjectStore(projectDir)

	// Unset: both hooks report the absence of workers.
	stdout := &bytes.Buffer{}
	ctx := statusHookContext{Stdout: stdout, Store: store, Environment: backend.Environment{Name: "demo"}}
	if err := statusWorkersConfigHook(ctx); err != nil {
		t.Fatalf("statusWorkersConfigHook(unset) error = %v", err)
	}
	if err := statusWorkersRuntimeHook(ctx); err != nil {
		t.Fatalf("statusWorkersRuntimeHook(unset) error = %v", err)
	}
	if got := stdout.String(); got != "workers unset\nworkers unset\n" {
		t.Fatalf("status output = %q, want workers unset lines", got)
	}

	environment := backend.Environment{
		Name: "demo",
		Workers: map[string]config.WorkerConfig{
			"queue":     {Command: "php artisan queue:work", Replicas: 2},
			"scheduler": {Command: "php artisan schedule:work"},
		},
	}

	stdout.Reset()
	ctx = statusHookContext{Stdout: stdout, Store: store, Environment: environment}
	if err := statusWorkersConfigHook(ctx); err != nil {
		t.Fatalf("statusWorkersConfigHook() error = %v", err)
	}
	wantConfig := "worker queue: php artisan queue:work (replicas 2)\nworker scheduler: php artisan schedule:work\n"
	if stdout.String() != wantConfig {
		t.Fatalf("config status = %q, want %q", stdout.String(), wantConfig)
	}

	// Stopped: no state file exists.
	stdout.Reset()
	if err := statusWorkersRuntimeHook(ctx); err != nil {
		t.Fatalf("statusWorkersRuntimeHook(stopped) error = %v", err)
	}
	if stdout.String() != "workers stopped\n" {
		t.Fatalf("runtime status = %q, want workers stopped", stdout.String())
	}

	// Running: live replicas over configured replicas.
	state := service.WorkersRuntimeState{
		EnvironmentName: "demo",
		Processes: []service.WorkerProcessState{
			{Name: "queue", Replica: 1, PID: 101, Command: "php artisan queue:work"},
			{Name: "queue", Replica: 2, PID: 102, Command: "php artisan queue:work"},
			{Name: "scheduler", Replica: 1, PID: 103, Command: "php artisan schedule:work"},
		},
	}
	if err := service.WriteWorkersState(service.WorkersStatePath(store.RootDir, "demo"), state); err != nil {
		t.Fatalf("WriteWorkersState() error = %v", err)
	}
	oldPIDIsLive := workerPIDIsLiveFunc
	t.Cleanup(func() { workerPIDIsLiveFunc = oldPIDIsLive })
	workerPIDIsLiveFunc = func(pid int) bool { return pid != 103 }

	stdout.Reset()
	if err := statusWorkersRuntimeHook(ctx); err != nil {
		t.Fatalf("statusWorkersRuntimeHook(running) error = %v", err)
	}
	if stdout.String() != "workers running 2/3\n" {
		t.Fatalf("runtime status = %q, want workers running 2/3", stdout.String())
	}
}
