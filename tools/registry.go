package tools

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"sort"
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

// InstallResult describes the outcome of installing (or skipping) one tool.
// Skipped is true when the tool was already installed at the requested version
// and the payload extraction was bypassed; CachePath is empty and Downloaded is
// false in that case.
type InstallResult struct {
	Tool       string
	Version    string
	CachePath  string
	TargetPath string
	Downloaded bool
	Skipped    bool
	// Built reports that this install compiled the payload from source.
	Built bool
}

const (
	LogLevelInfo  = "info"
	LogLevelError = "error"
	LogLevelDebug = "debug"
)

// LogEntry describes one project-local log file produced by a managed tool.
type LogEntry struct {
	Path  string
	Level string
}

// ToolPlugin describes an installable managed tool and its command dispatch behavior.
type ToolPlugin interface {
	ID() string
	Version(config.Environment) string
	PHPExtensions() map[string]bool
	Validate(config.Environment) error
	InstallCandidates(root, version string) []string
	DispatchCommands() []string
	CleanupCommands() []string
	ActiveCommands(config.Environment) []string
	DispatchCandidates(root, executable, version string) []string
	Logs() []LogEntry
	Download(DownloadContext) error
	PostInstall(InstallContext) error
}

// ToolDependencyProvider is implemented by tools that require other managed
// payloads to be installed before they can be used.
type ToolDependencyProvider interface {
	Dependencies(config.Environment) ([]InstallRequest, error)
}

// ToolSettingsProvider is an optional interface implemented by ToolPlugins that
// declare versionless settings or mutual-exclusion peers.
type ToolSettingsProvider interface {
	Settings() map[string]SettingType
	SettingsNamespace() string
	ExclusiveWith() []string
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

// LogRequest describes the tool plugin selected for log resolution.
type LogRequest struct {
	ConfigTool string
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

// SettingsSchema returns the scalar settings schema declared for namespace and
// the ordered list of tool IDs that may satisfy the tool version requirement
// for that namespace (for example "php" -> ["php", "php-zts"]).
func (r *Registry) SettingsSchema(namespace string) (map[string]SettingType, []string, bool) {
	if r == nil {
		return nil, nil, false
	}
	target := strings.ToLower(strings.TrimSpace(namespace))
	if target == "" {
		return nil, nil, false
	}

	var schema map[string]SettingType
	owners := []string{}
	seenOwners := map[string]bool{}
	for _, plugin := range r.plugins {
		if r.InternalOnly(plugin.ID()) {
			continue
		}
		provider, ok := plugin.(ToolSettingsProvider)
		if !ok {
			continue
		}
		pluginID := strings.ToLower(strings.TrimSpace(plugin.ID()))
		pluginSettings := normalizeManifestSettings(provider.Settings())
		pluginNamespace := strings.ToLower(strings.TrimSpace(provider.SettingsNamespace()))
		if pluginNamespace == "" && len(pluginSettings) > 0 {
			pluginNamespace = pluginID
		}
		if pluginNamespace != target {
			continue
		}
		if len(pluginSettings) > 0 && len(schema) == 0 {
			schema = pluginSettings
		}
		if !seenOwners[pluginID] {
			seenOwners[pluginID] = true
			owners = append(owners, pluginID)
		}
	}
	if len(schema) == 0 {
		return nil, nil, false
	}

	return copySettingTypes(schema), owners, true
}

// ExclusivePeers returns the tool IDs that cannot be configured alongside id.
func (r *Registry) ExclusivePeers(id string) []string {
	if r == nil {
		return nil
	}
	target := strings.ToLower(strings.TrimSpace(id))
	if target == "" {
		return nil
	}

	peers := []string{}
	seen := map[string]bool{}
	addPeer := func(peer string) {
		normalized := strings.ToLower(strings.TrimSpace(peer))
		if normalized == "" || normalized == target || seen[normalized] {
			return
		}
		seen[normalized] = true
		peers = append(peers, normalized)
	}

	if plugin, ok := r.Plugin(target); ok {
		if provider, ok := plugin.(ToolSettingsProvider); ok {
			for _, peer := range provider.ExclusiveWith() {
				addPeer(peer)
			}
		}
	}
	for _, plugin := range r.plugins {
		provider, ok := plugin.(ToolSettingsProvider)
		if !ok {
			continue
		}
		pluginID := strings.ToLower(strings.TrimSpace(plugin.ID()))
		for _, peer := range provider.ExclusiveWith() {
			if strings.EqualFold(strings.TrimSpace(peer), target) {
				addPeer(pluginID)
			}
		}
	}
	if len(peers) == 0 {
		return nil
	}

	return peers
}

func (r *Registry) ValidateEnvironment(environment config.Environment) error {
	if r == nil {
		return fmt.Errorf("tool registry is not configured")
	}

	for _, plugin := range r.plugins {
		if strings.TrimSpace(plugin.Version(environment)) != "" {
			for _, peerID := range r.ExclusivePeers(plugin.ID()) {
				if peer, ok := r.Plugin(peerID); ok && strings.TrimSpace(peer.Version(environment)) != "" {
					return fmt.Errorf("%s and %s versions are mutually exclusive", plugin.ID(), peer.ID())
				}
			}
		}
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

// InstallRequestLayers returns dependency-ordered installation layers. Tools
// in one layer are independent and may be installed concurrently.
func (r *Registry) InstallRequestLayers(environment config.Environment, roots []InstallRequest) ([][]InstallRequest, error) {
	if r == nil {
		return nil, fmt.Errorf("tool registry is not configured")
	}
	if roots == nil {
		roots = r.InstallRequests(environment)
	}

	requests := map[string]InstallRequest{}
	order := map[string]int{}
	nextOrder := 0
	dependencies := map[string][]string{}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(InstallRequest) error
	visit = func(request InstallRequest) error {
		id := strings.ToLower(strings.TrimSpace(request.Tool))
		if id == "" || strings.TrimSpace(request.Version) == "" {
			return fmt.Errorf("install dependency requires tool and version")
		}
		if visiting[id] {
			return fmt.Errorf("managed tool dependency cycle includes %q", id)
		}
		if previous, ok := requests[id]; ok && previous.Version != request.Version {
			return fmt.Errorf("managed tool %q requires conflicting versions %q and %q", id, previous.Version, request.Version)
		}
		requests[id] = InstallRequest{Tool: id, Version: strings.TrimSpace(request.Version)}
		if _, ok := order[id]; !ok {
			order[id] = nextOrder
			nextOrder++
		}
		if visited[id] {
			return nil
		}
		plugin, ok := r.Plugin(id)
		if !ok {
			return fmt.Errorf("unsupported tool dependency %q", id)
		}
		visiting[id] = true
		if provider, ok := plugin.(ToolDependencyProvider); ok {
			requests, err := provider.Dependencies(environment)
			if err != nil {
				return err
			}
			for _, dependency := range requests {
				dependencyID := strings.ToLower(strings.TrimSpace(dependency.Tool))
				dependencies[id] = append(dependencies[id], dependencyID)
				if err := visit(dependency); err != nil {
					return err
				}
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for _, root := range roots {
		if err := visit(root); err != nil {
			return nil, err
		}
	}

	remaining := map[string]bool{}
	for id := range requests {
		remaining[id] = true
	}
	layers := [][]InstallRequest{}
	for len(remaining) > 0 {
		layer := []InstallRequest{}
		for id := range remaining {
			ready := true
			for _, dependency := range dependencies[id] {
				if remaining[dependency] {
					ready = false
					break
				}
			}
			if ready {
				layer = append(layer, requests[id])
			}
		}
		if len(layer) == 0 {
			return nil, fmt.Errorf("managed tool dependency cycle detected")
		}
		sort.Slice(layer, func(i, j int) bool { return order[layer[i].Tool] < order[layer[j].Tool] })
		for _, request := range layer {
			delete(remaining, request.Tool)
		}
		layers = append(layers, layer)
	}
	return layers, nil
}

// PHPExtensions returns the union of extensions required by configured tools.
func (r *Registry) PHPExtensions(environment config.Environment) map[string]bool {
	if r == nil {
		return nil
	}

	extensions := map[string]bool{}
	for _, plugin := range r.plugins {
		if plugin.Version(environment) == "" {
			continue
		}
		for name, enabled := range plugin.PHPExtensions() {
			if enabled {
				extensions[name] = true
			}
		}
	}
	if len(extensions) == 0 {
		return nil
	}
	return extensions
}

// internalToolInfo is implemented by plugins that support global internal
// installs. Plugins without it are treated as regular project-only tools.
type internalToolInfo interface {
	InternalOnly() bool
	InternalVersion(config.Environment) string
}

// InternalOnly reports whether the tool is reserved for polka-internal use and
// cannot be configured in a project environment.
func (r *Registry) InternalOnly(id string) bool {
	plugin, ok := r.Plugin(id)
	if !ok {
		return false
	}
	info, ok := plugin.(internalToolInfo)

	return ok && info.InternalOnly()
}

// InternalVersion resolves the tool version configured in the reserved
// _internal environment, falling back to the plugin's public Version func for
// plugins without internal metadata.
func (r *Registry) InternalVersion(id string, environment config.Environment) string {
	plugin, ok := r.Plugin(id)
	if !ok {
		return ""
	}
	if info, ok := plugin.(internalToolInfo); ok {
		return info.InternalVersion(environment)
	}

	return strings.TrimSpace(plugin.Version(environment))
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

// ResolveDispatchRequestForEnvironment selects the configured provider when
// multiple plugins expose the same command, such as php and php-zts.
func (r *Registry) ResolveDispatchRequestForEnvironment(tool string, environment config.Environment) (DispatchRequest, error) {
	if r == nil {
		return DispatchRequest{}, fmt.Errorf("tool registry is not configured")
	}

	trimmed := strings.ToLower(strings.TrimSpace(tool))
	var fallback *DispatchRequest
	for _, plugin := range r.plugins {
		for _, command := range plugin.DispatchCommands() {
			if strings.EqualFold(strings.TrimSpace(command), trimmed) {
				request := DispatchRequest{
					ConfigTool: plugin.ID(),
					Executable: strings.ToLower(strings.TrimSpace(command)),
					Plugin:     plugin,
				}
				if strings.TrimSpace(plugin.Version(environment)) != "" {
					return request, nil
				}
				if fallback == nil {
					fallback = &request
				}
			}
		}
	}
	if fallback != nil {
		return *fallback, nil
	}

	return DispatchRequest{}, fmt.Errorf("unsupported tool %q", tool)
}

// ResolveLogRequest resolves either a manifest tool ID or a dispatch command
// to the plugin that declares log files for that tool.
func (r *Registry) ResolveLogRequest(tool string) (LogRequest, error) {
	if r == nil {
		return LogRequest{}, fmt.Errorf("tool registry is not configured")
	}

	trimmed := strings.ToLower(strings.TrimSpace(tool))
	if plugin, ok := r.Plugin(trimmed); ok {
		return LogRequest{ConfigTool: plugin.ID(), Plugin: plugin}, nil
	}

	request, err := r.ResolveDispatchRequest(trimmed)
	if err != nil {
		return LogRequest{}, err
	}

	return LogRequest{ConfigTool: request.ConfigTool, Plugin: request.Plugin}, nil
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
	build              func(context.Context, BuildRequest) (BuildResult, error)
	buildSupports      func(goos, goarch string) bool
	version            func(config.Environment) string
	internalOnly       bool
	internalVersion    func(config.Environment) string
	versionConstraint  manifestVersionConstraint
	wrappers           map[string]manifestWrapper
	executablePaths    func(installDir string) []string
	settings           map[string]SettingType
	settingsNamespace  string
	exclusiveWith      []string
	phpExtensions      map[string]bool
	validate           func(config.Environment) error
	installCandidates  func(root, version string) []string
	dispatchCommands   []string
	cleanupCommands    []string
	activeCommands     func(config.Environment) []string
	dispatchCandidates func(root, executable, version string) []string
	logs               []LogEntry
	download           func(DownloadContext) error
	postInstall        func(InstallContext) error
	dependencies       func(config.Environment) ([]InstallRequest, error)
}

// Dependencies returns managed payloads that must precede this tool.
func (p builtinPlugin) Dependencies(environment config.Environment) ([]InstallRequest, error) {
	if p.dependencies == nil {
		return nil, nil
	}
	requests, err := p.dependencies(environment)
	if err != nil {
		return nil, err
	}

	return append([]InstallRequest(nil), requests...), nil
}

func (p builtinPlugin) ID() string {
	return p.id
}

// InternalOnly reports whether the tool may only be provisioned internally by
// polka and never configured in a project environment.
func (p builtinPlugin) InternalOnly() bool {
	return p.internalOnly
}

// InternalVersion resolves the tool version from the reserved _internal
// environment using the raw manifest version mapping, bypassing the
// internal-only wrapper that neutralizes the public Version func.
func (p builtinPlugin) InternalVersion(environment config.Environment) string {
	if p.internalVersion == nil {
		return ""
	}

	return strings.TrimSpace(p.internalVersion(environment))
}

// Settings returns a defensive copy of the manifest-declared settings schema.
func (p builtinPlugin) Settings() map[string]SettingType {
	return copySettingTypes(p.settings)
}

// SettingsNamespace returns the settings.<namespace> key owned or shared by
// this plugin.
func (p builtinPlugin) SettingsNamespace() string {
	return p.settingsNamespace
}

// ExclusiveWith returns a defensive copy of mutually exclusive tool IDs.
func (p builtinPlugin) ExclusiveWith() []string {
	return copyStrings(p.exclusiveWith)
}

func (p builtinPlugin) Version(environment config.Environment) string {
	if p.version == nil {
		return ""
	}

	return strings.TrimSpace(p.version(environment))
}

// PHPExtensions returns a defensive copy of the extensions required by the tool.
func (p builtinPlugin) PHPExtensions() map[string]bool {
	if len(p.phpExtensions) == 0 {
		return nil
	}

	extensions := make(map[string]bool, len(p.phpExtensions))
	for name, enabled := range p.phpExtensions {
		extensions[name] = enabled
	}
	return extensions
}

func (p builtinPlugin) Validate(environment config.Environment) error {
	if err := p.validateManifestRules(environment); err != nil {
		return err
	}
	if p.validate != nil {
		return p.validate(environment)
	}

	return nil
}

func (p builtinPlugin) validateManifestRules(environment config.Environment) error {
	version := p.Version(environment)
	hasSettings := p.hasConfiguredSettings(environment)
	if version == "" && !hasSettings {
		return nil
	}
	if version == "" && !p.hasExclusivePeerVersion(environment) {
		return fmt.Errorf("%s configuration requires version", p.id)
	}
	if version != "" {
		if err := validateVersion(p.id, version); err != nil {
			return err
		}
		if !p.versionConstraint.allowsLabel(version) {
			return p.versionConstraint.error(p.id, version)
		}
	}
	if len(p.settings) > 0 && p.settingsNamespace != "" {
		keys := make([]string, 0, len(p.settings))
		for key := range p.settings {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if p.settings[key] != SettingTypePort {
				continue
			}
			port := environment.SettingInt(p.settingsNamespace, key)
			if port != 0 && !validPort(port) {
				return fmt.Errorf("%s %s must be between 1 and 65535", p.settingsNamespace, key)
			}
		}
	}

	return nil
}

func (p builtinPlugin) hasConfiguredSettings(environment config.Environment) bool {
	return settingsConfigured(environment, p.settingsNamespace, p.settings)
}

// settingsConfigured reports whether any setting declared in schema has a
// non-zero value under settings.<namespace> in environment.
func settingsConfigured(environment config.Environment, namespace string, schema map[string]SettingType) bool {
	if len(schema) == 0 || namespace == "" {
		return false
	}
	for key, settingType := range schema {
		switch settingType {
		case SettingTypePort:
			if environment.SettingInt(namespace, key) != 0 {
				return true
			}
		case SettingTypeString:
			if environment.SettingString(namespace, key) != "" {
				return true
			}
		case SettingTypeBool:
			if environment.SettingBool(namespace, key) {
				return true
			}
		}
	}

	return false
}

func (p builtinPlugin) hasExclusivePeerVersion(environment config.Environment) bool {
	for _, peer := range p.exclusiveWith {
		if strings.TrimSpace(environment.ToolVersion(peer)) != "" {
			return true
		}
	}

	return false
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

func (p builtinPlugin) Logs() []LogEntry {
	return copyLogEntries(p.logs)
}

func (p builtinPlugin) Download(ctx DownloadContext) error {
	if p.download == nil {
		return fmt.Errorf("unsupported tool %q", p.id)
	}

	return p.download(ctx)
}

// Buildable reports whether the plugin has a compile recipe for the platform.
func (p builtinPlugin) Buildable(goos, goarch string) bool {
	return p.build != nil && p.buildSupports != nil && p.buildSupports(goos, goarch)
}

// Build compiles the tool through its recipe.
func (p builtinPlugin) Build(ctx context.Context, request BuildRequest) (BuildResult, error) {
	if p.build == nil {
		return BuildResult{}, fmt.Errorf("%s has no build recipe", p.id)
	}

	return p.build(ctx, request)
}

func (p builtinPlugin) PostInstall(ctx InstallContext) error {
	installDir := resolvePostInstallDir(p.id, ctx)
	writtenWrappers, err := writeManifestWrappers(installDir, p.wrappers, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	execPaths := []string{ctx.Result.TargetPath}
	if p.executablePaths != nil && installDir != "" {
		execPaths = append(execPaths, p.executablePaths(installDir)...)
	}
	execPaths = append(execPaths, writtenWrappers...)
	if err := ensureInstalledExecutables(execPaths); err != nil {
		return err
	}

	if p.postInstall == nil {
		return nil
	}

	return p.postInstall(ctx)
}

func copySettingTypes(values map[string]SettingType) map[string]SettingType {
	if len(values) == 0 {
		return nil
	}

	copied := make(map[string]SettingType, len(values))
	for key, value := range values {
		copied[key] = value
	}

	return copied
}

func copyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	copied := make([]string, len(values))
	copy(copied, values)
	return copied
}

func copyLogEntries(values []LogEntry) []LogEntry {
	if len(values) == 0 {
		return nil
	}

	copied := make([]LogEntry, len(values))
	copy(copied, values)
	return copied
}

// NormalizeLogLevel returns a canonical log level value for comparison.
func NormalizeLogLevel(level string) string {
	return strings.ToLower(strings.TrimSpace(level))
}

// ValidLogLevel reports whether level is one of the manifest-supported log levels.
func ValidLogLevel(level string) bool {
	switch NormalizeLogLevel(level) {
	case LogLevelInfo, LogLevelError, LogLevelDebug:
		return true
	default:
		return false
	}
}
