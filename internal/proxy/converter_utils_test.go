package proxy

import (
	"encoding/json"
	"lmtools/internal/core"
	"regexp"
	"strings"
	"testing"
)

// TestGenerateResponseID tests the response ID generation function
func TestGenerateResponseID(t *testing.T) {
	// Compile regex pattern once outside the loop for better performance
	pattern := regexp.MustCompile(`^msg_[0-9a-f]{8}[0-9a-f]{4}[0-9a-f]{4}[0-9a-f]{4}[0-9a-f]{12}$`)

	// Test that IDs are generated with correct format
	for i := 0; i < 10; i++ {
		id := generateResponseID()

		// Check prefix
		if !strings.HasPrefix(id, "msg_") {
			t.Errorf("Response ID should start with 'msg_', got: %s", id)
		}

		// Check format (msg_ followed by hex characters with specific pattern)
		// UUID v4 format: xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx (without dashes in our case)
		if !pattern.MatchString(id) {
			t.Errorf("Response ID does not match expected format, got: %s", id)
		}

		// Verify UUID v4 version bits (the 13th character should be '4')
		// In our format: msg_xxxxxxxxyyyy4... where yyyy is the 6th-8th byte
		hexPart := strings.TrimPrefix(id, "msg_")
		if len(hexPart) >= 12 && hexPart[12] != '4' {
			t.Errorf("Response ID is not UUID v4 format (version bit), got: %s", id)
		}
	}
}

// TestGenerateToolUseID tests the tool use ID generation function
func TestGenerateToolUseID(t *testing.T) {
	// Compile regex pattern once outside the loop for better performance
	pattern := regexp.MustCompile(`^toolu_[0-9a-f]{8}[0-9a-f]{4}[0-9a-f]{4}[0-9a-f]{4}[0-9a-f]{12}$`)

	// Test that IDs are generated with correct format
	for i := 0; i < 10; i++ {
		id := generateToolUseID()

		// Check prefix
		if !strings.HasPrefix(id, "toolu_") {
			t.Errorf("Tool use ID should start with 'toolu_', got: %s", id)
		}

		// Check format
		if !pattern.MatchString(id) {
			t.Errorf("Tool use ID does not match expected format, got: %s", id)
		}

		// Verify UUID v4 version bits
		hexPart := strings.TrimPrefix(id, "toolu_")
		if len(hexPart) >= 12 && hexPart[12] != '4' {
			t.Errorf("Tool use ID is not UUID v4 format (version bit), got: %s", id)
		}
	}
}

// TestIDUniqueness tests that generated IDs are unique
func TestIDUniqueness(t *testing.T) {
	const numIDs = 1000

	// Test response ID uniqueness
	responseIDs := make(map[string]bool)
	for i := 0; i < numIDs; i++ {
		id := generateResponseID()
		if responseIDs[id] {
			t.Errorf("Duplicate response ID generated: %s", id)
		}
		responseIDs[id] = true
	}

	// Test tool use ID uniqueness
	toolUseIDs := make(map[string]bool)
	for i := 0; i < numIDs; i++ {
		id := generateToolUseID()
		if toolUseIDs[id] {
			t.Errorf("Duplicate tool use ID generated: %s", id)
		}
		toolUseIDs[id] = true
	}
}

// TestGenerateUUID tests the unified UUID generation function
func TestGenerateUUID(t *testing.T) {
	// Test with different prefixes
	prefixes := []string{"msg_", "toolu_", "chatcmpl-", "test_"}

	for _, prefix := range prefixes {
		id := generateUUID(prefix)

		// Check prefix
		if !strings.HasPrefix(id, prefix) {
			t.Errorf("UUID should start with '%s', got: %s", prefix, id)
		}

		// Check that the rest is valid hex
		hexPart := strings.TrimPrefix(id, prefix)
		// The format is: xxxxxxxxyyyyzzzzaaaabbbbbbbbbbbb (32 hex chars total)
		if len(hexPart) != 32 {
			t.Errorf("UUID hex part should be 32 characters, got %d: %s", len(hexPart), hexPart)
		}

		// Verify UUID v4 version bits (the 13th character should be '4')
		// In our format without separators, position 12 should be '4'
		if len(hexPart) >= 12 && hexPart[12] != '4' {
			t.Errorf("UUID is not v4 format (version bit), got: %s", id)
		}
	}
}

// TestExtractSystemContentUtils tests the system content extraction (renamed to avoid conflict)
func TestExtractSystemContentUtils(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string
		wantErr  bool
	}{
		{
			name:     "simple string",
			input:    []byte(`"You are a helpful assistant"`),
			expected: "You are a helpful assistant",
			wantErr:  false,
		},
		{
			name:     "array of text blocks",
			input:    []byte(`[{"type": "text", "text": "Part 1"}, {"type": "text", "text": "Part 2"}]`),
			expected: "Part 1\nPart 2",
			wantErr:  false,
		},
		{
			name:     "single text block",
			input:    []byte(`{"type": "text", "text": "Single block"}`),
			expected: "Single block",
			wantErr:  false,
		},
		{
			name:     "mixed content blocks (only text extracted)",
			input:    []byte(`[{"type": "text", "text": "Text"}, {"type": "image", "source": {"url": "data:..."}}]`),
			expected: "Text",
			wantErr:  false,
		},
		{
			name:     "invalid JSON",
			input:    []byte(`{invalid json`),
			expected: "",
			wantErr:  true,
		},
		{
			name:     "non-text block only",
			input:    []byte(`{"type": "image", "source": {"url": "data:..."}}`),
			expected: "",
			wantErr:  false,
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
				t.Errorf("extractSystemContent() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestParseAnthropicMessageContent(t *testing.T) {
	tests := []struct {
		name           string
		input          json.RawMessage
		wantText       *string
		wantBlockCount int
		wantErr        bool
	}{
		{
			name:     "string content",
			input:    json.RawMessage(`"hello"`),
			wantText: ptr("hello"),
		},
		{
			name:           "block array",
			input:          json.RawMessage(`[{"type":"text","text":"hello"},{"type":"tool_use","id":"tool-1","name":"run","input":{"cmd":"pwd"}}]`),
			wantBlockCount: 2,
		},
		{
			name:           "single block",
			input:          json.RawMessage(`{"type":"text","text":"single"}`),
			wantBlockCount: 1,
		},
		{
			name:    "invalid content",
			input:   json.RawMessage(`{not-json`),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, blocks, err := parseAnthropicMessageContent(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseAnthropicMessageContent() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			switch {
			case tt.wantText != nil:
				if text == nil || *text != *tt.wantText {
					t.Fatalf("parseAnthropicMessageContent() text = %v, want %q", text, *tt.wantText)
				}
				if len(blocks) != 0 {
					t.Fatalf("parseAnthropicMessageContent() blocks = %d, want 0", len(blocks))
				}
			default:
				if text != nil {
					t.Fatalf("parseAnthropicMessageContent() unexpected text = %q", *text)
				}
				if len(blocks) != tt.wantBlockCount {
					t.Fatalf("parseAnthropicMessageContent() block count = %d, want %d", len(blocks), tt.wantBlockCount)
				}
			}
		})
	}
}

func TestAnthropicBlocksToCorePreservesToolResultError(t *testing.T) {
	blocks := []AnthropicContentBlock{
		{
			Type:      "tool_result",
			ToolUseID: "tool-1",
			Content:   json.RawMessage(`"command failed"`),
			IsError:   true,
		},
	}

	converted := AnthropicBlocksToCore(blocks)
	if len(converted) != 1 {
		t.Fatalf("len(converted) = %d, want 1", len(converted))
	}

	result, ok := converted[0].(core.ToolResultBlock)
	if !ok {
		t.Fatalf("converted[0] type = %T, want core.ToolResultBlock", converted[0])
	}
	if !result.IsError {
		t.Fatal("ToolResultBlock.IsError = false, want true")
	}
	if result.Content != "command failed" {
		t.Fatalf("ToolResultBlock.Content = %q, want %q", result.Content, "command failed")
	}
}

func TestCoreBlocksToAnthropicPreservesToolResultError(t *testing.T) {
	blocks := []core.Block{
		core.ToolResultBlock{
			ToolUseID: "tool-1",
			Content:   "command failed",
			IsError:   true,
		},
	}

	converted := CoreBlocksToAnthropic(blocks)
	if len(converted) != 1 {
		t.Fatalf("len(converted) = %d, want 1", len(converted))
	}
	if !converted[0].IsError {
		t.Fatal("AnthropicContentBlock.IsError = false, want true")
	}
	if converted[0].ToolUseID != "tool-1" {
		t.Fatalf("ToolUseID = %q, want %q", converted[0].ToolUseID, "tool-1")
	}

	var content string
	if err := json.Unmarshal(converted[0].Content, &content); err != nil {
		t.Fatalf("tool result content unmarshal error = %v", err)
	}
	if content != "command failed" {
		t.Fatalf("tool result content = %q, want %q", content, "command failed")
	}
}

func ptr(s string) *string {
	return &s
}
