package core

import (
	"encoding/json"
	"testing"
)

// TestToOpenAIAssistantMultipleTextBlocks tests that assistant messages with multiple text blocks
// accumulate all text instead of only keeping the last one
func TestToOpenAIAssistantMultipleTextBlocks(t *testing.T) {
	messages := []TypedMessage{
		{
			Role: "assistant",
			Blocks: []Block{
				TextBlock{Text: "First part. "},
				TextBlock{Text: "Second part. "},
				TextBlock{Text: "Third part."},
			},
		},
	}

	// Use typed conversion and marshal/unmarshal to get proper format
	typedMessages := ToOpenAITyped(messages)
	result := MarshalOpenAIMessagesForRequest(typedMessages)

	if len(result) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(result))
	}

	msg := result[0].(map[string]interface{})

	// Check that all text blocks are accumulated
	content, ok := msg["content"].(string)
	if !ok {
		t.Fatalf("Expected content to be a string, got %T", msg["content"])
	}

	expected := "First part. Second part. Third part."
	if content != expected {
		t.Errorf("Expected content '%s', got '%s'", expected, content)
	}
}

// TestToOpenAIAssistantMultimodal tests that assistant messages with multimodal content
// use array format when non-text blocks are present
func TestToOpenAIAssistantMultimodal(t *testing.T) {
	messages := []TypedMessage{
		{
			Role: "assistant",
			Blocks: []Block{
				TextBlock{Text: "Here's an image:"},
				ImageBlock{URL: "https://example.com/image.png", Detail: "high"},
				TextBlock{Text: " And some more text."},
			},
		},
	}

	// Use typed conversion and marshal/unmarshal to get proper format
	typedMessages := ToOpenAITyped(messages)
	result := MarshalOpenAIMessagesForRequest(typedMessages)

	if len(result) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(result))
	}

	msg := result[0].(map[string]interface{})

	// Check that content is an array when multimodal
	contentArray, ok := msg["content"].([]interface{})
	if !ok {
		t.Fatalf("Expected content to be an array for multimodal, got %T", msg["content"])
	}

	if len(contentArray) != 3 {
		t.Fatalf("Expected 3 content blocks, got %d", len(contentArray))
	}

	// Check first text block
	firstBlock := contentArray[0].(map[string]interface{})
	if firstBlock["type"] != "text" {
		t.Errorf("Expected first block type 'text', got %v", firstBlock["type"])
	}
	if firstBlock["text"] != "Here's an image:" {
		t.Errorf("Unexpected first block text: %v", firstBlock["text"])
	}

	// Check image block
	imageBlock := contentArray[1].(map[string]interface{})
	if imageBlock["type"] != "image_url" {
		t.Errorf("Expected second block type 'image_url', got %v", imageBlock["type"])
	}
	imageURL := imageBlock["image_url"].(map[string]interface{})
	if imageURL["url"] != "https://example.com/image.png" {
		t.Errorf("Unexpected image URL: %v", imageURL["url"])
	}
	if imageURL["detail"] != "high" {
		t.Errorf("Expected detail 'high', got %v", imageURL["detail"])
	}

	// Check second text block
	thirdBlock := contentArray[2].(map[string]interface{})
	if thirdBlock["type"] != "text" {
		t.Errorf("Expected third block type 'text', got %v", thirdBlock["type"])
	}
	if thirdBlock["text"] != " And some more text." {
		t.Errorf("Unexpected third block text: %v", thirdBlock["text"])
	}
}

// TestToOpenAIAssistantWithToolCalls tests that assistant messages with tool calls
// still preserve text content
func TestToOpenAIAssistantWithToolCalls(t *testing.T) {
	messages := []TypedMessage{
		{
			Role: "assistant",
			Blocks: []Block{
				TextBlock{Text: "I'll help you with that. "},
				TextBlock{Text: "Let me search for information."},
				ToolUseBlock{
					ID:    "tool_123",
					Name:  "search",
					Input: json.RawMessage(`{"query":"test"}`),
				},
			},
		},
	}

	// Use typed conversion and marshal/unmarshal to get proper format
	typedMessages := ToOpenAITyped(messages)
	result := MarshalOpenAIMessagesForRequest(typedMessages)

	if len(result) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(result))
	}

	msg := result[0].(map[string]interface{})

	// Check that text content is accumulated
	content, ok := msg["content"].(string)
	if !ok {
		t.Fatalf("Expected content to be a string, got %T", msg["content"])
	}

	expected := "I'll help you with that. Let me search for information."
	if content != expected {
		t.Errorf("Expected content '%s', got '%s'", expected, content)
	}

	// Check tool calls
	toolCalls, ok := msg["tool_calls"].([]OpenAIToolCall)
	if !ok {
		// Try as slice of interface{}
		if toolCallsInterface, ok2 := msg["tool_calls"].([]interface{}); ok2 {
			// Convert to expected format for testing
			if len(toolCallsInterface) != 1 {
				t.Errorf("Expected 1 tool call, got %d", len(toolCallsInterface))
			}
			return
		}
		t.Fatalf("Expected tool_calls to be present")
	}
	if len(toolCalls) != 1 {
		t.Errorf("Expected 1 tool call, got %d", len(toolCalls))
	}
}

// TestGetImageMediaType tests the media type detection for various image formats
func TestGetImageMediaType(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"https://example.com/image.png", "image/png"},
		{"https://example.com/image.PNG", "image/png"},
		{"https://example.com/photo.jpg", "image/jpeg"},
		{"https://example.com/photo.jpeg", "image/jpeg"},
		{"https://example.com/photo.JPEG", "image/jpeg"},
		{"https://example.com/animation.gif", "image/gif"},
		{"https://example.com/animation.GIF", "image/gif"},
		{"https://example.com/image.webp", "image/webp"},
		{"https://example.com/image.bmp", "image/bmp"},
		{"https://example.com/logo.svg", "image/svg+xml"},
		{"https://example.com/unknown", "image/jpeg"}, // Default
		{"https://example.com/noext", "image/jpeg"},   // Default
	}

	for _, tt := range tests {
		result := DetectImageMediaType(tt.url)
		if result != tt.expected {
			t.Errorf("For URL %s: expected %s, got %s", tt.url, tt.expected, result)
		}
	}
}

// TestToAnthropicWithImageMediaType tests that ToAnthropic uses proper media type detection
func TestToAnthropicWithImageMediaType(t *testing.T) {
	messages := []TypedMessage{
		{
			Role: "user",
			Blocks: []Block{
				TextBlock{Text: "Look at this image:"},
				ImageBlock{URL: "https://example.com/photo.png", Detail: "auto"},
			},
		},
	}

	// Use typed conversion and convert to interface{}
	typedMessages := ToAnthropicTyped(messages)
	result := make([]interface{}, 0, len(typedMessages))
	for _, msg := range typedMessages {
		msgMap := map[string]interface{}{
			"role": msg.Role,
		}

		// Handle content based on ContentUnion structure
		if msg.Content.Text != nil && *msg.Content.Text != "" {
			// Simple text content
			msgMap["content"] = *msg.Content.Text
		} else if len(msg.Content.Contents) > 0 {
			// Array of content blocks
			contentMaps := make([]map[string]interface{}, 0, len(msg.Content.Contents))
			for _, block := range msg.Content.Contents {
				blockMap := map[string]interface{}{
					"type": block.Type,
				}

				// Add fields based on block type
				switch block.Type {
				case "text":
					blockMap["text"] = block.Text
				case "image":
					if block.Source != nil {
						blockMap["source"] = map[string]interface{}{
							"type":       block.Source.Type,
							"url":        block.Source.URL,
							"media_type": block.Source.MediaType,
						}
					}
				}

				contentMaps = append(contentMaps, blockMap)
			}
			msgMap["content"] = contentMaps
		}

		result = append(result, msgMap)
	}

	if len(result) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(result))
	}

	msg := result[0].(map[string]interface{})
	content := msg["content"].([]map[string]interface{})

	if len(content) != 2 {
		t.Fatalf("Expected 2 content blocks, got %d", len(content))
	}

	// Check image block has correct media type
	imageBlock := content[1]
	if imageBlock["type"] != "image" {
		t.Errorf("Expected type 'image', got %v", imageBlock["type"])
	}

	source := imageBlock["source"].(map[string]interface{})
	if source["media_type"] != "image/png" {
		t.Errorf("Expected media_type 'image/png', got %v", source["media_type"])
	}
}

// TestToOpenAIAssistantAudioAndFile tests that assistant messages handle audio and file blocks
func TestToOpenAIAssistantAudioAndFile(t *testing.T) {
	messages := []TypedMessage{
		{
			Role: "assistant",
			Blocks: []Block{
				TextBlock{Text: "Here's the audio:"},
				AudioBlock{ID: "audio_123"},
				TextBlock{Text: " and the file:"},
				FileBlock{FileID: "file_456"},
			},
		},
	}

	// Use typed conversion and marshal/unmarshal to get proper format
	typedMessages := ToOpenAITyped(messages)
	result := MarshalOpenAIMessagesForRequest(typedMessages)

	if len(result) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(result))
	}

	msg := result[0].(map[string]interface{})

	// Should use array format due to non-text content
	contentArray, ok := msg["content"].([]interface{})
	if !ok {
		t.Fatalf("Expected content to be an array for multimodal, got %T", msg["content"])
	}

	if len(contentArray) != 4 {
		t.Fatalf("Expected 4 content blocks, got %d", len(contentArray))
	}

	// Check audio block
	audioBlock := contentArray[1].(map[string]interface{})
	if audioBlock["type"] != "input_audio" {
		t.Errorf("Expected type 'input_audio', got %v", audioBlock["type"])
	}
	inputAudio := audioBlock["input_audio"].(map[string]interface{})
	if inputAudio["id"] != "audio_123" {
		t.Errorf("Expected audio id 'audio_123', got %v", inputAudio["id"])
	}

	// Check file block
	fileBlock := contentArray[3].(map[string]interface{})
	if fileBlock["type"] != "file" {
		t.Errorf("Expected type 'file', got %v", fileBlock["type"])
	}
	file := fileBlock["file"].(map[string]interface{})
	if file["file_id"] != "file_456" {
		t.Errorf("Expected file_id 'file_456', got %v", file["file_id"])
	}
}

// TestToOpenAIAssistantEmptyContent tests handling of assistant messages with no text content
func TestToOpenAIAssistantEmptyContent(t *testing.T) {
	messages := []TypedMessage{
		{
			Role: "assistant",
			Blocks: []Block{
				ToolUseBlock{
					ID:    "tool_789",
					Name:  "calculator",
					Input: json.RawMessage(`{"operation":"add","a":1,"b":2}`),
				},
			},
		},
	}

	// Use typed conversion and marshal/unmarshal to get proper format
	typedMessages := ToOpenAITyped(messages)
	result := MarshalOpenAIMessagesForRequest(typedMessages)

	if len(result) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(result))
	}

	msg := result[0].(map[string]interface{})

	// Should not have content field when only tool calls
	if _, hasContent := msg["content"]; hasContent {
		t.Errorf("Expected no content field for tool-only message")
	}

	// Should have tool calls
	toolCalls, ok := msg["tool_calls"].([]OpenAIToolCall)
	if !ok {
		t.Errorf("Expected tool_calls to be []OpenAIToolCall, got %T", msg["tool_calls"])
	} else if len(toolCalls) != 1 {
		t.Errorf("Expected 1 tool call, got %d", len(toolCalls))
	}
}
