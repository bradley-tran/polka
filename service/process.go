package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func prepareServiceCommand(target string, args []string) (*exec.Cmd, error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return nil, fmt.Errorf("dispatch target cannot be empty")
	}

	if runtime.GOOS == "windows" {
		extension := strings.ToLower(filepath.Ext(trimmed))
		if extension == ".cmd" || extension == ".bat" {
			commandArgs := append([]string{"/c", trimmed}, args...)
			return exec.Command("cmd.exe", commandArgs...), nil
		}
	}

	return exec.Command(trimmed, args...), nil
}

func openServiceLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create service log directory: %w", err)
	}
	logFile, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open service log %s: %w", path, err)
	}

	return logFile, nil
}

func stopServicePID(pid int) error {
	if pid <= 0 {
		return nil
	}
	if runtime.GOOS == "windows" {
		output, err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").CombinedOutput()
		if err == nil || isMissingServiceProcessOutput(string(output)) {
			return nil
		}

		return fmt.Errorf("stop service process %d: %w (%s)", pid, err, strings.TrimSpace(string(output)))
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := process.Kill(); err != nil && !isMissingServiceProcessOutput(err.Error()) {
		return fmt.Errorf("stop service process %d: %w", pid, err)
	}
	_ = process.Release()

	return nil
}

func stopServiceProcess(process *os.Process) {
	if process == nil {
		return
	}

	_ = stopServicePID(process.Pid)
	_ = process.Release()
}

func isMissingServiceProcessOutput(output string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(output))
	return trimmed == "" ||
		strings.Contains(trimmed, "not found") ||
		strings.Contains(trimmed, "no running instance") ||
		strings.Contains(trimmed, "process already finished") ||
		strings.Contains(trimmed, "no such process")
}
