package proxy

import (
	"encoding/json"
	"lmtools/internal/core"
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

func TestOpenAIRequestToTyped_PreservesInstructionMessagesInOrder(t *testing.T) {
	req := &OpenAIRequest{
		Messages: []OpenAIMessage{
			{Role: core.RoleDeveloper, Content: "developer"},
			{Role: core.RoleSystem, Content: "system"},
			{Role: core.RoleUser, Content: "user"},
		},
	}

	typed := OpenAIRequestToTyped(req)
	if typed.System != "" || typed.Developer != "" {
		t.Fatalf("OpenAIRequestToTyped() hoisted instructions: system=%q developer=%q", typed.System, typed.Developer)
	}
	if len(typed.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3", len(typed.Messages))
	}
	for i, role := range []string{string(core.RoleDeveloper), string(core.RoleSystem), string(core.RoleUser)} {
		if typed.Messages[i].Role != role {
			t.Fatalf("Messages[%d].Role = %q, want %q", i, typed.Messages[i].Role, role)
		}
	}
}
