package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrepareWindowsComposerScriptSupportWrapsDirectPHPCommands verifies that
// legacy Composer scripts such as bin/console run through Polka's PHP on Windows.
func TestPrepareWindowsComposerScriptSupportWrapsDirectPHPCommands(t *testing.T) {
	projectDir := t.TempDir()
	binDir := filepath.Join(projectDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(bin) error = %v", err)
	}
	consolePath := filepath.Join(binDir, "console")
	if err := os.WriteFile(consolePath, []byte("#!/usr/bin/env php\n<?php echo 'ok';\n"), 0o755); err != nil {
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

	support, err := prepareWindowsComposerScriptSupport("windows", projectDir, nil, nil)
	if err != nil {
		t.Fatalf("prepareWindowsComposerScriptSupport() error = %v", err)
	}
	wrapperPath := consolePath + ".cmd"
	wrapper, err := os.ReadFile(wrapperPath)
	if err != nil {
		t.Fatalf("ReadFile(bin/console.cmd) error = %v", err)
	}
	text := string(wrapper)
	if !strings.Contains(text, windowsComposerPHPWrapperMarker) || !strings.Contains(text, `call php "`+consolePath+`" %*`) {
		t.Fatalf("bin/console.cmd = %q, want managed PHP wrapper", text)
	}

	if err := support.cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if _, err := os.Stat(wrapperPath); !os.IsNotExist(err) {
		t.Fatalf("Stat(bin/console.cmd) error = %v, want temporary wrapper removed", err)
	}
}

// TestPrepareWindowsComposerScriptSupportPreservesProjectWrapper verifies that
// Polka never overwrites or removes an existing project-owned command wrapper.
func TestPrepareWindowsComposerScriptSupportPreservesProjectWrapper(t *testing.T) {
	projectDir := t.TempDir()
	consolePath := filepath.Join(projectDir, "console")
	if err := os.WriteFile(consolePath, []byte("#!/usr/bin/env php\n<?php\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(console) error = %v", err)
	}
	existingWrapper := []byte("@echo project wrapper\r\n")
	wrapperPath := consolePath + ".bat"
	if err := os.WriteFile(wrapperPath, existingWrapper, 0o755); err != nil {
		t.Fatalf("WriteFile(console.bat) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "composer.json"), []byte(`{"scripts":{"test":"console test"}}`), 0o644); err != nil {
		t.Fatalf("WriteFile(composer.json) error = %v", err)
	}

	support, err := prepareWindowsComposerScriptSupport("windows", projectDir, nil, nil)
	if err != nil {
		t.Fatalf("prepareWindowsComposerScriptSupport() error = %v", err)
	}
	if err := support.cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	got, err := os.ReadFile(wrapperPath)
	if err != nil {
		t.Fatalf("ReadFile(console.bat) error = %v", err)
	}
	if string(got) != string(existingWrapper) {
		t.Fatalf("console.bat = %q, want existing wrapper unchanged", got)
	}
	if _, err := os.Stat(consolePath + ".cmd"); !os.IsNotExist(err) {
		t.Fatalf("Stat(console.cmd) error = %v, want no generated wrapper", err)
	}
}

// TestPrepareWindowsComposerScriptSupportUsesComposerWorkingDir verifies that
// Composer's working-directory option controls manifest and script resolution.
func TestPrepareWindowsComposerScriptSupportUsesComposerWorkingDir(t *testing.T) {
	projectDir := t.TempDir()
	appDir := filepath.Join(projectDir, "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(app) error = %v", err)
	}
	scriptPath := filepath.Join(appDir, "artisan")
	if err := os.WriteFile(scriptPath, []byte("#!/usr/bin/php\n<?php\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(artisan) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "composer.json"), []byte(`{"scripts":{"post-update-cmd":"artisan migrate"}}`), 0o644); err != nil {
		t.Fatalf("WriteFile(composer.json) error = %v", err)
	}

	support, err := prepareWindowsComposerScriptSupport("windows", projectDir, []string{"--working-dir", "app", "update"}, nil)
	if err != nil {
		t.Fatalf("prepareWindowsComposerScriptSupport() error = %v", err)
	}
	if _, err := os.Stat(scriptPath + ".cmd"); err != nil {
		t.Fatalf("Stat(artisan.cmd) error = %v", err)
	}
	if err := support.cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
}
