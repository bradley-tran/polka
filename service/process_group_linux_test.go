//go:build linux

package service

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestStopProcessGroupStopsDescendants checks that stopping a group leader
// also stops the background child it started, which a plain PID kill would
// leave running.
func TestStopProcessGroupStopsDescendants(t *testing.T) {
	command := exec.Command("sh", "-c", "sleep 60 & echo $!; wait")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	setProcessGroup(command)
	if err := command.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	exited := make(chan struct{})
	go func() {
		// The leader is expected to die from a signal; only its exit matters.
		_ = command.Wait()
		close(exited)
	}()

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read child pid error = %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("parse child pid %q error = %v", line, err)
	}

	if err := stopProcessGroup(command.Process.Pid, 2*time.Second); err != nil {
		t.Fatalf("stopProcessGroup() error = %v", err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("group leader still running after stopProcessGroup")
	}

	deadline := time.Now().Add(5 * time.Second)
	for linuxProcessRunning(childPID) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d still running after stopProcessGroup", childPID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// linuxProcessRunning reports whether pid exists and is not a zombie. Zombies
// count as stopped because reaping them is up to their new parent, which in a
// container may never happen.
func linuxProcessRunning(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// The state follows the parenthesized command name: "pid (comm) S ...".
	fields := strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))

	return len(fields) > 0 && fields[0] != "Z"
}
