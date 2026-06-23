package plugins

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"polka/config"
	"polka/tools"
)

const (
	CakePHP     = "cakephp"
	CodeIgniter = "codeigniter"
	Drupal      = "drupal"
	WordPress   = "wordpress"
	Laravel     = "laravel"
	Symfony     = "symfony"
)

const (
	defaultMariaDBVersion = "11.8"
	defaultDatabasePort   = 3306
	defaultPostgreSQLPort = 5432
	defaultDatabaseHost   = "127.0.0.1"
)

// DatabaseCredentials is the framework-safe shape of generated database credentials.
type DatabaseCredentials struct {
	Engine       string
	Version      string
	Host         string
	Port         int
	DatabaseName string
	User         string
	Password     string
}

// RuntimeEnvContext is passed to framework plugins while composing process environment variables.
type RuntimeEnvContext struct {
	Environment config.Environment
	Database    *DatabaseCredentials
}

// PostComposerContext is passed to framework plugins after selected Composer workflows finish.
type PostComposerContext struct {
	Environment config.Environment
	ProjectDir  string
	WorkingDir  string
	Args        []string
	Database    *DatabaseCredentials
}

// NginxConfigContext is passed to framework plugins before Polka writes its generated nginx config.
type NginxConfigContext struct {
	Environment           config.Environment
	Host                  string
	Port                  int
	Docroot               string
	IndexNames            string
	TryFilesFallback      string
	FastCGIIndex          string
	BackendAddress        string
	TLSEnabled            bool
	TLSCertificatePath    string
	TLSCertificateKeyPath string
}

// NginxConfigResult lets a framework plugin replace Polka's generic generated nginx config.
type NginxConfigResult struct {
	Config []byte
}

// FrameworkPlugin describes a higher-level PHP framework integration.
type FrameworkPlugin interface {
	ID() string
	Defaults() config.Environment
	PHPExtensions() map[string]bool
	ValidateEnvironment(config.Environment) error
	OPcacheConfig() map[string]string
	RuntimeEnv(RuntimeEnvContext) map[string]string
	PostComposer(PostComposerContext) error
	NginxConfig(NginxConfigContext) (NginxConfigResult, bool, error)
}

type builtinFrameworkPlugin struct {
	id              string
	defaults        config.Environment
	phpExtensions   map[string]bool
	databaseEngines map[string]bool
	opcacheConfig   map[string]string
	runtimeDatabase frameworkRuntimeDatabaseManifest
	postComposer    frameworkPostComposerManifest
	nginx           func(NginxConfigContext) (NginxConfigResult, bool, error)
}

// DefaultFrameworkPlugins returns the built-in framework plugins.
func DefaultFrameworkPlugins() []FrameworkPlugin {
	return []FrameworkPlugin{
		newManifestFrameworkPlugin(CakePHP),
		newManifestFrameworkPlugin(CodeIgniter),
		newManifestFrameworkPlugin(Drupal),
		newManifestFrameworkPlugin(WordPress),
		newManifestFrameworkPlugin(Laravel),
		newManifestFrameworkPlugin(Symfony),
	}
}

func (p builtinFrameworkPlugin) ID() string {
	return p.id
}

func (p builtinFrameworkPlugin) Defaults() config.Environment {
	if strings.TrimSpace(p.defaults.Framework) == "" {
		return config.Environment{Framework: strings.ToLower(strings.TrimSpace(p.id))}
	}

	return config.NormalizeEnvironment("", p.defaults)
}

func (p builtinFrameworkPlugin) PHPExtensions() map[string]bool {
	return copyBoolMap(p.phpExtensions)
}

// ValidateEnvironment rejects database engines unsupported by this framework.
func (p builtinFrameworkPlugin) ValidateEnvironment(environment config.Environment) error {
	if environment.Database == nil || len(p.databaseEngines) == 0 {
		return nil
	}
	engine := strings.ToLower(strings.TrimSpace(environment.Database.Engine))
	if engine == "" || p.databaseEngines[engine] {
		return nil
	}

	return fmt.Errorf("framework %q does not support database engine %q", p.id, engine)
}

func (p builtinFrameworkPlugin) OPcacheConfig() map[string]string {
	return copyStringMap(p.opcacheConfig)
}

func (p builtinFrameworkPlugin) RuntimeEnv(ctx RuntimeEnvContext) map[string]string {
	return frameworkManifestRuntimeEnv(ctx, p.runtimeDatabase)
}

func (p builtinFrameworkPlugin) PostComposer(ctx PostComposerContext) error {
	return frameworkManifestPostComposer(ctx, p)
}

func (p builtinFrameworkPlugin) NginxConfig(ctx NginxConfigContext) (NginxConfigResult, bool, error) {
	if p.nginx == nil {
		return NginxConfigResult{}, false, nil
	}

	return p.nginx(ctx)
}

func phpExtensionMap(names ...string) map[string]bool {
	extensions := make(map[string]bool, len(names))
	for _, name := range names {
		normalized := strings.ToLower(strings.TrimSpace(name))
		if normalized != "" {
			extensions[normalized] = true
		}
	}

	return extensions
}

func copyBoolMap(values map[string]bool) map[string]bool {
	if len(values) == 0 {
		return nil
	}

	copied := make(map[string]bool, len(values))
	for key, value := range values {
		copied[key] = value
	}

	return copied
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}

	return copied
}

func frameworkDatabaseRuntimeEnv(ctx RuntimeEnvContext, includeLaravelConnection bool, includeSymfonyDatabaseURL bool) map[string]string {
	if ctx.Database == nil {
		return nil
	}

	host := strings.TrimSpace(ctx.Database.Host)
	if host == "" {
		host = defaultDatabaseHost
	}
	port := ctx.Database.Port
	if port == 0 {
		port = frameworkDefaultDatabasePort(ctx.Database.Engine)
	}

	values := map[string]string{
		"DB_HOST":     host,
		"DB_PORT":     strconv.Itoa(port),
		"DB_DATABASE": ctx.Database.DatabaseName,
		"DB_USERNAME": ctx.Database.User,
		"DB_PASSWORD": ctx.Database.Password,
	}
	if includeLaravelConnection {
		if isPostgreSQL(ctx.Database.Engine) {
			values["DB_CONNECTION"] = "pgsql"
		} else {
			values["DB_CONNECTION"] = "mysql"
		}
	}
	if includeSymfonyDatabaseURL {
		values["DATABASE_URL"] = symfonyDatabaseURL(ctx, host, port)
	}

	return values
}

// codeIgniterDatabaseRuntimeEnv renders managed database credentials in CodeIgniter's config key form.
func codeIgniterDatabaseRuntimeEnv(ctx RuntimeEnvContext) map[string]string {
	credentials := normalizeDatabaseCredentials(ctx.Database)
	if credentials == nil {
		return nil
	}

	driver := "MySQLi"
	if isPostgreSQL(credentials.Engine) {
		driver = "Postgre"
	}

	return map[string]string{
		"database.default.hostname": credentials.Host,
		"database.default.port":     strconv.Itoa(credentials.Port),
		"database.default.database": credentials.DatabaseName,
		"database.default.username": credentials.User,
		"database.default.password": credentials.Password,
		"database.default.DBDriver": driver,
	}
}

// symfonyDatabaseURL renders managed database credentials in Symfony's Doctrine URL form.
func symfonyDatabaseURL(ctx RuntimeEnvContext, host string, port int) string {
	user := url.UserPassword(ctx.Database.User, ctx.Database.Password).String()
	databaseName := strings.TrimPrefix(url.PathEscape(ctx.Database.DatabaseName), "/")
	serverVersion := symfonyDatabaseServerVersion(ctx.Environment)
	scheme := "mysql"
	charset := "utf8mb4"
	if isPostgreSQL(ctx.Database.Engine) {
		scheme = "postgresql"
		charset = "utf8"
	}
	values := url.Values{"charset": []string{charset}}
	if serverVersion != "" {
		values.Set("serverVersion", serverVersion)
	}

	return scheme + "://" + user + "@" + host + ":" + strconv.Itoa(port) + "/" + databaseName + "?" + values.Encode()
}

// symfonyDatabaseServerVersion converts Polka's database selection to Doctrine's serverVersion value.
func symfonyDatabaseServerVersion(environment config.Environment) string {
	if environment.Database == nil {
		return ""
	}

	engine := strings.ToLower(strings.TrimSpace(environment.Database.Engine))
	version := strings.TrimSpace(environment.Database.Version)
	switch engine {
	case tools.MariaDB:
		if version == "" {
			version = strings.TrimSpace(environment.MariaDBVersion)
		}
		if version == "" {
			version = defaultMariaDBVersion
		}
		return "mariadb-" + version
	case tools.MySQL:
		if version == "" {
			version = strings.TrimSpace(environment.MySQLVersion)
		}
		if version == "" {
			return "mysql"
		}
		return version
	case tools.PostgreSQL:
		if version == "" {
			version = strings.TrimSpace(environment.PostgreSQLVersion)
		}
		return version
	default:
		return ""
	}
}

func frameworkDefaultDatabasePort(engine string) int {
	if isPostgreSQL(engine) {
		return defaultPostgreSQLPort
	}

	return defaultDatabasePort
}

func isPostgreSQL(engine string) bool {
	return strings.EqualFold(strings.TrimSpace(engine), tools.PostgreSQL)
}
