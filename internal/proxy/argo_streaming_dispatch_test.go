package proxy

import (
	"context"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamNativeArgoAnthropicUsesAnthropicEndpoint(t *testing.T) {
	SetupTestLogger(t)
	defer logger.Close()

	var sawAnthropic bool
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		sawAnthropic = true
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-haiku-20240307","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
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
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer mockArgo.Close()

	server := NewTestServerDirectWithClient(t, &Config{
		Provider:    constants.ProviderArgo,
		ProviderURL: mockArgo.URL,
	}, retry.NewClient(10*time.Minute, logger.GetLogger()))

	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "claude-3-haiku-20240307", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}

	err = server.streamNativeArgoAnthropic(context.Background(), &AnthropicRequest{
		Model:     "claude-3-haiku-20240307",
		MaxTokens: 10,
		Messages:  []AnthropicMessage{{Role: "user", Content: []byte(`"hello"`)}},
	}, handler)
	if err != nil {
		t.Fatalf("streamNativeArgoAnthropic() error = %v", err)
	}
	if !sawAnthropic {
		t.Fatal("Argo Anthropic endpoint was not called")
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"text":"hi"`) {
		t.Fatalf("expected Anthropic stream text, body=%s", body)
	}
}

func TestStreamNativeArgoOpenAIUsesOpenAIEndpointAndPreamble(t *testing.T) {
	SetupTestLogger(t)
	defer logger.Close()

	var sawOpenAI bool
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		sawOpenAI = true
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n")))
	}))
	defer mockArgo.Close()

	server := NewTestServerDirectWithClient(t, &Config{
		Provider:    constants.ProviderArgo,
		ProviderURL: mockArgo.URL,
	}, retry.NewClient(10*time.Minute, logger.GetLogger()))

	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "gpt-5", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}

	err = server.streamNativeArgoOpenAI(context.Background(), &AnthropicRequest{
		Model:     "gpt-5",
		MaxTokens: 10,
		Messages:  []AnthropicMessage{{Role: "user", Content: []byte(`"hello"`)}},
	}, handler)
	if err != nil {
		t.Fatalf("streamNativeArgoOpenAI() error = %v", err)
	}
	if !sawOpenAI {
		t.Fatal("Argo OpenAI endpoint was not called")
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "event: message_start") || !strings.Contains(body, `"text":"hi"`) {
		t.Fatalf("expected Anthropic preamble and converted OpenAI text, body=%s", body)
	}
}
