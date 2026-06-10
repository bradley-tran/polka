package plugins

import (
	"strconv"
	"strings"

	"polka/config"
	"polka/tools"
)

const (
	Drupal    = "drupal"
	WordPress = "wordpress"
	Laravel   = "laravel"
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
	RuntimeEnv(RuntimeEnvContext) map[string]string
	NginxConfig(NginxConfigContext) (NginxConfigResult, bool, error)
}

type builtinFrameworkPlugin struct {
	id         string
	defaults   func() config.Environment
	runtimeEnv func(RuntimeEnvContext) map[string]string
	nginx      func(NginxConfigContext) (NginxConfigResult, bool, error)
}

// DefaultFrameworkPlugins returns the built-in framework plugins.
func DefaultFrameworkPlugins() []FrameworkPlugin {
	return []FrameworkPlugin{
		newFrameworkPlugin(Drupal, "web", true),
		newFrameworkPlugin(WordPress, ".", false),
		newFrameworkPlugin(Laravel, "public", true),
	}
}

func newFrameworkPlugin(id, docroot string, includeComposerNodeAndMailpit bool) FrameworkPlugin {
	return builtinFrameworkPlugin{
		id: id,
		defaults: func() config.Environment {
			return frameworkDefaults(id, docroot, includeComposerNodeAndMailpit)
		},
		runtimeEnv: func(ctx RuntimeEnvContext) map[string]string {
			return frameworkDatabaseRuntimeEnv(ctx, id == Laravel)
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

func frameworkDatabaseRuntimeEnv(ctx RuntimeEnvContext, includeLaravelConnection bool) map[string]string {
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

	return values
}
