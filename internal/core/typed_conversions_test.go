package core

import (
	"encoding/json"
	"reflect"
	"strings"
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
	if len(anthropic[0].Content.Contents) == 0 {
		t.Fatalf("Expected content to be an array")
	}

	contentArray := anthropic[0].Content.Contents

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
	if fileBlock.File.FileID != "file-123" {
		t.Errorf("Expected file ID to be 'file-123', got %v", fileBlock.File.FileID)
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
	if len(anthropic[0].Content.Contents) == 0 {
		t.Fatalf("Expected content to be an array")
	}

	contentArray := anthropic[0].Content.Contents

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
	if len(openai[0].Content.Contents) == 0 {
		t.Fatalf("Expected content to be an array for multimodal content")
	}

	contentArray := openai[0].Content.Contents

	if len(contentArray) != 2 {
		t.Fatalf("Expected 2 content items, got %d", len(contentArray))
	}

	// Check the file block
	fileContent := contentArray[1]
	if fileContent.Type != "file" {
		t.Errorf("Expected type 'file', got %v", fileContent.Type)
	}

	if fileContent.File == nil {
		t.Fatal("Expected file data to be present")
	}

	// The FileBlock.FileID should map to file_id in OpenAI format
	// Note: OpenAI uses file_id, not name, for file references
	if fileContent.File.FileID != "file-xyz" {
		t.Errorf("Expected file ID 'file-xyz', got %v", fileContent.File.FileID)
	}
}

// TestAnthropicFileBlockParsing tests parsing of Anthropic file blocks
func TestAnthropicFileBlockParsing(t *testing.T) {
	// Create Anthropic message with file block directly using the typed structure
	// This simulates how the message would be created in actual usage
	anthMsg := AnthropicMessage{
		Role: "user",
		Content: AnthropicContentUnion{
			Contents: []AnthropicContent{
				{
					Type: "text",
					Text: "Here's a document:",
				},
				{
					Type: "file",
					File: &FileData{
						FileID: "important-doc",
					},
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

	// The Content field will be unmarshaled as interface{}
	// We need to handle the conversion properly
	var rawContent interface{}
	if err := json.Unmarshal([]byte(`[{"type":"text","text":"JSON test"},{"type":"file","file":{"file_id":"json-file"}}]`), &rawContent); err != nil {
		t.Fatalf("Failed to unmarshal content: %v", err)
	}

	// Convert the raw content to AnthropicContentUnion
	if contentArray, ok := rawContent.([]interface{}); ok {
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
					ac.File = &FileData{
						FileID: file["file_id"].(string),
					}
				}
				anthContents = append(anthContents, ac)
			}
		}
		parsedFromJSON.Content = AnthropicContentUnion{
			Contents: anthContents,
		}
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
	} else if fileBlock.FileID != "json-file" {
		t.Errorf("Expected file ID 'json-file' from JSON, got %s", fileBlock.FileID)
	}
}

// TestContentUnionMarshalFails tests that ContentUnion types intentionally fail direct marshaling
// This is a regression test to ensure the architectural decision is enforced
func TestContentUnionMarshalFails(t *testing.T) {
	t.Run("AnthropicContentUnion direct marshal fails", func(t *testing.T) {
		union := AnthropicContentUnion{
			Text: stringPtr("test text"),
		}
		_, err := json.Marshal(union)
		if err == nil {
			t.Error("Expected AnthropicContentUnion.MarshalJSON to return error, but it succeeded")
		}
		// The error is wrapped by json package, so check for the key part
		if !contains(err.Error(), "AnthropicContentUnion must not be marshaled directly") {
			t.Errorf("Expected error about direct marshaling, got: %v", err)
		}
	})

	t.Run("OpenAIContentUnion direct marshal fails", func(t *testing.T) {
		union := OpenAIContentUnion{
			Text: stringPtr("test text"),
		}
		_, err := json.Marshal(union)
		if err == nil {
			t.Error("Expected OpenAIContentUnion.MarshalJSON to return error, but it succeeded")
		}
		// The error is wrapped by json package, so check for the key part
		if !contains(err.Error(), "OpenAIContentUnion must not be marshaled directly") {
			t.Errorf("Expected error about direct marshaling, got: %v", err)
		}
	})

	t.Run("MarshalAnthropicMessagesForRequest works correctly", func(t *testing.T) {
		messages := []AnthropicMessage{
			{
				Role: "user",
				Content: AnthropicContentUnion{
					Text: stringPtr("Hello, world!"),
				},
			},
		}

		payload := MarshalAnthropicMessagesForRequest(messages)
		if payload == nil {
			t.Fatal("Expected non-nil payload from MarshalAnthropicMessagesForRequest")
		}

		// Verify it can be marshaled to JSON
		jsonData, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("Failed to marshal payload: %v", err)
		}

		// Parse back to verify content (field order doesn't matter in JSON)
		var parsed []map[string]interface{}
		if err := json.Unmarshal(jsonData, &parsed); err != nil {
			t.Fatalf("Failed to parse JSON: %v", err)
		}

		if len(parsed) != 1 {
			t.Fatalf("Expected 1 message, got %d", len(parsed))
		}

		if parsed[0]["role"] != "user" {
			t.Errorf("Expected role 'user', got %v", parsed[0]["role"])
		}

		if parsed[0]["content"] != "Hello, world!" {
			t.Errorf("Expected content 'Hello, world!', got %v", parsed[0]["content"])
		}
	})

	t.Run("MarshalOpenAIMessagesForRequest works correctly", func(t *testing.T) {
		messages := []OpenAIMessage{
			{
				Role: "user",
				Content: OpenAIContentUnion{
					Text: stringPtr("Hello, OpenAI!"),
				},
			},
		}

		payload := MarshalOpenAIMessagesForRequest(messages)
		if payload == nil {
			t.Fatal("Expected non-nil payload from MarshalOpenAIMessagesForRequest")
		}

		// Verify it can be marshaled to JSON
		jsonData, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("Failed to marshal payload: %v", err)
		}

		// Parse back to verify content (field order doesn't matter in JSON)
		var parsed []map[string]interface{}
		if err := json.Unmarshal(jsonData, &parsed); err != nil {
			t.Fatalf("Failed to parse JSON: %v", err)
		}

		if len(parsed) != 1 {
			t.Fatalf("Expected 1 message, got %d", len(parsed))
		}

		if parsed[0]["role"] != "user" {
			t.Errorf("Expected role 'user', got %v", parsed[0]["role"])
		}

		if parsed[0]["content"] != "Hello, OpenAI!" {
			t.Errorf("Expected content 'Hello, OpenAI!', got %v", parsed[0]["content"])
		}
	})

	t.Run("Complex content with arrays marshals correctly", func(t *testing.T) {
		messages := []AnthropicMessage{
			{
				Role: "user",
				Content: AnthropicContentUnion{
					Contents: []AnthropicContent{
						{Type: "text", Text: "First text"},
						{Type: "text", Text: "Second text"},
					},
				},
			},
		}

		payload := MarshalAnthropicMessagesForRequest(messages)
		jsonData, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("Failed to marshal complex content: %v", err)
		}

		// Parse back to verify structure
		var parsed []map[string]interface{}
		if err := json.Unmarshal(jsonData, &parsed); err != nil {
			t.Fatalf("Failed to parse JSON: %v", err)
		}

		if len(parsed) != 1 {
			t.Fatalf("Expected 1 message, got %d", len(parsed))
		}

		// Check the content is an array
		content, ok := parsed[0]["content"].([]interface{})
		if !ok {
			t.Fatalf("Expected content to be an array, got %T", parsed[0]["content"])
		}

		if len(content) != 2 {
			t.Fatalf("Expected 2 content blocks, got %d", len(content))
		}

		// Verify first block
		firstBlock, ok := content[0].(map[string]interface{})
		if !ok {
			t.Fatalf("Expected first block to be a map, got %T", content[0])
		}
		if firstBlock["type"] != "text" || firstBlock["text"] != "First text" {
			t.Errorf("First block incorrect: %v", firstBlock)
		}

		// Verify second block
		secondBlock, ok := content[1].(map[string]interface{})
		if !ok {
			t.Fatalf("Expected second block to be a map, got %T", content[1])
		}
		if secondBlock["type"] != "text" || secondBlock["text"] != "Second text" {
			t.Errorf("Second block incorrect: %v", secondBlock)
		}
	})
}

func TestMarshalAnthropicMessagesForRequestReplaysRawRedactedThinking(t *testing.T) {
	messages := ToAnthropicTyped([]TypedMessage{{
		Role: string(RoleAssistant),
		Blocks: []Block{
			ReasoningBlock{
				Provider: "anthropic",
				Type:     "redacted_thinking",
				Raw:      json.RawMessage(`{"type":"redacted_thinking","data":"redacted-payload"}`),
			},
			TextBlock{Text: "visible"},
		},
	}})
	payload := MarshalAnthropicMessagesForRequest(messages)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var parsed []map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	content, ok := parsed[0]["content"].([]interface{})
	if !ok || len(content) != 2 {
		t.Fatalf("content = %#v, want two blocks", parsed[0]["content"])
	}
	reasoning, ok := content[0].(map[string]interface{})
	if !ok {
		t.Fatalf("reasoning block = %T", content[0])
	}
	if reasoning["type"] != "redacted_thinking" || reasoning["data"] != "redacted-payload" {
		t.Fatalf("reasoning block = %#v, want redacted data replayed", reasoning)
	}
	if _, exists := reasoning["thinking"]; exists {
		t.Fatalf("reasoning block includes reconstructed thinking: %#v", reasoning)
	}
}

func TestMarshalAnthropicMessagesForRequestDropsSyntheticThinkingText(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want map[string]interface{}
	}{
		{
			name: "thinking",
			raw:  json.RawMessage(`{"type":"thinking","text":"","thinking":"hidden","signature":"sig_hidden"}`),
			want: map[string]interface{}{
				"type":      "thinking",
				"thinking":  "hidden",
				"signature": "sig_hidden",
			},
		},
		{
			name: "redacted_thinking",
			raw:  json.RawMessage(`{"type":"redacted_thinking","text":"","data":"redacted-payload"}`),
			want: map[string]interface{}{
				"type": "redacted_thinking",
				"data": "redacted-payload",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := ToAnthropicTyped([]TypedMessage{{
				Role: string(RoleAssistant),
				Blocks: []Block{
					ReasoningBlock{
						Provider: "anthropic",
						Type:     tt.name,
						Raw:      tt.raw,
					},
					TextBlock{Text: "visible"},
				},
			}})
			payload := MarshalAnthropicMessagesForRequest(messages)
			data, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}

			var parsed []map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			content, ok := parsed[0]["content"].([]interface{})
			if !ok || len(content) != 2 {
				t.Fatalf("content = %#v, want two blocks", parsed[0]["content"])
			}
			reasoning, ok := content[0].(map[string]interface{})
			if !ok {
				t.Fatalf("reasoning block = %T", content[0])
			}
			if _, exists := reasoning["text"]; exists {
				t.Fatalf("reasoning block includes synthetic text: %#v", reasoning)
			}
			for key, value := range tt.want {
				if reasoning[key] != value {
					t.Fatalf("reasoning[%q] = %#v, want %#v in %#v", key, reasoning[key], value, reasoning)
				}
			}
		})
	}
}

func TestToAnthropicTypedProjectsOpenAIReasoningSummaryToText(t *testing.T) {
	messages := ToAnthropicTyped([]TypedMessage{{
		Role: string(RoleAssistant),
		Blocks: []Block{
			ReasoningBlock{
				Provider: "openai",
				Type:     "reasoning",
				Summary:  json.RawMessage(`[{"type":"summary_text","text":"Checked the account status."}]`),
			},
		},
	}})
	payload := MarshalAnthropicMessagesForRequest(messages)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var parsed []map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	content, ok := parsed[0]["content"].([]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v, want one text block", parsed[0]["content"])
	}
	block, ok := content[0].(map[string]interface{})
	if !ok || block["type"] != "text" || block["text"] != "Checked the account status." {
		t.Fatalf("reasoning projection = %#v, want text block", content[0])
	}
}

func TestToAnthropicTypedDropsOpaqueReasoningOnlyMessages(t *testing.T) {
	messages := ToAnthropicTyped([]TypedMessage{{
		Role: string(RoleAssistant),
		Blocks: []Block{
			ReasoningBlock{
				Provider:         "openai",
				Type:             "reasoning",
				EncryptedContent: "enc_1",
			},
		},
	}})
	if len(messages) != 0 {
		t.Fatalf("len(messages) = %d, want opaque reasoning-only message dropped: %#v", len(messages), messages)
	}
}

func TestToAnthropicTypedDropsUnsignedAnthropicThinking(t *testing.T) {
	messages := ToAnthropicTyped([]TypedMessage{{
		Role: string(RoleAssistant),
		Blocks: []Block{
			ReasoningBlock{
				Provider: "anthropic",
				Type:     "thinking",
				Text:     "private chain of thought without a signature",
			},
		},
	}})
	if len(messages) != 0 {
		t.Fatalf("len(messages) = %d, want unsigned thinking-only message dropped: %#v", len(messages), messages)
	}
}

func TestConvertToolsTypedDefaultsEmptySchema(t *testing.T) {
	tools := []ToolDefinition{
		{
			Name:        "run",
			Description: "Run a command",
		},
	}

	openAITools := ConvertToolsToOpenAITyped(tools)
	anthropicTools := ConvertToolsToAnthropicTyped(tools)
	googleTools := ConvertToolsToGoogleTyped(tools)

	want := `{"type":"object","properties":{}}`

	if got := string(openAITools[0].Function.Parameters); got != want {
		t.Fatalf("OpenAI schema = %s, want %s", got, want)
	}
	if got := string(anthropicTools[0].InputSchema); got != want {
		t.Fatalf("Anthropic schema = %s, want %s", got, want)
	}

	if got := string(googleTools[0].FunctionDeclarations[0].Parameters); got != want {
		t.Fatalf("Google schema = %s, want %s", got, want)
	}
}

func TestConvertToolsToAnthropicTypedWrapsCustomTools(t *testing.T) {
	tools := []ToolDefinition{{
		Type:        "custom",
		Name:        "apply_patch",
		Description: "Apply a patch.",
		Format: map[string]interface{}{
			"type":       "grammar",
			"syntax":     "lark",
			"definition": "start: /.+/",
		},
	}}

	anthropicTools := ConvertToolsToAnthropicTyped(tools)
	if len(anthropicTools) != 1 {
		t.Fatalf("len(anthropicTools) = %d, want 1", len(anthropicTools))
	}
	if anthropicTools[0].Name != "apply_patch" {
		t.Fatalf("tool name = %q, want apply_patch", anthropicTools[0].Name)
	}
	if !strings.Contains(anthropicTools[0].Description, "raw custom-tool input text") ||
		!strings.Contains(anthropicTools[0].Description, "definition:\nstart: /.+/") {
		t.Fatalf("custom tool description = %q", anthropicTools[0].Description)
	}

	var schema map[string]interface{}
	if err := json.Unmarshal(anthropicTools[0].InputSchema, &schema); err != nil {
		t.Fatalf("json.Unmarshal(input_schema) error = %v", err)
	}
	properties, _ := schema["properties"].(map[string]interface{})
	if _, ok := properties[CustomToolInputField]; !ok || schema["additionalProperties"] != false {
		t.Fatalf("custom input schema = %#v", schema)
	}
}

func TestConvertToolsToGoogleTypedWrapsCustomTools(t *testing.T) {
	tools := []ToolDefinition{{
		Type:        "custom",
		Name:        "apply_patch",
		Description: "Apply a patch.",
		Format: map[string]interface{}{
			"type":       "grammar",
			"syntax":     "lark",
			"definition": "start: /.+/",
		},
	}}

	googleTools := ConvertToolsToGoogleTyped(tools)
	if len(googleTools) != 1 || len(googleTools[0].FunctionDeclarations) != 1 {
		t.Fatalf("google tools = %#v, want one declaration", googleTools)
	}
	decl := googleTools[0].FunctionDeclarations[0]
	if decl.Name != "apply_patch" || !strings.Contains(decl.Description, "raw custom-tool input text") || !strings.Contains(decl.Description, "definition:\nstart: /.+/") {
		t.Fatalf("google declaration = %#v", decl)
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(decl.Parameters, &schema); err != nil {
		t.Fatalf("json.Unmarshal(parameters) error = %v", err)
	}
	properties, _ := schema["properties"].(map[string]interface{})
	if _, ok := properties[CustomToolInputField]; !ok {
		t.Fatalf("custom input schema = %#v", schema)
	}
}

func TestToAnthropicTypedWrapsCustomToolUseInput(t *testing.T) {
	anthropic := ToAnthropicTyped([]TypedMessage{{
		Role: string(RoleAssistant),
		Blocks: []Block{ToolUseBlock{
			ID:          "call_1",
			Type:        "custom",
			Name:        "apply_patch",
			InputString: "*** Begin Patch\n*** End Patch\n",
		}},
	}})
	if len(anthropic) != 1 || len(anthropic[0].Content.Contents) != 1 {
		t.Fatalf("anthropic messages = %#v", anthropic)
	}
	input, ok := UnwrapCustomToolInput(anthropic[0].Content.Contents[0].Input)
	if !ok || input != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("wrapped input = %q, %v", input, ok)
	}
}

func TestToGoogleTypedWrapsCustomToolUseInput(t *testing.T) {
	google := ToGoogleTyped([]TypedMessage{{
		Role: string(RoleAssistant),
		Blocks: []Block{ToolUseBlock{
			ID:          "call_1",
			Type:        "custom",
			Name:        "apply_patch",
			InputString: "*** Begin Patch\n*** End Patch\n",
		}},
	}})
	if len(google) != 1 || len(google[0].Parts) != 1 || google[0].Parts[0].FunctionCall == nil {
		t.Fatalf("google messages = %#v", google)
	}
	input, ok := UnwrapCustomToolInput(google[0].Parts[0].FunctionCall.Args)
	if !ok || input != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("wrapped input = %q, %v", input, ok)
	}
}

// stringPtr helper function for string pointers
func stringPtr(s string) *string {
	return &s
}
