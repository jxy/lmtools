package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMalformedAnthropicThinkingBlockPaths(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "complete thinking block",
			body: `{"content":[{"type":"thinking","thinking":"hidden","signature":"sig"}]}`,
		},
		{
			name: "missing thinking",
			body: `{"content":[{"type":"thinking","signature":"sig"}]}`,
			want: []string{"$.content[0]"},
		},
		{
			name: "missing signature",
			body: `{"content":[{"type":"thinking","thinking":"hidden"}]}`,
			want: []string{"$.content[0]"},
		},
		{
			name: "thinking delta is not a thinking block",
			body: `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hidden"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var value interface{}
			if err := json.Unmarshal([]byte(tt.body), &value); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}

			got := malformedAnthropicThinkingBlockPaths(value, "$")
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("paths = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestWarnMalformedAnthropicThinkingBlocksLogsWarning(t *testing.T) {
	logs := captureStderrWithLevel(t, "warn", func() {
		warnMalformedAnthropicThinkingBlocks(context.Background(), json.RawMessage(`{"content":[{"type":"thinking","signature":"sig"}]}`), "Argo Anthropic")
	})

	if !strings.Contains(logs, "[WARN]") {
		t.Fatalf("logs missing WARN level: %s", logs)
	}
	if !strings.Contains(logs, "Argo Anthropic response contains type=thinking block missing thinking or signature at $.content[0]") {
		t.Fatalf("logs missing malformed thinking warning: %s", logs)
	}
}

func TestWarnMalformedAnthropicThinkingBlocksIgnoresCompleteBlock(t *testing.T) {
	logs := captureStderrWithLevel(t, "warn", func() {
		warnMalformedAnthropicThinkingBlocks(context.Background(), json.RawMessage(`{"content":[{"type":"thinking","thinking":"hidden","signature":"sig"}]}`), "Argo Anthropic")
	})

	if logs != "" {
		t.Fatalf("logs = %q, want no warning", logs)
	}
}

// TestMalformedAnthropicThinkingBlockPathsDeterministicOrder guards the sorted
// map-key traversal: without it, sibling keys are visited in Go's randomized
// map order and the emitted paths come out in a non-deterministic order.
func TestMalformedAnthropicThinkingBlockPathsDeterministicOrder(t *testing.T) {
	// Three sibling object keys, each a thinking block missing its signature.
	body := `{"c":{"type":"thinking","thinking":"x"},"a":{"type":"thinking","thinking":"x"},"b":{"type":"thinking","thinking":"x"}}`
	var value interface{}
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	want := []string{"$.a", "$.b", "$.c"}
	// Repeat to make a regression (random order) statistically obvious.
	for i := 0; i < 50; i++ {
		got := malformedAnthropicThinkingBlockPaths(value, "$")
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("paths = %#v, want %#v (iteration %d)", got, want, i)
		}
	}
}

// TestThinkingStreamAuditor exercises the per-block streaming auditor across
// full content_block_start/delta/stop sequences and asserts it warns once, at
// block close, only for assembled thinking blocks missing thinking or signature.
func TestThinkingStreamAuditor(t *testing.T) {
	block := func(typ, thinking, sig string) AnthropicContentBlock {
		return AnthropicContentBlock{Type: typ, Thinking: thinking, Signature: sig}
	}
	delta := func(typ, thinking, sig string) DeltaContent {
		return DeltaContent{Type: typ, Thinking: thinking, Signature: sig}
	}

	tests := []struct {
		name      string
		drive     func(a *thinkingStreamAuditor, ctx context.Context)
		wantWarns []int // indices expected to warn, in order
	}{
		{
			name: "complete thinking block",
			drive: func(a *thinkingStreamAuditor, ctx context.Context) {
				a.observeBlockStart(0, block("thinking", "", ""))
				a.observeDelta(0, delta("thinking_delta", "hidden", ""))
				a.observeDelta(0, delta("signature_delta", "", "sig"))
				a.observeBlockStop(ctx, 0, "Anthropic stream")
			},
		},
		{
			name: "missing signature warns",
			drive: func(a *thinkingStreamAuditor, ctx context.Context) {
				a.observeBlockStart(0, block("thinking", "", ""))
				a.observeDelta(0, delta("thinking_delta", "hidden", ""))
				a.observeBlockStop(ctx, 0, "Anthropic stream")
			},
			wantWarns: []int{0},
		},
		{
			name: "missing thinking warns",
			drive: func(a *thinkingStreamAuditor, ctx context.Context) {
				a.observeBlockStart(0, block("thinking", "", ""))
				a.observeDelta(0, delta("signature_delta", "", "sig"))
				a.observeBlockStop(ctx, 0, "Anthropic stream")
			},
			wantWarns: []int{0},
		},
		{
			name: "empty thinking delta does not satisfy thinking",
			drive: func(a *thinkingStreamAuditor, ctx context.Context) {
				a.observeBlockStart(0, block("thinking", "", ""))
				a.observeDelta(0, delta("thinking_delta", "", ""))
				a.observeDelta(0, delta("signature_delta", "", "sig"))
				a.observeBlockStop(ctx, 0, "Anthropic stream")
			},
			wantWarns: []int{0},
		},
		{
			name: "inline complete on start does not warn",
			drive: func(a *thinkingStreamAuditor, ctx context.Context) {
				a.observeBlockStart(0, block("thinking", "hidden", "sig"))
				a.observeBlockStop(ctx, 0, "Anthropic stream")
			},
		},
		{
			name: "text block ignored",
			drive: func(a *thinkingStreamAuditor, ctx context.Context) {
				a.observeBlockStart(0, block("text", "", ""))
				a.observeDelta(0, delta("text_delta", "", ""))
				a.observeBlockStop(ctx, 0, "Anthropic stream")
			},
		},
		{
			name: "redacted_thinking ignored",
			drive: func(a *thinkingStreamAuditor, ctx context.Context) {
				a.observeBlockStart(0, block("redacted_thinking", "", ""))
				a.observeBlockStop(ctx, 0, "Anthropic stream")
			},
		},
		{
			name: "two blocks only malformed one warns",
			drive: func(a *thinkingStreamAuditor, ctx context.Context) {
				// Block 0 is complete.
				a.observeBlockStart(0, block("thinking", "", ""))
				a.observeDelta(0, delta("thinking_delta", "hidden", ""))
				a.observeDelta(0, delta("signature_delta", "", "sig"))
				a.observeBlockStop(ctx, 0, "Anthropic stream")
				// Block 1 never receives a signature.
				a.observeBlockStart(1, block("thinking", "", ""))
				a.observeDelta(1, delta("thinking_delta", "hidden", ""))
				a.observeBlockStop(ctx, 1, "Anthropic stream")
			},
			wantWarns: []int{1},
		},
		{
			name: "stop without start is a no-op",
			drive: func(a *thinkingStreamAuditor, ctx context.Context) {
				a.observeBlockStop(ctx, 0, "Anthropic stream")
			},
		},
	}

	const warnPrefix = "missing thinking or signature at index"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureStderrWithLevel(t, "warn", func() {
				tt.drive(newThinkingStreamAuditor(), context.Background())
			})

			if got := strings.Count(logs, warnPrefix); got != len(tt.wantWarns) {
				t.Fatalf("warning count = %d, want %d\nlogs:\n%s", got, len(tt.wantWarns), logs)
			}
			for _, idx := range tt.wantWarns {
				want := fmt.Sprintf("%s %d", warnPrefix, idx)
				if !strings.Contains(logs, want) {
					t.Fatalf("logs missing warning for index %d\nlogs:\n%s", idx, logs)
				}
			}
		})
	}
}
