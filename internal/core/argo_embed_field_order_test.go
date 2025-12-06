package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// fieldOrderToolDefs provides tool definitions for field order tests
var fieldOrderToolDefs = []ToolDefinition{
	{Name: "Edit"},
	{Name: "TestTool"},
	{Name: "MiddleTool"},
	{Name: "NameFirst"},
	{Name: "InputFirst"},
	{Name: "Tool1"},
	{Name: "Tool2"},
	{Name: "test_func"},
	{Name: "ComplexTool"},
	{Name: "TrailTool"},
	{Name: "Perm1"},
	{Name: "Perm2"},
	{Name: "Perm3"},
	{Name: "Perm4"},
	{Name: "Perm5"},
	{Name: "Perm6"},
	{Name: "Perm7"},
	{Name: "Perm8"},
	{Name: "Perm9"},
	{Name: "Perm10"},
	{Name: "Perm11"},
	{Name: "Perm12"},
	{Name: "SingleTool"},
	{Name: "StandardTool"},
	{Name: "FirstTool"},
	{Name: "LastTool"},
	{Name: "Tool"},
	{Name: "Read"},
	{Name: "Grep"},
}

// This file tests that embedded tool calls in Argo responses are correctly parsed
// regardless of the order of fields in the JSON object. This addresses an issue
// where tool calls with the 'type' field at the end (instead of at the beginning)
// were not being detected correctly.
//
// The parser should handle all valid JSON field orderings since JSON objects are
// inherently unordered collections of key-value pairs.

// TestParseEmbeddedToolCalls_FieldOrder tests that embedded tool calls are detected
// regardless of the order of fields in the JSON object
func TestParseEmbeddedToolCalls_FieldOrder(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		expectedCalls int
		expectedOK    bool
		description   string
	}{
		{
			name:          "type_field_at_end",
			content:       "Let me fix this: {'id': 'toolu_vrtx_01XqLnQUvXJJJJJJJJJJJJJJJj', 'input': {'file_path': '/test.go', 'new_string': 'new', 'old_string': 'old'}, 'name': 'Edit', 'type': 'tool_use'}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Parse tool call with 'type' field at the end (exact failing case)",
		},
		{
			name:          "type_field_at_beginning",
			content:       "Tool call: {'type': 'tool_use', 'id': 'test123', 'name': 'TestTool', 'input': {'param': 'value'}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Parse tool call with 'type' field at the beginning (standard order)",
		},
		{
			name:          "type_field_in_middle",
			content:       "Execute: {'id': 'test456', 'type': 'tool_use', 'name': 'MiddleTool', 'input': {'key': 'val'}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Parse tool call with 'type' field in the middle",
		},
		{
			name:          "name_type_id_input_order",
			content:       "Run: {'name': 'NameFirst', 'type': 'tool_use', 'id': 'test789', 'input': {'data': 'test'}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Parse tool call with name, type, id, input order",
		},
		{
			name:          "input_name_id_type_order",
			content:       "Process: {'input': {'complex': {'nested': 'value'}}, 'name': 'InputFirst', 'id': 'test101', 'type': 'tool_use'}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Parse tool call with input first, type last",
		},
		{
			name:          "multiple_calls_different_orders",
			content:       "First: {'type': 'tool_use', 'id': '1', 'name': 'Tool1', 'input': {}} Second: {'id': '2', 'input': {}, 'name': 'Tool2', 'type': 'tool_use'}",
			expectedCalls: 2,
			expectedOK:    true,
			description:   "Multiple tool calls with different field orders",
		},
		{
			name:          "openai_style_type_at_end",
			content:       "Calling: {'function': {'name': 'test_func', 'arguments': '{\"param\": \"value\"}'}, 'id': 'call_123', 'type': 'function'}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "OpenAI-style function call with type at end (single quotes)",
		},
		{
			name:          "complex_nested_type_at_end",
			content:       "Execute: {'id': 'complex123', 'input': {'nested': {'deep': {'value': 'test'}}, 'array': ['item1', 'item2']}, 'name': 'ComplexTool', 'type': 'tool_use'}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Complex nested input with type at end",
		},
		{
			name:          "type_at_end_with_trailing_text",
			content:       "Processing: {'id': 'trail123', 'input': {'test': 'value'}, 'name': 'TrailTool', 'type': 'tool_use'} and continuing with more text",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Tool call with type at end followed by trailing text",
		},
		{
			name: "all_permutations_test",
			content: `Multiple formats:
1. {'type': 'tool_use', 'id': 'p1', 'name': 'Perm1', 'input': {}}
2. {'type': 'tool_use', 'id': 'p2', 'input': {}, 'name': 'Perm2'}
3. {'type': 'tool_use', 'name': 'Perm3', 'id': 'p3', 'input': {}}
4. {'type': 'tool_use', 'name': 'Perm4', 'input': {}, 'id': 'p4'}
5. {'type': 'tool_use', 'input': {}, 'id': 'p5', 'name': 'Perm5'}
6. {'type': 'tool_use', 'input': {}, 'name': 'Perm6', 'id': 'p6'}
7. {'id': 'p7', 'type': 'tool_use', 'name': 'Perm7', 'input': {}}
8. {'id': 'p8', 'type': 'tool_use', 'input': {}, 'name': 'Perm8'}
9. {'id': 'p9', 'name': 'Perm9', 'type': 'tool_use', 'input': {}}
10. {'id': 'p10', 'name': 'Perm10', 'input': {}, 'type': 'tool_use'}
11. {'id': 'p11', 'input': {}, 'type': 'tool_use', 'name': 'Perm11'}
12. {'id': 'p12', 'input': {}, 'name': 'Perm12', 'type': 'tool_use'}`,
			expectedCalls: 12,
			expectedOK:    true,
			description:   "All permutations of field order with type in different positions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq, suffix, err := parseEmbeddedToolCalls(tt.content, fieldOrderToolDefs)
			ok := err == nil

			if ok != tt.expectedOK {
				t.Errorf("%s: expected ok=%v, got %v (error: %v)", tt.description, tt.expectedOK, ok, err)
			}

			if len(seq) != tt.expectedCalls {
				t.Errorf("%s: expected %d calls, got %d", tt.description, tt.expectedCalls, len(seq))
				if len(seq) > 0 {
					for i, item := range seq {
						if item.Call != nil {
							t.Logf("  Call %d: Name=%s, ID=%s", i+1, item.Call.Name, item.Call.ID)
						} else {
							t.Logf("  Call %d: nil", i+1)
						}
					}
				}
			}

			// For successful parsing, verify that we got valid tool calls
			if err == nil && len(seq) > 0 {
				for i, item := range seq {
					if item.Call == nil {
						t.Errorf("%s: Call %d is nil", tt.description, i+1)
						continue
					}
					if item.Call.Name == "" {
						t.Errorf("%s: Call %d has empty name", tt.description, i+1)
					}
					if item.Call.ID == "" {
						t.Errorf("%s: Call %d has empty ID", tt.description, i+1)
					}
					if item.Call.ArgsJSON == nil {
						t.Errorf("%s: Call %d has nil ArgsJSON", tt.description, i+1)
					}
				}
			}

			// Check suffix handling for trailing text case
			if tt.name == "type_at_end_with_trailing_text" && ok {
				if suffix == "" {
					t.Errorf("%s: Expected non-empty suffix for trailing text", tt.description)
				}
			}
		})
	}
}

// TestParseEmbeddedToolCall_FieldOrder tests the single call wrapper with field order variations
func TestParseEmbeddedToolCall_FieldOrder(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		expectedOK   bool
		expectedID   string
		expectedName string
		description  string
	}{
		{
			name:         "single_call_type_at_end",
			content:      "Execute: {'id': 'single123', 'input': {'test': 'value'}, 'name': 'SingleTool', 'type': 'tool_use'}",
			expectedOK:   true,
			expectedID:   "single123",
			expectedName: "SingleTool",
			description:  "Single call with type at end",
		},
		{
			name:         "single_call_type_at_beginning",
			content:      "Execute: {'type': 'tool_use', 'id': 'single456', 'name': 'StandardTool', 'input': {}}",
			expectedOK:   true,
			expectedID:   "single456",
			expectedName: "StandardTool",
			description:  "Single call with type at beginning",
		},
		{
			name:         "multiple_calls_returns_last_with_type_at_end",
			content:      "First: {'type': 'tool_use', 'id': 'first', 'name': 'FirstTool', 'input': {}} Second: {'id': 'last', 'input': {}, 'name': 'LastTool', 'type': 'tool_use'}",
			expectedOK:   true,
			expectedID:   "last",
			expectedName: "LastTool",
			description:  "Multiple calls, last one has type at end",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use parseEmbeddedToolCalls directly instead of the wrapper
			seq, _, err := parseEmbeddedToolCalls(tt.content, fieldOrderToolDefs)
			ok := err == nil
			var call *EmbeddedCall
			if ok && len(seq) > 0 {
				// Get the last call and set its Trimmed field
				last := seq[len(seq)-1]
				call = last.Call
				call.Trimmed = strings.TrimSpace(last.Prefix)
			}

			if ok != tt.expectedOK {
				t.Errorf("%s: expected ok=%v, got %v (error: %v)", tt.description, tt.expectedOK, ok, err)
			}

			if ok && call != nil {
				if call.ID != tt.expectedID {
					t.Errorf("%s: expected ID=%s, got %s", tt.description, tt.expectedID, call.ID)
				}
				if call.Name != tt.expectedName {
					t.Errorf("%s: expected Name=%s, got %s", tt.description, tt.expectedName, call.Name)
				}
			}
		})
	}
}

// TestNormalizeFieldOrder_RealWorldCase tests the exact case that failed in production
func TestNormalizeFieldOrder_RealWorldCase(t *testing.T) {
	// This is the exact content from the failing apiproxy debug log
	// Use the exact escaped content as in apiproxy debug logs (escape sequences are literal)
	// Match the same escaping as TestNormalizeFieldOrder_RealWorldCase to reflect raw content (not doubly JSON-escaped)
	content := `There's an issue with the escape sequence in the test. Let me fix it{'id': 'toolu_vrtx_01XqLnQUvXJJJJJJJJJJJJJJj', 'input': {'file_path': '/path/to/project/internal/core/argo_embed_edge_cases_test.go', 'new_string': '\t\t\tname:          "apostrophe_adjacent_to_delimiter",\n\t\t\tcontent:       "Let\'s analyze: {\'type\': \'tool_use\', \'name\': \'test\', \'id\': \'123\', \'input\': {\'text\': \'It\\\'s working\'}}",\n\t\t\texpectedCalls: 1,\n\t\t\texpectedOK:    true,\n\t\t\tdescription:   "Handle apostrophes adjacent to JSON delimiters",', 'old_string': '\t\t\tname:          "apostrophe_adjacent_to_delimiter",\n\t\t\tcontent:       "Let\'s analyze: {\'type\': \'tool_use\', \'name\': \'test\', \'id\': \'123\', \'input\': {\'text\': \'It\'s working\'}}",\n\t\t\texpectedCalls: 1,\n\t\t\texpectedOK:    true,\n\t\t\tdescription:   "Handle apostrophes adjacent to JSON delimiters",'}, 'name': 'Edit', 'type': 'tool_use'}`

	seq, _, err := parseEmbeddedToolCalls(content, fieldOrderToolDefs)
	if err != nil {
		t.Errorf("Failed to parse real-world case with type at end: %v", err)
	}

	if len(seq) != 1 {
		t.Errorf("Expected 1 call, got %d", len(seq))
	}

	if err == nil && len(seq) > 0 {
		call := seq[0].Call
		if call == nil {
			t.Errorf("Expected non-nil call")
			return
		}
		if call.ID != "toolu_vrtx_01XqLnQUvXJJJJJJJJJJJJJJj" {
			t.Errorf("Expected ID 'toolu_vrtx_01XqLnQUvXJJJJJJJJJJJJJJj', got '%s'", call.ID)
		}
		if call.Name != "Edit" {
			t.Errorf("Expected name 'Edit', got '%s'", call.Name)
		}
		if call.ArgsJSON == nil {
			t.Errorf("Expected non-nil ArgsJSON")
		} else {
			// Parse the ArgsJSON to check contents
			var args map[string]interface{}
			if err := json.Unmarshal(call.ArgsJSON, &args); err != nil {
				t.Errorf("Failed to unmarshal ArgsJSON: %v", err)
			} else {
				// Check that the input contains the expected fields
				if filePath, ok := args["file_path"].(string); !ok || filePath == "" {
					t.Errorf("Expected file_path in args, got %v", args["file_path"])
				}
				if _, hasNew := args["new_string"]; !hasNew {
					t.Errorf("Expected new_string in args")
				}
				if _, hasOld := args["old_string"]; !hasOld {
					t.Errorf("Expected old_string in args")
				}
			}
		}
	}
}

// TestExactApiProxyFailingCase tests the exact case that failed in the apiproxy debug log
// This case has complex backslash escaping and no space before the embedded JSON
func TestExactApiProxyFailingCase(t *testing.T) {
	// This is the EXACT content from the apiproxy debug log response (raw content form)
	// Note: This is assistant text embedding a single-quoted JSON-like block; backslashes are literal in Go raw strings.
	content := `There's an issue with the escape sequence in the test. Let me fix it{'id': 'toolu_vrtx_01XqLnQUvXJJJJJJJJJJJJJJj', 'input': {'file_path': '/path/to/project/internal/core/argo_embed_edge_cases_test.go', 'new_string': '\t\t\tname:          "apostrophe_adjacent_to_delimiter",\n\t\t\tcontent:       "Let\'s analyze: {\'type\': \'tool_use\', \'name\': \'test\', \'id\': \'123\', \'input\': {\'text\': \'It\\\'s working\'}}",\n\t\t\texpectedCalls: 1,\n\t\t\texpectedOK:    true,\n\t\t\tdescription:   "Handle apostrophes adjacent to JSON delimiters",', 'old_string': '\t\t\tname:          "apostrophe_adjacent_to_delimiter",\n\t\t\tcontent:       "Let\'s analyze: {\'type\': \'tool_use\', \'name\': \'test\', \'id\': \'123\', \'input\': {\'text\': \'It\'s working\'}}",\n\t\t\texpectedCalls: 1,\n\t\t\texpectedOK:    true,\n\t\t\tdescription:   "Handle apostrophes adjacent to JSON delimiters",'}, 'name': 'Edit', 'type': 'tool_use'}`

	// Extra debug: locate anchors and slice candidate around last 'type': 'tool_use'
	lastAnth := strings.LastIndex(content, "'type': 'tool_use'")
	lastAnthEsc := strings.LastIndex(content, "\\'type\\': \\'tool_use\\'")
	t.Logf("Anchors: lastAnth=%d lastAnthEsc=%d", lastAnth, lastAnthEsc)
	anchor := lastAnth
	if anchor < 0 {
		anchor = lastAnthEsc
	}
	if anchor >= 0 {
		leftBrace := strings.LastIndex(content[:anchor+1], "{")
		t.Logf("Nearest left brace before anchor: %d", leftBrace)
		if leftBrace >= 0 {
			// Use production path: parse from leftBrace and let the loose parser find the end
			if m, end, ok := parseLooseJSONObjectAt(content, leftBrace); ok {
				t.Logf("Candidate slice length: %d", end-leftBrace+1)
				t.Logf("Candidate head: %q", content[leftBrace:min(end+1, leftBrace+120)])
				t.Logf("Candidate tail: %q", content[max(leftBrace, end-119):end+1])
				_ = m
			} else {
				t.Logf("parseLooseJSONObject failed")
			}
		}
	}

	seq, suffix, err := parseEmbeddedToolCalls(content, fieldOrderToolDefs)
	if err != nil {
		t.Errorf("Failed to parse the exact apiproxy failing case: %v", err)
		t.Logf("Content length: %d", len(content))
		t.Logf("First 100 chars: %q", content[:min(100, len(content))])
	}

	if len(seq) != 1 {
		t.Errorf("Expected 1 call, got %d", len(seq))
		if len(seq) > 0 {
			for i, item := range seq {
				if item.Call != nil {
					t.Logf("  Call %d: Name=%s, ID=%s", i+1, item.Call.Name, item.Call.ID)
				}
			}
		}
	}

	if err == nil && len(seq) > 0 {
		call := seq[0].Call
		if call == nil {
			t.Errorf("Expected non-nil call")
			return
		}

		// Check basic fields
		if call.ID != "toolu_vrtx_01XqLnQUvXJJJJJJJJJJJJJJj" {
			t.Errorf("Expected ID 'toolu_vrtx_01XqLnQUvXJJJJJJJJJJJJJJj', got '%s'", call.ID)
		}
		if call.Name != "Edit" {
			t.Errorf("Expected name 'Edit', got '%s'", call.Name)
		}

		// Check that the prefix doesn't include the JSON
		expectedPrefix := "There's an issue with the escape sequence in the test. Let me fix it"
		if seq[0].Prefix != expectedPrefix {
			t.Errorf("Expected prefix %q, got %q", expectedPrefix, seq[0].Prefix)
		}

		// Check suffix is empty (since the JSON goes to the end)
		if suffix != "" {
			t.Errorf("Expected empty suffix, got %q", suffix)
		}

		// Parse and verify the complex arguments
		if call.ArgsJSON != nil {
			var args map[string]interface{}
			if err := json.Unmarshal(call.ArgsJSON, &args); err != nil {
				t.Errorf("Failed to unmarshal ArgsJSON: %v", err)
			} else {
				// Check that we have the expected fields
				if filePath, ok := args["file_path"].(string); !ok {
					t.Errorf("Missing or invalid file_path in args")
				} else if filePath != "/path/to/project/internal/core/argo_embed_edge_cases_test.go" {
					t.Errorf("Unexpected file_path: %s", filePath)
				}

				if _, hasNew := args["new_string"]; !hasNew {
					t.Errorf("Missing new_string in args")
				}
				if _, hasOld := args["old_string"]; !hasOld {
					t.Errorf("Missing old_string in args")
				}
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TestNoSpaceBeforeJSON tests parsing when there's no space before the embedded JSON
func TestNoSpaceBeforeJSON(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		expectedCalls int
		expectedOK    bool
		description   string
	}{
		{
			name:          "no_space_simple",
			content:       "Text directly before{'type': 'tool_use', 'id': 'test1', 'name': 'Tool', 'input': {}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Simple case with no space before JSON",
		},
		{
			name:          "no_space_with_it",
			content:       "Let me fix it{'type': 'tool_use', 'id': 'test2', 'name': 'Tool', 'input': {}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "'it' before brace (like failing case)",
		},
		{
			name:          "no_space_type_at_end",
			content:       "Fix it{'id': 'test3', 'input': {}, 'name': 'Tool', 'type': 'tool_use'}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "No space and type at end",
		},
		{
			name:          "complex_escaping_simple",
			content:       "Test{'type': 'tool_use', 'id': 'test4', 'name': 'Edit', 'input': {'text': '\\\\t\\\\tvalue'}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Backslash escaping in input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq, _, err := parseEmbeddedToolCalls(tt.content, fieldOrderToolDefs)
			ok := err == nil

			if ok != tt.expectedOK {
				t.Errorf("%s: expected ok=%v, got %v (error: %v)", tt.description, tt.expectedOK, ok, err)
				t.Logf("Content: %q", tt.content)
			}

			if len(seq) != tt.expectedCalls {
				t.Errorf("%s: expected %d calls, got %d", tt.description, tt.expectedCalls, len(seq))
			}
		})
	}
}

// TestGraduallyComplexEscaping tests with gradually increasing backslash complexity
func TestGraduallyComplexEscaping(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		expectedCalls int
		expectedOK    bool
		description   string
	}{
		{
			name:          "level1_simple_escape",
			content:       "Test{'type': 'tool_use', 'id': 't1', 'name': 'Edit', 'input': {'text': '\\tvalue'}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Single level escape (\\t)",
		},
		{
			name:          "level2_double_escape",
			content:       "Test{'type': 'tool_use', 'id': 't2', 'name': 'Edit', 'input': {'text': '\\\\tvalue'}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Double level escape (\\\\t)",
		},
		{
			name:          "level3_quad_escape",
			content:       "Test{'type': 'tool_use', 'id': 't3', 'name': 'Edit', 'input': {'text': '\\\\\\\\tvalue'}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Quad level escape (\\\\\\\\t)",
		},
		{
			name:          "apostrophe_with_many_backslashes",
			content:       "Test{'type': 'tool_use', 'id': 't4', 'name': 'Edit', 'input': {'text': 'It\\\\\\\\\\\\\\'s working'}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Many backslashes before apostrophe",
		},
		{
			name:          "exact_failing_pattern",
			content:       "Fix it{'id': 't5', 'input': {'new_string': '\\\\t\\\\t\\\\tname: \"test\"\\\\n'}, 'name': 'Edit', 'type': 'tool_use'}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Pattern similar to failing case",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq, _, err := parseEmbeddedToolCalls(tt.content, fieldOrderToolDefs)
			ok := err == nil

			if ok != tt.expectedOK {
				t.Errorf("%s: expected ok=%v, got %v (error: %v)", tt.description, tt.expectedOK, ok, err)
				t.Logf("Content: %q", tt.content)
				t.Logf("Content length: %d", len(tt.content))
			}

			if len(seq) != tt.expectedCalls {
				t.Errorf("%s: expected %d calls, got %d", tt.description, tt.expectedCalls, len(seq))
			}
		})
	}
}
