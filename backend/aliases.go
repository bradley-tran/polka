package backend

import (
	"polka/config"
	"polka/tools"
)

type Config = config.Config
type DatabaseConfig = config.DatabaseConfig
type Environment = config.Environment
type MailpitConfig = config.MailpitConfig
type ServerConfig = config.ServerConfig

type InstallResult = tools.InstallResult
type ToolDispatchRequest = tools.DispatchRequest
type ToolDownloadContext = tools.DownloadContext
type ToolDownloader = tools.Downloader
type ToolInstallContext = tools.InstallContext
type ToolPlugin = tools.Plugin
type ToolRegistry = tools.Registry

type HTTPToolDownloader = tools.HTTPDownloader

const (
	toolPHP      = tools.PHP
	toolComposer = tools.Composer
	toolNodeJS   = tools.NodeJS
	toolNode     = tools.Node
	toolNPM      = tools.NPM
	toolNPX      = tools.NPX
	toolNginx    = tools.Nginx
	toolMailpit  = tools.Mailpit
	toolMySQL    = tools.MySQL
	toolMariaDB  = tools.MariaDB
)

func NewToolRegistry(plugins ...ToolPlugin) (*ToolRegistry, error) {
	return tools.NewRegistry(plugins...)
}

func NewDefaultToolRegistry() *ToolRegistry {
	return tools.NewDefaultRegistry()
}
