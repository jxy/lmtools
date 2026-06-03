package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAIStopSpecialProcessingLogsRawBoundariesNonStream(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonRoundTripResponse(http.StatusOK, OpenAIResponse{
			ID:      "chatcmpl_stop",
			Object:  "chat.completion",
			Created: 1,
			Model:   "gpt-upstream",
			Choices: []OpenAIChoice{{Index: 0, Message: OpenAIMessage{Role: core.RoleAssistant, Content: "visible END hidden"}, FinishReason: "length"}},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "openai-key"},
		MaxRequestBodySize: fixtureMaxBodySize,
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)
	rawBody := `{"model":"gpt-test","messages":[{"role":"user","content":"stop please"}],"stop":["END"]}`

	logs := captureStderr(t, func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(rawBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "hidden") || !strings.Contains(w.Body.String(), "visible ") {
			t.Fatalf("unexpected response body after stop enforcement: %s", w.Body.String())
		}
	})

	for _, want := range []string{"WIRE CLIENT REQUEST", rawBody, "WIRE BACKEND RESPONSE BODY OpenAI", "visible END hidden", "WIRE CLIENT RESPONSE BODY", "OpenAI-compatible stop special processing for OpenAI"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
	backendRequest := logSection(logs, "WIRE BACKEND REQUEST OpenAI", "WIRE BACKEND RESPONSE HEADERS OpenAI")
	if strings.Contains(backendRequest, `"stop"`) {
		t.Fatalf("backend request log still contains stop field:\n%s", backendRequest)
	}
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "OpenAI-compatible stop special processing") && strings.Contains(line, "END") {
			t.Fatalf("warning leaked stop sequence: %s", line)
		}
	}
}

func TestOpenAIStopSpecialProcessingLogsRawBoundariesStream(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"upstream","choices":[{"index":0,"delta":{"content":"visible <STOP> hidden"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(upstream)),
			Request:    r,
		}, nil
	})
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "openai-key"},
		MaxRequestBodySize: fixtureMaxBodySize,
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)
	rawBody := `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"stop please"}],"stop":["<STOP>"]}`

	logs := captureStderr(t, func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(rawBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "hidden") || strings.Contains(w.Body.String(), "<STOP>") {
			t.Fatalf("client stream leaked stopped text: %s", w.Body.String())
		}
	})

	for _, want := range []string{"WIRE CLIENT REQUEST", rawBody, "WIRE BACKEND RESPONSE BODY OpenAI", "<STOP> hidden", "WIRE CLIENT STREAM", "OpenAI-compatible stop special processing for OpenAI"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
	if got := strings.Count(logs, "OpenAI-compatible stop special processing"); got != 1 {
		t.Fatalf("special-processing warning count = %d, want 1\nlogs:\n%s", got, logs)
	}
}

func logSection(logs, start, end string) string {
	startIdx := strings.Index(logs, start)
	if startIdx < 0 {
		return ""
	}
	section := logs[startIdx:]
	if endIdx := strings.Index(section, end); endIdx >= 0 {
		section = section[:endIdx]
	}
	return section
}

func TestOpenAIStreamParserAcceptsNoSpaceDataLines(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "gpt-test", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}
	if err := ensureAnthropicTextPreamble(handler); err != nil {
		t.Fatalf("ensureAnthropicTextPreamble() error = %v", err)
	}
	stream := strings.Join([]string{
		`data:{"choices":[{"index":0,"delta":{"content":"hello"}}]}`,
		"",
		`data:[DONE]`,
		"",
	}, "\n")
	parser := NewOpenAIStreamParser(handler)
	if err := parser.Parse(strings.NewReader(stream)); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "hello") || !strings.Contains(body, `"type":"message_stop"`) {
		t.Fatalf("stream did not parse no-space data lines: %s", body)
	}
}

func TestOpenAIStreamWriterStopSequencesSplitMatch(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-test", context.Background(), WithStopSequences([]string{"<STOP>"}))
	if err != nil {
		t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
	}
	if err := writer.WriteInitialAssistantTextDelta(); err != nil {
		t.Fatalf("WriteInitialAssistantTextDelta() error = %v", err)
	}
	if err := writer.WriteContent("hello <ST"); err != nil {
		t.Fatalf("WriteContent(first) error = %v", err)
	}
	if err := writer.WriteContent("OP> hidden"); err != nil {
		t.Fatalf("WriteContent(second) error = %v", err)
	}

	body := recorder.Body.String()
	if strings.Contains(body, "<STOP>") || strings.Contains(body, "hidden") {
		t.Fatalf("stream leaked stop text or following text: %s", body)
	}
	if !strings.Contains(body, `"content":"hell"`) || !strings.Contains(body, `"content":"o "`) {
		t.Fatalf("stream missing prefix text: %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("stream missing forced stop and done: %s", body)
	}
}

func TestOpenAIStreamParserStopSequencesDoNotTouchToolArgs(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "gpt-test", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}
	if err := ensureAnthropicTextPreamble(handler); err != nil {
		t.Fatalf("ensureAnthropicTextPreamble() error = %v", err)
	}
	stream := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"hello <ST"}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{\"query\":\"<STOP> allowed\"}"}}]}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"content":"OP> hidden"}}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	parser := NewOpenAIStreamParserWithStops(handler, []string{"<STOP>"})
	if err := parser.Parse(strings.NewReader(stream)); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "hidden") {
		t.Fatalf("stream leaked text after stop: %s", body)
	}
	if !strings.Contains(body, `allowed`) || !strings.Contains(body, `query`) {
		t.Fatalf("tool arguments were not preserved: %s", body)
	}
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("stream missing end_turn completion after local stop: %s", body)
	}
}

func TestForwardOpenAICompatibleSSEWithStopsSplitMatch(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"upstream","choices":[{"index":0,"delta":{"content":"hello <ST"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"upstream","choices":[{"index":0,"delta":{"content":"OP> hidden"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"upstream","choices":[{"index":0,"delta":{"content":" more"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	recorder := httptest.NewRecorder()
	if err := forwardOpenAICompatibleSSEWithStops(context.Background(), recorder, strings.NewReader(upstream), "client-model", "OpenAI", []string{"<STOP>"}); err != nil {
		t.Fatalf("forwardOpenAICompatibleSSEWithStops() error = %v", err)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "<STOP>") || strings.Contains(body, "hidden") || strings.Contains(body, " more") {
		t.Fatalf("stream leaked stopped content: %s", body)
	}
	if !strings.Contains(body, "client-model") || strings.Contains(body, "upstream") {
		t.Fatalf("model rewrite failed: %s", body)
	}
	if strings.Count(body, `"finish_reason":"stop"`) != 1 || strings.Count(body, "data: [DONE]") != 1 {
		t.Fatalf("expected one synthetic stop and done: %s", body)
	}
}

func TestForwardOpenAICompatibleSSEWithStopsFlushesFinalChunkTail(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"upstream","choices":[{"index":0,"delta":{"content":"abcde"},"finish_reason":"stop"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	recorder := httptest.NewRecorder()
	if err := forwardOpenAICompatibleSSEWithStops(context.Background(), recorder, strings.NewReader(upstream), "client-model", "OpenAI", []string{"XYZ"}); err != nil {
		t.Fatalf("forwardOpenAICompatibleSSEWithStops() error = %v", err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"content":"abc"`) || !strings.Contains(body, `"content":"de"`) {
		t.Fatalf("stream dropped final pending stop tail: %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) || strings.Count(body, "data: [DONE]") != 1 {
		t.Fatalf("stream missing finish or done: %s", body)
	}
}

func TestOpenAIStreamWriterEmitsUsageAfterLocalTextStop(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-test", context.Background(), WithIncludeUsage(true), WithStopSequences([]string{"<STOP>"}))
	if err != nil {
		t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
	}
	if err := writer.WriteContent("hello <STOP> hidden"); err != nil {
		t.Fatalf("WriteContent() error = %v", err)
	}
	usage := &OpenAIUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}
	if err := writer.WriteFinish("length", usage); err != nil {
		t.Fatalf("WriteFinish() error = %v", err)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "hidden") || strings.Contains(body, "<STOP>") {
		t.Fatalf("stream leaked post-stop data: %s", body)
	}
	if strings.Count(body, `"finish_reason":"stop"`) != 1 {
		t.Fatalf("finish count/body = %s", body)
	}
	if !strings.Contains(body, `"prompt_tokens":3`) || strings.Count(body, "data: [DONE]") != 1 {
		t.Fatalf("stream missing usage or done: %s", body)
	}
}

func TestForwardOpenAICompatibleSSEWithStopsPreservesUnknownFields(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"upstream","provider_extension":"keep-top","choices":[{"index":0,"choice_extension":"keep-choice","delta":{"content":"hello <STOP>","delta_extension":"keep-delta"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	recorder := httptest.NewRecorder()
	if err := forwardOpenAICompatibleSSEWithStops(context.Background(), recorder, strings.NewReader(upstream), "client-model", "OpenAI", []string{"<STOP>"}); err != nil {
		t.Fatalf("forwardOpenAICompatibleSSEWithStops() error = %v", err)
	}
	body := recorder.Body.String()
	for _, want := range []string{"provider_extension", "keep-top", "choice_extension", "keep-choice", "delta_extension", "keep-delta"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream dropped %q: %s", want, body)
		}
	}
}

func TestOpenAIStreamWriterIgnoresToolDeltaAfterLocalTextStop(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-test", context.Background(), WithStopSequences([]string{"<STOP>"}))
	if err != nil {
		t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
	}
	if err := writer.WriteContent("hello <STOP> hidden"); err != nil {
		t.Fatalf("WriteContent() error = %v", err)
	}
	if err := writer.WriteToolCallDelta(0, &ToolCallDelta{Index: 0, ID: "call_1", Type: "function", Function: &FunctionCallDelta{Name: "late", Arguments: "{}"}}, nil, nil); err != nil {
		t.Fatalf("WriteToolCallDelta() error = %v", err)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "late") || strings.Contains(body, "call_1") || strings.Contains(body, "hidden") {
		t.Fatalf("stream leaked post-stop data: %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("stream missing forced stop and done: %s", body)
	}
}

func TestOpenAIStreamParserIgnoresToolDeltaAfterLocalTextStop(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "gpt-test", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}
	if err := ensureAnthropicTextPreamble(handler); err != nil {
		t.Fatalf("ensureAnthropicTextPreamble() error = %v", err)
	}
	stream := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"hello <STOP> hidden"}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_late","function":{"name":"late_tool","arguments":"{}"}}]}}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	parser := NewOpenAIStreamParserWithStops(handler, []string{"<STOP>"})
	if err := parser.Parse(strings.NewReader(stream)); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "hidden") || strings.Contains(body, "late_tool") || strings.Contains(body, "call_late") {
		t.Fatalf("stream leaked post-stop data: %s", body)
	}
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("stream missing end_turn completion after local stop: %s", body)
	}
}

func TestForwardOpenAICompatibleSSEWithStopsPerChoice(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"upstream","choices":[{"index":0,"delta":{"content":"choice0 "},"finish_reason":null},{"index":1,"delta":{"content":"choice1 <ST"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"upstream","choices":[{"index":0,"delta":{"content":"still visible"},"finish_reason":null},{"index":1,"delta":{"content":"OP> hidden"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	recorder := httptest.NewRecorder()
	if err := forwardOpenAICompatibleSSEWithStops(context.Background(), recorder, strings.NewReader(upstream), "client-model", "OpenAI", []string{"<STOP>"}); err != nil {
		t.Fatalf("forwardOpenAICompatibleSSEWithStops() error = %v", err)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "<STOP>") || strings.Contains(body, "hidden") {
		t.Fatalf("stream leaked stopped content: %s", body)
	}
	if !strings.Contains(body, `"index":1`) || !strings.Contains(body, `"finish_reason":"stop"`) || strings.Contains(body, `"index":0,"delta":{},"finish_reason":"stop"`) {
		t.Fatalf("stream missing single-choice forced stop: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("stream missing done marker: %s", body)
	}
}

func TestForwardOpenAICompatibleSSEWithStopsPreservesCustomToolCallDelta(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"upstream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_custom","type":"custom","custom":{"name":"apply_patch","input":""},"extension":"keep"}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"upstream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"custom","custom":{"input":"*** Begin Patch"}}]},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	recorder := httptest.NewRecorder()
	if err := forwardOpenAICompatibleSSEWithStops(context.Background(), recorder, strings.NewReader(upstream), "client-model", "OpenAI", []string{"<STOP>"}); err != nil {
		t.Fatalf("forwardOpenAICompatibleSSEWithStops() error = %v", err)
	}
	body := recorder.Body.String()
	for _, want := range []string{`"type":"custom"`, `"custom"`, `"name":"apply_patch"`, `"input":""`, `*** Begin Patch`, `"extension":"keep"`, "client-model"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q for custom tool delta: %s", want, body)
		}
	}
	if strings.Contains(body, "upstream") {
		t.Fatalf("model rewrite failed: %s", body)
	}
}

func TestForwardOpenAICompatibleSSEWithStopsDeletesStoppedChoiceTypedDeltaFields(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"upstream","choices":[{"index":1,"delta":{"content":"<STOP> hidden"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"upstream","choices":[{"index":0,"delta":{"content":"still visible"},"finish_reason":null},{"index":1,"delta":{"tool_calls":[{"index":0,"id":"call_late","type":"function","function":{"name":"late_tool","arguments":"{}"}}],"function_call":{"name":"legacy_late","arguments":"{}"},"refusal":"late refusal","audio":{"id":"late_audio"},"delta_extension":"keep-delta"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	recorder := httptest.NewRecorder()
	if err := forwardOpenAICompatibleSSEWithStops(context.Background(), recorder, strings.NewReader(upstream), "client-model", "OpenAI", []string{"<STOP>"}); err != nil {
		t.Fatalf("forwardOpenAICompatibleSSEWithStops() error = %v", err)
	}
	body := recorder.Body.String()
	for _, leaked := range []string{"late_tool", "legacy_late", "call_late", "late refusal", "late_audio", "hidden", "<STOP>"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("stream leaked stopped choice field %q: %s", leaked, body)
		}
	}
	for _, want := range []string{"still vi", "sible", "delta_extension", "keep-delta", "client-model", `"index":1`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q after stopped choice patch: %s", want, body)
		}
	}
	if strings.Contains(body, "upstream") {
		t.Fatalf("model rewrite failed: %s", body)
	}
}

func TestMessagesOpenAIStopSequencesEnforcedNonStream(t *testing.T) {
	var upstreamBody []byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		upstreamBody = append([]byte(nil), body...)
		return jsonRoundTripResponse(http.StatusOK, OpenAIResponse{
			ID:      "chatcmpl_stop",
			Object:  "chat.completion",
			Created: 1,
			Model:   "gpt-upstream",
			Choices: []OpenAIChoice{{Index: 0, Message: OpenAIMessage{Role: core.RoleAssistant, Content: "visible END hidden"}, FinishReason: "length"}},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "openai-key"},
		MaxRequestBodySize: fixtureMaxBodySize,
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := postMessageForStopTest(t, server, `{
	  "model": "claude-test",
	  "max_tokens": 16,
	  "stop_sequences": ["END"],
	  "messages": [{"role": "user", "content": "stop please"}]
	}`)
	assertAnthropicStopResponse(t, resp)
	if bytes.Contains(upstreamBody, []byte(`"stop"`)) {
		t.Fatalf("upstream OpenAI request still contained stop field: %s", string(upstreamBody))
	}
}

func TestMessagesArgoOpenAIStopSequencesEnforcedNonStream(t *testing.T) {
	var upstreamPath string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamPath = r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if bytes.Contains(body, []byte(`"stop"`)) {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "unexpected stop field"}), nil
		}
		return jsonRoundTripResponse(http.StatusOK, OpenAIResponse{
			ID:      "chatcmpl_stop",
			Object:  "chat.completion",
			Created: 1,
			Model:   "gpt-upstream",
			Choices: []OpenAIChoice{{Index: 0, Message: OpenAIMessage{Role: core.RoleAssistant, Content: "visible END hidden"}, FinishReason: "length"}},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local",
		ProviderKeySet:     ProviderKeySet{ArgoAPIKey: "argo-key"},
		ArgoUser:           "argo-user",
		MaxRequestBodySize: fixtureMaxBodySize,
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := postMessageForStopTest(t, server, `{
	  "model": "gpt-argo-test",
	  "max_tokens": 16,
	  "stop_sequences": ["END"],
	  "messages": [{"role": "user", "content": "stop please"}]
	}`)
	assertAnthropicStopResponse(t, resp)
	if upstreamPath != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want Argo OpenAI chat completions", upstreamPath)
	}
}

func postMessageForStopTest(t *testing.T, server http.Handler, body string) AnthropicResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp AnthropicResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response error = %v", err)
	}
	return resp
}

func assertAnthropicStopResponse(t *testing.T, resp AnthropicResponse) {
	t.Helper()
	if resp.StopReason != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn; response = %#v", resp.StopReason, resp)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != "visible " {
		t.Fatalf("unexpected content after stop enforcement: %#v", resp.Content)
	}
}

func TestOpenAIResponseStopEnforcement(t *testing.T) {
	resp := &OpenAIResponse{Choices: []OpenAIChoice{{Message: OpenAIMessage{Content: "hello END hidden"}, FinishReason: "length"}}}
	if !enforceOpenAIResponseStops(resp, []string{"END"}) {
		t.Fatal("expected stop match")
	}
	content, _ := resp.Choices[0].Message.Content.(string)
	if content != "hello " || resp.Choices[0].FinishReason != "stop" {
		encoded, _ := json.Marshal(resp)
		t.Fatalf("unexpected response after enforcement: %s", encoded)
	}
}
