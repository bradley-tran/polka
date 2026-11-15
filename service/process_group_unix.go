//go:build !windows

package service

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// processGroupPollInterval is how often stopProcessGroup checks whether the
// group has exited after SIGTERM.
const processGroupPollInterval = 50 * time.Millisecond

// setProcessGroup makes the command the leader of a new process group, so
// stopProcessGroup can later signal it and every descendant together, and so
// terminal signals aimed at the launching shell's job do not reach it.
func setProcessGroup(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
}

// stopProcessGroup sends SIGTERM to the process group led by pid, waits up to
// grace for every member to exit, then sends SIGKILL to whatever remains. A
// pid that does not lead a group falls back to stopping the single process.
func stopProcessGroup(pid int, grace time.Duration) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			// No such group: either everything already exited or pid was
			// started without its own group. Stopping the single process
			// covers the second case and is a no-op in the first.
			return stopServicePID(pid)
		}
		return fmt.Errorf("signal process group %d: %w", pid, err)
	}

	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !processGroupIsLive(pid) {
			return nil
		}
		time.Sleep(processGroupPollInterval)
	}

	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process group %d: %w", pid, err)
	}

	return nil
}

// processGroupIsLive reports whether any process in the group still exists.
// EPERM means a member exists but belongs to another user.
func processGroupIsLive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
