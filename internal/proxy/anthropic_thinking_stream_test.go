package proxy

import (
	"context"
	"strings"
	"testing"
)

func TestParseAnthropicStreamPreservesThinkingDeltas(t *testing.T) {
	raw := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"I should compute this carefully."}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_fixture"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":6}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	recorder := newFlushableRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "claude-opus-4-7", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}
	server := &Server{}
	if err := server.parseAnthropicStream(strings.NewReader(raw), handler); err != nil {
		t.Fatalf("parseAnthropicStream() error = %v", err)
	}

	out := recorder.Body.String()
	for _, want := range []string{
		`"type":"thinking_delta","thinking":"I should compute this carefully."`,
		`"type":"signature_delta","signature":"sig_fixture"`,
		`"type":"thinking"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream output missing %q\noutput:\n%s", want, out)
		}
	}
}
