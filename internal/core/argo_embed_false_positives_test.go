package core

import (
	"fmt"
	"testing"
)

// falsePositiveToolDefs provides tool definitions for false positive tests
// These include tools that appear in the valid test cases
var falsePositiveToolDefs = []ToolDefinition{
	{Name: "Edit"},
	{Name: "test_func"},
	{Name: "List"},
}

// TestParseEmbeddedToolCalls_FalsePositives tests JSON objects that should NOT be detected as tool calls
// These tests verify that the parser correctly rejects JSON that doesn't match the strict tool call structure
func TestParseEmbeddedToolCalls_FalsePositives(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		description string
	}{
		// Anthropic format false positives - missing required fields
		{
			name:        "anthropic_missing_id",
			content:     `Here's some code: {'type': 'tool_use', 'name': 'Edit', 'input': {'file': 'test.go'}}`,
			description: "Anthropic format without 'id' field should be rejected",
		},
		{
			name:        "anthropic_missing_input",
			content:     `Tool call: {'type': 'tool_use', 'name': 'Edit', 'id': 'toolu_123'}`,
			description: "Anthropic format without 'input' field should be rejected",
		},
		{
			name:        "anthropic_empty_id",
			content:     `Tool: {'type': 'tool_use', 'name': 'Edit', 'id': '', 'input': {}}`,
			description: "Anthropic format with empty 'id' should be rejected",
		},
		{
			name:        "anthropic_null_input",
			content:     `Call: {'type': 'tool_use', 'name': 'Edit', 'id': 'toolu_123', 'input': null}`,
			description: "Anthropic format with null 'input' should be rejected",
		},
		{
			name:        "anthropic_input_not_object",
			content:     `Tool: {'type': 'tool_use', 'name': 'Edit', 'id': 'toolu_123', 'input': 'not an object'}`,
			description: "Anthropic format with non-object 'input' should be rejected",
		},
		{
			name:        "anthropic_input_is_array",
			content:     `Tool: {'type': 'tool_use', 'name': 'Edit', 'id': 'toolu_123', 'input': ['item1', 'item2']}`,
			description: "Anthropic format with array 'input' should be rejected",
		},

		// OpenAI format false positives - missing required fields
		{
			name:        "openai_missing_arguments",
			content:     `Function: {'function': {'name': 'test_func'}, 'id': 'call_123'}`,
			description: "OpenAI format without 'arguments' should be rejected",
		},
		{
			name:        "openai_toplevel_missing_arguments",
			content:     `Call: {'name': 'test_func', 'id': 'call_123'}`,
			description: "OpenAI top-level format without 'arguments' should be rejected",
		},
		{
			name:        "openai_null_arguments",
			content:     `Function: {'function': {'name': 'test_func', 'arguments': null}, 'id': 'call_123'}`,
			description: "OpenAI format with null 'arguments' should be rejected",
		},
		{
			name:        "openai_arguments_not_valid",
			content:     `Function: {'function': {'name': 'test_func', 'arguments': 123}, 'id': 'call_123'}`,
			description: "OpenAI format with numeric 'arguments' should be rejected",
		},
		{
			name:        "openai_arguments_boolean",
			content:     `Function: {'function': {'name': 'test_func', 'arguments': true}, 'id': 'call_123'}`,
			description: "OpenAI format with boolean 'arguments' should be rejected",
		},

		// Code-like structures that look like tool calls but aren't
		{
			name:        "code_example_anthropic_like",
			content:     `In the code: part.Call = {'type': 'tool_use', 'name': part.Call.Name}`,
			description: "Code assignment that looks like tool_use should be rejected (missing required fields)",
		},
		{
			name:        "code_struct_definition",
			content:     `Define struct: {'type': 'Message', 'name': 'UserMessage', 'content': 'hello'}`,
			description: "Struct-like JSON should not be detected as tool call",
		},
		{
			name:        "json_api_response",
			content:     `API returned: {'type': 'error', 'name': 'ValidationError', 'message': 'Invalid input'}`,
			description: "API error response should not be detected as tool call",
		},
		{
			name:        "config_object",
			content:     `Config: {'type': 'settings', 'name': 'app_config', 'values': {'debug': true}}`,
			description: "Configuration object should not be detected as tool call",
		},

		// Invalid type field values
		{
			name:        "wrong_type_value",
			content:     `Call: {'type': 'function_call', 'name': 'Edit', 'id': 'test_123', 'input': {}}`,
			description: "Wrong 'type' value (not 'tool_use') should be rejected",
		},
		{
			name:        "type_is_tool_call",
			content:     `Call: {'type': 'tool_call', 'name': 'Edit', 'id': 'test_123', 'input': {}}`,
			description: "'tool_call' instead of 'tool_use' should be rejected",
		},
		{
			name:        "type_is_function",
			content:     `Call: {'type': 'function', 'name': 'Edit', 'id': 'test_123', 'input': {}}`,
			description: "'function' as type (OpenAI uses it differently) should be rejected",
		},

		// Mixed/malformed structures
		{
			name:        "mixed_anthropic_openai",
			content:     `Call: {'type': 'tool_use', 'function': {'name': 'Edit'}, 'arguments': '{}'}`,
			description: "Mixed Anthropic/OpenAI structure should be rejected",
		},
		// TODO: Future enhancement - reject nested tool calls
		// These test cases are commented out as they require deeper structural validation
		// that would need to track the JSON parsing context
		/*
			{
				name:        "nested_tool_use",
				content:     `Nested: {'data': {'type': 'tool_use', 'name': 'Edit', 'id': 'test', 'input': {}}}`,
				description: "Tool use nested in another object should be rejected",
			},
			{
				name:        "array_of_tool_uses",
				content:     `Array: [{'type': 'tool_use', 'name': 'Edit', 'id': 'test', 'input': {}}]`,
				description: "Tool use inside array should be rejected",
			},
		*/

		// Edge cases with valid-looking but incomplete data
		{
			name:        "only_type_field",
			content:     `Minimal: {'type': 'tool_use'}`,
			description: "Only 'type' field present should be rejected",
		},
		{
			name:        "only_name_field",
			content:     `Name only: {'name': 'Edit'}`,
			description: "Only 'name' field present should be rejected",
		},
		{
			name:        "type_and_name_only",
			content:     `Partial: {'type': 'tool_use', 'name': 'Edit'}`,
			description: "Type and name without id/input should be rejected",
		},

		// Real-world false positives from logs
		{
			name:        "log_message_with_type",
			content:     `Logger output: {'type': 'info', 'name': 'system', 'message': 'Started successfully'}`,
			description: "Log message structure should not be detected",
		},
		{
			name:        "database_record",
			content:     `Record: {'type': 'user', 'name': 'John Doe', 'id': '12345', 'email': 'john@example.com'}`,
			description: "Database record should not be detected",
		},
		{
			name:        "graphql_query",
			content:     `Query: {'type': 'query', 'name': 'GetUser', 'variables': {'id': '123'}}`,
			description: "GraphQL query should not be detected",
		},

		// Python code examples that might be picked up
		{
			name:        "python_dict_literal",
			content:     `Python code: my_dict = {'type': 'tool_use', 'name': variable_name}`,
			description: "Python dict literal in code should be rejected (incomplete)",
		},
		{
			name:        "python_function_call",
			content:     `Python: process({'type': response_type, 'name': get_name(), 'data': {}})`,
			description: "Python function call with dict should be rejected",
		},

		// JSON with unsupported tool names (if we implement whitelisting)
		// TODO: Future enhancement - tool name validation/whitelisting
		// These test cases are commented out as they require implementing
		// a whitelist of known tools or validation of tool names
		/*
			{
				name:        "unknown_tool_name",
				content:     `Call: {'type': 'tool_use', 'name': 'UnknownTool', 'id': 'test_123', 'input': {}}`,
				description: "Unknown tool name (when whitelisting enabled) should be rejected",
			},
			{
				name:        "suspicious_tool_name",
				content:     `Call: {'type': 'tool_use', 'name': '../../../etc/passwd', 'id': 'test_123', 'input': {}}`,
				description: "Suspicious tool name should be rejected",
			},
		*/
		{
			name:        "empty_tool_name",
			content:     `Call: {'type': 'tool_use', 'name': '', 'id': 'test_123', 'input': {}}`,
			description: "Empty tool name should be rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq, _, err := parseEmbeddedToolCalls(tt.content, falsePositiveToolDefs)

			// All these should fail to parse as tool calls
			if err == nil && len(seq) > 0 {
				t.Errorf("%s: incorrectly detected as tool call", tt.description)
				if len(seq) > 0 && seq[0].Call != nil {
					t.Errorf("  Detected as: Style=%s, Name=%s, ID=%s",
						seq[0].Call.Style, seq[0].Call.Name, seq[0].Call.ID)
				}
			}
		})
	}
}

// TestParseEmbeddedToolCalls_StrictValidation tests that only properly structured tool calls are accepted
func TestParseEmbeddedToolCalls_StrictValidation(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		shouldPass   bool
		description  string
		validateCall func(*EmbeddedCall) error
	}{
		{
			name:        "valid_anthropic_all_fields",
			content:     `Tool: {'type': 'tool_use', 'name': 'Edit', 'id': 'toolu_123', 'input': {'file': 'test.go'}}`,
			shouldPass:  true,
			description: "Valid Anthropic format with all required fields",
			validateCall: func(call *EmbeddedCall) error {
				if call.ID != "toolu_123" {
					return fmt.Errorf("ID should be 'toolu_123', got %s", call.ID)
				}
				if call.Name != "Edit" {
					return fmt.Errorf("Name should be 'Edit', got %s", call.Name)
				}
				if len(call.ArgsJSON) == 0 {
					return fmt.Errorf("ArgsJSON should not be empty")
				}
				return nil
			},
		},
		{
			name:        "valid_openai_nested",
			content:     `Function: {'function': {'name': 'test_func', 'arguments': '{"param": "value"}'}, 'id': 'call_123'}`,
			shouldPass:  true,
			description: "Valid OpenAI format with nested function",
			validateCall: func(call *EmbeddedCall) error {
				if call.Name != "test_func" {
					return fmt.Errorf("Name should be 'test_func', got %s", call.Name)
				}
				if call.ID != "call_123" {
					return fmt.Errorf("ID should be 'call_123', got %s", call.ID)
				}
				return nil
			},
		},
		{
			name:        "valid_openai_toplevel",
			content:     `Function: {'name': 'test_func', 'arguments': '{"param": "value"}', 'id': 'call_456'}`,
			shouldPass:  true,
			description: "Valid OpenAI format at top level",
			validateCall: func(call *EmbeddedCall) error {
				if call.Name != "test_func" {
					return fmt.Errorf("Name should be 'test_func', got %s", call.Name)
				}
				return nil
			},
		},
		{
			name:        "anthropic_with_empty_input",
			content:     `Tool: {'type': 'tool_use', 'name': 'List', 'id': 'toolu_456', 'input': {}}`,
			shouldPass:  true,
			description: "Valid Anthropic format with empty input object",
			validateCall: func(call *EmbeddedCall) error {
				if string(call.ArgsJSON) != "{}" {
					return fmt.Errorf("ArgsJSON should be '{}', got %s", string(call.ArgsJSON))
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq, _, err := parseEmbeddedToolCalls(tt.content, falsePositiveToolDefs)

			if tt.shouldPass {
				if err != nil || len(seq) == 0 {
					t.Errorf("%s: should have been detected as valid tool call", tt.description)
					return
				}

				if tt.validateCall != nil {
					if err := tt.validateCall(seq[0].Call); err != nil {
						t.Errorf("%s: validation failed: %v", tt.description, err)
					}
				}
			} else {
				if err == nil && len(seq) > 0 {
					t.Errorf("%s: should have been rejected", tt.description)
				}
			}
		})
	}
}

// TestParseEmbeddedToolCalls_FieldTypeValidation tests that field types are properly validated
func TestParseEmbeddedToolCalls_FieldTypeValidation(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		shouldFail  bool
		description string
	}{
		// ID field type validation
		{
			name:        "id_as_number",
			content:     `Tool: {'type': 'tool_use', 'name': 'Edit', 'id': 12345, 'input': {}}`,
			shouldFail:  true,
			description: "ID as number should be rejected",
		},
		{
			name:        "id_as_boolean",
			content:     `Tool: {'type': 'tool_use', 'name': 'Edit', 'id': true, 'input': {}}`,
			shouldFail:  true,
			description: "ID as boolean should be rejected",
		},
		{
			name:        "id_as_object",
			content:     `Tool: {'type': 'tool_use', 'name': 'Edit', 'id': {'value': 'test'}, 'input': {}}`,
			shouldFail:  true,
			description: "ID as object should be rejected",
		},

		// Name field type validation
		{
			name:        "name_as_number",
			content:     `Tool: {'type': 'tool_use', 'name': 123, 'id': 'test', 'input': {}}`,
			shouldFail:  true,
			description: "Name as number should be rejected",
		},
		{
			name:        "name_as_boolean",
			content:     `Tool: {'type': 'tool_use', 'name': false, 'id': 'test', 'input': {}}`,
			shouldFail:  true,
			description: "Name as boolean should be rejected",
		},
		{
			name:        "name_as_array",
			content:     `Tool: {'type': 'tool_use', 'name': ['Edit', 'Tool'], 'id': 'test', 'input': {}}`,
			shouldFail:  true,
			description: "Name as array should be rejected",
		},

		// Type field validation
		{
			name:        "type_as_number",
			content:     `Tool: {'type': 1, 'name': 'Edit', 'id': 'test', 'input': {}}`,
			shouldFail:  true,
			description: "Type as number should be rejected",
		},
		{
			name:        "type_as_array",
			content:     `Tool: {'type': ['tool_use'], 'name': 'Edit', 'id': 'test', 'input': {}}`,
			shouldFail:  true,
			description: "Type as array should be rejected",
		},

		// Input/Arguments field type validation
		{
			name:        "input_as_number",
			content:     `Tool: {'type': 'tool_use', 'name': 'Edit', 'id': 'test', 'input': 42}`,
			shouldFail:  true,
			description: "Input as number should be rejected",
		},
		{
			name:        "input_as_boolean",
			content:     `Tool: {'type': 'tool_use', 'name': 'Edit', 'id': 'test', 'input': false}`,
			shouldFail:  true,
			description: "Input as boolean should be rejected",
		},
		{
			name:        "arguments_as_array",
			content:     `Function: {'name': 'test', 'arguments': ['arg1', 'arg2'], 'id': 'call_123'}`,
			shouldFail:  true,
			description: "Arguments as array should be rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq, _, err := parseEmbeddedToolCalls(tt.content, falsePositiveToolDefs)

			if tt.shouldFail {
				if err == nil && len(seq) > 0 {
					t.Errorf("%s: should have been rejected due to invalid field type", tt.description)
					if len(seq) > 0 && seq[0].Call != nil {
						t.Errorf("  Incorrectly accepted with: Name=%s, ID=%s",
							seq[0].Call.Name, seq[0].Call.ID)
					}
				}
			}
		})
	}
}
