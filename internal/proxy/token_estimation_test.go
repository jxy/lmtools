package proxy

import (
	"encoding/json"
	"lmtools/internal/core"
	"testing"
)

func TestEstimateRequestTokens(t *testing.T) {
	tests := []struct {
		name      string
		request   *AnthropicRequest
		minTokens int // Minimum expected tokens
		maxTokens int // Maximum expected tokens
	}{
		{
			name: "Simple message",
			request: &AnthropicRequest{
				Messages: []AnthropicMessage{
					{
						Role:    core.RoleUser,
						Content: json.RawMessage(`"Hello, how are you?"`),
					},
				},
			},
			minTokens: 35, // ~20 chars + overhead / 3
			maxTokens: 45,
		},
		{
			name: "Message with system prompt",
			request: &AnthropicRequest{
				System: json.RawMessage(`"You are a helpful assistant."`),
				Messages: []AnthropicMessage{
					{
						Role:    core.RoleUser,
						Content: json.RawMessage(`"What is the weather today?"`),
					},
				},
			},
			minTokens: 50,
			maxTokens: 70,
		},
		{
			name: "Multiple messages",
			request: &AnthropicRequest{
				Messages: []AnthropicMessage{
					{
						Role:    core.RoleUser,
						Content: json.RawMessage(`"Tell me a story"`),
					},
					{
						Role:    core.RoleAssistant,
						Content: json.RawMessage(`"Once upon a time, there was a brave knight who lived in a castle."`),
					},
					{
						Role:    core.RoleUser,
						Content: json.RawMessage(`"What happened next?"`),
					},
				},
			},
			minTokens: 65, // Adjusted based on actual character count
			maxTokens: 85,
		},
		{
			name: "Request with tools",
			request: &AnthropicRequest{
				Messages: []AnthropicMessage{
					{
						Role:    core.RoleUser,
						Content: json.RawMessage(`"What's the weather in San Francisco?"`),
					},
				},
				Tools: []AnthropicTool{
					{
						Name:        "get_weather",
						Description: "Get the current weather for a given location",
						InputSchema: map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"location": map[string]interface{}{
									"type":        "string",
									"description": "The city and state, e.g. San Francisco, CA",
								},
							},
							"required": []string{"location"},
						},
					},
				},
			},
			minTokens: 100, // Adjusted for actual character count
			maxTokens: 130,
		},
		{
			name: "Large conversation (257 messages scenario from issue)",
			request: func() *AnthropicRequest {
				messages := make([]AnthropicMessage, 257)
				for i := 0; i < 257; i++ {
					if i%2 == 0 {
						messages[i] = AnthropicMessage{
							Role:    core.RoleUser,
							Content: json.RawMessage(`"This is a user message with some content to simulate a real conversation."`),
						}
					} else {
						messages[i] = AnthropicMessage{
							Role:    core.RoleAssistant,
							Content: json.RawMessage(`"This is an assistant response with helpful information and context."`),
						}
					}
				}
				return &AnthropicRequest{
					Messages: messages,
				}
			}(),
			minTokens: 6000, // Much more than 28!
			maxTokens: 8000,
		},
		{
			name: "Complex content blocks",
			request: &AnthropicRequest{
				Messages: []AnthropicMessage{
					{
						Role:    core.RoleUser,
						Content: json.RawMessage(`[{"type":"text","text":"Analyze this data"},{"type":"text","text":"Here is more context"}]`),
					},
				},
			},
			minTokens: 60, // Adjusted for JSON array content
			maxTokens: 80,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := EstimateRequestTokens(tt.request)

			if tokens < tt.minTokens {
				t.Errorf("EstimateRequestTokens() = %d, want at least %d", tokens, tt.minTokens)
			}
			if tokens > tt.maxTokens {
				t.Errorf("EstimateRequestTokens() = %d, want at most %d", tokens, tt.maxTokens)
			}

			// Ensure we never get unreasonably low counts like 28 for large conversations
			if tt.name == "Large conversation (257 messages scenario from issue)" && tokens < 1000 {
				t.Errorf("Large conversation should have at least 1000 tokens, got %d", tokens)
			}
		})
	}
}

func TestConvertArgoToAnthropicWithRequest_TokenCounting(t *testing.T) {
	converter := NewConverter(NewModelMapper(&Config{}))

	// Test request with known content
	request := &AnthropicRequest{
		System: json.RawMessage(`"You are a helpful coding assistant."`),
		Messages: []AnthropicMessage{
			{
				Role:    core.RoleUser,
				Content: json.RawMessage(`"Write a function to calculate fibonacci numbers"`),
			},
		},
	}

	// Test string response
	t.Run("String response", func(t *testing.T) {
		argoResp := &ArgoChatResponse{
			Response: "Here's a fibonacci function:\n\nfunc fibonacci(n int) int {\n    if n <= 1 {\n        return n\n    }\n    return fibonacci(n-1) + fibonacci(n-2)\n}",
		}

		anthResp := converter.ConvertArgoToAnthropicWithRequest(argoResp, "claude-3-opus", request)

		if anthResp.Usage == nil {
			t.Fatal("Expected Usage to be set")
		}

		// Input tokens should be based on request
		if anthResp.Usage.InputTokens < 20 || anthResp.Usage.InputTokens > 100 {
			t.Errorf("Input tokens = %d, expected between 20 and 100", anthResp.Usage.InputTokens)
		}

		// Output tokens should be based on response
		if anthResp.Usage.OutputTokens < 30 || anthResp.Usage.OutputTokens > 80 {
			t.Errorf("Output tokens = %d, expected between 30 and 80", anthResp.Usage.OutputTokens)
		}

		// Input and output should be different
		if anthResp.Usage.InputTokens == anthResp.Usage.OutputTokens {
			t.Error("Input and output tokens should not be equal")
		}
	})

	// Test map response with content array
	t.Run("Map response with content", func(t *testing.T) {
		argoResp := &ArgoChatResponse{
			Response: map[string]interface{}{
				"content": []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": "Here's the fibonacci function implementation.",
					},
				},
			},
		}

		anthResp := converter.ConvertArgoToAnthropicWithRequest(argoResp, "claude-3-opus", request)

		if anthResp.Usage == nil {
			t.Fatal("Expected Usage to be set")
		}

		// Verify tokens are calculated
		if anthResp.Usage.InputTokens == 0 {
			t.Error("Input tokens should not be 0")
		}
		if anthResp.Usage.OutputTokens == 0 {
			t.Error("Output tokens should not be 0")
		}
	})
}

func TestEstimateTokenCount(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name:     "Empty string",
			text:     "",
			expected: 0,
		},
		{
			name:     "Short text",
			text:     "Hello",
			expected: 1, // 5 chars / 3 = 1.67 -> 1
		},
		{
			name:     "Medium text",
			text:     "The quick brown fox jumps over the lazy dog",
			expected: 14, // 44 chars / 3 = 14.67 -> 14
		},
		{
			name:     "Long text",
			text:     "This is a longer piece of text that should result in more tokens when estimated using our simple heuristic",
			expected: 35, // 107 chars / 3 = 35.67 -> 35
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EstimateTokenCount(tt.text)
			if result != tt.expected {
				t.Errorf("EstimateTokenCount(%q) = %d, want %d", tt.text, result, tt.expected)
			}
		})
	}
}
