package proxy

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequestLoggerCreation(t *testing.T) {
	// Reset counter for consistent testing
	ResetCounter()

	// Test serial number increments
	logger1 := NewRequestScopedLogger()
	if logger1.GetRequestID() != 1 {
		t.Errorf("Expected first request ID to be 1, got %d", logger1.GetRequestID())
	}

	logger2 := NewRequestScopedLogger()
	if logger2.GetRequestID() != 2 {
		t.Errorf("Expected second request ID to be 2, got %d", logger2.GetRequestID())
	}

	logger3 := NewRequestScopedLogger()
	if logger3.GetRequestID() != 3 {
		t.Errorf("Expected third request ID to be 3, got %d", logger3.GetRequestID())
	}

	// Test timestamp is set correctly
	if logger1.GetStartTime().IsZero() {
		t.Error("Expected start time to be set, but it was zero")
	}

	// Test that start time is reasonable (within last second)
	if time.Since(logger1.GetStartTime()) > time.Second {
		t.Error("Start time seems too old")
	}
}

func TestRequestLoggerFormatting(t *testing.T) {
	// Reset counter for consistent testing
	ResetCounter()

	logger := NewRequestScopedLogger()

	// Test log format includes request ID and timestamp
	formatted := logger.formatMessage("Test message")

	// Should contain request ID
	if !strings.Contains(formatted, "[#1") {
		t.Errorf("Expected formatted message to contain request ID [#1, got: %s", formatted)
	}

	// Timestamp is now added by the core logger, not in formatMessage
	// So we should NOT have a timestamp in the formatted message itself

	// Should contain the original message
	if !strings.Contains(formatted, "Test message") {
		t.Errorf("Expected formatted message to contain 'Test message', got: %s", formatted)
	}

	// Test format with arguments
	formatted2 := logger.formatMessage("Test %s %d", "string", 42)
	if !strings.Contains(formatted2, "Test string 42") {
		t.Errorf("Expected formatted message to contain 'Test string 42', got: %s", formatted2)
	}
}

func TestRequestLoggerDuration(t *testing.T) {
	logger := NewRequestScopedLogger()

	// Sleep for a measurable duration
	time.Sleep(100 * time.Millisecond)

	duration := logger.GetDuration()
	if duration < 100*time.Millisecond {
		t.Errorf("Expected duration to be at least 100ms, got %v", duration)
	}

	if duration > 200*time.Millisecond {
		t.Errorf("Expected duration to be less than 200ms, got %v", duration)
	}
}

func TestConcurrentRequestIDs(t *testing.T) {
	// Reset counter for consistent testing
	ResetCounter()

	const numGoroutines = 100
	ids := make([]int64, numGoroutines)
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Create loggers concurrently
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			logger := NewRequestScopedLogger()
			ids[idx] = logger.GetRequestID()
		}(i)
	}

	wg.Wait()

	// Check that all IDs are unique
	seen := make(map[int64]bool)
	for i, id := range ids {
		if seen[id] {
			t.Errorf("Duplicate request ID found: %d at index %d", id, i)
		}
		seen[id] = true

		// IDs should be between 1 and numGoroutines
		if id < 1 || id > int64(numGoroutines) {
			t.Errorf("Request ID %d out of expected range [1, %d]", id, numGoroutines)
		}
	}

	// Should have exactly numGoroutines unique IDs
	if len(seen) != numGoroutines {
		t.Errorf("Expected %d unique IDs, got %d", numGoroutines, len(seen))
	}
}

func TestRequestLoggerContext(t *testing.T) {
	// Test adding logger to context
	ctx := context.Background()
	logger := NewRequestScopedLogger()

	ctxWithLogger := WithRequestLogger(ctx, logger)

	// Test retrieving logger from context
	retrieved := GetRequestLogger(ctxWithLogger)
	if retrieved.GetRequestID() != logger.GetRequestID() {
		t.Errorf("Expected retrieved logger to have same ID %d, got %d",
			logger.GetRequestID(), retrieved.GetRequestID())
	}

	// Test retrieving from context without logger (should create new one)
	emptyCtx := context.Background()
	defaultLogger := GetRequestLogger(emptyCtx)
	if defaultLogger.GetRequestID() == 0 {
		t.Error("Expected default logger to have non-zero ID")
	}
}

func TestLogDurationFormatting(t *testing.T) {
	// Test millisecond formatting
	logger := NewRequestScopedLogger()
	time.Sleep(50 * time.Millisecond)

	// Since we can't easily capture the log output in this test,
	// we'll just ensure the method doesn't panic
	logger.LogDuration("Test operation completed")

	// Test second formatting by manipulating start time
	logger2 := NewRequestScopedLogger()
	// Manually set start time to 2 seconds ago
	logger2.startTime = time.Now().Add(-2 * time.Second)
	logger2.LogDuration("Long operation completed")
}

func TestLogRequestFormatting(t *testing.T) {
	logger := NewRequestScopedLogger()

	// Test non-streaming request
	logger.LogRequest("POST", "/v1/messages", "claude-3-opus", "gpto3", "openai",
		3, 2, 200, false)

	// Test streaming request
	logger.LogRequest("POST", "/v1/messages", "claude-3-opus", "gpto3", "openai",
		3, 2, 200, true)

	// Sleep to test duration formatting
	time.Sleep(1100 * time.Millisecond)

	// Test with duration > 1 second
	logger.LogRequest("POST", "/v1/messages", "claude-3-opus", "gpto3", "openai",
		3, 2, 200, false)
}

func BenchmarkRequestLoggerCreation(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewRequestScopedLogger()
	}
}

func BenchmarkRequestLoggerFormatting(b *testing.B) {
	logger := NewRequestScopedLogger()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = logger.formatMessage("Test message with %s and %d", "string", 42)
	}
}

func BenchmarkConcurrentLoggerCreation(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = NewRequestScopedLogger()
		}
	})
}
