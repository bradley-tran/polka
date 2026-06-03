package tools

func DefaultPlugins() []Plugin {
	return []Plugin{
		phpPlugin(),
		composerPlugin(),
		nodeJSPlugin(),
		nginxPlugin(),
		mailpitPlugin(),
		phpMyAdminPlugin(),
		mysqlPlugin(),
		mariaDBPlugin(),
	}
}
