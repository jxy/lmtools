package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestFileBlockRoundTrip tests that file blocks can be converted to Anthropic format and back without loss
func TestFileBlockRoundTrip(t *testing.T) {
	// Create original TypedMessage with FileBlock
	original := []TypedMessage{
		{
			Role: "user",
			Blocks: []Block{
				TextBlock{Text: "Here is a file:"},
				FileBlock{FileID: "file-123"},
				TextBlock{Text: "Please analyze it."},
			},
		},
	}

	// Convert to Anthropic format
	anthropic := ToAnthropicTyped(original)

	// Verify the file block was properly converted
	if len(anthropic) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(anthropic))
	}

	// Check that content is an array
	contentArray, ok := anthropic[0].Content.([]AnthropicContent)
	if !ok {
		t.Fatalf("Expected content to be []AnthropicContent, got %T", anthropic[0].Content)
	}

	// Verify we have 3 blocks
	if len(contentArray) != 3 {
		t.Fatalf("Expected 3 content blocks, got %d", len(contentArray))
	}

	// Verify the file block
	fileBlock := contentArray[1]
	if fileBlock.Type != "file" {
		t.Errorf("Expected file block type, got %s", fileBlock.Type)
	}
	if fileBlock.File == nil {
		t.Fatal("Expected file block to have File field")
	}
	if fileID, ok := fileBlock.File["file_id"].(string); !ok || fileID != "file-123" {
		t.Errorf("Expected file_id to be 'file-123', got %v", fileBlock.File["file_id"])
	}

	// Convert back to TypedMessage
	roundTrip := FromAnthropicTyped(anthropic)

	// Verify the round trip preserved the structure
	if len(roundTrip) != len(original) {
		t.Fatalf("Round trip changed message count: %d -> %d", len(original), len(roundTrip))
	}

	if len(roundTrip[0].Blocks) != len(original[0].Blocks) {
		t.Fatalf("Round trip changed block count: %d -> %d",
			len(original[0].Blocks), len(roundTrip[0].Blocks))
	}

	// Check each block
	for i, block := range roundTrip[0].Blocks {
		originalBlock := original[0].Blocks[i]
		if !reflect.DeepEqual(block, originalBlock) {
			t.Errorf("Block %d differs after round trip:\nOriginal: %+v\nRoundTrip: %+v",
				i, originalBlock, block)
		}
	}
}

// TestFileBlockInMixedContent tests file blocks work correctly with other content types
func TestFileBlockInMixedContent(t *testing.T) {
	original := []TypedMessage{
		{
			Role: "assistant",
			Blocks: []Block{
				TextBlock{Text: "I'll analyze these files:"},
				FileBlock{FileID: "doc-1"},
				ImageBlock{URL: "https://example.com/image.png", Detail: "high"},
				FileBlock{FileID: "doc-2"},
				AudioBlock{ID: "audio-1", Format: "mp3"},
			},
		},
	}

	// Convert to Anthropic and back
	anthropic := ToAnthropicTyped(original)
	roundTrip := FromAnthropicTyped(anthropic)

	// Verify all blocks are preserved
	if len(roundTrip) != 1 || len(roundTrip[0].Blocks) != 5 {
		t.Fatalf("Block count changed: expected 5, got %d", len(roundTrip[0].Blocks))
	}

	// Check specific file blocks
	if file1, ok := roundTrip[0].Blocks[1].(FileBlock); !ok || file1.FileID != "doc-1" {
		t.Errorf("First file block not preserved correctly: %+v", roundTrip[0].Blocks[1])
	}
	if file2, ok := roundTrip[0].Blocks[3].(FileBlock); !ok || file2.FileID != "doc-2" {
		t.Errorf("Second file block not preserved correctly: %+v", roundTrip[0].Blocks[3])
	}
}

// TestEmptyFileID tests handling of empty file IDs
func TestEmptyFileID(t *testing.T) {
	original := []TypedMessage{
		{
			Role: "user",
			Blocks: []Block{
				FileBlock{FileID: ""}, // Empty file ID
			},
		},
	}

	anthropic := ToAnthropicTyped(original)

	// Verify the file block is still created (even with empty ID)
	contentArray, ok := anthropic[0].Content.([]AnthropicContent)
	if !ok {
		t.Fatalf("Expected content array, got %T", anthropic[0].Content)
	}

	if len(contentArray) != 1 {
		t.Fatalf("Expected 1 content block, got %d", len(contentArray))
	}

	if contentArray[0].Type != "file" {
		t.Errorf("Expected file type, got %s", contentArray[0].Type)
	}
}

// TestFileBlockToOpenAI tests that file blocks are properly converted to OpenAI format
func TestFileBlockToOpenAI(t *testing.T) {
	original := []TypedMessage{
		{
			Role: "user",
			Blocks: []Block{
				TextBlock{Text: "Analyze this:"},
				FileBlock{FileID: "file-xyz"},
			},
		},
	}

	// Convert to OpenAI format
	openai := ToOpenAITyped(original)

	if len(openai) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(openai))
	}

	// OpenAI should have array content when there's a file block
	contentArray, ok := openai[0].Content.([]interface{})
	if !ok {
		t.Fatalf("Expected content array for multimodal content, got %T", openai[0].Content)
	}

	if len(contentArray) != 2 {
		t.Fatalf("Expected 2 content items, got %d", len(contentArray))
	}

	// Check the file block
	fileContent, ok := contentArray[1].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map for file content, got %T", contentArray[1])
	}

	if fileContent["type"] != "file" {
		t.Errorf("Expected type 'file', got %v", fileContent["type"])
	}

	fileData, ok := fileContent["file"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected file data map, got %T", fileContent["file"])
	}

	if fileData["file_id"] != "file-xyz" {
		t.Errorf("Expected file_id 'file-xyz', got %v", fileData["file_id"])
	}
}

// TestAnthropicFileBlockParsing tests parsing of Anthropic file blocks
func TestAnthropicFileBlockParsing(t *testing.T) {
	// Create Anthropic message with file block directly using the typed structure
	// This simulates how the message would be created in actual usage
	anthMsg := AnthropicMessage{
		Role: "user",
		Content: []AnthropicContent{
			{
				Type: "text",
				Text: "Here's a document:",
			},
			{
				Type: "file",
				File: map[string]interface{}{
					"file_id": "important-doc",
				},
			},
		},
	}

	// Convert to TypedMessage directly (without JSON marshaling)
	// This tests the actual conversion logic
	typed := FromAnthropicTyped([]AnthropicMessage{anthMsg})

	if len(typed) != 1 {
		t.Fatalf("Expected 1 message, got %d messages", len(typed))
	}
	if len(typed[0].Blocks) != 2 {
		t.Fatalf("Expected 2 blocks, got %d blocks", len(typed[0].Blocks))
	}

	// Verify file block
	if fileBlock, ok := typed[0].Blocks[1].(FileBlock); !ok {
		t.Errorf("Expected FileBlock, got %T", typed[0].Blocks[1])
	} else if fileBlock.FileID != "important-doc" {
		t.Errorf("Expected file ID 'important-doc', got %s", fileBlock.FileID)
	}

	// Also test JSON round-trip with proper type handling
	// When Content is marshaled as an array, it needs special handling
	jsonData := `{
		"role": "user",
		"content": [
			{"type": "text", "text": "Here's a document:"},
			{"type": "file", "file": {"file_id": "important-doc"}}
		]
	}`

	var parsedFromJSON AnthropicMessage
	if err := json.Unmarshal([]byte(jsonData), &parsedFromJSON); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	// The Content field will be []interface{} after unmarshaling
	// We need to convert it to []AnthropicContent
	if contentArray, ok := parsedFromJSON.Content.([]interface{}); ok {
		anthContents := make([]AnthropicContent, 0, len(contentArray))
		for _, item := range contentArray {
			if blockMap, ok := item.(map[string]interface{}); ok {
				ac := AnthropicContent{}
				if typeStr, ok := blockMap["type"].(string); ok {
					ac.Type = typeStr
				}
				if text, ok := blockMap["text"].(string); ok {
					ac.Text = text
				}
				if file, ok := blockMap["file"].(map[string]interface{}); ok {
					ac.File = file
				}
				anthContents = append(anthContents, ac)
			}
		}
		parsedFromJSON.Content = anthContents
	}

	// Now convert the parsed message
	typedFromJSON := FromAnthropicTyped([]AnthropicMessage{parsedFromJSON})

	if len(typedFromJSON) != 1 {
		t.Fatalf("Expected 1 message from JSON, got %d messages", len(typedFromJSON))
	}
	if len(typedFromJSON[0].Blocks) != 2 {
		t.Fatalf("Expected 2 blocks from JSON, got %d blocks", len(typedFromJSON[0].Blocks))
	}

	// Verify file block from JSON parsing
	if fileBlock, ok := typedFromJSON[0].Blocks[1].(FileBlock); !ok {
		t.Errorf("Expected FileBlock from JSON, got %T", typedFromJSON[0].Blocks[1])
	} else if fileBlock.FileID != "important-doc" {
		t.Errorf("Expected file ID 'important-doc' from JSON, got %s", fileBlock.FileID)
	}
}
