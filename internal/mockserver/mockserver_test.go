package mockserver

import (
	"encoding/json"
	"lmtools/internal/config"
	"lmtools/internal/core"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMockServer_BasicChat(t *testing.T) {
	// Create mock server
	mock := NewMockServer(
		WithDefaultResponse("Hello from mock server!"),
	)
	defer mock.Close()

	// Create config pointing to mock server
	cfg := config.Config{
		ArgoUser: "testuser",
		Model:    "gpt4o",
		ArgoEnv:  mock.URL(),
		Timeout:  5 * time.Second,
	}

	// Make a chat request
	req, _, err := core.BuildRequest(cfg, "Test message")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	// Parse response
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify response - mock server returns contextual response for "test"
	expectedResponse := "This is a test response from the mock server."
	if result["response"] != expectedResponse {
		t.Errorf("Expected '%s', got %v", expectedResponse, result["response"])
	}

	// Check captured requests
	requests := mock.GetRequests()
	if len(requests) != 1 {
		t.Fatalf("Expected 1 request, got %d", len(requests))
	}

	lastReq := mock.GetLastRequest()
	if !strings.HasSuffix(lastReq.Path, "/chat/") {
		t.Errorf("Expected /chat/ endpoint, got %s", lastReq.Path)
	}
}

func TestMockServer_Embedding(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	cfg := config.Config{
		ArgoUser: "testuser",
		Model:    "v3large",
		Embed:    true,
		ArgoEnv:  mock.URL(),
		Timeout:  5 * time.Second,
	}

	req, _, err := core.BuildRequest(cfg, "Test embedding")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify embedding response (2D array)
	embedding, ok := result["embedding"].([]interface{})
	if !ok {
		t.Fatalf("Expected embedding array, got %T", result["embedding"])
	}

	if len(embedding) != 1 {
		t.Errorf("Expected 1 embedding vector, got %d", len(embedding))
	}

	// Check the inner array
	if len(embedding) > 0 {
		innerArray, ok := embedding[0].([]interface{})
		if !ok {
			t.Fatalf("Expected inner array, got %T", embedding[0])
		}
		if len(innerArray) != 1536 {
			t.Errorf("Expected 1536 embedding dimensions, got %d", len(innerArray))
		}
	}
}

func TestMockServer_StreamChat(t *testing.T) {
	mock := NewMockServer(
		WithDefaultResponse("Streaming response test"),
	)
	defer mock.Close()

	cfg := config.Config{
		ArgoUser:   "testuser",
		Model:      "gpt4o",
		StreamChat: true,
		ArgoEnv:    mock.URL(),
		Timeout:    5 * time.Second,
	}

	req, _, err := core.BuildRequest(cfg, "Test streaming")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	// Verify streaming response headers
	contentType := resp.Header.Get("Content-Type")
	if contentType != "text/plain" {
		t.Errorf("Expected text/plain, got %s", contentType)
	}
}

func TestMockServer_CustomResponse(t *testing.T) {
	mock := NewMockServer(
		WithResponseFunc(func(r *http.Request) (interface{}, int, error) {
			// Parse request to determine response
			var reqData map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&reqData); err != nil {
				return nil, 400, err
			}

			response := "Custom response"
			if messages, ok := reqData["messages"].([]interface{}); ok && len(messages) > 0 {
				lastMsg := messages[len(messages)-1]
				if msgMap, ok := lastMsg.(map[string]interface{}); ok {
					var content string
					// Handle both string content and content blocks
					switch c := msgMap["content"].(type) {
					case string:
						content = c
					case []interface{}:
						// Extract text from content blocks
						for _, block := range c {
							if blockMap, ok := block.(map[string]interface{}); ok {
								if blockMap["type"] == "text" {
									if text, ok := blockMap["text"].(string); ok {
										content = text
										break
									}
								}
							}
						}
					}
					if content != "" {
						response = "You said: " + content
					}
				}
			}

			return map[string]interface{}{
				"response": response,
				"custom":   true,
			}, 200, nil
		}),
	)
	defer mock.Close()

	cfg := config.Config{
		ArgoUser: "testuser",
		Model:    "gpt4o",
		ArgoEnv:  mock.URL(),
		Timeout:  5 * time.Second,
	}

	req, _, err := core.BuildRequest(cfg, "Hello custom")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify custom response
	if result["response"] != "You said: Hello custom" {
		t.Errorf("Expected 'You said: Hello custom', got %v", result["response"])
	}

	if result["custom"] != true {
		t.Errorf("Expected custom=true, got %v", result["custom"])
	}
}

func TestMockServer_ErrorSimulation(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	// Simulate a 500 error
	mock.SimulateError(500, "Internal server error")

	cfg := config.Config{
		ArgoUser: "testuser",
		Model:    "gpt4o",
		ArgoEnv:  mock.URL(),
		Timeout:  5 * time.Second,
	}

	req, _, err := core.BuildRequest(cfg, "This should fail")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 500 {
		t.Errorf("Expected status 500, got %d", resp.StatusCode)
	}
}

func TestMockServer_RequestCapture(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	cfg := config.Config{
		ArgoUser: "testuser",
		Model:    "gpt4o",
		System:   "You are a helpful assistant",
		ArgoEnv:  mock.URL(),
		Timeout:  5 * time.Second,
	}

	// Make multiple requests
	messages := []string{"First", "Second", "Third"}

	for _, msg := range messages {
		req, _, err := core.BuildRequest(cfg, msg)
		if err != nil {
			t.Fatalf("Failed to build request: %v", err)
		}

		client := &http.Client{Timeout: cfg.Timeout}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		resp.Body.Close()
	}

	// Verify all requests were captured
	requests := mock.GetRequests()
	if len(requests) != 3 {
		t.Fatalf("Expected 3 requests, got %d", len(requests))
	}

	// Verify request details
	for i, req := range requests {
		if !strings.Contains(req.Body, messages[i]) {
			t.Errorf("Request %d: expected body to contain %s", i, messages[i])
		}

		if req.Method != "POST" {
			t.Errorf("Request %d: expected POST, got %s", i, req.Method)
		}
	}

	// Test reset
	mock.Reset()
	if len(mock.GetRequests()) != 0 {
		t.Error("Expected 0 requests after reset")
	}
}
