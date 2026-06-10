package plugins

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"polka/tools"
)

const (
	TypeTool      = "tool"
	TypeFramework = "framework"
)

var validPluginID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Registry owns built-in Polka plugins, grouped by plugin type.
type Registry struct {
	tools          *tools.Registry
	frameworks     []FrameworkPlugin
	frameworksByID map[string]FrameworkPlugin
}

// NewRegistry builds a plugin registry from an installable tool registry and framework plugins.
func NewRegistry(toolRegistry *tools.Registry, frameworkPlugins ...FrameworkPlugin) (*Registry, error) {
	if toolRegistry == nil {
		toolRegistry = tools.NewDefaultRegistry()
	}
	registry := &Registry{
		tools:          toolRegistry,
		frameworksByID: map[string]FrameworkPlugin{},
	}
	for _, plugin := range frameworkPlugins {
		if err := registry.RegisterFramework(plugin); err != nil {
			return nil, err
		}
	}

	return registry, nil
}

// NewDefaultRegistry returns Polka's built-in tool and framework plugin registry.
func NewDefaultRegistry() *Registry {
	registry, err := NewRegistry(tools.NewDefaultRegistry(), DefaultFrameworkPlugins()...)
	if err != nil {
		panic(err)
	}

	return registry
}

// ToolRegistry returns the installable tool registry.
func (r *Registry) ToolRegistry() *tools.Registry {
	if r == nil || r.tools == nil {
		return tools.NewDefaultRegistry()
	}

	return r.tools
}

// RegisterFramework adds a framework plugin to the registry.
func (r *Registry) RegisterFramework(plugin FrameworkPlugin) error {
	if plugin == nil {
		return fmt.Errorf("framework plugin cannot be nil")
	}
	if r.frameworksByID == nil {
		r.frameworksByID = map[string]FrameworkPlugin{}
	}

	id := strings.ToLower(strings.TrimSpace(plugin.ID()))
	if id == "" {
		return fmt.Errorf("framework plugin id cannot be empty")
	}
	if !validPluginID.MatchString(id) {
		return fmt.Errorf("invalid framework plugin id %q: use letters, numbers, dots, dashes, or underscores", plugin.ID())
	}
	if _, exists := r.frameworksByID[id]; exists {
		return fmt.Errorf("duplicate framework plugin %q", id)
	}

	r.frameworks = append(r.frameworks, plugin)
	r.frameworksByID[id] = plugin
	return nil
}

// Framework resolves a framework plugin by ID.
func (r *Registry) Framework(id string) (FrameworkPlugin, bool) {
	if r == nil {
		return nil, false
	}

	plugin, ok := r.frameworksByID[strings.ToLower(strings.TrimSpace(id))]
	return plugin, ok
}

// Frameworks returns registered framework plugins in registration order.
func (r *Registry) Frameworks() []FrameworkPlugin {
	if r == nil {
		return nil
	}

	plugins := make([]FrameworkPlugin, len(r.frameworks))
	copy(plugins, r.frameworks)
	return plugins
}

// SupportedFrameworks returns framework IDs in stable sorted order.
func (r *Registry) SupportedFrameworks() []string {
	plugins := r.Frameworks()
	ids := make([]string, 0, len(plugins))
	for _, plugin := range plugins {
		ids = append(ids, strings.ToLower(strings.TrimSpace(plugin.ID())))
	}
	sort.Strings(ids)

	return ids
}

// ValidateFramework checks whether a non-empty framework ID is supported.
func (r *Registry) ValidateFramework(id string) error {
	trimmed := strings.ToLower(strings.TrimSpace(id))
	if trimmed == "" {
		return nil
	}
	if _, ok := r.Framework(trimmed); !ok {
		return fmt.Errorf("unsupported framework %q; supported frameworks: %s", id, strings.Join(r.SupportedFrameworks(), ", "))
	}

	return nil
}
