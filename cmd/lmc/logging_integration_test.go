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
	// Build argo binary
	lmcBin := buildLmcBinary(t)

	// Create temporary directories
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()

	// Create mock server
	ms := mockserver.NewMockServer()
	defer ms.Close()

	// Set up log directory
	logDir := filepath.Join(tmpHome, ".lmc", "logs")

	// Test 1: Embedding request with logging
	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-u", "testuser", "-e", "-m", "v3large",  "-env", ms.URL(), "-sessions-dir", sessionsDir},
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

	var requestLogFound bool
	var recentLogs []string

	now := time.Now()
	for _, entry := range entries {
		name := entry.Name()
		
		// Check if it's a recent file (within last minute)
		info, err := entry.Info()
		if err == nil && now.Sub(info.ModTime()) < time.Minute {
			recentLogs = append(recentLogs, name)
			
			if strings.Contains(name, "_embed_input") && strings.HasSuffix(name, ".json") {
				requestLogFound = true
			}
		}
	}

	t.Logf("Recent log files found: %v", recentLogs)

	// Note: Process logs are only created when logger is initialized with a log directory
	// The lmc binary doesn't create process logs by default

	if !requestLogFound {
		t.Error("Request log file not found")
	}

	// Test 2: Chat request with logging
	stdout, stderr, err = runLmcCommand(t, lmcBin,
		[]string{"-u", "testuser", "-m", "gpt4o",  "-env", ms.URL(), "-sessions-dir", sessionsDir},
		"Test chat message")

	if err != nil {
		t.Fatalf("Failed to run chat: %v\nStderr: %s", err, stderr)
	}

	// Check for new log files
	entries, err = os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	var chatRequestLogFound bool
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "_chat_input") && strings.HasSuffix(entry.Name(), ".json") {
			info, err := entry.Info()
			if err == nil && now.Sub(info.ModTime()) < time.Minute {
				chatRequestLogFound = true
				break
			}
		}
	}

	if !chatRequestLogFound {
		t.Error("Chat request log file not found")
	}

	// Test 3: Verify log content format
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
					
					// Should have timestamp format at start
					if len(line) < 19 {
						t.Errorf("Log line too short: %s", line)
						continue
					}

					// Verify timestamp can be parsed
					timestamp := line[:19]
					_, err := time.Parse("2006/01/02 15:04:05", timestamp)
					if err != nil {
						t.Errorf("Invalid timestamp in log: %s", line)
					}

					// Should contain [INFO] or [WARN]
					if !strings.Contains(line, "[INFO]") && !strings.Contains(line, "[WARN]") {
						t.Errorf("Log line missing level: %s", line)
					}
				}
				
				t.Logf("Process log validated: %s", entry.Name())
				break
			}
		}
	}
}

func TestConcurrentLogging(t *testing.T) {
	// Build argo binary
	lmcBin := buildLmcBinary(t)

	// Create temporary directories
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()

	// Create mock server
	ms := mockserver.NewMockServer()
	defer ms.Close()

	// Run multiple argo processes concurrently
	const numProcesses = 5
	done := make(chan error, numProcesses)

	for i := 0; i < numProcesses; i++ {
		go func(id int) {
			_, stderr, err := runLmcCommand(t, lmcBin,
				[]string{"-u", "testuser", "-e",  "-env", ms.URL(), "-sessions-dir", sessionsDir},
				"Concurrent test message")
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

	// Give a moment for log files to be written
	time.Sleep(100 * time.Millisecond)

	// Verify multiple log files were created
	logDir := filepath.Join(tmpHome, ".lmc", "logs")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	processLogs := 0
	requestLogs := 0
	now := time.Now()

	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil && now.Sub(info.ModTime()) < time.Minute {
			t.Logf("Found log file: %s", entry.Name())
			if strings.Contains(entry.Name(), "_lmc_") && strings.HasSuffix(entry.Name(), ".log") {
				processLogs++
			}
			if strings.Contains(entry.Name(), "_embed_input_") && strings.HasSuffix(entry.Name(), ".json") {
				requestLogs++
			}
		}
	}

	t.Logf("Found %d process logs and %d request logs", processLogs, requestLogs)

	// We should have at least numProcesses of each type
	if processLogs < numProcesses {
		t.Errorf("Expected at least %d process logs, found %d", numProcesses, processLogs)
	}

	if requestLogs < numProcesses {
		t.Errorf("Expected at least %d request logs, found %d", numProcesses, requestLogs)
	}
}