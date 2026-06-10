package tools

func DefaultPlugins() []ToolPlugin {
	return []ToolPlugin{
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
