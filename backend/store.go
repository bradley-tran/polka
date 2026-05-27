package backend

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

const (
	defaultRootDirectoryName = ".polka"
	configFileName           = "polka.yaml"
	binDirectoryName         = "bin"
	envsDirectoryName        = "envs"
	configVersion            = 1
	toolPHP                  = "php"
	toolComposer             = "composer"
	toolNodeJS               = "nodejs"
	toolNode                 = "node"
	toolNPM                  = "npm"
	toolNPX                  = "npx"
	toolNginx                = "nginx"
	toolMySQL                = "mysql"
	toolMariaDB              = "mariadb"
	dispatcherBinaryName     = "polka"
	dispatcherBatchFileName  = "polka.cmd"
	sessionStartFileName     = "session-start"
	sessionStopFileName      = "session-stop"
	powerShellExtension      = ".ps1"
)

var (
	validName                 = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	validVersion              = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	composerDefaultExtensions = []string{"openssl", "zip"}
)

type Environment struct {
	Name            string            `yaml:"-"`
	PHPVersion      string            `yaml:"php,omitempty"`
	ComposerVersion string            `yaml:"composer,omitempty"`
	NodeJSVersion   string            `yaml:"nodejs,omitempty"`
	NginxVersion    string            `yaml:"nginx,omitempty"`
	Docroot         string            `yaml:"docroot,omitempty"`
	EnvFile         string            `yaml:"env-file,omitempty"`
	EnvVars         map[string]string `yaml:"env-vars,omitempty"`
	Database        *DatabaseConfig   `yaml:"database,omitempty"`
	PHPExtensions   map[string]bool   `yaml:"php-extensions,omitempty"`
	Server          *ServerConfig     `yaml:"server,omitempty"`
}

type ServerConfig struct {
	Hostname string `yaml:"hostname,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	HTTPS    bool   `yaml:"https,omitempty"`
}

type InstallResult struct {
	Tool       string
	Version    string
	CachePath  string
	TargetPath string
	Downloaded bool
}

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

type Config struct {
	Version      int                    `yaml:"version"`
	Root         string                 `yaml:"root"`
	Current      string                 `yaml:"current,omitempty"`
	Environments map[string]Environment `yaml:"environments,omitempty"`
}

type Store struct {
	ProjectDir string
	RootDir    string
	EnvsDir    string
	BinDir     string
	ConfigFile string
	CacheDir   string
	Downloader ToolDownloader
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
	}
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
	if err := os.MkdirAll(s.EnvsDir, 0o755); err != nil {
		return fmt.Errorf("create environment root: %w", err)
	}
	if err := os.MkdirAll(s.BinDir, 0o755); err != nil {
		return fmt.Errorf("create binary root: %w", err)
	}

	if _, err := s.readConfig(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := s.writeConfig(s.defaultConfig()); err != nil {
				return fmt.Errorf("write config file: %w", err)
			}
		} else {
			return err
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

	config, err := s.loadConfig()
	if err != nil {
		return Environment{}, err
	}

	storedEnvironment, exists := config.Environments[name]
	if exists && !allowUpdate {
		return Environment{}, fmt.Errorf("environment %q already exists in %s", name, filepath.Base(s.ConfigFile))
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
	}
	if environment.PHPVersion == "" && environment.ComposerVersion == "" && environment.NodeJSVersion == "" && environment.NginxVersion == "" && environment.Database == nil {
		return Environment{}, fmt.Errorf("environment requires at least one of php, composer, nodejs, nginx, or database")
	}
	if err := validateDatabaseConfig(environment.Database); err != nil {
		return Environment{}, err
	}

	config.Environments[name] = environment
	if err := s.writeConfig(config); err != nil {
		return Environment{}, fmt.Errorf("write config file: %w", err)
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

func (s Store) install(name string, report func(InstallProgress)) ([]InstallResult, error) {
	if err := s.Init(); err != nil {
		return nil, err
	}
	if err := validateName(name); err != nil {
		return nil, err
	}

	config, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	environment, ok := config.Environments[name]
	if !ok {
		return nil, fmt.Errorf("environment %q does not exist in %s", name, filepath.Base(s.ConfigFile))
	}
	normalized := s.normalizeEnvironment(name, environment)
	installPHPExtensions := effectivePHPExtensionsForInstall(normalized)
	if len(normalized.PHPExtensions) > 0 && normalized.PHPVersion == "" {
		return nil, fmt.Errorf("environment %q defines php-extensions but does not define a php version", name)
	}

	requests := []InstallResult{}
	if normalized.PHPVersion != "" {
		requests = append(requests, InstallResult{Tool: toolPHP, Version: normalized.PHPVersion})
	}
	if normalized.ComposerVersion != "" {
		requests = append(requests, InstallResult{Tool: toolComposer, Version: normalized.ComposerVersion})
	}
	if normalized.NodeJSVersion != "" {
		requests = append(requests, InstallResult{Tool: toolNodeJS, Version: normalized.NodeJSVersion})
	}
	if normalized.NginxVersion != "" {
		requests = append(requests, InstallResult{Tool: toolNginx, Version: normalized.NginxVersion})
	}
	if normalized.Database != nil {
		requests = append(requests, InstallResult{Tool: normalized.Database.Engine, Version: normalized.Database.Version})
	}
	if len(requests) == 0 {
		return nil, fmt.Errorf("environment %q does not define any installable tool versions", name)
	}

	results := make([]InstallResult, 0, len(requests))
	for index, request := range requests {
		baseProgress := InstallProgress{
			Index:   index + 1,
			Total:   len(requests),
			Tool:    request.Tool,
			Version: request.Version,
		}

		cachedToolPath, downloaded, err := s.ensureCachedTool(request.Tool, request.Version, func(stage InstallProgressStage) {
			emitInstallProgress(report, baseProgress, stage)
		})
		if err != nil {
			return nil, err
		}

		emitInstallProgress(report, baseProgress, InstallProgressInstalling)
		targetPath, err := s.installToolFromCache(request.Tool, request.Version)
		if err != nil {
			return nil, err
		}

		results = append(results, InstallResult{
			Tool:       request.Tool,
			Version:    request.Version,
			CachePath:  cachedToolPath,
			TargetPath: targetPath,
			Downloaded: downloaded,
		})

		if request.Tool == toolPHP {
			if len(installPHPExtensions) > 0 {
				emitInstallProgress(report, baseProgress, InstallProgressConfiguring)
			}
			if err := s.configureInstalledPHPExtensions(request.Version, installPHPExtensions); err != nil {
				return nil, err
			}
		}

		emitInstallProgress(report, baseProgress, InstallProgressInstalled)
	}

	return results, nil
}

func (s Store) installToolFromCache(tool, version string) (string, error) {
	cacheSourceDir := filepath.Join(s.CacheDir, tool, version)
	projectInstallDir := filepath.Join(s.EnvsDir, tool, version)

	if err := os.RemoveAll(projectInstallDir); err != nil {
		return "", fmt.Errorf("reset project install directory: %w", err)
	}
	if err := copyDir(cacheSourceDir, projectInstallDir); err != nil {
		return "", fmt.Errorf("copy cached tool into project: %w", err)
	}

	targetPath, err := s.resolveInstalledTool(tool, version)
	if err != nil {
		return "", err
	}

	return targetPath, nil
}

func (s Store) ensureCachedTool(tool, version string, report func(InstallProgressStage)) (string, bool, error) {
	cacheToolPath, err := s.resolveInstalledToolIn(s.CacheDir, tool, version)
	if err == nil {
		if report != nil {
			report(InstallProgressUsingCache)
		}
		return cacheToolPath, false, nil
	}
	if !isMissingInstall(err) {
		return "", false, err
	}

	downloader := s.Downloader
	if downloader == nil {
		downloader = HTTPToolDownloader{}
	}

	if report != nil {
		report(InstallProgressDownloading)
	}
	if err := downloader.Download(s.CacheDir, tool, version); err != nil {
		return "", false, err
	}

	cacheToolPath, err = s.resolveInstalledToolIn(s.CacheDir, tool, version)
	if err != nil {
		return "", false, err
	}

	return cacheToolPath, true, nil
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

	config, err := s.loadConfig()
	if err != nil {
		return err
	}

	if _, ok := config.Environments[name]; !ok {
		return fmt.Errorf("environment %q does not exist in %s", name, filepath.Base(s.ConfigFile))
	}

	config.Current = name
	if err := s.writeConfig(config); err != nil {
		return fmt.Errorf("write current environment: %w", err)
	}
	if err := s.syncManagedBinaries(config); err != nil {
		return fmt.Errorf("sync managed binaries: %w", err)
	}

	return nil
}

func (s Store) Current() (*Environment, error) {
	config, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	if config.Current == "" {
		return nil, nil
	}

	environment, ok := config.Environments[config.Current]
	if !ok {
		return nil, fmt.Errorf("current environment %q is not defined in %s", config.Current, filepath.Base(s.ConfigFile))
	}

	normalized := s.normalizeEnvironment(config.Current, environment)
	return &normalized, nil
}

func (s Store) Remove(name string) error {
	if err := validateName(name); err != nil {
		return err
	}

	config, err := s.loadConfig()
	if err != nil {
		return err
	}

	if _, ok := config.Environments[name]; !ok {
		return fmt.Errorf("environment %q does not exist in %s", name, filepath.Base(s.ConfigFile))
	}

	delete(config.Environments, name)
	if config.Current == name {
		config.Current = ""
	}

	if err := s.writeConfig(config); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	if err := s.syncManagedBinaries(config); err != nil {
		return fmt.Errorf("sync managed binaries: %w", err)
	}

	return nil
}

func (s Store) ResolveTool(tool string) (string, error) {
	request, err := resolveToolRequest(tool)
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

	version := strings.TrimSpace(current.toolVersion(request.ConfigTool))
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
	config, err := s.readConfig()
	if errors.Is(err, os.ErrNotExist) {
		return s.defaultConfig(), nil
	}
	if err != nil {
		return Config{}, err
	}

	if config.Version == 0 {
		config.Version = configVersion
	}
	if strings.TrimSpace(config.Root) == "" {
		config.Root = s.defaultConfig().Root
	}
	if config.Environments == nil {
		config.Environments = map[string]Environment{}
	}

	return config, nil
}

func (s Store) readConfig() (Config, error) {
	data, err := os.ReadFile(s.ConfigFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, err
		}

		return Config{}, fmt.Errorf("read config file: %w", err)
	}

	var config Config
	if len(data) == 0 {
		return config, nil
	}

	if err := yaml.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("decode config file: %w", err)
	}

	return config, nil
}

func (s Store) writeConfig(config Config) error {
	return writeYAML(s.ConfigFile, config)
}

func (s Store) defaultConfig() Config {
	return Config{
		Version:      configVersion,
		Root:         s.relativeRootDir(),
		Environments: map[string]Environment{},
	}
}

func (s Store) relativeRootDir() string {
	relativePath, err := filepath.Rel(s.ProjectDir, s.RootDir)
	if err != nil {
		return filepath.ToSlash(s.RootDir)
	}

	return filepath.ToSlash(relativePath)
}

func (s Store) normalizeEnvironment(name string, environment Environment) Environment {
	return Environment{
		Name:            name,
		PHPVersion:      strings.TrimSpace(environment.PHPVersion),
		ComposerVersion: strings.TrimSpace(environment.ComposerVersion),
		NodeJSVersion:   strings.TrimSpace(environment.NodeJSVersion),
		NginxVersion:    strings.TrimSpace(environment.NginxVersion),
		Docroot:         strings.TrimSpace(environment.Docroot),
		EnvFile:         strings.TrimSpace(environment.EnvFile),
		EnvVars:         normalizeEnvironmentVariables(environment.EnvVars),
		Database:        normalizeDatabaseConfig(environment.Database),
		PHPExtensions:   normalizePHPExtensions(environment.PHPExtensions),
		Server:          normalizeServerConfig(environment.Server),
	}
}

func normalizeEnvironmentVariables(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	normalized := make(map[string]string, len(values))
	for key, value := range values {
		normalized[strings.TrimSpace(key)] = value
	}

	return normalized
}

func normalizeServerConfig(server *ServerConfig) *ServerConfig {
	if server == nil {
		return nil
	}

	normalized := &ServerConfig{
		Hostname: strings.TrimSpace(server.Hostname),
		Port:     server.Port,
		HTTPS:    server.HTTPS,
	}
	if normalized.Hostname == "" && normalized.Port == 0 && !normalized.HTTPS {
		return nil
	}

	return normalized
}

func normalizePHPExtensions(extensions map[string]bool) map[string]bool {
	if len(extensions) == 0 {
		return nil
	}

	normalized := make(map[string]bool, len(extensions))
	for name, enabled := range extensions {
		normalized[strings.ToLower(strings.TrimSpace(name))] = enabled
	}

	return normalized
}

func effectivePHPExtensionsForInstall(environment Environment) map[string]bool {
	if environment.ComposerVersion == "" {
		return environment.PHPExtensions
	}

	effective := make(map[string]bool, len(environment.PHPExtensions)+len(composerDefaultExtensions))
	for name, enabled := range environment.PHPExtensions {
		effective[name] = enabled
	}
	for _, name := range composerDefaultExtensions {
		if _, ok := effective[name]; !ok {
			effective[name] = true
		}
	}

	return effective
}

func (s Store) configureInstalledPHPExtensions(version string, extensions map[string]bool) error {
	if len(extensions) == 0 {
		return nil
	}

	phpPath, err := s.resolveInstalledTool(toolPHP, version)
	if err != nil {
		return err
	}

	phpDir := filepath.Dir(phpPath)
	extensionDir, err := filepath.Rel(phpDir, filepath.Join(s.EnvsDir, toolPHP, version, "ext"))
	if err != nil {
		extensionDir = filepath.Join(s.EnvsDir, toolPHP, version, "ext")
	}

	configData, err := renderPHPExtensionConfig(filepath.ToSlash(extensionDir), extensions)
	if err != nil {
		return err
	}

	phpIniPath := filepath.Join(phpDir, "php.ini")
	if err := os.WriteFile(phpIniPath, configData, 0o644); err != nil {
		return fmt.Errorf("write php extension config: %w", err)
	}

	return nil
}

func renderPHPExtensionConfig(extensionDir string, extensions map[string]bool) ([]byte, error) {
	names := make([]string, 0, len(extensions))
	for name := range extensions {
		if err := validatePHPExtensionName(name); err != nil {
			return nil, err
		}

		names = append(names, name)
	}
	sort.Strings(names)

	var builder strings.Builder
	builder.WriteString("; Generated by Polka. Re-run polka install after editing php-extensions.\n")
	builder.WriteString("[PHP]\n")
	builder.WriteString("extension_dir=\"")
	builder.WriteString(extensionDir)
	builder.WriteString("\"\n")
	for _, name := range names {
		if extensions[name] {
			builder.WriteString("extension=")
		} else {
			builder.WriteString(";extension=")
		}
		builder.WriteString(name)
		builder.WriteByte('\n')
	}

	return []byte(builder.String()), nil
}

func validatePHPExtensionName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("php extension name cannot be empty")
	}
	if !validName.MatchString(trimmed) {
		return fmt.Errorf("invalid php extension name %q: use letters, numbers, dots, dashes, or underscores", name)
	}

	return nil
}

func (s Store) resolveInstalledTool(tool, version string) (string, error) {
	return s.resolveInstalledToolIn(s.EnvsDir, tool, version)
}

func (s Store) resolveInstalledDispatchExecutable(configTool, executable, version string) (string, error) {
	return resolveInstalledDispatchExecutableIn(s.EnvsDir, configTool, executable, version)
}

func (s Store) resolveInstalledToolIn(root, tool, version string) (string, error) {
	candidates := toolInstallCandidatesIn(root, tool, version)
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
	candidates := dispatchExecutableCandidatesIn(root, configTool, executable, version)
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
	installDir := filepath.Join(root, tool, version)

	switch tool {
	case toolPHP:
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
	case toolComposer:
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
	case toolNodeJS:
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
	case toolNginx:
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
	case toolMySQL, toolMariaDB:
		return databaseToolInstallCandidates(tool, installDir)
	default:
		return nil
	}
}

func dispatchExecutableCandidatesIn(root, configTool, executable, version string) []string {
	installDir := filepath.Join(root, configTool, version)

	switch configTool {
	case toolNodeJS:
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
		}
	}

	return nil
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

	for _, binary := range s.managedBinariesForEnvironment(s.currentEnvironmentFromConfig(config)) {
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
	return []installedBinary{
		shellDispatchBinary("php"),
		shellDispatchBinary("composer"),
		shellDispatchBinary(toolNode),
		shellDispatchBinary(toolNPM),
		shellDispatchBinary(toolNPX),
		shellDispatchBinary(toolNodeJS),
		shellDispatchBinary(toolNginx),
		shellDispatchBinary(toolMySQL),
		shellDispatchBinary(toolMariaDB),
		windowsDispatchBinary("php"),
		windowsDispatchBinary("composer"),
		windowsDispatchBinary(toolNode),
		windowsDispatchBinary(toolNPM),
		windowsDispatchBinary(toolNPX),
		windowsDispatchBinary(toolNodeJS),
		windowsDispatchBinary(toolNginx),
		windowsDispatchBinary(toolMySQL),
		windowsDispatchBinary(toolMariaDB),
	}
}

func (s Store) managedBinariesForEnvironment(environment *Environment) []installedBinary {
	tools := managedToolsForEnvironment(environment)
	binaries := make([]installedBinary, 0, len(tools)*2)
	for _, tool := range tools {
		binaries = append(binaries, shellDispatchBinary(tool), windowsDispatchBinary(tool))
	}

	return binaries
}

func (s Store) currentEnvironmentFromConfig(config Config) *Environment {
	if config.Current == "" {
		return nil
	}

	environment, ok := config.Environments[config.Current]
	if !ok {
		return nil
	}

	normalized := s.normalizeEnvironment(config.Current, environment)
	return &normalized
}

func managedToolsForEnvironment(environment *Environment) []string {
	if environment == nil {
		return nil
	}

	tools := make([]string, 0, 7)
	if environment.PHPVersion != "" {
		tools = append(tools, toolPHP)
	}
	if environment.ComposerVersion != "" {
		tools = append(tools, toolComposer)
	}
	if environment.NodeJSVersion != "" {
		tools = append(tools, toolNode, toolNPM, toolNPX)
	}
	if environment.NginxVersion != "" {
		tools = append(tools, toolNginx)
	}
	if environment.Database != nil && environment.Database.Engine != "" {
		tools = append(tools, environment.Database.Engine)
	}

	return tools
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

func copyFile(sourcePath, targetPath string, mode os.FileMode) error {
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer targetFile.Close()

	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		return err
	}

	return nil
}

func copyDir(sourcePath, targetPath string) error {
	fileInfo, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(targetPath, fileInfo.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(sourcePath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		sourceEntryPath := filepath.Join(sourcePath, entry.Name())
		targetEntryPath := filepath.Join(targetPath, entry.Name())

		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}

		if entry.IsDir() {
			if err := copyDir(sourceEntryPath, targetEntryPath); err != nil {
				return err
			}

			continue
		}

		if err := copyFile(sourceEntryPath, targetEntryPath, entryInfo.Mode()); err != nil {
			return err
		}
	}

	return nil
}

func dispatcherBinaryFileName() string {
	if runtime.GOOS == "windows" {
		return dispatcherBatchFileName
	}

	return dispatcherBinaryName
}

func (e Environment) toolVersion(tool string) string {
	switch tool {
	case toolPHP:
		return e.PHPVersion
	case toolComposer:
		return e.ComposerVersion
	case toolNodeJS, toolNode, toolNPM, toolNPX:
		return e.NodeJSVersion
	case toolNginx:
		return e.NginxVersion
	case toolMySQL, toolMariaDB:
		return e.databaseToolVersion(tool)
	default:
		return ""
	}
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
	trimmed := strings.ToLower(strings.TrimSpace(tool))
	switch trimmed {
	case toolPHP, toolComposer, toolNginx, toolMySQL, toolMariaDB:
		return toolRequest{ConfigTool: trimmed, Executable: trimmed}, nil
	case toolNode, toolNPM, toolNPX:
		return toolRequest{ConfigTool: toolNodeJS, Executable: trimmed}, nil
	default:
		return toolRequest{}, fmt.Errorf("unsupported tool %q", tool)
	}
}

func defaultCacheDir(projectDir string) string {
	if override := strings.TrimSpace(os.Getenv("Polka_CACHE_DIR")); override != "" {
		return filepath.Clean(filepath.FromSlash(override))
	}

	cacheDir, err := os.UserCacheDir()
	if err == nil {
		return filepath.Join(cacheDir, "polka", "tools")
	}

	return filepath.Join(projectDir, ".polka-cache")
}

func isMissingInstall(err error) bool {
	return err != nil && strings.Contains(err.Error(), " is not installed under ")
}
