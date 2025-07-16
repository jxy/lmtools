//go:build !windows
// +build !windows

package argo

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
		done := make(chan struct{})
		acquired := make(chan error, 1)

		go func() {
			select {
			case <-done:
				return // Cancelled before acquiring
			default:
				err := syscall.Flock(fd, syscall.LOCK_EX)
				select {
				case acquired <- err:
				case <-done:
					// Main routine timed out, cleanup if we got the lock
					if err == nil {
						_ = syscall.Flock(fd, syscall.LOCK_UN)
					}
				}
			}
		}()

		select {
		case err := <-acquired:
			if err != nil {
				close(done)
				return fmt.Errorf("failed to acquire lock: %w", err)
			}
		case <-time.After(timeout):
			close(done)
			return ErrLockTimeout
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
