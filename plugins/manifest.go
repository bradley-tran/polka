package plugins

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
	"polka/config"
)

//go:embed manifests/*.yaml manifests/templates/* manifests/scripts/*.star
var builtinFrameworkManifestFiles embed.FS

// frameworkManifest contains immutable declarations interpreted by shared Go
// operations. No behavior is selected by a framework ID.
type frameworkManifest struct {
	ID              string                        `yaml:"id"`
	Defaults        config.EnvironmentFile        `yaml:"defaults"`
	PHPExtensions   []string                      `yaml:"php-extensions"`
	DatabaseEngines []string                      `yaml:"database-engines"`
	OPcacheConfig   map[string]string             `yaml:"opcache-config"`
	Detection       FrameworkDetection            `yaml:"detection"`
	AppRoot         frameworkAppRootConfig        `yaml:"app-root"`
	Values          map[string]frameworkValue     `yaml:"values"`
	RuntimeEnv      frameworkRuntimeEnvManifest   `yaml:"runtime-env"`
	PostComposer    frameworkPostComposerManifest `yaml:"post-composer"`
	Script          frameworkScript               `yaml:"script"`
}

// frameworkAppRootConfig holds permissive root hints, independently of the
// distinctive marker files used for framework detection.
type frameworkAppRootConfig struct {
	PublicDir string   `yaml:"public-dir"`
	Markers   []string `yaml:"markers"`
}

type frameworkRuntimeEnvManifest struct {
	RequiresDatabase bool                      `yaml:"requires-database"`
	Values           map[string]frameworkValue `yaml:"values"`
}

type frameworkPostComposerManifest struct {
	RequiresDatabase bool                      `yaml:"requires-database"`
	Actions          []frameworkActionManifest `yaml:"actions"`
}

// frameworkActionManifest is a discriminated union. Fields belonging to other
// operations are rejected rather than accepted without effect.
type frameworkActionManifest struct {
	Type                 string                    `yaml:"type"`
	Base                 string                    `yaml:"base"`
	Path                 string                    `yaml:"path"`
	Fallback             string                    `yaml:"fallback"`
	FallbackTemplateFile string                    `yaml:"fallback-template-file"`
	TemplateFile         string                    `yaml:"template-file"`
	Encoding             string                    `yaml:"encoding"`
	Values               map[string]frameworkValue `yaml:"values"`
	Order                []string                  `yaml:"order"`
	Updates              []frameworkArrayUpdate    `yaml:"updates"`
	InsertBefore         string                    `yaml:"insert-before"`
	Include              string                    `yaml:"include"`
	Marker               string                    `yaml:"marker"`
}

// frameworkArrayUpdate binds scalar values within a returned-array path.
type frameworkArrayUpdate struct {
	Path   []string                  `yaml:"path"`
	Values map[string]frameworkValue `yaml:"values"`
	Order  []string                  `yaml:"order"`
}

var frameworkPlaceholder = regexp.MustCompile(`\{\{([A-Za-z0-9_.-]+)\}\}`)

// newManifestFrameworkPlugin also supports focused tests of built-in manifests.
func newManifestFrameworkPlugin(name string) FrameworkPlugin {
	m, err := loadBuiltinFrameworkManifest(name)
	if err != nil {
		panic(err)
	}
	plugin, err := m.toPlugin()
	if err != nil {
		panic(err)
	}
	return plugin
}

// loadBuiltinFrameworkManifest reads a single embedded declaration for focused callers.
func loadBuiltinFrameworkManifest(name string) (frameworkManifest, error) {
	filename := "manifests/" + strings.TrimSpace(name) + ".yaml"
	data, err := builtinFrameworkManifestFiles.ReadFile(filename)
	if err != nil {
		return frameworkManifest{}, fmt.Errorf("read builtin framework manifest %s: %w", filename, err)
	}
	return parseFrameworkManifest(data)
}

// loadFrameworkPlugins enumerates manifests without a hardcoded registry list.
// Supplying an FS also allows tests to register entirely new frameworks.
func loadFrameworkPlugins(files fs.FS) ([]FrameworkPlugin, error) {
	names, err := fs.Glob(files, "manifests/*.yaml")
	if err != nil {
		return nil, err
	}
	plugins := make([]FrameworkPlugin, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		data, err := fs.ReadFile(files, name)
		if err != nil {
			return nil, fmt.Errorf("read framework manifest %s: %w", name, err)
		}
		m, err := parseFrameworkManifestWithFiles(data, files)
		if err != nil {
			return nil, fmt.Errorf("framework manifest %s: %w", name, err)
		}
		if seen[m.ID] {
			return nil, fmt.Errorf("duplicate framework manifest %q", m.ID)
		}
		seen[m.ID] = true
		plugin, err := m.pluginWithFiles(files)
		if err != nil {
			return nil, err
		}
		plugins = append(plugins, plugin)
	}
	return plugins, nil
}

// parseFrameworkManifest validates against the embedded template assets.
func parseFrameworkManifest(data []byte) (frameworkManifest, error) {
	return parseFrameworkManifestWithFiles(data, builtinFrameworkManifestFiles)
}

// parseFrameworkManifestWithFiles rejects unknown fields before graph validation.
func parseFrameworkManifestWithFiles(data []byte, files fs.FS) (frameworkManifest, error) {
	var m frameworkManifest
	if err := yaml.UnmarshalWithOptions(data, &m, yaml.Strict()); err != nil {
		return frameworkManifest{}, fmt.Errorf("parse framework manifest: %w", err)
	}
	m.ID = strings.ToLower(strings.TrimSpace(m.ID))
	if err := m.validate(files); err != nil {
		return frameworkManifest{}, fmt.Errorf("framework manifest %q: %w", m.ID, err)
	}
	m.Defaults.Framework = m.ID
	m.OPcacheConfig = config.NormalizeOPcacheConfig(m.OPcacheConfig)
	return m, nil
}

// validate checks metadata, portable paths, references, and every action before use.
func (m frameworkManifest) validate(files fs.FS) error {
	if m.ID == "" {
		return fmt.Errorf("framework manifest id cannot be empty")
	}
	if !validPluginID.MatchString(m.ID) {
		return fmt.Errorf("invalid framework manifest id %q", m.ID)
	}
	if id := strings.ToLower(strings.TrimSpace(m.Defaults.Framework)); id != "" && id != m.ID {
		return fmt.Errorf("defaults.framework must match id")
	}
	if strings.TrimSpace(m.Defaults.Docroot) == "" {
		return fmt.Errorf("defaults.docroot cannot be empty")
	}
	if err := config.ValidateWorkersConfig(m.Defaults.Workers); err != nil {
		return err
	}
	if err := m.Detection.validate(); err != nil {
		return err
	}
	if err := m.validateScript(files); err != nil {
		return err
	}
	if m.AppRoot.PublicDir != "" {
		if err := validateFrameworkManifestRelativePath(m.AppRoot.PublicDir, "app-root.public-dir", true); err != nil {
			return err
		}
	} else if len(m.PostComposer.Actions) > 0 || m.Script.hasHook(frameworkHookPostComposer) {
		return fmt.Errorf("app-root.public-dir cannot be empty")
	}
	for _, marker := range m.AppRoot.Markers {
		if err := validateFrameworkManifestRelativePath(marker, "app-root.markers", false); err != nil {
			return err
		}
	}
	for _, engine := range m.DatabaseEngines {
		if !slices.Contains([]string{"mysql", "mariadb", "postgresql", "sqlite"}, engine) {
			return fmt.Errorf("unsupported database engine %q", engine)
		}
	}
	if err := m.validateValues(); err != nil {
		return err
	}
	for i, a := range m.PostComposer.Actions {
		if err := a.validate(files); err != nil {
			return fmt.Errorf("post-composer.actions[%d]: %w", i, err)
		}
	}
	return nil
}

// validateFrameworkManifestRelativePath applies portable containment rules,
// rejecting Windows drive and backslash paths even on Unix.
func validateFrameworkManifestRelativePath(value, label string, allowDot bool) error {
	if value == "." && allowDot {
		return nil
	}
	if value == "" || strings.ContainsAny(value, "\\:\x00") || !fs.ValidPath(value) {
		return fmt.Errorf("%s %q must be a slash-separated relative path inside its base", label, value)
	}
	for _, segment := range strings.Split(value, "/") {
		if strings.TrimRight(segment, " .") != segment {
			return fmt.Errorf("%s %q must not contain trailing dots or spaces in path segments", label, value)
		}
	}
	return nil
}

// validate checks the action discriminator and its required and forbidden fields.
func (a frameworkActionManifest) validate(files fs.FS) error {
	if !slices.Contains([]string{"dotenv", "php-array", "php-constants", "write-file", "php-include"}, a.Type) {
		return fmt.Errorf("unsupported action type %q", a.Type)
	}
	if a.Base != "" && a.Base != "app-root" && a.Base != "docroot" {
		return fmt.Errorf("unsupported base %q", a.Base)
	}
	for _, item := range []struct{ label, value string }{{"path", a.Path}, {"fallback", a.Fallback}, {"include", a.Include}} {
		if item.value != "" || item.label == "path" {
			if err := validateFrameworkManifestRelativePath(item.value, item.label, false); err != nil {
				return err
			}
		}
	}
	for _, template := range []string{a.TemplateFile, a.FallbackTemplateFile} {
		if template == "" {
			continue
		}
		if err := validateFrameworkManifestRelativePath(template, "template-file", false); err != nil {
			return err
		}
		data, err := fs.ReadFile(files, "manifests/templates/"+template)
		if err != nil {
			return fmt.Errorf("read template %q: %w", template, err)
		}
		if template == a.FallbackTemplateFile && frameworkPlaceholder.Match(data) {
			return fmt.Errorf("fallback-template-file must contain literal content without placeholders")
		}
	}
	if err := validateFrameworkOrder(a.Order, a.Values); err != nil {
		return err
	}
	if a.Type != "php-array" && len(a.Updates) != 0 {
		return fmt.Errorf("updates requires php-array")
	}
	if a.Type != "php-constants" && a.InsertBefore != "" {
		return fmt.Errorf("insert-before requires php-constants")
	}
	if a.Type != "php-include" && (a.Include != "" || a.Marker != "") {
		return fmt.Errorf("include and marker require php-include")
	}
	if a.Type != "write-file" && (a.TemplateFile != "" || a.Encoding != "") {
		return fmt.Errorf("template-file and encoding require write-file")
	}
	if (a.Type == "php-array" || a.Type == "php-include") && (len(a.Values) != 0 || len(a.Order) != 0) {
		return fmt.Errorf("values and order are not supported for %s", a.Type)
	}
	switch a.Type {
	case "php-array":
		if len(a.Updates) == 0 {
			return fmt.Errorf("php-array requires updates")
		}
		for _, u := range a.Updates {
			for _, key := range u.Path {
				if key == "" {
					return fmt.Errorf("array path keys cannot be empty")
				}
			}
			if len(u.Values) == 0 {
				return fmt.Errorf("array update requires values")
			}
			if err := validateFrameworkOrder(u.Order, u.Values); err != nil {
				return err
			}
		}
	case "php-include":
		if a.Include == "" || a.Marker == "" || !validPluginID.MatchString(a.Marker) {
			return fmt.Errorf("php-include requires include and a valid marker")
		}
	case "write-file":
		if a.TemplateFile == "" {
			return fmt.Errorf("write-file requires template-file")
		}
		if a.Encoding != "text" && a.Encoding != "php-literal" {
			return fmt.Errorf("write-file encoding must be text or php-literal")
		}
		if a.Fallback != "" || a.FallbackTemplateFile != "" {
			return fmt.Errorf("write-file does not support fallbacks")
		}
		data, err := fs.ReadFile(files, "manifests/templates/"+a.TemplateFile)
		if err != nil {
			return err
		}
		for _, match := range frameworkPlaceholder.FindAllStringSubmatch(string(data), -1) {
			if _, ok := a.Values[match[1]]; !ok {
				return fmt.Errorf("unknown template placeholder %q", match[1])
			}
		}
	default:
		if len(a.Values) == 0 {
			return fmt.Errorf("%s requires values", a.Type)
		}
	}
	return nil
}

// validateFrameworkOrder rejects misspelled or repeated output keys.
func validateFrameworkOrder(order []string, values map[string]frameworkValue) error {
	seen := map[string]bool{}
	for _, key := range order {
		if _, ok := values[key]; !ok || seen[key] {
			return fmt.Errorf("order contains unknown or duplicate key %q", key)
		}
		seen[key] = true
	}
	return nil
}

// toPlugin converts validated declarations using the built-in asset filesystem.
func (m frameworkManifest) toPlugin() (FrameworkPlugin, error) {
	return m.pluginWithFiles(builtinFrameworkManifestFiles)
}

// pluginWithFiles retains immutable declarations and normalizes isolated defaults.
func (m frameworkManifest) pluginWithFiles(files fs.FS) (FrameworkPlugin, error) {
	defaults := config.EnvironmentFileToEnvironment("", m.Defaults)
	defaults.OPcacheConfig = config.NormalizeOPcacheConfig(m.OPcacheConfig)
	return builtinFrameworkPlugin{
		id: m.ID, defaults: config.NormalizeEnvironment("", defaults), phpExtensions: phpExtensionMap(m.PHPExtensions...),
		databaseEngines: stringSet(m.DatabaseEngines), opcacheConfig: config.NormalizeOPcacheConfig(m.OPcacheConfig), manifest: m, files: files,
		loadScript: newFrameworkScriptLoader(files, m.ID, m.Script),
	}, nil
}

// stringSet canonicalizes identifiers for read-only membership checks.
func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		if v := strings.ToLower(strings.TrimSpace(value)); v != "" {
			set[v] = true
		}
	}
	return set
}

// frameworkTemplate reads embedded assets, never application PHP.
func frameworkTemplate(files fs.FS, name string) (string, error) {
	data, err := fs.ReadFile(files, "manifests/templates/"+name)
	return string(data), err
}
