package tools

import (
	"fmt"
	"net/http"
	"strings"

	"polka/config"
)

type DownloadContext struct {
	Client   *http.Client
	CacheDir string
	Tool     string
	Version  string
}

type InstallContext struct {
	ProjectDir  string
	RootDir     string
	EnvsDir     string
	BinDir      string
	CacheDir    string
	Environment config.Environment
	Result      InstallResult
}

type InstallRequest struct {
	Tool    string
	Version string
}

type InstallResult struct {
	Tool       string
	Version    string
	CachePath  string
	TargetPath string
	Downloaded bool
}

// ToolPlugin describes an installable managed tool and its command dispatch behavior.
type ToolPlugin interface {
	ID() string
	Version(config.Environment) string
	Validate(config.Environment) error
	InstallCandidates(root, version string) []string
	DispatchCommands() []string
	CleanupCommands() []string
	ActiveCommands(config.Environment) []string
	DispatchCandidates(root, executable, version string) []string
	Download(DownloadContext) error
	PostInstall(InstallContext) error
}

// Plugin is kept as a compatibility alias for older backend-facing tests and helpers.
type Plugin = ToolPlugin

type Registry struct {
	plugins []ToolPlugin
	byID    map[string]ToolPlugin
}

type DispatchRequest struct {
	ConfigTool string
	Executable string
	Plugin     ToolPlugin
}

func NewRegistry(plugins ...ToolPlugin) (*Registry, error) {
	registry := &Registry{byID: map[string]ToolPlugin{}}
	for _, plugin := range plugins {
		if err := registry.Register(plugin); err != nil {
			return nil, err
		}
	}

	return registry, nil
}

func NewDefaultRegistry() *Registry {
	registry, err := NewRegistry(DefaultPlugins()...)
	if err != nil {
		panic(err)
	}

	return registry
}

func (r *Registry) Register(plugin ToolPlugin) error {
	if plugin == nil {
		return fmt.Errorf("tool plugin cannot be nil")
	}
	if r.byID == nil {
		r.byID = map[string]ToolPlugin{}
	}

	id := strings.ToLower(strings.TrimSpace(plugin.ID()))
	if id == "" {
		return fmt.Errorf("tool plugin id cannot be empty")
	}
	if !validName.MatchString(id) {
		return fmt.Errorf("invalid tool plugin id %q: use letters, numbers, dots, dashes, or underscores", plugin.ID())
	}
	if _, exists := r.byID[id]; exists {
		return fmt.Errorf("duplicate tool plugin %q", id)
	}

	r.plugins = append(r.plugins, plugin)
	r.byID[id] = plugin
	return nil
}

func (r *Registry) Plugins() []ToolPlugin {
	if r == nil {
		return nil
	}

	plugins := make([]ToolPlugin, len(r.plugins))
	copy(plugins, r.plugins)
	return plugins
}

func (r *Registry) Plugin(id string) (ToolPlugin, bool) {
	if r == nil {
		return nil, false
	}

	plugin, ok := r.byID[strings.ToLower(strings.TrimSpace(id))]
	return plugin, ok
}

func (r *Registry) ValidateEnvironment(environment config.Environment) error {
	if r == nil {
		return fmt.Errorf("tool registry is not configured")
	}

	for _, plugin := range r.plugins {
		if err := plugin.Validate(environment); err != nil {
			return err
		}
	}
	if environment.Database != nil {
		engine := strings.ToLower(strings.TrimSpace(environment.Database.Engine))
		if engine != "" {
			if _, ok := r.Plugin(engine); !ok {
				return fmt.Errorf("unsupported database engine %q", environment.Database.Engine)
			}
		}
		if err := validateDatabaseConfig(environment.Database); err != nil {
			return err
		}
	}

	return nil
}

func (r *Registry) InstallRequests(environment config.Environment) []InstallRequest {
	if r == nil {
		return nil
	}

	requests := []InstallRequest{}
	for _, plugin := range r.plugins {
		version := strings.TrimSpace(plugin.Version(environment))
		if version != "" {
			requests = append(requests, InstallRequest{Tool: plugin.ID(), Version: version})
		}
	}

	return requests
}

func (r *Registry) ResolveDispatchRequest(tool string) (DispatchRequest, error) {
	if r == nil {
		return DispatchRequest{}, fmt.Errorf("tool registry is not configured")
	}

	trimmed := strings.ToLower(strings.TrimSpace(tool))
	for _, plugin := range r.plugins {
		for _, command := range plugin.DispatchCommands() {
			if strings.EqualFold(strings.TrimSpace(command), trimmed) {
				return DispatchRequest{
					ConfigTool: plugin.ID(),
					Executable: strings.ToLower(strings.TrimSpace(command)),
					Plugin:     plugin,
				}, nil
			}
		}
	}

	return DispatchRequest{}, fmt.Errorf("unsupported tool %q", tool)
}

func (r *Registry) InstallCandidates(root, tool, version string) []string {
	plugin, ok := r.Plugin(tool)
	if !ok {
		return nil
	}

	return plugin.InstallCandidates(root, version)
}

func (r *Registry) DispatchCandidates(root, configTool, executable, version string) []string {
	plugin, ok := r.Plugin(configTool)
	if !ok {
		return nil
	}

	return plugin.DispatchCandidates(root, executable, version)
}

func (r *Registry) CleanupCommandNames() []string {
	if r == nil {
		return nil
	}

	return uniqueToolCommands(r.plugins, func(plugin ToolPlugin) []string {
		return plugin.CleanupCommands()
	})
}

func (r *Registry) ActiveCommandNames(environment *config.Environment) []string {
	if r == nil || environment == nil {
		return nil
	}

	return uniqueToolCommands(r.plugins, func(plugin ToolPlugin) []string {
		return plugin.ActiveCommands(*environment)
	})
}

func uniqueToolCommands(plugins []ToolPlugin, commandList func(ToolPlugin) []string) []string {
	seen := map[string]bool{}
	commands := []string{}
	for _, plugin := range plugins {
		for _, command := range commandList(plugin) {
			normalized := strings.ToLower(strings.TrimSpace(command))
			if normalized == "" || seen[normalized] {
				continue
			}
			seen[normalized] = true
			commands = append(commands, normalized)
		}
	}

	return commands
}

type builtinPlugin struct {
	id                 string
	version            func(config.Environment) string
	validate           func(config.Environment) error
	installCandidates  func(root, version string) []string
	dispatchCommands   []string
	cleanupCommands    []string
	activeCommands     func(config.Environment) []string
	dispatchCandidates func(root, executable, version string) []string
	download           func(DownloadContext) error
	postInstall        func(InstallContext) error
}

func (p builtinPlugin) ID() string {
	return p.id
}

func (p builtinPlugin) Version(environment config.Environment) string {
	if p.version == nil {
		return ""
	}

	return strings.TrimSpace(p.version(environment))
}

func (p builtinPlugin) Validate(environment config.Environment) error {
	if p.validate != nil {
		return p.validate(environment)
	}

	version := p.Version(environment)
	if version == "" {
		return nil
	}

	return validateVersion(p.id, version)
}

func (p builtinPlugin) InstallCandidates(root, version string) []string {
	if p.installCandidates == nil {
		return nil
	}

	return p.installCandidates(root, version)
}

func (p builtinPlugin) DispatchCommands() []string {
	return copyStrings(p.dispatchCommands)
}

func (p builtinPlugin) CleanupCommands() []string {
	if len(p.cleanupCommands) > 0 {
		return copyStrings(p.cleanupCommands)
	}

	return p.DispatchCommands()
}

func (p builtinPlugin) ActiveCommands(environment config.Environment) []string {
	if p.activeCommands != nil {
		return copyStrings(p.activeCommands(environment))
	}
	if p.Version(environment) == "" {
		return nil
	}

	return p.DispatchCommands()
}

func (p builtinPlugin) DispatchCandidates(root, executable, version string) []string {
	if p.dispatchCandidates == nil {
		if strings.EqualFold(strings.TrimSpace(executable), p.id) {
			return p.InstallCandidates(root, version)
		}
		return nil
	}

	return p.dispatchCandidates(root, executable, version)
}

func (p builtinPlugin) Download(ctx DownloadContext) error {
	if p.download == nil {
		return fmt.Errorf("unsupported tool %q", p.id)
	}

	return p.download(ctx)
}

func (p builtinPlugin) PostInstall(ctx InstallContext) error {
	if p.postInstall == nil {
		return nil
	}

	return p.postInstall(ctx)
}

func copyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	copied := make([]string, len(values))
	copy(copied, values)
	return copied
}
