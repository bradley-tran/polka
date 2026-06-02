package tools

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"polka/config"
)

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

func phpPlugin() Plugin {
	return builtinPlugin{
		id:                PHP,
		version:           func(environment config.Environment) string { return environment.PHPVersion },
		installCandidates: phpInstallCandidates,
		dispatchCommands:  []string{PHP},
		download: func(ctx DownloadContext) error {
			return downloadPHP(ctx.Client, ctx.CacheDir, ctx.Version)
		},
		postInstall: func(ctx InstallContext) error {
			extensions := EffectivePHPExtensionsForInstall(ctx.Environment)
			if len(extensions) == 0 {
				return nil
			}

			return configureInstalledPHPExtensions(ctx.EnvsDir, ctx.Result.Version, extensions)
		},
	}
}

func composerPlugin() Plugin {
	return builtinPlugin{
		id:                Composer,
		version:           func(environment config.Environment) string { return environment.ComposerVersion },
		installCandidates: composerInstallCandidates,
		dispatchCommands:  []string{Composer},
		download: func(ctx DownloadContext) error {
			return downloadComposer(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	}
}

func nodeJSPlugin() Plugin {
	return builtinPlugin{
		id:                NodeJS,
		version:           func(environment config.Environment) string { return environment.NodeJSVersion },
		installCandidates: nodeJSInstallCandidates,
		dispatchCommands:  []string{Node, NPM, NPX},
		cleanupCommands:   []string{Node, NPM, NPX, NodeJS},
		activeCommands: func(environment config.Environment) []string {
			if strings.TrimSpace(environment.NodeJSVersion) == "" {
				return nil
			}

			return []string{Node, NPM, NPX}
		},
		dispatchCandidates: nodeJSDispatchCandidates,
		download: func(ctx DownloadContext) error {
			return downloadNodeJS(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	}
}

func nginxPlugin() Plugin {
	return builtinPlugin{
		id:                Nginx,
		version:           func(environment config.Environment) string { return environment.NginxVersion },
		installCandidates: nginxInstallCandidates,
		dispatchCommands:  []string{Nginx},
		download: func(ctx DownloadContext) error {
			return downloadNginx(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	}
}

func mailpitPlugin() Plugin {
	return builtinPlugin{
		id: Mailpit,
		version: func(environment config.Environment) string {
			if environment.Mailpit == nil {
				return ""
			}

			return environment.Mailpit.Version
		},
		validate:          func(environment config.Environment) error { return validateMailpitConfig(environment.Mailpit) },
		installCandidates: mailpitInstallCandidates,
		dispatchCommands:  []string{Mailpit},
		download: func(ctx DownloadContext) error {
			return downloadMailpit(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	}
}

func phpMyAdminPlugin() Plugin {
	return builtinPlugin{
		id: PHPMyAdmin,
		version: func(environment config.Environment) string {
			if environment.PHPMyAdmin == nil {
				return ""
			}

			return environment.PHPMyAdmin.Version
		},
		validate:          func(environment config.Environment) error { return validatePHPMyAdminConfig(environment.PHPMyAdmin) },
		installCandidates: phpMyAdminInstallCandidates,
		download: func(ctx DownloadContext) error {
			return downloadPHPMyAdmin(ctx.Client, ctx.CacheDir, ctx.Version)
		},
		postInstall: configureInstalledPHPMyAdmin,
	}
}

func mysqlPlugin() Plugin {
	return databasePlugin(MySQL)
}

func mariaDBPlugin() Plugin {
	return databasePlugin(MariaDB)
}

func databasePlugin(tool string) Plugin {
	return builtinPlugin{
		id: tool,
		version: func(environment config.Environment) string {
			if environment.Database != nil && environment.Database.Engine == tool {
				return environment.Database.Version
			}

			return ""
		},
		validate: func(environment config.Environment) error {
			if environment.Database == nil || environment.Database.Engine != tool {
				return nil
			}

			return validateDatabaseConfig(environment.Database)
		},
		installCandidates: func(root, version string) []string {
			return databaseInstallCandidates(tool, filepath.Join(root, tool, version))
		},
		dispatchCommands: []string{tool},
		download: func(ctx DownloadContext) error {
			return downloadDatabaseTool(ctx.Client, ctx.CacheDir, tool, ctx.Version)
		},
	}
}

func validateDatabaseConfig(database *config.DatabaseConfig) error {
	if database == nil {
		return nil
	}

	engine, err := normalizeDatabaseEngine(database.Engine)
	if err != nil {
		return err
	}
	if engine == "" || database.Version == "" {
		return fmt.Errorf("database configuration requires both engine and version")
	}
	if err := validateVersion(engine, database.Version); err != nil {
		return err
	}
	if database.Port != 0 && !validPort(database.Port) {
		return fmt.Errorf("database port must be between 1 and 65535")
	}

	return nil
}

func normalizeDatabaseEngine(engine string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(engine))
	switch trimmed {
	case "":
		return "", nil
	case MySQL, MariaDB:
		return trimmed, nil
	default:
		return "", fmt.Errorf("unsupported database engine %q", engine)
	}
}

func validateMailpitConfig(mailpit *config.MailpitConfig) error {
	if mailpit == nil {
		return nil
	}
	if strings.TrimSpace(mailpit.Version) == "" {
		return fmt.Errorf("mailpit configuration requires version")
	}
	if err := validateVersion(Mailpit, mailpit.Version); err != nil {
		return err
	}
	if mailpit.SMTPPort != 0 && !validPort(mailpit.SMTPPort) {
		return fmt.Errorf("mailpit smtp-port must be between 1 and 65535")
	}
	if mailpit.UIPort != 0 && !validPort(mailpit.UIPort) {
		return fmt.Errorf("mailpit ui-port must be between 1 and 65535")
	}

	return nil
}

func validatePHPMyAdminConfig(phpMyAdmin *config.PHPMyAdminConfig) error {
	if phpMyAdmin == nil {
		return nil
	}
	if strings.TrimSpace(phpMyAdmin.Version) == "" {
		return fmt.Errorf("phpmyadmin configuration requires version")
	}
	if err := validateVersion(PHPMyAdmin, phpMyAdmin.Version); err != nil {
		return err
	}
	if phpMyAdmin.Port != 0 && !validPort(phpMyAdmin.Port) {
		return fmt.Errorf("phpmyadmin port must be between 1 and 65535")
	}

	return nil
}

func phpInstallCandidates(root, version string) []string {
	installDir := filepath.Join(root, PHP, version)
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(installDir, "bin", "php.exe"),
			filepath.Join(installDir, "bin", "php.cmd"),
			filepath.Join(installDir, "bin", "php.bat"),
			filepath.Join(installDir, "php.exe"),
			filepath.Join(installDir, "php.cmd"),
			filepath.Join(installDir, "php.bat"),
		}
	}

	return []string{
		filepath.Join(installDir, "bin", "php"),
		filepath.Join(installDir, "php"),
	}
}

func composerInstallCandidates(root, version string) []string {
	installDir := filepath.Join(root, Composer, version)
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(installDir, "bin", "composer.cmd"),
			filepath.Join(installDir, "bin", "composer.bat"),
			filepath.Join(installDir, "bin", "composer.exe"),
			filepath.Join(installDir, "bin", "composer.phar"),
			filepath.Join(installDir, "composer.cmd"),
			filepath.Join(installDir, "composer.bat"),
			filepath.Join(installDir, "composer.exe"),
			filepath.Join(installDir, "composer.phar"),
		}
	}

	return []string{
		filepath.Join(installDir, "bin", "composer"),
		filepath.Join(installDir, "bin", "composer.phar"),
		filepath.Join(installDir, "composer"),
		filepath.Join(installDir, "composer.phar"),
	}
}

func nodeJSInstallCandidates(root, version string) []string {
	installDir := filepath.Join(root, NodeJS, version)
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(installDir, "node.exe"),
			filepath.Join(installDir, "bin", "node.exe"),
		}
	}

	return []string{
		filepath.Join(installDir, "bin", "node"),
		filepath.Join(installDir, "node"),
	}
}

func nginxInstallCandidates(root, version string) []string {
	installDir := filepath.Join(root, Nginx, version)
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(installDir, "nginx.exe"),
			filepath.Join(installDir, "sbin", "nginx.exe"),
		}
	}

	return []string{
		filepath.Join(installDir, "sbin", "nginx"),
		filepath.Join(installDir, "nginx"),
	}
}

func mailpitInstallCandidates(root, version string) []string {
	installDir := filepath.Join(root, Mailpit, version)
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(installDir, "mailpit.exe"),
			filepath.Join(installDir, "bin", "mailpit.exe"),
		}
	}

	return []string{
		filepath.Join(installDir, "mailpit"),
		filepath.Join(installDir, "bin", "mailpit"),
	}
}

func phpMyAdminInstallCandidates(root, version string) []string {
	installDir := filepath.Join(root, PHPMyAdmin, version)
	return []string{
		filepath.Join(installDir, "index.php"),
	}
}

func databaseInstallCandidates(tool, installDir string) []string {
	switch tool {
	case MySQL:
		if runtime.GOOS == "windows" {
			return []string{
				filepath.Join(installDir, "bin", "mysql.cmd"),
				filepath.Join(installDir, "bin", "mysql.bat"),
				filepath.Join(installDir, "bin", "mysql.exe"),
				filepath.Join(installDir, "mysql.cmd"),
				filepath.Join(installDir, "mysql.bat"),
				filepath.Join(installDir, "mysql.exe"),
			}
		}

		return []string{
			filepath.Join(installDir, "bin", "mysql"),
			filepath.Join(installDir, "mysql"),
		}
	case MariaDB:
		if runtime.GOOS == "windows" {
			return []string{
				filepath.Join(installDir, "bin", "mariadb.cmd"),
				filepath.Join(installDir, "bin", "mariadb.bat"),
				filepath.Join(installDir, "bin", "mariadb.exe"),
				filepath.Join(installDir, "bin", "mysql.cmd"),
				filepath.Join(installDir, "bin", "mysql.bat"),
				filepath.Join(installDir, "bin", "mysql.exe"),
				filepath.Join(installDir, "mariadb.cmd"),
				filepath.Join(installDir, "mariadb.bat"),
				filepath.Join(installDir, "mariadb.exe"),
				filepath.Join(installDir, "mysql.cmd"),
				filepath.Join(installDir, "mysql.bat"),
				filepath.Join(installDir, "mysql.exe"),
			}
		}

		return []string{
			filepath.Join(installDir, "bin", "mariadb"),
			filepath.Join(installDir, "bin", "mysql"),
			filepath.Join(installDir, "mariadb"),
			filepath.Join(installDir, "mysql"),
		}
	default:
		return nil
	}
}

func nodeJSDispatchCandidates(root, executable, version string) []string {
	installDir := filepath.Join(root, NodeJS, version)
	switch executable {
	case Node:
		if runtime.GOOS == "windows" {
			return []string{
				filepath.Join(installDir, "node.cmd"),
				filepath.Join(installDir, "node.exe"),
				filepath.Join(installDir, "bin", "node.cmd"),
				filepath.Join(installDir, "bin", "node.exe"),
			}
		}

		return []string{
			filepath.Join(installDir, "bin", "node"),
			filepath.Join(installDir, "node"),
		}
	case NPM, NPX:
		if runtime.GOOS == "windows" {
			return []string{
				filepath.Join(installDir, executable+".cmd"),
				filepath.Join(installDir, executable),
				filepath.Join(installDir, "bin", executable+".cmd"),
				filepath.Join(installDir, "bin", executable),
			}
		}

		return []string{
			filepath.Join(installDir, "bin", executable),
			filepath.Join(installDir, executable),
		}
	default:
		return nil
	}
}
