package tools

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// goToolHooks lists the tools that still need Go hooks, for work manifests and
// scripts cannot express. Every other tool is defined entirely by its manifest.
func goToolHooks() map[string]pluginHooks {
	return map[string]pluginHooks{
		PHP:        phpRuntimeHooks(PHP),
		PHPZTS:     phpRuntimeHooks(PHPZTS),
		FrankenPHP: {postInstall: configureInstalledFrankenPHP},
	}
}

// DefaultPlugins returns a plugin for every embedded tool manifest. A manifest
// is a compile-time asset, so a broken one is a programming error.
func DefaultPlugins() []ToolPlugin {
	plugins, err := discoverManifestPlugins(builtinManifestFS(), loadBuiltinManifest, goToolHooks())
	if err != nil {
		panic(err)
	}

	return plugins
}

// discoverManifestPlugins builds a plugin for every *.yaml manifest in fsys and
// attaches any Go hooks keyed by tool ID. Plugins are ordered by descending
// dispatch-priority, then by ID, because the registry order decides which tool
// provides a command several tools share.
func discoverManifestPlugins(fsys fs.FS, load func(name string) (pluginManifest, error), hooks map[string]pluginHooks) ([]ToolPlugin, error) {
	files, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		return nil, fmt.Errorf("list tool manifests: %w", err)
	}

	type discovered struct {
		id       string
		priority int
		plugin   ToolPlugin
	}
	tools := make([]discovered, 0, len(files))
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		name := strings.TrimSuffix(file, ".yaml")
		manifest, err := load(name)
		if err != nil {
			return nil, err
		}
		// Manifests find each other by file name, as settings-from does.
		id := strings.ToLower(strings.TrimSpace(manifest.ID))
		if id != name {
			return nil, fmt.Errorf("tool manifest %s must declare id %q, got %q", file, name, manifest.ID)
		}
		plugin, err := manifest.toPlugin(hooks[id])
		if err != nil {
			return nil, err
		}
		tools = append(tools, discovered{id: id, priority: manifest.DispatchPriority, plugin: plugin})
		seen[id] = true
	}
	for id := range hooks {
		if !seen[id] {
			return nil, fmt.Errorf("go hooks reference tool %q, which has no manifest", id)
		}
	}

	sort.SliceStable(tools, func(left, right int) bool {
		if tools[left].priority != tools[right].priority {
			return tools[left].priority > tools[right].priority
		}
		return tools[left].id < tools[right].id
	})
	plugins := make([]ToolPlugin, 0, len(tools))
	for _, tool := range tools {
		plugins = append(plugins, tool.plugin)
	}

	return plugins, nil
}
