package argo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInitLogging(t *testing.T) {
	// Create temporary log directory
	tmpDir := t.TempDir()
	oldLogBaseDir := logBaseDir
	logBaseDir = tmpDir
	defer func() { logBaseDir = oldLogBaseDir }()

	// Initialize logging
	err := InitLogging("")
	if err != nil {
		t.Fatalf("InitLogging failed: %v", err)
	}
	defer CloseLogging()

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
		if strings.Contains(entry.Name(), "_argo_") && strings.HasSuffix(entry.Name(), ".log") {
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

	if !strings.Contains(logFile, "_argo_") {
		t.Errorf("Log file name doesn't contain _argo_ pattern: %s", logFile)
	}
}

func TestInfoAndWarnLogging(t *testing.T) {
	// Create temporary log directory
	tmpDir := t.TempDir()
	oldLogBaseDir := logBaseDir
	logBaseDir = tmpDir
	defer func() { logBaseDir = oldLogBaseDir }()

	// Initialize logging
	err := InitLogging("")
	if err != nil {
		t.Fatalf("InitLogging failed: %v", err)
	}
	defer CloseLogging()

	// Log some messages
	Infof("Test info message: %s", "hello")
	Warnf("Test warning message: %d", 42)

	// Force flush by closing and reading
	CloseLogging()

	// Find and read the log file
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	var logFilePath string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "_argo_") && strings.HasSuffix(entry.Name(), ".log") {
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

	// Check for INFO message
	if !strings.Contains(logContent, "[INFO] Test info message: hello") {
		t.Errorf("INFO message not found in log file")
	}

	// Check for WARN message
	if !strings.Contains(logContent, "[WARN] Test warning message: 42") {
		t.Errorf("WARN message not found in log file")
	}

	// Check timestamp format
	lines := strings.Split(strings.TrimSpace(logContent), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Each line should start with timestamp in format "2006-01-02 15:04:05"
		if len(line) < 19 {
			t.Errorf("Line too short to contain timestamp: %s", line)
			continue
		}
		timestamp := line[:19]
		_, err := time.Parse("2006-01-02 15:04:05", timestamp)
		if err != nil {
			t.Errorf("Invalid timestamp format in line: %s", line)
		}
	}
}

func TestProcessLogFile(t *testing.T) {
	// Create temporary log directory
	tmpDir := t.TempDir()
	oldLogBaseDir := logBaseDir
	logBaseDir = tmpDir
	defer func() { logBaseDir = oldLogBaseDir }()

	// Reset the global defaultLogger to simulate fresh process
	oldDefaultLogger := defaultLogger
	defaultLogger = nil
	defer func() { defaultLogger = oldDefaultLogger }()

	// Initialize logging
	err := InitLogging("")
	if err != nil {
		t.Fatalf("InitLogging failed: %v", err)
	}
	defer CloseLogging()

	// Log multiple messages
	for i := 0; i < 3; i++ {
		Infof("Message %d", i)
		time.Sleep(5 * time.Millisecond)
	}

	// Close to flush
	CloseLogging()

	// Check that one log file was created
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	logFiles := 0
	var logFilePath string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "_argo_") && strings.HasSuffix(entry.Name(), ".log") {
			logFiles++
			logFilePath = filepath.Join(tmpDir, entry.Name())
		}
	}

	if logFiles != 1 {
		t.Errorf("Expected exactly 1 log file, found %d", logFiles)
	}

	// Read the log file and verify all messages are there
	if logFilePath != "" {
		content, err := os.ReadFile(logFilePath)
		if err != nil {
			t.Fatalf("Failed to read log file: %v", err)
		}

		logContent := string(content)
		for i := 0; i < 3; i++ {
			expectedMsg := "[INFO] Message " + string(rune('0'+i))
			if !strings.Contains(logContent, expectedMsg) {
				t.Errorf("Missing message %d in log file", i)
			}
		}
	}
}
