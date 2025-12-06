package core

import (
	"testing"
)

// Tool definitions for these tests - including tool names with dots
var codeTestToolDefs = []ToolDefinition{
	{Name: "universal_command"},
	{Name: "method.Name"},
	{Name: "part.Call.Name"},
}

// TestParseEmbeddedToolCalls_IgnoresCodeExamples tests that code examples
// containing tool-like structures are not incorrectly parsed as tool calls
func TestParseEmbeddedToolCalls_IgnoresCodeExamples(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		expectFound bool
		description string
	}{
		{
			name: "code_with_part.Call.Name",
			content: `The code looks like this:
				tb := AnthropicContentBlock{Type: "tool_use", ID: part.Call.ID, Name: part.Call.Name}
				This is just a code example.`,
			expectFound: false,
			description: "Should not parse Go code with part.Call.Name as a tool call",
		},
		{
			name: "json_like_code_with_dots",
			content: `Here's the structure:
				{"type": "tool_use", "id": "obj.ID", "name": "method.Name", "input": {}}
				This is documentation.`,
			expectFound: true, // Now we allow dots in tool names
			description: "Should parse tool calls with dots in names (legitimate use case)",
		},
		{
			name: "valid_tool_call",
			content: `Processing request...
				{'type': 'tool_use', 'id': 'tool_123', 'name': 'universal_command', 'input': {'command': 'ls'}}
				Done.`,
			expectFound: true,
			description: "Should still parse valid tool calls without dots",
		},
		{
			name: "single_quoted_code_example",
			content: `The assistant might return:
				{'type': 'tool_use', 'id': 'part.Call.ID', 'name': 'part.Call.Name', 'input': {}}
				But this is just an example.`,
			expectFound: true, // Now we allow dots in tool names
			description: "Should parse tool calls with dots (now allowed)",
		},
		{
			name: "valid_single_quoted_tool",
			content: `Executing:
				{'type': 'tool_use', 'id': 'exec_456', 'name': 'universal_command', 'input': {'command': 'pwd'}}
				Completed.`,
			expectFound: true,
			description: "Should parse valid single-quoted tool calls",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq, _, err := parseEmbeddedToolCalls(tt.content, codeTestToolDefs)
			found := err == nil
			if found != tt.expectFound {
				t.Errorf("%s: expected found=%v, got %v", tt.description, tt.expectFound, found)
			}
			if found && tt.expectFound {
				// Verify we got a valid tool call
				if len(seq) == 0 {
					t.Errorf("%s: expected at least one tool call in sequence", tt.description)
				} else {
					call := seq[0].Call
					if call.Name == "" || call.ID == "" {
						t.Errorf("%s: tool call has empty name or ID", tt.description)
					}
					// Dots in tool names are now allowed (removed restriction)
				}
			}
		})
	}
}
