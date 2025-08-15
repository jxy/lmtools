package logger

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// SetupTestLogger initializes a logger for testing with automatic cleanup
func SetupTestLogger(t *testing.T, opts ...Option) {
	t.Helper()

	// Reset logger to allow reinitialization
	ResetForTesting()

	// Create temp directory if needed
	tmpDir := t.TempDir()

	// Default options for testing
	defaultOpts := []Option{
		WithLogDir(tmpDir),
		WithLevel("debug"),
		WithFormat("text"),
		WithOutputMode(OutputBoth),
		WithComponent("test"),
	}

	// Append any custom options (they will override defaults)
	allOpts := append(defaultOpts, opts...)

	// Initialize logger
	if err := InitializeWithOptions(allOpts...); err != nil {
		t.Fatalf("Failed to initialize test logger: %v", err)
	}

	// Register cleanup
	t.Cleanup(func() {
		Close()
		ResetForTesting()
	})
}

func TestLoggerSuite(t *testing.T) {
	// Use the new test helper for automatic setup and cleanup
	SetupTestLogger(t)

	t.Run("Initialize", func(t *testing.T) {
		// Test that logger is initialized
		logger := GetLogger()
		if logger == nil {
			t.Fatal("Logger is nil after initialization")
		}
		if !logger.initialized {
			t.Error("Logger not marked as initialized")
		}
	})

	t.Run("InfoAndWarnLogging", func(t *testing.T) {
		// Capture stderr
		var buf bytes.Buffer
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		// Log messages
		Infof("Test info message: %s", "hello")
		Warnf("Test warning message: %d", 42)

		// Restore stderr and read output
		w.Close()
		os.Stderr = oldStderr
		_, _ = buf.ReadFrom(r)

		output := buf.String()
		if !strings.Contains(output, "Test info message: hello") {
			t.Errorf("Info message not found in output: %s", output)
		}
		if !strings.Contains(output, "Test warning message: 42") {
			t.Errorf("Warning message not found in output: %s", output)
		}
	})

	t.Run("LogJSON", func(t *testing.T) {
		// Test LogJSON function
		testData := []byte(`{"key1":"value1","key2":42,"key3":true}`)

		// Create a temporary directory for this test
		testDir := t.TempDir()

		err := LogJSON(testDir, "test-json", testData)
		if err != nil {
			t.Fatalf("LogJSON failed: %v", err)
		}

		// Verify file was created
		pattern := filepath.Join(testDir, "*_test-json_*.json")
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("Failed to glob for JSON files: %v", err)
		}
		if len(matches) == 0 {
			t.Error("No JSON file was created")
		}
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		// Test concurrent logging
		const numGoroutines = 100
		const numLogs = 10
		var wg sync.WaitGroup

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < numLogs; j++ {
					switch j % 3 {
					case 0:
						Infof("Concurrent info message from goroutine %d iteration %d", id, j)
					case 1:
						Warnf("Concurrent warning message from goroutine %d iteration %d", id, j)
					case 2:
						Errorf("Concurrent error message from goroutine %d iteration %d", id, j)
					}
				}
			}(i)
		}

		wg.Wait()
		// If we get here without deadlock or panic, concurrent access is working
	})
}

func TestLogJSON(t *testing.T) {
	tmpDir := t.TempDir()

	// Test with valid data
	payload := []byte(`{"test":"data","number":123}`)
	err := LogJSON(tmpDir, "test-prefix", payload)
	if err != nil {
		t.Fatalf("LogJSON failed: %v", err)
	}

	// Find the created file
	pattern := filepath.Join(tmpDir, "*_test-prefix_*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("Failed to glob for JSON files: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("Expected 1 JSON file, found %d", len(matches))
	}

	// Read and verify content
	data, err := os.ReadFile(matches[0])
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

	// Find the created JSON file
	pattern := filepath.Join(tmpDir, "*_test-perms_*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("Failed to glob for JSON files: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("Expected 1 JSON file, found %d", len(matches))
	}

	// Check JSON file permissions
	info, err = os.Stat(matches[0])
	if err != nil {
		t.Fatalf("Failed to stat JSON file: %v", err)
	}

	mode = info.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("Expected JSON file permissions 0600, got %04o", mode)
	}
}

func TestLogJSON_UsesExplicitDir(t *testing.T) {
	// Initialize logger with one directory
	tmp1 := t.TempDir()
	tmp2 := t.TempDir()

	ResetForTesting()
	err := InitializeWithOptions(
		WithLogDir(tmp1),
		WithLevel("info"),
		WithFormat("text"),
		WithOutputMode(OutputFileOnly),
		WithComponent("test"),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer Close()

	// Call LogJSON with a different directory
	payload := []byte(`{"test":"explicit_dir"}`)
	err = LogJSON(tmp2, "explicit_test", payload)
	if err != nil {
		t.Fatalf("LogJSON failed: %v", err)
	}

	// Verify file was created in tmp2, not tmp1
	files, err := os.ReadDir(tmp2)
	if err != nil {
		t.Fatalf("Failed to read directory: %v", err)
	}
	if len(files) == 0 {
		t.Error("Expected file in explicit directory, but found none")
	}

	// Verify no JSON files in tmp1
	files, err = os.ReadDir(tmp1)
	if err != nil {
		t.Fatalf("Failed to read directory: %v", err)
	}
	jsonCount := 0
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".json") {
			jsonCount++
		}
	}
	if jsonCount > 0 {
		t.Errorf("Expected no JSON files in logger's directory, but found %d", jsonCount)
	}
}

func TestLogJSON_DirectoryPermissions(t *testing.T) {
	// Use a subdirectory to test creation permissions
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir", "logs")

	ResetForTesting()
	err := InitializeWithOptions(
		WithLogDir(tmpDir),
		WithLevel("info"),
		WithFormat("text"),
		WithOutputMode(OutputFileOnly),
		WithComponent("test"),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer Close()

	// Call LogJSON to create the subdirectory
	payload := []byte(`{"test":"dir_perms"}`)
	err = LogJSON(subDir, "perms_test", payload)
	if err != nil {
		t.Fatalf("LogJSON failed: %v", err)
	}

	// Check directory permissions
	info, err := os.Stat(subDir)
	if err != nil {
		t.Fatalf("Failed to stat directory: %v", err)
	}

	mode := info.Mode().Perm()
	if mode != DirPerm {
		t.Errorf("Expected directory permissions %04o, got %04o", DirPerm, mode)
	}
}

func TestScopedLogger(t *testing.T) {
	// Initialize logger with a temp directory
	tmpDir := t.TempDir()
	ResetForTesting()
	err := InitializeWithOptions(
		WithLogDir(tmpDir),
		WithLevel("debug"),
		WithFormat("text"),
		WithOutputMode(OutputBoth),
		WithComponent("test"),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer Close()

	// Create a scoped logger
	logger := GetLogger()
	scope := logger.NewScope("test-operation")

	// Test request ID
	if scope.GetRequestID() != 1 {
		t.Errorf("Expected first request ID to be 1, got %d", scope.GetRequestID())
	}

	// Test duration tracking
	time.Sleep(10 * time.Millisecond)
	duration := scope.GetDuration()
	if duration < 10*time.Millisecond {
		t.Errorf("Expected duration to be at least 10ms, got %v", duration)
	}

	// Test logging with scope
	var buf bytes.Buffer
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	scope.Infof("Scoped log message")

	w.Close()
	os.Stderr = oldStderr
	_, _ = buf.ReadFrom(r)

	output := buf.String()
	if !strings.Contains(output, "Scoped log message") {
		t.Errorf("Scoped message not found in output: %s", output)
	}
	if !strings.Contains(output, "[#1]") {
		t.Errorf("Request ID not found in output: %s", output)
	}
}

func TestLogLevels(t *testing.T) {
	tests := []struct {
		name          string
		logLevel      string
		debugExpected bool
		infoExpected  bool
		warnExpected  bool
		errorExpected bool
	}{
		{"debug level", "debug", true, true, true, true},
		{"info level", "info", false, true, true, true},
		{"warn level", "warn", false, false, true, true},
		{"error level", "error", false, false, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Initialize logger with specific level
			ResetForTesting()
			err := InitializeWithOptions(
				WithLevel(tt.logLevel),
				WithFormat("text"),
				WithOutputMode(OutputStderrOnly),
			)
			if err != nil {
				t.Fatalf("Failed to initialize logger: %v", err)
			}
			defer Close()

			// Capture stderr
			var buf bytes.Buffer
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			// Log at all levels
			Debugf("Debug message")
			Infof("Info message")
			Warnf("Warn message")
			Errorf("Error message")

			// Restore stderr and read output
			w.Close()
			os.Stderr = oldStderr
			_, _ = buf.ReadFrom(r)

			output := buf.String()

			// Check expectations
			if tt.debugExpected && !strings.Contains(output, "Debug message") {
				t.Errorf("Expected debug message in output, but not found")
			}
			if !tt.debugExpected && strings.Contains(output, "Debug message") {
				t.Errorf("Did not expect debug message in output, but found it")
			}

			if tt.infoExpected && !strings.Contains(output, "Info message") {
				t.Errorf("Expected info message in output, but not found")
			}
			if !tt.infoExpected && strings.Contains(output, "Info message") {
				t.Errorf("Did not expect info message in output, but found it")
			}

			if tt.warnExpected && !strings.Contains(output, "Warn message") {
				t.Errorf("Expected warn message in output, but not found")
			}
			if !tt.warnExpected && strings.Contains(output, "Warn message") {
				t.Errorf("Did not expect warn message in output, but found it")
			}

			if tt.errorExpected && !strings.Contains(output, "Error message") {
				t.Errorf("Expected error message in output, but not found")
			}
			if !tt.errorExpected && strings.Contains(output, "Error message") {
				t.Errorf("Did not expect error message in output, but found it")
			}
		})
	}
}

func TestJSONFormat(t *testing.T) {
	// Initialize logger with JSON format
	ResetForTesting()
	err := InitializeWithOptions(
		WithLevel("info"),
		WithFormat("json"),
		WithOutputMode(OutputStderrOnly),
		WithComponent("test-component"),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer Close()

	// Capture stderr
	var buf bytes.Buffer
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// Log a message
	Infof("Test JSON message with %s", "formatting")

	// Restore stderr and read output
	w.Close()
	os.Stderr = oldStderr
	_, _ = buf.ReadFrom(r)

	output := strings.TrimSpace(buf.String())

	// Check that output is valid JSON
	if !strings.HasPrefix(output, "{") || !strings.HasSuffix(output, "}") {
		t.Errorf("Output does not appear to be JSON: %s", output)
	}

	// Check for expected fields
	expectedFields := []string{
		`"level":"INFO"`,
		`"message":"Test JSON message with formatting"`,
		`"component":"test-component"`,
		`"time":"`,
	}

	for _, field := range expectedFields {
		if !strings.Contains(output, field) {
			t.Errorf("Expected field %s not found in JSON output: %s", field, output)
		}
	}
}

func TestRequestCounter(t *testing.T) {
	// Initialize logger with request counter
	ResetForTesting()
	err := InitializeWithOptions(
		WithLevel("info"),
		WithFormat("text"),
		WithOutputMode(OutputStderrOnly),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer Close()

	logger := GetLogger()

	// Create multiple scopes
	scope1 := logger.NewScope("op1")
	scope2 := logger.NewScope("op2")
	scope3 := logger.NewScope("op3")

	// Check request IDs are sequential
	if scope1.GetRequestID() != 1 {
		t.Errorf("Expected first request ID to be 1, got %d", scope1.GetRequestID())
	}
	if scope2.GetRequestID() != 2 {
		t.Errorf("Expected second request ID to be 2, got %d", scope2.GetRequestID())
	}
	if scope3.GetRequestID() != 3 {
		t.Errorf("Expected third request ID to be 3, got %d", scope3.GetRequestID())
	}

	// Test counter reset
	ResetRequestCounter()
	scope4 := logger.NewScope("op4")
	if scope4.GetRequestID() != 1 {
		t.Errorf("Expected request ID after reset to be 1, got %d", scope4.GetRequestID())
	}
}

func TestConcurrentInitialization(t *testing.T) {
	// Test that concurrent initialization is safe
	const numGoroutines = 10
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	tmpDir := t.TempDir()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			// Try to initialize
			err := InitializeWithOptions(
				WithLogDir(tmpDir),
				WithLevel("info"),
				WithFormat("text"),
				WithComponent("test"),
			)
			if err != nil {
				errors <- err
			}

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

// TestExplicitLevelsRespected tests that explicit min levels are not overridden by auto-defaults
func TestExplicitLevelsRespected(t *testing.T) {
	// Test case 1: Explicit stderr min level should be respected with log dir
	t.Run("WithLogDir", func(t *testing.T) {
		SetupTestLogger(t,
			WithLevel("debug"),
			WithStderrMinLevel("error"), // Explicitly set to error
			WithFileMinLevel("warn"),    // Explicitly set to warn
		)

		// Check that the levels were respected
		logger := GetLogger()
		if logger.stderrMinLevel != LevelError {
			t.Errorf("Expected stderr min level to be ERROR, got %d", logger.stderrMinLevel)
		}
		if logger.fileMinLevel != LevelWarn {
			t.Errorf("Expected file min level to be WARN, got %d", logger.fileMinLevel)
		}
	})

	// Test case 2: Without log dir, stderr min level defaults to main level
	t.Run("WithoutLogDir", func(t *testing.T) {
		// Reset and initialize manually without using SetupTestLogger
		// to avoid the default temp directory creation
		ResetForTesting()
		err := InitializeWithOptions(
			WithLevel("warn"), // Set main level to warn
			WithOutputMode(OutputStderrOnly),
		)
		if err != nil {
			t.Fatalf("Failed to initialize logger: %v", err)
		}
		t.Cleanup(func() {
			Close()
			ResetForTesting()
		})

		logger := GetLogger()
		// Without log dir, stderr min level should match the main level
		if logger.stderrMinLevel != LevelWarn {
			t.Errorf("Expected stderr min level to be WARN (matching main level), got %d", logger.stderrMinLevel)
		}
	})
}
