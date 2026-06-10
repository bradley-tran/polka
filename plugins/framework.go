package plugins

import (
	"net/url"
	"strconv"
	"strings"

	"polka/config"
	"polka/tools"
)

const (
	Drupal    = "drupal"
	WordPress = "wordpress"
	Laravel   = "laravel"
	Symfony   = "symfony"
)

const (
	defaultPHPVersion        = "8.4"
	defaultComposerVersion   = "2.8"
	defaultNodeJSVersion     = "24"
	defaultNginxVersion      = "1.30"
	defaultMariaDBVersion    = "11.8"
	defaultPHPMyAdminVersion = "5.2"
	defaultMailpitVersion    = "1.30"
	defaultDatabasePort      = 3306
	defaultPHPMyAdminPort    = 8082
	defaultMailpitSMTPPort   = 1025
	defaultMailpitUIPort     = 8025
	defaultDatabaseHost      = "127.0.0.1"
)

// DatabaseCredentials is the framework-safe shape of generated database credentials.
type DatabaseCredentials struct {
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
	OPcacheConfig() map[string]string
	RuntimeEnv(RuntimeEnvContext) map[string]string
	NginxConfig(NginxConfigContext) (NginxConfigResult, bool, error)
}

type builtinFrameworkPlugin struct {
	id            string
	defaults      func() config.Environment
	opcacheConfig func() map[string]string
	runtimeEnv    func(RuntimeEnvContext) map[string]string
	nginx         func(NginxConfigContext) (NginxConfigResult, bool, error)
}

// DefaultFrameworkPlugins returns the built-in framework plugins.
func DefaultFrameworkPlugins() []FrameworkPlugin {
	return []FrameworkPlugin{
		newFrameworkPlugin(Drupal, "web", true),
		newFrameworkPlugin(WordPress, ".", false),
		newFrameworkPlugin(Laravel, "public", true),
		newFrameworkPlugin(Symfony, "public", true),
	}
}

func newFrameworkPlugin(id, docroot string, includeComposerNodeAndMailpit bool) FrameworkPlugin {
	return builtinFrameworkPlugin{
		id: id,
		defaults: func() config.Environment {
			return frameworkDefaults(id, docroot, includeComposerNodeAndMailpit)
		},
		opcacheConfig: func() map[string]string {
			return frameworkOPcacheConfig(id)
		},
		runtimeEnv: func(ctx RuntimeEnvContext) map[string]string {
			return frameworkDatabaseRuntimeEnv(ctx, id == Laravel, id == Symfony)
		},
	}
}

func (p builtinFrameworkPlugin) ID() string {
	return p.id
}

func (p builtinFrameworkPlugin) Defaults() config.Environment {
	if p.defaults == nil {
		return config.Environment{Framework: strings.ToLower(strings.TrimSpace(p.id))}
	}

	return config.NormalizeEnvironment("", p.defaults())
}

func (p builtinFrameworkPlugin) OPcacheConfig() map[string]string {
	if p.opcacheConfig == nil {
		return nil
	}

	return copyStringMap(p.opcacheConfig())
}

func (p builtinFrameworkPlugin) RuntimeEnv(ctx RuntimeEnvContext) map[string]string {
	if p.runtimeEnv == nil {
		return nil
	}

	return p.runtimeEnv(ctx)
}

func (p builtinFrameworkPlugin) NginxConfig(ctx NginxConfigContext) (NginxConfigResult, bool, error) {
	if p.nginx == nil {
		return NginxConfigResult{}, false, nil
	}

	return p.nginx(ctx)
}

func frameworkDefaults(id, docroot string, includeComposerNodeAndMailpit bool) config.Environment {
	environment := config.Environment{
		Framework:      strings.ToLower(strings.TrimSpace(id)),
		PHPVersion:     defaultPHPVersion,
		NginxVersion:   defaultNginxVersion,
		MariaDBVersion: defaultMariaDBVersion,
		Docroot:        docroot,
		Database: &config.DatabaseConfig{
			Engine:  tools.MariaDB,
			Version: defaultMariaDBVersion,
			Port:    defaultDatabasePort,
		},
		PHPMyAdmin: &config.PHPMyAdminConfig{
			Version: defaultPHPMyAdminVersion,
			Port:    defaultPHPMyAdminPort,
		},
		PHPExtensions: frameworkPHPExtensions(id),
		OPcachePreset: config.OPcachePresetDev,
		OPcacheConfig: frameworkOPcacheConfig(id),
	}
	if includeComposerNodeAndMailpit {
		environment.ComposerVersion = defaultComposerVersion
		environment.NodeJSVersion = defaultNodeJSVersion
		environment.Mailpit = &config.MailpitConfig{
			Version:  defaultMailpitVersion,
			SMTPPort: defaultMailpitSMTPPort,
			UIPort:   defaultMailpitUIPort,
		}
	}

	return environment
}

func frameworkOPcacheConfig(id string) map[string]string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case Drupal:
		return map[string]string{"opcache.save_comments": "1"}
	default:
		return nil
	}
}

func frameworkPHPExtensions(id string) map[string]bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case Drupal:
		return phpExtensionMap(
			"curl",
			"dom",
			"fileinfo",
			"gd",
			"intl",
			"mbstring",
			"mysqli",
			"opcache",
			"openssl",
			"pdo_mysql",
			"pdo_sqlite",
			"simplexml",
			"sqlite3",
			"xmlreader",
			"xsl",
			"zip",
			"zlib",
		)
	case Laravel:
		return phpExtensionMap(
			"bcmath",
			"curl",
			"dom",
			"fileinfo",
			"gd",
			"intl",
			"mbstring",
			"mysqli",
			"opcache",
			"openssl",
			"pdo_mysql",
			"pdo_sqlite",
			"simplexml",
			"sqlite3",
			"xmlreader",
			"xsl",
			"zip",
		)
	case Symfony:
		return phpExtensionMap(
			"ctype",
			"curl",
			"dom",
			"fileinfo",
			"gd",
			"iconv",
			"intl",
			"mbstring",
			"mysqli",
			"opcache",
			"openssl",
			"pdo_mysql",
			"pdo_sqlite",
			"session",
			"simplexml",
			"sqlite3",
			"tokenizer",
			"xmlreader",
			"zip",
		)
	case WordPress:
		return phpExtensionMap(
			"bcmath",
			"curl",
			"dom",
			"exif",
			"fileinfo",
			"ftp",
			"gd",
			"iconv",
			"intl",
			"mbstring",
			"mysqli",
			"opcache",
			"openssl",
			"pdo_mysql",
			"shmop",
			"simplexml",
			"sockets",
			"sodium",
			"xmlreader",
			"xsl",
			"zip",
			"zlib",
		)
	default:
		return phpExtensionMap("curl", "fileinfo", "gd", "mbstring", "mysqli", "opcache", "openssl", "pdo_mysql", "zip")
	}
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
		port = defaultDatabasePort
	}

	values := map[string]string{
		"DB_HOST":     host,
		"DB_PORT":     strconv.Itoa(port),
		"DB_DATABASE": ctx.Database.DatabaseName,
		"DB_USERNAME": ctx.Database.User,
		"DB_PASSWORD": ctx.Database.Password,
	}
	if includeLaravelConnection {
		values["DB_CONNECTION"] = "mysql"
	}
	if includeSymfonyDatabaseURL {
		values["DATABASE_URL"] = symfonyDatabaseURL(ctx, host, port)
	}

	return values
}

// symfonyDatabaseURL renders managed database credentials in Symfony's Doctrine URL form.
func symfonyDatabaseURL(ctx RuntimeEnvContext, host string, port int) string {
	user := url.UserPassword(ctx.Database.User, ctx.Database.Password).String()
	databaseName := strings.TrimPrefix(url.PathEscape(ctx.Database.DatabaseName), "/")
	serverVersion := symfonyDatabaseServerVersion(ctx.Environment)
	values := url.Values{"charset": []string{"utf8mb4"}}
	if serverVersion != "" {
		values.Set("serverVersion", serverVersion)
	}

	return "mysql://" + user + "@" + host + ":" + strconv.Itoa(port) + "/" + databaseName + "?" + values.Encode()
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
	default:
		return ""
	}
}
