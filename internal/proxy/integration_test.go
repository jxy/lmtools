//go:build integration
// +build integration

package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)


func TestIntegrationBasicChat(t *testing.T) {
	proxyServer, openAIMock, geminiMock, argoMock := SetupTestServer(t)
	defer proxyServer.Close()
	defer openAIMock.Close()
	defer geminiMock.Close()
	defer argoMock.Close()

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
						Role:    RoleUser,
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
			name: "direct Gemini model",
			request: AnthropicRequest{
				Model:     "gemini-2.0-flash",
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Hello"`),
					},
				},
			},
			checkResp: func(t *testing.T, resp *AnthropicResponse) {
				if len(resp.Content) == 0 {
					t.Fatal("Expected content in response")
				}
				if !strings.Contains(resp.Content[0].Text, "Gemini") {
					t.Errorf("Expected Gemini response, got %s", resp.Content[0].Text)
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
	proxyServer, openAIMock, geminiMock, argoMock := SetupTestServer(t)
	defer proxyServer.Close()
	defer openAIMock.Close()
	defer geminiMock.Close()
	defer argoMock.Close()

	// Make streaming request
	req := AnthropicRequest{
		Model:     "claude-3-haiku",
		MaxTokens: 100,
		Stream:    true,
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
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
	expectedEvents := []string{"message_start", "content_block_start", "ping", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	assertContainsEvents(t, string(body), expectedEvents)
}

func TestIntegrationRetry(t *testing.T) {
	// Create a mock server that fails initially then succeeds
	attemptCount := 0
	retryMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it's hitting the chat endpoint
		if r.URL.Path != "/chat/" {
			t.Errorf("Expected path /chat/, got %s", r.URL.Path)
		}
		
		attemptCount++
		t.Logf("Retry mock received attempt %d", attemptCount)
		
		// Fail first 2 attempts with 503
		if attemptCount < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("Service temporarily unavailable"))
			return
		}
		
		// Success on 3rd attempt
		resp := ArgoChatResponse{
			Response: "Success after retries",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer retryMock.Close()
	
	// Create config with retry settings
	config := &Config{
		OpenAIAPIKey:       "test-key",
		GeminiAPIKey:       "test-key",
		ArgoUser:           "testuser",
		ArgoEnv:            "test",
		ArgoBaseURL:        retryMock.URL, // Use ArgoBaseURL instead
		Provider:  "argo",
		ProviderURL:        "",
		SmallModel:         "gpt35",
		BigModel:           "gpt4",
		MaxRequestBodySize: 10 * 1024 * 1024,
		OpenAIModels:       []string{"gpt-4o", "gpt-4o-mini"},
		GeminiModels:       []string{"gemini-2.0-flash", "gemini-1.5-pro"},
		ArgoModels:         []string{"gpt4", "gpt35", "claude"},
	}
	config.InitializeModelLists()
	
	// Create proxy server
	server := NewServer(config)
	proxyServer := httptest.NewServer(server)
	defer proxyServer.Close()
	
	// Make request
	reqBody := AnthropicRequest{
		Model:     "gpt4",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
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
		// Verify it's hitting the chat endpoint
		if r.URL.Path != "/chat/" {
			t.Errorf("Expected path /chat/, got %s", r.URL.Path)
		}
		
		attemptCount++
		t.Logf("Rate limit mock received attempt %d", attemptCount)
		
		// Return 429 for first attempt with Retry-After header
		if attemptCount == 1 {
			w.Header().Set("Retry-After", "1") // 1 second
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("Rate limit exceeded"))
			return
		}
		
		// Success on 2nd attempt
		resp := ArgoChatResponse{
			Response: "Success after rate limit",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer rateLimitMock.Close()
	
	// Create config
	config := &Config{
		OpenAIAPIKey:       "test-key",
		GeminiAPIKey:       "test-key",
		ArgoUser:           "testuser",
		ArgoEnv:            "test",
		ArgoBaseURL:        rateLimitMock.URL, // Use ArgoBaseURL instead
		Provider:  "argo",
		SmallModel:         "gpt35",
		BigModel:           "gpt4",
		MaxRequestBodySize: 10 * 1024 * 1024,
		OpenAIModels:       []string{"gpt-4o", "gpt-4o-mini"},
		GeminiModels:       []string{"gemini-2.0-flash", "gemini-1.5-pro"},
		ArgoModels:         []string{"gpt4", "gpt35", "claude"},
	}
	config.InitializeModelLists()
	
	// Create proxy server
	server := NewServer(config)
	proxyServer := httptest.NewServer(server)
	defer proxyServer.Close()
	
	// Make request
	reqBody := AnthropicRequest{
		Model:     "gpt4",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
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
	// Allow some tolerance for timing variations (800ms instead of 1s)
	if duration < 800*time.Millisecond {
		t.Errorf("Expected delay of at least 800ms (with 1s Retry-After), got %v", duration)
	}
}

func TestIntegrationSimulatedStreamingWithTools(t *testing.T) {
	proxyServer, _, _, _ := SetupTestServer(t)

	// Make streaming request with tools
	req := AnthropicRequest{
		Model:     "gpt4",
		MaxTokens: 100,
		Stream:    true,
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
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

	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Error reading stream: %v", err)
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
			break
		}
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
			preferredProvider: "openai",
			setupConfig: func(t *testing.T) (*Config, *httptest.Server) {
				// Create a custom mock server
				customMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Logf("Custom OpenAI mock received: %s %s", r.Method, r.URL.Path)
					
					// Verify the expected path
					if r.URL.Path != "/custom/openai/path" {
						t.Errorf("Expected path /custom/openai/path, got %s", r.URL.Path)
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
					json.NewEncoder(w).Encode(resp)
				}))
				
				config := &Config{
					OpenAIAPIKey:       "test-key",
					GeminiAPIKey:       "test-key",
					ArgoUser:           "testuser",
					ArgoEnv:            "test",
					Provider:  "openai",
					ProviderURL:        customMock.URL + "/custom/openai/path",
					SmallModel:         "gpt-4o-mini",
					BigModel:           "gpt-4o",
					MaxRequestBodySize: 10 * 1024 * 1024,
					OpenAIModels:       []string{"gpt-4o", "gpt-4o-mini"},
					GeminiModels:       []string{"gemini-2.0-flash", "gemini-1.5-pro"},
					ArgoModels:         []string{"gpt4", "gpt35", "claude"},
				}
				config.InitializeModelLists()
				
				return config, customMock
			},
			expectedPath: "/custom/openai/path",
		},
		{
			name:              "Gemini custom URL",
			preferredProvider: "google",
			setupConfig: func(t *testing.T) (*Config, *httptest.Server) {
				// Create a custom mock server
				customMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Logf("Custom Gemini mock received: %s %s", r.Method, r.URL.Path)
					
					// Gemini URLs include the model in the path
					if !strings.Contains(r.URL.Path, "/custom/gemini/models/") {
						t.Errorf("Expected path to contain /custom/gemini/models/, got %s", r.URL.Path)
					}
					
					// Return a simple response
					resp := GeminiResponse{
						Candidates: []GeminiCandidate{
							{
								Content: GeminiContent{
									Parts: []GeminiPart{
										{Text: "Response from custom Gemini endpoint"},
									},
									Role: "model",
								},
								FinishReason: "STOP",
							},
						},
					}
					json.NewEncoder(w).Encode(resp)
				}))
				
				config := &Config{
					OpenAIAPIKey:       "test-key",
					GeminiAPIKey:       "test-key",
					ArgoUser:           "testuser",
					ArgoEnv:            "test",
					Provider:  "google",
					ProviderURL:        customMock.URL + "/custom/gemini/models",
					SmallModel:         "gemini-2.0-flash",
					BigModel:           "gemini-2.5-pro-preview-03-25",
					MaxRequestBodySize: 10 * 1024 * 1024,
					OpenAIModels:       []string{"gpt-4o", "gpt-4o-mini"},
					GeminiModels:       []string{"gemini-2.0-flash", "gemini-1.5-pro"},
					ArgoModels:         []string{"gpt4", "gpt35", "claude"},
				}
				config.InitializeModelLists()
				
				return config, customMock
			},
			expectedPath: "/custom/gemini/models",
		},
		{
			name:              "Argo custom URL",
			preferredProvider: "argo",
			setupConfig: func(t *testing.T) (*Config, *httptest.Server) {
				// Create a custom mock server
				customMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Logf("Custom Argo mock received: %s %s", r.Method, r.URL.Path)
					
					// Argo URLs end with /chat/
					if !strings.HasSuffix(r.URL.Path, "/chat/") {
						t.Errorf("Expected path to end with /chat/, got %s", r.URL.Path)
					}
					
					// Return a simple response
					resp := ArgoChatResponse{
						Response: "Response from custom Argo endpoint",
					}
					json.NewEncoder(w).Encode(resp)
				}))
				
				config := &Config{
					OpenAIAPIKey:       "test-key",
					GeminiAPIKey:       "test-key",
					ArgoUser:           "testuser",
					ArgoEnv:            "test",
					Provider:  "argo",
					ProviderURL:        customMock.URL + "/custom/argo",
					SmallModel:         "gpt35",
					BigModel:           "gpt4",
					MaxRequestBodySize: 10 * 1024 * 1024,
					OpenAIModels:       []string{"gpt-4o", "gpt-4o-mini"},
					GeminiModels:       []string{"gemini-2.0-flash", "gemini-1.5-pro"},
					ArgoModels:         []string{"gpt4", "gpt35", "claude"},
				}
				config.InitializeModelLists()
				
				return config, customMock
			},
			expectedPath: "/custom/argo/chat/",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup custom config and mock
			config, customMock := tt.setupConfig(t)
			defer customMock.Close()
			
			// Create proxy server with custom config
			server := NewServer(config)
			proxyServer := httptest.NewServer(server)
			defer proxyServer.Close()
			
			// Create request
			reqBody := AnthropicRequest{
				Model:     "claude-3-haiku-20240307",
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
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
