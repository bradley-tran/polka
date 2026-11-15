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
	Redis                   RedisRuntimeHooks
	RabbitMQ                RabbitMQRuntimeHooks
	PHPMyAdmin              PHPMyAdminRuntimeHooks
	Workers                 WorkersRuntimeHooks
	Cron                    CronRuntimeHooks
	EnsurePHPMyAdminStorage func(Context, Environment, DatabaseRuntimeHooks) error
}

type StartResult struct {
	Meilisearch *MeilisearchStartSummary
	Redis       *RedisStartSummary
	RabbitMQ    *RabbitMQStartSummary
	PHPMyAdmin  *PHPMyAdminStartResult
	Workers     *WorkersStartSummary
	Cron        *CronStartSummary
}

type MeilisearchStartSummary struct {
	State          MeilisearchRuntimeState
	AlreadyStarted bool
}

type RedisStartSummary struct {
	State          RedisRuntimeState
	AlreadyStarted bool
}

type RabbitMQStartSummary struct {
	State          RabbitMQRuntimeState
	AlreadyStarted bool
}

type PHPMyAdminStartResult struct {
	State          ServeRuntimeState
	AlreadyStarted bool
}

type WorkersStartSummary struct {
	State          WorkersRuntimeState
	AlreadyStarted bool
}

// CronStartSummary reports the cron runner started (or found running) by serve.
type CronStartSummary struct {
	State          CronRunnerState
	AlreadyStarted bool
}

type StopResult struct {
	Cron        *StopCronResult
	Workers     *StopWorkersResult
	PHPMyAdmin  *StopServeResult
	Meilisearch *StopMeilisearchResult
	Redis       *StopRedisResult
	RabbitMQ    *StopRabbitMQResult
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

type StopRedisResult struct {
	State          RedisRuntimeState
	AlreadyStopped bool
}

type StopRabbitMQResult struct {
	State          RabbitMQRuntimeState
	AlreadyStopped bool
}

type StopWorkersResult struct {
	State          WorkersRuntimeState
	AlreadyStopped bool
}

// StopCronResult reports the cron runner stopped by stop.
type StopCronResult struct {
	State          CronRunnerState
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

	if ctx.Environment.Mailpit() != nil && strings.TrimSpace(ctx.Environment.Mailpit().Version) != "" {
		if !ctx.skipMissingTool("mailpit", toolMailpit) {
			if _, _, err := EnsureManagedMailpitStarted(ctx, hooks.Mailpit); err != nil {
				return StartResult{}, err
			}
		}
	}

	result := StartResult{}
	if ctx.Environment.Meilisearch() != nil && strings.TrimSpace(ctx.Environment.Meilisearch().Version) != "" {
		if !ctx.skipMissingTool("meilisearch", toolMeilisearch) {
			state, alreadyStarted, err := EnsureManagedMeilisearchStarted(ctx, hooks.Meilisearch)
			if err != nil {
				return StartResult{}, err
			}
			result.Meilisearch = &MeilisearchStartSummary{State: state, AlreadyStarted: alreadyStarted}
		}
	}

	if ctx.Environment.Redis() != nil && strings.TrimSpace(ctx.Environment.Redis().Version) != "" {
		if !ctx.skipMissingTool("redis", toolRedis) {
			state, alreadyStarted, err := EnsureManagedRedisStarted(ctx, hooks.Redis)
			if err != nil {
				return StartResult{}, err
			}
			result.Redis = &RedisStartSummary{State: state, AlreadyStarted: alreadyStarted}
		}
	}

	if ctx.Environment.RabbitMQ() != nil && strings.TrimSpace(ctx.Environment.RabbitMQ().Version) != "" {
		if !ctx.skipMissingTool("rabbitmq", toolRabbitMQ) {
			state, alreadyStarted, err := EnsureManagedRabbitMQStarted(ctx, hooks.RabbitMQ)
			if err != nil {
				return StartResult{}, err
			}
			result.RabbitMQ = &RabbitMQStartSummary{State: state, AlreadyStarted: alreadyStarted}
		}
	}

	if ctx.Environment.PHPMyAdmin() != nil && strings.TrimSpace(ctx.Environment.PHPMyAdmin().Version) != "" {
		if !ctx.skipMissingTool("phpmyadmin", toolPHPMyAdmin) {
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
	}

	// Workers start last so managed services (database, redis, ...) are
	// already available to queue consumers and schedulers. Worker failures are
	// warnings only: they must not abort serve or the other services.
	if len(ctx.Environment.Workers) > 0 {
		state, alreadyStarted, err := EnsureManagedWorkersStarted(ctx, hooks.Workers)
		if err != nil {
			ctx.warnf("Skipping workers for environment %q: %v.\n", ctx.Environment.Name, err)
		} else {
			result.Workers = &WorkersStartSummary{State: state, AlreadyStarted: alreadyStarted}
		}
	}

	// The cron runner starts after workers, so scheduled jobs that enqueue
	// work find their consumers running. Like workers, a runner that fails to
	// start is a warning only.
	if len(ctx.Environment.Cron) > 0 {
		state, alreadyStarted, err := EnsureManagedCronStarted(ctx, hooks.Cron)
		if err != nil {
			ctx.warnf("Skipping the cron runner for environment %q: %v.\n", ctx.Environment.Name, err)
		} else {
			result.Cron = &CronStartSummary{State: state, AlreadyStarted: alreadyStarted}
		}
	} else if _, _, err := StopManagedCron(ctx, hooks.Cron); err != nil {
		// The cron config was removed since the last serve: stop the runner
		// left from then so its old jobs stop firing.
		ctx.warnf("Failed to stop the cron runner for environment %q: %v.\n", ctx.Environment.Name, err)
	}

	return result, nil
}

func (m Manager) Stop(ctx Context, hooks RuntimeHooks) (StopResult, error) {
	result := StopResult{}

	// The cron runner stops before workers, so no scheduled job enqueues new
	// work while consumers shut down. Failures are warnings only.
	cronState, cronAlreadyStopped, err := StopManagedCron(ctx, hooks.Cron)
	if err != nil {
		ctx.warnf("Failed to stop the cron runner for environment %q: %v.\n", ctx.Environment.Name, err)
	} else {
		result.Cron = &StopCronResult{State: cronState, AlreadyStopped: cronAlreadyStopped}
	}

	// Workers stop next so queue consumers wind down before the services
	// they depend on (database, redis, ...) go away. Worker failures are
	// warnings only: they must not block stopping the other services.
	workersState, workersAlreadyStopped, err := StopManagedWorkers(ctx, hooks.Workers)
	if err != nil {
		ctx.warnf("Failed to stop workers for environment %q: %v.\n", ctx.Environment.Name, err)
	} else {
		result.Workers = &StopWorkersResult{State: workersState, AlreadyStopped: workersAlreadyStopped}
	}

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

	redisState, redisAlreadyStopped, err := StopManagedRedis(ctx, hooks.Redis)
	if err != nil {
		return StopResult{}, err
	}
	result.Redis = &StopRedisResult{State: redisState, AlreadyStopped: redisAlreadyStopped}

	rabbitMQState, rabbitMQAlreadyStopped, err := StopManagedRabbitMQ(ctx, hooks.RabbitMQ)
	if err != nil {
		return StopResult{}, err
	}
	result.RabbitMQ = &StopRabbitMQResult{State: rabbitMQState, AlreadyStopped: rabbitMQAlreadyStopped}

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
	if ctx.Environment.PHPMyAdmin() != nil && strings.TrimSpace(ctx.Environment.PHPMyAdmin().Version) != "" {
		_ = ctx.skipMissingTool("phpmyadmin", toolPHPMyAdmin)
	}
	if ctx.Environment.Database != nil && strings.TrimSpace(ctx.Environment.Database.Engine) != "" {
		_ = ctx.skipMissingTool("database", ctx.Environment.Database.Engine)
	}
	if ctx.Environment.Mailpit() != nil && strings.TrimSpace(ctx.Environment.Mailpit().Version) != "" {
		_ = ctx.skipMissingTool("mailpit", toolMailpit)
	}
	if ctx.Environment.Meilisearch() != nil && strings.TrimSpace(ctx.Environment.Meilisearch().Version) != "" {
		_ = ctx.skipMissingTool("meilisearch", toolMeilisearch)
	}
	if ctx.Environment.Redis() != nil && strings.TrimSpace(ctx.Environment.Redis().Version) != "" {
		_ = ctx.skipMissingTool("redis", toolRedis)
	}
	if ctx.Environment.RabbitMQ() != nil && strings.TrimSpace(ctx.Environment.RabbitMQ().Version) != "" {
		_ = ctx.skipMissingTool("rabbitmq", toolRabbitMQ)
	}
}
