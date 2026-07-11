package backend

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"polka/config"
	"polka/tools"
)

// RunPIE provisions the internal PHP runtime and PIE PHAR on demand, then
// executes `<internal-php> pie.phar args...` streaming output to the given
// writers. It returns the PIE process exit code. Both the polka ext command
// and the install pipeline's extension provisioning use this entry point.
func (s Store) RunPIE(stdout, stderr io.Writer, args []string, report func(InstallProgress)) (int, error) {
	phpPath, err := s.EnsureInternalTool(toolPHP, "", report)
	if err != nil {
		return 0, err
	}
	piePath, err := s.EnsureInternalTool(toolPIE, "", report)
	if err != nil {
		return 0, err
	}

	command, err := prepareInternalPHPCommand(phpPath, append([]string{piePath}, args...))
	if err != nil {
		return 0, err
	}
	command.Env = internalPHPProcessEnv(os.Environ(), phpPath)
	command.Stdout = stdout
	command.Stderr = stderr

	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return exitError.ExitCode(), nil
		}

		return 0, fmt.Errorf("run pie: %w", err)
	}

	return 0, nil
}

// PIEExtensionInstallArgs builds the PIE arguments that install a package
// against a specific PHP binary. --skip-enable-extension is required because
// polka regenerates php.ini itself and would clobber any ini line PIE adds.
func PIEExtensionInstallArgs(projectPHPPath, pkg, version string) []string {
	spec := pkg
	if trimmed := strings.TrimSpace(version); trimmed != "" && trimmed != "*" {
		spec = pkg + ":" + trimmed
	}

	return []string{"install", "--with-php-path=" + projectPHPPath, "--skip-enable-extension", spec}
}

// PIEExtensionUninstallArgs builds the PIE arguments that remove a package
// from a specific PHP binary's extension directory.
func PIEExtensionUninstallArgs(projectPHPPath, pkg string) []string {
	return []string{"uninstall", "--with-php-path=" + projectPHPPath, pkg}
}

// prepareInternalPHPCommand wraps batch files on Windows so fake test PHP
// runtimes (.cmd shims) execute like real php.exe binaries.
func prepareInternalPHPCommand(target string, args []string) (*exec.Cmd, error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return nil, fmt.Errorf("php target cannot be empty")
	}

	if runtime.GOOS == "windows" {
		extension := strings.ToLower(filepath.Ext(trimmed))
		if extension == ".cmd" || extension == ".bat" {
			return exec.Command("cmd.exe", append([]string{"/c", trimmed}, args...)...), nil
		}
	}

	return exec.Command(trimmed, args...), nil
}

// internalPHPProcessEnv sets PHPRC and OPENSSL_CONF for the internal PHP so
// its generated php.ini and OpenSSL config load on every platform: Linux PHP
// does not search the executable's directory for php.ini.
func internalPHPProcessEnv(base []string, phpPath string) []string {
	env := append([]string(nil), base...)
	phpDir := filepath.Dir(phpPath)

	if iniPath := filepath.Join(phpDir, "php.ini"); regularFileExistsAt(iniPath) {
		env = setProcessEnvValue(env, "PHPRC", iniPath)
	}
	opensslCandidates := []string{
		filepath.Join(phpDir, "extras", "ssl", "openssl.cnf"),
		filepath.Join(filepath.Dir(phpDir), "extras", "ssl", "openssl.cnf"),
	}
	for _, candidate := range opensslCandidates {
		if regularFileExistsAt(candidate) {
			env = setProcessEnvValue(env, "OPENSSL_CONF", candidate)
			break
		}
	}

	return env
}

// setProcessEnvValue replaces or appends one variable in an environment
// slice; keys compare case-insensitively on Windows.
func setProcessEnvValue(env []string, key, value string) []string {
	entry := key + "=" + value
	for index, existing := range env {
		name, _, ok := strings.Cut(existing, "=")
		if !ok {
			continue
		}
		if name == key || (runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			env[index] = entry
			return env
		}
	}

	return append(env, entry)
}

// regularFileExistsAt reports whether path is an existing regular file.
func regularFileExistsAt(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// installPIEExtensions provisions the vendor/name php-extensions entries that
// are not yet loadable by the environment's installed PHP, using the internal
// PIE. It runs after the tool installs so the target PHP binary exists; the
// generated php.ini already lists the modules, so PHP only reports startup
// warnings until the extension binaries land, within this same install run.
func (s Store) installPIEExtensions(environment Environment, report func(InstallProgress)) error {
	pieExtensions := config.NormalizePIEExtensions(environment.PIEExtensions)
	if len(pieExtensions) == 0 {
		return nil
	}

	tool, version := config.PrimaryPHPTool(environment)
	if tool == "" {
		return fmt.Errorf("environment %q defines PIE-managed php-extensions but no standalone php or php-zts runtime; PIE cannot target FrankenPHP's embedded PHP", environment.Name)
	}
	projectPHP, err := s.resolveInstalledTool(tool, version)
	if err != nil {
		return err
	}

	modules, err := tools.InstalledPHPModules(projectPHP)
	if err != nil {
		return err
	}

	// Prefix internal tool provisioning progress so it cannot collide with
	// the project php entry a progress display registered for this install.
	internalReport := func(progress InstallProgress) {
		if report == nil {
			return
		}
		progress.Tool = "internal " + progress.Tool
		report(progress)
	}

	names := make([]string, 0, len(pieExtensions))
	for pkg := range pieExtensions {
		names = append(names, pkg)
	}
	sort.Strings(names)

	for index, pkg := range names {
		constraint := pieExtensions[pkg]
		progress := InstallProgress{Index: index + 1, Total: len(names), Tool: pkg, Version: constraint}
		module := tools.PIEExtensionModuleName(pkg)

		// Skip extensions PHP already loads and modules the user explicitly
		// disabled via a bundled toggle.
		if modules[module] {
			emitInstallProgress(report, progress, InstallProgressSkipped)
			continue
		}
		if enabled, exists := environment.PHPExtensions[module]; exists && !enabled {
			emitInstallProgress(report, progress, InstallProgressSkipped)
			continue
		}

		emitInstallProgress(report, progress, InstallProgressInstalling)
		var output bytes.Buffer
		exitCode, err := s.RunPIE(&output, &output, PIEExtensionInstallArgs(projectPHP, pkg, constraint), internalReport)
		if err != nil {
			return err
		}
		if exitCode != 0 {
			return fmt.Errorf("pie install %s failed with exit code %d:\n%s", pkg, exitCode, strings.TrimSpace(output.String()))
		}
		emitInstallProgress(report, progress, InstallProgressInstalled)
	}

	return nil
}

// ResolveInstalledTool returns the executable path of an installed tool
// version inside the project envs directory.
func (s Store) ResolveInstalledTool(tool, version string) (string, error) {
	return s.resolveInstalledTool(tool, version)
}

// SyncPHPRuntimeConfig regenerates the php.ini for the environment's
// installed standalone PHP runtime from the current effective config,
// applying framework, tool, bundled, and PIE-managed extensions.
func (s Store) SyncPHPRuntimeConfig(name string) error {
	environment, _, err := s.installEnvironment(name)
	if err != nil {
		return err
	}
	tool, version := config.PrimaryPHPTool(environment)
	if tool == "" {
		return fmt.Errorf("environment %q does not define a standalone php or php-zts runtime", name)
	}

	return tools.ResyncInstalledPHPRuntimeConfig(s.EnvsDir, tool, version, s.withFrameworkPHPConfig(s.withInstalledPECLExtensions(environment)))
}
