package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"polka/backend"
	"polka/config"
	"polka/service"
)

// Test seams mirroring command_workers.go: tests replace these to avoid
// spawning a real runner or job.
var (
	startCronRunnerFunc = service.StartCronRunnerProcess
	stopCronRunnerFunc  = service.StopCronRunnerProcess
	cronPIDIsLiveFunc   = service.CronPIDIsLive
	cronNowFunc         = time.Now
	polkaExecutableFunc = os.Executable
	runCronJobOnceFunc  = runCronJobForeground
)

const (
	// cronListTimeLayout formats run times in `polka cron` listings.
	cronListTimeLayout = "2006-01-02 15:04"
	// cronRunnerLineLayout timestamps lines in the runner log.
	cronRunnerLineLayout = time.RFC3339
)

// cronCommandInput holds the flags of `polka cron`.
type cronCommandInput struct {
	Follow bool
}

// newCronCommand builds `polka cron [list|run <job>|logs [job]]` and its
// hidden `runner` subcommand, which is the background process serve starts.
func newCronCommand(ctx *commandContext) *cobra.Command {
	input := cronCommandInput{}
	cmd := &cobra.Command{
		Use:  "cron [list|run <job>|logs [job]]",
		Args: maximumArgsError("cron accepts a subcommand and at most one job name", 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}
			action := "list"
			if len(args) > 0 {
				action = strings.ToLower(strings.TrimSpace(args[0]))
			}
			job := ""
			if len(args) > 1 {
				job = strings.ToLower(strings.TrimSpace(args[1]))
			}

			ctx.exitCode = runCron(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), store, action, job, input.Follow)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&input.Follow, "follow", "f", false, "follow cron logs (logs only)")
	configureCommand(cmd, cronUsage)
	cmd.AddCommand(newCronRunnerCommand(ctx))

	return cmd
}

// newCronRunnerCommand builds the hidden `polka cron runner --env <name>`
// command that serve launches in the background. It runs until SIGTERM or
// Ctrl+C.
func newCronRunnerCommand(ctx *commandContext) *cobra.Command {
	var environmentName string
	cmd := &cobra.Command{
		Use:    "runner --env <name>",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			// The runner outlives the terminal that started serve, so a
			// hangup must not stop it; polka stop sends SIGTERM instead.
			signal.Ignore(syscall.SIGHUP)
			signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			ctx.exitCode = runCronRunner(signalContext, cmd.ErrOrStderr(), store, environmentName)
			return nil
		},
	}
	cmd.Flags().StringVar(&environmentName, "env", "", "environment whose cron jobs to run")

	return cmd
}

// runCron executes one `polka cron` subcommand for the active environment.
func runCron(ctx context.Context, stdout, stderr io.Writer, store backend.Store, action, job string, follow bool) int {
	current, err := store.Current()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	environment := *current

	switch action {
	case "list":
		if job != "" {
			fmt.Fprintln(stderr, "error: cron list does not take a job name")
			return 2
		}
		if err := writeCronList(stdout, store, environment, cronNowFunc()); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		return 0
	case "run":
		if job == "" {
			fmt.Fprintln(stderr, "error: cron run requires a job name")
			return 2
		}
		return runCronJobNow(stdout, stderr, store, environment, job)
	case "logs":
		return runCronLogs(ctx, stdout, stderr, store, environment, job, follow)
	default:
		fmt.Fprintf(stderr, "error: unknown cron subcommand %q; use list, run, or logs\n", action)
		return 2
	}
}

// writeCronList prints the runner state and one row per configured job with
// its schedule, next run, last result, and command.
func writeCronList(stdout io.Writer, store backend.Store, environment backend.Environment, now time.Time) error {
	if len(environment.Cron) == 0 {
		_, err := fmt.Fprintf(stdout, "No cron jobs configured for environment %q. Add them under cron in the environment config.\n", environment.Name)
		return err
	}

	runner, err := service.LoadLiveCronState(store.RootDir, environment.Name, cronPIDIsLiveFunc)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, cronRunnerSummary(environment, runner)); err != nil {
		return err
	}
	history, err := service.LoadCronRunsState(service.CronRunsPath(store.RootDir, environment.Name))
	if err != nil {
		return err
	}

	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "JOB\tSCHEDULE\tNEXT RUN\tLAST RUN\tCOMMAND")
	for _, name := range config.SortedCronJobNames(environment.Cron) {
		job := environment.Cron[name]
		next := "-"
		if schedule, err := config.ParseCronSchedule(job.Schedule); err == nil {
			if at := schedule.Next(now); !at.IsZero() {
				next = at.Format(cronListTimeLayout)
			}
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", name, job.Schedule, next, cronLastRunSummary(history.Jobs[name]), job.Command)
	}

	return writer.Flush()
}

// cronRunnerSummary describes whether the runner is up, and whether it runs
// the current config.
func cronRunnerSummary(environment backend.Environment, runner *service.CronRunnerState) string {
	if runner == nil {
		return fmt.Sprintf("Cron runner for environment %q is stopped; polka serve starts it.", environment.Name)
	}
	summary := fmt.Sprintf("Cron runner for environment %q is running (pid %d, since %s).", environment.Name, runner.PID, runner.StartedAt.Local().Format(cronListTimeLayout))
	if fingerprint, err := service.CronFingerprint(environment.Cron); err == nil && fingerprint != runner.Fingerprint {
		summary += " The cron config changed since it started; run polka serve to apply it."
	}

	return summary
}

// cronLastRunSummary renders one job's latest result for the list.
func cronLastRunSummary(run service.CronJobRunState) string {
	switch {
	case run.Running:
		return run.LastStart.Local().Format(cronListTimeLayout) + " (running)"
	case run.LastStart.IsZero():
		return "never"
	case run.LastError != "":
		return run.LastStart.Local().Format(cronListTimeLayout) + " (failed to start)"
	default:
		return fmt.Sprintf("%s (exit %d)", run.LastStart.Local().Format(cronListTimeLayout), run.LastExitCode)
	}
}

// runCronJobNow runs one configured job in the foreground with the terminal
// attached and returns its exit code. It does not touch the runner's state.
func runCronJobNow(stdout, stderr io.Writer, store backend.Store, environment backend.Environment, name string) int {
	job, ok := environment.Cron[name]
	if !ok {
		fmt.Fprintf(stderr, "error: cron job %q is not defined for environment %q\n", name, environment.Name)
		return 1
	}

	single := environment
	single.Cron = map[string]config.CronJobConfig{name: job}
	specs, err := service.BuildCronJobSpecs(managedServiceContext(store, single, stderr), cronJobBuildHooks(store, environment))
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if len(specs) == 0 {
		// BuildCronJobSpecs already warned about why the job was skipped.
		return 1
	}

	exitCode, err := runCronJobOnceFunc(stdout, stderr, specs[0])
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	return exitCode
}

// runCronJobForeground runs a job spec with the terminal's stdio.
func runCronJobForeground(stdout, stderr io.Writer, spec service.CronJobSpec) (int, error) {
	command, err := prepareCommand(spec.Target, spec.Args)
	if err != nil {
		return 0, err
	}
	command.Dir = spec.Dir
	command.Env = append(append([]string(nil), spec.Env...), "POLKA_CRON_JOB="+spec.Name)
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr

	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 0, fmt.Errorf("run cron job %s: %w", spec.Name, err)
	}

	return 0, nil
}

// runCronLogs prints (or follows) one job's log, or, without a job, the
// runner log followed by every configured job's log.
func runCronLogs(ctx context.Context, stdout, stderr io.Writer, store backend.Store, environment backend.Environment, job string, follow bool) int {
	var paths []string
	if job != "" {
		if _, ok := environment.Cron[job]; !ok {
			fmt.Fprintf(stderr, "error: cron job %q is not defined for environment %q\n", job, environment.Name)
			return 1
		}
		paths = []string{service.CronJobLogPath(store.RootDir, environment.Name, job)}
	} else {
		paths = append(paths, service.CronRunnerLogPath(store.RootDir, environment.Name))
		for _, name := range config.SortedCronJobNames(environment.Cron) {
			paths = append(paths, service.CronJobLogPath(store.RootDir, environment.Name, name))
		}
	}

	var (
		found bool
		err   error
	)
	if follow {
		found, err = followLogFiles(ctx, stdout, paths, logFollowPollInterval)
	} else {
		for _, path := range paths {
			var wrote bool
			wrote, err = writeLogFile(stdout, path)
			if err != nil {
				break
			}
			found = found || wrote
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if !found {
		fmt.Fprintf(stderr, "error: no cron logs found for environment %q; jobs log after their first run\n", environment.Name)
		return 1
	}

	return 0
}

// runCronRunner is the body of the background runner: it resolves the
// environment's jobs once and schedules them until ctx is cancelled. Its
// output goes to the runner log.
func runCronRunner(ctx context.Context, logOutput io.Writer, store backend.Store, environmentName string) int {
	logf := func(format string, args ...any) {
		fmt.Fprintf(logOutput, "%s %s", time.Now().Format(cronRunnerLineLayout), fmt.Sprintf(format, args...))
	}

	current, err := store.Current()
	if err != nil {
		logf("Cron runner cannot load the environment: %v.\n", err)
		return 1
	}
	// Managed tools resolve against the active environment, so the runner
	// only starts while the environment it was launched for is active.
	if current.Name != environmentName {
		logf("Cron runner was started for environment %q, but %q is active; run polka serve again.\n", environmentName, current.Name)
		return 1
	}
	if len(current.Cron) == 0 {
		logf("No cron jobs are configured for environment %q.\n", environmentName)
		return 1
	}

	serviceContext := managedServiceContext(store, *current, logOutput)
	specs, err := service.BuildCronJobSpecs(serviceContext, cronJobBuildHooks(store, *current))
	if err != nil {
		logf("Cron runner cannot prepare jobs: %v.\n", err)
		return 1
	}
	if len(specs) == 0 {
		logf("No cron job for environment %q could be prepared.\n", environmentName)
		return 1
	}

	runsPath := service.CronRunsPath(store.RootDir, environmentName)
	history, err := service.LoadCronRunsState(runsPath)
	if err != nil {
		logf("Starting with empty cron run history: %v.\n", err)
		history = service.CronRunsState{}
	}
	history.EnvironmentName = environmentName

	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name+" ("+spec.Schedule.String()+")")
	}
	logf("Cron runner started for environment %q with %d job(s): %s.\n", environmentName, len(specs), strings.Join(names, ", "))
	err = service.RunCronScheduler(ctx, specs, history, service.CronSchedulerHooks{
		SaveRuns: func(state service.CronRunsState) error { return service.WriteCronRunsState(runsPath, state) },
		Logf:     logf,
	})
	if err != nil {
		logf("Cron runner failed: %v.\n", err)
		return 1
	}
	logf("Cron runner stopped.\n")

	return 0
}

// cronRuntimeHooks wires the CLI-only pieces into the service-layer runner
// lifecycle: how to launch Polka itself as the runner.
func cronRuntimeHooks(store backend.Store, environment backend.Environment) service.CronRuntimeHooks {
	return service.CronRuntimeHooks{
		BuildRunnerSpec: func(service.Context) (service.CronRunnerSpec, error) {
			return buildCronRunnerSpec(store, environment.Name)
		},
		StartRunner: startCronRunnerFunc,
		StopRunner:  stopCronRunnerFunc,
		PIDIsLive:   cronPIDIsLiveFunc,
		Now:         cronNowFunc,
	}
}

// cronJobBuildHooks resolves cron job commands exactly like worker commands:
// the `polka exec` environment and managed-tool targets.
func cronJobBuildHooks(store backend.Store, environment backend.Environment) service.CronJobBuildHooks {
	return service.CronJobBuildHooks{
		BuildEnv: func(service.Context) ([]string, error) {
			return buildWorkerBaseEnvironment(store, environment.Name)
		},
		ResolveTarget: func(_ service.Context, argv []string, env []string) (string, []string, []string, error) {
			return resolveWorkerTarget(store, argv, env)
		},
	}
}

// buildCronRunnerSpec returns the command line that runs this Polka binary as
// the cron runner for one environment of this project.
func buildCronRunnerSpec(store backend.Store, environmentName string) (service.CronRunnerSpec, error) {
	executable, err := polkaExecutableFunc()
	if err != nil {
		return service.CronRunnerSpec{}, fmt.Errorf("resolve polka executable: %w", err)
	}

	return service.CronRunnerSpec{
		EnvironmentName: environmentName,
		Target:          executable,
		Args:            []string{"-C", store.ProjectDir, "cron", "runner", "--env", environmentName},
		Dir:             store.ProjectDir,
		Env:             os.Environ(),
		LogPath:         service.CronRunnerLogPath(store.RootDir, environmentName),
	}, nil
}

// stopManagedCron stops the environment's cron runner.
func stopManagedCron(store backend.Store, environment backend.Environment) (service.CronRunnerState, bool, error) {
	return service.StopManagedCron(managedServiceContext(store, environment, nil), cronRuntimeHooks(store, environment))
}

// loadLiveCronState loads the environment's runner state if it is running.
func loadLiveCronState(rootDir, environmentName string) (*service.CronRunnerState, error) {
	return service.LoadLiveCronState(rootDir, environmentName, cronPIDIsLiveFunc)
}
