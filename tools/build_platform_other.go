//go:build !linux

package tools

import (
	"context"
	"os/exec"
)

// acquireBuildLock is a no-op where builds are not supported. It exists so the
// platform-neutral engine compiles and its fake-runner tests run everywhere.
func acquireBuildLock(context.Context, string, func()) (func() error, error) {
	return func() error { return nil }, nil
}

// configureBuildProcess leaves the command unchanged where builds are not supported.
func configureBuildProcess(*exec.Cmd) {}
