package backend

import (
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
)

type ToolDownloadContext struct {
	Client   *http.Client
	CacheDir string
	Tool     string
	Version  string
}

type ToolInstallContext struct {
	Store       Store
	Environment Environment
	Result      InstallResult
}

type ToolPlugin interface {
	ID() string
	Version(Environment) string
	Validate(Environment) error
	InstallCandidates(root, version string) []string
	DispatchCommands() []string
	CleanupCommands() []string
	ActiveCommands(Environment) []string
	DispatchCandidates(root, executable, version string) []string
	Download(ToolDownloadContext) error
	PostInstall(ToolInstallContext) error
}

type ToolRegistry struct {
	plugins []ToolPlugin
	byID    map[string]ToolPlugin
}

type ToolDispatchRequest struct {
	ConfigTool string
	Executable string
	plugin     ToolPlugin
}

func NewToolRegistry(plugins ...ToolPlugin) (*ToolRegistry, error) {
	registry := &ToolRegistry{byID: map[string]ToolPlugin{}}
	for _, plugin := range plugins {
		if err := registry.Register(plugin); err != nil {
			return nil, err
		}
	}

	return registry, nil
}

func NewDefaultToolRegistry() *ToolRegistry {
	registry, err := NewToolRegistry(defaultToolPlugins()...)
	if err != nil {
		panic(err)
	}

	return registry
}

func (r *ToolRegistry) Register(plugin ToolPlugin) error {
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

func (r *ToolRegistry) Plugins() []ToolPlugin {
	if r == nil {
		return nil
	}

	plugins := make([]ToolPlugin, len(r.plugins))
	copy(plugins, r.plugins)
	return plugins
}

func (r *ToolRegistry) Plugin(id string) (ToolPlugin, bool) {
	if r == nil {
		return nil, false
	}

	plugin, ok := r.byID[strings.ToLower(strings.TrimSpace(id))]
	return plugin, ok
}

func (r *ToolRegistry) ValidateEnvironment(environment Environment) error {
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

func (r *ToolRegistry) InstallRequests(environment Environment) []InstallResult {
	if r == nil {
		return nil
	}

	requests := []InstallResult{}
	for _, plugin := range r.plugins {
		version := strings.TrimSpace(plugin.Version(environment))
		if version != "" {
			requests = append(requests, InstallResult{Tool: plugin.ID(), Version: version})
		}
	}

	return requests
}

func (r *ToolRegistry) ResolveDispatchRequest(tool string) (ToolDispatchRequest, error) {
	if r == nil {
		return ToolDispatchRequest{}, fmt.Errorf("tool registry is not configured")
	}

	trimmed := strings.ToLower(strings.TrimSpace(tool))
	for _, plugin := range r.plugins {
		for _, command := range plugin.DispatchCommands() {
			if strings.EqualFold(strings.TrimSpace(command), trimmed) {
				return ToolDispatchRequest{
					ConfigTool: plugin.ID(),
					Executable: strings.ToLower(strings.TrimSpace(command)),
					plugin:     plugin,
				}, nil
			}
		}
	}

	return ToolDispatchRequest{}, fmt.Errorf("unsupported tool %q", tool)
}

func (r *ToolRegistry) InstallCandidates(root, tool, version string) []string {
	plugin, ok := r.Plugin(tool)
	if !ok {
		return nil
	}

	return plugin.InstallCandidates(root, version)
}

func (r *ToolRegistry) DispatchCandidates(root, configTool, executable, version string) []string {
	plugin, ok := r.Plugin(configTool)
	if !ok {
		return nil
	}

	return plugin.DispatchCandidates(root, executable, version)
}

func (r *ToolRegistry) CleanupCommandNames() []string {
	if r == nil {
		return nil
	}

	return uniqueToolCommands(r.plugins, func(plugin ToolPlugin) []string {
		return plugin.CleanupCommands()
	})
}

func (r *ToolRegistry) ActiveCommandNames(environment *Environment) []string {
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

type builtinToolPlugin struct {
	id                 string
	version            func(Environment) string
	validate           func(Environment) error
	installCandidates  func(root, version string) []string
	dispatchCommands   []string
	cleanupCommands    []string
	activeCommands     func(Environment) []string
	dispatchCandidates func(root, executable, version string) []string
	download           func(ToolDownloadContext) error
	postInstall        func(ToolInstallContext) error
}

func (p builtinToolPlugin) ID() string {
	return p.id
}

func (p builtinToolPlugin) Version(environment Environment) string {
	if p.version == nil {
		return ""
	}

	return strings.TrimSpace(p.version(environment))
}

func (p builtinToolPlugin) Validate(environment Environment) error {
	if p.validate != nil {
		return p.validate(environment)
	}

	version := p.Version(environment)
	if version == "" {
		return nil
	}

	return validateVersion(p.id, version)
}

func (p builtinToolPlugin) InstallCandidates(root, version string) []string {
	if p.installCandidates == nil {
		return nil
	}

	return p.installCandidates(root, version)
}

func (p builtinToolPlugin) DispatchCommands() []string {
	return copyStrings(p.dispatchCommands)
}

func (p builtinToolPlugin) CleanupCommands() []string {
	if len(p.cleanupCommands) > 0 {
		return copyStrings(p.cleanupCommands)
	}

	return p.DispatchCommands()
}

func (p builtinToolPlugin) ActiveCommands(environment Environment) []string {
	if p.activeCommands != nil {
		return copyStrings(p.activeCommands(environment))
	}
	if p.Version(environment) == "" {
		return nil
	}

	return p.DispatchCommands()
}

func (p builtinToolPlugin) DispatchCandidates(root, executable, version string) []string {
	if p.dispatchCandidates == nil {
		if strings.EqualFold(strings.TrimSpace(executable), p.id) {
			return p.InstallCandidates(root, version)
		}
		return nil
	}

	return p.dispatchCandidates(root, executable, version)
}

func (p builtinToolPlugin) Download(ctx ToolDownloadContext) error {
	if p.download == nil {
		return fmt.Errorf("unsupported tool %q", p.id)
	}

	return p.download(ctx)
}

func (p builtinToolPlugin) PostInstall(ctx ToolInstallContext) error {
	if p.postInstall == nil {
		return nil
	}

	return p.postInstall(ctx)
}

func defaultToolPlugins() []ToolPlugin {
	return []ToolPlugin{
		phpToolPlugin(),
		composerToolPlugin(),
		nodeJSToolPlugin(),
		nginxToolPlugin(),
		mailpitToolPlugin(),
		mysqlToolPlugin(),
		mariaDBToolPlugin(),
	}
}

func phpToolPlugin() ToolPlugin {
	return builtinToolPlugin{
		id:                toolPHP,
		version:           func(environment Environment) string { return environment.PHPVersion },
		installCandidates: phpInstallCandidates,
		dispatchCommands:  []string{toolPHP},
		download: func(ctx ToolDownloadContext) error {
			return downloadPHP(ctx.Client, ctx.CacheDir, ctx.Version)
		},
		postInstall: func(ctx ToolInstallContext) error {
			extensions := effectivePHPExtensionsForInstall(ctx.Environment)
			if len(extensions) == 0 {
				return nil
			}

			return ctx.Store.configureInstalledPHPExtensions(ctx.Result.Version, extensions)
		},
	}
}

func composerToolPlugin() ToolPlugin {
	return builtinToolPlugin{
		id:                toolComposer,
		version:           func(environment Environment) string { return environment.ComposerVersion },
		installCandidates: composerInstallCandidates,
		dispatchCommands:  []string{toolComposer},
		download: func(ctx ToolDownloadContext) error {
			return downloadComposer(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	}
}

func nodeJSToolPlugin() ToolPlugin {
	return builtinToolPlugin{
		id:                toolNodeJS,
		version:           func(environment Environment) string { return environment.NodeJSVersion },
		installCandidates: nodeJSInstallCandidates,
		dispatchCommands:  []string{toolNode, toolNPM, toolNPX},
		cleanupCommands:   []string{toolNode, toolNPM, toolNPX, toolNodeJS},
		activeCommands: func(environment Environment) []string {
			if strings.TrimSpace(environment.NodeJSVersion) == "" {
				return nil
			}

			return []string{toolNode, toolNPM, toolNPX}
		},
		dispatchCandidates: nodeJSDispatchCandidates,
		download: func(ctx ToolDownloadContext) error {
			return downloadNodeJS(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	}
}

func nginxToolPlugin() ToolPlugin {
	return builtinToolPlugin{
		id:                toolNginx,
		version:           func(environment Environment) string { return environment.NginxVersion },
		installCandidates: nginxInstallCandidates,
		dispatchCommands:  []string{toolNginx},
		download: func(ctx ToolDownloadContext) error {
			return downloadNginx(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	}
}

func mailpitToolPlugin() ToolPlugin {
	return builtinToolPlugin{
		id: toolMailpit,
		version: func(environment Environment) string {
			if environment.Mailpit == nil {
				return ""
			}

			return environment.Mailpit.Version
		},
		validate: func(environment Environment) error {
			return validateMailpitConfig(environment.Mailpit)
		},
		installCandidates: mailpitInstallCandidates,
		dispatchCommands:  []string{toolMailpit},
		download: func(ctx ToolDownloadContext) error {
			return downloadMailpit(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	}
}

func mysqlToolPlugin() ToolPlugin {
	return databaseToolPlugin(toolMySQL)
}

func mariaDBToolPlugin() ToolPlugin {
	return databaseToolPlugin(toolMariaDB)
}

func databaseToolPlugin(tool string) ToolPlugin {
	return builtinToolPlugin{
		id: tool,
		version: func(environment Environment) string {
			if environment.Database != nil && environment.Database.Engine == tool {
				return environment.Database.Version
			}

			return ""
		},
		validate: func(environment Environment) error {
			if environment.Database == nil || environment.Database.Engine != tool {
				return nil
			}

			return validateDatabaseConfig(environment.Database)
		},
		installCandidates: func(root, version string) []string {
			return databaseToolInstallCandidates(tool, filepath.Join(root, tool, version))
		},
		dispatchCommands: []string{tool},
		download: func(ctx ToolDownloadContext) error {
			return downloadDatabaseTool(ctx.Client, ctx.CacheDir, tool, ctx.Version)
		},
	}
}

func phpInstallCandidates(root, version string) []string {
	installDir := filepath.Join(root, toolPHP, version)
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(installDir, "bin", "php.exe"),
			filepath.Join(installDir, "bin", "php.cmd"),
			filepath.Join(installDir, "bin", "php.bat"),
			filepath.Join(installDir, "php.exe"),
			filepath.Join(installDir, "php.cmd"),
			filepath.Join(installDir, "php.bat"),
		}
	}

	return []string{
		filepath.Join(installDir, "bin", "php"),
		filepath.Join(installDir, "php"),
	}
}

func composerInstallCandidates(root, version string) []string {
	installDir := filepath.Join(root, toolComposer, version)
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(installDir, "bin", "composer.cmd"),
			filepath.Join(installDir, "bin", "composer.bat"),
			filepath.Join(installDir, "bin", "composer.exe"),
			filepath.Join(installDir, "bin", "composer.phar"),
			filepath.Join(installDir, "composer.cmd"),
			filepath.Join(installDir, "composer.bat"),
			filepath.Join(installDir, "composer.exe"),
			filepath.Join(installDir, "composer.phar"),
		}
	}

	return []string{
		filepath.Join(installDir, "bin", "composer"),
		filepath.Join(installDir, "bin", "composer.phar"),
		filepath.Join(installDir, "composer"),
		filepath.Join(installDir, "composer.phar"),
	}
}

func nodeJSInstallCandidates(root, version string) []string {
	installDir := filepath.Join(root, toolNodeJS, version)
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(installDir, "node.exe"),
			filepath.Join(installDir, "bin", "node.exe"),
		}
	}

	return []string{
		filepath.Join(installDir, "bin", "node"),
		filepath.Join(installDir, "node"),
	}
}

func nginxInstallCandidates(root, version string) []string {
	installDir := filepath.Join(root, toolNginx, version)
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(installDir, "nginx.exe"),
			filepath.Join(installDir, "sbin", "nginx.exe"),
		}
	}

	return []string{
		filepath.Join(installDir, "sbin", "nginx"),
		filepath.Join(installDir, "nginx"),
	}
}

func mailpitInstallCandidates(root, version string) []string {
	installDir := filepath.Join(root, toolMailpit, version)
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(installDir, "mailpit.exe"),
			filepath.Join(installDir, "bin", "mailpit.exe"),
		}
	}

	return []string{
		filepath.Join(installDir, "mailpit"),
		filepath.Join(installDir, "bin", "mailpit"),
	}
}

func nodeJSDispatchCandidates(root, executable, version string) []string {
	installDir := filepath.Join(root, toolNodeJS, version)
	switch executable {
	case toolNode:
		if runtime.GOOS == "windows" {
			return []string{
				filepath.Join(installDir, "node.cmd"),
				filepath.Join(installDir, "node.exe"),
				filepath.Join(installDir, "bin", "node.cmd"),
				filepath.Join(installDir, "bin", "node.exe"),
			}
		}

		return []string{
			filepath.Join(installDir, "bin", "node"),
			filepath.Join(installDir, "node"),
		}
	case toolNPM, toolNPX:
		if runtime.GOOS == "windows" {
			return []string{
				filepath.Join(installDir, executable+".cmd"),
				filepath.Join(installDir, executable),
				filepath.Join(installDir, "bin", executable+".cmd"),
				filepath.Join(installDir, "bin", executable),
			}
		}

		return []string{
			filepath.Join(installDir, "bin", executable),
			filepath.Join(installDir, executable),
		}
	default:
		return nil
	}
}

func copyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	copied := make([]string, len(values))
	copy(copied, values)
	return copied
}
