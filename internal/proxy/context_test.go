package proxy

import (
	"context"
	"lmtools/internal/logger"
	"testing"
)

func TestWithRequestLogger(t *testing.T) {
	// Initialize logger
	logger.ResetForTesting()
	err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create a scoped logger
	scope := logger.GetLogger().NewScope("test")

	// Add it to context
	ctx := WithRequestLogger(context.Background(), scope)

	// Retrieve it
	retrieved := GetRequestLogger(ctx)

	// Should be the same instance
	if retrieved != scope {
		t.Error("Retrieved logger should be the same as the one stored")
	}
	if retrieved.GetRequestID() != scope.GetRequestID() {
		t.Errorf("Request IDs should match: got %d, want %d",
			retrieved.GetRequestID(), scope.GetRequestID())
	}
}

func TestGetRequestLogger(t *testing.T) {
	// Initialize logger
	logger.ResetForTesting()
	err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	tests := []struct {
		name           string
		setup          func() context.Context
		expectNil      bool
		expectPositive bool // expect positive request ID
	}{
		{
			name: "with logger in context",
			setup: func() context.Context {
				scope := logger.GetLogger().NewScope("test")
				return WithRequestLogger(context.Background(), scope)
			},
			expectNil:      false,
			expectPositive: true,
		},
		{
			name: "without logger in context",
			setup: func() context.Context {
				return context.Background()
			},
			expectNil:      true,
			expectPositive: false,
		},
		{
			name: "with nil logger in context",
			setup: func() context.Context {
				return WithRequestLogger(context.Background(), nil)
			},
			expectNil:      true,
			expectPositive: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setup()
			result := GetRequestLogger(ctx)

			if tt.expectNil {
				if result != nil {
					t.Error("Expected nil logger")
				}
			} else {
				if result == nil {
					t.Error("Expected non-nil logger")
				} else if tt.expectPositive && result.GetRequestID() <= 0 {
					t.Errorf("Expected positive request ID, got %d", result.GetRequestID())
				}
			}
		})
	}
}

func TestGetRequestLoggerOrDefault(t *testing.T) {
	// Initialize logger
	logger.ResetForTesting()
	err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	tests := []struct {
		name  string
		setup func() context.Context
	}{
		{
			name: "returns existing logger from context",
			setup: func() context.Context {
				scope := logger.GetLogger().NewScope("existing")
				return WithRequestLogger(context.Background(), scope)
			},
		},
		{
			name: "creates default logger when none in context",
			setup: func() context.Context {
				return context.Background()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setup()
			result := GetRequestLoggerOrDefault(ctx)

			if result == nil {
				t.Fatal("GetRequestLoggerOrDefault should never return nil")
			}

			// Should have a valid request ID
			if result.GetRequestID() <= 0 {
				t.Errorf("Expected positive request ID, got %d", result.GetRequestID())
			}
		})
	}
}

func TestContextRoundTrip(t *testing.T) {
	// Initialize logger
	logger.ResetForTesting()
	err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Test that WithRequestLogger and GetRequestLogger properly round-trip
	scope1 := logger.GetLogger().NewScope("scope1")
	scope2 := logger.GetLogger().NewScope("scope2")

	ctx := context.Background()

	// Add first scope
	ctx = WithRequestLogger(ctx, scope1)
	retrieved1 := GetRequestLogger(ctx)
	if retrieved1 != scope1 {
		t.Error("First retrieval should return scope1")
	}

	// Override with second scope
	ctx = WithRequestLogger(ctx, scope2)
	retrieved2 := GetRequestLogger(ctx)
	if retrieved2 != scope2 {
		t.Error("Second retrieval should return scope2")
	}
	if retrieved1 == retrieved2 {
		t.Error("The two scopes should be different")
	}
}

func TestLoggerBridging(t *testing.T) {
	// This test verifies that the proxy context functions properly bridge
	// to the logger package's context functions

	logger.ResetForTesting()
	err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	scope := logger.GetLogger().NewScope("bridge-test")

	// Use proxy's WithRequestLogger
	ctx := WithRequestLogger(context.Background(), scope)

	// Verify logger.From can retrieve it (since WithRequestLogger uses logger.WithContext)
	fromLogger := logger.From(ctx)
	if fromLogger == nil {
		t.Fatal("logger.From should return a logger")
	}
	if fromLogger.GetRequestID() != scope.GetRequestID() {
		t.Errorf("Request IDs should match: got %d, want %d",
			fromLogger.GetRequestID(), scope.GetRequestID())
	}

	// Verify GetRequestLogger can retrieve it
	fromProxy := GetRequestLogger(ctx)
	if fromProxy != scope {
		t.Error("GetRequestLogger should return the original scope")
	}

	// Verify they're the same instance
	if fromLogger != fromProxy {
		t.Error("Both methods should return the same instance")
	}
}

func TestDefaultLoggerCreation(t *testing.T) {
	// Test that GetRequestLoggerOrDefault creates a valid default logger
	logger.ResetForTesting()
	err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Empty context
	ctx := context.Background()

	// Get default logger
	defaultLogger := GetRequestLoggerOrDefault(ctx)
	if defaultLogger == nil {
		t.Fatal("GetRequestLoggerOrDefault should not return nil")
	}

	// Should have a valid request ID
	id1 := defaultLogger.GetRequestID()
	if id1 <= 0 {
		t.Errorf("Expected positive request ID, got %d", id1)
	}

	// Getting again should return a different logger (new default each time)
	defaultLogger2 := GetRequestLoggerOrDefault(ctx)
	id2 := defaultLogger2.GetRequestID()
	if id1 == id2 {
		t.Error("Each call should create a new default logger with different ID")
	}
}

func TestNilHandling(t *testing.T) {
	// Test that nil loggers are handled gracefully
	logger.ResetForTesting()
	err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Store nil logger
	ctx := WithRequestLogger(context.Background(), nil)

	// GetRequestLogger should return nil
	retrieved := GetRequestLogger(ctx)
	if retrieved != nil {
		t.Error("GetRequestLogger should return nil when nil was stored")
	}

	// GetRequestLoggerOrDefault should return a valid logger
	defaultLogger := GetRequestLoggerOrDefault(ctx)
	if defaultLogger == nil {
		t.Fatal("GetRequestLoggerOrDefault should never return nil")
	}
	if defaultLogger.GetRequestID() <= 0 {
		t.Error("Default logger should have positive request ID")
	}
}
