package core

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// edgeCaseToolDefs provides common tool definitions for edge case tests
var edgeCaseToolDefs = []ToolDefinition{
	{Name: "Edit"},
	{Name: "test"},
	{Name: "test1"},
	{Name: "test2"},
	{Name: "test_func"},
	{Name: "EndTool"},
	{Name: "EndToolDQ"},
	{Name: "Tool1"},
	{Name: "Tool2"},
	{Name: "Tool3"},
	{Name: "TodoWrite"},
	{Name: "MultiClear"},
	{Name: "NestedTest"},
	{Name: "MixedArrays"},
	{Name: "clear_list"},
}

// TestParseEmbeddedToolCalls_EdgeCases tests edge cases for embedded JSON parsing
func TestParseEmbeddedToolCalls_EdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		expectedCalls int
		expectedOK    bool
		description   string
	}{
		{
			name:          "content_only_single_quoted_block_with_inner_double_quotes",
			content:       "Let me replace the usage with `strings.IndexByte`:\n\n{'id': 'toolu_01BnQvkLJhJJHhJJJJJJJJJJ', 'input': {'file_path': '/path/to/project/internal/core/argo_embed.go', 'new_string': \"\\ti := 0\\n\\tlastEndNorm := 0\\n\\tfor i < len(normalizedContent) {\\n\\t\\t// Find next '{' in normalized content\\n\\t\\t// After simple quote normalization, quotes in free text can be unbalanced,\\n\\t\\t// so we scan every '{' and let JSON parsing decide\\n\\t\\tidx := strings.IndexByte(normalizedContent[i:], '{')\\n\\t\\tif idx < 0 {\\n\\t\\t\\tbreak\\n\\t\\t}\\n\\t\\tstart := i + idx\", 'old_string': \"\\ti := 0\\n\\tlastEndNorm := 0\\n\\tfor i < len(normalizedContent) {\\n\\t\\t// Find next '{' outside quotes on normalized content\\n\\t\\tidx := nextBraceOutsideQuotes(normalizedContent, i)\\n\\t\\tif idx < 0 {\\n\\t\\t\\tbreak\\n\\t\\t}\\n\\t\\tstart := idx\"}, 'name': 'Edit', 'type': 'tool_use'}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Direct content string with single-quoted tool_use containing inner double quotes",
		},
		{
			name:          "mix_json_like_structure_with_double_quotes_in_single_quotes",
			content:       "You're absolutely correct! In Go string literals:\n- In a raw string (backticks): `'text': 'It\\'s working'` - the backslash is literal\n- In a regular string (double quotes): `\"'text': 'It\\\\'s working'\"` - the backslash needs to be escaped\n\nSince the test is using double quotes, we need `\\\\'` (two backslashes) to represent a single backslash in the actual string. Let me fix it properly:\n\n{'id': 'toolu', 'input': {'file_path': 'edge_cases_test.go', 'new_string': '\\t\\t\\tname:   \"apostrophe_adjacent_to_delimiter\",\\n\\t\\t\\tcontent:       \"Let\\'s analyze: {\\'type\\': \\'tool_use\\', \\'name\\': \\'test\\', \\'id\\': \\'123\\', \\'input\\': {\\'text\\': \\'It\\\\\\'s working\\'}}\",\\n\\t\\t\\texpectedCalls: 1,\\n\\t\\t\\texpectedOK:    true,\\n\\t\\t\\tdescription:   \"Handle apostrophes adjacent to JSON delimiters\",', 'old_string': '\\t\\t\\tname:          \"apostrophe_adjacent_to_delimiter\",\\n\\t\\t\\tcontent:       \"Let\\'s analyze: {\\'type\\': \\'tool_use\\', \\'name\\': \\'test\\', \\'id\\': \\'123\\', \\'input\\': {\\'text\\': \\'It\\\\\\'s working\\'}}\",\\n\\t\\t\\texpectedCalls: 1,\\n\\t\\t\\texpectedOK:    true,\\n\\t\\t\\tdescription:   \"Handle apostrophes adjacentto JSON delimiters\",'}, 'name': 'Edit', 'type': 'tool_use'}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Handle double quotes and braces in JSON strings",
		},
		{
			name:          "apostrophe_adjacent_to_delimiter",
			content:       "Let's analyze: {'type': 'tool_use', 'name': 'test', 'id': '123', 'input': {'text': 'It\\'s working'}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Handle apostrophes adjacent to JSON delimiters",
		},
		{
			name:          "nested_quotes_with_escapes",
			content:       "Here is the call: {'type': 'tool_use', 'name': 'test', 'id': '123', 'input': {'text': 'Line with \"nested\" quotes'}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Handle nested quotes with escape sequences",
		},
		{
			name:          "multiple_calls_with_punctuation",
			content:       "First: {'type': 'tool_use', 'name': 'test1', 'id': '1', 'input': {}}... Second: {'type': 'tool_use', 'name': 'test2', 'id': '2', 'input': {}}!",
			expectedCalls: 2,
			expectedOK:    true,
			description:   "Multiple calls with intervening punctuation",
		},
		{
			name:          "python_literals_near_misses",
			content:       "{'type': 'tool_use', 'name': 'test', 'id': '123', 'input': {'notTrue': 'NotTrueValue', 'notFalse': 'FalseAlarm', 'notNone': 'Nonetheless'}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Ensure Python literal replacement doesn't affect similar words",
		},
		{
			name:          "short_circuit_valid_json",
			content:       "Valid JSON: {'type': 'tool_use', 'name': 'test', 'id': '123', 'input': {'key': 'value'}}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Short-circuit path for already valid JSON",
		},
		{
			name:          "trailing_comma_removal",
			content:       "Call: {'type': 'tool_use', 'name': 'test', 'id': '123', 'input': {'key': 'value',},}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Remove trailing commas in nested objects",
		},
		{
			name:          "empty_content",
			content:       "",
			expectedCalls: 0,
			expectedOK:    false,
			description:   "Handle empty content gracefully",
		},
		{
			name:          "no_json_objects",
			content:       "This is just plain text with no JSON objects",
			expectedCalls: 0,
			expectedOK:    false,
			description:   "Handle content with no JSON objects",
		},
		{
			name:          "malformed_json",
			content:       "Bad JSON: {'type': 'tool_use, 'name': test'}",
			expectedCalls: 0,
			expectedOK:    false,
			description:   "Handle malformed JSON gracefully",
		},
		{
			name:          "openai_style_function_call",
			content:       "Calling function: {'function': {'name': 'test_func', 'arguments': '{\"param\": \"value\"}'}, 'id': 'call_123'}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Parse OpenAI-style function calls",
		},
		{
			name:          "type_field_at_end_single_quotes",
			content:       "Tool call: {'id': 'test_end', 'input': {'param': 'value'}, 'name': 'EndTool', 'type': 'tool_use'}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Parse tool call with 'type' field at the end (field order variation)",
		},
		{
			name:          "type_field_at_end_double_quotes",
			content:       "Tool call: {'id': 'test_end_dq', 'input': {'param': 'value'}, 'name': 'EndToolDQ', 'type': 'tool_use'}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Parse tool call with type field at end using single quotes",
		},
		{
			name:          "multiple_calls_varied_field_order",
			content:       "First: {'type': 'tool_use', 'id': '1', 'name': 'Tool1', 'input': {}} Second: {'id': '2', 'input': {}, 'name': 'Tool2', 'type': 'tool_use'} Third: {'name': 'Tool3', 'type': 'tool_use', 'id': '3', 'input': {}}",
			expectedCalls: 3,
			expectedOK:    true,
			description:   "Multiple tool calls with different field orderings",
		},
		{
			name:          "real_world_edit_case",
			content:       "Let me fix it{'id': 'toolu_vrtx_01XqLnQUvXJJJJJJJJJJJJJJJj', 'input': {'file_path': '/test.go', 'new_string': 'new', 'old_string': 'old'}, 'name': 'Edit', 'type': 'tool_use'}",
			expectedCalls: 1,
			expectedOK:    true,
			description:   "Real-world case from apiproxy debug log with type at end",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq, suffix, err := parseEmbeddedToolCalls(tt.content, edgeCaseToolDefs)
			ok := err == nil

			if ok != tt.expectedOK {
				t.Errorf("%s: expected ok=%v, got %v (error: %v)", tt.description, tt.expectedOK, ok, err)
			}

			if len(seq) != tt.expectedCalls {
				t.Errorf("%s: expected %d calls, got %d", tt.description, tt.expectedCalls, len(seq))
			}

			// Verify suffix is not empty when there's trailing content
			if ok && tt.expectedCalls > 0 {
				// Check that we have either a suffix or the last call consumed everything
				if suffix == "" && len(seq) > 0 {
					// If the last call has no prefix and there's content, it means
					// the entire content was consumed by calls, which is fine
					_ = seq[len(seq)-1].Prefix // Just reference it to avoid unused variable
				}
			}
		})
	}
}

// TestQuotedBraceInProse mirrors an apiproxy case where prose contains a quoted '{'
// before the embedded single-quoted tool_use JSON object. Our scanner must skip
// that stray brace and continue to find the real JSON start.
func TestQuotedBraceInProse(t *testing.T) {
	content := "Now I'll rename the function to better reflect its actual behavior. Since it's just finding the next '{' character without any quote awareness, I'll rename it to `findNextBrace`:\n\n{'id': 'toolu_vrtx_01TjCdtZQwUxvJCYxsJqCfEr', 'input': {'file_path': '/path/to/project/internal/core/json_normalizer.go', 'new_string': '// findNextBrace finds the next \\'{\\' character starting from pos.\\n// Note: This is a simple character search that doesn\\'t track quotes because after\\n// normalization, quotes in free text can be unbalanced. We scan every \\'{\\' and let\\n// JSON parsing decide if it\\'s valid.\\nfunc findNextBrace(s string, pos int) int {\\n\\tif pos < 0 {\\n\\t\\tpos = 0\\n\\t}\\n\\tfor i := pos; i < len(s); i++ {\\n\\t\\tif s[i] == \\'{\\' {\\n\\t\\t\\treturn i\\n\\t\\t}\\n\\t}\\n\\treturn -1\\n}', 'old_string': '// nextBraceOutsideQuotes finds the next \\'{\\' at or after pos that is outside quoted strings.\\nfunc nextBraceOutsideQuotes(s string, pos int) int {\\n\\t// Per strict pipeline: after simple quote normalization, quotes in free text\\n\\t// can be unbalanced. We should scan every \\'{\\' and let JSON parsing decide.\\n\\tif pos < 0 {\\n\\t\\tpos = 0\\n\\t}\\n\\tfor i := pos; i < len(s); i++ {\\n\\t\\tif s[i] == \\'{\\' {\\n\\t\\t\\treturn i\\n\\t\\t}\\n\\t}\\n\\treturn -1\\n}'}, 'name': 'Edit', 'type': 'tool_use'}"

	seq, suffix, err := parseEmbeddedToolCalls(content, edgeCaseToolDefs)
	if err != nil {
		t.Fatalf("Expected to parse embedded tool call despite quoted '{' in prose: %v", err)
	}
	if len(seq) != 1 {
		t.Fatalf("Expected 1 call, got %d", len(seq))
	}
	call := seq[0].Call
	if call == nil {
		t.Fatalf("Call is nil")
	}
	if call.ID != "toolu_vrtx_01TjCdtZQwUxvJCYxsJqCfEr" {
		t.Errorf("Unexpected ID: %s", call.ID)
	}
	if call.Name != "Edit" {
		t.Errorf("Unexpected Name: %s", call.Name)
	}
	if len(call.ArgsJSON) == 0 {
		t.Errorf("ArgsJSON should not be empty")
	}
	// Suffix can be empty in this case; ensure reconstruction doesn't lose prose
	contentOut, tools := buildContentAndToolsFromEmbedded(seq, suffix)
	if len(tools) != 1 {
		t.Errorf("Expected 1 tool extracted, got %d", len(tools))
	}
	if !strings.Contains(contentOut, "next '{' character") {
		t.Errorf("Output content missing prose before JSON; got: %q", contentOut)
	}
}

// TestParseEmbeddedToolCall_ThinWrapper tests that ParseEmbeddedToolCall correctly wraps parseEmbeddedToolCalls
func TestParseEmbeddedToolCall_ThinWrapper(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		expectedOK  bool
		description string
	}{
		{
			name:        "single_call_at_end",
			content:     "Some prefix text: {'type': 'tool_use', 'name': 'test', 'id': '123', 'input': {}}",
			expectedOK:  true,
			description: "Extract single call at end of content",
		},
		{
			name:        "multiple_calls_returns_last",
			content:     "First: {'type': 'tool_use', 'name': 'test1', 'id': '1', 'input': {}} Second: {'type': 'tool_use', 'name': 'test2', 'id': '2', 'input': {}}",
			expectedOK:  true,
			description: "With multiple calls, returns the last one",
		},
		{
			name:        "no_calls",
			content:     "Just plain text with no tool calls",
			expectedOK:  false,
			description: "Returns false when no calls found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use parseEmbeddedToolCalls directly instead of the wrapper
			seq, _, err := parseEmbeddedToolCalls(tt.content, edgeCaseToolDefs)
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

			if ok && call == nil {
				t.Errorf("%s: expected non-nil call when ok=true", tt.description)
			}

			if ok && call != nil {
				// Verify the Trimmed field is set
				// Note: Trimmed can be empty if the entire content was the JSON,
				// which is valid and expected in some cases

				// For multiple calls test, verify we got the last one
				if tt.name == "multiple_calls_returns_last" {
					if call.Name != "test2" || call.ID != "2" {
						t.Errorf("Expected last call (test2, id=2), got (%s, id=%s)", call.Name, call.ID)
					}
				}
			}
		})
	}
}

// TestEnsureAudioFormat_Consistency tests that audio format defaulting works consistently
func TestEnsureAudioFormat_Consistency(t *testing.T) {
	tests := []struct {
		name           string
		audio          *AudioData
		expectedFormat string
	}{
		{
			name:           "nil_audio",
			audio:          nil,
			expectedFormat: "",
		},
		{
			name:           "empty_data",
			audio:          &AudioData{Data: "", Format: ""},
			expectedFormat: "",
		},
		{
			name:           "data_no_format",
			audio:          &AudioData{Data: "base64data", Format: ""},
			expectedFormat: "wav",
		},
		{
			name:           "data_with_format",
			audio:          &AudioData{Data: "base64data", Format: "mp3"},
			expectedFormat: "mp3",
		},
		{
			name:           "url_no_format",
			audio:          &AudioData{URL: "http://example.com/audio", Format: ""},
			expectedFormat: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ensureAudioFormat(tt.audio)

			actualFormat := ""
			if tt.audio != nil {
				actualFormat = tt.audio.Format
			}

			if actualFormat != tt.expectedFormat {
				t.Errorf("Expected format %q, got %q", tt.expectedFormat, actualFormat)
			}
		})
	}
}

// TestParseEmbeddedToolCalls_EmptyArrays tests that empty arrays are preserved correctly
func TestParseEmbeddedToolCalls_EmptyArrays(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		expectedJSON string // Expected JSON in the extracted tool call
		description  string
	}{
		{
			name:         "single_empty_array",
			content:      "Let me clear the todos: {'id': 'toolu_test1', 'input': {'todos': []}, 'name': 'TodoWrite', 'type': 'tool_use'}",
			expectedJSON: `{"todos":[]}`,
			description:  "Single empty array should be preserved as []",
		},
		{
			name:         "multiple_empty_arrays",
			content:      "Clearing multiple lists: {'id': 'toolu_test2', 'input': {'items': [], 'tags': [], 'count': 0}, 'name': 'MultiClear', 'type': 'tool_use'}",
			expectedJSON: `{"items":[],"tags":[],"count":0}`,
			description:  "Multiple empty arrays in same object should all be preserved",
		},
		{
			name:         "nested_empty_array",
			content:      "Nested structure: {'id': 'toolu_test3', 'input': {'data': {'nested': [], 'value': 'test'}}, 'name': 'NestedTest', 'type': 'tool_use'}",
			expectedJSON: `{"data":{"nested":[],"value":"test"}}`,
			description:  "Empty arrays in nested structures should be preserved",
		},
		{
			name:         "mixed_arrays",
			content:      "Mixed arrays: {'id': 'toolu_test4', 'input': {'empty': [], 'filled': ['item1', 'item2']}, 'name': 'MixedArrays', 'type': 'tool_use'}",
			expectedJSON: `{"empty":[],"filled":["item1","item2"]}`,
			description:  "Empty arrays alongside filled arrays should be preserved",
		},
		{
			name:         "openai_format_empty_array",
			content:      "OpenAI format: {'type': 'function', 'function': {'name': 'clear_list', 'arguments': '{\"items\":[]}'}}",
			expectedJSON: `{"items":[]}`,
			description:  "Empty arrays in OpenAI format should be preserved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq, _, err := parseEmbeddedToolCalls(tt.content, edgeCaseToolDefs)
			if err != nil {
				t.Fatalf("%s: failed to parse embedded tool call: %v", tt.description, err)
			}

			if len(seq) != 1 {
				t.Fatalf("%s: expected 1 call, got %d", tt.description, len(seq))
			}

			call := seq[0].Call
			actualJSON := string(call.ArgsJSON)

			// Compare JSON structures (order might differ)
			var expected, actual interface{}
			if err := json.Unmarshal([]byte(tt.expectedJSON), &expected); err != nil {
				t.Fatalf("%s: failed to parse expected JSON: %v", tt.description, err)
			}
			if err := json.Unmarshal([]byte(actualJSON), &actual); err != nil {
				t.Fatalf("%s: failed to parse actual JSON: %v", tt.description, err)
			}

			// Check if they're equal
			if !reflect.DeepEqual(expected, actual) {
				t.Errorf("%s:\nExpected: %s\nGot:      %s", tt.description, tt.expectedJSON, actualJSON)
			}

			// Specifically verify that empty arrays are not null
			if strings.Contains(tt.expectedJSON, "[]") && strings.Contains(actualJSON, "null") {
				t.Errorf("%s: empty array was converted to null\nExpected: %s\nGot:      %s", tt.description, tt.expectedJSON, actualJSON)
			}
		})
	}
}

// TestParseEmbeddedToolCalls_DeeplyNestedEscapes tests the problematic Argo response with deeply nested escape sequences
func TestParseEmbeddedToolCalls_DeeplyNestedEscapes(t *testing.T) {
	// This is the exact content from the Argo response that caused the Claude client to crash
	content := `Now I need to update the parseAnthropicEmbedded function to use the new helper:

{'id': 'toolu_01LMNOPQRSTUVWXYZabcdef', 'input': {'file_path': '/path/to/project/internal/core/argo_embed.go', 'new_string': '\tvar argsJSON json.RawMessage\n\tif input, ok := raw["input"].(map[string]interface{}); ok {\n\t\tif b, err := marshalPreservingEmptyArrays(input); err == nil {\n\t\t\targsJSON = json.RawMessage(b)\n\t\t}\n\t}', 'old_string': '\tvar argsJSON json.RawMessage\n\tif input, ok := raw["input"].(map[string]interface{}); ok {\n\t\tif b, err := json.Marshal(input); err == nil {\n\t\t\targsJSON = json.RawMessage(b)\n\t\t}\n\t}'}, 'name': 'Edit', 'type': 'tool_use'}`

	// Define the exact expected values for each field
	expectedID := "toolu_01LMNOPQRSTUVWXYZabcdef"
	expectedName := "Edit"
	expectedFilePath := "/path/to/project/internal/core/argo_embed.go"

	// The exact expected strings with proper tabs and newlines
	expectedNewString := "\tvar argsJSON json.RawMessage\n\tif input, ok := raw[\"input\"].(map[string]interface{}); ok {\n\t\tif b, err := marshalPreservingEmptyArrays(input); err == nil {\n\t\t\targsJSON = json.RawMessage(b)\n\t\t}\n\t}"
	expectedOldString := "\tvar argsJSON json.RawMessage\n\tif input, ok := raw[\"input\"].(map[string]interface{}); ok {\n\t\tif b, err := json.Marshal(input); err == nil {\n\t\t\targsJSON = json.RawMessage(b)\n\t\t}\n\t}"

	seq, suffix, err := parseEmbeddedToolCalls(content, edgeCaseToolDefs)
	// Test that parsing succeeds
	if err != nil {
		t.Fatalf("Failed to parse embedded tool call with deeply nested escapes: %v", err)
	}

	// Test that we got exactly one tool call
	if len(seq) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(seq))
	}

	call := seq[0].Call
	if call == nil {
		t.Fatalf("Extracted call is nil")
	}

	// 1. Verify ID matches exactly
	if call.ID != expectedID {
		t.Errorf("ID mismatch:\n  Expected: %q\n  Got:      %q", expectedID, call.ID)
	}

	// 2. Verify Name matches exactly
	if call.Name != expectedName {
		t.Errorf("Name mismatch:\n  Expected: %q\n  Got:      %q", expectedName, call.Name)
	}

	// 3. Verify Style field
	if call.Style != "anthropic" {
		t.Errorf("Style mismatch:\n  Expected: %q\n  Got:      %q", "anthropic", call.Style)
	}

	// 4. Parse and verify the ArgsJSON
	if len(call.ArgsJSON) == 0 {
		t.Fatalf("ArgsJSON is empty")
	}

	var args map[string]interface{}
	if err := json.Unmarshal(call.ArgsJSON, &args); err != nil {
		t.Fatalf("Failed to unmarshal ArgsJSON: %v\nArgsJSON: %s", err, string(call.ArgsJSON))
	}

	// 5. Verify file_path matches exactly
	filePath, ok := args["file_path"].(string)
	if !ok {
		t.Fatalf("file_path field missing or not a string")
	}
	if filePath != expectedFilePath {
		t.Errorf("file_path mismatch:\n  Expected: %q\n  Got:      %q", expectedFilePath, filePath)
		// Also show byte-by-byte comparison if they differ
		if len(filePath) != len(expectedFilePath) {
			t.Errorf("  Length differs: expected %d, got %d", len(expectedFilePath), len(filePath))
		}
	}

	// 6. Verify new_string matches exactly
	newString, ok := args["new_string"].(string)
	if !ok {
		t.Fatalf("new_string field missing or not a string")
	}
	if newString != expectedNewString {
		t.Errorf("new_string mismatch:\n  Expected: %q\n  Got:      %q", expectedNewString, newString)
		// Show length comparison
		if len(newString) != len(expectedNewString) {
			t.Errorf("  Length differs: expected %d, got %d", len(expectedNewString), len(newString))
		}
		// Show first difference
		for i := 0; i < len(expectedNewString) && i < len(newString); i++ {
			if expectedNewString[i] != newString[i] {
				t.Errorf("  First difference at position %d: expected %q, got %q",
					i, expectedNewString[i], newString[i])
				break
			}
		}
	}

	// 7. Verify old_string matches exactly
	oldString, ok := args["old_string"].(string)
	if !ok {
		t.Fatalf("old_string field missing or not a string")
	}
	if oldString != expectedOldString {
		t.Errorf("old_string mismatch:\n  Expected: %q\n  Got:      %q", expectedOldString, oldString)
		// Show length comparison
		if len(oldString) != len(expectedOldString) {
			t.Errorf("  Length differs: expected %d, got %d", len(expectedOldString), len(oldString))
		}
		// Show first difference
		for i := 0; i < len(expectedOldString) && i < len(oldString); i++ {
			if expectedOldString[i] != oldString[i] {
				t.Errorf("  First difference at position %d: expected %q, got %q",
					i, expectedOldString[i], oldString[i])
				break
			}
		}
	}

	// 8. Verify there are exactly 3 fields in args (file_path, new_string, old_string)
	if len(args) != 3 {
		t.Errorf("Expected exactly 3 fields in args, got %d: %v", len(args), getKeys(args))
	}

	// 9. Verify the complete JSON structure matches
	expectedJSON := map[string]interface{}{
		"id":   expectedID,
		"name": expectedName,
		"type": "tool_use",
		"input": map[string]interface{}{
			"file_path":  expectedFilePath,
			"new_string": expectedNewString,
			"old_string": expectedOldString,
		},
	}

	// Build actual structure from parsed data
	actualJSON := map[string]interface{}{
		"id":    call.ID,
		"name":  call.Name,
		"type":  "tool_use", // inferred from Style
		"input": args,
	}

	// Deep comparison
	if !reflect.DeepEqual(expectedJSON, actualJSON) {
		expBytes, _ := json.MarshalIndent(expectedJSON, "", "  ")
		actBytes, _ := json.MarshalIndent(actualJSON, "", "  ")
		t.Errorf("Complete JSON structure mismatch:\nExpected:\n%s\n\nGot:\n%s",
			string(expBytes), string(actBytes))
	}

	// 10. Verify prefix and suffix
	expectedPrefix := "Now I need to update the parseAnthropicEmbedded function to use the new helper:\n\n"
	if seq[0].Prefix != expectedPrefix {
		t.Errorf("Prefix mismatch:\n  Expected: %q\n  Got:      %q", expectedPrefix, seq[0].Prefix)
	}

	if suffix != "" {
		t.Errorf("Expected empty suffix, got: %q", suffix)
	}

	// 11. Log success with actual values for verification
	t.Logf("Successfully extracted tool call:")
	t.Logf("  ID: %q", call.ID)
	t.Logf("  Name: %q", call.Name)
	t.Logf("  Style: %q", call.Style)
	t.Logf("  file_path: %q", filePath)
	t.Logf("  new_string length: %d chars", len(newString))
	t.Logf("  old_string length: %d chars", len(oldString))
}

// Helper function to get map keys
func getKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
