package proxy

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIStreamParser_SharedStateTransitions(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "gpt-4", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}
	if err := ensureAnthropicTextPreamble(handler); err != nil {
		t.Fatalf("ensureAnthropicTextPreamble() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"usage":{"prompt_tokens":5,"completion_tokens":3},"choices":[]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"sum","arguments":"{\"a\":1}"}}]}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
	}, "\n")

	parser := NewOpenAIStreamParser(handler)
	if err := parser.Parse(strings.NewReader(stream)); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if handler.state.InputTokens != 5 {
		t.Fatalf("InputTokens = %d, want 5", handler.state.InputTokens)
	}
	if handler.state.OutputTokens != 3 {
		t.Fatalf("OutputTokens = %d, want 3", handler.state.OutputTokens)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, `"name":"sum"`) {
		t.Fatalf("expected tool_use start for sum, body=%s", body)
	}
	if !strings.Contains(body, `\"a\":1`) {
		t.Fatalf("expected tool input delta to contain arguments, body=%s", body)
	}
	if !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Fatalf("expected stop_reason tool_use, body=%s", body)
	}
}

func TestAnthropicStreamHandler_ParsedStateHelpersCloseTextAndReuseToolBlock(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "gpt-4", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}
	if err := ensureAnthropicTextPreamble(handler); err != nil {
		t.Fatalf("ensureAnthropicTextPreamble() error = %v", err)
	}
	if err := handler.SendTextDelta("Before tool"); err != nil {
		t.Fatalf("SendTextDelta() error = %v", err)
	}

	streamIndex := 0
	blockIndex, err := handler.BeginParsedToolUseBlock(&streamIndex, "toolu_1", "lookup")
	if err != nil {
		t.Fatalf("BeginParsedToolUseBlock() error = %v", err)
	}
	sameBlockIndex, err := handler.BeginParsedToolUseBlock(&streamIndex, "toolu_ignored", "ignored")
	if err != nil {
		t.Fatalf("BeginParsedToolUseBlock() second call error = %v", err)
	}
	if sameBlockIndex != blockIndex {
		t.Fatalf("same upstream tool index mapped to block %d, want %d", sameBlockIndex, blockIndex)
	}
	if err := handler.CloseParsedTextBlockIfNeeded(); err != nil {
		t.Fatalf("CloseParsedTextBlockIfNeeded() error = %v", err)
	}

	body := recorder.Body.String()
	if count := strings.Count(body, "event: content_block_stop"); count != 1 {
		t.Fatalf("content_block_stop count = %d, want 1\nbody=%s", count, body)
	}
	if count := strings.Count(body, `"type":"tool_use"`); count != 1 {
		t.Fatalf("tool_use starts = %d, want 1\nbody=%s", count, body)
	}
}

func TestOpenAIStreamParser_UsageAfterFinishReasonCompletesOnce(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "gpt-4", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}
	if err := ensureAnthropicTextPreamble(handler); err != nil {
		t.Fatalf("ensureAnthropicTextPreamble() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: {"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15},"choices":[]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	parser := NewOpenAIStreamParser(handler)
	if err := parser.Parse(strings.NewReader(stream)); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	body := recorder.Body.String()
	if count := strings.Count(body, "event: message_stop"); count != 1 {
		t.Fatalf("message_stop count = %d, want 1\nbody=%s", count, body)
	}
	if !strings.Contains(body, `"usage":{"input_tokens":10,"output_tokens":5}`) {
		t.Fatalf("expected final usage in message_delta, body=%s", body)
	}
}

func TestGoogleStreamParser_SharedStateTransitions(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "gemini-1.5-pro", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}
	if err := ensureAnthropicTextPreamble(handler); err != nil {
		t.Fatalf("ensureAnthropicTextPreamble() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":4},"candidates":[{"content":{"parts":[{"text":"Hi"}]}}]}`,
		"",
		`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"weather"}}}]}}]}`,
		"",
		`data: {"candidates":[{"finishReason":"MAX_TOKENS"}]}`,
		"",
	}, "\n")

	parser := NewGoogleStreamParser(handler)
	if err := parser.Parse(strings.NewReader(stream)); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if handler.state.InputTokens != 7 {
		t.Fatalf("InputTokens = %d, want 7", handler.state.InputTokens)
	}
	if handler.state.OutputTokens != 4 {
		t.Fatalf("OutputTokens = %d, want 4", handler.state.OutputTokens)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, `"name":"lookup"`) {
		t.Fatalf("expected tool_use start for lookup, body=%s", body)
	}
	if !strings.Contains(body, `"stop_reason":"max_tokens"`) {
		t.Fatalf("expected stop_reason max_tokens, body=%s", body)
	}
}

func TestGoogleStreamParser_FinishChunkStillEmitsText(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "gemini-1.5-pro", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}
	if err := ensureAnthropicTextPreamble(handler); err != nil {
		t.Fatalf("ensureAnthropicTextPreamble() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":4},"candidates":[{"content":{"parts":[{"text":"Hello"}]},"finishReason":"STOP"}]}`,
		"",
	}, "\n")

	parser := NewGoogleStreamParser(handler)
	if err := parser.Parse(strings.NewReader(stream)); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, `"text":"Hello"`) {
		t.Fatalf("expected finish chunk text to be emitted, body=%s", body)
	}
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected stop_reason end_turn, body=%s", body)
	}
}

func TestConvertGoogleStreamToOpenAI_SSE(t *testing.T) {
	recorder := newFlushableRecorder()
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-5.4-nano", context.Background())
	if err != nil {
		t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":4,"totalTokenCount":11},"candidates":[{"content":{"parts":[{"text":"Hi"}]}}]}`,
		"",
		`data: {"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":5,"totalTokenCount":12},"candidates":[{"finishReason":"STOP"}]}`,
		"",
	}, "\n")

	server := &Server{}
	if err := server.convertGoogleStreamToOpenAI(context.Background(), strings.NewReader(stream), writer); err != nil {
		t.Fatalf("convertGoogleStreamToOpenAI() error = %v", err)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, `"role":"assistant"`) {
		t.Fatalf("expected assistant role delta, body=%s", body)
	}
	if !strings.Contains(body, `"content":"Hi"`) {
		t.Fatalf("expected content delta, body=%s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("expected finish_reason stop, body=%s", body)
	}
	if !strings.Contains(body, `data: [DONE]`) {
		t.Fatalf("expected [DONE], body=%s", body)
	}
}

func TestConvertGoogleStreamToOpenAI_SSE_FinishChunkCarriesText(t *testing.T) {
	recorder := newFlushableRecorder()
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-5.4-nano", context.Background())
	if err != nil {
		t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":5,"totalTokenCount":12},"candidates":[{"content":{"parts":[{"text":"Hi"}]},"finishReason":"STOP"}]}`,
		"",
	}, "\n")

	server := &Server{}
	if err := server.convertGoogleStreamToOpenAI(context.Background(), strings.NewReader(stream), writer); err != nil {
		t.Fatalf("convertGoogleStreamToOpenAI() error = %v", err)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, `"content":"Hi"`) {
		t.Fatalf("expected finish chunk text to be emitted, body=%s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("expected finish_reason stop, body=%s", body)
	}
}

func TestConvertGoogleStreamToOpenAI_MalformedChunkReturnsError(t *testing.T) {
	recorder := newFlushableRecorder()
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-5.4-nano", context.Background())
	if err != nil {
		t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"candidates":{"not":"an array"}}`,
		"",
	}, "\n")

	server := &Server{}
	if err := server.convertGoogleStreamToOpenAI(context.Background(), strings.NewReader(stream), writer); err == nil {
		t.Fatal("convertGoogleStreamToOpenAI() error = nil, want malformed chunk error")
	}
}

func TestConvertGoogleStreamToOpenAI_FunctionCallWithoutIDGetsGeneratedID(t *testing.T) {
	recorder := newFlushableRecorder()
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-5.4-nano", context.Background())
	if err != nil {
		t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"weather"}}}]}}]}`,
		"",
		`data: {"candidates":[{"finishReason":"STOP"}]}`,
		"",
	}, "\n")

	server := &Server{}
	if err := server.convertGoogleStreamToOpenAI(context.Background(), strings.NewReader(stream), writer); err != nil {
		t.Fatalf("convertGoogleStreamToOpenAI() error = %v", err)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, `"id":"call_`) {
		t.Fatalf("expected generated call_ id, body=%s", body)
	}
	if !strings.Contains(body, `"name":"lookup"`) {
		t.Fatalf("expected lookup function name, body=%s", body)
	}
	argsDelta := `"arguments":"{\"q\":\"weather\"}"`
	if !strings.Contains(body, argsDelta) {
		t.Fatalf("expected serialized tool arguments, body=%s", body)
	}
	if count := strings.Count(body, argsDelta); count != 1 {
		t.Fatalf("serialized tool arguments count = %d, want 1\nbody=%s", count, body)
	}
}

func TestConvertGoogleStreamToOpenAI_IncludesUsageFromTypedParser(t *testing.T) {
	recorder := newFlushableRecorder()
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-5.4-nano", context.Background(), WithIncludeUsage(true))
	if err != nil {
		t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":5,"totalTokenCount":12},"candidates":[{"finishReason":"STOP"}]}`,
		"",
	}, "\n")

	server := &Server{}
	if err := server.convertGoogleStreamToOpenAI(context.Background(), strings.NewReader(stream), writer); err != nil {
		t.Fatalf("convertGoogleStreamToOpenAI() error = %v", err)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, `"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}`) {
		t.Fatalf("expected usage from Google stream chunk, body=%s", body)
	}
}

func TestOpenAIStreamParser_WarnsOnUnknownFields(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "gpt-4", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}
	if err := ensureAnthropicTextPreamble(handler); err != nil {
		t.Fatalf("ensureAnthropicTextPreamble() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"Hello"},"mystery":true}],"unexpectedTop":true}`,
		"",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	logs := captureWarnLogs(t, func() {
		parser := NewOpenAIStreamParser(handler)
		if err := parser.Parse(strings.NewReader(stream)); err != nil {
			t.Fatalf("Parse() error = %v", err)
		}
	})

	for _, want := range []string{
		`Unknown JSON fields in OpenAI stream chunk (ignored):`,
		`choices[].mystery`,
		`unexpectedTop`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("warning %q not found in logs:\n%s", want, logs)
		}
	}
}

func TestGoogleStreamParser_WarnsOnUnknownFields(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "gemini-1.5-pro", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}
	if err := ensureAnthropicTextPreamble(handler); err != nil {
		t.Fatalf("ensureAnthropicTextPreamble() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Hi","mysteryPart":true}]},"extraCandidate":true,"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":4,"totalTokenCount":11},"modelVersion":"gemini-3.1-flash-lite-preview","responseId":"resp_123","unexpectedTop":true}`,
		"",
	}, "\n")

	logs := captureWarnLogs(t, func() {
		parser := NewGoogleStreamParser(handler)
		if err := parser.Parse(strings.NewReader(stream)); err != nil {
			t.Fatalf("Parse() error = %v", err)
		}
	})

	for _, want := range []string{
		`Unknown JSON fields in Google stream chunk (ignored):`,
		`candidates[].content.parts[].mysteryPart`,
		`candidates[].extraCandidate`,
		`unexpectedTop`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("warning %q not found in logs:\n%s", want, logs)
		}
	}
}

func TestParseAnthropicStream_WarnsOnUnknownFieldsAndEvents(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus-20240229", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}

	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-3-opus-20240229","content":[],"usage":{"input_tokens":1,"output_tokens":0,"mystery_usage":true}}}`,
		"",
		`event: weird_event`,
		`data: {"type":"weird_event","mystery":true}`,
		"",
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	server := NewMinimalTestServer(t, &Config{})
	logs := captureWarnLogs(t, func() {
		if err := server.parseAnthropicStream(strings.NewReader(stream), handler); err != nil {
			t.Fatalf("parseAnthropicStream() error = %v", err)
		}
	})

	for _, want := range []string{
		`Unknown JSON fields in Anthropic stream message_start (ignored): message.usage.mystery_usage`,
		`Unknown Anthropic SSE event type "weird_event" ignored`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("warning %q not found in logs:\n%s", want, logs)
		}
	}
}

func TestConvertGoogleStreamToOpenAI_WarnsOnUnknownFields(t *testing.T) {
	recorder := newFlushableRecorder()
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-5.4-nano", context.Background())
	if err != nil {
		t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Hi","mysteryPart":true}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":4,"totalTokenCount":11},"unexpectedTop":true}`,
		"",
	}, "\n")

	server := &Server{}
	logs := captureWarnLogs(t, func() {
		if err := server.convertGoogleStreamToOpenAI(context.Background(), strings.NewReader(stream), writer); err != nil {
			t.Fatalf("convertGoogleStreamToOpenAI() error = %v", err)
		}
	})

	for _, want := range []string{
		`Unknown JSON fields in Google stream chunk (ignored):`,
		`candidates[].content.parts[].mysteryPart`,
		`unexpectedTop`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("warning %q not found in logs:\n%s", want, logs)
		}
	}
}

func TestConvertAnthropicStreamToOpenAI_WarnsOnUnknownFieldsAndEvents(t *testing.T) {
	recorder := newFlushableRecorder()
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-5.4-nano", context.Background())
	if err != nil {
		t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
	}

	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-3-opus-20240229","content":[],"usage":{"input_tokens":1,"output_tokens":0,"mystery_usage":true}}}`,
		"",
		`event: strange_event`,
		`data: {"type":"strange_event","mystery":true}`,
		"",
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	server := &Server{}
	logs := captureWarnLogs(t, func() {
		if err := server.convertAnthropicStreamToOpenAI(context.Background(), strings.NewReader(stream), writer); err != nil {
			t.Fatalf("convertAnthropicStreamToOpenAI() error = %v", err)
		}
	})

	for _, want := range []string{
		`Unknown JSON fields in Anthropic stream message_start (ignored): message.usage.mystery_usage`,
		`Unknown Anthropic SSE event type "strange_event" ignored during OpenAI conversion`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("warning %q not found in logs:\n%s", want, logs)
		}
	}
}
