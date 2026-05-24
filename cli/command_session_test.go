package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunSessionStartWritesActivationScriptAndState(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	systemPath := filepath.Join(projectDir, "system-bin")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	configPath := filepath.Join(projectDir, "polka.yaml")
	configData := []byte("version: 1\nroot: .polka\ncurrent: demo\nenvironments:\n  demo:\n    php: \"8.4\"\n")
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	oldSessionIDFunc := newShellSessionIDFunc
	t.Cleanup(func() {
		newShellSessionIDFunc = oldSessionIDFunc
	})
	newShellSessionIDFunc = func() (string, error) {
		return "session-demo", nil
	}

	t.Setenv("PATH", systemPath)
	t.Setenv(polkaSessionIDEnv, "")
	t.Setenv(polkaSessionStateEnv, "")

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

	if code := Run(stdout, stderr, []string{"--root", root, "session", "start"}); code != 0 {
		t.Fatalf("Run(session start) code = %d, stderr = %q", code, stderr.String())
	}

	kind := currentShellScriptKind(runtime.GOOS)
	activationPath := strings.TrimSpace(stdout.String())
	expectedActivationPath := shellSessionActivationPath(root, "session-demo", kind)
	if activationPath != expectedActivationPath {
		t.Fatalf("activation path = %q, want %q", activationPath, expectedActivationPath)
	}
	if !filepath.IsAbs(activationPath) {
		t.Fatalf("activation path = %q, want absolute path", activationPath)
	}

	statePath := shellSessionStatePath(root, "session-demo")
	state, err := loadShellSessionState(statePath)
	if err != nil {
		t.Fatalf("loadShellSessionState() error = %v", err)
	}
	if state.EnvironmentName != "demo" {
		t.Fatalf("session environment = %q, want demo", state.EnvironmentName)
	}
	if state.OriginalPathValue != systemPath {
		t.Fatalf("original PATH = %q, want %q", state.OriginalPathValue, systemPath)
	}
	if state.ActivationPath != activationPath {
		t.Fatalf("state activation path = %q, want %q", state.ActivationPath, activationPath)
	}

	activationScriptData, err := os.ReadFile(activationPath)
	if err != nil {
		t.Fatalf("ReadFile(activation script) error = %v", err)
	}
	expectedActivatedPath := joinPathList(runtime.GOOS,
		filepath.Join(root, "bin"),
		filepath.Join(projectDir, "vendor", "bin"),
		systemPath,
	)
	expectedScript := renderShellSessionStartScript(kind, state.OriginalPathKey, expectedActivatedPath, state.ID, statePath)
	if string(activationScriptData) != expectedScript {
		t.Fatalf("activation script = %q, want %q", string(activationScriptData), expectedScript)
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(session start) stderr = %q, want empty", stderr.String())
	}
}

func TestRunSessionStopWritesDeactivationScriptFromActiveSessionState(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	systemPath := filepath.Join(projectDir, "system-bin")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	configPath := filepath.Join(projectDir, "polka.yaml")
	configData := []byte("version: 1\nroot: .polka\ncurrent: demo\nenvironments:\n  demo:\n    php: \"8.4\"\n")
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	oldSessionIDFunc := newShellSessionIDFunc
	t.Cleanup(func() {
		newShellSessionIDFunc = oldSessionIDFunc
	})
	newShellSessionIDFunc = func() (string, error) {
		return "session-demo", nil
	}

	t.Setenv("PATH", systemPath)
	t.Setenv(polkaSessionIDEnv, "")
	t.Setenv(polkaSessionStateEnv, "")

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

	if code := Run(stdout, stderr, []string{"--root", root, "session", "start"}); code != 0 {
		t.Fatalf("Run(session start) code = %d, stderr = %q", code, stderr.String())
	}
	statePath := shellSessionStatePath(root, "session-demo")
	state, err := loadShellSessionState(statePath)
	if err != nil {
		t.Fatalf("loadShellSessionState() error = %v", err)
	}
	t.Setenv(polkaSessionIDEnv, state.ID)
	t.Setenv(polkaSessionStateEnv, statePath)

	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("Chdir(otherDir) error = %v", err)
	}
	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"session", "stop"}); code != 0 {
		t.Fatalf("Run(session stop) code = %d, stderr = %q", code, stderr.String())
	}

	kind := currentShellScriptKind(runtime.GOOS)
	deactivationPath := strings.TrimSpace(stdout.String())
	expectedDeactivationPath := shellSessionDeactivationPath(root, "session-demo", kind)
	if deactivationPath != expectedDeactivationPath {
		t.Fatalf("deactivation path = %q, want %q", deactivationPath, expectedDeactivationPath)
	}
	deactivationScriptData, err := os.ReadFile(deactivationPath)
	if err != nil {
		t.Fatalf("ReadFile(deactivation script) error = %v", err)
	}
	expectedScript := renderShellSessionStopScript(kind, *state, statePath)
	if string(deactivationScriptData) != expectedScript {
		t.Fatalf("deactivation script = %q, want %q", string(deactivationScriptData), expectedScript)
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(session stop) stderr = %q, want empty", stderr.String())
	}
}

func TestRunSessionStartRejectsNestedSession(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	t.Setenv(polkaSessionStateEnv, filepath.Join(projectDir, "active-session.json"))

	if code := Run(stdout, stderr, []string{"--root", root, "session", "start"}); code != 1 {
		t.Fatalf("Run(session start) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "already active") {
		t.Fatalf("Run(session start) stderr = %q, want active session error", stderr.String())
	}
}

func TestRunSessionStopRejectsWhenNoSessionIsActive(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	t.Setenv(polkaSessionIDEnv, "")
	t.Setenv(polkaSessionStateEnv, "")

	if code := Run(stdout, stderr, []string{"session", "stop"}); code != 1 {
		t.Fatalf("Run(session stop) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no active Polka session") {
		t.Fatalf("Run(session stop) stderr = %q, want inactive session error", stderr.String())
	}
}

func TestWriteShellSessionStateRoundTripsJSON(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "session.json")
	state := shellSessionState{
		ID:                "demo",
		Shell:             string(shellScriptKindPOSIX),
		EnvironmentName:   "blog",
		OriginalPathKey:   "PATH",
		OriginalPathValue: "/tmp/bin",
		HasOriginalPath:   true,
		ActivationPath:    "/tmp/activate.sh",
		DeactivationPath:  "/tmp/deactivate.sh",
	}

	if err := writeShellSessionState(statePath, state); err != nil {
		t.Fatalf("writeShellSessionState() error = %v", err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile(state) error = %v", err)
	}
	var decoded shellSessionState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(state) error = %v", err)
	}
	if decoded != state {
		t.Fatalf("decoded state = %#v, want %#v", decoded, state)
	}
}
