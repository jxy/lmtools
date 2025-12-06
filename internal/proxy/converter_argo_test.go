package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"strings"
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
						Role:    core.RoleUser,
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

func TestConvertArgoToAnthropicWithRequest_Workaround_AnthropicToolUseEmbeddedInContent(t *testing.T) {
	converter := &Converter{}
	argo := &ArgoChatResponse{
		Response: map[string]interface{}{
			"content":    "Now let me read the openai_convert.go file to understand the current ConvertBlocksToOpenAIContent implementation:{'type': 'tool_use', 'id': 'toolu_vrtx_01TCVSw8Ff8eJHs5nSaZsPBt', 'name': 'Read', 'input': {'file_path': '/path/to/project/internal/core/openai_convert.go'}}",
			"tool_calls": []interface{}{},
		},
	}

	req := &AnthropicRequest{
		Model:    "claude-3-sonnet-20240229",
		Messages: []AnthropicMessage{{Role: core.RoleUser, Content: json.RawMessage(`"do test"`)}},
		Tools:    []AnthropicTool{{Name: "Read"}, {Name: "Write"}, {Name: "Edit"}},
	}

	result := converter.ConvertArgoToAnthropicWithRequest(argo, "claude-3-sonnet-20240229", req)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Expect first block to be trimmed text, second to be tool_use
	if len(result.Content) < 2 {
		t.Fatalf("Expected at least 2 content blocks, got %d", len(result.Content))
	}
	if result.Content[0].Type != "text" {
		t.Errorf("Expected first block type 'text', got %s", result.Content[0].Type)
	}
	if result.Content[1].Type != "tool_use" {
		t.Errorf("Expected second block type 'tool_use', got %s", result.Content[1].Type)
	}
	if result.Content[1].Name != "Read" {
		t.Errorf("Unexpected tool name: %s", result.Content[1].Name)
	}
	if _, ok := result.Content[1].Input["file_path"]; !ok {
		t.Errorf("Expected file_path in tool_use input, got %v", result.Content[1].Input)
	}
}

// Ensure trailing punctuation/formatting suffix after embedded tool call is preserved
func TestConvertArgoToAnthropicWithRequest_Workaround_PreserveSuffixPunctuation(t *testing.T) {
	converter := &Converter{}
	argo := &ArgoChatResponse{
		Response: map[string]interface{}{
			// Note trailing period after the embedded JSON
			"content":    "Run it now:{'type': 'tool_use', 'id': 'tool_1', 'name': 'Read', 'input': {'file_path': '/path/a'}}.",
			"tool_calls": []interface{}{},
		},
	}

	req := &AnthropicRequest{Model: "claude-3-sonnet-20240229", Messages: []AnthropicMessage{{Role: core.RoleUser, Content: json.RawMessage(`"go"`)}}, Tools: []AnthropicTool{{Name: "Read"}, {Name: "Write"}, {Name: "Edit"}}}
	result := converter.ConvertArgoToAnthropicWithRequest(argo, "claude-3-sonnet-20240229", req)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}
	// Expect text → tool_use → text blocks: prefix "Run it now:" and suffix "."
	if len(result.Content) < 3 {
		t.Fatalf("Expected at least 3 blocks, got %d: %+v", len(result.Content), result.Content)
	}
	if result.Content[0].Type != "text" || result.Content[0].Text != "Run it now:" {
		t.Errorf("unexpected first text block: %+v", result.Content[0])
	}
	if result.Content[1].Type != "tool_use" {
		t.Errorf("expected tool_use as second block, got: %+v", result.Content[1])
	}
	// Find the last text block for suffix; should be a single period
	last := result.Content[len(result.Content)-1]
	if last.Type != "text" || strings.TrimSpace(last.Text) != "." {
		t.Errorf("suffix punctuation not preserved; got: %+v", last)
	}
}

// Simplified case: single-quoted embedded tool_use with content and file_path; ensure full input preserved
func TestConvertArgoToAnthropicWithRequest_Workaround_EmbeddedSingleQuotedSimplified(t *testing.T) {
	converter := &Converter{}

	// This string simulates the content after Argo JSON decoding (first-level escapes resolved):
	// single-quoted JSON-like object embedded in content, with inner content containing newlines and double quotes.
	// Use raw string to avoid Go escaping; include backslashes before apostrophes inside the single-quoted string.
	embedded := `...{'id': 'toolu', 'input': {'content': 'package core\n\nimport (\n\t"encoding/json"\n\t"strings"\n)\n\n "\'type\': \'tool_use\'")', 'file_path': 'embed_refactored.go'}, 'name': 'Write', 'type': 'tool_use'}`

	argo := &ArgoChatResponse{
		Response: map[string]interface{}{
			"content":    embedded,
			"tool_calls": []interface{}{},
		},
	}

	req := &AnthropicRequest{Model: "claude-3-sonnet-20240229", Messages: []AnthropicMessage{{Role: core.RoleUser, Content: json.RawMessage(`"check"`)}}, Tools: []AnthropicTool{{Name: "Read"}, {Name: "Write"}, {Name: "Edit"}}}
	result := converter.ConvertArgoToAnthropicWithRequest(argo, "claude-3-sonnet-20240229", req)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	var found bool
	for _, b := range result.Content {
		if b.Type == "tool_use" && b.Name == "Write" {
			found = true
			gotContent, ok := b.Input["content"].(string)
			if !ok {
				t.Fatalf("content not a string: %T", b.Input["content"])
			}
			// Expected content with actual newlines and double quotes preserved (after normalization)
			wantContent := "package core\n\nimport (\n\t\"encoding/json\"\n\t\"strings\"\n)\n\n \"'type': 'tool_use'\")"
			if gotContent != wantContent {
				t.Errorf("content mismatch\nwant: %q\ngot:  %q", wantContent, gotContent)
			}
			fp, ok := b.Input["file_path"].(string)
			if !ok || fp != "embed_refactored.go" {
				t.Errorf("file_path mismatch; got %v", b.Input["file_path"])
			}
		}
	}
	if !found {
		t.Fatalf("tool_use Write not found; content: %+v", result.Content)
	}
}

// Full-case derived from DEBUG: ensure both content and file_path are present
func TestConvertArgoToAnthropicWithRequest_Workaround_EmbeddedWithContentAndFilePath(t *testing.T) {
	converter := &Converter{}
	argo := &ArgoChatResponse{
		Response: map[string]interface{}{
			"content": `Let me create the refactored version with the content parameter:

{'id': 'toolu_vrtx_01KTmrSzwXSRQnBJfCuSy7vv', 'input': {'content': 'package core\n\nimport (\n\t"encoding/json"\n\t"strings"\n)\n\n// EmbeddedCall represents a tool call embedded in content by Argo\n...', 'file_path': '/path/to/project/internal/core/argo_embed_refactored.go'}, 'name': 'Write', 'type': 'tool_use'}`,
			"tool_calls": []interface{}{},
		},
	}

	req := &AnthropicRequest{Model: "claude-3-sonnet-20240229", Messages: []AnthropicMessage{{Role: core.RoleUser, Content: json.RawMessage(`"please write"`)}}, Tools: []AnthropicTool{{Name: "Read"}, {Name: "Write"}, {Name: "Edit"}}}
	result := converter.ConvertArgoToAnthropicWithRequest(argo, "claude-3-sonnet-20240229", req)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	var found bool
	for _, b := range result.Content {
		if b.Type == "tool_use" && b.Name == "Write" {
			found = true
			if _, ok := b.Input["content"]; !ok {
				t.Errorf("missing content in input: %v", b.Input)
			}
			fp, ok := b.Input["file_path"].(string)
			if !ok || fp == "" {
				t.Errorf("missing/empty file_path: %v", b.Input["file_path"])
			}
		}
	}
	if !found {
		t.Fatalf("tool_use Write not found; content: %+v", result.Content)
	}
}

func TestConvertArgoToAnthropicWithRequest_Workaround_OpenAIFunctionEmbeddedInContent(t *testing.T) {
	converter := &Converter{}
	embedded := `{'id': 'call_123', 'type': 'function', 'function': {'name': 'universal_command', 'arguments': '{"command":["ls","-la"]}'}}`
	argo := &ArgoChatResponse{
		Response: map[string]interface{}{
			"content":    "I'll run a command for you:" + embedded,
			"tool_calls": []interface{}{},
		},
	}

	req := &AnthropicRequest{Model: "claude-3-sonnet-20240229", Messages: []AnthropicMessage{{Role: core.RoleUser, Content: json.RawMessage(`"do test"`)}}, Tools: []AnthropicTool{{Name: "universal_command"}}}
	result := converter.ConvertArgoToAnthropicWithRequest(argo, "claude-3-sonnet-20240229", req)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}
	if len(result.Content) < 2 {
		t.Fatalf("Expected at least 2 content blocks, got %d", len(result.Content))
	}
	if result.Content[1].Type != "tool_use" || result.Content[1].Name != "universal_command" {
		t.Errorf("Expected tool_use with name universal_command, got type=%s name=%s", result.Content[1].Type, result.Content[1].Name)
	}
	if _, ok := result.Content[1].Input["command"]; !ok {
		t.Errorf("Expected command in tool_use input, got %v", result.Content[1].Input)
	}
}

func TestConvertArgoToAnthropicWithRequest_Workaround_MultipleEmbeddedCalls(t *testing.T) {
	converter := &Converter{}
	argo := &ArgoChatResponse{
		Response: map[string]interface{}{
			"content": "Step 1: read file:{'type': 'tool_use', 'id': 'toolu_r1', 'name': 'Read', 'input': {'file_path': '/path/a'}} Next, grep it:{'type': 'tool_use', 'id': 'toolu_r2', 'name': 'Grep', 'input': {'pattern': 'foo', 'glob': '*.go'}}",
		},
	}
	req := &AnthropicRequest{Model: "claude-3-sonnet-20240229", Messages: []AnthropicMessage{{Role: core.RoleUser, Content: json.RawMessage(`"do test"`)}}, Tools: []AnthropicTool{{Name: "Read"}, {Name: "Grep"}}}
	result := converter.ConvertArgoToAnthropicWithRequest(argo, "claude-3-sonnet-20240229", req)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}
	// Expect alternating text/tool/text/tool/text blocks
	var names []string
	var textCount int
	for _, b := range result.Content {
		switch b.Type {
		case "tool_use":
			names = append(names, b.Name)
		case "text":
			textCount++
		}
	}
	if len(names) != 2 || names[0] != "Read" || names[1] != "Grep" {
		t.Errorf("Expected tool names [Read, Grep], got %v", names)
	}
	if textCount < 2 {
		t.Errorf("Expected at least 2 text blocks, got %d", textCount)
	}
}

func TestConvertAnthropicToArgo_GoogleMessages(t *testing.T) {
	mapper := NewModelMapper(&Config{
		Provider: constants.ProviderArgo,
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
					Role:    core.RoleUser,
					Content: json.RawMessage(`"Hello, how are you?"`),
				},
			},
			checkParts: false, // Argo uses string content for simple messages
		},
		{
			name: "assistant with tool_use for Google",
			messages: []AnthropicMessage{
				{
					Role:    core.RoleUser,
					Content: json.RawMessage(`"What is the weather in Paris?"`),
				},
				{
					Role: core.RoleAssistant,
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
					Role:    core.RoleUser,
					Content: json.RawMessage(`"What is the weather in Paris?"`),
				},
				{
					Role: core.RoleAssistant,
					Content: json.RawMessage(`[
						{"type": "text", "text": "I'll check the weather in Paris for you."},
						{"type": "tool_use", "id": "tool_123", "name": "get_weather", "input": {"location": "Paris"}}
					]`),
				},
				{
					Role: core.RoleUser,
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

			result, err := converter.ConvertAnthropicToArgo(context.Background(), req, "testuser")

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
		Provider: constants.ProviderArgo,
		Model:    "gpt4o",
	})
	converter := &Converter{mapper: mapper}

	// Test that OpenAI models keep their original format
	req := &AnthropicRequest{
		Model: "gpt4o",
		Messages: []AnthropicMessage{
			{
				Role:    core.RoleUser,
				Content: json.RawMessage(`"Hello"`),
			},
			{
				Role: core.RoleAssistant,
				Content: json.RawMessage(`[
					{"type": "text", "text": "Hi! I'll help you."},
					{"type": "tool_use", "id": "tool_123", "name": "get_info", "input": {"query": "test"}}
				]`),
			},
		},
	}

	result, err := converter.ConvertAnthropicToArgo(context.Background(), req, "testuser")
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
