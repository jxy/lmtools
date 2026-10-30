package retry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The error event Argo sent on 2026-10-29 in place of a gpt6astra answer,
// and a Chat stream that answers.
const (
	argoStreamFailure = `data: {"error": {"message": "Streaming response failed", "type": "internal_error", "code": "internal_error"}}` + "\n\n"
	answerStream      = `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
)

func TestJudgeStreamDataNamesErrorStatuses(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		kind    streamEvent
		status  int
		message string
	}{
		{"argo failure", `{"error": {"message": "Streaming response failed", "type": "internal_error", "code": "internal_error"}}`, eventError, 500, "Streaming response failed (type: internal_error, code: internal_error)"},
		{"argo backend 404", `{"error": {"message": "Error code: 404 - {'error': {'code': 'DeploymentNotFound'}}", "type": "NotFoundError", "code": "internal_error"}}`, eventError, 404, ""},
		{"argo backend 400", `{"error": {"message": "Error code: 400 - {'error': {'code': 'context_length_exceeded'}}", "type": "BadRequestError", "code": "internal_error"}}`, eventError, 400, ""},
		{"argo backend 503", `{"error": {"message": "Error code: 503 - {'error': {'message': 'busy'}}", "type": "InternalServerError", "code": "internal_error"}}`, eventError, 503, ""},
		{"argo backend unreachable", `{"error": {"message": "Connection error.", "type": "APIConnectionError", "code": "internal_error"}}`, eventError, 502, ""},
		{"argo unknown exception", `{"error": {"message": "boom", "type": "ValueError", "code": "internal_error"}}`, eventError, 0, ""},
		{"openai server error", `{"error":{"message":"The server had an error","type":"server_error","param":null,"code":null}}`, eventError, 500, "The server had an error (type: server_error)"},
		{"anthropic overloaded", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, eventError, 529, ""},
		{"anthropic bad request", `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`, eventError, 0, ""},
		{"google unavailable", `{"error":{"code":503,"message":"The model is overloaded.","status":"UNAVAILABLE"}}`, eventError, 503, "The model is overloaded. (code: 503)"},
		{"google bad request", `{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}`, eventError, 400, ""},
		{"responses error event", `{"type":"error","code":"server_error","message":"An error occurred","param":null,"sequence_number":2}`, eventError, 500, "An error occurred (code: server_error)"},
		{"responses failed", `{"type":"response.failed","response":{"status":"failed","error":{"code":"rate_limit_exceeded","message":"slow down"}}}`, eventError, 429, ""},
		{"responses failed on the prompt", `{"type":"response.failed","response":{"status":"failed","error":{"code":"invalid_prompt","message":"no"}}}`, eventError, 0, ""},
		{"error as a string", `{"error":"boom"}`, eventError, 0, "boom"},
		{"azure filter results", `{"choices":[],"created":0,"id":"","model":"","object":"","prompt_filter_results":[{"prompt_index":0,"content_filter_results":{"hate":{"filtered":false,"severity":"safe"}}}]}`, eventEmpty, 0, ""},
		{"role chunk", `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null,"tool_calls":[]},"logprobs":null,"finish_reason":null}]}`, eventEmpty, 0, ""},
		{"text chunk", `{"choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`, eventContent, 0, ""},
		{"reasoning chunk", `{"choices":[{"index":0,"delta":{"reasoning_content":"Let me see"},"finish_reason":null}]}`, eventContent, 0, ""},
		{"tool call chunk", `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call1","function":{"name":"f","arguments":""}}]},"finish_reason":null}]}`, eventContent, 0, ""},
		{"finished with nothing", `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, eventContent, 0, ""},
		{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10}}}`, eventEmpty, 0, ""},
		{"ping", `{"type": "ping"}`, eventEmpty, 0, ""},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, eventContent, 0, ""},
		{"response.created", `{"type":"response.created","response":{"id":"resp_1","status":"in_progress","error":null}}`, eventEmpty, 0, ""},
		{"response.in_progress", `{"type":"response.in_progress","response":{"id":"resp_1","status":"in_progress"}}`, eventEmpty, 0, ""},
		{"output text", `{"type":"response.output_text.delta","delta":"Hi"}`, eventContent, 0, ""},
		{"done", `[DONE]`, eventContent, 0, ""},
		{"plain text", `Hello`, eventContent, 0, ""},
		{"empty", ``, eventEmpty, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, status, message := judgeStreamData([]byte(tt.data))
			if kind != tt.kind || status != tt.status || (tt.message != "" && message != tt.message) {
				t.Fatalf("judgeStreamData() = %v, %d, %q; want %v, %d, %q", kind, status, message, tt.kind, tt.status, tt.message)
			}
		})
	}
}

type recordingLogger struct {
	mu     sync.Mutex
	errors []string
}

func (l *recordingLogger) Infof(string, ...interface{})  {}
func (l *recordingLogger) Debugf(string, ...interface{}) {}
func (l *recordingLogger) Errorf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, fmt.Sprintf(format, args...))
}

// streamReplies answers each attempt with the next of bodies, the last one
// again once they run out, as a 200 event stream unless contentType says
// otherwise. It returns the bodies it handed out.
func streamReplies(contentType string, bodies ...string) (http.RoundTripper, func() []*trackedResponseBody) {
	var mu sync.Mutex
	var sent []*trackedResponseBody
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		text := bodies[len(bodies)-1]
		if len(sent) < len(bodies) {
			text = bodies[len(sent)]
		}
		body := &trackedResponseBody{Reader: strings.NewReader(text)}
		sent = append(sent, body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: body, Request: req}, nil
	})
	return transport, func() []*trackedResponseBody {
		mu.Lock()
		defer mu.Unlock()
		return append([]*trackedResponseBody(nil), sent...)
	}
}

func streamCheckingClient(transport http.RoundTripper, retries int, logger Logger) *Client {
	client := newClient(clientOptions{
		timeout:     5 * time.Second,
		transport:   transport,
		logger:      logger,
		retryConfig: &Config{MaxRetries: retries, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond, BackoffFactor: 1},
	})
	client.RetryStreamStartErrors()
	return client
}

func readStream(t *testing.T, ctx context.Context, client *Client) string {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://argo.test/v1/chat/completions?key=secret", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(ctx, req, "lmc")
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the stream: %v", err)
	}
	return string(data)
}

func TestClientRetriesErrorEventBeforeContent(t *testing.T) {
	keepalive := ": keepalive\n\n"
	filter := `data: {"choices":[],"created":0,"id":"","model":"","object":"","prompt_filter_results":[{"prompt_index":0}]}` + "\n\n"
	role := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n"
	anthropic := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"content\":[]}}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	for name, failure := range map[string]string{
		"argo":                        argoStreamFailure,
		"argo without a newline":      strings.TrimSpace(argoStreamFailure),
		"argo ended by the stream":    strings.TrimSuffix(argoStreamFailure, "\n"),
		"after empty chunks":          keepalive + filter + role + argoStreamFailure,
		"anthropic after a start":     anthropic,
		"anthropic over two lines":    "event: error\ndata: {\ndata: \"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n",
		"argo over two lines":         "data: {\"error\": {\"message\": \"Streaming response failed\",\ndata: \"type\": \"internal_error\", \"code\": \"internal_error\"}}\n\n",
		"a comment inside the event":  "data: {\"error\": {\"message\": \"Streaming response failed\",\n: waiting\ndata: \"type\": \"internal_error\"}}\n\n",
		"crlf lines":                  strings.ReplaceAll(keepalive+filter+argoStreamFailure, "\n", "\r\n"),
		"other fields and stray text": "id: 1\nretry: 3000\nevent: chunk\nstray text\n" + argoStreamFailure,
	} {
		t.Run(name, func(t *testing.T) {
			transport, sent := streamReplies("text/event-stream; charset=utf-8", failure, answerStream)
			logger := &recordingLogger{}
			var progress []Progress
			ctx := WithProgress(context.Background(), func(p Progress) { progress = append(progress, p) })
			if got := readStream(t, ctx, streamCheckingClient(transport, 2, logger)); got != answerStream {
				t.Fatalf("stream = %q, want the answer", got)
			}
			bodies := sent()
			if len(bodies) != 2 || bodies[0].closes != 1 {
				t.Fatalf("attempts = %d, first body closed %d times", len(bodies), bodies[0].closes)
			}
			if len(progress) != 3 || progress[1].Delay <= 0 || progress[2].Attempt != 2 {
				t.Fatalf("progress = %+v", progress)
			}
			if len(logger.errors) != 1 || !strings.Contains(logger.errors[0], "POST https://argo.test/v1/chat/completions -> error event before any content (as HTTP 5") || strings.Contains(logger.errors[0], "secret") {
				t.Fatalf("logged = %q", logger.errors)
			}
		})
	}
}

func TestClientReturnsOtherStreamsWhole(t *testing.T) {
	content := `data: {"choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}` + "\n\n"
	backend404 := `data: {"error": {"message": "Error code: 404 - {'error': {'code': 'DeploymentNotFound'}}", "type": "NotFoundError", "code": "internal_error"}}` + "\n\n"
	longLine := "data: " + strings.Repeat("x", streamStartLineBytes+10) + "\n\n"
	manyPings := strings.Repeat(": ping\n\n", maxStreamStartBytes/8+1)
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{"error after content", "text/event-stream", content + argoStreamFailure},
		{"error after content over two lines", "text/event-stream", "data: {\"choices\":[{\"index\":0,\ndata: \"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n" + argoStreamFailure},
		{"error cut off by the end of the stream", "text/event-stream", "data: {\"error\": {\"message\": \"Streaming"},
		{"error naming a status not retried", "text/event-stream", backend404},
		{"answer", "text/event-stream", answerStream},
		{"long answer", "text/event-stream", content + strings.Repeat(content, 20000)},
		{"line too long to judge", "text/event-stream", longLine + argoStreamFailure},
		{"empty events past the limit", "text/event-stream", manyPings + argoStreamFailure},
		{"empty stream", "text/event-stream", ""},
		{"not an event stream", "application/json", `{"error": {"message": "Streaming response failed", "type": "internal_error"}}`},
		{"legacy argo text", "text/plain; charset=utf-8", argoStreamFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport, sent := streamReplies(tt.contentType, tt.body)
			if got := readStream(t, context.Background(), streamCheckingClient(transport, 2, nil)); got != tt.body {
				t.Fatalf("stream changed: got %d bytes, want %d", len(got), len(tt.body))
			}
			if bodies := sent(); len(bodies) != 1 || bodies[0].closes != 1 {
				t.Fatalf("attempts = %d, want 1 closed body", len(bodies))
			}
		})
	}
}

func TestClientLeavesStreamsUncheckedUnlessAsked(t *testing.T) {
	transport, sent := streamReplies("text/event-stream", argoStreamFailure, answerStream)
	client := newClient(clientOptions{
		timeout:     5 * time.Second,
		transport:   transport,
		retryConfig: &Config{MaxRetries: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond, BackoffFactor: 1},
	})
	if got := readStream(t, context.Background(), client); got != argoStreamFailure || len(sent()) != 1 {
		t.Fatalf("stream = %q after %d attempts", got, len(sent()))
	}
}

// The last attempt's stream reaches the caller as sent, for its parser to
// report the error.
func TestClientReturnsTheLastAttemptUnread(t *testing.T) {
	for _, retries := range []int{0, 2} {
		t.Run(fmt.Sprint(retries), func(t *testing.T) {
			transport, sent := streamReplies("text/event-stream", argoStreamFailure)
			if got := readStream(t, context.Background(), streamCheckingClient(transport, retries, nil)); got != argoStreamFailure {
				t.Fatalf("stream = %q", got)
			}
			if len(sent()) != retries+1 {
				t.Fatalf("attempts = %d, want %d", len(sent()), retries+1)
			}
		})
	}
}

type failingBody struct {
	data string
	err  error
}

func (b *failingBody) Read(p []byte) (int, error) {
	if b.data == "" {
		return 0, b.err
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

func (b *failingBody) Close() error { return nil }

// A connection lost while the stream is checked reaches the caller after
// the bytes that came before it, as it would have unchecked.
func TestClientPassesOnAReadErrorWhileChecking(t *testing.T) {
	start := ": ping\n\n" + `data: {"choices":[],"prompt_filter_results":[]}` + "\n\ndata: {\"cho"
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &failingBody{data: start, err: io.ErrUnexpectedEOF}, Request: req}, nil
	})
	req, _ := http.NewRequest(http.MethodPost, "https://argo.test/v1/chat/completions", strings.NewReader("{}"))
	resp, err := streamCheckingClient(transport, 2, nil).Do(context.Background(), req, "lmc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if string(data) != start || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read %q, %v", data, err)
	}
}

// Cancelling while Do waits for the first content ends the wait; the
// caller meets the cancellation reading the body, as it did before.
func TestClientCheckEndsWhenCancelled(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": waiting\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	client := NewClientForTesting(5*time.Second, 2, nil, nil)
	client.RetryStreamStartErrors()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("{}"))
	done := make(chan error, 1)
	go func() {
		resp, err := client.Do(ctx, req, "lmc")
		if err == nil {
			_, err = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Do kept waiting after the cancel")
	}
}
