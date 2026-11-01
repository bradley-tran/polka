package plugins

import (
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"

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
	Detection() FrameworkDetection
	RuntimeEnv(RuntimeEnvContext) (map[string]string, error)
	PostComposer(PostComposerContext) error
	NginxConfig(NginxConfigContext) (NginxConfigResult, bool, error)
}

type builtinFrameworkPlugin struct {
	id              string
	defaults        config.Environment
	phpExtensions   map[string]bool
	databaseEngines map[string]bool
	opcacheConfig   map[string]string
	manifest        frameworkManifest
	files           fs.FS
	nginx           func(NginxConfigContext) (NginxConfigResult, bool, error)
}

// builtinFrameworkPlugins parses immutable embedded declarations once per
// process, keeping registry construction cheap on the dispatch path.
var builtinFrameworkPlugins = sync.OnceValue(func() []FrameworkPlugin {
	plugins, err := loadFrameworkPlugins(builtinFrameworkManifestFiles)
	if err != nil {
		panic(err)
	}
	return plugins
})

// DefaultFrameworkPlugins returns an isolated list of the built-in plugins.
func DefaultFrameworkPlugins() []FrameworkPlugin {
	return slices.Clone(builtinFrameworkPlugins())
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

// Detection returns isolated metadata so callers cannot mutate the registry.
func (p builtinFrameworkPlugin) Detection() FrameworkDetection { return p.manifest.Detection.clone() }

func (p builtinFrameworkPlugin) RuntimeEnv(ctx RuntimeEnvContext) (map[string]string, error) {
	return frameworkManifestRuntimeEnv(ctx, p.manifest)
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

// frameworkDefaultDatabasePort normalizes shared database context defaults.
func frameworkDefaultDatabasePort(engine string) int {
	if strings.EqualFold(strings.TrimSpace(engine), tools.PostgreSQL) {
		return defaultPostgreSQLPort
	}
	return defaultDatabasePort
}
