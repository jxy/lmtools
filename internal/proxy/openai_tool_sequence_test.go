package proxy

import (
	"encoding/json"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"strings"
	"testing"
)

func TestTypedToOpenAIRequest_ReordersResponsesTextAfterToolOutput(t *testing.T) {
	req, err := TypedToOpenAIRequest(TypedRequest{Messages: []core.TypedMessage{
		assistantToolUseMessage("call_1"),
		core.NewTextMessage(string(core.RoleUser), "next question"),
		userToolResultMessage("call_1", "tool result"),
	}}, "gpt-5.4-nano")
	if err != nil {
		t.Fatalf("TypedToOpenAIRequest() error = %v", err)
	}

	assertOpenAIRoles(t, req.Messages, "assistant", "tool", "user")
	if req.Messages[1].ToolCallID != "call_1" {
		t.Fatalf("tool_call_id = %q, want call_1", req.Messages[1].ToolCallID)
	}
	if req.Messages[2].Content != "next question" {
		t.Fatalf("user content = %#v, want next question", req.Messages[2].Content)
	}
}

func TestTypedToOpenAIRequest_MergesParallelResponsesFunctionCalls(t *testing.T) {
	req, err := TypedToOpenAIRequest(TypedRequest{Messages: []core.TypedMessage{
		assistantToolUseMessage("call_1"),
		assistantToolUseMessage("call_2"),
		userToolResultMessage("call_1", "first"),
		userToolResultMessage("call_2", "second"),
	}}, "gpt-5.4-nano")
	if err != nil {
		t.Fatalf("TypedToOpenAIRequest() error = %v", err)
	}

	assertOpenAIRoles(t, req.Messages, "assistant", "tool", "tool")
	if len(req.Messages[0].ToolCalls) != 2 {
		t.Fatalf("assistant tool_calls = %+v, want 2", req.Messages[0].ToolCalls)
	}
	if req.Messages[1].ToolCallID != "call_1" || req.Messages[2].ToolCallID != "call_2" {
		t.Fatalf("tool messages = %+v", req.Messages)
	}
}

func TestTypedToOpenAIRequest_MergesReasoningWithinPendingToolCalls(t *testing.T) {
	req, err := TypedToOpenAIRequest(TypedRequest{Messages: []core.TypedMessage{
		assistantToolUseMessage("call_1"),
		{
			Role: string(core.RoleAssistant),
			Blocks: []core.Block{core.ReasoningBlock{
				Provider: "openai",
				Type:     "reasoning",
				ID:       "rs_1",
			}},
		},
		assistantToolUseMessage("call_2"),
		userToolResultMessage("call_1", "first"),
		userToolResultMessage("call_2", "second"),
	}}, "gpt-5.4-nano")
	if err != nil {
		t.Fatalf("TypedToOpenAIRequest() error = %v", err)
	}

	assertOpenAIRoles(t, req.Messages, "assistant", "tool", "tool")
	if len(req.Messages[0].ToolCalls) != 2 {
		t.Fatalf("assistant tool_calls = %+v, want 2", req.Messages[0].ToolCalls)
	}
}

func TestTypedToOpenAIRequest_DropsReasoningOnlyMessages(t *testing.T) {
	req, err := TypedToOpenAIRequest(TypedRequest{Messages: []core.TypedMessage{
		core.NewTextMessage(string(core.RoleUser), "first"),
		{
			Role: string(core.RoleAssistant),
			Blocks: []core.Block{core.ReasoningBlock{
				Provider:         "openai",
				Type:             "reasoning",
				ID:               "rs_1",
				EncryptedContent: "opaque",
			}},
		},
		core.NewTextMessage(string(core.RoleUser), "second"),
	}}, "gpt-5.4-nano")
	if err != nil {
		t.Fatalf("TypedToOpenAIRequest() error = %v", err)
	}

	assertOpenAIRoles(t, req.Messages, "user", "user")
	for i, msg := range req.Messages {
		if msg.Role == core.RoleAssistant && msg.Content == nil && len(msg.ToolCalls) == 0 {
			t.Fatalf("messages[%d] rendered as empty assistant message: %+v", i, msg)
		}
	}
}

func TestTypedToOpenAIRequest_RendersToolResultBeforeSameMessageText(t *testing.T) {
	req, err := TypedToOpenAIRequest(TypedRequest{Messages: []core.TypedMessage{
		assistantToolUseMessage("call_1"),
		{
			Role: string(core.RoleUser),
			Blocks: []core.Block{
				core.TextBlock{Text: "continue after tool"},
				core.ToolResultBlock{ToolUseID: "call_1", Content: "tool result"},
			},
		},
	}}, "gpt-5.4-nano")
	if err != nil {
		t.Fatalf("TypedToOpenAIRequest() error = %v", err)
	}

	assertOpenAIRoles(t, req.Messages, "assistant", "tool", "user")
	if req.Messages[1].ToolCallID != "call_1" {
		t.Fatalf("tool_call_id = %q, want call_1", req.Messages[1].ToolCallID)
	}
	if req.Messages[2].Content != "continue after tool" {
		t.Fatalf("user content = %#v, want continue after tool", req.Messages[2].Content)
	}
}

func TestTypedToOpenAIRequest_RejectsMissingToolOutput(t *testing.T) {
	_, err := TypedToOpenAIRequest(TypedRequest{Messages: []core.TypedMessage{
		assistantToolUseMessage("call_missing"),
		core.NewTextMessage(string(core.RoleUser), "next question"),
	}}, "gpt-5.4-nano")
	if err == nil {
		t.Fatal("expected missing tool output error")
	}
	if !strings.Contains(err.Error(), "call_missing") {
		t.Fatalf("error = %q, want missing call id", err.Error())
	}
}

func TestValidateConvertedOpenAIChatToolSequenceSkipsLegacyArgo(t *testing.T) {
	typed := TypedRequest{Messages: []core.TypedMessage{
		assistantToolUseMessage("call_missing"),
		core.NewTextMessage(string(core.RoleUser), "next question"),
	}}

	nativeServer := &Server{config: &Config{}}
	err := nativeServer.validateConvertedOpenAIChatToolSequence(typed, constants.ProviderArgo, "gpt-5")
	if err == nil {
		t.Fatal("expected native Argo OpenAI-chat validation error")
	}
	if !strings.Contains(err.Error(), "call_missing") {
		t.Fatalf("error = %q, want missing call id", err.Error())
	}

	legacyServer := &Server{config: &Config{ArgoLegacy: true}}
	if err := legacyServer.validateConvertedOpenAIChatToolSequence(typed, constants.ProviderArgo, "gpt-5"); err != nil {
		t.Fatalf("legacy Argo validation error = %v, want nil", err)
	}
}

func TestValidateParsedOpenAIRequestRejectsStrayToolMessage(t *testing.T) {
	err := validateParsedOpenAIRequest(&OpenAIRequest{
		Model: "gpt-5.4-nano",
		Messages: []OpenAIMessage{{
			Role:       "tool",
			ToolCallID: "call_1",
			Content:    "tool result",
		}},
	})
	if err == nil {
		t.Fatal("expected stray tool message error")
	}
	if !strings.Contains(err.Error(), "no preceding assistant tool_calls") {
		t.Fatalf("error = %q, want no preceding assistant tool_calls", err.Error())
	}
}

func assistantToolUseMessage(id string) core.TypedMessage {
	return core.TypedMessage{
		Role: string(core.RoleAssistant),
		Blocks: []core.Block{core.ToolUseBlock{
			ID:    id,
			Name:  "lookup",
			Input: json.RawMessage(`{"query":"test"}`),
		}},
	}
}

func userToolResultMessage(id, content string) core.TypedMessage {
	return core.TypedMessage{
		Role: string(core.RoleUser),
		Blocks: []core.Block{core.ToolResultBlock{
			ToolUseID: id,
			Content:   content,
		}},
	}
}

func assertOpenAIRoles(t *testing.T, messages []OpenAIMessage, roles ...string) {
	t.Helper()
	if len(messages) != len(roles) {
		t.Fatalf("messages = %+v, want %d messages", messages, len(roles))
	}
	for i, role := range roles {
		if string(messages[i].Role) != role {
			t.Fatalf("messages[%d].Role = %q, want %q; messages=%+v", i, messages[i].Role, role, messages)
		}
	}
}
