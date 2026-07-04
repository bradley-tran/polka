package service

import "strings"

type Manager struct{}

func DefaultManager() Manager {
	return Manager{}
}

type RuntimeHooks struct {
	Database                DatabaseRuntimeHooks
	Mailpit                 MailpitRuntimeHooks
	Meilisearch             MeilisearchRuntimeHooks
	Traefik                 TraefikRuntimeHooks
	PHPMyAdmin              PHPMyAdminRuntimeHooks
	EnsurePHPMyAdminStorage func(Context, Environment, DatabaseRuntimeHooks) error
}

type StartResult struct {
	Meilisearch *MeilisearchStartSummary
	Traefik     *TraefikStartSummary
	PHPMyAdmin  *PHPMyAdminStartResult
}

type MeilisearchStartSummary struct {
	State          MeilisearchRuntimeState
	AlreadyStarted bool
}

type TraefikStartSummary struct {
	State          TraefikRuntimeState
	AlreadyStarted bool
}

type PHPMyAdminStartResult struct {
	State          ServeRuntimeState
	AlreadyStarted bool
}

type StopResult struct {
	PHPMyAdmin  *StopServeResult
	Meilisearch *StopMeilisearchResult
	Traefik     *StopTraefikResult
	Database    *StopDatabaseResult
	Mailpit     *StopMailpitResult
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

type StopMeilisearchResult struct {
	State          MeilisearchRuntimeState
	AlreadyStopped bool
}

type StopTraefikResult struct {
	State          TraefikRuntimeState
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
	if ctx.Environment.Meilisearch != nil && strings.TrimSpace(ctx.Environment.Meilisearch.Version) != "" {
		if !ctx.skipMissingTool("meilisearch", toolMeilisearch) {
			state, alreadyStarted, err := EnsureManagedMeilisearchStarted(ctx, hooks.Meilisearch)
			if err != nil {
				return StartResult{}, err
			}
			result.Meilisearch = &MeilisearchStartSummary{State: state, AlreadyStarted: alreadyStarted}
		}
	}

	if ctx.Environment.Traefik != nil && strings.TrimSpace(ctx.Environment.Traefik.Version) != "" {
		if !ctx.skipMissingTool("traefik", toolTraefik) {
			state, alreadyStarted, err := EnsureManagedTraefikStarted(ctx, hooks.Traefik)
			if err != nil {
				return StartResult{}, err
			}
			result.Traefik = &TraefikStartSummary{State: state, AlreadyStarted: alreadyStarted}
		}
	}

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

	meilisearchState, meilisearchAlreadyStopped, err := StopManagedMeilisearch(ctx, hooks.Meilisearch)
	if err != nil {
		return StopResult{}, err
	}
	result.Meilisearch = &StopMeilisearchResult{State: meilisearchState, AlreadyStopped: meilisearchAlreadyStopped}

	traefikState, traefikAlreadyStopped, err := StopManagedTraefik(ctx, hooks.Traefik)
	if err != nil {
		return StopResult{}, err
	}
	result.Traefik = &StopTraefikResult{State: traefikState, AlreadyStopped: traefikAlreadyStopped}

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
	warnPHPMyAdminPostgreSQL(ctx, ctx.Environment)
	if ctx.Environment.PHPMyAdmin != nil && strings.TrimSpace(ctx.Environment.PHPMyAdmin.Version) != "" {
		_ = ctx.skipMissingTool("phpmyadmin", toolPHPMyAdmin)
	}
	if ctx.Environment.Database != nil && strings.TrimSpace(ctx.Environment.Database.Engine) != "" {
		_ = ctx.skipMissingTool("database", ctx.Environment.Database.Engine)
	}
	if ctx.Environment.Mailpit != nil && strings.TrimSpace(ctx.Environment.Mailpit.Version) != "" {
		_ = ctx.skipMissingTool("mailpit", toolMailpit)
	}
	if ctx.Environment.Meilisearch != nil && strings.TrimSpace(ctx.Environment.Meilisearch.Version) != "" {
		_ = ctx.skipMissingTool("meilisearch", toolMeilisearch)
	}
	if ctx.Environment.Traefik != nil && strings.TrimSpace(ctx.Environment.Traefik.Version) != "" {
		_ = ctx.skipMissingTool("traefik", toolTraefik)
	}
}
