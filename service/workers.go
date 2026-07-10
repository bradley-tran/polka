package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"polka/config"
)

const (
	managedWorkersStateDirectory    = "run"
	managedWorkersStateSubdirectory = "workers"
	managedWorkersPollInterval      = 50 * time.Millisecond
	// managedWorkersStartupGrace is how long StartWorkerProcess watches a
	// freshly launched worker so commands that die immediately (typo, missing
	// script) surface an error instead of a phantom PID.
	managedWorkersStartupGrace = 750 * time.Millisecond
)

// WorkerProcessState is the persisted runtime state for one worker replica.
type WorkerProcessState struct {
	Name      string    `json:"name"`
	Replica   int       `json:"replica"`
	PID       int       `json:"pid"`
	Command   string    `json:"command"`
	LogPath   string    `json:"log_path,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// WorkersRuntimeState is the persisted runtime state for all workers of one environment.
type WorkersRuntimeState struct {
	EnvironmentName string               `json:"environment"`
	Processes       []WorkerProcessState `json:"processes"`
}

// WorkerSpec describes the launch of all replicas of one configured worker.
type WorkerSpec struct {
	EnvironmentName string
	Name            string
	Command         string // normalized raw command, recorded for change detection
	Target          string // resolved argv[0]
	Args            []string
	Dir             string // absolute working directory
	Replicas        int
	LogDir          string
	Env             []string
}

// WorkerStartResult reports one successfully launched worker replica.
type WorkerStartResult struct {
	PID     int
	LogPath string
}

// WorkersRuntimeHooks lets callers (and tests) replace the process-level side
// effects of worker management. CLI wiring injects BuildEnv and ResolveTarget
// so worker commands run with the managed-tool environment without the service
// package depending on backend.
type WorkersRuntimeHooks struct {
	StartWorker func(WorkerSpec, int) (WorkerStartResult, error)
	StopWorker  func(WorkerProcessState) error
	PIDIsLive   func(int) bool
	BuildEnv    func(Context) ([]string, error)
	// ResolveTarget maps argv (from the split worker command) to the concrete
	// executable and possibly adjusted argv/env. The default resolves argv[0]
	// through PATH at exec time.
	ResolveTarget func(ctx Context, argv []string, env []string) (string, []string, []string, error)
	Now           func() time.Time
}

// EnsureManagedWorkersStarted starts the configured workers for the context's
// environment. When the persisted live state already matches the current
// configuration it reports alreadyStarted; otherwise it reconciles by stopping
// leftover processes and starting fresh replicas.
func EnsureManagedWorkersStarted(ctx Context, hooks WorkersRuntimeHooks) (WorkersRuntimeState, bool, error) {
	hooks = hooks.withDefaults()
	environment := ctx.Environment
	if len(environment.Workers) == 0 {
		return WorkersRuntimeState{}, true, nil
	}

	state, err := LoadLiveWorkersState(ctx.RootDir, environment.Name, hooks.PIDIsLive)
	if err != nil {
		return WorkersRuntimeState{}, false, err
	}
	if state != nil && WorkersStateMatchesEnvironment(*state, environment) {
		return *state, true, nil
	}

	// Reconcile: the live state is missing, partial, or built from an older
	// workers configuration. Stop whatever is still running and start fresh.
	if state != nil {
		stopWorkerProcesses(ctx, state.Processes, hooks)
		if err := removeWorkersState(ctx.RootDir, environment.Name); err != nil {
			return WorkersRuntimeState{}, false, err
		}
	}

	specs, err := BuildWorkerSpecs(ctx, hooks)
	if err != nil {
		return WorkersRuntimeState{}, false, err
	}

	// A worker that fails to launch is reported as a warning rather than an
	// error so one broken worker command does not abort serve or the other
	// workers. The partial state also will not match the configuration, so the
	// next start attempt retries the failed replicas.
	started := make([]WorkerProcessState, 0)
	for _, spec := range specs {
		for replica := 1; replica <= spec.Replicas; replica++ {
			result, err := hooks.StartWorker(spec, replica)
			if err != nil {
				ctx.warnf("Worker %q replica %d for environment %q failed to start: %v.\n", spec.Name, replica, environment.Name, err)
				continue
			}
			started = append(started, WorkerProcessState{
				Name:      spec.Name,
				Replica:   replica,
				PID:       result.PID,
				Command:   spec.Command,
				LogPath:   result.LogPath,
				StartedAt: hooks.Now().UTC(),
			})
		}
	}
	if len(started) == 0 {
		return WorkersRuntimeState{EnvironmentName: environment.Name}, false, nil
	}

	startedState := WorkersRuntimeState{EnvironmentName: environment.Name, Processes: started}
	if err := WriteWorkersState(WorkersStatePath(ctx.RootDir, environment.Name), startedState); err != nil {
		stopWorkerProcesses(ctx, started, hooks)
		return WorkersRuntimeState{}, false, err
	}

	return startedState, false, nil
}

// StopManagedWorkers stops every live worker replica recorded for the
// context's environment and removes the persisted state.
func StopManagedWorkers(ctx Context, hooks WorkersRuntimeHooks) (WorkersRuntimeState, bool, error) {
	hooks = hooks.withDefaults()

	state, err := LoadLiveWorkersState(ctx.RootDir, ctx.Environment.Name, hooks.PIDIsLive)
	if err != nil {
		return WorkersRuntimeState{}, false, err
	}
	if state == nil {
		return WorkersRuntimeState{}, true, nil
	}
	stopWorkerProcesses(ctx, state.Processes, hooks)
	if err := removeWorkersState(ctx.RootDir, ctx.Environment.Name); err != nil {
		return WorkersRuntimeState{}, false, err
	}

	return *state, false, nil
}

// BuildWorkerSpecs converts the environment's worker configuration into launch
// specs, sorted by worker name for deterministic start order. Workers whose
// command cannot be split or resolved are skipped with a warning so one broken
// definition does not block the rest.
func BuildWorkerSpecs(ctx Context, hooks WorkersRuntimeHooks) ([]WorkerSpec, error) {
	hooks = hooks.withDefaults()
	environment := ctx.Environment

	baseEnv, err := hooks.BuildEnv(ctx)
	if err != nil {
		return nil, err
	}

	names := config.SortedWorkerNames(environment.Workers)
	specs := make([]WorkerSpec, 0, len(names))
	for _, name := range names {
		worker := environment.Workers[name]
		argv, err := config.SplitWorkerCommand(worker.Command)
		if err != nil {
			ctx.warnf("Skipping worker %q for environment %q: %v.\n", name, environment.Name, err)
			continue
		}

		// Per-worker env entries are appended after the base environment;
		// exec.Cmd keeps the last duplicate, so worker values win.
		env := append([]string(nil), baseEnv...)
		for _, key := range sortedWorkerEnvKeys(worker.Env) {
			env = append(env, key+"="+worker.Env[key])
		}

		target, args, env, err := hooks.ResolveTarget(ctx, argv, env)
		if err != nil {
			ctx.warnf("Skipping worker %q for environment %q: %v.\n", name, environment.Name, err)
			continue
		}

		specs = append(specs, WorkerSpec{
			EnvironmentName: environment.Name,
			Name:            name,
			Command:         strings.TrimSpace(worker.Command),
			Target:          target,
			Args:            args,
			Dir:             resolveWorkerDir(ctx.ProjectDir, worker.Dir),
			Replicas:        config.EffectiveWorkerReplicas(worker),
			LogDir:          WorkersLogDir(ctx.RootDir, environment.Name),
			Env:             env,
		})
	}

	return specs, nil
}

// StartWorkerProcess launches one worker replica, redirecting output to a
// per-replica log file. It briefly watches the new process so commands that
// exit immediately fail loudly instead of being recorded as running.
func StartWorkerProcess(spec WorkerSpec, replica int) (WorkerStartResult, error) {
	logPath := WorkerLogPath(spec.LogDir, spec.Name, replica)
	logFile, err := openServiceLog(logPath)
	if err != nil {
		return WorkerStartResult{}, err
	}

	command, err := prepareServiceCommand(spec.Target, spec.Args)
	if err != nil {
		_ = logFile.Close()
		return WorkerStartResult{}, err
	}
	command.Dir = spec.Dir
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = append(append([]string(nil), spec.Env...),
		"POLKA_WORKER_NAME="+spec.Name,
		"POLKA_WORKER_REPLICA="+strconv.Itoa(replica),
	)

	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return WorkerStartResult{}, fmt.Errorf("start worker: %w", err)
	}

	pid := command.Process.Pid
	deadline := time.Now().Add(managedWorkersStartupGrace)
	for time.Now().Before(deadline) {
		if !servicePIDIsLive(pid) {
			stopServiceProcess(command.Process)
			_ = logFile.Close()
			return WorkerStartResult{}, fmt.Errorf("worker %s replica %d exited immediately (see %s)", spec.Name, replica, logPath)
		}
		time.Sleep(managedWorkersPollInterval)
	}

	_ = logFile.Close()
	_ = command.Process.Release()

	return WorkerStartResult{PID: pid, LogPath: logPath}, nil
}

// StopWorkerProcess terminates one recorded worker replica.
func StopWorkerProcess(state WorkerProcessState) error {
	return stopServicePID(state.PID)
}

// WorkerPIDIsLive reports whether the given worker process is still running.
func WorkerPIDIsLive(pid int) bool {
	return servicePIDIsLive(pid)
}

// WorkersStateMatchesEnvironment reports whether the live worker processes
// exactly cover the configured workers: same names, same replica counts, and
// unchanged commands.
func WorkersStateMatchesEnvironment(state WorkersRuntimeState, environment config.Environment) bool {
	type liveWorker struct {
		replicas int
		command  string
	}
	live := make(map[string]liveWorker, len(state.Processes))
	for _, process := range state.Processes {
		entry := live[process.Name]
		if entry.replicas > 0 && entry.command != process.Command {
			return false
		}
		entry.replicas++
		entry.command = process.Command
		live[process.Name] = entry
	}
	if len(live) != len(environment.Workers) {
		return false
	}
	for name, worker := range environment.Workers {
		entry, ok := live[name]
		if !ok {
			return false
		}
		if entry.replicas != config.EffectiveWorkerReplicas(worker) {
			return false
		}
		if entry.command != strings.TrimSpace(worker.Command) {
			return false
		}
	}

	return true
}

// LoadLiveWorkersState loads the persisted worker state and prunes replicas
// whose processes are no longer running. It returns nil when no live replicas
// remain (removing the stale state file in that case).
func LoadLiveWorkersState(rootDir, environmentName string, pidIsLive func(int) bool) (*WorkersRuntimeState, error) {
	if pidIsLive == nil {
		pidIsLive = servicePIDIsLive
	}

	path := WorkersStatePath(rootDir, environmentName)
	state, err := LoadWorkersState(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	liveProcesses := make([]WorkerProcessState, 0, len(state.Processes))
	for _, process := range state.Processes {
		if pidIsLive(process.PID) {
			liveProcesses = append(liveProcesses, process)
		}
	}
	if len(liveProcesses) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale workers state: %w", err)
		}
		return nil, nil
	}
	state.Processes = liveProcesses

	return state, nil
}

// LoadWorkersState reads the persisted worker state file.
func LoadWorkersState(path string) (*WorkersRuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state WorkersRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode workers state %s: %w", path, err)
	}

	return &state, nil
}

// WriteWorkersState persists the worker state file, creating its directory as needed.
func WriteWorkersState(path string, state WorkersRuntimeState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create workers state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workers state: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write workers state %s: %w", path, err)
	}

	return nil
}

// WorkersStatePath returns the state file location for one environment's workers.
func WorkersStatePath(rootDir, environmentName string) string {
	return filepath.Join(rootDir, managedWorkersStateDirectory, managedWorkersStateSubdirectory, environmentName+".json")
}

// WorkersLogDir returns the log directory for one environment's workers.
func WorkersLogDir(rootDir, environmentName string) string {
	return ToolLogRoot(rootDir, managedWorkersStateSubdirectory, environmentName)
}

// WorkerLogPath returns the log file for one worker replica.
func WorkerLogPath(logDir, name string, replica int) string {
	return filepath.Join(logDir, name+"-"+strconv.Itoa(replica)+".log")
}

// stopWorkerProcesses stops the given replicas, reporting failures as
// warnings so one stuck process does not block the remaining replicas or the
// other managed services.
func stopWorkerProcesses(ctx Context, processes []WorkerProcessState, hooks WorkersRuntimeHooks) {
	for _, process := range processes {
		if err := hooks.StopWorker(process); err != nil {
			ctx.warnf("Failed to stop worker %q replica %d (pid %d) for environment %q: %v.\n", process.Name, process.Replica, process.PID, ctx.Environment.Name, err)
		}
	}
}

// removeWorkersState deletes the persisted state file, tolerating a missing file.
func removeWorkersState(rootDir, environmentName string) error {
	if err := os.Remove(WorkersStatePath(rootDir, environmentName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove workers state: %w", err)
	}

	return nil
}

// resolveWorkerDir resolves a worker's configured directory against the project directory.
func resolveWorkerDir(projectDir, dir string) string {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return projectDir
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}

	return filepath.Join(projectDir, trimmed)
}

// sortedWorkerEnvKeys returns the worker env keys in deterministic order.
func sortedWorkerEnvKeys(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	return keys
}

// defaultResolveWorkerTarget leaves argv untouched so exec resolves argv[0]
// through PATH from the provided environment.
func defaultResolveWorkerTarget(_ Context, argv []string, env []string) (string, []string, []string, error) {
	return argv[0], argv[1:], env, nil
}

func (hooks WorkersRuntimeHooks) withDefaults() WorkersRuntimeHooks {
	if hooks.StartWorker == nil {
		hooks.StartWorker = StartWorkerProcess
	}
	if hooks.StopWorker == nil {
		hooks.StopWorker = StopWorkerProcess
	}
	if hooks.PIDIsLive == nil {
		hooks.PIDIsLive = servicePIDIsLive
	}
	if hooks.BuildEnv == nil {
		hooks.BuildEnv = func(ctx Context) ([]string, error) { return ctx.runtimeEnv() }
	}
	if hooks.ResolveTarget == nil {
		hooks.ResolveTarget = defaultResolveWorkerTarget
	}
	if hooks.Now == nil {
		hooks.Now = time.Now
	}

	return hooks
}
