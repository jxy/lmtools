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
		OpenAIAPIKey:       "test-key",
		Provider:           constants.ProviderOpenAI,
		Model:              "gpt-4",
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
		OpenAIAPIKey:       "test-key",
		Provider:           constants.ProviderOpenAI,
		Model:              "gpt-4",
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
		Model:              "gpt-5.4-nano",
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
		Model:              "gpt-5.4-nano",
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
				Model:              "gpt-5.4-nano",
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
		Model:              "gpt-4",
		SmallModel:         "gpt-3.5-turbo",
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
