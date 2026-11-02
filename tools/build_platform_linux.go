package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// buildLockPollInterval is how often a waiting build retries the lock. Polling
// a non-blocking flock keeps the wait cancellable, which a blocking flock is not.
const buildLockPollInterval = 500 * time.Millisecond

// acquireBuildLock serializes builds of one tool label across processes. A
// compile can far outlast the cache metadata lock's staleness window, so this
// uses flock, which the kernel releases if the holder crashes. onWait runs once
// if another build already holds the lock.
func acquireBuildLock(ctx context.Context, lockPath string, onWait func()) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create build lock directory: %w", err)
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open build lock: %w", err)
	}

	waited := false
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			closeErr := file.Close()
			return nil, errors.Join(fmt.Errorf("acquire build lock: %w", err), closeErr)
		}
		if !waited {
			waited = true
			if onWait != nil {
				onWait()
			}
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), file.Close())
		case <-time.After(buildLockPollInterval):
		}
	}

	// Closing the descriptor releases the flock.
	return file.Close, nil
}

// configureBuildProcess puts a compile in its own process group and makes
// cancellation signal the whole group. make's compiler children would otherwise
// outlive Ctrl-C, because only the direct child is killed by default.
func configureBuildProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		return syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	}
	command.WaitDelay = 10 * time.Second
}
