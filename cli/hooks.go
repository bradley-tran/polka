package cli

import (
	"fmt"
	"io"
	"strings"

	"polka/backend"
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
	Store       backend.Store
	Environment backend.Environment
}

type startServiceHook struct {
	id  string
	run func(startHookContext) error
}

type webserverStartHook struct {
	id      string
	matches func(backend.Environment) bool
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
		},
		webservers: []webserverStartHook{
			{id: "nginx", matches: environmentUsesNginx, run: startNginxWebserverHook},
			{id: "php", matches: environmentUsesPHPWebserver, run: startPHPWebserverHook},
		},
		stopHooks: []stopHook{
			{id: "webserver", run: stopWebserverHook},
			{id: "database", run: stopDatabaseServiceHook},
			{id: "mailpit", run: stopMailpitServiceHook},
		},
		configStatusHooks: []statusHook{
			{id: "php", run: statusPHPConfigHook},
			{id: "composer", run: statusComposerConfigHook},
			{id: "nodejs", run: statusNodeJSConfigHook},
			{id: "nginx", run: statusNginxConfigHook},
			{id: "database", run: statusDatabaseConfigHook},
			{id: "mailpit", run: statusMailpitConfigHook},
		},
		runtimeStatusHooks: []statusHook{
			{id: "webserver", run: statusWebserverRuntimeHook},
			{id: "database", run: statusDatabaseRuntimeHook},
			{id: "mailpit", run: statusMailpitRuntimeHook},
		},
	}
}

func (r cliHookRegistry) StartServices(ctx startHookContext) error {
	for _, hook := range r.startServices {
		if err := hook.run(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (r cliHookRegistry) StartWebserver(ctx webserverStartHookContext) (int, error) {
	for _, hook := range r.webservers {
		if hook.matches(ctx.Environment) {
			return hook.run(ctx)
		}
	}

	return 0, fmt.Errorf("environment %q does not define a supported webserver", ctx.Environment.Name)
}

func (r cliHookRegistry) Stop(ctx stopHookContext) error {
	for _, hook := range r.stopHooks {
		if err := hook.run(ctx); err != nil {
			return err
		}
	}

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
	for _, hook := range r.runtimeStatusHooks {
		if err := hook.run(ctx); err != nil {
			return err
		}
	}

	return nil
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

func environmentUsesNginx(environment backend.Environment) bool {
	return strings.TrimSpace(environment.NginxVersion) != ""
}

func environmentUsesPHPWebserver(environment backend.Environment) bool {
	return strings.TrimSpace(environment.NginxVersion) == ""
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
	if strings.TrimSpace(ctx.Environment.PHPVersion) == "" {
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

func finishBackgroundWebserverStart(ctx webserverStartHookContext, startedState serveRuntimeState) (int, error) {
	if strings.TrimSpace(startedState.EnvironmentName) == "" {
		startedState.EnvironmentName = ctx.Environment.Name
	}
	if strings.TrimSpace(startedState.ServerKind) == "" {
		startedState.ServerKind = desiredServeKind(strings.TrimSpace(ctx.Environment.NginxVersion) != "")
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
	databaseState, databaseAlreadyStopped, err := backend.StopManagedDatabase(ctx.Store, resolved, dbRuntimeHooks())
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

func statusPHPConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "php %s\n", labelOrUnset(ctx.Environment.PHPVersion))
	return nil
}

func statusComposerConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "composer %s\n", labelOrUnset(ctx.Environment.ComposerVersion))
	return nil
}

func statusNodeJSConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "nodejs %s\n", labelOrUnset(ctx.Environment.NodeJSVersion))
	return nil
}

func statusNginxConfigHook(ctx statusHookContext) error {
	_, _ = fmt.Fprintf(ctx.Stdout, "nginx %s\n", labelOrUnset(ctx.Environment.NginxVersion))
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

	liveDatabaseState, err := backend.LoadLiveManagedDatabaseState(ctx.Store.RootDir, ctx.Environment.Name, pingDatabaseAddressFunc)
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
