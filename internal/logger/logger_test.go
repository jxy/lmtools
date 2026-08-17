package logger

import (
	"testing"
)

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
		})
	}
}

func TestLogger_LevelChecksHonorGlobalLevelWithFileSink(t *testing.T) {
	tests := []struct {
		level string
		debug bool
		info  bool
		warn  bool
	}{
		{level: "debug", debug: true, info: true, warn: true},
		{level: "info", debug: false, info: true, warn: true},
		{level: "warn", debug: false, info: false, warn: true},
		{level: "error", debug: false, info: false, warn: false},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			ResetForTesting()
			t.Cleanup(ResetForTesting)
			if err := InitializeWithOptions(
				WithLogDir(t.TempDir()),
				WithLevel(tt.level),
				WithStderr(false),
				WithFile(true),
			); err != nil {
				t.Fatalf("InitializeWithOptions() error = %v", err)
			}

			log := GetLogger()
			if got := log.IsDebugEnabled(); got != tt.debug {
				t.Errorf("IsDebugEnabled() = %t, want %t", got, tt.debug)
			}
			if got := log.IsInfoEnabled(); got != tt.info {
				t.Errorf("IsInfoEnabled() = %t, want %t", got, tt.info)
			}
			if got := log.IsWarnEnabled(); got != tt.warn {
				t.Errorf("IsWarnEnabled() = %t, want %t", got, tt.warn)
			}
		})
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
	// Log something - mainly testing it doesn't panic
	logger.Infof("Test message")

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
	logger.Infof("Test message")
}

func TestLogger_NilSafety(t *testing.T) {
	// Test that methods handle nil logger gracefully
	var logger *Logger

	// These should not panic
	if logger.IsDebugEnabled() {
		t.Error("Nil logger should return false for IsDebugEnabled")
	}
	if logger.IsWarnEnabled() {
		t.Error("Nil logger should return false for IsWarnEnabled")
	}
}
