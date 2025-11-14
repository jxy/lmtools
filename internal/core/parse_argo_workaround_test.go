package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// Verifies we can recover a single-quoted Anthropic-style tool_use JSON embedded at end of content
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

	expectedText := "Now let me read the openai_convert.go file to understand the current ConvertBlocksToOpenAIContent implementation:"
	if text != expectedText {
		t.Errorf("Expected trimmed text %q, got %q", expectedText, text)
	}

	if len(tools) != 1 {
		t.Fatalf("Expected 1 recovered tool call, got %d", len(tools))
	}

	if tools[0].ID != "toolu_vrtx_01TCVSw8Ff8eJHs5nSaZsPBt" {
		t.Errorf("Unexpected tool ID: %s", tools[0].ID)
	}
	if tools[0].Name != "Read" {
		t.Errorf("Unexpected tool name: %s", tools[0].Name)
	}

	var args map[string]interface{}
	if err := json.Unmarshal(tools[0].Args, &args); err != nil {
		t.Fatalf("Failed to unmarshal recovered args: %v", err)
	}

	if fp, ok := args["file_path"].(string); !ok || fp == "" {
		t.Errorf("Expected file_path in args, got %v", args)
	}
}

// Verifies we can recover an OpenAI-style embedded function call JSON appended to content
func TestParseArgoResponse_Workaround_OpenAIFunctionEmbeddedInContent(t *testing.T) {
	// Content has explanatory text ending with a colon, then an OpenAI-style function object
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

	expectedText := "I'll run a command for you:"
	if text != expectedText {
		t.Errorf("Expected trimmed text %q, got %q", expectedText, text)
	}

	if len(tools) != 1 {
		t.Fatalf("Expected 1 recovered tool call, got %d", len(tools))
	}

	if tools[0].Name != "universal_command" {
		t.Errorf("Unexpected tool name: %s", tools[0].Name)
	}
	var args map[string]interface{}
	if err := json.Unmarshal(tools[0].Args, &args); err != nil {
		t.Fatalf("Failed to unmarshal recovered args: %v", err)
	}
	if cmd, ok := args["command"].([]interface{}); !ok || len(cmd) == 0 {
		t.Errorf("Expected command array in args, got %v", args)
	}
}

// Additional test: Python-style booleans and trailing comma in embedded block
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

	// Trimmed content should exclude the embedded JSON; preserve the original punctuation without inserting spaces
	expectedText := "Grep the code:,"
	if text != expectedText {
		t.Errorf("Expected trimmed text %q, got %q", expectedText, text)
	}

	if len(tools) != 1 {
		t.Fatalf("Expected 1 recovered tool call, got %d", len(tools))
	}
	if tools[0].Name != "Grep" {
		t.Errorf("Unexpected tool name: %s", tools[0].Name)
	}

	var args map[string]interface{}
	if err := json.Unmarshal(tools[0].Args, &args); err != nil {
		t.Fatalf("Failed to unmarshal recovered args: %v", err)
	}

	if v, ok := args["-n"].(bool); !ok || !v {
		t.Errorf("Expected -n true in args, got %v", args["-n"])
	}
	if glob, ok := args["glob"].(string); !ok || glob != "*.go" {
		t.Errorf("Expected glob '*.go', got %v", args["glob"])
	}
}

// New test: handle missing tool_calls field entirely while embedded tool_use exists in content
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

	expectedText := "Search code with tool:"
	if text != expectedText {
		t.Errorf("Expected trimmed text %q, got %q", expectedText, text)
	}

	if len(tools) != 1 {
		t.Fatalf("Expected 1 recovered tool call, got %d", len(tools))
	}
	if tools[0].Name != "Grep" {
		t.Errorf("Unexpected tool name: %s", tools[0].Name)
	}
}

// New test: multiple embedded tool_use objects inside content should be parsed in order
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

	// Text should be the two prefixes concatenated with a space
	if !strings.Contains(text, "Step 1: read file") || !strings.Contains(text, "Next, grep it") {
		t.Errorf("Expected combined prefixes in text, got %q", text)
	}

	if len(tools) != 2 {
		t.Fatalf("Expected 2 recovered tool calls, got %d", len(tools))
	}
	if tools[0].Name != "Read" || tools[1].Name != "Grep" {
		t.Errorf("Unexpected tool order: %s, %s", tools[0].Name, tools[1].Name)
	}
}
