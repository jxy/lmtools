package proxy

import (
	"encoding/json"
	"testing"
)

func TestAnthropicRequestToTyped_ExtractsSystemTextBlocks(t *testing.T) {
	req := &AnthropicRequest{
		System: json.RawMessage(`[{"type":"text","text":"Part 1"},{"type":"text","text":"Part 2"}]`),
	}

	typed := AnthropicRequestToTyped(req)
	if typed.System != "Part 1\nPart 2" {
		t.Fatalf("AnthropicRequestToTyped() system = %q, want %q", typed.System, "Part 1\nPart 2")
	}
}

func TestOpenAIRequestToTyped_ParsesFunctionToolChoiceStringMap(t *testing.T) {
	req := &OpenAIRequest{
		ToolChoice: map[string]interface{}{
			"type": "function",
			"function": map[string]string{
				"name": "test_tool",
			},
		},
	}

	typed := OpenAIRequestToTyped(req)
	if typed.ToolChoice == nil {
		t.Fatal("OpenAIRequestToTyped() tool choice is nil")
	}
	if typed.ToolChoice.Type != "tool" {
		t.Fatalf("OpenAIRequestToTyped() tool choice type = %q, want %q", typed.ToolChoice.Type, "tool")
	}
	if typed.ToolChoice.Name != "test_tool" {
		t.Fatalf("OpenAIRequestToTyped() tool choice name = %q, want %q", typed.ToolChoice.Name, "test_tool")
	}
}
