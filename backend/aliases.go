package backend

import (
	"polka/config"
	"polka/plugins"
	"polka/tools"
)

type Config = config.Config
type DatabaseConfig = config.DatabaseConfig
type Environment = config.Environment
type MailpitConfig = config.MailpitConfig
type PHPMyAdminConfig = config.PHPMyAdminConfig
type ServerConfig = config.ServerConfig

type InstallResult = tools.InstallResult
type ToolLogEntry = tools.LogEntry
type ToolDispatchRequest = tools.DispatchRequest
type ToolDownloadContext = tools.DownloadContext
type ToolDownloader = tools.Downloader
type ToolInstallContext = tools.InstallContext
type ToolPlugin = tools.ToolPlugin
type ToolRegistry = tools.Registry
type FrameworkPlugin = plugins.FrameworkPlugin
type PluginRegistry = plugins.Registry

type HTTPToolDownloader = tools.HTTPDownloader

const (
	toolPHP        = tools.PHP
	toolPHPZTS     = tools.PHPZTS
	toolFrankenPHP = tools.FrankenPHP
	toolComposer   = tools.Composer
	toolPIE        = tools.PIE
	toolNodeJS     = tools.NodeJS
	toolNode       = tools.Node
	toolNPM        = tools.NPM
	toolNPX        = tools.NPX
	toolMago       = tools.Mago
	toolNginx      = tools.Nginx
	toolApache     = tools.Apache
	toolMailpit    = tools.Mailpit
	toolPHPMyAdmin = tools.PHPMyAdmin
	toolMySQL      = tools.MySQL
	toolMariaDB    = tools.MariaDB
	toolPostgreSQL = tools.PostgreSQL
	toolSQLite     = tools.SQLite
)

// PrimaryPHPTool returns the configured primary PHP tool and version.
func PrimaryPHPTool(environment Environment) (string, string) {
	return config.PrimaryPHPTool(environment)
}

// PrimaryPHPVersion returns the configured primary PHP runtime version.
func PrimaryPHPVersion(environment Environment) string {
	return config.PrimaryPHPVersion(environment)
}

// PHPCLIProvider returns the configured provider for the php command.
func PHPCLIProvider(environment Environment) (string, string) {
	return config.PHPCLIProvider(environment)
}

// HasPHPCLI reports whether the environment has a standalone or bundled PHP CLI.
func HasPHPCLI(environment Environment) bool {
	return config.HasPHPCLI(environment)
}

func NewToolRegistry(plugins ...ToolPlugin) (*ToolRegistry, error) {
	return tools.NewRegistry(plugins...)
}

func NewDefaultToolRegistry() *ToolRegistry {
	return tools.NewDefaultRegistry()
}

func NewDefaultPluginRegistry() *PluginRegistry {
	return plugins.NewDefaultRegistry()
}
