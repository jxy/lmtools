package proxy

import (
	"context"
	"encoding/json"
	"testing"
)

func TestConvertAnthropicToArgo_MaxTokensHandling(t *testing.T) {
	tests := []struct {
		name            string
		model           string
		maxTokens       int
		stream          bool
		hasTools        bool
		expectMaxTokens bool
		expectedValue   int
	}{
		{
			name:            "Claude non-streaming with max_tokens >= 21000",
			model:           "claude-3-opus-20240229",
			maxTokens:       32000,
			stream:          false,
			hasTools:        false,
			expectMaxTokens: false, // Should be dropped
		},
		{
			name:            "Claude streaming without tools with max_tokens >= 21000",
			model:           "claude-3-opus-20240229",
			maxTokens:       32000,
			stream:          true,
			hasTools:        false,
			expectMaxTokens: true, // Should be kept for real streaming
			expectedValue:   32000,
		},
		{
			name:            "Claude streaming with tools with max_tokens >= 21000",
			model:           "claude-3-opus-20240229",
			maxTokens:       32000,
			stream:          true,
			hasTools:        true,
			expectMaxTokens: false, // Should be dropped (simulated streaming)
		},
		{
			name:            "Claude non-streaming with max_tokens < 21000",
			model:           "claude-3-opus-20240229",
			maxTokens:       4096,
			stream:          false,
			hasTools:        false,
			expectMaxTokens: true, // Should be kept
			expectedValue:   4096,
		},
		{
			name:            "Claude streaming with tools with max_tokens < 21000",
			model:           "claude-3-opus-20240229",
			maxTokens:       4096,
			stream:          true,
			hasTools:        true,
			expectMaxTokens: true, // Should be kept
			expectedValue:   4096,
		},
		{
			name:            "OpenAI model should use max_completion_tokens",
			model:           "gpt-4",
			maxTokens:       32000,
			stream:          false,
			hasTools:        false,
			expectMaxTokens: false, // MaxTokens should not be set for OpenAI
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mapper := NewModelMapper(&Config{})
			converter := NewConverter(mapper)

			// Create request
			req := &AnthropicRequest{
				Model:     tt.model,
				MaxTokens: tt.maxTokens,
				Stream:    tt.stream,
				Messages: []AnthropicMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`"Hello"`),
					},
				},
			}

			// Add tools if needed
			if tt.hasTools {
				req.Tools = []AnthropicTool{
					{
						Name:        "test_tool",
						Description: "A test tool",
						InputSchema: map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"param": map[string]interface{}{
									"type": "string",
								},
							},
						},
					},
				}
			}

			// Convert
			argoReq, err := converter.ConvertAnthropicToArgo(ctx, req, "testuser")
			if err != nil {
				t.Fatalf("Failed to convert: %v", err)
			}

			// Check MaxTokens field
			if tt.expectMaxTokens {
				if argoReq.MaxTokens != tt.expectedValue {
					t.Errorf("Expected MaxTokens=%d, got %d", tt.expectedValue, argoReq.MaxTokens)
				}
			} else {
				if argoReq.MaxTokens != 0 {
					t.Errorf("Expected MaxTokens to be 0 (dropped), got %d", argoReq.MaxTokens)
				}
			}

			// For OpenAI models, check MaxCompletionTokens
			if tt.model == "gpt-4" {
				if argoReq.MaxCompletionTokens != tt.maxTokens {
					t.Errorf("Expected MaxCompletionTokens=%d for OpenAI model, got %d",
						tt.maxTokens, argoReq.MaxCompletionTokens)
				}
			}
		})
	}
}

// TestConvertAnthropicToArgo_MaxTokensJSON verifies that max_tokens is properly
// omitted from JSON when set to 0
func TestConvertAnthropicToArgo_MaxTokensJSON(t *testing.T) {
	ctx := context.Background()
	mapper := NewModelMapper(&Config{})
	converter := NewConverter(mapper)

	// Create request that should have max_tokens dropped
	req := &AnthropicRequest{
		Model:     "claude-3-opus-20240229",
		MaxTokens: 32000,
		Stream:    true,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"Hello"`),
			},
		},
		Tools: []AnthropicTool{
			{
				Name:        "test_tool",
				Description: "A test tool",
				InputSchema: map[string]interface{}{
					"type": "object",
				},
			},
		},
	}

	// Convert
	argoReq, err := converter.ConvertAnthropicToArgo(ctx, req, "testuser")
	if err != nil {
		t.Fatalf("Failed to convert: %v", err)
	}

	// Marshal to JSON
	jsonBytes, err := json.Marshal(argoReq)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	jsonStr := string(jsonBytes)

	// Check that max_tokens is not in the JSON
	if contains := json.Valid(jsonBytes); !contains {
		t.Error("Invalid JSON produced")
	}

	// Parse back to check max_tokens is not present
	var parsed map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if _, exists := parsed["max_tokens"]; exists {
		t.Errorf("max_tokens should not be present in JSON, but found in: %s", jsonStr)
	}
}
