package tools

import (
	"embed"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/goccy/go-yaml"

	"polka/config"
)

//go:embed manifests/*.yaml
var builtinManifestFiles embed.FS

type pluginHooks struct {
	validate    func(config.Environment) error
	download    func(DownloadContext) error
	postInstall func(InstallContext) error
}

type pluginManifest struct {
	ID                 string                           `yaml:"id"`
	PHPExtensions      []string                         `yaml:"php-extensions"`
	InstallCandidates  manifestPlatformPaths            `yaml:"install-candidates"`
	DispatchCommands   []string                         `yaml:"dispatch-commands"`
	CleanupCommands    []string                         `yaml:"cleanup-commands"`
	ActiveCommands     []string                         `yaml:"active-commands"`
	DispatchCandidates map[string]manifestPlatformPaths `yaml:"dispatch-candidates"`
	Logs               manifestLogs                     `yaml:"logs"`
	Download           manifestDownload                 `yaml:"download"`
}

type manifestPlatformPaths map[string][]string

type manifestLogs map[string][]string

type manifestDownload struct {
	GitHub       manifestGitHubDownload       `yaml:"github"`
	ReleaseIndex manifestReleaseIndexDownload `yaml:"release-index"`
	Assets       map[string]downloadAsset     `yaml:"assets"`
	Catalog      map[string]map[string]any    `yaml:"catalog"`
}

type manifestGitHubDownload struct {
	Owner     string `yaml:"owner"`
	Repo      string `yaml:"repo"`
	TagPrefix string `yaml:"tag-prefix"`
}

type manifestReleaseIndexDownload struct {
	URL     string `yaml:"url"`
	Pattern string `yaml:"pattern"`
}

type downloadAsset struct {
	FileName          string            `yaml:"filename"`
	SourceFileName    string            `yaml:"source-filename"`
	URL               string            `yaml:"url"`
	InstallPath       string            `yaml:"install-path"`
	Checksum          string            `yaml:"checksum"`
	ChecksumURL       string            `yaml:"checksum-url"`
	ChecksumAlgorithm checksumAlgorithm `yaml:"checksum-algorithm"`
	ArchiveFormat     archiveFormat     `yaml:"archive-format"`
}

type databaseDownloadAsset = downloadAsset

func newManifestPlugin(name string, hooks pluginHooks) Plugin {
	manifest, err := loadBuiltinManifest(name)
	if err != nil {
		panic(err)
	}
	plugin, err := manifest.toPlugin(hooks)
	if err != nil {
		panic(err)
	}

	return plugin
}

func loadBuiltinManifest(name string) (pluginManifest, error) {
	path := "manifests/" + strings.TrimSpace(name) + ".yaml"
	data, err := builtinManifestFiles.ReadFile(path)
	if err != nil {
		return pluginManifest{}, fmt.Errorf("read builtin tool manifest %s: %w", path, err)
	}

	return parsePluginManifest(data)
}

func parsePluginManifest(data []byte) (pluginManifest, error) {
	var manifest pluginManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return pluginManifest{}, fmt.Errorf("parse tool manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return pluginManifest{}, err
	}
	manifest.PHPExtensions = normalizeManifestPHPExtensions(manifest.PHPExtensions)

	return manifest, nil
}

func (m pluginManifest) validate() error {
	id := strings.ToLower(strings.TrimSpace(m.ID))
	if id == "" {
		return fmt.Errorf("tool manifest id cannot be empty")
	}
	if !validName.MatchString(id) {
		return fmt.Errorf("invalid tool manifest id %q: use letters, numbers, dots, dashes, or underscores", m.ID)
	}
	for index, extension := range m.PHPExtensions {
		if err := validatePHPExtensionName(extension); err != nil {
			return fmt.Errorf("tool manifest %q php-extensions[%d]: %w", id, index, err)
		}
	}
	if len(m.InstallCandidates) == 0 {
		return fmt.Errorf("tool manifest %q requires install-candidates", id)
	}
	if err := validateManifestPlatformPaths(id, "install-candidates", m.InstallCandidates); err != nil {
		return err
	}
	for command, paths := range m.DispatchCandidates {
		if strings.TrimSpace(command) == "" {
			return fmt.Errorf("tool manifest %q has empty dispatch-candidates command", id)
		}
		if err := validateManifestPlatformPaths(id, "dispatch-candidates."+command, paths); err != nil {
			return err
		}
	}
	for level, paths := range m.Logs {
		normalizedLevel := NormalizeLogLevel(level)
		if normalizedLevel == "" {
			return fmt.Errorf("tool manifest %q logs has empty level", id)
		}
		if !ValidLogLevel(normalizedLevel) {
			return fmt.Errorf("tool manifest %q logs has unsupported level %q", id, level)
		}
		if len(paths) == 0 {
			return fmt.Errorf("tool manifest %q logs.%s requires at least one path", id, normalizedLevel)
		}
		for index, path := range paths {
			if strings.TrimSpace(path) == "" {
				return fmt.Errorf("tool manifest %q logs.%s[%d] requires path", id, normalizedLevel, index)
			}
			if err := validateManifestLogPath(path); err != nil {
				return fmt.Errorf("tool manifest %q logs.%s[%d]: %w", id, normalizedLevel, index, err)
			}
		}
	}
	if len(m.Download.Catalog) > 0 {
		return fmt.Errorf("tool manifest %q uses deprecated download.catalog; use download.assets", id)
	}
	if m.Download.hasGitHub() {
		if strings.TrimSpace(m.Download.GitHub.Owner) == "" {
			return fmt.Errorf("tool manifest %q download.github requires owner", id)
		}
		if strings.TrimSpace(m.Download.GitHub.Repo) == "" {
			return fmt.Errorf("tool manifest %q download.github requires repo", id)
		}
	}
	if m.Download.hasReleaseIndex() {
		if strings.TrimSpace(m.Download.ReleaseIndex.URL) == "" {
			return fmt.Errorf("tool manifest %q download.release-index requires url", id)
		}
		if strings.TrimSpace(m.Download.ReleaseIndex.Pattern) == "" {
			return fmt.Errorf("tool manifest %q download.release-index requires pattern", id)
		}
		if _, err := regexp.Compile(m.Download.ReleaseIndex.Pattern); err != nil {
			return fmt.Errorf("tool manifest %q download.release-index has invalid pattern: %w", id, err)
		}
	}
	for platform, asset := range m.Download.Assets {
		if !validDownloadPlatform(platform) {
			return fmt.Errorf("tool manifest %q download assets has unsupported platform %q", id, platform)
		}
		if err := validateDownloadAsset(id, platform, asset); err != nil {
			return err
		}
	}

	return nil
}

func (d manifestDownload) hasGitHub() bool {
	return strings.TrimSpace(d.GitHub.Owner) != "" || strings.TrimSpace(d.GitHub.Repo) != ""
}

func (d manifestDownload) hasReleaseIndex() bool {
	return strings.TrimSpace(d.ReleaseIndex.URL) != "" || strings.TrimSpace(d.ReleaseIndex.Pattern) != ""
}

func validateManifestPlatformPaths(tool, field string, paths manifestPlatformPaths) error {
	for platform, candidates := range paths {
		if strings.TrimSpace(platform) == "" {
			return fmt.Errorf("tool manifest %q has empty %s platform", tool, field)
		}
		if len(candidates) == 0 {
			return fmt.Errorf("tool manifest %q %s platform %q has no paths", tool, field, platform)
		}
		for _, candidate := range candidates {
			if err := validateManifestRelativePath(candidate); err != nil {
				return fmt.Errorf("tool manifest %q %s platform %q: %w", tool, field, platform, err)
			}
		}
	}

	return nil
}

func validateManifestRelativePath(candidate string) error {
	return validateManifestContainedPath(candidate, "candidate path", "install directory")
}

func validateManifestLogPath(path string) error {
	return validateManifestContainedPath(path, "log path", "tool log root")
}

func validateManifestContainedPath(candidate, label, rootLabel string) error {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return fmt.Errorf("%s cannot be empty", label)
	}
	if filepath.IsAbs(trimmed) {
		return fmt.Errorf("%s %q must be relative", label, candidate)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(trimmed)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%s %q must stay inside the %s", label, candidate, rootLabel)
	}

	return nil
}

func validateDownloadAsset(tool, platform string, asset downloadAsset) error {
	if strings.TrimSpace(asset.FileName) == "" {
		return fmt.Errorf("tool manifest %q download assets %s requires filename", tool, platform)
	}
	if strings.TrimSpace(asset.URL) == "" {
		return fmt.Errorf("tool manifest %q download assets %s requires url", tool, platform)
	}
	switch asset.ChecksumAlgorithm {
	case checksumAlgorithmNone, checksumAlgorithmMD5, checksumAlgorithmSHA256, checksumAlgorithmSHA3_256:
	default:
		return fmt.Errorf("tool manifest %q download assets %s has unsupported checksum algorithm %q", tool, platform, asset.ChecksumAlgorithm)
	}
	if strings.TrimSpace(asset.InstallPath) != "" {
		if err := validateManifestRelativePath(asset.InstallPath); err != nil {
			return fmt.Errorf("tool manifest %q download assets %s install-path: %w", tool, platform, err)
		}
		if asset.ArchiveFormat != "" {
			return fmt.Errorf("tool manifest %q download assets %s cannot define archive-format with install-path", tool, platform)
		}
	} else {
		switch asset.ArchiveFormat {
		case archiveFormatZip, archiveFormatTarGz, archiveFormatTarXz:
		default:
			return fmt.Errorf("tool manifest %q download assets %s has unsupported archive format %q", tool, platform, asset.ArchiveFormat)
		}
	}
	if strings.TrimSpace(asset.Checksum) != "" {
		return fmt.Errorf("tool manifest %q download assets %s uses deprecated checksum; use checksum-url or resolver-provided checksum", tool, platform)
	}

	return nil
}

func validDownloadPlatform(platform string) bool {
	switch strings.TrimSpace(platform) {
	case "windows-amd64", "linux-amd64", "all":
		return true
	default:
		return false
	}
}

func (m pluginManifest) toPlugin(hooks pluginHooks) (Plugin, error) {
	manifestID := strings.ToLower(strings.TrimSpace(m.ID))
	download := hooks.download
	if download == nil && len(m.Download.Assets) > 0 {
		download = func(ctx DownloadContext) error {
			return downloadManifestAssetForRequest(ctx.Client, ctx.CacheDir, manifestID, ctx.Version, m.Download, runtime.GOOS, runtime.GOARCH)
		}
	}

	return builtinPlugin{
		id:                 manifestID,
		version:            manifestVersionFunc(m),
		phpExtensions:      manifestPHPExtensionMap(m.PHPExtensions),
		validate:           hooks.validate,
		installCandidates:  manifestInstallCandidatesFunc(m),
		dispatchCommands:   normalizeCommands(m.DispatchCommands),
		cleanupCommands:    normalizeCommands(m.CleanupCommands),
		activeCommands:     manifestActiveCommandsFunc(m),
		dispatchCandidates: manifestDispatchCandidatesFunc(m),
		logs:               normalizeLogEntries(m.Logs),
		download:           download,
		postInstall:        hooks.postInstall,
	}, nil
}

func manifestVersionFunc(m pluginManifest) func(config.Environment) string {
	id := strings.ToLower(strings.TrimSpace(m.ID))

	return func(environment config.Environment) string {
		switch id {
		case PHP:
			return environment.PHPVersion
		case PHPZTS:
			return environment.PHPZTSVersion
		case FrankenPHP:
			return environment.FrankenPHPVersion
		case Composer:
			return environment.ComposerVersion
		case PIE:
			return environment.PIEVersion
		case NodeJS:
			return environment.NodeJSVersion
		case Mago:
			return environment.MagoVersion
		case Nginx:
			return environment.NginxVersion
		case Apache:
			return environment.ApacheVersion
		case SQLite:
			return environment.SQLiteVersion
		case Mailpit:
			if environment.Mailpit == nil {
				return ""
			}
			return environment.Mailpit.Version
		case PHPMyAdmin:
			if environment.PHPMyAdmin == nil {
				return ""
			}
			return environment.PHPMyAdmin.Version
		case Meilisearch:
			if environment.Meilisearch == nil {
				return ""
			}
			return environment.Meilisearch.Version
		case MySQL, MariaDB, PostgreSQL:
			return config.DatabaseToolVersion(environment, id)
		default:
			return ""
		}
	}
}

func manifestInstallCandidatesFunc(m pluginManifest) func(root, version string) []string {
	tool := strings.ToLower(strings.TrimSpace(m.ID))
	paths := m.InstallCandidates

	return func(root, version string) []string {
		return manifestCandidatePaths(filepath.Join(root, tool, version), paths, runtime.GOOS, runtime.GOARCH)
	}
}

func manifestActiveCommandsFunc(m pluginManifest) func(config.Environment) []string {
	version := manifestVersionFunc(m)
	activeCommands := normalizeCommands(m.ActiveCommands)
	dispatchCommands := normalizeCommands(m.DispatchCommands)

	return func(environment config.Environment) []string {
		if strings.TrimSpace(version(environment)) == "" {
			return nil
		}
		if len(activeCommands) > 0 {
			return copyStrings(activeCommands)
		}

		return copyStrings(dispatchCommands)
	}
}

func manifestDispatchCandidatesFunc(m pluginManifest) func(root, executable, version string) []string {
	tool := strings.ToLower(strings.TrimSpace(m.ID))
	dispatchCandidates := make(map[string]manifestPlatformPaths, len(m.DispatchCandidates))
	for command, paths := range m.DispatchCandidates {
		dispatchCandidates[strings.ToLower(strings.TrimSpace(command))] = paths
	}
	installPaths := m.InstallCandidates

	return func(root, executable, version string) []string {
		normalizedExecutable := strings.ToLower(strings.TrimSpace(executable))
		installDir := filepath.Join(root, tool, version)
		if paths, ok := dispatchCandidates[normalizedExecutable]; ok {
			return manifestCandidatePaths(installDir, paths, runtime.GOOS, runtime.GOARCH)
		}
		if normalizedExecutable == tool {
			return manifestCandidatePaths(installDir, installPaths, runtime.GOOS, runtime.GOARCH)
		}

		return nil
	}
}

func manifestCandidatePaths(baseDir string, platformPaths manifestPlatformPaths, goos, goarch string) []string {
	relativePaths := selectManifestPlatformPaths(platformPaths, goos, goarch)
	if len(relativePaths) == 0 {
		return nil
	}

	candidates := make([]string, 0, len(relativePaths))
	for _, relativePath := range relativePaths {
		candidates = append(candidates, filepath.Join(baseDir, filepath.FromSlash(relativePath)))
	}

	return candidates
}

func selectManifestPlatformPaths(platformPaths manifestPlatformPaths, goos, goarch string) []string {
	for _, key := range []string{platformKey(goos, goarch), strings.TrimSpace(goos), "all"} {
		if paths, ok := platformPaths[key]; ok {
			return copyStrings(paths)
		}
	}

	return nil
}

func normalizeCommands(commands []string) []string {
	normalized := make([]string, 0, len(commands))
	for _, command := range commands {
		trimmed := strings.ToLower(strings.TrimSpace(command))
		if trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}

	return normalized
}

func normalizeLogEntries(logs manifestLogs) []LogEntry {
	if len(logs) == 0 {
		return nil
	}

	normalized := make([]LogEntry, 0)
	for _, level := range []string{LogLevelInfo, LogLevelError, LogLevelDebug} {
		for rawLevel, paths := range logs {
			if NormalizeLogLevel(rawLevel) != level {
				continue
			}
			for _, path := range paths {
				normalized = append(normalized, LogEntry{
					Path:  strings.TrimSpace(path),
					Level: level,
				})
			}
		}
	}

	return normalized
}
