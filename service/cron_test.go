package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"polka/config"
)

// cronTestTimeout bounds every wait on the scheduler goroutine so a broken
// scheduler fails the test instead of hanging it.
const cronTestTimeout = 5 * time.Second

// fakeCronClock is a manual clock for RunCronScheduler. After registers a
// waiter that fires when the clock is advanced past its deadline, and each
// registration is announced on registered so tests know the scheduler is idle.
type fakeCronClock struct {
	mu         sync.Mutex
	now        time.Time
	waiters    []fakeCronWaiter
	registered chan struct{}
}

// fakeCronWaiter is one pending After call.
type fakeCronWaiter struct {
	deadline time.Time
	channel  chan time.Time
}

// newFakeCronClock returns a clock reading start.
func newFakeCronClock(start time.Time) *fakeCronClock {
	return &fakeCronClock{now: start, registered: make(chan struct{}, 64)}
}

// Now returns the current fake time.
func (c *fakeCronClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After registers a waiter due d after the current fake time.
func (c *fakeCronClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	channel := make(chan time.Time, 1)
	c.waiters = append(c.waiters, fakeCronWaiter{deadline: c.now.Add(d), channel: channel})
	c.mu.Unlock()
	c.registered <- struct{}{}
	return channel
}

// Advance sets the clock to t and fires the waiters that are now due.
func (c *fakeCronClock) Advance(t time.Time) {
	c.set(t, false)
}

// FireAt sets the clock to t and fires every waiter regardless of deadline,
// simulating a timer that expires after the wall clock was set back.
func (c *fakeCronClock) FireAt(t time.Time) {
	c.set(t, true)
}

// set moves the clock and fires due (or all) waiters.
func (c *fakeCronClock) set(t time.Time, all bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
	remaining := c.waiters[:0]
	for _, waiter := range c.waiters {
		if all || !waiter.deadline.After(t) {
			waiter.channel <- t
			continue
		}
		remaining = append(remaining, waiter)
	}
	c.waiters = remaining
}

// awaitIdle waits until the scheduler registers its next After call.
func (c *fakeCronClock) awaitIdle(t *testing.T) {
	t.Helper()
	select {
	case <-c.registered:
	case <-time.After(cronTestTimeout):
		t.Fatal("scheduler did not wait for the next minute")
	}
}

// cronRunRecorder is a fake RunJob that counts runs per job and can hold a
// job until released.
type cronRunRecorder struct {
	mu      sync.Mutex
	runs    map[string]int
	block   map[string]chan struct{}
	started chan string
	exit    map[string]int
	err     map[string]error
}

// newCronRunRecorder returns an empty recorder.
func newCronRunRecorder() *cronRunRecorder {
	return &cronRunRecorder{
		runs:    map[string]int{},
		block:   map[string]chan struct{}{},
		started: make(chan string, 64),
		exit:    map[string]int{},
		err:     map[string]error{},
	}
}

// run records one job run, blocking while the job has a hold channel.
func (r *cronRunRecorder) run(ctx context.Context, spec CronJobSpec) (int, error) {
	r.mu.Lock()
	r.runs[spec.Name]++
	hold := r.block[spec.Name]
	exitCode, err := r.exit[spec.Name], r.err[spec.Name]
	r.mu.Unlock()
	r.started <- spec.Name
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
		}
	}
	return exitCode, err
}

// count returns how many times a job has run.
func (r *cronRunRecorder) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[name]
}

// awaitStart waits for the named job to start.
func (r *cronRunRecorder) awaitStart(t *testing.T, name string) {
	t.Helper()
	select {
	case got := <-r.started:
		if got != name {
			t.Fatalf("started job = %q, want %q", got, name)
		}
	case <-time.After(cronTestTimeout):
		t.Fatalf("job %q did not start", name)
	}
}

// awaitStarts waits for the named jobs to start, in any order, since jobs due
// in the same minute run concurrently.
func (r *cronRunRecorder) awaitStarts(t *testing.T, names ...string) {
	t.Helper()
	want := map[string]int{}
	for _, name := range names {
		want[name]++
	}
	for range names {
		select {
		case got := <-r.started:
			if want[got] == 0 {
				t.Fatalf("started job = %q, want one of %v", got, names)
			}
			want[got]--
		case <-time.After(cronTestTimeout):
			t.Fatalf("jobs %v did not all start", names)
		}
	}
}

// cronHistoryRecorder captures every saved run history and the log lines.
type cronHistoryRecorder struct {
	mu    sync.Mutex
	saves []CronRunsState
	logs  []string
}

// save records one saved history.
func (r *cronHistoryRecorder) save(state CronRunsState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saves = append(r.saves, state)
	return nil
}

// logf records one log line.
func (r *cronHistoryRecorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

// last returns the most recently saved history.
func (r *cronHistoryRecorder) last() CronRunsState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.saves) == 0 {
		return CronRunsState{}
	}
	return r.saves[len(r.saves)-1]
}

// logText returns every log line joined.
func (r *cronHistoryRecorder) logText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.logs, "")
}

// awaitHistory waits until the latest saved history satisfies done.
func (r *cronHistoryRecorder) awaitHistory(t *testing.T, done func(CronRunsState) bool) {
	t.Helper()
	deadline := time.Now().Add(cronTestTimeout)
	for time.Now().Before(deadline) {
		if done(r.last()) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run history never reached the expected state; last = %#v", r.last())
}

// cronTestJob returns a job spec with a parsed schedule.
func cronTestJob(t *testing.T, name, schedule string) CronJobSpec {
	t.Helper()
	parsed, err := config.ParseCronSchedule(schedule)
	if err != nil {
		t.Fatalf("ParseCronSchedule(%q) error = %v", schedule, err)
	}
	return CronJobSpec{Name: name, Schedule: parsed, Command: "php " + name + ".php"}
}

// cronTestMinute returns a UTC time on 2026-11-15.
func cronTestMinute(hour, minute, second int) time.Time {
	return time.Date(2026, 11, 15, hour, minute, second, 0, time.UTC)
}

// startCronTestScheduler runs RunCronScheduler in the background and returns
// a stop function that cancels it and waits for it to return.
func startCronTestScheduler(t *testing.T, jobs []CronJobSpec, history CronRunsState, hooks CronSchedulerHooks) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunCronScheduler(ctx, jobs, history, hooks) }()

	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("RunCronScheduler() error = %v", err)
			}
		case <-time.After(cronTestTimeout):
			t.Fatal("RunCronScheduler did not return after cancel")
		}
	}
}

// TestRunCronSchedulerDispatchesDueJobs checks that only jobs matching the
// current minute run, and that a forward clock jump runs the current minute
// once without catching up the minutes in between.
func TestRunCronSchedulerDispatchesDueJobs(t *testing.T) {
	clock := newFakeCronClock(cronTestMinute(10, 0, 30))
	runs := newCronRunRecorder()
	stop := startCronTestScheduler(t, []CronJobSpec{
		cronTestJob(t, "every", "* * * * *"),
		cronTestJob(t, "hourly", "0 * * * *"),
	}, CronRunsState{}, CronSchedulerHooks{Now: clock.Now, After: clock.After, RunJob: runs.run})
	defer stop()

	clock.awaitIdle(t)
	clock.Advance(cronTestMinute(10, 1, 0))
	runs.awaitStart(t, "every")
	clock.awaitIdle(t)

	clock.Advance(cronTestMinute(11, 0, 0))
	runs.awaitStarts(t, "every", "hourly")
	clock.awaitIdle(t)

	if got := runs.count("every"); got != 2 {
		t.Fatalf("every ran %d times, want 2 (no catch-up for skipped minutes)", got)
	}
	if got := runs.count("hourly"); got != 1 {
		t.Fatalf("hourly ran %d times, want 1", got)
	}
}

// TestRunCronSchedulerStartsAtNextMinute checks that the minute the runner
// starts in is not evaluated, matching cron.
func TestRunCronSchedulerStartsAtNextMinute(t *testing.T) {
	clock := newFakeCronClock(cronTestMinute(10, 0, 0))
	runs := newCronRunRecorder()
	stop := startCronTestScheduler(t, []CronJobSpec{cronTestJob(t, "every", "* * * * *")}, CronRunsState{}, CronSchedulerHooks{Now: clock.Now, After: clock.After, RunJob: runs.run})
	defer stop()

	clock.awaitIdle(t)
	if got := runs.count("every"); got != 0 {
		t.Fatalf("every ran %d times before the first boundary, want 0", got)
	}
	clock.Advance(cronTestMinute(10, 1, 0))
	runs.awaitStart(t, "every")
}

// TestRunCronSchedulerSkipsOverlappingRun checks that a job still running at
// its next due minute is skipped and logged, then runs again once finished.
func TestRunCronSchedulerSkipsOverlappingRun(t *testing.T) {
	clock := newFakeCronClock(cronTestMinute(10, 0, 30))
	runs := newCronRunRecorder()
	release := make(chan struct{})
	runs.block["slow"] = release
	history := &cronHistoryRecorder{}
	stop := startCronTestScheduler(t, []CronJobSpec{cronTestJob(t, "slow", "* * * * *")}, CronRunsState{}, CronSchedulerHooks{
		Now: clock.Now, After: clock.After, RunJob: runs.run, SaveRuns: history.save, Logf: history.logf,
	})
	defer stop()

	clock.awaitIdle(t)
	clock.Advance(cronTestMinute(10, 1, 0))
	runs.awaitStart(t, "slow")
	clock.awaitIdle(t)

	clock.Advance(cronTestMinute(10, 2, 0))
	clock.awaitIdle(t)
	if got := runs.count("slow"); got != 1 {
		t.Fatalf("slow ran %d times while still running, want 1", got)
	}
	if !strings.Contains(history.logText(), `Skipping cron job "slow"`) {
		t.Fatalf("logs = %q, want overlap skip message", history.logText())
	}

	close(release)
	history.awaitHistory(t, func(state CronRunsState) bool { return !state.Jobs["slow"].Running })
	clock.Advance(cronTestMinute(10, 3, 0))
	runs.awaitStart(t, "slow")
	if got := runs.count("slow"); got != 2 {
		t.Fatalf("slow ran %d times after finishing, want 2", got)
	}
}

// TestRunCronSchedulerSmallBackwardStepNeverRepeatsMinute checks that after
// the clock steps back a little, already-evaluated minutes are not run again.
func TestRunCronSchedulerSmallBackwardStepNeverRepeatsMinute(t *testing.T) {
	clock := newFakeCronClock(cronTestMinute(10, 4, 30))
	runs := newCronRunRecorder()
	stop := startCronTestScheduler(t, []CronJobSpec{cronTestJob(t, "every", "* * * * *")}, CronRunsState{}, CronSchedulerHooks{Now: clock.Now, After: clock.After, RunJob: runs.run})
	defer stop()

	clock.awaitIdle(t)
	clock.Advance(cronTestMinute(10, 5, 0))
	runs.awaitStart(t, "every")
	clock.awaitIdle(t)

	clock.FireAt(cronTestMinute(10, 3, 0))
	clock.awaitIdle(t)
	clock.FireAt(cronTestMinute(10, 5, 0))
	clock.awaitIdle(t)
	if got := runs.count("every"); got != 1 {
		t.Fatalf("every ran %d times after stepping back, want 1", got)
	}

	clock.Advance(cronTestMinute(10, 6, 0))
	runs.awaitStart(t, "every")
}

// TestRunCronSchedulerLargeBackwardStepResumes checks that a clock set back
// further than the reset threshold resumes scheduling from the new time.
func TestRunCronSchedulerLargeBackwardStepResumes(t *testing.T) {
	clock := newFakeCronClock(cronTestMinute(10, 4, 30))
	runs := newCronRunRecorder()
	history := &cronHistoryRecorder{}
	stop := startCronTestScheduler(t, []CronJobSpec{cronTestJob(t, "every", "* * * * *")}, CronRunsState{}, CronSchedulerHooks{
		Now: clock.Now, After: clock.After, RunJob: runs.run, Logf: history.logf,
	})
	defer stop()

	clock.awaitIdle(t)
	clock.Advance(cronTestMinute(10, 5, 0))
	runs.awaitStart(t, "every")
	clock.awaitIdle(t)

	clock.FireAt(cronTestMinute(9, 0, 0))
	clock.awaitIdle(t)
	clock.Advance(cronTestMinute(9, 1, 0))
	runs.awaitStart(t, "every")
	if !strings.Contains(history.logText(), "Clock moved back") {
		t.Fatalf("logs = %q, want clock reset message", history.logText())
	}
}

// TestRunCronSchedulerCancelWaitsForRunningJobs checks that cancelling the
// scheduler cancels in-flight jobs and returns only after they finish.
func TestRunCronSchedulerCancelWaitsForRunningJobs(t *testing.T) {
	clock := newFakeCronClock(cronTestMinute(10, 0, 30))
	jobDone := false
	var mu sync.Mutex
	runJob := func(ctx context.Context, spec CronJobSpec) (int, error) {
		<-ctx.Done()
		// Simulate a job that takes a moment to exit after being killed.
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		jobDone = true
		mu.Unlock()
		return -1, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		if err := RunCronScheduler(ctx, []CronJobSpec{cronTestJob(t, "long", "* * * * *")}, CronRunsState{}, CronSchedulerHooks{Now: clock.Now, After: clock.After, RunJob: runJob}); err != nil {
			t.Errorf("RunCronScheduler() error = %v", err)
		}
		close(done)
	}()

	clock.awaitIdle(t)
	clock.Advance(cronTestMinute(10, 1, 0))
	clock.awaitIdle(t)
	cancel()
	select {
	case <-done:
	case <-time.After(cronTestTimeout):
		t.Fatal("RunCronScheduler did not return after cancel")
	}
	mu.Lock()
	defer mu.Unlock()
	if !jobDone {
		t.Fatal("RunCronScheduler returned before the running job finished")
	}
}

// TestRunCronSchedulerRecordsHistory checks that history is seeded from the
// previous runner, stale running flags are cleared, and each run's outcome
// is saved.
func TestRunCronSchedulerRecordsHistory(t *testing.T) {
	clock := newFakeCronClock(cronTestMinute(10, 0, 30))
	runs := newCronRunRecorder()
	runs.exit["fail"] = 3
	runs.err["broken"] = fmt.Errorf("exec: not found")
	history := &cronHistoryRecorder{}
	previous := CronRunsState{EnvironmentName: "default", Jobs: map[string]CronJobRunState{
		"fail": {Running: true, LastExitCode: 0},
		"gone": {LastExitCode: 1},
	}}
	stop := startCronTestScheduler(t, []CronJobSpec{
		cronTestJob(t, "fail", "* * * * *"),
		cronTestJob(t, "broken", "* * * * *"),
	}, previous, CronSchedulerHooks{Now: clock.Now, After: clock.After, RunJob: runs.run, SaveRuns: history.save, Logf: history.logf})
	defer stop()

	clock.awaitIdle(t)
	initial := history.last()
	if initial.Jobs["fail"].Running {
		t.Fatalf("initial history = %#v, want stale running flag cleared", initial)
	}
	if initial.Jobs["gone"].LastExitCode != 1 {
		t.Fatalf("initial history = %#v, want earlier results kept", initial)
	}

	clock.Advance(cronTestMinute(10, 1, 0))
	history.awaitHistory(t, func(state CronRunsState) bool {
		return !state.Jobs["fail"].LastFinish.IsZero() && !state.Jobs["broken"].LastFinish.IsZero()
	})
	final := history.last()
	if got := final.Jobs["fail"]; got.LastExitCode != 3 || got.Running || !got.LastStart.Equal(cronTestMinute(10, 1, 0)) {
		t.Fatalf("fail history = %#v, want exit 3 started at 10:01", got)
	}
	if got := final.Jobs["broken"]; got.LastError != "exec: not found" {
		t.Fatalf("broken history = %#v, want launch error recorded", got)
	}
	if !strings.Contains(history.logText(), `Cron job "broken" failed`) {
		t.Fatalf("logs = %q, want launch failure logged", history.logText())
	}
}

// TestCronRunsStateRoundTrip checks that the run history survives a write
// and read, and that a missing file reads as empty history.
func TestCronRunsStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	empty, err := LoadCronRunsState(path)
	if err != nil || len(empty.Jobs) != 0 {
		t.Fatalf("LoadCronRunsState(missing) = %#v, %v, want empty history", empty, err)
	}

	state := CronRunsState{EnvironmentName: "default", Jobs: map[string]CronJobRunState{
		"tick": {LastStart: cronTestMinute(10, 1, 0), LastFinish: cronTestMinute(10, 1, 2), LastExitCode: 2, LastError: "boom"},
	}}
	if err := WriteCronRunsState(path, state); err != nil {
		t.Fatalf("WriteCronRunsState() error = %v", err)
	}
	loaded, err := LoadCronRunsState(path)
	if err != nil {
		t.Fatalf("LoadCronRunsState() error = %v", err)
	}
	if !reflect.DeepEqual(loaded, state) {
		t.Fatalf("LoadCronRunsState() = %#v, want %#v", loaded, state)
	}
}

// cronRuntimeHarness bundles fake runner hooks with recorders so lifecycle
// tests run without real processes.
type cronRuntimeHarness struct {
	nextPID  int
	livePIDs map[int]bool
	started  []CronRunnerSpec
	stopped  []int
	order    *[]string
}

// newCronRuntimeHarness returns a harness with no live runners.
func newCronRuntimeHarness() *cronRuntimeHarness {
	return &cronRuntimeHarness{nextPID: 500, livePIDs: map[int]bool{}}
}

// hooks returns runtime hooks backed by the harness.
func (h *cronRuntimeHarness) hooks() CronRuntimeHooks {
	return CronRuntimeHooks{
		BuildRunnerSpec: func(ctx Context) (CronRunnerSpec, error) {
			return CronRunnerSpec{
				EnvironmentName: ctx.Environment.Name,
				Target:          "polka",
				Args:            []string{"cron", "runner", "--env", ctx.Environment.Name},
				LogPath:         CronRunnerLogPath(ctx.RootDir, ctx.Environment.Name),
			}, nil
		},
		StartRunner: func(spec CronRunnerSpec) (int, error) {
			h.nextPID++
			h.livePIDs[h.nextPID] = true
			h.started = append(h.started, spec)
			if h.order != nil {
				*h.order = append(*h.order, "cron-start")
			}
			return h.nextPID, nil
		},
		StopRunner: func(state CronRunnerState) error {
			delete(h.livePIDs, state.PID)
			h.stopped = append(h.stopped, state.PID)
			if h.order != nil {
				*h.order = append(*h.order, "cron-stop")
			}
			return nil
		},
		PIDIsLive: func(pid int) bool { return h.livePIDs[pid] },
		Now:       func() time.Time { return cronTestMinute(12, 0, 0) },
	}
}

// cronTestContext returns a service context for an environment with the
// given cron jobs.
func cronTestContext(t *testing.T, jobs map[string]config.CronJobConfig) (Context, *strings.Builder) {
	t.Helper()
	rootDir := filepath.Join(t.TempDir(), ".polka")
	warnings := &strings.Builder{}
	return Context{
		ProjectDir:  filepath.Dir(rootDir),
		RootDir:     rootDir,
		Environment: config.Environment{Name: "default", Cron: jobs},
		Warnf: func(format string, args ...any) {
			warnings.WriteString(fmt.Sprintf(format, args...))
		},
	}, warnings
}

// TestEnsureManagedCronStartedStartsRunner checks the fresh start path: the
// runner launches from the built spec and its state is persisted.
func TestEnsureManagedCronStartedStartsRunner(t *testing.T) {
	harness := newCronRuntimeHarness()
	jobs := map[string]config.CronJobConfig{
		"tick":   {Schedule: "* * * * *", Command: "php tick.php"},
		"backup": {Schedule: "@daily", Command: "php backup.php"},
	}
	ctx, _ := cronTestContext(t, jobs)

	state, alreadyStarted, err := EnsureManagedCronStarted(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("EnsureManagedCronStarted() error = %v", err)
	}
	if alreadyStarted {
		t.Fatal("alreadyStarted = true, want false on first start")
	}
	if len(harness.started) != 1 || !reflect.DeepEqual(harness.started[0].Args, []string{"cron", "runner", "--env", "default"}) {
		t.Fatalf("started = %#v, want one runner launched from the spec", harness.started)
	}
	fingerprint, err := CronFingerprint(jobs)
	if err != nil {
		t.Fatalf("CronFingerprint() error = %v", err)
	}
	if state.PID != 501 || state.Fingerprint != fingerprint || !reflect.DeepEqual(state.Jobs, []string{"backup", "tick"}) {
		t.Fatalf("state = %#v, want pid 501, fingerprint, and sorted jobs", state)
	}

	persisted, err := LoadCronState(CronStatePath(ctx.RootDir, "default"))
	if err != nil {
		t.Fatalf("LoadCronState() error = %v", err)
	}
	if !reflect.DeepEqual(*persisted, state) {
		t.Fatalf("persisted state = %#v, want %#v", *persisted, state)
	}
}

// TestEnsureManagedCronStartedIsIdempotent checks that a live runner started
// from the same config is reported as already running.
func TestEnsureManagedCronStartedIsIdempotent(t *testing.T) {
	harness := newCronRuntimeHarness()
	ctx, _ := cronTestContext(t, map[string]config.CronJobConfig{"tick": {Schedule: "* * * * *", Command: "php tick.php"}})

	if _, _, err := EnsureManagedCronStarted(ctx, harness.hooks()); err != nil {
		t.Fatalf("EnsureManagedCronStarted(first) error = %v", err)
	}
	state, alreadyStarted, err := EnsureManagedCronStarted(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("EnsureManagedCronStarted(second) error = %v", err)
	}
	if !alreadyStarted || state.PID != 501 || len(harness.started) != 1 {
		t.Fatalf("second start = %#v, %v, started %d; want the existing runner", state, alreadyStarted, len(harness.started))
	}
}

// TestEnsureManagedCronStartedRestartsOnConfigChange checks that editing the
// cron config replaces the running runner.
func TestEnsureManagedCronStartedRestartsOnConfigChange(t *testing.T) {
	harness := newCronRuntimeHarness()
	ctx, _ := cronTestContext(t, map[string]config.CronJobConfig{"tick": {Schedule: "* * * * *", Command: "php tick.php"}})
	if _, _, err := EnsureManagedCronStarted(ctx, harness.hooks()); err != nil {
		t.Fatalf("EnsureManagedCronStarted(first) error = %v", err)
	}

	ctx.Environment.Cron = map[string]config.CronJobConfig{"tick": {Schedule: "*/5 * * * *", Command: "php tick.php"}}
	state, alreadyStarted, err := EnsureManagedCronStarted(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("EnsureManagedCronStarted(changed) error = %v", err)
	}
	if alreadyStarted || state.PID != 502 {
		t.Fatalf("changed start = %#v, %v; want a new runner", state, alreadyStarted)
	}
	if !reflect.DeepEqual(harness.stopped, []int{501}) {
		t.Fatalf("stopped = %#v, want the old runner stopped", harness.stopped)
	}
}

// TestEnsureManagedCronStartedRestartsDeadRunner checks that a recorded
// runner that died is replaced without trying to stop it.
func TestEnsureManagedCronStartedRestartsDeadRunner(t *testing.T) {
	harness := newCronRuntimeHarness()
	ctx, _ := cronTestContext(t, map[string]config.CronJobConfig{"tick": {Schedule: "* * * * *", Command: "php tick.php"}})
	if _, _, err := EnsureManagedCronStarted(ctx, harness.hooks()); err != nil {
		t.Fatalf("EnsureManagedCronStarted(first) error = %v", err)
	}
	delete(harness.livePIDs, 501)

	state, alreadyStarted, err := EnsureManagedCronStarted(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("EnsureManagedCronStarted(dead) error = %v", err)
	}
	if alreadyStarted || state.PID != 502 || len(harness.stopped) != 0 {
		t.Fatalf("dead restart = %#v, %v, stopped %#v; want a new runner and no stop", state, alreadyStarted, harness.stopped)
	}
}

// TestEnsureManagedCronStartedRequiresLauncher checks the error when no CLI
// launcher is wired in.
func TestEnsureManagedCronStartedRequiresLauncher(t *testing.T) {
	ctx, _ := cronTestContext(t, map[string]config.CronJobConfig{"tick": {Schedule: "* * * * *", Command: "php tick.php"}})
	_, _, err := EnsureManagedCronStarted(ctx, CronRuntimeHooks{PIDIsLive: func(int) bool { return false }})
	if err == nil || !strings.Contains(err.Error(), "launcher is not configured") {
		t.Fatalf("EnsureManagedCronStarted(no launcher) error = %v, want launcher error", err)
	}
}

// TestStopManagedCron checks that stopping removes the runner and its state,
// and that a second stop reports already stopped.
func TestStopManagedCron(t *testing.T) {
	harness := newCronRuntimeHarness()
	ctx, _ := cronTestContext(t, map[string]config.CronJobConfig{"tick": {Schedule: "* * * * *", Command: "php tick.php"}})
	if _, _, err := EnsureManagedCronStarted(ctx, harness.hooks()); err != nil {
		t.Fatalf("EnsureManagedCronStarted() error = %v", err)
	}

	state, alreadyStopped, err := StopManagedCron(ctx, harness.hooks())
	if err != nil {
		t.Fatalf("StopManagedCron() error = %v", err)
	}
	if alreadyStopped || state.PID != 501 || !reflect.DeepEqual(harness.stopped, []int{501}) {
		t.Fatalf("stop = %#v, %v, stopped %#v; want runner 501 stopped", state, alreadyStopped, harness.stopped)
	}
	if _, err := os.Stat(CronStatePath(ctx.RootDir, "default")); !os.IsNotExist(err) {
		t.Fatalf("state file after stop error = %v, want removed", err)
	}

	_, alreadyStopped, err = StopManagedCron(ctx, harness.hooks())
	if err != nil || !alreadyStopped {
		t.Fatalf("second stop = %v, %v; want already stopped", alreadyStopped, err)
	}
}

// TestBuildCronJobSpecs checks env layering, target resolution, directories,
// log paths, and that a broken job is skipped with a warning.
func TestBuildCronJobSpecs(t *testing.T) {
	ctx, warnings := cronTestContext(t, map[string]config.CronJobConfig{
		"tick":   {Schedule: "* * * * *", Command: "php tick.php", Dir: "app", Env: map[string]string{"B": "2", "A": "1"}},
		"broken": {Schedule: "61 * * * *", Command: "php broken.php"},
	})
	specs, err := BuildCronJobSpecs(ctx, CronJobBuildHooks{
		BuildEnv: func(Context) ([]string, error) { return []string{"BASE=1"}, nil },
		ResolveTarget: func(_ Context, argv []string, env []string) (string, []string, []string, error) {
			return "/managed/" + argv[0], argv[1:], env, nil
		},
	})
	if err != nil {
		t.Fatalf("BuildCronJobSpecs() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("specs = %#v, want only the valid job", specs)
	}
	spec := specs[0]
	if spec.Name != "tick" || spec.Target != "/managed/php" || !reflect.DeepEqual(spec.Args, []string{"tick.php"}) {
		t.Fatalf("spec = %#v, want resolved tick job", spec)
	}
	if want := []string{"BASE=1", "A=1", "B=2"}; !reflect.DeepEqual(spec.Env, want) {
		t.Fatalf("spec.Env = %#v, want %#v", spec.Env, want)
	}
	if want := filepath.Join(ctx.ProjectDir, "app"); spec.Dir != want {
		t.Fatalf("spec.Dir = %q, want %q", spec.Dir, want)
	}
	if want := CronJobLogPath(ctx.RootDir, "default", "tick"); spec.LogPath != want {
		t.Fatalf("spec.LogPath = %q, want %q", spec.LogPath, want)
	}
	if !strings.Contains(warnings.String(), `Skipping cron job "broken"`) {
		t.Fatalf("warnings = %q, want broken job skipped", warnings.String())
	}
}

// TestCronFingerprintIgnoresFormatting checks that configs differing only in
// normalization produce the same fingerprint, while real changes do not.
func TestCronFingerprintIgnoresFormatting(t *testing.T) {
	first, err := CronFingerprint(map[string]config.CronJobConfig{"Tick": {Schedule: "*  * * * *", Command: " php tick.php "}})
	if err != nil {
		t.Fatalf("CronFingerprint(first) error = %v", err)
	}
	second, err := CronFingerprint(map[string]config.CronJobConfig{"tick": {Schedule: "* * * * *", Command: "php tick.php"}})
	if err != nil {
		t.Fatalf("CronFingerprint(second) error = %v", err)
	}
	changed, err := CronFingerprint(map[string]config.CronJobConfig{"tick": {Schedule: "*/2 * * * *", Command: "php tick.php"}})
	if err != nil {
		t.Fatalf("CronFingerprint(changed) error = %v", err)
	}
	if first != second || first == changed {
		t.Fatalf("fingerprints first=%s second=%s changed=%s, want first == second != changed", first, second, changed)
	}
}

// TestManagerStartsCronAfterWorkersAndStopsItFirst checks the runner's place
// in the service order: started after workers, stopped before them.
func TestManagerStartsCronAfterWorkersAndStopsItFirst(t *testing.T) {
	workers := newWorkersTestHarness()
	cron := newCronRuntimeHarness()
	order := []string{}
	cron.order = &order
	ctx, _ := managerTestContext(t, config.Environment{
		Name:    "demo",
		Workers: map[string]config.WorkerConfig{"queue": {Command: "php artisan queue:work"}},
		Cron:    map[string]config.CronJobConfig{"tick": {Schedule: "* * * * *", Command: "php tick.php"}},
	})
	hooks := RuntimeHooks{Workers: workers.hooks(), Cron: cron.hooks()}
	startWorker := hooks.Workers.StartWorker
	hooks.Workers.StartWorker = func(spec WorkerSpec, replica int) (WorkerStartResult, error) {
		order = append(order, "workers-start")
		return startWorker(spec, replica)
	}
	stopWorker := hooks.Workers.StopWorker
	hooks.Workers.StopWorker = func(state WorkerProcessState) error {
		order = append(order, "workers-stop")
		return stopWorker(state)
	}

	result, err := DefaultManager().Start(ctx, hooks)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if result.Cron == nil || result.Cron.AlreadyStarted || result.Cron.State.PID != 501 {
		t.Fatalf("cron start result = %#v, want started runner", result.Cron)
	}

	stopResult, err := DefaultManager().Stop(ctx, hooks)
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if stopResult.Cron == nil || stopResult.Cron.AlreadyStopped {
		t.Fatalf("cron stop result = %#v, want stopped runner", stopResult.Cron)
	}
	if want := []string{"workers-start", "cron-start", "cron-stop", "workers-stop"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %#v, want %#v", order, want)
	}
}

// TestManagerStartStopsRunnerWhenCronRemoved checks that serving after the
// cron config was removed stops the runner left from an earlier serve.
func TestManagerStartStopsRunnerWhenCronRemoved(t *testing.T) {
	cron := newCronRuntimeHarness()
	ctx, _ := managerTestContext(t, config.Environment{
		Name: "demo",
		Cron: map[string]config.CronJobConfig{"tick": {Schedule: "* * * * *", Command: "php tick.php"}},
	})
	if _, _, err := EnsureManagedCronStarted(ctx, cron.hooks()); err != nil {
		t.Fatalf("EnsureManagedCronStarted() error = %v", err)
	}

	ctx.Environment.Cron = nil
	result, err := DefaultManager().Start(ctx, RuntimeHooks{Cron: cron.hooks()})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if result.Cron != nil {
		t.Fatalf("cron start result = %#v, want nil without cron config", result.Cron)
	}
	if !reflect.DeepEqual(cron.stopped, []int{501}) {
		t.Fatalf("stopped = %#v, want the leftover runner stopped", cron.stopped)
	}
}

// cronHelperEnv selects the helper-process behavior of TestCronHelperProcess.
const cronHelperEnv = "POLKA_CRON_HELPER"

// TestCronHelperProcess is not a real test: RunCronJobProcess tests re-run the
// test binary with cronHelperEnv set so the job is a portable child process.
func TestCronHelperProcess(t *testing.T) {
	switch os.Getenv(cronHelperEnv) {
	case "":
		return
	case "echo":
		fmt.Printf("job=%s\n", os.Getenv("POLKA_CRON_JOB"))
		os.Exit(3)
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

// cronHelperSpec returns a job spec that runs the test binary in helper mode.
func cronHelperSpec(t *testing.T, mode string) CronJobSpec {
	t.Helper()
	return CronJobSpec{
		Name:    "helper",
		Command: "helper " + mode,
		Target:  os.Args[0],
		Args:    []string{"-test.run=^TestCronHelperProcess$"},
		Dir:     t.TempDir(),
		Env:     append(os.Environ(), cronHelperEnv+"="+mode),
		LogPath: filepath.Join(t.TempDir(), "jobs", "helper.log"),
	}
}

// TestRunCronJobProcessAppendsOutputAndExitCode checks that a real job's
// output and exit code land in its log between marker lines, and that a
// second run appends rather than truncates.
func TestRunCronJobProcessAppendsOutputAndExitCode(t *testing.T) {
	spec := cronHelperSpec(t, "echo")

	for run := 1; run <= 2; run++ {
		exitCode, err := RunCronJobProcess(context.Background(), spec)
		if err != nil {
			t.Fatalf("RunCronJobProcess(run %d) error = %v", run, err)
		}
		if exitCode != 3 {
			t.Fatalf("RunCronJobProcess(run %d) exit = %d, want 3", run, exitCode)
		}
	}

	data, err := os.ReadFile(spec.LogPath)
	if err != nil {
		t.Fatalf("ReadFile(job log) error = %v", err)
	}
	log := string(data)
	if got := strings.Count(log, "job=helper"); got != 2 {
		t.Fatalf("job log has %d outputs, want 2 appended runs:\n%s", got, log)
	}
	if !strings.Contains(log, `cron job "helper" started: helper echo`) || !strings.Contains(log, "finished with exit code 3") {
		t.Fatalf("job log = %q, want start and finish markers", log)
	}
}

// TestRunCronJobProcessCancelKillsJob checks that cancelling the context
// stops a running job promptly.
func TestRunCronJobProcessCancelKillsJob(t *testing.T) {
	spec := cronHelperSpec(t, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(500*time.Millisecond, cancel)

	started := time.Now()
	exitCode, err := RunCronJobProcess(ctx, spec)
	if err != nil {
		t.Fatalf("RunCronJobProcess() error = %v", err)
	}
	if exitCode == 0 {
		t.Fatal("RunCronJobProcess() exit = 0, want a killed job to report non-zero")
	}
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("RunCronJobProcess() took %s after cancel, want prompt stop", elapsed)
	}
}

// TestRunCronJobProcessReportsLaunchFailure checks that a missing executable
// is an error, recorded in the job log.
func TestRunCronJobProcessReportsLaunchFailure(t *testing.T) {
	spec := cronHelperSpec(t, "echo")
	spec.Target = filepath.Join(t.TempDir(), "missing-binary")

	exitCode, err := RunCronJobProcess(context.Background(), spec)
	if err == nil || exitCode != -1 {
		t.Fatalf("RunCronJobProcess(missing) = %d, %v; want -1 and an error", exitCode, err)
	}
	data, readErr := os.ReadFile(spec.LogPath)
	if readErr != nil {
		t.Fatalf("ReadFile(job log) error = %v", readErr)
	}
	if !strings.Contains(string(data), "could not start") {
		t.Fatalf("job log = %q, want launch failure marker", string(data))
	}
}

// TestRotateCronJobLog checks that an oversized log moves to ".1" and a small
// one stays put.
func TestRotateCronJobLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	if err := rotateCronJobLog(path); err != nil {
		t.Fatalf("rotateCronJobLog(missing) error = %v", err)
	}
	if err := os.WriteFile(path, []byte("small"), 0o644); err != nil {
		t.Fatalf("WriteFile(small) error = %v", err)
	}
	if err := rotateCronJobLog(path); err != nil {
		t.Fatalf("rotateCronJobLog(small) error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("small log after rotate error = %v, want kept", err)
	}

	if err := os.WriteFile(path, make([]byte, cronJobLogRotateBytes), 0o644); err != nil {
		t.Fatalf("WriteFile(large) error = %v", err)
	}
	if err := rotateCronJobLog(path); err != nil {
		t.Fatalf("rotateCronJobLog(large) error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("large log after rotate error = %v, want moved", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated log error = %v, want present", err)
	}
}
