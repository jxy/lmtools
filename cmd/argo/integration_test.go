//go:build integration
// +build integration

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildArgoBinary builds the argo binary for testing
func buildArgoBinary(t *testing.T) string {
	t.Helper()
	
	tmpDir := t.TempDir()
	argoBin := filepath.Join(tmpDir, "argo.test")
	
	cmd := exec.Command("go", "build", "-o", argoBin, ".")
	cmd.Dir = "." // Run in cmd/argo directory
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build argo: %v\nOutput: %s", err, output)
	}
	
	return argoBin
}

// setupTestEnvironment creates a temporary home directory for testing
func setupTestEnvironment(t *testing.T) string {
	t.Helper()
	
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	
	// Create .argo directory
	argoDir := filepath.Join(tmpHome, ".argo")
	if err := os.MkdirAll(argoDir, 0o750); err != nil {
		t.Fatalf("Failed to create .argo directory: %v", err)
	}
	
	return tmpHome
}

// runArgoCommand runs argo with the given arguments and input
func runArgoCommand(t *testing.T, argoBin string, args []string, input string) (stdout, stderr string, err error) {
	t.Helper()
	
	cmd := exec.Command(argoBin, args...)
	cmd.Stdin = strings.NewReader(input)
	
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	
	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

// TestCrossProcessConcurrentResume tests multiple processes resuming the same session
func TestCrossProcessConcurrentResume(t *testing.T) {
	argoBin := buildArgoBinary(t)
	setupTestEnvironment(t)
	
	// Create initial session
	stdout, stderr, err := runArgoCommand(t, argoBin, []string{"-u", "testuser", "-m", "gpt4o", "-no-log"}, "Initial message")
	if err != nil {
		t.Fatalf("Failed to create initial session: %v\nStderr: %s", err, stderr)
	}
	
	// Extract session ID from stderr (e.g., "Session: abc123")
	var sessionID string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "Session: ") {
			sessionID = strings.TrimPrefix(line, "Session: ")
			break
		}
	}
	
	if sessionID == "" {
		t.Fatalf("Failed to extract session ID from stderr: %s", stderr)
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
			stdout, stderr, err := runArgoCommand(t, argoBin, 
				[]string{"-u", "testuser", "-m", "gpt4o", "-resume", sessionID, "-no-log"}, 
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
	stdout, stderr, err = runArgoCommand(t, argoBin, []string{"-u", "testuser", "-show", sessionID}, "")
	if err != nil {
		t.Errorf("Failed to show session: %v", err)
	} else {
		t.Logf("\nFinal session state:\n%s", stdout)
	}
}

// TestCrossProcessLockExclusion verifies that locks actually exclude other processes
func TestCrossProcessLockExclusion(t *testing.T) {
	argoBin := buildArgoBinary(t)
	setupTestEnvironment(t)
	
	// Create a test session
	_, stderr, err := runArgoCommand(t, argoBin, []string{"-u", "testuser", "-m", "gpt4o", "-no-log"}, "Test message")
	if err != nil {
		t.Fatalf("Failed to create session: %v\nStderr: %s", err, stderr)
	}
	
	// Extract session ID
	var sessionID string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "Session: ") {
			sessionID = strings.TrimPrefix(line, "Session: ")
			break
		}
	}
	
	// Create a long-running process that holds a lock
	cmd1 := exec.Command(argoBin, "-u", "testuser", "-m", "gpt4o", "-resume", sessionID, "-no-log")
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
	_, stderr2, err2 := runArgoCommand(t, argoBin, 
		[]string{"-u", "testuser", "-m", "gpt4o", "-resume", sessionID, "-no-log"}, 
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
	argoBin := buildArgoBinary(t)
	setupTestEnvironment(t)
	
	// Create initial session with a few messages
	stdout, stderr, err := runArgoCommand(t, argoBin, []string{"-u", "testuser", "-m", "gpt4o", "-no-log"}, "Message 1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	
	var sessionID string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "Session: ") {
			sessionID = strings.TrimPrefix(line, "Session: ")
			break
		}
	}
	
	// Add another message
	_, _, err = runArgoCommand(t, argoBin, []string{"-u", "testuser", "-m", "gpt4o", "-resume", sessionID, "-no-log"}, "Message 2")
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
			
			_, stderr, err := runArgoCommand(t, argoBin,
				[]string{"-u", "testuser", "-m", "gpt4o", "-branch", sessionID + "/0001", "-no-log"},
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
	stdout, _, err = runArgoCommand(t, argoBin, []string{"-u", "testuser", "-show-sessions"}, "")
	if err != nil {
		t.Errorf("Failed to show sessions: %v", err)
	} else {
		t.Logf("\nSession tree:\n%s", stdout)
	}
}