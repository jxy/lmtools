package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// Helper functions for creating pointers
func intPtr(i int) *int {
	return &i
}

func TestConvertAnthropicToOpenAI(t *testing.T) {
	mapper := &ModelMapper{config: &Config{}}
	converter := NewConverter(mapper)

	tests := []struct {
		name     string
		input    *AnthropicRequest
		expected *OpenAIRequest
		wantErr  bool
	}{
		{
			name: "simple text message",
			input: &AnthropicRequest{
				Model:     "gpt-4",
				MaxTokens: 1000,
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Hello, how are you?"`),
					},
				},
			},
			expected: &OpenAIRequest{
				Model:     "gpt-4",
				MaxTokens: intPtr(1000),
				Messages: []OpenAIMessage{
					{
						Role:    RoleUser,
						Content: "Hello, how are you?",
					},
				},
			},
		},
		{
			name: "with system message",
			input: &AnthropicRequest{
				Model:     "gpt-4",
				MaxTokens: 1000,
				System:    json.RawMessage(`"You are a helpful assistant."`),
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Tell me a joke"`),
					},
				},
			},
			expected: &OpenAIRequest{
				Model:     "gpt-4",
				MaxTokens: intPtr(1000),
				Messages: []OpenAIMessage{
					{
						Role:    RoleSystem,
						Content: "You are a helpful assistant.",
					},
					{
						Role:    RoleUser,
						Content: "Tell me a joke",
					},
				},
			},
		},
		{
			name: "with content blocks",
			input: &AnthropicRequest{
				Model:     "gpt-4",
				MaxTokens: 1000,
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`[{"type": "text", "text": "Hello"}, {"type": "text", "text": " World"}]`),
					},
				},
			},
			expected: &OpenAIRequest{
				Model:     "gpt-4",
				MaxTokens: intPtr(1000),
				Messages: []OpenAIMessage{
					{
						Role:    RoleUser,
						Content: "Hello World",
					},
				},
			},
		},
		{
			name: "with tools",
			input: &AnthropicRequest{
				Model:     "gpt-4",
				MaxTokens: 1000,
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Calculate 2+2"`),
					},
				},
				Tools: []AnthropicTool{
					{
						Name:        "calculator",
						Description: "Calculate math expressions",
						InputSchema: map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"expression": map[string]interface{}{
									"type": "string",
								},
							},
						},
					},
				},
			},
			expected: &OpenAIRequest{
				Model:     "gpt-4",
				MaxTokens: intPtr(1000),
				Messages: []OpenAIMessage{
					{
						Role:    RoleUser,
						Content: "Calculate 2+2",
					},
				},
				Tools: []OpenAITool{
					{
						Type: "function",
						Function: OpenAIFunc{
							Name:        "calculator",
							Description: "Calculate math expressions",
							Parameters: map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"expression": map[string]interface{}{
										"type": "string",
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "with tools containing $schema",
			input: &AnthropicRequest{
				Model:     "gpt-4",
				MaxTokens: 1000,
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Use the tool"`),
					},
				},
				Tools: []AnthropicTool{
					{
						Name:        "test_tool",
						Description: "Test tool with schema",
						InputSchema: map[string]interface{}{
							"$schema": "http://json-schema.org/draft-07/schema#",
							"type":    "object",
							"properties": map[string]interface{}{
								"param": map[string]interface{}{
									"type": "string",
								},
							},
						},
					},
				},
			},
			expected: &OpenAIRequest{
				Model:     "gpt-4",
				MaxTokens: intPtr(1000),
				Messages: []OpenAIMessage{
					{
						Role:    RoleUser,
						Content: "Use the tool",
					},
				},
				Tools: []OpenAITool{
					{
						Type: "function",
						Function: OpenAIFunc{
							Name:        "test_tool",
							Description: "Test tool with schema",
							Parameters: map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"param": map[string]interface{}{
										"type": "string",
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "max tokens capping",
			input: &AnthropicRequest{
				Model:     "gpt-4",
				MaxTokens: 20000, // Above OpenAI limit
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Hello"`),
					},
				},
			},
			expected: &OpenAIRequest{
				Model:     "gpt-4",
				MaxTokens: intPtr(20000), // No longer capped, pass through as-is
				Messages: []OpenAIMessage{
					{
						Role:    RoleUser,
						Content: "Hello",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := converter.ConvertAnthropicToOpenAI(context.Background(), tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertAnthropicToOpenAI() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !deepEqual(got, tt.expected) {
				t.Errorf("ConvertAnthropicToOpenAI() = %+v, want %+v", got, tt.expected)
			}
		})
	}
}

func TestConvertOpenAIToAnthropic(t *testing.T) {
	mapper := &ModelMapper{config: &Config{}}
	converter := NewConverter(mapper)

	tests := []struct {
		name          string
		input         *OpenAIResponse
		originalModel string
		expected      *AnthropicResponse
	}{
		{
			name: "simple text response",
			input: &OpenAIResponse{
				ID:    "chatcmpl-123",
				Model: "gpt-4",
				Choices: []OpenAIChoice{
					{
						Message: OpenAIMessage{
							Role:    RoleAssistant,
							Content: "Hello! I'm doing well, thank you.",
						},
						FinishReason: "stop",
					},
				},
				Usage: &OpenAIUsage{
					PromptTokens:     10,
					CompletionTokens: 20,
				},
			},
			originalModel: "claude-3-haiku",
			expected: &AnthropicResponse{
				ID:   "chatcmpl-123",
				Type: "message",
				Role: RoleAssistant,
				Content: []AnthropicContentBlock{
					{
						Type: "text",
						Text: "Hello! I'm doing well, thank you.",
					},
				},
				Model:      "claude-3-haiku",
				StopReason: "end_turn",
				Usage: &AnthropicUsage{
					InputTokens:  10,
					OutputTokens: 20,
				},
			},
		},
		{
			name: "with tool calls",
			input: &OpenAIResponse{
				ID:    "chatcmpl-124",
				Model: "gpt-4",
				Choices: []OpenAIChoice{
					{
						Message: OpenAIMessage{
							Role:    RoleAssistant,
							Content: "I'll calculate that for you.",
							ToolCalls: []ToolCall{
								{
									ID:   "call_123",
									Type: "function",
									Function: FunctionCall{
										Name:      "calculator",
										Arguments: `{"expression": "2+2"}`,
									},
								},
							},
						},
						FinishReason: "tool_calls",
					},
				},
				Usage: &OpenAIUsage{
					PromptTokens:     15,
					CompletionTokens: 25,
				},
			},
			originalModel: "claude-3-sonnet",
			expected: &AnthropicResponse{
				ID:   "chatcmpl-124",
				Type: "message",
				Role: RoleAssistant,
				Content: []AnthropicContentBlock{
					{
						Type: "text",
						Text: "I'll calculate that for you.",
					},
					{
						Type: "tool_use",
						ID:   "call_123",
						Name: "calculator",
						Input: map[string]interface{}{
							"expression": "2+2",
						},
					},
				},
				Model:      "claude-3-sonnet",
				StopReason: "tool_use",
				Usage: &AnthropicUsage{
					InputTokens:  15,
					OutputTokens: 25,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := converter.ConvertOpenAIToAnthropic(tt.input, tt.originalModel)
			// Compare without checking generated IDs
			if got.Type != tt.expected.Type ||
				got.Role != tt.expected.Role ||
				got.Model != tt.expected.Model ||
				got.StopReason != tt.expected.StopReason ||
				!reflect.DeepEqual(got.Usage, tt.expected.Usage) {
				t.Errorf("ConvertOpenAIToAnthropic() basic fields mismatch = %+v, want %+v", got, tt.expected)
			}

			// Compare content blocks
			if len(got.Content) != len(tt.expected.Content) {
				t.Errorf("ConvertOpenAIToAnthropic() content length = %d, want %d", len(got.Content), len(tt.expected.Content))
			}
			for i := range got.Content {
				if got.Content[i].Type != tt.expected.Content[i].Type {
					t.Errorf("ConvertOpenAIToAnthropic() content[%d].Type = %s, want %s", i, got.Content[i].Type, tt.expected.Content[i].Type)
				}
				if got.Content[i].Text != tt.expected.Content[i].Text {
					t.Errorf("ConvertOpenAIToAnthropic() content[%d].Text = %s, want %s", i, got.Content[i].Text, tt.expected.Content[i].Text)
				}
				if got.Content[i].Name != tt.expected.Content[i].Name {
					t.Errorf("ConvertOpenAIToAnthropic() content[%d].Name = %s, want %s", i, got.Content[i].Name, tt.expected.Content[i].Name)
				}
			}
		})
	}
}

func TestConvertAnthropicToArgo(t *testing.T) {
	mapper := &ModelMapper{config: &Config{}}
	converter := NewConverter(mapper)

	tests := []struct {
		name     string
		input    *AnthropicRequest
		user     string
		expected *ArgoChatRequest
		wantErr  bool
	}{
		{
			name: "simple message",
			input: &AnthropicRequest{
				Model: "gpt-4",
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Hello Argo"`),
					},
				},
			},
			user: "testuser",
			expected: &ArgoChatRequest{
				User:  "testuser",
				Model: "gpt-4",
				Messages: []ArgoMessage{
					{
						Role:    "user",
						Content: "Hello Argo",
					},
				},
			},
		},
		{
			name: "openai model uses max_completion_tokens",
			input: &AnthropicRequest{
				Model:     "gpt-4",
				MaxTokens: 1000,
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Hello"`),
					},
				},
			},
			user: "testuser",
			expected: &ArgoChatRequest{
				User:                "testuser",
				Model:               "gpt-4",
				MaxCompletionTokens: 1000,
				Messages: []ArgoMessage{
					{
						Role:    "user",
						Content: "Hello",
					},
				},
			},
		},
		{
			name: "non-openai model uses max_tokens",
			input: &AnthropicRequest{
				Model:     "claude-3-haiku",
				MaxTokens: 1000,
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Hello"`),
					},
				},
			},
			user: "testuser",
			expected: &ArgoChatRequest{
				User:      "testuser",
				Model:     "claude-3-haiku",
				MaxTokens: 1000,
				Messages: []ArgoMessage{
					{
						Role:    "user",
						Content: "Hello",
					},
				},
			},
		},
		{
			name: "with system message",
			input: &AnthropicRequest{
				Model:  "gpt-4",
				System: json.RawMessage(`"You are a helpful assistant"`),
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Help me"`),
					},
				},
			},
			user: "testuser",
			expected: &ArgoChatRequest{
				User:  "testuser",
				Model: "gpt-4",
				Messages: []ArgoMessage{
					{
						Role:    "system",
						Content: "You are a helpful assistant",
					},
					{
						Role:    "user",
						Content: "Help me",
					},
				},
			},
		},
		{
			name: "with content blocks",
			input: &AnthropicRequest{
				Model: "gpt-4",
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`[{"type": "text", "text": "Part 1"}, {"type": "text", "text": "Part 2"}]`),
					},
				},
			},
			user: "testuser",
			expected: &ArgoChatRequest{
				User:  "testuser",
				Model: "gpt-4",
				Messages: []ArgoMessage{
					{
						Role: "user",
						Content: []AnthropicContentBlock{
							{Type: "text", Text: "Part 1"},
							{Type: "text", Text: "Part 2"},
						},
					},
				},
			},
		},
		{
			name: "with tool_result blocks for OpenAI model",
			input: &AnthropicRequest{
				Model: "gpto3",
				Messages: []AnthropicMessage{
					{
						Role: RoleUser,
						Content: json.RawMessage(`[{
							"type": "tool_result",
							"tool_use_id": "call_PqB38FjnsQbA0cYOp0Af6Bbj",
							"content": "Tool result content here"
						}]`),
					},
				},
				MaxTokens: 1000,
			},
			user: "testuser",
			expected: &ArgoChatRequest{
				User:                "testuser",
				Model:               "gpto3",
				MaxCompletionTokens: 1000,
				Messages: []ArgoMessage{
					{
						Role:       "tool",
						ToolCallID: "call_PqB38FjnsQbA0cYOp0Af6Bbj",
						Content:    "Tool result content here",
					},
				},
			},
		},
		{
			name: "with tool_result blocks for non-OpenAI model (should preserve)",
			input: &AnthropicRequest{
				Model: "claude-3-haiku",
				Messages: []AnthropicMessage{
					{
						Role: RoleUser,
						Content: json.RawMessage(`[{
							"type": "tool_result",
							"tool_use_id": "call_PqB38FjnsQbA0cYOp0Af6Bbj",
							"content": "Tool result content here"
						}]`),
					},
				},
				MaxTokens: 1000,
			},
			user: "testuser",
			expected: &ArgoChatRequest{
				User:      "testuser",
				Model:     "claude-3-haiku",
				MaxTokens: 1000,
				Messages: []ArgoMessage{
					{
						Role: "user",
						Content: []AnthropicContentBlock{
							{
								Type:      "tool_result",
								ToolUseID: "call_PqB38FjnsQbA0cYOp0Af6Bbj",
								Content:   json.RawMessage(`"Tool result content here"`),
							},
						},
					},
				},
			},
		},
		{
			name: "assistant tool_use blocks for OpenAI model",
			input: &AnthropicRequest{
				Model: "gpto3",
				Messages: []AnthropicMessage{
					{
						Role: RoleAssistant,
						Content: json.RawMessage(`[{
							"type": "tool_use",
							"id": "call_47BSzK51qN5Fq0hMJT1HXx5j",
							"name": "Read",
							"input": {
								"file_path": "/usr/home/jin/K/W/P002/lmtools/cmd/apiproxy/run_tests.sh"
							}
						}]`),
					},
				},
				MaxTokens: 1000,
			},
			user: "testuser",
			expected: &ArgoChatRequest{
				User:                "testuser",
				Model:               "gpto3",
				MaxCompletionTokens: 1000,
				Messages: []ArgoMessage{
					{
						Role:    "assistant",
						Content: "", // Empty text content
						ToolCalls: []ToolCall{
							{
								ID:   "call_47BSzK51qN5Fq0hMJT1HXx5j",
								Type: "function",
								Function: FunctionCall{
									Name:      "Read",
									Arguments: `{"file_path":"/usr/home/jin/K/W/P002/lmtools/cmd/apiproxy/run_tests.sh"}`,
								},
							},
						},
					},
				},
			},
		},
		{
			name: "assistant mixed text and tool_use for OpenAI model",
			input: &AnthropicRequest{
				Model: "gpt-4",
				Messages: []AnthropicMessage{
					{
						Role: RoleAssistant,
						Content: json.RawMessage(`[{
							"type": "text",
							"text": "I'll read that file for you."
						}, {
							"type": "tool_use",
							"id": "call_123",
							"name": "Read",
							"input": {"file_path": "/tmp/test.txt"}
						}]`),
					},
				},
			},
			user: "testuser",
			expected: &ArgoChatRequest{
				User:  "testuser",
				Model: "gpt-4",
				Messages: []ArgoMessage{
					{
						Role:    "assistant",
						Content: "I'll read that file for you.",
						ToolCalls: []ToolCall{
							{
								ID:   "call_123",
								Type: "function",
								Function: FunctionCall{
									Name:      "Read",
									Arguments: `{"file_path":"/tmp/test.txt"}`,
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := converter.ConvertAnthropicToArgo(context.Background(), tt.input, tt.user)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertAnthropicToArgo() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("ConvertAnthropicToArgo() = %+v, want %+v", got, tt.expected)
			}
		})
	}
}

func TestExtractSystemContent(t *testing.T) {
	tests := []struct {
		name     string
		input    json.RawMessage
		expected string
		wantErr  bool
	}{
		{
			name:     "simple string",
			input:    json.RawMessage(`"You are helpful"`),
			expected: "You are helpful",
		},
		{
			name:     "content blocks",
			input:    json.RawMessage(`[{"type": "text", "text": "Line 1"}, {"type": "text", "text": "Line 2"}]`),
			expected: "Line 1\nLine 2",
		},
		{
			name:     "invalid json",
			input:    json.RawMessage(`{invalid`),
			expected: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractSystemContent(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("extractSystemContent() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("extractSystemContent() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFilterSchemaMetadata(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected interface{}
	}{
		{
			name: "simple schema with $schema",
			input: map[string]interface{}{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"type":    "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type": "string",
					},
				},
			},
			expected: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type": "string",
					},
				},
			},
		},
		{
			name: "nested $schema in properties",
			input: map[string]interface{}{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"type":    "object",
				"properties": map[string]interface{}{
					"nested": map[string]interface{}{
						"$schema": "http://json-schema.org/draft-07/schema#",
						"type":    "object",
					},
				},
			},
			expected: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"nested": map[string]interface{}{
						"type": "object",
					},
				},
			},
		},
		{
			name: "no $schema present",
			input: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"age": map[string]interface{}{
						"type": "number",
					},
				},
			},
			expected: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"age": map[string]interface{}{
						"type": "number",
					},
				},
			},
		},
		{
			name:     "non-map input",
			input:    "string value",
			expected: "string value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterSchemaMetadata(tt.input)
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("filterSchemaMetadata() = %+v, want %+v", got, tt.expected)
			}
		})
	}
}

// Helper function for deep equality comparison
func deepEqual(a, b interface{}) bool {
	// For OpenAIRequest comparison, we need custom logic because
	// interface{} fields might have different types but same values
	if reqA, ok := a.(*OpenAIRequest); ok {
		if reqB, ok := b.(*OpenAIRequest); ok {
			// Compare all fields except Messages
			if reqA.Model != reqB.Model ||
				!reflect.DeepEqual(reqA.MaxTokens, reqB.MaxTokens) ||
				!reflect.DeepEqual(reqA.Temperature, reqB.Temperature) ||
				!reflect.DeepEqual(reqA.TopP, reqB.TopP) ||
				reqA.Stream != reqB.Stream ||
				!reflect.DeepEqual(reqA.Stop, reqB.Stop) ||
				!reflect.DeepEqual(reqA.Tools, reqB.Tools) {
				return false
			}

			// Compare Messages with special handling for Content
			if len(reqA.Messages) != len(reqB.Messages) {
				return false
			}
			for i := range reqA.Messages {
				if reqA.Messages[i].Role != reqB.Messages[i].Role {
					return false
				}
				// Compare content as strings
				contentA := fmt.Sprintf("%v", reqA.Messages[i].Content)
				contentB := fmt.Sprintf("%v", reqB.Messages[i].Content)
				if contentA != contentB {
					return false
				}
			}
			return true
		}
	}

	return reflect.DeepEqual(a, b)
}
