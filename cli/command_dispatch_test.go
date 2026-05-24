package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestRunConfigSetsVersionLabelsAndDispatchesPhp(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("Polka_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	fakeComposer := cachedComposerPath(cacheDir, "2.8")
	if err := os.MkdirAll(filepath.Dir(fakeComposer), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache composer) error = %v", err)
	}
	if err := os.WriteFile(fakeComposer, []byte("composer\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache composer) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4", "--composer", "2.8"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var config testConfigFile
	if err := yaml.Unmarshal(configData, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(config) error = %v", err)
	}
	if config.Environments["demo"].PHP != "8.4" || config.Environments["demo"].Composer != "2.8" {
		t.Fatalf("config = %#v, want version labels for demo", config)
	}
	if strings.Contains(string(configData), fakePHP) {
		t.Fatalf("config contents = %q, want versions rather than paths", string(configData))
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(projectInstalledPHPPath(root, "8.4")); err != nil {
		t.Fatalf("Stat(installed php) error = %v", err)
	}
	installOutput := stdout.String()
	expectedProgress := []string{
		"[1/2] php 8.4: using cache",
		"[1/2] php 8.4: installing",
		"[1/2] php 8.4: configuring",
		"[1/2] php 8.4: installed",
		"[2/2] composer 2.8: using cache",
		"[2/2] composer 2.8: installing",
		"[2/2] composer 2.8: installed",
		"Installed 'demo' environment",
	}
	previousIndex := -1
	for _, expected := range expectedProgress {
		currentIndex := strings.Index(installOutput, expected)
		if currentIndex < 0 {
			t.Fatalf("Run(install) stdout = %q, want %q", installOutput, expected)
		}
		if currentIndex < previousIndex {
			t.Fatalf("Run(install) stdout = %q, want ordered progress lines %#v", installOutput, expectedProgress)
		}
		previousIndex = currentIndex
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "dispatch", "php", "-v", "--ini"}); code != 0 {
		t.Fatalf("Run(dispatch) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "fake-php -v --ini") {
		t.Fatalf("Run(dispatch) output = %q, want forwarded arguments", output)
	}
}
