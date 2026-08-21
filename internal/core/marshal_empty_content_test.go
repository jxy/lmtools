package core

import (
	"testing"
)

// A command that exits without writing to stdout or stderr yields an empty tool
// result. OpenAI rejects any non-assistant message that arrives without
// content, so the renderer must spell the emptiness out rather than drop the
// key. Assistant messages are the documented exception, but only while they
// carry tool_calls.
func TestMarshalOpenAIMessagesForRequest_EmptyContent(t *testing.T) {
	empty := ""

	tests := []struct {
		name        string
		msg         OpenAIMessage
		wantContent interface{}
		wantOmitted bool
	}{
		{
			name: "tool result with no output",
			msg: OpenAIMessage{
				Role:       "tool",
				ToolCallID: "call_1",
				Content:    OpenAIContentUnion{Text: &empty},
			},
			wantContent: "",
		},
		{
			name: "tool result with no content union at all",
			msg: OpenAIMessage{
				Role:       "tool",
				ToolCallID: "call_1",
			},
			wantContent: "",
		},
		{
			name:        "empty user message",
			msg:         OpenAIMessage{Role: "user", Content: OpenAIContentUnion{Text: &empty}},
			wantContent: "",
		},
		{
			name:        "empty system message",
			msg:         OpenAIMessage{Role: "system", Content: OpenAIContentUnion{Text: &empty}},
			wantContent: "",
		},
		{
			name:        "assistant message with neither text nor tool calls",
			msg:         OpenAIMessage{Role: "assistant"},
			wantContent: "",
		},
		{
			name: "assistant message carrying only tool calls",
			msg: OpenAIMessage{
				Role: "assistant",
				ToolCalls: []OpenAIToolCall{{
					ID:       "call_1",
					Type:     "function",
					Function: OpenAIFunctionCall{Name: "universal_command", Arguments: `{}`},
				}},
			},
			wantOmitted: true,
		},
		{
			name:        "non-empty text is unchanged",
			msg:         OpenAIMessage{Role: "user", Content: OpenAIContentUnion{Text: stringPtr("hello")}},
			wantContent: "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MarshalOpenAIMessagesForRequest([]OpenAIMessage{tt.msg})
			if len(got) != 1 {
				t.Fatalf("messages = %d, want 1", len(got))
			}
			msgMap, ok := got[0].(map[string]interface{})
			if !ok {
				t.Fatalf("message = %#v, want map", got[0])
			}

			content, present := msgMap["content"]
			if tt.wantOmitted {
				if present {
					t.Fatalf("content = %#v, want omitted for a message with tool_calls", content)
				}
				return
			}
			if !present {
				t.Fatalf("content missing from %#v; OpenAI rejects a message without it", msgMap)
			}
			if content != tt.wantContent {
				t.Fatalf("content = %#v, want %#v", content, tt.wantContent)
			}
		})
	}
}

// Anthropic documents content as optional on tool_result and publishes the
// empty-result shape as tool_use_id alone, so the OpenAI fix must not be
// mirrored here.
func TestMarshalAnthropicMessagesForRequest_EmptyToolResultOmitsContent(t *testing.T) {
	msgs := ToAnthropicTyped([]TypedMessage{{
		Role:   string(RoleUser),
		Blocks: []Block{ToolResultBlock{ToolUseID: "toolu_1", Content: ""}},
	}})

	got := MarshalAnthropicMessagesForRequest(msgs)
	if len(got) != 1 {
		t.Fatalf("messages = %d, want 1", len(got))
	}
	msgMap := got[0].(map[string]interface{})
	blocks, ok := msgMap["content"].([]interface{})
	if !ok || len(blocks) != 1 {
		t.Fatalf("content = %#v, want one block", msgMap["content"])
	}
	block := blocks[0].(map[string]interface{})
	if block["type"] != "tool_result" || block["tool_use_id"] != "toolu_1" {
		t.Fatalf("block = %#v, want an empty tool_result for toolu_1", block)
	}
	if content, present := block["content"]; present {
		t.Fatalf("content = %#v, want omitted per the documented empty tool_result shape", content)
	}
}
