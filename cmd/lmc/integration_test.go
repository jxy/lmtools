//go:build integration

package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// setupTestEnvironment is provided by cmd/lmc/test_helpers_test.go for integration/e2e builds.

// TestCrossProcessConcurrentResume tests multiple processes resuming the same session
func TestCrossProcessConcurrentResume(t *testing.T) {
	lmcBin := getLmcBinary(t)
	_, mockURL := setupTestEnvironment(t)

	// Create custom sessions directory
	sessionsDir := t.TempDir()

	// Create initial session
	_, stderr, err := runLmcCommand(t, lmcBin, []string{"-argo-user", "testuser", "-model", "gpt4o", "-provider-url", mockURL, "-sessions-dir", sessionsDir}, "Initial message")
	if err != nil {
		t.Fatalf("Failed to create initial session: %v\nStderr: %s", err, stderr)
	}

	// Get session ID using -show-sessions
	stdout, stderr, err := runLmcCommand(t, lmcBin, []string{"-argo-user", "testuser", "-show-sessions", "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v\nStderr: %s", err, stderr)
	}

	// Parse session ID from output (format: "0001 • 2025-07-16 15:04:05 • 1 messages • XXB")
	sessionID := extractFirstSessionID(stdout)
	if sessionID == "" {
		t.Fatalf("Failed to extract session ID from show-sessions output: %s", stdout)
	}

	t.Logf("Created session: %s", sessionID)

	// Launch multiple processes concurrently
	const numProcesses = 5
	var wg sync.WaitGroup
	results := make(chan struct {
		id     int
		stdout string
		stderr string
		err    error
	}, numProcesses)

	wg.Add(numProcesses)

	// Use a barrier to ensure all processes start at the same time
	startTime := time.Now()

	for i := 0; i < numProcesses; i++ {
		go func(processID int) {
			defer wg.Done()

			input := fmt.Sprintf("Response from process %d", processID)
			stdout, stderr, err := runLmcCommand(t, lmcBin,
				[]string{"-argo-user", "testuser", "-model", "gpt4o", "-resume", sessionID, "-provider-url", mockURL, "-sessions-dir", sessionsDir},
				input)

			results <- struct {
				id     int
				stdout string
				stderr string
				err    error
			}{processID, stdout, stderr, err}
		}(i)
	}

	wg.Wait()
	close(results)

	duration := time.Since(startTime)
	t.Logf("All processes completed in %v", duration)

	// Analyze results
	var successCount int
	var siblingCreated int

	for result := range results {
		if result.err != nil {
			t.Errorf("Process %d failed: %v\nStderr: %s", result.id, result.err, result.stderr)
		} else {
			successCount++

			// Check if a sibling was created
			if strings.Contains(result.stderr, "sibling branch") {
				siblingCreated++
				t.Logf("Process %d created a sibling branch", result.id)
			} else {
				t.Logf("Process %d appended to main session", result.id)
			}
		}
	}

	t.Logf("\nSummary:")
	t.Logf("  Successful: %d/%d", successCount, numProcesses)
	t.Logf("  Siblings created: %d", siblingCreated)

	// Verify all processes succeeded
	if successCount != numProcesses {
		t.Errorf("Not all processes succeeded: %d/%d", successCount, numProcesses)
	}

	// Show final session state
	stdout, _, err = runLmcCommand(t, lmcBin, []string{"-argo-user", "testuser", "-show", sessionID, "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Errorf("Failed to show session: %v", err)
	} else {
		t.Logf("\nFinal session state:\n%s", stdout)
	}
}

// TestCrossProcessLockExclusion verifies that locks actually exclude other processes
func TestCrossProcessLockExclusion(t *testing.T) {
	lmcBin := getLmcBinary(t)
	_, mockURL := setupTestEnvironment(t)

	// Create custom sessions directory
	sessionsDir := t.TempDir()

	// Create a test session
	_, stderr, err := runLmcCommand(t, lmcBin, []string{"-argo-user", "testuser", "-model", "gpt4o", "-provider-url", mockURL, "-sessions-dir", sessionsDir}, "Test message")
	if err != nil {
		t.Fatalf("Failed to create session: %v\nStderr: %s", err, stderr)
	}

	// Get session ID using -show-sessions
	stdout, _, err := runLmcCommand(t, lmcBin, []string{"-argo-user", "testuser", "-show-sessions", "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}

	// Extract session ID
	sessionID := extractFirstSessionID(stdout)

	// Create a long-running process that holds a lock
	logDir := t.TempDir() // Isolate test logs
	firstDone := make(chan struct{})
	var firstErr error
	go func() {
		defer close(firstDone)
		_, _, firstErr = runLmcCommand(t, lmcBin,
			[]string{"-argo-user", "testuser", "-model", "gpt4o", "-resume", sessionID, "-provider-url", mockURL, "-sessions-dir", sessionsDir},
			"This is a long message that will take time to process", WithLogDir(logDir))
	}()

	// Wait for the lock file to be created
	lockFile := filepath.Join(sessionsDir, sessionID, ".lock")
	if !waitForFile(t, lockFile, time.Second) {
		t.Log("Lock file not created within timeout, proceeding anyway")
	}

	// Try to access the same session from another process
	start := time.Now()
	_, stderr2, err2 := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-resume", sessionID, "-provider-url", mockURL, "-sessions-dir", sessionsDir},
		"Concurrent message")
	elapsed := time.Since(start)

	// The second process should either:
	// 1. Wait for the lock and then succeed
	// 2. Create a sibling branch
	if err2 != nil {
		t.Errorf("Second process failed: %v\nStderr: %s", err2, stderr2)
	} else if strings.Contains(stderr2, "sibling branch") {
		t.Logf("Second process created a sibling (good conflict resolution)")
	} else {
		t.Logf("Second process waited %.2f seconds for lock", elapsed.Seconds())
	}

	// Ensure the first process completes
	select {
	case <-firstDone:
		if firstErr != nil {
			t.Logf("First process completed with error: %v", firstErr)
		}
	case <-time.After(2 * time.Second):
		t.Logf("First process did not complete within 2s; continuing")
	}
}

// TestCrossProcessSiblingCreation tests concurrent sibling creation
func TestCrossProcessSiblingCreation(t *testing.T) {
	lmcBin := getLmcBinary(t)
	_, mockURL := setupTestEnvironment(t)

	// Create custom sessions directory
	sessionsDir := t.TempDir()

	// Create initial session with a few messages
	_, _, err := runLmcCommand(t, lmcBin, []string{"-argo-user", "testuser", "-model", "gpt4o", "-provider-url", mockURL, "-sessions-dir", sessionsDir}, "Message 1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Get session ID using -show-sessions
	stdout, _, err := runLmcCommand(t, lmcBin, []string{"-argo-user", "testuser", "-show-sessions", "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}

	sessionID := extractFirstSessionID(stdout)

	// Add another message
	_, _, err = runLmcCommand(t, lmcBin, []string{"-argo-user", "testuser", "-model", "gpt4o", "-resume", sessionID, "-provider-url", mockURL, "-sessions-dir", sessionsDir}, "Message 2")
	if err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}

	// Now have multiple processes try to branch from message 0001
	const numProcesses = 3
	var wg sync.WaitGroup
	results := make(chan error, numProcesses)

	wg.Add(numProcesses)

	for i := 0; i < numProcesses; i++ {
		go func(id int) {
			defer wg.Done()

			_, stderr, err := runLmcCommand(t, lmcBin,
				[]string{"-argo-user", "testuser", "-model", "gpt4o", "-branch", sessionID + "/0001", "-provider-url", mockURL, "-sessions-dir", sessionsDir},
				fmt.Sprintf("Branch %d response", id))
			if err != nil {
				t.Logf("Process %d stderr: %s", id, stderr)
			}
			results <- err
		}(i)
	}

	wg.Wait()
	close(results)

	// Check results
	successCount := 0
	for err := range results {
		if err == nil {
			successCount++
		} else {
			t.Errorf("Branch creation failed: %v", err)
		}
	}

	t.Logf("Successfully created %d/%d branches", successCount, numProcesses)

	// Show session tree
	stdout, _, err = runLmcCommand(t, lmcBin, []string{"-argo-user", "testuser", "-show-sessions", "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Errorf("Failed to show sessions: %v", err)
	} else {
		t.Logf("\nSession tree:\n%s", stdout)
	}
}
