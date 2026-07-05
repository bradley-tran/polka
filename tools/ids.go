package tools

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	PHP         = "php"
	PHPZTS      = "php-zts"
	FrankenPHP  = "frankenphp"
	Composer    = "composer"
	PIE         = "pie"
	NodeJS      = "nodejs"
	Node        = "node"
	NPM         = "npm"
	NPX         = "npx"
	Yarn        = "yarn"
	Mago        = "mago"
	Nginx       = "nginx"
	Apache      = "apache"
	Mailpit     = "mailpit"
	PHPMyAdmin  = "phpmyadmin"
	Meilisearch = "meilisearch"
	Redis       = "redis"
	Traefik     = "traefik"
	RoadRunner  = "roadrunner"
	MySQL       = "mysql"
	MariaDB     = "mariadb"
	PostgreSQL  = "postgresql"
	PSQL        = "psql"
	SQLite      = "sqlite"
)

var (
	validName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	validVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

func validateVersion(tool, version string) error {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return fmt.Errorf("%s version cannot be empty", tool)
	}
	if !validVersion.MatchString(trimmed) {
		return fmt.Errorf("invalid %s version %q: use letters, numbers, dots, dashes, or underscores", tool, version)
	}

	return nil
}

func validPort(port int) bool {
	return port >= 1 && port <= 65535
}
