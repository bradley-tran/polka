package tools

func DefaultPlugins() []ToolPlugin {
	return []ToolPlugin{
		phpPlugin(),
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
