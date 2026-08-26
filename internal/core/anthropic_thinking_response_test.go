package core

import (
	"encoding/json"
	"testing"
)

func TestParseAnthropicAdaptiveThinkingResponse(t *testing.T) {
	body := []byte(`{
		"id":"msg_1",
		"type":"message",
		"role":"assistant",
		"model":"claude-opus-4-7",
		"content":[
			{"type":"thinking","thinking":"Use the Euclidean algorithm.","signature":"sig_json"},
			{"type":"text","text":"The answer is 21."}
		]
	}`)

	response, err := parseAnthropicResponse(body, false)
	if err != nil {
		t.Fatalf("parseAnthropicResponse() error = %v", err)
	}
	if response.Text != "The answer is 21." {
		t.Fatalf("Text = %q, want final answer only", response.Text)
	}
	if len(response.Blocks) != 2 {
		t.Fatalf("Blocks = %#v, want thinking plus text", response.Blocks)
	}
	reasoning, ok := response.Blocks[0].(ReasoningBlock)
	if !ok {
		t.Fatalf("Blocks[0] = %T, want ReasoningBlock", response.Blocks[0])
	}
	if reasoning.Provider != "anthropic" || reasoning.Type != "thinking" || reasoning.Text != "Use the Euclidean algorithm." || reasoning.Signature != "sig_json" {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(reasoning.Raw, &raw); err != nil {
		t.Fatalf("unmarshal reasoning.Raw: %v", err)
	}
	if raw["thinking"] != "Use the Euclidean algorithm." || raw["signature"] != "sig_json" {
		t.Fatalf("reasoning.Raw = %#v, want original thinking block", raw)
	}
}
