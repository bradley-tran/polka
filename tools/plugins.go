package tools

func DefaultPlugins() []ToolPlugin {
	return []ToolPlugin{
		phpPlugin(),
		phpZTSPlugin(),
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
		traefikPlugin(),
		roadRunnerPlugin(),
		phpMyAdminPlugin(),
		mysqlPlugin(),
		mariaDBPlugin(),
		postgreSQLPlugin(),
		sqlitePlugin(),
	}
}
