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
		mailpitPlugin(),
		phpMyAdminPlugin(),
		mysqlPlugin(),
		mariaDBPlugin(),
		sqlitePlugin(),
	}
}
