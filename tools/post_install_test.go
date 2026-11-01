package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestWriteManifestWrappersPlacesScriptNextToResolvedTarget checks that a
// wrapper is written in the same directory as the first existing target binary
// for the target platform.
func TestWriteManifestWrappersPlacesScriptNextToResolvedTarget(t *testing.T) {
	installDir := t.TempDir()
	binDir := filepath.Join(installDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	corepackPath := filepath.Join(binDir, "corepack.cmd")
	if err := os.WriteFile(corepackPath, []byte("@echo off\r\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(corepack.cmd) error = %v", err)
	}

	manifest, err := loadBuiltinManifest(NodeJS)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(nodejs) error = %v", err)
	}

	written, err := writeManifestWrappers(installDir, manifest.Wrappers, "windows", "amd64")
	if err != nil {
		t.Fatalf("writeManifestWrappers() error = %v", err)
	}
	wantPath := filepath.Join(binDir, "yarn.cmd")
	if len(written) != 1 || written[0] != wantPath {
		t.Fatalf("written = %#v, want [%q]", written, wantPath)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("ReadFile(yarn.cmd) error = %v", err)
	}
	if !strings.Contains(string(data), `call "%~dp0corepack.cmd" yarn %*`) {
		t.Fatalf("yarn.cmd = %q, want corepack forwarding", data)
	}
}

// TestWriteManifestWrappersSkipsWhenTargetOrExistingCommandPresent verifies
// missing targets are a no-op and skip-if-exists preserves bundled commands.
func TestWriteManifestWrappersSkipsWhenTargetOrExistingCommandPresent(t *testing.T) {
	manifest, err := loadBuiltinManifest(NodeJS)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(nodejs) error = %v", err)
	}

	// Missing target binary is a no-op.
	emptyDir := t.TempDir()
	written, err := writeManifestWrappers(emptyDir, manifest.Wrappers, "linux", "amd64")
	if err != nil {
		t.Fatalf("writeManifestWrappers(empty) error = %v", err)
	}
	if len(written) != 0 {
		t.Fatalf("written for empty dir = %#v, want none", written)
	}

	// Existing yarn binary is preserved when skip-if-exists is true.
	installDir := t.TempDir()
	for _, name := range []string{"corepack", "yarn"} {
		if err := os.WriteFile(filepath.Join(installDir, name), []byte("bundled"), 0o755); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	written, err = writeManifestWrappers(installDir, manifest.Wrappers, "linux", "amd64")
	if err != nil {
		t.Fatalf("writeManifestWrappers(existing yarn) error = %v", err)
	}
	if len(written) != 0 {
		t.Fatalf("written with existing yarn = %#v, want none", written)
	}
	data, err := os.ReadFile(filepath.Join(installDir, "yarn"))
	if err != nil || string(data) != "bundled" {
		t.Fatalf("existing yarn = %q, %v; want preserved bundled file", data, err)
	}
}

// TestEnsureInstalledExecutablesMarksFilesExecutable verifies Unix executable
// permission bits are applied to existing files while missing paths are ignored.
func TestEnsureInstalledExecutablesMarksFilesExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not use Unix executable permission bits")
	}

	dir := t.TempDir()
	binary := filepath.Join(dir, "tool")
	if err := os.WriteFile(binary, []byte("binary"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	missing := filepath.Join(dir, "missing")
	if err := ensureInstalledExecutables([]string{binary, missing}); err != nil {
		t.Fatalf("ensureInstalledExecutables() error = %v", err)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("mode = %o, want executable", info.Mode().Perm())
	}
}
