package backend

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestResolvePHPBuildTools verifies Linux host-tool discovery accepts a
// matching php-dev toolset and standard C build commands.
func TestResolvePHPBuildTools(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host PHP build-tool verification is Linux-specific")
	}
	binDir := t.TempDir()
	scripts := map[string]string{
		"php":        "#!/bin/sh\nprintf '8.4.24~8.4~0~x64\\n'\n",
		"phpize":     "#!/bin/sh\nexit 0\n",
		"php-config": "#!/bin/sh\nprintf '8.4.24\\n'\n",
		"make":       "#!/bin/sh\nexit 0\n",
		"autoconf":   "#!/bin/sh\nexit 0\n",
		"cc":         "#!/bin/sh\nexit 0\n",
	}
	for name, content := range scripts {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	t.Setenv("PATH", binDir)

	phpPath := filepath.Join(binDir, "php")
	phpize, phpConfig, err := resolvePHPBuildTools(binDir, phpPath, "PHP extension SDK")
	if err != nil {
		t.Fatalf("resolvePHPBuildTools() error = %v", err)
	}
	if phpize != filepath.Join(binDir, "phpize") || phpConfig != filepath.Join(binDir, "php-config") {
		t.Fatalf("resolvePHPBuildTools() = %q, %q; want project host tools", phpize, phpConfig)
	}
}

// TestVerifyPHPBuildToolsAllowsFirstInstallOfBuildablePHP verifies a host with no
// system PHP can install a compiled PHP, which ships its own phpize and php-config,
// while a PHP that is installed still gets checked.
func TestVerifyPHPBuildToolsAllowsFirstInstallOfBuildablePHP(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host PHP build-tool verification is Linux-specific")
	}
	t.Setenv("PATH", t.TempDir())

	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	environment := Environment{Settings: map[string]map[string]any{"php": {"extension-sdk": true}}}
	environment.SetToolVersion(toolPHP, "8.4")

	if err := store.verifyPHPBuildTools(environment); err != nil {
		t.Fatalf("verifyPHPBuildTools(not installed) error = %v", err)
	}

	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")
	if _, err := store.installToolFromCache(toolPHP, "8.4"); err != nil {
		t.Fatalf("installToolFromCache() error = %v", err)
	}
	if err := store.verifyPHPBuildTools(environment); err == nil {
		t.Fatal("verifyPHPBuildTools(installed without tools) error = nil, want the check to run")
	}
}
