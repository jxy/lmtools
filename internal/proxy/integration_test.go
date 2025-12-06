//go:build integration

package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIntegrationBasicChat(t *testing.T) {
	openAIMock := httptest.NewServer(NewMockOpenAI(t))
	t.Cleanup(openAIMock.Close)

	config := &Config{
		OpenAIAPIKey:       "test-openai-key",
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        openAIMock.URL + "/v1",
		SmallModel:         "gpt-4o-mini",
		Model:              "gpt-4o",
		MaxRequestBodySize: 10 * 1024 * 1024,
	}

	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)
	proxyServer := httptest.NewServer(server)
	t.Cleanup(proxyServer.Close)

	tests := []struct {
		name      string
		request   AnthropicRequest
		checkResp func(t *testing.T, resp *AnthropicResponse)
	}{
		{
			name: "haiku to OpenAI",
			request: AnthropicRequest{
				Model:     "claude-3-haiku-20240307",
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{
						Role:    core.RoleUser,
						Content: json.RawMessage(`"Hello"`),
					},
				},
			},
			checkResp: func(t *testing.T, resp *AnthropicResponse) {
				if len(resp.Content) == 0 {
					t.Fatal("Expected content in response")
				}
				if resp.Content[0].Type != "text" {
					t.Errorf("Expected text content, got %s", resp.Content[0].Type)
				}
				if !strings.Contains(resp.Content[0].Text, "OpenAI") {
					t.Errorf("Expected OpenAI response, got %s", resp.Content[0].Text)
				}
			},
		},
		{
			name: "direct Google AI model",
			request: AnthropicRequest{
				Model:     "gemini-2.0-flash",
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{
						Role:    core.RoleUser,
						Content: json.RawMessage(`"Hello"`),
					},
				},
			},
			checkResp: func(t *testing.T, resp *AnthropicResponse) {
				if len(resp.Content) == 0 {
					t.Fatal("Expected content in response")
				}
				// With provider=openai, Google AI models also go to OpenAI
				if !strings.Contains(resp.Content[0].Text, "OpenAI") {
					t.Errorf("Expected OpenAI response, got %s", resp.Content[0].Text)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make request
			reqBody, err := json.Marshal(tt.request)
			if err != nil {
				t.Fatalf("Failed to marshal request: %v", err)
			}
			t.Logf("Request body: %s", string(reqBody))

			resp, err := http.Post(
				proxyServer.URL+"/v1/messages",
				"application/json",
				bytes.NewReader(reqBody),
			)
			if err != nil {
				t.Fatalf("Request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("Unexpected status %d: %s", resp.StatusCode, string(body))
			}

			// Parse response
			var anthResp AnthropicResponse
			if err := json.NewDecoder(resp.Body).Decode(&anthResp); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			// Check response
			tt.checkResp(t, &anthResp)
		})
	}
}

func TestIntegrationStreaming(t *testing.T) {
	argoMock := httptest.NewServer(NewMockArgo(t))
	t.Cleanup(argoMock.Close)

	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        argoMock.URL,
		ArgoUser:           "testuser",
		ArgoEnv:            "test",
		Model:              "gpto3",
		SmallModel:         "gemini25flash",
		MaxRequestBodySize: 10 * 1024 * 1024,
	}

	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)
	proxyServer := httptest.NewServer(server)
	t.Cleanup(proxyServer.Close)

	// Make streaming request
	req := AnthropicRequest{
		Model:     "gpto3", // Use Argo model for clarity
		MaxTokens: 100,
		Stream:    true,
		Messages: []AnthropicMessage{
			{
				Role:    core.RoleUser,
				Content: json.RawMessage(`"Hello"`),
			},
		},
	}

	reqBody, _ := json.Marshal(req)
	resp, err := http.Post(
		proxyServer.URL+"/v1/messages",
		"application/json",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Check Content-Type
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Expected Content-Type text/event-stream, got %s", ct)
	}

	// Read streaming response
	// Read the entire stream
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Error reading stream: %v", err)
	}

	// Verify we got expected events using the new helper
	// Note: ping events only occur if response takes longer than ping interval (1s)
	// Since we removed artificial delays, the response is now fast and no pings are sent
	expectedEvents := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	assertContainsEvents(t, string(body), expectedEvents)
}

func TestIntegrationRetry(t *testing.T) {
	// Create a mock server that fails initially then succeeds
	attemptCount := 0
	retryMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it's hitting the chat endpoint (now includes /api/v1 prefix)
		if r.URL.Path != "/api/v1/resource/chat/" {
			t.Errorf("Expected path /api/v1/resource/chat/, got %s", r.URL.Path)
		}

		attemptCount++
		t.Logf("Retry mock received attempt %d", attemptCount)

		// Fail first 2 attempts with 503
		if attemptCount < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			if _, err := w.Write([]byte("Service temporarily unavailable")); err != nil {
				t.Logf("write error: %v", err)
			}
			return
		}

		// Success on 3rd attempt
		resp := ArgoChatResponse{
			Response: "Success after retries",
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("encode error: %v", err)
		}
	}))
	defer retryMock.Close()

	// Create config with retry settings
	config := &Config{
		// Don't set OpenAI/Google keys so Argo is used
		ArgoUser:           "testuser",
		ArgoEnv:            "test",
		ProviderURL:        retryMock.URL, // Use ProviderURL instead
		Provider:           constants.ProviderArgo,
		SmallModel:         "gpt35",
		Model:              "gpt4",
		MaxRequestBodySize: 10 * 1024 * 1024,
	}
	// Create proxy server with fast retries (NewEndpoints is called internally)
	server := NewTestServerWithFastRetries(t, config)
	proxyServer := httptest.NewServer(server)
	defer proxyServer.Close()

	// Make request
	reqBody := AnthropicRequest{
		Model:     "gpt4",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role:    core.RoleUser,
				Content: json.RawMessage(`"Test retry mechanism"`),
			},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	// Send request
	resp, err := http.Post(proxyServer.URL+"/v1/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}
	defer resp.Body.Close()

	// Should succeed after retries
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, string(body))
	}

	// Verify response
	var anthResp AnthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&anthResp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(anthResp.Content) == 0 {
		t.Fatal("Expected content in response")
	}

	if !strings.Contains(anthResp.Content[0].Text, "Success after retries") {
		t.Errorf("Expected 'Success after retries', got %s", anthResp.Content[0].Text)
	}

	// Verify retry count
	if attemptCount != 3 {
		t.Errorf("Expected 3 attempts, got %d", attemptCount)
	}
}

func TestIntegrationRetryRateLimit(t *testing.T) {
	// Test retry with 429 rate limit errors
	attemptCount := 0
	rateLimitMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it's hitting the chat endpoint (now includes /api/v1 prefix)
		if r.URL.Path != "/api/v1/resource/chat/" {
			t.Errorf("Expected path /api/v1/resource/chat/, got %s", r.URL.Path)
		}

		attemptCount++
		t.Logf("Rate limit mock received attempt %d", attemptCount)

		// Return 429 for first attempt with Retry-After header
		if attemptCount == 1 {
			w.Header().Set("Retry-After", "1") // 1 second
			w.WriteHeader(http.StatusTooManyRequests)
			if _, err := w.Write([]byte("Rate limit exceeded")); err != nil {
				t.Logf("write error: %v", err)
			}
			return
		}

		// Success on 2nd attempt
		resp := ArgoChatResponse{
			Response: "Success after rate limit",
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("encode error: %v", err)
		}
	}))
	defer rateLimitMock.Close()

	// Create config
	config := &Config{
		// Don't set OpenAI/Google keys so Argo is used
		ArgoUser:           "testuser",
		ArgoEnv:            "test",
		ProviderURL:        rateLimitMock.URL, // Use ProviderURL instead
		Provider:           constants.ProviderArgo,
		SmallModel:         "gpt35",
		Model:              "gpt4",
		MaxRequestBodySize: 10 * 1024 * 1024,
	}
	// Use production server to test real Retry-After header handling
	// (NewTestServerWithFastRetries ignores Retry-After and uses millisecond delays)
	server, err := NewServer(config)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	proxyServer := httptest.NewServer(server)
	defer proxyServer.Close()

	// Make request
	reqBody := AnthropicRequest{
		Model:     "gpt4",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role:    core.RoleUser,
				Content: json.RawMessage(`"Test rate limit retry"`),
			},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	// Send request
	start := time.Now()
	resp, err := http.Post(proxyServer.URL+"/v1/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}
	defer resp.Body.Close()
	duration := time.Since(start)

	// Should succeed after retry
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, string(body))
	}

	// Verify response
	var anthResp AnthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&anthResp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(anthResp.Content) == 0 {
		t.Fatal("Expected content in response")
	}

	if !strings.Contains(anthResp.Content[0].Text, "Success after rate limit") {
		t.Errorf("Expected 'Success after rate limit', got %s", anthResp.Content[0].Text)
	}

	// Verify retry count
	if attemptCount != 2 {
		t.Errorf("Expected 2 attempts, got %d", attemptCount)
	}

	// Verify it respected Retry-After (should take close to 1 second)
	// Allow some tolerance for timing variations (750ms instead of 1s to account for system timing)
	if duration < 750*time.Millisecond {
		t.Errorf("Expected delay of at least 750ms (with 1s Retry-After), got %v", duration)
	}
}

func TestIntegrationSimulatedStreamingWithTools(t *testing.T) {
	openAIMock := httptest.NewServer(NewMockOpenAI(t))
	t.Cleanup(openAIMock.Close)

	config := &Config{
		OpenAIAPIKey:       "test-openai-key",
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        openAIMock.URL + "/v1",
		SmallModel:         "gpt-4o-mini",
		Model:              "gpt-4o",
		MaxRequestBodySize: 10 * 1024 * 1024,
	}

	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)
	proxyServer := httptest.NewServer(server)
	t.Cleanup(proxyServer.Close)

	// Make streaming request with tools
	req := AnthropicRequest{
		Model:     "gpt4",
		MaxTokens: 100,
		Stream:    true,
		Messages: []AnthropicMessage{
			{
				Role:    core.RoleUser,
				Content: json.RawMessage(`"List the contents of the current directory"`),
			},
		},
		Tools: []AnthropicTool{
			{
				Name:        "LS",
				Description: "List directory contents",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{
							"type":        "string",
							"description": "Directory path",
						},
					},
					"required": []string{"path"},
				},
			},
		},
	}

	reqBody, _ := json.Marshal(req)
	resp, err := http.Post(
		proxyServer.URL+"/v1/messages",
		"application/json",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Read streaming response and count content_block_stop events
	reader := bufio.NewReader(resp.Body)
	contentBlockStopCount := 0
	blockIndices := make(map[int]int) // Track how many times each index is closed

	// Add timeout protection for the read loop
	readCtx, readCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer readCancel()

	// Create a channel to signal when reading is done
	readDone := make(chan struct{})
	readErr := make(chan error, 1)

	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				close(readDone)
				return
			}
			if err != nil {
				readErr <- err
				return
			}

			line = strings.TrimSpace(line)

			// Check for content_block_stop events
			if line == "event: content_block_stop" {
				// Next line should be data
				dataLine, err := reader.ReadString('\n')
				if err == nil {
					dataLine = strings.TrimSpace(dataLine)
					if strings.HasPrefix(dataLine, "data: ") {
						var data map[string]interface{}
						jsonStr := strings.TrimPrefix(dataLine, "data: ")
						if err := json.Unmarshal([]byte(jsonStr), &data); err == nil {
							if index, ok := data["index"].(float64); ok {
								blockIndices[int(index)]++
								contentBlockStopCount++
							}
						}
					}
				}
			}

			// Check for completion
			if line == "data: [DONE]" {
				close(readDone)
				return
			}
		}
	}()

	// Wait for reading to complete or timeout
	select {
	case <-readDone:
		// Reading completed successfully
	case err := <-readErr:
		t.Fatalf("Error reading stream: %v", err)
	case <-readCtx.Done():
		t.Fatal("Timeout reading stream response after 10 seconds")
	}

	// Verify no double closing
	for index, count := range blockIndices {
		if count > 1 {
			t.Errorf("Block %d was closed %d times (expected 1)", index, count)
		}
	}

	// Should have at least 1 content_block_stop event
	// Note: Argo tool format parsing is not implemented, so tool tags are included in text
	if contentBlockStopCount < 1 {
		t.Errorf("Expected at least 1 content_block_stop event, got %d", contentBlockStopCount)
	}

	t.Logf("Blocks closed: %v", blockIndices)
}

func TestCustomProviderURL(t *testing.T) {
	// Test that custom provider URLs are used instead of defaults
	tests := []struct {
		name              string
		preferredProvider string
		setupConfig       func(t *testing.T) (*Config, *httptest.Server)
		expectedPath      string
	}{
		{
			name:              "OpenAI custom URL",
			preferredProvider: constants.ProviderOpenAI,
			setupConfig: func(t *testing.T) (*Config, *httptest.Server) {
				// Create a custom mock server
				customMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Logf("Custom OpenAI mock received: %s %s", r.Method, r.URL.Path)

					// Verify the expected path (now includes /chat/completions)
					if r.URL.Path != "/custom/openai/path/chat/completions" {
						t.Errorf("Expected path /custom/openai/path/chat/completions, got %s", r.URL.Path)
					}

					// Return a simple response
					resp := OpenAIResponse{
						ID:      "test-custom-openai",
						Object:  "chat.completion",
						Created: 1234567890,
						Model:   "gpt-4o",
						Choices: []OpenAIChoice{
							{
								Message: OpenAIMessage{
									Role:    "assistant",
									Content: "Response from custom OpenAI endpoint",
								},
								Index:        0,
								FinishReason: "stop",
							},
						},
						Usage: &OpenAIUsage{
							PromptTokens:     10,
							CompletionTokens: 5,
							TotalTokens:      15,
						},
					}
					if err := json.NewEncoder(w).Encode(resp); err != nil {
						t.Logf("encode error: %v", err)
					}
				}))

				config := &Config{
					OpenAIAPIKey:       "test-key",
					GoogleAPIKey:       "test-key",
					ArgoUser:           "testuser",
					ArgoEnv:            "test",
					Provider:           constants.ProviderOpenAI,
					ProviderURL:        customMock.URL + "/custom/openai/path",
					SmallModel:         "gpt-4o-mini",
					Model:              "gpt-4o",
					MaxRequestBodySize: 10 * 1024 * 1024,
				}

				return config, customMock
			},
			expectedPath: "/custom/openai/path/chat/completions",
		},
		{
			name:              "Google custom URL",
			preferredProvider: constants.ProviderGoogle,
			setupConfig: func(t *testing.T) (*Config, *httptest.Server) {
				// Create a custom mock server
				customMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Logf("Custom Google mock received: %s %s", r.Method, r.URL.Path)

					// Google URLs include the model in the path
					if !strings.Contains(r.URL.Path, "/custom/google/models/") {
						t.Errorf("Expected path to contain /custom/google/models/, got %s", r.URL.Path)
					}

					// Return a simple response
					resp := GoogleResponse{
						Candidates: []GoogleCandidate{
							{
								Content: GoogleContent{
									Parts: []GooglePart{
										{Text: "Response from custom Google endpoint"},
									},
									Role: "model",
								},
								FinishReason: "STOP",
							},
						},
					}
					if err := json.NewEncoder(w).Encode(resp); err != nil {
						t.Logf("encode error: %v", err)
					}
				}))

				config := &Config{
					OpenAIAPIKey:       "test-key",
					GoogleAPIKey:       "test-key",
					ArgoUser:           "testuser",
					ArgoEnv:            "test",
					Provider:           constants.ProviderGoogle,
					ProviderURL:        customMock.URL + "/custom/google/models",
					SmallModel:         "gemini-2.0-flash",
					Model:              "gemini-2.5-pro-preview-03-25",
					MaxRequestBodySize: 10 * 1024 * 1024,
				}

				return config, customMock
			},
			expectedPath: "/custom/google/models",
		},
		{
			name:              "Argo custom URL",
			preferredProvider: constants.ProviderArgo,
			setupConfig: func(t *testing.T) (*Config, *httptest.Server) {
				// Create a custom mock server
				customMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Logf("Custom Argo mock received: %s %s", r.Method, r.URL.Path)

					// Argo URLs end with /api/v1/resource/chat/
					if !strings.HasSuffix(r.URL.Path, "/api/v1/resource/chat/") {
						t.Errorf("Expected path to end with /api/v1/resource/chat/, got %s", r.URL.Path)
					}

					// Return a simple response
					resp := ArgoChatResponse{
						Response: "Response from custom Argo endpoint",
					}
					if err := json.NewEncoder(w).Encode(resp); err != nil {
						t.Logf("encode error: %v", err)
					}
				}))

				config := &Config{
					OpenAIAPIKey:       "test-key",
					GoogleAPIKey:       "test-key",
					ArgoUser:           "testuser",
					ArgoEnv:            "test",
					Provider:           constants.ProviderArgo,
					ProviderURL:        customMock.URL + "/custom/argo",
					SmallModel:         "gpt35",
					Model:              "gpt4",
					MaxRequestBodySize: 10 * 1024 * 1024,
				}

				return config, customMock
			},
			expectedPath: "/custom/argo/api/v1/resource/chat/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup custom config and mock
			config, customMock := tt.setupConfig(t)
			defer customMock.Close()

			// Create proxy server with custom config
			server, cleanup := NewTestServer(t, config)
			t.Cleanup(cleanup)
			proxyServer := httptest.NewServer(server)
			defer proxyServer.Close()

			// Create request
			reqBody := AnthropicRequest{
				Model:     "claude-3-haiku-20240307",
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{
						Role:    core.RoleUser,
						Content: json.RawMessage(`"Test custom URL"`),
					},
				},
			}

			body, err := json.Marshal(reqBody)
			if err != nil {
				t.Fatalf("Failed to marshal request: %v", err)
			}

			// Send request to proxy
			resp, err := http.Post(proxyServer.URL+"/v1/messages", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("Failed to send request: %v", err)
			}
			defer resp.Body.Close()

			// Check response
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, string(body))
			}

			// Verify response contains expected content
			var anthResp AnthropicResponse
			if err := json.NewDecoder(resp.Body).Decode(&anthResp); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			if len(anthResp.Content) == 0 {
				t.Fatal("Expected content in response")
			}

			// Log success
			t.Logf("Successfully used custom URL for %s provider", tt.preferredProvider)
		})
	}
}
