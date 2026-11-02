package tools

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"polka/config"
)

//go:embed manifests/*.yaml manifests/scripts/*.star manifests/scripts/lib/*.star
var builtinManifestFiles embed.FS

// builtinManifestFS returns the embedded manifest directory as an fs.FS whose
// root holds the manifest files, matching the layout external loaders use.
func builtinManifestFS() fs.FS {
	fsys, err := fs.Sub(builtinManifestFiles, "manifests")
	if err != nil {
		// fs.Sub only fails for invalid directory names; "manifests" is a
		// constant valid path.
		panic(err)
	}

	return fsys
}

// SettingType describes the scalar schema type of one settings.<namespace>.<key> entry.
type SettingType string

const (
	SettingTypeString SettingType = "string"
	SettingTypeBool   SettingType = "bool"
	SettingTypePort   SettingType = "port"
)

type pluginHooks struct {
	validate     func(config.Environment) error
	download     func(DownloadContext) error
	postInstall  func(InstallContext) error
	dependencies func(config.Environment) ([]InstallRequest, error)
}

type pluginManifest struct {
	ID                 string                           `yaml:"id"`
	InternalOnly       bool                             `yaml:"internal-only"`
	ExclusiveWith      []string                         `yaml:"exclusive-with"`
	DispatchPriority   int                              `yaml:"dispatch-priority"`
	VersionConstraint  manifestVersionConstraint        `yaml:"version-constraint"`
	Dependencies       []manifestDependency             `yaml:"dependencies"`
	Wrappers           map[string]manifestWrapper       `yaml:"wrappers"`
	Settings           map[string]SettingType           `yaml:"settings"`
	SettingsFrom       string                           `yaml:"settings-from"`
	PHPExtensions      []string                         `yaml:"php-extensions"`
	InstallCandidates  manifestPlatformPaths            `yaml:"install-candidates"`
	DispatchCommands   []string                         `yaml:"dispatch-commands"`
	CleanupCommands    []string                         `yaml:"cleanup-commands"`
	ActiveCommands     []string                         `yaml:"active-commands"`
	DispatchCandidates map[string]manifestPlatformPaths `yaml:"dispatch-candidates"`
	Logs               manifestLogs                     `yaml:"logs"`
	Download           manifestDownload                 `yaml:"download"`
	Script             manifestScript                   `yaml:"script"`
	Build              manifestBuild                    `yaml:"build"`

	// loadScript loads Script from the filesystem the manifest was read
	// from. It is nil when the manifest declares no script or was parsed
	// from bytes alone.
	loadScript scriptModuleLoader
	// sharedSettings is the settings-from manifest's schema, so script hooks
	// can read a shared namespace. It is nil unless both a script and
	// settings-from are declared.
	sharedSettings map[string]SettingType
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
	Mode      string `yaml:"mode"`
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
	Unwrap            downloadUnwrap    `yaml:"unwrap"`
}

// downloadUnwrap declares an archive nested inside a downloaded asset. The
// asset's checksum and archive format describe the outer download; the inner
// file named by Path is what gets cached and installed.
type downloadUnwrap struct {
	Path          string        `yaml:"path"`
	FileName      string        `yaml:"filename"`
	ArchiveFormat archiveFormat `yaml:"archive-format"`
}

// isZero reports whether the asset declares no nested archive.
func (u downloadUnwrap) isZero() bool {
	return u == downloadUnwrap{}
}

type databaseDownloadAsset = downloadAsset

// loadBuiltinManifest loads an embedded tool manifest. Built-in scripts share
// one lazily compiled module per process.
func loadBuiltinManifest(name string) (pluginManifest, error) {
	manifest, err := loadManifest(builtinManifestFS(), name)
	if err != nil {
		return pluginManifest{}, err
	}
	if manifest.loadScript != nil {
		manifest.loadScript = builtinScriptModuleLoader(manifest.ID, manifest.Script, manifest.loadScript)
	}

	return manifest, nil
}

// loadManifest reads <name>.yaml from fsys and binds any declared script to
// the same filesystem, so script paths resolve relative to the manifest.
func loadManifest(fsys fs.FS, name string) (pluginManifest, error) {
	path := strings.TrimSpace(name) + ".yaml"
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return pluginManifest{}, fmt.Errorf("read tool manifest %s: %w", path, err)
	}
	manifest, err := parsePluginManifest(data)
	if err != nil {
		return pluginManifest{}, err
	}
	manifest.Build, err = loadBuildFrom(fsys, manifest.ID, manifest.Build)
	if err != nil {
		return pluginManifest{}, err
	}
	if manifest.Script.File != "" {
		loader, err := newScriptModuleLoader(fsys, manifest.ID, manifest.Script)
		if err != nil {
			return pluginManifest{}, err
		}
		manifest.loadScript = loader
		if manifest.SettingsFrom != "" {
			manifest.sharedSettings, err = loadSharedSettings(fsys, manifest.ID, manifest.SettingsFrom)
			if err != nil {
				return pluginManifest{}, err
			}
		}
	}

	return manifest, nil
}

// loadSharedSettings reads the settings schema of the manifest a settings-from
// tool shares its namespace with.
func loadSharedSettings(fsys fs.FS, tool, name string) (map[string]SettingType, error) {
	data, err := fs.ReadFile(fsys, name+".yaml")
	if err != nil {
		return nil, fmt.Errorf("tool manifest %q settings-from: %w", tool, err)
	}
	manifest, err := parsePluginManifest(data)
	if err != nil {
		return nil, fmt.Errorf("tool manifest %q settings-from: %w", tool, err)
	}
	if len(manifest.Settings) == 0 {
		return nil, fmt.Errorf("tool manifest %q settings-from %q declares no settings", tool, name)
	}

	return manifest.Settings, nil
}

func parsePluginManifest(data []byte) (pluginManifest, error) {
	var manifest pluginManifest
	// Strict parsing keeps a misspelled key, such as a script block, from being
	// silently dropped.
	if err := yaml.UnmarshalWithOptions(data, &manifest, yaml.Strict()); err != nil {
		return pluginManifest{}, fmt.Errorf("parse tool manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return pluginManifest{}, err
	}
	manifest.PHPExtensions = normalizeManifestPHPExtensions(manifest.PHPExtensions)
	manifest.Settings = normalizeManifestSettings(manifest.Settings)
	manifest.SettingsFrom = strings.ToLower(strings.TrimSpace(manifest.SettingsFrom))
	manifest.ExclusiveWith = normalizeCommands(manifest.ExclusiveWith)
	manifest.VersionConstraint = manifestVersionConstraint{
		Min:   strings.TrimSpace(manifest.VersionConstraint.Min),
		Below: strings.TrimSpace(manifest.VersionConstraint.Below),
	}
	manifest.Dependencies = normalizeManifestDependencies(manifest.Dependencies)
	manifest.Wrappers = normalizeManifestWrappers(manifest.Wrappers)
	manifest.Download.GitHub.Mode = strings.ToLower(strings.TrimSpace(manifest.Download.GitHub.Mode))
	manifest.Script = manifest.Script.normalized()

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
	if m.InternalOnly {
		// Internal-only tools are never exposed through shims, dispatch, or
		// project settings, so command or settings declarations would silently
		// do nothing; reject them early.
		if len(m.DispatchCommands) > 0 || len(m.ActiveCommands) > 0 || len(m.CleanupCommands) > 0 || len(m.DispatchCandidates) > 0 {
			return fmt.Errorf("tool manifest %q is internal-only and cannot define dispatch, active, or cleanup commands", id)
		}
		if len(m.Settings) > 0 || strings.TrimSpace(m.SettingsFrom) != "" || len(m.ExclusiveWith) > 0 {
			return fmt.Errorf("tool manifest %q is internal-only and cannot define settings or exclusive-with", id)
		}
	}
	if len(m.Settings) > 0 && strings.TrimSpace(m.SettingsFrom) != "" {
		return fmt.Errorf("tool manifest %q cannot define both settings and settings-from", id)
	}
	if settingsFrom := strings.ToLower(strings.TrimSpace(m.SettingsFrom)); settingsFrom != "" {
		if !validName.MatchString(settingsFrom) {
			return fmt.Errorf("tool manifest %q has invalid settings-from %q", id, m.SettingsFrom)
		}
		if settingsFrom == id {
			return fmt.Errorf("tool manifest %q settings-from cannot reference itself", id)
		}
	}
	for key, settingType := range m.Settings {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if normalizedKey == "" || !validName.MatchString(normalizedKey) {
			return fmt.Errorf("tool manifest %q has invalid settings key %q", id, key)
		}
		switch SettingType(strings.ToLower(strings.TrimSpace(string(settingType)))) {
		case SettingTypeString, SettingTypeBool, SettingTypePort:
		default:
			return fmt.Errorf("tool manifest %q settings.%s has unsupported type %q", id, normalizedKey, settingType)
		}
	}
	for index, peer := range m.ExclusiveWith {
		normalizedPeer := strings.ToLower(strings.TrimSpace(peer))
		if normalizedPeer == "" || !validName.MatchString(normalizedPeer) {
			return fmt.Errorf("tool manifest %q exclusive-with[%d] has invalid tool id %q", id, index, peer)
		}
		if normalizedPeer == id {
			return fmt.Errorf("tool manifest %q exclusive-with[%d] cannot reference itself", id, index)
		}
	}
	if !m.VersionConstraint.isZero() {
		min := strings.TrimSpace(m.VersionConstraint.Min)
		below := strings.TrimSpace(m.VersionConstraint.Below)
		if min != "" {
			if err := validateVersion(id, min); err != nil {
				return fmt.Errorf("tool manifest %q version-constraint.min: %w", id, err)
			}
		}
		if below != "" {
			if err := validateVersion(id, below); err != nil {
				return fmt.Errorf("tool manifest %q version-constraint.below: %w", id, err)
			}
		}
		if min != "" && below != "" && compareVersions(min, below) >= 0 {
			return fmt.Errorf("tool manifest %q version-constraint min %q must be lower than below %q", id, min, below)
		}
	}
	for index, dep := range m.Dependencies {
		depTool := strings.ToLower(strings.TrimSpace(dep.Tool))
		if depTool == "" || !validName.MatchString(depTool) {
			return fmt.Errorf("tool manifest %q dependencies[%d] has invalid tool id %q", id, index, dep.Tool)
		}
		if depTool == id {
			return fmt.Errorf("tool manifest %q dependencies[%d] cannot reference itself", id, index)
		}
		if err := validateVersion(depTool, dep.Version); err != nil {
			return fmt.Errorf("tool manifest %q dependencies[%d]: %w", id, index, err)
		}
	}
	for rawName, wrapper := range m.Wrappers {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if name == "" || !validName.MatchString(name) {
			return fmt.Errorf("tool manifest %q has invalid wrappers key %q", id, rawName)
		}
		if len(wrapper.Target) == 0 {
			return fmt.Errorf("tool manifest %q wrappers.%s requires target", id, name)
		}
		if err := validateManifestPlatformPaths(id, "wrappers."+name+".target", wrapper.Target); err != nil {
			return err
		}
		for index, arg := range wrapper.Args {
			if !validWrapperArg.MatchString(strings.TrimSpace(arg)) {
				return fmt.Errorf("tool manifest %q wrappers.%s.args[%d] has invalid argument %q", id, name, index, arg)
			}
		}
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
		switch strings.ToLower(strings.TrimSpace(m.Download.GitHub.Mode)) {
		case "", "releases", "tag":
		default:
			return fmt.Errorf("tool manifest %q download.github has unsupported mode %q", id, m.Download.GitHub.Mode)
		}
	} else if strings.TrimSpace(m.Download.GitHub.Mode) != "" {
		return fmt.Errorf("tool manifest %q download.github.mode requires owner and repo", id)
	}
	if m.Download.hasReleaseIndex() {
		if strings.TrimSpace(m.Download.ReleaseIndex.URL) == "" {
			return fmt.Errorf("tool manifest %q download.release-index requires url", id)
		}
		if _, err := renderDownloadTemplate("download.release-index.url", m.Download.ReleaseIndex.URL, map[string]string{"requested": "1.0"}); err != nil {
			return fmt.Errorf("tool manifest %q %w", id, err)
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
		if asset.ChecksumAlgorithm == checksumAlgorithmEmbeddedCommitID {
			if !m.Download.hasGitHub() {
				return fmt.Errorf("tool manifest %q download assets %s with %s requires download.github", id, platform, checksumAlgorithmEmbeddedCommitID)
			}
			if asset.ArchiveFormat != archiveFormatZip {
				return fmt.Errorf("tool manifest %q download assets %s with %s requires archive-format %s", id, platform, checksumAlgorithmEmbeddedCommitID, archiveFormatZip)
			}
		}
	}
	if err := m.Script.validate(id, m.Download, m.Dependencies); err != nil {
		return err
	}
	if err := m.Build.validate(id, m.Script); err != nil {
		return err
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
	case checksumAlgorithmNone, checksumAlgorithmMD5, checksumAlgorithmSHA256, checksumAlgorithmSHA3_256, checksumAlgorithmEmbeddedCommitID:
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
	if !asset.Unwrap.isZero() {
		if err := validateDownloadUnwrap(asset); err != nil {
			return fmt.Errorf("tool manifest %q download assets %s unwrap: %w", tool, platform, err)
		}
	}

	return nil
}

// validateDownloadUnwrap checks an asset's nested archive declaration.
func validateDownloadUnwrap(asset downloadAsset) error {
	if strings.TrimSpace(asset.InstallPath) != "" {
		return fmt.Errorf("cannot be combined with install-path")
	}
	if asset.ChecksumAlgorithm == checksumAlgorithmEmbeddedCommitID {
		return fmt.Errorf("cannot be combined with %s", checksumAlgorithmEmbeddedCommitID)
	}
	if err := validateManifestContainedPath(asset.Unwrap.Path, "path", "archive"); err != nil {
		return err
	}
	// filepath.IsAbs misses rooted paths such as /x on Windows, so also require
	// a valid fs.FS path, which rejects any leading slash.
	if !fs.ValidPath(filepath.ToSlash(filepath.Clean(filepath.FromSlash(asset.Unwrap.Path)))) {
		return fmt.Errorf("path %q must be relative to the archive", asset.Unwrap.Path)
	}
	if strings.TrimSpace(asset.Unwrap.FileName) == "" {
		return fmt.Errorf("requires filename")
	}
	switch asset.Unwrap.ArchiveFormat {
	case archiveFormatZip, archiveFormatTarGz, archiveFormatTarXz:
	default:
		return fmt.Errorf("has unsupported archive format %q", asset.Unwrap.ArchiveFormat)
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
	if m.Script.File != "" && m.loadScript == nil {
		return nil, fmt.Errorf("tool manifest %q script %s requires loading the manifest from a filesystem", manifestID, m.Script.File)
	}
	if m.Script.hasHook(scriptHookResolve) && hooks.download != nil {
		return nil, fmt.Errorf("tool %q cannot define both a Go download hook and a script resolve hook", manifestID)
	}
	if m.Script.hasHook(scriptHookValidate) && hooks.validate != nil {
		return nil, fmt.Errorf("tool %q cannot define both a Go validate hook and a script validate hook", manifestID)
	}
	if m.Script.hasHook(scriptHookDependencies) && hooks.dependencies != nil {
		return nil, fmt.Errorf("tool %q cannot define both a Go dependencies hook and a script dependencies hook", manifestID)
	}
	if m.Script.hasHook(scriptHookPostInstall) && hooks.postInstall != nil {
		return nil, fmt.Errorf("tool %q cannot define both a Go post-install hook and a script post_install hook", manifestID)
	}

	download := hooks.download
	if download == nil && len(m.Download.Assets) > 0 {
		download = func(ctx DownloadContext) error {
			return downloadManifestRequest(ctx, m)
		}
	}

	settings := normalizeManifestSettings(m.Settings)
	settingsNamespace := strings.ToLower(strings.TrimSpace(m.SettingsFrom))
	if settingsNamespace == "" && len(settings) > 0 {
		settingsNamespace = manifestID
	}

	// rawVersion resolves the tool version from an environment's tools config;
	// internal installs use it against the reserved _internal environment even
	// when the public Version func below is neutralized for internal-only tools.
	rawVersion := manifestVersionFunc(m)
	version := rawVersion
	validate := hooks.validate
	// A settings-from tool has no schema of its own, so its hooks read the
	// shared namespace through the referenced manifest's schema.
	scriptSettings := settings
	if len(scriptSettings) == 0 {
		scriptSettings = m.sharedSettings
	}
	if m.Script.hasHook(scriptHookValidate) {
		validate = scriptValidateFunc(m, settingsNamespace, scriptSettings, rawVersion)
	}
	if m.InternalOnly {
		// Internal-only tools must never appear in project installs, shims, or
		// PHP extension unions, so their configured version reads as empty.
		version = func(config.Environment) string { return "" }
		validate = internalOnlyValidateFunc(manifestID, rawVersion, validate)
	}

	dependencies := hooks.dependencies
	if dependencies == nil && len(m.Dependencies) > 0 {
		declaredDeps := make([]InstallRequest, 0, len(m.Dependencies))
		for _, dep := range m.Dependencies {
			declaredDeps = append(declaredDeps, InstallRequest{
				Tool:    strings.ToLower(strings.TrimSpace(dep.Tool)),
				Version: strings.TrimSpace(dep.Version),
			})
		}
		dependencies = func(environment config.Environment) ([]InstallRequest, error) {
			if strings.TrimSpace(rawVersion(environment)) == "" {
				return nil, nil
			}
			return append([]InstallRequest(nil), declaredDeps...), nil
		}
	}
	if m.Script.hasHook(scriptHookDependencies) {
		dependencies = scriptDependenciesFunc(m, settingsNamespace, scriptSettings, rawVersion, runtime.GOOS, runtime.GOARCH)
	}

	postInstall := hooks.postInstall
	if m.Script.hasHook(scriptHookPostInstall) {
		postInstall = scriptPostInstallFunc(m, runtime.GOOS, runtime.GOARCH)
	}

	var build func(context.Context, BuildRequest) (BuildResult, error)
	if len(m.Build.Stages) > 0 {
		build = func(ctx context.Context, request BuildRequest) (BuildResult, error) {
			return buildManifestTool(ctx, m, request)
		}
	}

	return builtinPlugin{
		id:                 manifestID,
		version:            version,
		internalOnly:       m.InternalOnly,
		internalVersion:    rawVersion,
		versionConstraint:  m.VersionConstraint,
		wrappers:           normalizeManifestWrappers(m.Wrappers),
		executablePaths:    manifestExecutablePathsFunc(m),
		settings:           settings,
		settingsNamespace:  settingsNamespace,
		exclusiveWith:      normalizeCommands(m.ExclusiveWith),
		phpExtensions:      manifestPHPExtensionMap(m.PHPExtensions),
		validate:           validate,
		installCandidates:  manifestInstallCandidatesFunc(m),
		dispatchCommands:   normalizeCommands(m.DispatchCommands),
		cleanupCommands:    normalizeCommands(m.CleanupCommands),
		activeCommands:     manifestActiveCommandsFunc(m),
		dispatchCandidates: manifestDispatchCandidatesFunc(m),
		logs:               normalizeLogEntries(m.Logs),
		download:           download,
		postInstall:        postInstall,
		dependencies:       dependencies,
		build:              build,
		buildSupports:      m.Build.supports,
	}, nil
}

func normalizeManifestDependencies(dependencies []manifestDependency) []manifestDependency {
	if len(dependencies) == 0 {
		return nil
	}

	normalized := make([]manifestDependency, 0, len(dependencies))
	for _, dep := range dependencies {
		normalized = append(normalized, manifestDependency{
			Tool:    strings.ToLower(strings.TrimSpace(dep.Tool)),
			Version: strings.TrimSpace(dep.Version),
		})
	}

	return normalized
}

func normalizeManifestWrappers(wrappers map[string]manifestWrapper) map[string]manifestWrapper {
	if len(wrappers) == 0 {
		return nil
	}

	normalized := make(map[string]manifestWrapper, len(wrappers))
	for rawName, wrapper := range wrappers {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if name == "" {
			continue
		}
		args := make([]string, 0, len(wrapper.Args))
		for _, arg := range wrapper.Args {
			trimmed := strings.TrimSpace(arg)
			if trimmed != "" {
				args = append(args, trimmed)
			}
		}
		normalized[name] = manifestWrapper{
			Target:       wrapper.Target,
			Args:         args,
			SkipIfExists: wrapper.SkipIfExists,
		}
	}
	if len(normalized) == 0 {
		return nil
	}

	return normalized
}

func manifestExecutablePathsFunc(m pluginManifest) func(installDir string) []string {
	installPaths := m.InstallCandidates
	dispatchCommands := make([]string, 0, len(m.DispatchCandidates))
	for command := range m.DispatchCandidates {
		dispatchCommands = append(dispatchCommands, command)
	}
	sort.Strings(dispatchCommands)
	dispatchCandidates := m.DispatchCandidates

	return func(installDir string) []string {
		if strings.TrimSpace(installDir) == "" {
			return nil
		}
		paths := manifestCandidatePaths(installDir, installPaths, runtime.GOOS, runtime.GOARCH)
		for _, command := range dispatchCommands {
			paths = append(paths, manifestCandidatePaths(installDir, dispatchCandidates[command], runtime.GOOS, runtime.GOARCH)...)
		}

		return paths
	}
}

func normalizeManifestSettings(settings map[string]SettingType) map[string]SettingType {
	if len(settings) == 0 {
		return nil
	}

	normalized := make(map[string]SettingType, len(settings))
	for key, settingType := range settings {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if normalizedKey == "" {
			continue
		}
		normalized[normalizedKey] = SettingType(strings.ToLower(strings.TrimSpace(string(settingType))))
	}
	if len(normalized) == 0 {
		return nil
	}

	return normalized
}

// internalOnlyValidateFunc rejects project configs that pin a version for an
// internal-only tool, pointing users at the polka-managed alternative.
func internalOnlyValidateFunc(id string, rawVersion func(config.Environment) string, next func(config.Environment) error) func(config.Environment) error {
	return func(environment config.Environment) error {
		if strings.TrimSpace(rawVersion(environment)) != "" {
			return fmt.Errorf("%s is managed internally by polka and cannot be configured in a project environment", id)
		}
		if next != nil {
			return next(environment)
		}

		return nil
	}
}

func manifestVersionFunc(m pluginManifest) func(config.Environment) string {
	id := strings.ToLower(strings.TrimSpace(m.ID))

	return func(environment config.Environment) string {
		switch id {
		case MySQL, MariaDB, PostgreSQL:
			return config.DatabaseToolVersion(environment, id)
		default:
			return environment.ToolVersion(id)
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
		// An explicit dispatch-candidates entry overrides the primary binary for
		// commands whose executable differs from install-candidates (nodejs
		// npm/npx/yarn, frankenphp php). Every other dispatch command resolves to
		// the tool's install-candidates.
		if paths, ok := dispatchCandidates[normalizedExecutable]; ok {
			return manifestCandidatePaths(installDir, paths, runtime.GOOS, runtime.GOARCH)
		}

		return manifestCandidatePaths(installDir, installPaths, runtime.GOOS, runtime.GOARCH)
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
	if len(normalized) == 0 {
		return nil
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
