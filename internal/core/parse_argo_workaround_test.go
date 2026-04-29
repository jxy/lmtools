package core

import (
	"strings"
	"testing"
)

// Tests verify that embedded tool calls are NOT extracted when no tool definitions are provided.
// This is the expected behavior after removing backward compatibility - tool validation requires
// valid tool definitions, and without them, embedded tools remain as content.

// Verifies embedded tool_use JSON is NOT extracted when no tool definitions are provided
func TestParseArgoResponse_Workaround_AnthropicToolUseEmbeddedInContent(t *testing.T) {
	resp := `{
        "response": {
            "content": "Now let me read the openai_convert.go file to understand the current ConvertBlocksToOpenAIContent implementation:{'type': 'tool_use', 'id': 'toolu_vrtx_01TCVSw8Ff8eJHs5nSaZsPBt', 'name': 'Read', 'input': {'file_path': '/path/to/project/internal/core/openai_convert.go'}}",
            "tool_calls": []
        }
    }`

	text, tools, err := parseArgoResponseWithTools([]byte(resp), false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Without tool definitions, embedded tools are not extracted - content remains unchanged
	if !strings.Contains(text, "tool_use") {
		t.Errorf("Expected embedded tool_use to remain in content, got %q", text)
	}

	// No tool calls should be extracted without tool definitions
	if len(tools) != 0 {
		t.Fatalf("Expected 0 tool calls without tool definitions, got %d", len(tools))
	}
}

// Verifies embedded OpenAI-style function call is NOT extracted when no tool definitions are provided
func TestParseArgoResponse_Workaround_OpenAIFunctionEmbeddedInContent(t *testing.T) {
	resp := `{
        "response": {
            "content": "I'll run a command for you:{\n  'id': 'call_123',\n  'type': 'function',\n  'function': {\n    'name': 'universal_command',\n    'arguments': '{\"command\":[\"ls\",\"-la\"]}'\n  }\n}",
            "tool_calls": []
        }
    }`

	text, tools, err := parseArgoResponseWithTools([]byte(resp), false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Without tool definitions, embedded tools are not extracted - content remains unchanged
	if !strings.Contains(text, "universal_command") {
		t.Errorf("Expected embedded function to remain in content, got %q", text)
	}

	// No tool calls should be extracted without tool definitions
	if len(tools) != 0 {
		t.Fatalf("Expected 0 tool calls without tool definitions, got %d", len(tools))
	}
}

func TestParseArgoResponse_LegacyOptionsExtractEmbeddedFunction(t *testing.T) {
	resp := `{
        "response": {
            "content": "I'll run it:{'id':'call_123','type':'function','function':{'name':'universal_command','arguments':'{\"command\":[\"echo\",\"ok\"]}'}}",
            "tool_calls": []
        }
    }`

	text, tools, err := parseArgoResponseWithToolsOptions([]byte(resp), false, ArgoResponseParseOptions{
		ExtractEmbeddedTools: true,
		ToolDefs:             GetBuiltinUniversalCommandTool(),
	})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if strings.Contains(text, "universal_command") {
		t.Fatalf("embedded tool should be removed from text, got %q", text)
	}
	if len(tools) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(tools))
	}
	if tools[0].Name != "universal_command" {
		t.Fatalf("tool name = %q, want universal_command", tools[0].Name)
	}
	if string(tools[0].Args) != `{"command":["echo","ok"]}` {
		t.Fatalf("tool args = %s", tools[0].Args)
	}
}

// Verifies Python-style embedded block is NOT extracted when no tool definitions are provided
func TestParseArgoResponse_Workaround_PythonLiteralsAndTrailingComma(t *testing.T) {
	resp := `{
        "response": {
            "content": "Grep the code:{'type': 'tool_use', 'id': 'toolu_vrtx_01Lqh8RQBCiMqYRCYkxRsXjf', 'name': 'Grep', 'input': {'glob': '*.go', 'output_mode': 'content', '-A': 30, '-n': True, 'path': '/path/to/project/internal/proxy', 'pattern': 'func.*streamArgoResponseContent',}},",
            "tool_calls": []
        }
    }`

	text, tools, err := parseArgoResponseWithTools([]byte(resp), false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Without tool definitions, embedded tools are not extracted - content remains unchanged
	if !strings.Contains(text, "Grep") {
		t.Errorf("Expected embedded tool_use to remain in content, got %q", text)
	}

	// No tool calls should be extracted without tool definitions
	if len(tools) != 0 {
		t.Fatalf("Expected 0 tool calls without tool definitions, got %d", len(tools))
	}
}

// Verifies missing tool_calls field with embedded tool_use - still NOT extracted without definitions
func TestParseArgoResponse_Workaround_MissingToolCallsField(t *testing.T) {
	resp := `{
        "response": {
            "content": "Search code with tool: {'type': 'tool_use', 'id': 'toolu_vrtx_01MissingTC', 'name': 'Grep', 'input': {'glob': '*.go', 'pattern': 'ParseEmbeddedToolCall', 'path': '/path/to/project/internal/core'}}"
        }
    }`

	text, tools, err := parseArgoResponseWithTools([]byte(resp), false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Without tool definitions, embedded tools are not extracted - content remains unchanged
	if !strings.Contains(text, "Grep") {
		t.Errorf("Expected embedded tool_use to remain in content, got %q", text)
	}

	// No tool calls should be extracted without tool definitions
	if len(tools) != 0 {
		t.Fatalf("Expected 0 tool calls without tool definitions, got %d", len(tools))
	}
}

// Verifies multiple embedded tool_use objects are NOT extracted when no tool definitions are provided
func TestParseArgoResponse_Workaround_MultipleEmbeddedCalls(t *testing.T) {
	resp := `{
        "response": {
            "content": "Step 1: read file:{'type': 'tool_use', 'id': 'toolu_r1', 'name': 'Read', 'input': {'file_path': '/path/a'}} Next, grep it:{'type': 'tool_use', 'id': 'toolu_r2', 'name': 'Grep', 'input': {'pattern': 'foo', 'glob': '*.go'}}"
        }
    }`

	text, tools, err := parseArgoResponseWithTools([]byte(resp), false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Without tool definitions, embedded tools are not extracted - content remains unchanged
	if !strings.Contains(text, "Read") || !strings.Contains(text, "Grep") {
		t.Errorf("Expected embedded tool_use blocks to remain in content, got %q", text)
	}

	// No tool calls should be extracted without tool definitions
	if len(tools) != 0 {
		t.Fatalf("Expected 0 tool calls without tool definitions, got %d", len(tools))
	}
}
