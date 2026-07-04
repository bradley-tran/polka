package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"

	"polka/config"
	"polka/tools"
)

// internalEnvironmentName is the reserved environment tracking internal tool
// versions. It starts with an underscore, which validateName rejects for user
// environments, so it can never collide with a project environment.
const internalEnvironmentName = "_internal"

// internalConfigFileName is the environment file inside the global tools
// directory that records (and lets users override) internal tool versions.
const internalConfigFileName = "polka." + internalEnvironmentName + ".yaml"

// internalToolsDir returns the machine-global directory for internally
// provisioned tools, laid out as <dir>/<tool>/<version> like EnvsDir.
func internalToolsDir(projectDir string) string {
	if override := strings.TrimSpace(os.Getenv("POLKA_TOOLS_DIR")); override != "" {
		return filepath.Clean(filepath.FromSlash(override))
	}
	// Derive from the cache override when set so tests that isolate the cache
	// can never leak internal installs into the real user profile.
	if cacheOverride := strings.TrimSpace(os.Getenv("POLKA_CACHE_DIR")); cacheOverride != "" {
		return filepath.Join(filepath.Clean(filepath.FromSlash(cacheOverride)), "internal-tools")
	}

	cacheDir, err := os.UserCacheDir()
	if err == nil {
		return filepath.Join(cacheDir, "polka", "tools")
	}

	return filepath.Join(projectDir, ".polka-tools")
}

// InternalToolsDir returns the global internal tools directory for this store.
func (s Store) InternalToolsDir() string {
	return internalToolsDir(s.ProjectDir)
}

// internalStore returns a Store view of the reserved _internal environment:
// EnvsDir is the global tools dir so the install state, cache extraction, and
// tool resolution helpers operate on it unchanged. BinDir is intentionally
// left as-is and unused: internal tools are never shimmed, and the internal
// code paths never call syncManagedBinaries.
func (s Store) internalStore() Store {
	toolsDir := s.InternalToolsDir()
	internal := s
	internal.EnvsDir = toolsDir
	internal.ConfigFile = filepath.Join(toolsDir, internalConfigFileName)

	return internal
}

// readInternalEnvironment loads the reserved _internal environment from the
// global tools directory. A missing file yields an empty environment.
func readInternalEnvironment(path string) (Environment, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Environment{Name: internalEnvironmentName}, nil
	}
	if err != nil {
		return Environment{}, fmt.Errorf("read internal tools config %s: %w", path, err)
	}

	var environmentFile config.EnvironmentFile
	if len(data) > 0 {
		if err := validateEnvironmentFileSchema(data); err != nil {
			return Environment{}, fmt.Errorf("decode internal tools config %s: %w", path, err)
		}
		if err := yaml.Unmarshal(data, &environmentFile); err != nil {
			return Environment{}, fmt.Errorf("decode internal tools config %s: %w", path, err)
		}
	}

	return config.EnvironmentFileToEnvironment(internalEnvironmentName, environmentFile), nil
}

// writeInternalEnvironment persists the reserved _internal environment so the
// tools directory always records the versions in use; users may edit the file
// to override internal tool versions.
func writeInternalEnvironment(path string, environment Environment) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create internal tools directory: %w", err)
	}

	return writeYAML(path, config.EnvironmentFileFromEnvironment(environment))
}

// environmentWithInternalToolVersion records a tool version on the reserved
// _internal environment. PIE is handled here because the project-facing
// environmentWithInstallRequest deliberately rejects internal-only tools.
func environmentWithInternalToolVersion(environment Environment, tool, version string) Environment {
	if tool == toolPIE {
		environment.PIEVersion = version
		return environment
	}

	return environmentWithInstallRequest(environment, tools.InstallRequest{Tool: tool, Version: version})
}

// internalPHPEnvironment is the synthetic environment used to configure an
// internally installed PHP. It enables a curated broad set of extensions
// (filtered to those available in installDir/ext) so composer create-project
// scaffolders and PIE cover most use cases, always including the TLS
// extensions needed to reach package registries over HTTPS.
func internalPHPEnvironment(tool, version, installDir string) Environment {
	environment := Environment{
		Name:          internalEnvironmentName,
		PHPExtensions: tools.InternalPHPExtensions(installDir),
	}
	if tool == toolPHPZTS {
		environment.PHPZTSVersion = version
	} else {
		environment.PHPVersion = version
	}

	return environment
}

// EnsureInternalTool provisions a tool into the global internal tools
// directory via the shared download cache and returns the resolved executable
// path. An empty version means "use the _internal environment's configured
// version, falling back to the shared default tool version on first use". The
// resolved version is persisted to the _internal environment file so it
// always records what polka uses.
func (s Store) EnsureInternalTool(tool, version string, report func(InstallProgress)) (string, error) {
	normalizedTool := strings.ToLower(strings.TrimSpace(tool))
	registry := s.toolRegistry()
	plugin, ok := registry.Plugin(normalizedTool)
	if !ok {
		return "", fmt.Errorf("unsupported tool %q", tool)
	}

	internal := s.internalStore()
	environment, err := readInternalEnvironment(internal.ConfigFile)
	if err != nil {
		return "", err
	}

	requested := strings.TrimSpace(version)
	if requested == "" {
		requested = registry.InternalVersion(normalizedTool, environment)
	}
	if requested == "" {
		requested = defaultInternalToolVersion(normalizedTool)
	}
	if requested == "" {
		return "", fmt.Errorf("tool %q does not define an internal version", tool)
	}
	if err := validateVersion(normalizedTool, requested); err != nil {
		return "", err
	}

	if registry.InternalVersion(normalizedTool, environment) != requested {
		environment = environmentWithInternalToolVersion(environment, normalizedTool, requested)
		if err := writeInternalEnvironment(internal.ConfigFile, environment); err != nil {
			return "", err
		}
	}

	// Fast path without the lock: the recorded install still resolves on disk.
	if targetPath, ok := internal.resolvedInternalInstall(normalizedTool, requested); ok {
		return targetPath, nil
	}

	// The tools directory is machine-global: polka processes from different
	// projects can provision the same tool concurrently, so serialize per tool.
	lock, err := tools.AcquireDirLock(filepath.Join(internal.EnvsDir, normalizedTool))
	if err != nil {
		return "", err
	}
	defer lock.Release()

	// Re-check under the lock: another process may have finished the install.
	if targetPath, ok := internal.resolvedInternalInstall(normalizedTool, requested); ok {
		return targetPath, nil
	}

	progress := InstallProgress{Index: 1, Total: 1, Tool: normalizedTool, Version: requested}
	if _, _, err := internal.ensureCachedTool(normalizedTool, requested, func(stage InstallProgressStage) {
		emitInstallProgress(report, progress, stage)
	}); err != nil {
		return "", err
	}

	emitInstallProgress(report, progress, InstallProgressInstalling)
	targetPath, err := internal.installToolFromCache(normalizedTool, requested)
	if err != nil {
		return "", err
	}

	// Internal PHP hosts the composer and PIE PHARs; give it a php.ini with
	// the TLS extensions, CA bundle, and OpenSSL config they need. Other
	// internal tools (PHAR files) need no post-install configuration.
	if normalizedTool == toolPHP || normalizedTool == toolPHPZTS {
		emitInstallProgress(report, progress, InstallProgressConfiguring)
		if err := plugin.PostInstall(ToolInstallContext{
			ProjectDir:  s.ProjectDir,
			RootDir:     internal.EnvsDir,
			EnvsDir:     internal.EnvsDir,
			CacheDir:    internal.CacheDir,
			Environment: internalPHPEnvironment(normalizedTool, requested, filepath.Join(internal.EnvsDir, normalizedTool, requested)),
			Result: InstallResult{
				Tool:       normalizedTool,
				Version:    requested,
				TargetPath: targetPath,
			},
		}); err != nil {
			return "", err
		}
	}

	state := internal.readInstallState()
	state.record(normalizedTool, requested)
	if err := internal.writeInstallState(state); err != nil {
		return "", err
	}
	emitInstallProgress(report, progress, InstallProgressInstalled)

	return targetPath, nil
}

// resolvedInternalInstall reports whether the tool version is recorded as
// installed in the global tools directory and its executable still resolves.
func (s Store) resolvedInternalInstall(tool, version string) (string, bool) {
	if !s.readInstallState().has(tool, version) {
		return "", false
	}
	targetPath, err := s.resolveInstalledTool(tool, version)
	if err != nil {
		return "", false
	}

	return targetPath, true
}
