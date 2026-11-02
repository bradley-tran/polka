package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// stopTestProcess shares one Wait result between background reaping and cleanup.
// exec.Cmd.Wait must not be called concurrently or more than once.
type stopTestProcess struct {
	*exec.Cmd
	waitOnce sync.Once
	waitErr  error
}

// Wait reaps the child once and waits for any concurrent caller to finish.
func (command *stopTestProcess) Wait() error {
	command.waitOnce.Do(func() {
		command.waitErr = command.Cmd.Wait()
	})
	return command.waitErr
}

// startStopTestProcess starts a shell script and returns once it signals that
// its traps are installed, so a test's signal cannot arrive before them.
func startStopTestProcess(t *testing.T, script string) (*stopTestProcess, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows stops serve processes through taskkill")
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	ready := filepath.Join(dir, "ready")
	command := &stopTestProcess{Cmd: exec.Command("sh", "-c", script, "sh", marker, ready)}
	if err := command.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		// Already gone in the passing case; this only guards a failing test.
		_ = command.Process.Kill()
		// Forced termination can produce an exit error; cleanup only needs to reap.
		_ = command.Wait()
	})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(ready); err == nil {
			return command, marker
		}
		if time.Now().After(deadline) {
			t.Fatal("test process did not become ready")
		}
	}
}

// reapStopTestProcess reaps the child in the background, as init does for a real
// server, whose parent `polka serve` has long since exited.
func reapStopTestProcess(command *stopTestProcess) {
	// These tests assert shutdown behavior separately, so the exit status is ignored.
	go func() { _ = command.Wait() }()
}

// TestStopServePIDAsksServerToExitBeforeKilling verifies a server receives
// SIGTERM. nginx and httpd masters only stop their workers when asked; killing
// the master outright leaves workers holding the port.
func TestStopServePIDAsksServerToExitBeforeKilling(t *testing.T) {
	script := `trap 'echo stopped > "$1"; exit 0' TERM; touch "$2"; while :; do sleep 0.05; done`
	command, marker := startStopTestProcess(t, script)
	reapStopTestProcess(command)

	if err := stopServePID(command.Process.Pid); err != nil {
		t.Fatalf("stopServePID() error = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("Stat(marker) error = %v, want the SIGTERM handler to have run", err)
	}
}

// TestStopServePIDKillsServerIgnoringTerm verifies a hung server cannot block
// stop: once the grace period ends it is killed.
func TestStopServePIDKillsServerIgnoringTerm(t *testing.T) {
	oldGrace := serveStopGracePeriod
	serveStopGracePeriod = 300 * time.Millisecond
	t.Cleanup(func() { serveStopGracePeriod = oldGrace })

	script := `trap '' TERM; touch "$2"; while :; do sleep 0.05; done`
	command, _ := startStopTestProcess(t, script)
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()

	if err := stopServePID(command.Process.Pid); err != nil {
		t.Fatalf("stopServePID() error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process survived stopServePID")
	}
}

// TestStopServePIDDoesNotWaitForZombie verifies cleanup of an unreaped child that
// already exited returns immediately rather than using up the grace period.
func TestStopServePIDDoesNotWaitForZombie(t *testing.T) {
	command, _ := startStopTestProcess(t, `touch "$2"; exit 0`)
	// Give the child time to exit; it stays a zombie because nothing has waited.
	time.Sleep(100 * time.Millisecond)

	started := time.Now()
	if err := stopServePID(command.Process.Pid); err != nil {
		t.Fatalf("stopServePID() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= serveStopGracePeriod {
		t.Fatalf("stopServePID() took %v, want less than the %v grace period", elapsed, serveStopGracePeriod)
	}
}

// TestStopServePIDIgnoresMissingProcess verifies stopping a server that already
// exited succeeds.
func TestStopServePIDIgnoresMissingProcess(t *testing.T) {
	command, _ := startStopTestProcess(t, `touch "$2"; exit 0`)
	if err := command.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if err := stopServePID(command.Process.Pid); err != nil {
		t.Fatalf("stopServePID() error = %v", err)
	}
}
