package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"polka/backend"
	"polka/service"
)

// Test seams mirroring command_redis.go: tests replace these to avoid spawning
// real worker processes.
var (
	startWorkerProcessFunc = service.StartWorkerProcess
	stopWorkerProcessFunc  = service.StopWorkerProcess
	workerPIDIsLiveFunc    = service.WorkerPIDIsLive
	workersNowFunc         = time.Now
)

type workersRuntimeState = service.WorkersRuntimeState

// workersRuntimeHooks wires the CLI-only pieces into the service-layer worker
// runtime: the managed shell environment and managed-tool target resolution.
func workersRuntimeHooks(store backend.Store, environment backend.Environment) service.WorkersRuntimeHooks {
	return service.WorkersRuntimeHooks{
		StartWorker: startWorkerProcessFunc,
		StopWorker:  stopWorkerProcessFunc,
		PIDIsLive:   workerPIDIsLiveFunc,
		BuildEnv: func(service.Context) ([]string, error) {
			return buildWorkerBaseEnvironment(store, environment.Name)
		},
		ResolveTarget: func(_ service.Context, argv []string, env []string) (string, []string, []string, error) {
			return resolveWorkerTarget(store, argv, env)
		},
		Now: workersNowFunc,
	}
}

func stopManagedWorkers(store backend.Store, environment backend.Environment) (workersRuntimeState, bool, error) {
	return service.StopManagedWorkers(managedServiceContext(store, environment, nil), workersRuntimeHooks(store, environment))
}

func loadLiveWorkersState(rootDir, environmentName string) (*workersRuntimeState, error) {
	return service.LoadLiveWorkersState(rootDir, environmentName, workerPIDIsLiveFunc)
}

// buildWorkerBaseEnvironment builds the same execution environment `polka sh`
// and `polka exec` use: the configured env-file, framework runtime variables,
// env-vars, and PATH with the Polka bin and vendor bin directories, so worker
// commands (and anything they spawn) resolve managed tools.
func buildWorkerBaseEnvironment(store backend.Store, environmentName string) ([]string, error) {
	context, err := buildShellSessionContext(runtime.GOOS, store, store.ProjectDir, environmentName)
	if err != nil {
		return nil, err
	}

	return buildShellExecutionEnvironment(runtime.GOOS, os.Environ(), store, context)
}

// resolveWorkerTarget maps a worker command's argv[0] to a concrete executable.
// Managed tools resolve to the real installed binary (mirroring `polka
// dispatch`) rather than the bin-dir shim, so the recorded PID is the actual
// worker process and stopping it does not orphan a dispatched child.
func resolveWorkerTarget(store backend.Store, argv []string, env []string) (string, []string, []string, error) {
	command := strings.TrimSpace(argv[0])
	args := argv[1:]
	if filepath.IsAbs(command) || hasPathSeparator(command) {
		return command, args, env, nil
	}

	target, err := store.ResolveTool(command)
	if err != nil {
		// Not a managed tool: fall back to the shell PATH (vendor binaries,
		// system commands).
		fallback, fallbackErr := resolveExecTarget(runtime.GOOS, command, env)
		if fallbackErr != nil {
			return "", nil, nil, fallbackErr
		}
		return fallback, args, env, nil
	}

	usesManagedPHP := strings.EqualFold(command, "php") || strings.EqualFold(command, "frankenphp")
	if dispatchPHARRequiresManagedPHP(command, target) {
		phpTarget, resolveErr := store.ResolveTool("php")
		if resolveErr != nil {
			return "", nil, nil, fmt.Errorf("resolve php for %s: %w", strings.ToLower(command), resolveErr)
		}
		args = append([]string{target}, args...)
		target = phpTarget
		usesManagedPHP = true
	}
	if usesManagedPHP {
		env, err = applyManagedPHPRuntimeConfig(runtime.GOOS, env, target)
		if err != nil {
			return "", nil, nil, err
		}
	}

	return target, args, env, nil
}

// workerProcessSummary renders live worker replicas as "queue x2, scheduler"
// with names sorted for stable output.
func workerProcessSummary(state workersRuntimeState) string {
	counts := make(map[string]int)
	for _, process := range state.Processes {
		counts[process.Name]++
	}

	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		if counts[name] > 1 {
			parts = append(parts, fmt.Sprintf("%s x%d", name, counts[name]))
		} else {
			parts = append(parts, name)
		}
	}

	return strings.Join(parts, ", ")
}
