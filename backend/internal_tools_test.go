package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInternalToolsDirResolution verifies the override order: POLKA_TOOLS_DIR
// wins, then a POLKA_CACHE_DIR-derived directory keeps tests isolated.
func TestInternalToolsDirResolution(t *testing.T) {
	projectDir := t.TempDir()

	t.Setenv("POLKA_TOOLS_DIR", filepath.Join(projectDir, "tools-override"))
	t.Setenv("POLKA_CACHE_DIR", filepath.Join(projectDir, "cache-override"))
	if got := internalToolsDir(projectDir); got != filepath.Join(projectDir, "tools-override") {
		t.Fatalf("internalToolsDir() = %q, want POLKA_TOOLS_DIR override", got)
	}

	t.Setenv("POLKA_TOOLS_DIR", "")
	if got := internalToolsDir(projectDir); got != filepath.Join(projectDir, "cache-override", "internal-tools") {
		t.Fatalf("internalToolsDir() = %q, want POLKA_CACHE_DIR-derived directory", got)
	}
}

// TestEnsureInternalToolInstallsAndRecords verifies the first install extracts
// the cached payload into the global tools dir, seeds the _internal
// environment file with the shared default version, records install state,
// and that a second call is served from disk without any download.
func TestEnsureInternalToolInstallsAndRecords(t *testing.T) {
	projectDir := t.TempDir()
	toolsDir := filepath.Join(projectDir, "internal-tools")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_TOOLS_DIR", toolsDir)
	t.Setenv("POLKA_CACHE_DIR", cacheDir)

	store := NewProjectStore(projectDir)
	store.CacheDir = cacheDir
	writeCachedTool(t, cacheDir, toolPIE, "1")

	targetPath, err := store.EnsureInternalTool(toolPIE, "", nil)
	if err != nil {
		t.Fatalf("EnsureInternalTool(pie) error = %v", err)
	}
	if !strings.HasPrefix(targetPath, filepath.Join(toolsDir, toolPIE, "1")) {
		t.Fatalf("EnsureInternalTool(pie) = %q, want install under %s", targetPath, filepath.Join(toolsDir, toolPIE, "1"))
	}
	assertPathExists(t, targetPath)

	// The _internal environment file records the resolved version.
	environment, err := readInternalEnvironment(filepath.Join(toolsDir, internalConfigFileName))
	if err != nil {
		t.Fatalf("readInternalEnvironment() error = %v", err)
	}
	if environment.PIEVersion != DefaultPIEVersion {
		t.Fatalf("internal environment pie version = %q, want default %q", environment.PIEVersion, DefaultPIEVersion)
	}

	// Install state marks the version installed inside the tools dir.
	if !store.internalStore().readInstallState().has(toolPIE, "1") {
		t.Fatal("install state does not record pie 1 after EnsureInternalTool")
	}

	// A second call must not download anything.
	store.Downloader = fakeDownloader(func(cacheDir, tool, version string) error {
		return fmt.Errorf("unexpected download of %s %s", tool, version)
	})
	secondPath, err := store.EnsureInternalTool(toolPIE, "", nil)
	if err != nil {
		t.Fatalf("EnsureInternalTool(pie) second call error = %v", err)
	}
	if secondPath != targetPath {
		t.Fatalf("EnsureInternalTool(pie) second call = %q, want %q", secondPath, targetPath)
	}
}

// TestEnsureInternalToolHonorsConfiguredVersion verifies a user-edited
// _internal environment file overrides the shared default version.
func TestEnsureInternalToolHonorsConfiguredVersion(t *testing.T) {
	projectDir := t.TempDir()
	toolsDir := filepath.Join(projectDir, "internal-tools")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_TOOLS_DIR", toolsDir)
	t.Setenv("POLKA_CACHE_DIR", cacheDir)

	store := NewProjectStore(projectDir)
	store.CacheDir = cacheDir
	writeCachedTool(t, cacheDir, toolPIE, "2.5")

	configPath := filepath.Join(toolsDir, internalConfigFileName)
	if err := writeInternalEnvironment(configPath, Environment{Name: internalEnvironmentName, PIEVersion: "2.5"}); err != nil {
		t.Fatalf("writeInternalEnvironment() error = %v", err)
	}

	targetPath, err := store.EnsureInternalTool(toolPIE, "", nil)
	if err != nil {
		t.Fatalf("EnsureInternalTool(pie) error = %v", err)
	}
	if !strings.Contains(targetPath, filepath.Join(toolPIE, "2.5")) {
		t.Fatalf("EnsureInternalTool(pie) = %q, want configured version 2.5", targetPath)
	}
}

// TestEnsureInternalToolRecordsExplicitVersion verifies an explicit version
// argument installs that version and persists it to the _internal file.
func TestEnsureInternalToolRecordsExplicitVersion(t *testing.T) {
	projectDir := t.TempDir()
	toolsDir := filepath.Join(projectDir, "internal-tools")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_TOOLS_DIR", toolsDir)
	t.Setenv("POLKA_CACHE_DIR", cacheDir)

	store := NewProjectStore(projectDir)
	store.CacheDir = cacheDir
	writeCachedTool(t, cacheDir, toolComposer, "2.9")

	targetPath, err := store.EnsureInternalTool(toolComposer, "2.9", nil)
	if err != nil {
		t.Fatalf("EnsureInternalTool(composer, 2.9) error = %v", err)
	}
	assertPathExists(t, targetPath)

	environment, err := readInternalEnvironment(filepath.Join(toolsDir, internalConfigFileName))
	if err != nil {
		t.Fatalf("readInternalEnvironment() error = %v", err)
	}
	if environment.ComposerVersion != "2.9" {
		t.Fatalf("internal environment composer version = %q, want 2.9", environment.ComposerVersion)
	}
}

// TestEnsureInternalToolErrors verifies unknown tools and tools without an
// internal version are rejected.
func TestEnsureInternalToolErrors(t *testing.T) {
	projectDir := t.TempDir()
	t.Setenv("POLKA_TOOLS_DIR", filepath.Join(projectDir, "internal-tools"))
	t.Setenv("POLKA_CACHE_DIR", filepath.Join(projectDir, "global-cache"))
	store := NewProjectStore(projectDir)

	if _, err := store.EnsureInternalTool("unknown-tool", "", nil); err == nil {
		t.Fatal("EnsureInternalTool(unknown-tool) error = nil, want unsupported tool error")
	}
	if _, err := store.EnsureInternalTool(toolNginx, "", nil); err == nil || !strings.Contains(err.Error(), "internal version") {
		t.Fatalf("EnsureInternalTool(nginx) error = %v, want missing internal version error", err)
	}
}

// TestInternalEnvironmentNameReserved verifies _internal can never be used as
// a project environment name: validateName rejects leading underscores.
func TestInternalEnvironmentNameReserved(t *testing.T) {
	if err := validateName(internalEnvironmentName); err == nil {
		t.Fatalf("validateName(%q) error = nil, want reserved name rejected", internalEnvironmentName)
	}
}

// TestReadInternalEnvironmentMissingFile verifies a missing _internal file
// yields an empty environment instead of an error.
func TestReadInternalEnvironmentMissingFile(t *testing.T) {
	environment, err := readInternalEnvironment(filepath.Join(t.TempDir(), internalConfigFileName))
	if err != nil {
		t.Fatalf("readInternalEnvironment(missing) error = %v", err)
	}
	if environment.Name != internalEnvironmentName || environment.PIEVersion != "" {
		t.Fatalf("readInternalEnvironment(missing) = %#v, want empty _internal environment", environment)
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), internalConfigFileName)); !os.IsNotExist(err) {
		t.Fatalf("Stat(internal config) error = %v, want not exists", err)
	}
}
