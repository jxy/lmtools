package logger

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestScopedLogger_RequestIDMonotonicity(t *testing.T) {
	// Reset and initialize logger
	ResetForTesting()
	err := InitializeWithOptions(
		WithLevel("debug"),
		WithStderr(false),
		WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	logger := GetLogger()

	// Reset counter to start from 0
	ResetRequestCounter()

	// Create multiple scopes
	scope1 := logger.NewScope("test1")
	scope2 := logger.NewScope("test2")
	scope3 := logger.NewScope("test3")

	// Request IDs should be monotonically increasing
	if scope1.GetRequestID() != 1 {
		t.Errorf("Expected ID 1, got %d", scope1.GetRequestID())
	}
	if scope2.GetRequestID() != 2 {
		t.Errorf("Expected ID 2, got %d", scope2.GetRequestID())
	}
	if scope3.GetRequestID() != 3 {
		t.Errorf("Expected ID 3, got %d", scope3.GetRequestID())
	}

	// Create more scopes to verify continued monotonicity
	scope4 := logger.NewScope("test4")
	scope5 := logger.NewScope("test5")

	if scope4.GetRequestID() != 4 {
		t.Errorf("Expected ID 4, got %d", scope4.GetRequestID())
	}
	if scope5.GetRequestID() != 5 {
		t.Errorf("Expected ID 5, got %d", scope5.GetRequestID())
	}

	// Each scope should maintain its ID
	if scope1.GetRequestID() != 1 {
		t.Errorf("Scope1 ID changed: expected 1, got %d", scope1.GetRequestID())
	}
	if scope5.GetRequestID() != 5 {
		t.Errorf("Scope5 ID changed: expected 5, got %d", scope5.GetRequestID())
	}
}

func TestScopedLogger_Duration(t *testing.T) {
	// Reset and initialize logger
	ResetForTesting()
	err := InitializeWithOptions(
		WithLevel("debug"),
		WithStderr(false),
		WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	logger := GetLogger()
	scope := logger.NewScope("duration-test")

	// Get start time
	startTime := scope.GetStartTime()
	if startTime.IsZero() {
		t.Error("Start time should not be zero")
	}

	// Sleep for a measurable duration
	time.Sleep(100 * time.Millisecond)

	// Get duration
	duration := scope.GetDuration()
	if duration < 100*time.Millisecond {
		t.Errorf("Duration should be at least 100ms, got %v", duration)
	}
	if duration > 200*time.Millisecond {
		t.Errorf("Duration should be less than 200ms, got %v", duration)
	}

	// Sleep more
	time.Sleep(100 * time.Millisecond)

	// Duration should increase
	duration2 := scope.GetDuration()
	if duration2 < 200*time.Millisecond {
		t.Errorf("Duration should be at least 200ms, got %v", duration2)
	}
	if duration2 <= duration {
		t.Error("Duration should increase over time")
	}
}

func TestScopedLogger_Done(t *testing.T) {
	// This test verifies Done() doesn't panic
	ResetForTesting()
	err := InitializeWithOptions(
		WithLevel("info"),
		WithStderr(false),
		WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	logger := GetLogger()
	scope := logger.NewScope("done-test")

	// Sleep to ensure non-zero duration
	time.Sleep(10 * time.Millisecond)

	// Call Done - should log "done in Xms" at INFO level
	scope.Done()

	// No panic means success for this basic test
}

func TestScopedLogger_InfoJSON(t *testing.T) {
	// Reset and initialize logger
	ResetForTesting()
	err := InitializeWithOptions(
		WithLevel("info"),
		WithStderr(false),
		WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	logger := GetLogger()
	scope := logger.NewScope("json-test")

	tests := []struct {
		name  string
		label string
		data  interface{}
	}{
		{
			name:  "simple map",
			label: "Test data",
			data: map[string]interface{}{
				"key1": "value1",
				"key2": 123,
			},
		},
		{
			name:  "nested structure",
			label: "Nested",
			data: map[string]interface{}{
				"outer": map[string]interface{}{
					"inner": "value",
				},
			},
		},
		{
			name:  "array",
			label: "Array",
			data:  []int{1, 2, 3},
		},
		{
			name:  "string",
			label: "String",
			data:  "simple string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify the function doesn't panic and handles the data
			scope.InfoJSON(tt.label, tt.data)

			// Verify JSON marshaling works
			b, err := json.Marshal(tt.data)
			if err != nil {
				t.Errorf("Failed to marshal data: %v", err)
			}
			if len(b) == 0 {
				t.Error("Marshaled data should not be empty")
			}
		})
	}
}

func TestScopedLogger_InfoJSON_MarshalError(t *testing.T) {
	// Reset and initialize logger
	ResetForTesting()
	err := InitializeWithOptions(
		WithLevel("info"),
		WithStderr(false),
		WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	logger := GetLogger()
	scope := logger.NewScope("error-test")

	// Create a type that can't be marshaled to JSON
	type circularRef struct {
		Self *circularRef
	}
	circular := &circularRef{}
	circular.Self = circular

	// Should handle marshal error gracefully
	scope.InfoJSON("Circular", circular)
	// Should log: "Circular: <marshal error>"
	// Test passes if no panic
}

func TestScopedLogger_DebugJSON(t *testing.T) {
	// Reset and initialize logger
	ResetForTesting()
	err := InitializeWithOptions(
		WithLevel("debug"),
		WithStderr(false),
		WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	logger := GetLogger()
	scope := logger.NewScope("debug-json-test")

	// Test various data types
	scope.DebugJSON("String", "test")
	scope.DebugJSON("Number", 42)
	scope.DebugJSON("Map", map[string]int{"a": 1, "b": 2})
	scope.DebugJSON("Slice", []string{"x", "y", "z"})

	// No panic means success
}

func TestLogger_ContextIntegration(t *testing.T) {
	// Reset and initialize logger
	ResetForTesting()
	err := InitializeWithOptions(
		WithLevel("debug"),
		WithStderr(false),
		WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create a scoped logger
	logger := GetLogger()
	scope := logger.NewScope("context-test")

	// Store in context
	ctx := WithContext(context.Background(), scope)

	// Retrieve from context
	retrieved := From(ctx)
	if retrieved != scope {
		t.Error("Retrieved logger should be the same as stored")
	}
	if retrieved.GetRequestID() != scope.GetRequestID() {
		t.Errorf("Request IDs should match: got %d, want %d",
			retrieved.GetRequestID(), scope.GetRequestID())
	}

	// From empty context should return a default logger
	emptyCtx := context.Background()
	defaultLogger := From(emptyCtx)
	if defaultLogger == nil {
		t.Fatal("From() should never return nil")
	}
	if defaultLogger.GetRequestID() == scope.GetRequestID() {
		t.Error("Default logger should have different request ID")
	}
}

func TestLogger_IsDebugEnabled(t *testing.T) {
	tests := []struct {
		name     string
		level    string
		expected bool
	}{
		{"debug level", "debug", true},
		{"info level", "info", false},
		{"warn level", "warn", false},
		{"error level", "error", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetForTesting()
			err := InitializeWithOptions(
				WithLevel(tt.level),
				WithStderr(true),
				WithFile(false),
			)
			if err != nil {
				t.Fatalf("Failed to initialize logger: %v", err)
			}

			logger := GetLogger()
			if logger.IsDebugEnabled() != tt.expected {
				t.Errorf("IsDebugEnabled() = %v, want %v", logger.IsDebugEnabled(), tt.expected)
			}

			scope := logger.NewScope("test")
			if scope.IsDebugEnabled() != tt.expected {
				t.Errorf("Scope IsDebugEnabled() = %v, want %v", scope.IsDebugEnabled(), tt.expected)
			}
		})
	}
}

func TestInfoJSON_TruncatedData(t *testing.T) {
	// This test verifies that InfoJSON logs the full JSON structure
	// when given pre-truncated data

	ResetForTesting()
	err := InitializeWithOptions(
		WithLevel("info"),
		WithStderr(false),
		WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	logger := GetLogger()
	scope := logger.NewScope("truncate-test")

	// Simulate pre-truncated data (like what truncateValue would produce)
	truncatedData := map[string]interface{}{
		"tool": "calculator",
		"input": map[string]interface{}{
			"expression":  "2 + 2",
			"description": strings.Repeat("a", 61) + "...", // Truncated string
		},
	}

	// InfoJSON should log this as valid JSON
	scope.InfoJSON("Tool call", truncatedData)

	// Verify the data can be marshaled to valid JSON
	jsonBytes, err := json.Marshal(truncatedData)
	if err != nil {
		t.Errorf("Failed to marshal truncated data: %v", err)
	}

	var parsed map[string]interface{}
	err = json.Unmarshal(jsonBytes, &parsed)
	if err != nil {
		t.Errorf("Failed to unmarshal JSON: %v", err)
	}

	// Verify structure is preserved
	if parsed["tool"] != "calculator" {
		t.Errorf("Expected tool='calculator', got %v", parsed["tool"])
	}
	inputMap := parsed["input"].(map[string]interface{})
	if inputMap["expression"] != "2 + 2" {
		t.Errorf("Expected expression='2 + 2', got %v", inputMap["expression"])
	}
	desc := inputMap["description"].(string)
	if !strings.HasSuffix(desc, "...") {
		t.Error("Description should end with ...")
	}
}

func TestLogger_ThreadSafety(t *testing.T) {
	// Test that multiple goroutines can safely create scopes
	ResetForTesting()
	err := InitializeWithOptions(
		WithLevel("debug"),
		WithStderr(false),
		WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	logger := GetLogger()
	ResetRequestCounter()

	const numGoroutines = 100
	done := make(chan int64, numGoroutines)

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			scope := logger.NewScope("concurrent")
			done <- scope.GetRequestID()
		}()
	}

	wg.Wait()
	close(done)

	// Collect all IDs
	ids := make(map[int64]bool)
	for id := range done {
		if ids[id] {
			t.Errorf("Duplicate ID found: %d", id)
		}
		ids[id] = true
	}

	// All IDs should be unique
	if len(ids) != numGoroutines {
		t.Errorf("Expected %d unique IDs, got %d", numGoroutines, len(ids))
	}

	// IDs should be in range 1 to numGoroutines
	for id := range ids {
		if id < 1 || id > int64(numGoroutines) {
			t.Errorf("ID %d out of expected range [1, %d]", id, numGoroutines)
		}
	}
}

func TestLogger_Initialization(t *testing.T) {
	// Test various initialization options
	ResetForTesting()

	// Test with different levels
	levels := []string{"debug", "info", "warn", "error"}
	for _, level := range levels {
		ResetForTesting()
		err := InitializeWithOptions(
			WithLevel(level),
			WithStderr(false),
			WithFile(false),
		)
		if err != nil {
			t.Errorf("Failed to initialize with level %s: %v", level, err)
		}

		logger := GetLogger()
		if logger == nil {
			t.Errorf("GetLogger() returned nil after initialization with level %s", level)
		}
	}
}

func TestLogger_WithFormat(t *testing.T) {
	// Test JSON format
	ResetForTesting()
	err := InitializeWithOptions(
		WithLevel("info"),
		WithFormat("json"),
		WithStderr(false),
		WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize with JSON format: %v", err)
	}

	logger := GetLogger()
	scope := logger.NewScope("json-format")

	// Log something - mainly testing it doesn't panic
	scope.Infof("Test message")
	scope.InfoJSON("Test JSON", map[string]string{"key": "value"})

	// Test text format
	ResetForTesting()
	err = InitializeWithOptions(
		WithLevel("info"),
		WithFormat("text"),
		WithStderr(false),
		WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize with text format: %v", err)
	}

	logger = GetLogger()
	scope = logger.NewScope("text-format")
	scope.Infof("Test message")
}

func TestLogger_NilSafety(t *testing.T) {
	// Test that methods handle nil logger gracefully
	var logger *Logger
	var scope *ScopedLogger

	// These should not panic
	if logger.IsDebugEnabled() {
		t.Error("Nil logger should return false for IsDebugEnabled")
	}

	if scope.IsDebugEnabled() {
		t.Error("Nil scope should return false for IsDebugEnabled")
	}

	// Create a scope with nil parent
	scope = &ScopedLogger{parent: nil}
	if scope.IsDebugEnabled() {
		t.Error("Scope with nil parent should return false for IsDebugEnabled")
	}
}
