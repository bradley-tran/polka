//go:build windows

package service

import (
	"os/exec"
	"strconv"
	"strings"
)

func servicePIDIsLive(pid int) bool {
	if pid <= 0 {
		return false
	}

	pidText := strconv.Itoa(pid)
	output, err := exec.Command("tasklist", "/FI", "PID eq "+pidText, "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}

	return strings.Contains(string(output), `"`+pidText+`"`)
}
