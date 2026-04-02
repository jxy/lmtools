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
		`{"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":4},"candidates":[{"content":{"parts":[{"text":"Hi"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"weather"}}}]}}]}`,
		`{"candidates":[{"finishReason":"MAX_TOKENS"}]}`,
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
