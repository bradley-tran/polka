package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"polka/config"
)

const (
	managedCronStateSubdirectory = "cron"
	// managedCronStartupGrace is how long StartCronRunnerProcess watches a
	// freshly launched runner so one that dies at once (bad config, missing
	// executable) surfaces an error instead of a phantom PID.
	managedCronStartupGrace = 750 * time.Millisecond
	// managedCronStopGrace is how long a stopping runner and its jobs get to
	// exit after SIGTERM before the whole process group is killed.
	managedCronStopGrace = 5 * time.Second
	// cronJobLogRotateBytes is the size at which a job log is moved to
	// "<job>.log.1" before the next run, so a job that runs every minute
	// cannot grow its log without bound.
	cronJobLogRotateBytes = 5 << 20
	// cronClockResetThreshold is how far the wall clock may move backwards
	// before the scheduler treats it as a clock reset and resumes from the new
	// time. Smaller steps back wait for the clock to catch up, so no minute
	// runs twice.
	cronClockResetThreshold = 5 * time.Minute
)

// CronRunnerState is the persisted runtime state of one environment's cron
// runner process.
type CronRunnerState struct {
	EnvironmentName string    `json:"environment"`
	PID             int       `json:"pid"`
	Fingerprint     string    `json:"fingerprint"`
	Jobs            []string  `json:"jobs"`
	LogPath         string    `json:"log_path,omitempty"`
	StartedAt       time.Time `json:"started_at"`
}

// CronRunnerSpec describes how to launch the runner process for one
// environment. The CLI builds it, because the runner is Polka itself and only
// the CLI knows its command-line syntax.
type CronRunnerSpec struct {
	EnvironmentName string
	Target          string
	Args            []string
	Dir             string
	Env             []string
	LogPath         string
}

// CronRuntimeHooks lets callers (and tests) replace the process-level side
// effects of managing the cron runner.
type CronRuntimeHooks struct {
	BuildRunnerSpec func(Context) (CronRunnerSpec, error)
	StartRunner     func(CronRunnerSpec) (int, error)
	StopRunner      func(CronRunnerState) error
	PIDIsLive       func(int) bool
	Now             func() time.Time
}

// CronJobSpec is one resolved cron job the scheduler can launch.
type CronJobSpec struct {
	Name     string
	Schedule config.CronSchedule
	Command  string // normalized raw command, for logs and listings
	Target   string // resolved argv[0]
	Args     []string
	Dir      string // absolute working directory
	Env      []string
	LogPath  string
}

// CronJobBuildHooks supplies the environment and target resolution used to
// turn cron config into launchable specs. The CLI injects the managed shell
// environment and managed-tool resolution, exactly as it does for workers.
type CronJobBuildHooks struct {
	BuildEnv      func(Context) ([]string, error)
	ResolveTarget func(ctx Context, argv []string, env []string) (string, []string, []string, error)
}

// CronJobRunState is the outcome of one job's latest run.
type CronJobRunState struct {
	LastStart    time.Time `json:"last_start"`
	LastFinish   time.Time `json:"last_finish"`
	LastExitCode int       `json:"last_exit_code"`
	LastError    string    `json:"last_error,omitempty"`
	Running      bool      `json:"running"`
}

// CronRunsState is the per-job run history the runner keeps for listings,
// status, and the dashboard.
type CronRunsState struct {
	EnvironmentName string                     `json:"environment"`
	Jobs            map[string]CronJobRunState `json:"jobs"`
}

// CronSchedulerHooks lets callers (and tests) replace the clock, job
// execution, history persistence, and logging of RunCronScheduler.
type CronSchedulerHooks struct {
	Now      func() time.Time
	After    func(time.Duration) <-chan time.Time
	RunJob   func(context.Context, CronJobSpec) (int, error)
	SaveRuns func(CronRunsState) error
	Logf     func(format string, args ...any)
}

// EnsureManagedCronStarted starts the cron runner for the context's
// environment. When a live runner was started from the same cron config it
// reports alreadyStarted; otherwise it stops any leftover runner and starts a
// fresh one so config edits take effect.
func EnsureManagedCronStarted(ctx Context, hooks CronRuntimeHooks) (CronRunnerState, bool, error) {
	hooks = hooks.withDefaults()
	environment := ctx.Environment
	if len(environment.Cron) == 0 {
		return CronRunnerState{}, true, nil
	}

	fingerprint, err := CronFingerprint(environment.Cron)
	if err != nil {
		return CronRunnerState{}, false, err
	}
	state, err := LoadLiveCronState(ctx.RootDir, environment.Name, hooks.PIDIsLive)
	if err != nil {
		return CronRunnerState{}, false, err
	}
	if state != nil && state.Fingerprint == fingerprint {
		return *state, true, nil
	}
	if state != nil {
		if err := hooks.StopRunner(*state); err != nil {
			ctx.warnf("Failed to stop the cron runner (pid %d) for environment %q: %v.\n", state.PID, environment.Name, err)
		}
		if err := removeCronState(ctx.RootDir, environment.Name); err != nil {
			return CronRunnerState{}, false, err
		}
	}

	spec, err := hooks.BuildRunnerSpec(ctx)
	if err != nil {
		return CronRunnerState{}, false, err
	}
	pid, err := hooks.StartRunner(spec)
	if err != nil {
		return CronRunnerState{}, false, err
	}

	started := CronRunnerState{
		EnvironmentName: environment.Name,
		PID:             pid,
		Fingerprint:     fingerprint,
		Jobs:            config.SortedCronJobNames(environment.Cron),
		LogPath:         spec.LogPath,
		StartedAt:       hooks.Now().UTC(),
	}
	if err := WriteCronState(CronStatePath(ctx.RootDir, environment.Name), started); err != nil {
		if stopErr := hooks.StopRunner(started); stopErr != nil {
			ctx.warnf("Failed to stop the cron runner (pid %d) for environment %q: %v.\n", pid, environment.Name, stopErr)
		}
		return CronRunnerState{}, false, err
	}

	return started, false, nil
}

// StopManagedCron stops the live cron runner recorded for the context's
// environment, along with any jobs it is running, and removes its state.
func StopManagedCron(ctx Context, hooks CronRuntimeHooks) (CronRunnerState, bool, error) {
	hooks = hooks.withDefaults()

	state, err := LoadLiveCronState(ctx.RootDir, ctx.Environment.Name, hooks.PIDIsLive)
	if err != nil {
		return CronRunnerState{}, false, err
	}
	if state == nil {
		return CronRunnerState{}, true, nil
	}
	if err := hooks.StopRunner(*state); err != nil {
		return CronRunnerState{}, false, fmt.Errorf("stop cron runner (pid %d): %w", state.PID, err)
	}
	if err := removeCronState(ctx.RootDir, ctx.Environment.Name); err != nil {
		return CronRunnerState{}, false, err
	}

	return *state, false, nil
}

// CronFingerprint returns a stable digest of the normalized cron config. The
// runner records it at start so a later serve can tell whether the config
// changed since.
func CronFingerprint(jobs map[string]config.CronJobConfig) (string, error) {
	// encoding/json sorts map keys, so equal configs encode identically.
	data, err := json.Marshal(config.NormalizeCronConfig(jobs))
	if err != nil {
		return "", fmt.Errorf("encode cron config: %w", err)
	}
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

// BuildCronJobSpecs converts the environment's cron config into launchable
// specs, sorted by job name. A job whose schedule, command, or target cannot
// be resolved is skipped with a warning so one broken definition does not
// stop the rest.
func BuildCronJobSpecs(ctx Context, hooks CronJobBuildHooks) ([]CronJobSpec, error) {
	hooks = hooks.withDefaults()
	environment := ctx.Environment

	baseEnv, err := hooks.BuildEnv(ctx)
	if err != nil {
		return nil, err
	}

	names := config.SortedCronJobNames(environment.Cron)
	specs := make([]CronJobSpec, 0, len(names))
	for _, name := range names {
		job := environment.Cron[name]
		schedule, err := config.ParseCronSchedule(job.Schedule)
		if err != nil {
			ctx.warnf("Skipping cron job %q for environment %q: %v.\n", name, environment.Name, err)
			continue
		}
		argv, err := config.SplitWorkerCommand(job.Command)
		if err != nil {
			ctx.warnf("Skipping cron job %q for environment %q: %v.\n", name, environment.Name, err)
			continue
		}

		// Per-job env entries follow the base environment; exec.Cmd keeps the
		// last duplicate, so job values win.
		env := append([]string(nil), baseEnv...)
		for _, key := range sortedWorkerEnvKeys(job.Env) {
			env = append(env, key+"="+job.Env[key])
		}

		target, args, env, err := hooks.ResolveTarget(ctx, argv, env)
		if err != nil {
			ctx.warnf("Skipping cron job %q for environment %q: %v.\n", name, environment.Name, err)
			continue
		}

		specs = append(specs, CronJobSpec{
			Name:     name,
			Schedule: schedule,
			Command:  strings.TrimSpace(job.Command),
			Target:   target,
			Args:     args,
			Dir:      resolveWorkerDir(ctx.ProjectDir, job.Dir),
			Env:      env,
			LogPath:  CronJobLogPath(ctx.RootDir, environment.Name, name),
		})
	}

	return specs, nil
}

// RunCronScheduler runs jobs on their schedules until ctx is cancelled. At
// each minute boundary it starts every job whose schedule matches, unless a
// run of that job is still going, in which case the new run is skipped. Like
// cron, minutes missed while the machine slept are not caught up, and a small
// backwards clock step never runs a minute twice. On cancellation it stops
// scheduling, cancels running jobs, and waits for them before returning.
// history seeds the run state so listings keep earlier results.
func RunCronScheduler(ctx context.Context, jobs []CronJobSpec, history CronRunsState, hooks CronSchedulerHooks) error {
	hooks = hooks.withDefaults()
	scheduler := newCronScheduler(jobs, history, hooks)
	defer scheduler.wait()
	scheduler.save()

	last := cronMinute(hooks.Now())
	for {
		wait := last.Add(time.Minute).Sub(hooks.Now())
		if wait > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-hooks.After(wait):
			}
		}
		if ctx.Err() != nil {
			return nil
		}

		current := cronMinute(hooks.Now())
		if !current.After(last) {
			if last.Sub(current) > cronClockResetThreshold {
				// The clock was set back a long way: resume from the new time
				// rather than stalling until it catches up.
				hooks.Logf("Clock moved back from %s to %s; resuming the schedule from now.\n", last.Format(time.RFC3339), current.Format(time.RFC3339))
				last = current
			}
			continue
		}

		scheduler.dispatch(ctx, current)
		last = current
	}
}

// RunCronJobProcess runs one job to completion, appending its output to the
// job log between start and finish marker lines. A non-zero exit is reported
// through the exit code, not the error, which is reserved for failures to
// launch. Cancelling ctx kills the job.
func RunCronJobProcess(ctx context.Context, spec CronJobSpec) (int, error) {
	rotateErr := rotateCronJobLog(spec.LogPath)
	logFile, err := openCronJobLog(spec.LogPath)
	if err != nil {
		return -1, err
	}
	defer logFile.Close()
	if rotateErr != nil {
		writeCronLogLine(logFile, "could not rotate %s: %v", spec.LogPath, rotateErr)
	}

	command, err := prepareServiceCommand(spec.Target, spec.Args)
	if err != nil {
		writeCronLogLine(logFile, "cron job %q could not start: %v", spec.Name, err)
		return -1, err
	}
	command.Dir = spec.Dir
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = append(append([]string(nil), spec.Env...), "POLKA_CRON_JOB="+spec.Name)

	started := time.Now()
	writeCronLogLine(logFile, "cron job %q started: %s", spec.Name, spec.Command)
	if err := command.Start(); err != nil {
		writeCronLogLine(logFile, "cron job %q could not start: %v", spec.Name, err)
		return -1, fmt.Errorf("start cron job %s: %w", spec.Name, err)
	}

	exitCode, waitErr := waitCronJob(ctx, command)
	elapsed := time.Since(started).Round(time.Millisecond)
	if waitErr != nil {
		writeCronLogLine(logFile, "cron job %q failed after %s: %v", spec.Name, elapsed, waitErr)
		return exitCode, waitErr
	}
	writeCronLogLine(logFile, "cron job %q finished with exit code %d after %s", spec.Name, exitCode, elapsed)

	return exitCode, nil
}

// StartCronRunnerProcess launches the runner in its own process group,
// redirecting its output to the runner log. It briefly watches the process
// so a runner that exits at once fails loudly instead of being recorded as
// running.
func StartCronRunnerProcess(spec CronRunnerSpec) (int, error) {
	logFile, err := openServiceLog(spec.LogPath)
	if err != nil {
		return 0, err
	}

	command, err := prepareServiceCommand(spec.Target, spec.Args)
	if err != nil {
		_ = logFile.Close()
		return 0, err
	}
	command.Dir = spec.Dir
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = spec.Env
	setProcessGroup(command)

	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("start cron runner: %w", err)
	}

	pid := command.Process.Pid
	// Wait in the background so an early exit is reaped and visible to the
	// liveness check below instead of lingering as a zombie.
	exited := make(chan struct{})
	go func() {
		// The exit status does not matter here: an early exit is reported
		// below by pointing at the runner log.
		_ = command.Wait()
		close(exited)
	}()

	select {
	case <-exited:
		_ = logFile.Close()
		return 0, fmt.Errorf("cron runner for environment %s exited immediately (see %s)", spec.EnvironmentName, spec.LogPath)
	case <-time.After(managedCronStartupGrace):
	}

	// The child keeps its own handle to the log; the parent's copy is no
	// longer needed.
	_ = logFile.Close()

	return pid, nil
}

// StopCronRunnerProcess stops the runner and every job it started.
func StopCronRunnerProcess(state CronRunnerState) error {
	return stopProcessGroup(state.PID, managedCronStopGrace)
}

// CronPIDIsLive reports whether the given runner process is still running.
func CronPIDIsLive(pid int) bool {
	return servicePIDIsLive(pid)
}

// LoadLiveCronState loads the persisted runner state, returning nil (and
// removing the stale file) when the recorded runner is no longer running.
func LoadLiveCronState(rootDir, environmentName string, pidIsLive func(int) bool) (*CronRunnerState, error) {
	if pidIsLive == nil {
		pidIsLive = servicePIDIsLive
	}

	path := CronStatePath(rootDir, environmentName)
	state, err := LoadCronState(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !pidIsLive(state.PID) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale cron state: %w", err)
		}
		return nil, nil
	}

	return state, nil
}

// LoadCronState reads the persisted runner state file.
func LoadCronState(path string) (*CronRunnerState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state CronRunnerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode cron state %s: %w", path, err)
	}

	return &state, nil
}

// WriteCronState persists the runner state file atomically.
func WriteCronState(path string, state CronRunnerState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cron state: %w", err)
	}

	return writeFileAtomic(path, append(data, '\n'), 0o644)
}

// LoadCronRunsState reads the job run history, returning an empty history
// when the file does not exist yet.
func LoadCronRunsState(path string) (CronRunsState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return CronRunsState{}, nil
	}
	if err != nil {
		return CronRunsState{}, err
	}

	var state CronRunsState
	if err := json.Unmarshal(data, &state); err != nil {
		return CronRunsState{}, fmt.Errorf("decode cron run history %s: %w", path, err)
	}

	return state, nil
}

// WriteCronRunsState persists the job run history atomically, so readers
// never see a partial file.
func WriteCronRunsState(path string, state CronRunsState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cron run history: %w", err)
	}

	return writeFileAtomic(path, append(data, '\n'), 0o644)
}

// CronStatePath returns the runner state file for one environment.
func CronStatePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedWorkersStateDirectory, managedCronStateSubdirectory, environmentName+".json")
}

// CronLogDir returns the directory holding one environment's runner log, job
// logs, and run history.
func CronLogDir(rootDir, environmentName string) string {
	return ToolLogRoot(rootDir, managedCronStateSubdirectory, environmentName)
}

// CronRunnerLogPath returns the runner's own log file.
func CronRunnerLogPath(rootDir, environmentName string) string {
	return filepath.Join(CronLogDir(rootDir, environmentName), "runner.log")
}

// CronRunsPath returns the job run history file.
func CronRunsPath(rootDir, environmentName string) string {
	return filepath.Join(CronLogDir(rootDir, environmentName), "runs.json")
}

// CronJobLogPath returns the log file for one job.
func CronJobLogPath(rootDir, environmentName, jobName string) string {
	return filepath.Join(CronLogDir(rootDir, environmentName), "jobs", jobName+".log")
}

// cronScheduler holds the mutable state RunCronScheduler shares with the job
// goroutines it starts.
type cronScheduler struct {
	jobs  []CronJobSpec
	hooks CronSchedulerHooks

	mu   sync.Mutex
	runs CronRunsState
	wg   sync.WaitGroup
}

// newCronScheduler copies history and clears running flags left by a runner
// that did not shut down cleanly.
func newCronScheduler(jobs []CronJobSpec, history CronRunsState, hooks CronSchedulerHooks) *cronScheduler {
	runs := CronRunsState{EnvironmentName: history.EnvironmentName, Jobs: make(map[string]CronJobRunState, len(jobs))}
	for name, run := range history.Jobs {
		run.Running = false
		runs.Jobs[name] = run
	}

	return &cronScheduler{jobs: jobs, hooks: hooks, runs: runs}
}

// dispatch starts every job due in minute that is not already running.
func (s *cronScheduler) dispatch(ctx context.Context, minute time.Time) {
	for _, job := range s.jobs {
		if !job.Schedule.Matches(minute) {
			continue
		}

		s.mu.Lock()
		run := s.runs.Jobs[job.Name]
		if run.Running {
			s.mu.Unlock()
			s.hooks.Logf("Skipping cron job %q at %s: its previous run is still in progress.\n", job.Name, minute.Format(time.RFC3339))
			continue
		}
		run.Running = true
		run.LastStart = s.hooks.Now()
		s.runs.Jobs[job.Name] = run
		s.saveLocked()
		s.mu.Unlock()

		s.wg.Add(1)
		go s.run(ctx, job)
	}
}

// run executes one job and records its outcome.
func (s *cronScheduler) run(ctx context.Context, job CronJobSpec) {
	defer s.wg.Done()

	exitCode, err := s.hooks.RunJob(ctx, job)

	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs.Jobs[job.Name]
	run.Running = false
	run.LastFinish = s.hooks.Now()
	run.LastExitCode = exitCode
	run.LastError = ""
	if err != nil {
		run.LastError = err.Error()
		s.hooks.Logf("Cron job %q failed: %v.\n", job.Name, err)
	}
	s.runs.Jobs[job.Name] = run
	s.saveLocked()
}

// save persists the run history.
func (s *cronScheduler) save() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveLocked()
}

// saveLocked persists the run history; the caller holds s.mu. A failed write
// is logged rather than fatal, because the history only feeds listings.
func (s *cronScheduler) saveLocked() {
	if err := s.hooks.SaveRuns(s.runs.clone()); err != nil {
		s.hooks.Logf("Failed to save cron run history: %v.\n", err)
	}
}

// wait blocks until every started job has finished.
func (s *cronScheduler) wait() {
	s.wg.Wait()
}

// clone returns a copy whose job map can be handed to another goroutine.
func (state CronRunsState) clone() CronRunsState {
	jobs := make(map[string]CronJobRunState, len(state.Jobs))
	for name, run := range state.Jobs {
		jobs[name] = run
	}

	return CronRunsState{EnvironmentName: state.EnvironmentName, Jobs: jobs}
}

// cronMinute truncates t to the start of its minute in t's location and drops
// the monotonic reading, so comparisons use wall-clock time.
func cronMinute(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, t.Location())
}

// waitCronJob waits for a started job, killing it if ctx is cancelled first,
// and converts its exit status to an exit code.
func waitCronJob(ctx context.Context, command *exec.Cmd) (int, error) {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			// Best effort: if the kill fails the job already exited, and Wait
			// below reports how it ended.
			_ = stopServicePID(command.Process.Pid)
		case <-done:
		}
	}()

	err := command.Wait()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}

	return -1, fmt.Errorf("wait for cron job: %w", err)
}

// rotateCronJobLog moves an oversized job log to "<path>.1", replacing the
// previous rotation.
func rotateCronJobLog(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() < cronJobLogRotateBytes {
		return nil
	}

	return os.Rename(path, path+".1")
}

// openCronJobLog opens a job log for appending, creating its directory.
func openCronJobLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create cron log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open cron job log %s: %w", path, err)
	}

	return file, nil
}

// writeCronLogLine appends one timestamped marker line to a job log. Marker
// lines are informational, so a failed write is not reported.
func writeCronLogLine(file *os.File, format string, args ...any) {
	_, _ = fmt.Fprintf(file, "=== %s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// defaultBuildCronRunnerSpec fails, because only the CLI knows how to launch
// the runner.
func defaultBuildCronRunnerSpec(Context) (CronRunnerSpec, error) {
	return CronRunnerSpec{}, fmt.Errorf("cron runner launcher is not configured")
}

func (hooks CronRuntimeHooks) withDefaults() CronRuntimeHooks {
	if hooks.BuildRunnerSpec == nil {
		hooks.BuildRunnerSpec = defaultBuildCronRunnerSpec
	}
	if hooks.StartRunner == nil {
		hooks.StartRunner = StartCronRunnerProcess
	}
	if hooks.StopRunner == nil {
		hooks.StopRunner = StopCronRunnerProcess
	}
	if hooks.PIDIsLive == nil {
		hooks.PIDIsLive = servicePIDIsLive
	}
	if hooks.Now == nil {
		hooks.Now = time.Now
	}

	return hooks
}

func (hooks CronJobBuildHooks) withDefaults() CronJobBuildHooks {
	if hooks.BuildEnv == nil {
		hooks.BuildEnv = func(ctx Context) ([]string, error) { return ctx.runtimeEnv() }
	}
	if hooks.ResolveTarget == nil {
		hooks.ResolveTarget = defaultResolveWorkerTarget
	}

	return hooks
}

func (hooks CronSchedulerHooks) withDefaults() CronSchedulerHooks {
	if hooks.Now == nil {
		hooks.Now = time.Now
	}
	if hooks.After == nil {
		hooks.After = time.After
	}
	if hooks.RunJob == nil {
		hooks.RunJob = RunCronJobProcess
	}
	if hooks.SaveRuns == nil {
		hooks.SaveRuns = func(CronRunsState) error { return nil }
	}
	if hooks.Logf == nil {
		hooks.Logf = func(string, ...any) {}
	}

	return hooks
}

// removeCronState deletes the runner state file, tolerating a missing file.
func removeCronState(rootDir, environmentName string) error {
	if err := os.Remove(CronStatePath(rootDir, environmentName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove cron state: %w", err)
	}

	return nil
}
