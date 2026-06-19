package proxy

import (
	"context"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type delayedReadCloser struct {
	reader *strings.Reader
	delay  time.Duration
	once   sync.Once
}

func newDelayedReadCloser(delay time.Duration, body string) *delayedReadCloser {
	return &delayedReadCloser{
		reader: strings.NewReader(body),
		delay:  delay,
	}
}

func (r *delayedReadCloser) Read(p []byte) (int, error) {
	r.once.Do(func() {
		time.Sleep(r.delay)
	})
	return r.reader.Read(p)
}

func (r *delayedReadCloser) Close() error {
	return nil
}

func TestAnthropicStreamHandlerIdleHeartbeatSendsPing(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "claude-test", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}

	stop := handler.StartIdleHeartbeat(20 * time.Millisecond)
	time.Sleep(55 * time.Millisecond)
	stop()

	if got := strings.Count(recorder.Body.String(), "event: ping"); got < 1 {
		t.Fatalf("ping count = %d, want at least 1; body:\n%s", got, recorder.Body.String())
	}
}

func TestAnthropicStreamHandlerIdleHeartbeatResetsOnEvent(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "claude-test", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}

	stop := handler.StartIdleHeartbeat(150 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	if err := handler.SendMessageStart(); err != nil {
		t.Fatalf("SendMessageStart() error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	stop()

	if strings.Contains(recorder.Body.String(), "event: ping") {
		t.Fatalf("unexpected ping before idle interval elapsed; body:\n%s", recorder.Body.String())
	}
}

func TestAnthropicStreamHandlerIdleHeartbeatStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "claude-test", ctx)
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}

	stop := handler.StartIdleHeartbeat(20 * time.Millisecond)
	cancel()
	time.Sleep(55 * time.Millisecond)
	stop()

	if strings.Contains(recorder.Body.String(), "event: ping") {
		t.Fatalf("unexpected ping after context cancellation; body:\n%s", recorder.Body.String())
	}
}

func TestStreamFromAnthropicSendsIdlePingAfterUpstreamOK(t *testing.T) {
	stream := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":"","usage":{"input_tokens":1,"output_tokens":0}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	server := newStreamingHeartbeatTestServer(t, constants.ProviderAnthropic, stream)
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "claude-test", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}

	err = server.streamFromAnthropic(context.Background(), &AnthropicRequest{
		Model:     "claude-test",
		Messages:  []AnthropicMessage{{Role: "user", Content: []byte(`"hello"`)}},
		MaxTokens: 16,
	}, handler)
	if err != nil {
		t.Fatalf("streamFromAnthropic() error = %v", err)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, "event: ping") {
		t.Fatalf("missing idle ping; body:\n%s", body)
	}
	if !strings.Contains(body, "event: message_stop") {
		t.Fatalf("stream did not complete; body:\n%s", body)
	}
	if strings.Index(body, "event: ping") > strings.Index(body, "event: message_start") {
		t.Fatalf("idle ping should be emitted before delayed upstream message_start; body:\n%s", body)
	}
}

func TestStreamFromOpenAISendsIdlePingAfterUpstreamOK(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	server := newStreamingHeartbeatTestServer(t, constants.ProviderOpenAI, stream)
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "gpt-test", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}

	err = server.streamFromOpenAI(context.Background(), &AnthropicRequest{
		Model:     "gpt-test",
		Messages:  []AnthropicMessage{{Role: "user", Content: []byte(`"hello"`)}},
		MaxTokens: 16,
	}, handler)
	if err != nil {
		t.Fatalf("streamFromOpenAI() error = %v", err)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, "event: ping") {
		t.Fatalf("missing idle ping; body:\n%s", body)
	}
	if !strings.Contains(body, "event: message_stop") {
		t.Fatalf("stream did not complete; body:\n%s", body)
	}
	if strings.Index(body, "event: ping") < strings.Index(body, "event: message_start") {
		t.Fatalf("OpenAI conversion should send Anthropic preamble before idle ping; body:\n%s", body)
	}
}

func newStreamingHeartbeatTestServer(t *testing.T, provider, stream string) *Server {
	t.Helper()
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       newDelayedReadCloser(130*time.Millisecond, stream),
			Request:    r,
		}, nil
	})
	client := retry.NewClientWithTransport(
		10*time.Second,
		0,
		&retryLoggerAdapter{ctx: context.Background()},
		extractRequestLogger,
		transport,
	)
	return NewTestServerDirectWithClient(t, &Config{
		Provider:       provider,
		ProviderURL:    "https://provider.test/v1/messages",
		ProviderKeySet: ProviderKeySet{AnthropicAPIKey: "test-key", OpenAIAPIKey: "test-key"},
		PingInterval:   100 * time.Millisecond,
	}, client)
}

var _ io.ReadCloser = (*delayedReadCloser)(nil)
