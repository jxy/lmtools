package proxy

import (
	"context"
	"lmtools/internal/logger"
	"strings"
	"testing"
	"time"
)

func TestRequestSummary(t *testing.T) {
	// Initialize logger for testing
	logger.ResetForTesting()
	err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithStderr(true),
		logger.WithFile(false),
	)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	tests := []struct {
		name          string
		method        string
		path          string
		originalModel string
		mappedModel   string
		provider      string
		statusCode    int
		streaming     bool
	}{
		{
			name:          "non-streaming request",
			method:        "POST",
			path:          "/v1/messages",
			originalModel: "gpt-4o",
			mappedModel:   "gpt-4o-mini",
			provider:      "openai",
			statusCode:    200,
			streaming:     false,
		},
		{
			name:          "streaming request",
			method:        "POST",
			path:          "/v1/messages",
			originalModel: "claude-3-opus",
			mappedModel:   "claude-3-opus-20240229",
			provider:      "anthropic",
			statusCode:    200,
			streaming:     true,
		},
		{
			name:          "error response",
			method:        "POST",
			path:          "/v1/messages",
			originalModel: "unknown-model",
			mappedModel:   "unknown-model",
			provider:      "openai",
			statusCode:    400,
			streaming:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a context with a scoped logger
			scope := logger.GetLogger().NewScope("test")
			ctx := logger.WithContext(context.Background(), scope)

			// Sleep briefly to ensure non-zero duration
			time.Sleep(10 * time.Millisecond)

			// Call RequestSummary - mainly testing it doesn't panic
			duration := scope.GetDuration()
			numMessages := 3 // example values
			numTools := 1
			RequestSummary(ctx, tt.method, tt.path, tt.originalModel, tt.mappedModel,
				tt.provider, numMessages, numTools, tt.statusCode, tt.streaming, duration)

			// If we get here without panic, the test passes
		})
	}
}

func TestTruncateValue(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		maxLen   int
		expected interface{}
	}{
		{
			name:     "short string unchanged",
			input:    "hello world",
			maxLen:   64,
			expected: "hello world",
		},
		{
			name:     "long string truncated",
			input:    strings.Repeat("a", 100),
			maxLen:   64,
			expected: strings.Repeat("a", 64) + "...",
		},
		{
			name: "map with mixed values",
			input: map[string]interface{}{
				"short": "value",
				"long":  strings.Repeat("b", 100),
				"nested": map[string]interface{}{
					"key": strings.Repeat("c", 100),
				},
			},
			maxLen: 64,
			expected: map[string]interface{}{
				"short": "value",
				"long":  strings.Repeat("b", 64) + "...",
				"nested": map[string]interface{}{
					"key": strings.Repeat("c", 64) + "...",
				},
			},
		},
		{
			name: "slice with long strings",
			input: []interface{}{
				"short",
				strings.Repeat("d", 100),
				123,
			},
			maxLen: 64,
			expected: []interface{}{
				"short",
				strings.Repeat("d", 64) + "...",
				123,
			},
		},
		{
			name:     "non-string types unchanged",
			input:    123,
			maxLen:   64,
			expected: 123,
		},
		{
			name:     "nil value",
			input:    nil,
			maxLen:   64,
			expected: nil,
		},
		{
			name:     "empty string",
			input:    "",
			maxLen:   64,
			expected: "",
		},
		{
			name:     "string exactly at limit",
			input:    strings.Repeat("e", 64),
			maxLen:   64,
			expected: strings.Repeat("e", 64),
		},
		{
			name:     "string one over limit",
			input:    strings.Repeat("f", 65),
			maxLen:   64,
			expected: strings.Repeat("f", 64) + "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateValue(tt.input, tt.maxLen)
			if !deepEqualTest(result, tt.expected) {
				t.Errorf("truncateValue() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestTruncateValuePreservesJSONStructure(t *testing.T) {
	// Complex nested structure
	input := map[string]interface{}{
		"tool_name": "calculate_sum",
		"input": map[string]interface{}{
			"numbers":     []interface{}{1, 2, 3, 4, 5},
			"description": strings.Repeat("Calculate the sum of these numbers ", 10),
			"metadata": map[string]interface{}{
				"user":       "test_user",
				"long_field": strings.Repeat("x", 100),
			},
		},
	}

	result := truncateValue(input, 64)

	// Verify structure is preserved
	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("Result should be a map")
	}

	if resultMap["tool_name"] != "calculate_sum" {
		t.Errorf("tool_name should be 'calculate_sum', got %v", resultMap["tool_name"])
	}

	inputMap, ok := resultMap["input"].(map[string]interface{})
	if !ok {
		t.Fatal("input should be a map")
	}

	numbers, ok := inputMap["numbers"].([]interface{})
	if !ok {
		t.Fatal("numbers should be a slice")
	}
	expectedNumbers := []interface{}{1, 2, 3, 4, 5}
	if !deepEqualTest(numbers, expectedNumbers) {
		t.Errorf("numbers should be %v, got %v", expectedNumbers, numbers)
	}

	// Long strings should be truncated
	desc, ok := inputMap["description"].(string)
	if !ok {
		t.Fatal("description should be a string")
	}
	if !strings.HasSuffix(desc, "...") {
		t.Error("Long description should be truncated with ...")
	}
	if len(desc) > 67 { // 64 + "..." = 67
		t.Errorf("Truncated string should not exceed maxLen+3, got %d", len(desc))
	}

	metadata, ok := inputMap["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("metadata should be a map")
	}
	if metadata["user"] != "test_user" {
		t.Errorf("user should be 'test_user', got %v", metadata["user"])
	}

	longField, ok := metadata["long_field"].(string)
	if !ok {
		t.Fatal("long_field should be a string")
	}
	if !strings.HasSuffix(longField, "...") {
		t.Error("Long field should be truncated with ...")
	}
	if len(longField) > 67 { // 64 + "..." = 67
		t.Errorf("Truncated string should not exceed maxLen+3, got %d", len(longField))
	}
}

// Helper function to compare deeply nested structures
func deepEqualTest(a, b interface{}) bool {
	switch av := a.(type) {
	case map[string]interface{}:
		bv, ok := b.(map[string]interface{})
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !deepEqualTest(v, bv[k]) {
				return false
			}
		}
		return true
	case []interface{}:
		bv, ok := b.([]interface{})
		if !ok || len(av) != len(bv) {
			return false
		}
		for i, v := range av {
			if !deepEqualTest(v, bv[i]) {
				return false
			}
		}
		return true
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case int:
		bv, ok := b.(int)
		return ok && av == bv
	case nil:
		return b == nil
	default:
		return a == b
	}
}
