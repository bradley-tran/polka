package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"polka/backend"
)

func TestRunShLaunchesInteractiveShellWithPreferredPath(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	systemPath := filepath.Join(projectDir, "system-bin")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {PHP: "8.4"},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")

	t.Setenv("PATH", systemPath)
	if runtime.GOOS != "windows" {
		t.Setenv("SHELL", filepath.Join(projectDir, "bin", "custom-shell"))
	}

	oldLaunch := launchInteractiveShellFunc
	t.Cleanup(func() {
		launchInteractiveShellFunc = oldLaunch
	})

	var capturedTarget string
	var capturedArgs []string
	var capturedEnv []string
	launchInteractiveShellFunc = func(stdout, stderr io.Writer, env []string, target string, args []string) (int, error) {
		capturedTarget = target
		capturedArgs = append([]string(nil), args...)
		capturedEnv = append([]string(nil), env...)
		return 0, nil
	}

	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "sh"}); code != 0 {
		t.Fatalf("Run(sh) code = %d, stderr = %q", code, stderr.String())
	}

	pathKey, pathValue, ok := lookupEnvValue(runtime.GOOS, capturedEnv, "PATH")
	if !ok {
		t.Fatalf("captured env = %#v, want PATH entry", capturedEnv)
	}
	expectedArgs := []string{posixInteractiveArg}
	if runtime.GOOS == "windows" {
		expectedArgs = windowsInteractiveShellArgs()
	}
	if strings.Join(capturedArgs, "\x00") != strings.Join(expectedArgs, "\x00") {
		t.Fatalf("captured args = %#v, want %#v", capturedArgs, expectedArgs)
	}
	promptKey, promptRoot, ok := lookupEnvValue(runtime.GOOS, capturedEnv, polkaPromptRootEnv)
	if !ok {
		t.Fatalf("captured env = %#v, want %s entry", capturedEnv, polkaPromptRootEnv)
	}
	if promptRoot != projectDir {
		t.Fatalf("%s = %q, want %q", promptKey, promptRoot, projectDir)
	}
	promptEnvKey, promptEnvironment, ok := lookupEnvValue(runtime.GOOS, capturedEnv, polkaPromptEnvEnv)
	if !ok {
		t.Fatalf("captured env = %#v, want %s entry", capturedEnv, polkaPromptEnvEnv)
	}
	if promptEnvironment != "demo" {
		t.Fatalf("%s = %q, want demo", promptEnvKey, promptEnvironment)
	}

	expectedTarget := defaultWindowsShell
	if runtime.GOOS != "windows" {
		expectedTarget = filepath.Join(projectDir, "bin", "custom-shell")
	}
	if capturedTarget != expectedTarget {
		t.Fatalf("captured target = %q, want %q", capturedTarget, expectedTarget)
	}

	expectedPath := joinPathList(runtime.GOOS,
		filepath.Join(root, "bin"),
		filepath.Join(projectDir, "vendor", "bin"),
		systemPath,
	)
	if pathValue != expectedPath {
		t.Fatalf("%s = %q, want %q", pathKey, pathValue, expectedPath)
	}
	if stdout.String() != "Opened Polka shell for environment demo\n" {
		t.Fatalf("Run(sh) stdout = %q, want environment banner", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(sh) stderr = %q, want empty", stderr.String())
	}

	if runtime.GOOS == "windows" && pathKey != "Path" && pathKey != "PATH" {
		t.Fatalf("PATH key = %q, want preserved PATH casing", pathKey)
	}
}

func TestRunShUsesConfiguredNestedRootForPathLoading(t *testing.T) {
	projectDir := t.TempDir()
	testSiteDir := filepath.Join(projectDir, "test-site")
	root := filepath.Join(testSiteDir, ".polka")
	systemPath := filepath.Join(projectDir, "system-bin")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    "test-site/.polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {PHP: "8.4"},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")

	t.Setenv("PATH", systemPath)
	if runtime.GOOS != "windows" {
		t.Setenv("SHELL", filepath.Join(projectDir, "bin", "custom-shell"))
	}

	oldLaunch := launchInteractiveShellFunc
	t.Cleanup(func() {
		launchInteractiveShellFunc = oldLaunch
	})

	var capturedEnv []string
	launchInteractiveShellFunc = func(stdout, stderr io.Writer, env []string, target string, args []string) (int, error) {
		capturedEnv = append([]string(nil), env...)
		return 0, nil
	}

	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"sh"}); code != 0 {
		t.Fatalf("Run(sh) code = %d, stderr = %q", code, stderr.String())
	}

	pathKey, pathValue, ok := lookupEnvValue(runtime.GOOS, capturedEnv, "PATH")
	if !ok {
		t.Fatalf("captured env = %#v, want PATH entry", capturedEnv)
	}
	promptKey, promptRoot, ok := lookupEnvValue(runtime.GOOS, capturedEnv, polkaPromptRootEnv)
	if !ok {
		t.Fatalf("captured env = %#v, want %s entry", capturedEnv, polkaPromptRootEnv)
	}
	if promptRoot != testSiteDir {
		t.Fatalf("%s = %q, want %q", promptKey, promptRoot, testSiteDir)
	}

	expectedPath := joinPathList(runtime.GOOS,
		filepath.Join(root, "bin"),
		filepath.Join(testSiteDir, "vendor", "bin"),
		systemPath,
	)
	if pathValue != expectedPath {
		t.Fatalf("%s = %q, want %q", pathKey, pathValue, expectedPath)
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(sh) stderr = %q, want empty", stderr.String())
	}
}

func TestRunShellAliasLaunchesInteractiveShell(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {PHP: "8.4"},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")

	oldLaunch := launchInteractiveShellFunc
	t.Cleanup(func() {
		launchInteractiveShellFunc = oldLaunch
	})

	launched := false
	launchInteractiveShellFunc = func(stdout, stderr io.Writer, env []string, target string, args []string) (int, error) {
		launched = true
		return 0, nil
	}

	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "shell"}); code != 0 {
		t.Fatalf("Run(shell) code = %d, stderr = %q", code, stderr.String())
	}
	if !launched {
		t.Fatal("Run(shell) did not launch interactive shell")
	}
	if stdout.String() != "Opened Polka shell for environment demo\n" {
		t.Fatalf("Run(shell) stdout = %q, want environment banner", stdout.String())
	}
}

func TestRunShRejectsArguments(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"sh", "composer"}); code != 1 {
		t.Fatalf("Run(sh composer) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "sh/shell does not take arguments") {
		t.Fatalf("Run(sh composer) stderr = %q, want argument error", stderr.String())
	}
}

func TestResolveInteractiveShellUsesExpectedDefaults(t *testing.T) {
	if target, args := resolveInteractiveShell("windows", "C:/Program Files/Git/bin/sh.exe"); target != defaultWindowsShell || strings.Join(args, "\x00") != strings.Join(windowsInteractiveShellArgs(), "\x00") {
		t.Fatalf("resolveInteractiveShell(windows) = (%q, %#v), want (%q, %#v)", target, args, defaultWindowsShell, windowsInteractiveShellArgs())
	}

	if target, args := resolveInteractiveShell("linux", " /bin/bash "); target != "/bin/bash" || strings.Join(args, "\x00") != strings.Join([]string{posixInteractiveArg}, "\x00") {
		t.Fatalf("resolveInteractiveShell(linux, shell) = (%q, %#v), want (/bin/bash, %#v)", target, args, []string{posixInteractiveArg})
	}

	if target, args := resolveInteractiveShell("linux", " "); target != "/bin/sh" || strings.Join(args, "\x00") != strings.Join([]string{posixInteractiveArg}, "\x00") {
		t.Fatalf("resolveInteractiveShell(linux, empty) = (%q, %#v), want (/bin/sh, %#v)", target, args, []string{posixInteractiveArg})
	}
}

func TestWindowsPromptCommandUsesPromptRootEnv(t *testing.T) {
	command := windowsPromptCommand()
	for _, expected := range []string{
		"function global:Get-PolkaPromptPath",
		"function global:prompt",
		"$env:" + polkaPromptEnvEnv,
		"$env:" + polkaPromptRootEnv,
		"Get-PolkaPromptPath $root $current",
		"('polka ' + $relative + ' (' + $name + ') > ')",
	} {
		if !strings.Contains(command, expected) {
			t.Fatalf("windowsPromptCommand() = %q, want substring %q", command, expected)
		}
	}
}

func TestBuildShellEnvironmentPreservesExistingWindowsPathKey(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewStore(filepath.Join(projectDir, ".polka"))
	env := []string{"Path=C:/Windows/System32", "HOME=" + projectDir}

	updated := buildShellEnvironment("windows", env, store)
	pathKey, pathValue, ok := lookupEnvValue("windows", updated, "PATH")
	if !ok {
		t.Fatalf("updated env = %#v, want PATH entry", updated)
	}
	if pathKey != "Path" {
		t.Fatalf("PATH key = %q, want Path", pathKey)
	}

	expectedPath := joinPathList("windows",
		filepath.Join(store.RootDir, "bin"),
		filepath.Join(projectDir, "vendor", "bin"),
		"C:/Windows/System32",
	)
	if pathValue != expectedPath {
		t.Fatalf("Path = %q, want %q", pathValue, expectedPath)
	}

	pathEntries := 0
	for _, entry := range updated {
		entryKey, _, ok := strings.Cut(entry, "=")
		if ok && envKeysEqual("windows", entryKey, "PATH") {
			pathEntries++
		}
	}
	if pathEntries != 1 {
		t.Fatalf("PATH entries = %d, want 1 in %#v", pathEntries, updated)
	}
}

func TestPrepareShellEnvironmentAddsWindowsVendorPHPShimDir(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewStore(filepath.Join(projectDir, ".polka"))
	vendorBinDir := filepath.Join(projectDir, "vendor", "bin")
	if err := os.MkdirAll(vendorBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(vendor/bin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(vendorBinDir, "dcg"), []byte("#!/usr/bin/env php\n<?php echo 'dcg';\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(vendor php proxy) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(vendorBinDir, "drush"), []byte("#!/usr/bin/env sh\nexec \"$DRUSH_PHP\" \"$@\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(vendor shell launcher) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(vendorBinDir, "drush.php"), []byte("#!/usr/bin/env php\n<?php echo 'drush';\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(drush.php) error = %v", err)
	}

	updated, err := prepareShellEnvironment("windows", []string{"PATH=C:/Windows/System32"}, store, projectDir, "demo")
	if err != nil {
		t.Fatalf("prepareShellEnvironment() error = %v", err)
	}

	promptEnvKey, promptEnvironment, ok := lookupEnvValue("windows", updated, polkaPromptEnvEnv)
	if !ok {
		t.Fatalf("updated env = %#v, want %s entry", updated, polkaPromptEnvEnv)
	}
	if promptEnvKey != polkaPromptEnvEnv || promptEnvironment != "demo" {
		t.Fatalf("%s = %q, want demo", promptEnvKey, promptEnvironment)
	}

	promptKey, promptRoot, ok := lookupEnvValue("windows", updated, polkaPromptRootEnv)
	if !ok {
		t.Fatalf("updated env = %#v, want %s entry", updated, polkaPromptRootEnv)
	}
	if promptKey != polkaPromptRootEnv || promptRoot != projectDir {
		t.Fatalf("%s = %q, want %q", promptKey, promptRoot, projectDir)
	}

	_, pathValue, ok := lookupEnvValue("windows", updated, "PATH")
	if !ok {
		t.Fatalf("updated env = %#v, want PATH entry", updated)
	}

	shimDir := filepath.Join(store.RootDir, shellRuntimeDirName, shellSupportDirName, vendorShimDirName)
	expectedPath := joinPathList("windows",
		store.BinDir,
		shimDir,
		filepath.Join(projectDir, "vendor", "bin"),
		"C:/Windows/System32",
	)
	if pathValue != expectedPath {
		t.Fatalf("PATH = %q, want %q", pathValue, expectedPath)
	}

	dcgShimData, err := os.ReadFile(filepath.Join(shimDir, "dcg.cmd"))
	if err != nil {
		t.Fatalf("ReadFile(dcg shim) error = %v", err)
	}
	if !strings.Contains(string(dcgShimData), "call php \""+escapeWindowsShimValue(filepath.Join(vendorBinDir, "dcg"))+"\" %*") {
		t.Fatalf("dcg shim = %q, want php proxy target", string(dcgShimData))
	}

	vendorShimData, err := os.ReadFile(filepath.Join(shimDir, "drush.cmd"))
	if err != nil {
		t.Fatalf("ReadFile(vendor shim) error = %v", err)
	}
	if !strings.Contains(string(vendorShimData), "call php \""+escapeWindowsShimValue(filepath.Join(vendorBinDir, "drush.php"))+"\" %*") {
		t.Fatalf("vendor shim = %q, want matching php target", string(vendorShimData))
	}
}

// TestPrepareShellEnvironmentAddsPHPBuildToolDirectories verifies internal SDK
// payloads become directly runnable after the project and Composer bins.
func TestPrepareShellEnvironmentAddsPHPBuildToolDirectories(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("managed PHP SDK payloads are Windows-only")
	}
	projectDir := t.TempDir()
	store := backend.NewStore(filepath.Join(projectDir, ".polka"))
	config := []byte(strings.Join([]string{
		"version: 1",
		"root: .polka",
		"tools:",
		"  php: \"8.4\"",
		"settings:",
		"  php:",
		"    extension-sdk: true",
		"",
	}, "\n"))
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), config, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	updated, err := prepareShellEnvironment("windows", []string{"PATH=C:/Windows/System32"}, store, projectDir, "default")
	if err != nil {
		t.Fatalf("prepareShellEnvironment() error = %v", err)
	}
	_, pathValue, ok := lookupEnvValue("windows", updated, "PATH")
	if !ok {
		t.Fatalf("updated env = %#v, want PATH entry", updated)
	}
	want := joinPathList("windows",
		store.BinDir,
		filepath.Join(projectDir, "vendor", "bin"),
		filepath.Join(store.EnvsDir, "php-devel", "8.4"),
		filepath.Join(store.EnvsDir, "php-sdk", "2.8.3"),
		"C:/Windows/System32",
	)
	if pathValue != want {
		t.Fatalf("PATH = %q, want %q", pathValue, want)
	}
}

func TestShellPromptRootReturnsAbsolutePath(t *testing.T) {
	projectDir := t.TempDir()
	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	root, err := shellPromptRoot(filepath.Join(".", ".polka", ".."))
	if err != nil {
		t.Fatalf("shellPromptRoot() error = %v", err)
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("shellPromptRoot() = %q, want absolute path", root)
	}
	if root != projectDir {
		t.Fatalf("shellPromptRoot() = %q, want %q", root, projectDir)
	}
}

func TestPrepareShellEnvironmentSkipsUnresolvedWindowsVendorShellLauncher(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewStore(filepath.Join(projectDir, ".polka"))
	vendorBinDir := filepath.Join(projectDir, "vendor", "bin")
	if err := os.MkdirAll(vendorBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(vendor/bin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(vendorBinDir, "hello"), []byte("#!/usr/bin/env sh\necho hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(vendor script) error = %v", err)
	}

	updated, err := prepareShellEnvironment("windows", []string{"PATH=C:/Windows/System32"}, store, projectDir, "none")
	if err != nil {
		t.Fatalf("prepareShellEnvironment() error = %v", err)
	}
	_, pathValue, ok := lookupEnvValue("windows", updated, "PATH")
	if !ok {
		t.Fatalf("updated env = %#v, want PATH entry", updated)
	}
	expectedPath := joinPathList("windows",
		store.BinDir,
		filepath.Join(projectDir, "vendor", "bin"),
		"C:/Windows/System32",
	)
	if pathValue != expectedPath {
		t.Fatalf("PATH = %q, want %q", pathValue, expectedPath)
	}
	if _, err := os.Stat(filepath.Join(store.RootDir, shellRuntimeDirName, shellSupportDirName, vendorShimDirName, "hello.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(unresolved shim) error = %v, want missing wrapper", err)
	}
}

func TestPrepareShellEnvironmentUsesNearestNestedVendorBin(t *testing.T) {
	projectDir := t.TempDir()
	store := backend.NewStore(filepath.Join(projectDir, ".polka"))
	nestedProjectDir := filepath.Join(projectDir, "drupal")
	nestedVendorBinDir := filepath.Join(nestedProjectDir, "vendor", "bin")
	if err := os.MkdirAll(nestedVendorBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested vendor/bin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedVendorBinDir, "drush"), []byte("#!/usr/bin/env sh\nexec \"$DRUSH_PHP\" \"$@\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(vendor shell launcher) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedVendorBinDir, "drush.php"), []byte("#!/usr/bin/env php\n<?php echo 'drush';\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(drush.php) error = %v", err)
	}

	updated, err := prepareShellEnvironment("windows", []string{"PATH=C:/Windows/System32"}, store, nestedProjectDir, "demo")
	if err != nil {
		t.Fatalf("prepareShellEnvironment() error = %v", err)
	}

	_, pathValue, ok := lookupEnvValue("windows", updated, "PATH")
	if !ok {
		t.Fatalf("updated env = %#v, want PATH entry", updated)
	}
	shimDir := filepath.Join(store.RootDir, shellRuntimeDirName, shellSupportDirName, vendorShimDirName)
	expectedPath := joinPathList("windows",
		store.BinDir,
		shimDir,
		nestedVendorBinDir,
		"C:/Windows/System32",
	)
	if pathValue != expectedPath {
		t.Fatalf("PATH = %q, want %q", pathValue, expectedPath)
	}
	shimData, err := os.ReadFile(filepath.Join(shimDir, "drush.cmd"))
	if err != nil {
		t.Fatalf("ReadFile(drush shim) error = %v", err)
	}
	if !strings.Contains(string(shimData), escapeWindowsShimValue(filepath.Join(nestedVendorBinDir, "drush.php"))) {
		t.Fatalf("drush shim = %q, want nested vendor target", string(shimData))
	}
}
