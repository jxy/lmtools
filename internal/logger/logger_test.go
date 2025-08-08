package logger

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoggerSuite(t *testing.T) {
	// Create a single temporary directory for all tests
	tmpDir := t.TempDir()

	// Initialize logger once for all tests
	err := Initialize(tmpDir, "info", "text", false)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	defer Close()

	// Run all tests as subtests
	t.Run("Initialize", func(t *testing.T) {
		testInitialize(t, tmpDir)
	})

	t.Run("InfoAndWarnLogging", func(t *testing.T) {
		testInfoAndWarnLogging(t, tmpDir)
	})

	t.Run("LogJSON", func(t *testing.T) {
		testLogJSON(t, tmpDir)
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		testConcurrentAccess(t, tmpDir)
	})
}

func testInitialize(t *testing.T, tmpDir string) {
	// Check that log directory was created
	if _, err := os.Stat(tmpDir); err != nil {
		t.Errorf("Log directory not created: %v", err)
	}

	// Check that process log file was created
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	var logFile string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "_lmc_") && strings.HasSuffix(entry.Name(), ".log") {
			logFile = entry.Name()
			break
		}
	}

	if logFile == "" {
		t.Fatal("No process log file created")
	}

	// Verify file name format
	if !strings.Contains(logFile, time.Now().Format("20060102")) {
		t.Errorf("Log file name doesn't contain today's date: %s", logFile)
	}

	if !strings.Contains(logFile, "_lmc_") {
		t.Errorf("Log file name doesn't contain _lmc_ pattern: %s", logFile)
	}
}

func testInfoAndWarnLogging(t *testing.T, tmpDir string) {
	// Log some messages
	Infof("Test info message: %s", "hello")
	Warnf("Test warning message: %d", 42)

	// Give time for logs to be written
	time.Sleep(10 * time.Millisecond)

	// Find and read the log file
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	var logFilePath string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "_lmc_") && strings.HasSuffix(entry.Name(), ".log") {
			logFilePath = filepath.Join(tmpDir, entry.Name())
			break
		}
	}

	if logFilePath == "" {
		t.Fatal("No log file found")
	}

	content, err := os.ReadFile(logFilePath)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	logContent := string(content)

	// Check for INFO message (now includes timestamp)
	if !strings.Contains(logContent, "[INFO]") || !strings.Contains(logContent, "Test info message: hello") {
		t.Errorf("INFO message not found in log file")
	}

	// Check for WARN message (now includes timestamp)
	if !strings.Contains(logContent, "[WARN]") || !strings.Contains(logContent, "Test warning message: 42") {
		t.Errorf("WARN message not found in log file")
	}

	// Check timestamp format (standard log package format)
	lines := strings.Split(strings.TrimSpace(logContent), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Standard log format: "2006/01/02 15:04:05 [LEVEL] message"
		if len(line) < 19 {
			t.Errorf("Line too short to contain timestamp: %s", line)
			continue
		}
		// Extract date and time parts
		datePart := line[:10]
		timePart := line[11:19]
		fullTimestamp := strings.ReplaceAll(datePart, "/", "-") + " " + timePart
		_, err := time.Parse("2006-01-02 15:04:05", fullTimestamp)
		if err != nil {
			t.Errorf("Invalid timestamp format in line: %s", line)
		}
	}
}

func testLogJSON(t *testing.T, tmpDir string) {
	payload := []byte(`{"foo":"bar"}`)

	if err := LogJSON(tmpDir, "myop", payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}

	// Find the JSON log file
	var jsonFile string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "myop") && strings.HasSuffix(entry.Name(), ".json") {
			jsonFile = entry.Name()
			break
		}
	}

	if jsonFile == "" {
		t.Fatal("JSON log file not found")
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, jsonFile))
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Errorf("file content = %q; want %q", data, payload)
	}
}

func TestLoggerFilePermissions(t *testing.T) {
	// Skip on Windows as Unix permissions don't apply
	if runtime.GOOS == "windows" {
		t.Skip("Skipping Unix permission test on Windows")
	}

	tmpDir := t.TempDir()

	// Test CreateLogFile permissions directly
	f, path, err := CreateLogFile(tmpDir, "test-perm-log")
	if err != nil {
		t.Fatalf("CreateLogFile failed: %v", err)
	}
	f.Close()

	// Check file permissions
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Failed to stat log file: %v", err)
	}

	mode := info.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("Expected log file permissions 0600, got %04o", mode)
	}

	// Test JSON log file permissions
	payload := []byte(`{"test":"data"}`)
	err = LogJSON(tmpDir, "test-perms", payload)
	if err != nil {
		t.Fatalf("LogJSON failed: %v", err)
	}

	// Find the JSON file
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	var jsonFile string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "test-perms") && strings.HasSuffix(entry.Name(), ".json") {
			jsonFile = filepath.Join(tmpDir, entry.Name())
			break
		}
	}

	if jsonFile == "" {
		t.Fatal("JSON log file not found")
	}

	// Check JSON file permissions
	info2, err := os.Stat(jsonFile)
	if err != nil {
		t.Fatalf("Failed to stat JSON file: %v", err)
	}

	mode2 := info2.Mode().Perm()
	if mode2 != 0o600 {
		t.Errorf("Expected JSON file permissions 0600, got %04o", mode2)
	}
}

// TestDebugLoggingBehaviors tests the different debug logging behaviors
// for lmc (with log file) and apiproxy (without log file)
func TestDebugLoggingBehaviors(t *testing.T) {
	t.Run("WithLogFile", func(t *testing.T) {
		// Reset the global logger for this sub-test
		globalLogger = nil
		once = sync.Once{}

		// Create a temporary directory for logs
		tmpDir := t.TempDir()

		// Initialize logger WITH log directory (like lmc does)
		if err := Initialize(tmpDir, "DEBUG", "text", false); err != nil {
			t.Fatalf("Failed to initialize logger: %v", err)
		}
		defer Close()

		// Capture stderr
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		// Log debug and info messages
		Debugf("This is a debug message")
		Infof("This is an info message")

		// Restore stderr and read captured output
		w.Close()
		os.Stderr = oldStderr
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(r); err != nil {
			t.Fatalf("Failed to read from pipe: %v", err)
		}
		stderrOutput := buf.String()

		// Verify DEBUG is NOT in stderr
		if strings.Contains(stderrOutput, "This is a debug message") {
			t.Errorf("Debug message found in stderr when log file is configured")
		}

		// Verify INFO IS in stderr
		if !strings.Contains(stderrOutput, "This is an info message") {
			t.Errorf("Info message not found in stderr")
		}

		// Verify both messages are in the log file
		files, err := filepath.Glob(filepath.Join(tmpDir, "*.log"))
		if err != nil || len(files) == 0 {
			t.Fatalf("No log file created")
		}

		logContent, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatalf("Failed to read log file: %v", err)
		}

		logStr := string(logContent)
		if !strings.Contains(logStr, "This is a debug message") {
			t.Errorf("Debug message not found in log file")
		}
		if !strings.Contains(logStr, "This is an info message") {
			t.Errorf("Info message not found in log file")
		}
	})

	t.Run("WithoutLogFile", func(t *testing.T) {
		// Reset the global logger for this sub-test
		globalLogger = nil
		once = sync.Once{}

		// Initialize logger WITHOUT log directory (like apiproxy does)
		if err := Initialize("", "DEBUG", "text", false); err != nil {
			t.Fatalf("Failed to initialize logger: %v", err)
		}
		defer Close()

		// Capture stderr
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		// Log debug and info messages
		Debugf("Debug message for apiproxy")
		Infof("Info message for apiproxy")

		// Restore stderr and read captured output
		w.Close()
		os.Stderr = oldStderr
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(r); err != nil {
			t.Fatalf("Failed to read from pipe: %v", err)
		}
		stderrOutput := buf.String()

		// Verify BOTH debug and info are in stderr
		if !strings.Contains(stderrOutput, "Debug message for apiproxy") {
			t.Errorf("Debug message not found in stderr when no log file configured")
		}
		if !strings.Contains(stderrOutput, "Info message for apiproxy") {
			t.Errorf("Info message not found in stderr")
		}
	})
}

// testConcurrentAccess tests that the logger is thread-safe under heavy concurrent load
func testConcurrentAccess(t *testing.T, tmpDir string) {
	// Don't reset the logger, use the existing one from the suite

	// Number of goroutines and operations per goroutine
	numGoroutines := 100
	opsPerGoroutine := 100

	// Use WaitGroup to synchronize
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Track start time for rate checking
	startTime := time.Now()

	// Track if any panics occurred
	var panicOccurred bool
	var panicValue interface{}

	// Launch concurrent goroutines
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicOccurred = true
					panicValue = r
				}
			}()

			// Each goroutine performs multiple logging operations
			for j := 0; j < opsPerGoroutine; j++ {
				// Mix different log levels and operations (skip debug since logger is at INFO level)
				switch j % 3 {
				case 0:
					Infof("Concurrent info message from goroutine %d iteration %d", id, j)
				case 1:
					Warnf("Concurrent warning message from goroutine %d iteration %d", id, j)
				case 2:
					Errorf("Concurrent error message from goroutine %d iteration %d", id, j)
				}

				// Also test JSON logging
				if j%20 == 0 {
					jsonData := fmt.Sprintf(`{"goroutine": %d, "iteration": %d}`, id, j)
					_ = LogJSON(tmpDir, fmt.Sprintf("test_%d", id), []byte(jsonData))
				}
			}
		}(i)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Check for any panics
	if panicOccurred {
		t.Fatalf("Panic detected during concurrent access: %v", panicValue)
	}

	// Calculate elapsed time and rate
	elapsed := time.Since(startTime)
	totalOps := numGoroutines * opsPerGoroutine
	opsPerSecond := float64(totalOps) / elapsed.Seconds()

	t.Logf("Concurrent test completed: %d operations in %v (%.0f ops/sec)",
		totalOps, elapsed, opsPerSecond)

	// Verify the log file exists and has content
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	// Find the latest log file
	var logFile string
	var latestTime time.Time
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".log") {
			info, err := entry.Info()
			if err == nil && info.ModTime().After(latestTime) {
				latestTime = info.ModTime()
				logFile = entry.Name()
			}
		}
	}

	if logFile == "" {
		t.Error("No log file found after concurrent operations")
		return
	}

	// Read the log file and verify it has substantial content
	logPath := filepath.Join(tmpDir, logFile)
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	// We should have many log lines
	lines := strings.Split(string(content), "\n")
	// Since we're at INFO level and logging INFO, WARN, ERROR (all 3 levels)
	// We expect all messages to be logged
	// Account for some potential race conditions or buffering, expect at least 90%
	expectedMinLines := int(float64(totalOps) * 0.9)
	actualLines := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			actualLines++
		}
	}
	if actualLines < expectedMinLines {
		t.Errorf("Expected at least %d log lines, got %d", expectedMinLines, actualLines)
	}

	// Verify we have messages from different goroutines
	goroutinesSeen := make(map[string]bool)
	for _, line := range lines {
		if strings.Contains(line, "goroutine") {
			// Extract goroutine ID from the log line
			parts := strings.Split(line, "goroutine")
			if len(parts) > 1 {
				// Get the ID part
				idPart := strings.TrimSpace(parts[1])
				// Take first word as ID
				if spaceIdx := strings.Index(idPart, " "); spaceIdx > 0 {
					goroutinesSeen[idPart[:spaceIdx]] = true
				}
			}
		}
	}

	// We should see logs from many different goroutines
	if len(goroutinesSeen) < numGoroutines/2 {
		t.Errorf("Expected logs from at least %d goroutines, saw %d",
			numGoroutines/2, len(goroutinesSeen))
	}
}

// TestConcurrentInitialization tests that multiple Initialize calls are safe
func TestConcurrentInitialization(t *testing.T) {
	// Create separate temp directories for each initialization
	numInits := 20
	var wg sync.WaitGroup
	wg.Add(numInits)

	errors := make(chan error, numInits)

	for i := 0; i < numInits; i++ {
		go func(id int) {
			defer wg.Done()

			tmpDir := t.TempDir()
			err := Initialize(tmpDir, "info", "text", false)
			if err != nil {
				errors <- err
			}

			// Try to log something
			Infof("Concurrent init test from id %d", id)

			// Clean up
			Close()
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Error during concurrent initialization: %v", err)
	}
}
