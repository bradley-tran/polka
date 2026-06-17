package plugins

import (
	"embed"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"

	"polka/config"
)

//go:embed manifests/*.yaml
var builtinFrameworkManifestFiles embed.FS

type frameworkManifest struct {
	ID            string                        `yaml:"id"`
	Defaults      config.EnvironmentFile        `yaml:"defaults"`
	PHPExtensions []string                      `yaml:"php-extensions"`
	OPcacheConfig map[string]string             `yaml:"opcache-config"`
	RuntimeEnv    frameworkRuntimeEnvManifest   `yaml:"runtime-env"`
	PostComposer  frameworkPostComposerManifest `yaml:"post-composer"`
}

type frameworkRuntimeEnvManifest struct {
	Database frameworkRuntimeDatabaseManifest `yaml:"database"`
}

type frameworkRuntimeDatabaseManifest struct {
	Keys        string `yaml:"keys"`
	Connection  string `yaml:"connection"`
	DatabaseURL string `yaml:"database-url"`
}

type frameworkPostComposerManifest struct {
	Strategy string                 `yaml:"strategy"`
	AppRoot  frameworkAppRootConfig `yaml:"app-root"`
	Dotenv   frameworkDotenvConfig  `yaml:"dotenv"`
}

type frameworkAppRootConfig struct {
	PublicDir string `yaml:"public-dir"`
	Framework string `yaml:"framework"`
}

type frameworkDotenvConfig struct {
	Target   string   `yaml:"target"`
	Template string   `yaml:"template"`
	Values   []string `yaml:"values"`
}

func newManifestFrameworkPlugin(name string) FrameworkPlugin {
	manifest, err := loadBuiltinFrameworkManifest(name)
	if err != nil {
		panic(err)
	}
	plugin, err := manifest.toPlugin()
	if err != nil {
		panic(err)
	}

	return plugin
}

func loadBuiltinFrameworkManifest(name string) (frameworkManifest, error) {
	path := "manifests/" + strings.TrimSpace(name) + ".yaml"
	data, err := builtinFrameworkManifestFiles.ReadFile(path)
	if err != nil {
		return frameworkManifest{}, fmt.Errorf("read builtin framework manifest %s: %w", path, err)
	}

	return parseFrameworkManifest(data)
}

func parseFrameworkManifest(data []byte) (frameworkManifest, error) {
	var manifest frameworkManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return frameworkManifest{}, fmt.Errorf("parse framework manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return frameworkManifest{}, err
	}

	manifest.ID = strings.ToLower(strings.TrimSpace(manifest.ID))
	manifest.Defaults.Framework = manifest.ID
	manifest.RuntimeEnv.Database = normalizeRuntimeDatabaseManifest(manifest.RuntimeEnv.Database)
	manifest.PostComposer = normalizePostComposerManifest(manifest.ID, manifest.PostComposer)
	manifest.OPcacheConfig = config.NormalizeOPcacheConfig(manifest.OPcacheConfig)

	return manifest, nil
}

func (m frameworkManifest) validate() error {
	id := strings.ToLower(strings.TrimSpace(m.ID))
	if id == "" {
		return fmt.Errorf("framework manifest id cannot be empty")
	}
	if !validPluginID.MatchString(id) {
		return fmt.Errorf("invalid framework manifest id %q: use letters, numbers, dots, dashes, or underscores", m.ID)
	}
	defaultFramework := strings.ToLower(strings.TrimSpace(m.Defaults.Framework))
	if defaultFramework != "" && defaultFramework != id {
		return fmt.Errorf("framework manifest %q defaults.framework must match id", id)
	}
	if strings.TrimSpace(m.Defaults.Docroot) == "" {
		return fmt.Errorf("framework manifest %q defaults.docroot cannot be empty", id)
	}
	if err := validateFrameworkRuntimeEnvManifest(id, m.RuntimeEnv); err != nil {
		return err
	}
	if err := validateFrameworkPostComposerManifest(id, m.PostComposer); err != nil {
		return err
	}

	return nil
}

func validateFrameworkRuntimeEnvManifest(id string, runtimeEnv frameworkRuntimeEnvManifest) error {
	database := runtimeEnv.Database
	switch strings.ToLower(strings.TrimSpace(database.Keys)) {
	case "", "standard", "codeigniter":
	default:
		return fmt.Errorf("framework manifest %q runtime-env.database.keys has unsupported value %q", id, database.Keys)
	}
	switch strings.ToLower(strings.TrimSpace(database.Connection)) {
	case "", "mysql":
	default:
		return fmt.Errorf("framework manifest %q runtime-env.database.connection has unsupported value %q", id, database.Connection)
	}
	switch strings.ToLower(strings.TrimSpace(database.DatabaseURL)) {
	case "", "symfony":
	default:
		return fmt.Errorf("framework manifest %q runtime-env.database.database-url has unsupported value %q", id, database.DatabaseURL)
	}

	return nil
}

func validateFrameworkPostComposerManifest(id string, post frameworkPostComposerManifest) error {
	strategy := strings.ToLower(strings.TrimSpace(post.Strategy))
	switch strategy {
	case "":
		return nil
	case "cakephp-config", "dotenv", "drupal-settings", "wordpress-config":
	default:
		return fmt.Errorf("framework manifest %q post-composer.strategy has unsupported value %q", id, post.Strategy)
	}
	if strings.TrimSpace(post.AppRoot.PublicDir) == "" {
		return fmt.Errorf("framework manifest %q post-composer.app-root.public-dir cannot be empty", id)
	}
	if err := validateFrameworkManifestRelativePath(post.AppRoot.PublicDir, "post-composer.app-root.public-dir", true); err != nil {
		return fmt.Errorf("framework manifest %q: %w", id, err)
	}
	if strings.TrimSpace(post.AppRoot.Framework) != "" && !validPluginID.MatchString(strings.ToLower(strings.TrimSpace(post.AppRoot.Framework))) {
		return fmt.Errorf("framework manifest %q post-composer.app-root.framework has invalid value %q", id, post.AppRoot.Framework)
	}
	if strategy == "dotenv" {
		if strings.TrimSpace(post.Dotenv.Target) == "" {
			return fmt.Errorf("framework manifest %q post-composer.dotenv.target cannot be empty", id)
		}
		if err := validateFrameworkManifestRelativePath(post.Dotenv.Target, "post-composer.dotenv.target", false); err != nil {
			return fmt.Errorf("framework manifest %q: %w", id, err)
		}
		if strings.TrimSpace(post.Dotenv.Template) != "" {
			if err := validateFrameworkManifestRelativePath(post.Dotenv.Template, "post-composer.dotenv.template", false); err != nil {
				return fmt.Errorf("framework manifest %q: %w", id, err)
			}
		}
	}

	return nil
}

func validateFrameworkManifestRelativePath(path, label string, allowDot bool) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("%s cannot be empty", label)
	}
	if filepath.IsAbs(trimmed) || strings.HasPrefix(filepath.ToSlash(trimmed), "/") {
		return fmt.Errorf("%s %q must be relative", label, path)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(trimmed)))
	if clean == ".." || strings.HasPrefix(clean, "../") || (clean == "." && !allowDot) {
		return fmt.Errorf("%s %q must stay inside the framework app root", label, path)
	}

	return nil
}

func normalizeRuntimeDatabaseManifest(database frameworkRuntimeDatabaseManifest) frameworkRuntimeDatabaseManifest {
	return frameworkRuntimeDatabaseManifest{
		Keys:        strings.ToLower(strings.TrimSpace(database.Keys)),
		Connection:  strings.ToLower(strings.TrimSpace(database.Connection)),
		DatabaseURL: strings.ToLower(strings.TrimSpace(database.DatabaseURL)),
	}
}

func normalizePostComposerManifest(id string, post frameworkPostComposerManifest) frameworkPostComposerManifest {
	post.Strategy = strings.ToLower(strings.TrimSpace(post.Strategy))
	post.AppRoot.PublicDir = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(post.AppRoot.PublicDir))))
	post.AppRoot.Framework = strings.ToLower(strings.TrimSpace(post.AppRoot.Framework))
	if post.AppRoot.Framework == "" {
		post.AppRoot.Framework = id
	}
	post.Dotenv.Target = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(post.Dotenv.Target))))
	if strings.TrimSpace(post.Dotenv.Template) != "" {
		post.Dotenv.Template = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(post.Dotenv.Template))))
	}
	post.Dotenv.Values = normalizeManifestStrings(post.Dotenv.Values)

	return post
}

func normalizeManifestStrings(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}

	return normalized
}

func (m frameworkManifest) toPlugin() (FrameworkPlugin, error) {
	defaults := config.EnvironmentFileToEnvironment("", m.Defaults)
	defaults.OPcacheConfig = config.NormalizeOPcacheConfig(m.OPcacheConfig)

	return builtinFrameworkPlugin{
		id:              strings.ToLower(strings.TrimSpace(m.ID)),
		defaults:        config.NormalizeEnvironment("", defaults),
		phpExtensions:   phpExtensionMap(m.PHPExtensions...),
		opcacheConfig:   config.NormalizeOPcacheConfig(m.OPcacheConfig),
		runtimeDatabase: m.RuntimeEnv.Database,
		postComposer:    m.PostComposer,
	}, nil
}

func frameworkManifestRuntimeEnv(ctx RuntimeEnvContext, database frameworkRuntimeDatabaseManifest) map[string]string {
	switch database.Keys {
	case "codeigniter":
		return codeIgniterDatabaseRuntimeEnv(ctx)
	case "standard":
		return frameworkDatabaseRuntimeEnv(ctx, database.Connection == "mysql", database.DatabaseURL == "symfony")
	default:
		return nil
	}
}

func frameworkManifestPostComposer(ctx PostComposerContext, plugin builtinFrameworkPlugin) error {
	post := plugin.postComposer
	if post.Strategy == "" {
		return nil
	}

	appRoot := frameworkComposerAppRoot(ctx, post.AppRoot.PublicDir, post.AppRoot.Framework)
	switch post.Strategy {
	case "cakephp-config":
		return writeCakePHPConfigSecretsForAppRoot(ctx, appRoot)
	case "dotenv":
		values := plugin.RuntimeEnv(RuntimeEnvContext{
			Environment: ctx.Environment,
			Database:    ctx.Database,
		})
		if len(values) == 0 {
			return nil
		}
		return writeDotenvSecretFile(
			filepath.Join(appRoot, filepath.FromSlash(post.Dotenv.Target)),
			frameworkManifestFallbackPath(appRoot, post.Dotenv.Template),
			orderedDotenvAssignments(values, post.Dotenv.Values),
		)
	case "drupal-settings":
		return writeDrupalSettingsSecretsForAppRoot(ctx, appRoot, post.AppRoot.PublicDir)
	case "wordpress-config":
		return writeWordPressConfigSecretsForAppRoot(ctx, appRoot)
	default:
		return nil
	}
}

func frameworkManifestFallbackPath(appRoot, path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}

	return filepath.Join(appRoot, filepath.FromSlash(path))
}
