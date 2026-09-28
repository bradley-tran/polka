package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPatchPHPDevel verifies that PatchPHPDevel correctly patches phpize.js
// and config.w32.phpize.in to resolve PHP_PREFIX dynamically, and is idempotent.
func TestPatchPHPDevel(t *testing.T) {
	tempDir := t.TempDir()
	scriptDir := filepath.Join(tempDir, "script")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(script) error = %v", err)
	}

	rawPhpizeJS := strings.Join([]string{
		`var PHP_PREFIX="C:\\php"`,
		`var PHP_ZTS="No"`,
		`var VC_VERSION=1944`,
		`var PHP_VERSION=8`,
		`var PHP_MINOR_VERSION=4`,
		`var FSO = WScript.CreateObject("Scripting.FileSystemObject");`,
		`re = /\\script/i;`,
		`var PHP_DIR=FSO.GetParentFolderName(WScript.ScriptFullName).replace(re,"");`,
		``,
		`var modules = "";`,
		`C.WriteLine("var PHP_PREFIX = " + '"' + PHP_PREFIX.replace(new RegExp('(["\\\\])', "g"), '\\$1') + '"');`,
	}, "\r\n")

	rawConfigW32 := strings.Join([]string{
		`// configure script header`,
		`if (PHP_PREFIX == '') {`,
		`	PHP_PREFIX = "C:\\php";`,
		`	if (PHP_DEBUG == "yes")`,
		`		PHP_PREFIX += "\\debug";`,
		`}`,
		`DEFINE('PHP_PREFIX', PHP_PREFIX);`,
		`DEFINE("BASE_INCLUDES", "/I " + PHP_DIR + "/include");`,
	}, "\r\n")

	phpizePath := filepath.Join(scriptDir, "phpize.js")
	configW32Path := filepath.Join(scriptDir, "config.w32.phpize.in")

	if err := os.WriteFile(phpizePath, []byte(rawPhpizeJS), 0o644); err != nil {
		t.Fatalf("WriteFile(phpize.js) error = %v", err)
	}
	if err := os.WriteFile(configW32Path, []byte(rawConfigW32), 0o644); err != nil {
		t.Fatalf("WriteFile(config.w32.phpize.in) error = %v", err)
	}

	// First patch invocation
	if err := PatchPHPDevel(tempDir); err != nil {
		t.Fatalf("PatchPHPDevel() error = %v", err)
	}

	patchedPhpize, err := os.ReadFile(phpizePath)
	if err != nil {
		t.Fatalf("ReadFile(phpize.js) error = %v", err)
	}
	if !strings.Contains(string(patchedPhpize), polkaPHPDevelPatchMarker) {
		t.Fatalf("patched phpize.js does not contain patch marker: %s", string(patchedPhpize))
	}
	if !strings.Contains(string(patchedPhpize), "var polkaEnvsDir = FSO.GetParentFolderName(FSO.GetParentFolderName(PHP_DIR));") {
		t.Fatalf("patched phpize.js does not contain polkaEnvsDir traversal: %s", string(patchedPhpize))
	}

	patchedConfigW32, err := os.ReadFile(configW32Path)
	if err != nil {
		t.Fatalf("ReadFile(config.w32.phpize.in) error = %v", err)
	}
	if !strings.Contains(string(patchedConfigW32), polkaPHPDevelPatchMarker) {
		t.Fatalf("patched config.w32.phpize.in does not contain patch marker: %s", string(patchedConfigW32))
	}
	if !strings.Contains(string(patchedConfigW32), `if (PHP_PREFIX == '' || PHP_PREFIX == "C:\\php")`) {
		t.Fatalf("patched config.w32.phpize.in does not contain fallback condition: %s", string(patchedConfigW32))
	}

	// Idempotency: second patch invocation must succeed and not duplicate snippets
	if err := PatchPHPDevel(tempDir); err != nil {
		t.Fatalf("PatchPHPDevel() idempotent run error = %v", err)
	}

	secondPhpize, err := os.ReadFile(phpizePath)
	if err != nil {
		t.Fatalf("ReadFile(phpize.js) error = %v", err)
	}
	if string(secondPhpize) != string(patchedPhpize) {
		t.Fatalf("PatchPHPDevel() is not idempotent on phpize.js")
	}

	secondConfigW32, err := os.ReadFile(configW32Path)
	if err != nil {
		t.Fatalf("ReadFile(config.w32.phpize.in) error = %v", err)
	}
	if string(secondConfigW32) != string(patchedConfigW32) {
		t.Fatalf("PatchPHPDevel() is not idempotent on config.w32.phpize.in")
	}
}

// TestPatchPHPDevelMissingDirectory verifies PatchPHPDevel returns nil gracefully
// when the target directory or files do not exist.
func TestPatchPHPDevelMissingDirectory(t *testing.T) {
	tempDir := t.TempDir()
	nonExistentDir := filepath.Join(tempDir, "does-not-exist")
	if err := PatchPHPDevel(nonExistentDir); err != nil {
		t.Fatalf("PatchPHPDevel(nonExistentDir) error = %v; want nil", err)
	}
}

// TestPatchPHPDevelExecutableFilePath verifies that PatchPHPDevel and postInstallPHPDevel
// accept a path to phpize.bat directly (as returned by resolveInstalledTool) and resolve to its parent directory.
func TestPatchPHPDevelExecutableFilePath(t *testing.T) {
	tempDir := t.TempDir()
	scriptDir := filepath.Join(tempDir, "script")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(script) error = %v", err)
	}

	phpizeBat := filepath.Join(tempDir, "phpize.bat")
	if err := os.WriteFile(phpizeBat, []byte("@echo off\r\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(phpize.bat) error = %v", err)
	}

	rawPhpizeJS := "re = /\\\\script/i;\r\nvar PHP_DIR=FSO.GetParentFolderName(WScript.ScriptFullName).replace(re,\"\");\r\n"
	rawConfigW32 := "if (PHP_PREFIX == '') {\r\n\tPHP_PREFIX = \"C:\\\\php\";\r\n}\r\nDEFINE('PHP_PREFIX', PHP_PREFIX);\r\n"

	if err := os.WriteFile(filepath.Join(scriptDir, "phpize.js"), []byte(rawPhpizeJS), 0o644); err != nil {
		t.Fatalf("WriteFile(phpize.js) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "config.w32.phpize.in"), []byte(rawConfigW32), 0o644); err != nil {
		t.Fatalf("WriteFile(config.w32.phpize.in) error = %v", err)
	}

	// Pass the executable file path directly
	if err := PatchPHPDevel(phpizeBat); err != nil {
		t.Fatalf("PatchPHPDevel(phpizeBat) error = %v", err)
	}

	patchedPhpize, err := os.ReadFile(filepath.Join(scriptDir, "phpize.js"))
	if err != nil {
		t.Fatalf("ReadFile(phpize.js) error = %v", err)
	}
	if !strings.Contains(string(patchedPhpize), polkaPHPDevelPatchMarker) {
		t.Fatalf("phpize.js was not patched when passed phpize.bat")
	}

	// Also verify postInstallPHPDevel hook with InstallContext
	ctx := InstallContext{
		Result: InstallResult{
			Tool:       PHPDevel,
			Version:    "8.4",
			TargetPath: phpizeBat,
		},
	}
	if err := postInstallPHPDevel(ctx); err != nil {
		t.Fatalf("postInstallPHPDevel(ctx) error = %v", err)
	}
}

