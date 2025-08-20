package proxy

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestLogToolCall(t *testing.T) {
	tests := []struct {
		name          string
		toolName      string
		toolData      interface{}
		wantInfoLog   string
		wantDebugLog  string
		wantInfoPipe  bool
		debugShouldBe string // "input_only" or "full_block"
	}{
		{
			name:     "Simple input data (backward compatibility)",
			toolName: "search_tool",
			toolData: map[string]interface{}{
				"query": "test query",
				"limit": 10,
			},
			wantInfoLog:   `Tool call: search_tool | Data: {"limit":10,"query":"test query"}`,
			wantDebugLog:  `Tool call: {"limit":10,"query":"test query"}`,
			wantInfoPipe:  true,
			debugShouldBe: "input_only",
		},
		{
			name:     "Full AnthropicContentBlock",
			toolName: "web_search",
			toolData: AnthropicContentBlock{
				Type: "tool_use",
				ID:   "toolu_123456",
				Name: "web_search",
				Input: map[string]interface{}{
					"query": "AI news",
					"filters": map[string]interface{}{
						"date": "recent",
					},
				},
			},
			wantInfoLog:   `Tool call: web_search | Data: {"filters":{"date":"recent"},"query":"AI news"}`,
			wantDebugLog:  `Tool call: {"type":"tool_use","id":"toolu_123456","name":"web_search","input":{"filters":{"date":"recent"},"query":"AI news"}}`,
			wantInfoPipe:  true,
			debugShouldBe: "full_block",
		},
		{
			name:     "Large input data gets truncated at INFO level",
			toolName: "process_data",
			toolData: map[string]interface{}{
				"data": strings.Repeat("x", 100), // Long string that should be truncated
				"mode": "fast",
			},
			wantInfoLog:   `Tool call: process_data | Data: {"data":"` + strings.Repeat("x", 64) + `...","mode":"fast"}`,
			wantDebugLog:  `Tool call: {"data":"` + strings.Repeat("x", 100) + `","mode":"fast"}`,
			wantInfoPipe:  true,
			debugShouldBe: "input_only",
		},
		{
			name:     "Nested data structure",
			toolName: "complex_tool",
			toolData: map[string]interface{}{
				"level1": map[string]interface{}{
					"level2": map[string]interface{}{
						"deep_value": "test",
						"array":      []interface{}{1, 2, 3},
					},
				},
			},
			wantInfoLog:   `Tool call: complex_tool | Data: {"level1":{"level2":{"array":[1,2,3],"deep_value":"test"}}}`,
			wantDebugLog:  `Tool call: {"level1":{"level2":{"array":[1,2,3],"deep_value":"test"}}}`,
			wantInfoPipe:  true,
			debugShouldBe: "input_only",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stderr output
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			// Reinitialize logger
			logger.ResetForTesting()
			_ = logger.InitializeWithOptions(
				logger.WithLevel("debug"),
				logger.WithFormat("text"),
				logger.WithOutputMode(logger.OutputStderrOnly),
				logger.WithComponent("test"),
			)

			// Create request logger and log tool call
			reqLogger := NewRequestScopedLogger()
			reqLogger.LogToolCall(tt.toolName, tt.toolData)

			// Restore stderr and read output
			w.Close()
			os.Stderr = oldStderr

			var buf bytes.Buffer
			if _, err := buf.ReadFrom(r); err != nil {
				t.Fatalf("Failed to read from pipe: %v", err)
			}
			output := buf.String()

			// Split into lines
			lines := strings.Split(strings.TrimSpace(output), "\n")
			if len(lines) != 2 {
				t.Fatalf("Expected 2 log lines (INFO and DEBUG), got %d: %v", len(lines), lines)
			}

			// Check INFO log
			infoLine := lines[0]
			if !strings.Contains(infoLine, "[INFO]") {
				t.Errorf("First line should be INFO level, got: %s", infoLine)
			}
			if tt.wantInfoPipe && !strings.Contains(infoLine, " | Data: ") {
				t.Errorf("INFO log should contain pipe separator, got: %s", infoLine)
			}
			if !strings.Contains(infoLine, tt.toolName) {
				t.Errorf("INFO log should contain tool name %s, got: %s", tt.toolName, infoLine)
			}

			// Check DEBUG log
			debugLine := lines[1]
			if !strings.Contains(debugLine, "[DEBUG]") {
				t.Errorf("Second line should be DEBUG level, got: %s", debugLine)
			}

			// Extract JSON from debug line
			debugPrefix := "Tool call: "
			idx := strings.Index(debugLine, debugPrefix)
			if idx == -1 {
				t.Fatalf("DEBUG log should contain 'Tool call: ', got: %s", debugLine)
			}
			jsonStr := debugLine[idx+len(debugPrefix):]

			// Parse and verify JSON structure
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
				t.Errorf("Failed to parse DEBUG log JSON: %v, json: %s", err, jsonStr)
			}

			// Check structure based on expected type
			switch tt.debugShouldBe {
			case "input_only":
				// Should not have type, id, name at top level
				if _, hasType := parsed["type"]; hasType {
					t.Errorf("DEBUG log should not have 'type' field for input-only data, got: %v", parsed)
				}
				if _, hasID := parsed["id"]; hasID {
					t.Errorf("DEBUG log should not have 'id' field for input-only data, got: %v", parsed)
				}
			case "full_block":
				// Should have type, id, name, input fields
				if parsed["type"] != "tool_use" {
					t.Errorf("DEBUG log should have type='tool_use', got: %v", parsed["type"])
				}
				if _, hasID := parsed["id"]; !hasID {
					t.Errorf("DEBUG log should have 'id' field for full block, got: %v", parsed)
				}
				if parsed["name"] != tt.toolName {
					t.Errorf("DEBUG log should have name='%s', got: %v", tt.toolName, parsed["name"])
				}
				if _, hasInput := parsed["input"]; !hasInput {
					t.Errorf("DEBUG log should have 'input' field for full block, got: %v", parsed)
				}
			}
		})
	}

	// Restore logger
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
		logger.WithComponent("test"),
	)
}

func TestLogToolCallWithEmptyBlock(t *testing.T) {
	// Test with an empty AnthropicContentBlock
	block := AnthropicContentBlock{
		Type:  "tool_use",
		ID:    "toolu_empty",
		Name:  "empty_tool",
		Input: nil, // nil input
	}

	// Capture stderr output
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// Reinitialize logger
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
		logger.WithComponent("test"),
	)

	// Create request logger and log tool call
	reqLogger := NewRequestScopedLogger()
	reqLogger.LogToolCall(block.Name, block)

	// Restore stderr and read output
	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("Failed to read from pipe: %v", err)
	}
	output := buf.String()

	// Should still produce logs even with nil input
	if !strings.Contains(output, "[INFO]") {
		t.Errorf("Should have INFO log even with nil input, got: %s", output)
	}
	if !strings.Contains(output, "[DEBUG]") {
		t.Errorf("Should have DEBUG log even with nil input, got: %s", output)
	}

	// Restore logger
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
		logger.WithComponent("test"),
	)
}

func TestLogToolCallTruncation(t *testing.T) {
	// Test that truncation preserves structure for nested data
	deepData := map[string]interface{}{
		"short": "ok",
		"long":  strings.Repeat("x", 100),
		"nested": map[string]interface{}{
			"deep_long": strings.Repeat("y", 100),
		},
		"array": []interface{}{
			strings.Repeat("z", 100),
			"short_item",
		},
	}

	// Capture stderr output
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// Reinitialize logger
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
		logger.WithComponent("test"),
	)

	// Create request logger and log tool call
	reqLogger := NewRequestScopedLogger()
	reqLogger.LogToolCall("truncation_test", deepData)

	// Restore stderr and read output
	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("Failed to read from pipe: %v", err)
	}
	output := buf.String()

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Fatalf("Expected 2 log lines, got %d", len(lines))
	}

	// Check INFO line has truncation
	infoLine := lines[0]
	if !strings.Contains(infoLine, "...") {
		t.Errorf("INFO log should contain truncation marker '...', got: %s", infoLine)
	}

	// Check DEBUG line has full data
	debugLine := lines[1]
	if strings.Contains(debugLine, "...") {
		t.Errorf("DEBUG log should not contain truncation marker '...', got: %s", debugLine)
	}

	// Verify structure is preserved in INFO log
	infoPrefix := "Tool call: truncation_test | Data: "
	idx := strings.Index(infoLine, infoPrefix)
	if idx == -1 {
		t.Fatalf("INFO log should contain expected prefix, got: %s", infoLine)
	}
	infoJSON := infoLine[idx+len(infoPrefix):]

	var infoParsed map[string]interface{}
	if err := json.Unmarshal([]byte(infoJSON), &infoParsed); err != nil {
		t.Errorf("Failed to parse INFO log JSON: %v", err)
	}

	// Check that all keys are present
	expectedKeys := []string{"short", "long", "nested", "array"}
	for _, key := range expectedKeys {
		if _, ok := infoParsed[key]; !ok {
			t.Errorf("INFO log should preserve key '%s' even with truncation", key)
		}
	}

	// Restore logger
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
		logger.WithComponent("test"),
	)
}
