package tools

import "net/http"

func mysqlPlugin() Plugin {
	return databasePlugin(MySQL)
}

func downloadMySQL(client *http.Client, cacheDir, version string) error {
	return downloadDatabaseTool(client, cacheDir, MySQL, version)
}
