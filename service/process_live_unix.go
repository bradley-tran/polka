//go:build !windows

package service

import (
	"errors"
	"os"
	"syscall"
)

func servicePIDIsLive(pid int) bool {
	if pid <= 0 {
		return false
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer process.Release()

	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
