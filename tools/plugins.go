package tools

func DefaultPlugins() []Plugin {
	return []Plugin{
		phpPlugin(),
		composerPlugin(),
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
