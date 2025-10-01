//go:build integration
// +build integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lmtools/internal/mockserver"
)

func TestLoggingIntegration(t *testing.T) {
	// Get lmc binary
	lmcBin := getLmcBinary(t)

	// Create temporary directories
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()

	// Create mock server
	ms := mockserver.NewMockServer()
	defer ms.Close()

	// Test 1: Embedding request with logging
	stdout, stderr, logDir, err := runLmcCommandWithLogDir(t, lmcBin,
		[]string{"-argo-user", "testuser", "-e", "-model", "v3large",  "-argo-env", ms.URL(), "-sessions-dir", sessionsDir},
		"Test message for embedding")

	if err != nil {
		t.Fatalf("Failed to run embedding: %v\nStderr: %s", err, stderr)
	}

	// Verify embedding output
	if !strings.Contains(stdout, "[") || !strings.Contains(stdout, "]") {
		t.Errorf("Expected embedding array output, got: %s", stdout)
	}

	// Check that log directory was created
	if _, err := os.Stat(logDir); err != nil {
		t.Fatalf("Log directory not created: %v", err)
	}

	// Find log files
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	if !assertRecentLogFiles(t, logDir, "_embed_input", ".json") {
		t.Error("Request log file not found")
	}

	// Test 2: Chat request with logging (use same log dir)
	stdout, stderr, err = runLmcCommandWithSpecificLogDir(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o",  "-argo-env", ms.URL(), "-sessions-dir", sessionsDir},
		"Test chat message", logDir)

	if err != nil {
		t.Fatalf("Failed to run chat: %v\nStderr: %s", err, stderr)
	}

	// Check for new log files
	entries, err = os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	if !assertRecentLogFiles(t, logDir, "_chat_input", ".json") {
		t.Error("Chat request log file not found")
	}

	// Test 3: Verify log content format
	now := time.Now()
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "_lmc_") && strings.HasSuffix(entry.Name(), ".log") {
			info, err := entry.Info()
			if err == nil && now.Sub(info.ModTime()) < time.Minute {
				// Read the process log
				logPath := filepath.Join(logDir, entry.Name())
				content, err := os.ReadFile(logPath)
				if err != nil {
					t.Errorf("Failed to read log file %s: %v", entry.Name(), err)
					continue
				}

				logContent := string(content)
				if logContent == "" {
					// Log might be empty if nothing was logged
					continue
				}

				// Check for proper timestamp format
				lines := strings.Split(strings.TrimSpace(logContent), "\n")
				for _, line := range lines {
					if line == "" {
						continue
					}
					
					// Log format is now: [LEVEL] [RFC3339Nano timestamp] [component] message
					// Example: [INFO] [2025-08-13T03:42:20.524221955Z] [lmc] message
					
					// Should contain [INFO] or [WARN] or [DEBUG] or [ERROR]
					if !strings.Contains(line, "[INFO]") && !strings.Contains(line, "[WARN]") && 
					   !strings.Contains(line, "[DEBUG]") && !strings.Contains(line, "[ERROR]") {
						t.Errorf("Log line missing level: %s", line)
						continue
					}

					// Check for timestamp in RFC3339Nano format between brackets
					// Pattern: [2025-08-13T03:42:20.524221955Z]
					if !strings.Contains(line, "[20") || !strings.Contains(line, "T") || !strings.Contains(line, "Z]") {
						t.Errorf("Log line missing or invalid timestamp format: %s", line)
					}
				}
				
				t.Logf("Process log validated: %s", entry.Name())
				break
			}
		}
	}
}

func TestStreamingLogging(t *testing.T) {
	// Get lmc binary
	lmcBin := getLmcBinary(t)

	// Create temporary directories
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()
	logDir := t.TempDir()

	// Create mock server
	ms := mockserver.NewMockServer()
	defer ms.Close()

	// Test streaming request with custom log directory
	stdout, stderr, err := runLmcCommandWithSpecificLogDir(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-stream", "-argo-env", ms.URL(), "-sessions-dir", sessionsDir},
		"Test streaming message", logDir)

	if err != nil {
		t.Fatalf("Failed to run streaming: %v\nStderr: %s", err, stderr)
	}

	// Verify output contains the response text
	if !strings.Contains(stdout, "This is a mock response") {
		t.Errorf("Expected streaming output to contain response text, got: %s", stdout)
	}

	// Check log files
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	if !assertRecentLogFiles(t, logDir, "_stream_chat_input", ".json") {
		t.Error("Stream input log file not found")
	}
	if !assertRecentLogFiles(t, logDir, "_stream_chat_output", ".log") {
		t.Error("Stream output log file not found")
	} else {
		// Verify content
		for _, entry := range entries {
			if strings.Contains(entry.Name(), "_stream_chat_output") && strings.HasSuffix(entry.Name(), ".log") {
				logPath := filepath.Join(logDir, entry.Name())
				content, err := os.ReadFile(logPath)
				if err != nil {
					t.Errorf("Failed to read stream output log: %v", err)
				} else if len(content) == 0 {
					t.Error("Stream output log is empty")
				}
				break
			}
		}
	}

	// Verify session was created
	stdout, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}

	// Should have at least one session
	if !strings.Contains(stdout, " • ") {
		t.Error("No session created for streaming request")
	}
}

func TestConcurrentLogging(t *testing.T) {
	// Get lmc binary
	lmcBin := getLmcBinary(t)

	// Create temporary directories
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()

	// Create mock server
	ms := mockserver.NewMockServer()
	defer ms.Close()

	// Create shared log directory for this test
	logDir := t.TempDir()

	// Run multiple argo processes concurrently with same log directory
	const numProcesses = 5
	done := make(chan error, numProcesses)

	for i := 0; i < numProcesses; i++ {
		go func(id int) {
			// Use specific log dir for concurrent test
			_, stderr, err := runLmcCommandWithSpecificLogDir(t, lmcBin,
				[]string{"-argo-user", "testuser", "-e",  "-argo-env", ms.URL(), "-sessions-dir", sessionsDir},
				"Concurrent test message", logDir)
			if err != nil {
				t.Logf("Process %d failed: %v, stderr: %s", id, err, stderr)
			}
			done <- err
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < numProcesses; i++ {
		if err := <-done; err != nil {
			t.Errorf("Process failed: %v", err)
		}
	}

	// Wait for log files to be written
	processLogs := waitForLogFiles(t, logDir, "_lmc_", numProcesses, time.Second)
	requestLogs := waitForLogFiles(t, logDir, "_embed_input_", numProcesses, time.Second)

	t.Logf("Found %d process logs and %d request logs in %s", processLogs, requestLogs, logDir)

	// We should have at least numProcesses of each type
	if processLogs < numProcesses {
		t.Errorf("Expected at least %d process logs, found %d", numProcesses, processLogs)
	}

	if requestLogs < numProcesses {
		t.Errorf("Expected at least %d request logs, found %d", numProcesses, requestLogs)
	}
}