package argo

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SessionLock represents a lock on a session
type SessionLock struct {
	Path     string
	File     *os.File
	acquired bool
}

// AcquireSessionLock attempts to acquire an exclusive lock on a session
func AcquireSessionLock(sessionPath string) (*SessionLock, error) {
	lockPath := filepath.Join(sessionPath, ".lock")

	// Try to create lock file exclusively
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			// Lock file exists, check if process is still alive
			if isLockStale(lockPath) {
				// Remove stale lock and retry
				os.Remove(lockPath)
				file, err = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
				if err != nil {
					return nil, fmt.Errorf("failed to acquire lock after removing stale lock: %w", err)
				}
			} else {
				return nil, fmt.Errorf("session is locked by another process")
			}
		} else {
			return nil, fmt.Errorf("failed to create lock file: %w", err)
		}
	}

	// Write our PID to the lock file
	pid := os.Getpid()
	if _, err := fmt.Fprintf(file, "%d\n%d\n", pid, time.Now().Unix()); err != nil {
		file.Close()
		os.Remove(lockPath)
		return nil, fmt.Errorf("failed to write lock info: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(lockPath)
		return nil, fmt.Errorf("failed to sync lock file: %w", err)
	}

	return &SessionLock{
		Path:     lockPath,
		File:     file,
		acquired: true,
	}, nil
}

// TryAcquireSessionLock attempts to acquire a lock with timeout
func TryAcquireSessionLock(sessionPath string, timeout time.Duration) (*SessionLock, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		lock, err := AcquireSessionLock(sessionPath)
		if err == nil {
			return lock, nil
		}

		// Check if it's a lock conflict
		if err.Error() == "session is locked by another process" {
			// Wait a bit and retry
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Other errors, return immediately
		return nil, err
	}

	return nil, fmt.Errorf("failed to acquire lock within timeout")
}

// Release releases the session lock
func (l *SessionLock) Release() error {
	if !l.acquired {
		return nil
	}

	if l.File != nil {
		l.File.Close()
		l.File = nil
	}

	if err := os.Remove(l.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove lock file: %w", err)
	}

	l.acquired = false
	return nil
}

// isLockStale checks if a lock file is stale (process no longer exists)
func isLockStale(lockPath string) bool {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return true // Can't read, assume stale
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) < 1 {
		return true // Invalid format, assume stale
	}

	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return true // Invalid PID, assume stale
	}

	// Check if process exists
	if !isProcessAlive(pid) {
		return true
	}

	// Check age if timestamp is available
	if len(lines) >= 2 {
		timestamp, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
		if err == nil {
			lockTime := time.Unix(timestamp, 0)
			// Consider lock stale if older than 1 hour
			if time.Since(lockTime) > time.Hour {
				return true
			}
		}
	}

	return false
}

// isProcessAlive checks if a process with given PID exists
func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// On Unix systems, sending signal 0 checks if process exists
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// WithSessionLock executes a function with a session lock
func WithSessionLock(sessionPath string, fn func() error) error {
	lock, err := AcquireSessionLock(sessionPath)
	if err != nil {
		return fmt.Errorf("failed to acquire session lock: %w", err)
	}
	defer lock.Release()

	return fn()
}

// WithSessionLockT executes a function with a session lock and returns a value
func WithSessionLockT[T any](sessionPath string, fn func() (T, error)) (T, error) {
	var zero T
	lock, err := AcquireSessionLock(sessionPath)
	if err != nil {
		return zero, fmt.Errorf("failed to acquire session lock: %w", err)
	}
	defer lock.Release()

	return fn()
}
