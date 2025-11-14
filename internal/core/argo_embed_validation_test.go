package core

import (
	"testing"
)

// TestIsValidToolName tests the tool name validation function
func TestIsValidToolName(t *testing.T) {
	tests := []struct {
		name       string
		toolName   string
		validTools []ToolDefinition
		expected   bool
	}{
		{
			name:       "valid_tool_name",
			toolName:   "universal_command",
			validTools: []ToolDefinition{{Name: "universal_command"}},
			expected:   true,
		},
		{
			name:       "invalid_tool_name",
			toolName:   "unknown_tool",
			validTools: []ToolDefinition{{Name: "universal_command"}},
			expected:   false,
		},
		{
			name:       "empty_tool_list_allows_all",
			toolName:   "any_tool",
			validTools: []ToolDefinition{},
			expected:   true,
		},
		{
			name:       "nil_tool_list_allows_all",
			toolName:   "any_tool",
			validTools: nil,
			expected:   true,
		},
		{
			name:       "case_sensitive_match",
			toolName:   "Universal_Command",
			validTools: []ToolDefinition{{Name: "universal_command"}},
			expected:   false,
		},
		{
			name:       "multiple_tools_valid",
			toolName:   "tool2",
			validTools: []ToolDefinition{{Name: "tool1"}, {Name: "tool2"}, {Name: "tool3"}},
			expected:   true,
		},
		{
			name:       "multiple_tools_invalid",
			toolName:   "tool4",
			validTools: []ToolDefinition{{Name: "tool1"}, {Name: "tool2"}, {Name: "tool3"}},
			expected:   false,
		},
		{
			name:       "suspicious_tool_name",
			toolName:   "../../../etc/passwd",
			validTools: []ToolDefinition{{Name: "universal_command"}},
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidToolName(tt.toolName, tt.validTools)
			if result != tt.expected {
				t.Errorf("IsValidToolName(%q, %v) = %v, want %v",
					tt.toolName, tt.validTools, result, tt.expected)
			}
		})
	}
}

// TestParseEmbeddedToolCalls_WithValidation tests tool call extraction with name validation
func TestParseEmbeddedToolCalls_WithValidation(t *testing.T) {
	validTools := []ToolDefinition{
		{Name: "universal_command"},
		{Name: "Edit"},
		{Name: "Read"},
	}

	tests := []struct {
		name          string
		content       string
		validTools    []ToolDefinition
		expectedCalls int
		description   string
	}{
		{
			name:          "valid_tool_with_validation",
			content:       `Tool: {'type': 'tool_use', 'name': 'Edit', 'id': 'test_123', 'input': {'file': 'test.go'}}`,
			validTools:    validTools,
			expectedCalls: 1,
			description:   "Valid tool name should be accepted",
		},
		{
			name:          "invalid_tool_with_validation",
			content:       `Tool: {'type': 'tool_use', 'name': 'UnknownTool', 'id': 'test_123', 'input': {}}`,
			validTools:    validTools,
			expectedCalls: 0,
			description:   "Invalid tool name should be rejected",
		},
		{
			name:          "no_validation_accepts_any",
			content:       `Tool: {'type': 'tool_use', 'name': 'AnyTool', 'id': 'test_123', 'input': {}}`,
			validTools:    nil,
			expectedCalls: 1,
			description:   "Without validation, any tool name should be accepted",
		},
		{
			name:          "multiple_calls_mixed_validity",
			content:       `First: {'type': 'tool_use', 'name': 'Edit', 'id': '1', 'input': {}} Second: {'type': 'tool_use', 'name': 'InvalidTool', 'id': '2', 'input': {}} Third: {'type': 'tool_use', 'name': 'Read', 'id': '3', 'input': {}}`,
			validTools:    validTools,
			expectedCalls: 2,
			description:   "Only valid tool calls should be extracted",
		},
		{
			name:          "openai_format_with_validation",
			content:       `Function: {'function': {'name': 'universal_command', 'arguments': '{"cmd": "ls"}'}, 'id': 'call_123'}`,
			validTools:    validTools,
			expectedCalls: 1,
			description:   "OpenAI format with valid tool name should be accepted",
		},
		{
			name:          "openai_format_invalid_tool",
			content:       `Function: {'function': {'name': 'invalid_func', 'arguments': '{}'}, 'id': 'call_123'}`,
			validTools:    validTools,
			expectedCalls: 0,
			description:   "OpenAI format with invalid tool name should be rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq, _, err := parseEmbeddedToolCalls(tt.content, tt.validTools)

			if tt.expectedCalls == 0 {
				// Should not find any tool calls
				if err == nil && len(seq) > 0 {
					t.Errorf("%s: expected no tool calls, but found %d", tt.description, len(seq))
				}
			} else {
				// Should find the expected number of tool calls
				if err != nil {
					t.Errorf("%s: unexpected error: %v", tt.description, err)
				} else if len(seq) != tt.expectedCalls {
					t.Errorf("%s: expected %d tool calls, got %d", tt.description, tt.expectedCalls, len(seq))
				}
			}
		})
	}
}

// TestExtractEmbeddedToolCalls_WithValidation tests the full extraction pipeline with validation
func TestExtractEmbeddedToolCalls_WithValidation(t *testing.T) {
	validTools := []ToolDefinition{
		{Name: "Edit"},
	}

	tests := []struct {
		name           string
		content        string
		validTools     []ToolDefinition
		expectedCalls  int
		expectedPrefix string
	}{
		{
			name:           "extract_valid_tool",
			content:        `Let me edit the file: {'type': 'tool_use', 'name': 'Edit', 'id': '123', 'input': {}}`,
			validTools:     validTools,
			expectedCalls:  1,
			expectedPrefix: "Let me edit the file:",
		},
		{
			name:           "skip_invalid_tool",
			content:        `Using tool: {'type': 'tool_use', 'name': 'Delete', 'id': '123', 'input': {}}`,
			validTools:     validTools,
			expectedCalls:  0,
			expectedPrefix: "",
		},
		{
			name:           "no_validation_extracts_all",
			content:        `Tool call: {'type': 'tool_use', 'name': 'AnyTool', 'id': '123', 'input': {}}`,
			validTools:     nil,
			expectedCalls:  1,
			expectedPrefix: "Tool call:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, toolCalls, err := ExtractEmbeddedToolCalls(tt.content, tt.validTools)

			if tt.expectedCalls == 0 {
				// Should return original content unchanged
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if len(toolCalls) != 0 {
					t.Errorf("Expected no tool calls, got %d", len(toolCalls))
				}
				if content != tt.content {
					t.Errorf("Content should be unchanged, got: %q", content)
				}
			} else {
				// Should extract tool calls and modify content
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if len(toolCalls) != tt.expectedCalls {
					t.Errorf("Expected %d tool calls, got %d", tt.expectedCalls, len(toolCalls))
				}
				if tt.expectedPrefix != "" && content != tt.expectedPrefix {
					t.Errorf("Expected content prefix %q, got %q", tt.expectedPrefix, content)
				}
			}
		})
	}
}
