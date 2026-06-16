package proxy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestParseAnthropicStreamPreservesOutputTokensDetails verifies that
// usage.output_tokens_details (Anthropic's thinking-token breakdown) is parsed
// and forwarded to the client on the streaming passthrough path, and that it no
// longer produces an "Unknown JSON fields" warning.
func TestParseAnthropicStreamPreservesOutputTokensDetails(t *testing.T) {
	raw := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":0,"output_tokens_details":{"thinking_tokens":3}}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":42,"output_tokens_details":{"thinking_tokens":12}}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	recorder := newFlushableRecorder()
	server := &Server{}
	logs := captureWarnLogs(t, func() {
		handler, err := NewAnthropicStreamHandler(recorder, "claude-opus-4-7", context.Background())
		if err != nil {
			t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
		}
		if err := server.parseAnthropicStream(strings.NewReader(raw), handler); err != nil {
			t.Fatalf("parseAnthropicStream() error = %v", err)
		}
	})

	if strings.Contains(logs, "Unknown JSON fields") {
		t.Fatalf("unexpected unknown-field warning in logs:\n%s", logs)
	}

	out := recorder.Body.String()
	for _, want := range []string{
		`"output_tokens_details":{"thinking_tokens":3}`,
		`"output_tokens_details":{"thinking_tokens":12}`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream output missing %q\noutput:\n%s", want, out)
		}
	}
}

// TestAnthropicResponseRoundTripOutputTokensDetails verifies the non-streaming
// passthrough preserves usage.output_tokens_details across unmarshal/marshal.
func TestAnthropicResponseRoundTripOutputTokensDetails(t *testing.T) {
	in := `{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":42,"output_tokens_details":{"thinking_tokens":12}}}`

	var resp AnthropicResponse
	if err := json.Unmarshal([]byte(in), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.Usage == nil || resp.Usage.OutputTokensDetails == nil {
		t.Fatalf("output_tokens_details not parsed: %+v", resp.Usage)
	}
	if got := resp.Usage.OutputTokensDetails.ThinkingTokens; got != 12 {
		t.Fatalf("thinking_tokens = %d, want 12", got)
	}

	out, err := json.Marshal(&resp)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(out), `"output_tokens_details":{"thinking_tokens":12}`) {
		t.Fatalf("re-marshaled response missing output_tokens_details:\n%s", out)
	}
}

// TestUsageConversionThinkingTokens verifies thinking_tokens maps to
// reasoning_tokens across the OpenAI and Responses conversion helpers, in both
// directions.
func TestUsageConversionThinkingTokens(t *testing.T) {
	anth := &AnthropicUsage{
		InputTokens:         10,
		OutputTokens:        42,
		OutputTokensDetails: &AnthropicOutputTokensDetails{ThinkingTokens: 12},
	}

	openAI := AnthropicUsageToOpenAI(anth)
	if openAI.CompletionTokensDetails == nil || openAI.CompletionTokensDetails.ReasoningTokens != 12 {
		t.Fatalf("AnthropicUsageToOpenAI reasoning_tokens = %+v, want 12", openAI.CompletionTokensDetails)
	}

	responses := openAIUsageToResponsesUsage(openAI)
	if responses.OutputTokensDetails == nil || responses.OutputTokensDetails.ReasoningTokens != 12 {
		t.Fatalf("openAIUsageToResponsesUsage reasoning_tokens = %+v, want 12", responses.OutputTokensDetails)
	}

	back := OpenAIUsageToAnthropic(openAI)
	if back.OutputTokensDetails == nil || back.OutputTokensDetails.ThinkingTokens != 12 {
		t.Fatalf("OpenAIUsageToAnthropic thinking_tokens = %+v, want 12", back.OutputTokensDetails)
	}
}

// TestConvertAnthropicStreamToResponsesIncludesReasoningTokens verifies the
// Anthropic->Responses streaming conversion surfaces thinking_tokens from the
// message_delta as reasoning_tokens in the final Responses usage.
func TestConvertAnthropicStreamToResponsesIncludesReasoningTokens(t *testing.T) {
	raw := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":42,"output_tokens_details":{"thinking_tokens":12}}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	recorder := newFlushableRecorder()
	writer, err := newResponsesStreamWriter(recorder, context.Background(), "claude-opus-4-7")
	if err != nil {
		t.Fatalf("newResponsesStreamWriter() error = %v", err)
	}
	if err := writer.start(); err != nil {
		t.Fatalf("writer.start() error = %v", err)
	}
	server := &Server{}
	finishReason, err := server.convertAnthropicStreamToResponses(context.Background(), strings.NewReader(raw), writer)
	if err != nil {
		t.Fatalf("convertAnthropicStreamToResponses() error = %v", err)
	}
	resp, err := writer.Finish(finishReason)
	if err != nil {
		t.Fatalf("writer.Finish() error = %v", err)
	}
	if resp.Usage == nil || resp.Usage.OutputTokensDetails == nil {
		t.Fatalf("Responses usage missing output_tokens_details: %+v", resp.Usage)
	}
	if got := resp.Usage.OutputTokensDetails.ReasoningTokens; got != 12 {
		t.Fatalf("reasoning_tokens = %d, want 12", got)
	}
}
