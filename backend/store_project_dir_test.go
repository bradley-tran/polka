package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Tests for opening a project by directory and for dispatch shims that find
// their project from wherever the config's root key places the state directory.

// writeProjectConfigFile writes contents as projectDir's polka.yaml.
func writeProjectConfigFile(t *testing.T, projectDir, contents string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(projectDir, configFileName), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(polka.yaml) error = %v", err)
	}
}

// assertSameDir fails unless got and want name the same directory once
// symlinks are resolved, as with macOS temp directories under /var.
func assertSameDir(t *testing.T, label, got, want string) {
	t.Helper()

	resolvedGot, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("%s: EvalSymlinks(%q) error = %v", label, got, err)
	}
	resolvedWant, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatalf("%s: EvalSymlinks(%q) error = %v", label, want, err)
	}
	if !strings.EqualFold(resolvedGot, resolvedWant) || (runtime.GOOS != "windows" && resolvedGot != resolvedWant) {
		t.Fatalf("%s = %q, want %q", label, got, want)
	}
}

// TestStoreForProjectDirDoesNotSearchParentDirectories verifies an explicit
// project directory is used as-is even when an ancestor holds a polka.yaml.
func TestStoreForProjectDirDoesNotSearchParentDirectories(t *testing.T) {
	parentDir := t.TempDir()
	writeProjectConfigFile(t, parentDir, "version: 0.2\nroot: var/polka\n")
	nestedDir := filepath.Join(parentDir, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested) error = %v", err)
	}

	store, err := StoreForProjectDir(nestedDir)
	if err != nil {
		t.Fatalf("StoreForProjectDir() error = %v", err)
	}

	if store.ProjectDir != nestedDir {
		t.Fatalf("ProjectDir = %q, want %q", store.ProjectDir, nestedDir)
	}
	if store.RootDir != filepath.Join(nestedDir, defaultRootDirectoryName) {
		t.Fatalf("RootDir = %q, want default root under %q", store.RootDir, nestedDir)
	}
	if store.ConfigFile != filepath.Join(nestedDir, configFileName) {
		t.Fatalf("ConfigFile = %q, want polka.yaml in %q", store.ConfigFile, nestedDir)
	}
}

// TestStoreForProjectDirResolvesRelativePath verifies a relative project
// directory is resolved against the working directory.
func TestStoreForProjectDirResolvesRelativePath(t *testing.T) {
	workingDir := t.TempDir()
	projectDir := filepath.Join(workingDir, "app")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(app) error = %v", err)
	}
	writeProjectConfigFile(t, projectDir, "version: 0.2\nroot: state\n")
	t.Chdir(workingDir)

	store, err := StoreForProjectDir("app")
	if err != nil {
		t.Fatalf("StoreForProjectDir(app) error = %v", err)
	}

	if !filepath.IsAbs(store.ProjectDir) {
		t.Fatalf("ProjectDir = %q, want absolute path", store.ProjectDir)
	}
	assertSameDir(t, "ProjectDir", store.ProjectDir, projectDir)
	if store.RootDir != filepath.Join(store.ProjectDir, "state") {
		t.Fatalf("RootDir = %q, want configured root under %q", store.RootDir, store.ProjectDir)
	}
}

// TestStoreForProjectDirRejectsInvalidDirectory verifies a missing path or a
// regular file is reported instead of becoming a new project.
func TestStoreForProjectDirRejectsInvalidDirectory(t *testing.T) {
	baseDir := t.TempDir()
	filePath := filepath.Join(baseDir, "file.txt")
	if err := os.WriteFile(filePath, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(file.txt) error = %v", err)
	}

	for _, test := range []struct {
		path string
		want string
	}{
		{path: filepath.Join(baseDir, "missing"), want: "does not exist"},
		{path: filePath, want: "is not a directory"},
	} {
		for name, open := range map[string]func(string) (Store, error){
			"StoreForProjectDir": StoreForProjectDir,
			"InitStore":          InitStore,
		} {
			if _, err := open(test.path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s(%q) error = %v, want %q", name, test.path, err, test.want)
			}
			assertPathMissing(t, filepath.Join(baseDir, "missing"))
		}
	}
}

// TestInitStoreFollowsExistingConfigRoot verifies init reuses the root key of
// an existing polka.yaml instead of assuming .polka.
func TestInitStoreFollowsExistingConfigRoot(t *testing.T) {
	projectDir := t.TempDir()
	writeProjectConfigFile(t, projectDir, "version: 0.2\nroot: var/polka\n")

	store, err := InitStore(projectDir)
	if err != nil {
		t.Fatalf("InitStore() error = %v", err)
	}

	if store.RootDir != filepath.Join(projectDir, "var", "polka") {
		t.Fatalf("RootDir = %q, want configured var/polka root", store.RootDir)
	}
}

// TestInitStoreFallsBackToDefaultRootForUnreadableConfig verifies a broken
// polka.yaml does not stop init-style commands from opening the project, while
// a regular project store still reports the problem.
func TestInitStoreFallsBackToDefaultRootForUnreadableConfig(t *testing.T) {
	projectDir := t.TempDir()
	writeProjectConfigFile(t, projectDir, "version: 0.2\nroot: [\n")

	store, err := InitStore(projectDir)
	if err != nil {
		t.Fatalf("InitStore() error = %v", err)
	}
	if store.RootDir != filepath.Join(projectDir, defaultRootDirectoryName) {
		t.Fatalf("RootDir = %q, want default root", store.RootDir)
	}

	if _, err := StoreForProjectDir(projectDir); err == nil {
		t.Fatal("StoreForProjectDir() error = nil, want config parse error")
	}
}

// TestInitStoreDefaultsToWorkingDirectory verifies an empty project directory
// means the working directory, without searching its parents.
func TestInitStoreDefaultsToWorkingDirectory(t *testing.T) {
	parentDir := t.TempDir()
	writeProjectConfigFile(t, parentDir, "version: 0.2\nroot: .polka\n")
	workingDir := filepath.Join(parentDir, "child")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(child) error = %v", err)
	}
	t.Chdir(workingDir)

	store, err := InitStore("")
	if err != nil {
		t.Fatalf("InitStore() error = %v", err)
	}

	assertSameDir(t, "ProjectDir", store.ProjectDir, workingDir)
}

// TestShimProjectDirValues verifies how dispatch shims spell the project
// directory for POSIX shells and cmd.exe, including quoting and escaping.
func TestShimProjectDirValues(t *testing.T) {
	for _, test := range []struct {
		name       string
		projectDir shimProjectDir
		wantShell  string
		wantBatch  string
	}{
		{
			name:       "relative",
			projectDir: shimProjectDir{path: "../../..", relative: true},
			wantShell:  `$(CDPATH= cd -- "$SCRIPT_DIR/../../.." && pwd)`,
			wantBatch:  `%SCRIPT_DIR%..\..\..`,
		},
		{
			name:       "relative with special characters",
			projectDir: shimProjectDir{path: "../../my $app`\"100%", relative: true},
			wantShell:  "$(CDPATH= cd -- \"$SCRIPT_DIR/../../my \\$app\\`\\\"100%\" && pwd)",
			wantBatch:  "%SCRIPT_DIR%..\\..\\my $app`\"100%%",
		},
		{
			name:       "absolute",
			projectDir: shimProjectDir{path: "D:/work/it's"},
			wantShell:  `'D:/work/it'"'"'s'`,
			wantBatch:  `D:\work\it's`,
		},
		{
			name:       "absolute drive root",
			projectDir: shimProjectDir{path: "D:/"},
			wantShell:  `'D:/'`,
			wantBatch:  `D:\.`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.projectDir.shellValue(); got != test.wantShell {
				t.Fatalf("shellValue() = %q, want %q", got, test.wantShell)
			}
			if got := test.projectDir.batchValue(); got != test.wantBatch {
				t.Fatalf("batchValue() = %q, want %q", got, test.wantBatch)
			}
		})
	}
}

// TestStoreShimProjectDirFallsBackToAbsolutePath verifies a state root on
// another Windows volume embeds the absolute project directory.
func TestStoreShimProjectDirFallsBackToAbsolutePath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("cross-volume paths only exist on Windows")
	}

	store := newStore(`C:\work\app`, `D:\polka-state`)
	got := store.shimProjectDir()
	if got.relative || got.path != `C:\work\app` {
		t.Fatalf("shimProjectDir() = %#v, want absolute C:\\work\\app", got)
	}
}

// TestStoreInitWritesShimsIntoConfiguredRoot verifies init installs state and
// shims under the configured root, and that a generated shim dispatches to
// the project directory regardless of the caller's working directory.
func TestStoreInitWritesShimsIntoConfiguredRoot(t *testing.T) {
	projectDir := t.TempDir()
	writeProjectConfigFile(t, projectDir, "version: 0.2\nroot: var/polka\ntools:\n  php: \"8.4\"\n")

	store, err := InitStore(projectDir)
	if err != nil {
		t.Fatalf("InitStore() error = %v", err)
	}
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	if err := store.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	root := filepath.Join(projectDir, "var", "polka")
	assertPathExists(t, filepath.Join(root, envsDirectoryName))
	assertPathExists(t, filepath.Join(root, binDirectoryName, dispatcherBinaryName))
	assertPathExists(t, filepath.Join(root, binDirectoryName, "php"))
	assertPathExists(t, filepath.Join(root, binDirectoryName, "php.cmd"))
	assertPathMissing(t, filepath.Join(projectDir, defaultRootDirectoryName))

	shimData, err := os.ReadFile(filepath.Join(root, binDirectoryName, "php"))
	if err != nil {
		t.Fatalf("ReadFile(php shim) error = %v", err)
	}
	if shim := string(shimData); !strings.Contains(shim, `"$SCRIPT_DIR/../../.."`) || !strings.Contains(shim, `-C "$DISPATCH_PROJECT" dispatch php`) {
		t.Fatalf("php shim = %q, want relative project dir passed with -C", shim)
	}

	callerDir := t.TempDir()
	capturePath := filepath.Join(callerDir, "dispatch-capture.txt")
	var command *exec.Cmd
	if runtime.GOOS == "windows" {
		fakeDispatcher := filepath.Join(callerDir, "fake-dispatcher.cmd")
		fakeDispatcherData := "@echo off\r\n" +
			"> \"%POLKA_TEST_CAPTURE%\" echo %*\r\n" +
			">> \"%POLKA_TEST_CAPTURE%\" echo %POLKA_TOOL_PROJECT_DIR%\r\n" +
			"exit /b 0\r\n"
		if err := os.WriteFile(fakeDispatcher, []byte(fakeDispatcherData), 0o755); err != nil {
			t.Fatalf("WriteFile(fake dispatcher) error = %v", err)
		}
		command = exec.Command("cmd.exe", "/c", filepath.Join(root, binDirectoryName, "php.cmd"), "--version")
		command.Env = append(os.Environ(), "POLKA_TOOL_DISPATCHER="+fakeDispatcher)
	} else {
		if _, err := exec.LookPath("sh"); err != nil {
			t.Skip("sh is not available")
		}
		fakeDispatcher := filepath.Join(callerDir, "fake-dispatcher")
		fakeDispatcherData := "#!/bin/sh\n" +
			"printf '%s\\n' \"$*\" > \"$POLKA_TEST_CAPTURE\"\n" +
			"printf '%s\\n' \"$POLKA_TOOL_PROJECT_DIR\" >> \"$POLKA_TEST_CAPTURE\"\n"
		if err := os.WriteFile(fakeDispatcher, []byte(fakeDispatcherData), 0o755); err != nil {
			t.Fatalf("WriteFile(fake dispatcher) error = %v", err)
		}
		command = exec.Command("sh", filepath.Join(root, binDirectoryName, "php"), "--version")
		command.Env = append(os.Environ(), "POLKA_TOOL_DISPATCHER="+fakeDispatcher)
	}
	command.Dir = callerDir
	command.Env = append(command.Env, "POLKA_TEST_CAPTURE="+capturePath, toolProjectDirEnv+"=")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("php shim output = %q, error = %v", string(output), err)
	}

	captureData, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("ReadFile(capture) error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(captureData), "\r\n", "\n")), "\n")
	if len(lines) != 2 {
		t.Fatalf("capture = %q, want dispatcher args and exported project", string(captureData))
	}
	args, exported := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	projectArg, rest, ok := strings.Cut(strings.TrimPrefix(args, "-C "), " dispatch ")
	if !strings.HasPrefix(args, "-C ") || !ok || rest != "php --version" {
		t.Fatalf("dispatcher args = %q, want -C <project> dispatch php --version", args)
	}
	assertSameDir(t, "dispatched project", filepath.Clean(strings.Trim(projectArg, `"`)), projectDir)
	assertSameDir(t, "exported project", filepath.Clean(exported), projectDir)
}
