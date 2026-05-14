package proxy

import (
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

func TestOpenAIResponsesPromptOnlyPassesThroughToOpenAI(t *testing.T) {
	var sawPath string
	var captured map[string]interface{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawPath = r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal(upstream request) error = %v, body = %s", err, string(body))
		}
		return jsonRoundTripResponse(http.StatusOK, OpenAIResponsesResponse{
			ID:     "resp_upstream",
			Object: "response",
			Status: "completed",
			Model:  "gpt-test",
			Output: []OpenAIResponsesOutputItem{},
			Usage:  &OpenAIResponsesUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		OpenAIAPIKey:       "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := requestJSON(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model": "gpt-test",
		"prompt": map[string]interface{}{
			"id": "pmpt_123",
			"variables": map[string]interface{}{
				"topic": "tools",
			},
		},
		"tools": []interface{}{
			map[string]interface{}{"type": "web_search_preview"},
			map[string]interface{}{
				"type":             "file_search",
				"vector_store_ids": []interface{}{"vs_1"},
			},
		},
	})
	if resp["id"] != "resp_upstream" {
		t.Fatalf("response id = %#v, want resp_upstream", resp["id"])
	}
	if sawPath != "/v1/responses" {
		t.Fatalf("upstream path = %q, want /v1/responses", sawPath)
	}
	if _, ok := captured["input"]; ok {
		t.Fatalf("upstream request unexpectedly included input: %#v", captured["input"])
	}
	prompt, ok := captured["prompt"].(map[string]interface{})
	if !ok || prompt["id"] != "pmpt_123" {
		t.Fatalf("upstream prompt = %#v, want pmpt_123", captured["prompt"])
	}
	tools, ok := captured["tools"].([]interface{})
	if !ok || len(tools) != 2 {
		t.Fatalf("upstream tools = %#v, want 2 tools", captured["tools"])
	}
	firstTool, _ := tools[0].(map[string]interface{})
	secondTool, _ := tools[1].(map[string]interface{})
	if firstTool["type"] != "web_search_preview" || secondTool["type"] != "file_search" {
		t.Fatalf("upstream tool types = %#v", captured["tools"])
	}
}

func TestOpenAIResponsesDirectPassThroughPreservesCustomTools(t *testing.T) {
	var captured map[string]interface{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal(upstream request) error = %v, body = %s", err, string(body))
		}
		return jsonRoundTripResponse(http.StatusOK, OpenAIResponsesResponse{
			ID:     "resp_upstream",
			Object: "response",
			Status: "completed",
			Model:  "gpt-test",
			Output: []OpenAIResponsesOutputItem{},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		OpenAIAPIKey:       "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	_ = requestJSON(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model": "gpt-test",
		"input": "patch",
		"tools": []interface{}{map[string]interface{}{
			"type":        "custom",
			"name":        "apply_patch",
			"description": "Apply a patch.",
			"format": map[string]interface{}{
				"type":       "grammar",
				"syntax":     "lark",
				"definition": "start: /.+/",
			},
		}},
		"tool_choice": map[string]interface{}{"type": "custom", "name": "apply_patch"},
	})

	tools, _ := captured["tools"].([]interface{})
	if len(tools) != 1 {
		t.Fatalf("upstream tools = %#v, want custom tool", captured["tools"])
	}
	tool, _ := tools[0].(map[string]interface{})
	if tool["type"] != "custom" || tool["name"] != "apply_patch" {
		t.Fatalf("upstream custom tool = %#v", tool)
	}
	choice, _ := captured["tool_choice"].(map[string]interface{})
	if choice["type"] != "custom" || choice["name"] != "apply_patch" {
		t.Fatalf("upstream tool_choice = %#v", captured["tool_choice"])
	}
}

func TestOpenAIResponsesDirectPassThroughPreservesUnmodeledFields(t *testing.T) {
	var captured map[string]interface{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal(upstream request) error = %v, body = %s", err, string(body))
		}
		return jsonRoundTripResponse(http.StatusOK, OpenAIResponsesResponse{
			ID:     "resp_upstream",
			Object: "response",
			Status: "completed",
			Model:  "gpt-test",
			Output: []OpenAIResponsesOutputItem{},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		OpenAIAPIKey:       "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := requestJSON(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model":                  "gpt-test",
		"input":                  "say hi",
		"frequency_penalty":      0.25,
		"presence_penalty":       0.5,
		"prompt_cache_retention": "24h",
		"future_option": map[string]interface{}{
			"enabled": true,
		},
	})
	if resp["id"] != "resp_upstream" {
		t.Fatalf("response id = %#v, want resp_upstream", resp["id"])
	}
	assertCapturedResponsesPassthroughFields(t, captured)
}

func TestOpenAIResponsesDirectStreamPassThroughPreservesUnmodeledFields(t *testing.T) {
	var captured map[string]interface{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("Accept header = %q, want text/event-stream", r.Header.Get("Accept"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal(upstream request) error = %v, body = %s", err, string(body))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(strings.Join([]string{
				`event: response.completed`,
				`data: {"type":"response.completed","response":{"id":"resp_upstream","object":"response","status":"completed","model":"gpt-test","output":[]}}`,
				``,
			}, "\n"))),
		}, nil
	})
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		OpenAIAPIKey:       "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model":                  "gpt-test",
		"input":                  "say hi",
		"stream":                 true,
		"frequency_penalty":      0.25,
		"presence_penalty":       0.5,
		"prompt_cache_retention": "24h",
		"future_option": map[string]interface{}{
			"enabled": true,
		},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", code, string(body))
	}
	if !strings.Contains(string(body), "response.completed") {
		t.Fatalf("stream response missing completion event: %s", string(body))
	}
	assertCapturedResponsesPassthroughFields(t, captured)
}

func TestOpenAIResponsesDirectPassThroughRewritesOnlyMappedModel(t *testing.T) {
	var captured map[string]interface{}
	var lifecyclePath string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp_upstream" {
			lifecyclePath = r.URL.Path
			return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{
				"id":      "resp_upstream",
				"object":  "response",
				"status":  "completed",
				"model":   "gpt-upstream",
				"output":  []interface{}{},
				"unknown": "from-lifecycle",
			}), nil
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal(upstream request) error = %v, body = %s", err, string(body))
		}
		return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{
			"id":      "resp_upstream",
			"object":  "response",
			"status":  "completed",
			"model":   "gpt-upstream",
			"output":  []interface{}{},
			"unknown": "from-upstream",
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		OpenAIAPIKey:       "test-key",
		ModelMapRules:      []ModelMapRule{{Pattern: "^claude-3-sonnet$", Model: "gpt-upstream"}},
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := requestJSON(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model":                  "claude-3-sonnet",
		"input":                  "say hi",
		"frequency_penalty":      0.25,
		"presence_penalty":       0.5,
		"prompt_cache_retention": "24h",
		"future_option": map[string]interface{}{
			"enabled": true,
		},
	})
	if captured["model"] != "gpt-upstream" {
		t.Fatalf("upstream model = %#v, want gpt-upstream", captured["model"])
	}
	assertCapturedResponsesPassthroughFields(t, captured)
	if resp["model"] != "claude-3-sonnet" {
		t.Fatalf("downstream response model = %#v, want original model", resp["model"])
	}
	if resp["unknown"] != "from-upstream" {
		t.Fatalf("downstream response dropped upstream field: %#v", resp)
	}

	retrieved := requestJSON(t, server, http.MethodGet, "/v1/responses/resp_upstream", nil)
	if lifecyclePath != "/v1/responses/resp_upstream" {
		t.Fatalf("lifecycle path = %q, want /v1/responses/resp_upstream", lifecyclePath)
	}
	if retrieved["id"] != "resp_upstream" {
		t.Fatalf("retrieved response id = %#v, want resp_upstream", retrieved["id"])
	}
	if retrieved["model"] != "claude-3-sonnet" {
		t.Fatalf("retrieved response model = %#v, want original model", retrieved["model"])
	}
	if retrieved["unknown"] != "from-lifecycle" {
		t.Fatalf("retrieved response dropped lifecycle field: %#v", retrieved)
	}
}

func TestOpenAIResponsesLifecycleModelAliasesAreResponseIDScoped(t *testing.T) {
	server := &Server{}
	server.registerResponsesModelAlias("resp_one", "gpt-upstream", "claude-3-sonnet")
	server.registerResponsesModelAlias("resp_two", "gpt-upstream", "claude-3-opus")

	first := server.rewriteResponsesLifecycleBodyModel([]byte(`{"id":"resp_one","model":"gpt-upstream"}`), "")
	second := server.rewriteResponsesLifecycleBodyModel([]byte(`{"id":"resp_two","model":"gpt-upstream"}`), "")
	fallback := server.rewriteResponsesLifecycleBodyModel([]byte(`{"model":"gpt-upstream"}`), "resp_two")

	for name, tc := range map[string]struct {
		body []byte
		want string
	}{
		"first":    {body: first, want: "claude-3-sonnet"},
		"second":   {body: second, want: "claude-3-opus"},
		"fallback": {body: fallback, want: "claude-3-opus"},
	} {
		var decoded map[string]interface{}
		if err := json.Unmarshal(tc.body, &decoded); err != nil {
			t.Fatalf("%s response is invalid JSON: %v; body = %s", name, err, string(tc.body))
		}
		if decoded["model"] != tc.want {
			t.Fatalf("%s model = %#v, want %s; body = %s", name, decoded["model"], tc.want, string(tc.body))
		}
	}
}

func TestOpenAIResponsesDirectStreamRegistersModelAliasByResponseID(t *testing.T) {
	server := &Server{}
	line := server.rewriteResponsesStreamModel(
		`data: {"type":"response.created","response":{"id":"resp_stream","model":"gpt-upstream"}}`,
		"gpt-upstream",
		"claude-3-sonnet",
	)
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("stream line = %q, want data line", line)
	}

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
		t.Fatalf("stream line is invalid JSON: %v; line = %s", err, line)
	}
	response, ok := event["response"].(map[string]interface{})
	if !ok {
		t.Fatalf("event response = %#v", event["response"])
	}
	if response["model"] != "claude-3-sonnet" {
		t.Fatalf("stream response model = %#v, want claude-3-sonnet", response["model"])
	}

	retrieved := server.rewriteResponsesLifecycleBodyModel([]byte(`{"id":"resp_stream","model":"gpt-upstream"}`), "")
	var decoded map[string]interface{}
	if err := json.Unmarshal(retrieved, &decoded); err != nil {
		t.Fatalf("retrieved response is invalid JSON: %v; body = %s", err, string(retrieved))
	}
	if decoded["model"] != "claude-3-sonnet" {
		t.Fatalf("retrieved model = %#v, want claude-3-sonnet", decoded["model"])
	}
}

func TestOpenAIResponsesArgoLegacyNonClaudeUsesLegacyChat(t *testing.T) {
	var captured ArgoChatRequest
	var sawPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		if !strings.HasSuffix(r.URL.Path, "/api/v1/resource/chat/") {
			t.Fatalf("upstream path = %q, want legacy chat endpoint", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode legacy Argo request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(ArgoChatResponse{Response: "legacy ok"})
	}))
	defer backend.Close()

	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        backend.URL,
		ArgoUser:           "test-key",
		ArgoLegacy:         true,
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	})
	defer cleanup()

	code, body := requestJSONStatus(t, handler, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model": "gpt-5",
		"input": "say hi",
		"store": false,
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", code, string(body))
	}
	if captured.Model != "gpt-5" {
		t.Fatalf("legacy request model = %q, want gpt-5", captured.Model)
	}
	if captured.User != "test-key" {
		t.Fatalf("legacy request user = %q, want test-key", captured.User)
	}
	if strings.HasSuffix(sawPath, "/v1/chat/completions") {
		t.Fatalf("legacy request incorrectly used native OpenAI endpoint: %s", sawPath)
	}

	var decoded OpenAIResponsesResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, string(body))
	}
	if decoded.Status != "completed" || decoded.Model != "gpt-5" {
		t.Fatalf("response status/model = %q/%q, want completed/gpt-5", decoded.Status, decoded.Model)
	}
}

func TestOpenAIResponsesAnthropicPreservesStrictFunctionTool(t *testing.T) {
	var captured AnthropicRequest
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			t.Fatalf("upstream path = %q, want /v1/messages suffix", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal(upstream request) error = %v, body = %s", err, string(body))
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_upstream",
			Type:       "message",
			Role:       core.RoleAssistant,
			Model:      "claude-opus-4-7",
			Content:    []AnthropicContentBlock{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
			Usage:      &AnthropicUsage{InputTokens: 1, OutputTokens: 1},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local/v1",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model": "claude-opus-4-7",
		"input": "use a tool",
		"store": false,
		"tools": []interface{}{map[string]interface{}{
			"type":        "function",
			"name":        "lookup",
			"description": "Look up a value.",
			"strict":      true,
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{"type": "string"},
				},
				"required":             []interface{}{"query"},
				"additionalProperties": false,
			},
		}},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", code, string(body))
	}
	if len(captured.Tools) != 1 {
		t.Fatalf("captured tools len = %d, want 1", len(captured.Tools))
	}
	if captured.Tools[0].Strict == nil || *captured.Tools[0].Strict != true {
		t.Fatalf("captured Anthropic tool strict = %#v, want true", captured.Tools[0].Strict)
	}
}

func TestOpenAIResponsesArgoOpenAIConvertsInstructionRolesToSystem(t *testing.T) {
	var captured OpenAIRequest
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			t.Fatalf("upstream path = %q, want /v1/chat/completions suffix", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal(upstream request) error = %v, body = %s", err, string(body))
		}
		return jsonRoundTripResponse(http.StatusOK, OpenAIResponse{
			ID:      "chatcmpl-upstream",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "gpt-5",
			Choices: []OpenAIChoice{{
				Index: 0,
				Message: OpenAIMessage{
					Role:    core.RoleAssistant,
					Content: "ok",
				},
				FinishReason: "stop",
			}},
			Usage: &OpenAIUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local/v1",
		ArgoUser:           "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model":        "gpt-5",
		"instructions": "top-level instructions",
		"input": []interface{}{
			map[string]interface{}{
				"type":    "message",
				"role":    "developer",
				"content": "inline developer",
			},
			map[string]interface{}{
				"type":    "message",
				"role":    "user",
				"content": "hello",
			},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", code, string(body))
	}
	if len(captured.Messages) != 3 {
		t.Fatalf("captured messages = %+v, want 3 messages", captured.Messages)
	}
	wantRoles := []core.Role{core.RoleSystem, core.RoleSystem, core.RoleUser}
	for i, want := range wantRoles {
		if captured.Messages[i].Role != want {
			t.Fatalf("captured messages[%d].role = %q, want %q; messages = %+v", i, captured.Messages[i].Role, want, captured.Messages)
		}
	}
	if captured.Messages[0].Content != "top-level instructions" || captured.Messages[1].Content != "inline developer" {
		t.Fatalf("captured instruction contents = %#v, %#v", captured.Messages[0].Content, captured.Messages[1].Content)
	}
}

func TestOpenAIResponsesArgoOpenAIAdaptsCustomToolsToFunctions(t *testing.T) {
	var captured OpenAIRequest
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal(upstream request) error = %v, body = %s", err, string(body))
		}
		return jsonRoundTripResponse(http.StatusOK, OpenAIResponse{
			ID:      "chatcmpl-upstream",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "gpt-5",
			Choices: []OpenAIChoice{{
				Index: 0,
				Message: OpenAIMessage{
					Role: core.RoleAssistant,
					ToolCalls: []ToolCall{{
						ID:   "call_1",
						Type: "function",
						Function: FunctionCall{
							Name:      "apply_patch",
							Arguments: `{"input":"*** Begin Patch\n*** End Patch\n"}`,
						},
					}},
				},
				FinishReason: "tool_calls",
			}},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local/v1",
		ArgoUser:           "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model": "gpt-5",
		"input": "patch",
		"tools": []interface{}{map[string]interface{}{
			"type":        "custom",
			"name":        "apply_patch",
			"description": "Apply a patch.",
			"format": map[string]interface{}{
				"type":       "grammar",
				"syntax":     "lark",
				"definition": "start: /.+/",
			},
		}},
		"tool_choice": map[string]interface{}{"type": "custom", "name": "apply_patch"},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", code, string(body))
	}
	if len(captured.Tools) != 1 || captured.Tools[0].Type != "function" || captured.Tools[0].Function.Name != "apply_patch" {
		t.Fatalf("captured tools = %#v, want function apply_patch", captured.Tools)
	}
	parameters, _ := captured.Tools[0].Function.Parameters.(map[string]interface{})
	properties, _ := parameters["properties"].(map[string]interface{})
	if properties[core.CustomToolInputField] == nil {
		t.Fatalf("captured function parameters = %#v", captured.Tools[0].Function.Parameters)
	}
	choice, _ := captured.ToolChoice.(map[string]interface{})
	function, _ := choice["function"].(map[string]interface{})
	if choice["type"] != "function" || function["name"] != "apply_patch" {
		t.Fatalf("captured tool_choice = %#v", captured.ToolChoice)
	}

	var resp OpenAIResponsesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v, body = %s", err, string(body))
	}
	if len(resp.Output) != 1 || resp.Output[0].Type != "custom_tool_call" || resp.Output[0].Input != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("responses output = %+v", resp.Output)
	}
}

func assertCapturedResponsesPassthroughFields(t *testing.T, captured map[string]interface{}) {
	t.Helper()
	if captured["frequency_penalty"] != 0.25 {
		t.Fatalf("frequency_penalty = %#v, want 0.25; captured = %#v", captured["frequency_penalty"], captured)
	}
	if captured["presence_penalty"] != 0.5 {
		t.Fatalf("presence_penalty = %#v, want 0.5; captured = %#v", captured["presence_penalty"], captured)
	}
	if captured["prompt_cache_retention"] != "24h" {
		t.Fatalf("prompt_cache_retention = %#v, want 24h; captured = %#v", captured["prompt_cache_retention"], captured)
	}
	future, ok := captured["future_option"].(map[string]interface{})
	if !ok || future["enabled"] != true {
		t.Fatalf("future_option = %#v, want enabled=true; captured = %#v", captured["future_option"], captured)
	}
}

func TestOpenAIResponsesConvertedProviderDropsUnsupportedTools(t *testing.T) {
	upstreamCalls := 0
	var upstreamBody map[string]interface{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamCalls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &upstreamBody); err != nil {
			t.Fatalf("json.Unmarshal(upstream request) error = %v, body = %s", err, string(body))
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_1",
			Type:       "message",
			Role:       core.RoleAssistant,
			Model:      "claude-test",
			StopReason: "end_turn",
			Content: []AnthropicContentBlock{{
				Type: "text",
				Text: "search skipped",
			}},
			Usage: &AnthropicUsage{InputTokens: 1, OutputTokens: 2},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model": "claude-test",
		"input": "search",
		"tools": []interface{}{
			map[string]interface{}{"type": "web_search_preview"},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", code, string(body))
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}
	if _, ok := upstreamBody["tools"]; ok {
		t.Fatalf("upstream tools = %#v, want unsupported Responses tool dropped", upstreamBody["tools"])
	}
	var resp OpenAIResponsesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v, body = %s", err, string(body))
	}
	if resp.Status != "completed" || len(resp.Output) != 1 {
		t.Fatalf("response = %+v, want completed response with one message", resp)
	}
}

func TestOpenAIResponsesConvertedProviderWrapsCustomToolsForAnthropic(t *testing.T) {
	var upstreamBody map[string]interface{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &upstreamBody); err != nil {
			t.Fatalf("json.Unmarshal(upstream request) error = %v, body = %s", err, string(body))
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_1",
			Type:       "message",
			Role:       core.RoleAssistant,
			Model:      "claude-test",
			StopReason: "tool_use",
			Content: []AnthropicContentBlock{{
				Type:  "tool_use",
				ID:    "toolu_1",
				Name:  "apply_patch",
				Input: map[string]interface{}{core.CustomToolInputField: "*** Begin Patch\n*** End Patch\n"},
			}},
			Usage: &AnthropicUsage{InputTokens: 1, OutputTokens: 2},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model": "claude-test",
		"input": "patch",
		"tools": []interface{}{
			map[string]interface{}{
				"type":        "custom",
				"name":        "apply_patch",
				"description": "Apply a patch.",
				"format": map[string]interface{}{
					"type":       "grammar",
					"syntax":     "lark",
					"definition": "start: /.+/",
				},
			},
		},
		"tool_choice": map[string]interface{}{"type": "custom", "name": "apply_patch"},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", code, string(body))
	}

	tools, _ := upstreamBody["tools"].([]interface{})
	if len(tools) != 1 {
		t.Fatalf("upstream tools = %#v, want one wrapped custom tool", upstreamBody["tools"])
	}
	tool, _ := tools[0].(map[string]interface{})
	schema, _ := tool["input_schema"].(map[string]interface{})
	properties, _ := schema["properties"].(map[string]interface{})
	if tool["name"] != "apply_patch" || properties[core.CustomToolInputField] == nil {
		t.Fatalf("upstream custom tool = %#v", tool)
	}
	choice, _ := upstreamBody["tool_choice"].(map[string]interface{})
	if choice["type"] != "tool" || choice["name"] != "apply_patch" {
		t.Fatalf("upstream tool_choice = %#v", upstreamBody["tool_choice"])
	}

	var resp OpenAIResponsesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v, body = %s", err, string(body))
	}
	if len(resp.Output) != 1 || resp.Output[0].Type != "custom_tool_call" || resp.Output[0].Input != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("responses output = %+v", resp.Output)
	}
}

func TestOpenAIResponsesConvertedProviderWrapsCustomToolsForGoogle(t *testing.T) {
	var upstreamBody map[string]interface{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &upstreamBody); err != nil {
			t.Fatalf("json.Unmarshal(upstream request) error = %v, body = %s", err, string(body))
		}
		return jsonRoundTripResponse(http.StatusOK, GoogleResponse{
			Candidates: []GoogleCandidate{{
				Content: GoogleContent{
					Role: "model",
					Parts: []GooglePart{{
						FunctionCall: &GoogleFunctionCall{
							Name: "apply_patch",
							Args: map[string]interface{}{core.CustomToolInputField: "*** Begin Patch\n*** End Patch\n"},
						},
					}},
				},
				FinishReason: "STOP",
			}},
			UsageMetadata: &GoogleUsage{PromptTokenCount: 1, CandidatesTokenCount: 2, TotalTokenCount: 3},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderGoogle,
		ProviderURL:        "http://google.local/v1beta",
		GoogleAPIKey:       "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model": "gemini-test",
		"input": "patch",
		"tools": []interface{}{map[string]interface{}{
			"type":        "custom",
			"name":        "apply_patch",
			"description": "Apply a patch.",
			"format": map[string]interface{}{
				"type":       "grammar",
				"syntax":     "lark",
				"definition": "start: /.+/",
			},
		}},
		"tool_choice": map[string]interface{}{"type": "custom", "name": "apply_patch"},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", code, string(body))
	}

	tools, _ := upstreamBody["tools"].([]interface{})
	if len(tools) != 1 {
		t.Fatalf("upstream tools = %#v, want one Google function tool", upstreamBody["tools"])
	}
	tool, _ := tools[0].(map[string]interface{})
	declarations, _ := tool["functionDeclarations"].([]interface{})
	if len(declarations) != 1 {
		t.Fatalf("upstream declarations = %#v, want one declaration", tool["functionDeclarations"])
	}
	decl, _ := declarations[0].(map[string]interface{})
	parameters, _ := decl["parameters"].(map[string]interface{})
	properties, _ := parameters["properties"].(map[string]interface{})
	if decl["name"] != "apply_patch" || properties[core.CustomToolInputField] == nil {
		t.Fatalf("upstream declaration = %#v", decl)
	}
	toolConfig, _ := upstreamBody["toolConfig"].(map[string]interface{})
	callingConfig, _ := toolConfig["functionCallingConfig"].(map[string]interface{})
	allowed, _ := callingConfig["allowedFunctionNames"].([]interface{})
	if callingConfig["mode"] != "ANY" || len(allowed) != 1 || allowed[0] != "apply_patch" {
		t.Fatalf("upstream toolConfig = %#v", upstreamBody["toolConfig"])
	}

	var resp OpenAIResponsesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v, body = %s", err, string(body))
	}
	if len(resp.Output) != 1 || resp.Output[0].Type != "custom_tool_call" || resp.Output[0].Input != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("responses output = %+v", resp.Output)
	}
}
