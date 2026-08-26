package core

import (
	"context"
	"io"
	"lmtools/internal/apifixtures"
	"net/http"
	"strings"
	"testing"
)

func intPtr(v int) *int { return &v }

func assertUsageEquals(t *testing.T, got, want *Usage) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
	if got == nil {
		return
	}
	fields := []struct {
		name string
		got  *int
		want *int
	}{
		{"InputTokens", got.InputTokens, want.InputTokens},
		{"OutputTokens", got.OutputTokens, want.OutputTokens},
		{"TotalTokens", got.TotalTokens, want.TotalTokens},
		{"CacheReadInputTokens", got.CacheReadInputTokens, want.CacheReadInputTokens},
		{"CacheCreationInputTokens", got.CacheCreationInputTokens, want.CacheCreationInputTokens},
		{"ReasoningTokens", got.ReasoningTokens, want.ReasoningTokens},
	}
	for _, field := range fields {
		switch {
		case (field.got == nil) != (field.want == nil):
			t.Errorf("%s = %v, want %v", field.name, field.got, field.want)
		case field.got != nil && *field.got != *field.want:
			t.Errorf("%s = %d, want %d", field.name, *field.got, *field.want)
		}
	}
}

func TestUsageFromPayloadWireShapes(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    *Usage
	}{
		{
			name:    "anthropic messages",
			payload: `{"content":[],"usage":{"input_tokens":10,"output_tokens":19,"cache_creation_input_tokens":5,"cache_read_input_tokens":2048,"output_tokens_details":{"thinking_tokens":12}}}`,
			want: &Usage{
				InputTokens:              intPtr(10),
				OutputTokens:             intPtr(19),
				CacheReadInputTokens:     intPtr(2048),
				CacheCreationInputTokens: intPtr(5),
				ReasoningTokens:          intPtr(12),
			},
		},
		{
			name:    "openai chat completions",
			payload: `{"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":14,"total_tokens":23,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":7}}}`,
			want: &Usage{
				InputTokens:          intPtr(9),
				OutputTokens:         intPtr(14),
				TotalTokens:          intPtr(23),
				CacheReadInputTokens: intPtr(4),
				ReasoningTokens:      intPtr(7),
			},
		},
		{
			name:    "openai responses",
			payload: `{"output":[],"usage":{"input_tokens":23,"input_tokens_details":{"cached_tokens":6},"output_tokens":9,"output_tokens_details":{"reasoning_tokens":3},"total_tokens":32}}`,
			want: &Usage{
				InputTokens:          intPtr(23),
				OutputTokens:         intPtr(9),
				TotalTokens:          intPtr(32),
				CacheReadInputTokens: intPtr(6),
				ReasoningTokens:      intPtr(3),
			},
		},
		{
			name:    "google generate content",
			payload: `{"candidates":[],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":9,"totalTokenCount":13,"cachedContentTokenCount":1,"thoughtsTokenCount":2}}`,
			want: &Usage{
				InputTokens:          intPtr(4),
				OutputTokens:         intPtr(9),
				TotalTokens:          intPtr(13),
				CacheReadInputTokens: intPtr(1),
				ReasoningTokens:      intPtr(2),
			},
		},
		{
			name:    "anthropic message_start event",
			payload: `{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":100,"output_tokens":1}}}`,
			want: &Usage{
				InputTokens:  intPtr(100),
				OutputTokens: intPtr(1),
			},
		},
		{
			name:    "openai responses completed event",
			payload: `{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":23,"output_tokens":9,"total_tokens":32}}}`,
			want: &Usage{
				InputTokens:  intPtr(23),
				OutputTokens: intPtr(9),
				TotalTokens:  intPtr(32),
			},
		},
		{
			name:    "openai reasoning spelling precedes anthropic fallback",
			payload: `{"usage":{"output_tokens_details":{"reasoning_tokens":0,"thinking_tokens":12}}}`,
			want: &Usage{
				ReasoningTokens: intPtr(0),
			},
		},
		{
			name:    "usage null",
			payload: `{"choices":[],"usage":null}`,
			want:    nil,
		},
		{
			name:    "usage empty object",
			payload: `{"choices":[],"usage":{}}`,
			want:    nil,
		},
		{
			name:    "legacy argo string response",
			payload: `{"response":"plain text answer"}`,
			want:    nil,
		},
		{
			name:    "no usage anywhere",
			payload: `{"content":[{"type":"text","text":"hi"}]}`,
			want:    nil,
		},
		{
			name:    "not json",
			payload: `plain text`,
			want:    nil,
		},
		{
			name:    "empty payload",
			payload: ``,
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertUsageEquals(t, UsageFromPayload([]byte(tt.payload)), tt.want)
		})
	}
}

// TestUsageFromFixtureCaptures pins extraction against real captured provider
// bodies rather than hand-written payloads.
func TestUsageFromFixtureCaptures(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}

	tests := []struct {
		name    string
		caseID  string
		capture string
		want    *Usage
	}{
		{
			name:    "anthropic",
			caseID:  "anthropic-messages-basic-text",
			capture: "captures/anthropic.response.json",
			want: &Usage{
				InputTokens:              intPtr(10),
				OutputTokens:             intPtr(19),
				CacheReadInputTokens:     intPtr(0),
				CacheCreationInputTokens: intPtr(0),
			},
		},
		{
			name:    "openai",
			caseID:  "anthropic-messages-basic-text",
			capture: "captures/openai.response.json",
			want: &Usage{
				InputTokens:          intPtr(9),
				OutputTokens:         intPtr(14),
				TotalTokens:          intPtr(23),
				CacheReadInputTokens: intPtr(0),
				ReasoningTokens:      intPtr(0),
			},
		},
		{
			name:    "google",
			caseID:  "anthropic-messages-basic-text",
			capture: "captures/google.response.json",
			want: &Usage{
				InputTokens:  intPtr(4),
				OutputTokens: intPtr(9),
				TotalTokens:  intPtr(13),
			},
		},
		{
			name:    "argo legacy reports no usage",
			caseID:  "anthropic-messages-basic-text",
			capture: "captures/argo.response.json",
			want:    nil,
		},
		{
			name:    "openai responses",
			caseID:  "openai-responses-basic-text",
			capture: "captures/openai-responses.response.json",
			want: &Usage{
				InputTokens:          intPtr(23),
				OutputTokens:         intPtr(9),
				TotalTokens:          intPtr(32),
				CacheReadInputTokens: intPtr(0),
				ReasoningTokens:      intPtr(0),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := apifixtures.ReadCaseFile(suite.Root, tt.caseID, tt.capture)
			if err != nil {
				t.Fatalf("ReadCaseFile(%q, %q) error = %v", tt.caseID, tt.capture, err)
			}
			assertUsageEquals(t, UsageFromPayload(data), tt.want)
		})
	}
}

func TestUsageMergedWith(t *testing.T) {
	var none *Usage
	start := &Usage{InputTokens: intPtr(100), OutputTokens: intPtr(1)}
	final := &Usage{OutputTokens: intPtr(19)}

	if got := none.MergedWith(start); got != start {
		t.Fatalf("nil.MergedWith(start) = %+v, want the start value", got)
	}
	if got := start.MergedWith(nil); got != start {
		t.Fatalf("start.MergedWith(nil) = %+v, want the start value", got)
	}

	merged := start.MergedWith(final)
	assertUsageEquals(t, merged, &Usage{InputTokens: intPtr(100), OutputTokens: intPtr(19)})

	// The merge builds a new value; the inputs keep what they reported.
	if *start.OutputTokens != 1 {
		t.Fatalf("merge mutated its input: OutputTokens = %d, want 1", *start.OutputTokens)
	}
}

func TestUsageSummary(t *testing.T) {
	tests := []struct {
		name  string
		usage *Usage
		want  string
	}{
		{
			name:  "input and output only",
			usage: &Usage{InputTokens: intPtr(100), OutputTokens: intPtr(19)},
			want:  "input 100, output 19",
		},
		{
			name: "zero details are omitted",
			usage: &Usage{
				InputTokens:              intPtr(9),
				OutputTokens:             intPtr(14),
				TotalTokens:              intPtr(23),
				CacheReadInputTokens:     intPtr(0),
				CacheCreationInputTokens: intPtr(0),
				ReasoningTokens:          intPtr(0),
			},
			want: "input 9, output 14, total 23",
		},
		{
			name: "non-zero details are reported",
			usage: &Usage{
				InputTokens:              intPtr(1234),
				OutputTokens:             intPtr(56),
				TotalTokens:              intPtr(1290),
				CacheReadInputTokens:     intPtr(512),
				CacheCreationInputTokens: intPtr(64),
				ReasoningTokens:          intPtr(20),
			},
			want: "input 1234, cache read 512, cache write 64, output 56, reasoning 20, total 1290",
		},
		{
			name:  "reported zero main counts still show",
			usage: &Usage{InputTokens: intPtr(0), OutputTokens: intPtr(0)},
			want:  "input 0, output 0",
		},
		{
			name:  "nil usage renders empty",
			usage: nil,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.usage.Summary(); got != tt.want {
				t.Fatalf("Summary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAnthropicStreamStateCapturesUsage(t *testing.T) {
	state := &AnthropicStreamState{}
	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":100,"output_tokens":1,"output_tokens_details":{"thinking_tokens":3}}}}`,
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":19,"output_tokens_details":{"thinking_tokens":12}}}`,
		"event: message_stop",
		`data: {"type":"message_stop"}`,
	}
	for _, line := range lines {
		if _, _, _, err := state.ParseLine(line); err != nil {
			t.Fatalf("ParseLine(%q) error = %v", line, err)
		}
	}
	// message_start reported the input count, while message_delta reported the
	// final output and thinking counts; the state must carry all three.
	assertUsageEquals(t, state.Usage(), &Usage{
		InputTokens:     intPtr(100),
		OutputTokens:    intPtr(19),
		ReasoningTokens: intPtr(12),
	})
}

func TestOpenAIStreamStateCapturesUsage(t *testing.T) {
	state := NewOpenAIStreamState()
	lines := []string{
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}],"usage":null}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":null}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":19,"total_tokens":28,"completion_tokens_details":{"reasoning_tokens":5}}}`,
		`data: [DONE]`,
	}
	for _, line := range lines {
		if _, _, _, err := state.ParseLine(line); err != nil {
			t.Fatalf("ParseLine(%q) error = %v", line, err)
		}
	}
	assertUsageEquals(t, state.Usage(), &Usage{
		InputTokens:     intPtr(9),
		OutputTokens:    intPtr(19),
		TotalTokens:     intPtr(28),
		ReasoningTokens: intPtr(5),
	})
}

func TestOpenAIStreamStateWithoutUsageChunkReportsNone(t *testing.T) {
	state := NewOpenAIStreamState()
	lines := []string{
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}
	for _, line := range lines {
		if _, _, _, err := state.ParseLine(line); err != nil {
			t.Fatalf("ParseLine(%q) error = %v", line, err)
		}
	}
	if state.Usage() != nil {
		t.Fatalf("Usage() = %+v, want nil for a stream that reported none", state.Usage())
	}
}

func TestGoogleStreamStateCapturesUsage(t *testing.T) {
	state := &GoogleStreamState{}
	lines := []string{
		`data: {"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}`,
		`data: {"candidates":[{"content":{"parts":[{"text":" there"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":9,"totalTokenCount":13}}`,
	}
	for _, line := range lines {
		if _, _, _, err := state.ParseLine(line); err != nil {
			t.Fatalf("ParseLine(%q) error = %v", line, err)
		}
	}
	// Google restates running totals per chunk; the final chunk's counts win.
	assertUsageEquals(t, state.Usage(), &Usage{
		InputTokens:  intPtr(4),
		OutputTokens: intPtr(9),
		TotalTokens:  intPtr(13),
	})
}

func TestOpenAIResponsesStreamStateCapturesUsage(t *testing.T) {
	state := NewOpenAIResponsesStreamState()
	lines := []string{
		`data: {"type":"response.created","response":{"id":"resp_1","usage":null}}`,
		`data: {"type":"response.output_text.delta","delta":"hi","output_index":0}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":23,"input_tokens_details":{"cached_tokens":0},"output_tokens":9,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":32}}}`,
	}
	for _, line := range lines {
		if _, _, _, err := state.ParseLine(line); err != nil {
			t.Fatalf("ParseLine(%q) error = %v", line, err)
		}
	}
	assertUsageEquals(t, state.Usage(), &Usage{
		InputTokens:          intPtr(23),
		OutputTokens:         intPtr(9),
		TotalTokens:          intPtr(32),
		CacheReadInputTokens: intPtr(0),
		ReasoningTokens:      intPtr(0),
	})
}

// TestHandleResponseNotifiesTokenUsage covers the operator-facing half: one
// note per handled response, rendered from the provider's counts, emitted at
// handle time — which is upstream of any tool approval or execution — and no
// note when the provider reported nothing.
func TestHandleResponseNotifiesTokenUsage(t *testing.T) {
	tests := []struct {
		name     string
		cfg      RequestOptions
		body     string
		wantNote string
	}{
		{
			name: "anthropic reports input output and thinking",
			cfg:  RequestOptions{Provider: "anthropic", Model: "claude-sonnet-5"},
			body: `{"content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":19,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens_details":{"thinking_tokens":12}}}`,

			wantNote: "Token usage: input 10, output 19, reasoning 12",
		},
		{
			name:     "openai reports totals",
			cfg:      RequestOptions{Provider: "openai", Model: "gpt-5"},
			body:     `{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":9,"completion_tokens":14,"total_tokens":23}}`,
			wantNote: "Token usage: input 9, output 14, total 23",
		},
		{
			name:     "response without usage stays quiet",
			cfg:      RequestOptions{Provider: "openai", Model: "gpt-5"},
			body:     `{"choices":[{"message":{"content":"hi"}}]}`,
			wantNote: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}
			notifier := &MockNotifier{}
			result, err := HandleResponse(context.Background(), tt.cfg, resp, &MockLogger{}, notifier)
			if err != nil {
				t.Fatalf("HandleResponse() error = %v", err)
			}

			var usageNotes []string
			for _, note := range notifier.infos {
				if strings.HasPrefix(note, "Token usage: ") {
					usageNotes = append(usageNotes, note)
				}
			}
			if tt.wantNote == "" {
				if len(usageNotes) != 0 {
					t.Fatalf("usage notes = %q, want none", usageNotes)
				}
				if result.Usage != nil {
					t.Fatalf("Response.Usage = %+v, want nil", result.Usage)
				}
				return
			}
			if len(usageNotes) != 1 || usageNotes[0] != tt.wantNote {
				t.Fatalf("usage notes = %q, want exactly [%q]", usageNotes, tt.wantNote)
			}
			if result.Usage == nil {
				t.Fatal("Response.Usage = nil, want the reported counts")
			}
		})
	}
}
