package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunLogsPrintsAllExistingNginxLogs(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeLogsTestConfig(t, projectDir)
	writeTestLogFile(t, filepath.Join(root, "run", "nginx", "demo", "logs", "access.log"), "access\n")
	writeTestLogFile(t, filepath.Join(root, "run", "nginx", "demo", "logs", "error.log"), "error\n")
	writeTestLogFile(t, filepath.Join(root, "run", "nginx", "demo", "serve.log"), "debug-serve\n")
	writeTestLogFile(t, filepath.Join(root, "run", "nginx", "demo", "php.log"), "debug-php\n")

	if code := Run(stdout, stderr, []string{"--root", root, "logs", "nginx"}); code != 0 {
		t.Fatalf("Run(logs nginx) code = %d, stderr = %q", code, stderr.String())
	}
	want := "access\nerror\ndebug-serve\ndebug-php\n"
	if stdout.String() != want {
		t.Fatalf("Run(logs nginx) stdout = %q, want %q", stdout.String(), want)
	}
}

// TestRunLogsFollowsAppendedOutput verifies both spellings of the follow flag
// through Cobra parsing and the full log-resolution workflow.
func TestRunLogsFollowsAppendedOutput(t *testing.T) {
	for _, flag := range []string{"-f", "--follow"} {
		t.Run(flag, func(t *testing.T) {
			projectDir := t.TempDir()
			root := filepath.Join(projectDir, ".polka")
			logPath := filepath.Join(root, "run", "nginx", "demo", "logs", "access.log")
			stderr := &bytes.Buffer{}

			writeLogsTestConfig(t, projectDir)
			writeTestLogFile(t, logPath, "initial\n")

			executeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			stdout := newFollowTestWriter("initial\n", "appended\n", cancel)
			appendResult := make(chan error, 1)
			go func() {
				select {
				case <-stdout.initialWritten:
				case <-executeCtx.Done():
					appendResult <- executeCtx.Err()
					return
				}
				file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					appendResult <- err
					cancel()
					return
				}
				if _, err := file.WriteString("appended\n"); err != nil {
					appendResult <- errors.Join(err, file.Close())
					cancel()
					return
				}
				appendResult <- file.Close()
			}()

			commandCtx := &commandContext{stdout: stdout, stderr: stderr}
			command := newRootCommand(commandCtx)
			command.SetArgs([]string{"--root", root, "logs", "nginx", flag})
			command.SetOut(stdout)
			command.SetErr(stderr)
			if err := command.ExecuteContext(executeCtx); err != nil {
				t.Fatalf("ExecuteContext(logs nginx %s) error = %v", flag, err)
			}
			if err := <-appendResult; err != nil {
				t.Fatalf("append followed log error = %v", err)
			}
			if commandCtx.exitCode != 0 {
				t.Fatalf("ExecuteContext(logs nginx %s) code = %d, stderr = %q", flag, commandCtx.exitCode, stderr.String())
			}
			if got := stdout.String(); got != "initial\nappended\n" {
				t.Fatalf("ExecuteContext(logs nginx %s) stdout = %q, want initial and appended output", flag, got)
			}
		})
	}
}

func TestRunLogsFiltersByLevel(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeLogsTestConfig(t, projectDir)
	writeTestLogFile(t, filepath.Join(root, "run", "nginx", "demo", "logs", "access.log"), "access\n")
	writeTestLogFile(t, filepath.Join(root, "run", "nginx", "demo", "logs", "error.log"), "error\n")

	if code := Run(stdout, stderr, []string{"--root", root, "logs", "nginx", "--level", "error"}); code != 0 {
		t.Fatalf("Run(logs nginx --level error) code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "error\n" {
		t.Fatalf("Run(logs nginx --level error) stdout = %q, want error log only", stdout.String())
	}
}

func TestRunLogsPrintsFrankenPHPServeLog(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {FrankenPHP: "1.12"},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")
	writeTestLogFile(t, filepath.Join(root, "run", "frankenphp", "demo", "serve.log"), "frankenphp-debug\n")

	if code := Run(stdout, stderr, []string{"--root", root, "logs", "frankenphp"}); code != 0 {
		t.Fatalf("Run(logs frankenphp) code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "frankenphp-debug\n" {
		t.Fatalf("Run(logs frankenphp) stdout = %q, want serve log", stdout.String())
	}
}

func TestRunLogsErrorsWhenNoMatchingFilesExist(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeLogsTestConfig(t, projectDir)

	if code := Run(stdout, stderr, []string{"--root", root, "logs", "nginx"}); code == 0 {
		t.Fatal("Run(logs nginx) code = 0, want error")
	}
	if !strings.Contains(stderr.String(), "no log files found for nginx") {
		t.Fatalf("Run(logs nginx) stderr = %q, want missing logs error", stderr.String())
	}
}

func TestRunLogsRejectsInvalidLevel(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeLogsTestConfig(t, projectDir)

	if code := Run(stdout, stderr, []string{"--root", root, "logs", "nginx", "--level", "trace"}); code == 0 {
		t.Fatal("Run(logs nginx --level trace) code = 0, want error")
	}
	if !strings.Contains(stderr.String(), "invalid log level") {
		t.Fatalf("Run(logs nginx --level trace) stderr = %q, want invalid level error", stderr.String())
	}
}

func writeLogsTestConfig(t *testing.T, projectDir string) {
	t.Helper()

	root := filepath.Join(projectDir, ".polka")
	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {Nginx: "1.30"},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")
}

func writeTestLogFile(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

// followTestWriter captures output and coordinates the append and cancellation
// points without reading a bytes.Buffer concurrently with the command.
type followTestWriter struct {
	mu             sync.Mutex
	buffer         bytes.Buffer
	initial        string
	appended       string
	initialWritten chan struct{}
	cancel         context.CancelFunc
	initialOnce    sync.Once
	appendedOnce   sync.Once
}

// newFollowTestWriter creates a writer that cancels follow mode after observing
// the expected appended output.
func newFollowTestWriter(initial, appended string, cancel context.CancelFunc) *followTestWriter {
	return &followTestWriter{
		initial:        initial,
		appended:       appended,
		initialWritten: make(chan struct{}),
		cancel:         cancel,
	}
}

// Write records log output and signals the test's producer and cancellation.
func (writer *followTestWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	written, err := writer.buffer.Write(data)
	output := writer.buffer.String()
	if strings.Contains(output, writer.initial) {
		writer.initialOnce.Do(func() { close(writer.initialWritten) })
	}
	if strings.Contains(output, writer.appended) {
		writer.appendedOnce.Do(writer.cancel)
	}
	return written, err
}

// String returns the captured output after the followed command has stopped.
func (writer *followTestWriter) String() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	return writer.buffer.String()
}
