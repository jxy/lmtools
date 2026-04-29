package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMemorySessionStorePreservesToolLoopMessages(t *testing.T) {
	store := NewMemorySessionStore("system prompt", "run a command")

	callArgs := json.RawMessage(`{"command":["sh","-c","echo out; echo err >&2; exit 1"]}`)
	if _, _, err := store.SaveAssistant(context.Background(), "I will run it", []ToolCall{
		{
			ID:   "call_1",
			Name: "universal_command",
			Args: callArgs,
		},
	}, "test-model"); err != nil {
		t.Fatalf("SaveAssistant failed: %v", err)
	}

	if _, _, err := store.SaveToolResults(context.Background(), []ToolResult{
		{
			ID:     "call_1",
			Output: "partial output",
			Error:  "exit status 1",
		},
	}, "tool round note"); err != nil {
		t.Fatalf("SaveToolResults failed: %v", err)
	}

	messages, err := store.Messages(store.GetPath())
	if err != nil {
		t.Fatalf("Messages failed: %v", err)
	}

	if len(messages) != 4 {
		t.Fatalf("got %d messages, want 4", len(messages))
	}
	if messages[0].Role != string(RoleSystem) || messages[1].Role != string(RoleUser) {
		t.Fatalf("seed messages not preserved: %#v", messages[:2])
	}

	toolUse, ok := messages[2].Blocks[1].(ToolUseBlock)
	if !ok {
		t.Fatalf("assistant second block = %T, want ToolUseBlock", messages[2].Blocks[1])
	}
	if toolUse.Name != "universal_command" || string(toolUse.Input) != string(callArgs) {
		t.Fatalf("unexpected tool use block: %#v", toolUse)
	}

	toolResult, ok := messages[3].Blocks[1].(ToolResultBlock)
	if !ok {
		t.Fatalf("user second block = %T, want ToolResultBlock", messages[3].Blocks[1])
	}
	if toolResult.Name != "universal_command" {
		t.Fatalf("tool result name = %q, want universal_command", toolResult.Name)
	}
	if !toolResult.IsError {
		t.Fatal("tool result should be marked as error")
	}
	if !strings.Contains(toolResult.Content, "partial output") || !strings.Contains(toolResult.Content, "exit status 1") {
		t.Fatalf("tool result should preserve output and error, got %q", toolResult.Content)
	}
}
