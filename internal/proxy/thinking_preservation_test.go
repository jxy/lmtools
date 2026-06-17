package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAnthropicResponsePreservesThinkingBlockMissingThinkingKey verifies that a
// non-streaming Argo/Anthropic response whose assistant content includes a
// type=thinking block without a "thinking" key (a normal provider behavior) is
// parsed and re-rendered to the client with the block kept exactly as received:
// the block is not dropped and no empty "thinking" key is synthesized.
func TestAnthropicResponsePreservesThinkingBlockMissingThinkingKey(t *testing.T) {
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-7","content":[{"type":"thinking","signature":"sig"},{"type":"text","text":"hello"}]}`

	var resp AnthropicResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("parsed content blocks = %d, want 2 (thinking block dropped on parse)", len(resp.Content))
	}

	out, err := json.Marshal(&resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}

	var reparsed struct {
		Content []map[string]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(out, &reparsed); err != nil {
		t.Fatalf("reparse response: %v", err)
	}
	if len(reparsed.Content) != 2 {
		t.Fatalf("re-rendered content blocks = %d, want 2 (thinking block dropped):\n%s", len(reparsed.Content), out)
	}

	thinking := reparsed.Content[0]
	if got := string(thinking["type"]); got != `"thinking"` {
		t.Fatalf("first block type = %s, want \"thinking\":\n%s", got, out)
	}
	if got := string(thinking["signature"]); got != `"sig"` {
		t.Fatalf("signature = %s, want \"sig\" (signature not preserved):\n%s", got, out)
	}
	if _, hasThinking := thinking["thinking"]; hasThinking {
		t.Fatalf("an empty \"thinking\" key was synthesized; block must be kept as received:\n%s", out)
	}
}

// TestAnthropicRequestPreservesEmptyThinkingString verifies that when apiproxy
// forwards a client request to a v1/messages backend, a thinking block carrying
// an explicit empty "thinking" string (which the Anthropic API requires
// alongside type and signature) is preserved byte-for-byte. The forward path
// marshals AnthropicRequest, whose per-message Content is json.RawMessage, so
// the client's content bytes pass through unchanged.
func TestAnthropicRequestPreservesEmptyThinkingString(t *testing.T) {
	body := `{"model":"claude-opus-4-7","max_tokens":100,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"text","text":"hi"}]}]}`

	var req AnthropicRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	out, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	if !strings.Contains(string(out), `"thinking":""`) {
		t.Fatalf("forwarded request dropped the empty thinking string:\n%s", out)
	}
	if !strings.Contains(string(out), `"signature":"sig"`) {
		t.Fatalf("forwarded request dropped the signature:\n%s", out)
	}
}
