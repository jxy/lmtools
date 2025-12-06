package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// testToolDefs provides common tool definitions for tests
var testToolDefs = []ToolDefinition{
	{Name: "search"},
	{Name: "Read"},
	{Name: "Edit"},
	{Name: "Grep"},
	{Name: "Glob"},
	{Name: "Bash"},
	{Name: "Write"},
	{Name: "universal_command"},
	{Name: "tool1"},
	{Name: "tool2"},
	{Name: "tool3"},
}

// Test that parseEmbeddedToolCall handles apostrophes correctly
func TestParseEmbeddedToolCall_WithApostrophes(t *testing.T) {
	content := `Let me search for Alice's files: {'type': 'tool_use', 'id': 'tool_123', 'name': 'search', 'input': {'query': 'Alice\'s documents', 'filter': 'owner\'s name'}}`

	// Use parseEmbeddedToolCalls directly with valid tool definitions
	seq, _, err := parseEmbeddedToolCalls(content, testToolDefs)
	if err != nil {
		t.Fatal("Expected to find embedded tool call:", err)
	}
	if len(seq) == 0 {
		t.Fatal("No embedded tool calls found")
	}

	// Get the last call and set its Trimmed field
	last := seq[len(seq)-1]
	call := last.Call
	call.Trimmed = strings.TrimSpace(last.Prefix)

	if call.Trimmed != "Let me search for Alice's files:" {
		t.Errorf("Unexpected trimmed text: %s", call.Trimmed)
	}

	if call.Name != "search" {
		t.Errorf("Unexpected tool name: %s", call.Name)
	}

	var args map[string]interface{}
	if err := json.Unmarshal(call.ArgsJSON, &args); err != nil {
		t.Fatalf("Failed to unmarshal args: %v", err)
	}

	if query, ok := args["query"].(string); !ok || query != "Alice's documents" {
		t.Errorf("Expected query 'Alice's documents', got: %v", args["query"])
	}

	if filter, ok := args["filter"].(string); !ok || filter != "owner's name" {
		t.Errorf("Expected filter 'owner's name', got: %v", args["filter"])
	}
}
