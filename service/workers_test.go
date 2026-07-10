package service

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"polka/config"
)

// workersTestHarness bundles fake worker hooks with recorders so tests can
// exercise start/stop flows without real processes.
type workersTestHarness struct {
	nextPID  int
	livePIDs map[int]bool
	started  []string // "<name>#<replica>"
	stopped  []int    // stopped PIDs in order
	failOn   string   // "<name>#<replica>" that should fail to start
}

func newWorkersTestHarness() *workersTestHarness {
	return &workersTestHarness{nextPID: 100, livePIDs: map[int]bool{}}
}

func (h *workersTestHarness) hooks() WorkersRuntimeHooks {
	return WorkersRuntimeHooks{
		StartWorker: func(spec WorkerSpec, replica int) (WorkerStartResult, error) {
			key := fmt.Sprintf("%s#%d", spec.Name, replica)
			if h.failOn == key {
				return WorkerStartResult{}, fmt.Errorf("boom")
			}
			h.nextPID++
			h.livePIDs[h.nextPID] = true
			h.started = append(h.started, key)
			return WorkerStartResult{PID: h.nextPID, LogPath: WorkerLogPath(spec.LogDir, spec.Name, replica)}, nil
		},
		StopWorker: func(state WorkerProcessState) error {
			delete(h.livePIDs, state.PID)
			h.stopped = append(h.stopped, state.PID)
			return nil
		},
		PIDIsLive: func(pid int) bool { return h.livePIDs[pid] },
		BuildEnv:  func(Context) ([]string, error) { return []string{"BASE=1"}, nil },
		Now:       func() time.Time { return time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC) },
	}
}

func workersTestContext(t *testing.T, workers map[string]config.WorkerConfig) (Context, *strings.Builder) {
	t.Helper()

	rootDir := filepath.Join(t.TempDir(), ".polka")
	warnings := &strings.Builder{}
	return Context{
		ProjectDir:  filepath.Dir(rootDir),
		RootDir:     rootDir,
		Environment: config.Environment{Name: "default", Workers: workers},
		Warnf: func(format string, args ...any) {
			warnings.WriteString(fmt.Sprintf(format, args...))
		},
	}, warnings
}

// TestEnsureManagedWorkersStartedStartsConfiguredReplicas checks the fresh
// start path: replicas launch in sorted worker order and state is persisted.
func TestEnsureManagedWorkersStartedStartsConfiguredReplicas(t *testing.T) {
	harness := newWorkersTestHarness()
	ctx, _ := workersTestContext(t, map[string]config.WorkerConfig{
		"scheduler": {Command: "php artisan schedule:work"},
		"queue":     {Command: "php artisan queue:work", Replicas: 2},
	})

	state, alreadyStarted, err := EnsureManagedWorkersStarted(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("EnsureManagedWorkersStarted() error = %v", err)
	}
	if alreadyStarted {
		t.Fatal("EnsureManagedWorkersStarted() alreadyStarted = true, want fresh start")
	}
	if want := []string{"queue#1", "queue#2", "scheduler#1"}; !reflect.DeepEqual(harness.started, want) {
		t.Fatalf("started = %#v, want %#v", harness.started, want)
	}
	if len(state.Processes) != 3 || state.EnvironmentName != "default" {
		t.Fatalf("state = %#v, want 3 processes for default", state)
	}
	if state.Processes[0].Command != "php artisan queue:work" || state.Processes[0].Replica != 1 {
		t.Fatalf("state.Processes[0] = %#v, want queue replica 1", state.Processes[0])
	}
	if !strings.HasSuffix(state.Processes[2].LogPath, "scheduler-1.log") {
		t.Fatalf("scheduler log path = %q, want scheduler-1.log suffix", state.Processes[2].LogPath)
	}

	persisted, err := LoadWorkersState(WorkersStatePath(ctx.RootDir, "default"))
	if err != nil {
		t.Fatalf("LoadWorkersState() error = %v", err)
	}
	if !reflect.DeepEqual(*persisted, state) {
		t.Fatalf("persisted state = %#v, want %#v", *persisted, state)
	}
}

// TestEnsureManagedWorkersStartedIsIdempotent checks that a matching live
// state short-circuits without starting new processes.
func TestEnsureManagedWorkersStartedIsIdempotent(t *testing.T) {
	harness := newWorkersTestHarness()
	ctx, _ := workersTestContext(t, map[string]config.WorkerConfig{
		"queue": {Command: "php artisan queue:work", Replicas: 2},
	})

	if _, _, err := EnsureManagedWorkersStarted(ctx, harness.hooks()); err != nil {
		t.Fatalf("EnsureManagedWorkersStarted(first) error = %v", err)
	}
	startsAfterFirst := len(harness.started)

	state, alreadyStarted, err := EnsureManagedWorkersStarted(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("EnsureManagedWorkersStarted(second) error = %v", err)
	}
	if !alreadyStarted {
		t.Fatal("EnsureManagedWorkersStarted(second) alreadyStarted = false, want true")
	}
	if len(harness.started) != startsAfterFirst {
		t.Fatalf("started = %#v, want no new starts", harness.started)
	}
	if len(state.Processes) != 2 {
		t.Fatalf("state.Processes = %#v, want 2 live replicas", state.Processes)
	}
}

// TestEnsureManagedWorkersStartedReconcilesDeadReplica checks that a dead
// replica triggers a full reconcile: survivors stop and everything restarts.
func TestEnsureManagedWorkersStartedReconcilesDeadReplica(t *testing.T) {
	harness := newWorkersTestHarness()
	ctx, _ := workersTestContext(t, map[string]config.WorkerConfig{
		"queue": {Command: "php artisan queue:work", Replicas: 2},
	})

	first, _, err := EnsureManagedWorkersStarted(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("EnsureManagedWorkersStarted(first) error = %v", err)
	}
	// Simulate one replica crashing.
	delete(harness.livePIDs, first.Processes[0].PID)
	survivorPID := first.Processes[1].PID

	state, alreadyStarted, err := EnsureManagedWorkersStarted(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("EnsureManagedWorkersStarted(reconcile) error = %v", err)
	}
	if alreadyStarted {
		t.Fatal("EnsureManagedWorkersStarted(reconcile) alreadyStarted = true, want restart")
	}
	if !reflect.DeepEqual(harness.stopped, []int{survivorPID}) {
		t.Fatalf("stopped = %#v, want surviving replica %d stopped", harness.stopped, survivorPID)
	}
	if len(state.Processes) != 2 {
		t.Fatalf("state.Processes = %#v, want 2 fresh replicas", state.Processes)
	}
	for _, process := range state.Processes {
		if process.PID == survivorPID {
			t.Fatalf("state kept old PID %d, want fresh processes", survivorPID)
		}
	}
}

// TestEnsureManagedWorkersStartedReconcilesChangedCommand checks that a
// changed worker command replaces the running processes.
func TestEnsureManagedWorkersStartedReconcilesChangedCommand(t *testing.T) {
	harness := newWorkersTestHarness()
	ctx, _ := workersTestContext(t, map[string]config.WorkerConfig{
		"queue": {Command: "php artisan queue:work"},
	})

	first, _, err := EnsureManagedWorkersStarted(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("EnsureManagedWorkersStarted(first) error = %v", err)
	}

	ctx.Environment.Workers = map[string]config.WorkerConfig{
		"queue": {Command: "php artisan queue:work --tries=3"},
	}
	state, alreadyStarted, err := EnsureManagedWorkersStarted(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("EnsureManagedWorkersStarted(changed) error = %v", err)
	}
	if alreadyStarted {
		t.Fatal("EnsureManagedWorkersStarted(changed) alreadyStarted = true, want restart")
	}
	if !reflect.DeepEqual(harness.stopped, []int{first.Processes[0].PID}) {
		t.Fatalf("stopped = %#v, want old replica stopped", harness.stopped)
	}
	if state.Processes[0].Command != "php artisan queue:work --tries=3" {
		t.Fatalf("state command = %q, want updated command", state.Processes[0].Command)
	}
}

// TestEnsureManagedWorkersStartedWarnsOnReplicaFailure checks that a replica
// that fails to launch produces a warning while the remaining replicas keep
// running and are persisted.
func TestEnsureManagedWorkersStartedWarnsOnReplicaFailure(t *testing.T) {
	harness := newWorkersTestHarness()
	harness.failOn = "queue#2"
	ctx, warnings := workersTestContext(t, map[string]config.WorkerConfig{
		"queue":     {Command: "php artisan queue:work", Replicas: 2},
		"scheduler": {Command: "php artisan schedule:work"},
	})

	state, alreadyStarted, err := EnsureManagedWorkersStarted(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("EnsureManagedWorkersStarted(replica failure) error = %v, want warning only", err)
	}
	if alreadyStarted {
		t.Fatal("EnsureManagedWorkersStarted(replica failure) alreadyStarted = true, want fresh start")
	}
	if !strings.Contains(warnings.String(), `Worker "queue" replica 2 for environment "default" failed to start`) {
		t.Fatalf("warnings = %q, want queue replica 2 failure warning", warnings.String())
	}
	if want := []string{"queue#1", "scheduler#1"}; !reflect.DeepEqual(harness.started, want) {
		t.Fatalf("started = %#v, want %#v", harness.started, want)
	}
	if len(harness.stopped) != 0 {
		t.Fatalf("stopped = %#v, want surviving replicas kept running", harness.stopped)
	}
	if len(state.Processes) != 2 {
		t.Fatalf("state.Processes = %#v, want the two started replicas persisted", state.Processes)
	}

	persisted, err := LoadWorkersState(WorkersStatePath(ctx.RootDir, "default"))
	if err != nil {
		t.Fatalf("LoadWorkersState() error = %v", err)
	}
	if len(persisted.Processes) != 2 {
		t.Fatalf("persisted processes = %#v, want partial state persisted for retry", persisted.Processes)
	}
}

// TestEnsureManagedWorkersStartedWarnsWhenAllReplicasFail checks that a fully
// failed start leaves no state file and reports warnings instead of an error.
func TestEnsureManagedWorkersStartedWarnsWhenAllReplicasFail(t *testing.T) {
	harness := newWorkersTestHarness()
	harness.failOn = "queue#1"
	ctx, warnings := workersTestContext(t, map[string]config.WorkerConfig{
		"queue": {Command: "php artisan queue:work"},
	})

	state, alreadyStarted, err := EnsureManagedWorkersStarted(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("EnsureManagedWorkersStarted(all failed) error = %v, want warning only", err)
	}
	if alreadyStarted || len(state.Processes) != 0 {
		t.Fatalf("state = %#v alreadyStarted = %v, want empty fresh-start state", state, alreadyStarted)
	}
	if !strings.Contains(warnings.String(), `Worker "queue" replica 1 for environment "default" failed to start`) {
		t.Fatalf("warnings = %q, want start failure warning", warnings.String())
	}
	if _, statErr := os.Stat(WorkersStatePath(ctx.RootDir, "default")); !os.IsNotExist(statErr) {
		t.Fatalf("workers state after failure error = %v, want not exists", statErr)
	}
}

// TestBuildWorkerSpecsSkipsBrokenWorkerWithWarning checks that a worker whose
// command cannot be split is skipped while valid workers still build.
func TestBuildWorkerSpecsSkipsBrokenWorkerWithWarning(t *testing.T) {
	ctx, warnings := workersTestContext(t, map[string]config.WorkerConfig{
		"broken": {Command: `php "unbalanced`},
		"queue":  {Command: "php artisan queue:work"},
	})

	specs, err := BuildWorkerSpecs(ctx, WorkersRuntimeHooks{
		BuildEnv: func(Context) ([]string, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("BuildWorkerSpecs() error = %v, want broken worker skipped", err)
	}
	if len(specs) != 1 || specs[0].Name != "queue" {
		t.Fatalf("specs = %#v, want only the valid queue worker", specs)
	}
	if !strings.Contains(warnings.String(), `Skipping worker "broken" for environment "default"`) {
		t.Fatalf("warnings = %q, want broken worker warning", warnings.String())
	}
}

// TestStopManagedWorkers checks the stop flow and its already-stopped variant.
func TestStopManagedWorkers(t *testing.T) {
	harness := newWorkersTestHarness()
	ctx, _ := workersTestContext(t, map[string]config.WorkerConfig{
		"queue": {Command: "php artisan queue:work", Replicas: 2},
	})

	started, _, err := EnsureManagedWorkersStarted(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("EnsureManagedWorkersStarted() error = %v", err)
	}

	state, alreadyStopped, err := StopManagedWorkers(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("StopManagedWorkers() error = %v", err)
	}
	if alreadyStopped {
		t.Fatal("StopManagedWorkers() alreadyStopped = true, want stopped now")
	}
	if len(state.Processes) != 2 || len(harness.stopped) != 2 {
		t.Fatalf("stop state = %#v, stopped = %#v, want both replicas stopped", state.Processes, harness.stopped)
	}
	if harness.stopped[0] != started.Processes[0].PID || harness.stopped[1] != started.Processes[1].PID {
		t.Fatalf("stopped = %#v, want recorded PIDs %d, %d", harness.stopped, started.Processes[0].PID, started.Processes[1].PID)
	}
	if _, statErr := os.Stat(WorkersStatePath(ctx.RootDir, "default")); !os.IsNotExist(statErr) {
		t.Fatalf("workers state after stop error = %v, want not exists", statErr)
	}

	_, alreadyStopped, err = StopManagedWorkers(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("StopManagedWorkers(second) error = %v", err)
	}
	if !alreadyStopped {
		t.Fatal("StopManagedWorkers(second) alreadyStopped = false, want true")
	}
}

// TestBuildWorkerSpecs checks directory resolution, replica defaults, env
// appending, and target resolution.
func TestBuildWorkerSpecs(t *testing.T) {
	ctx, _ := workersTestContext(t, map[string]config.WorkerConfig{
		"queue":     {Command: "php artisan queue:work", Replicas: 2, Dir: "app", Env: map[string]string{"B": "2", "A": "1"}},
		"scheduler": {Command: `php "my script.php"`},
	})

	resolvedArgv := [][]string{}
	hooks := WorkersRuntimeHooks{
		BuildEnv: func(Context) ([]string, error) { return []string{"BASE=1"}, nil },
		ResolveTarget: func(_ Context, argv []string, env []string) (string, []string, []string, error) {
			resolvedArgv = append(resolvedArgv, argv)
			return "/opt/php", argv[1:], env, nil
		},
	}

	specs, err := BuildWorkerSpecs(ctx, hooks)
	if err != nil {
		t.Fatalf("BuildWorkerSpecs() error = %v", err)
	}
	if len(specs) != 2 || specs[0].Name != "queue" || specs[1].Name != "scheduler" {
		t.Fatalf("specs = %#v, want queue then scheduler", specs)
	}

	queue := specs[0]
	if queue.Target != "/opt/php" || !reflect.DeepEqual(queue.Args, []string{"artisan", "queue:work"}) {
		t.Fatalf("queue spec target/args = %q %#v, want resolved target", queue.Target, queue.Args)
	}
	if queue.Replicas != 2 || specs[1].Replicas != 1 {
		t.Fatalf("replicas = %d/%d, want 2 and default 1", queue.Replicas, specs[1].Replicas)
	}
	if queue.Dir != filepath.Join(ctx.ProjectDir, "app") || specs[1].Dir != ctx.ProjectDir {
		t.Fatalf("dirs = %q/%q, want project-relative resolution", queue.Dir, specs[1].Dir)
	}
	if want := []string{"BASE=1", "A=1", "B=2"}; !reflect.DeepEqual(queue.Env, want) {
		t.Fatalf("queue env = %#v, want base env then sorted worker env %#v", queue.Env, want)
	}
	if !reflect.DeepEqual(resolvedArgv[1], []string{"php", "my script.php"}) {
		t.Fatalf("scheduler argv = %#v, want quoted argument split", resolvedArgv[1])
	}
}

// TestWorkersStateMatchesEnvironment checks the reconcile predicate.
func TestWorkersStateMatchesEnvironment(t *testing.T) {
	environment := config.Environment{
		Name: "default",
		Workers: map[string]config.WorkerConfig{
			"queue": {Command: "php artisan queue:work", Replicas: 2},
		},
	}
	matching := WorkersRuntimeState{
		EnvironmentName: "default",
		Processes: []WorkerProcessState{
			{Name: "queue", Replica: 1, PID: 101, Command: "php artisan queue:work"},
			{Name: "queue", Replica: 2, PID: 102, Command: "php artisan queue:work"},
		},
	}
	if !WorkersStateMatchesEnvironment(matching, environment) {
		t.Fatal("WorkersStateMatchesEnvironment(matching) = false, want true")
	}

	missingReplica := matching
	missingReplica.Processes = matching.Processes[:1]
	if WorkersStateMatchesEnvironment(missingReplica, environment) {
		t.Fatal("WorkersStateMatchesEnvironment(missing replica) = true, want false")
	}

	changedCommand := WorkersRuntimeState{
		EnvironmentName: "default",
		Processes: []WorkerProcessState{
			{Name: "queue", Replica: 1, PID: 101, Command: "php artisan queue:work --old"},
			{Name: "queue", Replica: 2, PID: 102, Command: "php artisan queue:work --old"},
		},
	}
	if WorkersStateMatchesEnvironment(changedCommand, environment) {
		t.Fatal("WorkersStateMatchesEnvironment(changed command) = true, want false")
	}

	extraWorker := matching
	extraWorker.Processes = append(append([]WorkerProcessState(nil), matching.Processes...), WorkerProcessState{Name: "stale", Replica: 1, PID: 103, Command: "php old"})
	if WorkersStateMatchesEnvironment(extraWorker, environment) {
		t.Fatal("WorkersStateMatchesEnvironment(extra worker) = true, want false")
	}
}

// TestLoadLiveWorkersStatePrunesDeadReplicas checks dead-PID pruning and
// stale-file cleanup.
func TestLoadLiveWorkersStatePrunesDeadReplicas(t *testing.T) {
	rootDir := filepath.Join(t.TempDir(), ".polka")
	path := WorkersStatePath(rootDir, "default")
	state := WorkersRuntimeState{
		EnvironmentName: "default",
		Processes: []WorkerProcessState{
			{Name: "queue", Replica: 1, PID: 101, Command: "php run"},
			{Name: "queue", Replica: 2, PID: 102, Command: "php run"},
		},
	}
	if err := WriteWorkersState(path, state); err != nil {
		t.Fatalf("WriteWorkersState() error = %v", err)
	}

	live, err := LoadLiveWorkersState(rootDir, "default", func(pid int) bool { return pid == 102 })
	if err != nil {
		t.Fatalf("LoadLiveWorkersState() error = %v", err)
	}
	if live == nil || len(live.Processes) != 1 || live.Processes[0].PID != 102 {
		t.Fatalf("live state = %#v, want only PID 102", live)
	}

	none, err := LoadLiveWorkersState(rootDir, "default", func(int) bool { return false })
	if err != nil {
		t.Fatalf("LoadLiveWorkersState(all dead) error = %v", err)
	}
	if none != nil {
		t.Fatalf("live state = %#v, want nil when all replicas are dead", none)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("state file after prune error = %v, want removed", statErr)
	}
}

// TestManagerStartsWorkersEvenWhenPHPMyAdminSkipped guards the restructured
// PHPMyAdmin missing-tool branch in Manager.Start: workers configured after a
// skipped phpMyAdmin must still start.
func TestManagerStartsWorkersEvenWhenPHPMyAdminSkipped(t *testing.T) {
	harness := newWorkersTestHarness()
	ctx, warnings := managerTestContext(t, config.Environment{
		Name:       "demo",
		PHPMyAdmin: &config.PHPMyAdminConfig{Version: "5.2"},
		Workers: map[string]config.WorkerConfig{
			"queue": {Command: "php artisan queue:work"},
		},
	})

	result, err := DefaultManager().Start(ctx, RuntimeHooks{Workers: harness.hooks()})
	if err != nil {
		t.Fatalf("Start(workers with skipped phpmyadmin) error = %v", err)
	}
	if !strings.Contains(warnings.String(), `matching tool "phpmyadmin" is not registered`) {
		t.Fatalf("warnings = %q, want missing phpmyadmin warning", warnings.String())
	}
	if result.Workers == nil || result.Workers.AlreadyStarted || len(result.Workers.State.Processes) != 1 {
		t.Fatalf("workers result = %#v, want one started replica", result.Workers)
	}
}

// TestManagerStopStopsWorkersFirst checks that Manager.Stop stops workers
// before the managed services they depend on.
func TestManagerStopStopsWorkersFirst(t *testing.T) {
	harness := newWorkersTestHarness()
	ctx, _ := managerTestContext(t, config.Environment{
		Name: "demo",
		Workers: map[string]config.WorkerConfig{
			"queue": {Command: "php artisan queue:work"},
		},
	})
	if _, _, err := EnsureManagedWorkersStarted(ctx, harness.hooks()); err != nil {
		t.Fatalf("EnsureManagedWorkersStarted() error = %v", err)
	}

	// A live mailpit state lets Stop exercise a second service so the
	// relative stop order is observable.
	mailpitState := MailpitRuntimeState{
		EnvironmentName: "demo",
		Version:         "1.30",
		SMTPPort:        1125,
		UIPort:          8125,
		PID:             os.Getpid(),
		StartedAt:       time.Now().UTC(),
	}
	if err := WriteMailpitState(MailpitStatePath(ctx.RootDir, "demo"), mailpitState); err != nil {
		t.Fatalf("WriteMailpitState() error = %v", err)
	}

	order := []string{}
	hooks := RuntimeHooks{Workers: harness.hooks()}
	hooks.Workers.StopWorker = func(state WorkerProcessState) error {
		order = append(order, "workers")
		delete(harness.livePIDs, state.PID)
		return nil
	}
	hooks.Mailpit.PingAddress = func(address string) bool {
		return address == MailpitAddress(1125) || address == MailpitAddress(8125)
	}
	hooks.Mailpit.StopServer = func(MailpitRuntimeState) error {
		order = append(order, "mailpit")
		return nil
	}

	result, err := DefaultManager().Stop(ctx, hooks)
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if result.Workers == nil || result.Workers.AlreadyStopped {
		t.Fatalf("workers stop result = %#v, want stopped", result.Workers)
	}
	if want := []string{"workers", "mailpit"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("stop order = %#v, want %#v", order, want)
	}
}
