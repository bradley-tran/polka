package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
)

const (
	defaultWindowsShell = "powershell.exe"
	vendorDirectoryName = "vendor"
	binDirectoryName    = "bin"
	posixInteractiveArg = "-i"
	polkaPromptRootEnv  = "POLKA_PROMPT_ROOT"
	polkaPromptEnvEnv   = "POLKA_PROMPT_ENVIRONMENT"
	shellRuntimeDirName = "run"
	shellSupportDirName = "shell"
	vendorShimDirName   = "vendor-bin"
)

var (
	launchInteractiveShellFunc = launchInteractiveShell
)

type shellSessionContext struct {
	EnvironmentName  string
	RootDir          string
	PromptRoot       string
	VendorProjectDir string
	VendorBinDir     string
	VendorShimDir    string
	PathEntries      []string
}

func newShCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sh",
		Aliases: []string{"shell"},
		Args:    exactArgsError("sh/shell does not take arguments", 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			ctx.exitCode = runSh(cmd.OutOrStdout(), cmd.ErrOrStderr(), store)
			return nil
		},
	}
	configureCommand(cmd, shUsage)

	return cmd
}

func runSh(stdout, stderr io.Writer, store backend.Store) int {
	target, args := resolveInteractiveShell(runtime.GOOS, os.Getenv("SHELL"))
	environmentName, err := currentShellEnvironmentName(store)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	workingDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "error: resolve current working directory: %v\n", err)
		return 1
	}
	env, err := prepareShellEnvironment(runtime.GOOS, os.Environ(), store, workingDir, environmentName)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	writeShellBanner(stdout, environmentName)

	exitCode, err := launchInteractiveShellFunc(stdout, stderr, env, target, args)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	return exitCode
}

func launchInteractiveShell(stdout, stderr io.Writer, env []string, target string, args []string) (int, error) {
	return executeTargetWithEnv(stdout, stderr, env, target, args)
}

func resolveInteractiveShell(goos, shellEnv string) (string, []string) {
	if goos == "windows" {
		return defaultWindowsShell, windowsInteractiveShellArgs()
	}

	trimmedShell := strings.TrimSpace(shellEnv)
	if trimmedShell != "" {
		return trimmedShell, []string{posixInteractiveArg}
	}

	return "/bin/sh", []string{posixInteractiveArg}
}

func currentShellEnvironmentName(store backend.Store) (string, error) {
	current, err := store.Current()
	if err != nil {
		return "", err
	}
	if current == nil || strings.TrimSpace(current.Name) == "" {
		return "none", nil
	}

	return current.Name, nil
}

func writeShellBanner(stdout io.Writer, environmentName string) {
	if stdout == nil {
		return
	}

	_, _ = fmt.Fprintf(stdout, "Opened Polka shell for environment %s\n", environmentName)
}

func windowsInteractiveShellArgs() []string {
	return []string{"-NoLogo", "-NoExit", "-Command", windowsPromptCommand()}
}

func windowsPromptCommand() string {
	return strings.Join([]string{
		"& {",
		"function global:Get-PolkaPromptPath {",
		"param([string]$root, [string]$current)",
		"$normalizedRoot = [System.IO.Path]::GetFullPath($root).TrimEnd('\\', '/')",
		"$normalizedCurrent = [System.IO.Path]::GetFullPath($current).TrimEnd('\\', '/')",
		"if ($normalizedCurrent.Equals($normalizedRoot, [System.StringComparison]::OrdinalIgnoreCase)) { return '.' }",
		"$rootPrefix = $normalizedRoot + [System.IO.Path]::DirectorySeparatorChar",
		"if ($normalizedCurrent.StartsWith($rootPrefix, [System.StringComparison]::OrdinalIgnoreCase)) { return $normalizedCurrent.Substring($rootPrefix.Length).Replace('\\', '/') }",
		"if ([System.IO.Path]::GetPathRoot($normalizedCurrent).Equals([System.IO.Path]::GetPathRoot($normalizedRoot), [System.StringComparison]::OrdinalIgnoreCase)) {",
		"$rootUri = New-Object System.Uri(($normalizedRoot + [System.IO.Path]::DirectorySeparatorChar))",
		"$currentUri = New-Object System.Uri(($normalizedCurrent + [System.IO.Path]::DirectorySeparatorChar))",
		"$relative = [System.Uri]::UnescapeDataString($rootUri.MakeRelativeUri($currentUri).ToString()).TrimEnd('/')",
		"if (-not [string]::IsNullOrWhiteSpace($relative)) { return $relative }",
		"}",
		"return $normalizedCurrent.Replace('\\', '/')",
		"}",
		"function global:prompt {",
		"$root = $env:" + polkaPromptRootEnv,
		"$name = $env:" + polkaPromptEnvEnv,
		"if ([string]::IsNullOrWhiteSpace($name)) { $name = 'none' }",
		"if ([string]::IsNullOrWhiteSpace($root)) { ('polka . (' + $name + ') > '); return }",
		"$current = (Get-Location).ProviderPath",
		"$relative = Get-PolkaPromptPath $root $current",
		"('polka ' + $relative + ' (' + $name + ') > ')",
		"}",
		"}",
	}, "\n")
}

func prepareShellEnvironment(goos string, env []string, store backend.Store, workingDir, environmentName string) ([]string, error) {
	context, err := buildShellSessionContext(goos, store, workingDir, environmentName)
	if err != nil {
		return nil, err
	}

	return buildShellExecutionEnvironment(goos, env, store, context)
}

func buildShellExecutionEnvironment(goos string, env []string, store backend.Store, context shellSessionContext) ([]string, error) {
	resolvedEnv, err := resolveRuntimeEnvironment(goos, env, store)
	if err != nil {
		return nil, err
	}

	pathKey, systemPath, _ := lookupEnvValue(goos, resolvedEnv, "PATH")
	if pathKey == "" {
		pathKey = "PATH"
	}

	pathEntries := make([]string, 0, len(context.PathEntries)+1)
	pathEntries = append(pathEntries, context.PathEntries...)
	pathEntries = append(pathEntries, systemPath)
	resolvedPath := joinPathList(goos, pathEntries...)
	updatedEnv := replaceEnvValue(goos, resolvedEnv, pathKey, resolvedPath)
	updatedEnv = replaceEnvValue(goos, updatedEnv, polkaPromptEnvEnv, context.EnvironmentName)

	return replaceEnvValue(goos, updatedEnv, polkaPromptRootEnv, context.PromptRoot), nil
}

func buildShellSessionContext(goos string, store backend.Store, workingDir, environmentName string) (shellSessionContext, error) {
	rootDir, err := filepath.Abs(store.RootDir)
	if err != nil {
		return shellSessionContext{}, fmt.Errorf("resolve Polka root directory: %w", err)
	}
	binDir, err := filepath.Abs(store.BinDir)
	if err != nil {
		return shellSessionContext{}, fmt.Errorf("resolve Polka bin directory: %w", err)
	}
	shellProjectDir := shellPathRoot(rootDir)
	vendorProjectDir, err := discoverVendorProjectDir(workingDir, shellProjectDir)
	if err != nil {
		return shellSessionContext{}, err
	}
	vendorBinDir := filepath.Join(vendorProjectDir, vendorDirectoryName, binDirectoryName)
	promptRoot, err := shellPromptRoot(shellProjectDir)
	if err != nil {
		return shellSessionContext{}, err
	}

	context := shellSessionContext{
		EnvironmentName:  environmentName,
		RootDir:          rootDir,
		PromptRoot:       promptRoot,
		VendorProjectDir: vendorProjectDir,
		VendorBinDir:     vendorBinDir,
		PathEntries:      []string{binDir},
	}
	if goos == "windows" {
		context.VendorShimDir, err = prepareWindowsVendorBinPHPSupport(vendorBinDir, rootDir)
		if err != nil {
			return shellSessionContext{}, err
		}
		if context.VendorShimDir != "" {
			context.PathEntries = append(context.PathEntries, context.VendorShimDir)
		}
	}
	context.PathEntries = append(context.PathEntries, vendorBinDir)

	return context, nil
}

func discoverVendorProjectDir(workingDir, projectRoot string) (string, error) {
	currentDir, err := filepath.Abs(workingDir)
	if err != nil {
		return "", fmt.Errorf("resolve vendor project directory: %w", err)
	}
	resolvedProjectRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve Polka project root: %w", err)
	}

	for {
		if directoryExists(filepath.Join(currentDir, vendorDirectoryName, binDirectoryName)) {
			return currentDir, nil
		}
		if samePath(currentDir, resolvedProjectRoot) {
			return resolvedProjectRoot, nil
		}

		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			return resolvedProjectRoot, nil
		}
		currentDir = parentDir
	}
}

func buildShellEnvironment(goos string, env []string, store backend.Store) []string {
	pathKey, systemPath, _ := lookupEnvValue(goos, env, "PATH")
	if pathKey == "" {
		pathKey = "PATH"
	}
	shellProjectDir := shellPathRoot(store.RootDir)

	resolvedPath := joinPathList(goos,
		store.BinDir,
		filepath.Join(shellProjectDir, vendorDirectoryName, binDirectoryName),
		systemPath,
	)

	return replaceEnvValue(goos, env, pathKey, resolvedPath)
}

func shellPathRoot(rootDir string) string {
	return filepath.Dir(filepath.Clean(rootDir))
}

func prepareWindowsVendorBinPHPSupport(vendorBinDir, rootDir string) (string, error) {
	targets, err := windowsVendorBinPHPTargets(vendorBinDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}

		return "", fmt.Errorf("scan vendor/bin commands: %w", err)
	}
	if len(targets) == 0 {
		return "", nil
	}

	shimDir := filepath.Join(rootDir, shellRuntimeDirName, shellSupportDirName, vendorShimDirName)
	if err := recreateDirectory(shimDir); err != nil {
		return "", fmt.Errorf("prepare vendor php shim directory: %w", err)
	}
	for _, target := range targets {
		if err := writeWindowsVendorPHPShim(shimDir, target.CommandName, target.TargetPath); err != nil {
			return "", err
		}
	}

	return shimDir, nil
}

type vendorBinPHPTarget struct {
	CommandName string
	TargetPath  string
}

func windowsVendorBinPHPTargets(vendorBinDir string) ([]vendorBinPHPTarget, error) {
	entries, err := os.ReadDir(vendorBinDir)
	if err != nil {
		return nil, err
	}

	targets := make([]vendorBinPHPTarget, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != "" {
			continue
		}

		commandPath := filepath.Join(vendorBinDir, entry.Name())
		targetPath, err := resolveVendorBinPHPTarget(commandPath)
		if err != nil {
			return nil, err
		}
		if targetPath != "" {
			targets = append(targets, vendorBinPHPTarget{CommandName: entry.Name(), TargetPath: targetPath})
		}
	}

	sort.Slice(targets, func(left, right int) bool {
		return targets[left].CommandName < targets[right].CommandName
	})
	return targets, nil
}

func resolveVendorBinPHPTarget(commandPath string) (string, error) {
	line, err := readScriptFirstLine(commandPath)
	if err != nil {
		return "", err
	}

	if isPHPShebang(line) {
		return commandPath, nil
	}
	if !isPOSIXShellShebang(line) {
		return "", nil
	}

	siblingPHPTarget := commandPath + ".php"
	if fileExists(siblingPHPTarget) {
		return siblingPHPTarget, nil
	}

	return "", nil
}

func readScriptFirstLine(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	line, err := bufio.NewReader(file).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read shebang from %s: %w", path, err)
	}

	return line, nil
}

func isPHPShebang(line string) bool {
	return shebangUsesProgram(line, "php")
}

func isPOSIXShellShebang(line string) bool {
	return shebangUsesProgram(line, "sh", "bash")
}

func shebangUsesProgram(line string, names ...string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#!") {
		return false
	}

	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(trimmed, "#!")))
	if len(fields) == 0 {
		return false
	}

	interpreter := fields[0]
	if strings.EqualFold(filepath.Base(interpreter), "env") {
		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "-") || strings.Contains(field, "=") {
				continue
			}

			interpreter = field
			break
		}
	}

	base := strings.ToLower(strings.TrimSpace(filepath.Base(interpreter)))
	for _, name := range names {
		lowerName := strings.ToLower(strings.TrimSpace(name))
		if base == lowerName || base == lowerName+".exe" {
			return true
		}
	}

	return false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func samePath(left, right string) bool {
	cleanLeft := filepath.Clean(left)
	cleanRight := filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(cleanLeft, cleanRight)
	}

	return cleanLeft == cleanRight
}

func shellPromptRoot(projectDir string) (string, error) {
	absolutePath, err := filepath.Abs(projectDir)
	if err != nil {
		return "", fmt.Errorf("resolve prompt root: %w", err)
	}

	return absolutePath, nil
}

func recreateDirectory(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return err
	}

	return os.MkdirAll(path, 0o755)
}

func writeWindowsVendorPHPShim(shimDir, commandName, targetPath string) error {
	shimPath := filepath.Join(shimDir, commandName+".cmd")
	contents := "@echo off\r\n" +
		"setlocal\r\n" +
		"call php \"" + escapeWindowsShimValue(targetPath) + "\" %*\r\n" +
		"exit /b %ERRORLEVEL%\r\n"

	if err := os.WriteFile(shimPath, []byte(contents), 0o755); err != nil {
		return fmt.Errorf("write vendor php shim %q: %w", shimPath, err)
	}

	return nil
}

func escapeWindowsShimValue(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}

func lookupEnvValue(goos string, env []string, key string) (string, string, bool) {
	for _, entry := range env {
		entryKey, entryValue, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if envKeysEqual(goos, entryKey, key) {
			return entryKey, entryValue, true
		}
	}

	return "", "", false
}

func replaceEnvValue(goos string, env []string, key, value string) []string {
	updated := make([]string, 0, len(env)+1)
	replaced := false

	for _, entry := range env {
		entryKey, _, ok := strings.Cut(entry, "=")
		if !ok {
			updated = append(updated, entry)
			continue
		}
		if !envKeysEqual(goos, entryKey, key) {
			updated = append(updated, entry)
			continue
		}
		if replaced {
			continue
		}

		updated = append(updated, entryKey+"="+value)
		replaced = true
	}

	if !replaced {
		updated = append(updated, key+"="+value)
	}

	return updated
}

func envKeysEqual(goos, left, right string) bool {
	if goos == "windows" {
		return strings.EqualFold(left, right)
	}

	return left == right
}

func joinPathList(goos string, entries ...string) string {
	nonEmpty := make([]string, 0, len(entries))
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}

		nonEmpty = append(nonEmpty, trimmed)
	}

	return strings.Join(nonEmpty, pathListSeparator(goos))
}

func pathListSeparator(goos string) string {
	if goos == "windows" {
		return ";"
	}

	return ":"
}
