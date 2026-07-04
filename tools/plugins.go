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
		traefikPlugin(),
		phpMyAdminPlugin(),
		mysqlPlugin(),
		mariaDBPlugin(),
		postgreSQLPlugin(),
		sqlitePlugin(),
	}
}
