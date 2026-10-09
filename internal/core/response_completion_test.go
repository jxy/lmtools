package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIncompleteResponseCannotLeavePendingTools(t *testing.T) {
	partial := Response{
		Text: "Partial answer", IncompleteReason: "max_output_tokens",
		ToolCalls: []ToolCall{{ID: "call_1", Name: "universal_command", Args: json.RawMessage(`{"command":["echo"]}`)}},
		Blocks: []Block{
			ReasoningBlock{Type: "reasoning", Text: "Checking"},
			ToolUseBlock{ID: "call_1"},
			&ToolUseBlock{ID: "call_2"},
			TextBlock{Text: "Partial answer"},
		},
	}
	response := prepareResponseCompletion(RequestOptions{}, partial)
	if len(response.ToolCalls) != 0 || len(response.Blocks) != 2 || response.Text != partial.Text {
		t.Fatalf("unsafe or missing partial response: %+v", response)
	}
	if err := ValidateResponse(RequestOptions{}, response); err == nil || !strings.Contains(err.Error(), "partial answer") {
		t.Fatalf("incomplete response error = %v", err)
	}
	if len(partial.Blocks) != 4 {
		t.Fatal("completion handling mutated the source blocks")
	}
}

func TestCompletionDetectsBudgetWithoutMisclassifyingAnAnswer(t *testing.T) {
	count := 8
	for _, tt := range []struct {
		text string
		want string
	}{
		{"", "max_output_tokens"},
		{"done", ""},
	} {
		response := prepareResponseCompletion(RequestOptions{MaxTokens: count}, Response{
			Text: tt.text, Usage: &Usage{OutputTokens: &count},
			Blocks: []Block{ReasoningBlock{Type: "reasoning", Text: "Checking"}},
		})
		if response.IncompleteReason != tt.want {
			t.Errorf("text=%q: reason=%q, want %q", tt.text, response.IncompleteReason, tt.want)
		}
	}
}

func TestValidateResponseRejectsInvalidStructuredAnswers(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer","const":437}},"required":["answer"],"additionalProperties":false}`)
	for _, tt := range []struct {
		name     string
		cfg      RequestOptions
		response Response
		want     string
	}{
		{"valid object", RequestOptions{JSONMode: true}, Response{Text: `{"answer":437}`}, ""},
		{"fenced object", RequestOptions{JSONMode: true}, Response{Text: "```json\n{}\n```"}, "requested JSON"},
		{"array is not object", RequestOptions{JSONMode: true}, Response{Text: `[]`}, "not an object"},
		{"null is not object", RequestOptions{JSONMode: true}, Response{Text: `null`}, "not an object"},
		{"multiple JSON values", RequestOptions{JSONMode: true}, Response{Text: `{} {}`}, "requested JSON"},
		{"malformed JSON", RequestOptions{JSONMode: true}, Response{Text: `{"answer":`}, "requested JSON"},
		{"schema match", RequestOptions{JSONSchema: schema}, Response{Text: `{"answer":437}`}, ""},
		{"schema constraints left to provider", RequestOptions{JSONSchema: schema}, Response{Text: `{"answer":438}`}, ""},
		{"schema shape left to provider", RequestOptions{JSONSchema: schema}, Response{Text: `[]`}, ""},
		{"schema invalid JSON", RequestOptions{JSONSchema: schema}, Response{Text: "```json\n{}\n```"}, "requested JSON"},
		{"huge positive exponent", RequestOptions{JSONSchema: schema}, Response{Text: `{"answer":1e2147483648}`}, ""},
		{"huge negative exponent", RequestOptions{JSONSchema: schema}, Response{Text: `{"answer":1e-2147483648}`}, ""},
		{"tool intermediate", RequestOptions{JSONMode: true}, Response{ToolCalls: []ToolCall{{ID: "call_1"}}}, ""},
		{"thinking only", RequestOptions{}, Response{Blocks: []Block{ReasoningBlock{Type: "reasoning", Text: "Checking"}}}, "without an answer"},
		{"interrupted stream", RequestOptions{}, Response{Text: "partial", IncompleteReason: "stream_interrupted"}, "stream ended"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateResponse(tt.cfg, tt.response)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v, want %q", err, tt.want)
			}
		})
	}
}
