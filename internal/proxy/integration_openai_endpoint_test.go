package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAIChatCompletionsEndpoint(t *testing.T) {
	// Create mock provider
	mockProvider := NewMockProvider(t, constants.ProviderOpenAI)

	// Set up expected response
	mockProvider.responses["/v1/chat/completions"] = &OpenAIResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 1234567890,
		Model:   "gpt-4",
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Message: OpenAIMessage{
					Role:    "assistant",
					Content: "Hello! How can I help you?",
				},
				FinishReason: "stop",
			},
		},
		Usage: &OpenAIUsage{
			PromptTokens:     10,
			CompletionTokens: 8,
			TotalTokens:      18,
		},
	}

	// Start mock server
	mockServer := httptest.NewServer(mockProvider)
	defer mockServer.Close()

	// Create proxy config with increased body size limit
	config := &Config{
		ProviderURL:        mockServer.URL + "/v1/chat/completions",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		Provider:           constants.ProviderOpenAI,
		MaxRequestBodySize: 100 * 1024 * 1024, // 100 MB to avoid body size issues
	}

	// Create proxy server (NewEndpoints is called internally)
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)

	// Create test request
	reqBody := OpenAIRequest{
		Model: "gpt-4",
		Messages: []OpenAIMessage{
			{
				Role:    "user",
				Content: "Hello!",
			},
		},
		MaxTokens: intPtr(100),
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// Record response
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Check response
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
		t.Logf("Response body: %s", w.Body.String())
	}

	// Parse response
	var resp OpenAIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Validate response
	if resp.Object != "chat.completion" {
		t.Errorf("Expected object 'chat.completion', got %s", resp.Object)
	}

	if len(resp.Choices) != 1 {
		t.Errorf("Expected 1 choice, got %d", len(resp.Choices))
	}

	if resp.Choices[0].Message.Content != "Hello! How can I help you?" {
		t.Errorf("Unexpected response content: %s", resp.Choices[0].Message.Content)
	}
}

func TestOpenAIChatCompletionsProviderURLAuthentication(t *testing.T) {
	tests := []struct {
		name     string
		apiKey   string
		wantAuth string
	}{
		{
			name:     "api key provided",
			apiKey:   "test-key",
			wantAuth: "Bearer test-key",
		},
		{
			name: "provider URL without api key remains allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuth string
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
				}
				gotAuth = r.Header.Get("Authorization")
				resp := OpenAIResponse{
					ID:      "chatcmpl-test",
					Object:  "chat.completion",
					Created: 1234567890,
					Model:   "gpt-4",
					Choices: []OpenAIChoice{
						{
							Index: 0,
							Message: OpenAIMessage{
								Role:    "assistant",
								Content: "ok",
							},
							FinishReason: "stop",
						},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(resp); err != nil {
					t.Fatalf("encode response: %v", err)
				}
			}))
			defer mockServer.Close()

			config := &Config{
				ProviderURL:        mockServer.URL + "/v1/chat/completions",
				ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: tt.apiKey},
				Provider:           constants.ProviderOpenAI,
				MaxRequestBodySize: 100 * 1024 * 1024,
			}
			server, cleanup := NewTestServer(t, config)
			t.Cleanup(cleanup)

			reqBody := OpenAIRequest{
				Model: "gpt-4",
				Messages: []OpenAIMessage{
					{
						Role:    "user",
						Content: "Hello!",
					},
				},
				MaxTokens: intPtr(100),
			}
			body, err := json.Marshal(reqBody)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if gotAuth != tt.wantAuth {
				t.Fatalf("Authorization = %q, want %q", gotAuth, tt.wantAuth)
			}
		})
	}
}

func TestOpenAIChatCompletionsWithTools(t *testing.T) {
	// Create mock provider
	mockProvider := NewMockProvider(t, constants.ProviderOpenAI)

	// Set up expected response with tool calls
	mockProvider.responses["/v1/chat/completions"] = &OpenAIResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 1234567890,
		Model:   "gpt-4",
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Message: OpenAIMessage{
					Role:    "assistant",
					Content: "I'll help you with that.",
					ToolCalls: []ToolCall{
						{
							ID:   "call_123",
							Type: "function",
							Function: FunctionCall{
								Name:      "get_weather",
								Arguments: `{"location": "New York"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
		Usage: &OpenAIUsage{
			PromptTokens:     15,
			CompletionTokens: 20,
			TotalTokens:      35,
		},
	}

	// Start mock server
	mockServer := httptest.NewServer(mockProvider)
	defer mockServer.Close()

	// Create proxy config with increased body size limit
	config := &Config{
		ProviderURL:        mockServer.URL + "/v1/chat/completions",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		Provider:           constants.ProviderOpenAI,
		MaxRequestBodySize: 100 * 1024 * 1024, // 100 MB to avoid body size issues
	}

	// Create proxy server (NewEndpoints is called internally)
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)

	// Create test request with tools
	reqBody := OpenAIRequest{
		Model: "gpt-4",
		Messages: []OpenAIMessage{
			{
				Role:    "user",
				Content: "What's the weather in New York?",
			},
		},
		Tools: []OpenAITool{
			{
				Type: "function",
				Function: OpenAIFunc{
					Name:        "get_weather",
					Description: "Get the weather for a location",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"location": map[string]interface{}{
								"type":        "string",
								"description": "The location to get weather for",
							},
						},
						"required": []string{"location"},
					},
				},
			},
		},
		MaxTokens: intPtr(100),
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// Record response
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Check response
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
		t.Logf("Response body: %s", w.Body.String())
	}

	// Parse response
	var resp OpenAIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Validate response
	if len(resp.Choices) != 1 {
		t.Errorf("Expected 1 choice, got %d", len(resp.Choices))
	}

	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Errorf("Expected 1 tool call, got %d", len(resp.Choices[0].Message.ToolCalls))
	}

	if resp.Choices[0].Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("Unexpected tool name: %s", resp.Choices[0].Message.ToolCalls[0].Function.Name)
	}
}

func TestArgoOpenAIDirectOmitsZeroMaxCompletionTokens(t *testing.T) {
	var captured map[string]interface{}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("Expected path /v1/chat/completions, got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("Failed to decode upstream request: %v", err)
		}

		resp := OpenAIResponse{
			ID:      "chatcmpl-test",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "gpt-5.4-nano",
			Choices: []OpenAIChoice{
				{
					Index: 0,
					Message: OpenAIMessage{
						Role:    "assistant",
						Content: "ok",
					},
					FinishReason: "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("Failed to encode response: %v", err)
		}
	}))
	defer mockServer.Close()

	config := &Config{
		ProviderURL:        mockServer.URL,
		Provider:           constants.ProviderArgo,
		ArgoUser:           "testuser",
		MaxRequestBodySize: 100 * 1024 * 1024,
	}

	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)

	zero := 0
	reqBody := OpenAIRequest{
		Model: "gpt-5.4-nano",
		Messages: []OpenAIMessage{
			{
				Role:    "user",
				Content: "Hello!",
			},
		},
		MaxCompletionTokens: &zero,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, ok := captured["max_completion_tokens"]; ok {
		t.Fatalf("max_completion_tokens should be omitted for zero value, got body: %v", captured)
	}
}

func TestOpenAIChatCompletionsRetries400WithIncreasedMaxCompletionTokens(t *testing.T) {
	var attempts []int
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			t.Fatalf("upstream path = %q, want /v1/chat/completions suffix", r.URL.Path)
		}
		var captured OpenAIRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		if captured.MaxCompletionTokens == nil {
			t.Fatalf("upstream max_completion_tokens missing on attempt %d", len(attempts)+1)
		}
		attempts = append(attempts, *captured.MaxCompletionTokens)
		if len(attempts) == 1 {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{
				"error": map[string]interface{}{"message": "max_completion_tokens too small"},
			}), nil
		}
		return jsonRoundTripResponse(http.StatusOK, OpenAIResponse{
			ID:      "chatcmpl-test",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   captured.Model,
			Choices: []OpenAIChoice{{
				Index: 0,
				Message: OpenAIMessage{
					Role:    "assistant",
					Content: "ok",
				},
				FinishReason: "stop",
			}},
		}), nil
	})
	server := NewTestServerDirectWithClient(t, &Config{
		ProviderURL:        "http://openai.local/v1/chat/completions",
		Provider:           constants.ProviderOpenAI,
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		MaxRequestBodySize: 100 * 1024 * 1024,
	}, retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport))

	logs := captureStderr(t, func() {
		code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
			"model":                 "gpt-5.4-nano",
			"messages":              []interface{}{map[string]interface{}{"role": "user", "content": "Hello!"}},
			"max_completion_tokens": 100,
		})
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", code, string(body))
		}
	})

	wantAttempts := []int{100, 356}
	if !intSlicesEqual(attempts, wantAttempts) {
		t.Fatalf("max_completion_tokens attempts = %v, want %v", attempts, wantAttempts)
	}
	for _, want := range []string{"[WARN]", "max_completion_tokens=100", "max_completion_tokens=356", "retry 1/3"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
}

func TestOpenAIChatCompletionsRetries400ReturnsFirst400AfterExhaustion(t *testing.T) {
	var attempts []int
	errorBodies := []string{
		`{"error":{"message":"first 400"}}`,
		`{"error":{"message":"second 400"}}`,
		`{"error":{"message":"third 400"}}`,
		`{"error":{"message":"fourth 400"}}`,
	}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var captured OpenAIRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		if captured.MaxCompletionTokens == nil {
			t.Fatalf("upstream max_completion_tokens missing on attempt %d", len(attempts)+1)
		}
		attempts = append(attempts, *captured.MaxCompletionTokens)
		body := errorBodies[len(attempts)-1]
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})
	server := NewTestServerDirectWithClient(t, &Config{
		ProviderURL:        "http://openai.local/v1/chat/completions",
		Provider:           constants.ProviderOpenAI,
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		MaxRequestBodySize: 100 * 1024 * 1024,
	}, retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport))

	code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
		"model":                 "gpt-5.4-nano",
		"messages":              []interface{}{map[string]interface{}{"role": "user", "content": "Hello!"}},
		"max_completion_tokens": 100,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", code, string(body))
	}
	if string(body) != errorBodies[0] {
		t.Fatalf("body = %s, want first 400 body %s", string(body), errorBodies[0])
	}
	wantAttempts := []int{100, 356, 612, 1124}
	if !intSlicesEqual(attempts, wantAttempts) {
		t.Fatalf("max_completion_tokens attempts = %v, want %v", attempts, wantAttempts)
	}
}

func TestOpenAIChatCompletionsDoesNotRetry400WithoutMaxCompletionTokens(t *testing.T) {
	attempts := 0
	firstBody := `{"error":{"message":"bad request"}}`
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(firstBody)),
		}, nil
	})
	server := NewTestServerDirectWithClient(t, &Config{
		ProviderURL:        "http://openai.local/v1/chat/completions",
		Provider:           constants.ProviderOpenAI,
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		MaxRequestBodySize: 100 * 1024 * 1024,
	}, retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport))

	code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
		"model":    "gpt-5.4-nano",
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "Hello!"}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", code, string(body))
	}
	if string(body) != firstBody {
		t.Fatalf("body = %s, want %s", string(body), firstBody)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestOpenAIChatCompletionsDoesNotRetry400WithOnlyMaxTokens(t *testing.T) {
	attempts := 0
	firstBody := `{"error":{"message":"bad request"}}`
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(firstBody)),
		}, nil
	})
	server := NewTestServerDirectWithClient(t, &Config{
		ProviderURL:        "http://openai.local/v1/chat/completions",
		Provider:           constants.ProviderOpenAI,
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		MaxRequestBodySize: 100 * 1024 * 1024,
	}, retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport))

	code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
		"model":      "gpt-5.4-nano",
		"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "Hello!"}},
		"max_tokens": 100,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", code, string(body))
	}
	if string(body) != firstBody {
		t.Fatalf("body = %s, want %s", string(body), firstBody)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestOpenAIChatCompletionsStreamingRetries400BeforeStreaming(t *testing.T) {
	var attempts []int
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("Accept = %q, want text/event-stream", got)
		}
		var captured OpenAIRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		if captured.MaxCompletionTokens == nil {
			t.Fatalf("upstream max_completion_tokens missing on attempt %d", len(attempts)+1)
		}
		attempts = append(attempts, *captured.MaxCompletionTokens)
		if len(attempts) == 1 {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{
				"error": map[string]interface{}{"message": "max_completion_tokens too small"},
			}), nil
		}
		streamBody := `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","model":"gpt-5.4-nano","choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\n" + "data: [DONE]\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(streamBody)),
		}, nil
	})
	server := NewTestServerDirectWithClient(t, &Config{
		ProviderURL:        "http://openai.local/v1/chat/completions",
		Provider:           constants.ProviderOpenAI,
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		MaxRequestBodySize: 100 * 1024 * 1024,
	}, retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport))

	code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
		"model":                 "gpt-5.4-nano",
		"stream":                true,
		"messages":              []interface{}{map[string]interface{}{"role": "user", "content": "Hello!"}},
		"max_completion_tokens": 100,
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", code, string(body))
	}
	if !strings.Contains(string(body), "data:") || !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("stream body = %s, want SSE data", string(body))
	}
	wantAttempts := []int{100, 356}
	if !intSlicesEqual(attempts, wantAttempts) {
		t.Fatalf("max_completion_tokens attempts = %v, want %v", attempts, wantAttempts)
	}
}

func intSlicesEqual(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestArgoModelMapRoutesOpenAIAliasToAnthropicEndpoint(t *testing.T) {
	var captured AnthropicRequest
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			t.Fatalf("upstream path = %q, want /v1/messages suffix", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg-test",
			Type:       "message",
			Role:       "assistant",
			Model:      captured.Model,
			Content:    []AnthropicContentBlock{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
			Usage:      &AnthropicUsage{InputTokens: 1, OutputTokens: 1},
		}), nil
	})

	server := NewTestServerDirectWithClient(t, &Config{
		ProviderURL:        "http://argo.local/v1",
		Provider:           constants.ProviderArgo,
		ArgoUser:           "testuser",
		ModelMapRules:      []ModelMapRule{{Pattern: "^gpt-public$", Model: "claude-opus-4-7"}},
		MaxRequestBodySize: 100 * 1024 * 1024,
	}, retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport))

	reqBody := OpenAIRequest{
		Model: "gpt-public",
		Messages: []OpenAIMessage{{
			Role:    "user",
			Content: "Hello!",
		}},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if captured.Model != "claude-opus-4-7" {
		t.Fatalf("upstream model = %q, want claude-opus-4-7", captured.Model)
	}
}

func TestArgoModelMapRoutesClaudeAliasToOpenAIEndpoint(t *testing.T) {
	var captured OpenAIRequest
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			t.Fatalf("upstream path = %q, want /v1/chat/completions suffix", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		return jsonRoundTripResponse(http.StatusOK, OpenAIResponse{
			ID:      "chatcmpl-test",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   captured.Model,
			Choices: []OpenAIChoice{{
				Index: 0,
				Message: OpenAIMessage{
					Role:    "assistant",
					Content: "ok",
				},
				FinishReason: "stop",
			}},
		}), nil
	})

	server := NewTestServerDirectWithClient(t, &Config{
		ProviderURL:        "http://argo.local/v1",
		Provider:           constants.ProviderArgo,
		ArgoUser:           "testuser",
		ModelMapRules:      []ModelMapRule{{Pattern: "^claude-public$", Model: "gpt-5.4-nano"}},
		MaxRequestBodySize: 100 * 1024 * 1024,
	}, retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport))

	reqBody := OpenAIRequest{
		Model: "claude-public",
		Messages: []OpenAIMessage{{
			Role:    "user",
			Content: "Hello!",
		}},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if captured.Model != "gpt-5.4-nano" {
		t.Fatalf("upstream model = %q, want gpt-5.4-nano", captured.Model)
	}
}

func TestAnthropicMessagesArgoOpenAIMappedChatCompletionsRetries400WithIncreasedMaxTokens(t *testing.T) {
	var attempts []int
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			t.Fatalf("upstream path = %q, want /v1/chat/completions suffix", r.URL.Path)
		}
		var captured OpenAIRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		if captured.Model != "gpt55" {
			t.Fatalf("upstream model = %q, want gpt55", captured.Model)
		}
		if captured.MaxTokens == nil {
			t.Fatalf("upstream max_tokens missing on attempt %d; request = %+v", len(attempts)+1, captured)
		}
		attempts = append(attempts, *captured.MaxTokens)
		if len(attempts) == 1 {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{
				"error": map[string]interface{}{"message": "Could not finish the message because max_tokens or model output limit was reached. Please try again with higher max_tokens."},
			}), nil
		}
		return jsonRoundTripResponse(http.StatusOK, OpenAIResponse{
			ID:      "chatcmpl-test",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   captured.Model,
			Choices: []OpenAIChoice{{
				Index: 0,
				Message: OpenAIMessage{
					Role:    "assistant",
					Content: "ok",
				},
				FinishReason: "stop",
			}},
			Usage: &OpenAIUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
		}), nil
	})
	server := NewTestServerDirectWithClient(t, &Config{
		ProviderURL:        "http://argo.local/v1",
		Provider:           constants.ProviderArgo,
		ArgoUser:           "testuser",
		ModelMapRules:      []ModelMapRule{{Pattern: `^claude-opus-4-8\[1m\]$`, Model: "gpt55"}},
		MaxRequestBodySize: 100 * 1024 * 1024,
	}, retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport))

	logs := captureStderr(t, func() {
		code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/messages", map[string]interface{}{
			"model":      "claude-opus-4-8[1m]",
			"max_tokens": 100,
			"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "Hello!"}},
		})
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", code, string(body))
		}
		if !strings.Contains(string(body), `"model":"claude-opus-4-8[1m]"`) {
			t.Fatalf("response body = %s, want original model", string(body))
		}
	})

	wantAttempts := []int{100, 356}
	if !intSlicesEqual(attempts, wantAttempts) {
		t.Fatalf("max_tokens attempts = %v, want %v", attempts, wantAttempts)
	}
	for _, want := range []string{"[WARN]", "Argo OpenAI chat/completions returned 400", "max_tokens=100", "max_tokens=356", "retry 1/3"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
}

func TestArgoOpenAIDirectConvertsDeveloperRoleToSystem(t *testing.T) {
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
			ID:      "chatcmpl-test",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "gpt-5.4-nano",
			Choices: []OpenAIChoice{{
				Index: 0,
				Message: OpenAIMessage{
					Role:    "assistant",
					Content: "ok",
				},
				FinishReason: "stop",
			}},
		}), nil
	})

	config := &Config{
		ProviderURL:        "http://argo.local/v1",
		Provider:           constants.ProviderArgo,
		ArgoUser:           "testuser",
		MaxRequestBodySize: 100 * 1024 * 1024,
	}

	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
		"model": "gpt-5.4-nano",
		"messages": []interface{}{
			map[string]interface{}{"role": "developer", "content": "Follow policy."},
			map[string]interface{}{"role": "user", "content": "Hello!"},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", code, string(body))
	}
	if len(captured.Messages) != 2 {
		t.Fatalf("captured messages = %+v, want 2 messages", captured.Messages)
	}
	if captured.Messages[0].Role != "system" {
		t.Fatalf("captured first role = %q, want system", captured.Messages[0].Role)
	}
	if captured.Messages[1].Role != "user" {
		t.Fatalf("captured second role = %q, want user", captured.Messages[1].Role)
	}
}

func TestArgoOpenAIDirectUsesArgoUserAuthFallback(t *testing.T) {
	for _, tt := range []struct {
		name   string
		stream bool
	}{
		{name: "non_streaming"},
		{name: "streaming", stream: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuth string
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("Expected path /v1/chat/completions, got %s", r.URL.Path)
				}
				gotAuth = r.Header.Get("Authorization")
				if tt.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte(`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","model":"gpt-5.4-nano","choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\n"))
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return
				}

				resp := OpenAIResponse{
					ID:      "chatcmpl-test",
					Object:  "chat.completion",
					Created: 1234567890,
					Model:   "gpt-5.4-nano",
					Choices: []OpenAIChoice{{
						Index: 0,
						Message: OpenAIMessage{
							Role:    "assistant",
							Content: "ok",
						},
						FinishReason: "stop",
					}},
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(resp); err != nil {
					t.Logf("Failed to encode response: %v", err)
				}
			}))
			defer mockServer.Close()

			config := &Config{
				ProviderURL:        mockServer.URL,
				Provider:           constants.ProviderArgo,
				ArgoUser:           "argo-user-key",
				MaxRequestBodySize: 100 * 1024 * 1024,
			}

			server, cleanup := NewTestServer(t, config)
			t.Cleanup(cleanup)

			reqBody := OpenAIRequest{
				Model:  "gpt-5.4-nano",
				Stream: tt.stream,
				Messages: []OpenAIMessage{{
					Role:    "user",
					Content: "Hello!",
				}},
			}
			body, err := json.Marshal(reqBody)
			if err != nil {
				t.Fatalf("Failed to marshal request: %v", err)
			}

			req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
			}
			if gotAuth != "Bearer argo-user-key" {
				t.Fatalf("Authorization = %q, want Bearer argo-user-key", gotAuth)
			}
		})
	}
}

func TestOpenAIChatArgoAnthropicDefaultsMaxTokens(t *testing.T) {
	for _, tt := range []struct {
		name      string
		model     string
		maxTokens *int
		want      int
	}{
		{name: "opus_default", model: "claudeopus46", want: defaultClaudeOpusMaxTokens},
		{name: "claude_default", model: "claude-3-haiku-20240307", want: defaultClaudeDefaultMaxTokens},
		{name: "explicit_preserved", model: "claudeopus46", maxTokens: intPtr(17), want: 17},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var captured AnthropicRequest
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/messages" {
					t.Errorf("path = %s, want /v1/messages", r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(AnthropicResponse{
					ID:         "msg-test",
					Type:       "message",
					Role:       "assistant",
					Model:      captured.Model,
					Content:    []AnthropicContentBlock{{Type: "text", Text: "ok"}},
					StopReason: "end_turn",
				})
			}))
			defer mockServer.Close()

			config := &Config{
				ProviderURL:        mockServer.URL,
				Provider:           constants.ProviderArgo,
				ArgoUser:           "argo-user-key",
				MaxRequestBodySize: 100 * 1024 * 1024,
			}
			server, cleanup := NewTestServer(t, config)
			t.Cleanup(cleanup)

			payload := map[string]interface{}{
				"model": tt.model,
				"messages": []interface{}{
					map[string]interface{}{"role": "user", "content": "Hello!"},
				},
			}
			if tt.maxTokens != nil {
				payload["max_tokens"] = *tt.maxTokens
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			server.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
			}
			if captured.MaxTokens != tt.want {
				t.Fatalf("max_tokens = %d, want %d", captured.MaxTokens, tt.want)
			}
		})
	}
}

func TestOpenAIChatArgoAnthropicErrorLogsSingleProviderWarning(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`))
	}))
	defer mockServer.Close()

	config := &Config{
		ProviderURL:        mockServer.URL,
		Provider:           constants.ProviderArgo,
		ArgoUser:           "argo-user-key",
		MaxRequestBodySize: 100 * 1024 * 1024,
	}
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)

	body := []byte(`{"model":"claudeopus46","messages":[{"role":"user","content":"Hello!"}]}`)
	logs := captureStderr(t, func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
		}
	})

	for _, want := range []string{
		"WIRE BACKEND REQUEST Argo Anthropic",
		"WIRE BACKEND RESPONSE BODY Argo Anthropic",
		"Provider argo client error (status 400):",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
	for _, unwanted := range []string{
		"Argo Anthropic request:",
		"Raw Argo Anthropic error response:",
		"Provider Argo Anthropic returned error:",
	} {
		if strings.Contains(logs, unwanted) {
			t.Fatalf("logs contain %q\nlogs:\n%s", unwanted, logs)
		}
	}
}

func TestModelsEndpoint(t *testing.T) {
	// Create mock server for models endpoint
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			// Return mock models response in OpenAI format
			response := map[string]interface{}{
				"object": "list",
				"data": []map[string]interface{}{
					{
						"id":       "gpt-4",
						"object":   "model",
						"created":  1234567890,
						"owned_by": "openai",
					},
					{
						"id":       "gpt-3.5-turbo",
						"object":   "model",
						"created":  1234567890,
						"owned_by": "openai",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	// Create proxy config with mock provider URL
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        mockServer.URL + "/v1",
		MaxRequestBodySize: 100 * 1024 * 1024, // 100 MB to avoid body size issues
	}

	// Create proxy server (NewEndpoints is called internally)
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)

	// Create test request
	req := httptest.NewRequest("GET", "/v1/models", nil)

	// Record response
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Check response
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
		t.Logf("Response body: %s", w.Body.String())
	}

	// Parse response
	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Validate response
	if resp.Object != "list" {
		t.Errorf("Expected object 'list', got %s", resp.Object)
	}

	if len(resp.Data) != 2 {
		t.Errorf("Expected 2 models, got %d", len(resp.Data))
	}

	// Check that both models are present
	models := make(map[string]bool)
	for _, model := range resp.Data {
		models[model.ID] = true
	}

	if !models["gpt-4"] {
		t.Error("Expected gpt-4 in models list")
	}

	if !models["gpt-3.5-turbo"] {
		t.Error("Expected gpt-3.5-turbo in models list")
	}
}

func TestOpenAIErrorHandling(t *testing.T) {
	// Create proxy config with increased body size limit
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		MaxRequestBodySize: 100 * 1024 * 1024, // 100 MB to avoid body size issues
	}

	// Create proxy server (NewEndpoints is called internally)
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)

	// Test invalid method
	req := httptest.NewRequest("GET", "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405, got %d", w.Code)
	}

	// Parse error response
	var errResp OpenAIError
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("Failed to parse error response: %v", err)
	}

	if errResp.Error.Type != "invalid_request_error" {
		t.Errorf("Expected error type 'invalid_request_error', got %s", errResp.Error.Type)
	}

	// Test empty messages
	reqBody := OpenAIRequest{
		Model:     "gpt-4",
		Messages:  []OpenAIMessage{},
		MaxTokens: intPtr(100),
	}

	body, _ := json.Marshal(reqBody)
	req = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}
