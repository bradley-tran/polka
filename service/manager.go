package service

import "strings"

type Manager struct{}

func DefaultManager() Manager {
	return Manager{}
}

type RuntimeHooks struct {
	Database                DatabaseRuntimeHooks
	Mailpit                 MailpitRuntimeHooks
	PHPMyAdmin              PHPMyAdminRuntimeHooks
	EnsurePHPMyAdminStorage func(Context, Environment, DatabaseRuntimeHooks) error
}

type StartResult struct {
	PHPMyAdmin *PHPMyAdminStartResult
}

type PHPMyAdminStartResult struct {
	State          ServeRuntimeState
	AlreadyStarted bool
}

type StopResult struct {
	PHPMyAdmin *StopServeResult
	Database   *StopDatabaseResult
	Mailpit    *StopMailpitResult
}

type StopServeResult struct {
	State          ServeRuntimeState
	AlreadyStopped bool
}

type StopDatabaseResult struct {
	State          ManagedDatabaseRuntimeState
	AlreadyStopped bool
}

type StopMailpitResult struct {
	State          MailpitRuntimeState
	AlreadyStopped bool
}

func (m Manager) Start(ctx Context, hooks RuntimeHooks) (StartResult, error) {
	if ctx.Environment.Database != nil && strings.TrimSpace(ctx.Environment.Database.Engine) != "" {
		if !ctx.skipMissingTool("database", ctx.Environment.Database.Engine) {
			resolved := ResolvedDatabaseEnvironment{Environment: ctx.Environment, Database: ctx.Environment.Database}
			if _, _, err := EnsureManagedDatabaseStarted(ctx, resolved, hooks.Database); err != nil {
				return StartResult{}, err
			}
		}
	}

	if ctx.Environment.Mailpit != nil && strings.TrimSpace(ctx.Environment.Mailpit.Version) != "" {
		if !ctx.skipMissingTool("mailpit", toolMailpit) {
			if _, _, err := EnsureManagedMailpitStarted(ctx, hooks.Mailpit); err != nil {
				return StartResult{}, err
			}
		}
	}

	result := StartResult{}
	if ctx.Environment.PHPMyAdmin != nil && strings.TrimSpace(ctx.Environment.PHPMyAdmin.Version) != "" {
		if ctx.skipMissingTool("phpmyadmin", toolPHPMyAdmin) {
			return result, nil
		}
		if ctx.Environment.Database == nil || strings.TrimSpace(ctx.Environment.Database.Engine) == "" || ctx.hasTool(ctx.Environment.Database.Engine) {
			ensureStorage := hooks.EnsurePHPMyAdminStorage
			if ensureStorage == nil {
				ensureStorage = EnsurePHPMyAdminStorageConfigured
			}
			if err := ensureStorage(ctx, ctx.Environment, hooks.Database); err != nil {
				return StartResult{}, err
			}
		}
		state, alreadyStarted, err := EnsureManagedPHPMyAdminStarted(ctx, hooks.PHPMyAdmin)
		if err != nil {
			return StartResult{}, err
		}
		result.PHPMyAdmin = &PHPMyAdminStartResult{State: state, AlreadyStarted: alreadyStarted}
	}

	return result, nil
}

func (m Manager) Stop(ctx Context, hooks RuntimeHooks) (StopResult, error) {
	result := StopResult{}

	phpMyAdminState, phpMyAdminAlreadyStopped, err := StopManagedPHPMyAdmin(ctx, hooks.PHPMyAdmin)
	if err != nil {
		return StopResult{}, err
	}
	result.PHPMyAdmin = &StopServeResult{State: phpMyAdminState, AlreadyStopped: phpMyAdminAlreadyStopped}

	databaseState, databaseAlreadyStopped, err := StopManagedDatabaseForEnvironment(ctx, hooks.Database)
	if err != nil {
		return StopResult{}, err
	}
	result.Database = &StopDatabaseResult{State: databaseState, AlreadyStopped: databaseAlreadyStopped}

	mailpitState, mailpitAlreadyStopped, err := StopManagedMailpit(ctx, hooks.Mailpit)
	if err != nil {
		return StopResult{}, err
	}
	result.Mailpit = &StopMailpitResult{State: mailpitState, AlreadyStopped: mailpitAlreadyStopped}

	return result, nil
}

func (m Manager) WarnMissingRuntimeTools(ctx Context) {
	if ctx.Environment.PHPMyAdmin != nil && strings.TrimSpace(ctx.Environment.PHPMyAdmin.Version) != "" {
		_ = ctx.skipMissingTool("phpmyadmin", toolPHPMyAdmin)
	}
	if ctx.Environment.Database != nil && strings.TrimSpace(ctx.Environment.Database.Engine) != "" {
		_ = ctx.skipMissingTool("database", ctx.Environment.Database.Engine)
	}
	if ctx.Environment.Mailpit != nil && strings.TrimSpace(ctx.Environment.Mailpit.Version) != "" {
		_ = ctx.skipMissingTool("mailpit", toolMailpit)
	}
}
