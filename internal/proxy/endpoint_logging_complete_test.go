package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMessagesEndpointLogging tests logging for the /v1/messages endpoint
func TestMessagesEndpointLogging(t *testing.T) {
	// Reset and initialize logger with INFO level
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Create mock Anthropic server
	mockAnthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a simple response
		resp := AnthropicResponse{
			ID:   "msg_test",
			Type: "message",
			Role: core.RoleAssistant,
			Content: []AnthropicContentBlock{
				{Type: "text", Text: "Test response"},
			},
			StopReason: "end_turn",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("Failed to encode response: %v", err)
		}
	}))
	defer mockAnthropic.Close()

	// Create server config
	config := &Config{
		Provider:           "anthropic",
		AnthropicAPIKey:    "test-key",
		AnthropicURL:       mockAnthropic.URL,
		MaxRequestBodySize: 10 * 1024 * 1024,
	}

	// Create server
	server := NewServer(config)
	testServer := httptest.NewServer(server)
	defer testServer.Close()

	t.Run("POST request", func(t *testing.T) {
		// Create request
		anthReq := AnthropicRequest{
			Model: "claude-3-opus-20240229",
			Messages: []AnthropicMessage{
				{
					Role:    core.RoleUser,
					Content: json.RawMessage(`"Hello"`),
				},
			},
			MaxTokens: 100,
		}

		reqBody, _ := json.Marshal(anthReq)

		// Capture logs
		logs := captureStderr(t, func() {
			resp, err := http.Post(
				testServer.URL+"/v1/messages",
				"application/json",
				bytes.NewReader(reqBody),
			)
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("Expected status 200, got %d: %s", resp.StatusCode, string(body))
			}
		})

		// Verify INFO level log
		if !strings.Contains(logs, "[INFO]") {
			t.Error("Expected INFO level log for messages endpoint")
			t.Logf("Captured logs: %s", logs)
		}

		// Verify log message
		expectedMsg := "POST /v1/messages | Anthropic messages endpoint"
		if !strings.Contains(logs, expectedMsg) {
			t.Errorf("Expected '%s' in logs, got: %s", expectedMsg, logs)
		}

		// Verify request ID is present
		if !strings.Contains(logs, "[#") {
			t.Error("Expected request ID in logs")
		}
	})

	t.Run("Invalid method", func(t *testing.T) {
		// Capture logs
		logs := captureStderr(t, func() {
			resp, err := http.Get(testServer.URL + "/v1/messages")
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			// Should return error for GET method
			if resp.StatusCode == http.StatusOK {
				t.Error("Expected error for GET method, got 200")
			}
		})

		// Should still log the endpoint access
		expectedMsg := "GET /v1/messages | Anthropic messages endpoint"
		if !strings.Contains(logs, expectedMsg) {
			t.Errorf("Expected '%s' in logs even for invalid method", expectedMsg)
		}
	})
}

// TestChatCompletionsEndpointLogging tests logging for the /v1/chat/completions endpoint
func TestChatCompletionsEndpointLogging(t *testing.T) {
	// Reset and initialize logger with INFO level
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Create mock OpenAI server
	mockOpenAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a simple response
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
						Content: "Test response",
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
	defer mockOpenAI.Close()

	// Create server config
	config := &Config{
		Provider:           "openai",
		OpenAIAPIKey:       "test-key",
		OpenAIURL:          mockOpenAI.URL,
		MaxRequestBodySize: 10 * 1024 * 1024,
	}

	// Create server
	server := NewServer(config)
	testServer := httptest.NewServer(server)
	defer testServer.Close()

	t.Run("POST request", func(t *testing.T) {
		// Create request
		openAIReq := OpenAIRequest{
			Model: "gpt-4",
			Messages: []OpenAIMessage{
				{
					Role:    "user",
					Content: "Hello",
				},
			},
		}

		reqBody, _ := json.Marshal(openAIReq)

		// Capture logs
		logs := captureStderr(t, func() {
			resp, err := http.Post(
				testServer.URL+"/v1/chat/completions",
				"application/json",
				bytes.NewReader(reqBody),
			)
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("Expected status 200, got %d: %s", resp.StatusCode, string(body))
			}
		})

		// Verify INFO level log
		if !strings.Contains(logs, "[INFO]") {
			t.Error("Expected INFO level log for chat completions endpoint")
			t.Logf("Captured logs: %s", logs)
		}

		// Verify log message
		expectedMsg := "POST /v1/chat/completions | OpenAI chat completions endpoint"
		if !strings.Contains(logs, expectedMsg) {
			t.Errorf("Expected '%s' in logs, got: %s", expectedMsg, logs)
		}

		// Verify request ID is present
		if !strings.Contains(logs, "[#") {
			t.Error("Expected request ID in logs")
		}
	})

	t.Run("Invalid method", func(t *testing.T) {
		// Capture logs
		logs := captureStderr(t, func() {
			req, _ := http.NewRequest("PUT", testServer.URL+"/v1/chat/completions", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			// Should return error for PUT method
			if resp.StatusCode == http.StatusOK {
				t.Error("Expected error for PUT method, got 200")
			}
		})

		// Should still log the endpoint access
		expectedMsg := "PUT /v1/chat/completions | OpenAI chat completions endpoint"
		if !strings.Contains(logs, expectedMsg) {
			t.Errorf("Expected '%s' in logs even for invalid method", expectedMsg)
		}
	})
}

// TestModelsEndpointLogging tests logging for the /v1/models endpoint
func TestModelsEndpointLogging(t *testing.T) {
	// Reset and initialize logger with INFO level
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Create mock models server
	mockModels := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a simple models list
		resp := map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{
					"id":       "gpt-4",
					"object":   "model",
					"created":  1234567890,
					"owned_by": "openai",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("Failed to encode response: %v", err)
		}
	}))
	defer mockModels.Close()

	// Create server config
	config := &Config{
		Provider:           "openai",
		OpenAIAPIKey:       "test-key",
		ProviderURL:        mockModels.URL, // Use ProviderURL to override the models endpoint
		MaxRequestBodySize: 10 * 1024 * 1024,
	}

	// Create server
	server := NewServer(config)
	testServer := httptest.NewServer(server)
	defer testServer.Close()

	t.Run("GET request", func(t *testing.T) {
		// Capture logs
		logs := captureStderr(t, func() {
			resp, err := http.Get(testServer.URL + "/v1/models")
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("Expected status 200, got %d: %s", resp.StatusCode, string(body))
			}
		})

		// Verify INFO level log
		if !strings.Contains(logs, "[INFO]") {
			t.Error("Expected INFO level log for models endpoint")
			t.Logf("Captured logs: %s", logs)
		}

		// Verify log message
		expectedMsg := "GET /v1/models | Models listing endpoint"
		if !strings.Contains(logs, expectedMsg) {
			t.Errorf("Expected '%s' in logs, got: %s", expectedMsg, logs)
		}

		// Verify request ID is present
		if !strings.Contains(logs, "[#") {
			t.Error("Expected request ID in logs")
		}
	})

	t.Run("Invalid method POST", func(t *testing.T) {
		// Capture logs
		logs := captureStderr(t, func() {
			resp, err := http.Post(testServer.URL+"/v1/models", "application/json", bytes.NewReader([]byte("{}")))
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			// Should return error for POST method
			if resp.StatusCode == http.StatusOK {
				t.Error("Expected error for POST method, got 200")
			}
		})

		// Should still log the endpoint access
		expectedMsg := "POST /v1/models | Models listing endpoint"
		if !strings.Contains(logs, expectedMsg) {
			t.Errorf("Expected '%s' in logs even for invalid method", expectedMsg)
		}
	})

	t.Run("Invalid method DELETE", func(t *testing.T) {
		// Capture logs
		logs := captureStderr(t, func() {
			req, _ := http.NewRequest("DELETE", testServer.URL+"/v1/models", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			// Should return error for DELETE method
			if resp.StatusCode == http.StatusOK {
				t.Error("Expected error for DELETE method, got 200")
			}
		})

		// Should still log the endpoint access
		expectedMsg := "DELETE /v1/models | Models listing endpoint"
		if !strings.Contains(logs, expectedMsg) {
			t.Errorf("Expected '%s' in logs even for invalid method", expectedMsg)
		}
	})
}

// TestAllEndpointsLogging tests that all endpoints produce their expected log messages
func TestAllEndpointsLogging(t *testing.T) {
	// Reset and initialize logger with INFO level
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Create mock server that handles all endpoints
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages":
			resp := AnthropicResponse{
				ID:   "msg_test",
				Type: "message",
				Role: core.RoleAssistant,
				Content: []AnthropicContentBlock{
					{Type: "text", Text: "Test"},
				},
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Logf("Failed to encode response: %v", err)
			}
		case "/v1/chat/completions":
			resp := OpenAIResponse{
				ID:     "chatcmpl-test",
				Object: "chat.completion",
				Model:  "gpt-4",
				Choices: []OpenAIChoice{
					{
						Index:        0,
						Message:      OpenAIMessage{Role: "assistant", Content: "Test"},
						FinishReason: "stop",
					},
				},
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Logf("Failed to encode response: %v", err)
			}
		case "/v1/models":
			resp := map[string]interface{}{
				"object": "list",
				"data":   []interface{}{},
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Logf("Failed to encode response: %v", err)
			}
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer mockServer.Close()

	// Create server config
	config := &Config{
		Provider:           "openai",
		OpenAIAPIKey:       "test-key",
		OpenAIURL:          mockServer.URL,
		AnthropicAPIKey:    "test-key",
		AnthropicURL:       mockServer.URL,
		MaxRequestBodySize: 10 * 1024 * 1024,
	}

	// Create server
	server := NewServer(config)
	testServer := httptest.NewServer(server)
	defer testServer.Close()

	// Test all endpoints
	tests := []struct {
		method      string
		path        string
		expectedLog string
		requestBody interface{}
		contentType string
	}{
		{
			method:      "GET",
			path:        "/",
			expectedLog: "GET / | Root endpoint accessed",
		},
		{
			method:      "POST",
			path:        "/v1/messages",
			expectedLog: "POST /v1/messages | Anthropic messages endpoint",
			requestBody: AnthropicRequest{
				Model: "claude-3-opus-20240229",
				Messages: []AnthropicMessage{
					{Role: core.RoleUser, Content: json.RawMessage(`"Test"`)},
				},
				MaxTokens: 10,
			},
			contentType: "application/json",
		},
		{
			method:      "POST",
			path:        "/v1/chat/completions",
			expectedLog: "POST /v1/chat/completions | OpenAI chat completions endpoint",
			requestBody: OpenAIRequest{
				Model: "gpt-4",
				Messages: []OpenAIMessage{
					{Role: "user", Content: "Test"},
				},
			},
			contentType: "application/json",
		},
		{
			method:      "GET",
			path:        "/v1/models",
			expectedLog: "GET /v1/models | Models listing endpoint",
		},
		{
			method:      "POST",
			path:        "/v1/messages/count_tokens",
			expectedLog: "POST /v1/messages/count_tokens",
			requestBody: AnthropicTokenCountRequest{
				Messages: []AnthropicMessage{
					{Role: core.RoleUser, Content: json.RawMessage(`"Test"`)},
				},
			},
			contentType: "application/json",
		},
		{
			method:      "GET",
			path:        "/unknown/path",
			expectedLog: "GET /unknown/path | Path not found",
		},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("%s %s", test.method, test.path), func(t *testing.T) {
			// Capture logs
			logs := captureStderr(t, func() {
				var req *http.Request
				var err error

				// Create request based on method and body
				if test.requestBody != nil {
					body, _ := json.Marshal(test.requestBody)
					req, err = http.NewRequest(test.method, testServer.URL+test.path, bytes.NewReader(body))
					if test.contentType != "" {
						req.Header.Set("Content-Type", test.contentType)
					}
				} else {
					req, err = http.NewRequest(test.method, testServer.URL+test.path, nil)
				}

				if err != nil {
					t.Errorf("Failed to create request: %v", err)
					return
				}

				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Errorf("Request failed: %v", err)
					return
				}
				defer resp.Body.Close()
			})

			// Verify expected log message
			if !strings.Contains(logs, test.expectedLog) {
				t.Errorf("Expected '%s' in logs, got: %s", test.expectedLog, logs)
			}

			// Verify request ID is present
			if !strings.Contains(logs, "[#") {
				t.Error("Expected request ID in logs")
			}
		})
	}
}

// TestStreamingEndpointLogging tests that streaming requests also produce proper logs
func TestStreamingEndpointLogging(t *testing.T) {
	// Reset and initialize logger with INFO level
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Create mock streaming server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return SSE stream
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\n")
		fmt.Fprintf(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n")
		fmt.Fprintf(w, "event: message_stop\n")
		fmt.Fprintf(w, "data: {\"type\":\"message_stop\"}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer mockServer.Close()

	// Create server config
	config := &Config{
		Provider:           "anthropic",
		AnthropicAPIKey:    "test-key",
		AnthropicURL:       mockServer.URL,
		MaxRequestBodySize: 10 * 1024 * 1024,
	}

	// Create server
	server := NewServer(config)
	testServer := httptest.NewServer(server)
	defer testServer.Close()

	t.Run("Streaming messages request", func(t *testing.T) {
		// Create streaming request
		anthReq := AnthropicRequest{
			Model: "claude-3-opus-20240229",
			Messages: []AnthropicMessage{
				{
					Role:    core.RoleUser,
					Content: json.RawMessage(`"Hello"`),
				},
			},
			MaxTokens: 100,
			Stream:    true,
		}

		reqBody, _ := json.Marshal(anthReq)

		// Capture logs
		logs := captureStderr(t, func() {
			resp, err := http.Post(
				testServer.URL+"/v1/messages",
				"application/json",
				bytes.NewReader(reqBody),
			)
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			// Read response to ensure streaming completes
			if _, err := io.ReadAll(resp.Body); err != nil {
				t.Logf("Failed to read response body: %v", err)
			}
		})

		// Verify endpoint log message
		expectedMsg := "POST /v1/messages | Anthropic messages endpoint"
		if !strings.Contains(logs, expectedMsg) {
			t.Errorf("Expected '%s' in logs for streaming request", expectedMsg)
		}

		// Verify request ID is present
		if !strings.Contains(logs, "[#") {
			t.Error("Expected request ID in logs")
		}
	})
}
