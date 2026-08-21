package session

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"os"
	"reflect"
	"testing"
)

// writeLegacyNoteFirstBlocks replaces a committed .blocks.json with the layout
// binaries before core.ToolResultsMessageBlocks wrote for a truncated tool
// round: the note first, then the results.
func writeLegacyNoteFirstBlocks(t *testing.T, sessionPath, msgID, note, toolUseID, output string) {
	t.Helper()
	payload := fmt.Sprintf(`{
  "version": 1,
  "role": "user",
  "blocks": [
    {"type": "text", "text": %s},
    {"type": "tool_result", "tool_use_id": %s, "text": %s}
  ]
}`, mustJSONString(t, note), mustJSONString(t, toolUseID), mustJSONString(t, output))

	path := buildMessageFilePaths(sessionPath, msgID).BlocksPath
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write legacy blocks for %s: %v", msgID, err)
	}
}

func mustJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %q: %v", value, err)
	}
	return string(encoded)
}

// TestLegacyNoteFirstBlocksReplayAsToolResultsFirst pins that a session written
// by an older binary still resumes. Replaying [text, tool_result] verbatim puts
// a user message between the assistant tool_calls turn and the tool message
// answering it, which the proxy's own validateOpenAIChatToolSequence rejects.
func TestLegacyNoteFirstBlocksReplayAsToolResultsFirst(t *testing.T) {
	manager, _ := NewTestManager(t)
	ctx := context.Background()
	sess, err := manager.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	appendPlanMessage(t, ctx, sess, core.RoleUser, "run the tool")
	call := core.ToolCall{ID: "call-1", Name: "universal_command", Args: json.RawMessage(`{"command":["echo","hi"]}`)}
	if _, err := SaveAssistantResponseWithTools(ctx, sess, "on it", []core.ToolCall{call}, "test-model"); err != nil {
		t.Fatalf("save assistant tool call: %v", err)
	}
	const note = "Note: Output for tool 'universal_command' was truncated to 1.0 MB\n"
	results, err := SaveToolResults(ctx, sess, []core.ToolResult{{ID: call.ID, Output: "hi"}}, note)
	if err != nil {
		t.Fatalf("save tool results: %v", err)
	}
	writeLegacyNoteFirstBlocks(t, sess.Path, results.MessageID, note, call.ID, "hi")

	messages, err := BuildMessagesWithToolInteractionsWithManager(ctx, manager, sess.Path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractionsWithManager() error = %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("message count = %d, want 3", len(messages))
	}

	resultBlocks := messages[2].Blocks
	if len(resultBlocks) != 2 {
		t.Fatalf("tool-results blocks = %#v, want one result and one note", resultBlocks)
	}
	toolResult, ok := resultBlocks[0].(core.ToolResultBlock)
	if !ok || toolResult.ToolUseID != call.ID || toolResult.Content != "hi" {
		t.Fatalf("first block = %#v, want the tool_result for %s", resultBlocks[0], call.ID)
	}
	if toolResult.Name != call.Name {
		t.Fatalf("tool_result name = %q, want %q resolved from the assistant turn", toolResult.Name, call.Name)
	}
	text, ok := resultBlocks[1].(core.TextBlock)
	if !ok || text.Text != note {
		t.Fatalf("second block = %#v, want the trailing note %q", resultBlocks[1], note)
	}

	// The wire order is the point: the tool message must immediately follow the
	// assistant turn that requested it.
	wire := core.ToOpenAITyped(messages)
	roles := make([]string, 0, len(wire))
	for _, msg := range wire {
		roles = append(roles, msg.Role)
	}
	if want := []string{"user", "assistant", "tool", "user"}; !reflect.DeepEqual(roles, want) {
		t.Fatalf("OpenAI wire roles = %v, want %v", roles, want)
	}
	if wire[2].ToolCallID != call.ID {
		t.Fatalf("tool message tool_call_id = %q, want %q", wire[2].ToolCallID, call.ID)
	}
	if len(wire[1].ToolCalls) != 1 || wire[1].ToolCalls[0].ID != call.ID {
		t.Fatalf("assistant tool_calls = %#v, want one call %s", wire[1].ToolCalls, call.ID)
	}
}

func TestNormalizeToolResultsBlockOrder(t *testing.T) {
	note := core.TextBlock{Text: "note"}
	first := core.ToolResultBlock{ToolUseID: "call-1", Content: "one"}
	second := core.ToolResultBlock{ToolUseID: "call-2", Content: "two"}
	reasoning := core.ReasoningBlock{Provider: "google", Type: "thought_signature", Signature: "sig"}
	use := core.ToolUseBlock{ID: "call-1", Name: "universal_command"}

	tests := []struct {
		name   string
		blocks []core.Block
		want   []core.Block
	}{
		{
			name:   "legacy note first",
			blocks: []core.Block{note, first, second},
			want:   []core.Block{first, second, note},
		},
		{
			name:   "canonical order untouched",
			blocks: []core.Block{first, second, note},
			want:   []core.Block{first, second, note},
		},
		{
			name:   "results without a note untouched",
			blocks: []core.Block{first, second},
			want:   []core.Block{first, second},
		},
		{
			name:   "leading reasoning keeps its place",
			blocks: []core.Block{reasoning, note, first},
			want:   []core.Block{reasoning, first, note},
		},
		{
			name:   "plain user text untouched",
			blocks: []core.Block{note},
			want:   []core.Block{note},
		},
		{
			name:   "assistant tool_use turn untouched",
			blocks: []core.Block{note, use},
			want:   []core.Block{note, use},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeToolResultsBlockOrder(tt.blocks); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("normalizeToolResultsBlockOrder() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
