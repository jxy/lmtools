package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestConvertBlocksToOpenAIContent(t *testing.T) {
	tests := []struct {
		name              string
		blocks            []Block
		expectedContent   interface{}
		expectedToolCalls []OpenAIToolCall
	}{
		{
			name: "text only - single block",
			blocks: []Block{
				TextBlock{Text: "Hello, world!"},
			},
			expectedContent:   "Hello, world!",
			expectedToolCalls: []OpenAIToolCall{},
		},
		{
			name: "text only - multiple blocks",
			blocks: []Block{
				TextBlock{Text: "Hello, "},
				TextBlock{Text: "world!"},
			},
			expectedContent:   "Hello, world!",
			expectedToolCalls: []OpenAIToolCall{},
		},
		{
			name: "multimodal - text and image",
			blocks: []Block{
				TextBlock{Text: "Check out this image:"},
				ImageBlock{URL: "https://example.com/image.jpg", Detail: "high"},
			},
			expectedContent: []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "Check out this image:",
				},
				map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]interface{}{
						"url":    "https://example.com/image.jpg",
						"detail": "high",
					},
				},
			},
			expectedToolCalls: []OpenAIToolCall{},
		},
		{
			name: "multimodal - image without detail",
			blocks: []Block{
				ImageBlock{URL: "https://example.com/image.jpg"},
			},
			expectedContent: []interface{}{
				map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]interface{}{
						"url": "https://example.com/image.jpg",
					},
				},
			},
			expectedToolCalls: []OpenAIToolCall{},
		},
		{
			name: "audio block",
			blocks: []Block{
				AudioBlock{ID: "audio-123"},
			},
			expectedContent: []interface{}{
				map[string]interface{}{
					"type": "input_audio",
					"input_audio": map[string]interface{}{
						"id": "audio-123",
					},
				},
			},
			expectedToolCalls: []OpenAIToolCall{},
		},
		{
			name: "file block",
			blocks: []Block{
				FileBlock{FileID: "file-456"},
			},
			expectedContent: []interface{}{
				map[string]interface{}{
					"type": "file",
					"file": map[string]interface{}{
						"file_id": "file-456",
					},
				},
			},
			expectedToolCalls: []OpenAIToolCall{},
		},
		{
			name: "tool use only",
			blocks: []Block{
				ToolUseBlock{
					ID:    "tool-1",
					Name:  "get_weather",
					Input: json.RawMessage(`{"location":"New York"}`),
				},
			},
			expectedContent: nil,
			expectedToolCalls: []OpenAIToolCall{
				{
					ID:   "tool-1",
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      "get_weather",
						Arguments: `{"location":"New York"}`,
					},
				},
			},
		},
		{
			name: "text with tool use",
			blocks: []Block{
				TextBlock{Text: "Let me check the weather for you."},
				ToolUseBlock{
					ID:    "tool-2",
					Name:  "get_weather",
					Input: json.RawMessage(`{"location":"Paris"}`),
				},
			},
			expectedContent: "Let me check the weather for you.",
			expectedToolCalls: []OpenAIToolCall{
				{
					ID:   "tool-2",
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      "get_weather",
						Arguments: `{"location":"Paris"}`,
					},
				},
			},
		},
		{
			name: "multiple tool uses",
			blocks: []Block{
				ToolUseBlock{
					ID:    "tool-3",
					Name:  "get_weather",
					Input: json.RawMessage(`{"location":"London"}`),
				},
				ToolUseBlock{
					ID:    "tool-4",
					Name:  "get_time",
					Input: json.RawMessage(`{"timezone":"UTC"}`),
				},
			},
			expectedContent: nil,
			expectedToolCalls: []OpenAIToolCall{
				{
					ID:   "tool-3",
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      "get_weather",
						Arguments: `{"location":"London"}`,
					},
				},
				{
					ID:   "tool-4",
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      "get_time",
						Arguments: `{"timezone":"UTC"}`,
					},
				},
			},
		},
		{
			name: "tool result blocks are skipped",
			blocks: []Block{
				TextBlock{Text: "Here's the result:"},
				ToolResultBlock{
					ToolUseID: "tool-5",
					Content:   "Weather is sunny",
					IsError:   false,
				},
			},
			expectedContent:   "Here's the result:",
			expectedToolCalls: []OpenAIToolCall{},
		},
		{
			name:              "empty blocks",
			blocks:            []Block{},
			expectedContent:   nil,
			expectedToolCalls: []OpenAIToolCall{},
		},
		{
			name: "complex multimodal with everything",
			blocks: []Block{
				TextBlock{Text: "Here's a complex example:"},
				ImageBlock{URL: "https://example.com/img.jpg", Detail: "low"},
				AudioBlock{ID: "audio-789"},
				FileBlock{FileID: "file-abc"},
				ToolUseBlock{
					ID:    "tool-6",
					Name:  "analyze",
					Input: json.RawMessage(`{"data":"complex"}`),
				},
			},
			expectedContent: []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "Here's a complex example:",
				},
				map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]interface{}{
						"url":    "https://example.com/img.jpg",
						"detail": "low",
					},
				},
				map[string]interface{}{
					"type": "input_audio",
					"input_audio": map[string]interface{}{
						"id": "audio-789",
					},
				},
				map[string]interface{}{
					"type": "file",
					"file": map[string]interface{}{
						"file_id": "file-abc",
					},
				},
			},
			expectedToolCalls: []OpenAIToolCall{
				{
					ID:   "tool-6",
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      "analyze",
						Arguments: `{"data":"complex"}`,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, toolCalls := ConvertBlocksToOpenAIContent(tt.blocks)

			// Check content
			if !reflect.DeepEqual(content, tt.expectedContent) {
				t.Errorf("Content mismatch:\ngot:  %+v\nwant: %+v", content, tt.expectedContent)
			}

			// Check tool calls
			if !reflect.DeepEqual(toolCalls, tt.expectedToolCalls) {
				t.Errorf("Tool calls mismatch:\ngot:  %+v\nwant: %+v", toolCalls, tt.expectedToolCalls)
			}
		})
	}
}

func TestConvertBlocksToOpenAIContentMap(t *testing.T) {
	// Test that the Map wrapper produces the same results as the typed version
	blocks := []Block{
		TextBlock{Text: "Testing the map wrapper"},
		ToolUseBlock{
			ID:    "tool-map-1",
			Name:  "test_function",
			Input: json.RawMessage(`{"test":"value"}`),
		},
	}

	// Get typed results
	typedContent, typedToolCalls := ConvertBlocksToOpenAIContent(blocks)

	// Get map results
	mapContent, mapToolCalls := ConvertBlocksToOpenAIContentMap(blocks)

	// Content should be identical
	if !reflect.DeepEqual(typedContent, mapContent) {
		t.Errorf("Content mismatch between typed and map versions:\ntyped: %+v\nmap:   %+v", typedContent, mapContent)
	}

	// Tool calls should have the same data, just in different formats
	if len(typedToolCalls) != len(mapToolCalls) {
		t.Fatalf("Tool call count mismatch: typed=%d, map=%d", len(typedToolCalls), len(mapToolCalls))
	}

	for i, typedCall := range typedToolCalls {
		mapCall := mapToolCalls[i]

		// Check each field
		if mapCall["id"] != typedCall.ID {
			t.Errorf("Tool call ID mismatch at index %d: map=%v, typed=%v", i, mapCall["id"], typedCall.ID)
		}
		if mapCall["type"] != typedCall.Type {
			t.Errorf("Tool call Type mismatch at index %d: map=%v, typed=%v", i, mapCall["type"], typedCall.Type)
		}

		// Check function fields
		if funcMap, ok := mapCall["function"].(map[string]interface{}); ok {
			if funcMap["name"] != typedCall.Function.Name {
				t.Errorf("Function name mismatch at index %d: map=%v, typed=%v", i, funcMap["name"], typedCall.Function.Name)
			}
			if funcMap["arguments"] != typedCall.Function.Arguments {
				t.Errorf("Function arguments mismatch at index %d: map=%v, typed=%v", i, funcMap["arguments"], typedCall.Function.Arguments)
			}
		} else {
			t.Errorf("Function field is not a map at index %d", i)
		}
	}
}

// TestCompatibilityWithToOpenAI verifies that the new unified converter produces
// the same results as the original buildOpenAIContent function (now delegating to it)
func TestCompatibilityWithToOpenAI(t *testing.T) {
	// Create test messages with various content types
	messages := []TypedMessage{
		{
			Role: "user",
			Blocks: []Block{
				TextBlock{Text: "What's in this image?"},
				ImageBlock{URL: "https://example.com/test.jpg", Detail: "auto"},
			},
		},
		{
			Role: "assistant",
			Blocks: []Block{
				TextBlock{Text: "I'll analyze that for you."},
				ToolUseBlock{
					ID:    "call-123",
					Name:  "image_analyzer",
					Input: json.RawMessage(`{"url":"https://example.com/test.jpg"}`),
				},
			},
		},
		{
			Role: "user",
			Blocks: []Block{
				ToolResultBlock{
					ToolUseID: "call-123",
					Content:   "The image shows a sunset",
					IsError:   false,
				},
			},
		},
	}

	// Convert using ToOpenAITyped (which uses buildOpenAIContent that now delegates to our unified converter)
	typedOpenAIMessages := ToOpenAITyped(messages)
	openAIMessages := make([]interface{}, 0, len(typedOpenAIMessages))
	for _, msg := range typedOpenAIMessages {
		msgMap := map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		}
		if len(msg.ToolCalls) > 0 {
			msgMap["tool_calls"] = msg.ToolCalls
		}
		if msg.ToolCallID != "" {
			msgMap["tool_call_id"] = msg.ToolCallID
		}
		openAIMessages = append(openAIMessages, msgMap)
	}

	// Verify the structure is correct
	if len(openAIMessages) != 3 { // user, assistant, tool result
		t.Fatalf("Expected 3 messages, got %d", len(openAIMessages))
	}

	// Check first message (user with image)
	msg1 := openAIMessages[0].(map[string]interface{})
	if msg1["role"] != "user" {
		t.Errorf("First message role should be 'user', got %v", msg1["role"])
	}
	// Content should be an array for multimodal
	if content, ok := msg1["content"].([]interface{}); ok {
		if len(content) != 2 {
			t.Errorf("First message should have 2 content parts, got %d", len(content))
		}
	} else {
		t.Errorf("First message content should be an array, got %T", msg1["content"])
	}

	// Check second message (assistant with tool call)
	msg2 := openAIMessages[1].(map[string]interface{})
	if msg2["role"] != "assistant" {
		t.Errorf("Second message role should be 'assistant', got %v", msg2["role"])
	}
	if msg2["content"] != "I'll analyze that for you." {
		t.Errorf("Second message content incorrect: %v", msg2["content"])
	}
	if toolCalls, ok := msg2["tool_calls"].([]OpenAIToolCall); ok {
		if len(toolCalls) != 1 {
			t.Errorf("Second message should have 1 tool call, got %d", len(toolCalls))
		}
	} else {
		t.Errorf("Second message should have tool_calls field, got type %T", msg2["tool_calls"])
	}

	// Check third message (tool result)
	msg3 := openAIMessages[2].(map[string]interface{})
	if msg3["role"] != "tool" {
		t.Errorf("Third message role should be 'tool', got %v", msg3["role"])
	}
	if msg3["tool_call_id"] != "call-123" {
		t.Errorf("Third message tool_call_id incorrect: %v", msg3["tool_call_id"])
	}
	if msg3["content"] != "The image shows a sunset" {
		t.Errorf("Third message content incorrect: %v", msg3["content"])
	}
}
