package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"polka/config"
	"polka/tools"
)

type Environment = config.Environment
type DatabaseConfig = config.DatabaseConfig
type MailpitConfig = config.MailpitConfig
type PHPMyAdminConfig = config.PHPMyAdminConfig
type MeilisearchConfig = config.MeilisearchConfig

const (
	toolPHP         = tools.PHP
	toolNginx       = tools.Nginx
	toolMailpit     = tools.Mailpit
	toolPHPMyAdmin  = tools.PHPMyAdmin
	toolMeilisearch = tools.Meilisearch
	toolMySQL       = tools.MySQL
	toolMariaDB     = tools.MariaDB
	toolPostgreSQL  = tools.PostgreSQL
)

// Context carries the project-local paths and adapters needed by managed
// service runtimes without depending on backend.Store.
type Context struct {
	ProjectDir  string
	RootDir     string
	EnvsDir     string
	BinDir      string
	CacheDir    string
	Environment config.Environment
	Registry    *tools.Registry
	RuntimeEnv  func() ([]string, error)
	TLSCert     func(host string) (string, string, error)
	Warnf       func(format string, args ...any)
}

func (ctx Context) registry() *tools.Registry {
	if ctx.Registry != nil {
		return ctx.Registry
	}

	return tools.NewDefaultRegistry()
}

func (ctx Context) warnf(format string, args ...any) {
	if ctx.Warnf != nil {
		ctx.Warnf(format, args...)
	}
}

func (ctx Context) runtimeEnv() ([]string, error) {
	if ctx.RuntimeEnv == nil {
		return os.Environ(), nil
	}

	return ctx.RuntimeEnv()
}

func (ctx Context) tlsCertificate(host string) (string, string, error) {
	if ctx.TLSCert == nil {
		return "", "", fmt.Errorf("tls certificate resolver is not configured")
	}

	return ctx.TLSCert(host)
}

func (ctx Context) hasTool(tool string) bool {
	_, ok := ctx.registry().Plugin(tool)
	return ok
}

func (ctx Context) skipMissingTool(serviceName, tool string) bool {
	if ctx.hasTool(tool) {
		return false
	}

	ctx.warnf("Skipping %s for environment %q: matching tool %q is not registered.\n", serviceName, ctx.Environment.Name, tool)
	return true
}

func (ctx Context) resolveInstalledTool(tool, version string) (string, error) {
	return ResolveInstalledTool(ctx.registry(), ctx.EnvsDir, tool, version)
}

// ResolveInstalledTool returns the installed executable for a manifest tool.
func ResolveInstalledTool(registry *tools.Registry, root, tool, version string) (string, error) {
	if registry == nil {
		registry = tools.NewDefaultRegistry()
	}

	candidates := registry.InstallCandidates(root, tool, version)
	if len(candidates) == 0 {
		return "", fmt.Errorf("unsupported tool %q", tool)
	}

	for _, candidate := range candidates {
		fileInfo, err := os.Stat(candidate)
		if err == nil {
			if fileInfo.IsDir() {
				continue
			}

			return candidate, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat %s: %w", candidate, err)
		}
	}

	return "", fmt.Errorf("%s version %q is not installed under %s", tool, version, filepath.Join(root, tool, version))
}

// ToolLogRoot returns the predictable log root for one managed tool in one environment.
func ToolLogRoot(rootDir, tool, environmentName string) string {
	name := strings.TrimSpace(environmentName)
	if name == "" {
		name = "current"
	}

	return filepath.Join(rootDir, "run", strings.ToLower(strings.TrimSpace(tool)), name)
}
