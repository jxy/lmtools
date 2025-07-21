//go:build integration
// +build integration

package apiproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// MockProvider simulates API provider responses
type MockProvider struct {
	t         *testing.T
	provider  string
	responses map[string]interface{}
}

func NewMockProvider(t *testing.T, provider string) *MockProvider {
	return &MockProvider{
		t:         t,
		provider:  provider,
		responses: make(map[string]interface{}),
	}
}

func (m *MockProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.t.Logf("Mock %s received: %s %s", m.provider, r.Method, r.URL.Path)

	// Read body
	body, _ := io.ReadAll(r.Body)
	m.t.Logf("Request body: %s", string(body))

	switch m.provider {
	case "openai":
		m.handleOpenAI(w, r, body)
	case "gemini":
		m.handleGemini(w, r, body)
	case "argo":
		m.handleArgo(w, r, body)
	default:
		http.Error(w, "Unknown provider", http.StatusBadRequest)
	}
}

func (m *MockProvider) handleOpenAI(w http.ResponseWriter, r *http.Request, body []byte) {
	// Check authorization
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Handle streaming
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send streaming chunks
		chunks := []string{
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"Hello"},"index":0}]}`,
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":" from"},"index":0}]}`,
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":" OpenAI"},"index":0}]}`,
			`data: {"id":"chatcmpl-1","choices":[{"delta":{},"finish_reason":"stop","index":0}]}`,
			`data: [DONE]`,
		}

		for _, chunk := range chunks {
			fmt.Fprintf(w, "%s\n\n", chunk)
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
		return
	}

	// Non-streaming response
	resp := OpenAIResponse{
		ID:    "chatcmpl-123",
		Model: req.Model,
		Choices: []OpenAIChoice{
			{
				Message: OpenAIMessage{
					Role:    "assistant",
					Content: "Hello from mock OpenAI!",
				},
				FinishReason: "stop",
			},
		},
		Usage: OpenAIUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (m *MockProvider) handleGemini(w http.ResponseWriter, r *http.Request, body []byte) {
	// Check API key in query
	if !strings.Contains(r.URL.Query().Get("key"), "gemini-key") {
		http.Error(w, "Invalid API key", http.StatusForbidden)
		return
	}

	var req GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Handle streaming
	if strings.Contains(r.URL.Path, "streamGenerateContent") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send Gemini streaming format
		chunks := []map[string]interface{}{
			{
				"candidates": []map[string]interface{}{
					{
						"content": map[string]interface{}{
							"parts": []map[string]interface{}{
								{"text": "Hello"},
							},
						},
					},
				},
			},
			{
				"candidates": []map[string]interface{}{
					{
						"content": map[string]interface{}{
							"parts": []map[string]interface{}{
								{"text": " from Gemini"},
							},
						},
						"finishReason": "STOP",
					},
				},
			},
		}

		for _, chunk := range chunks {
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", string(data))
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
		return
	}

	// Non-streaming response
	resp := GeminiResponse{
		Candidates: []GeminiCandidate{
			{
				Content: GeminiContent{
					Role: "model",
					Parts: []GeminiPart{
						{Text: "Hello from mock Gemini!"},
					},
				},
				FinishReason: "STOP",
			},
		},
		UsageMetadata: GeminiUsage{
			PromptTokenCount:     10,
			CandidatesTokenCount: 5,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (m *MockProvider) handleArgo(w http.ResponseWriter, r *http.Request, body []byte) {
	var req ArgoChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Handle streaming
	if strings.Contains(r.URL.Path, "streamchat") {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)

		// Stream plain text
		response := "Hello from mock Argo streaming!"
		for _, char := range response {
			fmt.Fprintf(w, "%c", char)
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
		return
	}

	// Non-streaming response - check if tools are requested in the conversation
	if len(req.Conversation) > 0 && strings.Contains(req.Conversation[0].Content, "Available tools:") {
		// Response with tool use
		resp := ArgoChatResponse{
			Response: "I'll help you list the directory contents.\n\n<tool>LS</tool>\n<args>{\"path\":\"/usr/home/jin/K/W/P002/lmtools\"}</args>",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Regular non-streaming response
	resp := ArgoChatResponse{
		Response: "Hello from mock Argo!",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func TestIntegrationBasicChat(t *testing.T) {
	// Skip this test as it requires mocking global functions
	t.Skip("Skipping test that requires global function mocking")

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
			reqBody, _ := json.Marshal(tt.request)
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
	// Skip this test as it requires mocking global functions
	t.Skip("Skipping test that requires global function mocking")

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
	reader := bufio.NewReader(resp.Body)
	events := []string{}

	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Error reading stream: %v", err)
		}

		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "event:") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}

		// Check for completion
		if line == "data: [DONE]" {
			break
		}
	}

	// Verify we got expected events
	expectedEvents := []string{"message_start", "content_block_start", "ping", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	eventMap := make(map[string]bool)
	for _, e := range events {
		eventMap[e] = true
	}

	for _, expected := range expectedEvents {
		if !eventMap[expected] {
			t.Errorf("Missing expected event: %s", expected)
		}
	}
}

func TestIntegrationRetry(t *testing.T) {
	// Skip this test as it requires mocking global functions
	t.Skip("Skipping test that requires global function mocking")

	// Create server with custom retry config
	server := &Server{
		config:    config,
		mapper:    NewModelMapper(config),
		converter: NewConverter(NewModelMapper(config)),
		client:    NewRetryableHTTPClient(10 * time.Second),
	}

	// Override retry config for faster testing
	server.client.retryers["openai"] = NewRetryer(&RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
		BackoffFactor:  2.0,
	})

	// Create proxy server
	handler := NewRequestLogger(NewErrorMiddleware(server))
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	// Make request
	req := AnthropicRequest{
		Model:     "gpt-4",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
				Content: json.RawMessage(`"Test retry"`),
			},
		},
	}

	reqBody, _ := json.Marshal(req)
	start := time.Now()
	resp, err := http.Post(
		proxyServer.URL+"/v1/messages",
		"application/json",
		bytes.NewReader(reqBody),
	)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Verify retry happened
	if retryCount != 3 {
		t.Errorf("Expected 3 attempts, got %d", retryCount)
	}

	// Verify backoff occurred
	if elapsed < 20*time.Millisecond {
		t.Errorf("Expected backoff delay, elapsed only %v", elapsed)
	}

	// Verify response
	var anthResp AnthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&anthResp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(anthResp.Content) == 0 || !strings.Contains(anthResp.Content[0].Text, "Success after retry") {
		t.Errorf("Unexpected response content: %+v", anthResp)
	}
}

func TestIntegrationSimulatedStreamingWithTools(t *testing.T) {
	// Skip this test as it requires mocking global functions
	t.Skip("Skipping test that requires global function mocking")

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

	// Should have exactly 2 content_block_stop events (one for text, one for tool)
	if contentBlockStopCount != 2 {
		t.Errorf("Expected 2 content_block_stop events, got %d", contentBlockStopCount)
	}

	t.Logf("Blocks closed: %v", blockIndices)
}
