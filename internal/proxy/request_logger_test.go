package proxy

import (
	"bytes"
	"context"
	"lmtools/internal/logger"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func init() {
	// Initialize logger with request counter enabled for tests
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
		logger.WithComponent("test"),
	)
}

func TestRequestLoggerCreation(t *testing.T) {
	// Test serial number increments
	logger1 := NewRequestScopedLogger()
	id1 := logger1.GetRequestID()

	logger2 := NewRequestScopedLogger()
	id2 := logger2.GetRequestID()

	logger3 := NewRequestScopedLogger()
	id3 := logger3.GetRequestID()

	// Check that IDs are incrementing
	if id2 != id1+1 {
		t.Errorf("Expected second request ID to be %d, got %d", id1+1, id2)
	}

	if id3 != id2+1 {
		t.Errorf("Expected third request ID to be %d, got %d", id2+1, id3)
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
	// Capture the actual logged output
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// Reinitialize logger to use the new stderr
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
		logger.WithComponent("test"),
	)

	// Create request logger after reinitializing
	reqLogger := NewRequestScopedLogger()

	// Log a test message
	reqLogger.Infof("Test message")

	// Restore stderr and read captured output
	w.Close()
	os.Stderr = oldStderr

	// Restore logger to use original stderr
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
		logger.WithComponent("test"),
	)
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("Failed to read from pipe: %v", err)
	}
	output := buf.String()

	// Should contain request ID in the format [#N]
	if !strings.Contains(output, "[#") || !strings.Contains(output, "Test message") {
		t.Errorf("Expected output to contain request ID [#N] and message, got: %s", output)
	}

	// Test format with arguments
	r2, w2, _ := os.Pipe()
	os.Stderr = w2

	// Reinitialize logger again for second test
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
		logger.WithComponent("test"),
	)

	// Create new request logger
	reqLogger2 := NewRequestScopedLogger()
	reqLogger2.Infof("Test %s %d", "string", 42)
	w2.Close()
	os.Stderr = oldStderr

	// Final restore
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
		logger.WithComponent("test"),
	)
	var buf2 bytes.Buffer
	if _, err := buf2.ReadFrom(r2); err != nil {
		t.Fatalf("Failed to read from pipe: %v", err)
	}
	output2 := buf2.String()

	if !strings.Contains(output2, "Test string 42") {
		t.Errorf("Expected output to contain 'Test string 42', got: %s", output2)
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

	// Test retrieving from context without logger (should return nil)
	emptyCtx := context.Background()
	nilLogger := GetRequestLogger(emptyCtx)
	if nilLogger != nil {
		t.Error("Expected nil logger from empty context")
	}

	// Test GetRequestLoggerOrDefault creates new logger
	defaultLogger := GetRequestLoggerOrDefault(emptyCtx)
	if defaultLogger == nil {
		t.Fatal("Expected GetRequestLoggerOrDefault to return non-nil logger")
	}
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

	// Test second formatting by waiting
	logger2 := NewRequestScopedLogger()
	// Wait a bit to test second formatting
	time.Sleep(1100 * time.Millisecond)
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

func BenchmarkConcurrentLoggerCreation(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = NewRequestScopedLogger()
		}
	})
}
