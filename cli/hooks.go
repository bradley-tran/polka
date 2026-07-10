package cli

import (
	"fmt"
	"io"
	"strings"

	"polka/backend"
	"polka/config"
	"polka/service"
)

type cliHookRegistry struct {
	startServices      []startServiceHook
	webservers         []webserverStartHook
	stopHooks          []stopHook
	configStatusHooks  []statusHook
	runtimeStatusHooks []statusHook
}

type startHookContext struct {
	Stdout      io.Writer
	Stderr      io.Writer
	Store       backend.Store
	Environment backend.Environment
}

type webserverStartHookContext struct {
	Stdout      io.Writer
	Stderr      io.Writer
	Store       backend.Store
	Environment backend.Environment
	ServerType  string
	Input       serveCommandInput
	Endpoint    serverEndpoint
	Layout      serveAppLayout
}

type stopHookContext struct {
	Stdout      io.Writer
	Stderr      io.Writer
	Store       backend.Store
	Environment backend.Environment
}

type statusHookContext struct {
	Stdout      io.Writer
	Stderr      io.Writer
	Store       backend.Store
	Environment backend.Environment
}

type startServiceHook struct {
	id  string
	run func(startHookContext) error
}

type webserverStartHook struct {
	id      string
	matches func(webserverStartHookContext) bool
	run     func(webserverStartHookContext) (int, error)
}

type stopHook struct {
	id  string
	run func(stopHookContext) error
}

type statusHook struct {
	id  string
	run func(statusHookContext) error
}

func defaultCLIHookRegistry() cliHookRegistry {
	return cliHookRegistry{
		startServices: []startServiceHook{
			{id: "database", run: startDatabaseServiceHook},
			{id: "mailpit", run: startMailpitServiceHook},
			{id: "meilisearch", run: startMeilisearchServiceHook},
			{id: "redis", run: startRedisServiceHook},
			{id: "traefik", run: startTraefikServiceHook},
			{id: "phpmyadmin", run: startPHPMyAdminServiceHook},
		},
		webservers: []webserverStartHook{
			{id: "nginx", matches: environmentUsesNginx, run: startNginxWebserverHook},
			{id: "apache", matches: environmentUsesApache, run: startApacheWebserverHook},
			{id: "frankenphp", matches: environmentUsesFrankenPHP, run: startFrankenPHPWebserverHook},
			{id: "php", matches: environmentUsesPHPWebserver, run: startPHPWebserverHook},
		},
		stopHooks: []stopHook{
			{id: "webserver", run: stopWebserverHook},
			{id: "workers", run: stopWorkersServiceHook},
			{id: "phpmyadmin", run: stopPHPMyAdminServiceHook},
			{id: "traefik", run: stopTraefikServiceHook},
			{id: "redis", run: stopRedisServiceHook},
			{id: "meilisearch", run: stopMeilisearchServiceHook},
			{id: "database", run: stopDatabaseServiceHook},
			{id: "mailpit", run: stopMailpitServiceHook},
		},
		configStatusHooks: []statusHook{
			{id: "php", run: statusPHPConfigHook},
			{id: "composer", run: statusComposerConfigHook},
			{id: "pie", run: statusPIEConfigHook},
			{id: "nodejs", run: statusNodeJSConfigHook},
			{id: "mago", run: statusMagoConfigHook},
			{id: "nginx", run: statusNginxConfigHook},
			{id: "apache", run: statusApacheConfigHook},
			{id: "frankenphp", run: statusFrankenPHPConfigHook},
			{id: "roadrunner", run: statusRoadRunnerConfigHook},
			{id: "sqlite", run: statusSQLiteConfigHook},
			{id: "meilisearch", run: statusMeilisearchConfigHook},
			{id: "redis", run: statusRedisConfigHook},
			{id: "traefik", run: statusTraefikConfigHook},
			{id: "phpmyadmin", run: statusPHPMyAdminConfigHook},
			{id: "database", run: statusDatabaseConfigHook},
			{id: "mailpit", run: statusMailpitConfigHook},
			{id: "workers", run: statusWorkersConfigHook},
		},
		runtimeStatusHooks: []statusHook{
			{id: "webserver", run: statusWebserverRuntimeHook},
			{id: "workers", run: statusWorkersRuntimeHook},
			{id: "phpmyadmin", run: statusPHPMyAdminRuntimeHook},
			{id: "meilisearch", run: statusMeilisearchRuntimeHook},
			{id: "redis", run: statusRedisRuntimeHook},
			{id: "traefik", run: statusTraefikRuntimeHook},
			{id: "database", run: statusDatabaseRuntimeHook},
			{id: "mailpit", run: statusMailpitRuntimeHook},
		},
	}
}

func (r cliHookRegistry) StartServices(ctx startHookContext) error {
	result, err := service.DefaultManager().Start(managedServiceContext(ctx.Store, ctx.Environment, ctx.Stderr), service.RuntimeHooks{
		Database:                dbRuntimeHooks(),
		Mailpit:                 mailpitRuntimeHooks(),
		Meilisearch:             meilisearchRuntimeHooks(),
		Redis:                   redisRuntimeHooks(),
		Traefik:                 traefikRuntimeHooks(),
		PHPMyAdmin:              phpMyAdminRuntimeHooks(ctx.Store),
		Workers:                 workersRuntimeHooks(ctx.Store, ctx.Environment),
		EnsurePHPMyAdminStorage: ensurePHPMyAdminStorageConfiguredFunc,
	})
	if err != nil {
		return err
	}
	if result.PHPMyAdmin != nil {
		if result.PHPMyAdmin.AlreadyStarted {
			fmt.Fprintf(ctx.Stdout, "phpMyAdmin for environment %q is already running at %s.\n", ctx.Environment.Name, serveStateURL(result.PHPMyAdmin.State))
		} else {
			fmt.Fprintf(ctx.Stdout, "Started phpMyAdmin for environment %q at %s.\n", ctx.Environment.Name, serveStateURL(result.PHPMyAdmin.State))
		}
	}
	if result.Workers != nil {
		if result.Workers.AlreadyStarted {
			fmt.Fprintf(ctx.Stdout, "Workers for environment %q are already running.\n", ctx.Environment.Name)
		} else if len(result.Workers.State.Processes) > 0 {
			// When every replica failed to launch, the failures were already
			// reported as warnings; there is nothing to summarize.
			fmt.Fprintf(ctx.Stdout, "Started %d worker process(es) for environment %q (%s).\n", len(result.Workers.State.Processes), ctx.Environment.Name, workerProcessSummary(result.Workers.State))
		}
	}

	return nil
}

func (r cliHookRegistry) StartWebserver(ctx webserverStartHookContext) (int, error) {
	for _, hook := range r.webservers {
		if hook.matches(ctx) {
			return hook.run(ctx)
		}
	}

	return 0, fmt.Errorf("environment %q does not define a supported webserver", ctx.Environment.Name)
}

func (r cliHookRegistry) Stop(ctx stopHookContext) error {
	for _, hook := range r.stopHooks {
		if hook.id != "webserver" {
			continue
		}
		if err := hook.run(ctx); err != nil {
			return err
		}
	}

	result, err := service.DefaultManager().Stop(managedServiceContext(ctx.Store, ctx.Environment, ctx.Stderr), service.RuntimeHooks{
		Database:    dbRuntimeHooks(),
		Mailpit:     mailpitRuntimeHooks(),
		Meilisearch: meilisearchRuntimeHooks(),
		Redis:       redisRuntimeHooks(),
		Traefik:     traefikRuntimeHooks(),
		PHPMyAdmin:  phpMyAdminRuntimeHooks(ctx.Store),
		Workers:     workersRuntimeHooks(ctx.Store, ctx.Environment),
	})
	if err != nil {
		return err
	}
	writeManagedServiceStopSummary(ctx, result)

	return nil
}

func (r cliHookRegistry) WriteConfigStatus(ctx statusHookContext) error {
	for _, hook := range r.configStatusHooks {
		if err := hook.run(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (r cliHookRegistry) WriteRuntimeStatus(ctx statusHookContext) error {
	service.DefaultManager().WarnMissingRuntimeTools(managedServiceContext(ctx.Store, ctx.Environment, ctx.Stderr))
	for _, hook := range r.runtimeStatusHooks {
		if err := hook.run(ctx); err != nil {
			return err
		}
	}

	return nil
}

func writeManagedServiceStopSummary(ctx stopHookContext, result service.StopResult) {
	if result.Workers != nil {
		if result.Workers.AlreadyStopped {
			if len(ctx.Environment.Workers) > 0 {
				fmt.Fprintf(ctx.Stdout, "Workers for environment %q are already stopped.\n", ctx.Environment.Name)
			}
		} else {
			fmt.Fprintf(ctx.Stdout, "Stopped %d worker process(es) for environment %q.\n", len(result.Workers.State.Processes), ctx.Environment.Name)
		}
	}

	if result.PHPMyAdmin != nil {
		if result.PHPMyAdmin.AlreadyStopped {
			if ctx.Environment.PHPMyAdmin != nil && strings.TrimSpace(ctx.Environment.PHPMyAdmin.Version) != "" {
				fmt.Fprintf(ctx.Stdout, "phpMyAdmin for environment %q is already stopped.\n", ctx.Environment.Name)
			}
		} else {
			fmt.Fprintf(ctx.Stdout, "Stopped phpMyAdmin for environment %q.\n", ctx.Environment.Name)
		}
	}

	if result.Meilisearch != nil {
		if result.Meilisearch.AlreadyStopped {
			if ctx.Environment.Meilisearch != nil && strings.TrimSpace(ctx.Environment.Meilisearch.Version) != "" {
				fmt.Fprintf(ctx.Stdout, "Meilisearch for environment %q is already stopped.\n", ctx.Environment.Name)
			}
		} else {
			fmt.Fprintf(ctx.Stdout, "Stopped Meilisearch for environment %q.\n", ctx.Environment.Name)
		}
	}

	if result.Redis != nil {
		if result.Redis.AlreadyStopped {
			if ctx.Environment.Redis != nil && strings.TrimSpace(ctx.Environment.Redis.Version) != "" {
				fmt.Fprintf(ctx.Stdout, "Redis for environment %q is already stopped.\n", ctx.Environment.Name)
			}
		} else {
			fmt.Fprintf(ctx.Stdout, "Stopped Redis for environment %q.\n", ctx.Environment.Name)
		}
	}

	if result.Traefik != nil {
		if result.Traefik.AlreadyStopped {
			if ctx.Environment.Traefik != nil && strings.TrimSpace(ctx.Environment.Traefik.Version) != "" {
				fmt.Fprintf(ctx.Stdout, "Traefik for environment %q is already stopped.\n", ctx.Environment.Name)
			}
		} else {
			fmt.Fprintf(ctx.Stdout, "Stopped Traefik for environment %q.\n", ctx.Environment.Name)
		}
	}

	if result.Database != nil {
		if result.Database.AlreadyStopped {
			if ctx.Environment.Database != nil && strings.TrimSpace(ctx.Environment.Database.Engine) != "" {
				fmt.Fprintf(ctx.Stdout, "Database for environment %q is already stopped.\n", ctx.Environment.Name)
			}
		} else {
			fmt.Fprintf(ctx.Stdout, "Stopped %s for environment %q.\n", result.Database.State.Engine, ctx.Environment.Name)
		}
	}

	if result.Mailpit != nil {
		if result.Mailpit.AlreadyStopped {
			if ctx.Environment.Mailpit != nil && strings.TrimSpace(ctx.Environment.Mailpit.Version) != "" {
				fmt.Fprintf(ctx.Stdout, "Mailpit for environment %q is already stopped.\n", ctx.Environment.Name)
			}
		} else {
			fmt.Fprintf(ctx.Stdout, "Stopped mailpit for environment %q.\n", ctx.Environment.Name)
		}
	}
}

func startDatabaseServiceHook(ctx startHookContext) error {
	if ctx.Environment.Database == nil || strings.TrimSpace(ctx.Environment.Database.Engine) == "" {
		return nil
	}

	resolved := dbResolvedEnvironment{Environment: ctx.Environment, Database: ctx.Environment.Database}
	_, _, err := ensureManagedDatabaseStarted(ctx.Store, resolved)
	return err
}

func startMailpitServiceHook(ctx startHookContext) error {
	if ctx.Environment.Mailpit == nil || strings.TrimSpace(ctx.Environment.Mailpit.Version) == "" {
		return nil
	}

	_, _, err := ensureManagedMailpitStarted(ctx.Store, ctx.Environment)
	return err
}

func startMeilisearchServiceHook(ctx startHookContext) error {
	if ctx.Environment.Meilisearch == nil || strings.TrimSpace(ctx.Environment.Meilisearch.Version) == "" {
		return nil
	}

	_, _, err := ensureManagedMeilisearchStarted(ctx.Store, ctx.Environment)
	return err
}

func startRedisServiceHook(ctx startHookContext) error {
	if ctx.Environment.Redis == nil || strings.TrimSpace(ctx.Environment.Redis.Version) == "" {
		return nil
	}

	_, _, err := ensureManagedRedisStarted(ctx.Store, ctx.Environment)
	return err
}

func startTraefikServiceHook(ctx startHookContext) error {
	if ctx.Environment.Traefik == nil || strings.TrimSpace(ctx.Environment.Traefik.Version) == "" {
		return nil
	}

	_, _, err := ensureManagedTraefikStarted(ctx.Store, ctx.Environment)
	return err
}

func startPHPMyAdminServiceHook(ctx startHookContext) error {
	if ctx.Environment.PHPMyAdmin == nil || strings.TrimSpace(ctx.Environment.PHPMyAdmin.Version) == "" {
		return nil
	}

	if err := ensurePHPMyAdminStorageConfiguredFunc(managedServiceContext(ctx.Store, ctx.Environment, ctx.Stderr), ctx.Environment, dbRuntimeHooks()); err != nil {
		return err
	}

	state, alreadyStarted, err := ensureManagedPHPMyAdminStarted(ctx.Store, ctx.Environment)
	if err != nil {
		return err
	}
	if alreadyStarted {
		fmt.Fprintf(ctx.Stdout, "phpMyAdmin for environment %q is already running at %s.\n", ctx.Environment.Name, serveStateURL(state))
		return nil
	}

	fmt.Fprintf(ctx.Stdout, "Started phpMyAdmin for environment %q at %s.\n", ctx.Environment.Name, serveStateURL(state))
	return nil
}

func environmentUsesNginx(ctx webserverStartHookContext) bool {
	return ctx.ServerType == config.ServerTypeNginx
}

func environmentUsesApache(ctx webserverStartHookContext) bool {
	return ctx.ServerType == config.ServerTypeApache
}

func environmentUsesFrankenPHP(ctx webserverStartHookContext) bool {
	return ctx.ServerType == config.ServerTypeFrankenPHP
}

func environmentUsesPHPWebserver(ctx webserverStartHookContext) bool {
	return ctx.ServerType == config.ServerTypePHP
}

func startPHPWebserverHook(ctx webserverStartHookContext) (int, error) {
	if ctx.Input.Watch {
		return runPHPRuntimeServeFunc(ctx.Stdout, ctx.Stderr, ctx.Store, ctx.Endpoint.Address, ctx.Layout)
	}

	startedState, err := startBackgroundPHPRuntimeServe(ctx.Store, ctx.Environment, ctx.Endpoint.Address, ctx.Layout)
	if err != nil {
		return 0, err
	}

	return finishBackgroundWebserverStart(ctx, startedState)
}

func startNginxWebserverHook(ctx webserverStartHookContext) (int, error) {
	if backend.PrimaryPHPVersion(ctx.Environment) == "" {
		return 0, fmt.Errorf("environment %q defines nginx but does not define a php version", ctx.Environment.Name)
	}

	if ctx.Input.Watch {
		_, _ = fmt.Fprintf(ctx.Stdout, "nginx webserver started at %s\n", serverEndpointURL(ctx.Endpoint))
		return runNginxServeFunc(ctx.Stdout, ctx.Stderr, ctx.Store, ctx.Environment, ctx.Endpoint, ctx.Layout)
	}

	startedState, err := startBackgroundNginxServe(ctx.Store, ctx.Environment, ctx.Endpoint, ctx.Layout)
	if err != nil {
		return 0, err
	}

	return finishBackgroundWebserverStart(ctx, startedState)
}

func startApacheWebserverHook(ctx webserverStartHookContext) (int, error) {
	if backend.PrimaryPHPVersion(ctx.Environment) == "" {
		return 0, fmt.Errorf("environment %q defines apache but does not define a php version", ctx.Environment.Name)
	}

	if ctx.Input.Watch {
		_, _ = fmt.Fprintf(ctx.Stdout, "apache webserver started at %s\n", serverEndpointURL(ctx.Endpoint))
		return runApacheServeFunc(ctx.Stdout, ctx.Stderr, ctx.Store, ctx.Environment, ctx.Endpoint, ctx.Layout)
	}

	startedState, err := startBackgroundApacheServe(ctx.Store, ctx.Environment, ctx.Endpoint, ctx.Layout)
	if err != nil {
		return 0, err
	}

	return finishBackgroundWebserverStart(ctx, startedState)
}

func startFrankenPHPWebserverHook(ctx webserverStartHookContext) (int, error) {
	if ctx.Input.Watch {
		_, _ = fmt.Fprintf(ctx.Stdout, "FrankenPHP webserver started at %s\n", serverEndpointURL(ctx.Endpoint))
		return runFrankenPHPServeFunc(ctx.Stdout, ctx.Stderr, ctx.Store, ctx.Environment, ctx.Endpoint, ctx.Layout)
	}

	startedState, err := startBackgroundFrankenPHPServe(ctx.Store, ctx.Environment, ctx.Endpoint, ctx.Layout)
	if err != nil {
		return 0, err
	}

	return finishBackgroundWebserverStart(ctx, startedState)
}

func finishBackgroundWebserverStart(ctx webserverStartHookContext, startedState serveRuntimeState) (int, error) {
	if strings.TrimSpace(startedState.EnvironmentName) == "" {
		startedState.EnvironmentName = ctx.Environment.Name
	}
	if strings.TrimSpace(startedState.ServerKind) == "" {
		startedState.ServerKind = ctx.ServerType
	}
	if strings.TrimSpace(startedState.ServerScheme) == "" {
		startedState.ServerScheme = ctx.Endpoint.Scheme
	}
	if strings.TrimSpace(startedState.ServerAddress) == "" {
		startedState.ServerAddress = ctx.Endpoint.Address
	}
	if strings.TrimSpace(startedState.Docroot) == "" {
		startedState.Docroot = ctx.Layout.Docroot
	}
	if startedState.StartedAt.IsZero() {
		startedState.StartedAt = serveNowFunc().UTC()
	}
	if err := writeServeState(serveStatePath(ctx.Store.RootDir, ctx.Environment.Name), startedState); err != nil {
		_ = stopServeRuntimeFunc(startedState)
		return 0, err
	}

	fmt.Fprintf(ctx.Stdout, "Started %s for environment %q at %s.\n", serveRuntimeLabel(startedState.ServerKind), ctx.Environment.Name, serveStateURL(startedState))
	fmt.Fprintln(ctx.Stdout, "Run `polka stop` to stop it.")
	return 0, nil
}

func stopWebserverHook(ctx stopHookContext) error {
	state, alreadyStopped, err := stopManagedServe(ctx.Store, ctx.Environment.Name)
	if err != nil {
		return err
	}
	if alreadyStopped {
		fmt.Fprintf(ctx.Stdout, "Webserver for environment %q is already stopped.\n", ctx.Environment.Name)
		return nil
	}

	fmt.Fprintf(ctx.Stdout, "Stopped %s for environment %q.\n", serveRuntimeLabel(state.ServerKind), ctx.Environment.Name)
	return nil
}

func stopDatabaseServiceHook(ctx stopHookContext) error {
	if ctx.Environment.Database == nil || strings.TrimSpace(ctx.Environment.Database.Engine) == "" {
		return nil
	}

	resolved := dbResolvedEnvironment{Environment: ctx.Environment, Database: ctx.Environment.Database}
	databaseState, databaseAlreadyStopped, err := service.StopManagedDatabase(managedServiceContext(ctx.Store, ctx.Environment, ctx.Stderr), resolved, dbRuntimeHooks())
	if err != nil {
		return err
	}
	if databaseAlreadyStopped {
		fmt.Fprintf(ctx.Stdout, "Database for environment %q is already stopped.\n", ctx.Environment.Name)
		return nil
	}

	fmt.Fprintf(ctx.Stdout, "Stopped %s for environment %q.\n", databaseState.Engine, ctx.Environment.Name)
	return nil
}

func stopMailpitServiceHook(ctx stopHookContext) error {
	if ctx.Environment.Mailpit == nil || strings.TrimSpace(ctx.Environment.Mailpit.Version) == "" {
		return nil
	}

	_, mailpitAlreadyStopped, err := stopManagedMailpit(ctx.Store, ctx.Environment.Name)
	if err != nil {
		return err
	}
	if mailpitAlreadyStopped {
		fmt.Fprintf(ctx.Stdout, "Mailpit for environment %q is already stopped.\n", ctx.Environment.Name)
		return nil
	}

	fmt.Fprintf(ctx.Stdout, "Stopped mailpit for environment %q.\n", ctx.Environment.Name)
	return nil
}

func stopMeilisearchServiceHook(ctx stopHookContext) error {
	if ctx.Environment.Meilisearch == nil || strings.TrimSpace(ctx.Environment.Meilisearch.Version) == "" {
		return nil
	}

	_, meilisearchAlreadyStopped, err := stopManagedMeilisearch(ctx.Store, ctx.Environment.Name)
	if err != nil {
		return err
	}
	if meilisearchAlreadyStopped {
		fmt.Fprintf(ctx.Stdout, "Meilisearch for environment %q is already stopped.\n", ctx.Environment.Name)
		return nil
	}

	fmt.Fprintf(ctx.Stdout, "Stopped Meilisearch for environment %q.\n", ctx.Environment.Name)
	return nil
}

func stopRedisServiceHook(ctx stopHookContext) error {
	if ctx.Environment.Redis == nil || strings.TrimSpace(ctx.Environment.Redis.Version) == "" {
		return nil
	}

	_, redisAlreadyStopped, err := stopManagedRedis(ctx.Store, ctx.Environment.Name)
	if err != nil {
		return err
	}
	if redisAlreadyStopped {
		fmt.Fprintf(ctx.Stdout, "Redis for environment %q is already stopped.\n", ctx.Environment.Name)
		return nil
	}

	fmt.Fprintf(ctx.Stdout, "Stopped Redis for environment %q.\n", ctx.Environment.Name)
	return nil
}

func stopTraefikServiceHook(ctx stopHookContext) error {
	if ctx.Environment.Traefik == nil || strings.TrimSpace(ctx.Environment.Traefik.Version) == "" {
		return nil
	}

	_, traefikAlreadyStopped, err := stopManagedTraefik(ctx.Store, ctx.Environment.Name)
	if err != nil {
		return err
	}
	if traefikAlreadyStopped {
		fmt.Fprintf(ctx.Stdout, "Traefik for environment %q is already stopped.\n", ctx.Environment.Name)
		return nil
	}

	fmt.Fprintf(ctx.Stdout, "Stopped Traefik for environment %q.\n", ctx.Environment.Name)
	return nil
}

func stopPHPMyAdminServiceHook(ctx stopHookContext) error {
	if ctx.Environment.PHPMyAdmin == nil || strings.TrimSpace(ctx.Environment.PHPMyAdmin.Version) == "" {
		return nil
	}

	_, phpMyAdminAlreadyStopped, err := stopManagedPHPMyAdmin(ctx.Store, ctx.Environment.Name)
	if err != nil {
		return err
	}
	if phpMyAdminAlreadyStopped {
		fmt.Fprintf(ctx.Stdout, "phpMyAdmin for environment %q is already stopped.\n", ctx.Environment.Name)
		return nil
	}

	fmt.Fprintf(ctx.Stdout, "Stopped phpMyAdmin for environment %q.\n", ctx.Environment.Name)
	return nil
}

// stopWorkersServiceHook stops the environment's background workers. It is
// registered for lifecycle symmetry with the other managed services; the
// combined Stop path handles workers through the service manager.
func stopWorkersServiceHook(ctx stopHookContext) error {
	if len(ctx.Environment.Workers) == 0 {
		return nil
	}

	state, alreadyStopped, err := stopManagedWorkers(ctx.Store, ctx.Environment)
	if err != nil {
		return err
	}
	if alreadyStopped {
		fmt.Fprintf(ctx.Stdout, "Workers for environment %q are already stopped.\n", ctx.Environment.Name)
		return nil
	}

	fmt.Fprintf(ctx.Stdout, "Stopped %d worker process(es) for environment %q.\n", len(state.Processes), ctx.Environment.Name)
	return nil
}

// statusWorkersConfigHook prints one line per configured worker.
func statusWorkersConfigHook(ctx statusHookContext) error {
	if len(ctx.Environment.Workers) == 0 {
		_, _ = fmt.Fprintln(ctx.Stdout, "workers unset")
		return nil
	}

	for _, name := range config.SortedWorkerNames(ctx.Environment.Workers) {
		worker := ctx.Environment.Workers[name]
		if replicas := config.EffectiveWorkerReplicas(worker); replicas > 1 {
			_, _ = fmt.Fprintf(ctx.Stdout, "worker %s: %s (replicas %d)\n", name, worker.Command, replicas)
		} else {
			_, _ = fmt.Fprintf(ctx.Stdout, "worker %s: %s\n", name, worker.Command)
		}
	}

	return nil
}

// statusWorkersRuntimeHook reports how many configured worker replicas are live.
func statusWorkersRuntimeHook(ctx statusHookContext) error {
	if len(ctx.Environment.Workers) == 0 {
		_, _ = fmt.Fprintln(ctx.Stdout, "workers unset")
		return nil
	}

	liveWorkersState, err := loadLiveWorkersState(ctx.Store.RootDir, ctx.Environment.Name)
	if err != nil {
		return err
	}
	if liveWorkersState == nil {
		_, _ = fmt.Fprintln(ctx.Stdout, "workers stopped")
		return nil
	}

	configured := 0
	for _, worker := range ctx.Environment.Workers {
		configured += config.EffectiveWorkerReplicas(worker)
	}
	_, _ = fmt.Fprintf(ctx.Stdout, "workers running %d/%d\n", len(liveWorkersState.Processes), configured)
	return nil
}

func statusPHPConfigHook(ctx statusHookContext) error {
	phpTool, phpVersion := backend.PHPCLIProvider(ctx.Environment)
	if phpTool == "" {
		phpTool = "php"
	}
	if phpTool == config.ServerTypeFrankenPHP {
		_, _ = fmt.Fprintf(ctx.Stdout, "php via frankenphp %s\n", phpVersion)
		return nil
	}
	_, _ = fmt.Fprintf(ctx.Stdout, "%s %s\n", phpTool, labelOrUnset(phpVersion))
	return nil
}

func statusComposerConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "composer %s\n", labelOrUnset(ctx.Environment.ComposerVersion))
	return nil
}

func statusPIEConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "pie %s\n", labelOrUnset(ctx.Environment.PIEVersion))
	return nil
}

func statusNodeJSConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "nodejs %s\n", labelOrUnset(ctx.Environment.NodeJSVersion))
	return nil
}

func statusMagoConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "mago %s\n", labelOrUnset(ctx.Environment.MagoVersion))
	return nil
}

func statusNginxConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "nginx %s\n", labelOrUnset(ctx.Environment.NginxVersion))
	return nil
}

func statusApacheConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "apache %s\n", labelOrUnset(ctx.Environment.ApacheVersion))
	return nil
}

func statusFrankenPHPConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "frankenphp %s\n", labelOrUnset(ctx.Environment.FrankenPHPVersion))
	return nil
}

func statusRoadRunnerConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "roadrunner %s\n", labelOrUnset(ctx.Environment.RoadRunnerVersion))
	return nil
}

func statusSQLiteConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "sqlite %s\n", labelOrUnset(ctx.Environment.SQLiteVersion))
	return nil
}

func statusMeilisearchConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "meilisearch %s\n", labelMeilisearch(ctx.Environment.Meilisearch))
	return nil
}

func statusRedisConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "redis %s\n", labelRedis(ctx.Environment.Redis))
	return nil
}

func statusTraefikConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "traefik %s\n", labelTraefik(ctx.Environment.Traefik))
	return nil
}

func statusPHPMyAdminConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "phpmyadmin %s\n", labelPHPMyAdmin(ctx.Environment.PHPMyAdmin))
	return nil
}

func statusDatabaseConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "database %s\n", labelDatabase(ctx.Environment.Database))
	return nil
}

func statusMailpitConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "mailpit %s\n", labelMailpit(ctx.Environment.Mailpit))
	return nil
}

func statusWebserverRuntimeHook(ctx statusHookContext) error {
	webServerState, err := loadWebServerState(ctx.Store.RootDir, ctx.Environment.Name)
	if err != nil {
		return err
	}
	if webServerState == nil {
		_, _ = fmt.Fprintln(ctx.Stdout, "webserver stopped")
		return nil
	}

	_, _ = fmt.Fprintf(ctx.Stdout, "webserver running %s\n", serveStateURL(*webServerState))
	return nil
}

func statusDatabaseRuntimeHook(ctx statusHookContext) error {
	if ctx.Environment.Database == nil || strings.TrimSpace(ctx.Environment.Database.Engine) == "" {
		_, _ = fmt.Fprintln(ctx.Stdout, "database-server unset")
		return nil
	}

	liveDatabaseState, err := service.LoadLiveManagedDatabaseState(ctx.Store.RootDir, ctx.Environment.Name, pingDatabaseAddressFunc)
	if err != nil {
		return err
	}
	if liveDatabaseState == nil {
		_, _ = fmt.Fprintln(ctx.Stdout, "database-server stopped")
		return nil
	}

	_, _ = fmt.Fprintf(ctx.Stdout, "database-server running %s:%s@%d\n", liveDatabaseState.Engine, liveDatabaseState.Version, liveDatabaseState.Port)
	return nil
}

func statusPHPMyAdminRuntimeHook(ctx statusHookContext) error {
	if ctx.Environment.PHPMyAdmin == nil || strings.TrimSpace(ctx.Environment.PHPMyAdmin.Version) == "" {
		_, _ = fmt.Fprintln(ctx.Stdout, "phpmyadmin-server unset")
		return nil
	}

	livePHPMyAdminState, err := loadLivePHPMyAdminState(ctx.Store.RootDir, ctx.Environment.Name)
	if err != nil {
		return err
	}
	if livePHPMyAdminState == nil {
		_, _ = fmt.Fprintln(ctx.Stdout, "phpmyadmin-server stopped")
		return nil
	}

	_, _ = fmt.Fprintf(ctx.Stdout, "phpmyadmin-server running %s\n", serveStateURL(*livePHPMyAdminState))
	return nil
}

func statusMeilisearchRuntimeHook(ctx statusHookContext) error {
	if ctx.Environment.Meilisearch == nil || strings.TrimSpace(ctx.Environment.Meilisearch.Version) == "" {
		_, _ = fmt.Fprintln(ctx.Stdout, "meilisearch-server unset")
		return nil
	}

	liveMeilisearchState, err := loadLiveMeilisearchState(ctx.Store.RootDir, ctx.Environment.Name)
	if err != nil {
		return err
	}
	if liveMeilisearchState == nil {
		_, _ = fmt.Fprintln(ctx.Stdout, "meilisearch-server stopped")
		return nil
	}

	_, _ = fmt.Fprintf(ctx.Stdout, "meilisearch-server running %s\n", meilisearchURL(*liveMeilisearchState))
	return nil
}

func statusRedisRuntimeHook(ctx statusHookContext) error {
	if ctx.Environment.Redis == nil || strings.TrimSpace(ctx.Environment.Redis.Version) == "" {
		_, _ = fmt.Fprintln(ctx.Stdout, "redis-server unset")
		return nil
	}

	liveRedisState, err := loadLiveRedisState(ctx.Store.RootDir, ctx.Environment.Name)
	if err != nil {
		return err
	}
	if liveRedisState == nil {
		_, _ = fmt.Fprintln(ctx.Stdout, "redis-server stopped")
		return nil
	}

	_, _ = fmt.Fprintf(ctx.Stdout, "redis-server running %s\n", redisURL(*liveRedisState))
	return nil
}

func statusTraefikRuntimeHook(ctx statusHookContext) error {
	if ctx.Environment.Traefik == nil || strings.TrimSpace(ctx.Environment.Traefik.Version) == "" {
		_, _ = fmt.Fprintln(ctx.Stdout, "traefik-server unset")
		return nil
	}

	liveTraefikState, err := loadLiveTraefikState(ctx.Store.RootDir, ctx.Environment.Name)
	if err != nil {
		return err
	}
	if liveTraefikState == nil {
		_, _ = fmt.Fprintln(ctx.Stdout, "traefik-server stopped")
		return nil
	}

	_, _ = fmt.Fprintf(ctx.Stdout, "traefik-server running %s\n", traefikURL(*liveTraefikState))
	return nil
}

func statusMailpitRuntimeHook(ctx statusHookContext) error {
	if ctx.Environment.Mailpit == nil || strings.TrimSpace(ctx.Environment.Mailpit.Version) == "" {
		_, _ = fmt.Fprintln(ctx.Stdout, "mailpit-server unset")
		return nil
	}

	liveMailpitState, err := loadLiveMailpitState(ctx.Store.RootDir, ctx.Environment.Name)
	if err != nil {
		return err
	}
	if liveMailpitState == nil {
		_, _ = fmt.Fprintln(ctx.Stdout, "mailpit-server stopped")
		return nil
	}

	_, _ = fmt.Fprintf(ctx.Stdout, "mailpit-server running smtp=%d ui=%s\n", liveMailpitState.SMTPPort, mailpitUIURL(*liveMailpitState))
	return nil
}
