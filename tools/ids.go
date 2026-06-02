package tools

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	PHP        = "php"
	Composer   = "composer"
	NodeJS     = "nodejs"
	Node       = "node"
	NPM        = "npm"
	NPX        = "npx"
	Nginx      = "nginx"
	Mailpit    = "mailpit"
	PHPMyAdmin = "phpmyadmin"
	MySQL      = "mysql"
	MariaDB    = "mariadb"
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
