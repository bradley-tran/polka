//go:build windows

package service

import (
	"os/exec"
	"syscall"
	"time"
)

// setProcessGroup starts the command in a new console process group, so a
// Ctrl+C typed later in the terminal that launched it does not stop it.
// Children it starts inherit the group.
func setProcessGroup(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// stopProcessGroup stops pid and every descendant. taskkill /T already walks
// the process tree, so no grace period applies.
func stopProcessGroup(pid int, _ time.Duration) error {
	return stopServicePID(pid)
}
