package tools

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestAcquireBuildLockSerializesAndCancels verifies a second build waits and says
// so, can be cancelled while waiting, and proceeds once the first one finishes.
func TestAcquireBuildLockSerializesAndCancels(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "locks", "demo.lock")
	release, err := acquireBuildLock(context.Background(), lockPath, nil)
	if err != nil {
		t.Fatalf("acquireBuildLock() error = %v", err)
	}

	waited := false
	ctx, cancel := context.WithTimeout(context.Background(), 2*buildLockPollInterval)
	defer cancel()
	if _, err := acquireBuildLock(ctx, lockPath, func() { waited = true }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended acquireBuildLock() error = %v, want deadline exceeded", err)
	}
	if !waited {
		t.Fatal("onWait was not called while another build held the lock")
	}

	if err := release(); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	second, err := acquireBuildLock(ctx, lockPath, nil)
	if err != nil {
		t.Fatalf("acquireBuildLock() after release error = %v", err)
	}
	if err := second(); err != nil {
		t.Fatalf("second release() error = %v", err)
	}
}
