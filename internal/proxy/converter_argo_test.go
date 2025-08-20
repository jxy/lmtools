package proxy

import (
	"context"
	"encoding/json"
	"testing"
)

func TestConvertArgoToAnthropicWithRequest_ToolCallsAsObject(t *testing.T) {
	converter := &Converter{}

	tests := []struct {
		name     string
		response *ArgoChatResponse
		wantTool bool
		toolName string
		toolArgs map[string]interface{}
	}{
		{
			name: "tool_calls as single object (Google format)",
			response: &ArgoChatResponse{
				Response: map[string]interface{}{
					"content": "",
					"tool_calls": map[string]interface{}{
						"name": "get_weather",
						"args": map[string]interface{}{
							"location": "Paris",
						},
					},
				},
			},
			wantTool: true,
			toolName: "get_weather",
			toolArgs: map[string]interface{}{
				"location": "Paris",
			},
		},
		{
			name: "tool_calls as array (standard format)",
			response: &ArgoChatResponse{
				Response: map[string]interface{}{
					"content": "",
					"tool_calls": []interface{}{
						map[string]interface{}{
							"name": "get_weather",
							"args": map[string]interface{}{
								"location": "New York, NY",
							},
						},
					},
				},
			},
			wantTool: true,
			toolName: "get_weather",
			toolArgs: map[string]interface{}{
				"location": "New York, NY",
			},
		},
		{
			name: "no tool calls",
			response: &ArgoChatResponse{
				Response: map[string]interface{}{
					"content": "The weather is sunny today.",
				},
			},
			wantTool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &AnthropicRequest{
				Model: "gemini-2.5-pro",
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"What is the weather?"`),
					},
				},
			}

			result := converter.ConvertArgoToAnthropicWithRequest(tt.response, "claude-3-sonnet-20240229", req)

			if result == nil {
				t.Fatal("Expected non-nil result")
			}

			if tt.wantTool {
				// Check that we have a tool_use block
				found := false
				for _, block := range result.Content {
					if block.Type == "tool_use" {
						found = true
						if block.Name != tt.toolName {
							t.Errorf("Expected tool name %s, got %s", tt.toolName, block.Name)
						}
						// Compare tool args
						for k, v := range tt.toolArgs {
							if actual, ok := block.Input[k]; !ok || actual != v {
								t.Errorf("Expected arg %s=%v, got %v", k, v, actual)
							}
						}
						break
					}
				}
				if !found {
					t.Error("Expected tool_use block, but none found")
				}
			} else {
				// Check that we have text content
				if len(result.Content) == 0 || result.Content[0].Type != "text" {
					t.Error("Expected text content")
				}
			}
		})
	}
}

func TestConvertAnthropicToArgo_GoogleMessages(t *testing.T) {
	mapper := NewModelMapper(&Config{
		Provider: "argo",
		Model:    "gemini25pro",
	})
	converter := &Converter{mapper: mapper}

	tests := []struct {
		name       string
		messages   []AnthropicMessage
		checkParts bool
		wantError  bool
	}{
		{
			name: "simple text message for Google",
			messages: []AnthropicMessage{
				{
					Role:    RoleUser,
					Content: json.RawMessage(`"Hello, how are you?"`),
				},
			},
			checkParts: false, // Argo uses string content for simple messages
		},
		{
			name: "assistant with tool_use for Google",
			messages: []AnthropicMessage{
				{
					Role:    RoleUser,
					Content: json.RawMessage(`"What is the weather in Paris?"`),
				},
				{
					Role: RoleAssistant,
					Content: json.RawMessage(`[
						{"type": "text", "text": "I'll check the weather in Paris for you."},
						{"type": "tool_use", "id": "tool_123", "name": "get_weather", "input": {"location": "Paris"}}
					]`),
				},
			},
			checkParts: false, // Argo uses OpenAI-style format for tool messages
		},
		{
			name: "user with tool_result for Google",
			messages: []AnthropicMessage{
				{
					Role:    RoleUser,
					Content: json.RawMessage(`"What is the weather in Paris?"`),
				},
				{
					Role: RoleAssistant,
					Content: json.RawMessage(`[
						{"type": "text", "text": "I'll check the weather in Paris for you."},
						{"type": "tool_use", "id": "tool_123", "name": "get_weather", "input": {"location": "Paris"}}
					]`),
				},
				{
					Role: RoleUser,
					Content: json.RawMessage(`[
						{"type": "tool_result", "tool_use_id": "tool_123", "content": "Temperature: 22°C, Sunny"}
					]`),
				},
			},
			checkParts: false, // Argo uses OpenAI-style format for tool messages
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &AnthropicRequest{
				Model:    "gemini25pro",
				Messages: tt.messages,
			}

			result, err := converter.ConvertAnthropicToArgo(context.TODO(), req, "testuser")

			if tt.wantError {
				if err == nil {
					t.Fatal("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if result == nil {
				t.Fatal("Expected non-nil result")
			}

			// For Argo's Google integration, check the format based on message content
			for i, msg := range result.Messages {
				// Skip system messages
				if msg.Role == "system" {
					continue
				}

				// Check role mapping - Argo API uses "assistant" for all models including Google
				if msg.Role != "user" && msg.Role != "assistant" && msg.Role != "tool" {
					t.Errorf("Message %d: expected role to be 'user', 'assistant', or 'tool', got %s", i, msg.Role)
				}

				// For simple messages, Argo uses string content
				if _, ok := msg.Content.(string); ok {
					// This is expected for simple text messages
					continue
				}

				// For complex messages with tools, Argo uses OpenAI-style format
				if len(msg.ToolCalls) > 0 {
					// This is expected for tool call messages
					continue
				}

				// For Parts format (only used for non-tool complex messages)
				if parts, ok := msg.Content.([]GooglePart); ok && tt.checkParts {
					// Verify parts are properly formed
					if len(parts) == 0 {
						t.Errorf("Message %d: expected at least one part", i)
					}
				}
			}
		})
	}
}

func TestConvertAnthropicToArgo_OpenAIMessages(t *testing.T) {
	mapper := NewModelMapper(&Config{
		Provider: "argo",
		Model:    "gpt4o",
	})
	converter := &Converter{mapper: mapper}

	// Test that OpenAI models keep their original format
	req := &AnthropicRequest{
		Model: "gpt4o",
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
				Content: json.RawMessage(`"Hello"`),
			},
			{
				Role: RoleAssistant,
				Content: json.RawMessage(`[
					{"type": "text", "text": "Hi! I'll help you."},
					{"type": "tool_use", "id": "tool_123", "name": "get_info", "input": {"query": "test"}}
				]`),
			},
		},
	}

	result, err := converter.ConvertAnthropicToArgo(context.TODO(), req, "testuser")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// For OpenAI, tool_use should be converted to tool_calls
	found := false
	for _, msg := range result.Messages {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			found = true
			if msg.ToolCalls[0].Function.Name != "get_info" {
				t.Errorf("Expected tool name 'get_info', got %s", msg.ToolCalls[0].Function.Name)
			}
		}
	}

	if !found {
		t.Error("Expected assistant message with tool_calls for OpenAI model")
	}
}
