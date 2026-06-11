package backend

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"

	"polka/config"
	"polka/plugins"
	"polka/tools"
)

const (
	defaultRootDirectoryName = ".polka"
	configFileName           = "polka.yaml"
	namedConfigPrefix        = "polka."
	namedConfigSuffix        = ".yaml"
	defaultEnvironmentName   = "default"
	binDirectoryName         = "bin"
	envsDirectoryName        = "envs"
	runDirectoryName         = "run"
	currentEnvironmentName   = "current"
	configVersion            = 1
	dispatcherBinaryName     = "polka"
	dispatcherBatchFileName  = "polka.cmd"
	sessionStartFileName     = "session-start"
	sessionStopFileName      = "session-stop"
	powerShellExtension      = ".ps1"
)

var (
	validName                 = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	validVersion              = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	invalidLocalhostLabelPart = regexp.MustCompile(`[^a-z0-9-]+`)
)

type InstallProgressStage string

const (
	InstallProgressUsingCache  InstallProgressStage = "using cache"
	InstallProgressDownloading InstallProgressStage = "downloading"
	InstallProgressInstalling  InstallProgressStage = "installing"
	InstallProgressConfiguring InstallProgressStage = "configuring"
	InstallProgressInstalled   InstallProgressStage = "installed"
)

type InstallProgress struct {
	Index   int
	Total   int
	Tool    string
	Version string
	Stage   InstallProgressStage
}

// InstallRequest describes a single tool that will be installed.
type InstallRequest struct {
	Tool    string
	Version string
}

type Store struct {
	ProjectDir string
	RootDir    string
	EnvsDir    string
	BinDir     string
	ConfigFile string
	CacheDir   string
	Downloader ToolDownloader
	Plugins    *ToolRegistry
	Registry   *PluginRegistry
}

// InitOptions customizes project initialization.
type InitOptions struct {
	// Docroot sets the default environment document root when non-empty.
	Docroot string
}

func DefaultStore() (Store, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return Store{}, fmt.Errorf("resolve current working directory: %w", err)
	}

	projectDir, err := discoverProjectDir(workingDir)
	if err != nil {
		return Store{}, err
	}

	store := NewProjectStore(projectDir)
	config, err := store.loadConfig()
	if err != nil {
		return Store{}, err
	}

	return newStore(projectDir, resolveConfiguredRootDir(projectDir, config.Root)), nil
}

// StoreForWorkingDirectory returns a store rooted at the current working
// directory without discovering a parent project config.
func StoreForWorkingDirectory(root string) (Store, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return Store{}, fmt.Errorf("resolve current working directory: %w", err)
	}
	projectDir, err := filepath.Abs(workingDir)
	if err != nil {
		return Store{}, fmt.Errorf("resolve project directory: %w", err)
	}

	if strings.TrimSpace(root) == "" {
		return NewProjectStore(projectDir), nil
	}

	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return Store{}, fmt.Errorf("resolve Polka root directory: %w", err)
	}

	return newStore(projectDir, cleanRoot), nil
}

func StoreForRoot(root string) (Store, error) {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return Store{}, fmt.Errorf("resolve Polka root directory: %w", err)
	}

	projectDir, err := discoverProjectDirForRoot(cleanRoot)
	if err != nil {
		return Store{}, err
	}

	return newStore(projectDir, cleanRoot), nil
}

func discoverProjectDir(workingDir string) (string, error) {
	currentDir, err := filepath.Abs(workingDir)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	originalDir := currentDir

	for {
		configPath := filepath.Join(currentDir, configFileName)
		fileInfo, statErr := os.Stat(configPath)
		switch {
		case statErr == nil && !fileInfo.IsDir():
			return currentDir, nil
		case statErr == nil && fileInfo.IsDir():
			// Ignore directories named polka.yaml and keep walking upward.
		case !errors.Is(statErr, os.ErrNotExist):
			return "", fmt.Errorf("stat %s: %w", configPath, statErr)
		}

		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			return originalDir, nil
		}

		currentDir = parentDir
	}
}

func discoverProjectDirForRoot(root string) (string, error) {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve Polka root directory: %w", err)
	}

	currentDir := filepath.Dir(cleanRoot)
	originalDir := currentDir

	for {
		configPath := filepath.Join(currentDir, configFileName)
		fileInfo, statErr := os.Stat(configPath)
		switch {
		case statErr == nil && !fileInfo.IsDir():
			config, err := newStore(currentDir, cleanRoot).readConfig()
			if err != nil {
				return "", err
			}
			if pathsEqual(resolveConfiguredRootDir(currentDir, config.Root), cleanRoot) {
				return currentDir, nil
			}
		case statErr == nil && fileInfo.IsDir():
			// Ignore directories named polka.yaml and keep walking upward.
		case !errors.Is(statErr, os.ErrNotExist):
			return "", fmt.Errorf("stat %s: %w", configPath, statErr)
		}

		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			return originalDir, nil
		}

		currentDir = parentDir
	}
}

func NewProjectStore(projectDir string) Store {
	return newStore(projectDir, filepath.Join(projectDir, defaultRootDirectoryName))
}

func NewStore(root string) Store {
	cleanRoot := filepath.Clean(root)
	projectDir := filepath.Dir(cleanRoot)

	return newStore(projectDir, cleanRoot)
}

func newStore(projectDir, root string) Store {
	cleanProjectDir := filepath.Clean(projectDir)
	cleanRoot := filepath.Clean(root)

	return Store{
		ProjectDir: cleanProjectDir,
		RootDir:    cleanRoot,
		EnvsDir:    filepath.Join(cleanRoot, envsDirectoryName),
		BinDir:     filepath.Join(cleanRoot, binDirectoryName),
		ConfigFile: filepath.Join(cleanProjectDir, configFileName),
		CacheDir:   defaultCacheDir(cleanProjectDir),
		Plugins:    NewDefaultToolRegistry(),
		Registry:   NewDefaultPluginRegistry(),
	}
}

func (s Store) toolRegistry() *ToolRegistry {
	if s.Plugins != nil {
		return s.Plugins
	}
	if s.Registry != nil {
		return s.Registry.ToolRegistry()
	}

	return NewDefaultToolRegistry()
}

func (s Store) pluginRegistry() *PluginRegistry {
	if s.Registry != nil {
		return s.Registry
	}
	registry, err := plugins.NewRegistry(s.toolRegistry(), plugins.DefaultFrameworkPlugins()...)
	if err != nil {
		panic(err)
	}

	return registry
}

// SupportedFrameworks returns the built-in framework IDs supported by this store.
func (s Store) SupportedFrameworks() []string {
	return s.pluginRegistry().SupportedFrameworks()
}

// FrameworkPlugin resolves a built-in framework plugin by ID.
func (s Store) FrameworkPlugin(id string) (FrameworkPlugin, bool) {
	return s.pluginRegistry().Framework(id)
}

// FrameworkDefaults returns a validated default environment for a framework.
func (s Store) FrameworkDefaults(id string) (Environment, error) {
	frameworkID := strings.ToLower(strings.TrimSpace(id))
	if err := s.pluginRegistry().ValidateFramework(frameworkID); err != nil {
		return Environment{}, err
	}
	plugin, ok := s.pluginRegistry().Framework(frameworkID)
	if !ok {
		return Environment{}, fmt.Errorf("unsupported framework %q; supported frameworks: %s", id, strings.Join(s.SupportedFrameworks(), ", "))
	}

	environment := s.normalizeEnvironment(defaultEnvironmentName, plugin.Defaults())
	if err := s.toolRegistry().ValidateEnvironment(environment); err != nil {
		return Environment{}, err
	}

	return environment, nil
}

func resolveConfiguredRootDir(projectDir, configuredRoot string) string {
	trimmedRoot := strings.TrimSpace(configuredRoot)
	if trimmedRoot == "" {
		return filepath.Join(projectDir, defaultRootDirectoryName)
	}

	if filepath.IsAbs(trimmedRoot) {
		return filepath.Clean(trimmedRoot)
	}

	return filepath.Clean(filepath.Join(projectDir, filepath.FromSlash(trimmedRoot)))
}

func pathsEqual(left, right string) bool {
	cleanLeft := filepath.Clean(left)
	cleanRight := filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(cleanLeft, cleanRight)
	}

	return cleanLeft == cleanRight
}

func (s Store) Init() error {
	return s.InitWithOptions(InitOptions{})
}

// InitWithOptions initializes a project with config overrides.
func (s Store) InitWithOptions(options InitOptions) error {
	return s.init("", options)
}

// InitWithFramework initializes a project from a built-in framework preset.
func (s Store) InitWithFramework(framework string) error {
	return s.InitWithFrameworkOptions(framework, InitOptions{})
}

// InitWithFrameworkOptions initializes a project from a framework preset with overrides.
func (s Store) InitWithFrameworkOptions(framework string, options InitOptions) error {
	return s.init(framework, options)
}

func (s Store) init(framework string, options InitOptions) error {
	docroot := strings.TrimSpace(options.Docroot)
	var preset *Environment
	if strings.TrimSpace(framework) != "" {
		environment, err := s.FrameworkDefaults(framework)
		if err != nil {
			return err
		}
		if docroot != "" {
			environment.Docroot = docroot
		}
		if _, err := os.Stat(s.ConfigFile); err == nil {
			return fmt.Errorf("config file %s already exists; framework init would overwrite it", s.ConfigFile)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat config file: %w", err)
		}
		preset = &environment
	}

	if err := os.MkdirAll(s.EnvsDir, 0o755); err != nil {
		return fmt.Errorf("create environment root: %w", err)
	}
	if err := os.MkdirAll(s.BinDir, 0o755); err != nil {
		return fmt.Errorf("create binary root: %w", err)
	}

	if loadedConfig, err := s.readConfig(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			config := s.defaultConfig()
			if preset != nil {
				*preset = s.withInitDefaults(*preset)
				config.Environments[defaultEnvironmentName] = *preset
			} else if docroot != "" {
				environment := config.Environments[defaultEnvironmentName]
				environment.Docroot = docroot
				config.Environments[defaultEnvironmentName] = environment
			}
			if err := s.writeConfig(config); err != nil {
				return fmt.Errorf("write config file: %w", err)
			}
		} else {
			return err
		}
	} else if preset == nil && docroot != "" {
		environment := loadedConfig.Environments[defaultEnvironmentName]
		environment.Docroot = docroot
		loadedConfig.Environments[defaultEnvironmentName] = environment
		if err := s.writeConfig(loadedConfig); err != nil {
			return fmt.Errorf("write config file: %w", err)
		}
	}
	if err := s.installBinaries(); err != nil {
		return fmt.Errorf("install binaries: %w", err)
	}

	return nil
}

func (s Store) List() ([]Environment, error) {
	config, err := s.loadConfig()
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(config.Environments))
	for name := range config.Environments {
		names = append(names, name)
	}
	sort.Strings(names)

	environments := make([]Environment, 0, len(names))
	for _, name := range names {
		environments = append(environments, s.normalizeEnvironment(name, config.Environments[name]))
	}

	return environments, nil
}

func (s Store) Create(name, phpVersion, composerVersion string, database *DatabaseConfig) (Environment, error) {
	return s.CreateWithNodeJS(name, phpVersion, composerVersion, "", database)
}

func (s Store) Configure(name, phpVersion, composerVersion string, database *DatabaseConfig) (Environment, error) {
	return s.ConfigureWithNodeJS(name, phpVersion, composerVersion, "", database)
}

func (s Store) CreateWithNodeJS(name, phpVersion, composerVersion, nodeJSVersion string, database *DatabaseConfig) (Environment, error) {
	return s.writeEnvironment(name, phpVersion, composerVersion, nodeJSVersion, database, false)
}

func (s Store) ConfigureWithNodeJS(name, phpVersion, composerVersion, nodeJSVersion string, database *DatabaseConfig) (Environment, error) {
	return s.writeEnvironment(name, phpVersion, composerVersion, nodeJSVersion, database, true)
}

func (s Store) writeEnvironment(name, phpVersion, composerVersion, nodeJSVersion string, database *DatabaseConfig, allowUpdate bool) (Environment, error) {
	if err := s.Init(); err != nil {
		return Environment{}, err
	}
	if err := validateName(name); err != nil {
		return Environment{}, err
	}

	phpVersion = strings.TrimSpace(phpVersion)
	composerVersion = strings.TrimSpace(composerVersion)
	nodeJSVersion = strings.TrimSpace(nodeJSVersion)
	database = normalizeDatabaseConfig(database)
	if phpVersion != "" {
		if err := validateVersion(toolPHP, phpVersion); err != nil {
			return Environment{}, err
		}
	}
	if composerVersion != "" {
		if err := validateVersion(toolComposer, composerVersion); err != nil {
			return Environment{}, err
		}
	}
	if nodeJSVersion != "" {
		if err := validateVersion(toolNodeJS, nodeJSVersion); err != nil {
			return Environment{}, err
		}
	}

	storedEnvironment, exists, err := s.readEnvironment(name)
	if err != nil {
		return Environment{}, err
	}
	if exists && !allowUpdate {
		return Environment{}, fmt.Errorf("environment %q already exists", name)
	}

	environment := s.normalizeEnvironment(name, storedEnvironment)
	if phpVersion != "" {
		environment.PHPVersion = phpVersion
	}
	if composerVersion != "" {
		environment.ComposerVersion = composerVersion
	}
	if nodeJSVersion != "" {
		environment.NodeJSVersion = nodeJSVersion
	}
	if database != nil {
		environment.Database = mergeDatabaseConfig(environment.Database, database)
		environment = setDatabaseToolVersion(environment, database)
	}
	if environment.PHPVersion == "" && environment.ComposerVersion == "" && environment.PIEVersion == "" && environment.NodeJSVersion == "" && environment.MagoVersion == "" && environment.NginxVersion == "" && environment.MySQLVersion == "" && environment.MariaDBVersion == "" && environment.SQLiteVersion == "" && environment.PHPMyAdmin == nil && environment.Database == nil && environment.Mailpit == nil {
		return Environment{}, fmt.Errorf("environment requires at least one of php, composer, nodejs, mago, nginx, mysql, mariadb, sqlite, phpmyadmin, database, or mailpit")
	}
	if err := s.toolRegistry().ValidateEnvironment(environment); err != nil {
		return Environment{}, err
	}

	if err := s.writeEnvironmentConfig(name, environment); err != nil {
		return Environment{}, fmt.Errorf("write config file: %w", err)
	}
	config, err := s.loadConfig()
	if err != nil {
		return Environment{}, err
	}
	if err := s.syncManagedBinaries(config); err != nil {
		return Environment{}, fmt.Errorf("sync managed binaries: %w", err)
	}

	return environment, nil
}

func (s Store) Install(name string) ([]InstallResult, error) {
	return s.install(name, nil)
}

func (s Store) InstallWithProgress(name string, report func(InstallProgress)) ([]InstallResult, error) {
	return s.install(name, report)
}

func (s Store) InstallTool(name, tool, version string) (InstallResult, error) {
	return s.InstallToolWithProgress(name, tool, version, nil)
}

func (s Store) InstallToolWithProgress(name, tool, version string, report func(InstallProgress)) (InstallResult, error) {
	environment, registry, err := s.installEnvironment(name)
	if err != nil {
		return InstallResult{}, err
	}
	request, err := normalizeInstallRequest(registry, tool, version)
	if err != nil {
		return InstallResult{}, err
	}
	environment = environmentWithInstallRequest(environment, request)
	if err := validateInstallEnvironment(name, environment, []tools.InstallRequest{request}, registry); err != nil {
		return InstallResult{}, err
	}
	if err := s.writeEnvironmentConfig(name, environment); err != nil {
		return InstallResult{}, fmt.Errorf("write config file: %w", err)
	}
	config, err := s.loadConfig()
	if err != nil {
		return InstallResult{}, err
	}
	if err := s.syncManagedBinaries(config); err != nil {
		return InstallResult{}, fmt.Errorf("sync managed binaries: %w", err)
	}

	results, err := s.installRequests(environment, []tools.InstallRequest{request}, report)
	if err != nil {
		return InstallResult{}, err
	}
	if len(results) == 0 {
		return InstallResult{}, fmt.Errorf("no install result produced for %s %s", request.Tool, request.Version)
	}

	return results[0], nil
}

// InstallRequests returns the ordered list of tools that would be installed for
// the named environment, without performing any installation. This is useful for
// pre-registering tools in a progress display before installation begins.
func (s Store) InstallRequests(name string) ([]InstallRequest, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	environment, ok, err := s.readEnvironment(name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("environment %q does not exist", name)
	}
	normalized := s.normalizeEnvironment(name, environment)
	registry := s.toolRegistry()
	requests := registry.InstallRequests(normalized)
	result := make([]InstallRequest, len(requests))
	for i, r := range requests {
		result[i] = InstallRequest{Tool: r.Tool, Version: r.Version}
	}
	return result, nil
}

func (s Store) install(name string, report func(InstallProgress)) ([]InstallResult, error) {
	environment, registry, err := s.installEnvironment(name)
	if err != nil {
		return nil, err
	}

	requests := registry.InstallRequests(environment)
	if len(requests) == 0 {
		return nil, fmt.Errorf("environment %q does not define any installable tool versions", name)
	}
	if err := validateInstallEnvironment(name, environment, requests, registry); err != nil {
		return nil, err
	}

	return s.installRequests(environment, requests, report)
}

func (s Store) installEnvironment(name string) (Environment, *ToolRegistry, error) {
	if err := s.Init(); err != nil {
		return Environment{}, nil, err
	}
	if err := validateName(name); err != nil {
		return Environment{}, nil, err
	}

	environment, ok, err := s.readEnvironment(name)
	if err != nil {
		return Environment{}, nil, err
	}
	if !ok {
		return Environment{}, nil, fmt.Errorf("environment %q does not exist", name)
	}
	normalized := s.normalizeEnvironment(name, environment)
	registry := s.toolRegistry()

	return normalized, registry, nil
}

func validateInstallEnvironment(name string, environment Environment, requests []tools.InstallRequest, registry *ToolRegistry) error {
	if len(environment.PHPExtensions) > 0 && environment.PHPVersion == "" && !installRequestsIncludeTool(requests, toolPHP) {
		return fmt.Errorf("environment %q defines php-extensions but does not define a php version", name)
	}
	if environmentHasOPcacheConfig(environment) && environment.PHPVersion == "" && !installRequestsIncludeTool(requests, toolPHP) {
		return fmt.Errorf("environment %q defines OPcache config but does not define a php version", name)
	}
	if err := registry.ValidateEnvironment(environment); err != nil {
		return err
	}

	return nil
}

func environmentHasOPcacheConfig(environment Environment) bool {
	return config.NormalizeOPcachePreset(environment.OPcachePreset) != "" || len(environment.OPcacheConfig) > 0
}

func installRequestsIncludeTool(requests []tools.InstallRequest, tool string) bool {
	for _, request := range requests {
		if strings.EqualFold(strings.TrimSpace(request.Tool), strings.TrimSpace(tool)) {
			return true
		}
	}

	return false
}

func (s Store) installRequests(environment Environment, requests []tools.InstallRequest, report func(InstallProgress)) ([]InstallResult, error) {
	installEnvironment := s.withFrameworkPHPConfig(environment)
	installPHPConfig := tools.EffectivePHPConfigForInstall(installEnvironment)
	registry := s.toolRegistry()

	results := make([]InstallResult, len(requests))
	var wg sync.WaitGroup
	errs := make([]error, len(requests))
	var mu sync.Mutex

	safeReport := func(progress InstallProgress) {
		if report == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		report(progress)
	}

	for index, request := range requests {
		wg.Add(1)
		i, req := index, request
		go func() {
			defer wg.Done()

			plugin, ok := registry.Plugin(req.Tool)
			if !ok {
				errs[i] = fmt.Errorf("unsupported tool %q", req.Tool)
				return
			}
			baseProgress := InstallProgress{
				Index:   i + 1,
				Total:   len(requests),
				Tool:    req.Tool,
				Version: req.Version,
			}

			cachedPayloadPath, downloaded, err := s.ensureCachedTool(req.Tool, req.Version, func(stage InstallProgressStage) {
				baseProgress.Stage = stage
				safeReport(baseProgress)
			})
			if err != nil {
				errs[i] = err
				return
			}

			baseProgress.Stage = InstallProgressInstalling
			safeReport(baseProgress)
			targetPath, err := s.installToolFromCache(req.Tool, req.Version)
			if err != nil {
				errs[i] = err
				return
			}

			results[i] = InstallResult{
				Tool:       req.Tool,
				Version:    req.Version,
				CachePath:  cachedPayloadPath,
				TargetPath: targetPath,
				Downloaded: downloaded,
			}

			if req.Tool == toolPHP {
				if !installPHPConfig.IsZero() {
					baseProgress.Stage = InstallProgressConfiguring
					safeReport(baseProgress)
				}
			}
			if err := plugin.PostInstall(ToolInstallContext{
				ProjectDir:  s.ProjectDir,
				RootDir:     s.RootDir,
				EnvsDir:     s.EnvsDir,
				BinDir:      s.BinDir,
				CacheDir:    s.CacheDir,
				Environment: installEnvironment,
				Result:      results[i],
			}); err != nil {
				errs[i] = err
				return
			}

			baseProgress.Stage = InstallProgressInstalled
			safeReport(baseProgress)
		}()
	}

	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	return results, nil
}

func (s Store) withFrameworkPHPConfig(environment Environment) Environment {
	environment = s.withFrameworkPHPExtensions(environment)
	return s.withFrameworkOPcacheConfig(environment)
}

func (s Store) withFrameworkPHPExtensions(environment Environment) Environment {
	if strings.TrimSpace(environment.Framework) == "" {
		return environment
	}
	plugin, ok := s.FrameworkPlugin(environment.Framework)
	if !ok {
		return environment
	}
	frameworkExtensions := plugin.PHPExtensions()
	if len(frameworkExtensions) == 0 {
		return environment
	}

	merged := make(map[string]bool, len(frameworkExtensions)+len(environment.PHPExtensions))
	for name, enabled := range frameworkExtensions {
		merged[name] = enabled
	}
	for name, enabled := range environment.PHPExtensions {
		merged[name] = enabled
	}
	environment.PHPExtensions = config.NormalizePHPExtensions(merged)

	return environment
}

func (s Store) withFrameworkOPcacheConfig(environment Environment) Environment {
	if strings.TrimSpace(environment.Framework) == "" {
		return environment
	}
	plugin, ok := s.FrameworkPlugin(environment.Framework)
	if !ok {
		return environment
	}
	frameworkConfig := plugin.OPcacheConfig()
	if len(frameworkConfig) == 0 {
		return environment
	}

	merged := make(map[string]string, len(frameworkConfig)+len(environment.OPcacheConfig))
	for name, value := range frameworkConfig {
		merged[name] = value
	}
	for name, value := range environment.OPcacheConfig {
		merged[name] = value
	}
	environment.OPcacheConfig = config.NormalizeOPcacheConfig(merged)

	return environment
}

func normalizeInstallRequest(registry *ToolRegistry, tool, version string) (tools.InstallRequest, error) {
	normalizedTool := strings.ToLower(strings.TrimSpace(tool))
	normalizedVersion := strings.TrimSpace(version)
	if normalizedTool == "" {
		return tools.InstallRequest{}, fmt.Errorf("tool cannot be empty")
	}
	if _, ok := registry.Plugin(normalizedTool); !ok {
		return tools.InstallRequest{}, fmt.Errorf("unsupported tool %q", tool)
	}
	if err := validateVersion(normalizedTool, normalizedVersion); err != nil {
		return tools.InstallRequest{}, err
	}

	return tools.InstallRequest{Tool: normalizedTool, Version: normalizedVersion}, nil
}

func environmentWithInstallRequest(environment Environment, request tools.InstallRequest) Environment {
	switch request.Tool {
	case toolPHP:
		environment.PHPVersion = request.Version
	case toolComposer:
		environment.ComposerVersion = request.Version
	case toolPIE:
		environment.PIEVersion = request.Version
	case toolNodeJS:
		environment.NodeJSVersion = request.Version
	case toolMago:
		environment.MagoVersion = request.Version
	case toolNginx:
		environment.NginxVersion = request.Version
	case toolMySQL:
		environment.MySQLVersion = request.Version
		if environment.Database != nil && strings.EqualFold(strings.TrimSpace(environment.Database.Engine), toolMySQL) {
			environment.Database.Version = request.Version
		}
	case toolMariaDB:
		environment.MariaDBVersion = request.Version
		if environment.Database != nil && strings.EqualFold(strings.TrimSpace(environment.Database.Engine), toolMariaDB) {
			environment.Database.Version = request.Version
		}
	case toolSQLite:
		environment.SQLiteVersion = request.Version
	case toolMailpit:
		if environment.Mailpit == nil {
			environment.Mailpit = &MailpitConfig{}
		}
		environment.Mailpit.Version = request.Version
	case toolPHPMyAdmin:
		if environment.PHPMyAdmin == nil {
			environment.PHPMyAdmin = &PHPMyAdminConfig{}
		}
		environment.PHPMyAdmin.Version = request.Version
	}

	return environment
}

func (s Store) installToolFromCache(tool, version string) (string, error) {
	projectInstallDir := filepath.Join(s.EnvsDir, tool, version)

	if err := os.MkdirAll(filepath.Dir(projectInstallDir), 0o755); err != nil {
		return "", fmt.Errorf("create project install parent directory: %w", err)
	}
	stagingDir, err := os.MkdirTemp(filepath.Dir(projectInstallDir), version+"-tmp-")
	if err != nil {
		return "", fmt.Errorf("create project install staging directory: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	if _, err := tools.InstallCachedToolPayload(s.CacheDir, stagingDir, tool, version); err != nil {
		return "", fmt.Errorf("install cached tool payload: %w", err)
	}
	if err := os.RemoveAll(projectInstallDir); err != nil {
		return "", fmt.Errorf("reset project install directory: %w", err)
	}
	if err := os.Rename(stagingDir, projectInstallDir); err != nil {
		return "", fmt.Errorf("finalize project install directory: %w", err)
	}

	targetPath, err := s.resolveInstalledTool(tool, version)
	if err != nil {
		return "", err
	}

	return targetPath, nil
}

func (s Store) ensureCachedTool(tool, version string, report func(InstallProgressStage)) (string, bool, error) {
	cachedPayload, err := tools.CachedToolPayload(s.CacheDir, tool, version)
	if err == nil {
		if report != nil {
			report(InstallProgressUsingCache)
		}
		return cachedPayload.PayloadPath, false, nil
	}
	if !tools.IsCacheMiss(err) {
		return "", false, err
	}

	downloader := s.Downloader
	if downloader == nil {
		downloader = HTTPToolDownloader{Plugins: s.toolRegistry()}
	}

	if report != nil {
		report(InstallProgressDownloading)
	}
	if err := downloader.Download(s.CacheDir, tool, version); err != nil {
		return "", false, err
	}

	cachedPayload, err = tools.CachedToolPayload(s.CacheDir, tool, version)
	if err != nil {
		return "", false, err
	}

	return cachedPayload.PayloadPath, true, nil
}

func emitInstallProgress(report func(InstallProgress), progress InstallProgress, stage InstallProgressStage) {
	if report == nil {
		return
	}

	progress.Stage = stage
	report(progress)
}

func (s Store) Use(name string) error {
	if err := validateName(name); err != nil {
		return err
	}

	_, ok, err := s.readEnvironment(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("environment %q does not exist", name)
	}

	if name == defaultEnvironmentName {
		if err := s.clearActiveEnvironmentName(); err != nil {
			return fmt.Errorf("clear active environment: %w", err)
		}
	} else {
		if err := s.writeActiveEnvironmentName(name); err != nil {
			return fmt.Errorf("write active environment: %w", err)
		}
	}
	config, err := s.loadConfig()
	if err != nil {
		return err
	}
	if err := s.syncManagedBinaries(config); err != nil {
		return fmt.Errorf("sync managed binaries: %w", err)
	}

	return nil
}

func (s Store) Current() (*Environment, error) {
	name, err := s.activeEnvironmentName()
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = defaultEnvironmentName
	}

	environment, ok, err := s.readEnvironment(name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("current environment %q is not defined", name)
	}

	normalized := s.normalizeEnvironment(name, environment)
	return &normalized, nil
}

func (s Store) Remove(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if name == defaultEnvironmentName {
		return fmt.Errorf("environment %q is stored in %s and cannot be removed", defaultEnvironmentName, filepath.Base(s.ConfigFile))
	}

	_, ok, err := s.readEnvironment(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("environment %q does not exist", name)
	}

	currentName, err := s.activeEnvironmentName()
	if err != nil {
		return err
	}
	if err := os.Remove(s.environmentConfigFile(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove config file: %w", err)
	}
	if currentName == name {
		if err := s.clearActiveEnvironmentName(); err != nil {
			return fmt.Errorf("clear active environment: %w", err)
		}
	}
	config, err := s.loadConfig()
	if err != nil {
		return err
	}
	if err := s.syncManagedBinaries(config); err != nil {
		return fmt.Errorf("sync managed binaries: %w", err)
	}

	return nil
}

func (s Store) ResolveTool(tool string) (string, error) {
	request, err := s.toolRegistry().ResolveDispatchRequest(tool)
	if err != nil {
		return "", err
	}

	current, err := s.Current()
	if err != nil {
		return "", err
	}
	if current == nil {
		return "", fmt.Errorf("no active environment selected")
	}

	version := strings.TrimSpace(request.Plugin.Version(*current))
	if version == "" {
		return "", fmt.Errorf("environment %q does not define a %s version", current.Name, request.ConfigTool)
	}
	if err := validateVersion(request.ConfigTool, version); err != nil {
		return "", err
	}

	if request.ConfigTool == request.Executable {
		return s.resolveInstalledTool(request.ConfigTool, version)
	}

	return s.resolveInstalledDispatchExecutable(request.ConfigTool, request.Executable, version)
}

func (s Store) loadConfig() (Config, error) {
	loadedConfig, err := s.readConfig()
	if errors.Is(err, os.ErrNotExist) {
		return s.defaultConfig(), nil
	}
	if err != nil {
		return Config{}, err
	}

	if loadedConfig.Version == 0 {
		loadedConfig.Version = configVersion
	}
	if strings.TrimSpace(loadedConfig.Root) == "" {
		loadedConfig.Root = s.defaultConfig().Root
	}
	if loadedConfig.Environments == nil {
		loadedConfig.Environments = map[string]Environment{}
	}
	if _, ok := loadedConfig.Environments[defaultEnvironmentName]; !ok {
		loadedConfig.Environments[defaultEnvironmentName] = Environment{Name: defaultEnvironmentName}
	}

	return loadedConfig, nil
}

func (s Store) readConfig() (Config, error) {
	projectFile, defaultEnvironment, err := s.readProjectFile()
	if err != nil {
		return Config{}, err
	}

	loadedConfig := Config{
		Version: projectFile.Version,
		Root:    projectFile.Root,
		Environments: map[string]Environment{
			defaultEnvironmentName: defaultEnvironment,
		},
	}

	names, err := s.namedEnvironmentNames()
	if err != nil {
		return Config{}, err
	}
	for _, name := range names {
		environment, err := s.readNamedEnvironmentFile(name)
		if err != nil {
			return Config{}, err
		}
		loadedConfig.Environments[name] = environment
	}

	return loadedConfig, nil
}

func (s Store) readEnvironment(name string) (Environment, bool, error) {
	if err := validateName(name); err != nil {
		return Environment{}, false, err
	}
	if name == defaultEnvironmentName {
		_, environment, err := s.readProjectFile()
		if errors.Is(err, os.ErrNotExist) {
			return Environment{Name: defaultEnvironmentName}, true, nil
		}
		if err != nil {
			return Environment{}, false, err
		}

		return environment, true, nil
	}

	path := s.environmentConfigFile(name)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Environment{}, false, nil
		}
		return Environment{}, false, fmt.Errorf("stat %s: %w", path, err)
	}

	environment, err := s.readNamedEnvironmentFile(name)
	if err != nil {
		return Environment{}, false, err
	}

	return environment, true, nil
}

func (s Store) readProjectFile() (config.ProjectFile, Environment, error) {
	data, err := os.ReadFile(s.ConfigFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config.ProjectFile{}, Environment{}, err
		}

		return config.ProjectFile{}, Environment{}, fmt.Errorf("read config file: %w", err)
	}

	var projectFile config.ProjectFile
	if len(data) == 0 {
		return projectFile, config.ProjectFileToEnvironment(defaultEnvironmentName, projectFile), nil
	}

	hasLegacyEnvironments, err := yamlHasTopLevelKey(data, "environments")
	if err != nil {
		return config.ProjectFile{}, Environment{}, fmt.Errorf("decode config file: %w", err)
	}
	if hasLegacyEnvironments {
		return config.ProjectFile{}, Environment{}, legacyConfigError(s.ConfigFile)
	}
	if err := validateEnvironmentFileSchema(data); err != nil {
		return config.ProjectFile{}, Environment{}, fmt.Errorf("decode config file: %w", err)
	}

	if err := yaml.Unmarshal(data, &projectFile); err != nil {
		return config.ProjectFile{}, Environment{}, fmt.Errorf("decode config file: %w", err)
	}

	environment := config.ProjectFileToEnvironment(defaultEnvironmentName, projectFile)
	if err := s.validateEnvironmentFramework(environment); err != nil {
		return config.ProjectFile{}, Environment{}, fmt.Errorf("decode config file: %w", err)
	}

	return projectFile, environment, nil
}

func (s Store) readNamedEnvironmentFile(name string) (Environment, error) {
	path := s.environmentConfigFile(name)
	data, err := os.ReadFile(path)
	if err != nil {
		return Environment{}, fmt.Errorf("read config file %s: %w", path, err)
	}

	var environmentFile config.EnvironmentFile
	if len(data) == 0 {
		return config.EnvironmentFileToEnvironment(name, environmentFile), nil
	}

	hasLegacyEnvironments, err := yamlHasTopLevelKey(data, "environments")
	if err != nil {
		return Environment{}, fmt.Errorf("decode config file %s: %w", path, err)
	}
	if hasLegacyEnvironments {
		return Environment{}, legacyConfigError(path)
	}
	if err := validateEnvironmentFileSchema(data); err != nil {
		return Environment{}, fmt.Errorf("decode config file %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &environmentFile); err != nil {
		return Environment{}, fmt.Errorf("decode config file %s: %w", path, err)
	}

	environment := config.EnvironmentFileToEnvironment(name, environmentFile)
	if err := s.validateEnvironmentFramework(environment); err != nil {
		return Environment{}, fmt.Errorf("decode config file %s: %w", path, err)
	}

	return environment, nil
}

func (s Store) validateEnvironmentFramework(environment Environment) error {
	return s.pluginRegistry().ValidateFramework(environment.Framework)
}

func (s Store) writeConfig(loadedConfig Config) error {
	if loadedConfig.Version == 0 {
		loadedConfig.Version = configVersion
	}
	if strings.TrimSpace(loadedConfig.Root) == "" {
		loadedConfig.Root = s.relativeRootDir()
	}
	if loadedConfig.Environments == nil {
		loadedConfig.Environments = map[string]Environment{}
	}

	defaultEnvironment := loadedConfig.Environments[defaultEnvironmentName]
	if err := writeYAML(s.ConfigFile, config.ProjectFileFromEnvironment(loadedConfig.Version, loadedConfig.Root, defaultEnvironment)); err != nil {
		return err
	}

	names := make([]string, 0, len(loadedConfig.Environments))
	for name := range loadedConfig.Environments {
		if name != defaultEnvironmentName {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writeYAML(s.environmentConfigFile(name), config.EnvironmentFileFromEnvironment(loadedConfig.Environments[name])); err != nil {
			return err
		}
	}

	return nil
}

func (s Store) writeEnvironmentConfig(name string, environment Environment) error {
	if name == defaultEnvironmentName {
		projectFile, _, err := s.readProjectFile()
		if errors.Is(err, os.ErrNotExist) {
			projectFile = config.ProjectFile{Version: configVersion, Root: s.relativeRootDir()}
		} else if err != nil {
			return err
		}
		if projectFile.Version == 0 {
			projectFile.Version = configVersion
		}
		if strings.TrimSpace(projectFile.Root) == "" {
			projectFile.Root = s.relativeRootDir()
		}

		return writeYAML(s.ConfigFile, config.ProjectFileFromEnvironment(projectFile.Version, projectFile.Root, environment))
	}

	return writeYAML(s.environmentConfigFile(name), config.EnvironmentFileFromEnvironment(environment))
}

func (s Store) namedEnvironmentNames() ([]string, error) {
	entries, err := os.ReadDir(s.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("read project directory: %w", err)
	}

	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name, ok := namedEnvironmentNameFromFile(entry.Name())
		if !ok {
			continue
		}
		if name == defaultEnvironmentName {
			return nil, fmt.Errorf("environment name %q is reserved for %s", defaultEnvironmentName, filepath.Base(s.ConfigFile))
		}
		if err := validateName(name); err != nil {
			return nil, fmt.Errorf("invalid environment config file %q: %w", entry.Name(), err)
		}
		names = append(names, name)
	}
	sort.Strings(names)

	return names, nil
}

func namedEnvironmentNameFromFile(fileName string) (string, bool) {
	if fileName == configFileName || !strings.HasPrefix(fileName, namedConfigPrefix) || !strings.HasSuffix(fileName, namedConfigSuffix) {
		return "", false
	}

	return strings.TrimSuffix(strings.TrimPrefix(fileName, namedConfigPrefix), namedConfigSuffix), true
}

func (s Store) environmentConfigFile(name string) string {
	if name == defaultEnvironmentName {
		return s.ConfigFile
	}

	return filepath.Join(s.ProjectDir, namedConfigPrefix+name+namedConfigSuffix)
}

func yamlHasTopLevelKey(data []byte, key string) (bool, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false, err
	}
	_, ok := raw[key]

	return ok, nil
}

func validateEnvironmentFileSchema(data []byte) error {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}

	tools, hasTools, err := rawMapForKey(raw, "tools")
	if err != nil {
		return err
	}
	if hasTools {
		if err := validateToolsSchema(tools); err != nil {
			return err
		}
	}

	settings, hasSettings, err := rawMapForKey(raw, "settings")
	if err != nil {
		return err
	}
	if hasSettings {
		if err := validateSettingsSchema(settings, tools); err != nil {
			return err
		}
	}

	if value, ok := raw["opcache-preset"]; ok {
		if err := validateOPcachePresetSchema(value); err != nil {
			return err
		}
	}

	opcacheConfig, hasOPcacheConfig, err := rawMapForKey(raw, "opcache-config")
	if err != nil {
		return err
	}
	if hasOPcacheConfig {
		if err := validateOPcacheConfigSchema(opcacheConfig); err != nil {
			return err
		}
	}

	return nil
}

func validateToolsSchema(tools map[string]any) error {
	for key, value := range tools {
		if key == "database" {
			return fmt.Errorf("tools.database is no longer supported; use tools.mysql or tools.mariadb for versions and root database for runtime settings")
		}
		if !knownToolVersionKey(key) {
			return fmt.Errorf("unsupported tools.%s key", key)
		}
		if !isYAMLVersionScalar(value) {
			return fmt.Errorf("tools.%s must be a scalar version label", key)
		}
	}

	return nil
}

func validateSettingsSchema(settings, tools map[string]any) error {
	for key, value := range settings {
		settingMap, ok := asYAMLStringMap(value)
		if !ok {
			return fmt.Errorf("settings.%s must be a mapping", key)
		}
		switch key {
		case "mailpit":
			if !hasConfiguredToolVersion(tools, "mailpit") {
				return fmt.Errorf("settings.mailpit requires tools.mailpit")
			}
			if err := validateSettingKeys("settings.mailpit", settingMap, map[string]struct{}{"smtp-port": {}, "ui-port": {}}); err != nil {
				return err
			}
		case "phpmyadmin":
			if !hasConfiguredToolVersion(tools, "phpmyadmin") {
				return fmt.Errorf("settings.phpmyadmin requires tools.phpmyadmin")
			}
			if err := validateSettingKeys("settings.phpmyadmin", settingMap, map[string]struct{}{"port": {}}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported settings.%s key", key)
		}
	}

	return nil
}

func validateSettingKeys(prefix string, settings map[string]any, allowed map[string]struct{}) error {
	for key, value := range settings {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unsupported %s.%s key", prefix, key)
		}
		if !isYAMLSettingScalar(value) {
			return fmt.Errorf("%s.%s must be a scalar value", prefix, key)
		}
	}

	return nil
}

func validateOPcachePresetSchema(value any) error {
	preset, ok := value.(string)
	if !ok {
		return fmt.Errorf("opcache-preset must be one of none, dev, or production")
	}

	switch config.NormalizeOPcachePreset(preset) {
	case "", config.OPcachePresetDev, config.OPcachePresetProduction:
		return nil
	default:
		return fmt.Errorf("unsupported opcache-preset %q: use none, dev, or production", preset)
	}
}

func validateOPcacheConfigSchema(settings map[string]any) error {
	for key, value := range settings {
		if err := validateOPcacheConfigKey(key); err != nil {
			return err
		}
		if !isYAMLOPcacheScalar(value) {
			return fmt.Errorf("opcache-config.%s must be a scalar string, number, or boolean value", key)
		}
		if text, ok := value.(string); ok && strings.ContainsAny(text, "\r\n") {
			return fmt.Errorf("opcache-config.%s must be a single-line scalar value", key)
		}
	}

	return nil
}

func validateOPcacheConfigKey(key string) error {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if !strings.HasPrefix(normalized, "opcache.") || normalized == "opcache." {
		return fmt.Errorf("unsupported opcache-config.%s key: use opcache.* directive names", key)
	}
	if !validName.MatchString(normalized) {
		return fmt.Errorf("invalid opcache-config.%s key: use letters, numbers, dots, dashes, or underscores", key)
	}

	return nil
}

func rawMapForKey(raw map[string]any, key string) (map[string]any, bool, error) {
	value, ok := raw[key]
	if !ok {
		return nil, false, nil
	}
	mapping, ok := asYAMLStringMap(value)
	if !ok {
		return nil, true, fmt.Errorf("%s must be a mapping", key)
	}

	return mapping, true, nil
}

func asYAMLStringMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		result := make(map[string]any, len(typed))
		for key, value := range typed {
			keyString, ok := key.(string)
			if !ok {
				return nil, false
			}
			result[keyString] = value
		}
		return result, true
	default:
		return nil, false
	}
}

func knownToolVersionKey(key string) bool {
	switch key {
	case toolPHP, toolComposer, toolPIE, toolNodeJS, toolMago, toolNginx, toolMySQL, toolMariaDB, toolSQLite, toolMailpit, toolPHPMyAdmin:
		return true
	default:
		return false
	}
}

func hasConfiguredToolVersion(tools map[string]any, key string) bool {
	if tools == nil {
		return false
	}
	value, ok := tools[key]
	if !ok {
		return false
	}
	version, ok := value.(string)
	if ok {
		return strings.TrimSpace(version) != ""
	}

	return isYAMLVersionScalar(value)
}

func isYAMLVersionScalar(value any) bool {
	switch value.(type) {
	case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	default:
		return false
	}
}

func isYAMLSettingScalar(value any) bool {
	switch value.(type) {
	case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		return true
	default:
		return false
	}
}

func isYAMLOPcacheScalar(value any) bool {
	switch value.(type) {
	case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		return true
	default:
		return false
	}
}

func legacyConfigError(path string) error {
	return fmt.Errorf("legacy environments config in %s is no longer supported; move default settings to polka.yaml and named settings to polka.<name>.yaml", path)
}

func (s Store) activeEnvironmentPath() string {
	return filepath.Join(s.RootDir, runDirectoryName, currentEnvironmentName)
}

func (s Store) activeEnvironmentName() (string, error) {
	data, err := os.ReadFile(s.activeEnvironmentPath())
	switch {
	case err == nil:
		name := strings.TrimSpace(string(data))
		if name == "" {
			return "", nil
		}
		if err := validateName(name); err != nil {
			return "", fmt.Errorf("read active environment: %w", err)
		}
		return name, nil
	case os.IsNotExist(err):
		return "", nil
	default:
		return "", fmt.Errorf("read active environment: %w", err)
	}
}

func (s Store) writeActiveEnvironmentName(name string) error {
	if err := os.MkdirAll(filepath.Dir(s.activeEnvironmentPath()), 0o755); err != nil {
		return fmt.Errorf("create runtime state directory: %w", err)
	}
	if err := os.WriteFile(s.activeEnvironmentPath(), []byte(strings.TrimSpace(name)+"\n"), 0o644); err != nil {
		return err
	}

	return nil
}

func (s Store) clearActiveEnvironmentName() error {
	if err := os.Remove(s.activeEnvironmentPath()); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (s Store) defaultConfig() Config {
	return Config{
		Version: configVersion,
		Root:    s.relativeRootDir(),
		Environments: map[string]Environment{
			defaultEnvironmentName: s.withInitDefaults(Environment{Name: defaultEnvironmentName}),
		},
	}
}

func (s Store) withInitDefaults(environment Environment) Environment {
	environment.HTTPS = true
	if environment.Server == nil {
		environment.Server = &ServerConfig{}
	}
	if strings.TrimSpace(environment.Server.Hostname) == "" {
		environment.Server.Hostname = projectLocalHostname(s.ProjectDir)
	}

	return environment
}

func projectLocalHostname(projectDir string) string {
	label := strings.ToLower(strings.TrimSpace(filepath.Base(filepath.Clean(projectDir))))
	label = invalidLocalhostLabelPart.ReplaceAllString(label, "-")
	label = strings.Trim(label, "-")
	if label == "" || label == "." {
		label = "project"
	}

	return label + ".localhost"
}

func (s Store) relativeRootDir() string {
	relativePath, err := filepath.Rel(s.ProjectDir, s.RootDir)
	if err != nil {
		return filepath.ToSlash(s.RootDir)
	}

	return filepath.ToSlash(relativePath)
}

func (s Store) normalizeEnvironment(name string, environment Environment) Environment {
	return config.NormalizeEnvironment(name, environment)
}

func (s Store) resolveInstalledTool(tool, version string) (string, error) {
	return s.resolveInstalledToolIn(s.EnvsDir, tool, version)
}

func (s Store) resolveInstalledDispatchExecutable(configTool, executable, version string) (string, error) {
	return s.resolveInstalledDispatchExecutableIn(s.EnvsDir, configTool, executable, version)
}

func (s Store) resolveInstalledToolIn(root, tool, version string) (string, error) {
	candidates := s.toolRegistry().InstallCandidates(root, tool, version)
	if len(candidates) == 0 {
		return "", fmt.Errorf("unsupported tool %q", tool)
	}

	for _, candidate := range candidates {
		fileInfo, err := os.Stat(candidate)
		if err == nil {
			if fileInfo.IsDir() {
				continue
			}

			return candidate, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat %s: %w", candidate, err)
		}
	}

	return "", fmt.Errorf("%s version %q is not installed under %s", tool, version, filepath.Join(root, tool, version))
}

func resolveInstalledDispatchExecutableIn(root, configTool, executable, version string) (string, error) {
	return resolveInstalledDispatchExecutableWithRegistry(NewDefaultToolRegistry(), root, configTool, executable, version)
}

func (s Store) resolveInstalledDispatchExecutableIn(root, configTool, executable, version string) (string, error) {
	return resolveInstalledDispatchExecutableWithRegistry(s.toolRegistry(), root, configTool, executable, version)
}

func resolveInstalledDispatchExecutableWithRegistry(registry *ToolRegistry, root, configTool, executable, version string) (string, error) {
	candidates := registry.DispatchCandidates(root, configTool, executable, version)
	if len(candidates) == 0 {
		return "", fmt.Errorf("unsupported tool %q", executable)
	}

	for _, candidate := range candidates {
		fileInfo, err := os.Stat(candidate)
		if err == nil {
			if fileInfo.IsDir() {
				continue
			}

			return candidate, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat %s: %w", candidate, err)
		}
	}

	return "", fmt.Errorf("%s version %q does not include a %s command under %s", configTool, version, executable, filepath.Join(root, configTool, version))
}

func toolInstallCandidatesIn(root, tool, version string) []string {
	return NewDefaultToolRegistry().InstallCandidates(root, tool, version)
}

func dispatchExecutableCandidatesIn(root, configTool, executable, version string) []string {
	return NewDefaultToolRegistry().DispatchCandidates(root, configTool, executable, version)
}

func (s Store) installBinaries() error {
	if err := s.installDispatcherBinaries(); err != nil {
		return err
	}
	if err := s.installSessionScripts(); err != nil {
		return err
	}
	config, err := s.loadConfig()
	if err != nil {
		return err
	}
	if err := s.syncManagedBinaries(config); err != nil {
		return err
	}

	return nil
}

func (s Store) syncManagedBinaries(config Config) error {
	for _, binary := range s.managedBinaries() {
		binaryPath := filepath.Join(s.BinDir, binary.Name)
		if err := os.Remove(binaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove binary %q: %w", binary.Name, err)
		}
	}

	activeEnvironment, err := s.activeEnvironmentFromConfig(config)
	if err != nil {
		return err
	}
	for _, binary := range s.managedBinariesForEnvironment(activeEnvironment) {
		binaryPath := filepath.Join(s.BinDir, binary.Name)
		if err := os.WriteFile(binaryPath, []byte(binary.Contents), binary.Mode); err != nil {
			return fmt.Errorf("write binary %q: %w", binary.Name, err)
		}
	}

	return nil
}

func (s Store) installDispatcherBinaries() error {
	selfPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	for _, binary := range dispatcherBinaries(selfPath) {
		targetPath := filepath.Join(s.BinDir, binary.Name)
		if err := os.WriteFile(targetPath, []byte(binary.Contents), binary.Mode); err != nil {
			return fmt.Errorf("write dispatcher shim %q: %w", binary.Name, err)
		}
	}

	return nil
}

func (s Store) installSessionScripts() error {
	absRootDir, err := filepath.Abs(s.RootDir)
	if err != nil {
		return fmt.Errorf("resolve Polka root for session scripts: %w", err)
	}

	for _, script := range sessionScripts(absRootDir) {
		targetPath := filepath.Join(absRootDir, script.Name)
		if err := os.WriteFile(targetPath, []byte(script.Contents), script.Mode); err != nil {
			return fmt.Errorf("write session helper %q: %w", script.Name, err)
		}
	}

	return nil
}

func dispatcherBinaries(selfPath string) []installedBinary {
	return []installedBinary{
		shellDispatcherBinary(selfPath),
		windowsDispatcherBinary(selfPath),
	}
}

func sessionScripts(rootDir string) []installedBinary {
	return []installedBinary{
		shellSessionScript(rootDir, sessionStartFileName),
		shellSessionScript(rootDir, sessionStopFileName),
		powerShellSessionScript(rootDir, sessionStartFileName),
		powerShellSessionScript(rootDir, sessionStopFileName),
	}
}

func shellSessionScript(rootDir, name string) installedBinary {
	dispatcherPath := filepath.ToSlash(filepath.Join(rootDir, binDirectoryName, dispatcherBinaryName))
	return installedBinary{
		Name: name,
		Mode: 0o755,
		Contents: "#!/usr/bin/env sh\n" +
			"POLKA_SESSION_ROOT=" + shellLiteral(filepath.ToSlash(rootDir)) + "\n" +
			"POLKA_SESSION_DISPATCHER=" + shellLiteral(dispatcherPath) + "\n" +
			"if [ ! -f \"$POLKA_SESSION_DISPATCHER\" ]; then\n" +
			"  printf '%s\\n' 'Polka dispatcher shim not found in .polka/bin. Re-run \"polka init\".' >&2\n" +
			"  return 1 2>/dev/null || exit 1\n" +
			"fi\n" +
			"POLKA_SESSION_SCRIPT=$(\"$POLKA_SESSION_DISPATCHER\" --root \"$POLKA_SESSION_ROOT\" session " + sessionVerbForFile(name) + ") || {\n" +
			"  unset POLKA_SESSION_ROOT POLKA_SESSION_DISPATCHER POLKA_SESSION_SCRIPT\n" +
			"  return 1 2>/dev/null || exit 1\n" +
			"}\n" +
			"if [ -z \"$POLKA_SESSION_SCRIPT\" ]; then\n" +
			"  printf '%s\\n' 'Polka session command did not return a script path.' >&2\n" +
			"  unset POLKA_SESSION_ROOT POLKA_SESSION_DISPATCHER POLKA_SESSION_SCRIPT\n" +
			"  return 1 2>/dev/null || exit 1\n" +
			"fi\n" +
			". \"$POLKA_SESSION_SCRIPT\"\n" +
			"unset POLKA_SESSION_ROOT POLKA_SESSION_DISPATCHER POLKA_SESSION_SCRIPT\n",
	}
}

func powerShellSessionScript(rootDir, name string) installedBinary {
	dispatcherPath := filepath.Join(rootDir, binDirectoryName, dispatcherBatchFileName)
	return installedBinary{
		Name: name + powerShellExtension,
		Mode: 0o755,
		Contents: "$PolkaSessionRoot = " + powerShellLiteral(rootDir) + "\r\n" +
			"$PolkaSessionDispatcher = " + powerShellLiteral(dispatcherPath) + "\r\n" +
			"if (-not (Test-Path -LiteralPath $PolkaSessionDispatcher)) {\r\n" +
			"  Write-Error 'Polka dispatcher shim not found in .polka\\bin. Re-run polka init.'\r\n" +
			"  return\r\n" +
			"}\r\n" +
			"$PolkaSessionScript = & $PolkaSessionDispatcher --root $PolkaSessionRoot session " + sessionVerbForFile(name) + "\r\n" +
			"if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($PolkaSessionScript)) {\r\n" +
			"  if ([string]::IsNullOrWhiteSpace($PolkaSessionScript)) {\r\n" +
			"    Write-Error 'Polka session command did not return a script path.'\r\n" +
			"  }\r\n" +
			"  Remove-Variable PolkaSessionRoot, PolkaSessionDispatcher, PolkaSessionScript -ErrorAction SilentlyContinue\r\n" +
			"  return\r\n" +
			"}\r\n" +
			". ($PolkaSessionScript | Select-Object -First 1).Trim()\r\n" +
			"Remove-Variable PolkaSessionRoot, PolkaSessionDispatcher, PolkaSessionScript -ErrorAction SilentlyContinue\r\n",
	}
}

func sessionVerbForFile(name string) string {
	if name == sessionStopFileName {
		return "stop"
	}

	return "start"
}

func shellDispatcherBinary(selfPath string) installedBinary {
	return installedBinary{
		Name: dispatcherBinaryName,
		Mode: 0o755,
		Contents: "#!/usr/bin/env sh\n" +
			"set -eu\n" +
			"POLKA_EXE=${POLKA_DISPATCHER:-}\n" +
			"if [ -z \"$POLKA_EXE\" ]; then\n" +
			"  POLKA_EXE=" + shellLiteral(filepath.ToSlash(selfPath)) + "\n" +
			"fi\n" +
			"if [ ! -f \"$POLKA_EXE\" ]; then\n" +
			"  printf '%s\\n' 'Polka executable not found. Re-run \"polka init\" or set POLKA_DISPATCHER.' >&2\n" +
			"  exit 1\n" +
			"fi\n" +
			"exec \"$POLKA_EXE\" \"$@\"\n",
	}
}

func windowsDispatcherBinary(selfPath string) installedBinary {
	return installedBinary{
		Name: dispatcherBatchFileName,
		Mode: 0o755,
		Contents: "@echo off\r\n" +
			"setlocal\r\n" +
			"set \"POLKA_EXE=%POLKA_DISPATCHER%\"\r\n" +
			"if not defined POLKA_EXE set \"POLKA_EXE=" + escapeWindowsBatchValue(selfPath) + "\"\r\n" +
			"if not exist \"%POLKA_EXE%\" (\r\n" +
			"  >&2 echo Polka executable not found. Re-run polka init or set POLKA_DISPATCHER.\r\n" +
			"  exit /b 1\r\n" +
			")\r\n" +
			"\"%POLKA_EXE%\" %*\r\n" +
			"exit /b %ERRORLEVEL%\r\n",
	}
}

func (s Store) managedBinaries() []installedBinary {
	commands := s.toolRegistry().CleanupCommandNames()
	binaries := make([]installedBinary, 0, len(commands)*2)
	for _, command := range commands {
		binaries = append(binaries, shellDispatchBinary(command), windowsDispatchBinary(command))
	}

	return binaries
}

func (s Store) managedBinariesForEnvironment(environment *Environment) []installedBinary {
	tools := s.toolRegistry().ActiveCommandNames(environment)
	binaries := make([]installedBinary, 0, len(tools)*2)
	for _, tool := range tools {
		binaries = append(binaries, shellDispatchBinary(tool), windowsDispatchBinary(tool))
	}

	return binaries
}

func (s Store) activeEnvironmentFromConfig(config Config) (*Environment, error) {
	name, err := s.activeEnvironmentName()
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = defaultEnvironmentName
	}

	environment, ok := config.Environments[name]
	if !ok {
		return nil, nil
	}

	normalized := s.normalizeEnvironment(name, environment)
	return &normalized, nil
}

func managedToolsForEnvironment(environment *Environment) []string {
	return NewDefaultToolRegistry().ActiveCommandNames(environment)
}

func shellDispatchBinary(tool string) installedBinary {
	return installedBinary{
		Name: tool,
		Mode: 0o755,
		Contents: "#!/usr/bin/env sh\n" +
			"set -eu\n" +
			"SCRIPT_DIR=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" && pwd)\n" +
			"ROOT_DIR=$(CDPATH= cd -- \"$SCRIPT_DIR/..\" && pwd)\n" +
			"DISPATCHER=${POLKA_TOOL_DISPATCHER:-$SCRIPT_DIR/" + dispatcherBinaryName + "}\n" +
			"DISPATCH_ROOT=${POLKA_TOOL_ROOT:-$ROOT_DIR}\n" +
			"if [ ! -f \"$DISPATCHER\" ]; then\n" +
			"  printf '%s\\n' 'Polka dispatcher shim not found in .polka/bin. Re-run \"polka init\".' >&2\n" +
			"  exit 1\n" +
			"fi\n" +
			"export POLKA_TOOL_DISPATCHER=\"$DISPATCHER\"\n" +
			"export POLKA_TOOL_ROOT=\"$DISPATCH_ROOT\"\n" +
			"exec \"$DISPATCHER\" --root \"$DISPATCH_ROOT\" dispatch " + tool + " \"$@\"\n",
	}
}

func windowsDispatchBinary(tool string) installedBinary {
	return installedBinary{
		Name: tool + ".cmd",
		Mode: 0o755,
		Contents: "@echo off\r\n" +
			"setlocal\r\n" +
			"set \"SCRIPT_DIR=%~dp0\"\r\n" +
			"set \"ROOT_DIR=%SCRIPT_DIR%..\"\r\n" +
			"set \"DISPATCHER=%POLKA_TOOL_DISPATCHER%\"\r\n" +
			"if not defined DISPATCHER set \"DISPATCHER=%SCRIPT_DIR%" + dispatcherBatchFileName + "\"\r\n" +
			"set \"DISPATCH_ROOT=%POLKA_TOOL_ROOT%\"\r\n" +
			"if not defined DISPATCH_ROOT set \"DISPATCH_ROOT=%ROOT_DIR%\"\r\n" +
			"if not exist \"%DISPATCHER%\" (\r\n" +
			"  >&2 echo Polka dispatcher shim not found in .polka\\bin. Re-run polka init.\r\n" +
			"  exit /b 1\r\n" +
			")\r\n" +
			"set \"POLKA_TOOL_DISPATCHER=%DISPATCHER%\"\r\n" +
			"set \"POLKA_TOOL_ROOT=%DISPATCH_ROOT%\"\r\n" +
			"call \"%DISPATCHER%\" --root \"%DISPATCH_ROOT%\" dispatch " + tool + " %*\r\n" +
			"exit /b %ERRORLEVEL%\r\n",
	}
}

func shellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func powerShellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func escapeWindowsBatchValue(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}

func writeYAML(path string, value any) error {
	data, err := marshalYAML(path, value)
	if err != nil {
		return err
	}

	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return os.WriteFile(path, data, 0o644)
}

func marshalYAML(path string, value any) ([]byte, error) {
	existingData, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return yaml.Marshal(value)
	case err != nil:
		return nil, err
	case len(bytes.TrimSpace(existingData)) == 0:
		return yaml.Marshal(value)
	}

	comments := yaml.CommentMap{}
	var existing any
	if err := yaml.UnmarshalWithOptions(existingData, &existing, yaml.CommentToMap(comments)); err != nil {
		return nil, fmt.Errorf("decode existing YAML comments: %w", err)
	}
	if len(comments) == 0 {
		return yaml.Marshal(value)
	}

	return yaml.MarshalWithOptions(value, yaml.WithComment(comments))
}

func dispatcherBinaryFileName() string {
	if runtime.GOOS == "windows" {
		return dispatcherBatchFileName
	}

	return dispatcherBinaryName
}

type installedBinary struct {
	Name     string
	Mode     os.FileMode
	Contents string
}

func validateName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("environment name cannot be empty")
	}
	if !validName.MatchString(trimmed) {
		return fmt.Errorf("invalid environment name %q: use letters, numbers, dots, dashes, or underscores", name)
	}

	return nil
}

func validateVersion(tool, version string) error {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return fmt.Errorf("%s version cannot be empty", tool)
	}
	if !validVersion.MatchString(trimmed) {
		return fmt.Errorf("invalid %s version %q: use letters, numbers, dots, dashes, or underscores", tool, version)
	}

	return nil
}

type toolRequest struct {
	ConfigTool string
	Executable string
}

func resolveToolRequest(tool string) (toolRequest, error) {
	request, err := NewDefaultToolRegistry().ResolveDispatchRequest(tool)
	if err != nil {
		return toolRequest{}, err
	}

	return toolRequest{ConfigTool: request.ConfigTool, Executable: request.Executable}, nil
}

func defaultCacheDir(projectDir string) string {
	if override := strings.TrimSpace(os.Getenv("POLKA_CACHE_DIR")); override != "" {
		return filepath.Clean(filepath.FromSlash(override))
	}

	cacheDir, err := os.UserCacheDir()
	if err == nil {
		return filepath.Join(cacheDir, "polka", "cache")
	}

	return filepath.Join(projectDir, ".polka-cache")
}
