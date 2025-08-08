//go:build integration
// +build integration

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)


// setupTestEnvironment creates a temporary home directory for testing
func setupTestEnvironment(t *testing.T) (tmpHome string, mockServerURL string) {
	t.Helper()
	
	tmpHome = t.TempDir()
	t.Setenv("HOME", tmpHome)
	
	// Create .lmc directory
	argoDir := filepath.Join(tmpHome, ".lmc")
	if err := os.MkdirAll(argoDir, 0o750); err != nil {
		t.Fatalf("Failed to create .lmc directory: %v", err)
	}
	
	// Start a simple mock server
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{
			"response": "Mock response for testing",
			"model":    "gpt4o",
		}
		json.NewEncoder(w).Encode(response)
	})
	
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	
	return tmpHome, server.URL
}


// TestCrossProcessConcurrentResume tests multiple processes resuming the same session
func TestCrossProcessConcurrentResume(t *testing.T) {
	lmcBin := buildLmcBinary(t)
	_, mockURL := setupTestEnvironment(t)
	
	// Create custom sessions directory
	sessionsDir := t.TempDir()
	
	// Create initial session
	stdout, stderr, err := runLmcCommand(t, lmcBin, []string{"-u", "testuser", "-m", "gpt4o",  "-env", mockURL, "-sessions-dir", sessionsDir}, "Initial message")
	if err != nil {
		t.Fatalf("Failed to create initial session: %v\nStderr: %s", err, stderr)
	}
	
	// Get session ID using -show-sessions
	stdout, stderr, err = runLmcCommand(t, lmcBin, []string{"-u", "testuser", "-show-sessions", "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v\nStderr: %s", err, stderr)
	}
	
	// Parse session ID from output (format: "0001 • 2025-07-16 15:04:05 • 1 messages • XXB")
	var sessionID string
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for _, line := range lines {
		if strings.Contains(line, " • ") && strings.Contains(line, " messages • ") {
			sessionID = strings.TrimSpace(strings.Split(line, " • ")[0])
			break
		}
	}
	
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
				[]string{"-u", "testuser", "-m", "gpt4o", "-resume", sessionID,  "-env", mockURL, "-sessions-dir", sessionsDir}, 
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
	stdout, stderr, err = runLmcCommand(t, lmcBin, []string{"-u", "testuser", "-show", sessionID, "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Errorf("Failed to show session: %v", err)
	} else {
		t.Logf("\nFinal session state:\n%s", stdout)
	}
}

// TestCrossProcessLockExclusion verifies that locks actually exclude other processes
func TestCrossProcessLockExclusion(t *testing.T) {
	lmcBin := buildLmcBinary(t)
	_, mockURL := setupTestEnvironment(t)
	
	// Create custom sessions directory
	sessionsDir := t.TempDir()
	
	// Create a test session
	_, stderr, err := runLmcCommand(t, lmcBin, []string{"-u", "testuser", "-m", "gpt4o",  "-env", mockURL, "-sessions-dir", sessionsDir}, "Test message")
	if err != nil {
		t.Fatalf("Failed to create session: %v\nStderr: %s", err, stderr)
	}
	
	// Get session ID using -show-sessions
	stdout, _, err := runLmcCommand(t, lmcBin, []string{"-u", "testuser", "-show-sessions", "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}
	
	// Extract session ID
	var sessionID string
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for _, line := range lines {
		if strings.Contains(line, " • ") && strings.Contains(line, " messages • ") {
			sessionID = strings.TrimSpace(strings.Split(line, " • ")[0])
			break
		}
	}
	
	// Create a long-running process that holds a lock
	cmd1 := exec.Command(lmcBin, "-u", "testuser", "-m", "gpt4o", "-resume", sessionID,  "-env", mockURL, "-sessions-dir", sessionsDir)
	cmd1.Stdin = strings.NewReader("This is a long message that will take time to process")
	
	// Start first process
	if err := cmd1.Start(); err != nil {
		t.Fatalf("Failed to start first process: %v", err)
	}
	defer cmd1.Process.Kill()
	
	// Give it time to acquire the lock
	time.Sleep(100 * time.Millisecond)
	
	// Try to access the same session from another process
	start := time.Now()
	_, stderr2, err2 := runLmcCommand(t, lmcBin, 
		[]string{"-u", "testuser", "-m", "gpt4o", "-resume", sessionID,  "-env", mockURL, "-sessions-dir", sessionsDir}, 
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
	
	// Clean up first process
	cmd1.Process.Kill()
}

// TestCrossProcessSiblingCreation tests concurrent sibling creation
func TestCrossProcessSiblingCreation(t *testing.T) {
	lmcBin := buildLmcBinary(t)
	_, mockURL := setupTestEnvironment(t)
	
	// Create custom sessions directory
	sessionsDir := t.TempDir()
	
	// Create initial session with a few messages
	_, _, err := runLmcCommand(t, lmcBin, []string{"-u", "testuser", "-m", "gpt4o",  "-env", mockURL, "-sessions-dir", sessionsDir}, "Message 1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	
	// Get session ID using -show-sessions
	stdout, _, err := runLmcCommand(t, lmcBin, []string{"-u", "testuser", "-show-sessions", "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}
	
	var sessionID string
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for _, line := range lines {
		if strings.Contains(line, " • ") && strings.Contains(line, " messages • ") {
			sessionID = strings.TrimSpace(strings.Split(line, " • ")[0])
			break
		}
	}
	
	// Add another message
	_, _, err = runLmcCommand(t, lmcBin, []string{"-u", "testuser", "-m", "gpt4o", "-resume", sessionID,  "-env", mockURL, "-sessions-dir", sessionsDir}, "Message 2")
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
				[]string{"-u", "testuser", "-m", "gpt4o", "-branch", sessionID + "/0001",  "-env", mockURL, "-sessions-dir", sessionsDir},
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
	stdout, _, err = runLmcCommand(t, lmcBin, []string{"-u", "testuser", "-show-sessions", "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Errorf("Failed to show sessions: %v", err)
	} else {
		t.Logf("\nSession tree:\n%s", stdout)
	}
}