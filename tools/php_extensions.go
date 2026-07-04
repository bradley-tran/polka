package tools

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"polka/config"
)

// PHPInstallConfig is the effective generated php.ini content for an environment.
type PHPInstallConfig struct {
	Extensions    map[string]bool
	MemoryLimit   string
	OPcacheConfig map[string]string
	CABundlePath  string
}

// IsZero reports whether the install needs no generated php.ini.
func (config PHPInstallConfig) IsZero() bool {
	return len(config.Extensions) == 0 && strings.TrimSpace(config.MemoryLimit) == "" && len(config.OPcacheConfig) == 0 && strings.TrimSpace(config.CABundlePath) == ""
}

// EffectivePHPConfigForInstall returns all generated PHP ini settings for an environment.
func EffectivePHPConfigForInstall(environment config.Environment) PHPInstallConfig {
	return PHPInstallConfig{
		Extensions:    EffectivePHPExtensionsForInstall(environment),
		MemoryLimit:   config.NormalizePHPMemoryLimit(environment.MemoryLimit),
		OPcacheConfig: EffectiveOPcacheConfigForInstall(environment),
	}
}

func EffectivePHPExtensionsForInstall(environment config.Environment) map[string]bool {
	extensions := config.NormalizePHPExtensions(environment.PHPExtensions)
	if len(environment.PIEExtensions) == 0 {
		return extensions
	}

	// Fold PIE-managed packages into the extension toggles so the generated
	// php.ini loads their modules. An explicit bundled toggle for the same
	// module wins, letting users disable a PIE-installed extension.
	if extensions == nil {
		extensions = map[string]bool{}
	}
	for pkg := range config.NormalizePIEExtensions(environment.PIEExtensions) {
		module := PIEExtensionModuleName(pkg)
		if module == "" {
			continue
		}
		if _, exists := extensions[module]; !exists {
			extensions[module] = true
		}
	}

	return extensions
}

// PIEExtensionModuleName derives the loadable PHP module name from a
// Composer-style vendor/name package: the name after the slash. Packages
// whose module name differs from the package name are not supported yet.
func PIEExtensionModuleName(pkg string) string {
	name := strings.ToLower(strings.TrimSpace(pkg))
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}

	return name
}

func normalizeManifestPHPExtensions(extensions []string) []string {
	normalized := make([]string, 0, len(extensions))
	seen := make(map[string]struct{}, len(extensions))
	for _, extension := range extensions {
		name := strings.ToLower(strings.TrimSpace(extension))
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	return normalized
}

func manifestPHPExtensionMap(extensions []string) map[string]bool {
	if len(extensions) == 0 {
		return nil
	}

	result := make(map[string]bool, len(extensions))
	for _, extension := range extensions {
		result[extension] = true
	}
	return result
}

// EffectiveOPcacheConfigForInstall overlays explicit directives over the selected preset.
func EffectiveOPcacheConfigForInstall(environment config.Environment) map[string]string {
	effective := config.OPcachePresetConfig(environment.OPcachePreset)
	if len(environment.OPcacheConfig) == 0 {
		return effective
	}
	if effective == nil {
		effective = map[string]string{}
	}
	for name, value := range environment.OPcacheConfig {
		effective[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
	}

	return effective
}

func configureInstalledPHPExtensions(envsDir, version string, extensions map[string]bool) error {
	return configureInstalledPHPConfig(envsDir, version, PHPInstallConfig{Extensions: extensions})
}

func configureInstalledPHPConfig(envsDir, version string, phpConfig PHPInstallConfig) error {
	return configureInstalledPHPConfigForTool(envsDir, PHP, version, phpConfig)
}

func configureInstalledPHPConfigForTool(envsDir, tool, version string, phpConfig PHPInstallConfig) error {
	if phpConfig.IsZero() {
		return nil
	}

	return writeInstalledPHPConfigForTool(envsDir, tool, version, phpConfig)
}

// writeInstalledPHPConfigForTool renders and writes the generated php.ini
// unconditionally, even for an empty effective config, so shrinking the
// config clears previously generated directives.
func writeInstalledPHPConfigForTool(envsDir, tool, version string, phpConfig PHPInstallConfig) error {
	phpPath, err := resolveInstalledTool(NewDefaultRegistry(), envsDir, tool, version)
	if err != nil {
		return err
	}

	phpDir := filepath.Dir(phpPath)
	return configurePHPConfigAt(
		phpPath,
		filepath.Join(envsDir, tool, version, "ext"),
		filepath.Join(phpDir, "php.ini"),
		phpConfig,
	)
}

// configurePHPConfigAt writes php.ini for a specific PHP executable and extension directory.
func configurePHPConfigAt(phpPath, extensionPath, phpIniPath string, phpConfig PHPInstallConfig) error {
	phpDir := filepath.Dir(phpIniPath)
	extensionDir, err := filepath.Rel(phpDir, extensionPath)
	if err != nil {
		extensionDir = extensionPath
	}

	if len(phpConfig.Extensions) > 0 {
		builtInExtensions, err := installedPHPBuiltInExtensions(phpPath)
		if err != nil {
			return err
		}
		phpConfig.Extensions = skipBuiltInPHPExtensions(phpConfig.Extensions, builtInExtensions)
	}

	configData, err := renderPHPConfig(filepath.ToSlash(extensionDir), phpConfig)
	if err != nil {
		return err
	}

	if err := os.WriteFile(phpIniPath, configData, 0o644); err != nil {
		return fmt.Errorf("write php config: %w", err)
	}

	return nil
}

func renderPHPExtensionConfig(extensionDir string, extensions map[string]bool) ([]byte, error) {
	return renderPHPConfig(extensionDir, PHPInstallConfig{Extensions: extensions})
}

// installedPHPBuiltInExtensions asks PHP for modules available without php.ini.
func installedPHPBuiltInExtensions(phpPath string) (map[string]bool, error) {
	return listPHPModules(phpPath, []string{"-nm"}, nil, "list built-in PHP extensions")
}

// InstalledPHPModules asks PHP which modules it currently loads, applying the
// generated php.ini beside the binary so PIE-provisioned extensions are
// detected on every platform (Linux PHP does not search the executable's
// directory for php.ini).
func InstalledPHPModules(phpPath string) (map[string]bool, error) {
	var env []string
	iniPath := filepath.Join(filepath.Dir(phpPath), "php.ini")
	if info, err := os.Stat(iniPath); err == nil && !info.IsDir() {
		env = append(os.Environ(), "PHPRC="+iniPath)
	}

	return listPHPModules(phpPath, []string{"-m"}, env, "list installed PHP modules")
}

// listPHPModules runs php with the given module-listing arguments and parses
// the output. Startup warnings about missing extension binaries land on
// stderr and do not fail the listing.
func listPHPModules(phpPath string, args, env []string, label string) (map[string]bool, error) {
	command, err := preparePHPCommand(phpPath, args)
	if err != nil {
		return nil, err
	}
	if env != nil {
		command.Env = env
	}

	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return nil, fmt.Errorf("%s: %w: %s", label, err, detail)
		}
		return nil, fmt.Errorf("%s: %w", label, err)
	}

	return parsePHPModuleList(output), nil
}

// preparePHPCommand wraps batch files on Windows so test and shim targets run.
func preparePHPCommand(target string, args []string) (*exec.Cmd, error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return nil, fmt.Errorf("php target cannot be empty")
	}

	if runtime.GOOS == "windows" {
		extension := strings.ToLower(filepath.Ext(trimmed))
		if extension == ".cmd" || extension == ".bat" {
			commandArgs := append([]string{"/c", trimmed}, args...)
			return exec.Command("cmd.exe", commandArgs...), nil
		}
	}

	return exec.Command(trimmed, args...), nil
}

// parsePHPModuleList returns the normalized module names printed by php -m.
func parsePHPModuleList(output []byte) map[string]bool {
	modules := map[string]bool{}
	for _, line := range strings.Split(string(output), "\n") {
		normalized := normalizePHPModuleName(line)
		if normalized != "" {
			modules[normalized] = true
		}
	}

	return modules
}

// normalizePHPModuleName converts php -m lines into config extension keys.
func normalizePHPModuleName(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" || strings.HasPrefix(normalized, "[") && strings.HasSuffix(normalized, "]") {
		return ""
	}
	if strings.HasPrefix(normalized, "zend ") {
		normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "zend "))
	}

	return normalized
}

// skipBuiltInPHPExtensions removes php.ini entries PHP already has compiled in.
func skipBuiltInPHPExtensions(extensions map[string]bool, builtInExtensions map[string]bool) map[string]bool {
	if len(extensions) == 0 || len(builtInExtensions) == 0 {
		return extensions
	}

	filtered := make(map[string]bool, len(extensions))
	for name, enabled := range extensions {
		if builtInExtensions[normalizePHPModuleName(name)] {
			continue
		}
		filtered[name] = enabled
	}

	return filtered
}

func renderPHPConfig(extensionDir string, phpConfig PHPInstallConfig) ([]byte, error) {
	names := make([]string, 0, len(phpConfig.Extensions))
	for name := range phpConfig.Extensions {
		if err := validatePHPExtensionName(name); err != nil {
			return nil, err
		}

		names = append(names, name)
	}
	sort.Strings(names)

	opcacheNames := make([]string, 0, len(phpConfig.OPcacheConfig))
	opcacheConfig := make(map[string]string, len(phpConfig.OPcacheConfig))
	for name, value := range phpConfig.OPcacheConfig {
		if err := validateOPcacheDirective(name, value); err != nil {
			return nil, err
		}

		normalized := strings.ToLower(strings.TrimSpace(name))
		opcacheNames = append(opcacheNames, normalized)
		opcacheConfig[normalized] = strings.TrimSpace(value)
	}
	sort.Strings(opcacheNames)

	var builder strings.Builder
	builder.WriteString("; Generated by Polka. Re-run polka install after editing php-extensions, memory-limit, or OPcache config.\n")
	builder.WriteString("[PHP]\n")
	if len(names) > 0 {
		builder.WriteString("extension_dir=\"")
		builder.WriteString(extensionDir)
		builder.WriteString("\"\n")
	}
	memoryLimit := config.NormalizePHPMemoryLimit(phpConfig.MemoryLimit)
	if memoryLimit != "" {
		if err := config.ValidatePHPMemoryLimit(memoryLimit); err != nil {
			return nil, err
		}
		builder.WriteString("memory_limit=")
		builder.WriteString(memoryLimit)
		builder.WriteByte('\n')
	}
	caBundlePath := strings.TrimSpace(phpConfig.CABundlePath)
	if caBundlePath != "" {
		iniPath, err := phpINIPathValue(caBundlePath)
		if err != nil {
			return nil, err
		}
		builder.WriteString("curl.cainfo=\"")
		builder.WriteString(iniPath)
		builder.WriteString("\"\n")
		builder.WriteString("openssl.cafile=\"")
		builder.WriteString(iniPath)
		builder.WriteString("\"\n")
	}
	for _, name := range names {
		if strings.EqualFold(name, "opcache") {
			if phpConfig.Extensions[name] {
				builder.WriteString("zend_extension=")
			} else {
				builder.WriteString(";zend_extension=")
			}
		} else if phpConfig.Extensions[name] {
			builder.WriteString("extension=")
		} else {
			builder.WriteString(";extension=")
		}
		builder.WriteString(name)
		builder.WriteByte('\n')
	}
	if len(opcacheNames) > 0 {
		if len(names) > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString("[opcache]\n")
	}
	for _, name := range opcacheNames {
		builder.WriteString(name)
		builder.WriteByte('=')
		builder.WriteString(opcacheConfig[name])
		builder.WriteByte('\n')
	}

	return []byte(builder.String()), nil
}

func phpINIPathValue(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}
	if strings.ContainsAny(trimmed, "\r\n\"") {
		return "", fmt.Errorf("invalid PHP ini path %q: paths cannot contain quotes or newlines", path)
	}

	return filepath.ToSlash(trimmed), nil
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

func validateOPcacheDirective(name, value string) error {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if !strings.HasPrefix(trimmed, "opcache.") || trimmed == "opcache." {
		return fmt.Errorf("invalid OPcache directive %q: use opcache.* names", name)
	}
	if !validName.MatchString(trimmed) {
		return fmt.Errorf("invalid OPcache directive %q: use letters, numbers, dots, dashes, or underscores", name)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("invalid OPcache value for %q: values must be single-line scalars", name)
	}

	return nil
}
