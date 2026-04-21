package core

import (
	"encoding/json"
	"testing"
)

func TestParseArgoResponseWithOpenAIFormat(t *testing.T) {
	tests := []struct {
		name           string
		response       string
		expectedText   string
		expectedTools  int
		expectedErr    bool
		checkToolNames []string
		checkToolIDs   []string
	}{
		{
			name: "OpenAI format with single tool call",
			response: `{
				"response": {
					"content": "I'll list the files in the current directory for you.",
					"tool_calls": [
						{
							"id": "call_WV7RJKHOcXXZw4PzB4XuoPUQ",
							"type": "function",
							"function": {
								"name": "universal_command",
								"arguments": "{\"command\":[\"ls\",\"-la\"]}"
							}
						}
					]
				}
			}`,
			expectedText:   "I'll list the files in the current directory for you.",
			expectedTools:  1,
			expectedErr:    false,
			checkToolNames: []string{"universal_command"},
			checkToolIDs:   []string{"call_WV7RJKHOcXXZw4PzB4XuoPUQ"},
		},
		{
			name: "OpenAI format with multiple tool calls",
			response: `{
				"response": {
					"content": null,
					"tool_calls": [
						{
							"id": "call_123",
							"type": "function",
							"function": {
								"name": "get_weather",
								"arguments": "{\"location\":\"Paris\"}"
							}
						},
						{
							"id": "call_456",
							"type": "function",
							"function": {
								"name": "get_time",
								"arguments": "{\"timezone\":\"UTC\"}"
							}
						}
					]
				}
			}`,
			expectedText:   "",
			expectedTools:  2,
			expectedErr:    false,
			checkToolNames: []string{"get_weather", "get_time"},
			checkToolIDs:   []string{"call_123", "call_456"},
		},
		{
			name: "Mixed format - OpenAI tool call with assistant content",
			response: `{
				"response": {
					"content": "Let me check that for you.",
					"tool_calls": [
						{
							"id": "call_ABC123",
							"type": "function",
							"function": {
								"name": "universal_command",
								"arguments": "{\"command\":[\"bash\",\"-lc\",\"pwd\"]}"
							}
						}
					]
				}
			}`,
			expectedText:   "Let me check that for you.",
			expectedTools:  1,
			expectedErr:    false,
			checkToolNames: []string{"universal_command"},
			checkToolIDs:   []string{"call_ABC123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, tools, err := parseArgoResponseWithTools([]byte(tt.response), false)

			if tt.expectedErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if text != tt.expectedText {
				t.Errorf("Expected text='%s', got '%s'", tt.expectedText, text)
			}

			if len(tools) != tt.expectedTools {
				t.Errorf("Expected %d tools, got %d", tt.expectedTools, len(tools))
			}

			// Verify tool details
			for i, tool := range tools {
				if i < len(tt.checkToolNames) {
					if tool.Name != tt.checkToolNames[i] {
						t.Errorf("Tool %d: expected name='%s', got '%s'", i, tt.checkToolNames[i], tool.Name)
					}
				}

				if i < len(tt.checkToolIDs) {
					if tool.ID != tt.checkToolIDs[i] {
						t.Errorf("Tool %d: expected ID='%s', got '%s'", i, tt.checkToolIDs[i], tool.ID)
					}
				}

				// Verify assistant content is captured
				if tt.expectedText != "" && tool.AssistantContent != tt.expectedText {
					t.Errorf("Tool %d: expected AssistantContent='%s', got '%s'", i, tt.expectedText, tool.AssistantContent)
				}

				// Verify args are properly captured
				if len(tool.Args) == 0 {
					t.Errorf("Tool %d: Args should not be empty", i)
				}

				// For OpenAI format, args should be valid JSON
				var args map[string]interface{}
				if err := json.Unmarshal(tool.Args, &args); err != nil {
					t.Errorf("Tool %d: Failed to unmarshal args: %v", i, err)
				}
			}
		})
	}
}

func TestBuildArgoToolResultRequestOpenAIFormat(t *testing.T) {
	// Set up test config
	cfg := &TestRequestConfig{
		User:  "testuser",
		Model: "gpt5",
		Env:   "dev",
	}

	// Test tool results are now embedded in typedMessages

	// Create typed messages from the accumulated messages
	typedMessages := []TypedMessage{
		NewTextMessage("system", "You are a helpful assistant."),
		NewTextMessage("user", "List the files in the current directory"),
		{
			Role: "assistant",
			Blocks: []Block{
				TextBlock{Text: "I'll list the files for you"},
				ToolUseBlock{
					ID:    "call_123",
					Name:  "universal_command",
					Input: json.RawMessage(`{"command": ["ls", "-la"]}`),
				},
			},
		},
		{
			Role: "user",
			Blocks: []Block{
				ToolResultBlock{
					ToolUseID: "call_123",
					Content:   "file1.txt\nfile2.txt\nfile3.txt",
				},
			},
		},
	}

	// Tool definitions are now passed directly to request builders
	model := "gpt5"

	// Build the request
	req, body, err := BuildToolResultRequest(cfg, model, "You are a helpful assistant.", nil, typedMessages)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if req == nil {
		t.Fatal("Expected non-nil request")
	}

	// Parse the request body
	var parsedReq map[string]interface{}
	if err := json.Unmarshal(body, &parsedReq); err != nil {
		t.Fatalf("Failed to unmarshal request: %v", err)
	}

	// Check basic fields
	if parsedReq["model"] != "gpt5" {
		t.Errorf("Expected model='gpt5', got '%v'", parsedReq["model"])
	}
	if _, ok := parsedReq["user"]; ok {
		t.Errorf("OpenAI-format Argo request should not include user field, got %v", parsedReq["user"])
	}
	if req.URL.String() != "https://apps-dev.inside.anl.gov/argoapi/v1/chat/completions" {
		t.Errorf("Unexpected URL: %s", req.URL.String())
	}

	// Check messages structure
	messages, ok := parsedReq["messages"].([]interface{})
	if !ok {
		t.Fatal("Expected messages to be an array")
	}

	// Should have: system, user, assistant (with tool_calls), tool result
	if len(messages) != 4 {
		t.Errorf("Expected 4 messages, got %d", len(messages))
	}

	// Check assistant message has tool_calls
	if len(messages) >= 3 {
		assistantMsg, ok := messages[2].(map[string]interface{})
		if !ok {
			t.Error("Expected assistant message to be a map")
		} else {
			// Check for tool_calls array
			toolCalls, ok := assistantMsg["tool_calls"].([]interface{})
			if !ok {
				t.Error("Expected assistant message to have tool_calls array")
			} else if len(toolCalls) != 1 {
				t.Errorf("Expected 1 tool call, got %d", len(toolCalls))
			} else {
				// Check tool call structure
				tc, ok := toolCalls[0].(map[string]interface{})
				if !ok {
					t.Error("Expected tool call to be a map")
				} else {
					if tc["id"] != "call_123" {
						t.Errorf("Expected tool call id='call_123', got '%v'", tc["id"])
					}
					if tc["type"] != "function" {
						t.Errorf("Expected tool call type='function', got '%v'", tc["type"])
					}
					fn, ok := tc["function"].(map[string]interface{})
					if !ok {
						t.Error("Expected function field to be a map")
					} else {
						if fn["name"] != "universal_command" {
							t.Errorf("Expected function name='universal_command', got '%v'", fn["name"])
						}
					}
				}
			}
		}
	}

	// Check tool result message
	if len(messages) >= 4 {
		toolMsg, ok := messages[3].(map[string]interface{})
		if !ok {
			t.Error("Expected tool result message to be a map")
		} else {
			if toolMsg["role"] != "tool" {
				t.Errorf("Expected role='tool', got '%v'", toolMsg["role"])
			}
			if toolMsg["tool_call_id"] != "call_123" {
				t.Errorf("Expected tool_call_id='call_123', got '%v'", toolMsg["tool_call_id"])
			}
			if toolMsg["content"] != "file1.txt\nfile2.txt\nfile3.txt" {
				t.Errorf("Expected tool result content, got '%v'", toolMsg["content"])
			}
		}
	}
}

func TestBuildArgoToolResultRequestAnthropicFormat(t *testing.T) {
	// Set up test config for Claude model
	cfg := &TestRequestConfig{
		User:  "testuser",
		Model: "claudesonnet4",
		Env:   "dev",
	}

	// Test tool results are now embedded in typedMessages

	// Create typed messages from the accumulated messages
	typedMessages := []TypedMessage{
		NewTextMessage("system", "You are a brilliant assistant."),
		NewTextMessage("user", "List the files in the current directory"),
		{
			Role: "assistant",
			Blocks: []Block{
				TextBlock{Text: "I'll list the files in the current directory for you."},
				ToolUseBlock{
					ID:    "toolu_vrtx_01ABC",
					Name:  "universal_command",
					Input: json.RawMessage(`{"command": ["ls", "-la"]}`),
				},
			},
		},
		{
			Role: "user",
			Blocks: []Block{
				ToolResultBlock{
					ToolUseID: "toolu_vrtx_01ABC",
					Content:   "total 546\ndrwxr-xr-x  11 user user  20 Aug 22 21:48 .",
				},
			},
		},
	}

	// Tool definitions are now passed directly to request builders
	model := "claudesonnet4"

	// Build the request
	req, body, err := BuildToolResultRequest(cfg, model, "You are a helpful assistant.", nil, typedMessages)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if req == nil {
		t.Fatal("Expected non-nil request")
	}

	// Parse the request body
	var parsedReq map[string]interface{}
	if err := json.Unmarshal(body, &parsedReq); err != nil {
		t.Fatalf("Failed to unmarshal request: %v", err)
	}

	if parsedReq["system"] != "You are a helpful assistant." {
		t.Errorf("Expected top-level system override, got %v", parsedReq["system"])
	}
	if req.URL.String() != "https://apps-dev.inside.anl.gov/argoapi/v1/messages" {
		t.Errorf("Unexpected URL: %s", req.URL.String())
	}

	// Check messages structure
	messages, ok := parsedReq["messages"].([]interface{})
	if !ok {
		t.Fatal("Expected messages to be an array")
	}

	// Anthropic keeps the system prompt out of band.
	if len(messages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(messages))
	}

	// Check assistant message has content blocks
	if len(messages) >= 2 {
		assistantMsg, ok := messages[1].(map[string]interface{})
		if !ok {
			t.Error("Expected assistant message to be a map")
		} else {
			if assistantMsg["role"] != "assistant" {
				t.Errorf("Expected role='assistant', got '%v'", assistantMsg["role"])
			}
			// Check for content array
			content, ok := assistantMsg["content"].([]interface{})
			if !ok {
				t.Error("Expected assistant message to have content array")
			} else {
				// Should have text block and tool_use block
				if len(content) < 2 {
					t.Errorf("Expected at least 2 content blocks, got %d", len(content))
				}

				// Check text block
				if len(content) > 0 {
					textBlock, ok := content[0].(map[string]interface{})
					if !ok {
						t.Error("Expected text block to be a map")
					} else {
						if textBlock["type"] != "text" {
							t.Errorf("Expected type='text', got '%v'", textBlock["type"])
						}
						if textBlock["text"] != "I'll list the files in the current directory for you." {
							t.Errorf("Unexpected text content: %v", textBlock["text"])
						}
					}
				}

				// Check tool_use block
				if len(content) > 1 {
					toolBlock, ok := content[1].(map[string]interface{})
					if !ok {
						t.Error("Expected tool_use block to be a map")
					} else {
						if toolBlock["type"] != "tool_use" {
							t.Errorf("Expected type='tool_use', got '%v'", toolBlock["type"])
						}
						if toolBlock["id"] != "toolu_vrtx_01ABC" {
							t.Errorf("Expected id='toolu_vrtx_01ABC', got '%v'", toolBlock["id"])
						}
						if toolBlock["name"] != "universal_command" {
							t.Errorf("Expected name='universal_command', got '%v'", toolBlock["name"])
						}
					}
				}
			}
		}
	}

	// Check user message with tool_result
	if len(messages) >= 3 {
		userMsg, ok := messages[2].(map[string]interface{})
		if !ok {
			t.Error("Expected user message to be a map")
		} else {
			if userMsg["role"] != "user" {
				t.Errorf("Expected role='user', got '%v'", userMsg["role"])
			}

			// Check for content array with tool_result
			content, ok := userMsg["content"].([]interface{})
			if !ok {
				t.Error("Expected user message to have content array")
			} else if len(content) != 1 {
				t.Errorf("Expected 1 content block, got %d", len(content))
			} else {
				toolResult, ok := content[0].(map[string]interface{})
				if !ok {
					t.Error("Expected tool_result block to be a map")
				} else {
					if toolResult["type"] != "tool_result" {
						t.Errorf("Expected type='tool_result', got '%v'", toolResult["type"])
					}
					if toolResult["tool_use_id"] != "toolu_vrtx_01ABC" {
						t.Errorf("Expected tool_use_id='toolu_vrtx_01ABC', got '%v'", toolResult["tool_use_id"])
					}
				}
			}
		}
	}
}
