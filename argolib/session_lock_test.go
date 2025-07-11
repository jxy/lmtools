//go:build !windows
// +build !windows

package argo

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWithSessionLock_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "test-session")

	executed := false
	err := WithSessionLock(sessionPath, 0, func() error {
		executed = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithSessionLock failed: %v", err)
	}
	if !executed {
		t.Fatal("Function was not executed")
	}
}

func TestWithSessionLock_Concurrent(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "test-session")

	var counter int
	var mu sync.Mutex

	// Run 10 goroutines trying to acquire the same lock
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithSessionLock(sessionPath, 0, func() error {
				mu.Lock()
				current := counter
				counter = current + 1
				mu.Unlock()
				time.Sleep(10 * time.Millisecond) // Simulate work
				return nil
			})
			if err != nil {
				t.Errorf("WithSessionLock failed: %v", err)
			}
		}()
	}

	wg.Wait()

	if counter != 10 {
		t.Fatalf("Expected counter to be 10, got %d", counter)
	}
}

func TestWithSessionLock_Timeout(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "test-session")

	// Hold the lock in a goroutine
	holding := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_ = WithSessionLock(sessionPath, 0, func() error {
			close(holding)
			<-done // Wait until told to release
			return nil
		})
	}()

	// Wait for lock to be held
	<-holding

	// Try to acquire with timeout
	err := WithSessionLock(sessionPath, 50*time.Millisecond, func() error {
		t.Fatal("Should not have acquired lock")
		return nil
	})

	if err != ErrLockTimeout {
		t.Fatalf("Expected ErrLockTimeout, got %v", err)
	}

	close(done) // Release the lock
}

func TestWithSessionLockT_ReturnsValue(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "test-session")

	result, err := WithSessionLockT(sessionPath, 0, func() (string, error) {
		return "success", nil
	})
	if err != nil {
		t.Fatalf("WithSessionLockT failed: %v", err)
	}
	if result != "success" {
		t.Fatalf("Expected 'success', got %q", result)
	}
}

func TestWithSessionLock_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "nested", "deep", "session")

	err := WithSessionLock(sessionPath, 0, func() error {
		// Check that lock file was created
		lockPath := sessionPath + ".lock"
		if _, err := os.Stat(lockPath); os.IsNotExist(err) {
			t.Error("Lock file was not created")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithSessionLock failed: %v", err)
	}
}
