package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAIResponsesStreamArgoOpenAIUsesUpstreamStreaming(t *testing.T) {
	SetupTestLogger(t)

	firstChunkSeen := make(chan struct{})
	releaseFinalChunk := make(chan struct{})
	var sawStream bool
	var sawPath string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var req OpenAIRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode backend request: %v", err)
		}
		sawStream = req.Stream
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"))
		flusher.Flush()
		close(firstChunkSeen)
		<-releaseFinalChunk
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer backend.Close()

	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        backend.URL,
		ArgoUser:           "test-key",
		MaxRequestBodySize: constants.DefaultMaxRequestBodySize,
	})
	defer cleanup()
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	reqBody := []byte(`{"model":"gpt-5","stream":true,"input":"say hi","max_output_tokens":16}`)
	resp, err := http.Post(proxyServer.URL+"/v1/responses", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	select {
	case <-firstChunkSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("backend did not receive streaming request")
	}

	scanner := bufio.NewScanner(resp.Body)
	gotDeltaBeforeFinal := false
	deadline := time.After(2 * time.Second)
	for !gotDeltaBeforeFinal {
		lineCh := make(chan string, 1)
		go func() {
			if scanner.Scan() {
				lineCh <- scanner.Text()
				return
			}
			lineCh <- ""
		}()
		select {
		case line := <-lineCh:
			if strings.Contains(line, "response.output_text.delta") || strings.Contains(line, `"delta":"hi"`) {
				gotDeltaBeforeFinal = true
			}
		case <-deadline:
			t.Fatal("did not receive downstream delta before backend final chunk")
		}
	}
	close(releaseFinalChunk)
	_, _ = io.Copy(io.Discard, resp.Body)

	if !sawStream {
		t.Fatal("backend request did not set stream=true")
	}
	if !strings.HasSuffix(sawPath, "/v1/chat/completions") {
		t.Fatalf("backend path=%s", sawPath)
	}
}

func TestOpenAIResponsesStreamArgoLegacyNonClaudeUsesLegacyChat(t *testing.T) {
	SetupTestLogger(t)

	var captured ArgoChatRequest
	var sawPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		if !strings.HasSuffix(r.URL.Path, "/api/v1/resource/chat/") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode backend request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(ArgoChatResponse{Response: "legacy stream ok"})
	}))
	defer backend.Close()

	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        backend.URL,
		ArgoUser:           "test-key",
		ArgoLegacy:         true,
		MaxRequestBodySize: constants.DefaultMaxRequestBodySize,
	})
	defer cleanup()
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	reqBody := []byte(`{"model":"gpt-5","stream":true,"input":"say hi","store":false}`)
	resp, err := http.Post(proxyServer.URL+"/v1/responses", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d; body=%s", resp.StatusCode, string(body))
	}

	seenCreated := false
	seenDelta := false
	seenCompleted := false
	scanner := NewTestSSEScanner(resp.Body)
	for scanner.Scan() {
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(scanner.Data()), &event); err != nil {
			t.Fatalf("invalid SSE data: %v; data = %s", err, scanner.Data())
		}
		switch event["type"] {
		case "response.created":
			seenCreated = true
		case "response.output_text.delta":
			if event["delta"] == "legacy stream ok" {
				seenDelta = true
			}
		case "response.completed":
			seenCompleted = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan response stream: %v", err)
	}
	if !seenCreated || !seenDelta || !seenCompleted {
		t.Fatalf("stream events created/delta/completed = %v/%v/%v", seenCreated, seenDelta, seenCompleted)
	}
	if captured.Model != "gpt-5" {
		t.Fatalf("backend model = %q, want gpt-5", captured.Model)
	}
	if strings.HasSuffix(sawPath, "/v1/chat/completions") {
		t.Fatalf("legacy stream incorrectly used native OpenAI endpoint: %s", sawPath)
	}
}

func TestOpenAIResponsesStreamArgoAnthropicUsesUpstreamStreaming(t *testing.T) {
	SetupTestLogger(t)

	firstChunkSeen := make(chan struct{})
	releaseFinalChunk := make(chan struct{})
	var sawStream bool
	var sawPath string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var req AnthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode backend request: %v", err)
		}
		sawStream = req.Stream
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			``,
		}, "\n") + "\n"))
		flusher.Flush()
		close(firstChunkSeen)
		<-releaseFinalChunk
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":1,"output_tokens":1}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n") + "\n"))
		flusher.Flush()
	}))
	defer backend.Close()

	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        backend.URL,
		ArgoUser:           "test-key",
		MaxRequestBodySize: constants.DefaultMaxRequestBodySize,
	})
	defer cleanup()
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	reqBody := []byte(`{"model":"claude-test","stream":true,"input":"say hi","max_output_tokens":16}`)
	resp, err := http.Post(proxyServer.URL+"/v1/responses", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	select {
	case <-firstChunkSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("backend did not receive streaming request")
	}

	scanner := bufio.NewScanner(resp.Body)
	gotDeltaBeforeFinal := false
	deadline := time.After(2 * time.Second)
	for !gotDeltaBeforeFinal {
		lineCh := make(chan string, 1)
		go func() {
			if scanner.Scan() {
				lineCh <- scanner.Text()
				return
			}
			lineCh <- ""
		}()
		select {
		case line := <-lineCh:
			if strings.Contains(line, "response.output_text.delta") || strings.Contains(line, `"delta":"hi"`) {
				gotDeltaBeforeFinal = true
			}
		case <-deadline:
			t.Fatal("did not receive downstream delta before backend final chunk")
		}
	}
	close(releaseFinalChunk)
	_, _ = io.Copy(io.Discard, resp.Body)

	if !sawStream {
		t.Fatal("backend request did not set stream=true")
	}
	if !strings.HasSuffix(sawPath, "/v1/messages") {
		t.Fatalf("backend path=%s", sawPath)
	}
}

func TestOpenAIResponsesStreamAnthropicUsesUpstreamStreaming(t *testing.T) {
	SetupTestLogger(t)

	firstChunkSeen := make(chan struct{})
	releaseFinalChunk := make(chan struct{})
	var sawStream bool
	var sawPath string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var req AnthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode backend request: %v", err)
		}
		sawStream = req.Stream
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			``,
		}, "\n") + "\n"))
		flusher.Flush()
		close(firstChunkSeen)
		<-releaseFinalChunk
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":1,"output_tokens":1}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n") + "\n"))
		flusher.Flush()
	}))
	defer backend.Close()

	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        backend.URL,
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: constants.DefaultMaxRequestBodySize,
	})
	defer cleanup()
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	reqBody := []byte(`{"model":"claude-test","stream":true,"input":"say hi","max_output_tokens":16}`)
	resp, err := http.Post(proxyServer.URL+"/v1/responses", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	assertResponsesDeltaBeforeFinal(t, resp, firstChunkSeen, releaseFinalChunk)

	if !sawStream {
		t.Fatal("backend request did not set stream=true")
	}
	if !strings.HasSuffix(sawPath, "/messages") {
		t.Fatalf("backend path=%s", sawPath)
	}
}

func TestOpenAIResponsesStreamGoogleUsesUpstreamStreaming(t *testing.T) {
	SetupTestLogger(t)

	firstChunkSeen := make(chan struct{})
	releaseFinalChunk := make(chan struct{})
	var sawPath string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		if !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1}}\n\n"))
		flusher.Flush()
		close(firstChunkSeen)
		<-releaseFinalChunk
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1}}\n\n"))
		flusher.Flush()
	}))
	defer backend.Close()

	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderGoogle,
		ProviderURL:        backend.URL,
		GoogleAPIKey:       "test-key",
		MaxRequestBodySize: constants.DefaultMaxRequestBodySize,
	})
	defer cleanup()
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	reqBody := []byte(`{"model":"gemini-test","stream":true,"input":"say hi","max_output_tokens":16}`)
	resp, err := http.Post(proxyServer.URL+"/v1/responses", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	assertResponsesDeltaBeforeFinal(t, resp, firstChunkSeen, releaseFinalChunk)

	if !strings.Contains(sawPath, ":streamGenerateContent") {
		t.Fatalf("backend path=%s", sawPath)
	}
}

func TestOpenAIResponsesStreamWriterFailEmitsTerminalEvent(t *testing.T) {
	SetupTestLogger(t)

	ctx := context.Background()
	recorder := httptest.NewRecorder()
	writer, err := newResponsesStreamWriter(recorder, ctx, "gpt-5")
	if err != nil {
		t.Fatalf("newResponsesStreamWriter() error = %v", err)
	}
	if err := writer.start(); err != nil {
		t.Fatalf("writer.start() error = %v", err)
	}
	if err := writer.WriteTextDelta("partial"); err != nil {
		t.Fatalf("writer.WriteTextDelta() error = %v", err)
	}
	resp, err := writer.Fail(errors.New("upstream stream error: boom"))
	if err != nil {
		t.Fatalf("writer.Fail() error = %v", err)
	}

	if resp.Status != "failed" {
		t.Fatalf("response status = %q, want failed", resp.Status)
	}
	errPayload, ok := resp.Error.(map[string]interface{})
	if !ok {
		t.Fatalf("response error = %#v, want map", resp.Error)
	}
	if errPayload["code"] != "upstream_stream_error" || errPayload["message"] != "upstream stream error: boom" {
		t.Fatalf("response error payload = %#v", errPayload)
	}
	if len(resp.Output) != 1 || resp.Output[0].Status != "incomplete" {
		t.Fatalf("response output = %#v, want one incomplete item", resp.Output)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "response.failed") {
		t.Fatalf("stream missing response.failed event: %s", body)
	}
	if strings.Contains(body, "response.completed") {
		t.Fatalf("stream unexpectedly completed after failure: %s", body)
	}
}

func TestOpenAIResponsesStreamWriterClosesToolItemsByOutputIndex(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer, err := newResponsesStreamWriter(recorder, context.Background(), "gpt-test")
	if err != nil {
		t.Fatalf("newResponsesStreamWriter() error = %v", err)
	}
	if err := writer.start(); err != nil {
		t.Fatalf("writer.start() error = %v", err)
	}
	if err := writer.WriteTextDelta("hi"); err != nil {
		t.Fatalf("writer.WriteTextDelta() error = %v", err)
	}
	if err := writer.WriteFunctionCallDelta(9, "call_fn", "lookup", `{"q":"x"}`); err != nil {
		t.Fatalf("writer.WriteFunctionCallDelta() error = %v", err)
	}
	if err := writer.WriteCustomToolCallDelta(3, "call_custom", "patch", "raw input"); err != nil {
		t.Fatalf("writer.WriteCustomToolCallDelta() error = %v", err)
	}
	resp, err := writer.Finish("stop")
	if err != nil {
		t.Fatalf("writer.Finish() error = %v", err)
	}
	if len(resp.Output) != 3 {
		t.Fatalf("output length = %d, want 3: %#v", len(resp.Output), resp.Output)
	}
	if resp.Output[1].Type != "function_call" || resp.Output[1].Arguments != `{"q":"x"}` {
		t.Fatalf("function output item = %#v", resp.Output[1])
	}
	if resp.Output[2].Type != "custom_tool_call" || resp.Output[2].Input != "raw input" {
		t.Fatalf("custom output item = %#v", resp.Output[2])
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "response.function_call_arguments.done") {
		t.Fatalf("stream missing function arguments done event: %s", body)
	}
	if !strings.Contains(body, "response.custom_tool_call_input.done") {
		t.Fatalf("stream missing custom tool input done event: %s", body)
	}
}

func TestOpenAIResponsesStreamAnthropicErrorEventSendsResponseFailed(t *testing.T) {
	SetupTestLogger(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
			``,
			`event: error`,
			`data: {"type":"error","error":{"type":"overloaded_error","message":"backend failed"}}`,
			``,
		}, "\n") + "\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer backend.Close()

	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        backend.URL,
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: constants.DefaultMaxRequestBodySize,
		SessionsDir:        t.TempDir(),
	})
	defer cleanup()
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	resp, err := http.Post(proxyServer.URL+"/v1/responses", "application/json", bytes.NewReader([]byte(`{"model":"claude-test","stream":true,"input":"say hi","max_output_tokens":16}`)))
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, body=%s", resp.StatusCode, string(body))
	}
	assertResponsesStreamFailedAfterPartial(t, string(body), "backend failed")
	assertFailedResponsesStreamPersisted(t, proxyServer.URL, string(body))
}

func TestOpenAIResponsesStreamArgoOpenAIMalformedChunkSendsResponseFailed(t *testing.T) {
	SetupTestLogger(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`,
			``,
			`data: {bad json`,
			``,
		}, "\n") + "\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer backend.Close()

	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        backend.URL,
		ArgoUser:           "test-key",
		MaxRequestBodySize: constants.DefaultMaxRequestBodySize,
		SessionsDir:        t.TempDir(),
	})
	defer cleanup()
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	resp, err := http.Post(proxyServer.URL+"/v1/responses", "application/json", bytes.NewReader([]byte(`{"model":"gpt-5","stream":true,"input":"say hi","max_output_tokens":16}`)))
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, body=%s", resp.StatusCode, string(body))
	}
	assertResponsesStreamFailedAfterPartial(t, string(body), "invalid character")
	assertFailedResponsesStreamPersisted(t, proxyServer.URL, string(body))
}

func TestOpenAIResponsesStreamArgoOpenAIErrorChunkSendsResponseFailed(t *testing.T) {
	SetupTestLogger(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`,
			``,
			`data: {"error":{"message":"quota exceeded","type":"insufficient_quota","code":"billing_hard_limit_reached"}}`,
			``,
		}, "\n") + "\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer backend.Close()

	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        backend.URL,
		ArgoUser:           "test-key",
		MaxRequestBodySize: constants.DefaultMaxRequestBodySize,
		SessionsDir:        t.TempDir(),
	})
	defer cleanup()
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	resp, err := http.Post(proxyServer.URL+"/v1/responses", "application/json", bytes.NewReader([]byte(`{"model":"gpt-5","stream":true,"input":"say hi","max_output_tokens":16}`)))
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, body=%s", resp.StatusCode, string(body))
	}
	assertResponsesStreamFailedAfterPartial(t, string(body), "quota exceeded")
	for _, want := range []string{"insufficient_quota", "billing_hard_limit_reached"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("stream missing error detail %q: %s", want, string(body))
		}
	}
	assertFailedResponsesStreamPersisted(t, proxyServer.URL, string(body))
}

func TestOpenAIResponsesStreamFailureCommitSurvivesCanceledContext(t *testing.T) {
	SetupTestLogger(t)

	server := NewMinimalTestServer(t, &Config{SessionsDir: t.TempDir()})
	req := &OpenAIResponsesRequest{
		Model:  "claude-test",
		Input:  "say hi",
		Stream: true,
	}
	typedCurrent, err := OpenAIResponsesRequestToTyped(context.Background(), req)
	if err != nil {
		t.Fatalf("OpenAIResponsesRequestToTyped() error = %v", err)
	}
	stateCtx, _, err := server.prepareOpenAIResponsesStateWithMode(context.Background(), req, typedCurrent, req.Model, responsesStateForeground, responsesStoreRequested(req))
	if err != nil {
		t.Fatalf("prepareOpenAIResponsesStateWithMode(foreground) error = %v", err)
	}

	streamCtx, cancel := context.WithCancel(context.Background())
	recorder := httptest.NewRecorder()
	writer, err := newResponsesStreamWriter(recorder, streamCtx, req.Model)
	if err != nil {
		t.Fatalf("newResponsesStreamWriter() error = %v", err)
	}
	if err := writer.start(); err != nil {
		t.Fatalf("writer.start() error = %v", err)
	}
	if err := writer.WriteTextDelta("partial"); err != nil {
		t.Fatalf("writer.WriteTextDelta() error = %v", err)
	}
	failedID := writer.responseID
	cancel()

	server.failAndCommitOpenAIResponsesStream(streamCtx, stateCtx, req, typedCurrent, writer, context.Canceled, req.Model)

	rec, ok, err := server.responsesState.loadResponse(failedID)
	if err != nil || !ok {
		t.Fatalf("failed response record = rec:%#v ok:%v err:%v, want persisted", rec, ok, err)
	}
	if rec.Status != "failed" {
		t.Fatalf("failed response status = %q, want failed", rec.Status)
	}
	if rec.SessionPath == "" || rec.MessageID == "" {
		t.Fatalf("failed response state missing session linkage: %#v", rec)
	}
	history, err := server.responseRecordHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("responseRecordHistory() error = %v", err)
	}
	if len(history) == 0 || len(history[0].Blocks) == 0 {
		t.Fatalf("failed response history missing input message: %#v", history)
	}
	text, ok := history[0].Blocks[0].(core.TextBlock)
	if !ok || text.Text != "say hi" {
		t.Fatalf("failed response input history first block = %#v, want say hi", history[0].Blocks[0])
	}
}

func TestOpenAIResponsesStreamConvertsOpenAIToolCallDeltas(t *testing.T) {
	SetupTestLogger(t)

	ctx := context.Background()
	server := NewMinimalTestServer(t, &Config{})
	recorder := httptest.NewRecorder()
	writer, err := newResponsesStreamWriter(recorder, ctx, "gpt-5")
	if err != nil {
		t.Fatalf("newResponsesStreamWriter() error = %v", err)
	}
	if err := writer.start(); err != nil {
		t.Fatalf("writer.start() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function"}]},"finish_reason":null}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"lookup_city","arguments":"{\"city\":\"Chi"}}]},"finish_reason":null}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"cago\"}"}}]},"finish_reason":null}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	finishReason, err := server.convertOpenAIChatStreamToResponses(ctx, strings.NewReader(stream), writer)
	if err != nil {
		t.Fatalf("convertOpenAIChatStreamToResponses() error = %v", err)
	}
	resp, err := writer.Finish(finishReason)
	if err != nil {
		t.Fatalf("writer.Finish() error = %v", err)
	}

	if len(resp.Output) != 1 {
		t.Fatalf("output len=%d, want 1", len(resp.Output))
	}
	item := resp.Output[0]
	if item.Type != "function_call" || item.Name != "lookup_city" || item.Arguments != `{"city":"Chicago"}` {
		t.Fatalf("function item = %#v", item)
	}
	if item.CallID != "call_1" {
		t.Fatalf("function call_id = %q, want call_1", item.CallID)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "response.function_call_arguments.delta") || !strings.Contains(body, "response.function_call_arguments.done") {
		t.Fatalf("missing function call stream events: %s", body)
	}
}

func TestOpenAIResponsesStreamBuffersNamelessCustomToolCallDeltas(t *testing.T) {
	SetupTestLogger(t)

	ctx := context.Background()
	server := NewMinimalTestServer(t, &Config{})
	recorder := httptest.NewRecorder()
	writer, err := newResponsesStreamWriter(recorder, ctx, "gpt-5")
	if err != nil {
		t.Fatalf("newResponsesStreamWriter() error = %v", err)
	}
	writer.SetToolNameRegistry(responseToolNameRegistryFromCoreTools([]core.ToolDefinition{{
		Type: "custom",
		Name: "apply_patch",
	}}))
	if err := writer.start(); err != nil {
		t.Fatalf("writer.start() error = %v", err)
	}

	stream := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function"}]},"finish_reason":null}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"apply_patch","arguments":"{\"input\":\"*** Begin"}}]},"finish_reason":null}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":" Patch\\n*** End Patch\\n\"}"}}]},"finish_reason":null}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	finishReason, err := server.convertOpenAIChatStreamToResponses(ctx, strings.NewReader(stream), writer)
	if err != nil {
		t.Fatalf("convertOpenAIChatStreamToResponses() error = %v", err)
	}
	resp, err := writer.Finish(finishReason)
	if err != nil {
		t.Fatalf("writer.Finish() error = %v", err)
	}

	if len(resp.Output) != 1 {
		t.Fatalf("output len=%d, want 1: %#v", len(resp.Output), resp.Output)
	}
	item := resp.Output[0]
	if item.Type != "custom_tool_call" || item.Name != "apply_patch" || item.Input != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("custom tool item = %#v", item)
	}
	if item.CallID != "call_1" {
		t.Fatalf("custom call_id = %q, want call_1", item.CallID)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "response.function_call_arguments") || strings.Contains(body, `"type":"function_call"`) {
		t.Fatalf("unexpected function call stream events: %s", body)
	}
	if !strings.Contains(body, "response.custom_tool_call_input.delta") || !strings.Contains(body, "response.custom_tool_call_input.done") {
		t.Fatalf("missing custom tool stream events: %s", body)
	}
}

func TestConvertAnthropicStreamToResponsesDefaultsEmptyToolArguments(t *testing.T) {
	SetupTestLogger(t)

	ctx := context.Background()
	recorder := httptest.NewRecorder()
	writer, err := newResponsesStreamWriter(recorder, ctx, "claude-test")
	if err != nil {
		t.Fatalf("newResponsesStreamWriter() error = %v", err)
	}
	if err := writer.start(); err != nil {
		t.Fatalf("writer.start() error = %v", err)
	}

	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_time","input":{}}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":1,"output_tokens":1}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	finishReason, err := (&Server{}).convertAnthropicStreamToResponses(ctx, strings.NewReader(stream), writer)
	if err != nil {
		t.Fatalf("convertAnthropicStreamToResponses() error = %v", err)
	}
	resp, err := writer.Finish(finishReason)
	if err != nil {
		t.Fatalf("writer.Finish() error = %v", err)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("output len = %d, want 1: %#v", len(resp.Output), resp.Output)
	}
	item := resp.Output[0]
	if item.Type != "function_call" || item.Name != "get_time" || item.Arguments != "{}" {
		t.Fatalf("function item = %#v, want get_time with empty JSON object arguments", item)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "response.function_call_arguments.delta") {
		t.Fatalf("unexpected synthetic argument delta in stream: %s", body)
	}
	if !strings.Contains(body, "response.function_call_arguments.done") || !strings.Contains(body, `"arguments":"{}"`) {
		t.Fatalf("stream missing done event with empty JSON object arguments: %s", body)
	}
}

func TestConvertAnthropicStreamToResponsesUnwrapsCustomToolInput(t *testing.T) {
	SetupTestLogger(t)

	ctx := context.Background()
	recorder := httptest.NewRecorder()
	writer, err := newResponsesStreamWriter(recorder, ctx, "claude-test")
	if err != nil {
		t.Fatalf("newResponsesStreamWriter() error = %v", err)
	}
	writer.SetToolNameRegistry(responseToolNameRegistryFromCoreTools([]core.ToolDefinition{{
		Type: "custom",
		Name: "apply_patch",
	}}))
	if err := writer.start(); err != nil {
		t.Fatalf("writer.start() error = %v", err)
	}

	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"apply_patch","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"*** Begin"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" Patch\\n*** End Patch\\n\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":1,"output_tokens":1}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	finishReason, err := (&Server{}).convertAnthropicStreamToResponses(ctx, strings.NewReader(stream), writer)
	if err != nil {
		t.Fatalf("convertAnthropicStreamToResponses() error = %v", err)
	}
	resp, err := writer.Finish(finishReason)
	if err != nil {
		t.Fatalf("writer.Finish() error = %v", err)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("output len = %d, want 1: %#v", len(resp.Output), resp.Output)
	}
	item := resp.Output[0]
	if item.Type != "custom_tool_call" || item.Name != "apply_patch" || item.Input != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("custom item = %#v", item)
	}
	if strings.Contains(recorder.Body.String(), "response.function_call_arguments") {
		t.Fatalf("custom stream should not emit function argument events: %s", recorder.Body.String())
	}
}

func TestConvertAnthropicStreamToResponsesMergesPartialUsageDeltas(t *testing.T) {
	SetupTestLogger(t)

	ctx := context.Background()
	recorder := httptest.NewRecorder()
	writer, err := newResponsesStreamWriter(recorder, ctx, "claude-test")
	if err != nil {
		t.Fatalf("newResponsesStreamWriter() error = %v", err)
	}
	if err := writer.start(); err != nil {
		t.Fatalf("writer.start() error = %v", err)
	}

	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":12,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done."}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	finishReason, err := (&Server{}).convertAnthropicStreamToResponses(ctx, strings.NewReader(stream), writer)
	if err != nil {
		t.Fatalf("convertAnthropicStreamToResponses() error = %v", err)
	}
	resp, err := writer.Finish(finishReason)
	if err != nil {
		t.Fatalf("writer.Finish() error = %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("final response usage is nil")
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 3 || resp.Usage.TotalTokens != 15 {
		t.Fatalf("final response usage = %#v, want input=12 output=3 total=15", resp.Usage)
	}

	completed := responseCompletedEvent(t, recorder.Body.String())
	if completed.Usage == nil {
		t.Fatal("response.completed usage is nil")
	}
	if completed.Usage.InputTokens != 12 || completed.Usage.OutputTokens != 3 || completed.Usage.TotalTokens != 15 {
		t.Fatalf("response.completed usage = %#v, want input=12 output=3 total=15", completed.Usage)
	}
}

func TestOpenAIResponsesStreamWriterIncludesConversation(t *testing.T) {
	SetupTestLogger(t)

	ctx := context.Background()
	recorder := httptest.NewRecorder()
	writer, err := newResponsesStreamWriter(recorder, ctx, "claude-test")
	if err != nil {
		t.Fatalf("newResponsesStreamWriter() error = %v", err)
	}
	writer.SetConversationID("conv_test")
	if err := writer.start(); err != nil {
		t.Fatalf("writer.start() error = %v", err)
	}
	if err := writer.WriteTextDelta("hi"); err != nil {
		t.Fatalf("writer.WriteTextDelta() error = %v", err)
	}
	resp, err := writer.Finish("stop")
	if err != nil {
		t.Fatalf("writer.Finish() error = %v", err)
	}
	if resp.Conversation == nil || resp.Conversation.ID != "conv_test" {
		t.Fatalf("final response conversation = %#v, want conv_test", resp.Conversation)
	}
	body := recorder.Body.String()
	if strings.Count(body, `"conversation":{"id":"conv_test"}`) < 2 {
		t.Fatalf("stream events missing conversation id: %s", body)
	}
}

func TestConvertAnthropicStreamToResponsesPreservesThinking(t *testing.T) {
	SetupTestLogger(t)

	ctx := context.Background()
	recorder := httptest.NewRecorder()
	writer, err := newResponsesStreamWriter(recorder, ctx, "claude-test")
	if err != nil {
		t.Fatalf("newResponsesStreamWriter() error = %v", err)
	}
	if err := writer.start(); err != nil {
		t.Fatalf("writer.start() error = %v", err)
	}

	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"I should inspect the tool result."}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"1"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Done."}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":1,"output_tokens":3}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	finishReason, err := (&Server{}).convertAnthropicStreamToResponses(ctx, strings.NewReader(stream), writer)
	if err != nil {
		t.Fatalf("convertAnthropicStreamToResponses() error = %v", err)
	}
	resp, err := writer.Finish(finishReason)
	if err != nil {
		t.Fatalf("writer.Finish() error = %v", err)
	}
	if len(resp.Output) != 2 {
		t.Fatalf("output len=%d, want 2: %#v", len(resp.Output), resp.Output)
	}
	if resp.Output[0].Type != "reasoning" || resp.Output[0].Status != "completed" {
		t.Fatalf("reasoning output item = %#v", resp.Output[0])
	}

	blocks := writer.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2: %#v", len(blocks), blocks)
	}
	reasoning, ok := blocks[0].(core.ReasoningBlock)
	if !ok {
		t.Fatalf("blocks[0] = %T, want ReasoningBlock", blocks[0])
	}
	if reasoning.Provider != "anthropic" || reasoning.Type != "thinking" || reasoning.Text != "I should inspect the tool result." || reasoning.Signature != "sig_1" {
		t.Fatalf("reasoning block = %#v", reasoning)
	}
	text, ok := blocks[1].(core.TextBlock)
	if !ok || text.Text != "Done." {
		t.Fatalf("blocks[1] = %#v, want text Done.", blocks[1])
	}

	body := recorder.Body.String()
	if !strings.Contains(body, `"type":"reasoning"`) || !strings.Contains(body, "response.output_item.done") {
		t.Fatalf("stream missing reasoning output events: %s", body)
	}
}

func assertResponsesDeltaBeforeFinal(t *testing.T, resp *http.Response, firstChunkSeen <-chan struct{}, releaseFinalChunk chan<- struct{}) {
	t.Helper()
	select {
	case <-firstChunkSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("backend did not receive streaming request")
	}

	scanner := bufio.NewScanner(resp.Body)
	gotDeltaBeforeFinal := false
	deadline := time.After(2 * time.Second)
	for !gotDeltaBeforeFinal {
		lineCh := make(chan string, 1)
		go func() {
			if scanner.Scan() {
				lineCh <- scanner.Text()
				return
			}
			lineCh <- ""
		}()
		select {
		case line := <-lineCh:
			if strings.Contains(line, "response.output_text.delta") || strings.Contains(line, `"delta":"hi"`) {
				gotDeltaBeforeFinal = true
			}
		case <-deadline:
			t.Fatal("did not receive downstream delta before backend final chunk")
		}
	}
	close(releaseFinalChunk)
	_, _ = io.Copy(io.Discard, resp.Body)
}

func assertResponsesStreamFailedAfterPartial(t *testing.T, body, messageSubstring string) {
	t.Helper()
	if !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, `"delta":"partial"`) {
		t.Fatalf("stream missing partial delta before failure: %s", body)
	}
	if !strings.Contains(body, "response.failed") {
		t.Fatalf("stream missing response.failed terminal event: %s", body)
	}
	if strings.Contains(body, "response.completed") {
		t.Fatalf("stream unexpectedly completed after failure: %s", body)
	}
	if !strings.Contains(body, `"status":"failed"`) {
		t.Fatalf("failed event missing failed response status: %s", body)
	}
	if !strings.Contains(body, `"code":"upstream_stream_error"`) {
		t.Fatalf("failed event missing upstream_stream_error code: %s", body)
	}
	if !strings.Contains(body, messageSubstring) {
		t.Fatalf("failed event missing error message substring %q: %s", messageSubstring, body)
	}
}

func assertFailedResponsesStreamPersisted(t *testing.T, proxyURL, body string) {
	t.Helper()
	failed := responseFailedEvent(t, body)
	if failed.ID == "" {
		t.Fatalf("failed response missing id: %#v", failed)
	}
	retrieved := getProxyJSON(t, proxyURL+"/v1/responses/"+failed.ID)
	if got, _ := retrieved["id"].(string); got != failed.ID {
		t.Fatalf("retrieved failed response id = %q, want %q", got, failed.ID)
	}
	if got, _ := retrieved["status"].(string); got != "failed" {
		t.Fatalf("retrieved response status = %q, want failed; payload = %#v", got, retrieved)
	}
	errorPayload, ok := retrieved["error"].(map[string]interface{})
	if !ok || errorPayload["code"] != "upstream_stream_error" {
		t.Fatalf("retrieved response error = %#v, want upstream_stream_error", retrieved["error"])
	}
	if got, _ := lookupStatefulJSONPath(retrieved, "output.0.content.0.text"); got != "partial" {
		t.Fatalf("retrieved failed response partial output = %#v, want partial; payload = %#v", got, retrieved)
	}
	inputItems := getProxyJSON(t, proxyURL+"/v1/responses/"+failed.ID+"/input_items")
	data, _ := inputItems["data"].([]interface{})
	if len(data) != 1 {
		t.Fatalf("failed response input_items count = %d, want 1; payload = %#v", len(data), inputItems)
	}
}

func getProxyJSON(t *testing.T, url string) map[string]interface{} {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("GET %s status = %d, body = %s", url, resp.StatusCode, string(body))
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode GET %s body: %v; body = %s", url, err, string(body))
	}
	return decoded
}

func responseFailedEvent(t *testing.T, body string) OpenAIResponsesResponse {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || !strings.Contains(line, `"type":"response.failed"`) {
			continue
		}
		var payload struct {
			Response OpenAIResponsesResponse `json:"response"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
			t.Fatalf("decode response.failed payload: %v", err)
		}
		return payload.Response
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan stream body: %v", err)
	}
	t.Fatalf("response.failed event not found in stream: %s", body)
	return OpenAIResponsesResponse{}
}

func responseCompletedEvent(t *testing.T, body string) OpenAIResponsesResponse {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || !strings.Contains(line, `"type":"response.completed"`) {
			continue
		}
		var payload struct {
			Response OpenAIResponsesResponse `json:"response"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
			t.Fatalf("decode response.completed payload: %v", err)
		}
		return payload.Response
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan stream body: %v", err)
	}
	t.Fatalf("response.completed event not found in stream: %s", body)
	return OpenAIResponsesResponse{}
}
