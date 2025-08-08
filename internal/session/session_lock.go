//go:build !windows
// +build !windows

package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var (
	ErrLockTimeout = errors.New("lock acquisition timeout")
	ErrLockHeld    = errors.New("lock is already held")
)

// WithSessionLock executes a function while holding an exclusive lock on the session.
// If timeout is 0, it waits indefinitely. If timeout > 0, it returns ErrLockTimeout on timeout.
func WithSessionLock(sessionPath string, timeout time.Duration, fn func() error) error {
	lockPath := sessionPath + ".lock"

	// Ensure lock directory exists
	lockDir := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDir, 0o750); err != nil {
		return fmt.Errorf("failed to create lock directory: %w", err)
	}

	// Open or create lock file
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open lock file: %w", err)
	}
	defer syscall.Close(fd)

	// Try to acquire lock with timeout handling
	if timeout > 0 {
		deadline := time.Now().Add(timeout)
		backoff := time.Millisecond
		maxBackoff := 50 * time.Millisecond

		for {
			// Try non-blocking lock acquisition
			err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				// Successfully acquired lock
				break
			}

			// Check if it's a "would block" error
			if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
				return fmt.Errorf("failed to acquire lock: %w", err)
			}

			// Check timeout
			if time.Now().After(deadline) {
				return ErrLockTimeout
			}

			// Back off before retrying
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
		}
	} else {
		// Wait indefinitely
		if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
			return fmt.Errorf("failed to acquire lock: %w", err)
		}
	}

	// We have the lock, ensure we release it
	defer func() {
		// Best effort unlock - ignore errors on cleanup
		_ = syscall.Flock(fd, syscall.LOCK_UN)
	}()

	// Execute the function
	return fn()
}

// WithSessionLockT is a generic version that returns a value
func WithSessionLockT[T any](sessionPath string, timeout time.Duration, fn func() (T, error)) (T, error) {
	var result T
	err := WithSessionLock(sessionPath, timeout, func() error {
		var innerErr error
		result, innerErr = fn()
		return innerErr
	})
	return result, err
}
