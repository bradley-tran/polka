package tools

func DefaultPlugins() []ToolPlugin {
	return []ToolPlugin{
		phpPlugin(),
		phpZTSPlugin(),
		phpDevelPlugin(),
		phpSDKPlugin(),
		frankenPHPPlugin(),
		composerPlugin(),
		piePlugin(),
		nodeJSPlugin(),
		magoPlugin(),
		nginxPlugin(),
		apachePlugin(),
		mailpitPlugin(),
		meilisearchPlugin(),
		redisPlugin(),
		erlangPlugin(),
		rabbitMQPlugin(),
		traefikPlugin(),
		roadRunnerPlugin(),
		phpMyAdminPlugin(),
		mysqlPlugin(),
		mariaDBPlugin(),
		postgreSQLPlugin(),
		sqlitePlugin(),
	}
}
