package backend

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"polka/tools"
)

// ToolBuilder compiles a tool into the global cache. Store.Builder overrides it
// in tests, as Store.Downloader does for downloads.
type ToolBuilder interface {
	Build(ctx context.Context, request tools.BuildRequest) (tools.BuildResult, error)
}

// registryToolBuilder compiles through the recipes of a tool registry.
type registryToolBuilder struct {
	registry *ToolRegistry
}

// Build runs the tool's recipe.
func (b registryToolBuilder) Build(ctx context.Context, request tools.BuildRequest) (tools.BuildResult, error) {
	return tools.BuildTool(ctx, b.registry, request)
}

// toolBuilder returns the configured builder or the registry's recipes.
func (s Store) toolBuilder() ToolBuilder {
	if s.Builder != nil {
		return s.Builder
	}

	return registryToolBuilder{registry: s.toolRegistry()}
}

// BuildTool compiles a tool into the machine-global cache under the requested
// label, where any project's install finds it. It needs no project config, so it
// works from any directory. When a rebuild replaces a payload, the record of the
// internal install is cleared, because internal tools have no --force to pick up
// the new build with.
func (s Store) BuildTool(ctx context.Context, request tools.BuildRequest) (tools.BuildResult, error) {
	request.CacheDir = s.CacheDir
	result, err := s.toolBuilder().Build(ctx, request)
	if err != nil {
		return tools.BuildResult{}, err
	}
	if result.Replaced {
		internal := s.internalStore()
		state := internal.readInstallState()
		if state.has(result.Tool, result.RequestedVersion) {
			state.forget(result.Tool, result.RequestedVersion)
			if err := internal.writeInstallState(state); err != nil {
				return result, fmt.Errorf("update internal install state: %w", err)
			}
		}
	}

	return result, nil
}

// BuildLogDir returns the directory holding compile logs for this store's cache.
func (s Store) BuildLogDir() string {
	return tools.BuildLogDir(s.CacheDir)
}

// InternalBuildRequest reports the internal PHP that `polka install --build`
// compiles for the environment, so a progress display can show it. PIE-managed
// extensions are installed by internal PHP, which never compiles on demand.
func (s Store) InternalBuildRequest(environment Environment) (InstallRequest, bool, error) {
	if len(environment.PIEExtensions) == 0 {
		return InstallRequest{}, false, nil
	}
	registry := s.toolRegistry()
	if !registry.Buildable(toolPHP, runtime.GOOS, runtime.GOARCH) {
		return InstallRequest{}, false, nil
	}
	internal := s.internalStore()
	internalEnvironment, err := readInternalEnvironment(internal.ConfigFile)
	if err != nil {
		return InstallRequest{}, false, err
	}
	version := registry.InternalVersion(toolPHP, internalEnvironment)
	if version == "" {
		version = defaultInternalToolVersion(toolPHP)
	}
	if _, ok := internal.resolvedInternalInstall(toolPHP, version); ok {
		return InstallRequest{}, false, nil
	}

	return InstallRequest{Tool: toolPHP, Version: version}, true, nil
}

// buildMissingTools is the serial pass `polka install --build` runs before the
// parallel install layers. Each tool that would need a payload, has none cached,
// and has a recipe is compiled here, one at a time, so the install goroutines
// only ever see cache hits. Layer order does not matter, because recipes use
// host toolchains rather than other managed tools. It returns the labels it
// built, keyed by tool and version.
func (s Store) buildMissingTools(environment Environment, requests []tools.InstallRequest, options InstallOptions, force bool, report func(InstallProgress)) (map[string]bool, error) {
	built := map[string]bool{}
	if !options.Build {
		return built, nil
	}

	registry := s.toolRegistry()
	layers, err := registry.InstallRequestLayers(environment, requests)
	if err != nil {
		return nil, err
	}
	pending := []tools.InstallRequest{}
	for _, layer := range layers {
		pending = append(pending, layer...)
	}
	state := s.readInstallState()
	for index, request := range pending {
		if !force && state.has(request.Tool, request.Version) {
			if _, err := s.resolveInstalledTool(request.Tool, request.Version); err == nil {
				continue
			}
		}
		compiled, err := s.buildIfMissing(request, options, InstallProgress{Index: index + 1, Total: len(pending), Tool: request.Tool, Version: request.Version}, report)
		if err != nil {
			return nil, err
		}
		if compiled {
			built[toolKey(request.Tool, request.Version)] = true
		}
	}

	internalRequest, needsInternal, err := s.InternalBuildRequest(environment)
	if err != nil {
		return nil, err
	}
	if needsInternal {
		if _, err := s.buildIfMissing(tools.InstallRequest{Tool: internalRequest.Tool, Version: internalRequest.Version}, options, InstallProgress{Index: 1, Total: 1, Tool: internalRequest.Tool, Version: internalRequest.Version}, report); err != nil {
			return nil, err
		}
	}

	return built, nil
}

// buildIfMissing compiles one label unless the cache already holds it or the
// tool has no recipe here, in which case the normal download path applies.
func (s Store) buildIfMissing(request tools.InstallRequest, options InstallOptions, progress InstallProgress, report func(InstallProgress)) (bool, error) {
	if !s.toolRegistry().Buildable(request.Tool, runtime.GOOS, runtime.GOARCH) {
		return false, nil
	}
	if _, err := tools.CachedToolPayload(s.CacheDir, request.Tool, request.Version); err == nil {
		return false, nil
	} else if !tools.IsCacheMiss(err) {
		return false, err
	}

	emitInstallProgress(report, progress, InstallProgressBuilding)
	ctx := context.Background()
	if options.BuildContext != nil {
		buildContext, release := options.BuildContext()
		defer release()
		ctx = buildContext
	}
	if _, err := s.BuildTool(ctx, tools.BuildRequest{Tool: request.Tool, Version: request.Version, Warn: options.Warn}); err != nil {
		return false, fmt.Errorf("build %s %s: %w", request.Tool, request.Version, err)
	}

	return true, nil
}

// markBuiltResults flags results whose payload this install compiled, so output
// does not describe them as cached.
func markBuiltResults(results []InstallResult, built map[string]bool) {
	for index := range results {
		if built[toolKey(results[index].Tool, results[index].Version)] {
			results[index].Built = true
		}
	}
}

// toolKey identifies a tool version in the set of labels an install compiled.
func toolKey(tool, version string) string {
	return strings.ToLower(strings.TrimSpace(tool)) + "@" + strings.TrimSpace(version)
}
