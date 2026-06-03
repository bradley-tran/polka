package tools

import "net/http"

func mariaDBPlugin() Plugin {
	return databasePlugin(MariaDB)
}

func downloadMariaDB(client *http.Client, cacheDir, version string) error {
	return downloadDatabaseTool(client, cacheDir, MariaDB, version)
}
