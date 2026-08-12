package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPrepareLinuxComposerScriptSupportGrantsExecutePermission verifies that a
// Composer script target lacking the executable bit is temporarily made
// executable, and that cleanup restores its original permissions.
func TestPrepareLinuxComposerScriptSupportGrantsExecutePermission(t *testing.T) {
	projectDir := t.TempDir()
	binDir := filepath.Join(projectDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(bin) error = %v", err)
	}
	consolePath := filepath.Join(binDir, "console")
	if err := os.WriteFile(consolePath, []byte("#!/usr/bin/env php\n<?php echo 'ok';\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(bin/console) error = %v", err)
	}
	manifest := `{
  "scripts": {
    "post-install-cmd": ["@post-cmd"],
    "post-cmd": ["Vendor\\Handler::run", "bin/console cache:clear --no-warmup"]
  }
}`
	if err := os.WriteFile(filepath.Join(projectDir, "composer.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile(composer.json) error = %v", err)
	}

	support, err := prepareLinuxComposerScriptSupport("linux", projectDir, nil, nil)
	if err != nil {
		t.Fatalf("prepareLinuxComposerScriptSupport() error = %v", err)
	}
	info, err := os.Stat(consolePath)
	if err != nil {
		t.Fatalf("Stat(bin/console) error = %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("Mode(bin/console) = %v, want executable bit set", info.Mode())
	}

	if err := support.cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	info, err = os.Stat(consolePath)
	if err != nil {
		t.Fatalf("Stat(bin/console) error = %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("Mode(bin/console) = %v, want original permissions restored", info.Mode())
	}
}

// TestPrepareLinuxComposerScriptSupportLeavesExecutableScriptsUnchanged
// verifies that Polka never records or alters a script that is already
// executable.
func TestPrepareLinuxComposerScriptSupportLeavesExecutableScriptsUnchanged(t *testing.T) {
	projectDir := t.TempDir()
	consolePath := filepath.Join(projectDir, "console")
	if err := os.WriteFile(consolePath, []byte("#!/usr/bin/env php\n<?php\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(console) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "composer.json"), []byte(`{"scripts":{"test":"console test"}}`), 0o644); err != nil {
		t.Fatalf("WriteFile(composer.json) error = %v", err)
	}

	support, err := prepareLinuxComposerScriptSupport("linux", projectDir, nil, nil)
	if err != nil {
		t.Fatalf("prepareLinuxComposerScriptSupport() error = %v", err)
	}
	if len(support.restored) != 0 {
		t.Fatalf("restored = %v, want no tracked permission changes for an already-executable script", support.restored)
	}
	if err := support.cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	info, err := os.Stat(consolePath)
	if err != nil {
		t.Fatalf("Stat(console) error = %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("Mode(console) = %v, want permissions unchanged", info.Mode())
	}
}

// TestPrepareLinuxComposerScriptSupportUsesComposerWorkingDir verifies that
// Composer's working-directory option controls manifest and script resolution.
func TestPrepareLinuxComposerScriptSupportUsesComposerWorkingDir(t *testing.T) {
	projectDir := t.TempDir()
	appDir := filepath.Join(projectDir, "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(app) error = %v", err)
	}
	scriptPath := filepath.Join(appDir, "artisan")
	if err := os.WriteFile(scriptPath, []byte("#!/usr/bin/php\n<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(artisan) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "composer.json"), []byte(`{"scripts":{"post-update-cmd":"artisan migrate"}}`), 0o644); err != nil {
		t.Fatalf("WriteFile(composer.json) error = %v", err)
	}

	support, err := prepareLinuxComposerScriptSupport("linux", projectDir, []string{"--working-dir", "app", "update"}, nil)
	if err != nil {
		t.Fatalf("prepareLinuxComposerScriptSupport() error = %v", err)
	}
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("Stat(artisan) error = %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("Mode(artisan) = %v, want executable bit set", info.Mode())
	}
	if err := support.cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
}

// TestPrepareLinuxComposerScriptSupportSkipsOnOtherPlatforms verifies that the
// function is a no-op unless the reported OS is linux.
func TestPrepareLinuxComposerScriptSupportSkipsOnOtherPlatforms(t *testing.T) {
	projectDir := t.TempDir()
	consolePath := filepath.Join(projectDir, "console")
	if err := os.WriteFile(consolePath, []byte("#!/usr/bin/env php\n<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(console) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "composer.json"), []byte(`{"scripts":{"test":"console test"}}`), 0o644); err != nil {
		t.Fatalf("WriteFile(composer.json) error = %v", err)
	}

	support, err := prepareLinuxComposerScriptSupport("darwin", projectDir, nil, nil)
	if err != nil {
		t.Fatalf("prepareLinuxComposerScriptSupport() error = %v", err)
	}
	if len(support.restored) != 0 {
		t.Fatalf("restored = %v, want no changes on a non-linux platform", support.restored)
	}
	info, err := os.Stat(consolePath)
	if err != nil {
		t.Fatalf("Stat(console) error = %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("Mode(console) = %v, want permissions unchanged", info.Mode())
	}
}
